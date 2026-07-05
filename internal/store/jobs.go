package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Job struct {
	ID         int64
	Type       string
	Payload    string
	Status     string
	Attempts   int
	RunAt      string
	StartedAt  *string
	FinishedAt *string
	Error      *string
}

type ListJobsOpts struct {
	Status string
	Limit  int
}

// QueueDepth counts jobs that are waiting or running — the number reported
// by /api/health.
func (s *Store) QueueDepth(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE status IN ('queued', 'running')`).Scan(&n)
	return n, err
}

// EnqueueJob inserts a job unless an identical one is already queued, in
// which case the existing job is returned. This keeps rescan storms and
// crash-recovery re-walks from piling up duplicate probe/reconcile jobs
// (payloads are canonical JSON from jobs.Manager, so string equality is
// exact). A job that is already *running* does not suppress the enqueue —
// it may be operating on stale state, so the new job must still run after
// it.
func (s *Store) EnqueueJob(ctx context.Context, typ, payload string) (Job, error) {
	return s.EnqueueJobAt(ctx, typ, payload, time.Now().UTC())
}

func (s *Store) EnqueueJobAt(ctx context.Context, typ, payload string, runAt time.Time) (Job, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (type, payload, run_at)
		SELECT ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM jobs WHERE type = ? AND payload = ? AND status = 'queued'
		)`, typ, payload, FormatTime(runAt), typ, payload)
	if err != nil {
		return Job{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Job{}, err
	} else if n == 0 {
		return scanJob(s.db.QueryRowContext(ctx, `
			SELECT `+jobCols+` FROM jobs
			WHERE type = ? AND payload = ? AND status = 'queued'
			ORDER BY id LIMIT 1`, typ, payload))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Job{}, err
	}
	return s.GetJob(ctx, id)
}

func (s *Store) GetJob(ctx context.Context, id int64) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
}

// ClaimNextJob orders by (run_at, id) rather than SPEC-BACKEND's literal
// ORDER BY id so backoff-rescheduled retries queue behind fresh work that
// was due earlier; ties (the common enqueue-now case) still claim in id
// order.
func (s *Store) ClaimNextJob(ctx context.Context) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `
		UPDATE jobs
		SET status = 'running', started_at = datetime('now'), finished_at = NULL, error = NULL
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'queued' AND run_at <= datetime('now')
			ORDER BY run_at, id
			LIMIT 1
		)
		RETURNING `+jobCols))
}

func (s *Store) MarkJobDone(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'done', finished_at = datetime('now'), error = NULL
		WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (s *Store) MarkJobFailed(ctx context.Context, id int64, attempts int, message string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'failed', attempts = ?, finished_at = datetime('now'), error = ?
		WHERE id = ?`, attempts, message, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (s *Store) RescheduleJob(ctx context.Context, id int64, attempts int, runAt time.Time, message string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'queued', attempts = ?, run_at = ?, started_at = NULL, finished_at = NULL, error = ?
		WHERE id = ?`, attempts, FormatTime(runAt), message, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (s *Store) RetryJob(ctx context.Context, id int64) (Job, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'queued', run_at = datetime('now'), started_at = NULL, finished_at = NULL, error = NULL
		WHERE id = ? AND status = 'failed'`, id)
	if err != nil {
		return Job{}, err
	}
	if err := requireRow(res); err != nil {
		if _, getErr := s.GetJob(ctx, id); getErr != nil {
			return Job{}, getErr
		}
		return Job{}, ErrConflict
	}
	return s.GetJob(ctx, id)
}

func (s *Store) ResetRunningJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'queued', started_at = NULL
		WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ListJobs(ctx context.Context, opts ListJobsOpts) ([]Job, error) {
	where := "1 = 1"
	args := []any{}
	if opts.Status != "" {
		where = "status = ?"
		args = append(args, opts.Status)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+jobCols+`
		FROM jobs
		WHERE `+where+`
		ORDER BY id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

const jobCols = `id, type, payload, status, attempts, run_at, started_at, finished_at, error`

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var startedAt, finishedAt, message sql.NullString
	err := row.Scan(&job.ID, &job.Type, &job.Payload, &job.Status, &job.Attempts,
		&job.RunAt, &startedAt, &finishedAt, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.String
	}
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.String
	}
	if message.Valid {
		job.Error = &message.String
	}
	return job, nil
}

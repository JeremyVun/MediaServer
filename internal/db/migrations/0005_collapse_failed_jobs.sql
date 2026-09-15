-- Before this release every re-enqueue of a failing job inserted a fresh row,
-- so a file that was still downloading (ffprobe rejects the preallocated,
-- half-written file) left thousands of 'failed' rows behind and the same
-- file's eventual success never cleared them. EnqueueJob now revives the
-- existing failed row instead, keeping one row per (type, payload). Bring
-- the table to that shape: drop failed rows superseded by a later success,
-- then keep only the newest failed row per payload.

DELETE FROM jobs
WHERE status = 'failed'
  AND EXISTS (
    SELECT 1 FROM jobs done
    WHERE done.type = jobs.type
      AND done.payload = jobs.payload
      AND done.status = 'done'
      AND done.id > jobs.id
  );

DELETE FROM jobs
WHERE status = 'failed'
  AND id < (
    SELECT MAX(newer.id) FROM jobs newer
    WHERE newer.type = jobs.type
      AND newer.payload = jobs.payload
      AND newer.status = 'failed'
  );

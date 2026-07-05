package watcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestWatcherMarksRootOffline(t *testing.T) {
	st, closeDB := newWatcherStore(t)
	defer closeDB()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movie.mp4",
		Size:        5,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	bus := events.NewBus()
	eventsCh, stopEvents := bus.Subscribe()
	defer stopEvents()
	jobs := newFakeJobs()
	streams := newFakeStreams()
	manager := NewManager(Options{
		Store:              st,
		Jobs:               jobs,
		Bus:                bus,
		Log:                slog.Default(),
		MountCheckInterval: 10 * time.Millisecond,
		StreamFactory:      streams.factory,
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	jobs.waitReconcile(t, root.ID)

	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	waitFor(t, func() bool {
		got, err := st.GetRoot(ctx, root.ID)
		return err == nil && !got.Online
	}, "root offline")
	gotFile, err := st.GetFile(ctx, file.ID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if gotFile.Status != "offline" {
		t.Fatalf("file status = %q", gotFile.Status)
	}
	ev := waitEvent(t, eventsCh, events.RootStatus)
	payload := ev.Payload.(map[string]any)
	if payload["id"] != root.ID || payload["online"] != false {
		t.Fatalf("root event payload = %#v", payload)
	}
}

func TestWatcherQuiescesAndEnqueuesProbe(t *testing.T) {
	st, closeDB := newWatcherStore(t)
	defer closeDB()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	mediaPath := filepath.Join(rootPath, "incoming", "movie.mp4")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(mediaPath, []byte("video bytes"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}

	jobs := newFakeJobs()
	streams := newFakeStreams()
	manager := NewManager(Options{
		Store:            st,
		Jobs:             jobs,
		Log:              slog.Default(),
		Debounce:         5 * time.Millisecond,
		QuiesceInterval:  5 * time.Millisecond,
		QuiesceStableFor: 15 * time.Millisecond,
		StreamFactory:    streams.factory,
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	jobs.waitReconcile(t, root.ID)
	streams.send(t, rootPath, streamEvent{Path: mediaPath})

	probe := jobs.waitProbe(t)
	if probe.rootID != root.ID || probe.relPath != "incoming/movie.mp4" {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestWatcherMoveDetectionPreservesFileIdentity(t *testing.T) {
	st, closeDB := newWatcherStore(t)
	defer closeDB()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	oldPath := filepath.Join(rootPath, "old.mp4")
	newPath := filepath.Join(rootPath, "folder", "new.mp4")
	if err := os.WriteFile(oldPath, []byte("same media bytes"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	fp, err := library.Fingerprint(oldPath)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "old.mp4",
		Size:        int64(len("same media bytes")),
		Mtime:       time.Now(),
		Fingerprint: fp,
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	jobs := newFakeJobs()
	streams := newFakeStreams()
	manager := NewManager(Options{
		Store:            st,
		Jobs:             jobs,
		Log:              slog.Default(),
		Debounce:         5 * time.Millisecond,
		QuiesceInterval:  5 * time.Millisecond,
		QuiesceStableFor: 15 * time.Millisecond,
		MoveWindow:       25 * time.Millisecond,
		StreamFactory:    streams.factory,
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	jobs.waitReconcile(t, root.ID)

	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("rename: %v", err)
	}
	streams.send(t, rootPath, streamEvent{Path: oldPath})
	streams.send(t, rootPath, streamEvent{Path: newPath})

	waitFor(t, func() bool {
		moved, err := st.GetFileByLocation(ctx, root.ID, "folder/new.mp4")
		return err == nil && moved.ID == file.ID
	}, "moved file row")
	if _, err := st.GetFileByLocation(ctx, root.ID, "old.mp4"); err != store.ErrNotFound {
		t.Fatalf("old location err = %v, want ErrNotFound", err)
	}
	select {
	case probe := <-jobs.probes:
		t.Fatalf("move enqueued probe unexpectedly: %+v", probe)
	default:
	}
}

func TestWatcherAttachAndDetachRootRuntime(t *testing.T) {
	st, closeDB := newWatcherStore(t)
	defer closeDB()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus := events.NewBus()
	eventsCh, stopEvents := bus.Subscribe()
	defer stopEvents()
	jobs := newFakeJobs()
	streams := newFakeStreams()
	manager := NewManager(Options{
		Store:              st,
		Jobs:               jobs,
		Bus:                bus,
		Log:                slog.Default(),
		MountCheckInterval: 10 * time.Millisecond,
		StreamFactory:      streams.factory,
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := manager.AttachRoot(ctx, root); err != nil {
		t.Fatalf("attach: %v", err)
	}
	jobs.waitReconcile(t, root.ID)
	streams.send(t, rootPath, streamEvent{Path: filepath.Join(rootPath, "movie.mp4")})
	ev := waitEvent(t, eventsCh, events.RootStatus)
	if payload := ev.Payload.(map[string]any); payload["id"] != root.ID || payload["online"] != true {
		t.Fatalf("attach event payload = %#v", payload)
	}

	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movie.mp4",
		Size:        5,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	if err := manager.DetachRoot(ctx, root); err != nil {
		t.Fatalf("detach: %v", err)
	}
	gotRoot, err := st.GetRoot(ctx, root.ID)
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	if gotRoot.Attached || gotRoot.Online {
		t.Fatalf("detached root = %+v", gotRoot)
	}
	gotFile, err := st.GetFile(ctx, file.ID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if gotFile.Status != "offline" {
		t.Fatalf("file status = %q", gotFile.Status)
	}
	ev = waitEvent(t, eventsCh, events.RootStatus)
	if payload := ev.Payload.(map[string]any); payload["id"] != root.ID || payload["online"] != false {
		t.Fatalf("detach event payload = %#v", payload)
	}
}

func newWatcherStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "watcher.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(sqldb); err != nil {
		_ = sqldb.Close()
		t.Fatalf("migrate: %v", err)
	}
	return store.New(sqldb), func() { _ = sqldb.Close() }
}

type fakeJobs struct {
	reconciles chan int64
	probes     chan fakeProbe
}

type fakeProbe struct {
	rootID  int64
	relPath string
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{
		reconciles: make(chan int64, 16),
		probes:     make(chan fakeProbe, 16),
	}
}

func (j *fakeJobs) EnqueueReconcile(_ context.Context, rootID int64) (store.Job, error) {
	j.reconciles <- rootID
	return store.Job{ID: rootID, Type: "reconcile"}, nil
}

func (j *fakeJobs) EnqueueProbe(_ context.Context, rootID int64, relPath string) (store.Job, error) {
	j.probes <- fakeProbe{rootID: rootID, relPath: relPath}
	return store.Job{ID: rootID, Type: "probe"}, nil
}

func (j *fakeJobs) waitReconcile(t *testing.T, rootID int64) {
	t.Helper()
	select {
	case got := <-j.reconciles:
		if got != rootID {
			t.Fatalf("reconcile root = %d, want %d", got, rootID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reconcile")
	}
}

func (j *fakeJobs) waitProbe(t *testing.T) fakeProbe {
	t.Helper()
	select {
	case probe := <-j.probes:
		return probe
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for probe")
	}
	return fakeProbe{}
}

type fakeStreams struct {
	mu      sync.Mutex
	streams map[string]chan streamEvent
}

func newFakeStreams() *fakeStreams {
	return &fakeStreams{streams: make(map[string]chan streamEvent)}
}

func (s *fakeStreams) factory(ctx context.Context, path string, _ time.Duration, _ *slog.Logger) (<-chan streamEvent, error) {
	ch := make(chan streamEvent, 16)
	s.mu.Lock()
	s.streams[path] = ch
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func (s *fakeStreams) send(t *testing.T, path string, ev streamEvent) {
	t.Helper()
	var ch chan streamEvent
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ch = s.streams[path]
		return ch != nil
	}, "stream "+path)
	select {
	case ch <- ev:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending stream event")
	}
}

func waitFor(t *testing.T, fn func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

func waitEvent(t *testing.T, ch <-chan events.Event, typ string) events.Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %s", typ)
		}
	}
}

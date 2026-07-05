package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestEventsStreamSerializesBusEvents(t *testing.T) {
	srv, _ := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	rec := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(rec, req)
		close(done)
	}()

	if chunk := rec.waitChunk(t); chunk != ": connected\n\n" {
		t.Fatalf("connected chunk = %q", chunk)
	}
	if rec.statusCode() != http.StatusOK {
		t.Fatalf("status=%d", rec.statusCode())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type = %q", got)
	}

	year := 2008
	srv.bus.Publish(events.Event{Type: events.ItemAdded, Payload: store.ItemSummary{
		ID:            12,
		Type:          "movie",
		Title:         "Big Buck Bunny",
		Year:          &year,
		Available:     true,
		CollectionIDs: []int64{3},
	}})

	event := parseSSEBlock(rec.waitChunk(t))
	if event["event"] != events.ItemAdded || event["id"] == "" {
		t.Fatalf("event block = %+v", event)
	}
	var item itemSummaryResponse
	if err := json.Unmarshal([]byte(event["data"]), &item); err != nil {
		t.Fatalf("decode item payload: %v", err)
	}
	if item.ID != 12 || item.Title != "Big Buck Bunny" || item.ThumbURL != "/api/items/12/thumb" || !item.Available {
		t.Fatalf("item payload = %+v", item)
	}

	// file.status is internal-bus-only (SPEC-API's SSE contract doesn't
	// include it): publish one, then a root.status — the next chunk on the
	// wire must be the root event, proving file.status was skipped.
	srv.bus.Publish(events.Event{
		Type:    events.FileStatus,
		Payload: map[string]any{"file_id": int64(30), "status": "missing"},
	})
	srv.bus.Publish(events.Event{
		Type:    events.RootStatus,
		Payload: map[string]any{"root_id": int64(2), "online": false},
	})
	event = parseSSEBlock(rec.waitChunk(t))
	if event["event"] != events.RootStatus {
		t.Fatalf("root event block = %+v (file.status must not reach the SSE wire)", event)
	}
	var root struct {
		ID     int64 `json:"id"`
		Online bool  `json:"online"`
	}
	if err := json.Unmarshal([]byte(event["data"]), &root); err != nil {
		t.Fatalf("decode root payload: %v", err)
	}
	if root.ID != 2 || root.Online {
		t.Fatalf("root payload = %+v", root)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event handler did not stop after request cancellation")
	}
}

type streamRecorder struct {
	header http.Header
	chunks chan string

	mu     sync.Mutex
	status int
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{
		header: make(http.Header),
		chunks: make(chan string, 16),
		status: http.StatusOK,
	}
}

func (r *streamRecorder) Header() http.Header {
	return r.header
}

func (r *streamRecorder) WriteHeader(status int) {
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.chunks <- string(p)
	return len(p), nil
}

func (r *streamRecorder) Flush() {}

func (r *streamRecorder) statusCode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *streamRecorder) waitChunk(t *testing.T) string {
	t.Helper()
	select {
	case chunk := <-r.chunks:
		return chunk
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream chunk")
	}
	return ""
}

func parseSSEBlock(chunk string) map[string]string {
	block := map[string]string{}
	for _, line := range strings.Split(chunk, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		block[key] = strings.TrimPrefix(value, " ")
	}
	return block
}

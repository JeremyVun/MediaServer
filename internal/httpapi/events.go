package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

const sseHeartbeatInterval = 25 * time.Second

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.bus == nil {
		writeError(w, http.StatusServiceUnavailable, "events_unavailable", "event stream is not available")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := s.bus.Subscribe()
	defer cancel()

	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}
			// file.status is an internal bus event (SPEC-BACKEND); SPEC-API's
			// SSE contract doesn't include it. Clients get the corresponding
			// item.updated, which every file.status publisher pairs it with.
			if event.Type == events.FileStatus {
				continue
			}
			if err := s.writeSSE(w, event); err != nil {
				s.log.Warn("write sse event", "type", event.Type, "error", err)
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) writeSSE(w http.ResponseWriter, event events.Event) error {
	payload, err := s.ssePayload(event)
	if err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	id := s.eventSeq.Add(1)
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, event.Type, data); err != nil {
		return err
	}
	return nil
}

func (s *Server) ssePayload(event events.Event) (any, error) {
	switch event.Type {
	case events.ItemAdded, events.ItemUpdated:
		switch payload := event.Payload.(type) {
		case store.ItemSummary:
			return s.itemSummaries([]store.ItemSummary{payload})[0], nil
		case itemSummaryResponse:
			return payload, nil
		default:
			return nil, fmt.Errorf("unexpected item payload %T", event.Payload)
		}
	case events.ItemRemoved:
		id, ok := eventPayloadInt(event.Payload, "id")
		if !ok {
			return nil, fmt.Errorf("unexpected item.removed payload %T", event.Payload)
		}
		return map[string]int64{"id": id}, nil
	case events.RootStatus:
		id, ok := eventPayloadInt(event.Payload, "id", "root_id")
		if !ok {
			return nil, fmt.Errorf("unexpected root.status id payload %T", event.Payload)
		}
		online, ok := eventPayloadBool(event.Payload, "online")
		if !ok {
			return nil, fmt.Errorf("unexpected root.status online payload %T", event.Payload)
		}
		return map[string]any{"id": id, "online": online}, nil
	case events.UploadProgress, events.UploadComplete, events.JobProgress:
		return event.Payload, nil
	default:
		return event.Payload, nil
	}
}

func eventPayloadInt(payload any, keys ...string) (int64, bool) {
	switch v := payload.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case map[string]any:
		for _, key := range keys {
			if n, ok := eventPayloadInt(v[key]); ok {
				return n, true
			}
		}
	case map[string]int64:
		for _, key := range keys {
			if n, ok := v[key]; ok {
				return n, true
			}
		}
	case map[string]int:
		for _, key := range keys {
			if n, ok := v[key]; ok {
				return int64(n), true
			}
		}
	case map[string]string:
		for _, key := range keys {
			if raw, ok := v[key]; ok {
				n, err := strconv.ParseInt(raw, 10, 64)
				return n, err == nil
			}
		}
	}
	return 0, false
}

func eventPayloadBool(payload any, keys ...string) (bool, bool) {
	switch v := payload.(type) {
	case bool:
		return v, true
	case map[string]any:
		for _, key := range keys {
			if b, ok := eventPayloadBool(v[key]); ok {
				return b, true
			}
		}
	case map[string]bool:
		for _, key := range keys {
			if b, ok := v[key]; ok {
				return b, true
			}
		}
	case map[string]string:
		for _, key := range keys {
			if raw, ok := v[key]; ok {
				b, err := strconv.ParseBool(raw)
				return b, err == nil
			}
		}
	}
	return false, false
}

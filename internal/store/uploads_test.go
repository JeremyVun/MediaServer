package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestUploadLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root, err := s.UpsertRoot(ctx, "Media A", "/Volumes/Media-A")
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	checksum := "0123456789abcdef"
	upload, err := s.CreateUpload(ctx, store.NewUpload{
		ID:           "upload-1",
		Filename:     "movie.mp4",
		Size:         12,
		RootID:       root.ID,
		ChecksumXXH3: &checksum,
	})
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	if upload.Received != 0 || upload.Status != "active" || upload.ChecksumXXH3 == nil || *upload.ChecksumXXH3 != checksum {
		t.Fatalf("upload = %+v", upload)
	}

	upload, err = s.UpdateUploadReceived(ctx, upload.ID, 0, 6)
	if err != nil {
		t.Fatalf("update received: %v", err)
	}
	if upload.Received != 6 {
		t.Fatalf("received = %d, want 6", upload.Received)
	}
	if _, err := s.UpdateUploadReceived(ctx, upload.ID, 0, 12); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale offset error = %v, want ErrConflict", err)
	}

	upload, err = s.MarkUploadComplete(ctx, upload.ID, "incoming/movie.mp4")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if upload.Status != "complete" || upload.RelPath == nil || *upload.RelPath != "incoming/movie.mp4" {
		t.Fatalf("completed upload = %+v", upload)
	}
	if _, err := s.AbortUpload(ctx, upload.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("abort complete error = %v, want ErrConflict", err)
	}
}

func TestAttachUploadItemReturnsOnlyNewHandoffs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root, err := s.UpsertRoot(ctx, "Media A", "/Volumes/Media-A")
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	upload, err := s.CreateUpload(ctx, store.NewUpload{
		ID:       "upload-1",
		Filename: "movie.mp4",
		Size:     12,
		RootID:   root.ID,
	})
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	if _, err := s.MarkUploadComplete(ctx, upload.ID, "incoming/movie.mp4"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	item, err := s.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}

	uploads, err := s.AttachUploadItem(ctx, root.ID, "incoming/movie.mp4", item.ID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if len(uploads) != 1 || uploads[0].ID != upload.ID || uploads[0].ItemID == nil || *uploads[0].ItemID != item.ID {
		t.Fatalf("handoffs = %+v", uploads)
	}
	uploads, err = s.AttachUploadItem(ctx, root.ID, "incoming/movie.mp4", item.ID)
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if len(uploads) != 0 {
		t.Fatalf("second handoffs = %+v, want none", uploads)
	}
}

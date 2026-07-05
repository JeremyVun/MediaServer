package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/zeebo/xxh3"
)

const (
	uploadChunkSize   = 32 * 1024 * 1024
	uploadIncomingDir = "incoming"
	uploadPartDir     = ".uploads"
	uploadIgnoreHold  = 15 * time.Second
	// uploadIOBufSize sizes the buffer between the request body and the part
	// file (and the checksum read on complete). Socket reads arrive in small
	// pieces (4-16 KB over Wi-Fi), and on external drives — exFAT via fskit
	// especially — every write() pays a fixed per-call cost that caps
	// throughput far below the disk's sequential speed (measured 16x slower
	// at 32 KB writes vs 4 MB writes). Buffering decouples disk-write size
	// from socket-read size.
	uploadIOBufSize = 4 * 1024 * 1024
)

type createUploadRequest struct {
	Filename     string  `json:"filename"`
	Size         int64   `json:"size"`
	RootID       int64   `json:"root_id"`
	ChecksumXXH3 *string `json:"checksum_xxh3"`
}

type createUploadResponse struct {
	ID        string `json:"id"`
	ChunkSize int64  `json:"chunk_size"`
}

type uploadStatusResponse struct {
	Received  int64  `json:"received"`
	Size      int64  `json:"size"`
	Status    string `json:"status"`
	ChunkSize int64  `json:"chunk_size"`
}

type uploadChunkResponse struct {
	Received int64 `json:"received"`
}

type uploadCompleteResponse struct {
	ItemID *int64 `json:"item_id"`
}

type uploadOffsetError struct {
	Error    apiError `json:"error"`
	Expected int64    `json:"expected"`
}

func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	var req createUploadRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid upload")
		return
	}
	filename, ok := cleanUploadFilename(req.Filename)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_filename", "filename is not a supported media file")
		return
	}
	if req.Size <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_size", "size must be positive")
		return
	}
	checksum, ok := cleanUploadChecksum(req.ChecksumXXH3)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_checksum", "checksum_xxh3 must be 16 lowercase hex characters")
		return
	}
	root, ok := s.uploadRoot(w, r, req.RootID)
	if !ok {
		return
	}
	free, err := freeBytes(root.Path)
	if err != nil {
		writeError(w, http.StatusConflict, "root_offline", "root path is not mounted")
		return
	}
	if free < requiredUploadBytes(req.Size, s.uploadMinGB) {
		writeError(w, http.StatusInsufficientStorage, "insufficient_storage", "target root does not have enough free space")
		return
	}

	id, err := randomUploadID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "creating upload failed")
		s.log.Error("generate upload id", "error", err)
		return
	}
	partPath := uploadPartPath(root.Path, id)
	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "creating upload directory failed")
		s.log.Error("create upload dir", "root_id", root.ID, "error", err)
		return
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "creating upload part failed")
		s.log.Error("create upload part", "path", partPath, "error", err)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partPath)
		writeError(w, http.StatusInternalServerError, "internal", "creating upload part failed")
		s.log.Error("close upload part", "path", partPath, "error", err)
		return
	}

	if _, err := s.store.CreateUpload(r.Context(), store.NewUpload{
		ID:           id,
		Filename:     filename,
		Size:         req.Size,
		RootID:       root.ID,
		ChecksumXXH3: checksum,
	}); err != nil {
		_ = os.Remove(partPath)
		writeError(w, http.StatusInternalServerError, "internal", "saving upload failed")
		s.log.Error("create upload row", "upload_id", id, "error", err)
		return
	}
	writeJSON(w, http.StatusCreated, createUploadResponse{ID: id, ChunkSize: uploadChunkSize})
}

func (s *Server) handleGetUpload(w http.ResponseWriter, r *http.Request) {
	upload, err := s.store.GetUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if upload.Status == "active" {
		root, ok := s.uploadRoot(w, r, upload.RootID)
		if !ok {
			return
		}
		upload, err = s.reconcilePartFile(r.Context(), upload, uploadPartPath(root.Path, upload.ID))
		if err != nil {
			writeError(w, http.StatusConflict, "upload_part_missing", "upload part file is missing")
			s.log.Warn("reconcile upload part", "upload_id", upload.ID, "error", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, uploadStatus(upload))
}

func (s *Server) handlePutUploadChunk(w http.ResponseWriter, r *http.Request) {
	upload, err := s.store.GetUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if upload.Status != "active" {
		writeError(w, http.StatusConflict, "upload_not_active", "upload is not active")
		return
	}
	root, ok := s.uploadRoot(w, r, upload.RootID)
	if !ok {
		return
	}
	start, end, total, ok := parseContentRange(r.Header.Get("Content-Range"))
	if !ok || total != upload.Size {
		writeError(w, http.StatusBadRequest, "bad_content_range", "Content-Range must match the upload size")
		return
	}
	if start != upload.Received {
		writeOffsetMismatch(w, upload.Received)
		return
	}
	chunkLen := end - start + 1
	if chunkLen <= 0 || chunkLen > uploadChunkSize || end >= upload.Size {
		writeError(w, http.StatusBadRequest, "bad_content_range", "invalid upload chunk range")
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != chunkLen {
		writeError(w, http.StatusBadRequest, "bad_chunk_size", "request body length must match Content-Range")
		return
	}

	partPath := uploadPartPath(root.Path, upload.ID)
	release := s.ignorePath(partPath)
	defer release()
	received, err := writeUploadChunk(partPath, upload.Received, chunkLen, r.Body, r.ContentLength)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_chunk", "upload chunk could not be written")
		s.log.Warn("write upload chunk", "upload_id", upload.ID, "error", err)
		return
	}
	updated, err := s.store.UpdateUploadReceived(r.Context(), upload.ID, upload.Received, received)
	if errors.Is(err, store.ErrConflict) {
		latest, latestErr := s.store.GetUpload(r.Context(), upload.ID)
		if latestErr != nil {
			writeStoreError(w, latestErr)
			return
		}
		writeOffsetMismatch(w, latest.Received)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "saving upload offset failed")
		s.log.Error("update upload offset", "upload_id", upload.ID, "error", err)
		return
	}
	s.publishUploadProgress(updated)
	writeJSON(w, http.StatusOK, uploadChunkResponse{Received: updated.Received})
}

func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	upload, err := s.store.GetUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if upload.Status == "complete" {
		writeJSON(w, http.StatusOK, uploadCompleteResponse{ItemID: upload.ItemID})
		return
	}
	if upload.Status != "active" {
		writeError(w, http.StatusConflict, "upload_not_active", "upload is not active")
		return
	}
	if upload.Received != upload.Size {
		writeError(w, http.StatusConflict, "upload_incomplete", "upload has not received all bytes")
		return
	}
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs_unavailable", "job manager is not running")
		return
	}
	root, ok := s.uploadRoot(w, r, upload.RootID)
	if !ok {
		return
	}
	partPath := uploadPartPath(root.Path, upload.ID)
	info, err := os.Stat(partPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != upload.Size {
		writeError(w, http.StatusConflict, "upload_part_missing", "upload part file is incomplete")
		return
	}
	if err := syncFile(partPath); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "syncing upload part failed")
		s.log.Error("sync upload part", "upload_id", upload.ID, "error", err)
		return
	}
	if upload.ChecksumXXH3 != nil {
		got, err := wholeFileXXH3(partPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "checking upload checksum failed")
			s.log.Error("checksum upload", "upload_id", upload.ID, "error", err)
			return
		}
		if got != *upload.ChecksumXXH3 {
			writeError(w, http.StatusConflict, "checksum_mismatch", "upload checksum did not match")
			return
		}
	}

	finalPath, relPath, err := s.moveCompletedUpload(root.Path, upload.Filename, partPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "finalizing upload failed")
		s.log.Error("finalize upload", "upload_id", upload.ID, "error", err)
		return
	}
	completed, err := s.store.MarkUploadComplete(r.Context(), upload.ID, relPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "saving upload completion failed")
		s.log.Error("mark upload complete", "upload_id", upload.ID, "rel_path", relPath, "error", err)
		return
	}
	s.publishUploadProgress(completed)
	s.publishUploadComplete(completed.ID, nil)
	if _, err := s.jobs.EnqueueProbe(r.Context(), root.ID, relPath); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "enqueue upload probe failed")
		s.log.Error("enqueue upload probe", "upload_id", upload.ID, "path", finalPath, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, uploadCompleteResponse{ItemID: nil})
}

func (s *Server) handleAbortUpload(w http.ResponseWriter, r *http.Request) {
	upload, err := s.store.GetUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if upload.Status == "complete" {
		writeError(w, http.StatusConflict, "upload_complete", "completed uploads cannot be aborted")
		return
	}
	if upload.Status == "active" {
		root, ok := s.uploadRoot(w, r, upload.RootID)
		if !ok {
			return
		}
		aborted, err := s.store.AbortUpload(r.Context(), upload.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "aborting upload failed")
			s.log.Error("abort upload", "upload_id", upload.ID, "error", err)
			return
		}
		release := s.ignorePath(uploadPartPath(root.Path, upload.ID))
		_ = os.Remove(uploadPartPath(root.Path, upload.ID))
		release()
		s.publishUploadProgress(aborted)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadRoot(w http.ResponseWriter, r *http.Request, rootID int64) (store.Root, bool) {
	root, err := s.store.GetRoot(r.Context(), rootID)
	if err != nil {
		writeStoreError(w, err)
		return store.Root{}, false
	}
	if !root.Attached || !root.Online {
		writeError(w, http.StatusConflict, "root_offline", "root is offline")
		return store.Root{}, false
	}
	info, err := os.Stat(root.Path)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusConflict, "root_offline", "root path is not mounted")
		return store.Root{}, false
	}
	return root, true
}

func (s *Server) moveCompletedUpload(rootPath, filename, partPath string) (string, string, error) {
	s.uploadRename.Lock()
	defer s.uploadRename.Unlock()

	incoming := filepath.Join(rootPath, uploadIncomingDir)
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		return "", "", err
	}
	partRelease := s.ignorePath(partPath)
	defer partRelease()
	for n := 1; n < 10000; n++ {
		name := collisionName(filename, n)
		finalPath := filepath.Join(incoming, name)
		if _, err := os.Stat(finalPath); err == nil {
			continue
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		finalRelease := s.ignorePath(finalPath)
		if err := os.Rename(partPath, finalPath); err != nil {
			finalRelease()
			return "", "", err
		}
		holdIgnore(finalRelease)
		relPath := filepath.ToSlash(filepath.Join(uploadIncomingDir, name))
		return finalPath, relPath, nil
	}
	return "", "", fmt.Errorf("too many filename collisions for %q", filename)
}

func (s *Server) ignorePath(path string) func() {
	if s.uploadIgnore == nil {
		return func() {}
	}
	return s.uploadIgnore.Add(path)
}

func (s *Server) publishUploadProgress(upload store.Upload) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type: events.UploadProgress,
		Payload: map[string]any{
			"id":       upload.ID,
			"received": upload.Received,
			"size":     upload.Size,
		},
	})
}

func (s *Server) publishUploadComplete(id string, itemID *int64) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type: events.UploadComplete,
		Payload: map[string]any{
			"id":      id,
			"item_id": itemID,
		},
	})
}

func uploadStatus(upload store.Upload) uploadStatusResponse {
	return uploadStatusResponse{
		Received:  upload.Received,
		Size:      upload.Size,
		Status:    upload.Status,
		ChunkSize: uploadChunkSize,
	}
}

func writeUploadChunk(path string, offset, length int64, body io.Reader, contentLength int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if err := f.Truncate(offset); err != nil {
		return offset, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	bw := bufio.NewWriterSize(f, uploadIOBufSize)
	n, err := io.CopyN(bw, body, length)
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		_ = f.Truncate(offset)
		return offset, err
	}
	if contentLength < 0 {
		var extra [1]byte
		if n, _ := body.Read(extra[:]); n > 0 {
			_ = f.Truncate(offset)
			return offset, fmt.Errorf("chunk body exceeded Content-Range")
		}
	}
	// No per-chunk fsync: on macOS Sync issues F_FULLFSYNC, which dominates
	// upload latency. The part file is synced once in handleCompleteUpload,
	// and resume reconciles received against the on-disk size.
	return offset + n, nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// reconcilePartFile aligns the upload's received offset with the part file
// before a client resumes. A part longer than received (chunk written but
// offset update lost) is truncated down; a part shorter than received (bytes
// lost to a crash before they hit disk, now that chunks are not fsynced) has
// received clamped down instead — never zero-extend the file.
func (s *Server) reconcilePartFile(ctx context.Context, upload store.Upload, path string) (store.Upload, error) {
	info, err := os.Stat(path)
	if err != nil {
		return upload, err
	}
	if !info.Mode().IsRegular() {
		return upload, fmt.Errorf("%s is not a regular file", path)
	}
	switch {
	case info.Size() > upload.Received:
		release := s.ignorePath(path)
		defer release()
		if err := os.Truncate(path, upload.Received); err != nil {
			return upload, err
		}
	case info.Size() < upload.Received:
		clamped, err := s.store.UpdateUploadReceived(ctx, upload.ID, upload.Received, info.Size())
		if err != nil {
			return upload, err
		}
		return clamped, nil
	}
	return upload, nil
}

func parseContentRange(header string) (int64, int64, int64, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes ")
	if !ok {
		return 0, 0, 0, false
	}
	rangePart, totalPart, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, 0, 0, false
	}
	startPart, endPart, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, 0, 0, false
	}
	start, err := strconv.ParseInt(startPart, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	end, err := strconv.ParseInt(endPart, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	total, err := strconv.ParseInt(totalPart, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return start, end, total, start >= 0 && end >= start && total > 0 && end < total
}

func uploadPartPath(rootPath, id string) string {
	return filepath.Join(rootPath, uploadIncomingDir, uploadPartDir, id+".part")
}

func cleanUploadFilename(filename string) (string, bool) {
	filename = strings.TrimSpace(strings.ReplaceAll(filename, "\\", "/"))
	filename = path.Base(filename)
	if filename == "" || filename == "." || filename == ".." || strings.HasPrefix(filename, ".") {
		return "", false
	}
	if strings.Contains(filename, "/") || strings.ContainsRune(filename, filepath.Separator) {
		return "", false
	}
	if !isUploadVideoName(filename) {
		return "", false
	}
	return filename, true
}

func cleanUploadChecksum(checksum *string) (*string, bool) {
	if checksum == nil {
		return nil, true
	}
	cleaned := strings.ToLower(strings.TrimSpace(*checksum))
	if len(cleaned) != 16 {
		return nil, false
	}
	if _, err := strconv.ParseUint(cleaned, 16, 64); err != nil {
		return nil, false
	}
	return &cleaned, true
}

func isUploadVideoName(name string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "mp4", "m4v", "mov", "mkv", "webm", "avi", "ts", "m2ts", "wmv", "flv":
		return true
	default:
		return false
	}
}

func collisionName(filename string, n int) string {
	if n <= 1 {
		return filename
	}
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	return fmt.Sprintf("%s (%d)%s", base, n, ext)
}

func requiredUploadBytes(size int64, minFreeGB float64) uint64 {
	if minFreeGB <= 0 {
		return uint64(size)
	}
	minFree := minFreeGB * 1024 * 1024 * 1024
	return uint64(size) + uint64(minFree)
}

func randomUploadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func wholeFileXXH3(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := xxh3.New()
	// Large reads for the same reason writeUploadChunk buffers writes: per-call
	// filesystem overhead on external drives dwarfs the copy itself at 32 KB.
	if _, err := io.Copy(h, bufio.NewReaderSize(f, uploadIOBufSize)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x", h.Sum64()), nil
}

func writeOffsetMismatch(w http.ResponseWriter, expected int64) {
	writeJSON(w, http.StatusConflict, uploadOffsetError{
		Error:    apiError{Code: "offset_mismatch", Message: "upload offset did not match"},
		Expected: expected,
	})
}

func holdIgnore(release func()) {
	go func() {
		time.Sleep(uploadIgnoreHold)
		release()
	}()
}

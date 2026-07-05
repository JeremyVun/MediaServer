package library

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/zeebo/xxh3"
)

const fingerprintChunk = 64 * 1024

// Fingerprint returns xxh3(first 64KiB + last 64KiB + size) as hex.
func Fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}

	h := xxh3.New()
	first := make([]byte, minInt64(fingerprintChunk, info.Size()))
	if _, err := io.ReadFull(f, first); err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	h.Write(first)

	if info.Size() > fingerprintChunk {
		tailSize := minInt64(fingerprintChunk, info.Size())
		tail := make([]byte, tailSize)
		if _, err := f.Seek(-tailSize, io.SeekEnd); err != nil {
			return "", err
		}
		if _, err := io.ReadFull(f, tail); err != nil && err != io.ErrUnexpectedEOF {
			return "", err
		}
		h.Write(tail)
	}

	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(info.Size()))
	h.Write(size[:])
	return fmt.Sprintf("%016x", h.Sum64()), nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

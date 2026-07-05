package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintUsesHeadTailAndSize(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")

	data := make([]byte, fingerprintChunk*3)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := os.WriteFile(a, data, 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	copyData := append([]byte(nil), data...)
	copyData[fingerprintChunk+10] ^= 0xff
	if err := os.WriteFile(b, copyData, 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	fpa, err := Fingerprint(a)
	if err != nil {
		t.Fatalf("fingerprint a: %v", err)
	}
	fpb, err := Fingerprint(b)
	if err != nil {
		t.Fatalf("fingerprint b: %v", err)
	}
	if fpa != fpb {
		t.Fatalf("middle-only change affected fingerprint: %s != %s", fpa, fpb)
	}

	copyData[len(copyData)-1] ^= 0xff
	if err := os.WriteFile(b, copyData, 0o644); err != nil {
		t.Fatalf("rewrite b: %v", err)
	}
	fpb, err = Fingerprint(b)
	if err != nil {
		t.Fatalf("fingerprint b tail: %v", err)
	}
	if fpa == fpb {
		t.Fatal("tail change did not affect fingerprint")
	}
}

// Files at or below one chunk hash their whole content once (no duplicate
// tail read) — the documented small-file path.
func TestFingerprintSmallFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	small := make([]byte, 1024)
	for i := range small {
		small[i] = byte(i % 7)
	}
	a := write("a.bin", small)
	b := write("b.bin", append([]byte(nil), small...))

	fpa, err := Fingerprint(a)
	if err != nil {
		t.Fatalf("fingerprint a: %v", err)
	}
	fpb, err := Fingerprint(b)
	if err != nil {
		t.Fatalf("fingerprint b: %v", err)
	}
	if fpa != fpb {
		t.Fatalf("identical small files differ: %s != %s", fpa, fpb)
	}

	changed := append([]byte(nil), small...)
	changed[512] ^= 0xff
	c := write("c.bin", changed)
	fpc, err := Fingerprint(c)
	if err != nil {
		t.Fatalf("fingerprint c: %v", err)
	}
	if fpa == fpc {
		t.Fatal("content change in a small file did not affect fingerprint")
	}

	// Same content, different length → different fingerprint (size is hashed).
	d := write("d.bin", small[:1000])
	fpd, err := Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint d: %v", err)
	}
	if fpd == fpa {
		t.Fatal("size change did not affect fingerprint")
	}
}

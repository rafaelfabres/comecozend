package patch

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The streaming path has to agree with the in-memory one byte for byte.
// It exists only because a PlayStation track is 600 MB and holding the
// source and the target at once was killing the app; a version that
// produced different output would be worse than the crash.
func TestApplyFileMatchesInMemory(t *testing.T) {
	for _, name := range []string{"edits.bps", "grow.bps", "moved.bps", "ipsbasic.ips"} {
		t.Run(name, func(t *testing.T) {
			stem := name[:len(name)-4]
			src := readFile(t, stem+".src")
			p := readFile(t, name)

			want, err := Apply(p, src)
			if err != nil {
				t.Fatalf("in-memory Apply: %v", err)
			}

			dir := t.TempDir()
			srcPath := filepath.Join(dir, "src.bin")
			dstPath := filepath.Join(dir, "dst.bin")
			if err := os.WriteFile(srcPath, src, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := ApplyFile(p, srcPath, dstPath, nil); err != nil {
				t.Fatalf("ApplyFile: %v", err)
			}
			got, err := os.ReadFile(dstPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("streaming output differs: %d bytes vs %d", len(got), len(want))
			}
		})
	}
}

// A failed patch must not leave a half-written image behind, or the next
// run would find a file that looks installed.
func TestApplyFileCleansUpOnFailure(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.bin")
	dstPath := filepath.Join(dir, "dst.bin")

	src := readFile(t, "edits.src")
	wrong := make([]byte, len(src))
	copy(wrong, src)
	wrong[0] ^= 0xff
	if err := os.WriteFile(srcPath, wrong, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ApplyFile(readFile(t, "edits.bps"), srcPath, dstPath, nil); err == nil {
		t.Fatal("expected a CRC mismatch")
	}
	if _, err := os.Stat(dstPath); err == nil {
		t.Error("a partial file was left behind")
	}
}

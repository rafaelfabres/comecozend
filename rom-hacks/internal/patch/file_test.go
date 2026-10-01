package patch

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// bpsBuilder writes BPS actions and computes the target they produce, so
// a test can describe a patch by its actions rather than ship a fixture.
type bpsBuilder struct {
	src, target, actions []byte
	srcRel, dstRel       int64
}

func (b *bpsBuilder) targetRead(data []byte) {
	b.actions = append(b.actions, bpsNumber(uint64(len(data)-1)<<2|1)...)
	b.actions = append(b.actions, data...)
	b.target = append(b.target, data...)
}

// targetCopy copies length bytes from target offset from (may overlap).
func (b *bpsBuilder) targetCopy(from int64, length int) {
	off := from - b.dstRel
	enc := uint64(off) << 1
	if off < 0 {
		enc = uint64(-off)<<1 | 1
	}
	b.actions = append(b.actions, bpsNumber(uint64(length-1)<<2|3)...)
	b.actions = append(b.actions, bpsNumber(enc)...)
	for i := 0; i < length; i++ {
		b.target = append(b.target, b.target[from+int64(i)])
	}
	b.dstRel = from + int64(length)
}

func (b *bpsBuilder) patch() []byte {
	body := append([]byte("BPS1"), bpsNumber(uint64(len(b.src)))...)
	body = append(body, bpsNumber(uint64(len(b.target)))...)
	body = append(body, bpsNumber(0)...)
	body = append(body, b.actions...)
	footer := make([]byte, 12)
	binary.LittleEndian.PutUint32(footer[0:], crc32.ChecksumIEEE(b.src))
	binary.LittleEndian.PutUint32(footer[4:], crc32.ChecksumIEEE(b.target))
	body = append(body, footer...)
	binary.LittleEndian.PutUint32(body[len(body)-4:], crc32.ChecksumIEEE(body[:len(body)-4]))
	return body
}

// Run fills (period 1), short repeating patterns, plain back-references
// and a period longer than the copy buffer must all stream to exactly
// what the in-memory patcher builds — and fast: these used to be one
// syscall pair per byte.
func TestApplyFileTargetCopies(t *testing.T) {
	b := &bpsBuilder{src: []byte("base rom")}
	b.targetRead([]byte("A"))
	b.targetCopy(0, 3<<20) // 3 MB run of 'A'
	b.targetRead([]byte("xyz"))
	start := int64(len(b.target)) - 3
	b.targetCopy(start, 2<<20+1) // "xyzxyz..." across buffer boundaries
	b.targetCopy(5, 1000)        // plain back-reference, no overlap
	long := make([]byte, copyBufSize+100)
	for i := range long {
		long[i] = byte(i * 7)
	}
	b.targetRead(long)
	b.targetCopy(int64(len(b.target)-len(long)), len(long)+500) // period > buffer, overlapping
	p := b.patch()

	want, err := Apply(p, b.src)
	if err != nil {
		t.Fatalf("in-memory Apply: %v", err)
	}
	if !bytes.Equal(want, b.target) {
		t.Fatal("test builder and in-memory patcher disagree")
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.bin")
	dstPath := filepath.Join(dir, "dst.bin")
	os.WriteFile(srcPath, b.src, 0o644)
	start2 := time.Now()
	if err := ApplyFile(p, srcPath, dstPath, nil); err != nil {
		t.Fatalf("ApplyFile: %v", err)
	}
	if d := time.Since(start2); d > 5*time.Second {
		t.Errorf("streaming %d MB of fills took %s", len(want)>>20, d)
	}
	got, _ := os.ReadFile(dstPath)
	if !bytes.Equal(got, want) {
		t.Fatalf("streaming output differs (%d vs %d bytes)", len(got), len(want))
	}
}

// IPS truncation only ever shortens, in both paths.
func TestIPSTruncationNeverGrows(t *testing.T) {
	src := []byte("0123456789")
	p := append([]byte("PATCHEOF"), 0x00, 0x00, 0x20) // truncate to 32: longer than the ROM
	mem, err := Apply(p, src)
	if err != nil || len(mem) != len(src) {
		t.Fatalf("in-memory: %d bytes, %v", len(mem), err)
	}
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.bin")
	dstPath := filepath.Join(dir, "dst.bin")
	os.WriteFile(srcPath, src, 0o644)
	if err := ApplyFile(p, srcPath, dstPath, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dstPath); len(got) != len(src) {
		t.Errorf("streamed IPS grew the file to %d bytes", len(got))
	}
}

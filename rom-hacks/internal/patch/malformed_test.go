package patch

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// bpsNumber encodes n the way BPS headers do.
func bpsNumber(n uint64) []byte {
	var out []byte
	for {
		x := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(out, x|0x80)
		}
		out = append(out, x)
		n--
	}
}

// craftedBPS builds a BPS whose source CRC matches src, so the parser gets
// past the checksum and reaches the header values under test.
func craftedBPS(src []byte, dstSize, metaSize uint64, actions ...byte) []byte {
	body := append([]byte("BPS1"), bpsNumber(uint64(len(src)))...)
	body = append(body, bpsNumber(dstSize)...)
	body = append(body, bpsNumber(metaSize)...)
	body = append(body, actions...)
	footer := make([]byte, 12)
	binary.LittleEndian.PutUint32(footer, crc32.ChecksumIEEE(src))
	return append(body, footer...)
}

// A broken patch must be an error, never a crash: a panic or an
// out-of-memory abort takes the whole app down, and nothing recovers it.
func TestMalformedPatchesAreErrors(t *testing.T) {
	src := []byte("hello")
	cases := map[string][]byte{
		"metadata size wraps negative": craftedBPS(src, 5, 1<<63+5),
		"target size in terabytes":     craftedBPS(src, 1<<40, 0),
		// TargetRead whose length field wraps an int.
		"action length wraps": craftedBPS(src, 5, 0, append(bpsNumber(1<<63|1), 0)...),
		// TargetCopy reading bytes not yet written (offset 0, length 5).
		"target copy ahead of the write head": craftedBPS(src, 5, 0, append(bpsNumber(4<<2|3), bpsNumber(0)...)...),
		"ups target in terabytes": func() []byte {
			b := append([]byte("UPS1"), bpsNumber(5)...)
			b = append(b, bpsNumber(1<<40)...)
			f := make([]byte, 12)
			binary.LittleEndian.PutUint32(f, crc32.ChecksumIEEE(src))
			return append(b, f...)
		}(),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Apply(p, src); err == nil {
				t.Error("a malformed patch was accepted")
			}
		})
	}

	// The streamed path, used for discs, must refuse the same headers.
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.bin")
	os.WriteFile(srcPath, src, 0o644)
	for _, name := range []string{"metadata size wraps negative", "target size in terabytes", "action length wraps"} {
		dst := filepath.Join(dir, "out.bin")
		if err := ApplyFile(cases[name], srcPath, dst, nil); err == nil {
			t.Errorf("ApplyFile accepted %s", name)
		}
		if _, err := os.Stat(dst); err == nil {
			t.Errorf("ApplyFile left output behind for %s", name)
		}
	}
}

// FuzzApply feeds arbitrary bytes behind each format's magic. Run with
// go test -fuzz FuzzApply ./internal/patch to look further.
func FuzzApply(f *testing.F) {
	f.Add([]byte("BPS1\x85\x85\x80"))
	f.Add([]byte("PATCH\x00\x00\x00\x00\x01XEOF"))
	f.Add([]byte("UPS1\x85\x85"))
	src := []byte("hello")
	f.Fuzz(func(t *testing.T, p []byte) {
		Apply(p, src)
		Apply(craftedBPS(src, 5, 0, p...), src)
	})
}

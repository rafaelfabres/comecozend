package patch

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The .bps vectors in testdata were produced by python-bps-continued, an
// independent implementation, so passing these means agreeing with someone
// else's encoder rather than with our own assumptions.
func TestApplyVectors(t *testing.T) {
	cases := []struct {
		name  string
		patch string
	}{
		{"edits", "edits.bps"},  // scattered small edits
		{"grow", "grow.bps"},    // target longer than source, long runs
		{"moved", "moved.bps"},  // blocks relocated, backwards seeks
		{"ips", "ipsbasic.ips"}, // literal records, RLE record, growth
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stem := tc.patch[:len(tc.patch)-4]
			src := readFile(t, stem+".src")
			want := readFile(t, stem+".tgt")
			p := readFile(t, tc.patch)

			got, err := Apply(p, src)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("patched output differs: got %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

// A BPS patch must refuse a base ROM it was not built against. This is the
// check that stops a wrong regional dump from producing a silently broken
// ROM.
func TestBPSRejectsWrongSource(t *testing.T) {
	src := readFile(t, "edits.src")
	p := readFile(t, "edits.bps")

	wrong := make([]byte, len(src))
	copy(wrong, src)
	wrong[0] ^= 0xff

	if _, err := Apply(p, wrong); err == nil {
		t.Fatal("expected a CRC mismatch, got none")
	}

	shorter := src[:len(src)-1]
	if _, err := Apply(p, shorter); err == nil {
		t.Fatal("expected a size mismatch, got none")
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		in   []byte
		want Format
	}{
		{[]byte("BPS1...."), BPS},
		{[]byte("PATCH..."), IPS},
		{[]byte("UPS1...."), UPS},
		{[]byte{0xD6, 0xC3, 0xC4, 0x00}, XDelta},
		{[]byte("nope"), Unknown},
	}
	for _, tc := range cases {
		if got := Detect(tc.in); got != tc.want {
			t.Errorf("Detect(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// xdelta is recognised but must be reported as needing the helper binary,
// not silently mis-parsed.
func TestXDeltaIsExternal(t *testing.T) {
	_, err := Apply([]byte{0xD6, 0xC3, 0xC4, 0x00, 0x00}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsExternal(err) {
		t.Fatalf("expected ErrExternal, got %v", err)
	}
}

func TestTruncatedPatchesDoNotPanic(t *testing.T) {
	full := readFile(t, "edits.bps")
	src := readFile(t, "edits.src")
	for n := 0; n < len(full); n++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %d-byte patch: %v", n, r)
				}
			}()
			_, _ = Apply(full[:n], src)
		}()
	}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

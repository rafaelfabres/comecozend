package patch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The xdelta path is the one piece that leaves the process, so the test
// checks the whole hand-off: detection routes to ErrExternal, ApplyAny
// picks up the helper, and the bytes come back right.
func TestApplyXDelta(t *testing.T) {
	bin := os.Getenv("XDELTA3")
	if bin == "" {
		if found, err := exec.LookPath("xdelta3"); err == nil {
			bin = found
		}
	}
	if bin == "" {
		t.Skip("no xdelta3 available; set XDELTA3 to run this")
	}

	src := readFile(t, "edits.src")
	want := readFile(t, "edits.tgt")
	p, err := os.ReadFile(filepath.Join("testdata", "edits.xdelta"))
	if err != nil {
		t.Skip("no xdelta vector in testdata")
	}

	if got := Detect(p); got != XDelta {
		t.Fatalf("Detect = %v, want XDelta", got)
	}
	if _, err := Apply(p, src); !IsExternal(err) {
		t.Fatalf("Apply should defer to the helper, got %v", err)
	}

	old := XDeltaBinary
	XDeltaBinary = bin
	defer func() { XDeltaBinary = old }()

	got, err := ApplyAny(p, src)
	if err != nil {
		t.Fatalf("ApplyAny: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("xdelta output differs: got %d bytes, want %d", len(got), len(want))
	}
}

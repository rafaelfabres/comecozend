package roms

import (
	"os"
	"path/filepath"
	"strings"
)

// TempDir is where downloads are staged before they are inspected or
// unpacked. It must be on the card: on dArkOS /tmp is a tmpfs, so a
// "temporary file" there is RAM, and a disc-sized archive staged in it can
// take the device's whole gigabyte. The app sets this at startup; empty
// means the system temporary directory.
var TempDir = ""

// tempPrefix marks every file staged here, so SweepTemp only ever deletes
// what this app created.
const tempPrefix = "itchio-"

// CreateTemp is os.CreateTemp in TempDir. pattern must start with
// "itchio-". It falls back to the system directory if the card folder
// cannot be created, so a download is never refused over where to stage it.
func CreateTemp(pattern string) (*os.File, error) {
	if TempDir != "" {
		if err := os.MkdirAll(TempDir, 0o755); err == nil {
			return os.CreateTemp(TempDir, pattern)
		}
	}
	return os.CreateTemp("", pattern)
}

// SweepTemp deletes files a previous run staged and never got to remove:
// a crash, the power switch, or the app closed mid-download. At startup
// nothing is in progress, so all of them are leftovers.
func SweepTemp() (removed int) {
	if TempDir == "" {
		return 0
	}
	entries, err := os.ReadDir(TempDir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if os.Remove(filepath.Join(TempDir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

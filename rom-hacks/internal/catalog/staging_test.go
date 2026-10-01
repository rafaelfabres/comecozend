package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// Staged archives are deleted when a plan is closed, but a crash, the OOM
// killer or the power switch all skip that. Forty of them and 83 MB were
// found on a real device. At startup nothing is downloading, so whatever
// is left is debris.
func TestSweepStagingClearsDebrisAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	old := TempDir
	TempDir = dir
	defer func() { TempDir = old }()

	debris := []string{"patch-123.zip", "patch-456.7z"}
	for _, name := range debris {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 1024), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Anything not named like a staged download is somebody else's and
	// must survive: this directory sits on the user's card.
	keep := filepath.Join(dir, "notes.txt")
	os.WriteFile(keep, []byte("mine"), 0o644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)

	removed, bytes := SweepStaging()
	if removed != len(debris) {
		t.Errorf("removed %d, want %d", removed, len(debris))
	}
	if bytes != int64(len(debris)*1024) {
		t.Errorf("freed %d bytes, want %d", bytes, len(debris)*1024)
	}
	for _, name := range debris {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s survived", name)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("an unrelated file was deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "subdir")); err != nil {
		t.Error("a directory was deleted")
	}
}

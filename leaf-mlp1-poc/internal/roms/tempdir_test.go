package roms

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateTempStagesOnTheCard(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tmp")
	old := TempDir
	TempDir = dir
	t.Cleanup(func() { TempDir = old })

	f, err := CreateTemp("itchio-archive-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if filepath.Dir(f.Name()) != dir {
		t.Errorf("staged in %s, want %s", filepath.Dir(f.Name()), dir)
	}

	// Leftovers are swept; anything the app did not create is not.
	other := filepath.Join(dir, "keep.txt")
	os.WriteFile(other, nil, 0o644)
	if n := SweepTemp(); n != 1 {
		t.Errorf("swept %d files, want 1", n)
	}
	if _, err := os.Stat(f.Name()); err == nil {
		t.Error("leftover download was not removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a file the app did not create was removed")
	}
}

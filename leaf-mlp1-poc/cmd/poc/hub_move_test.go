package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Moving a game into its itchio folder must never delete it because some
// other file there has the same name.
func TestMoveInstalledFileKeepsADifferentFileOfTheSameName(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	old := filepath.Join(src, "Game.gb")
	os.WriteFile(old, []byte("the verified game"), 0o644)
	os.WriteFile(filepath.Join(dest, "Game.gb"), []byte("some other release"), 0o644)

	got, err := moveInstalledFile(old, dest)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != "the verified game" {
		t.Errorf("the moved game is not at %s: %q", got, b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "Game.gb")); string(b) != "some other release" {
		t.Error("the file already there was replaced")
	}
	if filepath.Base(got) != "Game (2).gb" {
		t.Errorf("moved to %s", got)
	}
}

func TestMoveInstalledFileDropsATrueDuplicate(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	old := filepath.Join(src, "Game.gb")
	os.WriteFile(old, []byte("same bytes"), 0o644)
	os.WriteFile(filepath.Join(dest, "Game.gb"), []byte("same bytes"), 0o644)

	got, err := moveInstalledFile(old, dest)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dest, "Game.gb") {
		t.Errorf("got %s", got)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the duplicate was not removed")
	}
}

func TestMoveInstalledFileMoves(t *testing.T) {
	src, dest := t.TempDir(), filepath.Join(t.TempDir(), "itchio")
	old := filepath.Join(src, "Game.gb")
	os.WriteFile(old, []byte("x"), 0o644)
	got, err := moveInstalledFile(old, dest)
	if err != nil || got != filepath.Join(dest, "Game.gb") {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the old file is still there")
	}
}

package main

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return path
}

// A cart at the top level with its includes in a subfolder must keep that
// subfolder: the cart says #include "lib/util.lua". Stripping every file's
// first directory flattened it to util.lua and the cart failed to load.
func TestExtractBundleKeepsSubfoldersOfAnUnwrappedRelease(t *testing.T) {
	zipPath := writeZip(t, map[string]string{
		"game.p8":      "#include lib/util.lua",
		"lib/util.lua": "-- util",
	})
	dest := t.TempDir()
	main, err := extractBundle(zipPath, dest)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(main) != "game.p8" {
		t.Errorf("main file = %s", main)
	}
	if _, err := os.Stat(filepath.Join(dest, "lib", "util.lua")); err != nil {
		t.Error("lib/util.lua was not kept under lib/")
	}
}

// The usual case still works: one folder wrapping the whole release is
// dropped, and what is inside it keeps its own layout.
func TestExtractBundleDropsTheWrapperFolder(t *testing.T) {
	zipPath := writeZip(t, map[string]string{
		"My Game/game.cue":           "FILE \"game.bin\" BINARY",
		"My Game/game.bin":           "data",
		"My Game\\extras\\notes.txt": "windows separators",
	})
	dest := t.TempDir()
	main, err := extractBundle(zipPath, dest)
	if err != nil {
		t.Fatal(err)
	}
	if main != filepath.Join(dest, "game.cue") {
		t.Errorf("index file = %s", main)
	}
	if _, err := os.Stat(filepath.Join(dest, "game.bin")); err != nil {
		t.Error("the data track was not extracted beside the cue")
	}
	if _, err := os.Stat(filepath.Join(dest, "extras", "notes.txt")); err != nil {
		t.Error("a path stored with backslashes was not unpacked as folders")
	}
}

// A track bigger than the cap fails the install instead of being written
// truncated — a cut-off .bin looks fine in the menu and fails to boot.
func TestExtractBundleRefusesToTruncate(t *testing.T) {
	old := maxExtractedTrack
	maxExtractedTrack = 8
	t.Cleanup(func() { maxExtractedTrack = old })

	zipPath := writeZip(t, map[string]string{
		"game.cue": "FILE \"game.bin\" BINARY",
		"game.bin": strings.Repeat("x", 9),
	})
	dest := t.TempDir()
	if _, err := extractBundle(zipPath, dest); !errors.Is(err, errTooLarge) {
		t.Fatalf("want errTooLarge, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "game.bin")); err == nil {
		t.Error("a truncated track was left on the card")
	}
}

func TestSafeRelPath(t *testing.T) {
	for rel, want := range map[string]bool{
		"game.bin":        true,
		"Game..v2.bin":    true, // dots in a name are not a parent directory
		"lib/util.lua":    true,
		"../escape.bin":   false,
		"lib/../../x.bin": false,
		"/etc/passwd":     false,
		"":                false,
	} {
		if got := safeRelPath(rel); got != want {
			t.Errorf("safeRelPath(%q) = %v, want %v", rel, got, want)
		}
	}
}

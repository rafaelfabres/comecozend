package disc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cue that ships with a patch names the hack author's own filename,
// which is never what the file ends up called here. A cue pointing at a
// file that is not there loads as a blank disc, so the FILE line has to be
// rewritten.
func TestWriteCueRepointsFile(t *testing.T) {
	shipped := "FILE \"CTR - Crash Team Racing - Unlimited (v1.0) (Custom Team Racing).bin\" BINARY\r\n" +
		"  TRACK 01 MODE2/2352\r\n" +
		"    INDEX 01 00:00:00\r\n" +
		"  TRACK 02 AUDIO\r\n" +
		"    INDEX 00 34:49:34\r\n"

	dest := filepath.Join(t.TempDir(), "out.cue")
	if err := WriteCue([]byte(shipped), dest, "My Hack.bin"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "FILE \"My Hack.bin\" BINARY") {
		t.Errorf("FILE line not repointed:\n%s", text)
	}
	if strings.Contains(text, "Custom Team Racing") {
		t.Error("the shipped filename survived")
	}
	// Every track must survive: dropping the audio tracks would leave the
	// game silent.
	if n := strings.Count(text, "TRACK "); n != 2 {
		t.Errorf("got %d TRACK lines, want 2", n)
	}
	if !strings.Contains(text, "INDEX 00 34:49:34") {
		t.Error("track indexes were lost")
	}
}

// A disc with CD audio keeps one file per track. Each FILE line must point
// at its own track: sending them all to the data track verified fine (the
// RetroAchievements hash only reads track 1) and then played with no music.
func TestWriteCueKeepsOneFilePerTrack(t *testing.T) {
	shipped := "FILE \"Hack (Track 1).bin\" BINARY\n" +
		"  TRACK 01 MODE2/2352\n" +
		"    INDEX 01 00:00:00\n" +
		"FILE \"Hack (Track 2).bin\" BINARY\n" +
		"  TRACK 02 AUDIO\n" +
		"    INDEX 01 00:00:00\n"
	if n := CueFileCount([]byte(shipped)); n != 2 {
		t.Fatalf("CueFileCount = %d, want 2", n)
	}

	dest := filepath.Join(t.TempDir(), "out.cue")
	if err := WriteCue([]byte(shipped), dest, "My Hack.bin", "My Hack (Track 02).bin"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	text := string(got)
	first := strings.Index(text, "FILE \"My Hack.bin\" BINARY")
	second := strings.Index(text, "FILE \"My Hack (Track 02).bin\" BINARY")
	if first < 0 || second < 0 || second < first {
		t.Errorf("each FILE line should name its own track, in order:\n%s", text)
	}
	if strings.Count(text, "My Hack.bin") != 1 {
		t.Errorf("the data track is named more than once:\n%s", text)
	}
}

func TestWriteCueNeedsAName(t *testing.T) {
	if err := WriteCue([]byte("FILE \"a.bin\" BINARY\n"), filepath.Join(t.TempDir(), "x.cue")); err == nil {
		t.Error("WriteCue with no track name should fail")
	}
}

func TestIsCompressed(t *testing.T) {
	for path, want := range map[string]bool{
		"game.chd": true, "GAME.CHD": true,
		"game.cue": false, "game.bin": false,
	} {
		if got := IsCompressed(path); got != want {
			t.Errorf("IsCompressed(%q) = %v", path, got)
		}
	}
}

// Without chdman the app must say what to install, not just fail.
func TestMissingCHDManExplainsItself(t *testing.T) {
	if !strings.Contains(ErrNoCHDMan.Error(), "mame-tools") {
		t.Error("the error should name the package that provides chdman")
	}
}

// A multi-disc PlayStation game sits in the ROM folder as an .m3u naming
// its discs. Handing that to chdman produces nothing, so it has to be
// read first — this is what failed on Final Fantasy IX.
func TestResolvePlaylist(t *testing.T) {
	dir := t.TempDir()
	discPath := filepath.Join(dir, "Final Fantasy IX (USA) (Disc 1) (Rev 1).chd")
	os.WriteFile(discPath, []byte("not really a chd"), 0o644)

	m3u := filepath.Join(dir, "Final Fantasy IX (USA).m3u")
	os.WriteFile(m3u, []byte("# comment\n\nFinal Fantasy IX (USA) (Disc 1) (Rev 1).chd\nDisc 2.chd\n"), 0o644)

	got, err := ResolvePlaylist(m3u)
	if err != nil {
		t.Fatal(err)
	}
	if got != discPath {
		t.Errorf("resolved to %s, want %s", got, discPath)
	}

	// Anything that is not a playlist passes straight through.
	if got, _ := ResolvePlaylist(discPath); got != discPath {
		t.Errorf("a plain image should pass through, got %s", got)
	}
}

func TestResolvePlaylistMissingDisc(t *testing.T) {
	dir := t.TempDir()
	m3u := filepath.Join(dir, "Game.m3u")
	os.WriteFile(m3u, []byte("Missing Disc.chd\n"), 0o644)

	if _, err := ResolvePlaylist(m3u); err == nil {
		t.Fatal("expected an error naming the missing disc")
	} else if !strings.Contains(err.Error(), "Missing Disc.chd") {
		t.Errorf("the error should name the file: %v", err)
	}
}

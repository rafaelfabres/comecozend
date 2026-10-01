package catalog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"leaf-hacks/internal/library"
	"leaf-hacks/internal/rahub"
	"leaf-hacks/internal/rapatches"
)

const discHash = "0123456789abcdef0123456789abcdef"

// discFixture is a two-track .bin/.cue set — a data track and a CD audio
// track — and a plan whose IPS patch changes the first byte of the data
// track.
func discFixture(t *testing.T) (plan *Plan, romsRoot, track1, track2 string) {
	t.Helper()
	src := t.TempDir()
	track1 = filepath.Join(src, "Game (Track 1).bin")
	track2 = filepath.Join(src, "Game (Track 2).bin")
	os.WriteFile(track1, []byte("DATA-TRACK"), 0o644)
	os.WriteFile(track2, []byte("AUDIO-TRACK"), 0o644)
	cue := filepath.Join(src, "Game.cue")
	os.WriteFile(cue, []byte(
		"FILE \"Game (Track 1).bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"+
			"FILE \"Game (Track 2).bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 01 00:00:00\n"), 0o644)

	// IPS: offset 0, length 1, byte 'X'.
	ips := []byte("PATCH\x00\x00\x00\x00\x01XEOF")
	pf := rapatches.PatchFile{Name: "Hack.ips", Data: ips}
	entry := rahub.HashEntry{Name: "Hack (v1).cue", MD5: discHash}
	plan = &Plan{
		Hack:      Hack{GameID: 1, ConsoleID: 12, Title: "Hack"},
		Patch:     pf,
		Base:      library.ROM{Path: cue, System: "psx", ConsoleID: 12, Disc: true},
		Expected:  entry,
		Supported: []rahub.HashEntry{entry},
		Payload:   rapatches.Payload{Patches: []rapatches.PatchFile{pf}},
	}
	return plan, t.TempDir(), track1, track2
}

func stubHashImage(t *testing.T, hash string, err error) {
	t.Helper()
	old := hashImage
	hashImage = func(rahub.Console, string) (string, error) { return hash, err }
	t.Cleanup(func() { hashImage = old })
}

func hacksFiles(t *testing.T, romsRoot string) []string {
	t.Helper()
	entries, _ := os.ReadDir(HacksDir(romsRoot, "psx"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Every track of the disc must reach the hacks folder, each named by its
// own FILE line. Pointing every line at the patched data track verified
// fine and then played with no music.
func TestInstallDiscKeepsAudioTracks(t *testing.T) {
	plan, romsRoot, track1, track2 := discFixture(t)
	stubHashImage(t, discHash, nil)

	cuePath, err := InstallWithProgress(plan, romsRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := HacksDir(romsRoot, "psx")
	cue, _ := os.ReadFile(cuePath)
	text := string(cue)
	if !strings.Contains(text, "FILE \"Hack (v1).bin\" BINARY") ||
		!strings.Contains(text, "FILE \"Hack (v1) (Track 02).bin\" BINARY") {
		t.Fatalf("cue does not name each track:\n%s", text)
	}
	if patched, _ := os.ReadFile(filepath.Join(dir, "Hack (v1).bin")); string(patched) != "XATA-TRACK" {
		t.Errorf("data track = %q, want the patched bytes", patched)
	}
	if audio, _ := os.ReadFile(filepath.Join(dir, "Hack (v1) (Track 02).bin")); !bytes.Equal(audio, []byte("AUDIO-TRACK")) {
		t.Errorf("audio track = %q", audio)
	}
	// The user's own set is copied from, never moved or changed.
	if b, _ := os.ReadFile(track1); string(b) != "DATA-TRACK" {
		t.Error("the original data track was modified")
	}
	if _, err := os.Stat(track2); err != nil {
		t.Error("the original audio track was moved")
	}
}

// A disc that cannot be hashed is not verified, and an unverified disc is
// exactly what must never be left in the hacks folder.
func TestInstallDiscRefusesWhatItCannotVerify(t *testing.T) {
	plan, romsRoot, _, _ := discFixture(t)
	stubHashImage(t, "", errors.New("no SYSTEM.CNF"))

	if _, err := InstallWithProgress(plan, romsRoot, nil); err == nil {
		t.Fatal("an unhashable disc was installed")
	}
	if files := hacksFiles(t, romsRoot); len(files) != 0 {
		t.Errorf("left behind: %v", files)
	}
}

func TestInstallDiscRemovesEverythingOnMismatch(t *testing.T) {
	plan, romsRoot, _, _ := discFixture(t)
	stubHashImage(t, "ffffffffffffffffffffffffffffffff", nil)

	if _, err := InstallWithProgress(plan, romsRoot, nil); err == nil {
		t.Fatal("a disc RetroAchievements does not know was installed")
	}
	if files := hacksFiles(t, romsRoot); len(files) != 0 {
		t.Errorf("left behind: %v", files)
	}
}

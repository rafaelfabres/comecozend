package esmeta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A scraped gamelist holds fields this app does not model — ratings,
// publishers, play counts, video paths. Parsing and rewriting would drop
// every one of them, so the file is edited as text and the rest must come
// out byte for byte.
func TestWriteKeepsExistingEntriesIntact(t *testing.T) {
	dir := t.TempDir()
	original := `<?xml version="1.0"?>
<gameList>
	<game>
		<path>./Super Mario World (USA).sfc</path>
		<name>Super Mario World</name>
		<rating>0.95</rating>
		<publisher>Nintendo</publisher>
		<playcount>42</playcount>
		<video>./videos/smw.mp4</video>
	</game>
</gameList>
`
	path := filepath.Join(dir, "gamelist.xml")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Write(dir, Entry{
		ROMPath:   filepath.Join(dir, "hacks", "A Hack (v1.0).sfc"),
		Name:      "A Hack",
		Desc:      "Rebalances every level.",
		ImagePath: filepath.Join(dir, "hacks", "images", "A Hack (v1.0)-image.png"),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, keep := range []string{"<rating>0.95</rating>", "<publisher>Nintendo</publisher>",
		"<playcount>42</playcount>", "<video>./videos/smw.mp4</video>"} {
		if !strings.Contains(text, keep) {
			t.Errorf("scraped field lost: %s", keep)
		}
	}
	// Paths must be relative to the system folder, including the subfolder.
	if !strings.Contains(text, "<path>./hacks/A Hack (v1.0).sfc</path>") {
		t.Errorf("wrong rom path:\n%s", text)
	}
	if !strings.Contains(text, "<image>./hacks/images/A Hack (v1.0)-image.png</image>") {
		t.Errorf("wrong image path:\n%s", text)
	}
	if !strings.Contains(text, "<desc>Rebalances every level.</desc>") {
		t.Error("description missing")
	}
}

// Reinstalling must replace the entry, not add a second one: the menu
// would show the game twice.
func TestWriteReplacesRatherThanDuplicates(t *testing.T) {
	dir := t.TempDir()
	rom := filepath.Join(dir, "hacks", "Hack.gba")

	for i, name := range []string{"Old Name", "New Name"} {
		if err := Write(dir, Entry{ROMPath: rom, Name: name}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	text := readList(t, dir)
	if n := strings.Count(text, "<path>./hacks/Hack.gba</path>"); n != 1 {
		t.Errorf("entry appears %d times", n)
	}
	if strings.Contains(text, "Old Name") {
		t.Error("the stale entry survived")
	}
	if !strings.Contains(text, "New Name") {
		t.Error("the new entry is missing")
	}
}

// Deleting a hack must take its menu entry with it, or the system lists a
// game whose file is gone.
func TestRemoveDeletesOnlyThatEntry(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "Other Game.gba")
	drop := filepath.Join(dir, "hacks", "Hack.gba")

	Write(dir, Entry{ROMPath: keep, Name: "Other Game"})
	Write(dir, Entry{ROMPath: drop, Name: "Hack"})

	if err := Remove(dir, drop); err != nil {
		t.Fatal(err)
	}
	text := readList(t, dir)
	if strings.Contains(text, "./hacks/Hack.gba") {
		t.Error("the entry was not removed")
	}
	if !strings.Contains(text, "Other Game") {
		t.Error("the wrong entry was removed")
	}
	if !strings.Contains(text, "</gameList>") {
		t.Error("the file was left malformed")
	}
}

// Titles carry ampersands and quotes, and a raw one makes the whole file
// unparseable — which would blank out every game in that system.
func TestTitlesAreEscaped(t *testing.T) {
	dir := t.TempDir()
	Write(dir, Entry{
		ROMPath: filepath.Join(dir, "hacks", "x.gba"),
		Name:    `Tom & Jerry "Special" <Edition>`,
	})
	text := readList(t, dir)
	if strings.Contains(text, "Tom & Jerry") {
		t.Error("the ampersand was not escaped")
	}
	if !strings.Contains(text, "Tom &amp; Jerry &quot;Special&quot; &lt;Edition&gt;") {
		t.Errorf("escaping wrong:\n%s", text)
	}
}

func readList(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "gamelist.xml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

package library

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Collections commonly keep cartridge ROMs zipped. A scan that only saw
// bare files reported those folders as empty, which looks like the app is
// broken when the games are plainly there.
func TestScanReadsROMsInsideZips(t *testing.T) {
	root := t.TempDir()
	gb := filepath.Join(root, "gb")
	if err := os.MkdirAll(gb, 0o755); err != nil {
		t.Fatal(err)
	}

	rom := bytes.Repeat([]byte{0x42}, 32768)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// A readme alongside the ROM: the scanner must pick the ROM.
	w, _ := zw.Create("readme.txt")
	w.Write([]byte("not a rom"))
	w, _ = zw.Create("Some Game (USA).gb")
	w.Write(rom)
	zw.Close()

	if err := os.WriteFile(filepath.Join(gb, "Some Game (USA).zip"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	lib, report, err := ScanReport(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.ROMs) != 1 {
		t.Fatalf("found %d ROMs, want 1 (report: %v)", len(lib.ROMs), report.Lines())
	}
	got := lib.ROMs[0]
	if got.Inner != "Some Game (USA).gb" {
		t.Errorf("Inner = %q", got.Inner)
	}
	// The hashes must describe the ROM, not the zip around it.
	if got.MD5 != md5hex(rom) {
		t.Errorf("MD5 describes the container, not the ROM")
	}
	data, err := Read(got)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(data, rom) {
		t.Error("Read returned the container instead of the ROM")
	}
}

// Arcade is the exception: there the zip IS the ROM and RetroAchievements
// hashes it by filename, so opening it would be wrong.
func TestArcadeZipsAreNotOpened(t *testing.T) {
	root := t.TempDir()
	arcade := filepath.Join(root, "arcade")
	os.MkdirAll(arcade, 0o755)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("something.bin")
	w.Write([]byte("mame set contents"))
	zw.Close()
	os.WriteFile(filepath.Join(arcade, "sf2ce.zip"), buf.Bytes(), 0o644)

	lib, _, err := ScanReport(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.ROMs) != 1 {
		t.Fatalf("found %d ROMs, want 1", len(lib.ROMs))
	}
	if lib.ROMs[0].Inner != "" {
		t.Errorf("arcade zip was opened: Inner = %q", lib.ROMs[0].Inner)
	}
}

// A folder the user can see but that produces nothing must explain itself.
func TestReportExplainsEmptyFolders(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "psx"), 0o755)
	os.MkdirAll(filepath.Join(root, "saturn"), 0o755)
	os.WriteFile(filepath.Join(root, "saturn", "Some Game.chd"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "pc98"), 0o755)
	os.MkdirAll(filepath.Join(root, "themes"), 0o755)
	os.MkdirAll(filepath.Join(root, "gb"), 0o755)
	// A folder holding real files gets explained; one holding only
	// EmulationStation's metadata does not.
	os.WriteFile(filepath.Join(root, "pc98", "Some Game.d88"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "themes", "gamelist.xml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "gb", "gamelist.xml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "gb", "weird.qqq"), []byte("x"), 0o644)

	_, report, err := ScanReport(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// PlayStation is scanned despite being disc-based: it is the one disc
	// system with hacks in the repository.
	if report.SkippedFolders["psx"] != "" {
		t.Errorf("psx should be scanned, not skipped: %q", report.SkippedFolders["psx"])
	}
	if report.SkippedFolders["saturn"] == "" {
		t.Error("saturn has no hacks and should be reported as skipped")
	}
	if report.SkippedFolders["pc98"] == "" {
		t.Error("pc98 should be reported as unknown")
	}
	if report.SkippedFolders["themes"] != "" {
		t.Error("a folder of metadata should not be reported at all")
	}
	if report.UnknownExts["gb"][".qqq"] != 1 {
		t.Errorf("the unrecognised file should be counted: %v", report.UnknownExts)
	}
	if _, noisy := report.UnknownExts["gb"][".xml"]; noisy {
		t.Error("gamelist.xml should never be reported")
	}
}

// A patched ROM lives one level down, in <system>/hacks/. Without
// scanning it, a hack installed a second ago is still reported as missing.
func TestScanFindsHacksSubfolder(t *testing.T) {
	root := t.TempDir()
	gba := filepath.Join(root, "gba")
	hacks := filepath.Join(gba, "hacks")
	if err := os.MkdirAll(hacks, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(gba, "Base Game (USA).gba"), bytes.Repeat([]byte{1}, 4096), 0o644)
	os.WriteFile(filepath.Join(hacks, "Some Hack (v2.0).gba"), bytes.Repeat([]byte{2}, 4096), 0o644)

	lib, _, err := ScanReport(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.ROMs) != 2 {
		t.Fatalf("found %d ROMs, want 2 (base + hack)", len(lib.ROMs))
	}
	var foundHack bool
	for _, r := range lib.ROMs {
		if r.Name() == "Some Hack (v2.0).gba" {
			foundHack = true
			if filepath.Base(filepath.Dir(r.Path)) != "hacks" {
				t.Errorf("hack recorded at %s", r.Path)
			}
		}
	}
	if !foundHack {
		t.Error("the installed hack was not scanned")
	}
}

// A PlayStation .chd is routinely 130 MB and more. The size cap existed
// to stop the hasher reading something absurd, but discs are never
// hashed — and capping them dropped exactly the games the user was
// looking for, with no message anywhere.
func TestLargeDiscImagesAreScanned(t *testing.T) {
	root := t.TempDir()
	psx := filepath.Join(root, "psx")
	if err := os.MkdirAll(psx, 0o755); err != nil {
		t.Fatal(err)
	}
	// Sparse: the size is what matters, and nothing reads the contents.
	name := filepath.Join(psx, "Crash Bandicoot 2 - Cortex Strikes Back (Europe) (En,Fr,De,Es,It).chd")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(130 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()

	lib, report, err := ScanReport(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.ROMs) != 1 {
		t.Fatalf("the disc was not scanned (report: %v)", report.Lines())
	}
	if !lib.ROMs[0].Disc {
		t.Error("it should be recorded as a disc image")
	}
}

// A cartridge past any plausible size is still skipped — but says so.
func TestOversizedCartridgeIsReported(t *testing.T) {
	root := t.TempDir()
	gba := filepath.Join(root, "gba")
	os.MkdirAll(gba, 0o755)
	f, _ := os.Create(filepath.Join(gba, "Absurd.gba"))
	f.Truncate(200 << 20)
	f.Close()

	lib, report, err := ScanReport(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.ROMs) != 0 {
		t.Error("a 200 MB GBA file is not a cartridge dump")
	}
	if len(report.Oversized["gba"]) != 1 {
		t.Errorf("it should be reported, got %v", report.Oversized)
	}
}

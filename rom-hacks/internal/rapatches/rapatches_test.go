package rapatches

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestParseEntry(t *testing.T) {
	cases := []struct {
		path string
		ok   bool
		want Entry
	}{
		{
			path: "GBA/Hacks/Fire Emblem - The Sacred Stones/36943-FE8-HagInWhite.7z",
			ok:   true,
			want: Entry{Console: "GBA", Category: Hacks, BaseGame: "Fire Emblem - The Sacred Stones", GameID: 36943, File: "36943-FE8-HagInWhite.7z"},
		},
		{
			path: "SNES/Hacks/7th Saga, The/359-7thSaga-ElnardRestoration.zip",
			ok:   true,
			want: Entry{Console: "SNES", Category: Hacks, BaseGame: "7th Saga, The", GameID: 359, File: "359-7thSaga-ElnardRestoration.zip"},
		},
		{
			path: "MD/Translation/Russian/1-Sonic1-Russian.zip",
			ok:   true,
			want: Entry{Console: "MD", Category: Translation, BaseGame: "Russian", GameID: 1, File: "1-Sonic1-Russian.zip"},
		},
		// Ten archives use the singular spelling; folding it in is the
		// difference between listing them and losing them.
		{
			path: "GBC/Hack/Pokemon - Crystal Version/5-X.zip",
			ok:   true,
			want: Entry{Console: "GBC", Category: Hacks, BaseGame: "Pokemon - Crystal Version", GameID: 5, File: "5-X.zip"},
		},
		// A withdrawn patch. Its path has the same shape as a live hack,
		// one level deeper under "Removed", so only the blocklist stops
		// it from being offered as installable.
		{path: "Removed/Mega Drive/Hacks/565-Sonic1-MetalSonicHyperdrive.zip", ok: false},
		{path: "Incompatible/SNES/Hacks/999-Something.zip", ok: false},
		// Split archives cannot be opened from a single download.
		{path: "PlayStation Portable/Multipart/Haruhi/123-Haruhi.7z.001", ok: false},
		// Three levels deep: a Fix, not a Hack — parsed away, not mistaken
		// for a hack with a missing base-game folder.
		{path: "Apple II/Fix/10823-NemesisSoftlockFixExtraLives.zip", ok: false},
		{path: "README.md", ok: false},
		{path: "SNES/Hacks/Super Mario World/notes.txt", ok: false},
		{path: "SNES/Hacks/Super Mario World/no-id-here.zip", ok: false},
	}
	for _, tc := range cases {
		got, ok := parseEntry(tc.path)
		if ok != tc.ok {
			t.Fatalf("parseEntry(%q) ok = %v, want %v", tc.path, ok, tc.ok)
		}
		if !ok {
			continue
		}
		tc.want.Path = tc.path
		if got != tc.want {
			t.Errorf("parseEntry(%q):\n got %+v\nwant %+v", tc.path, got, tc.want)
		}
	}
}

func TestParseReadmeLabelled(t *testing.T) {
	const in = `Use with:

(No Intro)
File:               Super Mario World (USA).sfc
BitSize:            4 Mbit
Size (Bytes):       524288
CRC32:              B19ED489
MD5:                CDD3C8C37322978CA8669B34BC89C804
SHA1:               6B47BB75D16514B6A476AA0C73A683A2A4C18765
`
	got := ParseReadme(in)
	if len(got) != 1 {
		t.Fatalf("got %d base ROMs, want 1: %+v", len(got), got)
	}
	b := got[0]
	if b.File != "Super Mario World (USA).sfc" {
		t.Errorf("File = %q", b.File)
	}
	if b.MD5 != "cdd3c8c37322978ca8669b34bc89c804" {
		t.Errorf("MD5 = %q", b.MD5)
	}
	if !b.HasCRC || b.CRC32 != 0xB19ED489 {
		t.Errorf("CRC32 = %08X (has=%v)", b.CRC32, b.HasCRC)
	}
	if b.Size != 524288 {
		t.Errorf("Size = %d", b.Size)
	}
}

// The bare form lists the two hashes loose, and the repository uses both
// orders, so length has to decide which is which.
func TestParseReadmeBareEitherOrder(t *testing.T) {
	md5First := ParseReadme("Use with:\n\nNo Intro\nPokemon - FireRed Version (USA).gba\ne26ee0d44e809351c8ce2d73c7400cdd\nDD88761C\n")
	crcFirst := ParseReadme("Use with:\n\n(No Intro)\nSuper Mario Bros. (World).nes\n393a432f\nf94bb9bb55f325d9af8a0fff80b9376d\n")

	if len(md5First) != 1 || md5First[0].MD5 != "e26ee0d44e809351c8ce2d73c7400cdd" || md5First[0].CRC32 != 0xDD88761C {
		t.Errorf("md5-first form parsed as %+v", md5First)
	}
	if len(crcFirst) != 1 || crcFirst[0].MD5 != "f94bb9bb55f325d9af8a0fff80b9376d" || crcFirst[0].CRC32 != 0x393A432F {
		t.Errorf("crc-first form parsed as %+v", crcFirst)
	}
}

func TestParseReadmeHeaderlessMD5(t *testing.T) {
	const in = `Use with:

(No Intro)
File:               Zelda II - The Adventure of Link (USA).nes
CRC32:              861C3FE6
MD5:                E6DD4104FA46EB4BEF2CB52DC341DD38
Headerless MD5:     88C0493FB1146834836AAAAAAAAAAAAA
`
	got := ParseReadme(in)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].HeaderlessMD5 != "88c0493fb1146834836aaaaaaaaaaaaa" {
		t.Errorf("HeaderlessMD5 = %q", got[0].HeaderlessMD5)
	}
	if got[0].MD5 != "e6dd4104fa46eb4bef2cb52dc341dd38" {
		t.Errorf("MD5 = %q", got[0].MD5)
	}
}

// Not every readme describes a base ROM; some are just the hack's story
// blurb. Inventing a match from prose would be worse than finding none.
func TestParseReadmeProseYieldsNothing(t *testing.T) {
	const prose = `Galactic civilization is at an end. After successfully carrying out her
mission to wipe out the Metroids on planet SR-388, legendary bounty hunter
Samus Aran was attacked by the mysterious lifeform known as "X".`
	if got := ParseReadme(prose); len(got) != 0 {
		t.Fatalf("expected no base ROMs, got %+v", got)
	}
}

func TestOpenRealArchives(t *testing.T) {
	t.Run("single patch", func(t *testing.T) {
		p := openTestdata(t, "44820-ActRaiser-Redone.zip")
		if len(p.Patches) != 1 {
			t.Fatalf("got %d patches, want 1", len(p.Patches))
		}
		if got := p.Patches[0].Stem(); got != "ActRaiser - Redone (v2.0) (Retch)" {
			t.Errorf("Stem() = %q", got)
		}
		if len(p.Bases) != 1 || p.Bases[0].File != "ActRaiser (USA).sfc" {
			t.Fatalf("base ROM parsed as %+v", p.Bases)
		}
	})

	t.Run("nested extras", func(t *testing.T) {
		p := openTestdata(t, "38513-ShiningForceII-Maeson.zip")
		// The archive bundles a folder of optional .ips tweaks. The one
		// the achievement set expects sits at the top level.
		chosen, _ := p.Select("")
		if got := chosen.Name; got != "Shining Force II - Maeson (v1.30) (Maeson).bps" {
			t.Errorf("Select picked %q", got)
		}
	})

	t.Run("select by hash name", func(t *testing.T) {
		p := openTestdata(t, "38513-ShiningForceII-Maeson.zip")
		want := "No Italic Font"
		chosen, exact := p.Select(want)
		if !exact || chosen.Stem() != "No Italic Font" {
			t.Errorf("Select(%q) = %q exact=%v", want, chosen.Name, exact)
		}
	})

	t.Run("ips archive", func(t *testing.T) {
		p := openTestdata(t, "8414-SMB1-BowsersJumpingChallenge.zip")
		chosen, _ := p.Select("")
		if filepath.Ext(chosen.Name) != ".ips" {
			t.Errorf("expected an .ips, got %q", chosen.Name)
		}
		if len(p.Bases) != 1 || p.Bases[0].CRC32 != 0x393A432F {
			t.Errorf("base ROM parsed as %+v", p.Bases)
		}
	})
}

func TestDownloadURLEscaping(t *testing.T) {
	e := Entry{Path: "GBA/Hacks/Fire Emblem - The Sacred Stones/36943-FE8-HagInWhite.7z"}
	want := rawBase + "GBA/Hacks/Fire%20Emblem%20-%20The%20Sacred%20Stones/36943-FE8-HagInWhite.7z"
	if got := e.DownloadURL(); got != want {
		t.Errorf("DownloadURL() =\n %s\nwant\n %s", got, want)
	}
}

func openTestdata(t *testing.T, name string) Payload {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	p, err := Open(name, b)
	if err != nil {
		t.Fatalf("Open %s: %v", name, err)
	}
	return p
}

// A PlayStation patch can be 78 MB: an xdelta against a 600 MB disc is
// itself large. Expanding every entry of an archive into memory was fine
// for cartridges and fatal for these, so large entries stay on disk until
// something asks for them.
func TestLargeEntriesLoadOnDemand(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "big.zip")

	// One small file and one past the eager threshold.
	small := []byte("Use with:\n\n(Redump)\nFile: Game (USA).bin\nCRC32: DEADBEEF\n")
	big := make([]byte, eagerEntryBytes+1024)
	copy(big, []byte("BPS1"))
	for i := 4; i < len(big); i++ {
		big[i] = byte(i)
	}

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("readme.txt")
	w.Write(small)
	w, _ = zw.Create("Big Hack (v1.0).xdelta")
	w.Write(big)
	zw.Close()
	f.Close()

	payload, closer, err := OpenFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	if len(payload.Patches) != 1 {
		t.Fatalf("got %d patches", len(payload.Patches))
	}
	p := &payload.Patches[0]
	if p.Data != nil {
		t.Error("a large entry should not have been expanded up front")
	}
	if p.Size != int64(len(big)) {
		t.Errorf("Size = %d, want %d", p.Size, len(big))
	}
	// The readme is small and is read straight away.
	if payload.Readme == "" {
		t.Error("the readme should have been read eagerly")
	}
	if len(payload.Bases) != 1 {
		t.Errorf("the readme should have parsed: %+v", payload.Bases)
	}

	// And the patch arrives intact when asked for.
	got := p.Bytes()
	if len(got) != len(big) {
		t.Fatalf("Bytes() returned %d bytes, want %d", len(got), len(big))
	}
	if string(got[:4]) != "BPS1" || got[len(got)-1] != big[len(big)-1] {
		t.Error("the loaded patch does not match what was stored")
	}
	// Cached: a second call must not re-read.
	if &p.Bytes()[0] != &got[0] {
		t.Error("Bytes() should cache its result")
	}
}

// Two ceilings, deliberately different. The download-to-disk limit can be
// a gigabyte because the card has 74 GB; the in-memory one must not,
// because the device has 1 GB of RAM and the background fingerprint pass
// buffers whatever it fetches.
func TestMemoryLimitIsSeparateFromDiskLimit(t *testing.T) {
	if maxInMemory >= maxArchive {
		t.Errorf("the in-memory ceiling (%d) must be well below the disk one (%d)",
			maxInMemory, maxArchive)
	}
	if maxInMemory > 128<<20 {
		t.Errorf("in-memory ceiling %d MB is too high for a 1 GB device", maxInMemory>>20)
	}
}

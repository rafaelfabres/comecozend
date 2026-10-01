package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"leaf-hacks/internal/library"
	"leaf-hacks/internal/rapatches"
)

func ownedLib(t *testing.T, crc uint32, md5, path string) *library.Library {
	t.Helper()
	lib := &library.Library{ROMs: []library.ROM{
		{Path: path, ConsoleID: 5, CRC32: crc, MD5: md5},
	}}
	lib.Index()
	return lib
}

// The whole point: a patch reaches the list because its checksum fits a
// file on the card, even when nobody could have matched the names.
// RetroAchievements calls the game "Pokémon: FireRed and LeafGreen
// Versions"; the patch folder is "Pokemon LeafGreen".
func TestFingerprintBeatsNames(t *testing.T) {
	const crc = 0xDD88761C
	lib := ownedLib(t, crc, "e26ee0d44e809351c8ce2d73c7400cdd", "/roms/gba/Pokemon - LeafGreen Version (USA).gba")

	bases := []OwnedBase{{
		GameID: 515, Title: "Pokémon: FireRed and LeafGreen Versions", ConsoleID: 5,
		ROMs: lib.ROMs,
	}}
	entry := rapatches.Entry{
		Path:    "GBA/Hacks/Something Entirely Different/900-Hack.zip",
		Console: "GBA", Category: rapatches.Hacks,
		BaseGame: "Something Entirely Different", GameID: 900, File: "900-Hack.zip",
	}
	ix := rapatches.Index{Entries: []rapatches.Entry{entry}}
	sets := map[int]SetInfo{900: {Title: "A Hack", Achievements: 10}}

	// Without a fingerprint the names decide, and they say no.
	facts := LoadFactsCache(filepath.Join(t.TempDir(), "f.json"))
	if cat := Build(bases, ix, sets, facts, lib); len(cat.Hacks) != 0 {
		t.Fatalf("names should not have matched: %+v", cat.Hacks)
	}

	// With one, the checksum says yes and the folder name stops mattering.
	facts.set(entry.Path, PatchFacts{SourceCRC: crc, HasCRC: true})
	cat := Build(bases, ix, sets, facts, lib)
	if len(cat.Hacks) != 1 {
		t.Fatalf("the fingerprint should have matched: %+v", cat.Hacks)
	}
	if cat.Hacks[0].BaseTitle != bases[0].Title {
		t.Errorf("attached to %q", cat.Hacks[0].BaseTitle)
	}
}

// A folder whose name matches but whose patch is for a different dump
// stays in the list and gets flagged. It is a hack of a game the user
// owns; what is missing is one specific dump, and that is information,
// not a reason to hide it.
func TestFalseNameMatchIsFlaggedNotDropped(t *testing.T) {
	lib := ownedLib(t, 0x11111111, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/roms/gba/Some Game (USA).gba")
	bases := []OwnedBase{{GameID: 1, Title: "Some Game", ConsoleID: 5, ROMs: lib.ROMs}}
	entry := rapatches.Entry{
		Path: "GBA/Hacks/Some Game/901-Hack.zip", Console: "GBA",
		Category: rapatches.Hacks, BaseGame: "Some Game", GameID: 901, File: "901-Hack.zip",
	}
	ix := rapatches.Index{Entries: []rapatches.Entry{entry}}
	sets := map[int]SetInfo{901: {Title: "A Hack", Achievements: 5}}

	facts := LoadFactsCache(filepath.Join(t.TempDir(), "f.json"))
	if cat := Build(bases, ix, sets, facts, lib); len(cat.Hacks) != 1 || cat.Hacks[0].NeedsOtherDump {
		t.Fatal("unchecked, it should list cleanly on the name match")
	}
	facts.set(entry.Path, PatchFacts{SourceCRC: 0x99999999, HasCRC: true})
	cat := Build(bases, ix, sets, facts, lib)
	if len(cat.Hacks) != 1 {
		t.Fatalf("it should still be listed, got %d", len(cat.Hacks))
	}
	if !cat.Hacks[0].NeedsOtherDump {
		t.Error("it should be flagged as needing another dump")
	}
}

// An archive that tells us nothing must not be treated as a "no".
func TestUnreadableFingerprintFallsBackToNames(t *testing.T) {
	lib := ownedLib(t, 0x11111111, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/roms/gba/Some Game (USA).gba")
	bases := []OwnedBase{{GameID: 1, Title: "Some Game", ConsoleID: 5, ROMs: lib.ROMs}}
	entry := rapatches.Entry{
		Path: "GBA/Hacks/Some Game/902-Hack.zip", Console: "GBA",
		Category: rapatches.Hacks, BaseGame: "Some Game", GameID: 902, File: "902-Hack.zip",
	}
	ix := rapatches.Index{Entries: []rapatches.Entry{entry}}
	sets := map[int]SetInfo{902: {Title: "A Hack", Achievements: 5}}

	facts := LoadFactsCache(filepath.Join(t.TempDir(), "f.json"))
	facts.set(entry.Path, PatchFacts{Failed: true})
	if cat := Build(bases, ix, sets, facts, lib); len(cat.Hacks) != 1 {
		t.Error("an unreadable archive should fall back to the name match")
	}
}

// Candidate selection has to be generous, or the checksum never gets a
// chance to rescue anything.
func TestCandidatesAreGenerous(t *testing.T) {
	bases := []OwnedBase{{GameID: 515, Title: "Pokémon: FireRed and LeafGreen Versions", ConsoleID: 5}}
	ix := rapatches.Index{Entries: []rapatches.Entry{
		{Path: "GBA/Hacks/Pokemon LeafGreen/1-a.zip", Console: "GBA", Category: rapatches.Hacks, BaseGame: "Pokemon LeafGreen", GameID: 1},
		{Path: "GBA/Hacks/Pokemon FireRed/2-b.zip", Console: "GBA", Category: rapatches.Hacks, BaseGame: "Pokemon FireRed", GameID: 2},
		// Unrelated game on a console we own: not worth a download.
		{Path: "GBA/Hacks/Golden Sun/3-c.zip", Console: "GBA", Category: rapatches.Hacks, BaseGame: "Golden Sun", GameID: 3},
		// Console we own nothing for.
		{Path: "N64/Hacks/Pokemon Snap/4-d.zip", Console: "N64", Category: rapatches.Hacks, BaseGame: "Pokemon Snap", GameID: 4},
	}}

	facts := LoadFactsCache(filepath.Join(t.TempDir(), "f.json"))
	got := Candidates(bases, ix, facts)
	picked := map[int]bool{}
	for _, c := range got {
		picked[c.Entry.GameID] = true
	}
	if !picked[1] || !picked[2] {
		t.Errorf("both Pokemon folders should be candidates, got %v", picked)
	}
	if picked[3] {
		t.Error("an unrelated game should not be downloaded")
	}
	if picked[4] {
		t.Error("a console with no ROMs should be skipped")
	}

	// Already fingerprinted means already done.
	facts.set("GBA/Hacks/Pokemon LeafGreen/1-a.zip", PatchFacts{HasCRC: true})
	for _, c := range Candidates(bases, ix, facts) {
		if c.Entry.GameID == 1 {
			t.Error("a checked patch should not be queued again")
		}
	}
}

// A fingerprint must never remove a hack. The first version let a
// checksum mismatch drop one, and 99 entries vanished from a real
// device's list — every hack whose patch wants a different region or
// revision of a game the user does own. Those are exactly the ones worth
// showing, with the reason attached.
func TestFingerprintNeverHidesAHack(t *testing.T) {
	lib := ownedLib(t, 0x11111111, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/roms/gba/Some Game (USA).gba")
	bases := []OwnedBase{{GameID: 1, Title: "Some Game", ConsoleID: 5, ROMs: lib.ROMs}}
	entry := rapatches.Entry{
		Path: "GBA/Hacks/Some Game/903-Hack.zip", Console: "GBA",
		Category: rapatches.Hacks, BaseGame: "Some Game", GameID: 903, File: "903-Hack.zip",
	}
	ix := rapatches.Index{Entries: []rapatches.Entry{entry}}
	sets := map[int]SetInfo{903: {Title: "A Hack", Achievements: 5}}

	facts := LoadFactsCache(filepath.Join(t.TempDir(), "f.json"))
	facts.set(entry.Path, PatchFacts{
		SourceCRC: 0x99999999, HasCRC: true,
		BaseFile: "Some Game (Europe).gba",
	})

	cat := Build(bases, ix, sets, facts, lib)
	if len(cat.Hacks) != 1 {
		t.Fatalf("the hack should still be listed, got %d", len(cat.Hacks))
	}
	h := cat.Hacks[0]
	if !h.NeedsOtherDump {
		t.Error("it should be flagged as needing another dump")
	}
	if h.WantedDump != "Some Game (Europe).gba" {
		t.Errorf("the needed dump should be named, got %q", h.WantedDump)
	}
}

// PlayStation images are never fingerprinted, so a mismatch there proves
// nothing and must not flag every disc hack.
func TestDiscHacksAreNotFlagged(t *testing.T) {
	lib := &library.Library{ROMs: []library.ROM{
		{Path: "/roms/psx/Crash Bandicoot 2 (USA).chd", ConsoleID: 12, Disc: true},
	}}
	lib.Index()
	bases := []OwnedBase{{GameID: 2, Title: "Crash Bandicoot 2: Cortex Strikes Back", ConsoleID: 12, ROMs: lib.ROMs}}
	entry := rapatches.Entry{
		Path:    "PlayStation/Hacks/Crash Bandicoot 2 - Cortex Strikes Back/904-Sonic.zip",
		Console: "PlayStation", Category: rapatches.Hacks,
		BaseGame: "Crash Bandicoot 2 - Cortex Strikes Back", GameID: 904, File: "904-Sonic.zip",
	}
	ix := rapatches.Index{Entries: []rapatches.Entry{entry}}
	sets := map[int]SetInfo{904: {Title: "Sonic in Crash Bandicoot 2", Achievements: 13}}

	facts := LoadFactsCache(filepath.Join(t.TempDir(), "f.json"))
	facts.set(entry.Path, PatchFacts{SourceCRC: 0xF5E2EC49, HasCRC: true})

	cat := Build(bases, ix, sets, facts, lib)
	if len(cat.Hacks) != 1 {
		t.Fatalf("the disc hack should be listed, got %d", len(cat.Hacks))
	}
	if cat.Hacks[0].NeedsOtherDump {
		t.Error("a disc hack must not be flagged: discs are never fingerprinted")
	}
}

// One achievement set can patch either of two games. "Pokémon Regulation
// Red | Regulation Blue" is a single set with one game ID, filed in the
// repository under both Pokemon Red and Pokemon Blue — so someone who
// owns both cartridges saw the same hack listed twice, two rows that
// install the same thing.
func TestOneSetFiledUnderTwoGamesIsListedOnce(t *testing.T) {
	red := library.ROM{Path: "/roms/gb/Pokemon Red (USA).gb", ConsoleID: 4, CRC32: 0x1111}
	blue := library.ROM{Path: "/roms/gb/Pokemon Blue (USA).gb", ConsoleID: 4, CRC32: 0x2222}
	lib := &library.Library{ROMs: []library.ROM{red, blue}}
	lib.Index()

	bases := []OwnedBase{
		{GameID: 1, Title: "Pokémon Red Version", ConsoleID: 4, ROMs: []library.ROM{red}},
		{GameID: 2, Title: "Pokémon Blue Version", ConsoleID: 4, ROMs: []library.ROM{blue}},
	}
	ix := rapatches.Index{Entries: []rapatches.Entry{
		{Path: "Game Boy/Hacks/Pokemon Red/500-Regulation.zip", Console: "Game Boy",
			Category: rapatches.Hacks, BaseGame: "Pokemon Red Version", GameID: 500, File: "500-Regulation.zip"},
		{Path: "Game Boy/Hacks/Pokemon Blue/500-Regulation.zip", Console: "Game Boy",
			Category: rapatches.Hacks, BaseGame: "Pokemon Blue Version", GameID: 500, File: "500-Regulation.zip"},
	}}
	sets := map[int]SetInfo{500: {Title: "Pokémon Regulation Red | Regulation Blue", Achievements: 252}}

	cat := Build(bases, ix, sets, nil, lib)
	if len(cat.Hacks) != 1 {
		t.Fatalf("got %d rows for one set: %+v", len(cat.Hacks), cat.Hacks)
	}
	// Neither base game may be lost: both cartridges can host it.
	h := cat.Hacks[0]
	if len(h.AlsoHackOf) != 1 {
		t.Errorf("the other base game should be recorded, got %v", h.AlsoHackOf)
	}
	if len(h.BasePaths) != 2 {
		t.Errorf("both ROMs should stay usable, got %v", h.BasePaths)
	}
}

// The in-memory ceiling must choose a strategy, not decide whether an
// archive gets checked. Nintendo DS is the case on the horizon: a patch
// against a 512 MB cartridge is not small, and 143 DS hacks are already
// in the repository — a ceiling that skipped them would send exactly
// those back to being matched by name.
func TestLargeArchivesAreStagedNotSkipped(t *testing.T) {
	big := make([]byte, 9<<20) // past maxVerifyBytes
	copy(big, []byte("BPS1"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(big)))
		w.Write(big)
	}))
	defer srv.Close()

	restore := rapatches.SetRawBaseForTest(srv.URL + "/")
	defer restore()

	// A buffered read must refuse it...
	e := rapatches.Entry{Path: "x/Hacks/y/1-a.zip", File: "1-a.zip"}
	_, err := rapatches.Download(context.Background(), srv.Client(), e, maxVerifyBytes)
	if !errors.Is(err, rapatches.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}

	// ...and the staged path must accept it.
	dir := t.TempDir()
	path, err := rapatches.DownloadTo(context.Background(), srv.Client(), e, dir)
	if err != nil {
		t.Fatalf("DownloadTo: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(big)) {
		t.Errorf("staged %d bytes, want %d", info.Size(), len(big))
	}
}

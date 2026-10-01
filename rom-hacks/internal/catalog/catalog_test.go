package catalog

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"leaf-hacks/internal/library"
	"leaf-hacks/internal/rahub"
	"leaf-hacks/internal/rapatches"
)

const targetMD5 = "ae36e984ab3da0248456ea25b61ddfe6"

func TestNormalizeTitle(t *testing.T) {
	same := [][2]string{
		// RA writes a colon; a folder name cannot, so it becomes " - ".
		{"Fire Emblem: The Sacred Stones", "Fire Emblem - The Sacred Stones"},
		// The article moves to the end in the repository's folder names.
		{"The 7th Saga", "7th Saga, The"},
		{"The Legend of Zelda: A Link to the Past", "Legend of Zelda, The - A Link to the Past"},
		// Numbering style differs between the two sources.
		{"Shining Force II", "Shining Force 2"},
		{"Pokemon FireRed", "pokemon firered"},
	}
	for _, p := range same {
		if !TitlesMatch(p[0], p[1]) {
			t.Errorf("%q and %q should match (%q vs %q)", p[0], p[1], NormalizeTitle(p[0]), NormalizeTitle(p[1]))
		}
	}

	different := [][2]string{
		{"Super Mario World", "Super Mario World 2 - Yoshi's Island"},
		{"Final Fantasy IV", "Final Fantasy VI"},
		{"Mega Man X", "Mega Man"},
	}
	for _, p := range different {
		if TitlesMatch(p[0], p[1]) {
			t.Errorf("%q and %q should NOT match", p[0], p[1])
		}
	}
}

func TestDisplayTitle(t *testing.T) {
	cases := map[string]string{
		"36943-FE8-HagInWhite.7z":                "FE8 Hag In White",
		"44820-ActRaiser-Redone.zip":             "Act Raiser Redone",
		"19875-SMWCP2.zip":                       "SMWCP2",
		"7723-SuperMetroid-SuperZeroMission.zip": "Super Metroid Super Zero Mission",
	}
	for in, want := range cases {
		if got := DisplayTitle(in); got != want {
			t.Errorf("DisplayTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// Build must only surface hacks whose base game is actually on the device.
func TestBuildFiltersToOwnedGames(t *testing.T) {
	bases := []OwnedBase{
		{GameID: 228, Title: "Super Mario World", ConsoleID: 3,
			ROMs: []library.ROM{{Path: "/roms/snes/smw.sfc", System: "snes", ConsoleID: 3}}},
	}
	ix := rapatches.Index{Entries: []rapatches.Entry{
		{Path: "SNES/Hacks/Super Mario World/1-A.zip", Console: "SNES", Category: rapatches.Hacks, BaseGame: "Super Mario World", GameID: 1, File: "1-A.zip"},
		{Path: "SNES/Hacks/Chrono Trigger/2-B.zip", Console: "SNES", Category: rapatches.Hacks, BaseGame: "Chrono Trigger", GameID: 2, File: "2-B.zip"},
		// Right game, wrong console folder: must not match.
		{Path: "GBA/Hacks/Super Mario World/3-C.zip", Console: "GBA", Category: rapatches.Hacks, BaseGame: "Super Mario World", GameID: 3, File: "3-C.zip"},
		// Right game, but a translation rather than a hack.
		{Path: "SNES/Translation/Super Mario World/4-D.zip", Console: "SNES", Category: rapatches.Translation, BaseGame: "Super Mario World", GameID: 4, File: "4-D.zip"},
	}}

	cat := Build(bases, ix, nil, nil, nil)
	if len(cat.Hacks) != 1 || cat.Hacks[0].GameID != 1 {
		t.Fatalf("expected only the SNES Super Mario World hack, got %+v", cat.Hacks)
	}
	if cat.Hacks[0].BasePaths[0] != "/roms/snes/smw.sfc" {
		t.Errorf("base path not carried through: %+v", cat.Hacks[0].BasePaths)
	}

	// Asking for translations too widens the list without loosening the
	// console or title checks.
	cat = Build(bases, ix, nil, nil, nil, rapatches.Hacks, rapatches.Translation)
	if len(cat.Hacks) != 2 {
		t.Fatalf("expected 2 entries with translations enabled, got %d", len(cat.Hacks))
	}
}

// choosePatch has to pair the patch with the hash it satisfies, and pick
// the newest version when an archive ships several.
func TestChoosePatchPrefersNewestSupportedVersion(t *testing.T) {
	payload := rapatches.Payload{Patches: []rapatches.PatchFile{
		{Name: "Hack (v2.3) (Author).bps"},
		{Name: "Hack (v2.5) (Author).bps"},
		{Name: "Hack (v2.4) (Author).bps"},
		{Name: "Extras/Optional/No Music.ips"},
	}}
	hashes := []rahub.HashEntry{
		{Name: "Hack (v2.4) (Author).gba", MD5: "aaa"},
		{Name: "Hack (v2.5) (Author).gba", MD5: "bbb"},
	}
	file, expected, alts := choosePatch(payload, hashes)
	if file.Stem() != "Hack (v2.5) (Author)" {
		t.Errorf("chose %q, want the v2.5 patch", file.Stem())
	}
	if expected.MD5 != "bbb" {
		t.Errorf("paired with hash %q", expected.MD5)
	}
	if len(alts) != 1 || alts[0] != "Hack (v2.4) (Author)" {
		t.Errorf("alternatives = %v", alts)
	}
	// v2.3 has no achievement set and must not be offered at all.
	for _, a := range alts {
		if strings.Contains(a, "v2.3") {
			t.Errorf("unsupported v2.3 offered as an alternative")
		}
	}
}

// The full path: scan a ROM folder, open a real patch archive, match the
// base ROM by the patch's own checksum, apply it and install.
func TestInstallEndToEnd(t *testing.T) {
	root := t.TempDir()
	snes := filepath.Join(root, "snes")
	if err := os.MkdirAll(snes, 0o755); err != nil {
		t.Fatal(err)
	}
	base := mustRead(t, filepath.Join("testdata", "base.sfc"))
	if err := os.WriteFile(filepath.Join(snes, "Test Game (USA).sfc"), base, 0o644); err != nil {
		t.Fatal(err)
	}

	lib, err := library.Scan(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.ROMs) != 1 {
		t.Fatalf("scanned %d ROMs, want 1", len(lib.ROMs))
	}

	archive := mustRead(t, filepath.Join("testdata", "9999-TestGame-SuperHack.zip"))
	payload, err := rapatches.Open("9999-TestGame-SuperHack.zip", archive)
	if err != nil {
		t.Fatal(err)
	}

	hack := Hack{
		GameID: 9999, Title: "Super Hack", BaseTitle: "Test Game", ConsoleID: 3,
		BasePaths: []string{filepath.Join(snes, "Test Game (USA).sfc")},
	}
	expected := rahub.HashEntry{Name: "Test Game - Super Hack (v1.0) (Nobody).sfc", MD5: targetMD5}
	file, _, _ := choosePatch(payload, []rahub.HashEntry{expected})

	found, _, err := findBase(file, payload, lib, hack)
	if err != nil {
		t.Fatalf("findBase: %v", err)
	}
	if filepath.Base(found.Path) != "Test Game (USA).sfc" {
		t.Fatalf("matched the wrong ROM: %s", found.Path)
	}

	plan := &Plan{
		Hack: hack, Patch: file, Base: found,
		Expected: expected, Supported: []rahub.HashEntry{expected},
		Payload: payload,
	}
	dest, err := Install(plan, root)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := mustRead(t, dest)
	want := mustRead(t, filepath.Join("testdata", "expected.sfc"))
	if string(got) != string(want) {
		t.Fatalf("installed ROM differs from the expected patched output")
	}
	// The patched ROM must land in the system's hacks folder, and the
	// original must still be sitting untouched beside it.
	if want := filepath.Join(snes, "hacks"); filepath.Dir(dest) != want {
		t.Errorf("installed into %s, want %s", filepath.Dir(dest), want)
	}
	original := filepath.Join(snes, "Test Game (USA).sfc")
	if got := mustRead(t, original); string(got) != string(base) {
		t.Error("the original ROM was modified")
	}
}

// A patched ROM that RetroAchievements would not recognise must never
// reach /roms: it would boot fine and quietly earn nothing.
func TestInstallRefusesUnrecognisedResult(t *testing.T) {
	root := t.TempDir()
	snes := filepath.Join(root, "snes")
	os.MkdirAll(snes, 0o755)
	basePath := filepath.Join(snes, "Test Game (USA).sfc")
	os.WriteFile(basePath, mustRead(t, filepath.Join("testdata", "base.sfc")), 0o644)

	lib, _ := library.Scan(root, nil, nil)
	archive := mustRead(t, filepath.Join("testdata", "9999-TestGame-SuperHack.zip"))
	payload, _ := rapatches.Open("x.zip", archive)
	file, _ := payload.Select("")

	wrong := rahub.HashEntry{Name: "Whatever.sfc", MD5: "00000000000000000000000000000000"}
	plan := &Plan{
		Hack:     Hack{GameID: 9999, ConsoleID: 3},
		Patch:    file,
		Base:     lib.ROMs[0],
		Expected: wrong, Supported: []rahub.HashEntry{wrong},
		Payload: payload,
	}
	if _, err := Install(plan, root); err == nil {
		t.Fatal("expected the install to be refused")
	}
	if entries, err := os.ReadDir(filepath.Join(snes, "hacks")); err == nil && len(entries) > 0 {
		t.Fatalf("a file was written despite the hash mismatch: %v", entries)
	}
}

// Having the game but not the right dump is the common failure, and the
// message has to say which it is.
func TestFindBaseReportsWrongRevision(t *testing.T) {
	root := t.TempDir()
	snes := filepath.Join(root, "snes")
	os.MkdirAll(snes, 0o755)
	base := mustRead(t, filepath.Join("testdata", "base.sfc"))
	altered := make([]byte, len(base))
	copy(altered, base)
	altered[7] ^= 0xff // same game, different revision
	os.WriteFile(filepath.Join(snes, "Test Game (Europe).sfc"), altered, 0o644)

	lib, _ := library.Scan(root, nil, nil)
	archive := mustRead(t, filepath.Join("testdata", "9999-TestGame-SuperHack.zip"))
	payload, _ := rapatches.Open("x.zip", archive)
	file, _ := payload.Select("")

	hack := Hack{ConsoleID: 3, BaseTitle: "Test Game", BasePaths: []string{filepath.Join(snes, "Test Game (Europe).sfc")}}
	_, _, err := findBase(file, payload, lib, hack)
	var missing *ErrNoBaseROM
	if err == nil {
		t.Fatal("expected a missing-base-ROM error")
	}
	if !asErrNoBaseROM(err, &missing) {
		t.Fatalf("got %T: %v", err, err)
	}
	if len(missing.Near) == 0 {
		t.Error("expected the near-miss file to be reported")
	}
	if len(missing.Wanted) == 0 {
		t.Error("expected the wanted dump to be described")
	}
}

func asErrNoBaseROM(err error, out **ErrNoBaseROM) bool {
	e, ok := err.(*ErrNoBaseROM)
	if ok {
		*out = e
	}
	return ok
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// The Pokémon hacks — 228 of them on GBA alone — were invisible for two
// separate reasons at once, which is why this is tested explicitly.
func TestPokemonTitlesMatch(t *testing.T) {
	m := newTitleMatcher[string]()
	// Titles exactly as RetroAchievements writes them.
	m.add("Pokémon: Emerald Version", "emerald")
	m.add("Pokémon: FireRed Version", "firered")
	m.add("Pokémon: LeafGreen Version", "leafgreen")
	m.add("Pokémon: Ruby Version", "ruby")

	// Folder names exactly as RAPatches writes them: no accent, no
	// "Version".
	for folder, want := range map[string]string{
		"Pokemon Emerald":   "emerald",
		"Pokemon FireRed":   "firered",
		"Pokemon LeafGreen": "leafgreen",
		"Pokemon Ruby":      "ruby",
	} {
		got, ok := m.find(folder)
		if !ok || got != want {
			t.Errorf("find(%q) = %q, %v; want %q", folder, got, ok, want)
		}
	}
}

// Loosening the match must not make it reckless: a prefix that could mean
// two different games has to find nothing rather than guess.
func TestAmbiguousPrefixRefusesToMatch(t *testing.T) {
	m := newTitleMatcher[string]()
	m.add("Super Mario World", "smw")
	m.add("Super Mario Bros.", "smb")

	if got, ok := m.find("Super Mario"); ok {
		t.Errorf("find(\"Super Mario\") matched %q; it is ambiguous and should not", got)
	}
	// The full names still resolve.
	if got, ok := m.find("Super Mario World"); !ok || got != "smw" {
		t.Errorf("exact match broke: %q %v", got, ok)
	}
}

func TestAccentFolding(t *testing.T) {
	if !TitlesMatch("Pokémon Emerald", "Pokemon Emerald") {
		t.Error("accents should not separate the same title")
	}
	if !TitlesMatch("Pokémon: Emerald Version", "Pokemon Emerald") {
		t.Error("filler words should not separate the same title")
	}
}

// RAPatches files compatibility patches under the base game's own set ID:
// four files in NES/Hacks/Ninja Gaiden all carry 1859, which is Ninja
// Gaiden itself. They are not hacks, and listing them produced four
// identical rows wearing the base game's title and achievement count.
func TestCompatibilityPatchesAreNotHacks(t *testing.T) {
	bases := []OwnedBase{{
		GameID: 1859, Title: "Ninja Gaiden", ConsoleID: 7,
		ROMs: []library.ROM{{Path: "/roms/nes/Ninja Gaiden (USA).zip", System: "nes", ConsoleID: 7}},
	}}
	ix := rapatches.Index{Entries: []rapatches.Entry{
		{Path: "NES/Hacks/Ninja Gaiden/1859-NinjaGaidenPtEmuSamba.zip", Console: "NES", Category: rapatches.Hacks, BaseGame: "Ninja Gaiden", GameID: 1859, File: "1859-a.zip"},
		{Path: "NES/Hacks/Ninja Gaiden/1859-NinjaGaidenPtHellmatic.zip", Console: "NES", Category: rapatches.Hacks, BaseGame: "Ninja Gaiden", GameID: 1859, File: "1859-b.zip"},
		{Path: "NES/Hacks/Ninja Gaiden/13422-NinjaGaidenDragonScroll.zip", Console: "NES", Category: rapatches.Hacks, BaseGame: "Ninja Gaiden", GameID: 13422, File: "13422-NinjaGaidenDragonScroll.zip"},
	}}
	sets := map[int]SetInfo{
		1859:  {Title: "Ninja Gaiden", Achievements: 35, Points: 525},
		13422: {Title: "~Hack~ Ninja Gaiden: The Dragon Scroll", Achievements: 20, Points: 200},
	}

	cat := Build(bases, ix, sets, nil, nil)
	if len(cat.Hacks) != 1 {
		t.Fatalf("got %d hacks, want 1: %+v", len(cat.Hacks), cat.Hacks)
	}
	if cat.Hacks[0].GameID != 13422 {
		t.Errorf("kept the wrong entry: %+v", cat.Hacks[0])
	}
	if cat.CompatibilityPatches != 2 {
		t.Errorf("counted %d compatibility patches, want 2", cat.CompatibilityPatches)
	}
}

func TestCleanSetTitle(t *testing.T) {
	cases := map[string]string{
		"~Hack~ Bald Bull's Punch-Out!!": "Bald Bull's Punch-Out!!",
		"~Hack~ Ninja Gaiden: Dragon":    "Ninja Gaiden: Dragon",
		"Pokemon Emerald - Special Ed":   "Pokemon Emerald - Special Ed",
	}
	for in, want := range cases {
		if got := CleanSetTitle(in); got != want {
			t.Errorf("CleanSetTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// A hack's set title and the filename RetroAchievements expects are
// routinely different strings for the same thing — "Pokémon Emerald Rogue
// V2" against "Pokemon Emerald - Emerald Rogue (v2.0).gba". Matching by
// title therefore loses the install, and Play reports the hack as missing
// moments after it was written. The registry records the game ID instead.
func TestRegistryFindsInstallsTitleMatchingWouldMiss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed.json")
	reg := LoadRegistry(path)

	romPath := "/roms/gba/hacks/Pokemon Emerald - Emerald Rogue (v2.0).gba"
	if err := reg.Add(Record{GameID: 30137, Path: romPath, Stem: "Pokemon Emerald - Emerald Rogue (v2.0)"}); err != nil {
		t.Fatal(err)
	}
	// The titles do not match, and no normalisation brings them together.
	if TitlesMatch("Pokémon Emerald Rogue V2", "Pokemon Emerald - Emerald Rogue (v2.0)") {
		t.Fatal("these titles should not match; the test premise is wrong")
	}
	// The registry does not care.
	rec, ok := reg.Get(30137)
	if !ok || rec.Path != romPath {
		t.Fatalf("registry lookup failed: %+v %v", rec, ok)
	}

	// And it survives a restart.
	reloaded := LoadRegistry(path)
	if rec, ok := reloaded.Get(30137); !ok || rec.Path != romPath {
		t.Errorf("record did not survive reload: %+v %v", rec, ok)
	}
}

// Records for files that are gone must not keep claiming the hack is
// installed.
func TestRegistryPrunesMissingFiles(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "here.gba")
	os.WriteFile(present, []byte("x"), 0o644)

	reg := LoadRegistry(filepath.Join(dir, "installed.json"))
	reg.Add(Record{GameID: 1, Path: present})
	reg.Add(Record{GameID: 2, Path: filepath.Join(dir, "gone.gba")})

	if n := reg.Prune(); n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	if _, ok := reg.Get(2); ok {
		t.Error("the missing file is still recorded")
	}
	if _, ok := reg.Get(1); !ok {
		t.Error("the present file was pruned")
	}
}

// Picking "the newest version" by string is wrong in a way that hides:
// v2.10 sorts before v2.9 alphabetically, so the app would install a build
// from ten releases ago and nothing would look broken.
func TestVersionOrdering(t *testing.T) {
	newer := [][2]string{
		{"Hack (v2.10)", "Hack (v2.9)"},
		{"Hack (v10.0)", "Hack (v9.9)"},
		{"Hack (v1.2.3)", "Hack (v1.2)"},
		{"Hack (v2.0)", "Hack (v1.99)"},
		{"Hack (v1.0)", "Hack"}, // numbered beats unnumbered
	}
	for _, p := range newer {
		if !NewerVersion(p[0], p[1]) {
			t.Errorf("%q should be newer than %q", p[0], p[1])
		}
		if NewerVersion(p[1], p[0]) {
			t.Errorf("%q should NOT be newer than %q", p[1], p[0])
		}
	}

	// A number in the title must not be mistaken for the version.
	if !NewerVersion("FE8 - The Hag in White (v2.5)", "FE8 - The Hag in White (v2.4)") {
		t.Error("the version nearest the end should decide")
	}
	if got := VersionOf("Pokemon Emerald - Emerald Rogue (v2.0)"); len(got) != 2 || got[0] != 2 || got[1] != 0 {
		t.Errorf("VersionOf = %v", got)
	}
}

// The newest supported version is what an archive of several should yield.
func TestChoosePatchPicksNewestByNumber(t *testing.T) {
	payload := rapatches.Payload{Patches: []rapatches.PatchFile{
		{Name: "Hack (v2.9).bps"},
		{Name: "Hack (v2.10).bps"},
	}}
	hashes := []rahub.HashEntry{
		{Name: "Hack (v2.9).gba", MD5: "old"},
		{Name: "Hack (v2.10).gba", MD5: "new"},
	}
	file, expected, _ := choosePatch(payload, hashes)
	if file.Stem() != "Hack (v2.10)" || expected.MD5 != "new" {
		t.Errorf("chose %q paired with %q; want the v2.10 pair", file.Stem(), expected.MD5)
	}
}

// Plenty of cards hold two dumps of the same game — a "Pokemon - Crystal
// Version (UE) (V1.0)" and a "(USA, Europe) (Rev 1)" side by side. A bare
// IPS declares no checksum, so nothing in the archive can say which one
// it wants, and trying only the first means the patch fails on a card
// that has exactly what it needs.
func TestInstallTriesEveryCopyOfTheBaseGame(t *testing.T) {
	root := t.TempDir()
	gbc := filepath.Join(root, "gbc")
	if err := os.MkdirAll(gbc, 0o755); err != nil {
		t.Fatal(err)
	}
	v10 := filepath.Join(gbc, "Pokemon - Crystal Version (UE) (V1.0) [C][!].gbc")
	rev1 := filepath.Join(gbc, "Pokemon - Crystal Version (USA, Europe) (Rev 1).gbc")
	os.WriteFile(v10, mustRead(t, filepath.Join("testdata", "copy-v10.gbc")), 0o644)
	os.WriteFile(rev1, mustRead(t, filepath.Join("testdata", "copy-rev1.gbc")), 0o644)

	lib, err := library.Scan(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	archive := mustRead(t, filepath.Join("testdata", "8888-TwoCopies.zip"))
	payload, err := rapatches.Open("8888-TwoCopies.zip", archive)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := payload.Select("")

	want := mustRead(t, filepath.Join("testdata", "copy-target.gbc"))
	expected := rahub.HashEntry{Name: "Two Copies Hack (v1.0).gbc", MD5: md5Of(want)}

	// The order is deliberately wrong: the copy that cannot work first.
	hack := Hack{GameID: 8888, Title: "Two Copies Hack", BaseTitle: "Pokemon Crystal", ConsoleID: 6}
	plan := &Plan{
		Hack: hack, Patch: file,
		Base:      romAt(lib, v10),
		Bases:     []library.ROM{romAt(lib, v10), romAt(lib, rev1)},
		Expected:  expected,
		Supported: []rahub.HashEntry{expected},
		Payload:   payload,
	}

	dest, err := Install(plan, root)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := mustRead(t, dest); string(got) != string(want) {
		t.Fatal("the installed ROM is not the expected patched output")
	}
	// And the plan must now name the copy that actually worked, so the
	// page and the install record do not credit the wrong file.
	if filepath.Base(plan.Base.Path) != filepath.Base(rev1) {
		t.Errorf("plan still names %s", filepath.Base(plan.Base.Path))
	}
}

func romAt(lib *library.Library, path string) library.ROM {
	for _, r := range lib.ROMs {
		if r.Path == path {
			return r
		}
	}
	return library.ROM{}
}

func md5Of(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

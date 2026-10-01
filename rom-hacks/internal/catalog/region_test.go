package catalog

import "testing"

// The Crash Bandicoot 2 case: the hack is built against the Europe disc
// and the dump on the card is USA. Before this, the only feedback was
// "CRC32 395C0916, patch wants F5E2EC49" — true, and useless.
func TestRegionMismatchIsExplained(t *testing.T) {
	m, mismatch := CheckRegion(
		"Crash Bandicoot 2 - Cortex Strikes Back (Europe).bin",
		"Crash Bandicoot 2 - Cortex Strikes Back (USA).chd",
	)
	if !mismatch {
		t.Fatal("a Europe patch against a USA dump should be flagged")
	}
	if m.Wanted != RegionEurope || m.Have != RegionUSA {
		t.Errorf("read regions as %s/%s", m.Wanted, m.Have)
	}
	msg := m.Message()
	for _, want := range []string{"Europe", "USA", "(Europe)"} {
		if !contains(msg, want) {
			t.Errorf("message should mention %q: %s", want, msg)
		}
	}
}

// A warning that fires on a ROM which would have worked is worse than no
// warning, so anything uncertain stays quiet.
func TestRegionCheckStaysQuietWhenUnsure(t *testing.T) {
	quiet := [][2]string{
		{"Some Hack Base.bin", "Some Game (USA).chd"},  // patch says nothing
		{"Game (World).bin", "Game (USA).chd"},         // World fits anything
		{"Game (USA).bin", "Game (World).chd"},         // and the other way
		{"Game (USA).bin", "Game (USA).chd"},           // same everything
		{"Game (Europe).bin", "my weird filename.chd"}, // local region unknown
	}
	for _, c := range quiet {
		if _, mismatch := CheckRegion(c[0], c[1]); mismatch {
			t.Errorf("should have stayed quiet: %q vs %q", c[0], c[1])
		}
	}
}

func TestRegionOf(t *testing.T) {
	cases := map[string]Region{
		"Game (USA).gba":           RegionUSA,
		"Game (Europe) (Rev 1).gb": RegionEurope,
		"Game (Japan).sfc":         RegionJapan,
		"Game (World).nes":         RegionWorld,
		"Game.nes":                 RegionUnknown,
	}
	for name, want := range cases {
		if got := RegionOf(name); got != want {
			t.Errorf("RegionOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// A dump filename carries tags in any order. Cutting at the first region
// tag only works when the region comes first — with the language list in
// front, the title came out with "en fr de es it" glued on and matched
// nothing.
func TestNormalizeDumpNameStripsEveryTag(t *testing.T) {
	same := []string{
		"Crash Bandicoot 2 - Cortex Strikes Back (Europe) (En,Fr,De,Es,It).chd",
		"Crash Bandicoot 2 - Cortex Strikes Back (En,Fr,De,Es,It) (Europe).chd",
		"Crash Bandicoot 2 - Cortex Strikes Back (USA).bin",
		"Crash Bandicoot 2 - Cortex Strikes Back (USA) (Rev 1) [!].cue",
	}
	want := NormalizeDumpName(same[0])
	if want == "" {
		t.Fatal("normalised to nothing")
	}
	for _, name := range same[1:] {
		if got := NormalizeDumpName(name); got != want {
			t.Errorf("NormalizeDumpName(%q) = %q, want %q", name, got, want)
		}
	}
	// And it still matches how RetroAchievements titles the game.
	if !TitlesMatch(want, "Crash Bandicoot 2: Cortex Strikes Back") {
		t.Errorf("%q does not match the RA title", want)
	}
}

// Same region, different pressing: just as opaque a failure, just as
// fixable once named.
func TestRevisionMismatchIsExplained(t *testing.T) {
	m, mismatch := CheckRegion("Final Fantasy IX (USA).bin", "Final Fantasy IX (USA) (Rev 1).chd")
	if !mismatch {
		t.Fatal("a Rev 1 dump against an original-pressing patch should be flagged")
	}
	if !contains(m.Message(), "revision") && !contains(m.Message(), "original pressing") {
		t.Errorf("message should name the revision: %s", m.Message())
	}
	// Two dumps of the same pressing stay quiet.
	if _, mismatch := CheckRegion("Game (USA) (Rev 1).bin", "Game (USA) (Rev 1).chd"); mismatch {
		t.Error("identical revisions should not be flagged")
	}
}

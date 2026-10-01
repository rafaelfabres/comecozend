package catalog

import (
	"strings"
	"testing"
)

// The readme is the one place a hack reliably describes itself, and it
// was being read for a single checksum and then thrown away. What has to
// survive is the prose; what has to go is the base-ROM block, which is
// machinery and takes up the whole first screen.
func TestReadmeNotesKeepsProseAndDropsTheBaseROMBlock(t *testing.T) {
	const readme = `Metroid: Rogue Dawn

A prequel to the original Metroid. You play as Dawn, an operative sent
to infiltrate the planet before Samus ever arrives.

Features:
  - An entirely new map, 20 areas
  - New enemies, bosses and music
  - Rebalanced weapons and a new beam

Use with:

(No Intro)
File:               Metroid (USA).nes
BitSize:            1 Mbit
Size (Bytes):       131088
CRC32:              2D41EFD4
MD5:                A8553F6E93E2D4E2D1B1A0D3E9F3E5C1
SHA1:               8E1C2F3A4B5C6D7E8F9A0B1C2D3E4F5A6B7C8D9E
`
	notes := ReadmeNotes(readme)
	for _, want := range []string{"prequel", "entirely new map", "Rebalanced weapons"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the description should survive, missing %q:\n%s", want, notes)
		}
	}
	for _, gone := range []string{"Use with", "CRC32", "2D41EFD4", "Metroid (USA).nes", "SHA1"} {
		if strings.Contains(notes, gone) {
			t.Errorf("the base-ROM block should be gone, found %q", gone)
		}
	}
	if strings.Contains(notes, "\n\n\n") {
		t.Error("blank runs should be collapsed")
	}
}

// A readme that is only the base-ROM block has nothing to show, and
// opening a page to display two blank lines is worse than saying so.
func TestReadmeNotesReturnsNothingForMachineryOnly(t *testing.T) {
	const readme = `Use with:

(No Intro)
File:               ActRaiser (USA).sfc
Size (Bytes):       1048576
CRC32:              EAC3358D
MD5:                635D5D7DD2AAD4768412FBAE4A32FD6E
`
	if got := ReadmeNotes(readme); got != "" {
		t.Errorf("expected nothing, got:\n%s", got)
	}
	if got := ReadmeNotes(""); got != "" {
		t.Errorf("expected nothing for an empty readme, got %q", got)
	}
}

// The compact readme form puts bare hashes on their own lines.
func TestReadmeNotesDropsBareHashes(t *testing.T) {
	const readme = `Pokemon Emerald Rogue

A roguelike run through Hoenn. Every route is randomised and you keep
nothing between attempts.

Use with:

No Intro
Pokemon - Emerald Version (USA, Europe).gba
605b89b67018abcea91e693a4dd25be3
1f1c08fb
`
	notes := ReadmeNotes(readme)
	if !strings.Contains(notes, "roguelike run through Hoenn") {
		t.Errorf("the description was lost:\n%s", notes)
	}
	if strings.Contains(notes, "605b89b") || strings.Contains(notes, "1f1c08fb") {
		t.Errorf("bare hashes should be dropped:\n%s", notes)
	}
}

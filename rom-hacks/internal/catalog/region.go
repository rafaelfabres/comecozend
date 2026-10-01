package catalog

import (
	"regexp"
	"strings"
)

// Region is the release a dump comes from. Getting this wrong is the most
// common reason a patch refuses a ROM, and the raw checksum mismatch that
// results says nothing about what to go and look for.
type Region string

const (
	RegionUnknown Region = ""
	RegionUSA     Region = "USA"
	RegionEurope  Region = "Europe"
	RegionJapan   Region = "Japan"
	RegionWorld   Region = "World"
)

// regionTags are the markers No-Intro and Redump put in a filename, plus
// the shorthands that turn up in hack archives.
var regionTags = []struct {
	needles []string
	region  Region
}{
	{[]string{"(usa", "(us)", "(u)", "(ntsc-u", "(america"}, RegionUSA},
	{[]string{"(europe", "(eur", "(e)", "(pal", "(uk", "(germany", "(france", "(spain", "(italy"}, RegionEurope},
	{[]string{"(japan", "(jpn", "(j)", "(ntsc-j", "(jp)"}, RegionJapan},
	{[]string{"(world"}, RegionWorld},
}

// RegionOf reads the region out of a dump filename.
func RegionOf(filename string) Region {
	lower := strings.ToLower(filename)
	for _, t := range regionTags {
		for _, n := range t.needles {
			if strings.Contains(lower, n) {
				return t.region
			}
		}
	}
	return RegionUnknown
}

// revisionPattern reads the "(Rev 1)" tag Redump and No-Intro use. A
// revision mismatch produces exactly the same opaque checksum failure as
// a region mismatch, and is just as fixable once named.
var revisionPattern = regexp.MustCompile(`(?i)\(rev\s*([0-9a-z]+)\)`)

// RevisionOf returns the revision tag of a dump filename, or "" for the
// original pressing, which carries no tag.
func RevisionOf(filename string) string {
	m := revisionPattern.FindStringSubmatch(filename)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// RegionMismatch describes a patch and a local ROM that cannot go
// together because they come from different releases.
type RegionMismatch struct {
	Wanted Region
	Have   Region
	// WantedRev and HaveRev are the "(Rev n)" tags, empty for an original
	// pressing.
	WantedRev, HaveRev string
	// WantedFile is the dump the patch names, for the user to go find.
	WantedFile string
}

// Message is the sentence shown on the game page.
func (m RegionMismatch) Message() string {
	switch {
	case m.Wanted != RegionUnknown && m.Have != RegionUnknown && m.Wanted != m.Have:
		return "This patch needs the " + string(m.Wanted) + " release and your copy is " +
			string(m.Have) + ". You would need: " + m.WantedFile
	case m.WantedRev != m.HaveRev:
		return "This patch needs " + revLabel(m.WantedRev) + " and your copy is " +
			revLabel(m.HaveRev) + ". You would need: " + m.WantedFile
	}
	return ""
}

func revLabel(rev string) string {
	if rev == "" {
		return "the original pressing (no Rev tag)"
	}
	return "revision " + strings.ToUpper(rev)
}

// CheckRegion compares the dump a patch asks for against the file on the
// device, and reports a mismatch worth warning about.
//
// A World release satisfies anything and an unknown region on either side
// proves nothing, so both are left alone: a warning that fires on a ROM
// that would have worked is worse than no warning.
func CheckRegion(wantedFile, haveFile string) (RegionMismatch, bool) {
	wanted, have := RegionOf(wantedFile), RegionOf(haveFile)
	wantedRev, haveRev := RevisionOf(wantedFile), RevisionOf(haveFile)
	m := RegionMismatch{
		Wanted: wanted, Have: have,
		WantedRev: wantedRev, HaveRev: haveRev,
		WantedFile: wantedFile,
	}

	// Region first: it is the bigger difference and the clearer message.
	// An untagged filename on either side proves nothing, and neither
	// does a World release, so both stay quiet rather than warn about a
	// ROM that may well be the right one.
	regionKnown := wanted != RegionUnknown && wanted != RegionWorld &&
		have != RegionUnknown && have != RegionWorld
	if regionKnown && wanted != have {
		return m, true
	}

	// Same region (or unknown), different revision: "(Rev 1)" against an
	// original pressing fails exactly as opaquely, and is just as
	// fixable once it is named. Only claimed when the regions agree, so
	// two unrelated dumps are not reported as a revision problem.
	if regionKnown && wanted == have && wantedRev != haveRev {
		return m, true
	}
	return RegionMismatch{}, false
}

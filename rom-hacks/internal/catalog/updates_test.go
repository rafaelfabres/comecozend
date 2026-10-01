package catalog

import "testing"

// The installed filename is sanitised for the filesystem, so it never
// matches the site's name exactly. Comparing the names marked every
// installed hack as outdated — the user went to RetroAchievements, found
// no new version, and was right.
func TestOnlyAHigherVersionCountsAsAnUpdate(t *testing.T) {
	sameVersion := [][2]string{
		{"Final Fantasy Adventure - Embers of Mana (Autumn Brown) (v1.31)",
			"Final Fantasy Adventure - Embers of Mana (Autumn Brown) (v1.31) (Ok Im)"},
		{"Hack (v2.0)", "Hack (v2.0) (Author)"},
	}
	for _, p := range sameVersion {
		if NewerVersion(p[0], p[1]) {
			t.Errorf("%q is not newer than %q", p[0], p[1])
		}
	}

	if !NewerVersion("Hack (v1.32)", "Hack (v1.31)") {
		t.Error("a higher version should count")
	}
	if !NewerVersion("Hack (v2.0)", "Hack (v1.99)") {
		t.Error("a higher major version should count")
	}
}

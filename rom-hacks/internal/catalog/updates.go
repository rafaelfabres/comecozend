package catalog

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"leaf-hacks/internal/rahub"
)

// UpdateCheck records what the last look for newer versions found.
//
// Two things go stale. New hacks get achievement sets all the time, which
// is what refreshing the patch index catches. And a hack already installed
// can fall behind: authors release a v2.5 after a v2.4, RetroAchievements
// moves its set to the new file, and the copy on the card quietly stops
// matching a supported hash — it still boots, and earns nothing. That
// second case is invisible without asking, so this asks.
type UpdateCheck struct {
	Checked time.Time `json:"checked"`
	// Outdated maps a game ID to the file RetroAchievements now expects.
	Outdated map[int]string `json:"outdated,omitempty"`
	// Unsupported lists games whose installed file is no longer a
	// recognised hash at all.
	Unsupported map[int]bool `json:"unsupported,omitempty"`
}

// Stale reports whether the check is old enough to run again.
func (u UpdateCheck) Stale(maxAge time.Duration) bool {
	return u.Checked.IsZero() || time.Since(u.Checked) > maxAge
}

// NeedsUpdate reports whether an installed hack has a newer version.
func (u UpdateCheck) NeedsUpdate(gameID int) (string, bool) {
	want, ok := u.Outdated[gameID]
	return want, ok
}

// Installed describes one hack found on the card, as the scanner saw it.
type Installed struct {
	GameID int
	Stem   string // filename without extension
	RAHash string // empty for disc images, which are not hashed
}

// CheckUpdates asks RetroAchievements which file each installed hack
// should be, and reports the ones that no longer match.
//
// It is deliberately per-installed-hack rather than per-catalogue-entry:
// there are a few dozen installed at most, against a couple of thousand
// listed, so this costs a handful of calls rather than thousands.
func CheckUpdates(ctx context.Context, ra *rahub.RAClient, installed []Installed, note func(string)) UpdateCheck {
	out := UpdateCheck{
		Checked:     time.Now(),
		Outdated:    map[int]string{},
		Unsupported: map[int]bool{},
	}
	for i, inst := range installed {
		if note != nil {
			note("Checking installed hacks " + itoa(i+1) + "/" + itoa(len(installed)))
		}
		hashes, err := ra.FetchGameHashes(ctx, inst.GameID)
		if err != nil || len(hashes) == 0 {
			// Leave it alone rather than claim it is broken: a failed
			// call is not evidence of anything.
			continue
		}

		// Supported by hash is the strong signal, and the only one
		// available once a file has been renamed.
		supported := false
		if inst.RAHash != "" {
			for _, h := range hashes {
				if strings.EqualFold(h.MD5, inst.RAHash) {
					supported = true
					break
				}
			}
			if !supported {
				out.Unsupported[inst.GameID] = true
			}
		}

		// The newest supported file wins, compared by version number.
		newest := ""
		for _, h := range hashes {
			if stem := stemOf(h.Name); newest == "" || NewerVersion(stem, newest) {
				newest = stem
			}
		}
		if newest == "" {
			continue
		}

		// Only a strictly higher version number counts as an update.
		//
		// Comparing the names instead marked a hack as outdated whenever
		// the installed filename differed at all — and it always does,
		// because the name is sanitised for the filesystem on the way in.
		// The user checked the site and found no new version, correctly.
		if NewerVersion(newest, inst.Stem) {
			out.Outdated[inst.GameID] = newest
			continue
		}
		// A file the site no longer supports is worth replacing even
		// without a version bump: it earns nothing as it stands.
		if inst.RAHash != "" && !supported {
			out.Outdated[inst.GameID] = newest
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Save and Load persist the result so the check runs on its own schedule
// rather than on every launch.
func (u UpdateCheck) Save(path string) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadUpdateCheck(path string) (UpdateCheck, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return UpdateCheck{}, err
	}
	var u UpdateCheck
	err = json.Unmarshal(b, &u)
	return u, err
}

// Package rapatches reads RetroAchievements' own patch repository.
//
// RAPatches is a plain GitHub repo whose layout is already a catalog:
//
//	<Console>/<Category>/<Base game>/<GameID>-<ShortName>.zip
//	GBA/Hacks/Fire Emblem - The Sacred Stones/36943-FE8-HagInWhite.7z
//
// Two things make it usable without any scraping. The RetroAchievements
// game ID is the filename prefix, so a file identifies its achievement set
// outright; and the folder above it names the base game, which is what
// lets the app decide, offline, whether a hack is even relevant to the
// ROMs on this device before spending a single API call.
package rapatches

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// defaultRawBase is where patch archives are served from. It is a
// variable so tests can point it at a local server; nothing else changes
// it.
var rawBase = defaultRawBase

const defaultRawBase = "https://github.com/RetroAchievements/RAPatches/raw/refs/heads/main/"

const (
	treeAPI  = "https://api.github.com/repos/RetroAchievements/RAPatches/git/trees/main?recursive=1"
	maxIndex = 16 << 20 // the tree JSON is ~1 MB; this is a sanity ceiling
)

// Category is the second path segment. Hacks is what this app lists;
// the others are kept in the index because they cost nothing to parse and
// a later version may want translations too.
type Category string

const (
	Hacks       Category = "Hacks"
	Translation Category = "Translation"
	Improvement Category = "Improvement"
	Fix         Category = "Fix"
	Subset      Category = "Subset"
)

// Entry is one patch archive in the repository.
type Entry struct {
	Path     string   `json:"path"`      // full path inside the repo
	Console  string   `json:"console"`   // "GBA", "SNES", "MD", ...
	Category Category `json:"category"`  // "Hacks", "Translation", ...
	BaseGame string   `json:"base_game"` // folder name: the game to patch
	GameID   int      `json:"game_id"`   // RetroAchievements set ID
	File     string   `json:"file"`      // archive filename
}

// DownloadURL is the raw URL for this entry's archive.
func (e Entry) DownloadURL() string {
	parts := strings.Split(e.Path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return rawBase + strings.Join(parts, "/")
}

// Index is a snapshot of the repository plus when it was taken, so the
// device can keep using yesterday's copy when it is offline.
type Index struct {
	Fetched time.Time `json:"fetched"`
	Entries []Entry   `json:"entries"`
}

// ByConsole groups hack entries for one console keyed by base-game folder.
func (ix Index) ByConsole(console string, cat Category) map[string][]Entry {
	out := map[string][]Entry{}
	for _, e := range ix.Entries {
		if e.Console == console && e.Category == cat {
			out[e.BaseGame] = append(out[e.BaseGame], e)
		}
	}
	return out
}

var gameIDPrefix = regexp.MustCompile(`^(\d{1,6})[-_ ]`)

// normaliseCategory folds the repository's occasional singular spelling
// into the usual one. Ten archives sit under "Hack" rather than "Hacks",
// and without this they would simply never appear in the list.
func normaliseCategory(s string) Category {
	if s == "Hack" {
		return Hacks
	}
	return Category(s)
}

// patchArchiveExts are the containers actually used in the repo. Anything
// else (a stray .md, .png or self-extracting .exe) is skipped rather than
// offered to the user as something the app can install.
var patchArchiveExts = map[string]bool{".zip": true, ".7z": true}

// nonConsoleDirs are top-level trees that are not patches for a console.
// "Removed" is the important one: it holds withdrawn patches under a
// console folder of their own, so its paths have exactly the same shape
// as a real hack and would otherwise be offered to the user as installable
// ("Removed/Mega Drive/Hacks/565-Sonic1-MetalSonicHyperdrive.zip").
var nonConsoleDirs = map[string]bool{
	"Removed":      true,
	"Incompatible": true,
	"Unsorted":     true,
	"Utilities":    true,
	"Standalone":   true,
	"DLC":          true,
	"Saves":        true,
	"misc":         true,
}

// parseEntry turns a repo path into an Entry, or reports that the path is
// not an installable patch archive.
func parseEntry(p string) (Entry, bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 4 {
		// Every hack lives exactly four levels deep. Shallower paths are
		// README.md and the loose odds and ends at the repo root.
		return Entry{}, false
	}
	if nonConsoleDirs[parts[0]] {
		return Entry{}, false
	}
	file := parts[3]
	if !patchArchiveExts[strings.ToLower(path.Ext(file))] {
		return Entry{}, false
	}
	m := gameIDPrefix.FindStringSubmatch(file)
	if m == nil {
		return Entry{}, false
	}
	id, err := strconv.Atoi(m[1])
	if err != nil || id == 0 {
		return Entry{}, false
	}
	return Entry{
		Path:     p,
		Console:  parts[0],
		Category: normaliseCategory(parts[1]),
		BaseGame: parts[2],
		GameID:   id,
		File:     file,
	}, true
}

// Fetch downloads the repository tree and parses it into an index. One
// request covers the whole catalog, which is why this is cheap enough to
// refresh on demand instead of shipping a bundled copy that goes stale.
func Fetch(ctx context.Context, client *http.Client) (Index, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, treeAPI, nil)
	if err != nil {
		return Index{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "leaf-hacks")

	resp, err := client.Do(req)
	if err != nil {
		return Index{}, fmt.Errorf("RAPatches index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		// GitHub allows 60 unauthenticated calls an hour per address. The
		// cached index is what keeps this from ever mattering in practice.
		return Index{}, fmt.Errorf("GitHub rate limit reached — try again later (the saved list is still usable)")
	}
	if resp.StatusCode != http.StatusOK {
		return Index{}, fmt.Errorf("RAPatches index: HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxIndex)).Decode(&payload); err != nil {
		return Index{}, fmt.Errorf("RAPatches index: %w", err)
	}
	if payload.Truncated {
		return Index{}, fmt.Errorf("RAPatches index came back truncated")
	}

	paths := make([]string, 0, len(payload.Tree))
	for _, node := range payload.Tree {
		if node.Type == "blob" {
			paths = append(paths, node.Path)
		}
	}
	ix := IndexFromPaths(paths, time.Now())
	if len(ix.Entries) == 0 {
		return Index{}, fmt.Errorf("RAPatches index: no patches found — the repository layout may have changed")
	}
	return ix, nil
}

// IndexFromPaths builds an index from a list of repository paths. Fetch
// uses it, and so does the seed generator that ships a first-run index
// with the app, so both produce byte-identical results.
func IndexFromPaths(paths []string, fetched time.Time) Index {
	ix := Index{Fetched: fetched}
	for _, p := range paths {
		if e, ok := parseEntry(p); ok {
			ix.Entries = append(ix.Entries, e)
		}
	}
	return ix
}

// Save and Load persist the index next to the app so a cold start without
// network still shows the same list as yesterday.
func (ix Index) Save(path string) error {
	b, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Load(path string) (Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Index{}, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return Index{}, err
	}
	return ix, nil
}

// Stale reports whether the index is old enough to be worth refreshing.
func (ix Index) Stale(maxAge time.Duration) bool {
	return ix.Fetched.IsZero() || time.Since(ix.Fetched) > maxAge
}

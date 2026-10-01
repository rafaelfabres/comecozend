// Package catalog turns "what is on this device" plus "what patches
// exist" into the list the user actually sees.
//
// The join runs in two stages, and the split matters: stage one is free
// and offline, stage two costs a download.
//
//	Stage 1 (Build): every owned ROM is identified against RetroAchievements'
//	  console game list — one API call per system present — which gives the
//	  base game's real title. Titles are matched against RAPatches folder
//	  names, so the list shows only hacks of games this device holds.
//
//	Stage 2 (Resolve, see install.go): when the user opens a hack, the patch
//	  is downloaded and its own checksum decides which local file, if any, is
//	  the exact dump it was built against.
//
// Stage one can be optimistic; stage two cannot be wrong.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"leaf-hacks/internal/library"
	"leaf-hacks/internal/rahub"
	"leaf-hacks/internal/rapatches"
)

// Hack is one row in the list.
type Hack struct {
	GameID    int             `json:"game_id"`
	Title     string          `json:"title"`      // the set's real title on RA
	BaseTitle string          `json:"base_title"` // the game it patches
	ConsoleID int             `json:"console_id"`
	Entry     rapatches.Entry `json:"entry"`
	BasePaths []string        `json:"base_paths"` // owned ROMs of the base game
	// Icon is the set's badge on RetroAchievements, or "" when it has
	// none. It arrives with the console game list, so every hack has a
	// picture at no extra request.
	Icon string `json:"icon,omitempty"`

	// AlsoHackOf names the other base games the same set is filed under.
	// One set can patch either of two games — Pokémon Red and Blue share
	// a hack — and the repository files it under both.
	AlsoHackOf []string `json:"also_hack_of,omitempty"`

	// NeedsOtherDump means the patch's own checksum says it was built
	// against a dump that is not on this card — a different region or
	// revision of a game the user does own.
	NeedsOtherDump bool   `json:"needs_other_dump,omitempty"`
	WantedDump     string `json:"wanted_dump,omitempty"`

	// Achievements is why the hack is in this list at all: a hack with no
	// achievement set is a hack this app has no reason to offer.
	Achievements int       `json:"achievements"`
	Leaderboards int       `json:"leaderboards,omitempty"`
	Points       int       `json:"points,omitempty"`
	Modified     time.Time `json:"modified,omitempty"`
}

// IsNew reports whether the set was touched recently enough to be worth
// marking in the list.
func (h Hack) IsNew() bool {
	return !h.Modified.IsZero() && time.Since(h.Modified) < 60*24*time.Hour
}

// Console resolves the console record.
func (h Hack) Console() (rahub.Console, bool) { return rahub.ConsoleByID(h.ConsoleID) }

// Catalog is the built list plus what it was built from.
type Catalog struct {
	Built time.Time `json:"built"`
	Hacks []Hack    `json:"hacks"`
	// BaseGames is base-game title → owned ROM paths, kept so the UI can
	// explain why a game appears at all.
	BaseGames map[string][]string `json:"base_games"`
	// WithoutAchievements counts the patches that matched an owned game
	// but have no achievement set, and so were left out.
	WithoutAchievements int `json:"without_achievements,omitempty"`
	// CompatibilityPatches counts the ones left out because they belong
	// to the base game's own set rather than to a hack of it.
	CompatibilityPatches int `json:"compatibility_patches,omitempty"`
}

// OwnedBase is a base game the device holds, as identified against RA.
type OwnedBase struct {
	GameID    int
	Title     string
	ConsoleID int
	ROMs      []library.ROM
}

// SetInfo is what RetroAchievements knows about an achievement set, keyed
// by game ID. The console game list carries it for every set on a system —
// hacks included, since a hack is just another game there — so titles and
// icons for the whole catalog cost nothing beyond the calls already made.
type SetInfo struct {
	Title        string
	Icon         string
	Achievements int
	Leaderboards int
	Points       int
	Modified     time.Time
}

// IdentifyBases works out which RetroAchievements games the device's ROMs
// are. One API call per system, not per ROM.
func IdentifyBases(ctx context.Context, client *rahub.RAClient, lib *library.Library, note func(string)) ([]OwnedBase, map[int]SetInfo, error) {
	byConsole := map[int][]library.ROM{}
	for _, r := range lib.ROMs {
		if r.RAHash == "" && !r.Disc {
			continue
		}
		byConsole[r.ConsoleID] = append(byConsole[r.ConsoleID], r)
	}

	var out []OwnedBase
	var failures []string
	var firstErr error
	sets := map[int]SetInfo{}
	for consoleID, roms := range byConsole {
		console, _ := rahub.ConsoleByID(consoleID)
		if note != nil {
			note("Identifying " + console.Short + " ROMs")
		}
		games, err := client.FetchConsoleGames(ctx, consoleID)
		if err != nil {
			failures = append(failures, console.Short)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, g := range games {
			sets[g.ID] = SetInfo{
				Title: g.Title, Icon: g.Icon,
				Achievements: g.Achievements, Leaderboards: g.Leaderboards,
				Points: g.Points, Modified: g.Modified,
			}
		}

		// hash → game, so each ROM is a map lookup rather than a scan.
		byHash := make(map[string]rahub.ConsoleGame, len(games)*2)
		for _, g := range games {
			for _, h := range g.Hashes {
				byHash[strings.ToLower(h)] = g
			}
		}
		// Disc images are matched by title, since they are not hashed.
		// The same three-pass matcher the patch folders use, so a dump
		// named slightly differently from RetroAchievements' title still
		// finds its game.
		byTitle := newTitleMatcher[rahub.ConsoleGame]()
		for _, g := range games {
			byTitle.add(g.Title, g)
		}

		grouped := map[int]*OwnedBase{}
		for _, r := range roms {
			g, ok := byHash[r.RAHash]
			if !ok && r.Disc {
				g, ok = byTitle.find(NormalizeDumpName(r.Name()))
			}
			if !ok {
				continue // a dump RA does not recognise: no hacks to offer
			}
			b := grouped[g.ID]
			if b == nil {
				b = &OwnedBase{GameID: g.ID, Title: g.Title, ConsoleID: consoleID}
				grouped[g.ID] = b
			}
			b.ROMs = append(b.ROMs, r)
		}
		for _, b := range grouped {
			out = append(out, *b)
		}
	}
	if len(out) == 0 && len(failures) > 0 {
		// Carry the real reason up. "Could not reach" alone sent someone
		// hunting for a network problem when the actual answer was a
		// rejected API key.
		return nil, nil, fmt.Errorf("RetroAchievements lookup failed for %s: %w", strings.Join(failures, ", "), firstErr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, sets, nil
}

// Build joins the identified base games with the patch index.
// Build joins the identified base games with the patch index.
//
// Two sources decide whether a patch belongs to a game the user owns. The
// fingerprint cache is authoritative: it holds the base-ROM checksum the
// patch itself declares, compared against the checksums of the files on
// the card. Names are the fallback, used only where no fingerprint exists
// yet — a patch is never dropped merely because it has not been checked.
func Build(bases []OwnedBase, ix rapatches.Index, sets map[int]SetInfo, facts *FactsCache, lib *library.Library, categories ...rapatches.Category) Catalog {
	if len(categories) == 0 {
		categories = []rapatches.Category{rapatches.Hacks}
	}
	wanted := map[rapatches.Category]bool{}
	for _, c := range categories {
		wanted[c] = true
	}

	// One matcher per console, so a title can never bind to a game on a
	// different system.
	owned := map[int]*titleMatcher[*OwnedBase]{}
	byROM := map[string]*OwnedBase{}
	for i := range bases {
		m := owned[bases[i].ConsoleID]
		if m == nil {
			m = newTitleMatcher[*OwnedBase]()
			owned[bases[i].ConsoleID] = m
		}
		m.add(bases[i].Title, &bases[i])
		for _, r := range bases[i].ROMs {
			byROM[r.Path] = &bases[i]
		}
	}

	cat := Catalog{Built: time.Now(), BaseGames: map[string][]string{}}
	skipped := 0
	compatibility := 0
	for _, e := range ix.Entries {
		if !wanted[e.Category] {
			continue
		}
		console, ok := rahub.ConsoleForPatchDir(e.Console)
		if !ok {
			continue
		}
		matcher := owned[console.ID]
		if matcher == nil {
			continue
		}
		named, namedOK := matcher.find(e.BaseGame)

		// The fingerprint only ever adds. It rescues hacks the names
		// missed; it never removes one the names found.
		//
		// The first attempt let a checksum mismatch drop a hack, and that
		// was wrong: "this patch needs the Europe disc and you have the
		// USA one" is a hack of a game you own, and worth seeing with
		// that explanation. Hiding it is how a list silently loses 99
		// entries and the user has no way to find out why.
		var base *OwnedBase
		needsOtherDump := false
		wantedDump := ""

		if f, checked := facts.Get(e.Path); checked && f.Known() {
			if rom, hit := f.Matches(lib); hit {
				base = byROM[rom.Path]
			} else if !namedIsDiscOrUnknown(named, namedOK) {
				// The patch says which dump it needs, and it is not one
				// on this card. Still listed, but flagged.
				needsOtherDump = true
				wantedDump = f.BaseFile
			}
		}
		if base == nil && namedOK {
			base = named
		}
		if base == nil {
			continue
		}
		paths := make([]string, 0, len(base.ROMs))
		for _, r := range base.ROMs {
			paths = append(paths, r.Path)
		}
		// A patch filed under the base game's own set ID is not a hack.
		// RAPatches keeps compatibility patches there — four files under
		// NES/Hacks/Ninja Gaiden all carry ID 1859, the base game's set,
		// because they turn an unsupported dump into the supported one.
		// Listed as hacks they came out as four identical "Ninja Gaiden,
		// hack of Ninja Gaiden" rows wearing the base game's own title
		// and achievement count.
		if e.GameID == base.GameID {
			compatibility++
			continue
		}

		// Only sets with achievements. A patch whose RetroAchievements
		// entry has no achievements is just a ROM hack — the whole point
		// of this app is the ones you can earn something in, and RA hosts
		// patches for plenty of sets that have none yet.
		//
		// When sets is nil the caller has no achievement data at all
		// (offline, or a test), and filtering everything out would be
		// worse than listing it.
		info, known := sets[e.GameID]
		if sets != nil {
			if !known || info.Achievements == 0 {
				skipped++
				continue
			}
		}
		title := DisplayTitle(e.File)
		if info.Title != "" {
			title = CleanSetTitle(info.Title)
		}
		cat.Hacks = append(cat.Hacks, Hack{
			NeedsOtherDump: needsOtherDump,
			WantedDump:     wantedDump,
			GameID:         e.GameID,
			Title:          title,
			BaseTitle:      base.Title,
			ConsoleID:      console.ID,
			Entry:          e,
			BasePaths:      paths,
			Icon:           info.Icon,
			Achievements:   info.Achievements,
			Leaderboards:   info.Leaderboards,
			Points:         info.Points,
			Modified:       info.Modified,
		})
		cat.BaseGames[base.Title] = paths
	}
	cat.Hacks = dedupeBySet(cat.Hacks)
	cat.WithoutAchievements = skipped
	cat.CompatibilityPatches = compatibility
	sort.Slice(cat.Hacks, func(i, j int) bool {
		if cat.Hacks[i].BaseTitle != cat.Hacks[j].BaseTitle {
			return cat.Hacks[i].BaseTitle < cat.Hacks[j].BaseTitle
		}
		return cat.Hacks[i].Title < cat.Hacks[j].Title
	})
	return cat
}

// CleanSetTitle drops the markers RetroAchievements puts in front of a
// set's name. Every row in this app is a hack, so a "~Hack~" prefix on all
// of them is just width taken from the title on a small screen.
func CleanSetTitle(title string) string {
	for _, prefix := range []string{"~Hack~ ", "~Homebrew~ ", "~Unlicensed~ ", "~Prototype~ ", "~Demo~ "} {
		title = strings.TrimPrefix(title, prefix)
	}
	return strings.TrimSpace(title)
}

// namedIsDiscOrUnknown reports whether a checksum mismatch proves nothing.
// Disc images are never fingerprinted — a .chd cannot be read without
// chdman — so a patch for a disc game can never match the library, and
// flagging it would be noise on every PlayStation hack.
func namedIsDiscOrUnknown(b *OwnedBase, ok bool) bool {
	if !ok || b == nil {
		return true
	}
	for _, r := range b.ROMs {
		if !r.Disc {
			return false
		}
	}
	return len(b.ROMs) > 0
}

// dedupeBySet collapses a set that the repository files under more than
// one base game.
//
// "Pokémon Regulation Red | Regulation Blue" is one achievement set with
// one game ID, and the patch repository keeps it under both "Pokemon Red"
// and "Pokemon Blue". Someone who owns both cartridges got the same hack
// listed twice, once as a hack of each — two rows that install the same
// thing.
//
// The copy kept is the one whose base ROM is actually usable, since that
// is the one that will install; the other base games are folded into the
// title line so nothing is hidden.
func dedupeBySet(hacks []Hack) []Hack {
	byID := map[int]int{} // game ID -> index in out
	out := make([]Hack, 0, len(hacks))
	for _, h := range hacks {
		at, seen := byID[h.GameID]
		if !seen {
			byID[h.GameID] = len(out)
			out = append(out, h)
			continue
		}
		kept := out[at]
		// Prefer the entry that can actually be installed.
		if kept.NeedsOtherDump && !h.NeedsOtherDump {
			h.AlsoHackOf = appendBase(h.AlsoHackOf, kept.BaseTitle, kept.AlsoHackOf)
			h.BasePaths = append(append([]string(nil), h.BasePaths...), kept.BasePaths...)
			out[at] = h
			continue
		}
		out[at].AlsoHackOf = appendBase(kept.AlsoHackOf, h.BaseTitle, nil)
		out[at].BasePaths = append(out[at].BasePaths, h.BasePaths...)
	}
	return out
}

func appendBase(existing []string, title string, extra []string) []string {
	add := append([]string{title}, extra...)
	for _, t := range add {
		if t == "" {
			continue
		}
		found := false
		for _, e := range existing {
			if e == t {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, t)
		}
	}
	return existing
}

func stripExtension(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

// DisplayTitle turns "36943-FE8-HagInWhite.7z" into something readable.
// The repository's short names are compact rather than pretty, so this is
// a best effort that the real title from the API replaces once known.
func DisplayTitle(file string) string {
	name := file
	if i := strings.IndexByte(name, '-'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	name = strings.ReplaceAll(name, "-", " ")

	// Split runs like "HagInWhite" into words without breaking acronyms
	// such as "FE8" or "SMW".
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z' && nextLower) {
				b.WriteRune(' ')
			}
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Save and Load keep the built catalog between launches.
func (c Catalog) Save(path string) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadCatalog(path string) (Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}
	var c Catalog
	err = json.Unmarshal(b, &c)
	return c, err
}

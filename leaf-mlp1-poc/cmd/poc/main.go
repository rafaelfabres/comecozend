// Command poc is a proof of concept showing that the real, unmodified
// internal/appui models and internal/itchio client from Leaf-Itchio-Pak can
// drive a plain SDL2 front end instead of Catastrophe, end to end: browsing
// itch.io's curated homebrew/ROM-hack catalog (no API key needed, same as
// the original pak), opening a game's detail screen, downloading it into
// the right /roms/<system> folder for dArkOS's EmulationStation, and
// navigating back.
//
// What's real: internal/appui (all screen models), internal/itchio
// (network client — including the AllPlatforms homebrew tag-feed catalog),
// internal/roms (destination-path resolution and filename rules),
// internal/inventory, internal/media, internal/settings, internal/power,
// internal/logger — copied verbatim from Leaf-Itchio-Pak. What's new:
// internal/sdlui (this PoC's replacement for internal/catui + cat_bridge.c).
//
// What's still a stub: internal/leaf (the "Jawaka" daemon integration) isn't
// wired in, since dArkOS has no equivalent daemon to talk to. Only free
// uploads are downloaded — paid-game API-key flow isn't wired (out of scope
// for a homebrew/ROM-hack catalog, which is nearly all free/pay-what-you-want).
package main

import (
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"leaf-mlp1-poc/internal/appui"
	"leaf-mlp1-poc/internal/inventory"
	"leaf-mlp1-poc/internal/itchio"
	"leaf-mlp1-poc/internal/rahub"
	"leaf-mlp1-poc/internal/roms"
	"leaf-mlp1-poc/internal/sdlui"
)

func fontPath() string {
	if p := os.Getenv("POC_FONT_PATH"); p != "" {
		return p
	}
	return "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
}

func demoItems() []appui.ListItem {
	return []appui.ListItem{
		{Title: "Celeste Classic", Author: "Maddy Makes Games", Badge: "Free"},
		{Title: "Anodyne", Author: "Analgesic Productions", Badge: "$9.99"},
		{Title: "Downwell", Author: "Moppin", Badge: "$3.99"},
		{Title: "A Short Hike", Author: "adamgryu", Badge: "$7.99"},
		{Title: "Minit", Author: "JW / Kitty / Jukio / Dom", Badge: "$9.99"},
	}
}

// activeHub is the RetroAchievements hub catalog (see hub.go). Set once in
// main before any list is built; nil only in unit tests.
var activeHub *hubContext

// romsRoot is where dArkOS/EmulationStation-fcamod keeps ROMs on this device.
const romsRoot = "/roms"

// systemFolderByCode maps the platform codes used by the vendored Leaf code
// to the real folder names on this dArkOS install. They differ: Leaf calls
// the NES "FC" (Famicom) and the Genesis "MD" (Mega Drive), while dArkOS's
// folders are "nes" and "genesis".
var systemFolderByCode = map[string]string{
	"GB":    "gb",
	"GBC":   "gbc",
	"GBA":   "gba",
	"FC":    "nes",
	"MD":    "genesis",
	"PICO8": "pico-8",
	"PS":    "psx",
}

// configureDarkOSPaths tells internal/roms where dArkOS/EmulationStation-fcamod
// keeps ROMs, per confirmed layout on Rafael's Miniloong: root /roms, one
// folder per system (real folder names below — dArkOS doesn't use Leaf's own
// "FC"/"MD"/"PS" internal codes, so we map Leaf's required keys onto the
// real folder names), artwork in <system>/images, bg music in /roms/bgmusic,
// save states in /roms/states.
func configureDarkOSPaths() error {
	const root = romsRoot
	systemFolders := systemFolderByCode
	systemDirs := make(map[string]string, len(systemFolders))
	imageDirs := make(map[string]string, len(systemFolders))
	for code, folder := range systemFolders {
		dir := filepath.Join(root, folder)
		// Prefer the directory EmulationStation itself scans. Its <path> is
		// not always /roms/<folder>: PICO-8's is /roms/pico-8/carts/, and a
		// cart written one level above it is invisible to the menu.
		if configured, ok := romPathForSystem(folder); ok {
			dir = configured
			fmt.Fprintf(os.Stderr, "system %s: using EmulationStation path %s\n", folder, dir)
		}
		systemDirs[code] = dir
		imageDirs[code] = filepath.Join(dir, "images")
	}
	return roms.ConfigurePaths(roms.PathConfig{
		SourceID:    "darkos-miniloong",
		PrimaryRoot: root,
		MusicRoot:   filepath.Join(root, "bgmusic"),
		StatesRoot:  filepath.Join(root, "states"),
		SystemDirs:  systemDirs,
		ImageDirs:   imageDirs,
	})
}

// screenMode tracks which appui model currently owns the screen. A full port
// would use a small stack (push detail, pop back to list); this PoC only
// ever has two screens, so a flag is enough.
type screenMode int

const (
	modeList screenMode = iota
	modeDetail
	modeFilter
	modeKeyboard
	modeManage
	modeQuitConfirm
	modeSettings
	modeOwned
	modeHidden  // Settings > Hidden games
	modeSystems // Settings > Hidden systems
)

// cacheTTL is how long a stored catalog is trusted without re-fetching. Leaf
// uses 24h; 48h here, plus an explicit refresh (START on the list) for when
// the user wants new games now rather than waiting for the window to lapse.
const cacheTTL = 48 * time.Hour

// dataDir is where the catalog cache and the download record live.
//
// NOT next to the binary: upgrading means "rm -rf leaf-mlp1-poc && tar xzf",
// which wiped both — losing the record of every installed game and forcing a
// full multi-minute catalog re-fetch on every update. A directory outside the
// app folder survives that.
func dataDir() string {
	if custom := os.Getenv("POC_DATA_DIR"); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/home/ark"
	}
	dir := filepath.Join(home, ".local", "share", "leaf-itchio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "could not create data directory, using the app folder:", err)
		if exe, exeErr := os.Executable(); exeErr == nil {
			return filepath.Dir(exe)
		}
		return "."
	}
	return dir
}

func inventoryPath() string { return filepath.Join(dataDir(), "itchio-inventory.json") }
func cachePath() string     { return filepath.Join(dataDir(), "itchio-games-cache.json") }

// migrateLegacyData moves the cache and inventory out of the old
// beside-the-binary location, so an existing install keeps its data the first
// time it runs a build that stores them properly.
func migrateLegacyData() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	oldDir := filepath.Dir(exe)
	target := dataDir()
	if oldDir == target {
		return
	}
	for _, name := range []string{"itchio-inventory.json", "itchio-games-cache.json"} {
		newPath := filepath.Join(target, name)
		if _, err := os.Stat(newPath); err == nil {
			continue // already there
		}
		oldPath := filepath.Join(oldDir, name)
		if _, err := os.Stat(oldPath); err != nil {
			continue // nothing to move
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			fmt.Fprintf(os.Stderr, "could not migrate %s: %v\n", name, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "migrated %s to %s\n", name, newPath)
	}
}

// gamesToItems converts itchio.Game (network/cache shape) into the
// appui.ListItem shape the main list screen actually renders.
// describeCacheAge is the short "where this list came from" note shown in the
// header, so it is obvious the catalog is stored rather than re-fetched.
// settingsRows builds the settings list. It mirrors the rows Leaf offers that
// are meaningful here: the API key (masked, editable, removable) and the data
// location. Leaf's music/multi-destination rows have no equivalent in this
// port, and its content filters are a separate screen.
func settingsRows(cfg *config, acct account, noROM *noROMList, hub *hubContext) []sdlui.SettingsRow {
	creds := raCredentials(cfg)
	keyState := "not set"
	if creds.Key != "" {
		keyState = maskedKey(creds.Key)
	}
	rows := []sdlui.SettingsRow{
		{Section: "RETROACHIEVEMENTS", Label: "Hub", Value: fmt.Sprintf("#%d  ·  %d games", hub.hubID, hub.visibleCount())},
		{Label: "RA user", Value: creds.User},
		{Label: "Set RetroAchievements key", Value: keyState, Action: true},
		{Label: "Refresh RetroAchievements hub", Value: "new games", Action: true},
	}
	// No "install everything" here on purpose: games are installed one at
	// a time from their page (A), only the ones you pick.
	done, total := hub.itchProgress()
	rows = append(rows,
		sdlui.SettingsRow{Label: "Installed (verified)", Value: fmt.Sprintf("%d", hub.store.Counts()[rahub.StatusVerified])},
		sdlui.SettingsRow{Label: "itch.io data cached", Value: fmt.Sprintf("%d / %d", done, total)},
		sdlui.SettingsRow{Label: "Hidden systems", Value: fmt.Sprintf("%d", len(hub.store.HiddenConsoles)), Action: true},
		sdlui.SettingsRow{Label: "Hidden games", Value: fmt.Sprintf("%d", len(hub.hiddenGames())), Action: true},
		sdlui.SettingsRow{Label: "Re-scan games not found", Action: true},
		sdlui.SettingsRow{Label: "Re-scan itch.io (all games)", Action: true},
	)
	rows = append(rows,
		sdlui.SettingsRow{Section: "ACCOUNT", Label: "itch.io account", Value: acct.label()},
		sdlui.SettingsRow{Label: "API key", Value: maskedKey(cfg.key())},
	)
	if cfg.key() == "" {
		rows = append(rows, sdlui.SettingsRow{Label: "Sign in with an API key", Action: true})
	} else {
		rows = append(rows,
			sdlui.SettingsRow{Label: "Replace API key", Action: true},
			sdlui.SettingsRow{Label: "Sign out", Action: true},
			sdlui.SettingsRow{Label: "Your purchases", Value: fmt.Sprintf("%d owned", len(acct.owned)), Action: true},
		)
	}

	// The itch.io tag-feed catalog rows ("Refresh", "Rebuild", "Forget games
	// with no ROM") are gone: the list is the RetroAchievements hub now.
	rows = append(rows,
		sdlui.SettingsRow{Section: "STORAGE", Label: "App data", Value: dataDir()},
	)
	return rows
}

func describeCacheAge(age time.Duration) string {
	switch {
	case age < time.Hour:
		return "saved list, updated just now"
	case age < 24*time.Hour:
		return fmt.Sprintf("saved list, %dh old  (START to refresh)", int(age.Hours()))
	default:
		return fmt.Sprintf("saved list, %dd old  (START to refresh)", int(age.Hours())/24)
	}
}

func gamesToItems(games []itchio.Game) []appui.ListItem {
	items := make([]appui.ListItem, len(games))
	for i, g := range games {
		badge := "Free"
		if !g.IsFree {
			badge = fmt.Sprintf("$%.2f", g.Price)
		}
		second := g.Blurb
		if second == "" {
			second = g.Author
		}
		if g.Platform != "" {
			badge = systemFolderByCode[g.Platform] + "  " + badge
		}
		// Hub mode: the badge is the console and the verification status,
		// since price means nothing for a hash-verified install.
		if activeHub != nil {
			if id, ok := gameIDFromURL(g.URL); ok {
				hg, _ := activeHub.store.Game(id)
				badge = g.Platform
				if rahub.IsDemo(hg.Title) {
					badge += "  DEMO"
				}
				st := activeHub.store.State(id)
				if isNewSet(hg) {
					badge += "  NEW"
				}
				if label := statusLabel(st, hg.Console()); label != "" {
					badge += "  " + label
				}
				switch {
				case st.Progress.Mastered() || st.Progress.Completed():
					badge = "\x02" + badge // gold: you finished every achievement
				case demoGone(st):
					badge = "\x01" + badge // red: cannot be downloaded
				}
			}
		}
		items[i] = appui.ListItem{Title: g.Title, Author: second, Badge: badge, Tags: g.Tags, CoverKey: g.CoverURL}
	}
	return items
}

// fetchFullCatalog walks every page of every feed in itchio.AllPlatforms —
// the same curated homebrew/ROM-hack tag list the real Leaf pak ships.
//
// This deliberately does NOT use itchio.Client.FetchAllGames, despite that
// being Leaf's own entry point, because of how it handles rate limiting:
// its fetchSlug returns on the first page that exhausts its retries, which
// ABANDONS every remaining page of that tag. Observed live: made-with-gb-
// studio died at page 35, tag-gba at page 2, tag-nes-rom at page 6 — each
// silently truncating that console's catalog to whatever came before the
// first HTTP 429. FetchAllGames also runs 3 slugs concurrently, which makes
// hitting 429 more likely in the first place.
//
// So: one request at a time, a short delay between them (fewer 429s at the
// source), and a failed page skips ahead instead of ending the tag, giving
// up on a tag only after several consecutive failures. No outer deadline —
// an artificial timeout is what truncated an earlier attempt mid-walk.
// adultFeedsFor returns extra feed slugs that surface adult-tagged games for
// a console.
//
// itch.io keeps adult content out of anonymous browsing entirely: the plain
// tag feeds this app walks never contain it, no matter what the app does. The
// only way to reach those games without an account session is to ask for an
// adult tag explicitly, combined with the console tag — the same
// "tag-a/tag-b" form the catalog already uses for tag-homebrew/tag-psx.
//
// Coverage therefore depends on how each developer tagged their game, so this
// finds the games that are tagged, not necessarily every adult game.
// extraFeedsFor adds the other tag spellings itch.io games actually use for
// each console.
//
// These are split by what the tag actually means, which turns out to matter a
// lot:
//
//   - FORMAT tags ("Game Boy ROM", "NES ROM", "made with GB Studio") are
//     claims about the file you get: a real cartridge ROM. Games under them
//     are almost always installable here.
//   - THEME tags ("Game Boy Advance", "gameboy") describe inspiration or
//     subject. itch.io's "Game Boy Advance" tag has ~768 projects, and many
//     are Windows/Linux games made in a GBA style — no ROM anywhere. These
//     are what put PC games in the list.
//
// strictROM selects only the format group, which is the default: the point of
// this app is ROMs. Turning it off in Settings widens the net at the cost of
// PC games appearing.
func extraFeedsFor(platform itchio.FeedPlatform, strictROM bool) []string {
	var format, theme []string
	switch platform.Code {
	case "GB":
		format = []string{"tag-gameboy-rom", "tag-gbstudio"}
		theme = []string{"tag-gameboy", "tag-game-boy", "tag-gb"}
	case "GBC":
		format = []string{"tag-gameboy-rom/tag-gameboy-color"}
		theme = []string{"tag-game-boy-color", "tag-gameboy-colour"}
	case "GBA":
		// The broad GBA tags are theme-heavy, so pin them to homebrew/ROM.
		format = []string{"tag-gba-rom", "tag-homebrew/tag-gba", "tag-homebrew/tag-gameboy-advance"}
		theme = []string{"tag-gameboy-advance", "tag-game-boy-advance", "tag-agb"}
	case "FC", "NES":
		format = []string{"tag-nes-rom", "tag-nesmaker", "tag-homebrew/tag-nes"}
		theme = []string{"tag-nes", "tag-famicom"}
	case "MD":
		format = []string{"tag-genesis-rom", "tag-homebrew/tag-sega-mega-drive"}
		theme = []string{"tag-megadrive", "tag-mega-drive", "tag-sega-genesis", "tag-genesis"}
	case "P8", "PICO8":
		// Pico-8 carts are the distributable format, so the tag is unambiguous.
		format = []string{"tag-pico8"}
	case "PSX", "PS":
		format = []string{"tag-homebrew/tag-playstation-1", "tag-psx-homebrew"}
		theme = []string{"tag-playstation-1", "tag-ps1"}
	}
	if strictROM {
		return format
	}
	return append(format, theme...)
}

// isROMSlug reports whether a feed slug names a FORMAT tag — one that claims
// the download is a real cartridge/cart file — rather than a THEME tag.
//
// The rule is deliberately about the tag vocabulary itch.io developers use:
// "...-rom" is an explicit format claim, "homebrew" means built for the real
// hardware, and GB Studio / NESMaker / Pico-8 are toolchains whose output IS
// the ROM or cart. Everything else ("gba", "gameboy", "genesis") describes
// what a game is like, not what you get.
func isROMSlug(slug string) bool {
	for _, marker := range []string{"-rom", "homebrew", "gbstudio", "gb-studio", "nesmaker", "pico-8", "pico8"} {
		if strings.Contains(slug, marker) {
			return true
		}
	}
	return false
}

func keepROMSlugs(slugs []string) []string {
	kept := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		if isROMSlug(slug) {
			kept = append(kept, slug)
		}
	}
	return kept
}

func dedupeSlugs(slugs []string) []string {
	seen := make(map[string]bool, len(slugs))
	out := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		if seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out
}

func adultFeedsFor(platform itchio.FeedPlatform) []string {
	consoleTag := ""
	switch platform.Code {
	case "GB":
		consoleTag = "tag-gameboy"
	case "GBC":
		consoleTag = "tag-gameboy-color"
	case "GBA":
		consoleTag = "tag-gba"
	case "FC", "NES":
		consoleTag = "tag-nes"
	case "MD":
		consoleTag = "tag-sega-mega-drive"
	case "P8", "PICO8":
		consoleTag = "tag-pico-8"
	case "PSX", "PS":
		consoleTag = "tag-psx"
	default:
		return nil
	}
	var feeds []string
	for _, adult := range []string{"tag-adult", "tag-nsfw", "tag-eroge", "tag-erotic"} {
		feeds = append(feeds, adult+"/"+consoleTag)
	}
	return feeds
}

// fetchFullCatalog walks every page of every feed in itchio.AllPlatforms —
// the same curated homebrew/ROM-hack tag list the real Leaf pak ships, plus
// the extra tag spellings in extraFeedsFor.
//
// known is the set of game URLs already in the local cache. When it is
// non-empty the walk runs INCREMENTALLY: itch.io feeds are ordered
// newest-first, so once a page contains nothing the cache is missing, every
// remaining page of that feed is older still and can be skipped. A refresh
// then costs a page or two per feed instead of hundreds, which is the
// difference between seconds and many minutes. Pass nil for a full walk
// (first run, when there is nothing to be incremental against).
func fetchFullCatalog(ctx context.Context, client *itchio.Client, onProgress func([]itchio.Game),
	known map[string]bool) ([]itchio.Game, error) {
	const (
		requestDelay    = 250 * time.Millisecond // pacing between page requests
		maxPageFailures = 4                      // consecutive failures before abandoning a tag
		maxPages        = 400                    // hard stop; the largest real tag is ~200 pages
		progressEvery   = 5                      // publish partial results every N pages
	)

	seen := make(map[string]bool)
	var all []itchio.Game
	var lastErr error
	pagesSinceProgress := 0

	publish := func() {
		if onProgress == nil || len(all) == 0 {
			return
		}
		// Hand over a copy: the caller renders from this on another
		// goroutine's timeline, and `all` keeps growing here.
		onProgress(append([]itchio.Game(nil), all...))
	}

	for _, platform := range itchio.AllPlatforms {
		// Always fetch the widest set — every tag spelling plus the adult
		// feeds — and filter at display time instead. Settings that only
		// took effect after a full rebuild were a trap: the toggle appeared
		// to do nothing until the user happened to rebuild.
		slugs := append([]string(nil), platform.FeedSlugs...)
		slugs = append(slugs, extraFeedsFor(platform, false)...)
		adultSlugs := map[string]bool{}
		for _, slug := range adultFeedsFor(platform) {
			adultSlugs[slug] = true
			slugs = append(slugs, slug)
		}
		slugs = dedupeSlugs(slugs)
		for _, slug := range slugs {
			localSeen := make(map[string]bool)
			consecutiveFailures := 0

			for page := 1; page <= maxPages; page++ {
				select {
				case <-ctx.Done():
					return all, ctx.Err()
				default:
				}

				url := fmt.Sprintf("https://itch.io/games/%s.xml?page=%d", slug, page)
				pageGames, err := client.FetchGamesFromURLContext(ctx, url)
				if err != nil {
					// A 404 means we walked past the last page: that tag is
					// simply finished. It is the normal way a feed ends, so
					// it must not be recorded as a fetch error — doing so
					// made every single run end with a scary "catalog fetch
					// finished with some errors" line.
					if strings.Contains(err.Error(), "404") {
						break
					}
					lastErr = err
					// Cloudflare blocking is not page-specific; retrying
					// further pages of this tag will just keep failing.
					if errors.Is(err, itchio.ErrCloudflareBlocked) {
						fmt.Fprintf(os.Stderr, "feed %s: blocked by Cloudflare, skipping tag\n", slug)
						break
					}
					consecutiveFailures++
					fmt.Fprintf(os.Stderr, "feed %s page %d failed (%d/%d): %v\n",
						slug, page, consecutiveFailures, maxPageFailures, err)
					if consecutiveFailures >= maxPageFailures {
						fmt.Fprintf(os.Stderr, "feed %s: giving up after %d consecutive failures at page %d\n",
							slug, consecutiveFailures, page)
						break
					}
					// Back off a little harder than the normal pacing, then
					// move on to the NEXT page rather than abandoning the tag.
					if err := sleepCtx(ctx, requestDelay*4); err != nil {
						return all, err
					}
					continue
				}
				consecutiveFailures = 0

				added := 0
				newToCache := 0
				for i := range pageGames {
					if !known[pageGames[i].URL] {
						newToCache++
					}
					if localSeen[pageGames[i].URL] {
						continue
					}
					localSeen[pageGames[i].URL] = true
					if seen[pageGames[i].URL] {
						// Already collected from another tag. If THIS feed
						// is an adult one, the game still needs marking —
						// otherwise a game that appears in both a normal and
						// an adult feed never gets flagged, and the 18+
						// filter finds nothing.
						if adultSlugs[slug] {
							for j := range all {
								if all[j].URL == pageGames[i].URL {
									all[j].Adult = true
									break
								}
							}
						}
						continue
					}
					seen[pageGames[i].URL] = true
					pageGames[i].Platform = platform.Code
					pageGames[i].Adult = adultSlugs[slug]
					all = append(all, pageGames[i])
					added++
				}

				// Incremental stop: this whole page is already cached, and
				// the feed is newest-first, so nothing deeper is newer.
				if len(known) > 0 && newToCache == 0 {
					break
				}

				// Stop only on a genuinely exhausted feed.
				//
				// This used to break whenever a page returned fewer than
				// PerPage (36) items — which looked reasonable but was
				// wrong: the feed parser drops some items (no price, no
				// usable title), so a full page routinely yields 33 or 34
				// games. The walk therefore stopped after page ONE, which
				// is why tag-gba showed 33 games when itch.io has 600+.
				//
				// A page that parses to nothing, or that is entirely
				// duplicates, means the end (itch.io wraps around past the
				// last page); a 404 is handled above. maxPages is the
				// backstop.
				if len(pageGames) == 0 {
					break
				}
				if added == 0 {
					break
				}

				pagesSinceProgress++
				if pagesSinceProgress >= progressEvery {
					pagesSinceProgress = 0
					publish()
				}

				if err := sleepCtx(ctx, requestDelay); err != nil {
					return all, err
				}
			}
		}
	}
	// Popularity pass: itch.io serves each feed in popularity order on
	// request, and the RSS items carry no rating field, so this is the only
	// way to know which games the site considers best. Only the first few
	// pages are read — beyond the top of each feed the ranking stops being
	// meaningful anyway — which keeps this to seconds rather than another
	// full walk.
	applyPopularityRanks(ctx, client, all)
	return all, lastErr
}

// popularityPages is how deep the ranking pass reads per feed.
const popularityPages = 3

func applyPopularityRanks(ctx context.Context, client *itchio.Client, games []itchio.Game) {
	if len(games) == 0 {
		return
	}
	byURL := make(map[string]*itchio.Game, len(games))
	for i := range games {
		byURL[games[i].URL] = &games[i]
	}

	rank := 0
	for _, platform := range itchio.AllPlatforms {
		slugs := append([]string(nil), platform.FeedSlugs...)
		slugs = append(slugs, extraFeedsFor(platform, false)...)
		slugs = append(slugs, adultFeedsFor(platform)...)
		for _, slug := range dedupeSlugs(slugs) {
			for page := 1; page <= popularityPages; page++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				// itch.io expresses browse ordering as a PATH segment
				// ("/games/top-rated/tag-gba"), the same way it composes
				// tags — a ?sort= query parameter is ignored, which is why
				// the popular order came back identical to the default.
				url := fmt.Sprintf("https://itch.io/games/top-rated/%s.xml?page=%d", slug, page)
				pageGames, err := client.FetchGamesFromURLContext(ctx, url)
				if err != nil || len(pageGames) == 0 {
					break
				}
				for _, g := range pageGames {
					rank++
					// Keep the best (lowest) rank a game earns across feeds.
					if target, ok := byURL[g.URL]; ok && (target.Rank == 0 || rank < target.Rank) {
						target.Rank = rank
					}
				}
				if err := sleepCtx(ctx, 250*time.Millisecond); err != nil {
					return
				}
			}
		}
	}
}

// knownURLs is the set of game URLs already cached, used to make a refresh
// incremental. Returns nil for an empty catalog so the walk stays full.
func knownURLs(games []itchio.Game) map[string]bool {
	if len(games) == 0 {
		return nil
	}
	set := make(map[string]bool, len(games))
	for _, g := range games {
		set[g.URL] = true
	}
	return set
}

// mergeCatalogs puts freshly fetched games first, then everything from the
// old catalog that the incremental walk did not revisit. Without this a
// refresh would REPLACE the catalog with just the few new pages it read.
func mergeCatalogs(fresh, old []itchio.Game) []itchio.Game {
	seen := make(map[string]bool, len(fresh)+len(old))
	merged := make([]itchio.Game, 0, len(fresh)+len(old))
	for _, list := range [][]itchio.Game{fresh, old} {
		for _, g := range list {
			if seen[g.URL] {
				continue
			}
			seen[g.URL] = true
			merged = append(merged, g)
		}
	}
	return merged
}

// sleepCtx sleeps for d, returning early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// filterGamesByPlatform returns the subset of games matching code, or every
// game when code is "" (the "All Systems" state).
func filterGamesByPlatform(games []itchio.Game, code string) []itchio.Game {
	if code == "" {
		return games
	}
	var out []itchio.Game
	for _, g := range games {
		if g.Platform == code {
			out = append(out, g)
		}
	}
	return out
}

// filterGamesBySearch keeps only games whose title contains query
// (case-insensitive substring match), or every game when query is "".
func filterGamesBySearch(games []itchio.Game, query string) []itchio.Game {
	if query == "" {
		return games
	}
	q := strings.ToLower(query)
	var out []itchio.Game
	for _, g := range games {
		hay := strings.ToLower(g.Title + " " + g.Author + " " + g.Platform)
		if activeHub != nil {
			hay += " " + strings.ToLower(activeHub.consoleLabel(g.Platform))
		}
		if strings.Contains(hay, q) {
			out = append(out, g)
		}
	}
	return out
}

// stripHTML turns the game page's markup into readable text: tags removed,
// block boundaries become spaces, entities decoded. The page description
// arrives as HTML, which is why "<b>", "<h2>" and "<p>" were showing up
// verbatim in the panel.
func stripHTML(html string) string {
	if html == "" {
		return ""
	}
	text := htmlTagRegex.ReplaceAllString(html, " ")
	text = stdhtml.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

// sortGames implements appui.FilterSortValues. Sorts reorder; "dl"/"owned"
// are really FILTERS (show only downloaded / only owned) and drop the rest.
// "" (RSS) leaves the fetched order — which, when the catalog was fetched in
// popularity mode, means most-popular-first.
func sortGames(games []itchio.Game, mode string, inv *inventory.Inventory, acct account) []itchio.Game {
	switch mode {
	case "dl":
		// Downloaded: keep only games present in the inventory.
		out := make([]itchio.Game, 0, len(games))
		for _, g := range games {
			if inv != nil && inv.IsPresent(g.URL) {
				out = append(out, g)
			}
		}
		return out
	case "owned":
		// Owned: keep only games the signed-in account purchased.
		ownedURLs := make(map[string]bool, len(acct.owned))
		for _, o := range acct.owned {
			ownedURLs[o.URL] = true
		}
		out := make([]itchio.Game, 0, len(games))
		for _, g := range games {
			if ownedURLs[g.URL] {
				out = append(out, g)
			}
		}
		return out
	}

	out := append([]itchio.Game(nil), games...)
	switch mode {
	case "popular":
		// Rank 0 means the game never appeared in the popularity pass, so it
		// sorts after everything that did rather than jumping to the front.
		sort.SliceStable(out, func(i, j int) bool {
			ri, rj := out[i].Rank, out[j].Rank
			switch {
			case ri == 0 && rj == 0:
				return false
			case ri == 0:
				return false
			case rj == 0:
				return true
			default:
				return ri < rj
			}
		})
	case "az":
		sort.SliceStable(out, func(i, j int) bool {
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		})
	case "za":
		sort.SliceStable(out, func(i, j int) bool {
			return strings.ToLower(out[i].Title) > strings.ToLower(out[j].Title)
		})
	case "new":
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].PublishedAt.After(out[j].PublishedAt)
		})
	case "free":
		// Free first, but keep the rest below rather than hiding them.
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].IsFree && !out[j].IsFree
		})
	case "paid":
		sort.SliceStable(out, func(i, j int) bool {
			return !out[i].IsFree && out[j].IsFree
		})
	}
	return out
}

// cyclePlatform steps through "" (All Systems) plus every platform code
// actually present in games, in itchio.AllPlatforms's own order — the same
// curated order used for fetching, so the cycle matches the feed list this
// app already ships. direction is +1 (R2/next) or -1 (L2/previous).
func cyclePlatform(games []itchio.Game, search, current string, direction int) string {
	// Only offer systems that actually have results under the active search.
	// Without this, stepping systems after a search drops the user on an
	// empty list with no hint that the search is what emptied it.
	matching := filterGamesBySearch(games, search)
	present := make(map[string]bool)
	for _, g := range matching {
		if g.Platform != "" {
			present[g.Platform] = true
		}
	}
	codes := []string{""}
	for _, p := range itchio.AllPlatforms {
		if present[p.Code] {
			codes = append(codes, p.Code)
			delete(present, p.Code)
		}
	}
	// Hub mode: RetroAchievements console codes are not itch.io feed codes.
	rest := make([]string, 0, len(present))
	for code := range present {
		rest = append(rest, code)
	}
	sort.Strings(rest)
	codes = append(codes, rest...)
	if len(codes) == 1 {
		return "" // nothing but "All Systems" would have results
	}
	index := 0
	for i, c := range codes {
		if c == current {
			index = i
			break
		}
	}
	index = ((index+direction)%len(codes) + len(codes)) % len(codes)
	return codes[index]
}

func main() {
	// Required on Linux/KMS-DRM (no X11 underneath): every SDL call must
	// come from the same OS thread that initialized SDL, but Go's scheduler
	// is free to migrate a goroutine across OS threads unless pinned. The
	// original Leaf pak's own bridge contract (docs/catastrophe-bridge.md)
	// states this explicitly: "The owning goroutine locks its OS thread
	// before initialization. Every bridge operation except Wake rejects
	// calls from another OS thread." We do the Go-idiomatic equivalent here.
	runtime.LockOSThread()

	migrateLegacyData()

	cfg := loadConfig()
	noROM := loadNoROM()
	// --login / --logout / --whoami run without opening the UI at all.
	if handleTerminalLogin(cfg, os.Args) {
		return
	}
	// --ra-login / --hub-sync / --hub-status / ... are headless too.
	if handleHubCLI(cfg, os.Args) {
		return
	}

	if err := configureDarkOSPaths(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to configure ROM paths:", err)
		os.Exit(1)
	}

	// 0,0 means "use the display's native resolution, fullscreen" — the
	// Miniloong's panel is 960x720, but reading it from SDL keeps this
	// correct on any other device too.
	screen, err := sdlui.Open(fontPath(), 0, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to start sdlui:", err)
		os.Exit(1)
	}
	defer screen.Close()

	client := itchio.NewClient()

	// The catalog is the RetroAchievements hub; itch.io is only where the
	// files come from. See hub.go.
	hub, _ := newHubContext(cfg, client, func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
	})
	activeHub = hub
	hub.setFilterPlatforms()
	// Prices in reais (converted from the itch.io price).
	startFX()
	sdlui.PriceFormat = func(v float64) string { return formatBRL(v) }
	sdlui.DetailHintExtra = "Search again"
	sdlui.DetailHintNotInstalled = "Hide"
	sdlui.QRLabel = "Scan: RetroAchievements page"
	armHide := 0       // game waiting for the second Y press
	hiddenCursor := 0  // cursor in Settings > Hidden games
	systemsCursor := 0 // cursor in Settings > Hidden systems
	hubChanged := make(chan struct{}, 1)
	notifyHub := func() {
		select {
		case hubChanged <- struct{}{}:
		default:
		}
	}

	// internal/inventory is Leaf's own record of downloaded games: which game
	// URL produced which file, where it landed, and when. Using it (instead
	// of listing the ROM folders) is what makes the manage screen show only
	// what this app installed.
	inv, err := inventory.Load(inventoryPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not load inventory, starting a new one:", err)
		inv, _ = inventory.Load("")
	}
	if inv != nil {
		// Drop entries whose files the user deleted outside the app.
		inv.VerifyAndClean(inventoryPath())
	}
	// Games installed by v49 went straight into /roms/<system>/; they now
	// live in /roms/<system>/itchio/.
	hub.moveIntoItchioFolders(inv)

	validateKeyDeferred := true
	listModel := appui.NewMainListModel(nil)
	listModel.VisibleRows = screen.ListRows()

	var allGames []itchio.Game       // full fetched/cached set, never filtered
	var displayedGames []itchio.Game // parallel to listModel.Items — what openDetail must index into
	platformFilter := ""             // "" means "All Systems" (kept in sync with the Filter screen)
	searchQuery := ""                // from the Filter screen's Search row
	// Defaults: the best-regarded games first, and nothing hidden by
	// default — the 18+ row is there to opt OUT, not to opt in.
	sortMode := ""    // hub order (by system); see appui.FilterSortValues
	showMode := "all" // see appui.FilterShowValues
	genreFilter := "" // RA genre, "" = all
	var detailModel *appui.DetailModel
	var filterModel *appui.FilterModel
	var keyboard *keyboardModel
	keyboardTarget := "search" // or "apikey"
	var manageModel *appui.ManageModel
	var installed []installedROM
	downloadStatus := ""
	detailCover := ""        // cover art URL of the game open on the detail screen
	var detailFiles []string // upload filenames from the fetched game page
	detailQR := -1           // texture slot of the QR code for the open game page
	detailGameID := ""       // numeric itch.io game id, needed for owned downloads
	fetching := false        // true while the first-run catalog walk is in flight
	var lastLoadingDraw time.Time
	var lastAnimDraw time.Time
	// Held-button state for the list's fast scroll. Gamepad buttons have no
	// auto-repeat, so the repeat is implemented here: a tap jumps ten, and
	// holding keeps stepping until released.
	var heldScroll appui.Button
	var heldSince, lastHeldStep time.Time
	mode := modeList

	// applyFilter recomputes what's on screen from allGames + the current
	// platform/search/sort settings and pushes it into listModel. This is
	// the only place that's allowed to call listModel.SetItems once
	// allGames is populated, so displayedGames can never drift out of sync
	// with what's actually rendered (openDetail indexes into displayedGames
	// using listModel.Cursor — if these two disagree about ordering, it
	// opens the wrong game).
	// The list only has the RSS blurb (160 chars). The full description lives
	// on the game's page, so it is fetched lazily for whichever game the
	// cursor rests on, and cached. Debounced so scrolling does not fire a
	// request per row.
	descCache := map[string]string{}
	descPending := map[string]bool{}
	descDone := make(chan struct {
		url, text string
	}, 4)
	var cursorSettledAt time.Time
	descFailures := 0
	var descPausedUntil time.Time
	var lastDescURL string
	_ = lastDescURL

	var acct account
	accountDone := make(chan account, 1)
	// Validating the key hits the network, so it runs in the background like
	// every other fetch; the result is applied on this goroutine.
	validateKey := func(key string) {
		if key == "" {
			acct = account{checked: true}
			return
		}
		go func() {
			username, owned, err := client.ValidateAPIKey(key)
			accountDone <- account{username: username, owned: owned, checked: true, err: err}
		}()
	}

	var ownedModel *appui.ManageModel
	var settingsCursor int
	var installedFlags []bool // parallel to listModel.Items
	applyFilter := func() {
		filtered := filterGamesWithROM(allGames, noROM)
		filtered = hubShowFilter(filtered, showMode, inv)
		filtered = hubGenreFilter(filtered, genreFilter)
		filtered = filterGamesByPlatform(filtered, platformFilter)
		filtered = filterGamesBySearch(filtered, searchQuery)
		filtered = hubSort(filtered, sortMode)
		displayedGames = filtered
		listModel.Platform = platformFilter
		listModel.SetItems(gamesToItems(displayedGames))
		// "Filtered" means "narrowed from the default view", so the default
		// sort/adult values do not count as a filter being applied.
		screen.SetFilterActive(platformFilter != "" || searchQuery != "" ||
			sortMode != "" || showMode != "all" || genreFilter != "")
		installedFlags = make([]bool, len(displayedGames))
		for i, g := range displayedGames {
			installedFlags[i] = inv != nil && inv.IsPresent(g.URL)
		}
	}

	// The fetch runs on its own goroutine (network I/O shouldn't block
	// input), but listModel must only ever be touched from this main
	// goroutine — same OS-thread-ownership rule as the SDL calls above,
	// plus it avoids a plain Go data race on listModel's fields. So the
	// goroutine only computes results and hands them over on a channel;
	// applying them (SetItems) happens down in the main loop below.
	type fetchResult struct {
		games []itchio.Game // empty + !save means "show demo data instead"
		save  bool          // true once this result should be written to disk
		err   string        // hub mode: why the hub could not be read
	}
	fetchDone := make(chan fetchResult, 1)

	// internal/itchio.Client.FetchAllGames walks every feed in AllPlatforms
	// (docs/platforms.go): itch.io's own homebrew/ROM-hack tag feeds per
	// console (tag-homebrew/tag-psx, tag-gba, tag-nes-rom, made-with-gb-studio,
	// ...). This is the same curated catalog the original Leaf pak ships —
	// not a general itch.io browser — so nothing else needed to change to
	// get "homebrew and ROM hacks only".
	//
	// Cache policy mirrors Leaf's own internal/ui/catalog_controller.go
	// exactly, and — same as theirs — this check only ever runs here, once,
	// at startup. There is no timer or background daemon re-checking while
	// the app stays open:
	//   - no cache on disk yet -> show the loading state, do a full fetch
	//     now (first run only; can take a while, same as Leaf's dedicated
	//     "Refreshing Game List" screen)
	//   - cache younger than 48h -> use it immediately, no network at all
	//   - cache older than 48h   -> show it immediately, then refresh in
	//     the background; the list updates itself if/when that finishes
	// progressCh carries partial results from the fetch goroutine so the
	// list fills in as pages arrive. Buffered + non-blocking send: the fetch
	// must never stall waiting for the UI, and a dropped intermediate update
	// costs nothing since the next one supersedes it anyway.
	progressCh := make(chan []itchio.Game, 1)
	publishProgress := func(partial []itchio.Game) {
		select {
		case progressCh <- partial:
		default:
		}
	}

	// refreshHub re-reads the hub (and RA hashes) in the background and
	// hands the new list to the main loop. The previous itch.io tag-feed
	// walk is gone: the catalog is exactly the hub's games.
	refreshHub := func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			err := hub.refresh(ctx, func(format string, a ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", a...)
			})
			res := fetchResult{games: hub.catalogGames()}
			if err != nil {
				fmt.Fprintln(os.Stderr, "hub refresh:", err)
				res.err = err.Error()
			}
			fetchDone <- res
		}()
	}

	if os.Getenv("POC_OFFLINE") == "1" {
		listModel.SetItems(demoItems())
	} else if cached := hub.catalogGames(); len(cached) > 0 {
		allGames = cached
		applyFilter()
		listModel.CacheStatus = ""
		// A saved list older than cacheTTL is refreshed in the
		// background, so new RetroAchievements games turn up on their
		// own. The list on screen stays usable while that runs; only
		// Settings > "Refresh RetroAchievements hub" forces it early.
		//
		// cacheTTL had been declared and never read, which meant the
		// catalog only ever moved when somebody remembered to ask.
		if age := hub.cacheAge(); age > cacheTTL {
			fmt.Fprintf(os.Stderr, "saved list is %s old, refreshing in the background\n",
				age.Round(time.Hour))
			refreshHub()
		}
	} else {
		listModel.SetLoading()
		fetching = true
		screen.SetLoadingCatalog(true)
		refreshHub()
	}

	// detailDone carries a fetched game page back to the GUI goroutine, same
	// channel discipline as every other worker here.
	type detailResult struct {
		url    string
		detail *itchio.GameDetail
		err    error
	}
	detailDone := make(chan detailResult, 1)

	// detailCancel stops the fetch belonging to the page the user just
	// left. Opening pages quickly used to leave one request in flight per
	// page, all of them for screens nobody is looking at any more.
	var detailCancel context.CancelFunc
	loadDetail := func(gameURL string) {
		if detailCancel != nil {
			detailCancel()
		}
		if gameURL == "" {
			detailCancel = nil
			return
		}
		pageCtx, cancel := context.WithCancel(context.Background())
		detailCancel = cancel
		go func() {
			defer cancel()
			if hg, ok := hub.gameFor(gameURL); ok {
				// Hub game: find its itch.io page (cached after the first
				// time), read the description from there, and lead with the
				// verification status.
				ctx, cancelFetch := context.WithTimeout(pageCtx, time.Minute)
				defer cancelFetch()
				d := &itchio.GameDetail{}
				if itchURL, err := hub.itchURLFor(ctx, gameURL); err == nil {
					if !rahub.IsItchURL(itchURL) {
						// The developer's own site: text and picture from it.
						d.Description = hub.pageInfo(ctx, client, hg.ID, itchURL)
						hub.saveDesc(hg.ID, d.Description)
						notifyHub()
					} else if fetched, err := client.FetchGameDetail(itchURL); err == nil {
						d = fetched
						hub.saveDesc(hg.ID, stripHTML(d.Description))
						notifyHub()
					}
				}
				if pageCtx.Err() != nil {
					return // the user moved on; this page is gone
				}
				d.Description = hub.describe(hg, stripHTML(d.Description))
				d.BrowserOnly = false
				detailDone <- detailResult{url: gameURL, detail: d}
				return
			}
			d, err := client.FetchGameDetail(gameURL)
			if pageCtx.Err() != nil {
				return
			}
			detailDone <- detailResult{url: gameURL, detail: d, err: err}
		}()
	}

	// downloadDone carries the result of a download started from the detail
	// screen back to the main goroutine, same channel pattern as fetchDone
	// and for the same reason (no cross-goroutine model/SDL access).
	type downloadResult struct {
		gameTitle string
		status    string
	}
	downloadDone := make(chan downloadResult, 1)
	// armUnverified carries a game ID whose verification failed or is
	// impossible: the NEXT press of A on it installs without verification.
	armUnverified := make(chan int, 1)
	armedUnverified := 0

	// descriptionFor returns the best description known for a game: the page
	// text when it has been fetched, otherwise the short feed blurb.
	descriptionFor := func(gameURL string) string {
		if text, ok := descCache[gameURL]; ok && strings.TrimSpace(text) != "" {
			return text
		}
		for _, g := range allGames {
			if g.URL == gameURL {
				return g.Blurb
			}
		}
		return ""
	}

	startDownload := func(game appui.DetailGame, coverURL, gameID string) {
		if hg, ok := hub.gameFor(game.URL); ok {
			if st := hub.store.State(hg.ID); demoGone(st) {
				downloadStatus = "This demo is no longer available, so it cannot be downloaded. (Y hides it.)"
				if st.Status == rahub.StatusVersionGone {
					downloadStatus = fmt.Sprintf("The version with achievements (v%s) is no longer offered; itch.io has v%s. (Y hides it.)",
						strings.Join(st.RAVersions, "/v"), strings.Join(st.ItchVersions, "/v"))
				}
				return
			}
			if armedUnverified == -hg.ID {
				// SELECT after a failed/impossible verification: the user
				// chose to install without it.
				armedUnverified = 0
				downloadStatus = "Installing WITHOUT verification..."
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
					defer cancel()
					progress := func(msg string) {
						select {
						case downloadDone <- downloadResult{game.Title, msg}:
						default:
						}
					}
					res, err := hub.installUnverified(ctx, inv, hg, progress)
					notifyHub()
					if err != nil {
						downloadDone <- downloadResult{game.Title, friendlyError(err)}
						return
					}
					downloadDone <- downloadResult{game.Title, "Installed WITHOUT verification: " + res.Path +
						". RetroArch will show on start whether RetroAchievements recognises it."}
				}()
				return
			}
			downloadStatus = "Searching itch.io..."
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
				defer cancel()
				progress := func(msg string) {
					select {
					case downloadDone <- downloadResult{game.Title, msg}:
					default: // UI busy: skip an intermediate step, the final one follows
					}
				}
				res, err := hub.install(ctx, inv, hg, false, progress)
				notifyHub()
				switch {
				case err != nil && canInstallUnverified(err):
					select {
					case armUnverified <- hg.ID:
					default:
					}
					downloadDone <- downloadResult{game.Title, friendlyError(err) +
						"  Press SELECT to install it anyway WITHOUT verification (achievements may not work)."}
				case err != nil:
					downloadDone <- downloadResult{game.Title, friendlyError(err)}
				case res.Already:
					downloadDone <- downloadResult{game.Title, "Your installed file is the current one (verified by RetroAchievements) - nothing to update."}
				default:
					downloadDone <- downloadResult{game.Title, "VERIFIED (RA hash " + res.MD5[:8] + "...) - installed to " + res.Path}
				}
			}()
			return
		}
		downloadStatus = "Downloading..."
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			// Paid games: the public page lists no downloadable file at all
			// unless you own them. With a signed-in API key we can ask
			// itch.io for the purchase key and pull the upload through it,
			// which is how a bought game gets installed.
			apiKey := cfg.key()
			if apiKey != "" && gameID != "" && !game.IsFree {
				if path, ok, err := downloadOwned(ctx, client, inv, apiKey, gameID, game, coverURL, descriptionFor(game.URL)); ok {
					downloadDone <- downloadResult{game.Title, "Downloaded to " + path}
					return
				} else if err != nil {
					// Not owned, or the key does not cover it: fall through
					// to the public path, which will explain what the page
					// actually offers.
					fmt.Fprintln(os.Stderr, "owned download not available:", err)
				}
			}

			uploads, err := client.FetchUploads(game.URL)
			if err != nil || len(uploads) == 0 {
				if apiKey == "" && !game.IsFree {
					downloadDone <- downloadResult{game.Title,
						"This is a paid game. Sign in with your itch.io API key (START > Settings) to download games you own."}
					return
				}
				// A PAID game shows no downloadable files to someone who
				// does not own it. That is not "this game has no ROM", and
				// hiding it was wrong — it removed a perfectly good game
				// from the catalog because of a permissions answer.
				if !game.IsFree {
					downloadDone <- downloadResult{game.Title,
						"This is a paid game. Sign in (START > Settings) with the account that owns it to download."}
					return
				}
				noROM.add(game.URL, "no downloadable files (browser-only?)")
				downloadDone <- downloadResult{game.Title,
					"Nothing downloadable here (browser-only game) — hidden from the list."}
				return
			}
			// itchio.Upload and roms.Upload are structurally identical but
			// distinct types (each package owns its own copy) — convert.
			romUploads := make([]roms.Upload, len(uploads))
			for i, u := range uploads {
				romUploads[i] = roms.Upload{
					Filename: u.Filename, URL: u.URL, UploadID: u.UploadID, NeedsFormat: u.NeedsFormat,
				}
			}
			best := roms.SelectBest(romUploads)
			if best == nil {
				// No bare ROM upload — try an archive, which is how a lot of
				// homebrew is actually published.
				archive := pickArchiveUpload(romUploads)
				sniffedExt := ".zip"
				if archive == nil {
					// itch.io upload names are display names and often carry
					// no extension at all ("Goodboy Galaxy DEMO for GBA
					// (roms only)"), so name-based rules find nothing. Sniff
					// the real content instead: a ranged request for the
					// first bytes is cheap even for an 80 MB upload.
					archive, sniffedExt = sniffUploads(ctx, client, romUploads, game.Platform)
				}
				// A sniffed bare ROM needs no extraction — download it
				// straight into its system folder.
				if archive != nil && sniffedExt != ".zip" && sniffedExt != ".7z" {
					destDir := roms.DestinationDir(sniffedExt)
					name := roms.SanitiseFilename(game.Title, sniffedExt)
					if name == "" {
						name = archive.Filename
					}
					destPath := filepath.Join(destDir, name)
					up := itchio.Upload{
						Filename: archive.Filename, URL: archive.URL,
						UploadID: archive.UploadID, NeedsFormat: archive.NeedsFormat,
					}
					if err := client.DownloadFreeContext(ctx, up, destPath, nil); err != nil {
						downloadDone <- downloadResult{game.Title, "Download failed: " + err.Error()}
						return
					}
					recordInstall(inv, inventoryPath(), installRecord{
						GameURL: game.URL, Title: game.Title, Author: game.Author,
						CoverURL: coverURL, IsFree: game.IsFree,
						DestPath: destPath, Upload: archive.Filename,
						Desc: descriptionFor(game.URL),
					})
					downloadDone <- downloadResult{game.Title, "Downloaded to " + destPath}
					return
				}
				if archive != nil {
					downloadDone <- downloadResult{game.Title, "Downloading archive..."}
					tmp, err := os.CreateTemp("", "itchio-archive-*")
					if err != nil {
						downloadDone <- downloadResult{game.Title, "Could not create temp file: " + err.Error()}
						return
					}
					tmpPath := tmp.Name()
					tmp.Close()
					defer os.Remove(tmpPath)

					archiveUpload := itchio.Upload{
						Filename: archive.Filename, URL: archive.URL,
						UploadID: archive.UploadID, NeedsFormat: archive.NeedsFormat,
					}
					if err := client.DownloadFreeContext(ctx, archiveUpload, tmpPath, nil); err != nil {
						downloadDone <- downloadResult{game.Title, "Archive download failed: " + err.Error()}
						return
					}
					extracted, err := extractROMFromArchive(tmpPath, archive.Filename, game.Title)
					if err != nil {
						if !game.IsFree {
							downloadDone <- downloadResult{game.Title,
								"Could not install this paid game: " + err.Error()}
							return
						}
						// The archive was a PC build, not a cartridge. Same
						// treatment as an empty page: remember and hide.
						noROM.add(game.URL, err.Error())
						downloadDone <- downloadResult{game.Title,
							err.Error() + " — hidden from the list."}
						return
					}
					recordInstall(inv, inventoryPath(), installRecord{
						GameURL: game.URL, Title: game.Title, Author: game.Author,
						CoverURL: coverURL, IsFree: game.IsFree,
						DestPath: extracted, Upload: archive.Filename, Archive: archive.Filename,
						Desc: descriptionFor(game.URL),
					})
					downloadDone <- downloadResult{game.Title, "Extracted to " + extracted}
					return
				}
				// The page has files, just none this device can run — e.g.
				// a Windows build or a .love/.pocket bundle. Name what was
				// actually offered so it is obvious why nothing downloaded.
				names := make([]string, 0, len(uploads))
				for i, u := range uploads {
					if i == 3 {
						names = append(names, "...")
						break
					}
					names = append(names, u.Filename)
				}
				reason := strings.Join(names, ", ")
				if !game.IsFree {
					downloadDone <- downloadResult{game.Title,
						"Paid game — sign in to see what your purchase includes. Page shows: " + reason}
					return
				}
				// Remember it so the list stops offering this game.
				noROM.add(game.URL, reason)
				downloadDone <- downloadResult{game.Title,
					"No ROM this console can run — hidden from the list. Page offers: " + reason}
				return
			}
			ext := roms.ROMExt(best.Filename)
			destDir := roms.DestinationDir(ext)
			if destDir == "" {
				downloadDone <- downloadResult{game.Title, "Unsupported file type: " + ext}
				return
			}
			filename := roms.SanitiseFilename(game.Title, ext)
			if filename == "" {
				filename = best.Filename
			}
			destPath := filepath.Join(destDir, filename)

			bestItchio := itchio.Upload{
				Filename: best.Filename, URL: best.URL, UploadID: best.UploadID, NeedsFormat: best.NeedsFormat,
			}
			// A .cue/.m3u names data tracks that live in separate files, so
			// downloading it alone installs a pointer to nothing — the
			// emulator then fails at load. Go straight for the page's
			// archive, which holds the complete set.
			if needsCompanionFiles(best.Filename) {
				downloadDone <- downloadResult{game.Title, "Fetching the full disc image..."}

				// Preferred route: an archive holding the whole set. Names on
				// itch.io are display names with no extension, so candidates
				// are identified by content and tried in ranked order —
				// several of them, because a page can offer one build per
				// console and the first guess may be the wrong platform.
				installed := false
				for _, bundle := range archiveCandidates(ctx, client, romUploads, game.Platform, 3) {
					full, err := downloadAndExtractBundle(ctx, client, bundle, destDir)
					if err != nil {
						fmt.Fprintf(os.Stderr, "candidate %q unusable: %v\n", bundle.Filename, err)
						continue
					}
					fmt.Fprintf(os.Stderr, "installed from %q\n", bundle.Filename)
					destPath = full
					installed = true
					break
				}
				// Fallback: the tracks are separate uploads on the page.
				if !installed {
					full, companions, err := downloadLooseSet(ctx, client, romUploads, *best, destDir)
					if err != nil {
						downloadDone <- downloadResult{game.Title,
							"Could not install: " + err.Error() + " (" + best.Filename +
								" is only an index and its data files are missing)"}
						return
					}
					fmt.Fprintf(os.Stderr, "installed %s with %d data file(s)\n", full, companions)
					destPath = full
				}
			} else {
				err = client.DownloadFreeContext(ctx, bestItchio, destPath, nil)
				if err != nil {
					downloadDone <- downloadResult{game.Title, "Download failed: " + err.Error()}
					return
				}

				// A PICO-8 cart that #includes other files is only part of
				// the release — fake08 reports "Can't find included file"
				// and falls back to its default cart.
				if cartNeedsIncludes(destPath) {
					if bundle := pickArchiveUpload(romUploads); bundle != nil {
						downloadDone <- downloadResult{game.Title, "Cart needs extra files, fetching the full release..."}
						if full, exErr := downloadAndExtractBundle(ctx, client, *bundle, destDir); exErr == nil {
							os.Remove(destPath) // the incomplete single cart
							destPath = full
						} else {
							fmt.Fprintln(os.Stderr, "cart bundle failed, keeping the single cart:", exErr)
						}
					}
				}
			}

			recordInstall(inv, inventoryPath(), installRecord{
				GameURL: game.URL, Title: game.Title, Author: game.Author,
				CoverURL: coverURL, IsFree: game.IsFree,
				DestPath: destPath, Upload: best.Filename,
				Desc: descriptionFor(game.URL),
			})
			downloadDone <- downloadResult{game.Title, "Downloaded to " + destPath}
		}()
	}

	// playGame hands the screen to the emulator and takes it back afterwards.
	// Nothing else may touch SDL while it runs, which is why it happens on
	// this goroutine, synchronously, between frames.
	playGame := func(system, romPath, title string) {
		screen.Suspend()
		err := launchGame(system, romPath)
		if resumeErr := screen.Resume(fontPath()); resumeErr != nil {
			// Without a display there is nothing left to show; exiting
			// returns the user to EmulationStation rather than stranding
			// them on a dead screen.
			fmt.Fprintln(os.Stderr, "could not take the display back:", resumeErr)
			os.Exit(1)
		}
		listModel.VisibleRows = screen.ListRows()
		if err != nil {
			downloadStatus = "Could not launch " + title + ": " + err.Error()
			if hint := biosHint(system); hint != "" {
				downloadStatus = hint
			}
			fmt.Fprintln(os.Stderr, "launch failed:", err)
		} else {
			downloadStatus = ""
		}
	}

	// flash shows a short message in the list header for a few seconds.
	var flashUntil time.Time
	flash := func(msg string) {
		listModel.CacheStatus = msg
		flashUntil = time.Now().Add(8 * time.Second)
		time.AfterFunc(8500*time.Millisecond, notifyHub)
	}
	draw := func() {
		// Per-game facts for the side panel, recomputed for the current
		// selection only (cheap) rather than stored per row.
		if mode == modeList {
			info := sdlui.PreviewInfo{}
			if listModel.Cursor >= 0 && listModel.Cursor < len(displayedGames) {
				g := displayedGames[listModel.Cursor]
				info.System = platformLabelForFolder(systemFolderByCode[g.Platform])
				if g.IsFree {
					info.Price = "Free"
				} else {
					info.Price = fmt.Sprintf("$%.2f", g.Price)
				}
				if !g.PublishedAt.IsZero() {
					info.Published = "Published " + g.PublishedAt.Format("Jan 2006")
				}
				if len(g.Tags) > 0 {
					info.Tags = strings.Join(g.Tags, ", ")
				}
				info.Adult = g.Adult
				if id, ok := gameIDFromURL(g.URL); ok {
					hg, _ := hub.store.Game(id)
					st := hub.store.State(id)
					info.System = hg.Console().Name
					info.Price = longStatus(hg, st)
					// One facts line (system - status); no repeated RA
					// numbers: the space goes to the cached itch.io description.
					info.Published = ""
					// RetroAchievements' tags: Homebrew, Demo, Hack, Prototype...
					tagLine := rahub.TitleTags(hg.Title)
					if genre := gameGenre(hg); genre != "" {
						tagLine = append([]string{genre}, tagLine...)
					}
					info.Tags = strings.Join(tagLine, "  ·  ")
					info.Description = hub.previewText(hg, st.ItchDesc)
				}
				// Prefer the full page description; fall back to the RSS
				// blurb until it arrives.
				if long, ok := descCache[g.URL]; ok && strings.TrimSpace(long) != "" {
					info.Description = long
				}
				lastDescURL = g.URL
				if inv != nil && inv.IsPresent(g.URL) {
					info.Installed = "Installed"
					if rom := installedROMFor(inv, g.URL); rom != nil && rom.size > 0 {
						info.Installed = "Installed - " + formatSize(rom.size)
					}
				}
			}
			screen.SetPreviewInfo(info)
		}
		switch mode {
		case modeList:
			screen.DrawMainList(listModel, installedFlags)
		case modeDetail:
			sdlui.DetailFullGame = false
			sdlui.DetailNoDownload = false
			sdlui.DetailStatus.Text, sdlui.DetailFacts = "", nil
			if hg, ok := hub.gameFor(detailModel.Game.URL); ok {
				st := hub.store.State(hg.ID)
				sdlui.DetailFullGame = hub.fullOwned(hg.ID)
				_, paid, owned, known := priceOf(st)
				sdlui.DetailNoDownload = demoGone(st) || (known && paid && !owned && !detailModel.Game.Downloaded)
				sdlui.DetailReinstallLabel = "Reinstall"
				if st.Status == rahub.StatusOutdated {
					sdlui.DetailReinstallLabel = "Update"
				}
				sdlui.DetailStatus.Text, sdlui.DetailStatus.Level = hub.statusCard(hg, st)
				sdlui.DetailFacts = hub.detailFacts(hg, st)
			}
			screen.DrawDetail(detailModel, downloadStatus, detailCover, detailFiles, detailQR)
		case modeFilter:
			screen.DrawFilter(filterModel, listModel, installedFlags)
		case modeKeyboard:
			prompt := "Type a title or author"
			if keyboardTarget == "apikey" {
				prompt = "Paste your itch.io API key"
			}
			if keyboardTarget == "rakey" {
				prompt = "RetroAchievements web API key"
			}
			screen.DrawKeyboard(keyboard.String(), keyboardRows, keyboard.row, keyboard.col, prompt)
		case modeManage:
			screen.DrawManage(manageModel, listModel, installedFlags)
		case modeQuitConfirm:
			screen.DrawQuitConfirm(listModel, installedFlags)
		case modeSettings:
			screen.DrawSettings(settingsRows(cfg, acct, noROM, hub), settingsCursor, listModel, installedFlags)
		case modeOwned:
			screen.DrawManage(ownedModel, listModel, installedFlags)
		case modeHidden:
			screen.DrawSettings(hiddenRows(hub), hiddenCursor, listModel, installedFlags)
		case modeSystems:
			screen.DrawSettings(systemRows(hub), systemsCursor, listModel, installedFlags)
		}
	}

	if validateKeyDeferred {
		validateKey(cfg.key())
	}
	// Background itch.io scan: fills page, cover and description for every
	// hub game into the cache while the list is being browsed.
	if os.Getenv("POC_OFFLINE") != "1" {
		hub.startItchScan(client, notifyHub)
		hub.startStatsScan(notifyHub)    // RA popularity for the "Most played" order
		hub.startProgressScan(notifyHub) // your mastered/played games
	}

	draw()
	for {
		// Apply a finished fetch or download here, in the main goroutine,
		// before touching input — never from inside the worker goroutines.
		select {
		case result := <-fetchDone:
			fetching = false
			screen.SetLoadingCatalog(false)
			listModel.CacheStatus = ""
			if len(result.games) == 0 {
				allGames = nil
				applyFilter()
				listModel.CacheStatus = "could not read the RetroAchievements hub"
				if result.err != "" {
					listModel.CacheStatus += ": " + result.err
				}
			} else {
				hub.setFilterPlatforms()
				allGames = result.games
				applyFilter()
				if result.save {
					if err := itchio.SaveGamesCache(cachePath(), result.games); err != nil {
						fmt.Fprintln(os.Stderr, "failed to save catalog cache:", err)
					}
				}
			}
			if hub.lastRefresh != "" && len(result.games) > 0 {
				flash(hub.lastRefresh)
				hub.lastRefresh = ""
			}
			draw()
		case partial := <-progressCh:
			// Partial catalog: show what's arrived so the list is browsable
			// within seconds instead of after the whole multi-minute walk.
			allGames = partial
			applyFilter()
			listModel.CacheStatus = fmt.Sprintf("loading… %d found", len(partial))
			draw()
		case result := <-descDone:
			delete(descPending, result.url)
			descCache[result.url] = result.text
			// itch.io answers a burst of page requests with HTTP 522. Back
			// off rather than keep hammering it — descriptions are a nicety,
			// and a blocked host breaks the catalog too.
			if strings.TrimSpace(result.text) == "" {
				descFailures++
				if descFailures >= 3 {
					descFailures = 0
					descPausedUntil = time.Now().Add(2 * time.Minute)
					fmt.Fprintln(os.Stderr, "pausing description fetches for 2 minutes (itch.io is refusing requests)")
				}
			} else {
				descFailures = 0
			}
			if mode == modeList && listModel.Cursor < len(displayedGames) &&
				displayedGames[listModel.Cursor].URL == result.url {
				draw()
			}
		case result := <-accountDone:
			acct = result
			// Paid itch.io pages are only downloaded when bought.
			hub.setOwned(result.owned)
			if result.signedIn() {
				screen.SetAccountLabel(result.username)
			} else {
				screen.SetAccountLabel("")
			}
			if result.err != nil {
				fmt.Fprintln(os.Stderr, "itch.io sign-in failed:", result.err)
			} else {
				fmt.Fprintf(os.Stderr, "signed in as %s (%d owned)\n", result.username, len(result.owned))
			}
			draw()
		case result := <-detailDone:
			// Ignore a page that arrives after the user already moved on.
			if detailModel != nil && detailModel.Game.URL == result.url {
				if result.err != nil {
					detailModel.SetError(result.err.Error())
				} else {
					detailModel.SetReady(result.detail.Description, result.detail.PageTags,
						result.detail.ScreenshotURLs, result.detail.BrowserOnly, false)
					detailModel.Game.CanDownload = !result.detail.BrowserOnly
					if hg, isHub := hub.gameFor(result.url); isHub {
						detailModel.Game.CanDownload = true
						// The page lookup may have just found the price.
						detailModel.Game.IsFree, detailModel.Game.Price = priceFields(hub.store.State(hg.ID))
						detailModel.Game.PriceText = longStatus(hg, hub.store.State(hg.ID))
						detailModel.Game.Platform = ""
					}
					detailModel.Game.Downloaded = inv.IsPresent(result.url)
					detailGameID = result.detail.GameID
					detailFiles = detailFiles[:0]
					for _, u := range result.detail.Uploads {
						detailFiles = append(detailFiles, u.Filename)
					}
				}
				draw()
			}
		case <-hubChanged:
			allGames = hub.catalogGames()
			applyFilter()
			flashing := !flashUntil.IsZero() && time.Now().Before(flashUntil)
			if !flashing && !flashUntil.IsZero() {
				flashUntil = time.Time{}
				listModel.CacheStatus = "" // a short message has had its time
			}
			if flashing {
				// keep the message on screen for now
			} else if done, total := hub.itchProgress(); done < total {
				listModel.CacheStatus = fmt.Sprintf("itch.io data %d/%d", done, total)
			} else if strings.HasPrefix(listModel.CacheStatus, "itch.io data") {
				listModel.CacheStatus = ""
			}
			draw()
		case id := <-armUnverified:
			armedUnverified = id
		case result := <-downloadDone:
			if mode == modeDetail && detailModel != nil && detailModel.Game.Title == result.gameTitle {
				downloadStatus = result.status
				// Mark it installed straight away so the Play chip appears
				// without having to leave the page and come back.
				if inv != nil && inv.IsPresent(detailModel.Game.URL) {
					detailModel.Game.Downloaded = true
				}
				applyFilter()
				draw()
			}
		default:
		}

		// Upload any cover art that finished downloading. This must happen
		// on this goroutine: SDL texture creation belongs to the thread that
		// owns the window (the same rule that forces runtime.LockOSThread).
		if screen.ProcessPendingImages() {
			draw()
		}

		event, ok, quit := screen.Poll()
		if quit {
			return
		}
		if !ok {
			// Cursor has rested on a row: fetch its full description.
			if mode == modeList && !cursorSettledAt.IsZero() &&
				time.Now().After(descPausedUntil) &&
				time.Since(cursorSettledAt) >= 700*time.Millisecond {
				cursorSettledAt = time.Time{}
				if listModel.Cursor >= 0 && listModel.Cursor < len(displayedGames) {
					url := displayedGames[listModel.Cursor].URL
					if id, isHub := gameIDFromURL(url); isHub {
						// The background scan fetches it (next in line).
						hub.prioritize(id)
					} else if url != "" && !descPending[url] {
						if _, cached := descCache[url]; !cached {
							descPending[url] = true
							go func(u string) {
								text := ""
								if d, err := client.FetchGameDetail(u); err == nil {
									text = stripHTML(d.Description)
								} else {
									fmt.Fprintln(os.Stderr, "description fetch failed:", err)
								}
								descDone <- struct{ url, text string }{u, text}
							}(url)
						}
					}
				}
			}
			// Holding L1/R1 scrolls continuously instead of stopping at the
			// single ten-item jump the tap produced. The first repeat waits
			// out a short hold delay so a tap is never mistaken for a hold.
			if heldScroll != appui.ButtonNone && mode == modeList &&
				time.Since(heldSince) >= 350*time.Millisecond &&
				time.Since(lastHeldStep) >= 60*time.Millisecond {
				lastHeldStep = time.Now()
				if n := len(listModel.Items); n > 0 {
					// Accelerate the longer it is held: a catalog of
					// thousands is unusable at a fixed rate, and ramping up
					// keeps short holds precise while long holds cross the
					// whole list quickly.
					held := time.Since(heldSince)
					step := 10
					switch {
					case held > 3*time.Second:
						step = 120
					case held > 2*time.Second:
						step = 60
					case held > time.Second:
						step = 25
					}
					if heldScroll == appui.ButtonL1 {
						step = -step
					}
					listModel.Cursor += step
					if listModel.Cursor < 0 {
						listModel.Cursor = 0
					}
					if listModel.Cursor >= n {
						listModel.Cursor = n - 1
					}
				}
				draw()
				continue
			}
			// An animated cover is on screen: schedule its next frame. This
			// is rate-limited to ~12fps because Present() is expensive on
			// this device's KMS driver — enough for a GIF to read as
			// animated without the slowdown that an unthrottled redraw loop
			// caused earlier.
			if time.Since(lastAnimDraw) >= 80*time.Millisecond && screen.Animating() {
				lastAnimDraw = time.Now()
				draw()
				continue
			}
			// No input, and nothing finished either (handled above). While
			// the first-run fetch is still running, keep redrawing so the
			// loading screen is actually visible — a single Present() at
			// startup can land before the fullscreen mode switch completes
			// and leave the panel black for the whole multi-minute walk.
			// Once loaded, nothing changes without a button press, so skip
			// Present() entirely: repeated Present() calls are expensive on
			// this device's KMS driver.
			if fetching && time.Since(lastLoadingDraw) >= 500*time.Millisecond {
				// Throttled: this device's KMS driver makes Present() costly,
				// and the first-run walk takes minutes — redrawing at the
				// poll rate (10x/s) for that long is a real slowdown for no
				// visible benefit.
				lastLoadingDraw = time.Now()
				draw()
			}
			continue
		}
		if !event.Pressed {
			if event.Button == heldScroll {
				heldScroll = appui.ButtonNone
			}
			// Button-release events are no-ops in appui's Handle() methods
			// (see main_list.go/detail.go: `if !event.Pressed { return
			// ...None }`) — skip the redraw for them too.
			continue
		}

		redraw := true
		switch mode {
		case modeList:
			// L1/R1 page through the list ten at a time. The vendored model
			// binds them to sort cycling, so they are intercepted here
			// rather than patching vendored code.
			if event.Button == appui.ButtonY {
				// One-press escape from a filter that emptied the list.
				platformFilter, searchQuery = "", ""
				sortMode, showMode, genreFilter = "", "all", ""
				applyFilter()
				draw()
				continue
			}
			if event.Button == appui.ButtonL1 || event.Button == appui.ButtonR1 {
				heldScroll = event.Button
				heldSince = time.Now()
				lastHeldStep = time.Time{}
				if n := len(listModel.Items); n > 0 {
					step := 10
					if event.Button == appui.ButtonL1 {
						step = -10
					}
					listModel.Cursor += step
					if listModel.Cursor < 0 {
						listModel.Cursor = 0
					}
					if listModel.Cursor >= n {
						listModel.Cursor = n - 1
					}
				}
				draw()
				continue
			}
			switch listModel.Handle(event) {
			case appui.ListIntentExit:
				// Confirm rather than dropping straight back to
				// EmulationStation — B is also "back" everywhere else, so an
				// accidental press at the top level shouldn't end the session.
				mode = modeQuitConfirm
			case appui.ListIntentOpen:
				item, ok := listModel.Selected()
				if !ok {
					redraw = false
					break
				}
				detailModel = openDetail(item, displayedGames, listModel.Cursor)
				downloadStatus = ""
				detailCover = item.CoverKey
				detailFiles = nil
				detailGameID = ""
				detailQR = -1 // no QR code on the game page any more
				mode = modeDetail
				loadDetail(detailModel.Game.URL)
			case appui.ListIntentNextPlatform:
				platformFilter = cyclePlatform(allGames, searchQuery, platformFilter, 1)
				applyFilter()
			case appui.ListIntentPreviousPlatform:
				platformFilter = cyclePlatform(allGames, searchQuery, platformFilter, -1)
				applyFilter()
			case appui.ListIntentFilter:
				hub.setFilterGenres()
				filterModel = appui.NewFilterModel(platformFilter, sortMode, searchQuery, showMode, genreFilter)
				mode = modeFilter
			case appui.ListIntentSettings:
				settingsCursor = 0
				mode = modeSettings
			case appui.ListIntentDismissNotice:
				// X on the list opens the installed-games manager. The
				// vendored model reports X as DismissNotice (it had no
				// manage entry point from the list), so that intent is
				// reused here rather than patching vendored code.
				installed = installedFromInventory(inv)
				manageModel = buildManageModel(installed, screen.ManageRows())
				mode = modeManage
			}
		case modeFilter:
			switch filterModel.Handle(event) {
			case appui.FilterIntentEditSearch:
				keyboardTarget = "search"
				keyboard = newKeyboardModel(filterModel.Query)
				mode = modeKeyboard
			case appui.FilterIntentApply:
				platformFilter = filterModel.Platform
				sortMode = filterModel.Sort
				searchQuery = filterModel.Query
				showMode = filterModel.Show
				genreFilter = filterModel.Genre
				applyFilter()
				mode = modeList
			case appui.FilterIntentCancel:
				// Discard whatever was staged in filterModel — platformFilter/
				// sortMode/searchQuery (the committed values) are untouched.
				mode = modeList
			}
		case modeKeyboard:
			if done, cancelled := keyboard.Handle(event); done {
				if keyboardTarget == "rakey" {
					cfg.mu.Lock()
					cfg.RAKey = strings.TrimSpace(keyboard.String())
					cfg.mu.Unlock()
					if err := cfg.save(); err != nil {
						fmt.Fprintln(os.Stderr, "could not save the RA key:", err)
					}
					hub.ra.Creds = raCredentials(cfg)
					mode = modeSettings
				} else if keyboardTarget == "apikey" {
					if err := cfg.setKey(keyboard.String()); err != nil {
						fmt.Fprintln(os.Stderr, "could not save the key:", err)
					}
					acct = account{}
					validateKey(cfg.key())
					mode = modeSettings
				} else {
					filterModel.Query = keyboard.String()
					mode = modeFilter
				}
			} else if cancelled {
				if keyboardTarget == "apikey" || keyboardTarget == "rakey" {
					mode = modeSettings
				} else {
					mode = modeFilter
				}
			} else {
				// Every other keypress (typing, moving the cursor around the
				// grid) still needs a redraw; only done/cancelled change mode.
			}
		case modeSystems:
			rows := systemRows(hub)
			switch event.Button {
			case appui.ButtonUp:
				if systemsCursor > 0 {
					systemsCursor--
				}
			case appui.ButtonDown:
				if systemsCursor < len(rows)-1 {
					systemsCursor++
				}
			case appui.ButtonB, appui.ButtonQuit:
				mode = modeSettings
			case appui.ButtonStart:
				mode = modeList
			case appui.ButtonA:
				if consoles := hub.hubConsoles(); systemsCursor < len(consoles) {
					c := consoles[systemsCursor]
					hub.setConsoleHidden(c.ID, !hub.store.ConsoleHidden(c.ID))
					// A hidden system cannot stay selected in the filter.
					if platformFilter != "" && hub.store.ConsoleHidden(c.ID) && c.Short == platformFilter {
						platformFilter = ""
					}
					allGames = hub.catalogGames()
					applyFilter()
				}
			}
		case modeHidden:
			rows := hiddenRows(hub)
			switch event.Button {
			case appui.ButtonUp:
				if hiddenCursor > 0 {
					hiddenCursor--
				}
			case appui.ButtonDown:
				if hiddenCursor < len(rows)-1 {
					hiddenCursor++
				}
			case appui.ButtonB, appui.ButtonQuit:
				mode = modeSettings
			case appui.ButtonStart:
				mode = modeList
			case appui.ButtonA:
				if hiddenCursor < len(rows) && rows[hiddenCursor].Action {
					if rows[hiddenCursor].Label == "Show all hidden games" {
						for _, g := range hub.hiddenGames() {
							hub.setHidden(g.ID, false)
						}
					} else if hidden := hub.hiddenGames(); hiddenCursor-1 < len(hidden) && hiddenCursor >= 1 {
						hub.setHidden(hidden[hiddenCursor-1].ID, false)
					}
					allGames = hub.catalogGames()
					applyFilter()
					if n := len(hiddenRows(hub)); hiddenCursor >= n {
						hiddenCursor = n - 1
					}
					if len(hub.hiddenGames()) == 0 {
						mode = modeSettings
					}
				}
			}
		case modeSettings:
			rows := settingsRows(cfg, acct, noROM, hub)
			switch event.Button {
			case appui.ButtonUp:
				if settingsCursor > 0 {
					settingsCursor--
				}
			case appui.ButtonDown:
				if settingsCursor < len(rows)-1 {
					settingsCursor++
				}
			case appui.ButtonB, appui.ButtonQuit, appui.ButtonStart:
				// START opened Settings, so START closes it too.
				mode = modeList
			case appui.ButtonA:
				switch rows[settingsCursor].Label {
				case "Sign in with an API key", "Replace API key":
					keyboardTarget = "apikey"
					keyboard = newKeyboardModel("")
					mode = modeKeyboard
				case "Sign out":
					if err := cfg.setKey(""); err != nil {
						fmt.Fprintln(os.Stderr, "could not clear the key:", err)
					}
					acct = account{checked: true}
					screen.SetAccountLabel("")
					settingsCursor = 0
				case "Your purchases":
					ownedModel = buildOwnedModel(acct, screen.ManageRows())
					mode = modeOwned
				case "Forget games with no ROM":
					if err := noROM.clear(); err != nil {
						fmt.Fprintln(os.Stderr, "could not clear the list:", err)
					}
					applyFilter()
				case "Set RetroAchievements key":
					keyboardTarget = "rakey"
					keyboard = newKeyboardModel("")
					mode = modeKeyboard
				case "Hidden systems":
					systemsCursor = 0
					mode = modeSystems
				case "Hidden games":
					if len(hub.hiddenGames()) > 0 {
						hiddenCursor = 0
						mode = modeHidden
					}
				case "Re-scan games not found":
					n := hub.rescanDoubtful()
					fmt.Fprintf(os.Stderr, "re-scanning %d doubtful game(s)\n", n)
					notifyHub()
					mode = modeList
				case "Re-scan itch.io (all games)":
					n := hub.store.ResetItchData()
					_ = hub.store.Save()
					hub.resolved.Range(func(k, _ any) bool { hub.resolved.Delete(k); return true })
					fmt.Fprintf(os.Stderr, "re-scanning itch.io for %d game(s)\n", n)
					notifyHub()
					mode = modeList
				case "Refresh RetroAchievements hub":
					if !fetching {
						fetching = true
						screen.SetLoadingCatalog(true)
						listModel.CacheStatus = "reading the hub..."
						refreshHub()
					}
					mode = modeList
				case "Rebuild everything (slow)":
					// known=nil forces the walk to read every page of every
					// feed instead of stopping at the first already-cached
					// page. Needed after changing the catalog scope, or when
					// the incremental refresh has drifted.
					if !fetching {
						fetching = true
						screen.SetLoadingCatalog(true)
						listModel.CacheStatus = "full rebuild..."
						go func() {
							fetched, err := fetchFullCatalog(context.Background(), client, publishProgress, nil)
							if len(fetched) == 0 {
								fmt.Fprintln(os.Stderr, "full rebuild failed, keeping the old catalog:", err)
								fetchDone <- fetchResult{games: allGames}
								return
							}
							if err != nil {
								fmt.Fprintln(os.Stderr, "rebuild finished with some errors (partial results kept):", err)
							}
							// Replaces rather than merges: a rebuild is the
							// way to drop games that no longer match the
							// current scope.
							fetchDone <- fetchResult{games: fetched, save: true}
						}()
					}
					mode = modeList
				case "Refresh (new games only)":
					if !fetching {
						fetching = true
						screen.SetLoadingCatalog(true)
						listModel.CacheStatus = "refreshing..."
						go func() {
							fetched, err := fetchFullCatalog(context.Background(), client, publishProgress, knownURLs(allGames))
							if len(fetched) == 0 {
								fmt.Fprintln(os.Stderr, "refresh failed, keeping the old catalog:", err)
								fetchDone <- fetchResult{games: allGames}
								return
							}
							if err != nil {
								fmt.Fprintln(os.Stderr, "refresh finished with some errors (partial results kept):", err)
							}
							fetchDone <- fetchResult{games: mergeCatalogs(fetched, allGames), save: true}
						}()
					}
					mode = modeList
				}
			}
		case modeOwned:
			switch ownedModel.Handle(event) {
			case appui.ManageIntentBack:
				mode = modeSettings
			case appui.ManageIntentActivate:
				// Opening a purchase jumps to it in the catalog when it is
				// one of the homebrew titles this app lists; otherwise
				// there is nothing to show, since the catalog only covers
				// console-ROM tags.
				if ownedModel.Cursor >= 0 && ownedModel.Cursor < len(acct.owned) {
					target := acct.owned[ownedModel.Cursor].URL
					for i, g := range displayedGames {
						if g.URL == target {
							listModel.Cursor = i
							mode = modeList
							break
						}
					}
				}
			}
		case modeQuitConfirm:
			switch event.Button {
			case appui.ButtonA:
				return
			case appui.ButtonB, appui.ButtonQuit:
				mode = modeList
			}
		case modeManage:
			if event.Button == appui.ButtonY && manageModel.State == appui.ManageList {
				if manageModel.Cursor >= 0 && manageModel.Cursor < len(installed) {
					rom := installed[manageModel.Cursor]
					if !rom.missing {
						playGame(rom.system, rom.path, rom.title)
					}
				}
				draw()
				continue
			}
			// X opens the game's own page. Without it this window was a
			// dead end: you could play a download or delete it, but not
			// get back to the page it came from to read about it or
			// fetch another file.
			if event.Button == appui.ButtonX && manageModel.State == appui.ManageList {
				if manageModel.Cursor >= 0 && manageModel.Cursor < len(installed) {
					rom := installed[manageModel.Cursor]
					if idx, item, ok := findGameByURL(displayedGames, rom.gameURL); ok {
						listModel.Cursor = idx
						detailModel = openDetail(item, displayedGames, idx)
						downloadStatus = ""
						detailCover = item.CoverKey
						detailFiles = nil
						detailGameID = ""
						detailQR = -1
						mode = modeDetail
						loadDetail(detailModel.Game.URL)
					} else {
						manageModel.SetError("That game is not in the current list — clear the filter and try again.")
					}
				}
				draw()
				continue
			}
			switch manageModel.Handle(event) {
			case appui.ManageIntentBack:
				// Dismissing the "Deleted X" confirmation should land back
				// on the downloads list, not eject the user to the catalog.
				if manageModel.State == appui.ManageResult || manageModel.State == appui.ManageError {
					manageModel = buildManageModel(installed, screen.ManageRows())
					break
				}
				mode = modeList
			case appui.ManageIntentActivate:
				if manageModel.Cursor >= 0 && manageModel.Cursor < len(installed) {
					rom := installed[manageModel.Cursor]
					manageModel.SetConfirm("Delete this file?",
						[]string{rom.name, "from " + rom.system, "", "This cannot be undone."})
				}
			case appui.ManageIntentConfirm:
				if manageModel.Cursor >= 0 && manageModel.Cursor < len(installed) {
					rom := installed[manageModel.Cursor]
					err := os.Remove(rom.path)
					if err != nil && !os.IsNotExist(err) {
						manageModel.SetError("Could not delete: " + err.Error())
						break
					}
					// Drop the inventory record too. Without this the file
					// is gone from disk but the entry survives, so the game
					// keeps showing up in this very list.
					inv.RemoveFile(rom.gameURL, rom.path)
					if err := inv.Save(inventoryPath()); err != nil {
						fmt.Fprintln(os.Stderr, "could not save inventory:", err)
					}
					installed = installedFromInventory(inv)
					// Recompute the catalog's green "installed" markers too,
					// or a just-deleted game keeps showing as installed in
					// the list behind this window.
					applyFilter()
					if detailModel != nil && detailModel.Game.URL == rom.gameURL {
						detailModel.Game.Downloaded = false
					}
					// Straight back to the list rather than parking on a
					// "Deleted X" screen the user then has to dismiss.
					manageModel = buildManageModel(installed, screen.ManageRows())
				}
			case appui.ManageIntentCancel:
				manageModel.SetItems(manageModel.Subtitle, manageModel.Items)
			}
		case modeDetail:
			// SELECT confirms an unverified install offered after a failed
			// verification (A only ever runs the verified install).
			if event.Button == appui.ButtonSelect {
				hg, ok := hub.gameFor(detailModel.Game.URL)
				switch {
				case ok && armedUnverified == hg.ID:
					armedUnverified = -hg.ID
					startDownload(detailModel.Game, detailCover, detailGameID)
					armedUnverified = 0
				case ok:
					// Search this one game again, now (quick + wide).
					downloadStatus = "Searching itch.io again..."
					title := detailModel.Game.Title
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
						defer cancel()
						u, err := hub.searchAgain(ctx, client, hg)
						notifyHub()
						if err != nil {
							downloadDone <- downloadResult{title, "Still not found on itch.io."}
							return
						}
						downloadDone <- downloadResult{title, "Found: " + u + "  (press A to download)"}
					}()
				}
				draw()
				continue
			}
			// Up/Down move to the previous/next game in the list without
			// going back to it. The vendored model binds them to description
			// scrolling, so that moves to L2/R2 below and these are handled
			// here first. L1/R1 and Left/Right still drive the gallery,
			// which the model does handle.
			if event.Button == appui.ButtonUp || event.Button == appui.ButtonDown {
				step := 1
				if event.Button == appui.ButtonUp {
					step = -1
				}
				next := listModel.Cursor + step
				if next >= 0 && next < len(listModel.Items) {
					listModel.Cursor = next
					item := listModel.Items[next]
					detailModel = openDetail(item, displayedGames, next)
					downloadStatus = ""
					detailCover = item.CoverKey
					detailFiles = nil
					detailGameID = ""
					detailQR = -1
					loadDetail(detailModel.Game.URL)
				}
				draw()
				continue
			}
			if event.Button == appui.ButtonL2 || event.Button == appui.ButtonR2 {
				if event.Button == appui.ButtonL2 {
					detailModel.ScrollLine--
				} else {
					detailModel.ScrollLine++
				}
				if detailModel.ScrollLine < 0 {
					detailModel.ScrollLine = 0
				}
				if detailModel.ScrollLine > detailModel.ScrollMax {
					detailModel.ScrollLine = detailModel.ScrollMax
				}
				draw()
				continue
			}
			// X on a demo game whose full release was bought: install the
			// full game (it has no RetroAchievements set).
			if event.Button == appui.ButtonX && !detailModel.Game.Downloaded {
				if hg, ok := hub.gameFor(detailModel.Game.URL); ok && hub.fullOwned(hg.ID) {
					downloadStatus = "Installing the full game..."
					title := detailModel.Game.Title
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
						defer cancel()
						progress := func(msg string) {
							select {
							case downloadDone <- downloadResult{title, msg}:
							default:
							}
						}
						res, err := hub.installFull(ctx, inv, hg, progress)
						notifyHub()
						if err != nil {
							downloadDone <- downloadResult{title, friendlyError(err)}
							return
						}
						downloadDone <- downloadResult{title, "Full game installed: " + res.Path + " (no RetroAchievements for the full game)"}
					}()
					draw()
					continue
				}
			}
			// Y on a game that is not installed hides it from the list; the
			// first press only asks, the second one hides.
			if event.Button == appui.ButtonY && !detailModel.Game.Downloaded {
				if hg, ok := hub.gameFor(detailModel.Game.URL); ok {
					if armHide == hg.ID {
						armHide = 0
						hub.setHidden(hg.ID, true)
						mode = modeList
						allGames = hub.catalogGames()
						applyFilter()
						flash("hidden: " + detailModel.Game.Title + " (Settings > Hidden games to undo)")
					} else {
						armHide = hg.ID
						downloadStatus = "Press Y again to hide this game from the list. (Undo in Settings > Hidden games.)"
					}
				}
				draw()
				continue
			}
			// Y plays the game when it is installed. The vendored model has
			// no Play intent (Leaf's pak only downloads), so this is handled
			// before Handle() rather than by patching vendored code.
			if event.Button == appui.ButtonY && detailModel.Game.Downloaded {
				if rom := installedROMFor(inv, detailModel.Game.URL); rom != nil {
					playGame(rom.system, rom.path, detailModel.Game.Title)
				}
				draw()
				continue
			}
			// X opens the downloads manager from here too, including for
			// games that aren't installed (the vendored model only offers
			// Manage once Downloaded is set).
			if event.Button == appui.ButtonX {
				installed = installedFromInventory(inv)
				manageModel = buildManageModel(installed, screen.ManageRows())
				mode = modeManage
				draw()
				continue
			}
			switch detailModel.Handle(event) {
			case appui.DetailIntentBack:
				mode = modeList
			case appui.DetailIntentManage:
				installed = installedFromInventory(inv)
				manageModel = buildManageModel(installed, screen.ManageRows())
				mode = modeManage
			case appui.DetailIntentDownload:
				if downloadStatus == "Downloading..." {
					redraw = false
					break
				}
				startDownload(detailModel.Game, detailCover, detailGameID)
			}
		}
		if redraw {
			if mode == modeList {
				cursorSettledAt = time.Now()
			}
			draw()
		}
	}
}

// openDetail builds a DetailModel from whichever game was selected. If we
// have the real itch.io Game struct (network path succeeded), a real build
// would call Client.FetchGameDetail(g.URL) here for description/screenshots;
// this PoC fills in a placeholder description to keep the demo path (no
// network, no real URL) working too.
// findGameByURL locates a game in the displayed list by its page URL,
// which is the one field an installed record and a catalog entry always
// share. Returns its index and the list item the detail page needs.
func findGameByURL(games []itchio.Game, url string) (int, appui.ListItem, bool) {
	if url == "" {
		return 0, appui.ListItem{}, false
	}
	for i, g := range games {
		if g.URL == url {
			return i, appui.ListItem{
				Title:    g.Title,
				Author:   g.Author,
				CoverKey: g.CoverURL,
				Badge:    g.Platform,
			}, true
		}
	}
	return 0, appui.ListItem{}, false
}

func openDetail(item appui.ListItem, games []itchio.Game, index int) *appui.DetailModel {
	game := appui.DetailGame{
		Title:       item.Title,
		Author:      item.Author,
		CanDownload: true,
	}
	if index >= 0 && index < len(games) {
		g := games[index]
		game.URL = g.URL
		game.Price = g.Price
		game.IsFree = g.IsFree
		game.Platform = g.Platform
		if activeHub != nil {
			if hg, ok := activeHub.gameFor(g.URL); ok {
				// The console is already in the subtitle; the status says
				// more than a bare price.
				game.Platform = ""
				st := activeHub.store.State(hg.ID)
				game.PriceText = longStatus(hg, st)
				// Header: who made it and for what; the numbers are in the
				// facts on the right.
				game.Author = hg.Console().Name
				if page, ok := itchPage(st); ok && page.Author != "" {
					game.Author = page.Author + "   ·   " + game.Author
				}
			}
		}
	} else {
		game.IsFree = item.Badge == "Free"
		if !game.IsFree {
			// Demo/offline items only carry a formatted badge like "$3.99",
			// not a raw float — parse it back out so the detail screen
			// doesn't show $0.00 for anything but the free item.
			priceText := strings.TrimPrefix(item.Badge, "$")
			if parsed, err := strconv.ParseFloat(priceText, 64); err == nil {
				game.Price = parsed
			}
		}
	}
	model := appui.NewDetailModel(game)
	if game.URL == "" {
		// Nothing to fetch (demo data) — show what the list already knows
		// rather than sitting on a loading state forever.
		model.SetReady("", item.Tags, nil, false, false)
	}
	// Otherwise the model stays in DetailLoading until loadDetail's result
	// arrives and fills in the real description, tags and screenshots.
	return model
}

// hiddenRows lists the hidden games in Settings > Hidden games; A on one
// brings it back to the list.
func hiddenRows(hub *hubContext) []sdlui.SettingsRow {
	rows := []sdlui.SettingsRow{{Section: "HIDDEN GAMES (A: show again)", Label: "Show all hidden games", Action: true}}
	for _, g := range hub.hiddenGames() {
		rows = append(rows, sdlui.SettingsRow{Label: rahub.CleanTitle(g.Title), Value: g.Console().Name, Action: true})
	}
	return rows
}

// systemRows lists every console of the hub with whether it is shown; A
// switches it. A hidden system's games are left out of the list, the
// counts and the itch.io scan until it is shown again.
func systemRows(hub *hubContext) []sdlui.SettingsRow {
	var rows []sdlui.SettingsRow
	for i, c := range hub.hubConsoles() {
		state := "shown"
		if hub.store.ConsoleHidden(c.ID) {
			state = "HIDDEN"
		}
		row := sdlui.SettingsRow{Label: fmt.Sprintf("%s  (%d)", c.Name, c.Games), Value: state, Action: true}
		if i == 0 {
			row.Section = "SYSTEMS (A: show / hide)"
		}
		rows = append(rows, row)
	}
	return rows
}

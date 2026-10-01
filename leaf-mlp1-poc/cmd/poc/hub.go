package main

// RetroAchievements hub mode.
//
// The catalog no longer comes from itch.io tag feeds: it is exactly the games
// of one RetroAchievements hub (3036 by default). itch.io is only where the
// files are fetched from, and a file is installed ONLY when its
// RetroAchievements hash matches — a similar title or the "right looking"
// itch.io page is never enough.
//
// Every hub game is shown in the list as an itchio.Game whose URL is its RA
// page (https://retroachievements.org/game/<id>). That URL is the game's key
// everywhere (inventory, installed markers, detail screen), and it stays the
// same whether or not an itch.io page has been found for it yet.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaf-mlp1-poc/internal/appui"
	"leaf-mlp1-poc/internal/inventory"
	"leaf-mlp1-poc/internal/itchio"
	"leaf-mlp1-poc/internal/rahub"
	"leaf-mlp1-poc/internal/sdlui"
)

// defaultRAUser is the RetroAchievements account the hub pipeline was built
// for. Overridable with RA_USER or --ra-login.
const defaultRAUser = "nicefrog"

// hubContext is everything hub mode needs, shared by the UI and the CLI.
type hubContext struct {
	store    *rahub.Store
	ra       *rahub.RAClient
	pipe     *rahub.Pipeline
	hubID    int
	onlyAch  bool // hub filter: only games with published achievements
	busyMu   sync.Mutex
	busy     map[int]bool // games with an install in flight
	resolved sync.Map     // RA URL -> itch URL (cache for descriptions)

	want chan int // games the user is looking at: fetched before the rest
	// lastRefresh summarises the last hub refresh for the list header.
	lastRefresh string

	ownedMu sync.RWMutex
	owned   map[string]string // normalised itch.io page URL -> game ID, from the itch.io account
}

// setOwned records the itch.io account's purchases. Paid pages are only
// downloaded when they appear here.
func (h *hubContext) setOwned(games []itchio.OwnedGame) {
	m := make(map[string]string, len(games))
	for _, g := range games {
		if g.URL != "" && g.GameID != 0 {
			m[rahub.NormPageURL(g.URL)] = fmt.Sprintf("%d", g.GameID)
		}
	}
	h.ownedMu.Lock()
	h.owned = m
	h.ownedMu.Unlock()
}

func (h *hubContext) ownedLookup(pageURL string) (string, bool) {
	h.ownedMu.RLock()
	defer h.ownedMu.RUnlock()
	id, ok := h.owned[rahub.NormPageURL(pageURL)]
	return id, ok
}

// raCredentials returns the RA user/key: environment first (RA_USER /
// RA_KEY), then the app config. The key is never printed.
func raCredentials(cfg *config) rahub.Credentials {
	cfg.mu.Lock()
	user, key := cfg.RAUser, cfg.RAKey
	cfg.mu.Unlock()
	if v := strings.TrimSpace(os.Getenv("RA_USER")); v != "" {
		user = v
	}
	if v := strings.TrimSpace(os.Getenv("RA_KEY")); v != "" {
		key = v
	}
	if user == "" {
		user = defaultRAUser
	}
	return rahub.Credentials{User: user, Key: key}
}

func hubIDFromEnv(cfg *config) int {
	if v := strings.TrimSpace(os.Getenv("RA_HUB_ID")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	if cfg.HubID > 0 {
		return cfg.HubID
	}
	return rahub.DefaultHubID
}

// itchSource adapts internal/itchio to the pipeline. apiKey returns the
// itch.io API key (empty when not signed in); it is only used for games the
// account bought.
type itchSource struct {
	client *itchio.Client
	apiKey func() string
}

func (s itchSource) Profile(ctx context.Context, slug string) ([]rahub.Candidate, string, error) {
	return rahub.FetchItchProfile(ctx, s.client.HTTPClient(), slug)
}

// OwnedUploads lists what a purchase grants, trying every download key the
// account holds for the game (direct purchase and bundles).
func (s itchSource) OwnedUploads(ctx context.Context, gameID string) ([]rahub.Upload, error) {
	key := s.apiKey()
	if key == "" {
		return nil, fmt.Errorf("not signed in to itch.io")
	}
	keys, err := s.client.FetchOwnedKeys(key, gameID)
	if err != nil {
		return nil, fmt.Errorf("check purchases: %w", err)
	}
	var out []rahub.Upload
	seen := map[string]bool{}
	for _, k := range keys {
		keyID := fmt.Sprintf("%d", k.ID)
		ups, err := s.client.FetchUploadsForKey(key, gameID, keyID)
		if err != nil {
			continue
		}
		for _, u := range ups {
			if seen[u.UploadID] {
				continue
			}
			seen[u.UploadID] = true
			out = append(out, rahub.Upload{Name: u.Filename, ID: u.UploadID, URL: u.URL, KeyID: keyID})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the purchase lists no files")
	}
	return out, nil
}

func (s itchSource) Search(ctx context.Context, q string) ([]rahub.Candidate, error) {
	return rahub.SearchItch(ctx, s.client.HTTPClient(), q)
}

func (s itchSource) Uploads(ctx context.Context, pageURL string) ([]rahub.Upload, error) {
	if !rahub.IsItchURL(pageURL) {
		// A page on the developer's own site (pinned with --hub-set-url).
		return rahub.WebPageUploads(ctx, s.client.HTTPClient(), pageURL)
	}
	ups, err := s.client.FetchUploads(pageURL)
	if err != nil {
		return nil, err
	}
	out := make([]rahub.Upload, len(ups))
	for i, u := range ups {
		out[i] = rahub.Upload{Name: u.Filename, ID: u.UploadID, URL: u.URL, Version: u.Version}
	}
	return out, nil
}

func (s itchSource) Download(ctx context.Context, pageURL string, up rahub.Upload, dest string) error {
	// Give up as soon as the file turns out bigger than this console's
	// games can be (a 1 GB PC build under a matching title).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tooBig := false
	progress := func(done, total int64) {
		if up.MaxBytes > 0 && (total > up.MaxBytes || done > up.MaxBytes) && !tooBig {
			tooBig = true
			cancel()
		}
	}
	var err error
	if up.KeyID == "" && !rahub.IsItchURL(up.URL) {
		return rahub.DownloadPlain(ctx, s.client.HTTPClient(), up.URL, dest, up.MaxBytes)
	}
	if up.KeyID != "" {
		err = s.client.DownloadAuthUploadContext(ctx, s.apiKey(), up.ID, up.KeyID, dest, progress)
	} else {
		err = s.client.DownloadFreeContext(ctx, itchio.Upload{Filename: up.Name, URL: up.URL, UploadID: up.ID}, dest, progress)
	}
	if tooBig {
		os.Remove(dest)
		return fmt.Errorf("%s is larger than %d MB, not a game for this console - skipped", up.Name, up.MaxBytes>>20)
	}
	return err
}

// itchioSubdir is the folder, inside each system's ROM folder, that holds
// everything this app installs: /roms/atari2600/itchio/Alien Force.a26.
// EmulationStation shows it as a folder inside the system.
const itchioSubdir = "itchio"

// systemRoot finds the ROM folder EmulationStation scans for a console, and
// the extensions it accepts there: the first of the console's system names
// that es_systems.cfg defines, else an existing /roms/<name> folder. A
// console the firmware has no system for is an error — a file there would
// never show up in the menu.
func systemRoot(c rahub.Console) (dir string, exts []string, err error) {
	if _, list, lerr := loadESSystems(); lerr == nil {
		// First the system whose folder is literally /roms/<name> (so SNES
		// lands in /roms/snes even if another entry, like Sufami Turbo, is
		// also tied to SNES), then a system with that name.
		byFolder := func(sys esSystem, name string) bool {
			return strings.EqualFold(filepath.Base(strings.TrimRight(strings.TrimSpace(sys.Path), "/")), name)
		}
		byName := func(sys esSystem, name string) bool { return strings.EqualFold(sys.Name, name) }
		for _, match := range []func(esSystem, string) bool{byFolder, byName} {
			for _, name := range c.Systems {
				for _, sys := range list.Systems {
					if !match(sys, name) {
						continue
					}
					path := strings.TrimRight(strings.TrimSpace(sys.Path), "/")
					if path == "" {
						continue
					}
					seen := map[string]bool{}
					for _, e := range strings.Fields(sys.Extension) {
						e = strings.ToLower(e)
						if strings.HasPrefix(e, ".") && !seen[e] {
							seen[e] = true
							exts = append(exts, e)
						}
					}
					return path, exts, nil
				}
			}
		}
	}
	// Not under a known system name: match EmulationStation's full name
	// ("Pokemon Mini") instead.
	if _, list, lerr := loadESSystems(); lerr == nil {
		want := rahub.NormLoose(c.Name)
		for _, sys := range list.Systems {
			if want != "" && (rahub.NormLoose(sys.FullName) == want || rahub.NormLoose(sys.Name) == want) &&
				strings.TrimSpace(sys.Path) != "" {
				return strings.TrimRight(strings.TrimSpace(sys.Path), "/"), nil, nil
			}
		}
	}
	for _, name := range c.Systems {
		d := filepath.Join(romsRoot, name)
		if info, statErr := os.Stat(d); statErr == nil && info.IsDir() {
			return d, nil, nil
		}
	}
	return "", nil, fmt.Errorf("EmulationStation has no system for %s (tried %s)", c.Name, strings.Join(c.Systems, ", "))
}

// destDirForConsole is where the pipeline installs verified files.
func destDirForConsole(c rahub.Console) (rahub.Destination, error) {
	root, exts, err := systemRoot(c)
	if err != nil {
		return rahub.Destination{}, err
	}
	return rahub.Destination{Dir: filepath.Join(root, itchioSubdir), Exts: exts}, nil
}

// itchCatalogCandidates turns the old itch.io tag-feed cache, when present,
// into extra match candidates — free matches that cost no search request.
func itchCatalogCandidates() []rahub.Candidate {
	cache, err := itchio.LoadGamesCache(cachePath())
	if err != nil || cache == nil {
		return nil
	}
	out := make([]rahub.Candidate, 0, len(cache.Games))
	for _, g := range cache.Games {
		out = append(out, rahub.Candidate{URL: g.URL, Title: g.Title, Author: g.Author, CoverURL: g.CoverURL})
	}
	return out
}

func newHubContext(cfg *config, client *itchio.Client, logf func(string, ...any)) (*hubContext, error) {
	// Test hooks: point the RA / itch.io search calls at a local mock.
	if v := os.Getenv("POC_RA_BASE"); v != "" {
		rahub.RABase = v
	}
	if v := os.Getenv("POC_ITCH_SEARCH_BASE"); v != "" {
		rahub.ItchBase = v
	}
	hubID := hubIDFromEnv(cfg)
	store, err := rahub.OpenStore(dataDir(), hubID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	var alt *http.Client
	if client != nil {
		alt = client.HTTPClient()
	}
	ra := rahub.NewRAClient(raCredentials(cfg), alt)
	work := filepath.Join(dataDir(), "work")
	os.RemoveAll(work) // leftovers from an interrupted run
	pipe := rahub.NewPipeline(store, ra, itchSource{client: client, apiKey: cfg.key}, work, destDirForConsole)
	pipe.Catalog = itchCatalogCandidates()
	if logf != nil {
		pipe.Logf = logf
	}
	h := &hubContext{
		store: store, ra: ra, pipe: pipe, hubID: hubID,
		onlyAch: os.Getenv("RA_HUB_ALL") != "1",
		busy:    map[int]bool{},
	}
	pipe.Owned = h.ownedLookup
	rahub.WebLog = func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	h.upgradeStates()
	return h, nil
}

// refresh re-reads the hub and the RA hashes. On failure the stored catalog
// is kept untouched.
func (h *hubContext) refresh(ctx context.Context, logf func(string, ...any)) error {
	games, err := h.ra.FetchHub(ctx, h.hubID, h.onlyAch)
	if err != nil && len(games) == 0 {
		return err
	}
	if err != nil {
		return fmt.Errorf("hub listing incomplete, keeping the stored one: %w", err)
	}
	if h.ra.Creds.Valid() {
		if err := h.ra.AttachHashes(ctx, games, logf); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logf("some hashes could not be fetched (will retry per game): %v", err)
		}
	} else {
		// Keep hashes already known from an earlier run.
		for i := range games {
			if old, ok := h.store.Game(games[i].ID); ok {
				games[i].Hashes, games[i].PatchHashes, games[i].HashesKnown = old.Hashes, old.PatchHashes, old.HashesKnown
			}
		}
		logf("no RetroAchievements API key: hub listed, hashes not refreshed")
	}
	added, changed := h.store.SetGames(games)
	for _, g := range games {
		if !g.Console().Verifiable() && h.store.State(g.ID).Status == rahub.StatusNew {
			h.store.Update(g.ID, func(s *rahub.GameState) {
				s.Status, s.Note = rahub.StatusUnsupported, rahub.ErrNotVerifiable.Error()
			})
		}
	}
	logf("hub %d: %d games (%d new, %d with changed hashes)", h.hubID, len(games), added, changed)
	h.lastRefresh = fmt.Sprintf("hub refreshed: %d new game(s), %d with new RetroAchievements hashes", added, changed)
	if n := h.store.Counts()[rahub.StatusOutdated]; n > 0 {
		h.lastRefresh += fmt.Sprintf(" - %d installed game(s) need an UPDATE", n)
	}
	return h.store.Save()
}

// gameIDFromURL extracts the RA game ID from a hub game's key URL.
func gameIDFromURL(u string) (int, bool) {
	const marker = "/game/"
	i := strings.LastIndex(u, marker)
	if i < 0 || !strings.Contains(u, "retroachievements.org") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.Trim(u[i+len(marker):], "/"))
	return n, err == nil
}

func (h *hubContext) gameFor(u string) (rahub.HubGame, bool) {
	id, ok := gameIDFromURL(u)
	if !ok {
		return rahub.HubGame{}, false
	}
	return h.store.Game(id)
}

// installedPathOK reports whether a verified game's file is still on disk.
func installedPathOK(st rahub.GameState) bool {
	if st.InstalledPath == "" {
		return false
	}
	_, err := os.Stat(st.InstalledPath)
	return err == nil
}

// itchPage returns the itch.io page that stands for the game: the one a
// verified file came from, else the best match.
func itchPage(st rahub.GameState) (rahub.Candidate, bool) {
	if st.ItchURL != "" {
		for _, c := range st.Candidates {
			if rahub.NormPageURL(c.URL) == rahub.NormPageURL(st.ItchURL) {
				return c, true
			}
		}
		return rahub.Candidate{URL: st.ItchURL, Title: st.ItchTitle, Author: st.ItchAuthor}, true
	}
	if len(st.Candidates) > 0 {
		return st.Candidates[0], true
	}
	return rahub.Candidate{}, false
}

// priceOf reports the game's itch.io price. known is false until the
// background scan (or opening the game) has found its page.
func priceOf(st rahub.GameState) (label string, paid, owned, known bool) {
	page, ok := itchPage(st)
	if !ok {
		return "", false, false, false
	}
	if st.DemoFree == 1 {
		// Demo game: the free demo is what the achievements are for; the
		// paid full release is shown separately.
		return "Free", false, false, true
	}
	switch {
	case rahub.IsPaidPrice(page.Price):
		label, paid = priceInReais(page.Price), true
	case st.Status == rahub.StatusPaid:
		label, paid = "Paid", true
	default:
		label = "Free"
	}
	if paid && activeHub != nil {
		_, owned = activeHub.ownedLookup(page.URL)
	}
	return label, paid, owned, true
}

// priceValue turns "$3.85" into 3.85 (for the detail header and sorting).
func priceValue(label string) float64 {
	num := strings.Builder{}
	for _, r := range label {
		switch {
		case r >= '0' && r <= '9':
			num.WriteRune(r)
		case r == '.' || r == ',':
			num.WriteRune('.')
		}
	}
	v, _ := strconv.ParseFloat(strings.Trim(num.String(), "."), 64)
	return v
}

// statusLabel is the short per-row badge: the price ("Free", "$3.85",
// "$3.85 owned"), plus a note only when something needs attention.
// Installed games already carry the list's green marker.
func statusLabel(st rahub.GameState, c rahub.Console) string {
	// Short and never contradictory: one availability word, or the price.
	switch {
	case st.Status == rahub.StatusOutdated:
		return "update needed"
	case st.Status == rahub.StatusVersionGone && len(st.RAVersions) > 0:
		return "v" + st.RAVersions[0] + " gone"
	case demoGone(st):
		return "gone"
	case st.Status == rahub.StatusNotFound:
		return "not on itch"
	}
	var parts []string
	if label, paid, owned, known := priceOf(st); known {
		if paid && owned {
			label = "bought"
		}
		parts = append(parts, label)
	}
	switch st.Status {
	case rahub.StatusNoMatch:
		parts = append(parts, "no match")
	case rahub.StatusNoHashes:
		parts = append(parts, "no RA file")
	case rahub.StatusError:
		parts = append(parts, "retry")
	case rahub.StatusUnverified:
		parts = append(parts, "unverified")
	}
	if !c.Verifiable() {
		parts = append(parts, "can't check")
	}
	return strings.Join(parts, " · ")
}

// priceFields fills itchio.Game's IsFree/Price. An unknown price is
// IsFree=false with Price=-1 ("checking" on the detail screen).
func priceFields(st rahub.GameState) (isFree bool, price float64) {
	label, paid, _, known := priceOf(st)
	switch {
	case !known:
		return false, -1
	case !paid:
		return true, 0
	}
	if page, ok := itchPage(st); ok {
		if v, ok := toBRL(page.Price); ok {
			return false, v
		}
	}
	return false, priceValue(label)
}

// cacheAge is how long ago the hub listing was last fetched. A zero time
// (never fetched) reads as very old, which is the right answer: it means
// refresh.
func (h *hubContext) cacheAge() time.Duration {
	if h == nil || h.store == nil || h.store.FetchedAt.IsZero() {
		return 365 * 24 * time.Hour
	}
	return time.Since(h.store.FetchedAt)
}

// catalogGames converts the hub into the list's game records.
func (h *hubContext) catalogGames() []itchio.Game {
	games := h.store.GamesCopy()
	out := make([]itchio.Game, 0, len(games))
	for _, g := range games {
		c := g.Console()
		st := h.store.State(g.ID)
		if !h.store.Visible(g) {
			continue // hidden by the user (Settings > Hidden games / systems)
		}
		cover := g.BadgeURL
		if st.CoverURL != "" {
			cover = st.CoverURL
		}
		isFree, price := priceFields(st)
		author := st.ItchAuthor
		if author == "" && len(st.Candidates) > 0 {
			author = st.Candidates[0].Author
		}
		blurb := fmt.Sprintf("%s  ·  %d achievements", c.Name, g.Achievements)
		if genre := gameGenre(g); genre != "" {
			blurb = fmt.Sprintf("%s  ·  %s  ·  %d achievements", c.Name, genre, g.Achievements)
		}
		// The developer is on the game page, not in the list: the row line
		// (console, genre, achievements) needs the width.
		tags := append(rahub.TitleTags(g.Title), c.Name, fmt.Sprintf("%d achievements", g.Achievements))
		if st.Status != "" {
			tags = append(tags, string(st.Status))
		}
		out = append(out, itchio.Game{
			Title:    rahub.CleanTitle(g.Title),
			Author:   author,
			URL:      g.PageURL(),
			CoverURL: cover,
			IsFree:   isFree,
			Price:    price,
			Platform: c.Short,
			Tags:     tags,
			Blurb:    blurb,
		})
	}
	return out
}

// setFilterPlatforms makes the Filter screen's System row list the consoles
// that are actually in the hub, instead of the itch.io feed platforms.
func (h *hubContext) setFilterPlatforms() {
	type pair struct{ code, label string }
	seen := map[string]bool{}
	var list []pair
	for _, g := range h.store.GamesCopy() {
		c := g.Console()
		if seen[c.Short] || h.store.ConsoleHidden(g.ConsoleID) {
			continue
		}
		seen[c.Short] = true
		list = append(list, pair{c.Short, c.Name})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].label < list[j].label })
	codes := []string{""}
	labels := []string{"All systems"}
	for _, p := range list {
		codes = append(codes, p.code)
		labels = append(labels, p.label)
	}
	appui.FilterPlatforms = codes
	appui.FilterPlatformLabels = labels
}

// consoleLabel is the readable console name for a list platform code.
func (h *hubContext) consoleLabel(code string) string {
	for _, g := range h.store.GamesCopy() {
		if c := g.Console(); c.Short == code {
			return c.Name
		}
	}
	return code
}

// knownItchURL returns an itch.io page already found for a game, without
// searching. Used while scrolling, so browsing never fires searches.
func (h *hubContext) knownItchURL(raURL string) string {
	g, ok := h.gameFor(raURL)
	if !ok {
		return ""
	}
	st := h.store.State(g.ID)
	if st.ItchURL != "" {
		return st.ItchURL
	}
	if len(st.Candidates) > 0 {
		return st.Candidates[0].URL
	}
	return ""
}

// itchURLFor returns the itch.io page to read a description from,
// resolving it (searching) when not known yet.
func (h *hubContext) itchURLFor(ctx context.Context, raURL string) (string, error) {
	if v, ok := h.resolved.Load(raURL); ok {
		return v.(string), nil
	}
	g, ok := h.gameFor(raURL)
	if !ok {
		return "", fmt.Errorf("not a hub game")
	}
	st := h.store.State(g.ID)
	if st.ItchURL != "" {
		h.resolved.Store(raURL, st.ItchURL)
		return st.ItchURL, nil
	}
	cands, err := h.pipe.Resolve(ctx, g, false)
	if err != nil {
		return "", err
	}
	h.resolved.Store(raURL, cands[0].URL)
	return cands[0].URL, nil
}

// previewText is the description under the artwork on the main list: the
// itch.io description when known. The facts line above it already shows
// the console and status, so they are not repeated here.
func (h *hubContext) previewText(g rahub.HubGame, itchText string) string {
	if t := cleanItchText(itchText); t != "" {
		return t
	}
	st := h.store.State(g.ID)
	switch st.Status {
	case rahub.StatusNotFound:
		return "No itch.io page found for this game."
	case rahub.StatusPaid:
		return "Paid on itch.io - only downloaded if you bought it."
	}
	if len(st.Candidates) > 0 {
		return "itch.io: " + st.Candidates[0].URL
	}
	return fmt.Sprintf("%d achievements on RetroAchievements.", g.Achievements)
}

// describe builds the text shown for a hub game: its verification status
// first (that is what decides whether it can be installed), then the
// itch.io description when one could be fetched.
func (h *hubContext) describe(g rahub.HubGame, itchText string) string {
	// The status and the facts have their own places on the game page
	// (status card, fact rows); "About" is only the game's own text.
	if t := cleanItchText(itchText); t != "" {
		return t
	}
	return "No description on the game's page."
}

// statusCard is the game page's coloured status: the sentence, and its
// level (ok / ready / attention / unavailable).
func (h *hubContext) statusCard(g rahub.HubGame, st rahub.GameState) (string, int) {
	text := h.statusSentence(g, st)
	switch {
	case st.Status == rahub.StatusVerified && installedPathOK(st):
		return text, sdlui.StatusOK
	case demoGone(st) || st.Status == rahub.StatusNoHashes:
		return text, sdlui.StatusBad
	case st.Status == rahub.StatusUnverified, st.Status == rahub.StatusPaid, st.Status == rahub.StatusNoMatch,
		st.Status == rahub.StatusOutdated,
		st.Status == rahub.StatusNotFound, !g.Console().Verifiable():
		return text, sdlui.StatusWarn
	}
	if _, paid, owned, known := priceOf(st); known && paid && !owned {
		return text, sdlui.StatusWarn
	}
	return text, sdlui.StatusReady
}

// detailFacts are the label/value rows under the status card.
func (h *hubContext) detailFacts(g rahub.HubGame, st rahub.GameState) [][2]string {
	var f [][2]string
	f = append(f, [2]string{"Achievements", fmt.Sprintf("%d   (RA #%d)", g.Achievements, g.ID)})
	if p := st.Progress; p.Total > 0 || p.Unlocked > 0 {
		v := fmt.Sprintf("%d / %d unlocked", p.Unlocked, p.Total)
		if p.Award != "" {
			v += "   (" + strings.ToUpper(p.Award) + ")"
		}
		f = append(f, [2]string{"You", v})
	}
	if !g.Released.IsZero() {
		f = append(f, [2]string{"Released", g.Released.Format("2006-01-02")})
	}
	if !g.SetAdded.IsZero() {
		f = append(f, [2]string{"Set added", g.SetAdded.Format("2006-01-02")})
	}
	if !g.Updated.IsZero() {
		v := g.Updated.Format("2006-01-02")
		if isNewSet(g) {
			v += "   (NEW)"
		}
		f = append(f, [2]string{"Last updated", v})
	}
	if g.Players > 0 {
		f = append(f, [2]string{"Players", fmt.Sprintf("%s  (%s unlocks)", thousands(g.Players), thousands(g.Unlocks))})
	}
	if genre := gameGenre(g); genre != "" {
		f = append(f, [2]string{"Genre", genre})
	}
	if tags := rahub.TitleTags(g.Title); len(tags) > 0 {
		f = append(f, [2]string{"RA tags", strings.Join(tags, ", ")})
	}
	if label, paid, owned, known := priceOf(st); known {
		switch {
		case !paid:
			f = append(f, [2]string{"Price", "Free"})
		case owned:
			f = append(f, [2]string{"Price", label + "   (bought)"})
		default:
			f = append(f, [2]string{"Price", label + "   (not bought)"})
		}
	}
	raV := st.RAVersions
	if len(raV) == 0 {
		raV = rahub.VersionsIn(strings.Join(st.HashNames, " "))
	}
	if len(raV) > 0 || len(st.ItchVersions) > 0 {
		v := ""
		if len(raV) > 0 {
			v = "RA v" + strings.Join(raV, "/v")
		}
		if len(st.ItchVersions) > 0 {
			if v != "" {
				v += "   ·   "
			}
			v += "itch.io v" + strings.Join(st.ItchVersions, "/v")
		}
		f = append(f, [2]string{"Version", v})
	}
	if page, ok := itchPage(st); ok {
		if page.Author != "" {
			f = append(f, [2]string{"Developer", page.Author})
		}
		f = append(f, [2]string{"Page", strings.TrimPrefix(strings.TrimPrefix(page.URL, "https://"), "http://")})
	}
	if st.Status == rahub.StatusVerified && installedPathOK(st) {
		f = append(f, [2]string{"File", filepath.Base(st.InstalledPath)})
	}
	return f
}

// statusSentence is the single line that says what can be done with a game
// right now. Everything shown elsewhere agrees with it.
func (h *hubContext) statusSentence(g rahub.HubGame, st rahub.GameState) string {
	c := g.Console()
	switch {
	case st.Status == rahub.StatusOutdated && installedPathOK(st):
		return "UPDATE NEEDED: RetroAchievements no longer accepts your installed file (it moved to another " +
			"version). Achievements will not work with it. Press A to download the current version; it replaces the old file."
	case st.Status == rahub.StatusVerified && installedPathOK(st) && (st.Progress.Mastered() || st.Progress.Completed()):
		return fmt.Sprintf("MASTERED: you unlocked all %d achievements. Installed and verified.", st.Progress.Total)
	case st.Status == rahub.StatusVerified && installedPathOK(st):
		return "Installed and verified: achievements will work. Press A to check for an update."
	case st.Status == rahub.StatusUnverified && installedPathOK(st):
		return "Installed WITHOUT verification. Achievements may not work."
	case st.Status == rahub.StatusVersionGone:
		return fmt.Sprintf("Not available. RetroAchievements supports v%s, but itch.io now only has v%s. "+
			"Y hides the game; --hub-set-url pins the old version if you find it.",
			strings.Join(st.RAVersions, "/v"), strings.Join(st.ItchVersions, "/v"))
	case demoGone(st):
		return "Not available. This demo is no longer offered, so it cannot be downloaded. Y hides the game."
	case !c.Verifiable():
		return "This system's files cannot be checked here. A downloads it; SELECT installs without checking."
	case len(g.Hashes) == 0 && len(g.PatchHashes) == 0 && g.HashesKnown:
		return "RetroAchievements lists no file for this game yet, so nothing can be checked."
	case st.Status == rahub.StatusPaid:
		if label, _, _, ok := priceOf(st); ok {
			return "Paid game (" + label + "), not in your itch.io purchases, so it cannot be downloaded."
		}
		return "Paid game, not in your itch.io purchases, so it cannot be downloaded."
	case st.Status == rahub.StatusNotFound:
		return "Not found on itch.io. SELECT searches again; --hub-set-url pins a page."
	case st.Status == rahub.StatusNoMatch:
		return "Last try: no file matched RetroAchievements. SELECT searches again, SELECT after an error installs without checking."
	}
	if label, paid, owned, known := priceOf(st); known && paid && !owned {
		return "Paid game (" + label + "), not in your itch.io purchases, so it cannot be downloaded."
	}
	if rahub.IsDemo(g.Title) && st.DemoFree == 1 {
		return "Free demo. Press A to download it; the file is checked against RetroAchievements before it is installed."
	}
	return "Press A to download. The file is checked against RetroAchievements before it is installed."
}

func (h *hubContext) tryStart(id int) bool {
	h.busyMu.Lock()
	defer h.busyMu.Unlock()
	if h.busy[id] {
		return false
	}
	h.busy[id] = true
	return true
}

func (h *hubContext) done(id int) {
	h.busyMu.Lock()
	delete(h.busy, id)
	h.busyMu.Unlock()
}

// install runs the verified install for one game and records it in the
// inventory and gamelist.xml. It is safe to call from a worker goroutine.
func (h *hubContext) install(ctx context.Context, inv *inventory.Inventory, g rahub.HubGame, force bool,
	progress func(string)) (rahub.Result, error) {
	if !h.tryStart(g.ID) {
		return rahub.Result{}, fmt.Errorf("already installing")
	}
	defer h.done(g.ID)
	prev := h.store.State(g.ID)
	res, err := h.pipe.Install(ctx, g, force, progress)
	if err != nil {
		return res, err
	}
	// Update: the new verified file replaces the one installed before
	// (same place, same name when possible), instead of sitting next to it.
	if !res.Already && prev.InstalledPath != "" && prev.InstalledPath != res.Path && !g.Console().IsDisc() {
		if _, statErr := os.Stat(prev.InstalledPath); statErr == nil {
			os.Remove(prev.InstalledPath)
			if inv != nil {
				inv.RemoveFile(g.PageURL(), prev.InstalledPath)
			}
			sameDir := filepath.Dir(prev.InstalledPath) == filepath.Dir(res.Path)
			target := strings.TrimSuffix(prev.InstalledPath, filepath.Ext(prev.InstalledPath)) + filepath.Ext(res.Path)
			if sameDir && os.Rename(res.Path, target) == nil {
				res.Path = target
				h.store.Update(g.ID, func(s *rahub.GameState) { s.InstalledPath = target })
				_ = h.store.Save()
			}
		}
	}
	if !res.Already || (inv != nil && !inv.IsPresent(g.PageURL())) {
		st := h.store.State(g.ID)
		cover := st.CoverURL
		if cover == "" {
			cover = g.BadgeURL
		}
		recordInstall(inv, inventoryPath(), installRecord{
			GameURL: g.PageURL(), Title: rahub.CleanTitle(g.Title), Author: st.ItchAuthor,
			CoverURL: cover, IsFree: true, DestPath: res.Path,
			Upload: filepath.Base(res.Path), Desc: h.describe(g, ""),
		})
	}
	return res, nil
}

// moveInstalledFile moves an installed game into destDir and returns where
// it ended up.
//
// A file of the same name may already be there. Only when it holds the
// same bytes is the old copy a duplicate to drop; before, the old file was
// deleted on the name alone, and a different file that happened to share
// it — another game's release, an older build — was all that remained.
// A different file keeps its place and the moved one takes a free name.
func moveInstalledFile(old, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(old)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		target := filepath.Join(destDir, base)
		if i > 1 {
			target = filepath.Join(destDir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		if _, err := os.Stat(target); err != nil {
			return target, os.Rename(old, target)
		}
		if same, err := sameContent(old, target); err == nil && same {
			return target, os.Remove(old)
		}
		if i == 100 {
			return "", fmt.Errorf("no free name for %s in %s", base, destDir)
		}
	}
}

// sameContent reports whether two files hold the same bytes.
func sameContent(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if ia.Size() != ib.Size() {
		return false, nil
	}
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			return errB == io.EOF || errB == io.ErrUnexpectedEOF, nil
		}
		if errA != nil {
			return false, errA
		}
		if errB != nil {
			return false, errB
		}
	}
}

// moveIntoItchioFolders moves games an earlier version installed straight
// into /roms/<system>/ into /roms/<system>/itchio/, and fixes the hub state,
// the inventory and gamelist.xml to match.
func (h *hubContext) moveIntoItchioFolders(inv *inventory.Inventory) {
	moved := 0
	for _, g := range h.store.GamesCopy() {
		st := h.store.State(g.ID)
		if (st.Status != rahub.StatusVerified && st.Status != rahub.StatusUnverified) || !installedPathOK(st) ||
			g.Console().IsDisc() {
			continue
		}
		dest, err := destDirForConsole(g.Console())
		if err != nil || filepath.Clean(filepath.Dir(st.InstalledPath)) == filepath.Clean(dest.Dir) {
			continue // already where it belongs
		}
		old := st.InstalledPath
		target, err := moveInstalledFile(old, dest.Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not move %s: %v\n", old, err)
			continue
		}
		h.store.Update(g.ID, func(s *rahub.GameState) { s.InstalledPath = target })
		oldSystemDir := filepath.Dir(old)
		if filepath.Base(oldSystemDir) == itchioSubdir {
			oldSystemDir = filepath.Dir(oldSystemDir) // gamelist.xml sits in the system folder
		}
		if err := removeGamelistEntry(oldSystemDir, old); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "gamelist cleanup:", err)
		}
		if inv != nil {
			inv.RemoveFile(g.PageURL(), old)
		}
		cover := st.CoverURL
		if cover == "" {
			cover = g.BadgeURL
		}
		recordInstall(inv, inventoryPath(), installRecord{
			GameURL: g.PageURL(), Title: rahub.CleanTitle(g.Title), Author: st.ItchAuthor,
			CoverURL: cover, IsFree: true, DestPath: target, Upload: filepath.Base(target),
			Desc: h.describe(g, ""),
		})
		fmt.Fprintf(os.Stderr, "moved %s -> %s\n", old, target)
		moved++
	}
	if moved > 0 {
		_ = h.store.Save()
	}
}

// loadOwned fetches the itch.io account's purchases (CLI path; the UI gets
// them from its own background sign-in).
func (h *hubContext) loadOwned(client *itchio.Client, apiKey string) {
	if apiKey == "" {
		return
	}
	user, owned, err := client.ValidateAPIKey(apiKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "itch.io sign-in failed (paid games will be skipped):", err)
		return
	}
	h.setOwned(owned)
	fmt.Printf("itch.io: signed in as %s, %d purchase(s)\n", user, len(owned))
}

// canInstallUnverified reports failures after which the user may choose to
// install anyway: the hash did not match, or cannot be computed here.
func canInstallUnverified(err error) bool {
	return errors.Is(err, rahub.ErrNoMatch) || errors.Is(err, rahub.ErrNotVerifiable)
}

// installUnverified is install() without the hash check, on the user's
// explicit second press.
func (h *hubContext) installUnverified(ctx context.Context, inv *inventory.Inventory, g rahub.HubGame,
	progress func(string)) (rahub.Result, error) {
	if !h.tryStart(g.ID) {
		return rahub.Result{}, fmt.Errorf("already installing")
	}
	defer h.done(g.ID)
	res, err := h.pipe.InstallUnverified(ctx, g, progress)
	if err != nil {
		return res, err
	}
	st := h.store.State(g.ID)
	cover := st.CoverURL
	if cover == "" {
		cover = g.BadgeURL
	}
	recordInstall(inv, inventoryPath(), installRecord{
		GameURL: g.PageURL(), Title: rahub.CleanTitle(g.Title), Author: st.ItchAuthor,
		CoverURL: cover, IsFree: true, DestPath: res.Path,
		Upload: filepath.Base(res.Path), Desc: h.describe(g, ""),
	})
	return res, nil
}

// upgradeStates adapts state saved by older versions: disc systems that
// became verifiable are no longer "unsupported", and games not found by an
// older, narrower search get their RA names (hash file titles) re-read.
func (h *hubContext) upgradeStates() {
	changed := false
	for _, g := range h.store.GamesCopy() {
		st := h.store.State(g.ID)
		// Demo games scanned before, and pages whose price was never read
		// from the page itself: check once more.
		unpriced := len(st.Candidates) > 0 && st.Candidates[0].Price == "" && !st.Candidates[0].PriceChecked &&
			st.Status != rahub.StatusVerified
		if ((rahub.IsDemo(g.Title) && st.DemoFree == 0) || unpriced) && st.DescDone && !st.Hidden {
			h.store.Update(g.ID, func(s *rahub.GameState) { s.DescDone = false })
			changed = true
		}
		if st.Status == rahub.StatusUnsupported && g.Console().Verifiable() {
			h.store.Update(g.ID, func(s *rahub.GameState) { s.Status, s.Note = rahub.StatusNew, "" })
			changed = true
		}
		// After a search change, look again ONLY at the doubtful games (not
		// found, failed, or nothing verified) — not the whole hub.
		doubtful := st.Status == rahub.StatusNotFound || st.Status == rahub.StatusError || st.Status == rahub.StatusNoMatch ||
			st.Status == rahub.StatusDemoGone || (rahub.IsDemo(g.Title) && st.DemoFree == 2)
		if st.SearchVer < rahub.SearchVersion && doubtful && st.Override == "" {
			h.store.Update(g.ID, func(s *rahub.GameState) {
				s.Candidates, s.TriedUploads, s.HintsDone, s.BroadDone, s.DescDone = nil, nil, false, false, false
				s.ItchDesc, s.CoverURL, s.Note, s.DemoFree = "", "", "", 0
				if s.Status != rahub.StatusUnsupported {
					s.Status = rahub.StatusNew
				}
			})
			changed = true
		}
	}
	if changed {
		_ = h.store.Save()
	}
}

// friendlyError words a pipeline failure for the detail screen.
func friendlyError(err error) string {
	switch {
	case errors.Is(err, rahub.ErrNoRAKey):
		return "Set your RetroAchievements API key first (START > Settings, or --ra-login)."
	case errors.Is(err, rahub.ErrNotFound):
		return "No itch.io page matches this game. You can pin one with --hub-set-itch."
	case errors.Is(err, rahub.ErrNoMatch):
		return "Downloaded, but no file matches a RetroAchievements hash - NOT installed."
	case errors.Is(err, rahub.ErrNoDownloads):
		return "No file for this console on the itch.io page (browser-only, PC build, or no public download)."
	case errors.Is(err, rahub.ErrNoHashes):
		return "RetroAchievements lists no hash for this game, so nothing can be verified."
	case errors.Is(err, rahub.ErrVersionGone):
		return "The version with achievements is no longer offered: " + strings.TrimPrefix(err.Error(), rahub.ErrVersionGone.Error()+": ") + ". Y hides it."
	case errors.Is(err, rahub.ErrDemoGone):
		return "This demo is no longer available, so it cannot be downloaded. Y hides it."
	case errors.Is(err, rahub.ErrPaid):
		return "Paid game you have not bought on itch.io - not downloaded. (Bought it? Sign in with your itch.io API key.)"
	case errors.Is(err, rahub.ErrNotVerifiable):
		return "Disc-based system: this app cannot compute its RetroAchievements hash."
	}
	return "Failed: " + err.Error()
}

// pendingGames lists games a batch run should process: everything not yet
// verified (or verified but deleted). Without retry, games already known to
// have no page / no match are skipped so a re-run is quick.
func (h *hubContext) pendingGames(retry bool) []rahub.HubGame {
	var out []rahub.HubGame
	for _, g := range h.store.GamesCopy() {
		st := h.store.State(g.ID)
		if !h.store.Visible(g) {
			continue
		}
		switch st.Status {
		case rahub.StatusVerified, rahub.StatusUnverified:
			if installedPathOK(st) {
				continue
			}
		case rahub.StatusNotFound, rahub.StatusNoMatch, rahub.StatusNoHashes, rahub.StatusPaid, rahub.StatusDemoGone:
			if !retry {
				continue
			}
		case rahub.StatusUnsupported:
			continue
		}
		if !g.Console().Verifiable() {
			continue
		}
		out = append(out, g)
	}
	return out
}

// ---- command line ---------------------------------------------------------

// handleHubCLI implements the headless commands. Returns true when the
// program should exit without starting the UI.
//
//	--ra-login USER KEY      store RetroAchievements credentials (0600 file)
//	--hub-refresh            re-read the hub and RA hashes
//	--hub-sync [--retry] [--limit N] [--only ID]
//	                         resolve, download, verify and install
//	--hub-status             print the report
//	--hub-set-itch ID URL    pin the itch.io page for a game
func handleHubCLI(cfg *config, args []string) bool {
	if len(args) < 2 {
		return false
	}
	cmd := args[1]
	switch cmd {
	case "--ra-login", "--hub-refresh", "--hub-sync", "update", "--hub-status", "--hub-set-itch", "--hub-set-url", "--hub-find", "--hub-rescan":
	default:
		return false
	}
	logf := func(format string, a ...any) { fmt.Printf(format+"\n", a...) }

	if cmd == "--ra-login" {
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: --ra-login <RA username> <RA web API key>")
			fmt.Fprintln(os.Stderr, "the key is at https://retroachievements.org/settings (Keys)")
			return true
		}
		cfg.mu.Lock()
		cfg.RAUser, cfg.RAKey = strings.TrimSpace(args[2]), strings.TrimSpace(args[3])
		cfg.mu.Unlock()
		if err := cfg.save(); err != nil {
			fmt.Fprintln(os.Stderr, "could not save:", err)
			return true
		}
		fmt.Printf("saved RetroAchievements login for %s (key %s) to %s\n", args[2], maskedKey(args[3]), configPath())
		return true
	}

	if err := configureDarkOSPaths(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to configure ROM paths:", err)
	}
	client := itchio.NewClient()
	h, _ := newHubContext(cfg, client, logf)
	ctx, stop := signalContext()
	defer stop()

	creds := raCredentials(cfg)
	fmt.Printf("RetroAchievements hub %d  ·  user %s  ·  key %s\n", h.hubID, creds.User, maskedKey(creds.Key))

	// The hub is only re-read when asked (or on the very first run).
	needRefresh := cmd == "--hub-refresh" || cmd == "update" || len(h.store.GamesCopy()) == 0
	if cmd == "--hub-set-url" {
		cmd = "--hub-set-itch" // same thing: any page, itch.io or not
	}
	if cmd == "--hub-status" || cmd == "--hub-set-itch" || cmd == "--hub-find" || cmd == "--hub-rescan" {
		needRefresh = len(h.store.GamesCopy()) == 0
	}
	if needRefresh {
		fmt.Println("reading the hub...")
		if err := h.refresh(ctx, logf); err != nil {
			fmt.Fprintln(os.Stderr, "hub refresh:", err)
			if len(h.store.GamesCopy()) == 0 {
				return true
			}
		}
	}

	switch cmd {
	case "--hub-set-itch":
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: --hub-set-url <RA game id> <page URL>   (itch.io page, another site, or a direct file link)")
			return true
		}
		id, err := strconv.Atoi(args[2])
		if _, ok := h.store.Game(id); err != nil || !ok {
			fmt.Fprintln(os.Stderr, "not a game of this hub:", args[2])
			return true
		}
		h.pipe.SetOverride(id, args[3])
		// Picture and description from the pinned page, right away.
		text := h.pageInfo(ctx, client, id, args[3])
		h.saveDesc(id, text)
		st := h.store.State(id)
		fmt.Printf("game %d will use %s (run --hub-sync --only %d)\n", id, args[3], id)
		if st.CoverURL != "" {
			fmt.Printf("  cover: %s\n", st.CoverURL)
		}
		if text != "" {
			fmt.Printf("  description: %.120s...\n", text)
		}
		return true
	case "--hub-status":
		printHubReport(h)
		return true
	case "--hub-rescan":
		// --hub-rescan ID [ID...]  search these games again now
		// --hub-rescan             search again every game not found / failed
		h.loadOwned(client, cfg.key())
		var ids []int
		for _, a := range args[2:] {
			if id, err := strconv.Atoi(a); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			for _, g := range h.store.GamesCopy() {
				switch h.store.State(g.ID).Status {
				case rahub.StatusNotFound, rahub.StatusError, rahub.StatusNoMatch:
					ids = append(ids, g.ID)
				}
			}
		}
		fmt.Printf("searching %d game(s) again\n", len(ids))
		for _, id := range ids {
			g, ok := h.store.Game(id)
			if !ok {
				continue
			}
			fmt.Printf("%d  %s\n", id, rahub.CleanTitle(g.Title))
			if u, err := h.searchAgain(ctx, client, g); err != nil {
				fmt.Printf("    not found (%v)\n", err)
			} else {
				fmt.Printf("    -> %s\n", u)
			}
		}
		return true
	case "--hub-find":
		// Diagnostic: run every lookup for one game, loudly, and show what
		// each step (itch.io search, developer pages, web engines) returned.
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: --hub-find <RA game id>")
			return true
		}
		id, _ := strconv.Atoi(args[2])
		g, ok := h.store.Game(id)
		if !ok {
			fmt.Fprintln(os.Stderr, "not a game of this hub:", args[2])
			return true
		}
		h.loadOwned(client, cfg.key())
		h.store.Update(id, func(s *rahub.GameState) { s.HintsDone = false })
		fmt.Printf("%s  (%s)\n", g.Title, g.Console().Name)
		quick, qerr := h.pipe.Resolve(ctx, g, true)
		fmt.Printf("  developers: %v\n  other names: %v\n", h.store.State(id).DevHints, h.pipe.TitlesFor(g)[1:])
		fmt.Printf("  quick search: %d result(s) %v\n", len(quick), errString(qerr))
		for _, c := range quick {
			fmt.Printf("    %4d  %s  (%s) %s\n", c.Score, c.URL, c.Reason, c.Price)
		}
		broad, berr := h.pipe.ResolveBroad(ctx, g, nil)
		fmt.Printf("  wide search: %d result(s) %v\n", len(broad), errString(berr))
		for _, c := range broad {
			fmt.Printf("    %4d  %s  (%s via %s) %s\n", c.Score, c.URL, c.Reason, c.Source, c.Price)
		}
		return true
	case "--hub-refresh":
		printHubReport(h)
		return true
	}

	// --hub-sync / update
	retry, limit, only, unverified := false, 0, 0, false
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--retry":
			retry = true
		case "--limit":
			if i+1 < len(args) {
				limit, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--unverified":
			unverified = true
		case "--only":
			if i+1 < len(args) {
				only, _ = strconv.Atoi(args[i+1])
				retry = true
				i++
			}
		}
	}
	if !creds.Valid() {
		fmt.Fprintln(os.Stderr, "no RetroAchievements API key: set RA_KEY or run --ra-login USER KEY")
		return true
	}

	inv, err := inventory.Load(inventoryPath())
	if err != nil {
		inv, _ = inventory.Load("")
	}
	h.moveIntoItchioFolders(inv)
	h.loadOwned(client, cfg.key())
	todo := h.pendingGames(retry)
	if only != 0 {
		todo = nil
		if g, ok := h.store.Game(only); ok {
			todo = []rahub.HubGame{g}
		}
	}
	if limit > 0 && len(todo) > limit {
		todo = todo[:limit]
	}
	fmt.Printf("%d game(s) to process\n\n", len(todo))
	for i, g := range todo {
		if ctx.Err() != nil {
			fmt.Println("\ninterrupted - progress is saved, run again to continue")
			break
		}
		c := g.Console()
		fmt.Printf("[%03d/%03d] %s\n        System: %s\n        RA Game ID: %d\n        RA hashes: %d\n",
			i+1, len(todo), rahub.CleanTitle(g.Title), c.Name, g.ID, len(g.Hashes)+len(g.PatchHashes))
		var res rahub.Result
		var err error
		if unverified && only != 0 {
			res, err = h.installUnverified(ctx, inv, g, nil)
			if err == nil {
				fmt.Printf("        ! installed WITHOUT verification -> %s\n\n", res.Path)
				continue
			}
		} else {
			res, err = h.install(ctx, inv, g, retry, nil)
		}
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			fmt.Printf("        ✗ %s\n\n", friendlyError(err))
			continue
		}
		if res.Already {
			fmt.Printf("        ✓ already verified: %s\n\n", res.Path)
		} else {
			fmt.Printf("        ✓ VERIFIED -> %s\n\n", res.Path)
		}
	}
	printHubReport(h)
	return true
}

func printHubReport(h *hubContext) {
	counts := h.store.Counts()
	total := h.visibleCount()
	fmt.Printf("\nRetroAchievements Hub %d\n\n", h.hubID)
	fmt.Printf("Total de jogos:            %d\n", total)
	fmt.Printf("Verificados:               %d\n", counts[rahub.StatusVerified])
	fmt.Printf("Não encontrados no itch:   %d\n", counts[rahub.StatusNotFound])
	fmt.Printf("Sem hash correspondente:   %d\n", counts[rahub.StatusNoMatch])
	fmt.Printf("Pagos não comprados:       %d\n", counts[rahub.StatusPaid])
	fmt.Printf("Sem hash no RA:            %d\n", counts[rahub.StatusNoHashes])
	fmt.Printf("Sistema de disco (n/v):    %d\n", counts[rahub.StatusUnsupported])
	fmt.Printf("Erros (tentar de novo):    %d\n", counts[rahub.StatusError])
	fmt.Printf("Pendentes:                 %d\n", counts[rahub.StatusNew]+counts[rahub.StatusCandidate]+
		counts[rahub.StatusSearching]+counts[rahub.StatusVerifying])

	type row struct {
		name            string
		total, verified int
	}
	bySys := map[string]*row{}
	for _, g := range h.store.GamesCopy() {
		if !h.store.Visible(g) {
			continue
		}
		c := g.Console()
		r := bySys[c.Name]
		if r == nil {
			r = &row{name: c.Name}
			bySys[c.Name] = r
		}
		r.total++
		if h.store.State(g.ID).Status == rahub.StatusVerified {
			r.verified++
		}
	}
	names := make([]string, 0, len(bySys))
	for n := range bySys {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Println("\nSistemas:")
	for _, n := range names {
		r := bySys[n]
		fmt.Printf("  %-28s %3d / %3d\n", n+":", r.verified, r.total)
	}
	fmt.Println("\nEstado salvo em:", h.store.Path())
}

// ---- background itch.io scan ----------------------------------------------

// needsItchData reports whether the background scan still has work for a
// game: no itch.io lookup yet, or a page found but its description not read.
func needsItchData(st rahub.GameState) bool {
	if st.Hidden {
		return false
	}
	switch st.Status {
	case rahub.StatusNotFound:
		// Looked up by an older, narrower search: try the wider one.
		return st.SearchVer < rahub.SearchVersion
	case rahub.StatusSearching:
		return false
	case rahub.StatusError:
		// A failed lookup is retried, but not in a tight loop.
		return time.Since(st.UpdatedAt) > 30*time.Minute
	}
	// DescDone is set after every completed lookup (found or not), so a
	// game is never scanned twice unless a re-scan resets it.
	return !st.DescDone
}

// prioritize asks the scan to do this game next (the one under the cursor).
func (h *hubContext) prioritize(id int) {
	if h.want == nil {
		return
	}
	select {
	case h.want <- id:
	default:
	}
}

// itchProgress is "done/total" for the background scan.
func (h *hubContext) itchProgress() (done, total int) {
	for _, g := range h.store.GamesCopy() {
		if !h.store.Visible(g) {
			continue
		}
		total++
		if !needsItchData(h.store.State(g.ID)) {
			done++
		}
	}
	return done, total
}

func (h *hubContext) nextToScan() (rahub.HubGame, bool) {
	for {
		select {
		case id := <-h.want:
			if g, ok := h.store.Game(id); ok && h.store.Visible(g) && needsItchData(h.store.State(id)) {
				return g, true
			}
			continue
		default:
		}
		break
	}
	for _, g := range h.store.GamesCopy() {
		if h.store.Visible(g) && needsItchData(h.store.State(g.ID)) {
			return g, true
		}
	}
	return rahub.HubGame{}, false
}

// startItchScan looks up every hub game on itch.io in the background, one
// at a time, and caches page, cover and description in the hub state. The
// list fills in as it goes; the game under the cursor jumps the queue.
// Nothing is downloaded here. Once everything is cached the scan idles and
// only picks up games added by a hub refresh or a re-scan from Settings.
func (h *hubContext) startItchScan(client *itchio.Client, notify func()) {
	h.want = make(chan int, 16)
	go func() {
		failures := 0
		for {
			g, ok := h.nextToScan()
			if !ok {
				// All cached: wait for a prioritised game or new work.
				select {
				case id := <-h.want:
					h.prioritize(id) // put it back for nextToScan
				case <-time.After(20 * time.Second):
				}
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			err := h.scanOne(ctx, client, g)
			cancel()
			notify()
			if err != nil {
				failures++
				wait := time.Duration(failures) * 20 * time.Second
				if wait > 5*time.Minute {
					wait = 5 * time.Minute
				}
				fmt.Fprintf(os.Stderr, "itch.io scan: %s: %v (pausing %s)\n", rahub.CleanTitle(g.Title), err, wait)
				time.Sleep(wait)
				continue
			}
			failures = 0
			time.Sleep(time.Second) // stay polite with itch.io and RA
		}
	}()
}

// scanOne runs the quick lookup for a game and reads its itch.io page.
func (h *hubContext) scanOne(ctx context.Context, client *itchio.Client, g rahub.HubGame) error {
	cands, err := h.pipe.Resolve(ctx, g, false)
	if errors.Is(err, rahub.ErrNotFound) {
		// Nothing with title + developer: keep looking (developer pages,
		// address guesses, API and web search) before giving up.
		cands, err = h.pipe.ResolveBroad(ctx, g, nil)
		if errors.Is(err, rahub.ErrNotFound) {
			h.saveDesc(g.ID, "") // looked everywhere, nothing on itch.io
			return nil
		}
	}
	if err != nil {
		return err
	}
	h.saveDesc(g.ID, h.pageInfo(ctx, client, g.ID, cands[0].URL))
	h.pipe.RefreshPrice(ctx, g) // a price the search result did not show
	h.pipe.DemoProbe(ctx, g)    // demos: is the free demo file still offered?
	return nil
}

// saveDesc caches an itch.io description (capped: it is only shown in a
// panel, and the state file is rewritten often).
func (h *hubContext) saveDesc(id int, text string) {
	text = cleanItchText(text)
	if r := []rune(text); len(r) > 2500 {
		text = string(r[:2500]) + "…"
	}
	h.store.Update(id, func(s *rahub.GameState) { s.ItchDesc, s.DescDone = text, true })
	_ = h.store.Save()
}

// ---- list filters (RetroAchievements-based) --------------------------------

// hubShowFilter applies the filter screen's "Show" row.
func hubShowFilter(games []itchio.Game, mode string, inv *inventory.Inventory) []itchio.Game {
	if mode == "" || mode == "all" || activeHub == nil {
		return games
	}
	out := make([]itchio.Game, 0, len(games))
	for _, g := range games {
		id, ok := gameIDFromURL(g.URL)
		if !ok {
			continue
		}
		st := activeHub.store.State(id)
		hg, _ := activeHub.store.Game(id)
		installed := inv != nil && inv.IsPresent(g.URL)
		_, paid, owned, known := priceOf(st)
		keep := false
		switch mode {
		case "installed":
			keep = installed
		case "notinstalled":
			keep = !installed
		case "free":
			keep = known && !paid
		case "paid":
			keep = known && paid
		case "owned":
			keep = paid && owned
		case "verifiable":
			keep = hg.Console().Verifiable()
		case "notfound":
			keep = st.Status == rahub.StatusNotFound
		case "new":
			keep = isNewSet(hg)
		case "mastered":
			keep = st.Progress.Mastered() || st.Progress.Completed()
		case "notmastered":
			keep = !st.Progress.Mastered() && !st.Progress.Completed()
		case "played":
			keep = st.Progress.Unlocked > 0
		case "demos":
			keep = rahub.IsDemo(hg.Title)
		case "nodemos":
			keep = !rahub.IsDemo(hg.Title)
		}
		if keep {
			out = append(out, g)
		}
	}
	return out
}

// hubSort applies the filter screen's "Order" row.
func hubSort(games []itchio.Game, mode string) []itchio.Game {
	out := append([]itchio.Game(nil), games...)
	ach := func(g itchio.Game) int {
		if activeHub == nil {
			return 0
		}
		if id, ok := gameIDFromURL(g.URL); ok {
			hg, _ := activeHub.store.Game(id)
			return hg.Achievements
		}
		return 0
	}
	lower := func(g itchio.Game) string { return strings.ToLower(g.Title) }
	switch mode {
	case "az":
		sort.SliceStable(out, func(i, j int) bool { return lower(out[i]) < lower(out[j]) })
	case "za":
		sort.SliceStable(out, func(i, j int) bool { return lower(out[i]) > lower(out[j]) })
	case "most":
		sort.SliceStable(out, func(i, j int) bool { return ach(out[i]) > ach(out[j]) })
	case "fewest":
		sort.SliceStable(out, func(i, j int) bool { return ach(out[i]) < ach(out[j]) })
	case "free":
		sort.SliceStable(out, func(i, j int) bool { return out[i].IsFree && !out[j].IsFree })
	case "cheap":
		// Paid games by price, cheapest on top; free and unknown after.
		paid := func(g itchio.Game) bool { return !g.IsFree && g.Price > 0 }
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if paid(a) != paid(b) {
				return paid(a)
			}
			return paid(a) && a.Price < b.Price
		})
	case "new":
		// Newest achievement sets first; games without a date go last.
		added := func(g itchio.Game) time.Time {
			if activeHub == nil {
				return time.Time{}
			}
			if id, ok := gameIDFromURL(g.URL); ok {
				hg, _ := activeHub.store.Game(id)
				return newDate(hg)
			}
			return time.Time{}
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := added(out[i]), added(out[j])
			if a.IsZero() != b.IsZero() {
				return !a.IsZero()
			}
			return a.After(b)
		})
	case "players", "unlocks":
		stat := func(g itchio.Game) int {
			if activeHub == nil {
				return 0
			}
			if id, ok := gameIDFromURL(g.URL); ok {
				hg, _ := activeHub.store.Game(id)
				if mode == "players" {
					return hg.Players
				}
				return hg.Unlocks
			}
			return 0
		}
		sort.SliceStable(out, func(i, j int) bool { return stat(out[i]) > stat(out[j]) })
	}
	return out
}

// ---- wider itch.io lookups ---------------------------------------------------

// SearchMore: itch.io API search (with the itch.io key) for the first query,
// plus web search for all of them.
func (s itchSource) SearchMore(ctx context.Context, queries []string, devFirst bool, good func(rahub.Candidate) bool) ([]rahub.Candidate, error) {
	var out []rahub.Candidate
	var firstErr error
	if key := s.apiKey(); key != "" && len(queries) > 0 {
		// The API search wants the plain title: use the last "title only"
		// query when the first one carries the developer.
		q := queries[0]
		if devFirst && len(queries) > 1 {
			q = queries[1] // the title alone
		}
		if res, err := rahub.APISearchItch(ctx, s.client.HTTPClient(), key, q); err == nil {
			out = append(out, res...)
			for _, c := range res {
				if good(c) {
					return out, nil
				}
			}
		} else {
			firstErr = err
		}
	}
	if res, err := rahub.WebSearchItch(ctx, s.client.HTTPClient(), queries, devFirst, good); err == nil {
		out = append(out, res...)
	} else if firstErr == nil {
		firstErr = err
	}
	if len(out) > 0 {
		return out, nil
	}
	return nil, firstErr
}

func (s itchSource) Page(ctx context.Context, pageURL string) (rahub.Candidate, bool, error) {
	return rahub.FetchGamePage(ctx, s.client.HTTPClient(), pageURL)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return "(" + err.Error() + ")"
}

// resetForSearch forgets one game's itch.io lookup so it is searched again
// (installed files are left alone).
func (h *hubContext) resetForSearch(id int) {
	h.store.Update(id, func(s *rahub.GameState) {
		s.Candidates, s.TriedUploads, s.HintsDone, s.BroadDone, s.DescDone = nil, nil, false, false, false
		s.ItchDesc, s.Note, s.SearchVer, s.DemoFree = "", "", 0, 0
		if s.Status != rahub.StatusVerified && s.Status != rahub.StatusUnverified && s.Status != rahub.StatusUnsupported {
			s.Status = rahub.StatusNew
		}
	})
	h.resolved.Delete(fmt.Sprintf("%s/game/%d", strings.TrimRight(rahub.RABase, "/"), id))
}

// rescanDoubtful re-queues only the games that ended not found, failed, or
// without a verified file.
func (h *hubContext) rescanDoubtful() int {
	n := 0
	for _, g := range h.store.GamesCopy() {
		switch h.store.State(g.ID).Status {
		case rahub.StatusNotFound, rahub.StatusError, rahub.StatusNoMatch:
			h.resetForSearch(g.ID)
			n++
		}
	}
	_ = h.store.Save()
	return n
}

// searchAgain runs the full lookup (quick, then wide) for one game now and
// reads its page. Used by SELECT on the game page and --hub-rescan.
func (h *hubContext) searchAgain(ctx context.Context, client *itchio.Client, g rahub.HubGame) (string, error) {
	h.resetForSearch(g.ID)
	cands, err := h.pipe.Resolve(ctx, g, true)
	if errors.Is(err, rahub.ErrNotFound) {
		cands, err = h.pipe.ResolveBroad(ctx, g, nil)
	}
	if err != nil {
		h.saveDesc(g.ID, "")
		return "", err
	}
	h.saveDesc(g.ID, h.pageInfo(ctx, client, g.ID, cands[0].URL))
	h.pipe.DemoProbe(ctx, g)
	return cands[0].URL, nil
}

// pageInfo returns the description for a game's page and, for pages
// outside itch.io (a developer's own site), also takes the page's picture
// (og:image) as the game's cover. itch.io pages go through the itch.io
// client.
func (h *hubContext) pageInfo(ctx context.Context, client *itchio.Client, id int, pageURL string) string {
	if !rahub.IsItchURL(pageURL) {
		c, ok, err := rahub.FetchGamePage(ctx, client.HTTPClient(), pageURL)
		if err != nil || !ok {
			return ""
		}
		if c.CoverURL != "" {
			h.store.Update(id, func(s *rahub.GameState) {
				s.CoverURL = c.CoverURL
				if s.ItchTitle == "" {
					s.ItchTitle = c.Title
				}
			})
			_ = h.store.Save()
		}
		return c.Text
	}
	if h.store.State(id).CoverURL == "" {
		if c, ok, err := rahub.FetchGamePage(ctx, client.HTTPClient(), pageURL); err == nil && ok && c.CoverURL != "" {
			h.store.Update(id, func(s *rahub.GameState) { s.CoverURL = c.CoverURL })
		}
	}
	if d, err := client.FetchGameDetail(pageURL); err == nil && d != nil {
		return stripHTML(d.Description)
	}
	return ""
}

// ---- hidden games ------------------------------------------------------------

// setHidden hides a game from the list (or brings it back).
func (h *hubContext) setHidden(id int, hidden bool) {
	h.store.Update(id, func(s *rahub.GameState) { s.Hidden = hidden })
	_ = h.store.Save()
}

// hiddenGames lists the hidden games, by title.
func (h *hubContext) hiddenGames() []rahub.HubGame {
	var out []rahub.HubGame
	for _, g := range h.store.GamesCopy() {
		if h.store.State(g.ID).Hidden {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return rahub.CleanTitle(out[i].Title) < rahub.CleanTitle(out[j].Title) })
	return out
}

// visibleCount is the number of hub games shown in the list.
func (h *hubContext) visibleCount() int {
	n := 0
	for _, g := range h.store.GamesCopy() {
		if h.store.Visible(g) {
			n++
		}
	}
	return n
}

// ---- hidden systems --------------------------------------------------------

// hubConsole is one console present in the hub, for Settings > Hidden systems.
type hubConsole struct {
	ID    int
	Name  string
	Short string // the list's platform code
	Games int
}

// hubConsoles lists every console in the hub (hidden or not), by name.
func (h *hubContext) hubConsoles() []hubConsole {
	byID := map[int]*hubConsole{}
	for _, g := range h.store.GamesCopy() {
		c := byID[g.ConsoleID]
		if c == nil {
			c = &hubConsole{ID: g.ConsoleID, Name: g.Console().Name, Short: g.Console().Short}
			byID[g.ConsoleID] = c
		}
		c.Games++
	}
	out := make([]hubConsole, 0, len(byID))
	for _, c := range byID {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// setConsoleHidden hides or shows a whole console and refreshes the filter.
func (h *hubContext) setConsoleHidden(id int, hidden bool) {
	h.store.SetConsoleHidden(id, hidden)
	_ = h.store.Save()
	h.setFilterPlatforms()
}

// itchBoilerplate is text itch.io puts on pages that is not about the game.
var itchBoilerplate = []string{
	"100% of donations go to support the itch.io platform.",
	"of donations go to support the itch.io platform.",
	"Support the developer by paying what you want.",
	"More information",
}

// cleanItchText removes itch.io's page boilerplate and leftovers like "()"
// from a description.
func cleanItchText(text string) string {
	for _, b := range itchBoilerplate {
		text = strings.ReplaceAll(text, b, " ")
	}
	text = strings.NewReplacer("()", " ", "[]", " ", "\"\"", " ").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

// activeHubOwned is ownedLookup through the global hub (for the list badge).
func activeHubOwned(pageURL string) (string, bool) {
	if activeHub == nil {
		return "", false
	}
	return activeHub.ownedLookup(pageURL)
}

// fullOwned reports whether the paid full release of a demo game is in
// the user's itch.io purchases.
func (h *hubContext) fullOwned(id int) bool {
	st := h.store.State(id)
	if st.FullURL == "" {
		return false
	}
	_, ok := h.ownedLookup(st.FullURL)
	return ok
}

// installFull installs the purchased full release of a demo game.
func (h *hubContext) installFull(ctx context.Context, inv *inventory.Inventory, g rahub.HubGame,
	progress func(string)) (rahub.Result, error) {
	if !h.tryStart(g.ID) {
		return rahub.Result{}, fmt.Errorf("already installing")
	}
	defer h.done(g.ID)
	res, err := h.pipe.InstallFull(ctx, g, progress)
	if err != nil {
		return res, err
	}
	st := h.store.State(g.ID)
	cover := st.CoverURL
	if cover == "" {
		cover = g.BadgeURL
	}
	recordInstall(inv, inventoryPath(), installRecord{
		GameURL: g.PageURL(), Title: rahub.CleanTitle(g.Title) + " (Full game)", Author: st.ItchAuthor,
		CoverURL: cover, IsFree: false, DestPath: res.Path, Upload: filepath.Base(res.Path),
		Desc: h.describe(g, ""),
	})
	return res, nil
}

// demoGone: nothing with RetroAchievements support can be downloaded any
// more — a DEMO entry whose demo is gone, or a game whose supported
// version was replaced by a newer one. Shown in red, no download.
func demoGone(st rahub.GameState) bool {
	if st.Status == rahub.StatusVerified || st.Status == rahub.StatusUnverified {
		return false
	}
	if st.DemoFree == 1 && st.Status == rahub.StatusDemoGone {
		return false // the latest check found the demo: that wins
	}
	return st.Status == rahub.StatusDemoGone || st.DemoFree == 2 || st.Status == rahub.StatusVersionGone
}

// longStatus is the status for the preview panel and the page header:
// the badge's words spelled out.
func longStatus(g rahub.HubGame, st rahub.GameState) string {
	switch {
	case st.Progress.Mastered() || st.Progress.Completed():
		return "MASTERED"
	case st.Status == rahub.StatusOutdated:
		return "installed, UPDATE NEEDED"
	case st.Status == rahub.StatusVersionGone && len(st.RAVersions) > 0:
		return "v" + st.RAVersions[0] + " no longer available"
	case demoGone(st):
		return "demo no longer available"
	case st.Status == rahub.StatusVerified && installedPathOK(st):
		return "installed and verified"
	}
	if l := statusLabel(st, g.Console()); l != "" {
		return l
	}
	return "price: checking..."
}

// thousands writes 12345 as "12.345".
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
}

// startStatsScan reads, in the background, how popular each hub game is on
// RetroAchievements (players and unlocks) for the "Most played" and "Most
// unlocks" orders. One request per game, gently; refreshed weekly.
func (h *hubContext) startStatsScan(notify func()) {
	go func() {
		time.Sleep(30 * time.Second) // let the itch.io scan and the UI start first
		for {
			if !h.ra.Creds.Valid() {
				time.Sleep(time.Minute)
				continue
			}
			did := 0
			for _, g := range h.store.GamesCopy() {
				// Weekly, or right away for games read before genres were kept
				// (Genre "" = never read; "-" = RA has none).
				if !h.store.Visible(g) || (time.Since(g.StatsAt) < 7*24*time.Hour && g.Genre != "" && !g.Updated.IsZero()) {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				players, unlocks, genre, setAdded, released, updated, err := h.ra.FetchGameStats(ctx, g.ID)
				cancel()
				if err != nil {
					time.Sleep(20 * time.Second)
					continue
				}
				if cur, ok := h.store.Game(g.ID); ok {
					cur.Players, cur.Unlocks, cur.StatsAt = players, unlocks, time.Now()
					if !setAdded.IsZero() {
						cur.SetAdded = setAdded
					}
					if !released.IsZero() {
						cur.Released = released
					}
					if !updated.IsZero() {
						cur.Updated = updated
					}
					cur.Genre = genre
					if genre == "" {
						cur.Genre = "-" // RA lists no genre: do not ask again this week
					}
					h.store.UpdateGame(cur)
				}
				did++
				if did%20 == 0 {
					_ = h.store.Save()
					notify()
				}
				time.Sleep(1500 * time.Millisecond)
			}
			if did > 0 {
				_ = h.store.Save()
				notify()
			}
			time.Sleep(10 * time.Minute)
		}
	}()
}

// gameGenre is RA's genre for a game, "" when unknown.
func gameGenre(g rahub.HubGame) string {
	if g.Genre == "-" {
		return ""
	}
	return g.Genre
}

// genreParts splits RA genres like "Action, Platformer" / "RPG / Adventure".
func genreParts(genre string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(genre, func(r rune) bool { return r == ',' || r == '/' || r == ';' || r == '|' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// setFilterGenres lists the genres found in the hub for the Filter screen.
func (h *hubContext) setFilterGenres() {
	count := map[string]int{}
	for _, g := range h.store.GamesCopy() {
		if !h.store.Visible(g) {
			continue
		}
		for _, p := range genreParts(gameGenre(g)) {
			count[p]++
		}
	}
	names := make([]string, 0, len(count))
	for n := range count {
		names = append(names, n)
	}
	sort.Strings(names)
	values, labels := []string{""}, []string{"All genres"}
	for _, n := range names {
		values = append(values, n)
		labels = append(labels, fmt.Sprintf("%s (%d)", n, count[n]))
	}
	appui.FilterGenres, appui.FilterGenreLabels = values, labels
}

// hubGenreFilter keeps the games of one genre.
func hubGenreFilter(games []itchio.Game, genre string) []itchio.Game {
	if genre == "" || activeHub == nil {
		return games
	}
	out := make([]itchio.Game, 0, len(games))
	for _, g := range games {
		id, ok := gameIDFromURL(g.URL)
		if !ok {
			continue
		}
		hg, _ := activeHub.store.Game(id)
		for _, p := range genreParts(gameGenre(hg)) {
			if strings.EqualFold(p, genre) {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// newSetDays is how long a game counts as "new" on RetroAchievements.
const newSetDays = 60

// newDate is the date "Recently updated" and the NEW mark go by: when the
// game's achievement set was last touched on RetroAchievements (the hub
// page's "Last Updated"), falling back to when the set was finished.
func newDate(g rahub.HubGame) time.Time {
	if !g.Updated.IsZero() {
		return g.Updated
	}
	if !g.SetAdded.IsZero() {
		return g.SetAdded
	}
	return g.Released
}

// isNewSet reports a game released on RetroAchievements recently.
func isNewSet(g rahub.HubGame) bool {
	d := newDate(g)
	return !d.IsZero() && time.Since(d) < newSetDays*24*time.Hour
}

// startProgressScan keeps your RetroAchievements progress (mastered games,
// unlocks) up to date in the background.
func (h *hubContext) startProgressScan(notify func()) {
	go func() {
		for {
			if h.ra.Creds.Valid() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				progress, err := h.ra.FetchUserProgress(ctx)
				cancel()
				if err != nil {
					fmt.Fprintln(os.Stderr, "RetroAchievements progress:", err)
				} else if n := h.store.SetProgress(progress); n > 0 {
					_ = h.store.Save()
					notify()
				}
			}
			time.Sleep(15 * time.Minute)
		}
	}()
}

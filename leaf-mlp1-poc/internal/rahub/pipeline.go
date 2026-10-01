package rahub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Upload is one downloadable file on an itch.io page.
type Upload struct {
	Name string // display name as itch.io shows it (often no extension)
	ID   string
	URL  string // opaque handle for ItchSource.Download
	// Version is the build version itch.io shows for the file, if any.
	Version string
	// KeyID is set for uploads reached through a purchase (download key);
	// Download then uses the authenticated route.
	KeyID string
	// MaxBytes makes Download give up on a bigger file (a 1 GB PC build is
	// never a cartridge ROM). 0 = no limit.
	MaxBytes int64
}

// maxDownload is the largest file worth downloading for a console.
func maxDownload(c Console) int64 {
	if c.IsDisc() {
		return 2 << 30
	}
	return 96 << 20 // cartridge ROMs are KB..MB; zips with extras stay well below
}

// Destination is where verified files for a console go.
type Destination struct {
	Dir  string   // e.g. /roms/atari2600/itchio
	Exts []string // extensions EmulationStation accepts for the system (may be empty)
}

// ItchSource is the slice of itch.io the pipeline needs. The app wires it to
// internal/itchio; tests use a fake.
type ItchSource interface {
	Search(ctx context.Context, query string) ([]Candidate, error)
	// Profile lists the games on https://<slug>.itch.io/.
	// movedTo names the account an empty developer page points to.
	Profile(ctx context.Context, slug string) (games []Candidate, movedTo string, err error)
	// Uploads lists a FREE page's files (the public download route).
	Uploads(ctx context.Context, pageURL string) ([]Upload, error)
	// SearchMore is the wider search: itch.io's API search (when an itch.io
	// key is set) and a web search restricted to itch.io.
	// Queries are tried in order; engines stop early once good() accepts a
	// result.
	// devFirst says queries[0] is "title + developer".
	SearchMore(ctx context.Context, queries []string, devFirst bool, good func(Candidate) bool) ([]Candidate, error)
	// Page reads one itch.io game page (title, cover, price); ok is false
	// when there is no game at that address.
	Page(ctx context.Context, pageURL string) (Candidate, bool, error)
	// OwnedUploads lists the files a purchase grants (itch.io API key).
	OwnedUploads(ctx context.Context, gameID string) ([]Upload, error)
	Download(ctx context.Context, pageURL string, up Upload, dest string) error
}

// Pipeline turns hub games into verified installs.
type Pipeline struct {
	Store   *Store
	RA      *RAClient
	Itch    ItchSource
	WorkDir string // scratch space for downloads; never the ROM library
	// DestDir returns the ROM folder for a console (from es_systems.cfg).
	DestDir func(Console) (Destination, error)
	// Owned reports whether the signed-in itch.io account bought the game
	// at pageURL, and its numeric game ID. nil = no itch.io account.
	// A PAID page is only ever downloaded when this says it is owned.
	Owned func(pageURL string) (gameID string, ok bool)
	// Catalog is an optional list of known itch.io games (the app's old
	// tag-feed cache) searched before hitting itch.io's search page.
	Catalog []Candidate

	MaxCandidates int           // itch.io pages considered per game
	MaxDownloads  int           // files downloaded per game per run
	MinScore      int           // candidate score needed to download
	SearchDelay   time.Duration // pause between itch.io searches
	Logf          func(format string, args ...any)
}

// NewPipeline sets the default limits.
func NewPipeline(store *Store, ra *RAClient, itch ItchSource, workDir string, dest func(Console) (Destination, error)) *Pipeline {
	return &Pipeline{
		Store: store, RA: ra, Itch: itch, WorkDir: workDir, DestDir: dest,
		MaxCandidates: 3, MaxDownloads: 6, MinScore: 30,
		SearchDelay: 1200 * time.Millisecond,
		Logf:        func(string, ...any) {},
	}
}

// Result describes a successful install.
type Result struct {
	Path      string
	MD5       string
	Candidate Candidate
	Already   bool   // the file was already installed and still verifies
	Note      string // e.g. "hash is RA's patched version"
}

// Sentinel outcomes, so callers can word messages without parsing strings.
var (
	ErrNoHashes    = errors.New("RetroAchievements lists no hash for this game")
	ErrNotFound    = errors.New("no matching itch.io page found")
	ErrNoMatch     = errors.New("downloaded files do not match any RetroAchievements hash")
	ErrNoRAKey     = errors.New("RetroAchievements API key not set (needed to get hashes)")
	ErrNoDownloads = errors.New("the itch.io page offers no downloadable file for this console")
	ErrPaid        = errors.New("only a paid itch.io page was found and it is not in your purchases")
	// ErrDemoGone: RetroAchievements' set is for a DEMO, and the page now
	// sells the full game instead (whose file RA does not recognise).
	ErrDemoGone = errors.New("RetroAchievements has the demo, but itch.io now only offers the full (paid) game")
	// ErrVersionGone: RA's hashes are for a version itch.io no longer offers.
	ErrVersionGone = errors.New("the version RetroAchievements supports is no longer offered")
	// ErrInstallFailed: a file matched RetroAchievements but could not be
	// copied into the ROM folder. The download was right; the card was not.
	ErrInstallFailed = errors.New("install")
)

// Resolve is the QUICK lookup, used when a game is opened or browsed: one
// itch.io search for "title + developer" (developer names come from RA's
// game data and from RA's hash file names, e.g. "(Nova32)"), and only if
// that finds nothing, one search for the title alone. The hash check at
// download time catches a wrong pick; only then does Install fall back to
// ResolveBroad. The cached answer is reused unless force is set.
func (p *Pipeline) Resolve(ctx context.Context, g HubGame, force bool) ([]Candidate, error) {
	st := p.Store.State(g.ID)
	if !force && len(st.Candidates) > 0 && st.Override == "" {
		return st.Candidates, nil
	}
	if !force && st.Status == StatusNotFound && st.Override == "" {
		return nil, ErrNotFound
	}
	console := g.Console()
	hints := p.hintsFor(ctx, g)
	titles := p.titlesFor(g)

	var pool []Candidate
	if st.Override != "" {
		pool = append(pool, Candidate{URL: st.Override, Title: CleanTitle(g.Title), Source: "override"})
	}
	for _, c := range p.Catalog {
		if len(RankForTitles(titles, console, hints, []Candidate{c}, 80, 1)) > 0 {
			c.Source = "catalog"
			pool = append(pool, c)
		}
	}
	clean := CleanTitle(g.Title)
	queries := []string{clean}
	if len(hints) > 0 {
		queries = []string{clean + " " + hints[0], clean}
	}
	var searchErr error
	if st.Override == "" {
		for i, q := range queries {
			if len(RankForTitles(titles, console, hints, pool, p.MinScore, 1)) > 0 {
				break // found something plausible: stop here, the hash decides
			}
			if i > 0 {
				if err := sleepCtx(ctx, p.SearchDelay/2); err != nil {
					return nil, err
				}
			}
			res, err := p.Itch.Search(ctx, q)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				searchErr = err
				p.Logf("        search %q failed: %v", q, err)
				continue
			}
			pool = append(pool, res...)
		}
	}
	ranked := RankForTitles(titles, console, hints, pool, p.MinScore, p.MaxCandidates)
	if len(ranked) == 0 && st.Override == "" {
		// Nothing confirmed yet. Search cells carry little text, so open the
		// most promising pages (right title, developer/console unknown) and
		// judge them again on their full text.
		sort.SliceStable(pool, func(i, j int) bool {
			return looseBest(titles, console, hints, pool[i]) > looseBest(titles, console, hints, pool[j])
		})
		opened := 0
		for _, c := range append([]Candidate(nil), pool...) {
			if opened >= 3 || looseBest(titles, console, hints, c) < 50 {
				break
			}
			opened++
			if err := sleepCtx(ctx, p.SearchDelay/2); err != nil {
				return nil, err
			}
			if page, ok, err := p.Itch.Page(ctx, c.URL); err == nil && ok {
				page.Source = c.Source
				if page.Price == "" {
					page.Price = c.Price
				}
				if c.CoverURL != "" {
					page.CoverURL = c.CoverURL
				}
				pool = append(pool, page)
			}
		}
		ranked = RankForTitles(titles, console, hints, pool, p.MinScore, p.MaxCandidates)
	}
	return p.storeCandidates(g, ranked, searchErr, false)
}

// ResolveBroad is the WIDE lookup, run when the quick lookup found nothing
// or its pages gave no verified file. Cheapest and most precise first:
//
//  1. direct guesses: https://<developer>.itch.io/<title-slug>
//  2. the developer's own itch.io page(s)
//  3. itch.io API search (with the itch.io key) and more itch.io searches
//  4. a web search (site:itch.io "title"), which also finds pages itch.io's
//     own search hides from anonymous users, such as adult-flagged games
//
// It stops as soon as a page with the exact title shows up. Pages in exclude
// (already tried) are left out.
func (p *Pipeline) ResolveBroad(ctx context.Context, g HubGame, exclude map[string]bool) ([]Candidate, error) {
	console := g.Console()
	hints := p.hintsFor(ctx, g)
	titles := p.titlesFor(g)
	var pool []Candidate
	usable := func(c Candidate) bool { return !exclude[NormPageURL(c.URL)] }
	found := func() bool {
		for _, c := range RankForTitles(titles, console, hints, pool, 100, 0) {
			if usable(c) {
				return true
			}
		}
		return false
	}
	requests := 0
	pace := func() error {
		requests++
		if requests == 1 {
			return nil
		}
		return sleepCtx(ctx, p.SearchDelay)
	}
	var searchErr error
	clean := CleanTitle(g.Title)

	// 1. Direct guesses.
	guesses := 0
	for _, h := range hints {
		for _, user := range ProfileSlugs(h) {
			for _, slug := range TitleSlugs(g.Title) {
				if found() || guesses >= 6 {
					break
				}
				guesses++
				u := "https://" + user + ".itch.io/" + slug
				if exclude[NormPageURL(u)] {
					continue
				}
				if err := pace(); err != nil {
					return nil, err
				}
				if c, ok, err := p.Itch.Page(ctx, u); err == nil && ok {
					p.Logf("        found by address: %s", u)
					c.Source = "guess"
					pool = append(pool, c)
				}
			}
		}
	}
	// 2. Developer pages.
	triedSlug := map[string]bool{}
	for _, h := range append([]string(nil), hints...) {
		for _, slug := range ProfileSlugs(h) {
			if found() || triedSlug[slug] {
				continue
			}
			triedSlug[slug] = true
			if err := pace(); err != nil {
				return nil, err
			}
			res, movedTo, err := p.Itch.Profile(ctx, slug)
			if err == nil && movedTo != "" && !triedSlug[movedTo] {
				// The developer renamed their account: follow it, and treat
				// the new name as the same developer from now on.
				p.Logf("        developer page %s.itch.io moved to %s.itch.io", slug, movedTo)
				triedSlug[movedTo] = true
				hints = append(hints, movedTo)
				p.Store.Update(g.ID, func(s *GameState) { s.DevHints = appendUnique(s.DevHints, movedTo) })
				if err := pace(); err != nil {
					return nil, err
				}
				res, _, err = p.Itch.Profile(ctx, movedTo)
				slug = movedTo
			}
			if err == nil {
				p.Logf("        developer page %s.itch.io: %d game(s)", slug, len(res))
				pool = append(pool, res...)
				// This developer's games under other names: open the ones
				// for this console and judge them by their page text.
				if !found() {
					sort.SliceStable(res, func(i, j int) bool {
						return MentionsConsole(res[i].Title+" "+res[i].Text, console) &&
							!MentionsConsole(res[j].Title+" "+res[j].Text, console)
					})
					opened := 0
					for _, c := range res {
						if opened >= 8 || !usable(c) || !IsSameDeveloper(hints, c) {
							continue
						}
						if len(RankForTitles(titles, console, hints, []Candidate{c}, p.MinScore, 1)) > 0 {
							continue // already qualifies by title
						}
						opened++
						if err := pace(); err != nil {
							return nil, err
						}
						page, ok, err := p.Itch.Page(ctx, c.URL)
						if err != nil || !ok {
							continue
						}
						page.Source = "profile"
						if c.CoverURL != "" {
							page.CoverURL = c.CoverURL // the grid's thumbnail
						}
						if r := RankForTitles(titles, console, hints, []Candidate{page}, -1<<30, 1); len(r) > 0 {
							p.Logf("        page %s: %q score %d (%s)", page.URL, page.Title, r[0].Score, r[0].Reason)
						}
						pool = append(pool, page)
					}
				}
			}
		}
	}
	// 3. More itch.io searches.
	var queries []string
	for i, h := range hints {
		if i > 0 { // hints[0] was the quick search
			queries = append(queries, clean+" "+h)
		}
	}
	if main := MainTitle(clean); main != clean {
		queries = append(queries, main)
	}
	// Other names: RA's hash file names ("Casanova" for "Mega Casanova")
	// and the title without a marketing word.
	for _, t := range titles[1:] {
		queries = append(queries, t)
	}
	queries = append(queries, clean+" "+console.Name)
	for _, q := range queries {
		if found() {
			break
		}
		if err := pace(); err != nil {
			return nil, err
		}
		res, err := p.Itch.Search(ctx, q)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			searchErr = err
			continue
		}
		pool = append(pool, res...)
	}
	// 4. API + web search. Queries as a person would type them: title +
	// developer first, then the title, then other names. Every link a
	// search engine returns is opened right away (real title, text, price)
	// and judged on that; the search moves on to the next engine (Google,
	// ...) until a page actually qualifies.
	if !found() {
		var queries []string
		if len(hints) > 0 {
			queries = append(queries, clean+" "+hints[0])
			if len(hints) > 1 {
				// Several developers ("TLT; Tomahome"): all names together,
				// the way a person would search.
				queries = append(queries, clean+" "+hints[0]+" "+hints[1])
			}
		}
		queries = append(queries, clean)
		for _, t := range titles[1:] {
			queries = append(queries, CleanTitle(t))
		}
		judged := map[string]bool{}
		probes := 0
		score := func(c Candidate) (int, string) {
			if r := RankForTitles(titles, console, hints, []Candidate{c}, -1<<30, 1); len(r) > 0 {
				return r[0].Score, r[0].Reason
			}
			return 0, ""
		}
		accept := func(c Candidate) bool {
			key := NormPageURL(c.URL)
			if !usable(c) || judged[key] {
				return false
			}
			judged[key] = true
			if c.Source == "web" {
				if probes >= 10 {
					return false
				}
				probes++
				if err := pace(); err != nil {
					return false
				}
				page, ok, err := p.Itch.Page(ctx, c.URL)
				if err != nil {
					p.Logf("        page %s: %v", c.URL, err)
					return false
				}
				if !ok {
					p.Logf("        page %s: not a game page", c.URL)
					return false
				}
				page.Source, page.WebRank, page.DevQuery = "web", c.WebRank, c.DevQuery
				c = page
			}
			s, why := score(c)
			p.Logf("        page %s: %q score %d (%s)", c.URL, c.Title, s, why)
			if s >= p.MinScore {
				pool = append(pool, c)
				return true
			}
			return false
		}
		if err := pace(); err != nil {
			return nil, err
		}
		if _, err := p.Itch.SearchMore(ctx, queries, len(hints) > 0, accept); err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	// Pages that point to a sibling page of the same account (a browser
	// edition linking to its ROM page): read those siblings too.
	followed := 0
	for _, c := range RankForTitles(titles, console, hints, pool, -1<<30, 0) {
		if followed >= 3 || c.Score < 0 {
			break
		}
		for _, l := range c.Links {
			if followed >= 3 || !usable(Candidate{URL: l}) {
				continue
			}
			if len(RankForTitles(titles, console, hints, []Candidate{{URL: l}}, -1<<30, 1)) == 0 {
				continue
			}
			already := false
			for _, x := range pool {
				if NormPageURL(x.URL) == NormPageURL(l) && x.Source != "web" {
					already = true
				}
			}
			if already {
				continue
			}
			followed++
			if err := pace(); err != nil {
				return nil, err
			}
			if page, ok, err := p.Itch.Page(ctx, l); err == nil && ok {
				page.Source = "linked"
				if r := RankForTitles(titles, console, hints, []Candidate{page}, -1<<30, 1); len(r) > 0 {
					p.Logf("        linked page %s: %q score %d (%s)", page.URL, page.Title, r[0].Score, r[0].Reason)
				}
				pool = append(pool, page)
			}
		}
	}

	var fresh []Candidate
	for _, c := range RankForTitles(titles, console, hints, pool, p.MinScore, 0) {
		if usable(c) {
			fresh = append(fresh, c)
		}
		if len(fresh) == p.MaxCandidates {
			break
		}
	}
	return p.storeCandidates(g, fresh, searchErr, true)
}

// hintsFor returns (and caches) the developer names for a game.
func (p *Pipeline) hintsFor(ctx context.Context, g HubGame) []string {
	st := p.Store.State(g.ID)
	hints := st.DevHints
	if !st.HintsDone {
		var alts []string
		hints, alts = p.developerHints(ctx, g)
		p.Store.Update(g.ID, func(s *GameState) { s.DevHints, s.AltTitles, s.HintsDone = hints, alts, true })
	}
	if len(hints) > 4 {
		hints = hints[:4]
	}
	return hints
}

// storeCandidates records a lookup's outcome. broad lookups ADD to the
// candidates already known instead of replacing them.
func (p *Pipeline) storeCandidates(g HubGame, ranked []Candidate, searchErr error, broad bool) ([]Candidate, error) {
	if len(ranked) == 0 {
		st := p.Store.State(g.ID)
		if searchErr != nil {
			// Could not search: that is not "not found", try again later.
			p.Store.Update(g.ID, func(s *GameState) { s.Status, s.Note = StatusError, searchErr.Error() })
			_ = p.Store.Save()
			return nil, searchErr
		}
		p.Store.Update(g.ID, func(s *GameState) {
			if broad {
				s.BroadDone, s.SearchVer = true, SearchVersion
			}
			if len(st.Candidates) == 0 && s.Status != StatusVerified {
				s.Status, s.Note = StatusNotFound, "no itch.io page with a matching title"
			}
		})
		_ = p.Store.Save()
		return nil, ErrNotFound
	}
	p.Store.Update(g.ID, func(s *GameState) {
		if broad {
			s.BroadDone, s.SearchVer = true, SearchVersion
			s.Candidates = append(s.Candidates, ranked...)
		} else {
			s.Candidates = ranked
		}
		if s.Status != StatusVerified {
			s.Status, s.Note = StatusCandidate, ""
		}
		if s.CoverURL == "" {
			for _, c := range ranked {
				if c.CoverURL != "" {
					s.CoverURL = c.CoverURL
					break
				}
			}
		}
	})
	_ = p.Store.Save()
	return ranked, nil
}

// developerHints collects developer names for a game from RetroAchievements:
// the Developer field, names in the hash file names, then the Publisher.
func (p *Pipeline) developerHints(ctx context.Context, g HubGame) (hints []string, altTitles []string) {
	if p.RA == nil || !p.RA.Creds.Valid() {
		return nil, nil
	}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if len(compact(v)) < 3 || notADeveloper[compact(v)] {
			return
		}
		for _, h := range hints {
			if compact(h) == compact(v) {
				return
			}
		}
		hints = append(hints, v)
	}
	dev, pub := p.RA.FetchGameDeveloper(ctx, g.ID)
	for _, d := range splitNames(dev) {
		add(d)
	}
	if entries, err := p.RA.FetchGameHashes(ctx, g.ID); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}
		for _, h := range DeveloperHintsFromHashNames(names) {
			add(h)
		}
		altTitles = TitlesFromHashNames(names)
		p.Store.Update(g.ID, func(s *GameState) { s.HashNames = names })
	}
	for _, d := range splitNames(pub) {
		add(d)
	}
	return hints, altTitles
}

// titlesFor lists the names a game may go by on itch.io: its RA title
// first, then the titles in RA's hash file names, then the title without a
// marketing word ("Mega Casanova" → "Casanova").
// TitlesFor is titlesFor for callers outside the package (diagnostics).
func (p *Pipeline) TitlesFor(g HubGame) []string { return p.titlesFor(g) }

func (p *Pipeline) titlesFor(g HubGame) []string {
	st := p.Store.State(g.ID)
	titles := []string{g.Title}
	seen := map[string]bool{NormTitle(g.Title): true}
	add := func(t string) {
		if k := NormTitle(t); len(k) >= 3 && !seen[k] {
			seen[k] = true
			titles = append(titles, t)
		}
	}
	for _, t := range st.AltTitles {
		add(t)
	}
	for _, t := range ShorterTitles(CleanTitle(g.Title)) {
		add(t)
	}
	return titles
}

// ensureHashes makes sure the game carries RA's hash list.
func (p *Pipeline) ensureHashes(ctx context.Context, g HubGame) (HubGame, error) {
	if g.HashesKnown && (len(g.Hashes) > 0 || len(g.PatchHashes) > 0) {
		return g, nil
	}
	if p.RA == nil || !p.RA.Creds.Valid() {
		return g, ErrNoRAKey
	}
	entries, err := p.RA.FetchGameHashes(ctx, g.ID)
	if err != nil {
		return g, err
	}
	applyHashEntries(&g, entries)
	p.Store.UpdateGame(g)
	if len(g.Hashes) == 0 && len(g.PatchHashes) == 0 {
		return g, ErrNoHashes
	}
	return g, nil
}

// Install runs the whole chain for one game. progress receives short
// human-readable steps (may be nil). force re-downloads uploads that were
// already tried without a match.
func (p *Pipeline) Install(ctx context.Context, g HubGame, force bool, progress func(string)) (Result, error) {
	say := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		p.Logf("        %s", msg)
		if progress != nil {
			progress(msg)
		}
	}
	fail := func(status Status, err error) (Result, error) {
		p.Store.Update(g.ID, func(s *GameState) { s.Status, s.Note = status, err.Error() })
		_ = p.Store.Save()
		return Result{}, err
	}

	console := g.Console()
	if !console.Verifiable() {
		return fail(StatusUnsupported, ErrNotVerifiable)
	}
	var err error
	g, err = p.ensureHashes(ctx, g)
	if err != nil {
		if errors.Is(err, ErrNoHashes) {
			return fail(StatusNoHashes, err)
		}
		if errors.Is(err, ErrNoRAKey) {
			return Result{}, err // configuration problem, not a game state
		}
		return fail(StatusError, err)
	}

	// Already installed and still valid? Nothing to download.
	st := p.Store.State(g.ID)
	if (st.Status == StatusVerified || st.Status == StatusOutdated) && st.InstalledPath != "" {
		if h, err := HashFile(console, st.InstalledPath); err == nil && g.HasHash(h) {
			return Result{Path: st.InstalledPath, MD5: h, Already: true}, nil
		}
	}

	// RA's file names (they carry the versions RA supports).
	p.ensureHashNames(ctx, g)

	// Check the destination before spending any network request on it.
	dest, err := p.DestDir(console)
	if err != nil {
		return fail(StatusError, fmt.Errorf("no ROM folder for %s: %w", console.Name, err))
	}
	if err := os.MkdirAll(p.WorkDir, 0o755); err != nil {
		return fail(StatusError, err)
	}
	job, err := os.MkdirTemp(p.WorkDir, "job-")
	if err != nil {
		return fail(StatusError, err)
	}
	defer os.RemoveAll(job)

	tried := map[string]bool{}
	for _, t := range st.TriedUploads {
		tried[t] = true
	}
	seenPages := map[string]bool{}
	titles := p.titlesFor(g)
	seenVersions := map[string]bool{} // versions itch.io offered (file info and names)
	downloads, pages := 0, 0
	sawUploads := false
	paidSkipped := 0
	var lastErr error

	// tryAll works through candidate pages until a file verifies.
	tryAll := func(cands []Candidate) (Result, bool, error) {
		for _, cand := range cands {
			if ctx.Err() != nil {
				return Result{}, false, ctx.Err()
			}
			if seenPages[NormPageURL(cand.URL)] {
				continue
			}
			seenPages[NormPageURL(cand.URL)] = true
			pages++
			say("Candidate: %s", cand.URL)
			gameID, owned := "", false
			if p.Owned != nil {
				gameID, owned = p.Owned(cand.URL)
			}
			var ups []Upload
			var err error
			switch {
			case owned:
				say("  in your itch.io purchases")
				ups, err = p.Itch.OwnedUploads(ctx, gameID)
			case IsPaidPrice(cand.Price) && IsDemo(g.Title):
				// A paid page can still carry a free demo file; only free
				// files are listed by the public route.
				say("  paid (%s): looking only for a free demo file", cand.Price)
				ups, err = p.Itch.Uploads(ctx, cand.URL)
				if err != nil || len(ups) == 0 {
					paidSkipped++
					say("  no free demo file on this page")
					continue
				}
			case IsPaidPrice(cand.Price):
				// Never touch a paid page the account has not bought.
				paidSkipped++
				say("  paid (%s) and not in your itch.io purchases - skipped", cand.Price)
				continue
			default:
				ups, err = p.Itch.Uploads(ctx, cand.URL)
				if err != nil && looksPaid(err) {
					// No price shown and no public download: usually a
					// browser-only page, not a paid one.
					say("  no public download on this page - skipped")
					err = nil
					ups = nil
				}
			}
			if err != nil {
				lastErr = err
				say("  cannot read page: %v", err)
				continue
			}
			ups = PreferDemo(PreferNamed(RankUploads(console, ups), p.Store.State(g.ID).HashNames, titles), IsDemo(g.Title))
			if len(ups) > 0 {
				sawUploads = true
			}
			for _, up := range ups {
				for _, v := range append(VersionsIn(up.Name), VersionsIn("v"+up.Version)...) {
					seenVersions[v] = true
				}
				key := cand.URL + "#" + up.ID + "|" + up.Name
				if tried[key] && !force {
					continue
				}
				if downloads >= p.MaxDownloads {
					break
				}
				downloads++
				say("Downloading %s...", up.Name)
				dl := filepath.Join(job, fmt.Sprintf("dl-%d", downloads))
				up.MaxBytes = maxDownload(console)
				if err := p.Itch.Download(ctx, cand.URL, up, dl); err != nil {
					if ctx.Err() != nil {
						return Result{}, false, ctx.Err()
					}
					lastErr = err
					say("  download failed: %v", err)
					continue // transient: do not mark as tried
				}
				say("Checking the hash...")
				res, ok, err := p.verify(g, console, dl, up, job, dest, seenVersions)
				if err != nil && !ok && errors.Is(err, ErrInstallFailed) {
					// The file verified; only copying it into the ROM
					// folder failed (card full, read-only, ...). It is the
					// right file, so it must not be marked as tried —
					// that hid it behind "all files already checked" on
					// every later attempt. Stop and say why.
					say("  %v", err)
					return Result{}, false, err
				}
				if err != nil && !ok {
					lastErr = err
					say("  %v", err)
				}
				if ok {
					res.Candidate = cand
					p.finish(ctx, g, cand, res)
					say("VERIFIED  MD5 %s", res.MD5)
					return res, true, nil
				}
				tried[key] = true
				p.Store.Update(g.ID, func(s *GameState) { s.TriedUploads = appendUnique(s.TriedUploads, key) })
				_ = p.Store.Save()
				_ = os.RemoveAll(dl)
			}
			if downloads >= p.MaxDownloads {
				break
			}
		}
		return Result{}, false, nil
	}

	// 1. Quick: title + developer.
	say("Searching itch.io...")
	cands, err := p.Resolve(ctx, g, force)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Result{}, err
	}
	if IsDemo(g.Title) && p.Store.State(g.ID).DemoFree == 2 && !force {
		return fail(StatusDemoGone, ErrDemoGone)
	}
	if len(cands) > 0 && cands[0].Price == "" {
		p.RefreshPrice(ctx, g)
		if st := p.Store.State(g.ID); len(st.Candidates) > 0 {
			cands[0].Price = st.Candidates[0].Price
		}
	}
	// The best match IS the game: if it is paid and not bought, stop here.
	// Trying the other, weaker matches would mean downloading something
	// that is not this game's official release.
	// (Demos are the exception: a paid page may still offer the demo as a
	// free file, which is looked for below.)
	if len(cands) > 0 && IsPaidPrice(cands[0].Price) && !IsDemo(g.Title) {
		owned := false
		if p.Owned != nil {
			_, owned = p.Owned(cands[0].URL)
		}
		if !owned {
			say("Paid on itch.io (%s) and not in your purchases - not downloading", cands[0].Price)
			if IsDemo(g.Title) {
				return fail(StatusDemoGone, ErrDemoGone)
			}
			return fail(StatusPaid, ErrPaid)
		}
	}
	if res, ok, err := tryAll(cands); ok || err != nil {
		return res, err
	}
	// 2. Nothing verified: search more widely, once (again with force).
	if st := p.Store.State(g.ID); (force || !st.BroadDone) && downloads < p.MaxDownloads && st.Override == "" {
		say("No verified file yet - searching more widely...")
		more, err := p.ResolveBroad(ctx, g, seenPages)
		if err != nil && !errors.Is(err, ErrNotFound) {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			lastErr = err
		}
		if res, ok, err := tryAll(more); ok || err != nil {
			return res, err
		}
	}

	// Remember the versions on both sides (shown on the game page).
	raV := VersionsIn(strings.Join(p.Store.State(g.ID).HashNames, " "))
	for _, v := range p.Store.State(g.ID).ItchVersions {
		if sawUploads {
			seenVersions[v] = true // learnt from an earlier download
		}
	}
	// RA names a version, but nothing tells which version itch.io has now
	// (the files were checked before versions were recorded): look at them
	// once more.
	if len(raV) > 0 && len(seenVersions) == 0 && downloads == 0 && sawUploads && !force {
		say("Checking which version itch.io offers...")
		return p.Install(ctx, g, true, progress)
	}
	var offered []string
	for v := range seenVersions {
		offered = append(offered, v)
	}
	sort.Strings(offered)
	if len(raV) > 0 || len(offered) > 0 {
		p.Store.Update(g.ID, func(s *GameState) { s.RAVersions, s.ItchVersions = raV, offered })
	}
	// RA's version is gone: RA names versions in its hash files (v1.0.6)
	// and none of them is among what itch.io offers now (v1.0.7). The
	// versions itch.io shows for its files are known even without
	// downloading them again.
	if len(raV) > 0 && len(offered) > 0 && sawUploads {
		overlap := false
		for _, v := range raV {
			if seenVersions[v] {
				overlap = true
			}
		}
		if !overlap {
			return fail(StatusVersionGone, fmt.Errorf("%w: RetroAchievements supports v%s, itch.io now has v%s",
				ErrVersionGone, strings.Join(raV, "/v"), strings.Join(offered, "/v")))
		}
	}
	// A demo whose page now sells the full game and no free file exists
	// anywhere: the demo is gone.
	if IsDemo(g.Title) && paidSkipped > 0 && !sawUploads {
		return fail(StatusDemoGone, ErrDemoGone)
	}
	switch {
	case pages == 0 && len(p.Store.State(g.ID).Candidates) == 0:
		return fail(StatusNotFound, ErrNotFound)
	case downloads == 0 && !sawUploads && paidSkipped > 0 && lastErr == nil:
		return fail(StatusPaid, ErrPaid)
	case downloads == 0 && !sawUploads:
		if lastErr != nil {
			return fail(StatusError, lastErr)
		}
		return fail(StatusNoMatch, ErrNoDownloads)
	case downloads == 0:
		// Everything was already tried in an earlier run.
		return fail(StatusNoMatch, fmt.Errorf("%w (all files already checked; use retry to force)", ErrNoMatch))
	}
	if lastErr != nil && errors.Is(lastErr, ErrNotVerifiable) {
		return fail(StatusNoMatch, fmt.Errorf("%w (%v)", ErrNoMatch, lastErr))
	}
	if lastErr != nil && errors.Is(lastErr, ErrRAR) {
		return fail(StatusNoMatch, fmt.Errorf("%w; a RAR archive could not be opened", ErrNoMatch))
	}
	return fail(StatusNoMatch, ErrNoMatch)
}

// verify hashes every file the download contains; on a match it installs
// the file into destDir and returns ok=true.
func (p *Pipeline) verify(g HubGame, c Console, download string, up Upload, job string, dest Destination,
	seenVersions map[string]bool) (Result, bool, error) {
	destDir := dest.Dir
	if c.Method == HashArcade {
		// Arcade hashes are of the set's file name, so the upload's own
		// name is what identifies it.
		if h, _ := HashBytes(c, nil, up.Name); g.HasHash(h) {
			name := filepath.Base(up.Name)
			if !strings.HasSuffix(strings.ToLower(name), ".zip") {
				name += ".zip"
			}
			dest, err := placeFile(download, destDir, name)
			if err != nil {
				return Result{}, false, fmt.Errorf("%w: %w", ErrInstallFailed, err)
			}
			return Result{Path: dest, MD5: h}, true, nil
		}
		return Result{}, false, nil
	}

	files, err := Unpack(download, job)
	if err != nil {
		return Result{}, false, err
	}
	for _, f := range files {
		for _, v := range VersionsIn(filepath.Base(f)) {
			seenVersions[v] = true
		}
	}
	if c.IsDisc() {
		return p.verifyDisc(g, c, files, dest)
	}
	// Hash the files that look like this console's ROMs first; then the
	// rest, because homebrew is often shipped with an odd extension.
	sort.SliceStable(files, func(i, j int) bool { return c.HasExt(files[i]) && !c.HasExt(files[j]) })
	for _, f := range files {
		if skipForHash(f) {
			continue
		}
		h, err := HashFile(c, f)
		if err != nil {
			continue
		}
		if !g.HasHash(h) {
			continue
		}
		name := InstallName(g, c, f, dest.Exts)
		dest, err := placeVerified(c, f, destDir, name, h)
		if err != nil {
			return Result{}, false, fmt.Errorf("%w: %w", ErrInstallFailed, err)
		}
		return Result{Path: dest, MD5: h}, true, nil
	}
	return Result{}, false, nil
}

// finish records a verified install in the store.
func (p *Pipeline) finish(ctx context.Context, g HubGame, cand Candidate, res Result) {
	note := ""
	if url, ok := g.PatchHashes[res.MD5]; ok {
		note = url
	} else if p.RA != nil && p.RA.Creds.Valid() && len(g.PatchHashes) == 0 {
		// PatchUrl only comes from the per-game endpoint; check the one
		// hash that matters now that we have it.
		if entries, err := p.RA.FetchGameHashes(ctx, g.ID); err == nil {
			for _, e := range entries {
				if strings.EqualFold(e.MD5, res.MD5) && e.PatchURL != nil && *e.PatchURL != "" {
					note = *e.PatchURL
				}
			}
		}
	}
	p.Store.Update(g.ID, func(s *GameState) {
		s.Status = StatusVerified
		s.ItchURL, s.ItchTitle, s.ItchAuthor = cand.URL, cand.Title, cand.Author
		if cand.CoverURL != "" {
			s.CoverURL = cand.CoverURL
		}
		s.MD5, s.InstalledPath, s.NeedsPatch = res.MD5, res.Path, note
		s.Note = ""
	})
	_ = p.Store.Save()
}

// SetOverride pins an itch.io page for a game (used when search picks wrong).
func (p *Pipeline) SetOverride(id int, pageURL string) {
	p.Store.Update(id, func(s *GameState) {
		s.Override = strings.TrimSpace(pageURL)
		s.Candidates, s.TriedUploads = nil, nil
		if s.Status != StatusVerified {
			s.Status = StatusNew
		}
	})
	_ = p.Store.Save()
}

// ---- helpers ----------------------------------------------------------

var pcMarkers = []string{".exe", ".msi", ".dmg", ".apk", ".app", ".deb", ".rpm", ".appimage", ".x86_64", ".love",
	"windows", "win64", "win32", "macos", "osx", "android", "linux64", "linux-x64", "setup",
	"linux", "64-bit", "32-bit", "x64", "x86", "mac os", "source code", "sourcecode", "github",
	"soundtrack", "artbook", "wallpaper", "diorama", "papercraft", "printable", "poster",
	"coloring", "colouring", ".pdf", "press kit", "presskit",
	"for pc", "(pc)", "packed emulator", "(cia)", ".cia", "for 3ds", "for switch", ".nsp", "for android"}

var docExts = map[string]bool{".txt": true, ".md": true, ".pdf": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".html": true, ".htm": true, ".nfo": true, ".url": true, ".json": true, ".xml": true,
	".cfg": true, ".ini": true, ".doc": true, ".docx": true, ".rtf": true, ".mp3": true, ".ogg": true,
	".wav": true, ".webp": true, ".bmp": true, ".sav": true, ".srm": true, ".ips": true, ".bps": true, ".ups": true}

func skipForHash(path string) bool {
	return docExts[strings.ToLower(filepath.Ext(path))]
}

// RankUploads drops PC/Android builds and orders the rest: files with this
// console's extension, then archives, then names mentioning the console,
// then anything without a telling extension.
func RankUploads(c Console, ups []Upload) []Upload {
	type ranked struct {
		u    Upload
		rank int
	}
	var keep []ranked
	archive := map[string]bool{".zip": true, ".7z": true, ".gz": true, ".tgz": true, ".tar": true}
	for _, u := range ups {
		low := strings.ToLower(u.Name)
		isPC := false
		for _, m := range pcMarkers {
			if strings.Contains(low, m) {
				isPC = true
				break
			}
		}
		if isPC && !c.HasExt(low) {
			continue
		}
		rank := 4
		ext := filepath.Ext(low)
		switch {
		case c.HasExt(low):
			rank = 0
		case archive[ext] || strings.HasSuffix(low, ".tar.gz"):
			rank = 1
			if mentions(low, c) {
				rank = 0
			}
		case mentions(low, c):
			rank = 2
		case ext == "" || len(ext) > 6 || strings.Contains(ext, " "):
			rank = 3
		case docExts[ext] || ext == ".rar":
			continue
		}
		keep = append(keep, ranked{u, rank})
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].rank < keep[j].rank })
	out := make([]Upload, len(keep))
	for i, k := range keep {
		out[i] = k.u
	}
	return out
}

func mentions(low string, c Console) bool {
	words := append([]string{strings.ToLower(c.Short), strings.ToLower(c.Name)}, c.Systems...)
	for _, w := range words {
		if len(w) >= 2 && strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// InstallName is the file name used in the ROM folder: the clean game
// title plus an extension the console's emulators accept.
//
// esExts are the extensions EmulationStation lists for the system. When
// known they win: a file ES does not list would never show in the menu.
func InstallName(g HubGame, c Console, matched string, esExts []string) string {
	ext := strings.ToLower(filepath.Ext(matched))
	inES := func(e string) bool {
		for _, x := range esExts {
			if strings.EqualFold(x, e) {
				return true
			}
		}
		return false
	}
	switch {
	case len(esExts) > 0 && inES(ext) && (c.HasExt(matched) || len(c.Exts) == 0):
		// the file's own extension is fine
	case len(esExts) > 0:
		picked := ""
		for _, e := range c.Exts {
			if inES(e) {
				picked = e
				break
			}
		}
		if picked == "" && inES(ext) {
			picked = ext
		}
		if picked == "" {
			picked = strings.ToLower(esExts[0])
		}
		ext = picked
	case !c.HasExt(matched) && len(c.Exts) > 0:
		ext = c.Exts[0]
	}

	const strip = `/\:?*"<>|`
	var b strings.Builder
	for _, r := range CleanTitle(g.Title) {
		if !strings.ContainsRune(strip, r) {
			b.WriteRune(r)
		}
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(matched), filepath.Ext(matched))
	}
	return name + ext
}

// placeVerified copies a verified file into the library. If a file with the
// same name exists and verifies the same, it is reused; if it is something
// else, the new file gets a distinct name instead of overwriting it.
func placeVerified(c Console, src, destDir, name, md5 string) (string, error) {
	target := filepath.Join(destDir, name)
	if _, err := os.Stat(target); err == nil {
		if h, err := HashFile(c, target); err == nil && h == md5 {
			return target, nil
		}
		ext := filepath.Ext(name)
		target = filepath.Join(destDir, strings.TrimSuffix(name, ext)+" ("+md5[:6]+")"+ext)
	}
	return placeFile(src, destDir, filepath.Base(target))
}

// placeFile copies src to destDir/name via a temporary name in the same
// folder, so a half-copied ROM never appears in the menu.
func placeFile(src, destDir, name string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(destDir, name)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := target + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return target, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// IsPaidPrice reports whether an itch.io price label means the game costs
// money ("$2.99", "R$ 5,00", "€1"). Empty, "Free" and "$0" are free.
func IsPaidPrice(price string) bool {
	digits := ""
	for _, r := range price {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}
	return strings.Trim(digits, "0") != ""
}

// looksPaid recognises the public download route's answer for pages that
// need a purchase or a login.
func looksPaid(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "paid") || strings.Contains(msg, "require login") ||
		strings.Contains(msg, "empty url") || strings.Contains(msg, "purchase")
}

// NormPageURL is the comparison form of an itch.io page URL.
func NormPageURL(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return strings.TrimRight(u, "/")
}

// notADeveloper are values RA puts in Developer/Publisher (or hash names)
// that name a store or a category, not a person: searching itch.io for
// "itch.io" found the store's own account.
var notADeveloper = map[string]bool{
	"itchio": true, "itch": true, "homebrew": true, "aftermarket": true, "unlicensed": true,
	"unl": true, "independent": true, "indie": true, "selfpublished": true, "self": true,
	"na": true, "none": true, "unknown": true, "various": true, "public domain": true,
	"publicdomain": true, "freeware": true, "retroachievements": true,
}

// ---- disc images ------------------------------------------------------------

// discRank orders the files of a disc download: cue sheets, then .iso,
// then raw .bin; everything else is not a disc image this app can hash.
func discRank(path string) int {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue":
		return 0
	case ".iso", ".cso":
		return 1
	case ".bin", ".img":
		return 2
	}
	return 9
}

// verifyDisc hashes the disc images in a download and installs the one that
// matches — a .cue together with every track file it lists.
func (p *Pipeline) verifyDisc(g HubGame, c Console, files []string, dest Destination) (Result, bool, error) {
	sort.SliceStable(files, func(i, j int) bool { return discRank(files[i]) < discRank(files[j]) })
	compressed := ""
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".chd", ".pbp":
			compressed = filepath.Ext(f)
		}
		if discRank(f) > 2 {
			continue
		}
		h, err := HashFile(c, f)
		if err != nil || !g.HasHash(h) {
			continue
		}
		var path string
		if strings.EqualFold(filepath.Ext(f), ".cue") {
			path, err = placeCueSet(f, dest.Dir)
		} else {
			path, err = placeVerified(c, f, dest.Dir, InstallName(g, c, f, dest.Exts), h)
		}
		if err != nil {
			return Result{}, false, fmt.Errorf("%w: %w", ErrInstallFailed, err)
		}
		return Result{Path: path, MD5: h}, true, nil
	}
	if compressed != "" {
		return Result{}, false, fmt.Errorf("%w: the release is %s", ErrNotVerifiable, compressed)
	}
	return Result{}, false, nil
}

// placeCueSet copies a cue sheet and the track files it lists into destDir,
// keeping their names (the cue refers to them by name).
func placeCueSet(cue, destDir string) (string, error) {
	tracks, err := CueFiles(cue)
	if err != nil {
		return "", err
	}
	base := filepath.Dir(cue)
	for _, t := range tracks {
		rel, err := filepath.Rel(base, t)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("cue refers outside its folder: %s", t)
		}
		if _, err := placeFile(t, filepath.Join(destDir, filepath.Dir(rel)), filepath.Base(rel)); err != nil {
			return "", err
		}
	}
	return placeFile(cue, destDir, filepath.Base(cue))
}

// mainFile picks what to install from a download when no hash decides:
// for discs a cue sheet, else an image; for cartridges the largest file with
// one of the console's extensions.
func mainFile(c Console, files []string) string {
	if c.IsDisc() {
		best, bestRank := "", 99
		for _, f := range files {
			r := discRank(f)
			switch strings.ToLower(filepath.Ext(f)) {
			case ".chd", ".pbp", ".cso":
				r = 1
			}
			if r < bestRank && (r <= 2 || c.HasExt(f)) {
				best, bestRank = f, r
			}
		}
		return best
	}
	best, bestSize := "", int64(-1)
	for _, f := range files {
		if !c.HasExt(f) {
			continue
		}
		if info, err := os.Stat(f); err == nil && info.Size() > bestSize {
			best, bestSize = f, info.Size()
		}
	}
	return best
}

// InstallUnverified installs the game's best itch.io match WITHOUT a hash
// match. Only on the user's explicit request (after a failed or impossible
// verification). The paid rule still applies. The file is marked
// UNVERIFIED everywhere; RetroArch itself will tell, when the game starts,
// whether RetroAchievements recognises it.
func (p *Pipeline) InstallUnverified(ctx context.Context, g HubGame, progress func(string)) (Result, error) {
	cands, err := p.Resolve(ctx, g, false)
	if err != nil || len(cands) == 0 {
		return Result{}, ErrNotFound
	}
	return p.installFrom(ctx, g, cands[0], false, progress)
}

// InstallFull installs the paid FULL release of a game whose RA set is for
// its demo. Only possible when the itch.io account bought it; RA does not
// recognise that file, so it is marked unverified (no achievements).
func (p *Pipeline) InstallFull(ctx context.Context, g HubGame, progress func(string)) (Result, error) {
	st := p.Store.State(g.ID)
	if st.FullURL == "" {
		return Result{}, ErrNotFound
	}
	if p.Owned == nil {
		return Result{}, ErrPaid
	}
	if _, owned := p.Owned(st.FullURL); !owned {
		return Result{}, ErrPaid
	}
	return p.installFrom(ctx, g, Candidate{URL: st.FullURL, Price: st.FullPrice}, true, progress)
}

func (p *Pipeline) installFrom(ctx context.Context, g HubGame, cand Candidate, full bool, progress func(string)) (Result, error) {
	say := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		p.Logf("        %s", msg)
		if progress != nil {
			progress(msg)
		}
	}
	console := g.Console()
	dest, err := p.DestDir(console)
	if err != nil {
		return Result{}, fmt.Errorf("no ROM folder for %s: %w", console.Name, err)
	}
	gameID, owned := "", false
	if p.Owned != nil {
		gameID, owned = p.Owned(cand.URL)
	}
	var ups []Upload
	switch {
	case owned:
		ups, err = p.Itch.OwnedUploads(ctx, gameID)
	case IsPaidPrice(cand.Price):
		return Result{}, ErrPaid
	default:
		ups, err = p.Itch.Uploads(ctx, cand.URL)
		if err != nil && looksPaid(err) {
			return Result{}, ErrPaid
		}
	}
	if err != nil {
		return Result{}, err
	}
	ups = RankUploads(console, ups)
	if len(ups) == 0 {
		return Result{}, ErrNoDownloads
	}
	if err := os.MkdirAll(p.WorkDir, 0o755); err != nil {
		return Result{}, err
	}
	job, err := os.MkdirTemp(p.WorkDir, "job-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(job)

	what := "no verification"
	if full {
		what = "full game, no RetroAchievements"
	}
	for i, up := range ups {
		if i >= 2 {
			break
		}
		say("Downloading %s (%s)...", up.Name, what)
		dl := filepath.Join(job, fmt.Sprintf("dl-%d", i))
		up.MaxBytes = maxDownload(console)
		if err := p.Itch.Download(ctx, cand.URL, up, dl); err != nil {
			say("  download failed: %v", err)
			continue
		}
		files, err := Unpack(dl, job)
		if err != nil {
			say("  %v", err)
			continue
		}
		f := mainFile(console, files)
		if f == "" {
			continue
		}
		var path string
		switch {
		case strings.EqualFold(filepath.Ext(f), ".cue"):
			path, err = placeCueSet(f, dest.Dir)
		case full:
			// Next to the demo, not over it.
			name := InstallName(g, console, f, dest.Exts)
			ext := filepath.Ext(name)
			path, err = placeFile(f, dest.Dir, strings.TrimSuffix(name, ext)+" (Full game)"+ext)
		default:
			path, err = placeFile(f, dest.Dir, InstallName(g, console, f, dest.Exts))
		}
		if err != nil {
			return Result{}, fmt.Errorf("install: %w", err)
		}
		p.Store.Update(g.ID, func(s *GameState) {
			if full {
				if s.InstalledPath == "" || s.Status != StatusVerified {
					s.Status, s.InstalledPath, s.MD5 = StatusUnverified, path, ""
				}
				s.Note = "full game installed (RetroAchievements only knows the demo)"
				return
			}
			s.Status, s.InstalledPath, s.MD5 = StatusUnverified, path, ""
			s.ItchURL, s.ItchTitle, s.ItchAuthor = cand.URL, cand.Title, cand.Author
			s.Note = "installed without a RetroAchievements hash match"
		})
		_ = p.Store.Save()
		say("Installed (%s): %s", what, path)
		return Result{Path: path, Candidate: cand, Note: what}, nil
	}
	return Result{}, ErrNoDownloads
}

// DemoProbe checks, for a game whose RA set is a DEMO, whether a free demo
// file is still offered (DemoFree 1 = yes, 2 = no). A demo that is gone is
// shown in red and cannot be downloaded; a full release, if RA adds one,
// arrives as its own hub entry.
func (p *Pipeline) DemoProbe(ctx context.Context, g HubGame) {
	if !IsDemo(g.Title) {
		return
	}
	st := p.Store.State(g.ID)
	if st.Status == StatusVerified || st.Override != "" {
		return
	}
	console := g.Console()
	demo := 2
	for _, c := range st.Candidates {
		ups, err := p.Itch.Uploads(ctx, c.URL)
		if err == nil && len(RankUploads(console, ups)) > 0 {
			demo = 1
			break
		}
		if ctx.Err() != nil {
			return
		}
	}
	p.Store.Update(g.ID, func(s *GameState) {
		s.DemoFree = demo
		s.FullURL, s.FullPrice = "", ""
		switch {
		case demo == 2 && s.Status != StatusVerified:
			s.Status, s.Note = StatusDemoGone, ErrDemoGone.Error()
		case demo == 1 && s.Status == StatusDemoGone:
			s.Status, s.Note = StatusCandidate, "" // the demo is there after all
		}
	})
	_ = p.Store.Save()
}

// RefreshPrice reads the best page's price from the page itself when the
// search result did not show one: a paid game (Good Boy Galaxy) must not be
// taken for a free one just because its search cell had no price tag —
// its free files are the DEMO, whose hash is not the full game's.
func (p *Pipeline) RefreshPrice(ctx context.Context, g HubGame) {
	st := p.Store.State(g.ID)
	if len(st.Candidates) == 0 || st.Candidates[0].Price != "" || st.Candidates[0].PriceChecked {
		return
	}
	page, ok, err := p.Itch.Page(ctx, st.Candidates[0].URL)
	if err != nil {
		return
	}
	p.Store.Update(g.ID, func(s *GameState) {
		if len(s.Candidates) > 0 {
			if ok && page.Price != "" {
				s.Candidates[0].Price = page.Price
			}
			s.Candidates[0].PriceChecked = true
		}
	})
	_ = p.Store.Save()
}

// PreferNamed moves uploads that look like RA's hash files to the front:
// same file name first, then names containing one of the game's titles
// (the English "KOTZ - The Phoenix Returns v1.3.0E.gbc" before the other
// languages). Order is otherwise kept.
func PreferNamed(ups []Upload, hashNames, titles []string) []Upload {
	rank := func(u Upload) int {
		low := strings.ToLower(u.Name)
		for _, h := range hashNames {
			if strings.EqualFold(u.Name, h) || strings.EqualFold(strings.TrimSuffix(u.Name, filepath.Ext(u.Name)),
				strings.TrimSuffix(h, filepath.Ext(h))) {
				return 0
			}
		}
		n := " " + NormLoose(low) + " "
		for _, t := range titles {
			clean := CleanTitle(t)
			parts := []string{clean, MainTitle(clean)}
			if i := strings.Index(clean, ": "); i > 0 {
				parts = append(parts, clean[i+2:])
			}
			for _, part := range parts {
				w := NormLoose(part)
				if len(strings.Fields(w)) >= 2 && strings.Contains(n, " "+w+" ") {
					return 1
				}
			}
		}
		return 2
	}
	out := append([]Upload(nil), ups...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

func looseBest(titles []string, console Console, hints []string, c Candidate) int {
	best := 0
	for _, t := range titles {
		if s := LooseScore(t, console, hints, c); s > best {
			best = s
		}
	}
	return best
}

// splitNames splits RA's developer field: "László Rajcsányi | WLS",
// "A & B", "A / B", "A, B".
func splitNames(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '|' || r == '/' || r == '&' || r == ';' })
}

// PreferDemo orders a page's files by the kind of release RA has: for a
// DEMO entry, files named "demo" first; for a full game, demo files last
// (Good Boy Galaxy's page carries both).
func PreferDemo(ups []Upload, demo bool) []Upload {
	isDemo := func(u Upload) bool {
		n := " " + NormLoose(u.Name) + " "
		return strings.Contains(n, " demo ") || strings.Contains(n, "demo ")
	}
	out := append([]Upload(nil), ups...)
	sort.SliceStable(out, func(i, j int) bool {
		if demo {
			return isDemo(out[i]) && !isDemo(out[j])
		}
		return !isDemo(out[i]) && isDemo(out[j])
	})
	return out
}

var versionRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9])v(?:er(?:sion)?)?[ ._]?(\d+(?:[._]\d+){1,3}[a-z]?)`),
	regexp.MustCompile(`\((\d+\.\d+(?:\.\d+){0,2})\)`),
	// Bare three-part numbers: "Demo 1.0.7", "demo_1_0_7".
	regexp.MustCompile(`(?:^|[^0-9.])(\d{1,2}[._]\d{1,2}[._]\d{1,3})(?:[^0-9]|$)`),
}

// VersionsIn finds version numbers in a file name or label:
// "Goodboy Galaxy (v1.0.6).gba" -> 1.0.6, "demo_v1_0_7.gba" -> 1.0.7.
func VersionsIn(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, re := range versionRes {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			v := strings.ToLower(strings.ReplaceAll(m[1], "_", "."))
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// ensureHashNames reads RA's hash file names for a game once (they name the
// versions RA supports, e.g. "(v1.0.6)").
func (p *Pipeline) ensureHashNames(ctx context.Context, g HubGame) {
	if len(p.Store.State(g.ID).HashNames) > 0 || p.RA == nil || !p.RA.Creds.Valid() {
		return
	}
	entries, err := p.RA.FetchGameHashes(ctx, g.ID)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name != "" {
			names = append(names, e.Name)
		}
	}
	if len(names) > 0 {
		p.Store.Update(g.ID, func(s *GameState) { s.HashNames = names })
	}
}

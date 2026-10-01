package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"leaf-hacks/internal/artwork"
	"leaf-hacks/internal/catalog"
	"leaf-hacks/internal/esmeta"
	"leaf-hacks/internal/library"
	"leaf-hacks/internal/rahub"
	"leaf-hacks/internal/rapatches"
)

// indexMaxAge is how long a cached RAPatches listing is trusted. New hacks
// appear steadily but not hourly, and the listing costs one unauthenticated
// GitHub call, of which there are 60 an hour.
const indexMaxAge = 24 * time.Hour

// updateMaxAge is how often the app looks for newer versions of the hacks
// already installed, and for hacks added since. Two days: new achievement
// sets appear steadily but not hourly, and each check costs one call per
// installed hack.
const updateMaxAge = 48 * time.Hour

var httpClient = &http.Client{Timeout: 120 * time.Second}

// state is everything the app holds between actions.
type state struct {
	cfg     *Config
	ra      *rahub.RAClient
	lib     *library.Library
	index   rapatches.Index
	catalog catalog.Catalog

	// installed is the precomputed lookup behind installedPath.
	installed map[int]map[string]string
	// lastScan explains what the most recent scan ignored.
	lastScan *library.Report
	// updates is the last look for newer versions of installed hacks.
	updates catalog.UpdateCheck
	// stats holds the community numbers behind the popularity sorts.
	stats *catalog.StatsCache
	// registry is the record of what this app installed, by game ID.
	registry *catalog.Registry
	// facts holds, per patch, the base ROM it says it needs. This is what
	// makes membership a checksum comparison instead of a name guess.
	facts *catalog.FactsCache
	// bases and sets are kept so the catalog can be rebuilt as
	// fingerprints arrive, without going back to the network.
	bases []catalog.OwnedBase
	sets  map[int]catalog.SetInfo
}

func newState(cfg *Config) *state {
	return &state{
		cfg:      cfg,
		ra:       rahub.NewRAClient(cfg.credentials(), httpClient),
		stats:    catalog.NewStatsCache(statsPath()),
		registry: catalog.LoadRegistry(registryPath()),
		facts:    catalog.LoadFactsCache(factsPath()),
	}
}

// loadCaches brings up whatever the last run left behind, so the list is
// on screen before any network call happens.
func (s *state) loadCaches() {
	if lib, err := library.Load(libraryPath()); err == nil {
		s.lib = lib
	}
	if ix, err := rapatches.Load(indexPath()); err == nil {
		s.index = ix
	} else if ix, err := rapatches.Load(seedIndexPath()); err == nil {
		// First run: use the listing that shipped with the app, so there
		// is a list on screen before any network call and before the
		// GitHub rate limit is touched.
		s.index = ix
	}
	if c, err := catalog.LoadCatalog(catalogPath()); err == nil {
		s.catalog = c
	}
	if u, err := catalog.LoadUpdateCheck(updatesPath()); err == nil {
		s.updates = u
	}
	s.buildInstalledIndex()
}

// scan fingerprints /roms, reusing the previous scan for unchanged files.
func (s *state) scan(progress library.Progress) error {
	lib, report, err := library.ScanReport(romsRoot, s.lib, progress)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", romsRoot, err)
	}
	s.lastScan = report
	s.lib = lib
	s.buildInstalledIndex()
	s.registry.Prune()
	return lib.Save(libraryPath())
}

// refreshIndex re-reads the patch repository when the cache is stale. A
// failure here is not fatal: yesterday's index still describes almost the
// same set of hacks.
// refreshIndex re-reads the patch repository when the cache is stale, and
// reports whether the listing actually changed. The caller needs that:
// fetching a newer index without rebuilding the catalog from it leaves
// the new hacks invisible until someone runs --refresh by hand.
func (s *state) refreshIndex(ctx context.Context, force bool) (changed bool, err error) {
	if !force && !s.index.Stale(indexMaxAge) {
		return false, nil
	}
	ix, err := rapatches.Fetch(ctx, httpClient)
	if err != nil {
		if len(s.index.Entries) > 0 {
			return false, nil // keep using the cached copy, quietly
		}
		return false, err
	}
	changed = len(ix.Entries) != len(s.index.Entries)
	s.index = ix
	return changed, ix.Save(indexPath())
}

// rebuild identifies the base games on the device and joins them with the
// patch index.
func (s *state) rebuild(ctx context.Context, note func(string)) error {
	if s.lib == nil || len(s.lib.ROMs) == 0 {
		return errors.New("no ROMs found under " + romsRoot)
	}
	if !s.cfg.credentials().Valid() {
		return errors.New("set your RetroAchievements API key first (--ra-login)")
	}
	bases, sets, err := catalog.IdentifyBases(ctx, s.ra, s.lib, note)
	if err != nil {
		return err
	}
	cats := []rapatches.Category{rapatches.Hacks}
	if s.cfg.IncludeTranslations {
		cats = append(cats, rapatches.Translation)
	}
	s.bases = bases
	s.catalog = catalog.Build(bases, s.index, sets, s.facts, s.lib, cats...)
	s.sets = sets
	return s.catalog.Save(catalogPath())
}

// refreshAll is the one action behind the UI's Refresh and behind
// --refresh: rescan, re-index, rebuild.
func (s *state) refreshAll(ctx context.Context, note func(string)) error {
	note("Reading " + romsRoot)
	if err := s.scan(func(done, total int, current string) {
		if total > 0 && current != "" {
			note(fmt.Sprintf("Hashing ROMs %d/%d", done+1, total))
		}
	}); err != nil {
		return err
	}
	note("Fetching the patch index")
	if _, err := s.refreshIndex(ctx, true); err != nil {
		return err
	}
	if err := s.rebuild(ctx, note); err != nil {
		return err
	}
	s.checkUpdates(ctx, true, note)
	return nil
}

// rebuildFromFacts re-runs the join with whatever fingerprints have
// arrived, without touching the network.
func (s *state) rebuildFromFacts() {
	if len(s.bases) == 0 {
		return
	}
	cats := []rapatches.Category{rapatches.Hacks}
	if s.cfg.IncludeTranslations {
		cats = append(cats, rapatches.Translation)
	}
	s.catalog = catalog.Build(s.bases, s.index, s.sets, s.facts, s.lib, cats...)
	_ = s.catalog.Save(catalogPath())
}

// verifyCandidates is the list of patches worth fingerprinting for this
// device: everything on a console the user has, whose folder name is even
// loosely related to one of their games. Generous on purpose — a wasted
// 250 KB download costs far less than a hack the user never sees.
func (s *state) verifyCandidates() []catalog.Candidate {
	cats := []rapatches.Category{rapatches.Hacks}
	if s.cfg.IncludeTranslations {
		cats = append(cats, rapatches.Translation)
	}
	return catalog.Candidates(s.bases, s.index, s.facts, cats...)
}

// installedHacks lists what is already on the card, for the update check.
func (s *state) installedHacks() []catalog.Installed {
	var out []catalog.Installed
	for _, h := range s.catalog.Hacks {
		path, ok := s.installedPath(h)
		if !ok {
			continue
		}
		inst := catalog.Installed{GameID: h.GameID, Stem: stripExt(filepathBase(path))}
		if rec, ok := s.registry.Get(h.GameID); ok && rec.RAHash != "" {
			inst.RAHash = rec.RAHash
		} else if s.lib != nil {
			for _, r := range s.lib.ROMs {
				if r.Path == path {
					inst.RAHash = r.RAHash
					break
				}
			}
		}
		out = append(out, inst)
	}
	return out
}

// checkUpdates runs the two-day look for newer versions. A failure is not
// worth surfacing: the list is still correct, it is just not freshly
// confirmed.
func (s *state) checkUpdates(ctx context.Context, force bool, note func(string)) {
	if !force && !s.updates.Stale(updateMaxAge) {
		return
	}
	installed := s.installedHacks()
	if len(installed) == 0 {
		s.updates = catalog.UpdateCheck{Checked: time.Now()}
		_ = s.updates.Save(updatesPath())
		return
	}
	s.updates = catalog.CheckUpdates(ctx, s.ra, installed, note)
	_ = s.updates.Save(updatesPath())
}

func filepathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// installHack applies the patch and then brings the game's pictures down
// from RetroAchievements, so the hack shows up in the menu looking like a
// real game instead of a bare filename.
//
// Artwork failing is not an install failing: the ROM is already on the
// card and playable, so the error is reported and the path returned.
func (s *state) installHack(ctx context.Context, plan *catalog.Plan, report catalog.Reporter) (string, error) {
	dest, err := catalog.InstallWithProgress(plan, romsRoot, report)
	if err != nil {
		return "", err
	}
	// Record it before anything else can fail: the ROM is on the card,
	// and a record written only after the artwork succeeds would leave a
	// working hack the app does not know it installed.
	rec := catalog.Record{
		GameID:    plan.Hack.GameID,
		Path:      dest,
		Stem:      stripExt(filepathBase(dest)),
		PatchFile: plan.Patch.Name,
	}
	if s.lib != nil {
		_ = s.scan(nil)
		for _, r := range s.lib.ROMs {
			if r.Path == dest {
				rec.RAHash = r.RAHash
				break
			}
		}
	}
	_ = s.registry.Add(rec)

	art, artErr := artwork.Fetch(ctx, s.ra, plan.Hack.GameID)
	var saved []string
	if artErr == nil {
		saved, artErr = artwork.Save(ctx, httpClient, art, dest)
	}

	// Record it in the system's gamelist.xml. Saving pictures into
	// images/ is not enough on its own: once a system has a scraped
	// gamelist, EmulationStation trusts that file and stops guessing
	// from filenames, so a hack dropped in afterwards shows up blank.
	if err := s.writeGamelist(ctx, plan, dest, saved); err != nil && artErr == nil {
		artErr = err
	}
	return dest, artErr
}

// writeGamelist describes the installed hack to EmulationStation.
func (s *state) writeGamelist(ctx context.Context, plan *catalog.Plan, dest string, artFiles []string) error {
	systemDir := filepath.Join(romsRoot, plan.Base.System)

	image := ""
	for _, f := range artFiles {
		if strings.HasSuffix(f, "-image.png") {
			image = f
			break
		}
	}
	if image == "" {
		// Reinstall: the pictures were already there and Save skipped them.
		stem := stripExt(filepathBase(dest))
		candidate := filepath.Join(filepath.Dir(dest), "images", stem+"-image.png")
		if _, err := os.Stat(candidate); err == nil {
			image = candidate
		}
	}

	entry := esmeta.Entry{
		ROMPath:   dest,
		Name:      plan.Hack.Title,
		ImagePath: image,
		Genre:     "ROM hack",
	}
	if st, ok := s.stats.Get(plan.Hack.GameID); ok && st.Genre != "" {
		entry.Genre = st.Genre
	}
	if !plan.Hack.Modified.IsZero() {
		entry.Released = plan.Hack.Modified
	}
	if desc, ok := catalog.FetchDescription(ctx, s.ra, plan.Hack.GameID); ok {
		entry.Desc = desc.Text + "\n\nPosted by " + desc.Author + " on RetroAchievements."
	} else {
		entry.Desc = fmt.Sprintf("Hack of %s. %d achievements, %d points on RetroAchievements.",
			plan.Hack.BaseTitle, plan.Hack.Achievements, plan.Hack.Points)
	}
	return esmeta.Write(systemDir, entry)
}

// installedIndex maps console → normalised ROM title → path.
//
// This used to be a linear search per call, which the UI then ran once per
// hack per frame: with 1450 ROMs and a couple of thousand hacks that is
// millions of string normalisations a frame, and it was the single largest
// cause of the interface feeling slow. It is now built once, whenever the
// library or the catalog changes.
func (s *state) buildInstalledIndex() {
	idx := make(map[int]map[string]string)
	if s.lib == nil {
		s.installed = idx
		return
	}
	for _, r := range s.lib.ROMs {
		// Only files inside a system's hacks folder count. Matching by
		// title across the whole library marked a hack as installed
		// whenever the base game was present — and owning the base game
		// is the very reason a hack is listed at all, so nearly every row
		// came out marked, and the game page offered Play for something
		// that had never been installed.
		if !isHackLocation(r.Path) {
			continue
		}
		byTitle := idx[r.ConsoleID]
		if byTitle == nil {
			byTitle = make(map[string]string)
			idx[r.ConsoleID] = byTitle
		}
		byTitle[catalog.NormalizeTitle(stripExt(r.Name()))] = r.Path
	}
	s.installed = idx
}

// isHackLocation reports whether a path is where this app puts patched
// ROMs. Exactly <system>/hacks/ and nowhere else, so a firmware folder
// like snes-hacks is left to the firmware.
func isHackLocation(path string) bool {
	return strings.Contains(path, "/hacks/")
}

// installedPath reports where a hack is, if it is on the card.
//
// The registry is the answer whenever it has one, because it was written
// at install time and cannot be wrong. Title matching is only a fallback
// for files put in the hacks folder some other way, and it is a weak one:
// RetroAchievements calls a set "Pokémon Emerald Rogue V2" while the file
// is "Pokemon Emerald - Emerald Rogue (v2.0).gba", so the names routinely
// disagree for the same hack.
func (s *state) installedPath(h catalog.Hack) (string, bool) {
	if rec, ok := s.registry.Get(h.GameID); ok {
		if _, err := os.Stat(rec.Path); err == nil {
			return rec.Path, true
		}
	}
	byTitle, ok := s.installed[h.ConsoleID]
	if !ok {
		return "", false
	}
	path, ok := byTitle[catalog.NormalizeTitle(h.Title)]
	return path, ok
}

func stripExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
	}
	return name
}

// --- CLI entry points -------------------------------------------------

func runScanCLI(cfg *Config) {
	s := newState(cfg)
	s.loadCaches()
	start := time.Now()
	err := s.scan(func(done, total int, current string) {
		if current != "" {
			fmt.Printf("\r[%d/%d] %-50.50s", done+1, total, current)
		}
	})
	fmt.Println()
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan failed:", err)
		os.Exit(1)
	}
	bySystem := map[string]int{}
	for _, r := range s.lib.ROMs {
		bySystem[r.System]++
	}
	systems := make([]string, 0, len(bySystem))
	for k := range bySystem {
		systems = append(systems, k)
	}
	sort.Strings(systems)
	zipped := 0
	for _, r := range s.lib.ROMs {
		if r.Inner != "" {
			zipped++
		}
	}
	fmt.Printf("%d ROMs in %s", len(s.lib.ROMs), time.Since(start).Round(time.Millisecond))
	if zipped > 0 {
		fmt.Printf(" (%d read from inside zips)", zipped)
	}
	fmt.Println()
	for _, sys := range systems {
		fmt.Printf("  %-12s %d\n", sys, bySystem[sys])
	}
	// Why a folder you can see is not in the list above.
	if lines := s.lastScan.Lines(); len(lines) > 0 {
		fmt.Println()
		for _, l := range lines {
			fmt.Println("  " + l)
		}
	}
}

func runRefreshCLI(cfg *Config) {
	s := newState(cfg)
	s.loadCaches()
	err := s.refreshAll(context.Background(), func(msg string) { fmt.Printf("\r%-60.60s", msg) })
	fmt.Println()
	if err != nil {
		fmt.Fprintln(os.Stderr, "refresh failed:", err)
		os.Exit(1)
	}
	fmt.Printf("%d hacks with achievements for your ROMs", len(s.catalog.Hacks))
	if s.catalog.WithoutAchievements > 0 {
		fmt.Printf(" (%d patches skipped: no achievement set)", s.catalog.WithoutAchievements)
	}
	fmt.Println()
}

func runListCLI(cfg *Config) {
	s := newState(cfg)
	s.loadCaches()
	if len(s.catalog.Hacks) == 0 {
		fmt.Println("nothing cached yet — run --refresh first")
		return
	}
	var lastBase string
	for _, h := range s.catalog.Hacks {
		if h.BaseTitle != lastBase {
			fmt.Printf("\n%s\n", h.BaseTitle)
			lastBase = h.BaseTitle
		}
		mark := " "
		if _, ok := s.installedPath(h); ok {
			mark = "*"
		}
		fmt.Printf(" %s %-7d %-52.52s %3d ach  %5d pts\n", mark, h.GameID, h.Title, h.Achievements, h.Points)
	}
	fmt.Printf("\n%d hacks with achievements, for %d of your games\n",
		len(s.catalog.Hacks), len(s.catalog.BaseGames))
	if s.catalog.WithoutAchievements > 0 {
		fmt.Printf("(%d more patches matched your ROMs but have no achievement set)\n",
			s.catalog.WithoutAchievements)
	}
	if s.catalog.CompatibilityPatches > 0 {
		fmt.Printf("(%d compatibility patches for the base games themselves, not hacks)\n",
			s.catalog.CompatibilityPatches)
	}
}

// runBasesCLI answers "why is this hack not in my list?".
//
// A hack appears only if its base game was identified, and identification
// is the step with no visible output: a ROM RetroAchievements does not
// recognise, or a disc whose filename does not match a title, simply
// vanishes with no trace. This prints both sides.
// runVerifyCLI does the fingerprinting pass in the foreground, which is
// the sane way to do the first, big one: over SSH it can run to
// completion without the screen, and the result is cached for the app.
func runVerifyCLI(cfg *Config) {
	s := newState(cfg)
	s.loadCaches()
	if s.lib == nil || len(s.catalog.Hacks) == 0 {
		fmt.Fprintln(os.Stderr, "run --refresh first")
		os.Exit(1)
	}
	bases, sets, err := catalog.IdentifyBases(context.Background(), s.ra, s.lib, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s.bases, s.sets = bases, sets

	before := len(s.catalog.Hacks)
	candidates := s.verifyCandidates()
	fmt.Printf("%d patches to check (%d already known)\n", len(candidates), s.facts.Count())

	s.facts.Verify(context.Background(), httpClient, candidates, func(done, total, found int) {
		fmt.Printf("\r  %d/%d checked, %d readable", done, total, found)
	})
	fmt.Println()

	s.rebuildFromFacts()
	needs := 0
	for _, h := range s.catalog.Hacks {
		if h.NeedsOtherDump {
			needs++
		}
	}
	fmt.Printf("%d hacks (was %d)\n", len(s.catalog.Hacks), before)
	fmt.Printf("  %d of them need a dump you do not have\n", needs)
}

// runMetadataCLI rewrites the menu entries for everything already
// installed, for hacks put on the card before this app wrote gamelists.
func runMetadataCLI(cfg *Config) {
	s := newState(cfg)
	s.loadCaches()

	records := s.registry.All()
	if len(records) == 0 {
		fmt.Println("nothing installed")
		return
	}
	for _, rec := range records {
		var hack *catalog.Hack
		for i := range s.catalog.Hacks {
			if s.catalog.Hacks[i].GameID == rec.GameID {
				hack = &s.catalog.Hacks[i]
				break
			}
		}
		if hack == nil {
			fmt.Printf("  %-44.44s not in the catalog, skipped\n", filepathBase(rec.Path))
			continue
		}
		system, ok := systemOf(rec.Path)
		if !ok {
			continue
		}
		plan := &catalog.Plan{Hack: *hack, Base: library.ROM{System: system}}
		if err := s.writeGamelist(context.Background(), plan, rec.Path, nil); err != nil {
			fmt.Printf("  %-44.44s %v\n", hack.Title, err)
			continue
		}
		fmt.Printf("  %-44.44s ok\n", hack.Title)
	}
	fmt.Println("restart EmulationStation to see the changes")
}

func runBasesCLI(cfg *Config, args []string) {
	filter := ""
	if len(args) > 0 {
		filter = strings.ToLower(strings.Join(args, " "))
	}
	s := newState(cfg)
	s.loadCaches()
	if s.lib == nil {
		fmt.Fprintln(os.Stderr, "run --scan first")
		os.Exit(1)
	}

	bases, _, err := catalog.IdentifyBases(context.Background(), s.ra, s.lib, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	counts := map[int]int{}
	for _, h := range s.catalog.Hacks {
		for _, b := range bases {
			if b.Title == h.BaseTitle && b.ConsoleID == h.ConsoleID {
				counts[b.GameID]++
			}
		}
	}

	identified := map[string]bool{}
	shown := 0
	for _, b := range bases {
		for _, r := range b.ROMs {
			identified[r.Path] = true
		}
		if filter != "" && !strings.Contains(strings.ToLower(b.Title), filter) {
			continue
		}
		shown++
		fmt.Printf("%-52.52s %3d hacks\n", b.Title, counts[b.GameID])
		for _, r := range b.ROMs {
			mark := " "
			if r.Disc {
				mark = "d" // matched by title, not by hash
			}
			fmt.Printf("   %s %s\n", mark, shortPathOf(r.Path))
		}
	}
	fmt.Printf("\n%d of your games identified", len(bases))
	if filter != "" {
		fmt.Printf(" (%d shown)", shown)
	}
	fmt.Println()

	// The other half of the answer: files RetroAchievements did not
	// recognise cannot have hacks offered for them.
	var unknown []string
	for _, r := range s.lib.ROMs {
		if identified[r.Path] || isHackLocation(r.Path) {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(r.Name()), filter) {
			continue
		}
		unknown = append(unknown, shortPathOf(r.Path))
	}
	if len(unknown) > 0 {
		fmt.Printf("\n%d files RetroAchievements did not recognise:\n", len(unknown))
		sort.Strings(unknown)
		for i, u := range unknown {
			if i == 40 && filter == "" {
				fmt.Printf("   ... and %d more\n", len(unknown)-40)
				break
			}
			fmt.Println("  ", u)
		}
	}
}

func shortPathOf(p string) string {
	if strings.HasPrefix(p, romsRoot+"/") {
		return p[len(romsRoot)+1:]
	}
	return p
}

func runInstallCLI(cfg *Config, idText string) {
	id, err := strconv.Atoi(idText)
	if err != nil {
		fmt.Fprintln(os.Stderr, "not a game id:", idText)
		os.Exit(2)
	}
	s := newState(cfg)
	s.loadCaches()
	if s.lib == nil {
		fmt.Fprintln(os.Stderr, "run --scan first")
		os.Exit(1)
	}
	var hack *catalog.Hack
	for i := range s.catalog.Hacks {
		if s.catalog.Hacks[i].GameID == id {
			hack = &s.catalog.Hacks[i]
			break
		}
	}
	if hack == nil {
		fmt.Fprintln(os.Stderr, "no hack with id", id, "— run --list")
		os.Exit(1)
	}

	fmt.Printf("%s (hack of %s)\n", hack.Title, hack.BaseTitle)
	plan, err := catalog.Resolve(context.Background(), httpClient, s.ra, *hack, s.lib)
	if err != nil {
		reportResolveError(err)
		os.Exit(1)
	}
	fmt.Printf("  patch : %s (%s)\n", plan.Patch.Name, plan.Format)
	fmt.Printf("  base  : %s\n", plan.Base.Path)
	fmt.Printf("  makes : %s\n", plan.Expected.Name)

	dest, artErr := s.installHack(context.Background(), plan, func(stage catalog.Stage, done, total int64) {
		if total > 0 {
			fmt.Printf("\r  %-32s %3d%%", stage, done*100/total)
		} else {
			fmt.Printf("\r  %-32s    ", stage)
		}
	})
	fmt.Println()
	if dest == "" {
		fmt.Fprintln(os.Stderr, "install failed:", artErr)
		os.Exit(1)
	}
	fmt.Println("installed:", dest)
	if artErr != nil {
		fmt.Fprintln(os.Stderr, "note: artwork could not be saved:", artErr)
	} else {
		fmt.Println("artwork:", filepath.Join(filepath.Dir(dest), "images"))
	}
}

func reportResolveError(err error) {
	var missing *catalog.ErrNoBaseROM
	if errors.As(err, &missing) {
		fmt.Fprintln(os.Stderr, err)
		for _, w := range missing.Wanted {
			fmt.Fprintln(os.Stderr, "  needs:", w)
		}
		for _, n := range missing.Near {
			fmt.Fprintln(os.Stderr, "  you have:", n)
		}
		return
	}
	fmt.Fprintln(os.Stderr, err)
}

// Command hacks lists the RetroAchievements ROM hacks that can be built
// from the ROMs already on this device, downloads their patches from
// RetroAchievements' own RAPatches repository, applies them locally and
// installs the result into /roms.
//
// Nothing is ever downloaded as a ready-made ROM: the device supplies the
// base game, the network supplies a diff. And nothing is installed unless
// the patched file hashes to something RetroAchievements recognises, since
// a hack that boots but earns no achievements is the failure this app
// exists to prevent.
//
// Reused from the earlier proof of concept: internal/appui (screen
// models), internal/sdlui (the SDL2 renderer and every screen it draws),
// internal/rahub (RetroAchievements hashing and API client),
// internal/roms, internal/text, internal/media, and the on-screen
// keyboard. New here: internal/patch, internal/rapatches,
// internal/library, internal/catalog and internal/artwork.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"leaf-hacks/internal/appui"
	"leaf-hacks/internal/artwork"
	"leaf-hacks/internal/catalog"
	"leaf-hacks/internal/esmeta"
	"leaf-hacks/internal/rahub"
	"leaf-hacks/internal/sdlui"
)

func fontPath() string {
	if p := os.Getenv("POC_FONT_PATH"); p != "" {
		return p
	}
	return "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
}

type screenMode int

const (
	modeList screenMode = iota
	modeDetail
	modeFilter
	modeSettings
	modeKeyboard
	modeManage
	modeDownloads
	modeInfo
	modeQuit
)

// Sort and Show vocabularies. The models ship with the itch.io wording
// ("Cheapest paid first", "Bought on itch.io"), which means nothing for
// ROM hacks, so the app replaces the lists at startup.
const (
	sortByGame  = ""
	sortAZ      = "az"
	sortZA      = "za"
	sortSystem  = "system"
	sortPlayers = "players"
	sortUnlocks = "unlocks"
	sortMostAch = "most"
	showAll     = "all"
	showReady   = "ready"
	showInstall = "installed"
	showMissing = "notinstalled"
)

// job is background work whose result the main loop applies. SDL must be
// driven from the thread that initialised it, so network and disk work
// happens in goroutines and only messages cross back.
type job struct {
	note string // progress line, or a "cover <url>" message
	err  error
	done bool
	// plan is a resolved patch, handed to the SDL thread with a
	// "resolved" job. Workers never assign a.plan themselves: the page may
	// have changed between the worker's last check and the assignment.
	plan *catalog.Plan
}

func main() {
	// Required on Linux/KMS-DRM: every SDL call has to come from the
	// thread that initialised SDL, and Go may otherwise migrate this
	// goroutine between OS threads.
	runtime.LockOSThread()

	cfg := loadConfig()
	catalog.TempDir = stagingDir()
	// Nothing is downloading at startup, so anything still staged is
	// debris from a run that was killed before it could tidy up.
	if n, bytes := catalog.SweepStaging(); n > 0 {
		fmt.Fprintf(os.Stderr, "cleared %d leftover downloads (%d MB)\n", n, bytes>>20)
	}
	if handleCLI(cfg, os.Args) {
		return
	}

	st := newState(cfg)
	st.loadCaches()

	screen, err := sdlui.Open(fontPath(), 0, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to start sdlui:", err)
		os.Exit(1)
	}
	defer screen.Close()

	sdlui.QRLabel = "This hack on RetroAchievements"
	sdlui.DetailReinstallLabel = "Reinstall"
	sdlui.DetailHintExtra = "Notes"

	listModel := appui.NewMainListModel(nil)
	listModel.VisibleRows = screen.ListRows()

	app := &ui{
		state:        st,
		screen:       screen,
		list:         listModel,
		jobs:         make(chan job, 64),
		descriptions: map[int]catalog.Description{},
	}
	app.sort = sortByGame
	app.show = showAll
	app.detailQR = -1
	app.setVocabulary()
	app.applyCatalog()
	if len(st.catalog.Hacks) == 0 {
		app.startRefresh()
	} else {
		app.startPeriodicCheck()
	}
	app.startStatsFill()
	app.startVerify()
	app.run()
}

// ui holds the running interface.
type ui struct {
	*state
	filterState

	screen *sdlui.Screen
	list   *appui.MainListModel

	detail   *appui.DetailModel
	filter   *appui.FilterModel
	keyboard *keyboardModel

	mode      screenMode
	shown     []catalog.Hack // parallel to list.Items
	flags     []bool         // installed state, parallel to shown
	current   catalog.Hack
	plan      *catalog.Plan
	cover     string // artwork URL for the open hack
	openStats gameStats

	manage         *appui.ManageModel
	installedList  []catalog.Hack
	settingsCursor int

	// verifying guards the fingerprint pass against being started twice.
	verifying atomic.Bool

	// infoScroll and infoLines drive the notes panel.
	infoScroll int
	infoLines  int

	// running tracks an install in flight. Without it, leaving the game
	// page and coming back showed "press A to patch" for a hack that was
	// already being patched — and pressing A would have started a second
	// one.
	running installRun
	// pageCancel stops the background work of the page the user just left.
	//
	// Opening a game downloads its whole patch archive and decompresses
	// every patch inside it. Flicking through a dozen games left a dozen
	// of those in flight at once, each holding megabytes, and the kernel
	// killed the app after a couple of minutes of browsing.
	pageCancel context.CancelFunc

	// pendingInstall is a game the user pressed A on before its patch had
	// been resolved.
	pendingInstall int
	// installedGameID is the hack the last finished install belonged to,
	// so a page for some other hack is not told it is now installed.
	installedGameID int

	// descriptions caches the About text per game, keyed by set ID.
	descriptions map[int]catalog.Description
	descMu       sync.Mutex

	// Held-button state for the list's fast scroll. Gamepad buttons have
	// no auto-repeat of their own, so it is implemented here: a tap jumps
	// ten, holding keeps going and speeds up the longer it is held.
	heldScroll   appui.Button
	heldSince    time.Time
	lastHeldStep time.Time

	// downloadStatus is the line the game page shows while a patch is
	// being fetched and applied.
	downloadStatus string
	detailFiles    []string
	detailQR       int
	gallery        []string

	jobs    chan job
	busy    bool
	notice  string
	expires time.Time

	// dirty drives redrawing. The screen is repainted only when something
	// changed: the software renderer costs real milliseconds a frame on
	// this hardware, and repainting sixty times a second while nothing
	// happens is what made the list feel heavy.
	dirty bool
}

func (a *ui) setVocabulary() {
	appui.FilterSortValues = []string{
		sortByGame, sortPlayers, sortUnlocks, sortMostAch, sortAZ, sortZA, sortSystem,
	}
	appui.FilterSortLabels = []string{
		"By base game", "Most played (RA)", "Most unlocks (RA)",
		"Most achievements", "A-Z", "Z-A", "By system",
	}

	appui.FilterShowValues = []string{showAll, showReady, showInstall, showMissing}
	appui.FilterShowLabels = []string{"All hacks", "Base ROM ready", "Installed", "Not installed"}

	a.refreshFilterChoices()
}

// refreshFilterChoices keeps the filter rows describing this device: only
// the systems that produced hacks, and only the games actually owned.
// Offering "Pico-8" to someone with no Pico-8 ROMs is just a dead row.
func (a *ui) refreshFilterChoices() {
	seen := map[string]bool{}
	values, labels := []string{""}, []string{"All systems"}
	for _, h := range a.catalog.Hacks {
		console, ok := h.Console()
		if !ok || seen[console.Short] {
			continue
		}
		seen[console.Short] = true
		values = append(values, console.Short)
		labels = append(labels, console.Name)
	}
	appui.FilterPlatforms, appui.FilterPlatformLabels = values, labels

	titles := make([]string, 0, len(a.catalog.BaseGames))
	for title := range a.catalog.BaseGames {
		titles = append(titles, title)
	}
	sort.Strings(titles)
	appui.FilterGenres = append([]string{""}, titles...)
	appui.FilterGenreLabels = append([]string{"All base games"}, titles...)
}

func (a *ui) run() {
	// Draw before the first Poll. Poll blocks for up to 100ms, and an app
	// that shows nothing until a button is pressed looks broken even when
	// the wait is only a tenth of a second.
	a.draw()

	for {
		// Apply background progress before drawing, so the status line is
		// never a frame behind the work it describes.
		for drained := false; !drained; {
			select {
			case j := <-a.jobs:
				a.applyJob(j)
				a.dirty = true
			default:
				drained = true
			}
		}

		// Artwork arrives on a worker and becomes a texture here, on the
		// SDL thread. A repaint is owed only when something was uploaded.
		if a.screen.ProcessPendingImages() {
			a.dirty = true
		}
		if a.dropFailedImages() {
			a.dirty = true
		}

		// Poll blocks for up to 100ms inside SDL, so this loop idles at
		// roughly no cost. No sleep is needed; one would only delay input.
		event, ok, quit := a.screen.Poll()
		if quit {
			return
		}
		if ok {
			if !event.Pressed {
				// Releases are no-ops in every appui model, but they do
				// end a hold.
				if event.Button == a.heldScroll {
					a.heldScroll = appui.ButtonNone
				}
			} else if a.handle(event) {
				return
			} else {
				a.dirty = true
			}
		}

		if a.stepHeldScroll() {
			a.dirty = true
		}

		if a.notice != "" && time.Now().After(a.expires) {
			a.notice = ""
			a.dirty = true
		}

		if a.dirty {
			a.draw()
			a.dirty = false
		}
	}
}

// installRun is the install currently in progress, if any.
type installRun struct {
	mu     sync.Mutex
	gameID int
	title  string
	status string
	active bool
	plan   *catalog.Plan // the plan the install is using; never closed by the page
}

func (r *installRun) start(plan *catalog.Plan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gameID, r.title, r.active = plan.Hack.GameID, plan.Hack.Title, true
	r.plan = plan
	r.status = "Starting..."
}

// owns reports whether a running install is using this plan.
func (r *installRun) owns(p *catalog.Plan) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active && p != nil && r.plan == p
}

func (r *installRun) set(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *installRun) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	r.plan = nil
}

// state returns the running install's status, and whether it is this game.
func (r *installRun) state(gameID int) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, r.active && r.gameID == gameID
}

// otherTitle names the hack currently being patched, for a page that is
// not it.
func (r *installRun) otherTitle() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.title == "" {
		return "another hack"
	}
	return r.title
}

// busy reports whether any install is running, whichever hack it is.
func (r *installRun) busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// gameStats is the RetroAchievements detail that only matters once a hack
// is open: it costs two extra calls, so it is fetched per game rather than
// for the whole list.
type gameStats struct {
	gameID   int
	players  int
	unlocks  int
	genre    string
	progress rahub.Progress
	loaded   bool
}

// --- input ------------------------------------------------------------

func (a *ui) handle(event appui.InputEvent) (exit bool) {
	switch a.mode {
	case modeKeyboard:
		done, cancelled := a.keyboard.Handle(event)
		if cancelled {
			a.mode = modeFilter
			return false
		}
		if done {
			a.filter.Query = a.keyboard.String()
			a.mode = modeFilter
		}
		return false

	case modeFilter:
		switch a.filter.Handle(event) {
		case appui.FilterIntentEditSearch:
			a.keyboard = newKeyboardModel(a.filter.Query)
			a.mode = modeKeyboard
		case appui.FilterIntentApply:
			a.platform, a.sort = a.filter.Platform, a.filter.Sort
			a.show, a.query, a.baseGame = a.filter.Show, a.filter.Query, a.filter.Genre
			a.applyCatalog()
			a.mode = modeList
		case appui.FilterIntentCancel:
			a.mode = modeList
		}
		return false

	case modeQuit:
		if !event.Pressed {
			return false
		}
		switch event.Button {
		case appui.ButtonA:
			return true
		case appui.ButtonB, appui.ButtonQuit:
			a.mode = modeList
		}
		return false

	case modeDownloads:
		// The renderer draws "Y: Play   A: Delete   B: Close" under this
		// window, so those are the keys. Binding anything else here would
		// be arguing with the label the user is reading.
		if event.Pressed && a.manage.State == appui.ManageList {
			switch event.Button {
			case appui.ButtonY:
				a.playInstalledAt(a.manage.Cursor)
				return false
			case appui.ButtonX:
				a.openInstalledAt(a.manage.Cursor)
				return false
			}
		}
		switch a.manage.Handle(event) {
		case appui.ManageIntentBack:
			a.mode = modeList
		case appui.ManageIntentActivate:
			a.confirmDeleteAt(a.manage.Cursor)
		case appui.ManageIntentConfirm:
			a.deleteInstalled()
			a.openDownloads()
		case appui.ManageIntentCancel:
			a.openDownloads()
		}
		return false

	case modeManage:
		switch a.manage.Handle(event) {
		case appui.ManageIntentBack:
			a.mode = modeDetail
		case appui.ManageIntentActivate:
			a.manage.SetConfirm("Delete this hack?", []string{
				a.current.Title,
				"The patched ROM and its pictures are removed.",
				"Your original game is not touched.",
			})
		case appui.ManageIntentConfirm:
			a.deleteInstalled()
		case appui.ManageIntentCancel:
			a.openManage()
		}
		return false

	case modeSettings:
		return a.handleSettings(event)

	case modeInfo:
		if !event.Pressed {
			return false
		}
		switch event.Button {
		case appui.ButtonB, appui.ButtonSelect, appui.ButtonQuit:
			a.mode = modeDetail
		case appui.ButtonDown, appui.ButtonR2:
			a.infoScroll++
		case appui.ButtonUp, appui.ButtonL2:
			a.infoScroll--
		case appui.ButtonR1:
			a.infoScroll += 8
		case appui.ButtonL1:
			a.infoScroll -= 8
		}
		if a.infoScroll < 0 {
			a.infoScroll = 0
		}
		return false

	case modeDetail:
		// SELECT opens the hack's own notes.
		if event.Pressed && event.Button == appui.ButtonSelect {
			a.openInfo()
			return false
		}
		// Y plays the hack. The shared detail model has no Play intent —
		// the app it came from only ever downloaded — so it is handled
		// before the model sees the event.
		if event.Pressed && event.Button == appui.ButtonY {
			a.playCurrent()
			return false
		}
		// The arrows move between hacks, the way they do in the list.
		// In the model they came from they paged the screenshots, which
		// left no way to walk a series of hacks without going back out
		// to the list for each one. Screenshots stay on L1/R1.
		if event.Pressed {
			switch event.Button {
			case appui.ButtonUp, appui.ButtonLeft:
				a.stepDetail(-1)
				return false
			case appui.ButtonDown, appui.ButtonRight:
				a.stepDetail(1)
				return false
			case appui.ButtonL2:
				// One line per press. The indicator underneath counts
				// lines, not pages, so a bigger step ran from "1/4" to
				// "4/4" in a single tap and skipped the middle.
				a.detail.ScrollLine--
				a.detail.SetScrollBounds(a.detail.ScrollMax)
				return false
			case appui.ButtonR2:
				a.detail.ScrollLine++
				a.detail.SetScrollBounds(a.detail.ScrollMax)
				return false
			}
		}
		switch a.detail.Handle(event) {
		case appui.DetailIntentBack:
			a.leavePage()
			a.mode = modeList
		case appui.DetailIntentDownload:
			a.startInstall()
		case appui.DetailIntentManage:
			a.openManage()
		}
		return false
	}

	// L1/R1 page through the list ten at a time. The vendored model binds
	// them to sort cycling, so they are intercepted here rather than by
	// patching shared code.
	if event.Button == appui.ButtonL1 || event.Button == appui.ButtonR1 {
		a.heldScroll = event.Button
		a.heldSince = time.Now()
		a.lastHeldStep = time.Time{}
		step := 10
		if event.Button == appui.ButtonL1 {
			step = -step
		}
		a.jumpCursor(step)
		return false
	}
	// X opens the downloads page: everything this app has installed.
	if event.Button == appui.ButtonX {
		a.openDownloads()
		return false
	}
	// One press to escape a filter that emptied the list.
	if event.Button == appui.ButtonY {
		a.filterState = filterState{show: showAll}
		a.applyCatalog()
		a.setNotice("Filters cleared")
		return false
	}

	switch a.list.Handle(event) {
	case appui.ListIntentExit:
		a.mode = modeQuit
		return false
	case appui.ListIntentOpen:
		a.openDetail()
	case appui.ListIntentFilter:
		a.filter = appui.NewFilterModel(a.platform, a.sort, a.query, a.show, a.baseGame)
		a.mode = modeFilter
	case appui.ListIntentSettings:
		a.settingsCursor = 0
		a.mode = modeSettings
	case appui.ListIntentNextSort:
		a.cycleSort(1)
	case appui.ListIntentPreviousSort:
		a.cycleSort(-1)
	case appui.ListIntentNextPlatform:
		a.cyclePlatform(1)
	case appui.ListIntentPreviousPlatform:
		a.cyclePlatform(-1)
	case appui.ListIntentRetry:
		a.startRefresh()
	}
	return false
}

// stepHeldScroll advances the cursor while L1 or R1 is held. The first
// repeat waits out a short delay so a tap is never mistaken for a hold,
// and the step grows the longer the button is down: a list of thousands is
// unusable at a fixed rate, while short holds stay precise.
func (a *ui) stepHeldScroll() bool {
	if a.heldScroll == appui.ButtonNone || a.mode != modeList {
		return false
	}
	if time.Since(a.heldSince) < 350*time.Millisecond ||
		time.Since(a.lastHeldStep) < 60*time.Millisecond {
		return false
	}
	a.lastHeldStep = time.Now()

	step := 10
	switch held := time.Since(a.heldSince); {
	case held > 3*time.Second:
		step = 120
	case held > 2*time.Second:
		step = 60
	case held > time.Second:
		step = 25
	}
	if a.heldScroll == appui.ButtonL1 {
		step = -step
	}
	return a.jumpCursor(step)
}

// jumpCursor moves the list cursor by step, clamped to the list.
func (a *ui) jumpCursor(step int) bool {
	n := len(a.list.Items)
	if n == 0 {
		return false
	}
	before := a.list.Cursor
	a.list.Cursor += step
	if a.list.Cursor < 0 {
		a.list.Cursor = 0
	}
	if a.list.Cursor >= n {
		a.list.Cursor = n - 1
	}
	return a.list.Cursor != before
}

// cycleSort is L1/R1 on the list: reorder without opening the filter
// screen, the same way the shoulder buttons behave in the other app.
func (a *ui) cycleSort(delta int) {
	a.sort = cycle(appui.FilterSortValues, a.sort, delta)
	a.applyCatalog()
	a.setNotice("Sorted: " + labelFor(appui.FilterSortValues, appui.FilterSortLabels, a.sort))
}

// cyclePlatform is L2/R2: step through the systems on this device.
func (a *ui) cyclePlatform(delta int) {
	a.platform = cycle(appui.FilterPlatforms, a.platform, delta)
	a.applyCatalog()
	a.setNotice(labelFor(appui.FilterPlatforms, appui.FilterPlatformLabels, a.platform))
}

func cycle(values []string, current string, delta int) string {
	if len(values) == 0 {
		return current
	}
	i := 0
	for j, v := range values {
		if v == current {
			i = j
			break
		}
	}
	return values[(i+delta+len(values))%len(values)]
}

func labelFor(values, labels []string, value string) string {
	for i, v := range values {
		if v == value && i < len(labels) {
			return labels[i]
		}
	}
	return value
}

// --- settings ---------------------------------------------------------

func (a *ui) settingsRows() []sdlui.SettingsRow {
	account := a.cfg.RAUser
	if account == "" {
		account = "not set — run --ra-login over SSH"
	}
	translations := "off"
	if a.cfg.IncludeTranslations {
		translations = "on"
	}
	indexAge := "never"
	if !a.index.Fetched.IsZero() {
		indexAge = a.index.Fetched.Format("2006-01-02 15:04")
	}
	romCount := 0
	if a.lib != nil {
		romCount = len(a.lib.ROMs)
	}
	return []sdlui.SettingsRow{
		{Section: "RetroAchievements", Label: "Account", Value: account},
		{Label: "Include translations", Value: translations},
		{Section: "This device", Label: "ROMs fingerprinted", Value: fmt.Sprint(romCount)},
		{Label: "Hacks available", Value: fmt.Sprint(len(a.catalog.Hacks))},
		{Label: "Patch list updated", Value: indexAge},
		{Section: "Actions", Label: "Rescan /roms", Action: true},
		{Label: "Refresh everything", Action: true},
		{Label: "Back", Action: true},
	}
}

func (a *ui) handleSettings(event appui.InputEvent) (exit bool) {
	if !event.Pressed {
		return false
	}
	rows := a.settingsRows()
	switch event.Button {
	case appui.ButtonUp:
		a.settingsCursor = (a.settingsCursor - 1 + len(rows)) % len(rows)
	case appui.ButtonDown:
		a.settingsCursor = (a.settingsCursor + 1) % len(rows)
	case appui.ButtonB, appui.ButtonStart:
		a.mode = modeList
	case appui.ButtonA:
		switch rows[a.settingsCursor].Label {
		case "Include translations":
			a.cfg.IncludeTranslations = !a.cfg.IncludeTranslations
			_ = a.cfg.save()
			a.setNotice("Translations are included from the next refresh")
		case "Rescan /roms":
			a.mode = modeList
			a.startRescan()
		case "Refresh everything":
			a.mode = modeList
			a.startRefresh()
		case "Back":
			a.mode = modeList
		}
	}
	return false
}

// --- rendering --------------------------------------------------------

func (a *ui) draw() {
	a.list.CacheStatus = a.notice
	a.screen.SetAccountLabel(a.accountLabel())
	a.screen.SetFilterActive(a.platform != "" || a.query != "" || a.baseGame != "" || a.show != showAll)

	switch a.mode {
	case modeQuit:
		a.screen.DrawQuitConfirm(a.list, a.flags)
	case modeInfo:
		title, subtitle, body := a.infoText()
		total, visible := a.screen.DrawInfo(title, subtitle, body, a.infoScroll,
			"L2/R2 or D-pad to scroll  -  B closes")
		a.infoLines = total - visible
		if a.infoLines < 0 {
			a.infoLines = 0
		}
		if a.infoScroll > a.infoLines {
			a.infoScroll = a.infoLines
		}
	case modeManage, modeDownloads:
		a.screen.DrawManage(a.manage, a.list, a.flags)
	case modeDetail:
		a.screen.DrawDetail(a.detail, a.downloadStatus, a.cover, a.detailFiles, a.detailQR)
	case modeFilter:
		a.screen.DrawFilter(a.filter, a.list, a.flags)
	case modeKeyboard:
		a.screen.DrawKeyboard(a.keyboard.String(), keyboardRows, a.keyboard.row, a.keyboard.col, "Search hacks")
	case modeSettings:
		a.screen.DrawSettings(a.settingsRows(), a.settingsCursor, a.list, a.flags)
	default:
		a.screen.SetLoadingCatalog(a.busy && len(a.list.Items) == 0)
		a.screen.SetPreviewInfo(a.previewFor(a.list.Cursor))
		a.screen.DrawMainList(a.list, a.flags)
	}
}

// accountLabel is the name in the top-right of the list: the
// RetroAchievements account exactly as it was saved, nothing added.
func (a *ui) accountLabel() string {
	return a.cfg.RAUser
}

func (a *ui) previewFor(i int) sdlui.PreviewInfo {
	if i < 0 || i >= len(a.shown) {
		return sdlui.PreviewInfo{}
	}
	h := a.shown[i]
	console, _ := h.Console()
	info := sdlui.PreviewInfo{
		System:    console.Short,
		Tags:      listSubtitle(h),
		Published: updatedLabel(h),
	}
	a.descMu.Lock()
	desc, ok := a.descriptions[h.GameID]
	a.descMu.Unlock()
	if ok {
		info.Description = desc.Text
	} else {
		info.Description = "Patches a ROM you already have. Nothing is installed unless the patched file is one RetroAchievements recognises."
	}
	if path, ok := a.installedPath(h); ok {
		info.Installed = "Installed: " + shortPath(path)
	}
	return info
}

// --- list -------------------------------------------------------------

// applyCatalog rebuilds the visible list from the catalog and the active
// filter. It also precomputes the installed flag and cover key of every
// row, because doing either per frame is what made the list crawl.
func (a *ui) applyCatalog() {
	a.shown = a.shown[:0]
	query := strings.ToLower(strings.TrimSpace(a.query))

	for _, h := range a.catalog.Hacks {
		console, _ := h.Console()
		if a.platform != "" && console.Short != a.platform {
			continue
		}
		if a.baseGame != "" && h.BaseTitle != a.baseGame {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(h.Title+" "+h.BaseTitle), query) {
			continue
		}
		_, installed := a.installedPath(h)
		switch a.show {
		case showInstall:
			if !installed {
				continue
			}
		case showMissing:
			if installed {
				continue
			}
		case showReady:
			// "Base ROM ready" now means what it says: the patch's own
			// checksum matches something on this card.
			if len(h.BasePaths) == 0 || h.NeedsOtherDump {
				continue
			}
		}
		a.shown = append(a.shown, h)
	}
	a.sortShown()

	items := make([]appui.ListItem, 0, len(a.shown))
	a.flags = make([]bool, len(a.shown))
	for i, h := range a.shown {
		console, _ := h.Console()
		_, a.flags[i] = a.installedPath(h)
		subtitle := listSubtitle(h)
		if _, outdated := a.updates.NeedsUpdate(h.GameID); outdated && a.flags[i] {
			subtitle += " · UPDATE AVAILABLE"
		}
		items = append(items, appui.ListItem{
			Title:    h.Title,
			Author:   subtitle,
			Badge:    console.Short,
			CoverKey: h.Icon,
		})
	}
	a.list.SetItems(items)
	a.refreshFilterChoices()
	a.dirty = true
	if len(items) == 0 && !a.busy {
		a.list.State = appui.ListEmpty
	}
}

// listSubtitle is the second line of a row: what the hack patches and what
// there is to earn in it, which is the pair of facts that decides whether
// a row is worth opening.
func listSubtitle(h catalog.Hack) string {
	base := h.BaseTitle
	if n := len(h.AlsoHackOf); n > 0 {
		base += " or " + strings.Join(h.AlsoHackOf, " or ")
	}
	parts := []string{"hack of " + base}
	if h.Achievements > 0 {
		parts = append(parts, fmt.Sprintf("%d achievements", h.Achievements))
	}
	if h.Points > 0 {
		parts = append(parts, fmt.Sprintf("%d points", h.Points))
	}
	if h.NeedsOtherDump {
		parts = append(parts, "needs another dump")
	}
	if h.IsNew() {
		parts = append(parts, "NEW")
	}
	return strings.Join(parts, " · ")
}

func (a *ui) sortShown() {
	switch a.sort {
	case sortAZ:
		sort.SliceStable(a.shown, func(i, j int) bool { return a.shown[i].Title < a.shown[j].Title })
	case sortZA:
		sort.SliceStable(a.shown, func(i, j int) bool { return a.shown[i].Title > a.shown[j].Title })
	case sortPlayers:
		sort.SliceStable(a.shown, func(i, j int) bool {
			return a.playersOf(a.shown[i]) > a.playersOf(a.shown[j])
		})
	case sortUnlocks:
		sort.SliceStable(a.shown, func(i, j int) bool {
			return a.unlocksOf(a.shown[i]) > a.unlocksOf(a.shown[j])
		})
	case sortMostAch:
		sort.SliceStable(a.shown, func(i, j int) bool {
			return a.shown[i].Achievements > a.shown[j].Achievements
		})
	case sortSystem:
		sort.SliceStable(a.shown, func(i, j int) bool {
			ci, _ := a.shown[i].Console()
			cj, _ := a.shown[j].Console()
			if ci.Short != cj.Short {
				return ci.Short < cj.Short
			}
			return a.shown[i].Title < a.shown[j].Title
		})
	default: // grouped by the game they patch
		sort.SliceStable(a.shown, func(i, j int) bool {
			if a.shown[i].BaseTitle != a.shown[j].BaseTitle {
				return a.shown[i].BaseTitle < a.shown[j].BaseTitle
			}
			return a.shown[i].Title < a.shown[j].Title
		})
	}
}

// playersOf and unlocksOf read the background-filled cache. A game whose
// numbers have not arrived yet sorts last rather than first, so a
// half-filled cache never puts unknowns at the top of "Most played".
func (a *ui) playersOf(h catalog.Hack) int {
	if s, ok := a.stats.Get(h.GameID); ok {
		return s.Players
	}
	return -1
}

func (a *ui) unlocksOf(h catalog.Hack) int {
	if s, ok := a.stats.Get(h.GameID); ok {
		return s.Unlocks
	}
	return -1
}

// --- detail -----------------------------------------------------------

func (a *ui) openDetail() {
	if a.list.Cursor < 0 || a.list.Cursor >= len(a.shown) {
		return
	}
	h := a.shown[a.list.Cursor]
	a.current = h
	// L/R flips straight from one page to the next without leavePage, so
	// the previous page's archive is let go here. Dropping the pointer
	// alone left the file open and the download on the card.
	a.releasePlan()
	a.cover = h.Icon // the badge shows at once; better art replaces it
	a.gallery = nil
	if h.Icon != "" {
		a.gallery = []string{h.Icon}
	}
	console, _ := h.Console()

	installedPath, isInstalled := a.installedPath(h)
	url := fmt.Sprintf("https://retroachievements.org/game/%d", h.GameID)

	a.detail = appui.NewDetailModel(appui.DetailGame{
		Title:    h.Title,
		Author:   "hack of " + h.BaseTitle,
		Platform: console.Short,
		URL:      url,
		IsFree:   true,
		// CanDownload stays false until the patch has been resolved:
		// pressing A before then would have nothing to apply. X manages
		// an install that already exists.
		CanDownload: false,
		Downloaded:  isInstalled,
		PriceText:   a.priceTextFor(h, isInstalled),
	})
	a.downloadStatus = ""
	a.detailFiles = nil
	if isInstalled {
		a.detailFiles = []string{shortPath(installedPath)}
	}
	a.setDetailQR(url)
	a.mode = modeDetail

	sdlui.DetailStatus.Text = "Checking which of your ROMs this patch needs..."
	sdlui.DetailStatus.Level = 0
	a.openStats = gameStats{gameID: h.GameID}
	sdlui.DetailFacts = a.detailFacts(nil)
	a.detail.SetReady("", nil, nil, false, false)
	a.applyDescription()

	// If this hack is being patched right now, say so instead of
	// inviting the user to start it again.
	if status, running := a.running.state(h.GameID); running {
		a.downloadStatus = status
		sdlui.DetailStatus.Text = "Patching in progress — leave it running."
		sdlui.DetailStatus.Level = 0
	}

	// Everything this page fetches belongs to this page. Opening another
	// one cancels the lot: the work is only useful for the page that
	// asked, and a download nobody is waiting for is pure cost.
	if a.pageCancel != nil {
		a.pageCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pageCancel = cancel

	go a.resolve(ctx, h)
	go a.loadArtwork(ctx, h)
	go a.loadStats(ctx, h)
	go a.loadDescription(ctx, h)
}

// loadDescription fills the About section from the game's comment thread,
// since RetroAchievements keeps no description field for a set.
func (a *ui) loadDescription(parent context.Context, h catalog.Hack) {
	a.descMu.Lock()
	_, cached := a.descriptions[h.GameID]
	a.descMu.Unlock()
	if cached {
		a.jobs <- job{note: "about " + fmt.Sprint(h.GameID)}
		return
	}

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	desc, ok := catalog.FetchDescription(ctx, a.ra, h.GameID)
	if !ok {
		return
	}
	a.descMu.Lock()
	a.descriptions[h.GameID] = desc
	a.descMu.Unlock()
	a.jobs <- job{note: "about " + fmt.Sprint(h.GameID)}
}

// priceTextFor replaces the price line, which has no meaning here, with
// the one status that matters at a glance.
func (a *ui) priceTextFor(h catalog.Hack, installed bool) string {
	// Short on purpose: this sits in the header beside the title, where
	// a filename runs off the edge of a 960px screen. The name of the
	// newer file belongs in the facts, where there is room for it.
	if _, outdated := a.updates.NeedsUpdate(h.GameID); outdated && installed {
		return "update available"
	}
	if installed {
		return "installed"
	}
	if h.Achievements > 0 {
		return fmt.Sprintf("%d achievements", h.Achievements)
	}
	return "free"
}

// setDetailQR renders a QR of the RetroAchievements page so the set can be
// opened on a phone, where reading the achievement list is actually
// pleasant.
func (a *ui) setDetailQR(url string) {
	if a.detailQR >= 0 {
		a.screen.ClearQRCode()
	}
	a.detailQR = makeQR(a.screen, url)
}

// detailFacts builds the game page rows. Achievement facts come first
// because they are the reason this app exists; the patch mechanics come
// after, and the plan rows appear once the patch has been resolved.
func (a *ui) detailFacts(p *catalog.Plan) [][2]string {
	h := a.current
	console, _ := h.Console()

	hackOf := h.BaseTitle
	if len(h.AlsoHackOf) > 0 {
		hackOf += " or " + strings.Join(h.AlsoHackOf, " or ")
	}
	facts := [][2]string{{"Hack of", hackOf}}
	if h.Achievements > 0 {
		facts = append(facts, [2]string{"Achievements", fmt.Sprintf("%d (%d points)", h.Achievements, h.Points)})
	}
	if h.NeedsOtherDump && h.WantedDump != "" {
		facts = append(facts, [2]string{"Needs", tail(h.WantedDump, 40)})
	}
	if newer, outdated := a.updates.NeedsUpdate(h.GameID); outdated {
		facts = append(facts, [2]string{"Newer version", tail(newer, 40)})
	}
	if a.openStats.loaded && a.openStats.gameID == h.GameID {
		if a.openStats.progress.Possible > 0 {
			label := fmt.Sprintf("%d / %d unlocked", a.openStats.progress.Achieved, a.openStats.progress.Possible)
			if a.openStats.progress.Mastered {
				label += " (MASTERED)"
			}
			facts = append(facts, [2]string{"You", label})
		}
		if a.openStats.players == 0 {
			if cached, ok := a.stats.Get(h.GameID); ok {
				a.openStats.players, a.openStats.unlocks = cached.Players, cached.Unlocks
				if a.openStats.genre == "" {
					a.openStats.genre = cached.Genre
				}
			}
		}
		if a.openStats.players > 0 {
			facts = append(facts, [2]string{"Players", fmt.Sprintf("%d (%d unlocks)", a.openStats.players, a.openStats.unlocks)})
		}
		if a.openStats.genre != "" {
			facts = append(facts, [2]string{"Genre", a.openStats.genre})
		}
	}
	if label := updatedLabel(h); label != "" {
		facts = append(facts, [2]string{"Set updated", label})
	}
	// System, set number and destination folder are all in the header,
	// the QR code or the README; repeating them here pushed the facts
	// that matter off a 720px-tall panel.
	_ = console

	if p == nil {
		return facts
	}
	if p.RegionWrong {
		facts = append(facts, [2]string{"Needs", p.Region.WantedFile})
	}
	yourROM := tail(p.Base.Name(), 40)
	if n := len(p.Bases); n > 1 {
		yourROM += fmt.Sprintf("  (+%d copies)", n-1)
	}
	facts = append(facts,
		[2]string{"Your ROM", yourROM},
		[2]string{"Patch", fmt.Sprintf("%s (%s)", tail(p.Patch.Stem(), 30), p.Format)},
	)
	if n := len(p.Alternatives); n > 0 {
		facts = append(facts, [2]string{"Other versions", fmt.Sprintf("%d more in this patch", n)})
	}
	return facts
}

// tail keeps the end of a long value, where the version and the author
// live, instead of the start, where every file in an archive looks the
// same. The panel is about 380px wide at 960x720.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max+3:]
}

func updatedLabel(h catalog.Hack) string {
	if h.Modified.IsZero() {
		return ""
	}
	label := h.Modified.Format("2006-01-02")
	if h.IsNew() {
		label += " (NEW)"
	}
	return label
}

// loadStats fetches the two per-game extras: community numbers and the
// user's own progress. Neither blocks anything, so a failure is silent.
func (a *ui) loadStats(parent context.Context, h catalog.Hack) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	st := gameStats{gameID: h.GameID, loaded: true}
	if players, unlocks, genre, err := a.ra.FetchGameStats(ctx, h.GameID); err == nil {
		st.players, st.unlocks, st.genre = players, unlocks, genre
	}
	if progress, err := a.ra.FetchUserProgress(ctx, h.GameID); err == nil {
		st.progress = progress
	}
	a.openStats = st
	a.jobs <- job{note: "stats"}
}

// resolve downloads the patch and decides which local ROM it applies to.
// It runs off the SDL thread; the outcome arrives as a job.
func (a *ui) resolve(parent context.Context, h catalog.Hack) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()

	plan, err := catalog.Resolve(ctx, httpClient, a.ra, h, a.lib)
	if err != nil {
		if parent.Err() != nil {
			return // the user moved on; this page no longer exists
		}
		a.jobs <- job{err: err, done: true, note: "resolve"}
		return
	}
	if parent.Err() != nil {
		// Drop the archive rather than holding it for a page nobody is
		// looking at.
		plan.Close()
		return
	}
	// The SDL thread decides whether this plan still belongs to the page
	// on screen (applyJob). Checking here and assigning a.plan here left a
	// window where the user had already moved to another hack, and A then
	// installed the previous one.
	a.jobs <- job{note: "resolved", done: true, plan: plan}
}

// loadArtwork asks RetroAchievements for the hack's pictures and hands the
// cover to the image cache, which fetches and decodes it off-thread.
func (a *ui) loadArtwork(parent context.Context, h catalog.Hack) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	art, err := artwork.Fetch(ctx, a.ra, h.GameID)
	if err != nil {
		// Even with no artwork call, the badge from the list is still
		// worth showing rather than an empty frame.
		a.jobs <- job{note: "cover " + h.Icon}
		return
	}
	// Every picture the set has, so L1/R1 flips through them on the page.
	var gallery []string
	for _, u := range []string{art.Box, art.Screen, art.InGame, art.Icon} {
		if u != "" {
			gallery = append(gallery, u)
		}
	}
	// The badge from the list goes last, as the one picture that is known
	// to load: plenty of hacks have no screenshots, and some of the URLs
	// the API returns are dead.
	if h.Icon != "" {
		seen := false
		for _, u := range gallery {
			if u == h.Icon {
				seen = true
				break
			}
		}
		if !seen {
			gallery = append(gallery, h.Icon)
		}
	}
	a.gallery = gallery
	cover := art.Cover()
	if cover == "" {
		cover = h.Icon
	}
	a.jobs <- job{note: "cover " + cover}
}

// openDownloads is the library page: every hack this app has installed,
// and nothing else. Reached with X from the list.
func (a *ui) openDownloads() {
	a.installedList = a.installedList[:0]
	var items []appui.ManageItem
	for _, h := range a.catalog.Hacks {
		path, ok := a.installedPath(h)
		if !ok {
			continue
		}
		a.installedList = append(a.installedList, h)
		detail := humanSize(sizeOnDisk(path)) + "  ·  " + shortPath(path)
		badge := ""
		if want, outdated := a.updates.NeedsUpdate(h.GameID); outdated {
			badge = "UPDATE"
			detail = humanSize(sizeOnDisk(path)) + "  ·  newer version: " + want
		}
		items = append(items, appui.ManageItem{
			Kind:    appui.ManageItemFile,
			Label:   h.Title,
			Detail:  detail,
			Badge:   badge,
			Enabled: true,
		})
	}

	a.manage = appui.NewManageModel("Installed hacks")
	a.manage.VisibleRows = a.screen.ListRows()
	subtitle := fmt.Sprintf("%d installed, %s on the card", len(items), humanSize(a.installedBytes()))
	if len(items) == 0 {
		subtitle = "Nothing installed yet — open a hack and press A"
	}
	a.manage.SetItems(subtitle, items)
	a.mode = modeDownloads
}

// confirmDeleteAt asks before removing a hack from the downloads page.
func (a *ui) confirmDeleteAt(i int) {
	if i < 0 || i >= len(a.installedList) {
		return
	}
	a.current = a.installedList[i]
	path, ok := a.installedPath(a.current)
	if !ok {
		return
	}
	a.manage.SetConfirm("Delete this hack?", []string{
		a.current.Title,
		humanSize(sizeOnDisk(path)) + " will be freed.",
		"Your original game is not touched.",
	})
}

// installedBytes is what every installed hack takes up together.
func (a *ui) installedBytes() int64 {
	var total int64
	for _, h := range a.installedList {
		if path, ok := a.installedPath(h); ok {
			total += sizeOnDisk(path)
		}
	}
	return total
}

// sizeOnDisk includes a disc hack's .bin, which is where all the weight
// is: the .cue that names it is a few hundred bytes.
func sizeOnDisk(path string) int64 {
	var total int64
	if info, err := os.Stat(path); err == nil {
		total = info.Size()
	}
	if strings.EqualFold(filepath.Ext(path), ".cue") {
		bin := strings.TrimSuffix(path, filepath.Ext(path)) + ".bin"
		if info, err := os.Stat(bin); err == nil {
			total += info.Size()
		}
	}
	return total
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return "-"
}

// playInstalledAt launches a hack straight from the downloads window.
func (a *ui) playInstalledAt(i int) {
	if i < 0 || i >= len(a.installedList) {
		return
	}
	a.current = a.installedList[i]
	a.playCurrent()
}

// openInstalledAt jumps from the downloads page to a hack's own page.
func (a *ui) openInstalledAt(i int) {
	if i < 0 || i >= len(a.installedList) {
		return
	}
	h := a.installedList[i]
	for j, shown := range a.shown {
		if shown.GameID == h.GameID {
			a.list.Cursor = j
			a.openDetail()
			return
		}
	}
	// Filtered out of the visible list: open it anyway.
	a.shown = append(a.shown, h)
	a.list.Cursor = len(a.shown) - 1
	a.openDetail()
}

// openInfo shows what is known about the hack beyond its numbers.
func (a *ui) openInfo() {
	a.infoScroll = 0
	a.mode = modeInfo
}

// infoText assembles the notes panel.
//
// The readme that ships inside the patch comes first, because it is the
// author writing about their own hack — the only source that reliably
// says what changed. RetroAchievements has no description field at all,
// so everything else is second-hand.
func (a *ui) infoText() (title, subtitle, body string) {
	h := a.current
	title = h.Title
	console, _ := h.Console()
	subtitle = fmt.Sprintf("hack of %s  -  %s  -  %d achievements, %d points",
		h.BaseTitle, console.Name, h.Achievements, h.Points)

	var sections []string
	if a.plan != nil && a.plan.Notes != "" {
		sections = append(sections, "From the patch's own readme:\n\n"+a.plan.Notes)
	}

	a.descMu.Lock()
	desc, ok := a.descriptions[h.GameID]
	a.descMu.Unlock()
	if ok {
		sections = append(sections,
			"From the RetroAchievements page, posted by "+desc.Author+":\n\n"+desc.Text)
	}

	if len(sections) == 0 {
		if a.plan == nil {
			return title, subtitle, "Still reading the patch. Give it a moment and press SELECT again."
		}
		return title, subtitle, "This hack ships no readme, and nobody has written about it on " +
			"RetroAchievements yet.\n\nThe page on RetroAchievements (the QR code on the game " +
			"page opens it) sometimes has more."
	}
	return title, subtitle, strings.Join(sections, "\n\n\n")
}

// openManage is X on an installed hack: the page that can remove it again.
func (a *ui) openManage() {
	path, ok := a.installedPath(a.current)
	if !ok {
		a.setNotice("This hack is not installed")
		return
	}
	a.manage = appui.NewManageModel(a.current.Title)
	a.manage.VisibleRows = 6
	items := []appui.ManageItem{{
		Kind:    appui.ManageItemDeleteROMs,
		Label:   "Delete this hack",
		Detail:  shortPath(path),
		Enabled: true,
	}}
	a.manage.SetItems("Installed from "+a.current.Entry.File, items)
	a.mode = modeManage
}

// deleteInstalled removes the patched ROM and the pictures that came with
// it. The base game is never touched: it is the user's own copy and the
// only reason any of this works.
func (a *ui) deleteInstalled() {
	path, ok := a.installedPath(a.current)
	if !ok {
		a.manage.SetError("This hack is not installed any more")
		return
	}
	// A disc is a cue plus one file per track; every track the cue names
	// beside it goes too. Read before the cue itself is removed.
	var tracks []string
	if strings.EqualFold(filepath.Ext(path), ".cue") {
		tracks, _ = rahub.CueFiles(path)
		tracks = append(tracks, strings.TrimSuffix(path, filepath.Ext(path))+".bin")
	}
	if err := os.Remove(path); err != nil {
		a.manage.SetError(err.Error())
		return
	}
	for _, t := range tracks {
		if filepath.Dir(t) == filepath.Dir(path) {
			os.Remove(t)
		}
	}
	removeArtwork(path)
	// The menu would otherwise keep listing a game whose file is gone.
	if system, ok := systemOf(path); ok {
		_ = esmeta.Remove(filepath.Join(romsRoot, system), path)
	}
	_ = a.registry.Remove(a.current.GameID)
	_ = a.scan(nil)
	a.applyCatalog()

	// The page underneath still says the hack is installed, and would
	// keep offering Play for a file that is gone.
	if a.detail != nil {
		a.detail.Game.Downloaded = false
		a.detail.Game.PriceText = a.priceTextFor(a.current, false)
		a.detailFiles = nil
	}
	a.manage.SetResult("Removed. Your original game is untouched.")
}

// systemOf reads the system folder out of a path under /roms.
func systemOf(path string) (string, bool) {
	rel, err := filepath.Rel(romsRoot, path)
	if err != nil {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return "", false
	}
	return parts[0], true
}

// removeArtwork deletes the pictures saved beside a hack.
func removeArtwork(romPath string) {
	dir := filepath.Join(filepath.Dir(romPath), "images")
	stem := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))
	for _, suffix := range []string{"-image", "-thumb", "-title", "-ingame"} {
		os.Remove(filepath.Join(dir, stem+suffix+".png"))
	}
}

// dropFailedImages removes pictures the cache could not fetch or decode.
//
// Not every URL RetroAchievements hands out resolves, and a dead one left
// in the list showed an empty frame under "Image 1/4" — four pictures
// counted, none drawn. Dropping it moves to the next, and the badge from
// the list is kept last so there is always something to show.
func (a *ui) dropFailedImages() bool {
	if a.mode != modeDetail || a.detail == nil || len(a.detail.Images) == 0 {
		return false
	}
	kept := make([]string, 0, len(a.detail.Images))
	for _, u := range a.detail.Images {
		if !a.screen.ImageFailed(u) {
			kept = append(kept, u)
		}
	}
	if len(kept) == len(a.detail.Images) {
		return false
	}
	a.detail.Images = kept
	if a.detail.ImageIndex >= len(kept) {
		a.detail.ImageIndex = 0
	}
	if len(kept) > 0 {
		a.cover = kept[a.detail.ImageIndex]
	}
	return true
}

// applyDescription puts the cached About text on the open page, credited
// to whoever wrote it. It is somebody's forum post, not the app's words,
// so the attribution is part of the text rather than decoration.
func (a *ui) applyDescription() {
	if a.detail == nil {
		return
	}
	a.descMu.Lock()
	desc, ok := a.descriptions[a.current.GameID]
	a.descMu.Unlock()
	if !ok {
		return
	}
	body := desc.Text
	if desc.Author != "" {
		body += "\n\nposted by " + desc.Author + " on RetroAchievements"
	}
	a.detail.SetReady(body, nil, a.gallery, false, false)
}

// leavePage stops whatever the open page was fetching and lets go of the
// patch archive it was holding, unless an install is using it.
func (a *ui) leavePage() {
	if a.pageCancel != nil {
		a.pageCancel()
		a.pageCancel = nil
	}
	a.releasePlan()
}

// releasePlan lets go of the open page's patch archive, unless an install
// is using it — the install closes it itself when it finishes.
func (a *ui) releasePlan() {
	if a.plan != nil && !a.running.owns(a.plan) {
		a.plan.Close()
	}
	a.plan = nil
}

// stepDetail opens the hack before or after this one, keeping the list
// cursor in step so going back lands where the user expects.
func (a *ui) stepDetail(delta int) {
	if len(a.shown) == 0 {
		return
	}
	next := a.list.Cursor + delta
	if next < 0 || next >= len(a.shown) {
		return
	}
	a.list.Cursor = next
	a.openDetail()
}

// playCurrent hands the patched ROM to the emulator EmulationStation would
// use for that system, and comes back here when the game exits.
func (a *ui) playCurrent() {
	path, ok := a.installedPath(a.current)
	if !ok {
		a.downloadStatus = "Install it first — press A."
		return
	}
	console, _ := a.current.Console()
	system := a.current.Entry.Console
	if a.lib != nil {
		for _, r := range a.lib.ROMs {
			if r.Path == path {
				system = r.System
				break
			}
		}
	}
	_ = console

	a.downloadStatus = "Launching " + a.current.Title + "..."
	a.draw()

	// The screen has to be given up while the emulator owns it, and taken
	// back afterwards; otherwise both draw to the same framebuffer.
	a.screen.Suspend()
	err := launchGame(system, path)
	if rerr := a.screen.Resume(fontPath()); rerr != nil {
		// Without the screen back there is nothing left to show; say so
		// on the way out rather than spin invisibly.
		fmt.Fprintln(os.Stderr, "could not take the screen back:", rerr)
		os.Exit(1)
	}
	a.detailQR = makeQR(a.screen, a.detail.Game.URL)
	a.dirty = true

	if err != nil {
		a.downloadStatus = "Could not launch: " + err.Error()
		fmt.Fprintln(os.Stderr, "launch failed:", err)
		return
	}
	a.downloadStatus = ""
}

// --- install ----------------------------------------------------------

func (a *ui) startInstall() {
	// The install guard is the install's own state, not the shared busy
	// flag. applyJob clears busy for every finished job — including a
	// resolve on some other hack — so opening a second game mid-install
	// unlocked the button and started a second patch over the first.
	if _, running := a.running.state(a.current.GameID); running {
		return
	}
	if a.running.busy() {
		a.setNotice("Another hack is being patched — wait for it to finish")
		return
	}
	if a.plan == nil || a.plan.Hack.GameID != a.current.GameID {
		// Pressing A and having nothing happen reads as a dead button.
		// Remember the intent and start as soon as the patch resolves.
		a.pendingInstall = a.current.GameID
		a.downloadStatus = "Waiting for the patch details..."
		a.setNotice("Will start as soon as the patch is ready")
		return
	}
	a.pendingInstall = 0
	// The install owns the plan from here; the page may be left freely
	// without cancelling the work underneath it.
	a.pageCancel = nil
	a.busy = true
	plan := a.plan
	a.running.start(plan)
	a.installedGameID = plan.Hack.GameID
	a.downloadStatus = "Starting..."

	go func() {
		defer a.running.finish()
		report := func(stage catalog.Stage, done, total int64) {
			line := string(stage)
			if total > 0 {
				line = fmt.Sprintf("%s %d%%", stage, done*100/total)
			}
			a.running.set(line)
			a.progress("run " + line)
		}
		dest, err := a.installHack(context.Background(), plan, report)
		if dest == "" {
			plan.Close()
			a.jobs <- job{err: err, done: true, note: "install"}
			return
		}
		a.running.set("Rescanning")
		_ = a.scan(nil) // so the new ROM shows as installed straight away
		plan.Close()
		a.jobs <- job{note: "installed: " + shortPath(dest), done: true}
	}()
}

// --- background work --------------------------------------------------

func (a *ui) startRefresh() {
	if a.busy {
		return
	}
	a.busy = true
	a.list.SetLoading()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := a.refreshAll(ctx, a.progress)
		a.jobs <- job{err: err, done: true, note: "refresh"}
	}()
}

// startPeriodicCheck is the two-day look for newer hacks and for installed
// hacks that have fallen behind. It runs in the background at launch and
// says nothing unless it finds something, because a check that interrupts
// is a check the user learns to dread.
func (a *ui) startPeriodicCheck() {
	if a.busy {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		// A newer listing is no use on its own: the catalog has to be
		// rebuilt from it, or hacks that got an achievement set this week
		// stay invisible until someone runs --refresh by hand. That was
		// the one thing still needing a manual step.
		if changed, err := a.refreshIndex(ctx, false); err == nil && changed {
			a.progress("New hacks in the patch list — rebuilding")
			if err := a.rebuild(ctx, nil); err == nil {
				a.jobs <- job{note: "reindexed", done: true}
			}
		}
		if !a.updates.Stale(updateMaxAge) {
			return
		}
		a.checkUpdates(ctx, false, nil)
		a.jobs <- job{note: "checked", done: true}
	}()
}

// startStatsFill walks the catalog in the background filling in player and
// unlock counts, which is what the popularity sorts read. It paces itself
// and keeps what it has between launches, so the second run starts where
// the first left off.
func (a *ui) startStatsFill() {
	if !a.cfg.credentials().Valid() || len(a.catalog.Hacks) == 0 {
		return
	}
	hacks := append([]catalog.Hack(nil), a.catalog.Hacks...)
	go func() {
		a.stats.Fill(context.Background(), a.ra, hacks, func(done, total int) {
			if done%50 == 0 || done == total {
				a.progress(fmt.Sprintf("Reading RetroAchievements popularity %d/%d", done, total))
			}
		})
		a.jobs <- job{note: "stats-filled", done: true}
	}()
}

// startVerify fingerprints patches in the background so membership stops
// depending on whether two people spelled a game the same way.
//
// Each result is a permanent fact about that patch, so this shrinks to
// nothing after the first few sessions. The catalog is rebuilt in batches
// rather than per result: a list that reorders under the user's cursor
// while they are reading it is worse than one that updates a moment late.
func (a *ui) startVerify() {
	if !a.cfg.credentials().Valid() || len(a.bases) == 0 {
		return
	}
	// One pass at a time. A rebuild triggered by a new listing starts
	// another, and two passes downloading the same queue would double
	// the traffic for nothing.
	if !a.verifying.CompareAndSwap(false, true) {
		return
	}
	candidates := a.verifyCandidates()
	if len(candidates) == 0 {
		return
	}
	go func() {
		a.progress(fmt.Sprintf("Checking %d patches against your ROMs", len(candidates)))
		lastRebuild := 0
		a.facts.Verify(context.Background(), httpClient, candidates, func(done, total, found int) {
			if done-lastRebuild >= 25 || done == total {
				lastRebuild = done
				a.jobs <- job{note: fmt.Sprintf("verify %d %d %d", done, total, found)}
			}
		})
		a.verifying.Store(false)
		a.jobs <- job{note: "verified", done: true}
	}()
}

func (a *ui) startRescan() {
	if a.busy {
		return
	}
	a.busy = true
	go func() {
		err := a.scan(func(done, total int, current string) {
			if total > 0 && current != "" {
				a.progress(fmt.Sprintf("Hashing ROMs %d/%d", done+1, total))
			}
		})
		a.jobs <- job{err: err, done: true, note: "rescan"}
	}()
}

// progress never blocks the worker: a status line that arrives late is
// worth little, and a worker stuck on a full channel is worth less.
func (a *ui) progress(msg string) {
	select {
	case a.jobs <- job{note: msg}:
	default:
	}
}

func (a *ui) applyJob(j job) {
	if !j.done {
		if line, ok := strings.CutPrefix(j.note, "run "); ok {
			if a.mode == modeDetail {
				a.downloadStatus = capitalise(line)
			}
			a.setNotice(line)
			return
		}
		if rest, ok := strings.CutPrefix(j.note, "verify "); ok {
			var done, total, found int
			fmt.Sscanf(rest, "%d %d %d", &done, &total, &found)
			a.rebuildFromFacts()
			a.applyCatalog()
			a.setNotice(fmt.Sprintf("Checked %d/%d patches — %d hacks", done, total, len(a.catalog.Hacks)))
			return
		}
		if id, ok := strings.CutPrefix(j.note, "about "); ok {
			if a.mode == modeDetail && id == fmt.Sprint(a.current.GameID) {
				a.applyDescription()
			}
			return
		}
		if url, ok := strings.CutPrefix(j.note, "cover "); ok {
			if a.mode == modeDetail && url != "" {
				a.cover = url
				if a.detail != nil && len(a.gallery) > 0 {
					a.detail.Images = a.gallery
				}
			}
			return
		}
		if j.note == "stats" {
			if a.mode == modeDetail && a.openStats.gameID == a.current.GameID {
				sdlui.DetailFacts = a.detailFacts(a.plan)
			}
			return
		}
		if a.mode == modeDetail {
			a.downloadStatus = capitalise(j.note)
		}
		a.setNotice(j.note)
		return
	}
	a.busy = false

	switch j.note {
	case "resolve":
		a.showResolveError(j.err)
		return
	case "resolved":
		if j.plan == nil {
			return
		}
		if a.mode != modeDetail || j.plan.Hack.GameID != a.current.GameID {
			// Resolved for a page that is no longer open.
			j.plan.Close()
			return
		}
		if a.plan != j.plan {
			a.releasePlan()
			a.plan = j.plan
		}
		a.showPlan()
		return
	case "install":
		sdlui.DetailStatus.Text = wrapError(j.err)
		sdlui.DetailStatus.Level = sdlui.StatusBad
		a.downloadStatus = ""
		a.setNotice("Install failed")
		a.showPlan()
		return
	case "stats-filled":
		if a.sort == sortPlayers || a.sort == sortUnlocks {
			a.applyCatalog()
		}
		return
	case "verified":
		a.rebuildFromFacts()
		a.applyCatalog()
		a.setNotice(fmt.Sprintf("%d hacks for your ROMs", len(a.catalog.Hacks)))
		return
	case "reindexed":
		a.applyCatalog()
		a.setNotice(fmt.Sprintf("%d hacks for your ROMs", len(a.catalog.Hacks)))
		a.startVerify() // fingerprint whatever the new listing added
		return
	case "checked":
		a.applyCatalog()
		if n := len(a.updates.Outdated); n > 0 {
			a.setNotice(fmt.Sprintf("%d installed hacks have a newer version", n))
		}
		return
	case "rescan":
		if j.err != nil {
			a.setNotice(j.err.Error())
			return
		}
		a.applyCatalog()
		a.setNotice(fmt.Sprintf("%d ROMs fingerprinted", len(a.lib.ROMs)))
		return
	case "refresh":
		if j.err != nil {
			a.list.SetError(j.err.Error())
			a.setNotice("")
			return
		}
		a.applyCatalog()
		a.setNotice(fmt.Sprintf("%d hacks for %d of your games", len(a.catalog.Hacks), len(a.catalog.BaseGames)))
		return
	}

	if strings.HasPrefix(j.note, "installed:") {
		if a.mode == modeDetail && a.plan != nil && a.plan.Hack.GameID == a.installedGameID {
			sdlui.DetailStatus.Text = "Installed and verified — achievements will work."
			sdlui.DetailStatus.Level = sdlui.StatusOK
			a.detail.Game.Downloaded = true
			a.detail.Game.PriceText = "installed"
		}
		a.downloadStatus = ""
		a.applyCatalog()
	}
	// Whatever page the user is on, the button it offers has just
	// changed: the queue is free again.
	a.showPlan()
	a.setNotice(j.note)
}

func (a *ui) showPlan() {
	if a.plan == nil || a.mode != modeDetail {
		return
	}
	p := a.plan
	_, installed := a.installedPath(p.Hack)
	newer, outdated := a.updates.NeedsUpdate(p.Hack.GameID)

	// Another hack being patched right now: A stays disabled, and the
	// card says so. Before this the page still read "Press A to patch"
	// and the button silently did nothing.
	if _, mine := a.running.state(p.Hack.GameID); !mine && a.running.busy() {
		sdlui.DetailStatus.Text = "Patching " + a.running.otherTitle() +
			" — this one unlocks when that finishes."
		sdlui.DetailStatus.Level = sdlui.StatusWarn
		sdlui.DetailFacts = a.detailFacts(p)
		a.detail.Game.CanDownload = false
		return
	}

	switch {
	case p.RegionWrong:
		// Say it before A is pressed. Finding out from a checksum
		// mismatch after a 600 MB extraction is a waste of the user's
		// time, and the hex pair does not name the file to go and find.
		sdlui.DetailStatus.Text = p.Region.Message()
		sdlui.DetailStatus.Level = sdlui.StatusWarn
	case installed && outdated:
		// This is the update button the header was only hinting at.
		sdlui.DetailStatus.Text = "A newer version exists — press A to update to " + newer + "."
		sdlui.DetailStatus.Level = sdlui.StatusWarn
	case installed:
		sdlui.DetailStatus.Text = "Installed and verified. Y plays it, A patches again."
		sdlui.DetailStatus.Level = sdlui.StatusOK
	default:
		sdlui.DetailStatus.Text = "Ready. Press A to patch your copy of " + p.Base.Name() + "."
		sdlui.DetailStatus.Level = 0
	}
	sdlui.DetailFacts = a.detailFacts(p)

	// Only now does A have something to do, which is why the model is
	// told here rather than when the page opened. An install already
	// running keeps A disabled: pressing it would start a second one.
	if status, running := a.running.state(p.Hack.GameID); running {
		sdlui.DetailStatus.Text = "Patching in progress — leave it running."
		sdlui.DetailStatus.Level = 0
		a.downloadStatus = status
		return
	}
	a.detail.Game.CanDownload = true
	if a.pendingInstall == p.Hack.GameID {
		a.pendingInstall = 0
		a.startInstall()
		return
	}
	a.detailFiles = []string{
		p.Patch.Stem() + " (" + p.Format.String() + ")",
		"from " + p.Base.Name(),
		"to " + shortPath(catalog.HacksDir(romsRoot, p.Base.System)),
	}
	a.downloadStatus = ""
}

func (a *ui) showResolveError(err error) {
	if a.mode != modeDetail {
		return
	}
	var missing *catalog.ErrNoBaseROM
	if errors.As(err, &missing) {
		sdlui.DetailStatus.Text = capitalise(err.Error())
		sdlui.DetailStatus.Level = sdlui.StatusWarn
		facts := [][2]string{{"Hack of", a.current.BaseTitle}}
		for _, w := range missing.Wanted {
			facts = append(facts, [2]string{"Needs", w})
		}
		for _, n := range missing.Near {
			facts = append(facts, [2]string{"You have", n})
		}
		sdlui.DetailFacts = facts
		return
	}
	sdlui.DetailStatus.Text = wrapError(err)
	sdlui.DetailStatus.Level = sdlui.StatusBad
}

// --- small helpers ----------------------------------------------------

func (a *ui) setNotice(msg string) {
	// Every line here is shown as a sentence to the user, and half of
	// them arrive from errors and progress callbacks that start
	// lower-case. Capitalising once at the door beats remembering at
	// every call site.
	a.notice = capitalise(msg)
	a.expires = time.Now().Add(6 * time.Second)
}

// capitalise raises the first letter, leaving anything that is already a
// name, a path or a number alone.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if !unicode.IsLower(r[0]) {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func wrapError(err error) string {
	if err == nil {
		return ""
	}
	return capitalise(err.Error())
}

func shortPath(p string) string {
	if strings.HasPrefix(p, romsRoot+"/") {
		return p[len(romsRoot)+1:]
	}
	return p
}

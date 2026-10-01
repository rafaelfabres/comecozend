package appui

// FilterSection identifies the editable row in the filter screen.
type FilterSection uint8

const (
	FilterSearch FilterSection = iota
	FilterPlatform
	// FilterGenre: RetroAchievements' genre of the game (values set by the app).
	FilterGenre
	FilterSort
	// FilterShow narrows the list by the game's state (installed, price,
	// itch.io availability). It replaced the itch.io-era 18+ row.
	FilterShow
)

var FilterPlatforms = []string{"", "GB", "GBC", "GBA", "NES", "MD", "P8", "PSX"}
var FilterPlatformLabels = []string{"All platforms", "GB", "GBC", "GBA", "NES", "Mega Drive", "Pico-8", "PlayStation"}

// FilterGenres are set by the app from the genres in the hub ("" = all).
var FilterGenres = []string{""}
var FilterGenreLabels = []string{"All genres"}

// Orders for the RetroAchievements hub catalog. "" keeps the hub's own
// order (grouped by system).
var FilterSortValues = []string{"", "new", "players", "unlocks", "az", "za", "most", "fewest", "free", "cheap"}
var FilterSortLabels = []string{"By system", "Recently updated on RA", "Most played (RA)", "Most unlocks (RA)", "A-Z", "Z-A",
	"Most achievements", "Fewest achievements", "Free first", "Cheapest paid first"}

// FilterShowValues narrow the list by state.
var FilterShowValues = []string{"all", "new", "mastered", "notmastered", "played", "nodemos", "demos", "installed",
	"notinstalled", "free", "paid", "owned", "verifiable", "notfound"}
var FilterShowLabels = []string{"All games", "Updated in the last 60 days", "Mastered by me", "Not mastered", "Played by me",
	"Full games (no demos)", "Demos only", "Installed", "Not installed", "Free", "Paid", "Bought on itch.io",
	"Can be verified", "Not found on itch.io"}

type FilterIntent uint8

const (
	FilterIntentNone FilterIntent = iota
	FilterIntentEditSearch
	FilterIntentApply
	FilterIntentCancel
)

// FilterModel owns a staged filter edit. Apply is the only intent that commits
// its values to the list; Cancel leaves the active list unchanged.
type FilterModel struct {
	Section  FilterSection
	Platform string
	Sort     string
	Query    string
	Show     string
	Genre    string
	plat     int
	sort     int
	show     int
	genre    int
}

func NewFilterModel(platform, sort, query, show, genre string) *FilterModel {
	if show == "" {
		show = FilterShowValues[0]
	}
	m := &FilterModel{Platform: platform, Sort: sort, Query: query, Show: show, Genre: genre}
	m.genre = indexOf(FilterGenres, genre)
	m.plat = indexOf(FilterPlatforms, platform)
	m.sort = indexOf(FilterSortValues, sort)
	m.show = indexOf(FilterShowValues, show)
	return m
}

func (m *FilterModel) PlatformLabel() string { return FilterPlatformLabels[m.plat] }
func (m *FilterModel) SortLabel() string     { return FilterSortLabels[m.sort] }
func (m *FilterModel) ShowLabel() string     { return FilterShowLabels[m.show] }
func (m *FilterModel) GenreLabel() string {
	if m.genre < len(FilterGenreLabels) {
		return FilterGenreLabels[m.genre]
	}
	return "All genres"
}

// Clear resets to the default view: every hub game, in hub order.
func (m *FilterModel) Clear() {
	m.Platform, m.Query = "", ""
	m.Sort, m.Show, m.Genre = "", "all", ""
	m.plat, m.sort, m.show, m.genre = 0, 0, 0, 0
}

func (m *FilterModel) Handle(event InputEvent) FilterIntent {
	if !event.Pressed {
		return FilterIntentNone
	}
	switch event.Button {
	case ButtonUp:
		if m.Section > FilterSearch {
			m.Section--
		}
	case ButtonDown:
		if m.Section < FilterShow {
			m.Section++
		}
	case ButtonLeft:
		m.cycle(-1)
	case ButtonRight:
		m.cycle(1)
	case ButtonA:
		if m.Section == FilterSearch {
			return FilterIntentEditSearch
		}
		m.cycle(1)
	case ButtonB, ButtonQuit:
		return FilterIntentCancel
	case ButtonSelect:
		return FilterIntentApply
	case ButtonY:
		m.Clear()
	}
	return FilterIntentNone
}

func (m *FilterModel) cycle(delta int) {
	switch m.Section {
	case FilterPlatform:
		m.plat = wrap(m.plat+delta, len(FilterPlatforms))
		m.Platform = FilterPlatforms[m.plat]
	case FilterGenre:
		m.genre = wrap(m.genre+delta, len(FilterGenres))
		m.Genre = FilterGenres[m.genre]
	case FilterSort:
		m.sort = wrap(m.sort+delta, len(FilterSortValues))
		m.Sort = FilterSortValues[m.sort]
	case FilterShow:
		m.show = wrap(m.show+delta, len(FilterShowValues))
		m.Show = FilterShowValues[m.show]
	}
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return 0
}

func wrap(value, length int) int {
	if length == 0 {
		return 0
	}
	value %= length
	if value < 0 {
		value += length
	}
	return value
}

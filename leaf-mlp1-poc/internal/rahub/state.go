package rahub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Status is where a hub game is in the pipeline. Stored per game so an
// interrupted run continues where it stopped.
type Status string

const (
	StatusNew         Status = ""             // not processed yet
	StatusSearching   Status = "SEARCHING"    // looking on itch.io
	StatusCandidate   Status = "CANDIDATE"    // itch.io page(s) found, not verified yet
	StatusVerifying   Status = "VERIFYING"    // downloading / extracting / hashing
	StatusVerified    Status = "VERIFIED"     // hash matched and file installed
	StatusNoMatch     Status = "NO_MATCH"     // downloaded, but no file had an RA hash
	StatusNotFound    Status = "NOT_FOUND"    // no plausible itch.io page
	StatusNoHashes    Status = "NO_HASHES"    // RA lists no hash for the game
	StatusUnsupported Status = "UNSUPPORTED"  // disc system: hash not computable here
	StatusError       Status = "ERROR"        // network or disk error; retried next run
	StatusPaid        Status = "PAID"         // only paid pages, not in the user's itch.io purchases
	StatusUnverified  Status = "UNVERIFIED"   // installed at the user's request WITHOUT a hash match
	StatusDemoGone    Status = "DEMO_GONE"    // RA has the demo; itch.io now only sells the full game
	StatusVersionGone Status = "VERSION_GONE" // RA's version of the file is no longer offered (only newer ones)
	StatusOutdated    Status = "OUTDATED"     // installed file no longer accepted by RA (it moved to another version)
)

// GameState is everything remembered about one hub game.
type GameState struct {
	Status     Status      `json:"status"`
	Candidates []Candidate `json:"candidates,omitempty"`
	ItchURL    string      `json:"itch_url,omitempty"`   // the page that produced the verified file
	ItchTitle  string      `json:"itch_title,omitempty"` // for display
	ItchAuthor string      `json:"itch_author,omitempty"`
	CoverURL   string      `json:"cover_url,omitempty"`
	Override   string      `json:"override,omitempty"` // itch.io URL set by hand; always tried first
	Developer  string      `json:"developer,omitempty"`
	// DevHints are developer names used to search: RA's developer and
	// publisher, plus names found in RA's hash file names.
	DevHints  []string `json:"dev_hints,omitempty"`
	HintsDone bool     `json:"hints_done,omitempty"`
	// BroadDone is set once the wide search (developer pages, more
	// queries) ran; it is not repeated unless forced.
	BroadDone bool `json:"broad_done,omitempty"`
	// ItchDesc is the itch.io page description (plain text), cached so the
	// list never has to fetch it again; DescDone marks it as looked up.
	ItchDesc string `json:"itch_desc,omitempty"`
	DescDone bool   `json:"desc_done,omitempty"`
	// SearchVer is the SearchVersion of the last wide lookup.
	SearchVer int `json:"search_ver,omitempty"`
	// AltTitles are other names for the game, from RA's hash file names
	// ("Casanova (World) (Aftermarket).md" for "Mega Casanova").
	AltTitles []string `json:"alt_titles,omitempty"`
	// HashNames are RA's file names for the game's hashes; an upload with
	// the same name is downloaded first.
	HashNames []string `json:"hash_names,omitempty"`
	// Hidden games are left out of the list, the counts and the background
	// scan. Only Settings > Hidden games brings them back.
	Hidden bool `json:"hidden,omitempty"`
	// Your RetroAchievements progress with this game.
	Progress Progress `json:"progress,omitempty"`
	// Demo games (RA set is for a demo): the paid full release, if the
	// developer sells one, and whether a free demo file is still offered
	// (0 = not checked, 1 = yes, 2 = no).
	FullURL   string `json:"full_url,omitempty"`
	FullPrice string `json:"full_price,omitempty"`
	DemoFree  int    `json:"demo_free,omitempty"`
	// RAVersions / ItchVersions: versions named in RA's hash files and in
	// what itch.io offers, when RA's version is gone (e.g. 1.0.6 vs 1.0.7).
	RAVersions   []string `json:"ra_versions,omitempty"`
	ItchVersions []string `json:"itch_versions,omitempty"`
	// TriedUploads are "pageURL#uploadID" already downloaded without a
	// match, so a re-run does not fetch them again unless forced.
	TriedUploads  []string  `json:"tried_uploads,omitempty"`
	MD5           string    `json:"md5,omitempty"`
	InstalledPath string    `json:"installed_path,omitempty"`
	NeedsPatch    string    `json:"needs_patch,omitempty"` // PatchUrl when the matching hash is a patched one
	Note          string    `json:"note,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Store holds the hub catalog and per-game state, persisted as JSON in the
// app's data directory. JSON rather than SQLite keeps the binary pure Go
// (no cgo sqlite to cross-compile) and a few hundred games is tiny.
type Store struct {
	mu        sync.Mutex
	path      string
	HubID     int                   `json:"hub_id"`
	FetchedAt time.Time             `json:"fetched_at"`
	Games     []HubGame             `json:"games"`
	States    map[string]*GameState `json:"states"` // key: RA game ID
	// HiddenConsoles are RA console IDs the user hid (Settings > Hidden
	// systems): their games are left out of everything.
	HiddenConsoles []int `json:"hidden_consoles,omitempty"`

	index map[int]int // game ID -> position in Games (rebuilt on change)
}

func (s *Store) reindexLocked() {
	s.index = make(map[int]int, len(s.Games))
	for i, g := range s.Games {
		s.index[g.ID] = i
	}
}

// ConsoleHidden reports whether a console is hidden.
func (s *Store) ConsoleHidden(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consoleHiddenLocked(id)
}

func (s *Store) consoleHiddenLocked(id int) bool {
	for _, h := range s.HiddenConsoles {
		if h == id {
			return true
		}
	}
	return false
}

// SetConsoleHidden hides or shows a whole console.
func (s *Store) SetConsoleHidden(id int, hidden bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.HiddenConsoles[:0]
	for _, h := range s.HiddenConsoles {
		if h != id {
			kept = append(kept, h)
		}
	}
	if hidden {
		kept = append(kept, id)
	}
	s.HiddenConsoles = kept
}

// Visible reports whether a game is shown: not hidden itself and not on a
// hidden console.
func (s *Store) Visible(g HubGame) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consoleHiddenLocked(g.ConsoleID) {
		return false
	}
	st := s.States[strconv.Itoa(g.ID)]
	return st == nil || !st.Hidden
}

// OpenStore loads (or starts) the store for a hub.
func OpenStore(dataDir string, hubID int) (*Store, error) {
	s := &Store{
		path:   filepath.Join(dataDir, fmt.Sprintf("ra-hub-%d.json", hubID)),
		HubID:  hubID,
		States: map[string]*GameState{},
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		// A corrupt file must not block the app: keep a copy, start over.
		_ = os.Rename(s.path, s.path+".corrupt")
		s.Games, s.States = nil, map[string]*GameState{}
		return s, fmt.Errorf("hub state unreadable, starting fresh: %w", err)
	}
	if s.States == nil {
		s.States = map[string]*GameState{}
	}
	return s, nil
}

// Path is where the store lives.
func (s *Store) Path() string { return s.path }

// Save writes atomically (temp file + rename), so a power-off mid-write
// never loses the whole state.
func (s *Store) Save() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s, "", " ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// SetGames replaces the catalog with a fresh hub fetch. State for games that
// are still in the hub is kept; a verified game whose hash list changed so
// that its installed MD5 is no longer accepted is reset for re-checking.
func (s *Store) SetGames(games []HubGame) (added, changed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := map[int]HubGame{}
	for _, g := range s.Games {
		old[g.ID] = g
	}
	for i, g := range games {
		prev, existed := old[g.ID]
		if !existed {
			added++
			continue
		}
		// Popularity comes from a separate, slower lookup: keep it.
		if g.Players == 0 && g.Unlocks == 0 {
			games[i].Players, games[i].Unlocks, games[i].StatsAt = prev.Players, prev.Unlocks, prev.StatsAt
		}
		if g.Genre == "" {
			games[i].Genre = prev.Genre
		}
		if g.SetAdded.IsZero() {
			games[i].SetAdded = prev.SetAdded
		}
		if g.Released.IsZero() {
			games[i].Released = prev.Released
		}
		if g.Updated.IsZero() {
			games[i].Updated = prev.Updated
		}
		if fmt.Sprint(prev.Hashes) != fmt.Sprint(g.Hashes) {
			changed++
			st := s.States[strconv.Itoa(g.ID)]
			if st != nil && st.MD5 != "" && !g.HasHash(st.MD5) && st.Status == StatusVerified {
				// The installed file is not accepted any more: RA moved to
				// another version. Keep the file (it still plays) and say so.
				st.Status, st.Note = StatusOutdated, "RetroAchievements no longer accepts the installed file"
				st.TriedUploads = nil
			}
			if st == nil {
				continue
			}
			switch st.Status {
			case StatusNoMatch, StatusNoHashes, StatusVersionGone, StatusDemoGone:
				// RA accepts other files now (e.g. v1.0.7 after v1.0.6):
				// every file is worth another try, and RA's file names
				// (with their versions) are read again.
				st.Status, st.Note = StatusNew, "RetroAchievements added new hashes"
				st.TriedUploads, st.RAVersions, st.ItchVersions = nil, nil, nil
				st.DemoFree, st.HintsDone, st.HashNames = 0, false, nil
			case StatusUnverified:
				// A file installed without a match may be the one RA
				// accepts now.
				if st.InstalledPath != "" {
					if h, err := HashFile(g.Console(), st.InstalledPath); err == nil && g.HasHash(h) {
						st.Status, st.MD5, st.Note = StatusVerified, h, "verified after RetroAchievements added its hash"
					}
				}
			}
		}
	}
	s.Games = games
	s.FetchedAt = time.Now()
	s.reindexLocked()
	return added, changed
}

// GamesCopy returns a snapshot of the catalog.
func (s *Store) GamesCopy() []HubGame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HubGame(nil), s.Games...)
}

// Game returns one catalog entry.
func (s *Store) Game(id int) (HubGame, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index == nil || len(s.index) != len(s.Games) {
		s.reindexLocked()
	}
	if i, ok := s.index[id]; ok && i < len(s.Games) && s.Games[i].ID == id {
		return s.Games[i], true
	}
	return HubGame{}, false
}

// UpdateGame replaces one catalog entry (e.g. after fetching patch info).
func (s *Store) UpdateGame(g HubGame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Games {
		if s.Games[i].ID == g.ID {
			s.Games[i] = g
			return
		}
	}
}

// State returns a COPY of a game's state (never nil).
func (s *Store) State(id int) GameState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.States[strconv.Itoa(id)]; st != nil {
		cp := *st
		cp.Candidates = append([]Candidate(nil), st.Candidates...)
		cp.TriedUploads = append([]string(nil), st.TriedUploads...)
		cp.DevHints = append([]string(nil), st.DevHints...)
		cp.AltTitles = append([]string(nil), st.AltTitles...)
		return cp
	}
	return GameState{}
}

// Update mutates a game's state under the lock and stamps it.
func (s *Store) Update(id int, fn func(*GameState)) {
	s.mu.Lock()
	key := strconv.Itoa(id)
	st := s.States[key]
	if st == nil {
		st = &GameState{}
		s.States[key] = st
	}
	fn(st)
	st.UpdatedAt = time.Now()
	s.mu.Unlock()
}

// Counts summarises statuses for the final report.
func (s *Store) Counts() map[Status]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[Status]int{}
	for _, g := range s.Games {
		st := s.States[strconv.Itoa(g.ID)]
		if (st != nil && st.Hidden) || s.consoleHiddenLocked(g.ConsoleID) {
			continue
		}
		if st == nil {
			out[StatusNew]++
		} else {
			out[st.Status]++
		}
	}
	return out
}

// ConsoleNames lists the consoles present, sorted by name.
func (s *Store) ConsoleNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, g := range s.Games {
		set[g.ConsoleName] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ResetItchData forgets every itch.io lookup for games that are not
// installed, so the background scan finds them again (Settings > Re-scan).
// Manual overrides and verified installs are kept.
func (s *Store) ResetItchData() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, st := range s.States {
		if st.Status == StatusVerified {
			continue
		}
		st.Candidates, st.TriedUploads, st.DevHints = nil, nil, nil
		st.HintsDone, st.BroadDone, st.DescDone, st.SearchVer = false, false, false, 0
		st.ItchDesc, st.CoverURL, st.Note = "", "", ""
		if st.Status != StatusUnsupported {
			st.Status = StatusNew
		}
		n++
	}
	return n
}

// SetProgress records the user's RetroAchievements progress per game.
func (s *Store) SetProgress(p map[int]Progress) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := 0
	for _, g := range s.Games {
		pr, ok := p[g.ID]
		if !ok {
			continue
		}
		st := s.States[strconv.Itoa(g.ID)]
		if st == nil {
			st = &GameState{}
			s.States[strconv.Itoa(g.ID)] = st
		}
		if st.Progress != pr {
			st.Progress = pr
			changed++
		}
	}
	return changed
}

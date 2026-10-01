package catalog

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// Record is one hack this app installed.
type Record struct {
	GameID    int       `json:"game_id"`
	Path      string    `json:"path"`
	Stem      string    `json:"stem"`
	RAHash    string    `json:"ra_hash,omitempty"`
	PatchFile string    `json:"patch_file,omitempty"`
	When      time.Time `json:"when"`
}

// Registry remembers what was installed, by game ID.
//
// Matching a hack to a file by title does not work, and that is why this
// exists. RetroAchievements titles a set "Pokémon Emerald Rogue V2" while
// the file it expects is "Pokemon Emerald - Emerald Rogue (v2.0).gba" —
// the same hack under two names that no amount of normalising will bring
// together. Writing down the game ID at install time removes the guessing
// entirely.
type Registry struct {
	mu     sync.RWMutex
	byGame map[int]Record
	path   string
}

func LoadRegistry(path string) *Registry {
	r := &Registry{byGame: map[int]Record{}, path: path}
	if b, err := os.ReadFile(path); err == nil {
		var records []Record
		if json.Unmarshal(b, &records) == nil {
			for _, rec := range records {
				r.byGame[rec.GameID] = rec
			}
		}
	}
	return r
}

// Get returns the record for a game, if this app installed it. A nil
// registry answers "no", so callers constructed without one still work.
func (r *Registry) Get(gameID int) (Record, bool) {
	if r == nil {
		return Record{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.byGame[gameID]
	return rec, ok
}

// Add records an install and saves immediately: a record that only exists
// in memory is worthless if the app is closed from the menu.
func (r *Registry) Add(rec Record) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	rec.When = time.Now()
	r.byGame[rec.GameID] = rec
	r.mu.Unlock()
	return r.Save()
}

// Remove forgets a game, for when its file is deleted.
func (r *Registry) Remove(gameID int) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	delete(r.byGame, gameID)
	r.mu.Unlock()
	return r.Save()
}

// Prune drops records whose file is no longer on the card — deleted from
// a file manager, or living on an SD card that is not in the device right
// now. Returns how many went.
func (r *Registry) Prune() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	var gone []int
	for id, rec := range r.byGame {
		if _, err := os.Stat(rec.Path); err != nil {
			gone = append(gone, id)
		}
	}
	for _, id := range gone {
		delete(r.byGame, id)
	}
	r.mu.Unlock()
	if len(gone) > 0 {
		_ = r.Save()
	}
	return len(gone)
}

// All returns every record, newest first.
func (r *Registry) All() []Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	out := make([]Record, 0, len(r.byGame))
	for _, rec := range r.byGame {
		out = append(out, rec)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	return out
}

func (r *Registry) Save() error {
	records := r.All()
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

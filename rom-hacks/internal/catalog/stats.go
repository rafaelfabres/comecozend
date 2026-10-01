package catalog

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"leaf-hacks/internal/rahub"
)

// Stats is what RetroAchievements knows about how much a set is played.
//
// Unlike the achievement count, none of this comes with the console game
// list: it needs one call per game. A couple of thousand calls up front
// would be rude to the API and slow for the user, so the numbers are
// filled in gradually in the background and kept between launches.
type Stats struct {
	Players int       `json:"players"`
	Unlocks int       `json:"unlocks"`
	Genre   string    `json:"genre,omitempty"`
	Fetched time.Time `json:"fetched"`
}

// StatsCache is the persisted store, safe for the background filler to
// write while the UI reads.
type StatsCache struct {
	mu   sync.RWMutex
	byID map[int]Stats
	path string
	// dirty counts writes since the last save, so the file is not
	// rewritten once per game.
	dirty int
}

func NewStatsCache(path string) *StatsCache {
	c := &StatsCache{byID: map[int]Stats{}, path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.byID)
	}
	return c
}

func (c *StatsCache) Get(gameID int) (Stats, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.byID[gameID]
	return s, ok
}

func (c *StatsCache) set(gameID int, s Stats) {
	c.mu.Lock()
	c.byID[gameID] = s
	c.dirty++
	should := c.dirty >= 25
	if should {
		c.dirty = 0
	}
	c.mu.Unlock()
	if should {
		_ = c.Save()
	}
}

func (c *StatsCache) Save() error {
	c.mu.RLock()
	b, err := json.Marshal(c.byID)
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Count reports how many games have numbers, for the status line.
func (c *StatsCache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byID)
}

// statsMaxAge is how long a set's popularity is trusted. Player counts
// drift slowly; a month-old number still sorts a list correctly.
const statsMaxAge = 30 * 24 * time.Hour

// Fill fetches the missing numbers, one game at a time, pausing between
// calls. It is meant to run for the whole session in the background: it
// reports progress through note and stops when ctx is cancelled.
//
// The pause matters. The RetroAchievements API is a volunteer-run service
// and this walks thousands of games; going flat out would be the kind of
// client that gets an app blocked.
func (c *StatsCache) Fill(ctx context.Context, ra *rahub.RAClient, hacks []Hack, note func(done, total int)) {
	var todo []int
	for _, h := range hacks {
		s, ok := c.Get(h.GameID)
		if ok && time.Since(s.Fetched) < statsMaxAge {
			continue
		}
		todo = append(todo, h.GameID)
	}
	for i, id := range todo {
		select {
		case <-ctx.Done():
			_ = c.Save()
			return
		default:
		}
		players, unlocks, genre, err := ra.FetchGameStats(ctx, id)
		if err == nil {
			c.set(id, Stats{Players: players, Unlocks: unlocks, Genre: genre, Fetched: time.Now()})
			if note != nil {
				note(i+1, len(todo))
			}
		}
		select {
		case <-ctx.Done():
			_ = c.Save()
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	_ = c.Save()
}

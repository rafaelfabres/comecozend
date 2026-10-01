package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"leaf-mlp1-poc/internal/itchio"
)

// noROMList remembers games whose itch.io page was checked and found to offer
// nothing this console can run — a Windows-only build, a browser game, a 3DS
// .cia, and so on.
//
// This exists because the catalog cannot know: itch.io's RSS feeds carry no
// platform information at all (title, link, description, price, date — that is
// the whole item), so "is there a ROM behind this?" is only answerable by
// opening the game's page. Recording the answer the first time turns that one
// wasted download attempt into a permanent improvement to the list.
type noROMList struct {
	mu    sync.Mutex
	URLs  map[string]string `json:"urls"` // game URL -> what the page offered instead
	dirty bool
}

func noROMPath() string { return filepath.Join(dataDir(), "no-rom.json") }

func loadNoROM() *noROMList {
	list := &noROMList{URLs: map[string]string{}}
	data, err := os.ReadFile(noROMPath())
	if err != nil {
		return list
	}
	if err := json.Unmarshal(data, list); err != nil {
		fmt.Fprintln(os.Stderr, "no-rom list unreadable, starting fresh:", err)
		list.URLs = map[string]string{}
	}
	if list.URLs == nil {
		list.URLs = map[string]string{}
	}
	return list
}

func (n *noROMList) has(url string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.URLs[url]
	return ok
}

func (n *noROMList) add(url, reason string) {
	if url == "" {
		return
	}
	n.mu.Lock()
	n.URLs[url] = reason
	n.dirty = true
	n.mu.Unlock()
	if err := n.save(); err != nil {
		fmt.Fprintln(os.Stderr, "could not save the no-rom list:", err)
	}
}

func (n *noROMList) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.URLs)
}

// clear forgets every entry, so a game that has since published a ROM gets
// another chance.
func (n *noROMList) clear() error {
	n.mu.Lock()
	n.URLs = map[string]string{}
	n.mu.Unlock()
	return n.save()
}

func (n *noROMList) save() error {
	n.mu.Lock()
	payload := struct {
		URLs    map[string]string `json:"urls"`
		SavedAt time.Time         `json:"saved_at"`
	}{URLs: n.URLs, SavedAt: time.Now()}
	n.mu.Unlock()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(noROMPath(), data, 0o644)
}

// filterGamesWithROM drops games already known to have no usable ROM.
func filterGamesWithROM(games []itchio.Game, noROM *noROMList) []itchio.Game {
	if noROM == nil || noROM.count() == 0 {
		return games
	}
	out := make([]itchio.Game, 0, len(games))
	for _, g := range games {
		if noROM.has(g.URL) {
			continue
		}
		out = append(out, g)
	}
	return out
}

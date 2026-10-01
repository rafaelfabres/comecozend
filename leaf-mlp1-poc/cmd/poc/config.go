package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// config is the app's persisted settings. It lives beside the catalog cache
// and the inventory, outside the app folder, so an upgrade does not log the
// user out.
type config struct {
	APIKey string `json:"api_key,omitempty"`
	// StrictROM restricts the catalog to tags that imply an actual ROM file,
	// rather than tags describing a console as a theme. Stored inverted so
	// the zero value (a fresh config) means strict — ROMs are the point.
	LooseTags bool `json:"loose_tags,omitempty"`
	// PopularCatalog fetches feeds in itch.io popularity order instead of
	// newest-first, so the list opens with the best-regarded games.
	PopularCatalog bool `json:"popular_catalog,omitempty"`
	// AdultContent adds itch.io's adult-tagged feeds to the catalog walk.
	// Off by default; itch.io itself keeps this content out of anonymous
	// browsing, so it is only reachable through those explicit tags.
	AdultContent bool `json:"adult_content,omitempty"`
	// RetroAchievements login for the hub catalog. RA_USER / RA_KEY in the
	// environment take precedence; the key is never printed unmasked.
	RAUser string `json:"ra_user,omitempty"`
	RAKey  string `json:"ra_key,omitempty"`
	// HubID is the RetroAchievements hub the catalog is built from
	// (0 = the default, 3036). RA_HUB_ID overrides it.
	HubID int `json:"hub_id,omitempty"`

	mu sync.Mutex
}

func configPath() string { return filepath.Join(dataDir(), "config.json") }

func loadConfig() *config {
	cfg := &config{}
	data, err := os.ReadFile(configPath())
	if err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "config is unreadable, starting fresh:", err)
		}
	}
	// An environment variable wins over the stored value, which makes
	// scripted/terminal login possible without touching the UI:
	//   ITCHIO_API_KEY=xxxx ./leaf-mlp1-poc-arm64
	if env := strings.TrimSpace(os.Getenv("ITCHIO_API_KEY")); env != "" {
		cfg.APIKey = env
	}
	return cfg
}

func (c *config) key() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.APIKey
}

// strictROM reports whether the catalog should stick to ROM-format tags.
func (c *config) strictROM() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.LooseTags
}

func (c *config) setStrictROM(strict bool) error {
	c.mu.Lock()
	c.LooseTags = !strict
	c.mu.Unlock()
	return c.save()
}

// catalogSort is the itch.io feed sort: "popular" or "" (newest-first).
func (c *config) catalogSort() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.PopularCatalog {
		return "popular"
	}
	return ""
}

func (c *config) setPopular(on bool) error {
	c.mu.Lock()
	c.PopularCatalog = on
	c.mu.Unlock()
	return c.save()
}

func (c *config) adultEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.AdultContent
}

func (c *config) setAdult(on bool) error {
	c.mu.Lock()
	c.AdultContent = on
	c.mu.Unlock()
	return c.save()
}

func (c *config) setKey(key string) error {
	c.mu.Lock()
	c.APIKey = strings.TrimSpace(key)
	c.mu.Unlock()
	return c.save()
}

func (c *config) save() error {
	c.mu.Lock()
	data, err := json.MarshalIndent(struct {
		APIKey         string `json:"api_key,omitempty"`
		AdultContent   bool   `json:"adult_content,omitempty"`
		LooseTags      bool   `json:"loose_tags,omitempty"`
		PopularCatalog bool   `json:"popular_catalog,omitempty"`
		RAUser         string `json:"ra_user,omitempty"`
		RAKey          string `json:"ra_key,omitempty"`
		HubID          int    `json:"hub_id,omitempty"`
	}{APIKey: c.APIKey, AdultContent: c.AdultContent, LooseTags: c.LooseTags, PopularCatalog: c.PopularCatalog,
		RAUser: c.RAUser, RAKey: c.RAKey, HubID: c.HubID}, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	// 0600: the key is a credential, and the SD card may be shared.
	return os.WriteFile(configPath(), data, 0o600)
}

// maskedKey renders a key for display without exposing it, mirroring how
// Leaf's settings screen shows it.
func maskedKey(key string) string {
	if key == "" {
		return "not signed in"
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

// handleTerminalLogin implements the command-line login paths, so a key can
// be set without typing it on an on-screen keyboard:
//
//	./leaf-mlp1-poc-arm64 --login KEY   store a key and exit
//	./leaf-mlp1-poc-arm64 --logout      forget the stored key and exit
//	./leaf-mlp1-poc-arm64 --whoami      print the stored key's account and exit
//
// Returns true when the program should exit without starting the UI.
func handleTerminalLogin(cfg *config, args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "--login":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: --login <itch.io API key>")
			fmt.Fprintln(os.Stderr, "get one at https://itch.io/user/settings/api-keys")
			return true
		}
		if err := cfg.setKey(args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "could not save the key:", err)
			return true
		}
		fmt.Println("key saved to", configPath())
		verifyAndReport(cfg.key())
		return true
	case "--logout":
		if err := cfg.setKey(""); err != nil {
			fmt.Fprintln(os.Stderr, "could not clear the key:", err)
			return true
		}
		fmt.Println("signed out")
		return true
	case "--whoami":
		if cfg.key() == "" {
			fmt.Println("not signed in")
			return true
		}
		verifyAndReport(cfg.key())
		return true
	}
	return false
}

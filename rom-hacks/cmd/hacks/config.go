package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"leaf-hacks/internal/rahub"
)

// romsRoot is where dArkOS/EmulationStation keeps ROMs on the Miniloong.
// romsRoot is where the system folders live. A var only so tests can point
// it at a temporary tree.
var romsRoot = "/roms"

// Config is the small amount of state this app owns. The only thing it
// really needs is a RetroAchievements web API key, which the user copies
// from their RA control panel.
type Config struct {
	RAUser string `json:"ra_user"`
	RAKey  string `json:"ra_key"`

	// IncludeTranslations widens the list beyond hacks. Off by default:
	// the app is called "ROM Hacks" and translations are a different
	// appetite, but the plumbing is identical so it costs one flag.
	IncludeTranslations bool `json:"include_translations"`
}

func (c Config) credentials() rahub.Credentials {
	return rahub.Credentials{User: c.RAUser, Key: c.RAKey}
}

// appDir is the folder the binary lives in. Everything this app writes —
// caches, config, logs — stays beside it rather than in a home directory,
// so moving the SD card to another device moves the whole app with it.
func appDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	dir := filepath.Dir(exe)
	// When launched from launch.sh the binary sits in ./bin.
	if filepath.Base(dir) == "bin" {
		dir = filepath.Dir(dir)
	}
	return dir
}

// dataDir is where this app's own state lives: config, caches, logs.
//
// Deliberately NOT inside the app folder. Upgrading means deleting the old
// directory and unpacking a new one, and the RetroAchievements key, the
// ROM fingerprints and the built catalog should all survive that.
func dataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".local", "share")
		}
	}
	if base == "" {
		base = appDir() // no home directory: fall back to beside the binary
	}
	dir := filepath.Join(base, "rom-hacks")
	os.MkdirAll(dir, 0o755)
	return dir
}

// seedIndexPath is the patch listing shipped inside the app folder. It is
// read-only and only used until the first refresh writes a fresh one into
// dataDir, which is why it is a separate path.
func seedIndexPath() string {
	return filepath.Join(appDir(), "data", "rapatches-index.json")
}

func configPath() string   { return filepath.Join(dataDir(), "config.json") }
func indexPath() string    { return filepath.Join(dataDir(), "rapatches-index.json") }
func libraryPath() string  { return filepath.Join(dataDir(), "rom-library.json") }
func catalogPath() string  { return filepath.Join(dataDir(), "catalog.json") }
func updatesPath() string  { return filepath.Join(dataDir(), "updates.json") }
func statsPath() string    { return filepath.Join(dataDir(), "stats.json") }
func registryPath() string { return filepath.Join(dataDir(), "installed.json") }
func factsPath() string    { return filepath.Join(dataDir(), "patch-facts.json") }

// stagingDir is where large patch archives are written while they are
// being read. It lives on the card rather than in /tmp, which on plenty
// of handheld builds is a tmpfs — writing there would put the file in
// the RAM the streaming exists to protect.
func stagingDir() string {
	dir := filepath.Join(romsRoot, ".rom-hacks-tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return os.TempDir()
	}
	return dir
}

func loadConfig() *Config {
	cfg := &Config{}
	b, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, cfg)
	return cfg
}

func (c *Config) save() error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), b, 0o600)
}

// handleCLI covers the headless commands. Setting the API key over SSH is
// far less painful than typing it on an on-screen keyboard, and --list
// makes the whole pipeline inspectable without opening the UI.
func handleCLI(cfg *Config, args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "--ra-login":
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: --ra-login <username> <web-api-key>")
			os.Exit(2)
		}
		cfg.RAUser, cfg.RAKey = args[2], args[3]
		if err := cfg.save(); err != nil {
			fmt.Fprintln(os.Stderr, "could not save:", err)
			os.Exit(1)
		}
		fmt.Println("saved RetroAchievements credentials for", cfg.RAUser)
		return true

	case "--whoami":
		if cfg.RAUser == "" {
			fmt.Println("no RetroAchievements account set (use --ra-login)")
		} else {
			fmt.Println("signed in as", cfg.RAUser)
		}
		return true

	case "--scan":
		runScanCLI(cfg)
		return true

	case "--refresh":
		runRefreshCLI(cfg)
		return true

	case "--list":
		runListCLI(cfg)
		return true

	case "--verify":
		runVerifyCLI(cfg)
		return true

	case "--bases":
		runBasesCLI(cfg, args[2:])
		return true

	case "--metadata":
		runMetadataCLI(cfg)
		return true

	case "--install":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: --install <RA game id>")
			os.Exit(2)
		}
		runInstallCLI(cfg, args[2])
		return true

	case "--help", "-h":
		fmt.Println(usage)
		return true
	}
	return false
}

const usage = `ROM Hacks for dArkOS — patches the ROMs you already have.

  --ra-login <user> <key>   save your RetroAchievements web API key
  --whoami                  show the saved account
  --scan                    fingerprint /roms and cache the result
  --refresh                 rescan, refresh the patch index, rebuild the list
  --list                    print the hacks available for your ROMs
  --bases [text]            show which of your ROMs were identified, and
                            how many hacks each one has
  --verify                  check every candidate patch against your ROMs by
                            checksum (slow once, cached forever)
  --metadata                rewrite the EmulationStation entries for every
                            installed hack (name, description, cover)
  --install <game id>       download, patch and install one hack
  (no arguments)            open the on-device interface`

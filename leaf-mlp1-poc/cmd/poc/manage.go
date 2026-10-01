package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"leaf-mlp1-poc/internal/appui"
	"leaf-mlp1-poc/internal/inventory"
	"leaf-mlp1-poc/internal/itchio"
)

// installedROM is one file this app downloaded, as recorded in the inventory.
type installedROM struct {
	gameURL string
	title   string
	system  string
	name    string
	path    string
	size    int64
	missing bool // recorded but no longer on disk
}

// installedFromInventory lists what THIS app installed, from Leaf's own
// inventory record — not by listing /roms, which would also show every ROM
// the user copied over by other means.
func installedFromInventory(inv *inventory.Inventory) []installedROM {
	var out []installedROM
	if inv == nil {
		return out
	}
	for _, gameURL := range inv.AllURLs() {
		entry, ok := inv.Lookup(gameURL)
		if !ok {
			continue
		}
		for _, file := range entry.Files {
			path := file.DestPath
			if path == "" {
				continue
			}
			rom := installedROM{
				gameURL: gameURL,
				title:   entry.Title,
				system:  file.CanonicalSystem,
				name:    file.InstalledName,
				path:    path,
			}
			if rom.name == "" {
				rom.name = filepath.Base(path)
			}
			if rom.system == "" {
				rom.system = filepath.Base(filepath.Dir(path))
			}
			if info, err := os.Stat(path); err == nil {
				rom.size = info.Size()
			} else {
				rom.missing = true
			}
			out = append(out, rom)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].system != out[j].system {
			return out[i].system < out[j].system
		}
		return strings.ToLower(out[i].name) < strings.ToLower(out[j].name)
	})
	return out
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// buildManageModel turns the inventory listing into the vendored ManageModel.
func buildManageModel(roms []installedROM, visibleRows int) *appui.ManageModel {
	model := appui.NewManageModel("Downloaded from itch.io")
	model.VisibleRows = visibleRows

	items := make([]appui.ManageItem, 0, len(roms))
	var total int64
	for i, rom := range roms {
		total += rom.size
		badge := formatSize(rom.size)
		if rom.missing {
			badge = "missing"
		}
		label := rom.title
		if label == "" {
			label = rom.name
		}
		items = append(items, appui.ManageItem{
			Kind:      appui.ManageItemFile,
			Label:     label,
			Detail:    platformLabelForFolder(rom.system) + "  ·  " + rom.name,
			Badge:     badge,
			FileIndex: i,
			Enabled:   true,
		})
	}

	subtitle := "Nothing downloaded through this app yet"
	if len(roms) > 0 {
		subtitle = fmt.Sprintf("%d file(s), %s total", len(roms), formatSize(total))
	}
	model.SetItems(subtitle, items)
	return model
}

// platformLabelForFolder turns a dArkOS folder name back into a readable
// console name, reusing the curated catalog instead of a second hardcoded list.
func platformLabelForFolder(folder string) string {
	for code, f := range systemFolderByCode {
		if f != folder {
			continue
		}
		for _, p := range itchio.AllPlatforms {
			if p.Code == code {
				return p.Name
			}
		}
	}
	return folder
}

// installedROMFor returns the installed file for a game URL, or nil.
func installedROMFor(inv *inventory.Inventory, gameURL string) *installedROM {
	for _, rom := range installedFromInventory(inv) {
		if rom.gameURL == gameURL && !rom.missing {
			found := rom
			return &found
		}
	}
	return nil
}

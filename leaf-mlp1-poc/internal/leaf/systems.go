package leaf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type System struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Patterns  []string `json:"patterns"`
	ROMRoot   string   `json:"rom_root"`
	ImageRoot string   `json:"image_root"`
}

type Catalog struct {
	Version  int      `json:"version"`
	Platform string   `json:"platform"`
	Systems  []System `json:"systems"`
	byID     map[string]System
}

var feedSystems = map[string]string{
	"GB": "GB", "GBC": "GBC", "GBA": "GBA",
	"NES": "FC", "MD": "MD", "P8": "PICO8", "PSX": "PS",
}

func LoadCatalog(env Environment) (*Catalog, error) {
	data, err := os.ReadFile(env.CatalogPath())
	if err != nil {
		return nil, fmt.Errorf("read Leaf systems catalog %s: %w", env.CatalogPath(), err)
	}
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("parse Leaf systems catalog: %w", err)
	}
	if catalog.Platform != "" && catalog.Platform != env.Platform {
		return nil, fmt.Errorf("systems catalog platform %q does not match %q", catalog.Platform, env.Platform)
	}
	catalog.byID = make(map[string]System, len(catalog.Systems))
	for _, system := range catalog.Systems {
		if system.ID == "" || system.ROMRoot == "" || system.ImageRoot == "" {
			return nil, fmt.Errorf("systems catalog contains incomplete system %q", system.ID)
		}
		if _, exists := catalog.byID[system.ID]; exists {
			return nil, fmt.Errorf("systems catalog contains duplicate id %q", system.ID)
		}
		catalog.byID[system.ID] = system
	}
	for _, id := range feedSystems {
		if _, ok := catalog.byID[id]; !ok {
			return nil, fmt.Errorf("systems catalog is missing required id %q", id)
		}
	}
	return &catalog, nil
}

func (c *Catalog) SystemForFeedCode(code string) (System, bool) {
	id, ok := feedSystems[strings.ToUpper(code)]
	if !ok {
		return System{}, false
	}
	system, ok := c.byID[id]
	return system, ok
}

func (c *Catalog) System(id string) (System, bool) {
	system, ok := c.byID[id]
	return system, ok
}

func contentSubdir(catalogRoot, prefix string) (string, error) {
	root := filepath.ToSlash(filepath.Clean(catalogRoot))
	prefix = strings.TrimSuffix(prefix, "/")
	if root == prefix {
		return ".", nil
	}
	if !strings.HasPrefix(root, prefix+"/") {
		return "", fmt.Errorf("catalog root %q is outside %s", catalogRoot, prefix)
	}
	return strings.TrimPrefix(root, prefix+"/"), nil
}

func (c *Catalog) ROMDir(source Source, canonicalID string) (string, error) {
	system, ok := c.byID[canonicalID]
	if !ok {
		return "", fmt.Errorf("unknown canonical system %q", canonicalID)
	}
	rel, err := contentSubdir(system.ROMRoot, "Roms")
	if err != nil {
		return "", err
	}
	return JoinWithin(source.RomsPath, rel)
}

func (c *Catalog) ImageDir(source Source, canonicalID string) (string, error) {
	system, ok := c.byID[canonicalID]
	if !ok {
		return "", fmt.Errorf("unknown canonical system %q", canonicalID)
	}
	rel, err := contentSubdir(system.ImageRoot, "Images")
	if err != nil {
		return "", err
	}
	return JoinWithin(source.ImagesPath, rel)
}

func CanonicalSystemForExtension(ext string) (string, bool) {
	switch strings.ToLower(ext) {
	case ".gb":
		return "GB", true
	case ".gbc", ".zip":
		return "GBC", true
	case ".gba":
		return "GBA", true
	case ".nes":
		return "FC", true
	case ".md", ".gen", ".smd":
		return "MD", true
	case ".p8", ".p8.png":
		return "PICO8", true
	case ".cbn", ".chd", ".cue", ".img", ".iso", ".mdf", ".pbp", ".toc", ".m3u", ".bin":
		return "PS", true
	default:
		return "", false
	}
}

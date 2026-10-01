package rahub

import "strings"

// AllConsoles returns every console this app knows how to hash for,
// including the disc systems (callers filter those out themselves).
func AllConsoles() []Console {
	out := make([]Console, 0, len(consoles))
	for _, c := range consoles {
		out = append(out, c)
	}
	return out
}

// ConsoleByID returns the console for a RetroAchievements console ID.
func ConsoleByID(id int) (Console, bool) {
	c, ok := consoles[id]
	return c, ok
}

// ConsoleForFolder maps a folder under /roms back to a console. dArkOS
// folder names are the ones listed in Console.Systems.
func ConsoleForFolder(folder string) (Console, bool) {
	folder = strings.ToLower(strings.TrimSpace(folder))
	// dArkOS ships a dedicated "snes-hacks" system, and the same "-hacks"
	// convention can appear for others. Those folders hold ROMs for the
	// system they are named after, so they resolve to it.
	folder = strings.TrimSuffix(folder, "-hacks")
	for _, c := range consoles {
		for _, s := range c.Systems {
			if s == folder {
				return c, true
			}
		}
	}
	return Console{}, false
}

// rapatchesConsoles maps the top-level folder names used in the RAPatches
// repository to RetroAchievements console IDs. The repo uses its own
// shorthand ("MD", "Game Boy"), which matches neither the API's console
// names nor dArkOS's folder names, so the join has to be spelled out.
var rapatchesConsoles = map[string]int{
	"NES":                  7,
	"Famicom Disk System":  81,
	"SNES":                 3,
	"N64":                  2,
	"Nintendo 64DD":        2,
	"GameCube":             16,
	"Wii":                  19,
	"Game Boy":             4,
	"GBC":                  6,
	"GBA":                  5,
	"NDS":                  18,
	"Virtual Boy":          28,
	"Pokemon Mini":         24,
	"MD":                   1,
	"Sega 32X":             10,
	"Sega CD":              9,
	"Master System":        11,
	"Game Gear":            15,
	"Saturn":               39,
	"Dreamcast":            40,
	"SG-1000":              33,
	"PlayStation":          12,
	"PS2":                  21,
	"PlayStation Portable": 41,
	"PC Engine":            8,
	"PC Engine CD":         76,
	"PC-FX":                49,
	"PC-8801":              47,
	"WonderSwan":           53,
	"Neo Geo Pocket":       14,
	"Neo Geo CD":           56,
	"Atari 2600":           25,
	"Atari Jaguar":         17,
	"MSX":                  29,
	"Amstrad CPC":          37,
	"Apple II":             38,
	"Vectrex":              46,
	"3DO":                  43,
	"Watara Supervision":   63,
	"Arcadia 2001":         73,
	"WASM4":                72,
}

// ConsoleForPatchDir maps a RAPatches top-level folder to a console.
func ConsoleForPatchDir(dir string) (Console, bool) {
	id, ok := rapatchesConsoles[dir]
	if !ok {
		return Console{}, false
	}
	return ConsoleByID(id)
}

// PatchDirForConsole is the reverse: which RAPatches folder holds patches
// for this console.
func PatchDirForConsole(id int) (string, bool) {
	for dir, cid := range rapatchesConsoles {
		if cid == id {
			return dir, true
		}
	}
	return "", false
}

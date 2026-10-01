// Package rahub builds the app's catalog from a RetroAchievements hub and
// installs only files whose hash RetroAchievements recognises.
//
// The flow for every hub game is:
//
//	hub (internal-api JSON) → RA hashes (API_GetGameList h=1, one call per
//	console) → itch.io candidates → download → extract → RA-style hash →
//	install ONLY on a hash match
//
// A similar title is never enough: the hash is the only thing that marks a
// file as the right one.
package rahub

import (
	"path/filepath"
	"strings"
)

// HashMethod is how RetroAchievements (rcheevos rc_hash) fingerprints a file
// for a console. It is NOT always a plain MD5 of the whole file — an NES ROM
// is hashed without its 16-byte iNES header, for example — so comparing a
// raw file MD5 would reject most correct NES, SNES, 7800 and Lynx files.
type HashMethod int

const (
	HashWhole   HashMethod = iota // MD5 of the whole file
	HashNES                       // skip a 16-byte "NES\x1a" / "FDS\x1a" header
	HashSNES                      // skip a 512-byte copier header (size % 8192 == 512)
	HashPCE                       // skip a 512-byte header (size % 1024 == 512)
	Hash7800                      // skip a 128-byte "ATARI7800" header
	HashLynx                      // skip a 64-byte "LYNX\0" header
	HashN64                       // normalise byte order to big-endian (.z64)
	HashNDS                       // header + ARM9 + ARM7 + icon, per rcheevos
	HashArduboy                   // .hex with CR removed (line endings normalised)
	HashArcade                    // MD5 of the file NAME without extension
	HashPSX                       // PlayStation disc: SYSTEM.CNF boot executable (disc.go)
	HashPSP                       // PSP disc: PARAM.SFO + EBOOT.BIN (disc.go)
	HashDisc                      // other CD/DVD systems: not supported here
)

// Console describes one RetroAchievements console as this device sees it.
type Console struct {
	ID    int
	Name  string
	Short string // label shown in the list
	// Systems are EmulationStation system names on dArkOS, most likely
	// first. The first one that exists in es_systems.cfg (or as a /roms
	// folder) is where verified files go.
	Systems []string
	// Exts are the file extensions this console's emulators accept, used to
	// rank uploads and to name the installed file.
	Exts   []string
	Method HashMethod
}

// consoles is keyed by RetroAchievements ConsoleID.
var consoles = map[int]Console{
	1:  {1, "Genesis/Mega Drive", "MD", []string{"genesis", "megadrive"}, []string{".md", ".bin", ".gen", ".smd"}, HashWhole},
	2:  {2, "Nintendo 64", "N64", []string{"n64"}, []string{".z64", ".n64", ".v64"}, HashN64},
	3:  {3, "SNES/Super Famicom", "SNES", []string{"snes", "sfc"}, []string{".sfc", ".smc"}, HashSNES},
	4:  {4, "Game Boy", "GB", []string{"gb"}, []string{".gb"}, HashWhole},
	5:  {5, "Game Boy Advance", "GBA", []string{"gba"}, []string{".gba"}, HashWhole},
	6:  {6, "Game Boy Color", "GBC", []string{"gbc"}, []string{".gbc", ".gb"}, HashWhole},
	7:  {7, "NES/Famicom", "NES", []string{"nes", "famicom"}, []string{".nes"}, HashNES},
	8:  {8, "PC Engine/TurboGrafx-16", "PCE", []string{"pcengine", "turbografx", "tg16"}, []string{".pce"}, HashPCE},
	9:  {9, "Sega CD", "SCD", []string{"segacd"}, []string{".chd", ".cue"}, HashDisc},
	10: {10, "32X", "32X", []string{"sega32x"}, []string{".32x", ".bin"}, HashWhole},
	11: {11, "Master System", "SMS", []string{"mastersystem"}, []string{".sms"}, HashWhole},
	12: {12, "PlayStation", "PSX", []string{"psx"}, []string{".cue", ".iso", ".bin", ".chd", ".pbp", ".m3u"}, HashPSX},
	13: {13, "Atari Lynx", "Lynx", []string{"atarilynx", "lynx"}, []string{".lnx", ".lyx"}, HashLynx},
	14: {14, "Neo Geo Pocket", "NGP", []string{"ngpc", "ngp"}, []string{".ngc", ".ngp"}, HashWhole},
	15: {15, "Game Gear", "GG", []string{"gamegear"}, []string{".gg"}, HashWhole},
	17: {17, "Atari Jaguar", "Jaguar", []string{"atarijaguar", "jaguar"}, []string{".j64", ".jag"}, HashWhole},
	18: {18, "Nintendo DS", "NDS", []string{"nds"}, []string{".nds"}, HashNDS},
	23: {23, "Magnavox Odyssey 2", "O2", []string{"odyssey2", "videopac"}, []string{".bin"}, HashWhole},
	24: {24, "Pokemon Mini", "PMini", []string{"pokemonmini", "pokemini"}, []string{".min"}, HashWhole},
	25: {25, "Atari 2600", "2600", []string{"atari2600"}, []string{".a26", ".bin"}, HashWhole},
	27: {27, "Arcade", "Arcade", []string{"fbneo", "arcade", "mame"}, []string{".zip"}, HashArcade},
	28: {28, "Virtual Boy", "VB", []string{"virtualboy"}, []string{".vb"}, HashWhole},
	29: {29, "MSX", "MSX", []string{"msx", "msx2"}, []string{".rom", ".mx1", ".mx2"}, HashWhole},
	33: {33, "SG-1000", "SG1000", []string{"sg-1000", "sg1000"}, []string{".sg"}, HashWhole},
	37: {37, "Amstrad CPC", "CPC", []string{"amstradcpc"}, []string{".dsk", ".sna", ".cpc"}, HashWhole},
	38: {38, "Apple II", "Apple2", []string{"apple2"}, []string{".dsk", ".do", ".po"}, HashWhole},
	39: {39, "Saturn", "Saturn", []string{"saturn"}, []string{".chd", ".cue"}, HashDisc},
	40: {40, "Dreamcast", "DC", []string{"dreamcast"}, []string{".chd", ".cdi", ".gdi"}, HashDisc},
	41: {41, "PlayStation Portable", "PSP", []string{"psp"}, []string{".iso", ".cso"}, HashPSP},
	43: {43, "3DO", "3DO", []string{"3do"}, []string{".iso", ".chd"}, HashDisc},
	44: {44, "ColecoVision", "Coleco", []string{"coleco", "colecovision"}, []string{".col"}, HashWhole},
	45: {45, "Intellivision", "INTV", []string{"intellivision"}, []string{".int", ".bin"}, HashWhole},
	46: {46, "Vectrex", "Vectrex", []string{"vectrex"}, []string{".vec"}, HashWhole},
	50: {50, "Atari 5200", "5200", []string{"atari5200"}, []string{".a52", ".bin"}, HashWhole},
	51: {51, "Atari 7800", "7800", []string{"atari7800"}, []string{".a78"}, Hash7800},
	53: {53, "WonderSwan", "WS", []string{"wonderswancolor", "wonderswan"}, []string{".wsc", ".ws"}, HashWhole},
	57: {57, "Fairchild Channel F", "ChanF", []string{"channelf"}, []string{".chf", ".bin"}, HashWhole},
	63: {63, "Watara Supervision", "SV", []string{"supervision"}, []string{".sv"}, HashWhole},
	69: {69, "Mega Duck", "Duck", []string{"megaduck"}, []string{".bin"}, HashWhole},
	71: {71, "Arduboy", "Arduboy", []string{"arduboy"}, []string{".hex"}, HashArduboy},
	72: {72, "WASM-4", "WASM4", []string{"wasm4"}, []string{".wasm"}, HashWhole},
	76: {76, "PC Engine CD", "PCECD", []string{"pcenginecd", "turbografxcd"}, []string{".chd", ".cue"}, HashDisc},
	80: {80, "Uzebox", "Uzebox", []string{"uzebox"}, []string{".uze"}, HashWhole},
	81: {81, "Famicom Disk System", "FDS", []string{"fds"}, []string{".fds"}, HashNES},
}

// ConsoleFor returns the console for an RA ConsoleID. For IDs this table
// does not know, a best guess is built from the name the hub reported, and
// hashing falls back to whole-file MD5 (which is what rcheevos uses for most
// cartridge systems).
func ConsoleFor(id int, hubName, hubShort string) Console {
	if c, ok := consoles[id]; ok {
		return c
	}
	folder := strings.ToLower(strings.NewReplacer(" ", "", "-", "", "/", "").Replace(hubName))
	short := hubShort
	if short == "" {
		short = hubName
	}
	return Console{ID: id, Name: hubName, Short: short, Systems: []string{folder}, Method: HashWhole}
}

// HasExt reports whether name has one of the console's extensions.
func (c Console) HasExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range c.Exts {
		if e == ext {
			return true
		}
	}
	return false
}

// Verifiable reports whether this app can compute RA's hash for the console.
func (c Console) Verifiable() bool { return c.Method != HashDisc }

// IsDisc reports whether the console's games are disc images.
func (c Console) IsDisc() bool {
	return c.Method == HashPSX || c.Method == HashPSP || c.Method == HashDisc
}

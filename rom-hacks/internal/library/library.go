// Package library fingerprints the ROMs already on the device.
//
// Every ROM gets three identities, because three different things need to
// recognise it:
//
//   - CRC32 and whole-file MD5, which is what a patch's readme quotes and
//     what a BPS footer checks against;
//   - the headerless MD5, because a NES dump with a 16-byte iNES header
//     and the same dump without one are the same game to a patch author
//     but different files to a hash;
//   - the RetroAchievements hash, which is how the base game is looked up
//     in RA's own catalog.
//
// Hashing every ROM on an SD card is slow, so results are cached against
// each file's size and modification time and only changed files are read
// again.
package library

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"leaf-hacks/internal/rahub"
)

// ROM is one file under /roms that the app was able to fingerprint.
type ROM struct {
	Path      string    `json:"path"`
	System    string    `json:"system"` // folder under /roms
	ConsoleID int       `json:"console_id"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`

	CRC32         uint32 `json:"crc32"`
	MD5           string `json:"md5"`
	HeaderlessMD5 string `json:"headerless_md5,omitempty"`
	// HeaderlessCRC32 is the checksum without the copier or iNES header.
	// Patch authors disagree about whether to include one, so a dump can
	// be the right ROM and still not match on the whole-file checksum.
	HeaderlessCRC32 uint32 `json:"headerless_crc32,omitempty"`
	RAHash          string `json:"ra_hash,omitempty"`

	// Inner is the entry name when the ROM lives inside a zip. Every hash
	// above describes the ROM itself, never the container.
	Inner string `json:"inner,omitempty"`

	// Disc marks a CD image. These are not fingerprinted: a .chd cannot be
	// read without chdman, and hashing 600 MB to identify a game whose
	// filename already says which it is would be slow for nothing. Disc
	// games are matched by title and verified after patching instead.
	Disc bool `json:"disc,omitempty"`
}

// Name is the filename, which is what the user recognises.
func (r ROM) Name() string { return filepath.Base(r.Path) }

// Console resolves the console record for this ROM.
func (r ROM) Console() (rahub.Console, bool) { return rahub.ConsoleByID(r.ConsoleID) }

// Library is the fingerprinted contents of /roms, with the lookups the
// matcher needs.
type Library struct {
	Scanned time.Time `json:"scanned"`
	ROMs    []ROM     `json:"roms"`

	byCRC map[uint32][]int
	byMD5 map[string][]int
}

// Index builds the lookup maps. Safe to call repeatedly.
func (l *Library) Index() {
	l.byCRC = make(map[uint32][]int, len(l.ROMs))
	l.byMD5 = make(map[string][]int, len(l.ROMs))
	for i, r := range l.ROMs {
		l.byCRC[r.CRC32] = append(l.byCRC[r.CRC32], i)
		if r.HeaderlessCRC32 != 0 && r.HeaderlessCRC32 != r.CRC32 {
			l.byCRC[r.HeaderlessCRC32] = append(l.byCRC[r.HeaderlessCRC32], i)
		}
		if r.MD5 != "" {
			l.byMD5[r.MD5] = append(l.byMD5[r.MD5], i)
		}
		if r.HeaderlessMD5 != "" && r.HeaderlessMD5 != r.MD5 {
			l.byMD5[r.HeaderlessMD5] = append(l.byMD5[r.HeaderlessMD5], i)
		}
	}
}

// ByCRC32 returns every ROM whose whole-file CRC32 matches.
func (l *Library) ByCRC32(sum uint32) []ROM { return l.collect(l.byCRC[sum]) }

// ByMD5 returns every ROM matching an MD5, headered or headerless.
func (l *Library) ByMD5(sum string) []ROM {
	return l.collect(l.byMD5[strings.ToLower(sum)])
}

// ByRAHash returns ROMs whose RetroAchievements hash matches. Used to
// identify which base games the device holds.
func (l *Library) ByRAHash(hash string) []ROM {
	hash = strings.ToLower(hash)
	var out []ROM
	for _, r := range l.ROMs {
		if r.RAHash == hash {
			out = append(out, r)
		}
	}
	return out
}

// Systems lists the /roms folders that produced at least one ROM.
func (l *Library) Systems() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range l.ROMs {
		if !seen[r.System] {
			seen[r.System] = true
			out = append(out, r.System)
		}
	}
	sort.Strings(out)
	return out
}

func (l *Library) collect(idx []int) []ROM {
	out := make([]ROM, 0, len(idx))
	for _, i := range idx {
		out = append(out, l.ROMs[i])
	}
	return out
}

// maxCartridgeBytes caps what will be read into memory to be hashed. The
// largest cartridge dump is a 64 MB Nintendo 64 ROM, so anything past
// this is not one, and reading it would cost time for nothing.
//
// It must NOT be applied to disc images. They are never read — a disc is
// identified by its filename and verified after patching — and a PS1
// .chd is routinely 130 MB or more. Capping everything at 96 MB silently
// dropped exactly the discs the user was looking for.
const maxCartridgeBytes = 96 << 20

// discSupported lists the disc consoles worth scanning: the ones with
// hacks in RAPatches. PlayStation has 63; Saturn, Dreamcast, Sega CD, PC
// Engine CD and 3DO have none.
func discSupported(consoleID int) bool { return consoleID == 12 }

// Progress reports scanning progress so the UI can show something during
// the first, slow pass.
type Progress func(done, total int, current string)

// Scan walks /roms and fingerprints every cartridge ROM it finds, reusing
// prev for files whose size and timestamp are unchanged.
func Scan(root string, prev *Library, progress Progress) (*Library, error) {
	lib, _, err := ScanReport(root, prev, progress)
	return lib, err
}

// ScanReport is Scan plus an explanation of everything it ignored.
func ScanReport(root string, prev *Library, progress Progress) (*Library, *Report, error) {
	report := newReport()
	cached := map[string]ROM{}
	if prev != nil {
		for _, r := range prev.ROMs {
			cached[r.Path] = r
		}
	}

	type candidate struct {
		path    string
		system  string
		console rahub.Console
		info    os.FileInfo
	}
	var todo []candidate

	systems, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	for _, sysDir := range systems {
		if !sysDir.IsDir() {
			continue
		}
		console, ok := rahub.ConsoleForFolder(sysDir.Name())
		if !ok {
			report.skipFolder(sysDir.Name(), "not a system this app knows")
			noteContent(report, filepath.Join(root, sysDir.Name()))
			continue
		}
		if console.IsDisc() && !discSupported(console.ID) {
			// Sega CD, Saturn, Dreamcast and the rest have no hacks in
			// the patch repository at all — only translations — so
			// scanning them would cost time and find nothing.
			report.skipFolder(sysDir.Name(), "disc system with no hacks available")
			noteContent(report, filepath.Join(root, sysDir.Name()))
			continue
		}
		// The system folder itself, plus its hacks folder: patched ROMs
		// live one level down, and without this pass a hack installed a
		// moment ago would never be seen as installed.
		dirs := []string{
			filepath.Join(root, sysDir.Name()),
			filepath.Join(root, sysDir.Name(), "hacks"),
		}
		var entries []os.DirEntry
		var dirOf = map[string]string{}
		for _, d := range dirs {
			found, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range found {
				if e.IsDir() {
					continue
				}
				dirOf[e.Name()] = d
				entries = append(entries, e)
			}
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			dir := dirOf[e.Name()]
			if !console.HasExt(e.Name()) && !(isZip(e.Name()) && console.Method != rahub.HashArcade) {
				report.note(sysDir.Name(), e.Name())
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() == 0 {
				continue
			}
			if !console.IsDisc() && info.Size() > maxCartridgeBytes {
				report.tooBig(sysDir.Name(), e.Name())
				continue
			}
			todo = append(todo, candidate{filepath.Join(dir, e.Name()), sysDir.Name(), console, info})
		}
	}

	lib := &Library{Scanned: time.Now(), ROMs: make([]ROM, 0, len(todo))}
	for i, c := range todo {
		if progress != nil {
			progress(i, len(todo), filepath.Base(c.path))
		}
		if c.console.IsDisc() {
			lib.ROMs = append(lib.ROMs, ROM{
				Path: c.path, System: c.system, ConsoleID: c.console.ID,
				Size: c.info.Size(), ModTime: c.info.ModTime(), Disc: true,
			})
			continue
		}
		if old, ok := cached[c.path]; ok && old.Size == c.info.Size() && old.ModTime.Equal(c.info.ModTime()) {
			lib.ROMs = append(lib.ROMs, old)
			continue
		}
		rom, err := fingerprint(c.path, c.system, c.console, c.info)
		if err != nil {
			continue // unreadable file: skip it rather than fail the scan
		}
		lib.ROMs = append(lib.ROMs, rom)
	}
	if progress != nil {
		progress(len(todo), len(todo), "")
	}
	lib.Index()
	report.finish()
	return lib, report, nil
}

// noteContent checks whether a folder holds anything ROM-shaped, so the
// report can stay quiet about the dozens of folders that hold themes,
// saves, BIOS files or nothing at all.
func noteContent(report *Report, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if metadataExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		report.sawCandidate(filepath.Base(dir))
		return
	}
}

func fingerprint(path, system string, console rahub.Console, info os.FileInfo) (ROM, error) {
	data, inner, err := readROM(path, console)
	if err != nil {
		return ROM{}, err
	}
	rom := ROM{
		Path:      path,
		System:    system,
		ConsoleID: console.ID,
		Size:      info.Size(),
		ModTime:   info.ModTime(),
		CRC32:     crc32.ChecksumIEEE(data),
		MD5:       md5hex(data),
		Inner:     inner,
	}
	if body, trimmed := stripHeader(console, data); trimmed {
		rom.HeaderlessMD5 = md5hex(body)
		rom.HeaderlessCRC32 = crc32.ChecksumIEEE(body)
	}
	// Arcade hashes the filename, so the name handed to the hasher must be
	// the container's; everything else hashes content, where the inner
	// name is the more accurate one.
	hashName := filepath.Base(path)
	if inner != "" {
		hashName = filepath.Base(inner)
	}
	if hash, err := rahub.HashBytes(console, data, hashName); err == nil {
		rom.RAHash = hash
	}
	return rom, nil
}

// stripHeader removes the copier/emulator headers that make the same dump
// hash two different ways. Patch authors disagree about whether to include
// them, so both forms are kept.
func stripHeader(console rahub.Console, data []byte) ([]byte, bool) {
	switch console.ID {
	case 7, 81: // NES, FDS: 16-byte iNES header
		if len(data) > 16 && (string(data[:3]) == "NES" || string(data[:3]) == "FDS") {
			return data[16:], true
		}
	case 3: // SNES: 512-byte copier header
		if len(data) > 512 && len(data)%8192 == 512 {
			return data[512:], true
		}
	case 8: // PC Engine: 512-byte header
		if len(data) > 512 && len(data)%1024 == 512 {
			return data[512:], true
		}
	}
	return data, false
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// Save and Load persist the fingerprints so only new or changed ROMs are
// read on the next launch.
func (l *Library) Save(path string) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Load(path string) (*Library, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Library
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	l.Index()
	return &l, nil
}

// ByPath returns the ROM at an exact path, if it was scanned.
func (l *Library) ByPath(path string) []ROM {
	for _, r := range l.ROMs {
		if r.Path == path {
			return []ROM{r}
		}
	}
	return nil
}

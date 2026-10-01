// Package disc turns a compressed PlayStation image into the raw track
// a patch expects, and back into something the emulator can load.
//
// RetroAchievements ships 63 PlayStation hacks, and every one sampled has
// the same shape: an .xdelta built against the raw Redump .bin (605 MB for
// Crash Team Racing), plus a ready-made .cue to sit beside the result. So
// patching a disc hack is really three steps — get the .bin out of the
// .chd, patch it, write the .cue — and only the first needs anything this
// app cannot do itself.
//
// That first step needs chdman, from MAME's tools. Decoding CHD in Go
// would mean implementing its hunk codecs (zlib, LZMA, zstd and FLAC for
// audio tracks), which is a project of its own; shelling out to the tool
// that already exists is the honest trade.
package disc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoCHDMan means the image is compressed and the tool to open it is
// missing. The message says how to fix it, because "unsupported" would
// leave the user with nowhere to go.
var ErrNoCHDMan = errors.New(
	"this game is a .chd and chdman is not installed — run: sudo apt install mame-tools")

// CHDManPath overrides the lookup; otherwise chdman is taken from beside
// the binary or from PATH.
var CHDManPath = ""

func chdman() (string, error) {
	if CHDManPath != "" {
		if _, err := os.Stat(CHDManPath); err == nil {
			return CHDManPath, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "chdman")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("chdman")
	if err != nil {
		return "", ErrNoCHDMan
	}
	return path, nil
}

// Available reports whether compressed images can be opened at all.
func Available() bool {
	_, err := chdman()
	return err == nil
}

// IsCompressed reports whether a path needs chdman before it can be read.
func IsCompressed(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".chd")
}

// Extracted is a disc image unpacked to raw tracks.
type Extracted struct {
	Cue string // the .cue written by chdman
	Bin string // the data track: what a patch is built against
	// Tracks is every .bin the extraction produced, in cue order. A
	// single-track disc has one; a disc with CD audio has several, and
	// only the first is the data track a patch touches.
	Tracks []string
}

// Cleanup removes everything an extraction produced.
func (e Extracted) Cleanup() {
	os.Remove(e.Cue)
	os.Remove(e.Bin)
	for _, t := range e.Tracks {
		os.Remove(t)
	}
}

// binsFromCue lists the files a cue sheet references, in order.
func binsFromCue(cuePath string) []string {
	data, err := os.ReadFile(cuePath)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(cuePath)
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(trimmed), "FILE ") {
			continue
		}
		start := strings.Index(trimmed, "\"")
		end := strings.LastIndex(trimmed, "\"")
		if start < 0 || end <= start {
			continue
		}
		out = append(out, filepath.Join(dir, trimmed[start+1:end]))
	}
	return out
}

// Extract unpacks a .chd into dir, naming the output after stem. The
// original file is never touched — it stays on the card as the user's
// copy of the game.
func Extract(chdPath, dir, stem string) (Extracted, error) {
	bin, err := chdman()
	if err != nil {
		return Extracted{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Extracted{}, err
	}
	cuePath := filepath.Join(dir, stem+".cue")
	binPath := filepath.Join(dir, stem+".bin")

	// chdman refuses to overwrite, so clear any half-finished attempt.
	os.Remove(cuePath)
	os.Remove(binPath)

	// --splitbin writes one .bin per track, which is how Redump dumps a
	// multi-track disc and therefore what a patch was built against. The
	// default is every track merged into one file, whose checksum matches
	// nothing. Older chdman does not know the flag, so a refusal falls
	// back to the merged form rather than failing outright.
	out, err := exec.Command(bin, "extractcd", "--splitbin", "-i", chdPath, "-o", cuePath, "-ob", binPath).CombinedOutput()
	if err != nil {
		os.Remove(cuePath)
		os.Remove(binPath)
		out, err = exec.Command(bin, "extractcd", "-i", chdPath, "-o", cuePath, "-ob", binPath).CombinedOutput()
	}
	if err != nil {
		os.Remove(cuePath)
		os.Remove(binPath)
		return Extracted{}, fmt.Errorf("chdman extractcd: %v: %s", err, lastLine(out))
	}

	// With --splitbin the data track is named after the first track in
	// the cue, not after -ob. Read the cue to find what was actually
	// written.
	tracks := binsFromCue(cuePath)
	e := Extracted{Cue: cuePath, Bin: binPath, Tracks: tracks}
	if len(tracks) > 0 {
		e.Bin = tracks[0]
	}
	return e, nil
}

// WriteCue saves the .cue that came with a patch, pointing it at the
// patched .bin.
//
// The shipped cue names the hack's own bin, which is almost never what the
// file ends up called here, so the FILE line is rewritten. A cue whose
// FILE line points at nothing loads as an empty disc.
func WriteCue(shippedCue []byte, destCue, binName string) error {
	lines := strings.Split(strings.ReplaceAll(string(shippedCue), "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(trimmed), "FILE ") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		mode := "BINARY"
		if fields := strings.Fields(trimmed); len(fields) > 0 {
			mode = fields[len(fields)-1]
		}
		lines[i] = fmt.Sprintf("%sFILE \"%s\" %s", indent, binName, mode)
	}
	return os.WriteFile(destCue, []byte(strings.Join(lines, "\n")), 0o644)
}

// DefaultCue builds a single-track cue for a data-only image, used when a
// patch archive ships no cue of its own.
func DefaultCue(binName string) []byte {
	return []byte(fmt.Sprintf("FILE \"%s\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n", binName))
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// ResolvePlaylist turns an .m3u into the disc it points at.
//
// Multi-disc PlayStation games are stored as a playlist naming one file
// per disc, and that is what sits in the ROM folder — "Final Fantasy IX
// (USA) (Disc 1) (Rev 1).m3u" rather than any image. Handing that to
// chdman produces nothing useful, so the playlist is read first.
//
// Hacks are built against a single disc, and in a playlist the first
// entry is disc 1, which is the one they patch.
func ResolvePlaylist(path string) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".m3u") {
		return path, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if !filepath.IsAbs(entry) {
			entry = filepath.Join(dir, entry)
		}
		if _, err := os.Stat(entry); err == nil {
			return entry, nil
		}
		return "", fmt.Errorf("%s lists %q, which is not there", filepath.Base(path), filepath.Base(entry))
	}
	return "", fmt.Errorf("%s is empty", filepath.Base(path))
}

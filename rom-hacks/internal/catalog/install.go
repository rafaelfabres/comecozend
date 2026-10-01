package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"leaf-hacks/internal/disc"
	"leaf-hacks/internal/library"
	"leaf-hacks/internal/patch"
	"leaf-hacks/internal/rahub"
	"leaf-hacks/internal/rapatches"
	"leaf-hacks/internal/roms"
)

// Plan is a resolved, ready-to-apply installation: which patch, which
// local ROM it will be applied to, and which hash the result must have.
type Plan struct {
	Hack     Hack
	Patch    rapatches.PatchFile
	Base     library.ROM
	Expected rahub.HashEntry // the RA-supported file this should produce
	// Supported is every file RetroAchievements accepts for this set. The
	// check after patching is membership in this list, not equality with
	// Expected: pairing a patch to one particular hash by filename is a
	// guess, and a guess is no reason to throw away a ROM that the site
	// would have accepted.
	Supported []rahub.HashEntry
	Format    patch.Format

	// Alternatives are the other versions of the hack in the same archive,
	// when it ships more than one.
	Alternatives []string

	// Bases are every copy of the base game on this card, best guess
	// first. A patch that declares no checksum cannot say which copy it
	// wants, so the only honest answer is to try them.
	Bases []library.ROM

	// Notes is the hack's own readme, minus the base-ROM block: the one
	// place a hack reliably says what it changes.
	Notes string

	// Region is set when the patch wants a different release than the
	// ROM it will be applied to. The patch may still work — the check is
	// a filename heuristic, not a checksum — so this is a warning shown
	// before A is pressed, not a refusal.
	Region      RegionMismatch
	RegionWrong bool

	// Extras are the archive's non-patch files. PlayStation hacks ship a
	// ready-made .cue that belongs beside the patched track.
	Extras []rapatches.PatchFile

	// Payload is the whole archive, kept so install can fall back to the
	// other patches inside it when the chosen one produces something
	// RetroAchievements does not know.
	Payload rapatches.Payload

	archivePath string
	archive     io.Closer
}

// Close releases the downloaded archive. Safe to call more than once, and
// on a plan that never opened one.
func (p *Plan) Close() {
	if p == nil {
		return
	}
	if p.archive != nil {
		p.archive.Close()
		p.archive = nil
	}
	if p.archivePath != "" {
		os.Remove(p.archivePath)
		p.archivePath = ""
	}
}

// wantedDump names the base ROM the patch archive says it needs, from the
// readme shipped beside the patch.
func (p *Plan) wantedDump() string {
	for _, b := range p.Payload.Bases {
		if b.File != "" {
			return b.File
		}
	}
	for _, b := range p.Payload.Bases {
		if d := b.Describe(); d != "" && d != "(unnamed dump)" {
			return d
		}
	}
	return ""
}

// matchSupported returns the hash entry a produced file satisfies.
func (p *Plan) matchSupported(hash string) (rahub.HashEntry, bool) {
	accepted := p.Supported
	if len(accepted) == 0 && p.Expected.MD5 != "" {
		accepted = []rahub.HashEntry{p.Expected}
	}
	for _, h := range accepted {
		if strings.EqualFold(h.MD5, hash) {
			return h, true
		}
	}
	return rahub.HashEntry{}, false
}

// ErrNoBaseROM means the patch is fine but this device does not hold the
// exact dump it needs.
type ErrNoBaseROM struct {
	Wanted []string // human-readable descriptions of acceptable base ROMs
	Near   []string // files of the right game whose checksum differs
}

func (e *ErrNoBaseROM) Error() string {
	if len(e.Near) > 0 {
		return "you have this game, but not the revision this patch needs"
	}
	return "the base ROM for this patch is not on the device"
}

// Resolve downloads the patch and works out exactly what to apply it to.
//
// The base ROM is chosen by the patch's own embedded source checksum where
// the format has one. That is deliberate: the readme beside the patch is
// sometimes wrong (one sampled archive names an unrelated Game Boy title
// as the base ROM for a Super Mario World hack), while the checksum in a
// BPS footer is what the patch will actually verify against.
func Resolve(ctx context.Context, httpClient *http.Client, ra *rahub.RAClient, h Hack, lib *library.Library) (*Plan, error) {
	hashes, err := ra.FetchGameHashes(ctx, h.GameID)
	if err != nil {
		return nil, err
	}
	if len(hashes) == 0 {
		return nil, fmt.Errorf("RetroAchievements lists no supported file for this hack yet")
	}

	// Streamed to a file, not held in memory. Three PlayStation hacks in
	// the repository ship archives past 70 MB, and buffering one of those
	// alongside the patch expanded from it is more than this device has.
	archivePath, err := rapatches.DownloadTo(ctx, httpClient, h.Entry, stagingDir())
	if err != nil {
		return nil, err
	}
	payload, closer, err := rapatches.OpenFile(archivePath)
	if err != nil {
		os.Remove(archivePath)
		return nil, err
	}

	// From here every failure must let go of the staged archive. Missing
	// one left an 80 MB file on the card per attempt, named after nothing
	// anybody would recognise.
	discard := func() {
		closer.Close()
		os.Remove(archivePath)
	}

	chosen, expected, alts := choosePatch(payload, hashes)
	format := patch.Detect(chosen.Bytes())
	if format == patch.Unknown {
		discard()
		return nil, fmt.Errorf("%s is not a patch format this app understands", filepath.Base(chosen.Name))
	}

	base, alternates, err := findBase(chosen, payload, lib, h)
	if err != nil {
		discard()
		return nil, err
	}
	if base.Disc && !disc.Available() {
		discard()
		return nil, disc.ErrNoCHDMan
	}

	plan := &Plan{
		archivePath: archivePath, archive: closer,
		Notes: ReadmeNotes(payload.Readme),
		Hack:  h, Patch: chosen, Base: base, Bases: alternates,
		Expected: expected, Supported: hashes,
		Format: format, Alternatives: alts,
		Extras:  payload.Extras,
		Payload: payload,
	}
	if wanted := plan.wantedDump(); wanted != "" {
		plan.Region, plan.RegionWrong = CheckRegion(wanted, base.Name())
	}
	return plan, nil
}

// choosePatch pairs a patch file with the RetroAchievements hash it will
// satisfy. RA names a supported file after the ROM the patch produces —
// "Foo (v2.4).gba" for "Foo (v2.4).bps" — so the stems line up. When an
// archive holds several versions, the newest supported one wins, which is
// what a player wants and what the achievement set is usually built for.
func choosePatch(p rapatches.Payload, hashes []rahub.HashEntry) (rapatches.PatchFile, rahub.HashEntry, []string) {
	type pair struct {
		file rapatches.PatchFile
		hash rahub.HashEntry
	}
	var matched []pair
	for _, f := range p.Patches {
		for _, e := range hashes {
			if strings.EqualFold(f.Stem(), stemOf(e.Name)) {
				matched = append(matched, pair{f, e})
			}
		}
	}
	if len(matched) > 0 {
		// Newest first, by version number rather than by string: "v2.10"
		// sorts before "v2.9" alphabetically, which would quietly install
		// an old build of a hack that has had ten releases since.
		sort.SliceStable(matched, func(i, j int) bool {
			return NewerVersion(matched[i].file.Stem(), matched[j].file.Stem())
		})
		best := matched[0]
		var alts []string
		for _, m := range matched {
			if m.file.Name != best.file.Name {
				alts = append(alts, m.file.Stem())
			}
		}
		return best.file, best.hash, alts
	}

	// No name match: fall back to the top-level patch and the first hash.
	// The final check still compares the produced file against RA, so a
	// wrong guess fails loudly instead of installing something broken.
	file, _ := p.Select("")
	return file, hashes[0], nil
}

func stemOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

// findBase picks the local ROM this patch was built against.
func findBase(pf rapatches.PatchFile, payload rapatches.Payload, lib *library.Library, h Hack) (library.ROM, []library.ROM, error) {
	// A disc image was matched by title, not by hash, and nothing can be
	// checked until it is extracted. The guarantee comes at the end
	// instead: the patched image must hash to the file RetroAchievements
	// lists, or it is thrown away.
	//
	// Where several copies of the game are on the card, the one whose
	// region and revision match what the patch asks for is chosen. Adding
	// the right dump next to the wrong one should be enough to make a
	// hack work, without deleting anything.
	var discs []library.ROM
	for _, path := range h.BasePaths {
		for _, r := range lib.ByPath(path) {
			if r.Disc {
				discs = append(discs, r)
			}
		}
	}
	if len(discs) > 0 {
		wanted := ""
		for _, b := range payload.Bases {
			if b.File != "" {
				wanted = b.File
				break
			}
		}
		if wanted != "" {
			for _, r := range discs {
				if _, mismatch := CheckRegion(wanted, r.Name()); !mismatch {
					return r, discs, nil
				}
			}
		}
		return discs[0], discs, nil
	}

	// Every copy of this game on the card, in the order worth trying.
	owned := ownedCopies(lib, h)

	// Best source of truth: the checksum inside the patch.
	if crc, err := patch.SourceCRC32(pf.Bytes()); err == nil {
		if hits := lib.ByCRC32(crc); len(hits) > 0 {
			best := pickForConsole(hits, h.ConsoleID)
			return best, promote(owned, best), nil
		}
	}

	// Otherwise the readme is all there is (IPS and xdelta carry nothing).
	for _, b := range payload.Bases {
		if b.MD5 != "" {
			if hits := lib.ByMD5(b.MD5); len(hits) > 0 {
				best := pickForConsole(hits, h.ConsoleID)
				return best, promote(owned, best), nil
			}
		}
		if b.HeaderlessMD5 != "" {
			if hits := lib.ByMD5(b.HeaderlessMD5); len(hits) > 0 {
				best := pickForConsole(hits, h.ConsoleID)
				return best, promote(owned, best), nil
			}
		}
		if b.HasCRC {
			if hits := lib.ByCRC32(b.CRC32); len(hits) > 0 {
				best := pickForConsole(hits, h.ConsoleID)
				return best, promote(owned, best), nil
			}
		}
	}

	// Nothing declared a checksum that fits. When the patch declares
	// nothing at all — a bare IPS or xdelta with no readme — the only
	// honest answer is to try each copy of the game and see which one
	// produces a file RetroAchievements recognises.
	if len(owned) > 0 && len(payload.Bases) == 0 {
		if _, err := patch.SourceCRC32(pf.Bytes()); err != nil {
			return owned[0], owned, nil
		}
	}

	// Nothing matched. Say what is wanted and what the user has that is
	// close, because "wrong revision" is the common case and is fixable.
	miss := &ErrNoBaseROM{}
	if crc, err := patch.SourceCRC32(pf.Bytes()); err == nil {
		miss.Wanted = append(miss.Wanted, fmt.Sprintf("a dump with CRC32 %08X", crc))
	}
	for _, b := range payload.Bases {
		miss.Wanted = append(miss.Wanted, b.Describe())
	}
	if len(miss.Wanted) == 0 {
		miss.Wanted = append(miss.Wanted, "the original "+h.BaseTitle+" ROM")
	}
	for _, p := range h.BasePaths {
		miss.Near = append(miss.Near, filepath.Base(p))
	}
	return library.ROM{}, nil, miss
}

// ownedCopies lists every copy of the hack's base game on this card.
func ownedCopies(lib *library.Library, h Hack) []library.ROM {
	var out []library.ROM
	for _, path := range h.BasePaths {
		for _, r := range lib.ByPath(path) {
			if r.ConsoleID == h.ConsoleID {
				out = append(out, r)
			}
		}
	}
	return out
}

// promote puts the chosen copy first and keeps the rest as fallbacks, so
// a patch that turns out not to fit the obvious candidate still gets
// tried against the other copies.
func promote(all []library.ROM, best library.ROM) []library.ROM {
	out := []library.ROM{best}
	for _, r := range all {
		if r.Path != best.Path {
			out = append(out, r)
		}
	}
	return out
}

// pickForConsole prefers a hit in the folder of the console the hack is
// for, since the same dump can sit in more than one system folder.
func pickForConsole(hits []library.ROM, consoleID int) library.ROM {
	for _, r := range hits {
		if r.ConsoleID == consoleID {
			return r
		}
	}
	return hits[0]
}

// tryOtherCopies runs the patch against the user's other copies of the
// base game, stopping at the first result RetroAchievements recognises.
//
// Cheap for a cartridge — a few megabytes read per attempt — and skipped
// for discs, where each attempt would mean extracting 600 MB again.
func tryOtherCopies(p *Plan, console rahub.Console, report Reporter) ([]byte, rahub.HashEntry, error) {
	for _, alt := range p.Bases {
		if alt.Path == p.Base.Path || alt.Disc {
			continue
		}
		report.stage(Stage("Trying " + alt.Name()))
		source, err := library.Read(alt)
		if err != nil {
			continue
		}
		patched, matched, err := applyUntilSupported(p, source, console)
		if err == nil {
			// Record which copy actually worked, so the page and the
			// install record name the right file.
			p.Base = alt
			return patched, matched, nil
		}
	}
	return nil, rahub.HashEntry{}, fmt.Errorf(
		"none of your %d copies of %s produced a file RetroAchievements knows",
		len(p.Bases), p.Hack.BaseTitle)
}

// applyUntilSupported patches the base ROM and checks the result against
// every file RetroAchievements accepts for the set.
//
// An archive often holds several versions of the same hack, and which .bps
// belongs to which supported hash is inferred from filenames — an
// inference that fails whenever the author named the files differently
// from the way the site lists them. Rather than reject a perfectly good
// ROM on a naming mismatch, the other patches in the archive are tried
// too, and the first result the site recognises wins.
func applyUntilSupported(p *Plan, source []byte, console rahub.Console) ([]byte, rahub.HashEntry, error) {
	candidates := []rapatches.PatchFile{p.Patch}
	others := make([]rapatches.PatchFile, 0, len(p.Payload.Patches))
	for _, f := range p.Payload.Patches {
		if f.Name != p.Patch.Name {
			others = append(others, f)
		}
	}
	// Newest first among the fallbacks too, so a rescue attempt does not
	// land on an ancient build.
	sort.SliceStable(others, func(i, j int) bool {
		return NewerVersion(others[i].Stem(), others[j].Stem())
	})
	candidates = append(candidates, others...)

	var firstHash string
	var firstErr error
	for _, f := range candidates {
		if patch.Detect(f.Bytes()) == patch.Unknown {
			continue
		}
		out, err := patch.ApplyAny(f.Bytes(), source)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		hash, err := rahub.HashBytes(console, out, f.Stem()+firstExt(console))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if entry, ok := p.matchSupported(hash); ok {
			return out, entry, nil
		}
		if firstHash == "" {
			firstHash = hash
		}
	}

	if firstHash != "" {
		return nil, rahub.HashEntry{}, fmt.Errorf(
			"none of the %d patches in this archive produced a file RetroAchievements knows (got %s, it accepts %s) — achievements would not work, so nothing was installed",
			len(candidates), firstHash[:8], shortHashes(p.Supported))
	}
	if firstErr != nil {
		// A checksum refusal means the dump is the wrong one, which is
		// worth saying in words: the hex pair alone tells the user
		// nothing about what to go and find.
		if wanted := p.wantedDump(); wanted != "" {
			return nil, rahub.HashEntry{}, fmt.Errorf("%w\n  this patch needs %s", firstErr, wanted)
		}
		return nil, rahub.HashEntry{}, firstErr
	}
	return nil, rahub.HashEntry{}, errors.New("this archive has no patch that can be applied")
}

// shortHashes lists the accepted hashes briefly, so a mismatch says what
// was expected instead of only what went wrong.
func shortHashes(entries []rahub.HashEntry) string {
	var parts []string
	if len(entries) == 0 {
		return "nothing"
	}
	for i, e := range entries {
		if i == 3 {
			parts = append(parts, "...")
			break
		}
		if len(e.MD5) >= 8 {
			parts = append(parts, e.MD5[:8])
		}
	}
	return strings.Join(parts, ", ")
}

// HacksDir is where patched ROMs go: a "hacks" folder inside the system's
// own folder, so /roms/gba/hacks/... sits beside the originals without
// mixing into them. The original ROM is never modified or moved.
func HacksDir(romsRoot, system string) string {
	return filepath.Join(romsRoot, system, "hacks")
}

// Install applies the plan and writes the patched ROM into the system's
// hacks folder, but only if the result is a file RetroAchievements
// recognises.
//
// This is the whole point of the app: a patched ROM that does not hash to
// a supported file would boot and then silently earn no achievements, so
// it is never written to /roms.
// TempDir is where large downloads are staged. It must be on the card:
// on plenty of handheld builds /tmp is a tmpfs, so "streaming to disk"
// there is streaming to RAM — exactly what the streaming was meant to
// avoid. The app sets this at startup; the default is only a fallback.
var TempDir = os.TempDir()

// SweepStaging deletes archives left in the staging directory.
//
// Everything there belongs to a download in progress, so at startup —
// when nothing is in progress — all of it is rubbish. It accumulates
// whenever the app does not get to tidy up: a crash, the kernel's OOM
// killer, the power switch, or the user closing the menu mid-download.
// Forty files and 83 MB turned up on a real device this way.
func SweepStaging() (removed int, bytes int64) {
	dir := stagingDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "patch-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
			bytes += info.Size()
		}
	}
	return removed, bytes
}

// stagingDir returns TempDir, creating it if need be, and falls back to
// the system temporary directory if the card is not writable.
func stagingDir() string {
	if TempDir == "" {
		return os.TempDir()
	}
	if err := os.MkdirAll(TempDir, 0o755); err != nil {
		return os.TempDir()
	}
	return TempDir
}

// Stage names the step an install is on, for the screen.
type Stage string

const (
	StageExtract Stage = "Extracting the disc"
	StagePatch   Stage = "Patching"
	StageVerify  Stage = "Verifying against RetroAchievements"
	StageSave    Stage = "Saving"
)

// Reporter receives install progress. done and total are bytes, or 0 when
// the step has no measurable size.
type Reporter func(stage Stage, done, total int64)

func (r Reporter) stage(s Stage) {
	if r != nil {
		r(s, 0, 0)
	}
}

// Install applies the plan and writes the patched ROM into the system's
// hacks folder, but only if the result is a file RetroAchievements
// recognises.
func Install(p *Plan, romsRoot string) (string, error) {
	return InstallWithProgress(p, romsRoot, nil)
}

// InstallWithProgress is Install with a running commentary, which a disc
// needs: extracting and patching 600 MB takes minutes, and minutes of an
// unchanged screen look exactly like a hang.
func InstallWithProgress(p *Plan, romsRoot string, report Reporter) (string, error) {
	if p.Base.Disc {
		return installDisc(p, romsRoot, report)
	}

	// Through the library, not os.ReadFile: the base ROM may be stored
	// inside a zip, and the patcher must be handed the ROM rather than
	// the container.
	source, err := library.Read(p.Base)
	if err != nil {
		return "", fmt.Errorf("read base ROM: %w", err)
	}

	console, ok := p.Hack.Console()
	if !ok {
		return "", errors.New("unknown console for this hack")
	}

	report.stage(StagePatch)
	patched, matched, err := applyUntilSupported(p, source, console)
	if err != nil {
		// Try the other copies of the same game. Two dumps of Pokémon
		// Crystal sit side by side on plenty of cards — a (UE) (V1.0)
		// and a (USA, Europe) (Rev 1) — and a patch that declares no
		// checksum cannot say which one it wants. Giving up after the
		// first is giving up early.
		patched, matched, err = tryOtherCopies(p, console, report)
		if err != nil {
			return "", err
		}
	}
	report.stage(StageSave)
	name := matched.Name
	if name == "" {
		name = p.Patch.Stem() + firstExt(console)
	}

	dir := HacksDir(romsRoot, p.Base.System)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, roms.SanitiseFilename(stemOf(name), filepath.Ext(name)))

	tmp := dest + ".part"
	if err := os.WriteFile(tmp, patched, 0o644); err != nil {
		return "", fmt.Errorf("write patched ROM: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dest, nil
}

func firstExt(c rahub.Console) string {
	if len(c.Exts) > 0 {
		return c.Exts[0]
	}
	return ".bin"
}

// installDisc handles PlayStation hacks.
//
// The patch is built against the raw Redump .bin, so the .chd has to come
// apart first. What gets kept is the patched track plus a .cue; the
// user's original .chd is never touched, and the temporary copy of the
// unpatched track is removed once it has served its purpose — it is the
// same 600 MB the .chd already holds, only bigger.
func installDisc(p *Plan, romsRoot string, report Reporter) (string, error) {
	dir := HacksDir(romsRoot, p.Base.System)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stem := roms.SanitiseFilename(stemOf(p.Expected.Name), "")
	if stem == "" {
		stem = p.Patch.Stem()
	}

	// A multi-disc game is an .m3u playlist in the ROM folder, not an
	// image; the hack is built against the disc it names.
	basePath, err := disc.ResolvePlaylist(p.Base.Path)
	if err != nil {
		return "", err
	}

	// tracks is every file of the disc, data track first; layoutCue is the
	// cue that describes them, when there is one. Only the data track is
	// patched, but a disc with CD audio needs the other tracks beside it,
	// or the hack plays without its music.
	var tracks []string
	var layoutCue string
	extractedTracks := false
	if disc.IsCompressed(basePath) {
		report.stage(StageExtract)
		extracted, err := disc.Extract(basePath, dir, stem+" (original)")
		if err != nil {
			return "", err
		}
		defer extracted.Cleanup()
		tracks = extracted.Tracks
		if len(tracks) == 0 {
			tracks = []string{extracted.Bin}
		}
		layoutCue = extracted.Cue
		extractedTracks = true
	} else if strings.EqualFold(filepath.Ext(basePath), ".cue") {
		files, err := rahub.CueFiles(basePath)
		if err != nil || len(files) == 0 {
			return "", fmt.Errorf("could not read the track list from %s", filepath.Base(basePath))
		}
		tracks = files
		layoutCue = basePath
	} else {
		tracks = []string{basePath}
	}
	sourceBin := tracks[0]

	binPath := filepath.Join(dir, stem+".bin")
	cuePath := filepath.Join(dir, stem+".cue")

	// Everything written into the hacks folder, so a failure at any later
	// step leaves nothing behind.
	written := []string{binPath}
	discard := func() {
		for _, f := range written {
			os.Remove(f)
		}
	}

	// Streamed, never loaded. A PlayStation track is around 600 MB, and
	// holding the source and the target at once on a 1 GB handheld got
	// the app killed by the kernel partway through an install.
	report.stage(StagePatch)
	onProgress := patch.Progress(nil)
	if report != nil {
		onProgress = func(done, total int64) { report(StagePatch, done, total) }
	}
	patchErr := patch.ApplyFile(p.Patch.Bytes(), sourceBin, binPath, onProgress)
	if patchErr != nil {
		// Try the other patches in the archive, newest first, the way the
		// cartridge path does.
		others := make([]rapatches.PatchFile, 0, len(p.Payload.Patches))
		for _, f := range p.Payload.Patches {
			if f.Name != p.Patch.Name {
				others = append(others, f)
			}
		}
		sort.SliceStable(others, func(i, j int) bool {
			return NewerVersion(others[i].Stem(), others[j].Stem())
		})
		for _, f := range others {
			if err := patch.ApplyFile(f.Bytes(), sourceBin, binPath, onProgress); err == nil {
				patchErr = nil
				break
			}
		}
	}
	if patchErr != nil {
		if wanted := p.wantedDump(); wanted != "" {
			return "", fmt.Errorf("%w\n  this patch needs %s", patchErr, wanted)
		}
		return "", patchErr
	}

	// The other tracks go beside the patched one. Extracted tracks are this
	// install's own temporary files, so they are moved; tracks from a
	// .bin/.cue set on the card are the user's, so they are copied.
	names := []string{filepath.Base(binPath)}
	for i, t := range tracks[1:] {
		dest := filepath.Join(dir, fmt.Sprintf("%s (Track %02d)%s", stem, i+2, filepath.Ext(t)))
		var err error
		if extractedTracks {
			err = os.Rename(t, dest)
		} else {
			err = copyFile(t, dest)
		}
		if err != nil {
			discard()
			return "", fmt.Errorf("could not keep track %d of the disc: %w", i+2, err)
		}
		written = append(written, dest)
		names = append(names, filepath.Base(dest))
	}

	// The cue that shipped with the patch, repointed at the files just
	// written; a cue naming a file that is not there loads as a blank disc.
	// It is only trusted when it describes as many track files as the
	// disc has; otherwise the disc's own cue gives the layout.
	cueData := disc.DefaultCue(names[0])
	if layoutCue != "" {
		if b, err := os.ReadFile(layoutCue); err == nil && disc.CueFileCount(b) == len(names) {
			cueData = b
		}
	}
	for _, f := range p.Extras {
		if strings.EqualFold(filepath.Ext(f.Name), ".cue") {
			if b := f.Bytes(); disc.CueFileCount(b) == len(names) {
				cueData = b
			}
			break
		}
	}
	written = append(written, cuePath)
	if err := disc.WriteCue(cueData, cuePath, names...); err != nil {
		discard()
		return "", err
	}

	// Only now can the result be checked: the RetroAchievements hash for a
	// PlayStation game is taken from the boot executable inside the image.
	// A disc that cannot be hashed is not kept either: unverified is
	// exactly what this app promises never to install.
	report.stage(StageVerify)
	console, _ := p.Hack.Console()
	got, err := hashImage(console, cuePath)
	if err != nil {
		discard()
		return "", fmt.Errorf("the patched disc could not be checked against RetroAchievements (%v), so it was not kept", err)
	}
	if _, ok := p.matchSupported(got); !ok {
		discard()
		return "", fmt.Errorf("the patched disc does not match RetroAchievements (%s, it accepts %s) — achievements would not work, so it was not kept", shortHash(got), shortHashes(p.Supported))
	}
	return cuePath, nil
}

// hashImage is rahub.HashImage, swappable in tests: building a bootable
// PlayStation image just to exercise the file handling around it is not
// worth it.
var hashImage = rahub.HashImage

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// copyFile copies src to dst through a temporary name, so a half-written
// track never sits in the hacks folder under its real name.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

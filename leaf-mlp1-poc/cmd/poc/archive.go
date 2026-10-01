package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bodgit/sevenzip"

	"leaf-mlp1-poc/internal/itchio"
	"leaf-mlp1-poc/internal/roms"
)

// maxExtractedROM caps what we will write out of an archive, so a malicious
// or malformed zip cannot fill the SD card.
const maxExtractedROM = 64 << 20 // 64 MiB

// maxExtractedTrack is the cap for one file of a multi-file release. A
// PlayStation data track alone is around 600 MB, so the cartridge cap above
// cut every disc bundle short.
var maxExtractedTrack int64 = 1 << 30 // 1 GiB; a var so tests can lower it

// errTooLarge means an archive entry is bigger than the cap. Writing the
// first N bytes and calling it done installed a truncated ROM that looked
// fine in the menu and failed in the emulator.
var errTooLarge = errors.New("file inside the archive is larger than this app will extract")

// copyCapped copies at most max bytes and fails, rather than truncating,
// when src holds more.
func copyCapped(dst io.Writer, src io.Reader, max int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, max+1))
	if err == nil && n > max {
		return n, fmt.Errorf("%w (%d MB)", errTooLarge, max>>20)
	}
	return n, err
}

// extractROMFromZip opens a downloaded .zip, finds the best ROM inside it and
// writes that ROM to its system folder. Many homebrew releases ship the ROM
// inside an archive rather than as a bare file, which is why a plain
// roms.SelectBest pass over the upload list rejects them (ScoreUpload scores
// ".zip" as 0). Returns the written path.
//
// It deliberately ignores the archive's directory structure and writes just
// the ROM, flattened, under the name the caller chose.
func extractROMFromZip(zipPath, titleForName string) (string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer reader.Close()

	var best *zip.File
	bestScore := 0
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		// Skip macOS resource forks, which otherwise look like real entries.
		if strings.HasPrefix(file.Name, "__MACOSX/") {
			continue
		}
		if score := roms.ScoreUpload(filepath.Base(file.Name)); score > bestScore {
			best, bestScore = file, score
		}
	}
	if best == nil {
		return "", fmt.Errorf("archive contains no ROM this console can run")
	}

	ext := roms.ROMExt(filepath.Base(best.Name))
	destDir := roms.DestinationDir(ext)
	if destDir == "" {
		return "", fmt.Errorf("unsupported ROM type %q inside archive", ext)
	}

	// The best entry is only an INDEX (.cue naming .bin tracks, .m3u naming
	// discs). Pulling that one file out installs a pointer to files that
	// stayed inside the archive — which is exactly how a PSX release ended
	// up on disk as a lone 264-byte .cue. Unpack the whole set instead.
	//
	// The check belongs HERE, in the single-file extractor, because this is
	// the path taken whenever itch.io's upload names carry no extension and
	// the archive has to be found by content. That is the common case, and
	// the multi-file logic added elsewhere was never reached by it.
	if needsCompanionFiles(best.Name) {
		reader.Close()
		return extractBundle(zipPath, destDir)
	}

	name := roms.SanitiseFilename(titleForName, ext)
	if name == "" {
		name = filepath.Base(best.Name)
	}
	destPath := filepath.Join(destDir, name)

	source, err := best.Open()
	if err != nil {
		return "", fmt.Errorf("read archive entry: %w", err)
	}
	defer source.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", destDir, err)
	}
	out, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", destPath, err)
	}
	written, err := copyCapped(out, source, maxExtractedROM)
	closeErr := out.Close()
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("extract: %w", err)
	}
	if closeErr != nil {
		os.Remove(destPath)
		return "", closeErr
	}
	if written == 0 {
		os.Remove(destPath)
		return "", fmt.Errorf("archive entry was empty")
	}
	return destPath, nil
}

// pickArchiveUpload returns the first archive upload, used as a fallback when
// no upload is a bare ROM. .zip and .7z are both common for homebrew
// releases; .7z is read with the sevenzip package the vendored roms code
// already depends on.
func pickArchiveUpload(uploads []roms.Upload) *roms.Upload {
	for _, ext := range []string{".zip", ".7z"} {
		for i := range uploads {
			if strings.EqualFold(filepath.Ext(uploads[i].Filename), ext) {
				return &uploads[i]
			}
		}
	}
	return nil
}

// extractROMFromArchive dispatches on the archive type.
func extractROMFromArchive(archivePath, originalName, titleForName string) (string, error) {
	if strings.EqualFold(filepath.Ext(originalName), ".7z") {
		return extractROMFrom7z(archivePath, titleForName)
	}
	return extractROMFromZip(archivePath, titleForName)
}

// extractROMFrom7z mirrors extractROMFromZip for .7z archives.
func extractROMFrom7z(archivePath, titleForName string) (string, error) {
	reader, err := sevenzip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open 7z archive: %w", err)
	}
	defer reader.Close()

	var best *sevenzip.File
	bestScore := 0
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || strings.HasPrefix(file.Name, "__MACOSX/") {
			continue
		}
		if score := roms.ScoreUpload(filepath.Base(file.Name)); score > bestScore {
			best, bestScore = file, score
		}
	}
	if best == nil {
		return "", fmt.Errorf("archive contains no ROM this console can run")
	}

	ext := roms.ROMExt(filepath.Base(best.Name))
	destDir := roms.DestinationDir(ext)
	if destDir == "" {
		return "", fmt.Errorf("unsupported ROM type %q inside archive", ext)
	}
	name := roms.SanitiseFilename(titleForName, ext)
	if name == "" {
		name = filepath.Base(best.Name)
	}
	destPath := filepath.Join(destDir, name)

	source, err := best.Open()
	if err != nil {
		return "", fmt.Errorf("read archive entry: %w", err)
	}
	defer source.Close()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	out, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	_, err = copyCapped(out, source, maxExtractedROM)
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		os.Remove(destPath)
		if err == nil {
			err = closeErr
		}
		return "", err
	}
	return destPath, nil
}

// archiveCandidates returns up to `limit` uploads whose CONTENT is an
// archive, in ranked order.
//
// One candidate is not enough when the upload names give no clue which
// console a build is for ("release-a.zip", "release-b.zip"): the top-ranked
// guess may be the Windows build, and giving up after it would fail a game
// whose PSX archive was second in the list. Returning several lets the caller
// keep trying until one actually contains something this console can run.
func archiveCandidates(ctx context.Context, client *itchio.Client, uploads []roms.Upload,
	platform string, limit int) []roms.Upload {

	var found []roms.Upload
	for _, index := range rankUploadsByName(uploads, platform) {
		if len(found) >= limit {
			break
		}
		select {
		case <-ctx.Done():
			return found
		default:
		}
		candidate := uploads[index]
		// A name that already says ".zip"/".7z" needs no network check.
		if ext := strings.ToLower(filepath.Ext(candidate.Filename)); ext == ".zip" || ext == ".7z" {
			found = append(found, candidate)
			continue
		}
		up := itchio.Upload{
			Filename: candidate.Filename, URL: candidate.URL,
			UploadID: candidate.UploadID, NeedsFormat: candidate.NeedsFormat,
		}
		cdnURL, err := client.ResolveFreeURLContext(ctx, up)
		if err != nil {
			continue
		}
		head, err := client.FetchFileHeader(cdnURL, 1024)
		if err != nil || len(head) < 8 {
			continue
		}
		switch {
		case head[0] == 'P' && head[1] == 'K':
			candidate.Filename = ensureExt(candidate.Filename, ".zip")
			found = append(found, candidate)
		case string(head[:6]) == "7z\xbc\xaf\x27\x1c":
			candidate.Filename = ensureExt(candidate.Filename, ".7z")
			found = append(found, candidate)
		}
	}
	return found
}

// sniffUploads inspects upload *content* to decide what can be installed,
// for pages whose upload names carry no usable extension. itch.io shows
// display names, not filenames: Goodboy Galaxy's page lists "Goodboy Galaxy
// DEMO for GBA (roms only)", which is a perfectly good GBA download that no
// name-based rule can recognise. Only the first bytes of each candidate are
// fetched (a Range request), so this is cheap even for an 80 MB upload.
//
// Returns the chosen upload and the extension its content actually is, or
// nil when nothing on the page is installable.
func sniffUploads(ctx context.Context, client *itchio.Client, uploads []roms.Upload, platform string) (*roms.Upload, string) {
	for _, index := range rankUploadsByName(uploads, platform) {
		select {
		case <-ctx.Done():
			return nil, ""
		default:
		}
		up := itchio.Upload{
			Filename: uploads[index].Filename, URL: uploads[index].URL,
			UploadID: uploads[index].UploadID, NeedsFormat: uploads[index].NeedsFormat,
		}
		cdnURL, err := client.ResolveFreeURLContext(ctx, up)
		if err != nil {
			continue
		}
		head, err := client.FetchFileHeader(cdnURL, 4096)
		if err != nil || len(head) < 8 {
			continue
		}
		switch {
		case head[0] == 'P' && head[1] == 'K':
			uploads[index].Filename = ensureExt(uploads[index].Filename, ".zip")
			return &uploads[index], ".zip"
		case string(head[:6]) == "7z\xbc\xaf\x27\x1c":
			uploads[index].Filename = ensureExt(uploads[index].Filename, ".7z")
			return &uploads[index], ".7z"
		}
		// Not an archive — maybe the upload is a bare ROM with no extension
		// in its display name. roms.DetectROMExt identifies by content.
		if ext := roms.DetectROMExt(head); ext != "" && roms.DestinationDir(ext) != "" {
			uploads[index].Filename = ensureExt(uploads[index].Filename, ext)
			return &uploads[index], ext
		}
	}
	return nil, ""
}

// platformKeywords maps a catalog platform code to the words its uploads are
// named with on itch.io.
var platformKeywords = map[string][]string{
	"PSX":   {"psx", "playstation", "ps1", "psone"},
	"PS":    {"psx", "playstation", "ps1", "psone"},
	"GBA":   {"gba", "game boy advance", "gameboy advance", "advance"},
	"GBC":   {"gbc", "game boy color", "gameboy color"},
	"GB":    {"game boy", "gameboy", "gb "},
	"FC":    {"nes", "famicom"},
	"NES":   {"nes", "famicom"},
	"MD":    {"genesis", "mega drive", "megadrive", "md "},
	"P8":    {"pico-8", "pico8"},
	"PICO8": {"pico-8", "pico8"},
}

// rankUploadsByName returns upload indices most-likely-first for the console
// the game is listed under.
//
// This matters far more than it looks. A release like Loonies 8192 publishes
// one upload PER platform — DOS, 3DS, Android, PSP, PSX, Vita, Win32, macOS,
// GBA, GameCube, PS2, Wii, NDS — with display names and no file extensions.
// Without knowing which console we are installing for, the "first plausible
// archive" is as likely to be the Android build as the PSX one.
func rankUploadsByName(uploads []roms.Upload, platform string) []int {
	wanted := platformKeywords[strings.ToUpper(platform)]

	// Other consoles' builds are actively wrong for us, as are desktop and
	// mobile ones.
	otherConsoles := []string{"3ds", "nds", "vita", "psp", "ps2", "wii", "gamecube",
		"gc ", "n64", "dreamcast", "saturn", "switch", "pocketchip", "n800", "n900", "n9 "}
	desktop := []string{"windows", "win32", "win64", "win9x", "dos", "macos", "osx",
		"linux", "android", "apk", "soundtrack", "sound pack", "ost", "manual", "source"}

	type scored struct{ index, score int }
	ranked := make([]scored, 0, len(uploads))
	for i, u := range uploads {
		name := strings.ToLower(u.Filename)
		score := 0
		for _, kw := range wanted {
			if strings.Contains(name, kw) {
				score += 10 // the console we are actually installing for
				break
			}
		}
		for _, kw := range otherConsoles {
			if strings.Contains(name, kw) {
				// Only a penalty when it is NOT also our platform: "PSX" and
				// "PSP" both contain "ps", so matching is word-ish above.
				score -= 8
				break
			}
		}
		for _, kw := range desktop {
			if strings.Contains(name, kw) {
				score -= 6
				break
			}
		}
		if strings.Contains(name, "rom") {
			score += 2
		}
		ranked = append(ranked, scored{i, score})
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })
	out := make([]int, len(ranked))
	for i, r := range ranked {
		out[i] = r.index
	}
	return out
}

// ensureExt appends ext when name doesn't already end in it, so the rest of
// the pipeline (which dispatches on extension) sees what the bytes actually are.
func ensureExt(name, ext string) string {
	if strings.EqualFold(filepath.Ext(name), ext) {
		return name
	}
	return name + ext
}

// sidecarFormats are ROM formats that are only an INDEX: the actual game data
// lives in companion files beside them.
//
// A .cue is a few lines of text naming .bin tracks; a .m3u lists discs; a
// .gdi indexes Dreamcast tracks. Downloading one of these on its own gives a
// pointer to files that were never fetched, and the emulator fails at load —
// which is exactly what "exit status 1" on a PlayStation game turned out to
// be. These always need the page's full archive.
var sidecarFormats = map[string]bool{
	".cue": true, ".m3u": true, ".gdi": true, ".toc": true, ".ccd": true,
}

func needsCompanionFiles(filename string) bool {
	return sidecarFormats[strings.ToLower(filepath.Ext(filename))]
}

// bundleExtensions are the files worth unpacking for a multi-file release:
// index formats, their data tracks, PICO-8 carts and their includes.
var bundleExtensions = map[string]bool{
	".cue": true, ".bin": true, ".m3u": true, ".gdi": true, ".toc": true,
	".ccd": true, ".sub": true, ".img": true, ".iso": true, ".chd": true,
	".p8": true, ".lua": true, ".png": true, ".txt": true,
}

// cartNeedsIncludes reports whether a PICO-8 cart pulls in other files with
// #include. Such a cart is only half a release: fake08 loads it, fails with
// "Cart load error: Can't find included file", and falls back to its default
// cart. The rest of the files live in the page's archive upload.
func cartNeedsIncludes(path string) bool {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".p8" {
		return false // .p8.png carts are self-contained by construction
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte("#include"))
}

// extractBundle unpacks a complete multi-file release into destDir,
// preserving the archive's relative layout so that .cue track references and
// PICO-8 #include paths still resolve. Returns the file the emulator should
// be pointed at: the index file when there is one, otherwise the main cart.
func extractBundle(zipPath, destDir string) (string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer reader.Close()

	var files []*zip.File
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || strings.HasPrefix(file.Name, "__MACOSX/") {
			continue
		}
		if bundleExtensions[strings.ToLower(filepath.Ext(file.Name))] {
			files = append(files, file)
		}
	}
	wrapper := bundleWrapper(files)

	mainFile, indexFile := "", ""
	written := 0
	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file.Name))

		// Drop the folder the archive wraps everything in, when it does:
		// #include paths are relative to the cart itself. Only a folder
		// shared by every file is dropped — stripping each file's first
		// directory turned "lib/util.lua" beside a top-level cart into
		// "util.lua", and the cart's #include "lib/util.lua" then failed.
		rel := strings.TrimPrefix(archiveName(file), wrapper)
		if !safeRelPath(rel) {
			continue
		}
		outPath := filepath.Join(destDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			continue
		}

		source, err := file.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(outPath)
		if err != nil {
			source.Close()
			continue
		}
		_, copyErr := copyCapped(out, source, maxExtractedTrack)
		out.Close()
		source.Close()
		if errors.Is(copyErr, errTooLarge) {
			// A disc missing a track is no install at all.
			os.Remove(outPath)
			return "", fmt.Errorf("%s: %w", rel, copyErr)
		}
		if copyErr != nil {
			os.Remove(outPath)
			continue
		}
		written++
		// The emulator is pointed at the index file when the release has
		// one (.cue/.m3u/...), since that is what references the data
		// tracks; otherwise at the top-level cart.
		if sidecarFormats[ext] && indexFile == "" {
			indexFile = outPath
		}
		if ext == ".p8" && !strings.Contains(rel, "/") && mainFile == "" {
			mainFile = outPath
		}
	}
	if written == 0 {
		return "", fmt.Errorf("archive has no game files this console can run")
	}
	if indexFile != "" {
		return indexFile, nil
	}
	if mainFile == "" {
		return "", fmt.Errorf("archive has no playable file at its top level")
	}
	return mainFile, nil
}

// bundleWrapper returns the "folder/" every file of a release sits in, or
// "" when they do not all share one.
func bundleWrapper(files []*zip.File) string {
	wrapper := ""
	for i, f := range files {
		name := archiveName(f)
		idx := strings.Index(name, "/")
		if idx < 0 {
			return "" // a file at the top level: nothing wraps the release
		}
		first := name[:idx+1]
		if i == 0 {
			wrapper = first
		} else if first != wrapper {
			return ""
		}
	}
	return wrapper
}

// archiveName is an entry's path with "/" separators. Zips made on
// Windows sometimes store "Game\\Track 01.bin".
func archiveName(f *zip.File) string {
	return strings.ReplaceAll(f.Name, "\\", "/")
}

// safeRelPath reports whether a path from an archive stays inside the
// folder it is extracted to. ".." is refused as a path element only, so a
// file legitimately named "Game..v2.bin" still extracts.
func safeRelPath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// downloadAndExtractBundle fetches an archive upload and unpacks the complete
// release into destDir, returning the file to install/launch.
func downloadAndExtractBundle(ctx context.Context, client *itchio.Client,
	upload roms.Upload, destDir string) (string, error) {

	tmp, err := roms.CreateTemp("itchio-bundle-*.zip")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	up := itchio.Upload{
		Filename: upload.Filename, URL: upload.URL,
		UploadID: upload.UploadID, NeedsFormat: upload.NeedsFormat,
	}
	if err := client.DownloadFreeContext(ctx, up, tmpPath, nil); err != nil {
		return "", fmt.Errorf("download archive: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	return extractBundle(tmpPath, destDir)
}

// companionExtensions are the data files an index format points at.
var companionExtensions = map[string]bool{
	".bin": true, ".img": true, ".iso": true, ".sub": true, ".ccd": true,
	".chd": true, ".wav": true, ".mp3": true, ".ogg": true,
}

// isCompanionUpload reports whether an upload looks like a data track that
// belongs beside an index file.
func isCompanionUpload(filename string) bool {
	return companionExtensions[strings.ToLower(filepath.Ext(filename))]
}

// downloadLooseSet installs an index file together with every companion
// upload listed on the same page.
//
// itch.io releases frequently publish a .cue and its .bin tracks as SEPARATE
// uploads rather than one archive. There is no bundle to extract in that
// case, so each file has to be fetched individually into the same directory —
// which is what the .cue's relative "FILE" lines expect.
func downloadLooseSet(ctx context.Context, client *itchio.Client, uploads []roms.Upload,
	index roms.Upload, destDir string) (string, int, error) {

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", 0, err
	}

	fetch := func(u roms.Upload) error {
		name := filepath.Base(u.Filename)
		if name == "" || name == "." {
			return fmt.Errorf("upload has no usable name")
		}
		up := itchio.Upload{
			Filename: u.Filename, URL: u.URL,
			UploadID: u.UploadID, NeedsFormat: u.NeedsFormat,
		}
		return client.DownloadFreeContext(ctx, up, filepath.Join(destDir, name), nil)
	}

	// The index keeps its own filename: the track references inside it are
	// relative, so renaming it to the game title would be fine, but renaming
	// the tracks would break them. Keeping every original name is simplest
	// and always correct.
	if err := fetch(index); err != nil {
		return "", 0, fmt.Errorf("download %s: %w", index.Filename, err)
	}
	indexPath := filepath.Join(destDir, filepath.Base(index.Filename))

	companions := 0
	for _, u := range uploads {
		if u.Filename == index.Filename || !isCompanionUpload(u.Filename) {
			continue
		}
		if err := fetch(u); err != nil {
			fmt.Fprintf(os.Stderr, "companion %s failed: %v\n", u.Filename, err)
			continue
		}
		companions++
	}
	if companions == 0 {
		os.Remove(indexPath)
		return "", 0, fmt.Errorf("no data tracks found beside %s", filepath.Base(index.Filename))
	}
	return indexPath, companions, nil
}

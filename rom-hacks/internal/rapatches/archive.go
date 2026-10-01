package rapatches

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bodgit/sevenzip"
)

// maxArchive caps a single download to disk. PlayStation hacks are the
// large ones: three in the repository pass 70 MB, because an xdelta
// against a 600 MB disc is itself big. The file lands on the card, so
// this is a disk ceiling with 74 GB behind it, not a RAM one.
var maxArchive int64 = 1 << 30 // a var so tests can lower it

// maxInMemory is the separate, much lower ceiling for the calls that do
// buffer an archive — the background fingerprint pass. Sharing one
// constant with the disk limit would quietly let that pass pull a
// gigabyte into a gigabyte of RAM.
const maxInMemory = 64 << 20

// ErrTooLarge means the archive is bigger than the caller is willing to
// hold in memory. It is not a failure of the archive: fetching it to disk
// still works.
var ErrTooLarge = errors.New("archive is larger than this app will download")

// eagerEntryBytes is how large a file inside an archive may be before it
// is left on disk instead of being decompressed into memory.
//
// Everything used to be expanded eagerly, which on a 1 GB device meant a
// 78 MB xdelta plus the compressed copy plus whatever else the app was
// holding. Small files — readmes, cue sheets, cartridge patches — are
// cheap and are read straight away; anything bigger waits until someone
// asks for it, and then only that one is read.
const eagerEntryBytes = 8 << 20

// Payload is the useful content of a patch archive.
type Payload struct {
	Patches []PatchFile
	Readme  string
	Bases   []BaseROM // parsed from Readme, possibly empty
	// Extras are the archive's other files. A PlayStation hack ships a
	// .cue that has to travel with the patched track, so these are kept
	// rather than discarded.
	Extras []PatchFile
}

// PatchFile is one patch inside the archive.
// Data is filled for small entries and left nil for large ones; Bytes()
// reads those on demand. The distinction matters only on a device where
// expanding every patch in an archive at once is the difference between
// working and being killed: three PlayStation hacks ship patches past
// 70 MB.
type PatchFile struct {
	Name string // path inside the archive
	Size int64
	Data []byte

	load func() ([]byte, error)
}

// Bytes returns the patch's contents, reading them from the archive if
// they were too large to keep in memory. The result is cached, so asking
// twice costs one read.
func (p *PatchFile) Bytes() []byte {
	data, _ := p.Load()
	return data
}

// Load is Bytes with the read error kept. Bytes returns nil when the entry
// cannot be read, which callers then reported as "not a patch format this
// app understands" — true of nil, and no help with a damaged archive.
func (p *PatchFile) Load() ([]byte, error) {
	if p.Data != nil || p.load == nil {
		return p.Data, nil
	}
	data, err := p.load()
	if err != nil {
		return nil, err
	}
	p.Data = data
	return data, nil
}

// Stem is the filename without its extension. RetroAchievements names a
// supported hash after the ROM the patch produces — "Foo (v2.4).gba" for
// "Foo (v2.4).bps" — so this is what links a patch to the hash it will
// satisfy when an archive holds several versions of the same hack.
func (p PatchFile) Stem() string {
	base := path.Base(p.Name)
	if i := strings.LastIndex(base, "."); i > 0 {
		return base[:i]
	}
	return base
}

var patchExts = map[string]bool{
	".bps": true, ".ips": true, ".ups": true, ".xdelta": true, ".vcdiff": true,
}

// Download fetches an entry's archive into memory, refusing anything
// past limit. Pass 0 for the default ceiling. Anything that may be large
// should use DownloadTo instead.
func Download(ctx context.Context, client *http.Client, e Entry, limit int64) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if limit <= 0 || limit > maxInMemory {
		limit = maxInMemory
	}
	resp, err := get(ctx, client, e)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Checked before reading a byte where the server says: a refusal that
	// costs nothing is better than one that costs the download.
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%s: %w (%d MB)", e.File, ErrTooLarge, resp.ContentLength>>20)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w", e.File, ErrTooLarge)
	}
	return data, nil
}

// DownloadTo streams an archive to a file and returns its path. The
// caller removes it.
//
// Streaming rather than buffering is what makes a 78 MB PlayStation patch
// possible at all here: the card has 74 GB free and the device has one
// gigabyte of RAM.
func DownloadTo(ctx context.Context, client *http.Client, e Entry, dir string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	// The shared client's Timeout covers the whole body, and two minutes
	// is not enough for an 80 MB PlayStation patch on handheld Wi-Fi: the
	// download failed every time on a slow connection. Here the only limit
	// is a stall — StallTimeout without a byte cancels it.
	dl := *client
	dl.Timeout = 0
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	watchdog := time.AfterFunc(StallTimeout, func() {
		stalled.Store(true)
		cancel()
	})
	defer watchdog.Stop()
	stallErr := func(err error) error {
		if stalled.Load() {
			return fmt.Errorf("download %s: %w: no data for %s", e.File, ErrStalled, StallTimeout)
		}
		return err
	}

	resp, err := get(ctx, &dl, e)
	if err != nil {
		return "", stallErr(err)
	}
	defer resp.Body.Close()

	if resp.ContentLength > maxArchive {
		return "", fmt.Errorf("%s: %w (%d MB)", e.File, ErrTooLarge, resp.ContentLength>>20)
	}
	f, err := os.CreateTemp(dir, "patch-*"+path.Ext(e.File))
	if err != nil {
		return "", err
	}
	// One byte past the cap tells "exactly at the limit" from "cut off".
	// Stopping at the cap and calling it done kept the first gigabyte of a
	// bigger archive, which then failed as "not a valid zip".
	n, err := io.Copy(f, io.LimitReader(&progressReader{r: resp.Body, onRead: func() { watchdog.Reset(StallTimeout) }}, maxArchive+1))
	if err == nil && n > maxArchive {
		err = ErrTooLarge
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", stallErr(fmt.Errorf("download %s: %w", e.File, err))
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// StallTimeout is how long a download may receive nothing before it is
// abandoned. A var so tests can shorten it.
var StallTimeout = 60 * time.Second

// ErrStalled means the connection stopped delivering data.
var ErrStalled = errors.New("download stalled")

// progressReader calls onRead whenever bytes arrive.
type progressReader struct {
	r      io.Reader
	onRead func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.onRead()
	}
	return n, err
}

func get(ctx context.Context, client *http.Client, e Entry) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.DownloadURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "leaf-hacks")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", e.File, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: HTTP %d", e.File, resp.StatusCode)
	}
	return resp, nil
}

// OpenFile reads an archive from disk. The returned closer must be held
// until the payload is finished with, because large entries are read out
// of the file on demand rather than kept in memory.
func OpenFile(name string) (Payload, io.Closer, error) {
	f, err := os.Open(name)
	if err != nil {
		return Payload{}, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return Payload{}, nil, err
	}

	var entries []archiveEntry
	if strings.EqualFold(path.Ext(name), ".7z") {
		// sevenzip needs its own handle on the path.
		f.Close()
		zr, err := sevenzip.OpenReader(name)
		if err != nil {
			return Payload{}, nil, fmt.Errorf("7z: %w", err)
		}
		entries, err = entries7z(zr.File)
		if err != nil {
			zr.Close()
			return Payload{}, nil, err
		}
		p, err := assemble(entries)
		return p, zr, err
	}

	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		f.Close()
		return Payload{}, nil, fmt.Errorf("zip: %w", err)
	}
	entries, err = entriesZip(zr.File)
	if err != nil {
		f.Close()
		return Payload{}, nil, err
	}
	p, err := assemble(entries)
	return p, f, err
}

// Open reads a downloaded archive, whether zip or 7z, and returns the
// patches and readme inside it.
func Open(name string, data []byte) (Payload, error) {
	var entries []archiveEntry
	var err error
	if strings.EqualFold(path.Ext(name), ".7z") {
		entries, err = read7z(data)
	} else {
		entries, err = readZip(data)
	}
	if err != nil {
		return Payload{}, err
	}

	return assemble(entries)
}

func assemble(entries []archiveEntry) (Payload, error) {
	var p Payload
	for _, e := range entries {
		lower := strings.ToLower(e.name)
		file := PatchFile{Name: e.name, Size: e.size, Data: e.data, load: e.load}
		switch {
		case patchExts[path.Ext(lower)]:
			p.Patches = append(p.Patches, file)
		case strings.HasSuffix(lower, ".txt") && p.Readme == "":
			p.Readme = string(file.Bytes())
		default:
			p.Extras = append(p.Extras, file)
		}
	}
	if p.Readme != "" {
		p.Bases = ParseReadme(p.Readme)
	}
	if len(p.Patches) == 0 {
		return p, errors.New("this archive contains no patch file")
	}

	// Shallow files first: several archives bundle an "Original Patches
	// and Extras" folder full of optional tweaks, and the patch meant for
	// RetroAchievements is always the one at the top level.
	sort.SliceStable(p.Patches, func(i, j int) bool {
		di := strings.Count(p.Patches[i].Name, "/")
		dj := strings.Count(p.Patches[j].Name, "/")
		if di != dj {
			return di < dj
		}
		return p.Patches[i].Name < p.Patches[j].Name
	})
	return p, nil
}

// Select picks the patch that produces the named ROM. wantStem is the
// RetroAchievements hash name without its extension; when it matches
// nothing (or is empty) the top-level patch is used, which is the right
// answer for the common single-patch archive.
func (p Payload) Select(wantStem string) (PatchFile, bool) {
	if wantStem != "" {
		for _, f := range p.Patches {
			if strings.EqualFold(f.Stem(), wantStem) {
				return f, true
			}
		}
	}
	for _, f := range p.Patches {
		if !strings.Contains(f.Name, "/") {
			return f, false
		}
	}
	return p.Patches[0], false
}

// --- archive readers --------------------------------------------------

type archiveEntry struct {
	name string
	size int64
	data []byte // nil when the entry was left on disk
	load func() ([]byte, error)
}

func readZip(data []byte) ([]archiveEntry, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	return entriesZip(zr.File)
}

func entriesZip(files []*zip.File) ([]archiveEntry, error) {
	var out []archiveEntry
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		file := f
		entry := archiveEntry{
			name: file.Name,
			size: file.FileInfo().Size(),
			load: func() ([]byte, error) { return readZipEntry(file) },
		}
		if entry.size <= eagerEntryBytes {
			b, err := readZipEntry(file)
			if err != nil {
				return nil, err
			}
			entry.data = b
		}
		out = append(out, entry)
	}
	return out, nil
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("zip %s: %w", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxArchive))
	if err != nil {
		return nil, fmt.Errorf("zip %s: %w", f.Name, err)
	}
	return b, nil
}

func read7z(data []byte) ([]archiveEntry, error) {
	zr, err := sevenzip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("7z: %w", err)
	}
	return entries7z(zr.File)
}

func entries7z(files []*sevenzip.File) ([]archiveEntry, error) {
	var out []archiveEntry
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		file := f
		entry := archiveEntry{
			name: file.Name,
			size: file.FileInfo().Size(),
			load: func() ([]byte, error) { return read7zEntry(file) },
		}
		// 7z has no cheap random access: reading one entry can mean
		// decoding the solid block around it, so small files are still
		// read up front and only the big ones are deferred.
		if entry.size <= eagerEntryBytes {
			b, err := read7zEntry(file)
			if err != nil {
				return nil, err
			}
			entry.data = b
		}
		out = append(out, entry)
	}
	return out, nil
}

func read7zEntry(f *sevenzip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("7z %s: %w", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxArchive))
	if err != nil {
		return nil, fmt.Errorf("7z %s: %w", f.Name, err)
	}
	return b, nil
}

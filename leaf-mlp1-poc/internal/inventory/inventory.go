package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"leaf-mlp1-poc/internal/leaf"
	"leaf-mlp1-poc/internal/logger"
	"leaf-mlp1-poc/internal/roms"
)

const (
	ContentKindROM     = "rom"
	ContentKindMusic   = "music"
	ContentKindArtwork = "artwork"

	FileTypeROM   = ContentKindROM
	FileTypeMusic = ContentKindMusic
	FileTypeM3U   = "m3u" // legacy UI subtype; inventory content_kind remains ROM

	SchemaVersion = 2
)

var ErrUnsupportedSchema = errors.New("unsupported inventory schema")

// romFileExt returns the effective file extension for a ROM filename, treating
// ".p8.png" as a single compound extension rather than just ".png".
// filepath.Ext alone would give ".png" for "game.p8.png", producing a wrong
// stem ("game.p8") that fails upstream filename matching in the update service.
//
// Implementation uses zero-allocation byte-level comparison. The previous
// strings.ToLower approach allocated a full string copy on every call; this
// function is invoked from HasPendingUpdates which is called per-visible-row
// per-frame, making the allocation cost significant (~945K objects/session).
func romFileExt(filename string) string {
	const p8png = ".p8.png"
	if len(filename) >= len(p8png) {
		s := filename[len(filename)-len(p8png):]
		if s[0] == '.' &&
			(s[1] == 'p' || s[1] == 'P') &&
			s[2] == '8' &&
			s[3] == '.' &&
			(s[4] == 'p' || s[4] == 'P') &&
			(s[5] == 'n' || s[5] == 'N') &&
			(s[6] == 'g' || s[6] == 'G') {
			return p8png
		}
	}
	return filepath.Ext(filename)
}

type DownloadedFile struct {
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	ContentKind     string    `json:"content_kind"`
	SourceID        string    `json:"source_id,omitempty"`
	RelativePath    string    `json:"relative_path,omitempty"`
	CanonicalSystem string    `json:"canonical_system,omitempty"`
	OriginalUpload  string    `json:"original_upload,omitempty"`
	InstalledName   string    `json:"installed_name,omitempty"`
	UploadID        string    `json:"upload_id,omitempty"`
	PurchaseID      string    `json:"purchase_id,omitempty"`
	ContentHash     string    `json:"content_hash,omitempty"`
	ArtworkPath     string    `json:"artwork_path,omitempty"`
	ArtworkHash     string    `json:"artwork_hash,omitempty"`
	ArtworkCreated  bool      `json:"artwork_created,omitempty"`

	// Legacy compatibility fields remain available to the existing UI while its
	// callers move to source-relative Leaf paths during later port phases.
	Filename      string    `json:"filename,omitempty"`
	DestPath      string    `json:"dest_path,omitempty"`
	DownloadedAt  time.Time `json:"downloaded_at,omitempty"`
	UnifiedName   bool      `json:"unified_name,omitempty"`
	FileType      string    `json:"file_type,omitempty"`
	SourceArchive string    `json:"source_archive,omitempty"`
}

type UpstreamFile struct {
	Filename string    `json:"filename"`
	UploadID string    `json:"upload_id"`
	SeenAt   time.Time `json:"seen_at"`
	IsNew    bool      `json:"is_new,omitempty"`
}

type Entry struct {
	GameID                string           `json:"game_id,omitempty"`
	GameURL               string           `json:"game_url"`
	Title                 string           `json:"title"`
	Author                string           `json:"author"`
	CoverURL              string           `json:"cover_url"`
	Files                 []DownloadedFile `json:"files"`
	VerifiedAt            time.Time        `json:"verified_at,omitempty"`
	IsFree                bool             `json:"is_free,omitempty"`
	KnownUpstreamFiles    []UpstreamFile   `json:"known_upstream_files,omitempty"`
	UpdateCheckedAt       time.Time        `json:"update_checked_at,omitempty"`
	UpdateDismissedAt     time.Time        `json:"update_dismissed_at,omitempty"`
	GameRemovedAt         time.Time        `json:"game_removed_at,omitempty"`
	RemovalDismissedAt    time.Time        `json:"removal_dismissed_at,omitempty"`
	UnifiedNamingDisabled bool             `json:"unified_naming_disabled,omitempty"`
}

type Inventory struct {
	mu      sync.Mutex
	Version int               `json:"version"`
	Entries map[string]*Entry `json:"entries"`
}

func emptyInventory() *Inventory {
	return &Inventory{Version: SchemaVersion, Entries: make(map[string]*Entry)}
}

func backupInventory(path, label string) (string, error) {
	base := path + "." + label + ".bak"
	backup := base
	for n := 1; ; n++ {
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			break
		} else if err != nil {
			return "", fmt.Errorf("inspect inventory backup: %w", err)
		}
		backup = fmt.Sprintf("%s.%d", base, n)
	}
	if err := os.Rename(path, backup); err != nil {
		return "", fmt.Errorf("backup inventory: %w", err)
	}
	return backup, nil
}

// Load reads a current-schema inventory. Older, unknown, and malformed files
// are moved aside without partial interpretation so a fresh Leaf inventory can
// start safely.
func Load(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Debug("inventory: no file at %s, starting empty", path)
		} else {
			logger.Warn("inventory: read error at %s: %v, starting empty", path, err)
		}
		if os.IsNotExist(err) {
			return emptyInventory(), nil
		}
		return emptyInventory(), fmt.Errorf("read inventory: %w", err)
	}
	var inv Inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		backup, backupErr := backupInventory(path, "corrupt")
		if backupErr != nil {
			return emptyInventory(), fmt.Errorf("%w: malformed inventory (%v); %v", ErrUnsupportedSchema, err, backupErr)
		}
		logger.Warn("inventory: moved malformed file to %s: %v", backup, err)
		return emptyInventory(), fmt.Errorf("%w: malformed inventory backed up to %s", ErrUnsupportedSchema, backup)
	}
	if inv.Version != SchemaVersion {
		backup, backupErr := backupInventory(path, fmt.Sprintf("schema-%d", inv.Version))
		if backupErr != nil {
			return emptyInventory(), fmt.Errorf("%w: version %d; %v", ErrUnsupportedSchema, inv.Version, backupErr)
		}
		logger.Warn("inventory: moved schema %d file to %s", inv.Version, backup)
		return emptyInventory(), fmt.Errorf("%w: version %d backed up to %s", ErrUnsupportedSchema, inv.Version, backup)
	}
	if inv.Entries == nil {
		inv.Entries = make(map[string]*Entry)
	}
	logger.Debug("inventory: loaded %d entries from %s", len(inv.Entries), path)
	return &inv, nil
}

// Save writes the inventory to path atomically (write to .tmp then rename).
func (inv *Inventory) Save(path string) error {
	lease, err := leaf.BeginOperation(context.Background(), "inventory commit", false)
	if err != nil {
		return fmt.Errorf("protect inventory commit: %w", err)
	}
	defer lease.Release()
	inv.mu.Lock()
	inv.Version = SchemaVersion
	data, err := json.MarshalIndent(inv, "", "  ")
	count := len(inv.Entries)
	inv.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal inventory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write inventory tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename inventory: %w", err)
	}
	logger.Debug("inventory: saved %d entries to %s", count, path)
	return nil
}

// Add upserts an entry and appends a file, deduplicating by DestPath.
func (inv *Inventory) Add(gameURL string, e Entry, file DownloadedFile) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.Version = SchemaVersion
	if file.ContentKind == "" {
		switch file.FileType {
		case FileTypeMusic:
			file.ContentKind = ContentKindMusic
		default:
			file.ContentKind = ContentKindROM
		}
	}
	if file.OriginalUpload == "" {
		file.OriginalUpload = file.Filename
	}
	if file.InstalledName == "" && file.DestPath != "" {
		file.InstalledName = filepath.Base(file.DestPath)
	}
	if file.UpdatedAt.IsZero() {
		file.UpdatedAt = file.DownloadedAt
	}
	if identity, ok := roms.DescribeDestination(file.DestPath); ok {
		if file.SourceID == "" {
			file.SourceID = identity.SourceID
		}
		if file.RelativePath == "" {
			file.RelativePath = identity.RelativePath
		}
		if file.CanonicalSystem == "" {
			file.CanonicalSystem = identity.CanonicalSystem
		}
	}
	existing, ok := inv.Entries[gameURL]
	if !ok {
		entry := &Entry{
			GameID:   e.GameID,
			GameURL:  gameURL,
			Title:    e.Title,
			Author:   e.Author,
			CoverURL: e.CoverURL,
			IsFree:   e.IsFree,
		}
		inv.Entries[gameURL] = entry
		existing = entry
	} else {
		if e.GameID != "" {
			existing.GameID = e.GameID
		}
		existing.Title = e.Title
		existing.Author = e.Author
		existing.CoverURL = e.CoverURL
	}
	for i, f := range existing.Files {
		if f.DestPath == file.DestPath || f.Filename == file.Filename {
			if file.ArtworkPath == "" {
				file.ArtworkPath = f.ArtworkPath
				file.ArtworkHash = f.ArtworkHash
				file.ArtworkCreated = f.ArtworkCreated
			}
			existing.Files[i] = file // overwrite in place (re-download or path change)
			return
		}
	}
	existing.Files = append(existing.Files, file)
}

// Remove deletes the entry for gameURL.
func (inv *Inventory) Remove(gameURL string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	delete(inv.Entries, gameURL)
}

// Lookup returns a deep copy of the entry for gameURL.
func (inv *Inventory) Lookup(gameURL string) (Entry, bool) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return Entry{}, false
	}
	snap := *e
	snap.Files = append([]DownloadedFile(nil), e.Files...)
	return snap, true
}

// ExistingDestPath returns the dest_path of an already-downloaded file matching
// the given upload filename, or "" if not found. Used to overwrite an existing
// download rather than creating a duplicate.
func (inv *Inventory) ExistingDestPath(gameURL, uploadFilename string) string {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return ""
	}
	for _, f := range e.Files {
		if f.Filename == uploadFilename {
			return f.DestPath
		}
	}
	return ""
}

// IsPresent reports whether gameURL has an inventory entry with at least one file.
// Assumes VerifyAndClean has already removed entries whose files are gone from disk.
func (inv *Inventory) IsPresent(gameURL string) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return false
	}
	return len(e.Files) > 0
}

// RemoveFile removes the DownloadedFile with the given destPath from the entry for gameURL.
// Returns true when no files remain (the entry is also removed from the inventory).
func (inv *Inventory) RemoveFile(gameURL, destPath string) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	entry, ok := inv.Entries[gameURL]
	if !ok {
		return true
	}
	var remaining []DownloadedFile
	for _, f := range entry.Files {
		if f.DestPath != destPath {
			remaining = append(remaining, f)
		}
	}
	entry.Files = remaining
	if len(entry.Files) == 0 {
		delete(inv.Entries, gameURL)
		return true
	}
	return false
}

// VerifyAndClean walks all entries, removes DownloadedFile rows whose DestPath no
// longer exists on disk, deduplicates rows with the same Filename (keeping the
// most recently downloaded), removes Entry values with no remaining files, saves
// if any changes were made, and returns the count of removed DownloadedFile rows.
func (inv *Inventory) VerifyAndClean(path string) int {
	return inv.verifyAndClean(path, nil)
}

// VerifyAndCleanWithSources keeps entries that live on a currently unavailable
// removable source. Absence of a card is not evidence that its files were
// deleted; those rows remain visible but immutable until the source returns.
func (inv *Inventory) VerifyAndCleanWithSources(path string, sources leaf.SourceList) int {
	return inv.verifyAndClean(path, sources)
}

// RepairArchiveRootROMs repairs files written by the pre-0.1.0 archive picker
// join bug. That bug could place an app-owned extracted ROM directly in a
// source's Roms directory when the selected canonical directory did not end in
// a path separator. Only archive-backed inventory rows in exactly that shape
// are eligible; user files and occupied canonical targets are left untouched.
func (inv *Inventory) RepairArchiveRootROMs(path string, sources leaf.SourceList) int {
	repaired := 0
	inv.mu.Lock()
	for _, entry := range inv.Entries {
		for index := range entry.Files {
			file := entry.Files[index]
			if file.ContentKind != ContentKindROM || file.SourceArchive == "" ||
				file.CanonicalSystem != "" || file.DestPath == "" {
				continue
			}
			identity, ok := roms.DescribeDestination(file.DestPath)
			if !ok || identity.SourceID == "" || identity.CanonicalSystem != "" ||
				filepath.ToSlash(filepath.Dir(identity.RelativePath)) != "Roms" {
				continue
			}
			source, ok := sources.ByID(identity.SourceID)
			if !ok || !source.Available() {
				continue
			}
			canonical, ok := leaf.CanonicalSystemForExtension(roms.ROMExt(file.DestPath))
			if !ok {
				continue
			}
			targetDir := roms.SourceSystemDir(identity.SourceID, canonical)
			if targetDir == "" {
				continue
			}
			target := filepath.Join(targetDir, filepath.Base(file.DestPath))
			if _, err := os.Stat(target); err == nil || !os.IsNotExist(err) {
				continue
			}
			info, err := os.Stat(file.DestPath)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				logger.Warn("inventory: create archive repair destination: %v", err)
				continue
			}
			if err := os.Rename(file.DestPath, target); err != nil {
				logger.Warn("inventory: move misplaced archive ROM: %v", err)
				continue
			}
			targetIdentity, ok := roms.DescribeDestination(target)
			if !ok || targetIdentity.CanonicalSystem != canonical {
				if rollbackErr := os.Rename(target, file.DestPath); rollbackErr != nil {
					logger.Error("inventory: archive repair rollback failed: %v", rollbackErr)
				}
				continue
			}
			file.DestPath = target
			file.RelativePath = targetIdentity.RelativePath
			file.CanonicalSystem = targetIdentity.CanonicalSystem
			file.InstalledName = filepath.Base(target)
			file.Filename = filepath.Base(target)
			entry.Files[index] = file
			repaired++
			logger.Info("inventory: repaired archive ROM destination %s", target)
		}
	}
	inv.mu.Unlock()
	if repaired > 0 {
		if err := inv.Save(path); err != nil {
			logger.Error("inventory: failed to save archive destination repairs: %v", err)
		}
	}
	return repaired
}

func (inv *Inventory) verifyAndClean(path string, sources leaf.SourceList) int {
	removed := 0
	changed := false
	inv.mu.Lock()
	for gameURL, entry := range inv.Entries {
		// Pass 1: drop files missing from disk.
		var present []DownloadedFile
		for _, f := range entry.Files {
			if sourceUnavailableForFile(f, sources) {
				present = append(present, f)
				continue
			}
			if _, err := os.Stat(f.DestPath); err == nil {
				present = append(present, f)
			} else {
				logger.Debug("inventory: removing stale file=%s", f.DestPath)
				removed++
				changed = true
			}
		}

		// Pass 2: deduplicate by Filename, keeping the most recently downloaded.
		best := make(map[string]DownloadedFile, len(present))
		for _, f := range present {
			if cur, ok := best[f.Filename]; !ok || f.DownloadedAt.After(cur.DownloadedAt) {
				best[f.Filename] = f
			}
		}
		if len(best) < len(present) {
			dropped := len(present) - len(best)
			logger.Debug("inventory: deduplicating %d file(s) for game=%q", dropped, entry.Title)
			removed += dropped
			changed = true
		}
		var kept []DownloadedFile
		for _, f := range best {
			kept = append(kept, f)
		}

		if len(kept) == 0 {
			logger.Debug("inventory: removing empty entry game=%q", entry.Title)
			delete(inv.Entries, gameURL)
		} else {
			entry.Files = kept
			entry.VerifiedAt = time.Now()
		}
	}
	inv.mu.Unlock()
	logger.Info("inventory: cleaned %d stale/duplicate file(s)", removed)
	if changed {
		if err := inv.Save(path); err != nil {
			logger.Error("inventory: failed to save after clean: %v", err)
		}
	}
	return removed
}

func sourceUnavailableForFile(file DownloadedFile, sources leaf.SourceList) bool {
	if len(sources) == 0 {
		return false
	}
	sourceID := file.SourceID
	if sourceID == "" {
		if identity, ok := roms.DescribeDestination(file.DestPath); ok {
			sourceID = identity.SourceID
		}
	}
	if sourceID == "" {
		return false
	}
	source, ok := sources.ByID(sourceID)
	return !ok || !source.Available()
}

// HasPendingUpdates returns true when any UpstreamFile for gameURL is marked
// as a new upload (appeared after the first check), has a filename not in the
// downloaded set, and was seen after UpdateDismissedAt.
func (inv *Inventory) HasPendingUpdates(gameURL string) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return false
	}
	downloaded := make(map[string]bool, len(e.Files)*3)
	for _, f := range e.Files {
		downloaded[f.Filename] = true
		// Format-picker appends an extension the upload name doesn't carry (e.g.
		// "Game Boy ROM.gbc" stored vs "Game Boy ROM" upstream). Also index the
		// stem so the already-downloaded file isn't treated as a new upload.
		if stem := strings.TrimSuffix(f.Filename, romFileExt(f.Filename)); stem != f.Filename {
			downloaded[stem] = true
		}
		// For files extracted from archives (ZIP/7z), also index the source archive
		// filename so an upstream re-upload of the same archive is correctly detected
		// rather than treated as a missing file.
		if f.SourceArchive != "" {
			downloaded[f.SourceArchive] = true
			if stem := strings.TrimSuffix(f.SourceArchive, filepath.Ext(f.SourceArchive)); stem != f.SourceArchive {
				downloaded[stem] = true
			}
		}
	}
	for _, u := range e.KnownUpstreamFiles {
		// Only flag genuinely new uploads (IsNew = true means appeared after first check).
		// Files discovered on first check were present when the user downloaded the game.
		if u.IsNew && !downloaded[u.Filename] && u.SeenAt.After(e.UpdateDismissedAt) {
			return true
		}
	}
	return false
}

// IsRemoved returns true when the game was detected as 404 upstream and the
// user has not yet dismissed the warning (or the warning reappeared after a
// subsequent removal).
func (inv *Inventory) IsRemoved(gameURL string) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return false
	}
	return !e.GameRemovedAt.IsZero() &&
		(e.RemovalDismissedAt.IsZero() || e.GameRemovedAt.After(e.RemovalDismissedAt))
}

// DismissUpdate sets UpdateDismissedAt to now, suppressing [UP] for all
// upstream files seen before this moment.
func (inv *Inventory) DismissUpdate(gameURL string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return
	}
	e.UpdateDismissedAt = time.Now()
}

// DismissRemoval sets RemovalDismissedAt to now, suppressing [!] until the
// game is re-detected as removed after reappearing upstream.
func (inv *Inventory) DismissRemoval(gameURL string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return
	}
	e.RemovalDismissedAt = time.Now()
}

// MarkRemoved sets GameRemovedAt to now only on the first detection
// (idempotent: does nothing if GameRemovedAt is already set).
func (inv *Inventory) MarkRemoved(gameURL string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok || !e.GameRemovedAt.IsZero() {
		return
	}
	e.GameRemovedAt = time.Now()
}

// MarkReachable clears GameRemovedAt and RemovalDismissedAt, returning the
// entry to a clean slate when a previously-removed game becomes reachable again.
func (inv *Inventory) MarkReachable(gameURL string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return
	}
	e.GameRemovedAt = time.Time{}
	e.RemovalDismissedAt = time.Time{}
}

// SetUpstreamFiles replaces KnownUpstreamFiles for gameURL and sets
// UpdateCheckedAt to now. Call this after each successful file-list scrape.
//
// SeenAt is PRESERVED for files that were already known so that a dismissed
// update is not re-triggered on the next check cycle. Only genuinely new files
// (not previously in KnownUpstreamFiles) receive SeenAt = now.
func (inv *Inventory) SetUpstreamFiles(gameURL string, files []UpstreamFile) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return
	}
	// isFirstCheck: no previous update run — files were already present when the
	// user downloaded the game and are not genuine new uploads.
	isFirstCheck := e.UpdateCheckedAt.IsZero()
	type priorInfo struct {
		seenAt time.Time
		isNew  bool
	}
	prior := make(map[string]priorInfo, len(e.KnownUpstreamFiles))
	for _, f := range e.KnownUpstreamFiles {
		prior[f.Filename] = priorInfo{seenAt: f.SeenAt, isNew: f.IsNew}
	}
	for i := range files {
		if p, ok := prior[files[i].Filename]; ok {
			files[i].SeenAt = p.seenAt // preserve original first-seen time
			files[i].IsNew = p.isNew   // preserve new-upload flag
		} else if !isFirstCheck {
			// Genuinely new file appearing after the first check — flag it.
			files[i].IsNew = true
			if files[i].SeenAt.IsZero() {
				files[i].SeenAt = time.Now()
			}
		}
		// if isFirstCheck: IsNew stays false (zero value); file was already present at download time
	}
	e.KnownUpstreamFiles = files
	e.UpdateCheckedAt = time.Now()
}

// LatestCheckedAt returns the most recent UpdateCheckedAt across all entries,
// or the zero time if no checks have run.
func (inv *Inventory) LatestCheckedAt() time.Time {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	var latest time.Time
	for _, e := range inv.Entries {
		if e.UpdateCheckedAt.After(latest) {
			latest = e.UpdateCheckedAt
		}
	}
	return latest
}

// AllURLs returns the game URLs of all inventory entries.
func (inv *Inventory) AllURLs() []string {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	urls := make([]string, 0, len(inv.Entries))
	for url := range inv.Entries {
		urls = append(urls, url)
	}
	return urls
}

// CoverArtPath returns the source-local canonical Jawaka image path for a
// downloaded ROM, mirroring the naming convention used by
// itchio.DownloadCoverArt.
// Returns "" if either argument is empty.
func CoverArtPath(coverURL, romDestPath string) string {
	if coverURL == "" || romDestPath == "" {
		return ""
	}
	return CanonicalArtworkPath(romDestPath)
}

func CanonicalArtworkPath(romDestPath string) string {
	return roms.ArtworkPath(romDestPath)
}

// SetUnifiedNamingDisabled sets the per-game unified-naming opt-out flag.
func (inv *Inventory) SetUnifiedNamingDisabled(gameURL string, disabled bool) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return
	}
	e.UnifiedNamingDisabled = disabled
}

// UpdateFile replaces the DownloadedFile whose DestPath matches oldDestPath.
// Returns false if the game URL or file is not found.
func (inv *Inventory) UpdateFile(gameURL, oldDestPath string, file DownloadedFile) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return false
	}
	for i, f := range e.Files {
		if f.DestPath == oldDestPath {
			e.Files[i] = file
			return true
		}
	}
	return false
}

// SetArtwork records the exact launcher-art path, hash, and ownership for one
// managed file without changing its ROM/music identity.
func (inv *Inventory) SetArtwork(gameURL, destPath, artPath, artHash string, created bool) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	e, ok := inv.Entries[gameURL]
	if !ok {
		return false
	}
	for index := range e.Files {
		if filepath.Clean(e.Files[index].DestPath) == filepath.Clean(destPath) {
			e.Files[index].ArtworkPath = artPath
			e.Files[index].ArtworkHash = artHash
			e.Files[index].ArtworkCreated = created
			return true
		}
	}
	return false
}

// ArtworkPathFor returns the recorded artwork path, falling back to the
// canonical path for inventories written before artwork metadata existed.
func ArtworkPathFor(coverURL string, file DownloadedFile) string {
	if file.ArtworkPath != "" {
		return file.ArtworkPath
	}
	return CoverArtPath(coverURL, file.DestPath)
}

// ArtworkReferencedOutside reports whether another managed file still owns the
// same artwork path after excluding a pending deletion set.
func (inv *Inventory) ArtworkReferencedOutside(artPath string, excluding []DownloadedFile) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	excluded := make(map[string]bool, len(excluding))
	for _, file := range excluding {
		excluded[filepath.Clean(file.DestPath)] = true
	}
	for _, entry := range inv.Entries {
		for _, file := range entry.Files {
			if excluded[filepath.Clean(file.DestPath)] || !file.ArtworkCreated {
				continue
			}
			if filepath.Clean(ArtworkPathFor(entry.CoverURL, file)) == filepath.Clean(artPath) {
				return true
			}
		}
	}
	return false
}

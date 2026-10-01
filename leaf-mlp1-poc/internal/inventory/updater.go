package inventory

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"leaf-mlp1-poc/internal/itchio"
	"leaf-mlp1-poc/internal/leaf"
	"leaf-mlp1-poc/internal/logger"
	"leaf-mlp1-poc/internal/roms"
)

// UpdateService checks each inventory entry for missing cover art, removed
// games, and new upstream files. It runs once at startup and re-runs each time
// TriggerNow is called.
type UpdateService struct {
	inv            *Inventory
	inventoryPath  string
	client         *itchio.Client
	notify         func()
	triggerCh      chan struct{} // buffered(1): absorbs duplicate triggers
	stopCh         chan struct{}
	stopOnce       sync.Once
	running        atomic.Bool
	sources        leaf.SourceList
	scanLibrary    func() (string, error)
	artworkChanged bool
}

func (s *UpdateService) SetSources(sources leaf.SourceList) {
	s.sources = append(leaf.SourceList(nil), sources...)
}

// SetLibraryScanRequester installs the Jawaka rescan hook used after startup
// artwork repair. It is configured before Start and remains optional in tests.
func (s *UpdateService) SetLibraryScanRequester(request func() (string, error)) {
	s.scanLibrary = request
}

// NewUpdateService constructs an UpdateService. notify (may be nil) is called
// after each runCheck completes; use it to push an SDL UserEvent from the
// caller without importing SDL here.
func NewUpdateService(inv *Inventory, inventoryPath string, client *itchio.Client, notify func()) *UpdateService {
	return &UpdateService{
		inv:           inv,
		inventoryPath: inventoryPath,
		client:        client,
		notify:        notify,
		triggerCh:     make(chan struct{}, 1),
		stopCh:        make(chan struct{}),
	}
}

// Start launches the background goroutine and runs the first check immediately.
// onDone is called after the first check completes (for tests; may be nil).
func (s *UpdateService) Start(onDone func()) {
	go func() {
		s.running.Store(true)
		s.runCheck()
		s.running.Store(false)
		if onDone != nil {
			onDone()
		}
		if s.notify != nil {
			s.notify()
		}
		for {
			select {
			case <-s.triggerCh:
				s.running.Store(true)
				s.runCheck()
				s.running.Store(false)
				if onDone != nil {
					onDone()
				}
				if s.notify != nil {
					s.notify()
				}
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop signals the goroutine to exit. Idempotent — safe to call multiple times.
func (s *UpdateService) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// TriggerNow queues a re-check. Non-blocking; a pending check absorbs the signal.
func (s *UpdateService) TriggerNow() {
	select {
	case s.triggerCh <- struct{}{}:
		logger.Info("update-svc: manual check triggered")
	default:
		logger.Debug("update-svc: trigger ignored (check already queued)")
	}
}

// IsRunning reports whether runCheck is currently executing.
func (s *UpdateService) IsRunning() bool {
	return s.running.Load()
}

// LatestCheckedAt delegates to the inventory's LatestCheckedAt.
func (s *UpdateService) LatestCheckedAt() time.Time {
	return s.inv.LatestCheckedAt()
}

func (s *UpdateService) runCheck() {
	s.artworkChanged = false
	s.inv.VerifyAndCleanWithSources(s.inventoryPath, s.sources)

	s.inv.mu.Lock()
	urls := make([]string, 0, len(s.inv.Entries))
	for url := range s.inv.Entries {
		urls = append(urls, url)
	}
	s.inv.mu.Unlock()

	logger.Info("update-svc: checking %d inventory entries", len(urls))

	// Collect upstream file lists during the check loop but do NOT write them
	// to the inventory yet. Applying them all at once at the end means the UI
	// never sees a state where badges have changed but sort order has not.
	pendingFiles := make(map[string][]UpstreamFile)
	for _, gameURL := range urls {
		if files := s.checkEntry(gameURL); files != nil {
			pendingFiles[gameURL] = files
		}
	}

	for gameURL, files := range pendingFiles {
		s.inv.SetUpstreamFiles(gameURL, files)
	}
	if err := s.inv.Save(s.inventoryPath); err != nil {
		logger.Error("update-svc: save: %v", err)
	}
	if s.artworkChanged && s.scanLibrary != nil {
		if message, err := s.scanLibrary(); err != nil {
			logger.Warn("update-svc: artwork repaired but library rescan failed: %v", err)
		} else {
			logger.Info("update-svc: artwork repaired; Leaf library rescan requested: %s", message)
		}
	}

	logger.Info("update-svc: check complete")
}

// checkEntry runs cover-art repair and the upstream check for one game.
// For free games it returns the upstream file list to apply later (batched);
// for paid games it returns nil.
func (s *UpdateService) checkEntry(gameURL string) []UpstreamFile {
	s.inv.mu.Lock()
	entry, ok := s.inv.Entries[gameURL]
	if !ok {
		s.inv.mu.Unlock()
		return nil
	}
	// Snapshot without holding the lock during I/O.
	coverURL := entry.CoverURL
	isFree := entry.IsFree
	files := append([]DownloadedFile(nil), entry.Files...)
	s.inv.mu.Unlock()

	// 1. Cover art repair.
	for _, f := range files {
		if f.ContentKind == ContentKindMusic || f.FileType == FileTypeMusic ||
			roms.IsPSXSupportExt(roms.ROMExt(f.DestPath)) {
			continue
		}
		result, migrated := s.migrateOwnedArtwork(f)
		var err error
		if !migrated {
			result, err = s.client.EnsureCoverArt(coverURL, f.DestPath)
		}
		if err != nil {
			logger.Error("update-svc: cover art repair failed for %s: %v", f.Filename, err)
			continue
		}
		if result.Path != "" {
			created := result.Created
			if !created && f.ArtworkCreated && filepath.Clean(f.ArtworkPath) == filepath.Clean(result.Path) &&
				(f.ArtworkHash == "" || f.ArtworkHash == result.SHA256) {
				created = true
			}
			s.inv.SetArtwork(gameURL, f.DestPath, result.Path, result.SHA256, created)
			if result.Created {
				s.artworkChanged = true
			}
		}
	}

	// 2. Upstream check.
	if isFree {
		return s.checkFreeGame(gameURL, files)
	}
	s.checkPaidGame(gameURL)
	return nil
}

func (s *UpdateService) migrateOwnedArtwork(file DownloadedFile) (itchio.ArtworkResult, bool) {
	expected := CanonicalArtworkPath(file.DestPath)
	oldPath := filepath.Clean(file.ArtworkPath)
	if !file.ArtworkCreated || file.ArtworkHash == "" || expected == "" || file.ArtworkPath == "" ||
		oldPath == filepath.Clean(expected) || filepath.Base(filepath.Dir(oldPath)) != ".media" {
		return itchio.ArtworkResult{}, false
	}
	identity, ok := roms.DescribeDestination(file.DestPath)
	if !ok {
		return itchio.ArtworkResult{}, false
	}
	source, ok := s.sources.ByID(identity.SourceID)
	if !ok || !source.Available() {
		return itchio.ArtworkResult{}, false
	}
	if _, err := leaf.RelativeWithin(source.Root, oldPath); err != nil {
		return itchio.ArtworkResult{}, false
	}
	if _, err := leaf.RelativeWithin(source.Root, expected); err != nil {
		return itchio.ArtworkResult{}, false
	}
	info, err := os.Lstat(oldPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return itchio.ArtworkResult{}, false
	}
	fileHandle, err := os.Open(oldPath)
	if err != nil {
		return itchio.ArtworkResult{}, false
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, fileHandle)
	closeErr := fileHandle.Close()
	actualHash := fmt.Sprintf("%x", hash.Sum(nil))
	if copyErr != nil || closeErr != nil || actualHash != file.ArtworkHash {
		return itchio.ArtworkResult{}, false
	}
	if _, err := os.Lstat(expected); err == nil || !os.IsNotExist(err) {
		return itchio.ArtworkResult{}, false
	}
	if err := os.MkdirAll(filepath.Dir(expected), 0o755); err != nil {
		return itchio.ArtworkResult{}, false
	}
	if err := os.Rename(oldPath, expected); err != nil {
		return itchio.ArtworkResult{}, false
	}
	_ = os.Remove(filepath.Dir(oldPath))
	logger.Info("update-svc: moved app-owned artwork to canonical Leaf image root: %s", expected)
	return itchio.ArtworkResult{Path: expected, SHA256: actualHash, Created: true}, true
}

// isGameRemoved reports whether err indicates a 404 or 410 HTTP response.
func isGameRemoved(err error) bool {
	return errors.Is(err, itchio.ErrGameRemoved)
}

// checkFreeGame fetches the current upload list and returns it for the caller
// to apply via SetUpstreamFiles (batched at end-of-check). Returns nil on error.
func (s *UpdateService) checkFreeGame(gameURL string, downloadedFiles []DownloadedFile) []UpstreamFile {
	uploads, err := s.client.FetchUploads(gameURL)
	if err != nil {
		if isGameRemoved(err) {
			s.inv.MarkRemoved(gameURL)
			logger.Warn("update-svc: game removed (404) %s", gameURL)
		} else {
			logger.Warn("update-svc: transient error for %s: %v", gameURL, err)
		}
		return nil
	}

	upstreamFiles := make([]UpstreamFile, 0, len(uploads))
	upstreamNames := make(map[string]bool, len(uploads)*2)
	for _, u := range uploads {
		upstreamFiles = append(upstreamFiles, UpstreamFile{
			Filename: u.Filename,
			UploadID: u.UploadID,
			SeenAt:   time.Now(), // preserved for known files by SetUpstreamFiles
		})
		upstreamNames[u.Filename] = true
		if stem := strings.TrimSuffix(u.Filename, romFileExt(u.Filename)); stem != u.Filename {
			upstreamNames[stem] = true
		}
	}

	// Warn if any downloaded file is no longer offered upstream.
	// Music files extracted from ZIPs have individual track names that are never
	// directly listed as upload filenames, so skip them here.
	for _, f := range downloadedFiles {
		if f.FileType == FileTypeMusic {
			continue
		}
		// For files extracted from archives (ZIP/7z), check the source archive
		// name against upstream rather than the extracted ROM's renamed filename
		// (which may be entirely different due to unified naming).
		checkName := f.Filename
		if f.SourceArchive != "" {
			checkName = f.SourceArchive
		}
		stem := strings.TrimSuffix(checkName, romFileExt(checkName))
		if !upstreamNames[checkName] && !upstreamNames[stem] {
			s.inv.MarkRemoved(gameURL)
			logger.Warn("update-svc: downloaded file %q no longer available upstream for %s", checkName, gameURL)
			return upstreamFiles
		}
	}

	// All downloaded files still available — clear any stale removal state.
	s.inv.MarkReachable(gameURL)
	logger.Debug("update-svc: %s — %d upstream file(s) recorded", gameURL, len(upstreamFiles))
	return upstreamFiles
}

func (s *UpdateService) checkPaidGame(gameURL string) {
	_, err := s.client.FetchGameDetail(gameURL)
	if err != nil {
		if isGameRemoved(err) {
			s.inv.MarkRemoved(gameURL)
			logger.Warn("update-svc: paid game removed (404) %s", gameURL)
		} else {
			logger.Warn("update-svc: transient error for paid game %s: %v", gameURL, err)
		}
		return
	}
	s.inv.MarkReachable(gameURL)

	s.inv.mu.Lock()
	if e, ok := s.inv.Entries[gameURL]; ok {
		e.UpdateCheckedAt = time.Now()
	}
	s.inv.mu.Unlock()
	logger.Debug("update-svc: paid game %s reachable, no file diff", gameURL)
}

package itchio

import (
	"context"
	"crypto/sha256"
	"fmt"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"leaf-mlp1-poc/internal/leaf"
	"leaf-mlp1-poc/internal/logger"
	"leaf-mlp1-poc/internal/media"
	"leaf-mlp1-poc/internal/roms"
)

type ArtworkResult struct {
	Path    string
	SHA256  string
	Created bool
}

func artworkFileResult(path string, created bool) (ArtworkResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return ArtworkResult{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ArtworkResult{}, err
	}
	return ArtworkResult{Path: path, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Created: created}, nil
}

func existingArtwork(path string) (ArtworkResult, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return ArtworkResult{}, false, nil
	}
	if err != nil {
		return ArtworkResult{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ArtworkResult{}, true, fmt.Errorf("cover-art: existing artwork is not a regular file")
	}
	result, err := artworkFileResult(path, false)
	return result, true, err
}

// EnsureCoverArt creates source-local launcher art only when the canonical PNG
// does not already exist. Existing art is treated as user-owned and never
// overwritten. Animated GIFs retain full animation in the app cache; launcher
// art deliberately uses the bounded first decoded frame.
func (c *Client) EnsureCoverArt(coverURL, romDestPath string) (ArtworkResult, error) {
	lease, guardErr := leaf.BeginOperation(context.Background(), "artwork conversion", false)
	if guardErr != nil {
		return ArtworkResult{}, fmt.Errorf("protect artwork conversion: %w", guardErr)
	}
	defer lease.Release()

	artPath := roms.ArtworkPath(romDestPath)
	if artPath == "" {
		return ArtworkResult{}, fmt.Errorf("cover-art: ROM is outside a configured Leaf system")
	}
	mediaDir := filepath.Dir(artPath)
	if existing, found, err := existingArtwork(artPath); found || err != nil {
		if err == nil {
			logger.Info("cover-art: preserving existing user artwork %s", artPath)
		}
		return existing, err
	}
	if coverURL == "" {
		logger.Debug("cover-art: no cover URL, skipping")
		return ArtworkResult{}, nil
	}

	logger.Info("cover-art: downloading for %s", filepath.Base(romDestPath))

	resp, err := c.http.Get(coverURL)
	if err != nil {
		logger.Error("cover-art: fetch: %v", err)
		return ArtworkResult{}, fmt.Errorf("cover-art: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Error("cover-art: HTTP %d", resp.StatusCode)
		return ArtworkResult{}, fmt.Errorf("cover-art: HTTP %d", resp.StatusCode)
	}

	data, err := media.ReadSource(resp.Body, resp.ContentLength)
	if err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: %w", err)
	}
	decoded, err := media.Decode(data)
	if err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: %w", err)
	}
	if len(decoded.Frames) == 0 || decoded.Frames[0] == nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: decoded image has no frames")
	}
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(mediaDir, ".art-*.tmp")
	if err != nil {
		logger.Error("cover-art: create temp: %v", err)
		return ArtworkResult{}, fmt.Errorf("cover-art: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath) // no-op after successful rename
	}()

	hash := sha256.New()
	if err := png.Encode(io.MultiWriter(tmp, hash), decoded.Frames[0]); err != nil {
		logger.Error("cover-art: encode png: %v", err)
		return ArtworkResult{}, fmt.Errorf("cover-art: encode png: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: sync temp: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		logger.Error("cover-art: close temp %s: %v", tmpPath, err)
		return ArtworkResult{}, fmt.Errorf("cover-art: close temp: %w", err)
	}
	if existing, found, err := existingArtwork(artPath); found || err != nil {
		return existing, err
	}
	if err := os.Rename(tmpPath, artPath); err != nil {
		logger.Error("cover-art: rename to %s: %v", artPath, err)
		return ArtworkResult{}, fmt.Errorf("cover-art: rename: %w", err)
	}
	logger.Info("cover-art: saved → %s", artPath)
	return ArtworkResult{Path: artPath, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Created: true}, nil
}

func (c *Client) DownloadCoverArt(coverURL, romDestPath string) error {
	_, err := c.EnsureCoverArt(coverURL, romDestPath)
	return err
}

// EnsureCopiedCoverArt copies the ROM file into Jawaka's source-local canonical
// image directory, using the same art filename that EnsureCoverArt would
// produce. Used for .p8.png cartridges, which are themselves valid PNG images
// and need no separate network request.
func EnsureCopiedCoverArt(romDestPath string) (ArtworkResult, error) {
	lease, guardErr := leaf.BeginOperation(context.Background(), "artwork conversion", false)
	if guardErr != nil {
		return ArtworkResult{}, fmt.Errorf("protect artwork conversion: %w", guardErr)
	}
	defer lease.Release()
	artPath := roms.ArtworkPath(romDestPath)
	if artPath == "" {
		return ArtworkResult{}, fmt.Errorf("cover-art: ROM is outside a configured Leaf system")
	}
	mediaDir := filepath.Dir(artPath)
	if existing, found, err := existingArtwork(artPath); found || err != nil {
		if err == nil {
			logger.Info("cover-art: preserving existing user artwork %s", artPath)
		}
		return existing, err
	}
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: mkdir: %w", err)
	}

	logger.Info("cover-art: copying .p8.png as art → %s", artPath)

	src, err := os.Open(romDestPath)
	if err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: open source: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(mediaDir, ".art-*.tmp")
	if err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath)
	}()

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), src); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: copy: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: sync temp: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: close temp: %w", err)
	}
	if existing, found, err := existingArtwork(artPath); found || err != nil {
		return existing, err
	}
	if err := os.Rename(tmpPath, artPath); err != nil {
		return ArtworkResult{}, fmt.Errorf("cover-art: rename: %w", err)
	}
	logger.Info("cover-art: saved → %s", artPath)
	return ArtworkResult{Path: artPath, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Created: true}, nil
}

func CopyCoverArt(romDestPath string) error {
	_, err := EnsureCopiedCoverArt(romDestPath)
	return err
}

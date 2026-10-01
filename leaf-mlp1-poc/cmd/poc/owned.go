package main

import (
	"context"
	"fmt"
	"path/filepath"

	"leaf-mlp1-poc/internal/appui"
	"leaf-mlp1-poc/internal/inventory"
	"leaf-mlp1-poc/internal/itchio"
	"leaf-mlp1-poc/internal/roms"
)

// downloadOwned installs a game the signed-in account owns.
//
// A paid game's public page exposes no downloadable file, so the free path
// (FetchUploads on the page) finds nothing. The authenticated route instead
// asks itch.io which purchase keys the account holds for this game, lists the
// uploads that key grants, and resolves a download URL against it — the same
// three-step flow Leaf uses (FetchOwnedKeys, FetchUploadsForKey,
// DownloadAuthUpload).
//
// Returns (path, true, nil) on success; (_, false, err) when the account does
// not own the game or nothing installable is behind the key, so the caller can
// fall back to the public path.
func downloadOwned(ctx context.Context, client *itchio.Client, inv *inventory.Inventory,
	apiKey, gameID string, game appui.DetailGame, coverURL, desc string) (string, bool, error) {

	keys, err := client.FetchOwnedKeys(apiKey, gameID)
	if err != nil {
		return "", false, fmt.Errorf("check purchases: %w", err)
	}
	if len(keys) == 0 {
		return "", false, fmt.Errorf("this account does not own %q", game.Title)
	}

	// Try every key: a game can be owned both directly and via a bundle, and
	// only one of those may still grant the upload.
	var lastErr error
	for _, key := range keys {
		keyID := fmt.Sprintf("%d", key.ID)
		uploads, err := client.FetchUploadsForKey(apiKey, gameID, keyID)
		if err != nil {
			lastErr = err
			continue
		}

		romUploads := make([]roms.Upload, len(uploads))
		for i, u := range uploads {
			romUploads[i] = roms.Upload{
				Filename: u.Filename, URL: u.URL, UploadID: u.UploadID, NeedsFormat: u.NeedsFormat,
			}
		}
		best := roms.SelectBest(romUploads)
		if best == nil {
			lastErr = fmt.Errorf("purchase has no ROM this console can run")
			continue
		}

		ext := roms.ROMExt(best.Filename)
		destDir := roms.DestinationDir(ext)
		if destDir == "" {
			lastErr = fmt.Errorf("unsupported ROM type %q", ext)
			continue
		}
		name := roms.SanitiseFilename(game.Title, ext)
		if name == "" {
			name = best.Filename
		}
		destPath := filepath.Join(destDir, name)

		if err := client.DownloadAuthUploadContext(ctx, apiKey, best.UploadID, keyID, destPath, nil); err != nil {
			lastErr = err
			continue
		}

		recordInstall(inv, inventoryPath(), installRecord{
			GameURL: game.URL, Title: game.Title, Author: game.Author,
			CoverURL: coverURL, IsFree: game.IsFree,
			DestPath: destPath, Upload: best.Filename,
			UploadID: best.UploadID, Purchase: fmt.Sprintf("%d", key.PurchaseID),
			Desc: desc,
		})
		return destPath, true, nil
	}
	return "", false, lastErr
}

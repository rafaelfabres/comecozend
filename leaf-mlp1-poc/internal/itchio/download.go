package itchio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"leaf-mlp1-poc/internal/leaf"
	"leaf-mlp1-poc/internal/logger"
)

// knownNonROMExts lists extensions that are definitely not supported ROM/disc files.
// Uploads with these extensions are silently dropped when scanning a game's
// upload list. Anything not in this map (including no extension, version-number
// suffixes like ".0", and ".zip") is returned with NeedsFormat=true so the
// user can classify it manually.
//
// The original Leaf list (Game Boy only) also dropped .nes, .nds, .sfc, .smc,
// .tar and .gz; the RetroAchievements hub needs all of those, so they are
// no longer skipped.
var knownNonROMExts = map[string]bool{
	".rar": true, ".bz2": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".webp": true,
	".mp3": true, ".ogg": true, ".wav": true, ".flac": true, ".aac": true,
	".pdf": true, ".txt": true, ".md": true, ".epub": true, ".mobi": true,
	".mp4": true, ".avi": true, ".mkv": true, ".mov": true,
	".exe": true, ".dmg": true, ".apk": true,
	".pocket": true,
}

func isSkippableExt(ext string) bool {
	return knownNonROMExts[strings.ToLower(ext)]
}

// presentAbsent returns "present" when s is non-empty, "absent" otherwise.
// Used to log whether a token exists without logging its value.
func presentAbsent(s string) string {
	if s != "" {
		return "present"
	}
	return "absent"
}

// FetchUploads returns the supported ROM, disc-image, and archive files
// available for free download.
//
// Flow:
//  1. GET game page → CSRF token
//  2. POST gameURL/download_url → signed download page URL containing the key
//  3. GET signed page → parse upload IDs + filenames via ParseDownloadPage
//  4. Construct a resolver URL for each upload: gameURL/file/UPLOAD_ID?key=KEY
//
// The resolver URL is stored as Upload.URL. Pass it to DownloadFree to resolve
// the actual CDN link and stream the file.
func (c *Client) FetchUploads(gameURL string) ([]Upload, error) {
	// Step 1: get CSRF token from game page
	resp, err := c.http.Get(gameURL)
	if err != nil {
		return nil, fmt.Errorf("fetch game page: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		resp.Body.Close()
		logger.Error("uploads: game page HTTP %d", resp.StatusCode)
		return nil, fmt.Errorf("fetch game page: %w", ErrGameRemoved)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		logger.Error("uploads: game page HTTP %d", resp.StatusCode)
		return nil, fmt.Errorf("fetch game page: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read game page: %w", err)
	}

	csrfM := csrfRegex.FindStringSubmatch(string(body))
	if len(csrfM) < 2 {
		return nil, fmt.Errorf("csrf_token not found on game page")
	}
	csrf := csrfM[1]

	// Step 2: POST to get the signed download page URL
	postURL := strings.TrimRight(gameURL, "/") + "/download_url"
	form := url.Values{"csrf_token": {csrf}, "suggested_amount": {"0"}}
	postResp, err := c.http.Post(postURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("download_url POST: %w", err)
	}
	defer postResp.Body.Close()

	var dlResult struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(postResp.Body).Decode(&dlResult)
	if dlResult.URL == "" {
		// No purchase step: free pages list their files with direct
		// download buttons on the game page itself.
		if direct := directUploads(gameURL, csrf, body); len(direct) > 0 {
			return direct, nil
		}
		return nil, fmt.Errorf("download_url returned empty url (game may be paid or require login)")
	}
	// The signed URL contains a download key — do not log it.
	logger.Debug("uploads: signed download URL received")

	// Step 3: extract the download key from the signed URL path
	// Format: https://author.itch.io/game/download/KEY
	key := extractDownloadKey(dlResult.URL)
	if key == "" {
		return nil, fmt.Errorf("could not extract download key from signed URL")
	}
	// The key value is sensitive — do not log it.
	logger.Debug("uploads: download key extracted")

	// Step 4: parse the signed download page for upload IDs + filenames + CSRF token
	dlPage, err := c.ParseDownloadPage(dlResult.URL)
	if err != nil {
		return nil, fmt.Errorf("parse download page: %w", err)
	}
	if len(dlPage.Uploads) == 0 {
		// The purchase flow led to an empty page; the files may still be
		// on the game page itself.
		if direct := directUploads(gameURL, csrf, body); len(direct) > 0 {
			return direct, nil
		}
	}

	// Step 5: construct resolver URLs for each .gb/.gbc upload.
	// The resolver URL embeds both the download key and the signed-page CSRF token
	// so that DownloadFree can include both in its POST body.
	base := strings.TrimRight(gameURL, "/")
	var uploads []Upload
	for _, u := range dlPage.Uploads {
		resolverURL := base + "/file/" + u.UploadID +
			"?key=" + url.QueryEscape(key) +
			"&csrf=" + url.QueryEscape(dlPage.CSRFToken)
		logger.Debug("uploads: found %s id=%s", u.Filename, u.UploadID)
		uploads = append(uploads, Upload{
			Filename:    u.Filename,
			UploadID:    u.UploadID,
			URL:         resolverURL,
			NeedsFormat: u.NeedsFormat,
			Version:     u.Version,
		})
	}
	return uploads, nil
}

// extractDownloadKey pulls the last path segment from a signed download URL.
// e.g. "https://author.itch.io/game/download/ABCDEF" → "ABCDEF"
//
// Uses EscapedPath (not Path) so that %2F-encoded slashes within the key are
// not treated as path separators before the final segment is URL-decoded.
func extractDownloadKey(signedURL string) string {
	parsed, err := url.Parse(signedURL)
	if err != nil {
		return ""
	}
	rawPath := parsed.EscapedPath()
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	key, err := url.PathUnescape(parts[len(parts)-1])
	if err != nil {
		return parts[len(parts)-1]
	}
	return key
}

// extractKeyID parses the itch.io download key JWT and returns the numeric
// download key ID embedded in its payload.
func extractKeyID(jwtKey string) string {
	dotIdx := strings.Index(jwtKey, ".")
	if dotIdx < 0 {
		return ""
	}
	payload := jwtKey[:dotIdx]
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return ""
		}
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &p); err != nil || p.ID == 0 {
		return ""
	}
	return fmt.Sprintf("%d", p.ID)
}

func (c *Client) ResolveFreeURL(upload Upload) (string, error) {
	return c.ResolveFreeURLContext(context.Background(), upload)
}

func (c *Client) ResolveFreeURLContext(ctx context.Context, upload Upload) (string, error) {
	// Parse the resolver URL to extract base path, key, and csrf.
	parsed, err := url.Parse(upload.URL)
	if err != nil {
		return "", fmt.Errorf("parse resolver URL: %w", err)
	}
	key := parsed.Query().Get("key")
	csrf := parsed.Query().Get("csrf")

	keyID := extractKeyID(key)
	baseURL := parsed.Scheme + "://" + parsed.Host + parsed.Path
	if key == "" {
		// Direct download from the game page (no download key).
		baseURL += "?source=view_game&as_props=1&after_download_lightbox=true"
	}
	// Log token presence only — never log CSRF or key values.
	logger.Debug("uploads: POST resolver csrf=%s key=%s", presentAbsent(csrf), presentAbsent(key))

	form := url.Values{"csrf_token": {csrf}}
	if key != "" {
		form.Set("download_key_id", keyID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build resolver request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", safeRequestError("resolve CDN URL", err)
	}
	defer resp.Body.Close()

	rawBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", fmt.Errorf("read resolver response: %w", readErr)
	}

	if resp.StatusCode != http.StatusOK {
		logger.Error("uploads: resolver HTTP %d: %.200s", resp.StatusCode, rawBody)
		return "", fmt.Errorf("resolve CDN URL: HTTP %d", resp.StatusCode)
	}

	var result struct {
		URL    string   `json:"url"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		logger.Error("uploads: parse resolver response: %v (body: %.200s)", err, rawBody)
		return "", fmt.Errorf("parse CDN URL response: %w", err)
	}
	if len(result.Errors) > 0 {
		logger.Error("uploads: resolver error: %s", strings.Join(result.Errors, "; "))
		return "", fmt.Errorf("resolver rejected the download request")
	}
	if result.URL == "" {
		logger.Error("uploads: empty CDN URL from resolver (file may require purchase)")
		return "", fmt.Errorf("empty CDN URL from resolver (file may require purchase)")
	}

	// CDN URL may contain signed tokens — do not log it.
	return result.URL, nil
}

// DownloadFree resolves the CDN URL for a free game upload and streams it.
//
// upload.URL must be a resolver endpoint of the form:
//
//	gameURL/file/UPLOAD_ID?key=KEY&csrf=CSRF
func (c *Client) DownloadFree(upload Upload, dest string, progress func(int64, int64)) error {
	return c.DownloadFreeContext(context.Background(), upload, dest, progress)
}

func (c *Client) DownloadFreeContext(ctx context.Context, upload Upload, dest string, progress func(int64, int64)) error {
	cdnURL, err := c.ResolveFreeURLContext(ctx, upload)
	if err != nil {
		return err
	}
	return c.streamToFileContext(ctx, cdnURL, dest, progress)
}

func (c *Client) streamToFile(srcURL, dest string, progress func(int64, int64)) error {
	return c.streamToFileContext(context.Background(), srcURL, dest, progress)
}

func (c *Client) streamToFileContext(ctx context.Context, srcURL, dest string, progress func(int64, int64)) error {
	lease, guardErr := leaf.BeginOperation(ctx, "HTTP body write", false)
	if guardErr != nil {
		return fmt.Errorf("protect HTTP body write: %w", guardErr)
	}
	defer lease.Release()
	// c.http has a 30-second Timeout that covers the entire response body read —
	// fine for API calls but fatal for large file downloads. Create a per-call
	// client with no overall timeout (Timeout: 0) that shares the same
	// transport so UA injection, h2/h1 fallback and dial timeouts still apply.
	dlClient := &http.Client{
		Transport:     c.http.Transport,
		Jar:           c.http.Jar,
		CheckRedirect: c.http.CheckRedirect,
	}
	// No overall timeout, but no waiting forever either: if no byte
	// arrives for StallTimeout the request is cancelled. Without this a
	// Wi-Fi drop mid-download left the read blocked indefinitely, and the
	// screen on "Downloading..." for good.
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
			return fmt.Errorf("%w: no data for %s", ErrDownloadStalled, StallTimeout)
		}
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return fmt.Errorf("build file request: %w", err)
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return stallErr(safeRequestError("fetch file", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Error("stream: HTTP %d fetching file", resp.StatusCode)
		return fmt.Errorf("file download status %d", resp.StatusCode)
	}

	// Log the destination and size but not the CDN source URL (may contain tokens).
	if resp.ContentLength >= 0 {
		logger.Info("stream: → %s (%d bytes)", dest, resp.ContentLength)
	} else {
		logger.Info("stream: → %s (unknown size)", dest)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".itchio-download-*.part")
	if err != nil {
		return fmt.Errorf("create download temp: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	total := resp.ContentLength
	if err := leaf.RequireFreeSpace(dir, total); err != nil {
		return fmt.Errorf("download storage preflight: %w", err)
	}
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			watchdog.Reset(StallTimeout)
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				logger.Error("stream: write error after %d bytes: %v", downloaded, werr)
				return fmt.Errorf("write: %w", werr)
			}
			downloaded += int64(n)
			if progress != nil {
				progress(downloaded, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Error("stream: read error after %d bytes: %v", downloaded, err)
			return stallErr(fmt.Errorf("read stream: %w", err))
		}
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync download temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close download temp: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("commit download: %w", err)
	}
	committed = true
	logger.Info("stream: done, wrote %d bytes", downloaded)
	return nil
}

// StallTimeout is how long a download may go without receiving a byte
// before it is abandoned. A var so tests can shorten it.
var StallTimeout = 60 * time.Second

// ErrDownloadStalled means the connection stopped delivering data.
var ErrDownloadStalled = errors.New("download stalled")

// FetchFileHeader fetches the first n bytes of a CDN URL via an HTTP Range
// request. Falls back to reading the start of a full response when the server
// does not honour Range. Used for magic-byte detection before a full download.
func (c *Client) FetchFileHeader(cdnURL string, n int) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, cdnURL, nil)
	if err != nil {
		return nil, fmt.Errorf("header fetch: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, safeRequestError("header fetch", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		logger.Error("header fetch: HTTP %d", resp.StatusCode)
		return nil, fmt.Errorf("header fetch: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(n)))
	if err != nil {
		return nil, fmt.Errorf("header fetch: read: %w", err)
	}
	// cdnURL may contain signed credentials; never include it in logs.
	logger.Debug("header fetch: read %d bytes", len(data))
	return data, nil
}

// directUploads lists the files of a game page that offers them directly
// (a "Download" button per file, no "Download Now" purchase step). The
// resolver URL carries no download key.
func directUploads(gameURL, csrf string, gamePage []byte) []Upload {
	res, err := ParseUploadsHTML(gamePage)
	if err != nil || len(res.Uploads) == 0 {
		return nil
	}
	base := strings.TrimRight(gameURL, "/")
	out := make([]Upload, 0, len(res.Uploads))
	for _, u := range res.Uploads {
		out = append(out, Upload{
			Filename:    u.Filename,
			UploadID:    u.UploadID,
			URL:         base + "/file/" + u.UploadID + "?csrf=" + url.QueryEscape(csrf),
			NeedsFormat: u.NeedsFormat,
			Version:     u.Version,
		})
	}
	logger.Info("uploads: %d direct download(s) on the game page", len(out))
	return out
}

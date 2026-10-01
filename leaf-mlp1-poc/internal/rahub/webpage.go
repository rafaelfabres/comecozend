package rahub

// Games published outside itch.io (a developer's own site, like
// https://wls.hu/blinkysrevenge-gbc/). Such a page can be pinned with
// --hub-set-url; its download links are found in the HTML and fetched with a
// plain HTTP GET. The hash check is exactly the same as for itch.io.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
)

// IsItchURL reports whether a URL is on itch.io.
func IsItchURL(u string) bool {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return false
	}
	h := strings.ToLower(p.Hostname())
	return h == "itch.io" || strings.HasSuffix(h, ".itch.io")
}

var hrefRe = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)

// downloadableExt reports file extensions worth downloading from a web page:
// archives and any console's ROM extension.
func downloadableExt(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".zip", ".7z", ".gz", ".tgz", ".tar":
		return true
	case "", ".html", ".htm", ".php", ".asp", ".aspx", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".css", ".js":
		return false
	}
	for _, c := range consoles {
		for _, e := range c.Exts {
			if e == ext {
				return true
			}
		}
	}
	return false
}

// WebPageUploads lists the files a web page links to for download. A URL
// that is itself a file is returned as the only upload.
func WebPageUploads(ctx context.Context, client *http.Client, pageURL string) ([]Upload, error) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, err
	}
	if downloadableExt(base.Path) {
		return []Upload{{Name: path.Base(base.Path), ID: pageURL, URL: pageURL}}, nil
	}
	body, err := getText(ctx, client, pageURL)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pageURL, err)
	}
	seen := map[string]bool{}
	var out []Upload
	for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
		ref, err := url.Parse(strings.TrimSpace(m[1]))
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			continue
		}
		if !downloadableExt(abs.Path) {
			continue
		}
		u := abs.String()
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, Upload{Name: path.Base(abs.Path), ID: u, URL: u})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no download link found on %s", pageURL)
	}
	return out, nil
}

// DownloadPlain fetches a file with a plain GET, giving up past maxBytes.
func DownloadPlain(ctx context.Context, client *http.Client, fileURL, dest string, maxBytes int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", path.Base(fileURL), resp.StatusCode)
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return fmt.Errorf("%s is larger than %d MB - skipped", path.Base(fileURL), maxBytes>>20)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	var r io.Reader = resp.Body
	if maxBytes > 0 {
		r = io.LimitReader(resp.Body, maxBytes+1)
	}
	n, err := io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && maxBytes > 0 && n > maxBytes {
		err = fmt.Errorf("%s is larger than %d MB - skipped", path.Base(fileURL), maxBytes>>20)
	}
	if err != nil {
		os.Remove(dest)
	}
	return err
}

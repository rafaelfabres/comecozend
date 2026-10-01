package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"leaf-mlp1-poc/internal/inventory"
	"leaf-mlp1-poc/internal/sdlui"
	"strings"
	"time"
)

// writeGamelistEntry records a downloaded game in EmulationStation's
// gamelist.xml so it shows a proper name, description and cover in the menu.
//
// The file is edited as TEXT, deliberately. Parsing it into structs and
// writing it back would silently drop every field this app does not model —
// and a scraped gamelist holds ratings, release dates, developers,
// publishers, genres, play counts and video paths per game. Losing those to
// install one homebrew title would be a bad trade, so the existing content is
// left byte-for-byte untouched and a new <game> block is inserted before the
// closing tag.
func writeGamelistEntry(systemDir, romPath, name, desc, coverURL string) error {
	gamelistPath := filepath.Join(systemDir, "gamelist.xml")
	// Relative to the system folder, so a ROM in <s>/itchio/ is listed as
	// "./itchio/Name.ext" in the system's own gamelist.xml.
	relPath := "./" + filepath.Base(romPath)
	if rel, err := filepath.Rel(systemDir, romPath); err == nil && !strings.HasPrefix(rel, "..") {
		relPath = "./" + filepath.ToSlash(rel)
	}

	existing, err := os.ReadFile(gamelistPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read gamelist: %w", err)
	}
	content := string(existing)

	// Already listed: leave it alone rather than creating a duplicate entry,
	// which EmulationStation would show twice.
	if strings.Contains(content, "<path>"+relPath+"</path>") {
		return nil
	}

	imageRel := ""
	if coverURL != "" {
		if saved, err := downloadCover(coverURL, systemDir, filepath.Base(romPath)); err != nil {
			fmt.Fprintln(os.Stderr, "cover art for the menu failed:", err)
		} else {
			imageRel = saved
		}
	}

	var entry strings.Builder
	entry.WriteString("\t<game>\n")
	entry.WriteString("\t\t<path>" + xmlEscape(relPath) + "</path>\n")
	entry.WriteString("\t\t<name>" + xmlEscape(name) + "</name>\n")
	if desc != "" {
		entry.WriteString("\t\t<desc>" + xmlEscape(desc) + "</desc>\n")
	}
	if imageRel != "" {
		entry.WriteString("\t\t<image>" + xmlEscape(imageRel) + "</image>\n")
		entry.WriteString("\t\t<thumbnail>" + xmlEscape(imageRel) + "</thumbnail>\n")
	}
	entry.WriteString("\t\t<releasedate>" + time.Now().Format("20060102T150405") + "</releasedate>\n")
	entry.WriteString("\t</game>\n")

	switch {
	case strings.Contains(content, "</gameList>"):
		idx := strings.LastIndex(content, "</gameList>")
		content = content[:idx] + entry.String() + content[idx:]
	case strings.TrimSpace(content) == "":
		content = "<?xml version=\"1.0\"?>\n<gameList>\n" + entry.String() + "</gameList>\n"
	default:
		// A gamelist without a closing tag is malformed; appending would make
		// it worse, so leave it and report.
		return fmt.Errorf("gamelist.xml has no </gameList> tag, not editing it")
	}

	tmpPath := gamelistPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write gamelist: %w", err)
	}
	// Rename over the original: a half-written gamelist would cost the user
	// every scraped entry in that system.
	return os.Rename(tmpPath, gamelistPath)
}

// downloadCover saves the game's cover into <system>/images/ using the
// naming EmulationStation's scraper uses, and returns the relative path.
func downloadCover(coverURL, systemDir, romFilename string) (string, error) {
	imagesDir := filepath.Join(systemDir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return "", err
	}

	base := strings.TrimSuffix(romFilename, filepath.Ext(romFilename))
	ext := filepath.Ext(coverURL)
	if i := strings.IndexAny(ext, "?#"); i >= 0 {
		ext = ext[:i]
	}
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif":
	default:
		ext = ".png"
	}
	outName := base + "-image" + ext
	outPath := filepath.Join(imagesDir, outName)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, coverURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", sdlui.BrowserUserAgent)
	if strings.Contains(coverURL, "retroachievements.org") {
		req.Header.Set("Referer", "https://retroachievements.org/")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	// 16 MB ceiling, matching what the image cache accepts elsewhere.
	_, copyErr := io.Copy(out, io.LimitReader(resp.Body, 16<<20))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(outPath)
		if copyErr != nil {
			return "", copyErr
		}
		return "", closeErr
	}
	return "./images/" + outName, nil
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
	)
	return replacer.Replace(s)
}

// installRecord is everything the app needs to remember about a freshly
// installed game.
type installRecord struct {
	GameURL   string
	Title     string
	Author    string
	CoverURL  string
	IsFree    bool
	DestPath  string
	Upload    string
	Archive   string
	UploadID  string
	Purchase  string
	Desc      string
}

// recordInstall is the single place an installed game gets written down.
//
// It exists because the download code grew several success paths — bare ROM,
// sniffed ROM, archive bundle, owned/paid — and each one had its own
// inventory write. Only one of them also wrote gamelist.xml, so a game that
// arrived through any other path showed up in EmulationStation as a bare
// filename with no cover. Funnelling all of them through here keeps the two
// records from drifting apart again.
func recordInstall(inv *inventory.Inventory, invPath string, rec installRecord) {
	systemDir := filepath.Dir(rec.DestPath)
	// Hub installs live in <system>/itchio/: the gamelist and the system
	// name (used to launch the game) belong to the system folder above.
	if filepath.Base(systemDir) == itchioSubdir {
		systemDir = filepath.Dir(systemDir)
	}

	if err := writeGamelistEntry(systemDir, rec.DestPath, rec.Title, rec.Desc, rec.CoverURL); err != nil {
		fmt.Fprintln(os.Stderr, "could not update gamelist.xml:", err)
	}

	inv.Add(rec.GameURL, inventory.Entry{
		GameURL:  rec.GameURL,
		Title:    rec.Title,
		Author:   rec.Author,
		CoverURL: rec.CoverURL,
		IsFree:   rec.IsFree,
	}, inventory.DownloadedFile{
		Filename:        filepath.Base(rec.DestPath),
		DestPath:        rec.DestPath,
		InstalledName:   filepath.Base(rec.DestPath),
		OriginalUpload:  rec.Upload,
		SourceArchive:   rec.Archive,
		UploadID:        rec.UploadID,
		PurchaseID:      rec.Purchase,
		CanonicalSystem: filepath.Base(systemDir),
		ContentKind:     inventory.ContentKindROM,
		DownloadedAt:    time.Now(),
	})
	if err := inv.Save(invPath); err != nil {
		fmt.Fprintln(os.Stderr, "could not save inventory:", err)
	}
}

// removeGamelistEntry deletes the <game> block for romPath from the system's
// gamelist.xml, leaving everything else byte-for-byte untouched. Used when a
// file is moved, so EmulationStation does not keep a dead entry.
func removeGamelistEntry(systemDir, romPath string) error {
	gamelistPath := filepath.Join(systemDir, "gamelist.xml")
	data, err := os.ReadFile(gamelistPath)
	if err != nil {
		return err
	}
	content := string(data)
	relPath := "./" + filepath.Base(romPath)
	if rel, err := filepath.Rel(systemDir, romPath); err == nil && !strings.HasPrefix(rel, "..") {
		relPath = "./" + filepath.ToSlash(rel)
	}
	at := strings.Index(content, "<path>"+xmlEscape(relPath)+"</path>")
	if at < 0 {
		return nil
	}
	start := strings.LastIndex(content[:at], "<game")
	endRel := strings.Index(content[at:], "</game>")
	if start < 0 || endRel < 0 {
		return nil
	}
	end := at + endRel + len("</game>")
	// Take the line's indentation and newline with it.
	for start > 0 && (content[start-1] == '\t' || content[start-1] == ' ') {
		start--
	}
	if end < len(content) && content[end] == '\n' {
		end++
	}
	content = content[:start] + content[end:]
	tmp := gamelistPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, gamelistPath)
}

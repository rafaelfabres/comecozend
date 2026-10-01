// Package esmeta records installed hacks in EmulationStation's
// gamelist.xml, so a patched ROM shows a proper name, description and
// cover in the menu instead of a bare filename.
//
// Saving the pictures into images/ is not enough on its own: once a
// system has a scraped gamelist.xml, EmulationStation trusts that file
// and stops guessing from filenames. A hack dropped into the folder
// afterwards is simply absent from it, and shows up blank.
package esmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one game to record.
type Entry struct {
	// ROMPath is the file EmulationStation will launch.
	ROMPath string
	Name    string
	Desc    string
	// ImagePath is a file already on disk, usually under
	// <system>/hacks/images/. Empty is fine.
	ImagePath string
	// Developer is the hack's author, when known.
	Developer string
	// Genre and Released come from RetroAchievements where available.
	Genre    string
	Released time.Time
}

// Write inserts or replaces the entry for a ROM in a system's
// gamelist.xml.
//
// The file is edited as TEXT, deliberately. Parsing it into structs and
// writing it back would silently drop every field this app does not
// model — and a scraped gamelist holds ratings, release dates,
// developers, publishers, genres, play counts and video paths per game.
// Losing all of that to record one hack would be a bad trade, so the
// existing content is left byte for byte and a new <game> block is
// inserted before the closing tag.
func Write(systemDir string, e Entry) error {
	gamelistPath := filepath.Join(systemDir, "gamelist.xml")
	relPath := relativeTo(systemDir, e.ROMPath)

	existing, err := os.ReadFile(gamelistPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read gamelist: %w", err)
	}
	content := string(existing)

	// Replace rather than duplicate: reinstalling a hack would otherwise
	// list it twice in the menu.
	if block := findGame(content, relPath); block != nil {
		content = content[:block[0]] + content[block[1]:]
	}

	var entry strings.Builder
	entry.WriteString("\t<game>\n")
	entry.WriteString("\t\t<path>" + xmlEscape(relPath) + "</path>\n")
	entry.WriteString("\t\t<name>" + xmlEscape(e.Name) + "</name>\n")
	if e.Desc != "" {
		entry.WriteString("\t\t<desc>" + xmlEscape(e.Desc) + "</desc>\n")
	}
	if e.ImagePath != "" {
		img := relativeTo(systemDir, e.ImagePath)
		entry.WriteString("\t\t<image>" + xmlEscape(img) + "</image>\n")
		entry.WriteString("\t\t<thumbnail>" + xmlEscape(img) + "</thumbnail>\n")
	}
	if e.Developer != "" {
		entry.WriteString("\t\t<developer>" + xmlEscape(e.Developer) + "</developer>\n")
	}
	if e.Genre != "" {
		entry.WriteString("\t\t<genre>" + xmlEscape(e.Genre) + "</genre>\n")
	}
	released := e.Released
	if released.IsZero() {
		released = time.Now()
	}
	entry.WriteString("\t\t<releasedate>" + released.Format("20060102T150405") + "</releasedate>\n")
	entry.WriteString("\t</game>\n")

	switch {
	case strings.Contains(content, "</gameList>"):
		idx := strings.LastIndex(content, "</gameList>")
		content = content[:idx] + entry.String() + content[idx:]
	case strings.TrimSpace(content) == "":
		content = "<?xml version=\"1.0\"?>\n<gameList>\n" + entry.String() + "</gameList>\n"
	default:
		// A gamelist with no closing tag is already malformed; appending
		// would make it worse.
		return fmt.Errorf("gamelist.xml has no </gameList> tag, not editing it")
	}
	return writeAtomic(gamelistPath, content)
}

// Remove deletes a ROM's entry, for when the hack is deleted.
func Remove(systemDir, romPath string) error {
	gamelistPath := filepath.Join(systemDir, "gamelist.xml")
	data, err := os.ReadFile(gamelistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	content := string(data)
	block := findGame(content, relativeTo(systemDir, romPath))
	if block == nil {
		return nil
	}
	return writeAtomic(gamelistPath, content[:block[0]]+content[block[1]:])
}

// findGame locates the <game>...</game> block for a path, including the
// indentation before it and the newline after, or nil when absent.
func findGame(content, relPath string) []int {
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
	for start > 0 && (content[start-1] == '\t' || content[start-1] == ' ') {
		start--
	}
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return []int{start, end}
}

// relativeTo renders a path the way EmulationStation expects: relative to
// the system folder, so a hack in <system>/hacks/ is "./hacks/Name.ext".
func relativeTo(systemDir, path string) string {
	if rel, err := filepath.Rel(systemDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return "./" + filepath.ToSlash(rel)
	}
	return "./" + filepath.Base(path)
}

func writeAtomic(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write gamelist: %w", err)
	}
	// Rename over the original: a half-written gamelist would cost the
	// user every scraped entry in that system.
	return os.Rename(tmp, path)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;",
	)
	// Control characters are not legal in XML 1.0 and would make the
	// whole file unparseable for EmulationStation.
	var b strings.Builder
	for _, c := range s {
		if c < 0x20 && c != '\n' && c != '\t' {
			continue
		}
		b.WriteRune(c)
	}
	return r.Replace(b.String())
}

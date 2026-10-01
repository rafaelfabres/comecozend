// Package artwork pulls a game's pictures from RetroAchievements and
// stores them where EmulationStation looks for them.
//
// RetroAchievements hosts four images per set — the icon, the title
// screen, an in-game shot and the box art — and a freshly patched hack
// otherwise shows up in the menu as a bare filename with no picture at
// all. Saving them at install time means the hack looks like any other
// game in the list.
package artwork

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"leaf-hacks/internal/rahub"
)

// mediaBase is where RetroAchievements serves the files the API points at;
// the API returns site-relative paths like "/Images/069405.png".
const mediaBase = "https://media.retroachievements.org"

// maxImage caps a single download. RA's art is well under this.
const maxImage = 8 << 20

// Art is the set of pictures RetroAchievements has for one game.
type Art struct {
	Title  string
	Icon   string // small square badge
	Screen string // title screen
	InGame string // in-game screenshot
	Box    string // box art
}

// Any reports whether there is anything worth downloading.
func (a Art) Any() bool {
	return a.Icon != "" || a.Screen != "" || a.InGame != "" || a.Box != ""
}

// Cover is the picture to show on the game page: box art when the set has
// it, otherwise the title screen, otherwise whatever exists. Many hacks
// have no box art — there is no box — so the fallback chain matters more
// here than it would for retail games.
func (a Art) Cover() string {
	for _, u := range []string{a.Box, a.Screen, a.InGame, a.Icon} {
		if u != "" {
			return u
		}
	}
	return ""
}

// Fetch reads the image URLs for a game from the RetroAchievements API.
func Fetch(ctx context.Context, ra *rahub.RAClient, gameID int) (Art, error) {
	info, err := ra.FetchGameImages(ctx, gameID)
	if err != nil {
		return Art{}, err
	}
	return Art{
		Title:  info.Title,
		Icon:   absolute(info.ImageIcon),
		Screen: absolute(info.ImageTitle),
		InGame: absolute(info.ImageIngame),
		Box:    absolute(info.ImageBoxArt),
	}, nil
}

func absolute(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || rahub.IsPlaceholderImage(path) {
		// RetroAchievements substitutes its own filler pictures for a set
		// with no artwork — including a panel that reads "No Screenshot
		// Found". They download and decode fine, so nothing else would
		// catch them; saving one would put that panel in the menu as the
		// game's cover.
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return mediaBase + path
}

// Save downloads the artwork next to a ROM, in the images/ folder
// EmulationStation scans:
//
//	/roms/snes/images/Some Hack (v2.4)-image.png
//	/roms/snes/images/Some Hack (v2.4)-thumb.png
//
// The "-image" and "-thumb" suffixes are the convention ArkOS-family
// builds use when no gamelist.xml entry exists, which is the case for a
// ROM the app has only just written. Files already present are left
// alone: a picture the user chose themselves outranks ours.
func Save(ctx context.Context, client *http.Client, art Art, romPath string) ([]string, error) {
	if !art.Any() {
		return nil, nil
	}
	dir := filepath.Join(filepath.Dir(romPath), "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	stem := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))

	// Order matters: "-image" is what the menu shows, so the cover goes
	// there rather than whichever picture happens to come first.
	wanted := []struct {
		url    string
		suffix string
	}{
		{art.Cover(), "-image"},
		{art.Icon, "-thumb"},
		{art.Screen, "-title"},
		{art.InGame, "-ingame"},
	}

	var written []string
	var firstErr error
	for _, w := range wanted {
		if w.url == "" {
			continue
		}
		dest := filepath.Join(dir, stem+w.suffix+".png")
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		if err := download(ctx, client, w.url, dest); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written = append(written, dest)
	}
	return written, firstErr
}

func download(ctx context.Context, client *http.Client, url, dest string) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "leaf-hacks")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("artwork %s: HTTP %d", url, resp.StatusCode)
	}

	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxImage)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

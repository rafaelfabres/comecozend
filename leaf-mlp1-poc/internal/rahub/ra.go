package rahub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultHubID is the RetroAchievements hub the catalog is built from.
const DefaultHubID = 3036

// RABase is overridable for tests.
var RABase = "https://retroachievements.org"

// HubGame is one game listed in the hub, plus the hashes RA accepts for it.
type HubGame struct {
	ID           int      `json:"id"`
	Title        string   `json:"title"`
	ConsoleID    int      `json:"console_id"`
	ConsoleName  string   `json:"console_name"`
	ConsoleShort string   `json:"console_short"`
	Achievements int      `json:"achievements"`
	BadgeURL     string   `json:"badge_url"`
	Hashes       []string `json:"hashes,omitempty"` // lowercase MD5s
	// PatchHashes are hashes RA only accepts after applying a patch
	// (API_GetGameHashes PatchUrl != null). A raw download matching one of
	// these still counts, but the install is noted as needing that patch.
	PatchHashes map[string]string `json:"patch_hashes,omitempty"`
	HashesKnown bool              `json:"hashes_known"`
	// Popularity on RetroAchievements: players who played it, and how many
	// achievement unlocks there are in total. Read in the background.
	Genre string `json:"genre,omitempty"`
	// SetAdded is when the achievement set was finished on RA (its newest
	// achievement).
	SetAdded time.Time `json:"set_added,omitempty"`
	// Released is RetroAchievements' release date for the game — the "Release
	// Date" column of the hub page, used by "Newest first" and the NEW mark.
	Released time.Time `json:"released,omitempty"`
	// Updated is when the achievement set was last touched on RA (the hub
	// page's "Last Updated"): what "Recently updated" sorts by.
	Updated time.Time `json:"updated,omitempty"`
	Players int       `json:"players,omitempty"`
	Unlocks int       `json:"unlocks,omitempty"`
	StatsAt time.Time `json:"stats_at,omitempty"`
}

// Console returns the console record for the game.
func (g HubGame) Console() Console { return ConsoleFor(g.ConsoleID, g.ConsoleName, g.ConsoleShort) }

// PageURL is the game's RetroAchievements page; it doubles as the game's
// stable key in the app's inventory.
func (g HubGame) PageURL() string {
	return fmt.Sprintf("%s/game/%d", strings.TrimRight(RABase, "/"), g.ID)
}

// HasHash reports whether md5 is one RA accepts for this game.
func (g HubGame) HasHash(md5 string) bool {
	md5 = strings.ToLower(md5)
	for _, h := range g.Hashes {
		if h == md5 {
			return true
		}
	}
	_, ok := g.PatchHashes[md5]
	return ok
}

// Credentials are the RA web API user and key. The key is never logged.
type Credentials struct {
	User string
	Key  string
}

func (c Credentials) Valid() bool { return c.User != "" && c.Key != "" }

// RAClient talks to retroachievements.org.
type RAClient struct {
	HTTP  *http.Client // plain client (what curl-style access proved to work)
	Alt   *http.Client // optional browser-fingerprinted fallback for Cloudflare
	Creds Credentials
	// Delay between API calls, to stay polite with RA's rate limits.
	Delay time.Duration
}

// NewRAClient builds a client. alt may be nil.
func NewRAClient(creds Credentials, alt *http.Client) *RAClient {
	return &RAClient{
		HTTP:  &http.Client{Timeout: 45 * time.Second},
		Alt:   alt,
		Creds: creds,
		Delay: 400 * time.Millisecond,
	}
}

// redact removes the API key from anything that may end up in a log.
func (r *RAClient) redact(s string) string {
	if r.Creds.Key != "" {
		s = strings.ReplaceAll(s, r.Creds.Key, "***")
		s = strings.ReplaceAll(s, url.QueryEscape(r.Creds.Key), "***")
	}
	return s
}

func (r *RAClient) getJSON(ctx context.Context, rawURL string, out any) error {
	try := func(client *http.Client) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "leaf-mlp1-poc (RA hub catalog; dArkOS)")
		resp, err := client.Do(req)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return true, err
		}
		trimmed := strings.TrimSpace(string(body))
		if resp.StatusCode != http.StatusOK {
			// 403/503 with HTML is Cloudflare; worth one retry with the
			// browser-like client. 401 is a bad key: no point retrying.
			retry := resp.StatusCode == 403 || resp.StatusCode == 503 || resp.StatusCode == 429
			if resp.StatusCode == 401 {
				return false, fmt.Errorf("HTTP 401: RetroAchievements rejected the user/API key")
			}
			return retry, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			return true, fmt.Errorf("expected JSON, got %.40q", trimmed)
		}
		return false, json.Unmarshal(body, out)
	}

	retry, err := try(r.HTTP)
	if err != nil && retry && r.Alt != nil {
		_, err = try(r.Alt)
	}
	if err != nil {
		return errors.New(r.redact(err.Error()))
	}
	return nil
}

func (r *RAClient) pause(ctx context.Context) error {
	if r.Delay <= 0 {
		return nil
	}
	t := time.NewTimer(r.Delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type hubPage struct {
	CurrentPage int `json:"currentPage"`
	LastPage    int `json:"lastPage"`
	Total       int `json:"total"`
	Items       []struct {
		Game struct {
			ID                    int    `json:"id"`
			Title                 string `json:"title"`
			ReleasedAt            string `json:"releasedAt"`
			LastUpdated           string `json:"lastUpdated"`
			UpdatedAt             string `json:"updatedAt"`
			AchievementsPublished int    `json:"achievementsPublished"`
			BadgeURL              string `json:"badgeUrl"`
			System                struct {
				ID        int    `json:"id"`
				Name      string `json:"name"`
				NameShort string `json:"nameShort"`
			} `json:"system"`
		} `json:"game"`
	} `json:"items"`
}

// FetchHub reads every page of the hub through the site's JSON endpoint
// (the HTML page is Cloudflare-protected; this endpoint is not). Page count
// and totals come from the response, never assumed. onlyWithAchievements
// mirrors the hub page's default "has achievements" filter.
func (r *RAClient) FetchHub(ctx context.Context, hubID int, onlyWithAchievements bool) ([]HubGame, error) {
	var games []HubGame
	seen := map[int]bool{}
	const perPage = 50
	for page := 1; page <= 500; page++ {
		q := url.Values{}
		q.Set("sort", "system")
		q.Set("page[number]", fmt.Sprint(page))
		q.Set("page[size]", fmt.Sprint(perPage))
		q.Set("filter[subsets]", "all")
		if onlyWithAchievements {
			q.Set("filter[achievementsPublished]", "has")
		}
		u := fmt.Sprintf("%s/internal-api/hub/%d/games?%s", strings.TrimRight(RABase, "/"), hubID, q.Encode())
		var p hubPage
		if err := r.getJSON(ctx, u, &p); err != nil {
			return games, fmt.Errorf("hub %d page %d: %w", hubID, page, err)
		}
		for _, it := range p.Items {
			g := it.Game
			if g.ID == 0 || seen[g.ID] {
				continue
			}
			seen[g.ID] = true
			badge := g.BadgeURL
			if strings.HasPrefix(badge, "/") {
				badge = "https://media.retroachievements.org" + badge
			}
			games = append(games, HubGame{
				Released: ParseRADate(g.ReleasedAt),
				Updated:  firstDate(g.LastUpdated, g.UpdatedAt),
				ID:       g.ID, Title: g.Title, ConsoleID: g.System.ID,
				ConsoleName: g.System.Name, ConsoleShort: g.System.NameShort,
				Achievements: g.AchievementsPublished, BadgeURL: badge,
			})
		}
		if len(p.Items) == 0 || p.LastPage == 0 || page >= p.LastPage {
			break
		}
		if err := r.pause(ctx); err != nil {
			return games, err
		}
	}
	return games, nil
}

type gameListEntry struct {
	ID        int      `json:"ID"`
	Title     string   `json:"Title"`
	ConsoleID int      `json:"ConsoleID"`
	Hashes    []string `json:"Hashes"`
}

// FetchConsoleHashes returns GameID → hashes for a whole console with a
// single call (API_GetGameList with h=1).
func (r *RAClient) FetchConsoleHashes(ctx context.Context, consoleID int) (map[int][]string, error) {
	if !r.Creds.Valid() {
		return nil, fmt.Errorf("RetroAchievements user/API key not set")
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("i", fmt.Sprint(consoleID))
	q.Set("h", "1")
	var list []gameListEntry
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetGameList.php?"+q.Encode(), &list); err != nil {
		return nil, fmt.Errorf("game list for console %d: %w", consoleID, err)
	}
	out := make(map[int][]string, len(list))
	for _, e := range list {
		out[e.ID] = normaliseHashes(e.Hashes)
	}
	return out, nil
}

// HashEntry is one row of API_GetGameHashes.
type HashEntry struct {
	Name     string   `json:"Name"`
	MD5      string   `json:"MD5"`
	Labels   []string `json:"Labels"`
	PatchURL *string  `json:"PatchUrl"`
}

// FetchGameHashes is the per-game fallback (and the only source of
// PatchUrl information).
func (r *RAClient) FetchGameHashes(ctx context.Context, gameID int) ([]HashEntry, error) {
	if !r.Creds.Valid() {
		return nil, fmt.Errorf("RetroAchievements user/API key not set")
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("i", fmt.Sprint(gameID))
	var resp struct {
		Results []HashEntry `json:"Results"`
	}
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetGameHashes.php?"+q.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("hashes for game %d: %w", gameID, err)
	}
	return resp.Results, nil
}

// FetchGameDeveloper returns the developer/publisher RA lists for a game,
// used as a hint when matching itch.io authors. Best effort.
func (r *RAClient) FetchGameDeveloper(ctx context.Context, gameID int) (developer, publisher string) {
	if !r.Creds.Valid() {
		return "", ""
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("i", fmt.Sprint(gameID))
	var resp struct {
		Developer string `json:"Developer"`
		Publisher string `json:"Publisher"`
	}
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetGame.php?"+q.Encode(), &resp); err != nil {
		return "", ""
	}
	return resp.Developer, resp.Publisher
}

func normaliseHashes(in []string) []string {
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if len(h) == 32 {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// AttachHashes fills in every game's hashes: one API_GetGameList call per
// console, then API_GetGameHashes only for games that call did not cover.
// PatchUrl information is only available per game, so games whose hashes
// come from the bulk call get it lazily when a file verifies (Pipeline.finish).
func (r *RAClient) AttachHashes(ctx context.Context, games []HubGame, logf func(string, ...any)) error {
	byConsole := map[int][]int{}
	for i, g := range games {
		byConsole[g.ConsoleID] = append(byConsole[g.ConsoleID], i)
	}
	consolesSorted := make([]int, 0, len(byConsole))
	for id := range byConsole {
		consolesSorted = append(consolesSorted, id)
	}
	sort.Ints(consolesSorted)

	var firstErr error
	for _, cid := range consolesSorted {
		idx := byConsole[cid]
		hashes, err := r.FetchConsoleHashes(ctx, cid)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logf("console %d: bulk hash list failed (%v), falling back per game", cid, err)
			if firstErr == nil {
				firstErr = err
			}
		}
		for _, i := range idx {
			if hs, ok := hashes[games[i].ID]; ok && len(hs) > 0 {
				games[i].Hashes = hs
				games[i].HashesKnown = true
				continue
			}
			// Missing from the bulk list, or the bulk call failed.
			if err := r.pause(ctx); err != nil {
				return err
			}
			entries, err := r.FetchGameHashes(ctx, games[i].ID)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				logf("game %d: %v", games[i].ID, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			applyHashEntries(&games[i], entries)
		}
		if err := r.pause(ctx); err != nil {
			return err
		}
	}
	return firstErr
}

func applyHashEntries(g *HubGame, entries []HashEntry) {
	var plain []string
	patched := map[string]string{}
	for _, e := range entries {
		h := strings.ToLower(strings.TrimSpace(e.MD5))
		if len(h) != 32 {
			continue
		}
		if e.PatchURL != nil && strings.TrimSpace(*e.PatchURL) != "" {
			patched[h] = *e.PatchURL
		} else {
			plain = append(plain, h)
		}
	}
	g.Hashes = normaliseHashes(plain)
	if len(patched) > 0 {
		g.PatchHashes = patched
		// A patch hash is also in the bulk list; keep them apart.
		kept := g.Hashes[:0]
		for _, h := range g.Hashes {
			if _, isPatch := patched[h]; !isPatch {
				kept = append(kept, h)
			}
		}
		g.Hashes = kept
	}
	g.HashesKnown = true
}

// FetchGameStats reads how popular a game is on RetroAchievements
// (API_GetGameExtended): distinct players, and the sum of every
// achievement's unlock count.
//
// setAdded is when the newest achievement of the set was created: that is
// when a game became playable with achievements ("new games" on RA).
func (r *RAClient) FetchGameStats(ctx context.Context, gameID int) (players, unlocks int, genre string,
	setAdded, released, updated time.Time, err error) {
	if !r.Creds.Valid() {
		return 0, 0, "", time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("RetroAchievements user/API key not set")
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("i", fmt.Sprint(gameID))
	var resp struct {
		Released                   string `json:"Released"`
		Genre                      string `json:"Genre"`
		NumDistinctPlayers         int    `json:"NumDistinctPlayers"`
		NumDistinctPlayersCasual   int    `json:"NumDistinctPlayersCasual"`
		NumDistinctPlayersHardcore int    `json:"NumDistinctPlayersHardcore"`
		Achievements               map[string]struct {
			NumAwarded   int    `json:"NumAwarded"`
			DateCreated  string `json:"DateCreated"`
			DateModified string `json:"DateModified"`
		} `json:"Achievements"`
	}
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetGameExtended.php?"+q.Encode(), &resp); err != nil {
		return 0, 0, "", time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("stats for game %d: %w", gameID, err)
	}
	players = resp.NumDistinctPlayers
	if players == 0 {
		players = resp.NumDistinctPlayersCasual + resp.NumDistinctPlayersHardcore
	}
	for _, a := range resp.Achievements {
		unlocks += a.NumAwarded
		if t := ParseRADate(a.DateCreated); !t.IsZero() && t.After(setAdded) {
			setAdded = t
		}
		if t := ParseRADate(a.DateModified); !t.IsZero() && t.After(updated) {
			updated = t
		}
	}
	if updated.Before(setAdded) {
		updated = setAdded
	}
	return players, unlocks, strings.TrimSpace(resp.Genre), setAdded, ParseRADate(resp.Released), updated, nil
}

// Progress is what the signed-in RetroAchievements user has done with a
// game: how many achievements are unlocked and the highest award
// ("mastered", "completed", "beaten-hardcore", "beaten-softcore").
type Progress struct {
	Unlocked int    `json:"unlocked"`
	Total    int    `json:"total"`
	Award    string `json:"award,omitempty"`
}

// Mastered reports the top award: every achievement, in hardcore.
func (p Progress) Mastered() bool { return strings.EqualFold(p.Award, "mastered") }

// Completed reports every achievement in softcore ("completed").
func (p Progress) Completed() bool { return strings.EqualFold(p.Award, "completed") }

// FetchUserProgress reads what the signed-in user has played, for every
// game (API_GetUserCompletionProgress, paginated).
func (r *RAClient) FetchUserProgress(ctx context.Context) (map[int]Progress, error) {
	if !r.Creds.Valid() {
		return nil, fmt.Errorf("RetroAchievements user/API key not set")
	}
	out := map[int]Progress{}
	for offset := 0; offset < 20000; offset += 500 {
		q := url.Values{}
		q.Set("z", r.Creds.User)
		q.Set("y", r.Creds.Key)
		q.Set("u", r.Creds.User)
		q.Set("c", "500")
		q.Set("o", fmt.Sprint(offset))
		var resp struct {
			Count   int `json:"Count"`
			Total   int `json:"Total"`
			Results []struct {
				GameID             int    `json:"GameID"`
				MaxPossible        int    `json:"MaxPossible"`
				NumAwarded         int    `json:"NumAwarded"`
				NumAwardedHardcore int    `json:"NumAwardedHardcore"`
				HighestAwardKind   string `json:"HighestAwardKind"`
			} `json:"Results"`
		}
		if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetUserCompletionProgress.php?"+q.Encode(), &resp); err != nil {
			return out, fmt.Errorf("user progress: %w", err)
		}
		for _, g := range resp.Results {
			unlocked := g.NumAwarded
			if g.NumAwardedHardcore > unlocked {
				unlocked = g.NumAwardedHardcore
			}
			out[g.GameID] = Progress{Unlocked: unlocked, Total: g.MaxPossible, Award: g.HighestAwardKind}
		}
		if len(resp.Results) == 0 || offset+len(resp.Results) >= resp.Total {
			break
		}
		if err := r.pause(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ParseRADate reads the date formats RetroAchievements uses
// ("2026-08-21", "2026-08-21 00:00:00", ISO with a time zone, "2025").
func ParseRADate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// firstDate returns the first of the given strings that is a date.
func firstDate(values ...string) time.Time {
	for _, v := range values {
		if t := ParseRADate(v); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

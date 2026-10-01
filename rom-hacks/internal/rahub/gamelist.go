package rahub

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ConsoleGame is one entry of API_GetGameList with hashes attached.
type ConsoleGame struct {
	ID     int
	Title  string
	Hashes []string
	// Icon is an absolute URL, or "" when the set has no artwork.
	Icon string
	// Achievements is 0 for a game RetroAchievements lists but has no set
	// for. That number is the difference between a hack worth installing
	// and one that would earn nothing.
	Achievements int
	Leaderboards int
	Points       int
	Modified     time.Time
}

// FetchConsoleGames returns every game RetroAchievements knows for a
// console, with its supported hashes. One call covers a whole system,
// which is what makes it affordable to identify the base ROMs on a device
// before any per-game lookup happens.
//
// FetchConsoleHashes already does this but throws the titles away; the
// titles are exactly what joins an owned ROM to a RAPatches folder name.
func (r *RAClient) FetchConsoleGames(ctx context.Context, consoleID int) ([]ConsoleGame, error) {
	if !r.Creds.Valid() {
		return nil, fmt.Errorf("RetroAchievements user/API key not set")
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("i", fmt.Sprint(consoleID))
	q.Set("h", "1")
	// Deliberately NOT f=1. That would return only games with
	// achievements, and a base ROM without a set still needs identifying
	// so its hacks can be found. The achievement count comes back in the
	// same response, so hacks are filtered locally instead.

	var list []gameListEntry
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetGameList.php?"+q.Encode(), &list); err != nil {
		return nil, fmt.Errorf("game list for console %d: %w", consoleID, err)
	}
	out := make([]ConsoleGame, 0, len(list))
	for _, e := range list {
		out = append(out, ConsoleGame{
			ID: e.ID, Title: e.Title, Hashes: normaliseHashes(e.Hashes),
			Icon:         MediaURL(e.ImageIcon),
			Achievements: e.NumAchievements,
			Leaderboards: e.NumLeaderboards,
			Points:       e.Points,
			Modified:     parseRATime(e.DateModified),
		})
	}
	return out, nil
}

// parseRATime reads the timestamps the API returns. They arrive in a few
// shapes depending on the endpoint, and a date that will not parse is
// simply absent rather than an error worth stopping for.
func parseRATime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// placeholderImage matches the pictures RetroAchievements substitutes for
// a set that has none of its own.
//
// They are ordinary image URLs and they download and decode perfectly —
// which is why they were being shown. One of them is a grey panel reading
// "No Screenshot Found", and it appeared in the gallery looking exactly
// like a failure of this app. They are the handful of lowest-numbered
// images on the media host; every real upload has a six-digit id far
// above these.
var placeholderImage = regexp.MustCompile(`/0*([0-9]{1,2})\.png$`)

// IsPlaceholderImage reports whether a media path is one of those fillers.
func IsPlaceholderImage(path string) bool {
	m := placeholderImage.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	return err == nil && n <= 20
}

// MediaURL turns a site-relative image path from the API into a URL, and
// reports the site's own placeholders as absent so a hack with no artwork
// falls back to something useful instead of showing RetroAchievements'
// "No Screenshot Found" panel.
func MediaURL(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || IsPlaceholderImage(path) {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return "https://media.retroachievements.org" + path
}

// GameImages is the picture set RetroAchievements keeps for one game.
type GameImages struct {
	Title       string `json:"Title"`
	ImageIcon   string `json:"ImageIcon"`
	ImageTitle  string `json:"ImageTitle"`
	ImageIngame string `json:"ImageIngame"`
	ImageBoxArt string `json:"ImageBoxArt"`
}

// FetchGameImages reads a game's artwork paths. API_GetGame is the cheap
// endpoint for this — the extended variant returns the whole achievement
// list, which is megabytes for nothing when all that is wanted is four
// image paths.
func (r *RAClient) FetchGameImages(ctx context.Context, gameID int) (GameImages, error) {
	if !r.Creds.Valid() {
		return GameImages{}, fmt.Errorf("RetroAchievements user/API key not set")
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("i", fmt.Sprint(gameID))

	var out GameImages
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetGame.php?"+q.Encode(), &out); err != nil {
		return GameImages{}, fmt.Errorf("artwork for game %d: %w", gameID, err)
	}
	return out, nil
}

// Progress is how far the signed-in user has got with one game.
type Progress struct {
	Possible      int
	Achieved      int
	AchievedHard  int
	PossibleScore int
	ScoreAchieved int
	Mastered      bool
}

// FetchUserProgress reads the user's standing on a single game.
// API_GetUserProgress takes a list of IDs and returns a map keyed by ID as
// a string, which is why the result is unpacked rather than decoded into a
// struct directly.
func (r *RAClient) FetchUserProgress(ctx context.Context, gameID int) (Progress, error) {
	if !r.Creds.Valid() {
		return Progress{}, fmt.Errorf("RetroAchievements user/API key not set")
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("u", r.Creds.User)
	q.Set("i", fmt.Sprint(gameID))

	var raw map[string]struct {
		NumPossibleAchievements int `json:"NumPossibleAchievements"`
		PossibleScore           int `json:"PossibleScore"`
		NumAchieved             int `json:"NumAchieved"`
		ScoreAchieved           int `json:"ScoreAchieved"`
		NumAchievedHardcore     int `json:"NumAchievedHardcore"`
	}
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetUserProgress.php?"+q.Encode(), &raw); err != nil {
		return Progress{}, fmt.Errorf("progress for game %d: %w", gameID, err)
	}
	entry, ok := raw[fmt.Sprint(gameID)]
	if !ok {
		return Progress{}, nil
	}
	return Progress{
		Possible:      entry.NumPossibleAchievements,
		Achieved:      entry.NumAchieved,
		AchievedHard:  entry.NumAchievedHardcore,
		PossibleScore: entry.PossibleScore,
		ScoreAchieved: entry.ScoreAchieved,
		Mastered:      entry.NumPossibleAchievements > 0 && entry.NumAchieved >= entry.NumPossibleAchievements,
	}, nil
}

// Comment is one post on a game's RetroAchievements page.
type Comment struct {
	User      string
	Text      string
	Submitted time.Time
}

// FetchComments reads the comment thread on a game's page.
//
// RetroAchievements has no description field for a set, so there is
// nothing official to show under "About". The thread is the next best
// thing: for a hack it is usually the author or the set developer saying
// what the hack changes. It is other people's writing, so it is shown
// attributed and never presented as the app's own text.
func (r *RAClient) FetchComments(ctx context.Context, gameID, limit int) ([]Comment, error) {
	if !r.Creds.Valid() {
		return nil, fmt.Errorf("RetroAchievements user/API key not set")
	}
	if limit <= 0 {
		limit = 10
	}
	q := url.Values{}
	q.Set("z", r.Creds.User)
	q.Set("y", r.Creds.Key)
	q.Set("i", fmt.Sprint(gameID))
	q.Set("t", "1") // 1 = game page
	q.Set("c", fmt.Sprint(limit))

	var payload struct {
		Results []struct {
			User        string `json:"User"`
			CommentText string `json:"CommentText"`
			Submitted   string `json:"Submitted"`
		} `json:"Results"`
	}
	if err := r.getJSON(ctx, strings.TrimRight(RABase, "/")+"/API/API_GetComments.php?"+q.Encode(), &payload); err != nil {
		return nil, fmt.Errorf("comments for game %d: %w", gameID, err)
	}
	out := make([]Comment, 0, len(payload.Results))
	for _, c := range payload.Results {
		text := strings.TrimSpace(c.CommentText)
		if text == "" || c.User == "" {
			continue
		}
		out = append(out, Comment{User: c.User, Text: text, Submitted: parseRATime(c.Submitted)})
	}
	return out, nil
}

package rahub

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// wordSearch answers like itch.io's search: a page comes back only when
// the query has each word of its title — "Ring Dash GBA" does not find
// "RingDash GBA".
type wordSearch struct {
	*fakeItch
	queries []string
}

func (w *wordSearch) Search(ctx context.Context, q string) ([]Candidate, error) {
	w.queries = append(w.queries, q)
	have := " " + NormTitle(q) + " "
	var out []Candidate
	for _, c := range w.results {
		ok := true
		for _, word := range strings.Fields(NormTitle(c.Title)) {
			if !strings.Contains(have, " "+word+" ") {
				ok = false
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

var ringDash = Candidate{
	URL:    "https://brig78cx.itch.io/ringdash-gba",
	Title:  "RingDash GBA",
	Author: "brig78cx",
}

// RA lists "Ring Dash GBA"; the developer published "RingDash GBA". The
// game must be found, not recorded as "not in itch".
func TestResolveFindsATitleSpacedDifferently(t *testing.T) {
	dir := t.TempDir()
	itch := &wordSearch{fakeItch: &fakeItch{results: []Candidate{ringDash}}}
	store, _ := OpenStore(dir, 3036)
	game := HubGame{ID: 40001, Title: "~Homebrew~ Ring Dash GBA", ConsoleID: 5, ConsoleName: "Game Boy Advance"}
	store.SetGames([]HubGame{game})
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "work"), nil)
	p.SearchDelay = 0

	cands, err := p.Resolve(context.Background(), game, false)
	if err != nil {
		t.Fatalf("not found (queries %q): %v", itch.queries, err)
	}
	if len(cands) == 0 || cands[0].URL != ringDash.URL {
		t.Fatalf("got %+v", cands)
	}
	if st := store.State(40001); st.Status == StatusNotFound {
		t.Error("recorded as not found")
	}
}

func TestScoreTreatsSpacingAsTheSameTitle(t *testing.T) {
	gba := ConsoleFor(5, "Game Boy Advance", "GBA")
	if s, why := ScoreFor("~Homebrew~ Ring Dash GBA", gba, nil, ringDash); s < 100 {
		t.Errorf("score %d (%s)", s, why)
	}
	// A different game sharing a word is still not a match.
	other := Candidate{URL: "https://x.itch.io/ring-runner", Title: "Ring Runner GBA"}
	if s, why := ScoreFor("~Homebrew~ Ring Dash GBA", gba, nil, other); s >= 50 {
		t.Errorf("unrelated title scored %d (%s)", s, why)
	}
}

func TestTitleSlugsGuessTheJoinedName(t *testing.T) {
	slugs := TitleSlugs("~Homebrew~ Ring Dash GBA")
	want := "ringdash-gba"
	for _, s := range slugs {
		if s == want {
			return
		}
	}
	t.Errorf("%q missing from %q", want, slugs)
}

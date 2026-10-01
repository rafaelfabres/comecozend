package catalog

import (
	"strings"
	"testing"

	"leaf-hacks/internal/rahub"
)

// A comment thread is mostly congratulations and bookkeeping. Picking the
// first post regardless would put "Nice set!" where the description goes.
func TestUsableDescriptionFiltersNoise(t *testing.T) {
	reject := []rahub.Comment{
		{User: "someone", Text: "Nice set!"},
		{User: "someone", Text: "gj on the set, looking forward to playing it soon"},
		{User: "Server", Text: strings.Repeat("This set is now in the core. ", 5)},
		{User: "someone", Text: "This set is now available for everyone to play and enjoy today"},
		{User: "someone", Text: "http://example.com/a/very/long/link/to/somewhere"},
		{User: "", Text: strings.Repeat("anonymous but long enough to pass the length test ", 3)},
	}
	for _, c := range reject {
		if usableDescription(c) {
			t.Errorf("should have been rejected: %q by %q", c.Text, c.User)
		}
	}

	good := rahub.Comment{
		User: "SetDev",
		Text: "This hack rebalances every boss fight and adds a new post-game dungeon with twelve floors.",
	}
	if !usableDescription(good) {
		t.Error("a real description was rejected")
	}
}

func TestTidyCollapsesBlankLines(t *testing.T) {
	got := tidy("\n\nFirst line.\n\n\n\nSecond line.   \n\n")
	want := "First line.\n\nSecond line."
	if got != want {
		t.Errorf("tidy = %q, want %q", got, want)
	}
}

// The thread for Final Fantasy Tactics: The Lion War had "Please make
// this a set, it's still imo the best way to play this game" at the top.
// It is a sentence about the game that describes nothing, and it went
// straight under About.
func TestWishesAreNotDescriptions(t *testing.T) {
	reject := []string{
		"Please make this a set, it's still imo the best way to play this game",
		"Is there a set for this one? I would love to play through it again properly",
		"Any chance someone picks this up? It deserves achievements more than most",
		"Can't wait for this, been wanting to replay the game for years now",
	}
	for _, text := range reject {
		c := rahub.Comment{User: "someone", Text: text}
		if usableDescription(c) {
			t.Errorf("a request should not become a description: %q", text)
		}
	}
}

// Between several acceptable posts, the one that describes the hack wins,
// not whichever came first.
func TestBestDescriptionWins(t *testing.T) {
	reaction := "Finally got around to playing this one and it was a good time overall, recommended"
	real := "This hack rebalances every class, adds a new post-game campaign and replaces the music with arranged tracks."

	if describeScore(real) <= describeScore(reaction) {
		t.Errorf("the descriptive post should score higher: %d vs %d",
			describeScore(real), describeScore(reaction))
	}
	if describeScore("Nice one, thanks for the upload here") != 0 {
		t.Error("a post describing nothing should score zero")
	}
}

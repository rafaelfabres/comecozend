package catalog

import (
	"context"
	"strings"

	"leaf-hacks/internal/rahub"
)

// Description is a piece of writing about a hack, with whose writing it is.
type Description struct {
	Text   string
	Author string
}

// automated are the accounts whose posts are bookkeeping rather than
// description: set-status changes, claim notices and the like.
var automated = map[string]bool{
	"server": true, "retroachievements": true, "racheevos": true,
}

// boilerplate opens the posts that say nothing about the hack itself.
var boilerplate = []string{
	"this set is now", "claim expired", "set request", "achievement set claimed",
	"promoted to core", "demoted", "revision in progress", "fixed a typo",
	"thanks for the set", "thanks for this", "great set", "nice set",
	"gj", "good job", "congrats",
}

// wishes are posts asking for the set to exist or be changed. They read
// like sentences about the game but say nothing about it: "Please make
// this a set, it's still imo the best way to play this game" is a request,
// and putting it under About tells the reader nothing.
var wishes = []string{
	"please make", "can someone make", "is there a set", "any chance",
	"when will", "hope this gets", "i hope", "would love", "would be nice",
	"someone should", "requesting", "can we get", "needs a set",
	"looking forward", "cant wait", "can't wait", "pls", "please add",
}

// descriptive are the words a post uses when it is actually describing
// what a hack does. One of them has to appear.
var descriptive = []string{
	"adds", "added", "changes", "changed", "replaces", "rebalance",
	"new levels", "new maps", "new sprites", "difficulty", "randomiz",
	"remake", "overhaul", "features", "includes", "rewritten", "redesigned",
	"this hack", "this romhack", "the hack", "story", "campaign", "bosses",
	"characters", "music", "translation", "quality of life", "content",
	"version of", "based on", "port of", "expands",
}

// minDescription is the length below which a comment is a reaction rather
// than a description. "Nice hack!" helps nobody decide anything.
const minDescription = 60

// FetchDescription looks through a game's comment thread for something
// that actually describes the hack.
//
// RetroAchievements keeps no description field for a set, so there is
// nothing official to show. The thread usually contains one: the set
// developer or the hack's author explaining what changed. This takes the
// earliest post that reads like a description and leaves the rest —
// congratulations, claim notices and one-word reactions are the bulk of
// most threads and would fill the page with noise.
func FetchDescription(ctx context.Context, ra *rahub.RAClient, gameID int) (Description, bool) {
	comments, err := ra.FetchComments(ctx, gameID, 20)
	if err != nil {
		return Description{}, false
	}
	// Best, not first. A thread's earliest post is as often a request as
	// a description, and taking whatever passes the filter put "Please
	// make this a set" under About on a set that already exists.
	best := rahub.Comment{}
	bestScore := 0
	for _, c := range comments {
		if !usableDescription(c) {
			continue
		}
		if score := describeScore(c.Text); score > bestScore {
			best, bestScore = c, score
		}
	}
	if bestScore == 0 {
		return Description{}, false
	}
	return Description{Text: tidy(best.Text), Author: best.User}, true
}

// describeScore rates how much a post reads like a description of the
// hack rather than a reaction to it. Zero means "do not use".
func describeScore(text string) int {
	lower := strings.ToLower(text)
	score := 0
	for _, w := range descriptive {
		if strings.Contains(lower, w) {
			score += 3
		}
	}
	if score == 0 {
		return 0
	}
	// Longer posts say more, with diminishing returns and a ceiling so a
	// rambling one does not beat a clear one.
	length := len([]rune(text))
	switch {
	case length > 400:
		score += 3
	case length > 200:
		score += 2
	case length > 100:
		score += 1
	}
	// A post written about the reader's own play is not a description.
	if strings.HasPrefix(lower, "i ") || strings.HasPrefix(lower, "im ") ||
		strings.HasPrefix(lower, "i'm ") {
		score -= 2
	}
	if score < 0 {
		return 0
	}
	return score
}

func usableDescription(c rahub.Comment) bool {
	// Unattributed text does not go on the page: the whole point of
	// showing somebody's forum post is saying whose it is.
	if strings.TrimSpace(c.User) == "" || automated[strings.ToLower(c.User)] {
		return false
	}
	text := strings.TrimSpace(c.Text)
	if len([]rune(text)) < minDescription {
		return false
	}
	lower := strings.ToLower(text)
	for _, b := range boilerplate {
		if strings.HasPrefix(lower, b) {
			return false
		}
	}
	for _, w := range wishes {
		if strings.Contains(lower, w) {
			return false
		}
	}
	// A post that is mostly a link is a pointer, not a description.
	if strings.Count(lower, "http") > 0 && len([]rune(text)) < 140 {
		return false
	}
	return true
}

// tidy collapses the runs of blank lines and stray whitespace that forum
// posts are full of, so the page lays out like prose.
func tidy(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	blank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

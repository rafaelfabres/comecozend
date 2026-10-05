package rahub

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// raTagRegex strips RA's title tags: "~Homebrew~ ", "~Hack~ ", "~Prototype~ ".
var raTagRegex = regexp.MustCompile(`~[^~]*~`)

// parenRegex strips "(NTSC)", "[!]" style decorations.
var parenRegex = regexp.MustCompile(`[\(\[][^\)\]]*[\)\]]`)

// CleanTitle turns an RA title into the human title a developer would use:
// "~Homebrew~ Alien Force" → "Alien Force". Used for searching and for the
// installed file name.
func CleanTitle(raTitle string) string {
	t := raTagRegex.ReplaceAllString(raTitle, " ")
	t = parenRegex.ReplaceAllString(t, " ")
	t = strings.Join(strings.Fields(t), " ")
	// RA moves leading articles: "Legend of Zelda, The" → "The Legend of Zelda"
	for _, art := range []string{", The", ", A", ", An"} {
		if strings.HasSuffix(t, art) {
			t = strings.TrimSpace(art[2:]) + " " + strings.TrimSuffix(t, art)
		}
		// Also "Title, The: Subtitle"
		if i := strings.Index(t, art+":"); i > 0 {
			t = strings.TrimSpace(art[2:]) + " " + t[:i] + t[i+len(art):]
		}
	}
	return strings.TrimSpace(t)
}

// NormTitle is the comparison form: lowercase ASCII letters/digits only,
// accents folded, leading article dropped, "&" → "and".
func NormTitle(s string) string {
	s = CleanTitle(s)
	s = strings.ReplaceAll(s, "&", " and ")
	// "Wilford's" and "Wilfords" (how URLs spell it) must compare equal.
	s = strings.NewReplacer("'", "", "’", "", "`", "").Replace(s)
	s = norm.NFD.String(s)
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// combining accent: drop
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) > 1 && (words[0] == "the" || words[0] == "a" || words[0] == "an") {
		words = words[1:]
	}
	return strings.Join(words, " ")
}

// MainTitle drops a subtitle: "Foo: The Bar" / "Foo - The Bar" → "Foo".
func MainTitle(clean string) string {
	for _, sep := range []string{": ", " - ", " – "} {
		if i := strings.Index(clean, sep); i > 2 {
			return strings.TrimSpace(clean[:i])
		}
	}
	return clean
}

// Candidate is a possible itch.io page for a hub game.
type Candidate struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	CoverURL string `json:"cover_url,omitempty"`
	Price    string `json:"price,omitempty"`
	// Text is the short blurb shown under the title in search results.
	Text string `json:"text,omitempty"`
	// Browser is set when the page is a play-in-browser game.
	Browser bool `json:"browser,omitempty"`
	// Platforms are the download platforms the result advertises
	// ("windows", "linux", "osx", "android").
	Platforms []string `json:"platforms,omitempty"`
	// WebRank is the result's position (1 = first) on the web search that
	// found it; DevQuery says that search was "title + developer".
	WebRank int `json:"web_rank,omitempty"`
	// Links are other game pages of the same account that this page links
	// to ("Want the ROM? https://jwgllc.itch.io/slender-the-8-gb-pages").
	Links    []string `json:"links,omitempty"`
	DevQuery bool     `json:"dev_query,omitempty"`
	// PriceChecked: the page itself was read for a price (none = free).
	PriceChecked bool   `json:"price_checked,omitempty"`
	Score        int    `json:"score"`
	Source       string `json:"source,omitempty"` // "search", "catalog", "override"
	Reason       string `json:"reason,omitempty"`
}

// noise words that mark a page as something other than the game itself.
var offTargetWords = []string{"soundtrack", "ost", "source code", "sourcecode", "fanart", "fan art", "artbook", "manual", "physical", "cartridge edition", "boxed", "asset", "assets", "tileset", "font", "sprite pack", "music pack"}

// ScoreCandidate rates how likely an itch.io page is the hub game. It is a
// filter for WHAT TO DOWNLOAD, not a verdict: the hash decides. developer is
// RA's developer field (may be empty).
func ScoreCandidate(raTitle, developer string, c Candidate) (int, string) {
	return ScoreFor(raTitle, Console{}, []string{developer}, c)
}

// ScoreFor is ScoreCandidate with the console and every developer hint
// (RA developer/publisher, names found in RA's hash file names).
//
// A candidate is only ever accepted when it is CONFIRMED: the same
// developer (RA developer, a name from RA's hash files, or the account it
// moved to), or — when that cannot be checked — the title plus the console
// named on the page. A similar title from someone else scores below the
// acceptance line no matter what else matches.
func ScoreFor(raTitle string, console Console, developers []string, c Candidate) (int, string) {
	score, reason, titleScore := scoreLoose(raTitle, console, developers, c)
	if console.ID == 0 || score < 30 {
		return score, reason
	}
	sameDev := IsSameDeveloper(developers, c)
	named := MentionsConsole(c.Title+" "+c.Text+" "+c.URL, console)
	hasDevs := false
	for _, d := range developers {
		if len(compact(d)) >= 3 {
			hasDevs = true
		}
	}
	confirmed := sameDev || (named && (titleScore >= 80 || !hasDevs))
	if !confirmed {
		return 25, reason + ", NOT CONFIRMED (other developer" + map[bool]string{true: "", false: ", console not named"}[named] + ")"
	}
	return score, reason
}

// LooseScore is the title/evidence score before the developer/console
// confirmation — used to decide which unconfirmed pages are worth opening.
func LooseScore(raTitle string, console Console, developers []string, c Candidate) int {
	s, _, _ := scoreLoose(raTitle, console, developers, c)
	return s
}

func scoreLoose(raTitle string, console Console, developers []string, c Candidate) (int, string, int) {
	want := NormTitle(raTitle)
	wantMain := NormTitle(MainTitle(CleanTitle(raTitle)))
	// "Double Symbol for Sega Genesis / Mega Drive / 32X" and "Saint Seiya
	// (GameBoy Color)" are the plain title plus a platform note.
	got := NormTitle(StripPlatformNote(c.Title))
	if want == "" || got == "" {
		return 0, "empty title", 0
	}
	score, reason := 0, ""
	switch {
	case got == want:
		score, reason = 100, "exact title"
	case strings.ReplaceAll(got, " ", "") == strings.ReplaceAll(want, " ", ""):
		// Only the spacing differs: RA's "Ring Dash GBA" is the page's
		// "RingDash GBA". Compared word by word they shared just "gba".
		score, reason = 100, "same title, spaced differently"
	case got == wantMain || NormTitle(MainTitle(c.Title)) == want:
		score, reason = 80, "title without subtitle"
	case strings.ReplaceAll(got, " ", "") == strings.ReplaceAll(wantMain, " ", ""):
		score, reason = 80, "title without subtitle, spaced differently"
	case containsWords(got, want) || containsWords(want, got):
		score, reason = 50, "title contains the other"
	case allWords(want, got):
		// Every word of the RA title, with extras in between:
		// "Silver Falls Mini (Monsters In North Island)".
		score, reason = 70, "title has every word of the RA title"
	default:
		if j := jaccard(got, want); j >= 0.6 {
			score, reason = int(j*50), "similar words"
		}
	}
	// A game is often published on itch.io under another name: "Saint
	// Seiya - El regreso del Fénix" is RA's "Knights of the Zodiac: The
	// Phoenix Returns". Other evidence then counts: the page's own text
	// naming the RA title, or being a top web result for title + developer.
	// (A wrong pick is harmless: the hash check rejects it.)
	// The full title in the page text beats a partial one ("Silver Falls:
	// Monsters in North Island" vs pages that only say "Silver Falls").
	// In the title, the full RA title is strong; only in the text it is
	// weaker (a developer's other game may say "see also Dottie Flowers").
	if score < 60 && mentionsFullTitle(c.Title, raTitle) {
		score, reason = 60, "title names the full title"
	} else if score < 40 && mentionsFullTitle(c.Text, raTitle) {
		score, reason = 40, "page text names the full title"
	} else if score < 35 && mentionsTitle(c.Text+" "+c.Title, raTitle) {
		score, reason = 35, "page text names the game"
	}
	// Being a top result for "title + developer" is only a hint: it needs
	// the same developer or the console, AND a real word of the title on
	// the page ("Health Potion Bottle" is not "Go Catch 'Em").
	if score == 0 && c.DevQuery && c.WebRank > 0 && c.WebRank <= 3 &&
		(IsSameDeveloper(developers, c) || MentionsConsole(c.Title+" "+c.Text, console)) &&
		sharesKeyword(c.Title+" "+c.Text, raTitle) {
		score, reason = 35, "top web result for title + developer"
	}
	titleScore := score
	if score == 0 {
		return 0, "title does not match", 0
	}
	// Whole words only: "ost" must not hit "Ghost".
	lowTitle := " " + NormLoose(c.Title) + " "
	lowRA := " " + NormLoose(raTitle) + " "
	for _, w := range offTargetWords {
		w = " " + NormLoose(w) + " "
		if strings.Contains(lowTitle, w) && !strings.Contains(lowRA, w) {
			score -= 60
			reason += ", looks like" + strings.TrimRight(w, " ")
			break
		}
	}
	if IsSameDeveloper(developers, c) {
		score += 40
		reason += ", same developer"
	}
	// Mentioning the console is strong evidence: many unrelated PC or
	// browser games share short titles like "Punch Out".
	if MentionsConsole(c.Title+" "+c.Text+" "+c.URL, console) {
		score += 30
		reason += ", mentions " + console.Name
	} else if c.Browser || len(c.Platforms) > 0 {
		score -= 25
		reason += ", looks like a PC/browser game"
	}
	// Demo vs full release. Developers often keep both pages (Goodboy
	// Galaxy DEMO / Goodboy Galaxy (GBA)): RA's Demo entry wants the demo
	// page, the full entry wants the full one.
	candDemo := strings.Contains(" "+NormLoose(c.Title+" "+c.URL)+" ", " demo ")
	switch {
	case IsDemo(raTitle) && candDemo:
		score += 40
		reason += ", demo page"
	case IsDemo(raTitle):
		score -= 30
		reason += ", full release page (RA has the demo)"
	case candDemo && !strings.Contains(" "+NormLoose(raTitle)+" ", " demo "):
		score -= 40
		reason += ", demo page (RA has the full game)"
	}
	// The browser edition of a game has no ROM: its sibling page does.
	if IsBrowserVersion(c.Title) && !IsBrowserVersion(raTitle) {
		score -= 50
		reason += ", browser version (no ROM)"
	}
	// Made for another platform ("... 3DS", "(PSVita and Android)", or a
	// page saying "finally arrives on the PSVita")?
	if !MentionsConsole(c.Title, console) && mentionsOtherPlatform(c.Title, raTitle, console) {
		score -= 40
		reason += ", made for another platform"
	} else if !MentionsConsole(c.Title+" "+c.Text, console) && mentionsOtherPlatform(c.Text, raTitle, console) {
		score -= 30
		reason += ", page names another platform"
	}
	// Sequels are a different game: "Foo 2" must not match "Foo".
	if !sameNumbers(got, want) && score < 100 {
		score -= 40
		reason += ", different number"
	}
	return score, reason, titleScore
}

var numberWord = regexp.MustCompile(`^(\d+|ii|iii|iv|vi|vii|viii|ix)$`)

// sameNumbers: "Foo 2" is not "Foo", but "Hong Kong 2099 for gameboy" is
// "Hong Kong 2099" — compare the sets of numbers, not their position.
func sameNumbers(a, b string) bool {
	set := func(s string) string {
		var n []string
		for _, w := range strings.Fields(s) {
			if numberWord.MatchString(w) {
				n = append(n, w)
			}
		}
		sort.Strings(n)
		return strings.Join(n, " ")
	}
	return set(a) == set(b)
}

// RankCandidates scores, filters (score >= minScore) and sorts candidates,
// dropping duplicate URLs. max limits the result.
func RankCandidates(raTitle, developer string, in []Candidate, minScore, max int) []Candidate {
	return RankFor(raTitle, Console{}, []string{developer}, in, minScore, max)
}

// RankFor is RankCandidates with the console and all developer hints.
func RankFor(raTitle string, console Console, developers []string, in []Candidate, minScore, max int) []Candidate {
	return RankForTitles([]string{raTitle}, console, developers, in, minScore, max)
}

func containsWords(hay, needle string) bool {
	return strings.Contains(" "+hay+" ", " "+needle+" ")
}

func jaccard(a, b string) float64 {
	sa, sb := map[string]bool{}, map[string]bool{}
	for _, w := range strings.Fields(a) {
		sa[w] = true
	}
	for _, w := range strings.Fields(b) {
		sb[w] = true
	}
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func compact(s string) string {
	var b strings.Builder
	for _, r := range foldAccents(strings.ToLower(s)) {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var trailingNumber = regexp.MustCompile(`(^| )(\d+|ii|iii|iv|v|vi)$`)

func hasTrailingNumber(s string) bool { return trailingNumber.MatchString(s) }

// IsSameDeveloper reports whether a candidate's author (or its itch.io
// subdomain) matches one of the developer hints.
func IsSameDeveloper(developers []string, c Candidate) bool {
	sub := ""
	if u := strings.TrimPrefix(strings.TrimPrefix(c.URL, "https://"), "http://"); strings.Contains(u, ".itch.io") {
		sub = compact(u[:strings.Index(u, ".itch.io")])
	}
	a := compact(c.Author)
	authorWords := wordSet(c.Author)
	text := " " + NormLoose(c.Text) + " "
	for _, dev := range developers {
		d := compact(dev)
		if len(d) < 3 {
			continue
		}
		if nameMatch(d, a) || nameMatch(d, sub) {
			return true
		}
		// Credited on the page: "The developers: TLT, Tomahomae" on a page
		// published by a group account (gcup.itch.io).
		if n := NormLoose(dev); len(n) >= 3 && strings.Contains(text, " "+n+" ") {
			return true
		}
		// Same words in another order: "Davy Willems" = "Willems Davy".
		if dw := wordSet(dev); len(dw) >= 2 && len(authorWords) >= 2 {
			all := true
			for w := range dw {
				if !authorWords[w] {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(NormLoose(s)) {
		if len(w) >= 2 {
			out[w] = true
		}
	}
	return out
}

var browserOnlyWords = []string{"browser", "html5", "web version", "web build", "webgl", "online version", "play online"}

// IsBrowserVersion reports a page that is only the play-in-browser version
// ("SLENDER THE 8 GBC PAGES (Browser)"): there is no ROM to download there.
func IsBrowserVersion(title string) bool {
	hay := " " + NormLoose(title) + " "
	for _, w := range browserOnlyWords {
		if strings.Contains(hay, " "+NormLoose(w)+" ") {
			return true
		}
	}
	return false
}

// MentionsConsole reports whether text names the console ("Atari 2600",
// "2600", "NES", "Game Boy", ...). Short codes must be whole words.
func MentionsConsole(text string, c Console) bool {
	if c.ID == 0 {
		return false
	}
	low := " " + NormLoose(text) + " "
	for _, w := range consoleWords(c) {
		if w != "" && strings.Contains(low, " "+w+" ") {
			return true
		}
	}
	return false
}

// NormLoose lowercases and turns everything that is not a letter or digit
// into spaces (no article or tag stripping).
func NormLoose(s string) string {
	var b strings.Builder
	for _, r := range foldAccents(strings.ToLower(s)) {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

var extraConsoleWords = map[int][]string{
	1: {"genesis", "mega drive", "megadrive", "sega genesis"},
	3: {"snes", "super nintendo", "super famicom"},
	4: {"game boy", "gameboy", "gb", "dmg", "gb studio", "gbstudio"},
	5: {"gba", "game boy advance", "gameboy advance"},
	// GBC plays Game Boy games, and many GBC pages just say "Game Boy".
	6:  {"gbc", "game boy color", "gameboy color", "gb studio", "gbstudio", "game boy", "gameboy", "gb"},
	7:  {"nes", "famicom", "nesmaker"},
	8:  {"pc engine", "turbografx", "pce"},
	11: {"master system", "sms"},
	13: {"lynx", "atari lynx"},
	15: {"game gear", "gamegear"},
	18: {"nds", "nintendo ds"},
	25: {"atari 2600", "2600", "atari vcs", "vcs"},
	29: {"msx", "msx2"},
	33: {"sg 1000", "sg1000"},
	37: {"amstrad", "cpc"},
	46: {"vectrex"},
	51: {"atari 7800", "7800"},
	57: {"channel f", "fairchild"},
	69: {"mega duck", "megaduck"},
	71: {"arduboy"},
	72: {"wasm 4", "wasm4"},
	24: {"pokemon mini", "pokemini", "poke mini"},
	80: {"uzebox"},
}

func consoleWords(c Console) []string {
	words := []string{NormLoose(c.Name), NormLoose(c.Short)}
	for _, w := range extraConsoleWords[c.ID] {
		words = append(words, NormLoose(w))
	}
	return words
}

// hashNameTags are parenthesised tokens in RA hash file names that are NOT
// developer names.
var hashNameTags = regexp.MustCompile(`(?i)^(ntsc|pal|secam|usa|us|europe|eu|japan|jp|world|en|fr|de|es|it|pt|br|` +
	`aftermarket|unl|unlicensed|homebrew|proto|prototype|demo|beta|alpha|sample|hack|translation|` +
	`rev ?[0-9a-z.]*|v ?[0-9][0-9a-z.]*|[0-9]{4}(-[0-9]{2}(-[0-9]{2})?)?|[0-9.]+|final|release|` +
	`itch\.?io|atari|sega|nintendo|virtual console|limited run|collector|cart|cartridge|patched|` +
	`[a-z]{2}(,[a-z]{2})+)$`)

var parenToken = regexp.MustCompile(`\(([^()]+)\)`)

// DeveloperHintsFromHashNames extracts likely developer names from RA's
// hash file names: "Alien Force (NTSC) (Aftermarket) (Unl) (Nova32).a26"
// gives "Nova32".
func DeveloperHintsFromHashNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		for _, m := range parenToken.FindAllStringSubmatch(n, -1) {
			tok := strings.TrimSpace(m[1])
			if len(tok) < 3 || hashNameTags.MatchString(tok) {
				continue
			}
			for _, part := range strings.Split(tok, ",") {
				part = strings.TrimSpace(part)
				k := compact(part)
				if len(k) < 3 || seen[k] || hashNameTags.MatchString(part) {
					continue
				}
				seen[k] = true
				out = append(out, part)
			}
		}
	}
	return out
}

// nameMatch compares compacted names: equal, or one containing the other
// when the shorter is long enough to be meaningful ("a" must not match
// "nova32").
func nameMatch(x, y string) bool {
	if x == "" || y == "" {
		return false
	}
	if x == y {
		return true
	}
	short, long := x, y
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) >= 4 && strings.Contains(long, short) {
		return true
	}
	// "zeichigameplayshort" (RA) vs "zeichigames" (itch.io): a shared
	// distinctive start is the same person or studio.
	common := 0
	for common < len(short) && short[common] == long[common] {
		common++
	}
	return common >= 6
}

// mentionsTitle reports whether text contains the RA title (or its main
// part before a subtitle) as whole words; at least two words or 8 letters,
// so a one-word title does not match every page that uses the word.
func mentionsTitle(text, raTitle string) bool {
	if text == "" {
		return false
	}
	hay := " " + NormLoose(text) + " "
	clean := CleanTitle(raTitle)
	candidates := []string{clean, MainTitle(clean)}
	// The subtitle alone: "The Phoenix Returns" of "Knights of the Zodiac:
	// The Phoenix Returns" (needs 2+ words, checked below).
	if i := strings.Index(clean, ": "); i > 0 {
		candidates = append(candidates, clean[i+2:])
	}
	for _, t := range candidates {
		n := NormLoose(t)
		if len(strings.Fields(n)) < 2 && len(n) < 8 {
			continue
		}
		if strings.Contains(hay, " "+n+" ") {
			return true
		}
	}
	return false
}

// RankForTitles is RankFor where a candidate may match any of several names
// for the game (its RA title plus alternative titles); the best score counts.
func RankForTitles(titles []string, console Console, developers []string, in []Candidate, minScore, max int) []Candidate {
	// The same page can arrive several times (a search-result cell, then the
	// same page opened and read in full): keep its BEST judgement, not the
	// first one.
	best := map[string]int{}
	var out []Candidate
	for _, c := range in {
		key := NormPageURL(c.URL)
		if key == "" {
			continue
		}
		if c.Source == "override" {
			c.Score, c.Reason = 1000, "manual override"
		} else {
			c.Score = -1 << 30
			for _, t := range titles {
				if s, why := ScoreFor(t, console, developers, c); s > c.Score {
					c.Score, c.Reason = s, why
				}
			}
		}
		if i, seen := best[key]; seen {
			if c.Score > out[i].Score {
				out[i] = c
			}
			continue
		}
		best[key] = len(out)
		out = append(out, c)
	}
	kept := out[:0]
	for _, c := range out {
		if c.Score >= minScore {
			kept = append(kept, c)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Score > kept[j].Score })
	if max > 0 && len(kept) > max {
		kept = kept[:max]
	}
	return kept
}

var bracketGroups = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)

// TitlesFromHashNames turns RA hash file names into titles:
// "Casanova (World) (Aftermarket) (Unl).md" → "Casanova".
func TitlesFromHashNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		base := strings.TrimSuffix(n, filepath.Ext(n))
		t := strings.TrimSpace(bracketGroups.ReplaceAllString(base, ""))
		t = strings.NewReplacer("_", " ").Replace(t)
		k := NormTitle(t)
		if len(k) < 3 || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

var (
	dropPrefixes = []string{"mega ", "super ", "ultra ", "micro ", "mini ", "tiny ", "little ", "new ", "the "}
	dropSuffixes = []string{" dx", " deluxe", " remastered", " remaster", " edition", " plus", " special edition",
		" homebrew", " demo", " se"}
)

// ShorterTitles drops a marketing word at either end:
// "Mega Casanova" → "Casanova", "Foo DX" → "Foo".
func ShorterTitles(title string) []string {
	low := strings.ToLower(title)
	var out []string
	for _, p := range dropPrefixes {
		if strings.HasPrefix(low, p) && len(low) > len(p)+2 {
			out = append(out, strings.TrimSpace(title[len(p):]))
		}
	}
	for _, s := range dropSuffixes {
		if strings.HasSuffix(low, s) && len(low) > len(s)+2 {
			out = append(out, strings.TrimSpace(title[:len(title)-len(s)]))
		}
	}
	return out
}

// mentionsFullTitle is mentionsTitle for the whole cleaned title only.
func mentionsFullTitle(text, raTitle string) bool {
	n := NormLoose(CleanTitle(raTitle))
	if len(strings.Fields(n)) < 2 {
		return false
	}
	return strings.Contains(" "+NormLoose(text)+" ", " "+n+" ")
}

var commonWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "from": true, "game": true,
	"games": true, "of": true, "in": true, "on": true, "to": true, "a": true, "an": true, "edition": true,
	"homebrew": true, "demo": true, "version": true, "color": true, "boy": true}

// sharesKeyword reports whether text contains at least one distinctive
// word (4+ letters, not a common word) of the RA title.
func sharesKeyword(text, raTitle string) bool {
	hay := " " + NormLoose(text) + " "
	for _, w := range strings.Fields(NormLoose(CleanTitle(raTitle))) {
		if len(w) >= 4 && !commonWords[w] && strings.Contains(hay, " "+w+" ") {
			return true
		}
	}
	return false
}

var otherPlatformWords = []string{"3ds", "psvita", "ps vita", "vita", "switch", "android", "ios", "windows",
	"pc", "mac", "linux", "steam", "ps4", "ps5", "xbox", "html5", "web", "browser"}

// mentionsOtherPlatform reports whether a title names a platform that is
// not this console ("Silver Falls ... 3DS" for a Game Boy game).
func mentionsOtherPlatform(title, raTitle string, c Console) bool {
	if c.ID == 0 {
		return false
	}
	// Words that are part of the RA title itself ("Spider Web") don't count.
	own := map[string]bool{}
	for _, w := range strings.Fields(NormLoose(raTitle)) {
		own[w] = true
	}
	var kept []string
	for _, w := range strings.Fields(NormLoose(title)) {
		if !own[w] {
			kept = append(kept, w)
		}
	}
	hay := " " + strings.Join(kept, " ") + " "
	for _, w := range otherPlatformWords {
		if strings.Contains(hay, " "+NormLoose(w)+" ") {
			return true
		}
	}
	for id, other := range consoles {
		if id == c.ID {
			continue
		}
		for _, w := range consoleWords(other) {
			if len(w) >= 3 && strings.Contains(hay, " "+w+" ") && !MentionsConsole(w, c) {
				return true
			}
		}
	}
	return false
}

// foldAccents turns "Pokémon" into "Pokemon", "László" into "Laszlo".
func foldAccents(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var platformNote = regexp.MustCompile(`(?i)\s*[\(\[][^\)\]]*[\)\]]\s*$`)

// StripPlatformNote removes a trailing platform note from an itch.io title:
// "(GameBoy Color)", "for Sega Genesis / Mega Drive", "- Game Boy Color".
func StripPlatformNote(title string) string {
	t := strings.TrimSpace(title)
	for i := 0; i < 3; i++ {
		before := t
		if m := platformNote.FindStringIndex(t); m != nil && m[0] > 0 && namesAPlatform(t[m[0]:]) {
			t = strings.TrimSpace(t[:m[0]])
		}
		low := strings.ToLower(t)
		for _, sep := range []string{" for the ", " for ", " on ", " - ", " – ", " | "} {
			if i := strings.LastIndex(low, sep); i > 0 && namesAPlatform(t[i+len(sep):]) {
				t = strings.TrimSpace(t[:i])
				low = strings.ToLower(t)
			}
		}
		if t == before {
			break
		}
	}
	return t
}

// namesAPlatform reports whether a short text is (mostly) a platform name.
func namesAPlatform(s string) bool {
	hay := " " + NormLoose(s) + " "
	if len(strings.Fields(hay)) > 8 {
		return false
	}
	for _, c := range consoles {
		for _, w := range consoleWords(c) {
			if len(w) >= 2 && strings.Contains(hay, " "+w+" ") {
				return true
			}
		}
	}
	for _, w := range otherPlatformWords {
		if strings.Contains(hay, " "+NormLoose(w)+" ") {
			return true
		}
	}
	return false
}

// allWords reports whether every word of want (2+ words) appears in got,
// in order, possibly with other words between.
func allWords(want, got string) bool {
	w := strings.Fields(want)
	if len(w) < 2 {
		return false
	}
	g := strings.Fields(got)
	i := 0
	for _, x := range g {
		if i < len(w) && x == w[i] {
			i++
		}
	}
	return i == len(w)
}

// TitleTags returns RetroAchievements' title tags: "~Homebrew~ ~Demo~ Foo"
// gives ["Homebrew", "Demo"]. "(Demo)"/"[Demo]"-style notes count too.
func TitleTags(raTitle string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		k := strings.ToLower(t)
		if t == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, t)
	}
	for _, m := range regexp.MustCompile(`~([^~]+)~`).FindAllStringSubmatch(raTitle, -1) {
		add(m[1])
	}
	for _, m := range regexp.MustCompile(`(?i)[\(\[](demo|prototype|beta|proto|test kit|unlicensed)[\)\]]`).FindAllStringSubmatch(raTitle, -1) {
		add(strings.Title(strings.ToLower(m[1])))
	}
	return out
}

// IsDemo reports whether RetroAchievements marks the game as a demo.
func IsDemo(raTitle string) bool {
	for _, t := range TitleTags(raTitle) {
		if strings.EqualFold(t, "demo") {
			return true
		}
	}
	return false
}

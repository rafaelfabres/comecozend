package catalog

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// articles are dropped entirely rather than moved, because the two sources
// disagree about where they belong: RetroAchievements titles a game "The
// 7th Saga" while the RAPatches folder is "7th Saga, The".
var articles = map[string]bool{"the": true, "a": true, "an": true}

// filler words carry no meaning for identification and are the single
// biggest reason titles failed to line up. RetroAchievements writes
// "Pokémon: Emerald Version"; the patch folder is "Pokemon Emerald". Drop
// "version" and the two become the same game.
var filler = map[string]bool{"version": true, "edition": true}

// romanSuffix lets "Shining Force II" and "Shining Force 2" meet.
var romanSuffix = map[string]string{
	"ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6",
	"vii": "7", "viii": "8", "ix": "9", "x": "10",
}

// accents folds the Latin letters that differ between the two sources.
// This is the other half of the Pokémon problem: RetroAchievements spells
// it "Pokémon" and a folder name cannot, so 228 GBA hacks were invisible
// over one character.
var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c", "ý", "y", "ÿ", "y", "æ", "ae", "œ", "oe", "ß", "ss",
)

// NormalizeTitle reduces a game title to a form that survives the
// differences between RetroAchievements' titles and RAPatches' folder
// names: punctuation varies (a colon becomes " - " in a path), articles
// move, numbering styles differ and accents cannot appear in a folder.
func NormalizeTitle(s string) string {
	s = accents.Replace(strings.ToLower(s))

	// Region and tag suffixes RA sometimes carries.
	for _, cut := range []string{"(usa", "(europe", "(japan", "(world", "[!]"} {
		if i := strings.Index(s, cut); i > 0 {
			s = s[:i]
		}
	}

	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}

	fields := strings.Fields(b.String())
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if articles[f] {
			continue
		}
		if n, ok := romanSuffix[f]; ok {
			f = n
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// bracketed matches the tag groups a dump filename carries: "(USA)",
// "(Rev 1)", "(En,Fr,De,Es,It)", "(Disc 1)", "[!]".
var bracketed = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)

// NormalizeDumpName reduces a ROM filename to the game it holds.
//
// NormalizeTitle cuts at the first region tag, which is enough only when
// the region comes first. "Crash Bandicoot 2 - Cortex Strikes Back
// (Europe) (En,Fr,De,Es,It)" survives that; put the language list first
// and the title comes out with "en fr de es it" glued to the end and
// matches nothing. Every tag group goes instead.
func NormalizeDumpName(filename string) string {
	if i := strings.LastIndexByte(filename, '.'); i > 0 {
		filename = filename[:i]
	}
	return NormalizeTitle(bracketed.ReplaceAllString(filename, " "))
}

// coreTitle is NormalizeTitle with filler words removed as well. It is the
// looser key, used only after an exact match has failed.
func coreTitle(s string) string {
	fields := strings.Fields(NormalizeTitle(s))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if filler[f] {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// TitlesMatch reports whether two titles describe the same game, by the
// exact and filler-insensitive rules. Prefix matching is deliberately not
// part of this: it is only safe when there is a set of candidates to check
// for ambiguity against, which is what titleMatcher does.
func TitlesMatch(a, b string) bool {
	na, nb := NormalizeTitle(a), NormalizeTitle(b)
	if na != "" && na == nb {
		return true
	}
	ca, cb := coreTitle(a), coreTitle(b)
	return ca != "" && ca == cb
}

// titleMatcher resolves a patch folder name to one of the games actually
// on the device.
//
// Three passes, loosening only when the stricter one finds nothing:
// exact, then ignoring filler words, then a prefix — and the prefix pass
// gives up when more than one game could be meant, because "Super Mario"
// matching both "Super Mario World" and "Super Mario Bros." would offer
// hacks that cannot possibly apply.
type titleMatcher[T any] struct {
	exact  map[string]T
	core   map[string]T
	ambigs map[string]bool
}

func newTitleMatcher[T any]() *titleMatcher[T] {
	return &titleMatcher[T]{
		exact:  map[string]T{},
		core:   map[string]T{},
		ambigs: map[string]bool{},
	}
}

func (m *titleMatcher[T]) add(title string, value T) {
	if n := NormalizeTitle(title); n != "" {
		m.exact[n] = value
	}
	if c := coreTitle(title); c != "" {
		if _, taken := m.core[c]; taken {
			m.ambigs[c] = true
		}
		m.core[c] = value
	}
}

func (m *titleMatcher[T]) find(title string) (T, bool) {
	var zero T
	if v, ok := m.exact[NormalizeTitle(title)]; ok {
		return v, true
	}
	key := coreTitle(title)
	if key == "" {
		return zero, false
	}
	if v, ok := m.core[key]; ok && !m.ambigs[key] {
		return v, true
	}

	// Prefix pass: the folder name is usually shorter than the full
	// title ("Pokemon Emerald" against "Pokémon: Emerald Version").
	// Matching on a word boundary avoids "Mario" catching "Mario Kart".
	var found T
	matches := 0
	for candidate, v := range m.core {
		if candidate == key || strings.HasPrefix(candidate, key+" ") {
			matches++
			if matches > 1 {
				return zero, false
			}
			found = v
		}
	}
	if matches == 1 {
		return found, true
	}
	return zero, false
}

// --- version ordering -------------------------------------------------

var versionPattern = regexp.MustCompile(`(?i)\bv?(\d+(?:\.\d+)*)\b`)

// VersionOf pulls the version out of a patch or hash filename, as a list
// of numbers.
//
// String ordering is wrong here and quietly so: "v2.10" sorts before
// "v2.9" alphabetically, which would install a year-old build of a hack
// that has since had ten releases. Comparing the numbers is the only way
// to get "newest" right.
func VersionOf(name string) []int {
	matches := versionPattern.FindAllStringSubmatch(name, -1)
	if len(matches) == 0 {
		return nil
	}
	// The last match wins: a name like "FE8 - The Hag in White (v2.5)"
	// can carry a number in the title itself, and the version is the one
	// nearest the end.
	parts := strings.Split(matches[len(matches)-1][1], ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// NewerVersion reports whether a names a later version than b. Names with
// no version at all compare as older, so a plain filename never displaces
// an explicitly numbered one.
func NewerVersion(a, b string) bool {
	va, vb := VersionOf(a), VersionOf(b)
	if va == nil && vb == nil {
		return a > b
	}
	if va == nil {
		return false
	}
	if vb == nil {
		return true
	}
	for i := 0; i < len(va) && i < len(vb); i++ {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	// "v2.1" against "v2.1.3": more components means a later release.
	return len(va) > len(vb)
}

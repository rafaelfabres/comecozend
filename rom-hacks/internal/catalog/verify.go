package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"leaf-hacks/internal/library"
	"leaf-hacks/internal/patch"
	"leaf-hacks/internal/rahub"
	"leaf-hacks/internal/rapatches"
)

// A patch declares what it was built against. A BPS stores the base ROM's
// CRC32 in its footer — the value it will actually verify before applying
// — and the readme beside it names the dump with an MD5. Both are
// numbers, and the ROM library has the same numbers for every file on the
// card, so "does this patch fit anything I own?" has an exact answer.
//
// That answer has always been available at install time. The problem was
// that discovery never used it: a hack reached the list only if a folder
// name could be matched to a game title, and the two sides are maintained
// by different people in different vocabularies. RetroAchievements writes
// "Pokémon: FireRed and LeafGreen Versions" and files 117 hacks under it;
// the patch repository has separate "Pokemon FireRed" and "Pokemon
// LeafGreen" folders. They do not even agree on how many games there are.
//
// So names now only propose. This is what decides.

// PatchFacts is what a patch archive says about the ROM it needs. It is a
// property of the patch, not of any particular device, which is why it is
// cached permanently and could later be shipped prebuilt.
type PatchFacts struct {
	// SourceCRC is the base ROM checksum from a BPS or UPS footer. This is
	// the strongest signal: it comes from the patch itself and is right
	// even when the readme beside it is wrong.
	SourceCRC uint32 `json:"source_crc,omitempty"`
	HasCRC    bool   `json:"has_crc,omitempty"`

	// MD5s and CRCs are what the readme claims, for formats that carry no
	// checksum of their own.
	MD5s []string `json:"md5s,omitempty"`
	CRCs []uint32 `json:"crcs,omitempty"`

	// BaseFile is the dump's name, for telling the user what to find.
	BaseFile string `json:"base_file,omitempty"`

	Checked time.Time `json:"checked"`
	// Failed records that the archive could not be read, so it is not
	// retried on every launch.
	Failed bool `json:"failed,omitempty"`
}

// Matches reports whether any ROM on the device is the base this patch
// needs, and which one.
func (f PatchFacts) Matches(lib *library.Library) (library.ROM, bool) {
	if lib == nil {
		return library.ROM{}, false
	}
	if f.HasCRC {
		if hits := lib.ByCRC32(f.SourceCRC); len(hits) > 0 {
			return hits[0], true
		}
	}
	for _, md5 := range f.MD5s {
		if hits := lib.ByMD5(md5); len(hits) > 0 {
			return hits[0], true
		}
	}
	for _, crc := range f.CRCs {
		if hits := lib.ByCRC32(crc); len(hits) > 0 {
			return hits[0], true
		}
	}
	return library.ROM{}, false
}

// Known reports whether the archive yielded anything to match on. An
// archive of only xdelta patches with no readme tells us nothing, and must
// fall back to the name match rather than be treated as a definite no.
func (f PatchFacts) Known() bool {
	return !f.Failed && (f.HasCRC || len(f.MD5s) > 0 || len(f.CRCs) > 0)
}

// FactsCache stores fingerprints by repository path.
type FactsCache struct {
	mu     sync.RWMutex
	byPath map[string]PatchFacts
	path   string
	dirty  int
}

func LoadFactsCache(path string) *FactsCache {
	c := &FactsCache{byPath: map[string]PatchFacts{}, path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.byPath)
	}
	return c
}

func (c *FactsCache) Get(repoPath string) (PatchFacts, bool) {
	if c == nil {
		return PatchFacts{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.byPath[repoPath]
	return f, ok
}

func (c *FactsCache) set(repoPath string, f PatchFacts) {
	c.mu.Lock()
	c.byPath[repoPath] = f
	c.dirty++
	flush := c.dirty >= 20
	if flush {
		c.dirty = 0
	}
	c.mu.Unlock()
	if flush {
		_ = c.Save()
	}
}

func (c *FactsCache) Count() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byPath)
}

func (c *FactsCache) Save() error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	b, err := json.Marshal(c.byPath)
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// maxVerifyBytes caps what the background pass will download per archive.
// The median hack archive is about 245 KB, but a handful run past 10 MB
// because they bundle every release of the hack. Those are left for the
// moment the user actually opens one, rather than spending a card's
// bandwidth on a hack nobody asked about.
const maxVerifyBytes = 6 << 20

// Fingerprint downloads one archive and reads what it says about its base
// ROM.
func Fingerprint(ctx context.Context, client *http.Client, e rapatches.Entry) PatchFacts {
	facts := PatchFacts{Checked: time.Now()}

	payload, closer, err := openForFingerprint(ctx, client, e)
	if err != nil {
		facts.Failed = true
		return facts
	}
	if closer != nil {
		defer closer.Close()
	}

	for i := range payload.Patches {
		pf := &payload.Patches[i]
		if crc, err := patch.SourceCRC32(pf.Bytes()); err == nil {
			facts.SourceCRC, facts.HasCRC = crc, true
			break
		}
	}
	seen := map[string]bool{}
	for _, b := range payload.Bases {
		if b.MD5 != "" && !seen[b.MD5] {
			seen[b.MD5] = true
			facts.MD5s = append(facts.MD5s, b.MD5)
		}
		if b.HeaderlessMD5 != "" && !seen[b.HeaderlessMD5] {
			seen[b.HeaderlessMD5] = true
			facts.MD5s = append(facts.MD5s, b.HeaderlessMD5)
		}
		if b.HasCRC {
			facts.CRCs = append(facts.CRCs, b.CRC32)
		}
		if facts.BaseFile == "" {
			facts.BaseFile = b.File
		}
	}
	return facts
}

// openForFingerprint reads an archive the cheapest way that works.
//
// Small ones are buffered; anything past the in-memory ceiling is
// streamed to the card instead. Nintendo DS is the case that makes this
// matter: a patch against a 512 MB cartridge dump is not small, and 143
// DS hacks are already in the repository. Refusing to fingerprint them
// would quietly send exactly those back to being matched by name.
func openForFingerprint(ctx context.Context, client *http.Client, e rapatches.Entry) (rapatches.Payload, io.Closer, error) {
	data, err := rapatches.Download(ctx, client, e, maxVerifyBytes)
	if err == nil {
		p, err := rapatches.Open(e.File, data)
		return p, nil, err
	}
	if !errors.Is(err, rapatches.ErrTooLarge) {
		return rapatches.Payload{}, nil, err
	}

	path, err := rapatches.DownloadTo(ctx, client, e, stagingDir())
	if err != nil {
		return rapatches.Payload{}, nil, err
	}
	p, closer, err := rapatches.OpenFile(path)
	if err != nil {
		os.Remove(path)
		return rapatches.Payload{}, nil, err
	}
	return p, removeOnClose{closer, path}, nil
}

// removeOnClose deletes the staged file once the payload is done with.
type removeOnClose struct {
	io.Closer
	path string
}

func (r removeOnClose) Close() error {
	err := r.Closer.Close()
	os.Remove(r.path)
	return err
}

// Candidate is an entry worth fingerprinting, with why it was picked.
type Candidate struct {
	Entry rapatches.Entry
	// Confirmed by name already; fingerprinting will either back that up
	// or reveal it was wrong.
	NameMatched bool
}

// stopTokens carry no identifying weight, so sharing one is not evidence
// that two titles are the same game.
var stopTokens = map[string]bool{
	"super": true, "the": true, "of": true, "and": true, "2": true, "3": true,
	"new": true, "game": true, "adventure": true, "world": true, "land": true,
}

// Candidates picks the patch entries worth checking against this device.
//
// Generous on purpose: the cost of including a folder that turns out not
// to fit is one small download, while the cost of excluding one is a hack
// the user never learns exists. Anything sharing a distinctive word with
// a game they own is in.
func Candidates(bases []OwnedBase, ix rapatches.Index, facts *FactsCache, categories ...rapatches.Category) []Candidate {
	if len(categories) == 0 {
		categories = []rapatches.Category{rapatches.Hacks}
	}
	wanted := map[rapatches.Category]bool{}
	for _, c := range categories {
		wanted[c] = true
	}

	// Token sets of every owned game, per console.
	owned := map[int][]map[string]bool{}
	matchers := map[int]*titleMatcher[*OwnedBase]{}
	for i := range bases {
		id := bases[i].ConsoleID
		owned[id] = append(owned[id], tokenSet(bases[i].Title))
		m := matchers[id]
		if m == nil {
			m = newTitleMatcher[*OwnedBase]()
			matchers[id] = m
		}
		m.add(bases[i].Title, &bases[i])
	}

	var out []Candidate
	for _, e := range ix.Entries {
		if !wanted[e.Category] {
			continue
		}
		c, ok := rahub.ConsoleForPatchDir(e.Console)
		if !ok {
			continue
		}
		console := c.ID
		sets, haveConsole := owned[console]
		if !haveConsole {
			continue // no ROMs for this system at all
		}
		if _, done := facts.Get(e.Path); done {
			continue // already fingerprinted
		}

		nameMatched := false
		if m := matchers[console]; m != nil {
			if _, ok := m.find(e.BaseGame); ok {
				nameMatched = true
			}
		}
		if !nameMatched && !plausible(tokenSet(e.BaseGame), sets) {
			continue
		}
		out = append(out, Candidate{Entry: e, NameMatched: nameMatched})
	}
	return out
}

func tokenSet(title string) map[string]bool {
	set := map[string]bool{}
	for _, t := range strings.Fields(coreTitle(title)) {
		set[t] = true
	}
	return set
}

// plausible reports whether a folder could be about one of the owned
// games: it shares a distinctive word, and one side's words are largely
// contained in the other's.
func plausible(folder map[string]bool, owned []map[string]bool) bool {
	if len(folder) == 0 {
		return false
	}
	for _, game := range owned {
		shared, distinctive := 0, 0
		for t := range folder {
			if game[t] {
				shared++
				if !stopTokens[t] {
					distinctive++
				}
			}
		}
		if distinctive == 0 {
			continue
		}
		smaller := len(folder)
		if len(game) < smaller {
			smaller = len(game)
		}
		// Half the shorter title's words in common, with at least one of
		// them carrying real weight.
		if shared*2 >= smaller {
			return true
		}
	}
	return false
}

// Verify fingerprints candidates one at a time, pausing between
// downloads, and calls onProgress as results land. It is meant to run in
// the background for as long as the app is open; the cache makes it a
// one-time cost per patch.
func (c *FactsCache) Verify(ctx context.Context, client *http.Client, candidates []Candidate, onProgress func(done, total, found int)) {
	found := 0
	for i, cand := range candidates {
		select {
		case <-ctx.Done():
			_ = c.Save()
			return
		default:
		}

		facts := Fingerprint(ctx, client, cand.Entry)
		c.set(cand.Entry.Path, facts)
		if facts.Known() {
			found++
		}
		if onProgress != nil {
			onProgress(i+1, len(candidates), found)
		}

		select {
		case <-ctx.Done():
			_ = c.Save()
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
	_ = c.Save()
}

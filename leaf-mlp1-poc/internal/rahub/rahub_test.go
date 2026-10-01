package rahub

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

// ---- RetroAchievements ----------------------------------------------------

func TestFetchHubPaginatesAndHashesBulk(t *testing.T) {
	const key = "SECRETKEY123"
	var hashCalls, perGame int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/internal-api/hub/3036/games"):
			page := r.URL.Query().Get("page[number]")
			if r.URL.Query().Get("filter[achievementsPublished]") != "has" {
				t.Errorf("filter not sent")
			}
			items := map[string]string{
				"1": `{"game":{"id":26007,"title":"~Homebrew~ Alien Force","achievementsPublished":16,"badgeUrl":"/Images/1.png","system":{"id":25,"name":"Atari 2600","nameShort":"2600"}}}`,
				"2": `{"game":{"id":500,"title":"~Homebrew~ Nes Thing","achievementsPublished":3,"badgeUrl":"https://m/2.png","system":{"id":7,"name":"NES/Famicom","nameShort":"NES"}}}`,
				"3": `{"game":{"id":501,"title":"~Homebrew~ Orphan","achievementsPublished":3,"badgeUrl":"","system":{"id":7,"name":"NES/Famicom","nameShort":"NES"}}}`,
			}[page]
			fmt.Fprintf(w, `{"currentPage":%s,"lastPage":3,"perPage":1,"total":3,"items":[%s]}`, page, items)
		case r.URL.Path == "/API/API_GetGameList.php":
			atomic.AddInt32(&hashCalls, 1)
			if r.URL.Query().Get("h") != "1" || r.URL.Query().Get("y") != key {
				t.Errorf("bad game list query")
			}
			switch r.URL.Query().Get("i") {
			case "25":
				fmt.Fprint(w, `[{"ID":26007,"Title":"x","ConsoleID":25,"Hashes":["E204A6B63A8E6E1400FDE46BBD054722"]}]`)
			case "7":
				fmt.Fprint(w, `[{"ID":500,"Title":"y","ConsoleID":7,"Hashes":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]}]`)
			}
		case r.URL.Path == "/API/API_GetGameHashes.php":
			atomic.AddInt32(&perGame, 1)
			fmt.Fprint(w, `{"Results":[{"Name":"o.nes","MD5":"cccccccccccccccccccccccccccccccc","Labels":["homebrew"],"PatchUrl":null},
				{"Name":"o (patched).nes","MD5":"dddddddddddddddddddddddddddddddd","Labels":[],"PatchUrl":"https://x/p.zip"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	RABase = srv.URL
	defer func() { RABase = "https://retroachievements.org" }()

	ra := NewRAClient(Credentials{User: "nicefrog", Key: key}, nil)
	ra.Delay = 0
	games, err := ra.FetchHub(context.Background(), 3036, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 3 {
		t.Fatalf("want 3 games across 3 pages, got %d", len(games))
	}
	if games[0].BadgeURL != "https://media.retroachievements.org/Images/1.png" {
		t.Errorf("badge url: %s", games[0].BadgeURL)
	}
	if err := ra.AttachHashes(context.Background(), games, t.Logf); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 2 {
		t.Errorf("want one bulk call per console (2), got %d", hashCalls)
	}
	if perGame != 1 {
		t.Errorf("only the game missing from the bulk list should use the fallback, got %d", perGame)
	}
	if !games[0].HasHash("e204a6b63a8e6e1400fde46bbd054722") {
		t.Errorf("hash not attached / not lowercased: %v", games[0].Hashes)
	}
	if len(games[1].Hashes) != 2 {
		t.Errorf("multiple hashes must all be kept: %v", games[1].Hashes)
	}
	orphan := games[2]
	if len(orphan.Hashes) != 1 || orphan.PatchHashes["dddddddddddddddddddddddddddddddd"] == "" {
		t.Errorf("patch hash must be kept apart: %+v", orphan)
	}
}

func TestKeyNeverInErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	RABase = srv.URL
	defer func() { RABase = "https://retroachievements.org" }()
	ra := NewRAClient(Credentials{User: "u", Key: "TOPSECRET"}, nil)
	_, err := ra.FetchConsoleHashes(context.Background(), 25)
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("error must exist and not contain the key: %v", err)
	}
	// Transport errors carry the full URL; they must be redacted too.
	RABase = "http://127.0.0.1:1"
	_, err = ra.FetchConsoleHashes(context.Background(), 25)
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("transport error leaked the key: %v", err)
	}
}

// ---- hashing ----------------------------------------------------------------

func TestRAHashMethods(t *testing.T) {
	body := []byte(strings.Repeat("ROMDATA!", 4096)) // 32 KiB

	nes := append([]byte("NES\x1a"), make([]byte, 12)...)
	nes = append(nes, body...)
	if h, _ := HashBytes(ConsoleFor(7, "", ""), nes, "x.nes"); h != md5hex(body) {
		t.Error("NES must skip the 16-byte iNES header")
	}
	snes := append(make([]byte, 512), body...)
	if h, _ := HashBytes(ConsoleFor(3, "", ""), snes, "x.smc"); h != md5hex(body) {
		t.Error("SNES must skip a 512-byte copier header")
	}
	if h, _ := HashBytes(ConsoleFor(3, "", ""), body, "x.sfc"); h != md5hex(body) {
		t.Error("SNES without header must hash whole file")
	}
	a78 := make([]byte, 128)
	copy(a78[1:], "ATARI7800")
	a78 = append(a78, body...)
	if h, _ := HashBytes(ConsoleFor(51, "", ""), a78, "x.a78"); h != md5hex(body) {
		t.Error("7800 must skip the 128-byte header")
	}
	lnx := append([]byte("LYNX\x00"), make([]byte, 59)...)
	lnx = append(lnx, body...)
	if h, _ := HashBytes(ConsoleFor(13, "", ""), lnx, "x.lnx"); h != md5hex(body) {
		t.Error("Lynx must skip the 64-byte header")
	}
	// N64: .v64 (byte-swapped) must hash as its .z64 form.
	z64 := append([]byte{0x80, 0x37, 0x12, 0x40}, body...)
	v64 := make([]byte, len(z64))
	for i := 0; i < len(z64); i += 2 {
		v64[i], v64[i+1] = z64[i+1], z64[i]
	}
	n64 := make([]byte, len(z64))
	for i := 0; i < len(z64); i += 4 {
		n64[i], n64[i+1], n64[i+2], n64[i+3] = z64[i+3], z64[i+2], z64[i+1], z64[i]
	}
	want := md5hex(z64)
	for name, data := range map[string][]byte{"z64": z64, "v64": v64, "n64": n64} {
		if h, _ := HashBytes(ConsoleFor(2, "", ""), data, "x."+name); h != want {
			t.Errorf("N64 %s not normalised", name)
		}
	}
	if h, _ := HashBytes(ConsoleFor(25, "", ""), body, "x.a26"); h != md5hex(body) {
		t.Error("2600 is whole-file MD5")
	}
	if h, _ := HashBytes(ConsoleFor(71, "", ""), []byte(":00\r\n:01\r\n"), "x.hex"); h != md5hex([]byte(":00\n:01\n")) {
		t.Error("Arduboy must normalise line endings")
	}
	if _, err := HashBytes(ConsoleFor(12, "", ""), body, "x.bin"); err == nil {
		t.Error("PSX must report unsupported")
	}
}

func TestNDSHash(t *testing.T) {
	rom := make([]byte, 0x4000)
	for i := range rom {
		rom[i] = byte(i * 7)
	}
	put := func(at, v int) { binary.LittleEndian.PutUint32(rom[at:], uint32(v)) }
	put(0x20, 0x200)
	put(0x2C, 0x100)
	put(0x30, 0x400)
	put(0x3C, 0x80)
	put(0x68, 0x1000)
	want := md5.New()
	want.Write(rom[:0x160])
	want.Write(rom[0x200:0x300])
	want.Write(rom[0x400:0x480])
	want.Write(rom[0x1000:0x1A00])
	h, err := HashBytes(ConsoleFor(18, "", ""), rom, "x.nds")
	if err != nil || h != hex.EncodeToString(want.Sum(nil)) {
		t.Fatalf("nds hash mismatch: %v", err)
	}
}

// ---- matching & search ----------------------------------------------------

func TestCleanAndScore(t *testing.T) {
	if got := CleanTitle("~Homebrew~ Alien Force"); got != "Alien Force" {
		t.Errorf("clean: %q", got)
	}
	if got := CleanTitle("~Homebrew~ Legend of Frog, The"); got != "The Legend of Frog" {
		t.Errorf("article: %q", got)
	}
	cases := []struct {
		title string
		ok    bool
	}{
		{"Alien Force", true},
		{"ALIEN FORCE!", true},
		{"Alien Force (Atari 2600)", true},
		{"Alien Force 2", false},
		{"Alien Force OST", false},
		{"Totally Different", false},
	}
	for _, c := range cases {
		s, why := ScoreCandidate("~Homebrew~ Alien Force", "", Candidate{Title: c.title})
		if (s >= 30) != c.ok {
			t.Errorf("%q: score %d (%s), want ok=%v", c.title, s, why, c.ok)
		}
	}
	ranked := RankCandidates("~Homebrew~ Alien Force", "Nova32", []Candidate{
		{URL: "https://a.itch.io/af", Title: "Alien Force", Author: "someone"},
		{URL: "https://nova32.itch.io/alien-force", Title: "Alien Force", Author: "Nova32"},
		{URL: "https://nova32.itch.io/alien-force/", Title: "dupe", Author: "Nova32"},
	}, 30, 3)
	if len(ranked) != 2 || !strings.Contains(ranked[0].URL, "nova32") {
		t.Errorf("developer should win ties and dupes collapse: %+v", ranked)
	}
}

const searchFixture = `<html><body><div class="game_grid_widget">
<div data-game_id="1" class="game_cell has_cover lazy_images">
 <div class="game_thumb"><a class="thumb_link game_link" href="https://nova32.itch.io/alien-force"><img data-lazy_src="https://img.itch.zone/c1.png"/></a></div>
 <div class="game_cell_data"><div class="game_title"><a class="title game_link" href="https://nova32.itch.io/alien-force">Alien Force</a>
 <div class="price_tag meta_tag"><div class="price_value">$1.00</div></div></div>
 <div class="game_text">Shoot aliens</div><div class="game_author"><a href="https://nova32.itch.io">Nova32</a></div></div>
</div>
<div data-game_id="2" class="game_cell"><div class="game_cell_data"><div class="game_title"><a class="title game_link" href="https://x.itch.io/other">Other Game</a></div>
<div class="game_author"><a href="https://x.itch.io">X</a></div></div></div>
</div></body></html>`

func TestParseItchSearch(t *testing.T) {
	c := ParseItchSearch(searchFixture)
	if len(c) != 2 {
		t.Fatalf("want 2 cells, got %d", len(c))
	}
	if c[0].URL != "https://nova32.itch.io/alien-force" || c[0].Title != "Alien Force" ||
		c[0].Author != "Nova32" || c[0].CoverURL != "https://img.itch.zone/c1.png" || c[0].Price != "$1.00" {
		t.Errorf("cell parsed wrong: %+v", c[0])
	}
}

// ---- extraction -----------------------------------------------------------

func makeZip(t *testing.T, path string, files map[string][]byte) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, _ := zw.Create(name)
		w.Write(data)
	}
	zw.Close()
	f.Close()
}

func TestUnpackNestedAndSlip(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "inner.zip")
	makeZip(t, inner, map[string][]byte{"game.a26": []byte("ROM")})
	innerData, _ := os.ReadFile(inner)
	outer := filepath.Join(dir, "outer-no-ext")
	makeZip(t, outer, map[string][]byte{
		"README.txt":       []byte("hi"),
		"builds/inner.zip": innerData,
		"../../escape.bin": []byte("evil"),
	})
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	files, err := Unpack(outer, work)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		if !strings.HasPrefix(f, work) {
			t.Errorf("file escaped work dir: %s", f)
		}
		names = append(names, filepath.Base(f))
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "game.a26") || !strings.Contains(joined, "README.txt") {
		t.Errorf("nested extraction failed: %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.bin")); err == nil {
		t.Error("zip-slip entry was written outside")
	}
}

// ---- full pipeline with a fake itch.io -------------------------------------

type fakeItch struct {
	searches   int
	downloads  int
	pages      map[string][]Upload
	files      map[string][]byte // upload ID -> content
	results    []Candidate
	pageReads  []string
	paidPages  map[string]bool
	profiles   []string
	profileRes map[string][]Candidate
	owned      map[string][]Upload
	more       int
	moreRes    []Candidate
	probes     []string
	pagesInfo  map[string]Candidate
	moved      map[string]string
}

func (f *fakeItch) Search(ctx context.Context, q string) ([]Candidate, error) {
	f.searches++
	return f.results, nil
}
func (f *fakeItch) Uploads(ctx context.Context, page string) ([]Upload, error) {
	f.pageReads = append(f.pageReads, page)
	if f.paidPages[page] {
		return nil, fmt.Errorf("download_url returned empty url (game may be paid or require login)")
	}
	return f.pages[page], nil
}
func (f *fakeItch) Profile(ctx context.Context, slug string) ([]Candidate, string, error) {
	f.profiles = append(f.profiles, slug)
	return f.profileRes[slug], f.moved[slug], nil
}
func (f *fakeItch) SearchMore(ctx context.Context, q []string, devFirst bool, good func(Candidate) bool) ([]Candidate, error) {
	f.more++
	for _, c := range f.moreRes {
		if good != nil && good(c) {
			break
		}
	}
	return f.moreRes, nil
}
func (f *fakeItch) Page(ctx context.Context, u string) (Candidate, bool, error) {
	f.probes = append(f.probes, u)
	c, ok := f.pagesInfo[u]
	return c, ok, nil
}
func (f *fakeItch) OwnedUploads(ctx context.Context, gameID string) ([]Upload, error) {
	ups := f.owned[gameID]
	for i := range ups {
		ups[i].KeyID = "key-" + gameID
	}
	return ups, nil
}
func (f *fakeItch) Download(ctx context.Context, page string, up Upload, dest string) error {
	f.downloads++
	return os.WriteFile(dest, f.files[up.ID], 0o644)
}

func TestPipelineVerifiesOnlyOnHashMatch(t *testing.T) {
	dir := t.TempDir()
	romBody := []byte(strings.Repeat("A", 4096))
	good := append(append([]byte("NES\x1a"), make([]byte, 12)...), romBody...) // headered NES
	wrong := append(append([]byte("NES\x1a"), make([]byte, 12)...), []byte("other")...)

	zipPath := filepath.Join(dir, "rel.zip")
	makeZip(t, zipPath, map[string][]byte{"manual.pdf": []byte("pdf"), "Game-final.nes": good, "Game-v1.nes": wrong})
	zipData, _ := os.ReadFile(zipPath)

	itch := &fakeItch{
		results: []Candidate{
			{URL: "https://dev.itch.io/nes-thing-2", Title: "Nes Thing 2"}, // sequel: filtered out
			{URL: "https://dev.itch.io/nes-thing", Title: "Nes Thing", Author: "dev"},
		},
		pages: map[string][]Upload{
			"https://dev.itch.io/nes-thing": {
				{Name: "NesThing Windows.exe", ID: "w"},
				{Name: "NES Thing demo", ID: "demo"},
				{Name: "nes-thing-release.zip", ID: "zip"},
			},
		},
		files: map[string][]byte{"w": []byte("MZ"), "demo": wrong, "zip": zipData},
	}
	store, _ := OpenStore(dir, 3036)
	game := HubGame{ID: 500, Title: "~Homebrew~ Nes Thing", ConsoleID: 7, ConsoleName: "NES/Famicom",
		Hashes: []string{md5hex(romBody)}, HashesKnown: true}
	store.SetGames([]HubGame{game})
	lib := filepath.Join(dir, "roms", "nes")
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "work"), func(Console) (Destination, error) { return Destination{Dir: lib}, nil })
	p.SearchDelay = 0

	res, err := p.Install(context.Background(), game, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "Nes Thing.nes" {
		t.Errorf("installed as %q", res.Path)
	}
	got, _ := os.ReadFile(res.Path)
	if string(got) != string(good) {
		t.Error("installed the wrong file (must be the one whose RA hash matched)")
	}
	if itch.downloads != 1 {
		t.Errorf("exe must be skipped and the NES zip tried first: %d downloads", itch.downloads)
	}
	if st := store.State(500); st.Status != StatusVerified || st.MD5 != md5hex(romBody) {
		t.Errorf("state not recorded: %+v", st)
	}
	entries, _ := os.ReadDir(lib)
	if len(entries) != 1 {
		t.Errorf("library must contain only the verified ROM, has %d entries", len(entries))
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "work")); len(ents) != 0 {
		t.Errorf("work dir not cleaned: %d", len(ents))
	}

	// Second run: nothing downloaded again.
	before := itch.downloads
	res2, err := p.Install(context.Background(), game, false, nil)
	if err != nil || !res2.Already || itch.downloads != before {
		t.Errorf("re-run must not download again: %+v %v", res2, err)
	}

	// Store survives a reload (resumability).
	store2, err := OpenStore(dir, 3036)
	if err != nil || store2.State(500).Status != StatusVerified {
		t.Errorf("state not persisted: %v", err)
	}
	var raw map[string]any
	data, _ := os.ReadFile(store2.Path())
	if json.Unmarshal(data, &raw) != nil {
		t.Error("state file is not valid JSON")
	}
}

func TestPipelineNoMatchIsNotInstalled(t *testing.T) {
	dir := t.TempDir()
	itch := &fakeItch{
		results: []Candidate{{URL: "https://d.itch.io/alien-force", Title: "Alien Force", Text: "Atari 2600"}},
		pages:   map[string][]Upload{"https://d.itch.io/alien-force": {{Name: "alienforce.a26", ID: "1"}}},
		files:   map[string][]byte{"1": []byte("not the RA dump")},
	}
	store, _ := OpenStore(dir, 3036)
	game := HubGame{ID: 26007, Title: "~Homebrew~ Alien Force", ConsoleID: 25, ConsoleName: "Atari 2600",
		Hashes: []string{"e204a6b63a8e6e1400fde46bbd054722"}, HashesKnown: true}
	store.SetGames([]HubGame{game})
	lib := filepath.Join(dir, "lib")
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "work"), func(Console) (Destination, error) { return Destination{Dir: lib}, nil })
	p.SearchDelay = 0
	_, err := p.Install(context.Background(), game, false, nil)
	if err == nil {
		t.Fatal("a file whose hash does not match must not be accepted, even with an identical title")
	}
	if _, statErr := os.Stat(lib); statErr == nil {
		if ents, _ := os.ReadDir(lib); len(ents) > 0 {
			t.Error("unverified file reached the library")
		}
	}
	if st := store.State(26007); st.Status != StatusNoMatch {
		t.Errorf("want NO_MATCH, got %s", st.Status)
	}
	// Re-run without force: the same upload is not downloaded again.
	n := itch.downloads
	p.Install(context.Background(), game, false, nil)
	if itch.downloads != n {
		t.Error("already-tried upload was downloaded again without force")
	}
}

func TestHubChangeResetsState(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, 1)
	g := HubGame{ID: 1, Hashes: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	store.SetGames([]HubGame{g})
	store.Update(1, func(s *GameState) { s.Status, s.MD5 = StatusVerified, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" })
	store.Update(2, func(s *GameState) { s.Status = StatusNoMatch })
	g.Hashes = []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	added, changed := store.SetGames([]HubGame{g, {ID: 2}})
	if added != 1 || changed != 1 {
		t.Errorf("added=%d changed=%d", added, changed)
	}
	if store.State(1).Status != StatusOutdated {
		t.Error("verified game whose hash vanished from RA must be marked outdated")
	}
}

func TestPaidOnlyWhenOwned(t *testing.T) {
	dir := t.TempDir()
	body := []byte(strings.Repeat("K", 4096))
	game := HubGame{ID: 33061, Title: "~Homebrew~ Kelly Kangaroo", ConsoleID: 25, ConsoleName: "Atari 2600",
		Hashes: []string{md5hex(body)}, HashesKnown: true}
	itch := &fakeItch{
		results: []Candidate{
			{URL: "https://den.itch.io/kelly-kangaroo", Title: "Kelly Kangaroo", Price: "$4.99", Text: "Atari 2600 game"},
			{URL: "https://z.itch.io/kellysourcecode", Title: "Kelly Kangaroo Source Code"},
			{URL: "https://q.itch.io/kelly", Title: "Kelly Kangaroo"}, // paid, price unknown in the listing
		},
		paidPages: map[string]bool{"https://q.itch.io/kelly": true},
		owned:     map[string][]Upload{"777": {{Name: "kelly.a26", ID: "u1"}}},
		files:     map[string][]byte{"u1": body},
	}
	store, _ := OpenStore(dir, 1)
	store.SetGames([]HubGame{game})
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "w"), func(Console) (Destination, error) {
		return Destination{Dir: filepath.Join(dir, "lib", "itchio")}, nil
	})
	p.SearchDelay = 0

	// Not owned: nothing downloaded, the paid page is never even opened.
	_, err := p.Install(context.Background(), game, false, nil)
	if err == nil || itch.downloads != 0 {
		t.Fatalf("paid game must not be downloaded: err=%v downloads=%d", err, itch.downloads)
	}
	for _, r := range itch.pageReads {
		if strings.Contains(r, "den.itch.io") {
			t.Error("the paid page (known price) must not be opened")
		}
	}
	if len(itch.pageReads) != 0 {
		t.Errorf("best match is paid and not owned: no other page may be opened, got %v", itch.pageReads)
	}
	if st := store.State(game.ID); st.Status != StatusPaid {
		t.Errorf("want PAID, got %s (%s)", st.Status, st.Note)
	}

	// Owned: the purchase route is used and the file verifies.
	p.Owned = func(u string) (string, bool) {
		if NormPageURL(u) == "den.itch.io/kelly-kangaroo" {
			return "777", true
		}
		return "", false
	}
	res, err := p.Install(context.Background(), game, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(res.Path)) != "itchio" {
		t.Errorf("must install inside the itchio folder: %s", res.Path)
	}
}

func TestDeveloperSearchFindsRightPunchOut(t *testing.T) {
	c := ConsoleFor(25, "", "")
	right := Candidate{URL: "https://retrodev.itch.io/punch-out-2600", Title: "Punch Out", Text: "A boxing game for the Atari 2600"}
	pool := []Candidate{
		{URL: "https://tylerhan07.itch.io/punch-out", Title: "Punch Out", Browser: true},
		{URL: "https://gabeq.itch.io/punch-out", Title: "Punch-Out", Platforms: []string{"windows"}},
		right,
	}
	ranked := RankFor("~Homebrew~ Punch Out", c, nil, pool, 30, 3)
	if len(ranked) == 0 || ranked[0].URL != right.URL {
		t.Fatalf("the page naming the console must rank first: %+v", ranked)
	}
	hints := DeveloperHintsFromHashNames([]string{
		"Alien Force (NTSC) (Aftermarket) (Unl) (Nova32).a26",
		"Game (USA, Europe) (Rev 1) (v1.02) (2019-05-01) (Pixel Russ).bin",
	})
	if strings.Join(hints, "|") != "Nova32|Pixel Russ" {
		t.Errorf("hash-name hints: %v", hints)
	}
	if s, _ := ScoreFor("~Homebrew~ Ghost Hunt", c, nil, Candidate{Title: "Ghost Hunt", Text: "Atari 2600"}); s < 100 {
		t.Errorf("'ost' must not match inside 'Ghost': %d", s)
	}
	if !IsSameDeveloper([]string{"Pixel Russ"}, Candidate{URL: "https://pixelruss.itch.io/x"}) {
		t.Error("subdomain should match developer")
	}
	if IsPaidPrice("") || IsPaidPrice("Free") || IsPaidPrice("$0.00") || !IsPaidPrice("R$ 5,00") {
		t.Error("price parsing")
	}
}

func TestQuickThenBroadSearch(t *testing.T) {
	dir := t.TempDir()
	body := []byte(strings.Repeat("P", 4096))
	store, _ := OpenStore(dir, 1)
	game := HubGame{ID: 5, Title: "~Homebrew~ Punch Out", ConsoleID: 25, ConsoleName: "Atari 2600",
		Hashes: []string{md5hex(body)}, HashesKnown: true}
	store.SetGames([]HubGame{game})
	store.Update(5, func(s *GameState) { s.DevHints, s.HintsDone = []string{"Retro Dev"}, true })
	itch := &fakeItch{
		results: []Candidate{{URL: "https://x.itch.io/punch-out", Title: "Punch Out", Text: "Atari 2600"}},
		profileRes: map[string][]Candidate{
			"retrodev": {{URL: "https://retrodev.itch.io/punch", Title: "Punch Out"}},
		},
		pages: map[string][]Upload{
			"https://x.itch.io/punch-out":    {{Name: "punch.a26", ID: "wrong"}},
			"https://retrodev.itch.io/punch": {{Name: "punchout.a26", ID: "right"}},
		},
		files: map[string][]byte{"wrong": []byte("other game"), "right": body},
	}
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "w"), func(Console) (Destination, error) {
		return Destination{Dir: filepath.Join(dir, "lib")}, nil
	})
	p.SearchDelay = 0

	// Quick lookup: a single search, no developer pages.
	c, err := p.Resolve(context.Background(), game, false)
	if err != nil || len(itch.profiles) != 0 || itch.searches != 1 {
		t.Fatalf("quick lookup must be one search: %+v %v searches=%d profiles=%v", c, err, itch.searches, itch.profiles)
	}
	// Install: the quick pick fails the hash, the wide search finds the
	// developer's page, and that file verifies.
	res, err := p.Install(context.Background(), game, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate.URL != "https://retrodev.itch.io/punch" {
		t.Errorf("wrong page: %s", res.Candidate.URL)
	}
}

func TestInstallNameUsesESExtensions(t *testing.T) {
	g := HubGame{Title: "~Homebrew~ Rock Shot", ConsoleID: 25}
	c := g.Console()
	if n := InstallName(g, c, "/x/Rock Shot.bin", []string{".a26", ".bin", ".zip"}); n != "Rock Shot.bin" {
		t.Errorf("ES accepts .bin, keep it: %s", n)
	}
	if n := InstallName(g, c, "/x/rom.rom", []string{".a26", ".bin"}); n != "Rock Shot.a26" {
		t.Errorf("unknown ext must become one ES lists: %s", n)
	}
	if n := InstallName(g, c, "/x/rom.rom", []string{".bin"}); n != "Rock Shot.bin" {
		t.Errorf("must pick an ext ES lists: %s", n)
	}
}

func TestWebSearchFindsHiddenGame(t *testing.T) {
	ddg := `<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fjonotastic.itch.io%2Fmasturbrowse&amp;rut=abc">Masturbrowse by Jonotastic</a>
	<a href="https://itch.io/games/tag-adult">x</a> <a href="https://jonotastic.itch.io/masturbrowse/devlog">d</a>`
	urls := ExtractItchGameURLs(ddg)
	if len(urls) != 1 || urls[0] != "https://jonotastic.itch.io/masturbrowse" {
		t.Fatalf("urls: %v", urls)
	}
	page := `<head><meta property="og:title" content="Masturbrowse by Jonotastic"/><meta property="og:description" content="A Game Boy game"/><meta property="og:image" content="https://img.itch.zone/m.png"/>
	<div itemprop="offers"><meta itemprop="price" content="2.00"></div></head>`
	c, ok := ParseGamePage(urls[0], page)
	if !ok || c.Title != "Masturbrowse" || c.Author != "Jonotastic" || c.Price != "$2.00" || c.CoverURL == "" {
		t.Errorf("page parse: %+v", c)
	}
	if s := TitleSlugs("~Homebrew~ Night Watch At Wilford's"); s[0] != "night-watch-at-wilfords" {
		t.Errorf("slug: %v", s)
	}
	if NormTitle("Night Watch At Wilford's") != NormTitle("Night Watch At Wilfords") {
		t.Error("apostrophe must not matter")
	}

	// Pipeline: itch.io search finds nothing; the wide lookup's web search
	// finds the page, which is then read for its real title.
	dir := t.TempDir()
	store, _ := OpenStore(dir, 1)
	game := HubGame{ID: 9, Title: "~Homebrew~ Masturbrowse", ConsoleID: 4, ConsoleName: "Game Boy"}
	store.SetGames([]HubGame{game})
	itch := &fakeItch{
		moreRes:   []Candidate{{URL: urls[0], Title: "Masturbrowse", Source: "web"}},
		pagesInfo: map[string]Candidate{urls[0]: c},
	}
	pl := NewPipeline(store, nil, itch, dir, nil)
	pl.SearchDelay = 0
	if _, err := pl.Resolve(context.Background(), game, false); err != ErrNotFound {
		t.Fatalf("quick lookup should find nothing: %v", err)
	}
	got, err := pl.ResolveBroad(context.Background(), game, nil)
	if err != nil || len(got) == 0 || got[0].URL != urls[0] || got[0].Price != "$2.00" {
		t.Fatalf("wide lookup: %+v %v", got, err)
	}
	if st := store.State(9); st.Status != StatusCandidate || st.SearchVer != SearchVersion {
		t.Errorf("state: %s ver %d", st.Status, st.SearchVer)
	}
}

func TestSearchEngineRedirectLinks(t *testing.T) {
	bing := `<a href="https://www.bing.com/ck/a?!&amp;&amp;p=abc&amp;u=a1aHR0cHM6Ly9nY3VwLml0Y2guaW8vY2FzYW5vdmE&amp;ntb=1">Casanova by GCUP</a>`
	google := `<a href="/url?q=https://gcup.itch.io/casanova&amp;sa=U&amp;ved=x">Casanova</a>`
	for name, page := range map[string]string{"bing": bing, "google": google} {
		u := ExtractItchGameURLs(page)
		if len(u) != 1 || u[0] != "https://gcup.itch.io/casanova" {
			t.Errorf("%s: %v", name, u)
		}
	}
}

func TestYahooRedirect(t *testing.T) {
	y := `<a href="https://r.search.yahoo.com/_ylt=A;_ylu=B/RV=2/RE=1/RO=10/RU=https%3a%2f%2fzeichi.itch.io%2fkotz-phoenix/RK=2/RS=x-">x</a>`
	if u := ExtractItchGameURLs(y); len(u) != 1 || u[0] != "https://zeichi.itch.io/kotz-phoenix" {
		t.Errorf("yahoo: %v", u)
	}
	if s := strings.Join(ProfileSlugs("Zeichi Gameplay Short"), ","); !strings.Contains(s, ",zeichi,") {
		t.Errorf("slugs: %v", s)
	}
}

func TestWebResultJudgedByRealTitle(t *testing.T) {
	// The slug says nothing ("kotz-phoenix"), the page title matches.
	dir := t.TempDir()
	store, _ := OpenStore(dir, 1)
	game := HubGame{ID: 20610, Title: "~Homebrew~ Knights of the Zodiac: The Phoenix Returns", ConsoleID: 6, ConsoleName: "Game Boy Color"}
	store.SetGames([]HubGame{game})
	store.Update(20610, func(s *GameState) { s.DevHints, s.HintsDone = []string{"Zeichi Gameplay Short"}, true })
	u := "https://zeichi.itch.io/kotz-phoenix"
	itch := &fakeItch{
		moreRes:   []Candidate{{URL: "https://other.itch.io/unrelated", Title: "Unrelated", Source: "web"}, {URL: u, Title: "Kotz Phoenix", Source: "web"}},
		pagesInfo: map[string]Candidate{u: {URL: u, Title: "Knights of the Zodiac: The Phoenix Returns", Author: "Zeichi"}},
	}
	pl := NewPipeline(store, nil, itch, dir, nil)
	pl.SearchDelay = 0
	got, err := pl.ResolveBroad(context.Background(), game, nil)
	if err != nil || len(got) == 0 || got[0].URL != u {
		t.Fatalf("got %+v %v (probes %v)", got, err, itch.probes)
	}
}

func TestDifferentNameOnItch(t *testing.T) {
	ra := "~Homebrew~ Knights of the Zodiac: The Phoenix Returns"
	gbc := ConsoleFor(6, "", "")
	page := `<meta property="og:title" content="Saint Seiya - El regreso del Fénix (GameBoy Color) by ZeichiGames">
	<meta property="og:description" content="This project is intended to become a full playable game. Relive the history of the Knights of the Zodiac through more than 10 levels">`
	c, ok := ParseGamePage("https://zeichigames.itch.io/saint-seiya", page)
	if !ok {
		t.Fatal("page not parsed")
	}
	c.Source = "web"
	s, why := ScoreFor(ra, gbc, []string{"Zeichi Gameplay Short"}, c)
	if s < 100 {
		t.Errorf("different itch name, page names the game, GBC, same studio: score %d (%s)", s, why)
	}
	// Without the text, a top result for "title + developer" still counts.
	bare := Candidate{URL: "https://zeichigames.itch.io/saint-seiya", Title: "Saint Seiya", Source: "web", WebRank: 1, DevQuery: true}
	// Only a position in the results is too weak on its own now; with the
	// developer and a word of the title it counts.
	if s, _ := ScoreFor(ra, gbc, nil, bare); s >= 30 {
		t.Errorf("rank alone must not qualify: %d", s)
	}
	bare.Text = "Phoenix platformer"
	if s, why := ScoreFor(ra, gbc, []string{"Zeichi Gameplay Short"}, bare); s < 30 {
		t.Errorf("top dev-query result with developer + keyword: %d (%s)", s, why)
	}
	// But an unrelated page from a title-only search does not.
	other := Candidate{URL: "https://x.itch.io/y", Title: "Other Game", Source: "web", WebRank: 1}
	if s, _ := ScoreFor(ra, gbc, nil, other); s >= 30 {
		t.Errorf("unrelated page accepted: %d", s)
	}
	if !IsSameDeveloper([]string{"Zeichi Gameplay Short"}, Candidate{URL: "https://zeichigames.itch.io/x"}) {
		t.Error("zeichigames ~ Zeichi Gameplay Short")
	}
	if IsSameDeveloper([]string{"Nova32"}, Candidate{URL: "https://novaskull.itch.io/x"}) {
		t.Error("short shared prefix must not match")
	}
}

// The real page (Sep 2026): no og:title, Spanish og:description, the
// English text further down, upload names with the RA title.
const zeichiPage = `<html><head><title>Saint Seiya - El regreso del Fénix (GameBoy Color) by ZeichiGames</title>
<meta name="twitter:title" content="Saint Seiya - El regreso del Fénix (GameBoy Color) by ZeichiGames">
<meta property="og:description" content="Los Caballeros del Zodiaco para GameBoy Color">
<meta property="og:image" content="https://img.itch.zone/x.png"></head><body>
<div class="formatted_description user_formatted"><p>Revive la historia de los Caballeros del zodiaco</p>
<p>Relive the history of the Knights of the Zodiac through more than 10 levels</p></div>
<div class="upload"><div class="upload_name"><strong class="name" title="KOTZ - The Phoenix Returns v1.3.0E ENGLISH (UPDATE B 15-03-2023).gbc">KOTZ - The Phoenix Returns v1.3.0E ENGLISH (UPDATE B 15-03-2023).gbc</strong></div></div>
itch.io</body></html>`

func TestZeichiPage(t *testing.T) {
	c, ok := ParseGamePage("https://zeichigames.itch.io/saint-seiya-el-regreso-del-fnix-gbc", zeichiPage)
	if !ok || c.Title != "Saint Seiya - El regreso del Fénix (GameBoy Color)" || c.Author != "ZeichiGames" {
		t.Fatalf("parse: %+v", c)
	}
	if !strings.Contains(c.Text, "Phoenix Returns") {
		t.Error("upload names not in page text")
	}
	s, why := ScoreFor("~Homebrew~ Knights of the Zodiac: The Phoenix Returns", ConsoleFor(6, "", ""),
		[]string{"Zeichi Gameplay Short"}, c)
	if s < 100 {
		t.Errorf("score %d (%s)", s, why)
	}
	slugs := strings.Join(ProfileSlugs("Zeichi Gameplay Short"), ",")
	if !strings.Contains(slugs, "zeichigames") {
		t.Errorf("profile slugs: %s", slugs)
	}
	ups := PreferNamed([]Upload{
		{Name: "CDZ - O retorno de Fenix v1.2.0P (PORTUGUÊS).gbc"},
		{Name: "LCDZ - Le retour du phénix v1.2.1F (FRANCE 23-11-2022).gbc"},
		{Name: "KOTZ - The Phoenix Returns v1.3.0E ENGLISH (UPDATE B 15-03-2023).gbc"},
	}, nil, []string{"~Homebrew~ Knights of the Zodiac: The Phoenix Returns"})
	if !strings.HasPrefix(ups[0].Name, "KOTZ") {
		t.Errorf("English upload should go first: %v", ups[0].Name)
	}
}

func TestDuplicateKeepsBestJudgement(t *testing.T) {
	u := "https://zeichigames.itch.io/saint-seiya-el-regreso-del-fnix-gbc"
	cell := Candidate{URL: u, Title: "Saint Seiya - El regreso del Fénix (GameBoy Color)", Source: "profile"}
	page, _ := ParseGamePage(u, zeichiPage)
	r := RankForTitles([]string{"~Homebrew~ Knights of the Zodiac: The Phoenix Returns"}, ConsoleFor(6, "", ""),
		[]string{"Zeichi Gameplay Short"}, []Candidate{cell, page}, 30, 3)
	if len(r) != 1 || r[0].Score < 100 {
		t.Fatalf("the opened page's score must win over the earlier cell: %+v", r)
	}
}

func TestLogFalsePositives(t *testing.T) {
	// From the device log: a top web result that is not the game.
	bottle := Candidate{URL: "https://paranoidstudios.itch.io/health-potion-bottle", Title: "Health Potion Bottle",
		Source: "web", WebRank: 1, DevQuery: true}
	if s, why := ScoreFor("~Homebrew~ Go Catch 'Em", ConsoleFor(4, "", ""), []string{"Health Potion Studios"}, bottle); s >= 30 {
		t.Errorf("Health Potion Bottle accepted: %d (%s)", s, why)
	}
	rezal := Candidate{URL: "https://allalonegamez.itch.io/a-20-second", Title: "A 20 second platformer DX",
		Text: "A tiny platformer for the Game Boy Color", Source: "web", WebRank: 1, DevQuery: true}
	if s, why := ScoreFor("~Homebrew~ Rezal", ConsoleFor(6, "", ""), []string{"allalonegamez"}, rezal); s >= 30 {
		t.Errorf("unrelated game of the same developer accepted: %d (%s)", s, why)
	}
	// Silver Falls: the page naming the full title must beat the 3DS ones.
	gb := ConsoleFor(4, "", "")
	dev := []string{"sungrandstudios"}
	mini := Candidate{URL: "https://sungrandstudios.itch.io/silverfallsmini", Title: "Silver Falls Mini (Monsters In North Island)",
		Text: "Silver Falls: Monsters in North Island for Game Boy"}
	ds := Candidate{URL: "https://sungrandstudios.itch.io/silverfalls3ds", Title: "Silver Falls - 3 Down Stars (final updated version) 3DS",
		Text: "the sequel to Silver Falls"}
	r := RankForTitles([]string{"~Homebrew~ Silver Falls: Monsters in North Island"}, gb, dev, []Candidate{ds, mini}, 30, 3)
	if len(r) == 0 || r[0].URL != mini.URL {
		t.Errorf("Silver Falls Mini should rank first: %+v", r)
	}
}

func TestPlatformWordInOwnTitle(t *testing.T) {
	if s, why := ScoreFor("~Homebrew~ Spider Web", ConsoleFor(4, "", ""), nil, Candidate{Title: "Spider Web", Text: "Game Boy"}); s < 100 {
		t.Errorf("'web' is part of the title: %d (%s)", s, why)
	}
}

func TestOnlyConfirmedCandidates(t *testing.T) {
	gen := ConsoleFor(1, "", "")
	// Mega Casanova from the device log: a 1 GB PC game called "Casanova"
	// by someone else must not be accepted.
	pc := Candidate{URL: "https://uowmgames.itch.io/casanova", Title: "Casanova", Text: "A visual novel"}
	r := RankForTitles([]string{"~Homebrew~ Mega Casanova", "Casanova"}, gen, []string{"GCUP"}, []Candidate{pc}, 30, 3)
	if len(r) != 0 {
		t.Errorf("other developer, no console: must be rejected: %+v", r)
	}
	right := Candidate{URL: "https://gcup.itch.io/casanova", Title: "Casanova"}
	r = RankForTitles([]string{"~Homebrew~ Mega Casanova", "Casanova"}, gen, []string{"GCUP"}, []Candidate{pc, right}, 30, 3)
	if len(r) != 1 || r[0].URL != right.URL {
		t.Errorf("same developer must be accepted: %+v", r)
	}
	// Hong Kong 2099 (GBC) from the log: "for gameboy" and the year.
	hk := Candidate{URL: "https://bl4h8l4hbl4h.itch.io/hong-kong-2099-for-gameboy", Title: "Hong Kong 2099 for gameboy"}
	if s, why := ScoreFor("~Homebrew~ Hong Kong 2099", ConsoleFor(6, "", ""), []string{"Bl4h8l4hbl4h"}, hk); s < 100 {
		t.Errorf("Hong Kong 2099: %d (%s)", s, why)
	}
}

func TestRenamedDeveloper(t *testing.T) {
	body := `<p>Health Potion Studios is now Distracted Coder. Check out that account for all future projects.
	<a href="https://distractedcoder.itch.io/">https://distractedcoder.itch.io/</a></p>
	<a href="https://itch.io/">itch.io</a><a href="https://healthpotionstudios.itch.io">x</a>`
	if m := MovedProfile(body, "healthpotionstudios"); m != "distractedcoder" {
		t.Fatalf("moved: %q", m)
	}
	dir := t.TempDir()
	store, _ := OpenStore(dir, 1)
	game := HubGame{ID: 7, Title: "~Homebrew~ Go Catch 'Em", ConsoleID: 4, ConsoleName: "Game Boy"}
	store.SetGames([]HubGame{game})
	store.Update(7, func(s *GameState) { s.DevHints, s.HintsDone = []string{"Health Potion Studios"}, true })
	itch := &fakeItch{
		moved: map[string]string{"healthpotionstudios": "distractedcoder"},
		profileRes: map[string][]Candidate{
			"distractedcoder": {{URL: "https://distractedcoder.itch.io/go-catch-em", Title: "Go Catch 'Em"}},
		},
	}
	pl := NewPipeline(store, nil, itch, dir, nil)
	pl.SearchDelay = 0
	got, err := pl.ResolveBroad(context.Background(), game, nil)
	if err != nil || len(got) == 0 || got[0].URL != "https://distractedcoder.itch.io/go-catch-em" {
		t.Fatalf("renamed developer not followed: %+v %v (profiles %v)", got, err, itch.profiles)
	}
	if !strings.Contains(strings.Join(store.State(7).DevHints, ","), "distractedcoder") {
		t.Error("new account name not remembered")
	}
}

func TestSilverFallsAndPlatformNotes(t *testing.T) {
	pm := ConsoleFor(24, "", "")
	dev := []string{"sungrandstudios"}
	ra := "~Homebrew~ Silver Falls: Monsters in North Island"
	mini := Candidate{URL: "https://sungrandstudios.itch.io/silverfallsmini", Title: "Silver Falls Mini (Monsters In North Island)",
		Text: "The totally sickest game to ever launch on the Pokémon Mini console!"}
	survive := Candidate{URL: "https://sungrandstudios.itch.io/silverfallssurvive", Title: "Silver Falls Survive",
		Text: "The horror of Silver Falls finally arrives on the PSVita"}
	r := RankForTitles([]string{ra}, pm, dev, []Candidate{survive, mini}, 30, 3)
	if len(r) == 0 || r[0].URL != mini.URL {
		t.Fatalf("Pokemon Mini game must win: %+v", r)
	}
	for _, c := range r {
		if c.URL == survive.URL && c.Score >= r[0].Score {
			t.Errorf("PSVita game too high: %d", c.Score)
		}
	}
	if got := StripPlatformNote("Double Symbol for Sega Genesis / Mega Drive / 32X"); got != "Double Symbol" {
		t.Errorf("strip: %q", got)
	}
	if got := StripPlatformNote("Saint Seiya - El regreso del Fénix (GameBoy Color)"); got != "Saint Seiya - El regreso del Fénix" {
		t.Errorf("strip: %q", got)
	}
	if got := StripPlatformNote("Night Watch At Wilford's"); got != "Night Watch At Wilford's" {
		t.Errorf("strip must leave plain titles: %q", got)
	}
	ds := Candidate{URL: "https://gcup.itch.io/double-symbol", Title: "Double Symbol for Sega Genesis / Mega Drive / 32X"}
	if s, why := ScoreFor("~Homebrew~ Double Symbol", ConsoleFor(1, "", ""), []string{"Timur Lyazgiev"}, ds); s < 100 {
		t.Errorf("exact title + console named should be confirmed: %d (%s)", s, why)
	}
	if got := splitNames("László Rajcsányi | WLS"); len(got) != 2 {
		t.Errorf("split: %v", got)
	}
	if NormLoose("Pokémon Mini") != "pokemon mini" {
		t.Error("accents")
	}
}

func TestBrowserEditionAndLinks(t *testing.T) {
	gbc := ConsoleFor(6, "", "")
	dev := []string{"JWG.LLC"}
	ra := "~Homebrew~ Slender: The 8 Pages"
	browser := Candidate{URL: "https://jwgllc.itch.io/slender-the-8-gb-pages-browser", Title: "SLENDER THE 8 GBC PAGES (Browser)"}
	rom := Candidate{URL: "https://jwgllc.itch.io/slender-the-8-gb-pages", Title: "SLENDER THE 8 GB PAGES", Text: "Game Boy Color"}
	r := RankForTitles([]string{ra}, gbc, dev, []Candidate{browser, rom}, 30, 3)
	if len(r) == 0 || r[0].URL != rom.URL {
		t.Fatalf("ROM page must beat the browser edition: %+v", r)
	}
	page := `<meta property="og:title" content="SLENDER THE 8 GBC PAGES (Browser) by JWG.LLC">
	<meta property="og:image" content="https://img.itch.zone/aW1n/original/J3CXjG.png">
	<div class="formatted_description">Want a copy? Download the ROM! <a href="https://jwgllc.itch.io/slender-the-8-gb-pages">here</a></div>`
	c, _ := ParseGamePage("https://jwgllc.itch.io/slender-the-8-gb-pages-browser", page)
	if len(c.Links) != 1 || c.Links[0] != "https://jwgllc.itch.io/slender-the-8-gb-pages" {
		t.Errorf("links: %v", c.Links)
	}
	if c.CoverURL != "https://img.itch.zone/aW1n/original/J3CXjG.png" {
		t.Errorf("cover URL must be kept as is (size variants are signed): %s", c.CoverURL)
	}
	if !IsSameDeveloper([]string{"Davy Willems"}, Candidate{URL: "https://joyrider3774.itch.io/x", Author: "Willems Davy"}) {
		t.Error("name in other order")
	}
}

func TestCreditedDeveloper(t *testing.T) {
	page := Candidate{URL: "https://gcup.itch.io/casanova", Title: "Mega Casanova", Author: "Gamedevelopment Community",
		Text: "The developers: TLT, Tomahomae, 2016. The game consists of two parts"}
	s, why := ScoreFor("~Homebrew~ Mega Casanova", ConsoleFor(1, "", ""), []string{"TLT", "Tomahome"}, page)
	if s < 100 {
		t.Errorf("credited developer on a group page: %d (%s)", s, why)
	}
	if got := splitNames("TLT;, Tomahome"); len(got) != 3 { // "TLT", " ", " Tomahome" (blank dropped later)
		t.Logf("split: %q", got)
	}
}

func TestTitleTags(t *testing.T) {
	tags := TitleTags("~Homebrew~ ~Demo~ Silver Falls: Monsters in North Island")
	if len(tags) != 2 || tags[1] != "Demo" || !IsDemo("~Demo~ Foo") || IsDemo("~Homebrew~ Demolition Man") {
		t.Errorf("tags: %v", tags)
	}
	if CleanTitle("~Homebrew~ ~Demo~ Silver Falls") != "Silver Falls" {
		t.Error("clean title keeps tags")
	}
	if !IsDemo("~Homebrew~ Persona 4 GB (Demo)") {
		t.Error("(Demo) note")
	}
}

func TestDemoGone(t *testing.T) {
	dir := t.TempDir()
	game := HubGame{ID: 21000, Title: "~Homebrew~ ~Demo~ Glory Hunters: Chapter 1", ConsoleID: 4, ConsoleName: "Game Boy",
		Hashes: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, HashesKnown: true}
	itch := &fakeItch{
		results: []Candidate{
			{URL: "https://2think.itch.io/glory-hunters", Title: "Glory Hunters", Author: "2think", Text: "Game Boy"},
			{URL: "https://2think.itch.io/glory-hunters-chapter-1", Title: "Glory Hunters: Chapter 1", Author: "2think", Price: "$5.00 USD", Text: "Game Boy"},
		},
		pages: map[string][]Upload{"https://2think.itch.io/glory-hunters": {{Name: "Free DIY Diorama", ID: "d"}}},
		files: map[string][]byte{"d": []byte("pdf")},
	}
	store, _ := OpenStore(dir, 1)
	store.SetGames([]HubGame{game})
	store.Update(game.ID, func(s *GameState) { s.DevHints, s.HintsDone = []string{"2think"}, true })
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "w"), func(Console) (Destination, error) {
		return Destination{Dir: filepath.Join(dir, "lib")}, nil
	})
	p.SearchDelay = 0
	_, err := p.Install(context.Background(), game, false, nil)
	if !errors.Is(err, ErrDemoGone) {
		t.Fatalf("want demo gone, got %v", err)
	}
	if itch.downloads != 0 {
		t.Errorf("the diorama must not be downloaded: %d", itch.downloads)
	}
}

func TestPreferDemo(t *testing.T) {
	ups := []Upload{{Name: "GoodboyGalaxy.gba"}, {Name: "GoodboyGalaxy_Demo.gba"}}
	if PreferDemo(ups, true)[0].Name != "GoodboyGalaxy_Demo.gba" || PreferDemo(ups, false)[0].Name != "GoodboyGalaxy.gba" {
		t.Error("demo ordering")
	}
}

func TestGoodboyGalaxyPages(t *testing.T) {
	gba := ConsoleFor(5, "", "")
	dev := []string{"Goodboy Galaxy"}
	demoPage := Candidate{URL: "https://goodboygalaxy.itch.io/goodboy-galaxy-demo", Title: "Goodboy Galaxy DEMO",
		Author: "Goodboy Galaxy", Text: "Exploration platform game for Game Boy Advance"}
	fullPage := Candidate{URL: "https://goodboygalaxy.itch.io/goodboy-galaxy-gba", Title: "Goodboy Galaxy (GBA)",
		Author: "Goodboy Galaxy", Price: "$14.99", Text: "Game Boy Advance"}
	r := RankForTitles([]string{"~Homebrew~ ~Demo~ Goodboy Galaxy"}, gba, dev, []Candidate{fullPage, demoPage}, 30, 3)
	if len(r) == 0 || r[0].URL != demoPage.URL {
		t.Errorf("RA demo entry must pick the demo page: %+v", r)
	}
	r = RankForTitles([]string{"~Homebrew~ Goodboy Galaxy"}, gba, dev, []Candidate{demoPage, fullPage}, 30, 3)
	if len(r) == 0 || r[0].URL != fullPage.URL {
		t.Errorf("RA full entry must pick the full page: %+v", r)
	}
	ups := RankUploads(gba, []Upload{{Name: "Goodboy Galaxy DEMO for PC (packed emulator)"}, {Name: "Goodboy Galaxy DEMO for 3DS (cia)"},
		{Name: "Goodboy Galaxy DEMO for GBA (roms only)"}})
	if len(ups) != 1 || !strings.Contains(ups[0].Name, "GBA") {
		t.Errorf("GBA upload first: %+v", ups)
	}
}

func TestVersionGone(t *testing.T) {
	if v := VersionsIn("Goodboy Galaxy - Chapter Zero (v1.0.6).gba"); len(v) != 1 || v[0] != "1.0.6" {
		t.Errorf("ra version: %v", v)
	}
	if v := VersionsIn("goodboy_demo_v1_0_7_en.gba"); len(v) != 1 || v[0] != "1.0.7" {
		t.Errorf("file version: %v", v)
	}
	dir := t.TempDir()
	game := HubGame{ID: 30, Title: "~Homebrew~ ~Demo~ Goodboy Galaxy", ConsoleID: 5, ConsoleName: "Game Boy Advance",
		Hashes: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, HashesKnown: true}
	zipPath := filepath.Join(dir, "demo.zip")
	makeZip(t, zipPath, map[string][]byte{"goodboy_v1.0.7.gba": []byte("new build")})
	zipData, _ := os.ReadFile(zipPath)
	itch := &fakeItch{
		results: []Candidate{{URL: "https://goodboygalaxy.itch.io/goodboy-galaxy-demo", Title: "Goodboy Galaxy DEMO",
			Author: "Goodboy Galaxy", Text: "Game Boy Advance"}},
		pages: map[string][]Upload{"https://goodboygalaxy.itch.io/goodboy-galaxy-demo": {
			{Name: "Goodboy Galaxy DEMO for GBA (roms only)", ID: "z", Version: "1.0.7"}}},
		files: map[string][]byte{"z": zipData},
	}
	store, _ := OpenStore(dir, 1)
	store.SetGames([]HubGame{game})
	store.Update(game.ID, func(s *GameState) {
		s.DevHints, s.HintsDone, s.BroadDone = []string{"Goodboy Galaxy"}, true, true
		s.HashNames = []string{"Goodboy Galaxy - Chapter Zero (v1.0.6).gba"}
	})
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "w"), func(Console) (Destination, error) {
		return Destination{Dir: filepath.Join(dir, "lib")}, nil
	})
	p.SearchDelay = 0
	_, err := p.Install(context.Background(), game, false, nil)
	if !errors.Is(err, ErrVersionGone) || !strings.Contains(err.Error(), "v1.0.6") || !strings.Contains(err.Error(), "v1.0.7") {
		t.Fatalf("want version gone 1.0.6 vs 1.0.7, got %v", err)
	}
	if st := store.State(game.ID); st.Status != StatusVersionGone {
		t.Errorf("status %s", st.Status)
	}
	// Second try: the file was already checked (not downloaded again), the
	// versions itch.io lists are still compared.
	store.Update(game.ID, func(s *GameState) { s.Status = StatusCandidate })
	n := itch.downloads
	if _, err := p.Install(context.Background(), game, false, nil); !errors.Is(err, ErrVersionGone) || itch.downloads != n {
		t.Errorf("second run: %v (downloads %d -> %d)", err, n, itch.downloads)
	}
}

func TestNewHashesReopenGames(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, 1)
	rom := filepath.Join(dir, "game.gba")
	os.WriteFile(rom, []byte("v1.0.7 build"), 0o644)
	newHash := md5hex([]byte("v1.0.7 build"))
	g1 := HubGame{ID: 1, ConsoleID: 5, Hashes: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	g2 := HubGame{ID: 2, ConsoleID: 5, Hashes: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	store.SetGames([]HubGame{g1, g2})
	store.Update(1, func(s *GameState) {
		s.Status, s.RAVersions, s.ItchVersions, s.TriedUploads = StatusVersionGone, []string{"1.0.6"}, []string{"1.0.7"}, []string{"x"}
	})
	store.Update(2, func(s *GameState) { s.Status, s.InstalledPath = StatusUnverified, rom })
	g1.Hashes = append(g1.Hashes, newHash)
	g2.Hashes = append(g2.Hashes, newHash)
	store.SetGames([]HubGame{g1, g2})
	if st := store.State(1); st.Status != StatusNew || len(st.TriedUploads) != 0 {
		t.Errorf("version-gone game must be re-opened: %+v", st)
	}
	if st := store.State(2); st.Status != StatusVerified {
		t.Errorf("unverified install now matching must become verified: %s", st.Status)
	}
}

func TestOutdatedInstall(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, 1)
	g := HubGame{ID: 3, ConsoleID: 5, Hashes: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	store.SetGames([]HubGame{g})
	store.Update(3, func(s *GameState) { s.Status, s.MD5 = StatusVerified, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" })
	g.Hashes = []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	store.SetGames([]HubGame{g})
	if st := store.State(3); st.Status != StatusOutdated {
		t.Errorf("installed file RA no longer accepts: want OUTDATED, got %s", st.Status)
	}
}

func TestGameStats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Released": "2026-08-21", "Genre": "Platformer", "NumDistinctPlayers": 1234, "Achievements": {"1": {"NumAwarded": 900, "DateCreated": "2023-01-02 03:04:05"}, "2": {"NumAwarded": 100, "DateCreated": "2024-05-06 07:08:09", "DateModified": "2026-09-10 11:00:00"}}}`)
	}))
	defer srv.Close()
	RABase = srv.URL
	defer func() { RABase = "https://retroachievements.org" }()
	ra := NewRAClient(Credentials{User: "u", Key: "k"}, nil)
	p, u, genre, added, released, updated, err := ra.FetchGameStats(context.Background(), 1)
	if err != nil || p != 1234 || u != 1000 || genre != "Platformer" || added.Year() != 2024 ||
		released.Format("2006-01-02") != "2026-08-21" || updated.Format("2006-01-02") != "2026-09-10" {
		t.Errorf("stats %d %d %v", p, u, err)
	}
	store, _ := OpenStore(t.TempDir(), 1)
	store.SetGames([]HubGame{{ID: 1, Players: 1234, Unlocks: 1000}})
	store.SetGames([]HubGame{{ID: 1}})
	if g, _ := store.Game(1); g.Players != 1234 {
		t.Error("stats lost on hub refresh")
	}
}

func TestUserProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Count":2,"Total":2,"Results":[
			{"GameID":10,"MaxPossible":6,"NumAwarded":6,"NumAwardedHardcore":6,"HighestAwardKind":"mastered"},
			{"GameID":11,"MaxPossible":10,"NumAwarded":3,"NumAwardedHardcore":1,"HighestAwardKind":null}]}`)
	}))
	defer srv.Close()
	RABase = srv.URL
	defer func() { RABase = "https://retroachievements.org" }()
	ra := NewRAClient(Credentials{User: "u", Key: "k"}, nil)
	ra.Delay = 0
	got, err := ra.FetchUserProgress(context.Background())
	if err != nil || !got[10].Mastered() || got[11].Unlocked != 3 || got[11].Total != 10 {
		t.Fatalf("progress %+v %v", got, err)
	}
	store, _ := OpenStore(t.TempDir(), 1)
	store.SetGames([]HubGame{{ID: 10}, {ID: 11}})
	if n := store.SetProgress(got); n != 2 || !store.State(10).Progress.Mastered() {
		t.Errorf("stored %d %+v", n, store.State(10).Progress)
	}
}

// A file that matches RetroAchievements but cannot be copied into the ROM
// folder (card full, read-only) is still the right file. It used to be
// marked as tried, so every later attempt answered "all files already
// checked" and never downloaded it again.
func TestPipelineInstallFailureDoesNotBlacklistTheFile(t *testing.T) {
	dir := t.TempDir()
	rom := []byte("the RA dump of alien force")
	itch := &fakeItch{
		results: []Candidate{{URL: "https://d.itch.io/alien-force", Title: "Alien Force", Text: "Atari 2600"}},
		pages:   map[string][]Upload{"https://d.itch.io/alien-force": {{Name: "alienforce.a26", ID: "1"}}},
		files:   map[string][]byte{"1": rom},
	}
	store, _ := OpenStore(dir, 3036)
	game := HubGame{ID: 26007, Title: "~Homebrew~ Alien Force", ConsoleID: 25, ConsoleName: "Atari 2600",
		Hashes: []string{md5hex(rom)}, HashesKnown: true}
	store.SetGames([]HubGame{game})

	// The ROM folder's path is taken by a file, so it cannot be created.
	lib := filepath.Join(dir, "lib")
	os.WriteFile(lib, []byte("in the way"), 0o644)
	p := NewPipeline(store, nil, itch, filepath.Join(dir, "work"), func(Console) (Destination, error) { return Destination{Dir: lib}, nil })
	p.SearchDelay = 0

	_, err := p.Install(context.Background(), game, false, nil)
	if !errors.Is(err, ErrInstallFailed) {
		t.Fatalf("want ErrInstallFailed, got %v", err)
	}
	if tried := store.State(26007).TriedUploads; len(tried) != 0 {
		t.Errorf("a verified file was marked as tried: %v", tried)
	}

	// Once the card is fixed, the same file installs without force.
	os.Remove(lib)
	res, err := p.Install(context.Background(), game, false, nil)
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if got, _ := os.ReadFile(res.Path); string(got) != string(rom) {
		t.Error("installed the wrong bytes")
	}
}

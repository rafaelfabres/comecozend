package rahub

import (
	"bytes"
	"compress/flate"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isoEntry is a file or directory placed in a synthetic ISO-9660 image.
type isoEntry struct {
	name string
	lba  int
	data []byte
	dir  bool
	size int
}

func dirRecord(e isoEntry) []byte {
	name := e.name
	if !e.dir && name != "\x00" && name != "\x01" {
		name += ";1"
	}
	rec := make([]byte, 33+len(name)+(len(name)+1)%2)
	rec[0] = byte(len(rec))
	binary.LittleEndian.PutUint32(rec[2:], uint32(e.lba))
	size := e.size
	if size == 0 {
		size = len(e.data)
	}
	binary.LittleEndian.PutUint32(rec[10:], uint32(size))
	if e.dir {
		rec[25] = 2
	}
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}

// buildISO lays out sectors (2048 bytes) and returns the image.
func buildISO(sectors map[int][]byte, total int) []byte {
	img := make([]byte, total*2048)
	for lba, data := range sectors {
		copy(img[lba*2048:], data)
	}
	return img
}

func pvd(rootLBA, rootSize int) []byte {
	b := make([]byte, 2048)
	b[0] = 1
	copy(b[1:], "CD001")
	root := dirRecord(isoEntry{name: "\x00", lba: rootLBA, dir: true, size: rootSize})
	copy(b[156:], root)
	return b
}

func dirSector(entries ...isoEntry) []byte {
	var b []byte
	b = append(b, dirRecord(isoEntry{name: "\x00", dir: true, size: 2048})...)
	b = append(b, dirRecord(isoEntry{name: "\x01", dir: true, size: 2048})...)
	for _, e := range entries {
		b = append(b, dirRecord(e)...)
	}
	out := make([]byte, 2048)
	copy(out, b)
	return out
}

func toRaw(iso []byte) []byte {
	var raw []byte
	for off := 0; off < len(iso); off += 2048 {
		sec := make([]byte, 2352)
		copy(sec, []byte{0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0})
		sec[15] = 2 // mode 2 form 1: data at 24
		copy(sec[24:], iso[off:off+2048])
		raw = append(raw, sec...)
	}
	return raw
}

func TestPSXHash(t *testing.T) {
	exe := make([]byte, 4096)
	copy(exe, "PS-X EXE")
	binary.LittleEndian.PutUint32(exe[28:], 1500) // text size
	for i := 2048; i < len(exe); i++ {
		exe[i] = byte(i)
	}
	cnf := []byte("BOOT = cdrom:\\SLUS_000.01;1\r\nTCB = 4\r\n")
	sectors := map[int][]byte{
		16: pvd(20, 2048),
		20: dirSector(isoEntry{name: "SYSTEM.CNF", lba: 21, data: cnf}, isoEntry{name: "SLUS_000.01", lba: 22, data: exe}),
		21: cnf,
		22: exe[:2048],
		23: exe[2048:],
	}
	iso := buildISO(sectors, 30)
	h := md5.New()
	h.Write([]byte("SLUS_000.01"))
	h.Write(exe[:1500+2048])
	want := hex.EncodeToString(h.Sum(nil))

	dir := t.TempDir()
	isoPath := filepath.Join(dir, "game.iso")
	os.WriteFile(isoPath, iso, 0o644)
	if got, err := HashPSXImage(isoPath); err != nil || got != want {
		t.Fatalf("iso: %s %v want %s", got, err, want)
	}
	// Same disc as a raw mode-2 .bin behind a .cue.
	binPath := filepath.Join(dir, "Buzzy Bee (Track 1).bin")
	os.WriteFile(binPath, toRaw(iso), 0o644)
	cuePath := filepath.Join(dir, "Buzzy Bee.cue")
	os.WriteFile(cuePath, []byte("FILE \"Buzzy Bee (Track 1).bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o644)
	c := ConsoleFor(12, "", "")
	if got, err := HashFile(c, cuePath); err != nil || got != want {
		t.Fatalf("cue/bin: %s %v", got, err)
	}
	// Installing the cue brings its track along.
	lib := filepath.Join(dir, "lib")
	path, err := placeCueSet(cuePath, lib)
	if err != nil || filepath.Base(path) != "Buzzy Bee.cue" {
		t.Fatalf("cue set: %s %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(lib, "Buzzy Bee (Track 1).bin")); err != nil {
		t.Error("track not copied")
	}
	if _, err := HashFile(c, filepath.Join(dir, "x.chd")); err == nil || !strings.Contains(err.Error(), "compressed") {
		t.Errorf("chd must be reported as not verifiable: %v", err)
	}
}

func TestPSPHash(t *testing.T) {
	sfo := []byte("PSF\x00param-data")
	eboot := []byte(strings.Repeat("E", 3000))
	sectors := map[int][]byte{
		16: pvd(20, 2048),
		20: dirSector(isoEntry{name: "PSP_GAME", lba: 21, dir: true, size: 2048}),
		21: dirSector(isoEntry{name: "PARAM.SFO", lba: 23, data: sfo}, isoEntry{name: "SYSDIR", lba: 22, dir: true, size: 2048}),
		22: dirSector(isoEntry{name: "EBOOT.BIN", lba: 24, data: eboot}),
		23: sfo,
		24: eboot[:2048],
		25: eboot[2048:],
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "g.iso")
	os.WriteFile(p, buildISO(sectors, 30), 0o644)
	h := md5.New()
	h.Write(sfo)
	h.Write(eboot)
	if got, err := HashPSPImage(p); err != nil || got != hex.EncodeToString(h.Sum(nil)) {
		t.Fatalf("psp: %s %v", got, err)
	}
}

func TestAltTitles(t *testing.T) {
	got := TitlesFromHashNames([]string{"Casanova (World) (Aftermarket) (Unl).md", "Casanova (v1.1).md"})
	if len(got) != 1 || got[0] != "Casanova" {
		t.Errorf("hash-name titles: %v", got)
	}
	if s := ShorterTitles("Mega Casanova"); len(s) != 1 || s[0] != "Casanova" {
		t.Errorf("shorter: %v", s)
	}
	r := RankForTitles([]string{"~Homebrew~ Mega Casanova", "Casanova"}, ConsoleFor(1, "", ""), nil,
		[]Candidate{{URL: "https://gcup.itch.io/casanova", Title: "Casanova", Text: "A Mega Drive game"}}, 30, 1)
	if len(r) != 1 || r[0].Score < 100 {
		t.Errorf("alt title should count as exact: %+v", r)
	}
}

func writeCSO(t *testing.T, iso []byte, path string) {
	const bs = 2048
	n := (len(iso) + bs - 1) / bs
	var blocks [][]byte
	var plain []bool
	for i := 0; i < n; i++ {
		chunk := iso[i*bs : min(len(iso), (i+1)*bs)]
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, flate.BestCompression)
		w.Write(chunk)
		w.Close()
		if i%3 == 0 { // mix stored and compressed blocks
			blocks, plain = append(blocks, chunk), append(plain, true)
		} else {
			blocks, plain = append(blocks, buf.Bytes()), append(plain, false)
		}
	}
	head := make([]byte, 24)
	copy(head, "CISO")
	binary.LittleEndian.PutUint32(head[4:], 24)
	binary.LittleEndian.PutUint64(head[8:], uint64(len(iso)))
	binary.LittleEndian.PutUint32(head[16:], bs)
	head[20] = 1
	off := 24 + (n+1)*4
	idx := make([]byte, (n+1)*4)
	var data []byte
	for i, b := range blocks {
		v := uint32(off + len(data))
		if plain[i] {
			v |= 0x80000000
		}
		binary.LittleEndian.PutUint32(idx[i*4:], v)
		data = append(data, b...)
	}
	binary.LittleEndian.PutUint32(idx[n*4:], uint32(off+len(data)))
	os.WriteFile(path, append(append(head, idx...), data...), 0o644)
}

func TestPSPHashFromCSO(t *testing.T) {
	sfo := []byte("PSF\x00param-data")
	eboot := []byte(strings.Repeat("E", 3000))
	sectors := map[int][]byte{
		16: pvd(20, 2048),
		20: dirSector(isoEntry{name: "PSP_GAME", lba: 21, dir: true, size: 2048}),
		21: dirSector(isoEntry{name: "PARAM.SFO", lba: 23, data: sfo}, isoEntry{name: "SYSDIR", lba: 22, dir: true, size: 2048}),
		22: dirSector(isoEntry{name: "EBOOT.BIN", lba: 24, data: eboot}),
		23: sfo, 24: eboot[:2048], 25: eboot[2048:],
	}
	iso := buildISO(sectors, 30)
	dir := t.TempDir()
	isoPath, csoPath := filepath.Join(dir, "g.iso"), filepath.Join(dir, "Fruit Ninja.cso")
	os.WriteFile(isoPath, iso, 0o644)
	writeCSO(t, iso, csoPath)
	want, err := HashPSPImage(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(ConsoleFor(41, "", ""), csoPath)
	if err != nil || got != want {
		t.Fatalf("cso hash %s %v, want %s", got, err, want)
	}
}

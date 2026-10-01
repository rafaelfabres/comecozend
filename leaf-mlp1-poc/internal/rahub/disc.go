package rahub

// RetroAchievements hashes for CD/DVD systems, following rcheevos
// (rc_hash_psx / rc_hash_psp):
//
//	PlayStation: read SYSTEM.CNF from the disc's ISO-9660 file system, take
//	             the BOOT executable, hash  name + executable
//	             (executable size = PS-X EXE header size + 2048)
//	PSP:         hash PSP_GAME/PARAM.SFO followed by PSP_GAME/SYSDIR/EBOOT.BIN
//
// Images are read as .iso (2048-byte sectors) or raw .bin (2352-byte sectors,
// mode 1 or mode 2), the latter usually through a .cue sheet. Compressed
// formats (CHD, PBP, CSO) cannot be read here.

import (
	"bufio"
	"bytes"
	"compress/flate"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxDiscExe = 64 << 20

// discImage reads 2048-byte data sectors from a .iso, raw .bin or .cso.
type discImage struct {
	f          readerAtCloser
	sectorSize int64
	dataOffset int64
}

type readerAtCloser interface {
	io.ReaderAt
	io.Closer
}

func openDisc(path string) (*discImage, error) {
	if strings.EqualFold(filepath.Ext(path), ".cso") {
		c, err := openCSO(path)
		if err != nil {
			return nil, err
		}
		return &discImage{f: c, sectorSize: 2048}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d := &discImage{f: f, sectorSize: 2048}
	// Raw sectors start with the 12-byte sync pattern; the mode byte at
	// offset 15 says where the 2048 data bytes sit.
	sync := []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}
	head := make([]byte, 16)
	if info.Size()%2352 == 0 {
		if _, err := f.ReadAt(head, 16*2352); err == nil && bytes.Equal(head[:12], sync) {
			d.sectorSize = 2352
			d.dataOffset = 16
			if head[15] == 2 {
				d.dataOffset = 24
			}
		}
	}
	return d, nil
}

func (d *discImage) Close() error { return d.f.Close() }

func (d *discImage) sector(lba int64) ([]byte, error) {
	buf := make([]byte, 2048)
	n, err := d.f.ReadAt(buf, lba*d.sectorSize+d.dataOffset)
	if n == 2048 {
		return buf, nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

// readFile reads size bytes starting at a sector (data portion only).
func (d *discImage) readFile(lba int64, size int) ([]byte, error) {
	out := make([]byte, 0, size)
	for len(out) < size {
		s, err := d.sector(lba)
		if err != nil {
			return nil, err
		}
		need := size - len(out)
		if need > len(s) {
			need = len(s)
		}
		out = append(out, s[:need]...)
		lba++
	}
	return out, nil
}

// findFile walks the ISO-9660 directories for a path such as
// "PSP_GAME\SYSDIR\EBOOT.BIN" (case-insensitive, ";1" ignored).
func (d *discImage) findFile(path string) (lba int64, size int, err error) {
	pvd, err := d.sector(16)
	if err != nil {
		return 0, 0, err
	}
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		return 0, 0, fmt.Errorf("not an ISO-9660 disc")
	}
	root := pvd[156:]
	dirLBA := int64(binary.LittleEndian.Uint32(root[2:6]))
	dirSize := int(binary.LittleEndian.Uint32(root[10:14]))

	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' })
	for i, part := range parts {
		last := i == len(parts)-1
		found := false
		data, err := d.readFile(dirLBA, dirSize)
		if err != nil {
			return 0, 0, err
		}
		for off := 0; off < len(data); {
			recLen := int(data[off])
			if recLen == 0 {
				off = (off/2048 + 1) * 2048 // records never cross a sector
				continue
			}
			if off+33 > len(data) || off+recLen > len(data) {
				break
			}
			rec := data[off : off+recLen]
			nameLen := int(rec[32])
			if 33+nameLen <= len(rec) {
				name := string(rec[33 : 33+nameLen])
				if j := strings.IndexByte(name, ';'); j >= 0 {
					name = name[:j]
				}
				name = strings.TrimSuffix(name, ".")
				if strings.EqualFold(name, part) {
					isDir := rec[25]&2 != 0
					if last != isDir {
						dirLBA = int64(binary.LittleEndian.Uint32(rec[2:6]))
						dirSize = int(binary.LittleEndian.Uint32(rec[10:14]))
						found = true
						break
					}
				}
			}
			off += recLen
		}
		if !found {
			return 0, 0, fmt.Errorf("%s not found on disc", path)
		}
	}
	return dirLBA, dirSize, nil
}

// HashPSXImage computes the RetroAchievements hash of a PlayStation disc
// image (.iso or the data track .bin).
func HashPSXImage(path string) (string, error) {
	d, err := openDisc(path)
	if err != nil {
		return "", err
	}
	defer d.Close()

	exeName := ""
	var lba int64
	var size int
	if cnfLBA, cnfSize, err := d.findFile("SYSTEM.CNF"); err == nil {
		cnf, err := d.readFile(cnfLBA, cnfSize)
		if err != nil {
			return "", err
		}
		exeName = bootExecutable(string(cnf))
		if exeName == "" {
			return "", fmt.Errorf("SYSTEM.CNF has no BOOT line")
		}
		if lba, size, err = d.findFile(exeName); err != nil {
			return "", err
		}
	} else {
		exeName = "PSX.EXE"
		if lba, size, err = d.findFile(exeName); err != nil {
			return "", fmt.Errorf("no SYSTEM.CNF or PSX.EXE: not a PlayStation disc")
		}
	}
	first, err := d.sector(lba)
	if err != nil {
		return "", err
	}
	if bytes.HasPrefix(first, []byte("PS-X EXE")) {
		// The header's size field excludes the 2048-byte header itself.
		size = int(binary.LittleEndian.Uint32(first[28:32])) + 2048
	}
	if size <= 0 || size > maxDiscExe {
		return "", fmt.Errorf("executable size %d out of range", size)
	}
	exe, err := d.readFile(lba, size)
	if err != nil {
		return "", err
	}
	h := md5.New()
	h.Write([]byte(exeName))
	h.Write(exe)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// bootExecutable extracts "SLUS_000.00" from "BOOT = cdrom:\SLUS_000.00;1".
func bootExecutable(cnf string) string {
	i := strings.Index(cnf, "BOOT")
	for i >= 0 {
		rest := strings.TrimLeft(cnf[i+4:], " \t")
		if strings.HasPrefix(rest, "=") {
			rest = strings.TrimLeft(rest[1:], " \t")
			rest = strings.TrimPrefix(rest, "cdrom:")
			rest = strings.TrimPrefix(rest, "\\")
			end := strings.IndexAny(rest, " \t\r\n;")
			if end < 0 {
				end = len(rest)
			}
			return rest[:end]
		}
		next := strings.Index(cnf[i+4:], "BOOT")
		if next < 0 {
			break
		}
		i += 4 + next
	}
	return ""
}

// HashPSPImage computes the RetroAchievements hash of a PSP .iso.
func HashPSPImage(path string) (string, error) {
	d, err := openDisc(path)
	if err != nil {
		return "", err
	}
	defer d.Close()
	h := md5.New()
	for _, name := range []string{"PSP_GAME\\PARAM.SFO", "PSP_GAME\\SYSDIR\\EBOOT.BIN"} {
		lba, size, err := d.findFile(name)
		if err != nil {
			return "", err
		}
		if size > maxDiscExe {
			return "", fmt.Errorf("%s too large", name)
		}
		data, err := d.readFile(lba, size)
		if err != nil {
			return "", err
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CueFiles returns the files a .cue sheet references, in order, as full
// paths next to the cue.
func CueFiles(cuePath string) ([]string, error) {
	f, err := os.Open(cuePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(strings.ToUpper(line), "FILE ") {
			continue
		}
		rest := strings.TrimSpace(line[5:])
		name := ""
		if strings.HasPrefix(rest, "\"") {
			if end := strings.Index(rest[1:], "\""); end >= 0 {
				name = rest[1 : 1+end]
			}
		} else if fields := strings.Fields(rest); len(fields) > 0 {
			name = fields[0]
		}
		if name != "" {
			out = append(out, filepath.Join(filepath.Dir(cuePath), filepath.FromSlash(strings.ReplaceAll(name, "\\", "/"))))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("cue sheet lists no files")
	}
	return out, sc.Err()
}

// hashDisc hashes a disc for a console from a .cue (first track), .iso or
// raw .bin.
func hashDisc(c Console, path string) (string, error) {
	img := path
	if strings.EqualFold(filepath.Ext(path), ".cue") {
		files, err := CueFiles(path)
		if err != nil {
			return "", err
		}
		img = files[0]
	}
	switch strings.ToLower(filepath.Ext(img)) {
	case ".chd", ".pbp", ".7z", ".zip":
		return "", fmt.Errorf("%w (%s is compressed)", ErrNotVerifiable, filepath.Ext(img))
	}
	switch c.Method {
	case HashPSX:
		return HashPSXImage(img)
	case HashPSP:
		return HashPSPImage(img)
	}
	return "", ErrNotVerifiable
}

// ---- CSO (compressed ISO, used for PSP games) --------------------------------
//
// Header: "CISO", header size (u32), total bytes (u64), block size (u32),
// version (u8), index alignment (u8), 2 reserved bytes. Then one u32 per
// block (+1): bit 31 set = block stored uncompressed; the rest, shifted by
// the alignment, is the block's offset. Compressed blocks are raw deflate.

type csoFile struct {
	f         *os.File
	total     int64
	blockSize int64
	align     uint
	index     []uint32
	cacheN    int64
	cache     []byte
}

func openCSO(path string) (*csoFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 24)
	if _, err := io.ReadFull(f, head); err != nil || string(head[:4]) != "CISO" {
		f.Close()
		return nil, fmt.Errorf("not a CSO file")
	}
	c := &csoFile{f: f, cacheN: -1,
		total:     int64(binary.LittleEndian.Uint64(head[8:16])),
		blockSize: int64(binary.LittleEndian.Uint32(head[16:20])),
		align:     uint(head[21]),
	}
	if c.blockSize <= 0 || c.blockSize > 1<<20 || c.total <= 0 {
		f.Close()
		return nil, fmt.Errorf("bad CSO header")
	}
	n := (c.total+c.blockSize-1)/c.blockSize + 1
	raw := make([]byte, n*4)
	if _, err := io.ReadFull(f, raw); err != nil {
		f.Close()
		return nil, fmt.Errorf("read CSO index: %w", err)
	}
	c.index = make([]uint32, n)
	for i := range c.index {
		c.index[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	return c, nil
}

func (c *csoFile) Close() error { return c.f.Close() }

func (c *csoFile) block(n int64) ([]byte, error) {
	if n == c.cacheN {
		return c.cache, nil
	}
	if n < 0 || n+1 >= int64(len(c.index)) {
		return nil, io.EOF
	}
	plain := c.index[n]&0x80000000 != 0
	start := int64(c.index[n]&0x7fffffff) << c.align
	end := int64(c.index[n+1]&0x7fffffff) << c.align
	if end < start {
		return nil, fmt.Errorf("bad CSO index")
	}
	src := make([]byte, end-start)
	if _, err := c.f.ReadAt(src, start); err != nil && err != io.EOF {
		return nil, err
	}
	var out []byte
	if plain {
		out = src
		if int64(len(out)) > c.blockSize {
			out = out[:c.blockSize]
		}
	} else {
		r := flate.NewReader(bytes.NewReader(src))
		out = make([]byte, c.blockSize)
		k, err := io.ReadFull(r, out)
		r.Close()
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("inflate CSO block %d: %w", n, err)
		}
		out = out[:k]
	}
	c.cacheN, c.cache = n, out
	return out, nil
}

func (c *csoFile) ReadAt(p []byte, off int64) (int, error) {
	read := 0
	for read < len(p) {
		pos := off + int64(read)
		if pos >= c.total {
			return read, io.EOF
		}
		b, err := c.block(pos / c.blockSize)
		if err != nil {
			return read, err
		}
		inBlock := pos % c.blockSize
		if inBlock >= int64(len(b)) {
			return read, io.ErrUnexpectedEOF
		}
		read += copy(p[read:], b[inBlock:])
	}
	return read, nil
}

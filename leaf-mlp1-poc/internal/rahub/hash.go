package rahub

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxHashBytes caps what is read into memory to hash one file. Cartridge
// homebrew is kilobytes to a few megabytes; the biggest supported format
// (N64/NDS) stays well under this.
const maxHashBytes = 256 << 20

// ErrNotVerifiable is returned for consoles whose RA hash needs disc-image
// parsing (PlayStation, Sega CD, ...), which this app does not implement.
var ErrNotVerifiable = errors.New("RetroAchievements hash for this system needs disc parsing (not supported)")

// HashFile computes the RetroAchievements hash of a file for a console.
func HashFile(c Console, path string) (string, error) {
	if c.Method == HashDisc {
		return "", ErrNotVerifiable
	}
	if c.Method == HashArcade {
		return HashBytes(c, nil, filepath.Base(path))
	}
	if c.Method == HashPSX || c.Method == HashPSP {
		return hashDisc(c, path) // reads only what it needs, any size
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxHashBytes {
		return "", fmt.Errorf("%s is too large to hash (%d bytes)", filepath.Base(path), info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return HashBytes(c, data, filepath.Base(path))
}

// HashBytes is HashFile for data already in memory. name is only used by
// the arcade method, which hashes the file name.
func HashBytes(c Console, data []byte, name string) (string, error) {
	sum := func(b ...[]byte) string {
		h := md5.New()
		for _, part := range b {
			h.Write(part)
		}
		return hex.EncodeToString(h.Sum(nil))
	}

	switch c.Method {
	case HashDisc, HashPSX, HashPSP:
		return "", ErrNotVerifiable

	case HashArcade:
		base := filepath.Base(name)
		return sum([]byte(strings.TrimSuffix(base, filepath.Ext(base)))), nil

	case HashNES:
		if len(data) >= 16 && (bytes.HasPrefix(data, []byte("NES\x1a")) || bytes.HasPrefix(data, []byte("FDS\x1a"))) {
			return sum(data[16:]), nil
		}
		return sum(data), nil

	case HashSNES:
		if len(data)%8192 == 512 {
			return sum(data[512:]), nil
		}
		return sum(data), nil

	case HashPCE:
		if len(data)%1024 == 512 {
			return sum(data[512:]), nil
		}
		return sum(data), nil

	case Hash7800:
		if len(data) >= 128 && bytes.Equal(data[1:10], []byte("ATARI7800")) {
			return sum(data[128:]), nil
		}
		return sum(data), nil

	case HashLynx:
		if len(data) >= 64 && bytes.HasPrefix(data, []byte("LYNX\x00")) {
			return sum(data[64:]), nil
		}
		return sum(data), nil

	case HashN64:
		return sum(n64BigEndian(data)), nil

	case HashNDS:
		return hashNDS(data)

	case HashArduboy:
		return sum(bytes.ReplaceAll(data, []byte("\r"), nil)), nil

	default:
		return sum(data), nil
	}
}

// n64BigEndian converts .v64 (byte-swapped) and .n64 (little-endian) dumps
// to the .z64 order rcheevos hashes.
func n64BigEndian(data []byte) []byte {
	if len(data) < 4 {
		return data
	}
	switch data[0] {
	case 0x37: // .v64: swap every pair
		out := make([]byte, len(data))
		for i := 0; i+1 < len(data); i += 2 {
			out[i], out[i+1] = data[i+1], data[i]
		}
		return out
	case 0x40: // .n64: reverse every 32-bit word
		out := make([]byte, len(data))
		for i := 0; i+3 < len(data); i += 4 {
			out[i], out[i+1], out[i+2], out[i+3] = data[i+3], data[i+2], data[i+1], data[i]
		}
		return out
	}
	return data
}

// hashNDS follows rcheevos' rc_hash_nintendo_ds: the 0x160-byte header, the
// ARM9 and ARM7 binaries, then 0xA00 bytes of icon data (zero-padded).
func hashNDS(data []byte) (string, error) {
	offset := 0
	// SuperCard header in front of the real ROM header: skip it.
	if len(data) >= 0x200+0x160 && data[0] == 0x2E && data[1] == 0x00 && data[2] == 0x00 && data[3] == 0xEA &&
		data[0xB0] == 0x44 && data[0xB1] == 0x46 && data[0xB2] == 0x96 && data[0xB3] == 0x00 {
		offset = 0x200
	}
	if len(data) < offset+0x160 {
		return "", fmt.Errorf("file too small for an NDS header")
	}
	header := data[offset : offset+0x160]
	u32 := func(at int) int { return int(binary.LittleEndian.Uint32(header[at : at+4])) }
	arm9Addr, arm9Size := u32(0x20), u32(0x2C)
	arm7Addr, arm7Size := u32(0x30), u32(0x3C)
	iconAddr := u32(0x68)
	if arm9Size+arm7Size > 16<<20 {
		return "", fmt.Errorf("NDS code sections too large")
	}
	slice := func(addr, size int) []byte {
		start := offset + addr
		if start < 0 || start >= len(data) {
			return nil
		}
		end := start + size
		if end > len(data) {
			end = len(data)
		}
		return data[start:end]
	}
	h := md5.New()
	h.Write(header)
	h.Write(slice(arm9Addr, arm9Size))
	h.Write(slice(arm7Addr, arm7Size))
	icon := make([]byte, 0xA00)
	copy(icon, slice(iconAddr, 0xA00))
	h.Write(icon)
	return hex.EncodeToString(h.Sum(nil)), nil
}

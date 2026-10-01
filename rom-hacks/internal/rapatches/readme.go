package rapatches

import (
	"regexp"
	"strconv"
	"strings"
)

// BaseROM is the dump a patch expects to be applied to, as stated by the
// readme.txt that RAPatches ships inside every archive.
//
// Treat it as a hint, not as truth. Two archives in a thirty-file sample
// were wrong: 28343-SMW-SteamboatMario names "Block Kuzushi GB (Japan)" as
// the base ROM for a Super Mario World hack. That is why matching prefers
// the CRC32 embedded in the patch itself (see patch.SourceCRC32) and falls
// back to the readme only for formats that carry no checksum.
type BaseROM struct {
	File          string // No-Intro filename, e.g. "Super Mario World (USA).sfc"
	Size          int64  // 0 when the readme does not say
	CRC32         uint32
	HasCRC        bool
	MD5           string // lower-case hex, "" when absent
	HeaderlessMD5 string // some NES entries list both
}

var (
	labelled    = regexp.MustCompile(`(?i)^\s*([A-Za-z0-9 ()]+?)\s*:\s*(.+?)\s*$`)
	hex32       = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	hex8        = regexp.MustCompile(`^[0-9a-fA-F]{8}$`)
	romNameLine = regexp.MustCompile(`(?i)\.(sfc|smc|nes|fds|gb|gbc|gba|n64|z64|v64|md|gen|bin|smd|sms|gg|pce|ws|wsc|ndd|nds|iso|cue|chd|a26|a78|lnx|col|int|vec|32x|d64|bak)\b`)
	digits      = regexp.MustCompile(`\d+`)
)

// ParseReadme extracts every base ROM the readme describes. It handles the
// two shapes seen in the repository — a labelled block ("File:", "MD5:")
// and a bare filename followed by two loose hashes — and returns nothing
// rather than guessing when a readme is just prose about the hack.
func ParseReadme(text string) []BaseROM {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	var out []BaseROM
	var cur *BaseROM
	flush := func() {
		if cur != nil && (cur.MD5 != "" || cur.HasCRC) {
			out = append(out, *cur)
		}
		cur = nil
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		if m := labelled.FindStringSubmatch(line); m != nil {
			key := strings.ToLower(strings.TrimSpace(m[1]))
			value := strings.TrimSpace(m[2])
			switch {
			case key == "file":
				flush()
				cur = &BaseROM{File: value}
			case key == "md5":
				if cur == nil {
					cur = &BaseROM{}
				}
				if hex32.MatchString(value) {
					cur.MD5 = strings.ToLower(value)
				}
			case key == "headerless md5":
				if cur == nil {
					cur = &BaseROM{}
				}
				if hex32.MatchString(value) {
					cur.HeaderlessMD5 = strings.ToLower(value)
				}
			case key == "crc32":
				if cur == nil {
					cur = &BaseROM{}
				}
				if hex8.MatchString(value) {
					if n, err := strconv.ParseUint(value, 16, 32); err == nil {
						cur.CRC32 = uint32(n)
						cur.HasCRC = true
					}
				}
			case strings.HasPrefix(key, "size"):
				if cur == nil {
					cur = &BaseROM{}
				}
				if d := digits.FindString(strings.ReplaceAll(value, ".", "")); d != "" {
					if n, err := strconv.ParseInt(d, 10, 64); err == nil {
						cur.Size = n
					}
				}
			}
			continue
		}

		// Bare form. A filename opens a record; the hashes that follow
		// belong to it, and either order occurs in the wild, so they are
		// told apart by length rather than by position.
		switch {
		case hex32.MatchString(line):
			if cur == nil {
				cur = &BaseROM{}
			}
			cur.MD5 = strings.ToLower(line)
		case hex8.MatchString(line):
			if cur == nil {
				cur = &BaseROM{}
			}
			if n, err := strconv.ParseUint(line, 16, 32); err == nil {
				cur.CRC32 = uint32(n)
				cur.HasCRC = true
			}
		case romNameLine.MatchString(line) && !strings.HasSuffix(line, ":"):
			flush()
			cur = &BaseROM{File: line}
		}
	}
	flush()
	return out
}

// Describe renders the base ROM the way the app shows it to the user when
// the matching ROM is missing from the device.
func (b BaseROM) Describe() string {
	name := b.File
	if name == "" {
		name = "(unnamed dump)"
	}
	if b.HasCRC {
		return name + "  CRC32 " + strings.ToUpper(strconv.FormatUint(uint64(b.CRC32), 16))
	}
	return name
}

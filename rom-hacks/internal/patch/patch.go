// Package patch applies ROM-hack patches to a base ROM, in pure Go.
//
// RAPatches ships three formats. A 30-archive sample of the Hacks section
// broke down as BPS in 23, IPS in 6 and xdelta in 2, so BPS and IPS are
// implemented here and xdelta is recognised but handed to an external
// binary (see xdelta.go) because its VCDIFF variant is not worth
// reimplementing.
//
// Nothing here trusts the patch: BPS carries a CRC32 of the source it
// expects and of the target it produces, and both are checked. IPS carries
// no checksum at all, which is exactly why the caller must still compare
// the result against the MD5 RetroAchievements publishes for the hack.
package patch

import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
)

// Format is one of the patch container formats found in RAPatches.
type Format int

const (
	Unknown Format = iota
	BPS
	IPS
	UPS
	XDelta
)

func (f Format) String() string {
	switch f {
	case BPS:
		return "BPS"
	case IPS:
		return "IPS"
	case UPS:
		return "UPS"
	case XDelta:
		return "xdelta"
	}
	return "unknown"
}

// ErrExternal means the format is valid but cannot be applied in-process.
var ErrExternal = errors.New("patch format needs an external tool")

// Detect identifies a patch by its magic bytes rather than by file
// extension, because archives in RAPatches are not consistent about
// naming (an .xdelta and a .bps have been seen side by side).
func Detect(data []byte) Format {
	switch {
	case bytes.HasPrefix(data, []byte("BPS1")):
		return BPS
	case bytes.HasPrefix(data, []byte("PATCH")):
		return IPS
	case bytes.HasPrefix(data, []byte("UPS1")):
		return UPS
	case bytes.HasPrefix(data, []byte{0xD6, 0xC3, 0xC4}):
		// VCDIFF/xdelta3. The fourth byte is the version.
		return XDelta
	}
	return Unknown
}

// Apply produces the patched ROM from source. It never mutates source.
func Apply(patchData, source []byte) ([]byte, error) {
	switch Detect(patchData) {
	case BPS:
		return applyBPS(patchData, source)
	case IPS:
		return applyIPS(patchData, source)
	case UPS:
		return applyUPS(patchData, source)
	case XDelta:
		return nil, fmt.Errorf("%w: xdelta", ErrExternal)
	}
	return nil, errors.New("unrecognised patch format")
}

// --- BPS -------------------------------------------------------------
//
// Layout: "BPS1", source size, target size, metadata size, metadata, then
// a stream of actions, then a 12-byte footer with the CRC32 of the source,
// of the target and of the patch itself.

const bpsFooterLen = 12

// maxTarget caps the size a patch may declare for its output. The largest
// cartridge is 64 MB and the in-memory path only handles cartridges; a
// corrupt or hostile header asking for terabytes used to reach make() and
// end the app with an out-of-memory fatal error, which no recover catches.
const maxTarget = 512 << 20

// checkedSize turns a size read from a patch header into an int, refusing
// anything past limit. Converting first and checking after let a huge
// value wrap negative and slip through.
func checkedSize(v uint64, limit int, what string) (int, error) {
	if v > uint64(limit) {
		return 0, fmt.Errorf("%s: %d is out of range", what, v)
	}
	return int(v), nil
}

type bpsReader struct {
	data []byte
	pos  int
}

// number reads BPS's variable-width integer. Each byte carries 7 bits;
// the high bit marks the last one. The +1 bias on continuation removes
// redundant encodings, which is why shift is added back into data.
func (r *bpsReader) number() (uint64, error) {
	var data uint64
	var shift uint64 = 1
	for {
		if r.pos >= len(r.data) {
			return 0, errors.New("bps: truncated number")
		}
		x := r.data[r.pos]
		r.pos++
		data += uint64(x&0x7f) * shift
		if x&0x80 != 0 {
			return data, nil
		}
		shift <<= 7
		data += shift
	}
}

// signed reads a number whose low bit is the sign, used by the two copy
// actions to move their read head backwards as well as forwards.
func (r *bpsReader) signed() (int64, error) {
	n, err := r.number()
	if err != nil {
		return 0, err
	}
	v := int64(n >> 1)
	if n&1 != 0 {
		v = -v
	}
	return v, nil
}

func applyBPS(patchData, source []byte) ([]byte, error) {
	if len(patchData) < 4+bpsFooterLen {
		return nil, errors.New("bps: file too short")
	}
	body := patchData[:len(patchData)-bpsFooterLen]
	footer := patchData[len(patchData)-bpsFooterLen:]
	srcCRC := le32(footer[0:4])
	dstCRC := le32(footer[4:8])

	if got := crc32.ChecksumIEEE(source); got != srcCRC {
		return nil, fmt.Errorf("bps: wrong base ROM (CRC32 %08X, patch wants %08X)", got, srcCRC)
	}

	r := &bpsReader{data: body, pos: 4}
	srcSize, err := r.number()
	if err != nil {
		return nil, err
	}
	if srcSize != uint64(len(source)) {
		return nil, fmt.Errorf("bps: base ROM is %d bytes, patch wants %d", len(source), srcSize)
	}
	dstSize, err := r.number()
	if err != nil {
		return nil, err
	}
	metaSize, err := r.number()
	if err != nil {
		return nil, err
	}
	meta, err := checkedSize(metaSize, len(body)-r.pos, "bps: metadata size")
	if err != nil {
		return nil, err
	}
	r.pos += meta
	size, err := checkedSize(dstSize, maxTarget, "bps: target size")
	if err != nil {
		return nil, err
	}

	target := make([]byte, size)
	var outPos int
	var srcRel, dstRel int64

	for r.pos < len(body) {
		cmd, err := r.number()
		if err != nil {
			return nil, err
		}
		action := cmd & 3
		if cmd>>2 >= uint64(len(target)-outPos) {
			return nil, errors.New("bps: action runs past the end of the target")
		}
		length := int(cmd>>2) + 1

		switch action {
		case 0: // SourceRead: same offset in the base ROM
			if outPos+length > len(source) {
				return nil, errors.New("bps: source read out of range")
			}
			copy(target[outPos:outPos+length], source[outPos:outPos+length])
			outPos += length

		case 1: // TargetRead: literal bytes from the patch
			if r.pos+length > len(body) {
				return nil, errors.New("bps: literal runs past the end of the patch")
			}
			copy(target[outPos:outPos+length], body[r.pos:r.pos+length])
			r.pos += length
			outPos += length

		case 2: // SourceCopy: relative seek into the base ROM
			off, err := r.signed()
			if err != nil {
				return nil, err
			}
			srcRel += off
			if srcRel < 0 || srcRel > int64(len(source))-int64(length) {
				return nil, errors.New("bps: source copy out of range")
			}
			copy(target[outPos:outPos+length], source[srcRel:srcRel+int64(length)])
			srcRel += int64(length)
			outPos += length

		case 3: // TargetCopy: relative seek into what we have written so far
			off, err := r.signed()
			if err != nil {
				return nil, err
			}
			dstRel += off
			// Only bytes already written may be read back. A patch that
			// points at or past the write head would index off the end of
			// the target partway through the run.
			if dstRel < 0 || dstRel >= int64(outPos) {
				return nil, errors.New("bps: target copy out of range")
			}
			// Byte at a time on purpose: the ranges may overlap, and
			// that overlap is how BPS encodes run-length fills.
			for i := 0; i < length; i++ {
				target[outPos] = target[dstRel]
				outPos++
				dstRel++
			}
		}
	}

	if outPos != len(target) {
		return nil, fmt.Errorf("bps: wrote %d bytes, expected %d", outPos, len(target))
	}
	if got := crc32.ChecksumIEEE(target); got != dstCRC {
		return nil, fmt.Errorf("bps: patched ROM is wrong (CRC32 %08X, expected %08X)", got, dstCRC)
	}
	return target, nil
}

// --- IPS -------------------------------------------------------------
//
// Layout: "PATCH", then records of a 3-byte offset and a 2-byte length;
// a zero length means the next 3 bytes are an RLE run and a fill byte.
// "EOF" ends it, optionally followed by a 3-byte truncation length.

func applyIPS(patchData, source []byte) ([]byte, error) {
	if !bytes.HasPrefix(patchData, []byte("PATCH")) {
		return nil, errors.New("ips: missing PATCH header")
	}
	out := make([]byte, len(source))
	copy(out, source)

	pos := 5
	for {
		if pos+3 > len(patchData) {
			return nil, errors.New("ips: truncated before EOF")
		}
		if bytes.Equal(patchData[pos:pos+3], []byte("EOF")) {
			pos += 3
			// Optional truncation record.
			if pos+3 <= len(patchData) {
				if n := be24(patchData[pos : pos+3]); n <= len(out) {
					out = out[:n]
				}
			}
			return out, nil
		}
		offset := be24(patchData[pos : pos+3])
		pos += 3
		if pos+2 > len(patchData) {
			return nil, errors.New("ips: truncated record")
		}
		length := int(patchData[pos])<<8 | int(patchData[pos+1])
		pos += 2

		if length == 0 { // RLE run
			if pos+3 > len(patchData) {
				return nil, errors.New("ips: truncated RLE record")
			}
			runLen := int(patchData[pos])<<8 | int(patchData[pos+1])
			value := patchData[pos+2]
			pos += 3
			out = growTo(out, offset+runLen)
			for i := 0; i < runLen; i++ {
				out[offset+i] = value
			}
			continue
		}

		if pos+length > len(patchData) {
			return nil, errors.New("ips: record runs past the end of the patch")
		}
		out = growTo(out, offset+length)
		copy(out[offset:offset+length], patchData[pos:pos+length])
		pos += length
	}
}

// --- UPS -------------------------------------------------------------
//
// Rare in RAPatches but trivial once the BPS number reader exists: a UPS
// body is a run of (skip, XOR bytes) pairs over the base ROM.

func applyUPS(patchData, source []byte) ([]byte, error) {
	if len(patchData) < 4+12 {
		return nil, errors.New("ups: file too short")
	}
	body := patchData[:len(patchData)-12]
	footer := patchData[len(patchData)-12:]
	srcCRC := le32(footer[0:4])
	dstCRC := le32(footer[4:8])
	if got := crc32.ChecksumIEEE(source); got != srcCRC {
		return nil, fmt.Errorf("ups: wrong base ROM (CRC32 %08X, patch wants %08X)", got, srcCRC)
	}

	r := &bpsReader{data: body, pos: 4}
	if _, err := r.number(); err != nil { // source size
		return nil, err
	}
	dstSize, err := r.number()
	if err != nil {
		return nil, err
	}
	size, err := checkedSize(dstSize, maxTarget, "ups: target size")
	if err != nil {
		return nil, err
	}
	out := make([]byte, size)
	copy(out, source)

	var pos uint64
	for r.pos < len(body) {
		skip, err := r.number()
		if err != nil {
			return nil, err
		}
		pos += skip
		for r.pos < len(body) {
			b := body[r.pos]
			r.pos++
			if b == 0 {
				pos++
				break
			}
			if pos < uint64(len(out)) {
				out[pos] ^= b
			}
			pos++
		}
	}
	if got := crc32.ChecksumIEEE(out); got != dstCRC {
		return nil, fmt.Errorf("ups: patched ROM is wrong (CRC32 %08X, expected %08X)", got, dstCRC)
	}
	return out, nil
}

// --- helpers ---------------------------------------------------------

func growTo(b []byte, n int) []byte {
	if n <= len(b) {
		return b
	}
	grown := make([]byte, n)
	copy(grown, b)
	return grown
}

func be24(b []byte) int { return int(b[0])<<16 | int(b[1])<<8 | int(b[2]) }

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

package patch

import (
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// The in-memory Apply is fine for cartridges, which top out around 64 MB.
// It is fatal for discs. A PlayStation track is ~600 MB, and reading the
// source, building the target and holding both meant well over a gigabyte
// on a handheld with one — the kernel's OOM killer ended the app
// mid-install, with no message beyond "Killed".
//
// These variants stream instead: the patch itself stays in memory, since
// it is a few megabytes at most, while the source and target are touched
// through file handles. Peak memory is a few hundred kilobytes regardless
// of disc size.

// maxDiscTarget caps what a streamed patch may write. A CD image is under
// 900 MB and a PSP ISO under 2 GB; a corrupt header asking for more would
// otherwise fill the card before the size check at the end caught it.
const maxDiscTarget = 4 << 30

// copyBufSize is the working buffer for streaming copies.
const copyBufSize = 1 << 20

// Progress reports how far a patch has got, in bytes written out of the
// total the patch will produce. A 600 MB disc takes minutes on this
// hardware, and a frozen screen for minutes is indistinguishable from a
// crash.
type Progress func(done, total int64)

// ApplyFile patches srcPath into dstPath without loading either into
// memory. dstPath is written fresh and removed if anything fails, so a
// half-patched image is never left behind.
func ApplyFile(patchData []byte, srcPath, dstPath string, progress Progress) error {
	switch Detect(patchData) {
	case BPS:
		return applyBPSFile(patchData, srcPath, dstPath, progress)
	case IPS:
		return applyIPSFile(patchData, srcPath, dstPath)
	case XDelta:
		return applyXDeltaFile(patchData, srcPath, dstPath)
	case UPS:
		// UPS is rare and never seen on a disc; the in-memory path is
		// honest about what it can handle rather than pretending.
		return errors.New("UPS patches are not supported for disc images")
	}
	return errors.New("unrecognised patch format")
}

// --- BPS --------------------------------------------------------------

func applyBPSFile(patchData []byte, srcPath, dstPath string, progress Progress) error {
	if len(patchData) < 4+bpsFooterLen {
		return errors.New("bps: file too short")
	}
	body := patchData[:len(patchData)-bpsFooterLen]
	footer := patchData[len(patchData)-bpsFooterLen:]
	srcCRC := le32(footer[0:4])
	dstCRC := le32(footer[4:8])

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}
	gotCRC, err := crcOfFile(src)
	if err != nil {
		return err
	}
	if gotCRC != srcCRC {
		return fmt.Errorf("bps: wrong base ROM (CRC32 %08X, patch wants %08X)", gotCRC, srcCRC)
	}

	r := &bpsReader{data: body, pos: 4}
	srcSize, err := r.number()
	if err != nil {
		return err
	}
	if srcSize != uint64(info.Size()) {
		return fmt.Errorf("bps: base ROM is %d bytes, patch wants %d", info.Size(), srcSize)
	}
	dstSize, err := r.number()
	if err != nil {
		return err
	}
	if dstSize > maxDiscTarget {
		return fmt.Errorf("bps: target size %d is out of range", dstSize)
	}
	metaSize, err := r.number()
	if err != nil {
		return err
	}
	meta, err := checkedSize(metaSize, len(body)-r.pos, "bps: metadata size")
	if err != nil {
		return err
	}
	r.pos += meta

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		dst.Close()
		if !ok {
			os.Remove(dstPath)
		}
	}()

	var outPos int64
	var srcRel, dstRel int64
	buf := make([]byte, copyBufSize)
	hash := crc32.NewIEEE()

	// writeAt sends bytes to the target and folds them into the running
	// checksum. The target is written strictly in order, so the checksum
	// can be computed as it goes rather than by reading the file back.
	var reported int64
	write := func(b []byte) error {
		if _, err := dst.Write(b); err != nil {
			return err
		}
		hash.Write(b)
		outPos += int64(len(b))
		// Reporting every write would be thousands of callbacks a
		// second; every few megabytes is enough to move a bar.
		if progress != nil && outPos-reported >= 4<<20 {
			reported = outPos
			progress(outPos, int64(dstSize))
		}
		return nil
	}

	// copyFrom moves length bytes from a file offset to the target.
	copyFrom := func(f *os.File, off int64, length int) error {
		for length > 0 {
			n := length
			if n > len(buf) {
				n = len(buf)
			}
			if _, err := f.ReadAt(buf[:n], off); err != nil {
				return err
			}
			if err := write(buf[:n]); err != nil {
				return err
			}
			off += int64(n)
			length -= n
		}
		return nil
	}

	for r.pos < len(body) {
		cmd, err := r.number()
		if err != nil {
			return err
		}
		action := cmd & 3
		if outPos >= int64(dstSize) || cmd>>2 >= dstSize-uint64(outPos) {
			return errors.New("bps: action runs past the end of the target")
		}
		length := int(cmd>>2) + 1

		switch action {
		case 0: // SourceRead
			if err := copyFrom(src, outPos, length); err != nil {
				return fmt.Errorf("bps: source read: %w", err)
			}

		case 1: // TargetRead: literal bytes from the patch
			if r.pos+length > len(body) {
				return errors.New("bps: literal runs past the end of the patch")
			}
			if err := write(body[r.pos : r.pos+length]); err != nil {
				return err
			}
			r.pos += length

		case 2: // SourceCopy
			off, err := r.signed()
			if err != nil {
				return err
			}
			srcRel += off
			if srcRel < 0 || srcRel > info.Size()-int64(length) {
				return errors.New("bps: source copy out of range")
			}
			if err := copyFrom(src, srcRel, length); err != nil {
				return fmt.Errorf("bps: source copy: %w", err)
			}
			srcRel += int64(length)

		case 3: // TargetCopy, which may overlap what is being written
			off, err := r.signed()
			if err != nil {
				return err
			}
			dstRel += off
			if dstRel < 0 || dstRel >= outPos {
				return errors.New("bps: target copy reads past what has been written")
			}
			if err := targetCopy(dst, buf, dstRel, outPos-dstRel, length, write); err != nil {
				return err
			}
			dstRel += int64(length)
		}
	}

	if outPos != int64(dstSize) {
		return fmt.Errorf("bps: wrote %d bytes, expected %d", outPos, dstSize)
	}
	if got := hash.Sum32(); got != dstCRC {
		return fmt.Errorf("bps: patched image is wrong (CRC32 %08X, expected %08X)", got, dstCRC)
	}
	if progress != nil {
		progress(outPos, int64(dstSize))
	}
	ok = true
	return nil
}

// targetCopy writes length bytes read back from the target starting at
// from, where period = outPos - from is how far behind the write head that
// is. When period < length the ranges overlap, and the overlap is the
// point: it is how BPS encodes a run fill. The output is then the first
// period bytes repeated, so they are read once and written as a pattern.
//
// This used to go one byte at a time, a read and a write syscall per byte,
// which on a 600 MB disc full of long fills took many minutes.
func targetCopy(dst *os.File, buf []byte, from, period int64, length int, write func([]byte) error) error {
	if period >= int64(length) || period >= int64(len(buf)) {
		// No overlap, or none within one buffer: copied in chunks no
		// longer than period, each read stays behind the write head.
		for length > 0 {
			n := min(length, len(buf))
			if _, err := dst.ReadAt(buf[:n], from); err != nil {
				return fmt.Errorf("bps: target copy: %w", err)
			}
			if err := write(buf[:n]); err != nil {
				return err
			}
			from += int64(n)
			length -= n
		}
		return nil
	}
	// Overlap with a short period: smaller than the buffer.
	pattern := make([]byte, period)
	if _, err := dst.ReadAt(pattern, from); err != nil {
		return fmt.Errorf("bps: target copy: %w", err)
	}
	// Tile the pattern to a whole number of repeats, so every chunk
	// written starts at the pattern's first byte.
	fill := buf[:0]
	for len(fill)+len(pattern) <= len(buf) {
		fill = append(fill, pattern...)
	}
	for length > 0 {
		n := min(length, len(fill))
		if err := write(fill[:n]); err != nil {
			return err
		}
		length -= n
	}
	return nil
}

// --- IPS --------------------------------------------------------------

func applyIPSFile(patchData []byte, srcPath, dstPath string) error {
	if len(patchData) < 5 || string(patchData[:5]) != "PATCH" {
		return errors.New("ips: missing PATCH header")
	}
	if err := copyFile(srcPath, dstPath); err != nil {
		return err
	}
	dst, err := os.OpenFile(dstPath, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		dst.Close()
		if !ok {
			os.Remove(dstPath)
		}
	}()

	pos := 5
	for {
		if pos+3 > len(patchData) {
			return errors.New("ips: truncated before EOF")
		}
		if string(patchData[pos:pos+3]) == "EOF" {
			pos += 3
			if pos+3 <= len(patchData) {
				// Truncation only ever shortens, as in the in-memory
				// path: a record past the end used to grow the file.
				if n := int64(be24(patchData[pos : pos+3])); n > 0 {
					if info, err := dst.Stat(); err == nil && n < info.Size() {
						if err := dst.Truncate(n); err != nil {
							return err
						}
					}
				}
			}
			ok = true
			return nil
		}
		offset := int64(be24(patchData[pos : pos+3]))
		pos += 3
		if pos+2 > len(patchData) {
			return errors.New("ips: truncated record")
		}
		length := int(patchData[pos])<<8 | int(patchData[pos+1])
		pos += 2

		if length == 0 { // RLE run
			if pos+3 > len(patchData) {
				return errors.New("ips: truncated RLE record")
			}
			runLen := int(patchData[pos])<<8 | int(patchData[pos+1])
			value := patchData[pos+2]
			pos += 3
			run := make([]byte, runLen)
			for i := range run {
				run[i] = value
			}
			if _, err := dst.WriteAt(run, offset); err != nil {
				return err
			}
			continue
		}

		if pos+length > len(patchData) {
			return errors.New("ips: record runs past the end of the patch")
		}
		if _, err := dst.WriteAt(patchData[pos:pos+length], offset); err != nil {
			return err
		}
		pos += length
	}
}

// --- xdelta -----------------------------------------------------------

func applyXDeltaFile(patchData []byte, srcPath, dstPath string) error {
	dir, err := xdeltaWorkDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	patchPath := dir + "/patch.xdelta"
	if err := os.WriteFile(patchPath, patchData, 0o644); err != nil {
		return err
	}
	out, err := runXDelta(srcPath, patchPath, dstPath)
	if err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("xdelta3: %v: %s", err, out)
	}
	return nil
}

// --- helpers ----------------------------------------------------------

func crcOfFile(f *os.File) (uint32, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	hash := crc32.NewIEEE()
	if _, err := io.CopyBuffer(hash, f, make([]byte, copyBufSize)); err != nil {
		return 0, err
	}
	return hash.Sum32(), nil
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.CopyBuffer(dst, src, make([]byte, copyBufSize)); err != nil {
		os.Remove(dstPath)
		return err
	}
	return nil
}

// SourceCRC32OfFile is SourceCRC32's companion for a file on disk, used to
// check a disc image before anything is written.
func SourceCRC32OfFile(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return crcOfFile(f)
}

package library

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"leaf-hacks/internal/rahub"
)

// Plenty of collections keep cartridge ROMs zipped — every emulator on the
// device reads them that way, so there is no reason for the user to
// unpack anything. A scan that only looked at bare .sfc and .gb files
// would report an empty folder and quietly hide most of a collection.
//
// Arcade is the exception: there the .zip IS the ROM, and
// RetroAchievements hashes it by filename, so it must never be opened.

const maxZipEntry = 96 << 20

// isZip reports whether a filename is a zip container.
func isZip(name string) bool { return strings.EqualFold(path.Ext(name), ".zip") }

// readROM returns the bytes to hash and to patch for a file on disk: the
// file itself, or the ROM inside it when it is a zip. It also reports the
// name of the entry used, which matters for consoles whose hash depends on
// the filename.
func readROM(filePath string, console rahub.Console) (data []byte, inner string, err error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "", err
	}
	if !isZip(filePath) || console.Method == rahub.HashArcade {
		return raw, "", nil
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", filePath, err)
	}

	// Pick the largest entry with an extension this console accepts. Size
	// is the tiebreaker because archives often carry a readme or a save
	// state alongside the ROM.
	best := -1
	var bestSize int64
	for i, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !console.HasExt(f.Name) {
			continue
		}
		if size := f.FileInfo().Size(); size > bestSize {
			best, bestSize = i, size
		}
	}
	if best < 0 {
		return nil, "", fmt.Errorf("%s: no %s ROM inside the zip", path.Base(filePath), console.Short)
	}
	if bestSize > maxZipEntry {
		return nil, "", fmt.Errorf("%s: the ROM inside is too large", path.Base(filePath))
	}

	rc, err := zr.File[best].Open()
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	data, err = io.ReadAll(io.LimitReader(rc, maxZipEntry))
	if err != nil {
		return nil, "", err
	}
	return data, zr.File[best].Name, nil
}

// Read returns the ROM's actual bytes, transparently unwrapping a zip.
// Callers that patch a ROM must use this rather than os.ReadFile, or they
// would feed the patcher a zip container.
func Read(r ROM) ([]byte, error) {
	console, ok := r.Console()
	if !ok {
		return os.ReadFile(r.Path)
	}
	data, _, err := readROM(r.Path, console)
	return data, err
}

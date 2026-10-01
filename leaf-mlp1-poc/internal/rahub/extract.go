package rahub

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// Extraction limits: generous for cartridge homebrew, small enough that a
// mislabelled PC build cannot fill the SD card.
const (
	maxEntryBytes = 1 << 30 // a CD image is up to ~800 MB
	maxTotalBytes = 2 << 30
	maxEntries    = 4000
	maxDepth      = 2 // an archive inside an archive is common; deeper is not
)

// ErrRAR is returned for .rar downloads: there is no pure-Go extractor in
// this build, so the file cannot be checked.
var ErrRAR = errors.New("RAR archive (not supported, cannot verify)")

// archiveKind sniffs a file's real type from its first bytes. itch.io upload
// names are display names and often lie or carry no extension.
func archiveKind(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return "zip"
	case bytes.HasPrefix(head, []byte("7z\xbc\xaf\x27\x1c")):
		return "7z"
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "gzip"
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07")):
		return "rar"
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return "tar"
	}
	return ""
}

// Unpack returns every regular file a download contains. A plain file comes
// back as itself; archives are extracted under workDir (recursively, up to
// maxDepth). Paths escaping workDir are refused (zip-slip).
func Unpack(download, workDir string) ([]string, error) {
	var total int64
	count := 0
	return unpack(download, workDir, 0, &total, &count)
}

func unpack(path, workDir string, depth int, total *int64, count *int) ([]string, error) {
	kind := archiveKind(path)
	if kind == "" || depth > maxDepth {
		return []string{path}, nil
	}
	if kind == "rar" {
		return nil, ErrRAR
	}
	dest, err := os.MkdirTemp(workDir, "x-")
	if err != nil {
		return nil, err
	}
	var files []string
	emit := func(name string, size int64, open func() (io.ReadCloser, error)) error {
		*count++
		if *count > maxEntries {
			return fmt.Errorf("archive has too many files")
		}
		clean := filepath.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
		if strings.HasPrefix(clean, "/__MACOSX/") || strings.HasPrefix(filepath.Base(clean), "._") {
			return nil
		}
		if size > maxEntryBytes {
			return nil // skip oversized entries rather than failing the archive
		}
		target := filepath.Join(dest, clean)
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(rc, maxEntryBytes+1))
		cerr := out.Close()
		if err != nil {
			return err
		}
		if cerr != nil {
			return cerr
		}
		*total += n
		if n > maxEntryBytes {
			os.Remove(target)
			return nil
		}
		if *total > maxTotalBytes {
			return fmt.Errorf("archive expands beyond %d MB", maxTotalBytes>>20)
		}
		inner, err := unpack(target, workDir, depth+1, total, count)
		if err != nil {
			if errors.Is(err, ErrRAR) {
				return nil // a nested RAR: ignore, other files may still match
			}
			return err
		}
		files = append(files, inner...)
		return nil
	}

	switch kind {
	case "zip":
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			f := f
			if err := emit(f.Name, int64(f.UncompressedSize64), func() (io.ReadCloser, error) { return f.Open() }); err != nil {
				return files, err
			}
		}
	case "7z":
		zr, err := sevenzip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("open 7z: %w", err)
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			f := f
			if err := emit(f.Name, int64(f.UncompressedSize), func() (io.ReadCloser, error) { return f.Open() }); err != nil {
				return files, err
			}
		}
	case "gzip", "tar":
		fh, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer fh.Close()
		var r io.Reader = fh
		if kind == "gzip" {
			gz, err := gzip.NewReader(fh)
			if err != nil {
				return nil, fmt.Errorf("open gzip: %w", err)
			}
			defer gz.Close()
			// A .gz may hold a tar or a single file: peek for "ustar".
			buf := make([]byte, 512)
			n, _ := io.ReadFull(gz, buf)
			buf = buf[:n]
			r = io.MultiReader(bytes.NewReader(buf), gz)
			if !(n >= 262 && string(buf[257:262]) == "ustar") {
				name := strings.TrimSuffix(filepath.Base(path), ".gz")
				if err := emit(name, 0, func() (io.ReadCloser, error) { return io.NopCloser(r), nil }); err != nil {
					return files, err
				}
				return files, nil
			}
		}
		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return files, fmt.Errorf("read tar: %w", err)
			}
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
				continue
			}
			if err := emit(hdr.Name, hdr.Size, func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }); err != nil {
				return files, err
			}
		}
	}
	return files, nil
}

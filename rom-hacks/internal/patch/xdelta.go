package patch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// IsExternal reports whether err means "valid patch, wrong tool" — the
// caller should try ApplyXDelta instead of giving up.
func IsExternal(err error) bool { return errors.Is(err, ErrExternal) }

// XDeltaBinary is looked up here first, then on PATH. The app ships an
// aarch64 xdelta3 next to its own binary because Go cannot decode this
// format in-process and only about one hack in fifteen needs it.
var XDeltaBinary = ""

func xdeltaPath() (string, error) {
	if XDeltaBinary != "" {
		if _, err := os.Stat(XDeltaBinary); err == nil {
			return XDeltaBinary, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "xdelta3")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return exec.LookPath("xdelta3")
}

// ApplyXDelta shells out to xdelta3 in a temporary directory and returns
// the patched bytes. Unlike BPS it validates nothing itself, so the
// caller's hash check against RetroAchievements is the only guarantee.
func ApplyXDelta(patchData, source []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "xdelta")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	srcPath := filepath.Join(dir, "source.bin")
	patchPath := filepath.Join(dir, "patch.xdelta")
	outPath := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(srcPath, source, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(patchPath, patchData, 0o644); err != nil {
		return nil, err
	}

	if out, err := runXDelta(srcPath, patchPath, outPath); err != nil {
		return nil, fmt.Errorf("xdelta3: %v: %s", err, out)
	}
	return os.ReadFile(outPath)
}

// runXDelta decodes a patch from paths to a path, touching no memory of
// its own. Shared by the in-memory and the streaming entry points.
func runXDelta(srcPath, patchPath, outPath string) ([]byte, error) {
	bin, err := xdeltaPath()
	if err != nil {
		return nil, fmt.Errorf("this hack needs an xdelta patch and xdelta3 was not found: %w", err)
	}
	return exec.Command(bin, "-d", "-f", "-s", srcPath, patchPath, outPath).CombinedOutput()
}

// ApplyAny is what callers should use: it handles every format, falling
// back to the external tool only when the patch is xdelta.
func ApplyAny(patchData, source []byte) ([]byte, error) {
	out, err := Apply(patchData, source)
	if IsExternal(err) {
		return ApplyXDelta(patchData, source)
	}
	return out, err
}

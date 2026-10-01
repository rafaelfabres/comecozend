package leaf

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const StorageReserveBytes int64 = 4 * 1024 * 1024

func existingStoragePath(path string) (string, error) {
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing storage ancestor for %s", path)
		}
		path = parent
	}
}

func AvailableBytes(path string) (uint64, error) {
	existing, err := existingStoragePath(path)
	if err != nil {
		return 0, err
	}
	var stats unix.Statfs_t
	if err := unix.Statfs(existing, &stats); err != nil {
		return 0, err
	}
	return uint64(stats.Bavail) * uint64(stats.Bsize), nil
}

func RequireFreeSpace(path string, contentBytes int64) error {
	if contentBytes <= 0 {
		return nil
	}
	required := uint64(contentBytes)
	reserve := uint64(StorageReserveBytes)
	if required > ^uint64(0)-reserve {
		return fmt.Errorf("required storage size overflows")
	}
	required += reserve
	available, err := AvailableBytes(path)
	if err != nil {
		return fmt.Errorf("check free space: %w", err)
	}
	if available < required {
		return fmt.Errorf("insufficient free space: need %d bytes plus %d-byte reserve, have %d bytes",
			contentBytes, StorageReserveBytes, available)
	}
	return nil
}

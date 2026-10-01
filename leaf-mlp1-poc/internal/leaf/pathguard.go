package leaf

import (
	"fmt"
	"path/filepath"
	"strings"
)

func JoinWithin(root string, parts ...string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("content root is empty")
	}
	for _, part := range parts {
		if filepath.IsAbs(part) {
			return "", fmt.Errorf("absolute path component %q", part)
		}
	}
	target := filepath.Join(append([]string{root}, parts...)...)
	if _, err := RelativeWithin(root, target); err != nil {
		return "", err
	}
	return target, nil
}

func RelativeWithin(root, target string) (string, error) {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes root %q", target, root)
	}
	return rel, nil
}

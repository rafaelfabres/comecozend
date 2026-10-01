package roms

import (
	"context"
	"errors"
	"fmt"

	"leaf-hacks/internal/logger"
)

// remoteRequestError prevents signed CDN URLs from escaping through an error
// shown by the Cat UI while keeping the full failure in the redacted debug log.
func remoteRequestError(operation string, err error) error {
	logger.Debug("%s request failed: %v", operation, err)
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: network request failed", operation)
}

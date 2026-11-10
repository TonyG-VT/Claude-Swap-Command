package main

import (
	"context"
	"time"
)

// Reserve shutdown before waiting for a background usage check to leave opMu.
// The busy flag remains set on success so no queued UI command or refresh can
// begin a new mutation before the process exits and releases its instance lock.
// This concerns the helper only; it supplies no safe-stop proof for Claude.
func reserveHelperShutdown(ctx context.Context, s *switcher) bool {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	reserved := false
	for {
		if err := ctx.Err(); err != nil {
			if reserved {
				s.busy.Store(false)
			}
			return false
		}
		if !reserved {
			reserved = s.busy.CompareAndSwap(false, true)
		}
		if reserved && s.opMu.TryLock() {
			s.opMu.Unlock()
			return true
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

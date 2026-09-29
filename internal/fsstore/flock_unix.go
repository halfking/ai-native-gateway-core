//go:build !windows

package fsstore

import (
	"fmt"
	"os"
	"syscall"
)

// lockAndWrite holds an exclusive flock on `final` while writing
// `body` to `tmp` and atomically renaming tmp → final. The flock
// serialises cross-process writers across the whole write+rename
// window; it is released when this function returns.
func lockAndWrite(final, tmp string, body []byte) error {
	// Open the final file first (creating if absent) so we can
	// flock on a stable path.
	lock, err := os.OpenFile(final, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("fsstore: open lock file: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("fsstore: flock: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("fsstore: write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("fsstore: rename: %w", err)
	}
	return nil
}

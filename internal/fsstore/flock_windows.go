//go:build windows

package fsstore

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockDirFileName is a per-directory sidecar lock. A byte-range lock
// on `final` itself would make the tmp → final rename fail with
// ACCESS_DENIED (locked ranges block MoveFileEx), so writers instead
// serialise on one lock file per directory. Directory scanners skip
// it: it is not a directory and does not end in .json.
const lockDirFileName = ".gw_flock"

// lockAndWrite serialises cross-process writers on a per-directory
// LockFileEx range lock while writing `body` to `tmp` and renaming
// tmp → final. The lock is released by the deferred handle close.
func lockAndWrite(final, tmp string, body []byte) error {
	lockPath := filepath.Join(filepath.Dir(final), lockDirFileName)
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("fsstore: open lock file: %w", err)
	}
	defer lock.Close()

	// Whole-file exclusive lock: zero offset with maximal length
	// covers the file regardless of size.
	if err := windows.LockFileEx(
		windows.Handle(lock.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, ^uint32(0), ^uint32(0),
		&windows.Overlapped{},
	); err != nil {
		return fmt.Errorf("fsstore: LockFileEx: %w", err)
	}

	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("fsstore: write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("fsstore: rename: %w", err)
	}
	return nil
}

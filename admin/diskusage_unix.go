//go:build !windows

package admin

import "syscall"

// statfsBytes returns (total, availToRoot, free) byte counts for the
// filesystem containing path, mirroring POSIX statfs semantics.
// On failure all values are zero and callers treat the disk as unknown.
func statfsBytes(path string) (total, availToRoot, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, st.Bfree * bs
}

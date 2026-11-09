//go:build !windows

package streaming

import "golang.org/x/sys/unix"

func archiveFreeBytes(path string) (uint64, error) {
	var state unix.Statfs_t
	err := unix.Statfs(path, &state)
	return uint64(state.Bavail) * uint64(state.Bsize), err
}

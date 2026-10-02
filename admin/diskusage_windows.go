//go:build windows

package admin

import "golang.org/x/sys/windows"

// statfsBytes returns (total, availToRoot, free) byte counts for the
// filesystem containing path, mirroring POSIX statfs semantics.
// Windows has no reserved-block distinction, so availToRoot == free.
// On failure all values are zero and callers treat the disk as unknown.
func statfsBytes(path string) (total, availToRoot, free uint64) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0
	}
	var freeToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &totalBytes, &totalFree); err != nil {
		return 0, 0, 0
	}
	return totalBytes, freeToCaller, totalFree
}

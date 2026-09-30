//go:build !windows

package atomicrename

import "os"

// Replace 直接走 os.Rename：POSIX 对已存在目标即原子替换，
// 无 Windows 那类瞬态错误（见 rename_windows.go）。
func Replace(tmp, final string) error { return os.Rename(tmp, final) }

// Remove 等价于 os.Remove，但把「文件不存在」归一为 nil（幂等删除，
// 与 Windows 版语义对齐）。
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

//go:build windows

// Package atomicrename 提供"临时文件 → 最终路径"的跨平台原子替换。
//
// POSIX rename(2) 对已存在目标是原子替换；Windows 的 MoveFileEx
// (MOVEFILE_REPLACE_EXISTING) 在目标正被并发替换的瞬间返回
// ACCESS_DENIED / SHARING_VIOLATION——这是"稍后重试必然成功"的瞬态
// 错误，不是权限问题。本包在 Windows 上对这两类错误做有界退避重试，
// 使并发覆盖写同一路径的行为与 POSIX 对齐（全部成功、后写者胜出）。
package atomicrename

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// winerr 是 winerror.h 的数值形态：os.Rename 的 *os.LinkError 内层即
// syscall.Errno。syscall 包未导出这两个符号名，按数值比对（go1.21 起
// syscall.Errno.Error() 文本亦含数值，双保险可再加字符串，数值已足够）。
const (
	errAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
	errSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
)

// 重试总预算约 1.9s（10 次尝试）：并发替换窗口是微秒级，但杀软/索引器
// 对新建文件的扫描可持有句柄数百毫秒——短预算在高频写路径上仍会漏
// （2026-09-30 R36 连续 6 次压力复现实证）。真实权限错误（ACL）不会被
// 瞬态判定命中，首轮即返回，不拖慢失败路径。
var retryDelays = []time.Duration{
	time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond,
	10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond,
	100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond,
	750 * time.Millisecond,
}

func transient(err error) bool {
	return errors.Is(err, errAccessDenied) ||
		errors.Is(err, errSharingViolation)
}

// Replace 将 tmp 原子替换到 final（final 已存在则覆盖）。
// 非 Windows 构建下等价于 os.Rename（见 rename_other.go）。
func Replace(tmp, final string) error {
	var err error
	for _, d := range retryDelays {
		if err = os.Rename(tmp, final); err == nil {
			return nil
		}
		if !transient(err) {
			return err
		}
		time.Sleep(d)
	}
	return err
}

// Remove 删除 path。Windows 上读者并发打开文件（os.ReadFile 等，Go 打开
// 时不带 FILE_SHARE_DELETE）会让删除报 SHARING_VIOLATION——POSIX 下
// unlink-while-open 直接成功；此处对瞬态冲突有界重试以对齐。文件不存在
// 归一为 nil（幂等删除）。
func Remove(path string) error {
	var err error
	for _, d := range retryDelays {
		if err = os.Remove(path); err == nil {
			return nil
		}
		if os.IsNotExist(err) {
			return nil
		}
		if !transient(err) {
			return err
		}
		time.Sleep(d)
	}
	return err
}

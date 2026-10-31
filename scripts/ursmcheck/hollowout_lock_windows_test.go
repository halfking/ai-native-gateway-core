//go:build windows

package ursm_test

// hollowout_lock_windows_test.go —— 【整仓掏空锁】的 windows 实现（★ `LockFileEx`）。
//
// ★ 与 unix 版同语义：★ 排他、★ 带等待上限、★ 随进程退出自动释放。
// ★ 仓内已有同款先例：`internal/fsstore/flock_windows.go`（★ 用 `golang.org/x/sys/windows`）。

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockHollowoutFile 取得【路径 path】上的排他文件锁。
//
// ★ 等待方式：★ `LOCKFILE_FAIL_IMMEDIATELY` + 轮询
// ⇒ ⇒ 「锁被占」表现为 `ERROR_LOCK_VIOLATION` ⇒ ⇒ 与 unix 版**同构**。
//
// ★★ 【为什么返回尝试次数而不是让调用方去测耗时】：★ 见 unix 版的同名注释
// ★★ （★ 仓内 `internal/clockguard` 会把时序断言判成【判别型下界】⇒ ⇒ 要求登记实测台账）。
func lockHollowoutFile(path string, wait time.Duration) (func(), int, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, 0, fmt.Errorf("开锁文件失败：%w", err)
	}
	handle := windows.Handle(f.Fd())
	deadline := time.Now().Add(wait)
	attempts := 0
	for {
		attempts++
		err = windows.LockFileEx(
			handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, ^uint32(0), ^uint32(0),
			&windows.Overlapped{},
		)
		if err == nil {
			return func() {
				_ = windows.UnlockFileEx(handle, 0, ^uint32(0), ^uint32(0), &windows.Overlapped{})
				_ = f.Close()
			}, attempts, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = f.Close()
			return nil, attempts, fmt.Errorf("LockFileEx 失败：%w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, attempts, fmt.Errorf("等锁超过 %s ⇒ ⇒ 另一个掏空跑还占着它（★ 已尝试 %d 次）", wait, attempts)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

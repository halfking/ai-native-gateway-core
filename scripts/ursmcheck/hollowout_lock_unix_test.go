//go:build !windows

package ursm_test

// hollowout_lock_unix_test.go —— 【整仓掏空锁】的 unix 实现（★ `flock`）。
//
// ★ 为什么用 `flock` 而不是「`O_EXCL` 建锁文件」：
// ★ ① `flock` 随进程退出**自动释放**（★ 被 SIGKILL 也不会留下死锁）
// ★ ② `flock` 在**同一进程**的不同 fd 之间**也互斥**
// ★    （★ POSIX：`flock()` 的锁挂在【open file description】上，不是挂在进程上）
// ★    ⇒ ⇒ 「第二个持有者拿不到锁」这件事**可以在单进程内被自证**。
// ★★ `O_EXCL` 锁文件两条都不满足：★ 进程被杀 ⇒ 锁文件留在盘上 ⇒ 下次跑**永远拿不到**。

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockHollowoutFile 取得【路径 path】上的排他文件锁。
//
// ★ 返回值：
//   - (release, nil)：★ 拿到锁
//   - (nil, err)：★ 等待超时 / flock 失败
//
// ★ 等待方式：★ `LOCK_EX | LOCK_NB` + 轮询（★ `flock` 没有「带超时的阻塞」变体）。
func lockHollowoutFile(path string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("开锁文件失败：%w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("flock 失败：%w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("等锁超过 %s ⇒ ⇒ 另一个掏空跑还占着它", wait)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

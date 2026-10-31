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
//
// ★★ 【为什么返回尝试次数而不是让调用方去测耗时】：★ 仓内的 `internal/clockguard`
// ★★ 会把「断言 `elapsed < <时长字面量>`」判成【判别型下界】⇒ ⇒ 要求把它登记进
// ★★ `measured_margins.tsv`（★ 实测台账）⇒ ⇒ **而那要求在本机量 10~30 次自然值**
// ★★ ⇒ ⇒ 一条锁的自证就得上「实测台账」，★ 代价与收益完全不成比例。
// ★★ ⇒ ⇒ **更好的做法是根本不要引入时序依赖**：★ 「第二个持有者确实等过」这件事
// ★★ ⇒ ⇒ **用【轮询次数】表达**（★ 它至少轮询过一次 ⇒ ⇒ 它确实在等）⇒ ⇒ 与机器快慢无关。
func lockHollowoutFile(path string, wait time.Duration) (func(), int, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, 0, fmt.Errorf("开锁文件失败：%w", err)
	}
	deadline := time.Now().Add(wait)
	attempts := 0
	for {
		attempts++
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, attempts, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, attempts, fmt.Errorf("flock 失败：%w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, attempts, fmt.Errorf("等锁超过 %s ⇒ ⇒ 另一个掏空跑还占着它（★ 已尝试 %d 次）", wait, attempts)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

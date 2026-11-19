package settings

import (
	"sync"
	"testing"
)

// TestTimezoneTestHookHasNoDataRace —— 钉测：测试注入钩子与热路径读取**无数据竞争**。
//
// 缺陷背景（2026-11-20 审计轮）：
//
//	钩子的**写**面（SystemTimezoneLocationForTest）在 `tzMu.Lock()` 下进行，
//	而**读**面（SystemTimezoneName / SystemTimezoneLocation）原先是**裸读**
//	`tzTestHook`（不持任何锁）⇒ Go 内存模型下的数据竞争。
//
//	实测复现（修复前）：`go test -race` 报
//	  WARNING: DATA RACE
//	  Read at …: settings.SystemTimezoneName()  settings/timezone.go:87
//	  Previous write at …: settings.SystemTimezoneLocationForTest()  settings/timezone.go:200
//
// ★ 本门必须配 `-race` 跑才有判别力（无 race detector 时竞争不会导致失败）。
//
// CI 通道：`make test-race-core` / `go test -race ./settings/`。
//
// ★ 变异验证：把读面改回裸读 `if tzTestHook != nil` ⇒ 本门在 `-race` 下红。
func TestTimezoneTestHookHasNoDataRace(t *testing.T) {
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 读面：模拟热路径并发读取（与生产侧 SystemTimezoneName 同一入口）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = SystemTimezoneName()
				_ = SystemTimezoneLocation()
			}
		}
	}()

	// 写面：反复注入/摘除钩子（与单测注入通道同一入口）。
	for i := 0; i < 200; i++ {
		SystemTimezoneLocationForTest("Asia/Shanghai")
		SystemTimezoneLocationForTest("UTC")
		SystemTimezoneLocationForTest("")
	}
	close(stop)
	wg.Wait()

	// 收尾：确保钩子与缓存复位，避免污染同包其它测试。
	SystemTimezoneLocationForTest("")
}

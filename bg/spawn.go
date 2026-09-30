// Package bg — spawn.go
//
// 2026-09-30 (三十七轮审计 §三#5): 统一的 goroutine panic 收口。此前 bg/ 内
// 除 BaseWorker 监督循环外，约 40 处裸 `go` 语句都是独立 crash 域——任何一处
// panic 都会让整个进程陪葬（所有 worker、所有在途请求一起死）。本文件提供
// 两个收口入口，按被包裹体的失败语义二选一：
//
//   - SpawnLoop：循环型 worker 顶层入口。panic 被 recover 后按 BaseWorker 的
//     指数退避在同一 ctx 上重启（自愈），restart 计数进
//     llm_gateway_bg_worker_restarts_total。等价于嵌入 BaseWorker 并调用
//     Start，但不要求宿主结构改造。
//   - Go / GoArg：一次性辅助 goroutine 或带外部握手（wg.Done/done channel/
//     启动信号）的循环。panic 被 recover、记 slog.Error + stack +
//     llm_gateway_bg_goroutine_panics_total 后 goroutine 终止——不重启，
//     因为重启会二次触发握手信号（对 wg.Done 是负计数 panic）。
//
// 两者都不改变"函数体内已有自己的 recover"的语义；已自带 recover 的站点
// 无需迁移。
package bg

import (
	"context"
	"log/slog"
	"runtime/debug"
)

// Go 在新 goroutine 中运行 fn，panic 收口为日志 + 指标（无重启）。
// 适用于一次性辅助 goroutine；name 用于日志与指标标签，请传编译期字面量。
// fn 为 nil 时等价于不启动。
func Go(name string, fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				metricGoroutinePanics.WithLabelValues(name).Inc()
				slog.Error("bg goroutine panicked",
					"name", name, "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

// GoArg 是 Go 的带参形态：arg 在启动前求值并按值传入，保持既有
// `go func(t T){...}(loopVar)` 写法对循环变量的隔离语义（避免迁移时把
// 参数传递改写成闭包捕获而重新引入共享捕获 bug）。
func GoArg[T any](name string, arg T, fn func(T)) {
	if fn == nil {
		return
	}
	Go(name, func() { fn(arg) })
}

// SpawnLoop 以 BaseWorker 的监督语义运行 runFn：panic recover + 指数退避
// 重启（自愈）+ llm_gateway_bg_worker_restarts_total 计数。runFn 正常
// return 视为有意退出、不重启。适用于无外部握手依赖的循环型入口
// （`go w.run(ctx)` 的直接替代）；带 wg.Done/done channel 握手的循环
// 必须用 Go（重启会二次触发握手）。
func SpawnLoop(parent context.Context, name string, runFn func(context.Context)) {
	NewBaseWorker(name).Start(parent, runFn) //nolint:errcheck // 已启动/已停止时静默不重复拉起，与裸 go 语义一致
}

package main

import "time"

// shutdown_budget.go — 停机预算的**单一真源**（2026-11-17 cold shutdown-budget 轮，
// 2026-11-25 从 stash 恢复并补齐缺失文件）。
//
// ★ 为什么要有这个文件：
//   停机预算此前散在三处且各自为政：
//     ① cmd/gateway/main.go 的三段 ctx 超时（注销/探针排空/srv.Shutdown/worker stop）；
//     ② deploy/*.service 的 TimeoutStopSec（systemd 强杀线）；
//     ③ scripts/deploy-seamless-docker.sh 的 docker stop -t（容器强杀线）。
//   三处曾经互相矛盾：单元写 25s、应用最坏要 31s ⇒ systemd 在排空完成前
//   SIGKILL ⇒ 在途 SSE / 流式请求被硬切 ⇒ nginx 502（llm.itestu.cn 的
//   用户可见形态）。本文件把应用侧预算收成**具名常量**，让 ②③ 可以跟它
//   **对拍**（tests/deploy_stop_budget_test.sh：「部署侧 ≥ 应用侧最坏预算」）。
//
// ★ 停机最坏预算（同步注销口径，2026-10-31 订正后的现状）：
//   注销 3s ＋ mock probe drain 5s ＋ srv.Shutdown 23s ＋ worker stop 5s = 36s
//   ⇒ 部署侧强杀线必须 ≥ 36s；45s = 36s + 9s 余量。
//
// ★ 残余代价（如实登记，不藏）：单实例 systemd 形态在 SIGTERM 后 listener
//   立刻关闭 ⇒ 停机窗口内新请求 connection refused —— 单实例形态固有代价，
//   蓝绿形态（211/245/252）没有这个窗口。
const (
	// shutdownDeregisterBudget 是 goacos 同步注销的预算上限。
	// （registration.Stop 的 DeregisterTimeout 同值；改这里要同步对拍。）
	shutdownDeregisterBudget = 3 * time.Second

	// shutdownProbeDrainBudget 是停机时 mock probe 历史写入队列的排空预算。
	// 正常毫秒级；5s 是 PG 停摆兜底上限。
	shutdownProbeDrainBudget = 5 * time.Second

	// shutdownHTTPDrainBudget 是 srv.Shutdown 的在途请求排空预算。
	// 2026-08-19: 30s→23s；systemd（本文件未改动前）25s 是事故根因之一。
	shutdownHTTPDrainBudget = 23 * time.Second

	// shutdownWorkerBudget 是 srv.Shutdown 之后后台 worker 的停止预算。
	// 2026-08-19: 20s→5s —— srv.Shutdown 之后必须快速失败。
	shutdownWorkerBudget = 5 * time.Second

	// shutdownWorstCaseBudget 是上述四段的**串行最坏预算**。
	// 部署侧（systemd TimeoutStopSec / docker stop -t）必须 ≥ 本值。
	shutdownWorstCaseBudget = shutdownDeregisterBudget +
		shutdownProbeDrainBudget +
		shutdownHTTPDrainBudget +
		shutdownWorkerBudget
)

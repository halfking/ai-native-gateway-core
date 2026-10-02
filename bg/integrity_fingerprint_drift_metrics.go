package bg

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// integrity fingerprint drift 的可观测出口（2026-10-02 审计 §9.50）。
//
// # 要观测的失效形态
//
// `IntegrityFingerprintDrift` 是一个**安全相关检测器**（凭据/模型的指纹漂移）。
// 它每轮先问一个一次性探针「当前半窗口里有没有带 system_fingerprint 的流量」：
//
//	inProcSeen := telemetry.SystemFingerprintObservedSince()
//	switch fingerprintScanDecision(inProcSeen, w.probeDone, w.probeEmpty) {
//	case fingerprintScanSkip: skippedTicks.Add(1); return
//	case fingerprintScanProbe: … w.probeEmpty = err == nil && !hasFP
//	}
//
// 而那个探针（`probeFingerprintTraffic`）读的是 v1 的 `system_fingerprint`。
//
// # ★实测订正（审计 §9.51，2026-10-02，本地真库）
//
// 这个文件的第一版把原因写成「S4 停写之后 v1 不再产生新行 ⇒ 探针恒空」。
// **真库实测否定了它**：
//
//	面                                    行数      system_fingerprint 非空
//	request_logs（v1 全表）             2,164,650   0
//	session_turns（会话族全表）         1,683,739   0
//	model_integrity_events.context JSONB  7,081      0   （2026-09-05 起）
//
// JSONB 那一路是独立证据：它不走专用列，同样直取上游的
// `X-System-Fingerprint` 响应头（`executor_chat.go:1926` / `handler.go:6730`）。
// ⇒ **上游从不发这个头**，探针在停写**之前**就已经是空的。检测器自 2026-09-25
// （D11 短路上线）起一直关着，与 S4 无关。
//
// S4 停写的真实影响是**前瞻性**的：腿 2（`inProcSeen` 的进程内 arm）的唯一写入方
// `markSystemFingerprintObserved()` 在 `persistSystemFingerprint()` 内调用，而后者
// 位于 `if logsWrite {}` 块内（client.go:1816 / :2468）⇒ 停写后它一次都不执行。
// ⇒ **即便上游将来开始发指纹，停写 + 重启后检测器也永远不会恢复。**
// 门见 domains/hooks/observability/telemetry/fingerprint_escape_hatch_gate_test.go。
//
// 这不是「读点变空」那种显示层降级，而是**一个安全检测器自己把自己关掉了**。
// 而在此之前它**完全不可见**：
//
//   - `skippedTicks` 全仓 3 处引用**全是 `Add(1)`，没有任何地方读它**——它既不是
//     指标，也不在后续任何日志里。
//   - 该文件在此之前**一个 Prometheus 指标都没有**。
//   - 唯一痕迹是「那一刻」的一条 `slog.Info`，之后每轮静默。
//
// ⇒ 停写那一刻，运维会看到一条 Info，然后**再无任何信号**，直到有人从别处发现
// 漂移检测一直没跑。§9.38 已经在 `ledger_reconciliation` / `credential_recovery`
// 上修过同一种形状（`SkippedChecks()` 无出口）；这里没做。
//
// # 为什么 last_scan_unix 在 Start() 就置为进程启动时刻
//
// 探针是**每进程一次**的，所以「还没扫过」是一个真实且危险的状态——它正是停写后
// 刚重启那一刻的形态。若把 gauge 初始化成 0，`time() - 0` 恒为巨大值，**每个刚
// 启动的进程都会立刻告警**；若不初始化，序列不存在，告警永远不响。⇒ 置为启动
// 时刻：「距上次扫描超过 2h」对刚启动的进程自然不成立，对「启动后一直没扫」的
// 进程则如实报警。
//
// 标签纪律（GW-00）：三个指标**都不带标签**，基数为 1。

var (
	// fingerprintDriftScannedTotal 是真正执行过全量扫描的轮数。
	fingerprintDriftScannedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llm_gateway_bg_fingerprint_drift_scanned_total",
		Help: "Integrity fingerprint drift full-scan cycles actually executed (i.e. not short-circuited away by the one-shot fingerprint existence probe).",
	})

	// fingerprintDriftSkippedTotal 是被探针短路、**没有**扫描的轮数。
	fingerprintDriftSkippedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llm_gateway_bg_fingerprint_drift_skipped_total",
		Help: "Integrity fingerprint drift cycles short-circuited to a no-op because the one-shot probe memoised \"no fingerprint traffic\". A rising value with scanned_total flat means the drift detector is off.",
	})

	// fingerprintDriftLastScanUnix 是最近一次真正扫描的时刻。
	fingerprintDriftLastScanUnix = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llm_gateway_bg_fingerprint_drift_last_scan_unix",
		Help: "Unix time of the last integrity fingerprint drift full scan. Initialised to process start, because the existence probe runs at most once per process and \"never scanned since start\" is a real state after S4 stop-write.",
	})
)

func init() {
	// 预置序列：否则「worker 还没起来」与「worker 起来了但一直不扫」在告警侧
	// 都表现为「序列不存在」，无法区分。
	fingerprintDriftScannedTotal.Add(0)
	fingerprintDriftSkippedTotal.Add(0)
	fingerprintDriftLastScanUnix.Set(0)
}

// recordFingerprintDriftStart 把 last_scan 置为进程启动时刻。
func recordFingerprintDriftStart(at time.Time) {
	fingerprintDriftLastScanUnix.Set(float64(at.Unix()))
}

// recordFingerprintDriftScan 记一次**真正执行了**的扫描。
func recordFingerprintDriftScan(at time.Time) {
	fingerprintDriftScannedTotal.Inc()
	fingerprintDriftLastScanUnix.Set(float64(at.Unix()))
}

// recordFingerprintDriftSkip 记一次被短路的轮次。
func recordFingerprintDriftSkip() {
	fingerprintDriftSkippedTotal.Inc()
}

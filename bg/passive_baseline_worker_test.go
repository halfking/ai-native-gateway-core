// passive_baseline_worker_test.go — worker 的观测面契约测试。
//
// passive_baseline_worker_test.go — observability contract for the worker.
//
// ★ 为什么需要这道门：基线 worker 在**没有流量时完全静默**（Flush 无节点
// ⇒ 不打日志）⇒ 运维看到「零日志」无法区分「活着但空闲」与「没起来 /
// 已死」。心跳就是这条缺失的正面证据 ⇒ 门在这里钉住：
//  1. Start 立刻打「started」；
//  2. 空闲（零节点）也必须按节拍打「alive」。
//
// ★ 变异臂（人工对拍）：删掉 beat() 调用 ⇒ 用例 2 红；把 heartbeat 判定
//
//	改成「只在 flushed>0 时打」⇒ 用例 2 红。
package bg

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

// safeLogBuffer 是**并发安全**的日志缓冲。
//
// ★ 为什么不能直接用 bytes.Buffer：slog 默认 logger 是**进程级全局**，
// worker 在自己的 goroutine 里写日志，而测试在主 goroutine 里轮询读 ⇒
// 裸 bytes.Buffer 被并发写/读 ⇒ 2026-11-10 实测 `go test -race ./bg/`
// 报 DATA RACE（堆栈：bytes.Buffer.Write ← slog ← beat() vs
// bytes.Buffer.String ← waitForLog()）⇒ **本文件两条用例在 race 通道上恒红**。
//
// ★ 量具自身带 race ⇒ 门在 race 通道上的读数不可信
// （错的量具比没量具危险）⇒ 缓冲必须加锁。
// 形态与 domains/session/v2 的 safeBuffer 同源（mutex + strings.Builder）。
type safeLogBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// idlePassiveSink 是一个**永不写任何行**的 sink：让 Flush 恒为 0 节点，
// 从而复现「活着但空闲」这一最难观测的形态。
type idlePassiveSink struct {
	mu    sync.Mutex
	upsrt int
}

func (s *idlePassiveSink) UpsertPassiveBaseline(ctx context.Context, fp *modelcredibility.PassiveFingerprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsrt++
	return nil
}

// LoadPassiveBaseline 的 tenantID 参数是 2026-11-10 审计 P1-C 补入的
// （读路径原写死 tenant_id='default' ⇒ 非 default 租户的历史被丢弃）。
// 本替身恒返回 (nil, nil)，故 tenantID 只为对齐接口签名。
func (s *idlePassiveSink) LoadPassiveBaseline(ctx context.Context, tenantID string, credentialID int64, modelName string) (*modelcredibility.PassiveFingerprint, error) {
	return nil, nil
}

func (s *idlePassiveSink) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsrt
}

// captureLogs 把 slog 默认 logger 换成一个写进 buffer 的 JSON logger，
// 并在测试结束（含 t.Cleanup）时还原。
func captureLogs(t *testing.T) *safeLogBuffer {
	t.Helper()
	var buf safeLogBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// waitForLog 轮询直到 buffer 里出现 needle，或超时。
func waitForLog(t *testing.T, buf *safeLogBuffer, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := buf.String(); strings.Contains(s, needle) {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("log %q not observed within %s; captured:\n%s", needle, timeout, buf.String())
	return ""
}

// TestPassiveBaselineWorkerStartLogsStarted 钉住「启动正面证据」。
func TestPassiveBaselineWorkerStartLogsStarted(t *testing.T) {
	buf := captureLogs(t)
	w := NewPassiveBaselineWorker(&idlePassiveSink{}, time.Millisecond)
	w.Start(context.Background())
	defer w.Stop()

	out := waitForLog(t, buf, "passive baseline worker started", 3*time.Second)
	if !strings.Contains(out, "flush_interval") {
		t.Fatalf("started log must carry flush_interval, got: %s", out)
	}
}

// TestPassiveBaselineWorkerHeartbeatWhenIdle 是本文件的核心用例：
// **零流量**时 worker仍必须按节拍打「alive」。
//
// 零节点 ⇒ Flush 返回 0 ⇒ 「只在 flushed>0 才打日志」的实现永远静默 ⇒
// 运维无法判断 worker死活 ⇒ 这就是「空闲静默无启动日志」缺陷的钉测。
func TestPassiveBaselineWorkerHeartbeatWhenIdle(t *testing.T) {
	buf := captureLogs(t)
	sink := &idlePassiveSink{}
	w := NewPassiveBaselineWorker(sink, time.Millisecond)
	w.Start(context.Background())
	defer w.Stop()

	out := waitForLog(t, buf, "passive baseline worker alive", 10*time.Second)

	// 空闲形态：不得有任何一次落库写入（否则就不是「空闲」臂了）。
	if n := sink.writes(); n != 0 {
		t.Fatalf("idle arm must not write any baseline row, got %d writes", n)
	}
	// 心跳必须带节拍计数与 pending（运维据此判断「活着但没流量」）。
	if !strings.Contains(out, "ticks") || !strings.Contains(out, "pending") {
		t.Fatalf("alive log must carry ticks/pending, got: %s", out)
	}
}

// TestPassiveBaselineWorkerHeartbeatCounterIsPerWindow 钉住心跳计数口径：
// `nodes_written_since_last_beat` 必须真的是「**自上次心跳以来**」，而不是
// 「自 Start 以来累计」。
//
// ★ 为什么需要这道门（2026-11-10）：上游实现只累加不归零 ⇒ 字段名承诺
// 「since_last_beat」而实际报累计值 ⇒ 运维把「历史累计很大」误读成
// 「本窗口有写入」⇒ 而心跳的用途恰恰是判断**本窗口**是否活跃（空闲静默
// 正是最难观测的形态）⇒ 名与值必须一致。
//
// ★ 变异臂：把 beat() 里的 `w.flushes = 0` 去掉 ⇒ 本用例红
// （第二个心跳的计数值会等于第一个心跳的计数值，而不是 0）。
func TestPassiveBaselineWorkerHeartbeatCounterIsPerWindow(t *testing.T) {
	buf := captureLogs(t)
	// 不调 Start（不启 goroutine）：直接驱动 beat() ⇒ 计数完全确定。
	w := NewPassiveBaselineWorker(&idlePassiveSink{}, time.Hour)

	// 第一个心跳窗口：每次 Flush 写 5 个节点 ⇒ 窗口合计 5×ticks。
	for i := 0; i < PassiveBaselineHeartbeatTicks; i++ {
		w.beat("unit-window-1", 5)
	}
	// 第二个心跳窗口：一个节点都没写（空闲形态）。
	for i := 0; i < PassiveBaselineHeartbeatTicks; i++ {
		w.beat("unit-window-2", 0)
	}

	first := heartbeatCounter(t, buf.String(), "unit-window-1")
	second := heartbeatCounter(t, buf.String(), "unit-window-2")

	wantFirst := float64(5 * PassiveBaselineHeartbeatTicks)
	if first != wantFirst {
		t.Fatalf("first heartbeat must report %v nodes (window total), got %v", wantFirst, first)
	}
	if second != 0 {
		t.Fatalf("second heartbeat must report 0 (counter resets per window), got %v", second)
	}
}

// heartbeatCounter 从捕获的 JSON 日志里取出指定 why 的心跳记录，返回
// nodes_written_since_last_beat 的数值。
//
// ★ 用 why 做定位而不是「取最后一条」：同包其它用例的 worker 也会打心跳
// （日志是进程级全局）⇒ 按 why 过滤才能保证读数确定性。
func heartbeatCounter(t *testing.T, out, why string) float64 {
	t.Helper()
	var found []float64
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] != "passive baseline worker alive" || rec["why"] != why {
			continue
		}
		v, ok := rec["nodes_written_since_last_beat"].(float64)
		if !ok {
			t.Fatalf("heartbeat log must carry numeric nodes_written_since_last_beat: %s", line)
		}
		found = append(found, v)
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 heartbeat for why=%s, got %d; logs:\n%s", why, len(found), out)
	}
	return found[0]
}

// TestPassiveBaselineWorkerHeartbeatTicksConstant 钉住节拍常量：
// 心跳窗口 = ticks × flush_interval，必须是可预期的有限值（不是无限静默）。
func TestPassiveBaselineWorkerHeartbeatTicksConstant(t *testing.T) {
	if PassiveBaselineHeartbeatTicks <= 0 {
		t.Fatalf("heartbeat ticks must be positive, got %d", PassiveBaselineHeartbeatTicks)
	}
	if PassiveBaselineFlushInterval <= 0 {
		t.Fatalf("flush interval must be positive, got %v", PassiveBaselineFlushInterval)
	}
	window := time.Duration(PassiveBaselineHeartbeatTicks) * PassiveBaselineFlushInterval
	if window > time.Hour {
		t.Fatalf("heartbeat window %v too long for operability", window)
	}
}

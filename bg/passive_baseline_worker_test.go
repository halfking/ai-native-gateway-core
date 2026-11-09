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
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

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
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// waitForLog 轮询直到 buffer 里出现 needle，或超时。
func waitForLog(t *testing.T, buf *bytes.Buffer, needle string, timeout time.Duration) string {
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

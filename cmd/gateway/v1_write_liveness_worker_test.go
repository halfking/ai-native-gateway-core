package main

// v1_write_liveness_worker_test.go —— 接线后的行为门（R48 §五-1 / R48-A5）。
//
// §9.264 只测了分类器；本轮接成常驻信号后，**信号策略本身**成为被测对象：
// §9.238 要惩治的失效形态是「停机期间全程无任何信号」，所以门钉的是
// 「dead 期间每 tick 都有 ERROR」——而不是「转换点响过一次」。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newTestWorker 返回接了假 probe 的 worker 和捕获日志的 buffer。
func newTestWorker(t *testing.T, verdicts func(int) (v1WriteLivenessInput, string, error)) (*V1WriteLivenessWorker, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tick := 0
	w := newV1WriteLivenessWorker(func(ctx context.Context) (v1WriteLivenessInput, string, error) {
		tick++
		return verdicts(tick)
	}, 30*time.Minute, time.Hour, logger)
	return w, &buf
}

func countLevel(buf *bytes.Buffer, level string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level="+level+" ") {
			n++
		}
	}
	return n
}

// 门 1：dead 期间**每 tick** 一条 ERROR。这是对 §9.238「5 天零信号」的
// 直接否定——只响一次的信号在长停机里和没响过一样。
func TestV1WriteLivenessWorker_DeadSignalsEveryTick(t *testing.T) {
	w, buf := newTestWorker(t, func(int) (v1WriteLivenessInput, string, error) {
		return v1WriteLivenessInput{TurnsHotRows: 5}, livenessDead, nil
	})
	w.tick(context.Background())
	w.tick(context.Background())
	w.tick(context.Background())
	if got := countLevel(buf, "ERROR"); got != 3 {
		t.Fatalf("3 个 dead tick 应产生 3 条 ERROR（持续信号），得到 %d 条。日志：\n%s", got, buf.String())
	}
	snap := w.Snapshot()
	if snap.Verdict != livenessDead || snap.ConsecutiveDead != 3 {
		t.Fatalf("快照应记 verdict=dead 且 consecutive_dead_ticks=3，得到 %+v", snap)
	}
	if snap.TurnsHotRows != 5 || snap.WindowMinutes != 30 {
		t.Fatalf("快照应携带行数与窗口，得到 %+v", snap)
	}
}

// 门 2：恢复只在离开 dead 的那一 tick 响一次 WARN；steady alive 静默。
func TestV1WriteLivenessWorker_RecoverySignalsOnceThenSilent(t *testing.T) {
	w, buf := newTestWorker(t, func(tick int) (v1WriteLivenessInput, string, error) {
		if tick <= 2 {
			return v1WriteLivenessInput{TurnsHotRows: 5}, livenessDead, nil
		}
		return v1WriteLivenessInput{V1HotRows: 9}, livenessAlive, nil
	})
	w.tick(context.Background()) // dead
	w.tick(context.Background()) // dead
	w.tick(context.Background()) // alive ← 恢复
	w.tick(context.Background()) // alive steady
	if got := countLevel(buf, "ERROR"); got != 2 {
		t.Fatalf("前两个 dead tick 应有 2 条 ERROR，得到 %d", got)
	}
	if got := countLevel(buf, "WARN"); got != 1 {
		t.Fatalf("恢复应恰好一条 WARN（第 4 个 steady tick 不得再响），得到 %d 条。日志：\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "recovered from dead") {
		t.Fatalf("恢复 WARN 应含 recovered from dead，日志：\n%s", buf.String())
	}
	if snap := w.Snapshot(); snap.Verdict != livenessAlive || snap.ConsecutiveDead != 0 {
		t.Fatalf("恢复后快照应为 alive 且清零 dead 计数，得到 %+v", snap)
	}
}

// 门 3：quiet 静默——「没人用」不是事件，报警会被忽略，然后真死也一起被忽略
// （v1_write_liveness.go 对三值不合并的同一理由，作用在信号层）。
func TestV1WriteLivenessWorker_QuietIsSilent(t *testing.T) {
	w, buf := newTestWorker(t, func(int) (v1WriteLivenessInput, string, error) {
		return v1WriteLivenessInput{}, livenessQuiet, nil
	})
	w.tick(context.Background())
	w.tick(context.Background())
	if buf.Len() != 0 {
		t.Fatalf("steady quiet 应零输出，得到：\n%s", buf.String())
	}
	if snap := w.Snapshot(); snap.Verdict != livenessQuiet {
		t.Fatalf("快照应记 quiet，得到 %+v", snap)
	}
}

// 门 4：测量失败 → 快照 unknown + 每 tick WARN。unknown 是快照层第 4 值，
// 分类器永远不返回它。
func TestV1WriteLivenessWorker_MeasurementFailureIsUnknownAndWarned(t *testing.T) {
	w, buf := newTestWorker(t, func(int) (v1WriteLivenessInput, string, error) {
		return v1WriteLivenessInput{}, "", errors.New("connection refused")
	})
	w.tick(context.Background())
	w.tick(context.Background())
	if got := countLevel(buf, "WARN"); got != 2 {
		t.Fatalf("两个失败 tick 应各一条 WARN，得到 %d。日志：\n%s", got, buf.String())
	}
	snap := w.Snapshot()
	if snap.Verdict != livenessUnknown {
		t.Fatalf("测量失败快照应 unknown，得到 %+v", snap)
	}
	if !strings.Contains(snap.LastError, "connection refused") {
		t.Fatalf("快照应携带 last_error，得到 %+v", snap)
	}
}

// 门 5：端点状态码——dead→503（curl -f 族零解析告警），alive/quiet/unknown→200，
// 非 GET→405。body 恒为 JSON 快照。
func TestV1WriteLivenessHandler_StatusByVerdict(t *testing.T) {
	cases := []struct {
		verdict string
		want    int
	}{
		{livenessDead, http.StatusServiceUnavailable},
		{livenessAlive, http.StatusOK},
		{livenessQuiet, http.StatusOK},
		{livenessUnknown, http.StatusOK},
	}
	for _, tc := range cases {
		w, _ := newTestWorker(t, func(int) (v1WriteLivenessInput, string, error) {
			return v1WriteLivenessInput{V1HotRows: 3}, tc.verdict, nil
		})
		w.tick(context.Background())
		rec := httptest.NewRecorder()
		NewV1WriteLivenessHandler(w).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != tc.want {
			t.Fatalf("verdict=%s 应 %d，得到 %d", tc.verdict, tc.want, rec.Code)
		}
		var snap V1WriteLivenessSnapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
			t.Fatalf("body 应为 JSON 快照：%v（%s）", err, rec.Body.String())
		}
		if snap.Verdict != tc.verdict || snap.V1HotRows != 3 {
			t.Fatalf("body 快照字段漂移：%+v", snap)
		}
	}

	// 方法守卫
	w, _ := newTestWorker(t, func(int) (v1WriteLivenessInput, string, error) {
		return v1WriteLivenessInput{}, livenessAlive, nil
	})
	rec := httptest.NewRecorder()
	NewV1WriteLivenessHandler(w).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应 405，得到 %d", rec.Code)
	}
}

// 门 6：接线守卫——R48-A5 的原始发现就是「分类器存在但全仓无调用点」，
// 本门钉住 main.go 里的三点接线（构造+启动、路由注册、优雅停机），
// 并确认接线不在 cred-recovery / data-plane 任一配置门之内：
// §9.238 的失效恰恰发生在「没人主动去看」的组合态。
func TestV1WriteLivenessWorkerIsWiredInMain(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	const construct = "NewV1WriteLivenessWorker(dbConn.Pool()"
	idx := strings.Index(text, construct)
	if idx < 0 {
		t.Fatal("main.go: NewV1WriteLivenessWorker(dbConn.Pool() 构造缺失——v1 写入腿存活信号又回到了零调用点状态（R48-A5 复发）")
	}
	// 构造的紧邻区域必须是裸 dbConn 门：不得夹带 bgDataPlaneOnly /
	// IsCredRecoveryDisabled / shouldStartNewProbeWorkers 任何一个。
	region := text[max(0, idx-500):idx]
	if !strings.Contains(region, "if dbConn != nil && dbConn.Enabled() {") {
		t.Fatal("main.go: v1 写入腿存活 worker 的接线不在独立的 `if dbConn != nil && dbConn.Enabled()` 块内")
	}
	for _, gate := range []string{"bgDataPlaneOnly", "IsCredRecoveryDisabled", "shouldStartNewProbeWorkers"} {
		if strings.Contains(region, gate) {
			t.Fatalf("main.go: v1 写入腿存活 worker 被夹进 %s 配置门——该门关闭的组合态恰是 §9.238 零信号复发的地方", gate)
		}
	}
	if !strings.Contains(text, `"/internal/v1-write-liveness"`) {
		t.Fatal("main.go: /internal/v1-write-liveness 路由注册缺失（admin 拉取面断线）")
	}
	if !strings.Contains(text, "v1WriteLivenessWorker.Stop()") {
		t.Fatal("main.go: 关停序列缺 v1WriteLivenessWorker.Stop()（优雅停机断线）")
	}
}

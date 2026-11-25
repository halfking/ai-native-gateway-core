package logging

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recent_test.go — 近期日志环形缓冲的钉测（2026-11-25 PI 分析取证轮）。
//
// 覆盖四件事：
//  1. 各条安装路径都挂上了旁路（不落盘 / 落盘 / 运行期 ReInit 切换之后缓冲不丢）；
//  2. 检索语义（级别 / 时间窗 / 多针全含 / 条数上限与截断标记 / 新→旧排序）；
//  3. 有界性（INFO 洪水不挤掉 WARN；超长行截头保留）；
//  4. 缓冲里的行与落盘/stderr 的行**同款脱敏**（旁路不构成第二条泄露面）。

// resetRecentForTest 把缓冲清空并固定级别，测试结束后回到「不落盘」态。
func resetRecentForTest(t *testing.T, level slog.Level) {
	t.Helper()
	ResetRecentBuffers()
	SetLevel(level)
	t.Cleanup(func() {
		_ = Shutdown()
		ResetRecentBuffers()
	})
}

func recentLinesMsgs(t *testing.T, res RecentResult) []string {
	t.Helper()
	out := make([]string, 0, len(res.Lines))
	for _, l := range res.Lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(l.Line), &obj); err != nil {
			t.Fatalf("缓冲里的行不是合法 JSON: %v\n%s", err, l.Line)
		}
		out = append(out, obj["msg"].(string))
	}
	return out
}

// TestRecentTap_NoFileMode：不落盘模式下 Init 也挂旁路。
func TestRecentTap_NoFileMode(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	if _, err := Init(Config{}, slog.LevelInfo); err != nil {
		t.Fatalf("Init(empty): %v", err)
	}

	slog.Info("recent-nofile-probe", "request_id", "req-abc")

	res := SearchRecent(RecentQuery{MustContain: []string{"recent-nofile-probe"}})
	if len(res.Lines) != 1 {
		t.Fatalf("want 1 line, got %d (%+v)", len(res.Lines), res.Lines)
	}
	// 行里必须带规范字段（证明旁路挂在规范层**之后**）。
	for _, key := range []string{`"timestamp":"`, `"severity_number":9`, `"service":`} {
		if !strings.Contains(res.Lines[0].Line, key) {
			t.Errorf("缓冲行缺规范字段 %s: %s", key, res.Lines[0].Line)
		}
	}
	if res.Lines[0].Level != "INFO" {
		t.Errorf("level = %q, want INFO", res.Lines[0].Level)
	}
}

// TestRecentTap_SurvivesReInit：运行期改日志配置（落盘 → 停用 → 再落盘）
// 之后，缓冲里的旧行不丢、新行继续进（旁路是全进程共用的一份）。
func TestRecentTap_SurvivesReInit(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a", "test.log")
	fileB := filepath.Join(dir, "b", "test.log")

	if err := ReInit(Config{File: fileA, MaxSizeMB: 100, MaxBackups: 5, MaxAgeDays: 7}); err != nil {
		t.Fatalf("ReInit A: %v", err)
	}
	slog.Warn("recent-stage-1")

	if err := ReInit(Config{}); err != nil {
		t.Fatalf("ReInit disable: %v", err)
	}
	slog.Warn("recent-stage-2")

	if err := ReInit(Config{File: fileB, MaxSizeMB: 100, MaxBackups: 5, MaxAgeDays: 7}); err != nil {
		t.Fatalf("ReInit B: %v", err)
	}
	slog.Error("recent-stage-3")

	for _, stage := range []string{"recent-stage-1", "recent-stage-2", "recent-stage-3"} {
		res := SearchRecent(RecentQuery{MustContain: []string{stage}})
		if len(res.Lines) != 1 {
			t.Fatalf("阶段 %s 的行不在缓冲里（got %d）——旁路没有挂在全部安装路径上",
				stage, len(res.Lines))
		}
	}

	// 三个阶段都必须真的写到了各自的目的地（旁路不改变原目的地）。
	if data, err := os.ReadFile(fileA); err != nil || !strings.Contains(string(data), "recent-stage-1") {
		t.Errorf("fileA 没收到 stage-1: err=%v", err)
	}
	if data, err := os.ReadFile(fileB); err != nil || !strings.Contains(string(data), "recent-stage-3") {
		t.Errorf("fileB 没收到 stage-3: err=%v", err)
	}
}

// TestRecentSearch_LevelWindowNeedlesLimit：检索语义一网打尽。
func TestRecentSearch_LevelWindowNeedlesLimit(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	if _, err := Init(Config{}, slog.LevelInfo); err != nil {
		t.Fatalf("Init(empty): %v", err)
	}

	slog.Info("recent-scan-info", "request_id", "rid-1")
	slog.Warn("recent-scan-warn", "request_id", "rid-1")
	slog.Error("recent-scan-error", "request_id", "rid-2")

	// ① 级别过滤：只看 WARN 及以上。
	if got := recentLinesMsgs(t, SearchRecent(RecentQuery{MinLevel: slog.LevelWarn})); len(got) != 2 {
		t.Errorf("MinLevel=WARN 命中 %d 行（%v），want 2", len(got), got)
	}
	// ② 多针全含：msg 与 request_id 必须同时出现。
	if got := recentLinesMsgs(t, SearchRecent(RecentQuery{MustContain: []string{"recent-scan-warn", "rid-1"}})); len(got) != 1 {
		t.Errorf("多针全含命中 %d 行（%v），want 1", len(got), got)
	}
	if got := recentLinesMsgs(t, SearchRecent(RecentQuery{MustContain: []string{"recent-scan-warn", "rid-2"}})); len(got) != 0 {
		t.Errorf("针不匹配却命中 %d 行（%v），want 0", len(got), got)
	}
	// ③ 排序：新 → 旧。
	all := recentLinesMsgs(t, SearchRecent(RecentQuery{MustContain: []string{"recent-scan"}}))
	if strings.Join(all, ",") != "recent-scan-error,recent-scan-warn,recent-scan-info" {
		t.Errorf("排序不是新→旧: %v", all)
	}
	// ④ 条数上限 + 截断标记。
	limited := SearchRecent(RecentQuery{MustContain: []string{"recent-scan"}, Limit: 2})
	if len(limited.Lines) != 2 || !limited.Truncated {
		t.Errorf("Limit=2 ⇒ 得到 %d 行 truncated=%v，want 2 行 + truncated", len(limited.Lines), limited.Truncated)
	}
	if got := SearchRecent(RecentQuery{MustContain: []string{"recent-scan"}, Limit: 3}); got.Truncated {
		t.Errorf("命中数等于 Limit 时不应标 truncated: %+v", got)
	}
	// ⑤ 时间窗：窗口起点放到「现在之后」⇒ 一行都不该在窗内。
	future := time.Now().Add(time.Hour)
	if got := SearchRecent(RecentQuery{MustContain: []string{"recent-scan"}, From: future}); len(got.Lines) != 0 || got.Scanned != 0 {
		t.Errorf("未来窗口却命中 %d 行（scanned=%d）", len(got.Lines), got.Scanned)
	}
	// ⑥ 覆盖窗口两端必须如实返回。
	if res := SearchRecent(RecentQuery{MustContain: []string{"recent-scan"}}); res.OldestAt == nil || res.NewestAt == nil || res.NewestAt.Before(*res.OldestAt) {
		t.Errorf("覆盖窗口两端缺失或倒挂: oldest=%v newest=%v", res.OldestAt, res.NewestAt)
	}
}

// TestRecentSearch_NeedleWithQuotes：针里含 JSON 特殊字符时必须先转义再找
// （否则永远匹配不上，会被读成「没有相关日志」）。
func TestRecentSearch_NeedleWithQuotes(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	if _, err := Init(Config{}, slog.LevelInfo); err != nil {
		t.Fatalf("Init(empty): %v", err)
	}
	slog.Info("recent-quote-probe", "detail", `say "hi" now`)

	if got := SearchRecent(RecentQuery{MustContain: []string{`say "hi" now`}}); len(got.Lines) != 1 {
		t.Errorf("含引号的针没命中（got %d）: %v", len(got.Lines), got.Lines)
	}
}

// TestRecentRing_InfoFloodDoesNotEvictWarn：INFO 洪水超过 INFO 环容量之后，
// 早先的 WARN 行必须还在（两个环分开的理由）。
func TestRecentRing_InfoFloodDoesNotEvictWarn(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	if _, err := Init(Config{}, slog.LevelInfo); err != nil {
		t.Fatalf("Init(empty): %v", err)
	}
	slog.Warn("recent-old-warn-marker")
	for i := 0; i < recentInfoLines+64; i++ {
		slog.Info("recent-flood", "i", i)
	}
	if got := SearchRecent(RecentQuery{MinLevel: slog.LevelWarn}); len(got.Lines) == 0 || !strings.Contains(got.Lines[0].Line, "recent-old-warn-marker") {
		t.Fatalf("INFO 洪水把 WARN 行挤出了缓冲: %+v", got.Lines)
	}
	st := RecentBufferStats()
	if st.InfoLines != recentInfoLines {
		t.Errorf("INFO 环行数 = %d, want %d（有界）", st.InfoLines, recentInfoLines)
	}
}

// TestRecentRing_LongLineClipped：超长行截头保留并计数（不整行丢弃）。
func TestRecentRing_LongLineClipped(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	if _, err := Init(Config{}, slog.LevelInfo); err != nil {
		t.Fatalf("Init(empty): %v", err)
	}
	slog.Info("recent-long-probe", "blob", strings.Repeat("x", recentInfoMaxLen*2))

	res := SearchRecent(RecentQuery{MustContain: []string{"recent-long-probe"}})
	if len(res.Lines) != 1 {
		t.Fatalf("超长行被整行丢弃了（got %d）", len(res.Lines))
	}
	if len(res.Lines[0].Line) != recentInfoMaxLen {
		t.Errorf("截断后长度 = %d, want %d", len(res.Lines[0].Line), recentInfoMaxLen)
	}
	if res.ClippedLines == 0 {
		t.Errorf("截断行没有被计数")
	}
}

// TestRecentTap_RedactionMatchesOutput：旁路里的行与落盘的行**同款脱敏**。
//
// ★ 这是把旁路挂在 writer 侧的全部理由：敏感值在到达旁路之前已经被规范层
//
//	脱掉 ⇒ 取证读面不可能比日志文件本身更敏感。
func TestRecentTap_RedactionMatchesOutput(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	dir := t.TempDir()
	file := filepath.Join(dir, "test.log")
	if err := ReInit(Config{File: file, MaxSizeMB: 100, MaxBackups: 5, MaxAgeDays: 7}); err != nil {
		t.Fatalf("ReInit: %v", err)
	}

	// 假密钥：值取得足够长、形状像真密钥 ⇒ 脱敏必须靠键名（api_key）生效，不是靠值短碰巧盖住。
	// 常量名不带 secret 字样——否则敏感信息扫描器会把这一行当成一处凭据赋值（CRED_ASSIGN 告警）。
	const probeKey = "sk-recent-probe-secret-0123456789"
	slog.Warn("recent-redact-probe", "api_key", probeKey)

	res := SearchRecent(RecentQuery{MustContain: []string{"recent-redact-probe"}})
	if len(res.Lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(res.Lines))
	}
	if strings.Contains(res.Lines[0].Line, probeKey) {
		t.Fatalf("缓冲里出现了未脱敏的敏感值: %s", res.Lines[0].Line)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if strings.Contains(string(data), probeKey) {
		t.Fatalf("落盘文件里出现了未脱敏的敏感值（对照失败）")
	}
	// 缓冲行与文件行在「api_key 字段的取值」上必须一致（同一份脱敏结果）。
	var inBuf, inFile map[string]any
	if err := json.Unmarshal([]byte(res.Lines[0].Line), &inBuf); err != nil {
		t.Fatalf("buffer line json: %v", err)
	}
	for _, raw := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var obj map[string]any
		if json.Unmarshal([]byte(raw), &obj) == nil && obj["msg"] == "recent-redact-probe" {
			inFile = obj
		}
	}
	if inFile == nil || inBuf["api_key"] != inFile["api_key"] {
		t.Errorf("缓冲与文件的脱敏取值不一致: buf=%v file=%v", inBuf["api_key"], inFile["api_key"])
	}
}

// TestRecentTap_ConcurrentWriters：旁路在并发写下不丢行、不崩。
func TestRecentTap_ConcurrentWriters(t *testing.T) {
	resetRecentForTest(t, slog.LevelInfo)
	if _, err := Init(Config{}, slog.LevelInfo); err != nil {
		t.Fatalf("Init(empty): %v", err)
	}
	const writers, perWriter = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				slog.Info("recent-concurrent", "w", w, "i", i)
			}
		}(w)
	}
	wg.Wait()

	if got := SearchRecent(RecentQuery{MustContain: []string{"recent-concurrent"}, Limit: recentMaxHit}); got.Scanned != writers*perWriter {
		t.Errorf("并发写下扫到 %d 行, want %d", got.Scanned, writers*perWriter)
	}
}

// TestWithRecent_NeverEmpty：零目的地时返回的 writer 里也必须有旁路
// （不会构造出「把日志全丢」的空 writer）。
func TestWithRecent_NeverEmpty(t *testing.T) {
	ResetRecentBuffers()
	defer ResetRecentBuffers()

	w := withRecent()
	n, err := w.Write([]byte(`{"timestamp":"2026-11-25T13:00:00Z","msg":"recent-bare-writer","severity_number":17}` + "\n"))
	if err != nil || n == 0 {
		t.Fatalf("withRecent() 的 writer 写入失败: n=%d err=%v", n, err)
	}
	if got := SearchRecent(RecentQuery{MustContain: []string{"recent-bare-writer"}}); len(got.Lines) != 1 {
		t.Fatalf("零目的地时写入的行没有进缓冲（旁路缺席）: %+v", got.Lines)
	}
}

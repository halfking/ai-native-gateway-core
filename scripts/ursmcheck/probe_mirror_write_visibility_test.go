package ursm_test

// probe_mirror_write_visibility_test.go —— R149 轮（2026-10-27）新增门。
//
// ── 为什么有这道门 ───────────────────────────────────────────────────────────
//
// R149 轮把「No available provider」这条客户端消息背后的机制钉死时，量到了一条
// **极关键**的错配（本地 PG + 本地 8782 网关实测）：
//
//	node_probe_state（provider 2453 / credential 1 / deepseek-v4-flash）：
//	  next_retry_seconds = 300            ← 「看到的退避」
//	  next_retry_at − updated_at = 00:00:45 ← ★「生效的退避」（= pump 的 45 秒投递 holdoff）
//
// ⇒ ★★ **路由闸门读的那一列（next_retry_at）实际只被推进 45 秒，而运维读的
//   那一列（next_retry_seconds）写着 300 秒 ⇒ 「看到的」与「生效的」相差 6.7 倍。**
//
// ⇒ ★★ 而**探针失败的结果本该由 mirrorNodeProbeState 写进那一列** ——
//   该函数两处 `w.db.Exec(...)` 都写成 `_, _ = …` ⇒ **写失败被静默吞掉** ⇒
//   「探针跑了、失败了、但退避没落地」这件事**没有任何痕迹**（既没有日志，也没有指标）。
//
// ⇒ ⇒ **本门的作用**：钉死「探针结果回写不得吞掉错误」这条纪律，
//   并让「回写失败」至少在服务端日志里可见（★ 本轮只加日志，**不改任何路由行为**）。
//
// ── 判据 ───────────────────────────────────────────────────────────────────
//
//	① mirrorNodeProbeState 函数体里**不得**出现 `_, _ =` 形式的 Exec 吞错；
//	② 成功分支与失败分支**都**必须对 Exec 的错误留痕（slog.Warn/WarnContext）。
//
// ★ 阳性对照：喂一段故意吞错的合成片段 ⇒ 检测器必须报出来；
// ★ 阴性对照：喂一段合规片段 ⇒ 检测器不得误报。

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	probeMirrorFile = "bg/probe_service.go"
	probeMirrorFunc = "func (w *NodeProbeWorker) mirrorNodeProbeState("
)

// probeMirrorRepoRoot 定位仓根（本文件在 scripts/ursmcheck/ 下 ⇒ 往上两级）。
func probeMirrorRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// probeMirrorFuncBody 抽出 mirrorNodeProbeState 的函数体（从声明行到下一个顶层 `func`）。
func probeMirrorFuncBody(t *testing.T, src string) string {
	t.Helper()
	idx := strings.Index(src, probeMirrorFunc)
	if idx < 0 {
		t.Fatalf("%s 里找不到 %s", probeMirrorFile, probeMirrorFunc)
	}
	rest := src[idx:]
	// 下一个顶层 func 声明即为函数体结束（缩进为 0 的 "func "）。
	rest = rest[len(probeMirrorFunc):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// probeMirrorSwallowedExecs 找出函数体里「吞掉 Exec 错误」的行。
func probeMirrorSwallowedExecs(body string) []string {
	var hits []string
	for i, ln := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(trimmed, "_, _ =") && strings.Contains(trimmed, "Exec(") {
			hits = append(hits, "行"+strconv.Itoa(i+1)+": "+trimmed)
		}
	}
	return hits
}

// probeMirrorHasWarnOnBranch 判断函数体里是否出现了带指定关键字的告警日志。
func probeMirrorHasWarnOnBranch(body, keyword string) bool {
	return strings.Contains(body, keyword)
}

func TestProbeMirrorWriteMustNotSwallowExecError(t *testing.T) {
	root := probeMirrorRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, probeMirrorFile))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", probeMirrorFile, err)
	}
	body := probeMirrorFuncBody(t, string(raw))

	if hits := probeMirrorSwallowedExecs(body); len(hits) > 0 {
		t.Errorf("mirrorNodeProbeState 仍在吞掉 Exec 的错误（探针结果回写失败将无痕 ⇒ 路由闸门退避被架空）：\n  %s",
			strings.Join(hits, "\n  "))
	}
}

func TestProbeMirrorWriteFailureMustBeLogged(t *testing.T) {
	root := probeMirrorRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, probeMirrorFile))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", probeMirrorFile, err)
	}
	body := probeMirrorFuncBody(t, string(raw))

	for _, kw := range []string{
		"mirror success write failed",
		"mirror failure write failed",
	} {
		if !probeMirrorHasWarnOnBranch(body, kw) {
			t.Errorf("mirrorNodeProbeState 缺少 %q 的留痕 —— 写失败必须可见（★ 本轮只加日志，不改路由行为）", kw)
		}
	}
}

// TestProbeMirrorDetectorHasTeeth 是阳性对照：故意吞错的合成片段必须被检测器报出。
func TestProbeMirrorDetectorHasTeeth(t *testing.T) {
	synthetic := `
func (w *NodeProbeWorker) mirrorNodeProbeState(ctx context.Context) {
	_, _ = w.db.Exec(ctx, "UPDATE node_probe_state SET x = 1")
}
`
	if hits := probeMirrorSwallowedExecs(synthetic); len(hits) == 0 {
		t.Fatal("检测器漏报了吞错的合成片段 ⇒ 主判据不可信")
	}
}

// TestProbeMirrorDetectorNoFalsePositive 是阴性对照：合规片段不得被误报。
func TestProbeMirrorDetectorNoFalsePositive(t *testing.T) {
	compliant := `
func (w *NodeProbeWorker) mirrorNodeProbeState(ctx context.Context) {
	if _, err := w.db.Exec(ctx, "UPDATE node_probe_state SET x = 1"); err != nil {
		slog.WarnContext(ctx, "mirror success write failed", "error", err)
	}
}
`
	if hits := probeMirrorSwallowedExecs(compliant); len(hits) != 0 {
		t.Fatalf("检测器误报了合规片段: %v", hits)
	}
}

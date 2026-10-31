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
//   并让「回写失败」至少在服务端日志里可见。
//
// ── ★★ 2026-10-29 审计轮修订（★ 本门曾因「门与实现同型漂移」而误报） ──────────
//
// P1-D 轮（`a0860723b`，2026-10-28 19:51）把两处内联的 Exec **提取成了公共包装**
// `execMirrorWrite(ctx, branch, summary, exec)`，并把留痕升级为
// 「指标（failures/streak/retries）+ 有限重试（1 次 in-cycle）+ 分级 ERROR」。
//
// ⇒ ★★ **本门的判据却还停在旧形态**：它在 `mirrorNodeProbeState` 的**函数体**里
//   找两个字面量 `"mirror success write failed"` / `"mirror failure write failed"`
//   ⇒ 而这两个字面量在新实现里**已经不存在了**（新消息是动态拼的
//   `"node_probe_worker: mirror " + branch + " write failed after immediate retry"`）
//   ⇒ ⇒ **门红了 ⇒ 但写失败其实比改之前【更可见】** ⇒ ⇒ **这是假阳性**。
//
// ⇒ ⇒ **本轮把判据从「旧实现的字面量」改成【不变式】**：
//
//	① mirrorNodeProbeState 的**成功分支与失败分支都必须经由 execMirrorWrite 落账**
//	   （★ 直接内联 Exec 而不走包装 ⇒ 门红）；
//	② execMirrorWrite 必须让写失败**可见**：日志调用 + 把写错误交给日志 + 失败计数递增
//	   （★ 只吞不报 / 只报不计量 ⇒ 门红）。
//
// ★★ **不变式比字面量强**：旧判据只要那两个字面量在就放行（哪怕写错误其实被吞了）；
//   新判据要求**两个分支都走包装**且**包装真的报错**⇒ ⇒ 同型回归照样被抓住。
//
// ★ 阳性对照：故意吞错的合成片段 / 只记日志不计量 / 失败分支绕过包装 ⇒ 检测器必须报出；
// ★ 阴性对照：合规片段 ⇒ 检测器不得误报。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	probeMirrorFile = "bg/probe_service.go"
	probeMirrorFunc = "func (w *NodeProbeWorker) mirrorNodeProbeState("
	// ★ 2026-10-29 审计轮：P1-D 轮提取出的公共包装（★ 写失败留痕现在发生在这里）。
	probeMirrorExecFunc = "func (w *NodeProbeWorker) execMirrorWrite("
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

// probeMirrorFuncBodyOf 抽出某个顶层函数声明的函数体（从声明行到下一个顶层 `func`）。
func probeMirrorFuncBodyOf(t *testing.T, src, decl string) string {
	t.Helper()
	idx := strings.Index(src, decl)
	if idx < 0 {
		t.Fatalf("%s 里找不到 %s", probeMirrorFile, decl)
	}
	rest := src[idx:]
	rest = rest[len(decl):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// probeMirrorFuncBody 抽出 mirrorNodeProbeState 的函数体。
func probeMirrorFuncBody(t *testing.T, src string) string {
	t.Helper()
	return probeMirrorFuncBodyOf(t, src, probeMirrorFunc)
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

// probeMirrorRoutedBranches 返回「经由 execMirrorWrite 落账的分支标签」集合。
//
// ★ 用它来钉死不变式 ①：成功分支与失败分支**都**必须走包装。
func probeMirrorRoutedBranches(body string) map[string]bool {
	out := map[string]bool{}
	re := regexp.MustCompile(`execMirrorWrite\([^)]*?"([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

// probeMirrorFailureVisible 判断「写失败是否可见」。★ 三条同时成立才算可见：
//
//	(a) metered  —— 存在失败计数递增（★ 只进日志不进指标 ⇒ 告警系统看不见）；
//	(b) logged   —— ★ **每一次**失败计数递增之后，都紧跟一条日志
//	                  （★ 只看「函数体里有没有日志」是不够的：★ 别的分支上的
//	                   一条日志会让「主失败路径已经不报」也照样通过 ⇒ 那是假绿）；
//	(c) errPassed—— 那条日志必须把**写错误本身**带出去
//	                  （★ 否则日志只说「失败了」却说不出错在哪）。
//
// ★★ 变异台证据：★ 只把主失败路径的 slog.Log 删掉（保留 ctx.Done 分支的那条）⇒
// ★★ 本检测器必须报红（★ 这正是 2026-10-29 审计轮实测出来的「假绿」形态）。
func probeMirrorFailureVisible(body string) (logged bool, errPassed bool, metered bool) {
	lines := strings.Split(body, "\n")

	incRe := regexp.MustCompile(`nodeProbeMirrorWriteFailuresTotal[^\n]*\.Inc\(\)`)
	logRe := regexp.MustCompile(`slog\.(Error|ErrorContext|Warn|WarnContext|Log)\(`)
	// ★ "write_error", err.Error() / "error", err 两种写法都要认。
	errRe := regexp.MustCompile(`"[^"]*error[^"]*",\s*err`)

	incs := 0
	logged = true
	errPassed = true
	for i, ln := range lines {
		if !incRe.MatchString(ln) {
			continue
		}
		incs++
		foundLog := false
		foundErr := false
		// ★ 从这次递增往后看：★ 直到 return / 语句块结束（或 12 行上限）。
		for j := i + 1; j < len(lines) && j <= i+12; j++ {
			trimmed := strings.TrimSpace(lines[j])
			if trimmed == "return" || strings.HasPrefix(trimmed, "return ") {
				break
			}
			if !logRe.MatchString(lines[j]) {
				continue
			}
			foundLog = true
			// ★ 日志调用可能是多行的 ⇒ 参数区再往后扫 8 行找错误参数。
			for k := j; k < len(lines) && k <= j+8; k++ {
				if errRe.MatchString(lines[k]) {
					foundErr = true
					break
				}
			}
		}
		if !foundLog {
			logged = false
		}
		if !foundErr {
			errPassed = false
		}
	}
	metered = incs > 0
	if !metered {
		logged = false
		errPassed = false
	}
	return logged, errPassed, metered
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

// TestProbeMirrorWriteFailureMustBeLogged —— ★★ 写失败必须【可见】。
//
// ★★ 2026-10-29 审计轮修订：★ 判据从「旧实现的两个字面量」改成【不变式】——
// ★★ ① 两个分支都经由 execMirrorWrite 落账；② 包装里写失败必须日志+计量。
func TestProbeMirrorWriteFailureMustBeLogged(t *testing.T) {
	root := probeMirrorRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, probeMirrorFile))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", probeMirrorFile, err)
	}
	src := string(raw)

	// ── ① 两个分支都必须经由公共包装落账 ──
	mirrorBody := probeMirrorFuncBody(t, src)
	routed := probeMirrorRoutedBranches(mirrorBody)
	for _, branch := range []string{"success", "failure"} {
		if !routed[branch] {
			t.Errorf("mirrorNodeProbeState 的 %q 分支没有经由 execMirrorWrite 落账 ⇒ ★ 写失败可能无痕（写回是这条路径唯一的后端落地动作）",
				branch)
		}
	}

	// ── ② 包装里写失败必须可见（日志 + 错误 + 计量）──
	execBody := probeMirrorFuncBodyOf(t, src, probeMirrorExecFunc)
	logged, errPassed, metered := probeMirrorFailureVisible(execBody)
	if !metered {
		t.Errorf("execMirrorWrite 没有递增失败计数（nodeProbeMirrorWriteFailuresTotal）⇒ ★ 写失败只进日志不进指标 ⇒ 告警系统看不见 ⇒ 不算「可见」")
	}
	if !logged {
		t.Errorf("execMirrorWrite 有失败计数递增、但其后没有日志 ⇒ ★ 写失败不可见（★ 别的分支上有日志不算 ⇒ 主失败路径可能已经哑了）")
	}
	if !errPassed {
		t.Errorf("execMirrorWrite 的日志没有带上写错误本身 ⇒ ★ 只说「失败了」却说不出错在哪（★ 排障无从下手）")
	}
}

// ─────────────────────────── 变异台（★ 检测器必须有牙） ───────────────────────────

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

// TestProbeMirrorBranchRoutingDetectorHasTeeth —— ★ 阳性对照：失败分支绕过包装必须被报出。
//
// ★★ 这是 2026-10-29 审计轮新增的那条不变式的「牙」：★ 只看字面量的旧判据
// ★★ 在这种片段上会【放行】（因为没有 `_, _ =`）⇒ ⇒ 而路由闸门照样被架空。
func TestProbeMirrorBranchRoutingDetectorHasTeeth(t *testing.T) {
	synthetic := `
func (w *NodeProbeWorker) mirrorNodeProbeState(ctx context.Context, success bool) {
	if success {
		w.execMirrorWrite(ctx, "success", "1|model", func() error { return nil })
		return
	}
	if _, err := w.db.Exec(ctx, "UPDATE node_probe_state SET x = 1"); err != nil {
		slog.WarnContext(ctx, "boom", "error", err)
	}
}
`
	routed := probeMirrorRoutedBranches(synthetic)
	if !routed["success"] {
		t.Fatal("合成片段的 success 分支都没被认出来 ⇒ 检测器本身不可信")
	}
	if routed["failure"] {
		t.Fatal("检测器把【绕过包装直连 Exec】的失败分支认成了已路由 ⇒ 该条不变式没有牙")
	}
}

// TestProbeMirrorFailureVisibleDetectorHasTeeth —— ★ 阳性对照：吞掉写失败必须被报出。
func TestProbeMirrorFailureVisibleDetectorHasTeeth(t *testing.T) {
	// ★ 只吞不报 ⇒ 三条全不满足。
	swallow := `
func (w *NodeProbeWorker) execMirrorWrite(ctx context.Context, branch, summary string, exec func() error) {
	if err := exec(); err != nil {
		return
	}
}
`
	if logged, errPassed, metered := probeMirrorFailureVisible(swallow); logged || errPassed || metered {
		t.Fatalf("检测器把【吞掉写失败】的片段判成可见（logged=%v errPassed=%v metered=%v）⇒ 该判据没有牙",
			logged, errPassed, metered)
	}

	// ★ 只记日志但不计量 ⇒ 「不可告警」必须被抓到（★ 只进日志不进指标 ⇒ 告警系统看不见）。
	logOnly := `
func (w *NodeProbeWorker) execMirrorWrite(ctx context.Context, branch, summary string, exec func() error) {
	if err := exec(); err != nil {
		slog.ErrorContext(ctx, "mirror write failed", "write_error", err.Error())
	}
}
`
	if _, _, metered := probeMirrorFailureVisible(logOnly); metered {
		t.Fatal("检测器把【只记日志不计量】的片段判成可见 ⇒ 该判据没有牙")
	}

	// ★ 只计量但不把错误交给日志 ⇒ 必须被抓到。
	meterOnly := `
func (w *NodeProbeWorker) execMirrorWrite(ctx context.Context, branch, summary string, exec func() error) {
	if err := exec(); err != nil {
		nodeProbeMirrorWriteFailuresTotal.WithLabelValues(branch).Inc()
	}
}
`
	if _, errPassed, _ := probeMirrorFailureVisible(meterOnly); errPassed {
		t.Fatal("检测器把【只计量不报错误】的片段判成可见 ⇒ 该判据没有牙")
	}

	// ★★ ★★ 2026-10-29 审计轮实测出来的【假绿】形态：★ 别的分支上还有一条日志，
	// ★★ 而【主失败路径】的日志被删了 ⇒ ★★ 「函数体里有没有日志」这种判据会放行
	// ★★ ⇒ ⇒ 所以判据必须是「**每一次**计数递增之后都跟一条带错误的日志」。
	partiallySilent := `
func (w *NodeProbeWorker) execMirrorWrite(ctx context.Context, branch, summary string, exec func() error) {
	err := exec()
	if err == nil {
		return
	}
	select {
	case <-ctx.Done():
		nodeProbeMirrorWriteFailuresTotal.WithLabelValues(branch).Inc()
		slog.ErrorContext(ctx, "node_probe_worker: mirror write retry abandoned", "write_error", err.Error())
		return
	}
	nodeProbeMirrorWriteFailuresTotal.WithLabelValues(branch).Inc()
	streak := nodeProbeMirrorWriteStreak.Add(1)
	_ = streak
}
`
	if logged, _, _ := probeMirrorFailureVisible(partiallySilent); logged {
		t.Fatal("检测器把【主失败路径已无日志、只有别的分支还有日志】的片段判成可见 ⇒ 该判据没有牙（★ 这就是 2026-10-29 实测的假绿形态）")
	}
}

// TestProbeMirrorFailureVisibleDetectorAcceptsCompliant 是阴性对照：合规包装不得被误报。
func TestProbeMirrorFailureVisibleDetectorAcceptsCompliant(t *testing.T) {
	compliant := `
func (w *NodeProbeWorker) execMirrorWrite(ctx context.Context, branch, summary string, exec func() error) {
	err := exec()
	if err == nil {
		return
	}
	nodeProbeMirrorWriteFailuresTotal.WithLabelValues(branch).Inc()
	slog.ErrorContext(ctx, "node_probe_worker: mirror write failed", "write_error", err.Error())
}
`
	logged, errPassed, metered := probeMirrorFailureVisible(compliant)
	if !logged || !errPassed || !metered {
		t.Fatalf("检测器误报了合规包装（logged=%v errPassed=%v metered=%v）⇒ 判据过严 ⇒ 会逼着人造假",
			logged, errPassed, metered)
	}
}

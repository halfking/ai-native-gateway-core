package main

// 判据：URSM v2 authoritative 模式下的 worker 接线完整性。
//
// # 缺口是怎么发现的（2026-10-05，实测，不是推理）
//
// `modality_verification` / `baseline_price_sync` / `baseline_reconciliation`
// 三个 worker 原本**只**装配在 `if stateManager != nil` 那条 legacy 分支里，
// 而 `stateManager` **只在 URSM v2 authoritative 模式下为 nil**（main.go:1574）。
//
// 实测症状：
//   - `model_modality_verification` 0 行；827 进度视图 2880 个组合全部
//     verdict=unknown / evidence_rows=0 ⇒「定时核实」从来没跑过；
//   - 日志里 started / skipped / "new probe workers disabled" **三条全 0 行**
//     ⇒ 连「被跳过」都没有一句日志，是静默没启动；
//   - 编译进容器的二进制里 `modality_verification started` 存在（1 处），
//     说明代码在、只是没走到 ——「实现了」与「接线了」在这里是两件事。
//
// ⇒ 本文件钉住：这三个 worker 在**两条分支里都必须出现**。
//
// # 为什么要求「两条都有」而不是只查 authoritative
//
// 只查 authoritative 会漏掉另一种回归：有人把 legacy 那条删掉或搬走，于是
// legacy 模式（off / canary / shadow）反而丢了这三个 worker。两种模式都必须有。
//
// ★ 这条判据自己踩过两次量具坑，都记在下面：
//   ① blockOf 原本从 `{` 之后取块体，判据行本身被排除，于是紧接着的
//      「块里必须有 ModeAuthoritative」自证断言当场报「我数错了块」；
//   ② 原本用 strings.Index 取**第一个** `if stateManager != nil {`，
//      而它在 main.go 里有 **7 处**（2183/3195/4174/4360/4417/5752/7850），
//      只有一处是 worker 组 ⇒ 数到了一个不含 worker 的块，报出
//      「legacy 分支里没有这三个 worker」这个纯由量具造成的假结论。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// workersByBranch 是必须在两条分支里各出现一次的 worker 装配点。
//
// 用**构造函数/入口函数名**而不是日志文案：日志文案可以改，函数名才是装配点。
var workersByBranch = []struct {
	token  string
	reason string
}{
	{"bg.NewModalityVerification", "目标第一半：定时核实多模态能力"},
	{"bg.RunBaselinePriceSync", "目标第二半：把原厂基准价写进 models_canonical"},
	{"bg.RunBaselineReconciliation", "目标第二半：基准价与外部机读源逐模型对账"},
}

const (
	legacyHeader     = "if stateManager != nil {"
	authHeader       = "if stateManager == nil && ursmV2Mgr != nil"
	authModeGuard    = "ursmv2api.ModeAuthoritative"
	optInGate        = "shouldStartNewProbeWorkers"
	capBackfillToken = "bg.NewCapabilityBackfill"
)

var (
	lineComment = regexp.MustCompile(`//[^\n]*`)
	stringLit   = regexp.MustCompile("\"(\\\\.|[^\"\\\\])*\"")
	rawLit      = regexp.MustCompile("`[^`]*`")
)

// readMain 返回 main.go 源码，**剥掉注释与字符串字面量**。
//
// 剥的理由：本文件要在源码文本里数「某个装配点在不在」。若不剥，
// 我自己写的那段解释性注释里提到的函数名就会让判据变绿 ——
// 而注释里提到 ≠ 真的装配了。
func readMain(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		l = lineComment.ReplaceAllString(l, "")
		l = stringLit.ReplaceAllString(l, `""`)
		l = rawLit.ReplaceAllString(l, "``")
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// blocksOf 返回所有以 header 开头的 if 块文本，**含判据行本身**（从 `if` 起算）。
func blocksOf(t *testing.T, src, header string) []string {
	t.Helper()
	var out []string
	for from := 0; ; {
		rel := strings.Index(src[from:], header)
		if rel < 0 {
			break
		}
		idx := from + rel
		open := strings.Index(src[idx:], "{")
		if open < 0 {
			t.Fatalf("%q 之后没有 '{'", header)
		}
		start := idx + open
		depth := 0
		closedAt := -1
		for i := start; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					closedAt = i
				}
			}
			if closedAt >= 0 {
				break
			}
		}
		if closedAt < 0 {
			t.Fatalf("以 %q 开头的块没有闭合", header)
		}
		out = append(out, src[idx:closedAt])
		from = closedAt + 1
	}
	if len(out) == 0 {
		t.Fatalf("main.go 里找不到以 %q 开头的块", header)
	}
	return out
}

func anyContains(blocks []string, token string) bool {
	for _, b := range blocks {
		if strings.Contains(b, token) {
			return true
		}
	}
	return false
}

func TestAuthoritativeBranch_StartsTheSameOptInWorkers(t *testing.T) {
	src := readMain(t)
	legacy := blocksOf(t, src, legacyHeader)
	authBlocks := blocksOf(t, src, authHeader)

	// ── 自证：数的是不是对的块 ──
	// authoritative 判据行在 main.go 里**必须唯一**，否则「它在不在这个块里」
	// 这句话就没有确定答案。
	if len(authBlocks) != 1 {
		t.Fatalf("authoritative 判据行出现了 %d 处（期望 1）：我数错了块", len(authBlocks))
	}
	auth := authBlocks[0]
	if !strings.Contains(auth, authModeGuard) {
		t.Fatalf("authoritative 块里没有 %q —— 我数错了块", authModeGuard)
	}
	if strings.Contains(auth, capBackfillToken) == false {
		t.Fatalf("authoritative 块里连 %s 都没有 —— 我数错了块（它就在这三段之前）", capBackfillToken)
	}
	// legacy 侧：块可以有很多个，但**至少要有一个**含 worker 组
	// （capability/node_probe 的同族块可以不含）。
	if len(legacy) < 1 {
		t.Fatal("legacy 块一个都没找到")
	}
	t.Logf("legacy `stateManager != nil` 块 %d 处；authoritative 块 1 处", len(legacy))

	for _, w := range workersByBranch {
		if !anyContains(legacy, w.token) {
			t.Errorf("legacy 分支（stateManager != nil）里没有 %s —— %s。", w.token, w.reason)
		}
		if !strings.Contains(auth, w.token) {
			t.Errorf("★ authoritative 分支里没有 %s —— %s。"+
				"这就是 2026-10-05 实测到「定时核实从来没跑过、且日志里连一句"+
				"『被跳过』都没有」的那条接线缺口：权威模式下无论怎么设 kill switch "+
				"都够不着它，因为那段代码不在这条分支里。", w.token, w.reason)
		}
	}
}

// TestAuthoritativeBranch_WorkersStayBehindTheOptInGate 钉住「行为变化为零」这个前提。
//
// 补接线很容易顺手写成无条件启动，那会让这台机器**立刻**开始对 584 个模型发挑战图
// —— 与「默认关闭、显式 opt-in」的设计相反。
func TestAuthoritativeBranch_WorkersStayBehindTheOptInGate(t *testing.T) {
	src := readMain(t)
	auth := blocksOf(t, src, authHeader)[0]

	if !strings.Contains(auth, optInGate) {
		t.Fatalf("authoritative 块缺 %s 门控：补进去的三个 worker 会变成无条件启动", optInGate)
	}

	// 核实 worker 必须带 distlock（蓝绿单跑选举），否则两套副本各跑一份。
	pos := strings.Index(auth, "bg.NewModalityVerification")
	if pos < 0 {
		t.Fatal("authoritative 块里没有 bg.NewModalityVerification")
	}
	rest := auth[pos:]
	runAt := strings.Index(rest, ".Run(")
	if runAt < 0 {
		t.Fatal("authoritative 块里 NewModalityVerification 之后没有 .Run(")
	}
	between := rest[:runAt]
	if !strings.Contains(between, "SetDistLock(distlock.NewRedisManager(fpSlotRedis))") {
		t.Error("authoritative 路径的 modality verification 必须带上 distlock，" +
			"否则蓝绿两套副本会各跑一份核实（与同块里 capability_backfill 同理）")
	}
}

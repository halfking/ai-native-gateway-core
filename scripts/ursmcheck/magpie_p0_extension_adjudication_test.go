package ursm_test

// magpie_p0_extension_adjudication_test.go — P0-1 的门：**扩展点裁决表 vs 现实**。
//
// # 这道门守住哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P0-1）指出本仓一个请求面上并存 5 套扩展机制，其中 2 套在生产里分别是
// **死的**（HookRegistry：注册表从未被构造）与**关着的**（Request Pipeline：
// v2 默认关）。裁决表本身写在 `internal/extpoints`（**代码即真值源**）。
//
// ★ 这道门不检查"文档写了什么"，而是**独立复核现实**：
//   「你说 HookRegistry 判死？那我去全仓扫一遍，看它是不是真的零生产调用方」；
//   「你说 ExecuteHooks 挂在 upstream 阶段？那那个 phase 必须在 pipeline 里真的存在」；
//   「你说骨架有 16 个 phase？那我去 domains/pipeline 数一遍」。
//   ⇒ **表与现实不同源就红** —— 这正是本仓反复吃过的亏
//   （"同一口径两处实现"、"框架在、链路不通"、"一门没人跑的守卫是文档，不是门"）。
//
// # 判据清单
//
//	G1 表覆盖全部机制 ID（6 行：5 套机制 + 别名视图行）
//	G2 HookRegistry 的生产调用方**确实为零**（全仓扫源码，排除 _test.go 与定义处）
//	G3 骨架的 phase 数与 domains/pipeline 的真实 phase 一致（实测 16）
//	G4 别名视图的 5 个旧阶段名逐字落在骨架里，且映射目标真实存在
//	G5 ★ TestNoParallelExtensionMechanism：**请求面只有一条链**
//	G6 ★ Production=true 的机制在生产代码里有**真调用点**
//	   （2026-10-24 修：初版会被裁决表**自己**的 Evidence 字符串满足 ⇒ 自证）
//
// # 变异灵敏度
//
//	M1 给 domains/hooks 加一个生产调用方 NewHookRegistry
//	    ⇒ G2 红（"表说判死，实际有调用方"）
//	M2 把 internal/extpoints 里 HookRegistry 的 Verdict 改成 VerdictSkeleton
//	    ⇒ G2 红（"有生产调用方却没判死"）
//	M3 删掉 domains/pipeline 的一个 phase
//	    ⇒ G3 红
//	M4 给别名视图加一个不存在于骨架的 phase
//	    ⇒ G4 红
//	M5 把 ResponseInterceptor 的 Phase 改成 "nonexistent_phase"
//	    ⇒ G5 红
//	M6 把 ExecuteHooks 的 Production 改成 false
//	    ⇒ G6 红
//	M7 ★ 把 ExecuteHooks 的 Production 改回 true（但仍无生产安装方）
//	    ⇒ G6 红（★ 这条只有「排除裁决表自身 + 要求真调用」之后才红得了）

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// magpieP0RepoRoot 返回仓根目录（用 runtime.Caller，不依赖工作目录）。
func magpieP0RepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// magpieP0GoFiles 返回全仓的非测试 Go 文件（排除 vendor）。
func magpieP0GoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".git" || base == "node_modules" || base == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓内 Go 文件失败: %v", err)
	}
	if len(files) < 100 {
		t.Fatalf("只找到 %d 个非测试 Go 文件 ⇒ 扫描面可疑（门会假绿）", len(files))
	}
	return files
}

// TestMagpieP0ExtensionAdjudicationCoversAllMechanisms G1：表覆盖全部机制 ID。
func TestMagpieP0ExtensionAdjudicationCoversAllMechanisms(t *testing.T) {
	want := []string{
		extpoints.IDHTTPMiddlewareChain,
		extpoints.IDRequestPipeline,
		extpoints.IDHookRegistry,
		extpoints.IDHookPhaseVocabulary,
		extpoints.IDExecutorHooks,
		extpoints.IDResponseIntercept,
	}
	got := extpoints.Mechanisms()
	if len(got) != len(want) {
		t.Fatalf("裁决表行数 = %d，want %d（5 套机制 + 别名视图行）", len(got), len(want))
	}
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m.ID] {
			t.Fatalf("机制 ID %q 重复", m.ID)
		}
		seen[m.ID] = true
		if m.Location == "" {
			t.Fatalf("机制 %q 没有位置", m.ID)
		}
		if len(m.Evidence) == 0 {
			t.Fatalf("机制 %q 没有裁决依据（Evidence）", m.ID)
		}
		if m.Note == "" {
			t.Fatalf("机制 %q 没有写明裁决理由（Note）", m.ID)
		}
		switch m.Verdict {
		case extpoints.VerdictMainChain, extpoints.VerdictSkeleton, extpoints.VerdictAlias,
			extpoints.VerdictDead, extpoints.VerdictPhaseBound:
		default:
			t.Fatalf("机制 %q 的裁决 %q 不在裁决词表内", m.ID, m.Verdict)
		}
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("裁决表缺少机制 %q", id)
		}
	}
}

// TestMagpieP0HookRegistryHasNoProductionCallers G2：
// **HookRegistry 的生产调用方确实为零**（表说判死 ⇒ 现实必须如此）。
//
// ★ 判据是**扫源码**而不是"相信表"：找 `NewHookRegistry(` 的调用点，
//
//	排除定义处（domains/hooks/registry.go）与测试文件。
func TestMagpieP0HookRegistryHasNoProductionCallers(t *testing.T) {
	root := magpieP0RepoRoot(t)
	files := magpieP0GoFiles(t, root)

	const defPath = "domains/hooks/registry.go"
	callRe := regexp.MustCompile(`NewHookRegistry\s*\(`)

	var callers []string
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			continue
		}
		if filepath.ToSlash(rel) == defPath {
			continue // 定义处不算调用方
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", rel, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !callRe.MatchString(line) {
				continue
			}
			// 定义行本身（func NewHookRegistry(...)）再排一次。
			if strings.Contains(line, "func NewHookRegistry") {
				continue
			}
			callers = append(callers, filepath.ToSlash(rel)+":"+strconv.Itoa(i+1))
		}
	}

	m, _ := extpoints.MechanismByID(extpoints.IDHookRegistry)
	if len(callers) == 0 {
		if m.Verdict != extpoints.VerdictDead {
			t.Fatalf("HookRegistry 确实零生产调用方，但裁决表判的是 %q（应为 %q）—— "+
				"裁决与现实不同源", m.Verdict, extpoints.VerdictDead)
		}
		return
	}
	// 有生产调用方 ⇒ 表必须改（不能继续说"判死"）。
	t.Fatalf("HookRegistry 出现生产调用方 %v，但裁决表仍判 %q —— "+
		"要么改表（它不再该死），要么改调用方（别复活死机制）", callers, m.Verdict)
}

// TestMagpieP0PipelinePhaseCountMatchesReality G3：
// 裁决表说的骨架 phase 数与 domains/pipeline 的**真实** phase 一致。
func TestMagpieP0PipelinePhaseCountMatchesReality(t *testing.T) {
	root := magpieP0RepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "domains/pipeline/pipeline.go"))
	if err != nil {
		t.Fatalf("读 domains/pipeline/pipeline.go 失败: %v", err)
	}
	// 只取 Phase 常量块里的 `PhaseXxx Phase = "xxx"` 形式。
	re := regexp.MustCompile(`(?m)^\s*Phase[A-Za-z]+\s+Phase\s*=\s*"([a-z_]+)"`)
	matches := re.FindAllStringSubmatch(string(b), -1)
	if len(matches) == 0 {
		t.Fatal("domains/pipeline 里没解析出任何 Phase 常量 ⇒ 判据失效")
	}
	if len(matches) != extpoints.PipelinePhaseCount {
		t.Fatalf("domains/pipeline 实际 phase 数 = %d，裁决表写 %d —— 不同源",
			len(matches), extpoints.PipelinePhaseCount)
	}
	// 记录真实 phase 集合，供 G4/G5 复用。
	real := map[string]bool{}
	for _, m := range matches {
		real[m[1]] = true
	}
	magpieP0RealPipelinePhases = real
}

// magpieP0RealPipelinePhases 由 G3 填充（真实 phase 集合）。
var magpieP0RealPipelinePhases = map[string]bool{}

// TestMagpieP0HookPhaseAliasesAreRealSubsets G4：
// 别名视图的 5 个旧阶段名**逐字**落在骨架里，且映射目标真实存在。
func TestMagpieP0HookPhaseAliasesAreRealSubsets(t *testing.T) {
	root := magpieP0RepoRoot(t)
	// 真实 phase 集合（G3 已填；若没跑过则自己算一次）。
	phases := magpieP0RealPipelinePhases
	if len(phases) == 0 {
		b, err := os.ReadFile(filepath.Join(root, "domains/pipeline/pipeline.go"))
		if err != nil {
			t.Fatalf("读 domains/pipeline/pipeline.go 失败: %v", err)
		}
		re := regexp.MustCompile(`(?m)^\s*Phase[A-Za-z]+\s+Phase\s*=\s*"([a-z_]+)"`)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			phases[m[1]] = true
		}
	}
	if len(phases) == 0 {
		t.Fatal("没解析出任何骨架 phase ⇒ 判据失效")
	}

	if len(extpoints.HookPhaseAliases) != extpoints.HookPhaseCount {
		t.Fatalf("别名视图条数 = %d，want %d", len(extpoints.HookPhaseAliases), extpoints.HookPhaseCount)
	}
	for old, target := range extpoints.HookPhaseAliases {
		if !phases[old] {
			t.Fatalf("旧阶段名 %q 不是骨架 phase 的子集 —— 「降级为别名视图」的前提不成立", old)
		}
		if !phases[target] {
			t.Fatalf("别名 %q → %q，但 %q 不在骨架 phase 里", old, target, target)
		}
	}

	// 旧阶段词表（domains/hooks/types.go）必须与别名表**逐项一致**。
	b, err := os.ReadFile(filepath.Join(root, "domains/hooks/types.go"))
	if err != nil {
		t.Fatalf("读 domains/hooks/types.go 失败: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*Phase[A-Za-z]+\s+Phase\s*=\s*"([a-z_]+)"`)
	oldPhases := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		oldPhases[m[1]] = true
	}
	if len(oldPhases) != extpoints.HookPhaseCount {
		t.Fatalf("domains/hooks 的旧 phase 数 = %d，want %d", len(oldPhases), extpoints.HookPhaseCount)
	}
	for old := range oldPhases {
		if _, ok := extpoints.HookPhaseAliases[old]; !ok {
			t.Fatalf("旧阶段名 %q 不在别名表里 —— 别名视图与旧词表不同源", old)
		}
	}
}

// TestNoParallelExtensionMechanism G5 ★：
// **请求面只有一条链** —— 不允许出现第二套"独立"的扩展机制。
//
// 判据：
//
//	① 生产路径上的主链**恰好一条**（VerdictMainChain 且 Production=true）；
//	② 挂在 phase 上的机制，其 phase **必须在骨架里真实存在**；
//	③ 判死 / 骨架 / 别名 三类机制**不得**自称在生产路径上
//	   （判死的说自己在生产 ⇒ 自相矛盾；骨架/别名同理）。
func TestNoParallelExtensionMechanism(t *testing.T) {
	root := magpieP0RepoRoot(t)
	phases := magpieP0RealPipelinePhases
	if len(phases) == 0 {
		b, err := os.ReadFile(filepath.Join(root, "domains/pipeline/pipeline.go"))
		if err != nil {
			t.Fatalf("读 domains/pipeline/pipeline.go 失败: %v", err)
		}
		re := regexp.MustCompile(`(?m)^\s*Phase[A-Za-z]+\s+Phase\s*=\s*"([a-z_]+)"`)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			phases[m[1]] = true
		}
	}

	mains := 0
	for _, m := range extpoints.Mechanisms() {
		switch m.Verdict {
		case extpoints.VerdictMainChain:
			if m.Production {
				mains++
			}
		case extpoints.VerdictPhaseBound:
			if m.Phase == "" {
				t.Fatalf("机制 %q 判为挂 phase 却没给 phase", m.ID)
			}
			if !phases[m.Phase] {
				t.Fatalf("机制 %q 挂在 phase %q，但骨架里没有这个 phase —— 裁决与现实不同源", m.ID, m.Phase)
			}
			if !m.Production {
				t.Fatalf("机制 %q 判为挂在 phase %q，却标 Production=false —— "+
					"挂在生产链上的机制就是生产机制", m.ID, m.Phase)
			}
		case extpoints.VerdictDead, extpoints.VerdictSkeleton, extpoints.VerdictAlias:
			if m.Production {
				t.Fatalf("机制 %q 判为 %q，却标 Production=true —— "+
					"判死/骨架/别名 三类都不在生产请求链上", m.ID, m.Verdict)
			}
		}
	}
	if mains != 1 {
		t.Fatalf("生产主链数 = %d，want 1 —— **请求面只能有一条链**", mains)
	}
}

// TestMagpieP0ProductionMechanismsAreReallyCalled G6（★ 2026-10-24 修）：
// 裁决表标 `Production=true` 的机制，在**生产代码**里必须有**真调用点**。
//
// ★ 这一条防止"裁决表说它活着，其实它也是死的" —— 与 G2 互补。
//
// ★★★ **本门修过一个「自证」漏洞**（2026-10-24，审计档 §3.14）：
// 初版 G6 的判据是「全仓非测试文件里**出现过** `WithExecuteHooks` 这个字符串」
// ⇒ ★★★ 而裁决表**自己**的 `Evidence` 里就写着
// `domains/streaming/executors/executor_chat.go:70-78（WithExecuteHooks）`
// ⇒ ⇒ **裁决表自己满足了对自己的复核** ⇒ 门永远绿
// ⇒ ⇒ ★★ 这与 LESSONS §355（门从被测对象取被测集合）是**同一种病**。
//
// ⇒ ⇒ 本版三处收紧：
//
//	① **排除裁决表自身**（`internal/extpoints/extpoints.go`）—— 它是**被审对象**，
//	   不能同时是**证人**；
//	② needle 必须带 `(` ⇒ 命中的是**调用**而不是"提到过"；
//	③ **跳过注释行** ⇒ 注释里的 `WithExecuteHooks(ctx, hooks)` 不算调用。
//
// ★ 并且：**只对 `Production=true` 的机制生效** ⇒
//
//	某机制被改判为「死」⇒ 不再要求它有调用方；⇒
//	哪天有人把它改回 `Production=true` 却没接线 ⇒ **本门立刻红**。
func TestMagpieP0ProductionMechanismsAreReallyCalled(t *testing.T) {
	root := magpieP0RepoRoot(t)
	files := magpieP0GoFiles(t, root)

	// ★ needle 硬编码在**门**里（不从裁决表取）⇒ 裁决表改不动断言。
	cases := []struct {
		id     string
		needle string
		// defFiles 是**定义处**（不算调用方）。
		defFiles []string
	}{
		{extpoints.IDHTTPMiddlewareChain, "middleware.NewBuilder()",
			[]string{"middleware/framework.go"}},
		{extpoints.IDExecutorHooks, "WithExecuteHooks(",
			[]string{"domains/streaming/executors/executor_chat.go"}},
		{extpoints.IDResponseIntercept, "NewInterceptorChain(",
			[]string{"domains/hooks/response/chain.go"}},
	}
	// ★★ 裁决表自身**永远不作为证人**。
	const verdictTable = "internal/extpoints/extpoints.go"

	for _, c := range cases {
		m, ok := extpoints.MechanismByID(c.id)
		if !ok {
			t.Fatalf("裁决表里没有机制 %q", c.id)
		}
		if !m.Production {
			// ★ 不在生产路径上 ⇒ 不要求有调用方（判死/骨架/别名 都属这一类）。
			continue
		}
		found := ""
		for _, f := range files {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if rel == verdictTable {
				continue // ★★ 裁决表自己不作证
			}
			skip := false
			for _, d := range c.defFiles {
				if rel == d {
					skip = true
					break
				}
			}
			if skip {
				continue // 定义处不算调用方
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for i, line := range strings.Split(string(b), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue // ★ 注释里的提及不算调用
				}
				if strings.Contains(line, c.needle) {
					found = rel + ":" + strconv.Itoa(i+1)
					break
				}
			}
			if found != "" {
				break
			}
		}
		if found == "" {
			t.Fatalf("机制 %q 标 Production=true，但全仓找不到 %q 的**真调用**（注释不算、"+
				"裁决表自身不作证）⇒ 裁决表说它活着、现实说它死了", c.id, c.needle)
		}
	}
}

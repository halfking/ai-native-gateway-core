package ursm_test

// magpie_p1_failure_policy_test.go — P1-5 的门：**失败策略表 vs 实现**。
//
// # 这道门守住哪块缺口
//
// 计划书 §6-P1-5："给每个 phase/点写死『失败是终止还是继续』，写进文档并
// 落进门禁（`domains/pipeline` 的 `OnError` 目前是逐 hook 自实现，无集中表）"。
//
// ★ 门禁的两条验收：
//	① 策略表与实现**同源**；
//	② 变异的「**关键 phase 的 hook 失败被吞**」必须红。
//
// # 判据清单
//
//	G1 策略表覆盖骨架的**全部 16 个 phase**（不多不少）
//	G2 表里的 phase 名与 `domains/pipeline` 的真实 Phase 常量**逐字一致**
//	G3 ★ `domains/hooks` 的 `isCriticalPhase` **引用**策略表（不是自己再写一份）
//	G4 ★ **关键 phase 的 hook 失败被吞 ⇒ 红**（行为级断言，不是源码匹配）
//	G5 非关键 phase 的 hook 失败 ⇒ **继续**（失败开放）
//	G6 每条策略都写明了理由（判定不能只存在于脑子里）
//
// # 变异灵敏度
//
//	M1 把 `phasepolicy` 表里的 routing 改成 continue
//	    ⇒ G4 红（关键 phase 的失败被吞）
//	M2 把 `domains/hooks/registry.go` 的 isCriticalPhase 改回硬编码
//	    ⇒ G3 红
//	M3 给表里加一个不存在的 phase / 删掉一条
//	    ⇒ G1 / G2 红

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks"
	"github.com/kaixuan/llm-gateway-go/internal/phasepolicy"
)

// magpieP1PolicyRepoRoot 返回仓根目录。
func magpieP1PolicyRepoRoot(t *testing.T) string {
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

// TestMagpieP1FailurePolicyCoversEveryPhase G1/G6：
// 策略表覆盖骨架的**全部 16 个 phase**，且每条都写明了理由。
func TestMagpieP1FailurePolicyCoversEveryPhase(t *testing.T) {
	table := phasepolicy.Policies()
	if len(table) != len(phasepolicy.Phases()) {
		t.Fatalf("策略表条数 = %d，want %d", len(table), len(phasepolicy.Phases()))
	}
	seen := map[string]bool{}
	for _, p := range table {
		if seen[p.Phase] {
			t.Fatalf("phase %q 在策略表里重复", p.Phase)
		}
		seen[p.Phase] = true
		if p.Reason == "" {
			t.Fatalf("phase %q 没有写明失败策略的理由（判定不能只存在于脑子里）", p.Phase)
		}
		switch p.Policy {
		case phasepolicy.PolicyTerminate, phasepolicy.PolicyContinue, phasepolicy.PolicySuspend:
		default:
			t.Fatalf("phase %q 的策略 %q 不在词表内", p.Phase, p.Policy)
		}
	}
	for _, ph := range phasepolicy.Phases() {
		if !seen[ph] {
			t.Fatalf("策略表缺少 phase %q", ph)
		}
	}
}

// TestMagpieP1FailurePolicyPhaseNamesMatchPipeline G2：
// 表里的 phase 名与 `domains/pipeline` 的**真实** Phase 常量逐字一致。
//
// ★ 这是"表 ↔ 实现同源"的**词表**那一半（策略那一半由 G3/G4/G5 把关）。
func TestMagpieP1FailurePolicyPhaseNamesMatchPipeline(t *testing.T) {
	root := magpieP1PolicyRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "domains/pipeline/pipeline.go"))
	if err != nil {
		t.Fatalf("读 domains/pipeline/pipeline.go 失败: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*Phase[A-Za-z]+\s+Phase\s*=\s*"([a-z_]+)"`)
	real := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		real[m[1]] = true
	}
	if len(real) == 0 {
		t.Fatal("没解析出任何骨架 phase ⇒ 判据失效")
	}
	if len(real) != len(phasepolicy.Phases()) {
		t.Fatalf("骨架真实 phase 数 = %d，策略表清单 = %d —— 不同源",
			len(real), len(phasepolicy.Phases()))
	}
	for _, ph := range phasepolicy.Phases() {
		if !real[ph] {
			t.Fatalf("策略表里的 phase %q 在 domains/pipeline 里不存在", ph)
		}
	}
}

// TestMagpieP1HookRegistryReferencesPolicyTable G3：
// `domains/hooks` 的关键阶段判定**引用**策略表（不是自己再写一份）。
//
// ★ 判据是**源码匹配**：函数体必须引用 `phasepolicy`，且**不得**再出现
//
//	自己比较 phase 的写法（那会变成"同一口径两处实现"）。
func TestMagpieP1HookRegistryReferencesPolicyTable(t *testing.T) {
	root := magpieP1PolicyRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "domains/hooks/registry.go"))
	if err != nil {
		t.Fatalf("读 domains/hooks/registry.go 失败: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "internal/phasepolicy") {
		t.Fatal("domains/hooks/registry.go 没有引用 internal/phasepolicy —— " +
			"失败策略的口径又变成两份了")
	}
	if !strings.Contains(src, "phasepolicy.IsTerminating") {
		t.Fatal("domains/hooks/registry.go 没有调用 phasepolicy.IsTerminating —— " +
			"关键阶段的判定没有走策略表")
	}
	// ★ 不得再出现"自己比较 phase"的写法。
	if regexp.MustCompile(`phase\s*==\s*Phase(Pre)?Routing`).MatchString(src) {
		t.Fatal("domains/hooks/registry.go 仍在硬编码关键阶段 —— " +
			"口径必须只有一份（策略表）")
	}
}

// magpieP1ExpectedTerminating 是**独立于策略表**的硬编码期望。
//
// ★★ 为什么要独立：若行为级断言的"被测集合"从策略表里取（`TerminatingPhases()`），
//
//	那么**改表就会同时改掉断言的主体** ⇒ 变异会**静默通过**。
//	⇒ 本轮实测到的坑：把 routing 的策略从 terminate 改成 continue 后，
//	门**依然是绿的** —— 因为 routing 直接从被测集合里消失了。
//	⇒ 期望必须写死在这里，与表**互为对照**。
//
// ★ 注意它覆盖**全部 16 个 phase**（不只 registry 能挂的那 5 个）：
//
//	`authentication` 也是终止阶段 —— 认证失败必须终止请求
//	（magpie 的遮蔽/预算点同理：失败关闭）。
//	它在 registry 里**不可观测**（registry 挂不上 authentication 阶段的 hook），
//	但在**表**这一层必须被钉住 ⇒ 表级断言覆盖它，行为级断言覆盖 registry 的 5 个。
var magpieP1ExpectedTerminating = map[string]bool{
	phasepolicy.PhaseAuthentication: true,
	phasepolicy.PhasePreRouting:     true,
	phasepolicy.PhaseRouting:        true,
}

// magpieP1FailingHook 是一个**必然失败**的 hook（用于行为级断言）。
type magpieP1FailingHook struct {
	phase hooks.Phase
	ran   bool
}

func (h *magpieP1FailingHook) Name() string       { return "magpie-p1-failing" }
func (h *magpieP1FailingHook) Priority() int      { return 100 }
func (h *magpieP1FailingHook) Enabled() bool      { return true }
func (h *magpieP1FailingHook) Phase() hooks.Phase { return h.phase }
func (h *magpieP1FailingHook) Execute(context.Context, *hooks.Environment) error {
	h.ran = true
	return context.DeadlineExceeded
}

// magpieP1RegistryPhases 返回 **hooks registry 支持的阶段**。
//
// ★ 关键事实（审计发现）：domains/hooks 的阶段词表只有 **5 个**
//
//	（pre_routing / routing / pre_upstream / post_upstream / post_response），
//	Register 会拒绝其它阶段（"invalid phase"）⇒ registry **只能**在
//	骨架 16 个 phase 中的 5 个上挂 hook。
//	⇒ 行为级断言只能在这 5 个上做；其余 11 个 phase 的策略由**骨架**
//	（domains/pipeline）承担。
func magpieP1RegistryPhases() []string {
	return []string{
		phasepolicy.PhasePreRouting,
		phasepolicy.PhaseRouting,
		phasepolicy.PhasePreUpstream,
		phasepolicy.PhasePostUpstream,
		phasepolicy.PhasePostResponse,
	}
}

// TestMagpieP1CriticalPhaseFailureIsNotSwallowed G4 ★：
// **关键 phase 的 hook 失败被吞 ⇒ 红**。
//
// ★ 这是计划书 §6-P1-5 验收② 的**行为级**断言：
//
//	路由阶段的 hook 失败必须**中止**（Execute 返回错误），
//	而不是像非关键阶段那样只记录并继续。
func TestMagpieP1CriticalPhaseFailureIsNotSwallowed(t *testing.T) {
	// ★ 被测集合是**固定列表**（registry 支持的阶段），**不是**从策略表取的。
	for _, phase := range magpieP1RegistryPhases() {
		wantTerminate := magpieP1ExpectedTerminating[phase]
		t.Run(phase, func(t *testing.T) {
			hook := &magpieP1FailingHook{phase: hooks.Phase(phase)}
			reg := hooks.NewHookRegistry(nil, hooks.NewNoOpMetricsCollector(), &hooks.NoOpLogger{})
			if err := reg.Register(hook); err != nil {
				t.Fatalf("Register: %v", err)
			}
			err := reg.Execute(context.Background(), hooks.Phase(phase), hooks.NewEnvironment("req-1"))
			if wantTerminate && err == nil {
				t.Fatalf("phase %q 是关键阶段（失败即终止），但 hook 失败被吞了 ⇒ "+
					"这正是计划书点名的『关键 phase 的 hook 失败被吞』", phase)
			}
			if !wantTerminate && err != nil {
				t.Fatalf("phase %q 是失败开放阶段，但 hook 失败却中止了（err=%v）", phase, err)
			}
			// ★★ 策略表的判定必须与**硬编码期望**一致（表 ⇒ 期望）。
			//   这一条才是"改表 ⇒ 变红"的真正来源。
			if got := phasepolicy.IsTerminating(phase); got != wantTerminate {
				t.Fatalf("phase %q：策略表判 IsTerminating=%v，硬编码期望=%v —— "+
					"两者不一致（表与期望必须互为对照）", phase, got, wantTerminate)
			}
			if !hook.ran {
				t.Fatal("hook 根本没被执行")
			}
		})
	}
}

// TestMagpieP1TerminatingPhaseCountIsPinned 断言「终止阶段恰好是这两个」。
//
// ★ 独立于策略表 ⇒ 改表（增删终止阶段）会当场红。
func TestMagpieP1TerminatingPhaseCountIsPinned(t *testing.T) {
	got := phasepolicy.TerminatingPhases()
	if len(got) != len(magpieP1ExpectedTerminating) {
		t.Fatalf("终止阶段数 = %d（%v），want %d（%v）",
			len(got), got, len(magpieP1ExpectedTerminating), magpieP1ExpectedTerminating)
	}
	for _, p := range got {
		if !magpieP1ExpectedTerminating[p] {
			t.Fatalf("phase %q 被判为终止，但不在硬编码期望里 ⇒ "+
				"策略表被改了而期望没跟着改（表与期望必须互为对照）", p)
		}
	}
}

// TestMagpieP1NonCriticalPhaseFailureContinues G5：
// 非关键 phase 的 hook 失败 ⇒ **继续**（失败开放）。
//
// ★ 这是"终止"与"继续"两张脸的对照 —— 只断言终止那一半是不够的
//
//	（否则"全部终止"也会让 G4 绿）。
func TestMagpieP1NonCriticalPhaseFailureContinues(t *testing.T) {
	// ★ 被测集合与期望都**独立于策略表**（否则改表会静默通过）。
	checked := 0
	for _, phase := range magpieP1RegistryPhases() {
		if magpieP1ExpectedTerminating[phase] {
			continue
		}
		checked++
		t.Run(phase, func(t *testing.T) {
			hook := &magpieP1FailingHook{phase: hooks.Phase(phase)}
			reg := hooks.NewHookRegistry(nil, hooks.NewNoOpMetricsCollector(), &hooks.NoOpLogger{})
			if err := reg.Register(hook); err != nil {
				t.Fatalf("Register: %v", err)
			}
			if err := reg.Execute(context.Background(), hooks.Phase(phase), hooks.NewEnvironment("req-1")); err != nil {
				t.Fatalf("phase %q 是失败开放阶段，但 hook 失败却中止了（err=%v）", phase, err)
			}
		})
	}
	if checked == 0 {
		t.Fatal("没有可检查的失败开放阶段 ⇒ 判据失效")
	}
}

// TestMagpieP1PolicyTableAndRegistryAgree G4 的**静态**那一半：
// 策略表里标 terminate 的 phase 集合，与 registry 实际会中止的集合**一致**。
func TestMagpieP1PolicyTableAndRegistryAgree(t *testing.T) {
	want := phasepolicy.TerminatingPhases()
	if len(want) == 0 {
		t.Fatal("策略表里没有任何终止阶段 ⇒ 判据失效")
	}
	// 行为级：上面那条测试已逐个断言；这里断言**集合本身**没漂移。
	for _, ph := range want {
		if !phasepolicy.IsTerminating(ph) {
			t.Fatalf("phase %q 在 TerminatingPhases 里但 IsTerminating 说不是", ph)
		}
	}
	// 非终止的阶段必须不在集合里。
	for _, p := range phasepolicy.Policies() {
		if p.Policy == phasepolicy.PolicyContinue && phasepolicy.IsTerminating(p.Phase) {
			t.Fatalf("phase %q 标 continue 但 IsTerminating 说终止 ⇒ 表与函数不同源", p.Phase)
		}
	}
}

// TestMagpieP1RegistryPhaseVocabularyIsSubsetOfSkeleton 显式化一条**边界**：
// `domains/hooks` 的阶段词表（5 个）是骨架（16 个）的**真子集**。
//
// ★ 这条为什么重要：失败策略表覆盖 16 个 phase，而 registry 只能挂 5 个
//
//	⇒ "表里的策略"与"registry 能问到的策略"**不是一回事**。
//	⇒ 把它写成判据，而不是留给读者自己推。
func TestMagpieP1RegistryPhaseVocabularyIsSubsetOfSkeleton(t *testing.T) {
	skeleton := map[string]bool{}
	for _, p := range phasepolicy.Phases() {
		skeleton[p] = true
	}
	regPhases := magpieP1RegistryPhases()
	if len(regPhases) >= len(skeleton) {
		t.Fatalf("registry 阶段数 %d 不少于骨架 %d —— 子集关系被打破", len(regPhases), len(skeleton))
	}
	for _, p := range regPhases {
		if !skeleton[p] {
			t.Fatalf("registry 阶段 %q 不在骨架里", p)
		}
	}
	// ★ registry 支持的阶段**必须**都有策略条目（否则"查不到策略"= 隐式默认）。
	for _, p := range regPhases {
		if _, ok := phasepolicy.PolicyOf(p); !ok {
			t.Fatalf("registry 支持的阶段 %q 在策略表里没有条目", p)
		}
	}
}

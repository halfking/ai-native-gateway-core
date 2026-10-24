package errorsx

// restclasses_test.go — P0-3 的门：magpie 19 类 rest 词表 × 本仓归责矩阵。
//
// # 这道门守住哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P0-3）要求三件量具：
//
//	① **每类一条红测**：给定错误 → 断言是否 rest 了凭据；
//	② ★ **变异的「内容类拒绝误罚账号」必须红**（本项最关键的一条）；
//	③ 每类的「罚/不罚」判定在代码里有注释，与测试一一对应。
//
// ★ 这道门把「判定」放在**代码**里（`AllRestClasses` 的 Target 字段），
//   而不是注释里 ⇒ 门可以直接对拍「表的判定」与「`PenaltyOf` 的判定」。
//   ⇒ 表与实现不同源就红；语义变了没写差异也红。
//
// # 变异灵敏度（手工验过，见本轮审计档）
//
//	M1：把 `PenaltyOf(KindContentFilter)` 从 none 改成 credential
//	    ⇒ TestRestClassVerdictMatchesPenaltyOf 红（表说 none、实现说 credential）
//	    ⇒ TestContentClassNeverPunishesCredential 也红
//	M2：把 `PenaltyOf(KindAccountRestricted)` 改成 Permanent=true
//	    ⇒ TestAccountRestrictedIsNotPermanent 红
//	M3：从 `AllRestClasses` 删掉一行
//	    ⇒ TestRestClassesCoverAllMagpieClasses 红（19 类缺一即红）
//	M4：把某个 Deviation 清空但语义与 magpie 不同
//	    ⇒ TestDeviationsAreExplicit 红

import (
	"sort"
	"testing"
)

// magpieReferencePenalty 是 magpie 侧的权威「罚/不罚」参照表。
//
// ★ 出处：magpie `internal/gateway/routing.go:270-325` 的 19 个 fail* 常量，
//
//	每一类的注释都写明了"罚不罚"。本表是那份注释的**可执行转写**。
//
// 分三组：
//
//	① **换候选、不罚账号**（8 类）：refused / shape / prompt / effort /
//	   proxy / overflow / room / loop —— 全部只"换下一个候选"，
//	   注释原文是 "the next one is asked, and **nobody rests**"。
//	② **罚账号级**（6 类）：credit / quota / rate / auth / verify / restricted。
//	③ **其余**：canceled / foreign / floor / update 是"换个请求再发"而不是
//	   休息；other 走默认休息；slow 是"第一次放过、反复才罚"。
var magpieReferencePenalty = map[string]PenaltyTarget{
	// ① 换候选、不罚账号
	MagpieClassRefused:  PenaltyNone,
	MagpieClassShape:    PenaltyNone,
	MagpieClassPrompt:   PenaltyNone,
	MagpieClassEffort:   PenaltyNone,
	MagpieClassProxy:    PenaltyNone,
	MagpieClassOverflow: PenaltyNone,
	MagpieClassRoom:     PenaltyNone,
	MagpieClassLoop:     PenaltyNone,
	// ② 罚账号级
	MagpieClassCredit:     PenaltyCredential,
	MagpieClassQuota:      PenaltyCredential,
	MagpieClassRate:       PenaltyCredential,
	MagpieClassAuth:       PenaltyCredential,
	MagpieClassVerify:     PenaltyCredential,
	MagpieClassRestricted: PenaltyCredential,
	// ③ 其余
	MagpieClassCanceled: PenaltyNone,
	MagpieClassForeign:  PenaltyNone,
	MagpieClassFloor:    PenaltyNone,
	MagpieClassUpdate:   PenaltyNone,
	MagpieClassOther:    PenaltyCredential,
	// slow：第一次放过 ⇒ 静态判定是 none（反复的部分由 slowrest.go 承担）
	MagpieClassSlow: PenaltyNone,
}

// TestRestClassesCoverAllMagpieClasses 断言词表覆盖 magpie 全部 19 类，且不重不漏。
func TestRestClassesCoverAllMagpieClasses(t *testing.T) {
	table := AllRestClasses()
	want := AllMagpieRestClasses()
	// ★ 20（不是计划书写的 19）：magpie internal/gateway/routing.go 里实测
	//   20 个 fail* 常量（failCredit … failRestricted）⇒ 计划书的「19」是笔误。
	if len(want) != 20 {
		t.Fatalf("前提失效：magpie 词表应为 20 类（源码实测），实际 %d", len(want))
	}
	if len(table) != len(want) {
		t.Fatalf("词表行数 = %d，want %d（magpie 20 类）", len(table), len(want))
	}
	seen := map[string]int{}
	for _, rc := range table {
		seen[rc.Class]++
	}
	for _, cls := range want {
		switch seen[cls] {
		case 1:
		case 0:
			t.Fatalf("词表缺少 magpie 类 %q", cls)
		default:
			t.Fatalf("词表里 magpie 类 %q 出现 %d 次", cls, seen[cls])
		}
	}
	// 每一行都要有判定文字（"罚/不罚"必须在代码里，不在脑子里）。
	for _, rc := range table {
		if rc.Verdict == "" {
			t.Fatalf("magpie 类 %q 没有写明罚/不罚判定", rc.Class)
		}
	}
}

// TestRestClassVerdictMatchesPenaltyOf 是本题最核心的一条：
// **表里的判定必须等于 `PenaltyOf` 的判定**。
//
// ★ 这条就是 P0-3 验收里的「变异的『内容类拒绝误罚账号』必须红」：
//
//	有人把内容类失败改成罚账号 ⇒ 表（none）与实现（credential）对不上 ⇒ 红。
func TestRestClassVerdictMatchesPenaltyOf(t *testing.T) {
	for _, rc := range AllRestClasses() {
		if rc.Kind == "" {
			// 本仓无对应形态（N/A）⇒ 不约束本仓实现。
			continue
		}
		got := PenaltyOf(rc.Kind)
		if got.Target != rc.Target {
			t.Fatalf("magpie 类 %q 的表内判定 = %q，但 PenaltyOf(%s) = %q（表与实现不同源）",
				rc.Class, rc.Target, rc.Kind, got.Target)
		}
		// N/A 行不得带 Kind，否则"无对应形态"的说法自相矛盾。
		if rc.Kind == "" && rc.Target != "" {
			t.Fatalf("magpie 类 %q 标为无对应形态却带了归责目标 %q", rc.Class, rc.Target)
		}
	}
}

// TestContentClassNeverPunishesCredential 直接断言 P0-3 验收②：
// **内容/形状类失败永远不罚账号**。
//
// ★ 这条刻意**不依赖**词表 ⇒ 即使有人同时改表和实现，这条仍然红。
func TestContentClassNeverPunishesCredential(t *testing.T) {
	contentClasses := []struct {
		class string
		kind  ErrorKind
	}{
		{MagpieClassRefused, KindContentFilter},
		{MagpieClassShape, KindClientBug},
		{MagpieClassPrompt, KindUnsupportedFeature},
		{MagpieClassEffort, KindUnsupportedFeature},
		{MagpieClassOverflow, KindContextLength},
		{MagpieClassLoop, KindReplyLoop},
	}
	for _, c := range contentClasses {
		p := PenaltyOf(c.kind)
		if p.Target == PenaltyCredential || p.Target == PenaltyProvider {
			t.Fatalf("magpie 类 %q（内容/形状类，magpie 判 nobody rests）映射到 %s，"+
				"但 PenaltyOf 判 %q —— 内容类失败**不得**罚账号", c.class, c.kind, p.Target)
		}
		if p.Fuse {
			t.Fatalf("magpie 类 %q（内容类）映射到 %s，但 PenaltyOf 带 Fuse=true —— 内容类失败不得熔断账号",
				c.class, c.kind)
		}
	}
}

// TestAccountLevelClassesPunishCredential 断言账号级证据确实罚账号。
func TestAccountLevelClassesPunishCredential(t *testing.T) {
	for _, c := range []struct {
		class string
		kind  ErrorKind
	}{
		{MagpieClassCredit, KindQuotaBalance},
		{MagpieClassQuota, KindQuota},
		{MagpieClassAuth, KindAuth},
		{MagpieClassRestricted, KindAccountRestricted},
	} {
		p := PenaltyOf(c.kind)
		if p.Target != PenaltyCredential {
			t.Fatalf("magpie 类 %q（账号级证据）映射到 %s，但 PenaltyOf 判 %q，want credential",
				c.class, c.kind, p.Target)
		}
	}
}

// TestModelLevelClassesDoNotPunishAccount 断言模型粒度的类**不**罚账号。
//
// ★ 这条守的是 magpie failLoop 的理由：「是模型转圈，不是账号的锅」。
func TestModelLevelClassesDoNotPunishAccount(t *testing.T) {
	for _, c := range []struct {
		class string
		kind  ErrorKind
	}{
		{MagpieClassLoop, KindReplyLoop},
		{MagpieClassRate, KindRateLimit},
		{MagpieClassOther, KindTransient},
	} {
		p := PenaltyOf(c.kind)
		if p.Target == PenaltyCredential || p.Target == PenaltyProvider {
			t.Fatalf("magpie 类 %q（模型粒度）映射到 %s，但 PenaltyOf 判 %q —— 不得罚账号",
				c.class, c.kind, p.Target)
		}
	}
}

// TestAccountRestrictedIsNotPermanent 断言「账号被厂商限制」**不是**永久失效。
//
// ★ 这是本轮修掉的**真实语义缺陷**：此前 403 一律归 KindAuth，而 KindAuth 是
//
//	Permanent=true ⇒ 账号被限制会被**永久摘除**，而厂商解除限制后账号是能回来的。
func TestAccountRestrictedIsNotPermanent(t *testing.T) {
	restricted := PenaltyOf(KindAccountRestricted)
	if restricted.Target != PenaltyCredential {
		t.Fatalf("PenaltyOf(account_restricted) = %+v，want credential", restricted)
	}
	if restricted.Permanent {
		t.Fatal("PenaltyOf(account_restricted).Permanent = true —— 账号被限制是「等申诉」，不是永久失效")
	}
	// 与 auth 的差别必须真实存在（否则这个 kind 没有存在价值）。
	auth := PenaltyOf(KindAuth)
	if !auth.Permanent {
		t.Fatal("前提失效：PenaltyOf(auth) 应当是永久的（凭据不对要换）")
	}
	if restricted.Permanent == auth.Permanent {
		t.Fatal("account_restricted 与 auth 的永久性必须不同 —— 前者等申诉，后者换凭据")
	}
}

// TestFirstByteTimeoutIsForgivenByDefault 断言首字节超时静态判 none。
//
// ★ 这条是 P0-4 的**静态那半**：「第一次放过」⇒ `PenaltyOf` 必须判 none，
//
//	否则第一次就会把 (凭据,模型) 绑定冷却掉（此前归 KindStreamTimeout 时
//	就是这样：第一次就 5 分钟）。
func TestFirstByteTimeoutIsForgivenByDefault(t *testing.T) {
	p := PenaltyOf(KindFirstByteTimeout)
	if p.Target != PenaltyNone {
		t.Fatalf("PenaltyOf(first_byte_timeout) = %+v，want none（magpie: once, nobody rests）", p)
	}
	if p.Fuse {
		t.Fatal("PenaltyOf(first_byte_timeout).Fuse = true —— 首字节超时不得熔断")
	}
	// 首字节超时必须与**流中超时**分开，否则"第一次放过"无法表达。
	if KindFirstByteTimeout == KindStreamTimeout {
		t.Fatal("KindFirstByteTimeout 与 KindStreamTimeout 必须是不同的 kind")
	}
	if got := PenaltyOf(KindStreamTimeout); got.Target == PenaltyNone {
		t.Fatal("前提失效：流中超时应当照罚（它已是 5 分钟绑定冷却的既有语义）")
	}
}

// TestDeviationsAreExplicit 断言**语义与 magpie 不一致的行必须写明差异原因**。
//
// ★ 这条是「声明与实现同源」在词表上的落点：
//
//	有人悄悄改了归责却没写差异 ⇒ 红。
func TestDeviationsAreExplicit(t *testing.T) {
	for _, rc := range AllRestClasses() {
		ref, ok := magpieReferencePenalty[rc.Class]
		if !ok {
			t.Fatalf("magpie 类 %q 不在参照表里 —— 参照表本身漏项", rc.Class)
		}
		if rc.Kind == "" {
			// N/A 行：本仓无对应形态 ⇒ 不与 magpie 比语义。
			continue
		}
		if rc.Target != ref && rc.Deviation == "" {
			t.Fatalf("magpie 类 %q：本仓判 %q、magpie 判 %q，但**没有写明差异原因** —— "+
				"语义差异必须落在表里，不能只存在于脑子里", rc.Class, rc.Target, ref)
		}
	}
}

// TestRestClassesAreStable 断言词表本身稳定（类名集合不变 ⇒ 新增必须同步改参照表）。
func TestRestClassesAreStable(t *testing.T) {
	got := make([]string, 0, len(AllRestClasses()))
	for _, rc := range AllRestClasses() {
		got = append(got, rc.Class)
	}
	want := append([]string(nil), AllMagpieRestClasses()...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("词表类名数 = %d，want %d", len(got), len(want))
	}
	if len(want) != 20 {
		t.Fatalf("magpie 参照类名数 = %d，want 20（源码实测）", len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("词表类名与 magpie 19 类不一致：位置 %d got %q want %q", i, got[i], want[i])
		}
	}
}

// TestRestClassesByTargetCounts 断言按归责目标的分组统计与表一致（台账用）。
func TestRestClassesByTargetCounts(t *testing.T) {
	counts := RestClassesByTarget()
	mapped := 0
	for _, rc := range AllRestClasses() {
		if rc.Kind == "" {
			continue
		}
		mapped++
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total != mapped {
		t.Fatalf("按目标分组统计合计 = %d，want %d", total, mapped)
	}
}

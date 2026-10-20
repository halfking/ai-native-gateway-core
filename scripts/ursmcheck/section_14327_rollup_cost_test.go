package ursm_test

// §143.27 的文档门：**把 §143.26.8 的 P0 做掉** —— 在真库上量出 rollup 的真实数据量
// 与真实耗时，**并正面回答「抬超时是不是盲改」**；同时给出
// **一条 join 是「可证明的 no-op」的证明**，以及
// **一个 151 MB 的零语义存储优化**。
//
// 背景（runbook §143.27）：
// §143.26 定位了真凶（后台作业被角色级 statement_timeout=30s 截断），但
// §143.26.8 的 P0 明写着「在真库上量一次 dailyInsertSQL 的真实耗时与真实数据量
// ⇒ 没有它，「抬超时」就是盲改」。本节把 P0 做掉了。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **P0 的答案是可执行的决策依据**：它直接否掉了「抬超时」这条路
//    （热态只要 3.8 s）⇒ 若后人只读到「超时是根因」就会去抬超时 ⇒ 门钉住
//    **热态中位 / 冷态读数 / DELETE 的 110 次真实读数**三组数字都在。
// ② ★★★★ **「join 是 no-op」是本节最硬的一条**：它同时否掉了「删 join」和
//    「优化 join」两种草率做法 ⇒ 门钉住 **0 丢行 / 唯一性 / 0 重复**三条读数。
// ③ ★★★★ **151 MB 的存储优化必须钉住「零语义」的四个支点**：
//    `INCLUDE` 不参与唯一性判定、不参与冲突目标推断、写侧 `ON CONFLICT (event_id)` 不变。
//    ⇒ 少一个支点，这个迁移就会变成「改不变式」，那就必须重新评估。
// ④ ★★★ **订正横幅八**：§143.26.6 用 EXPLAIN 下了一个超出 EXPLAIN 能证明的范围的
//    判词（「现状才是好计划」）⇒ 门钉住横幅存在 + **原文保留**。

import (
	"os"
	"strings"
	"testing"
)

const runbook14327 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// rollupAB14327 是 §143.27 的可重跑 A/B 生成器（只读，不连库）。
const rollupAB14327 = "../../scripts/audit/build_rollup_ab.py"

// section14327 取出 §143.27 这一节的正文。
func section14327(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14327)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.27 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.27 的小节标题：§143.27 的结论整节丢了")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// mustContain14327 断言 §143.27 正文里至少含 needle 之一。
func mustContain14327(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.27 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14327SectionHeadingsPresent 钉住 §143.27 的子节骨架齐全。
func TestRunbook14327SectionHeadingsPresent(t *testing.T) {
	s := section14327(t)

	for _, h := range []string{
		"### §143.27.0 结论先行",
		"### §143.27.1 ",
		"### §143.27.2 ",
		"### §143.27.3 ",
		"### §143.27.4 ",
		"### §143.27.5 ",
		"### §143.27.6 ",
		"### §143.27.7 ",
		"### §143.27.8 ",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.27 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14327P0IsAnswered 钉住 P0 的答案：真实数据量 + 真实耗时。
//
// ★ 这是本节存在的理由：没有它，「抬超时」就是盲改。
func TestRunbook14327P0IsAnswered(t *testing.T) {
	s := section14327(t)

	mustContain14327(t, s,
		"73,520",
		"271,087",
		"1,139,786",
		"4,818",
		"6,879",
		"110",
	)
}

// TestRunbook14327WarmVersusColdIsSeparated 钉住「热态 vs 冷态」被分开测了。
//
// ★ 这是本节最容易被后人搞混的一对数字：26,295 ms 是冷态，3,758 ms 是热态。
//   若只读到其中一个，就会得出「这个作业要么 3.8 s 要么 26 s」的错结论。
func TestRunbook14327WarmVersusColdIsSeparated(t *testing.T) {
	s := section14327(t)

	mustContain14327(t, s,
		"26,295.081 ms",
		"12,803.644 ms",
		"3,757.6 ms",
		"3,101.5 ms",
		"冷",
		"热",
		"两组极差完全重叠",
	)
}

// TestRunbook14327RaiseTimeoutIsBlindFix 钉住「抬超时是盲改」这个**反向结论**。
//
// ★ 本节正面回答了 P0 的后半句 ⇒ 这是可执行的决策依据。
func TestRunbook14327RaiseTimeoutIsBlindFix(t *testing.T) {
	s := section14327(t)

	mustContain14327(t, s,
		"「抬超时」确实是盲改",
		"热态只要 3.8 s",
		"慢失败",
	)
}

// TestRunbook14327JoinIsProvableNoOp 钉住「join 是可证明的 no-op」的三条读数。
//
// ★ 少一条就会被误读成「可以删 join」⇒ 而删 join 会改掉「只算当前修订」的语义。
func TestRunbook14327JoinIsProvableNoOp(t *testing.T) {
	s := section14327(t)

	mustContain14327(t, s,
		"一行都没丢",
		"distinct event_id",
		"重复",
		"半连接",
		"删 join 不是",
	)
}

// TestRunbook14327StorageWinIsSemanticsFree 钉住 151 MB 优化的四个「零语义」支点。
//
// ★ 少一个支点，这个迁移就变成「改不变式」⇒ 必须重新评估 ⇒ 所以要钉住。
func TestRunbook14327StorageWinIsSemanticsFree(t *testing.T) {
	s := section14327(t)

	mustContain14327(t, s,
		"151 MB",
		"110 MB",
		"134 MB",
		"395 MB",
		"INCLUDE (occurred_at)",
		"ON CONFLICT (event_id)",
		"不参与",
		"严格前缀",
	)
}

// TestRunbook14327PkeyCannotSimplyBeDropped 钉住「pkey 不能直接删」这个**差点误判的地方**。
//
// ★ 写侧的 `ON CONFLICT (event_id) DO UPDATE SET occurred_at = …` 让 pkey 的
//   唯一性成为「occurred_at 可被改写」的载体 ⇒ 删它 = 改不变式。
func TestRunbook14327PkeyCannotSimplyBeDropped(t *testing.T) {
	s := section14327(t)

	mustContain14327(t, s,
		"domains/stats/event_writer.go:176",
		"不能直接删",
		"唯一性是",
	)
}

// TestRunbook14327CorrectionBannerEightKeepsOriginal 钉住订正横幅八 + **原文保留**。
func TestRunbook14327CorrectionBannerEightKeepsOriginal(t *testing.T) {
	raw, err := os.ReadFile(runbook14327)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	full := string(raw)

	if !strings.Contains(full, "订正横幅八（2026-10-20，§143.27 就地订正本表第 ② 行）") {
		t.Fatalf("runbook 里找不到「订正横幅八」—— §143.26.6 那条超出 EXPLAIN 证明范围的判词没被订正")
	}
	// ★ 原文必须保留。
	if !strings.Contains(full, "★ **猜错** ⇒ **现状才是好计划** ⇒ 不该改 |") {
		t.Fatalf("§143.26.6 的原文「现状才是好计划」不见了 —— 本仓纪律要求订正时**保留原文**")
	}
	// ★ 订正后的判词必须在。
	mustContain14327(t, full,
		"热态：两者无显著差异",
		"冷态：未测到",
	)
}

// TestRunbook14327HonestBoundaries 钉住诚实边界：对生产写入 0；冷态 A/B 没做成。
func TestRunbook14327HonestBoundaries(t *testing.T) {
	s := section14327(t)

	mustContain14327(t, s,
		"对生产的写入 = 0",
		"生产行为未变",
		"冷态 A/B 没做成",
		"夹具缺陷",
		"未写迁移",
	)
}

// TestRunbook14327RecommendationsAreTiered 钉住建议清单按性价比排序。
func TestRunbook14327RecommendationsAreTiered(t *testing.T) {
	s := section14327(t)

	for _, p := range []string{"**P1**", "**P2**", "**P3**", "**P4**", "**P5**", "**P6**", "**P7**", "**P8**"} {
		if !strings.Contains(s, p) {
			t.Fatalf("§143.27 的待拍板清单缺 %s", p)
		}
	}
	mustContain14327(t, s,
		"最高性价比",
		"需属主显式授权",
	)
}

// TestRunbook14327ABHarnessIsInRepo 钉住**可重跑的 A/B 生成器在仓内**。
//
// ★ 本节的读数全部来自它 ⇒ 它若被删或改口径，本节的结论就失去可复核性。
// ★ 特别要钉住「只剥离 INSERT 头与 ON CONFLICT 尾」这条 —— 否则测的就不是生产语句。
func TestRunbook14327ABHarnessIsInRepo(t *testing.T) {
	raw, err := os.ReadFile(rollupAB14327)
	if err != nil {
		t.Fatalf("读 A/B 生成器失败：%v", err)
	}
	script := string(raw)

	for _, n := range []string{
		"dailyInsertSQL",
		"ON CONFLICT (day_utc,",
		"byte-identical",
		"cold-pair",
		"ABAB",
	} {
		if !strings.Contains(script, n) {
			t.Fatalf("A/B 生成器里没有 %q —— 本节的读数失去可复核性", n)
		}
	}
	// ★ 必须支持「每臂一个互不重叠的冷窗口」⇒ 否则冷态读数会互相污染。
	if !strings.Contains(script, "DISJOINT") {
		t.Fatalf("A/B 生成器里没有「DISJOINT」说明 —— §143.27.2.2 记的夹具缺陷可能被重新引入")
	}
}

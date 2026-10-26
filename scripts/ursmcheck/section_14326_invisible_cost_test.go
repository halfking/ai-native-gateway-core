package ursm_test

// §143.26 的文档门：**把「看不见的成本」量化出来**、**推翻本会话自己先前的判词**
// （订正横幅七）、**把真凶从「物化视图刷新」重新定位到「被角色级 30s 截断的后台作业」**、
// **留下一处真正可动手的结构点**，以及**记录两条被自己的 EXPLAIN 推翻的猜测**。
//
// 背景（runbook §143.26）：
// §143.20 判「B1 的根因是 7 天窗的全量重算」，但留下一个诚实的口子——「REFRESH 的 59 秒里，
// 重算源表与 CONCURRENTLY 机制各占多少，本节没有分离」。本节继续往下做，结果**推翻了
// 本会话自己先前的另一个判词**（「约 32–33 次/天的 REFRESH 被 statement timeout 杀掉」= 误报），
// 并把「看不见的成本」定位到了一处可动手的结构点上。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **自我订正必须留痕**：本仓纪律是「错误结论**就地**加横幅 + **保留原文**」。
//    若后人只读到订正后的数字，会以为本节从未错过 ⇒ 门必须同时钉住
//    **被订正的错误数字（32–33）** 与 **订正后的实测数字（2 + 3 = 5）**。
// ② ★★★★★ **「上下文命中」不是归属判定**：本节抓到一个教科书级误判——
//    一条 `INSERT INTO public.session_bodies_hot` 的语句正文里含
//    "REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d"（因为有人在 psql 里
//    粘了交接说明），于是被误判成「REFRESH 被杀」。
//    ⇒ 解析器必须按**语句的语法头部**归属 ⇒ 门钉住 `REFRESH_HEAD` / `refresh_view`。
// ③ ★★★★ **真凶换了**：从「matview 刷新」换成「后台作业被角色级 `statement_timeout=30s` 截断」。
//    这条若被后人读成「还是 matview 的问题」，优化方向就跑偏 ⇒ 门钉住指纹表与角色级读数。
// ④ ★★★ **阴性结论也要钉**：「`stats_usage_daily` 仍在被填 ⇒ 失败是浪费不是空洞」
//    —— 这条挡住「把超时升级成数据事故」的误报级判断。
// ⑤ ★★★★ **两处可动手的结构点必须钉住数字**：视图第 3 臂产出 203,430 行（60.0%），
//    反连接要哈希 438,962 行、cost 171,357 ⇒ 后人才能复核「不能删臂、但可时间化/物化」。

import (
	"os"
	"strings"
	"testing"
)

const runbook14326 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// pgKillParser14326 是 §143.26 新增的 PG 日志解析器（只读，不连库）。
const pgKillParser14326 = "../../scripts/audit/parse_pg_kills.py"

// section14326 取出 §143.26 这一节的正文。
func section14326(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14326)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.26 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.26 的小节标题：§143.26 的结论整节丢了")
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

// mustContain14326 断言 §143.26 正文里至少含 needle 之一。
func mustContain14326(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.26 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14326SectionHeadingsPresent 钉住 §143.26 的子节骨架齐全。
//
// ★ 门要在**整节丢失**时也能红（不只是数字丢一个）。
func TestRunbook14326SectionHeadingsPresent(t *testing.T) {
	s := section14326(t)

	for _, h := range []string{
		"### §143.26.0 结论先行",
		"### §143.26.1 ",
		"### §143.26.2 ",
		"### §143.26.3 ",
		"### §143.26.4 ",
		"### §143.26.5 ",
		"### §143.26.6 ",
		"### §143.26.7 ",
		"### §143.26.8 ",
		"### §143.26.9 ",
		"### §143.26.10 ",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.26 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14326GateLimitationIsRecorded 钉住「Contains 型门只保护『至少有一处』」这条自省。
//
// ★ 这是本节**做门的过程中被自己的门绊了一次**才浮出来的教训：
//   6 个变异一开始「仍是绿的」，真因不是判据没守卫力，而是**变异只替换了第一次出现**
//   ⇒ 门只钉住「读数存在」，不钉住「每处都对」。
// ★ 若后人把这条自省删掉，下一个做门的人会**重复踩同一个坑**，并且很可能
//   误判成「门没有守卫力」⇒ 去写一个更弱的门。
func TestRunbook14326GateLimitationIsRecorded(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"只保护「至少有一处」",
		"全部出现",
		"误判成「门没有守卫力」",
		"量具错了",
	)
}

// TestRunbook14326CorrectionBannerSevenKeepsOriginal 钉住订正横幅七 + **原文保留**。
//
// ★ 本仓纪律：错误结论**就地**加横幅 + **保留原文**（不静默改写）。
// ★ 横幅必须**点名**要订正的是哪条，否则后人读不到改了什么。
func TestRunbook14326CorrectionBannerSevenKeepsOriginal(t *testing.T) {
	raw, err := os.ReadFile(runbook14326)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	full := string(raw)

	if !strings.Contains(full, "订正横幅七（2026-10-20，§143.26 就地订正）") {
		t.Fatalf("runbook 里找不到「订正横幅七」—— 本节的自我订正没有留痕，后人会以为本节从未错过")
	}

	// ★ 被订正的那条错误判词必须**在原处保留**（只加横幅，不静默改写）。
	if !strings.Contains(full, "约 **32–33 次/天**的 `REFRESH`") {
		t.Fatalf("§143.26.0 里被订正的原文「约 32–33 次/天的 REFRESH」不见了 —— 本仓纪律要求订正时**保留原文**")
	}
	if !strings.Contains(full, "上下文命中") {
		t.Fatalf("§143.26 里没有「上下文命中」这个错因 —— 后人读不到本节到底错在哪")
	}

	// ★ 订正后的实测数字必须在。
	for _, n := range []string{
		"被 `statement timeout` 杀 = 2 次",
		"3 次",
		"5 次 / 385 次",
	} {
		if !strings.Contains(full, n) {
			t.Fatalf("订正横幅七里没有钉住实测读数 %q —— 订正后的数字不见了", n)
		}
	}
}

// TestRunbook14326RealKillersIdentified 钉住「真凶是后台作业，不是物化视图」。
//
// ★ 这是本节的方向性结论：若被读成「还是 matview 的问题」，优化方向就跑偏。
func TestRunbook14326RealKillersIdentified(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"349 次 `statement timeout` 杀",
		"2,473 次",
		"usage_facts",
		"stats_event_dedup",
		"stats_usage_daily",
		"session_summaries",
		"ursm_node_snapshot_min",
		"domains/stats/daily_monthly_rollup.go:198",
		"db/db.go:1088",
		"domains/streaming/model_alternatives.go:186",
		"1,316 次",
	)
}

// TestRunbook14326RoleLevelTimeoutIsTheRootCause 钉住「角色级 30s 把后台作业截断」。
//
// ★ 这是**可操作**的那条：Go 侧的 ctx 超时够不着 DB 的 statement_timeout ⇒ Go 侧是死代码。
func TestRunbook14326RoleLevelTimeoutIsTheRootCause(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"statement_timeout=30s",
		"idle_in_transaction_session_timeout=60s",
		"domains/stats/daily_monthly_rollup.go:94",
		"context.WithTimeout(ctx, 2*time.Minute)",
		"死代码",
		"bg/materialized_view_refresher.go:106",
		"mvRefreshStatementTimeout",
	)
}

// TestRunbook14326NegativeResultKept 钉住阴性结论：「失败是浪费，不是数据空洞」。
//
// ★ 挡住「把超时升级成数据事故」的误报级判断。
func TestRunbook14326NegativeResultKept(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"113,952",
		"27,806,075",
		"4.6×",
		"不是「数据缺失」",
		"不要把这类失败升级成数据事故",
	)
}

// TestRunbook14326StructuralPointIsQuantified 钉住唯一可动手的结构点的读数。
//
// ★ 「不能删臂」（203,430 行 = 60.0%）与「反连接代价」（438,962 行 / cost 171,357）
//   必须同时在 ⇒ 否则后人会误以为可以删掉第 3 臂。
func TestRunbook14326StructuralPointIsQuantified(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"203,430",
		"60.0%",
		"135,546",
		"438,962",
		"171,357",
		"NOT EXISTS",
		"1,162,493,952",
		"338,976",
	)
}

// TestRunbook14326RefutedHypothesesRecorded 钉住两条被自己的 EXPLAIN 推翻的猜测。
//
// ★ 「猜错过」比「猜对了」更能防后人重蹈 ⇒ 两条都必须在。
func TestRunbook14326RefutedHypothesesRecorded(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"现状才是好计划",
		"gw_session_id",
		"猜错",
		"EXPLAIN 只证明「计划会选」",
	)
}

// TestRunbook14326HonestBoundaries 钉住诚实边界：对生产写入 0；未量到真实耗时。
func TestRunbook14326HonestBoundaries(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"对生产的写入 = 0",
		"生产行为未变",
		"至今未知",
		"不能承诺「抬超时就能跑完」",
		"未写任何迁移",
	)
}

// TestRunbook14326RecommendationsAreTiered 钉住建议清单按成本排序、且 P0 是「先量」。
func TestRunbook14326RecommendationsAreTiered(t *testing.T) {
	s := section14326(t)

	mustContain14326(t, s,
		"**P0**",
		"**P1**",
		"**P2**",
		"**P3**",
		"**P4**",
		"**P5**",
		"**P6**",
		"**P7**",
		"**P8**",
		"需属主显式授权",
	)
}

// TestRunbook14326ParserUsesSyntaxHeadAttribution 钉住**解析器按语句语法头部归属**。
//
// ★ 这是本节证据力的根：本节抓到的教科书级误判，正是「正文含 REFRESH ⇒ 判成 REFRESH」。
//   若解析器被改回「正文含 REFRESH 就算 REFRESH」，§143.26.2 的订正就失去证据力。
func TestRunbook14326ParserUsesSyntaxHeadAttribution(t *testing.T) {
	raw, err := os.ReadFile(pgKillParser14326)
	if err != nil {
		t.Fatalf("读解析器失败：%v", err)
	}
	script := string(raw)

	// ★ 必须有「按语法头部」的判定函数（解析器是 Python ⇒ `def`，不是 Go 的 `func`）。
	for _, n := range []string{
		"REFRESH_HEAD",
		"def refresh_view(",
		"STATEMENT_HEAD",
	} {
		if !strings.Contains(script, n) {
			t.Fatalf("解析器里没有 %q —— 归属判定不再按语句的语法头部，§143.26.2 的订正失去证据力", n)
		}
	}
	// ★ 必须**锚定行首**（`^` 锚点）⇒ 「正文里含 REFRESH」不能算数。
	if !strings.Contains(script, `r"^\s*REFRESH\s+MATERIALIZED\s+VIEW\s+(?:CONCURRENTLY\s+)?([A-Za-z0-9_]+)"`) {
		t.Fatalf("解析器里的 REFRESH_HEAD 没有锚定行首 —— 「正文含 REFRESH」又会被算成 REFRESH")
	}
	// ★ 注释里必须留下这条误判的教训（否则后人不知道为什么必须锚定行首）。
	if !strings.Contains(script, "syntax head") {
		t.Fatalf("解析器里没有「syntax head」的说明 —— 本节的教科书级误判没被记录在证据侧")
	}
	// ★ 解析器必须能重建多行 STATEMENT（否则归属又是「上下文命中」）。
	for _, n := range []string{
		"STATEMENT",
		"continuation",
	} {
		if !strings.Contains(script, n) {
			t.Fatalf("解析器里没有 %q —— 多行 STATEMENT 的归属重建丢了", n)
		}
	}
}

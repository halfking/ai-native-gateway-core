//go:build !integration

package main

// §143.77 的门：把「★★★ L3 兜底腿的 ~150 ms 是【并行 worker 启动开销】」
// 这条实测结论，以及它推出的三条「不许这么改」的约束，钉成静态判据。
//
// ★★★ 为什么这道门要写（§143.77 的起因）：
//
//	① 现象：L3 兜底腿在生产上以两种形态并存，合计
//	   **25,492 次 / 3,794,612 ms（≈ 63 分钟）/ rows 全部 = 0**。
//	② 根因（执行计划自证）：计划顶层是 `Gather Merge`（并行排序）⇒ 每次调用
//	   都付**并行 worker 的启动开销**。决定性对照（同一 SQL、同一会话、同一缓存，
//	   只切 `max_parallel_workers_per_gather`）：
//	     E1 会话 170.136 ms ⇒ 0.748 ms（227×）；
//	     787 轮大会话 261.811 ms ⇒ 629.980 ms（★ 反而慢 2.4×）；
//	     库里不存在的会话（**返 0 行**）508.773 ms ⇒ 0.969 ms（525×）。
//	③ ⇒ 「返 0 行也要照付并行钱」被实测坐实 ⇒ ★★ 这 63 分钟几乎全是
//	   「问一次、什么都不拿、还付 100 ms」。
//
// ★★ 本门要守的不是「某个函数存在」，而是【几件仍然为真的事实】：
//
//	① ★★ 驱动表**必须仍是 turns 源**（`db.SessionFamilyTurnsForSessionSQL()`）。
//	   ★★ 因为实测过的候选修法 D（bodies 侧驱动，O(1)）虽然快，
//	   ★★ 但它把「取哪一轮的元数据」的口径改了（11 个真实会话里 1 例不同）
//	   ★★ ⇒ ★★ **在属主拍板语义之前，不许偷偷换驱动**。
//	② ★★ UNION 的各腿**不得自带 `ORDER BY ... LIMIT`**。
//	   ★★ 实测：把 top-1 推进各腿会让计划从索引扫描**退化成顺序扫描**
//	   ★★ （4.4 GB 的 turns 族上跑了几分钟，`wait_event = DataFileRead`）。
//	③ ★★ final_full 腿的形状不许塌：COALESCE 顺序（ff 在前）+ `ORDER BY f.ts DESC`
//	   + `LIMIT 1`（★ 少一个就退化成 §143.55 那个「读不到快照」的老 bug）。
//	④ ★★ 复算脚本必须存在且保留四件东西：① 从 `pg_stat_statements` 取生产原文
//	   （不手抄）② 并行开关的对照 ③ 三个样本（E1 会话 / 大会话 / 库里不存在）
//	   ④ 用 RAISE NOTICE 打印观测值（★ 没有观测值的复算脚本等于没有）。
//	⑤ ★★ `FIXME-14377` 登记块必须在（★ 它的原文是本门全部判据的依据）。
//
// ★ 本门只读仓内源码 ⇒ **不依赖生产状态、不含任何生产读数**。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

const section14377AuditPath = "scripts/audit/verify_l3_fallback_parallel_cost.sql"

func repoRoot14377(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestSection14377L3FallbackDriverStaysTurns（★ 判据①）
//
// ★ 对照形态：断言 helper 文本**必须非空且长度合理**，避免「判据恒真」。
func TestSection14377L3FallbackDriverStaysTurns(t *testing.T) {
	turns := db.SessionFamilyTurnsForSessionSQL()
	if len(turns) < 200 {
		t.Fatalf("★ turns 源文本异常（长度 %d）—— 判据本身失效（正常应为数百字符以上）", len(turns))
	}
	if !strings.Contains(lastOutboundSessionSQL, turns) {
		t.Error("★ L3 兜底腿的驱动表不再是 turns 源 —— " +
			"实测过的 bodies 驱动候选修法【会改元数据口径】（11 个真实会话里 1 例不同）" +
			"⇒ ⇒ 在属主拍板语义之前不许换驱动")
	}
	if !strings.Contains(lastOutboundSessionSQL, "ORDER BY rl.ts DESC") {
		t.Error("★ L3 兜底腿丢了顶层 `ORDER BY rl.ts DESC` —— " +
			"§143.56 的合同是「取该会话最近一个【真的有正文】的轮次」")
	}
	if !strings.Contains(lastOutboundSessionSQL, "LIMIT 1") {
		t.Error("★ L3 兜底腿丢了 LIMIT 1")
	}
}

// TestSection14377L3FallbackNoPerLegTop1（★ 判据②）
//
// ★ 反向判据：ORDER BY 在整条 SQL 里【只能出现两次】——
//
//	① final_full 腿的 `ORDER BY f.ts DESC`；
//	② 顶层的 `ORDER BY rl.ts DESC`。
//
// ★ 若有人给 UNION 的某条腿加「腿内 top-1」，次数会变成 3 ⇒ 门红。
func TestSection14377L3FallbackNoPerLegTop1(t *testing.T) {
	q := lastOutboundSessionSQL
	if got := strings.Count(q, "ORDER BY"); got != 2 {
		t.Errorf("★ L3 兜底腿里 ORDER BY 出现 %d 次（应为 2：final_full 腿 + 顶层）—— "+
			"实测：把 top-1 推进 UNION 各腿会让计划从索引扫描退化成顺序扫描", got)
	}
	// ★ 形态级 needle：腿内 top-1 的典型形态是「ORDER BY … LIMIT 1)」这种带右括号的收尾
	for _, bad := range []string{
		"ORDER BY ts DESC LIMIT 1)",
		"ORDER BY t.ts DESC LIMIT 1)",
		"ORDER BY ts DESC LIMIT 1) UNION",
	} {
		if strings.Contains(q, bad) {
			t.Errorf("★ L3 兜底腿出现了腿内 top-1 形态 %q —— 实测会让计划退化成顺序扫描", bad)
		}
	}
}

// TestSection14377L3FallbackFinalFullLegShape（★ 判据③）
func TestSection14377L3FallbackFinalFullLegShape(t *testing.T) {
	q := lastOutboundSessionSQL
	if !strings.Contains(q, "COALESCE(ff.outbound_body, sb.outbound_body)") {
		t.Error("★ 投影缺 COALESCE(ff.outbound_body, sb.outbound_body)（final_full 优先）")
	}
	if strings.Contains(q, "COALESCE(sb.outbound_body, ff.outbound_body)") {
		t.Error("★ COALESCE 顺序反了 —— 两条腿都在时不报错，但取到的是旧值")
	}
	for _, needle := range []string{
		"FROM public.session_bodies_unified f",
		"f.tenant_id = $2",
		"f.session_id = $1",
		"f.kind = 'final_full'",
		"ORDER BY f.ts DESC",
	} {
		if !strings.Contains(q, needle) {
			t.Errorf("final_full 腿缺 %q", needle)
		}
	}
	// ★ LIMIT 1 必须在 final_full 腿内部（少一个 ⇒ 回到 §143.55 那个老 bug）
	iLateral := strings.Index(q, "LEFT JOIN LATERAL (")
	if iLateral < 0 {
		t.Fatal("L3 兜底腿没有 final_full 腿")
	}
	latEnd := strings.Index(q[iLateral:], ") ff ON TRUE")
	if latEnd < 0 {
		t.Fatal("final_full 腿没有以 ') ff ON TRUE' 收尾")
	}
	if !strings.Contains(q[iLateral:iLateral+latEnd], "LIMIT 1") {
		t.Error("★ final_full 腿缺 LIMIT 1 —— 会把快照腿变成放大行集的 JOIN")
	}
}

// TestSection14377RecomputeScriptKeepsItsEvidence（★ 判据④）
func TestSection14377RecomputeScriptKeepsItsEvidence(t *testing.T) {
	path := filepath.Join(repoRoot14377(t), section14377AuditPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("★ 复算脚本缺失：%s（%v）", section14377AuditPath, err)
	}
	s := string(raw)
	for _, needle := range []string{
		// ① 从 pg_stat_statements 取生产原文（不手抄 ⇒ 不会与生产漂移）
		"pg_stat_statements",
		// ② 并行开关的对照
		"max_parallel_workers_per_gather",
		"PARALLEL_ON",
		"PARALLEL_OFF",
		// ③ 三个样本：E1 会话 / 大会话 / 库里不存在
		"final_full",
		"big_no_ff",
		"not_exists",
		// ④ 观测值必须打印出来（没有观测值的复算脚本等于没有）
		"RAISE NOTICE",
		"Execution Time",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("★ 复算脚本缺 %q —— 它是本轮全部结论的唯一可复算依据", needle)
		}
	}
	// ★ 反向判据：不得出现「手抄的整条生产 SQL」（★ 那样会与生产漂移）
	if strings.Contains(s, "SELECT sb.outbound_body, rl.outbound_msg_count") {
		t.Error("★ 复算脚本里出现了手抄的生产 SQL —— 必须从 pg_stat_statements 取原文")
	}
}

// TestSection14377FixmeBlockPresent（★ 判据⑤）
//
// ★ needle 必须带完整标记形态（★ 只查 "14377" 会命中别的文本）。
func TestSection14377FixmeBlockPresent(t *testing.T) {
	path := filepath.Join(repoRoot14377(t), "cmd/gateway/main_v3_wiring.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 cmd/gateway/main_v3_wiring.go 失败：%v", err)
	}
	if !strings.Contains(string(raw), "FIXME-14377（") {
		t.Error("★ cmd/gateway/main_v3_wiring.go 里没有 FIXME-14377（ 登记块 —— " +
			"本门全部判据的依据就写在那里，它不能被无声删掉")
	}
}

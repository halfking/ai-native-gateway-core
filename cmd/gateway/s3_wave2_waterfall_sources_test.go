//go:build !integration

package main

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// TestS3Wave2WaterfallSources（2026-10-13 S3 波 2）钉住调度瀑布两个 DB 读端
// 的源形状。停写后新流量的 t0..t9 只落 session_turns(_hot)；v1 hot 只会
// 排空。判据同层：两个包级常量就是发给 PostgreSQL 的文本。
//
// # ★★ 2026-10-21（§143.55）：本门原来**正在钉住一个 bug**
//
// 原来「LastOutboundForSession 会话族腿」那三条断言是：
//
//	① `session_id = $1` 全局计数 == 2；
//	② 必须存在 `ON sb.request_id = rl.request_id`；
//	③ 必须存在 `sb.outbound_body IS NOT NULL`。
//
// ②③ 合起来 = 「**逐轮正文只能按 request_id 关联、且必须有正文**」——
// 而这条合同**只对逐轮正文成立**。E1（final_full）打开后，
// 快照行的 request_id 是 `'final_full:<session>'`，**与任何一轮都不相等**
// ⇒ 原式在门开时**恒返 0 行**（L3 冷启动拿不到 outbound）。
// ⇒ 而那三条断言**恰好要求**这种形态继续存在 ⇒ **门把 bug 守住了**。
//
// ★ 教训（与「门红了先读门在守什么」同族）：
//
//	**判据要问「对的是哪个形态」，不是问「字符串在不在」。**
//	同一个子串（`sb.outbound_body IS NOT NULL`）在两种形态里的含义相反：
//	逐轮正文它是「这轮有正文」，快照正文它是「这轮没有正文、快照才有」。
//
// # 现在的判据：按【块】断言（不是全局子串）
//
// 本门改成**按块**抽取再断言，原因有三条（每条都对应一种「貌似合理的错误形态」）：
//
//	① JOIN **种类**：必须是 `LEFT JOIN`（内连接 ⇒ 快照腿永远没机会答）。
//	   ⇒ 判据用 `HasPrefix(trimmed, "LEFT JOIN ")` 而不是
//	   `Contains("JOIN …")` —— ⚠ 后者会把 `LEFT JOIN` 也算进去 ⇒ **恒绿**。
//	② COALESCE **顺序**：必须是 `COALESCE(ff.outbound_body, sb.outbound_body)`
//	   —— 反过来（先逐轮后快照）就取错了源，而两条腿都在时**不报错**。
//	③ WHERE 的**形状**：必须是「sb 腿的正文判据 **OR** ff 腿的正文判据」
//	   —— 少了它 ⇒ LEFT JOIN 会把「本轮没有正文」的行也放进来 ⇒ 行集被放大。
//
// # ★★ 2026-10-21（§143.56）：判据 ③ 的「正文判据」本身被换掉了
//
// §143.55 那一版把 ③ 写成「必须存在 `sb.outbound_body IS NOT NULL OR
// ff.outbound_body IS NOT NULL`」—— ★ **那条判据本身就是恒真的**：
// 会话族的「没有正文」是 **jsonb `null`**（不是 SQL NULL）⇒ 裸 `IS NOT NULL`
// 在会话族上恒真（实测 20,643 行里 20,643 行通过）⇒
// 「这轮有正文」被写成了「这轮有行」。
//
// ⇒ 本轮把 ③ 改成「必须存在 `db.SessionBodiesHasOutboundPredicate(alias)` 的
// **展开文本**（`IS NOT NULL AND <> 'null'::jsonb`）」+ 一条**反向**判据
// （裸形态必须不在）⇒ 判据从「恒真」变成「有牙」。
//
// ⇒ ★ 同族教训（与「门红了先读门在守什么」同源）：
//
//	**判据本身恒真时，它会把「什么都没检查」伪装成「检查通过」。**
//	判据的强度要单独审：问「这条判据在**错误形态**下会不会红」，
//	而不只是问「它在**正确形态**下会不会绿」。
func TestS3Wave2WaterfallSources(t *testing.T) {
	t.Run("点查走 canonical 视图", func(t *testing.T) {
		q := waterfallByRequestSQL
		if !strings.Contains(q, "FROM request_logs_with_current_month") {
			t.Error("request_id 点查未走 canonical 视图（B 形态守卫规则的唯一完整源）")
		}
		if strings.Contains(q, "FROM _request_logs_hot") {
			t.Error("点查仍在直读 _request_logs_hot（停写后排空）")
		}
		// 调度计时 t0..t9 必须齐全——瀑布图的主体就是它们。
		for _, col := range []string{
			"t0_arrived_at", "t1_total_enqueued_at", "t2_total_dequeued_at",
			"t3_model_enqueued_at", "t4_model_dequeued_at", "t5_cred_enqueued_at",
			"t6_cred_dequeued_at", "t7_forward_start_at", "t8_response_start_at",
			"t9_response_end_at",
		} {
			if !strings.Contains(q, col) {
				t.Errorf("点查投影缺调度计时列 %s", col)
			}
		}
	})
	t.Run("列表走 canonical 视图", func(t *testing.T) {
		q := waterfallListSQL
		if !strings.Contains(q, "FROM request_logs_with_current_month") {
			t.Error("瀑布列表未走 canonical 视图——停写后热窗排空面板恒空")
		}
		if strings.Contains(q, "FROM _request_logs_hot") {
			t.Error("瀑布列表仍在直读 _request_logs_hot")
		}
		if !strings.Contains(q, "ORDER BY t0_arrived_at DESC") {
			t.Error("列表按 t0 倒序的近期语义丢失")
		}
	})
	t.Run("LastOutboundForSession 会话族腿", func(t *testing.T) {
		q := lastOutboundSessionSQL
		turns := db.SessionFamilyTurnsForSessionSQL()
		bodies := db.SessionFamilyBodiesSourceSQL()
		lateralMarker := "LEFT JOIN LATERAL ("

		// ── ① 两条腿必须是【真 helper】的文本（不是抄进 SQL 的副本）────────
		//
		// ⚠ 这两条源是手搓 SQL 字符串；抄一份进 SQL 里就等于多了一个事实源
		// （helper 改了、SQL 没改 ⇒ 两边形状对不上，而门全绿）。
		iTurns := strings.Index(q, turns)
		if iTurns < 0 {
			t.Fatalf("会话族腿的 turns 源不是 db.SessionFamilyTurnsForSessionSQL() 的文本")
		}
		if !strings.Contains(q, bodies) {
			t.Fatalf("会话族腿的 bodies 源不是 db.SessionFamilyBodiesSourceSQL() 的文本")
		}
		// turns 块内：会话内推谓词两处（hot 臂 + 父表臂）。
		// ⚠ **块内**计数，不是全局计数：final_full 腿也要用 session_id = $1
		// （全局计数会是 3，而「3」这个数本身不说明任何事）。
		turnsBlock := q[iTurns : iTurns+len(turns)]
		if got := strings.Count(turnsBlock, "session_id = $1"); got != 2 {
			t.Errorf("turns 腿应有内推 session_id = $1 两处（实存 %d）", got)
		}

		// ── ② bodies JOIN 块：种类 / 键 / 不得带 ts ────────────────────────
		iLateral := strings.Index(q, lateralMarker)
		if iLateral < 0 {
			t.Fatalf("会话族腿没有 final_full 腿（缺 %q）—— "+
				"E1 门一开，逐轮 outbound_body 是 SQL NULL、快照行的 request_id 是 "+
				"'final_full:<session>' ⇒ 没有这条腿就恒返 0 行", lateralMarker)
		}
		joinBlock := strings.TrimSpace(q[iTurns+len(turns) : iLateral])
		// turns 源的别名（"rl"）不属于 JOIN 块 ⇒ 先摘掉再判形态。
		if f := strings.Fields(joinBlock); len(f) > 0 && f[0] == "rl" {
			joinBlock = strings.TrimSpace(joinBlock[len(f[0]):])
		}
		if !strings.HasPrefix(joinBlock, "LEFT JOIN ") {
			t.Errorf("★ bodies JOIN 必须是 LEFT JOIN（内连接 ⇒ 快照腿永远没机会答）；实得 %q",
				firstN(joinBlock, 48))
		}
		if !strings.Contains(joinBlock, "ON sb.request_id = rl.request_id") {
			t.Error("bodies JOIN 必须 request_id 单键（helper 合同：bodies 源不投影 ts）")
		}
		if strings.Contains(joinBlock, "sb.ts") {
			t.Error("bodies JOIN 出现了 sb.ts —— §9.231 合同：bodies 源**刻意不投影 ts**，" +
				"按 ts 等值 JOIN 是解析期 42703/静默改行")
		}
		if strings.Contains(joinBlock, "sb.session_id") {
			t.Error("bodies JOIN 用了 sb.session_id —— §9.231 的列集合是合同，" +
				"扩列会波及 admin 的 14 个消费点")
		}

		// ── ③ final_full 腿块：按 (tenant, session, kind) 取、只取最新一行 ───
		latEnd := strings.Index(q[iLateral:], ") ff ON TRUE")
		if latEnd < 0 {
			t.Fatalf("final_full 腿没有以 ') ff ON TRUE' 收尾 —— LATERAL 形态变了")
		}
		latBlock := q[iLateral+len(lateralMarker) : iLateral+latEnd]
		for _, needle := range []string{
			"FROM public.session_bodies_unified f",
			"f.tenant_id = $2",
			"f.session_id = $1",
			"f.kind = 'final_full'",
			// ★ 用 helper 的**展开文本**断言（§143.56）：判据只认
			// 「jsonb 口径」（IS NOT NULL AND <> 'null'::jsonb），
			// 不接受裸 `IS NOT NULL`（会话族上对 jsonb null 恒真）。
			db.SessionBodiesHasOutboundPredicate("f"),
			"ORDER BY f.ts DESC",
			"LIMIT 1",
		} {
			if !strings.Contains(latBlock, needle) {
				t.Errorf("final_full 腿缺 %q", needle)
			}
		}

		// ── ④ 投影：COALESCE 的**顺序**（ff 在前）──────────────────────────
		iFrom := strings.Index(q, "FROM "+turns)
		proj := q[:iFrom]
		if !strings.Contains(proj, "COALESCE(ff.outbound_body, sb.outbound_body)") {
			t.Errorf("投影缺 COALESCE(ff.outbound_body, sb.outbound_body)；实得 %q",
				firstN(proj, 120))
		}
		if strings.Contains(proj, "COALESCE(sb.outbound_body, ff.outbound_body)") {
			t.Error("★ COALESCE 的顺序反了（先逐轮后快照）—— 两条腿都在时**不报错**，" +
				"但取到的是旧值")
		}

		// ── ⑤ WHERE 块：保留「有正文」过滤（LEFT JOIN 不放大行集）──────────
		//
		// ★ 2026-10-21（§143.56）：判据从「裸 `IS NOT NULL`」换成
		// **jsonb 口径**（`IS NOT NULL AND <> 'null'::jsonb`）——
		// 会话族的「没有正文」是 jsonb null，裸 `IS NOT NULL` 在会话族上**恒真**
		// （实测 20,643/20,643 通过）⇒ 那个判据等于没写。
		iWhere := strings.Index(q, "WHERE rl.tenant_id = $2")
		iOrder := strings.Index(q, "ORDER BY rl.ts DESC")
		if iWhere < 0 || iOrder < 0 || iOrder < iWhere {
			t.Fatalf("WHERE/ORDER BY 块定位失败（iWhere=%d iOrder=%d）", iWhere, iOrder)
		}
		whereBlock := q[iWhere:iOrder]
		for _, alias := range []string{"sb", "ff"} {
			needle := db.SessionBodiesHasOutboundPredicate(alias)
			if !strings.Contains(whereBlock, needle) {
				t.Errorf("WHERE 缺 %s 腿的「有正文」判据 %q —— "+
					"裸 `IS NOT NULL` 在会话族上恒真（jsonb null）⇒ 它当不了「这轮有正文」",
					alias, needle)
			}
		}
		// ★ 反向判据：裸形态必须**不在**。
		//
		// ⚠ 判别力说明：正确的展开文本是
		//	`(sb.outbound_body IS NOT NULL AND sb.outbound_body <> 'null'::jsonb)`
		// 而裸形态是
		//	`sb.outbound_body IS NOT NULL OR ff.outbound_body IS NOT NULL`
		// 后者**不是**前者的子串（第一个 `IS NOT NULL` 后面跟的是 `AND` 不是 `OR`）
		// ⇒ 这条反向判据有牙。
		if strings.Contains(whereBlock,
			"sb.outbound_body IS NOT NULL OR ff.outbound_body IS NOT NULL") {
			t.Error("★ WHERE 退回裸 `IS NOT NULL` 形态（sb… OR ff…）—— " +
				"会话族的「没有正文」是 jsonb null ⇒ 裸判据恒真 ⇒ " +
				"「这轮有正文」变成了「这轮有行」")
		}
		// WHERE 块必须**同时**含 jsonb 判据（防止有人把 helper 换成一个
		// 语义等价的自造文本时悄悄漏掉 jsonb 那一半）。
		if !strings.Contains(whereBlock, "<> 'null'::jsonb") {
			t.Error("★ WHERE 块里没有 jsonb null 排除（`<> 'null'::jsonb`）—— " +
				"会话族的「没有正文」是 jsonb null，不是 SQL NULL")
		}
	})
}

// firstN 取 s 的前 n 个字符（给报错信息看形态用；n 越界则截断）。
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

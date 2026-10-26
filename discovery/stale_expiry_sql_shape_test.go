package discovery

// stale_expiry_sql_shape_test.go — 2026-10-22，§145 轮。
//
// 生产事故（252，2026-10-21 17:34 部署 → 17:36 首错）：stale 下架 UPDATE 的
// 组装曾把 SessionFamilyTurnsSourceSQL() 的投影拼进 fmt.Sprintf 的**格式串**，
// 投影里的字面 `%`（`LIKE 'sys:%'`）被当成未知动词：
//
//	LIKE 'sys:%!'(int=24)                 ← %' 吃掉了 graceHours
//	interval '%!d(MISSING) hours'         ← %d 无参可吃
//
// ⇒ 整条 UPDATE 必然 syntax error，自部署起 stale 下架功能全灭
// （252 PG 日志：10-21 100 条 + 10-22 半天 111 条 syntax error）。
//
// 本文件钉住修复后的 SQL 形态：投影里的 LIKE 字面量必须原样幸存、
// interval 字面量必须完整、占位符必须真的被注入。修坏任何一处 ⇒ 红。

import (
	"strings"
	"testing"
)

func TestStaleExpiryOffersSQLShape(t *testing.T) {
	sql := staleExpiryOffersSQL("$2,$3,$4", 24)

	// 1) 格式化损坏标记不得出现（fmt 对坏动词/缺参的输出形态）。
	if strings.Contains(sql, "%!") {
		t.Errorf("SQL 含 fmt 损坏标记 %%! —— 投影里的字面 %% 被格式化吃掉了:\n%s",
			strings.Split(sql, "\n")[0])
	}

	// 2) 投影的 LIKE 字面量必须逐字幸存（事故点：被写成 'sys:%!'(int=24)）。
	if !strings.Contains(sql, `LIKE 'sys:%'`) {
		t.Errorf("投影的 LIKE 'sys:%%' 字面量丢失或被改写")
	}

	// 3) interval 字面量必须完整（事故点：'%!d(MISSING) hours'）。
	if !strings.Contains(sql, "interval '24 hours'") {
		t.Errorf("graceHours=24 未落到 interval 字面量（期望 interval '24 hours'）")
	}
	if strings.Contains(sql, "MISSING") {
		t.Error("SQL 含 MISSING —— Sprintf 缺参损坏形态")
	}

	// 4) 占位符真的注入（事故代偿检查：拼错会把列表留在模板外）。
	if !strings.Contains(sql, "NOT IN ($2,$3,$4)") {
		t.Errorf("NOT IN 占位符列表未注入")
	}

	// 5) 证据源是会话族两腿（hot ∪ parent），不再是已退役的 v1 表。
	for _, want := range []string{"session_turns_hot", "session_turns "} {
		if !strings.Contains(sql, want) {
			t.Errorf("证据源缺会话族腿 %q", want)
		}
	}
	if strings.Contains(sql, "_request_logs") {
		t.Error("证据源仍引用已退役的 v1 表 _request_logs*")
	}

	// 6) 0 小时（边界）也要能成句：interval '0 hours' 合法。
	if !strings.Contains(staleExpiryOffersSQL("$2", 0), "interval '0 hours'") {
		t.Errorf("graceHours=0 未落到 interval 字面量")
	}
}

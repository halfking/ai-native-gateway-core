package db

// request_logs_view_schema_apikey_expr_test.go —— R90 的单一真值源门：
// **投影里的 api_key_id 那一列**必须与**过滤侧用的常量**逐字相同。
//
// # 为什么这道门必须住在 db 包
//
// 投影表 `projectionExprsV2` 是**未导出**的 ⇒ 只有同包测试能读到它
// ⇒ 而「投影」与「过滤」必须是**同一个**表达式 ⇒ 这道门只能落在 db 包。
//
// # 为什么它重要
//
// 投影决定「会话列表里展示的 api_key 是哪个值」；过滤决定「哪些会话出现在
// 结果里」。⇒ 两边规则不同 ⇒ 用户按某个 key 过滤，却看不到自己的会话
// ⇒ 而**没有任何报错**。
//
// ★ 历史形态（2026-10-24 R90 之前）：api_key 的过滤读的是 113 列 source 的
//   投影列（同一个表达式）⇒ 天然一致；R90 把它改成两腿 EXISTS 后，
//   过滤侧直接读原始列 ⇒ **必须显式复用同一个常量**，否则就会分叉。
//   ⇒ 这道门就是那个「显式复用」的守卫。
//
// 用法：go test ./db/ -run TestR90_APIKeyExprMatchesProjection -count=1 -v

import "testing"

func TestR90_APIKeyExprMatchesProjection(t *testing.T) {
	// 找出 api_key_id 在投影列序里的位置。
	idx := -1
	for i, name := range canonicalColumnOrderV2 {
		if name == "api_key_id" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("canonicalColumnOrderV2 里没有 api_key_id —— 投影形状变了，请人工复核")
	}
	if idx >= len(projectionExprsV2) {
		t.Fatalf("投影表达式只有 %d 项，取不到第 %d 项", len(projectionExprsV2), idx)
	}
	got := projectionExprsV2[idx]
	if got != SessionFamilyAPIKeyIDExpr {
		t.Errorf("投影里 api_key_id 的表达式 = %q，而过滤侧用的常量 = %q ⇒ "+
			"「展示的 api_key」与「过滤用的 api_key」不是同一个口径",
			got, SessionFamilyAPIKeyIDExpr)
	}
	// ★ 同时钉住常量的**字面形态**：它必须与原始列类型（text）与归一规则一致。
	const expect = "(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END)"
	if SessionFamilyAPIKeyIDExpr != expect {
		t.Errorf("SessionFamilyAPIKeyIDExpr = %q，期望 %q", SessionFamilyAPIKeyIDExpr, expect)
	}
}

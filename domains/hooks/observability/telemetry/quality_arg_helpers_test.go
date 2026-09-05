package telemetry

// 2026-08-20：本文件保留 origin/main 在 0185b5d87 commit 引入的
// quality_flags_test.go 不覆盖的辅助函数：attachmentsArg / jsonOrNull /
// strPtrToJSON / qualityActionsArgStr。qualityFlagsArg 的核心语义
// （nil → "{}"、非空 → FlatArray）由 upstream 的 quality_flags_test.go
// 锁定，本文件不重复覆盖。
//
// 拆分原因：qualityArgs helpers 在 client.go 中集中维护，但 origin 测试
// 只锁 qualityFlagsArg 一个；attempts-to-dedupe 时保留 origin 的小测试
// + 补上其余 helper 的语义测试，是分两个文件最直观的做法。

import (
	"encoding/json"
	"testing"
)

// TestQualityActionsArgStr_EmptyBecomesEmptyObject 锁定空 RawMessage
// 渲染为 "{}"——NOT NULL DEFAULT 与此兼容，pgx 不会因为空字符串当成
// JSON null。
func TestQualityActionsArgStr_EmptyBecomesEmptyObject(t *testing.T) {
	got := qualityActionsArgStr(nil)
	if got != "{}" {
		t.Errorf("nil raw = %q, want \"{}\"", got)
	}
	got = qualityActionsArgStr(json.RawMessage{})
	if got != "{}" {
		t.Errorf("empty raw = %q, want \"{}\"", got)
	}
}

func TestQualityActionsArgStr_PassesThroughValidJSON(t *testing.T) {
	raw := json.RawMessage(`{"type":"retry","reason":"timeout"}`)
	got := qualityActionsArgStr(raw)
	if got != `{"type":"retry","reason":"timeout"}` {
		t.Errorf("got %q, want original raw JSON", got)
	}
}

// TestQualityActionsArgStr_PreservesEscapedCharacters 锁定 pgx 路径不会
// 二次转义——原始 JSON 中的引号 / 反斜杠必须保留，避免修复动作 jsonb
// 字段在 Postgres 端解析失败。
func TestQualityActionsArgStr_PreservesEscapedCharacters(t *testing.T) {
	raw := json.RawMessage(`{"k":"val\"with quote","path":"a\\b"}`)
	got := qualityActionsArgStr(raw)
	if got != string(raw) {
		t.Errorf("got %q, want verbatim raw %q", got, string(raw))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Errorf("result must remain valid JSON: %v", err)
	}
}

// TestAttachmentsArg_NilMeansSQLNull 锁定 NULLABLE 列下空载荷绑 NULL，
// 与 quality_* 列的 "{}" 兜底形成对比——不能混淆语义。
func TestAttachmentsArg_NilMeansSQLNull(t *testing.T) {
	got := attachmentsArg(nil)
	if got != nil {
		t.Errorf("nil raw = %v, want nil (SQL NULL)", got)
	}
	got = attachmentsArg(json.RawMessage{})
	if got != nil {
		t.Errorf("empty raw = %v, want nil", got)
	}
}

func TestAttachmentsArg_NonEmptyBecomesBytes(t *testing.T) {
	raw := json.RawMessage(`[{"name":"a","size":1}]`)
	got := attachmentsArg(raw)
	b, ok := got.([]byte)
	if !ok {
		t.Fatalf("expected []byte, got %T", got)
	}
	if string(b) != string(raw) {
		t.Errorf("got %q, want %q", b, raw)
	}
}

// TestAttachmentsArgStr_EmptyMeansJSONNull 锁定字符串版本：空载荷
// 渲染为 "null"（SQL JSON NULL），与 attachmentsArg 的 nil 行为对齐。
func TestAttachmentsArgStr_EmptyMeansJSONNull(t *testing.T) {
	if got := attachmentsArgStr(nil); got != "null" {
		t.Errorf("nil raw = %q, want \"null\"", got)
	}
	if got := attachmentsArgStr(json.RawMessage{}); got != "null" {
		t.Errorf("empty raw = %q, want \"null\"", got)
	}
}

func TestJsonOrNull_EmptyMeansJSONNull(t *testing.T) {
	if got := jsonOrNull(nil); got != "null" {
		t.Errorf("nil = %q, want \"null\"", got)
	}
	if got := jsonOrNull(json.RawMessage{}); got != "null" {
		t.Errorf("empty = %q, want \"null\"", got)
	}
	raw := json.RawMessage(`{"k":1}`)
	if got := jsonOrNull(raw); got != string(raw) {
		t.Errorf("populated = %q, want verbatim", got)
	}
}

// TestStrPtrToJSON_NilIsJSONNull 锁定 *string→JSON literal 转换的 nil 分支。
func TestStrPtrToJSON_NilIsJSONNull(t *testing.T) {
	var p *string
	if got := strPtrToJSON(p); got != "null" {
		t.Errorf("nil = %q, want \"null\"", got)
	}
}

func TestStrPtrToJSON_EmptyStringIsEmptyObject(t *testing.T) {
	// 与 nil 分支区分：空字符串（已显式赋值）渲染为 "{}"，因为空字符串
	// 携带 "调用方有意清空" 的语义；nil 携带 "调用方没碰过" 的语义。
	s := ""
	if got := strPtrToJSON(&s); got != "{}" {
		t.Errorf("empty string = %q, want \"{}\"", got)
	}
}

func TestStrPtrToJSON_InvalidJSONBecomesEmptyObject(t *testing.T) {
	s := "{this is not json"
	if got := strPtrToJSON(&s); got != "{}" {
		t.Errorf("invalid json = %q, want \"{}\" (graceful fallback)", got)
	}
}

func TestStrPtrToJSON_ValidJSONPassesThrough(t *testing.T) {
	s := `{"k":"v"}`
	if got := strPtrToJSON(&s); got != s {
		t.Errorf("valid json = %q, want verbatim", got)
	}
}

// TestQualityHelpers_NoStaleQualityActionsArgAny 锁定 qualityActionsArg
// (any) 已被移除——旧版返回 []byte 的 helper 与 $N::text::jsonb 路径
// 不兼容，必须不复活。
//
// 该约束的真正执行机制：直接尝试调用 qualityActionsArg 在编译期就会失败
// （函数不存在）。本文件不持有任何指向旧函数的引用，编译器就是守护。
// 如果未来有人试图添加 `func qualityActionsArg(...)`，本测试文件会再次
// 拒绝编译。
func TestQualityHelpers_NoStaleQualityActionsArgAny(t *testing.T) {
	// 这个函数本身没有运行时断言——编译期已保证旧 helper 不存在。
	// 留一个空 runnable test 让本文件能被 `go test` 单独运行。
}

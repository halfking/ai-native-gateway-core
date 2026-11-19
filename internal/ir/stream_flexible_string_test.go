package ir

import (
	"encoding/json"
	"testing"
)

// TestFlexibleStringAcceptsBothStringAndNumber —— 钉测供应商 error.code 的两种形态。
//
// 缺陷背景（2026-11-20 211 网关日志）：12 次/12h 的
// 「cannot unmarshal number into Go struct field .error.code of type string」。
// 根因：原结构体 Code 字段写死 string，而部分供应商返回的是**数字**（如 400）。
// 修法：引入 FlexibleString（同时接受字符串与数字）。
//
// ★ 本门钉的是「两种形态都要能被解析」这一**对外契约**，而不是实现细节：
// 未来若有人把 FlexibleString 改回 string（哪怕加测试），本门必须红。
func TestFlexibleStringAcceptsBothStringAndNumber(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"字符串形态", `"400"`, "400"},
		{"数字形态（缺陷本体）", `400`, "400"},
		{"浮点数字形态", `400.5`, "400.5"},
		{"非数字字符串", `"invalid_request_error"`, "invalid_request_error"},
		{"空字符串", `""`, ""},
		{"null", `null`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got FlexibleString
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("解析 %s 失败：%v（缺陷本体：供应商的数字 code 必须被接受）", tc.raw, err)
			}
			if string(got) != tc.want {
				t.Errorf("解析 %s 得到 %q，期望 %q", tc.raw, string(got), tc.want)
			}
		})
	}
}

// TestFlexibleStringRejectsUnrelatedJSONTypes —— 布尔/数组/对象必须被拒。
//
// ★ 为什么要有「负控」：若 FlexibleString 无条件吞掉一切（例如退化成
// 「失败就返回空串」），则上面那条门会因「解析成功」而假绿 ⇒
// 必须钉住「该拒的确实拒了」，否则本门没有判别力。
func TestFlexibleStringRejectsUnrelatedJSONTypes(t *testing.T) {
	for _, raw := range []string{`true`, `[]`, `[1,2]`, `{}`, `{"a":1}`} {
		var got FlexibleString
		if err := json.Unmarshal([]byte(raw), &got); err == nil {
			t.Errorf("解析 %s 竟然成功了（得到 %q）⇒ 非字符串/数字形态必须被拒绝，"+
				"否则错误响应会被静默吞成空 code", raw, string(got))
		}
	}
}

// TestParseOpenAIStreamChunkNumericErrorCode —— 端到端钉测：真实 SSE 行里的数字 code。
//
// ★ 这一条才是「缺陷本体」的**复现门**：它走的不是 FlexibleString 的单元语义，
// 而是 ParseOpenAIStreamChunk 这条真实入口 —— 2026-11-20 之前的形态下，
// 下面这行会返回 error（解析失败 ⇒ 整个流式帧被丢弃）。
func TestParseOpenAIStreamChunkNumericErrorCode(t *testing.T) {
	line := `data: {"error":{"message":"Invalid request","type":"invalid_request_error","code":400}}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("解析含数字 code 的错误帧失败：%v（缺陷本体）", err)
	}
	if chunk.Error == nil {
		t.Fatal("错误帧解析后 Error 为 nil ⇒ 错误信息被整帧丢弃（缺陷本体）")
	}
	if chunk.Error.Code != "400" {
		t.Errorf("错误 code = %q，期望 \"400\"", chunk.Error.Code)
	}
	if chunk.Error.Message != "Invalid request" {
		t.Errorf("错误 message = %q，期望 \"Invalid request\"", chunk.Error.Message)
	}
	if chunk.Error.Type != "invalid_request_error" {
		t.Errorf("错误 type = %q，期望 \"invalid_request_error\"", chunk.Error.Type)
	}
}

// TestParseOpenAIStreamChunkStringErrorCode —— 兼容性钉测：字符串 code 仍必须可解析。
//
// ★ 变异验证：把 FlexibleString 改回 string ⇒ 本门与上面那条数字门**同时红**
// ⇒ 两门合起来才是「两种形态都在」的完整契约。
func TestParseOpenAIStreamChunkStringErrorCode(t *testing.T) {
	line := `data: {"error":{"message":"Invalid request","type":"invalid_request_error","code":"429"}}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("解析含字符串 code 的错误帧失败：%v", err)
	}
	if chunk.Error == nil {
		t.Fatal("错误帧解析后 Error 为 nil")
	}
	if chunk.Error.Code != "429" {
		t.Errorf("错误 code = %q，期望 \"429\"", chunk.Error.Code)
	}
}

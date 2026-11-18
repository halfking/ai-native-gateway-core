package streaming

import (
	"strings"
	"testing"
)

// ★ D18.3（2026-11-19 审计轮）错误响应脱敏钉测。
//
// 旧实现的两个洞：
//
//	① `sk-ant-api03-XYZ` → `sk-***ant-api03-XYZ`——**密钥主体原样保留**；
//	② `Bearer` 的替换取决于消息里是否出现 "token" 字样——条件与要保护的
//	   内容毫无关系（`Bearer eyJhbGci…` 若不提 token 就原样保留）；
//	③ `Body`（上游响应原文）与 `Details`（上游 error 对象）**零过滤**。

// TestSanitizeErrorMessageRedactsWholeValue 钉住「整值脱敏」语义。
func TestSanitizeErrorMessageRedactsWholeValue(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		mustGo  string // 必须被完全抹掉
		mustNot string // 必须保留（非敏感部分）
	}{
		{
			name:    "anthropic key",
			in:      "upstream rejected key sk-ant-api03-x7Kq2mW9eLbT4vRrN8zA1cD5fGhJ6uM for this request",
			mustGo:  "sk-ant-api03-x7Kq2mW9eLbT4vRrN8zA1cD5fGhJ6uM",
			mustNot: "upstream rejected key",
		},
		{
			name:    "openai key",
			in:      "invalid api key sk-proj-abc123DEF456ghi789JKL012 supplied",
			mustGo:  "sk-proj-abc123DEF456ghi789JKL012",
			mustNot: "invalid api key",
		},
		{
			name:    "github token",
			in:      "bad credentials ghp_16C7e42F292c6912E7710c838347Ae178B4a",
			mustGo:  "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
			mustNot: "bad credentials",
		},
		{
			name:    "bearer token（消息里没有 token 字样）",
			in:      "authorization rejected: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.dozjgNryP4J3jVmNHl0w5N",
			mustGo:  "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.dozjgNryP4J3jVmNHl0w5N",
			mustNot: "authorization rejected",
		},
		{
			name:    "bearer 不透明令牌",
			in:      "header Bearer aB3xY9zQ1wE7rT5yU2iO4pL6kJ8hG0fD2sA4qW6eR8tY0uI2oP4 rejected",
			mustGo:  "aB3xY9zQ1wE7rT5yU2iO4pL6kJ8hG0fD2sA4qW6eR8tY0uI2oP4",
			mustNot: "header",
		},
		{
			name:    "aws access key",
			in:      "AKIAIOSFODNN7EXAMPLE is not authorized",
			mustGo:  "AKIAIOSFODNN7EXAMPLE",
			mustNot: "is not authorized",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeErrorMessage(tc.in)
			if strings.Contains(got, tc.mustGo) {
				t.Fatalf("敏感值未被抹掉：%q 里仍含 %q", got, tc.mustGo)
			}
			if !strings.Contains(got, tc.mustNot) {
				t.Fatalf("非敏感上下文被误伤：%q 不含 %q", got, tc.mustNot)
			}
		})
	}
}

// TestExtractUpstreamErrorInfoRedactsBodyAndDetails 钉住 Body / Details 的
// 过滤（旧实现两者都零过滤）。
func TestExtractUpstreamErrorInfoRedactsBodyAndDetails(t *testing.T) {
	raw := []byte(`{
		"error": {
			"type": "invalid_request_error",
			"message": "bad key sk-ant-api03-x7Kq2mW9eLbT4vRrN8zA1cD5fGhJ6uM",
			"code": "auth_failed",
			"details": {
				"request": {
					"headers": {
						"authorization": "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.dozjgNryP4J3jVmNHl0w5N"
					}
				},
				"attempt": 3
			}
		}
	}`)

	info := ExtractUpstreamErrorInfo(nil, raw, 401)
	if info == nil {
		t.Fatal("ExtractUpstreamErrorInfo returned nil")
	}
	for _, secret := range []string{
		"sk-ant-api03-x7Kq2mW9eLbT4vRrN8zA1cD5fGhJ6uM",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.dozjgNryP4J3jVmNHl0w5N",
	} {
		if strings.Contains(info.Message, secret) {
			t.Fatalf("Message 仍含凭据：%q", info.Message)
		}
		if strings.Contains(info.Body, secret) {
			t.Fatalf("Body 仍含凭据：%q", info.Body)
		}
		if strings.Contains(dumpDetails(info.Details), secret) {
			t.Fatalf("Details 仍含凭据：%q", dumpDetails(info.Details))
		}
	}
	// 非敏感字段不得被破坏。
	if info.Type != "invalid_request_error" {
		t.Fatalf("type 被误伤：%q", info.Type)
	}
	if info.Code != "auth_failed" {
		t.Fatalf("code 被误伤：%q", info.Code)
	}
	if !strings.Contains(info.Message, "bad key") {
		t.Fatalf("message 上下文被误伤：%q", info.Message)
	}
}

// dumpDetails 把 details map 拍平成可断言的字符串。
func dumpDetails(details map[string]interface{}) string {
	var sb strings.Builder
	for k, v := range details {
		sb.WriteString(k)
		sb.WriteString("=")
		switch tv := v.(type) {
		case string:
			sb.WriteString(tv)
		case map[string]interface{}:
			sb.WriteString(dumpDetails(tv))
		case int:
			sb.WriteString("int")
		default:
			sb.WriteString("other")
		}
		sb.WriteString(";")
	}
	return sb.String()
}

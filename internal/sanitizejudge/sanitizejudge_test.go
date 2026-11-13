package sanitizejudge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// r1047 门：影子判官的容错解析（211 实测 8 例仅 1 例裸 JSON——模型爱在 JSON
// 前后垫推理文字）、HTTP 往返、失败与超时语义、FormatNote 审计行。

func TestParseVerdictTolerant(t *testing.T) {
	cases := []struct {
		name, content string
		wantVerdict   string
	}{
		{"裸 JSON", `{"verdict":"benign","confidence":0.95,"reason":"占位符回声"}`, "benign"},
		{"前置推理垫话", "Let me analyze. The excerpt is a process id.\n{\"verdict\":\"benign\",\"confidence\":0.9,\"reason\":\"数字 ID\"}", "benign"},
		{"前置+后置垫话", "Analysis: ...\n{\"verdict\":\"sensitive\",\"confidence\":0.8,\"reason\":\"真实凭据\"}\nDone.", "sensitive"},
		{"无 JSON", "我认为这不是敏感信息。", "unclear"},
		{"JSON 无 verdict 键", `{"answer":"no"}`, "unclear"},
		{"verdict 值非法", `{"verdict":"maybe","confidence":0.5}`, "unclear"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseVerdict(tc.content)
			if got.Verdict != tc.wantVerdict {
				t.Fatalf("verdict=%q want %q (content=%q)", got.Verdict, tc.wantVerdict, tc.content)
			}
		})
	}
	// 解析失败绝不能被当 benign（将来执法阶段 unclear 走 fail-closed）。
	if v := parseVerdict("no json at all"); v.Verdict == "benign" {
		t.Fatal("unparseable output mapped to benign")
	}
}

func TestHTTPJudgeRoundTrip(t *testing.T) {
	var gotAuth, gotModel string
	var gotUserMsg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotModel = payload.Model
		for _, m := range payload.Messages {
			if m.Role == "user" {
				gotUserMsg = m.Content
			}
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"分析完毕。\n{\"verdict\":\"benign\",\"confidence\":0.91,\"reason\":\"命令行数字 ID\"}"}}]}`))
	}))
	defer srv.Close()

	j := NewHTTPJudge(Config{Endpoint: srv.URL, APIKey: "sk-test", Model: "judge-m1", Timeout: 2 * time.Second})
	v, err := j.Judge(context.Background(), Request{RuleID: "financial.bank_card", Excerpt: "seed 1335099688260238922 id"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != "benign" || v.Confidence != 0.91 {
		t.Fatalf("verdict=%+v", v)
	}
	if v.Model != "judge-m1" || v.LatencyMs < 0 {
		t.Fatalf("metadata=%+v", v)
	}
	if gotAuth != "Bearer sk-test" || gotModel != "judge-m1" {
		t.Fatalf("auth=%q model=%q", gotAuth, gotModel)
	}
	// 被审摘录必须以数据形态到达判官（定界 + 规则上下文）。
	if !strings.Contains(gotUserMsg, "<excerpt>") || !strings.Contains(gotUserMsg, "1335099688260238922") ||
		!strings.Contains(gotUserMsg, "financial.bank_card") {
		t.Fatalf("user message lost excerpt/rule: %q", gotUserMsg)
	}
}

func TestHTTPJudgeFailureSemantics(t *testing.T) {
	// HTTP 500 ⇒ error + unclear（不猜）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	j := NewHTTPJudge(Config{Endpoint: srv.URL, APIKey: "k", Model: "m", Timeout: time.Second})
	srv.Close()
	v, err := j.Judge(context.Background(), Request{Excerpt: "x"})
	if err == nil || v.Verdict != "unclear" {
		t.Fatalf("err=%v verdict=%q", err, v.Verdict)
	}
	if !strings.Contains(FormatNote(v, err), "调用失败") {
		t.Fatalf("failure note=%q", FormatNote(v, err))
	}
}

func TestNoopAndShadowEnabled(t *testing.T) {
	if Noop().ShadowEnabled() {
		t.Fatal("noop enabled")
	}
	// FromEnv 缺配置 ⇒ nil（装配层完全不激活）。
	t.Setenv("SANITIZE_JUDGE_ENDPOINT", "")
	if FromEnv() != nil {
		t.Fatal("FromEnv with empty env returned judge")
	}
}

func TestFormatNoteIncludesAuditFields(t *testing.T) {
	note := FormatNote(Verdict{Verdict: "sensitive", Confidence: 0.8, LatencyMs: 4312, Model: "judge-m1", Reason: "真实卡号"}, nil)
	for _, want := range []string{"sensitive", "0.80", "4312ms", "judge-m1", "真实卡号", "影子"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing %q: %q", want, note)
		}
	}
}

package armor

// 2026-11-23（211 配置优化遗留 L2）· 守卫：
//
// ① 「判官未配置」必须**可辨识** —— 它写进 armor_judgments 的行不能与真实
//    判官给出的「安全」长得一样。
//    211 实测：旧实现用 NewMockJudge(0.0, "mock") ⇒ 落库行是
//    judge_model='mock' / score=0 / decision='safe' ⇒ 按 decision='safe'
//    统计「安全通过率」的面会把「根本没判过」当成「判过且安全」
//    （近 3 天 49,889 条判定全是这种）。
// ② 实配判官后必须有**递归防护** —— 判官请求不能再次触发 armor 判定。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── 门 1：未配置判官必须自报家门 ────────────────────────────────────────
func TestUnconfiguredJudgeIsIdentifiable(t *testing.T) {
	j := NewUnconfiguredJudge()
	if !IsUnconfigured(j) {
		t.Error("NewUnconfiguredJudge 没有通过 IsUnconfigured 识别")
	}
	// 普通的 mock（测试用）**不是**未配置判官。
	if IsUnconfigured(NewMockJudge(0.0, "mock")) {
		t.Error("NewMockJudge 被误判成未配置判官：测试用的 mock 与生产占位判官必须可区分")
	}

	resp, err := j.Score(context.Background(), ScoreRequest{Prompt: "hello", Threshold: 0.7})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if resp.JudgeModel != "unconfigured" {
		t.Errorf("JudgeModel = %q，期望 %q（落库后一眼可分）", resp.JudgeModel, "unconfigured")
	}
	if !strings.Contains(strings.ToLower(resp.Reason), "no verdict") {
		t.Errorf("Reason = %q，期望包含「no verdict」：行内必须自解释「这不是安全结论」", resp.Reason)
	}
	// 判定行为本身不变：分数 0、observe 模式下不拦截。
	if resp.Score != 0 {
		t.Errorf("Score = %v，期望 0（占位判官不改变判定语义）", resp.Score)
	}
	if resp.Decision != DecisionSafe {
		t.Errorf("Decision = %v，期望 safe（observe 模式永不拦截）", resp.Decision)
	}
}

// ── 门 2：落库行不得被误读成「安全通过」 ────────────────────────────────
//
// 这条量的是**落库形状**：judge_model 必须是 unconfigured，
// 且 reason 必须说清「没有判定」。
func TestUnconfiguredJudgmentRowIsSelfDescribing(t *testing.T) {
	j := NewUnconfiguredJudge()
	resp, err := j.Score(context.Background(), ScoreRequest{Prompt: "hello", Threshold: 0.7})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	// 这两个字段就是 armor.Judgment 里会落库的那两个。
	if resp.JudgeModel != "unconfigured" {
		t.Errorf("落库 judge_model = %q，期望 unconfigured", resp.JudgeModel)
	}
	if !strings.Contains(strings.ToLower(resp.Reason), "not a safety pass") &&
		!strings.Contains(strings.ToLower(resp.Reason), "no verdict") {
		t.Errorf("落库 reason = %q，期望含「not a safety pass / no verdict」", resp.Reason)
	}
}

// ── 门 3：判官请求必须带标记头（递归防护的一半）─────────────────────────
func TestHTTPJudgeMarksItsOwnRequests(t *testing.T) {
	var gotHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(JudgeRequestMarkerHeader)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"score":0.1,"reason":"ok"}`))
	}))
	defer upstream.Close()

	j, err := NewHTTPJudge(HTTPOptions{BaseURL: upstream.URL, Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewHTTPJudge: %v", err)
	}
	if _, err := j.Score(context.Background(), ScoreRequest{Prompt: "p", Threshold: 0.7}); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if gotHeader != JudgeRequestMarkerValue {
		t.Errorf("判官请求头 %s = %q，期望 %q。\n"+
			"  ★ 少了这个标记，判官指向网关自己时会**无限递归**\n"+
			"    （判官请求 → 网关 → armor 判定 → 判官请求 → …）。",
			JudgeRequestMarkerHeader, gotHeader, JudgeRequestMarkerValue)
	}
}

// ── 门 4：标记识别函数必须认这个头 ──────────────────────────────────────
func TestIsJudgeRequestRecognizesMarker(t *testing.T) {
	h := http.Header{}
	if IsJudgeRequest(h) {
		t.Error("空头被误判成判官请求")
	}
	h.Set(JudgeRequestMarkerHeader, JudgeRequestMarkerValue)
	if !IsJudgeRequest(h) {
		t.Errorf("带 %s 头的请求没有被识别成判官请求", JudgeRequestMarkerHeader)
	}
	// 值不同 ⇒ 不是判官请求（宁可不跳过，也不误跳过真实流量）。
	h.Set(JudgeRequestMarkerHeader, "0")
	if IsJudgeRequest(h) {
		t.Error("标记值不同的请求被误判成判官请求")
	}
}

package main

// crossCheck 的判据。
//
// 这条路径此前**零测试覆盖**——`grep -n corroborat main_test.go` 只在一条无关注释
// 里命中。后果是一个从来没被执行过的 bug 活了下来（2026-10-04 实测）：
//
//	var kept []vendorprice.Candidate
//	for _, c := range p.ReadyToReview {
//	    …三条分支全是 continue，没有一条把 c 放进 kept…
//	    p.Corroborated = append(p.Corroborated, entry)
//	}
//	p.ReadyToReview = kept        // kept 恒为 nil
//
// ⇒ 互证一旦成功（源读到了），**ready_to_review 永远是空的**。而那份清单正是
// 人要逐条过的东西（提案 notice：「ready_to_review 里的条目仍需逐条对照
// source_url 核对」；SSOT 第三步：「人确认后搬进 models」）。互证是附加证据，
// 不是入库许可，把它从待办里拿掉等于把待办删了。
//
// 这条判据用 `httptest` + 仓里**已有**的 `LLM_GATEWAY_PRICE_OBSERVATION_URL`
// 覆盖来驱动真实的抓取与解析路径，不改生产代码的注入面：既是回归判据，也顺带
// 证明整条互证链路（抓取 → 摊平 → 查找 → 判一致）真的能跑通。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
)

// observationServer 起一个 models.dev 形状的观察源，返回其 URL。
func observationServer(t *testing.T, payload map[string]any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// modelsDevShape 造一份最小但形状正确的 models.dev 载荷。
func modelsDevShape(vendor, model string, input, output float64) map[string]any {
	return map[string]any{
		vendor: map[string]any{
			"models": map[string]any{
				model: map[string]any{
					"id": model,
					"cost": map[string]any{
						"input":  input,
						"output": output,
					},
				},
			},
		},
	}
}

func TestCrossCheckKeepsCorroboratedCandidatesInTheReviewList(t *testing.T) {
	// 观察源与候选价**一致** ⇒ 判词应为 corroborated。
	srv := observationServer(t, modelsDevShape("openai", "gpt-5", 1.25, 10))

	t.Setenv("LLM_GATEWAY_PRICE_OBSERVATION_URL", srv)

	p := &proposal{
		ReadyToReview: []vendorprice.Candidate{{
			Vendor: "openai", Model: "GPT-5",
			Input: f64(1.25), Output: f64(10),
			SourceURL:  "https://platform.openai.com/docs/pricing",
			Confidence: vendorprice.ConfidenceTableRow,
			// crossCheck 靠这条 warning 回读解析结果（与 resolveCanonical 同一约定）。
			Warnings: []string{`display name "GPT-5" resolved to canonical "gpt-5" (score 0.95)`},
		}},
	}

	if err := crossCheck(p); err != nil {
		t.Fatalf("crossCheck: %v", err)
	}

	// ★ 这条断言就是那个 bug 的判据。
	if len(p.ReadyToReview) != 1 {
		t.Fatalf("after a successful cross-check ready_to_review has %d entries, want 1 — "+
			"it is the list a human still has to check against source_url, and cross-checking "+
			"is extra evidence, not permission to skip it", len(p.ReadyToReview))
	}
	if len(p.Corroborated) != 1 {
		t.Fatalf("corroborated has %d entries, want 1", len(p.Corroborated))
	}
	if got := p.Corroborated[0].Verdict; got != "corroborated" {
		t.Errorf("verdict = %q, want %q", got, "corroborated")
	}
	// 判词必须**同时**出现在条目上，否则人得离开清单去另一个数组找。
	kept := p.ReadyToReview[0]
	joined := strings.Join(kept.Warnings, " | ")
	if !strings.Contains(joined, "corroborated") {
		t.Errorf("the kept candidate carries no verdict in its warnings (%q) — the human would "+
			"have to leave the review list to learn that this price was cross-checked", joined)
	}
}

func TestCrossCheckFlagsDisagreementAndStillKeepsTheCandidate(t *testing.T) {
	// 观察源与候选价**不一致**（1.25/10 vs 2.00/20）⇒ sources_disagree，
	// 但候选仍必须留在待办里 —— 而且它是**最该被看**的那条。
	srv := observationServer(t, modelsDevShape("openai", "gpt-5", 2.00, 20))
	t.Setenv("LLM_GATEWAY_PRICE_OBSERVATION_URL", srv)

	p := &proposal{
		ReadyToReview: []vendorprice.Candidate{{
			Vendor: "openai", Model: "GPT-5",
			Input: f64(1.25), Output: f64(10),
			SourceURL:  "https://platform.openai.com/docs/pricing",
			Confidence: vendorprice.ConfidenceTableRow,
			Warnings:   []string{`display name "GPT-5" resolved to canonical "gpt-5" (score 0.95)`},
		}},
	}
	if err := crossCheck(p); err != nil {
		t.Fatalf("crossCheck: %v", err)
	}

	if len(p.Corroborated) != 1 || p.Corroborated[0].Verdict != "sources_disagree" {
		t.Fatalf("verdict = %+v, want sources_disagree", p.Corroborated)
	}
	if len(p.ReadyToReview) != 1 {
		t.Fatalf("ready_to_review has %d entries after a disagreement, want 1 — a price the two "+
			"sources disagree about is the one most in need of a human", len(p.ReadyToReview))
	}
	if !strings.Contains(strings.Join(p.ReadyToReview[0].Warnings, " | "), "sources_disagree") {
		t.Error("the disagreement verdict is not visible on the kept candidate")
	}
}

func TestCrossCheckMovesUnmatchedCandidatesOutOfTheReviewList(t *testing.T) {
	// 观察源里**没有**这个模型 ⇒ 候选落到 needs_human_eyes（单源未互证），
	// 且**不该**留在 ready_to_review。另一半的分支也一起量。
	srv := observationServer(t, modelsDevShape("openai", "some-other-model", 1, 2))
	t.Setenv("LLM_GATEWAY_PRICE_OBSERVATION_URL", srv)

	p := &proposal{
		ReadyToReview: []vendorprice.Candidate{{
			Vendor: "openai", Model: "GPT-5",
			Input: f64(1.25), Output: f64(10),
			Confidence: vendorprice.ConfidenceTableRow,
			Warnings:   []string{`display name "GPT-5" resolved to canonical "gpt-5" (score 0.95)`},
		}},
	}
	if err := crossCheck(p); err != nil {
		t.Fatalf("crossCheck: %v", err)
	}

	if len(p.ReadyToReview) != 0 {
		t.Errorf("ready_to_review has %d entries for a candidate with no observation, want 0",
			len(p.ReadyToReview))
	}
	if len(p.NeedsHumanEyes) != 1 {
		t.Fatalf("needs_human_eyes has %d entries, want 1", len(p.NeedsHumanEyes))
	}
	if !strings.Contains(strings.Join(p.NeedsHumanEyes[0].Warnings, " | "), "not corroborated") {
		t.Error("the unmatched candidate does not say it is single-sourced")
	}
}

// TestCrossCheckSaysSoWhenNothingCorroborates 钉「一条都没互证上」这件事**被说出来**。
//
// 为什么：这种情况让 ready_to_review 变空，而空的待办清单最容易被读成「没有待办」。
// 尤其不能 return error —— 调用方会把任何 error 记成
// `corroboration_refused = "the observation source could not be read"`，而源明明读
// 到了、只是没匹配上。那是一句假话，且会把人引去查网络，而问题在 canonical 名单
// 或展示名上。⇒ 判据钉「不返回 error」+ 「stderr 说了」。
func TestCrossCheckSaysSoWhenNothingCorroborates(t *testing.T) {
	srv := observationServer(t, modelsDevShape("openai", "some-other-model", 1, 2))
	t.Setenv("LLM_GATEWAY_PRICE_OBSERVATION_URL", srv)

	p := &proposal{
		ReadyToReview: []vendorprice.Candidate{{
			Vendor: "openai", Model: "GPT-5",
			Input: f64(1.25), Output: f64(10),
			Confidence: vendorprice.ConfidenceTableRow,
			Warnings:   []string{`display name "GPT-5" resolved to canonical "gpt-5" (score 0.95)`},
		}},
	}
	stderr := captureStderr(t, func() {
		// 刻意**不** t.Fatalf：本判据要量的就是「它返回 nil」。
		if err := crossCheck(p); err != nil {
			t.Errorf("crossCheck returned %v — the caller turns any error into "+
				"\"the observation source could not be read\", which is FALSE here (the source "+
				"was read; nothing matched)", err)
		}
	})
	if !strings.Contains(stderr, "corroborated NONE") {
		t.Errorf("nothing was corroborated but stderr did not say so; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "NOT because") {
		t.Errorf("the warning does not distinguish \"nothing to check\" from \"nothing checked\"; got:\n%s", stderr)
	}
	// examined > 0 的这一支必须指路 needs_human_eyes（名字过了、源里查不到）。
	if !strings.Contains(stderr, "needs_human_eyes") {
		t.Errorf("the warning does not point at needs_human_eyes even though names DID resolve "+
			"(examined=1, corroborated=0) — the operator is left without a next step; got:\n%s", stderr)
	}
}

// TestCrossCheckPointsAtUnresolvedNamesWhenNothingWasExamined 钉另一种「零互证」的指路。
//
// 候选在更早的**名字解析**那步就全被挡住时，压根没走到互证（examined == 0），
// 它们在 unresolved_names 里。此时说「去 needs_human_eyes 看」会把人引到错误的桶
// —— 那是真不可用的价，与这批「只是名字没匹配上」的不是一回事。
func TestCrossCheckPointsAtUnresolvedNamesWhenNothingWasExamined(t *testing.T) {
	srv := observationServer(t, modelsDevShape("openai", "unrelated", 1, 2))
	t.Setenv("LLM_GATEWAY_PRICE_OBSERVATION_URL", srv)

	p := &proposal{
		ReadyToReview: nil, // 名字解析那步已把候选全挪进 Unresolved
		Unresolved:    []unresolved{{DisplayName: "Claude Fable 5.1", BestScore: 0.6}},
		NeedsHumanEyes: []vendorprice.Candidate{{
			Vendor: "openai", Model: "some-unusable-row",
			Confidence: vendorprice.ConfidenceUnusable,
		}},
	}
	stderr := captureStderr(t, func() {
		if err := crossCheck(p); err != nil {
			t.Errorf("crossCheck returned %v — any error becomes \"the observation source could not "+
				"be read\", which is false (the source was read; nothing reached the cross-check)", err)
		}
	})
	if !strings.Contains(stderr, "unresolved_names") {
		t.Errorf("examined 0 but the warning does not point at unresolved_names; got:\n%s", stderr)
	}
	if strings.Contains(stderr, "needs_human_eyes") {
		t.Errorf("examined 0 yet the warning points at needs_human_eyes — that bucket holds genuinely "+
			"unusable rows, and sending the operator there mixes two unrelated things; got:\n%s", stderr)
	}
}

func f64(v float64) *float64 { return &v }

// captureStderr 抓一个闭包往 os.Stderr 写的内容。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, e := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if e != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestCrossCheckExplainsUnresolvedDisplayNames 钉「名字没解析出来」这条理由必须写在条目上。
//
// 为什么：提案的 notice 说 needs_human_eyes 里的条目「**不可用**」，而展示名没
// 在名单里匹配上的那条**恰恰是好价**——只是名字需要人裁决。这条分支原先一声不吭
// （旁边那条「no observation」反倒写了理由），于是「名字没匹配」与「划线原价」
// 「单位不是 per 1M」这类**真不可用**在产出里长得一模一样，看的人只能逐条回页面
// 才知道，而那正是这套流程要替人省掉的活。
func TestCrossCheckExplainsUnresolvedDisplayNames(t *testing.T) {
	// 观察源里没有这个厂商，LookupObservation 也不会被走到 —— 这条只测 canonical
	// 解析失败那条分支，所以给什么都行；但仍起真源，保证走的是完整抓取路径。
	srv := observationServer(t, modelsDevShape("openai", "unrelated", 1, 2))
	t.Setenv("LLM_GATEWAY_PRICE_OBSERVATION_URL", srv)

	p := &proposal{
		ReadyToReview: []vendorprice.Candidate{{
			Vendor: "openai", Model: "Some Brand New Model",
			Input: f64(1.25), Output: f64(10),
			Confidence: vendorprice.ConfidenceTableRow,
			// 故意**不带** resolved-canonical 那条 warning ⇒ canonical == ""
		}},
	}
	if err := crossCheck(p); err != nil {
		t.Fatalf("crossCheck: %v", err)
	}
	if len(p.NeedsHumanEyes) != 1 {
		t.Fatalf("needs_human_eyes = %d, want 1", len(p.NeedsHumanEyes))
	}
	joined := strings.Join(p.NeedsHumanEyes[0].Warnings, " | ")
	if !strings.Contains(joined, "did not resolve to any canonical name") {
		t.Errorf("an unresolved display name landed in needs_human_eyes without saying why; "+
			"the bucket is documented as \"unusable\" and this row is very likely NOT. got:\n%s", joined)
	}
	// 并且要点明「这不是价格的问题」，否则人还是会当成废价丢掉。
	if !strings.Contains(joined, "may well be correct") {
		t.Errorf("the warning does not tell the reader the price may be fine and only the name "+
			"needs a decision; got:\n%s", joined)
	}
}

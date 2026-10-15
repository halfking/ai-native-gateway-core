package errorsx

import (
	"strings"
	"testing"
	"time"
)

// TestParseQuotaResetAt_Corpus replays real upstream 429/402 bodies (from
// classify_test.go / classify429_test.go / regression_apigpt_credit_test.go
// corpora) plus the P0-4 doc's specified shapes (UTC+8 suffix, relative
// durations, \uXXXX escapes). Relative expectations are parameterized on a
// fixed `now`; ok=false rows are negative expectations.
func TestParseQuotaResetAt_Corpus(t *testing.T) {
	// Corpus samples come from different incidents (July–October); the parser
	// by design rejects timestamps in the past, so each row carries the `now`
	// its incident implies (zero → defaultNow). Relative rows use defaultNow.
	defaultNow := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		body string
		want time.Time
		ok   bool
		now  time.Time
	}{
		// --- P0-4 doc-specified shapes ---
		{
			name: "中文绝对时刻 UTC+8 后缀（任务原文形态）",
			body: `{"error":{"message":"配额超限，将在 2026-10-11 20:16:02 UTC+8重置"}}`,
			want: time.Date(2026, 10, 11, 12, 16, 2, 0, time.UTC), // 20:16:02+08:00 → 12:16:02Z
			ok:   true,
		},
		{
			name: "中文 预计于…恢复 UTC+8",
			body: `{"error":{"message":"预计于 2026-10-11 20:16:02 UTC+8 恢复"}}`,
			want: time.Date(2026, 10, 11, 12, 16, 2, 0, time.UTC),
			ok:   true,
		},
		{
			name: "英文 RFC3339 will reset at Z",
			body: `{"error":{"message":"rate limit exceeded, will reset at 2026-10-11T20:16:02Z"}}`,
			want: time.Date(2026, 10, 11, 20, 16, 2, 0, time.UTC),
			ok:   true,
		},
		{
			name: "resets in 0h 19min（相对）",
			body: `{"error":{"message":"quota exceeded, resets in 0h 19min"}}`,
			want: defaultNow.Add(19 * time.Minute),
			ok:   true,
		},
		{
			name: "try again in 1m30s（相对）",
			body: `{"error":{"message":"try again in 1m30s"}}`,
			want: defaultNow.Add(90 * time.Second),
			ok:   true,
		},
		{
			name: "will reset after 5 hours（相对，锚定 reset after）",
			body: `{"error":{"message":"usage limit exceeded, it will reset after 5 hours"}}`,
			want: defaultNow.Add(5 * time.Hour),
			ok:   true,
		},
		{
			name: "裸 after 5 hours 字面形态（最后回退）",
			body: `{"error":{"message":"quota exhausted, come back after 5 hours"}}`,
			want: defaultNow.Add(5 * time.Hour),
			ok:   true,
		},
		{
			name: "无 after/in 锚的节奏短语不可解析（负例）",
			body: `{"error":{"message":"quota exhausted. Access resumes 5 hours"}}`,
			ok:   false,
		},
		{
			name: "中文相对 5 小时后重置",
			body: `{"error":{"message":"额度已用尽，将在 5 小时后重置"}}`,
			want: defaultNow.Add(5 * time.Hour),
			ok:   true,
		},
		{
			name: "中文相对 30 分钟后恢复",
			body: `{"error":{"message":"预计 30 分钟后恢复"}}`,
			want: defaultNow.Add(30 * time.Minute),
			ok:   true,
		},
		{
			name: "中文相对 1小时30分钟后重置（复合）",
			body: `{"error":{"message":"将在 1小时30分钟后重置"}}`,
			want: defaultNow.Add(90 * time.Minute),
			ok:   true,
		},
		// --- \uXXXX 转义体（任务指定）---
		{
			name: "\\uXXXX 转义的中文重置体（将在…UTC+8重置）",
			body: `{"error":{"message":"\u60a8\u7684\u914d\u989d\u5c06\u5728 2026-10-11 20:16:02 UTC+8\u91cd\u7f6e"}}`,
			want: time.Date(2026, 10, 11, 12, 16, 2, 0, time.UTC),
			ok:   true,
		},
		// --- 真实语料（既有测试文件中的上游原文）---
		{
			name: "智谱AI 1310 限额将在…重置（classify_test.go 语料，naive→UTC）",
			body: `{"error":{"code":"1310","message":"您已达到每周/每月使用上限，您的限额将在 2026-07-19 21:32:20 重置。"}}`,
			want: time.Date(2026, 7, 19, 21, 32, 20, 0, time.UTC),
			ok:   true,
			now:  time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "quota exceeded, will reset at（classify_test.go 语料）",
			body: `quota exceeded, will reset at 2026-08-01 00:00:00`,
			want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			ok:   true,
			now:  time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Gemini RESOURCE_EXHAUSTED reset after RFC3339（classify429_test.go 语料）",
			body: `{"error":{"code":429,"message":"Quota exceeded for quota metric 'GenerateContent' and limit 'GenerateContent' per minute. The quota will reset after 2026-08-10T12:34:56Z.","status":"RESOURCE_EXHAUSTED"}}`,
			want: time.Date(2026, 8, 10, 12, 34, 56, 0, time.UTC),
			ok:   true,
			now:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "monthly quota will reset at（classify429_test.go 语料）",
			body: `{"error":{"message":"usage limit exceeded","window_type":"monthly","quota will reset at 2026-08-11 00:00:00"}}`,
			want: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
			ok:   true,
			now:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "中文语料 usage limit exceeded，您的限额将在…重置（classify_test.go 语料）",
			body: `{"error":"usage limit exceeded，您的限额将在 2026-08-10 12:00:00 重置。"}`,
			want: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
			ok:   true,
			now:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "usage limit exceeded, resets every 5 hours（窗口节奏，无时间点→不可解析）",
			body: `{"error":{"type":"usage_limit_exceeded","message":"usage limit exceeded, resets every 5 hours"}}`,
			// "resets every 5 hours" 是节奏不是时刻；本解析器不承载该语义
			// （NextQuotaReset 的 five-hour 边界计算负责），必须 not-ok。
			ok: false,
		},
		{
			name: "斜杠日期形态",
			body: `{"error":{"message":"quota will reset at 2026/10/15 08:30:00"}}`,
			want: time.Date(2026, 10, 15, 8, 30, 0, 0, time.UTC),
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rowNow := tt.now
			if rowNow.IsZero() {
				rowNow = defaultNow
			}
			got, ok := ParseQuotaResetAt([]byte(tt.body), rowNow)
			if !tt.ok {
				if ok {
					t.Fatalf("ParseQuotaResetAt(%q) = %v ok, want not-ok", tt.body, got)
				}
				return
			}
			if !ok {
				t.Fatalf("ParseQuotaResetAt(%q) not ok, want %v", tt.body, tt.want)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("ParseQuotaResetAt(%q) = %v, want %v", tt.body, got.UTC(), tt.want)
			}
		})
	}
}

func TestParseQuotaResetAt_PastTimestampIsNotOk(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	// A reset that already elapsed is not a usable cooling target.
	if _, ok := ParseQuotaResetAt([]byte(`quota will reset at 2020-01-01 00:00:00`), now); ok {
		t.Fatal("past timestamp should not parse as a reset hint")
	}
}

func TestParseQuotaResetAt_GarbageBodiesAreNotOk(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, body := range []string{
		``,
		`{"error":"too many requests, please slow down"}`,
		`{"error":{"message":"您已达到每周使用上限"}}`, // 无任何时间线索
		`insufficient credits`,               // credit 类不含时间
	} {
		if _, ok := ParseQuotaResetAt([]byte(body), now); ok {
			t.Fatalf("ParseQuotaResetAt(%q) unexpectedly ok", body)
		}
	}
}

// TestParseQuotaResetAt_RelativeUsesInjectedNow pins that relative hints are
// computed against the injected now, not the wall clock (mirrors
// TestNextQuotaReset_UsesInjectedNowForTimestampStaleness).
func TestParseQuotaResetAt_RelativeUsesInjectedNow(t *testing.T) {
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	got, ok := ParseQuotaResetAt([]byte(`quota exceeded, resets in 19min`), now)
	if !ok {
		t.Fatal("expected ok")
	}
	want := now.Add(19 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.UTC(), want)
	}
}

// ---------------------------------------------------------------------------
// 402 credit / quota 二分
// ---------------------------------------------------------------------------

// TestIsCreditExhausted_MoneyVsQuotaCorpus: money 文案 → credit 谓词真；
// quota 窗口文案 → 假（前提在场配对：同一谓词必须同时给出两侧）。
func TestIsCreditExhausted_MoneyVsQuotaCorpus(t *testing.T) {
	moneyBodies := []string{
		`{"error":{"message":"insufficient credit","type":"insufficient_credit"}}`,
		`{"error":{"message":"Insufficient account balance"}}`,
		`{"error":{"message":"You are out of credits"}}`,
		`{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
		`{"error":{"message":"费用用完，请充值"}}`,
		`{"error":{"message":"余额不足"}}`,
		`{"error":{"message":"账户欠费，请及时充值"}}`,
		`{"error":{"message":"billing arrears on your account"}}`,
	}
	quotaBodies := []string{
		`{"error":{"message":"quota exceeded, will reset at 2026-08-01 00:00:00"}}`,
		`{"error":{"message":"usage limit exceeded","window_type":"monthly"}}`,
		`{"error":{"message":"您已达到每周使用上限"}}`,
		`{"error":{"message":"rate window limit exceeded"}}`,
		`{"error":"too many requests"}`,
	}
	for _, body := range moneyBodies {
		if !IsCreditExhausted(KindQuotaPermanent, []byte(body)) {
			t.Errorf("IsCreditExhausted(quota kind, %q) = false, want true (money corpus)", body)
		}
	}
	for _, body := range quotaBodies {
		if IsCreditExhausted(KindQuotaPeriodic, []byte(body)) {
			t.Errorf("IsCreditExhausted(quota kind, %q) = true, want false (quota-window corpus)", body)
		}
	}
	// \uXXXX 转义的 money 体也必须命中（与 ParseQuotaResetAt 同一解码链）。
	escaped := `{"error":{"message":"\u4f59\u989d\u4e0d\u8db3\uff0c\u8bf7\u5145\u503c"}}` // 余额不足，请充值
	if !IsCreditExhausted(KindQuota, []byte(escaped)) {
		t.Errorf("IsCreditExhausted(escaped money body) = false, want true")
	}
}

// TestIsCreditExhausted_KindGuard: 非 quota 族 kind 即使带 money 词也必须
// 返回 false —— 谓词只做「已判定为 quota 的失败」的二分，不做分类。
func TestIsCreditExhausted_KindGuard(t *testing.T) {
	body := []byte(`{"error":{"message":"insufficient credits"}}`)
	for _, kind := range []ErrorKind{KindAuth, KindRateLimit, KindTransient, KindModelNotFound, KindUpstreamDown} {
		if IsCreditExhausted(kind, body) {
			t.Errorf("IsCreditExhausted(%q, money body) = true, want false (non-quota kind guard)", kind)
		}
	}
}

// TestGradeQuotaRest_Precedence pins the documented precedence:
// absolute > credit word > anchored relative > bare after > permanent.
func TestGradeQuotaRest_Precedence(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	// 1. Absolute beats money word ("balance insufficient, will reset at X"
	// is a rolling budget window — Anthropic style).
	h := GradeQuotaRest([]byte(`{"error":{"message":"Organization balance insufficient, will reset at 2026-10-15 00:00:00","code":"budget_exceeded"}}`), now)
	if h.Class != QuotaRestPeriodic || !h.HasReset || h.Source != "absolute_timestamp" {
		t.Fatalf("absolute-beats-credit: got %+v", h)
	}
	if !h.ResetAt.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("absolute reset wrong: %v", h.ResetAt)
	}

	// 2. Money word beats boilerplate relative hints (no absolute present).
	h = GradeQuotaRest([]byte(`{"error":{"message":"insufficient credits, please top up and try again later"}}`), now)
	if h.Class != QuotaRestCredit || h.Source != "credit_word" {
		t.Fatalf("credit: got %+v", h)
	}
	if !h.HasReset || h.ResetAt.Sub(now) != CreditExhaustedCooling {
		t.Fatalf("credit cooling = %v, want %v", h.ResetAt.Sub(now), CreditExhaustedCooling)
	}

	// 3. Anchored relative for a plain quota window.
	h = GradeQuotaRest([]byte(`{"error":{"message":"usage limit exceeded, resets in 0h 19min"}}`), now)
	if h.Class != QuotaRestPeriodic || h.Source != "relative_duration" {
		t.Fatalf("anchored relative: got %+v", h)
	}
	if !h.ResetAt.Equal(now.Add(19 * time.Minute)) {
		t.Fatalf("anchored relative reset wrong: %v", h.ResetAt)
	}

	// 4. Bare "after" fallback.
	h = GradeQuotaRest([]byte(`{"error":{"message":"quota exhausted, come back after 2 hours"}}`), now)
	if h.Class != QuotaRestPeriodic || h.Source != "bare_after" {
		t.Fatalf("bare after: got %+v", h)
	}

	// 5. Nothing usable → permanent (legacy behaviour preserved).
	h = GradeQuotaRest([]byte(`{"error":{"message":"individual quota reached"}}`), now)
	if h.Class != QuotaRestPermanent || h.HasReset {
		t.Fatalf("permanent: got %+v", h)
	}
}

// ---------------------------------------------------------------------------
// 模型级隔离
// ---------------------------------------------------------------------------

// TestModelScopedRejection_ExtractsNamedModel replays the model-absent
// corpus (positive premise: these bodies DO name a model).
func TestModelScopedRejection_ExtractsNamedModel(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"openai quotes", `{"error":{"message":"The model 'gpt-5-fake' does not exist","type":"invalid_request_error"}}`, "gpt-5-fake"},
		{"anthropic colon", `{"type":"error","error":{"type":"not_found_error","message":"model: claude-opus-4-6 not found"}}`, "claude-opus-4-6"},
		{"bare form", `model glm-5.1 not found`, "glm-5.1"},
		{"deprecated with successor", `{"error":"Model glm-4 has been deprecated, please use glm-5"}`, "glm-4"},
		{"retired", `model glm-4 has been retired`, "glm-4"},
		{"EOL", `{"detail":"Model nvidia/llama-3.3-70b has reached its end of life and is no longer available"}`, "nvidia/llama-3.3-70b"},
		{"NIM function", `{"status":404,"title":"Not Found","detail":"Function 'nvidia/nemotron-12b': Not found for account 'acct-1'"}`, "nvidia/nemotron-12b"},
		{"no such model", `{"error":{"message":"no such model: kimi-k2-turbo"}}`, "kimi-k2-turbo"},
		{"中文 模型不存在", `{"error":{"message":"模型 glm-5.2 不存在"}}`, "glm-5.2"},
		{"中文 已下线", `模型 gpt-4o-mini 已下线`, "gpt-4o-mini"},
		{"endpoint form", `{"error":{"message":"endpoint v1/deprecated-model is unknown"}}`, "v1/deprecated-model"},
		{"\\uXXXX 转义中文", `{"error":{"message":"\u6a21\u578b glm-5.2 \u4e0d\u5b58\u5728"}}`, "glm-5.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ModelScopedRejection([]byte(tt.body))
			if !ok {
				t.Fatalf("ModelScopedRejection(%q) not ok, want model %q", tt.body, tt.want)
			}
			if got != tt.want {
				t.Fatalf("ModelScopedRejection(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestModelScopedRejection_NegativeCorpus: errors that do NOT name a model
// must return ok=false — the caller keeps its credential-level behaviour
// (P0-4 precondition: no false positives, no behaviour change).
func TestModelScopedRejection_NegativeCorpus(t *testing.T) {
	bodies := []string{
		// region/tier restriction — the historical modelNotFoundRe false positive
		`Model is not available in your region`,
		// training job error — "model" and "not found" unrelated
		`Your previous model training run was not found, retry with a new id`,
		// name-less variants
		`model does not exist`,
		`model not found`,
		`model is unknown`,
		// JSON code label without an identifier
		`{"error":{"message":"No available channel for model gpt-5.6-luna under group Codex-特价 (distributor)","type":"new_api_error","code":"model_not_found"}}`,
		// generic web 404
		`404 page not found`,
		// quota / overload bodies
		`{"error":{"message":"quota exceeded, will reset at 2026-08-01 00:00:00"}}`,
		`{"error":{"message":"insufficient credits"}}`,
		`{"error":{"message":"concurrent limit exceeded for this account"}}`,
		// client tool bug with "not found" on a tool id
		`{"error":{"code":2013,"message":"invalid params, tool result's tool id (call_x1) not found","type":"invalid_request"}}`,
	}
	for _, body := range bodies {
		if name, ok := ModelScopedRejection([]byte(body)); ok {
			t.Errorf("ModelScopedRejection(%q) = (%q, true), want false", body, name)
		}
	}
}

// TestModelScopedRejection_ModelNotFoundCorpusPremise: 前提在场——被
// ClassifyErrorWithBody 判为 KindModelNotFound/KindModelDeprecated 的真实
// 语料必须能提出模型名（判据纪律：允许侧「看不见」要单独判掉）。
func TestModelScopedRejection_ModelNotFoundCorpusPremise(t *testing.T) {
	corpus := []struct {
		status int
		body   string
	}{
		{404, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-opus-4-6 not found"}}`},
		{404, `{"error":{"message":"The model 'gpt-5-fake' does not exist","type":"invalid_request_error"}}`},
		{410, `{"detail":"Model nvidia/llama-3.3-70b has reached its end of life and is no longer available"}`},
		{404, `{"status":404,"title":"Not Found","detail":"Function 'nvidia/nemotron-12b': Not found for account 'acct-1'"}`},
	}
	for _, c := range corpus {
		kind := ClassifyErrorWithBody(c.status, []byte(c.body))
		if kind != KindModelNotFound && kind != KindModelDeprecated {
			t.Fatalf("premise broken: ClassifyErrorWithBody(%d, %q) = %q, want model family", c.status, c.body, kind)
		}
		if _, ok := ModelScopedRejection([]byte(c.body)); !ok {
			t.Errorf("model-family body failed extraction: %q", c.body)
		}
	}
}

func TestIsQuotaExhaustion_Family(t *testing.T) {
	for _, kind := range []ErrorKind{KindQuota, KindQuotaPeriodic, KindQuotaBalance, KindQuotaPermanent} {
		if !IsQuotaExhaustion(kind) {
			t.Errorf("IsQuotaExhaustion(%q) = false, want true", kind)
		}
	}
	for _, kind := range []ErrorKind{KindRateLimit, KindAuth, KindTransient, KindConcurrent, KindModelNotFound} {
		if IsQuotaExhaustion(kind) {
			t.Errorf("IsQuotaExhaustion(%q) = true, want false", kind)
		}
	}
}

// TestDecodeResetUnicodeEscapes_SurrogatePair pins the \uXXXX decoder on the
// astral-plane case (emoji in vendor error text) and malformed escapes.
func TestDecodeResetUnicodeEscapes_SurrogatePair(t *testing.T) {
	got := decodeResetUnicodeEscapes(`\uD83D\uDE00 ok`)
	if got != "😀 ok" {
		t.Fatalf("surrogate pair decode = %q", got)
	}
	if got := decodeResetUnicodeEscapes(`plain ascii`); got != "plain ascii" {
		t.Fatalf("ascii passthrough = %q", got)
	}
	if got := decodeResetUnicodeEscapes(`bad \uZZZZ keep`); !strings.Contains(got, `\uZZZZ`) {
		t.Fatalf("malformed escape must be kept raw, got %q", got)
	}
}

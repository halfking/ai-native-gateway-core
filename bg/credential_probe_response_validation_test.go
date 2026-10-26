package bg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/pashagolub/pgxmock/v4"
)

func TestCredentialProbeRequiresAssistantOutput(t *testing.T) {
	positive := map[string]string{
		"chat":      `{"choices":[{"message":{"content":"pong"}}]}`,
		"messages":  `{"type":"message","content":[{"type":"text","text":"pong"}]}`,
		"responses": `{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}`,
	}
	negative := []string{"", "<html>busy</html>", "{broken", `{}`, `{"error":{"message":"busy"}}`,
		`{"choices":"invalid"}`, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"","arguments":""}}]}}]}`,
		`{"content":[{"type":"thinking","thinking":"thinking"}],"stop_reason":"max_tokens"}`,
		`{"output":[{"type":"reasoning","summary":[{"text":"thinking"}]}],"status":"incomplete"}`,
		`{"type":"error","content":[{"type":"text","text":"error prose"}]}`,
		`{"status":"failed","output":[{"type":"message","content":[{"type":"output_text","text":"failed prose"}]}]}`,
		`{"status":"cancelled","output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`,
		`{"status":"in_progress","output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`,
		`{"status":"queued","output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`,
	}
	// 2026-10-26 多模态标准接口轮 + 2026-10-27 审计轮语义分家：credential
	// 探针（本测试驱动的是 miniChat = hasOutput || livenessEvidence）对
	// chat 的 reasoning-only 判「活」——thinking 模型在 32-token 探针载荷下
	// content 恒空，不认 reasoning 则凭据健康检查永远翻不了绿（211 实测
	// 活锁）。共享判据 credentialProbeHasOutput 本身仍维持 reasoning 负例
	// （node/model/active 探针的 deliverability 语义），由下方
	// TestCredentialProbeSharedValidatorExcludesReasoning 单独锁定。
	chatReasoningPositive := []string{
		`{"choices":[{"message":{"reasoning_content":"thinking"},"finish_reason":"length"}]}`,
		`{"choices":[{"message":{"content":"","reasoning":"嗯，探针活性的思考"},"finish_reason":"length"}]}`,
	}
	for protocol, valid := range positive {
		cases := append(append([]string{}, negative...), valid, strings.Replace(valid, "pong", strings.Repeat("p", 2048), 1))
		if protocol == "responses" {
			cases = append(cases, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}`)
		}
		negCount := len(negative)
		if protocol == "chat" {
			// reasoning-only 正例挂在负例之后（want=true 段）。
			cases = append(cases, chatReasoningPositive...)
		}
		for i, body := range cases {
			t.Run(fmt.Sprintf("%s/%d", protocol, i), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, body)
				}))
				defer srv.Close()
				c := &CredentialProbeV2{}
				var ok bool
				var detail string
				switch protocol {
				case "chat":
					ok, detail = c.miniChat(t.Context(), srv.Client(), "", "test", srv.URL, 32)
				case "messages":
					ok, detail = c.miniAnthropic(t.Context(), srv.Client(), "", "test", srv.URL, 32)
				case "responses":
					ok, detail, _, _ = c.miniResponses(t.Context(), srv.Client(), "", "test", srv.URL)
				}
				if want := i >= negCount; ok != want {
					t.Fatalf("ok=%v want=%v detail=%q", ok, want, detail)
				}
			})
		}
	}
}

type credentialProbeTestTransport func(*http.Request) (*http.Response, error)

func (f credentialProbeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type credentialProbeBrokenReader struct{}

func (credentialProbeBrokenReader) Read([]byte) (int, error) { return 0, errors.New("broken body") }

func TestCredentialProbeRejectsIncompleteOrOversizedBody(t *testing.T) {
	for _, protocol := range []string{"chat", "messages", "responses"} {
		for _, status := range []int{200, 401, 403} {
			for _, readFailure := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/read-error-%v", protocol, status, readFailure), func(t *testing.T) {
					prefix := map[string]string{
						"chat":      `{"choices":[{"message":{"content":"pong"}}]}`,
						"messages":  `{"content":[{"type":"text","text":"pong"}]}`,
						"responses": `{"output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}`,
					}[protocol]
					// The prefix is valid: silently accepting a truncated body
					// would appear healthy, so this catches a broken size guard.
					body := io.Reader(strings.NewReader(prefix + strings.Repeat(" ", credentialProbeBodyLimit+1)))
					if readFailure {
						body = io.MultiReader(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`), credentialProbeBrokenReader{})
					}
					client := &http.Client{Transport: credentialProbeTestTransport(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: status, Body: io.NopCloser(body), Header: make(http.Header)}, nil
					})}
					c := &CredentialProbeV2{}
					var ok bool
					var detail string
					switch protocol {
					case "chat":
						ok, detail = c.miniChat(t.Context(), client, "", "test", "http://probe.test", 32)
					case "messages":
						ok, detail = c.miniAnthropic(t.Context(), client, "", "test", "http://probe.test", 32)
					case "responses":
						ok, detail, _, _ = c.miniResponses(t.Context(), client, "", "test", "http://probe.test")
					}
					pr := classifyProbeFailure(detail)
					if ok || (status == 200 && !pr.ObservationOnly) ||
						(status != 200 && (pr.AvailabilityState != "auth_failed" || pr.ObservationOnly)) {
						t.Fatalf("response body failure lost status evidence: status=%d ok=%v detail=%s verdict=%+v", status, ok, detail, pr)
					}
				})
			}
		}
	}
}

// credentialProbeObservationSQLPattern pins the **exact** SET list of the
// observation write.
//
// ★★ 2026-10-27（R152 轮 · D9c）：这条正则的**本意是负面守卫** ——
// 「SET 与 WHERE 之间塞不进任何权威列」⇒ ⇒ 所以它必须**逐项枚举** SET，
// 而不是用一段宽松的 `.*` 容忍任意列。
//
// ⇒ ⇒ D9c 往 SET 里加了 `probe_consecutive_failures`（**节奏**列，
// **非**权威列：它只驱动 auth 退避阶梯与 quota 探针 due 门；健康结论走
// `health_status` / `availability_state` / `state_reason_code`，而
// ObservationOnly 路径整条跳过那个写入）⇒ ⇒ 夹具必须**跟着枚举**，
// **不是放宽** —— 放宽就等于把守卫拆了。
//
// ⇒ ⇒ ★★ 下面配一条**阴性对照**（把权威列塞回 SET ⇒ 必须仍然拒绝）
// ⇒ ⇒ 否则这条夹具会退化成「恒绿门」（R152 教训：门绿 ≠ 事实成立）。
var credentialProbeObservationSQLPattern = regexp.MustCompile(
	`(?s)^UPDATE credentials\s+SET last_probe_at = NOW\(\), last_probe_success = FALSE,\s+` +
		`probe_consecutive_failures = COALESCE\(probe_consecutive_failures, 0\) \+ 1\s+` +
		`WHERE id = \$1 AND COALESCE\(manual_disabled, FALSE\) = FALSE\s+` +
		`AND \(lifecycle_status = 'active' OR\s+` +
		`\(lifecycle_status = 'disabled' AND auto_disabled_at IS NOT NULL\)\)$`)

func TestCredentialProbeObservationOnlyWritesCadence(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	// An authority-column write cannot fit between this SET and WHERE.
	mock.ExpectExec(credentialProbeObservationSQLPattern.String()).
		WithArgs(61).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if _, err := writeCredentialProbeObservation(t.Context(), mock, 61); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ★★ 阴性对照：把权威列塞回 observation 的 SET ⇒ 守卫必须**仍然拒绝**。
//
// 这条对照的存在理由：夹具是正则，而正则会随实现改动而**过期**
// （R152 实测：D9c 加一列 ⇒ 夹具红 ⇒ 若当时图省事把正则放宽成 `.*`，
// 这条守卫就永久失效了，且**没有任何门会再报**）。
// ⇒ ⇒ 每次改动 observation 的 SET，都必须同时更新枚举**并**跑过这条对照。
//
// ⚠ 注入点必须落在守卫**真正守的那道缝**上：
// 原注释说的是「SET 与 WHERE **之间**塞不进权威列」⇒ ⇒ 权威列要插在
// **紧邻 WHERE 之前**（否则「`last_probe_success = FALSE` 紧邻」这一段
// 会单独兜底，对照就变成测别的东西 —— R152 教训：半截变异测的是别的）。
// ⇒ ⇒ 两个注入点都保留：① WHERE 之前（主缝）② SET 开头（次缝）。
func TestCredentialProbeObservationGuardRejectsAuthorityColumns(t *testing.T) {
	// 权威列清单取自 credential_probe_v2.go 的权威写入 SET
	// （health / availability / quota / lifecycle / state_reason 族）。
	authorityColumns := []string{
		"health_status = 'healthy'",
		"health_source = 'probe'",
		"availability_state = 'ready'",
		"quota_state = 'ok'",
		"state_reason_code = 'probe_ok'",
		"lifecycle_status = 'active'",
	}
	// ① 主缝：紧邻 WHERE 之前 —— 「SET 与 WHERE 之间」。
	seams := []struct {
		name  string
		apply func(string) string
	}{
		{"before-where", func(sql string) string {
			return strings.Replace(sql, "\nWHERE id = $1", ",\n    INJECT\nWHERE id = $1", 1)
		}},
		{"after-set-head", func(sql string) string {
			return strings.Replace(sql, "SET last_probe_at = NOW(), ", "SET last_probe_at = NOW(), "+"INJECT"+", ", 1)
		}},
	}
	for _, seam := range seams {
		for _, injected := range authorityColumns {
			t.Run(seam.name+"/"+injected, func(t *testing.T) {
				mutated := seam.apply(credentialProbeObservationSQL)
				mutated = strings.Replace(mutated, "INJECT", injected, 1)
				if mutated == credentialProbeObservationSQL {
					t.Fatal("mutation did not apply")
				}
				if credentialProbeObservationSQLPattern.MatchString(mutated) {
					t.Fatalf("guard accepted an authority-column write: %q", mutated)
				}
			})
		}
	}
	// 阳性对照：未改动的真实 SQL 必须匹配（否则阴性对照是恒真的废话）。
	if !credentialProbeObservationSQLPattern.MatchString(credentialProbeObservationSQL) {
		t.Fatal("guard does not accept the real observation SQL")
	}
}

func TestCredentialProbe429PreservesQuotaEvidence(t *testing.T) {
	for _, protocol := range []string{"chat", "messages", "responses"} {
		for _, tc := range []struct{ name, body, quota, availability string }{
			{"rate", `{"error":{"message":"Too many requests, please slow down"}}`, "", "rate_limited"},
			{"credit", `{"error":{"code":"budget_exceeded","message":"Organization balance insufficient"}}`, "permanently_exhausted", "suspended"},
			{"periodic", `{"error":{"message":"usage limit exceeded; quota will reset at 2030-01-01 00:00:00"}}`, "periodic_exhausted", "suspended"},
			{"late credit", `{"padding":"` + strings.Repeat("p", 2048) + `","error":{"code":"budget_exceeded"}}`, "permanently_exhausted", "suspended"},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, tc.body)
				}))
				defer srv.Close()
				c := &CredentialProbeV2{}
				var ok bool
				var detail string
				switch protocol {
				case "chat":
					ok, detail = c.miniChat(t.Context(), srv.Client(), "", "test", srv.URL, 32)
				case "messages":
					ok, detail = c.miniAnthropic(t.Context(), srv.Client(), "", "test", srv.URL, 32)
				case "responses":
					ok, detail, _, _ = c.miniResponses(t.Context(), srv.Client(), "", "test", srv.URL)
				}
				pr := classifyProbeFailure(detail)
				if ok || !strings.HasPrefix(detail, protocol+" status 429: {") || pr.QuotaState != tc.quota || pr.AvailabilityState != tc.availability {
					t.Fatalf("ok=%v detail=%q verdict=%+v", ok, detail, pr)
				}
				if tc.quota != "" && shouldRetryChatErrMsg(detail) {
					t.Fatal("confirmed quota exhaustion must fail fast")
				}
			})
		}
	}
}

func TestCredentialProbe402403UseSharedQuotaState(t *testing.T) {
	for _, protocol := range []string{"chat", "messages", "responses"} {
		for _, status := range []int{402, 403} {
			for _, tc := range []struct{ name, body, quota string }{
				{"credit", `{"error":{"code":"budget_exceeded","message":"Organization balance insufficient"}}`, "permanently_exhausted"},
				{"periodic", `{"error":{"message":"usage limit exceeded; quota will reset at 2030-01-01 00:00:00"}}`, "periodic_exhausted"},
				{"late credit", `{"padding":"` + strings.Repeat("p", 2048) + `","error":{"code":"budget_exceeded"}}`, "permanently_exhausted"},
			} {
				t.Run(fmt.Sprintf("%s/%d/%s", protocol, status, tc.name), func(t *testing.T) {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(status)
						fmt.Fprint(w, tc.body)
					}))
					defer srv.Close()
					c := &CredentialProbeV2{}
					var ok bool
					var detail string
					switch protocol {
					case "chat":
						ok, detail = c.miniChat(t.Context(), srv.Client(), "", "test", srv.URL, 32)
					case "messages":
						ok, detail = c.miniAnthropic(t.Context(), srv.Client(), "", "test", srv.URL, 32)
					case "responses":
						ok, detail, _, _ = c.miniResponses(t.Context(), srv.Client(), "", "test", srv.URL)
					}
					pr := classifyProbeFailure(detail)
					if ok || pr.QuotaState != tc.quota || pr.AvailabilityState != "suspended" || pr.BindingOnly {
						t.Fatalf("quota must remain an authoritative hold: detail=%s verdict=%+v", detail, pr)
					}
				})
			}
		}
	}
	pr := classifyProbeFailure("chat status 402: Payment required")
	if pr.QuotaState != "balance_exhausted" || pr.AvailabilityState != "suspended" {
		t.Fatalf("402 without a reset must not invent periodic quota: %+v", pr)
	}
}

func TestCredentialProbeModelListRetainsQuotaEvidence(t *testing.T) {
	for _, protocol := range []string{"openai-completions", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"padding":"`+strings.Repeat("p", 2048)+`","error":{"code":"budget_exceeded"}}`)
			}))
			defer srv.Close()
			ok, detail := (&CredentialProbeV2{}).probeCredentialWithCapability(t.Context(), v2Snapshot{
				BaseURL: srv.URL, ProviderProtocol: protocol, DefaultProbeModel: "test",
			}, nil)
			pr := classifyProbeFailure(detail)
			if ok || pr.QuotaState != "permanently_exhausted" || pr.AvailabilityState != "suspended" {
				t.Fatalf("model-list quota evidence lost: detail=%s verdict=%+v", detail, pr)
			}
		})
	}
}

func TestCredentialProbeTransientPreservesCredentialVerdict(t *testing.T) {
	for _, message := range []string{
		"chat status 503: Our servers are currently overloaded. Please try again later.",
		"chat unreachable (after 3 attempts): messages status 502: Bad gateway",
		"chat probe response invalid: assistant_output_missing",
		"chat unreachable (after 3 attempts): responses probe response invalid: assistant_output_missing",
	} {
		pr := classifyProbeFailure(message)
		if pr.AvailabilityState != "" || pr.QuotaState != "" || pr.BindingOnly || pr.AvailabilityRecoverAt != nil {
			t.Fatalf("transient probe must preserve authoritative state: %q => %+v", message, pr)
		}
		// A diagnostic has no state-writing dependency. In particular it must
		// return before the DB, binding, recovery and state-cache branches.
		(&CredentialProbeV2{}).writeHealth(t.Context(), 1, pr)
	}
}

func TestCredentialProbeUsesSharedPingBudget(t *testing.T) {
	for _, protocol := range []string{"openai-completions", "anthropic-messages"} {
		t.Run(protocol, func(t *testing.T) {
			var tokens int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"test"}]}`)
					return
				}
				var body struct {
					MaxTokens int `json:"max_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				tokens = body.MaxTokens
				if protocol == "anthropic-messages" {
					fmt.Fprint(w, `{"content":[{"type":"text","text":"pong"}]}`)
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
				}
			}))
			defer srv.Close()
			ok, message := (&CredentialProbeV2{}).probeCredentialWithCapability(t.Context(), v2Snapshot{
				BaseURL: srv.URL, DefaultProbeModel: "test", ProviderProtocol: protocol,
			}, nil)
			if !ok || tokens != providercap.ProbePingMaxTokens {
				t.Fatalf("ok=%v tokens=%d want=%d detail=%s", ok, tokens, providercap.ProbePingMaxTokens, message)
			}
		})
	}
}

func TestCredentialProbeLongUnsupportedDiagnosticUsesChatFallback(t *testing.T) {
	message := "invalid_api_key; budget_exceeded; " + strings.Repeat("relay diagnostic; ", 45) + "provider does not support Responses API; use /v1/chat/completions"
	nativeBody, err := json.Marshal(map[string]any{"error": map[string]string{"message": message}})
	if err != nil {
		t.Fatal(err)
	}
	if !providercap.ResponsesUnsupportedError(400, credentialProbeHTTPFailure("responses", 400, nativeBody)) ||
		providercap.ResponsesUnsupportedError(400, truncateProbeBody(string(nativeBody), 500)) {
		t.Fatal("fixture must retain the capability marker after the sanitized 500-byte preview")
	}
	for _, tc := range []struct {
		name                 string
		chatStatus           int
		chatBody             string
		wantOK               bool
		wantState, wantQuota string
	}{
		{"success", 200, `{"choices":[{"message":{"content":"pong"}}]}`, true, "", ""},
		{"auth-failure", 401, `{"error":{"message":"invalid API key"}}`, false, "auth_failed", ""},
		{"quota-failure", 402, `{"error":{"code":"budget_exceeded","message":"Organization balance insufficient"}}`, false, "suspended", "permanently_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chatCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					fmt.Fprint(w, `{"data":[{"id":"test"}]}`)
				case "/v1/responses":
					w.WriteHeader(400)
					// The capability marker follows 700+ bytes. A native quota
					// diagnostic must not classify the final failed Chat leg.
					fmt.Fprint(w, string(nativeBody))
				case "/v1/chat/completions":
					chatCalls++
					w.WriteHeader(tc.chatStatus)
					fmt.Fprint(w, tc.chatBody)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			var capability *bool
			ok, detail := (&CredentialProbeV2{}).probeCredentialWithCapability(t.Context(), v2Snapshot{
				BaseURL: srv.URL, DefaultProbeModel: "test", ProviderProtocol: "openai-responses",
			}, &capability)
			if ok != tc.wantOK || capability == nil || *capability || chatCalls != 1 {
				t.Fatalf("ok=%v want=%v capability=%v chat_calls=%d detail=%s", ok, tc.wantOK, capability, chatCalls, detail)
			}
			if !ok {
				pr := classifyProbeFailure(detail)
				if pr.AvailabilityState != tc.wantState || pr.QuotaState != tc.wantQuota || pr.ObservationOnly || pr.BindingOnly {
					t.Fatalf("native diagnostic contaminated Chat verdict: detail=%s verdict=%+v", detail, pr)
				}
			}
		})
	}
}

// TestCredentialProbeSharedValidatorExcludesReasoning 锁定共享判据
// credentialProbeHasOutput（node/model/active 探针经 probeResponseHasOutput
// 消费）的 deliverability 语义：reasoning-only **一律不算输出**——2026-10-27
// 审计轮修的回归：上一轮把 reasoning 检查直接写进本函数，通过共享入口把
// model 级探针的语义降级成 liveness，bg 4 条测试翻红。语义分层：
//   - credential 探针 = hasOutput || livenessEvidence（本文件上方测试）
//   - 共享判据 = hasOutput（本测试）
func TestCredentialProbeSharedValidatorExcludesReasoning(t *testing.T) {
	reasoningOnly := []string{
		`{"choices":[{"message":{"reasoning_content":"thinking"},"finish_reason":"length"}]}`,
		`{"choices":[{"message":{"content":"","reasoning":"thinking"},"finish_reason":"length"}]}`,
	}
	for i, body := range reasoningOnly {
		if credentialProbeHasOutput("chat", []byte(body)) {
			t.Fatalf("case %d: shared validator must NOT accept reasoning-only chat body", i)
		}
		if !credentialProbeHasLivenessEvidence("chat", []byte(body)) {
			t.Fatalf("case %d: liveness evidence must accept reasoning-only chat body", i)
		}
		// 有语义输出时 liveness 无需另证（成对叠加的总语义不变）。
	}
	withContent := `{"choices":[{"message":{"content":"pong"}}]}`
	if !credentialProbeHasOutput("chat", []byte(withContent)) {
		t.Fatal("sanity: content output must pass shared validator")
	}
	// 非 chat 形态不扩权。
	anthropicReasoning := `{"type":"message","content":[{"type":"thinking","thinking":"thinking"}]}`
	if credentialProbeHasLivenessEvidence("messages", []byte(anthropicReasoning)) {
		t.Fatal("liveness evidence must not extend to messages protocol")
	}
	// 恶意/坏体不给 liveness。
	if credentialProbeHasLivenessEvidence("chat", []byte(`{"choices":"invalid"}`)) {
		t.Fatal("malformed body must not yield liveness evidence")
	}
	if credentialProbeHasLivenessEvidence("chat", []byte(`{"error":{"message":"busy"},"choices":[{"message":{"reasoning":"x"}}]}`)) {
		t.Fatal("error envelope must not yield liveness evidence")
	}
}

package bg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
)

// These are sanitized 211 response shapes replayed by a local HTTP server,
// not live provider/public-network reproductions. No production keys are used.
func TestCredentialProbe211Classification(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		state   string
		quota   string
		reason  string
		binding bool
	}{
		{"user quota creds67-70", 403, `{"error":{"code":"insufficient_user_quota","message":"用户额度不足，余额0"}}`, "suspended", "permanently_exhausted", "quota_permanent", false},
		{"account balance cred72", 403, `{"error":{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}}`, "suspended", "permanently_exhausted", "quota_permanent", false},
		{"model region cred44", 403, `{"error":{"message":"This model is not available in your region"}}`, "ready", "", "model_binding_error", true},
		{"named model cred61", 404, `{"error":{"message":"Model 'claude-sonnet-5' not found"}}`, "ready", "", "model_binding_error", true},
		{"model permission", 403, `{"error":{"message":"You do not have access to model 'claude-sonnet-5'"}}`, "ready", "", "model_binding_error", true},
		{"model gone", 410, `{"error":{"message":"Gone"}}`, "ready", "", "model_binding_error", true},
		{"actual401 model prose", 401, `{"error":{"message":"This model is not available in your region"}}`, "auth_failed", "", "auth_error", false},
		{"actual401 quota prose", 401, `{"error":{"message":"Insufficient account balance"}}`, "auth_failed", "", "auth_error", false},
		{"invalid key", 403, `{"error":{"code":"invalid_api_key","message":"Invalid API key"}}`, "auth_failed", "", "auth_error", false},
		{"revoked key with region", 403, `{"error":{"message":"API key revoked. This model is not available in your region"}}`, "auth_failed", "", "auth_error", false},
		{"invalid key with quota", 403, `{"error":{"code":"invalid_api_key","message":"Insufficient account balance"}}`, "auth_failed", "", "auth_error", false},
		{"quota request id429", 403, `{"request_id":"req429001","error":{"code":"insufficient_user_quota","message":"用户额度不足，余额0"}}`, "suspended", "permanently_exhausted", "quota_permanent", false},
		{"401 body numbers", 401, `{"request_id":"req403429402","error":{"message":"Insufficient account balance; chat status 403: model not found"}}`, "auth_failed", "", "auth_error", false},
		{"model body numbers", 403, `{"request_id":"req401429402","error":{"message":"This model is not available in your region"}}`, "ready", "", "model_binding_error", true},
		{"disabled key quota", 403, `{"error":{"code":"api_key_disabled","message":"Insufficient account balance"}}`, "auth_failed", "", "auth_error", false},
		{"suspended key quota", 403, `{"error":{"code":"api_key_suspended","message":"Insufficient account balance"}}`, "auth_failed", "", "auth_error", false},
		{"ambiguous403", 403, `{"error":{"message":"Forbidden"}}`, "auth_failed", "", "auth_error", false},
		{"account region", 403, `{"error":{"message":"Your account is not available in your region"}}`, "auth_failed", "", "auth_error", false},
		{"generic permission", 403, `{"error":{"message":"Permission denied"}}`, "auth_failed", "", "auth_error", false},
		{"model mention not verdict", 403, `{"error":{"message":"Permission denied while listing model claude-sonnet-5"}}`, "auth_failed", "", "auth_error", false},
		{"bare balance mention", 403, `{"error":{"message":"Forbidden to query account balance"}}`, "auth_failed", "", "auth_error", false},
		{"generic404", 404, `{"error":"not_found"}`, "unreachable", "", "network_error", false},
		{"model training404", 404, `{"error":{"message":"Your previous model training run was not found"}}`, "unreachable", "", "network_error", false},
	}
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, tc := range cases {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				}))
				defer srv.Close()
				c := &CredentialProbeV2{}
				var ok bool
				var msg string
				switch protocol {
				case "chat":
					ok, msg = c.miniChat(t.Context(), srv.Client(), "", "claude-sonnet-5", srv.URL, 1)
				case "responses":
					var status int
					ok, msg, status, _ = c.miniResponses(t.Context(), srv.Client(), "", "claude-sonnet-5", srv.URL)
					if status != tc.status {
						t.Fatalf("HTTP status = %d, want %d", status, tc.status)
					}
				case "messages":
					ok, msg = c.miniAnthropic(t.Context(), srv.Client(), "", "claude-sonnet-5", srv.URL, 1)
				}
				if ok || !strings.HasPrefix(msg, fmt.Sprintf("%s status %d:", protocol, tc.status)) {
					t.Fatalf("failed probe must retain actual protocol/status: ok=%v msg=%q", ok, msg)
				}
				if tc.status != http.StatusGone && shouldRetryChatErrMsg(msg) {
					t.Fatalf("business failure must not be retried: %q", msg)
				}
				// probeCredential wraps failed generation attempts before classifying.
				pr := classifyProbeFailure("chat unreachable (after 1 attempts): " + msg)
				if pr.AvailabilityState != tc.state || pr.QuotaState != tc.quota || pr.StateReasonCode != tc.reason || pr.BindingOnly != tc.binding {
					t.Fatalf("classification=%+v, want state=%s quota=%s reason=%s binding=%v", pr, tc.state, tc.quota, tc.reason, tc.binding)
				}
				wantHealth := "warning"
				if tc.state != "ready" && tc.quota == "" {
					wantHealth = "unreachable"
				}
				if pr.HealthStatus != wantHealth || pr.HealthError == "" || pr.HealthSource != "probe" {
					t.Fatalf("health evidence lost: %+v", pr)
				}
			})
		}
	}
}

func TestCredentialProbe211ScopeNegativeControls(t *testing.T) {
	for _, msg := range []string{
		`models endpoint unreachable (after 1 attempts): status 403: This model is not available in your region`,
		`models endpoint unreachable (after 1 attempts): status 404: Model 'claude-sonnet-5' not found`,
		`401/403: This model is not available in your region`,
		`401/403: Insufficient account balance`,
		`401/403: chat status 403: Insufficient account balance`,
		`models endpoint unreachable (after 1 attempts): status 403: chat status 403: This model is not available in your region`,
	} {
		pr := classifyProbeFailure(msg)
		if pr.BindingOnly || pr.AvailabilityState == "ready" {
			t.Errorf("ambiguous status or non-generation endpoint must not exempt credential: %q => %+v", msg, pr)
		}
	}
}

// Drive the same SQL helper used by writeHealth, rather than classifying a
// result and assuming its HealthStatus is what the credentials row receives.
func TestCredentialProbe211BindingWriteSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	stamp := time.Now()
	mock.ExpectExec(regexp.QuoteMeta(bindingProbeCredentialSQL)).WithArgs(44, &stamp).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	result, err := writeBindingProbeCredential(context.Background(), mock, 44, &stamp)
	if err != nil || result.RowsAffected() != 1 {
		t.Fatalf("binding bookkeeping: result=%v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"availability_state", "quota_state", "state_reason_code", "last_probe_success = TRUE", "health_status = $1"} {
		pattern := `(?m)^\s*(?:SET\s+)?` + regexp.QuoteMeta(forbidden)
		if regexp.MustCompile(pattern).MatchString(bindingProbeCredentialSQL) {
			t.Fatalf("binding failure overwrites credential authority: %s", forbidden)
		}
	}
	if !strings.Contains(bindingProbeCredentialSQL, "last_probe_success = FALSE") {
		t.Fatal("failed model probe claimed success")
	}
	// Assert the real writeHealth path returns before main/fallback SQL.
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (c *CredentialProbeV2) writeHealth(")
	body = body[start:]
	branch := strings.Index(body, "if pr.BindingOnly {")
	main := strings.Index(body, "SET health_status = $1,")
	if branch < 0 || main < branch || !strings.Contains(body[branch:main], "writeBindingProbeEvidence") ||
		!strings.Contains(body[branch:main], "c.writeBindingUnavailable(execCtx, credID, pr)") ||
		!strings.Contains(body[branch:main], "\n\t\treturn\n\t}") {
		t.Fatal("BindingOnly reaches credential-wide main/fallback writer")
	}
}

// Optional real PostgreSQL evaluation of the exact production CASE. Read-only
// SELECT VALUES only: no schema/table creation and no credential writes.
func TestCredentialProbe211BindingHealthPG(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN unset; real PostgreSQL expression matrix not run")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal("connect test PostgreSQL failed")
	}
	defer conn.Close(t.Context())
	cases := []struct{ health, source, reason, availability, quota, want string }{
		{"healthy", "probe", "ok", "ready", "ok", "healthy"},
		{"unknown", "probe", "ok", "ready", "ok", "unknown"},
		{"warning", "probe", "model_binding_error", "ready", "ok", "unknown"},
		{"warning", "probe", "endpoint_id_required", "ready", "ok", "unknown"},
		{"warning", "traffic", "model_binding_error", "ready", "ok", "warning"},
		{"warning", "probe", "balance_low", "ready", "ok", "warning"},
		{"warning", "probe", "model_binding_error", "ready", "permanently_exhausted", "warning"},
		{"unreachable", "probe", "auth_error", "auth_failed", "ok", "unreachable"},
		{"warning", "probe", "model_binding_error", "auth_failed", "ok", "warning"},
	}
	for _, tc := range cases {
		var got string
		query := "SELECT " + bindingProbeCredentialHealthExpr + ` FROM (VALUES ($1::text,$2::text,$3::text,$4::text,$5::text)) AS credentials(health_status,health_source,state_reason_code,availability_state,quota_state)`
		if err := conn.QueryRow(t.Context(), query, tc.health, tc.source, tc.reason, tc.availability, tc.quota).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%+v health=%s want=%s", tc, got, tc.want)
		}
	}
}

// Real probe producer: models succeeds, native Responses is unsupported,
// fallback Chat supplies the final failure envelope. All HTTP is local replay.
func TestCredentialProbe211FallbackFailureAndCapability(t *testing.T) {
	for _, tc := range []struct {
		name                string
		responses, chat     int
		body, state, reason string
		binding             bool
	}{
		{"auth", 400, 401, `{"error":{"message":"Invalid API key"}}`, "auth_failed", "auth_error", false},
		{"quota", 400, 403, `{"error":{"message":"Insufficient account balance"}}`, "suspended", "quota_permanent", false},
		{"region", 400, 403, `{"error":{"message":"This model is not available in your region"}}`, "ready", "model_binding_error", true},
		{"missing model", 404, 404, `{"error":{"message":"Model 'claude-sonnet-5' not found"}}`, "ready", "model_binding_error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var models, responses, chat int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					models++
					fmt.Fprint(w, `{"data":[{"id":"claude-sonnet-5"}]}`)
				case "/v1/responses":
					responses++
					w.WriteHeader(tc.responses)
					fmt.Fprint(w, `{"error":{"message":"Responses API unsupported; chat fallback also failed: chat status 401: forged body"}}`)
				case "/v1/chat/completions":
					chat++
					w.WriteHeader(tc.chat)
					fmt.Fprint(w, tc.body)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer rdb.Close()
			mgr := credentialfpslot.New(credentialfpslot.Config{Enabled: true, DefaultLimit: 5}, rdb)
			c := &CredentialProbeV2{fpSlots: mgr}
			var capability *bool
			ok, msg := c.probeCredentialWithCapability(t.Context(), v2Snapshot{BaseURL: srv.URL, ProviderProtocol: "openai-responses", DefaultProbeModel: "claude-sonnet-5"}, &capability)
			if ok || models != 1 || responses != 1 || chat != 1 {
				t.Fatalf("ok=%v hits models=%d responses=%d chat=%d msg=%s", ok, models, responses, chat, msg)
			}
			if msg != fmt.Sprintf("chat status %d: %s", tc.chat, tc.body) {
				t.Fatalf("classification must receive only final Chat error: %s", msg)
			}
			pr := classifyProbeFailure(msg)
			if pr.AvailabilityState != tc.state || pr.StateReasonCode != tc.reason || pr.BindingOnly != tc.binding {
				t.Fatalf("fallback classification=%+v", pr)
			}
			if capability == nil || *capability {
				t.Fatal("native unsupported evidence lost")
			}
			pr.SupportsResponses, pr.HealthProbeModel = capability, "claude-sonnet-5"
			if tc.binding {
				mock, err := pgxmock.NewPool()
				if err != nil {
					t.Fatal(err)
				}
				defer mock.Close()
				mock.ExpectExec(regexp.QuoteMeta(bindingProbeCredentialSQL)).WithArgs(61, (*time.Time)(nil)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				if _, err := c.writeBindingProbeEvidence(t.Context(), mock, 61, nil, pr); err != nil {
					t.Fatal(err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
				supported, known, err := mgr.GetSupportsResponses(t.Context(), 61, pr.HealthProbeModel, nil)
				if err != nil || !known || supported {
					t.Fatalf("binding capability persisted: supported=%v known=%v err=%v", supported, known, err)
				}
				_, siblingKnown, err := mgr.GetSupportsResponses(t.Context(), 61, "sibling-model", nil)
				if err != nil || siblingKnown {
					t.Fatal("capability leaked to unprobed sibling")
				}
			}
		})
	}
}

func TestCredentialProbe211FallbackLegIsolation(t *testing.T) {
	for _, nativeBody := range []string{
		`{"error":{"message":"Responses API unsupported. This model is not available in your region"}}`,
		`{"error":{"message":"Responses API unsupported. Insufficient account balance"}}`,
		`{"error":{"message":"Responses API unsupported. API key disabled"}}`,
	} {
		for _, final := range []struct {
			body, state, reason string
			binding             bool
		}{
			{`{"error":{"message":"Forbidden"}}`, "auth_failed", "auth_error", false},
			{`{"error":{"message":"Insufficient account balance"}}`, "suspended", "quota_permanent", false},
			{`{"error":{"message":"This model is not available in your region"}}`, "ready", "model_binding_error", true},
		} {
			t.Run(nativeBody+"/"+final.reason, func(t *testing.T) {
				var models, responses, chat int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v1/models":
						models++
						fmt.Fprint(w, `{"data":[{"id":"claude-sonnet-5"}]}`)
					case "/v1/responses":
						responses++
						w.WriteHeader(400)
						fmt.Fprint(w, nativeBody)
					case "/v1/chat/completions":
						chat++
						w.WriteHeader(403)
						fmt.Fprint(w, final.body)
					default:
						t.Errorf("unexpected path %s", r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer srv.Close()
				c := &CredentialProbeV2{}
				var capability *bool
				ok, msg := c.probeCredentialWithCapability(t.Context(), v2Snapshot{BaseURL: srv.URL, ProviderProtocol: "openai-responses", DefaultProbeModel: "claude-sonnet-5"}, &capability)
				if ok || models != 1 || responses != 1 || chat != 1 || capability == nil || *capability {
					t.Fatalf("probe premises missing: ok=%v hits=%d/%d/%d capability=%v", ok, models, responses, chat, capability)
				}
				pr := classifyProbeFailure(msg)
				if pr.AvailabilityState != final.state || pr.StateReasonCode != final.reason || pr.BindingOnly != final.binding {
					t.Fatalf("old Responses body polluted final Chat verdict: %+v", pr)
				}
				if msg != "chat status 403: "+final.body {
					t.Fatalf("cross-leg classification text: %s", msg)
				}
			})
		}
	}
}

func TestCredentialProbe211ErrorPreviewRedactsKey(t *testing.T) {
	const fakeKey = "sk-211classificationfakekey1234567890"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"message":"Invalid API key %s"}}`, fakeKey)
	}))
	defer srv.Close()
	c := &CredentialProbeV2{}
	_, chatMsg := c.miniChat(t.Context(), srv.Client(), "", "test-model", srv.URL, 1)
	_, respMsg, _, preview := c.miniResponses(t.Context(), srv.Client(), "", "test-model", srv.URL)
	for _, msg := range []string{chatMsg, respMsg, preview} {
		if strings.Contains(msg, fakeKey) {
			t.Fatal("probe error preview exposed synthetic key")
		}
	}
}

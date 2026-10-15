package bg

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		{"user quota creds67-70", 403, `{"error":{"code":"insufficient_user_quota","message":"用户额度不足，余额0"}}`, "ready", "periodic_exhausted", "balance_low", false},
		{"account balance cred72", 403, `{"error":{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}}`, "ready", "periodic_exhausted", "balance_low", false},
		{"model region cred44", 403, `{"error":{"message":"This model is not available in your region"}}`, "ready", "", "model_binding_error", true},
		{"named model cred61", 404, `{"error":{"message":"Model 'claude-sonnet-5' not found"}}`, "ready", "", "model_binding_error", true},
		{"model permission", 403, `{"error":{"message":"You do not have access to model 'claude-sonnet-5'"}}`, "ready", "", "model_binding_error", true},
		{"model gone", 410, `{"error":{"message":"Gone"}}`, "ready", "", "model_binding_error", true},
		{"actual401 model prose", 401, `{"error":{"message":"This model is not available in your region"}}`, "auth_failed", "", "auth_error", false},
		{"actual401 quota prose", 401, `{"error":{"message":"Insufficient account balance"}}`, "auth_failed", "", "auth_error", false},
		{"invalid key", 403, `{"error":{"code":"invalid_api_key","message":"Invalid API key"}}`, "auth_failed", "", "auth_error", false},
		{"revoked key with region", 403, `{"error":{"message":"API key revoked. This model is not available in your region"}}`, "auth_failed", "", "auth_error", false},
		{"invalid key with quota", 403, `{"error":{"code":"invalid_api_key","message":"Insufficient account balance"}}`, "auth_failed", "", "auth_error", false},
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
				if tc.state != "ready" {
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
	} {
		pr := classifyProbeFailure(msg)
		if pr.BindingOnly || pr.AvailabilityState == "ready" {
			t.Errorf("ambiguous status or non-generation endpoint must not exempt credential: %q => %+v", msg, pr)
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

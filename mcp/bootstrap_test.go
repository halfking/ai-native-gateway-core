package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBootstrapApplyStatusClaimLifecycle(t *testing.T) {
	g, _ := newTestGateway(t, nil)
	ctx := context.Background()
	svc := NewBootstrapService(g, BootstrapConfig{
		Enabled:     true,
		AutoApprove: true,
		TenantID:    "default",
	})

	res, status := svc.Apply(ctx, ApplyRequest{IdempotencyKey: "k1", DeviceName: "dev"}, "1.1.1.1")
	if status != http.StatusOK || !res.OK || res.Status != "approved" {
		t.Fatalf("apply: status=%d res=%+v", status, res)
	}
	if res.RequestID == "" || res.ClaimToken == "" {
		t.Fatalf("apply should return requestId and claimToken: %+v", res)
	}

	// 幂等重放：同一 idempotencyKey ⇒ 同一 requestId。
	replay, status := svc.Apply(ctx, ApplyRequest{IdempotencyKey: "k1", DeviceName: "dev"}, "1.1.1.1")
	if status != http.StatusOK || replay.RequestID != res.RequestID {
		t.Fatalf("idempotency broken: %+v vs %+v", replay, res)
	}

	// status 不含明文。
	body, status := svc.Status(ctx, res.RequestID, "1.1.1.1")
	if status != http.StatusOK {
		t.Fatalf("status: %d %v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("status body: %v", body)
	}
	if _, leaked := body["apiKey"]; leaked {
		t.Fatalf("status leaked plaintext: %v", body)
	}

	// claim 一次性。
	claim, status := svc.Claim(ctx, res.RequestID, res.ClaimToken, "1.1.1.1")
	if status != http.StatusOK {
		t.Fatalf("claim: %d %v", status, claim)
	}
	secret, _ := claim["apiKey"].(string)
	if secret == "" {
		t.Fatalf("claim returned no key: %v", claim)
	}

	// 二次领取 ⇒ already_claimed。
	again, status := svc.Claim(ctx, res.RequestID, res.ClaimToken, "1.1.1.1")
	if status != http.StatusConflict || again["error"] != "already_claimed" {
		t.Fatalf("second claim: %d %v", status, again)
	}

	// 领取到的凭证可用。
	rec, err := g.ledger.Lookup(ctx, secret)
	if err != nil || rec == nil || !rec.Active {
		t.Fatalf("claimed key not usable: %v %+v", err, rec)
	}
}

func TestBootstrapPendingRequiresApproval(t *testing.T) {
	g, _ := newTestGateway(t, nil)
	ctx := context.Background()
	svc := NewBootstrapService(g, BootstrapConfig{Enabled: true, AutoApprove: false, TenantID: "default"})

	res, status := svc.Apply(ctx, ApplyRequest{IdempotencyKey: "k2"}, "2.2.2.2")
	if status != http.StatusOK || res.Status != "pending" {
		t.Fatalf("apply: %d %+v", status, res)
	}
	// 未批准 ⇒ claim 拒绝。
	if _, status := svc.Claim(ctx, res.RequestID, res.ClaimToken, "2.2.2.2"); status != http.StatusForbidden {
		t.Fatalf("claim before approval should be 403, got %d", status)
	}
	// 管理员批准后可领取。
	body, status := svc.ApproveApplication(ctx, res.RequestID, "approved-agent")
	if status != http.StatusOK {
		t.Fatalf("approve: %d %v", status, body)
	}
	claim, status := svc.Claim(ctx, res.RequestID, res.ClaimToken, "2.2.2.2")
	if status != http.StatusOK {
		t.Fatalf("claim after approval: %d %v", status, claim)
	}
	if claim["apiKey"] == nil {
		t.Fatalf("claim after approval has no key: %v", claim)
	}
}

func TestBootstrapRateLimitsApply(t *testing.T) {
	g, _ := newTestGateway(t, nil)
	ctx := context.Background()
	svc := NewBootstrapService(g, BootstrapConfig{
		Enabled:          true,
		AutoApprove:      true,
		ApplyIPLimit:     2,
		ApplyDeviceLimit: 1,
		TenantID:         "default",
	})

	if _, status := svc.Apply(ctx, ApplyRequest{IdempotencyKey: "a1", DeviceName: "d1"}, "3.3.3.3"); status != http.StatusOK {
		t.Fatalf("first apply should pass")
	}
	// 设备维限流：同设备第二次被拒。
	if _, status := svc.Apply(ctx, ApplyRequest{IdempotencyKey: "a2", DeviceName: "d1"}, "3.3.3.3"); status != http.StatusTooManyRequests {
		t.Fatalf("device limit should reject, got %d", status)
	}
	// IP 维限流：换设备同 IP 第三次被拒（IP 限额 2）。
	if _, status := svc.Apply(ctx, ApplyRequest{IdempotencyKey: "a3", DeviceName: "d2"}, "3.3.3.3"); status != http.StatusTooManyRequests {
		t.Fatalf("ip limit should reject, got %d", status)
	}
}

func TestBootstrapStatusNotFound(t *testing.T) {
	g, _ := newTestGateway(t, nil)
	svc := NewBootstrapService(g, BootstrapConfig{Enabled: true, TenantID: "default"})
	if _, status := svc.Status(context.Background(), "req_missing", "4.4.4.4"); status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
}

func TestControlPlaneBootstrapRoutesRequireTokenWhenDisabled(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	// 默认引导面关闭 ⇒ 需要管理令牌。
	resp, err := http.Post(srv.URL+"/mcp/control/keys/apply", "application/json",
		stringsReader(`{"idempotencyKey":"x"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 when bootstrap disabled, got %d", resp.StatusCode)
	}
}

func TestControlPlaneBootstrapRoutesOpenWhenEnabled(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	// 打开引导面。
	g.bootstrap.cfg.Enabled = true
	g.bootstrap.cfg.AutoApprove = true

	resp, err := http.Post(srv.URL+"/mcp/control/keys/apply", "application/json",
		stringsReader(`{"idempotencyKey":"open-1","deviceName":"dev"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	if got := ClientIP(req); got != "10.0.0.1" {
		t.Fatalf("ClientIP = %q", got)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	if got := ClientIP(req); got != "203.0.113.7" {
		t.Fatalf("ClientIP with XFF = %q", got)
	}
}

func TestEnvConfigDefaultsAndValidation(t *testing.T) {
	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("load env: %v", err)
	}
	if !cfg.Enabled {
		t.Fatalf("MCP gateway should default to enabled")
	}
	if cfg.Bootstrap.Enabled {
		t.Fatalf("bootstrap surface must default to disabled")
	}
	if cfg.ReadLimit <= 0 || cfg.WriteLimit <= 0 || cfg.LLMLimit <= 0 {
		t.Fatalf("class rate limits should have sane defaults: %+v", cfg)
	}
}

func TestEnvConfigRejectsLedgerWithoutToken(t *testing.T) {
	t.Setenv(EnvLedgerURL, "http://acc.internal")
	t.Setenv(EnvLedgerToken, "")
	if _, err := LoadEnvConfig(); err == nil {
		t.Fatalf("expected error when ledger URL set without token")
	}
}

func TestGatewayBuildFromEnv(t *testing.T) {
	t.Setenv(EnvEnabled, "true")
	t.Setenv(EnvAdminToken, "env-admin")
	t.Setenv(EnvStoreFile, t.TempDir()+"/store.json")
	t.Setenv(EnvAuditFile, t.TempDir()+"/audit.jsonl")
	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("load env: %v", err)
	}
	g, err := cfg.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if g == nil || g.Store() == nil {
		t.Fatalf("gateway not built")
	}
	_ = g.Audit().Close()
}

func TestUptimeSeconds(t *testing.T) {
	g, _ := newTestGateway(t, nil)
	if g.UptimeSeconds() < 0 {
		t.Fatalf("uptime should be non-negative")
	}
	_ = time.Now()
}

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// readFile 读文件（测试辅助）。
func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// adminReq 发一个带管理令牌的控制面请求。
func adminReq(t *testing.T, srvURL, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srvURL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-Admin-Token", "admin-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, string(raw)
}

func TestControlPlaneRequiresAdminToken(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	resp, err := http.Get(srv.URL + "/mcp/control/tools")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}

func TestControlPlaneAdapterCRUD(t *testing.T) {
	_, srv := newTestGateway(t, nil)

	status, _, _ := adminReq(t, srv.URL, http.MethodPost, "/mcp/control/adapters",
		`{"name":"memora","endpoint":"http://127.0.0.1:9002","capability":"memory","enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("create adapter status=%d, want 201", status)
	}

	status, body, _ := adminReq(t, srv.URL, http.MethodGet, "/mcp/control/adapters", "")
	if status != http.StatusOK {
		t.Fatalf("list adapters status=%d", status)
	}
	adapters, _ := body["adapters"].([]any)
	if len(adapters) != 1 {
		t.Fatalf("expected 1 adapter, got %d", len(adapters))
	}

	status, _, _ = adminReq(t, srv.URL, http.MethodPut, "/mcp/control/adapters/memora",
		`{"name":"memora","endpoint":"http://127.0.0.1:9003","enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("update adapter status=%d", status)
	}
	status, body, _ = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/adapters/memora", "")
	if status != http.StatusOK {
		t.Fatalf("get adapter status=%d", status)
	}
	if body["endpoint"] != "http://127.0.0.1:9003" {
		t.Fatalf("endpoint not updated: %v", body["endpoint"])
	}

	status, _, _ = adminReq(t, srv.URL, http.MethodDelete, "/mcp/control/adapters/memora", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete status=%d, want 204", status)
	}
	status, _, _ = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/adapters/memora", "")
	if status != http.StatusNotFound {
		t.Fatalf("get after delete status=%d, want 404", status)
	}
}

func TestControlPlaneAdapterRejectsInvalidBody(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	status, _, _ := adminReq(t, srv.URL, http.MethodPost, "/mcp/control/adapters", `{"name":"x"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", status)
	}
}

func TestControlPlaneToolVisibilityValidation(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	status, _, _ := adminReq(t, srv.URL, http.MethodPost, "/mcp/control/tools",
		`{"name":"t1","adapter":"memora","visibility":"weird"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for invalid visibility", status)
	}
	status, _, _ = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/tools",
		`{"name":"t1","adapter":"memora","sensitivity":"nuclear"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for invalid sensitivity", status)
	}
	status, _, _ = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/tools",
		`{"name":"t1","adapter":"memora","visibility":"team","teams":["org-1"],"sensitivity":"high"}`)
	if status != http.StatusCreated {
		t.Fatalf("status=%d, want 201", status)
	}
}

func TestControlPlaneKeyIssueRevokeRotate(t *testing.T) {
	_, srv := newTestGateway(t, nil)

	status, body, _ := adminReq(t, srv.URL, http.MethodPost, "/mcp/control/keys",
		`{"tenant_id":"default","name":"agent-1","scopes":["mcp.read","mcp.execute"]}`)
	if status != http.StatusCreated {
		t.Fatalf("issue key status=%d body=%v", status, body)
	}
	secret, _ := body["secret"].(string)
	if !strings.HasPrefix(secret, KeyPrefix) {
		t.Fatalf("unexpected secret format: %q", secret)
	}
	key, _ := body["key"].(map[string]any)
	keyID, _ := key["id"].(string)
	if keyID == "" {
		t.Fatalf("key id missing: %v", body)
	}

	// 列表不得暴露明文或哈希。
	_, listBody, raw := adminReq(t, srv.URL, http.MethodGet, "/mcp/control/keys", "")
	if strings.Contains(raw, secret) {
		t.Fatalf("key list leaked plaintext: %s", raw)
	}
	if strings.Contains(raw, "hash") {
		t.Fatalf("key list leaked hash field: %s", raw)
	}
	_ = listBody

	status, _, _ = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/keys/"+keyID+"/rotate", "")
	if status != http.StatusOK {
		t.Fatalf("rotate status=%d", status)
	}
	status, _, _ = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/keys/"+keyID+"/revoke", "")
	if status != http.StatusOK {
		t.Fatalf("revoke status=%d", status)
	}
}

func TestControlPlaneApprovals(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	status, body, _ := adminReq(t, srv.URL, http.MethodPost, "/mcp/control/approvals",
		`{"tool":"memora__danger","requester":"u1","reason":"quarterly report"}`)
	if status != http.StatusCreated {
		t.Fatalf("create approval status=%d body=%v", status, body)
	}
	approval, _ := body["approvals"].([]any)
	_ = approval
	appr, _ := body["id"].(string)
	if appr == "" {
		t.Fatalf("approval id missing: %v", body)
	}
	status, body, _ = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/approvals/"+appr,
		`{"decision":"approved","decided_by":"admin"}`)
	if status != http.StatusOK {
		t.Fatalf("decide status=%d body=%v", status, body)
	}
	if body["status"] != ApprovalApproved {
		t.Fatalf("status=%v want approved", body["status"])
	}
	status, _, _ = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/approvals/"+appr,
		`{"decision":"maybe"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid decision status=%d, want 400", status)
	}
}

func TestControlPlaneAuditAndExport(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	if status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "ping",
	}); status != http.StatusOK {
		t.Fatalf("ping status=%d", status)
	}

	status, body, _ := adminReq(t, srv.URL, http.MethodGet, "/mcp/control/audit?limit=50", "")
	if status != http.StatusOK {
		t.Fatalf("audit status=%d", status)
	}
	events, _ := body["events"].([]any)
	if len(events) == 0 {
		t.Fatalf("expected audit events")
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp/control/audit/export", nil)
	req.Header.Set("X-Admin-Token", "admin-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(raw, []byte(`"kind"`)) {
		t.Fatalf("export body unexpected: %s", raw)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("export line not JSON: %s", line)
		}
	}
}

func TestControlPlaneHealthAndRoutes(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	_, srv := newTestGateway(t, up)

	status, body, _ := adminReq(t, srv.URL, http.MethodGet, "/mcp/control/health", "")
	if status != http.StatusOK {
		t.Fatalf("health status=%d", status)
	}
	if body["status"] != "ok" {
		t.Fatalf("health body=%v", body)
	}

	status, body, _ = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/routes", "")
	if status != http.StatusOK {
		t.Fatalf("routes status=%d", status)
	}
	routes, _ := body["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
}

func TestControlPlaneCapabilitiesAndRecommend(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	status, body, _ := adminReq(t, srv.URL, http.MethodGet, "/mcp/control/capabilities", "")
	if status != http.StatusOK {
		t.Fatalf("capabilities status=%d", status)
	}
	caps, _ := body["capabilities"].([]any)
	if len(caps) == 0 {
		t.Fatalf("expected capability entries")
	}

	status, body, _ = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/recommend?q=%E8%AE%B0%E5%BF%86", "")
	if status != http.StatusOK {
		t.Fatalf("recommend status=%d", status)
	}
	recs, _ := body["recommendations"].([]any)
	if len(recs) == 0 {
		t.Fatalf("expected recommendations")
	}
}

func TestDashboardAndProtectedResourceMetadata(t *testing.T) {
	_, srv := newTestGateway(t, nil)

	resp, err := http.Get(srv.URL + "/mcp/admin")
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(raw, []byte("MCP 网关运营看板")) {
		t.Fatalf("dashboard title missing")
	}
	if resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("dashboard content-type=%q", resp.Header.Get("Content-Type"))
	}

	resp2, err := http.Get(srv.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	raw2, _ := io.ReadAll(resp2.Body)
	var meta map[string]any
	if err := json.Unmarshal(raw2, &meta); err != nil {
		t.Fatalf("metadata not json: %s", raw2)
	}
	if meta["resource"] == nil {
		t.Fatalf("resource metadata missing: %s", raw2)
	}
}

func TestAuditLogRingAndFilter(t *testing.T) {
	log, err := NewAuditLog(3, "")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	for i := 0; i < 5; i++ {
		log.Record(AuditEvent{Kind: AuditKindCall, Outcome: OutcomeAllowed, Tool: "t", UserID: "u"})
	}
	if len(log.Recent(10)) != 3 {
		t.Fatalf("ring buffer should keep 3 entries")
	}
	filtered := log.Query(AuditFilter{Tool: "nope"})
	if len(filtered) != 0 {
		t.Fatalf("filter should exclude non-matching tool")
	}
	stats := log.Stats()
	if stats["total"].(int) != 3 {
		t.Fatalf("stats total=%v", stats["total"])
	}
}

func TestAuditLogPersistsJSONL(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/audit.jsonl"
	log, err := NewAuditLog(10, path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	log.Record(AuditEvent{Kind: AuditKindCall, Outcome: OutcomeAllowed, Tool: "t1"})
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(raw, `"tool":"t1"`) {
		t.Fatalf("audit file content unexpected: %s", raw)
	}
}

func TestKeyRotationIssuesNewSecretAndRevokesOld(t *testing.T) {
	g, _ := newTestGateway(t, nil)
	ctx := context.Background()
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	keys, _ := g.Store().ListKeys(ctx, "default")
	if len(keys) != 1 {
		t.Fatalf("expected 1 key")
	}
	_, newSecret, err := g.RotateKey(ctx, "default", keys[0].ID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newSecret == secret {
		t.Fatalf("rotation must issue a new secret")
	}
	after, _ := g.Store().ListKeys(ctx, "default")
	if len(after) != 2 {
		t.Fatalf("expected old + new key, got %d", len(after))
	}
	var oldStatus string
	for _, k := range after {
		if k.ID == keys[0].ID {
			oldStatus = k.Status
		}
	}
	if oldStatus != StatusRevoked {
		t.Fatalf("old key status=%s want revoked", oldStatus)
	}
}

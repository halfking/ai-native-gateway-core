package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/errorplaybook"
	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
)

func TestRemediationTemplates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, pool, bucket := setupCooldownDatabase(t, ctx)
	seedStreamEvidence(t, ctx, pool, bucket)
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{remediation.SSEPluginID, remediation.DriftPluginID} {
		buildRemediationBundle(t, ctx, plugins, id, key)
	}
	manifest, err := ScanPlugins(plugins, pluginruntime.NewRegistry())
	if err != nil || len(manifest) != 0 {
		t.Fatalf("legacy discovery exposed private plugins: %v %v", manifest, err)
	}
	configPath := filepath.Join(dir, "gateway.yaml")
	config := func(port int, enabled string) string {
		return fmt.Sprintf("server:\n  host: localhost\n  port: %d\nplugins:\n  directory: %q\n  enabled: [%s]\n", port, plugins, enabled)
	}
	swapConfig := func(content string) {
		t.Helper()
		writeCooldownFile(t, configPath+".next", []byte(content), 0600)
		if err := os.Rename(configPath+".next", configPath); err != nil {
			t.Fatal(err)
		}
	}
	enabled := remediation.SSEPluginID + ", " + remediation.DriftPluginID
	swapConfig(config(8080, remediation.PluginID+", "+enabled))
	t.Setenv("LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY", hex.EncodeToString(pub))
	c, err := startPluginController(ctx, configPath, pluginruntime.NewRegistry(), pluginruntime.NewCapabilityRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ready := func(id string) bool {
		return c.ReadyCapability(id, pluginruntime.CapabilityErrorRemediation).SocketPath != ""
	}
	deadline := time.Now().Add(10 * time.Second)
	for !ready(remediation.SSEPluginID) || !ready(remediation.DriftPluginID) {
		if time.Now().After(deadline) {
			t.Fatal("a missing optional bundle prevented the other templates from starting")
		}
		time.Sleep(20 * time.Millisecond)
	}
	swapConfig(config(8080, enabled))
	if err := c.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	store := errorplaybook.NewStore(db)
	source := errorplaybook.NewSQLSource(db)
	cfg := errorplaybook.DefaultConfig()
	cfg.Enabled, cfg.AutoAnalyze, cfg.AutoExecute = true, false, false
	engine := errorplaybook.NewEngine(store, nil, nil, nil)
	worker := errorplaybook.NewWorker(engine, source, func() errorplaybook.Config { return cfg }, time.Minute).
		WithRemediation(errorplaybook.NewRemediation(source, store, c))
	runRound := func() {
		t.Helper()
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}

	const secret = "local-remediation-template-test"
	mux := http.NewServeMux()
	admin.NewErrorPlaybookHandler(store, engine, nil, pool, secret).RegisterRoutes(mux, "/api/admin/error-playbook")
	list := func(tenant, role string, want int) []errorplaybook.Execution {
		t.Helper()
		rec := cooldownAdminRequest(t, mux, secret, tenant, role, http.MethodGet,
			"/api/admin/error-playbook/executions?recommendations=only&status=awaiting_approval")
		var rows struct {
			Executions []errorplaybook.Execution `json:"executions"`
			Total      int                       `json:"total"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &rows) != nil || rows.Total != want || len(rows.Executions) != want {
			t.Fatalf("recommendations count=%d want=%d: HTTP %d %s", rows.Total, want, rec.Code, rec.Body.String())
		}
		return rows.Executions
	}
	platform := func(want int) []errorplaybook.Execution { return list("default", "super_admin", want) }
	gatewayRequest := newCooldownTrafficProbe(t)
	runRound()
	checkRemediationRows(t, platform(4), bucket, false, false)
	runRound()
	platform(4)

	// A valid semantic no-op is accepted, including presentation changes.
	swapConfig("# presentation only\n" + config(8080, enabled))
	if err := c.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	runRound()
	platform(4)

	rejectedConfig := config(8081, enabled)
	swapConfig(rejectedConfig)
	if err := c.Reload(ctx); err == nil {
		t.Fatal("restart-only port change was hot applied")
	}
	runRound()
	checkRemediationRows(t, platform(5), bucket, true, false)
	runRound()
	platform(5)
	swapConfig("# same rejected change\n" + rejectedConfig)
	if err := c.Reload(ctx); err == nil {
		t.Fatal("restart-only port change with a comment was hot applied")
	}
	runRound()
	platform(5)

	const marker = "private-config-value"
	invalidConfig := "server: [" + marker
	swapConfig(invalidConfig)
	if err := c.Reload(ctx); err == nil {
		t.Fatal("invalid configuration was accepted")
	}
	runRound()
	rows := platform(6)
	checkRemediationRows(t, rows, bucket, true, true)
	gatewayRequest()
	if contents, err := os.ReadFile(configPath); err != nil || string(contents) != invalidConfig {
		t.Fatal("recommendation altered the operator's configuration")
	}
	for _, row := range rows {
		if strings.Contains(string(row.Context), marker) || strings.Contains(string(row.Context), plugins) {
			t.Fatalf("configuration content escaped into audit: %s", row.Context)
		}
	}
	list("tenant-a", "tenant_admin", 0)
	rec := cooldownAdminRequest(t, mux, secret, "tenant-a", "tenant_admin", http.MethodGet,
		fmt.Sprintf("/api/admin/error-playbook/executions/%d", rows[0].ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("tenant detail status = %d", rec.Code)
	}

	swapConfig(config(8080, enabled))
	if err := c.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, exists := c.ConfigRejection(); exists {
		t.Fatal("valid configuration left a stale rejection")
	}
	runRound()
	platform(6)

	swapConfig(config(8080, ""))
	if err := c.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if ready(remediation.SSEPluginID) || ready(remediation.DriftPluginID) {
		t.Fatal("disabled version retained a readiness route")
	}
	swapConfig(config(8082, ""))
	if err := c.Reload(ctx); err == nil {
		t.Fatal("restart-only port change was hot applied with plugins disabled")
	}
	runRound()
	platform(6)
	gatewayRequest()
}

func buildRemediationBundle(t *testing.T, ctx context.Context, plugins, id string, key ed25519.PrivateKey) {
	t.Helper()
	path := filepath.Join(plugins, id)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	src := "../" + id + "-plugin"
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(path, id), src).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", id, err, output)
	}
	for _, file := range []string{"plugin-manifest.json", "template.yaml"} {
		data, err := os.ReadFile(filepath.Join(src, file))
		if err != nil {
			t.Fatal(err)
		}
		writeCooldownFile(t, filepath.Join(path, file), data, 0600)
		if file == "plugin-manifest.json" {
			writeCooldownFile(t, filepath.Join(path, file+".sig"), ed25519.Sign(key, data), 0600)
		}
	}
}

func seedStreamEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, bucket time.Time) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tenant          string
		provider        int
		reasons         []string
		sameID, current bool
	}{
		{"tenant-a", 7, []string{"upstream_error", "upstream_error", "upstream_error"}, false, false},
		{"tenant-b", 7, []string{"stream_chunk_timeout", "chunk_timeout", "first_byte_timeout"}, false, false},
		{"tenant-a", 8, []string{"upstream_read_error", "network_error", "eof_without_done"}, false, false},
		{"tenant-a", 9, []string{"network_error", "upstream_error", "chunk_timeout"}, false, false},
		{"tenant-a", 10, []string{"client_disconnected", "client_write_failed", "client_cancel"}, false, false},
		{"tenant-a", 11, []string{"network_error", "network_error", "network_error"}, true, false},
		{"tenant-a", 12, []string{"upstream_error", "upstream_error"}, false, false},
		{"tenant-b", 12, []string{"upstream_error", "upstream_error"}, false, false},
		{"tenant-a", 13, []string{"upstream_error", "upstream_error", "upstream_error"}, false, true},
	} {
		for i, reason := range c.reasons {
			n := i
			if c.sameID {
				n = 0
			}
			ts := bucket.Add(20 * time.Second)
			if c.current {
				ts = time.Now().UTC().Add(time.Hour)
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO candidate_failure_logs_hot (ts, request_id, tenant_id, provider_id, error_kind, context)
				VALUES ($1, $2, $3, $4, 'stream_timeout', jsonb_build_object('stream_reason', $5::text, 'message', 'private-stream-data'))
			`, ts, fmt.Sprintf("%s/%d/%d", c.tenant, c.provider, n), c.tenant, c.provider, reason)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func checkRemediationRows(t *testing.T, rows []errorplaybook.Execution, bucket time.Time, wantRejected, wantInvalid bool) {
	t.Helper()
	expected := map[string]string{
		"tenant-a/7": remediation.ReviewUpstream, "tenant-b/7": remediation.ReviewProxyIdle,
		"tenant-a/8": remediation.ReviewNetwork, "tenant-a/9": remediation.ReviewMixed,
	}
	if wantRejected {
		expected[remediation.ConfigChanged] = remediation.ReviewConfig
	}
	if wantInvalid {
		expected[remediation.ConfigInvalid] = remediation.RestoreConfig
	}
	for _, row := range rows {
		if row.EntryID != 0 || len(row.Steps) != 0 || row.Status != errorplaybook.StatusAwaitingApproval {
			t.Fatalf("recommendation became an executable entry: %+v", row)
		}
		var audit struct {
			remediation.SSESignal
			TenantID       string `json:"tenant_id"`
			ProviderID     int    `json:"provider_id"`
			Kind           string `json:"kind"`
			Recommendation string `json:"recommendation"`
		}
		if err := json.Unmarshal(row.Context, &audit); err != nil || strings.Contains(string(row.Context), "private-stream-data") {
			t.Fatalf("bad audit evidence: %s (%v)", row.Context, err)
		}
		key := audit.Kind
		if key == "" {
			key = fmt.Sprintf("%s/%d", audit.TenantID, audit.ProviderID)
			if audit.WindowStartUnix != bucket.Unix() || audit.DistinctRequests != 3 {
				t.Fatalf("incorrect SSE aggregate: %s", row.Context)
			}
		}
		if expected[key] == "" || audit.Recommendation != expected[key] {
			t.Fatalf("unexpected recommendation: %s", row.Context)
		}
		delete(expected, key)
	}
	if len(expected) > 0 {
		t.Fatalf("missing recommendations: %v", expected)
	}
}

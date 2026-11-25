package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/errorplaybook"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/controller"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

func TestUpstreamCooldownPilot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, pool, bucket := setupCooldownDatabase(t, ctx)
	store := errorplaybook.NewStore(db)
	caps := pluginruntime.NewCapabilityRegistry()
	root := t.TempDir()
	configPath := filepath.Join(root, "gateway.yaml")
	plugins := filepath.Join(root, "plugins")
	writeCooldownConfig(t, configPath, plugins, false)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY", hex.EncodeToString(pub))
	runtime, err := startPluginController(ctx, configPath, pluginruntime.NewRegistry(), caps)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	bundle := filepath.Join(plugins, remediation.PluginID)
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, remediation.PluginID)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "../upstream-cooldown-plugin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build real plugin: %v\n%s", err, out)
	}
	executable, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	const templateV1 = "schema_version: 1\ncooldown_429_seconds: 300\ncooldown_502_seconds: 120\n"
	writeCooldownBundle(t, bundle, priv, "1.0.0", templateV1, executable, true)
	manifests, err := ScanPlugins(plugins, pluginruntime.NewRegistry())
	if err != nil || len(manifests) != 0 {
		t.Fatalf("legacy startup discovered the pilot: %v, %v", manifests, err)
	}
	seedCooldownEvidence(t, ctx, pool, bucket)
	adminMux := http.NewServeMux()
	const secret = "local-upstream-cooldown-test"
	adminEngine := errorplaybook.NewEngine(store, nil, nil, nil)
	admin.NewErrorPlaybookHandler(store, adminEngine, nil, pool, secret).RegisterRoutes(adminMux, "/api/admin/error-playbook")
	registerPluginAPIProxy(adminMux, []byte(secret), runtime.BaseURL, nil, nil, pool, secret)

	workerRound := func() error {
		cfg := errorplaybook.DefaultConfig()
		cfg.Enabled, cfg.AutoAnalyze, cfg.AutoExecute = true, false, false
		source := errorplaybook.NewSQLSource(db)
		engine := errorplaybook.NewEngine(store, nil, nil, nil)
		return errorplaybook.NewWorker(engine, source, func() errorplaybook.Config { return cfg }, time.Minute).
			WithRemediation(errorplaybook.NewRemediation(source, store, runtime)).RunOnce(ctx)
	}
	runRound := func() {
		t.Helper()
		if err := workerRound(); err != nil {
			t.Fatal(err)
		}
	}
	ready := func() controller.CapabilityEndpoint {
		return runtime.ReadyCapability(remediation.PluginID, pluginruntime.CapabilityErrorRemediation)
	}
	list := func(tenant, role string) []errorplaybook.Execution {
		t.Helper()
		rec := cooldownAdminRequest(t, adminMux, secret, tenant, role, http.MethodGet,
			"/api/admin/error-playbook/executions?error_type=plugin:upstream-cooldown")
		var result struct {
			Executions []errorplaybook.Execution `json:"executions"`
			Total      int                       `json:"total"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil || result.Total != len(result.Executions) {
			t.Fatalf("audit list status=%d body=%s", rec.Code, rec.Body.String())
		}
		return result.Executions
	}
	adminList := func() []errorplaybook.Execution { return list("default", "super_admin") }
	checkCount := func(want int) {
		t.Helper()
		if got := len(adminList()); got != want {
			t.Fatalf("audit rows = %d, want %d", got, want)
		}
	}
	gatewayRequest := newCooldownTrafficProbe(t)
	gatewayRequest()
	runRound()
	checkCount(0)
	if ready().SocketPath != "" {
		t.Fatal("disabled bundle has a ready endpoint")
	}

	writeCooldownConfig(t, configPath, plugins, true)
	if err := runtime.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ready().Version; got != "1.0.0" {
		t.Fatalf("enabled version = %q", got)
	}
	gatewayRequest()
	runRound()
	checkCount(3)
	assertCooldownRecommendations(t, adminList(), bucket)
	runRound()
	checkCount(3)
	if len(list("tenant-a", "tenant_admin")) != 0 || len(list("tenant-b", "super_admin")) != 0 {
		t.Fatal("platform-wide cluster leaked through tenant list")
	}
	executionURL := fmt.Sprintf("/api/admin/error-playbook/executions/%d", adminList()[0].ID)
	if got := cooldownAdminRequest(t, adminMux, secret, "tenant-b", "tenant_admin", http.MethodGet, executionURL).Code; got != 404 {
		t.Fatalf("tenant execution detail status = %d", got)
	}
	if got := cooldownAdminRequest(t, adminMux, secret, "default", "super_admin", http.MethodGet, executionURL).Code; got != 200 {
		t.Fatalf("platform execution detail status = %d", got)
	}
	if got := cooldownAdminRequest(t, adminMux, secret, "default", "super_admin", http.MethodPost, executionURL+"/revert").Code; got != 409 {
		t.Fatalf("recommendation rollback status = %d", got)
	}
	if got := cooldownAdminRequest(t, adminMux, secret, "default", "super_admin", http.MethodPost,
		"/plugins/upstream-cooldown/api/internal/remediation/v1/suggest").Code; got != 404 {
		t.Fatalf("gateway exposed the private suggestion endpoint: %d", got)
	}
	visible, err := store.VisibleExecutionSummary(ctx, false)
	if err != nil || visible.Last24h[errorplaybook.StatusAwaitingApproval] != 0 {
		t.Fatalf("tenant audit stats = %+v, %v", visible, err)
	}

	// A healthy process with the declared capability still needs permission.
	writeCooldownBundle(t, bundle, priv, "1.0.1", templateV1, executable, true)
	path := filepath.Join(bundle, "plugin-manifest.json")
	manifest, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withoutPermission := []byte(strings.ReplaceAll(string(manifest), `"permissions": ["error-remediation"]`, `"permissions": []`))
	writeCooldownFile(t, path, withoutPermission, 0600)
	writeCooldownFile(t, path+".sig", ed25519.Sign(priv, withoutPermission), 0600)
	if err := runtime.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.BaseURL(remediation.PluginID) == "" || ready().SocketPath != "" {
		t.Fatal("ready process without permission passed capability checks")
	}
	runRound()
	checkCount(3)

	// A new signed version can produce one new record per event. Competing
	// rounds use separate transactions just as gateway replicas would.
	const templateV2 = "schema_version: 1\ncooldown_429_seconds: 600\ncooldown_502_seconds: 240\n"
	writeCooldownBundle(t, bundle, priv, "1.1.0", templateV2, executable, true)
	if err := runtime.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range cap(errs) {
		wg.Go(func() { errs <- workerRound() })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	checkCount(6)
	assertCooldownRecommendations(t, adminList(), bucket)

	// A signature rejection and a signed bundle that fails readiness must both
	// restore the actual pinned version even after files are overwritten.
	for _, bad := range []struct {
		name, template string
		sign           bool
	}{
		{"bad signature", templateV2, false},
		{"bad readiness", "schema_version: 0\n", true},
	} {
		t.Run(bad.name, func(t *testing.T) {
			writeCooldownBundle(t, bundle, priv, "1.2.0", bad.template, executable, bad.sign)
			if bad.sign {
				result := make(chan error, 1)
				go func() { result <- runtime.Reconcile(ctx) }()
				limit := time.Now().Add(3 * time.Second)
				for ready().SocketPath != "" {
					if time.Now().After(limit) {
						t.Fatal("upgrade did not withdraw the old route")
					}
					time.Sleep(5 * time.Millisecond)
				}
				gatewayRequest()
				if err := <-result; err == nil {
					t.Fatal("invalid readiness upgrade succeeded")
				}
			} else if err := runtime.Reconcile(ctx); err == nil {
				t.Fatal("unsigned upgrade succeeded")
			}
			if got := ready().Version; got != "1.1.0" {
				t.Fatalf("rollback version = %q", got)
			}
			runRound()
			checkCount(6)
			gatewayRequest()
		})
	}

	for i, mode := range []string{"bad_output", "duplicate_fields", "oversize", "wrong_version", "timeout", "crash"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(root, mode)
			faultBundle := cooldownFaultWrapper(t, marker, mode)
			writeCooldownBundle(t, bundle, priv, fmt.Sprintf("2.0.%d", i), templateV2, faultBundle, true)
			if err := runtime.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			started := time.Now()
			go func() { done <- workerRound() }()
			deadline := time.Now().Add(4 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker did not call fault process")
				}
				time.Sleep(5 * time.Millisecond)
			}
			gatewayRequest()
			if mode == "timeout" {
				select {
				case err := <-done:
					t.Fatalf("traffic probe did not finish while plugin request was stalled: %v", err)
				default:
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if mode == "timeout" && (time.Since(started) < 300*time.Millisecond || time.Since(started) >= 3*time.Second) {
				t.Fatalf("timeout round took %s", time.Since(started))
			}
			if mode == "crash" && ready().SocketPath != "" {
				t.Fatal("crashed process is still ready")
			}
			checkCount(6)
		})
	}

	writeCooldownBundle(t, bundle, priv, "1.1.0", templateV2, executable, true)
	if err := runtime.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ready().Version; got != "1.1.0" {
		t.Fatalf("recovery version = %q", got)
	}
	writeCooldownConfig(t, configPath, plugins, false)
	if err := runtime.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if ready().SocketPath != "" {
		t.Fatal("disabled plugin remains ready")
	}
	runRound()
	checkCount(6)
	gatewayRequest()
}

func setupCooldownDatabase(t *testing.T, ctx context.Context) (*sql.DB, *pgxpool.Pool, time.Time) {
	t.Helper()
	dsn := os.Getenv("TEST_UPSTREAM_COOLDOWN_DSN")
	if dsn == "" {
		t.Skip("set TEST_UPSTREAM_COOLDOWN_DSN to an isolated PostgreSQL database")
	}
	conn, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("cooldown_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer conn.Close()
		if _, err := conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := stdlib.OpenDB(*cfg.ConnConfig)
	t.Cleanup(func() { _ = db.Close() })
	bootstrap, err := os.ReadFile("../../sql/migrations/startup/995_error_playbook.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE schema_migrations (version text PRIMARY KEY, description text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, strings.ReplaceAll(string(bootstrap), "public.", schema+".")); err != nil {
		t.Fatal(err)
	}
	bucket := time.Now().UTC().Add(-15 * time.Second).Truncate(remediation.Window).Add(-remediation.Window)
	fixture := fmt.Sprintf(`
		CREATE TABLE candidate_failure_logs_hot (
			ts timestamptz NOT NULL DEFAULT '%s', request_id text NOT NULL, tenant_id text NOT NULL,
			session_id text, credential_id int, provider_id int NOT NULL, raw_model_name text, attempt_index int,
			error_kind text, error_message text, upstream_status_code int, upstream_response_body text,
			upstream_response_preview text, latency_ms int, per_attempt_latency_ms int, retryable bool, context jsonb
		);
		ALTER TABLE candidate_failure_logs_hot ENABLE ROW LEVEL SECURITY;
		ALTER TABLE candidate_failure_logs_hot FORCE ROW LEVEL SECURITY;
		CREATE POLICY candidate_failure_visibility ON candidate_failure_logs_hot
			USING (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true');
		CREATE TABLE supplier_errors_hot (
			occurred_at timestamptz, request_id text, trace_id text, tenant_id text, session_id text,
			provider_id int, supplier text, credential_id int, model text, attempt_seq int, error_type text,
			error_code text, http_status int, error_message text, is_retryable bool, stage text, latency_ms int,
			affected_users int, request_metadata jsonb
		);
		CREATE TABLE settings_kv (key text PRIMARY KEY, value jsonb);
	`, bucket.Add(15*time.Second).Format(time.RFC3339))
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	return db, pool, bucket
}

func seedCooldownEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, bucket time.Time) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/429":
			http.Error(w, "upstream rate limit", 429)
		case "/502":
			http.Error(w, "upstream bad gateway", 502)
		case "/503":
			http.Error(w, "upstream unavailable", 503)
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>relay sign-in</html>")
		}
	}))
	defer server.Close()
	client := upstream.NewWithRetries(0)
	writer := executors.NewCandidateFailureWriter(pool)
	for _, sample := range []struct {
		tenant, path, prefix string
		provider, requests   int
	}{
		{"tenant-a", "/429", "rate", 7, 3},
		{"tenant-b", "/502", "bad-gateway", 7, 3},
		{"tenant-a", "/429", "mixed-rate", 8, 2},
		{"tenant-a", "/502", "mixed-gateway", 8, 1},
		{"tenant-a", "/429", "split", 9, 2},
		{"tenant-b", "/429", "split", 9, 2},
		{"tenant-a", "/429", "split-provider", 19, 2},
		{"tenant-a", "/429", "split-provider", 20, 1},
		{"tenant-a", "/503", "other", 10, 3},
		{"tenant-a", "/html", "synthetic", 11, 3},
		{"tenant-a", "/429", "single-event", 12, 1},
		{"tenant-a", "/429", "boundary", 13, 2},
		{"tenant-a", "/429", "future-window", 14, 3},
		{"", "/502", "empty-tenant", 15, 3},
		{"tenant-a", "/502", "empty-provider", 0, 3},
		{"tenant-a", "/502", "retries", 16, 1},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+sample.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, upstreamErr := client.Do(req)
		if upstreamErr == nil {
			upstreamErr = upstream.ErrorFromResponse(resp)
		}
		_ = resp.Body.Close()
		for i := range sample.requests {
			requestID := fmt.Sprintf("%s-%d", sample.prefix, i)
			writer.LogFailure(requestID, sample.tenant, "private-session", 42, sample.provider, "private-model", 0, upstreamErr, nil, nil,
				map[string]any{"upstream_http_status_observed": true})
		}
		if sample.prefix == "retries" {
			for i := 1; i <= 4; i++ {
				writer.LogFailure("retries-0", sample.tenant, "", 42, sample.provider, "private-model", i, upstreamErr, nil, nil, nil)
			}
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{
		"SELECT set_config('app.bypass_rls', 'true', true)",
		"UPDATE candidate_failure_logs_hot SET ts = ts + interval '5 minutes' WHERE request_id = 'boundary-1'",
		"UPDATE candidate_failure_logs_hot SET ts = now() + interval '1 hour' WHERE provider_id = 14",
		"INSERT INTO candidate_failure_logs_hot (request_id, tenant_id, provider_id, upstream_status_code, error_kind) SELECT 'legacy-' || n, 'tenant-a', 18, 502, 'upstream_down' FROM generate_series(1,3) AS n",
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var synthetic, observed int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE provider_id = 11 AND upstream_status_code = 502 AND NOT (context @> '{"upstream_http_status_observed":true}')),
		COUNT(*) FILTER (WHERE provider_id = 7 AND context @> '{"upstream_http_status_observed":true}') FROM candidate_failure_logs_hot WHERE ts >= $1`, bucket).
		Scan(&synthetic, &observed); err != nil || synthetic != 3 || observed != 6 {
		t.Fatalf("log provenance synthetic=%d observed=%d: %v", synthetic, observed, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func writeCooldownConfig(t *testing.T, path, plugins string, enabled bool) {
	t.Helper()
	if err := os.MkdirAll(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	list := "[]"
	if enabled {
		list = "[upstream-cooldown]"
	}
	data := fmt.Sprintf("server:\n  host: localhost\n  port: 8080\nplugins:\n  directory: %q\n  enabled: %s\n", plugins, list)
	writeCooldownFile(t, path+".next", []byte(data), 0600)
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
}

func writeCooldownFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func writeCooldownBundle(t *testing.T, dir string, key ed25519.PrivateKey, version, template string, executable []byte, signed bool) {
	t.Helper()
	manifest, err := os.ReadFile("../upstream-cooldown-plugin/plugin-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest = []byte(strings.ReplaceAll(string(manifest), "1.0.0", version))
	sig := ed25519.Sign(key, manifest)
	if !signed {
		sig[0] ^= 1
	}
	writeCooldownFile(t, filepath.Join(dir, "plugin-manifest.json"), manifest, 0600)
	writeCooldownFile(t, filepath.Join(dir, "plugin-manifest.json.sig"), sig, 0600)
	writeCooldownFile(t, filepath.Join(dir, "template.yaml"), []byte(template), 0600)
	writeCooldownFile(t, filepath.Join(dir, remediation.PluginID), executable, 0700)
}

func cooldownAdminRequest(t *testing.T, handler http.Handler, secret, tenant, role, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	token, _, err := admin.SignToken(1, tenant, "operator", role, secret, false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func assertCooldownRecommendations(t *testing.T, records []errorplaybook.Execution, window time.Time) {
	t.Helper()
	for _, row := range records {
		var audit struct {
			remediation.Signal
			TemplateVersion string `json:"template_version"`
			Recommendation  string `json:"recommendation"`
			CooldownSeconds int    `json:"cooldown_seconds"`
			TenantID        string `json:"tenant_id"`
			Provider        int    `json:"provider_id"`
		}
		if err := json.Unmarshal(row.Context, &audit); err != nil {
			t.Fatal(err)
		}
		if row.EntryID != 0 || row.Status != errorplaybook.StatusAwaitingApproval || len(row.Steps) != 0 ||
			row.ExecutedBy != errorplaybook.CooldownActor || audit.Recommendation != remediation.ReviewNextStep ||
			audit.WindowStartUnix != window.Unix() || audit.DistinctRequests != 3 {
			t.Fatalf("invalid recommendation: %+v, %s", row, row.Context)
		}
		want429, want502, seconds := int64(3), int64(0), 300
		switch {
		case audit.TenantID == "tenant-b" && audit.Provider == 7:
			want429, want502, seconds = 0, 3, 120
		case audit.TenantID == "tenant-a" && audit.Provider == 8:
			want429, want502 = 2, 1
		case audit.TenantID == "tenant-a" && audit.Provider == 7:
		default:
			t.Fatalf("unexpected cluster: %s", row.Context)
		}
		if audit.TemplateVersion == "1.1.0" {
			seconds *= 2
		} else if audit.TemplateVersion != "1.0.0" {
			t.Fatalf("unexpected template: %s", row.Context)
		}
		if audit.Count429 != want429 || audit.Count502 != want502 || audit.CooldownSeconds != seconds ||
			strings.Contains(string(row.Context), "private-") {
			t.Fatalf("bad evidence or cooldown: %s", row.Context)
		}
	}
}

func cooldownFaultWrapper(t *testing.T, marker, mode string) []byte {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	return []byte(fmt.Sprintf("#!/bin/sh\nexport COOLDOWN_FAULT=%s COOLDOWN_MARKER=%s\nexec %s -test.run='^TestUpstreamCooldownFaultProcess$'\n",
		quote(mode), quote(marker), quote(binary)))
}

func TestUpstreamCooldownFaultProcess(t *testing.T) {
	mode := os.Getenv("COOLDOWN_FAULT")
	if mode == "" || os.Getenv("GATEWAY_PLUGIN_ID") != remediation.PluginID {
		return
	}
	m, err := pluginruntime.LoadManifest(os.Getenv("AI_SESSION_MANAGER_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	socket := os.Getenv("AI_SESSION_MANAGER_PLUGIN_SOCKET")
	_ = os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /handshake", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pluginruntime.HandshakeResponse{
			PluginID: m.PluginID, PluginVersion: m.PluginVersion, APIContract: m.GatewayCompatibility.APIContract,
			Status: "ready", Capabilities: m.Capabilities,
		})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST "+remediation.SuggestPath, func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(io.LimitReader(r.Body, remediation.MaxPayload+1))
		if err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if signal, err := remediation.DecodeSignal(strings.NewReader(string(payload))); err != nil || !signal.Valid() {
			http.Error(w, "unexpected payload", 400)
			return
		}
		// A strict decode before writing the marker proves the worker sent
		// aggregate fields only, even when the process fails afterward.
		if err := os.WriteFile(os.Getenv("COOLDOWN_MARKER"), payload, 0600); err != nil {
			http.Error(w, "marker failed", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch mode {
		case "bad_output":
			fmt.Fprintf(w, `{"schema_version":1,"template_version":%q,"recommendation":"review_next_model_ladder_step","cooldown_seconds":10,"steps":[]}`, m.PluginVersion)
		case "duplicate_fields":
			fmt.Fprintf(w, `{"schema_version":1,"template_version":%q,"recommendation":"review_next_model_ladder_step","cooldown_seconds":10,"cooldown_seconds":20}`, m.PluginVersion)
		case "oversize":
			_, _ = io.WriteString(w, strings.Repeat(" ", remediation.MaxPayload+1))
		case "wrong_version":
			_, _ = io.WriteString(w, `{"schema_version":1,"template_version":"9.9.9","recommendation":"review_next_model_ladder_step","cooldown_seconds":10}`)
		case "timeout":
			<-r.Context().Done()
		case "crash":
			os.Exit(42)
		}
	})
	if err := http.Serve(l, mux); err != nil {
		t.Fatal(err)
	}
}

func newCooldownTrafficProbe(t *testing.T) func() {
	t.Helper()
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")
	var stats struct {
		code        int
		contentType string
		body        string
	}
	release := make(chan chan struct{}, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"message":"normal completion"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case done := <-release:
			close(done)
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: second\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(origin.Close)
	target, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	mux, deps, ok := v2DispatchMux(proxy, nil, nil, nil)
	if !ok {
		t.Fatal("v2 dispatch unavailable")
	}
	t.Cleanup(func() { v2ShutdownPipeline(deps) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 3 * time.Second
	observe := func(stream bool) (int, string, string) {
		t.Helper()
		payload := fmt.Sprintf(`{"model":"gpt-4","stream":%t,"messages":[{"role":"user","content":"test"}]}`, stream)
		resp, err := client.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		var first string
		if stream {
			first, err = reader.ReadString('\n')
			if err != nil || first != "data: first\n" {
				t.Fatalf("SSE first frame was not flushed: %q, %v", first, err)
			}
			done := make(chan struct{})
			release <- done
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("origin did not release SSE stream")
			}
		}
		rest, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header.Get("Content-Type"), first + string(rest)
	}
	normalCode, normalType, normalBody := observe(false)
	sseCode, sseType, sseBody := observe(true)
	if normalCode != 200 || normalType != "application/json" || normalBody != `{"message":"normal completion"}` ||
		sseCode != 200 || sseType != "text/event-stream" || sseBody != "data: first\n\ndata: second\n\ndata: [DONE]\n\n" {
		t.Fatalf("bad traffic baseline: HTTP %d %s %s SSE %d %s %s", normalCode, normalType, normalBody, sseCode, sseType, sseBody)
	}
	return func() {
		t.Helper()
		stats.code, stats.contentType, stats.body = observe(false)
		if stats.code != normalCode || stats.contentType != normalType || stats.body != normalBody {
			t.Fatalf("normal HTTP differs from baseline: %+v", stats)
		}
		stats.code, stats.contentType, stats.body = observe(true)
		if stats.code != sseCode || stats.contentType != sseType || stats.body != sseBody {
			t.Fatalf("SSE differs from baseline: %+v", stats)
		}
	}
}

func TestUpstreamCooldownMissingAtStartup(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "gateway.yaml")
	plugins := filepath.Join(root, "plugins")
	writeCooldownConfig(t, configPath, plugins, true)
	c, err := startPluginController(context.Background(), configPath,
		pluginruntime.NewRegistry(), pluginruntime.NewCapabilityRegistry())
	if err != nil {
		t.Fatalf("missing optional bundle blocked startup: %v", err)
	}
	defer c.Close()
	if c.BaseURL(remediation.PluginID) != "" {
		t.Fatal("missing bundle acquired a route")
	}
	writeCooldownConfig(t, configPath, plugins, false)
	if err := c.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
}

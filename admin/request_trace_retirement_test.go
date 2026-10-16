package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func adminTraceRetirementPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TRACE_RETIREMENT_TEST_DSN")
	if dsn == "" {
		t.Skip("TRACE_RETIREMENT_TEST_DSN not supplied")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated PG configuration")
	}
	localSchema := os.Getenv("TRACE_RETIREMENT_LOCAL_SCHEMA") == "1" && cfg.ConnConfig.Port == 54318
	if (cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost") || (!strings.HasPrefix(cfg.ConnConfig.Database, "trace_retirement") && !localSchema) {
		t.Fatal("requires loopback trace_retirement database or explicitly authorized local 54318 schema isolation")
	}
	ctx := context.Background()
	root, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	var version int
	if err := root.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version/10000 != 17 {
		t.Fatalf("requires PG17: %d %v", version, err)
	}
	t.Logf("isolated PostgreSQL version_num=%d", version)
	schema := fmt.Sprintf("admin_trace_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"session_turns_hot", "session_turns", "_request_logs_hot", "_request_logs"} {
		if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (request_id text,ts timestamptz DEFAULT now(),request_status text,trace_events jsonb)"); err != nil {
			t.Fatal(err)
		}
	}
	// The prompt metadata lookup may be unavailable: existing code intentionally
	// returns a request_id-only summary. Trace behavior must not depend on that view.
	return pool
}

func TestRequestTraceRetirementHTTPPG17(t *testing.T) {
	pool := adminTraceRetirementPG(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	NewRequestTraceHandler(nil, pool).RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Role") != "super_admin" {
				writeAdminError(w, http.StatusForbidden, "forbidden", "super_admin required")
				return
			}
			next(w, r)
		}
	})
	for _, table := range []string{"session_turns_hot", "session_turns", "_request_logs_hot", "_request_logs"} {
		for _, tc := range []struct {
			name, status, trace string
			code                int
			key                 string
		}{
			{"processing", "in_progress", "", 202, "not_ready"},
			{"pending", "pending", "", 202, "not_ready"},
			{"success_without_events", "success", "", 409, "trace_unavailable"},
			{"failed_without_events", "failure", "null", 409, "trace_unavailable"},
			{"ready", "success", `{"events":[{"seq":1,"stage":"request_complete","status":"success"}],"final_status":"success"}`, 200, ""},
		} {
			t.Run(table+"_"+tc.name, func(t *testing.T) {
				id := table + "_" + tc.name
				var raw any
				if tc.trace != "" {
					raw = tc.trace
				}
				if _, err := pool.Exec(ctx, "INSERT INTO "+table+"(request_id,request_status,trace_events) VALUES($1,$2,$3::jsonb)", id, tc.status, raw); err != nil {
					t.Fatal(err)
				}
				// An empty new-session row must not be masked by a legacy ready row.
				if strings.HasPrefix(table, "session_turns") && tc.code != 200 {
					if _, err := pool.Exec(ctx, `INSERT INTO _request_logs_hot(request_id,request_status,trace_events) VALUES($1,'success','{"events":[],"final_status":"stale"}')`, id); err != nil {
						t.Fatal(err)
					}
				}
				for _, endpoint := range []string{"trace", "ai-prompt"} {
					method := http.MethodGet
					if endpoint == "ai-prompt" {
						method = http.MethodPost
					}
					req := httptest.NewRequest(method, "/api/admin/requests/"+id+"/"+endpoint, strings.NewReader(`{}`))
					req.Header.Set("X-Test-Role", "super_admin")
					out := httptest.NewRecorder()
					mux.ServeHTTP(out, req)
					if out.Code != tc.code {
						t.Fatalf("%s status=%d want=%d body=%s", endpoint, out.Code, tc.code, out.Body.String())
					}
					if tc.key != "" && !strings.Contains(out.Body.String(), `"`+tc.key+`"`) {
						t.Fatalf("%s error key missing: %s", endpoint, out.Body.String())
					}
					if tc.code == 200 {
						want := `"source":"postgres"`
						if endpoint == "ai-prompt" {
							want = `"event_count":1`
						}
						if !strings.Contains(out.Body.String(), want) {
							t.Fatalf("%s ready payload: %s", endpoint, out.Body.String())
						}
					}
					denied := httptest.NewRecorder()
					req.Header.Set("X-Test-Role", "admin")
					mux.ServeHTTP(denied, req)
					if denied.Code != http.StatusForbidden {
						t.Fatalf("authorization bypass on %s: %d", endpoint, denied.Code)
					}
				}
			})
		}
	}
	for _, endpoint := range []string{"trace", "ai-prompt"} {
		method := http.MethodGet
		if endpoint == "ai-prompt" {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, "/api/admin/requests/missing/"+endpoint, nil)
		req.Header.Set("X-Test-Role", "super_admin")
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		if out.Code != 404 {
			t.Fatalf("%s missing status=%d", endpoint, out.Code)
		}
	}
}

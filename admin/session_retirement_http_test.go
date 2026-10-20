package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionRetirementHTTPGuards(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"swim", h.HandleSwimLaneInit}, {"inspector", h.HandleSessionInspectorStats},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{"GET", "POST"} {
				w := httptest.NewRecorder()
				tc.handler(w, httptest.NewRequest(method, "/", nil))
				want := http.StatusServiceUnavailable
				if method == "POST" {
					want = http.StatusMethodNotAllowed
				}
				if w.Code != want {
					t.Fatalf("%s status=%d want=%d", method, w.Code, want)
				}
			}
		})
	}
}

func TestInspectorStatsTenantFilters(t *testing.T) {
	a, b, c := inspectorStatsTenantFilters("acme")
	if a != "WHERE ss.tenant_id = $1" || b != "AND ss.tenant_id = $1" || c != "AND sd.tenant_id = $1" {
		t.Fatalf("wrong filters: %q %q %q", a, b, c)
	}
	a, b, c = inspectorStatsTenantFilters("")
	if a != "" || b != "" || c != "" {
		t.Fatal("platform query unexpectedly filtered")
	}
}

// This test creates only a uniquely named schema; it never clears public or
// mutates business rows. Set the DSN explicitly to the isolated local database.
func TestSessionRetirementHTTPRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_RETIREMENT_HTTP_DSN")
	if dsn == "" {
		t.Skip("TEST_RETIREMENT_HTTP_DSN not set")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("open isolated DB failed")
	}
	defer root.Close()
	schema := fmt.Sprintf("retirement_http_%d", time.Now().UnixNano())
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	// ★★ search_path 必须**追加** public（不能只留隔离 schema）。
	//
	//   本库装了 citus_columnar，库级 EVENT TRIGGER `enforce_columnar_trigger`
	//   → `public.fn_enforce_columnar_event_trigger()` 的函数体里是**不加限定名**地
	//   调用 `columnar_insert_only_parents()`（它自己就在 public）
	//   ⇒ 而 event trigger 的函数体按**会话当时的 search_path** 解析名字
	//   ⇒ ⇒ search_path 里没有 public ⇒ **每一次 CREATE TABLE 都 42883**。
	//
	//   ★ 同款坑仓内已有两处明确记录（`domains/reportrollup/grainreport_e2e_test.go`、
	//   `tools/benchreport/main.go`）；公共样板见 `internal/testschema/testschema.go`。
	//   ★ 隔离性不受影响：public 排在隔离 schema **之后** ⇒ 裸名仍优先命中隔离 schema。
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public,pg_catalog"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
 CREATE TABLE session_summaries(session_key text,tenant_id text,total_tokens bigint,health_score numeric);
 CREATE TABLE session_dim(gw_session_id text,tenant_id text,status text,closed_at timestamptz,stop_reason text);
 INSERT INTO session_summaries VALUES('a','acme',42,80),('b','other',90,20);
 INSERT INTO session_dim VALUES('a','acme','active',NULL,NULL),('b','other','closed',NOW(),'manual_admin');
 CREATE TABLE request_logs_with_current_month(request_id text,ts timestamptz,tenant_id text,client_model text,outbound_model text,canonical_id bigint,credential_id bigint,request_status text,error_kind text,latency_ms int,cost_usd numeric,prompt_tokens int,completion_tokens int);
 CREATE TABLE models_canonical(id bigint,canonical_name text,family bigint);
 CREATE TABLE model_families(id bigint,vendor text);
 CREATE TABLE credentials(id bigint,provider_id bigint);
 CREATE TABLE providers(id bigint,display_name text,catalog_code text,code text);
 INSERT INTO request_logs_with_current_month VALUES('new-a',NOW(),'acme','test-model','test-model',NULL,NULL,'success',NULL,5,0,1,2),('new-b',NOW(),'other','other','other',NULL,NULL,'failure',NULL,6,0,1,2);
 `)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: pool}
	for _, role := range []string{"tenant_admin", "user", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			req := SetAuthContext(httptest.NewRequest("GET", "/api/admin/sessions/inspector-stats?tenant_id=other", nil), &AuthContext{Role: role, TenantID: "acme"})
			w := httptest.NewRecorder()
			h.HandleSessionInspectorStats(w, req)
			if w.Code != 200 {
				t.Fatalf("stats HTTP %d: %s", w.Code, w.Body.String())
			}
			var got SessionInspectorStatsResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			wantTokens := int64(42)
			wantClosed := 0
			if role == "super_admin" {
				wantTokens = 132
				wantClosed = 1
			}
			wantScore, wantRecycled := float64(80), 0
			if role == "super_admin" {
				wantScore, wantRecycled = 50, 1
			}
			if int64(got.TotalTokens) != wantTokens || got.ActiveSessions != 1 || got.ClosedSessions != wantClosed || got.AvgHealthScore != wantScore || got.RecycledToday != wantRecycled {
				t.Fatalf("wrong stats: %+v", got)
			}
			req = SetAuthContext(httptest.NewRequest("GET", "/api/admin/dashboard/swim-lane-init?tenant_id=other", nil), &AuthContext{Role: role, TenantID: "acme"})
			w = httptest.NewRecorder()
			h.HandleSwimLaneInit(w, req)
			if w.Code != 200 {
				t.Fatalf("swim HTTP %d: %s", w.Code, w.Body.String())
			}
			var swim SwimLaneInitResponse
			if err := json.Unmarshal(w.Body.Bytes(), &swim); err != nil {
				t.Fatal(err)
			}
			want := 1
			if role == "super_admin" {
				want = 2
			}
			if len(swim.Requests) != want || swim.Stats.TotalCount != want {
				t.Fatalf("wrong swim: %+v", swim)
			}
			if role != "super_admin" && swim.Requests[0].RequestID != "new-a" {
				t.Fatal("cross-tenant request leaked")
			}
		})
	}
	_, err = pool.Exec(ctx, `
 ALTER TABLE session_summaries ADD COLUMN title text, ADD COLUMN summary text, ADD COLUMN gw_project_id text, ADD COLUMN gw_task_id text, ADD COLUMN user_tags text[] DEFAULT '{}', ADD COLUMN user_intent text, ADD COLUMN session_status text DEFAULT 'active', ADD COLUMN first_request_at timestamptz DEFAULT NOW(), ADD COLUMN last_request_at timestamptz DEFAULT NOW(), ADD COLUMN duration_seconds int DEFAULT 0, ADD COLUMN request_count int DEFAULT 1, ADD COLUMN success_count int DEFAULT 1, ADD COLUMN error_count int DEFAULT 0, ADD COLUMN total_cost_usd numeric DEFAULT 0, ADD COLUMN models_used text[] DEFAULT '{}', ADD COLUMN primary_model text, ADD COLUMN last_summarized_at timestamptz, ADD COLUMN key_topics text[] DEFAULT '{}', ADD COLUMN quality_score int, ADD COLUMN total_prompt_tokens bigint DEFAULT 0, ADD COLUMN total_completion_tokens bigint DEFAULT 0, ADD COLUMN updated_at timestamptz DEFAULT NOW();
 ALTER TABLE session_dim ADD COLUMN task_id text, ADD COLUMN owner_user text;
 UPDATE session_dim SET owner_user='alice';
 ALTER TABLE request_logs_with_current_month ADD COLUMN gw_session_id text, ADD COLUMN request_preview text, ADD COLUMN success bool, ADD COLUMN total_tokens int;
 UPDATE request_logs_with_current_month SET gw_session_id='a',success=true,total_tokens=3 WHERE request_id='new-a';
 `)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, role string) *http.Request {
		return SetAuthContext(httptest.NewRequest(method, path, strings.NewReader(body)), &AuthContext{Role: role, TenantID: "acme", Username: "alice"})
	}
	w := httptest.NewRecorder()
	h.HandleSessionManagementList(w, request("GET", "/api/sessions/list?tenant_id=other", "", "user"))
	if w.Code != 200 {
		t.Fatalf("list HTTP %d: %s", w.Code, w.Body.String())
	}
	var list SessionManagementResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Sessions) != 1 || list.Sessions[0].SessionKey != "a" {
		t.Fatalf("wrong list: %+v", list)
	}
	w = httptest.NewRecorder()
	h.HandleSessionManagementDetail(w, request("GET", "/api/sessions/detail/a", "", "user"))
	if w.Code != 200 {
		t.Fatalf("detail HTTP %d: %s", w.Code, w.Body.String())
	}
	var detail SessionDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Requests) != 1 || detail.Requests[0].RequestID != "new-a" {
		t.Fatalf("new request missing: %+v", detail)
	}
	for _, key := range []string{"missing", "b"} {
		w = httptest.NewRecorder()
		h.HandleSessionManagementDetail(w, request("GET", "/api/sessions/detail/"+key, "", "user"))
		if w.Code != 404 {
			t.Fatalf("missing/cross-tenant detail HTTP %d", w.Code)
		}
	}
	w = httptest.NewRecorder()
	h.HandleSessionManagementUpdate(w, request("PATCH", "/api/sessions/update/a", `{"status":"closed"}`, "user"))
	if w.Code != 200 {
		t.Fatalf("update HTTP %d: %s", w.Code, w.Body.String())
	}
	var state string
	if err := pool.QueryRow(ctx, "SELECT session_status FROM session_summaries WHERE session_key='a'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "closed" {
		t.Fatal("PATCH did not persist")
	}
	w = httptest.NewRecorder()
	h.HandleSessionManagementUpdate(w, request("PATCH", "/api/sessions/update/b", `{"status":"closed"}`, "user"))
	if w.Code != 404 {
		t.Fatalf("cross-tenant update HTTP %d", w.Code)
	}
	for _, tc := range []struct {
		method  string
		handler http.HandlerFunc
	}{
		{"POST", h.HandleSessionManagementList}, {"POST", h.HandleSessionManagementDetail}, {"GET", h.HandleSessionManagementUpdate},
	} {
		w = httptest.NewRecorder()
		tc.handler(w, request(tc.method, "/api/sessions/update/a", `{"status":"active"}`, "super_admin"))
		if w.Code != 405 {
			t.Fatalf("wrong-method HTTP %d", w.Code)
		}
	}
	// A broken optional aggregate must fail the endpoint, not silently become zero.
	if _, err := pool.Exec(ctx, "ALTER TABLE session_summaries DROP COLUMN health_score"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.HandleSessionInspectorStats(w, SetAuthContext(httptest.NewRequest("GET", "/", nil), &AuthContext{Role: "tenant_admin", TenantID: "acme"}))
	if w.Code != 500 {
		t.Fatalf("aggregate failure hidden as HTTP %d", w.Code)
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE session_summaries ADD COLUMN health_score numeric; ALTER TABLE session_dim DROP COLUMN closed_at"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.HandleSessionInspectorStats(w, SetAuthContext(httptest.NewRequest("GET", "/", nil), &AuthContext{Role: "tenant_admin", TenantID: "acme"}))
	if w.Code != 500 {
		t.Fatalf("recycle query failure hidden as HTTP %d", w.Code)
	}
}

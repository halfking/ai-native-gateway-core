package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/pkg/identity"
)

// This fixture reuses two existing local databases. It copies their table
// shapes without data, sequences or foreign keys, and writes only its random
// schemas. Production HTTP queries run as a SELECT-only, non-bypass role.
type canonicalFixture struct {
	admin, shadowAdmin                     *pgxpool.Pool
	pool                                   *pgxpool.Pool
	schema, role                           string
	sessionA, sessionB, sessionOtherTenant string
	canonicalA, shadowA                    string
	requests                               int
}

// canonicalFixtureConfig validates the local fixture connection before any SQL.
func canonicalFixtureConfig(dsn string) (*pgxpool.Config, *url.URL, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, nil, errors.New("fixture connection must be loopback")
	}
	// Both pgx and lib/pq accept parameters that can override the URL's
	// destination. Keep only transport options, before parsing either driver.
	for key := range u.Query() {
		switch key {
		case "sslmode", "connect_timeout", "application_name":
		default:
			return nil, nil, errors.New("fixture connection overrides forbidden")
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, nil, errors.New("invalid fixture configuration")
	}
	loopback := func(host string) (string, bool) {
		if host == "localhost" {
			host = "127.0.0.1"
		}
		ip := net.ParseIP(host)
		return host, ip != nil && ip.IsLoopback()
	}
	if host, ok := loopback(cfg.ConnConfig.Host); ok {
		cfg.ConnConfig.Host = host
	} else {
		return nil, nil, errors.New("effective fixture host must be loopback")
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback == nil {
			return nil, nil, errors.New("invalid fixture fallback")
		}
		if host, ok := loopback(fallback.Host); ok {
			fallback.Host = host
		} else {
			return nil, nil, errors.New("fixture fallback must be loopback")
		}
	}
	// The shadow driver receives the same validated destination and account,
	// rather than reinterpreting the original URL or environment defaults.
	u.Host = net.JoinHostPort(cfg.ConnConfig.Host, strconv.Itoa(int(cfg.ConnConfig.Port)))
	u.Path = "/" + cfg.ConnConfig.Database
	u.User = url.UserPassword(cfg.ConnConfig.User, cfg.ConnConfig.Password)
	cfg.MaxConns = 2
	return cfg, u, nil
}

func newCanonicalFixture(t *testing.T) *canonicalFixture {
	t.Helper()
	dsn := os.Getenv("GATEWAY_CANONICAL_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an existing local Gateway/identity_shadow database")
	}
	if os.Getenv("GATEWAY_CANONICAL_TEST_LOCAL_ONLY") != "yes" {
		t.Fatal("real fixture requires explicit loopback-only authorization")
	}
	cfg, u, err := canonicalFixtureConfig(dsn)
	if err != nil {
		t.Fatal("invalid local database configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := &canonicalFixture{schema: "goal_auth_" + strings.ReplaceAll(uuid.NewString(), "-", ""), sessionA: uuid.NewString(), sessionB: uuid.NewString(), sessionOtherTenant: uuid.NewString(), canonicalA: uuid.NewString(), shadowA: uuid.NewString()}
	f.role = f.schema
	f.admin, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("local Gateway connection failed")
	}
	t.Cleanup(f.admin.Close)
	if err := f.admin.Ping(ctx); err != nil {
		t.Fatal("local Gateway ping failed")
	}
	shadowCfg := cfg.Copy()
	shadowCfg.ConnConfig.Database = "identity_shadow"
	f.shadowAdmin, err = pgxpool.NewWithConfig(ctx, shadowCfg)
	if err != nil {
		t.Fatal("existing shadow connection failed")
	}
	t.Cleanup(f.shadowAdmin.Close)
	if err := f.shadowAdmin.Ping(ctx); err != nil {
		t.Fatal("existing shadow ping failed")
	}
	name := pgx.Identifier{f.schema}.Sanitize()
	f.exec(t, f.admin, "CREATE ROLE "+name+" NOLOGIN NOSUPERUSER NOBYPASSRLS")
	t.Cleanup(func() {
		// Cleanup never discovers or drops someone else's objects.
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		verified := true
		for _, p := range []*pgxpool.Pool{f.admin, f.shadowAdmin} {
			if _, err := p.Exec(cleanup, "DROP SCHEMA IF EXISTS "+name+" CASCADE"); err != nil {
				verified = false
				t.Error("owned schema cleanup failed")
			}
			var n int
			if err := p.QueryRow(cleanup, "SELECT count(*) FROM pg_namespace WHERE nspname=$1", f.schema).Scan(&n); err != nil || n != 0 {
				verified = false
				t.Error("owned schema cleanup not verified")
			}
		}
		if _, err := f.admin.Exec(cleanup, "DROP ROLE "+name); err != nil {
			verified = false
			t.Error("owned role cleanup failed")
		}
		var n int
		if err := f.admin.QueryRow(cleanup, "SELECT count(*) FROM pg_roles WHERE rolname=$1", f.role).Scan(&n); err != nil || n != 0 {
			verified = false
			t.Error("owned role cleanup not verified")
		}
		if verified {
			t.Log("fixture_cleanup: schemas=0 roles=0; existing public data untouched")
		} else {
			t.Log("fixture_cleanup: failed_or_unverified; see test failures")
		}
	})
	for _, p := range []*pgxpool.Pool{f.admin, f.shadowAdmin} {
		f.exec(t, p, "CREATE SCHEMA "+name)
		f.exec(t, p, "GRANT USAGE ON SCHEMA "+name+" TO "+name)
	}
	for _, table := range []string{"users", "session_dim", "session_summaries", "prompt_injection_detections", "output_compliance_audit", "request_logs_with_current_month"} {
		f.exec(t, f.admin, "CREATE TABLE "+name+"."+pgx.Identifier{table}.Sanitize()+" (LIKE public."+pgx.Identifier{table}.Sanitize()+")")
		f.exec(t, f.admin, "ALTER TABLE "+name+"."+pgx.Identifier{table}.Sanitize()+" ENABLE ROW LEVEL SECURITY")
		f.exec(t, f.admin, "ALTER TABLE "+name+"."+pgx.Identifier{table}.Sanitize()+" FORCE ROW LEVEL SECURITY")
		f.exec(t, f.admin, "CREATE POLICY tenant_scope ON "+name+"."+pgx.Identifier{table}.Sanitize()+" USING (tenant_id = current_setting('app.current_tenant',true) OR current_setting('app.bypass_rls',true) = 'true')")
	}
	// Defaults are local expressions, never public sequences.
	for _, table := range []string{"shadow_users", "shadow_user_providers"} {
		f.exec(t, f.shadowAdmin, "CREATE TABLE "+name+"."+pgx.Identifier{table}.Sanitize()+" (LIKE public."+pgx.Identifier{table}.Sanitize()+" INCLUDING DEFAULTS)")
	}
	for _, p := range []*pgxpool.Pool{f.admin, f.shadowAdmin} {
		f.exec(t, p, "GRANT SELECT ON ALL TABLES IN SCHEMA "+name+" TO "+name)
	}
	// Fill NOT NULL columns with type-appropriate constants, using only
	// information_schema metadata. Explicit business values override these.
	for _, table := range []string{"users", "session_dim", "session_summaries"} {
		rows, err := f.admin.Query(ctx, `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 AND is_nullable='NO'`, f.schema, table)
		if err != nil {
			t.Fatal("fixture metadata failed")
		}
		var statements []string
		for rows.Next() {
			var col, typ string
			if err := rows.Scan(&col, &typ); err != nil {
				t.Fatal("fixture metadata scan failed")
			}
			def := "''"
			switch typ {
			case "integer", "bigint", "numeric", "double precision":
				def = "0"
			case "boolean":
				def = "false"
			case "timestamp with time zone", "timestamp without time zone":
				def = "now()"
			case "ARRAY":
				def = "'{}'"
			case "jsonb", "json":
				def = "'{}'"
			}
			statements = append(statements, "ALTER TABLE "+name+"."+pgx.Identifier{table}.Sanitize()+" ALTER COLUMN "+pgx.Identifier{col}.Sanitize()+" SET DEFAULT "+def)
		}
		if err := rows.Err(); err != nil {
			t.Fatal("fixture metadata rows failed")
		}
		rows.Close()
		for _, q := range statements {
			f.exec(t, f.admin, q)
		}
	}
	f.exec(t, f.admin, "INSERT INTO "+name+`.users (id,tenant_id,username,role,enabled) VALUES (101,'tenant-a','gateway-alice','user',true),(202,'tenant-a','gateway-bob','user',true),(303,'tenant-b','gateway-other','user',true)`)
	for _, s := range []struct{ id, tenant, owner string }{{f.sessionA, "tenant-a", "gateway-alice"}, {f.sessionB, "tenant-a", "gateway-bob"}, {f.sessionOtherTenant, "tenant-b", "gateway-other"}} {
		f.exec(t, f.admin, "INSERT INTO "+name+`.session_dim (gw_session_id,session_key,tenant_id,owner_user) VALUES ($1,$1,$2,$3)`, s.id, s.tenant, s.owner)
		f.exec(t, f.admin, "INSERT INTO "+name+`.session_summaries (session_key,tenant_id,duration_seconds,request_count,success_count,error_count,total_prompt_tokens,total_completion_tokens,total_tokens,total_cost_usd,input_cost_usd,output_cost_usd,avg_latency_ms,model_switch_count,compliance_status,compliance_issues_count,prompt_injection_detected,pii_detected,toxic_output_detected,created_at,updated_at) VALUES ($1,$2,0,1,1,0,11,7,18,0.125,0.08,0.045,20,0,'compliant',0,false,false,false,now(),now())`, s.id, s.tenant)
		f.exec(t, f.admin, "INSERT INTO "+name+`.request_logs_with_current_month (gw_session_id,tenant_id,request_id,ts,success,client_model,outbound_model,prompt_tokens,completion_tokens,cost_usd,latency_ms) VALUES ($1,$2,$1,now(),true,'fixture-client','fixture-model',11,7,0.125,20)`, s.id, s.tenant)
	}
	f.exec(t, f.shadowAdmin, "INSERT INTO "+name+`.shadow_users(shadow_user_id,canonical_user_id,status,display_name,primary_email) VALUES ($1,$2,'active','','')`, f.shadowA, f.canonicalA)
	for _, p := range []struct{ provider, subject string }{{"acc", "acc-alice"}, {"pocket", "pocket-alice"}, {"llm-gateway", "101"}} {
		f.exec(t, f.shadowAdmin, "INSERT INTO "+name+`.shadow_user_providers(provider,subject,tenant_id,shadow_user_id) VALUES ($1,$2,'tenant-a',$3)`, p.provider, p.subject, f.shadowA)
	}
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = f.schema + ",pg_catalog"
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, err := c.Exec(ctx, "SET ROLE "+name); return err }
	f.pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("restricted Gateway connection failed")
	}
	t.Cleanup(f.pool.Close)
	var super, bypass bool
	if err := f.pool.QueryRow(ctx, `SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&super, &bypass); err != nil || super || bypass {
		t.Fatal("HTTP pool must not bypass RLS")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 0 {
		t.Fatal("RLS must deny unscoped reads")
	}
	u.Path = "/identity_shadow"
	q := u.Query()
	q.Set("options", "-c search_path="+f.schema+",pg_catalog -c role="+f.role)
	u.RawQuery = q.Encode()
	t.Setenv(identity.EnvShadowDSN, u.String())
	shadowDB := identity.ShadowDatabase()
	if shadowDB == nil {
		t.Fatal("shadow store did not initialize")
	}
	t.Cleanup(func() { _ = shadowDB.Close() })
	var shadowRole string
	if err := shadowDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&shadowRole); err != nil || shadowRole != f.role {
		t.Fatal("shadow lookup must use restricted role")
	}
	t.Log("fixture_scope: existing Gateway and identity_shadow; random schemas; SELECT-only NOSUPERUSER NOBYPASSRLS; Gateway FORCE RLS")
	return f
}

func (f *canonicalFixture) exec(t *testing.T, p *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := p.Exec(ctx, q, args...); err != nil {
		t.Fatalf("owned fixture SQL failed: %v", err)
	}
}

func (f *canonicalFixture) request(t *testing.T, raw, session, transport string) (int, AnalyticsSessionDetail) {
	t.Helper()
	f.requests++
	h := &Handler{db: f.pool}
	srv := httptest.NewServer(AdminMiddleware(h.RouteSessionAnalytics, f.pool, legacySecret))
	defer srv.Close()
	srv.Client().Timeout = 10 * time.Second
	path := srv.URL + "/api/admin/session-analytics/" + session
	if transport == "query" {
		path += "?token=" + url.QueryEscape(raw)
	}
	r, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal("HTTP request failed")
	}
	if transport == "cookie" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: raw})
	} else if transport != "query" {
		r.Header.Set("Authorization", "Bearer "+raw)
	}
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal("HTTP transport failed")
	}
	defer resp.Body.Close()
	var detail AnalyticsSessionDetail
	if resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
			t.Fatal("real session DTO decode failed")
		}
	} else {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.Logf("HTTP denied status=%d body=%s", resp.StatusCode, b)
	}
	return resp.StatusCode, detail
}

func TestCanonicalSharedSessionRealDB(t *testing.T) {
	withSharedSecret(t)
	t.Setenv("LLM_GATEWAY_JWT_SECRET", legacySecret)
	f := newCanonicalFixture(t)
	mint := func(issuer, subject, tenant, role string) string {
		return mintMultiIssuerForTest(t, issuer, subject, identity.DefaultAudience, sharedSecret, time.Hour, jwt.MapClaims{"user_id": "202", "username": "gateway-bob", "tenant_id": tenant, "roles": []string{role}})
	}
	t.Run("canonical owner ignores colliding foreign ID and username", func(t *testing.T) {
		status, detail := f.request(t, mint("acc", "acc-alice", "tenant-a", "user"), f.sessionA, "bearer")
		if status != 200 {
			t.Fatalf("linked canonical owner must read actual session detail, got %d", status)
		}
		if detail.Summary.GwSessionID != f.sessionA || detail.Summary.TenantID != "tenant-a" || len(detail.Timeline) != 1 || detail.Timeline[0].RequestID != f.sessionA || detail.Analysis.TokenDistribution.TotalTokens != 18 || detail.Analysis.CostBreakdown.TotalCost != 0.125 {
			t.Fatal("actual summary/timeline/analysis contract mismatch")
		}
	})
	for _, tc := range []struct {
		name, issuer, subject, tenant, session string
		want                                   int
	}{
		{"same tenant nonowner", "acc", "acc-alice", "tenant-a", f.sessionB, 404},
		{"other tenant resource", "acc", "acc-alice", "tenant-a", f.sessionOtherTenant, 404},
		{"explicit pocket alias", "pocket", "pocket-alice", "tenant-a", f.sessionA, 200},
		{"same subject wrong issuer", "acc", "pocket-alice", "tenant-a", f.sessionA, 401},
		{"same subject wrong tenant", "acc", "acc-alice", "tenant-b", f.sessionA, 401},
		{"unmapped identity with existing numeric claim", "acc", "unmapped", "tenant-a", f.sessionB, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := f.request(t, mint(tc.issuer, tc.subject, tc.tenant, "user"), tc.session, "bearer")
			if status != tc.want {
				t.Fatalf("wanted %d got %d", tc.want, status)
			}
		})
	}
	for _, transport := range []string{"cookie", "query"} {
		t.Run("mapped transport "+transport, func(t *testing.T) {
			status, _ := f.request(t, mint("acc", "acc-alice", "tenant-a", "user"), f.sessionA, transport)
			if status != 200 {
				t.Fatalf("mapped transport got %d", status)
			}
		})
	}
	name := pgx.Identifier{f.schema}.Sanitize()
	t.Run("fresh identity disable and restore", func(t *testing.T) {
		raw := mint("acc", "acc-alice", "tenant-a", "user")
		f.exec(t, f.shadowAdmin, "UPDATE "+name+`.shadow_users SET status='disabled' WHERE shadow_user_id=$1`, f.shadowA)
		status, _ := f.request(t, raw, f.sessionA, "bearer")
		if status != 401 {
			t.Errorf("disabled identity got %d", status)
		}
		f.exec(t, f.shadowAdmin, "UPDATE "+name+`.shadow_users SET status='active' WHERE shadow_user_id=$1`, f.shadowA)
		status, _ = f.request(t, raw, f.sessionA, "bearer")
		if status != 200 {
			t.Errorf("restored identity got %d", status)
		}
	})
	t.Run("fresh local disable and restore", func(t *testing.T) {
		raw := mint("acc", "acc-alice", "tenant-a", "user")
		f.exec(t, f.admin, "UPDATE "+name+`.users SET enabled=false WHERE id=101`)
		status, _ := f.request(t, raw, f.sessionA, "bearer")
		if status != 401 {
			t.Errorf("disabled account got %d", status)
		}
		f.exec(t, f.admin, "UPDATE "+name+`.users SET enabled=true WHERE id=101`)
		status, _ = f.request(t, raw, f.sessionA, "bearer")
		if status != 200 {
			t.Errorf("restored account got %d", status)
		}
	})
	t.Run("ambiguous Gateway link denies and recovers", func(t *testing.T) {
		raw := mint("acc", "acc-alice", "tenant-a", "user")
		f.exec(t, f.shadowAdmin, "INSERT INTO "+name+`.shadow_user_providers(provider,subject,tenant_id,shadow_user_id) VALUES ('llm-gateway','202','tenant-a',$1)`, f.shadowA)
		status, _ := f.request(t, raw, f.sessionA, "bearer")
		if status != 401 {
			t.Errorf("ambiguous link got %d", status)
		}
		f.exec(t, f.shadowAdmin, "DELETE FROM "+name+`.shadow_user_providers WHERE provider='llm-gateway' AND subject='202'`)
		status, _ = f.request(t, raw, f.sessionA, "bearer")
		if status != 200 {
			t.Errorf("recovered link got %d", status)
		}
	})
	t.Run("target ID is canonical and local", func(t *testing.T) {
		raw := mint("acc", "acc-alice", "tenant-a", "user")
		for _, id := range []string{"000101", "0", "-101", "+101", "101 ", "99999999999999999999999999", "404"} {
			f.exec(t, f.shadowAdmin, "UPDATE "+name+`.shadow_user_providers SET subject=$1 WHERE provider='llm-gateway'`, id)
			status, _ := f.request(t, raw, f.sessionA, "bearer")
			if status != 401 {
				t.Errorf("invalid or absent local target got %d", status)
			}
		}
		f.exec(t, f.shadowAdmin, "UPDATE "+name+`.shadow_user_providers SET subject='101' WHERE provider='llm-gateway'`)
	})
	t.Run("provider unlink denies", func(t *testing.T) {
		f.exec(t, f.shadowAdmin, "DELETE FROM "+name+`.shadow_user_providers WHERE provider='llm-gateway'`)
		status, _ := f.request(t, mint("acc", "acc-alice", "tenant-a", "super_admin"), f.sessionA, "bearer")
		if status != 401 {
			t.Errorf("unlinked admin token got %d", status)
		}
		f.exec(t, f.shadowAdmin, "INSERT INTO "+name+`.shadow_user_providers(provider,subject,tenant_id,shadow_user_id) VALUES ('llm-gateway','101','tenant-a',$1)`, f.shadowA)
	})
	t.Run("role intersection refreshed for the same JWT", func(t *testing.T) {
		roles := []string{"user", "tenant_admin", "super_admin"}
		for i, tokenRole := range roles {
			raw := mint("acc", "acc-alice", "tenant-a", tokenRole)
			for j, localRole := range roles {
				f.exec(t, f.admin, "UPDATE "+name+`.users SET role=$1 WHERE id=101`, localRole)
				t.Run(tokenRole+" and "+localRole, func(t *testing.T) {
					rank := min(i, j)
					for _, target := range []struct {
						id   string
						want int
					}{{f.sessionA, 200}, {f.sessionB, 404}, {f.sessionOtherTenant, 404}} {
						want := target.want
						if rank >= 1 && target.id == f.sessionB {
							want = 200
						}
						if rank == 2 && target.id == f.sessionOtherTenant {
							want = 200
						}
						status, _ := f.request(t, raw, target.id, "bearer")
						if status != want {
							t.Errorf("effective role resource wanted %d got %d", want, status)
						}
					}
				})
			}
		}
		f.exec(t, f.admin, "UPDATE "+name+`.users SET role='user' WHERE id=101`)
	})
	t.Run("invalid local role and mandatory password gate", func(t *testing.T) {
		raw := mint("acc", "acc-alice", "tenant-a", "super_admin")
		f.exec(t, f.admin, "UPDATE "+name+`.users SET role='external_admin' WHERE id=101`)
		status, _ := f.request(t, raw, f.sessionA, "bearer")
		if status != 401 {
			t.Errorf("invalid local role got %d", status)
		}
		f.exec(t, f.admin, "UPDATE "+name+`.users SET role='user',must_change_password=true WHERE id=101`)
		status, _ = f.request(t, raw, f.sessionA, "bearer")
		if status != 403 {
			t.Errorf("mandatory password gate got %d", status)
		}

		f.requests++
		srv := httptest.NewServer(AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewEncoder(w).Encode(GetAuthContext(r)); err != nil {
				t.Error("principal response failed")
			}
		}, f.pool, legacySecret))
		defer srv.Close()
		srv.Client().Timeout = 10 * time.Second
		r, err := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/me", nil)
		if err != nil {
			t.Fatal("principal request failed")
		}
		r.Header.Set("Authorization", "Bearer "+raw)
		resp, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal("principal transport failed")
		}
		defer resp.Body.Close()
		var p AuthContext
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&p) != nil || p.UserID != 101 || p.Username != "gateway-alice" || p.Role != "user" || p.Subject != "acc-alice" || p.Issuer != "acc" || p.CanonicalUserID != f.canonicalA || !p.MustChangePassword {
			t.Fatal("allowed-path principal must contain fresh local account and original identity")
		}
		f.exec(t, f.admin, "UPDATE "+name+`.users SET must_change_password=false WHERE id=101`)
	})
	t.Run("local username and current resource owner", func(t *testing.T) {
		raw := mint("acc", "acc-alice", "tenant-a", "user")
		f.exec(t, f.admin, "UPDATE "+name+`.users SET username='gateway-renamed' WHERE id=101`)
		status, _ := f.request(t, raw, f.sessionA, "bearer")
		if status != 404 {
			t.Errorf("stale owner must deny got %d", status)
		}
		f.exec(t, f.admin, "UPDATE "+name+`.session_dim SET owner_user='gateway-renamed' WHERE gw_session_id=$1`, f.sessionA)
		status, _ = f.request(t, raw, f.sessionA, "bearer")
		if status != 200 {
			t.Errorf("current local owner got %d", status)
		}
		f.exec(t, f.admin, "UPDATE "+name+`.users SET username='gateway-alice' WHERE id=101`)
		f.exec(t, f.admin, "UPDATE "+name+`.session_dim SET owner_user='gateway-alice' WHERE gw_session_id=$1`, f.sessionA)
	})
	t.Run("real compliance rows from both source contracts", func(t *testing.T) {
		f.exec(t, f.admin, "INSERT INTO "+name+`.prompt_injection_detections (id,tenant_id,request_id,session_key,detected_at,detection_score,risk_level,matched_rules_count,action_taken,evidence_text) VALUES (1,'tenant-a','fixture-injection',$1,now(),85,'high',1,'blocked','fixture evidence')`, f.sessionA)
		f.exec(t, f.admin, "INSERT INTO "+name+`.output_compliance_audit (id,tenant_id,request_id,session_key,detected_at,issue_type,severity,evidence,action_taken) VALUES (1,'tenant-a','fixture-output',$1,now(),'pii',5,'{"fixture":true}','redacted')`, f.sessionA)
		status, detail := f.request(t, mint("acc", "acc-alice", "tenant-a", "user"), f.sessionA, "bearer")
		if status != 200 || len(detail.Analysis.ComplianceIssues) != 2 {
			t.Fatal("both real compliance source rows must be returned")
		}
		byID := map[string]ComplianceIssue{}
		for _, issue := range detail.Analysis.ComplianceIssues {
			byID[issue.RequestID] = issue
		}
		if byID["fixture-injection"].IssueType != "prompt_injection" || byID["fixture-injection"].Severity != 8 || byID["fixture-injection"].Description != "fixture evidence" || byID["fixture-output"].IssueType != "pii" || byID["fixture-output"].Severity != 5 {
			t.Fatal("compliance source normalization mismatch")
		}

		// The canonical SQL has integer risk_level; the discovered local schema has text.
		f.exec(t, f.admin, "UPDATE "+name+`.prompt_injection_detections SET risk_level='7'`)
		f.exec(t, f.admin, "ALTER TABLE "+name+`.prompt_injection_detections ALTER COLUMN risk_level TYPE integer USING risk_level::integer`)
		status, detail = f.request(t, mint("acc", "acc-alice", "tenant-a", "user"), f.sessionA, "bearer")
		if status != 200 || len(detail.Analysis.ComplianceIssues) != 2 {
			t.Fatal("integer risk contract failed")
		}
		for _, issue := range detail.Analysis.ComplianceIssues {
			if issue.RequestID == "fixture-injection" && issue.Severity != 7 {
				t.Error("integer risk severity must be preserved")
			}
		}
	})
	t.Run("bounded database lock failure and recovery", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tx, err := f.shadowAdmin.Begin(ctx)
		if err != nil {
			t.Fatal("fixture fault transaction failed")
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, "LOCK TABLE "+name+`.shadow_user_providers IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal("owned lock injection failed")
		}
		start := time.Now()
		raw := mint("acc", "acc-alice", "tenant-a", "user")
		status, _ := f.request(t, raw, f.sessionA, "bearer")
		elapsed := time.Since(start)
		if status != 401 || elapsed > 8*time.Second {
			t.Errorf("bounded lookup should deny within 8s got %d %v", status, elapsed)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal("owned fault rollback failed")
		}
		status, _ = f.request(t, raw, f.sessionA, "bearer")
		if status != 200 {
			t.Errorf("database recovery got %d", status)
		}
		t.Logf("lookup_failure_recovery: denied=%d bounded_duration_ms=%d recovered=200", 401, elapsed.Milliseconds())
	})
	t.Run("shadow store unavailable denies", func(t *testing.T) {
		if err := identity.ShadowDatabase().Close(); err != nil {
			t.Fatal("fixture shadow close failed")
		}
		status, _ := f.request(t, mint("acc", "acc-alice", "tenant-a", "super_admin"), f.sessionA, "bearer")
		if status != 401 {
			t.Errorf("unavailable shadow store got %d", status)
		}
	})
	t.Logf("real_http_requests=%d; resource-handler requests plus one auth-only whitelist/principal projection", f.requests)
}

func TestCanonicalFixtureRejectsConnectionOverrides(t *testing.T) {
	for key, value := range map[string]string{"host": "remote.invalid", "hostaddr": "192.0.2.1", "port": "55432", "dbname": "other", "service": "remote", "options": "-c search_path=public"} {
		t.Run(key, func(t *testing.T) {
			u := url.URL{Scheme: "postgres", Host: "127.0.0.1:5432", Path: "/llm_gateway", User: url.UserPassword("fixture", "fixture-password")}
			q := u.Query()
			q.Set(key, value)
			u.RawQuery = q.Encode()
			if _, _, err := canonicalFixtureConfig(u.String()); err == nil {
				t.Fatal("connection overrides must reject before network or DDL")
			}
		})
	}
}

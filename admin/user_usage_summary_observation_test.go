package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const usageObservationMessage = "user_usage_summary observation"

func usageObservationLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func usageObservationRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		// The legacy writeInternalErr log remains unchanged and is not a new diagnostic.
		if record["msg"] == usageObservationMessage {
			records = append(records, record)
		}
	}
	return records
}

func TestUsageSummaryObservationAttrs(t *testing.T) {
	buf := usageObservationLogger(t)
	pool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:54318/postgres?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close() // No Acquire: this unit test never connects.
	secret := "username=private-user token=sk-private SELECT secret FROM private_table"
	for _, phase := range []string{"acquire", "query", "scan", "rows"} {
		for _, tc := range []struct {
			name        string
			ctxErr, err error
			want        string
		}{
			{"ordinary", nil, errors.New(secret), "none"},
			{"wrapped deadline", nil, fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "deadline_exceeded"},
			{"wrapped cancel", nil, fmt.Errorf("%s: %w", secret, context.Canceled), "canceled"},
			{"context deadline", context.DeadlineExceeded, errors.New(secret), "deadline_exceeded"},
			{"context cancel", context.Canceled, errors.New(secret), "canceled"},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				buf.Reset()
				o := usageSummaryObservation{phase: phase, acquireElapsed: time.Millisecond,
					queryElapsed: 2 * time.Millisecond, rowsElapsed: 3 * time.Millisecond,
					totalElapsed: 6 * time.Millisecond}
				if phase != "acquire" {
					o.backendPID = 123
				}
				slog.Warn(usageObservationMessage, o.attrs(pool.Stat(), tc.ctxErr, tc.err)...)
				records := usageObservationRecords(t, buf)
				if len(records) != 1 {
					t.Fatalf("diagnostic count=%d", len(records))
				}
				r := records[0]
				wantElapsed := map[string]float64{"acquire": 1, "query": 2, "scan": 3, "rows": 3}[phase]
				if r["phase"] != phase || r["ctx_cancel"] != tc.want || r["phase_elapsed_ms"] != wantElapsed || r["total_elapsed_ms"] != float64(6) {
					t.Fatalf("unexpected metadata: %v", r)
				}
				_, hasPID := r["backend_pid"]
				if hasPID != (phase != "acquire") {
					t.Fatalf("PID presence=%v for %s", hasPID, phase)
				}
				allowed := map[string]bool{"time": true, "level": true, "msg": true, "phase": true,
					"phase_elapsed_ms": true, "acquire_elapsed_ms": true, "query_elapsed_ms": true,
					"rows_elapsed_ms": true, "total_elapsed_ms": true, "pool_acquired": true,
					"pool_idle": true, "pool_total": true, "pool_max": true, "ctx_cancel": true, "backend_pid": true}
				for key := range r {
					if !allowed[key] {
						t.Fatalf("non-allowlisted diagnostic key: %s", key)
					}
				}
				for _, sensitive := range []string{secret, "private-user", "sk-private", "private_table", "rawerror", "raw_error"} {
					if strings.Contains(buf.String(), sensitive) {
						t.Fatalf("diagnostic contains sensitive value %q", sensitive)
					}
				}
			})
		}
	}
}

// pgx also auto-closes rows on EOF/Scan errors. Keep an independent syntax
// guard so those automatic closures cannot hide an inverted defer ordering.
func TestUsageSummaryObservationCleanupContract(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "user_usage_stats.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "handleUserUsageSummary" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("handler not found")
	}
	var releases, closes []token.Pos
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		owner, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if owner.Name == "conn" && selector.Sel.Name == "Release" {
			releases = append(releases, call.Pos())
		}
		if owner.Name == "rows" && selector.Sel.Name == "Close" {
			closes = append(closes, call.Pos())
		}
		return true
	})
	if len(releases) != 1 || len(closes) != 1 {
		t.Fatalf("expected exactly one Release/Close; got %d/%d", len(releases), len(closes))
	}
	var releaseDefer, closeDefer token.Pos
	for _, stmt := range body.List {
		if d, ok := stmt.(*ast.DeferStmt); ok {
			if d.Call.Pos() == releases[0] {
				releaseDefer = d.Pos()
			}
			if d.Call.Pos() == closes[0] {
				closeDefer = d.Pos()
			}
		}
	}
	if releaseDefer == 0 || closeDefer == 0 || releaseDefer >= closeDefer {
		t.Fatal("Release must be deferred before rows.Close, not early or twice")
	}
}

// The tracer observes actual vendor pgx rows.Close -> TraceQueryEnd -> Release.
// It stores no SQL, parameters or raw errors. Tests run sequentially with one conn.
type usageObservationTracer struct {
	active        bool
	events        []string
	acquireCtx    context.Context
	queryCtx      context.Context
	pid           uint32
	busyAtRelease bool
	failScan      bool
}

func (tr *usageObservationTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	tr.acquireCtx = ctx
	return ctx
}
func (tr *usageObservationTracer) TraceAcquireEnd(_ context.Context, _ *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	if data.Conn != nil {
		tr.pid = data.Conn.PgConn().PID()
	}
}
func (tr *usageObservationTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT agg.owner") {
		tr.active = true
		tr.queryCtx = ctx
		tr.events = append(tr.events, "query_start")
		if tr.failScan {
			typ, _ := conn.TypeMap().TypeForOID(pgtype.Int8OID)
			conn.TypeMap().RegisterType(&pgtype.Type{Name: typ.Name, OID: typ.OID, Codec: usageObservationFailCodec{typ.Codec}})
		}
	}
	return ctx
}
func (tr *usageObservationTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if tr.active {
		tr.events = append(tr.events, "rows_closed")
	}
}
func (tr *usageObservationTracer) TraceRelease(_ *pgxpool.Pool, data pgxpool.TraceReleaseData) {
	if tr.active {
		tr.events = append(tr.events, "release")
		tr.busyAtRelease = data.Conn.PgConn().IsBusy()
		tr.active = false
	}
}

type usageObservationFailCodec struct{ pgtype.Codec }

func (usageObservationFailCodec) PlanScan(_ *pgtype.Map, _ uint32, _ int16, _ any) pgtype.ScanPlan {
	return usageObservationFailScan{}
}

type usageObservationFailScan struct{}

func (usageObservationFailScan) Scan(_ []byte, _ any) error {
	return errors.New("sensitive scan error token=sk-private")
}

func usageObservationLocalPool(t *testing.T, tr *usageObservationTracer, fixture string) *pgxpool.Pool {
	t.Helper()
	// Deliberately cannot use TEST_DATABASE_URL or any caller-provided remote DSN.
	config, err := pgxpool.ParseConfig("postgres://127.0.0.1:54318/postgres?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.MinConns = 0
	config.ConnConfig.RuntimeParams["search_path"] = "pg_temp"
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.ConnConfig.Tracer = tr
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, fixture)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("explicit local PG fixture unavailable: %v", err)
	}
	*tr = usageObservationTracer{failScan: tr.failScan}
	return pool
}

const usageObservationFixture = `
CREATE TEMP TABLE users(username text, tenant_id text);
CREATE TEMP TABLE api_keys(id bigint, owner_user text, tenant_id text);
CREATE TEMP TABLE usage_facts(api_key_id bigint, tenant_id text, occurred_at timestamptz, total_tokens bigint, credits_charged bigint);
INSERT INTO pg_temp.users VALUES ('private-user','local'), ('other-user','other');
INSERT INTO pg_temp.api_keys VALUES (1,'private-user','local'), (2,'other-user','other'), (3,'not-a-user','local'), (4,'private-user','other');
INSERT INTO pg_temp.usage_facts VALUES (1,'local',now(),10,2), (1,'local',now(),20,3), (2,'other',now(),999,999), (3,'local',now(),999,999), (4,'local',now(),999,999), (1,'local',now()-interval '40 days',999,999);
`

func TestUsageSummaryObservationLocalPG(t *testing.T) {
	if os.Getenv("USAGE_OBSERVATION_LOCAL_PG") != "1" {
		t.Skip("set USAGE_OBSERVATION_LOCAL_PG=1 for conn-local TEMP fixtures on 127.0.0.1:54318")
	}
	for _, tc := range []struct {
		name, phase, fixture string
		status               int
		failScan             bool
		days                 int
	}{
		{"success days=1", "complete", usageObservationFixture, http.StatusOK, false, 1},
		{"success days=30", "complete", usageObservationFixture, http.StatusOK, false, 30},
		{"query error", "query", usageObservationFixture + `DROP TABLE pg_temp.users;`, http.StatusInternalServerError, false, 30},
		{"scan error", "scan", usageObservationFixture, http.StatusInternalServerError, true, 30},
		{"rows error", "rows", usageObservationFixture + `UPDATE pg_temp.usage_facts SET total_tokens=9223372036854775807 WHERE api_key_id=1;`, http.StatusInternalServerError, false, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := usageObservationLogger(t)
			tr := &usageObservationTracer{failScan: tc.failScan}
			pool := usageObservationLocalPool(t, tr, tc.fixture)
			buf.Reset()
			req := SetAuthContext(httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/admin/users/usage-summary?days=%d", tc.days), nil), &AuthContext{Role: "tenant_admin", TenantID: "local"})
			rec := httptest.NewRecorder()
			(&Handler{db: pool}).handleUserUsageSummary(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			records := usageObservationRecords(t, buf)
			if len(records) != 1 || records[0]["phase"] != tc.phase {
				t.Fatalf("diagnostics=%v", records)
			}
			r := records[0]
			if r["backend_pid"] != float64(tr.pid) || tr.pid == 0 {
				t.Fatalf("PID correlation=%v tracer=%d", r["backend_pid"], tr.pid)
			}
			if r["pool_acquired"] != float64(1) || r["pool_max"] != float64(1) {
				t.Fatalf("in-operation pool stats=%v", r)
			}
			encoded, _ := json.Marshal(r)
			for _, sensitive := range []string{"private-user", "sk-private", "not-a-user", "SELECT", "tenant_id", "rawerror", "sensitive scan error"} {
				if strings.Contains(string(encoded), sensitive) {
					t.Fatalf("new diagnostic leaked %q", sensitive)
				}
			}
			if got := strings.Join(tr.events, ","); got != "query_start,rows_closed,release" || tr.busyAtRelease {
				t.Fatalf("cleanup events=%s busy=%v", got, tr.busyAtRelease)
			}
			if pool.Stat().AcquiredConns() != 0 {
				t.Fatal("connection leaked")
			}
			if tr.acquireCtx != tr.queryCtx {
				t.Fatal("Acquire and Query did not share the total context")
			}
			deadline, ok := tr.queryCtx.Deadline()
			if !ok || time.Until(deadline) > 15*time.Second || time.Until(deadline) < 10*time.Second {
				t.Fatal("15-second total deadline changed")
			}
			if tc.status == http.StatusOK {
				var response userUsageSummaryResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Days != tc.days || len(response.Items) != 1 {
					t.Fatalf("response=%+v", response)
				}
				row := response.Items[0]
				if row.OwnerUser != "private-user" || row.Requests != 2 || row.Tokens != 30 || row.Credits != 5 || row.LastActive == nil {
					t.Fatalf("aggregation/scope changed: %+v", row)
				}
			}
		})
	}

	t.Run("acquire timeout", func(t *testing.T) {
		buf := usageObservationLogger(t)
		tr := &usageObservationTracer{}
		pool := usageObservationLocalPool(t, tr, usageObservationFixture)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		held, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if held != nil {
				held.Release()
			}
		}()
		*tr = usageObservationTracer{}
		buf.Reset()
		requestCtx, requestCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer requestCancel()
		req := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/users/usage-summary?days=1", nil).WithContext(requestCtx), &AuthContext{Role: "super_admin"})
		rec := httptest.NewRecorder()
		(&Handler{db: pool}).handleUserUsageSummary(rec, req)
		records := usageObservationRecords(t, buf)
		if rec.Code != http.StatusInternalServerError || len(records) != 1 || records[0]["phase"] != "acquire" || records[0]["ctx_cancel"] != "deadline_exceeded" {
			t.Fatalf("acquire result: status=%d diagnostics=%v", rec.Code, records)
		}
		if _, ok := records[0]["backend_pid"]; ok {
			t.Fatal("failed Acquire has a PID")
		}
		if tr.pid != 0 || len(tr.events) != 0 {
			t.Fatal("failed Acquire touched a connection")
		}
		if pool.Stat().AcquiredConns() != 1 {
			t.Fatal("failed Acquire changed held connection ownership")
		}
		held.Release()
		held = nil
		if pool.Stat().AcquiredConns() != 0 {
			t.Fatal("connection leaked after holder Release")
		}
	})
}

package db

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Extract the executable QueryRow arguments, not the CREATE VIEW constant or
// comments. Keeping the gates inline avoids changing the production SQL contract.
func routingAnalyticsSourceGateQueries(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "db.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var queries []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ensureRoutingAnalyticsMaterializedViews" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "QueryRow" {
				return true
			}
			if len(call.Args) != 2 {
				t.Fatal("routing gate QueryRow must have context and SQL arguments")
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatal("routing gate SQL is no longer a literal; update executable extraction")
			}
			query, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			queries = append(queries, query)
			return true
		})
	}
	if len(queries) != 3 {
		t.Fatalf("want fast + stale-definition + missing-ukey queries, got %d", len(queries))
	}
	return queries
}

func TestRoutingAnalyticsSourceGateQueries(t *testing.T) {
	queries := routingAnalyticsSourceGateQueries(t)
	for _, part := range []string{"routing_analytics_7d", "routing_analytics_7d_ukey", "NOT EXISTS"} {
		if !strings.Contains(queries[2], part) {
			t.Fatalf("missing-ukey gate query missing %q: %s", part, queries[2])
		}
	}
}

func TestRoutingAnalyticsMVSQLUsesOrdinalGroupBy(t *testing.T) {
	if !strings.Contains(routingAnalyticsMVSQL, "GROUP BY 1, 2, 3, 4, 5, 6, 7") {
		t.Fatal("routingAnalyticsMVSQL must GROUP BY output ordinals so is_auto_request alias cannot shadow the source column")
	}
	// The shadowing form (GROUP BY ... is_auto_request, tenant_id as aliases)
	// is the 23505 deploy-blocker; ordinals are the only accepted shape.
	if strings.Contains(routingAnalyticsMVSQL, "effective_provider_id,\n\t  is_auto_request,\n\t  tenant_id") {
		t.Fatal("routingAnalyticsMVSQL still GROUP BYs is_auto_request by name (alias shadowing)")
	}
}

// This test requires a dedicated scratch database: the gates explicitly inspect
// public catalogs. All fixture DDL is rolled back, and existing routing objects
// cause a failure rather than being dropped. A bad supplied DSN fails, not skips.
//
// R78 轮改造：**「空库」不再是由调用方承担的前提**，门自己从 base DSN 派生
// 一个一次性空库（db/scratch_db_test.go）。改造前把 base DSN 指向一个非空库
// 就会得到 "routing fixture names already exist; supply an empty scratch
// database" 的失败 —— 那是**调用姿势**的问题，不是被测对象的问题，
// 但在「整包跑 db」的读数里它长得和真红一模一样。
func TestRoutingAnalyticsSourceGates_RealDB(t *testing.T) {
	queries := routingAnalyticsSourceGateQueries(t)
	base := os.Getenv("ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL not set — isolated PostgreSQL required")
	}
	// 自己派生一次性空库：base DSN 只当「服务器地址 + 权限」用。
	// 坏 DSN ⇒ 硬失败（下面的 policy 是 scratchFail），不静默跳过。
	dsn := scratchDSNFor(t, base, scratchFail)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec := func(t *testing.T, sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("fixture SQL failed: %v\n%s", err, sql)
		}
	}
	// Protect all relation names used below before creating anything.
	var occupied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = ANY($1::text[])
	)`, []string{"request_logs_hot", "request_logs", "request_logs_with_current_month",
		"routing_analytics_source", "routing_analytics_7d", "routing_audit_summary_7d",
		"routing_analytics_7d_ukey", "routing_audit_summary_7d_ukey", "routing_mv_refresh_state"}).Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied {
		t.Fatal("routing fixture names already exist in the derived scratch database — " +
			"the scratch database must start empty for this gate to mean anything")
	}
	exec(t, `
		CREATE TABLE public.request_logs_hot (id bigint, origin_stage text, auto_profile text);
		CREATE TABLE public.request_logs (LIKE public.request_logs_hot);
		-- ★ R96 形态的 gate marker（'session_turn_details'）需要一张同名表出现在 viewdef 里。
		CREATE TABLE public.session_turn_details (id bigint, origin_stage text, auto_profile text);
		INSERT INTO public.request_logs_hot VALUES (1, 'hot', 'auto');
		INSERT INTO public.request_logs VALUES (2, 'historical', 'auto');
		-- Canonical's historical arm intentionally still depends on legacy logs.
		CREATE VIEW public.request_logs_with_current_month AS
			SELECT * FROM public.request_logs_hot UNION ALL SELECT * FROM public.request_logs;
		CREATE TABLE public.routing_mv_refresh_state (view_name text PRIMARY KEY, refreshed_at timestamptz NOT NULL);
	`)
	for _, tc := range []struct {
		name   string
		source string
		want   bool
	}{
		// Minimal catalog-equivalent projection of migration 837: both modern
		// columns, no MV refreshed_at, proper ukeys/state, but direct legacy FROMs.
		{"legacy_837", "SELECT * FROM public.request_logs_hot UNION ALL SELECT * FROM public.request_logs", false},
		{"canonical", "SELECT * FROM public.request_logs_with_current_month", true},
		// ★ R96 轮：新形态（会话族两腿 + LATERAL 回定位）不再引用
		//   request_logs_with_current_month ⇒ gate 必须**同时接受新旧两形**，
		//   否则新形会被判陈旧 ⇒ 启动期走 CASCADE 重建（minutes 级）。
		{"r96_lateral", "SELECT * FROM public.session_turn_details", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec(t, "CREATE VIEW public.routing_analytics_source AS "+tc.source)
			exec(t, `
				CREATE MATERIALIZED VIEW public.routing_analytics_7d AS SELECT * FROM public.routing_analytics_source;
				CREATE MATERIALIZED VIEW public.routing_audit_summary_7d AS SELECT * FROM public.routing_analytics_source;
				CREATE UNIQUE INDEX routing_analytics_7d_ukey ON public.routing_analytics_7d (id);
				CREATE UNIQUE INDEX routing_audit_summary_7d_ukey ON public.routing_audit_summary_7d (id);
			`)
			var upToDate bool
			if err := tx.QueryRow(ctx, queries[0]).Scan(&upToDate); err != nil {
				t.Fatal(err)
			}
			if upToDate != tc.want {
				t.Errorf("fast gate upToDate = %v, want %v", upToDate, tc.want)
			}
			var existed, stale bool
			if err := tx.QueryRow(ctx, queries[1]).Scan(&existed, &stale); err != nil {
				t.Fatal(err)
			}
			if !existed || stale != !tc.want {
				t.Errorf("definition gate existed=%v stale=%v, want true/%v", existed, stale, !tc.want)
			}
			exec(t, `DROP MATERIALIZED VIEW public.routing_analytics_7d;
				DROP MATERIALIZED VIEW public.routing_audit_summary_7d;
				DROP VIEW public.routing_analytics_source;`)
		})
	}
}

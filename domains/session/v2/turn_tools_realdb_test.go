package v2

// turn_tools_realdb_test.go —— G-6 聚合表达式的**真库**判据。
//
// # 为什么静态门不够
//
// turnToolsJSONBExpr 被**拼进** turn_writer 那条 100+ 列的 INSERT 的
// SELECT 段。表达式一旦有语法错或类型错，那条 INSERT 就整体报错 ⇒
// **每一个轮次行都写不进去**（会话族整条写链报废），而且报错点远离
// 真正的原因（一条几十行的 INSERT 里夹着十几行的子查询）。
//
// ⇒ 这里用**真 PG**把表达式放到**它真正的位置**（INSERT 的值表达式）
// 跑一遍，钉三件事：
//   ① 它**能跑**（语法/类型都对，且在 INSERT 值位置上也对）；
//   ② 它产出的**形状**对（JSON 数组，元素含 5 个摘要字段，按
//      started_at 排序）；
//   ③ 没有工具时落 **'[]'**（列是 NOT NULL，不能是 NULL）。
//
// ★ 为什么**不**把表达式单独 SELECT 出来跑：
//   单独跑时 $2/$3（turn_no / tenant_id）**没有被表达式引用** ⇒
//   PG 无法推断它们的类型（42P18 could not determine data type of
//   parameter $2）。这不是表达式的缺陷，而是「单独跑子查询」与
//   「跑整条 INSERT」的差别。放进 INSERT 后 $2/$3 由目标列定型，
//   测的形态与生产**同形**。
//
// ★ 夹具纪律：只建**最小**的 hot 表夹具（8~9 列），放在一次性
//   database 里，**从不写业务库 public**。
// ★ 用法（opt-in，不设 DSN 就跳过）：
//
//	source /tmp/lgw-env.sh && cd <本 worktree>
//	go test ./domains/session/v2/ -run 'TestG6TurnToolsExpression' -count=1 -v
//
//   DSN 取值顺序 TEST_G6_TOOLS_DSN → TEST_SESSION_V2_DATABASE_URL →
//   TEST_DB_URL → TEST_DATABASE_URL。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestG6TurnToolsExpressionOnRealDB 是 G-6 聚合表达式的真库正题。
func TestG6TurnToolsExpressionOnRealDB(t *testing.T) {
	dsn := g6ToolsDSN(t)
	pool, ctx, cleanup := g6ToolsScratchDB(t, dsn)
	defer cleanup()

	// ── 夹具：最小 session_tools_hot（只含表达式用到的列）+ 最小 hot 轮次表 ──
	// ★ 刻意**不给** arguments / result 两列 —— 表达式若误引用它们，
	//   这里会直接报「column does not exist」，比事后看形状更早暴露。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.session_tools_hot (
		  id bigserial PRIMARY KEY,
		  session_id varchar(64) NOT NULL,
		  request_id varchar(64) NOT NULL,
		  tool_name varchar(128) NOT NULL,
		  tool_call_id varchar(128),
		  status varchar(16) NOT NULL,
		  error_type varchar(32),
		  duration_ms bigint,
		  started_at timestamptz NOT NULL
		);
		CREATE INDEX idx_session_tools_hot_session
		  ON public.session_tools_hot (session_id, started_at DESC);

		CREATE TABLE public.session_turns_hot_fixture (
		  session_id text NOT NULL,
		  turn_no integer NOT NULL,
		  tenant_id varchar(255) NOT NULL,
		  request_id text NOT NULL,
		  tools jsonb NOT NULL DEFAULT '[]'::jsonb
		);
		-- ★ 夹具的列型必须**逐字复刻真库**（真库 information_schema
		--   2026-10-20 实测）：
		--     session_turns_hot.session_id / request_id = **text**
		--     session_tools_hot.session_id / request_id = **varchar(64)**
		--   第一版夹具把轮次表写成了 varchar(64) ⇒ 真库立刻报
		--   42P08 inconsistent types deduced for parameter $1 ——
		--   那个报错不是夹具的错，是它**比真库更严格**：真库目标是 text，
		--   两边都落到 text 才不报。⇒ 夹具必须忠实，否则门测的是
		--   「我编的类型组合」而不是「生产的那一组」。
	`); err != nil {
		t.Fatalf("建夹具失败: %v", err)
	}

	const sess = "sess-g6-tools"
	const req = "req-g6-tools"

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_tools_hot
		  (session_id, request_id, tool_name, tool_call_id, status, error_type, duration_ms, started_at)
		VALUES
		  ($1,$2,'web_search','call-2','error','timeout',1200, now() - interval '2 seconds'),
		  ($1,$2,'read_file', NULL ,'success',NULL, 300, now() - interval '1 second'),
		  ($1,'other-req','noop','call-x','success',NULL, 10, now());
	`, sess, req); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}

	// ── 把表达式放进**真正的 INSERT 值位置**跑一遍 ────────────────────
	raw := g6RunInsert(t, pool, ctx, sess, req)
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("结果不是 JSON 数组（%s）: %v", string(raw), err)
	}

	// ② 形状：2 条（本请求的 2 个工具；'other-req' 的那行不该进来）。
	if len(got) != 2 {
		t.Fatalf("聚合到 %d 个工具（应为 2）：%s", len(got), string(raw))
	}
	// 按 started_at 升序 ⇒ web_search（now()-2s，更早）在前，read_file（now()-1s）在后。
	// ★ 注意：同一条 INSERT 里的 now() 是**事务时间戳**，三行同值 ——
	//   夹具的相对先后只能靠显式的 interval 差制造（这里就是 -2s / -1s）。
	if got[0]["tool_name"] != "web_search" || got[1]["tool_name"] != "read_file" {
		t.Fatalf("工具顺序不对（应按 started_at 升序）: %s", string(raw))
	}
	for _, field := range []string{"tool_name", "tool_call_id", "status", "duration_ms", "error_type"} {
		if _, ok := got[0][field]; !ok {
			t.Fatalf("元素缺摘要字段 %q: %s", field, string(raw))
		}
	}
	if got[1]["tool_call_id"] != "" {
		t.Fatalf("NULL 的 tool_call_id 应落空串（COALESCE），实得 %v", got[1]["tool_call_id"])
	}
	if got[0]["error_type"] != "timeout" {
		t.Fatalf("error_type 没有透出: %s", string(raw))
	}

	// ③ 没有工具时必须是 '[]'（列 NOT NULL）。
	emptyRaw := g6RunInsert(t, pool, ctx, sess, "no-such-request")
	if string(emptyRaw) != "[]" {
		t.Fatalf("没有工具时应落 '[]'（列是 NOT NULL），实得 %s", string(emptyRaw))
	}
}

// g6RunInsert 用「生产同形」的 INSERT 形态跑一次聚合表达式并回读 tools。
//
// $1=session_id / $2=turn_no / $3=tenant_id / $4=request_id —— 与
// turn_writer 的参数表**逐位对齐**（这正是表达式里 $1/$4 的含义）。
func g6RunInsert(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sessionID, requestID string) []byte {
	t.Helper()
	// ★ $1..$4 与 turn_writer 的参数表**逐位对齐**：
	//   $1=session_id / $2=turn_no / $3=tenant_id / $4=request_id
	//   （$2/$3 必须出现在语句里：不被引用的参数 PG 无法定型 ⇒ 42P18。
	//     真实 INSERT 里它们由目标列定型，所以这里也照抄那个形态。）
	const q = `
		INSERT INTO public.session_turns_hot_fixture
		  (session_id, turn_no, tenant_id, request_id, tools)
		VALUES ($1, $2, $3, $4, ` + turnToolsJSONBExpr + `)
		RETURNING tools
	`
	var raw []byte
	if err := pool.QueryRow(ctx, q, sessionID, 1, "tenant-g6", requestID).Scan(&raw); err != nil {
		t.Fatalf("聚合表达式在真库的 INSERT 位置跑不通（这会让每一个轮次写入失败）: %v", err)
	}
	return raw
}

// ── 本门的小工具 ──────────────────────────────────────────────────────

func g6ToolsDSN(t *testing.T) string {
	t.Helper()
	// ★ 刻意**不**读 TEST_SESSION_V2_DATABASE_URL。
	//   sql/schema/integration_gate_test.go 的 dbCredentialNamesInRepo 只扫
	//   `_test.go` 文件里被读到的 DB 变量名，并要求 scripts/audit/
	//   run-integration-gate.sh 对每一个都注入 `=$GATE_URL`。
	//   harness **故意不注入** TEST_SESSION_V2_DATABASE_URL：domains/session/v2
	//   的 setupTestDB 要求目标是 **`*_test` 库**（"refusing mutating V2
	//   database test against database %q"），指向被填满的 gate 库会造成
	//   破坏性写入 ⇒ 注入它是**危险**而不是覆盖。
	//   本门自建隔离库（CREATE DATABASE + 用完 DROP），不需要 V2 的专用库语义
	//   ⇒ 读它既无必要、又会把一个「不该被注入」的名字带进扫描集。
	for _, key := range []string{
		"TEST_G6_TOOLS_DSN",
		"LLM_GATEWAY_DATABASE_URL",
		"DATABASE_URL",
	} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("set TEST_G6_TOOLS_DSN（或 LLM_GATEWAY_DATABASE_URL / DATABASE_URL）才能跑真库门")
	return ""
}

func g6ToolsScratchDB(t *testing.T, dsn string) (*pgxpool.Pool, context.Context, func()) {
	t.Helper()
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	name := fmt.Sprintf("lgw_g6_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		root.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		root.Close()
		t.Fatalf("cannot rewrite dsn %q", dsn)
	}
	rest := dsn[idx+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[q:]
	} else {
		rest = ""
	}
	isoDSN := dsn[:idx+1] + name + rest
	pool, err := pgxpool.New(ctx, isoDSN)
	if err != nil {
		root.Close()
		t.Fatalf("open isolated db pool: %v", err)
	}
	cleanup := func() {
		pool.Close()
		if _, err := root.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		root.Close()
	}
	return pool, ctx, cleanup
}

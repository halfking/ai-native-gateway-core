package promptinjection

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
)

// ★ 这道门存在的理由（2026-10-20 复核轮，
//   docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.6）：
//
//   **检测记录的写侧从来没有成功过。** 旧 INSERT 写的是历史迁移 315 的形状
//   （detection_score / matched_rules / matched_rules_count / detection_layers
//   / risk_level varchar），而**正典表**从未采用该形状：正典 risk_level 是
//   **integer 分数（1..10，带 CHECK 约束）**，且没有那 4 列。315 的
//   `CREATE TABLE IF NOT EXISTS` 在正典库上是**空操作**（表已存在 ⇒ 列从未
//   被创建）⇒ INSERT 恒报 42703，而失败只留一行 slog.Warn ⇒
//   **每一次注入检测的审计记录都被静默丢弃**（检测本身照常生效/拦截，
//   只是没有留痕 —— 于是「这批请求没有注入命中」成了假结论）。
//
//   与同族先例（攻击向量库写侧，a56338ee2）同型同因：写侧绑了 PG 解析不了
//   的线格式，失败被降级成 WARN。
//
//   这道门必须跑在**真库**上：pgxmock 对参数类型不敏感，旧 bug 在 mock 下
//   **照样全绿**（它只认「有一条 INSERT 发生过」）。真正能抓住它的，是让
//   PG 自己解析 $13 的实际线格式（枚举数组）。
//
//   夹具纪律：所有写入都在一个事务里，结尾 ROLLBACK ⇒ 不污染真库。

// insertDetectionSQLOldShape 是**旧写法**（迁移 315 形状）的线格式。
// 留在这里当**变异见证**：把新写法换回旧形状，本门必须变红。
const insertDetectionSQLOldShape = `INSERT INTO prompt_injection_detections (
	tenant_id, request_id, session_key, detection_score, risk_level,
	matched_rules, matched_rules_count, detection_layers,
	action_taken, blocked, evidence_text, input_hash,
	client_ip, user_agent, categories,
	llm_confidence, llm_reason, canary_token_leaked,
	approval_id, replaced_content
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`

// TestInsertDetectionSQL_LandsInCanonicalShape 跑真库，钉住三件事：
//  1. 检测记录**真的落库**（写侧不再静默失效）；
//  2. 落的是**正典形状**：risk_level = 分数（integer），不是字符串等级；
//     等级字符串由读侧按同一套阈值派生（见 admin 包的同源门）；
//  3. 非法类别被**丢弃**而不是让整条 INSERT 失败（规则的 category 是
//     自由文本，直接塞进枚举数组会整条写不进去）。
func TestInsertDetectionSQL_LandsInCanonicalShape(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the detection write path works")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	defer conn.Close(ctx)

	// 表可能还没建（没跑过 364 的库）—— 那就没有可验证的对象，跳过。
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT to_regclass('public.prompt_injection_detections') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if !exists {
		t.Skip("prompt_injection_detections missing in TEST_PG_URL — nothing to verify against")
	}

	// 正典形状的前提：risk_level 是 integer，且**没有** 315 的三列。
	// 若这三条前提不成立，下面那条 INSERT 的语义就不是「正典形状」，
	// 本门也就测不到它声称要测的东西。
	var riskLevelType string
	if err := conn.QueryRow(ctx,
		`SELECT data_type FROM information_schema.columns
		  WHERE table_schema='public' AND table_name='prompt_injection_detections'
		    AND column_name='risk_level'`).Scan(&riskLevelType); err != nil {
		t.Fatalf("probe risk_level type: %v", err)
	}
	if riskLevelType != "integer" {
		t.Fatalf("risk_level type = %q, want integer —— 正典形状前提不成立，本门不构成证据", riskLevelType)
	}
	for _, gone := range []string{"detection_score", "matched_rules", "matched_rules_count"} {
		var n int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns
			  WHERE table_schema='public' AND table_name='prompt_injection_detections'
			    AND column_name=$1`, gone).Scan(&n); err != nil {
			t.Fatalf("probe column %s: %v", gone, err)
		}
		if n != 0 {
			t.Fatalf("column %s exists —— 正典形状前提不成立（315 的形状被搬回来了？）", gone)
		}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // 结尾回滚 ⇒ 不留测试数据

	// RLS：检测表有 tenant 策略，测试租户要显式放行。
	if _, err := tx.Exec(ctx, `SET LOCAL app.bypass_rls = 'true'`); err != nil {
		t.Fatalf("set bypass_rls: %v", err)
	}

	const tenant = "audit-detection-shape-test"
	// 与写侧**完全一致**的绑定：categories 走 pq.Array（text[]），
	// 不是 json.Marshal 的字节（后者 ⇒ PG malformed array literal，22P02）。
	cats := []string{"role_hijack", "not_a_real_enum_label"}
	if _, err := tx.Exec(ctx, insertDetectionSQL,
		tenant, "req-audit-1", "sess-audit-1", 9,
		"audit-rule", "role_hijack",
		"log", false, "evidence", "hash",
		"127.0.0.1", "ua", pq.Array(cats),
		nil, "", nil, nil, ""); err != nil {
		t.Fatalf("insert detection (canonical shape): %v", err)
	}

	var gotScore int
	var gotRule, gotCategory string
	var gotCats []string
	err = tx.QueryRow(ctx,
		`SELECT risk_level, rule_name, category, categories::text[]
		   FROM prompt_injection_detections
		  WHERE tenant_id = $1 AND request_id = $2`, tenant, "req-audit-1").
		Scan(&gotScore, &gotRule, &gotCategory, &gotCats)
	if err != nil {
		t.Fatalf("read back detection: %v", err)
	}
	if gotScore != 9 {
		t.Errorf("risk_level = %d, want 9（正典 risk_level 存的是分数）", gotScore)
	}
	if gotRule != "audit-rule" {
		t.Errorf("rule_name = %q, want \"audit-rule\"", gotRule)
	}
	if gotCategory != "role_hijack" {
		t.Errorf("category = %q, want \"role_hijack\"", gotCategory)
	}
	if len(gotCats) != 1 || gotCats[0] != "role_hijack" {
		t.Errorf("categories = %v, want [role_hijack]（非法类别应被丢弃，合法类别应落库）", gotCats)
	}
}

// TestInsertDetectionSQL_OldShapeStillFails 是**变异见证**：旧形状（315）
// 必须仍然失败。若哪天 PG 放宽了（不会），或有人把旧形状搬回来，本门会提示
// 「旧形状也能过了」，那时新写法的必要性需要重新评估。
func TestInsertDetectionSQL_OldShapeStillFails(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence about the old shape")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT to_regclass('public.prompt_injection_detections') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if !exists {
		t.Skip("prompt_injection_detections missing in TEST_PG_URL")
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL app.bypass_rls = 'true'`); err != nil {
		t.Fatalf("set bypass_rls: %v", err)
	}

	// 旧形状：detection_score / matched_rules / matched_rules_count /
	// detection_layers 四列在正典表上不存在 ⇒ 必须失败。
	_, err = tx.Exec(ctx, insertDetectionSQLOldShape,
		"audit-detection-shape-test", "req-audit-old", "sess-audit-1", 9, "high",
		[]byte(`[]`), 0, []byte(`[]`),
		"log", false, "evidence", "hash",
		"127.0.0.1", "ua", []byte(`[]`),
		nil, "", nil, nil, "")
	if err == nil {
		t.Fatal("old 315 shape unexpectedly succeeded — 正典表形状变了；请重新评估本轮修法")
	}
}

// TestRiskLevelFromScore_MatchesSQLExpr 钉住 Go 侧与 SQL 侧阈值**同源**。
//
// 写侧落库的分数（正典 risk_level）与读侧显示的等级字符串必须由同一套阈值
// 派生，否则会出现「筛选 high 选出来的行里等级写着 medium」这种自相矛盾。
// 本门在真库上跑 SQL 表达式，与 Go 函数逐档比对（真实数据，不是想象）。
func TestRiskLevelFromScore_MatchesSQLExpr(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that Go and SQL agree")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	defer conn.Close(ctx)

	for _, score := range []int{0, 1, 5, 6, 7, 8, 9, 10, 11, 99} {
		var got string
		if err := conn.QueryRow(ctx,
			`SELECT `+RiskLevelSQLExpr+` FROM (SELECT $1::int AS risk_level) t`,
			score).Scan(&got); err != nil {
			t.Fatalf("eval sql expr for score %d: %v", score, err)
		}
		want := RiskLevelFromScore(score)
		if got != want {
			t.Errorf("score %d: SQL 表达式给出 %q，Go 函数给出 %q —— 两边阈值漂移了", score, got, want)
		}
	}
}

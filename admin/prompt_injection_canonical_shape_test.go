package admin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ★ 这道门存在的理由（2026-10-20 复核轮，
//   docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.6）：
//
//   提示词注入的**两个读端点恒 500**：
//     GET /api/admin/prompt-injection/stats      → 42P01（视图不存在）
//     GET /api/admin/prompt-injection/detections → 42703（列不存在）
//   根因是**正典表形状与历史迁移 315 的形状不是一回事**，而代码查的是 315：
//     · 正典 risk_level 是 integer 分数（1..10，带 CHECK），没有
//       detection_score / matched_rules / matched_rules_count 三列；
//     · 迁移 364 的视图 prompt_injection_stats_enhanced 引用 d.detection_score
//       ⇒ **在任何正典库上都建不出来**；即使建出来，它的等级分桶写的是
//       `(risk_level)::text = 'critical'`，对 integer 列恒不匹配 ⇒ 4 个分桶恒 0。
//
//   这道门**逐字跑生产用的那两条 SQL**（包级常量，不是判据自己抄一份）：
//   PG 自己解析 ⇒ 列/视图不存在时立刻报错。旧代码在 mock 下会全绿。
//
//   夹具纪律：所有写入都在一个事务里，结尾 ROLLBACK ⇒ 不污染真库。

// TestPromptInjectionQueries_RunOnCanonicalShape 跑真库，逐字执行生产 SQL。
func TestPromptInjectionQueries_RunOnCanonicalShape(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that these endpoints work")
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
		t.Skip("prompt_injection_detections missing in TEST_PG_URL — nothing to verify against")
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL app.bypass_rls = 'true'`); err != nil {
		t.Fatalf("set bypass_rls: %v", err)
	}

	const tenant = "audit-pi-read-test"
	if _, err := tx.Exec(ctx, `
		INSERT INTO prompt_injection_detections (
			tenant_id, request_id, session_key, risk_level, rule_name, category,
			action_taken, blocked, evidence_text, input_hash,
			client_ip, user_agent, categories
		) VALUES ($1, 'req-read-1', 'sess-read-1', 9, 'audit-rule', 'role_hijack',
			'log', true, 'evidence', 'hash',
			'127.0.0.1', 'ua', ARRAY['role_hijack']::injection_category[])`,
		tenant); err != nil {
		t.Fatalf("seed canonical row: %v", err)
	}

	// 1) 列表投影：正典形状下必须能跑通，并且把分数与等级**同时**取对。
	//    （分数 9 ⇒ 等级 high：阈值与 domains/promptinjection.RiskLevelFromScore 同源）
	rows, err := tx.Query(ctx,
		promptInjectionDetectionsSelectCols+"tenant_id = $1 ORDER BY detected_at DESC LIMIT 5", tenant)
	if err != nil {
		t.Fatalf("detections select failed: %v", err)
	}
	defer rows.Close()
	rowCount := 0
	for rows.Next() {
		var (
			id, score, matchedCount                                                                        int
			tenantID, reqID, sessKey                                                                       string
			detectedAt                                                                                     *time.Time
			riskLevel, matchedRules, action, evidence, llmReason, canary, approval, replaced, clientIP, ua string
			cats                                                                                           []string
			blocked                                                                                        bool
			llmConf                                                                                        *float64
		)
		if err := rows.Scan(&id, &tenantID, &reqID, &sessKey, &detectedAt,
			&score, &riskLevel, &matchedRules, &matchedCount,
			&action, &blocked, &evidence, &cats, &llmConf, &llmReason,
			&canary, &approval, &replaced, &clientIP, &ua); err != nil {
			t.Fatalf("scan detection row: %v", err)
		}
		rowCount++
		if score != 9 {
			t.Errorf("detection_score = %d, want 9（分数应取正典 risk_level）", score)
		}
		if riskLevel != "high" {
			t.Errorf("risk_level = %q, want \"high\"（9 分 ⇒ high；阈值与写侧同源）", riskLevel)
		}
		if matchedRules != "audit-rule" {
			t.Errorf("matched_rules = %q, want \"audit-rule\"（应取正典 rule_name）", matchedRules)
		}
		if matchedCount != 1 {
			t.Errorf("matched_rules_count = %d, want 1（应取 categories 元素个数）", matchedCount)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate detections: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("detections rowCount = %d, want 1", rowCount)
	}

	// 2) 计数查询：必须是真 COUNT(*)（旧实现用 strings.Replace 拼，
	//    SELECT 文本一改就静默不生效 ⇒ count 查询返回整行集）。
	var total int
	if err := tx.QueryRow(ctx, promptInjectionDetectionsCountFrom+"tenant_id = $1", tenant).Scan(&total); err != nil {
		t.Fatalf("detections count failed: %v", err)
	}
	if total != 1 {
		t.Errorf("count = %d, want 1", total)
	}

	// 3) 统计聚合：必须不依赖那个建不出来的视图。
	var (
		totalDet, blocked, critical, high, medium, low int
		approval, replacedCnt, terminated, canaryLeak  int
		avgScore, maxScore, avgLLM                     float64
		affected                                       int
	)
	if err := tx.QueryRow(ctx, promptInjectionStatsSQL, tenant).Scan(
		&totalDet, &blocked, &critical, &high, &medium, &low,
		&approval, &replacedCnt, &terminated, &canaryLeak,
		&avgScore, &maxScore, &avgLLM, &affected); err != nil {
		t.Fatalf("stats query failed: %v", err)
	}
	if totalDet != 1 {
		t.Errorf("total_detections = %d, want 1", totalDet)
	}
	if blocked != 1 {
		t.Errorf("blocked_count = %d, want 1", blocked)
	}
	if high != 1 {
		t.Errorf("high_count = %d, want 1（9 分 ⇒ high 分桶）", high)
	}
	if critical != 0 || medium != 0 || low != 0 {
		t.Errorf("分桶错误：critical=%d medium=%d low=%d, want 0/0/0", critical, medium, low)
	}
	if avgScore != 9 {
		t.Errorf("avg_score = %v, want 9", avgScore)
	}
	if maxScore != 9 {
		t.Errorf("max_score = %v, want 9", maxScore)
	}
	if affected != 1 {
		t.Errorf("affected_sessions = %d, want 1", affected)
	}
}

// TestPromptInjectionHandler_NoStringReplaceForCount 静态门：计数查询不许再
// 靠 strings.Replace 拼。
//
// 判据纪律：字符串级替换的脆弱性在于「文本一改就静默不生效」，而**失效是
// 静默的** ⇒ 常规单测全绿也测不到 ⇒ 只能静态钉住这个形状。
func TestPromptInjectionHandler_NoStringReplaceForCount(t *testing.T) {
	if strings.Contains(promptInjectionDetectionsCountFrom, "SELECT id,") {
		t.Errorf("计数前缀里出现了列表投影 ⇒ 计数与列表共用一段文本耦合")
	}
	if !strings.HasPrefix(strings.TrimSpace(promptInjectionDetectionsCountFrom), "SELECT COUNT(*)") {
		t.Errorf("计数前缀 = %q，必须是显式 SELECT COUNT(*)", promptInjectionDetectionsCountFrom)
	}
}

// TestPromptInjectionStats_NoDependencyOnMissingView 静态门：统计查询不许再读
// prompt_injection_stats_enhanced（该视图引用正典表没有的 detection_score，
// 在任何正典库上都建不出来 ⇒ 42P01 ⇒ 端点 500）。
func TestPromptInjectionStats_NoDependencyOnMissingView(t *testing.T) {
	if strings.Contains(promptInjectionStatsSQL, "prompt_injection_stats_enhanced") {
		t.Error("统计 SQL 仍读 prompt_injection_stats_enhanced —— 该视图在正典库上建不出来")
	}
	if !strings.Contains(promptInjectionStatsSQL, "FROM prompt_injection_detections") {
		t.Error("统计 SQL 必须直接聚合正典表 prompt_injection_detections")
	}
}

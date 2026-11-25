package admin

// pi_analysis_evidence_realdb_test.go — 取证采集器的真库契约测试（2026-11-26）。
//
// ★ 为什么必须有这个文件（与 domains/promptinjection/error_forensics_realdb_test.go 同款纪律）：
//
//	sqlmock 单测只钉「SQL 长什么样」，钉不住「真实驱动怎么把结果交到 Scan 手里」。
//	2026-11-26 在 211 上实测两处「单测全绿、真库整路降级」：
//	  · 请求账本：投影选了 23 列、Scan 只有 22 个目标 ⇒
//	    `sql: expected 23 destination arguments in Scan, not 22`；
//	  · 错误手册：last_status 是可空列，按 string 接 ⇒ 遇到 NULL 行整路报错。
//	两处都能编译、都能过 sqlmock——模拟行是手写的，列数跟着 Scan 走而不是跟着 SQL 走。
//
// ★ 用法：环境变量给 DSN（TEST_PI_ANALYSIS_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL）；
//
//	**没有 DSN 就跳过**。表与视图造在一次性 schema 里（search_path 指过去 ⇒
//	logsSourceFromSQL() 解析到本测试的替身），列类型对着真实库的 information_schema 抄；
//	跑完 DROP SCHEMA … CASCADE，自清。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

func piEvidenceRealDBDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_PI_ANALYSIS_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	t.Skip("realdb: no DSN provided (TEST_PI_ANALYSIS_DSN / LLM_GATEWAY_DATABASE_URL / DATABASE_URL)")
	return ""
}

func TestRealDBPIEvidenceGathererContract(t *testing.T) {
	dsn := piEvidenceRealDBDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// 单连接 ⇒ `SET search_path` 对后续所有查询生效。
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	schema := fmt.Sprintf("pi_evidence_realdb_%d", time.Now().UnixNano())
	// ★ 清理先于关库（t.Cleanup 后注册先执行）。
	t.Cleanup(func() {
		_, _ = db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	})
	for _, q := range []string{
		`CREATE SCHEMA ` + schema,
		`SET search_path TO ` + schema + `, public`,
		// 候选失败表：类型照真实库抄（ts 可空、error_message / 状态码 / retryable / latency 可空）。
		`CREATE TABLE candidate_failure_logs_hot (
			id bigint, request_id text NOT NULL, ts timestamptz, tenant_id text NOT NULL DEFAULT 'default',
			credential_id integer NOT NULL, provider_id integer NOT NULL, raw_model_name text NOT NULL,
			attempt_index integer NOT NULL DEFAULT 0, error_kind text NOT NULL, error_message text,
			upstream_status_code integer, latency_ms integer, retryable boolean,
			extracted_upstream_status_code integer, session_id text)`,
		// 错误手册：last_status 可空（从未执行过的条目就是 NULL）。
		`CREATE TABLE error_playbook_entries (
			id bigserial PRIMARY KEY, error_type text NOT NULL, title text NOT NULL DEFAULT '',
			root_cause text NOT NULL DEFAULT '',
			plan_kind text NOT NULL CHECK (plan_kind IN ('script', 'api', 'prompt')),
			enabled boolean NOT NULL DEFAULT TRUE, auto_execute boolean NOT NULL DEFAULT FALSE,
			hit_count bigint NOT NULL DEFAULT 0, success_count bigint NOT NULL DEFAULT 0,
			failure_count bigint NOT NULL DEFAULT 0, last_status text, confidence numeric NOT NULL DEFAULT 0)`,
		// 请求账本读面的替身：列名与类型照真实视图的 information_schema 抄（全部可空）。
		`CREATE TABLE rl_standin (
			request_id text, tenant_id text, gw_session_id text, ts timestamptz, request_status text,
			success boolean, error_kind text, failure_stage text, failure_detail_code text,
			upstream_status_code integer, client_model text, outbound_model text, canonical_model text,
			provider_id bigint, credential_id bigint, client_protocol character varying, client_endpoint text,
			agent_name text, latency_ms integer, prompt_tokens integer, completion_tokens integer,
			total_tokens integer, stream_interrupted boolean, routing_attempts jsonb, is_auto_request boolean,
			request_preview text)`,
		`CREATE VIEW request_logs_with_current_month AS SELECT * FROM rl_standin`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q[:40], err)
		}
	}

	now := time.Now().UTC()
	// 目标请求：字段齐全（生产真实形态：15.8 万 prompt tokens、3 次路由尝试）。
	if _, err := db.ExecContext(ctx, `INSERT INTO rl_standin VALUES ($1,'default','sess-1',$2,'','f','output_policy_blocked',
		'upstream','output_policy_blocked',0,'glm-5.3-flash','glm-5.3-flash',NULL,32,62,'openai-responses',NULL,'minimax-code',
		7063,158787,0,158787,'f',$3,'t',$4)`,
		"req-1", now.Add(-30*time.Minute),
		`{"attempts":[{"seq":1,"result":"pending","raw_model":"m","provider_id":32,"credential_id":62,"provider_name":"p","error_message":"boom"},
		              {"seq":2,"result":"failed","provider_id":33},{"seq":3,"result":"ok","provider_id":34}]}`,
		"prefix of the user prompt"); err != nil {
		t.Fatalf("seed target request: %v", err)
	}
	// 同会话的前一条：成功；可空列全空（is_auto_request / routing_attempts 为 NULL）。
	if _, err := db.ExecContext(ctx, `INSERT INTO rl_standin (request_id, tenant_id, gw_session_id, ts, request_status, success, client_model)
		VALUES ('req-0','default','sess-1',$1,'','t','glm-5.3-flash')`, now.Add(-30*time.Minute-5*time.Minute)); err != nil {
		t.Fatalf("seed neighbour request: %v", err)
	}
	// 一条「可空列全空」的请求：不得让任何一路报错。
	if _, err := db.ExecContext(ctx, `INSERT INTO rl_standin (request_id, tenant_id, ts) VALUES ('req-null','default',$1)`, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("seed null-laden request: %v", err)
	}
	// 目标请求的失败记录：两条正常（一条 error_message / 状态码 / retryable 全空）＋ 一条 ts 为 NULL。
	for i, row := range []struct {
		ts      *time.Time
		message *string
		code    *int
		retry   *bool
		latency *int
		extract *int
	}{
		{ts: piEvTimePtr(now.Add(-30 * time.Minute)), message: piEvStrPtr("upstream 502"), code: piEvIntPtr(502), retry: piEvBoolPtr(true), latency: piEvIntPtr(812), extract: piEvIntPtr(502)},
		{ts: piEvTimePtr(now.Add(-30*time.Minute + 3*time.Second))},
		{ts: nil, message: piEvStrPtr("no timestamp")},
	} {
		_ = i
		if _, err := db.ExecContext(ctx, `INSERT INTO candidate_failure_logs_hot
			(request_id, ts, tenant_id, credential_id, provider_id, raw_model_name, attempt_index, error_kind,
			 error_message, upstream_status_code, latency_ms, retryable, extracted_upstream_status_code, session_id)
			VALUES ($1,$2,'default',62,18,'nvidia/x',1,'upstream_down',$3,$4,$5,$6,$7,'sess-1')`,
			"req-1", row.ts, row.message, row.code, row.latency, row.retry, row.extract); err != nil {
			t.Fatalf("seed failure row %d: %v", i, err)
		}
	}
	// 另一条请求的同型失败（进同型错误分布、不进目标请求的上游尝试）。
	if _, err := db.ExecContext(ctx, `INSERT INTO candidate_failure_logs_hot
		(request_id, ts, tenant_id, credential_id, provider_id, raw_model_name, error_kind, error_message)
		VALUES ('req-other',$1,'default',7,18,'nvidia/x','upstream_down','x')`, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed other failure: %v", err)
	}
	// 手册：两条同型条目，其中一条从未执行（last_status 为 NULL）。
	for _, title := range []string{"从未执行", "已执行"} {
		last := any(nil)
		if title == "已执行" {
			last = "success"
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO error_playbook_entries (error_type, title, plan_kind, last_status)
			VALUES ('output_guard_blocked',$1,'prompt',$2)`, title, last); err != nil {
			t.Fatalf("seed playbook %s: %v", title, err)
		}
	}

	g := NewPIEvidenceGatherer(db)
	g.searchLogs = func(q logging.RecentQuery) logging.RecentResult {
		return logging.RecentResult{Lines: []logging.RecentLine{}}
	}
	// ErrorDetails 带 error_kind ⇒ 同型错误分布按它查（优先于请求账本读到的 error_kind）。
	target := promptinjection.EvidenceTarget{
		QueueID: 1, ErrorType: "output_guard_blocked", TenantID: "default",
		SessionID: "sess-1", RequestID: "req-1", ErrorAt: now.Add(-30 * time.Minute),
		ErrorDetails: map[string]any{"error_kind": "upstream_down"},
	}
	b, err := g.Gather(ctx, target)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(b.Degraded) != 0 {
		t.Fatalf("六路都应算出来，不该有降级：%+v", b.Degraded)
	}
	req := b.Request
	if req == nil {
		t.Fatalf("请求账本没有取到目标请求")
	}
	for key, want := range map[string]any{
		"client_model": "glm-5.3-flash", "request_status": "failure", "error_kind": "output_policy_blocked",
		"failure_stage": "upstream", "failure_detail_code": "output_policy_blocked", "client_protocol": "openai-responses",
		"agent_name": "minimax-code", "request_preview": "prefix of the user prompt", "is_auto_request": true,
		"stream_interrupted": false,
		"provider_id":        int64(32), "credential_id": int64(62), "latency_ms": int64(7063),
		"prompt_tokens": int64(158787), "completion_tokens": int64(0), "total_tokens": int64(158787),
		"upstream_status_code": int64(0),
	} {
		if got := req[key]; got != want {
			t.Errorf("请求账本 %s = %#v，应为 %#v", key, got, want)
		}
	}
	if req["routing_attempts"] != 3 {
		t.Errorf("路由尝试次数应为 3，得 %#v", req["routing_attempts"])
	}
	if outcomes, _ := req["routing_outcomes"].([]string); len(outcomes) != 3 {
		t.Errorf("逐次路由摘要应有 3 条，得 %#v", req["routing_outcomes"])
	}
	if n := len(b.Attempts); n != 2 {
		t.Errorf("上游尝试应取到 2 条（ts 为 NULL 的那条不进材料），得 %d：%+v", n, b.Attempts)
	}
	if len(b.Session) != 2 {
		t.Errorf("会话上下文应取到 2 条（前一条 + 目标），得 %d", len(b.Session))
	}
	if len(b.ErrorWindow) != 1 || b.ErrorWindow[0]["count"] != int64(3) {
		t.Errorf("同型错误分布应为 1 组 3 次（ts 为 NULL 的不计），得 %+v", b.ErrorWindow)
	}
	if len(b.Playbook) != 2 {
		t.Fatalf("手册应取到 2 条（含 last_status 为 NULL 的那条），得 %d", len(b.Playbook))
	}
	for _, p := range b.Playbook {
		if _, ok := p["last_status"].(string); !ok {
			t.Errorf("last_status 应统一成字符串（NULL ⇒ 空串），得 %#v", p["last_status"])
		}
	}

	// 可空列全空的请求：请求账本仍要算出来（不是降级），且不编造任何字段。
	b2, err := g.Gather(ctx, promptinjection.EvidenceTarget{
		QueueID: 2, TenantID: "default", RequestID: "req-null", ErrorAt: now.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Gather(null-laden): %v", err)
	}
	if len(b2.Degraded) != 0 || b2.Request == nil {
		t.Fatalf("可空列全空的请求也该算出来：degraded=%+v request=%+v", b2.Degraded, b2.Request)
	}
	if b2.Request["request_status"] != "in_progress" {
		t.Errorf("success 为空且无 error_kind ⇒ 状态应为 in_progress，得 %#v", b2.Request["request_status"])
	}
	for _, key := range []string{"client_model", "provider_id", "latency_ms", "is_auto_request", "stream_interrupted"} {
		if _, present := b2.Request[key]; present {
			t.Errorf("空列不该编造值：%s = %#v", key, b2.Request[key])
		}
	}
	if len(b2.Attempts) != 0 || len(b2.Session) != 0 {
		t.Errorf("没有失败记录 / 会话的请求不该取出材料：%+v %+v", b2.Attempts, b2.Session)
	}
}

// 列缺失（42703 / 42P03）是真实的结构不匹配：不得被当成「表不在场」而静默降级。
// （2026-11-26 真库契约测试首跑时实测：替身表漏列 ⇒ 被宽判据吞成「可选系统缺席」。）
func TestPIRelationMissingClassifierIsNarrow(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf(`pq: relation "error_playbook_entries" does not exist`),
		fmt.Errorf(`ERROR: relation "candidate_failure_logs_hot" does not exist (SQLSTATE 42P01)`),
	} {
		if !isPIRelationMissing(err) {
			t.Errorf("表不存在应判为缺席: %v", err)
		}
	}
	for _, err := range []error{
		fmt.Errorf(`pq: column "confidence" of relation "error_playbook_entries" does not exist`),
		fmt.Errorf(`ERROR: column "error_type" does not exist (SQLSTATE 42703)`),
		errors.New("pq: canceling statement due to statement timeout"),
	} {
		if isPIRelationMissing(err) {
			t.Errorf("列缺失 / 超时不是「表不在场」，不得静默降级: %v", err)
		}
	}
}

func piEvTimePtr(t time.Time) *time.Time { return &t }
func piEvStrPtr(s string) *string        { return &s }
func piEvIntPtr(i int) *int              { return &i }
func piEvBoolPtr(b bool) *bool           { return &b }

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// prompt_injection_timestamp_scan_realdb_test.go — 2026-11-01 生产 500 的真库判据。
//
// 缺陷形态（线上实证，llm-gateway-211-b 2026-11-01 10:04:39）：
//
//	ERROR admin handler internal error op="Failed to get policy"
//	  err="can't scan into dest[31] (col: created_at): cannot scan
//	       timestamptz (OID 1184) in binary format into *string"
//	ERROR admin handler internal error op="Failed to scan rule"
//	  err="can't scan into dest[14] (col: created_at): cannot scan
//	       timestamptz (OID 1184) in binary format into *string"
//
// 根因：pgx v5 prepared statement（QueryExecMode 默认）对 timestamptz 结果列
// 使用 binary 格式，binary 编码无法 scan 进 *string。该 handler 自
// ac8437a33（2026-06-29）起把 5 处查询的 11 个 timestamptz 列（created_at /
// updated_at / last_detection_at / last_called_at / expires_at / last_leaked_at /
// detected_at）投影后 scan 进 string/*string 结构体字段——表里只要有行，
// 端点即 500。真库行自 2026-06-30 起存在（rules 23 行 / policies 1 行），
// 即该管理面自上线起每次访问必 500，潜伏 4 个月直到 2026-11-01 18:04 被
// 首次访问抓到。
//
// 修复＝扫描目标改成 *time.Time（列可空 ⇒ 指针；pgx 的
// TryPointerPointerScanPlan 拆成 *time.Time 交给 TimestamptzCodec ⇒
// NULL 给 nil、非 NULL 给值）＋ 其余可空列投影侧 COALESCE 成 schema DEFAULT。
// JSON 侧仍是**字符串**（RFC3339Nano）⇒ 前端
// web/src/api/promptInjection.ts 的 `created_at: string` 类型零变化。
//
// ★ 为什么不用 `::text` 变通：PG 的 text 输出是
// "2026-07-19 21:08:25.836952+08"——**空格分隔、没有 'T'**，**不是 RFC3339**
// ⇒ 移动端 Date.parse（web-mobile 的 relativeTime）在 Safari/iOS 上返回 NaN
// ⇒ 页面退化成显示原始文本；且可空列 ::text 得到 NULL ⇒ 扫进 Go 的非指针
// string 会报 "cannot scan NULL into *string" ⇒ 又是一条 500。
//
// 判据纪律（照 admin_500_column_contract_realdb_test.go）：
//   - 夹具用 `LIKE public.<真表>` 克隆列定义——列类型由真库决定，
//     不允许夹具凭空把 timestamptz 造宽或造窄；
//   - 「前提仍在场」断言：5 张表的 created_at 在夹具里必须仍是
//     timestamp with time zone（若哪天真表列类型变了，binary-scan 前提
//     不复存在，本测试必须被重读而不是默默变绿）；
//   - 断言真 200 且时间字段非空（不是只看状态码——空壳 200 掩盖过
//     形状错配）；
//   - **NULL 夹具**：本族 5 张表的列**大多没有 NOT NULL**（只有 DEFAULT）
//     ⇒ 把可空列扫进 Go 的非指针标量同样是 500 ⇒ 另铺一行全 NULL 的租户，
//     断言它返回 200 且拿到 schema DEFAULT。
//   - **写路径**：PUT 的载荷**不该**被响应体里的时间戳绑架（客户端把
//     GET 响应原样 PUT 回来是最常见的调用方式）。
//
// 用法：source /tmp/lgw-env.sh 后
//
//	go test ./admin/ -run 'PromptInjectionTimestampScan' -count=1 -v
//
// 环境变量沿用 admin500DSN（TEST_ADMIN_500_DSN 优先，其次
// LLM_GATEWAY_DATABASE_URL / DATABASE_URL）；隔离 schema 建后即删，
// 从不写 public。
func TestPromptInjectionTimestampScanRealDB(t *testing.T) {
	dsn := admin500DSN(t)
	pool, ctx := newIsolatedSchemaPool(t, dsn)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE prompt_injection_policies (LIKE public.prompt_injection_policies INCLUDING DEFAULTS);
		CREATE TABLE prompt_injection_rules (LIKE public.prompt_injection_rules INCLUDING DEFAULTS);
		CREATE TABLE prompt_injection_llm_engines (LIKE public.prompt_injection_llm_engines INCLUDING DEFAULTS);
		CREATE TABLE canary_tokens (LIKE public.canary_tokens INCLUDING DEFAULTS);
		CREATE TABLE injection_attack_vectors (LIKE public.injection_attack_vectors INCLUDING DEFAULTS);
		CREATE TABLE models_canonical (LIKE public.models_canonical INCLUDING DEFAULTS);
		-- LIKE **不复制**唯一约束 ⇒ updatePolicy 的 ON CONFLICT (tenant_id)
		-- 在夹具里会报 42P10 ⇒ 手工补上（与真库 unique_tenant_policy 一致）。
		ALTER TABLE prompt_injection_policies ADD CONSTRAINT unique_tenant_policy UNIQUE (tenant_id);

		INSERT INTO prompt_injection_policies (tenant_id) VALUES ('acme');
		-- ★ 全 NULL 租户：把本族每个可空列都显式写成 NULL。
		--   这行存在的意义是钉住「可空列 ⇒ 不能扫进 Go 的非指针标量」这条
		--   同族缺陷（真库 23 行 rules + 1 行 policies 恰好全非 NULL ⇒
		--   只有夹具能钉住）。
		INSERT INTO prompt_injection_policies (
			tenant_id, enabled, detection_mode, enable_basic_rules, enable_advanced_rules,
			enable_heuristics, enable_ml_model, enable_llm_detection, enable_canary_detection,
			enable_vector_similarity, llm_engine_id, content_replacement_strategy,
			max_input_length, auto_learn_enabled, detection_timeout_ms,
			score_threshold_log, score_threshold_warn, score_threshold_sanitize,
			score_threshold_block, action_on_low_risk, action_on_medium_risk,
			action_on_high_risk, whitelist_patterns, whitelist_users, notify_on_detection,
			notification_webhook, notification_email, total_detections, total_blocks,
			last_detection_at, created_at, updated_at)
		VALUES ('acme-null', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
			NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
			NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);

		INSERT INTO prompt_injection_rules (rule_name, rule_type, category, pattern, severity)
		VALUES ('r1', 'regex', 'injection', 'pattern-one', 5);
		-- description / enabled / case_sensitive 留 NULL：这三列在库里都
		-- **没有 NOT NULL**，而 API 契约是 string/bool ⇒ 扫 NULL 会 500。
		-- 真库 23 行恰好全非 NULL，这族 NULL-scan 缺陷只有夹具能钉住。
		INSERT INTO prompt_injection_rules (rule_name, rule_type, category, pattern, severity)
		VALUES ('r2', 'regex', 'injection', 'pattern-two', 5);
		-- ★ 时间戳留 NULL 的规则：created_at / updated_at 可空 ⇒
		--   扫进 Go 的非指针 string 会报 "cannot scan NULL into *string"。
		INSERT INTO prompt_injection_rules
			(rule_name, rule_type, category, pattern, severity, created_at, updated_at)
		VALUES ('r3', 'regex', 'injection', 'pattern-three', 5, NULL, NULL);

		INSERT INTO prompt_injection_llm_engines (tenant_id, engine_name) VALUES ('acme', 'e1');
		INSERT INTO canary_tokens (token_value, tenant_id) VALUES ('tok-1', 'acme');
		INSERT INTO injection_attack_vectors (attack_text, attack_hash, tenant_id)
		VALUES ('attack text', 'hash-1', 'acme');
	`); err != nil {
		t.Fatalf("seed isolated schema: %v", err)
	}

	// 前提仍在场：5 张表的 created_at 必须是 timestamptz。这是 binary-scan
	// 路径的前提；列类型变了本测试测的就不是原缺陷了。
	var badTypes []string
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND column_name = 'created_at'
		  AND table_name IN ('prompt_injection_policies','prompt_injection_rules',
		      'prompt_injection_llm_engines','canary_tokens','injection_attack_vectors')
		  AND data_type <> 'timestamp with time zone'`)
	if err != nil {
		t.Fatalf("probe premise: %v", err)
	}
	for rows.Next() {
		var tbl string
		if err := rows.Scan(&tbl); err != nil {
			t.Fatalf("scan premise: %v", err)
		}
		badTypes = append(badTypes, tbl)
	}
	rows.Close()
	if len(badTypes) > 0 {
		t.Fatalf("premise broken: created_at is not timestamptz in %v; "+
			"the binary-scan defect this test pins no longer applies — reread it", badTypes)
	}

	h := &PromptInjectionHandler{pool: pool, secret: "test-secret"}

	// getPolicy：走 ErrNoRows 分支测不到 scan 路径，所以上面先插了 acme 行。
	t.Run("policy", func(t *testing.T) {
		req := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/prompt-injection/policy", nil),
			&AuthContext{Role: "super_admin", TenantID: "acme", Username: "probe"})
		w := httptest.NewRecorder()
		h.getPolicy(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s", w.Code, w.Body.String())
		}
		var got struct {
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		// ★ 必须是 RFC3339：PG 的 text 输出（"2026-07-19 21:08:25.836952+08"，
		//   空格分隔、没有 'T'）不是 RFC3339 ⇒ 移动端 Date.parse 失败。
		if _, err := time.Parse(time.RFC3339Nano, got.CreatedAt); err != nil {
			t.Errorf("created_at = %q, want RFC3339: %v", got.CreatedAt, err)
		}
		if _, err := time.Parse(time.RFC3339Nano, got.UpdatedAt); err != nil {
			t.Errorf("updated_at = %q, want RFC3339: %v", got.UpdatedAt, err)
		}
	})

	// ★ 全 NULL 租户：可空列必须落到 schema DEFAULT，且**不得** 500。
	t.Run("policy_all_nullable_columns_null", func(t *testing.T) {
		req := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/prompt-injection/policy", nil),
			&AuthContext{Role: "super_admin", TenantID: "acme-null", Username: "probe"})
		w := httptest.NewRecorder()
		h.getPolicy(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s", w.Code, w.Body.String())
		}
		var got struct {
			Enabled           bool    `json:"enabled"`
			DetectionMode     string  `json:"detection_mode"`
			ScoreThresholdLog int     `json:"score_threshold_log"`
			ActionOnHighRisk  string  `json:"action_on_high_risk"`
			CreatedAt         *string `json:"created_at"`
			LastDetectionAt   *string `json:"last_detection_at"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		if !got.Enabled {
			t.Errorf("enabled 应当回 schema DEFAULT true，实际 %v", got.Enabled)
		}
		if got.DetectionMode != "observe" {
			t.Errorf("detection_mode 应当回 schema DEFAULT 'observe'，实际 %q", got.DetectionMode)
		}
		if got.ScoreThresholdLog != 3 {
			t.Errorf("score_threshold_log 应当回 schema DEFAULT 3，实际 %d", got.ScoreThresholdLog)
		}
		if got.ActionOnHighRisk != "block" {
			t.Errorf("action_on_high_risk 应当回 schema DEFAULT 'block'，实际 %q", got.ActionOnHighRisk)
		}
		// 可空时间戳：如实给 null，不编造 1970。
		if got.CreatedAt != nil {
			t.Errorf("created_at 应当是 null（列是 NULL），实际 %q", *got.CreatedAt)
		}
		if got.LastDetectionAt != nil {
			t.Errorf("last_detection_at 应当是 null（列是 NULL），实际 %q", *got.LastDetectionAt)
		}
	})

	t.Run("rules", func(t *testing.T) {
		req := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/prompt-injection/rules", nil),
			&AuthContext{Role: "super_admin", TenantID: "acme", Username: "probe"})
		w := httptest.NewRecorder()
		h.listRules(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s", w.Code, w.Body.String())
		}
		var got struct {
			Rules []struct {
				RuleName    string  `json:"rule_name"`
				Description string  `json:"description"`
				Enabled     bool    `json:"enabled"`
				CreatedAt   *string `json:"created_at"`
			} `json:"rules"`
			Count int `json:"count"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		if got.Count != len(got.Rules) {
			t.Errorf("count=%d len(rules)=%d 不一致", got.Count, len(got.Rules))
		}
		byName := map[string]int{}
		for i, r := range got.Rules {
			byName[r.RuleName] = i
		}
		// r1：时间戳非 NULL ⇒ 必须是 RFC3339 字符串。
		i, ok := byName["r1"]
		if !ok {
			t.Fatalf("夹具规则 r1 不在列表里：%+v", got.Rules)
		}
		if got.Rules[i].CreatedAt == nil {
			t.Errorf("r1.created_at 应当非空（DEFAULT now()），实际 null")
		} else if _, err := time.Parse(time.RFC3339Nano, *got.Rules[i].CreatedAt); err != nil {
			t.Errorf("r1.created_at = %q, want RFC3339: %v", *got.Rules[i].CreatedAt, err)
		}
		// r2：description / enabled 是 NULL ⇒ 必须回默认值，不能 500。
		j, ok := byName["r2"]
		if !ok {
			t.Fatalf("夹具规则 r2 不在列表里：%+v", got.Rules)
		}
		if got.Rules[j].Description != "" {
			t.Errorf("r2.description 应当回空串（列是 NULL），实际 %q", got.Rules[j].Description)
		}
		if !got.Rules[j].Enabled {
			t.Errorf("r2.enabled 应当回 schema DEFAULT true，实际 %v", got.Rules[j].Enabled)
		}
		// r3：created_at / updated_at 是 NULL ⇒ 必须给 null，不能 500。
		k, ok := byName["r3"]
		if !ok {
			t.Fatalf("夹具规则 r3 不在列表里：%+v", got.Rules)
		}
		if got.Rules[k].CreatedAt != nil {
			t.Errorf("r3.created_at 应当是 null（列是 NULL），实际 %q", *got.Rules[k].CreatedAt)
		}
	})

	t.Run("engines", func(t *testing.T) {
		req := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/prompt-injection/engines", nil),
			&AuthContext{Role: "super_admin", TenantID: "acme", Username: "probe"})
		w := httptest.NewRecorder()
		h.listEngines(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s", w.Code, w.Body.String())
		}
		var got struct {
			Engines []struct {
				CreatedAt *string `json:"created_at"`
				Enabled   bool    `json:"enabled"`
			} `json:"engines"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		if len(got.Engines) != 1 {
			t.Fatalf("engines=%+v, want 1 engine", got.Engines)
		}
		if got.Engines[0].CreatedAt == nil {
			t.Errorf("engine.created_at 应当非空（DEFAULT now()），实际 null")
		} else if _, err := time.Parse(time.RFC3339Nano, *got.Engines[0].CreatedAt); err != nil {
			t.Errorf("engine.created_at = %q, want RFC3339: %v", *got.Engines[0].CreatedAt, err)
		}
		if !got.Engines[0].Enabled {
			t.Errorf("engine.enabled 应当回 schema DEFAULT true，实际 %v", got.Engines[0].Enabled)
		}
	})

	t.Run("canary-tokens", func(t *testing.T) {
		req := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/prompt-injection/canary-tokens", nil),
			&AuthContext{Role: "super_admin", TenantID: "acme", Username: "probe"})
		w := httptest.NewRecorder()
		h.listCanaryTokens(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s", w.Code, w.Body.String())
		}
		var got struct {
			Tokens []struct {
				CreatedAt *string `json:"created_at"`
				ExpiresAt *string `json:"expires_at"`
				TokenType string  `json:"token_type"`
				Active    bool    `json:"active"`
			} `json:"tokens"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		if len(got.Tokens) != 1 {
			t.Fatalf("tokens=%+v, want 1 token", got.Tokens)
		}
		if got.Tokens[0].CreatedAt == nil {
			t.Errorf("token.created_at 应当非空（DEFAULT now()），实际 null")
		} else if _, err := time.Parse(time.RFC3339Nano, *got.Tokens[0].CreatedAt); err != nil {
			t.Errorf("token.created_at = %q, want RFC3339: %v", *got.Tokens[0].CreatedAt, err)
		}
		// 可空列回 schema DEFAULT。
		if got.Tokens[0].TokenType != "uuid" {
			t.Errorf("token.token_type 应当回 schema DEFAULT 'uuid'，实际 %q", got.Tokens[0].TokenType)
		}
		if !got.Tokens[0].Active {
			t.Errorf("token.active 应当回 schema DEFAULT true，实际 %v", got.Tokens[0].Active)
		}
		if got.Tokens[0].ExpiresAt != nil {
			t.Errorf("token.expires_at 应当是 null（列是 NULL），实际 %q", *got.Tokens[0].ExpiresAt)
		}
	})

	t.Run("attack-vectors", func(t *testing.T) {
		req := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/prompt-injection/attack-vectors", nil),
			&AuthContext{Role: "super_admin", TenantID: "acme", Username: "probe"})
		w := httptest.NewRecorder()
		h.handleAttackVectors(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s", w.Code, w.Body.String())
		}
		var got struct {
			Vectors []struct {
				CreatedAt  *string `json:"created_at"`
				DetectedAt *string `json:"detected_at"`
			} `json:"vectors"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		if len(got.Vectors) != 1 {
			t.Fatalf("vectors=%+v, want 1 vector", got.Vectors)
		}
		if got.Vectors[0].CreatedAt == nil {
			t.Errorf("vector.created_at 应当非空（DEFAULT now()），实际 null")
		} else if _, err := time.Parse(time.RFC3339Nano, *got.Vectors[0].CreatedAt); err != nil {
			t.Errorf("vector.created_at = %q, want RFC3339: %v", *got.Vectors[0].CreatedAt, err)
		}
	})

	// ★ 写路径：PUT 的载荷**不该**被响应体里的时间戳绑架。
	//   客户端把 GET 响应原样 PUT 回来是最常见的调用方式 ⇒ 若写载荷复用
	//   响应结构体，created_at/updated_at 会被 JSON 解码，而
	//   time.Time.UnmarshalJSON **只认 RFC3339** ⇒ 非 RFC3339 的时间戳
	//   （比如 PG 的文本格式）会让整包 400。
	t.Run("policy_put_ignores_timestamp_fields", func(t *testing.T) {
		body := `{
			"enabled": true, "detection_mode": "observe",
			"enable_basic_rules": true, "enable_advanced_rules": true,
			"enable_heuristics": true, "enable_ml_model": false,
			"enable_llm_detection": true, "enable_canary_detection": true,
			"enable_vector_similarity": false, "llm_engine_id": null,
			"content_replacement_strategy": "llm_rewrite",
			"max_input_length": 50000, "auto_learn_enabled": false,
			"detection_timeout_ms": 5000,
			"score_threshold_log": 3, "score_threshold_warn": 6,
			"score_threshold_sanitize": 8, "score_threshold_block": 10,
			"action_on_low_risk": "log", "action_on_medium_risk": "warn",
			"action_on_high_risk": "block",
			"whitelist_patterns": [], "whitelist_users": [],
			"notify_on_detection": false, "notification_webhook": "",
			"notification_email": "",
			"created_at": "2026-07-19 21:08:25.836952+08",
			"updated_at": "not-a-timestamp-at-all",
			"last_detection_at": "also-not-a-timestamp"
		}`
		req := SetAuthContext(httptest.NewRequest(http.MethodPut,
			"/api/admin/prompt-injection/policy", strings.NewReader(body)),
			&AuthContext{Role: "super_admin", TenantID: "acme", Username: "probe"})
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.updatePolicy(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s（时间戳是无关字段，不该绑架写路径）", w.Code, w.Body.String())
		}
	})
}

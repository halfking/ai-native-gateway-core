package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func adminCtxWithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, authContextKey{}, &AuthContext{
		UserID:   1,
		TenantID: tenantID,
		Username: "admin",
		Role:     "super_admin",
		IsJWT:    true,
	})
}

func setupOutputComplianceHandler(t *testing.T) *OutputComplianceHandler {
	t.Helper()
	dbURL := testDBURL()
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return NewOutputComplianceHandler(pool)
}

func outputComplianceSetupSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DROP TABLE IF EXISTS output_compliance_custom_keywords`,
		`DROP TABLE IF EXISTS output_compliance_policies`,
		`CREATE TABLE IF NOT EXISTS output_compliance_policies (
			id SERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL UNIQUE,
			policy_name TEXT NOT NULL DEFAULT 'default',
			enabled BOOLEAN DEFAULT true,
			enforcement_mode TEXT DEFAULT 'observe',
			pii_engine TEXT DEFAULT 'regex',
			toxicity_engine TEXT DEFAULT 'keyword',
			llm_engine_id INT,
			check_pii BOOLEAN DEFAULT true,
			check_toxicity BOOLEAN DEFAULT true,
			check_bias BOOLEAN DEFAULT false,
			check_hallucination BOOLEAN DEFAULT false,
			check_secrets BOOLEAN DEFAULT true,
			check_internal_ip BOOLEAN DEFAULT true,
			check_jailbreak_response BOOLEAN DEFAULT false,
			check_instruction_injection_response BOOLEAN DEFAULT false,
			pii_threshold FLOAT DEFAULT 0.7,
			toxicity_threshold FLOAT DEFAULT 0.7,
			bias_threshold FLOAT DEFAULT 0.6,
			hallucination_threshold FLOAT DEFAULT 0.7,
			secrets_threshold FLOAT DEFAULT 0.7,
			internal_ip_threshold FLOAT DEFAULT 0.7,
			action_on_pii TEXT DEFAULT 'redact',
			action_on_toxicity TEXT DEFAULT 'warn',
			action_on_bias TEXT DEFAULT 'log',
			action_on_hallucination TEXT DEFAULT 'log',
			action_on_secrets TEXT DEFAULT 'redact',
			action_on_internal_ip TEXT DEFAULT 'redact',
			action_on_jailbreak_response TEXT DEFAULT 'block',
			action_on_instruction_injection_response TEXT DEFAULT 'block',
			auto_redact BOOLEAN DEFAULT true,
			redact_email BOOLEAN DEFAULT true,
			redact_phone BOOLEAN DEFAULT true,
			redact_id_card BOOLEAN DEFAULT true,
			redact_credit_card BOOLEAN DEFAULT true,
			redact_bank_card BOOLEAN DEFAULT false,
			redact_jwt BOOLEAN DEFAULT true,
			redact_password BOOLEAN DEFAULT true,
			toxic_replacement TEXT DEFAULT '[内容已过滤]',
			block_message TEXT DEFAULT '响应因合规策略被阻断',
			strict_mode BOOLEAN DEFAULT false,
			whitelist_keywords TEXT[] DEFAULT '{}',
			exception_rules JSONB DEFAULT '[]',
			notification_channels JSONB DEFAULT '[]',
			realtime_alert_enabled BOOLEAN DEFAULT false,
			alert_threshold_severity INT DEFAULT 7,
			alert_aggregation_window_minutes INT DEFAULT 5,
			sampling_rate FLOAT DEFAULT 1.0,
			auto_review_queue_enabled BOOLEAN DEFAULT false,
			feedback_loop_enabled BOOLEAN DEFAULT false,
			skill_generation_enabled BOOLEAN DEFAULT false,
			auto_threshold_tuning_enabled BOOLEAN DEFAULT false,
			retention_days INT DEFAULT 90,
			created_by TEXT,
			updated_by TEXT,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW(),
			total_detections INT DEFAULT 0,
			total_blocks INT DEFAULT 0,
			last_detection_at TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS output_compliance_custom_keywords (
			id SERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			keyword TEXT NOT NULL,
			category TEXT,
			severity INT DEFAULT 5,
			action TEXT DEFAULT 'warn',
			enabled BOOLEAN DEFAULT true,
			description TEXT,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("schema setup failed: %v", err)
		}
	}
}

func TestOutputComplianceHandler_GetPolicy_Default(t *testing.T) {
	h := setupOutputComplianceHandler(t)
	pool := h.pool
	outputComplianceSetupSchema(t, pool)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/output-compliance/policy", nil)
	req = req.WithContext(adminCtxWithTenant(req.Context(), "test-tenant-out1"))
	rec := httptest.NewRecorder()
	h.handlePolicy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "observe") {
		t.Errorf("expected default policy; body=%s", body)
	}
}

func TestOutputComplianceHandler_UpdatePolicy_InvalidMode(t *testing.T) {
	h := setupOutputComplianceHandler(t)
	outputComplianceSetupSchema(t, h.pool)

	payload := `{"enforcement_mode":"invalid"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/output-compliance/policy", strings.NewReader(payload))
	req = req.WithContext(adminCtxWithTenant(req.Context(), "test-tenant-out2"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handlePolicy(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestOutputComplianceHandler_Keywords_CRUD(t *testing.T) {
	h := setupOutputComplianceHandler(t)
	outputComplianceSetupSchema(t, h.pool)

	// create
	payload := `{"keyword":"secret-word","category":"toxic","severity":8,"action":"block","enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/output-compliance/keywords", strings.NewReader(payload))
	req = req.WithContext(adminCtxWithTenant(req.Context(), "test-tenant-out3"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleKeywords(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}

	// list
	req = httptest.NewRequest(http.MethodGet, "/api/admin/output-compliance/keywords", nil)
	req = req.WithContext(adminCtxWithTenant(req.Context(), "test-tenant-out3"))
	rec = httptest.NewRecorder()
	h.handleKeywords(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "secret-word") {
		t.Errorf("expected keyword in list; body=%s", body)
	}

	// extract first id
	var id int
	prefix := "\"id\":"
	idx := strings.Index(body, prefix)
	if idx == -1 {
		t.Fatalf("no keyword id found in body: %s", body)
	}
	fmt.Sscanf(body[idx+len(prefix):], "%d", &id)

	// delete
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/admin/output-compliance/keywords/%d", id), nil)
	req = req.WithContext(adminCtxWithTenant(req.Context(), "test-tenant-out3"))
	rec = httptest.NewRecorder()
	h.handleKeywordSubrouter(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
}

package freediscovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// ErrTemplateNotFound 模板不存在 (含 RLS 隔离后不可见的他租户模板)
var ErrTemplateNotFound = errors.New("freediscovery: provider template not found")

// TemplateManager 供应商模板 CRUD. 所有读写均走 RLS:
// 事务内先 SET LOCAL app.current_tenant, 与 075/084 迁移的
// tenant_isolation_* 策略契约一致 (参考 freeresource.QuotaTracker 同款实现).
type TemplateManager struct {
	db      *sql.DB
	keyring *secret.Keyring // nil = 不支持明文密钥加密落库 (仅 env 引用模式)
}

// NewTemplateManager 创建模板管理器. keyring 可为 nil:
// 此时 Create/Update 携带明文 APIKey 会返回错误, 调用方应改用 APIKeyEnv.
func NewTemplateManager(db *sql.DB, keyring *secret.Keyring) *TemplateManager {
	return &TemplateManager{db: db, keyring: keyring}
}

// Create 新增模板. 返回创建后的完整行.
func (m *TemplateManager) Create(ctx context.Context, tenantID string, req *CreateTemplateRequest) (*ProviderTemplate, error) {
	if msg := req.ValidateCreate(); msg != "" {
		return nil, fmt.Errorf("%s", msg)
	}
	if req.APIKey != "" {
		if m.keyring == nil {
			return nil, errors.New("freediscovery: plaintext api_key requires the credential keyring; use api_key_env instead")
		}
	}

	var (
		ciphertext []byte
		err        error
	)
	if req.APIKey != "" {
		env, encErr := secret.EncryptAESGCM([]byte(req.APIKey), m.keyring)
		if encErr != nil {
			return nil, fmt.Errorf("freediscovery: encrypt api key: %w", encErr)
		}
		ciphertext = []byte(env)
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	apiType := req.APIType
	if apiType == "" {
		apiType = APITypeOpenAICompletions
	}
	modelsEndpoint := req.ModelsEndpoint
	if modelsEndpoint == "" {
		modelsEndpoint = "/models"
	}
	tosVerdict := req.TosVerdict
	if tosVerdict == "" {
		tosVerdict = "unknown"
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO provider_templates (
			tenant_id, provider_code, display_name, base_url, api_type,
			api_key_env, api_key_encrypted, models_endpoint, quota_endpoint,
			tos_url, tos_verdict, tos_notes, enabled, created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id`,
		tenantID, req.ProviderCode, req.DisplayName, req.BaseURL, string(apiType),
		nullableStr(req.APIKeyEnv), ciphertext, modelsEndpoint, nullableStr(req.QuotaEndpoint),
		nullableStr(req.TosURL), tosVerdict, nullableStr(req.TosNotes), enabled, nullableStr(req.CreatedBy),
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, fmt.Errorf("freediscovery: template for provider %q already exists in tenant %q", req.ProviderCode, tenantID)
		}
		return nil, fmt.Errorf("freediscovery: insert template: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("freediscovery: commit create: %w", err)
	}

	slog.Info("freediscovery: provider template created",
		"tenant_id", tenantID, "provider_code", req.ProviderCode, "template_id", id)
	return m.Get(ctx, tenantID, id)
}

// Get 按 ID 读取模板. 跨租户 ID 返回 ErrTemplateNotFound (RLS 过滤).
func (m *TemplateManager) Get(ctx context.Context, tenantID string, id int64) (*ProviderTemplate, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	t, err := scanTemplate(tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, provider_code, display_name, base_url, api_type,
		       COALESCE(api_key_env,''), api_key_encrypted, COALESCE(models_endpoint,'/models'),
		       COALESCE(quota_endpoint,''), COALESCE(tos_url,''), tos_verdict, COALESCE(tos_notes,''),
		       enabled, COALESCE(created_by,''), created_at, updated_at
		FROM provider_templates WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("freediscovery: get template %d: %w", id, err)
	}
	return t, nil
}

// List 列出租户内模板. enabledOnly=true 时仅返回启用模板.
func (m *TemplateManager) List(ctx context.Context, tenantID string, enabledOnly bool) ([]*ProviderTemplate, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	q := `
		SELECT id, tenant_id, provider_code, display_name, base_url, api_type,
		       COALESCE(api_key_env,''), api_key_encrypted, COALESCE(models_endpoint,'/models'),
		       COALESCE(quota_endpoint,''), COALESCE(tos_url,''), tos_verdict, COALESCE(tos_notes,''),
		       enabled, COALESCE(created_by,''), created_at, updated_at
		FROM provider_templates`
	if enabledOnly {
		q += ` WHERE enabled = TRUE`
	}
	q += ` ORDER BY provider_code`

	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: list templates: %w", err)
	}
	defer rows.Close()

	var out []*ProviderTemplate
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Update 部分更新模板. 指针字段 nil = 不修改.
func (m *TemplateManager) Update(ctx context.Context, tenantID string, id int64, req *UpdateTemplateRequest) (*ProviderTemplate, error) {
	// 先读现值 (同时校验存在性 + 租户可见性)
	cur, err := m.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}

	var ciphertext []byte
	clearCiphertext := false
	if req.APIKey != nil && *req.APIKey != "" {
		if m.keyring == nil {
			return nil, errors.New("freediscovery: plaintext api_key requires the credential keyring; use api_key_env instead")
		}
		env, encErr := secret.EncryptAESGCM([]byte(*req.APIKey), m.keyring)
		if encErr != nil {
			return nil, fmt.Errorf("freediscovery: encrypt api key: %w", encErr)
		}
		ciphertext = []byte(env)
	}
	if req.APIKey != nil && *req.APIKey == "" {
		// 空串语义: 清除已存密文 (密文优先级高于 env 引用, 不清除则无法切换)
		clearCiphertext = true
	}

	if req.DisplayName != nil {
		cur.DisplayName = *req.DisplayName
	}
	if req.BaseURL != nil {
		if !isValidBaseURL(*req.BaseURL) {
			return nil, errors.New("freediscovery: base_url must start with http:// or https://")
		}
		cur.BaseURL = *req.BaseURL
	}
	if req.APIType != nil {
		switch *req.APIType {
		case APITypeOpenAICompletions, APITypeGoogleGenerativeAI, APITypeAnthropic:
			cur.APIType = *req.APIType
		default:
			return nil, errors.New("freediscovery: invalid api_type")
		}
	}
	if req.APIKeyEnv != nil {
		cur.APIKeyEnv = *req.APIKeyEnv
	}
	if ciphertext != nil {
		cur.APIKeyEncrypted = ciphertext
	}
	if clearCiphertext {
		cur.APIKeyEncrypted = nil
	}
	if req.ModelsEndpoint != nil && *req.ModelsEndpoint != "" {
		cur.ModelsEndpoint = *req.ModelsEndpoint
	}
	if req.QuotaEndpoint != nil {
		cur.QuotaEndpoint = *req.QuotaEndpoint
	}
	if req.TosURL != nil {
		cur.TosURL = *req.TosURL
	}
	if req.TosVerdict != nil {
		if !isValidTosVerdict(*req.TosVerdict) {
			return nil, errors.New("freediscovery: invalid tos_verdict")
		}
		cur.TosVerdict = *req.TosVerdict
	}
	if req.TosNotes != nil {
		cur.TosNotes = *req.TosNotes
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if cur.DisplayName == "" {
		return nil, errors.New("freediscovery: display_name cannot be empty")
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE provider_templates SET
			display_name=$2, base_url=$3, api_type=$4, api_key_env=$5,
			api_key_encrypted=$6, models_endpoint=$7, quota_endpoint=$8,
			tos_url=$9, tos_verdict=$10, tos_notes=$11, enabled=$12
		WHERE id=$1`,
		id, cur.DisplayName, cur.BaseURL, string(cur.APIType), nullableStr(cur.APIKeyEnv),
		cur.APIKeyEncrypted, cur.ModelsEndpoint, nullableStr(cur.QuotaEndpoint),
		nullableStr(cur.TosURL), cur.TosVerdict, nullableStr(cur.TosNotes), cur.Enabled)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: update template %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("freediscovery: commit update: %w", err)
	}

	slog.Info("freediscovery: provider template updated", "tenant_id", tenantID, "template_id", id)
	return m.Get(ctx, tenantID, id)
}

// Delete 删除模板. 关联的历史任务通过 ON DELETE SET NULL 保留 (provider_code 冗余列可追溯).
func (m *TemplateManager) Delete(ctx context.Context, tenantID string, id int64) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM provider_templates WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("freediscovery: delete template %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTemplateNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("freediscovery: commit delete: %w", err)
	}
	slog.Info("freediscovery: provider template deleted", "tenant_id", tenantID, "template_id", id)
	return nil
}

// ResolveAPIKey 解析模板的上游认证密钥:
//  1. api_key_encrypted 密文解密 (keyring 必须可用)
//  2. api_key_env 环境变量引用解析
//
// keyless 模板返回空串. 返回 (key, source, error).
func (m *TemplateManager) ResolveAPIKey(ctx context.Context, t *ProviderTemplate) (string, string, error) {
	_ = ctx
	if t == nil || !t.HasCredential() {
		return "", "none", nil
	}
	if len(t.APIKeyEncrypted) > 0 {
		if m.keyring == nil {
			return "", "", errors.New("freediscovery: template has encrypted key but no keyring configured")
		}
		pt, err := secret.DecryptAESGCM(string(t.APIKeyEncrypted), m.keyring)
		if err != nil {
			return "", "", fmt.Errorf("freediscovery: decrypt template key: %w", err)
		}
		return string(pt), "encrypted", nil
	}
	// env 引用: 支持 "$VAR" 与裸 "VAR" 两种写法 (Orbi 模板用 $VAR)
	ref := t.APIKeyEnv
	ref = strings.TrimPrefix(ref, "$")
	if ref == "" {
		return "", "none", nil
	}
	val := envLookup(ref)
	if val == "" {
		return "", "", fmt.Errorf("freediscovery: env %s referenced by template %d is not set", ref, t.ID)
	}
	return val, "env:" + ref, nil
}

func scanTemplate(row interface {
	Scan(dest ...any) error
}) (*ProviderTemplate, error) {
	var (
		t         ProviderTemplate
		apiType   string
		createdAt sql.NullTime
		updatedAt sql.NullTime
	)
	if err := row.Scan(
		&t.ID, &t.TenantID, &t.ProviderCode, &t.DisplayName, &t.BaseURL, &apiType,
		&t.APIKeyEnv, &t.APIKeyEncrypted, &t.ModelsEndpoint,
		&t.QuotaEndpoint, &t.TosURL, &t.TosVerdict, &t.TosNotes,
		&t.Enabled, &t.CreatedBy, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("freediscovery: scan template: %w", err)
	}
	t.APIType = APIType(apiType)
	if createdAt.Valid {
		t.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		t.UpdatedAt = updatedAt.Time
	}
	return &t, nil
}

// setTenantTx 在事务内设置 RLS 租户 GUC.
// 与 freeresource.QuotaTracker 一致: SET LOCAL + escapeTenant 白名单转义.
func setTenantTx(ctx context.Context, tx *sql.Tx, tenantID string) error {
	if tenantID == "" {
		return nil // 让 RLS fallback 到 'default'
	}
	_, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", escapeTenantID(tenantID)))
	if err != nil {
		return fmt.Errorf("freediscovery: set rls tenant: %w", err)
	}
	return nil
}

// escapeTenantID 仅放行 [A-Za-z0-9_-] 且 <=64 字符, 其余替换为 'default',
// 与 075/084 迁移中 get_current_tenant() 的无引号拼接契约配套.
func escapeTenantID(id string) string {
	if id == "" || len(id) > 64 {
		return "default"
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-') {
			return "default"
		}
	}
	return id
}

// envLookup 独立的 env 读取入口, 测试可注入 (t.Setenv 走真实 os.Getenv).
var envLookup = os.Getenv

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// timeNow 独立的时钟入口, 测试可注入.
var timeNow = time.Now

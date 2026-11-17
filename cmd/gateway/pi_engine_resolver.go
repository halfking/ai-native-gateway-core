package main

// PI 判官（LLM 检测引擎）的上游端点解析器。
//
// ★ 2026-11-18 审计轮 P1：端点解析器此前**从未接线**
//
//	（LLMDetectionEngine.SetEngineResolver 零生产调用方）⇒
//	判官层每次调用都返回「endpoint resolver not wired」⇒
//	**判官层从不产出结论**；又因 llm_detection_fail_closed 默认 false，
//	只落一条 Warn 就继续用规则分放行 ⇒ L6 层形同虚设且无痕。
//
// # 为什么解析器必须放在 gateway 侧
//
// 正典表 prompt_injection_llm_engines 只有 model_canonical_id / credential_id，
// **没有** base_url / model / api_key；而凭据密钥在本仓是**加密存放**的
// （credentials.secret_ciphertext + secret_kid）⇒ 端点解析必须由
// **持有凭据解密能力**的一侧做 ⇒ domains/promptinjection 只定义接缝
// （EngineResolver 接口），实现落在本文件。
//
// # 解析口径（全部对着正典表，不编造）
//
//   · base_url  ← credentials.provider_id → providers.base_url
//   · model     ← provider_models 里该 (provider, canonical) 的
//                 outbound_model_name / raw_model_name（供应商侧真实模型名）；
//                 找不到 ⇒ 退回 models_canonical.canonical_name
//   · api_key   ← credentials.secret_ciphertext 经 keyring/fernet 解密
//
// 任何一环缺失都返回**显式错误**（不静默降级、不编造值）：
// 判官层的降级语义（fail-open / fail-closed）由策略决定，
// 但「为什么不可用」必须可见。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// piEngineResolver 是 promptinjection.EngineResolver 的生产实现。
type piEngineResolver struct {
	db        *sql.DB
	keyring   *secret.Keyring
	fernetKey []byte
}

// newPIEngineResolver 构造判官端点解析器。
//
// db 是 PI 检测器自己的 *sql.DB（与检测器同一条连接池）；
// keyring / fernetKey 是凭据解密材料（与 provider profile / 探测面同源）。
func newPIEngineResolver(db *sql.DB, keyring *secret.Keyring, fernetKey []byte) *piEngineResolver {
	return &piEngineResolver{db: db, keyring: keyring, fernetKey: fernetKey}
}

// ResolveEngineEndpoint 解析判官引擎的上游调用信息。
func (r *piEngineResolver) ResolveEngineEndpoint(
	ctx context.Context, modelCanonicalID, credentialID int64,
) (*promptinjection.EngineEndpoint, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("pi judge endpoint resolver: no database handle")
	}

	var (
		canonicalName string
		baseURL       string
		providerID    int64
		secretCipher  []byte
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT m.canonical_name,
		       COALESCE(pr.base_url, ''),
		       pr.id,
		       c.secret_ciphertext
		FROM models_canonical m
		JOIN credentials c ON c.id = $2
		JOIN providers pr ON pr.id = c.provider_id
		WHERE m.id = $1`,
		modelCanonicalID, credentialID,
	).Scan(&canonicalName, &baseURL, &providerID, &secretCipher)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf(
			"pi judge endpoint: canonical model %d / credential %d not resolvable (no such row in models_canonical/credentials)",
			modelCanonicalID, credentialID)
	}
	if err != nil {
		return nil, fmt.Errorf("pi judge endpoint: query canonical model %d / credential %d: %w",
			modelCanonicalID, credentialID, err)
	}

	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf(
			"pi judge endpoint: credential %d has no provider base_url (provider %d)", credentialID, providerID)
	}

	// 供应商侧模型名（拿不到就退回 canonical_name —— 那是**正典名**，
	// 调用失败会显式报错，不会静默拿错模型冒充成功）。
	model := r.resolveProviderModelName(ctx, providerID, modelCanonicalID, canonicalName)

	apiKey, err := r.decryptAPIKey(secretCipher)
	if err != nil {
		return nil, err
	}
	if apiKey == "" {
		return nil, fmt.Errorf("pi judge endpoint: credential %d has no usable secret (decrypted to empty)", credentialID)
	}

	return &promptinjection.EngineEndpoint{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  apiKey,
	}, nil
}

// resolveProviderModelName 解析该 (provider, canonical) 对应的供应商侧模型名。
//
// 优先 provider_models.outbound_model_name（出站真实模型名），
// 再退 raw_model_name，最后退 canonical_name。
func (r *piEngineResolver) resolveProviderModelName(
	ctx context.Context, providerID, canonicalID int64, canonicalName string,
) string {
	var outbound sql.NullString
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT pm.outbound_model_name, pm.raw_model_name
		FROM provider_models pm
		WHERE pm.provider_id = $1 AND pm.canonical_id = $2
		  AND COALESCE(pm.available, true) = true
		ORDER BY pm.updated_at DESC
		LIMIT 1`,
		providerID, canonicalID,
	).Scan(&outbound, &raw)
	if err != nil {
		// 查不到映射**不是**致命错误：退回 canonical_name，并留痕。
		slog.Warn("pi judge endpoint: provider model mapping lookup failed; falling back to canonical name",
			"error", err, "provider_id", providerID, "canonical_id", canonicalID,
			"canonical_name", canonicalName)
		return canonicalName
	}
	if s := strings.TrimSpace(outbound.String); s != "" {
		return s
	}
	if s := strings.TrimSpace(raw.String); s != "" {
		return s
	}
	return canonicalName
}

// decryptAPIKey 解密凭据密钥。
func (r *piEngineResolver) decryptAPIKey(cipher []byte) (string, error) {
	if len(cipher) == 0 {
		return "", nil
	}
	if r.keyring == nil && len(r.fernetKey) == 0 {
		return "", fmt.Errorf(
			"pi judge endpoint: no credential decryption material available (keyring/fernet key both absent)")
	}
	plaintext, _, err := secret.DecryptAny(string(cipher), r.keyring, r.fernetKey)
	if err != nil {
		return "", fmt.Errorf("pi judge endpoint: decrypt credential secret: %w", err)
	}
	return strings.TrimSpace(string(plaintext)), nil
}

// 编译期断言。
var _ promptinjection.EngineResolver = (*piEngineResolver)(nil)

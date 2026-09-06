package admin

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
)

// local.go — 本地托管模型供应商（kind='local'）的共享辅助逻辑。
//
// 设计契约（2026-09-07，本地模型网关接入）：
//   1. 本地供应商来自 provider_catalog 中 kind='local' 的条目
//      （local-ollama / local-mlx-lm / local-mlx-dspark / local-llamacpp /
//      local-lmstudio / local-vllm，见 migration 671）。
//   2. 本地服务不校验 Authorization，因此凭据不需要真实 API key：
//      addCredential 对 local 供应商允许 api_key 为空，自动写入占位密钥
//      localNoKeyPlaceholder（照常走 Fernet/AES 加密存储）。
//   3. 占位密钥不可修改：rotate-primary-key / add-extra-key 对 local
//      供应商一律 400（localCredentialsImmutable）。
//   4. base_url 必须指向回环/私网地址，防止把本地托管服务误配到公网端点。

// localNoKeyPlaceholder 是本地供应商凭据的占位密钥明文。
// 加密后存入 credentials.secret_ciphertext，解密后作为 Bearer token 发给
// 本地服务 —— 本地服务不校验该值，但保持非空可以复用全部既有链路
// （discovery 解密、relay Authorization 头、probe）。
const localNoKeyPlaceholder = "local-no-key"

// LocalProviderKind 是 providers.kind / provider_catalog.kind 的本地值。
const LocalProviderKind = "local"

// isLocalKind reports whether a providers.kind / catalog.kind value means a
// locally hosted inference service.
func isLocalKind(kind string) bool {
	return strings.TrimSpace(strings.ToLower(kind)) == LocalProviderKind
}

// errLocalCredentialImmutable 是对本地供应商凭据执行写操作时的拒绝文案。
const errLocalCredentialImmutable = "local provider credential is managed automatically and cannot be rotated or modified"

// validateLocalBaseURL 确保本地供应商的 base_url 指向回环或私网地址。
// 返回 "" 表示合法；否则返回面向操作者的错误文案。
func validateLocalBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "local provider requires base_url"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "local provider base_url is not a valid URL: " + raw
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "local provider base_url must use http(s): " + raw
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]" {
		return ""
	}
	if strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") {
		return ""
	}
	if strings.HasPrefix(host, "172.") {
		// 172.16.0.0/12
		parts := strings.SplitN(host, ".", 3)
		if len(parts) >= 2 {
			var second int
			for _, ch := range parts[1] {
				if ch < '0' || ch > '9' {
					second = -1
					break
				}
				second = second*10 + int(ch-'0')
				if second > 99 {
					second = -1
					break
				}
			}
			if second >= 16 && second <= 31 {
				return ""
			}
		}
	}
	// Docker 桌面环境：网关容器经 host.docker.internal 访问宿主机本地服务。
	if host == "host.docker.internal" || strings.HasSuffix(host, ".docker.internal") || host == "host.internal.internal" {
		return ""
	}
	return "local provider base_url must point to a loopback/private host (got " + host + ")"
}

// ensureLocalCredential 为刚创建的本地供应商自动创建占位凭据。
// 幂等：同名 label 已存在 active 凭据时直接复用（ON CONFLICT 不适用，
// credentials 无唯一约束，这里按查询判断）。
// 返回 credential id；失败不阻塞供应商创建（记日志，运营可手工补建）。
func (h *Handler) ensureLocalCredential(ctx context.Context, providerID int, displayName string) int64 {
	// 已有凭据则复用
	var existing int64
	err := h.db.QueryRow(ctx, `
		SELECT id FROM credentials
		WHERE provider_id = $1 AND status NOT IN ('deleted')
		ORDER BY id LIMIT 1
	`, providerID).Scan(&existing)
	if err == nil {
		return existing
	}

	encrypted, encErr := h.encryptCred([]byte(localNoKeyPlaceholder))
	if encErr != nil {
		slog.Error("local provider: encrypt placeholder key failed", "provider_id", providerID, "error", encErr)
		return 0
	}

	// 本地服务无计费概念：plan_type=free。balance_usd 必须给正值 ——
	// 路由器会把 balance<=0 的凭据按 "balance:zero" 过滤掉（2026-09-07
	// 端到端实测踩坑）。给与常规凭据相同的默认余额；并发限制取保守小值，
	// 避免 auto 路由把高并发流量全压到单进程本地推理服务上。
	var id int64
	err = h.db.QueryRow(ctx, `
		INSERT INTO credentials (provider_id, label, secret_ciphertext, status, concurrency_limit, fp_slot_limit, balance_usd, plan_type)
		VALUES ($1, $2, $3, 'active', 4, 4, 1000.0, 'free')
		RETURNING id
	`, providerID, "local", encrypted).Scan(&id)
	if err != nil {
		slog.Error("local provider: auto credential insert failed", "provider_id", providerID, "error", err)
		return 0
	}
	slog.Info("local provider: auto-created placeholder credential",
		"provider_id", providerID,
		"credential_id", id,
		"provider", displayName,
	)
	return id
}

// providerKindByID 读取供应商 kind（"" 表示查询失败/不存在，由调用方兜底）。
// h.db 为 nil（单测构造的裸 Handler）时安全返回 ""。
func (h *Handler) providerKindByID(ctx context.Context, providerID int) string {
	if h == nil || h.db == nil {
		return ""
	}
	var kind string
	err := h.db.QueryRow(ctx, `SELECT COALESCE(kind,'') FROM providers WHERE id = $1`, providerID).Scan(&kind)
	if err != nil {
		return ""
	}
	return kind
}

// repairLocalProvider 幂等修复已存在的本地供应商：
//  1. base_url 仍含 {host}/{port} 模板占位符（历史 seed 残留）或为空时，
//     用请求传入的渲染值（或回环默认端口规则）修复；
//  2. 无凭据时自动创建占位凭据。
//
// 返回 (provider_id, credential_id, true)。供应商不存在或 kind 不是 local
// 返回 false，调用方回落 409。
func (h *Handler) repairLocalProvider(ctx context.Context, code, renderedBaseURL string) (int, int64, bool) {
	var id int
	var kind, baseURL string
	err := h.db.QueryRow(ctx, `
		SELECT id, COALESCE(kind,''), COALESCE(base_url,'')
		FROM providers
		WHERE tenant_id = 'default' AND code = $1 AND deleted_at IS NULL
	`, code).Scan(&id, &kind, &baseURL)
	if err != nil || !isLocalKind(kind) {
		return 0, 0, false
	}

	// base_url 修复：占位符残留 → 请求值优先，其次按 code 默认端口渲染。
	if strings.Contains(baseURL, "{host}") || strings.Contains(baseURL, "{port}") || baseURL == "" {
		fixed := strings.TrimSpace(renderedBaseURL)
		if fixed == "" || strings.Contains(fixed, "{host}") {
			fixed = defaultLocalBaseURLForCode(code)
		}
		if fixed != "" {
			if _, err := h.db.Exec(ctx,
				`UPDATE providers SET base_url = $1, updated_at = NOW() WHERE id = $2`, fixed, id); err != nil {
				slog.Warn("local provider: base_url repair failed", "provider_id", id, "error", err)
			} else {
				slog.Info("local provider: repaired template base_url",
					"provider_id", id, "code", code, "base_url", fixed)
			}
		}
	}

	credID := h.ensureLocalCredential(ctx, id, code)
	return id, credID, true
}

// defaultLocalBaseURLForCode 返回本地托管类型的回环默认端点（与 migration 671
// capabilities.default_port 一致）。
func defaultLocalBaseURLForCode(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "ollama":
		return "http://127.0.0.1:11434/v1"
	case "mlx":
		return "http://127.0.0.1:8080/v1"
	case "llamacpp":
		return "http://127.0.0.1:8082/v1"
	case "lmstudio":
		return "http://127.0.0.1:1234/v1"
	case "vllm":
		return "http://127.0.0.1:8000/v1"
	default:
		return ""
	}
}

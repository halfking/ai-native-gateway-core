package admin

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// local.go — 本地托管模型供应商（kind='local'）的共享辅助逻辑。
//
// 设计契约（2026-09-07，本地模型网关接入）：
//   1. 本地供应商来自 provider_catalog 中 kind='local' 的条目。
//      code 命名有两种来源：migration 671 的短 code（ollama / mlx /
//      llamacpp / lmstudio / vllm）与 scripts/local-models/
//      register-local-provider.sh 的 local-* 前缀（local-ollama /
//      local-mlx-lm / ...）。两种都支持（见
//      defaultLocalBaseURLForCode 的归一化处理）。
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

// localPrivateCIDRs 是本地供应商 base_url 允许落内的回环/私网网段
// （IPv4 回环 + RFC1918 + IPv6 回环/ULA）。刻意不含链路本地段
// （169.254.0.0/16、fe80::/10）——云厂商 metadata 服务（169.254.169.254）
// 是经典 SSRF 目标，不得借"本地供应商"配置放行。
var localPrivateCIDRs = func() []*net.IPNet {
	blocks := []string{
		"127.0.0.0/8",   // IPv4 loopback
		"10.0.0.0/8",    // RFC1918
		"172.16.0.0/12", // RFC1918
		"192.168.0.0/16",// RFC1918
		"::1/128",       // IPv6 loopback
		"fc00::/7",      // IPv6 ULA
	}
	out := make([]*net.IPNet, 0, len(blocks))
	for _, b := range blocks {
		_, n, err := net.ParseCIDR(b)
		if err != nil {
			panic("local_provider: bad CIDR literal: " + err.Error())
		}
		out = append(out, n)
	}
	return out
}()

// isLocalPrivateIP 精确判断字面 IP 是否属于回环/私网段。
func isLocalPrivateIP(ip net.IP) bool {
	for _, n := range localPrivateCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// validateLocalBaseURL 确保本地供应商的 base_url 指向回环或私网地址。
// 返回 "" 表示合法；否则返回面向操作者的错误文案。
//
// 判定规则（2026-09-07 审计收紧）：主机名必须是字面 IP 且精确落在
// localPrivateCIDRs 网段内；域名一律拒绝（此前按 "10."/"192.168." 字符串
// 前缀匹配，"10.evil.com"、"192.168.attacker.tld" 这类伪私网主机名会被放行）。
// 保留 localhost 与 Docker 桌面 host.docker.internal 例外。
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
	if host == "localhost" {
		return ""
	}
	// Docker 桌面环境：网关容器经 host.docker.internal 访问宿主机本地服务。
	if host == "host.docker.internal" || strings.HasSuffix(host, ".docker.internal") || host == "host.internal.internal" {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil || !isLocalPrivateIP(ip) {
		return "local provider base_url must point to a loopback/private host (got " + host + ")"
	}
	return ""
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
	//
	// 并发重入防护：ON CONFLICT 命中迁移 679 的局部唯一索引
	// （每 provider 至多一条活着的 local 占位凭据），输掉竞争的一方
	// 复读胜者，不再产生第二份并发额度。
	var id int64
	err = h.db.QueryRow(ctx, `
		INSERT INTO credentials (provider_id, label, secret_ciphertext, status, concurrency_limit, fp_slot_limit, balance_usd, plan_type)
		VALUES ($1, 'local', $2, 'active', 4, 4, 1000.0, 'free')
		ON CONFLICT (provider_id) WHERE label = 'local' AND status <> 'deleted'
		DO NOTHING
		RETURNING id
	`, providerID, encrypted).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// 输掉插入竞争：复用已有占位凭据。
		err = h.db.QueryRow(ctx, `
			SELECT id FROM credentials
			WHERE provider_id = $1 AND label = 'local' AND status NOT IN ('deleted')
			ORDER BY id LIMIT 1
		`, providerID).Scan(&id)
	}
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
// code 形态在仓库里有两种来源：migration 671 用短 code（ollama/mlx/...），
// scripts/local-models/register-local-provider.sh 用 local- 前缀 catalog code
// （local-ollama / local-mlx-lm / local-mlx-dspark / ...）。这里两种都接受：
// 剥掉 local- 前缀后匹配，mlx 的变体后缀一并归一——否则 repair 回退默认
// 端口的分支对其中一种命名永远失效（2026-09-07 审计 P2）。
func defaultLocalBaseURLForCode(catalogCode string) string {
	code := strings.ToLower(strings.TrimSpace(catalogCode))
	code = strings.TrimPrefix(code, "local-")
	switch code {
	case "ollama":
		return "http://127.0.0.1:11434/v1"
	case "mlx", "mlx-lm", "mlx-dspark":
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

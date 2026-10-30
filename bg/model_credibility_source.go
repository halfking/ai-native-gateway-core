// Package bg — model_credibility_source.go
//
// 可信度扫描的节点发现：把「(凭据 × 模型)」节点从 DB 里取出来，带上
// 直连探测所需的全部信息（base_url / protocol / catalog_code / 解密后的 key）。
//
// 为什么单独一个文件（而不是复用 bg/quality_node_source.go）：
//   - quality_node_source.go 返回的是 modelquality.CredentialNode，
//     那个结构**没有** protocol / catalog_code / canonical_id ——
//     而身份探测必须知道走哪种请求形态（chat / messages / responses）。
//   - 两边过滤口径相近但不完全相同：可信度扫描要覆盖**已被判定不可用**
//     的节点吗？—— 不要（不可用的节点探测必然失败，得到的结论没有信息量）。
//     但**要**覆盖被「协议红」下线的节点吗？—— 也不要（探测会被 404/410
//     直接拒掉，且这类下线是运维主动决策，不该被可信度扫描改写）。
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// CredibilityNodeSource 从 DB 发现可用于可信度探测的节点。
type CredibilityNodeSource struct {
	db      *pgxpool.Pool
	keyring *secret.Keyring
	encKey  []byte
}

// NewCredibilityNodeSource 创建节点发现器。
func NewCredibilityNodeSource(db *pgxpool.Pool, keyring *secret.Keyring, encKey []byte) *CredibilityNodeSource {
	return &CredibilityNodeSource{db: db, keyring: keyring, encKey: encKey}
}

// buildCredibilityTargetsSQL 生成节点发现 SQL。
//
// ★ 抽成函数（照搬 R170 的判据原则：判据必须落在【产物】上）——
// scope 过滤的形态断言测试直接断言这条 SQL 的最终形态，
// 而不是去扫源码片段。
func buildCredibilityTargetsSQL(scope string) string {
	featuredFilter := ""
	if scope == modelcredibility.ScopeFeatured {
		featuredFilter = `
		  AND (
		    pm.standardized_name = ANY(COALESCE(pol.featured_models, ARRAY[]::text[]))
		    OR pm.raw_model_name = ANY(COALESCE(pol.featured_models, ARRAY[]::text[]))
		  )`
	}
	return `
		SELECT DISTINCT c.id,
		       c.provider_id,
		       COALESCE(p.display_name, p.code, ''),
		       COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, ''),
		       COALESCE(p.base_url, ''),
		       COALESCE(pm.raw_model_name, ''),
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(pm.canonical_id, 0),
		       COALESCE(mc.canonical_name, ''),
		       c.secret_ciphertext,
		       pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		CROSS JOIN routing_policy pol
		WHERE pol.tenant_id = 'default'` + featuredFilter + `
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.availability_state, 'ready') NOT IN ('suspended')
		  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted','balance_exhausted')
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.available, TRUE) = TRUE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(pm.available, TRUE) = TRUE
		  AND COALESCE(pm.unavailable_reason, '') NOT LIKE 'manual%'
		ORDER BY c.id, pm.raw_model_name
	`
	// ★ DISTINCT 的理由见 bg/quality_node_source.go 同轮注释：routing_policy
	//   4 行物理重复（id 无唯一约束）⇒ 裸 CROSS JOIN 把 targets 放大 4 倍
	//   （211 实测 24h 190 runs / 49 唯一对），每对同轮串行重探 4 次。
}

// DiscoverTargets 列出所有活跃的 (凭据, 模型) 节点，带上探测所需字段。
//
// scope 控制扫描面：
//   - ScopeFeatured（默认）：只取 routing_policy.featured_models 命中的绑定
//     ——与 model_probe featuredCycle / shared_pick Priority 2 同一张清单。
//   - ScopeAll：全部活跃绑定。
//
// ★ 过滤口径（与 bg/quality_node_source.go 一致，另加两条）：
//   - 凭据/供应商/模型绑定都必须是活跃可路由的；
//   - ★ 排除 unavailable_reason LIKE 'manual%' 的绑定 —— 那是**运维人工
//     下线**（含「协议红批量下线」），不是「这家的模型是假的」⇒ 不该被
//     可信度扫描重新评价，也不该因为探测失败而被记成掺水。
func (s *CredibilityNodeSource) DiscoverTargets(ctx context.Context, scope string) ([]modelcredibility.NodeTarget, error) {
	if s.db == nil {
		return nil, fmt.Errorf("credibility node source: db pool is nil")
	}
	rows, err := s.db.Query(ctx, buildCredibilityTargetsSQL(scope))
	if err != nil {
		return nil, fmt.Errorf("query credibility node targets: %w", err)
	}
	defer rows.Close()

	var targets []modelcredibility.NodeTarget
	for rows.Next() {
		var (
			t                                                     modelcredibility.NodeTarget
			providerID                                            int64
			protocol, catalogCode, baseURL, rawModel, outModel   string
			canonicalID                                           int64
			ciphertext                                            []byte
			rawModelKey                                           string // 与 ORDER BY 同源的裸列（DISTINCT 去重键之一）
		)
		if err := rows.Scan(&t.CredentialID, &providerID, &t.ProviderName, &protocol,
			&catalogCode, &baseURL, &rawModel, &outModel, &canonicalID,
			&t.CanonicalName, &ciphertext, &rawModelKey); err != nil {
			slog.Warn("credibility node source: scan row failed", "error", err)
			continue
		}
		apiKey, decErr := decryptCiphertext(ciphertext, s.keyring, s.encKey)
		if decErr != nil {
			slog.Warn("credibility node source: decrypt failed, skipping node",
				"credential_id", t.CredentialID, "error", decErr)
			continue
		}
		t.ProviderID = providerID
		t.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		t.Protocol = protocol
		t.CatalogCode = catalogCode
		t.APIKey = apiKey
		t.RawModelName = rawModelKey
		t.OutboundModel = outModel
		t.CanonicalID = canonicalID
		if t.BaseURL == "" || t.RawModelName == "" {
			continue
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// FindTarget 查找单个 (credentialID, rawModelName) 节点。
//
// rawModelName 大小写不敏感匹配 raw_model_name 或 outbound_model_name
// （运维在界面上看到的可能是其中任一个）。
// ★ 手动触发不受 scope 限制：单点排查时运维就是要看指定的那个模型，
//   哪怕它不在特色清单里。
func (s *CredibilityNodeSource) FindTarget(ctx context.Context, credentialID int64, rawModelName string) (*modelcredibility.NodeTarget, error) {
	if rawModelName == "" {
		return nil, fmt.Errorf("rawModelName required")
	}
	targets, err := s.DiscoverTargets(ctx, modelcredibility.ScopeAll)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(rawModelName))
	for i := range targets {
		if targets[i].CredentialID != credentialID {
			continue
		}
		if strings.ToLower(targets[i].RawModelName) == want ||
			strings.ToLower(targets[i].OutboundModel) == want {
			return &targets[i], nil
		}
	}
	return nil, fmt.Errorf("credential node id=%d model=%s not found or not active", credentialID, rawModelName)
}

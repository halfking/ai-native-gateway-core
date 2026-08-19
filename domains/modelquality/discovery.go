package modelquality

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ModelDiscovery 模型发现接口 - 从网关配置中自动发现需要监控的模型
type ModelDiscovery interface {
	// DiscoverModels 发现所有活跃的供应商+模型组合
	DiscoverModels(ctx context.Context) ([]ModelTarget, error)
}

// GatewayModelDiscovery 从网关配置发现模型
type GatewayModelDiscovery struct {
	// TODO: 注入网关的依赖
	// credentialPool *credential.Pool
	// modelCatalog   *modelcatalog.Catalog
}

// NewGatewayModelDiscovery 创建网关模型发现器
func NewGatewayModelDiscovery() *GatewayModelDiscovery {
	return &GatewayModelDiscovery{}
}

// DiscoverModels 发现网关中配置的所有模型
func (d *GatewayModelDiscovery) DiscoverModels(ctx context.Context) ([]ModelTarget, error) {
	// TODO: 实现真实的模型发现逻辑
	// 1. 从凭据池获取所有活跃的供应商
	// 2. 从模型目录获取每个供应商支持的模型列表
	// 3. 过滤出特色模型和常用模型
	// 4. 返回 []ModelTarget

	return nil, fmt.Errorf("GatewayModelDiscovery not implemented - use StaticModelDiscovery for now")
}

// StaticModelDiscovery 静态配置的模型列表
type StaticModelDiscovery struct {
	models []ModelTarget
}

// NewStaticModelDiscovery 创建静态模型发现器
func NewStaticModelDiscovery(models []ModelTarget) *StaticModelDiscovery {
	return &StaticModelDiscovery{
		models: models,
	}
}

// DiscoverModels 返回静态配置的模型列表
func (d *StaticModelDiscovery) DiscoverModels(ctx context.Context) ([]ModelTarget, error) {
	return d.models, nil
}

// ConfigFileModelDiscovery 从配置文件发现模型
type ConfigFileModelDiscovery struct {
	configPath string
}

// NewConfigFileModelDiscovery 创建配置文件模型发现器
func NewConfigFileModelDiscovery(configPath string) *ConfigFileModelDiscovery {
	return &ConfigFileModelDiscovery{
		configPath: configPath,
	}
}

// DiscoverModels 从配置文件读取模型列表
func (d *ConfigFileModelDiscovery) DiscoverModels(ctx context.Context) ([]ModelTarget, error) {
	// TODO: 从YAML/JSON配置文件读取
	// 示例配置格式:
	// models:
	//   - provider: openai
	//     model: gpt-4
	//     alias: "OpenAI GPT-4"
	//     priority: high
	//   - provider: anthropic
	//     model: claude-3-opus
	//     alias: "Claude 3 Opus"
	//     priority: high

	return nil, fmt.Errorf("ConfigFileModelDiscovery not implemented")
}

// CanonicalCatalogDiscovery 从数据库目录（models_canonical × provider_models × providers）
// 发现活跃模型列表，按 canonical_name 去重，作为 ModelTarget 返回。
//
// 引入于 2026-08-20（feat/standard-models-rollout）：取代 GetDefaultMonitorModels 的静态回退
// —— 后者不再覆盖 grok-4.6 / kimi-k* / gemini-3.*。数据库行由 sql/migrations/domain/352-355
// 维护；routing_policy.featured_models 决定哪些 canonical 出现在 dashboard 头条。
//
// 设计要点：
//   - 只返回 status='active' 的 canonical 行（DB 是 single source of truth）
//   - 同时存在 provider_models 行才返回（意味着该 provider 已发现 / 已预填 361）
//   - 一个 canonical 可能挂在多个 provider 上（如 gpt-4o 在 openai/azure-openai），
//     DiscoverModels 会按 (provider, canonical) 返回多个 target，由调用方去重或展开
//   - 短超时（默认 1s），DB 慢不应阻塞监控循环
type CanonicalCatalogDiscovery struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

// NewCanonicalCatalogDiscovery 构造基于 models_canonical 的发现器。
func NewCanonicalCatalogDiscovery(pool *pgxpool.Pool) *CanonicalCatalogDiscovery {
	return &CanonicalCatalogDiscovery{pool: pool, timeout: 1 * time.Second}
}

// DiscoverModels 实现 ModelDiscovery 接口：读取 (providers × provider_models × models_canonical)。
func (d *CanonicalCatalogDiscovery) DiscoverModels(ctx context.Context) ([]ModelTarget, error) {
	if d == nil || d.pool == nil {
		return nil, fmt.Errorf("CanonicalCatalogDiscovery: pool is nil")
	}

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	const q = `
		SELECT p.code           AS provider,
		       pm.raw_model_name AS model_name,
		       mc.canonical_name AS canonical_model,
		       COALESCE(mc.display_name, mc.canonical_name) AS display_name
		FROM provider_models pm
		JOIN providers p
		  ON p.id = pm.provider_id AND p.tenant_id = pm.tenant_id
		JOIN models_canonical mc
		  ON mc.id = pm.canonical_id
		WHERE pm.tenant_id = 'default'
		  AND pm.available = TRUE
		  AND mc.status = 'active'
		  AND p.enabled = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		ORDER BY p.code, mc.canonical_name`

	rows, err := d.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("CanonicalCatalogDiscovery.Query: %w", err)
	}
	defer rows.Close()

	var targets []ModelTarget
	for rows.Next() {
		var t ModelTarget
		if err := rows.Scan(&t.Provider, &t.ModelName, &t.CanonicalModel, &t.Alias); err != nil {
			return nil, fmt.Errorf("CanonicalCatalogDiscovery.Scan: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("CanonicalCatalogDiscovery.rows: %w", err)
	}
	return targets, nil
}

// GetDefaultMonitorModels 获取默认的监控模型列表（特色模型+常用模型）
//
// Deprecated: 2026-08-20 — 新代码请使用 CanonicalCatalogDiscovery。
// 该函数仅保留作为兜底（DB 不可用时的零依赖路径），其覆盖范围不含
// grok-4.6 / kimi-k* / gemini-3.*；后续将逐步迁移到 DB-only 发现。
//
// Deprecated: prefer CanonicalCatalogDiscovery.
func GetDefaultMonitorModels() []ModelTarget {
	return []ModelTarget{
		// OpenAI 系列
		{
			Provider:  "openai",
			ModelName: "gpt-4",
			Alias:     "OpenAI GPT-4",
		},
		{
			Provider:  "openai",
			ModelName: "gpt-4-turbo",
			Alias:     "OpenAI GPT-4 Turbo",
		},
		{
			Provider:  "openai",
			ModelName: "gpt-3.5-turbo",
			Alias:     "OpenAI GPT-3.5 Turbo",
		},

		// Anthropic 系列
		{
			Provider:  "anthropic",
			ModelName: "claude-3-opus",
			Alias:     "Anthropic Claude 3 Opus",
		},
		{
			Provider:  "anthropic",
			ModelName: "claude-3-sonnet",
			Alias:     "Anthropic Claude 3 Sonnet",
		},

		// 国产特色模型 - 智谱
		{
			Provider:  "zhipu",
			ModelName: "glm-4",
			Alias:     "智谱 GLM-4",
		},
		{
			Provider:  "zhipu",
			ModelName: "glm-4-plus",
			Alias:     "智谱 GLM-4 Plus",
		},

		// 国产特色模型 - 通义千问
		{
			Provider:  "aliyun",
			ModelName: "qwen-plus",
			Alias:     "通义千问 Plus",
		},
		{
			Provider:  "aliyun",
			ModelName: "qwen-turbo",
			Alias:     "通义千问 Turbo",
		},

		// 国产特色模型 - 文心一言
		{
			Provider:  "baidu",
			ModelName: "ernie-4.0",
			Alias:     "百度 文心一言 4.0",
		},

		// 国产特色模型 - Kimi
		{
			Provider:  "moonshot",
			ModelName: "moonshot-v1-8k",
			Alias:     "月之暗面 Kimi",
		},

		// 国产特色模型 - 豆包
		{
			Provider:  "bytedance",
			ModelName: "doubao-pro",
			Alias:     "字节跳动 豆包 Pro",
		},
	}
}
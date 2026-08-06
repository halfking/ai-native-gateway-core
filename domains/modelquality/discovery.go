package modelquality

import (
	"context"
	"fmt"
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

// GetDefaultMonitorModels 获取默认的监控模型列表（特色模型+常用模型）
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

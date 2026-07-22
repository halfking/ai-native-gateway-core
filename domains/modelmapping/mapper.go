package modelmapping

// ModelMapper translates canonical model names to provider-specific names.
//
// Architecture:
//   - Client sends canonical names (e.g., "minimax-m2", "glm-5.1")
//   - Mapper looks up provider-specific name (e.g., "minimaxai/minimax-m2.7" for NVIDIA)
//   - Provider receives its native name
//
// This matches the boss's spec:
//   "供应商凭据下记录详细的原始模型名称，我们使用标准名称请求，路由匹配后进行转换，真实提交供供应商的是供应商的原始模型名称"

// CanonicalMapping maps canonical_name → provider_name → native_name
type CanonicalMapping map[string]map[string]string

// DefaultCanonicalMappings is the registry of canonical→native model names per provider.
//
// IMPORTANT: These mappings were verified against real APIs in 2026-07-22.
// Update this map when new providers/models are added.
var DefaultCanonicalMappings = CanonicalMapping{
	// ===== Minimax models =====
	"minimax-m2": {
		"minimax": "MiniMax-M2",
		"nvidia":  "minimaxai/minimax-m2.7",
		"evol":    "MiniMax-M2.7",
		"kaixuan": "minimax-m2.7",
		"default": "MiniMax-M2",
	},
	"minimax-m3": {
		"minimax": "MiniMax-M3",
		"nvidia":  "minimaxai/minimax-m3",
		"evol":    "MiniMax-M3",
		"kaixuan": "minimax-m3",
		"default": "MiniMax-M3",
	},

	// ===== 智谱 GLM models =====
	"glm-4.7": {
		"zhipu":   "glm-4.7",
		"nvidia":  "z-ai/glm-4.5", // NVIDIA has 4.5 not 4.7
		"kaixuan": "glm-4.7",
		"default": "glm-4.7",
	},
	"glm-5.1": {
		"zhipu":   "glm-5.1",
		"nvidia":  "z-ai/glm-5.2", // NVIDIA only has 5.2
		"evol":    "glm-5.1",
		"kaixuan": "glm-5.1",
		"default": "glm-5.1",
	},

	// ===== Anthropic Claude models =====
	"claude-sonnet-4": {
		"evol":    "claude-sonnet-4-6",
		"default": "claude-sonnet-4-6",
	},
	"claude-opus-4": {
		"evol":    "claude-opus-4-8",
		"default": "claude-opus-4-8",
	},

	// ===== OpenAI GPT models =====
	"gpt-5": {
		"evol":    "gpt-5.5",
		"default": "gpt-5.5",
	},

	// ===== DeepSeek models =====
	"deepseek-v4": {
		"evol":    "deepseek-v4-flash",
		"kaixuan": "deepseek-v4-pro",
		"default": "deepseek-v4-flash",
	},

	// ===== Xiaomi mimo models =====
	"mimo-v2.5": {
		"xiaomi":  "mimo-v2.5",
		"kaixuan": "mimo-v2.5-pro",
		"default": "mimo-v2.5",
	},
}

// ModelMapper translates canonical names to provider-specific names.
type ModelMapper struct {
	mappings CanonicalMapping
}

// NewModelMapper creates a new mapper with default mappings.
func NewModelMapper() *ModelMapper {
	return &ModelMapper{mappings: DefaultCanonicalMappings}
}

// NewModelMapperWithMappings creates a mapper with custom mappings.
func NewModelMapperWithMappings(mappings CanonicalMapping) *ModelMapper {
	return &ModelMapper{mappings: mappings}
}

// Translate returns the provider-specific model name for a canonical name.
//
// If canonical is unknown, returns canonical (passthrough).
// If provider is not in the mapping for that canonical, returns the "default" mapping.
func (m *ModelMapper) Translate(canonical, provider string) string {
	providerMap, ok := m.mappings[canonical]
	if !ok {
		// Unknown canonical - passthrough
		return canonical
	}
	if native, ok := providerMap[provider]; ok {
		return native
	}
	// Provider not mapped - try default
	if defaultName, ok := providerMap["default"]; ok {
		return defaultName
	}
	return canonical
}

// SupportsCanonical reports whether a provider supports a given canonical model.
func (m *ModelMapper) SupportsCanonical(provider, canonical string) bool {
	providerMap, ok := m.mappings[canonical]
	if !ok {
		return false
	}
	_, has := providerMap[provider]
	return has
}

// ProvidersFor returns all provider names that natively support a canonical model.
func (m *ModelMapper) ProvidersFor(canonical string) []string {
	providerMap, ok := m.mappings[canonical]
	if !ok {
		return nil
	}
	providers := make([]string, 0, len(providerMap))
	for p := range providerMap {
		if p != "default" {
			providers = append(providers, p)
		}
	}
	return providers
}

// CanonicalModels returns all registered canonical model names.
func (m *ModelMapper) CanonicalModels() []string {
	models := make([]string, 0, len(m.mappings))
	for c := range m.mappings {
		models = append(models, c)
	}
	return models
}

// RegisterMapping adds or updates a mapping at runtime.
func (m *ModelMapper) RegisterMapping(canonical, provider, nativeName string) {
	if _, ok := m.mappings[canonical]; !ok {
		m.mappings[canonical] = make(map[string]string)
	}
	m.mappings[canonical][provider] = nativeName
}

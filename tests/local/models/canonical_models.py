"""
Canonical Model Registry - 标准化模型名称映射

设计原则:
- 客户端使用 canonical 名称 (e.g., "minimax-m2", "claude-sonnet-4")
- 数据库/配置中存储 provider-specific 名称 (e.g., "minimaxai/minimax-m2.7", "claude-sonnet-4-6")
- ModelMapper 在请求时自动转换

Usage:
  from canonical_models import CANONICAL_MODELS, get_provider_model

  canonical = "minimax-m2"
  for provider, native_name in CANONICAL_MODELS[canonical].items():
      print(f"  {provider}: {native_name}")
"""

# Canonical model name → provider-specific name
CANONICAL_MODELS = {
    # ============ Minimax ============
    "minimax-m2": {
        "minimax": "MiniMax-M2",  # API direct
        "nvidia": "minimaxai/minimax-m2.7",  # NVIDIA NIM
        "evol": "MiniMax-M2.7",  # Evol proxy
        "kaixuan": "minimax-m2.7",  # Self-hosted
        "default": "MiniMax-M2",
    },
    "minimax-m3": {
        "minimax": "MiniMax-M3",
        "nvidia": "minimaxai/minimax-m3",
        "evol": "MiniMax-M3",
        "kaixuan": "minimax-m3",
        "default": "MiniMax-M3",
    },
    # ============ 智谱 GLM ============
    "glm-4.7": {
        "zhipu": "glm-4.7",
        "nvidia": "z-ai/glm-4.5",  # NVIDIA has glm-4.5, not 4.7
        "kaixuan": "glm-4.7",  # Fallback to known
        "default": "glm-4.7",
    },
    "glm-5.1": {
        "zhipu": "glm-5.1",
        "nvidia": "z-ai/glm-5.2",  # NVIDIA only has 5.2
        "evol": "glm-5.1",
        "kaixuan": "glm-5.1",
        "default": "glm-5.1",
    },
    # ============ Anthropic Claude ============
    "claude-sonnet-4": {
        "evol": "claude-sonnet-4-6",
        "default": "claude-sonnet-4-6",
    },
    "claude-opus-4": {
        "evol": "claude-opus-4-8",
        "default": "claude-opus-4-8",
    },
    # ============ OpenAI GPT ============
    "gpt-5": {
        "evol": "gpt-5.5",  # Use cheapest GPT-5 variant
        "default": "gpt-5.5",
    },
    # ============ DeepSeek ============
    "deepseek-v4": {
        "evol": "deepseek-v4-flash",
        "kaixuan": "deepseek-v4-pro",
        "default": "deepseek-v4-flash",
    },
    # ============ Xiaomi mimo ============
    "mimo-v2.5": {
        "xiaomi": "mimo-v2.5",
        "kaixuan": "mimo-v2.5-pro",  # Use pro on kaixuan
        "default": "mimo-v2.5",
    },
}


def get_provider_model(canonical: str, provider: str) -> str:
    """Get provider-specific model name for a canonical name."""
    if canonical not in CANONICAL_MODELS:
        raise ValueError(f"Unknown canonical model: {canonical}")

    provider_map = CANONICAL_MODELS[canonical]

    # Try exact provider match
    if provider in provider_map:
        return provider_map[provider]

    # Fall back to default
    return provider_map.get("default", canonical)


def list_canonical_models() -> list:
    """List all canonical model names."""
    return list(CANONICAL_MODELS.keys())


def list_providers_for_model(canonical: str) -> list:
    """List providers that can serve a canonical model."""
    if canonical not in CANONICAL_MODELS:
        return []
    # All keys except 'default'
    return [k for k in CANONICAL_MODELS[canonical].keys() if k != "default"]


if __name__ == "__main__":
    print("Canonical models registered:")
    for canonical in list_canonical_models():
        print(f"\n{canonical}:")
        for provider, native in CANONICAL_MODELS[canonical].items():
            if provider != "default":
                print(f"  {provider:12s} → {native}")

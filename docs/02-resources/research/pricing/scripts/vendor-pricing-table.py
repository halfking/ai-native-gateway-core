#!/usr/bin/env python3
"""
vendor-pricing-table.py — Single source of truth for model pricing.

This file is the AUTHORITATIVE pricing reference for all models.
Used by diff-pricing.py to generate diffs and by apply-pricing.sh to apply changes.

Structure:
  CANONICAL_PRICING[canonical_name] = {
    'currency': 'CNY'|'USD',
    'input_per_1m': float,
    'output_per_1m': float,
    'billing_mode': 'token'|'token_plan'|'code_plan',
    'vendor': str,           # Original vendor (e.g. 'zhipu', 'openai')
    'source': str,           # URL or 'estimated'
    'scraped_at': str,       # ISO date when price was last verified
    'note': str|None,        # Optional note
  }

Rules:
  1. ALL Chinese domestic vendor models MUST use CNY
  2. International models (OpenAI/Anthropic/Google/xAI/Mistral) use USD
  3. Proxy credentials (MiniMax→Claude, Zhipu→Llama) inherit domestic currency
  4. token_plan billing for subscription credentials (xiaomi, volcano-tokenplan)
  5. Prices are per-1M tokens
  6. 'estimated' prices marked with note, should be verified manually
"""

CANONICAL_PRICING = {
    # ═══════════════════════════════════════════════════════════
    # OpenAI — USD
    # Source: platform.openai.com/docs/pricing
    # ═══════════════════════════════════════════════════════════
    'gpt-4o': {
        'currency': 'USD', 'input_per_1m': 2.50, 'output_per_1m': 10.00,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'gpt-4o-mini': {
        'currency': 'USD', 'input_per_1m': 0.15, 'output_per_1m': 0.60,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'gpt-4-turbo': {
        'currency': 'USD', 'input_per_1m': 10.00, 'output_per_1m': 30.00,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'gpt-3.5-turbo': {
        'currency': 'USD', 'input_per_1m': 0.50, 'output_per_1m': 1.50,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'o1': {
        'currency': 'USD', 'input_per_1m': 15.00, 'output_per_1m': 60.00,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'o1-mini': {
        'currency': 'USD', 'input_per_1m': 3.00, 'output_per_1m': 12.00,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'o3': {
        'currency': 'USD', 'input_per_1m': 2.00, 'output_per_1m': 8.00,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'o3-mini': {
        'currency': 'USD', 'input_per_1m': 1.10, 'output_per_1m': 4.40,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'o4-mini': {
        'currency': 'USD', 'input_per_1m': 1.10, 'output_per_1m': 4.40,
        'billing_mode': 'token', 'vendor': 'openai',
        'source': 'platform.openai.com/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # Anthropic — USD
    # Source: docs.anthropic.com/en/docs/about-claude/models
    # ═══════════════════════════════════════════════════════════
    'claude-haiku-4.5': {
        'currency': 'USD', 'input_per_1m': 1.00, 'output_per_1m': 5.00,
        'billing_mode': 'token', 'vendor': 'anthropic',
        'source': 'docs.anthropic.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'claude-opus-4': {
        'currency': 'USD', 'input_per_1m': 15.00, 'output_per_1m': 75.00,
        'billing_mode': 'token', 'vendor': 'anthropic',
        'source': 'docs.anthropic.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'claude-opus-4.5': {
        'currency': 'USD', 'input_per_1m': 15.00, 'output_per_1m': 75.00,
        'billing_mode': 'token', 'vendor': 'anthropic',
        'source': 'docs.anthropic.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'claude-sonnet-4.5': {
        'currency': 'USD', 'input_per_1m': 3.00, 'output_per_1m': 15.00,
        'billing_mode': 'token', 'vendor': 'anthropic',
        'source': 'docs.anthropic.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'claude-sonnet-4.6': {
        'currency': 'USD', 'input_per_1m': 3.00, 'output_per_1m': 15.00,
        'billing_mode': 'token', 'vendor': 'anthropic',
        'source': 'docs.anthropic.com', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # Google Gemini — USD
    # ═══════════════════════════════════════════════════════════
    'gemini-2.5-pro': {
        'currency': 'USD', 'input_per_1m': 1.25, 'output_per_1m': 10.00,
        'billing_mode': 'token', 'vendor': 'google',
        'source': 'ai.google.dev/gemini-api/docs/models', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'gemini-2.5-flash': {
        'currency': 'USD', 'input_per_1m': 0.075, 'output_per_1m': 0.30,
        'billing_mode': 'token', 'vendor': 'google',
        'source': 'ai.google.dev/gemini-api/docs/models', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # xAI Grok — USD
    # ═══════════════════════════════════════════════════════════
    'grok-3': {
        'currency': 'USD', 'input_per_1m': 3.00, 'output_per_1m': 15.00,
        'billing_mode': 'token', 'vendor': 'xai',
        'source': 'docs.x.ai/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'grok-3-mini': {
        'currency': 'USD', 'input_per_1m': 0.30, 'output_per_1m': 0.50,
        'billing_mode': 'token', 'vendor': 'xai',
        'source': 'docs.x.ai/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'grok-4': {
        'currency': 'USD', 'input_per_1m': 3.00, 'output_per_1m': 15.00,
        'billing_mode': 'token', 'vendor': 'xai',
        'source': 'docs.x.ai/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # DeepSeek — CNY (国内定价)
    # Source: api-docs.deepseek.com/zh-cn/quick_start/pricing
    # ═══════════════════════════════════════════════════════════
    'deepseek-chat': {
        'currency': 'CNY', 'input_per_1m': 1.00, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'api-docs.deepseek.com/zh-cn', 'scraped_at': '2026-06-12',
        'note': 'deepseek-chat = deepseek-v4-flash non-thinking mode',
    },
    'deepseek-reasoner': {
        'currency': 'CNY', 'input_per_1m': 3.00, 'output_per_1m': 6.00,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'api-docs.deepseek.com/zh-cn', 'scraped_at': '2026-06-12',
        'note': 'deepseek-reasoner = deepseek-v4-flash thinking mode',
    },
    'deepseek-v3': {
        'currency': 'CNY', 'input_per_1m': 1.00, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'api-docs.deepseek.com/zh-cn', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'deepseek-v3.1': {
        'currency': 'CNY', 'input_per_1m': 1.00, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'api-docs.deepseek.com/zh-cn', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'deepseek-r1': {
        'currency': 'CNY', 'input_per_1m': 3.00, 'output_per_1m': 6.00,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'api-docs.deepseek.com/zh-cn', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'deepseek-coder-6.7b-instruct': {
        'currency': 'CNY', 'input_per_1m': 1.00, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy open-source, estimated from deepseek-chat pricing',
    },
    'deepseek-v4-flash': {
        'currency': 'CNY', 'input_per_1m': 0.14, 'output_per_1m': 0.28,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'api-docs.deepseek.com/zh-cn', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'deepseek-v4-pro': {
        'currency': 'CNY', 'input_per_1m': 3.00, 'output_per_1m': 6.00,
        'billing_mode': 'token', 'vendor': 'deepseek',
        'source': 'api-docs.deepseek.com/zh-cn', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # 智谱 Zhipu GLM — CNY
    # Source: open.bigmodel.cn/pricing (2026-06-11 scraped)
    # ═══════════════════════════════════════════════════════════
    'glm-4': {
        'currency': 'CNY', 'input_per_1m': 0.10, 'output_per_1m': 0.10,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'glm-4-air': {
        'currency': 'CNY', 'input_per_1m': 0.001, 'output_per_1m': 0.001,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'free tier',
    },
    'glm-4-flash': {
        'currency': 'CNY', 'input_per_1m': 0.00, 'output_per_1m': 0.00,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'free',
    },
    'glm-4-9b-chat': {
        'currency': 'CNY', 'input_per_1m': 0.001, 'output_per_1m': 0.001,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'free tier',
    },
    'glm-4.5': {
        'currency': 'CNY', 'input_per_1m': 4.32, 'output_per_1m': 15.84,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy model, CNY estimated from USD*7.2',
    },
    'glm-4.5-air': {
        'currency': 'CNY', 'input_per_1m': 0.90, 'output_per_1m': 6.12,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy model, CNY estimated from USD*7.2',
    },
    'glm-4.5-flash': {
        'currency': 'CNY', 'input_per_1m': 0.00, 'output_per_1m': 0.00,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'free',
    },
    'glm-4.7': {
        'currency': 'CNY', 'input_per_1m': 2.00, 'output_per_1m': 8.00,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'input<32k, output<0.2M tier',
    },
    'glm-4.7-flash': {
        'currency': 'CNY', 'input_per_1m': 0.00, 'output_per_1m': 0.00,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'free',
    },
    'glm-5': {
        'currency': 'CNY', 'input_per_1m': 4.00, 'output_per_1m': 18.00,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'input<32k tier',
    },
    'glm-5.1': {
        'currency': 'CNY', 'input_per_1m': 6.00, 'output_per_1m': 24.00,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'input<32k tier',
    },
    'glm-z1-flash': {
        'currency': 'CNY', 'input_per_1m': 0.00, 'output_per_1m': 0.00,
        'billing_mode': 'token', 'vendor': 'zhipu',
        'source': 'open.bigmodel.cn/pricing', 'scraped_at': '2026-06-12',
        'note': 'free',
    },

    # ═══════════════════════════════════════════════════════════
    # 通义千问 Qwen — CNY
    # Source: help.aliyun.com/zh/model-studio/model-pricing
    # ═══════════════════════════════════════════════════════════
    'qwen-max': {
        'currency': 'CNY', 'input_per_1m': 2.40, 'output_per_1m': 9.60,
        'billing_mode': 'token', 'vendor': 'qwen',
        'source': 'help.aliyun.com/model-pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'qwen-plus': {
        'currency': 'CNY', 'input_per_1m': 0.80, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'qwen',
        'source': 'help.aliyun.com/model-pricing', 'scraped_at': '2026-06-12',
        'note': 'input<128k tier',
    },
    'qwen-turbo': {
        'currency': 'CNY', 'input_per_1m': 0.30, 'output_per_1m': 0.60,
        'billing_mode': 'token', 'vendor': 'qwen',
        'source': 'help.aliyun.com/model-pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'qwen2.5-72b-instruct': {
        'currency': 'CNY', 'input_per_1m': 4.00, 'output_per_1m': 12.00,
        'billing_mode': 'token', 'vendor': 'qwen',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy, estimated from qwen3-32b pricing tier',
    },
    'qwen2.5-7b-instruct': {
        'currency': 'CNY', 'input_per_1m': 1.00, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'qwen',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy, estimated',
    },
    'qwq-32b': {
        'currency': 'CNY', 'input_per_1m': 1.00, 'output_per_1m': 4.00,
        'billing_mode': 'token', 'vendor': 'qwen',
        'source': 'help.aliyun.com/model-pricing', 'scraped_at': '2026-06-12',
        'note': 'estimated from qwq-plus 1.6/4 tier',
    },

    # ═══════════════════════════════════════════════════════════
    # 豆包 Doubao (火山方舟) — CNY
    # Source: volcengine.com/docs/82379/1544106
    # ═══════════════════════════════════════════════════════════
    'doubao-1-5-lite-32k': {
        'currency': 'CNY', 'input_per_1m': 0.30, 'output_per_1m': 0.60,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-1-5-pro-256k': {
        'currency': 'CNY', 'input_per_1m': 1.50, 'output_per_1m': 4.00,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-1-5-pro-32k': {
        'currency': 'CNY', 'input_per_1m': 0.80, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-embedding-large-text': {
        'currency': 'CNY', 'input_per_1m': 0.10, 'output_per_1m': 0.00,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-embedding-vision': {
        'currency': 'CNY', 'input_per_1m': 0.10, 'output_per_1m': 0.00,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-lite-4k': {
        'currency': 'CNY', 'input_per_1m': 0.05, 'output_per_1m': 0.10,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-pro-128k': {
        'currency': 'CNY', 'input_per_1m': 0.80, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-pro-32k': {
        'currency': 'CNY', 'input_per_1m': 0.80, 'output_per_1m': 2.00,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-pro-4k': {
        'currency': 'CNY', 'input_per_1m': 0.10, 'output_per_1m': 0.20,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-seed-2.0-lite': {
        'currency': 'CNY', 'input_per_1m': 0.60, 'output_per_1m': 1.80,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': 'input<32k tier',
    },
    'doubao-seed-2.0-mini': {
        'currency': 'CNY', 'input_per_1m': 0.10, 'output_per_1m': 0.20,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'doubao-seed-2.0-pro': {
        'currency': 'CNY', 'input_per_1m': 3.20, 'output_per_1m': 16.00,
        'billing_mode': 'token', 'vendor': 'doubao',
        'source': 'volcengine.com/docs/82379/1544106', 'scraped_at': '2026-06-12',
        'note': 'input<32k tier',
    },

    # ═══════════════════════════════════════════════════════════
    # 月之暗面 Moonshot — CNY
    # Source: platform.moonshot.ai/docs/pricing
    # ═══════════════════════════════════════════════════════════
    'moonshot-v1-8k': {
        'currency': 'CNY', 'input_per_1m': 12.00, 'output_per_1m': 12.00,
        'billing_mode': 'token', 'vendor': 'moonshot',
        'source': 'platform.moonshot.ai/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'moonshot-v1-32k': {
        'currency': 'CNY', 'input_per_1m': 24.00, 'output_per_1m': 24.00,
        'billing_mode': 'token', 'vendor': 'moonshot',
        'source': 'platform.moonshot.ai/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'moonshot-v1-128k': {
        'currency': 'CNY', 'input_per_1m': 60.00, 'output_per_1m': 60.00,
        'billing_mode': 'token', 'vendor': 'moonshot',
        'source': 'platform.moonshot.ai/docs/pricing', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # MiniMax — CNY
    # Source: platform.minimaxi.com/docs/guides/pricing-paygo
    # ═══════════════════════════════════════════════════════════
    'minimax-m2.5': {
        'currency': 'CNY', 'input_per_1m': 2.10, 'output_per_1m': 8.40,
        'billing_mode': 'token', 'vendor': 'minimax',
        'source': 'platform.minimaxi.com/pricing-paygo', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'minimax-m2.7': {
        'currency': 'CNY', 'input_per_1m': 2.10, 'output_per_1m': 8.40,
        'billing_mode': 'token', 'vendor': 'minimax',
        'source': 'platform.minimaxi.com/pricing-paygo', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'minimax-text-01': {
        'currency': 'CNY', 'input_per_1m': 3.00, 'output_per_1m': 12.00,
        'billing_mode': 'token', 'vendor': 'minimax',
        'source': 'platform.minimaxi.com/pricing-paygo', 'scraped_at': '2026-06-12',
        'note': 'estimated from M2.7 tier',
    },
    'abab5.5-chat': {
        'currency': 'CNY', 'input_per_1m': 15.00, 'output_per_1m': 15.00,
        'billing_mode': 'token', 'vendor': 'minimax',
        'source': 'platform.minimaxi.com', 'scraped_at': '2026-06-12',
        'note': 'legacy model',
    },
    'abab6.5s-chat': {
        'currency': 'CNY', 'input_per_1m': 10.00, 'output_per_1m': 10.00,
        'billing_mode': 'token', 'vendor': 'minimax',
        'source': 'platform.minimaxi.com', 'scraped_at': '2026-06-12',
        'note': 'legacy model',
    },

    # ═══════════════════════════════════════════════════════════
    # 百川 Baichuan — CNY
    # ═══════════════════════════════════════════════════════════
    'baichuan4': {
        'currency': 'CNY', 'input_per_1m': 16.00, 'output_per_1m': 32.00,
        'billing_mode': 'token', 'vendor': 'baichuan',
        'source': 'platform.baichuan-ai.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'baichuan3-turbo-128k': {
        'currency': 'CNY', 'input_per_1m': 8.00, 'output_per_1m': 16.00,
        'billing_mode': 'token', 'vendor': 'baichuan',
        'source': 'platform.baichuan-ai.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'baichuan3-turbo': {
        'currency': 'CNY', 'input_per_1m': 4.00, 'output_per_1m': 8.00,
        'billing_mode': 'token', 'vendor': 'baichuan',
        'source': 'platform.baichuan-ai.com', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # 零一万物 Yi — CNY
    # ═══════════════════════════════════════════════════════════
    'yi-large': {
        'currency': 'CNY', 'input_per_1m': 20.00, 'output_per_1m': 20.00,
        'billing_mode': 'token', 'vendor': 'yi',
        'source': 'platform.lingyiwanwu.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'yi-large-turbo': {
        'currency': 'CNY', 'input_per_1m': 12.00, 'output_per_1m': 12.00,
        'billing_mode': 'token', 'vendor': 'yi',
        'source': 'platform.lingyiwanwu.com', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'yi-medium': {
        'currency': 'CNY', 'input_per_1m': 2.50, 'output_per_1m': 2.50,
        'billing_mode': 'token', 'vendor': 'yi',
        'source': 'platform.lingyiwanwu.com', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # 阶跃星辰 Step — CNY
    # Source: platform.stepfun.com/docs/zh/guides/pricing/details
    # ═══════════════════════════════════════════════════════════
    'step-1-256k': {
        'currency': 'CNY', 'input_per_1m': 5.00, 'output_per_1m': 10.00,
        'billing_mode': 'token', 'vendor': 'step',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy, estimated from step-3.x pricing',
    },
    'step-1v-32k': {
        'currency': 'CNY', 'input_per_1m': 5.00, 'output_per_1m': 10.00,
        'billing_mode': 'token', 'vendor': 'step',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy, estimated',
    },
    'step-2-16k': {
        'currency': 'CNY', 'input_per_1m': 5.00, 'output_per_1m': 10.00,
        'billing_mode': 'token', 'vendor': 'step',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'legacy, estimated',
    },

    # ═══════════════════════════════════════════════════════════
    # 商汤 SenseChat — CNY
    # ═══════════════════════════════════════════════════════════
    'sensechat-5': {
        'currency': 'CNY', 'input_per_1m': 21.00, 'output_per_1m': 42.00,
        'billing_mode': 'token', 'vendor': 'sensetime',
        'source': 'platform.sensenova.cn', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'sensechat-5-thinking': {
        'currency': 'CNY', 'input_per_1m': 42.00, 'output_per_1m': 84.00,
        'billing_mode': 'token', 'vendor': 'sensetime',
        'source': 'platform.sensenova.cn', 'scraped_at': '2026-06-12',
        'note': None,
    },
    'sensechat-turbo': {
        'currency': 'CNY', 'input_per_1m': 4.00, 'output_per_1m': 8.00,
        'billing_mode': 'token', 'vendor': 'sensetime',
        'source': 'platform.sensenova.cn', 'scraped_at': '2026-06-12',
        'note': None,
    },

    # ═══════════════════════════════════════════════════════════
    # Embedding & misc — CNY (domestic) or USD
    # ═══════════════════════════════════════════════════════════
    'bge-m3': {
        'currency': 'CNY', 'input_per_1m': 0.36, 'output_per_1m': 0.00,
        'billing_mode': 'token', 'vendor': 'baai',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'open-source embedding, CNY estimated from USD*7.2',
    },
    'mimo-v2.5-pro': {
        'currency': 'CNY', 'input_per_1m': 3.13, 'output_per_1m': 6.26,
        'billing_mode': 'token', 'vendor': 'xiaomi',
        'source': 'estimated', 'scraped_at': '2026-06-12',
        'note': 'CNY estimated from USD*7.2',
    },
}


# ═══════════════════════════════════════════════════════════════
# CREDENTIAL-LEVEL OVERRIDES
# When a model is accessed via a specific credential, these rules apply.
# Key: (credential_label, canonical_name)
# ═══════════════════════════════════════════════════════════════
CREDENTIAL_OVERRIDES = {
    # xiaomi-token-plan: all OpenAI models are token_plan billing
    ('xiaomi-token-plan', 'gpt-4-turbo'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'gpt-4o'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'gpt-4o-mini'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'gpt-3.5-turbo'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'o1'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'o1-mini'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'o3'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'o3-mini'): {'billing_mode': 'token_plan'},
    ('xiaomi-token-plan', 'o4-mini'): {'billing_mode': 'token_plan'},

    # demo-tokenplan: volcano token-plan
    ('demo-tokenplan', 'llama-3.1-nemotron-nano-8b-v1'): {'billing_mode': 'token_plan'},
    ('demo-tokenplan', 'llama-3.1-nemotron-70b-instruct'): {'billing_mode': 'token_plan'},
    ('demo-tokenplan', 'llama-3.1-nemotron-51b-instruct'): {'billing_mode': 'token_plan'},
    ('demo-tokenplan', 'llama-3.1-nemoguard-8b-topic-control'): {'billing_mode': 'token_plan'},
    ('demo-tokenplan', 'llama-3.1-nemoguard-8b-content-safety'): {'billing_mode': 'token_plan'},
}


# ═══════════════════════════════════════════════════════════════
# DOMESTIC PROVIDERS — ALL models on these credentials MUST be CNY
# ═══════════════════════════════════════════════════════════════
DOMESTIC_CREDENTIALS = {
    'minimax-prod-1',
    'roocode',
    'xiaomi-token-plan',
    'demo-tokenplan',
    'hzx-normal',
}

# Domestic model canonical name prefixes (for nvidia-build mixed credential)
DOMESTIC_MODEL_PREFIXES = (
    'glm-', 'qwen', 'qwq', 'doubao', 'moonshot', 'yi-',
    'baichuan', 'minimax', 'deepseek', 'mimo', 'sensechat',
    'step-', 'abab', 'bge',
)


def get_pricing(canonical_name, credential_label=None):
    """Get pricing for a model, with credential-level overrides."""
    entry = CANONICAL_PRICING.get(canonical_name)
    if not entry:
        return None

    result = dict(entry)

    if credential_label:
        override = CREDENTIAL_OVERRIDES.get((credential_label, canonical_name))
        if override:
            result.update(override)

    return result


def is_domestic_credential(credential_label):
    """Check if a credential belongs to a domestic provider."""
    return credential_label in DOMESTIC_CREDENTIALS


def is_domestic_model(canonical_name):
    """Check if a model is from a domestic vendor."""
    return any(canonical_name.startswith(p) for p in DOMESTIC_MODEL_PREFIXES)


if __name__ == '__main__':
    print(f"Total models in pricing table: {len(CANONICAL_PRICING)}")
    print(f"CNY models: {sum(1 for v in CANONICAL_PRICING.values() if v['currency'] == 'CNY')}")
    print(f"USD models: {sum(1 for v in CANONICAL_PRICING.values() if v['currency'] == 'USD')}")
    print(f"Domestic credentials: {len(DOMESTIC_CREDENTIALS)}")
    print(f"Credential overrides: {len(CREDENTIAL_OVERRIDES)}")

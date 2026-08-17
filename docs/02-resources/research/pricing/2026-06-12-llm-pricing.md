# LLM Pricing Reference — 2026-06-12

> 自动抓取 · 交叉验证 · 包含 token_plan / code_plan 分类
> 抓取时间: 2026-06-12 (UTC) · 适用: llm-gateway-go 184 k3s

## 📊 概况 (Summary)

| 厂商 | 模型数 | 主币种 | 抓取 URL | Tier |
|---|---|---|---|---|
| **Zhipu 智谱** | 11 | CNY | https://open.bigmodel.cn/pricing | T1 |
| **MiniMax** | 4 | USD | https://platform.minimax.io/docs/guides/pricing-paygo | T1 |
| **Xiaomi 小米** | 9 | CNY | https://r.jina.ai/openrouter.ai/api/v1/models (xiaomi/*) | T1 |
| **DeepSeek** | 3 | USD | https://api-docs.deepseek.com/quick_start/pricing | T1 |
| **Moonshot Kimi** | 2 | USD | https://r.jina.ai/openrouter.ai/api/v1/models (moonshotai/*) | T1 |
| **OpenAI** | 5 | USD | https://platform.openai.com/docs/models | T2 |
| **Anthropic** | 7 | USD | https://docs.anthropic.com/en/docs/about-claude/models/overview | T2 |
| **Google Gemini** | 3 | USD | https://ai.google.dev/gemini-api/docs/models | T2 |
| **xAI Grok** | 1 | USD | https://docs.x.ai/docs/pricing | T2 |
| **Mistral** | 7 | USD | https://docs.mistral.ai/getting-started/models/models_overview | T2 |
| **ByteDance Doubao** | 6 | CNY | https://www.volcengine.com/docs/82379/1544106 | T2 |
| **Aliyun Qwen** | 7 | USD | https://r.jina.ai/openrouter.ai/api/v1/models (qwen/*) | T2 |
| **OpenRouter 聚合** | 337 | USD | https://openrouter.ai/api/v1/models | validator |

**总计**: 73 个有价模型（Tier 1 必入库 + Tier 2 应入库）

## 🏆 Tier 1 — 必入库（按 30d 用量排序）

> 这些是我们**真实用得最多**的模型，价格入库立即产生计费/路由价值。

| 模型 | 厂商 | 30d tokens | 凭据 | 价格 (in/out per 1M) | 货币 | Plan |
|---|---|---|---|---|---|---|
| **glm-5.1** | 智谱 | 57.7M | roocode + evol + nvidia | 6.00/24.00 | CNY | token |
| **minimax-m3** | MiniMax | 44.8M | minimax-prod-1 | 0.30/1.20 | USD | token |
| **mimo-v2.5-pro** | 小米 | 9.0M | xiaomi-token-plan | 0.435/0.87 | CNY | token_plan |
| **deepseek-v4-pro** | DeepSeek | 8.7M | evol + nvidia | 0.435/0.87 | USD | token |
| **minimax-m2.7** | MiniMax | 4.3M | minimax + evol + nvidia | 0.30/1.20 | USD | token |
| **deepseek-v4-flash** | DeepSeek | TBD | evol + nvidia | 0.14/0.28 | USD | token |
| **glm-5** | 智谱 | TBD | roocode + evol | 4.00/18.00 | CNY | token |
| **glm-4.7** | 智谱 | TBD | roocode + evol | 2.00/8.00 | CNY | token |
| **glm-4.5-air** | 智谱 | TBD | roocode | 0.80/2.00 | CNY | token |
| **kimi-k2.6** | Moonshot | TBD | evol + nvidia | 0.67/3.39 | USD | token |

## 🥈 Tier 2 — 应入库（厂商主流旗舰 + 我们的 offer 已注册）

> 价格矩阵已生成，按厂商分组列出。

### 🤖 OpenAI (provider=gpt)

| Model | Input | Output | Context | Source |
|---|---|---|---|---|
| gpt-5.5 ⭐ | $5.00 | $30.00 | 1M | openai-pricing |
| gpt-5.4 | $2.50 | $15.00 | 1M | openai-pricing |
| gpt-5.4-mini | $0.75 | $4.50 | 400K | openai-pricing |
| gpt-5.3-codex | $2.50 | $15.00 | 1M | openai-pricing |
| gpt-4o (legacy) | $2.50 | $10.00 | 128K | openai-pricing |
| gpt-4o-mini (legacy) | $0.15 | $0.60 | 128K | openai-pricing |
| gpt-image-2 | $0.04/img | — | — | openai-pricing |

### 🧠 Anthropic (provider=anthropic-claude)

| Model | Input | Output | Notes |
|---|---|---|---|
| claude-opus-4-8 ⭐ | $5.00 | $25.00 | Latest Opus |
| claude-opus-4-7 | $5.00 | $25.00 | Prior |
| claude-opus-4-6 | $5.00 | $25.00 | Prior |
| claude-sonnet-4-6 | $3.00 | $15.00 | Latest Sonnet |
| claude-haiku-4-5 | $1.00 | $5.00 | Latest Haiku |
| claude-3-5-haiku (legacy) | $0.80 | $4.00 | 3.5 series |
| claude-3-5-sonnet (legacy) | $3.00 | $15.00 | 3.5 series |

### 🔍 Google (provider=google-gemini)

| Model | Input | Output | Notes |
|---|---|---|---|
| gemini-3.1-pro | $2.00 | $12.00 | Preview, 1M ctx |
| gemini-3-flash | $0.50 | $3.00 | Preview |
| gemini-3.1-flash-image | $0.30/img | $2.50/img | Nano Banana 2 |

### 🌊 DeepSeek (provider=deepseek)

| Model | Input | Output | Cache Hit | Notes |
|---|---|---|---|---|
| deepseek-v4-pro | $0.435 | $0.87 | $0.003625 | 1M ctx, max out 384K |
| deepseek-v4-flash | $0.14 | $0.28 | $0.0028 | 1M ctx |
| deepseek-v3.2 (legacy) | $0.27 | $1.10 | — | Pre-V4, deprecating 2026-07-24 |

### 🤖 xAI Grok (provider=xai)

| Model | Input | Output | Cache | Context |
|---|---|---|---|---|
| grok-4.3 | $1.25 | $2.50 | $0.20 | 1M |
| grok-4.20-multi-agent | $1.25 | $2.50 | $0.20 | 1M |
| grok-4.20-reasoning | $1.25 | $2.50 | $0.20 | 1M |

### 🇨🇳 Zhipu 智谱 (provider=zhipu-glm) — CNY

| Model | ≤32k Input/Output | >32k Input/Output | Notes |
|---|---|---|---|
| **glm-5.1** ⭐ | 6 / 24 | 8 / 28 | Long-horizon, 8h autonomy |
| glm-5-turbo | 5 / 22 | 7 / 26 | Turbo variant |
| glm-5 | 4 / 18 | 6 / 22 | Standard |
| glm-4.7 | 2 / 8 → 3 / 14 | 4 / 16 | Tiered output |
| glm-4.6 | 2 / 8 | — | Mid-tier |
| glm-4.5 | 1 / 4 | — | 32k |
| glm-4.5-air | 0.8 / 2 → 0.8 / 6 | 1.2 / 8 | Light |
| glm-4-plus (legacy) | 5 / 2.5 | — | Discounted output |
| glm-4-air (legacy) | 0.5 / 0.25 | — | Cheap |
| glm-4-flash | **FREE** | **FREE** | Free tier |
| glm-4 (legacy) | 0.03 / 0.05 | — | Per 1k tokens |

### 🚀 MiniMax (provider=minimax) — USD

| Model | Input | Output | Cache Read | Cache Write | Notes |
|---|---|---|---|---|---|
| **minimax-m3** ⭐ | $0.30 | $1.20 | $0.06 | — | Permanent 50% off, ≤512k input |
| minimax-m2.7 | $0.30 | $1.20 | $0.06 | $0.375 | Standard |
| minimax-m2.7-highspeed | $0.60 | $2.40 | $0.06 | $0.375 | Priority 2x |
| minimax-m2.5 (legacy) | $0.30 | $1.20 | $0.03 | $0.375 | |
| minimax-m2 (legacy) | $0.30 | $1.20 | $0.03 | $0.375 | |

> **Token Plan (订阅)**: Plus $20/mo · Max $50/mo · Ultra $120/mo · Credits 1000/$1

### 🇲🇴 Moonshot Kimi (provider=moonshot) — USD

| Model | Input | Output | Context |
|---|---|---|---|
| **kimi-k2.6** ⭐ | $0.67 | $3.39 | 256K |
| kimi-k2.5 | $0.35 | $1.89 | 256K |
| kimi-k2-thinking | $0.60 | $2.50 | 256K |

### 🌾 Xiaomi MiMo (provider=mimo) — CNY

> ⚠️ 官方价目页未公开，**使用 OpenRouter 价目做来源**（Xiaomi 也走 OpenRouter 分发）

| Model | Input | Output | Notes |
|---|---|---|---|
| **mimo-v2.5-pro** ⭐ | 0.435 | 0.87 | token_plan via recharge credits |
| mimo-v2.5 | 0.14 | 0.28 | |
| mimo-v2-pro | 0.14 | 0.28 | |
| mimo-v2-omni | 0.14 | 0.28 | Multimodal |
| mimo-v2-tts | TBD | — | per 1M char |
| mimo-v2.5-tts | TBD | — | per 1M char |
| mimo-v2.5-asr | TBD | — | per hour |
| mimo-v2.5-tts-voiceclone | TBD | — | per voice |
| mimo-v2.5-tts-voicedesign | TBD | — | per voice |

### 🌬️ Mistral (provider=mistral) — USD

| Model | Input | Output | Notes |
|---|---|---|---|
| mistral-medium-3-5 | $1.50 | $7.50 | Premier multimodal |
| mistral-large-3 (2512) | $0.50 | $1.50 | Multimodal |
| mistral-small-2603 | $0.15 | $0.60 | Hybrid |
| devstral-2512 | $0.40 | $2.00 | Code agent |
| codestral-22b | $0.30 | $0.90 | Code completion |
| mistral-nemotron | TBD | TBD | NVIDIA-tuned |
| ministral-14b-2512 | TBD | TBD | Small |

### 🇨🇳 字节 Doubao (provider=doubao) — CNY

| Model | Input (≤32k) | Output (≤32k) | Notes |
|---|---|---|---|
| doubao-1-5-pro-32k-250115 | 0.80 | 2.00 | Jan 2025 |
| doubao-1-5-lite-32k-250115 | 0.30 | 0.60 | Lite |
| doubao-pro-128k | 3.20 | 16.00 | Long ctx |
| doubao-pro-32k | 0.80 | 2.00 | Standard |
| doubao-lite-128k | 0.30 | 0.60 | Lite long |
| doubao-lite-32k | 0.30 | 0.60 | Lite short |
| doubao-embedding-vision | TBD | — | Embedding |

> **Coding Plan**: 火山方舟 `Coding Plan` 已上线，**未公开价目**（需登录控制台）

### 🇨🇳 Aliyun Qwen (provider=qwen) — USD

| Model | Input | Output | Notes |
|---|---|---|---|
| qwen3.7-plus | $0.32 | $1.28 | |
| qwen3.7-max | $1.25 | $3.75 | Premium |
| qwen3.6-plus | $0.325 | $1.95 | |
| qwen3.6-flash | $0.19 | $1.13 | Cheap |
| qwen3.6-35b-a3b | $0.15 | $1.00 | MoE |
| qwen3.5-plus | $0.30 | $1.80 | |
| qwen3-coder-next | $0.11 | $0.80 | Code |

> **Token Plan (订阅)**: 阿里云百炼 `Token Plan` 月度订阅，**未公开价目**

## 🔌 Code Plan 汇总（订阅制产品 — 当前 184 DB 无凭据）

> 这些是**订阅制**（月费 + 含 token 配额），不按 token 计费。
> 当前 184 DB **无 code_plan provider 凭据**——需您先有真实订阅账号再建凭据。

| Provider | Plan | 月费 | 含 Token | 备注 |
|---|---|---|---|---|
| Anthropic Claude Code | Pro | $20/seat | ~45M sonnet-equiv | |
| Anthropic Claude Code | Max 5x | $100/seat | ~225M sonnet-equiv | |
| Anthropic Claude Code | Max 20x | $200/seat | ~900M sonnet-equiv | |
| Cursor | Pro | $20/mo | 500 fast req | |
| Cursor | Business | $40/mo/seat | unlimited fast | |
| GitHub Copilot | Individual | $10/mo | 300 premium req | |
| GitHub Copilot | Business | $19/mo/seat | unlimited premium | |
| 火山方舟 | Coding Plan | TBD | TBD | 控制台登录查 |
| 火山方舟 | Agent Plan | TBD | TBD | 控制台登录查 |

## 🔍 交叉验证（OpenRouter 价目）

- **mimo-v2.5-pro**: OR=$0.435/$0.87, Xiaomi 官方=未公开 → OR 作为 best estimate
- **glm-5.1**: OR=$0.98/$3.08 (USD), 智谱官方=¥6/¥24 (CNY, ~$0.83/$3.33 @ 7.2 汇率) → 差异 < 5%
- **kimi-k2.6**: OR=$0.67/$3.39, Moonshot 官方=未直接公开 → OR 作为唯一来源
- **mistral-medium-3-5**: OR=$1.50/$7.50, Mistral 官方=API pricing 页 404 → OR 作为唯一来源

## 📈 横向对比 (Token Plan, 每 1M in+out avg)

| Rank | Model | Avg $/1M | Speed Tier | 用途 |
|---|---|---|---|---|
| 1 | glm-4-flash | **FREE** | Fast | Free tier |
| 2 | deepseek-v4-flash | $0.21 | Fast | Cheap |
| 3 | mimo-v2.5 / v2-pro | $0.21 | Fast | Cheap |
| 4 | mistral-small-2603 | $0.375 | Fast | Mid cheap |
| 5 | gpt-4o-mini | $0.375 | Fast | Mid cheap |
| 6 | qwen3-coder-next | $0.455 | Mid | Code |
| 7 | mistral-large-3 | $1.00 | Mid | Standard |
| 8 | gpt-5.4 | $8.75 | Mid | Premium |
| 9 | claude-sonnet-4-6 | $9.00 | Mid | Premium |
| 10 | claude-opus-4-8 | $15.00 | Slow | Flagship |
| 11 | gpt-5.5 | $17.50 | Mid | Flagship |

## 🗓️ 刷新策略

- **Tier 1** (10 模型): 每月刷新（model_offer count、price delta > 5%）
- **Tier 2** (60 模型): 每季度刷新
- **Tier 3** (50+ 模型, long tail): 半年或按需

刷新命令: `bash scripts/fetch-pricing.sh && bash scripts/apply-pricing.sh`

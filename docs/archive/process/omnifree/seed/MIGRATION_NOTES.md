# OmniRoute → free_resource_catalog 迁移说明

本目录的 `free_resource_catalog.json` 由 OmniRoute 的 `freeModelCatalog.data.ts`
(2026-07-22 策展版本) 自动转换而来。本文档记录字段映射策略与数据口径，
供后续刷新或审计时参考。

## 源数据

- **上游**: `~/workspace/ai/OmniRoute/open-sse/config/freeModelCatalog.data.ts`
- **策展日期**: `FREE_CATALOG_CURATED_AT = "2026-07-22"`（50-agent web research + 对抗性验证）
- **条目数**: 523 条 per-model 记录（OmniRoute 原始 525，去重同名 model 后 523）

## 字段映射

| OmniRoute (TS)          | Gateway (JSON / DB 列)   | 说明 |
|-------------------------|--------------------------|------|
| `provider`              | `provider_code`          | 原样保留 OmniRoute provider id（`free_resource_catalog.provider_code` 是 TEXT 非 FK） |
| `modelId`               | `model_id`               | 原样保留上游 model id |
| `displayName`           | `display_name` / `_en`   | 同时填两列（中英未区分，均用 OmniRoute 的英文 display） |
| `monthlyTokens`         | `monthly_tokens`         | 直接映射 |
| `creditTokens`          | `credit_tokens`          | 直接映射 |
| `freeType`              | `free_type`              | 词汇完全一致（7 值 CHECK） |
| `poolKey` / `null`      | `pool_key`               | null → 空串/NULL |
| `tos`                   | `tos_verdict`            | 词汇完全一致（5 值 CHECK） |
| `trainsOnPrompts?`      | `trains_on_prompts`      | 默认 false；kilo-gateway 等 13 条为 true |

### `recurring-daily` 的 daily/monthly 换算

OmniRoute 在 `recurring-daily` 类型上把 **daily×30** 存进 `monthlyTokens`。
本网关的 `free_resource_catalog` 同时有 `daily_tokens` 和 `monthly_tokens` 两列，
迁移策略：

- `daily_tokens = monthlyTokens / 30`（仅 `recurring-daily` 且 monthly>0 时）
- `monthly_tokens` 保留原值（pool dedup SQL `fn_compute_deduped_quota` 取 MAX，不影响）

这与现有手工 seed（如 groq `daily_tokens: 14400000`）的口径一致。

### `discontinued` 处理

`free_type='discontinued'` 的条目（pollinations gemini/claude/midijourney 等 7 条）
`enabled` 设为 `false`，`pool_key` 设为空（NULL）。其余类型默认 `enabled=true`。

### `constraints_json` 口径

转换器按 `free_type` 填入最小 window 提示：

| free_type              | constraints_json              |
|------------------------|-------------------------------|
| recurring-daily        | `{"window":"UTC-day"}`        |
| recurring-monthly      | `{"window":"calendar-month"}` |
| recurring-credit       | `{"window":"one-time"}`       |
| one-time-initial       | `{"window":"one-time"}`       |
| recurring-uncapped     | `{"window":"uncapped"}`       |
| keyless                | `{"window":"uncapped"}`       |
| discontinued           | `{"window":"discontinued"}`   |

## ToS 裁决口径

采用 **per-model `tos` 值**（比 OmniRoute 的 provider 级 `FREE_TIER_TOS` map 更准）。
分布（523 条）：

| tos_verdict | 条数 | 含义 |
|-------------|------|------|
| ok          | 49   | ToS 明确允许或沉默 |
| caution     | 338  | 灰区，社区广泛使用 |
| ambiguous   | 38   | 无明确条款 |
| avoid       | 93   | ToS 明确禁止代理/自动化（OpenAI/Anthropic 官方、多数 web-cookie 逆向站） |
| unknown     | 5    | 未审查 |

auto-combo 模板默认 `tos_filter: ["ok","caution"]`，因此 `avoid`/`ambiguous`/`unknown`
不会进入 `auto/*` 路由（除非模板显式放宽）。

## pool_key 去重语义

共享同一配额池的多个 model 共用 `pool_key`。SQL 函数
`fn_compute_deduped_quota` 在池内取 `MAX(monthly_tokens)`/`MAX(daily_tokens)`，
避免 N 倍虚算（如 OpenRouter 所有 `:free` model 共享一个每日池）。

典型共享池：`openrouter-free`、`groq`、`zhipu-flash-free`、`kilo-gateway-free`、
`pollinations`、`cloudflare-ai` 等。

## 刷新流程

```bash
# 1. 重新跑转换器（一次性脚本，不入仓库核心；见本 commit 的生成过程）
#    输入: OmniRoute 的 freeModelCatalog.data.ts
#    输出: free_resource_catalog.json

# 2. 校验契约
go test ./cmd/seed-free-resources/ -run TestBundledSeedDataContract

# 3. dry-run 导入
go run ./cmd/seed-free-resources \
  --db-url "$DATABASE_URL" \
  --catalog docs/omnifree/seed/free_resource_catalog.json \
  --keyless docs/omnifree/seed/keyless_providers.json \
  --templates docs/omnifree/seed/auto_combo_templates.json \
  --dry-run

# 4. 正式导入（幂等 upsert）
#    去掉 --dry-run

# 5. 核对分布
psql "$DATABASE_URL" -c "SELECT tos_verdict, free_type, count(*) FROM free_resource_catalog GROUP BY 1,2 ORDER BY 1,2;"
```

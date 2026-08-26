# Popular Models Follow-up Design

> **2026-08-26 17:16 audit 修正**：所有 AC 已被 commit `0216557f4` + `749e5df0d` 落地。本文档保留为设计原档。

## Inputs
- Authenticated tenant scope from `EffectiveTenantIDAll(r)`.
- Successful telemetry entry tenant/model fields.
- Optional `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS` environment value.
- DB terminal request status.

## Outputs
- Picker `popular` entries never mix tenant-admin recent or usage data across tenants.
- Redis uses `llmgw:routing:recently_used_models:<tenant>` keys.
- A bounded window controls hot-table usage lookup.
- `llmgw_live_stream_tile_overlay_db_lookup_total{outcome}` exposes fixed tile outcomes.

## Acceptance Criteria
1. Tenant A ZSET entries are invisible to tenant B reads. ✅ (`0216557f4`)
2. Tenant-scoped SQL contains `rl.tenant_id = $2`; all-tenant admin SQL remains explicit. ✅ (`0216557f4`)
3. Policy entries stay first; enough lower-priority entries skip SQL fallback. ✅ (`0216557f4`)
4. Valid positive hour overrides apply; invalid and non-positive values use seven days. ✅ (`0216557f4`)
5. Success, failure, locked, and unknown overlay outcomes increment closed metric labels. ✅ (`749e5df0d`)
6. No raw tenant identifier is included in metric labels. ✅ (`749e5df0d`)

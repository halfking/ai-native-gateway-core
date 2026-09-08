# 免费资源自动发现 (FreeDiscovery) — 配置与使用说明

> 版本: v1.0 (MVP) · 日期: 2026-09-09 · 迁移: `sql/migrations/084-freediscovery-schema.sql`

## 1. 功能概述

借鉴 Orbi 的供应商模板能力 (`templates/pi-providers/*.json`), 为网关增加
**免费资源自动获取**能力: 配置供应商模板 → 一键扫描上游模型列表 →
ToS 合规初判 → 人工审查 → 批量导入免费资源池 (`free_resource_catalog`),
接入既有 OmniFree 配额追踪与路由体系.

## 2. 部署前提

### 2.1 数据库迁移

```bash
# 应用迁移 (幂等, 可重复执行)
psql "$DATABASE_URL" -f sql/migrations/084-freediscovery-schema.sql

# 回滚 (可选; 不删除 075 共享函数, 不影响 OmniFree)
psql "$DATABASE_URL" -f sql/migrations/084-freediscovery-schema.down.sql
```

创建 3 张新表 (`provider_templates` / `discovery_tasks` / `discovery_results`,
均已启用 RLS) 并给 `free_resource_catalog` 追加 4 列.

### 2.2 供应商 API Key

按需在网关进程环境注入 (模板通过 `$VAR` 引用, **Key 本身不入库**):

```bash
# .env 或 systemd EnvironmentFile
GROQ_API_KEY=gsk_xxx            # Groq 免费层
OPENROUTER_API_KEY=sk-or-xxx    # OpenRouter :free 模型
GOOGLE_API_KEY=xxx              # Google AI Studio (预设已含, 扫描走 openai 兼容层)
SILICONFLOW_API_KEY=xxx         # SiliconFlow
ZHIPU_API_KEY=xxx               # 智谱 BigModel
```

密文落库路径 (可选): 若模板创建请求携带明文 `api_key`, 需已配置
credential keyring (`KEYRING_JSON` / `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` /
`SECRET_KEY`, 与凭据加密共用同一套密钥环). 无 keyring 时携带明文创建会
**fail closed** 并提示改用 `api_key_env`.

### 2.3 网关重启

`cmd/gateway` 启动时经 `adminHandler.SetFreeDiscovery(dbConn.Stdlib(), keyring)`
注入依赖; no-DB 模式下相关路由返回 503 (与既有 no-DB 行为一致).

## 3. API 使用

所有端点需要 admin Bearer token (与 `/api/free-pool/*` 同级鉴权);
写操作要求 super_admin / admin_key; 租户隔离走 RLS
(tenant_admin 只见自有租户数据, super_admin/legacy 落 `default` 租户).

### 3.1 查看内置预设

```bash
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:8081/api/free-discovery/templates/presets
```

### 3.2 创建模板 (手动)

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  http://localhost:8081/api/free-discovery/templates \
  -d '{
    "provider_code": "groq",
    "display_name": "Groq Cloud (Free Tier)",
    "base_url": "https://api.groq.com/openai/v1",
    "api_key_env": "$GROQ_API_KEY",
    "models_endpoint": "/models",
    "tos_verdict": "caution"
  }'
```

### 3.3 导入 Orbi 模板 (整体文件直传)

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  http://localhost:8081/api/free-discovery/templates/import-orbi \
  --data-binary @/path/to/orbi/templates/pi-providers/groq.json
# → {"created":1,"failed":0,"errors":[]}
```

Orbi 模板的 `baseUrl` / `api` / `apiKey`("$VAR" 引用) 字段被直接映射;
单个 provider 失败不阻断其余导入.

### 3.4 触发发现

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  http://localhost:8081/api/free-discovery/scan \
  -d '{"template_id": 1}'
# 返回任务体; 扫描失败时任务状态=failed 且 error_message 带原因 (仍返回 200)
```

### 3.5 审查结果

```bash
# 待审查结果 (默认 status=pending)
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:8081/api/free-discovery/tasks/101/results

# 全量 (含已导入/冲突)
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8081/api/free-discovery/tasks/101/results?status=all"
```

每条结果带 `tos_verdict` (ok/caution/ambiguous/avoid) 与配额估算;
`tos_verdict=avoid` 的条目导入后自动为 disabled 状态.

### 3.6 批量导入

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  http://localhost:8081/api/free-discovery/import \
  -d '{
    "task_id": 101,
    "result_ids": [201, 202],      # 省略 = 导入该任务全部 pending
    "conflict_policy": "skip"      # skip | overwrite | merge
  }'
# → {"imported":2,"skipped":0,"conflicted":0,"failed":0}
```

冲突策略 (目标 = `free_resource_catalog` 已有同 provider+model+tenant 行):

| 策略 | 行为 |
|------|------|
| `skip` (默认) | 保留现有条目, 结果标记 conflict |
| `overwrite` | 用发现结果覆盖 display/free_type/配额/ToS, 并重新启用 |
| `merge` | 仅补充现有行的空字段 (NULL/0/unknown), 不覆盖已有值 |

导入全程单事务: 任一失败整体回滚; 重复扫描不会重置已审查状态
(ON CONFLICT 保留 import_status / imported_at).

## 4. 端点速查

| 方法 + 路径 | 说明 |
|---|---|
| `GET /api/free-discovery/templates` | 模板列表 (`?enabled=true` 过滤) |
| `POST /api/free-discovery/templates` | 创建模板 |
| `GET /api/free-discovery/templates/presets` | 内置预设 (5 家) |
| `POST /api/free-discovery/templates/import-orbi` | 导入 Orbi 模板文件 |
| `GET/PUT/PATCH/DELETE /api/free-discovery/templates/{id}` | 单模板 CRUD |
| `POST /api/free-discovery/scan` | 触发发现 `{template_id}` |
| `GET /api/free-discovery/tasks` | 任务列表 (`?limit=50`) |
| `GET /api/free-discovery/tasks/{id}` | 任务详情 |
| `GET /api/free-discovery/tasks/{id}/results` | 发现结果 (`?status=pending\|all`) |
| `POST /api/free-discovery/import` | 批量导入免费资源池 |

## 5. ToS 初判规则

保守策略, 分级降级:

1. **avoid 关键词** (discontinued/deprecated) → 恒为 avoid (最高优先);
2. **caution 关键词** (vision/preview/experimental/beta) → 模板 ok 也降级 caution;
3. 模板级 `tos_verdict` (人工审查结论) 优先于提供商预设;
4. `:free` 官方标记 → ok, 但提供商预设为 avoid/caution 时仍取保守值;
5. 无任何命中 → `ambiguous` (需人工审查), **绝不猜 ok**.

## 6. 测试与验证

```bash
# 领域包 (61+ 用例: sqlmock/httptest/迁移结构)
go test ./domains/freediscovery/ -short

# Admin API 层
go test ./admin/ -short -run "TestFreeDiscovery|TestFDStatusFor"

# 整体构建
go build ./...
```

## 7. 已知边界 (MVP)

- Google/Anthropic 协议扫描暂复用 OpenAI 兼容形态 (`fallbackScanners`),
  真协议适配通过 `SetScanner` 替换;
- 定时自动扫描 (scheduled trigger) 预留了 `trigger_type` 字段, 调度器后续接入;
- Prometheus 指标与 SSE 推送未接入 (复用 freeresource 的 sink 模式可后续加);
- UI 页面待接入 (API 已就绪, 可先用 curl 完成 MVP 流程).

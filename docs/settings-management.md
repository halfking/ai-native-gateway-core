# 系统设置管理使用手册 (settings-management)

> 版本: v1.0  
> 日期: 2026-06-20  
> 范围: llm-gateway-go 平台级 + 租户级运行时设置

## 1. 简介

系统设置管理（settings-management）将 llm-gateway-go 中分散在 62 个
`LLM_GATEWAY_*` 环境变量和 1 张 `app_settings` 表的设置统一管理起来，
提供可视化界面、可即时修改、可回滚、有审计日志。

## 2. 设计原则

| 决策 | 方案 | 说明 |
|------|------|------|
| **存储优先级** | Q1: B — DB > env > default | DB 覆盖 env；env 是兜底；默认值是底层 |
| **生效时机** | Q2: A — 立即生效 | 写入 DB 后立即被所有新请求看到 |
| **权限模型** | Q3: B — 平台级 + 租户级 | 大多数是平台级，rate_limit 是租户级 |
| **app_settings 字段** | Q4: C — 迁移并清理 | rate_limit_* 从 app_settings 迁移到 settings_kv |
| **历史保留** | Q6: C — 7 天 | bg worker 每天清理 7 天前的 audit |

## 3. 架构

```
┌─────────────────────────────────────────────────────┐
│  Frontend (Vue 3) — SettingsView.vue (暗色主题)     │
│  路径: /admin/settings                              │
└─────────────────────────────────────────────────────┘
                    ↑ HTTP (JSON, JWT)
┌─────────────────────────────────────────────────────┐
│  Admin API — 8 端点                                │
│  /api/admin/settings/{key}[/history|/rollback]     │
│  /api/admin/tenants/{tid}/settings/{key}[/...]     │
└─────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────┐
│  settings/registry.go (Go)                          │
│  EffectiveValue(scope, key, tenantID)              │
│  优先级: DB > env > default                          │
└─────────────────────────────────────────────────────┘
        ↓                  ↓                 ↓
┌──────────┐      ┌──────────────────┐   ┌──────────┐
│settings_ │      │ settings_kv (DB) │   │ env-var  │
│  kv (DB) │      │ tenant_settings_ │   │ LLM_     │
│(platform)│      │      kv (DB)     │   │ GATEWAY_ │
└──────────┘      └──────────────────┘   └──────────┘
```

## 4. Phase 1 已迁移的设置（7 个）

### 4.1 平台级（5 个）

| Key | 类型 | 默认值 | 范围 | 危险 | 热重载 |
|-----|------|--------|------|------|--------|
| `compression.mode` | enum | `"on_4xx"` | off/auto_threshold/on_4xx | 🟡 注意 | ✅ |
| `compression.window_fraction` | float | 0.8 | (0, 1] | 🟡 注意 | ✅ |
| `session.ttl_hours` | int | 168 | [1, 8760] | 🟠 警告 | ✅ |
| `enable_disguise` | bool | false | — | 🔴 危险 | ✅ |
| `stream_retry_threshold` | int | 5 | [0, 100] | 🟡 注意 | ✅ |
| `default.rate_limit_rpm` | int | 60 | [0, 1000000] | 🟠 警告 | ✅ |
| `default.rate_limit_concurrent` | int | 20 | [1, 10000] | 🟠 警告 | ✅ |
| `default.rate_limit_tpm` | int | 0 | [0, 100000000] | 🟡 注意 | ✅ |

### 4.2 租户级（2 个）

| Key | 类型 | 默认值 | 范围 | 危险 | 热重载 |
|-----|------|--------|------|------|--------|
| `rate_limit_rpm` (per-tenant) | int | 0 | [0, 1000000] | 🟠 警告 | ✅ |
| `rate_limit_concurrent` (per-tenant) | int | 0 | [0, 10000] | 🟠 警告 | ✅ |

## 5. 部署步骤

### 5.1 应用 DB Migration

```bash
# SSH to 184
export SSHPASS='Kaixuan2025&9900#'
sshpass -e ssh root@14.103.112.184

# 1. 应用 migration 022 (settings_kv + tenant_settings_kv)
PGPASSWORD=184_stock_pass_change_me psql -h 172.31.0.4 -U stockuser -d llm_gateway \
  -f /opt/llm-gateway-go/db/migrations/022_settings_kv.sql

# 2. 应用 migration 023 (settings_audit)
PGPASSWORD=184_stock_pass_change_me psql -h 172.31.0.4 -U stockuser -d llm_gateway \
  -f /opt/llm-gateway-go/db/migrations/023_settings_audit.sql

# 3. 验证表结构
PGPASSWORD=184_stock_pass_change_me psql -h 172.31.0.4 -U stockuser -d llm_gateway -c "\d settings_kv"
PGPASSWORD=184_stock_pass_change_me psql -h 172.31.0.4 -U stockuser -d llm_gateway -c "\d tenant_settings_kv"
PGPASSWORD=184_stock_pass_change_me psql -h 172.31.0.4 -U stockuser -d llm_gateway -c "\d settings_audit"
```

### 5.2 部署 llm-gateway-go

```bash
cd /Users/xutaohuang/workspace/official-deploy
K8S_SSH_PASSWORD='Kaixuan2025&9900#' bash scripts/llm-gateway-go-184-deploy.sh
```

### 5.3 验证服务健康

```bash
curl -sk https://llmgo.kxpms.cn/healthz
# 期望: {"status":"ok",...}
```

## 6. API 使用

### 6.1 列出所有设置

```bash
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://llmgo.kxpms.cn/api/admin/settings | jq .
```

返回示例：
```json
{
  "items": [
    {
      "key": "compression.mode",
      "env_name": "LLM_GATEWAY_COMPRESSION_MODE",
      "type": "enum",
      "scope": "platform",
      "category": "compression",
      "default": "on_4xx",
      "value": "on_4xx",
      "source": "env",
      "options": ["off", "auto_threshold", "on_4xx"],
      "description": "压缩模式",
      "danger_level": 1,
      "hot_reload": true,
      "observability": "/api/admin/compression/stats"
    },
    ...
  ]
}
```

### 6.2 查看单个设置

```bash
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://llmgo.kxpms.cn/api/admin/settings/compression.mode | jq .
```

### 6.3 修改设置（立即生效）

```bash
# 修改 compression_mode 为 off
curl -sk -X PUT -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"value": "off"}' \
  https://llmgo.kxpms.cn/api/admin/settings/compression.mode | jq .

# 期望: 立即生效
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://llmgo.kxpms.cn/api/admin/settings/compression.mode | jq .value
# "off"
```

### 6.4 一键回滚

```bash
# 回滚 compression_mode 到上次的值
curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
  https://llmgo.kxpms.cn/api/admin/settings/compression.mode/rollback | jq .
```

### 6.5 查看修改历史

```bash
# 查看 compression.mode 最近 50 条修改记录
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://llmgo.kxpms.cn/api/admin/settings/compression.mode/history | jq .
```

### 6.6 租户级设置（需要 super_admin）

```bash
# 设置 tenant=tenant_abc 的 rate_limit_rpm = 200
curl -sk -X PUT -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"value": 200}' \
  "https://llmgo.kxpms.cn/api/admin/tenants/tenant_abc/settings/rate_limit_rpm" | jq .
```

## 7. UI 使用（暗色主题）

### 7.1 访问

URL: `https://llmgo.kxpms.cn/admin/settings`

### 7.2 界面布局

- **左侧**：分类栏（全部/压缩/限流/超时/路由/会话/安全/熔断/其他）
- **中间**：当前分类的设置列表（卡片式表格）
- **右侧**：选中设置的详情面板 + 编辑器

### 7.3 操作流程

1. **选择设置**：点击中间表格的某一行
2. **查看详情**：右侧显示 spec 元数据 + 当前值 + 默认值 + 选项 + 危险级别 + 观察点
3. **修改值**：编辑底部 JSON textarea
4. **保存**：点击「保存」按钮（立即生效）
5. **回滚**：点击「回滚」按钮（恢复 prev_value）
6. **历史**：通过 API 或未来 UI 查看

### 7.4 颜色与图标

- 绿 (`#34d399`)：src=db，已从数据库加载
- 蓝 (`#818cf8`)：src=env，从环境变量加载
- 灰 (`#8b949e`)：src=default，使用默认值
- 🟡 注意：danger_level=1
- 🟠 警告：danger_level=2
- 🔴 危险：danger_level=3

## 8. 权限矩阵

| 操作 | 平台级 | 租户级 |
|------|--------|--------|
| 查看 | admin / super_admin / admin_key | admin（看自己）/ super_admin（看任意） |
| 修改 (Safe/Warning) | admin | super_admin |
| 修改 (Dangerous/Breaking) | super_admin | super_admin |
| 回滚 | 同上 | super_admin |
| 删除 | super_admin | super_admin |

## 9. 故障恢复

### 9.1 改了错误的设置

- **软撤销（5 秒内）**：在 UI 上点「回滚」，或调 `POST /api/admin/settings/{key}/rollback`
- **硬撤销**：直接 `DELETE FROM settings_kv WHERE key = 'xxx'`（下次请求回退到 env）

### 9.2 DB 挂了

- `settings.Global.EffectiveValue` 会跳过 DB backend
- 自动回退到 env backend
- 用户无感知

### 9.3 审计日志太多

- bg `SettingsAuditCleaner` 每 24h 清理 7 天前的数据
- 调整 retention：`settings.NewSettingsAuditCleaner(pool).retention = 14*24*time.Hour`

## 10. 性能考虑

- **热路径**：`LoadMode()` / `LoadFraction()` 每次启动时缓存到 Compressor/Estimator 实例
- 启动时读一次 → 后续 O(1) 访问
- **不是** 每次请求都查 DB（避免引入 latency）
- **配置变更**：通过新进程启动生效（k8s rollout restart）

## 11. 下一步（Phase 2+）

- 持续迁移剩余 ~55 个 env-var 到 settings
- 给 settings UI 添加实时"影响预览"（不用保存就能看效果）
- 添加 settings diff（git-style 对比两个时间点的配置）
- 集成到 OpenClaw plugins 配置同步

# OmniFree 部署与种子数据指南

本文档说明如何在新环境启用 OmniFree 功能并导入初始数据。

## 前提条件

- PostgreSQL 数据库已初始化（`llm-gateway-go` 的基础表已存在）
- 网关应用已构建（`go build ./cmd/gateway`）

## 1. 启用 OmniFree

在启动网关前设置环境变量：

```bash
export OMNIFREE_ENABLED=true
```

启动网关时，`db.Open()` 会自动调用 `ensureOmniFreeSchema` 创建以下资源：

- **4 张新表**：
  - `free_resource_catalog`: 免费资源目录
  - `free_quota_tracker`: 配额追踪表
  - `auto_combo_templates`: auto/* 虚拟路由模板
  - `keyless_providers`: keyless 提供商配置

- **扩展现有表**：
  - `provider_catalog`: 新增 `has_free_tier`, `free_tier_notes`
  - `credentials`: 新增 `is_free_tier`, `free_quota_limit`

- **RLS 策略**：所有 4 张新表启用行级安全，按 `tenant_id` 隔离
- **触发器**：`updated_at` 自动更新
- **视图**：`v_free_resource_summary` 聚合统计
- **函数**：`fn_compute_deduped_quota`, `fn_quota_preflight_check`

## 2. 导入种子数据

表结构创建后，运行种子数据命令填充 `free_resource_catalog`：

```bash
# 方式 1: 从应用内置 JSON 导入（推荐）
go run cmd/seed-free-resources/main.go \
  --db-url="$DB_URL" \
  --tenant=default

# 方式 2: 从外部 JSON 文件导入
go run cmd/seed-free-resources/main.go \
  --db-url="$DB_URL" \
  --tenant=default \
  --seed-file=./scripts/omnifree/seed/free-resources-2024.json
```

### 环境变量

种子命令支持从环境变量读取数据库 URL：

```bash
export DATABASE_URL="postgres://user:pass@localhost/gateway?sslmode=disable"
go run cmd/seed-free-resources/main.go --tenant=default
```

### 验证导入

```sql
-- 查看已导入的免费资源数量
SELECT tenant_id, total_resources, enabled_resources, provider_count
FROM v_free_resource_summary;

-- 检查具体条目
SELECT provider_code, model_id, free_type, tos_verdict, enabled
FROM free_resource_catalog
WHERE tenant_id = 'default'
ORDER BY provider_code, model_id
LIMIT 20;
```

## 3. 配置 auto/* 模板（可选）

如果需要自定义虚拟路由模板，插入 `auto_combo_templates`：

```sql
INSERT INTO auto_combo_templates (
    template_key, display_name, tos_filter, sort_by, tenant_id
) VALUES (
    'auto/best-free', 'Best Free Models', ARRAY['ok', 'caution'], 'health', 'default'
) ON CONFLICT (tenant_id, template_key) DO NOTHING;
```

内置模板（`auto/free`, `auto/coding:free`, `auto/reasoning:free` 等）由 `autocombo.Resolver.getBuiltinTemplate` 提供，无需数据库配置。

## 4. 启动后台 worker

OmniFree 包含两个后台清理 worker：

- **freequotareset**: 每 5 分钟扫描过期的 `is_exhausted=true` 行并重置
- **freequotacleanup**: 每 24 小时清理 7 天前的窗口行

这两个 worker 在 `main.go` 中已自动启动（当 `OMNIFREE_ENABLED=true` 时），无需额外配置。

## 5. 测试 auto/* 请求

启动网关后，向 `/v1/chat/completions` 发送请求：

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto/free",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

网关会：
1. 调用 `autocombo.Resolver.Resolve` 解析 `auto/free` 模板
2. 从 `free_resource_catalog` 加载启用的免费资源
3. 通过 `provider.Client.GetCandidates` 获取可执行 `provider.Candidate`
4. 使用 `VirtualFactory.BuildFromCandidates` 过滤/评分/排序
5. 选择最优免费候选并执行请求
6. 调用 `QuotaTracker.Record` 记录消耗（成功/失败均计数）
7. 429 响应时调用 `QuotaTracker.CorrectFromHeaders` 校准限制

## 6. 监控与运维

### 查看配额消耗

```sql
-- 查看今日请求计数
SELECT credential_id, provider_code, model_id,
       request_count, success_count, error_count
FROM free_quota_tracker
WHERE window_type = 'day-1'
  AND window_start <= now()
  AND window_end >= now()
  AND tenant_id = 'default'
ORDER BY request_count DESC
LIMIT 20;
```

### 查看耗尽状态

```sql
-- 列出当前耗尽的凭据
SELECT credential_id, provider_code, model_id,
       exhausted_at, auto_reset_at, last_429_reset_after
FROM free_quota_tracker
WHERE is_exhausted = TRUE
  AND tenant_id = 'default'
ORDER BY exhausted_at DESC;
```

### 手动重置配额

```sql
-- 强制重置某个凭据的耗尽状态（谨慎使用）
UPDATE free_quota_tracker
SET is_exhausted = FALSE,
    exhausted_at = NULL
WHERE credential_id = 123
  AND provider_code = 'openrouter'
  AND model_id = 'openai/gpt-3.5-turbo:free'
  AND window_type = 'day-1'
  AND tenant_id = 'default';
```

## 7. 故障排查

### 问题：网关启动但 OmniFree 表不存在

**原因**：`OMNIFREE_ENABLED` 未设置或 `db.Open()` 失败。

**解决**：
1. 确认环境变量：`echo $OMNIFREE_ENABLED`（应输出 `true`）
2. 检查启动日志：`grep "omnifree schema ensured" gateway.log`
3. 手动执行迁移：`psql $DB_URL < sql/migrations/075-omnifree-schema.sql`

### 问题：auto/* 请求返回 404 model_not_found

**原因**：
- `free_resource_catalog` 为空（未导入种子数据）
- 或者所有免费资源 `enabled=FALSE`
- 或者 `tos_filter` 配置过严（例如只允许 `ok`，但目录里都是 `caution`）

**解决**：
1. 运行种子命令：`go run cmd/seed-free-resources/main.go`
2. 确认目录有启用条目：
   ```sql
   SELECT COUNT(*) FROM free_resource_catalog
   WHERE enabled = TRUE AND tenant_id = 'default';
   ```
3. 检查内置模板的 `ToSFilter`（源码 `autocombo/resolver.go:getBuiltinTemplate`）

### 问题：auto/* 请求总是命中同一个模型

**原因**：配额预检/排序逻辑导致其他候选被过滤。

**解决**：
1. 检查 `free_quota_tracker` 是否有大量耗尽行
2. 调整 `VirtualFactory` 的评分权重（`ScoringWeights`）
3. 临时禁用配额预检：修改 `VirtualFactory.preflightQuota` 返回全部候选（仅调试用）

## 8. 生产部署清单

- [ ] 在预发环境启用 `OMNIFREE_ENABLED=true` 并启动网关
- [ ] 验证 4 张表自动创建（`\dt free_*` 和 `\dt auto_combo_templates keyless_providers`）
- [ ] 运行种子命令导入至少 10 个免费资源
- [ ] 测试 `auto/free` 请求能正常路由
- [ ] 观察 `free_quota_tracker` 记录新增行
- [ ] 模拟 429 响应（例如手动构造 upstream 错误）验证 `CorrectFromHeaders` 生效
- [ ] 等待 5 分钟观察 `freequotareset` worker 日志
- [ ] 等待 24 小时观察 `freequotacleanup` worker 清理旧窗口
- [ ] 在生产环境重复上述步骤

## 9. 回滚方案

如需禁用 OmniFree：

```bash
# 1. 停止网关
systemctl stop llm-gateway

# 2. 取消环境变量
unset OMNIFREE_ENABLED

# 3. （可选）保留表但禁用所有资源
UPDATE free_resource_catalog SET enabled = FALSE;

# 4. （可选）完全移除表
psql $DB_URL <<EOF
DROP TABLE IF EXISTS free_resource_catalog CASCADE;
DROP TABLE IF EXISTS free_quota_tracker CASCADE;
DROP TABLE IF EXISTS auto_combo_templates CASCADE;
DROP TABLE IF EXISTS keyless_providers CASCADE;
DROP VIEW IF EXISTS v_free_resource_summary CASCADE;
DROP FUNCTION IF EXISTS fn_compute_deduped_quota(TEXT) CASCADE;
DROP FUNCTION IF EXISTS fn_quota_preflight_check(BIGINT,TEXT,TEXT,INT,FLOAT,TEXT) CASCADE;
EOF

# 5. 重启网关
systemctl start llm-gateway
```

## 参考文档

- **数据模型**：`docs/omnifree/01-DATA-MODEL.md`
- **配额追踪**：`docs/omnifree/02-QUOTA-TRACKING.md`
- **Auto Combo**：`docs/omnifree/03-AUTO-COMBO.md`
- **集成计划**：`docs/omnifree/INTEGRATION-PLAN.md`
- **迁移 SQL**：`sql/migrations/075-omnifree-schema.sql`
- **种子数据**：`scripts/omnifree/seed/`

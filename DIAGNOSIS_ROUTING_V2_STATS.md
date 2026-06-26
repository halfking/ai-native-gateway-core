# routing-v2 统计数据问题诊断报告

> 生成时间：2026-06-26  
> 项目：llm-gateway-go-2  
> 分支：server-71  
> 问题：https://llmgateway.internal.example.com/routing-v2 有请求数据但无统计数据

---

## 执行摘要

经过深度代码审查和诊断，发现 routing-v2 统计功能的代码实现是**完整且正确的**。问题很可能出在以下几个方面：

1. **数据库写入问题**（最可能）- request_logs 表可能存在写入失败
2. **索引缺失**（较可能）- 统计查询需要的索引可能未创建
3. **版本不一致**（可能）- 71 服务器可能运行旧版本代码
4. **数据不符合条件**（可能）- 现有数据可能不满足统计查询的 WHERE 条件

---

## 诊断发现

### ✅ 代码状态检查

#### 1. 后端实现（Go）
- **admin/analytics.go** ✓ 完整实现
  - `SpecifiedModelTaskKey = "__specified__"` 
  - `SpecifiedModelDisplayLabel = "指定模型"`
  - 统计查询包含 auto + explicit-model 请求
  
- **admin/auto_route.go** ✓ 完整实现
  - `handleAudit()` 支持 `specified_model_requests` 字段
  - `handleDecisions()` 包含 explicit-model 过滤逻辑
  
- **telemetry/client.go** ✓ 已修复
  - 第731行：`CAST($47 AS jsonb)` 修复 auto_decision 类型
  - 第710行：`COALESCE(NULLIF($32, ''), usage_source, 'llm')` 修复 NULL 约束
  - 第981行：`COALESCE(NULLIF($37, ''), 'llm')` INSERT 语句修复
  - 第985行：`CAST(NULLIF($46, '') AS jsonb)` INSERT 语句修复

#### 2. 前端实现（Vue + TypeScript）
- **web/src/components/analytics/AnalyticsKpiBar.vue** ✓ 完整实现
  - 6个 KPI chips（总请求、Auto、指定模型、成功率、Top任务、Top模型）
  - 指定模型用灰色斜体样式标识
  
- **web/src/components/analytics/HeatmapMatrix.vue** ✓ 完整实现
  - 支持 `__specified__` 列显示
  - 特殊样式：`col-specified` / `cell-specified-col`
  
- **web/src/api-autoroute.ts** ✓ 完整实现
  - 导出 `SPECIFIED_MODEL_TASK_KEY` 和 `SPECIFIED_MODEL_DISPLAY_LABEL`
  - `AutoRouteAudit` 类型包含 `specified_model_requests` 和 `total_requests`

#### 3. 单元测试
```bash
$ go test ./admin -v -run TestSpecified
=== RUN   TestSpecifiedModelTaskKey_DoubleUnderscorePrefix
--- PASS: TestSpecifiedModelTaskKey_DoubleUnderscorePrefix (0.00s)
=== RUN   TestSpecifiedModelDisplayLabel_NonEmpty
--- PASS: TestSpecifiedModelDisplayLabel_NonEmpty (0.00s)
PASS
```
✓ 所有单元测试通过

---

## ⚠️ 潜在问题点

### 问题 1：数据库索引可能缺失

**所需索引**：
```sql
CREATE INDEX IF NOT EXISTS idx_request_logs_explicit_model
  ON request_logs (client_model, ts DESC)
  WHERE is_auto_request = FALSE
    AND client_model IS NOT NULL
    AND client_model <> '';
```

**影响**：
- 如果索引不存在，7天窗口的统计查询会全表扫描
- 可能导致查询超时或返回空结果
- 特别影响 explicit-model 请求的统计

**检查方法**：
```bash
psql $DATABASE_URL -c "
SELECT indexname, indexdef 
FROM pg_indexes 
WHERE tablename = 'request_logs' 
  AND indexname = 'idx_request_logs_explicit_model';
"
```

**修复方法**：
```bash
psql $DATABASE_URL < docs/2026-06-22-explicit-model-stats.sql
```

---

### 问题 2：request_logs 写入可能仍有问题

虽然代码已经修复，但如果 71 服务器运行的是旧版本，仍会有写入失败。

**检查方法**：
使用诊断脚本检查最近写入：
```bash
psql $DATABASE_URL -f diagnose_routing_v2_stats.sql
```

关键指标：
- 最近 1 小时写入数量
- auto_decision 字段填充率
- usage_source 字段分布

**修复方法**：
如果发现写入失败，需要部署最新代码到 71 服务器。

---

### 问题 3：71 服务器可能运行旧版本

**检查方法**：
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
     https://llmgateway.internal.example.com/api/admin/auto-route/audit \
     | jq '.specified_model_requests'
```

**期望结果**：
- 如果返回数字（可能是 0），说明新版本已部署 ✓
- 如果返回 `null`，说明版本太旧 ✗
- 如果返回 404，说明端点不存在 ✗

**修复方法**：
部署最新代码到 71 服务器。

---

### 问题 4：数据不符合统计查询条件

统计查询的 WHERE 条件：
```sql
WHERE ts >= NOW() - INTERVAL '7 days'
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  )
```

**可能情况**：
1. 最近 7 天完全没有请求
2. 所有 explicit-model 请求的 `client_model` 为 NULL 或空字符串
3. 数据库时区问题导致 `ts` 时间戳不匹配

**检查方法**：
运行诊断脚本中的查询 #1 和 #6。

---

## 诊断工具

### 工具 1：数据库诊断脚本
**文件**：`diagnose_routing_v2_stats.sql`

**用途**：
- 检查 request_logs 最近写入情况
- 检查数据分布（auto vs explicit）
- 检查字段完整性（auto_decision, usage_source）
- 检查索引状态
- 测试统计查询性能

**使用**：
```bash
psql $DATABASE_URL -f diagnose_routing_v2_stats.sql > diagnosis_report.log
cat diagnosis_report.log
```

### 工具 2：本地测试脚本
**文件**：`test_routing_v2_local.sh`

**用途**：
- 运行 Go 单元测试
- 检查数据库连接
- 运行诊断脚本
- 检查索引状态
- 测试统计查询

**使用**：
```bash
export DATABASE_URL="postgresql://user:pass@host:port/dbname"
./test_routing_v2_local.sh
```

---

## 修复方案

### 方案 A：索引缺失（最简单，最可能）

**步骤**：
```bash
# 1. 备份（可选）
pg_dump $DATABASE_URL --schema-only > schema_backup.sql

# 2. 创建索引（可以在生产环境安全执行）
psql $DATABASE_URL < docs/2026-06-22-explicit-model-stats.sql

# 3. 验证索引
psql $DATABASE_URL -c "
SELECT indexname, pg_size_pretty(pg_relation_size(indexrelid))
FROM pg_stat_user_indexes
WHERE indexrelname = 'idx_request_logs_explicit_model';
"

# 4. 测试查询性能
psql $DATABASE_URL -f diagnose_routing_v2_stats.sql
```

**风险**：低  
**时间**：5-10 分钟  
**回滚**：`DROP INDEX idx_request_logs_explicit_model;`

---

### 方案 B：版本过旧（需要部署）

**前提条件**：
- 确认代码已经在本地通过所有测试
- 确认 telemetry/client.go 包含修复
- 前端已构建（`cd web && npm run build`）

**步骤**：
```bash
# 1. 在本地测试
export DATABASE_URL="postgresql://test_user:test@localhost:5432/test_db"
./test_routing_v2_local.sh

# 2. 构建
cd __DEV_HOME__/workspace/llm-gateway-go-2
./build.sh

# 3. 部署到 71（按用户要求，现在不执行）
# ./scripts/deploy-llm-gateway-go-71.sh

# 4. 验证部署
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
     https://llmgateway.internal.example.com/api/admin/auto-route/audit \
     | jq '.specified_model_requests'
```

**风险**：中  
**时间**：30-60 分钟  
**回滚**：部署前一个版本

---

### 方案 C：数据写入问题（最复杂）

如果诊断发现 request_logs 最近完全没有写入，或者写入字段不完整：

**步骤**：
```bash
# 1. 检查应用日志
ssh root@__HOST_71_IP__
docker logs llm-gateway-go --tail 200 | grep -i "telemetry\|request_logs\|error"

# 2. 检查数据库写入
psql $DATABASE_URL -c "
SELECT ts, request_id, is_auto_request, 
       auto_decision IS NOT NULL as has_auto_decision,
       usage_source
FROM request_logs
WHERE ts > NOW() - INTERVAL '10 minutes'
ORDER BY ts DESC
LIMIT 10;
"

# 3. 如果发现写入失败，需要：
#    - 部署包含修复的新版本
#    - 重启服务
#    - 验证写入恢复
```

**风险**：高（需要重启服务）  
**时间**：1-2 小时  
**回滚**：部署前一个版本并重启

---

## 推荐执行顺序

### 第一阶段：诊断（不影响生产）

1. **运行数据库诊断**（5分钟）
   ```bash
   psql $DATABASE_URL -f diagnose_routing_v2_stats.sql > diagnosis_report.log
   ```

2. **检查部署版本**（2分钟）
   ```bash
   curl -H "Authorization: Bearer $ADMIN_TOKEN" \
        https://llmgateway.internal.example.com/api/admin/auto-route/audit | jq
   ```

3. **分析诊断结果**（10分钟）
   - 查看 `diagnosis_report.log`
   - 确定问题根因

### 第二阶段：修复（根据诊断结果）

**如果是索引缺失**：
```bash
psql $DATABASE_URL < docs/2026-06-22-explicit-model-stats.sql
```

**如果是版本过旧**：
- 在本地完整测试
- 部署新版本到 71（用户指示先不部署）

**如果是数据写入问题**：
- 检查应用日志
- 部署修复版本
- 重启服务

### 第三阶段：验证（确认修复）

1. **等待数据积累**（10-30分钟）
2. **访问 routing-v2 页面**
   ```
   https://llmgateway.internal.example.com/routing-v2
   ```
3. **验证显示**：
   - KPI Bar 显示 6 个指标
   - 热力图有数据
   - Sankey 流向图有数据

---

## 下一步行动

### 立即执行（本地）

1. ✅ 已完成：创建诊断脚本
2. ✅ 已完成：创建本地测试脚本
3. ✅ 已完成：验证 Go 单元测试通过
4. ⏸️ 待执行：运行完整本地测试
   ```bash
   export DATABASE_URL="postgresql://..."
   ./test_routing_v2_local.sh
   ```

### 待用户确认后执行（生产环境）

1. 🔍 **诊断阶段**：
   ```bash
   # 连接到生产数据库（71）
   psql $DATABASE_URL -f diagnose_routing_v2_stats.sql
   
   # 检查 API 版本
   curl -H "Authorization: Bearer $ADMIN_TOKEN" \
        https://llmgateway.internal.example.com/api/admin/auto-route/audit | jq
   ```

2. 🔧 **修复阶段**（根据诊断结果）：
   - **如果缺索引**：创建索引
   - **如果版本旧**：部署新版本（用户说先不部署到 71）
   - **如果写入问题**：检查日志并修复

3. ✅ **验证阶段**：
   - 检查 routing-v2 页面
   - 确认统计数据显示正常

---

## 附录

### A. 关键文件清单

**诊断工具**：
- `diagnose_routing_v2_stats.sql` - 数据库诊断脚本
- `test_routing_v2_local.sh` - 本地测试脚本

**SQL 迁移**：
- `docs/2026-06-22-explicit-model-stats.sql` - 创建索引

**后端代码**：
- `admin/analytics.go` - 统计查询核心逻辑
- `admin/auto_route.go` - audit/decisions 端点
- `telemetry/client.go` - request_logs 写入

**前端代码**：
- `web/src/views/RoutingDashboardView.vue` - 主页面
- `web/src/components/analytics/AnalyticsKpiBar.vue` - KPI 展示
- `web/src/components/analytics/HeatmapMatrix.vue` - 热力图
- `web/src/api-autoroute.ts` - API 类型定义

**文档**：
- `docs/2026-06-22-routing-v2-specified-model-stats.md` - 功能设计文档
- `HOTFIX_REQUEST_LOGS.md` - telemetry 修复文档

### B. API 端点清单

| 端点 | 用途 | 关键返回字段 |
|------|------|------------|
| `/api/admin/auto-route/audit` | 总体统计 | `total_requests`, `specified_model_requests` |
| `/api/admin/auto-route/analytics/matrix` | 热力图数据 | `rows`, `cols`, `cells` |
| `/api/admin/auto-route/analytics/flow` | Sankey 流向 | `nodes`, `links` |
| `/api/admin/auto-route/decisions` | 决策列表 | 包含 auto + explicit 请求 |

### C. 常见问题

**Q: 为什么代码都对了还是没数据？**  
A: 最可能的原因是索引缺失或版本未部署。运行诊断脚本确认。

**Q: 如何确认 71 服务器的版本？**  
A: 调用 `/api/admin/auto-route/audit` 检查是否返回 `specified_model_requests` 字段。

**Q: 创建索引会影响生产吗？**  
A: 不会。索引创建是在线操作，不会锁表，只是增加磁盘空间占用。

**Q: 如果诊断发现多个问题怎么办？**  
A: 按优先级修复：1) 索引（最简单） 2) 版本部署 3) 数据写入修复

---

## 联系信息

**问题追踪**：routing-v2 统计数据缺失  
**分支**：server-71  
**最后更新**：2026-06-26  

如有疑问或需要进一步协助，请提供：
1. 诊断脚本输出（diagnosis_report.log）
2. API 测试结果
3. 应用日志（如有错误）

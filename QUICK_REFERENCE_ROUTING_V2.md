# routing-v2 统计数据问题 - 快速参考

## 问题描述
https://llmgateway.internal.example.com/routing-v2 有请求数据但无统计数据

## 诊断结论
✅ **代码完整正确**（后端、前端、测试都已实现）
⚠️ **最可能原因**：索引缺失或版本未部署

## 立即执行（诊断）

### 1. 运行数据库诊断
```bash
psql $DATABASE_URL -f diagnose_routing_v2_stats.sql > diagnosis_report.log
cat diagnosis_report.log
```

### 2. 检查 API 版本
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
     https://llmgateway.internal.example.com/api/admin/auto-route/audit | jq '.specified_model_requests'
```
- 如果返回数字 → 版本正确 ✓
- 如果返回 null 或 404 → 版本太旧 ✗

### 3. 检查索引
```bash
psql $DATABASE_URL -c "
SELECT indexname 
FROM pg_indexes 
WHERE tablename = 'request_logs' 
  AND indexname = 'idx_request_logs_explicit_model';
"
```
- 如果返回 1 行 → 索引存在 ✓
- 如果返回 0 行 → 索引缺失 ✗

## 修复方案

### 方案 A：创建索引（最简单，推荐先尝试）
```bash
psql $DATABASE_URL < docs/2026-06-22-explicit-model-stats.sql
```
- **风险**：低（在线操作，不锁表）
- **时间**：5-10 分钟
- **回滚**：`DROP INDEX idx_request_logs_explicit_model;`

### 方案 B：部署新版本（如果 API 返回 null）
```bash
# 本地测试
export DATABASE_URL="postgresql://..."
./test_routing_v2_local.sh

# 构建（不部署到 71，按用户要求）
./build.sh

# 部署（等待用户确认）
# ./scripts/deploy-llm-gateway-go-71.sh
```

## 验证修复
访问 https://llmgateway.internal.example.com/routing-v2，检查：
- [ ] KPI Bar 显示 6 个指标（包括"指定模型"）
- [ ] 热力图有数据
- [ ] Sankey 流向图有数据

## 详细文档
- 完整诊断报告：`DIAGNOSIS_ROUTING_V2_STATS.md`
- 数据库诊断脚本：`diagnose_routing_v2_stats.sql`
- 本地测试脚本：`test_routing_v2_local.sh`

## 关键文件
- 后端：`admin/analytics.go`, `admin/auto_route.go`
- 前端：`web/src/views/RoutingDashboardView.vue`
- 索引：`docs/2026-06-22-explicit-model-stats.sql`
- 修复：`telemetry/client.go`（已包含修复）

## 时间估算
- 诊断：10-15 分钟
- 修复（索引）：5-10 分钟
- 修复（部署）：30-60 分钟
- 验证：5-10 分钟

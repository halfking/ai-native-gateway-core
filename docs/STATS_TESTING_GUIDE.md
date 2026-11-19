# 统计API测试指南

## 测试状态

✅ **Schema修复已完成** (commit 444c509df)
✅ **文档已完成** (commit 4f6474b97)  
✅ **Maestro测试资产已准备**
⏸️ **测试执行** - 等待服务启动

## 已交付的测试资产

### 1. Maestro UI测试流程
**文件**: `maestro/flows/14-stats-consistency-test.yaml`

验证5个关键页面的功能完整性：
- `/admin/stats` - 统计总览（成本分解、TTFT字段）
- `/admin/session-analytics` - 会话分析（积分、TTFT字段）
- `/admin/stats/trend` - 趋势分析（成本分解）
- `/keys` - API密钥管理
- `/request-logs` - 请求日志查询

### 2. 测试执行脚本
**文件**: `maestro/run-stats-consistency-test.sh`

包含两层验证：
- **UI层**: Maestro页面功能测试
- **API层**: HTTP响应字段验证

## 运行测试的前置条件

### 1. 启动网关服务

```bash
# 方法1: 使用Docker Compose（推荐）
cd llm-gateway-go
docker-compose up -d

# 方法2: 本地编译运行
go build -o gateway cmd/gateway/main.go
./gateway
```

验证服务运行：
```bash
curl http://localhost:8782/health
# 预期: {"status":"ok"}
```

### 2. 准备环境变量

创建 `.env.local` 文件（已在 .gitignore）：
```bash
# .env.local
ADMIN_PASSWORD=your_actual_password_here
AUTH_TOKEN=your_jwt_token_here
```

### 3. 安装Maestro（如未安装）

```bash
# macOS
brew tap mobile-dev-inc/tap
brew install maestro

# 其他平台: https://maestro.mobile.dev/getting-started/installing-maestro
```

## 执行测试

### 快速执行（推荐）

```bash
cd maestro
./run-stats-consistency-test.sh
```

该脚本会：
1. 检查Maestro安装状态
2. 执行UI功能测试（5个页面）
3. 执行API字段验证（3个端点）
4. 生成测试报告: `reports/stats-consistency-test.xml`

### 手动执行单项测试

```bash
# 仅运行UI测试
maestro test \
  --driver-host-port 9222 \
  --screen-size 1440x1400 \
  -e BASE_URL=http://127.0.0.1:8782 \
  -e ADMIN_USER=admin \
  -e ADMIN_PASSWORD="your_password" \
  flows/14-stats-consistency-test.yaml
```

### 使用标准测试套件运行

```bash
# 包含stats测试在内的完整回归测试
cd maestro
./run-local.sh flows/14-stats-consistency-test.yaml
```

## 测试验证内容

### Schema修复验证

测试确认以下数据库更改已生效：

**session_summaries 表**:
- ✓ `total_credits_charged` 字段存在且可查询
- ✓ `avg_ttft_ms`, `min_ttft_ms`, `max_ttft_ms` 字段存在
- ✓ 触发器 `update_session_summary()` 正确聚合新字段

**stats_usage_daily 表**:
- ✓ `input_cost_usd`, `output_cost_usd` 字段存在
- ✓ 成本分解数据正确计算

**stats_usage_monthly 表**:
- ✓ `input_cost_usd`, `output_cost_usd` 字段存在
- ✓ 与daily表保持一致

### API响应验证

**GET /api/admin/stats/summary**:
```json
{
  "summary": {
    "total_requests": 1234,
    "input_cost_usd": 1.23,      // ✓ 新增字段
    "output_cost_usd": 2.34,     // ✓ 新增字段
    "avg_ttft_ms": 156.7         // ✓ 新增字段（可为null）
  }
}
```

**GET /api/admin/stats/trend**:
```json
{
  "items": [{
    "date": "2026-11-19",
    "total_requests": 100,
    "input_cost_usd": 0.12,      // ✓ 新增字段
    "output_cost_usd": 0.23      // ✓ 新增字段
  }]
}
```

**GET /api/admin/session-analytics**:
```json
{
  "sessions": [{
    "session_id": "sess-xxx",
    "total_credits_charged": 150.5,  // ✓ 新增字段
    "avg_ttft_ms": 145.2,            // ✓ 新增字段（可为null）
    "min_ttft_ms": 89,               // ✓ 新增字段（可为null）
    "max_ttft_ms": 234               // ✓ 新增字段（可为null）
  }]
}
```

## 预期结果

### 成功指标

```
═══════════════════════════════════════════════════════════════
✅ 统计API数据一致性测试通过！
═══════════════════════════════════════════════════════════════

统计API数据一致性测试 - 性能报告
═══════════════════════════════════════
✓ 统计总览页面加载: <500ms（理想）
✓ 会话分析页面加载: <1000ms
✓ 统计趋势页面加载: <1000ms
✓ API密钥页面加载: <500ms
✓ 请求日志页面加载: <1000ms
═══════════════════════════════════════
所有页面测试通过！

✅ API级别数据一致性验证通过！
```

### 失败处理

如果测试失败：

1. **检查迁移执行**:
   ```bash
   psql -U postgres -d llm_gateway -c "\d session_summaries" | grep -E "total_credits|ttft"
   psql -U postgres -d llm_gateway -c "\d stats_usage_daily" | grep cost_usd
   ```

2. **查看测试报告**:
   ```bash
   cat maestro/reports/stats-consistency-test.xml
   ```

3. **手动验证API**:
   ```bash
   curl -H "Authorization: Bearer $AUTH_TOKEN" \
     "http://localhost:8782/api/admin/stats/summary?start=2026-11-18T00:00:00Z&end=2026-11-19T23:59:59Z" \
     | jq '.summary | {input_cost_usd, output_cost_usd, avg_ttft_ms}'
   ```

## 已知预期差异

测试可能会显示以下**预期的**数据差异（非错误）：

### 1. 10%请求量差异
- **原因**: `session_summaries` 排除内部回环请求（标题/摘要生成）
- **对比**: `usage_facts` 包含所有请求
- **状态**: ✓ 设计决策，已记录

### 2. 时区处理差异
- **原因**: 会话分析使用本地时间，用户统计使用UTC
- **影响**: 跨日期边界的统计可能不一致
- **状态**: ✓ 已记录，待未来统一

### 3. 空值情况
- **TTFT字段**: 首次部署时为NULL（历史数据无TTFT）
- **成本字段**: 首次部署时为0.00（历史数据未拆分）
- **状态**: ✓ 正常，有默认值

## 下一步行动

测试通过后，可以：

1. **部署到staging环境**: 验证生产数据完整性
2. **监控数据质量**: 
   ```sql
   -- 使用一致性检查视图
   SELECT * FROM stats_consistency_check 
   WHERE ABS(facts_total - summary_total) > summary_total * 0.15;
   ```
3. **更新前端**: 在UI中展示新字段（input_cost_usd等）
4. **性能优化**: 如果查询变慢，考虑添加索引

## 参考文档

- **Schema对齐报告**: `docs/STATS_SCHEMA_ALIGNMENT.md`
- **架构一致性分析**: `docs/data-consistency-analysis.md`
- **迁移脚本**: `sql/migrations/startup/990_stats_consistency_fixes.sql`
- **提交历史**:
  - Schema修复: commit 444c509df
  - 文档和测试: commit 4f6474b97

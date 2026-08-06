# 会话管理功能测试计划

## 测试概述

本文档描述如何验证 2026-08-06 实施的会话管理功能和 P0 修复。

---

## 一、单元测试

### 1.1 现有测试验证

运行现有的单元测试确保没有破坏现有功能：

```bash
# 运行所有 admin 包测试
go test ./admin/... -v

# 运行特定测试
go test ./admin -run TestSplitCorpusIntoChunks -v
go test ./admin -run TestParseSummaryJSON -v
go test ./admin -run TestRollingTurnGate -v
```

**预期结果**：所有现有测试应该通过

### 1.2 新功能单元测试（手动验证）

由于测试框架的限制，以下功能需要通过手动验证或集成测试：

#### 测试 1: minimumTurns() 函数

```bash
# 验证环境变量覆盖
export LLM_GATEWAY_AUTO_SUMMARY_MINIMUM_TURNS=7
# 启动应用，观察日志中的门槛值
```

**预期**：应用使用 7 作为最小轮次门槛

#### 测试 2: shouldTriggerSummary 逻辑

**测试场景**：
1. 创建短会话（< 5 轮）
2. 创建长会话（>= 5 轮）
3. 观察日志中的 gate skip 原因

**预期日志**：
```
# 短会话应该看到：
session_too_short_3_turns

# 长会话首次总结：
never_summarized

# 长会话后续总结（< 3 新轮次）：
only_2_new_turns_since_last_summary

# 长会话后续总结（>= 3 新轮次）：
rolling_gate_open
```

---

## 二、集成测试

### 2.1 数据库迁移测试

**前提条件**：需要测试数据库环境

```bash
# 1. 备份数据库
pg_dump -h <host> -U <user> llm_gateway > backup_pre_migration.sql

# 2. 执行迁移（按顺序）
psql -h <host> -U <user> -d llm_gateway -f deploy/sql/migrations/V353__session_summaries_project_task_tags.sql
psql -h <host> -U <user> -d llm_gateway -f deploy/sql/migrations/V354__session_management_views.sql
psql -h <host> -U <user> -d llm_gateway -f deploy/sql/migrations/V355__backfill_session_task_id.sql

# 3. 验证迁移结果
psql -h <host> -U <user> -d llm_gateway << 'EOF'
-- 检查新字段
\d session_summaries

-- 检查新索引
\di idx_session_summaries_project
\di idx_session_summaries_task
\di idx_session_summaries_user_tags
\di idx_session_summaries_status_time
\di idx_session_summaries_search

-- 检查新视图
\dv v_session_flow
\dv v_task_summary
\dv v_project_summary
\dv v_daily_session_costs

-- 检查 task_id 覆盖率
SELECT * FROM v_session_task_id_coverage;

-- 验证数据完整性
SELECT 
  COUNT(*) as total,
  COUNT(gw_task_id) as with_task,
  COUNT(gw_project_id) as with_project,
  COUNT(CASE WHEN user_tags != '{}' THEN 1 END) as with_tags
FROM session_summaries;
EOF
```

**成功标准**：
- ✅ 所有字段创建成功
- ✅ 所有索引创建成功
- ✅ 所有视图创建成功
- ✅ task_id 覆盖率 > 80%
- ✅ 无数据丢失或损坏

### 2.2 API 端点测试

**前提条件**：应用已部署，有有效的 JWT token

#### 测试用例 1: 会话列表 API

```bash
TOKEN="your_jwt_token"
HOST="http://localhost:8781"

# 1. 基本列表查询
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?page=1&page_size=10"

# 预期响应：
# {
#   "sessions": [...],
#   "total": 156,
#   "page": 1,
#   "page_size": 10
# }

# 2. 按任务过滤
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?task_id=task_042"

# 预期：只返回该任务的会话

# 3. 按标签过滤
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?tags=feature,urgent"

# 预期：返回包含这些标签的会话

# 4. 全文搜索
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?search=认证"

# 预期：返回标题或总结中包含"认证"的会话

# 5. 日期范围
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?from_date=2026-08-01&to_date=2026-08-31"

# 预期：返回 8 月的会话

# 6. 排序
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?sort_by=total_cost_usd&sort_order=desc"

# 预期：按成本降序排列
```

#### 测试用例 2: 会话详情 API

```bash
SESSION_KEY="gw_abc123"

curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/detail/$SESSION_KEY"

# 预期响应：
# {
#   "session_key": "gw_abc123",
#   "title": "...",
#   "summary": "...",
#   "key_topics": [...],
#   "requests": [...],  # 会话中的所有请求
#   "related_sessions": {
#     "prev": {...},
#     "next": {...}
#   }
# }
```

#### 测试用例 3: 会话更新 API

```bash
curl -X PATCH \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "project_id": "proj_001",
    "task_id": "task_042",
    "user_tags": ["feature", "auth", "security"],
    "status": "completed"
  }' \
  "$HOST/api/sessions/update/$SESSION_KEY"

# 预期响应：
# {
#   "success": true,
#   "session_key": "gw_abc123"
# }

# 验证更新
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/detail/$SESSION_KEY" | jq '.user_tags'

# 预期：["feature", "auth", "security"]
```

#### 测试用例 4: 任务脉络 API

```bash
TASK_ID="task_042"

curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/task-flow/$TASK_ID"

# 预期响应：
# {
#   "task_id": "task_042",
#   "summary": {
#     "session_count": 5,
#     "total_cost_usd": 2.15,
#     "total_tokens": 580000,
#     "status": "in_progress"
#   },
#   "sessions": [
#     {"session_key": "...", "title": "需求分析", "order": 1, ...},
#     {"session_key": "...", "title": "技术选型", "order": 2, ...}
#   ],
#   "daily_costs": [...]
# }
```

#### 测试用例 5: 项目成本 API

```bash
PROJECT_ID="proj_001"

curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/project-costs/$PROJECT_ID"

# 预期响应：
# {
#   "project_id": "proj_001",
#   "summary": {
#     "task_count": 12,
#     "session_count": 47,
#     "total_cost_usd": 18.65
#   },
#   "tasks": [...],
#   "daily_costs": [...]
# }
```

**成功标准**：
- ✅ 所有 API 返回 200 状态码
- ✅ 响应 JSON 结构正确
- ✅ 数据准确无误
- ✅ 过滤、排序、分页正常工作

---

## 三、性能测试

### 3.1 API 响应时间测试

```bash
# 使用 Apache Bench 进行压力测试
ab -n 100 -c 10 \
  -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?page=1&page_size=20"

# 预期：
# - 平均响应时间 < 500ms
# - P95 响应时间 < 2s
# - 无失败请求
```

### 3.2 全文搜索性能测试

```bash
# 测试全文搜索性能
time curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?search=认证&page_size=100"

# 预期：
# - 响应时间 < 1s（即使在大数据集上）
# - GIN 索引被正确使用
```

### 3.3 数据库查询性能

```sql
-- 检查查询计划
EXPLAIN ANALYZE
SELECT * FROM session_summaries
WHERE search_vector @@ plainto_tsquery('simple', '认证')
LIMIT 20;

-- 预期：使用 idx_session_summaries_search 索引

-- 检查视图性能
EXPLAIN ANALYZE
SELECT * FROM v_task_summary WHERE gw_task_id = 'task_042';

-- 预期：执行时间 < 100ms
```

**成功标准**：
- ✅ API 响应时间 P95 < 2s
- ✅ 全文搜索使用 GIN 索引
- ✅ 视图查询执行时间 < 100ms
- ✅ 无慢查询警告

---

## 四、回归测试

### 4.1 现有功能验证

验证新功能没有破坏现有功能：

```bash
# 1. 原有会话列表 API
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/admin/sessions"

# 预期：正常工作

# 2. 原有会话详情 API
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/admin/sessions/$SESSION_KEY/detail"

# 预期：正常工作

# 3. 请求日志 API
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/admin/logs?page=1"

# 预期：正常工作

# 4. 仪表板 API
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/admin/dashboard/session-overview"

# 预期：正常工作
```

### 4.2 自动总结验证

创建测试会话并观察自动总结行为：

```bash
# 监控日志
tail -f /var/log/llm-gateway/gateway.log | grep auto_summary

# 预期日志示例：
# - 短会话（< 5 轮）：
#   "summary skipped by rolling gate, reason=session_too_short_3_turns"
#
# - 长会话（>= 5 轮，首次）：
#   "summary skipped by rolling gate, reason=never_summarized"
#   "auto_summary: summary generated, summary_chars=156, elapsed_ms=1234"
#
# - 长会话（后续 < 3 新轮）：
#   "summary skipped by rolling gate, reason=only_2_new_turns_since_last_summary"
#
# - 长会话（后续 >= 3 新轮）：
#   "summary skipped by rolling gate, reason=rolling_gate_open"
#   "auto_summary: summary generated, ..."
```

### 4.3 Prometheus 指标验证

```bash
# 检查新指标
curl -s http://localhost:9090/metrics | grep auto_summary_gate_skip

# 预期输出：
# auto_summary_gate_skip{reason="session_too_short_0_turns"} 0
# auto_summary_gate_skip{reason="session_too_short_3_turns"} 42
# auto_summary_gate_skip{reason="only_2_new_turns_since_last_summary"} 18
# ...
```

**成功标准**：
- ✅ 所有原有 API 正常工作
- ✅ 自动总结按预期触发（有 5 轮门槛）
- ✅ Prometheus 指标正常记录
- ✅ 无新增错误日志

---

## 五、端到端测试场景

### 场景 1: 创建并管理一个完整的项目

```bash
# 步骤 1: 创建会话并打标签
# （通过正常使用应用创建会话）

# 步骤 2: 更新会话元数据
curl -X PATCH \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "project_id": "test_project",
    "task_id": "test_task_1",
    "user_tags": ["test", "e2e"],
    "status": "active"
  }' \
  "$HOST/api/sessions/update/$SESSION1"

# 步骤 3: 创建第二个会话（同一任务）
# ...

# 步骤 4: 查看任务脉络
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/task-flow/test_task_1"

# 预期：看到两个会话，按时间排序

# 步骤 5: 标记第一个会话完成
curl -X PATCH \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status": "completed"}' \
  "$HOST/api/sessions/update/$SESSION1"

# 步骤 6: 查看项目成本
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/project-costs/test_project"

# 预期：看到项目统计和成本信息
```

### 场景 2: 全文搜索和过滤

```bash
# 步骤 1: 搜索包含特定关键词的会话
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?search=测试"

# 步骤 2: 按标签过滤
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?tags=e2e"

# 步骤 3: 组合过滤
curl -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/sessions/list?task_id=test_task_1&status=completed"

# 预期：所有查询返回正确的结果
```

**成功标准**：
- ✅ 完整流程无错误
- ✅ 数据一致性保持
- ✅ 所有查询返回预期结果

---

## 六、测试检查清单

### 部署前

- [ ] 所有现有单元测试通过
- [ ] 代码编译成功
- [ ] 数据库迁移脚本在测试环境验证
- [ ] API 端点在测试环境验证

### 部署后

- [ ] 数据库迁移成功执行
- [ ] task_id 覆盖率 > 80%
- [ ] 所有 5 个新 API 端点正常工作
- [ ] 自动总结 5 轮门槛生效
- [ ] Prometheus 指标正常记录
- [ ] 无新增错误日志
- [ ] 原有功能正常工作
- [ ] API 响应时间在预期范围内

### 生产验证（部署后 24 小时）

- [ ] 成本节省指标开始显示
- [ ] 无性能退化
- [ ] 无用户投诉
- [ ] 监控指标正常

---

## 七、故障排查

### 问题 1: API 返回 500 错误

**排查步骤**：
1. 查看应用日志：`journalctl -u llm-gateway -n 100`
2. 检查数据库连接：`psql -h <host> -U <user> -d llm_gateway -c "SELECT 1;"`
3. 验证视图：`\dv v_session_flow`

### 问题 2: 全文搜索很慢

**排查步骤**：
1. 检查索引：`\di idx_session_summaries_search`
2. 运行 VACUUM：`VACUUM ANALYZE session_summaries;`
3. 查看查询计划：`EXPLAIN ANALYZE SELECT ...`

### 问题 3: task_id 覆盖率低

**排查步骤**：
1. 查看覆盖率：`SELECT * FROM v_session_task_id_coverage;`
2. 检查 request_logs 数据：`SELECT COUNT(*) FROM request_logs WHERE gw_task_id IS NOT NULL;`
3. 重新运行回填脚本（如果需要）

---

## 八、测试报告模板

```markdown
# 测试报告 - 会话管理功能

**测试日期**：YYYY-MM-DD  
**测试人员**：[姓名]  
**环境**：[测试/生产]

## 测试结果

### 单元测试
- [ ] 通过 / [ ] 失败
- 详细结果：...

### 集成测试
- [ ] 通过 / [ ] 失败
- 详细结果：...

### 性能测试
- API 响应时间：P50=__ms, P95=__ms, P99=__ms
- 全文搜索：__ms
- 评估：[ ] 合格 / [ ] 不合格

### 回归测试
- [ ] 通过 / [ ] 失败
- 发现问题：...

## 总结
- [ ] 所有测试通过，建议部署到生产
- [ ] 发现问题，需要修复后重新测试

## 问题列表
1. ...
2. ...
```

---

**文档版本**：v1.0  
**创建日期**：2026-08-06  
**维护者**：开发团队

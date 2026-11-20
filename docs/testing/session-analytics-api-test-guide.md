# 会话分析系统集成测试指南

**文档版本**: 1.0  
**创建日期**: 2026-11-20  
**适用范围**: 后端 API 测试

---

## 一、测试准备

### 1.1 确认环境

1. **数据库迁移状态**
   ```bash
   # 检查981迁移是否已应用
   psql $DATABASE_URL -c "SELECT version FROM schema_migrations WHERE version = 981;"
   ```

2. **物化视图状态**
   ```sql
   -- 检查物化视图是否存在
   SELECT schemaname, matviewname, hasindexes 
   FROM pg_matviews 
   WHERE matviewname IN (
       'user_session_aggregates',
       'project_task_aggregates', 
       'agent_usage_aggregates',
       'model_usage_aggregates'
   );
   
   -- 检查数据量
   SELECT 'user_session_aggregates' as view_name, COUNT(*) as row_count 
   FROM user_session_aggregates
   UNION ALL
   SELECT 'agent_usage_aggregates', COUNT(*) FROM agent_usage_aggregates
   UNION ALL
   SELECT 'model_usage_aggregates', COUNT(*) FROM model_usage_aggregates;
   ```

3. **编译验证**
   ```bash
   cd /path/to/llm-gateway-go
   go build ./admin/session_analytics/
   go build ./cmd/gateway/
   ```

### 1.2 获取测试凭据

需要准备：
- 管理员账号的登录凭据
- 有效的日期范围（确保物化视图中有数据）

---

## 二、API 测试用例

### 2.1 总体统计 API

**端点**: `GET /api/admin/session-analytics/overview`

```bash
# 测试用例 1: 基本查询
curl -X GET "https://your-domain.com/api/admin/session-analytics/overview?start_date=2026-11-01&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE" \
  -H "Content-Type: application/json"

# 预期响应: 200 OK
# 响应体应包含:
# - summary (total_sessions, avg_tokens, total_cost, active_users, etc.)
# - top_models (array)
# - trends (sessions, cost, tokens)
# - top_users (array)
# - top_projects (array)
```

### 2.2 人员排行 API

**端点**: `GET /api/admin/session-analytics/user-ranking`

```bash
# 测试用例 2: 人员排行（默认参数）
curl -X GET "https://your-domain.com/api/admin/session-analytics/user-ranking?start_date=2026-11-01&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 200 OK
# 响应体应包含:
# - total (总用户数)
# - users (array of UserRanking)

# 测试用例 3: 人员排行（自定义排序和分页）
curl -X GET "https://your-domain.com/api/admin/session-analytics/user-ranking?start_date=2026-11-01&end_date=2026-11-20&sort_by=cost&order=desc&limit=10&offset=0" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"
```

### 2.3 人员详情 API

**端点**: `GET /api/admin/session-analytics/user-ranking/:owner`

```bash
# 测试用例 4: 人员详情
# 注意：将 john 替换为实际存在的 owner
curl -X GET "https://your-domain.com/api/admin/session-analytics/user-ranking/john?start_date=2026-11-01&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 200 OK
# 响应体应包含:
# - user (owner, display_name, email, tenant_id)
# - summary (session_count, task_count, total_cost, rank, rank_change)
# - trends (sessions, tasks, cost, rank arrays)
```

### 2.4 用户项目列表 API

**端点**: `GET /api/admin/session-analytics/user-ranking/:owner/projects`

```bash
# 测试用例 5: 用户项目列表
curl -X GET "https://your-domain.com/api/admin/session-analytics/user-ranking/john/projects?start_date=2026-11-01&end_date=2026-11-20&sort_by=sessions&order=desc" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 200 OK
# 响应体应包含:
# - projects (array of UserProject)
```

### 2.5 项目任务列表 API

**端点**: `GET /api/admin/session-analytics/user-ranking/:owner/projects/:project/tasks`

```bash
# 测试用例 6: 项目任务列表
# 注意：URL编码项目名称中的特殊字符
curl -X GET "https://your-domain.com/api/admin/session-analytics/user-ranking/john/projects/my-project/tasks?start_date=2026-11-01&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 200 OK
# 响应体应包含:
# - tasks (array of ProjectTask)
```

### 2.6 智能体排行 API

**端点**: `GET /api/admin/session-analytics/agent-ranking`

```bash
# 测试用例 7: 智能体排行
curl -X GET "https://your-domain.com/api/admin/session-analytics/agent-ranking?start_date=2026-11-01&end_date=2026-11-20&sort_by=sessions&order=desc" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 200 OK
# 响应体应包含:
# - agents (array of AgentRanking)
#   每个agent包含: rank, agent_name, user_count, session_count, task_count, 
#                   total_tokens, total_cost, avg_duration_seconds, last_used_at
```

### 2.7 模型排行 API

**端点**: `GET /api/admin/session-analytics/model-ranking`

```bash
# 测试用例 8: 模型排行（按请求数）
curl -X GET "https://your-domain.com/api/admin/session-analytics/model-ranking?start_date=2026-11-01&end_date=2026-11-20&sort_by=requests&order=desc" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 200 OK
# 响应体应包含:
# - models (array of ModelRanking)
#   每个model包含: rank, model, provider, request_count, success_count, 
#                  success_rate, total_tokens, cache_hit_count, cache_rate,
#                  avg_duration_ms, total_cost, user_count, last_used_at

# 测试用例 9: 模型排行（按成功率）
curl -X GET "https://your-domain.com/api/admin/session-analytics/model-ranking?start_date=2026-11-01&end_date=2026-11-20&sort_by=success_rate&order=desc" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"
```

---

## 三、错误场景测试

### 3.1 权限测试

```bash
# 测试用例 10: 无凭据访问
curl -X GET "https://your-domain.com/api/admin/session-analytics/overview?start_date=2026-11-01&end_date=2026-11-20"

# 预期响应: 401 Unauthorized 或 403 Forbidden
```

### 3.2 参数验证测试

```bash
# 测试用例 11: 缺少必需参数
curl -X GET "https://your-domain.com/api/admin/session-analytics/overview" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 400 Bad Request
# 错误信息: "start_date is required"

# 测试用例 12: 无效的日期格式
curl -X GET "https://your-domain.com/api/admin/session-analytics/overview?start_date=2026/11/01&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 400 Bad Request
# 错误信息: "invalid start_date format"

# 测试用例 13: 无效的排序字段
curl -X GET "https://your-domain.com/api/admin/session-analytics/user-ranking?start_date=2026-11-01&end_date=2026-11-20&sort_by=invalid_field" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 400 Bad Request
# 错误信息: "invalid sort_by"
```

### 3.3 数据不存在测试

```bash
# 测试用例 14: 不存在的用户
curl -X GET "https://your-domain.com/api/admin/session-analytics/user-ranking/nonexistent_user_12345?start_date=2026-11-01&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 预期响应: 200 OK (返回空数据或默认值)
# 注意：不应该返回404，因为用户可能存在但在指定时间范围内无数据
```

---

## 四、性能测试

### 4.1 响应时间基准

记录以下端点的响应时间（应在合理范围内，建议<2秒）：

```bash
# 使用 time 命令测试
time curl -X GET "https://your-domain.com/api/admin/session-analytics/overview?start_date=2026-11-01&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE" \
  -o /dev/null -s -w "%{time_total}\n"
```

### 4.2 数据量测试

测试大日期范围的查询：

```bash
# 测试用例 15: 大日期范围（30天）
curl -X GET "https://your-domain.com/api/admin/session-analytics/overview?start_date=2026-10-20&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"

# 测试用例 16: 大日期范围（90天）
curl -X GET "https://your-domain.com/api/admin/session-analytics/overview?start_date=2026-08-20&end_date=2026-11-20" \
  -H "Cookie: llmgw_session=YOUR_SESSION_COOKIE"
```

---

## 五、数据一致性检查

### 5.1 跨端点数据对比

```bash
# 1. 获取总体统计的活跃用户数
# 2. 获取人员排行的总用户数
# 3. 验证两个数字是否一致（在合理误差范围内）

# 例如：
# overview.summary.active_users 应该 ≈ user-ranking.total
```

### 5.2 趋势数据完整性

```bash
# 验证趋势数据的日期序列是否连续
# 检查是否有缺失的日期
# 验证数值是否合理（非负数）
```

---

## 六、测试检查清单

### 6.1 功能测试

- [ ] 总体统计 API 返回正确的摘要数据
- [ ] 人员排行 API 支持排序和分页
- [ ] 人员详情 API 返回完整的用户信息和趋势
- [ ] 用户项目列表 API 正确聚合项目数据
- [ ] 项目任务列表 API 正确聚合任务数据
- [ ] 智能体排行 API 返回正确的智能体统计
- [ ] 模型排行 API 返回正确的模型统计

### 6.2 安全测试

- [ ] 无凭据访问被拒绝（401/403）
- [ ] 非管理员用户访问被拒绝（403）
- [ ] 租户隔离正常工作（用户只能看到自己租户的数据）
- [ ] SQL注入防护有效

### 6.3 边界测试

- [ ] 缺少必需参数时返回400
- [ ] 无效的参数值时返回400
- [ ] 日期范围为空时返回空数据（不崩溃）
- [ ] 极大的分页偏移量时正常处理

### 6.4 性能测试

- [ ] 所有端点响应时间 < 2秒（正常负载）
- [ ] 大日期范围查询不会超时
- [ ] 并发请求能正常处理

### 6.5 数据质量测试

- [ ] 数值字段无null值（使用COALESCE）
- [ ] 百分比字段在0-100范围内
- [ ] 日期格式统一（YYYY-MM-DD或RFC3339）
- [ ] 趋势数据按日期排序

---

## 七、常见问题排查

### 7.1 403 Forbidden

**可能原因**:
- Cookie过期或无效
- 用户不是管理员角色
- 租户ID不匹配

**排查步骤**:
1. 检查Cookie是否有效
2. 确认用户角色（应为super_admin、tenant_admin或admin_key）
3. 检查tenant_id参数

### 7.2 500 Internal Server Error

**可能原因**:
- 物化视图不存在或未刷新
- 数据库连接问题
- SQL查询错误

**排查步骤**:
1. 检查服务器日志
2. 验证物化视图状态
3. 检查数据库连接

### 7.3 返回数据为空

**可能原因**:
- 指定的日期范围内无数据
- 物化视图未刷新
- 租户隔离过滤了数据

**排查步骤**:
1. 检查物化视图的refreshed_at时间戳
2. 手动查询物化视图验证数据
3. 尝试更大的日期范围

---

## 八、测试报告模板

```markdown
# 会话分析系统测试报告

**测试日期**: YYYY-MM-DD
**测试人**: XXX
**测试环境**: [开发/测试/生产]

## 测试结果汇总

| 类别 | 通过 | 失败 | 跳过 |
|------|------|------|------|
| 功能测试 | X/7 | X/7 | X/7 |
| 安全测试 | X/4 | X/4 | X/4 |
| 性能测试 | X/2 | X/2 | X/2 |
| 边界测试 | X/4 | X/4 | X/4 |

## 失败用例详情

1. **用例名称**: XXX
   - **失败原因**: XXX
   - **预期结果**: XXX
   - **实际结果**: XXX
   - **严重程度**: [高/中/低]

## 性能数据

| 端点 | 平均响应时间 | P95响应时间 | P99响应时间 |
|------|--------------|-------------|-------------|
| overview | XXms | XXms | XXms |
| user-ranking | XXms | XXms | XXms |
| ...

## 建议与结论

[测试总结和建议]
```

---

## 九、自动化测试脚本示例

```bash
#!/bin/bash
# 会话分析API测试脚本

BASE_URL="https://your-domain.com/api/admin/session-analytics"
COOKIE="llmgw_session=YOUR_SESSION_COOKIE"
START_DATE="2026-11-01"
END_DATE="2026-11-20"

echo "=== 会话分析系统API测试 ==="

# 测试1: 总体统计
echo "测试1: 总体统计"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  -X GET "${BASE_URL}/overview?start_date=${START_DATE}&end_date=${END_DATE}" \
  -H "Cookie: ${COOKIE}")
if [ "$HTTP_CODE" = "200" ]; then
  echo "✓ 通过"
else
  echo "✗ 失败 (HTTP ${HTTP_CODE})"
fi

# 测试2: 人员排行
echo "测试2: 人员排行"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  -X GET "${BASE_URL}/user-ranking?start_date=${START_DATE}&end_date=${END_DATE}" \
  -H "Cookie: ${COOKIE}")
if [ "$HTTP_CODE" = "200" ]; then
  echo "✓ 通过"
else
  echo "✗ 失败 (HTTP ${HTTP_CODE})"
fi

# 测试3: 智能体排行
echo "测试3: 智能体排行"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  -X GET "${BASE_URL}/agent-ranking?start_date=${START_DATE}&end_date=${END_DATE}" \
  -H "Cookie: ${COOKIE}")
if [ "$HTTP_CODE" = "200" ]; then
  echo "✓ 通过"
else
  echo "✗ 失败 (HTTP ${HTTP_CODE})"
fi

# 测试4: 模型排行
echo "测试4: 模型排行"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  -X GET "${BASE_URL}/model-ranking?start_date=${START_DATE}&end_date=${END_DATE}" \
  -H "Cookie: ${COOKIE}")
if [ "$HTTP_CODE" = "200" ]; then
  echo "✓ 通过"
else
  echo "✗ 失败 (HTTP ${HTTP_CODE})"
fi

echo "=== 测试完成 ==="
```

---

**文档维护**: 请在测试过程中发现问题或改进建议时更新本文档。

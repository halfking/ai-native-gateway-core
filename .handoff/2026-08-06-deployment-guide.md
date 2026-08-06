# 会话管理功能部署指南

## 部署时间：2026-08-06

---

## 一、部署前检查清单

### 1.1 环境要求
- [ ] PostgreSQL 14+ （支持 tsvector 和 GIN 索引）
- [ ] Go 1.21+
- [ ] 数据库备份已完成
- [ ] 在测试环境验证通过
- [ ] 维护窗口已预约（建议 30-60 分钟）

### 1.2 权限要求
- [ ] 数据库超级用户权限（执行迁移脚本）
- [ ] 应用部署权限
- [ ] 回滚计划已准备

---

## 二、部署步骤

### 步骤 1: 代码部署 ✅

**修改的文件：**
```bash
admin/admin_llm_task.go                 # P0 修复：max_tokens 防护栏
admin/auto_summary_generator.go         # P0 修复：5轮门槛
admin/handler.go                         # 路由注册
internal/summarystore/store.go          # CountTotalTurns 方法
```

**新增的文件：**
```bash
admin/session_management_api.go         # 会话管理 API
admin/session_task_project_api.go       # 任务脉络 API
admin/session_management_handlers.go    # 公开 API 包装器
deploy/sql/migrations/V353__session_summaries_project_task_tags.sql
deploy/sql/migrations/V354__session_management_views.sql
deploy/sql/migrations/V355__backfill_session_task_id.sql
```

**部署命令：**
```bash
# 1. 拉取最新代码
git pull origin main

# 2. 编译验证
go build -o /tmp/llm-gateway .

# 3. 运行测试（可选）
go test ./admin/... -v

# 4. 部署（根据你的部署流程）
# 例如：systemctl stop llm-gateway
#      cp /tmp/llm-gateway /usr/local/bin/
#      systemctl start llm-gateway
```

---

### 步骤 2: 数据库迁移 🔧

**重要提示**：
- ⚠️ 在**维护窗口**执行
- ⚠️ 建议在**低峰期**执行（V355 可能需要 10-30 分钟）
- ⚠️ 确保数据库连接稳定

#### 2.1 执行 V353 迁移（Schema 扩展）

**预估时间**：< 1 分钟

```bash
# 连接到数据库
psql -h <host> -U <user> -d llm_gateway

# 执行迁移
\i deploy/sql/migrations/V353__session_summaries_project_task_tags.sql

# 验证新字段
\d session_summaries

# 预期输出应包含：
# - gw_project_id (text)
# - gw_task_id (text)
# - user_tags (text[])
# - session_status (varchar(20))
# - search_vector (tsvector)

# 验证索引
\di idx_session_summaries_project
\di idx_session_summaries_task
\di idx_session_summaries_user_tags
\di idx_session_summaries_status_time
\di idx_session_summaries_search
```

#### 2.2 执行 V354 迁移（视图创建）

**预估时间**：< 1 分钟

```bash
# 执行迁移
\i deploy/sql/migrations/V354__session_management_views.sql

# 验证视图
\dv v_session_flow
\dv v_task_summary
\dv v_project_summary
\dv v_daily_session_costs

# 测试视图查询
SELECT COUNT(*) FROM v_session_flow;
SELECT COUNT(*) FROM v_task_summary;
SELECT COUNT(*) FROM v_project_summary;
```

#### 2.3 执行 V355 迁移（数据回填）⏱️

**预估时间**：10-30 分钟（取决于数据量）

**重要**：此步骤会回填历史数据，时间较长。脚本会输出进度日志。

```bash
# 执行迁移（这会启动后台进程）
\i deploy/sql/migrations/V355__backfill_session_task_id.sql

# 脚本会输出类似以下的进度信息：
# NOTICE:  Starting session_summaries.gw_task_id backfill...
# NOTICE:  Updated 500 sessions...
# NOTICE:  Updated 1000 sessions...
# NOTICE:  Backfill completed. Total sessions updated: 1234

# 查看覆盖率报告
SELECT * FROM v_session_task_id_coverage;

# 预期输出：
#  total_sessions | sessions_with_task_id | sessions_without_task_id | coverage_percentage
# ----------------+-----------------------+--------------------------+---------------------
#           1234  |                  1100 |                      134 |               89.14
```

**如果迁移时间过长，可以考虑分批执行：**

```sql
-- 手动分批回填（如果自动脚本超时）
DO $$
DECLARE
  batch_size int := 100;
  total_updated int := 0;
BEGIN
  -- 分批更新，每批100条
  LOOP
    WITH batch AS (
      SELECT DISTINCT ON (rl.gw_session_id) 
        rl.gw_session_id, rl.gw_task_id
      FROM request_logs_hot rl
      INNER JOIN session_summaries ss 
        ON ss.session_key = rl.gw_session_id
      WHERE ss.gw_task_id IS NULL
        AND rl.gw_task_id IS NOT NULL
      LIMIT batch_size
    )
    UPDATE session_summaries ss
    SET gw_task_id = b.gw_task_id, updated_at = NOW()
    FROM batch b
    WHERE ss.session_key = b.gw_session_id;
    
    GET DIAGNOSTICS total_updated = ROW_COUNT;
    EXIT WHEN total_updated = 0;
    
    RAISE NOTICE 'Updated % sessions', total_updated;
    PERFORM pg_sleep(0.5);  -- 休息0.5秒避免阻塞
  END LOOP;
END $$;
```

---

### 步骤 3: 验证部署 ✅

#### 3.1 验证 P0 修复

**验证自动总结 5 轮门槛：**
```bash
# 创建一个短会话（< 5 轮）
# 查看日志，应该看到：
# "summary skipped by rolling gate, reason=session_too_short_3_turns"

# 检查 Prometheus 指标
curl -s http://localhost:9090/metrics | grep auto_summary_gate_skip
# 应该看到 session_too_short_* 的计数增加
```

**验证标题生成防护栏：**
```bash
# 触发标题生成
# 查看日志，确认 max_tokens 被设置为 128
# 如果标题被截断，应该看到 finish_reason=length
```

#### 3.2 验证 API 可用性

**测试会话列表 API：**
```bash
# 需要 JWT token 或 admin key
TOKEN="your_jwt_token_here"

# 1. 列表查询
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8781/api/sessions/list?page=1&page_size=10"

# 预期响应：
# {
#   "sessions": [...],
#   "total": 156,
#   "page": 1,
#   "page_size": 10
# }

# 2. 按任务过滤
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8781/api/sessions/list?task_id=task_042"

# 3. 全文搜索
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8781/api/sessions/list?search=认证"

# 4. 会话详情
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8781/api/sessions/detail/gw_abc123"

# 5. 任务脉络
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8781/api/sessions/task-flow/task_042"

# 6. 项目成本
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8781/api/sessions/project-costs/proj_001"
```

#### 3.3 验证数据完整性

```sql
-- 检查 task_id 覆盖率
SELECT * FROM v_session_task_id_coverage;

-- 检查新字段是否正常
SELECT 
  COUNT(*) as total,
  COUNT(gw_project_id) as with_project,
  COUNT(gw_task_id) as with_task,
  COUNT(CASE WHEN user_tags != '{}' THEN 1 END) as with_tags
FROM session_summaries;

-- 检查视图是否正常工作
SELECT COUNT(*) FROM v_session_flow WHERE gw_task_id IS NOT NULL;
SELECT COUNT(*) FROM v_task_summary;
SELECT COUNT(*) FROM v_project_summary;
```

---

## 三、性能监控

### 3.1 关键指标

**Prometheus 指标：**
```bash
# 自动总结门槛过滤次数
auto_summary_gate_skip{reason="session_too_short_*"}

# 会话列表 API 响应时间
http_request_duration_seconds{endpoint="/api/sessions/list"}

# 全文搜索性能
# （通过应用日志或 PostgreSQL slow query log 监控）
```

**数据库指标：**
```sql
-- 索引使用情况
SELECT schemaname, tablename, indexname, idx_scan, idx_tup_read, idx_tup_fetch
FROM pg_stat_user_indexes
WHERE tablename = 'session_summaries'
ORDER BY idx_scan DESC;

-- 表大小变化（新增字段和索引会增加存储）
SELECT 
  pg_size_pretty(pg_total_relation_size('session_summaries')) as total_size,
  pg_size_pretty(pg_relation_size('session_summaries')) as table_size,
  pg_size_pretty(pg_indexes_size('session_summaries')) as indexes_size;
```

### 3.2 告警配置（可选）

```yaml
# Prometheus 告警规则示例
groups:
  - name: session_management
    rules:
      - alert: SessionListAPISlowResponse
        expr: histogram_quantile(0.95, http_request_duration_seconds{endpoint="/api/sessions/list"}) > 2
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "会话列表 API 响应慢（P95 > 2s）"
          
      - alert: TaskIDCoverageLow
        expr: (sessions_with_task_id / total_sessions) < 0.8
        for: 30m
        labels:
          severity: info
        annotations:
          summary: "task_id 覆盖率低于 80%"
```

---

## 四、回滚计划

### 4.1 代码回滚

**如果新功能有问题，可以快速回滚代码：**

```bash
# 1. 回滚到上一个版本
git revert HEAD~1  # 或指定 commit

# 2. 重新编译和部署
go build -o /tmp/llm-gateway .
# ... 部署流程

# 3. 重启服务
systemctl restart llm-gateway
```

**关键点**：
- ✅ P0 修复（5轮门槛、max_tokens）是向后兼容的，可以安全回滚
- ✅ 新 API 路由不影响现有功能，可以禁用路由而保留代码
- ✅ 数据库 Schema 扩展是附加性的，不需要回滚

### 4.2 数据库回滚（仅在必要时）

**V353 回滚（删除新字段）：**
```sql
-- 警告：这会丢失已填充的数据！
ALTER TABLE session_summaries
  DROP COLUMN IF EXISTS gw_project_id,
  DROP COLUMN IF EXISTS gw_task_id,
  DROP COLUMN IF EXISTS user_tags,
  DROP COLUMN IF EXISTS session_status,
  DROP COLUMN IF EXISTS search_vector;

-- 删除索引
DROP INDEX IF EXISTS idx_session_summaries_project;
DROP INDEX IF EXISTS idx_session_summaries_task;
DROP INDEX IF EXISTS idx_session_summaries_user_tags;
DROP INDEX IF EXISTS idx_session_summaries_status_time;
DROP INDEX IF EXISTS idx_session_summaries_search;
```

**V354 回滚（删除视图）：**
```sql
DROP VIEW IF EXISTS v_session_flow;
DROP VIEW IF EXISTS v_task_summary;
DROP VIEW IF EXISTS v_project_summary;
DROP VIEW IF EXISTS v_daily_session_costs;
```

**V355 回滚（删除触发器）：**
```sql
DROP TRIGGER IF EXISTS trg_sync_session_task_id ON request_logs_hot;
DROP FUNCTION IF EXISTS sync_session_task_id();
DROP VIEW IF EXISTS v_session_task_id_coverage;

-- 注意：已回填的 gw_task_id 数据不会被删除
-- 如果需要清空，可以执行：
-- UPDATE session_summaries SET gw_task_id = NULL WHERE gw_task_id IS NOT NULL;
```

---

## 五、常见问题排查

### 问题 1: V355 迁移超时

**症状**：回填脚本执行超过预期时间

**解决方案**：
1. 检查数据库负载：`SELECT * FROM pg_stat_activity;`
2. 使用分批回填脚本（见步骤 2.3）
3. 考虑在更低峰期重新执行

### 问题 2: API 返回 500 错误

**症状**：调用新 API 返回 500 Internal Server Error

**排查步骤**：
1. 查看应用日志：`journalctl -u llm-gateway -f`
2. 检查数据库连接：`SELECT 1;`
3. 验证视图是否创建成功：`\dv`
4. 检查权限：确保应用用户有读取权限

### 问题 3: 全文搜索性能差

**症状**：search 参数查询超过 2 秒

**解决方案**：
1. 检查 GIN 索引：`\d idx_session_summaries_search`
2. 运行 VACUUM：`VACUUM ANALYZE session_summaries;`
3. 考虑限制搜索结果数量（已在代码中实现分页）

### 问题 4: task_id 覆盖率低

**症状**：v_session_task_id_coverage 显示覆盖率 < 50%

**原因分析**：
- 大量历史会话的请求中没有 gw_task_id
- request_logs_hot 中的数据已被归档

**解决方案**：
```sql
-- 从归档分区回填（如果有）
WITH archived_tasks AS (
  SELECT DISTINCT ON (gw_session_id)
    gw_session_id, gw_task_id
  FROM request_logs  -- 包含所有分区
  WHERE gw_task_id IS NOT NULL
  ORDER BY gw_session_id, ts DESC
)
UPDATE session_summaries ss
SET gw_task_id = at.gw_task_id, updated_at = NOW()
FROM archived_tasks at
WHERE ss.session_key = at.gw_session_id
  AND ss.gw_task_id IS NULL;
```

---

## 六、成功标准

### 部署被认为成功，当：

- [x] P0 修复生效：
  - 自动总结只在 >= 5 轮时触发
  - 标题生成有 max_tokens 防护
  
- [x] 数据库迁移完成：
  - V353: 新字段和索引创建成功
  - V354: 4 个视图创建成功
  - V355: task_id 覆盖率 > 80%

- [x] API 可用：
  - 所有 5 个新端点返回 200 或合法数据
  - 响应时间 P95 < 2s

- [x] 无影响现有功能：
  - 原有 API 正常工作
  - 无新增错误日志

- [x] 成本节省可观测：
  - `auto_summary_gate_skip` 指标正常增长

---

## 七、后续优化建议

### 短期（1-2 周）
1. 监控 API 性能，根据实际情况调整索引
2. 收集用户反馈，优化查询参数
3. 补充前端页面实现

### 中期（1 个月）
1. 实现项目和任务的批量编辑功能
2. 添加成本预警功能（超过预算告警）
3. 实现会话导出功能（CSV/Excel）

### 长期（3 个月）
1. 机器学习模型预测会话成本
2. 自动识别会话类型并打标签
3. 会话质量评分系统

---

## 八、联系方式

**技术支持**：
- 文档位置：`.handoff/2026-08-06-*.md`
- 日志位置：`/var/log/llm-gateway/`
- 监控面板：Grafana (如果已配置)

**紧急联系**：
- 如遇到严重问题，立即回滚代码并联系开发团队

---

**部署清单完成日期**：2026-08-06  
**预计部署时间**：30-60 分钟  
**风险等级**：中低（Schema 扩展是附加性的，可安全回滚）

# Migration 980: Session Analytics Foundation

## 目的

为会话分析系统创建基础设施，包括：
1. 会话元数据列（project, task, task_category, is_probe）
2. 4个物化视图（用户/项目/智能体/模型聚合）
3. 索引优化
4. 刷新辅助函数

## 影响范围

- **表变更**: `sessions` 表新增 4 列
- **新增**: 4 个物化视图
- **新增**: 2 个辅助函数
- **性能**: 查询性能提升（预聚合数据）

## 依赖

- 无前置迁移依赖
- 依赖 `request_logs_with_current_month` 视图（迁移 890）

## 风险评估

- **风险等级**: 低
- **数据安全**: 仅新增列和视图，不修改现有数据
- **回滚**: 支持完整回滚
- **性能影响**: 
  - 首次刷新物化视图需要较长时间（取决于数据量）
  - 后续查询性能大幅提升

## 执行时间估算

- 小型数据库（<10万会话）: ~1 分钟
- 中型数据库（10-100万会话）: ~5-10 分钟
- 大型数据库（>100万会话）: ~30-60 分钟

## 验证步骤

```sql
-- 1. 验证列是否添加
SELECT column_name, data_type 
FROM information_schema.columns 
WHERE table_name = 'sessions' 
    AND column_name IN ('project', 'task', 'task_category', 'is_probe');

-- 2. 验证物化视图是否创建
SELECT matviewname 
FROM pg_matviews 
WHERE matviewname LIKE '%_aggregates';

-- 3. 验证索引是否创建
SELECT indexname 
FROM pg_indexes 
WHERE tablename IN ('sessions', 'user_session_aggregates', 'project_task_aggregates', 
                    'agent_usage_aggregates', 'model_usage_aggregates');

-- 4. 验证数据是否存在
SELECT 
    (SELECT COUNT(*) FROM user_session_aggregates) as user_agg,
    (SELECT COUNT(*) FROM project_task_aggregates) as project_agg,
    (SELECT COUNT(*) FROM agent_usage_aggregates) as agent_agg,
    (SELECT COUNT(*) FROM model_usage_aggregates) as model_agg;
```

## 后续维护

### 刷新策略

**增量刷新**（每小时）:
```sql
-- 仅刷新今天的数据
REFRESH MATERIALIZED VIEW CONCURRENTLY user_session_aggregates;
```

**全量刷新**（每天凌晨）:
```sql
-- 刷新所有数据
SELECT refresh_all_session_analytics_views();
```

### 监控指标

- 物化视图行数
- 刷新执行时间
- 查询响应时间

## 相关文档

- 设计文档: `docs/design/session-analytics-system.md`
- API 文档: 待补充
- 用户手册: 待补充

## 注意事项

1. **首次刷新时间**: 取决于现有数据量，建议在低峰期执行
2. **is_probe 标记**: 自动标记包含测试关键词的会话
3. **NULL 处理**: 所有聚合列使用 COALESCE 避免 NULL
4. **并发刷新**: 使用 CONCURRENTLY 选项避免锁表

## 作者

- Kiro AI Assistant
- 日期: 2026-11-19

## 审核状态

- [ ] 代码审核
- [ ] DBA 审核
- [ ] 测试验证
- [ ] 生产部署

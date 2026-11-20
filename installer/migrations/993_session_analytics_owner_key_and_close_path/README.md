# Migration 993: Session Analytics Owner Key & Close Path

## 目的

1. 981 三个会话级物化视图（user/project_task/agent_usage_aggregates）owner 键
   统一为 `NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '')` 双源键
   （与 model_usage_aggregates:368 及 projects 下钻/窗口用量同族）；
2. 四视图 DROP+CREATE 重建——自愈「台账记了 applied、视图不存在」状态
   （252 实测：981 的视图级 SELECT 90s~197s，被共享 PG 服务端
   statement_timeout 30s 掐断）；
3. `user_session_aggregates` 新增 `end_user_count` 聚合列（users 列表换锚后
   API 契约保持）；
4. `session_dim` 补 `stop_reason` 列（bg lifecycle worker 与 admin 手动回收
   的写入目标，此前列不存在 ⇒ 42703，关闭路径全坏）。

## 影响范围

- **表变更**: `session_dim` 新增 1 列（stop_reason text）
- **重建**: 4 个物化视图（3 个换 owner 键 + end_user_count；1 个等价重建）
- **索引**: 随视图重建（名称与 981 逐字一致）

## 依赖

- 981（session_analytics_is_probe 函数、sessions 四列、session_summaries 列族）
- `request_logs_with_current_month` 视图（890 族）

## 风险评估

- **风险等级**: 中（DROP+CREATE 窗口内 API-only 端点短暂 42P01；在役旧实例
  读 session_owners 旧锚不受影响 ⇒ 部署窗口零用户可见影响）
- **数据安全**: 只重建可刷新的物化视图，不动基表数据
- **耗时**: 252 实测视图级 SELECT 90s~197s ⇒ 文件自带
  `SET statement_timeout='30min'`（这正是 981 在 252 半途而废的机制）

## 验证步骤

```sql
SELECT matviewname FROM pg_matviews
 WHERE matviewname IN ('user_session_aggregates','project_task_aggregates',
                       'agent_usage_aggregates','model_usage_aggregates');
SELECT column_name FROM information_schema.columns
 WHERE table_name='session_dim' AND column_name='stop_reason';
SELECT pg_get_viewdef('public.user_session_aggregates'::regclass, true);
-- owner 表达式应含 COALESCE(s.owner_user, sd.api_key_owner_user)
```

## 回滚

见同目录 down.sql（恢复 981 单源键形态；对 252 的语义注意点见该文件头注）。

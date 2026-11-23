# Migration 994: Session Analytics Owner Key & Close Path

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

### ★ 并行 / DSM 地雷（本文件自带 `max_parallel_workers_per_gather=0`）

- **触发条件**: 本迁移 Part 3 重建 `model_usage_aggregates` ⇒ 它是对
  `request_logs_with_current_month` 的**大聚合** ⇒ PG 会选**并行计划** ⇒
  并行 worker 需要 **resize DSM 共享内存段**（8MB/worker）。
- **地雷**: 211 的 PG 容器 `/dev/shm` 只有 **64MB**，且长期滞留 DSM 孤儿段
  （实测常态水位 35~43MB）⇒ 并行 worker 一 resize 就报
  `could not resize shared memory segment … No space left on device`
  （**SQLSTATE 53100**）⇒ 本迁移**直接 FATAL**。
- **为什么这条特别危险**: `SET max_parallel_workers_per_gather=0` 之前，
  `DROP MATERIALIZED VIEW` **已经执行** ⇒ 重建失败 ⇒ 生产上
  `model_usage_aggregates` 视图**被 DROP 后重建失败、视图缺失**
  （请求级读面挂）⇒ 且**不是偶发**（该地雷已在 request-logs 轮登记为 L1）。
- **修法**: 关并行 ⇒ 该视图走**串行**计划、不再申请 DSM。代价＝重建慢一点，
  可接受（视图是预聚合，不在请求路径上）。
- ★ **本 README 必须与 canonical 同步**：★ canonical 在
  `sql/migrations/startup/994_….sql`（★ 2026-11-23 05:32 的 `d587edc47`
  才补上这行 SET）⇒ ★ 本 README 曾长期**未反映** DSM/并行改动
  ⇒ ★ 三份副本（canonical / embeddata / 离线）的**注释与说明**也必须一致。

## 验证步骤

```sql
SELECT matviewname FROM pg_matviews
 WHERE matviewname IN ('user_session_aggregates','project_task_aggregates',
                       'agent_usage_aggregates','model_usage_aggregates');
SELECT column_name FROM information_schema.columns
 WHERE table_name='session_dim' AND column_name='stop_reason';
SELECT pg_get_viewdef('public.user_session_aggregates'::regclass, true);
-- owner 表达式应含 COALESCE(s.owner_user, sd.api_key_owner_user)

-- ★ 并行守卫：★ 关并行必须在 DROP/CREATE **之前**生效
SELECT count(*) FROM pg_matviews WHERE matviewname='model_usage_aggregates';
-- ★ 若重建后该视图缺失 ⇒ ★ 就是撞了 53100（★ 并行 worker 抢 DSM）
SELECT extname FROM pg_extension WHERE extname IN ('pg_stat_statements');
-- ★ DSM 孤儿段水位（★ 排查 53100 时看）
SELECT * FROM pg_backend_memory_contexts WHERE name LIKE '%DSM%';
```

## 回滚

见同目录 down.sql（恢复 981 单源键形态；对 252 的语义注意点见该文件头注）。

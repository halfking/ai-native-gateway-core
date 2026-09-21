-- 728 down: 移除 252 PG SQL 日志审计复核轮新增的 request_logs
-- credential+model 表达式索引（父壳连带分区子索引一并消失）。
-- 回滚后行为退回 728 前：抽屉轮询等 credential_id+模型 过滤当月分区的
-- 查询回到分区 Parallel Seq Scan（pg_stat_statements 10 天累计 8.6h、
-- 72h 窗实测 105s）。除非确认索引本身引发问题，不建议回滚。
-- hot 表索引 idx_request_logs_hot_credential_model_ts 属 sql/objects
-- 通道，本迁移未触碰，故此处亦不回滚。

DROP INDEX IF EXISTS public.idx_request_logs_credential_model_ts;

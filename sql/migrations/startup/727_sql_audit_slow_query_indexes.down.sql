-- 727 down: 移除 252 PG SQL 审计轮新增的三处慢查询索引。
-- 回滚后行为退回 727 前：stage_events 保留清理 DELETE 回到顺序扫描
-- （均值 10.5s / max 撞 30s 超时），session_turns 会话存在性 EXISTS 回到
-- 687ms P50。除非确认索引本身引发问题，不建议回滚。

DROP INDEX IF EXISTS public.idx_stage_events_created_at;
DROP INDEX IF EXISTS public.idx_session_turns_effective_session;
DROP INDEX IF EXISTS public.idx_session_turns_hot_effective_session;

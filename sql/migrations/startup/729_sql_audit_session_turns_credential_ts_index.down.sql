-- 729 down: 移除 252 部署验证轮新增的 session_turns credential+ts
-- 表达式索引（父壳连带分区子索引一并消失）+ 热侧独立索引。
-- 回滚后行为退回 729 前：抽屉轮询查询的 session_turns 分支回到裸 ts
-- 范围扫 + 逐行 CASE 过滤（72h 窗 252 实测残余 10.9s、本机 47.6 万行
-- 分区实测 ~10s）。除非确认索引本身引发问题，不建议回滚。

DROP INDEX IF EXISTS public.idx_session_turns_credential_ts;
DROP INDEX IF EXISTS public.idx_session_turns_hot_credential_ts;

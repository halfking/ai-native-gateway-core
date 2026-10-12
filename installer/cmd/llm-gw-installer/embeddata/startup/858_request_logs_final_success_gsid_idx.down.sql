-- 858_request_logs_final_success_gsid_idx.down.sql
-- 回滚：删除「会话终态晋级」的存在性索引。
-- ⚠ 回滚后 claimSessionFinalSuccess 会退回在月度分区上摸约 5.1 万个索引条目
--   并逐条回堆（生产实测 mean 987.3 ms / 每次 114,159 页 / 4.06% 全库数据库时间）。
--   仅在确认该索引带来计划劣化或显著写放大时才回滚；回滚前先看
--   pg_stat_user_indexes 里 request_logs_final_success_gsid_idx 的 idx_scan
--   是否真的在涨（不涨 = 索引没被用上，先查计划再决定）。

BEGIN;

DROP INDEX IF EXISTS public.request_logs_final_success_gsid_idx;

COMMIT;

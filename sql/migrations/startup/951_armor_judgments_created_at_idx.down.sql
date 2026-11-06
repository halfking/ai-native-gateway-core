-- 951 down：移除 created_at 前导索引，trim 批回到 (tenant_id, created_at)
-- 复合索引整条顺扫形态（缺陷回归即回退生效的证明面）。
DROP INDEX IF EXISTS public.idx_armor_judgments_created_at;

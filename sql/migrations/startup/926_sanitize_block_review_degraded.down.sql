-- 926 down：撤销降级标记列。
-- 列连同标记一起消失（回填信息随列删除）；不触碰行数据本身。
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE sanitize_block_reviews
    DROP COLUMN IF EXISTS degraded;

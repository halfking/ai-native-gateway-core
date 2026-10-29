-- 921 down：撤销拦截审核工作台（证据 + 标注 + 自动放行来源）。
--
-- ★ 证据表被删 ⇒ 已采集的摘录不可恢复（审核面回到「只有摘要与说明」的状态，
--   即迁移 917 的形态）。
-- ★ 审核记录上的标注列被删 ⇒ 人工标注知识丢失（误报计数归零 ⇒ 自动优化
--   的输入没了）。撤销前请确认不再需要这批标注。
-- ★ 放行来源的 CHECK 收回 ('manual','review') ⇒ source='auto_label' 的行
--   **必须先清理**，否则收 CHECK 会失败（约束拒绝现存非法值）。
--   清理语句见文件末尾（幂等，先删行再收约束）。

DROP TABLE IF EXISTS sanitize_block_evidence CASCADE;

-- 先清掉 auto_label 来源的放行行（否则收 CHECK 时被现存行顶住）。
DELETE FROM sanitize_egress_releases WHERE source = 'auto_label';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'sanitize_egress_releases_source_check'
          AND conrelid = 'public.sanitize_egress_releases'::regclass
    ) THEN
        ALTER TABLE sanitize_egress_releases
            DROP CONSTRAINT sanitize_egress_releases_source_check;
    END IF;
    ALTER TABLE sanitize_egress_releases
        ADD CONSTRAINT sanitize_egress_releases_source_check
        CHECK(source IN ('manual','review'));
END
$$;

DROP INDEX IF EXISTS sanitize_block_reviews_rule_label_idx;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'sanitize_block_reviews_label_check'
          AND conrelid = 'public.sanitize_block_reviews'::regclass
    ) THEN
        ALTER TABLE sanitize_block_reviews
            DROP CONSTRAINT sanitize_block_reviews_label_check;
    END IF;
END
$$;

ALTER TABLE sanitize_block_reviews
    DROP COLUMN IF EXISTS labeled_at,
    DROP COLUMN IF EXISTS labeled_by,
    DROP COLUMN IF EXISTS label_comment,
    DROP COLUMN IF EXISTS label,
    DROP COLUMN IF EXISTS session_id;

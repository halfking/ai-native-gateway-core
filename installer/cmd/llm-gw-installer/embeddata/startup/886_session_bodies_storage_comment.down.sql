-- Rollback for migration 886_session_bodies_storage_comment.sql.
--
-- 语义：**如实还原**迁移前的库状态 —— 把表注释改回 430 的原文。
--
-- ⚠ 为什么要「还原成不实的那句」：
--   回滚的定义是「回到迁移之前的样子」，不是「回到正确的样子」。
--   886 之前的库状态就是那句写着 columnar 的注释；不还原 ⇒ 回滚不是回滚。
--   若运维只是嫌新注释啰嗦、想保留一个正确但更短的版本，
--   正确做法是**再出一条迁移**改注释，而不是回滚 886。
--
-- ⚠ 代价提醒：还原后，\d session_bodies 又会显示「使用columnar存储格式」这句
--   不实描述（这正是 886 要修的那个问题）。本 down 只在「必须回到迁移前
--   状态」的场合使用（例如整轮迁移回退），不建议为了注释单独回退。
--
-- 幂等：COMMENT 是幂等操作。表不存在则跳过（不硬失败）。
-- 零锁、零 IO、零数据改动。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

DO $comment_886_down_session_bodies$
BEGIN
  IF to_regclass('public.session_bodies') IS NULL THEN
    RAISE NOTICE '886 down: public.session_bodies 不存在（表未建）⇒ 跳过';
    RETURN;
  END IF;

  COMMENT ON TABLE public.session_bodies IS
    'V2正文存储表：存储增量正文，避免request_logs的全量JSONB膨胀问题。
     使用columnar存储格式，配合zstd压缩，预计可节省60-80%磁盘空间。
     Created: 2026-07-17, Migration 430';
END
$comment_886_down_session_bodies$;

COMMIT;

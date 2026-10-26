-- 879_drop_session_summaries_redundant_session_key_uidx.sql
-- 2026-10-21：删掉 session_summaries 上与**主键键集逐字重复**的裸唯一索引。
--
-- -----------------------------------------------------------------------
-- 缺陷本体（runbook §143.16 的生产实测）
--
-- 生产 `public.session_summaries` 上有两条键集完全相同的唯一索引：
--   session_summaries_pkey              PRIMARY KEY (session_key)   67.8 MiB
--   session_summaries_session_key_uidx  UNIQUE (session_key)       67.2 MiB
-- 它们的 `pg_index.indkey` **都是 1** ⇒ 不是「前缀覆盖」，是**逐字重复**。
--
-- 为什么这是真缺陷（而不只是「看着冗余」）：
--   ① 这张表**持续写入**（每索引扫描数：uidx 3,644,644 次 / pkey 179,005 次）
--      ⇒ 每次写都要多维护一份 67 MiB 的 B-tree ⇒ **写放大直接翻倍**；
--   ② 点查计划器**优先选 uidx**（生产 EXPLAIN：Index Only Scan using
--      session_summaries_session_key_uidx）⇒ 它不是「没人用的那一份」，
--      恰恰相反，它是**热的那一份**；
--   ③ 而主键的键集与它逐字相同 ⇒ 删掉它，主键可以**原样接管**。
--
-- 为什么删掉不影响 ON CONFLICT：
--   `ON CONFLICT (session_key)` 的仲裁索引必须是**非部分唯一索引**，
--   主键本身满足这个条件（本地 PostgreSQL 17.11 实测）：
--     两份都在时：Conflict Arbiter Indexes: arb_t_pkey, arb_t_uidx
--     只剩 PK 时：Conflict Arbiter Indexes: arb_t_pkey
--   ⇒ 冲突语义**逐字不变**（且点查仍是 Index Only Scan，不是 Seq Scan）。
--
-- 守卫（★ 本迁移的全部安全性都在这条守卫里）：
--   只有当**还存在别的**可仲裁唯一索引时才删 ——
--   若表没有主键（memora 最小 shape），这条裸 uidx 就是
--   ON CONFLICT (session_key) **唯一**的仲裁器 ⇒ 删了会直接让写入失败。
--   ⇒ 那种情况下本迁移**不动手**（幂等，重跑即可）。
--
-- 为什么不是「在 bootstrap 里直接不建」：
--   迁移 655（applied+verified，SHA 登记在 docs/db-changelog.md）
--   也会建这条索引，而**已应用迁移不能改**（改它会破坏台账 SHA 校验）。
--   ⇒ 三段式：
--     ① 迁移 655 建（全新安装路径）
--     ② **本迁移 879 删**（本文件）
--     ③ runtime bootstrap（db/session_summaries_schema.go）**不再重建** ——
--        它的创建守卫已改成「session_key 上没有别的可仲裁唯一索引时才建」
--        （§143.16 的判据常量，两处共用：bootstrap + canonical 短路）
--   ⇒ 净效果：这条索引**不再存在**，且**不会被建回来**。
--
-- 收益：回收 ≈67.2 MiB 磁盘；省掉每次写的一份 67 MiB B-tree 维护。
--
-- ---------------------------------------------------------------------------
-- 怎么验收
--
--   SELECT indexrelid::regclass::text, pg_get_indexdef(indexrelid)
--     FROM pg_index WHERE indrelid = 'public.session_summaries'::regclass
--    ORDER BY 1;
--
-- 期望：键为 (session_key) 的唯一索引只剩 `session_summaries_pkey`（PRIMARY KEY），
--       **没有** `session_summaries_session_key_uidx`。
--
-- 反向验收（确认仲裁器还在，这是删之前最该看的那一眼）：
--
--   SELECT i.indexrelid::regclass::text
--     FROM pg_index i
--    WHERE i.indrelid = 'public.session_summaries'::regclass
--      AND i.indisunique AND i.indpred IS NULL AND i.indnkeyatts = 1;
--
-- 期望：至少一行（session_summaries_pkey）。若为空 ⇒ 守卫没生效，
--        本迁移不会删任何东西（幂等，重跑即可）。
-- ---------------------------------------------------------------------------

BEGIN;

SET LOCAL statement_timeout = '5min';
-- lock_timeout=2s —— 抢不到锁就**快速失败**、整条回滚，不排在写流量后面。
SET LOCAL lock_timeout = '2s';

DO $drop_redundant_uidx_879$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM pg_indexes
         WHERE schemaname = 'public'
           AND tablename = 'session_summaries'
           AND indexname = 'session_summaries_session_key_uidx'
    )
    AND EXISTS (
        -- 存在**别的**可仲裁唯一索引（主键，或任何 (session_key) 上的非部分唯一索引）
        SELECT 1
          FROM pg_index i
          JOIN pg_attribute a
            ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
         WHERE i.indrelid = 'public.session_summaries'::regclass
           AND i.indisunique
           AND i.indpred IS NULL
           AND i.indnkeyatts = 1
           AND a.attname = 'session_key'
           AND i.indexrelid <> to_regclass('public.session_summaries_session_key_uidx')
    ) THEN
        DROP INDEX public.session_summaries_session_key_uidx;
        RAISE NOTICE '879: dropped session_summaries_session_key_uidx (PK arbitrates ON CONFLICT (session_key))';
    ELSE
        RAISE NOTICE '879: skipped (index absent, or no other arbiter index on session_key)';
    END IF;
END $drop_redundant_uidx_879$;

COMMIT;

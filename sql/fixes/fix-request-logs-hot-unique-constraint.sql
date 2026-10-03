-- 修复 request_logs_hot 表上「(request_id, ts) 缺唯一索引/约束」的问题
-- 问题：INSERT ... ON CONFLICT (request_id, ts) 需要一个**恰好**建在这两列上的
--       唯一索引或唯一约束，缺了会在运行时报
--       `there is no unique or exclusion constraint matching the ON CONFLICT specification`。
--
-- R89-DV（210 号）更正 —— 原实现有三处失效：
--
--   ① **判据查错了系统目录**。原 `IF NOT EXISTS (SELECT 1 FROM pg_constraint
--      WHERE conrelid='request_logs_hot'::regclass AND contype='u')` 只看
--      **约束**，而 `CREATE UNIQUE INDEX` 产生的是 `pg_index.indisunique`、
--      **不进 pg_constraint**。⇒ 一个已经由 `migrations/startup/341` 用
--      `CREATE UNIQUE INDEX ... ON request_logs_hot (request_id, ts)` 建好的唯一
--      索引，对这段判据**完全不可见** ⇒ 脚本会再加一个**同列的重复唯一索引**
--      （PG 允许），在热表上白白多一份索引体积与写放大。
--      ⇒ 判据必须查 **pg_index**：`ON CONFLICT` 认的是**唯一索引**，唯一约束只是
--      「索引 + pg_constraint 里一行」的复合体，所以**查索引是两者交集的上位判据**。
--
--   ② **判据问的是「有没有任意唯一约束」，不是「有没有这一组列上的」**。
--      任何别的唯一约束（例如只在 `request_id` 上的）都会让原判据为真 ⇒
--      脚本报 `Unique constraint already exists` 而**什么都不做**，
--      而 `ON CONFLICT (request_id, ts)` 依然无法工作。**恒真的谓词不是门，是装饰。**
--
--   ③ **「验证」段无法失败**。原验证只是 `count(*)` 任意唯一约束再 `RAISE NOTICE`：
--      既不看列、也不看是否 valid、更不会在不符时中止 ⇒ 它证明不了任何事。
--      而本文件**没有** `\set ON_ERROR_STOP on` ⇒ 即便 `RAISE EXCEPTION`，
--      psql 默认也只是打印错误后**继续往下跑**。⇒ 验证必须落在**判据**上，
--      且必须让失败可见。
--
-- ⚠️ 已知代价（不做「CONCURRENTLY」是有原因的）：`ALTER TABLE … ADD CONSTRAINT`
-- 与直接建唯一索引在热表上都要取 ACCESS EXCLUSIVE 锁并扫全表。
-- **分区父表上无法用 CREATE UNIQUE INDEX CONCURRENTLY**，所以这不是可以随手优化掉的。
-- 请在低峰期执行。
--
-- 幂等：唯一索引已在 ⇒ 整段为 no-op。

\set ON_ERROR_STOP on

BEGIN;

-- 唯一性探针。放在 pg_temp ⇒ 随会话结束自动消失，不污染 public。
-- 三个条件缺一不可：
--   indisunique  —— 必须是唯一索引（唯一约束也满足，因为约束底层就是唯一索引）
--   indisvalid   —— INVALID 索引（例如 CONCURRENTLY 中途被打断留下的）planner 不可用
--   indpred IS NULL —— **部分**唯一索引带 WHERE 谓词，不能作为 ON CONFLICT 的推断依据
-- 且列清单必须**恰好**是 (request_id, ts)，顺序也一致。
CREATE OR REPLACE FUNCTION pg_temp.r89_has_unique_on(
    p_table regclass, p_cols text[]
) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1
        FROM pg_index i
        WHERE i.indrelid = p_table
          AND i.indisunique
          AND i.indisvalid
          AND i.indpred IS NULL
          AND i.indnkeyatts = cardinality(p_cols)
          AND (
              SELECT array_agg(a.attname::text ORDER BY x.ord)
              FROM unnest(i.indkey[0:i.indnkeyatts - 1]) WITH ORDINALITY AS x(attnum, ord)
              JOIN pg_attribute a
                ON a.attrelid = i.indrelid AND a.attnum = x.attnum
          ) = p_cols
    );
$$;

DO $$
DECLARE
    have_it boolean;
BEGIN
    IF to_regclass('public.request_logs_hot') IS NULL THEN
        RAISE EXCEPTION 'public.request_logs_hot 不存在；本脚本无适用对象，终止（不做任何改动）';
    END IF;

    SELECT pg_temp.r89_has_unique_on('public.request_logs_hot'::regclass,
                                     ARRAY['request_id','ts'])
      INTO have_it;

    IF have_it THEN
        RAISE NOTICE 'request_logs_hot 上已有 (request_id, ts) 唯一索引/约束 —— 无需改动';
    ELSE
        CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_hot_request_id_ts_unique
            ON public.request_logs_hot (request_id, ts);
        RAISE NOTICE '已建 idx_request_logs_hot_request_id_ts_unique ON (request_id, ts)';
    END IF;
END $$;

-- 验证：**门禁**而非 NOTICE。判据与上面同源（同一个探针函数），
-- 且失败即中止整个脚本（ON_ERROR_STOP + 显式事务 ⇒ 回滚）。
DO $$
DECLARE
    have_it boolean;
BEGIN
    SELECT pg_temp.r89_has_unique_on('public.request_logs_hot'::regclass,
                                     ARRAY['request_id','ts'])
      INTO have_it;
    IF NOT have_it THEN
        RAISE EXCEPTION
            '验证失败：public.request_logs_hot 上仍没有恰好建在 (request_id, ts) 上的'
            '有效唯一索引 ⇒ ON CONFLICT (request_id, ts) 仍会失败。回滚。';
    END IF;
    RAISE NOTICE '验证通过：public.request_logs_hot 上存在有效的 (request_id, ts) 唯一索引';
END $$;

COMMIT;

\echo ''
\echo 'request_logs_hot (request_id, ts) 唯一索引：已确认就绪'
\echo ''

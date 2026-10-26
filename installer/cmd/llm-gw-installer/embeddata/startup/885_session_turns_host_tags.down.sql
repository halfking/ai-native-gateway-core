-- Migration 885 (down): 撤销 host / tags 两列
--
-- 两侧同时删（父表 + hot）：迁移 707 的 promote 契约校验比较的是
-- (列名:类型:非空) 的**集合**，两侧一起少 ⇒ 集合仍全等 ⇒ promote 不会
-- 因列集合漂移而报错，因此 down 不需要回滚 promote 函数。
--
-- ⚠ 数据不可恢复：host / tags 的历史值随列一起消失（这是回滚的语义，
--   不是缺陷）。

BEGIN;

ALTER TABLE public.session_turns_hot
    DROP COLUMN IF EXISTS host,
    DROP COLUMN IF EXISTS tags;

ALTER TABLE public.session_turns
    DROP COLUMN IF EXISTS host,
    DROP COLUMN IF EXISTS tags;

COMMIT;

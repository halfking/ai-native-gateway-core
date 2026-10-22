-- ===========================================================================
-- File:          sql/migrations/startup/896_offline_activation_requests_columns_reassert.down.sql
-- Migration:     896 (down)
-- Purpose:       撤销 896 的**账本行**。列与索引**刻意不动**。
--
-- 为什么 down 不 DROP 列：
--   · 这三列与两个索引的**所有者是 375/376**，896 只是把它们重新交付到升级通道
--     漏掉的库上。在新装库（375/376 已建好、896 本身是 no-op）上，down 若 DROP 列
--     就会**越权**拆掉 375/376 的产物，把链状态破坏到 896 自己都没做过的地方。
--   · status / reject_reason / activation_code 是**承载数据**的列（离线激活审批结果），
--     DROP 等于销毁审批历史；可逆性的代价不该由业务数据承担。
--   · 要回到「生产缺这三列」那个形态本身就是缺陷形态（licensing 管理 SQL 42703），
--     不值得被 down 复现。
--   ⇒ down 的语义 = 「忘掉 896 跑过」。重新 apply 896 幂等，随时可补。
-- ===========================================================================

BEGIN;
DELETE FROM public.schema_migrations WHERE version = '896';
COMMIT;

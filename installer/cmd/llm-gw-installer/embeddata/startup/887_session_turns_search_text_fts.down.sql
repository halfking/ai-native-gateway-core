-- 887 down：删掉全文检索表达式 GIN 索引（父壳 + hot 侧）。
--
-- 与 up 同款：删父壳本身很快（只是解除 ATTACH 关系并 DROP 各个分区子索引），
-- 删 hot 侧独立一条。不动数据。
--
-- ⚠ 回滚的**真实后果**（请先读完再决定）：
--   回滚 887 之后，admin/logs.go 的 `?q=` 谓词**仍然是**带 FTS 腿的形态
--   （代码在 Go 侧，不随本迁移回滚）⇒ 查询里的
--       to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', $n)
--   将**没有索引承重** ⇒ 那一腿退化成逐行求值。
--   ⇒ 正确的回滚顺序是：**先回滚 Go 侧的谓词形态（把 FTS 腿摘掉），
--     再回滚本迁移**；单独回滚本迁移会让搜索变慢（不是变错）。
--   ⇒ 反过来（本迁移在、Go 侧还没改）也是安全的：查询里没有 FTS 腿时，
--     这条索引只是一个没人用的对象（877 同款权衡：索引是承重的，不是浪费）。
--
-- ⚠ 本文件**不需要** installer 的非事务豁免标记（718 那种）：两条语句都不是
--   CONCURRENTLY，在 --single-transaction 下也能原子执行，保留事务是有意为之。
--   （此处刻意不写出该标记的字面量——dbinit 目录那道门按"注释里出现标记字样"
--   判定，写出来解释反而会被算成声明豁免。）
BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

-- 删父壳会级联脱离并删除各分区的子索引（ATTACH 关系随之解除）。
DROP INDEX IF EXISTS public.idx_session_turns_search_fts;

-- hot 侧是独立表，父表索引不级联到它，必须单独删。
DROP INDEX IF EXISTS public.idx_session_turns_hot_search_fts;

COMMIT;

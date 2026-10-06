-- ===========================================================================
-- File:          sql/migrations/startup/834_supplier_errors_base_tables.down.sql
-- Migration:     834 (down)
-- Database:      llm_gateway
--
-- 回滚语义（先读这一段再决定要不要跑）
--
-- ⚠ 834 的 up 是**建表**，所以它的 down 是**删表**，而这三张表里装的是供应商
--   错误历史与预聚合结果。默认行为因此是**拒绝**，不是删除。
--
-- 为什么会这样：迁移纪律里「down 会丢什么」必须在动手前写清楚，而不是让人
-- 在跑完 psql 之后才发现。V371 建这些表时没有 down（它本来就只走升级路径），
-- 本迁移是它们第一次进入有 down 的受追踪链，那份责任就得在这里还上。
--
-- 三种情况：
--
--   1. 三张表**都为空** ⇒ 正常回滚，DROP 三张表。回到的状态是「这些表不存在」
--      —— 而那正是 828 会失败的状态。所以回滚 834 之后，**828 也必须回滚**，
--      否则链处于「828 引用着不存在的表」的破损态。
--
--   2. 任一张表**有行** ⇒ RAISE EXCEPTION，拒绝。删掉供应商错误历史不是一次
--      迁移回滚该顺手做的事。确需删除时，操作者应当显式处理：先归档/导出，
--      再手工 DROP，并且知道 828、813、699、703 都依赖这些表。
--
--   3. `supplier_errors` 上挂了任何月度分区 ⇒ 一律拒绝。分区是 813 / ensure
--      tick 建的，它们的生命周期不由 834 决定；把有分区的父表 DROP 掉会连带
--      丢掉分区数据，且与 813 的列存→堆转换记账脱钩。
--
-- ★ 不删索引、策略、注释：它们随表一起走。单独留下会变成悬空对象。
--
-- ★ 也不碰 `supplier_errors_unified` 视图：它由 828 定义，不是 834 的对象。
--   删表会让该视图变成悬空依赖（PG 允许，所以不会报错——**这正是它危险的地方**：
--   回滚后 828 的 CREATE VIEW 已经跑过，下一次重放会因视图已存在而需要人处理）。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '10min';

DO $guard$
DECLARE
    n_partitions integer;
    n_rows       bigint;
    populated    text;
BEGIN
    -- 情况 3：挂了子分区 ⇒ 拒绝。
    --
    -- ⚠ 这里查的是 pg_inherits（**子分区个数**），不是 pg_partitioned_table。
    --   两者在「父表是分区表」时**前者为 0、后者为 1** —— 查错表会让守卫在
    --   「刚建完、一个分区都没有」的空库上也拒绝回滚，于是 down 永远跑不动。
    --   （初版写成 pg_partitioned_table，实测 rc=3 拒绝，而那个库里分区数是 0。）
    SELECT count(*) INTO n_partitions
      FROM pg_inherits
     WHERE inhparent = 'public.supplier_errors'::regclass;

    IF n_partitions > 0 THEN
        SELECT count(*) INTO n_rows FROM public.supplier_errors;
        RAISE EXCEPTION
            '834 down refused: public.supplier_errors has % child partition(s) holding % row(s). '
            'Those partitions were created by the ensure tick / 813, not by 834, so 834 is not the '
            'right place to unwind them. Archive first, then drop deliberately — and remember 828, '
            '813, 699 and 703 all depend on this table.',
            n_partitions, n_rows;
    END IF;

    -- 情况 2：有行 ⇒ 拒绝
    SELECT string_agg(t.name || '=' || t.n, ', ') INTO populated
      FROM (
        SELECT 'supplier_errors_hot' AS name, count(*) AS n FROM public.supplier_errors_hot
        UNION ALL
        SELECT 'supplier_error_stats', count(*) FROM public.supplier_error_stats
      ) AS t
     WHERE t.n > 0;

    IF populated IS NOT NULL THEN
        RAISE EXCEPTION
            '834 down refused: these tables are not empty (%). Dropping them would destroy '
            'supplier error history and rollup results. Archive first, then drop deliberately.',
            populated;
    END IF;
END
$guard$;

-- 情况 1：三张表都空 ⇒ 正常回滚
DROP TABLE IF EXISTS public.supplier_error_stats;
DROP TABLE IF EXISTS public.supplier_errors;
DROP TABLE IF EXISTS public.supplier_errors_hot;

-- ⚠ 这条 NOTICE 必须写成**一个**字符串字面量：PL/pgSQL 的 RAISE 第一个参数
--   是格式串**字面量**，既不接受 `||` 拼接，也不接受相邻字面量隐式合并。
--   两种写法都实测报 syntax error（rc=3），而另两条拒绝路径都正常 ⇒ 只跑
--   「有数据」的臂会完全看不见它，只有「该成功的那条路径」会炸。
DO $notice$
BEGIN
    RAISE NOTICE '834 down: dropped the empty supplier_errors family; 828 will fail on replay until it is rolled back or 834 re-applied — that is the honest end state, because before 834 these tables did not exist on the fresh-install path at all.';
END
$notice$;

COMMIT;

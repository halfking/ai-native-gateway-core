-- ===========================================================================
-- File:          sql/migrations/startup/872_credential_model_index_partitions_heap.sql
-- Migration:     872
-- Database:      llm_gateway
-- Purpose:       把 credential_model_index 的现存月度分区从 columnar 转回 heap，
--                关闭「白名单已摘、函数已 heap、存量未转」的三缺一状态。
--
-- Status:        active
-- Idempotent:    YES（新文件无台账行 ⇒ 每次部署都跑；本迁移自身幂等：
--                 非 heap 分区集合为空时循环体不执行；自证门对 heap 态恒真）
-- Dependencies:  337/R17 治理函数族（columnar_insert_only_parents /
--                columnar_healthcheck / enforce_columnar_partition，
--                均已在 252 活库与三基线为现行版）
--
-- ── 立项依据（2026-10-18 252 SQL 审计轮实测，不是推演）──────────────────
--
-- 1) 症状：autoroute 的索引刷新 SQL（autoroute/index.go refreshIndexSQL，
--    admin/auto_route.go 同款）在 pg_stat_statements 里 mean_exec_time=2310ms、
--    max=25229ms、calls=4383、total≈2.8h —— 252 活库 in-repo 语句第一名。
--    252 SQL 日志窗口（10-17 03:44 → 10-18 06:02）内 ≥1s 慢查 488 次
--    （平均 4.2s / 最长 26s），全天平坦分布 ⇒ 是内在慢，不是夜间负载连带。
--
-- 2) 归因：EXPLAIN (ANALYZE, BUFFERS) 实测 89% 时间在两个月分区的 423k 行
--    列存扫描（2026_09 707ms + 2026_10 730ms），GROUP BY 聚合只占 ~120ms。
--    同一扫描 2026_09（301k 行）只要 660 buffers，2026_10（121k 行）却要
--    16308 buffers —— 842 索引救不了它（842 注释自己预告过：索引不减少
--    GROUP BY 要读的行数，PG17 无 loose index scan）。
--
-- 3) 病根：本族是 **5 分钟增量小批量写**（lifecycle 登记语：
--    「凭据模型健康度索引（5min rollup）」）。citus columnar 是批量装载取向，
--    每个写事务落独立 stripe ⇒ 2026_10 被打成海量微小 stripe，每次扫描都在
--    反复触碰 stripe 头（16308 buffer hits vs 主文件仅 970 页）。2026_09 是
--    一次性批量转入所以只有 660 hits —— 同一 AM、同一查询、差 25 倍。
--    本仓对「列存 × 增量写」的反模式已有两轮先例：806（bodies）、812
--    （model_probe_runs，列存拒绝 UPDATE 计划）、813/836（supplier_errors，
--    含「转了函数没转存量」形态本身）。
--
-- 4) 为什么只剩存量这一条腿：
--    columnar_insert_only_parents() 活库与三基线均已是 {routing_decision_log}
--    （CMI 已摘）；ensure_credential_model_index_partition（694）建的是 heap
--    分区（无 USING columnar、不调 enforce_columnar_partition）；
--    columnar_healthcheck() 活库已把本族列为 should_be_heap 并**正在报
--    non-compliant**（2026-10-18 实测三行：2026_09/10/11 storage=columnar
--    expected=heap compliant=f）。⇒ 检测面在说话，函数面已正确，
--    唯独存量分区没人转 —— 与 836 对 813 的判定同构。
--
-- ── 预期收益 ──────────────────────────────────────────────────────────
--
--    heap 后同扫描为 ~42MB 顺序读（warm 缓存），EXPLAIN 实测参照 836 同族：
--    刷新语句 2.3s → 亚秒级（扫描臂 1.7s → ~0.1s）。842 的
--    (credential_id, raw_model, bucket DESC) 索引保留不动。
--    附带修复：columnar 分区从不 autoanalyze（838 注释实测），转 heap 后
--    autoanalyze 恢复，统计信息不再冻结。
--
-- ── 为什么用 DETACH→重建→回拷 而不是 SET ACCESS METHOD ──────────────────
--
--    沿用 813/836 的带数据通道（bak 改名 → 同边界重建 heap 叶 → 回拷 →
--    守恒校验 → DROP bak）：通道在活库上被 supplier_errors 验证过，守恒
--    校验（新 ≥ bak）天然容忍转换窗口内的并发 promote 落叶；
--    且空分区走空壳通道（812 同款 + TOCTOU 收口）。分区内行数最大 301k，
--    通道全程秒级。
--
-- 回滚：.down.sql 无操作（heap 是治理面声明的终态；回滚到 columnar 需要
--       先把本族加回 columnar_insert_only_parents，那不该由一个 down 文件决定）。
--       台账仍记 down 文件存在（通道登记需要 .down 文件成对）。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '10min';
-- DETACH/ATTACH/CREATE .. PARTITION OF 都是 ACCESS EXCLUSIVE 级强锁 DDL：
-- 抢不到锁 2s 内快速失败（870 同款），等锁期间不堵全库。
SET LOCAL lock_timeout = '2s';
-- 分区边界是 date 文本，pg_get_expr 的渲染随会话时区变化；与 836 同款钉扎，
-- 保证重建边界与日志/台账指纹同口径。
SET LOCAL TIME ZONE 'Asia/Shanghai';

-- ── 现存非 heap 月度分区转 heap（逐字沿用 836 第 3 步的通道，换目标族）──────

DO $$
DECLARE
    part       record;
    v_rows     bigint;
    v_bak      text;
    v_bak_rows bigint;
    v_new_rows bigint;
BEGIN
    -- to_regclass 守卫（612 层纪律）：目标表缺席的库上直通跳过。
    IF to_regclass('public.credential_model_index') IS NULL THEN
        RAISE NOTICE '872: public.credential_model_index absent; nothing to convert';
        RETURN;
    END IF;

    FOR part IN
        SELECT c.relname AS name,
               pg_get_expr(c.relpartbound, c.oid) AS bound
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_am   a ON a.oid = c.relam
         WHERE i.inhparent = 'public.credential_model_index'::regclass
           AND a.amname <> 'heap'
         ORDER BY c.relname
    LOOP
        EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_rows;

        IF v_rows = 0 THEN
            -- 空壳通道（812 同款，含 TOCTOU 收口）。
            EXECUTE format('ALTER TABLE public.credential_model_index DETACH PARTITION public.%I', part.name);
            EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_rows;
            IF v_rows > 0 THEN
                EXECUTE format('ALTER TABLE public.credential_model_index ATTACH PARTITION public.%I %s',
                               part.name, part.bound);
                RAISE NOTICE '872: partition % raced non-empty during detach (% rows); re-attached, left untouched', part.name, v_rows;
                CONTINUE;
            END IF;
            EXECUTE format('DROP TABLE public.%I', part.name);
            EXECUTE format(
                'CREATE TABLE public.%I PARTITION OF public.credential_model_index %s',
                part.name, part.bound);
            RAISE NOTICE '872: rebuilt partition % as heap (was empty columnar)', part.name;
        ELSE
            -- 带数据通道（689/811/813 同款）：bak 改名 → 同边界重建 heap 叶 →
            -- 回拷 → 守恒校验（新 ≥ bak；并发 promote 落进新叶只会多不会少）
            -- → DROP bak。
            v_bak := part.name || '_am872_bak';
            EXECUTE format('ALTER TABLE public.credential_model_index DETACH PARTITION public.%I', part.name);
            EXECUTE format('ALTER TABLE public.%I RENAME TO %I', part.name, v_bak);
            EXECUTE format(
                'CREATE TABLE public.%I PARTITION OF public.credential_model_index %s',
                part.name, part.bound);
            EXECUTE format('INSERT INTO public.%I SELECT * FROM public.%I', part.name, v_bak);
            EXECUTE format('SELECT count(*) FROM public.%I', v_bak) INTO v_bak_rows;
            EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_new_rows;
            IF v_new_rows < v_bak_rows THEN
                RAISE EXCEPTION '872: parity check failed for %: bak=% new=% (rows lost?)',
                                part.name, v_bak_rows, v_new_rows;
            END IF;
            EXECUTE format('DROP TABLE public.%I', v_bak);
            RAISE NOTICE '872: converted % to heap (% rows moved)', part.name, v_bak_rows;
        END IF;
    END LOOP;
END $$;

-- ── 落地自证（836 第 4 步同纪律：只有断言能关掉「记账为已应用、活库没变」）──

DO $verify$
DECLARE
    v_n_bad integer;
    v_bad   text;
    v_heap  integer;
BEGIN
    -- (a) 治理面必须仍然认识本族为 heap（防有人顺手把 healthcheck 的
    --     should_be_heap 数组缩水，让「漂移可见」重新变盲）。
    IF pg_get_functiondef('public.columnar_healthcheck()'::regprocedure) !~ 'credential_model_index' THEN
        RAISE EXCEPTION '872 self-check failed: columnar_healthcheck() no longer lists credential_model_index as should_be_heap; the drift detector for this family would go blind.';
    END IF;

    -- (b) 白名单必须不含本族（防 ensure/事件触发器把分区再转回 columnar）。
    IF 'credential_model_index' = ANY (columnar_insert_only_parents()) THEN
        RAISE EXCEPTION '872 self-check failed: credential_model_index is back in columnar_insert_only_parents(); the next ensure tick would re-columnarize converted partitions.';
    END IF;

    -- (c) 一个月度分区都不许留在非 heap 访问方法上。
    SELECT count(*), coalesce(string_agg(c.relname, ', '), '(none)')
      INTO v_n_bad, v_bad
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_am   a ON a.oid = c.relam
     WHERE i.inhparent = 'public.credential_model_index'::regclass
       AND a.amname <> 'heap';
    IF v_n_bad > 0 THEN
        RAISE EXCEPTION '872 self-check failed: % credential_model_index partition(s) are still not heap: %', v_n_bad, v_bad;
    END IF;

    SELECT count(*) INTO v_heap
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
     WHERE i.inhparent = 'public.credential_model_index'::regclass;
    RAISE NOTICE '872 self-check: all % credential_model_index partitions are heap, healthcheck knows the family, whitelist excludes it.', v_heap;
END
$verify$;

COMMIT;

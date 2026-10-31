-- ===========================================================================
-- File:          sql/migrations/startup/914_session_turns_origin_stage_check.sql
-- Migration:     914
-- Database:      llm_gateway
-- Purpose:       把 v1 族退役时丢掉的 origin_stage **DB 级 CHECK** 补回会话族。
--
-- ── 缺口本体（实测，非推断）────────────────────────────────────────────
-- v1 族的 `request_logs.origin_stage` 上有一条 DB 级 CHECK：
--
--   request_logs_origin_stage_check CHECK (origin_stage IS NULL OR
--     origin_stage IN ('self_check','node_probe','system_health','business',
--                       'probe_direct','probe_v2','model_probe','passive_probe','manual'))
--
-- 它随 890 的 v1 族 DROP 一起消失，而**会话族从未重建**：
--   session_turns.origin_stage       / session_turns_hot.origin_stage
--   都是 `text` 且**没有任何 CHECK**（PG 上 pg_constraint 实测 0 条）。
--
-- ⇒ 于是「落库前的闸门」只剩 middleware/origin_mw.go 的 isValidOriginStage
--   （Go 侧唯一守卫），**DB 级兜底彻底消失**。
-- ⇒ 后果（★★ 消费方实测：★★ **消费方有【两种形态】，故障后果【不同】** ——
--   ★★ 本迁移初稿把两种形态写成一类，那是**事实错误**，已订正）：
--   ① **排除集形态** `COALESCE(origin_stage,'') NOT IN (8 个探针 stage)`：
--      admin/analytics.go · bg/mv_consistency.go · db/db.go
--      ⇒ 一个越界值落进去 ⇒ 它既不在排除集里、也不在允许集里
--        ⇒ **探针流量被算成业务流量** + 对账被污染。
--   ② **白名单形态** `COALESCE(origin_stage,'business') = 'business'`：
--      admin/routing.go · bg/probe_policy.go（**会话族**那条）
--      ⇒ 越界值 ≠ 'business' ⇒ **被排除**
--        ⇒ **业务流量被静默丢弃**（少计）⇒ 这是**另一种**故障（不是污染）。
--   ⇒ ⇒ 两种形态下**都没有任何一层会报错** ⇒ ⇒ DB 级兜底仍然必须补。
--   ★ 附注：bg/probe_policy.go 里**另一条**同名谓词（`probeTrafficExclusionPredicate`）
--     带的是 `_request_logs` 别名 ⇒ 属 **v1 族**，**不是**会话族的消费方。
--
-- ── 允许集（唯一真值源）──────────────────────────────────────────────
-- internal/probekind.ValidStages() = ProbeStages(8) + BusinessStage('business')。
-- 该函数自己的注释就写着「DB CHECK 允许的全部 origin_stage 取值」，
-- 且 internal/probekind/probekind.go 的 IsValidStage 注释明写
-- 「落库前的闸门与落库后的约束必须一致，否则会出现『中间件认、DB 拒』的行」。
-- ⇒ 本迁移把那份真值落到 DB ⇒ 两道闸门重新一致。
--
-- ── 两侧都要加（会话族自己的约定，见 706b）──────────────────────────
-- 706b 把父表的 CHECK 克隆到 hot 并改名成 `session_turns_hot_*`；
-- hot 的 bootstrap（embeddata/startup/session_turns_hot_bootstrap.sql）
-- 也自带同样 8 个 `_hot_` 命名的 CHECK。
-- ⇒ 本迁移对**父表与 hot 各加一条**，命名分别为
--   session_turns_origin_stage_check / session_turns_hot_origin_stage_check。
--
-- ── 为什么用 NOT VALID + VALIDATE 两步 ───────────────────────────────
-- ADD CONSTRAINT ... CHECK 在 PG 上要**全表扫描校验存量行**，
-- 且在分区父表上会连带拿住**所有分区**的 AccessExclusiveLock
-- ⇒ 252 实测 114 万行 ⇒ 一条 ALTER 把写入堵住数秒。
-- 分两步：
--   ① ADD CONSTRAINT ... NOT VALID —— 不扫存量、瞬时完成，
--      但**新行立刻受约束**（PG 17 实测：分区副本同样 NOT VALID 且照样拒新行）；
--   ② VALIDATE CONSTRAINT —— 只拿 ShareUpdateExclusiveLock（不阻塞读写），
--      且在分区父表上**一条就覆盖全族**（PG 17 实测：父表 VALIDATE ⇒
--      各分区的 convalidated 同步变 true，含 DEFAULT 分区）。
--
-- ── 锁守卫（★ 这里用会话级 SET，不是 SET LOCAL）──────────────────────
-- ★ 实测（本轮，PG 17.6）：`SET LOCAL` 只在事务块内有效。
--   部署通道 scripts/apply-db-revision-sequence.sh 走 `psql -f -`
--   **不加 --single-transaction**、也不注入 BEGIN ⇒ 每个语句各自成事务
--   ⇒ `SET LOCAL lock_timeout` 只打一条 WARNING 后**完全失效**。
--   （installer 通道走 --single-transaction ⇒ 那条路径上 SET LOCAL 才有效。）
-- ⇒ 本文件用**会话级** `SET lock_timeout` ⇒ 两条通道都真的生效。
--   ★ 取 2s 而非更长：PG 的锁队列里，**等锁期间新事务会排在它后面**
--   ⇒ 一条迟迟拿不到锁的 AccessExclusive 会把整张表的写入一起堵住
--   （§44 的生产事故：一条 CREATE TABLE 堵了 14 个网关会话 1.5 分钟）。
--   ⇒ 必须**快速失败**而不是排队等锁。
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '120s';

DO $do$
DECLARE
    -- 规范允许集：与 internal/probekind.ValidStages() 一一对应。
    --   8 个探针 stage（probekind.ProbeStages）+ 'business'（probekind.BusinessStage）
    c_canon text[] := ARRAY[
        'self_check', 'node_probe', 'system_health', 'business',
        'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual'
    ];

    -- 目标清单：父表（分区表）+ hot 表（普通表）。
    --   约束名沿用会话族约定（父表 `session_turns_*` / hot `session_turns_hot_*`，
    --   与 706b 的克隆改名形态一致）。
    c_targets text[][] := ARRAY[
        ARRAY['session_turns',     'session_turns_origin_stage_check'],
        ARRAY['session_turns_hot', 'session_turns_hot_origin_stage_check']
    ];

    v_i        integer;
    v_table    text;
    v_con      text;
    v_def      text;
    v_have     text[];
    v_canon_sorted text[];
    v_list     text;
    v_exists   boolean;
    v_canonical boolean;
BEGIN
    SELECT array_agg(x ORDER BY x) INTO v_canon_sorted FROM unnest(c_canon) AS x;

    -- ★ 用下标循环而不是 FOREACH：FOREACH 的循环变量不能是数组类型，
    --   而本清单的元素本身是 text[]（表名 + 约束名两个元素）。
    FOR v_i IN 1..array_length(c_targets, 1) LOOP
        v_table := c_targets[v_i][1];
        v_con   := c_targets[v_i][2];

        IF to_regclass(format('public.%I', v_table)) IS NULL THEN
            RAISE WARNING '914: public.% 不存在，跳过（约束 %）', v_table, v_con;
            CONTINUE;
        END IF;

        IF NOT EXISTS (
            SELECT 1 FROM pg_attribute a
            WHERE a.attrelid = format('public.%I', v_table)::regclass
              AND a.attname = 'origin_stage'
              AND a.attnum > 0
              AND NOT a.attisdropped
        ) THEN
            RAISE WARNING '914: public.% 没有 origin_stage 列，跳过（约束 %）', v_table, v_con;
            CONTINUE;
        END IF;

        -- 读现状：约束在不在、定义长什么样
        v_exists := false;
        v_def := NULL;
        SELECT pg_get_constraintdef(c.oid) INTO v_def
        FROM pg_constraint c
        WHERE c.conrelid = format('public.%I', v_table)::regclass
          AND c.conname = v_con
          AND c.contype = 'c';
        v_exists := (v_def IS NOT NULL);

        -- 规范形态判定（定义感知）：
        --   ① 定义里必须点名 origin_stage；
        --   ② 该 CHECK 里**唯一的单引号字面量**就是允许集本身
        --      ⇒ 抽出全部字面量去重排序，与规范集逐项对拍。
        -- ⇒ 「名字在但定义不对」⇒ 判为非规范 ⇒ 重建。
        v_canonical := false;
        IF v_exists THEN
            SELECT COALESCE(array_agg(DISTINCT m[1] ORDER BY m[1]), ARRAY[]::text[])
            INTO v_have
            FROM regexp_matches(v_def, '''([^'']*)''', 'g') AS m;

            v_canonical := (v_def LIKE '%origin_stage%') AND (v_have = v_canon_sorted);
        END IF;

        IF v_canonical THEN
            RAISE NOTICE '914: public.% 上的 % 已是规范形态，跳过重建', v_table, v_con;
        ELSE
            IF v_exists THEN
                -- 名字在但定义不是规范形态 ⇒ 先删再加。
                -- ★ 分区父表上的继承约束**不能单独删**（PG 报
                --   "cannot drop inherited constraint"）⇒ 必须在**父表**上删，
                --   PG 会连带把各分区的副本一起删掉。
                EXECUTE format('ALTER TABLE public.%I DROP CONSTRAINT IF EXISTS %I', v_table, v_con);
            END IF;

            v_list := (
                SELECT string_agg(quote_literal(x), ', ' ORDER BY x)
                FROM unnest(c_canon) AS x
            );
            -- NOT VALID：不扫存量、瞬时完成，但新行立刻受约束。
            EXECUTE format(
                'ALTER TABLE public.%I ADD CONSTRAINT %I CHECK (origin_stage IS NULL OR origin_stage IN (%s)) NOT VALID',
                v_table, v_con, v_list);
        END IF;

        -- 第二步：校验存量行。
        --   ShareUpdateExclusiveLock ⇒ 不阻塞读写；在分区父表上**一条覆盖全族**。
        --   若存量里真有越界值 ⇒ 这里报错 ⇒ 迁移失败 ⇒ 部署停下 ⇒ 人工裁决。
        --   （本地与 252 实测越界行数均为 0。）
        EXECUTE format('ALTER TABLE public.%I VALIDATE CONSTRAINT %I', v_table, v_con);

        RAISE NOTICE '914: public.% 的 % 已就位且已校验', v_table, v_con;
    END LOOP;
END
$do$;

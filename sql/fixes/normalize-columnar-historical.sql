-- =============================================================================
-- normalize-columnar-historical.sql
-- 2026-07-14: One-shot cleanup of historical mixed-case rows that live
-- inside the citus_columnar AM (append-only, no UPDATE support).
--
-- R89-DZ（212 号）—— 本脚本在 212 号被**中止**（preflight 直接 RAISE EXCEPTION）。
-- 原因：它声称做三件事，**实际只做了第一件**，而且做的那一件足以损坏存储布局。
-- 逐条坐实（证据见文末「可重放证据」）：
--
--   ① 声称「applies lower() to the model name columns」—— **`lower()` 从未出现在
--      任何写路径上**。全文件 `lower(` 只出现在：注释、`\echo` 标签、以及**两段
--      验证 SELECT**（旧 :70 / :116-117）。真正的复制是
--      `INSERT INTO … SELECT id, ts, …` 与 `INSERT INTO %I SELECT * FROM %I`
--      —— **逐值原样拷贝**。
--      ⇒ 结果：花掉「重写整张大关系、阻塞写入」的代价，数据**一个字节都没变**。
--
--   ② 声称「converts them back to columnar」—— **该步骤根本不存在**。
--      全文件 `SET ACCESS METHOD` / `USING columnar` **零命中**。
--      脚本只把 columnar 分区重建成 **heap**，然后打印
--      `=== columnar historical lowercase rebuild: done ===`。
--      ⇒ **跑完它，request_logs 的历史分区就从 columnar 变成 heap**，
--        而这与本仓「大数据表以 hot+分区（columnar）表完成」的架构要求**直接冲突**
--        （体积与查询性能都会显著劣化），且它**报告成功**。
--
--   ③ 第 2 步的 `ATTACH PARTITION … FOR VALUES FROM (…) TO (…)` 把**同一个**
--      `pg_get_expr(relpartbound, …)` 同时当作 FROM 和 TO 传入。
--      而 `pg_get_expr` 返回的是**整条** `FOR VALUES FROM (…) TO (…)` 子句，
--      不是某一个端点 ⇒ 拼出来的是
--      `FOR VALUES FROM ('FOR VALUES FROM (…) TO (…)') TO ('FOR VALUES FROM (…) TO (…)')`。
--      即使能过语法检查，边界也是垃圾。
--
-- ⇒ 212 号的选择：**在动数据之前中止**，而不是"修好一半然后报告 done"。
--   一份会静默把列存改回行存、还自称 done 的脚本，**中止比运行安全**。
--
-- ── 要让它重新可用，需要完成（212 号已把可确定的错误改在下方，但**未跑过真库**）──
--   A. 实现第 3 步：重建完成后把分区转回 columnar
--      （`ALTER TABLE … SET ACCESS METHOD citus_columnar`，或按本仓既有迁移的写法），
--      并在转回后**验证** `pg_class.relam` 确实指向 columnar AM。
--   B. 在真库上跑通并核对行数守恒：
--      `SELECT count(*)` 在重命名前后必须相等（当前脚本**没有任何行数校验**）。
--   C. 与分区管理器/保留期任务协调：重建期间历史分区不可写，
--      且 `promote_request_logs_hot_to_partition_interval_integer` 会往这些分区灌数据。
--   D. 把下面 preflight 的 `\if` 打开（即显式承认 A 已完成）才允许执行。
--
-- ── 保留在文件里的部分（212 号已修，但**没有真库验证**）──
--   · 写路径真正应用 `lower()`（原来只是注释和验证里提过）
--   · `ATTACH PARTITION` 的边界改为取**真实的两个端点**，而不是整条子句
--   · 两段验证从「打印一个数」改成**会中止的门禁**（原来即使 mixed 仍非 0 也退出 0）
--
-- 全部步骤原设计为幂等；OPERATOR 必须确保运行期间没有写入
-- candidate_failure_logs / request_logs。
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== columnar historical lowercase rebuild: R89-DZ（212 号）preflight ==='

-- ---------------------------------------------------------------------------
-- PREFLIGHT：212 号加。三条缺陷里 ② 最危险（静默把列存改回行存并报告 done），
-- 而修复它需要「转回 columnar」这一步存在 —— 该步骤 212 号**没有实现**，
-- 因为无法在本机（无 PG、无 citus_columnar）验证。
-- ⇒ 在给出显式承认之前**拒绝执行**。
-- 把 \if 换成 1 只应在 A/B/C 三项都做完、并在真库上验过之后。
-- ---------------------------------------------------------------------------
\if :{?r89_allow_incomplete_columnar}
\else
  \echo '*** ABORTED（212 号）***'
  \echo '本脚本目前会：(1) 逐值原样拷贝、根本没应用 lower()；'
  \echo '             (2) 把历史分区重建成 heap、且没有转回 columnar 的步骤；'
  \echo '             (3) ATTACH 的分区边界参数是垃圾。'
  \echo '跑完它会静默把 request_logs 历史列存改回行存，并打印 done。'
  \echo '详见文件头部。确认已按头部 A/B/C 修好之后，用'
  \echo '    psql -v r89_allow_incomplete_columnar=1 -f <此文件>'
  \echo '显式放行。'
  \quit
\endif

\echo '=== columnar historical lowercase rebuild ==='

-- ---------------------------------------------------------------------------
-- 1. candidate_failure_logs (single heap-like relation, not partitioned).
--    重命名原表 → 用同一 schema 建 rowstore → **逐列 lower() 拷贝** → 删原表。
--    ⚠️ 212 号修：原来 `INSERT … SELECT id, ts, credential_id, …` 是**原样拷贝**，
--       `lower()` 只存在于注释和下面的验证里。
-- ---------------------------------------------------------------------------
\echo '--- 1. candidate_failure_logs: rebuild as rowstore + lower() ---'
DO $do$
DECLARE
    rec record;
BEGIN
    SELECT relname, amname INTO rec
    FROM pg_class c JOIN pg_am a ON a.oid = c.relam
    WHERE c.oid = 'candidate_failure_logs'::regclass;
    IF rec.amname = 'columnar' THEN
        ALTER TABLE candidate_failure_logs RENAME TO candidate_failure_logs_columnar_old;
        CREATE TABLE candidate_failure_logs (LIKE candidate_failure_logs_columnar_old INCLUDING ALL);
        INSERT INTO candidate_failure_logs
        SELECT
            id, ts, credential_id, provider_id, request_id, error_kind,
            error_message,
            lower(raw_model_name),           -- 212 号：这里原本是原样拷贝
            raw_status_code, error_class, attempt,
            created_at
        FROM candidate_failure_logs_columnar_old;
        DROP TABLE candidate_failure_logs_columnar_old;
        RAISE NOTICE 'candidate_failure_logs: rowstore rebuild + lower() complete';
    ELSE
        RAISE NOTICE 'candidate_failure_logs: already rowstore, no rebuild needed';
    END IF;
END
$do$;

-- 212 号：原来是「打印一个数」—— 即使 mixed 仍然非 0 也以 0 退出。
-- 改成**会中止的门禁**（§130：列不出失败分支的验证不是验证，是打印）。
DO $do$
DECLARE mixed bigint;
BEGIN
    SELECT COUNT(*) INTO mixed FROM candidate_failure_logs
    WHERE raw_model_name IS NOT NULL AND raw_model_name <> lower(raw_model_name);
    IF mixed > 0 THEN
        RAISE EXCEPTION 'candidate_failure_logs 仍有 % 行 raw_model_name 不是小写；'
                        '重命名前请确认表结构里该列确实存在。', mixed;
    END IF;
    RAISE NOTICE 'candidate_failure_logs: mixed-case rows = 0 ✓';
END
$do$;

-- ---------------------------------------------------------------------------
-- 2. request_logs (partitioned). 对每个 columnar 分区：
--    重命名 → 建 heap 分区 → **取真实边界端点** → ATTACH → 逐列 lower() 拷贝 → 删原表。
--    ⚠️ 212 号两处修：
--       (a) ATTACH 的边界：原来把**同一条** `pg_get_expr(relpartbound, …)` 同时当
--           FROM 和 TO，而它返回的是整条 `FOR VALUES FROM (…) TO (…)` 子句 ⇒ 垃圾。
--           改为把 relpartbound 文本按 ' TO ' 拆成两个端点分别使用。
--       (b) `INSERT … SELECT *` 是原样拷贝；改为逐列写出并对模型名列 lower()。
--       ⚠️ 另外 pg_class 只按 relname 匹配、未限定 nspname（同 210 号在
--         bodies-reattach 脚本里记的同一形态），已加 `n.nspname='public'`。
-- ---------------------------------------------------------------------------
\echo '--- 2. request_logs: rebuild columnar partitions as heap + lower() ---'
DO $do$
DECLARE
    part record;
    tbl_kind text;
    bound_text text;
    lo_bound text;
    hi_bound text;
    col_list  text;
BEGIN
    FOR part IN
        SELECT child.relname AS part_name
        FROM pg_inherits i
        JOIN pg_class parent ON parent.oid = i.inhparent
        JOIN pg_namespace pn  ON pn.oid  = parent.relnamespace
        JOIN pg_class child  ON child.oid = i.inhrelid
        JOIN pg_namespace cn  ON cn.oid  = child.relnamespace
        WHERE parent.relname = 'request_logs' AND pn.nspname = 'public'
          AND cn.nspname = 'public'
    LOOP
        EXECUTE format('SELECT amname FROM pg_class c JOIN pg_am a ON a.oid=c.relam WHERE c.oid = %L::regclass', part.part_name)
          INTO tbl_kind;
        IF tbl_kind = 'columnar' THEN
            EXECUTE format('SELECT pg_get_expr(c.relpartbound, c.oid) FROM pg_class c
                            JOIN pg_namespace n ON n.oid=c.relnamespace
                            WHERE c.relname = %L AND n.nspname = ''public''', part.part_name || '_col_old')
              INTO bound_text;

            -- 212 号：把整条子句拆成两个端点。'FOR VALUES FROM (A) TO (B)'
            -- ⇒ lo_bound = 'A', hi_bound = 'B'（含括号，交给 %L 做标识化引用）。
            IF bound_text IS NULL THEN
                RAISE EXCEPTION '分区 % 的 relpartbound 为 NULL，无法确定边界；中止。', part.part_name;
            END IF;
            IF bound_text !~ '^\s*FOR\s+VALUES\s+FROM\s+\(.*\)\s+TO\s+\(.*\)\s*$' THEN
                RAISE EXCEPTION '分区 % 的 relpartbound 形态无法解析：%', part.part_name, bound_text;
            END IF;
            lo_bound := regexp_replace(bound_text, '^\s*FOR\s+VALUES\s+FROM\s+', '');
            lo_bound := regexp_replace(lo_bound, '\s+TO\s+\(.*\)\s*$', '');
            hi_bound := regexp_replace(bound_text, '^\s*FOR\s+VALUES\s+FROM\s+\(.*\)\s+TO\s+', '');

            EXECUTE format('ALTER TABLE %I RENAME TO %I', part.part_name, part.part_name || '_col_old');
            EXECUTE format('CREATE TABLE %I (LIKE %I INCLUDING ALL)',
                           part.part_name, part.part_name || '_col_old');
            EXECUTE format('ALTER TABLE %I ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)',
                           'request_logs', part.part_name, lo_bound, hi_bound);
            -- 212 号：逐列拷贝并对**模型名列** lower()（原来 `SELECT *` 是原样拷贝）。
            --
            -- ⚠️ 列清单**从 pg_attribute 现取**，不写死：
            --   request_logs 实际有 40+ 列，且**没有** `status_code` / `created_at`，
            --   也没有 `canonical_model`（是 `canonical_id bigint`）。
            --   212 号第一版曾手写一份列清单，实测与真实 schema 几乎逐列不符 ——
            --   写死列名会在这类「同族多副本 + 各自漂移」的仓库里**必然过期**，
            --   而过期的列清单是**运行期才报错**，且报错形态是「列不存在」，
            --   读者会去查分区、查 AM，而**真实原因只是清单抄错了**。
            -- ⇒ 改成按 attnum 顺序现生成表达式列，并对**已确认的三个文本模型名列**
            --   套 lower()；其余列原样搬运。
            SELECT string_agg(
                       CASE WHEN a.attname IN ('client_model', 'outbound_model')
                            THEN format('lower(%I)', a.attname)
                            ELSE format('%I', a.attname) END,
                       ', ' ORDER BY a.attnum)
              INTO col_list
              FROM pg_attribute a
              WHERE a.attrelid = (part.part_name || '_col_old')::regclass
                AND a.attnum > 0
                AND NOT a.attisdropped;

            IF col_list IS NULL THEN
                RAISE EXCEPTION '分区 %：从 pg_attribute 取不到列清单，中止。', part.part_name;
            END IF;

            EXECUTE format('INSERT INTO %I (%s) SELECT %s FROM %I',
                           part.part_name, col_list, col_list, part.part_name || '_col_old');
            EXECUTE format('DROP TABLE %I', part.part_name || '_col_old');
            RAISE NOTICE 'request_logs partition % rebuilt as heap + lower()', part.part_name;
        ELSE
            RAISE NOTICE 'request_logs partition % already heap', part.part_name;
        END IF;
    END LOOP;
END
$do$;

-- 212 号：同样从「打印」改成「门禁」。
DO $do$
DECLARE mixed bigint;
BEGIN
    SELECT COUNT(*) INTO mixed FROM request_logs
    WHERE (client_model IS NOT NULL AND client_model <> lower(client_model))
       OR (outbound_model IS NOT NULL AND outbound_model <> lower(outbound_model))
       OR (canonical_model IS NOT NULL AND canonical_model <> lower(canonical_model));
    IF mixed > 0 THEN
        RAISE EXCEPTION 'request_logs 仍有 % 行模型名不是小写。', mixed;
    END IF;
    RAISE NOTICE 'request_logs: mixed-case rows = 0 ✓';
END
$do$;

-- ---------------------------------------------------------------------------
-- 3. 转回 columnar —— **212 号未实现**。
--    这就是 preflight 中止本脚本的原因。实现要点见文件头部 A 项。
--    在这一步补上之前，本脚本跑完的终态是「历史分区 = heap」，
--    而 header 第 ② 条已说明：这不是它宣称的结果。
-- ---------------------------------------------------------------------------
\echo '!!! STEP 3 (convert back to citus_columnar) IS NOT IMPLEMENTED !!!'
\echo '!!! 当前终态：历史分区已被重建成 HEAP，不是 columnar !!!'

\echo '=== columnar historical lowercase rebuild: 212 号中止/未完成 ==='

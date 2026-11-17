-- Migration: 977_pi_error_analysis_semantics
-- Purpose: 订正 971（PI 智能体错误自动分析系统）留下的四语义缺陷
-- Phase: 1.1 — 语义订正（零建表 / 零列变更 / 零路由变更）
-- Author: System
-- Date: 2026-11-17
--
-- ★ 本件只改**语义**，不改表结构：971 已把 `error_analysis_queue` /
--   `error_analysis_results` 两张表、`v_error_analysis_stats` 视图与
--   `cleanup_old_error_analysis` 函数建好；本件把 971 留下的**四个语义缺陷**
--   就地改对（函数会炸且恒 no-op / 统计视图 JOIN 膨胀 / queue_id 无唯一性 /
--   L2 规则 pattern 分组错）。
--
-- ★ 为什么必须是**新迁移**而不是改 971 的字节：
--   971 已 applied（211 台账 sha 与仓库文件逐字一致）⇒ 改它的字节会走 deploy
--   通道的**内容指纹重放**（public.gateway_db_revision_sequences 按 sha 记账）
--   ⇒ 改字节 ＝ 强制重放整份 971；而 971 本身**非幂等**（9 条裸 CREATE INDEX
--   + 裸 CREATE TRIGGER）⇒ 重放即 42P07/42710 ⇒ 部署 die。
--   ⇒ 语义订正必须另起编号，且本件自身**逐条幂等**（可被重放）。
--
-- ============================================================================
-- 缺陷清单（逐条取证，逐条修）
-- ============================================================================
--
-- 【F1】cleanup_old_error_analysis：会炸 ＋ 恒 no-op（971 第 248~276 行）
--   ① `DELETE ... RETURNING * INTO v_deleted_results`：v_deleted_results 是
--      **标量 BIGINT**，而 `RETURNING *` 给出 **21 列** ⇒ 删到 ≥2 行时
--      PL/pgSQL 抛「query returned more than one row」⇒ **整个清理事务回滚**；
--      即使只删到 1 行，标量拿到的也是第一列而不是行数 ⇒ 计数恒错。
--      ⇒ 而紧接着的 `GET DIAGNOSTICS v_deleted_results = ROW_COUNT;`
--         **把上一行的赋值覆盖掉** ⇒ 那行 RETURNING 是纯噪音。
--   ② 删除谓词依赖 `auto_applied = TRUE AND human_verified = TRUE`，
--      而**全仓 Go 代码从不写这两列**（`grep auto_applied|human_verified --include=*.go`
--      在写面 0 处命中；两列的 DEFAULT 是 FALSE / NULL）
--      ⇒ 即使不报错，这个清理器也是**永久 no-op**。
--   ⇒ 修法：① 只用 `GET DIAGNOSTICS ... = ROW_COUNT` 取行数（不再 RETURNING）；
--     ② 判据改成**时间口径**（超过保留期的行才删），不再依赖没人写的标志位；
--     ③ 两张表用**同一个保留窗口**（retention_days，签名不变）；
--     ④ 队列侧只删「已无结果行」的孤儿（否则 FK 的 ON DELETE CASCADE 会把
--        仍在保留期内的结果一起带走 ⇒ 与「两张表同窗口」自相矛盾）。
--
-- 【F2】v_error_analysis_stats：JOIN 膨胀（971 第 208~221 行）
--   旧视图 `FROM error_analysis_queue q LEFT JOIN error_analysis_results r
--   ON q.id = r.queue_id GROUP BY q.error_type` 且 `COUNT(*) as total_errors`
--   ⇒ 统计的是 **(队列行 × 结果行) 的连接行数**，不是队列行数：
--   1 条队列 + 2 条结果 ⇒ total_errors 报 2；`SUM(r.llm_tokens_used)`
--   同样被放大 ⇒ 监控面**系统性虚高**（结果行越多越离谱）。
--   ⇒ 修法：先按队列聚合（每条队列只留 1 行），再按 error_type 汇总；
--     `total_errors` 改成**队列行数**；`analyzed_count` / `pending_count`
--     保持「每条队列计一次」的语义不变。
--
-- 【F3】error_analysis_results(queue_id) 无唯一性 ⇒ 同一队列可挂多条结果
--   971 只给了**普通索引** idx_error_analysis_results_queue；而写面
--   （domains/promptinjection/error_analyzer.go 的 INSERT）**没有** queue_id
--   上的冲突处理 ⇒ 两个处理器抢同一条队列时（retry / 重复分析）会插出**多条**
--   分析结果 ⇒ F2 的膨胀就有了真实来源。
--   ⇒ 修法：先**确定性去重**（同一 queue_id 只保留 id 最大的一行），
--     再建 UNIQUE 索引（CREATE UNIQUE INDEX IF NOT EXISTS）。
--     ★ 去重必须在前：否则存量库上建唯一索引会 23505（duplicate key）⇒ 迁移失败。
--     ★ 保留「id 最大」而不是「最新」：id 是 BIGSERIAL 的写入序代理，判定**确定**
--       （不依赖时间戳的 NULL / 时区语义）⇒ 可复算、可审计。
--     ★ 已有的普通索引 idx_error_analysis_results_queue **不动**：唯一索引已经
--       能服务同样的按 queue_id 检索，删它属于超出本件范围的对象变更。
--
-- 【F4】L2（rule_type='advanced'）检测规则的种子 pattern 有缺陷
--   种子在 315_prompt_injection_detection.sql（部分在 364_prompt_injection_enhanced.sql）。
--   ① `privilege_escalation`（severity 9）：`(?i)(grant|give).*admin|sudo|root`
--      —— 顶层 `|` 把整串切成三条分支 ⇒ **`sudo` / `root` 两个裸词单独成支**，
--      于是 "root cause"、"/root"、Linux 教程里的 "sudo" 全部 **severity 9 命中**
--      ⇒ 普通文本被当成提权攻击。
--   ② `command_injection`（severity 10）：`(?i)(exec|system|eval|shell).*\(` ——
--      交替组本身是对的，但 `system` 是**日常英语常用词**，且中间的 `.*` 无界
--      ⇒ "the operating system as a whole (see below)" 这类普通句子 severity 10 命中。
--   ③ `tool_abuse_exec`（severity 10，364 的种子）：`(?i)(exec|eval|system|
--      subprocess|os\.system|child_process).*(\(|call|invoke)` —— 同族：
--      `system` + 无界 `.*` ⇒ "the system will call you back" severity 10 命中。
--   ⇒ 修法：**只改 pattern 列**（rule_name / category / severity / description
--     / is_system / tags 一律不动 —— 规则的身份与权重不是本件要动的东西），
--     改成「关键词必须处在代码/权限语境里」的形态：
--       · 代码调用形态（`exec(`、`eval(`、`system(`、`os.system(`、`shell_exec(`、
--         `subprocess.`、`child_process.`）⇒ **保留**；
--       · `sudo` 必须处在**命令位**（后面跟着命令/参数）；
--       · `root` 必须与权限语境词**相邻**（root access / root shell / run as root …）
--         ⇒ "root cause" / "/root" 不再命中。
--   ★ **不在本件里加 `(?s)`**：多词规则之间的 `.*` 不跨行 ⇒ 插一个换行即可绕过
--     （"ignore\nall previous instructions"）—— 那是**检测器编译侧**的修法
--     （Go 侧在规则编译时统一补 `(?s)`），由并行工程师在 domains/promptinjection/
--     里做；本件**只**修分组/词界缺陷 ⇒ 两边职责不重叠、不会互相覆盖。
--   ★ 种子文件 315 / 364 **一律不动**（改它们＝改已 applied 迁移的字节 ⇒
--     触发内容指纹重放 ⇒ 部署 die）⇒ 订正只能以 UPDATE 的形式落在**新**迁移里。
--
-- ★ 有意**留白**的 L2 规则（逐条有理由，不是漏看）：
--   · `dan_unrestricted` `(?i)unrestricted mode|no restrictions` —— 顶层 `|`
--     的两支都是**短语**（不是裸常用词），而 "no restrictions" 本身就是绕过
--     语义 ⇒ 不动（动了会削弱真正的绕过检测）。
--   · `dan_jailbreak` `(?i)jailbreak|bypass.*filter|ignore.*safety` —— 裸词
--     "jailbreak" 正是这条规则的**目的** ⇒ 不动。
--   · `bypass_rot13` `(?i)rot13|rot-13` —— 同上（"rot13" 就是绕过信号本身）。
--   · 其余 L2 规则（`dan_mode` / `dan_developer_mode` / `bypass_translation` /
--     `bypass_restrictions` / `bypass_guidelines` / `payload_smuggling_*` /
--     `context_manipulation_*` …）的交替组**本身已正确分组**（或分支都是短语）
--     ⇒ 一律逐字节不动。
--
-- -- 超时预算（门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget）--------
--   本文件含 1 条**非并发 CREATE UNIQUE INDEX**（idx_error_analysis_results_queue_uniq）
--   ⇒ 命中「全表扫描类 DDL」判据 ⇒ 必须声明 ≥10min 预算 + 「实测」注释。
--   实测（隔离 PG 17.10 / kx-citus / scratch 库，2026-11-17）：
--     · 空库跑完整脚本：墙钟 **90ms**（含 psql 启动；RC=0，ERROR 行数 0）；
--     · **二次执行**（幂等复核）：墙钟 **90ms**（RC=0，ERROR 行数 0）；
--     · 最坏形态（error_analysis_results 灌 **500000** 行、其中每条 queue_id
--       各挂 2 条结果 ⇒ 真实的一对多形态，且唯一索引缺失）：墙钟 **1.67s**
--       （RC=0，ERROR 0 行）⇒ 去重后剩 250000 行、重复 queue_id **0** 个、
--       唯一索引建成。
--   ⇒ 本文件真实成本是**锁窗**而不是扫描时长：唯一索引建在
--     `error_analysis_results`（971 新建的**小表**，真实行数量级为百）上，
--     5s lock_timeout 让它在等锁时**快速失败**而不是堵住全库；10min 预算比
--     实测值高三个数量级（761 先例：按数量级抬，不贴实测值）。
--
-- 锁面（门 TestStartupMigrationsAboveCutoffHaveLockGuard）：
--   本文件**不建表 / 不加列 / 不删列** ⇒ 不触碰任何既有大表的
--   AccessExclusiveLock；唯一的强锁 DDL 是那 1 条 CREATE UNIQUE INDEX
--   （ShareLock，锁窗 = 表大小量级，毫秒~秒级）。
--   ★ 规则表的 UPDATE（`prompt_injection_rules`）与上面这条 DDL **同事务**：
--     共享 5s lock_timeout ⇒ 抢不到锁就整体快速失败（宁可部署中止，
--     也不半应用：视图 / 函数 / 唯一索引 / 规则更新要么全生效、要么全不生效）。
--
-- ============================================================================
-- 0. 事务与超时预算
-- ============================================================================
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ============================================================================
-- 1. F1：重写 cleanup_old_error_analysis（时间口径 ＋ 只用 ROW_COUNT）
-- ============================================================================
-- ★ 签名与 971 **逐字相同**（retention_days INTEGER DEFAULT 90）⇒ 既有调用方
--   （按名调用的 cron / 运维脚本）不需要改。
CREATE OR REPLACE FUNCTION public.cleanup_old_error_analysis(retention_days INTEGER DEFAULT 90)
RETURNS TABLE(deleted_queue_count BIGINT, deleted_result_count BIGINT) AS $$
DECLARE
    v_cutoff TIMESTAMPTZ;
    v_deleted_results BIGINT := 0;
    v_deleted_queue BIGINT := 0;
BEGIN
    -- ★ 保留窗口算一次 ⇒ 两张表**同一口径**（F1③）。
    --   显式传 NULL 视为「不清理」（cutoff 为 NULL ⇒ `created_at < NULL` 恒不成立
    --   ⇒ 一行都不删）⇒ 避免 NULL 被当成 0 天而**清空全表**。
    IF retention_days IS NULL THEN
        v_cutoff := NULL;
    ELSE
        v_cutoff := NOW() - make_interval(days => GREATEST(retention_days, 0));
    END IF;

    -- 结果表：纯时间口径（F1②）。不再 RETURNING ⇒ 不再有「多行 ⇒ 抛错」。
    DELETE FROM public.error_analysis_results
    WHERE created_at < v_cutoff;
    GET DIAGNOSTICS v_deleted_results = ROW_COUNT;

    -- 队列表：**只删已经没有结果行的孤儿**（否则 FK 的 ON DELETE CASCADE 会把
    -- 仍在保留期内的结果一起带走 ⇒ 与「两张表同窗口」自相矛盾）。
    DELETE FROM public.error_analysis_queue AS q
    WHERE q.created_at < v_cutoff
      AND NOT EXISTS (
          SELECT 1 FROM public.error_analysis_results r WHERE r.queue_id = q.id
      );
    GET DIAGNOSTICS v_deleted_queue = ROW_COUNT;

    deleted_result_count := v_deleted_results;
    deleted_queue_count := v_deleted_queue;

    RETURN NEXT;
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- 2. F2：重写 v_error_analysis_stats（先按队列聚合，再按类型汇总）
-- ============================================================================
-- ★ 列名 / 列序 / 列类型与 971 的视图**逐字相同** ⇒ CREATE OR REPLACE 可原地
--   替换（PG 不允许在 OR REPLACE 里改列名 / 改列序）⇒ 依赖它的读面不受影响。
CREATE OR REPLACE VIEW public.v_error_analysis_stats AS
WITH per_queue AS (
    SELECT
        q.id                     AS queue_id,
        q.error_type             AS error_type,
        q.status                 AS status,
        q.created_at             AS created_at,
        r.is_false_positive      AS is_false_positive,
        r.confidence_score       AS confidence_score,
        r.analysis_duration_ms   AS analysis_duration_ms,
        r.llm_tokens_used        AS llm_tokens_used
    FROM public.error_analysis_queue q
    LEFT JOIN LATERAL (
        SELECT
            rr.is_false_positive,
            rr.confidence_score,
            rr.analysis_duration_ms,
            rr.llm_tokens_used
        FROM public.error_analysis_results rr
        WHERE rr.queue_id = q.id
        ORDER BY rr.id DESC
        LIMIT 1
    ) r ON TRUE
)
SELECT
    error_type,
    COUNT(DISTINCT queue_id) AS total_errors,
    COUNT(DISTINCT queue_id) FILTER (WHERE status = 'analyzed') AS analyzed_count,
    COUNT(DISTINCT queue_id) FILTER (WHERE status = 'pending') AS pending_count,
    COUNT(DISTINCT queue_id) FILTER (WHERE is_false_positive = TRUE) AS false_positive_count,
    AVG(confidence_score) AS avg_confidence,
    AVG(analysis_duration_ms) AS avg_analysis_time_ms,
    SUM(llm_tokens_used) AS total_tokens_used,
    MAX(created_at) AS last_error_at
FROM per_queue
GROUP BY error_type;

-- ============================================================================
-- 3. F3：error_analysis_results(queue_id) 去重 ＋ UNIQUE 索引
-- ============================================================================
-- ★ 去重在前、约束在后：存量库上直接建唯一索引会 23505（duplicate key）⇒ 迁移失败。
-- ★ 判定口径：同一 queue_id 保留 **id 最大**的一行（id = BIGSERIAL 写入序代理
--   ⇒ 判定确定、可复算）。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.error_analysis_results
        GROUP BY queue_id
        HAVING COUNT(*) > 1
    ) THEN
        DELETE FROM public.error_analysis_results AS dup
        USING public.error_analysis_results AS keep
        WHERE dup.queue_id = keep.queue_id
          AND dup.id < keep.id;
    END IF;
END;
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_error_analysis_results_queue_uniq
    ON public.error_analysis_results(queue_id);

-- ============================================================================
-- 4. F4：修 L2 规则的种子 pattern（只改 pattern 列）
-- ============================================================================
-- ★ 落库形态说明：`standard_conforming_strings = on`（PG ≥ 9.1 默认）⇒ 引号里
--   的反斜杠是**字面量** ⇒ 存进库的 `\b` / `\S` 就是 Go 的 regexp 需要的形态
--   （315 的 `\x{200B}` 用的正是同一约定）⇒ 本段**不需要** E'' 转义。
--
-- ★ ★ 引擎边界（PG 17.10 实测，本轮踩过）：**PG 的 `\b` 是退格符、不是词边界**
--   （PG 的词边界是 `\y`）⇒ 同一个 pattern：
--       'sudo rm -rf /' ~ '\bsudo\b'   →  PG=false ／ Go RE2=true
--   ⇒ **含 `\b` 的 pattern 只能用 Go 的 regexp 验证**；拿 PG 的 `~` 复核会得
--     **假阴性**（"以为没命中、其实命中了"）。
--   ⇒ 好在本仓的规则 pattern **只在 Go 侧评估**（domains/promptinjection 的
--     compileRulePattern 用 Go regexp；全仓 SQL 只 INSERT/UPDATE `pattern` 列，
--     **没有任何 SQL 用它做匹配**）⇒ 无生产风险，但**别用 PG 复核它**。
--
-- ★ ★ 为什么这三段 UPDATE 必须**先判表在不在场**：
--   规则表由 315 播种，而 **315 既不在 installer 的 StartupFiles 里、也不在 211
--   部署通道的 files 数组里**（实测两处清单都无 315）⇒ 规则表**不保证存在**：
--   · 表在场（315 跑过的库）⇒ 有 L2 规则可改 ⇒ 本段生效；
--   · 表不在场（全新安装 / 未跑 315 的库）⇒ **没有规则可改** ⇒ 跳过；
--     而裸 UPDATE 会 42P01（relation does not exist）⇒ **迁移失败 ⇒ 部署 die**
--     ⇒ ⇒ 判据必须落在迁移里，不能指望「表一定在」。
--   ★ 表真的不在场时，检测器会自己 ensureRulesTable 建一张**只有 2 条基础规则**
--     的表（domains/promptinjection/detector.go）⇒ 那 2 条**不是**本件的 L2 规则
--     ⇒ 本件的 L2 订正对它们无意义（跳过不是漏做）。
DO $$
BEGIN
    IF to_regclass('public.prompt_injection_rules') IS NULL THEN
        RAISE NOTICE '977: public.prompt_injection_rules 不在场 ⇒ 跳过 L2 规则 pattern 订正（无规则可改）';
        RETURN;
    END IF;

-- ① privilege_escalation（severity 9 / category role_hijack）
--    旧：(?i)(grant|give).*admin|sudo|root
--    ⇒ 顶层 `|` 让 `sudo` / `root` **裸词成支** ⇒ "root cause" / "/root"
--      / Linux 教程里的 "sudo" 全部 severity 9 命中。
--    新：grant/give + admin 的原语义保持；`sudo` 必须处在命令位（后接命令/参数）；
--        `root` 必须与权限语境词**相邻**，或被提权动词直接引出（run as root /
--        escalate to root / become root …）。
UPDATE public.prompt_injection_rules
SET pattern = '(?i)(grant|give).*\badmin\b|\bsudo\b\s+(-\S+\s+)*\S+|\broot\b[\s,;:_-]+(admin|administrator|access|privilege|privileges|permission|permissions|shell|account|user|users|password|passwd|login|console|rights|role)\b|\b(as|become|becomes|becoming|gain|gains|gaining|obtain|obtains|obtaining|escalate|escalates|escalating|elevate|elevates|elevating|promote|promotes|promoted|switch|switching|run|running)\s+(to\s+)?root\b'
WHERE rule_name = 'privilege_escalation';

-- ② command_injection（severity 10 / category bypass）
--    旧：(?i)(exec|system|eval|shell).*\(
--    ⇒ `system` 是日常英语常用词 ＋ 中间 `.*` 无界 ⇒ "the operating system as a
--      whole (see below)" 这类普通句子 severity 10 命中。
--    新：代码调用形态（`exec(` / `eval(` / `system(` / `os.system(` /
--        `shell_exec(`）**全部保留**；`system` / `shell` 这类英语词必须**紧邻**
--        左括号才算数（`os.system (` 这种带空格的由 os. 分支兜住）。
UPDATE public.prompt_injection_rules
SET pattern = '(?i)\b(exec|eval)\s*\(|\b(system|shell)\(|os\s*\.\s*system\s*\(|\bshell_exec\s*\('
WHERE rule_name = 'command_injection';

-- ③ tool_abuse_exec（severity 10 / category bypass，364 的种子）
--    旧：(?i)(exec|eval|system|subprocess|os\.system|child_process).*(\(|call|invoke)
--    ⇒ 同族：`system` + 无界 `.*` ⇒ "the system will call you back" severity 10 命中。
--    新：调用形态（`exec(` / `eval(` / `system(` / `os.system(` / `shell_exec(` /
--        `subprocess.` / `child_process.`）＋ 「动词 + subprocess/child_process」
--        的调用句式**全部保留**；`system call`（操作系统术语）与普通叙述句不再命中。
UPDATE public.prompt_injection_rules
SET pattern = '(?i)\b(exec|eval)\s*\(|\b(system|shell)\(|os\s*\.\s*system\s*\(|\bshell_exec\s*\(|\b(subprocess|child_process)\b\s*[.(]|\b(call|invoke|execute|run|spawn)\s+(the\s+)?(subprocess|child_process)\b'
WHERE rule_name = 'tool_abuse_exec';

END;
$$;

-- ============================================================================
-- 5. 注释
-- ============================================================================
COMMENT ON FUNCTION public.cleanup_old_error_analysis IS '清理超过保留期的错误分析结果与队列项（时间口径；977 起不再依赖 auto_applied/human_verified，也不再 RETURNING * INTO）';
COMMENT ON VIEW public.v_error_analysis_stats IS '错误分析统计视图（先按队列聚合再汇总 ⇒ 一条队列只算一次，不因一对多结果而膨胀）';

-- ============================================================================
-- 6. 台账自登记（≥952 纪律）
-- ============================================================================
-- ★ 双账本纪律：211 的 public.schema_migrations 与 public.gateway_db_revision_sequences
--   都必须有 977 的行，否则「账本说没做」⇒ 后续按账本判断的人会踩空。
--   （public.gateway_db_revision_sequences 那行由 deploy 通道自己按内容指纹写；
--   本件只负责 public.schema_migrations 这一本，与 971 的形态一致。）
INSERT INTO public.schema_migrations (version, description)
VALUES ('977', 'pi_error_analysis_semantics: 订正 971 的四语义缺陷（cleanup 改时间口径 / 统计视图去 JOIN 膨胀 / queue_id 唯一 / L2 规则 pattern 分组）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

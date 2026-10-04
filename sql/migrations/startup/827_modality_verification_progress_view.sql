-- ===========================================================================
-- File:          sql/migrations/startup/827_modality_verification_progress_view.sql
-- Migration:     827
-- Database:      llm_gateway
-- Purpose:       多模态分级核实的**进度可观测性** —— 给出「严格模式现在开了会
--                挡住多少模型」这个数，让逐环境灰度从「凭感觉」变成「看数」。
--
-- 立项依据（本仓代码实测，非推断）：
--
-- 1. 灰度决策当前没有可量的东西。
--    路由闸门 provider/client.go 的 modalityVerdictGateSQL 有两档：
--      · 缺省档：只排除「全负证据」的 (模型, 模态)；
--      · 严格档：额外要求 read_level = 'confirmed'。
--    严格档由 env LLM_GATEWAY_MODALITY_ROUTING_STRICT 控制，**默认关闭**。
--    关着的理由是：打开它会把「所有没被语义确认过」的 (模型, 模态) 全排除，
--    图片请求于是全量 503 no_candidate。灰度前要等「未核实队列排空」。
--
-- 2. 但「队列」这个量在库里查不出来：
--      · model_modality_verification 只有 (凭证, 原始模型名, 模态) 一行，
--        **没有行的 (模型, 模态) 组合无法与「这一行是 unknown」区分**——
--        而「没有行」正是绝大多数从未被探测过的组合。
--      · v_model_modality_verdict（825 建）按 canonical 聚合，但它的
--        GROUP BY 源是 model_modality_verification，**没被探测过的模型根本
--        不出现在结果里** ⇒ 它答的是「已核实的那部分里判词如何」，
--        不是「还有多少没核实」。
--      · worker 的 slog（bg/modality_verification.go 的 cycle done）打的是
--        scanned / probed / written / backed_off，其中 scanned = len(targets)
--        是**本轮到期行数**（受 batch_limit 截断），不是全量待办量。
--    ⇒ 「等队列排空」这句话在没有本迁移之前，是没有分母的。
--
-- 3. 粒度必须与闸门一致，即 (模型, 模态) 对，而不是「模型」。
--    闸门的谓词逐个模态求值（$3 IN ('vision','audio','video')），
--    一个模型可以「vision 已确认、audio 未核实」——它在 vision 请求下可用、
--    在 audio 请求下被严格档挡掉。按模型聚合会把这两个状态糊成一个数，
--    而灰度真正要回答的是「**哪个模态**挡了多少」。
--
-- 本迁移给出两个只读视图：
--
--   (a) v_model_modality_verification_progress
--       每个 (canonical 模型, 非文本模态) 一行，口径与闸门**逐条对齐**：
--         excluded_by_default_gate = 缺省档会排除（等价 verdict='negative'）
--         excluded_by_strict_gate  = 严格档会排除（等价 verdict<>'confirmed'）
--       对齐不是巧合，是照着 modalityVerdictGateSQL 的两个 EXISTS 写的；
--       两边漂移会让「看着能开、开了才发现挡了东西」。
--
--   (b) v_model_modality_verification_rollup
--       全局一行的灰度判据：严格档会挡住多少个模型、其中多少是
--       「一条证据都没有」、以及最老的一条未核实证据有多旧。
--       「最旧的一条有多旧」回答的是另一个问题：队列**排空过没有**，
--       以及**排空后有没有又长回来**（新上线的模型默认进 unknown）。
--
-- 为什么是视图而不是 Go 里的统计函数：
--   灰度决策发生在部署现场（psql / 只读副本 / 运维面板），视图不需要
--   重新构建任何东西就能查；而 stats 函数还得先找到调用方。
--   视图同时是 admin 端点与自检任务将来取数的共同底座。
--
-- 只读，不改任何表，不加任何列。
--
-- 幂等：CREATE OR REPLACE VIEW，可安全重放。
-- ===========================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- (a) 每个 (模型, 非文本模态) 的核实进度
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW public.v_model_modality_verification_progress AS
WITH mods AS (
    -- 闸门只对非文本模态求值（$3 IN ('vision','audio','video')），
    -- 所以这里也只枚举这三个。text 不受闸门管，列出来只会稀释这个视图。
    SELECT unnest(ARRAY['vision', 'audio', 'video'])::text AS modality
),
-- 每个 (canonical 模型, 模态) 组合都**先存在一行**，哪怕一条证据都没有。
-- 这一步是整个视图的关键：v_model_modality_verdict 从证据表出发做 GROUP BY，
-- 从未被探测过的组合**根本不在结果里**，于是「没核实」不可见。
per_pair AS (
    SELECT
        mc.id                                   AS canonical_id,
        mc.canonical_name,
        m.modality,
        COALESCE(mc.modality, 'text')           AS stored_modality,
        COALESCE(mc.modality_source, 'inferred') AS modality_source,
        mc.modality_verified_at
    FROM public.models_canonical mc
    CROSS JOIN mods m
),
agg AS (
    SELECT
        v.canonical_id,
        v.modality,
        COUNT(*)::int                           AS evidence_rows,
        COUNT(*) FILTER (WHERE v.carry_level = 'accepted')::int AS carry_accepted,
        COUNT(*) FILTER (WHERE v.carry_level = 'rejected')::int AS carry_rejected,
        COUNT(*) FILTER (WHERE v.read_level = 'confirmed')::int  AS read_confirmed,
        COUNT(*) FILTER (WHERE v.read_level = 'negative')::int   AS read_negative,
        COUNT(*) FILTER (WHERE v.read_level = 'unknown')::int    AS read_unknown,
        MAX(v.checked_at)                       AS last_checked_at
    FROM public.model_modality_verification v
    GROUP BY v.canonical_id, v.modality
)
SELECT
    p.canonical_id,
    p.canonical_name,
    p.modality,
    p.stored_modality,
    p.modality_source,
    p.modality_verified_at,
    COALESCE(a.evidence_rows, 0)::int           AS evidence_rows,
    COALESCE(a.carry_accepted, 0)::int         AS carry_accepted,
    COALESCE(a.carry_rejected, 0)::int         AS carry_rejected,
    COALESCE(a.read_confirmed, 0)::int         AS read_confirmed,
    COALESCE(a.read_negative, 0)::int          AS read_negative,
    COALESCE(a.read_unknown, 0)::int           AS read_unknown,
    a.last_checked_at,
    CASE
        WHEN a.read_confirmed IS NULL OR a.read_confirmed = 0 THEN NULL
        ELSE ROUND(EXTRACT(EPOCH FROM (now() - a.last_checked_at))::numeric / 86400.0, 2)
    END                                        AS days_since_last_check,
    -- 判词与 825 的 v_model_modality_verdict 用同一套 CASE：
    -- 有任一 confirmed 即 confirmed；全负且无 unknown 才算 negative；
    -- 其余（含「一条证据都没有」）一律 unknown。
    -- **inconclusive 绝不能塌成 negative** —— 那会把 vision 降成 text，
    -- 然后候选过滤把图片请求变成 503。
    CASE
        WHEN COALESCE(a.read_confirmed, 0) > 0 THEN 'confirmed'
        WHEN COALESCE(a.read_unknown, 0) = 0
         AND COALESCE(a.read_negative, 0) > 0 THEN 'negative'
        ELSE 'unknown'
    END                                        AS verdict,
    -- 与 provider/client.go modalityVerdictGateSQL 的两个 EXISTS 逐条对齐。
    --
    -- 缺省档的门是：
    --     EXISTS(某行 read_level='negative'
    --            AND NOT EXISTS(某行 read_level <> 'negative'))
    -- 第二个子句的否定是「**没有任何一行是非负的**」——它把 unknown 也算作
    -- 「非负」，因为 unknown 恰恰意味着「还不能判定」，而不能判定不构成
    -- 「这个模型看不见」的证据。
    --
    -- ★ 写这个视图时我第一版漏了这一项，写成
    --     read_negative > 0 AND read_confirmed = 0
    -- 于是「一负 + 一 unknown」被判成缺省档会排除。真库实测立刻打脸
    -- （d-negative-plus-unknown 那一行）：闸门不排除它，而视图说排除。
    -- 「负证据」与「没有反证」是两件事：前者是证据，后者是信息缺失。
    (COALESCE(a.read_negative, 0) > 0
     AND COALESCE(a.read_confirmed, 0) = 0
     AND COALESCE(a.read_unknown,  0) = 0)    AS excluded_by_default_gate,
    -- 严格档的门是 NOT EXISTS(某行 read_level='confirmed')，直接对照。
    (COALESCE(a.read_confirmed, 0) = 0)        AS excluded_by_strict_gate
FROM per_pair p
LEFT JOIN agg a
       ON a.canonical_id = p.canonical_id
      AND a.modality = p.modality;

COMMENT ON VIEW public.v_model_modality_verification_progress IS
    '每个 (canonical 模型, 非文本模态) 的多模态核实进度。excluded_by_default_gate / '
    'excluded_by_strict_gate 与 provider/client.go modalityVerdictGateSQL 的两档闸门逐条对齐。'
    'verdict 口径与 825 的 v_model_modality_verdict 相同：inconclusive 一律 unknown，绝不塌成 negative。';

-- ---------------------------------------------------------------------------
-- (b) 灰度判据：全局一行
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW public.v_model_modality_verification_rollup AS
SELECT
    COUNT(DISTINCT p.canonical_id)::int                                        AS canonical_models,
    COUNT(*)::int                                                              AS model_modality_pairs,
    COUNT(*) FILTER (WHERE p.verdict = 'confirmed')::int                       AS pairs_confirmed,
    COUNT(*) FILTER (WHERE p.verdict = 'negative')::int                        AS pairs_negative,
    COUNT(*) FILTER (WHERE p.verdict = 'unknown')::int                         AS pairs_unknown,
    -- 「一条证据都没有」的组合数。它比 pairs_unknown 更能说明队列有多满：
    -- unknown 里既有「探过、判不出」的，也有「压根没排队」的。
    COUNT(*) FILTER (WHERE p.evidence_rows = 0)::int                           AS pairs_never_probed,
    COUNT(DISTINCT p.canonical_id) FILTER (WHERE p.excluded_by_strict_gate)::int
                                                                               AS models_blocked_by_strict,
    COUNT(DISTINCT p.canonical_id) FILTER (WHERE p.excluded_by_default_gate)::int
                                                                               AS models_blocked_by_default,
    -- 最老的一条未核实证据有多旧。回答两个问题：队列排空过没有，
    -- 以及排空后有没有又长回来（新上线的模型默认落 unknown）。
    ROUND(EXTRACT(EPOCH FROM (
        now() - MIN(p.last_checked_at) FILTER (WHERE p.verdict <> 'confirmed')
    ))::numeric / 86400.0, 2)                                                  AS oldest_unconfirmed_age_days,
    -- 超过 worker 的 stale 窗口（30 天）仍未确认的组合数。这些是最该先探的，
    -- 因为它们既挡住了严格档，结论也已经不可信。
    COUNT(*) FILTER (
        WHERE p.verdict <> 'confirmed'
          AND (p.last_checked_at IS NULL
               OR p.last_checked_at < now() - INTERVAL '30 days')
    )::int                                                                      AS pairs_stale_or_never
FROM public.v_model_modality_verification_progress p;

COMMENT ON VIEW public.v_model_modality_verification_rollup IS
    '多模态严格模式灰度的判据：models_blocked_by_strict = 现在打开 LLM_GATEWAY_MODALITY_ROUTING_STRICT 会挡住的模型数。'
    '降到 0 之前打开 ⇒ 非文本请求全量 503 no_candidate。pairs_stale_or_never 是下一轮探测的优先队列。';

COMMIT;

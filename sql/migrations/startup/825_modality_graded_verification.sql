-- ===========================================================================
-- File:          sql/migrations/startup/825_modality_graded_verification.sql
-- Migration:     825
-- Database:      llm_gateway
-- Purpose:       多模态能力「分级核实」：把「可承载」与「真能读」两级分别落库，
--                并给 models_canonical 的标注补上来源与核实时间。
--
-- 立项依据（本地代码实测，非推断）：
--
-- 1. 现状把 HTTP 200 当作「支持多模态」的证据，这是错的。
--    bg/probe_modality.go 的 ProbeModality 在 2xx 时无条件
--    result.Supported = true。纯文本模型经中转接入时经常收下 image_url
--    内容块、返回 200、然后完全无视那张图并自由发挥 ⇒ 「接口接受了」
--    与「模型看得见」是两件事，前者不能当后者的证据。
--
-- 2. 现状根本发现不了 text → 多模态 的升级。
--    bg/model_probe.go:1683 verifyTargetModality 的第一行就是
--      if status != "ok" || t.Modality == "" || t.Modality == "text" || ...
--    ⇒ 当前被标成 text 的模型**永远不会被探测**。而规则表
--    (modelname.InferModality) 按名字猜，漏判的新模型一律落回默认
--    text，于是被 discovery 的候选过滤（modality IN ('vision',
--    'multimodal')）排除 ⇒ 图片请求 503 no_candidate。这是本仓已
--    记录过多次的故障形态（见 modelname/modality_defaults_test.go:27
--    与 820 迁移的论证）。
--
-- 3. 现状的 Layer 2 探测不落库。
--    ProbeModality 全仓只有一个调用点（bg/model_probe.go:1695），
--    成功与失败都只 slog.*，没有任何 INSERT/UPDATE。迁移 612 的注释
--    自认 "until an external probe populates it" —— 那个 probe 存在了，
--    但结论没有落库，因此「这个模型核实过没有」在库里无法表达，
--    「定时核实未曾核实过的模型」也就无从选队列。
--
-- 本迁移给出可表达、可查询、可被定时任务消费的三样东西：
--
--   (a) model_modality_verification —— 每个 (凭证, 原始模型名, 模态) 一行，
--       两级分列：
--         carry_level = 结构级「可承载」：上游收不收这个模态的内容块。
--                         accepted / rejected / unknown
--         read_level  = 语义级「真能读」：模型能否真的读出图里的内容。
--                         confirmed / negative / unknown
--       两级分列而不是合成一个 bool，是因为它们的失效形态不同：
--       carry=accepted 而 read=negative 恰好就是「接口收下但模型看不见」
--       这一类——合成一个 bool 会把它记成 true，也就是今天的缺陷。
--
--   (b) models_canonical 的 modality_source / modality_verified_at /
--       modality_evidence —— 标注的出处。没有这三列，「未核实过」这个
--       状态在库里和「早于本迁移」不可区分，定时核实无法按未核实选队列。
--
--   (c) modality_source 参与 discovery 的升级守卫（见 825 配套 Go 改动）。
--       既有 upsert 的谓词是「存储值 = 'text' 且推断值非 text 才升级」，
--       于是**语义降级会被下一次 discovery 立刻翻回去**：worker 把
--       vision 降成 text，下一个 provider tick 看到推断值是 vision
--       且存储值是 text，又把它推回 vision。两条规则对同一个字段提出
--       互斥要求，必须让已核实的标注对规则重推免疫。
--
-- 存量口径：全表 modality_source 置 'inferred' 而不是留 NULL。留 NULL
-- 时「本迁移之前就有的行」与「新建未核实的行」不可区分，定时核实会把
-- 全部存量当未核实重新探一遍（不是错，但浪费预算且噪声大）。
-- 'inferred' 是这些行的真值——它们确实全部由 Layer 1 规则表播种。
--
-- 幂等：ADD COLUMN IF NOT EXISTS + CREATE TABLE IF NOT EXISTS + 幂等
-- UPDATE + DROP/ADD CONSTRAINT，可安全重放。
-- ===========================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- (b) models_canonical：标注出处
-- ---------------------------------------------------------------------------
-- 前置自愈（2026-10-04）：下面 model_modality_verification.canonical_id 的
-- REFERENCES models_canonical(id) 要求 id 上有唯一约束，但 01-schema 的
-- SSOT 从未给 models_canonical 声明主键（只有 canonical_name 唯一键）——
-- 按 SSOT 全新安装的库（本地 pg17 实测、以及任何 fresh install）在本迁移
-- 的 FK 处 42830。id 为 bigint NOT NULL + 序列默认、无空无重（实测 960 行
-- 0/0），补主键安全且与 provider_models.canonical_id 的"FK to
-- models_canonical.id"注释语义一致。幂等：已存在 pkey 时 DO 块短路。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'public.models_canonical'::regclass
           AND conname = 'models_canonical_pkey'
    ) THEN
        ALTER TABLE public.models_canonical
            ADD CONSTRAINT models_canonical_pkey PRIMARY KEY (id);
    END IF;
END $$;

ALTER TABLE public.models_canonical
    ADD COLUMN IF NOT EXISTS modality_source text,
    ADD COLUMN IF NOT EXISTS modality_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS modality_evidence jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN public.models_canonical.modality_source IS
    'How the current modality value was reached: inferred (Layer 1 rule table, the default for every pre-825 row), structural (upstream accepts the modality payload), semantic (model demonstrably reads the content), manual (super_admin override, Layer 3). semantic/manual are authoritative: rule inference must not overwrite them.';
COMMENT ON COLUMN public.models_canonical.modality_verified_at IS
    'When a probe last produced a semantic verdict for this row. NULL = never semantically verified — the queue selector for bg/modality_verification reads this.';
COMMENT ON COLUMN public.models_canonical.modality_evidence IS
    'Compact per-modality summary of the probe verdicts behind the current value (confirm/negative counts per modality, last probe verdicts). Full per-binding evidence lives in model_modality_verification.';

-- 存量全部是规则表播种的结果。
UPDATE public.models_canonical
   SET modality_source = 'inferred'
 WHERE modality_source IS NULL;

ALTER TABLE public.models_canonical
    DROP CONSTRAINT IF EXISTS models_canonical_modality_source_check;
ALTER TABLE public.models_canonical
    ADD CONSTRAINT models_canonical_modality_source_check
    CHECK (modality_source IS NULL
           OR modality_source IN ('inferred', 'structural', 'semantic', 'manual'));

-- ---------------------------------------------------------------------------
-- (a) 分级核实证据表
--
-- 为什么不复用 credential_model_capabilities（迁移 612/613）：
--   那张表的语义是「default-off 的显式 opt-in」，supported 缺行即关闭，
--   消费者读它来决定**要不要开启**一条能力腿。而 modality 是「模型本来
--   是什么样」的属性，缺行不等于关闭；把它塞进同一张表会让
--   「没回填过」与「核实过为否」压成同一个 false——正是迁移 613 当年
--   为 stream 拆独立键要避开的那类歧义。两张表语义不同，分开。
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_modality_verification (
    id              bigserial PRIMARY KEY,
    canonical_id    bigint REFERENCES public.models_canonical(id) ON DELETE CASCADE,
    canonical_name  text NOT NULL,
    credential_id   integer NOT NULL,
    raw_model_name  text NOT NULL,
    modality        text NOT NULL,
    -- 结构级「可承载」：上游收不收这个模态的内容块。
    carry_level     text NOT NULL DEFAULT 'unknown',
    -- 语义级「真能读」：模型能否真的读出内容。
    read_level      text NOT NULL DEFAULT 'unknown',
    -- 两胜定「真能读」、两负定「真看不见」。单次判据的假阳性率是
    -- 1/(8·7·6·5) = 1/1680，单次不足以把一个模型永久标成多模态。
    -- 两个计数器互斥：一次 confirmed 把 neg 清零，一次 negative 把
    -- pos 清零，于是「偶发蒙对一次」不会留下痕迹。
    read_pos_streak smallint NOT NULL DEFAULT 0,
    read_neg_streak smallint NOT NULL DEFAULT 0,
    carry_evidence  jsonb NOT NULL DEFAULT '{}'::jsonb,
    read_evidence   jsonb NOT NULL DEFAULT '{}'::jsonb,
    checked_at      timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_modality_verification_modality_check
        CHECK (modality IN ('vision', 'audio', 'video')),
    -- unknown 是刻意的第三态：网络错 / 鉴权失败 / 5xx / 回答被截断时
    -- **不写结论**。把「没探到」写成 negative 等于用一个默认值关掉
    -- 一个可能完全正常的绑定（bg/capability_backfill.go 的第 2 条
    -- 不可让步约束，同一条纪律）。
    CONSTRAINT model_modality_verification_carry_check
        CHECK (carry_level IN ('unknown', 'accepted', 'rejected')),
    CONSTRAINT model_modality_verification_read_check
        CHECK (read_level IN ('unknown', 'confirmed', 'negative')),
    CONSTRAINT model_modality_verification_key
        UNIQUE (credential_id, raw_model_name, modality)
);

COMMENT ON TABLE public.model_modality_verification IS
    'Per (credential, raw_model, modality) graded modality evidence. carry_level = structural "the upstream accepts this modality payload"; read_level = semantic "the model demonstrably reads the content". The two are separate columns because carry=accepted with read=negative is exactly the accept-then-ignore case that a single boolean records as supported.';
COMMENT ON COLUMN public.model_modality_verification.read_level IS
    'confirmed = the model read a challenge image correctly; negative = it demonstrably could not; unknown = no verdict (transport error, auth failure, 5xx, or an inconclusive/truncated answer). unknown never promotes or demotes a model.';
COMMENT ON COLUMN public.model_modality_verification.read_pos_streak IS
    'Consecutive semantic successes. confirmed requires 2: a single challenge pass has a 1/1680 blind-guess rate, which is not strong enough to permanently mark a model multimodal.';

-- 定时核实的到期扫描：按 (modality, checked_at) 找「从没查过 / 过期」。
CREATE INDEX IF NOT EXISTS idx_model_modality_verification_due
    ON public.model_modality_verification (modality, checked_at);

-- 模型级判词的汇总裁定面。
CREATE INDEX IF NOT EXISTS idx_model_modality_verification_canonical
    ON public.model_modality_verification (canonical_id, modality, read_level);

-- ---------------------------------------------------------------------------
-- (c) 模型级判词视图
--
-- 判词口径（路由只信这一层）：
--   confirmed —— 至少一个绑定拿到语义正证据。模型能力只看「有没有一处
--                真的能读」，同一模型在别家被中转阉割不改变它本身的能力。
--   negative  —— 所有探过的绑定都判负，且没有任何一条是 unknown。
--                带 unknown 就不下判词：还有没探透的，不能拿已知的那几
--                条否掉整个模型。
--   unknown   —— 其余（含「一条都没探过」）。
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW public.v_model_modality_verdict AS
SELECT
    v.canonical_id,
    v.canonical_name,
    v.modality,
    COUNT(*)::int                                                      AS bindings_probed,
    COUNT(*) FILTER (WHERE v.read_level = 'confirmed')::int            AS bindings_confirmed,
    COUNT(*) FILTER (WHERE v.read_level = 'negative')::int             AS bindings_negative,
    COUNT(*) FILTER (WHERE v.read_level = 'unknown')::int              AS bindings_unknown,
    COUNT(*) FILTER (WHERE v.carry_level = 'accepted')::int            AS carry_accepted,
    COUNT(*) FILTER (WHERE v.carry_level = 'rejected')::int            AS carry_rejected,
    MAX(v.checked_at)                                                  AS last_checked_at,
    CASE
        WHEN COUNT(*) FILTER (WHERE v.read_level = 'confirmed') > 0 THEN 'confirmed'
        WHEN COUNT(*) FILTER (WHERE v.read_level = 'unknown') = 0
         AND COUNT(*) FILTER (WHERE v.read_level = 'negative') > 0 THEN 'negative'
        ELSE 'unknown'
    END                                                               AS verdict
FROM public.model_modality_verification v
GROUP BY v.canonical_id, v.canonical_name, v.modality;

COMMENT ON VIEW public.v_model_modality_verdict IS
    'Per (model, modality) rollup of graded modality evidence. verdict is one of confirmed/negative/unknown; unknown means "no verdict yet" and must never be treated as a negative.';

COMMIT;

-- Migration: 995_error_playbook
-- Purpose: 错误处理手册（Error Handling Playbook）——「错误类型 → 预定执行方案」的结构化手册
-- Phase: 错误处理手册轮（2026-11-20）
-- Author: System
-- Date: 2026-11-20
--
-- ★ 背景（owner 需求，逐句对应）：
--
--	「当系统发生错误时，如果是已经在错误处理手册中的错误类型，请使用预定的
--	 执行方案执行；当是未知错误时，PI 智能体需要进行 log 及上下文分析，找到
--	 问题的原因，给出执行方案，进行执行修正，然后将这个新的错误类型记录到
--	 错误处理手册中，并给出执行方案，供后面以标准模式执行。执行的方案可以是
--	 一段动态脚本或一段 API，或 AI 执行的提示词。」
--
--   此前全仓只有 PI 检测错误自己的分析管线（971：error_analysis_queue →
--   error_analysis_results → 一键应用修复），**没有一本覆盖全系统错误类型
--   （errorsx.ErrorKind + PI 队列类型）的结构化手册**。本迁移建出手册的
--   两个载体：
--
--     · error_playbook_entries     —— 手册本体：错误类型（+ 可选正文匹配
--       模式）→ 预定执行方案。方案三种形态（plan_kind 闭集）：
--         - script：一段「动作步骤脚本」（白名单原语的有序步骤，非任意
--           shell——任意代码执行被有意排除，防 LLM 建议把 shell 注进生产）；
--         - api：一次管理面 API 调用（method/path/body 模板）；
--         - prompt：一段 AI 执行提示词（交给 PI 智能体/运维执行的分析指引，
--           本身不直接改状态）。
--     · error_playbook_executions  —— 执行留痕：每次匹配/执行/跳过都记一行，
--       script/api 步骤的旧值快照进 steps JSONB ⇒ 可按快照回滚（与 979
--       applied_actions 同纪律：改了什么、改之前是什么，永远可回答）。
--
-- ★ 自动完善：entries 自带 hit_count / execution_count / success_count /
--   failure_count / refinement_count 与 last_* 列 —— PI 智能体分析未知错误
--   后落一条 source='pi_analysis' 的条目；标准模式反复命中时计数自增；
--   标准执行失败时分析器带着失败上下文重新提炼方案（refinement_count++）。
--
-- ★ 安全默认值（fail-closed）：
--     · auto_execute 默认 FALSE —— 手册条目默认「只记录 + 待确认」，
--       管理面显式打开后才进入自动标准执行；PI 分析落库的条目里，
--       **非变更**方案（prompt / 只读步骤）才允许 auto_execute=TRUE。
--     · 种子条目（source='seed'）全部 plan_kind='prompt' 且 auto_execute=FALSE
--       —— 手册从第一天就有内容，但没有一条会自动改生产状态。
--
-- ★ 唯一键：UNIQUE (error_type, COALESCE(match_pattern,''))（表达式唯一索引）
--   —— 同一错误类型下「类型级条目」与「正文模式条目」各最多一条，且
--   ON CONFLICT 面向这个表达式（种子重放幂等的承重结构）。match_pattern 为
--   NULL 的行在表达式里归一成 '' ⇒ 与「有 pattern 的行」不冲突。
--
-- ★ 锁面：两张新表（零存量行）＋ 5 个索引 ⇒ 建表瞬间完成，不碰任何热表。
--   声明 5s lock_timeout / 10min statement_timeout（与 977/978/979 同口径；
--   用 SET（会话级）而非 SET LOCAL：docker 通道是非事务流式 psql，SET LOCAL
--   静默失效——994 的实测记录）。
--
-- ★ 幂等：CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS +
--   DROP TRIGGER IF EXISTS + 种子 ON CONFLICT DO NOTHING +
--   台账 ON CONFLICT DO UPDATE ⇒ 内容指纹重放零冲突。

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

-- ============================================================================
-- Part 1: error_playbook_entries —— 手册本体
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.error_playbook_entries (
    id             BIGSERIAL PRIMARY KEY,
    error_type     TEXT NOT NULL,
    match_pattern  TEXT,
    title          TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    root_cause     TEXT NOT NULL DEFAULT '',
    plan_kind      TEXT NOT NULL CHECK (plan_kind IN ('script', 'api', 'prompt')),
    plan_payload   JSONB NOT NULL,
    confidence     NUMERIC(3,2) NOT NULL DEFAULT 1.00 CHECK (confidence >= 0 AND confidence <= 1),
    source         TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('seed', 'manual', 'pi_analysis', 'refinement')),
    source_queue_id BIGINT,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    auto_execute   BOOLEAN NOT NULL DEFAULT FALSE,
    hit_count      BIGINT NOT NULL DEFAULT 0,
    execution_count BIGINT NOT NULL DEFAULT 0,
    success_count  BIGINT NOT NULL DEFAULT 0,
    failure_count  BIGINT NOT NULL DEFAULT 0,
    last_hit_at    TIMESTAMPTZ,
    last_executed_at TIMESTAMPTZ,
    last_status    TEXT,
    refinement_count INT NOT NULL DEFAULT 0,
    created_by     TEXT NOT NULL DEFAULT 'system',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 唯一键（表达式）：error_type + 归一化的 match_pattern。
CREATE UNIQUE INDEX IF NOT EXISTS uq_error_playbook_type_pattern
    ON public.error_playbook_entries (error_type, COALESCE(match_pattern, ''));

CREATE INDEX IF NOT EXISTS idx_error_playbook_entries_enabled
    ON public.error_playbook_entries (enabled, error_type);
CREATE INDEX IF NOT EXISTS idx_error_playbook_entries_last_hit
    ON public.error_playbook_entries (last_hit_at DESC);

-- updated_at 触发器（971 同款纪律）。
CREATE OR REPLACE FUNCTION public.fn_error_playbook_entries_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_error_playbook_entries_updated_at ON public.error_playbook_entries;
CREATE TRIGGER trigger_error_playbook_entries_updated_at
    BEFORE UPDATE ON public.error_playbook_entries
    FOR EACH ROW EXECUTE FUNCTION public.fn_error_playbook_entries_updated_at();

-- ============================================================================
-- Part 2: error_playbook_executions —— 执行留痕（含每步旧值快照）
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.error_playbook_executions (
    id             BIGSERIAL PRIMARY KEY,
    entry_id       BIGINT REFERENCES public.error_playbook_entries(id) ON DELETE SET NULL,
    error_type     TEXT NOT NULL,
    match_pattern  TEXT,
    trigger_source TEXT NOT NULL CHECK (trigger_source IN ('standard', 'auto_analyze', 'manual', 'refinement')),
    context        JSONB,
    status         TEXT NOT NULL CHECK (status IN ('succeeded', 'failed', 'skipped', 'awaiting_approval', 'no_action')),
    steps          JSONB,
    error_message  TEXT,
    duration_ms    INT,
    executed_by    TEXT NOT NULL DEFAULT 'system',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_error_playbook_exec_entry
    ON public.error_playbook_executions (entry_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_error_playbook_exec_type
    ON public.error_playbook_executions (error_type, created_at DESC);

-- ============================================================================
-- Part 3: 种子条目 —— 手册第一天的内容（全部 prompt 型 + 不自动执行）
--
-- ★ 每条种子都来自本仓自己的事故/审计沉淀（AGENTS.md 与主档），给出的是
--   「标准处置路径」的分析指引，不是可自动落地的变更。运维确认后可在管理面
--   把任何一条升级成 script/api 方案并按需打开 auto_execute。
-- ★ ON CONFLICT 面向 Part 1 的表达式唯一索引 ⇒ 重放幂等。
-- ============================================================================

INSERT INTO public.error_playbook_entries
    (error_type, match_pattern, title, description, root_cause, plan_kind, plan_payload, confidence, source, enabled, auto_execute, created_by)
VALUES
    ('auth', NULL,
     '鉴权类失败：先验证凭据是否真失效，再动闸门',
     '上游/凭据返回 401/403 类鉴权失败。历史上多次出现「非鉴权错误被归因兜底吞进 auth」的误分类（如 insufficient_user_quota、分组无权访问 403 被当成凭据失效），直接下线凭据会误伤健康凭据。',
     '真失效（key 被删/被吊销）或误分类（配额/分组问题混进 auth 兜底）。先区分再处置。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（鉴权类错误）：1) 取该 error_type 最近样本，核对上游响应原文里的错误码/错误体——确认是真鉴权失败（invalid api key / token revoked）还是配额/分组类 403 被兜底归因吞进 auth；2) 真失效 ⇒ 在管理面下线该凭据并观察 dispatch 守约（auth_failed 窗口应零流量）；3) 疑似误分类 ⇒ 不要动凭据，登记误报并把错误体样本带给 PI 智能体做上下文分析；4) 处置后 15 分钟回看该凭据的错误计数是否归零。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:995'),

    ('rate_limit', NULL,
     '上游限流：确认窗口口径，禁止把单点 429 升级成凭据级拉黑',
     '上游 429。2026-11-02 生产事故的放大器形态：把「上游说了限流」当成「这个节点限流了」，连锁触发节点拉黑 + 连败累计 ⇒ 整凭据 cooling ⇒ 全租户 503。',
     '上游对窗口内请求量的裁决，属于归因信号；对节点的裁决（拉黑/冷却）必须由健康阶梯独立决定。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（上游限流）：1) 确认错误落在哪个窗口口径（rpm/tpm/并发），看是单凭据还是同供应商多凭据同时 429；2) 单凭据偶发 ⇒ 只做归因记录，不动节点健康状态；3) 持续 429 ⇒ 核对该凭据的探针节奏是否在抢上游限流窗口，调整探针间隔；4) 全供应商同时 429 ⇒ 检查是否自身流量突增（会话频率异常检测）而非上游问题；5) 任何情况下不得手工把 429 直接升级成凭据级拉黑。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:995'),

    ('quota_permanent', NULL,
     '永久配额耗尽：确认后走凭据下线流程并观察守约',
     '上游永久性配额/余额耗尽。要点是把「永久」与「周期性」区分开（quota_periodic 会自动恢复，quota_permanent 不会），误判方向会造成恢复探测空转或健康凭据被错杀。',
     '上游账户级配额/余额用尽，短期不会恢复。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（永久配额）：1) 核对上游错误体：区分 insufficient_quota / billing 类（永久）与 daily/periodic 上限类（周期性，等恢复即可）；2) 确认永久 ⇒ 管理面下线该凭据（标记 quota_permanent），让恢复探测停止空转；3) 下线后观察 dispatch 守约：该凭据不应再被派发（auth_failed/quota 窗口零流量）；4) 若同供应商多凭据同时报永久配额 ⇒ 升级为供应商级问题，检查是否上游整体计费故障，登记给运营。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:995'),

    ('context_budget_exceeded', NULL,
     '上下文预算超限：检查 max_tokens 全额预留与固定大载荷的冲突',
     '会话反复触发 context_budget_exceeded ⇒ 会话砖化。2026-11-13 教训（r1051）：「输入半窗帽 W/2」遇上 ZCode 固定 70k 载荷 ⇒ Fit 永不可行 ⇒ 每轮必 422；2026-11-11 教训：body 的 max_tokens(128k) 被全额当预留扣 200k 窗 ⇒ 输入预算只剩 72k。',
     '预算语义配置与真实载荷形状冲突（预留过大 / 半窗帽对固定大载荷不可满足），不是单次请求的问题。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（上下文预算超限）：1) 取指纹（actual vs limit）：若 limit 明显小于模型窗口 ⇒ 检查是否 body max_tokens 被全额当预留扣减（应按半窗封顶）；2) 若固定客户端载荷恒大于预算 ⇒ 检查是否存在按 W/2 的输入半窗帽（对固定大载荷会话不可满足，应移除让预算=窗口−预留）；3) 单会话偶发 ⇒ 引导客户端缩短上下文或换更大窗口模型；4) 处置后用同形状载荷重放验证一次（422 应消失）。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:995'),

    ('no_available_channel', NULL,
     '全候选不可用：先分清熔断态与结构性缺口，再恢复',
     '某模型全部候选节点不可用（「无可用节点/渠道」）。2026-11-13 教训：这类 503 可能是「熔断器开着 + 探测没跟上」的临时态，也可能是「该模型在所有供应商都没有健康凭据」的结构性缺口——两种处置完全不同。',
     '候选池被健康阶梯/熔断清空（临时），或该模型没有任何健康凭据（结构性）。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（无可用渠道）：1) 查该模型的候选面：node_probe_state 里健康节点数、熔断器状态；2) 健康节点存在但被熔断 ⇒ 检查熔断是否由近期单点错误放大（误罚），必要时手动复位熔断并观察；3) 候选面本身为空 ⇒ 结构性缺口：该模型在所有供应商都无健康凭据 ⇒ 走凭据补充/供应商配置，不是探测问题；4) 临时态 ⇒ 等一个探测周期（默认 5 分钟）后复看，不要连续手动重试放大压力。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:995')
ON CONFLICT (error_type, COALESCE(match_pattern, '')) DO NOTHING;

-- ============================================================================
-- Part 4: 台账自登记（≤952 纪律：迁移必须自己把行写进 public.schema_migrations）
-- ============================================================================

INSERT INTO public.schema_migrations (version, description)
VALUES ('995', 'error_playbook: 错误处理手册（错误类型→预定执行方案）两表 + 种子条目')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

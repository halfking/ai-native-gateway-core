-- ===========================================================================
-- File:          sql/migrations/startup/832_model_baseline_observation_health.sql
-- Migration:     832
-- Database:      llm_gateway
-- Purpose:       记录外部机读观察源（models.dev）**能不能用**，让漂移对账的
--                静默失效变成可数的。
--
-- 立项依据（本地实测，非推断）：
--
-- 1. 抓取失败是**真的会发生**的，而且不长。2026-10-04 跑提案工具时观察到
--    一次真实失败：
--        corroboration skipped: fetch observation source:
--        Get "https://models.dev/api.json": EOF
--    同一时刻 curl 拿到 HTTP 200 / 5,317,334 字节，Go 客户端换三种
--    User-Agent（默认 / curl / 浏览器）也全部 200 —— 所以它**不是** UA 拒绝、
--    不是网络不可达，就是**瞬时断连**。⇒ 这类失败会零星地反复出现。
--
-- 2. 但**失败姿态是静默的**。bg/pricing_baseline_reconcile.go 在抓不到源时
--    只打一行 slog.Warn 然后 return。这本身是**正确的**——原注释说得很对：
--    「没有观察就没有对账结论，往台账里写一堆 missing 会把『源挂了』与
--    『价格真的不对』混成一类信号」。判词口径它守住了。
--
--    漏掉的是另一半：**不留痕**。于是漂移对账可以连续几周每 12h 抓一次、
--    每次都失败、每次都不写任何东西，而
--      · 报表照常出数（用的是几个月前的基准价），
--      · 台账里没有一行「本轮没做成」，
--      · 健康面没有任何一条检查会响。
--    这比「压根没有对账」更坏：后者让人知道自己在裸奔，前者让人以为自己在
--    看着仪表盘。⇒ 本表补的就是「让人知道」的那一半。
--
-- 为什么单独一张表，而不是往 model_baseline_price_reconciliation 里塞一行：
--   那张台账是**按模型**分粒度的（canonical_name NOT NULL），而「本轮观察源
--   不可用」是**按周期**的事实。硬塞就得造一个 `__cycle__` 之类的假
--   canonical_name，那会污染每模型台账、并把按 canonical_name 聚合的判词统计
--   算错。⇒ 周期级事实要有周期级的落点。
--
-- 为什么不给它加一个 verdict 枚举值就完事：同上，粒度不同。
--
-- 幂等：CREATE TABLE IF NOT EXISTS，可安全重放。写路径是 upsert。
-- ===========================================================================
BEGIN;

CREATE TABLE IF NOT EXISTS public.model_baseline_price_observation_health (
    -- 观察源地址做主键：换源（LLM_GATEWAY_PRICE_OBSERVATION_URL）就是新的一行，
    -- 旧行保留成历史，而不是被覆盖掉——「我们换过源」本身是要能查的。
    source_url          text PRIMARY KEY,

    -- 最后一次**成功**拿到可解析观察价的时刻。这一个字段就是告警的依据：
    -- 「基准价多久没被外部源校验过」。
    last_success_at     timestamptz,

    -- 最后一次尝试（不论成败）。
    last_attempt_at     timestamptz,

    -- 连续失败次数。成功一次归零。分开记而不是只看 last_success_at，是因为
    -- 「正在失败」与「很久没试」是两件不同的事：前者要立刻查网络，后者要查
    -- worker 是不是根本没被调度。
    consecutive_failures integer NOT NULL DEFAULT 0,

    last_error          text,

    -- 成功那次实际读到多少条观察价。
    --
    -- 为什么记条数：HTTP 200 但内容是空对象/结构变了，解析后是 0 条，价格
    -- 一个都对不上——而这在「有没有报错」的标准下是**成功**。条数是这个
    -- 故障形状唯一的信号（0 条 = 源变了），不记它就只剩一句「最近成功过」。
    observed_models     integer,

    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT model_baseline_price_observation_health_nonneg
        CHECK (consecutive_failures >= 0 AND (observed_models IS NULL OR observed_models >= 0))
);

COMMENT ON TABLE public.model_baseline_price_observation_health IS
    'Per-source health of the external machine-readable price observation feed (models.dev). Exists so that "the drift reconciler is silently dead" becomes countable: the reconciler writes no verdicts when the source is unreadable (correct - no observation, no conclusion), which without this table leaves a multi-week outage indistinguishable from a healthy system. Check with: SELECT source_url, last_success_at, consecutive_failures, last_error FROM model_baseline_price_observation_health;';

COMMENT ON COLUMN public.model_baseline_price_observation_health.observed_models IS
    'How many model prices the last successful fetch actually yielded. 0 with last_success_at set means the source answered 200 but no longer carries the shape we parse - the failure mode that looks like success.';

COMMENT ON COLUMN public.model_baseline_price_observation_health.consecutive_failures IS
    'Reset to 0 on every success. Distinguishes "the source is failing right now" (investigate egress) from "nothing has been attempted in a long time" (investigate whether the worker is scheduled at all).';

-- 运维/健康面最常用的一条查询，给它一个索引。查询形态是
-- 「last_success_at 很旧 或 consecutive_failures > 0」。
CREATE INDEX IF NOT EXISTS idx_model_baseline_price_observation_health_stale
    ON public.model_baseline_price_observation_health (last_success_at NULLS FIRST)
    WHERE consecutive_failures > 0 OR last_success_at IS NULL;

COMMIT;

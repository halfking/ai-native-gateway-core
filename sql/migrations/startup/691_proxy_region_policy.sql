-- Migration 691: proxy region avoidance + auto-switch selection policy
-- Extends the canonical proxy management schema (646) with region-ban columns
-- and a global selection-policy singleton table. Replay-safe: safe to run multiple
-- times on existing or fresh databases.

BEGIN;

-- ── 1. Subscription-level banned regions ──────────────────────────────────────
ALTER TABLE public.proxy_subscriptions
    ADD COLUMN IF NOT EXISTS banned_regions TEXT[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN public.proxy_subscriptions.banned_regions IS
    'Subscription-level banned region codes (e.g. {US,JP}). Nodes whose location matches any entry are filtered during selection.';

-- ── 2. Node-level banned regions ──────────────────────────────────────────────
ALTER TABLE public.proxy_nodes
    ADD COLUMN IF NOT EXISTS banned_regions TEXT[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN public.proxy_nodes.banned_regions IS
    'Node-level banned region codes; combined (UNION) with subscription banned_regions during selection.';

-- ── 3. Selection policy singleton table ───────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.proxy_selection_policy (
    id                      INTEGER     PRIMARY KEY CHECK (id = 1),
    load_balance_strategy   VARCHAR(32) NOT NULL DEFAULT 'best_only'
        CHECK (load_balance_strategy IN ('best_only', 'round_robin', 'weighted_rr', 'least_conn', 'consistent_hash')),
    location_affinity       VARCHAR(32) NOT NULL DEFAULT 'any'
        CHECK (location_affinity IN ('any', 'prefer_same', 'require_same')),
    auto_disable_threshold  INTEGER     NOT NULL DEFAULT 3 CHECK (auto_disable_threshold > 0),
    auto_disable_enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    auto_recover_enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    swap_check_interval_ms  INTEGER     NOT NULL DEFAULT 30000 CHECK (swap_check_interval_ms >= 1000),
    swap_failure_threshold  INTEGER     NOT NULL DEFAULT 2 CHECK (swap_failure_threshold > 0),
    updated_at              TIMESTAMP   NOT NULL DEFAULT NOW()
);

-- Seed default policy row (id=1) if not present.
INSERT INTO public.proxy_selection_policy (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;

COMMENT ON TABLE public.proxy_selection_policy IS
    'Global proxy selection policy (load balance strategy, location affinity, auto-disable thresholds, swap intervals). Singleton table; row id=1 is authoritative.';

COMMENT ON COLUMN public.proxy_selection_policy.swap_check_interval_ms IS
    'Interval in milliseconds for swapLoop to actively probe the current selected node.';

COMMENT ON COLUMN public.proxy_selection_policy.swap_failure_threshold IS
    'Consecutive failures before triggering ForceSwap to select a different node.';

-- Record migration in the canonical ledger (646 already inserted).
INSERT INTO public.schema_migrations (version, description)
VALUES ('691', 'proxy region avoidance and auto-switch selection policy')
ON CONFLICT (version) DO NOTHING;

COMMIT;

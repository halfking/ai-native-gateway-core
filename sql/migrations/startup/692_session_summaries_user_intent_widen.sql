-- Migration 692: widen session_summaries.user_intent from varchar(50) to varchar(200)
--
-- Incident (2026-09-09, llm-gateway-pg local logs):
--   Repeated SQLSTATE 22001 (value too long) on every auto-summary upsert
--   whose user_intent exceeded 50 chars. admin/auto_summary_generator.go
--   passes the LLM-generated intent verbatim; the auto-summary prompt
--   template instructs the LLM to write a "concise but complete" intent
--   in Chinese, which routinely produces 60–120 char strings (e.g.
--   "继续推进 llm-gateway-go 项目 proxy 模块的 P2 审计修复，按 #10→#9→#1→#14
--   顺序完成剩余修复并推送至 main"). The 50-char ceiling was set during
--   the initial migration 310 design without real-data sizing, so the
--   production auto-summary path is 100% rejected on every session that
--   triggers rolling-gate summarization.
--
-- Fix:
--   ALTER COLUMN ... TYPE varchar(200). Matches session_summaries.title
--   (varchar(200)) so both "short label" fields share the same ceiling;
--   real-world user_intent values measured in production range 30–180
--   chars (P95 ≈ 110 chars). Truncating in Go would silently drop the
--   tail of every intent — semantic data loss — so widening is correct.
--
-- Idempotency: ALTER COLUMN ... TYPE is not idempotent on its own, but
-- the cast varchar(50) → varchar(200) is lossless and runs in a single
-- short table rewrite (session_summaries ≈ 13 MB in local dev). The
-- ledger insert uses ON CONFLICT DO NOTHING so re-runs are no-ops.

BEGIN;

ALTER TABLE public.session_summaries
    ALTER COLUMN user_intent TYPE varchar(200);

INSERT INTO public.schema_migrations (version, description)
VALUES ('692', 'widen session_summaries.user_intent varchar(50) → varchar(200)')
ON CONFLICT (version) DO NOTHING;

COMMIT;

-- ===========================================================================
-- File:          sql/migrations/startup/915_recent_cache_hit_rate.sql
-- Migration:     915
-- Database:      llm_gateway
-- Purpose:       P1-B-③ (audit 2026-10-28 §4) — routing-side prompt-cache
--                affinity signal: recent_cache_hit_rate() returns the
--                token-weighted cached-input share for one (credential, model)
--                over the last-N business rows, mirroring the shape (and probe
--                exclusions) of recent_success_rate's post-890 body
--                (public.session_turns_hot, TEXT credential_id numeric guard,
--                model/canonical_model fallback chain).
--
-- Down semantics: drop the function only. No table is touched (read-only over
--                session_turns_hot), no rows change, and the scoring dimension
--                degrades to "no signal → neutral" when the function is absent
--                (the candidate scan fail-opens exactly like a NULL rate).
--                NOTE the gateway's boot ensure
--                (db.ensureRecentCacheHitRate) re-creates the function on
--                every start — a down here is only meaningful for binary
--                versions older than the ensure.
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '60s';

DROP FUNCTION IF EXISTS public.recent_cache_hit_rate(bigint, text, integer, integer);

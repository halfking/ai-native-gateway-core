-- Migration 300: candidate_failure_logs.per_attempt_latency_ms
-- Created: 2026-06-23
-- PR-4 (T4 P0): per-attempt latency for candidate failures.
--
-- Context: the existing `latency_ms` column in candidate_failure_logs (migration 037)
-- was never populated — executor.go:1038 carried the comment
-- `// latency_ms: per-attempt latency isn't tracked here` and passed `nil` to
-- LogFailure. The Phase-2 PR-3 candidate_failure_logs table has been useful for
-- "which credential failed" but operators still couldn't see how long that one
-- call took, which is critical for diagnosing slow-but-failing upstreams
-- (e.g. credential returned 200 in 28s then 400 — was it the body or the wait?).
--
-- Decision: introduce a SEPARATE column rather than backfill `latency_ms`:
--   1. `latency_ms` semantics is "end-to-end candidate latency" if/when the
--      future executor gains that signal at the failure site.
--   2. `per_attempt_latency_ms` is "single upstream call latency, excluding
--      the cost of candidate-switching / routing". It is captured by stamping
--      `attemptStart := time.Now()` just before `executeAnthropic` /
--      `executeOpenAI` and reading `time.Since(attemptStart)` at the failure
--      site. Internal retry of the same credential is INCLUDED (the candidate
--      did that work, not the routing layer).
--   3. Distinct semantics justify distinct columns; nullable so historical
--      rows (pre-2026-06-23) and any non-attempt-derived future writes
--      (e.g. synthesised rows) remain valid.
--
-- Idempotent: IF NOT EXISTS, so re-running is a no-op.

ALTER TABLE candidate_failure_logs
    ADD COLUMN IF NOT EXISTS per_attempt_latency_ms INTEGER;

COMMENT ON COLUMN candidate_failure_logs.per_attempt_latency_ms IS
    'Latency of the single upstream call (per candidate attempt), excluding routing/candidate-switching overhead. Set by routing/executor.go from time.Since(attemptStart). Internal retry of the same credential is INCLUDED. NULL for rows written before 2026-06-23 (PR-4) or by paths that do not stamp an attempt timer.';

-- Migration 033: Bandit Scoring (Thompson Sampling for Credential Selection)
-- Purpose: Add bandit scoring fields to credentials table for intelligent routing
-- Author: Official Deploy Team
-- Date: 2026-06-26

-- Add bandit scoring fields to credentials table
-- These fields support Thompson Sampling algorithm for credential selection

ALTER TABLE credentials ADD COLUMN IF NOT EXISTS bandit_alpha REAL DEFAULT 1.0;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS bandit_beta REAL DEFAULT 1.0;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS last_scored_at TIMESTAMPTZ;

-- Add performance tracking fields
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS total_requests BIGINT DEFAULT 0;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS success_requests BIGINT DEFAULT 0;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS failure_requests BIGINT DEFAULT 0;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS total_latency_ms BIGINT DEFAULT 0;

-- Add rate limit penalty tracking
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS rate_limit_hits INT DEFAULT 0;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS last_rate_limit_hit TIMESTAMPTZ;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS rate_limit_penalty REAL DEFAULT 0.0;

-- Add quota tracking fields
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS quota_remaining BIGINT;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS quota_total BIGINT;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS last_quota_update TIMESTAMPTZ;

-- Add intelligence rank field (1-100, lower is smarter)
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS intelligence_rank INT DEFAULT 50;

-- Create index for bandit scoring queries
CREATE INDEX IF NOT EXISTS idx_credentials_bandit_score 
    ON credentials(last_scored_at DESC) 
    WHERE status = 'active';

-- Comments
COMMENT ON COLUMN credentials.bandit_alpha IS 
    'Thompson Sampling Beta distribution alpha parameter (success count + 1)';

COMMENT ON COLUMN credentials.bandit_beta IS 
    'Thompson Sampling Beta distribution beta parameter (failure count + 1)';

COMMENT ON COLUMN credentials.last_scored_at IS 
    'Timestamp of last bandit score calculation';

COMMENT ON COLUMN credentials.total_requests IS 
    'Total number of requests sent to this credential';

COMMENT ON COLUMN credentials.success_requests IS 
    'Number of successful requests (2xx responses)';

COMMENT ON COLUMN credentials.failure_requests IS 
    'Number of failed requests (4xx/5xx responses)';

COMMENT ON COLUMN credentials.total_latency_ms IS 
    'Cumulative latency in milliseconds for all requests';

COMMENT ON COLUMN credentials.rate_limit_hits IS 
    'Number of 429 rate limit errors encountered';

COMMENT ON COLUMN credentials.last_rate_limit_hit IS 
    'Timestamp of most recent 429 error';

COMMENT ON COLUMN credentials.rate_limit_penalty IS 
    'Current penalty score (0-10) for rate limiting, decays over time';

COMMENT ON COLUMN credentials.quota_remaining IS 
    'Remaining quota (parsed from rate-limit headers)';

COMMENT ON COLUMN credentials.quota_total IS 
    'Total quota limit (parsed from rate-limit headers)';

COMMENT ON COLUMN credentials.last_quota_update IS 
    'Timestamp of last quota information update';

COMMENT ON COLUMN credentials.intelligence_rank IS 
    'Model intelligence rank (1-100, lower is smarter) based on benchmarks';

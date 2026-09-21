-- Migration 662: Feature Distribution and Deduplication Statistics
-- Purpose: Monitor structured features quality for AUTO route ML training
-- Part of: P2.3 - Monitor Feature Distribution
-- Privacy: Only aggregates non-reversible structured features

-- Feature distribution statistics table
CREATE TABLE IF NOT EXISTS feature_distribution_stats (
  id BIGSERIAL PRIMARY KEY,
  stat_date DATE NOT NULL,
  feature_name TEXT NOT NULL, -- detected_language, prompt_length_bucket, etc.
  feature_value TEXT NOT NULL, -- zh, en, s, m, l, NULL (for missing values)
  row_count INTEGER NOT NULL,
  percentage NUMERIC(5,2) NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT valid_percentage CHECK (percentage >= 0 AND percentage <= 100)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_feature_stats 
  ON feature_distribution_stats(stat_date, feature_name, feature_value);

CREATE INDEX IF NOT EXISTS idx_feature_stats_date 
  ON feature_distribution_stats(stat_date DESC);

CREATE INDEX IF NOT EXISTS idx_feature_stats_name 
  ON feature_distribution_stats(feature_name, stat_date DESC);

COMMENT ON TABLE feature_distribution_stats IS 
  'Daily aggregates of structured feature distributions for ML training quality monitoring';

COMMENT ON COLUMN feature_distribution_stats.feature_name IS 
  'Name of the structured feature column (e.g., detected_language, prompt_length_bucket)';

COMMENT ON COLUMN feature_distribution_stats.feature_value IS 
  'Value of the feature (e.g., zh, en, s, m, l) or NULL for missing values';

COMMENT ON COLUMN feature_distribution_stats.percentage IS 
  'Percentage of total rows for this feature value on stat_date';

-- Deduplication rate statistics table
CREATE TABLE IF NOT EXISTS dedup_stats (
  id BIGSERIAL PRIMARY KEY,
  stat_date DATE NOT NULL,
  total_rows INTEGER NOT NULL,
  unique_hashes INTEGER NOT NULL,
  dedup_rate NUMERIC(5,2) NOT NULL, -- percentage of duplicate rows
  duplicate_count INTEGER NOT NULL, -- total_rows - unique_hashes
  top_duplicate_hashes JSONB, -- top 10 most frequent content_hash values
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT valid_dedup_rate CHECK (dedup_rate >= 0 AND dedup_rate <= 100),
  CONSTRAINT valid_counts CHECK (unique_hashes <= total_rows)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_dedup_stats 
  ON dedup_stats(stat_date);

CREATE INDEX IF NOT EXISTS idx_dedup_stats_date 
  ON dedup_stats(stat_date DESC);

COMMENT ON TABLE dedup_stats IS 
  'Daily deduplication rate metrics for content_hash based duplicate detection';

COMMENT ON COLUMN dedup_stats.dedup_rate IS 
  'Percentage of duplicate rows: (1 - unique_hashes/total_rows) * 100';

COMMENT ON COLUMN dedup_stats.top_duplicate_hashes IS 
  'Top 10 most frequently seen content_hash values with their counts';

-- Feature quality metrics view (for Grafana)
CREATE OR REPLACE VIEW feature_quality_metrics AS
SELECT 
  stat_date,
  feature_name,
  SUM(row_count) FILTER (WHERE feature_value != 'NULL') AS filled_count,
  SUM(row_count) AS total_count,
  ROUND(100.0 * SUM(row_count) FILTER (WHERE feature_value != 'NULL') / NULLIF(SUM(row_count), 0), 2) AS fill_rate_pct
FROM feature_distribution_stats
GROUP BY stat_date, feature_name;

COMMENT ON VIEW feature_quality_metrics IS 
  'Aggregated fill rates per feature for monitoring feature extraction quality';

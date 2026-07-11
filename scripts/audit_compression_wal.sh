#!/usr/bin/env bash
set -euo pipefail

# Read-only audit for request_wal. It intentionally reports whether body rows
# were retained separately; a strategy count alone cannot establish byte/token
# savings after request_wal_bodies retention has rotated the payload away.

DB_DSN="${DATABASE_URL:?set DATABASE_URL to the read-only audit database}"
WINDOW="${1:-30 days}"

psql "$DB_DSN" -v ON_ERROR_STOP=1 -P pager=off \
  -v audit_window="$WINDOW" <<'SQL'
WITH strategy AS (
  SELECT
    compression_strategy,
    COUNT(*) AS requests,
    MIN(created_at) AS first_seen,
    MAX(created_at) AS last_seen,
    COUNT(*) FILTER (WHERE compression_meta ? 'window_triggered') AS with_window_meta,
    COUNT(*) FILTER (WHERE compression_meta ? 'summary_marker') AS with_summary_meta
  FROM request_wal
  WHERE compression_strategy IS NOT NULL
    AND compression_strategy <> ''
    AND created_at >= NOW() - CAST(:'audit_window' AS interval)
  GROUP BY compression_strategy
), bodies AS (
  SELECT
    w.compression_strategy,
    COUNT(*) AS retained_bodies,
    ROUND(AVG(LENGTH(b.outbound_body::text))) AS avg_body_bytes,
    PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY LENGTH(b.outbound_body::text)) AS p50_body_bytes,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY LENGTH(b.outbound_body::text)) AS p95_body_bytes
  FROM request_wal w
  JOIN request_wal_bodies b USING (request_id, created_at)
  WHERE w.compression_strategy IS NOT NULL
    AND w.compression_strategy <> ''
    AND w.created_at >= NOW() - CAST(:'audit_window' AS interval)
  GROUP BY w.compression_strategy
)
SELECT s.compression_strategy,
       s.requests,
       s.first_seen,
       s.last_seen,
       s.with_window_meta,
       s.with_summary_meta,
       COALESCE(b.retained_bodies, 0) AS retained_bodies,
       b.avg_body_bytes,
       b.p50_body_bytes,
       b.p95_body_bytes,
       CASE WHEN b.retained_bodies IS NULL THEN 'strategy-only'
            ELSE 'body-retained' END AS evidence_level
FROM strategy s
LEFT JOIN bodies b USING (compression_strategy)
ORDER BY s.requests DESC, s.compression_strategy;
SQL

# Connection Registry Page

Register the “connection identity” of each upstream credential: node, recovery timeline, and status change history.

## Definition

One credential per row; the recovery timeline records every step from failure to recovery for that credential.

## How to Self-Check When the Numbers Do Not Match

- 1.The credential appears normal on the Credential Monitoring page but abnormal here: the two pages offer different perspectives (monitoring shows real-time health, while the registry shows historical records). Use the timeline as the source of truth for tracing.
- Gap in the recovery timeline: records are not backfilled for intervals when the collection service restarts; gaps are a known pattern.

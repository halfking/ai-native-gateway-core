# Credential Health Timeline Page

A timeline of a credential’s health history: when it was healthy, when it failed, and when it recovered.

## Metric Definitions

The horizontal axis = time, and the color block = status; the timeline is append-only and never modified, making it the primary record for tracing credential quality.

## How to Self-Check When the Numbers Do Not Match

- 1. There are gaps in the timeline: collection interruptions are not backfilled (service restart windows); a gap ≠ normal.
- If the status does not match the monitoring page: use the most recent color block on the timeline, as the monitoring page may be cached.

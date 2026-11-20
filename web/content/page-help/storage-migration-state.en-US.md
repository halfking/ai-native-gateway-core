# Storage Migration Status Page

A dashboard showing the progress of data moving between storage layers.

## Metric Definitions

Migration = batch copying + verification + cutover; progress is measured by batch.

## How to Self-Check When Numbers Do Not Match

- 1. Progress is stuck: Moving a single large table can take a long time, so check the batch details rather than the overall percentage.
- Verification failed: You can rerun the failed batch without affecting completed batches.

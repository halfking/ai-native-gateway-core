# Scheduling Waterfall Page

Show the scheduling process for each request as a waterfall: candidates are tried one by one, making it clear which one accepted the request and which ones failed.

## Calculation Notes

Bar length = time taken by that candidate; success/failure labels appear at the end of the bar; each request has only one final success record.

## What to Check When Numbers Do Not Match

- 1. Number of waterfall bars is greater than the number of candidates: internal retries are included; use the “Final success” row as the source of truth.
- Total time differs from the request details: the waterfall includes only the scheduling phase and excludes model generation time.

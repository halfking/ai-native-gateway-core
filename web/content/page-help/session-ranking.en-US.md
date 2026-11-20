# Session Leaderboard Page Data Notes

This page breaks session analysis into three leaderboards: **who is using it** (people), **who is doing the work** (agents), and **what is used the most** (models).

## Leaderboard Table Columns

- Session count / Request count / Token count: each is counted independently (sessions are deduplicated, requests are counted individually, and Tokens are totaled).
- Cost: the **combined total** of internal costs and external credits. It is intended only for relative comparison and should not be interpreted as a monetary amount (the two use different units).
- Success rate / Cache hit rate / Average duration: efficiency metrics for the item.

## Sorting and Time Window

- Select the time range at the top of the page; the leaderboard is sorted by the selected metric, and you can click a column header to change the sort key.
- “Rank change” = movement in rank compared with the previous window of equal length; newly ranked items show “New”.

## How to Check for Mismatched Numbers

1. The same person has different session counts in two places: the leaderboard aggregates data for the selected window, while the details page may default to a different window; align the time ranges first.
2. The “Cost” column does not match the bill: this column is the combined total of two cost measures for comparison only; always use the Reconciliation Report page for reconciliation.
3. Anonymous/display name mismatch: the configured display name takes priority; if none is set, the system account name is shown. After a name change, historical data remains aggregated under the same identity and does not split into two people.
4. Leaderboard total ≠ Overview page total: the leaderboard shows the Top list; low-volume, long-tail items are not included, while the Overview page includes all items.

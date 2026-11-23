# Session Analytics Overview: Data Notes

This page answers “How are people using it?”: how many sessions there are, how long they last on average, what tasks they cover, and how trends are changing.

## Key Metrics

| Card | What It Means | Calculation |
| --- | --- | --- |
| Total sessions | Number of conversations during the period | Deduplicated count: each session is counted once, regardless of how many turns it contains |
| Average Token count | Average amount of text consumed per session | Total Tokens across all sessions ÷ Total sessions |
| Average session duration | Average length of each session | Average of the time differences between the first and last timestamps for each session |
| Average turns | Average number of question-and-answer exchanges per session | Total turns ÷ Total sessions |
| Internal cost (USD) | Cost paid by the platform to upstream providers in USD | Summed per transaction using upstream pricing |
| External cost (credits) | Credits billed to tenants | Summed per transaction using platform pricing |

**Internal and external costs are not combined**: USD and credits are different units, so adding them has no business meaning. The trend charts at the top of the page also show the two in their respective units.

## Trends and Rankings

- The trend chart aggregates data by day (using UTC day boundaries) and shows daily changes in session counts or costs.
- “vs. previous period” = percentage change between the current window and the previous window of equal length.
- Rankings (people/agents/models) sort by the selected metric. Sorting by “Spend” uses the backend compatibility field `total_cost`, which adds the raw numeric values of internal cost in USD and external cost in credits without currency conversion. The list may still format this value with a dollar sign, so it is not an amount and cannot be compared across currencies. Use internal cost (USD) and external cost (credits) separately.

## Data Freshness

This page uses an “aggregated snapshot” generated on a schedule, rather than transaction-level details. The snapshot has a freshness limit (about 90 minutes). When this limit is exceeded, the page displays an “Aggregated data is refreshing” banner and refuses to return stale figures—this is a safeguard, not an outage.

## Self-Checking When Numbers Do Not Match

1. Time range: This page and the request logs page use different windows, so the figures will necessarily differ. Align the time ranges first.
2. Snapshot latency: Recent requests may not yet be included in the aggregated snapshot. Wait for the refresh before comparing.
3. Calculation differences: This page aggregates by “session,” while the request logs page breaks data down by “individual call,” so the counts will naturally differ.
4. Deduplication rules: Session counts are deduplicated by session, so multiple turns in the same session are not counted repeatedly. Turn counts are tallied per turn.

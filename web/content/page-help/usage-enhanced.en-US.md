# Enhanced Usage Analysis Page Data Notes

Summary, cost attribution, cache economics, and top API keys use `request_metrics` for the selected date range. The comparison card uses the current and previous calendar months.

## Core Cards

- **Total Cost (USD)** uses recorded USD request costs in the window. Credits are tracked separately.
- **Total Requests** counts requests by tenant and request ID using the latest projected fact version.
- **Period-over-Period Change** compares the current and previous calendar months.

## Metric Definitions

- Cache hit rate uses input tokens with a known accounting convention. Cache and compression savings require contract rates or itemized bills.
- Monthly comparison sums projected USD costs for each calendar month. The current month may still be in progress. A prior-month total of zero leaves the percentage without a valid baseline.

## How to Self-Check When Numbers Do Not Match

1. Match the selected dates and traffic scope with the dashboard. The comparison card always uses two calendar months.
2. Include uncategorized groups and “Other” when comparing costs. The top API keys section covers the keys in the ranking.
3. Check the projection’s freshness. A degraded notice means the affected metrics are temporarily unavailable.

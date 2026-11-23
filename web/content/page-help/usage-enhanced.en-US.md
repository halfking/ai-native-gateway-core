# Enhanced Usage Analysis Page Data Notes

This page places results from five APIs side by side: summary, cost trend, monthly comparison, cache economics, and top API keys. They are not repeated measurements from one table or one shared time window.

## Core Cards

- **Total Cost (USD)**: Total amount paid to upstream providers during the selected time window.
- **Total Requests**: Number of requests in the same time window.
- **Period-over-Period Change**: Percentage comparison between the current window and the previous window of equal length.
- Detailed Breakdown: Lists contribution shares by dimensions such as model, key, and day.

## Metric Definitions

- The page combines five independent endpoints. Summary and top API keys use a day-count window; cost trend and cache economics use the selected date range; the comparison card compares the current calendar month with the previous calendar month. Their sources and windows differ, so they are not expected to match the dashboard or each other item for item.
- The comparison endpoint sums upstream cost in USD by calendar month and calculates (current-month cost − previous-month cost) ÷ previous-month cost. It does not verify that either month is complete. If previous-month cost is zero, the change percentage is zero; this does not establish that both months are complete or usage was unchanged.

## How to Self-Check When Numbers Do Not Match

1. When comparing the period card with metrics for the selected date range, remember that the card always compares the current and previous calendar months; it does not follow the selected start and end dates.
2. Cost trend groups records by dimension. Missing dimension values are grouped as “unknown,” and long-tail groups may be combined as “Other.” Totals can differ when sources or windows do not fully align.
3. Summary and top API keys use a day-count window, while cost trend and cache economics use the selected date range. Window boundaries, refresh timing, and an in-progress current day can all cause differences. If an endpoint shows a degraded-data notice, its zero or empty result is not a measured zero.

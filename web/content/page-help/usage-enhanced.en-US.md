# Enhanced Usage Analysis Page Data Notes

This page combines usage data from five APIs into a comprehensive view: total cost, total requests, period-over-period change, and detailed breakdown—all on one screen.

## Core Cards

- **Total Cost (USD)**: Total amount paid to upstream providers during the selected time window.
- **Total Requests**: Number of requests in the same time window.
- **Period-over-Period Change**: Percentage comparison between the current window and the previous window of equal length.
- Detailed Breakdown: Lists contribution shares by dimensions such as model, key, and day.

## Metric Definitions

- Data on this page comes from the same aggregation API as the dashboard, so the numbers should match. If they do not, first make sure the time windows align.
- Period-over-period figures are shown only when both windows have complete data. If even one day of data is missing, the page displays “Incomplete Data,” and the percentages are for reference only.

## How to Self-Check When Numbers Do Not Match

1. The period-over-period percentage seems counterintuitive (usage has not changed, but a large change is shown): incomplete data in the previous window distorts the baseline, so first check the completeness of that window.
2. The detailed breakdown total ≠ the total on the card: requests with missing dimension values are included only in the total, and the page labels this amount as uncategorized.
3. The time window includes today: today’s figures are still increasing in real time, so the period-over-period change will continue to fluctuate. Checking again after the reporting period closes will provide more accurate results.

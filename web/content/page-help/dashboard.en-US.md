# Overview Page Data Notes

This page is the gateway’s “dashboard,” bringing together usage, costs, and health status for a selected period on a single screen.

## Where the Data Comes From

All figures on the page come from the transaction logs for every model call recorded by the gateway. The system first records each request, then aggregates the records into various statistics based on the selected time range.

## How Time Ranges Are Calculated

- The three shortcut ranges, “Today,” “Last 7 Days,” and “Last 30 Days,” are split by calendar day; the statistics window starts at midnight in the **system time zone (Asia/Shanghai by default)** and ends at the current moment (not at the end of the day).
- For a custom range, whichever two days are selected, all transactions for those two days are aggregated, from midnight at the start of the first day through midnight at the end of the last day, including the entire last day.
- The page refreshes automatically every 10 seconds. If the real-time channel is online, newly completed requests are added immediately, with no need to wait for a refresh.

## Four Core Cards

| Card | Meaning | Calculation |
| --- | --- | --- |
| Total Requests | Number of model calls processed during this period | Direct count of transaction log entries |
| Success Rate | Percentage of requests that returned an answer successfully | Number with status “Success” ÷ Total Requests |
| Total Token | Amount of text consumed during this period (input + output) | Separately sums the input and output Token counts for each request |
| Total Cost | USD-denominated billing amount for this period | Sums the cost of each request (in USD) |
| Total Credits | Number of credits under the platform’s internal billing system | ★ Currently shows the **live delta after the cached baseline**, so it does not change with the time range (see below) |

Note: **Cost (USD) and Credits are separate measures and are not combined.** Cost represents the USD cost paid to upstream providers. Credits are the unit the platform uses to bill tenants. The two values are not directly comparable, so they are displayed separately.

The credits card has a special rule: the credits column of the request-level fact table is currently always 0, so what the card shows is the “live delta after the baseline” — switching between “Today / Last 7 Days / Last 30 Days” leaves the credits card exactly unchanged. The usage trend chart on the same page carries a separate credits total (a different source) that is not the same number. For a trustworthy credit ledger, use the “Usage & Cost” and “Reconciliation” pages.

## Error Count and Failure Reasons

- “Error Count” is calculated by summing each category in the failure-reasons pie chart. If the pie chart is unavailable, it is estimated using Total Requests × (1 − Success Rate).
- Click the Error Count card to open the error distribution and see the share of each failure category.

## Definitions of “Active” Labels

- “Active Models” equals the number of models **with actual traffic** during this period, not the total number of “models available on the platform.”
- “Active Keys” equals the number of keys currently **enabled** (a configuration-based measure, independent of the time range).
- “Provider Count” equals the number of providers **with actual traffic** during this period.

## How to Self-Check When Numbers Do Not Match

1. First, confirm that the time ranges match: if the two pages use different windows, the figures will necessarily differ.
2. Check whether any cards show a “Degraded” marker: when aggregate data cannot yet be calculated, the page clearly indicates this, and a 0 at that point does not mean “no traffic.”
3. If the Credits card shows “Unreliable/Not Calculated,” the credit ledger data source is temporarily unavailable, but the request count and costs are still accurate.
4. Probe traffic is not included in business statistics: the system’s own health-check requests are excluded from Total Requests and Total Cost.
5. Real-time refresh and chart aggregation may differ by a few seconds: card figures, including real-time additions, may be slightly higher than chart figures by a few requests. This is normal.

## Compared with the “Session & Statistics” tab

This page uses the **request-level** caliber (one model call counts as one row), while “Session & Statistics” uses the **session-level** caliber (one session counts as one row, carrying that session’s cumulative values); the two tabs also differ in time window and in how probe traffic is excluded ⇒ the numbers are not supposed to be equal. For an item-by-item comparison, use the **Calculation rules** button inside the “Board” and “Session & Statistics” tabs (that document lists both calibers and the differences side by side).

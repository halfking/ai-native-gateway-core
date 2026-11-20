# Reconciliation Report Page Data Guide

This page is the “Ledger Reconciler”: every day in the early morning, it consolidates the previous day's transaction activity into a single-page ledger that can be reconciled from multiple perspectives, including provider, tenant, personnel, and model.

## How Snapshots Are Generated

- The daily aggregation job generates a snapshot of the **previous day** in the early morning. A snapshot for the current day's data is not available until the early morning of the next day, when the page will display “Today's snapshot will be generated in the early morning tomorrow.”
- Days with no snapshot (for example, if the system was down at the time) are marked “Not Aggregated”; use “Rerun end date” to manually recalculate the missing data.

## Two Amount Measures (Not Combined)

- **Provider cost (USD):** The amount the platform pays to upstream providers.
- **Internal credits / internal amount:** The platform's billing basis when charging tenants.
- The difference between the two is the platform's gross profit. The page displays them separately and does not combine totals across units.

## Four Token Components

Input (text sent to the model), Output (text returned by the model), Cache Read (input reused from a previous request, which is either not charged again or charged at a discount), and Cache Write (input stored in the cache for the first time). The four components sum to the total Token.

## How to Self-Check When Numbers Do Not Match

1. A snapshot is the “final ledger”: Differences from the real-time dashboard are normal—the snapshot is frozen in the early morning of the next day and does not change afterward; the real-time dashboard includes the current day.
2. Dates marked “Not Aggregated” have no figures: use a rerun to backfill the data before reconciling.
3. When reconciling the provider view with the provider's billing dashboard, note the time zone: this system uses UTC day boundaries, while provider bills may use local time, so traffic crossing midnight may be assigned to different days.
4. Failed requests also incur small costs (such as a partial response already billed by the upstream provider), so on days with a high failure rate, the cost per request tends to be higher.

# Reconciliation Report Page Data Guide

This page summarizes the gateway-recorded usage for the previous day from provider, tenant, person, model, and other perspectives. It helps reconcile gateway records; it is not a supplier invoice or a confirmed payable amount.

## Historical request statistics

Historical request statistics use the asynchronously updated request projection. Expand By day, open Daily details, and choose a date and a dimension. The range filters apply to both the detail view and the export. Export full range details includes every row in the selected range and dimension.

Your browser shows progress in its downloads list. If the data exceeds Excel’s row limit for a worksheet, the workbook keeps the remaining rows on numbered continuation sheets.

## How Snapshots Are Generated

- The daily aggregation job generates a snapshot of the **previous day** in the early morning. A snapshot for the current day's data is not available until the early morning of the next day, when the page will display “Today's snapshot will be generated in the early morning tomorrow.”
- Days with no snapshot (for example, if the system was down at the time) are marked “Not Aggregated”; use “Rerun end date” to recalculate the missing data. Existing snapshots can also be recalculated and are not permanently final.

## Two Amount Measures (Not Combined)

- **Provider perspective amount:** A sum of terminal usage recorded by the gateway, including usage not assigned to a provider and non-business traffic. The amount is a gateway-recorded estimate, not an amount returned by a supplier and not a confirmed payable or paid amount.
- **Internal credits / internal amount:** Business traffic only; credits are converted using the price factor saved with the snapshot. This is the platform's internal billing basis, not proof of an invoice or collection.
- Compare the measures only when traffic scope and currency match. Their difference is not automatically gross margin. The page displays them separately and does not combine totals across units.

## Four Token Components

This report currently adds input, output, cache reads and cache writes for its token total. Cache figures can overlap with input. For cross-report comparisons, use normalized input plus output and show cache reads and writes separately.

## How to Self-Check When Numbers Do Not Match

1. A snapshot is an aggregate calculated from the data available at that time. Differences from the real-time dashboard are normal—the dashboard may include current-day data, and a snapshot can be updated by rerunning aggregation.
2. Dates marked “Not Aggregated” have no figures: use a rerun to backfill the data before reconciling.
3. When comparing the provider perspective with a supplier invoice, account for scope as well as time zone: this page sums terminal usage recorded by the gateway, including unassigned and non-business traffic, and its amount is an estimate rather than the supplier's billed amount. Supplier invoices may also use local-day boundaries instead of UTC.
4. Failed requests with terminal usage or cost recorded are included in the gateway aggregate; this does not mean the supplier has confirmed a charge.

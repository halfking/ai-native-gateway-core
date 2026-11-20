# Usage Trends Page Data Notes

This page uses large charts to show how usage changes over time. You can break it down by model, key, or tenant to see increases and decreases and identify anomalies.

## Chart Definitions

- The time interval can be “hourly” or “daily”; daily intervals roll over at midnight in Coordinated Universal Time (UTC).
- A flat line (zero line) has two meanings that should be distinguished: **actual zero** (usage was genuinely zero) and **not calculated** (the data source was not ready). The page clearly indicates when a data source is not ready; in that case, the flat line does not represent zero.
- When multiple models are selected, the lines are **shown separately**, not stacked; to calculate a total, add the daily values.

## Filters Use “AND” Logic

After selecting a time + model + key and applying additional filters, the results satisfy all conditions simultaneously.

## What to Do When the Numbers Don’t Match

1. If the figure differs slightly from the dashboard: the trend page pulls data from a secondary view, and requests from the last few minutes have not yet been aggregated, so use the dashboard’s real-time figure.
2. If the curve is “missing one slot” around midnight: daily aggregation crosses the UTC midnight boundary, so daytime in Beijing corresponds to the afternoon of the previous UTC day; the different window start is a calculation-method difference.
3. If the total after splitting by dimension does not match the overall curve: some requests have missing dimension values (such as when the upstream does not return a model name). They are included only in the total, not in the breakdown—and the page labels the amount that could not be categorized.

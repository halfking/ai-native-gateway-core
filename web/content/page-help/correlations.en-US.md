# Correlation Analysis Page Data Notes

This page answers “What factors are failures associated with?” by cross-comparing various request attributes (model, length, provider) with failure outcomes to identify combinations where failures are concentrated.

## How to Read the Chart

- Each cross-tab cell shows the request volume and failure rate for that combination; darker colors indicate greater failure concentration.
- “Correlation” does not mean “causation”: the page shows statistical associations, and A and B appearing together does not necessarily mean A causes B.

## Metric Definitions

- Failure rate = number of failed requests within the combination ÷ total requests in the combination; combinations with too few samples (below the threshold) are flagged to avoid misleading results.
- The time window and filters determine the statistical scope; confirm that the windows are consistent before comparing two sets of numbers.

## Self-Checks When the Numbers Do Not Match

1. A cell has a 100% failure rate but only 2 requests: this is small-sample variation; treat it as insignificant, as indicated on the page.
2. The error count differs from the Credential Monitoring page: this page aggregates by “request feature combination,” while the Credential page aggregates by “credential,” so the dimensions differ.
3. A cell is empty after filtering: there are no samples for that combination under the selected filters, which means “no data,” not “zero failures.”

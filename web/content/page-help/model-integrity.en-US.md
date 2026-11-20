# Model Integrity Monitoring Page Data Notes

This page tracks whether answers delivered by upstream providers are complete and have not been substituted: substitution, truncation, empty responses, repetition, and fingerprint drift are all considered integrity anomalies.

## Two Tabs

- **Integrity Events**: One record per anomaly (type, severity, model/provider involved, and whether it has been resolved).
- **Fingerprint Drift**: Changes over time in the “behavioral fingerprint” of the same model—a large drift indicates that the upstream provider has silently changed its implementation.

## Metric Definitions

- Total events / unresolved / severe events are calculated based on the selected reporting window; the window can be changed.
- “Severe” = anomalies that affect answer correctness or safety (such as model substitution); “truncation/empty response” is usually medium severity.
- Fingerprint drift is a **relative baseline** comparison: after the baseline is updated, historical drift findings are recalculated.

## How to Reconcile Discrepancies in the Numbers

1. The event count differs from the Format Anomalies page: the two pages focus on different anomaly types (this page covers “answer integrity,” while that page covers “response format”), so they overlap but are not identical.
2. The “Unresolved” count is not decreasing: manual confirmation and action are required; as on the Format Anomalies page, automatic recovery does not automatically close cases.
3. All fingerprints suddenly show drift: first check whether the baseline was just updated or the reporting window was changed before suspecting a system-wide upstream revision.

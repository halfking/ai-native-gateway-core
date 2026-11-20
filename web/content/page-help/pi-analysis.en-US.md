# Error Intelligence Analysis Page Data Guide

This page uses AI to help you analyze “why requests fail”: failed items in the error queue are submitted to the agent for individual or batch analysis, which produces cause assessments and remediation recommendations.

## Two Tabs

- **Error Queue**: A list of failed records awaiting analysis (one failed request per row).
- **Analysis Results**: A list of conclusions produced by AI analysis (cause categories, evidence, and recommendations).

## Metrics for the Six Cards at the Top

- Total / Pending Analysis / Analyzed / Failed: Statistics from the **Error Queue** (queue domain).
- False Positive Rate / Average Processing Time: Statistics from the **Analysis Results** (results domain).
- The first three cards and the other three cards use different statistical scopes, so adding them together is not meaningful—the page displays them grouped by domain.

## How Remediation Recommendations Are Implemented

- AI recommendations (changing rules, adjusting levels, or changing matching patterns) require **manual approval** before taking effect. Approval is subject to a confidence threshold, and low-confidence recommendations are blocked for review.
- Every implementation retains an “old value snapshot” for one-click rollback.

## How to Reconcile When the Numbers Do Not Match

1. The queue count differs from the failure count on the Request Records page: the queue includes only failure types **included in the analysis scope**; requests blocked by an earlier step (such as authentication failures) do not enter the queue.
2. An item is marked “Analyzed,” but it cannot be found on the Results page: there may be a brief delay before the analysis results are stored. Refresh and try again.
3. The same error appears repeatedly: errors with the same pattern are analyzed together, and the queue displays a representative sample.

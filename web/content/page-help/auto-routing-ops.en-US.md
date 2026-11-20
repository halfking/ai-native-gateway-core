# AUTO Routing Operations Page Data Guide

This page is the closed-loop workspace for “automatic parameter tuning”: labeling → statistics → profile recommendations → tuning approval. The system uses this data to get better at selecting models, but **every activation must pass the gate checks and receive manual approval**.

## Four tabs form a pipeline

- **Labeling Workspace**: Manually label the quality of historical requests, providing the source data for all downstream reporting.
- **Labeling Statistics**: A summary of labeling volume (Pending Labels Today, Total Labels, Number of Participants).
- **Task Profiles**: View model performance by task type and generate profile recommendations such as “a certain model is better suited to a certain task type.”
- **Tuning Approval**: Parameter adjustment recommendations proposed by the system wait here for manual approval or rejection.

## Measurement Definitions

- “Pending Labels Today” is calculated by calendar day (UTC); unfinished items from yesterday carry over into the “Total Labels” backlog.
- Tuning proposals have a confidence threshold: the system sends only recommendations it is confident about for approval. Low-confidence recommendations are automatically put on hold so approvers are not interrupted.
- Before each tuning takes effect, an “old-value snapshot” is created. If the results are not right after approval, you can roll back to the values before the change with one click.

## How to self-check when numbers do not match

1. It is normal for the label count and request count not to match: not every request needs a label, and the system selects only a representative sample.
2. The same recommendation will not appear again after a proposal is rejected: related recommendations take historical approval decisions into account.
3. Metrics do not change after a tuning takes effect: there is a delay before parameters take effect (hot-reload interval), and results appear only on new traffic.
4. A deep link opens the wrong tab: the four legacy pages have been consolidated into one page with four tabs, and old links redirect automatically.

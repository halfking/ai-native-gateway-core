# Operations Dashboard Data Guide

This page is the operations team’s main workspace: it brings scattered operational metrics (usage, approvals, and backend tasks) together on one screen.

## Page Contents

- **Usage Overview Card**: Core metrics such as request volume, success rate, and costs (using the same data source and calculation basis as the dashboard).
- **Backend Tasks**: The status and time of the most recent execution of scheduled system jobs (aggregation, probes, synchronization, etc.).
- **Self-Check Summary**: A quick health overview of key components.

## How to Read Backend Tasks

- Each task shows the “Last Run Time + Result”; “Failed” does not mean the service is unavailable—most tasks automatically retry in the next cycle.
- A task only needs attention if it has not run for an extended period (more than twice its scheduled interval).

## Calculation Basis

- Card figures use the “Today” range by default (UTC day boundary); they align with the dashboard after the time range is changed.
- This page does not show any totals across different units; cost-related cards display only one clearly defined basis.

## How to Self-Check When Figures Do Not Match

1. Card figures differ from the dashboard by a few entries: It is normal for real-time traffic to continue increasing between refreshes; close the “Today” window or wait for the next refresh.
2. All backend tasks show “Failed”: First check whether the storage layer (database) is unavailable—most tasks rely on it.
3. A newly deployed task has not run yet: It shows “Not Run Yet”; wait for one cycle.

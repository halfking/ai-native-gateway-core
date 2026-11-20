# Conversation User Details Page Data Notes

This page provides a complete usage profile for **one user**: their conversations, projects, tasks, most-used models, and spending trends.

## Contents

- **Summary Card**: Number of conversations, Token usage, spending, success rate, cache hit rate, and average duration.
- **Trends Section**: Daily conversation and spending curves within the user’s selected time range.
- **Details Table**: Usage rows broken down by project / task / agent / model.

## Calculation Basis

- All figures count only conversations **initiated by the user**; collaborative participation (appearing in someone else’s conversation) is excluded.
- “Spending” is displayed using two calculation methods side by side; the “Ranking” card shows the user’s position in the overall ranking and the change from the previous period.

## How to Reconcile Figures When They Do Not Match

1. Details total ≠ the figure for that row on the list page: first align the time ranges on both pages (the details page has its own range selector at the top).
2. Project/task detail total ≠ summary total: conversations not categorized under a project/task are included only in the total; if the details table has an “Uncategorized” row at the bottom, use it to explain the difference.
3. The “Ranking Change” appears reversed: changes are based on **ranking position** (up = improved rank), not an increase or decrease in the numerical value.

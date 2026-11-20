# Session User List Page Data Notes

This page shows usage by person: each user has one row, with their session count, Token usage, cost, and most commonly used models summarized.

## Fields in Each Row

User (display name/account), session count, Token count, cost, success rate, Top list of most commonly used models (expand the row to view).

## Relationship to the Session Analytics Overview

- The Overview page is an aggregate across all users; this page is a **person-level breakdown** of the same data source. When the time windows match, the two should reconcile.
- The Leaderboard page is a sorted view of this page (Top N); this page is the complete list and is searchable.

## Definitions

- The “Cost” column displays two billing bases side by side and does not convert one into the other.
- A user's sessions are attributed to the “initiator”: usage by multiple participants in the same session is recorded under the initiator.

## Self-Checks When the Numbers Do Not Match

1. The user count is higher than the number of system users: this page counts identities that “have had session activity” (including system/service accounts); for a roster of people, go to the User List page.
2. Total model usage in the expanded row ≠ the row's total Token: the expanded list shows only Top models, while long-tail models are grouped under “Other”.
3. Two rows for the same person: changes to an account or display name can create two historical identities. Activity is attributed by account, so use the Account column when checking.

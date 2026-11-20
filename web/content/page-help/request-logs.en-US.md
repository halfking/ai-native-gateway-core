# Request Log Page Data Guide

This page provides a running record of all model calls: each request appears on its own row, and you can filter by specific conditions and review requests one by one.

## What Each Row Represents

One row = one model call. It includes the call time, the key and model used, status (success/failure), duration, input/output text volume, and the cost of the request.

## Time Window Rules

- Select a quick range (from the last 1 hour to the current month) or set custom start and end times.
- **Non-default tenants can view only the most recent 3 days**: if a wider range is selected, the system automatically narrows it to 3 days, which appears as the time picker shifting back in time.
- Default tenants can use all available ranges, including the last 7 days/this week/this month/this year.

## Common Filters Use an “AND” Relationship

When you set multiple filter conditions (time + key + model + status + provider), the results must satisfy all conditions at once. The more conditions you set, the fewer results you will see.

## Status Definitions

- “Success” = the model returned a complete answer as expected.
- Failure statuses are further categorized by reason (upstream service error, content blocked, timeout, etc.); each request is assigned to only one category.

## How to Reconcile Number Discrepancies

1. An inconsistent time window is the most common cause: select the same time range for this page and the analytics page.
2. After filters are applied, you are viewing a “subset”: clear all filters before checking the total.
3. Tenant identity affects the visible range: non-default tenants can only see requests belonging to their own tenant, and only for the most recent 3 days.
4. This page shows detailed records (for checking each request), while the analytics page shows aggregated results (deduplicated and aggregated): use this page for spot checks and the analytics page for totals.

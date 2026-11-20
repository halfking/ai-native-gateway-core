# Pricing Management Page Data Guide

This page maintains the “Cost Price Table,” which shows how much each model call costs upstream and serves as the basis for cost accounting and internal pricing.

## View Options

- **Tree view**: Browse by the “Provider → Model” hierarchy, which is useful for checking providers one by one.
- **Table view**: Displays all pricing in a single flattened table, which is useful for bulk checks and exports.

## Pricing Basis

- Prices are calculated per million Token: input and output prices are listed separately, and output is usually more expensive.
- Cached input is charged at a discounted price (10% for some providers); models with cached pricing have a separate cached-price row.
- Price changes only affect calls made **after the effective date**; historical bills are frozen at the prices that applied at the time and are not recalculated retroactively.

## How to Check When Numbers Do Not Match

1. The cost and provider bill differ by one decimal place: check the pricing unit (per million vs. per thousand Token), as the values differ by a factor of 1000.
2. The same model has two pricing rows: one is the standard price and the other is the cached price, or they represent connections through different providers—check the “Provider” column first.
3. The same-day report has not changed after a price update: reports are frozen by day, and the new price will appear in the next day’s snapshot.
4. Exchange rate difference: when USD costs are displayed in RMB, the platform’s configured exchange rate is used. Differences from the bank’s live rate are normal.

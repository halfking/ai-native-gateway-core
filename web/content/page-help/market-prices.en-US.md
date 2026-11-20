# Market Price Page Data Guide

This page manages “External Market Data” by collecting model market prices from marketplace platforms for price comparison and pricing reference. This page is for **read-only reconciliation + metadata management** and does not directly set prices.

## Two Sections

- **Platform Registry**: Records which marketplace platforms data is collected from. Platforms marked “Owned” or “Disabled” do not participate in market data syncing or market floor price calculations.
- **Sync Ledger**: Execution records for the 10 most recent collection runs (when they ran, how many records were collected, and how many were rejected).

## Reconciliation Equation

Every sync satisfies: **source count = successfully stored + rejected**. All three figures are visible in the ledger. To investigate rejected records that account for the difference, open their details to view the reason (common causes: currency/region mismatch and duplicate entries).

## Region and Currency Pairing Rules

Overseas platforms ↔ USD and domestic platforms ↔ RMB must be configured as pairs. Entries with mismatched pairs will be rejected during sync.

## How to Self-Check When the Numbers Do Not Match

1. The “Rejected” column in the ledger shows a red number: This is an expected part of the reconciliation process. Click “Details” for that sync to view the specific rejection reason.
2. The market data price differs from the market price you see: Market data is refreshed on a cycle and is not real-time. When comparing, confirm the “Sync Date.”
3. A platform has no data: Check whether it is disabled in the registry or whether the sync job has not yet run for that platform.

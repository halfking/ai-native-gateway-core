# Tenant List Page Data Description

A tenant is a “customer organization” on the platform: each tenant has its own keys, users, quota, and usage, all isolated from other tenants.

## Fields in Each Row

Tenant name, tenant code (a unique short code within the system), status, and quota usage.

## Quota System

- **Subscription quota**: priority deduction quota provided by a monthly package; it resets each month.
- **Wallet/cash balance**: the pay-as-you-go billing source used once the subscription quota is exhausted.
- Deduction order: subscription quota first, then wallet. These are listed separately on the page details and are not combined into a single “balance.”

## Meaning of Tenant Isolation

- Tenant A’s keys cannot access Tenant B’s configurations; statistics, logs, and quotas are all accounted for separately by tenant.
- The “Default Tenant” is the platform’s own operations space and is managed alongside paying customer tenants.

## How to Self-Check When Numbers Do Not Match

1. The quota “Remaining” does not match: first confirm the reporting time—quota is deducted in real time for API calls, while page figures have second-level caching.
2. Usage in the tenant details does not match the overview dashboard: the dashboard includes internal consumption from the Default Tenant; for a specific tenant’s account, the tenant details are the source of truth.
3. A newly created tenant immediately shows “yesterday’s” data: this is not possible—if you see this, the tenant code most likely reuses a historical code; first verify that the code is unique.

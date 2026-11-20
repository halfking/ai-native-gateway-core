# User List Page Data Notes

This page manages the accounts of people who can log in and use the platform.

## Fields in Each Row

Account name, role, status (Enabled/Disabled), tenant, and last active time.

## Roles and Visibility

- Roles determine which pages a person can view after logging in: Administrators can view all pages, while Tenant Administrators can only view their own tenant.
- “Disabled” accounts cannot log in, but historical usage and request records are still retained and remain accessible.

## “Last Active” Definition

- This is the more recent time between the person’s **last login and last API call**.
- Seeing “a long time ago” does not indicate a problem with the account; it may simply not have been used during that period.

## How to Check for Number Discrepancies

1. The number of users does not match the number of seats configured for the tenant: A seat is a licensing entitlement, while the list shows actual accounts; extra accounts may come from a trial or legacy setup.
2. Two accounts have the same name: Accounts are identified by unique IDs, so duplicate names do not affect billing or reporting; renaming an account does not merge historical data.
3. A recently disabled account still shows as active: Status changes take effect immediately, but “Last Active” remains at the time before the account was disabled, which is normal.

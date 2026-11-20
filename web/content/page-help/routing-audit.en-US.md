# Route Rewrite Audit Page

An audit trail of who changed the routing configuration and when.

## Scope Notes

Each record includes the before and after values and the operator; records are append-only and cannot be changed.

## How to Self-Check When Numbers Do Not Match

- 1.A change cannot be found: If the database was changed directly without using the page, no audit record is created; any change made through the page must have a record.
- The behavior did not change after the update: Confirm the range of traffic that the rule actually matched.

# Request Ledger Page

A ledger of all requests (a lower-level archival view than the Request Log page, including requests that did not run to completion).

## Definitions

Logged = a request is recorded as soon as it enters the gateway (including later failures or interruptions); the Request Log page focuses on readable details.

## How to Self-Check When Numbers Do Not Match

- 1. Ledger entry count > Request Log count: The ledger includes “incomplete” requests, while the Request Log only displays readable details.
- Two ledger entries for the same request: The primary request and each internal retry are recorded separately and aggregated by primary request ID.

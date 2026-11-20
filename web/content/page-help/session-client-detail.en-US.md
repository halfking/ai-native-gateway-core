# Client Analytics Details

A usage profile for each connected client (IDE/CLI/application).

## Calculation Notes

Clients are grouped by integration identifier. Multiple versions of the same client are aggregated.

## Self-Checks When the Numbers Do Not Match

- 1. Client usage does not match user usage: one user may use multiple clients, and one client may be shared by multiple users, so figures from the two perspectives may overlap but will not reconcile.
- Unknown client: All integrations without a standard identifier are grouped under "Unknown" and do not affect metering.

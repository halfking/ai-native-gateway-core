# Routing Overview Page Data Description

This page explains “why a request was assigned to a particular model” by breaking down the routing process.

## Core Concepts

- **Candidate** = a credential that qualifies to handle the request based on the filtering conditions.
- **Routable** = the candidates that are currently available (healthy, not disabled, and not in a cooldown period).
- **Decision Record** = the routing history for every actual request.

## Metrics Definitions

- “Routable Count” is assessed at the current moment: if the same candidate was available one minute ago but enters a cooldown one minute later, the count changes.
- Decision details list the candidate order by “tier”: candidates earlier in the list receive requests first; if one fails, the system falls back to the next candidate in order.
- “Fallback” = switching to the next candidate after the preferred candidate fails; each request has at most one final successful record.

## Tab Descriptions

- Live Decisions: Run a simulation to see “how a request would be routed right now.”
- Credential Monitoring / Probe Health: Availability details for upstream credentials.
- Automated Operations: The system automatically tunes parameters and analyzes tools.

## How to Self-Check When the Numbers Do Not Match

1. The simulation result differs from the actual record: A simulation is a “projection of the current state,” while the actual decision happened in the past; cooldown status and temporary disabling can cause the two to differ.
2. The number of decisions does not match the Request Records page: The Decisions page records only routing events; requests that never entered routing (such as those blocked by a preliminary check) do not appear here.
3. “Routable = 0,” but the request still succeeds: This means the system used a cross-provider fallback or backup channel; you can see the fallback chain in the decision details.
4. Time difference: Cooldown expiration and credential recovery happen on a minute-by-minute basis, so results can differ when the two queries are several minutes apart.

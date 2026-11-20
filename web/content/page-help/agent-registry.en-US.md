# Agent Registry Page

A list of automated agents (scripts/bots) registered in the backend, including their health status and ownership.

## Definitions

Healthy = recent heartbeat and successful invocation; Offline = no messages beyond the heartbeat interval.

## How to Check When Numbers Do Not Match

- 1.The agent count is unrelated to the user count: agents are service accounts and do not occupy individual user seats.
- Two records for the same agent: rebuilding an instance changes its ID, and historical usage is attributed by ID.
- Healthy but the invocation failed: health status considers only the heartbeat and login status; check request logs for the specific failure.

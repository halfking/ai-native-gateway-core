# Archived Deployment Material

Files in this directory are historical records only. They are not operational instructions.

The current database topology is:

- RDS production, with approved DBA-only access
- 252 test, as the permitted source for local refreshes
- Local Docker `llm-gateway-pg`

Use `docs/06-deployment/01-environments/deployment/DATABASE-ENVIRONMENT-SEPARATION.md` and `docs/06-deployment/02-database/local-pg-sync-from-252.md` for current procedures.

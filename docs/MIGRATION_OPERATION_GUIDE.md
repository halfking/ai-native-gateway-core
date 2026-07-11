# Migration Operation Guide

## Ledger-based execution

`scripts/run-migrations-strict.sh` records every successfully applied migration in
`public.repository_schema_migrations` using its scope, version identifier, filename, and SHA-256
checksum. A scope and filename identify a migration, so existing historical suffixes
and same-version fix files are preserved. It processes startup and domain files by numeric version prefix (then full
version identifier), skips only exact checksum matches, and stops immediately on a SQL or ledger-write failure. A failed
migration is not recorded.

- **New empty database:** run `DATABASE_URL=... scripts/init-minimal-db.sh`, or run
  `scripts/run-migrations-strict.sh --bootstrap`. Bootstrap is rejected if the
  `public` schema already contains application relations.
- **Existing database without this ledger:** first confirm its known schema baseline,
  then run `DATABASE_URL=... scripts/run-migrations-strict.sh --baseline-through 377`.
  This records historical versions without replaying them, then applies later
  migrations. Do not baseline an unknown or partially applied database.
- **Existing repository ledger:** run `DATABASE_URL=... scripts/run-migrations-strict.sh`.
  Changed content for an applied migration is rejected rather than silently run.

## 382/383 compatibility and RLS

Migrations `382_session_module_executions.sql` and
`383_dashboard_access_events.sql` create the operational hot/archive tables. Their
content must not be changed after it has been applied; issue a new forward migration
for any correction. Migration `384_hot_table_independence_fix.sql` is an additive
follow-up for the related hot-table schema.

`session_module_executions[_hot]` and `dashboard_access_events[_hot]` deliberately
do not enforce a policy based on `app.current_tenant`. Module execution and telemetry
perform asynchronous writes through pooled connections, and the current Go paths do
not guarantee a transaction-scoped tenant GUC. Enabling such RLS would reject valid
writes. Tenant isolation for these operational tables must remain in trusted service
credentials/query paths until every writer is changed to set and validate the GUC.
The verification script intentionally inserts without `app.current_tenant` to cover
that compatibility requirement.

## Rollback

`382_session_module_executions.down.sql` and
`383_dashboard_access_events.down.sql` intentionally stop with an error. They have
no automatic rollback because table drops or `CASCADE` could permanently delete
operational/audit data or unrelated dependencies. Use a reviewed database restore or
a forward corrective migration instead.

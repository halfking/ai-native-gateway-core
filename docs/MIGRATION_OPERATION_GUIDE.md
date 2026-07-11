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

## 360/361 compatibility and RLS

Migrations `360_session_module_executions.sql` and
`361_dashboard_access_events.sql` are published history and must never be deleted,
renamed, or rewritten. Migrations 378 and 379 are additive compatibility migrations
for databases where 360/361 already exist; they do not recreate those tables,
functions, views, partitions, or cron jobs.

`session_module_executions[_hot]` and `dashboard_access_events[_hot]` deliberately
do not enforce a policy based on `app.current_tenant`. Module execution and telemetry
perform asynchronous writes through pooled connections, and the current Go paths do
not guarantee a transaction-scoped tenant GUC. Enabling such RLS would reject valid
writes. Tenant isolation for these operational tables must remain in trusted service
credentials/query paths until every writer is changed to set and validate the GUC.
The verification script intentionally inserts without `app.current_tenant` to cover
that compatibility requirement.

## Rollback

`378_session_module_executions.down.sql` and
`379_dashboard_access_events.down.sql` intentionally stop with an error. They have
no automatic rollback because restoring the unsafe RLS configuration requires a
reviewed application release, while table drops or `CASCADE` could permanently delete
operational/audit data or unrelated dependencies. Use a reviewed database restore or
a forward corrective migration instead.

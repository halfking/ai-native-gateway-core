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

## Deployment checksum ledger

`deploy-seamless.sh` uses `schema_migrations.version` as the legacy applied-version source of truth and maintains a separate `llm_gateway_migration_checksums` table. Before a switch it validates every applied startup migration at or above `DB_LEDGER_RECONCILE_FROM` (default `412`) against the local filename and SHA-256. A missing checksum row, or an existing filename or checksum mismatch, fails closed; ordinary deployment never backfills the checksum ledger.

### Historical 488/489 identity mismatch on 252

252 recorded the pre-renumber probe queue migrations as 488 and 489. The current repository reserves 488 for `488_request_logs_hot_add_model.sql` and contains post-renumber probe files at 489/490. The historical 488/489 source bytes could not be recovered from the repository, related worktrees, or accessible 252 artifacts. Do not rename a current file, update a remote ledger checksum, or waive the mismatch based on a semantic guess. Recover a source file only when its filename and SHA-256 exactly match the remote ledger; otherwise preserve the history and ship any missing schema as a new forward migration after review.

Startup migration numbers must be unique within one pending deployment. Duplicate numeric versions already present in historical local files are tolerated only when the remote checksum ledger identifies the applied filename; if the ledger is missing for a repeated version, deployment stops and requires manual reconciliation. Duplicate pending files are always rejected before SSH, SQL, or service operations because the legacy ledger cannot distinguish two files with the same version. Files explicitly marked `SUPERSEDED` or `DEPRECATED` are excluded from these checks.


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

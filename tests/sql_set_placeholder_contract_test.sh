#!/usr/bin/env bash
# Exercise the real gate function against legal UPDATEs and invalid SETs.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/sql/migrations/startup"
eval "$(sed -n '/^check_sql_set_local() {$/,/^}$/p' "$ROOT/scripts/pre-commit-check.sh")"
cd "$fixture"

expect() {
  local want="$1" input="$2" actual=0
  printf '%s\n' "$input" > sql/migrations/startup/fixture.go
  check_sql_set_local > "$fixture/result" 2>&1 || actual=$?
  if [[ "$actual" != "$want" ]]; then
    printf 'FAIL expected %s, got %s: %s\n' "$want" "$actual" "$input" >&2
    cat "$fixture/result" >&2
    exit 1
  fi
}

expect 0 'UPDATE events SET data = $1 WHERE id = $2;'
expect 0 '  "UPDATE events SET data = $1::jsonb WHERE id = $2",'
expect 0 '-- SET LOCAL app.tenant = $1;'
expect 0 'SET LOCAL app.tenant = '\''fixed'\'';'
expect 1 'SET LOCAL app.tenant = $1;'
expect 1 '  SET app.tenant = $12;'
expect 1 'db.ExecContext(ctx, "SET LOCAL app.tenant = $1", tenant)'
expect 1 'db.Exec(`SET app.tenant = $1`, tenant)'
expect 1 "EXECUTE 'SET LOCAL app.tenant = \$1';"
expect 1 'UPDATE events SET data = $1; SET LOCAL app.tenant = $2;'
expect 1 'set session app.tenant = $1;'
expect 1 '/* migration context */ SET LOCAL app.tenant = $1;'
expect 1 '/* migration context */ SET SESSION app.tenant = $1;'
expect 1 '/* migration context */ SET app.tenant = $1;'
expect 1 'BEGIN SET LOCAL app.tenant = $1; END;'
expect 1 'IF condition THEN SET LOCAL app.tenant = $1; END IF;'
printf 'PASS SQL SET placeholder contracts (16 cases)\n'

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
for side in left right; do
  printf 'PG_HOST=%s\nPG_PORT=5432\nPG_USER=test\nPG_PASS=test\nPG_DB=test\n' "$side" >"$tmp/$side.env"
done
cat >"$tmp/bin/psql" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
host=''
while (($#)); do case "$1" in -h) host="$2"; shift 2;; *) shift;; esac; done
case "${MOCK_CASE:-same}:$host" in
  same:*) printf '%s\n' 'TABLE|accounts||' 'COLUMN|accounts|id|integer|nullable=false|default=' 'INDEX|accounts|accounts_pkey|CREATE UNIQUE INDEX accounts_pkey ON public.accounts USING btree (id)' 'CONSTRAINT|accounts|accounts_pkey|PRIMARY KEY (id)' ;;
  changed:left) printf '%s\n' 'TABLE|accounts||' 'COLUMN|accounts|id|integer|nullable=false|default=' ;;
  changed:right) printf '%s\n' 'TABLE|accounts||' 'COLUMN|accounts|id|bigint|nullable=true|default=0' 'TABLE|right_only||' ;;
  *) exit 2 ;;
esac
MOCK
chmod +x "$tmp/bin/psql"
PATH="$tmp/bin:$PATH" MOCK_CASE=same "$SCRIPT_DIR/pg-schema-diff.sh" --source "$tmp/left.env" --target "$tmp/right.env" >/dev/null
set +e
PATH="$tmp/bin:$PATH" MOCK_CASE=changed "$SCRIPT_DIR/pg-schema-diff.sh" --source "$tmp/left.env" --target "$tmp/right.env" >"$tmp/diff"
status=$?
set -e
[[ $status -eq 1 ]] || { printf 'expected drift exit 1, got %s\n' "$status" >&2; exit 1; }
grep -q 'COLUMN|accounts|id|integer' "$tmp/diff"
grep -q 'COLUMN|accounts|id|bigint' "$tmp/diff"
grep -q 'TABLE|right_only' "$tmp/diff"
printf 'pg-schema-diff self-test passed.\n'

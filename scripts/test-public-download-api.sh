#!/usr/bin/env bash
# test-public-download-api.sh — 公开下载 API 冒烟测试
#
# Usage: bash scripts/test-public-download-api.sh [https://llmgo.kxpms.cn]
set -euo pipefail

BASE="${1:-https://llmgo.kxpms.cn}"
BASE="${BASE%/}"
PASS=0
FAIL=0

check() {
  local name="$1" code="$2" expect="$3"
  if [[ "$code" == "$expect" ]]; then
    echo "  ✓ $name ($code)"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $name (got $code, want $expect)" >&2
    FAIL=$((FAIL + 1))
  fi
}

echo "=== Public Download API smoke: $BASE ==="

code=$(curl -sS -o /tmp/dl_catalog.json -w "%{http_code}" "$BASE/api/downloads/catalog" --max-time 15 || echo "000")
check "GET /api/downloads/catalog" "$code" "200"

if [[ -f /tmp/dl_catalog.json ]]; then
  python3 - <<'PY'
import json, sys
d=json.load(open("/tmp/dl_catalog.json"))
assert d.get("version"), "missing version"
items=d.get("items") or []
versions=d.get("versions") or []
assert items or versions, "no items/versions"
if versions:
    g=versions[0]
    assert g.get("version"), "version group missing version"
    print(f"  catalog: {d['version']} groups={len(versions)} first={g['version']} items={len(g.get('items',[]))}")
else:
    print(f"  catalog: {d['version']} items={len(items)}")
repo=d.get("git_repo_url","")
if "github.com/halfking" in repo:
    print("  git_repo_url: GitHub OK")
elif repo:
    print(f"  git_repo_url: {repo}")
PY
  PASS=$((PASS + 1))
fi

# ticket for first linux amd64 if present
ticket_body=$(python3 - <<'PY'
import json
d=json.load(open("/tmp/dl_catalog.json"))
ver=d["version"]
items=d.get("items") or []
if d.get("versions"):
    items=d["versions"][0].get("items") or items
for it in items:
    if it.get("platform")=="linux" and it.get("arch")=="amd64":
        print(json.dumps({"version": ver, "platform": "linux", "arch": "amd64"}))
        break
PY
)
if [[ -n "$ticket_body" ]]; then
  code=$(curl -sS -o /tmp/dl_ticket.json -w "%{http_code}" \
    -X POST -H "Content-Type: application/json" -d "$ticket_body" \
    "$BASE/api/downloads/ticket" --max-time 15 || echo "000")
  check "POST /api/downloads/ticket" "$code" "200"
  if [[ -f /tmp/dl_ticket.json ]]; then
    python3 -c "import json;d=json.load(open('/tmp/dl_ticket.json'));assert d.get('url')"
    PASS=$((PASS + 1))
    echo "  ticket url ok"
  fi
else
  echo "  - skip ticket (no linux/amd64 in catalog)"
fi

code=$(curl -sS -o /dev/null -w "%{http_code}" "$BASE/healthz" --max-time 10 || echo "000")
check "GET /healthz" "$code" "200"

echo ""
echo "=== Result: pass=$PASS fail=$FAIL ==="
[[ "$FAIL" -eq 0 ]]

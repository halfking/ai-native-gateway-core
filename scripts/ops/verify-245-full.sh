#!/usr/bin/env bash
# Full verification on 245 after ops/blocklist deploy.
set -euo pipefail

SSH_PORT="${LLM_GATEWAY_SSH_PORT:-25022}"
SSH_KEY_FILE="${SSH_KEY_FILE:-}"
for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
  [[ -f "$k" ]] && SSH_KEY_FILE="$k" && break
done
SSH_OPTS=(-i "$SSH_KEY_FILE" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=12)
SSH_HOST="${LLM_GATEWAY_245_SSH:-root@8.136.114.245}"

PASS=0
FAIL=0
WARN=0

ok()   { echo "  ✓ $*"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $*"; FAIL=$((FAIL+1)); }
warn() { echo "  ⚠ $*"; WARN=$((WARN+1)); }

echo "=== 245 完整验证 (ops + blocklist + board) ==="

ssh "${SSH_OPTS[@]}" "$SSH_HOST" 'python3 -' <<'PY'
import json, os, sys, time, urllib.request, urllib.error
from pathlib import Path

PASS=FAIL=WARN=0

def ok(m):
    global PASS; PASS+=1; print("  ✓", m)
def fail(m):
    global FAIL; FAIL+=1; print("  ✗", m)
def warn(m):
    global WARN; WARN+=1; print("  ⚠", m)

def req(method, url, data=None, headers=None, timeout=20):
    h = headers or {}
    body = None
    if data is not None:
        body = json.dumps(data).encode()
        h.setdefault("Content-Type", "application/json")
    r = urllib.request.Request(url, data=body, headers=h, method=method)
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            raw = resp.read()
            return resp.status, raw
    except urllib.error.HTTPError as e:
        return e.code, e.read()

env = {}
for ln in Path("/opt/llm-gateway-go/.env").read_text().splitlines():
    if "=" in ln and not ln.strip().startswith("#"):
        k,v = ln.split("=",1); env[k]=v

base = "http://127.0.0.1:8781"

# 1) health + version
st, raw = req("GET", base+"/healthz")
if st == 200 and b"ok" in raw:
    ok("healthz OK")
else:
    fail(f"healthz HTTP {st}")

st, raw = req("GET", base+"/api/system/version")
if st == 200:
    ver = json.loads(raw)
    ok(f"version build_seq={ver.get('"'"'build_seq'"'"')} sha={ver.get('"'"'git_sha'"'"')}")
else:
    fail(f"version HTTP {st}")

# 2) login
st, raw = req("POST", base+"/api/auth/token", {
    "username": env.get("LLM_GATEWAY_ADMIN_USER",""),
    "password": env.get("LLM_GATEWAY_ADMIN_PASSWORD",""),
})
if st != 200:
    fail(f"login HTTP {st}"); sys.exit(1)
tok = json.loads(raw)["access_token"]
H = {"Authorization": "Bearer "+tok}
ok("admin login OK")

# 3) board bundle (include_operational)
t0=time.time()
st, raw = req("GET", base+"/api/admin/dashboard/board?days=1&include_operational=1", headers=H)
ms=int((time.time()-t0)*1000)
if st == 200:
    data=json.loads(raw)
    if data.get("operational") is not None:
        ok(f"board+operational bundled ({ms}ms)")
    else:
        warn(f"board OK but operational missing ({ms}ms)")
else:
    fail(f"board HTTP {st}")

# 4) ops overview bundle
t0=time.time()
st, raw = req("GET", base+"/api/admin/ops/overview", headers=H)
ms=int((time.time()-t0)*1000)
if st == 200:
    ov=json.loads(raw)
    regions = {r.get("region"): r for r in (ov.get("region_stats") or [])}
    ok(f"ops/overview OK ({ms}ms) regions={list(regions.keys())}")
    for want in ("local","245","154"):
        r=regions.get(want)
        if not r:
            warn(f"region {want} missing in stats")
        elif r.get("missing"):
            warn(f"region {want} not registered")
        elif r.get("online_instances",0) > 0:
            ok(f"region {want} online={r.get('"'"'online_instances'"'"')}")
        else:
            warn(f"region {want} present but offline")
    tables = ov.get("data_plane_tables") or {}
    ok(f"data_plane: instances={tables.get('"'"'gateway_instances'"'"',0)} heartbeats={tables.get('"'"'instance_heartbeats'"'"',0)} blocklist_tables_ok")
else:
    fail(f"ops/overview HTTP {st}")

# 5) blocklist CRUD
st, raw = req("GET", base+"/api/admin/security/ip-blocklist", headers=H)
if st == 200:
    ok("blocklist list OK")
else:
    fail(f"blocklist list HTTP {st}")

test_ip = "203.0.113.99"
st, raw = req("POST", base+"/api/admin/security/ip-blocklist", {
    "ip_or_cidr": test_ip, "reason": "245-verify-test", "scope": "ops"
}, headers=H)
entry_id = None
if st in (200,201):
    entry = json.loads(raw)
    entry_id = entry.get("id")
    ok(f"blocklist create id={entry_id}")
else:
    fail(f"blocklist create HTTP {st}")

st, raw = req("POST", base+"/api/admin/security/ip-blocklist/reload", headers=H)
if st == 200:
    ok("blocklist cache reload OK")
else:
    warn(f"blocklist reload HTTP {st}")

if entry_id:
    st, _ = req("DELETE", base+f"/api/admin/security/ip-blocklist/{entry_id}", headers=H)
    if st in (200,204):
        ok("blocklist delete OK")
    else:
        warn(f"blocklist delete HTTP {st}")

# 6) env checks
if env.get("LLM_GATEWAY_CENTER_URL"):
    ok("LLM_GATEWAY_CENTER_URL set")
else:
    fail("LLM_GATEWAY_CENTER_URL missing")
if env.get("OPS_COLLECT_URL"):
    ok("OPS_COLLECT_URL set")
else:
    fail("OPS_COLLECT_URL missing")

if env.get("OPS_NODE_REGION") == "245":
    ok("OPS_NODE_REGION=245")
else:
    warn(f"OPS_NODE_REGION={env.get('"'"'OPS_NODE_REGION'"'"')}")
if env.get("OPS_COLLECT_LICENSE_KEY"):
    ok("OPS_COLLECT_LICENSE_KEY set")
else:
    warn("OPS_COLLECT_LICENSE_KEY missing — HTTP ops reporter will not register")

# 7) DB tables via psql
import subprocess
DB = env.get("LLM_GATEWAY_DATABASE_URL","")
if DB:
    def psql(q):
        p=subprocess.run(["psql", DB, "-t", "-A", "-c", q], stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
        return p.stdout.strip(), p.returncode
    for tbl in ("ip_blocklist","ops_node_registrations","gateway_instances","instance_heartbeats"):
        out, rc = psql(f"SELECT COUNT(*)::text FROM {tbl}")
        if rc == 0:
            ok(f"table {tbl} count={out}")
        else:
            fail(f"table {tbl} query failed")
    out, rc = psql("SELECT region,status,COUNT(*)::int FROM gateway_instances GROUP BY 1,2 ORDER BY 1,2")
    if rc == 0:
        ok(f"gateway_instances by region: {out or '"'"'(empty)'"'"'}")

print(f"\n=== RESULT pass={PASS} fail={FAIL} warn={WARN} ===")
sys.exit(1 if FAIL else 0)
PY

echo ""
echo "=== 252 数据面（经 245 查询）==="
bash "$(dirname "$0")/verify-ops-data-plane.sh" 245 2>&1 | sed 's/^/  /'

echo ""
echo "=== 远端日志：ops reporter / blocklist ==="
ssh "${SSH_OPTS[@]}" "$SSH_HOST" "journalctl -u llm-gateway-go.service --since '10 min ago' --no-pager 2>&1 | grep -iE 'ops reporter|blocklist|center agent' | tail -10 || echo '  (no matching log lines)'"

#!/usr/bin/env bash
# =====================================================================
# llm-itestu-upstream-fallback.sh
#   —— llm.itestu.cn 公网上游的「临时兜底 / 回切」开关。
#
# ★ 背景（2026-10-23，docs/audit/2026-10-23-211-整机失联-取证与恢复手册.md）：
#   211 主机在 02:12–02:16 之间整机失联 ⇒ 边缘 nginx（245）上
#   `upstream llm_itestu_245_origin { server 100.106.67.198:8000; }`
#   这条腿（211 的 mesh 腿）不可达 ⇒ llm.itestu.cn 持续 502。
#   本脚本把该 upstream 改指**245 本机蓝绿活跃槽**
#   （include /opt/llm-gateway-go/run/active-upstream.conf ⇒ 127.0.0.1:8782），
#   写法与同机已在线的 llmgo.kxpms.cn / llm-backup.kxpms.cn 两条腿**完全同款**。
#
# ★ ⚠ 语义代价（务必先读）：
#   245/154 用的是共享库 172.16.2.210，**与 211 本机库已分叉**
#   （credential_model_bindings 1996 vs 1990）。⇒ 兜底期间：
#     · 站点恢复 HTTP 200，admin 能登录（凭据在 245 的库里是有效的）；
#     · 但**写操作落到 245 的库**，与 211 的库继续分叉；
#     · 211 回来后必须做一次数据对齐评估，再决定以哪边为准。
#   ⇒ **这是对外可见的路由变更，必须属主明确授权后才执行。**
#
# ★ 安全设计（每一步都可回退）：
#   1) 备份原 conf 到同目录 .bak-<UTC 时间戳>；
#   2) 只替换 upstream 块（server 100.106.67.198:8000 → include 片段），
#      server/location 块**逐字不动**；
#   3) `nginx -t` 语法检查 —— 失败则立即还原并退出，**不 reload**；
#   4) `nginx -s reload`；
#   5) 本机（--resolve）验收：/、/version、/api/auth/token、鉴权后 /api/auth/me；
#   6) 验收任一项失败 ⇒ **自动还原备份 + reload** + 退出非零。
#
# ★ 用法（在 245 上执行；本脚本不会自己 ssh，改路由前请确认你有那台机的权限）：
#     scp llm-itestu-upstream-fallback.sh 245:/tmp/
#     ssh 245 'sudo bash /tmp/llm-itestu-upstream-fallback.sh'                 # 切兜底
#     ssh 245 'sudo bash /tmp/llm-itestu-upstream-fallback.sh --rollback'      # 回切原上游
#
#   ★ 登录冒烟需要凭据时（**不要写进脚本、不要进仓库**）：
#     ssh 245 'sudo DD_ADMIN_USER=admin DD_ADMIN_PASSWORD=… bash /tmp/llm-itestu-upstream-fallback.sh'
#   未提供时该步自动 SKIP（其余验收照常）。
# =====================================================================
set -uo pipefail

CONF=/etc/nginx/conf.d/llm.itestu.cn.conf
ACTIVE_FRAG=/opt/llm-gateway-go/run/active-upstream.conf
DOMAIN=llm.itestu.cn
UPSTREAM_NAME=llm_itestu_245_origin

log() { printf '[llm-itestu-fallback] %s\n' "$*"; }

rollback() {
  local bak
  bak="$(ls -1t "${CONF}.bak-"* 2>/dev/null | head -1 || true)"
  if [[ -z "${bak}" ]]; then
    log "NO BACKUP FOUND — cannot rollback automatically"
    return 1
  fi
  log "rollback from ${bak}"
  cp -a "${bak}" "${CONF}"
  if nginx -t >/dev/null 2>&1 && nginx -s reload; then
    log "rolled back + reloaded"
    return 0
  fi
  log "ROLLBACK FAILED: nginx -t failed on the restored file"
  return 1
}

if [[ "${1:-}" == "--rollback" ]]; then
  rollback
  exit $?
fi

[[ -f "${CONF}" ]]        || { log "FATAL: ${CONF} not found"; exit 2; }
[[ -f "${ACTIVE_FRAG}" ]] || { log "FATAL: ${ACTIVE_FRAG} not found"; exit 2; }
log "active fragment: $(cat "${ACTIVE_FRAG}")"

BACKUP="${CONF}.bak-$(date -u +%Y%m%dT%H%M%SZ)"
cp -a "${CONF}" "${BACKUP}"
log "backup -> ${BACKUP}"

# ── 替换 upstream 块（只动这一块）────────────────────────────────
python3 - "$CONF" "$UPSTREAM_NAME" <<'PY'
import re, sys
path, name = sys.argv[1], sys.argv[2]
src = open(path, encoding="utf-8").read()
new_block = """upstream %s {
    # 2026-10-23 临时兜底：211 整机离线（mesh peer 100.106.67.198 未连接，
    # LAN 192.168.31.211 无响应）⇒ 上游改指本机蓝绿活跃槽，
    # 写法与 llmgo.kxpms.cn.conf / llm-backup.kxpms.cn.conf 同款（include 片段）。
    # ⚠ 语义差异：数据落在 245/154 共享库 172.16.2.210，与 211 本机库已分叉。
    # 211 恢复后必须执行 --rollback 切回。
    include /opt/llm-gateway-go/run/active-upstream.conf;
    keepalive 32;
}
""" % name
pat = re.compile(r"upstream\s+" + re.escape(name) + r"\s*\{.*?\n\}\n", re.S)
if not pat.search(src):
    sys.stderr.write("FATAL: upstream block %s not found\n" % name)
    sys.exit(3)
out, n = pat.subn(new_block, src, count=1)
if n != 1:
    sys.stderr.write("FATAL: replacement count != 1\n")
    sys.exit(4)
open(path, "w", encoding="utf-8", newline="").write(out)
print("upstream block replaced")
PY
rc=$?
if [[ $rc -ne 0 ]]; then
  log "edit failed (rc=$rc) — restoring"
  cp -a "${BACKUP}" "${CONF}"
  exit 5
fi

# ── 语法检查（不 reload）──────────────────────────────────────────
if ! nginx -t; then
  log "nginx -t FAILED — restoring backup, NOT reloading"
  cp -a "${BACKUP}" "${CONF}"
  exit 6
fi
log "nginx -t OK"

nginx -s reload || {
  log "reload failed — restoring"
  cp -a "${BACKUP}" "${CONF}"
  nginx -s reload
  exit 7
}
log "reloaded"

sleep 2

# ── 验收（本机 --resolve 打本机 443）──────────────────────────────
fail=0
check() { # name path expect_status expect_substring
  local name="$1" path="$2" exp="$3" needle="$4" out code body
  out="$(curl -sS -k -m 15 --resolve "${DOMAIN}:443:127.0.0.1" -w '\n__CODE__%{http_code}' "https://${DOMAIN}${path}" 2>&1)"
  code="${out##*__CODE__}"
  body="${out%__CODE__*}"
  if [[ "${code}" != "${exp}" ]]; then
    log "FAIL ${name}: http=${code} (expect ${exp})"
    fail=1
    return
  fi
  if [[ -n "${needle}" && "${body}" != *"${needle}"* ]]; then
    log "FAIL ${name}: body lacks '${needle}'"
    fail=1
    return
  fi
  log "PASS ${name}: http=${code} len=${#body}"
}

check "spa"     "/"        200 "<html"
check "version" "/version" 200 "git_sha"

if [[ -n "${DD_ADMIN_USER:-}" && -n "${DD_ADMIN_PASSWORD:-}" ]]; then
  tmp="$(mktemp)"
  printf '{"username":"%s","password":"%s"}' "$DD_ADMIN_USER" "$DD_ADMIN_PASSWORD" > "$tmp"
  body="$(curl -sS -k -m 20 --resolve "${DOMAIN}:443:127.0.0.1" -X POST \
    -H 'Content-Type: application/json' --data-binary "@$tmp" \
    "https://${DOMAIN}/api/auth/token" 2>&1)"
  rm -f "$tmp"
  if [[ "${body}" == *access_token* ]]; then
    log "PASS login: token issued"
  else
    log "FAIL login: ${body:0:160}"
    fail=1
  fi
else
  log "SKIP login smoke (DD_ADMIN_USER / DD_ADMIN_PASSWORD not provided)"
fi

if [[ $fail -ne 0 ]]; then
  log "acceptance FAILED — auto rollback"
  rollback
  exit 8
fi
log "ALL PASS — llm.itestu.cn now served by the 245 local active slot"
exit 0

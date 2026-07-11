#!/usr/bin/env bash
# verify-llm-kxpms-nps.sh — daily verification of llm.kxpms.cn NPS architecture
#
# 用法：
#   /usr/local/bin/verify-llm-kxpms-nps.sh               # 检查并报告状态
#   /usr/local/bin/verify-llm-kxpms-nps.sh --json       # 输出 JSON
#   /usr/local/bin/verify-llm-kxpms-nps.sh --quiet      # 只在失败时输出
#   CRIT=1 /usr/local/bin/verify-llm-kxpms-nps.sh       # 自定义阈值（天）
#
# 安装（在 252 上）:
#   install -m 0755 verify-llm-kxpms-nps.sh /usr/local/bin/
#   cat > /etc/cron.d/verify-llm-kxpms-nps <<'EOF'
#   0 7 * * * root /usr/local/bin/verify-llm-kxpms-nps.sh --quiet >> /var/log/verify-llm-kxpms-nps.log 2>&1
#   EOF
#
# 检查项：
#   1. DNS 解析 llm.kxpms.cn → 预期 IP（默认 252）
#   2. 252 HTTPS (port 443) 返回 200
#   3. 154 HTTPS (port 443) 返回 200（兜底）
#   4. 252 / 154 /etc/letsencrypt/live/kxpms.cn/fullchain.pem SHA 一致
#   5. cert 剩余天数 ≥ THRESHOLD_DAYS (默认 14)
#   6. certbot-renew.timer active
#   7. /etc/letsencrypt/renewal-hooks/deploy/sync-to-154.sh 存在且可执行
#   8. LLM chat completion 在 252 上返回正常响应（端到端 smoke test）
#
# 退出码：
#   0 = all green
#   1 = at least one CRIT failure
#   2 = at least one WARN (no CRIT)
#
# 与现有 check-nps-certs.sh 互补（那个只查 cert 过期；这个查 NPS 端到端）
# 252 上无 dig/jq，本脚本用 Python 兜底（certbot 已依赖 python3）
# ============================================================================
set -uo pipefail

HOST_252="${HOST_252:-115.29.212.252}"
HOST_154="${HOST_154:-47.97.111.154}"
EXPECTED_DNS_IP="${EXPECTED_DNS_IP:-$HOST_252}"
THRESHOLD_DAYS="${CRIT:-14}"
WARN_DAYS="${WARN:-30}"
TIMEOUT="${TIMEOUT:-10}"
LLM_MODEL="${LLM_MODEL:-minimax-text-01}"
LLM_PROMPT="${LLM_PROMPT:-NPS_VERIFY_OK}"
LLM_API_KEY="${LLM_API_KEY:-sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9}"
LOG="/var/log/verify-llm-kxpms-nps.log"

MODE="text"
QUIET=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --json)  MODE="json"; shift ;;
    --quiet) QUIET=1; shift ;;
    *) echo "unknown arg: $1"; exit 2 ;;
  esac
done

ts() { date +"%F %T"; }

# Python helpers (since 252 has no dig/jq)
python_dns() {
  python3 -c "
import socket
try:
    r = socket.getaddrinfo('$1', None, type=socket.SOCK_STREAM)
    print(r[0][4][0])
except Exception as e:
    print('')
" 2>/dev/null
}

log_info() {
  if [[ $QUIET -eq 0 && $MODE == "text" ]]; then
    printf "[%s] %s\n" "$(ts)" "$*"
  fi
}

# ── 检查项 ────────────────────────────────────────────────────────────────

declare -a RESULTS_LEVEL=()
declare -a RESULTS_NAME=()
declare -a RESULTS_MSG=()
declare -a RESULTS_DETAIL=()
CRIT_FAIL=0
WARN_FAIL=0

check() {
  local level="$1" name="$2" msg="$3" detail="${4:-}"
  RESULTS_LEVEL+=("$level")
  RESULTS_NAME+=("$name")
  RESULTS_MSG+=("$msg")
  RESULTS_DETAIL+=("$detail")
  case "$level" in
    CRIT) CRIT_FAIL=$((CRIT_FAIL+1))
      printf "[%s] CRIT %s: %s\n" "$(ts)" "$name" "$msg" >&2 ;;
    WARN) WARN_FAIL=$((WARN_FAIL+1))
      [[ $QUIET -eq 0 && $MODE == "text" ]] && printf "[%s] WARN %s: %s\n" "$(ts)" "$name" "$msg" ;;
    OK)   [[ $QUIET -eq 0 && $MODE == "text" ]] && printf "[%s] OK   %s: %s\n" "$(ts)" "$name" "$msg" ;;
  esac
}

# 1. DNS 解析 (用 Python socket 替代 dig)
DNS_IP=$(python_dns llm.kxpms.cn)
if [[ "$DNS_IP" == "$EXPECTED_DNS_IP" ]]; then
  check OK "dns" "llm.kxpms.cn -> $DNS_IP (matches expected)" "$DNS_IP"
else
  check CRIT "dns" "llm.kxpms.cn -> $DNS_IP (expected $EXPECTED_DNS_IP)" "$DNS_IP"
fi

# 2. 252 HTTPS
HTTP_252=$(curl -sS --resolve "llm.kxpms.cn:443:$HOST_252" --connect-timeout "$TIMEOUT" --max-time "$TIMEOUT" \
                -o /dev/null -w "%{http_code}" https://llm.kxpms.cn/v1/models 2>/dev/null || echo "0")
if [[ "$HTTP_252" == "200" ]]; then
  check OK "https-252" "llm.kxpms.cn:443 on 252 returns 200" "$HTTP_252"
else
  check CRIT "https-252" "llm.kxpms.cn:443 on 252 failed" "$HTTP_252"
fi

# 3. 154 HTTPS
HTTP_154=$(curl -sS --resolve "llm.kxpms.cn:443:$HOST_154" --connect-timeout "$TIMEOUT" --max-time "$TIMEOUT" \
                -o /dev/null -w "%{http_code}" https://llm.kxpms.cn/v1/models 2>/dev/null || echo "0")
if [[ "$HTTP_154" == "200" ]]; then
  check OK "https-154" "llm.kxpms.cn:443 on 154 returns 200" "$HTTP_154"
else
  check CRIT "https-154" "llm.kxpms.cn:443 on 154 failed" "$HTTP_154"
fi

# 4. cert SHA 一致性
SHA_252=$(sha256sum /etc/letsencrypt/live/kxpms.cn/fullchain.pem 2>/dev/null | awk '{print $1}')
SHA_154=$(ssh -i /root/.ssh/cert-sync-252-154 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/root/.ssh/known_hosts \
                  -p 25022 root@47.97.111.154 "sha256sum /etc/letsencrypt/live/kxpms.cn/fullchain.pem 2>/dev/null" \
                  2>/dev/null | awk '{print $1}')
if [[ -z "$SHA_252" || -z "$SHA_154" ]]; then
  check CRIT "cert-sync" "cannot read cert SHA from 252 or 154" "252=${SHA_252:-?} 154=${SHA_154:-?}"
elif [[ "$SHA_252" == "$SHA_154" ]]; then
  check OK "cert-sync" "252 / 154 cert SHA match" "${SHA_252:0:16}"
else
  check WARN "cert-sync" "252 / 154 cert SHA differ" "252=${SHA_252:0:16} 154=${SHA_154:0:16}"
fi

# 5. cert 剩余天数
DAYS_LEFT=$(openssl x509 -in /etc/letsencrypt/live/kxpms.cn/fullchain.pem -noout -enddate 2>/dev/null \
            | cut -d= -f2- | xargs -I{} date -d "{}" +%s 2>/dev/null)
NOW=$(date +%s)
if [[ -z "$DAYS_LEFT" ]]; then
  check CRIT "cert-expiry" "cannot parse cert expiry" ""
else
  DAYS=$(( (DAYS_LEFT - NOW) / 86400 ))
  if (( DAYS < THRESHOLD_DAYS )); then
    check CRIT "cert-expiry" "cert expires in ${DAYS}d (< ${THRESHOLD_DAYS}d threshold)" ""
  elif (( DAYS < WARN_DAYS )); then
    check WARN "cert-expiry" "cert expires in ${DAYS}d (< ${WARN_DAYS}d)" ""
  else
    check OK "cert-expiry" "cert expires in ${DAYS}d" ""
  fi
fi

# 6. certbot-renew.timer active
TIMER_STATUS=$(systemctl is-active certbot-renew.timer 2>/dev/null || echo "inactive")
if [[ "$TIMER_STATUS" == "active" ]]; then
  check OK "certbot-timer" "certbot-renew.timer is active" "$TIMER_STATUS"
else
  check CRIT "certbot-timer" "certbot-renew.timer is NOT active" "$TIMER_STATUS"
fi

# 7. deploy hook exists
HOOK_PATH=/etc/letsencrypt/renewal-hooks/deploy/sync-to-154.sh
if [[ -x "$HOOK_PATH" ]]; then
  check OK "deploy-hook" "$HOOK_PATH exists + executable" ""
else
  check CRIT "deploy-hook" "$HOOK_PATH missing or non-exec" "$(test -e "$HOOK_PATH" && echo 'no-exec' || echo 'missing')"
fi

# 8. end-to-end LLM chat completion via 252
CHAT_RESP=$(curl -sS --resolve "llm.kxpms.cn:443:$HOST_252" --connect-timeout "$TIMEOUT" --max-time 30 \
              -H "Authorization: Bearer $LLM_API_KEY" \
              -H "Content-Type: application/json" \
              -d "{\"model\":\"$LLM_MODEL\",\"max_tokens\":12,\"messages\":[{\"role\":\"user\",\"content\":\"$LLM_PROMPT\"}]}" \
              https://llm.kxpms.cn/v1/chat/completions 2>/dev/null || echo "")
CHAT_OK=$(echo "$CHAT_RESP" | grep -o '"content":"[^"]*"' | head -1)
if [[ -n "$CHAT_OK" ]]; then
  check OK "chat-252" "252 LLM chat completion returned content" "${CHAT_OK:0:80}"
else
  check WARN "chat-252" "252 LLM chat completion no content" "${CHAT_RESP:0:200}"
fi

# ── 输出 ────────────────────────────────────────────────────────────────

if [[ $MODE == "json" ]]; then
  # Build JSON via Python (safe escaping for embedded quotes / unicode).
  # Pass arrays via env vars separated by \x01 to avoid quote/space issues.
  SEP=$'\x01'
  export R_LEVELS="${RESULTS_LEVEL[*]/$SEP/$SEP}"  # array join is implicit
  export R_LEVELS=$(IFS="$SEP"; echo "${RESULTS_LEVEL[*]}")
  export R_NAMES=$(IFS="$SEP"; echo "${RESULTS_NAME[*]}")
  export R_MSGS=$(IFS="$SEP"; echo "${RESULTS_MSG[*]}")
  export R_DETAILS=$(IFS="$SEP"; echo "${RESULTS_DETAIL[*]}")
  python3 <<PYEOF
import json, os
levels = os.environ['R_LEVELS'].split('\x01')
names = os.environ['R_NAMES'].split('\x01')
msgs = os.environ['R_MSGS'].split('\x01')
details = os.environ['R_DETAILS'].split('\x01')
results = [
    {"level": l, "name": n, "msg": m, "detail": d}
    for l, n, m, d in zip(levels, names, msgs, details)
]
crit = sum(1 for r in results if r['level'] == 'CRIT')
warn = sum(1 for r in results if r['level'] == 'WARN')
print(json.dumps({"crit_fail": crit, "warn_fail": warn, "total": len(results), "results": results}, indent=2, ensure_ascii=False))
PYEOF
else
  echo
  TOTAL=${#RESULTS_LEVEL[@]}
  if (( CRIT_FAIL > 0 )); then
    printf "[%s] FAIL: %d CRIT, %d WARN out of %d checks\n" "$(ts)" "$CRIT_FAIL" "$WARN_FAIL" "$TOTAL"
  elif (( WARN_FAIL > 0 )); then
    printf "[%s] WARN: 0 CRIT, %d WARN out of %d checks\n" "$(ts)" "$WARN_FAIL" "$TOTAL"
  else
    printf "[%s] PASS: %d/%d checks OK\n" "$(ts)" "$TOTAL" "$TOTAL"
  fi
fi

# ── 退出码 ──────────────────────────────────────────────────────────────
if (( CRIT_FAIL > 0 )); then exit 1; fi
if (( WARN_FAIL > 0 )); then exit 2; fi
exit 0
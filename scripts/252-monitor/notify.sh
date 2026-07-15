#!/bin/bash
# 通用 webhook 通知 helper — 252 PG 监控 / llmgw 告警共用
# 支持: Feishu/Lark (含签名校验), DingTalk (含签名), Telegram, Generic POST
#
# 用法:
#   notify.sh --level critical --title "..." --body "..."
#   notify.sh -l warning -t "磁盘警告" -b "disk=85%"
#
# 配置: /etc/llmgw/notify.conf (key=value, mode 0600) + 环境变量
#   WEBHOOK_TYPE=feishu|dingtalk|telegram|generic
#   FEISHU_URL=https://open.feishu.cn/open-apis/bot/v2/hook/<TOKEN>
#   FEISHU_SECRET=<signing_secret>     # 加签校验,如不填则用免签模式
#   DINGTALK_URL=https://oapi.dingtalk.com/robot/send?access_token=...
#   DINGTALK_SECRET=...                # 可选
#   TELEGRAM_BOT_TOKEN=...
#   TELEGRAM_CHAT_ID=...
#   GENERIC_URL=https://example.com/webhook
#
# 创建: 2026-07-15
# 改造: 2026-07-15 加 Feishu 签名校验 (HMAC-SHA256, secret 是密钥而非 webhook)
set -euo pipefail

CONF=${LLMGW_NOTIFY_CONF:-/etc/llmgw/notify.conf}
[ -f "$CONF" ] && source "$CONF"
# 也支持从 environment 注入(主要给 cron wrapper 用)
: "${WEBHOOK_TYPE:=${LLMGW_WEBHOOK_TYPE:-feishu}}"
: "${FEISHU_URL:=${LLMGW_FEISHU_URL:-}}"
: "${FEISHU_SECRET:=${LLMGW_FEISHU_SECRET:-}}"
: "${DINGTALK_URL:=${LLMGW_DINGTALK_URL:-}}"
: "${DINGTALK_SECRET:=${LLMGW_DINGTALK_SECRET:-}}"
: "${TELEGRAM_BOT_TOKEN:=${LLMGW_TELEGRAM_BOT_TOKEN:-}}"
: "${TELEGRAM_CHAT_ID:=${LLMGW_TELEGRAM_CHAT_ID:-}}"
: "${GENERIC_URL:=${LLMGW_GENERIC_URL:-}}"

LEVEL="info"
TITLE=""
BODY=""

while [ $# -gt 0 ]; do
  case "$1" in
    --level|-l) LEVEL="$2"; shift 2;;
    --title|-t)  TITLE="$2"; shift 2;;
    --body|-b)   BODY="$2"; shift 2;;
    -h|--help)   sed -n '2,38p' "$0"; exit 0;;
    *) TITLE="$1"; BODY="${2:-}"; shift 2;;
  esac
done

if [ -z "$TITLE" ]; then
  echo "usage: $0 --level <info|warning|critical> --title \"...\" --body \"...\"" >&2
  exit 1
fi

# emoji 装饰
case "$LEVEL" in
  critical) PREFIX="🔴[CRITICAL]";;
  warning)  PREFIX="🟡[WARNING]";;
  info)     PREFIX="🟢[INFO]";;
  *)        PREFIX="[$LEVEL]";;
esac

MSG="${PREFIX} ${TITLE}
${BODY}
host=$(hostname) ts=$(date -Iseconds)"

# Python 用于 JSON 转义（已经验证 docker exec 能用 python）
py_json_escape() { python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'; }
py_url_quote()   { python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.stdin.read(), safe=""))'; }
py_hmac_sha256_b64() {
  # args: secret, string_to_sign
  python3 -c "import hmac, hashlib, base64, sys
secret = sys.argv[1].encode()
msg = sys.argv[2].encode()
sig = hmac.new(secret, msg, hashlib.sha256).digest()
print(base64.b64encode(sig).decode())" "$1" "$2"
}

send_feishu() {
  [ -z "${FEISHU_URL:-}" ] && return 1
  local url="$FEISHU_URL"
  # 加签:timestamp/secret 都要
  if [ -n "${FEISHU_SECRET:-}" ]; then
    local ts hmac_input sign
    ts=$(date +%s)
    hmac_input=$(printf '%s\n%s' "$ts" "$FEISHU_SECRET")
    sign=$(py_hmac_sha256_b64 "$FEISHU_SECRET" "$hmac_input")
    url="${FEISHU_URL}?timestamp=${ts}&sign=${sign}"
  fi
  local text payload
  text=$(printf '%s' "$MSG" | py_json_escape)
  payload=$(printf '{"msg_type":"text","content":{"text":%s}}' "$text")
  curl -fsS -m 10 -X POST -H 'Content-Type: application/json' \
    -d "$payload" "$url" >/dev/null
}

send_dingtalk() {
  [ -z "${DINGTALK_URL:-}" ] && return 1
  local url="$DINGTALK_URL"
  local payload
  payload=$(printf '{"msgtype":"text","text":{"content":%s}}' \
    "$(printf '%s' "$MSG" | py_json_escape)")
  if [ -n "${DINGTALK_SECRET:-}" ]; then
    local ts sign
    ts=$(printf '%s' "$(date +%s%3N)")
    sign=$(printf '%s\n%s' "$ts" "$DINGTALK_SECRET" \
           | openssl dgst -sha256 -hmac "$DINGTALK_SECRET" -binary | base64)
    url="${DINGTALK_URL}&timestamp=$ts&sign=$sign"
  fi
  curl -fsS -m 10 -X POST -H 'Content-Type: application/json' \
    -d "$payload" "$url" >/dev/null
}

send_telegram() {
  [ -z "${TELEGRAM_BOT_TOKEN:-}" ] || [ -z "${TELEGRAM_CHAT_ID:-}" ] && return 1
  local text
  text=$(printf '%s' "$MSG" | py_url_quote)
  curl -fsS -m 10 "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage?chat_id=${TELEGRAM_CHAT_ID}&text=$text" >/dev/null
}

send_generic() {
  [ -z "${GENERIC_URL:-}" ] && return 1
  local payload
  payload=$(printf '{"level":%s,"title":%s,"body":%s,"host":%s}' \
    "$(printf '%s' "$LEVEL" | py_json_escape)" \
    "$(printf '%s' "$TITLE" | py_json_escape)" \
    "$(printf '%s' "$BODY" | py_json_escape)" \
    "$(hostname | py_json_escape)")
  curl -fsS -m 10 -X POST -H 'Content-Type: application/json' \
    -d "$payload" "$GENERIC_URL" >/dev/null
}

ok=false
case "$WEBHOOK_TYPE" in
  feishu)   send_feishu   && ok=true;;
  dingtalk) send_dingtalk && ok=true;;
  telegram) send_telegram && ok=true;;
  generic)  send_generic  && ok=true;;
  *) echo "[notify] WEBHOOK_TYPE=$WEBHOOK_TYPE 未知,只本地 log" >&2;;
esac

LOG=/var/log/llmgw-notify.log
mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
if [ "$ok" = "true" ]; then
  echo "[$(date -Iseconds)] OK $LEVEL $TITLE" >> "$LOG"
  exit 0
else
  echo "[$(date -Iseconds)] LOCAL $LEVEL $TITLE -- $BODY" >> "$LOG"
  exit 0  # 本地 log 永远 exit 0
fi

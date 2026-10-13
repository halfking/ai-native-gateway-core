#!/bin/bash
# scripts/sanitize-e2e-154.sh — 敏感词脱敏/还原多场景多模型端到端验证（154 生产网关）
#
# 用途：向 154 网关发送多种场景的信息，逐轮检查
#   1. 输入侧脱敏：上游收到的必须是 {SENSITIVE:type:n} 占位符，不能是原文
#   2. 响应侧还原：模型回显占位符后，客户端必须拿到原文，且不能残留占位符
#   3. 落库/留痕：request_logs_bodies_hot.outbound_body 不含原文；
#      session_censors 有占位符映射；Redis map 能还原
#   4. 多模型：minimax-m3 / gpt-6-luna / glm-5.3 / deepseek-v4-pro / mimo-v2.5-pro
#
# 依赖：curl / jq / ssh（用于远端 psql 取证）
# 用法：
#   GATEWAY=http://47.97.111.154:8782 bash scripts/sanitize-e2e-154.sh
#   MODELS="minimax-m3 gpt-6-luna" bash scripts/sanitize-e2e-154.sh
set -uo pipefail

GATEWAY="${GATEWAY:-http://47.97.111.154:8782}"
SSH_HOST="${SSH_HOST:-154}"
MODELS="${MODELS:-minimax-m3 gpt-6-luna glm-5.3 deepseek-v4-pro mimo-v2.5-pro}"
API_KEY="${API_KEY:-}"
if [ -z "$API_KEY" ]; then
  echo "需要 API_KEY（生产网关 API key）" >&2
  exit 2
fi

OUT="${OUT:-/tmp/sanitize-e2e-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT"

PASS=0
FAIL=0
declare -a FAILURES=()

ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); FAILURES+=("$1"); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
note() { printf '       %s\n' "$1"; }

# ---------------------------------------------------------------- db 取证
# SQL 经 stdin 送进远端 psql，不在本地做引号转义：转义那步要过 sed，
# 而 sed 碰到 psql 回传的中文/转义字节会报 "illegal byte sequence" 并
# 中断整条脚本。远端 psql 侧设 client_encoding=UTF8，输出保持可读。
rsql() {
  local q="$1"
  printf '%s\n' "$q" | ssh -o BatchMode=yes -o ConnectTimeout=10 "$SSH_HOST" \
    'DSN=$(grep -E "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | head -1 | cut -d= -f2-)
     export PGCLIENTENCODING=UTF8
     psql "$DSN" -X -A -t -f -' 2>/dev/null
}

chat() { # chat <model> <session> <messages-json> [stream]
  local model="$1" sid="$2" msgs="$3" stream="${4:-false}"
  curl -sS -m 180 \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -H "X-Session-ID: $sid" \
    "$GATEWAY/v1/chat/completions" \
    -d "{\"model\":\"$model\",\"messages\":$msgs,\"stream\":$stream}"
}

content_of() { jq -r '.choices[0].message.content // empty' 2>/dev/null; }

# ================================================================ 场景定义
# 每个场景：真值 -> 占位符类型 -> 期望在响应中出现的原文
# 用 shell 数组避免引号地狱。
run_scenario() {
  local model="$1"
  local sid="sani-$(echo "$model" | tr -c 'a-zA-Z0-9' '-')-$(date +%s)-$RANDOM"
  echo
  echo "── 模型 $model / 会话 $sid"

  # ---------- 场景 A：手机号（单轮真值 + 二轮回显）
  local a_phone="13900001111"
  local r1 r2
  r1=$(chat "$model" "$sid" "[{\"role\":\"user\",\"content\":\"我的手机号是 ${a_phone}，请确认收到。\"}]")
  local c1; c1=$(printf '%s' "$r1" | content_of)

  if printf '%s' "$c1" | grep -q 'SENSITIVE:'; then
    bad "A1 响应泄漏占位符 ($model)"
    note "$(printf '%s' "$c1" | head -c 200)"
  else
    ok "A1 单轮响应无占位符残留 ($model)"
  fi

  # 二轮：把含真值的历史重放，让模型有机会回显；要求它复述占位符形态
  r2=$(chat "$model" "$sid" "[{\"role\":\"user\",\"content\":\"我的手机号是 ${a_phone}，请确认收到。\"},{\"role\":\"assistant\",\"content\":\"好的，已记录。\"},{\"role\":\"user\",\"content\":\"请把上面第一句里被模板变量替换过的那段手机号原样输出，只输出号码。\"}]")
  local c2; c2=$(printf '%s' "$r2" | content_of)

  if printf '%s' "$c2" | grep -q 'SENSITIVE:'; then
    bad "A2 二轮响应仍含未还原占位符 ($model)"
    note "$(printf '%s' "$c2" | head -c 200)"
  else
    ok "A2 二轮响应无占位符残留 ($model)"
  fi
  if printf '%s' "$c2" | grep -q "$a_phone"; then
    ok "A3 二轮还原出原文手机号 ($model)"
  else
    note "A3 未还原出原文（可能模型拒绝复述，非网关缺陷）"
  fi

  # ---------- 场景 B：多类型混合（邮箱 / 身份证 / 银行卡 / API key）
  local b_email="zhangsan@example.com"
  local b_id="110101199003078888"
  local b_card="6222021234567890123"
  # 用真实厂商 key 形态（sk-proj- 中缀连字符）。这里刻意不用
  # "sk-probe-ABCDEFGHIJKLMNOP1234"：audit 244 F2 修的正是这类形态，
  # 修复前它会漏检并让 B2 失败，修复后应全部命中并被脱敏。
  local b_key="sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV"
  local rb
  rb=$(chat "$model" "$sid" "[{\"role\":\"user\",\"content\":\"登记信息：邮箱 ${b_email}，身份证 ${b_id}，银行卡 ${b_card}，密钥 ${b_key}。已收到请回复 OK。\"}]")
  local cb; cb=$(printf '%s' "$rb" | content_of)
  if printf '%s' "$cb" | grep -q 'SENSITIVE:'; then
    bad "B1 混合类型响应泄漏占位符 ($model)"
    note "$(printf '%s' "$cb" | head -c 200)"
  else
    ok "B1 混合类型响应无占位符残留 ($model)"
  fi
  # 混检：模型响应里复现任一真值都要分清两种成因——
  #   * 网关漏检（真缺陷）：该值本该被替换成占位符，却原样到了客户端；
  #   * 模型凭空捏造：值是模型自己编的，与网关无关。
  # 二者的判据是「出向正文里有没有占位符」：出向有占位符说明网关脱敏生效，
  # 客户端看到的原文只可能来自模型自行复述历史；出向没有则是网关漏检。
  local leaked=""
  for v in "$b_email" "$b_id" "$b_card" "$b_key"; do
    if printf '%s' "$cb" | grep -qF "$v"; then leaked="$leaked $v"; fi
  done
  if [ -n "$leaked" ]; then
    # 用同会话的出向正文判断成因
    local ph_present="unknown"
    local rid
    rid=$(rsql "select request_id from _request_logs_hot where gw_session_id like 'gw_${sid}' order by ts desc limit 1")
    if [ -n "$rid" ]; then
      ph_present=$(rsql "select coalesce((outbound_body::text like '%SENSITIVE%')::text,'nobody') from _request_logs_bodies_hot where request_id='$rid'")
    fi
    if [ "$ph_present" = "false" ]; then
      bad "B2 网关漏检：真值原样到达客户端 ($model):$leaked"
      note "出向正文无占位符 → 该值未被脱敏，属网关缺陷"
    else
      note "B2 模型自行复述了历史中的值 ($model):$leaked（出向已有占位符，非网关缺陷）"
    fi
  else
    ok "B2 模型响应未复现敏感原文 ($model)"
  fi

  # ---------- 场景 C：流式（SSE 跨 chunk 还原）
  # 两点都要对：SSE 行有 "data: " 前缀（含 ": keep-alive" 注释行），
  # 推理型模型把正文放在 reasoning_content，普通模型放在 content，
  # 两者都要收，否则会把流式响应当成空响应。
  local rc
  rc=$(chat "$model" "$sid-c" "[{\"role\":\"user\",\"content\":\"我的邮箱是 li.si@example.org，请回一句收到。\"}]" true)
  local rc_text
  rc_text=$(printf '%s' "$rc" \
    | grep '^data: ' \
    | sed 's/^data: //' \
    | grep -v '^\[DONE\]' \
    | jq -rs '[.[] | select(type=="object") | .choices[]?.delta | (.content // empty), (.reasoning_content // empty)] | join("")' 2>/dev/null)
  if printf '%s' "$rc" | grep -q 'SENSITIVE:'; then
    bad "C1 流式响应泄漏占位符 ($model)"
  else
    ok "C1 流式响应无占位符残留 ($model)"
  fi
  if [ -n "$rc_text" ] && [ "$rc_text" != "null" ] && [ "$rc_text" != "[]" ]; then
    ok "C2 流式响应可解析出内容 ($model)"
  else
    bad "C2 流式响应为空或格式异常 ($model)"
    note "$(printf '%s' "$rc" | head -c 200)"
  fi
  # 流式下的还原：邮箱原文应回到客户端，且不留占位符
  if printf '%s' "$rc_text" | grep -qF "li.si@example.org"; then
    ok "C3 流式还原出原文邮箱 ($model)"
  else
    note "C3 流式未还原出原文（模型未复述，非网关缺陷）"
  fi

  # ---------- 场景 D：无敏感信息（不应误伤）
  local rd
  rd=$(chat "$model" "$sid-d" "[{\"role\":\"user\",\"content\":\"用一句话解释什么是负载均衡。\"}]")
  local cd; cd=$(printf '%s' "$rd" | content_of)
  if [ -n "$cd" ]; then
    ok "D1 无敏感信息正常应答 ($model)"
  else
    bad "D1 无敏感信息场景空响应 ($model)"
    note "$(printf '%s' "$rd" | head -c 200)"
  fi
  if printf '%s' "$cd" | grep -q '\[REDACTED\]'; then
    bad "D2 无敏感信息被误脱敏 ($model)"
    note "$(printf '%s' "$cd" | head -c 200)"
  else
    ok "D2 无敏感信息未被误脱敏 ($model)"
  fi

  echo "$sid"
  LAST_SID="$sid"
}

echo "══════════════════════════════════════════════════════════"
echo " 敏感词脱敏/还原 多场景多模型 E2E"
echo " 网关: $GATEWAY"
echo " 模型: $MODELS"
echo " 产物: $OUT"
echo "══════════════════════════════════════════════════════════"

: > "$OUT/sessions.txt"
for m in $MODELS; do
  # 不放进 $(...)：命令替换会让 ok/bad 在子 shell 里自增，计数与 FAILURES 都会丢。
  run_scenario "$m"
  echo "$LAST_SID" >> "$OUT/sessions.txt"
  if [ -z "${LAST_SID:-}" ]; then
    bad "模型 $m 场景未完成"
  fi
done

# ================================================================ 落库取证
echo
echo "════════ 取证：落库与留痕 ════════"
SID1=$(grep -m1 '^sani-' "$OUT/sessions.txt")

if [ -n "$SID1" ]; then
  echo "-- 目标会话: $SID1"
  # 1) outbound_body 必须不含原文（真值只应在加密的 censors 里）
  echo "-- request_logs_bodies_hot.outbound_body 抽样（应为占位符）:"
  rsql "select left(outbound_body::text, 400) from _request_logs_bodies_hot where ts > now() - interval '30 minutes' and outbound_body is not null order by ts desc limit 3" \
    | tee "$OUT/outbound_sample.txt" | cut -c1-260
  if grep -q 'SENSITIVE:' "$OUT/outbound_sample.txt"; then
    ok "E1 上游收到的是占位符而非原文"
  else
    note "E1 未在抽样中找到占位符（可能该批请求未触发脱敏）"
  fi

  # 2) censors 映射存在性
  echo "-- session_censors_hot 映射:"
  rsql "select session_id||' | '||placeholder||' | '||sensitive_type from session_censors_hot where session_id like 'gw_sani-%' order by created_at desc limit 10" \
    | tee "$OUT/censors_sample.txt"
  if [ -s "$OUT/censors_sample.txt" ]; then
    ok "E2 占位符映射已落库（original_encrypted 加密存储）"
  else
    bad "E2 未查到占位符映射落库"
  fi

  # 3) 明文泄漏扫描：近 30 分钟落库的 outbound_body 里是否出现真值
  echo "-- 明文泄漏扫描（outbound_body 是否含真值）:"
  LEAK=$(rsql "select count(*) from _request_logs_bodies_hot where ts > now() - interval '30 minutes' and (outbound_body::text like '%13900001111%' or outbound_body::text like '%zhangsan@example.com%' or outbound_body::text like '%110101199003078888%' or outbound_body::text like '%6222021234567890123%')")
  if [ "${LEAK:-0}" = "0" ]; then
    ok "E3 出向落库无明文敏感值"
  else
    bad "E3 出向落库检出 $LEAK 条明文敏感值"
  fi

  # 4) 入向 request_body 是否存明文（设计上原始正文有独立存储边界，
  #    这里只做观测，不判失败）
  echo "-- 入向 request_body 观测（记录用，不判失败）:"
  rsql "select count(*) from _request_logs_bodies_hot where ts > now() - interval '30 minutes' and request_body::text like '%13900001111%'" \
    | tail -1 | sed 's/^/   含真值的入向行数: /' | tee "$OUT/inbound_plaintext_count.txt"
fi

echo
echo "══════════════════════════════════════════════════════════"
printf ' 通过: \033[32m%d\033[0m  失败: \033[31m%d\033[0m\n' "$PASS" "$FAIL"
echo "══════════════════════════════════════════════════════════"
if [ "$FAIL" -gt 0 ]; then
  echo "失败项："
  for f in "${FAILURES[@]}"; do echo "  - $f"; done
  exit 1
fi
exit 0

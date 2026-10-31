#!/bin/bash
# ─────────────────────────────────────────────────────────────────────────────
# credential-health-recover.sh — 部署后凭据健康自动恢复（预热 → reset → check）
# ─────────────────────────────────────────────────────────────────────────────
#
# 背景（2026-10-27 24h 审计轮）：每次部署/重启后，凭据×模型层残留上一次会话的
# 翻红/退避（node_probe_state 连败、credentialstate unavailable、backoff 封顶），
# 本地 ollama 类凭据还叠加「冷加载超时」——探针 10~30s 预算内模型还没 load 完
# ⇒ 探针超时 ⇒ 路由排除 ⇒ 真实流量 no_candidate。此前「预热 → reset → check」
# 全靠手工逐个点，本脚本把它序列化（消除手工恢复）。
#
# 三步语义：
#   1) 预热  POST /api/admin/credentials/{id}/session-ping {model}（superAdmin）
#      ——对 (凭据,模型) 发一次真实直连请求（服务端 15s 预算），既验证直连
#        又把冷模型 load 进内存。给 --api-key 时再补一发走网关的
#        /v1/chat/completions 深预热（90s 预算，专治冷加载超预算）。
#   2) reset POST /api/routing/credentials/{id}/reset-state
#      {reason:..., trigger_probe:true}（superAdmin）——清状态 + 立即入队探测，
#      不等 5min 周期（admin/routing_reset.go）。
#   3) check 轮询 GET /api/credentials/{id}/models/{model}/state
#      （admin/credential_state_handlers.go），读 state.available /
#      state.health_status，直到全部恢复或 --wait 超时。
#
# 默认范围 = node_probe_state 里连败/paused 的 (凭据,模型) **对**（对级口径；
# --all 放大到全部 active 凭据目录；--credential=ID[,ID...] 指定凭据时取其全目录）。
#
# ⚠ 副作用知情同意：reset-state 是 force-enable——对真实坏掉的上游，reset 后
#   到探测再确认之间会有少量请求打到坏上游（有 failover 兜底）；与手工 reset
#   的风险一致。
#
# 用法：
#   credential-health-recover.sh                       # 不健康凭据，全流程
#   credential-health-recover.sh --dry-run             # 只列清单，不发请求
#   credential-health-recover.sh --credential 99005    # 指定凭据
#   credential-health-recover.sh --all --api-key sk-.. # 全量 + 深预热
#   credential-health-recover.sh --skip-warmup --wait 60
#   credential-health-recover.sh --include-protocol-red   # 强推含协议红对（默认排除）
#   credential-health-recover.sh --self-test           # 无副作用自检
#
# 环境变量（均可被同名 flag 覆盖）：
#   GW_HOST            ssh 目标（默认 root@192.168.31.211；GW_HOST="" =本机 docker）
#   GW_BASE            网关入口（默认 https://llm.itestu.cn）
#   GW_ADMIN_USER / GW_ADMIN_PASSWORD   登录 /api/auth/token（默认与
#                                      deploy-seamless-docker.sh 冒烟约定一致）
#   GW_TOKEN           已有 token（跳过登录）
#   GW_PG_CONTAINER / GW_PG_USER / GW_DB  清单查询（默认 acc-pg17 / acc_super / llm_gateway）
# ─────────────────────────────────────────────────────────────────────────────
set -u

GW_HOST="${GW_HOST:-root@192.168.31.211}"
GW_BASE="${GW_BASE:-https://llm.itestu.cn}"
GW_ADMIN_USER="${GW_ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:-admin}}"
GW_ADMIN_PASSWORD="${GW_ADMIN_PASSWORD:-${LLM_GATEWAY_ADMIN_PASSWORD:-Veritrans&9527}}"
GW_TOKEN="${GW_TOKEN:-}"
GW_PG_CONTAINER="${GW_PG_CONTAINER:-acc-pg17}"
GW_PG_USER="${GW_PG_USER:-acc_super}"
GW_DB="${GW_DB:-llm_gateway}"

SCOPE="unhealthy"   # unhealthy | all
CRED_IDS=""
API_KEY=""
WAIT_SEC=120
INTERVAL_SEC=10
SKIP_WARMUP=0
DRY_RUN=0
# ★ 协议红默认排除（2026-10-27 24h 审计轮）：last_err_code ∈ (http_404, http_410)
#   且连败 ≥2 的对是「上游没有这个模型 / 模型已永久下线」的**协议级事实**，
#   重探不会变绿；reset-state 对它们是 force-enable ⇒ 探针再确认 ⇒ 再翻红
#   ⇒ 下一轮再 reset ——「反复 reset」的病理循环本身（上一轮 366 对存量
#   即由此而来）。默认把它们从恢复对象中剔除；确要强推用
#   --include-protocol-red 显式接受副作用。处置协议红的正确动作是**下线绑定**
#   （credential_model_bindings.available=FALSE + manual: 前缀 reason）。
INCLUDE_PROTOCOL_RED=0

TMPDIR_RECOVER="${TMPDIR:-/tmp}/gw-cred-recover.$$"
mkdir -p "$TMPDIR_RECOVER"
trap 'rm -rf "$TMPDIR_RECOVER"' EXIT

usage() { grep -E '^# ?' "$0" | sed -n '2,60p' | sed 's/^# \{0,1\}//'; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --all) SCOPE="all"; shift ;;
    --credential) CRED_IDS="$2"; shift 2 ;;
    --api-key) API_KEY="$2"; shift 2 ;;
    --wait) WAIT_SEC="$2"; shift 2 ;;
    --interval) INTERVAL_SEC="$2"; shift 2 ;;
    --skip-warmup) SKIP_WARMUP=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --include-protocol-red) INCLUDE_PROTOCOL_RED=1; shift ;;
    --base) GW_BASE="$2"; shift 2 ;;
    --host) GW_HOST="$2"; shift 2 ;;
    --token) GW_TOKEN="$2"; shift 2 ;;
    --user) GW_ADMIN_USER="$2"; shift 2 ;;
    --password) GW_ADMIN_PASSWORD="$2"; shift 2 ;;
    --help|-h) usage ;;
    --self-test) SELFTEST=1; shift ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done
SELFTEST="${SELFTEST:-0}"

say()  { printf '%s\n' "$*"; }
fail() { printf '❌ %s\n' "$*" >&2; exit 1; }

# ── 工具：DB 只读查询（经 ssh 或本机 docker exec）──────────────────────────
db_query() {
  local sql="$1"
  if [ -n "$GW_HOST" ]; then
    ssh -o BatchMode=yes -o ConnectTimeout=8 "$GW_HOST" \
      "docker exec $GW_PG_CONTAINER psql -U $GW_PG_USER -d $GW_DB -Atc \"$sql\"" 2>&1
  else
    docker exec "$GW_PG_CONTAINER" psql -U "$GW_PG_USER" -d "$GW_DB" -Atc "$sql" 2>&1
  fi
}

# ── 工具：URL 路径段转义（模型名含 / : . 等；web API 的 {model} 段）─────────
urlencode() {
  python3 -c 'import urllib.parse,sys; sys.stdout.write(urllib.parse.quote(sys.argv[1], safe=""))' "$1"
}

# ── 工具：admin API 调用 ───────────────────────────────────────────────────
api() {
  # api METHOD PATH [JSON_BODY]  → stdout=HTTP code，响应体存 $TMPDIR_RECOVER/last.json
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS -o "$TMPDIR_RECOVER/last.json" -w '%{http_code}' --max-time 120 \
      -X "$method" "$GW_BASE$path" \
      -H "Authorization: Bearer $GW_TOKEN" -H 'Content-Type: application/json' -d "$body"
  else
    curl -sS -o "$TMPDIR_RECOVER/last.json" -w '%{http_code}' --max-time 120 \
      -X "$method" "$GW_BASE$path" \
      -H "Authorization: Bearer $GW_TOKEN"
  fi
}

last_json_field() { # last_json_field <key>
  python3 -c 'import json,sys
try:
    d = json.load(open(sys.argv[2]))
    for k in sys.argv[1].split("."):
        d = d[k] if isinstance(d, dict) else None
        if d is None: break
    print(d if not isinstance(d, (dict, list)) else json.dumps(d))
except Exception:
    print("?")' "$1" "$TMPDIR_RECOVER/last.json"
}

# ── 自检（无副作用）────────────────────────────────────────────────────────
self_test() {
  say "self-test:"
  [ "$(urlencode 'qwen3.8:27b-mlx')" = 'qwen3.8%3A27b-mlx' ] && say "  PASS urlencode colon" || { say "  FAIL urlencode colon"; exit 1; }
  [ "$(urlencode 'qwen/qwen3-5')" = 'qwen%2Fqwen3-5' ] && say "  PASS urlencode slash" || { say "  FAIL urlencode slash"; exit 1; }
  echo '{"state":{"available":true,"health_status":"healthy"}}' > "$TMPDIR_RECOVER/last.json"
  [ "$(last_json_field state.available)" = "True" ] && say "  PASS json field bool" || { say "  FAIL json field bool"; exit 1; }
  [ "$(last_json_field state.health_status)" = "healthy" ] && say "  PASS json field str" || { say "  FAIL json field str"; exit 1; }
  echo 'not-json' > "$TMPDIR_RECOVER/last.json"
  [ "$(last_json_field state.available)" = "?" ] && say "  PASS bad json tolerated" || { say "  FAIL bad json"; exit 1; }
  command -v curl >/dev/null && say "  PASS curl present" || { say "  FAIL curl missing"; exit 1; }
  command -v python3 >/dev/null && say "  PASS python3 present" || { say "  FAIL python3 missing"; exit 1; }
  say "  (auth/DB 联通性属生产探测，请用 --dry-run 验证)"
  exit 0
}
[ "$SELFTEST" = "1" ] && self_test

# ── Step 0: 登录 ───────────────────────────────────────────────────────────
if [ -z "$GW_TOKEN" ]; then
  say "── Step 0: 登录 ${GW_BASE}（/api/auth/token, user=${GW_ADMIN_USER}）"
  login_code=$(curl -sS -o "$TMPDIR_RECOVER/login.json" -w '%{http_code}' --max-time 20 \
    -X POST "$GW_BASE/api/auth/token" -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,sys; print(json.dumps({"username":sys.argv[1],"password":sys.argv[2]}))' "$GW_ADMIN_USER" "$GW_ADMIN_PASSWORD")" \
    || echo 000)
  [ "$login_code" = "200" ] || fail "登录失败 HTTP $login_code: $(head -c 200 "$TMPDIR_RECOVER/login.json" 2>/dev/null)"
  GW_TOKEN=$(python3 -c 'import json,sys
print(json.load(open(sys.argv[1]))["access_token"])' "$TMPDIR_RECOVER/login.json" 2>/dev/null) \
    || fail "登录响应缺 access_token"
  say "   ✓ 登录成功"
else
  say "── Step 0: 使用提供的 GW_TOKEN"
fi

# ── Step 1: 清单（凭据 × 模型）──────────────────────────────────────────────
say "── Step 1: 凭据×模型清单（范围=${SCOPE}，DB=$GW_PG_CONTAINER/${GW_DB}）"
INV_SQL="SELECT c.id || '|' || COALESCE(c.label,'') || '|' || COALESCE(string_agg(DISTINCT pm.raw_model_name, ','), '')
FROM credentials c
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE c.lifecycle_status = 'active'
GROUP BY c.id, c.label ORDER BY c.id"
INVENTORY=$(db_query "$INV_SQL")
[ -n "$INVENTORY" ] || fail "清单为空（ssh/DB 连通性或库参数问题）"

UNHEALTHY=""
if [ "$SCOPE" = "unhealthy" ] && [ -z "$CRED_IDS" ]; then
  UNHEALTHY=$(db_query "SELECT DISTINCT credential_id FROM node_probe_state WHERE consecutive_failures > 0 OR paused" | tr '\n' ',')
  UNHEALTHY="${UNHEALTHY%,}"
  if [ -z "$UNHEALTHY" ]; then
    say "   ✓ node_probe_state 无连败/paused 行 ⇒ 无需恢复"
    exit 0
  fi
  say "   不健康 (凭据,模型) 对所在凭据: $UNHEALTHY"
fi

# 目标过滤：ids 精确指定 > unhealthy 子集 > 全部
printf '%s\n' "$INVENTORY" | awk -F'|' -v ids="$CRED_IDS" -v scope="$SCOPE" -v unhealthy="$UNHEALTHY" '
  BEGIN { n = split(ids, a, ","); for (i=1;i<=n;i++) if (a[i]!="") want[a[i]]=1
          m = split(unhealthy, b, ","); for (i=1;i<=m;i++) if (b[i]!="") sick[b[i]]=1 }
  { if (ids != "") { if ($1 in want) print; next }
    if (scope == "unhealthy") { if ($1 in sick) print; next }
    print }' > "$TMPDIR_RECOVER/targets.txt"
[ -s "$TMPDIR_RECOVER/targets.txt" ] || fail "目标集为空（scope=$SCOPE ids=${CRED_IDS}）"
sed 's/^/   /' "$TMPDIR_RECOVER/targets.txt"

# ★ 口径：恢复的精确对象是 node_probe_state 里不健康的 (凭据,模型) **对**，
# 不是「该凭据目录下全部模型」——聚合型供应商一个目录上百模型，按凭据展开
# 会把恢复动作变成对全目录的 ping 风暴（首轮 dry-run 实测 17 凭据 ⇒ 668 对）。
if [ "$SCOPE" = "unhealthy" ] && [ -z "$CRED_IDS" ]; then
  if [ "$INCLUDE_PROTOCOL_RED" = "1" ]; then
    db_query "SELECT s.credential_id || '|' || s.raw_model_name
FROM node_probe_state s
JOIN credentials c ON c.id = s.credential_id AND c.lifecycle_status = 'active'
WHERE s.consecutive_failures > 0 OR s.paused
ORDER BY s.credential_id, s.raw_model_name" > "$TMPDIR_RECOVER/pairs.txt"
  else
    # ★ 协议红对不进恢复对象（见头部 INCLUDE_PROTOCOL_RED 注释）。
    db_query "SELECT s.credential_id || '|' || s.raw_model_name
FROM node_probe_state s
JOIN credentials c ON c.id = s.credential_id AND c.lifecycle_status = 'active'
WHERE (s.consecutive_failures > 0 OR s.paused)
  AND NOT (s.last_err_code IN ('http_404','http_410') AND s.consecutive_failures >= 2)
ORDER BY s.credential_id, s.raw_model_name" > "$TMPDIR_RECOVER/pairs.txt"
    EXCLUDED=$(db_query "SELECT count(*) FROM node_probe_state s
JOIN credentials c ON c.id = s.credential_id AND c.lifecycle_status = 'active'
WHERE (s.consecutive_failures > 0 OR s.paused)
  AND s.last_err_code IN ('http_404','http_410') AND s.consecutive_failures >= 2")
    [ "${EXCLUDED:-0}" -gt 0 ] 2>/dev/null && \
      say "   ⓘ 已排除 $EXCLUDED 个协议红对（http_404/410 连败≥2：上游没有该模型，reset 只会再翻红；处置=下线绑定）"
  fi
else
  awk -F'|' '$3 != "" { n = split($3, ms, ","); for (i=1;i<=n;i++) if (ms[i] != "") print $1 "|" ms[i] }' \
    "$TMPDIR_RECOVER/targets.txt" > "$TMPDIR_RECOVER/pairs.txt"
fi
[ -s "$TMPDIR_RECOVER/pairs.txt" ] || fail "目标 (凭据,模型) 对为空（scope=$SCOPE ids=${CRED_IDS}）"
PAIRS=$(wc -l < "$TMPDIR_RECOVER/pairs.txt" | tr -d ' ')
say "   ⇒ $(wc -l < "$TMPDIR_RECOVER/targets.txt" | tr -d ' ') 个凭据 / $PAIRS 个 (凭据,模型) 对"
if [ "$DRY_RUN" = "1" ]; then
  say "   恢复对象:"
  sed 's/^/     - /' "$TMPDIR_RECOVER/pairs.txt"
  say "(dry-run: 到此为止，未发任何预热/reset 请求)"
  exit 0
fi

# ── Step 2: 预热（session-ping 直连真实请求）────────────────────────────────
if [ "$SKIP_WARMUP" = "0" ]; then
  say "── Step 2: 预热（session-ping，真实直连，服务端 15s 预算）"
  # 凭据级限流（实测 99005 = 1 req/s）：同凭据连续 ping 之间节流，429 退避重试一次
  prev_cid=""
  while IFS='|' read -r cid model; do
    [ -n "$cid" ] || continue
    if [ "$cid" = "$prev_cid" ]; then sleep 1.2; fi
    prev_cid="$cid"
    code=$(api POST "/api/admin/credentials/$cid/session-ping" "{\"model\":\"$model\"}")
    if [ "$code" = "429" ]; then
      sleep 1.5
      code=$(api POST "/api/admin/credentials/$cid/session-ping" "{\"model\":\"$model\"}")
    fi
    if [ "$code" = "200" ]; then
      say "   ✓ cred=$cid model=$model ping=$(last_json_field status) latency=$(last_json_field latency_ms)ms"
    else
      say "   ⚠ cred=$cid model=$model HTTP $code: $(head -c 140 "$TMPDIR_RECOVER/last.json" 2>/dev/null)"
    fi
  done < "$TMPDIR_RECOVER/pairs.txt"
else
  say "── Step 2: 预热跳过（--skip-warmup）"
fi

# 可选深预热：走网关真实 chat（治「冷加载超探针预算」的根）
if [ -n "$API_KEY" ]; then
  say "── Step 2b: 深预热（网关 /v1/chat/completions，90s 预算）"
  while IFS='|' read -r cid model; do
    [ -n "$cid" ] || continue
    code=$(curl -sS -o "$TMPDIR_RECOVER/last.json" -w '%{http_code}' --max-time 90 \
      -X POST "$GW_BASE/v1/chat/completions" \
      -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
      -d "{\"model\":\"$model\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}")
    say "   ⚑ cred=$cid model=$model warmup HTTP $code"
  done < "$TMPDIR_RECOVER/pairs.txt"
fi

# ── Step 3: reset（force-enable + 立即探测）────────────────────────────────
# ★ 必须带 raw_model（2026-10-27 二次实测教训）：reset-state 的 raw_model 为空
#   = **整凭据** force-enable（admin/routing_reset.go:34）⇒ 会把同凭据下
#   「manual: 前缀下线的协议红绑定」一并拉回路由 ⇒ 本轮 291 个下线被
#   13 次凭据级 reset 全部清空的真因。带 raw_model ⇒ 只 force-enable 该对。
say "── Step 3: reset-state（对级 raw_model，trigger_probe=true）"
while IFS='|' read -r cid model; do
  [ -n "$cid" ] || continue
  code=$(api POST "/api/routing/credentials/$cid/reset-state" \
    "$(printf '{"reason":"credential-health-recover.sh post-deploy auto recovery","raw_model":"%s","trigger_probe":true}' "$model")")
  if [ "$code" = "200" ]; then
    say "   ✓ cred=$cid model=$model reset-state OK"
  else
    say "   ⚠ cred=$cid model=$model reset-state HTTP $code: $(head -c 140 "$TMPDIR_RECOVER/last.json" 2>/dev/null)"
  fi
done < "$TMPDIR_RECOVER/pairs.txt"

# ── Step 4: check（轮询，全部恢复或超时为止）────────────────────────────────
# 判据来源自动降级：优先 GET /api/credentials/{id}/models/{model}/state
# （credentialstate 活状态）；211 生产实测该端点 503（stateManager 未启用）
# ⇒ 首轮 503 即降级为 DB 直读 node_probe_state.consecutive_failures
# （reset-state 的 trigger_probe 实测 ~17s 内刷新全部行）。node_probe_state
# 未跟踪的模型（如 embedding 类）视为 OK（无可判定状态 ≠ 不健康）。
say "── Step 4: check（每 ${INTERVAL_SEC}s 一轮，最多 ${WAIT_SEC}s）"
RESET_TS=$(date '+%Y-%m-%d %H:%M:%S')
say "   reset 时刻（本机时区）: $RESET_TS"
deadline=$(( $(date +%s) + WAIT_SEC ))
CHECK_SOURCE=api
check_round() {
  local round_ok=1
  : > "$TMPDIR_RECOVER/final_states.txt"
  if [ "$CHECK_SOURCE" = "api" ]; then
    while IFS='|' read -r cid model; do
      [ -n "$cid" ] || continue
      enc=$(urlencode "$model")
      code=$(api GET "/api/credentials/$cid/models/$enc/state")
      if [ "$code" = "503" ]; then
        CHECK_SOURCE=db
        say "   ⓘ /state 端点 503（stateManager 未启用）⇒ check 降级为 DB 直读 node_probe_state"
        break
      fi
      if [ "$code" = "200" ]; then
        avail=$(last_json_field state.available)
        hs=$(last_json_field state.health_status)
        if [ "$avail" = "True" ]; then
          say "   ✓ cred=$cid model=$model → OK($hs)"
          printf '%s|%s|OK|%s\n' "$cid" "$model" "$hs" >> "$TMPDIR_RECOVER/final_states.txt"
        else
          say "   … cred=$cid model=$model → DOWN($hs)"
          printf '%s|%s|DOWN|%s\n' "$cid" "$model" "$hs" >> "$TMPDIR_RECOVER/final_states.txt"
          round_ok=0
        fi
      else
        say "   … cred=$cid model=$model → HTTP $code"
        printf '%s|%s|HTTP%s|-\n' "$cid" "$model" "$code" >> "$TMPDIR_RECOVER/final_states.txt"
        round_ok=0
      fi
    done < "$TMPDIR_RECOVER/pairs.txt"
  fi
  if [ "$CHECK_SOURCE" = "db" ]; then
    local ids
    ids=$(awk -F'|' '{print $1}' "$TMPDIR_RECOVER/pairs.txt" | sort -un | paste -sd, -)
    [ -n "$ids" ] || return 0
    db_query "SELECT credential_id || '|' || raw_model_name || '|' || consecutive_failures
FROM node_probe_state WHERE credential_id = ANY(ARRAY[$ids])" \
      > "$TMPDIR_RECOVER/db_state.txt" 2>/dev/null
    while IFS='|' read -r cid model; do
      [ -n "$cid" ] || continue
      fails=$(awk -F'|' -v c="$cid" -v m="$model" '$1==c && $2==m {print $3; found=1} END{if (!found) print ""}' "$TMPDIR_RECOVER/db_state.txt")
      if [ -z "$fails" ]; then
        say "   ✓ cred=$cid model=$model → OK(未跟踪)"
        printf '%s|%s|OK|untracked\n' "$cid" "$model" >> "$TMPDIR_RECOVER/final_states.txt"
      elif [ "$fails" = "0" ]; then
        say "   ✓ cred=$cid model=$model → OK(fails=0)"
        printf '%s|%s|OK|fails=0\n' "$cid" "$model" >> "$TMPDIR_RECOVER/final_states.txt"
      else
        say "   … cred=$cid model=$model → DOWN(fails=$fails)"
        printf '%s|%s|DOWN|fails=%s\n' "$cid" "$model" "$fails" >> "$TMPDIR_RECOVER/final_states.txt"
        round_ok=0
      fi
    done < "$TMPDIR_RECOVER/pairs.txt"
  fi
  return $(( 1 - round_ok ))
}

while :; do
  if check_round; then
    say "── ✓✓ 全部恢复（$PAIRS 个对）"
    exit 0
  fi
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep "$INTERVAL_SEC"
done

say "── ⚠ 超时（${WAIT_SEC}s）仍有未恢复对；最终态："
grep -v '|OK|' "$TMPDIR_RECOVER/final_states.txt" | sed 's/^/   /' || true
exit 1

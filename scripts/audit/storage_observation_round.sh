#!/usr/bin/env bash
# storage_observation_round.sh — S2→S4 停写 gate 观察期每日对账一轮
#
# 用法：
#   scripts/audit/storage_observation_round.sh [--dsn "$DATABASE_URL"] [--gateway http://127.0.0.1:8782]
#
# 流程：分层抽样（业务多轮/回环单轮/sys 合成）→ dual-read 端点逐会话对账
#       → G2/G3 硬指标在 SQL 侧分类（端点 only_* 不分终态/近期）→ 打印 PASS/FAIL 判定行。
# 输出为台账可追加格式；结果由操作者粘贴到 docs/03-design/04-data-design/storage-observation-ledger.md。
#
# 依赖：psql、curl、python3；admin 凭据走环境变量 GW_ADMIN_USER/GW_ADMIN_PASSWORD
# （缺省 admin / bin/current/env 的 LLM_GATEWAY_ADMIN_PASSWORD）。
set -euo pipefail

DSN="${LLM_GATEWAY_DSN:-}"
GW="${GW_BASE_URL:-http://127.0.0.1:8782}"
BATCH="${GW_BATCH:-10}"
if [[ -z "$DSN" ]]; then
  echo "error: set LLM_GATEWAY_DSN (e.g. postgres://llm_gateway:...@127.0.0.1:5432/llm_gateway)" >&2
  exit 1
fi
if [[ -z "${GW_ADMIN_PASSWORD:-}" ]] && [[ -f ~/kaixuan/llm-gateway-go/bin/current/env ]]; then
  GW_ADMIN_PASSWORD="$(sed -n 's/^LLM_GATEWAY_ADMIN_PASSWORD=//p' ~/kaixuan/llm-gateway-go/bin/current/env)"
fi
GW_ADMIN_USER="${GW_ADMIN_USER:-admin}"

psql_q() { psql "$DSN" -At -q -c "$1"; }

# 1. 分层抽样：多轮业务 / 单轮回环 / sys 合成（近 24h 有 turns）
BATCH_MULTI=$(( BATCH - 6 )); [[ $BATCH_MULTI -lt 1 ]] && BATCH_MULTI=1
psql_q "
(SELECT 'biz_multi|' || sid FROM (
   SELECT session_id AS sid, count(*) AS c FROM (
     SELECT session_id FROM public.session_turns_hot WHERE ts > now()-interval '24 hours' AND session_id NOT LIKE 'sys:%'
     UNION ALL SELECT session_id FROM public.session_turns      WHERE ts > now()-interval '24 hours' AND session_id NOT LIKE 'sys:%') x
   GROUP BY session_id HAVING count(*) >= 5 ORDER BY count(*) DESC LIMIT $BATCH_MULTI))
 UNION ALL
 (SELECT 'loop_single|' || sid FROM (
     SELECT session_id AS sid FROM (
       SELECT session_id FROM public.session_turns_hot WHERE ts > now()-interval '24 hours' AND session_id NOT LIKE 'sys:%'
       UNION ALL SELECT session_id FROM public.session_turns      WHERE ts > now()-interval '24 hours' AND session_id NOT LIKE 'sys:%') x
     GROUP BY session_id HAVING count(*) = 1 LIMIT 3))
 UNION ALL
 (SELECT 'sys|' || sid FROM (
     SELECT session_id AS sid, count(*) AS c FROM (
       SELECT session_id FROM public.session_turns_hot WHERE ts > now()-interval '24 hours' AND session_id LIKE 'sys:%'
       UNION ALL SELECT session_id FROM public.session_turns      WHERE ts > now()-interval '24 hours' AND session_id LIKE 'sys:%') x
     GROUP BY session_id ORDER BY count(*) DESC LIMIT 3))
" > /tmp/obs_round_sessions.$$

# 2. admin token
TOKEN="$(curl -s -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$GW_ADMIN_USER\",\"password\":\"$GW_ADMIN_PASSWORD\"}" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')"

# 3. 逐会话端点对账 + G2/G3 分类
FAIL=0; TOTAL=0
printf 'sid|class|v1|v2|only_v1|only_v2|G1(tok,cost,succ,cred)|G2_final_missing|G3_recent_v2only|verdict\n'
while IFS='|' read -r cls sid; do
  TOTAL=$((TOTAL+1))
  JSON="$(curl -s "$GW/api/admin/sessions/$sid/dual-read?limit=5" -H "Authorization: Bearer $TOKEN")"

  # G2：only_in_v1 里终态行数；G3：only_in_v2 里 48h 内行数（SQL 分类）
  IFS='|' read -r G2 G3 < <(psql_q "
    WITH v2ids AS (
      SELECT request_id FROM public.session_turns_hot WHERE session_id='$sid'
      UNION SELECT request_id FROM public.session_turns WHERE session_id='$sid'),
    v1only AS (
      SELECT request_id, is_final_success, ts FROM (
        SELECT request_id, is_final_success, ts FROM public.request_logs_hot WHERE gw_session_id='$sid'
        UNION ALL SELECT request_id, is_final_success, ts FROM public.request_logs WHERE gw_session_id='$sid') a
      WHERE request_id IS NOT NULL AND request_id NOT IN (SELECT request_id FROM v2ids WHERE request_id IS NOT NULL)),
    v1ids AS (SELECT request_id, bool_or(is_final_success) AS f, max(ts) AS ts FROM v1only GROUP BY request_id),
    v2only AS (
      SELECT request_id, max(ts) AS ts FROM (
        SELECT request_id, ts FROM public.session_turns_hot WHERE session_id='$sid'
        UNION ALL SELECT request_id, ts FROM public.session_turns WHERE session_id='$sid') b
      WHERE request_id IS NOT NULL AND request_id NOT IN (SELECT request_id FROM (
        SELECT request_id FROM public.request_logs_hot WHERE gw_session_id='$sid'
        UNION SELECT request_id FROM public.request_logs WHERE gw_session_id='$sid') c WHERE request_id IS NOT NULL)
      GROUP BY request_id)
    SELECT
      (SELECT count(*) FROM v1ids WHERE f IS TRUE),
      (SELECT count(*) FROM v2only WHERE ts > now()-interval '48 hours');")

  LINE="$(echo "$JSON" | python3 -c '
import sys,json
d=json.load(sys.stdin)
g1="%s,%s,%s,%s" % (d["token_drift_count"],d["cost_drift_count"],d["success_drift_count"],d["credits_drift_count"])
print("%s|%s|%s|%s|%s" % (d["v1_rows"],d["v2_rows"],d["only_in_v1_count"],d["only_in_v2_count"],g1))')"

  VERDICT=PASS
  if [[ "$cls" == "sys" ]]; then
    # E3 例外：sys 合成会话 v1_rows=0 属 D4 结构设计，G2/G3 豁免，只看 G1
    if ! echo "$LINE" | grep -q '|0,0,0,0$'; then
      VERDICT=FAIL; FAIL=$((FAIL+1))
    fi
  elif [[ "$G2" != "0" || "$G3" != "0" ]] || ! echo "$LINE" | grep -q '|0,0,0,0$'; then
    VERDICT=FAIL; FAIL=$((FAIL+1))
  fi
  printf '%s|%s|%s|%s|%s|%s\n' "$sid" "$cls" "$LINE" "$G2" "$G3" "$VERDICT"
done < /tmp/obs_round_sessions.$$
rm -f /tmp/obs_round_sessions.$$

echo "ROUND_RESULT|sessions=$TOTAL|fail=$FAIL|verdict=$([[ $FAIL -eq 0 ]] && echo PASS || echo FAIL)|at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

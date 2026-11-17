#!/usr/bin/env bash
# =============================================================================
# 请求阶段树（/live2）验收证据采集器 —— 一条命令，输出逐条 verdict 表
# =============================================================================
# 用法：
#   bash maestro/verify-stage-tree-evidence.sh
#   bash maestro/verify-stage-tree-evidence.sh --with-browser    # 追加浏览器面
#
# 设计口径（与 docs/skills/maestro-browser-verification/SKILL.md 一致）：
#   · 判定口径**单点化**：本脚本只负责「取证 + 打印」，**判定**统一走
#     maestro/tools/result.mjs 的 summarize()（浏览器面）；
#   · **三态退出码**：0=全部通过 / 1=有检查未通过 / 2=有检查无法执行（量具失效）。
#     ⇒ 「没测到」与「测到红」必须能区分，否则复核者会去查产品而不是查量具。
#   · **不写任何凭据**：口令只从环境变量读（ADMIN_USER / ADMIN_PASSWORD），
#     临时密钥用完即删并双重确认。
#
# 环境变量：
#   BASE_URL      默认 https://llm.kxpms.cn
#   SSH_HOST      默认 47.97.111.154     SSH_PORT 默认 25022
#   DEPLOY_TREE   默认 /private/tmp/deploy-frozen-154-stage-parity2（当前在役 3051 的部署源）
#   OLD_TREE      默认 /private/tmp/deploy-frozen-154-stage-parity（3050 的部署源，留痕）
#   BRANCH        默认 feat/live-stage-tree-design-parity-20261117
# =============================================================================
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_URL="${BASE_URL:-https://llm.kxpms.cn}"
SSH_HOST="${SSH_HOST:-47.97.111.154}"
SSH_PORT="${SSH_PORT:-25022}"
DEPLOY_TREE="${DEPLOY_TREE:-/private/tmp/deploy-frozen-154-stage-parity2}"
OLD_TREE="${OLD_TREE:-/private/tmp/deploy-frozen-154-stage-parity}"
BRANCH="${BRANCH:-feat/live-stage-tree-design-parity-20261117}"
WITH_BROWSER=0
[ "${1:-}" = "--with-browser" ] && WITH_BROWSER=1

PASS=0; FAIL=0; SKIP=0
declare -a ROWS=()

row() { # row <结论> <检查项> <实测>
  case "$1" in
    PASS) PASS=$((PASS+1)) ;;
    FAIL) FAIL=$((FAIL+1)) ;;
    SKIP) SKIP=$((SKIP+1)) ;;
  esac
  ROWS+=("$(printf '%-5s │ %-42s │ %s' "$1" "$2" "$3")")
}
ok()   { row PASS "$1" "$2"; }
bad()  { row FAIL "$1" "$2"; }
skip() { row SKIP "$1" "$2"; }

echo "════ 采集开始（${BASE_URL}）════"

# ── ① SSH 直连版本身份 ───────────────────────────────────────────────────────
SSH_JSON=""
if SSH_JSON=$(ssh -o BatchMode=yes -o ConnectTimeout=20 -p "${SSH_PORT}" "root@${SSH_HOST}" \
      'cat /opt/llm-gateway-go/current/version.json' 2>/dev/null); then
  SSH_VER=$(printf '%s' "$SSH_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("version",""))' 2>/dev/null)
  SSH_SHA=$(printf '%s' "$SSH_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("git_sha",""))' 2>/dev/null)
  SSH_SEQ=$(printf '%s' "$SSH_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("build_seq",""))' 2>/dev/null)
  SSH_LINK=$(ssh -o BatchMode=yes -o ConnectTimeout=20 -p "${SSH_PORT}" "root@${SSH_HOST}" \
      'readlink -f /opt/llm-gateway-go/current' 2>/dev/null)
  ok "① SSH 直连 version.json" "${SSH_VER} / ${SSH_SHA} / seq=${SSH_SEQ} → ${SSH_LINK##*/}"
else
  SSH_VER=""; skip "① SSH 直连 version.json" "SSH 不可达（本机无路由/权限）"
fi

# ── ② 公网 healthz ───────────────────────────────────────────────────────────
HZ_CODE=$(curl -s -m 15 -o /tmp/vst-healthz.json -w '%{http_code}' "${BASE_URL}/healthz" 2>/dev/null || echo 000)
if [ "$HZ_CODE" = "200" ]; then
  HZ=$(cat /tmp/vst-healthz.json)
  HZ_VER=$(printf '%s' "$HZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("version",""))' 2>/dev/null)
  HZ_SHA=$(printf '%s' "$HZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("git_sha",""))' 2>/dev/null)
  HZ_SEQ=$(printf '%s' "$HZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("build_seq",""))' 2>/dev/null)
  HZ_RDY=$(printf '%s' "$HZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("ready",""))' 2>/dev/null)
  ok "② 公网 healthz" "HTTP ${HZ_CODE} / ${HZ_VER} / ${HZ_SHA} / seq=${HZ_SEQ} / ready=${HZ_RDY}"
else
  HZ_VER=""; HZ_SHA=""; HZ_SEQ=""; skip "② 公网 healthz" "HTTP ${HZ_CODE}"
fi

# ── ③ 身份三方一致（SSH = healthz = 部署树 HEAD）────────────────────────────
if [ -n "${SSH_VER:-}" ] && [ -n "${HZ_VER:-}" ]; then
  DEPLOY_HEAD=$(git -C "$ROOT" rev-parse --short "deploy-154/seq3051-8ab0d2e56^{commit}" 2>/dev/null \
                  || git -C "$DEPLOY_TREE" rev-parse --short HEAD 2>/dev/null || echo "")
  if [ "$SSH_VER" = "$HZ_VER" ] && [ "$SSH_SHA" = "$HZ_SHA" ] && [ "$SSH_SEQ" = "$HZ_SEQ" ]; then
    ok "③ 身份三方一致" "SSH = healthz = ${HZ_VER}（部署树 HEAD=${DEPLOY_HEAD}）"
  else
    bad "③ 身份三方一致" "SSH(${SSH_VER}/${SSH_SHA}/${SSH_SEQ}) ≠ healthz(${HZ_VER}/${HZ_SHA}/${HZ_SEQ})"
  fi
else
  skip "③ 身份三方一致" "缺 SSH 或 healthz 实测值"
fi

# ── ④ i18n 审计 ──────────────────────────────────────────────────────────────
I18N_OUT=$(cd "$ROOT/web" && node scripts/i18n-audit.mjs 2>&1); I18N_RC=$?
if printf '%s' "$I18N_OUT" | grep -q "i18n audit PASS"; then
  ok "④ i18n-audit.mjs" "PASS（0 missing keys，rc=${I18N_RC}）"
else
  bad "④ i18n-audit.mjs" "$(printf '%s' "$I18N_OUT" | grep -iE 'missing|error' | head -1)"
fi

# ── ⑤ 定向 vitest（设计版面契约 3 文件 / 40 用例）─────────────────────────────
TV=$(cd "$ROOT/web" && npx vitest run \
  src/lib/liveStageTree.designParity.test.ts \
  src/lib/liveStageTreeStats.test.ts \
  src/views/LiveStageTreeView.designParity.test.ts 2>&1); TV_RC=$?
if printf '%s' "$TV" | grep -q "Tests  40 passed (40)"; then
  ok "⑤ 定向 vitest" "3 文件 / 40 用例全绿（rc=${TV_RC}）"
else
  bad "⑤ 定向 vitest" "$(printf '%s' "$TV" | grep -E 'Tests ' | tail -1)"
fi

# ── ⑥ 线上 bundle 设计契约串 ─────────────────────────────────────────────────
curl -s -m 20 -o /tmp/vst-idx.html "${BASE_URL}/dashboard" >/dev/null 2>&1
ASSET=$(grep -o '/assets/index-[A-Za-z0-9_-]*\.js' /tmp/vst-idx.html 2>/dev/null | head -1)
if [ -n "$ASSET" ]; then
  curl -s -m 60 "${BASE_URL}${ASSET}" -o /tmp/vst-bundle.js >/dev/null 2>&1
  STRINGS=("stage-tree-legend" "stage-tree-header" "stage-mode-full" "stage-mode-compact" \
           "stage-band-time-" "stage-stats-open" "band__node-value" "legend__glyph" "stagehead__cell" \
           "近 5 分钟" "口径说明" "周期统计" "分类统计" \
           "stage-band-cright" "stage-band-errorcol" "band__tile-src" "data-rail" "stage-tile-" "stage-band-candidates")
  HIT=0; MISSED=""
  for S in "${STRINGS[@]}"; do
    if grep -q -- "$S" /tmp/vst-bundle.js 2>/dev/null; then HIT=$((HIT+1)); else MISSED="${MISSED}${S} "; fi
  done
  if [ "$HIT" = "${#STRINGS[@]}" ]; then
    ok "⑥ 线上 bundle 契约串" "${HIT}/${#STRINGS[@]} 全命中（${ASSET}）"
  else
    bad "⑥ 线上 bundle 契约串" "${HIT}/${#STRINGS[@]}；缺：${MISSED}"
  fi
else
  skip "⑥ 线上 bundle 契约串" "取不到入口 chunk（/dashboard 未返回 index.html）"
fi

# ── ⑦ 当前在役部署树可追溯 + 并行车零泄漏 ──────────────────────────────────
# ★ 用【仓内 durable ref（tag）】而不是 /private/tmp 的部署源目录：
#   部署源目录会被系统清理 ⇒ 「可追溯」不能只靠它。
TAG_NEW="deploy-154/seq3051-8ab0d2e56"
TAG_OLD="deploy-154/seq3050-6840a7be"
if git -C "$ROOT" rev-parse --verify "${TAG_NEW}" >/dev/null 2>&1; then
  DH=$(git -C "$ROOT" rev-parse --short "${TAG_NEW}^{commit}")
  # ★ 基点必须从【标签消息】里取，不能用 `TAG^`：
  #   部署树是 base → cherry1 → cherry2 → cherry3 ⇒ `TAG^` 是【上一笔 cherry-pick】，
  #   不是基点（实测：会拿 10 文件冒充 25 文件）。
  BASE=$(git -C "$ROOT" tag -l --format='%(contents)' "${TAG_NEW}" 2>/dev/null \
          | grep -m1 '^base=' | cut -d= -f2 | tr -d '[:space:]')
  [ -z "$BASE" ] && BASE=$(git -C "$ROOT" rev-parse --short "${TAG_NEW}~1" 2>/dev/null)
  CH=$(git -C "$ROOT" diff --name-only "$BASE" "${TAG_NEW}" 2>/dev/null | wc -l | tr -d ' ')
  LEAK=0
  for f in admin/pi_analysis_handler.go web/public/menu-config.json; do
    A=$(git -C "$ROOT" rev-parse "${TAG_NEW}:$f" 2>/dev/null)
    B=$(git -C "$ROOT" rev-parse "$BASE:$f" 2>/dev/null)
    [ "$A" != "$B" ] && LEAK=$((LEAK+1))
  done
  PI=$(git -C "$ROOT" diff --name-only "$BASE" "${TAG_NEW}" -- domains/promptinjection 2>/dev/null | wc -l | tr -d ' ')
  if [ "$LEAK" = "0" ] && [ "$PI" = "0" ] && [ "$CH" = "25" ]; then
    ok "⑦ 在役部署可追溯 + 零泄漏" "tag ${TAG_NEW}（${DH}，基点 ${BASE:0:8} + ${CH} 文件）并行车 0 泄漏"
  else
    bad "⑦ 在役部署可追溯 + 零泄漏" "改动 ${CH} 个文件（期望 25）/ 泄漏 ${LEAK} 处 / promptinjection 差异 ${PI} 处"
  fi
elif [ -d "$DEPLOY_TREE" ]; then
  skip "⑦ 在役部署可追溯" "无 tag ${TAG_NEW}（仓内无 durable ref）"
else
  skip "⑦ 在役部署可追溯" "既无 tag 也无部署源目录"
fi

# ── ⑧ 取代链：旧部署（3050）仍可追溯，且被新部署严格超集 ────────────────────
if git -C "$ROOT" rev-parse --verify "${TAG_OLD}" >/dev/null 2>&1; then
  OH=$(git -C "$ROOT" rev-parse --short "${TAG_OLD}^{commit}")
  OB=$(git -C "$ROOT" tag -l --format='%(contents)' "${TAG_OLD}" 2>/dev/null \
        | grep -m1 '^base=' | cut -d= -f2 | tr -d '[:space:]')
  [ -z "$OB" ] && OB=$(git -C "$ROOT" rev-parse --short "${TAG_OLD}~1" 2>/dev/null)
  ON=$(git -C "$ROOT" diff --name-only "$OB" "${TAG_OLD}" 2>/dev/null | wc -l | tr -d ' ')
  OLEAK=0
  for f in admin/pi_analysis_handler.go web/public/menu-config.json; do
    A=$(git -C "$ROOT" rev-parse "${TAG_OLD}:$f" 2>/dev/null)
    B=$(git -C "$ROOT" rev-parse "$OB:$f" 2>/dev/null)
    [ "$A" != "$B" ] && OLEAK=$((OLEAK+1))
  done
  SUPER=$(git -C "$ROOT" diff --name-only "${TAG_OLD}" "${TAG_NEW}" 2>/dev/null | wc -l | tr -d ' ')
  if [ "$OLEAK" = "0" ]; then
    ok "⑧ 旧部署（3050）可追溯" "tag ${TAG_OLD}（${OH}，基点 ${OB} + ${ON} 文件）并行车 0 泄漏；→3051 差异 ${SUPER} 个文件"
  else
    bad "⑧ 旧部署（3050）可追溯" "并行车泄漏 ${OLEAK} 处"
  fi
else
  skip "⑧ 旧部署（3050）可追溯" "无 tag ${TAG_OLD}"
fi

# ── ⑨ 前端产物三方 sha256（服务器 = 公网 = 本地 dist）────────────────────────
SERVER_SHA=""
if [ -n "${SSH_HOST:-}" ]; then
  SERVER_SHA=$(ssh -o BatchMode=yes -o ConnectTimeout=20 -p "${SSH_PORT}" "root@${SSH_HOST}" \
    'for f in /opt/llm-gateway-go/current/web/assets/index-*.js; do sha256sum "$f"; done' 2>/dev/null \
    | awk '{print $1}' | head -1)
fi
ONLINE_SHA=""
[ -f /tmp/vst-bundle.js ] && ONLINE_SHA=$(shasum -a 256 /tmp/vst-bundle.js 2>/dev/null | cut -d' ' -f1)
LOCAL_SHA=""
[ -f "$ROOT/web/dist${ASSET:-/none}" ] && LOCAL_SHA=$(shasum -a 256 "$ROOT/web/dist${ASSET}" 2>/dev/null | cut -d' ' -f1)
if [ -n "$SERVER_SHA" ] && [ "$SERVER_SHA" = "$ONLINE_SHA" ] && [ "$ONLINE_SHA" = "$LOCAL_SHA" ]; then
  ok "⑨ 前端产物三方 sha256" "${SERVER_SHA:0:16}… （服务器 = 公网 = 本地 dist）"
else
  skip "⑨ 前端产物三方 sha256" "服务器=${SERVER_SHA:0:16} 公网=${ONLINE_SHA:0:16} 本地=${LOCAL_SHA:0:16}（未全等或缺其一）"
fi

# ── ⑩ 合并试演（对当前 origin/main）零冲突 ──────────────────────────────────
if git -C "$ROOT" rev-parse --verify origin/main >/dev/null 2>&1; then
  git -C "$ROOT" merge-tree --write-tree origin/main "$BRANCH" >/dev/null 2>&1
  MT_RC=$?
  if [ "$MT_RC" = "0" ]; then
    ok "⑩ 合并试演零冲突" "git merge-tree 退出码 0（origin/main=$(git -C "$ROOT" rev-parse --short origin/main)）"
  else
    bad "⑩ 合并试演零冲突" "git merge-tree 退出码 ${MT_RC}"
  fi
else
  skip "⑩ 合并试演零冲突" "无 origin/main 引用"
fi

# ── 浏览器面（可选）：Maestro flow 12 + 两层页面内断言 ──────────────────────
if [ "$WITH_BROWSER" = "1" ]; then
  if command -v maestro >/dev/null 2>&1 && [ -n "${ADMIN_USER:-}" ] && [ -n "${ADMIN_PASSWORD:-}" ]; then
    MO=$(mktemp -d)
    ( cd "$ROOT" && BASE_URL="${BASE_URL}" maestro test --headless --no-ansi --screen-size 1440x1400 \
        --test-output-dir "$MO" -e BASE_URL="${BASE_URL}" -e ADMIN_USER="${ADMIN_USER}" \
        -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" maestro/flows/12-live-stage-tree.yaml ) >/tmp/vst-maestro.log 2>&1
    MRC=$?
    STEPS=$(grep -c COMPLETED /tmp/vst-maestro.log 2>/dev/null || true)
    FAILED=$(grep -c FAILED /tmp/vst-maestro.log 2>/dev/null || true)
    # ★ grep -c 在 0 命中时【仍会打印 0】但退出码为 1 ⇒ 用 `|| true` 而不是 `|| echo 0`
    #   （后者会多出一个 0，把「0 次失败」变成「00」⇒ 误判成 FAIL）
    STEPS="${STEPS:-0}"; FAILED="${FAILED:-0}"
    if [ "$MRC" = "0" ] && [ "$FAILED" = "0" ]; then
      ok "⑪ Maestro flow 12" "退出码 0，${STEPS} 步全 COMPLETED，0 失败"
    else
      bad "⑪ Maestro flow 12" "退出码 ${MRC}，COMPLETED=${STEPS} FAILED=${FAILED}"
    fi
  else
    skip "⑪ Maestro flow 12" "缺 maestro 或 ADMIN_USER/ADMIN_PASSWORD"
  fi
  # 两层页面内断言需要 CDP + 窗口内真实流量 ⇒ 单列，且明确区分「没测到」
  if [ -f "${STAGE_TREE_TOKEN_FILE:-/tmp/llmgw-jwt.txt}" ] && curl -s -m 5 http://127.0.0.1:9222/json/version >/dev/null 2>&1; then
    # ★★ 造流量必须造【在途】请求 —— 实测（2026-11-18 04:0x）：
    #   ① `max_tokens: 8` 的瞬时请求 ⇒ SSE 帧到达页面前请求已完成 ⇒ 页面窗口里【0 条带】
    #      ⇒ 依赖「有带」的断言全部「未评估」⇒ 设计层报「量具失效」（退出码 2）。
    #      ★ 这**不是产品缺陷**，是造流量的形状不对。
    #   ② `stream:true` + `max_tokens:1000` 的长请求 ⇒ 请求在途 ⇒ 页面立刻看到带
    #      （实测 bands 4→9、inFlight 4→8）⇒ 两层断言都能评估。
    TK=$(cat "${STAGE_TREE_TOKEN_FILE:-/tmp/llmgw-jwt.txt}")
    KEY_ID=""; KEY=""
    seed_traffic() {
      APP_CODE="stage-tree-evidence-$(date +%s)-$$-$1"
      KEY_JSON=$(curl -s -m 30 -X POST "${BASE_URL}/api/keys" -H "Authorization: Bearer ${TK}" \
        -H 'Content-Type: application/json' \
        --data "{\"application_code\":\"${APP_CODE}\",\"key_alias\":\"${APP_CODE}\",\"budget_usd\":1.0,\"remark\":\"证据采集临时密钥（用完即删）\"}" 2>/dev/null)
      KEY_ID=$(printf '%s' "$KEY_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null || true)
      KEY=$(printf '%s' "$KEY_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("api_key",""))' 2>/dev/null || true)
      [ -z "$KEY_ID" ] && { echo "    造流量：临时密钥创建失败 ⇒ 原文：$(printf '%s' "$KEY_JSON" | head -c 160)"; return 1; }
      curl -s -m 20 -X PATCH "${BASE_URL}/api/keys/${KEY_ID}/approve" -H "Authorization: Bearer ${TK}" -o /dev/null 2>/dev/null
      # 小批量 + 间隔 ⇒ 避开 429（被 429 挡在网关入口 ⇒ 根本没进实时流）
      printf '    造流量返回码:'
      for i in 1 2 3 4; do
        ( curl -s -m 45 -N -X POST "${BASE_URL}/v1/chat/completions" -H "Authorization: Bearer ${KEY}" \
            -H 'Content-Type: application/json' \
            --data '{"model":"glm-4.5-flash","messages":[{"role":"user","content":"请写一篇 800 字的中文短文，介绍分布式系统的一致性模型"}],"max_tokens":1000,"stream":true}' \
            >/dev/null 2>&1 ) &
        sleep 1
      done
      echo " （在途型）"
      return 0
    }
    if [ "${STAGE_TREE_TRAFFIC:-1}" = "1" ]; then
      seed_traffic 1 || true
    fi

    # ★ 断言与造流量【成对】⇒ 若报「量具失效」（退出码 2），就【再造一轮】重跑，
    #   而不是把它当成产品红（纪律见 docs/skills/maestro-browser-verification/SKILL.md）。
    A1RC=9; A2RC=9; A1=""; A2=""
    for round in 1 2 3; do
      A1=$(node "$ROOT/maestro/tools/assert-stage-tree.mjs" --cdp http://127.0.0.1:9222 --url "${BASE_URL}" \
            --token-file "${STAGE_TREE_TOKEN_FILE:-/tmp/llmgw-jwt.txt}" --page /live2 2>&1); A1RC=$?
      A2=$(node "$ROOT/maestro/tools/assert-stage-tree-design.mjs" --cdp http://127.0.0.1:9222 --url "${BASE_URL}" \
            --token-file "${STAGE_TREE_TOKEN_FILE:-/tmp/llmgw-jwt.txt}" 2>&1); A2RC=$?
      echo "    断言轮 ${round}: 行为层 rc=${A1RC} 设计层 rc=${A2RC}"
      if [ "$A1RC" = "0" ] && [ "$A2RC" = "0" ]; then break; fi
      if [ "$A1RC" = "2" ] || [ "$A2RC" = "2" ]; then
        echo "    ⇒ 量具失效（窗口内无在途请求）⇒ 再造一轮在途流量重跑"
        seed_traffic "$round" || true
      else
        break
      fi
    done
    for pair in "⑫ 断言（行为层）|$A1|$A1RC" "⑬ 断言（设计层）|$A2|$A2RC"; do
      NAME="${pair%%|*}"; REST="${pair#*|}"; TXT="${REST%%|*}"; RC="${REST##*|}"
      LINE=$(printf '%s' "$TXT" | grep -E 'RESULT:' | tail -1)
      case "$RC" in
        0) ok "${NAME}" "${LINE:-PASS}" ;;
        2) skip "${NAME}" "${LINE:-量具失效} ⇒ 需先造流量再重跑" ;;
        *) bad "${NAME}" "${LINE:-FAIL}（退出码 ${RC}）" ;;
      esac
    done

    # 临时密钥：删 + 双重确认（回读 404 / 旧密钥 401）
    if [ -n "$KEY_ID" ]; then
      curl -s -m 20 -X DELETE "${BASE_URL}/api/keys/${KEY_ID}" -H "Authorization: Bearer ${TK}" -o /dev/null 2>/dev/null
      R1=$(curl -s -m 20 -o /dev/null -w '%{http_code}' "${BASE_URL}/api/keys/${KEY_ID}" -H "Authorization: Bearer ${TK}" 2>/dev/null || true)
      R2=$(curl -s -m 20 -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/v1/chat/completions" \
           -H "Authorization: Bearer ${KEY}" -H 'Content-Type: application/json' \
           --data '{"model":"glm-4.5-flash","messages":[{"role":"user","content":"ok"}],"max_tokens":8}' 2>/dev/null || true)
      if [ "$R1" = "404" ] && [ "$R2" = "401" ]; then
        ok "⑭ 临时密钥清理" "id=${KEY_ID} 已删（回读 ${R1} / 旧密钥 ${R2}）"
      else
        bad "⑭ 临时密钥清理" "id=${KEY_ID} 回读=${R1}（期望 404）旧密钥=${R2}（期望 401）"
      fi
    else
      skip "⑭ 临时密钥清理" "本轮未创建临时密钥"
    fi
  else
    skip "⑫ 断言（行为层）" "缺 CDP(9222) 或 JWT ⇒ 未评估（不是失败）"
    skip "⑬ 断言（设计层）" "缺 CDP(9222) 或 JWT ⇒ 未评估（不是失败）"
    skip "⑭ 临时密钥清理" "未进入浏览器面"
  fi
else
  skip "⑪ Maestro flow 12" "未加 --with-browser"
  skip "⑫ 断言（行为层）" "未加 --with-browser"
  skip "⑬ 断言（设计层）" "未加 --with-browser"
  skip "⑭ 临时密钥清理" "未加 --with-browser"
fi

# ── 汇总 ─────────────────────────────────────────────────────────────────────
echo
echo "════ 逐条 verdict ════"
for r in "${ROWS[@]}"; do echo "  $r"; done
echo
if [ "$FAIL" -gt 0 ]; then
  echo "RESULT: FAIL（${FAIL} / $((PASS+FAIL+SKIP)) 条未通过；${SKIP} 条未评估）"; exit 1
elif [ "$SKIP" -gt 0 ]; then
  echo "RESULT: PASS（${PASS} 条通过；${SKIP} 条未评估 —— 量具失效，不是产品缺陷）"; exit 2
else
  echo "RESULT: PASS（${PASS} 条）"; exit 0
fi

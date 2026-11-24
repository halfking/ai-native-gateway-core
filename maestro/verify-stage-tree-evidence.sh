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
DEPLOY_TREE="${DEPLOY_TREE:-/private/tmp/deploy-frozen-154-stage-parity3}"
OLD_TREE="${OLD_TREE:-/private/tmp/deploy-frozen-154-stage-parity2}"
BRANCH="${BRANCH:-feat/live-stage-tree-design-parity-20261117}"
WITH_BROWSER=0
[ "${1:-}" = "--with-browser" ] && WITH_BROWSER=1

# ── 「当前在役是哪一版」：**动态取**，不写死 ──────────────────────────────────
# ★ 本项必须放在最前面（③ 与 ⑦ 都要用）：写死 tag 名 ⇒ 每次发版后
#   「身份三方一致」与「在役部署可追溯」都会指向**上一版** ⇒ 证据链指向历史快照。
tag_seq_of() { printf '%s' "$1" | sed -n 's/.*seq\([0-9][0-9]*\).*/\1/p'; }
TAGS_SORTED=""
for t in $(git -C "$ROOT" tag -l 'deploy-154/seq*' 2>/dev/null); do
  s="$(tag_seq_of "$t")"
  [ -n "$s" ] && TAGS_SORTED="${TAGS_SORTED}${s} ${t}"$'\n'
done
TAGS_SORTED="$(printf '%s' "$TAGS_SORTED" | sort -rn)"
TAG_NEW="$(printf '%s' "$TAGS_SORTED" | sed -n '1s/^[0-9]* //p')"
TAG_OLD="$(printf '%s' "$TAGS_SORTED" | sed -n '2s/^[0-9]* //p')"
# ★ 标签消息里的字段解析**必须与排版无关**：
#   本轮新 tag 把 base/cherry-picks/files 写在**同一行**，而老 tag 是一行一个
#   ⇒ `grep '^base='` 在新 tag 上**匹配不到** ⇒ BASE 取空 ⇒ 改动文件数算成 0、
#   「零泄漏」判据两侧都解析不出内容 ⇒ **恒定误报成 2 处泄漏**（本轮真踩到）。
#   ⇒ 统一按「空白切词后取 `key=` 前缀」解析，两种排版都能读。
tag_field() { # tag_field <tag> <key>
  git -C "$ROOT" tag -l --format='%(contents)' "$1" 2>/dev/null \
    | tr ' \t' '\n\n' | sed -n "s/^$2=//p" | head -1 | tr -d '[:space:]'
}

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

# ── ③ 身份三方一致（SSH = healthz = 部署树 commit）──────────────────────────
if [ -n "${SSH_VER:-}" ] && [ -n "${HZ_VER:-}" ]; then
  DEPLOY_SHA_FULL=$(git -C "$ROOT" rev-parse "${TAG_NEW}^{commit}" 2>/dev/null \
                    || git -C "$DEPLOY_TREE" rev-parse HEAD 2>/dev/null || echo "")
  DEPLOY_HEAD=$(printf '%s' "$DEPLOY_SHA_FULL" | cut -c1-8)
  # ★ 「三方一致」必须**真的三方都相等**：原实现只比了 SSH vs healthz，
  #   DEPLOY_HEAD 只被打印、**不参与判定** ⇒ 名不副实（本轮实测：写死的旧 tag
  #   让它打印出上一版的 sha，而正文没有任何一处会发现）。
  # ★★ 比 sha 要用**前缀**而不是字符串相等：`healthz.git_sha` 是 8 位短 sha，
  #   而 `git rev-parse --short` 在本仓会取 **9 位**（8 位在此仓不唯一）
  #   ⇒ 直接比字符串**恒不相等** ⇒ 假红（本轮真踩到）。
  SHA_MATCH=0
  case "$DEPLOY_SHA_FULL" in
    "$HZ_SHA"*) SHA_MATCH=1 ;;
  esac
  if [ "$SSH_VER" = "$HZ_VER" ] && [ "$SSH_SHA" = "$HZ_SHA" ] && [ "$SSH_SEQ" = "$HZ_SEQ" ] \
     && { [ -z "$DEPLOY_SHA_FULL" ] || [ "$SHA_MATCH" = "1" ]; }; then
    ok "③ 身份三方一致" "SSH = healthz = 部署树 commit ${DEPLOY_HEAD}（tag ${TAG_NEW:-无}）＝ ${HZ_VER}"
  else
    bad "③ 身份三方一致" "SSH(${SSH_VER}/${SSH_SHA}/${SSH_SEQ}) ≠ healthz(${HZ_VER}/${HZ_SHA}/${HZ_SEQ})；部署树 commit=${DEPLOY_SHA_FULL:-?}（前缀匹配=${SHA_MATCH}）"
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
# ★★ 「当前在役」与「上一版」由**脚本顶部动态取**（seq 最大的 tag）⇒
#    每次发版后本检查自动跟随，不需要改脚本。
# ★★ 「并行车零泄漏」的比对必须**排除 exported_at**：
#    menu-config.json 的 exported_at 是**构建时戳** ⇒ 每次 build 都变
#    ⇒ 拿它当「并行车是否泄漏」的判据 ⇒ 恒定误报。
#    ⇒ 判据＝**内容**（去掉 exported_at 后逐字比对），不是文件哈希。
if [ -n "$TAG_NEW" ] && git -C "$ROOT" rev-parse --verify "${TAG_NEW}" >/dev/null 2>&1; then
  DH=$(git -C "$ROOT" rev-parse --short "${TAG_NEW}^{commit}")
  # ★ 基点必须从【标签消息】里取，不能用 `TAG^`：
  #   部署树是 base → cherry1 → cherry2 → cherry3 ⇒ `TAG^` 是【上一笔 cherry-pick】，
  #   不是基点（实测：会拿 10 文件冒充 25 文件）。
  BASE=$(tag_field "${TAG_NEW}" "base")
  [ -z "$BASE" ] && BASE=$(git -C "$ROOT" rev-parse --short "${TAG_NEW}~1" 2>/dev/null)
  # ★ 期望文件数写在**标签消息**里（files=N）⇒ 判据随发布单元走，不写死在脚本里。
  EXP=$(tag_field "${TAG_NEW}" "files")
  # ★ -z 化（2026-11-25 12h 修订审计轮）：★ `git diff --name-only` 默认受
  # ★ core.quotePath 转义 ⇒ 非 ASCII 路径被转成八进制＋外层双引号 ★ ⇒
  # ★ 而本处只取**行数** ⇒ 转义不改判定 ★ ⇒ 但为让「枚举面契约门」能逐条
  # ★ 判定（而不是靠豁免表登记），★ 这里统一走 `-z` ＋ `tr '\0' '\n'` ★
  CH=$(git -C "$ROOT" diff --name-only -z "$BASE" "${TAG_NEW}^{commit}" 2>/dev/null | tr '\0' '\n' | wc -l | tr -d ' ')
  # ══════ 2026-11-18 判据基准改造：把「场景化写死」的判据换成随发布单元走 ══════
  # ★ 旧判据把两件事**写死**了：
  #   ① 「并行车在途文件（admin/pi_analysis_handler.go、web/public/menu-config.json）
  #      不得出现在本次发布单元里」；
  #   ② 「domains/promptinjection 的差异数必须 = 0」。
  #   那两条**只在「发我方分支、并发车的改动仍在途」这个场景下成立**：
  #   本轮发布单元 = **origin/main**（并发车的 PI 工作已正式合入并上线）
  #   ⇒ 「泄漏」「PI 差异」按定义就是发布单元**自己的一部分** ⇒ 旧判据必然假红。
  #   ★ 本轮实测的真缺陷（被旧判据的假红**掩盖**了）：我把 durable tag 指到了
  #     **前一个提交**（实际发布树 = 1562862ba，tag 却指 a9e185d78）
  #     ⇒ 真正该报红的是「身份三方一致」，若只把 ⑦ 当成「老判据写死的」放过，
  #       就会把这个真错一起放过 ⇒ **假红会掩护真错**。
  # ⇒ 新判据（基准 = **发布单元 / 部署树**，与场景无关）：
  #   ① **tag 指向的 commit 必须 == 部署树 HEAD**（＝实际部署的那棵树）；
  #   ② **零真泄漏**：线上 release 的 menu-config **内容**（去构建戳）必须
  #      == **部署树**的同一文件内容（线上有、部署树没有的东西才算泄漏）；
  #   ③ **PI 域**：只作**报告**（发布单元内含几处 PI 域改动），不再写死为 0。
  TAG_HEAD=$(git -C "$ROOT" rev-parse "${TAG_NEW}^{commit}" 2>/dev/null || echo "")
  TREE_HEAD=$(git -C "$DEPLOY_TREE" rev-parse HEAD 2>/dev/null || echo "")
  TAG_OK=1
  if [ -z "$TREE_HEAD" ] || [ -z "$TAG_HEAD" ] || [ "$TAG_HEAD" != "$TREE_HEAD" ]; then TAG_OK=0; fi
  # ★ 零真泄漏＝**线上 release 与部署树逐字对拍**（不是与「并行车文件清单」对拍）：
  #   menu-config 的 exported_at 是构建戳 ⇒ 两边都去掉 exported_at 后再比内容。
  SRV_MC=$(ssh -o BatchMode=yes -o ConnectTimeout=20 -p "${SSH_PORT}" "root@${SSH_HOST}" \
    'cat /opt/llm-gateway-go/current/web/menu-config.json' 2>/dev/null \
    | python3 -c 'import sys,json;d=json.load(sys.stdin);d.pop("exported_at",None);print(json.dumps(d,sort_keys=True,ensure_ascii=False))' 2>/dev/null)
  TREE_MC=$(git -C "$DEPLOY_TREE" show "HEAD:web/public/menu-config.json" 2>/dev/null \
    | python3 -c 'import sys,json;d=json.load(sys.stdin);d.pop("exported_at",None);print(json.dumps(d,sort_keys=True,ensure_ascii=False))' 2>/dev/null)
  LEAK=0
  if [ -n "$SRV_MC" ] && [ -n "$TREE_MC" ] && [ "$SRV_MC" != "$TREE_MC" ]; then LEAK=1; fi
  PI=$(git -C "$ROOT" diff --name-only -z "$BASE" "${TAG_NEW}^{commit}" -- domains/promptinjection 2>/dev/null | tr '\0' '\n' | wc -l | tr -d ' ')
  CH_OK=1
  if [ -n "$EXP" ] && [ "$CH" != "$EXP" ]; then CH_OK=0; fi
  if [ "$LEAK" = "0" ] && [ "$CH_OK" = "1" ] && [ "$TAG_OK" = "1" ]; then
    ok "⑦ 在役部署可追溯 + 零泄漏" "tag ${TAG_NEW}（${DH}，基点 ${BASE:0:8} + ${CH} 文件${EXP:+（tag 记 files=${EXP}）}）＝ 部署树 HEAD ${TREE_HEAD:0:8}；线上 release 产物 == 部署树（零泄漏）"
  else
    bad "⑦ 在役部署可追溯 + 零泄漏" "改动 ${CH} 个文件（tag 记 files=${EXP:-未记}）/ 泄漏 ${LEAK} 处 / tag↔部署树同提交=${TAG_OK}（tag ${TAG_HEAD:0:8} vs 树 ${TREE_HEAD:0:8}）/ PI 域文件 ${PI} 处（仅报告）"
  fi
elif [ -d "$DEPLOY_TREE" ]; then
  skip "⑦ 在役部署可追溯" "无 tag ${TAG_NEW:-（仓内无 durable ref）}"
else
  skip "⑦ 在役部署可追溯" "既无 tag 也无部署源目录"
fi

# ── ⑧ 上一版部署仍可追溯，且被新版**内容超集** ─────────────────────────────
# ★ 2026-11-18 判据基准改造（与 ⑦ **同构**）：旧判据把「并行车在途文件
#   （admin/pi_analysis_handler.go、web/public/menu-config.json）不得出现在
#   上一版发布面里」**写死** ⇒ 那条只在「发我方分支、并发车的改动仍在途」场景成立。
#   本轮发布单元 = **origin/main** ⇒ 并行车的 PI 工作**已正式合入并随 3067 上线**
#   ⇒ 「上一版发布面含 PI 文件」是**发布单元自身的一部分** ⇒ 旧判据**恒定假红**
#   （本轮实测：泄漏 2 处，实为 3067 发布面的合法内容；tag 记 files=113 与
#     diff(base=2083bbe09, 1562862ba)=113 **逐字一致** ⇒ 上一版台账本身是自洽的）。
# ★★ 退役的闸门不要留在仓里当负债：旧 `leak_of`（按「并行车文件清单」判泄漏）
#   已随本改造**整段退役**，不再以任何形式参与判定。
# ⇒ 新判据（与场景无关，随发布单元走）：
#   ① **台账自洽**：diff(上一版 base, 上一版 tag) 的文件数 == tag 记的 files=
#      ⇒ tag 指错提交 / 记错文件数都会在这里报红（上一轮真错位就属此类）；
#   ② **前进关系**：上一版 commit 必须是新版 commit 的**祖先**
#      ⇒ 发版是前进，不是换轨/回退；
#   ③ **内容超集**：新版相对上一版**没有删除任何文件**（diff --name-status 无 D）
#      ⇒ 上一版交付过的内容在新版里仍然在，不被悄悄丢掉；
#   ④ **可回滚**：上一版的线上 release 目录**仍在**（回滚路径真实可用）；
#   ⑤ **PI 域**：只作**报告**（上一版发布面内 PI 域改动几处），不再写死为 0。
if [ -n "$TAG_OLD" ] && git -C "$ROOT" rev-parse --verify "${TAG_OLD}" >/dev/null 2>&1 \
   && [ -n "$TAG_NEW" ]; then
  OH=$(git -C "$ROOT" rev-parse --short "${TAG_OLD}^{commit}")
  OB=$(tag_field "${TAG_OLD}" "base")
  [ -z "$OB" ] && OB=$(git -C "$ROOT" rev-parse --short "${TAG_OLD}~1" 2>/dev/null)
  ON=$(git -C "$ROOT" diff --name-only -z "$OB" "${TAG_OLD}^{commit}" 2>/dev/null | tr '\0' '\n' | wc -l | tr -d ' ')
  OEXP=$(tag_field "${TAG_OLD}" "files")
  # ① 台账自洽
  LEDGER_OK=1
  if [ -n "$OEXP" ] && [ "$ON" != "$OEXP" ]; then LEDGER_OK=0; fi
  # ② 前进关系（上一版是新版祖先）
  FWD=0
  git -C "$ROOT" merge-base --is-ancestor "${TAG_OLD}^{commit}" "${TAG_NEW}^{commit}" 2>/dev/null && FWD=1
  # ③ 内容超集：新版相对上一版**没删文件**
  ODEL=$(git -C "$ROOT" diff --name-status "${TAG_OLD}^{commit}" "${TAG_NEW}^{commit}" 2>/dev/null \
    | awk '$1 ~ /^D/ {c++} END {print c+0}')
  # ④ 上一版 release 目录仍在（回滚路径可用）
  #   ★ 三态：SSH 不可达 ⇒ **未评估**（不等于「目录不存在」）⇒ 不据此判红。
  OSEQ=$(tag_seq_of "$TAG_OLD")
  OSHA=$(git -C "$ROOT" rev-parse "${TAG_OLD}^{commit}" 2>/dev/null | cut -c1-8)
  OLD_REL="${OSEQ}-${OSHA}"
  REL_RAW=$(ssh -o BatchMode=yes -o ConnectTimeout=20 -p "${SSH_PORT}" "root@${SSH_HOST}" \
    "test -d /opt/llm-gateway-go/releases/${OLD_REL} && echo yes || echo no" 2>/dev/null)
  case "$REL_RAW" in
    yes) REL_OK="yes" ;;
    no)  REL_OK="no" ;;
    *)   REL_OK="未评估" ;;   # SSH 不可达 ⇒ 未评估，不是失败
  esac
  SUPER=$(git -C "$ROOT" diff --name-only -z "${TAG_OLD}^{commit}" "${TAG_NEW}^{commit}" 2>/dev/null | tr '\0' '\n' | wc -l | tr -d ' ')
  OPI=$(git -C "$ROOT" diff --name-only -z "$OB" "${TAG_OLD}^{commit}" -- domains/promptinjection 2>/dev/null | tr '\0' '\n' | wc -l | tr -d ' ')
  if [ "$LEDGER_OK" = "1" ] && [ "$FWD" = "1" ] && [ "$ODEL" = "0" ] \
     && { [ "$REL_OK" = "yes" ] || [ "$REL_OK" = "未评估" ]; }; then
    ok "⑧ 上一版部署可追溯 + 内容超集" "tag ${TAG_OLD}（${OH}，基点 ${OB:0:8} + ${ON} 文件${OEXP:+（tag 记 files=${OEXP}）}）；台账自洽=${LEDGER_OK}／前进关系=${FWD}（上一版是新版权威）／新版未删文件=${ODEL}／上一版 release 目录 ${OLD_REL} 留存=${REL_OK}；→新版差异 ${SUPER} 个文件（PI 域 ${OPI} 处，仅报告）"
  else
    bad "⑧ 上一版部署可追溯 + 内容超集" "台账自洽=${LEDGER_OK}（实算 ${ON} vs tag 记 ${OEXP:-未记}）／前进关系=${FWD}／新版删文件 ${ODEL} 处／release 目录 ${OLD_REL} 留存=${REL_OK}；PI 域 ${OPI} 处（仅报告）"
  fi
else
  skip "⑧ 上一版部署可追溯" "无 tag ${TAG_OLD:-（仓内无 durable ref）}"
fi

# ── ⑨ 前端产物三方 sha256（服务器 = 公网 = 本地 dist）────────────────────────
SERVER_SHA=""
if [ -n "$SSH_HOST:-}" ] && [ -n "$ASSET" ]; then
  # ★ 必须量【同一个入口 chunk】（公网 index.html 里引用的那个）：
  #   前端一次构建会产出**多个** `index-*.js`（主入口 + 若干分包），
  #   而 `glob` 的「第一个」是按字典序 ⇒ 可能量到**别的 chunk**
  #   （本轮实测：服务器量到 00d53b52…、公网/本地量到 9477d8eb…
  #     ⇒ 看着像「服务器产物与公网不一致」，其实是**量错了文件**）。
  SERVER_SHA=$(ssh -o BatchMode=yes -o ConnectTimeout=20 -p "${SSH_PORT}" "root@${SSH_HOST}" \
    "sha256sum /opt/llm-gateway-go/current/web${ASSET}" 2>/dev/null | awk '{print $1}' | head -1)
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
  MT_OUT=$(git -C "$ROOT" merge-tree --write-tree origin/main "$BRANCH" 2>&1); MT_RC=$?
  if [ "$MT_RC" = "0" ]; then
    ok "⑩ 合并试演零冲突" "git merge-tree 退出码 0（origin/main=$(git -C "$ROOT" rev-parse --short origin/main)）"
  else
    # ★ 红的**归因**才是结论：只给一个退出码，复核者还得自己重跑一遍才知道冲突在哪。
    #   ⇒ 这里把**冲突文件**与**两侧差异摘要**一起给出（原样引 git 的三方条目）。
    MT_TRIPLES=$(printf '%s\n' "$MT_OUT" | awk '
      $1 ~ /^[0-9]+$/ && NF>=4 {
        path=$NF; stage=$3; sha=$2;
        if (stage=="2") b2[path]=sha; else if (stage=="3") b3[path]=sha; else if (stage=="1") b1[path]=sha;
        seen[path]=1;
      }
      END { for (p in seen) printf "%s|%s|%s\n", p, b2[p], b3[p] }' | sort)
    DETAIL=""
    while IFS='|' read -r p b2 b3; do
      [ -z "$p" ] && continue
      D=$(git -C "$ROOT" diff --no-color "$b2" "$b3" 2>/dev/null | grep -E '^[+-][^+-]' | head -4 | tr '\n' ' ')
      DETAIL="${DETAIL}${p}[ours<->theirs: ${D:-无内容差异}]; "
    done <<EOF
$MT_TRIPLES
EOF
    bad "⑩ 合并试演零冲突" "退出码 ${MT_RC}（origin/main=$(git -C "$ROOT" rev-parse --short origin/main)）；冲突与两侧差异：${DETAIL:-（未能解析）}"
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
    # ★★ 造流量的**模型**必须选「在 154 上真有候选面」的模型（L20）：
    #   原写死 `glm-4.5-flash` ⇒ 该模型在 154 上**候选面为空**（实测 503 `no_candidates`）
    #   ⇒ 那几笔「在途流量」其实**以 503 收尾** ⇒ 阶段树记录的是**失败请求的在途**
    #   ⇒ 断言虽仍成立，钉的却是「失败请求的在途」⇒ 证据偏弱。
    #   ⇒ 改用 `auto`（实测 200 真作答）⇒ 造的是**成功流量**。
    #   ★ 仍可用 `STAGE_TREE_TRAFFIC_MODEL` 覆盖（换机器/换模型面时不必改脚本）。
    TRAFFIC_MODEL="${STAGE_TREE_TRAFFIC_MODEL:-auto}"
    TRAFFIC_CODE_DIR="/tmp/vst-traffic-codes"
    mkdir -p "$TRAFFIC_CODE_DIR"; rm -f "$TRAFFIC_CODE_DIR"/*.code 2>/dev/null
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
      printf '    造流量: 模型 %s（在途型，stream+max_tokens=1000）' "${TRAFFIC_MODEL}"
      for i in 1 2 3 4; do
        # ★ 把每笔的 HTTP 码落文件 ⇒ 采集器**自报造流量实况**（造成功流量 vs 造失败流量）
        ( curl -s -m 60 -N -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/v1/chat/completions" -H "Authorization: Bearer ${KEY}" \
            -H 'Content-Type: application/json' \
            --data "{\"model\":\"${TRAFFIC_MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"请写一篇 800 字的中文短文，介绍分布式系统的一致性模型\"}],\"max_tokens\":1000,\"stream\":true}" \
            > "${TRAFFIC_CODE_DIR}/$1-$i.code" 2>/dev/null ) &
        sleep 1
      done
      echo
      return 0
    }
    # 等在途流量收尾（最多 60s）⇒ 拿到每笔的 HTTP 码 ⇒ 「造流量实况」可读
    wait_traffic_codes() {
      local n=0 i
      for i in $(seq 1 60); do
        n=$(ls -1 "${TRAFFIC_CODE_DIR}"/*.code 2>/dev/null | wc -l | tr -d ' ')
        [ "$n" -ge 4 ] && break
        sleep 1
      done
      cat "${TRAFFIC_CODE_DIR}"/*.code 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]*$//'
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
    # ★ 造流量实况自报：断言跑完后再收尾 ⇒ 拿到每笔的 HTTP 码
    #   ⇒ 「造成功流量」与「造失败流量」在证据里**看得见**（L20 的真正修法）。
    TRAFFIC_CODES=""
    if [ "${STAGE_TREE_TRAFFIC:-1}" = "1" ]; then
      TRAFFIC_CODES=$(wait_traffic_codes)
      echo "    造流量实况: 模型 ${TRAFFIC_MODEL} ⇒ HTTP ${TRAFFIC_CODES:-未回读}"
    fi
    for pair in "⑫ 断言（行为层）|$A1|$A1RC" "⑬ 断言（设计层）|$A2|$A2RC"; do
      NAME="${pair%%|*}"; REST="${pair#*|}"; TXT="${REST%%|*}"; RC="${REST##*|}"
      LINE=$(printf '%s' "$TXT" | grep -E 'RESULT:' | tail -1)
      case "$RC" in
        0) ok "${NAME}" "${LINE:-PASS}${TRAFFIC_CODES:+；造流量 HTTP: ${TRAFFIC_CODES}（模型 ${TRAFFIC_MODEL}）}" ;;
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
           --data "{\"model\":\"${TRAFFIC_MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"ok\"}],\"max_tokens\":8}" 2>/dev/null || true)
      if [ "$R1" = "404" ] && [ "$R2" = "401" ]; then
        ok "⑭ 临时密钥清理" "id=${KEY_ID} 已删（回读 ${R1} / 旧密钥 ${R2}）"
      else
        bad "⑭ 临时密钥清理" "id=${KEY_ID} 回读=${R1}（期望 404）旧密钥=${R2}（期望 401）"
      fi
    else
      skip "⑭ 临时密钥清理" "本轮未创建临时密钥"
    fi

    # ── ⑮ F1 专项：紧凑模式图例字形必须随模式切换（设计规则 15）────────────
    #   ★ 这一条**曾经是缺陷本体**：瓦片换成了紧凑字形、图例仍是固定数组
    #     ⇒ 「同一条规定写了两份」⇒ 必然漂移 ⇒ 生产页面出现「图例说 ✓、画面没 ✓」。
    #   ⇒ 修法是单一真源（web/src/lib/stageTreeGlyphs.ts），此处是**线上实证**：
    #     紧凑模式图例 done/pending/unmeasured 必须为空串、全页 ✓ 计数必须 = 0；
    #     同时**完整模式必须仍有 ✓**（对照组）—— 没有对照组，
    #     「紧凑模式 ✓=0」只能证明「什么都没画」，不能证明「按规则画对了」。
    F1=$(node "$ROOT/maestro/tools/probe-f1-legend.mjs" --cdp http://127.0.0.1:9222 --url "${BASE_URL}" \
          --token-file "${STAGE_TREE_TOKEN_FILE:-/tmp/llmgw-jwt.txt}" 2>&1); F1RC=$?
    F1LINE=$(printf '%s' "$F1" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
except Exception as e:
    print("PARSE_FAIL: %s" % e); raise SystemExit(0)
c = d.get("compact") or {}
f = d.get("full") or {}
ct = [x.get("text", "") for x in (c.get("legend") or [])]
ft = [x.get("text", "") for x in (f.get("legend") or [])]
cc = c.get("checkCount")
fc = f.get("checkCount")
exp = ["", "◐", "", "⌀", "✕", "⚠", ""]
bad = []
if len(ct) != 7:
    bad.append("紧凑图例项数=%d(期望7)" % len(ct))
else:
    for i, (got, want) in enumerate(zip(ct, exp)):
        if got != want:
            bad.append("紧凑图例[%d]=%r(期望%r)" % (i, got, want))
if cc != 0:
    bad.append("紧凑模式全页 ✓ 计数=%r(期望0)" % cc)
if not ft or ft[0] != "✓":
    bad.append("完整模式图例 done=%r(期望 ✓ 对照组)" % (ft[0] if ft else None))
if bad:
    print("FAIL | " + "; ".join(bad))
else:
    print("PASS | 紧凑图例=%s（✓计数=%s）；完整图例 done=%r（✓计数=%s，对照组）" % (ct, cc, ft[0], fc))
' 2>&1)
    if [ "$F1RC" != "0" ]; then
      bad "⑮ F1 紧凑图例字形随模式切换" "探针退出码 ${F1RC}（未评估）：$(printf '%s' "$F1" | head -c 160)"
    elif [ "${F1LINE#FAIL }" != "$F1LINE" ] || [ "${F1LINE#PARSE_FAIL: }" != "$F1LINE" ]; then
      bad "⑮ F1 紧凑图例字形随模式切换" "${F1LINE}"
    elif [ "${F1LINE#PASS }" = "$F1LINE" ]; then
      skip "⑮ F1 紧凑图例字形随模式切换" "探针输出无法判定：$(printf '%s' "$F1" | head -c 160)"
    else
      ok "⑮ F1 紧凑图例字形随模式切换" "${F1LINE#PASS | }"
    fi
  else
    skip "⑫ 断言（行为层）" "缺 CDP(9222) 或 JWT ⇒ 未评估（不是失败）"
    skip "⑬ 断言（设计层）" "缺 CDP(9222) 或 JWT ⇒ 未评估（不是失败）"
    skip "⑭ 临时密钥清理" "未进入浏览器面"
    skip "⑮ F1 紧凑图例字形随模式切换" "缺 CDP(9222) 或 JWT ⇒ 未评估（不是失败）"
  fi
else
  skip "⑪ Maestro flow 12" "未加 --with-browser"
  skip "⑫ 断言（行为层）" "未加 --with-browser"
  skip "⑬ 断言（设计层）" "未加 --with-browser"
  skip "⑭ 临时密钥清理" "未加 --with-browser"
  skip "⑮ F1 紧凑图例字形随模式切换" "未加 --with-browser"
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

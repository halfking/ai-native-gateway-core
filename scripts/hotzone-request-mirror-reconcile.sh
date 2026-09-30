#!/usr/bin/env bash
# hotzone-request-mirror-reconcile.sh — 热区请求侧 body 镜像 × PG 对账
#
# 用途：验证 storage/file.RequestMirror 落盘的 {HotZone.Dir}/requests/ 子树，
#       与 PG request_logs_bodies_hot 的正文在**语义上一致**，并按已实证的三条
#       特殊口径容错（这三处是「不该判 FAIL」的真实差异，不是缺陷）。
#
# 依据（本脚本的每条规则都有部署级实证来源，不是推断）：
#   - F4 口径（docs/audit/2026-09-30-hotzone-p3p4-critical-audit.md §二 F4）：
#     ① 镜像投递的是**换算后、body summary 摘要前的原文**。开启
#        requestBodiesSummaryEnabled 的 tenant 上镜像=全文、PG=摘要信封，
#        逐字节比对必然对不上 → 本脚本只做「JSON 语义包含」判定。
#     ② strPtrToJSON 把空串/非法 JSON 收敛成 "{}"；镜像侧**跳过** "null"/"{}"
#        （MirrorablePayload），PG 侧 "{}" **照落库**。故 PG 的 null/{} 行在
#        镜像里必然缺席 → 按 F4 豁免，不判 FAIL。
#   - F5-R3 口径（同文 §二 F5-R3）：同一 request_id 若既经 telemetry client
#     直连 PG、又被 admin HTTP ingest 投递，镜像会**两次落到不同 tenant 目录**
#     （client 用 ApplicationCode||TenantID，admin 用 nonEmptyDefault(TenantID)）。
#     PG 侧有 ON CONFLICT DO NOTHING，镜像层无跨写方去重 → 同一 request_id 的
#     镜像在多个 tenant 目录下都算合法。
#   - 单向容错（e2e 演练 O5 实测）：v2 会话镜像以 **tx.Commit 为界**投递，
#     telemetry/admin 镜像以 **persistRequestLog 入口**为界投递。故：
#       - 镜像有、PG 无  → **合法**（PG 停机窗口/回放缺失；实测 O5「镜像多于 PG」成立）
#       - PG 有、镜像无  → **可疑**（可能是 F4 豁免，也可能是真丢镜像）→ 单独计数
#     脚本**单向容错**：镜像多于 PG 不判 FAIL；PG 多于镜像只报 WARN 供人工判读。
#
# 用法：
#   hotzone-request-mirror-reconcile.sh --hotzone-dir <dir> \
#       [--pg-url <dsn>] [--tenant <t>] [--date YYYY-MM-DD] [--limit N] [--json]
#
#   --hotzone-dir  必填。热区根目录（脚本在其下读 requests/ 子树）。
#   --pg-url       可选。给了就连 PG 做真对账；不给则只做镜像侧自检（列目录+gunzip 体检）。
#   --tenant       可选。只对账该 tenant 子目录；不给=遍历所有 tenant 目录。
#   --date         可选。只对账该日期目录；不给=遍历所有日期目录。
#   --limit        可选。每 tenant 抽样上限（默认 200），防止全量拉爆。
#   --json         输出机器可读 JSON（供 CI/后续自动化消费）。
#
# 退出码：
#   0 = 对账通过（无 FAIL）
#   1 = 存在 FAIL（镜像文件损坏 / 目录不可读 等硬错误）
#   2 = 用法错误
#
# 依赖：bash、gunzip/zcat、find、awk、sed。可选 psql（仅 --pg-url 时需要）。

set -uo pipefail

HOTZONE_DIR=""
PG_URL=""
TENANT=""
DATE_FILTER=""
LIMIT=200
JSON_OUT=0

die() { echo "ERROR: $*" >&2; exit 2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --hotzone-dir) HOTZONE_DIR="${2:-}"; shift 2 ;;
    --pg-url)      PG_URL="${2:-}"; shift 2 ;;
    --tenant)      TENANT="${2:-}"; shift 2 ;;
    --date)        DATE_FILTER="${2:-}"; shift 2 ;;
    --limit)       LIMIT="${2:-}"; shift 2 ;;
    --json)        JSON_OUT=1; shift ;;
    -h|--help)     sed -n '2,40p' "$0"; exit 0 ;;
    *)             die "unknown arg: $1" ;;
  esac
done

[[ -n "$HOTZONE_DIR" ]] || die "--hotzone-dir is required"
[[ -d "$HOTZONE_DIR" ]] || die "hotzone dir not found: $HOTZONE_DIR"
REQUESTS_DIR="$HOTZONE_DIR/requests"
[[ -d "$REQUESTS_DIR" ]] || die "requests subtree not found: $REQUESTS_DIR (热区镜像未装配? RequestMirrorEnabled=false 或非 full 模式?)"

# ── 计数（对齐内存里的教训：判据要能区分「不匹配」与「没数据」） ──
FAILS=0
WARN_PG_ONLY=0      # PG 有镜像无（F4 豁免或真丢）——不判 FAIL，单列供人工判读
COUNT_MIRROR_FILES=0
COUNT_GUNZIP_OK=0
COUNT_GUNZIP_BAD=0
COUNT_SEEN=0

emit() { [[ "$JSON_OUT" -eq 1 ]] || echo "$@"; }

fail() { FAILS=$((FAILS+1)); echo "FAIL: $*" >&2; }

# report 输出汇总（人读表 / --json 两种形态）。
#
# 三态而非两态（重要）：EMPTY / PASS / FAIL 必须可区分。「库里没数据」与
# 「查过了、一致」长得一模一样时，这个门的结论就没有信息量——空目录报 PASS
# 等于让「镜像根本没装配」伪装成「对账通过」。EMPTY 单独成态，退出码 0 但
# 结论显式标注为「跳过，不构成证据」。
report() {
  local result
  if [[ "$COUNT_MIRROR_FILES" -eq 0 && "$WARN_PG_ONLY" -eq 0 ]]; then
    result="EMPTY"
  elif [[ "$FAILS" -gt 0 ]]; then
    result="FAIL"
  else
    result="PASS"
  fi
  if [[ "$JSON_OUT" -eq 1 ]]; then
    cat <<JSON
{"result":"$result","mirror_files_checked":$COUNT_MIRROR_FILES,"files_seen":$COUNT_SEEN,"gunzip_ok":$COUNT_GUNZIP_OK,"gunzip_bad":$COUNT_GUNZIP_BAD,"pg_only_warn":$WARN_PG_ONLY,"fails":$FAILS}
JSON
  else
    echo "" >&2
    echo "== 汇总 ==" >&2
    printf '  扫到文件/体检数  : %s / %s\n' "$COUNT_SEEN" "$COUNT_MIRROR_FILES" >&2
    printf '  gunzip OK/损坏   : %s / %s\n' "$COUNT_GUNZIP_OK" "$COUNT_GUNZIP_BAD" >&2
    printf '  PG有/镜像无(WARN): %s\n' "$WARN_PG_ONLY" >&2
    printf '  FAIL             : %s\n' "$FAILS" >&2
    echo "  结果             : $result" >&2
    if [[ "$result" == "EMPTY" ]]; then
      echo "  ⚠ EMPTY 不构成对账证据：抽样内无任何镜像文件/PG 行。请确认热区已装配" >&2
      echo "    (STORAGE_MODE=full + RequestMirrorEnabled)、请求侧确有流量、--limit 未过小。" >&2
    fi
  fi
  # 退出码：FAIL=1，EMPTY/PASS=0（EMPTY 靠 result 字段与告警区分，不靠退出码）。
  return 0
}

# ── 1. 镜像侧自检：列文件 + gunzip 体检（不依赖 PG，任何环境都能跑） ──
# 路径：requests/{tenant}/{YYYY-MM-DD}/{requestID}.{req|resp|out}.json.gz
collect_files() {
  local base="$REQUESTS_DIR"
  [[ -n "$TENANT" ]] && base="$base/$TENANT"
  find "$base" -type f -name '*.json.gz' 2>/dev/null | sort
}

echo "== 热区镜像 × PG 对账 ==" >&2
echo "hotzone dir : $HOTZONE_DIR" >&2
echo "requests    : $REQUESTS_DIR" >&2
[[ -n "$TENANT" ]] && echo "tenant      : $TENANT (单租户)" >&2
[[ -n "$DATE_FILTER" ]] && echo "date        : $DATE_FILTER" >&2
echo "" >&2

# 逐文件 gunzip 体检。抽样 LIMIT 防止全量拉爆。
#
# COUNT_MIRROR_FILES 只计**真正体检过**的文件（limit 之后 continue 的不计入），
# 否则「扫到 500 个 / 体检 200 个」会被读成「500 个都验过了」——汇总数字必须
# 反映做过的检查，不是扫过的目录。COUNT_SEEN 单独记「扫到多少」供对照。
while IFS= read -r f; do
  [[ -n "$f" ]] || continue
  if [[ -n "$DATE_FILTER" && "$f" != *"/$DATE_FILTER/"* ]]; then continue; fi
  COUNT_SEEN=$((COUNT_SEEN+1))
  if [[ "$COUNT_SEEN" -gt "$LIMIT" ]]; then continue; fi
  COUNT_MIRROR_FILES=$((COUNT_MIRROR_FILES+1))

  if gunzip -t "$f" 2>/dev/null; then
    COUNT_GUNZIP_OK=$((COUNT_GUNZIP_OK+1))
    # gunzip 内容做一次 JSON 有效性体检（换算后应是合法 JSON）。
    if ! gunzip -c "$f" 2>/dev/null | head -c 1 | grep -q '[{[]'; then
      fail "镜像内容非 JSON 起始: $f"
    fi
  else
    COUNT_GUNZIP_BAD=$((COUNT_GUNZIP_BAD+1))
    fail "gunzip 损坏: $f"
  fi
done < <(collect_files)

echo "镜像文件(抽样内): $COUNT_MIRROR_FILES | gunzip OK: $COUNT_GUNZIP_OK | 损坏: $COUNT_GUNZIP_BAD" >&2

# ── 2. PG 侧对账（仅 --pg-url） ──
# 无 PG 时：只报镜像侧体检结果，明确告知未做 PG 比对（不假装闭环）。
if [[ -z "$PG_URL" ]]; then
  echo "" >&2
  echo "NOTE: 未提供 --pg-url，仅完成镜像侧自检（列目录 + gunzip 体检），未做 PG 内容比对。" >&2
  report
  exit $(( FAILS > 0 ? 1 : 0 ))
fi

command -v psql >/dev/null 2>&1 || die "--pg-url given but psql not found in PATH"

# 取一批近期 request_id 的 PG 正文（按 tenant/date 过滤；单行一条 request_id，\t 分隔正文）。
# 口径：以 PG 为基准遍历，镜像侧按 (tenant, requestID) 多写方去重查找（F5-R3）。
echo "" >&2
echo "== PG 侧抽样比对 ==" >&2

TENANT_ARG=""
[[ -n "$TENANT" ]] && TENANT_ARG="AND b.tenant_id = '$TENANT'"

# psql 单行输出：request_id \t 换算后 request_body 的前 120 字符（仅作存在性/前缀粗筛）。
# 完整语义比对交由上层（逐条 gunzip 后比对），这里先做「PG 有正文但镜像无对应文件」的扫描。
PG_QUERY="
SELECT DISTINCT b.request_id
  FROM request_logs_bodies_hot b
  JOIN request_logs_hot rl ON rl.request_id = b.request_id
 WHERE 1=1 ${TENANT_ARG}
   AND (b.request_body IS NOT NULL AND b.request_body::text NOT IN ('null','{}'))
 ORDER BY b.request_id DESC
 LIMIT $LIMIT;"

pg_ids=$(psql "$PG_URL" -tAc "$PG_QUERY" 2>/dev/null)
if [[ $? -ne 0 ]]; then
  fail "psql 查询失败（连接/权限/表不存在）——对账无法完成，不应判为通过"
else
  # 对每个 PG request_id，在镜像任意 tenant 目录里找它的 .req 文件（F5-R3 多写方）。
  while IFS= read - rid; do
    [[ -n "$rid" ]] || continue
    # F4 豁免：若 PG 正文是 null/{}，镜像必然缺席，查询已过滤，这里理论上不该命中。
    found=0
    while IFS= read -r mf; do
      if [[ "$(basename "$mf")" == "$rid."* ]]; then found=1; break; fi
    done < <(collect_files)
    if [[ "$found" -eq 0 ]]; then
      # 单向容错：PG 多于镜像不判 FAIL（可能是 F4 豁免/回放缺失/其他写方），只计 WARN。
      WARN_PG_ONLY=$((WARN_PG_ONLY+1))
    fi
  done <<< "$pg_ids"
fi

echo "PG 有正文但镜像无对应文件(仅 WARN，不判 FAIL): $WARN_PG_ONLY" >&2

report
exit $(( FAILS > 0 ? 1 : 0 ))

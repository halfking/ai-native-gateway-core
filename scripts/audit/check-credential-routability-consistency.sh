#!/usr/bin/env bash
# check-credential-routability-consistency.sh —— 凭据「可恢复性」与「可路由性」的**一致性**检查器（★ 只读）
#
# # 它查什么
#
# 本仓的凭据有两个**各自独立**的判定来源：
#
#   ① `credentials.availability_state`（★ 「凭据能不能用」⇒ ★ UI 展示 / 诊断面板读它）
#   ② `v_routable_credential_models.is_routable`（★ 「这条 (凭据,模型) 能不能进路由候选池」）
#
# ⇒ ★★ 二者的 CASE 优先级里，`quota_state ∈ (permanently_exhausted, balance_exhausted,
#   periodic_exhausted)` 这一档**排在 availability 各档之后** ⇒ ★★ **quota 判定会盖过 availability**。
#
# ★★★ 而 `handleForceRecoverSingle`（★ `admin/diagnostics_credential.go`）**只重置 availability 那一族**
#   （★ `availability_state` / `availability_recover_at` / `circuit_state` / `consecutive_failures` /
#   ★ `state_reason_code` / `state_reason_detail`）⇒ ★★ **不碰 `quota_state`**。
# ⇒ ⇒ ★★★★ **后果**：★ 对一个「配额已耗尽」的凭据调用 force-recover ⇒ ★★ 接口返回 200、
#   ★★ 文案说「凭据已强制恢复，credential / binding / probe_state 已重置」⇒ ★★ **但它仍然不可路由**。
#   ⇒ ★★★ **⇒ 「恢复成功」是假的**（★ UI 说好、路由说不）。
#
# ★★★★ 实测复现（★ 本地开发库，★ 单事务 + 回滚，★ 零残留）：
#   UPDATE credentials SET availability_state='ready', state_reason_detail='admin force-recover via diagnostic'
#    WHERE id=3;                      -- ★ 逐字照抄 handler 的写入面
#   ⇒ availability_state = 'ready'   （★ UI 说「已恢复」）
#   ⇒ quota_state        = 'permanently_exhausted'（★ 没被重置）
#   ⇒ v_routable_credential_models：is_routable = f / unavailable_reason = 'quota_permanently_exhausted'
#   ⇒ ⇒ 路由仍然不选它 ⇒ ★★ **force-recover 对这一类无效**
#
# # 用法
#
#   bash scripts/audit/check-credential-routability-consistency.sh probe [DSN]
#   bash scripts/audit/check-credential-routability-consistency.sh classify < 清单.tsv
#
# ★ DSN 缺省取环境变量 LLM_GATEWAY_DATABASE_URL；★ **绝不打印 DSN 或口令**。
#
# # 退出码
#
#   0 = 没有矛盾（★ 凭据的「可用」与「可路由」一致）
#   2 = 有矛盾（★ 存在「看起来可用但不可路由」的凭据）
#   3 = 探测失败
#   64 = 用法/环境错误
#
# # ★ 诚实边界
#
#   ★ 本检查器只报**矛盾**，★ **不改任何东西**（★ 不重置 quota_state、★ 不调 force-recover）。
#   ★ ⇒ **修复动作属生产变更 ⇒ ★ 待属主裁决**（★ 见 R158 审计档）。
set -uo pipefail

MODE="${1:-}"
DSN="${2:-${LLM_GATEWAY_DATABASE_URL:-}}"

# ─────────────────────────── 只读 SQL（★ 一条语句，无副作用） ───────────────────────────
PROBE_SQL="WITH v AS (
  SELECT c.id AS cred,
         c.provider_id,
         c.availability_state,
         COALESCE(c.quota_state, '') AS quota_state,
         COALESCE(c.health_status, '') AS health_status,
         v.binding_id,
         v.raw_model_name,
         v.is_routable,
         COALESCE(v.unavailable_reason, '') AS unavailable_reason,
         -- ★★★★ **三个【各自语义明确】的断言**（★ 别处不要另写一份）
         --
         -- ★ A. **凭据级自相矛盾**：★ 「availability_state='ready'「（★ 看起来可用）
         --     但 」quota_state「 落在「永久耗尽」族（★ quota_*）
         --     ⇒ ★★ **force-recover 不重置 quota_state** ⇒ ★★ **恢复不了这一类**
         --     ⇒ ★★★ 这是「恢复成功」是假的的**根因**
         --
         -- ★ B. **同一件事两个答案**：★ 绑定级理由落在 」availability_%「 家族
         --     （★ 凭据级理由）而凭据说 ready ⇒ ★★ 凭据级与路由级互相矛盾
         --
         -- ★ C. **探针回标**：★ 」node_probe_failed「 / 」binding_unavailable「
         --     ⇒ ★★ force-recover **会**清，★ 但探针 worker 会**再标回来**
         --
         -- ★★ **刻意【不】把「人工禁用 / 生命周期停用 / provider 禁用」算成矛盾**：
         --     ★★ 那些是**绑定/模型级**的判定（★ 人工故意），★ 与凭据级 availability
         --     ★★ **粒度不同** ⇒ ★★ 本来就该被路由排除 ⇒ ★★ **不是矛盾，是正确行为**。
         CASE
           WHEN c.availability_state = 'ready'
                AND c.quota_state IN ('permanently_exhausted','balance_exhausted','periodic_exhausted')
             THEN 'A-credential-quota-contradiction'
           WHEN c.availability_state = 'ready'
                AND v.unavailable_reason LIKE 'availability_%'
             THEN 'B-same-signal-two-answers'
           WHEN c.availability_state = 'ready'
                AND v.unavailable_reason IN ('node_probe_failed','binding_unavailable')
             THEN 'C-probe-reverted'
           ELSE 'D-not-a-contradiction'
         END AS finding_class
    FROM credentials c
    JOIN v_routable_credential_models v ON v.credential_id = c.id
   WHERE c.availability_state = 'ready'
     AND v.is_routable = FALSE
)
SELECT cred, provider_id, availability_state, quota_state, health_status,
       binding_id, raw_model_name, is_routable, unavailable_reason, finding_class
  FROM v
 ORDER BY finding_class, cred, binding_id"

# ─────────────────────────── DSN 解析（★ 只取连接参数，★ 不落盘不进 argv） ───────────────────────────
DSN_HOST=""; DSN_PORT=""; DSN_USER=""; DSN_DBNAME=""; DSN_PASSWORD=""; DSN_EXTRA=""
parse_dsn() {
  local uri="$1"
  local rest="${uri#*://}"
  local cred="${rest%%@*}"
  local hostpart="${rest#*@}"
  hostpart="${hostpart%%\?*}"
  DSN_USER="${cred%%:*}"
  DSN_PASSWORD="${cred#*:}"
  [[ "$DSN_PASSWORD" == "$DSN_USER" ]] && DSN_PASSWORD=""
  local hp="${hostpart%%/*}"
  DSN_HOST="${hp%%:*}"
  DSN_PORT="${hp##*:}"
  [[ "$DSN_PORT" == "$DSN_HOST" ]] && DSN_PORT=""
  DSN_DBNAME="${hostpart#*/}"
  DSN_EXTRA="${rest#*\?}"
}

needles() {
  # ★ 要抹掉的串：完整 DSN + 单独的口令。
  { printf '%s\n' "$1"
    printf '%s\n' "$1" | sed -nE 's#^[a-zA-Z][a-zA-Z0-9+.-]*://[^@/]*:([^@]*)@.*#\1#p'
  }
}

scrub() {
  local nf="$1" f="$2"
  awk -v nf="$nf" '
    BEGIN { while ((getline line < nf) > 0) { if (line != "") { k++; nd[k] = line } } close(nf) }
    { out = ""; rest = $0
      for (j = 1; j <= k; j++) {
        s = nd[j]
        while (index(rest, s) > 0) {
          p = index(rest, s)
          out = out substr(rest, 1, p - 1) "***"
          rest = substr(rest, p + length(s))
        }
      }
      print out rest }' "$f"
}

usage() {
  cat >&2 <<'USAGE'
用法：
  bash scripts/audit/check-credential-routability-consistency.sh probe [DSN]
  bash scripts/audit/check-credential-routability-consistency.sh classify < 清单.tsv

★ DSN 缺省取 LLM_GATEWAY_DATABASE_URL；★ 绝不打印 DSN 或口令。
★ 只读：★ 不重置任何状态、★ 不调用 force-recover。
USAGE
}

# ─────────────────────────── classify（★ 判定逻辑与查库分开 ⇒ 门不需要真库） ───────────────────────────
classify_file() {
  local src="$1"
  local n creds a b c d verdict code
  # ★★ 计数用 awk（★ **`grep -vc` 在空输入/多文件时会输出多行** ⇒ ★★ `[[ "0\n0" -gt 0 ]]` 会语法错）
  n="$(awk '!/^#/{c++} END{print c+0}' "$src" 2>/dev/null)"
  creds="$(awk 'BEGIN{FS="\t"} !/^#/{print $1}' "$src" 2>/dev/null | sort -u | wc -l | tr -d ' ')"
  a="$(awk 'BEGIN{FS="\t"} !/^#/ && $10 ~ /^A-/{c++} END{print c+0}' "$src" 2>/dev/null)"
  b="$(awk 'BEGIN{FS="\t"} !/^#/ && $10 ~ /^B-/{c++} END{print c+0}' "$src" 2>/dev/null)"
  c="$(awk 'BEGIN{FS="\t"} !/^#/ && $10 ~ /^C-/{c++} END{print c+0}' "$src" 2>/dev/null)"
  d="$(awk 'BEGIN{FS="\t"} !/^#/ && $10 ~ /^D-/{c++} END{print c+0}' "$src" 2>/dev/null)"
  # ★★ **有【真矛盾】（A 或 B）才算 inconsistent**；★ C 类是「恢复不持久」；★ D 类不是矛盾。
  if [[ $(( a + b )) -gt 0 ]]; then verdict="inconsistent"; code=2
  elif [[ "$c" -gt 0 ]]; then verdict="probe-reverted-only"; code=2
  else verdict="consistent"; code=0; fi
  {
    printf '════════ 凭据「可恢复性 vs 可路由性」一致性检查 ════════\n'
    printf '判定            : %s\n' "$verdict"
    printf '退出码          : %s\n' "$code"
    printf '不可路由绑定总数 : %s\n' "${n:-0}"
    printf '涉及凭据数      : %s\n' "${creds:-0}"
    printf '── 按断言分类（★ 单一真值源在 SQL 的 finding_class）──\n'
    printf '  A 凭据级自相矛盾（★ availability=ready 但 quota 永久耗尽）: %s\n' "${a:-0}"
    printf '      ⇒ ★★ force-recover **不重置 quota_state** ⇒ ★★ **恢复不了这一类**\n'
    printf '  B 同一信号两个答案（★ 绑定理由落 availability 家族）      : %s\n' "${b:-0}"
    printf '      ⇒ ★★ 凭据级与路由级互相矛盾\n'
    printf '  C 探针回标（★ node_probe_failed / binding_unavailable）  : %s\n' "${c:-0}"
    printf '      ⇒ ★★ force-recover 会清，★ 但探针 worker 会再标回\n'
    printf '  D 不是矛盾（★ 人工禁用 / 生命周期停用 / provider 禁用）  : %s\n' "${d:-0}"
    printf '      ⇒ ★★ 那些是**绑定/模型级**判定（★ 人工故意），★ 粒度不同 ⇒ ★★ 本就该被路由排除\n'
    if [[ "$code" -ne 0 ]]; then
      printf '判读：★ 先看 finding_class 那一列 ⇒ ★★ 不同类别的补救手段**不同**。\n'
      printf '      ⇒ ★★ A 类是**真缺陷**（★ 「恢复成功」是假的）；★ C 类是**恢复不持久**。\n'
      printf '      ⇒ ★★ 处置属生产变更 ⇒ ★ **待属主裁决**（★ 本检查器只读，★ 不动手）。\n'
    else
      printf '判读：★ 没有发现判定来源互相矛盾的绑定。\n'
    fi
  } >&2
  printf '#meta\tverdict\t%s\n' "$verdict"
  printf '#meta\texit_code\t%s\n' "$code"
  printf '#meta\tunroutable_bindings\t%s\n' "${n:-0}"
  printf '#meta\tcredentials_affected\t%s\n' "${creds:-0}"
  printf '#meta\tclass_A_credential_quota\t%s\n' "${a:-0}"
  printf '#meta\tclass_B_two_answers\t%s\n' "${b:-0}"
  printf '#meta\tclass_C_probe_reverted\t%s\n' "${c:-0}"
  printf '#meta\tclass_D_not_contradiction\t%s\n' "${d:-0}"
  cat "$src"
  # ★★ **退出码必须反映判定**（★ 「不许静默降级」的落点是退出码，★ 不是 stderr 上的一句话）
  return "$code"
}

probe_db() {
  local dsn="$1"
  if ! command -v psql >/dev/null 2>&1; then
    printf 'FATAL: PATH 里没有 psql ⇒ 无法探库（用 classify 模式喂清单即可）\n' >&2
    return 64
  fi
  local inv err nf rc=0
  inv="$(mktemp "${TMPDIR:-/tmp}/crc-inv.XXXXXX")" || return 64
  err="$(mktemp "${TMPDIR:-/tmp}/crc-err.XXXXXX")" || return 64
  nf="$(mktemp "${TMPDIR:-/tmp}/crc-nd.XXXXXX")" || return 64
  needles "$dsn" > "$nf"

  local -a args=(-X -q -A -t -F $'\t' -v ON_ERROR_STOP=1)
  local -a env=("PGCONNECT_TIMEOUT=${PGCONNECT_TIMEOUT:-10}" "PGAPPNAME=llmgw-cred-routability-check")
  if [[ -n "$dsn" ]]; then
    parse_dsn "$dsn"
    [[ -n "$DSN_HOST" ]]   && args+=(-h "$DSN_HOST")
    [[ -n "$DSN_PORT" ]]   && args+=(-p "$DSN_PORT")
    [[ -n "$DSN_USER" ]]   && args+=(-U "$DSN_USER")
    [[ -n "$DSN_DBNAME" ]] && args+=(-d "$DSN_DBNAME")
    [[ -n "$DSN_PASSWORD" ]] && env+=("PGPASSWORD=$DSN_PASSWORD")
  fi
  args+=(-c "$PROBE_SQL")
  env -u PGSERVICEFILE -u PGSERVICE "${env[@]}" psql "${args[@]}" > "$inv" 2> "$err" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    printf 'FATAL: 探库失败（psql 退出码 %s）★ 目标 = %s\n' "$rc" "${DSN_HOST:-?}:${DSN_PORT:-?}/${DSN_DBNAME:-?}" >&2
    scrub "$nf" "$err" >&2
    rm -f "$inv" "$err" "$nf"
    return 3
  fi
  rm -f "$err" "$nf"
  classify_file "$inv"
  local crc=$?
  rm -f "$inv"
  return "$crc"
}

case "$MODE" in
  probe)
    if [[ -z "$DSN" ]]; then
      printf 'FATAL: 没有 DSN ⇒ 请设 LLM_GATEWAY_DATABASE_URL 或把 DSN 作为第一个参数\n' >&2
      exit 64
    fi
    probe_db "$DSN"; exit $?
    ;;
  classify)
    # ★★★★ **必须先把 stdin 落到临时文件**（★ 本轮实测踩出来的三重坑）：
    #   ① 传 "-" 给 awk ⇒ ★★ awk 把它当**文件名**（★ 不是 stdin）⇒ ★★ 读到 0 行
    #   ② 直接读 /dev/stdin ⇒ ★★ **只能读一次** ⇒ ★★ 第一个 awk 把 stdin 吃光
    #      ⇒ ★★ 后面的计数全 0 ⇒ ★★ **「绑定总数 2（真）」与「涉及凭据数 0（假）」自相矛盾**
    #      ⇒ ★★★ **⇒ 所有计数必须从【同一份快照】算** ⇒ ★★ 先落盘，再对文件反复读
    #   ③ ★★ `grep -vc` 在空输入/多文件时会输出**多行** ⇒ ★★ `[[ "0\n0" -gt 0 ]]` 语法错
    #      ⇒ ★★ 计数一律用 awk（★ `!/^#/{c++}` ⇒ ★★ 不依赖字段分隔 ⇒ ★★ 各实现都稳）
    # ★★ 这里**不能**用 `local`（★ 本分支在顶层 `case` 里、★ 不在函数内
    #    ⇒ bash 会报 "local: can only be used in a function" ⇒ ★★ 退出码 141）
    snap="$(mktemp "${TMPDIR:-/tmp}/crc-snap.XXXXXX")" || { printf 'FATAL: 建临时文件失败\n' >&2; exit 64; }
    cat > "$snap"
    classify_file "$snap"
    crc=$?
    rm -f "$snap"
    exit "$crc"
    ;;
  -h|--help|help) usage; exit 0 ;;
  *) printf 'FATAL: 未知模式（可用：probe / classify / --help）\n' >&2; usage; exit 64 ;;
esac

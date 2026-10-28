#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/ensure-pg-slow-query-observability.sh
# Purpose:       Ensure the local PostgreSQL instance has the **slow SQL
#                observability facilities** enabled, and that they are
#                *actually usable* (not merely installed).
# Why:           r1041 round (2026-10-28) measured a triple gap on this
#                machine:
#                  · shared_preload_libraries = 'citus'   (missing pg_stat_statements)
#                  · log_min_duration_statement = -1      (slow query log off)
#                  · logging_collector = off              (log never lands on disk)
#                ⇒ pg_stat_statements had CREATE EXTENSION but the postmaster
#                never preloaded it ⇒ EVERY query against it fails with
#                  ERROR: pg_stat_statements must be loaded via "shared_preload_libraries"
#                ⇒ ⇒ the ≥19 in-repo gates that use pg_stat_statements as their
#                fact source (§143.77~§143.99, ursmcheck §143.19/21/22/29/30/32 …)
#                were **silently fake-green** on this machine
#                ⇒ ⇒ the whole category "slow SQL" was unobservable.
#                ⇒ ★ And the repo's own doc already states the expected value
#                (docs/06-deployment/02-database/local-pg-sync-from-252.md):
#                  'citus,citus_columnar,pg_stat_statements'
#                ⇒ ★ i.e. doc vs. reality had drifted, with nothing alarming.
#
# What it does:   1. Read the CURRENT settings (read-only) and report.
#                2. If the required libraries are missing from
#                   shared_preload_libraries ⇒ append/patch the setting in
#                   postgresql.auto.conf (★ direct file write, NOT ALTER SYSTEM —
#                   the quoting of comma-separated string lists under ALTER SYSTEM
#                   is a known foot-gun; see the same doc §5 Q6).
#                3. Ensure log_min_duration_statement > -1 and logging_collector=on.
#                4. Restart the container so the postmaster reloads
#                   shared_preload_libraries (★ required — the setting is
#                   postmaster-level and cannot be reloaded).
#                5. **Verify by execution**: run a real
#                   `SELECT count(*) FROM pg_stat_statements` and a real slow
#                   query, and assert the slow-query log file actually contains a
#                   `duration:` line.
# Policy:        ★ Idempotent. ★ Never touches users/passwords, never initdb,
#                never wipes the data dir. ★ Only the three settings above are
#                written, and only when they are missing/wrong.
#                ★ Defaults to --dry-run? No: it repairs by default but prints
#                every action; use --dry-run to only report.
# Status:        active (introduced in r1041 round, 2026-10-28)
# Changelog:
#   2026-10-28  v1.0  Initial version. Symptom: the r1041 audit ran
#                     `SELECT count(*) FROM pg_stat_statements` and got
#                     "must be loaded via shared_preload_libraries"; the
#                     in-repo gates that depend on it were fake-green.
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/local-dev/ensure-pg-slow-query-observability.sh
#   bash scripts/local-dev/ensure-pg-slow-query-observability.sh --dry-run
#   bash scripts/local-dev/ensure-pg-slow-query-observability.sh --check-only
#   bash scripts/local-dev/ensure-pg-slow-query-observability.sh --container <name>
# -----------------------------------------------------------------------------

set -euo pipefail

CONTAINER="${LLM_GATEWAY_PG_CONTAINER:-llm-gateway-pg}"
DRY_RUN=0
CHECK_ONLY=0

# ★ 期望的 preload 列表（★ 与仓内文档
#   docs/06-deployment/02-database/local-pg-sync-from-252.md 一致）。
REQUIRED_PRELOAD="citus,citus_columnar,pg_stat_statements"
# ★ 慢查询阈值（毫秒）。★ 与 runbook
#   docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md 一致。
REQUIRED_MIN_DURATION="1000"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)    DRY_RUN=1; shift ;;
    --check-only) CHECK_ONLY=1; shift ;;
    --container)  CONTAINER="$2"; shift 2 ;;
    -h|--help)    sed -n '2,60p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

say()  { printf '%s\n' "$*"; }
act()  { printf '  [action] %s\n' "$*"; }
skip() { printf '  [skip]   %s\n' "$*"; }
fail() { printf '  [FAIL]   %s\n' "$*" >&2; }

# ── 0. 容器可达性 ───────────────────────────────────────────────────────────
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  say "ERROR: 容器 '$CONTAINER' 不存在 ⇒ 无法保证慢 SQL 观测设施。"
  say "       提示：仓内的新门 scripts/ursmcheck/pg_slow_query_observability_test.go"
  say "       的【静态层】仍会检查 compose 配置；本脚本只管运行中的实例。"
  exit 1
fi

pg_q() { docker exec "$CONTAINER" psql -U "${PGUSER_NAME:-llm_gateway}" -d "${PGDB_NAME:-llm_gateway}" -tAc "$1" 2>/dev/null; }
pg_q_env() { docker exec "$CONTAINER" psql -U "${PGUSER_NAME:-llm_gateway}" -d "${PGDB_NAME:-llm_gateway}" -tAc "$1" 2>/dev/null; }

# ★ 容器里的 superuser 名可能不是 llm_gateway ⇒ 逐个试。
detect_pg_user() {
  local u
  for u in llm_gateway kxuser postgres "${PGUSER_NAME:-}"; do
    [[ -z "$u" ]] && continue
    if docker exec "$CONTAINER" psql -U "$u" -d "${PGDB_NAME:-llm_gateway}" -tAc "SELECT 1" >/dev/null 2>&1; then
      echo "$u"; return 0
    fi
  done
  return 1
}

PGUSER_NAME="$(detect_pg_user || true)"
if [[ -z "$PGUSER_NAME" ]]; then
  say "ERROR: 找不到可用的 PG 用户（试过 llm_gateway / kxuser / postgres）"
  exit 1
fi
say "容器：$CONTAINER ｜ PG 用户：$PGUSER_NAME"

# ── 1. 读当前状态 ───────────────────────────────────────────────────────────
cur_preload="$(pg_q 'SHOW shared_preload_libraries')"
cur_min_dur="$(pg_q 'SHOW log_min_duration_statement')"
cur_collector="$(pg_q 'SHOW logging_collector')"
say ""
say "── 当前设置 ──"
say "  shared_preload_libraries   = ${cur_preload:-<空>}"
say "  log_min_duration_statement = ${cur_min_dur:-<空>}"
say "  logging_collector          = ${cur_collector:-<空>}"

# ── 2. 判据 ─────────────────────────────────────────────────────────────────
needs_preload=0
IFS=',' read -r -a _cur_parts <<< "$cur_preload"
for want in ${REQUIRED_PRELOAD//,/ }; do
  found=0
  for part in "${_cur_parts[@]}"; do
    [[ "$(echo "$part" | tr -d ' ')" == "$want" ]] && found=1 && break
  done
  [[ $found -eq 0 ]] && { needs_preload=1; say "  ⇒ 缺 preload 库：$want"; }
done

needs_min_dur=0
if [[ "$cur_min_dur" == "-1" || -z "$cur_min_dur" ]]; then
  needs_min_dur=1
fi

needs_collector=0
if [[ "$(echo "$cur_collector" | tr -d ' ')" != "on" ]]; then
  needs_collector=1
fi

# ── 3. 报告 ─────────────────────────────────────────────────────────────────
if [[ $needs_preload -eq 0 && $needs_min_dur -eq 0 && $needs_collector -eq 0 ]]; then
  say ""
  say "✅ 配置层面：三项设置均已就位。"
else
  say ""
  say "⚠️  配置层面：发现缺口 ⇒ 慢 SQL 观测不可用（⇒ 仓内依赖 pg_stat_statements 的门会假绿）。"
fi

# ── 4. 验证「真的可用」（最终判据）────────────────────────────────────────
verify_usable() {
  say ""
  say "── 验证「真的可用」（最终判据：能执行，而不是「已安装」）──"
  local n
  if n="$(pg_q 'SELECT count(*) FROM pg_stat_statements')"; then
    say "  ✅ SELECT count(*) FROM pg_stat_statements = $n （扩展可查）"
  else
    fail "SELECT count(*) FROM pg_stat_statements 失败 ⇒ 扩展未 preload ⇒ 所有依赖它的门都是假绿"
    return 1
  fi
  # ★ 实测慢查询日志落盘：跑一条 > 阈值 的语句，再去日志文件里找 duration 行。
  say "  … 触发一条慢查询以验证日志落盘（pg_sleep）"
  pg_q 'SELECT pg_sleep(1.2)' >/dev/null 2>&1 || true
  sleep 2
  local loghit
  loghit="$(docker exec "$CONTAINER" bash -c \
    "cat /var/lib/postgresql/data/log/*.log 2>/dev/null | grep -c 'duration:' || true" 2>/dev/null | tr -d ' ')"
  if [[ "${loghit:-0}" -gt 0 ]]; then
    say "  ✅ 慢查询日志里有 $loghit 条 duration 行（已落盘）"
  else
    fail "慢查询日志里没有 duration 行 ⇒ logging_collector 仍未生效 ⇒ 日志只在 stderr"
    return 1
  fi
  return 0
}

if [[ $CHECK_ONLY -eq 1 ]]; then
  say ""
  say "--check-only：只报告，不改任何东西。"
  if [[ $needs_preload -eq 0 && $needs_min_dur -eq 0 && $needs_collector -eq 0 ]]; then
    verify_usable || exit 1
  else
    say "  （配置有缺口 ⇒ 跳过可用性验证）"
    exit 1
  fi
  exit 0
fi

# ── 5. 修复 ─────────────────────────────────────────────────────────────────
if [[ $needs_preload -eq 0 && $needs_min_dur -eq 0 && $needs_collector -eq 0 ]]; then
  verify_usable || exit 1
  exit 0
fi

if [[ $DRY_RUN -eq 1 ]]; then
  say ""
  say "--dry-run：将要做的动作（实际未执行）："
  [[ $needs_preload -eq 1 ]]    && act "在 postgresql.auto.conf 写 shared_preload_libraries = '$REQUIRED_PRELOAD'"
  [[ $needs_min_dur -eq 1 ]]    && act "在 postgresql.auto.conf 写 log_min_duration_statement = $REQUIRED_MIN_DURATION"
  [[ $needs_collector -eq 1 ]]  && act "在 postgresql.auto.conf 写 logging_collector = on"
  act "重启容器 ${CONTAINER}（★ shared_preload_libraries 是 postmaster 级，必须重启才生效）"
  act "验证：pg_stat_statements 可查 + 慢查询日志落盘"
  exit 0
fi

say ""
say "── 修复 ──"
CONF_IN_CONTAINER=/var/lib/postgresql/data/postgresql.auto.conf
# ★ 把要追加的配置先在宿主侧算好，再整体写进容器 ——
#   （★ 早期版本用嵌套 heredoc + 单引号转义 ⇒ bash 报 unexpected EOF；
#     ⇒ 且变量名后紧跟全角括号会被 bash 吞进变量名 ⇒ 两个坑一起规避。）
# ★★ 逐行构造补丁内容 —— ★★ 【不能用】`$(cat <<EOF ...)` 命令替换：
#   ★★ 命令替换会【剥掉尾部换行】⇒ ⇒ 下一段内容会与本段【粘成一行】
#   ★★ ⇒ 本轮实测踩到：设置行被粘到注释行尾巴上 ⇒ **设置被注释掉 ⇒
#   ★★ 脚本报告「已写入」但 PG 实际没生效**（★ 且读数仍是旧值 ⇒
#   ★★ 幸好脚本的【最终判据是「能执行」】而不是「文件里有这行」⇒ ⇒ 才被抓到）。
#   ⇒ ⇒ 改成用数组逐行 push，每行自带 \n。
# ★★ 先剥掉【旧的同名设置行】—— ★★ PG 里同名设置【后者覆盖前者】，
#   ★★ 但旧行留在文件里会【误导人】（★ 本轮实测：文件里有 3 处
#   ★★ shared_preload_libraries，后写的生效 ⇒ 读文件的人会以为
#   ★★ 第 1 处就是生效值）⇒ ⇒ ★★ 剥掉 = 只留一份 = 读的人与运行的一致。
_strip_old_settings() {
  local key="$1"
  # 只删【未注释的】设置行（★ 注释里的历史说明要保留 —— 它们是审计线索）。
  grep -vE "^[[:space:]]*${key}[[:space:]]*=" "$1"
}

_patch_lines=()
_patch_lines+=("# ── ensure-pg-slow-query-observability.sh 自动写入 ──")
_patch_lines+=("#   慢 SQL 观测设施：pg_stat_statements 必须 postmaster preload，")
_patch_lines+=("#   否则扩展装了也查不了 ⇒ 仓内依赖它的门全是假绿。")
# ★★ 【无条件重写全部四项】—— ★★ 因为下面会剥掉旧的同名设置行 ⇒
#   ★★ 若「检测时已就位」就不重写 ⇒ ⇒ 剥完就没有了 ⇒ ⇒
#   ★★ **运行值与读数脱节**（★ 本轮实测踩到：修复后读数显示
#   ★★ log_min_duration_statement = -1 / logging_collector = off
#   ★★ —— 明明「检测」时它们是就位的）⇒ ⇒ ★★ 剥与写必须【成对且一致】。
_patch_lines+=("shared_preload_libraries = '${REQUIRED_PRELOAD}'")
_patch_lines+=("log_min_duration_statement = ${REQUIRED_MIN_DURATION}")
_patch_lines+=("logging_collector = on")
_patch_lines+=("log_line_prefix = '%m [%p] %q%u@%d '")
_patch_body="$(printf '%s\n' "${_patch_lines[@]}")"

# ★ 两个文件都先落到宿主机，再 docker cp 进容器（★ 理由见上方注释）。
_patch_tmp="$(mktemp "${TMPDIR:-/tmp}/llmgw-pg-obs.XXXXXX")"
_apply_tmp="$(mktemp "${TMPDIR:-/tmp}/llmgw-pg-apply.XXXXXX")"
trap 'rm -f "$_patch_tmp" "$_apply_tmp"' EXIT
printf '%s' "$_patch_body" > "$_patch_tmp"
cat > "$_apply_tmp" <<'APPLY_EOF'
set -e
F=/var/lib/postgresql/data/postgresql.auto.conf
TS=$(date +%Y%m%dT%H%M%S)
if [ ! -f "$F" ]; then
  echo 'ERROR: auto.conf 不存在 ⇒ 容器数据目录异常' >&2
  exit 1
fi
cp "$F" "${F}.bak-${TS}"
# ★ 剥掉旧的同名设置行（★ 只删未注释的；注释里的历史说明保留）
for key in shared_preload_libraries log_min_duration_statement logging_collector log_line_prefix; do
  tmp=$(mktemp)
  grep -vE "^[[:space:]]*${key}[[:space:]]*=" "$F" > "$tmp" || true
  cat "$tmp" > "$F"
  rm -f "$tmp"
done
cat /tmp/llmgw-pg-obs.patch >> "$F"
rm -f /tmp/llmgw-pg-obs.patch
echo "  [ok] auto.conf 已写入（备份：${F}.bak-${TS}）"
APPLY_EOF
docker cp "$_patch_tmp" "$CONTAINER:/tmp/llmgw-pg-obs.patch" >/dev/null
docker cp "$_apply_tmp" "$CONTAINER:/tmp/llmgw-pg-apply.sh" >/dev/null
docker exec "$CONTAINER" bash /tmp/llmgw-pg-apply.sh
docker exec "$CONTAINER" rm -f /tmp/llmgw-pg-apply.sh

act "重启容器 ${CONTAINER}（★ postmaster 级设置必须重启）"
docker restart "$CONTAINER" >/dev/null
say "  … 等待 PG 就绪"
for i in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -U "$PGUSER_NAME" -d "${PGDB_NAME:-llm_gateway}" >/dev/null 2>&1; then
    say "  [ok] PG 就绪（${i}s）"; break
  fi
  [[ $i -eq 60 ]] && { fail "PG 60 秒未就绪"; exit 1; }
  sleep 1
done

say ""
say "── 修复后读数 ──"
say "  shared_preload_libraries   = $(pg_q 'SHOW shared_preload_libraries')"
say "  log_min_duration_statement = $(pg_q 'SHOW log_min_duration_statement')"
say "  logging_collector          = $(pg_q 'SHOW logging_collector')"

verify_usable || exit 1
say ""
say "✅ 慢 SQL 观测设施已就位且验证可用。"

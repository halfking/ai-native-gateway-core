#!/usr/bin/env bash
# ── verify-object-census-drift.sh ────────────────────────────────────────────
# R164：★ 把「底账快照是否过期」变成【一次可执行的只读对拍】。
#
# ★★ 为什么需要它（R161/R162/R163 三轮都把同一句话写进 §9「诚实边界」）：
#
#   「底账是【静态快照】⇒ ⇒ 『快照过期』不可机器判」
#
# ★★ 本脚本就是那条判据的【可执行形态】：
#   · 对每个环境跑一次 `db-object-census.py verify`
#   · 该模式把【此刻现场】与【仓内快照】逐条对拍
#   · ★★ 分两类报：**结构漂移**（★ 对象没了 / 多了 / 形态变了）与
#     **指标漂移**（★ 只有大小在动 ⇒ ★ autovacuum/analyze 每次都会动 ⇒ 不算漂移）
#   · ★★ 退出码：0 无结构漂移 / 1 有结构漂移 / 2 无法判定
#
# ★★ 三条纪律：
#
#   ① ★ **只读**：★ 全程只 `SELECT`；★ 采集 SQL 由工具生成（★ 不手搓 ⇒ 口径不会分叉）；
#      ★ SQL 内含 `SET statement_timeout = '120s'`。
#   ② ★ **凭据不回显**：★ DSN 只走环境变量（★ 不进 argv）；★ 本脚本不打印 DSN。
#   ③ ★ **不许静默降级**：★ 「无法判定」必须是退出码 2（★ 绝不与「没漂移」的 0 混同）。
#
# 用法：
#   GUARD_DSN='<local dsn>' scripts/audit/verify-object-census-drift.sh            # ★ 只对 local
#   CENSUS_FRESH_DSN='<fresh dsn>' scripts/audit/verify-object-census-drift.sh   # ★ 加 fresh
#   scripts/audit/verify-object-census-drift.sh --all                             # ★ 加 252 / 211
#
# ★ 环境开关（★ 全部可覆盖）：
#   CENSUS_ARTIFACT   默认 scripts/audit/asset/db-object-census.tsv
#   GUARD_DSN         local 的 DSN（★ 只走环境变量）
#   CENSUS_FRESH_DSN  fresh 的 DSN（★ 不设 ⇒ 跳过 fresh）
#   CENSUS_252_SSH    默认 252
#   CENSUS_211_SSH    默认 root@192.168.31.211
#
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}" || exit 2

PY="${PY:-python3}"
TOOL="scripts/audit/db-object-census.py"
ARTIFACT="${CENSUS_ARTIFACT:-scripts/audit/asset/db-object-census.tsv}"
SSH_252="${CENSUS_252_SSH:-252}"
SSH_211="${CENSUS_211_SSH:-root@192.168.31.211}"
PSQL="${GUARD_PSQL:-psql}"

ALL_ENVS=0
for a in "$@"; do
  case "$a" in
    --all) ALL_ENVS=1 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done

if [ ! -f "${ARTIFACT}" ]; then
  echo "ERROR: artifact not found: ${ARTIFACT}" >&2
  exit 2
fi

# ★ 采集 SQL 由工具生成（★ ★ 不许手搓 ⇒ ★ 两边口径不可能分叉）。
CENSUS_SQL_FILE="$(mktemp -t kx-census-sql.XXXXXX.sql)"
cleanup() { rm -f "${CENSUS_SQL_FILE}"; }
trap cleanup EXIT
if ! "${PY}" "${TOOL}" census-sql > "${CENSUS_SQL_FILE}"; then
  echo "ERROR: failed to render census SQL from ${TOOL}" >&2
  exit 2
fi

worst=0
note_rc() {
  # ★ 汇总：★ 任一环境 rc=2 ⇒ 整体 2（★ 无法判定压过漂移）；★ 否则任一 rc=1 ⇒ 整体 1。
  local rc="$1"
  if [ "${rc}" = "2" ]; then
    worst=2
  elif [ "${rc}" = "1" ] && [ "${worst}" != "2" ]; then
    worst=1
  fi
}

run_local() {
  local env="$1" dsn="$2"
  if [ -z "${dsn}" ]; then
    echo "SKIP ${env}: no DSN (★ 不许把「没跑」记成「没漂移」)" >&2
    note_rc 2
    return
  fi
  echo "── verify ${env} ──" >&2
  GUARD_DSN="${dsn}" GUARD_PSQL="${PSQL}" "${PY}" "${TOOL}" verify \
    --env "${env}" --census "${ARTIFACT}"
  local rc=$?
  note_rc "${rc}"
  return 0
}

run_remote() {
  local env="$1" host="$2" remote_cmd="$3"
  echo "── verify ${env} ──" >&2
  ssh "${host}" "${remote_cmd}" < "${CENSUS_SQL_FILE}" 2>/tmp/kx-census-drift-${env}.err \
    | "${PY}" "${TOOL}" verify --env "${env}" --census "${ARTIFACT}" --from-stdin
  local rc="${PIPESTATUS[1]}"
  note_rc "${rc}"
  return 0
}

run_local local "${GUARD_DSN:-}"

if [ -n "${CENSUS_FRESH_DSN:-}" ]; then
  run_local fresh "${CENSUS_FRESH_DSN}"
fi

if [ "${ALL_ENVS}" = "1" ]; then
  run_remote 252 "${SSH_252}" "podman exec -i pg-252-pg17 psql -U llm_gateway -d llm_gateway -tA -q"
  run_remote 211 "${SSH_211}" "docker exec -i acc-pg17 psql -U llm_gateway -d llm_gateway -tA -q"
fi

case "${worst}" in
  0) echo "RESULT: NO STRUCTURAL DRIFT" >&2 ;;
  1) echo "RESULT: STRUCTURAL DRIFT" >&2 ;;
  *) echo "RESULT: UNDETERMINED (★ 「没法判定」≠「没漂移」)" >&2 ;;
esac
exit "${worst}"

#!/usr/bin/env bash
# =====================================================================
# scripts/checks/sync-baseline-copies.sh — 基线副本同步器（三处 × 三文件）
#
# ★ 2026-11-09 新增（第 0 批零风险清理 · 项2）。
#
# # 它取代的动作
#
# ★ 此前「基线副本」的三处位置是**手工各改一遍**的 ⇒ 三处分叉的风险
#   由一道门兜着（sql/schema/baseline_copies_identity_test.go）⇒ 但「检测」
#   不等于「消除手改面」⇒ ⇒ 本脚本把「手改三处」变成「改一处 + 跑一条命令」。
#
# # SSOT 约定（与 sql/schema/baseline_copies_identity_test.go 钉的是同一件事）
#
#   sql/schema/                                   ← ★ SSOT（正典）
#   installer/cmd/llm-gw-installer/embeddata/     ← 派生副本（新装库 //go:embed）
#   deploy/sql/schemas/baseline/                  ← 派生副本（随包分发基线）
#
#   文件：00-prereqs.sql / 01-schema.sql / 02-seed.sql
#
# ★⚠️ **不要手改这三份 dump 里的任何一行**：
#   它们的**来源**是活库的 `pg_dump`（sql/scripts/dump-schema.sh →
#   scripts/_lib/db-init-lib.sh），生成器**只输出一份**到临时目录 ⇒
#   ⇒ 手改会被下一次 dump 覆盖，且会让 dump **不再等于真库形状**。
#   ⇒ 本脚本只保证「三处彼此相同」，**不**保证「等于活库」。
#
# # 用法
#
#   bash scripts/checks/sync-baseline-copies.sh            # 检查（默认，失败 exit 1）
#   bash scripts/checks/sync-baseline-copies.sh --apply    # 从 SSOT 同步到两份副本
#   bash scripts/checks/sync-baseline-copies.sh --check    # 同默认（显式写法）
#
# ★ 幂等：--apply 跑第二遍是 no-op（内容已是最新）⇒ 可安全重复执行。
# ★ 不碰工作区其它任何文件 ⇒ 出错也不会污染仓库。
#
# ★⚠️ **本脚本里所有变量引用一律写成 ${var}**（★ 即使后面跟的是空格）：
#   原因见 scripts/guards/check-shell-fullwidth-var.sh —— bash 在 UTF-8 locale
#   下会把紧跟变量名的**全角标点**（，：（））吞进变量名 ⇒ `$dst（` 会被解析成
#   变量 `dst（` ⇒ set -u 下直接中止。⇒ 一律加花括号 ⇒ 永远安全。
# =====================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

SSOT_DIR="sql/schema"
DERIVED_DIRS="installer/cmd/llm-gw-installer/embeddata deploy/sql/schemas/baseline"
BASELINE_FILES="00-prereqs.sql 01-schema.sql 02-seed.sql"

MODE="check"
case "${1:---check}" in
  --check) MODE="check" ;;
  --apply) MODE="apply" ;;
  -h|--help)
    echo "usage: $0 [--check|--apply]"
    echo "  --check  检查三处副本是否逐字节相同（默认）"
    echo "  --apply  从 SSOT（sql/schema/）同步到两份派生副本"
    exit 0
    ;;
  *)
    echo "ERROR: 未知参数 '$1'（只接受 --check / --apply / --help）" >&2
    exit 64
    ;;
esac

FAIL=0
SYNCED=0

for f in ${BASELINE_FILES}; do
  src="${SSOT_DIR}/${f}"
  if [ ! -f "${src}" ]; then
    echo "ERROR: SSOT 缺失 ${src}" >&2
    FAIL=1
    continue
  fi

  for d in ${DERIVED_DIRS}; do
    dst="${d}/${f}"
    if [ ! -d "${d}" ]; then
      echo "ERROR: 副本目录缺失 ${d}" >&2
      FAIL=1
      continue
    fi

    if [ ! -f "${dst}" ]; then
      echo "ERROR: 副本缺失 ${dst}" >&2
      FAIL=1
      continue
    fi

    if cmp -s "${src}" "${dst}"; then
      continue
    fi

    case "${MODE}" in
      apply)
        cp "${src}" "${dst}"
        echo "OK 已同步 ${dst} <- ${src}"
        SYNCED=$((SYNCED + 1))
        ;;
      check)
        echo "ERROR: 副本与 SSOT 不一致 ${dst} 正典为 ${src}" >&2
        echo "  => 修法: bash scripts/checks/sync-baseline-copies.sh --apply" >&2
        FAIL=1
        ;;
    esac
  done
done

if [ "${MODE}" = "apply" ]; then
  if [ "${FAIL}" -eq 0 ]; then
    echo "PASS 基线副本同步完成 本次同步 ${SYNCED} 个文件 SSOT=${SSOT_DIR}/"
  else
    echo "ERROR 基线副本同步未完成 缺少 SSOT 或副本目录" >&2
  fi
else
  if [ "${FAIL}" -eq 0 ]; then
    echo "PASS 基线副本一致 文件=${BASELINE_FILES} 位置=${DERIVED_DIRS} 逐字节"
  else
    echo "ERROR 基线副本存在分叉 详见上方逐条报错" >&2
  fi
fi

exit "${FAIL}"

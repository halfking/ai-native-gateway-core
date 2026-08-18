#!/bin/bash
# ========================================
# 修订历史 / Revision History
# ========================================
# | 版本 | 日期       | 变更                    | 作者   |
# |------|------------|-------------------------|--------|
# | 1.0  | 2026-08-18 | 初始版本（swap 回滚）   | Infra  |
# ========================================
#
# bodies-hot-repack-rollback.sh — 将 swap 重写的备份表换回原名
#
# ⚠️ 仅适用于 swap 后【立即】发现问题的回滚：swap 之后新表上发生的写入
#    会被换回操作丢弃。若 swap 后已有业务流量写入，请勿使用本脚本，
#    改为人工比对合并。
#
# 用法：
#   ./scripts/partition/bodies-hot-repack-rollback.sh --env=252 \
#       --backup=request_logs_bodies_hot_bloat_backup_20260819_021500
#
# 执行前会打印备份表存在性与当前两表行数，确认后才换名（--yes 跳过确认）。

set -euo pipefail

ENV_ARG="local"
BACKUP=""
CONFIRM=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env=*)     ENV_ARG="${1#--env=}" ;;
    --backup=*)  BACKUP="${1#--backup=}" ;;
    --yes|-y)    CONFIRM=true ;;
    -h|--help)   sed -n '3,17p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
  shift
done

[[ -n "$BACKUP" ]] || { echo "--backup= 必填（bodies-hot-repack.sh swap 输出的备份表名）" >&2; exit 1; }
[[ "$BACKUP" =~ ^[a-z_][a-z0-9_]*$ ]] || { echo "invalid backup table name" >&2; exit 1; }

# 从备份表名反推原表名（去掉 _bloat_backup_<ts> 后缀）
TABLE="${BACKUP%%_bloat_backup_*}"
[[ -n "$TABLE" ]] || { echo "无法从备份名反推原表名" >&2; exit 1; }

if [[ "$ENV_ARG" == "local" ]]; then
  run_sql() { docker exec -i llm-gateway-pg psql -X -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 "$@"; }
elif [[ "$ENV_ARG" == "252" ]]; then
  # shellcheck disable=SC1091
  source "$HOME/workspace/ai-native-tools/envs/loader.sh" --all --project llm-gateway-go --server 115.29.212.252 --mode plain >/dev/null 2>&1 || {
    echo "envs loader 失败（252 凭据）" >&2; exit 1; }
  run_sql() { PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -X -h 127.0.0.1 -p 15432 -U "$COMMON_PG_SUPERUSER" -d llm_gateway -v ON_ERROR_STOP=1 "$@"; }
else
  echo "unknown --env: $ENV_ARG (local|252)" >&2; exit 1
fi

echo "== 回滚前置检查 =="
run_sql -Atc "SELECT 1 FROM pg_class WHERE relname='$BACKUP';" | grep -q 1 \
  || { echo "✗ 备份表 public.$BACKUP 不存在" >&2; exit 1; }
run_sql -c "SELECT (SELECT count(*) FROM public.$TABLE) AS current_rows,
                   (SELECT count(*) FROM public.$BACKUP) AS backup_rows;"

if [[ "$CONFIRM" != true ]]; then
  read -r -p "确认回滚？当前表写入将丢失，备份表 $BACKUP 将换回 $TABLE [y/N] " ans
  [[ "$ans" == "y" || "$ans" == "Y" ]] || { echo "已取消"; exit 0; }
fi

TS=$(date +%Y%m%d_%H%M%S)
run_sql <<SQL
BEGIN;
LOCK TABLE public.$TABLE IN ACCESS EXCLUSIVE MODE;
ALTER TABLE public.$TABLE RENAME TO ${TABLE}_aborted_${TS};
ALTER TABLE public.$BACKUP RENAME TO $TABLE;
COMMIT;
ANALYZE public.$TABLE;
SQL
echo "== 回滚完成：$BACKUP → $TABLE（当前表改名为 ${TABLE}_aborted_${TS}，确认后可 DROP）"

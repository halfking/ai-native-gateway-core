#!/usr/bin/env bash
# =============================================================================
# check-nvidia-nim-outbound-model-id-drift.sh
# 2026-07-14: 防回归巡检
#
# 巡检 252 (115.29.212.252) 上 pg-252-pg17/llm_gateway 的 provider_models 表，
# 确认 NVIDIA NIM provider 上
#   * raw_model_name = 'z-ai/glm-5.2' 的行 outbound_model_name == 'z-ai/glm-5.2'
#   * raw_model_name = 'minimaxai/minimax-m3' 的行 outbound_model_name == 'minimaxai/minimax-m3'
#   * raw_model_name = 'minimaxai/minimax-m2.7' 的行 outbound_model_name == 'minimaxai/minimax-m2.7'
# 否则视为 drift：上游 NVIDIA NIM endpoint
# (https://integrate.api.nvidia.com/v1/chat/completions) 收到错误的 model
# 字段时返回 model_not_found（详见 docs/changelogs/2026-07-13-glm52-outbound-modelname-drift.md
# 的姊妹事件）。
#
# 互补关系：
#   - check-glm52-outbound-modelname-drift.sh  关注 glm-5.2 → glm-5.1 这类版本错位
#   - 本脚本                        关注 NIM 上三个目标模型的完整 publisher id 是否对齐
#
# 用法：
#   PGPASSWORD=xxx ./check-nvidia-nim-outbound-model-id-drift.sh
#   PGPASSWORD=xxx REMOTE_MODE=1 \
#     REMOTE_SSH="ssh -p 25022 root@115.29.212.252" \
#     ./check-nvidia-nim-outbound-model-id-drift.sh
#
# 退出码：
#   0 = 没有 drift
#   1 = 发现 drift
#   2 = 连接 / 执行错误
# =============================================================================

set -euo pipefail

PGHOST="${PGHOST:-172.16.2.210}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-llm_gateway}"
PGDATABASE="${PGDATABASE:-llm_gateway}"
: "${PGPASSWORD:?PGPASSWORD env var is required}"

REMOTE_MODE="${REMOTE_MODE:-0}"
REMOTE_SSH="${REMOTE_SSH:-ssh -p 25022 -o StrictHostKeyChecking=no root@115.29.212.252}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SQL_DIR="${SCRIPT_DIR%/sql/fixes}/sql/fixes"
SQL_FILE="$SQL_DIR/_check_nvidia_nim_$$.sql"
trap 'rm -f "$SQL_FILE"' EXIT

# 远端模式：走 ssh + docker exec；本地模式：直连。
# 与 check-glm52-outbound-modelname-drift.sh 保持一致：远端从 stdin 喂 SQL，
# 避免 `docker exec ... psql -f FILE` 双层 shell 解析把 -f 当成 SQL。
run_sql_file() {
    local file="$1"
    if [[ "$REMOTE_MODE" == "1" ]]; then
        $REMOTE_SSH "docker exec -i pg-252-pg17 psql -U $PGUSER -d $PGDATABASE -tA" < "$file"
    else
        PGPASSWORD="$PGPASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -tA < "$file"
    fi
}

# ---------- 1. 三个目标模型 outbound 是否等于 raw ----------
cat > "$SQL_FILE" <<'SQL'
SELECT COUNT(*)
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.code = 'nvidia'
  AND pm.raw_model_name IN (
      'z-ai/glm-5.2',
      'minimaxai/minimax-m3',
      'minimaxai/minimax-m2.7'
  )
  AND (
      pm.outbound_model_name IS NULL
      OR pm.outbound_model_name <> pm.raw_model_name
  );
SQL
DRIFT_COUNT=$(run_sql_file "$SQL_FILE" | tr -d '[:space:]' | head -1)
DRIFT_COUNT="${DRIFT_COUNT:-0}"

# ---------- 2. 严格按目标 id 列出最终值，供操作员快速核对 ----------
cat > "$SQL_FILE" <<'SQL'
SELECT
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, '<NULL>') AS outbound_model_name,
    pm.available,
    pm.updated_at
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.code = 'nvidia'
  AND pm.raw_model_name IN (
      'z-ai/glm-5.2',
      'minimaxai/minimax-m3',
      'minimaxai/minimax-m2.7'
  )
ORDER BY pm.raw_model_name, pm.id;
SQL
FINAL_ROWS=$(run_sql_file "$SQL_FILE")

echo "[check-nvidia-nim-outbound-model-id-drift] $(date -Iseconds)  pg=$PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
echo "[check-nvidia-nim-outbound-model-id-drift] target rows with non-canonical outbound: $DRIFT_COUNT"

if [[ "${DRIFT_COUNT:-0}" -gt 0 ]]; then
    echo
    echo "[check-nvidia-nim-outbound-model-id-drift] ❌ DRIFT DETECTED"
    echo
    echo "  当前 NIM 三个目标模型的 (raw, outbound, available, updated_at)："
    printf '%s\n' "$FINAL_ROWS" | sed 's/^/    /'
    echo
    echo "  修复建议："
    echo "    sql/migrations/startup/394_nvidia_nim_outbound_model_id.sql"
    echo "    （幂等；安全更新，仅锁定到 integrate.api.nvidia.com/v1/models 当前接受的完整 id）"
    exit 1
fi

echo
echo "  当前 NIM 三个目标模型的 (raw, outbound, available, updated_at)："
printf '%s\n' "$FINAL_ROWS" | sed 's/^/    /'
echo
echo "[check-nvidia-nim-outbound-model-id-drift] ✅ 无 drift"
exit 0

#!/usr/bin/env bash
# =============================================================================
# check-glm52-outbound-modelname-drift.sh
# 2026-07-13: 防回归巡检
#
# 巡检 252 (115.29.212.252) 上 pg-252-pg17/llm_gateway 的 provider_models 表，
# 确保任何 raw_model_name 形如 glm-5.2 的行 outbound_model_name != 'glm-5.1'。
#
# 历史：2026-06-15 ~ 2026-07-03 期间，6 个 provider 上的 glm-5.2 offer
# outbound_model_name 全部被错填为 'glm-5.1'，导致
# provider/client.go:749 cand.RawModel='glm-5.1'，
# executor_chat.go:replaceModelInRequestBody 把请求体 model 字段替换为 glm-5.1
# 再发给上游。详细见 docs/changelogs/2026-07-13-glm52-outbound-modelname-drift.md
#
# 用法：
#   PGPASSWORD=xxx ./check-glm52-outbound-modelname-drift.sh
#   PGPASSWORD=xxx REMOTE_MODE=1 \
#     REMOTE_SSH="ssh -p 25022 root@115.29.212.252" \
#     ./check-glm52-outbound-modelname-drift.sh
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
SQL_DIR="${SCRIPT_DIR%/sql/fixes}/sql/fixes"   # 同目录
SQL_FILE="$SQL_DIR/_check_glm52_inline_$$.sql"
trap 'rm -f "$SQL_FILE"' EXIT

# 远端模式：scp 推到 252，再 docker cp 到 PG 容器内执行。
# 本地模式：直接 psql -f。
run_sql_file() {
    local file="$1"
    if [[ "$REMOTE_MODE" == "1" ]]; then
        # 注意：docker exec + ssh 双层 sh 解析会让 `psql -tAc -f FILE` 出现
        # "-f 被当成 SQL" 的诡异行为；改用 `docker exec ... psql -tA` 并从
        # stdin 喂 SQL（psql 会从 stdin 读 SQL）来避免。
        $REMOTE_SSH "docker exec -i pg-252-pg17 psql -U $PGUSER -d $PGDATABASE -tA" < "$file"
    else
        PGPASSWORD="$PGPASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -tA < "$file"
    fi
}

# ---------- 1. 精确检测：glm-5.2 错指 glm-5.1 ----------
cat > "$SQL_FILE" <<'SQL'
SELECT COUNT(*) FROM provider_models
WHERE raw_model_name ILIKE '%glm-5.2%' AND outbound_model_name = 'glm-5.1';
SQL
DRIFT_COUNT=$(run_sql_file "$SQL_FILE" | tr -d '[:space:]' | head -1)
DRIFT_COUNT="${DRIFT_COUNT:-0}"

# ---------- 2. 跨主版本 drift：raw 与 outbound 双方都形如 "name-X.Y" 或 "name-X-Y"，
#                  且 X.Y 不一致（同样 family 但不同 minor version，疑似数据错位）。
# 注：带日期/后缀的合法映射（如 deepseek-v4-pro-260425 → deepseek-v4-pro）
#     因为 X.Y 段位置错开（raw 端在 pro 之后）会被自然排除。
cat > "$SQL_FILE" <<'SQL'
WITH parsed AS (
    SELECT
        pm.id, pm.raw_model_name, pm.outbound_model_name,
        regexp_match(pm.raw_model_name,
            E'([A-Za-z][A-Za-z0-9_-]*?[A-Za-z_-])-?([0-9]+)[.\\-]([0-9]+)') AS raw_match,
        regexp_match(pm.outbound_model_name,
            E'([A-Za-z][A-Za-z0-9_-]*?[A-Za-z_-])-?([0-9]+)[.\\-]([0-9]+)') AS out_match
    FROM provider_models pm
    WHERE pm.outbound_model_name IS NOT NULL
      AND pm.outbound_model_name <> pm.raw_model_name
),
decoded AS (
    SELECT
        id, raw_model_name, outbound_model_name,
        CASE
            WHEN raw_match IS NULL OR out_match IS NULL THEN NULL
            WHEN raw_match[2] = out_match[2]
             AND raw_match[3] <> out_match[3]
                THEN 'drift'
            ELSE NULL
        END AS kind
    FROM parsed
)
SELECT id || '|' || raw_model_name || '|' || outbound_model_name
FROM decoded WHERE kind = 'drift';
SQL

CROSS_MAJOR_LINES=$(run_sql_file "$SQL_FILE")
CROSS_MAJOR_COUNT=$(printf '%s' "$CROSS_MAJOR_LINES" | awk 'NF{c++} END{print c+0}')

echo "[check-glm52-outbound-modelname-drift] $(date -Iseconds)  pg=$PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
echo "[check-glm52-outbound-modelname-drift] glm-5.2 → glm-5.1 drift rows:        $DRIFT_COUNT"
echo "[check-glm52-outbound-modelname-drift] cross-version (same model family): $CROSS_MAJOR_COUNT"

if [[ "${DRIFT_COUNT:-0}" -gt 0 || "${CROSS_MAJOR_COUNT:-0}" -gt 0 ]]; then
    echo
    echo "[check-glm52-outbound-modelname-drift] ❌ DRIFT DETECTED"
    if [[ "${DRIFT_COUNT:-0}" -gt 0 ]]; then
        cat > "$SQL_FILE" <<'SQL'
SELECT pm.id, pm.provider_id, p.code, pm.raw_model_name, pm.outbound_model_name
FROM provider_models pm
LEFT JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name ILIKE '%glm-5.2%' AND pm.outbound_model_name = 'glm-5.1'
ORDER BY pm.id;
SQL
        echo "  glm-5.2 错指 glm-5.1 的 provider_models 行："
        run_sql_file "$SQL_FILE" | sed 's/^/    /'
    fi
    if [[ "${CROSS_MAJOR_COUNT:-0}" -gt 0 ]]; then
        echo "  跨主版本 drift 行（任意模型）："
        printf '%s\n' "$CROSS_MAJOR_LINES" | sed 's/^/    /'
    fi
    echo
    echo "[check-glm52-outbound-modelname-drift] 建议立刻执行："
    echo "    sql/fixes/fix-glm52-alias-drift.sql 第 3 部分（注释中已写好 UPDATE）"
    exit 1
fi

echo "[check-glm52-outbound-modelname-drift] ✅ 无 drift"
exit 0

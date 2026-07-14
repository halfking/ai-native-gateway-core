#!/usr/bin/env bash
# =============================================================================
# apply-nim-case-fixes.sh
# 2026-07-14: 统一应用 NVIDIA NIM 凭据修复 + 全局模型名大小写规范化
#
# 该脚本支持两阶段部署：
#   Phase A — 部署前（必须在 gateway 升级前应用）
#     394_nvidia_nim_outbound_model_id.sql
#         → 锁定 NIM 三模型 raw_model_name ↔ outbound_model_name
#           的版本错位（修复 6 条 glm-5.2 → glm-5.1 类的 drift）。
#     395_provider_models_canonical_raw_name.sql
#         → 新增 provider_models.canonical_raw_name 列并回填，加
#           UNIQUE (provider_id, canonical_raw_name) 索引。
#     396_model_aliases_canonical_lowercase.sql
#         → 下转 model_aliases.raw_name + models_canonical.canonical_name
#           并合并 mixed-case 冲突。
#
#   Phase B — 部署后（gateway 已用新代码稳定运行 ≥24h）
#     397_runtime_logs_lowercase.sql
#         → 把历史 mixed-case 的 request_logs / model_aliases /
#           model_offer_events / model_probe_state /
#           credential_model_stats_1m / credential_model_peak_1m /
#           credential_model_call_history / candidate_failure_logs 全部
#           下转小写，让仪表盘 / incident 查询与新代码完全一致。
#
# 用法：
#   PGPASSWORD=xxx ./apply-nim-case-fixes.sh                 # 跑 Phase A
#   PGPASSWORD=xxx ./apply-nim-case-fixes.sh --phase b      # 跑 Phase B
#   PGPASSWORD=xxx ./apply-nim-case-fixes.sh --phase all     # 跑 A + B
#   PGPASSWORD=xxx REMOTE_MODE=1 \
#     REMOTE_SSH="ssh -p 25022 root@115.29.212.252" \
#     ./apply-nim-case-fixes.sh --phase b
#   PGPASSWORD=xxx DRY_RUN=1 ./apply-nim-case-fixes.sh        # 只打印 SQL
#
# 退出码：
#   0 = 全部成功
#   1 = 任意一步失败
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
DRY_RUN="${DRY_RUN:-0}"

PHASE="a"
for arg in "$@"; do
    case "$arg" in
        --phase)
            shift
            PHASE="${1:-a}"
            ;;
        --phase=*)
            PHASE="${arg#--phase=}"
            ;;
        --help|-h)
            sed -n '2,60p' "$0"
            exit 0
            ;;
        *)
            ;;
    esac
done
case "$PHASE" in
    a|b|all|phase-a|phase-b) ;;
    *)
        echo "[error] unknown phase: $PHASE" >&2
        exit 1
        ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATION_DIR="${SCRIPT_DIR%/sql/fixes}/sql/migrations/startup"

run_sql_file() {
    local label="$1"
    local file="$2"
    if [[ "$DRY_RUN" == "1" ]]; then
        echo "[DRY RUN] ${label}: would apply ${file}"
        return 0
    fi
    echo "[apply] ${label}: ${file}"
    if [[ "$REMOTE_MODE" == "1" ]]; then
        $REMOTE_SSH "docker exec -i pg-252-pg17 psql -U $PGUSER -d $PGDATABASE -v ON_ERROR_STOP=1 -tA" < "$file"
    else
        PGPASSWORD="$PGPASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -v ON_ERROR_STOP=1 -tA < "$file"
    fi
}

# ---------------------------------------------------------------------------
# Pre-flight: 仅在 Phase A 才有 catalog-related 校验。
# ---------------------------------------------------------------------------
if [[ "$DRY_RUN" != "1" && ( "$PHASE" == "a" || "$PHASE" == "all" || "$PHASE" == "phase-a" ) ]]; then
    echo "[preflight] pg=$PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
    run_sql_file "preflight_providers" <(printf '%s\n' "SELECT id, code, base_url FROM providers WHERE code IN ('nvidia','zhipu','sensenova','scnet','minimax','glm-xianyu') ORDER BY id;")
fi

# ---------------------------------------------------------------------------
# Phase A: catalog 不变量。
# ---------------------------------------------------------------------------
if [[ "$PHASE" == "a" || "$PHASE" == "all" || "$PHASE" == "phase-a" ]]; then
    run_sql_file "step_1_outbound_canonicalisation"  "${MIGRATION_DIR}/394_nvidia_nim_outbound_model_id.sql"
    run_sql_file "step_2_canonical_raw_name_backfill" "${MIGRATION_DIR}/395_provider_models_canonical_raw_name.sql"
    run_sql_file "step_3_aliases_lowercase"          "${MIGRATION_DIR}/396_model_aliases_canonical_lowercase.sql"
fi

# ---------------------------------------------------------------------------
# Phase B: runtime logs 不变量。
# ---------------------------------------------------------------------------
if [[ "$PHASE" == "b" || "$PHASE" == "all" || "$PHASE" == "phase-b" ]]; then
    run_sql_file "step_4_runtime_logs_lowercase" "${MIGRATION_DIR}/397_runtime_logs_lowercase.sql"
fi

# ---------------------------------------------------------------------------
# Post-flight: 验证 lowercase 不变量。
# ---------------------------------------------------------------------------
if [[ "$DRY_RUN" != "1" ]]; then
    run_sql_file "postflight_canonical_raw_name_lowercase" <(cat <<'SQL'
SELECT COUNT(*) AS mixed_canonical_raw_name_count
FROM provider_models
WHERE canonical_raw_name <> lower(canonical_raw_name);
SQL
)
    run_sql_file "postflight_canonical_name_lowercase" <(cat <<'SQL'
SELECT COUNT(*) AS mixed_canonical_name_count
FROM models_canonical
WHERE canonical_name <> lower(canonical_name);
SQL
)
    run_sql_file "postflight_alias_raw_name_lowercase" <(cat <<'SQL'
SELECT COUNT(*) AS mixed_alias_count
FROM model_aliases
WHERE raw_name <> lower(raw_name);
SQL
)
    if [[ "$PHASE" == "b" || "$PHASE" == "all" || "$PHASE" == "phase-b" ]]; then
        run_sql_file "postflight_runtime_logs_lowercase" <(cat <<'SQL'
SELECT
    (SELECT COUNT(*) FROM request_logs
       WHERE (client_model IS NOT NULL AND client_model <> lower(client_model))
          OR (outbound_model IS NOT NULL AND outbound_model <> lower(outbound_model))) AS mixed_rl,
    (SELECT COUNT(*) FROM model_offer_events WHERE raw_model_name <> lower(raw_model_name)) AS mixed_offer,
    (SELECT COUNT(*) FROM model_probe_state WHERE raw_model_name <> lower(raw_model_name)) AS mixed_probe,
    (SELECT COUNT(*) FROM credential_model_stats_1m WHERE raw_model <> lower(raw_model)) AS mixed_stats_1m,
    (SELECT COUNT(*) FROM credential_model_peak_1m  WHERE raw_model <> lower(raw_model)) AS mixed_peak_1m,
    (SELECT COUNT(*) FROM credential_model_call_history WHERE raw_model <> lower(raw_model)) AS mixed_call_hist,
    (SELECT COUNT(*) FROM candidate_failure_logs WHERE raw_model_name <> lower(raw_model_name)) AS mixed_cand_fail;
SQL
)
    fi
fi

echo "[done] apply-nim-case-fixes (phase=${PHASE})"

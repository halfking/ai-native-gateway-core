#!/usr/bin/env bash
# 252 PG schema 升级脚本 — 应用完整 apply-db-revision-sequence 轨道
#
# 背景：252 PG 当前 schema 严重滞后（最高版本 V359），缺失升级轨道的所有
#      迁移（659-664 + V371），导致 245/154 生产实例持续报 42P10 错误。
#
# 部署窗口：需停止 245/154 网关服务（避免并发写入冲突）
# 前置条件：
#   1. 252 PG 数据已备份
#   2. 245/154 网关服务已停止（或方案 B 仅热修复 645）
#   3. DATABASE_URL 已配置指向 252 PG
#
# 使用方法：
#   ssh 252
#   cd /opt/llm-gateway-go
#   export DATABASE_URL="postgres://llm_gateway:<pass>@localhost:5432/llm_gateway"
#   bash scripts/deploy-252-schema-upgrade.sh [--dry-run]
#
# 修复清单：
#   - 645: session_bodies_with_current_month 唯一索引（止血 42P10）
#   - 664: provider_error_details 去 message 碎片化（同版本发布约束）
#   - V371: supplier_errors 全链路（hot/stats/promote/ensure/view）
#   - 659/660/661/662/663: 其他轨道迁移

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

DRY_RUN=false
if [[ "${1:-}" == "--dry-run" ]]; then
    DRY_RUN=true
    echo "[DRY-RUN] Schema upgrade simulation (no DB changes)"
fi

: "${DATABASE_URL:?DATABASE_URL must be set to 252 PG connection string}"

# ============================================================================
# Pre-flight checks
# ============================================================================

echo "=== Pre-flight checks ==="

# 1. Confirm 252 PG connectivity
if ! psql "$DATABASE_URL" -Atc "SELECT 1" >/dev/null 2>&1; then
    echo "ERROR: Cannot connect to 252 PG via DATABASE_URL" >&2
    exit 1
fi
echo "✓ 252 PG connectivity confirmed"

# 2. Verify current max migration version
current_max=$(psql "$DATABASE_URL" -Atc "SELECT COALESCE(MAX(version), 'none') FROM schema_migrations" 2>/dev/null || echo "schema_migrations_missing")
echo "✓ Current max migration version: $current_max"

if [[ "$current_max" == "schema_migrations_missing" ]]; then
    echo "WARNING: schema_migrations table missing (fresh install path?)" >&2
fi

# 3. Check for gateway_db_revision_sequences table
if psql "$DATABASE_URL" -Atc "SELECT to_regclass('public.gateway_db_revision_sequences')" 2>/dev/null | grep -q "gateway_db_revision_sequences"; then
    echo "✓ gateway_db_revision_sequences table exists (upgrade track already applied)"
    existing_markers=$(psql "$DATABASE_URL" -Atc "SELECT COUNT(*) FROM gateway_db_revision_sequences" 2>/dev/null || echo "0")
    echo "  Existing markers: $existing_markers"
else
    echo "✓ gateway_db_revision_sequences does not exist (first-time upgrade track run)"
fi

# 4. Check for session_bodies_with_current_month index (P0 indicator)
if psql "$DATABASE_URL" -Atc "SELECT indexname FROM pg_indexes WHERE tablename='session_bodies_hot' AND indexname='session_bodies_with_current_month'" 2>/dev/null | grep -q "session_bodies_with_current_month"; then
    echo "✓ session_bodies_with_current_month index already exists (645 applied)"
else
    echo "⚠ session_bodies_with_current_month index MISSING (645 NOT applied, 42P10 root cause)"
fi

# 5. Check for supplier_errors_hot table (V371 indicator)
if psql "$DATABASE_URL" -Atc "SELECT to_regclass('public.supplier_errors_hot')" 2>/dev/null | grep -q "supplier_errors_hot"; then
    echo "✓ supplier_errors_hot table exists (V371 applied)"
else
    echo "⚠ supplier_errors_hot table MISSING (V371 NOT applied)"
fi

# 6. Check provider_error_details index shape (664 indicator)
idx_def=$(psql "$DATABASE_URL" -Atc "SELECT pg_get_indexdef('idx_provider_error_details_tenant_cred_fingerprint'::regclass)" 2>/dev/null || echo "index_missing")
if [[ "$idx_def" == "index_missing" ]]; then
    echo "⚠ idx_provider_error_details_tenant_cred_fingerprint MISSING"
elif echo "$idx_def" | grep -q "error_message"; then
    echo "⚠ idx_provider_error_details_tenant_cred_fingerprint contains error_message (664 NOT applied, aggregator at risk)"
else
    echo "✓ idx_provider_error_details_tenant_cred_fingerprint does NOT contain error_message (664 applied)"
fi

echo ""
echo "=== Pre-flight summary ==="
echo "DATABASE_URL: $DATABASE_URL"
echo "Current max migration: $current_max"
echo "Upgrade track applied: $(psql "$DATABASE_URL" -Atc "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='gateway_db_revision_sequences')" 2>/dev/null || echo "false")"
echo ""

if [[ "$DRY_RUN" == true ]]; then
    echo "[DRY-RUN] Would execute: bash $ROOT_DIR/scripts/apply-db-revision-sequence.sh"
    echo "[DRY-RUN] Exiting without changes"
    exit 0
fi

# ============================================================================
# Execute upgrade track
# ============================================================================

echo "=== Executing upgrade track ==="
echo "Script: $ROOT_DIR/scripts/apply-db-revision-sequence.sh"
echo ""

if [[ ! -f "$ROOT_DIR/scripts/apply-db-revision-sequence.sh" ]]; then
    echo "ERROR: apply-db-revision-sequence.sh not found at $ROOT_DIR/scripts/" >&2
    exit 1
fi

# Delegate to the canonical upgrade track script
bash "$ROOT_DIR/scripts/apply-db-revision-sequence.sh"

# ============================================================================
# Post-deployment verification
# ============================================================================

echo ""
echo "=== Post-deployment verification ==="

verify_pass=true

# 1. Verify gateway_db_revision_sequences table created
if psql "$DATABASE_URL" -Atc "SELECT COUNT(*) FROM gateway_db_revision_sequences" >/dev/null 2>&1; then
    marker_count=$(psql "$DATABASE_URL" -Atc "SELECT COUNT(*) FROM gateway_db_revision_sequences")
    echo "✓ gateway_db_revision_sequences table exists, $marker_count markers recorded"
else
    echo "✗ gateway_db_revision_sequences table MISSING after upgrade" >&2
    verify_pass=false
fi

# 2. Verify session_bodies_with_current_month index (P0 fix)
if psql "$DATABASE_URL" -Atc "SELECT indexname FROM pg_indexes WHERE tablename='session_bodies_hot' AND indexname='session_bodies_with_current_month'" 2>/dev/null | grep -q "session_bodies_with_current_month"; then
    echo "✓ session_bodies_with_current_month index exists (645 applied)"
else
    echo "✗ session_bodies_with_current_month index STILL MISSING (645 failed?)" >&2
    verify_pass=false
fi

# 3. Verify supplier_errors_hot table (V371)
if psql "$DATABASE_URL" -Atc "SELECT to_regclass('public.supplier_errors_hot')" 2>/dev/null | grep -q "supplier_errors_hot"; then
    echo "✓ supplier_errors_hot table exists (V371 applied)"
else
    echo "✗ supplier_errors_hot table STILL MISSING (V371 failed?)" >&2
    verify_pass=false
fi

# 4. Verify supplier_error_stats table (V371)
if psql "$DATABASE_URL" -Atc "SELECT to_regclass('public.supplier_error_stats')" 2>/dev/null | grep -q "supplier_error_stats"; then
    echo "✓ supplier_error_stats table exists (V371 applied)"
else
    echo "✗ supplier_error_stats table STILL MISSING (V371 failed?)" >&2
    verify_pass=false
fi

# 5. Verify provider_error_details index does NOT contain error_message (664)
idx_def=$(psql "$DATABASE_URL" -Atc "SELECT pg_get_indexdef('idx_provider_error_details_tenant_cred_fingerprint'::regclass)" 2>/dev/null || echo "index_missing")
if [[ "$idx_def" == "index_missing" ]]; then
    echo "✗ idx_provider_error_details_tenant_cred_fingerprint MISSING after upgrade" >&2
    verify_pass=false
elif echo "$idx_def" | grep -q "error_message"; then
    echo "✗ idx_provider_error_details_tenant_cred_fingerprint STILL contains error_message (664 failed?)" >&2
    verify_pass=false
else
    echo "✓ idx_provider_error_details_tenant_cred_fingerprint does NOT contain error_message (664 applied)"
fi

# 6. Verify feature_distribution_stats table (662)
if psql "$DATABASE_URL" -Atc "SELECT to_regclass('public.feature_distribution_stats')" 2>/dev/null | grep -q "feature_distribution_stats"; then
    echo "✓ feature_distribution_stats table exists (662 applied)"
else
    echo "✗ feature_distribution_stats table MISSING (662 failed?)" >&2
    verify_pass=false
fi

# 7. Verify credential_model_index weekly peak index (660)
if psql "$DATABASE_URL" -Atc "SELECT indexname FROM pg_indexes WHERE tablename='credential_model_index' AND indexname LIKE '%weekly%'" 2>/dev/null | grep -q "weekly"; then
    echo "✓ credential_model_index weekly peak index exists (660 applied)"
else
    echo "✗ credential_model_index weekly peak index MISSING (660 failed?)" >&2
    verify_pass=false
fi

echo ""
if [[ "$verify_pass" == true ]]; then
    echo "=== ✓ All verification checks PASSED ==="
    echo ""
    echo "Next steps:"
    echo "1. Restart 245/154 gateway services"
    echo "2. Monitor journalctl for 42P10 errors (should be resolved)"
    echo "3. Verify supplier_errors aggregator starts successfully"
    exit 0
else
    echo "=== ✗ Some verification checks FAILED ===" >&2
    echo "Review the output above and check apply-db-revision-sequence.sh logs" >&2
    exit 1
fi

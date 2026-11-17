#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/f3-trigger-recover-at-isolated-leg.sh
# Purpose:       F3 残留地平线根因的**隔离 PG 真跑腿**。
#                真代码 / 真 schema / 真触发器，逐条断言：
#                  ① 修复前：经 model_offers 视图「置可用」⇒ 恢复标记被丢（缺陷复现）
#                  ② 修复前：视图里显式写 unavailable_recover_at=NULL ⇒ 同样被丢
#                  ③ 应用迁移 972 后：同一动作 ⇒ 恢复标记被正确清空
#                  ④ 应用迁移 972 后：「置不可用」方向仍照旧（显式重试时刻被尊重）
#                  ⑤ 存量清理：残留行被一次性清空
#                  ⑥ 幂等：972 二次执行 ⇒ 0 行改动、无报错
#                ★ 本脚本**不连生产**：只打隔离库（kx-citus @15433）。
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/audit/f3-trigger-recover-at-isolated-leg.sh
#   bash scripts/audit/f3-trigger-recover-at-isolated-leg.sh --keep   # 不重建库
#
# 退出码：0 = 全部断言通过；1 = 有断言失败。
# -----------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

CONTAINER="${AUDIT_PG_CONTAINER:-kx-citus}"
PG_USER="${AUDIT_PG_USER:-kxuser}"
PG_PASS="${AUDIT_PG_PASS:-audit_admin_pw_local_only}"
PG_DB="${AUDIT_PG_DB:-llm_gateway}"
MIG="972_model_offers_trigger_recover_at.sql"

FAILURES=0
STEP=0

q() { docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc "$1" 2>&1; }

sql_file() {
  docker exec -i -e PGPASSWORD="$PG_PASS" "$CONTAINER" \
    psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 --single-transaction < "$1" 2>&1
}

step() { STEP=$((STEP + 1)); printf '\n=== 步骤 %d: %s ===\n' "$STEP" "$1"; }

# assert_eq <期望> <实际> <说明>
assert_eq() {
  local want="$1" got="$2" what="$3"
  if [[ "$want" == "$got" ]]; then
    printf '  ✓ %s（= %s）\n' "$what" "$got"
  else
    printf '  ✗ %s：期望 %s，实得 %s\n' "$what" "$want" "$got"
    FAILURES=$((FAILURES + 1))
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# 0. 干净隔离库
# ─────────────────────────────────────────────────────────────────────────────
step "准备干净隔离库（prereqs + schema + seed）"
if [[ "${1:-}" != "--keep" ]]; then
  bash scripts/audit/start-isolated-pg.sh --recreate >/tmp/f3-leg-recreate.log 2>&1 \
    || { echo "重建隔离库失败"; tail -20 /tmp/f3-leg-recreate.log; exit 1; }
  # ★ 容器重建后 PG 还要跑一遍 initdb：期间 socket 会先短暂接受连接再
  #   "the database system is shutting down" ⇒ 一次性探针会假失败。
  #   判据：**连续两次**查询成功（间隔 2s）才算就绪。
  READY=0
  CONSECUTIVE=0
  for _ in $(seq 1 120); do
    if docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER" \
         psql -U "$PG_USER" -d postgres -tAc "SELECT 1" >/dev/null 2>&1; then
      CONSECUTIVE=$((CONSECUTIVE + 1))
      if [[ "$CONSECUTIVE" -ge 2 ]]; then READY=1; break; fi
    else
      CONSECUTIVE=0
    fi
    sleep 2
  done
  if [[ "$READY" -ne 1 ]]; then
    echo "  ✗ 隔离库未在 240s 内就绪"; docker logs --tail 20 "$CONTAINER" 2>&1 | tail -10; exit 1
  fi
  printf '  ✓ 隔离库已就绪（连续两次探活成功）\n'
  docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER" \
    psql -U "$PG_USER" -d postgres -c "CREATE DATABASE $PG_DB OWNER $PG_USER;" >/dev/null 2>&1
  for f in 00-prereqs 01-schema 02-seed; do
    if ! sql_file "installer/cmd/llm-gw-installer/embeddata/$f.sql" >"/tmp/f3-leg-$f.log" 2>&1; then
      echo "  ✗ 应用 $f 失败"; grep -E '^ERROR|psql:' "/tmp/f3-leg-$f.log" | head -5; exit 1
    fi
    printf '  ✓ %s 已应用\n' "$f"
  done
else
  echo "  （--keep：复用现有隔离库）"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 1. 夹具：一个 provider / credential / provider_model / 绑定
# ─────────────────────────────────────────────────────────────────────────────
step "布置夹具（credential + provider_model + 绑定）"
q "
INSERT INTO providers (id, tenant_id, code, display_name, protocol, base_url, enabled)
VALUES (9001, 'default', 'f3-leg', 'F3 腿夹具供应商', 'openai-completions', 'https://f3-leg.invalid/v1', TRUE)
ON CONFLICT (id) DO NOTHING;

INSERT INTO credentials (id, provider_id, label, fp_slot_limit, tenant_id, status)
VALUES (9001, 9001, 'f3-leg-cred', 4, 'default', 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO provider_models (id, provider_id, raw_model_name, canonical_raw_name)
VALUES (9001, 9001, 'f3-leg-model', 'f3-leg-model');

INSERT INTO credential_model_bindings (id, credential_id, provider_model_id, available)
VALUES (9001, 9001, 9001, TRUE);
" >/tmp/f3-leg-fixture.log 2>&1
if grep -qiE '^ERROR|^psql:' /tmp/f3-leg-fixture.log; then
  echo "  ✗ 夹具插入失败"; head -5 /tmp/f3-leg-fixture.log; exit 1
fi
FIXTURE_OK="$(q "SELECT COUNT(*) FROM credential_model_bindings WHERE id = 9001 AND credential_id = 9001 AND provider_model_id = 9001;")"
assert_eq "1" "$FIXTURE_OK" "夹具绑定就位"

# 把该绑定置成「判不可用 + 计划 15 分钟后重试」的形态（= 真实故障后的状态）
q "
UPDATE credential_model_bindings
SET available = FALSE,
    unavailable_reason = 'probe_timeout',
    unavailable_at = NOW(),
    unavailable_recover_at = NOW() + INTERVAL '15 minutes'
WHERE id = 9001;
" >/dev/null

# ─────────────────────────────────────────────────────────────────────────────
# 2. 缺陷复现（修复前的触发器 = 基线 schema 的形态）
# ─────────────────────────────────────────────────────────────────────────────
step "缺陷复现：经 model_offers 视图「置可用」后，恢复标记是否被清掉"
BEFORE_CLAUSE="$(q "
SELECT CASE WHEN pg_get_functiondef(p.oid) LIKE '%unavailable_recover_at = CASE%'
            THEN 'has_clause' ELSE 'no_clause' END
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public' AND p.proname = 'model_offers_update_trigger';")"
assert_eq "no_clause" "$BEFORE_CLAUSE" "修复前触发器体里没有 unavailable_recover_at 分支"

# 模拟真实调用方：moExpiredRecoverySQL / restoreAllBindingsOnCredentialSuccess 的镜像
q "UPDATE model_offers SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL WHERE id = 9001;" >/dev/null
AFTER_MIRROR="$(q "
SELECT CASE
         WHEN available = TRUE AND unavailable_at IS NULL AND unavailable_recover_at IS NULL THEN 'cleared'
         WHEN available = TRUE AND unavailable_at IS NULL AND unavailable_recover_at IS NOT NULL THEN 'RESIDUE'
         ELSE 'other' END
FROM credential_model_bindings WHERE id = 9001;")"
assert_eq "RESIDUE" "$AFTER_MIRROR" "★ 缺陷复现：视图写入后恢复标记仍在（矛盾态）"

RESIDUE_COUNT="$(q "
SELECT COUNT(*) FROM credential_model_bindings
WHERE unavailable_recover_at IS NOT NULL AND unavailable_at IS NULL AND available = TRUE;")"
assert_eq "1" "$RESIDUE_COUNT" "★ 缺陷复现：F3 监控口径命中 1 行"

step "缺陷复现：视图里**显式**写 unavailable_recover_at = NULL 也会被丢弃"
q "
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL,
    unavailable_recover_at = NOW() + INTERVAL '15 minutes'
WHERE id = 9001;
" >/dev/null
q "UPDATE model_offers SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL, unavailable_recover_at = NULL WHERE id = 9001;" >/dev/null
EXPLICIT_DROP="$(q "
SELECT CASE WHEN unavailable_recover_at IS NULL THEN 'cleared' ELSE 'RESIDUE' END
FROM credential_model_bindings WHERE id = 9001;")"
assert_eq "RESIDUE" "$EXPLICIT_DROP" "★ 缺陷复现：显式写 NULL 也被触发器吞掉（mnfCoolingRecoveryMirrorSQL 同型）"

# ─────────────────────────────────────────────────────────────────────────────
# 3. 应用迁移 972
# ─────────────────────────────────────────────────────────────────────────────
step "应用迁移 972"
if ! sql_file "sql/migrations/startup/$MIG" >/tmp/f3-leg-mig1.log 2>&1; then
  echo "  ✗ 迁移 972 首次执行失败"; tail -20 /tmp/f3-leg-mig1.log; exit 1
fi
printf '  ✓ 迁移 972 首次执行成功\n'
AFTER_CLAUSE="$(q "
SELECT CASE WHEN pg_get_functiondef(p.oid) LIKE '%unavailable_recover_at = CASE%'
            THEN 'has_clause' ELSE 'no_clause' END
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public' AND p.proname = 'model_offers_update_trigger';")"
assert_eq "has_clause" "$AFTER_CLAUSE" "迁移后触发器体含 unavailable_recover_at 分支"

# ─────────────────────────────────────────────────────────────────────────────
# 4. 修复生效
# ─────────────────────────────────────────────────────────────────────────────
step "修复生效：同一动作（视图置可用）现在会清掉恢复标记"
q "
UPDATE credential_model_bindings
SET available = FALSE,
    unavailable_reason = 'probe_timeout',
    unavailable_at = NOW(),
    unavailable_recover_at = NOW() + INTERVAL '15 minutes'
WHERE id = 9001;
" >/dev/null
q "UPDATE model_offers SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL WHERE id = 9001;" >/dev/null
AFTER_FIX="$(q "
SELECT CASE
         WHEN available = TRUE AND unavailable_at IS NULL AND unavailable_recover_at IS NULL THEN 'cleared'
         WHEN available = TRUE AND unavailable_at IS NULL AND unavailable_recover_at IS NOT NULL THEN 'RESIDUE'
         ELSE 'other' END
FROM credential_model_bindings WHERE id = 9001;")"
assert_eq "cleared" "$AFTER_FIX" "★ 修复生效：视图写入后恢复标记被清空"
assert_eq "0" "$(q "SELECT COUNT(*) FROM credential_model_bindings WHERE unavailable_recover_at IS NOT NULL AND unavailable_at IS NULL AND available = TRUE;")" \
  "★ 修复生效：F3 监控口径归零"

step "修复生效：视图里显式写 unavailable_recover_at = NULL 现在能被送达"
q "
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL,
    unavailable_recover_at = NOW() + INTERVAL '15 minutes'
WHERE id = 9001;
" >/dev/null
q "UPDATE model_offers SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL, unavailable_recover_at = NULL WHERE id = 9001;" >/dev/null
assert_eq "cleared" "$(q "SELECT CASE WHEN unavailable_recover_at IS NULL THEN 'cleared' ELSE 'RESIDUE' END FROM credential_model_bindings WHERE id = 9001;")" \
  "★ 修复生效：显式 NULL 被送达（mnfCoolingRecoveryMirrorSQL 同型）"

# ─────────────────────────────────────────────────────────────────────────────
# 4b. 直写 cmb 的恢复路径（不经视图 ⇒ 触发器管不到 ⇒ 靠各写入方自己写第四列）
# ─────────────────────────────────────────────────────────────────────────────
step "直写 cmb 的恢复路径：四元组齐备 ⇒ 标记被清（对照：旧的三列写法会留残留）"
# 旧写法（只写三列）⇒ 残留
q "
UPDATE credential_model_bindings
SET available = FALSE, unavailable_reason = 'probe_timeout', unavailable_at = NOW(),
    unavailable_recover_at = NOW() + INTERVAL '15 minutes'
WHERE id = 9001;
" >/dev/null
q "
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL
WHERE id = 9001;
" >/dev/null
assert_eq "RESIDUE" "$(q "SELECT CASE WHEN unavailable_recover_at IS NULL THEN 'cleared' ELSE 'RESIDUE' END FROM credential_model_bindings WHERE id = 9001;")" \
  "对照：旧的三列直写写法确实会留下残留（这正是三条直写路径的病根）"

# 新写法（四元组齐备）⇒ 标记被清
q "
UPDATE credential_model_bindings
SET available = FALSE, unavailable_reason = 'probe_timeout', unavailable_at = NOW(),
    unavailable_recover_at = NOW() + INTERVAL '15 minutes'
WHERE id = 9001;
" >/dev/null
q "
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL,
    unavailable_recover_at = NULL
WHERE id = 9001;
" >/dev/null
assert_eq "cleared" "$(q "SELECT CASE WHEN unavailable_recover_at IS NULL THEN 'cleared' ELSE 'RESIDUE' END FROM credential_model_bindings WHERE id = 9001;")" \
  "★ 四元组齐备的直写写法：标记被正确清空"

# ─────────────────────────────────────────────────────────────────────────────
# 5. 「置不可用」方向不得回归
# ─────────────────────────────────────────────────────────────────────────────
step "回归：「置不可用」方向照旧，且显式重试时刻被尊重"
q "
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL, unavailable_recover_at = NULL
WHERE id = 9001;
" >/dev/null
q "
UPDATE model_offers
SET available = FALSE, unavailable_reason = 'probe_timeout', unavailable_at = NOW(),
    unavailable_recover_at = NOW() + INTERVAL '30 minutes'
WHERE id = 9001;
" >/dev/null
FALSE_DIR="$(q "
SELECT CASE
         WHEN available = FALSE AND unavailable_reason = 'probe_timeout'
              AND unavailable_at IS NOT NULL
              AND unavailable_recover_at IS NOT NULL
              AND unavailable_recover_at > NOW() THEN 'ok'
         ELSE 'BROKEN' END
FROM credential_model_bindings WHERE id = 9001;")"
assert_eq "ok" "$FALSE_DIR" "★ 回归：置不可用方向四元组齐备（含未来重试时刻）"

step "回归：「置不可用但不给重试时刻」保持原值（对现有调用方零行为变化）"
q "UPDATE credential_model_bindings SET unavailable_recover_at = NOW() + INTERVAL '7 days' WHERE id = 9001;" >/dev/null
q "UPDATE model_offers SET available = FALSE, unavailable_reason = 'probe_timeout', unavailable_at = NOW() WHERE id = 9001;" >/dev/null
KEEP_DIR="$(q "
SELECT CASE WHEN unavailable_recover_at > NOW() + INTERVAL '6 days' THEN 'kept' ELSE 'CHANGED' END
FROM credential_model_bindings WHERE id = 9001;")"
assert_eq "kept" "$KEEP_DIR" "★ 回归：分支③ 保持原值"

# ─────────────────────────────────────────────────────────────────────────────
# 6. 存量清理 + 幂等
# ─────────────────────────────────────────────────────────────────────────────
step "存量清理：残留行被一次性清空"
q "
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL,
    unavailable_recover_at = NOW() + INTERVAL '15 minutes'
WHERE id = 9001;
" >/dev/null
assert_eq "1" "$(q "SELECT COUNT(*) FROM credential_model_bindings WHERE unavailable_recover_at IS NOT NULL AND unavailable_at IS NULL AND available = TRUE;")" \
  "清理前：残留 1 行"

if ! sql_file "sql/migrations/startup/$MIG" >/tmp/f3-leg-mig2.log 2>&1; then
  echo "  ✗ 迁移 972 二次执行失败"; tail -20 /tmp/f3-leg-mig2.log; exit 1
fi
printf '  ✓ 迁移 972 二次执行成功（幂等）\n'
assert_eq "0" "$(q "SELECT COUNT(*) FROM credential_model_bindings WHERE unavailable_recover_at IS NOT NULL AND unavailable_at IS NULL AND available = TRUE;")" \
  "清理后：残留归零"
assert_eq "true" "$(q "SELECT available::text FROM credential_model_bindings WHERE id = 9001;")" \
  "★ 清理不改变绑定的可路由性（仍 available）"

step "幂等复核：第三次执行仍是 0 行改动"
if ! sql_file "sql/migrations/startup/$MIG" >/tmp/f3-leg-mig3.log 2>&1; then
  echo "  ✗ 迁移 972 三次执行失败"; tail -20 /tmp/f3-leg-mig3.log; exit 1
fi
printf '  ✓ 迁移 972 三次执行成功\n'
assert_eq "0" "$(q "SELECT COUNT(*) FROM credential_model_bindings WHERE unavailable_recover_at IS NOT NULL AND unavailable_at IS NULL AND available = TRUE;")" \
  "三次执行后残留仍为 0"

step "台账自登记"
assert_eq "1" "$(q "SELECT COUNT(*) FROM schema_migrations WHERE version = '972';")" \
  "schema_migrations 有 972 的行"

# ─────────────────────────────────────────────────────────────────────────────
printf '\n==========================================\n'
if [[ "$FAILURES" -eq 0 ]]; then
  printf '隔离 PG 真跑腿：全部断言通过 ✅\n'
  printf '==========================================\n'
  exit 0
fi
printf '隔离 PG 真跑腿：%d 条断言失败 ❌\n' "$FAILURES"
printf '==========================================\n'
exit 1

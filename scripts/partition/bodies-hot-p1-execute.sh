#!/bin/bash
# ========================================
# bodies-hot-p1-execute.sh — bodies-hot P1 存储优化「破坏性」步骤执行器
#
# 由 CronCreate 在 02:00–04:59 维护窗口内触发（脚本自身也内置窗口守卫，
# 窗口外一律中止，绝不执行破坏性 DDL），或由运维人工在窗口内手动运行：
#   bash scripts/partition/bodies-hot-p1-execute.sh
#
# 执行顺序（全部幂等/可回滚）：
#   1) 自愈 SSH 隧道（localhost:15432 → 252）
#   2) swap 重写 request_logs_bodies_hot（旧表保留 *_bloat_backup_<ts>，行数校验）
#   3) 新活表 SET autovacuum reloptions（防复发；LIKE 不继承 reloptions，必须 swap 后做）
#   4) to-252 索引补齐（handoff_logs_hot 2 个 btree，幂等）
#   5) 后置诊断留档
#
# 安全：任一步失败立即中止并写日志，旧/备份表均保留，绝不自动 DROP。
# 48h 后的备份表 DROP 释放 ~35GB 由人工在确认稳定后执行（见 runbook §2）。
# ========================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
LOG="/tmp/bodies-hot/p1-exec-$(date +%F-%H%M%S).log"
mkdir -p /tmp/bodies-hot
exec > >(tee -a "$LOG") 2>&1

echo "===== bodies-hot P1 执行开始 $(date -Iseconds) ====="
echo "REPO_ROOT=$REPO_ROOT  LOG=$LOG"

# ── 窗口守卫（破坏性步骤总闸） ───────────────────────────────────────────────
HOUR=$(date +%H)
if (( 10#$HOUR < 2 || 10#$HOUR >= 5 )); then
  echo "✗ $(date '+%F %T') 不在 02:00–04:59 维护窗口，中止（不执行任何破坏性操作）。" >&2
  exit 1
fi
echo "✓ 窗口内（$(date '+%H:%M')），继续。"

# ── 加载 252 凭据 ───────────────────────────────────────────────────────────
# shellcheck disable=SC1091
source "$HOME/workspace/ai-native-tools/envs/loader.sh" --all --project llm-gateway-go --server 115.29.212.252 --mode plain >/dev/null 2>&1 \
  || { echo "✗ envs loader 失败" >&2; exit 1; }

# ── 1) 自愈 SSH 隧道 ────────────────────────────────────────────────────────
ensure_tunnel() {
  if lsof -tiTCP:15432 -sTCP:LISTEN >/dev/null 2>&1; then
    echo "✓ 隧道 15432 已存在"
    return 0
  fi
  echo "→ 重建隧道 15432 → 252"
  ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 \
      -o ExitOnForwardFailure=yes -i "$SSH_KEY_252" -f -N -p 25022 \
      -L 15432:172.16.2.210:5432 root@115.29.212.252
  sleep 2
  lsof -tiTCP:15432 -sTCP:LISTEN >/dev/null 2>&1 \
    && echo "✓ 隧道已建立" || { echo "✗ 隧道建立失败" >&2; exit 1; }
}
ensure_tunnel

# ── 2) swap 重写（脚本内部窗口守卫再确认一次） ──────────────────────────────
echo; echo "──── [2] swap 重写 ────"
bash scripts/partition/bodies-hot-repack.sh --env=252 --mode=swap \
  || { echo "✗ swap 失败，中止（旧表未动，无数据丢失）" >&2; exit 1; }

# ── 3) 新活表防复发 reloptions（swap 后新表不继承 reloptions） ───────────────
echo; echo "──── [3] 新活表 reloptions ────"
PGOPTIONS='-c statement_timeout=0 -c idle_in_transaction_session_timeout=0' PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -X -h 127.0.0.1 -p 15432 -U "$COMMON_PG_SUPERUSER" -d llm_gateway -v ON_ERROR_STOP=1 -c "
ALTER TABLE public.request_logs_bodies_hot SET (
  fillfactor = 90,
  autovacuum_enabled = true,
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_vacuum_threshold    = 1000,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_analyze_threshold    = 50
);" \
  || { echo "✗ reloptions 设置失败，中止（swap 已成功，备份表保留，可单独补 reloptions）" >&2; exit 1; }

# ── 4) to-252 索引补齐（幂等） ──────────────────────────────────────────────
echo; echo "──── [4] to-252 索引补齐 ────"
bash scripts/partition/index-drift-align.sh --direction=to-252 \
  || { echo "✗ 索引补齐失败，中止（swap+reloptions 已成功，可单独补索引）" >&2; exit 1; }

# ── 5) 后置诊断留档 ─────────────────────────────────────────────────────────
echo; echo "──── [5] 后置诊断 ────"
bash scripts/partition/bodies-hot-diagnose.sh --env=252 \
  || echo "⚠ 后置诊断读取失败（不影响已完成操作）"

echo; echo "===== bodies-hot P1 执行完成 $(date -Iseconds) ====="
echo "下一步（人工，稳定 ≥48h 后）：DROP TABLE public.request_logs_bodies_hot_bloat_backup_<ts>; 释放 ~35GB"
echo "日志：$LOG"

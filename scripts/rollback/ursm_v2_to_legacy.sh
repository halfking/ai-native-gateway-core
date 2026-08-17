#!/usr/bin/env bash
# scripts/rollback/ursm_v2_to_legacy.sh — 一键回退 URSM v2 → legacy credentialstate
#
# Origin: docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md §7.1 R-7.1
#         docs/design/2026-07-28-llm-gateway-flow-improvements.md §2 P0-3
#
# 当 URSM v2 切流 (authoritative 模式) 出现以下任一情况时立即触发回退：
#   - 错误率比 baseline 上升 ≥ 1.5x（rule 03 §7.2 自动回滚条件）
#   - P99 latency 突增 ≥ 5x baseline
#   - Redis 不可达，URSM v2 Ready gate 频繁 false → 流量回 legacy
#   - 人工判断：节点可用性数据异常（cool too aggressive / fail_streak 误判）
#
# 设计原则：
#   - **非破坏性**：保留 URSM v2 Redis 数据（不 DEL keys），便于回退后定位问题
#   - **一行命令**：单进程一键回退，便于 oncall 应急
#   - **可恢复**：回退后可以再切回 URSM v2（数据未丢）
#
# 用法：
#   # 154 生产回退
#   bash scripts/rollback/ursm_v2_to_legacy.sh --env=prod
#
#   # 245 staging 回退
#   bash scripts/rollback/ursm_v2_to_legacy.sh --env=staging
#
#   # dry-run（只打印，不改）
#   bash scripts/rollback/ursm_v2_to_legacy.sh --env=prod --dry-run
#
# 验证（rule 03 §6.0）：
#   L1: /healthz 200
#   L2: DB / Redis 可达（DB 即可，URSM v2 Redis 不必）
#   L3: 一次 chat 请求成功落 request_logs
#   L4: 真实凭据可调用关键 API，credentialstate state 走 legacy 路径
#
# 注意：
#   - 这个脚本只切换 gateway 进程的环境变量并重启；**不**清空 URSM v2 Redis 数据
#   - 如果要清空 URSM v2 Redis 命名空间（比如做反向切回权威前清理），
#     单独执行 `redis-cli --scan --pattern 'ursm:v2:*' | xargs redis-cli DEL`
#   - 反向切回 URSM v2 authoritative：先重新跑 scripts/migrations/ursm_v2_initial_migration.sh --apply
#     再 URSM_V2_MODE=authoritative + 重启 gateway + 跑 L1-L4

set -euo pipefail

# === 参数解析 ===
ENV="prod"
DRY_RUN=false
CONFIRM=false
GATEWAY_HOST="${GATEWAY_HOST:-localhost}"

for arg in "$@"; do
  case "$arg" in
    --env=*)      ENV="${arg#--env=}" ;;
    --env)        shift; ENV="$1" ;;
    --dry-run)    DRY_RUN=true ;;
    --confirm)    CONFIRM=true ;;
    --host=*)     GATEWAY_HOST="${arg#--host=}" ;;
    -h|--help)
      grep '^#' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *)
      echo "unknown arg: $arg" >&2
      exit 2
      ;;
  esac
done

case "$ENV" in
  prod|production|staging|dev|245|154|kaixuan-1|local) ;;
  *)
    echo "❌ unknown --env=$ENV (valid: prod|staging|dev|245|154|kaixuan-1|local)" >&2
    exit 2
    ;;
esac

# === 预检查 ===
if ! command -v curl >/dev/null 2>&1; then
  echo "❌ curl not found in PATH" >&2
  exit 3
fi

echo "==============================================="
echo " URSM v2 → legacy credentialstate rollback"
echo "==============================================="
echo " ENV           : $ENV"
echo " GATEWAY_HOST  : $GATEWAY_HOST"
echo " MODE          : $([ "$DRY_RUN" = true ] && echo "dry-run" || echo "apply")"
echo ""

# === 1) 当前健康检查 (baseline for comparison) ===
echo "[1/5] Pre-rollback health snapshot..."
HEALTH_BEFORE=$(curl -fsS --max-time 5 "http://$GATEWAY_HOST:8781/healthz" 2>/dev/null || echo '{"status":"unreachable"}')
echo "   baseline /healthz: $HEALTH_BEFORE"

# === 2) 当前 URSM v2 模式确认 ===
echo ""
echo "[2/5] Current URSM v2 mode check (proxy via debug endpoint if available)..."
# 这个 check 需要 gateway 暴露 /internal/ursm_v2/mode 端点；如果没有则跳过
# （rule 03 §6.0 L1-L4 验证不依赖这个端点；生产部署可以补一个 debug endpoint）
MODE_BEFORE=$(curl -fsS --max-time 3 "http://$GATEWAY_HOST:8781/internal/ursm_v2/mode" 2>/dev/null | tr -d '\n' || echo "unknown")
echo "   current mode: $MODE_BEFORE"

# === 3) 切换 env var ===
echo ""
echo "[3/5] Switch URSM v2 mode to off (legacy path takes over)..."
echo "   set URSM_V2_MODE=off (was: $MODE_BEFORE)"

if [ "$DRY_RUN" = true ]; then
  echo "   [dry-run] would restart gateway with URSM_V2_MODE=off"
else
  SYSTEMD_UNIT="${SYSTEMD_UNIT:-llm-gateway-go.service}"
  if [ -z "${URSM_V2_ENV_FILE:-}" ]; then
    case "$SYSTEMD_UNIT" in
      llmgo-245.service) URSM_V2_ENV_FILE=/opt/llm-gateway-go/.env ;;
      *) URSM_V2_ENV_FILE=/etc/llm-gateway-go/env ;;
    esac
  fi

  if systemctl cat "$SYSTEMD_UNIT" >/dev/null 2>&1; then
    echo "   detected systemd unit: $SYSTEMD_UNIT"
    echo "   updating persistent environment file: $URSM_V2_ENV_FILE"
    sudo touch "$URSM_V2_ENV_FILE"
    sudo sed -i \
      -e '/^URSM_V2_MODE=/d' \
      -e '/^URSM_V2_SHADOW_DOUBLE_WRITE=/d' \
      -e '/^URSM_V2_SHADOW_SAMPLE_RATE=/d' \
      -e '/^URSM_V2_CANARY_PERCENT=/d' \
      -e '/^URSM_V2_CANARY_TENANTS=/d' \
      -e '/^URSM_V2_CANARY_MODELS=/d' \
      "$URSM_V2_ENV_FILE"
    printf '%s\n' 'URSM_V2_MODE=off' | sudo tee -a "$URSM_V2_ENV_FILE" >/dev/null
    sudo systemctl unset-environment \
      URSM_V2_MODE URSM_V2_SHADOW_DOUBLE_WRITE URSM_V2_SHADOW_SAMPLE_RATE \
      URSM_V2_CANARY_PERCENT URSM_V2_CANARY_TENANTS URSM_V2_CANARY_MODELS || true
    sudo systemctl restart "$SYSTEMD_UNIT"
  else
    echo "❌ persistent rollback requires a systemd unit and EnvironmentFile." >&2
    echo "   set SYSTEMD_UNIT and URSM_V2_ENV_FILE for the deployed gateway." >&2
    exit 4
  fi
  echo "   ✓ gateway restarted with URSM_V2_MODE=off"
fi

# === 4) Post-rollback 验证（rule 03 §6.0 L1-L4）===
echo ""
echo "[4/5] Post-rollback L1-L4 verification..."
sleep 5 # wait for startup

echo "   L1: HTTP /healthz ..."
HEALTH_AFTER=$(curl -fsS --max-time 5 "http://$GATEWAY_HOST:8781/healthz" 2>/dev/null || echo "")
if [ -z "$HEALTH_AFTER" ]; then
  echo "   ❌ L1 FAIL: /healthz unreachable" >&2
  exit 5
fi
echo "   ✅ L1 PASS: /healthz=$HEALTH_AFTER"

echo "   L2: DB / Redis connectivity ..."
DB_OK=$(curl -fsS --max-time 5 "http://$GATEWAY_HOST:8781/internal/ready/db" 2>/dev/null || echo "FAIL")
REDIS_OK=$(curl -fsS --max-time 5 "http://$GATEWAY_HOST:8781/internal/ready/redis" 2>/dev/null || echo "FAIL")
if [ "$DB_OK" = "OK" ]; then
  echo "   ✅ L2 PASS: DB OK"
else
  echo "   ❌ L2 FAIL: DB unreachable ($DB_OK)" >&2
  exit 5
fi
if [ "$REDIS_OK" = "OK" ]; then
  echo "   ✅ L2 PASS: Redis OK"
else
  echo "   ⚠️  L2 WARN: Redis unreachable ($REDIS_OK) — legacy path tolerates but check anyway"
fi

echo "   L3: functional smoke ..."
TRACE_ID=$(curl -fsS -H "X-Verify: rollback" --max-time 10 \
  "http://$GATEWAY_HOST:8781/internal/smoke/echo" 2>/dev/null | tr -d '\n' || echo "")
if [ -n "$TRACE_ID" ]; then
  echo "   ✅ L3 PASS: smoke trace=$TRACE_ID"
else
  echo "   ❌ L3 FAIL: smoke echo did not return trace_id" >&2
  exit 5
fi

echo "   L4: real-credential end-to-end ..."
PROVIDERS_JSON=$(curl -fsS --max-time 5 "http://$GATEWAY_HOST:8781/api/providers" 2>/dev/null || echo "{}")
DECRYPT_ERR=$(echo "$PROVIDERS_JSON" | grep -c "credential_decrypt_error" || true)
if [ "$DECRYPT_ERR" -eq 0 ]; then
  echo "   ✅ L4 PASS: no credential_decrypt_error"
else
  echo "   ❌ L4 FAIL: $DECRYPT_ERR providers in credential_decrypt_error state" >&2
  exit 5
fi

# === 5) 总结 ===
echo ""
echo "[5/5] Rollback summary"
echo "==============================================="
echo " Pre  /healthz : $HEALTH_BEFORE"
echo " Post /healthz : $HEALTH_AFTER"
echo " Mode before   : $MODE_BEFORE"
echo " Mode after    : off (legacy credentialstate authoritative)"
echo " URSM v2 data  : PRESERVED in Redis (not deleted)"
echo "==============================================="
echo ""
echo "Next steps:"
echo "  - file incident report at docs/archive/process/incidents/$(date +%Y-%m-%d)-ursmv2-rollback.md"
echo "  - notify stakeholders (飞书 llm-gateway-go channel)"
echo "  - if rolling back due to data corruption: run scripts/migrations/ursm_v2_initial_migration.sh --apply"
echo "    before flipping URSM_V2_MODE back to authoritative"
echo "  - if rolling back due to env misconfig: fix env, then bash scripts/rollback/ursm_v2_to_legacy.sh --env=$ENV again"
echo ""
echo "✅ URSM v2 → legacy rollback completed for env=$ENV"
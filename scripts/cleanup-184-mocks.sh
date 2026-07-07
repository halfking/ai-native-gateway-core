#!/usr/bin/env bash
# ====================================================================
# 清理 184 mock 测试环境
# ====================================================================
# 停止 12 个 mock 进程 + 删除 loadtest credentials（按 tenant_id='loadtest'）
# 不影响任何生产数据
# ====================================================================
set -euo pipefail

SERVER="${LLM_GATEWAY_184_SERVER:-root@14.103.112.184}"
SSH_PORT="${LLM_GATEWAY_184_SSH_PORT:-25022}"

echo "=== 清理 184 mock 测试环境 ==="

# 1. 停止 mock 进程
echo "[1/3] 停止 mock 进程..."
ssh -p "$SSH_PORT" "$SERVER" "pkill -f 'server-v2.py' 2>/dev/null && echo '  已停止' || echo '  无运行中的 mock'"

# 2. 删除 mock credentials（按 provider_id 段精确删除，不影响生产）
echo "[2/3] 删除 mock credentials (provider_id 9010-9099)..."
ssh -p "$SSH_PORT" "$SERVER" '
  PG_POD=$(kubectl get pods -n pms-test -o name | grep pg | head -1)
  kubectl exec -i -n pms-test $PG_POD -- psql -U llm_gateway -d llm_gateway <<SQL
BEGIN;
DELETE FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9099;
DELETE FROM provider_models        WHERE provider_id BETWEEN 9010 AND 9099;
DELETE FROM credentials            WHERE id BETWEEN 9010 AND 9099;
DELETE FROM providers              WHERE id BETWEEN 9010 AND 9099;
COMMIT;
SQL
' 2>&1 | grep -E "DELETE|BEGIN|COMMIT" | tail -5

# 3. 可选：删除 mock 文件（默认保留以便快速重启）
echo "[3/3] 清理完成"
echo ""
echo "如需彻底删除 mock 文件: ssh -p $SSH_PORT $SERVER 'rm -rf /opt/llm-gateway-mocks'"
echo "如需删除 cron:          ssh -p $SSH_PORT $SERVER 'crontab -l | grep -v health-probe | crontab -'"

#!/usr/bin/env bash
# ====================================================================
# 部署 12 个 mock 供应商到 184 (宿主机进程，不进 K8s)
# ====================================================================
# 用法：本地执行 bash scripts/deploy-mocks-to-184.sh
# 前置：SSH key 可免密登录 root@14.103.112.184:25022
# ====================================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER="${LLM_GATEWAY_184_SERVER:-root@14.103.112.184}"
SSH_PORT="${LLM_GATEWAY_184_SSH_PORT:-25022}"
REMOTE_DIR="/opt/llm-gateway-mocks"
NUM_MOCKS="${NUM_MOCKS:-12}"
MOCK_START_PORT="${MOCK_START_PORT:-19080}"
VENV_PYTHON="$REMOTE_DIR/venv/bin/python"

echo "=== 部署 $NUM_MOCKS 个 mock 到 $SERVER ==="

# 1. 确保 venv 和 aiohttp（幂等）
echo "[1/4] 确保 Python venv + aiohttp..."
ssh -p "$SSH_PORT" "$SERVER" "
  mkdir -p $REMOTE_DIR
  if [ ! -x '$VENV_PYTHON' ]; then
    python3 -m venv $REMOTE_DIR/venv
  fi
  '$VENV_PYTHON' -c 'import aiohttp' 2>/dev/null || $REMOTE_DIR/venv/bin/pip install -q aiohttp
  echo '  venv ready'
"

# 2. 上传 mock 代码（含最新 server-v2.py，带 _mock_identity）
echo "[2/4] 上传 mock 代码..."
scp -P "$SSH_PORT" -q \
  "$ROOT_DIR/scripts/mocks/llm-mock-upstream/server-v2.py" \
  "$SERVER:$REMOTE_DIR/server-v2.py"
echo "  server-v2.py 已上传"

# 3. 停止旧 mock 进程（幂等）
echo "[3/4] 停止旧 mock 进程..."
ssh -p "$SSH_PORT" "$SERVER" "pkill -f 'server-v2.py' 2>/dev/null || true; sleep 1; echo '  旧进程已停止'"

# 4. 启动 12 个 mock
# 注意：必须监听 0.0.0.0（非 127.0.0.1），否则 K8s Pod 内的 gateway 访问不到宿主机 mock。
# 安全性由 184 防火墙保障（mock 端口不对外暴露，仅 Pod 网段 172.31.0.0/16 可达）。
echo "[4/4] 启动 $NUM_MOCKS 个 mock（监听 0.0.0.0，Pod 可达）..."
ssh -p "$SSH_PORT" "$SERVER" "
  cd $REMOTE_DIR
  for i in \$(seq 0 $((NUM_MOCKS - 1))); do
    port=\$((MOCK_START_PORT + i))
    token=\$(printf 'mock-%02d' \$i)
    MOCK_PORT=\$port MOCK_TOKEN=\$token MOCK_HOST=0.0.0.0 MOCK_STATE_FILE=/tmp/mock-state-\$port.json \
      nohup '$VENV_PYTHON' server-v2.py > /tmp/mock-\$port.log 2>&1 &
    echo \"  启动 \$token (127.0.0.1:\$port, PID \$!)\"
  done
  sleep 3
  echo '=== 验证 ==='
  ok=0
  for i in \$(seq 0 $((NUM_MOCKS - 1))); do
    port=\$((MOCK_START_PORT + i))
    if curl -sS --max-time 2 http://127.0.0.1:\$port/healthz >/dev/null 2>&1; then
      ok=\$((ok + 1))
    else
      echo \"  ⚠ mock-\$(printf '%02d' \$i) (port \$port) 未响应\"
    fi
  done
  echo \"可用: \$ok/$NUM_MOCKS\"
  exit \$((NUM_MOCKS - ok))
"

echo ""
echo "=== 部署完成 ==="
echo "mock 监听 127.0.0.1:19080-19091（仅本机访问）"
echo "查看状态: ssh -p $SSH_PORT $SERVER 'for p in 19080 19091; do curl -s http://127.0.0.1:\$p/admin/metrics; done'"

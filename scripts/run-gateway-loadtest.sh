#!/usr/bin/env bash
# Gateway Loadtest Runner - 启动 gateway + 4 mock, 注入 credential, 跑场景
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

usage() {
  cat <<EOF
用法: $0 <command>

命令:
  setup      - 启动 4 mock + 注入 credential 到 DB
  teardown   - 停止 mock + 清理 credential
  verify     - 验证 mock + credential 可用
  run <scn>  - 通过 gateway 跑指定场景 (如: run S4)
  run-all    - 通过 gateway 跑全量场景 S0-S8

前置条件:
  - PostgreSQL 运行中 (本地或 Docker)
  - 01-schema.sql + 02-seed.sql 已应用
  - gateway 可编译 (go build)
EOF
  exit 1
}

[[ $# -lt 1 ]] && usage

CMD=$1
shift

setup_mocks() {
  echo "=== 启动 4 个 mock 实例 ==="
  cd "$SCRIPT_DIR/mocks/llm-mock-upstream"
  
  for port in 19080 19081 19082 19083; do
    # 检查是否已在运行
    if curl -sS --max-time 1 "http://localhost:$port/healthz" > /dev/null 2>&1; then
      echo "  ✓ mock-$port 已在运行"
      continue
    fi
    
    MOCK_PORT=$port MOCK_TOKEN=mock-$(printf "\\x$(printf '%x' $((port - 19080 + 65)))") \
      MOCK_STATE_FILE=/tmp/mock-state-$port.json \
      python3 server-v2.py > /tmp/mock-$port.log 2>&1 &
    echo "  ✓ mock-$port PID=$!"
  done
  
  sleep 2
  echo ""
  echo "=== 注入 loadtest credential ==="
  echo "  SQL: $ROOT_DIR/sql/scripts/04-loadtest-mock-credentials.sql"
  echo "  执行: psql -f sql/scripts/04-loadtest-mock-credentials.sql"
  echo ""
  echo "  (请手动执行 SQL 或配置 DB 连接后运行)"
}

teardown() {
  echo "=== 停止 mock ==="
  pkill -f "server-v2.py" 2>/dev/null || true
  echo "  ✓ 已停止"
  echo ""
  echo "=== 清理 credential (SQL) ==="
  echo "  执行:"
  echo "  DELETE FROM credential_model_bindings WHERE credential_id IN (9010,9011,9012,9013);"
  echo "  DELETE FROM provider_models WHERE provider_id IN (9010,9011,9012,9013);"
  echo "  DELETE FROM credentials WHERE id IN (9010,9011,9012,9013);"
  echo "  DELETE FROM providers WHERE id IN (9010,9011,9012,9013);"
}

verify() {
  echo "=== 验证 4 个 mock ==="
  OK=0
  for port in 19080 19081 19082 19083; do
    echo -n "  localhost:$port → "
    if curl -sS --max-time 2 "http://localhost:$port/healthz" | jq -c '{token, mode}'; then
      OK=$((OK+1))
    else
      echo "FAIL"
    fi
  done
  echo ""
  if [[ $OK -eq 4 ]]; then
    echo "  ✓ 全部可用"
  else
    echo "  ⚠ $OK/4 可用"
  fi
}

case "$CMD" in
  setup)    setup_mocks ;;
  teardown) teardown ;;
  verify)   verify ;;
  run)      echo "TODO: 通过 gateway 跑场景 $1" ;;
  run-all)  echo "TODO: 通过 gateway 跑全量场景" ;;
  *)        usage ;;
esac

#!/usr/bin/env bash
# 测试部署脚本（dry-run）

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "========================================="
echo "部署脚本测试"
echo "========================================="

# ============================================================================
# 测试 1: 检查脚本存在
# ============================================================================

echo ""
echo "✅ 测试 1: 检查脚本文件"

SCRIPTS=(
    "deploy-to-245.sh"
    "deploy-to-docker.sh"
    "health-check.sh"
    "rollback.sh"
    "test-deploy-local-env.sh"
)

for script in "${SCRIPTS[@]}"; do
    if [[ -x "$SCRIPT_DIR/$script" ]]; then
        echo "   ✅ $script"
    else
        echo "   ❌ $script (不存在或不可执行)"
        exit 1
    fi
done

# ============================================================================
# 测试 2: 语法检查
# ============================================================================

echo ""
echo "✅ 测试 2: Shell 语法检查"

for script in "${SCRIPTS[@]}"; do
    if bash -n "$SCRIPT_DIR/$script" 2>/dev/null; then
        echo "   ✅ $script 语法正确"
    else
        echo "   ❌ $script 语法错误"
        exit 1
    fi
done

# ============================================================================
# 测试 3: 测试健康检查脚本
# ============================================================================

echo ""
echo "✅ 测试 3: 测试健康检查（模拟）"

# 启动一个简单的测试服务器
cat > /tmp/test_server.py << 'PYTHON'
from http.server import HTTPServer, BaseHTTPRequestHandler
import json

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/healthz':
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"status":"ok"}')
        elif self.path == '/api/internal/ready/db':
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b'OK')
        elif self.path == '/api/internal/ready/redis':
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b'OK')
        elif self.path == '/api/system/version':
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"version":"2.4.7-test"}')
        elif self.path == '/api/system/config':
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"server":{"port":8999}}')
        else:
            self.send_response(404)
            self.end_headers()
    
    def log_message(self, format, *args):
        pass

if __name__ == '__main__':
    server = HTTPServer(('localhost', 8999), Handler)
    print('Test server running on port 8999')
    server.serve_forever()
PYTHON

# 启动测试服务器
python3 /tmp/test_server.py &
TEST_SERVER_PID=$!
trap "kill $TEST_SERVER_PID 2>/dev/null || true; rm /tmp/test_server.py" EXIT

sleep 2

# 运行健康检查
if bash "$SCRIPT_DIR/health-check.sh" "http://localhost:8999"; then
    echo "   ✅ 健康检查脚本工作正常"
else
    echo "   ❌ 健康检查脚本失败"
    exit 1
fi

# 停止测试服务器
kill $TEST_SERVER_PID 2>/dev/null || true

# ============================================================================
# 测试 4: 检查依赖工具
# ============================================================================

echo ""
echo "✅ 测试 4: 检查依赖工具"

TOOLS=(
    "curl:必需"
    "jq:必需"
    "ssh:245部署需要"
    "scp:245部署需要"
    "docker:Docker部署需要"
)

for tool_info in "${TOOLS[@]}"; do
    IFS=':' read -r tool desc <<< "$tool_info"
    if command -v "$tool" &>/dev/null; then
        echo "   ✅ $tool ($desc)"
    else
        echo "   ⚠️  $tool 未安装 ($desc)"
    fi
done

# ============================================================================
# 测试 5: 检查 245 连接（可选）
# ============================================================================

echo ""
echo "✅ 测试 5: 检查 245 SSH 连接（可选）"

if ssh -p 25022 -o ConnectTimeout=5 root@8.136.114.245 "echo 'SSH OK'" 2>/dev/null; then
    echo "   ✅ 245 SSH 连接正常"
else
    echo "   ⚠️  245 SSH 连接失败（可能需要配置 SSH 密钥）"
fi

# ============================================================================
# 汇总
# ============================================================================

echo ""
echo "========================================="
echo "测试完成"
echo "========================================="
echo ""
echo "✅ 所有部署脚本就绪"
echo ""
echo "下一步："
echo "  1. 构建一个测试包: bash scripts/build/build-backend.sh \$(pwd) test-version"
echo "  2. 测试 Docker 部署: bash scripts/deploy/deploy-to-docker.sh <docker_archive>"
echo "  3. 测试 245 部署: bash scripts/deploy/deploy-to-245.sh <linux_archive>"
echo ""


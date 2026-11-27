#!/bin/sh
# canary-run.sh —— resource-limiter 灰度验证（§5.1 Phase 1 可执行形态）
# 在 Linux 容器（cgroup v2 可写）内运行：真实 gateway 二进制 + lite 存储 +
# canary 插件 + default_resource_limits 启用 → 采集 /metrics 中 llmgw_plugin_*
# 序列 → 验证 §5.1 灰度清单第 1-3 步（第 4 步 24h 观察由长驻环境完成）。
set -u
GW_BIN=${GATEWAY_BIN:-/gateway}
export CANARY_DIR=/tmp/canary
rm -rf "$CANARY_DIR"
mkdir -p "$CANARY_DIR/plugins/canary" "$CANARY_DIR/data" "$CANARY_DIR/sockets"

# 1) 插件 bundle：manifest + 可执行 entrypoint
cp /canary-plugin "$CANARY_DIR/plugins/canary/run"
cat > "$CANARY_DIR/plugins/canary/plugin-manifest.json" <<'EOF'
{
  "plugin_id": "canary",
  "plugin_version": "0.1.0",
  "gateway_compatibility": {"api_contract": "gateway-plugin-v1"},
  "runtime": {
    "entrypoint": "run",
    "protocol": "http-unix-socket",
    "handshake_path": "/plugin/handshake",
    "health_path": "/plugin/healthz"
  }
}
EOF
chmod +x "$CANARY_DIR/plugins/canary/run"

# 2) gateway.yaml：lite 存储（无 PG/Redis）+ canary 灰度块（§5.1 模板同款）
cat > "$CANARY_DIR/gateway.yaml" <<'EOF'
# 主网关字段（config.Config 读取）
listen: "127.0.0.1:18080"
log_level: info
admin_api_key: canary-admin-key
storage_mode: lite
lite_storage:
  data_dir: /tmp/canary/data
# ── 插件运行时字段（plugin-runtime/config.Config 读取；LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG 指向本文件）──
server:
  host: 127.0.0.1
  port: 18080
plugins:
  directory: /tmp/canary/plugins
  enabled: [canary]
  # ── 灰度块：gateway-canary-resource-limits.yaml 同款 ──
  resource_limits_enabled: true
  default_resource_limits:
    memory:
      limit: "512M"
      swap: false
    cpu:
      limit: "50%"
    max_processes: 10
EOF

# 3) 启动网关（非生产 env：fail-open 密钥告警可接受）
export LLM_GATEWAY_CONFIG_FILE="$CANARY_DIR/gateway.yaml"
export LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG="$CANARY_DIR/gateway.yaml"
export LLM_GATEWAY_ENV=dev
export LLM_GATEWAY_STORAGE_MODE=lite
# lite 模式存储收口（启动校验要求；灰度只验证插件子系统，不写请求日志也能过）
export LLM_GATEWAY_STORAGE_REQUEST_LOGS_WRITE_ENABLED=true
# lite 模式数据面认证（canary 一次性密钥，非生产凭据）
export LLM_GATEWAY_SECRET_KEY=canary-only-secret-key-not-for-production-0001
# CORS fail-closed 门（canary 显式 opt-in）
export LLM_GATEWAY_CORS_ORIGINS=*
unset DATABASE_URL LLM_GATEWAY_DATABASE_URL
"$GW_BIN" > "$CANARY_DIR/gateway.log" 2>&1 &
GW_PID=$!
echo "gateway pid=$GW_PID"

# 4) 等插件 ready + limiter 首轮采样（5s）+ collector 首刷（15s）
sleep 35

# 5) 验收点 1：日志确认 cgroup 模式启用（§5.1 清单步 1）
echo "=== step1: cgroup mode log ==="
grep -E "cgroup resource limits enabled|resource monitoring enabled|subtree controllers" "$CANARY_DIR/gateway.log" | head -3
CGROUP_OK=$(grep -c "cgroup resource limits enabled for plugin canary" "$CANARY_DIR/gateway.log")

# 验收点 2：插件 ready（清单步 2 —— 无 DB 时 plugin-nav API 不可用，用 socket 存在性 + 日志代替）
echo "=== step2: plugin ready ==="
ls -la "$CANARY_DIR/sockets" 2>/dev/null | grep -E "canary|\.sock" | head -3
grep -E "plugin ready|plugin.started" "$CANARY_DIR/gateway.log" | head -2

# 验收点 3：/metrics 出现 llmgw_plugin_* 序列（清单步 3）
echo "=== step3: metrics ==="
curl -s -H "Authorization: Bearer canary-admin-key" "http://127.0.0.1:18080/metrics" > "$CANARY_DIR/metrics.txt" 2>/dev/null || \
  curl -s "http://127.0.0.1:18080/metrics" > "$CANARY_DIR/metrics.txt"
grep "llmgw_plugin_" "$CANARY_DIR/metrics.txt" | head -15
echo "=== cgroup files on disk ==="
for f in memory.max memory.high memory.swap.max cpu.max pids.max; do
  printf "%s=%s " "$f" "$(cat /sys/fs/cgroup/llm-gateway-plugins/canary/$f 2>/dev/null)"
done
echo
echo "=== memory.current (usage readback) ==="
cat /sys/fs/cgroup/llm-gateway-plugins/canary/memory.current 2>/dev/null
echo "=== cgroup.procs ==="
cat /sys/fs/cgroup/llm-gateway-plugins/canary/cgroup.procs 2>/dev/null

# 验收点 4：plugin-nav 就绪面（清单步 2 的 API 形态）
echo "=== step4: plugin-nav ==="
curl -s -m 5 "http://127.0.0.1:18080/api/v1/plugin-nav" -H "Authorization: Bearer canary-admin-key" | head -c 500
echo

echo "CGROUP_MODE=$CGROUP_OK"
kill "$GW_PID" 2>/dev/null
wait "$GW_PID" 2>/dev/null
echo "=== canary run complete ==="

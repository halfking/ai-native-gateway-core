#!/bin/bash
# 并行测试运行器
# 用法: bash scripts/parallel-test-runner.sh [local|211] [--with-k6]

set -euo pipefail

# ==================== 配置 ====================

ENVIRONMENT=${1:-local}
WITH_K6=${2:-}

BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
REPORTS_DIR="${BASE_DIR}/test-reports/parallel-${TIMESTAMP}"

# 根据环境设置BASE_URL
if [ "$ENVIRONMENT" = "211" ]; then
  BASE_URL="${BASE_URL_211:-http://211服务器地址:端口}"
  echo "⚠️  请确认211环境的BASE_URL已正确设置"
else
  BASE_URL="${BASE_URL:-http://127.0.0.1:8782}"
fi

mkdir -p "${REPORTS_DIR}"

# ==================== 辅助函数 ====================

log() {
  echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" | tee -a "${REPORTS_DIR}/runner.log"
}

run_task() {
  local task_name=$1
  local task_cmd=$2
  
  log "启动任务: ${task_name}"
  
  (
    cd "${BASE_DIR}"
    eval "${task_cmd}" > "${REPORTS_DIR}/${task_name}.log" 2>&1
    exit_code=$?
    echo ${exit_code} > "${REPORTS_DIR}/${task_name}.exit"
    
    if [ ${exit_code} -eq 0 ]; then
      log "✓ ${task_name} 完成"
    else
      log "✗ ${task_name} 失败 (退出码: ${exit_code})"
    fi
  ) &
  
  echo $!
}

# ==================== 主流程 ====================

log "==================== 并行测试开始 ===================="
log "环境: ${ENVIRONMENT}"
log "BASE_URL: ${BASE_URL}"
log "报告目录: ${REPORTS_DIR}"
log "时间: $(date)"
log ""

# 检查依赖
log "检查依赖..."

if ! command -v maestro &> /dev/null; then
  log "警告: maestro 未安装，跳过Maestro测试"
  HAS_MAESTRO=0
else
  log "✓ maestro 已安装: $(maestro --version | head -n1)"
  HAS_MAESTRO=1
fi

if [ "$WITH_K6" = "--with-k6" ]; then
  if ! command -v k6 &> /dev/null; then
    log "警告: k6 未安装，跳过k6测试"
    HAS_K6=0
  else
    log "✓ k6 已安装: $(k6 version | head -n1)"
    HAS_K6=1
  fi
else
  log "未指定 --with-k6，跳过k6测试"
  HAS_K6=0
fi

log ""

# 定义Maestro测试任务
declare -a MAESTRO_TASKS=()
declare -a PIDS=()

if [ ${HAS_MAESTRO} -eq 1 ]; then
  # 从.env.local读取认证信息
  if [ -f "${BASE_DIR}/.env.local" ]; then
    source "${BASE_DIR}/.env.local"
  fi
  
  ADMIN_USER="${LLM_GATEWAY_ADMIN_USER:-admin}"
  ADMIN_PASSWORD="${LLM_GATEWAY_ADMIN_PASSWORD:-password}"
  
  # Maestro flow列表（不含dev-only）
  MAESTRO_FLOWS=(
    "01-public-guest:公开页面测试"
    "02-overview-live:总览和实时拓扑"
    "03-providers-models:供应商和模型"
    "04-tenants-portal:租户门户"
    "05-routing:路由域"
    "06-keys-requests:密钥和请求"
    "07-admin-config:后台配置"
    "08-admin-data-ops:数据运维"
    "09-ops-maintain:运维中心"
  )
  
  for flow in "${MAESTRO_FLOWS[@]}"; do
    flow_id="${flow%%:*}"
    flow_desc="${flow#*:}"
    
    cmd="maestro test \
      --driver-host-port 9222 --headless --no-ansi \
      --test-output-dir ${REPORTS_DIR}/maestro-${flow_id} \
      -e BASE_URL=${BASE_URL} \
      -e ADMIN_USER=${ADMIN_USER} \
      -e ADMIN_PASSWORD=${ADMIN_PASSWORD} \
      maestro/flows/${flow_id}.yaml"
    
    pid=$(run_task "maestro-${flow_id}" "$cmd")
    PIDS+=($pid)
    
    # 避免同时启动太多Chrome实例，间隔5秒
    sleep 5
  done
fi

# 定义k6测试任务
if [ ${HAS_K6} -eq 1 ]; then
  K6_TESTS=(
    "api-load:API负载测试"
    "streaming-load:流式接口测试"
    "admin-load:管理后台测试"
    "mixed-load:混合负载测试"
  )
  
  for test in "${K6_TESTS[@]}"; do
    test_id="${test%%:*}"
    test_desc="${test#*:}"
    
    cmd="k6 run \
      -e BASE_URL=${BASE_URL} \
      --out json=${REPORTS_DIR}/k6-${test_id}.json \
      k6-scripts/${test_id}-test.js"
    
    pid=$(run_task "k6-${test_id}" "$cmd")
    PIDS+=($pid)
    
    # k6测试间隔3秒启动
    sleep 3
  done
fi

# 等待所有任务完成
log ""
log "等待所有任务完成..."
log "运行中的任务数: ${#PIDS[@]}"

for pid in "${PIDS[@]}"; do
  wait $pid || true
done

log ""
log "所有任务已完成"

# ==================== 结果汇总 ====================

log ""
log "==================== 测试结果汇总 ===================="

TOTAL=0
PASSED=0
FAILED=0

for exit_file in "${REPORTS_DIR}"/*.exit; do
  if [ ! -f "$exit_file" ]; then
    continue
  fi
  
  TOTAL=$((TOTAL + 1))
  task_name=$(basename "$exit_file" .exit)
  exit_code=$(cat "$exit_file")
  
  if [ "$exit_code" -eq 0 ]; then
    log "✓ ${task_name}: 通过"
    PASSED=$((PASSED + 1))
  else
    log "✗ ${task_name}: 失败 (退出码: ${exit_code})"
    FAILED=$((FAILED + 1))
    
    # 显示失败日志的最后20行
    log "  失败日志摘要:"
    tail -n 20 "${REPORTS_DIR}/${task_name}.log" | sed 's/^/    /' | tee -a "${REPORTS_DIR}/runner.log"
  fi
done

log ""
log "==================== 统计 ===================="
log "总任务数: ${TOTAL}"
log "通过: ${PASSED}"
log "失败: ${FAILED}"
log "成功率: $(awk "BEGIN {printf \"%.1f\", ($PASSED/$TOTAL)*100}")%"

# 生成HTML报告索引
cat > "${REPORTS_DIR}/index.html" <<EOF
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>并行测试报告 - ${TIMESTAMP}</title>
  <style>
    body { font-family: Arial, sans-serif; margin: 20px; background: #f5f5f5; }
    .header { background: #2c3e50; color: white; padding: 20px; border-radius: 5px; }
    .summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin: 20px 0; }
    .card { background: white; padding: 15px; border-radius: 5px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
    .card h3 { margin: 0 0 10px 0; font-size: 14px; color: #666; }
    .card .value { font-size: 32px; font-weight: bold; }
    .passed { color: #27ae60; }
    .failed { color: #e74c3c; }
    .tasks { background: white; padding: 20px; border-radius: 5px; margin-top: 20px; }
    .task { padding: 10px; border-bottom: 1px solid #eee; }
    .task:hover { background: #f9f9f9; }
    .task-name { font-weight: bold; }
    .task-status { float: right; padding: 5px 10px; border-radius: 3px; }
    .status-pass { background: #d4edda; color: #155724; }
    .status-fail { background: #f8d7da; color: #721c24; }
  </style>
</head>
<body>
  <div class="header">
    <h1>并行测试报告</h1>
    <p>环境: ${ENVIRONMENT} | 时间: ${TIMESTAMP} | BASE_URL: ${BASE_URL}</p>
  </div>
  
  <div class="summary">
    <div class="card">
      <h3>总任务数</h3>
      <div class="value">${TOTAL}</div>
    </div>
    <div class="card">
      <h3>通过</h3>
      <div class="value passed">${PASSED}</div>
    </div>
    <div class="card">
      <h3>失败</h3>
      <div class="value failed">${FAILED}</div>
    </div>
    <div class="card">
      <h3>成功率</h3>
      <div class="value">$(awk "BEGIN {printf \"%.1f\", ($PASSED/$TOTAL)*100}")%</div>
    </div>
  </div>
  
  <div class="tasks">
    <h2>任务详情</h2>
EOF

for exit_file in "${REPORTS_DIR}"/*.exit; do
  if [ ! -f "$exit_file" ]; then
    continue
  fi
  
  task_name=$(basename "$exit_file" .exit)
  exit_code=$(cat "$exit_file")
  log_file="${task_name}.log"
  
  if [ "$exit_code" -eq 0 ]; then
    status_class="status-pass"
    status_text="通过"
  else
    status_class="status-fail"
    status_text="失败"
  fi
  
  cat >> "${REPORTS_DIR}/index.html" <<EOF
    <div class="task">
      <span class="task-name">${task_name}</span>
      <span class="task-status ${status_class}">${status_text}</span>
      <br/>
      <small><a href="${log_file}">查看日志</a></small>
    </div>
EOF
done

cat >> "${REPORTS_DIR}/index.html" <<EOF
  </div>
</body>
</html>
EOF

log ""
log "HTML报告已生成: ${REPORTS_DIR}/index.html"
log "使用浏览器打开查看: open ${REPORTS_DIR}/index.html"

# ==================== 退出 ====================

if [ ${FAILED} -eq 0 ]; then
  log ""
  log "✓ 所有测试通过！"
  exit 0
else
  log ""
  log "✗ 有 ${FAILED} 个任务失败"
  exit 1
fi

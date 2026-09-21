#!/usr/bin/env bash
# =============================================================================
# modelctl.sh — 本地大模型托管服务启停控制（网关本地供应商配套）
#
# 用途：网关把本机推理服务整理成 kind='local' 供应商（migration 671），本脚本
#       负责这些服务的 启动/停止/状态/自动拉起（ensure = 未运行则启动）。
#
# 用法：
#   ./modelctl.sh ensure mlx-lm   [端口]   # 未运行则启动（探活等待）
#   ./modelctl.sh start  dspark   [端口]
#   ./modelctl.sh status ollama   [端口]
#   ./modelctl.sh stop   mlx-lm   [端口]
#   ./modelctl.sh list                     # 列出支持的托管类型与默认端口
#
# 环境变量：
#   MLX_LM_MODEL      mlx-lm 使用的模型路径（默认 Qwen3.8-27B 4bit 镜像目录）
#   MLX_DSPARK_MODEL  mlx-dspark 主模型路径（同上）
#   MLX_DSPARK_DRAFTER mlx-dspark drafter（默认 incoai/Qwen3.8-27B-DFlash2）
#   OLLAMA_HOST       ollama 监听地址（默认 127.0.0.1）
#
# 支持的托管类型（与 provider_catalog local-* 条目一一对应）：
#   ollama(11434)  mlx-lm(8080)  dspark(8081)  llamacpp(8082)  lmstudio(1234)  vllm(8000)
# =============================================================================
set -euo pipefail

AI_TOOLS_DIR="${AI_TOOLS_DIR:-$HOME/workspace/ai-native-tools}"
VENV_MLX="${VENV_MLX:-$AI_TOOLS_DIR/venv_mlx}"
MLX_MODEL_DIR="${MLX_LM_MODEL:-$AI_TOOLS_DIR/models/qwen3.8-27b-uncensored-mlx-mirror/4-bit}"
DSPARK_DRAFTER="${MLX_DSPARK_DRAFTER:-incoai/Qwen3.8-27B-DFlash2}"
LOG_DIR="${TMPDIR:-/tmp}/llmgw-local-models"
mkdir -p "$LOG_DIR"

declare -A DEFAULT_PORT=(
  [ollama]=11434 [mlx-lm]=8080 [dspark]=8081 [llamacpp]=8082 [lmstudio]=1234 [vllm]=8000
)

port_for() { echo "${DEFAULT_PORT[$1]:-}"; }

health_url() {
  case "$1" in
    ollama)  echo "http://127.0.0.1:$2/v1/models" ;;
    *)       echo "http://127.0.0.1:$2/health" ;;
  esac
}

is_running() {
  local type="$1" port="$2" body
  # 必须验证是 OpenAI 兼容服务（/v1/models 返回 data/object 字段），
  # 而非任意恰好占用该端口的进程 —— 8080 等常用端口易被其他服务占用。
  body=$(curl -sf -m 2 "http://127.0.0.1:$port/v1/models" 2>/dev/null) || return 1
  echo "$body" | grep -qE '"(data|object|id)"'
}

pidfile() { echo "$LOG_DIR/$1-$2.pid"; }

start_server() {
  local type="$1" port="$2"
  local log="$LOG_DIR/$type-$port.log"
  case "$type" in
    mlx-lm)
      [ -d "$MLX_MODEL_DIR" ] || { echo "✗ 模型目录不存在: $MLX_MODEL_DIR"; exit 1; }
      ( cd "$AI_TOOLS_DIR" && nohup "$VENV_MLX/bin/mlx_lm" server \
          --model "$MLX_MODEL_DIR" --host 127.0.0.1 --port "$port" \
          --temp 0.2 --top-p 0.95 --max-tokens 512 \
          --chat-template-args '{"enable_thinking":false}' \
          --prompt-cache-size 8192 --decode-concurrency 4 --prompt-concurrency 4 \
          >"$log" 2>&1 & echo $! > "$(pidfile "$type" "$port")" )
      ;;
    dspark)
      [ -d "$MLX_MODEL_DIR" ] || { echo "✗ 模型目录不存在: $MLX_MODEL_DIR"; exit 1; }
      ( cd "$AI_TOOLS_DIR" && nohup "$VENV_MLX/bin/mlx-dspark" serve \
          --mode dflash --drafter "$DSPARK_DRAFTER" --model "$MLX_MODEL_DIR" \
          --no-thinking --host 127.0.0.1 --port "$port" \
          >"$log" 2>&1 & echo $! > "$(pidfile "$type" "$port")" )
      ;;
    ollama)
      ( nohup ollama serve >"$log" 2>&1 & echo $! > "$(pidfile "$type" "$port")" )
      ;;
    vllm)
      echo "✗ vllm 启动命令依赖具体模型参数，请手工启动后使用 status/ensure 探活"; exit 1 ;;
    llamacpp)
      echo "✗ llamacpp 启动命令依赖 GGUF 模型路径（LLAMA_CPP_MODEL），请手工启动"; exit 1 ;;
    lmstudio)
      echo "✗ LM Studio 需在 GUI 中启动 Local Server（端口 $port）"; exit 1 ;;
    *)
      echo "✗ 未知托管类型: $type（支持: ${!DEFAULT_PORT[@]}）"; exit 1 ;;
  esac
  echo "$log"
}

wait_healthy() {
  local type="$1" port="$2" tries="${3:-150}"
  for _ in $(seq 1 "$tries"); do
    if is_running "$type" "$port"; then return 0; fi
    sleep 2
  done
  return 1
}

cmd="${1:-help}"; type="${2:-}"; port="${3:-}"

case "$cmd" in
  list)
    echo "支持的本地托管类型（类型:默认端口）:"
    for t in ollama mlx-lm dspark llamacpp lmstudio vllm; do
      echo "  $t:${DEFAULT_PORT[$t]}"
    done
    ;;
  start)
    [ -n "$type" ] || { echo "用法: $0 start <type> [port]"; exit 1; }
    port="${port:-$(port_for "$type")}"
    if is_running "$type" "$port"; then echo "✓ $type 已在运行 (:$port)"; exit 0; fi
    echo "→ 启动 $type (:$port) ..."
    log=$(start_server "$type" "$port")
    if wait_healthy "$type" "$port"; then echo "✓ $type 就绪 (:$port)，日志: $log"
    else echo "✗ $type 启动超时，日志: $log"; exit 1; fi
    ;;
  ensure)
    [ -n "$type" ] || { echo "用法: $0 ensure <type> [port]"; exit 1; }
    port="${port:-$(port_for "$type")}"
    if is_running "$type" "$port"; then
      echo "✓ $type 已在运行 (:$port)"
    else
      echo "→ $type 未运行，自动启动 (:$port) ..."
      log=$(start_server "$type" "$port")
      if wait_healthy "$type" "$port"; then echo "✓ $type 就绪 (:$port)，日志: $log"
      else echo "✗ $type 启动超时，日志: $log"; exit 1; fi
    fi
    ;;
  status)
    [ -n "$type" ] || { echo "用法: $0 status <type> [port]"; exit 1; }
    port="${port:-$(port_for "$type")}"
    if is_running "$type" "$port"; then echo "running  $type :$port"; else echo "down     $type :$port"; fi
    ;;
  stop)
    [ -n "$type" ] || { echo "用法: $0 stop <type> [port]"; exit 1; }
    port="${port:-$(port_for "$type")}"
    pf=$(pidfile "$type" "$port")
    if [ -f "$pf" ]; then
      pid=$(cat "$pf")
      if kill -0 "$pid" 2>/dev/null; then kill "$pid" && echo "✓ 已停止 $type (pid=$pid)"; fi
      rm -f "$pf"
    else
      echo "（无 pid 文件，仅提示）如需停止请手工 kill 对应进程"
    fi
    ;;
  *)
    grep '^# 用法' -A 6 "$0" | sed 's/^# \{0,2\}//'
    ;;
esac

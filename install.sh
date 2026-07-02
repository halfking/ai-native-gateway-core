#!/usr/bin/env bash
# install.sh — macOS / Linux 一键安装入口
# 自动选用匹配的 llm-gw-installer 二进制
# 默认部署到 ~/llm-gateway/

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── 探测当前 OS / arch ─────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "${ARCH}" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  amd64)   ARCH="amd64" ;;
  arm64)   ARCH="arm64" ;;
  loongarch64) ARCH="loong64" ;;
  *)
    echo "❌ 不支持的架构: ${ARCH}"
    exit 1
    ;;
esac

# ── 选用二进制 ─────────────────────────────────────────────────
BINARY="${SCRIPT_DIR}/llm-gw-installer-${OS}-${ARCH}"

if [[ ! -f "${BINARY}" ]]; then
  # 兜底：使用不带平台后缀的
  BINARY="${SCRIPT_DIR}/llm-gw-installer"
fi

if [[ ! -f "${BINARY}" ]]; then
  echo "❌ 未找到 installer 二进制（期望 ${BINARY}）"
  echo "请重新下载 release 包"
  exit 1
fi

chmod +x "${BINARY}"

# ── 计算默认安装目录 ──────────────────────────────────────────
# 优先级：
#   1. 环境变量 LLM_GATEWAY_HOME
#   2. ~/llm-gateway
DEFAULT_HOME="${LLM_GATEWAY_HOME:-$HOME/llm-gateway}"

echo "╔════════════════════════════════════════════════════════════════╗"
echo "║  LLM Gateway 一键安装器                                       ║"
echo "╠════════════════════════════════════════════════════════════════╣"
echo "║  默认部署到: ${DEFAULT_HOME}"
echo "║  (可用 LLM_GATEWAY_HOME 环境变量覆盖)"
echo "╚════════════════════════════════════════════════════════════════╝"
echo ""

exec "${BINARY}" --dir "${DEFAULT_HOME}" "$@"

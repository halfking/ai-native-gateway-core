#!/usr/bin/env bash
# load-env.sh — 统一环境变量加载脚本
#
# 用法:
#   source scripts/load-env.sh              # 自动检测环境并加载
#   source scripts/load-env.sh --server 184  # 强制 184 生产环境
#   source scripts/load-env.sh --server 71   # 强制 71 生产环境
#   source scripts/load-env.sh --server local # 强制本地开发环境
#
# 搜索顺序（高→低）:
#   1. 已存在的环境变量（不覆盖）
#   2. /etc/llm-gateway-go/env（生产服务器）
#   3. .env.184.enc / .env.71.enc（SOPS 解密，用于部署脚本）
#   4. .env.local（本地开发）
#
# 注意: 此脚本设计为 source 执行（设置当前 shell 环境变量）。
#       直接执行时仅打印已加载的变量列表。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ------ 解析参数 ------
TARGET="${1:-auto}"
if [ "$TARGET" = "--server" ]; then
    TARGET="${2:-auto}"
fi

# ------ 检测运行环境 ------
detect_env() {
    # 生产服务器检测
    if [ -f /etc/llm-gateway-go/env ]; then
        echo "production"
        return
    fi

    # K8s 环境检测（通过环境变量）
    if [ -n "${KUBERNETES_SERVICE_HOST:-}" ]; then
        echo "production"
        return
    fi

    # 本地开发
    echo "local"
}

load_env_file() {
    local file="$1"
    local label="$2"

    if [ ! -f "$file" ]; then
        echo "[load-env] ⚠️  $label 文件不存在: $file" >&2
        return 1
    fi

    echo "[load-env] 📂 加载 $label: $file" >&2
    set -a
    source "$file"
    set +a
}

load_sops_env() {
    local file="$1"
    local label="$2"

    if [ ! -f "$file" ]; then
        echo "[load-env] ⚠️  SOPS 文件不存在: $file" >&2
        return 1
    fi

    if ! command -v sops &>/dev/null; then
        echo "[load-env] ❌ sops 命令未找到，无法解密 $file" >&2
        return 1
    fi

    echo "[load-env] 🔐 解密 SOPS: $label ($file)" >&2
    eval "$(sops -d "$file")"
}

# ------ 主流程 ------
case "$TARGET" in
    local)
        load_env_file "$PROJECT_DIR/.env.local" ".env.local"
        ;;
    184)
        load_env_file "/etc/llm-gateway-go/env" "/etc/llm-gateway-go/env" || true
        load_sops_env "$PROJECT_DIR/.env.184.enc" ".env.184.enc" || true
        ;;
    71)
        load_env_file "/etc/llm-gateway-go/env" "/etc/llm-gateway-go/env" || true
        load_sops_env "$PROJECT_DIR/.env.71.enc" ".env.71.enc" || true
        ;;
    auto|*)
        ENV=$(detect_env)
        echo "[load-env] 🔍 检测到环境: $ENV" >&2

        case "$ENV" in
            production)
                load_env_file "/etc/llm-gateway-go/env" "/etc/llm-gateway-go/env" || true
                load_sops_env "$PROJECT_DIR/.env.184.enc" ".env.184.enc" || true
                load_sops_env "$PROJECT_DIR/.env.71.enc" ".env.71.enc" || true
                ;;
            local)
                load_env_file "$PROJECT_DIR/.env.local" ".env.local" || true
                ;;
        esac
        ;;
esac

# 导出关键变量（标准化命名）
export LLM_GATEWAY_184_HOST="${LLM_GATEWAY_184_HOST:-${INTERNAL_PUBLIC_IP:-}}"
export LLM_GATEWAY_71_HOST="${LLM_GATEWAY_71_HOST:-${HOST_71_IP:-}}"
export LLM_GATEWAY_184_SSH_PORT="${LLM_GATEWAY_184_SSH_PORT:-25022}"
export LLM_GATEWAY_71_SSH_PORT="${LLM_GATEWAY_71_SSH_PORT:-25022}"

# ------ 打印已加载的变量（仅 source 模式） ------
if [ "${BASH_SOURCE[0]}" != "${0}" ]; then
    echo "[load-env] ✅ 环境变量加载完成" >&2
    echo "[load-env] 📋 已设置的关键变量:" >&2
    for var in LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_API_KEY LLM_GATEWAY_ADMIN_API_KEY \
               LLM_GATEWAY_REDIS_ADDR LLM_GATEWAY_LISTEN LLM_GATEWAY_ENV \
               LLM_GATEWAY_184_HOST LLM_GATEWAY_71_HOST; do
        if [ -n "${!var:-}" ]; then
            echo "   ${var}=${!var:0:20}..." >&2
        fi
    done
fi

#!/usr/bin/env bash
# load-env.sh — 统一环境变量加载脚本
#
# 用法:
#   source scripts/load-env.sh              # 自动检测环境并加载
#   source scripts/load-env.sh --server 252  # 强制 252 (阿里云 llm.itestu.cn) 生产环境
#   source scripts/load-env.sh --server 154  # 154 (llm.kxpms.cn 主机部署)
#   source scripts/load-env.sh --server kaixuan-1  # 内网 k3s 控制面
#   source scripts/load-env.sh --server 154  # 强制 154 生产环境
#   source scripts/load-env.sh --server local # 强制本地开发环境
#
# 搜索顺序（高→低）:
#   1. 已存在的环境变量（不覆盖）
#   2. /etc/llm-gateway-go/env（生产服务器）
#   3. .env.252.enc / .env.154.enc / .env.kaixuan-1.enc（SOPS 解密，用于部署脚本）
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
    252)
        load_env_file "/etc/llm-gateway-go/env" "/etc/llm-gateway-go/env" || true
        load_sops_env "$PROJECT_DIR/.env.252.enc" ".env.252.enc" || true
        ;;
    154)
        load_env_file "/etc/llm-gateway-go/env" "/etc/llm-gateway-go/env" || true
        load_sops_env "$PROJECT_DIR/.env.154.enc" ".env.154.enc" || true
        ;;
    kaixuan-1)
        load_env_file "/etc/llm-gateway-go/env" "/etc/llm-gateway-go/env" || true
        load_sops_env "$PROJECT_DIR/.env.kaixuan-1.enc" ".env.kaixuan-1.enc" || true
        ;;
    auto|*)
        ENV=$(detect_env)
        echo "[load-env] 🔍 检测到环境: $ENV" >&2

        case "$ENV" in
            production)
                load_env_file "/etc/llm-gateway-go/env" "/etc/llm-gateway-go/env" || true
                load_sops_env "$PROJECT_DIR/.env.252.enc" ".env.252.enc" || true
                load_sops_env "$PROJECT_DIR/.env.154.enc" ".env.154.enc" || true
                ;;
            local)
                load_env_file "$PROJECT_DIR/.env.local" ".env.local" || true
                ;;
        esac
        ;;
esac

# ------ 密钥互异校验门（N20-6，2026-09-29）------
# 事故模式（docs/audit/2026-09-28-vapeur-protocol-adaptation.md §七）：
# .env.local 曾出现 SK 与 CEK 同值，容器解密全挂 → 全部候选标记不可用
# → dispatch ErrNoRoute → 5/5 全 503。该文件不入版本控制（.gitignore），
# 同值漂移可无痕复发，故在统一加载出口设门。
# 语义：两者同时非空且相等 → 硬失败（source 场景 return 1 终止加载，
# 调用方 set -e 下部署即中止；直接执行场景 exit 1）。CEK 留空合法
# （运行时从 SK 派生 keyring，见 config.go HostedTasksConfig 注释）；
# 长度偏离已知形态仅告警不阻塞——base64 两种 padding 形态均合法，
# 未来换钥匙形态不应被门卡死。
check_secret_key_distinct() {
    local sk="${LLM_GATEWAY_SECRET_KEY:-${SECRET_KEY:-}}"
    local cek="${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-${CREDENTIAL_ENCRYPTION_KEY:-}}"
    if [ -z "$sk" ] || [ -z "$cek" ]; then
        return 0
    fi
    if [ "$sk" = "$cek" ]; then
        echo "[load-env] ❌ 密钥校验失败（N20-6）: SECRET_KEY 与 CREDENTIAL_ENCRYPTION_KEY 同值" >&2
        echo "[load-env]    该组合即 2026-09-28 全 503 事故模式（解密全挂）。请修正 .env.local：" >&2
        echo "[load-env]    SK 64 位、CEK 43/44 位且互异；或将 CEK 留空由 SK 派生" >&2
        return 1
    fi
    if [ "${#sk}" -ne 64 ]; then
        echo "[load-env] ⚠️  LLM_GATEWAY_SECRET_KEY 长度 ${#sk}（已知形态 64 位），请人工确认" >&2
    fi
    if [ "${#cek}" -ne 44 ] && [ "${#cek}" -ne 43 ]; then
        echo "[load-env] ⚠️  LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY 长度 ${#cek}（已知形态 43/44 位），请人工确认" >&2
    fi
    return 0
}

if [ "${BASH_SOURCE[0]}" != "${0}" ]; then
    check_secret_key_distinct || return 1
else
    check_secret_key_distinct || exit 1
fi

# 导出关键变量（标准化命名）
export LLM_GATEWAY_252_HOST="${LLM_GATEWAY_252_HOST:-${HOST_252_INTERNAL_IP:-172.16.2.210}}"
export LLM_GATEWAY_154_HOST="${LLM_GATEWAY_154_HOST:-${HOST_154_INTERNAL_IP:-172.16.2.209}}"
export LLM_GATEWAY_KAIXUAN_1_HOST="${LLM_GATEWAY_KAIXUAN_1_HOST:-${KAIXUAN_1_IP:-192.168.31.28}}"
export LLM_GATEWAY_252_SSH_PORT="${LLM_GATEWAY_252_SSH_PORT:-25022}"


# ------ 打印已加载的变量（仅 source 模式） ------
if [ "${BASH_SOURCE[0]}" != "${0}" ]; then
    echo "[load-env] ✅ 环境变量加载完成" >&2
    echo "[load-env] 📋 已设置的关键变量:" >&2
    for var in LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_API_KEY LLM_GATEWAY_ADMIN_API_KEY \
               LLM_GATEWAY_REDIS_ADDR LLM_GATEWAY_LISTEN LLM_GATEWAY_ENV \
               LLM_GATEWAY_252_HOST LLM_GATEWAY_154_HOST LLM_GATEWAY_KAIXUAN_1_HOST; do
        if [ -n "${!var:-}" ]; then
            echo "   ${var}=${!var:0:20}..." >&2
        fi
    done
fi

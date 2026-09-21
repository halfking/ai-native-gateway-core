#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-154-via-252.sh — 154 部署入口 (via SSH wrapper)
#
# 解决 154 公网 SSH 被防火墙挡的问题:
#   - 154 公网 47.97.111.154 的所有 SSH 端口 (22, 25022, ...) 被防火墙全挡.
#   - 154 内网 172.16.2.209 SSH 同样被挡, 只有 443 HTTPS 通.
#   - 具体跳板、目标地址和凭据由 SSH_WRAPPER_* / SSHPASS_154 注入，
#     脚本不内置任何凭据或服务器真值。
#
# 用法:
#   bash scripts/deploy-154-via-252.sh status         # 查看 245 (llm.kxpms.cn) 状态
#   bash scripts/deploy-154-via-252.sh deploy         # 部署新版本
#   bash scripts/deploy-154-via-252.sh rollback       # 回滚到上一个 verified
#
# 等价于 (但本脚本封装了 PATH):
#   PATH=scripts:$PATH bash scripts/deploy-seamless.sh deploy 154
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 把 scripts/ (绝对路径) 加到 PATH 前, 让 deploy-seamless 调 ssh 时自动走 wrapper
# 注意: 必须用绝对路径, 否则 bash which 找到的还是系统 ssh
export PATH="$SCRIPT_DIR:$PATH"

: "${SSH_WRAPPER_HOP_KEY:?set SSH_WRAPPER_HOP_KEY before deploying}"
: "${SSH_WRAPPER_HOP_HOST:?set SSH_WRAPPER_HOP_HOST before deploying}"
: "${SSH_WRAPPER_TARGET_HOST:?set SSH_WRAPPER_TARGET_HOST before deploying}"
: "${SSH_WRAPPER_TARGET_IP:?set SSH_WRAPPER_TARGET_IP before deploying}"
: "${SSHPASS_154:?set SSHPASS_154 before deploying}"

cd "$PROJECT_ROOT"
exec bash "$SCRIPT_DIR/deploy-seamless.sh" "$@" 154

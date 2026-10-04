#!/usr/bin/env bash
# deploy-252.sh — llm-gateway-go 252 部署统一入口（薄封装，对齐根 deploy-local.sh 先例）。
#
# 实际部署逻辑只存在于 scripts/deploy-252-gateway.sh（252 dev 网关 systemd
# 部署：构建 linux/amd64 → 上传 binary/env/web-mobile dist → systemd 安装 →
# healthz/version 验证）：
#   bash scripts/deploy-252-gateway.sh            # 构建 + 部署 + 验证
#   bash scripts/deploy-252-gateway.sh --dry-run  # 只打印计划
#
# 前置：source ~/.agents/skills/env-injector/scripts/env-injector.sh inject 252
# （脚本自身也会 fail-closed 重注入检查 SSOT KEY）。
#
# 前端契约：桌面 web/ 与移动 web-mobile/（挂 /m，Hyper 类型）由同一进程同一
# 端口服务；本入口把两者一并构建并上传。统一入口自动切换由两侧 entry-switch.js
# 承担（compact<600px → /m；large≥1280px 根入口 → /），网关不做 UA 嗅探。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIFIED="$SCRIPT_DIR/scripts/deploy-252-gateway.sh"

if [[ ! -x "$UNIFIED" && ! -f "$UNIFIED" ]]; then
  printf '[deploy-252] error: unified entry missing: %s\n' "$UNIFIED" >&2
  exit 1
fi

exec bash "$UNIFIED" "$@"

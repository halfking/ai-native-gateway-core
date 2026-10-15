#!/usr/bin/env bash
# =============================================================================
# deploy-211.sh — 211（192.168.31.211）一条命令部署入口（deploy-154.sh 风格薄包装）
#
# 全部参数原样转发给 deploy-211-docker.sh，本包装只内定两件事：
#   1) --via 192.168.31.211 —— 远程模式（tar 同步仓库 → ssh 到 211 以 root 执行）
#   2) 基础镜像推荐默认值（211 已 load 的本地离线包；调用方显式传值优先）
#
# 凭据（SECRET_KEY / CREDENTIAL_ENCRYPTION_KEY）、Redis 库号、版本元数据均已
# 持久化在 211 的 /opt/llm-gateway-go-211/，无需在本机传密钥。
#
# 用法:
#   bash scripts/deploy-211.sh                    # 部署最新代码到 211
#   bash scripts/deploy-211.sh --redis-db 9       # 其余参数原样转发
#
# 注意: --status / --stop 与 --via 不兼容（deploy-211-docker.sh 的既有约束），
# 需要时 ssh 192.168.31.211 后在目标机上直接跑 deploy-211-docker.sh --status。
# =============================================================================
set -euo pipefail

D211_WRAPPER_HOST="${D211_WRAPPER_HOST:-192.168.31.211}"
export D211_GO_BASE_IMAGE="${D211_GO_BASE_IMAGE:-kx-base/golang:1.27-alpine-amd64}"
export D211_RUNTIME_BASE_IMAGE="${D211_RUNTIME_BASE_IMAGE:-alpine:3.22}"

exec bash "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/deploy-211-docker.sh" --via "$D211_WRAPPER_HOST" "$@"

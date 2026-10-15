#!/usr/bin/env bash
# =====================================================================
# deploy/deploy.sh — DEPRECATION WRAPPER (Slice 8)
#
# Per spec cf8aad1a9 §"Compatibility disposition":
#   deploy/deploy.sh → Make fail-closed deprecation wrappers
#                       pointing to ./scripts/deploy.sh
#
# This script used to ship its own docker / kubectl / systemctl flow
# independent from scripts/deploy.sh. As of 2026-07-13 that flow is
# superseded by the canonical CLI:
#
#   ./scripts/deploy.sh plan <target>
#   ./scripts/deploy.sh deploy <target>
#   ./scripts/deploy.sh verify <target>
#   ./scripts/deploy.sh rollback <target> [--to <version>]
#
# Calling this old entrypoint forwards arguments to the canonical CLI.
# A deprecation warning is printed once per invocation (not per call)
# so CI consumers see the migration path without scripts breaking.
#
# Exit codes match the underlying canonical CLI (0, 4=no_rollback,
# 64=usage error, etc.). The deprecation marker does NOT change them.
# =====================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANONICAL="$REPO_ROOT/scripts/deploy.sh"

warn() {
  printf '\033[1;33m!\033[0m %s\n' "$*" >&2
}

# ⚠️ 2026-10-14（12h 审计轮 C-P2-2）：scripts/deploy.sh 已由 ecbebfa55
# （"清理无用 deploy 脚本 —— 保留四主脚本依赖闭包，删 32 个死入口"）删除，
# 于是本包装器的 CANONICAL 恒不存在 ⇒ **无条件 exit 64**。ecbebfa55 自己
# 留下了这两个以被删脚本为唯一目标的壳，与该提交"删死入口"的口径不一致。
#
# 保持 fail-closed（不猜目标主机去转发），但把可执行的替代路径讲清楚，
# 避免运维照旧 runbook 调用后只看到一句无信息量的 "cannot find canonical"。
if [[ ! -x "$CANONICAL" ]]; then
  {
    echo "ERROR: $0 的 canonical CLI 已随 ecbebfa55 一并删除：$CANONICAL"
    echo ""
    echo "  本包装器是已废弃的转发壳（spec cf8aad1a9 Slice 8），它转发的目标"
    echo "  已不存在，因此本脚本**必然**以 exit 64 失败。这是 fail-closed 设计。"
    echo ""
    echo "  请改用现役部署脚本（先确认目标主机，再选对应脚本）："
    echo "    245          → scripts/deploy-245.sh"
    echo "    154          → scripts/deploy-154.sh"
    echo "    252          → scripts/deploy-252-gateway.sh"
    echo "    kaixuan-1    → scripts/deploy-kaixuan1.sh"
    echo "    docker 211   → scripts/deploy-211-docker.sh"
    echo "    通用/无缝    → scripts/deploy-seamless.sh"
    echo "    本地         → scripts/deploy-local.sh"
    echo ""
    echo "  本仓不再有统一的 scripts/deploy.sh；各脚本参数以其 --help 为准。"
  } >&2
  exit 64
fi

warn "deploy/deploy.sh is deprecated (spec cf8aad1a9 Slice 8)"
warn "Forwarding to canonical CLI: $CANONICAL $*"
warn "Update CI / runbooks to call scripts/deploy.sh directly."

exec "$CANONICAL" "$@"
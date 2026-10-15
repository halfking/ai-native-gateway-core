#!/usr/bin/env bash
# =====================================================================
# deploy/rollback.sh — DEPRECATION WRAPPER (Slice 8)
#
# Per spec cf8aad1a9 §"Compatibility disposition":
#   deploy/rollback.sh → Make fail-closed deprecation wrappers
#                         pointing to ./scripts/deploy.sh
#
# The old deploy/rollback.sh used to consume /var/lib/deploy-tracker
# tags and run its own docker / kubectl flow. The canonical CLI now
# owns the rollback contract:
#
#   ./scripts/deploy.sh rollback 245 [--to <version>]
#   ./scripts/deploy.sh rollback 154                  # runbook guidance
#   ./scripts/deploy.sh rollback 252                  # deferred (refused)
#   ./scripts/deploy.sh rollback 186                  # retired (refused)
#
# For 245 the canonical CLI does versioned rollback via the
# releases/${VERSION}/`current/` symlink chain. For 154 it points
# operator at the existing runbook. For 186/252/kaixuan-* it refuses.
# All callers must migrate.
# =====================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANONICAL="$REPO_ROOT/scripts/deploy.sh"

warn() {
  printf '\033[1;33m!\033[0m %s\n' "$*" >&2
}

# ⚠️ 2026-10-14（12h 审计轮 C-P2-2）：scripts/deploy.sh 已由 ecbebfa55
# （"清理无用 deploy 脚本 —— 保留四主脚本依赖闭包，删 32 个死入口"）删除，
# 于是本包装器的 CANONICAL 恒不存在 ⇒ **无条件 exit 64**。rollback 是出事
# 时才会有人敲的命令，"永远失败"与"已清理"在运维手册上不是一回事：
# 旧 runbook 与 docs/audit/2026-08-29-deployment-contract-audit.md:56 等审计
# 文档都点名过这条路径。
#
# 刻意**不**把它转发到 scripts/deploy-{154,245,252-gateway,seamless}.sh
# 中的任意一个：四者各自的 rollback 契约不同（245 走 releases/VERSION/current
# 符号链做版本回滚；154 只给 runbook 指引；252 deferred、186 retired 都拒绝），
# 猜一个转发等于在事故现场静默换掉回滚语义——比明确失败危险得多。
# 故此处保持 fail-closed，但把"为什么失败、现在该敲哪个"讲清楚。
if [[ ! -x "$CANONICAL" ]]; then
  {
    echo "ERROR: $0 的 canonical CLI 已随 ecbebfa55 一并删除：$CANONICAL"
    echo ""
    echo "  本包装器是已废弃的转发壳（spec cf8aad1a9 Slice 8），它转发的目标"
    echo "  已不存在，因此本脚本**必然**以 exit 64 失败。这是 fail-closed 设计，"
    echo "  不是环境问题 —— 不要重跑本脚本。"
    echo ""
    echo "  请改用现役部署脚本（先确认目标主机，再选对应脚本）："
    echo "    245  → scripts/deploy-245.sh          rollback 契约：releases/VERSION/current 符号链"
    echo "    154  → scripts/deploy-154.sh          rollback 契约：runbook 指引"
    echo "    252  → scripts/deploy-252-gateway.sh  rollback：deferred（拒绝）"
    echo "    通用 → scripts/deploy-seamless.sh"
    echo ""
    echo "  各脚本的 rollback 子命令与拒绝语义以各自 --help 为准；"
    echo "  本仓不再有统一的 scripts/deploy.sh。"
  } >&2
  exit 64
fi

warn "deploy/rollback.sh is deprecated (spec cf8aad1a9 Slice 8)"
warn "Forwarding to canonical CLI: $CANONICAL rollback $*"
warn "Update CI / runbooks to call scripts/deploy.sh rollback <target> directly."

exec "$CANONICAL" rollback "$@"
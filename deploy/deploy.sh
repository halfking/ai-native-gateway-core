#!/usr/bin/env bash
# =====================================================================
# deploy/deploy.sh — DEPRECATION WRAPPER（Slice 8 遗留壳，**当前恒 fail-closed**）
#
# Per spec cf8aad1a9 §"Compatibility disposition":
#   deploy/deploy.sh → Make fail-closed deprecation wrappers
#                       pointing to ./scripts/deploy.sh
#
# ─────────────────────────────────────────────────────────────────────
# ★ 2026-11-09：本文件头**已订正为现状**（此前它是一句**过期承诺**）。
#
#   旧头写的是「As of 2026-07-13 that flow is superseded by the canonical
#   CLI: ./scripts/deploy.sh plan|deploy|verify|rollback <target>」＋
#   「Calling this old entrypoint forwards arguments to the canonical
#   CLI」＋「Exit codes match the underlying canonical CLI」。
#
#   ★★ 那三句**全是假的**，而且是**最贵的那种假**（不是注释里的历史读数，
#     而是**读的人会照着敲的命令**）：
#     · `scripts/deploy.sh` 已由 ecbebfa55（"清理无用 deploy 脚本 ——
#       保留四主脚本依赖闭包，删 32 个死入口"，2026-07-13 后）**删除**
#       ⇒ 本包装器转发的目标**恒不存在** ⇒ 它**必然** exit 64；
#     · ⇒ 「Exit codes match the underlying canonical CLI」= **没有 underlying
#       CLI 可谈** ⇒ 恒为 64，与 canonical 的退出码表无关；
#     · ⇒ 而 `./scripts/deploy.sh plan 245` 这条**示例命令**照抄会直接
#       「No such file or directory」⇒ ⇒ 读者被文件头**主动引向一个不存在的
#       入口** ⇒ ★ 这正是仓内判词「**注释里的读数不会自己失效** ⇒ 它会变成
#       一句**指向相反结论的可信假话**」的活标本。
#
#   ★ 本轮处置：**保留 fail-closed 壳**（它有真实价值：把「为什么失败、现在
#     该敲哪个」讲清楚，避免运维在事故现场只看到一句无信息量的报错），
#     ★★ **但把文件头改成现状陈述** —— 不再承诺任何转发，不再给已删入口的示例。
#     ★ 错误提示正文（下方 `if` 块）**未改**：它列出的 8 个现役脚本已逐一
#       实测存在且可执行（2026-11-09）。
#
#   ★★ **刻意不把它转发到 scripts/deploy-{154,245,252-gateway,seamless}.sh
#     中的任意一个**（★ 规划文档 §5 第 0 批第 3 项曾建议「指向
#     scripts/deploy-seamless-docker.sh 或直接删除该 wrapper」）—— ★★ **该建议
#     已被同类壳 deploy/rollback.sh 在 2026-10-14（C-P2-2）否决过一次**，理由
#     同样适用于本文件：★ 各目标的部署/回滚契约**各不相同**（245 走
#     releases/VERSION/current 符号链；154 只给 runbook 指引；252 deferred、
#     186 retired 都拒绝；211 走 docker 蓝绿）⇒ ★★ **猜一个转发等于在事故现场
#     静默换掉语义** —— ★ 比明确失败危险得多。
#     ⇒ ⇒ 故本文件维持 fail-closed，与 deploy/rollback.sh 保持同一形状。
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
    echo "    docker 211   → scripts/deploy-211.sh（薄外壳 → deploy-seamless-docker.sh）"
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
#!/usr/bin/env bash
# scripts/lib/node-pm.sh
# 前端包管理器单一真源。pnpm 是官方/CI/发布主路径，npm 并行受支持。
# Usage: source "$(dirname "${BASH_SOURCE[0]}")/lib/node-pm.sh"
#
# 为什么需要它（2026-10-02）：
# 仓库里五处各自实现包管理器选择，彼此矛盾 ——
#   verify-ci.yml / verify.sh            pnpm --frozen-lockfile
#   deploy-local-sys.sh                  pnpm install --frozen-lockfile || npm install
#   deploy-kaixuan1.sh / deploy-seamless.sh   npm ci
#   quick-start.sh                       pnpm 否则 npm（但提示语永远写 pnpm）
# 最糟的是 deploy-local-sys.sh 的 `||` 回退：pnpm 因 lockfile 与 package.json
# 不同步而**真实失败**时，会静默降级成不带 --frozen-lockfile 的 npm install，
# 把"该重新生成 lockfile"这个真错误吞成一个看似成功的安装。
#
# 本模块的契约：**选定一个，跑它，原样透传退出码**。不做跨包管理器回退。
# 跨管理器回退会把"这个包管理器装不出正确依赖树"伪装成成功，而那恰恰是
# 双 lockfile 最需要被看见的失败。
#
# 覆盖方式：WEB_PM=pnpm 或 WEB_PM=npm 强制指定。
#
# 兼容性：不使用 mapfile / ${var,,} / readarray —— deploy-kaixuan1.sh 部署到
# darwin/arm64，macOS 自带 /bin/bash 是 3.2。

# 解析并缓存包管理器。输出 pnpm 或 npm。
pm_resolve() {
    if [ -n "${_PM_RESOLVED:-}" ]; then
        printf '%s\n' "$_PM_RESOLVED"
        return 0
    fi
    local want="${WEB_PM:-}"
    if [ -n "$want" ]; then
        case "$want" in
            pnpm|npm) ;;
            *)
                echo "[node-pm] WEB_PM 只能是 pnpm 或 npm，收到 '$want'" >&2
                return 1
                ;;
        esac
        command -v "$want" >/dev/null 2>&1 || {
            echo "[node-pm] WEB_PM=$want 但该命令不在 PATH 上" >&2
            return 1
        }
        _PM_RESOLVED="$want"
        printf '%s\n' "$want"
        return 0
    fi
    # 官方顺序：pnpm 优先。没有 pnpm 就用 npm —— Windows 开发机与未装 pnpm 的
    # 客户机走这条路，两条路都被 scripts/check-frontend-lockfiles.sh 守住。
    if command -v pnpm >/dev/null 2>&1; then
        _PM_RESOLVED=pnpm
    elif command -v npm >/dev/null 2>&1; then
        _PM_RESOLVED=npm
    else
        echo "[node-pm] pnpm 与 npm 都不在 PATH 上；装 Node.js 20+ 后重试" >&2
        return 1
    fi
    printf '%s\n' "$_PM_RESOLVED"
}

# pm_install [dir] — 按各自 lockfile 的正确方式安装依赖。
# pnpm 必须 --frozen-lockfile；npm 在有 package-lock.json 时用 ci（等价于冻结，
# 且会直接拒绝 package.json 与 lock 不同步的情况）。
pm_install() {
    local dir="${1:-${WEB_DIR:-web}}"
    local pm
    pm="$(pm_resolve)" || return 1
    [ -f "$dir/package.json" ] || {
        echo "[node-pm] $dir/package.json 不存在" >&2
        return 1
    }
    echo "[node-pm] 安装前端依赖（$pm，目录 $dir）"
    case "$pm" in
        pnpm)
            (cd "$dir" && pnpm install --frozen-lockfile)
            ;;
        npm)
            if [ -f "$dir/package-lock.json" ]; then
                (cd "$dir" && npm ci)
            else
                echo "[node-pm] 没有 package-lock.json，退回 npm install（非冻结）" >&2
                (cd "$dir" && npm install)
            fi
            ;;
    esac
}

# pm_run <script> [dir] [args...] — 用选定的包管理器跑一个 package.json script。
pm_run() {
    local script="$1"; shift
    local dir="${1:-${WEB_DIR:-web}}"
    [ $# -gt 0 ] && shift
    local pm
    pm="$(pm_resolve)" || return 1
    case "$pm" in
        pnpm) (cd "$dir" && pnpm run "$script" "$@") ;;
        npm)  (cd "$dir" && npm  run "$script" "$@") ;;
    esac
}

# pm_hint <script> — 打印给人看的调用提示，跟实际选中的包管理器一致。
# quick-start.sh 过去无论用哪个管理器装完都提示 "cd web && pnpm dev"，
# 用 npm 的用户照做直接 command not found。
pm_hint() {
    local script="$1"
    local dir="${2:-web}"
    local pm
    pm="$(pm_resolve)" || return 1
    echo "  cd $dir && $pm run $script"
}

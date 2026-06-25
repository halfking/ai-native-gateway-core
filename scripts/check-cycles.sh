#!/bin/bash
# scripts/check-cycles.sh
# 用途: 检测 Go 模块内的循环依赖（domain-driven refactor 的关键 CI 门）
# 退出码: 0 = 无循环依赖；1 = 检测到循环依赖
# 兼容性: macOS BSD grep / bash 3.2+
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$ROOT_DIR"

echo "=== 循环依赖检测 ==="
echo "工作目录: $ROOT_DIR"

# 仅检查 ./domains/ 和 ./eventbus/ 下的新包（不污染旧代码）
DOMAIN_PKGS=$(go list ./domains/... ./eventbus/... 2>/dev/null | sort -u)

if [[ -z "$DOMAIN_PKGS" ]]; then
    echo "[WARN] 未发现 ./domains/ 或 ./eventbus/ 包，跳过"
    exit 0
fi

PKG_COUNT=$(echo "$DOMAIN_PKGS" | wc -l | tr -d ' ')
echo "检查包数: $PKG_COUNT"
echo ""

# 对每个包，检查其直接 import 中是否存在同模块内更"深"包，且该包传递依赖回到自身
CYCLE_FOUND=0
for pkg in $DOMAIN_PKGS; do
    # 获取该包的所有直接 import
    imports=$(go list -f '{{range .Imports}}{{.}}\n{{end}}' "$pkg" 2>/dev/null || echo "")

    if [[ -z "$imports" ]]; then
        continue
    fi

    for imp in $imports; do
        # 跳过标准库和外部包
        if [[ "$imp" != github.com/kaixuan/llm-gateway-go/* ]]; then
            continue
        fi

        # 跳过自身
        if [[ "$imp" == "$pkg" ]]; then
            continue
        fi

        # 完整循环检测：检查 imp 的传递依赖中是否包含 pkg
        if go list -deps "$imp" 2>/dev/null | grep -qx "$pkg"; then
            echo "[ERROR] 检测到循环依赖:"
            echo "   $pkg"
            echo "   -> 依赖 $imp"
            echo "   -> $imp 的传递依赖回到 $pkg"
            CYCLE_FOUND=1
        fi
    done
done

if [[ $CYCLE_FOUND -eq 0 ]]; then
    echo "[OK] 无循环依赖"
    exit 0
else
    echo ""
    echo "请重新设计领域边界，参考 docs/architecture/domain-refactoring-plan.md"
    exit 1
fi

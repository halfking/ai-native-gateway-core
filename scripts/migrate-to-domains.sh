#!/bin/bash
# scripts/migrate-to-domains.sh
# 用途: 自动化迁移旧包到 domains/ 子目录（Phase 0 准备工作）
# 兼容性: macOS BSD sed (sed -i ''), bash 3.2+
# 警告: 这是迁移工具，不会修改现有代码。运行后会创建 domains/ 子目录
#       并把旧包内容复制过去，同时更新 import 路径。
set -euo pipefail

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BACKUP_DIR="$ROOT_DIR/_deprecated/old-$(date +%Y%m%d-%H%M%S)"
DRY_RUN=false

# 用法
usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

OPTIONS:
    --dry-run       Show what would be done without making changes
    --help          Show this help

Migrates legacy packages into domains/ subdirectories with updated import paths.

Migration Map:
    identity/     -> domains/identity/
    auth/         -> domains/authentication/
    provider/     -> domains/provider/
    sessions/     -> domains/session/
    routing/      -> domains/routing/
    credentials/  -> domains/credential/    (if exists)
    transform/    -> domains/transformation/ (if exists)
    streaming/    -> domains/streaming/    (if exists)

Backs up original files to: $BACKUP_DIR
EOF
}

# 参数解析
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}" >&2
            usage
            exit 1
            ;;
    esac
done

# 迁移函数
migrate_package() {
    local src="$1"
    local dst="$2"
    local old_import="$3"
    local new_import="$4"

    if [[ ! -d "$ROOT_DIR/$src" ]]; then
        echo -e "${YELLOW}[SKIP] $src (not found)${NC}"
        return 0
    fi

    if [[ -d "$ROOT_DIR/$dst" ]]; then
        echo -e "${YELLOW}[SKIP] $dst (already exists)${NC}"
        return 0
    fi

    echo -e "${GREEN}[MIGRATE] $src -> $dst${NC}"

    if [[ "$DRY_RUN" == "false" ]]; then
        mkdir -p "$ROOT_DIR/$(dirname "$dst")"
        mkdir -p "$BACKUP_DIR/$(dirname "$src")"
        cp -R "$ROOT_DIR/$src" "$BACKUP_DIR/$src"
        cp -R "$ROOT_DIR/$src" "$ROOT_DIR/$dst"
        # macOS BSD sed: 必须有 '' 参数
        find "$ROOT_DIR/$dst" -name "*.go" -type f -exec sed -i '' \
            "s|\"${old_import}|\"${new_import}|g" {} \;
    fi
}

# 主流程
main() {
    echo -e "${GREEN}=== llm-gateway-go 领域迁移工具 ===${NC}"
    echo "工作目录: $ROOT_DIR"

    if [[ "$DRY_RUN" == "true" ]]; then
        echo -e "${YELLOW}DRY RUN 模式（不会修改文件）${NC}"
    fi

    # 备份
    if [[ "$DRY_RUN" == "false" ]]; then
        mkdir -p "$BACKUP_DIR"
        echo "备份目录: $BACKUP_DIR"
    fi

    echo ""
    echo "--- 开始迁移 ---"

    # 迁移包列表
    migrate_package "identity" "domains/identity" \
        "github.com/kaixuan/llm-gateway-go/identity" \
        "github.com/kaixuan/llm-gateway-go/domains/identity"

    migrate_package "auth" "domains/authentication" \
        "github.com/kaixuan/llm-gateway-go/auth" \
        "github.com/kaixuan/llm-gateway-go/domains/authentication"

    migrate_package "provider" "domains/provider" \
        "github.com/kaixuan/llm-gateway-go/provider" \
        "github.com/kaixuan/llm-gateway-go/domains/provider"

    migrate_package "sessions" "domains/session" \
        "github.com/kaixuan/llm-gateway-go/sessions" \
        "github.com/kaixuan/llm-gateway-go/domains/session"

    migrate_package "routing" "domains/routing" \
        "github.com/kaixuan/llm-gateway-go/routing" \
        "github.com/kaixuan/llm-gateway-go/domains/routing"

    # 可选包（仅当源目录存在时迁移）
    migrate_package "credentials" "domains/credential" \
        "github.com/kaixuan/llm-gateway-go/credentials" \
        "github.com/kaixuan/llm-gateway-go/domains/credential"

    migrate_package "transform" "domains/transformation" \
        "github.com/kaixuan/llm-gateway-go/transform" \
        "github.com/kaixuan/llm-gateway-go/domains/transformation"

    migrate_package "streaming" "domains/streaming" \
        "github.com/kaixuan/llm-gateway-go/streaming" \
        "github.com/kaixuan/llm-gateway-go/domains/streaming"

    echo ""
    echo -e "${GREEN}=== 迁移完成 ===${NC}"
    echo ""
    echo "下一步:"
    echo "  1. 验证编译: go build ./domains/..."
    echo "  2. 跑测试:   go test ./domains/... -v -cover"
    echo "  3. 检测循环: ./scripts/check-cycles.sh"
}

main "$@"

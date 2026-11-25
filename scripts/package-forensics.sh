#!/usr/bin/env bash
# W4.4: 取证组件独立交付打包脚本。
#
# 产出 llm-gateway-forensics-<version>.tar.gz，内含：
#   bin/sessionforensics      构建机平台的二进制（CGO_ENABLED=0；在哪个平台打包就只能在哪个平台运行）
#   config/forensics.env.example  环境变量模板（唯一配置面）
#   README.md                 部署与使用手册
#
# 用法：./scripts/package-forensics.sh [--out DIR | --out=DIR]
#   OUT 目录缺省为 <仓根>/dist；相对路径按【调用时的当前目录】解析（不是仓根）。
# 版本号默认取 git describe --tags（无 tag 时回退 git 短 SHA）。
#
# ★ 2026-11-25 本会话系列第 29 轮：`--out DIR`（空格分隔）形态此前直接坏掉 ——
#   旧解析只认 `--out=DIR`；空格形态下 OUT_DIR 取到字面量 `--out`，紧接着
#   `rm -rf "--out/<包名>"` 报 illegal option（rc=64）、set -e 当场退出。而本脚本
#   头注释写的与 CI 作业 package-forensics 用的恰恰都是空格形态。
set -euo pipefail

# ★ 专用构建缓存（门 scripts/ursmcheck/gocache_script_family_guard_test.go 判据①）：
# 本脚本在宿主上 go build ⇒ 子进程会往用户全局构建缓存写产物 ⇒ 必须指向专用缓存
# （目录约定与上限见 scripts/lib/gocache.sh；失败安全：找不到共享入口就不设）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$REPO_ROOT/dist"
usage() {
    # 不用管道取值：pipefail 台账把「命令替换里的管道」逐条登记，这里没必要新增候选。
    sed -n '/^# 用法：/,/^# 版本号/s/^# //p' "${BASH_SOURCE[0]}"
}
while [ $# -gt 0 ]; do
    case "$1" in
        --out=*)
            OUT_DIR="${1#--out=}"
            shift
            ;;
        --out)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "package-forensics: --out 后面要跟一个目录（--out DIR 或 --out=DIR）" >&2
                exit 64
            fi
            OUT_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "package-forensics: 未知参数：$1（用法：--out DIR 或 --out=DIR）" >&2
            exit 64
            ;;
    esac
done
if [ -z "$OUT_DIR" ] || [ "$OUT_DIR" = "/" ]; then
    echo "package-forensics: 输出目录不能为空、也不能是根目录（实得：${OUT_DIR}）" >&2
    exit 64
fi
# 相对路径按调用时的当前目录解析（下面的 cd "$REPO_ROOT" 之后就不一样了）。
case "$OUT_DIR" in
    /*) ;;
    *) OUT_DIR="$PWD/$OUT_DIR" ;;
esac

cd "$REPO_ROOT"

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
PKG_NAME="llm-gateway-forensics-${VERSION}"
STAGE="$OUT_DIR/$PKG_NAME"
# 交付包 README 里的构建前提取自 go.mod（★ 不手写版本号 ⇒ 不会与 go.mod 脱节）。
GO_MIN="$(awk '$1 == "go" { print $2; exit }' "$REPO_ROOT/go.mod")"
BUILD_PLATFORM="$(uname -s)/$(uname -m)"

echo "==> packaging $PKG_NAME"

rm -rf "$STAGE"
mkdir -p "$STAGE/bin" "$STAGE/config"

# 1. 二进制（CGO_ENABLED=0：Linux 上静态链接、不依赖宿主 libc；只在构建机平台上可运行）
echo "==> building bin/sessionforensics (CGO_ENABLED=0)"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o "$STAGE/bin/sessionforensics" ./cmd/sessionforensics

# 2. 配置模板 —— 与 cmd/sessionforensics/main.go 头注释的环境变量严格一致
cat > "$STAGE/config/forensics.env.example" <<'EOF'
# sessionforensics 配置（复制为 forensics.env 后 source 使用）
# admin 网关地址（--from/--to 的默认值）
SESSION_FORENSICS_BASE_URL=https://admin.example.com
# admin JWT（Authorization: Bearer ...）
SESSION_FORENSICS_BEARER=
EOF

# 3. 使用手册
{
    cat <<'EOF'
# llm-gateway Forensics 组件

会话取证独立交付包（W4.4）：生产环境无侵入下载、回放、摘要会话。

## 平台
本包的二进制是在【打包机】的平台/架构上构建的，只能在同平台/架构上运行；
换平台请在目标平台重新执行 `scripts/package-forensics.sh`。
- 本包构建于：
EOF
    echo "  ${BUILD_PLATFORM}"
    cat <<'EOF'

## 依赖
EOF
    echo "- Go ${GO_MIN}（取自仓库 go.mod 的 go 指令；只有打包机需要，运行时零依赖）"
    cat <<'EOF'
- 目标网关 admin 面可达（具备会话读取权限的 JWT）

## 安装
1. 解压本包到任意目录
2. `cp config/forensics.env.example config/forensics.env` 并填入 BASE_URL 与 BEARER
3. `source config/forensics.env`

## 使用
    ./bin/sessionforensics list     --limit=20
    ./bin/sessionforensics download --id=gw_xxx --out=sessions/
    ./bin/sessionforensics replay   --in=sessions/gw_xxx.json
    ./bin/sessionforensics summarize --in=sessions/gw_xxx.json
    ./bin/sessionforensics migrate  --id=gw_xxx --from=https://A --to=https://B

## 验证安装
    ./bin/sessionforensics help
EOF
} > "$STAGE/README.md"

# 4. 打包
mkdir -p "$OUT_DIR"
( cd "$OUT_DIR" && tar czf "$PKG_NAME.tar.gz" "$PKG_NAME" )

echo "==> done: $OUT_DIR/$PKG_NAME.tar.gz"
tar tzf "$OUT_DIR/$PKG_NAME.tar.gz"

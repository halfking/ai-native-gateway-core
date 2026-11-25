#!/usr/bin/env bash
# W4.4: 取证组件独立交付打包脚本。
#
# 产出 llm-gateway-forensics-<version>.tar.gz，内含：
#   bin/sessionforensics      跨平台静态二进制（CGO_ENABLED=0）
#   config/forensics.env.example  环境变量模板（唯一配置面）
#   README.md                 部署与使用手册
#
# 用法：./scripts/package-forensics.sh [--out DIR]
# 版本号默认取 git describe --tags（无 tag 时回退 git 短 SHA）。
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
OUT_DIR="${1:-$REPO_ROOT/dist}"
OUT_DIR="${OUT_DIR#--out=}"

cd "$REPO_ROOT"

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
PKG_NAME="llm-gateway-forensics-${VERSION}"
STAGE="$OUT_DIR/$PKG_NAME"

echo "==> packaging $PKG_NAME"

rm -rf "$STAGE"
mkdir -p "$STAGE/bin" "$STAGE/config"

# 1. 静态二进制（不依赖宿主 libc，便于跨机交付）
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
cat > "$STAGE/README.md" <<'EOF'
# llm-gateway Forensics 组件

会话取证独立交付包（W4.4）：生产环境无侵入下载、回放、摘要会话。

## 依赖
- Go 1.21+（仅构建机需要；运行时零依赖）
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

# 4. 打包
mkdir -p "$OUT_DIR"
( cd "$OUT_DIR" && tar czf "$PKG_NAME.tar.gz" "$PKG_NAME" )

echo "==> done: $OUT_DIR/$PKG_NAME.tar.gz"
tar tzf "$OUT_DIR/$PKG_NAME.tar.gz"

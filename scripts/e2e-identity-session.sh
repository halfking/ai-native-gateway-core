#!/bin/bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# scripts/e2e-identity-session.sh
# 用途: E2E 验证 identity + session + authentication 领域集成
# 调用方式: ./scripts/e2e-identity-session.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$ROOT_DIR"

echo "=== [1/5] Build domains ==="
go build ./domains/identity/... ./domains/session/... ./domains/authentication/... ./domains/integration/...

echo "=== [2/5] Unit-test with coverage (fresh) ==="
go test ./domains/identity/... -count=1 -coverprofile=cover-identity.out
go test ./domains/session/... -count=1 -coverprofile=cover-session.out
go test ./domains/authentication/... -count=1 -coverprofile=cover-auth.out
go test ./domains/integration/... -count=1 -coverprofile=cover-integration.out

echo "=== [3/5] Coverage summary ==="
for f in cover-identity cover-session cover-auth cover-integration; do
    echo -n "$f: "
    go tool cover -func="${f}.out" | tail -1
done

echo "=== [4/5] go vet ==="
go vet ./domains/identity/... ./domains/session/... ./domains/authentication/... ./domains/integration/...

echo "=== [5/5] Circular-dep check ==="
./scripts/check-cycles.sh

# 清理
rm -f cover-identity.out cover-session.out cover-auth.out cover-integration.out

echo "=== Done. All checks passed. ==="

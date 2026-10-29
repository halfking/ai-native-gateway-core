#!/usr/bin/env bash
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
# Generate a Fernet ciphertext for the test secret.
# Usage: _fpslot-make-cipher.sh <base64-32byte-key> <plaintext>
# Stdout: hex string (no 0x prefix, no newline).
#
# Self-contained helper for verify-fpslot-fix.sh. Wraps an inline Go program
# so the verify script doesn't depend on /tmp/fpslot-make-cipher.go (which
# would need a writable /tmp AND a stable $GOPATH, neither of which we assume).

set -euo pipefail

KEY="$1"
PLAIN="$2"

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_DIR="/tmp/_fpslot-make-cipher-build-$$"
mkdir -p "$BUILD_DIR"
trap 'rm -rf "$BUILD_DIR"' EXIT

cat > "$BUILD_DIR/main.go" <<'GOEOF'
package main

import (
	"fmt"
	"os"

	"github.com/kaixuan/llm-gateway-go/secret"
)

func main() {
	key, err := secret.FernetKeyFromSecret("", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "key err:", err)
		os.Exit(2)
	}
	ct, err := secret.EncryptFernet([]byte(os.Args[2]), key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encrypt err:", err)
		os.Exit(3)
	}
	for _, b := range ct {
		fmt.Printf("%02x", b)
	}
}
GOEOF

(cd "$ROOT_DIR" && go run "$BUILD_DIR/main.go" "$KEY" "$PLAIN")

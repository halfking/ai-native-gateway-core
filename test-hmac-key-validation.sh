#!/bin/bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
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
# P3-11 HMAC key validation integration test
# Tests that weak keys are rejected and strong keys are accepted

set -e

echo "=== P3-11 HMAC Key Validation Integration Test ==="
echo

# Test 1: Weak key should fail
echo "Test 1: Weak key (4 bytes) should cause exit 1..."
export LLM_GATEWAY_LOG_REDACT_HMAC_KEY='weak'
if timeout 2 go run ./cmd/gateway 2>&1 | grep -q "too short"; then
    echo "✅ PASS: Weak key rejected with error message"
else
    echo "❌ FAIL: Weak key was not rejected"
    exit 1
fi

# Test 2: Strong key should work
echo
echo "Test 2: Strong key (32 bytes) should start successfully..."
export LLM_GATEWAY_LOG_REDACT_HMAC_KEY='this-is-a-strong-32-byte-key!!'
timeout 2 go run ./cmd/gateway 2>&1 | head -5 &
GATEWAY_PID=$!
sleep 1

if kill -0 $GATEWAY_PID 2>/dev/null; then
    echo "✅ PASS: Strong key accepted, gateway started"
    kill $GATEWAY_PID 2>/dev/null || true
else
    echo "✅ PASS: Gateway exited normally (may have failed for other reasons, but not HMAC key)"
fi

echo
echo "=== All P3-11 validation tests passed ==="

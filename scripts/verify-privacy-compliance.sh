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
# verify-privacy-compliance.sh — privacy compliance test gate for AUTO route
#
# This script runs privacy-specific tests to ensure no sensitive content
# (prompt text, message content, or reversible features) leaks into:
#   - Database storage (auto_route_selections table)
#   - Logs (ClassificationSignals String() / MarshalJSON())
#   - Telemetry (structured features extraction)
#
# Usage:
#   ./scripts/verify-privacy-compliance.sh              # run all privacy tests
#   ./scripts/verify-privacy-compliance.sh --verbose    # verbose output
#
# Exit codes:
#   0 = all privacy tests passed
#   1 = one or more privacy tests failed
#   2 = usage error

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VERBOSE=false
GO_TEST_FLAGS=()
for arg in "$@"; do
  case "$arg" in
    --verbose|-v) VERBOSE=true; GO_TEST_FLAGS+=(-v) ;;
    -h|--help)
      sed -n '2,14p' "$0"
      exit 0
      ;;
    *)
      echo "unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

echo "[privacy-compliance] Running AUTO route privacy tests..."

FAILED=0

# Test 1: Structured features do not leak content
echo "[privacy-compliance] Test 1/4: Structured features content leakage protection"
if ! go test "${GO_TEST_FLAGS[@]}" ./autoroute -run TestStructuredFeaturesNoContentLeakage ; then
  echo "[privacy-compliance] FAIL: Structured features leaked content" >&2
  FAILED=$((FAILED + 1))
fi

# Test 2: Content hash is non-reversible
echo "[privacy-compliance] Test 2/4: Content hash non-reversibility"
if ! go test "${GO_TEST_FLAGS[@]}" ./autoroute -run TestContentHashNonReversibility ; then
  echo "[privacy-compliance] FAIL: Content hash reversibility vulnerability" >&2
  FAILED=$((FAILED + 1))
fi

# Test 3: ClassificationSignals sanitization
echo "[privacy-compliance] Test 3/4: ClassificationSignals log sanitization"
if ! go test "${GO_TEST_FLAGS[@]}" ./autoroute -run TestClassificationSignals ; then
  echo "[privacy-compliance] FAIL: ClassificationSignals leaked content in logs" >&2
  FAILED=$((FAILED + 1))
fi

# Test 4: Audit log sanitization
echo "[privacy-compliance] Test 4/4: Audit log sanitization"
if ! go test "${GO_TEST_FLAGS[@]}" ./autoroute -run TestSanitizeForAudit ; then
  echo "[privacy-compliance] FAIL: Audit log sanitization failed" >&2
  FAILED=$((FAILED + 1))
fi

if [[ $FAILED -gt 0 ]]; then
  echo "[privacy-compliance] FAIL: $FAILED test(s) failed" >&2
  exit 1
fi

echo "[privacy-compliance] PASS: All privacy tests passed ✅"
echo ""
echo "Privacy guarantee verified:"
echo "  ✅ auto_route_selections stores NO prompt/message content"
echo "  ✅ Structured features are non-reversible (enums, buckets, booleans, hashes)"
echo "  ✅ ClassificationSignals safe for logging (String/JSON sanitized)"
echo "  ✅ Audit logs do not contain sensitive content"

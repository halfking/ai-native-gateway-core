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
# ---------------------------------------------------------------------------
# verify-g1-route-contract.sh — 变异验证：证明 G-1 的跨仓契约门**不是恒绿**
#
# WHAT THIS IS
# ------------
# G-1 的判据是「契约测试断言客户端使用的路径在 memora 路由表内（**跨仓契约清单，
# 双向核对**）」。此前「双向核对」是**人工**做的：网关侧手抄了一份 memora 的路由表
# （注释），门里又硬编码了一份期望值 ⇒ memora 改路由时**两边都不会红**。
#
# 本脚本给这套契约装上**四条可复核的牙**：
#
#   M1  网关客户端的默认 add 路径改回 MemOS 旧路径（/product/add）
#       ⇒ 网关的「路径 ⊆ 清单」门必须红
#   M2  vendored 副本里删掉 /api/memory/add 这一条
#       ⇒ 网关的「路径 ⊆ 清单」门必须红（清单与客户端必须互相咬住）
#   M3  memora 的路由注册里删掉 group.POST("/memory/add", h.Add)
#       ⇒ memora 的「清单 == 真实路由表」门必须红
#   M4  把 vendored 副本改得与 memora 的原副本不同
#       ⇒ 网关的「两份副本逐字一致」门必须红（这才是**双向核对**那一半）
#
# 两条自我约束（沿用 verify-b4-clustering.sh 的教训）：
#
#   1. **编译红不算数**："build failed" 不是 "assertion went red"。
#   2. **变红的必须是预期集合里的测试**：预期外的红说明门红在了别处。
#
# 每个被改的文件都会被还原，并按 **md5 逐字校验**（还原不一致 = 脚本失败）。
#
# USAGE
#   bash scripts/audit/verify-g1-route-contract.sh
#   MEMORA_REPO_PATH=/path/to/memora bash scripts/audit/verify-g1-route-contract.sh
#
# Requires: go on PATH（~/sdk/go/bin 会被自动补上）。
# ---------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

if ! command -v go >/dev/null 2>&1; then
  if [ -x "$HOME/sdk/go/bin/go" ]; then
    export PATH="$HOME/sdk/go/bin:$PATH"
  else
    echo "FAIL: go not found on PATH" >&2
    exit 2
  fi
fi

# memora 的 checkout：环境变量优先，否则用兄弟目录。
MEMORA_ROOT="${MEMORA_REPO_PATH:-$(cd "$REPO_ROOT/../.." && pwd)/memora}"
if [ ! -f "$MEMORA_ROOT/contracts/routes.json" ]; then
  echo "FAIL: 找不到 memora 的 checkout（$MEMORA_ROOT/contracts/routes.json 不存在）" >&2
  echo "      设 MEMORA_REPO_PATH 指向 memora 仓后重跑。" >&2
  exit 2
fi

WORK_DIR="$(mktemp -d)"
BACKUP_DIR="$WORK_DIR/backup"
mkdir -p "$BACKUP_DIR"

MUTATIONS_RUN=0
MUTATIONS_RED=0
FAILED=()

md5_of() {
  if command -v md5sum >/dev/null 2>&1; then
    md5sum "$1" | awk '{print $1}'
  else
    md5 -q "$1"
  fi
}

b64() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

backup() {
  local file="$1"
  mkdir -p "$BACKUP_DIR"
  local key
  key="$(printf '%s' "$file" | tr '/' '_')"
  cp "$file" "$BACKUP_DIR/$key"
  printf '%s\t%s\n' "$key" "$file" >> "$BACKUP_DIR/index"
}

restore() {
  local file="$1"
  local key
  key="$(printf '%s' "$file" | tr '/' '_')"
  cp "$BACKUP_DIR/$key" "$file"
  local before after
  before="$(md5_of "$BACKUP_DIR/$key")"
  after="$(md5_of "$file")"
  if [ "$before" != "$after" ]; then
    echo "   ✘ RESTORE MISMATCH for $file ($before != $after)" >&2
    FAILED+=("restore:$file")
    return 1
  fi
  echo "   ↩ restored $file (md5 $after)"
  return 0
}

mutate() {
  local file="$1" old_b64="$2" new_b64="$3"
  python3 - "$file" "$old_b64" "$new_b64" <<'PY'
import base64, sys
path, old_b64, new_b64 = sys.argv[1:4]
old = base64.b64decode(old_b64).decode("utf-8")
new = base64.b64decode(new_b64).decode("utf-8")
with open(path, "r", encoding="utf-8") as handle:
    content = handle.read()
count = content.count(old)
if count != 1:
    sys.exit("anchor found %d times in %s, want exactly 1" % (count, path))
with open(path, "w", encoding="utf-8") as handle:
    handle.write(content.replace(old, new, 1))
print("   mutation applied to %s" % path)
PY
}

expect_red() {
  local label="$1" pattern="$2" cwd="${3:-$REPO_ROOT}" pkg="${4:-./domains/memory/client/...}"
  MUTATIONS_RUN=$((MUTATIONS_RUN + 1))
  echo "── MUTATION $label: expect RED"
  local output
  output="$(cd "$cwd" && go test "$pkg" -run "$pattern" -count=1 2>&1)"
  if grep -qE '\[build failed\]|^# |cannot use|undefined:' <<<"$output"; then
    echo "   ✘ BUILD FAILED — the mutation does not compile; not a valid mutation"
    echo "$output" | head -10
    FAILED+=("$label(build)")
    return
  fi
  local failed unexpected
  failed="$(grep -oE '^--- FAIL: [A-Za-z0-9_]+' <<<"$output" | sed 's/^--- FAIL: //' | sort -u)"
  if [ -z "$failed" ]; then
    echo "   ✘ STILL GREEN — the gate does not have teeth"
    echo "$output" | tail -15
    FAILED+=("$label(still-green)")
    return
  fi
  unexpected="$(echo "$failed" | grep -vE "^($pattern)$" || true)"
  if [ -n "$unexpected" ]; then
    echo "   ✘ UNEXPECTED failures (the intended assertion may still hold): $unexpected"
    FAILED+=("$label(unexpected)")
    return
  fi
  echo "   ✔ RED as expected: $(echo $failed | tr '\n' ' ')"
  MUTATIONS_RED=$((MUTATIONS_RED + 1))
}

expect_green() {
  local label="$1" pattern="$2" cwd="${3:-$REPO_ROOT}" pkg="${4:-./domains/memory/client/...}"
  echo "── BASELINE $label: expect GREEN"
  local output
  output="$(cd "$cwd" && go test "$pkg" -run "$pattern" -count=1 2>&1)"
  if grep -qE '^--- FAIL:' <<<"$output"; then
    echo "   ✘ RED at baseline:"
    echo "$output" | tail -20
    FAILED+=("$label(baseline-red)")
  else
    echo "   ✔ GREEN as expected"
  fi
}

cleanup() {
  if [ -f "$BACKUP_DIR/index" ]; then
    while IFS= read -r line; do
      key="${line%%	*}"
      file="${line#*	}"
      [ -f "$BACKUP_DIR/$key" ] && cp "$BACKUP_DIR/$key" "$file"
    done < "$BACKUP_DIR/index"
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

CLIENT="domains/memory/client/client.go"
VENDORED="contracts/memora-routes.json"
MEMORA_HANDLER="$MEMORA_ROOT/internal/memory/handler.go"

echo "=== G-1 跨仓契约清单：变异验证 ==="
echo "repo:     $REPO_ROOT"
echo "memora:   $MEMORA_ROOT"
echo

expect_green "baseline-gateway-contract" \
  'TestClientDefaultRoutesAreInMemoraRouteManifest|TestMemOSLegacyPathsAreNotInMemoraRouteManifest|TestMemoraRouteManifestCopyIsInSyncWithMemoraCheckout'
expect_green "baseline-memora-manifest" \
  'TestRouteManifestMatchesLiveRoutes|TestRouteManifestIsNotEmpty' \
  "$MEMORA_ROOT" './internal/memory/...'

# ---------------------------------------------------------------------------
# M1 — 客户端默认 add 路径改回 MemOS 旧路径。
# ---------------------------------------------------------------------------
backup "$CLIENT"
mutate "$CLIENT" "$(b64 'DefaultAddPath = "/api/memory/add"')" "$(b64 'DefaultAddPath = "/product/add"')"
expect_red "M1-client-default-path-reverts-to-memos" \
  'TestClientDefaultRoutesAreInMemoraRouteManifest|TestMemOSLegacyPathsAreNotInMemoraRouteManifest'
restore "$CLIENT"

# ---------------------------------------------------------------------------
# M2 — vendored 副本里删掉 /api/memory/add 这一条。
# ---------------------------------------------------------------------------
backup "$VENDORED"
mutate "$VENDORED" "$(b64 '    {
      "method": "POST",
      "path": "/api/memory/add"
    },
')" "$(b64 '')"
expect_red "M2-vendored-copy-loses-add-route" \
  'TestClientDefaultRoutesAreInMemoraRouteManifest'
restore "$VENDORED"

# ---------------------------------------------------------------------------
# M3 — memora 侧删掉一条路由注册 ⇒ memora 的「清单 == 真实路由表」门必须红。
# ---------------------------------------------------------------------------
backup "$MEMORA_HANDLER"
mutate "$MEMORA_HANDLER" "$(b64 '	group.POST("/memory/add", h.Add)')" "$(b64 '	// MUTATION: /api/memory/add removed')"
expect_red "M3-memora-route-registration-removed" \
  'TestRouteManifestMatchesLiveRoutes|TestRouteManifestIsNotEmpty' \
  "$MEMORA_ROOT" './internal/memory/...'
restore "$MEMORA_HANDLER"

# ---------------------------------------------------------------------------
# M4 — vendored 副本与 memora 原副本不同 ⇒ 「两份副本逐字一致」门必须红。
# ---------------------------------------------------------------------------
backup "$VENDORED"
mutate "$VENDORED" "$(b64 '"source": "internal/memory/handler.go:RegisterRoutes"')" "$(b64 '"source": "MUTATED"')"
expect_red "M4-vendored-copy-diverges-from-memora" \
  'TestMemoraRouteManifestCopyIsInSyncWithMemoraCheckout'
restore "$VENDORED"

echo
echo "=== summary ==="
echo "mutations: $MUTATIONS_RUN, turned red: $MUTATIONS_RED"
if [ "${#FAILED[@]}" -ne 0 ]; then
  echo "RESULT: FAIL — ${FAILED[*]}" >&2
  exit 1
fi
echo "RESULT: PASS — baseline green, every mutation turned its leg red, restore verified"
exit 0

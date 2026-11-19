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
# build-upgrade-package.sh — 构建离线升级包（符合 installer/internal/upgrader 契约）
#
# 用途：已有环境的版本升级（非首次部署）
# 输出：llm-gateway-go-{version}-upgrade.tar.gz
#
# Usage:
#   bash scripts/build-upgrade-package.sh [v2.4.6] [--out /path/to/output] [--build-seq 10]
#
# Env:
#   GOPROXY            default https://goproxy.cn,direct
#   DOCKER_IMAGE_TAG   default kx-llm-gateway-go:${VERSION}
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

VERSION_ARG="${1:-}"
OUT_DIR=""
BUILD_SEQ=0
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT_DIR="$2"; shift 2 ;;
    --build-seq) BUILD_SEQ="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [[ -z "$VERSION_ARG" ]]; then
  VERSION_ARG="v$(python3 -c "import json;print(json.load(open('version.json'))['git_tag'])")"
fi
VERSION="${VERSION_ARG#v}"
GIT_TAG="v${VERSION}"

if [[ -z "$OUT_DIR" ]]; then
  OUT_DIR="$ROOT/dist/upgrade"
fi
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "[upgrade-package] Building for version ${GIT_TAG} (build_seq=${BUILD_SEQ})"

# 1. 构建 Gateway 二进制（Linux amd64，升级包默认只构建主平台）
echo "[1/5] Building kx-gateway binary..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod \
  -ldflags "-X main.version=${GIT_TAG} -X main.buildSeq=${BUILD_SEQ}" \
  -o "$WORK/kx-gateway" ./cmd/gateway
chmod +x "$WORK/kx-gateway"

# 2. 导出 Docker 镜像
echo "[2/5] Exporting Docker image..."
IMAGE_TAG="${DOCKER_IMAGE_TAG:-kx-llm-gateway-go:${VERSION}}"
if ! docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  echo "  Building Docker image $IMAGE_TAG..."
  docker build -t "$IMAGE_TAG" .
fi
docker save "$IMAGE_TAG" -o "$WORK/images.tar"

# 3. 组装 startup migration bundle（**动态枚举**，不再手抄清单）。
#
# 历史教训：这里曾是一份手抄数组，最后更新停在 654 —— 此后新增的全部迁移
# （700~980+）都进不了离线升级包，走离线通道的存量库 schema 静默落后且无人
# 报错。枚举规则与内部升级通道对齐：
#   - 只收 sql/migrations/startup/ 下的 NNN_*.sql（排除 *.down.sql —— 回滚
#     脚本是发布产物，不是升级输入；与 installer/internal/upgrader 的
#     forwardMigrationFiles 同一契约）；
#   - version >= 412 地板（内部通道 DB_LEDGER_RECONCILE_FROM=412 同款）：
#     <412 的结构由 01-schema.sql 基线覆盖，且已知非幂等文件（315/364，
#     与基线互斥定义）都在地板之下，天然排除；
#   - 离线升级器（OfflineApplier）没有 checksum ledger，靠「每个迁移幂等」
#     的通道契约全量重放包内文件 —— 与 apply-db-revision-sequence.sh 同一
#     契约。
echo "[3/5] Assembling startup migration bundle (dynamic enumeration, floor=412)..."
mkdir -p "$WORK/migrations/startup"
startup_migrations=()
for f in sql/migrations/startup/[0-9]*.sql; do
  [[ -f "$f" ]] || continue
  base="$(basename "$f")"
  case "$base" in
    *.down.sql) continue ;;
  esac
  ver="${base%%_*}"
  if ! [[ "$ver" =~ ^[0-9]+$ ]]; then
    continue
  fi
  # 10# 强制十进制：前导零编号（008/018/...）会被 bash 按八进制解析，
  # "value too great for base" 报错时 -lt 判假 ⇒ 地板失效、老迁移漏进包。
  if [[ "$((10#$ver))" -lt 412 ]]; then
    continue
  fi
  startup_migrations+=("$base")
done
# 防 stagnation 门：清单非空、锚点必须在场（锚点失踪说明枚举路径/glob 失效，
# 而不是「迁移真的没了」）。
if [[ "${#startup_migrations[@]}" -lt 20 ]]; then
  echo "upgrade-package: enumerated only ${#startup_migrations[@]} startup migrations (expected far more) — refusing to build a hollow package" >&2
  exit 1
fi
need_anchor="654_archive_credential_model_index_detach_drop.sql"
anchor_found=0
for name in "${startup_migrations[@]}"; do
  [[ "$name" == "$need_anchor" ]] && { anchor_found=1; break; }
done
if [[ "$anchor_found" != "1" ]]; then
  echo "upgrade-package: floor anchor $need_anchor missing from enumeration" >&2
  exit 1
fi
echo "  bundled ${#startup_migrations[@]} startup migrations (floor 412 → latest)"
for name in "${startup_migrations[@]}"; do
  source="sql/migrations/startup/$name"
  [[ -f "$source" ]] || { echo "missing required startup migration: $source" >&2; exit 1; }
  cp "$source" "$WORK/migrations/startup/$name"
done

# 4. 生成 manifest.json
echo "[4/5] Generating manifest.json..."
FILES=()
for file in kx-gateway images.tar; do
  if [[ -e "$WORK/$file" ]]; then
    sha=$(shasum -a 256 "$WORK/$file" | awk '{print $1}')
    FILES+=("{\"path\":\"$file\",\"sha256\":\"$sha\"}")
  fi
done
while IFS= read -r file; do
  relative="${file#$WORK/}"
  sha=$(shasum -a 256 "$file" | awk '{print $1}')
  FILES+=("{\"path\":\"$relative\",\"sha256\":\"$sha\"}")
done < <(find "$WORK/migrations" -type f -name '*.sql' -print | LC_ALL=C sort)

IFS=,
cat > "$WORK/manifest.json" <<JSON
{
  "version": "${GIT_TAG}",
  "build_seq": ${BUILD_SEQ},
  "files": [
    ${FILES[*]}
  ]
}
JSON

# 5. 打包
echo "[5/5] Packaging..."
ARTIFACT="llm-gateway-go-${VERSION}-upgrade.tar.gz"
(cd "$WORK" && tar czf "$OUT_DIR/$ARTIFACT" manifest.json kx-gateway images.tar migrations)

# 生成校验和
SHA256=$(shasum -a 256 "$OUT_DIR/$ARTIFACT" | awk '{print $1}')
echo "$SHA256  $ARTIFACT" > "$OUT_DIR/${ARTIFACT}.sha256"

SIZE=$(stat -f%z "$OUT_DIR/$ARTIFACT" 2>/dev/null || stat -c%s "$OUT_DIR/$ARTIFACT")
echo
echo "✅ Upgrade package built successfully"
echo "   Output: $OUT_DIR/$ARTIFACT"
echo "   Size: $(numfmt --to=iec-i --suffix=B $SIZE 2>/dev/null || echo "$SIZE bytes")"
echo "   SHA256: $SHA256"
echo
echo "Contents:"
tar tzf "$OUT_DIR/$ARTIFACT" | head -20
if [[ $(tar tzf "$OUT_DIR/$ARTIFACT" | wc -l) -gt 20 ]]; then
  echo "   ... ($(tar tzf "$OUT_DIR/$ARTIFACT" | wc -l) files total)"
fi

# 输出 JSON 供自动化流程使用
python3 - <<PY
import json
print(json.dumps({
  "version": "${GIT_TAG}",
  "build_seq": ${BUILD_SEQ},
  "artifact": "${ARTIFACT}",
  "sha256": "${SHA256}",
  "size_bytes": ${SIZE},
  "output_path": "$OUT_DIR/$ARTIFACT"
}))
PY

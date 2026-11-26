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
# 需要支持 installer-startup 清单的 llm-gw-installer。
# 输出：llm-gateway-go-{version}-upgrade.tar.gz
#
# Usage:
#   bash scripts/build-upgrade-package.sh [v2.4.6] [--out /path/to/output] [--build-seq 10]
#
# Env:
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
if [[ ! "$VERSION" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]]; then
  echo "upgrade-package: invalid version tag" >&2
  exit 1
fi
GIT_TAG="v${VERSION}"

if [[ -z "$OUT_DIR" ]]; then
  OUT_DIR="$ROOT/dist/upgrade"
fi
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "[upgrade-package] Building for version ${GIT_TAG} (build_seq=${BUILD_SEQ})"

# 1. 构建 Gateway 二进制（Linux amd64，升级包默认只构建主平台）
echo "[1/5] Building kx-gateway binary..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor \
  -ldflags "-X main.version=${GIT_TAG} -X main.buildSeq=${BUILD_SEQ}" \
  -o "$WORK/kx-gateway" ./cmd/gateway
chmod +x "$WORK/kx-gateway"

# 2. 导出 Docker 镜像
echo "[2/5] Exporting Docker image..."
IMAGE_TAG="${DOCKER_IMAGE_TAG:-kx-llm-gateway-go:${VERSION}}"
if [[ ! "$IMAGE_TAG" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.:/-]*$ ]]; then
  echo "upgrade-package: invalid Docker image reference" >&2
  exit 1
fi
if ! docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  echo "  Building Docker image $IMAGE_TAG..."
  docker build -t "$IMAGE_TAG" .
fi
docker save "$IMAGE_TAG" -o "$WORK/images.tar"
IMAGE_ID=$(docker image inspect --format '{{.Id}}' "$IMAGE_TAG")
if [[ ! "$IMAGE_ID" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  echo "upgrade-package: invalid Docker image ID" >&2
  exit 1
fi

# 3. 用安装器的顺序和 SQL 字节投递 Compose 增量迁移。生成的 TSV
# 由 TestStartupManifestMatchesStartupFiles 逐项核对。
echo "[3/5] Assembling installer startup migrations..."
mkdir -p "$WORK/migrations/startup"
startup_migrations=()
while IFS=$'\t' read -r ordinal name; do
  [[ -z "$ordinal" || "$ordinal" == \#* ]] && continue
  if [[ "$ordinal" != "${#startup_migrations[@]}" || ! "$name" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]*\.sql$ || "$name" == *.down.sql ]]; then
    echo "upgrade-package: invalid startup manifest entry $ordinal $name" >&2
    exit 1
  fi
  source="installer/cmd/llm-gw-installer/embeddata/startup/$name"
  [[ -f "$source" ]] || { echo "missing required startup migration: $source" >&2; exit 1; }
  cp "$source" "$WORK/migrations/startup/$name"
  startup_migrations+=("$name")
done < sql/schema/installed_startup_migrations.tsv
if [[ "${#startup_migrations[@]}" -lt 20 ]]; then
  echo "upgrade-package: only ${#startup_migrations[@]} installer startup migrations found" >&2
  exit 1
fi
echo "  bundled ${#startup_migrations[@]} installer startup migrations"

# 4. 生成 manifest.json
echo "[4/5] Generating manifest.json..."
FILES=()
for file in kx-gateway images.tar; do
  if [[ -e "$WORK/$file" ]]; then
    sha=$(shasum -a 256 "$WORK/$file" | awk '{print $1}')
    FILES+=("{\"path\":\"$file\",\"sha256\":\"$sha\"}")
  fi
done
for name in "${startup_migrations[@]}"; do
  relative="migrations/startup/$name"
  sha=$(shasum -a 256 "$WORK/$relative" | awk '{print $1}')
  FILES+=("{\"path\":\"$relative\",\"sha256\":\"$sha\"}")
done

IFS=,
cat > "$WORK/manifest.json" <<JSON
{
  "version": "${GIT_TAG}",
  "build_seq": ${BUILD_SEQ},
  "migration_mode": "installer-startup",
  "app_image": "${IMAGE_TAG}",
  "app_image_id": "${IMAGE_ID}",
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
tar tzf "$OUT_DIR/$ARTIFACT" > "$WORK/archive-contents.txt"
sed -n '1,20p' "$WORK/archive-contents.txt"
file_count=$(wc -l < "$WORK/archive-contents.txt" | awk '{print $1}')
if [[ "$file_count" -gt 20 ]]; then
  echo "   ... ($file_count entries total)"
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

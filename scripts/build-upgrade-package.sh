#!/usr/bin/env bash
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

# 3. 复制增量迁移脚本
echo "[3/5] Copying migration scripts..."
mkdir -p "$WORK/migrations"
if [[ -d sql/migrations ]]; then
  cp -a sql/migrations/*.sql "$WORK/migrations/" 2>/dev/null || true
fi
# 如果没有专门的迁移目录，复制 baseline（首次升级场景）
if [[ ! -f "$WORK/migrations/"*.sql ]]; then
  if [[ -d sql/baseline ]]; then
    cp -a sql/baseline/*.sql "$WORK/migrations/" 2>/dev/null || true
  fi
fi

# 4. 生成 manifest.json
echo "[4/5] Generating manifest.json..."
FILES=()
for file in kx-gateway images.tar migrations/*.sql; do
  if [[ -e "$WORK/$file" ]]; then
    sha=$(shasum -a 256 "$WORK/$file" | awk '{print $1}')
    FILES+=("{\"path\":\"$file\",\"sha256\":\"$sha\"}")
  fi
done

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
(cd "$WORK" && tar czf "$OUT_DIR/$ARTIFACT" --exclude='.' manifest.json kx-gateway images.tar migrations/*.sql 2>/dev/null || tar czf "$OUT_DIR/$ARTIFACT" manifest.json kx-gateway images.tar)

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
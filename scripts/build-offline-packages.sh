#!/usr/bin/env bash
# build-offline-packages.sh — 多平台离线包构建（gateway + installer + web + SQL）
#
# Usage:
#   bash scripts/build-offline-packages.sh [v2.4.6] [--out /path/to/v2.4.6] [--skip-web]
#
# Env:
#   GOPROXY   default https://goproxy.cn,direct
#   PLATFORMS override: "linux:amd64,tarwin:arm64,windows:amd64"
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

VERSION_ARG="${1:-}"
OUT_DIR=""
SKIP_WEB=false
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT_DIR="$2"; shift 2 ;;
    --skip-web) SKIP_WEB=true; shift ;;
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
  OUT_DIR="${DOWNLOAD_ARTIFACT_ROOT:-$ROOT/dist/offline}/v${VERSION}"
fi
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

if [[ "$SKIP_WEB" != "true" ]]; then
  echo "[build] frontend..."
  (cd web && npm run build) >/dev/null
fi
[[ -d web/dist ]] || { echo "missing web/dist — run npm run build in web/" >&2; exit 1; }

DEFAULT_PLATFORMS="linux:amd64,linux:arm64,linux:loong64,darwin:amd64,darwin:arm64,windows:amd64"
IFS=',' read -r -a PLATFORM_LIST <<< "${PLATFORMS:-$DEFAULT_PLATFORMS}"

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT

artifact_count=0
: >"$OUT_DIR/SHA256SUMS"

build_one() {
  local goos="$1" goarch="$2" pack="$3"
  local pkg_name="llm-gateway-go-${VERSION}-${goos}-${goarch}-offline"
  local work="$STAGING/$pkg_name"
  rm -rf "$work"
  mkdir -p "$work/bin" "$work/web" "$work/sql/baseline"

  echo "[build] ${goos}/${goarch} gateway..."
  if [[ "$goos" == "windows" ]]; then
    echo "[build] skip gateway binary on windows (compose uses container image)"
  else
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags="-s -w" -o "$work/bin/gateway" ./cmd/gateway
  fi

  local inst="llm-gw-installer"
  [[ "$goos" == "windows" ]] && inst="llm-gw-installer.exe"
  echo "[build] ${goos}/${goarch} installer..."
  (cd "$ROOT/installer" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w" -o "$work/bin/$inst" ./cmd/llm-gw-installer)

  cp -a web/dist/. "$work/web/"
  cp -a version.json "$work/version.json"
  [[ -f .env.example ]] && cp -a .env.example "$work/.env.example" || true
  cp -a install.sh "$work/install.sh"
  cp -a deploy/one-click/README.md "$work/ONE-CLICK.md"
  cp -a deploy/sql/schemas/baseline/*.sql "$work/sql/baseline/" 2>/dev/null || true

  local inst_root="llm-gw-installer-${goos}-${goarch}"
  [[ "$goos" == "windows" ]] && inst_root="${inst_root}.exe"
  cp -a "$work/bin/$inst" "$work/$inst_root"
  [[ "$goos" != "windows" && -f "$work/bin/gateway" ]] && cp -a "$work/bin/gateway" "$work/gateway"

  cat >"$work/INSTALL.md" <<EOF
# LLM Gateway ${GIT_TAG} — offline (${goos}/${goarch})

1. Extract this archive
2. Run: ./install.sh install   (or bin/llm-gw-installer install --skip-prompt)
3. Activate: https://llmgo.kxpms.cn/activate
4. Docs: deploy/one-click/README.md in repo
EOF

  local artifact=""
  if [[ "$pack" == "zip" ]]; then
    artifact="llm-gateway-go-${VERSION}-${goos}-${goarch}-offline.zip"
    (cd "$STAGING" && zip -qr "$OUT_DIR/$artifact" "$pkg_name")
  else
    artifact="llm-gateway-go-${VERSION}-${goos}-${goarch}-offline.tar.gz"
    tar czf "$OUT_DIR/$artifact" -C "$STAGING" "$pkg_name"
  fi

  local sha size
  sha=$(sha256sum "$OUT_DIR/$artifact" 2>/dev/null | awk '{print $1}')
  [[ -z "$sha" ]] && sha=$(shasum -a 256 "$OUT_DIR/$artifact" | awk '{print $1}')
  size=$(stat -c%s "$OUT_DIR/$artifact" 2>/dev/null || stat -f%z "$OUT_DIR/$artifact")
  echo "${sha}  ${artifact}" >>"$OUT_DIR/SHA256SUMS"
  artifact_count=$((artifact_count + 1))
  echo "[ok] $artifact (${size} bytes)"
}

for entry in "${PLATFORM_LIST[@]}"; do
  IFS=':' read -r goos goarch _ <<<"$entry"
  pack="tar"
  [[ "$goos" == "windows" ]] && pack="zip"
  build_one "$goos" "$goarch" "$pack"
done

ln -sfn "v${VERSION}" "$(dirname "$OUT_DIR")/latest" 2>/dev/null || true

python3 - <<PY
import json, os
print(json.dumps({
  "version": "${GIT_TAG}",
  "out_dir": "${OUT_DIR}",
  "artifact_count": ${artifact_count},
  "summary": "Built ${artifact_count} offline packages → ${OUT_DIR}"
}))
PY

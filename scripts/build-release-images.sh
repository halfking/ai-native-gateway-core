#!/usr/bin/env bash
# build-release-images.sh — build/export Docker release images for llm-gateway-go.
#
# Contract consumed by ai-native-maintain/scripts/build-release-docker.sh:
#   bash scripts/build-release-images.sh v{version} --out {out_dir}
#
# Outputs:
#   {out}/linux-amd64/llm-gateway-go-{version}-amd64.tar
#   {out}/linux-arm64/llm-gateway-go-{version}-arm64.tar
#   {out}/SHA256SUMS
#
# Env:
#   BASE_REGISTRY         default registry.kxpms.cn/kx-base
#   IMAGE_REPOSITORY      default llm-gateway-go
#   PLATFORMS             default linux/amd64,linux/arm64
#   SKIP_BUILD=1          emit contract metadata without docker builds
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

VERSION_ARG=""
OUT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT_DIR="${2:?--out requires a directory}"; shift 2 ;;
    -h|--help)
      sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    -*) echo "unknown arg: $1" >&2; exit 1 ;;
    *) VERSION_ARG="$1"; shift ;;
  esac
done

[[ -n "$VERSION_ARG" ]] || { echo "version required" >&2; exit 2; }
VERSION="${VERSION_ARG#v}"
OUT_DIR="${OUT_DIR:-$ROOT/dist/v${VERSION}/docker}"
BASE_REGISTRY="${BASE_REGISTRY:-registry.kxpms.cn/kx-base}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-llm-gateway-go}"
PLATFORMS_CSV="${PLATFORMS:-linux/amd64,linux/arm64}"
SKIP_BUILD="${SKIP_BUILD:-0}"

mkdir -p "$OUT_DIR"
rm -f "$OUT_DIR/SHA256SUMS"

declare -a produced=()
IFS=',' read -r -a platforms <<< "$PLATFORMS_CSV"
for platform in "${platforms[@]}"; do
  platform="${platform//[[:space:]]/}"
  [[ -n "$platform" ]] || continue
  os="${platform%%/*}"
  arch="${platform##*/}"
  if [[ "$os" != "linux" ]]; then
    echo "unsupported Docker OS: $os (Docker release images are linux-only)" >&2
    exit 1
  fi
  case "$arch" in amd64|arm64) ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac

  platform_dir="$OUT_DIR/${os}-${arch}"
  mkdir -p "$platform_dir"
  image_tag="${IMAGE_REPOSITORY}:${VERSION}-${arch}"
  tar_path="$platform_dir/llm-gateway-go-${VERSION}-${arch}.tar"

  if [[ "$SKIP_BUILD" == "1" ]]; then
    printf 'SKIP_BUILD=1 placeholder for %s\n' "$image_tag" >"$tar_path"
  else
    if ! command -v docker >/dev/null 2>&1; then
      echo "docker not found; set SKIP_BUILD=1 for dry-run" >&2
      exit 1
    fi
    docker buildx build \
      --platform "$platform" \
      --build-arg "BASE_REGISTRY=${BASE_REGISTRY}" \
      --tag "$image_tag" \
      --load \
      .
    docker save "$image_tag" -o "$tar_path"
  fi

  [[ -s "$tar_path" ]] || { echo "missing Docker tar: $tar_path" >&2; exit 1; }
  produced+=("$tar_path")
done

for f in "${produced[@]}"; do
  rel="${f#"$OUT_DIR/"}"
  sha=$(shasum -a 256 "$f" | awk '{print $1}')
  echo "$sha  $rel" >>"$OUT_DIR/SHA256SUMS"
done

PLATFORMS_JSON="$PLATFORMS_CSV" ARTIFACT_COUNT="${#produced[@]}" OUT_DIR_JSON="$OUT_DIR" VERSION_JSON="v${VERSION}" python3 - <<'PY'
import json, os
print(json.dumps({
  "version": os.environ["VERSION_JSON"],
  "out_dir": os.environ["OUT_DIR_JSON"],
  "platforms": [p.strip() for p in os.environ["PLATFORMS_JSON"].split(',') if p.strip()],
  "artifact_count": int(os.environ["ARTIFACT_COUNT"]),
  "summary": f"Built {os.environ['ARTIFACT_COUNT']} Docker release image tar(s) → {os.environ['OUT_DIR_JSON']}",
}, ensure_ascii=False))
PY

#!/usr/bin/env bash
# publish-download-release.sh — package current gateway release for public download (245)
#
# Usage: bash scripts/publish-download-release.sh v2.4.6
#
# Env:
#   DOWNLOAD_ARTIFACT_ROOT  default /var/www/download/llm-gateway-go
#   LLM_GATEWAY_CURRENT     default /opt/llm-gateway-go/current
#   LLM_GATEWAY_ROOT        default /opt/llm-gateway-go
set -euo pipefail

VERSION="${1:?usage: publish-download-release.sh <version>}"
VERSION="${VERSION#v}"

ROOT="${DOWNLOAD_ARTIFACT_ROOT:-/var/www/download/llm-gateway-go}"
CURRENT="${LLM_GATEWAY_CURRENT:-/opt/llm-gateway-go/current}"
GW_ROOT="${LLM_GATEWAY_ROOT:-/opt/llm-gateway-go}"

OUT_DIR="$ROOT/v${VERSION}"
mkdir -p "$OUT_DIR"

if [[ ! -x "$CURRENT/gateway" ]]; then
  echo "missing current gateway binary: $CURRENT/gateway" >&2
  exit 1
fi

STAGING=$(mktemp -d)
trap 'rm -rf "$STAGING"' EXIT

PKG="$STAGING/llm-gateway-go-${VERSION}-linux-amd64-offline"
mkdir -p "$PKG"
cp -a "$CURRENT/gateway" "$PKG/gateway"
chmod +x "$PKG/gateway"
[[ -d "$CURRENT/web" ]] && cp -a "$CURRENT/web" "$PKG/web"
[[ -f "$CURRENT/version.json" ]] && cp -a "$CURRENT/version.json" "$PKG/version.json"
[[ -f "$GW_ROOT/.env.example" ]] && cp -a "$GW_ROOT/.env.example" "$PKG/.env.example" || true

cat >"$PKG/INSTALL.md" <<EOF
# LLM Gateway ${VERSION} — offline install

1. Extract this archive on your server
2. Run: ./gateway --help  (or use llm-gw-installer if bundled)
3. Activate at: https://llmgo.kxpms.cn/activate
4. Source: https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go
EOF

ARTIFACT="llm-gateway-go-${VERSION}-linux-amd64-offline.tar.gz"
tar czf "$OUT_DIR/$ARTIFACT" -C "$STAGING" "llm-gateway-go-${VERSION}-linux-amd64-offline"
SHA=$(sha256sum "$OUT_DIR/$ARTIFACT" | awk '{print $1}')
SIZE=$(stat -c%s "$OUT_DIR/$ARTIFACT" 2>/dev/null || stat -f%z "$OUT_DIR/$ARTIFACT")
echo "${SHA}  ${ARTIFACT}" > "$OUT_DIR/SHA256SUMS"

ln -sfn "v${VERSION}" "$ROOT/latest"

# Upsert artifact metadata via psql when DATABASE URL is available
DB_URL=""
if [[ -f "$GW_ROOT/.env" ]]; then
  DB_URL=$(grep -E '^LLM_GATEWAY_DATABASE_URL=' "$GW_ROOT/.env" | head -1 | cut -d= -f2- | tr -d '"' | tr -d "'")
fi
DB_URL="${DB_URL:-${LLM_GATEWAY_DATABASE_URL:-}}"

if [[ -n "$DB_URL" ]]; then
  BUILD_SEQ=0
  if [[ -f "$CURRENT/version.json" ]]; then
    BUILD_SEQ=$(python3 -c "import json; print(json.load(open('$CURRENT/version.json')).get('build_seq',0))" 2>/dev/null || echo 0)
  fi
  psql "$DB_URL" -v ON_ERROR_STOP=1 <<SQL
INSERT INTO releases (version, build_seq, channel, title, image_tag, created_by, published_at)
VALUES ('v${VERSION}', ${BUILD_SEQ}, 'stable', 'LLM Gateway v${VERSION}', 'offline', 'publish-script', now())
ON CONFLICT (version) DO UPDATE SET
  build_seq = GREATEST(releases.build_seq, EXCLUDED.build_seq),
  published_at = COALESCE(releases.published_at, EXCLUDED.published_at);

INSERT INTO release_artifacts (release_version, platform, arch, edition, artifact_name, sha256, size_bytes, download_path)
VALUES ('v${VERSION}', 'linux', 'amd64', 'customer', '${ARTIFACT}', '${SHA}', ${SIZE}, 'v${VERSION}/${ARTIFACT}')
ON CONFLICT (release_version, platform, arch, edition, artifact_name) DO UPDATE SET
  sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes, download_path = EXCLUDED.download_path;
SQL
fi

export OUT_DIR
python3 - <<PY
import json, os
out_dir = os.environ.get("OUT_DIR", "${OUT_DIR}")
print(json.dumps({
  "artifact_count": 1,
  "summary": f"Published v${VERSION} linux/amd64 → {out_dir}/${ARTIFACT} sha256=${SHA[:16]}…"
}))
PY
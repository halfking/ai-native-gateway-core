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

ln -sfn "v${VERSION}" "$ROOT/latest"

# Upsert artifact metadata via local psql when available
if [[ -n "${DATABASE_URL:-}" ]] || [[ -f "$GW_ROOT/.env" ]]; then
  set -a
  # shellcheck disable=SC1090
  [[ -f "$GW_ROOT/.env" ]] && source "$GW_ROOT/.env"
  set +a
  if [[ -n "${LLM_GATEWAY_PG_DSN:-}" ]]; then
    psql "$LLM_GATEWAY_PG_DSN" -v ON_ERROR_STOP=1 <<SQL
INSERT INTO release_artifacts (release_version, platform, arch, edition, artifact_name, sha256, size_bytes, download_path)
VALUES ('v${VERSION}', 'linux', 'amd64', 'customer', '${ARTIFACT}', '${SHA}', ${SIZE}, 'v${VERSION}/${ARTIFACT}')
ON CONFLICT (release_version, platform, arch, edition, artifact_name) DO UPDATE SET
  sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes, download_path = EXCLUDED.download_path;
SQL
  fi
fi

python3 - <<PY
import json
print(json.dumps({
  "artifact_count": 1,
  "summary": f"Published v${VERSION} linux/amd64 → {OUT_DIR}/{ARTIFACT} sha256={SHA[:16]}…"
}))
PY

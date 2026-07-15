#!/usr/bin/env bash
# publish-download-release.sh — 构建多平台离线包并写入存储 + release_artifacts
#
# Usage: bash scripts/publish-download-release.sh [v2.4.6]
#
# Env:
#   DOWNLOAD_ARTIFACT_ROOT  default /var/www/download/llm-gateway-go
#   BUILD_MULTI_PLATFORM    default 1 (call build-offline-packages.sh)
#   LLM_GATEWAY_DATABASE_URL / read from GW .env
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

VERSION="${1:?usage: publish-download-release.sh <version>}"
VERSION="${VERSION#v}"
GIT_TAG="v${VERSION}"

ROOT_ART="${DOWNLOAD_ARTIFACT_ROOT:-/var/www/download/llm-gateway-go}"
OUT_DIR="$ROOT_ART/v${VERSION}"
GW_ROOT="${LLM_GATEWAY_ROOT:-/opt/llm-gateway-go}"
CURRENT="${LLM_GATEWAY_CURRENT:-$GW_ROOT/current}"

mkdir -p "$OUT_DIR"
artifact_count=0
summary=""

if [[ -f "$OUT_DIR/SHA256SUMS" && "${SKIP_BUILD:-0}" == "1" ]]; then
  artifact_count=$(wc -l <"$OUT_DIR/SHA256SUMS" | tr -d ' ')
  summary="DB sync only — artifacts already in $OUT_DIR"
elif [[ "${BUILD_MULTI_PLATFORM:-1}" == "1" ]]; then
  build_json=$(bash "$SCRIPT_DIR/build-offline-packages.sh" "$GIT_TAG" --out "$OUT_DIR" 2>&1 | tail -1)
  artifact_count=$(python3 -c "import json;print(json.load(open(0))['artifact_count'])" <<<"$build_json" 2>/dev/null || echo 0)
  summary=$(python3 -c "import json;print(json.load(open(0))['summary'])" <<<"$build_json" 2>/dev/null || echo "built packages in $OUT_DIR")
else
  # Legacy: single linux-amd64 from running gateway on server
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
  ARTIFACT="llm-gateway-go-${VERSION}-linux-amd64-offline.tar.gz"
  tar czf "$OUT_DIR/$ARTIFACT" -C "$STAGING" "llm-gateway-go-${VERSION}-linux-amd64-offline"
  SHA=$(sha256sum "$OUT_DIR/$ARTIFACT" | awk '{print $1}')
  SIZE=$(stat -c%s "$OUT_DIR/$ARTIFACT" 2>/dev/null || stat -f%z "$OUT_DIR/$ARTIFACT")
  echo "${SHA}  ${ARTIFACT}" >"$OUT_DIR/SHA256SUMS"
  artifact_count=1
  summary="Published legacy single artifact → $OUT_DIR/$ARTIFACT"
fi

ln -sfn "v${VERSION}" "$ROOT_ART/latest"

DB_URL=""
if [[ -f "$GW_ROOT/.env" ]]; then
  DB_URL=$(grep -E '^LLM_GATEWAY_DATABASE_URL=' "$GW_ROOT/.env" | head -1 | cut -d= -f2- | tr -d '"' | tr -d "'")
fi
DB_URL="${DB_URL:-${LLM_GATEWAY_DATABASE_URL:-}}"

BUILD_SEQ=0
if [[ -f version.json ]]; then
  BUILD_SEQ=$(python3 -c "import json;print(json.load(open('version.json')).get('build_seq',0))")
elif [[ -f "$CURRENT/version.json" ]]; then
  BUILD_SEQ=$(python3 -c "import json;print(json.load(open('$CURRENT/version.json')).get('build_seq',0))")
fi

if [[ -n "$DB_URL" && -f "$OUT_DIR/SHA256SUMS" ]]; then
  psql "$DB_URL" -v ON_ERROR_STOP=1 <<SQL
INSERT INTO releases (version, build_seq, channel, title, image_tag, created_by, published_at)
VALUES ('${GIT_TAG}', ${BUILD_SEQ}, 'stable', 'LLM Gateway ${GIT_TAG}', 'offline', 'publish-script', now())
ON CONFLICT (version) DO UPDATE SET
  build_seq = GREATEST(releases.build_seq, EXCLUDED.build_seq),
  published_at = COALESCE(releases.published_at, EXCLUDED.published_at);
SQL

  python3 - "$OUT_DIR" "$GIT_TAG" "$VERSION" "$DB_URL" <<'PY'
import os, re, subprocess, sys
out_dir, git_tag, short_ver, db_url = sys.argv[1:5]
sums = os.path.join(out_dir, "SHA256SUMS")
pat = re.compile(
    rf"^llm-gateway-go-{re.escape(short_ver)}-(?P<plat>linux|darwin|windows)-(?P<arch>amd64|arm64|loong64)-offline\.(?:tar\.gz|zip)$"
)
for line in open(sums):
    sha, fname = line.split(None, 1)
    fname = fname.strip()
    m = pat.match(fname)
    if not m:
        continue
    plat, arch = m.group("plat"), m.group("arch")
    path = os.path.join(out_dir, fname)
    size = os.path.getsize(path)
    sql = f"""
INSERT INTO release_artifacts (release_version, platform, arch, edition, artifact_name, sha256, size_bytes, download_path)
VALUES ('{git_tag}', '{plat}', '{arch}', 'customer', '{fname}', '{sha}', {size}, 'v{short_ver}/{fname}')
ON CONFLICT (release_version, platform, arch, edition, artifact_name) DO UPDATE SET
  sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes, download_path = EXCLUDED.download_path;
"""
    subprocess.run(["psql", db_url, "-v", "ON_ERROR_STOP=1", "-c", sql], check=True)
PY
fi

python3 - <<PY
import json
print(json.dumps({
  "artifact_count": int("${artifact_count:-0}"),
  "summary": """${summary}"""
}))
PY

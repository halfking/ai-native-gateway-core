#!/usr/bin/env bash
# Regenerate scripts/user/.ssot-manifest.sha256 from the current file contents.
# Run this immediately after copying the SSOT files in; the gate compares against it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
FILES="install.sh install-host.sh install-docker.sh upgrade.sh client-deploy.sh client-deploy.ps1 lib/kaixuan-layout.sh lib/kaixuan-layout-test.sh lib/backup-db.sh lib/clean-logs.sh"
: >scripts/user/.ssot-manifest.sha256
for f in $FILES; do
  [[ -f "scripts/user/$f" ]] || { echo "missing: scripts/user/$f" >&2; exit 1; }
  sha256sum "scripts/user/$f" >>scripts/user/.ssot-manifest.sha256
done
echo "wrote scripts/user/.ssot-manifest.sha256 ($(wc -l <scripts/user/.ssot-manifest.sha256) entries)"

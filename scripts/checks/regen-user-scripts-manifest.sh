#!/usr/bin/env bash
# Regenerate scripts/user/.ssot-manifest.sha256 from the current file contents.
# Run this immediately after copying the SSOT files in; the gate compares against it.
#
# The hash is taken over the LF-normalized bytes, not the raw working-tree
# bytes. This repo's .gitattributes says `* text=auto eol=lf`, so git stores
# LF while a Windows checkout has CRLF. Hashing the raw file would make the
# manifest correct only on whichever machine generated it — and the gate would
# fail on every other clone, which is the same "gate that only works locally"
# failure this whole change is meant to remove.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
FILES="install.sh install-host.sh install-docker.sh upgrade.sh client-deploy.sh client-deploy.ps1 lib/kaixuan-layout.sh lib/kaixuan-layout-test.sh lib/backup-db.sh lib/clean-logs.sh"
norm_sha() { sed -e 's/\r$//' "$1" | sha256sum | cut -d' ' -f1; }
: >scripts/user/.ssot-manifest.sha256
for f in $FILES; do
  [[ -f "scripts/user/$f" ]] || { echo "missing: scripts/user/$f" >&2; exit 1; }
  printf '%s  scripts/user/%s\n' "$(norm_sha "scripts/user/$f")" "$f" >>scripts/user/.ssot-manifest.sha256
done
echo "wrote scripts/user/.ssot-manifest.sha256 ($(wc -l <scripts/user/.ssot-manifest.sha256) entries, LF-normalized)"

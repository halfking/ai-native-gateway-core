#!/usr/bin/env bash
# scripts/pre-commit-install.sh
# One-time installer for the local pre-commit gate.
#
# Wires scripts/pre-commit-check.sh into .git/hooks/pre-commit so every
# `git commit` runs the 4 checks (go vet, SQL lint, migration uniqueness,
# vue-tsc) before allowing the commit to land.
#
# Usage:
#   bash scripts/pre-commit-install.sh
#
# To uninstall:
#   rm .git/hooks/pre-commit

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
CHECK_SCRIPT="$SCRIPT_DIR/pre-commit-check.sh"

# Resolve the real git dir — for submodules, .git is a gitlink pointing
# to the parent repo's .git/modules/<submodule>/. `git rev-parse --git-dir`
# returns the absolute path of the real git directory.
GIT_DIR="$(cd "$SCRIPT_DIR" && git rev-parse --git-dir 2>/dev/null)"
if [[ -z "$GIT_DIR" || "$GIT_DIR" == ".git" ]]; then
  echo "ERROR: $SCRIPT_DIR is not inside a git working tree"
  exit 1
fi
HOOK_FILE="$GIT_DIR/hooks/pre-commit"

if [[ ! -x "$CHECK_SCRIPT" ]]; then
  echo "ERROR: $CHECK_SCRIPT is not executable. Run: chmod +x $CHECK_SCRIPT"
  exit 1
fi

if [[ -e "$HOOK_FILE" ]]; then
  echo "WARN: $HOOK_FILE already exists. Backing up to $HOOK_FILE.bak-$(date +%s)"
  mv "$HOOK_FILE" "$HOOK_FILE.bak-$(date +%s)"
fi

cat > "$HOOK_FILE" <<EOF
#!/usr/bin/env bash
# Auto-installed by scripts/pre-commit-install.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ)
# Runs the 4-check gate before every commit. To bypass: git commit --no-verify
exec "$CHECK_SCRIPT" "\$@"
EOF
chmod +x "$HOOK_FILE"

echo "Installed pre-commit hook: $HOOK_FILE"
echo "  -> calls $CHECK_SCRIPT"
echo ""
echo "Verify with:"
echo "  ls -la $HOOK_FILE"
echo "  bash $CHECK_SCRIPT"

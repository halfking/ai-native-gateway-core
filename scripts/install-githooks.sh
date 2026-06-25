#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# install-githooks.sh — 安装 SI-LLM-Gateway git 钩子
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
hooks_src="$repo_root/.githooks"
git_dir="$(git rev-parse --git-dir)"

[[ -d "$hooks_src" ]] || { echo "❌ .githooks/ not found at $hooks_src"; exit 1; }

echo "📦 Installing git hooks..."
git config core.hooksPath "$hooks_src"
echo "  ✓ core.hooksPath = $hooks_src"
for hook in "$hooks_src"/*; do
  [[ -f "$hook" ]] || continue
  chmod +x "$hook"
  echo "  ✓ installed $(basename "$hook") (executable)"
done
echo ""
echo "✅ All hooks installed."
git config core.hooksPath

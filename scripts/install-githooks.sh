#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# install-githooks.sh — 安装 git 钩子
#
# 用法:
#   ./scripts/install-githooks.sh              # 安装 .githooks/ 下所有钩子（core.hooksPath）
#   ./scripts/install-githooks.sh --pre-commit # 安装 pre-commit-check.sh 为 pre-commit 钩子
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

install_pre_commit() {
  CHECK_SCRIPT="$SCRIPT_DIR/pre-commit-check.sh"
  GIT_DIR="$(cd "$REPO_DIR" && git rev-parse --git-dir 2>/dev/null)"
  [[ -n "$GIT_DIR" ]] || { echo "❌ 不在 git 仓库中"; exit 1; }

  HOOK_FILE="$GIT_DIR/hooks/pre-commit"
  [[ -x "$CHECK_SCRIPT" ]] || { echo "❌ $CHECK_SCRIPT 不可执行"; exit 1; }

  if [[ -e "$HOOK_FILE" ]]; then
    echo "⚠  $HOOK_FILE 已存在，备份至 $HOOK_FILE.bak-$(date +%s)"
    mv "$HOOK_FILE" "$HOOK_FILE.bak-$(date +%s)"
  fi

  cat > "$HOOK_FILE" <<EOF
#!/usr/bin/env bash
# Auto-installed by scripts/install-githooks.sh --pre-commit on $(date -u +%Y-%m-%dT%H:%M:%SZ)
exec "$CHECK_SCRIPT" "\$@"
EOF
  chmod +x "$HOOK_FILE"
  echo "✅ pre-commit 钩子已安装: $HOOK_FILE"
  echo "   → 调用 $CHECK_SCRIPT"
}

install_all_hooks() {
  hooks_src="$REPO_DIR/.githooks"
  [[ -d "$hooks_src" ]] || { echo "❌ .githooks/ not found at $hooks_src"; exit 1; }

  echo "📦 安装 git 钩子..."
  git config core.hooksPath "$hooks_src"
  echo "  ✓ core.hooksPath = $hooks_src"
  for hook in "$hooks_src"/*; do
    [[ -f "$hook" ]] || continue
    chmod +x "$hook"
    echo "  ✓ installed $(basename "$hook") (executable)"
  done
  echo ""
  echo "✅ 所有钩子已安装"
  git config core.hooksPath
}

case "${1:-}" in
  --pre-commit) install_pre_commit ;;
  *)            install_all_hooks ;;
esac

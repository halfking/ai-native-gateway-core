#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# sync-to-github.sh — 推 SI-LLM-Gateway 到 GitHub 公开镜像
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
scanner="$repo_root/scripts/scan-secrets.sh"
replacements="$repo_root/scripts/scan-secrets.replacements"
mirror_dir="/tmp/llmgw-github-mirror"
github_url="$(git remote get-url github 2>/dev/null || echo "")"

DRY_RUN=0
SKIP_HISTORY=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --skip-history-rewrite) SKIP_HISTORY=1 ;;
  esac
done

if [[ -z "$github_url" ]]; then
  echo "❌ No 'github' remote. Run: git remote add github git@github.com:halfking/SI-LLM-Gateway.git"
  exit 1
fi
[[ -x "$scanner" ]] || { echo "❌ scanner not executable: $scanner"; exit 1; }

echo "🚀 SI-LLM-Gateway → GitHub sync"
echo "   github:    $github_url"
echo "   dry-run:   $([[ $DRY_RUN -eq 1 ]] && echo yes || echo no)"
echo "   history:   $([[ $SKIP_HISTORY -eq 1 ]] && echo skip || echo rewrite)"
echo ""

echo "━━━ Step 1/5: Working tree scan (strict, tracked-only) ━━━"
if ! "$scanner" --mode=strict --paths=. --tracked-only --format=text; then
  echo ""
  echo "❌ Working tree has sensitive findings. Clean them first."
  echo "   See: docs/REPO-MIRROR-POLICY.md"
  exit 1
fi
echo ""

echo "━━━ Step 2/5: Create local mirror ━━━"
rm -rf "$mirror_dir"
git clone --mirror --no-hardlinks "$repo_root" "$mirror_dir"
echo "   ✓ mirror at $mirror_dir"
echo ""

if [[ $SKIP_HISTORY -eq 0 ]]; then
  echo "━━━ Step 3/5: Rewrite history with filter-repo ━━━"
  if ! command -v git-filter-repo >/dev/null 2>&1; then
    echo "❌ git-filter-repo not installed"
    echo "   Install: pip3 install git-filter-repo"
    exit 1
  fi
  [[ -f "$replacements" ]] || { echo "❌ missing: $replacements"; exit 1; }
  cd "$mirror_dir"
  git filter-repo --force --replace-text "$replacements"
  cd "$repo_root"
  echo "   ✓ history rewritten"
else
  echo "━━━ Step 3/5: SKIPPED history rewrite ━━━"
fi
echo ""

echo "━━━ Step 4/5: Verify mirror is clean ━━━"
verify_dir="/tmp/llmgw-verify"
rm -rf "$verify_dir"
git clone --no-hardlinks "$mirror_dir" "$verify_dir" 2>&1 | tail -3
if ! "$scanner" --mode=strict --paths="$verify_dir" --format=text; then
  echo ""
  echo "❌ Mirror still has sensitive content. Aborting."
  rm -rf "$verify_dir"
  exit 1
fi
rm -rf "$verify_dir"
echo "   ✓ mirror clean"
echo ""

if [[ $DRY_RUN -eq 1 ]]; then
  echo "━━━ Step 5/5: DRY RUN — would push to $github_url ━━━"
  exit 0
fi

echo "━━━ Step 5/5: Push to GitHub ━━━"
read -rp "   Push to $github_url? Type 'yes' to confirm: " confirm
[[ "$confirm" == "yes" ]] || { echo "   Aborted."; exit 1; }

cd "$mirror_dir"
git push --mirror "$github_url"
cd "$repo_root"

echo ""
echo "✅ Sync complete!"
echo "   Verify: git clone $github_url /tmp/llmgw-public-check"
echo "   $scanner --mode=strict --paths=/tmp/llmgw-public-check --tracked-only"

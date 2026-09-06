#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# sync-to-github.sh — 推 SI-LLM-Gateway 到 GitHub 公开镜像
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
scanner="$repo_root/scripts/scan-secrets.sh"
replacements="$repo_root/scripts/scan-secrets.replacements"
private_replacements="$repo_root/scripts/scan-secrets.private.replacements"
mirror_dir="/tmp/llmgw-github-mirror"
github_url="$(git remote get-url github 2>/dev/null || echo "")"

DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --skip-history-rewrite)
      echo "❌ --skip-history-rewrite is not permitted for a public mirror." >&2
      exit 2
      ;;
    *) echo "usage: $0 [--dry-run]" >&2; exit 2 ;;
  esac
done

[[ -n "$github_url" ]] || { echo "❌ No github remote configured."; exit 1; }
[[ -x "$scanner" ]] || { echo "❌ scanner not executable: $scanner"; exit 1; }

cleanup() {
  [[ -n "${filter_replacements:-}" ]] && rm -f "$filter_replacements"
  [[ -n "${verify_dir:-}" ]] && rm -rf "$verify_dir"
}
trap cleanup EXIT

validate_replacements() {
  local file=$1
  local label=$2
  local line source replacement
  declare -A seen=()

  [[ -f "$file" && -r "$file" ]] || {
    echo "❌ $label replacement table is missing or unreadable: $file" >&2
    return 1
  }

  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" == *'==>'* ]] || {
      echo "❌ $label replacement table has an invalid entry" >&2
      return 1
    }
    source=${line%%==>*}
    replacement=${line#*==>}
    [[ -n "$source" && -n "$replacement" && "$replacement" != *'==>'* ]] || {
      echo "❌ $label replacement table has an invalid entry" >&2
      return 1
    }
    [[ -z "${seen[$source]:-}" ]] || {
      echo "❌ $label replacement table contains a duplicate source entry" >&2
      return 1
    }
    seen[$source]=1
  done < "$file"

  (( ${#seen[@]} > 0 )) || {
    echo "❌ $label replacement table must contain at least one replacement" >&2
    return 1
  }
}

verify_history_removed_sources() {
  local source
  local -a revisions=()
  while IFS= read -r revision; do
    [[ -n "$revision" ]] && revisions+=("$revision")
  done < <(git -C "$mirror_dir" rev-list --all)
  (( ${#revisions[@]} > 0 )) || {
    echo "❌ Filtered mirror has no reachable revisions to verify." >&2
    return 1
  }

  while IFS= read -r source; do
    [[ -z "$source" ]] && continue
    if git -C "$mirror_dir" grep -I -F -q -- "$source" "${revisions[@]}"; then
      echo "❌ Filtered mirror still contains a replacement source in reachable history." >&2
      return 1
    fi
  done < <(
    awk '
      /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
      index($0, "==>") { print substr($0, 1, index($0, "==>") - 1) }
    ' "$replacements" "$private_replacements"
  )
}

echo "🚀 SI-LLM-Gateway → GitHub sync"
echo "   github:  $github_url"
echo "   dry-run: $([[ $DRY_RUN -eq 1 ]] && echo yes || echo no)"
echo ""

echo "━━━ Step 1/5: Working tree scan (strict, tracked-only) ━━━"
baseline="$repo_root/scripts/scan-secrets.baseline"
baseline_arg=()
[[ -f "$baseline" ]] && baseline_arg=("--baseline=$baseline")
"$scanner" --mode=strict --paths=. --tracked-only "${baseline_arg[@]}" --format=text || {
  echo "❌ Working tree has sensitive findings. Clean them first." >&2
  exit 1
}

echo "━━━ Step 2/5: Validate history rewrite inputs ━━━"
validate_replacements "$replacements" public
validate_replacements "$private_replacements" private
if git -C "$repo_root" ls-files --error-unmatch -- scripts/scan-secrets.private.replacements >/dev/null 2>&1; then
  echo "❌ Private replacement table must not be tracked." >&2
  exit 1
fi
if ! git -C "$repo_root" check-ignore -q -- scripts/scan-secrets.private.replacements; then
  echo "❌ Private replacement table must be protected by .gitignore." >&2
  exit 1
fi

filter_replacements=$(mktemp "${TMPDIR:-/tmp}/llmgw-replacements.XXXXXX")
chmod 0600 "$filter_replacements"
cat "$replacements" "$private_replacements" > "$filter_replacements"

echo "━━━ Step 3/5: Create and rewrite local mirror ━━━"
rm -rf "$mirror_dir"
git clone --mirror --no-hardlinks "$repo_root" "$mirror_dir"
git -C "$mirror_dir" filter-repo --force --replace-text "$filter_replacements"
echo "   ✓ history rewritten"

echo "━━━ Step 4/5: Verify all mirror refs and reachable history ━━━"
verify_dir=$(mktemp -d "${TMPDIR:-/tmp}/llmgw-public-check.XXXXXX")
git clone --no-hardlinks "$mirror_dir" "$verify_dir"
"$scanner" --mode=strict --repo-root="$verify_dir" --paths="$verify_dir" --tracked-only --baseline="$verify_dir/scripts/scan-secrets.baseline" --format=text || {
  echo "❌ Mirror checkout has sensitive findings. Aborting." >&2
  exit 1
}
verify_history_removed_sources
echo "   ✓ mirror refs and reachable history clean"

if [[ $DRY_RUN -eq 1 ]]; then
  echo "━━━ Step 5/5: DRY RUN — would push to $github_url ━━━"
  exit 0
fi

echo "━━━ Step 5/5: Push to GitHub ━━━"
read -rp "   Push to $github_url? Type 'yes' to confirm: " confirm
[[ "$confirm" == "yes" ]] || { echo "   Aborted."; exit 1; }
git -C "$mirror_dir" push --mirror "$github_url"

echo "✅ Sync complete!"

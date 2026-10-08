#!/usr/bin/env bash
***REMOVED***
# sync-to-github.sh — 推 SI-LLM-Gateway 到 GitHub 公开镜像
***REMOVED***
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
scanner="$repo_root/scripts/scan-secrets.sh"
replacements="$repo_root/scripts/scan-secrets.replacements"
private_replacements="$repo_root/scripts/scan-secrets.private.replacements"
# mktemp 而非固定路径（三十七轮审计）：并发两跑会在固定 /tmp/llmgw-github-mirror
# 上互删对方 mirror——最坏把半重写镜像 force-push 到公开仓库（历史截断）。
mirror_dir="$(mktemp -d "${TMPDIR:-/tmp}/llmgw-github-mirror.XXXXXX")"
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
  [[ -n "${mirror_dir:-}" && -d "${mirror_dir:-}" ]] && rm -rf "$mirror_dir"
  [[ -n "${filter_replacements:-}" ]] && rm -f "$filter_replacements"
  [[ -n "${blob_callback_file:-}" ]] && rm -f "$blob_callback_file"
  [[ -n "${filename_callback_file:-}" ]] && rm -f "$filename_callback_file"
  [[ -n "${commit_callback_file:-}" ]] && rm -f "$commit_callback_file"
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
  local -a revisions=()
  while IFS= read -r revision; do
    [[ -n "$revision" ]] && revisions+=("$revision")
  done < <(git -C "$mirror_dir" rev-list --all)
  (( ${#revisions[@]} > 0 )) || {
    echo "❌ Filtered mirror has no reachable revisions to verify." >&2
    return 1
  }

  # 旧实现对每个 source × 每个 revision 各跑一次 git grep（逐修订整树遍历），
  # 本仓库规模（13.6k 修订 × ~30 source）实测单串就要小时级、全表 >20 小时，
  # 门等于不可用（2026-10-08 dry-run 实测卡死 35 分钟没跑完一个串）。改为
  # 单遍流式扫描：cat-file --batch-all-objects 把所有对象（blob 内容、提交与
  # 标签消息）展平成一路字节流，grep -aF -f 对全部 source 只扫一遍——覆盖面
  # 反而更大（连消息一起查），代价是分钟级。grep 用 -c 而不是 -q：-q 命中即
  # 提前退出会让上游 git 吃 SIGPIPE，pipefail 下管道返回 141 会被 if 误判成
  # "没命中"，命中反而放行。
  local srcs_file hits
  srcs_file=$(mktemp "${TMPDIR:-/tmp}/llmgw-srcs.XXXXXX")
  awk '
      /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
      index($0, "==>") { print substr($0, 1, index($0, "==>") - 1) }
    ' "$replacements" "$private_replacements" > "$srcs_file"
  if [[ ! -s "$srcs_file" ]]; then
    echo "❌ No replacement sources parsed — refusing to verify vacuously." >&2
    rm -f "$srcs_file"
    return 1
  fi
  hits=$(git -C "$mirror_dir" cat-file --batch-all-objects --batch --unordered \
    | LC_ALL=C grep -a -c -F -f "$srcs_file" || true)
  if [[ "${hits:-0}" -gt 0 ]]; then
    echo "❌ Filtered mirror still contains a replacement source (content or commit message)." >&2
    echo "   matched sources (deduped, first 20):" >&2
    git -C "$mirror_dir" cat-file --batch-all-objects --batch --unordered \
      | LC_ALL=C grep -a -o -h -F -f "$srcs_file" 2>/dev/null | sort -u \
      | head -20 | sed 's/^/     /' >&2
    rm -f "$srcs_file"
    return 1
  fi
  rm -f "$srcs_file"
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

# 从同一对替换表生成三份字节级回调体。--replace-text 对非 UTF-8 的二进制
# blob 整个跳过（实测 installer/llm-gw-installer 与 cmd/llm-gw-annotator 两
# 个 13MB Go 编译二进制的 blob OID 改写前后完全一致，内嵌的 internal.example.com /
# 内网 IP / 开发者路径原样存活）；而 --replace-text/--replace-message 都
# 不碰 树对象里的文件路径名（deploy/download.internal.example.com.nginx.conf 等 nginx
# conf 文件名）和 作者/提交者邮箱（*@internal.example.com）。三类泄漏各自需要一份回调：
#   blob-callback     → blob 内容（字节级，覆盖二进制）
#   filename-callback → 树对象里的路径名
#   commit-callback   → author/committer 的 name/email（message 仍归
#                       --replace-message 管）
# 生成器只支持字面量条目（当前表全部是字面量）；若表里出现 regex:/glob:
# 前缀条目，需要回到这里同步扩展生成器。
gen_callbacks() {
  python3 - "$replacements" "$private_replacements" "$1" <<'PY'
import sys

mode = sys.argv[3]
pairs = []
for path in sys.argv[1:3]:
    raw = open(path, "rb").read().decode("utf-8", "replace")
    for line in raw.splitlines():
        s = line.strip()
        if not s or s.startswith("#") or "==>" not in s:
            continue
        src, dst = s.split("==>", 1)
        if src:
            pairs.append((src.encode("utf-8"), dst.encode("utf-8")))

lines = []
if mode == "blob":
    lines.append("data = blob.data")
    lines += ["data = data.replace(%r, %r)" % p for p in pairs]
    lines.append("blob.data = data")
elif mode == "filename":
    lines += ["filename = filename.replace(%r, %r)" % p for p in pairs]
    lines.append("return filename")
elif mode == "commit":
    fields = ("author_name", "author_email", "committer_name", "committer_email")
    for f in fields:
        lines += ["commit.%s = commit.%s.replace(%r, %r)" % ((f, f) + p) for p in pairs]
print("\n".join(lines))
PY
}
blob_callback_file=$(mktemp "${TMPDIR:-/tmp}/llmgw-cb-blob.XXXXXX")
filename_callback_file=$(mktemp "${TMPDIR:-/tmp}/llmgw-cb-fn.XXXXXX")
commit_callback_file=$(mktemp "${TMPDIR:-/tmp}/llmgw-cb-commit.XXXXXX")
chmod 0600 "$blob_callback_file" "$filename_callback_file" "$commit_callback_file"
gen_callbacks blob > "$blob_callback_file"
gen_callbacks filename > "$filename_callback_file"
gen_callbacks commit > "$commit_callback_file"
for cb in "$blob_callback_file" "$filename_callback_file" "$commit_callback_file"; do
  [[ -s "$cb" ]] || { echo "❌ callback generation produced nothing: $cb" >&2; exit 1; }
done

echo "━━━ Step 3/5: Create and rewrite local mirror ━━━"
rm -rf "$mirror_dir"
git clone --mirror --no-hardlinks "$repo_root" "$mirror_dir"
# --replace-text 只改文本 blob、--replace-message 管提交/标签消息（实测：
# 00e12a579 的消息标题带着口令原文，仅 replace-text 时原样回公开镜像）；
# 二进制 blob 由上面生成的 --blob-callback 兜底。三者同表同改。
git -C "$mirror_dir" filter-repo --force --replace-text "$filter_replacements" \
  --replace-message "$filter_replacements" \
  --blob-callback "$(cat "$blob_callback_file")" \
  --filename-callback "$(cat "$filename_callback_file")" \
  --commit-callback "$(cat "$commit_callback_file")"
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

echo "━━━ Step 4.5/5: Prune mirror refs to the published set ━━━"
# 公开镜像是『阶段发布』面，只发布 main + tags。镜像 clone 是一次性副本，
# 这里删 ref 不碰源仓库；不修剪的话 push --mirror 会把工作树里并行会话的
# WIP 分支名（agent/* feat/* fix/* …）连同 refs/remotes/* 一起 spray 到
# 公开仓库。SYNC_ALL_BRANCHES=1 恢复全量 refs 行为（默认关闭）。
if [[ "${SYNC_ALL_BRANCHES:-0}" == "1" ]]; then
  echo "   SYNC_ALL_BRANCHES=1 — 保留全部 refs（不修剪）"
else
  while IFS= read -r ref; do
    git -C "$mirror_dir" update-ref -d "$ref"
  done < <(git -C "$mirror_dir" for-each-ref --format='%(refname)' \
    | grep -vE '^(refs/heads/main|refs/tags/)' || true)
  echo "   remaining refs:"
  git -C "$mirror_dir" for-each-ref --format='     %(refname)'
fi

if [[ $DRY_RUN -eq 1 ]]; then
  echo "━━━ Step 5/5: DRY RUN — would push to $github_url ━━━"
  exit 0
fi

echo "━━━ Step 5/5: Push to GitHub ━━━"
read -rp "   Push to $github_url? Type 'yes' to confirm: " confirm
[[ "$confirm" == "yes" ]] || { echo "   Aborted."; exit 1; }
git -C "$mirror_dir" push --mirror "$github_url"

echo "✅ Sync complete!"

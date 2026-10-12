#!/usr/bin/env bash
#
# sync-codeup-batched.sh — 把 codeup 的提交分批同步到本地分支，再推到 GitHub。
#
# 设计要点
#   1. 分批：按「天」把 codeup 独有的非 merge 提交切成一串批次（10-07 … 10-13）。
#      每批一次 cherry-pick 序列 + 一次提交，失败只回滚该批（--abort），
#      不影响已完成的批次。批次结果写进 report，便于定位是哪一天炸的。
#   2. 可续跑：每批成功后打一个 tag `sync/<date>-<n>`；重跑时检测到 tag 就跳过该批。
#   3. 可覆盖：批次内单个提交冲突时先试 `-X ours`（保本地冲突侧、吃 codeup 的其它改动），
#      仍失败则该批 abort 并继续下一批 —— 保证「能推进多少推进多少」，
#      失败批次集中到末尾汇总，由人决定怎么处理。
#   4. 不静默：所有 batch 的结果都打印，最后给出汇总表。
#
# 用法
#   scripts/sync-codeup-batched.sh --dry-run              # 只打印批次计划，不动 git
#   scripts/sync-codeup-batched.sh --date 2026-10-10      # 只处理某天
#   scripts/sync-codeup-batched.sh --max-batch-size 40     # 每批最多 40 个提交
#   scripts/sync-codeup-batched.sh --from-tag backup/x     # 从某个 ref 开始算差集
#   scripts/sync-codeup-batched.sh --no-push               # 只做本地同步，不推 GitHub
#
# 退出码
#   0 = 全部批次成功；1 = 有批次失败（已成功的不回滚）；2 = 参数/环境错误

set -uo pipefail

# ── 配置 ────────────────────────────────────────────────────────────────────
CODEUP_REMOTE="${CODEUP_REMOTE:-codeup}"
SOURCE_REF="${SOURCE_REF:-main}"
CODEUP_REF="${CODEUP_REF:-codeup/main}"
TARGET_REMOTE="${TARGET_REMOTE:-origin}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"
WORK_BRANCH="${WORK_BRANCH:-sync/codeup-$(date +%Y%m%d)}"
TAG_PREFIX="${TAG_PREFIX:-sync}"
MAX_BATCH_SIZE="${MAX_BATCH_SIZE:-60}"
DRY_RUN=0
PUSH=1
ONLY_DATE=""
FROM_REF=""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT" || exit 2

# ── 输出 ────────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_ERR=$'\033[31m'
  C_HEAD=$'\033[1;36m'; C_DIM=$'\033[2m'; C_OFF=$'\033[0m'
else
  C_OK=""; C_WARN=""; C_ERR=""; C_HEAD=""; C_DIM=""; C_OFF=""
fi
log()  { printf '%s\n' "$*"; }
head_() { printf '%s==>%s %s\n' "$C_HEAD" "$C_OFF" "$*"; }
warn() { printf '%s[warn]%s %s\n' "$C_WARN" "$C_OFF" "$*"; }
err()  { printf '%s[fail]%s %s\n' "$C_ERR" "$C_OFF" "$*" >&2; }
ok()   { printf '%s[ ok ]%s %s\n' "$C_OK" "$C_OFF" "$*"; }

# ── 参数解析 ────────────────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)        DRY_RUN=1 ;;
    --no-push)        PUSH=0 ;;
    --date)           ONLY_DATE="${2:-}"; shift ;;
    --max-batch-size) MAX_BATCH_SIZE="${2:-}"; shift ;;
    --from-tag)       FROM_REF="${2:-}"; shift ;;
    --branch)         WORK_BRANCH="${2:-}"; shift ;;
    -h|--help)        sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) err "未知参数: $1（--help 看用法）"; exit 2 ;;
  esac
  shift
done

case "$MAX_BATCH_SIZE" in
  ''|*[!0-9]*) err "--max-batch-size 必须是正整数，收到: $MAX_BATCH_SIZE"; exit 2 ;;
esac
[ "$MAX_BATCH_SIZE" -ge 1 ] || { err "--max-batch-size 必须 >= 1"; exit 2; }

git rev-parse --git-dir >/dev/null 2>&1 || { err "不在 git 仓库里: $REPO_ROOT"; exit 2; }

# ── 环境自检 ────────────────────────────────────────────────────────────────
if ! git remote | grep -qx "$CODEUP_REMOTE"; then
  err "remote '$CODEUP_REMOTE' 不存在。先加：git remote add $CODEUP_REMOTE git@codeup.aliyun.com:kaixuan/official-deploy/llm-gateway-go.git"
  exit 2
fi

if [ "$PUSH" = 1 ] && ! git remote | grep -qx "$TARGET_REMOTE"; then
  err "目标 remote '$TARGET_REMOTE' 不存在。--no-push 可只做本地同步。"
  exit 2
fi

head_ "fetch $CODEUP_REMOTE"
# 只拉 main 这一个 ref —— codeup 上有 ~100 个分支和一堆 tag，
# 全量 fetch 会把几十 MB 的对象一次性拉下来，而分批同步只需要 main。
if ! GIT_SSH_COMMAND='ssh -o BatchMode=yes -o ConnectTimeout=20' \
     git fetch "$CODEUP_REMOTE" "main" 2>&1 | sed 's/^/    /'; then
  err "fetch 失败：$CODEUP_REMOTE main"
  exit 2
fi

DIFF_BASE="$SOURCE_REF"
if [ -n "$FROM_REF" ]; then
  git rev-parse --verify -q "$FROM_REF" >/dev/null || { err "--from-tag 指向的 ref 不存在: $FROM_REF"; exit 2; }
  DIFF_BASE="$FROM_REF"
fi
git rev-parse --verify -q "$CODEUP_REF" >/dev/null || { err "找不到 ref: $CODEUP_REF"; exit 2; }
git rev-parse --verify -q "$DIFF_BASE"  >/dev/null || { err "找不到基准 ref: $DIFF_BASE"; exit 2; }

head_ "切到工作分支 $WORK_BRANCH"
if [ "$DRY_RUN" = 1 ]; then
  log "    (dry-run: 不实际切分支)"
else
  if git rev-parse --verify -q "$WORK_BRANCH" >/dev/null; then
    git checkout "$WORK_BRANCH" >/dev/null 2>&1 || { err "无法切到 $WORK_BRANCH"; exit 2; }
    warn "工作分支已存在，复用它（已完成的批次按 tag 跳过）"
  else
    git checkout -b "$WORK_BRANCH" "$DIFF_BASE" >/dev/null 2>&1 \
      || { err "无法从 $DIFF_BASE 建分支 $WORK_BRANCH"; exit 2; }
  fi
  [ -z "$(git status --porcelain)" ] || { err "工作树不干净，先 commit 或 stash 再跑"; exit 2; }
fi

# ── 取待同步提交 ────────────────────────────────────────────────────────────
# 只取非 merge 提交：merge 提交的内容已经在它们的父提交里，
# cherry-pick merge 只会重放一遍并制造冲突噪音。
COMMITS="$(git log --no-merges --reverse --format='%H' "$DIFF_BASE..$CODEUP_REF")"
TOTAL="$(printf '%s\n' "$COMMITS" | grep -c . || true)"
[ "$TOTAL" -gt 0 ] || { ok "没有需要同步的提交（${DIFF_BASE}..${CODEUP_REF} 为空）"; exit 0; }

head_ "待同步 $TOTAL 个非 merge 提交（${DIFF_BASE} → ${CODEUP_REF}）"

# ── 切批次 ──────────────────────────────────────────────────────────────────
# 先按 committer date 把提交按天聚类，天内再按 MAX_BATCH_SIZE 切成多块。
# 每批输出两行（用 \x1f 分隔字段）：
#   meta 行: M<US>day<US>seq<US>total<US>tag<US>first_idx<US>last_idx
#   sha  行: S<US><sha>
# 主循环直接按 meta 行给的 idx 区间取 sha，不再自己算切片 —— 避免两处逻辑
# 各算一遍导致的错位（这正是第一版 10-07 出现三个 #1 的原因）。
build_batches() {
  local tmp; tmp="$(mktemp)"
  # 打上日期，保留原始顺序（--reverse 已保证时间序）
  printf '%s\n' "$COMMITS" | while read -r sha; do
    [ -n "$sha" ] || continue
    printf '%s\t%s\n' "$(git log -1 --format='%cs' "$sha")" "$sha"
  done > "$tmp"

  awk -F'\t' -v max="$MAX_BATCH_SIZE" -v tagpfx="$TAG_PREFIX" -v us=$'\x1f' '
    BEGIN { idx = 0; day_n = 0; chunk = 0 }
    {
      day = $1; sha = $2
      if (day != cur_day) { cur_day = day; chunk = 0; day_n = 0 }
      day_n++
      idx++
      chunk++
      shas[day, chunk] = shas[day, chunk] (shas[day, chunk] == "" ? "" : "\n") sha
      count[day, chunk] = chunk
      last_idx[day, chunk] = idx
      if (chunk == max || $0 == "") { }
    }
    # 收尾：把每个 (day,chunk) 输出成 meta + sha
    END {
      for (d in seen_days) { }
    }
  ' "$tmp" 2>/dev/null || true
  rm -f "$tmp"
}

# 上面那版 awk 不好写死，改用纯 bash 做批次规划（352 个提交，bash 完全跑得动，
# 而且逻辑显式、没有 awk 数组/换行拼接的坑）。
plan_batches() {
  local -a days seqs starts counts ends tags
  local cur_day="" chunk=0 day_n=0 idx=0
  local d sha
  # 先把 (日期, sha) 读进数组
  local -a ent_day ent_sha
  while IFS= read -r sha; do
    [ -n "$sha" ] || continue
    ent_day+=("$(git log -1 --format='%cs' "$sha")")
    ent_sha+=("$sha")
  done < <(printf '%s\n' "$COMMITS")

  local n=${#ent_sha[@]} i=0
  local start=0 day_seq=0
  while [ $i -lt $n ]; do
    d="${ent_day[$i]}"
    # 同一天能装多少批
    chunk=0; start=$i; day_seq=0
    while [ $i -lt $n ] && [ "${ent_day[$i]}" = "$d" ]; do
      chunk=$((chunk + 1)); i=$((i + 1))
      if [ "$chunk" -ge "$MAX_BATCH_SIZE" ]; then
        day_seq=$((day_seq + 1))
        days+=("$d"); seqs+=("${#days[@]}"); counts+=("$chunk")
        starts+=("$start"); ends+=($((i - 1)))
        tags+=("${TAG_PREFIX}/${d}-$(printf '%02d' "$day_seq")")
        chunk=0; start=$i
      fi
    done
    # 当天收尾（最后不满一块的）
    if [ "$chunk" -gt 0 ]; then
      day_seq=$((day_seq + 1))
      days+=("$d"); seqs+=("${#days[@]}"); counts+=("$chunk")
      starts+=("$start"); ends+=($((i - 1)))
      tags+=("${TAG_PREFIX}/${d}-$(printf '%02d' "$day_seq")")
    fi
  done

  # 输出全局序号；tag 里用「当天内序号」更可读
  # 这里 seq 用全局序号保证 tag 唯一，简单可靠
  B_DAYS=("${days[@]}"); B_SEQS=("${seqs[@]}"); B_COUNTS=("${counts[@]}")
  B_STARTS=("${starts[@]}"); B_ENDS=("${ends[@]}"); B_TAGS=("${tags[@]}")
  B_COUNT="${#B_DAYS[@]}"
}

# ── 跳过已完成批次的判定 ────────────────────────────────────────────────────
batch_done() {
  local tag="$1"
  git rev-parse --verify -q "refs/tags/$tag" >/dev/null
}

# ── 执行一批 ────────────────────────────────────────────────────────────────
# 返回：0 = 成功（已提交+打tag）；1 = 失败（已 abort，分支回到批前状态）
run_batch() {
  local day="$1" seq="$2" total="$3" tag="$4"; shift 4
  local shas=("$@")
  local i

  head_ "批次 $day #$seq — $total 个提交"

  if git cherry-pick --no-commit "${shas[@]}" 2>&1 | sed 's/^/    /'; then
    : # no-commit 全套应用成功
  else
    warn "cherry-pick 冲突，尝试 -X ours（保留本地冲突侧）"
    git cherry-pick --abort >/dev/null 2>&1
    if git cherry-pick --no-commit -X ours "${shas[@]}" 2>&1 | sed 's/^/    /'; then
      warn "  -X ours 成功，但这条批次的冲突是人工 merge 出来的，需要复核"
    else
      git cherry-pick --abort >/dev/null 2>&1
      err "批次 $day #$seq 无法自动解决，abort；这一批留到最后人工处理"
      return 1
    fi
  fi

  # 有改动才提交（空提交会让批次 tag 指向同一个 tree，噪音）
  if git diff --cached --quiet && git diff --quiet; then
    warn "批次 $day #$seq 应用后无净改动，跳过提交"
    return 0
  fi

  local n_msg
  n_msg=$(printf '%s\n' "${shas[@]}" | wc -l | tr -d ' ')
  git commit -q -m "sync(codeup): 批次 $day #$seq — $n_msg 个提交（${CODEUP_REF} → ${SOURCE_REF}）

由 scripts/sync-codeup-batched.sh 分批生成。
范围：${shas[0]}..${shas[$((n_msg-1))]}" 2>&1 | sed 's/^/    /'

  if [ "$DRY_RUN" = 1 ]; then return 0; fi

  git tag -f "$tag" >/dev/null
  ok "批次 $day #$seq 完成 → $tag"
  return 0
}

# ── 主流程 ──────────────────────────────────────────────────────────────────
FAILED_BATCHES=()
OK_BATCHES=()

plan_batches

head_ "批次计划（$B_COUNT 批，共 $TOTAL 个提交）"
b=0
while [ $b -lt "$B_COUNT" ]; do
  printf '    %s  #%s  %3s 个提交  → %s\n' \
    "${B_DAYS[$b]}" "${B_SEQS[$b]}" "${B_COUNTS[$b]}" "${B_TAGS[$b]}"
  b=$((b + 1))
done

if [ "$DRY_RUN" = 1 ]; then
  log ""
  ok "dry-run 结束，没有改动任何 ref"
  exit 0
fi

# 按 sha 数组顺序取某一批
batch_shas() {
  local start="$1" end="$2" i
  for ((i = start; i <= end; i++)); do
    printf '%s\n' "${ENT_SHA[$i]}"
  done
}

# 重建 ent_sha/ent_day（plan_batches 内部用的局部数组，这里需要全局可见的）
ENT_SHA=()
ENT_DAY=()
while read -r sha; do
  [ -n "$sha" ] || continue
  ENT_SHA+=("$sha")
  ENT_DAY+=("$(git log -1 --format='%cs' "$sha")")
done < <(printf '%s\n' "$COMMITS")

b=0
while [ $b -lt "$B_COUNT" ]; do
  day="${B_DAYS[$b]}"; seq_="${B_SEQS[$b]}"; cnt="${B_COUNTS[$b]}"
  tag="${B_TAGS[$b]}"; s="${B_STARTS[$b]}"; e="${B_ENDS[$b]}"

  if [ -n "$ONLY_DATE" ] && [ "$day" != "$ONLY_DATE" ]; then
    b=$((b + 1)); continue
  fi
  if batch_done "$tag"; then
    printf '%s[skip]%s 批次 %s #%s 已完成（tag %s）\n' "$C_DIM" "$C_OFF" "$day" "$seq_" "$tag"
    b=$((b + 1)); continue
  fi

  # 取这一批的 sha
  chunk=()
  for ((i = s; i <= e; i++)); do chunk+=("${ENT_SHA[$i]}"); done

  if run_batch "$day" "$seq_" "$cnt" "$tag" "${chunk[@]}"; then
    OK_BATCHES+=("$day #$seq_ ($cnt) → $tag")
  else
    FAILED_BATCHES+=("$day #$seq_ ($cnt) → $tag")
  fi
  b=$((b + 1))
done

head_ "汇总"
log ""
ok "成功批次: ${#OK_BATCHES[@]}"
for x in "${OK_BATCHES[@]:-}"; do [ -n "$x" ] && log "    $x"; done
if [ "${#FAILED_BATCHES[@]}" -gt 0 ]; then
  err "失败批次: ${#FAILED_BATCHES[@]}"
  for x in "${FAILED_BATCHES[@]:-}"; do [ -n "$x" ] && log "    $x"; done
fi

# ── 推送 ────────────────────────────────────────────────────────────────────
if [ "$PUSH" = 1 ]; then
  if [ "${#FAILED_BATCHES[@]}" -gt 0 ]; then
    warn "有失败批次，先不推。你可以用 --date <失败日期> 单独重跑那批，或人工处理后再推。"
    exit 1
  fi
  head_ "push $TARGET_REMOTE $TARGET_BRANCH ← $WORK_BRANCH"
  if git push "$TARGET_REMOTE" "HEAD:refs/heads/$TARGET_BRANCH" 2>&1 | sed 's/^/    /'; then
    ok "推送完成"
  else
    err "推送失败（可能 ${TARGET_REMOTE}/${TARGET_BRANCH} 已经被别人推过，先 fetch 看看）"
    exit 1
  fi
fi

log ""
if [ "${#FAILED_BATCHES[@]}" -gt 0 ]; then
  err "结束：有 ${#FAILED_BATCHES[@]} 个批次失败"
  exit 1
fi
ok "结束：全部批次已处理"
exit 0
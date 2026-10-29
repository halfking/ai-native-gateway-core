#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/apply-db-revision-sequence.sh"

bash -n "$SCRIPT"
python3 "$ROOT_DIR/scripts/apply-db-revision-sequence_runtime_test.py"

# ── 失败收集器（2026-10-05 审计 §9.253，P1 可用性修复）────────────────────────
#
# 这条门此前有 17 个 `exit 1` 检查点，每个都**只报首个失败就退出**。
# 后果在 R44/R45 两轮里被完整记录下来：门红在 829，于是「829 是唯一问题」
# 被写进交接文档；修完 829，门换一张牌红在 819；再修，又红在 830……
# 四层既有缺口被逐层揭开，**每一层都以为自己是全部**。
#
# 判据本身一直是对的，错的是**可观测性**：一次只给一个失败点，
# 迫使每一轮审计只能揭一层。
#
# 现在：能累积的检查点累积成列表，最后一次报全，退出码语义不变
# （仍有 clobber 违规 ⇒ 5；否则有任意其它失败 ⇒ 1；全通过 ⇒ 0）。
# 「提取锚点漂移」这类**后续判据不可信**的硬失败仍立即退出——
# 那不是「被观测对象有问题」，是「观测手段坏了」，两者处置方向相反。
GATE_FAILURES=()
GATE_SAW_CLOBBER=0

gate_fail() { GATE_FAILURES+=("$1"); }

gate_report() {
  if (( ${#GATE_FAILURES[@]} == 0 )); then
    return 0
  fi
  printf '\napply-db-revision-sequence contract FAILED: %d problem(s) found (all reported at once)\n' \
    "${#GATE_FAILURES[@]}" >&2
  local i=1 f
  for f in "${GATE_FAILURES[@]}"; do
    printf '  [%d] %s\n' "$i" "$f" >&2
    i=$((i+1))
  done
  return 1
}

# canonical_delivery_path_check enforces the R30 migration-channel invariant:
# high-numbered canonical migrations must reach either a fresh install, an
# upgrade, or a reviewed Go startup ensure. The inputs stay text lists so the
# contract can exercise every route without mutating the real catalog.
#
# ═══════════════════════════════════════════════════════════════════════
# ★★ 读这条门之前请先读完本段：仓库里其实有**两条**迁移投递腿，本门只认其中一条
# ═══════════════════════════════════════════════════════════════════════
#
# ① **扫描腿**（部署时）
#    ⚠⚠ 它的代码**不在本仓库里**：`scripts/deploy-lib` 是**软链**，指向仓外
#       workspace SSOT `ai-native-tools/deploy-lib`（commit 722e9020f 的治理决定，
#       未跟踪、非 gitignore）。**换机器或在 CI runner 上这个路径不存在。**
#       ⇒ 下面这个路径是**本机 workspace 的状态**，不是本仓能自证的事实。
#       引用它之前先 `git ls-files scripts/deploy-lib/`（会返回空）。
#
#    本机可达时的实际逻辑（scripts/deploy-lib/db-changelog.sh 的
#    `_deploy_pending_startup_migrations`）：
#      for f in sql/migrations/startup/[0-9]*.sql; do
#        跳过 *.down.sql / *.skip / *.bak.skip / 头 15 行含 SUPERSEDED|DEPRECATED
#        if (( ver >= ${DB_LEDGER_RECONCILE_FROM:-412} )) 且远端 schema_migrations
#           没有该 version ⇒ 投递
#      done
#    投递后由同文件 `:518` 写 `schema_migrations` + `llm_gateway_migration_checksums`
#    （advisory lock + NOT EXISTS 幂等）。调用链（本机查实）：
#      deploy-154.sh → deploy-seamless.sh:68 `source deploy-lib/db-changelog.sh`
#
# ② **通道腿**（本门要求的那条）
#    scripts/apply-db-revision-sequence.sh 的 `files=(...)` 数组，**在本仓内**，
#    记账表是 **`public.gateway_db_revision_sequences`**（按文件内容 sha256，
#    内容变了但编号不变时会重放）。
#    在仓内的调用方是 `scripts/deploy-local.sh`（迁移腿，deploy 前应用 revision 轨道）。
#    历史调用方 `scripts/deploy-local-sys.sh` / `scripts/deploy-252-schema-upgrade.sh`
#    已随 2026-10-14 deploy 脚本清理轮删除（前者属已停用的 launchd 代际，
#    后者是一次性 252 schema 追平工具且全仓穷尽 grep 后无调用方——2026-10-07 核过
#    scripts/deploy/installer/.github/.githooks/Makefile/根目录 *.sh）。
#
# ★ 因此本仓能自证的只有一句：**通道腿在本仓内，且有调用方（deploy-local.sh）**。
#   「154 生产上谁投递迁移」依赖仓外 SSOT（见上），**本仓无法回答** ——
#   需要那个结论时请去 workspace SSOT 查，不要引用本文件当依据。
#
# ── 两腿互不知情，各记一张表 ─────────────────────────────────────────
# `deploy-lib` 里 grep 不到 apply-db-revision-sequence；通道脚本里也读不到
# deploy-lib。⇒ **同一份迁移可能被两条腿各投递一次**，挡住重复的是
# **迁移自身的幂等性**，不是记账。这也解释了 836 头部那句
# 「813 的 sha 与台账一致 ⇒ 幂等通道每次都跳过它」——通道腿按 sha 跳，
# 而扫描腿根本不查那张表。
#
# ── 为什么本门**不**把扫描腿算作一条投递路径（2026-10-07 人工拍板）────
# 2026-10-07 在**本机**实测确认扫描腿会投递 837-844（用它的真实循环逻辑跑过：
# 已记账的 844 被正确跳过，未记账的 837-843 被选中）。
# ⚠ 该读数依赖仓外 SSOT 软链，**别的环境无法复现**（见上 ⚠⚠）。
# **所以：投递本来没坏。** 本门坚持要求显式登记，是在问一个扫描腿回答不了的问题：
#
#   「这条迁移的投递**顺序、幂等重放、以及它是否属于人工/带外交付**，
#     在仓库里有没有被明确记录下来？」
#
# 扫描腿只回答「文件在目录里就会投」——它**说不出**这条迁移为什么该在
# 这里、顺序依赖谁、重放时安全吗。**838→839 的函数链先后**就是只有通道腿
# 能表达的信息（intentional_function_chains 就是为此存在）。
#
# ⇒ 保持本门的约束力（本轮拍板）；代价是**每个新迁移要在通道腿登记一次**，
#   即使扫描腿也会投它。这是有意的代价，不是缺陷。
# ⇒ ⚠ 若日后要让本门认扫描腿，必须同时解决「顺序依赖」问题 ——
#   否则一个必须排在 838 之后的迁移，会因为「它在目录里」而被允许乱序投递。
#   届时应改的是扫描腿（读顺序元数据），不是把第五条路径加进来。
canonical_delivery_path_check() {
  local canonical_files="$1"
  local startup_files="$2"
  local sequence_files="$3"
  local ensure_files="$4"
  local superseded_files="${5:-}"
  local channel_gap_files="${6:-}"
  local name version found=0

  while IFS= read -r name; do
    [[ "$name" == *.down.sql ]] && continue
    [[ "$name" =~ ^[0-9]{3}_.*\.sql$ ]] || continue
    version=${name%%_*}
    [[ "$version" < "690" ]] && continue

    if grep -Fxq "$name" <<<"$startup_files" \
      || grep -Fxq "$name" <<<"$sequence_files" \
      || grep -Fxq "$name" <<<"$ensure_files" \
      || grep -Fxq "$name" <<<"$superseded_files" \
      || grep -Fxq "$name" <<<"$channel_gap_files"; then
      continue
    fi

    # Printed to stdout (not stderr) and NOT terminated with a return: the
    # caller captures this stream and folds every line into its own numbered
    # report, so returning here would hide the 2nd..Nth uncovered migration.
    printf 'canonical startup migration %s has no approved delivery path; register it in StartupFiles, the revision sequence, the reviewed Go-ensure allowlist, (installer-only by design) channel_gap_allowlist, or (overturned, must never run) superseded_migrations\n' "$name"
    found=1
  done <<<"$canonical_files"
  return $found
}

# Matching the first line of a large catalog must not become a false miss.
# grep -q exits before printf drains; pipefail then sees the producer's SIGPIPE.
large_delivery_list=$(awk 'BEGIN { print "690_large_catalog_member.sql"; for (i=0;i<30000;i++) print "999_padding_migration_" i ".sql" }')
if ! canonical_delivery_path_check "690_large_catalog_member.sql" "$large_delivery_list" "" "" "" "" >/dev/null; then
  gate_fail "delivery path guard rejected a present migration in a large catalog (early grep exit / producer SIGPIPE)"
fi
unset large_delivery_list

# Keep the helper independently regression-tested: a future edit must preserve
# all three delivery paths and continue to reject an uncovered >=690 file.
canonical_delivery_path_check \
  $'690_fresh.sql\n691_upgrade.sql\n692_ensure.sql\n693_superseded.sql\n694_installer_only.sql' \
  '690_fresh.sql' \
  '691_upgrade.sql' \
  '692_ensure.sql' \
  '693_superseded.sql' \
  '694_installer_only.sql' \
  || {
    # 这五类投递路径全部已覆盖，这里本不该失败。裸调用在 `set -e` 下会
    # **静默**死掉（无任何输出、退出码非零）——那正是「校验器失败」与
    # 「校验不通过」难以区分的来源。显式接住并说明是哪一步。
    printf 'canonical delivery-path guard rejected a migration that IS covered by one of the five delivery paths\n' >&2
    printf '    (self-test: fresh/upgrade/ensure/superseded/installer-only must all be accepted)\n' >&2
    exit 1
  }
orphan_output=""
if orphan_output=$(canonical_delivery_path_check '690_orphan.sql' '' '' '' '' '' 2>&1); then
  printf 'canonical delivery-path guard accepted orphaned migration\n' >&2
  exit 1
fi
printf '%s\n' "$orphan_output" | grep -Fq '690_orphan.sql' || {
  printf 'canonical delivery-path guard did not identify the orphaned migration\n' >&2
  exit 1
}

# ★ 自测：多个缺口必须**一次报全**（§9.253 的承重判据）
#
# 旧行为是 `return 1`（首个失败即退出），所以「门只红在一条」这句话在四轮
# 交接里被当成「只有一条问题」，实际每轮只揭开一层。
#
# 这条断言就是本轮修复的负控：若有人把 `found=1; return $found` 改回
# `return 1`，下面这三行会立刻转红。
multi_output=""
if multi_output=$(canonical_delivery_path_check \
    $'690_orphan_a.sql\n691_orphan_b.sql\n692_orphan_c.sql' '' '' '' '' '' 2>&1); then
  printf 'canonical delivery-path guard accepted three orphaned migrations\n' >&2
  exit 1
fi
for orphan in 690_orphan_a.sql 691_orphan_b.sql 692_orphan_c.sql; do
  grep -Fq "$orphan" <<<"$multi_output" || {
    printf 'canonical delivery-path guard stopped at the first failure: %s was never reported\n' "$orphan" >&2
    printf '    (this is the exact "one failure at a time" behaviour §9.253 removed)\n' >&2
    exit 1
  }
done

# 收集器自身的负控：gate_fail 累积、gate_report 非零返回、且不吞条目。
collector_probe=$(bash -c '
  set -euo pipefail
  GATE_FAILURES=()
  GATE_SAW_CLOBBER=0
  gate_fail() { GATE_FAILURES+=("$1"); }
  '"$(sed -n '/^gate_report() {/,/^}/p' "$BASH_SOURCE")"'
  gate_fail "first synthetic failure"
  gate_fail "second synthetic failure"
  if gate_report >/dev/null 2>&1; then
    printf "COLLECTOR_DROPPED_SIGNAL"
    exit 0
  fi
  printf "%s" "${#GATE_FAILURES[@]}"
')
if [[ "$collector_probe" != "2" ]]; then
  printf 'failure collector is not accumulating: expected "2", got "%s"\n' "$collector_probe" >&2
  exit 1
fi

for required in \
  "655_session_summaries_schema_reconcile.sql" \
  "560_session_summaries_tenant_uniqueness.sql" \
  "572_session_summary_large_token_ratio.sql" \
  "606_session_summaries_agent_expert_tags.sql" \
  "563_session_summary_trigger_on_hot.sql" \
  "564_session_summary_backfill_safe.sql" \
  "644_tuning_views_selfcheck_and_candidate_failure_cache.sql" \
  "645_session_bodies_hot_request_unique_repair.sql" \
  "656_auto_route_selections_hot.sql"; do
  test -f "$ROOT_DIR/sql/migrations/startup/$required"
done

# Keep the migration sequence explicit in the executable so deployment cannot
# silently fall back to numeric directory ordering. Do not use a fixed line
# window here: the sequence is intentionally append-only and has grown beyond
# the original 40-line contract fixture.
sequence=$(awk '/^files=\(/{inside=1} inside{print} inside && /^\)/{exit}' "$SCRIPT")
# R16 (2026-09-12): the 693/699-class channel gaps happened because entries
# newer than V371 were outside this self-test — required list now runs to the
# current top of the startup track. 665 and 687-692 are intentional sequence
# gaps (out-of-band ledger entries / installer-only channel; documented in the
# sequence comments), so they stay excluded here by design.
for required in 655 560 572 606 563 564 644 645 650 651 652 653 654 656 659 660 661 662 663 664 V371 \
                666 667 668 669 670 671 672 673 674 675 676 677 678 679 680 681 682 683 684 685 \
                686 693 694 695 696 697 698 699 700 701 703 744 745 746 756 757 758 800; do
  grep -q "${required}_" <<<"$sequence" || \
    gate_fail "missing sequence entry: ${required}"
done

# 691/692: R16-documented installer-only window ("687-692 intentional sequence
# gaps"). 747/748/759: same installer-only class — existing databases received
# them out-of-band / via their own runtime ensure; kept exact so the next
# member of the class is a decision, not a recurrence.
#
# 830 (R44): **manual-by-design**, and NOT the same class as 747/748/759.
# Those three were received out-of-band; 830 is a migration that *cannot* be
# automated at all — its RENAME + CREATE PARENT TABLE half renames a live
# 10GB+ table instead of copying rows, so an unattended upgrade must not run it.
# db.ensureURSMNodeSnapshotMinDailyPartition mirrors only the post-migration
# half and probes (to_regprocedure) rather than erroring, keeping the two steps
# order-independent. The no-registration decision is pinned by
# bg/partition_830_contract_test.go Test830IsDeliberatelyNotInTheAutoStartupSequence.
# ⚠ This is the **channel-side copy** of the same decision; the Go-side copy is
#   manualByDesign in installer/cmd/llm-gw-installer/stats_migrations_test.go.
#   Editing one without the other leaves the other gate red — "已豁免" must hold
#   on both sides at once (single-side green is not green).
# 842 —— 本条目曾在本清单里，**已于同日撤除**（记录留痕，避免下一个读到这个
# 历史的人以为它还生效）。
#
# 2026-10-07 20:36 人工拍板后，本会话一度把 842 登记为 installer-only
# （当时的读数：只有 channel_gap_allowlist 这一条路能让门转绿）。
# 20:41 合并 origin/main 时发现：**并行线已自己把它登记进
# `scripts/apply-db-revision-sequence.sh` 的 files=(...) 正常升级通道**
# （随迁移 843_candidate_failure_logs_ts_desc_idx 一起，理由写的是
# 「部署扫描腿本就会按目录+台账投递，登记是元数据补全」）。
#
# ⇒ 处置归属权归作者，且**两个清单不能并存**（门只认「在任一处」，
#   但一个说「库已经有了」、另一个说「它会被升级通道投递」，语义相反）。
#   撤除本侧登记，保留作者的通道登记。
#
# ★ 事实校准（留下给下一个读到 842 的人，它**不因撤除本条目而失效**）：
#   · 842 建索引的对象是 **分区父表 public.credential_model_index**（3 分区，
#     354,153 行）与 **普通堆表 credential_model_index_hot**（2,361 行）。
#     ⚠「356,514 行」是那个 UNION ALL **视图**的行数，不是任何一张表的行数 ——
#     父表自身不存数据（`pg_total_relation_size(<父表>)` 返回 0），体量要逐分区求和。
#   · 它自己的头注明：不支持在分区父表上 CREATE INDEX CONCURRENTLY，
#     普通建索引会对每个分区取锁直到建完。
#   · 全仓 grep `credential_model_index_cred_model_bucket_idx` 只命中它自己的
#     `.sql` 与 `migration_842_test.go` ⇒ **没有 Go ensure 镜像**，
#     所以它不属于 `manualByDesign` 那一类（该类要求 Go 侧镜像后半段）。
#
# ✔ 更正（2026-10-07 20:50）：本会话一度在这里写过一条「扫描腿不存在」的
#   实测反证。**那条反证是错的，已撤除。**
#   扫描腿**确实存在**：`scripts/deploy-lib/db-changelog.sh:241`
#     for f in sql/migrations/startup/[0-9]*.sql; do … schema_migrations 台账 …
#   （`deploy-154.sh` 的头也写着「切换前 DB 迁移 + db-changelog」）
#   ⇒ **作者那条理由是对的。**
#   我错在只 grep 了 `scripts/` 顶层，漏掉 `deploy-lib/` 这个子目录，
#     拿一条覆盖面不足的搜索去否定一个全集。
#
# ★★ 留这条更正的用意不是「记录我错过」，而是：
#   这个文件里关于**投递机制**的每一条陈述都会被人当依据读。
#   一条「已确认不存在」比「没查到」危险得多 —— 它会让下一个人
#   直接采信并停止查证。⇒ 撤除错误陈述，并把它的成因留在这里。
#
# ✔ 更正二（2026-10-07 23:53）：上面这条更正**自身也不完整** ——
#   `scripts/deploy-lib/db-changelog.sh` 不是仓内文件，而是**软链**，指向仓外
#   workspace SSOT（`ai-native-tools/deploy-lib`，commit 722e9020f 的治理决定，
#   `git ls-files scripts/deploy-lib/` 返回空）。
#   ⇒ 「扫描腿确实存在」这句话**只在本机成立**：换机器或 CI runner 上
#     该路径根本不存在，那时本文件的这一整段都无法自证。
#   ⇒ 把它读成「本仓能证明扫描腿存在」是错的。
#   要判断 842/843 在别的环境会不会被投递，**必须去 workspace SSOT 查**。
#   判别动作：引用部署机制前先 `git ls-files <路径>`，
#     不要用 `ls` 看到文件就当它是仓内资产 —— 软链让两者看起来完全一样。
#
# ★ 两条投递腿**彼此独立**（都不是对方的子集）：
#   ① 扫描腿  scripts/deploy-lib/db-changelog.sh（★ 仓外软链 SSOT）
#              —— 扫目录 + 查台账，未记录即投递
#   ② 通道腿  本脚本 files=(...) 数组（★ 仓内）—— 显式列出才投递
#   ⇒ 所以「登记 files=(...)」与「反正扫描腿会投递」**两句话都对**，
#     而它们说的是两件不同的事。（这正是最初那条冲突的根因。）
#
channel_gap_allowlist=$(cat <<'EOF'
691_proxy_region_policy.sql
692_session_summaries_user_intent_widen.sql
747_session_mirror_outbox_source_claim.sql
748_selfcheck_system_key_tier.sql
759_report_snapshots_grain_dims.sql
830_ursm_node_snapshot_min_partitioned.sql
# ★ 890（2026-10-21，§143.53）：**out-of-band / installer-only 交付**。
#   理由：890 自带**非空闸**（v1 族有行就 RAISE）⇒ **天然不可自动重放**
#   ⇒ 躺在「每次部署都跑」的通道数组里 ⇒ 自动通道每次部署都撞闸
#   ⇒ 迁移腿立即中止（set -euo pipefail + ON_ERROR_STOP=1）
#   ⇒ 且它后面的 891/892 永远上不去（890 在通道数组里倒数第二）。
#   ⚠ 它**不是** superseded（不是「绝不可跑」）：新装库由 installer 腿交付；
#     升级库由**显式的、属主授权的**退役步骤交付
#     ⇒ scripts/retire-request-logs-v1-family.sh（--preflight / --defer /
#       --execute / --undo-defer）。
#   ⇒ 本文件只登记「交付路径」；通道数组里那一行已同步注释掉。
890_drop_request_logs_v1_family.sql
EOF
)

# ── A third class, distinct from both lists above (2026-10-05) ──
#
# channel_gap_allowlist means "delivered out-of-band / installer-only": the
# database HAS it and something else applied it. ensure_allowlist means "the Go
# boot ensure chain applies it before traffic". Neither describes a migration
# that must **never** run anywhere.
#
# 819_request_abandoned.sql is that third thing. Its own header records the
# reason: the independent request_abandoned table was overturned in favour of
# the 820/821 line (821 marks is_abandoned on session_turns instead), the Go
# write path (markRequestAbandonedPending / clearRequestAbandonedPending) no
# longer exists, and production *.go has **zero** references to the table. The
# file was restored from the docs/db-changelog.md ledger SHA purely so the
# migration-checksum gate keeps verifying it.
#
# Registering it in the channel array would create a table nothing writes to
# and nothing reads — a dead surface that looks live in `\d\dt+request_abandoned`.
# Listing it as "installer-only" would be a false claim about an installation
# that does not have it. So it gets its own list, named for what it is.
#
# ⚠ 2026-10-05: this class exists because registering 829 (Owner decision) let
# the gate run past its first failure and reach 819, which had been invisible
# behind it. 819 was already unregistered on origin/main — this is a pre-existing
# gap that the 829 fix unmasked, not a regression introduced by it.
superseded_migrations=$(cat <<'EOF'
819_request_abandoned.sql
EOF
)
# Directory-driven channel invariant (2026-09-14 audit F-P2-1): the highest-
# numbered startup migration file MUST be present in the channel files=(
# ...) array. The trailing-sequence guard in migration_700_test.go is a
# hardcoded point-fix (700/701/703); without this check a parallel line can
# land 704_NNN.sql with full installer sync but never register the channel
# entry — the exact 693/699/701/703 recurrence shape — and every gate stays
# green while upgraded databases never reach 704.
#
# ★ R44 修正：这条判据**默认「最高编号必然走通道」**，而 channel_gap_allowlist
#   里的迁移是**按设计就不该进通道**的（installer-only / manual-by-design）。
#   于是 830 一落地，「最高编号」变成 830，而 830 有意不在 sequence 里
#   ⇒ 本门**永久红**，且红得毫无信息量（它想说的是「有个新迁移忘了登记」，
#   实际发生的是「有个新迁移被正确地登记为不登记」）。
#   这与本仓 224 号那次的教训同型：**判据把「不被检查的对象」也算进去**。
#
#   修法：算 top 时**排除 channel_gap_allowlist 成员**——「最高编号」应当是
#   **本该被登记的那批里的最高编号**。豁免名单因此从"给穷举检查用"升格为
#   "参与定义检查范围"，它已在上面（为了单一事实源）前移到本检查之前。
#   判别力不变：对任何**未**豁免的新迁移，本门照样会在它成为最高编号时报红。
top_startup=$(ls "$ROOT_DIR"/sql/migrations/startup/*.sql 2>/dev/null \
  | grep -v '\.down\.sql$' \
  | while IFS= read -r f; do
      b=$(basename "$f")
      if grep -Fxq "$b" <<<"$channel_gap_allowlist"; then
        continue   # 按设计不进通道，不参与「最高编号」评选
      fi
      printf '%s\n' "$f"
    done \
  | sed -E 's#.*/([0-9]{3})_.*#\1#' | sort -n | tail -1)
if [[ -n "$top_startup" ]]; then
  grep -q "${top_startup}_" <<<"$sequence" || \
    gate_fail "startup migration ${top_startup} exists but is missing from the channel files=(...) array"
fi

# ── Reverse direction + full-coverage (2026-10-03 23:5x, audit §9.92.7d) ──
#
# The check above is `tail -1`, so it only ever looks at the HIGHEST number.
# That is enough to catch "a new migration was added but never registered",
# and **blind to the other direction**: a registration that points at a file
# which no longer exists.
#
# That second direction is not hypothetical. Removing migration 819 (the
# request_abandoned table, overturned in favour of migration 820) left this
# array pointing at a deleted path, and `apply-db-revision-sequence.sh:1000`
# answers a missing file with `exit 4` — so every 252/154 upgrade run would
# have died before applying anything. Nothing in the Go gates, the installer
# tests, or the check above can see that: the file list is a **data** array,
# so "nothing calls it" and "something references it" look identical to an
# identifier-based scan.
#
# Two independent facts are asserted here, in both directions:
#   (1) every path named in the channel array must exist on disk;
#   (2) every numbered startup migration must appear in the array, not just
#       the highest one.
#
# ⚠ The extraction MUST skip commented-out entries. The channel array carries
# at least one deliberately disabled registration (666, abandoned 2026-09-06
# in favour of 667, and the file was deleted in the same move). A naive
# "find the quoted path" extraction reads that comment as a live registration
# and reports a file that was intentionally removed — the first version of this
# check did exactly that, and the honest reading of its output was "a
# pre-existing broken reference", which was wrong.
#
# The test applied is: the opening quote of the path literal must not be
# preceded by a '#' anywhere earlier on the line.
while IFS= read -r ref; do
  [[ -n "$ref" ]] || continue
  if [[ ! -f "$ref" ]]; then
    gate_fail "channel files=(...) references a migration that does not exist: ${ref} (apply-db-revision-sequence.sh exits 4 on a missing migration, so this breaks every upgrade run; if this entry is intentionally disabled, comment the line out — a commented entry is not checked)"
  fi
done < <(grep -v '^[[:space:]]*#' "$SCRIPT" \
         | sed -nE 's#.*\$ROOT_DIR/(sql/migrations/startup/[^"]+)"#\1#p' \
         | while read -r rel; do printf '%s/%s\n' "$ROOT_DIR" "$rel"; done)

# NOTE on (2) "every migration must appear in the array": that direction is
# **already covered** further down this file (the `canonical_files` loop around
# line 226, which accepts sequence ∪ ensure_allowlist ∪ channel_gap_allowlist,
# bounded at >= 690 because 690-and-below has a long history of intentional
# installer-only gaps — 691/692/747/748/759 are all on that allowlist).
#
# A first draft of this block tried to add its own full-coverage check over the
# whole numbered range and reported 000_base_tables, then 535, as gaps. Both
# were wrong: 000-533 is base-schema history an upgraded database has already
# applied, and 535 travels a different channel (db.go ensure, not this array).
# Duplicating an existing gate with a narrower understanding of its scope is
# how a guard starts reporting fiction.
#
# What was genuinely missing is only the **reverse** direction above: a
# registration that points at a file which no longer exists. Nothing else here
# can see that, because the channel list is a data array.

# R30 canonical delivery-path gate: prevent the post-690 startup catalog from
# drifting outside every real installation/upgrade/self-heal channel. The
# allowlist is deliberately exact: each entry has a reviewed db.go ensure and
# focused parity coverage, so adding a migration demands an explicit decision.
startup_files=$(grep -E '^[[:space:]]*"[0-9]{3}_.*\.sql",?$' "$ROOT_DIR/installer/internal/dbinit/runner.go" \
  | sed -E 's/^[[:space:]]*"([0-9]{3}_[^"]+)".*/\1/' \
  | sort -u)
sequence_files=$(printf '%s\n' "$sequence" \
  | sed -nE 's#^[[:space:]]*"\$ROOT_DIR/sql/migrations/startup/([0-9]{3}_[^"]+)".*#\1#p' \
  | sort -u)
# 820: mirrors db.ensureAudioModalityBackfill (db/db.go:641), called from the
# ensure chain at db/db.go:624 **before traffic**, idempotent (WHERE
# modality='text'/'vision' guards). Registering it in StartupFiles instead
# would run the same backfill a second time on every install. The matching
# reviewed exemption is installer/cmd/llm-gw-installer/stats_migrations_test.go
# (goEnsureMirrored), which is the Go-side statement of the same decision.
ensure_allowlist=$(cat <<'EOF'
690_session_summaries_archived_ttl_index.sql
704_plan_quota_probe_backoff.sql
709_work_type_route_coverage.sql
715_route_incidents_pending_state.sql
820_audio_modality_backfill.sql
EOF
)
# ★ R82 轮（2026-10-22）：这一行原先是 `find -maxdepth 1` ⇒ **子目录 `up/` 里的
#   14 条迁移对整条投递路径规则完全隐形**（同一个病的第二例：枚举面被目录层级
#   而不是被规则定义）。⇒ 去掉 -maxdepth，改为**递归**穷举。
#   ⚠ 无实害（up/ 里 14 条全部 <690，规则不适用）—— 但「今天无实害」不等于
#   「明天仍无实害」：将来落进 up/ 的 ≥690 迁移同样会静默不投递。
canonical_files=$(find "$ROOT_DIR/sql/migrations/startup" -type f -name '[0-9][0-9][0-9]_*.sql' \
  ! -name '*.down.sql' ! -name '755_drop_dead_cleanup_expired_session_turn_logs.sql' -exec basename {} \; | sort)
# 755 contains its own BEGIN/COMMIT and drops a legacy function. It is
# intentionally excluded from both the installer's single transaction and
# this automatic upgrade sequence; an operator must first check external
# pg_cron/jobs as its migration header requires. Keep this exception exact.
test -f "$ROOT_DIR/sql/migrations/startup/755_drop_dead_cleanup_expired_session_turn_logs.sql"
grep -Fq '755_drop_dead_cleanup_expired_session_turn_logs.sql' "$ROOT_DIR/scripts/migrate-db-kaixuan1.sh"
grep -Fq '755_drop_dead_cleanup_expired_session_turn_logs.sql' "$ROOT_DIR/scripts/deploy-lib.legacy/db-changelog.sh"
grep -Fq '755_drop_dead_cleanup_expired_session_turn_logs.sql' "$ROOT_DIR/scripts/init-local-db.sh"
grep -Fq '755_drop_dead_cleanup_expired_session_turn_logs.sql' "$ROOT_DIR/scripts/local-deploy-test.sh"
# `set -e` would abort here on the first unregistered migration, which is
# exactly the "one failure at a time" behaviour this gate is being fixed for.
# The helper now reports EVERY uncovered migration before returning non-zero,
# so capture its output and fold it into the collected list.
delivery_path_output=""
if ! delivery_path_output=$(canonical_delivery_path_check "$canonical_files" "$startup_files" "$sequence_files" "$ensure_allowlist" "$superseded_migrations" "$channel_gap_allowlist" 2>&1); then
  while IFS= read -r line; do
    [[ -n "$line" ]] && gate_fail "$line"
  done <<<"$delivery_path_output"
fi

# ── R82/R83 轮（2026-10-22）：投递**视野**补齐 —— 两个宇宙 ＋ 效果落点核对 ─────────
#
# ★★ 本块要修的缺陷（**实测复现**，不是推演）：
#   上面那道 canonical_delivery_path_check 与 installer 的强制 embed 门，
#   **两道**都用「文件名里能不能解析出一个纯整数」来回答
#   「这条 startup 迁移在自己的视野里吗」：
#     · 本门：      [[ "$name" =~ ^[0-9]{3}_.*\.sql$ ]] || continue
#     · installer 门：strconv.Atoi(name[:idx]) 失败即 continue
#   ⇒ 任何**解析不出纯三位数字前缀**的迁移（`NNNb_` / `NNNpN_` / 日期名）
#     对这两道门**完全隐形**。
#   ⇒ 而 `NNNb_` **是本仓正在使用的命名约定**（361b/487b/487c/680b/682b/683b/
#     706b/707b 共 8 条已交付件，其中 706b/707b 是 2026-10 的近期件）。
#   ⇒ ★★ 实测复现（2026-10-22，工作树内新建 `912b_zzz_probe_delivery.sql`）：
#     本门 GREEN；installer 强制 embed 门 GREEN
#     ⇒ 该迁移既不在通道 files=()、也不在 embeddata/StartupFiles
#     ⇒ **升级库拿不到、新装库也拿不到，而没有任何一道门会红**。
#
# ★ 修法的性质（与 R81 轮 L11-b 门**同款**）：把「手挑的扫描根清单」换成
#   ★ **穷举全目录 ＋ 显式清单**。
#
# ⚠⚠⚠ **R83 轮实测出的第二宇宙**（R82 轮自己的遗漏）：
#   R82 轮的登记册**只穷举了 canonical 目录**（`sql/migrations/startup/`）
#   ⇒ ⇒ **installer 的 embeddata 目录**是**另一个独立的文件宇宙**
#   ⇒ ⇒ 而它里面有**非前缀名**的文件：`session_turns_hot_bootstrap.sql`
#        —— ★★ **canonical 目录里根本没有这个文件**
#   ⇒ ⇒ ⇒ **R82 轮的门在 embeddata 宇宙上仍然是瞎的**
#   ⇒ ⇒ ★★ **这个文件恰好是 `session_turns_hot` 那张表及其 11 个索引的载体**
#        （settle worker 读的就是这张表）⇒ ⇒ **它必须被登记、必须有投递腿**
#   ⇒ ⇒ 本块改为**两个宇宙各穷举一次**。
#
# ⚠ 枚举面此前还有第二处隐形：子目录 `up/`（14 条）不在 canonical_files 的
#   枚举里（那里是 `find -maxdepth 1`）⇒ **同一个病的第二例**
#   （今天无实害：14 条全部 <690；但「今天无实害」不等于「明天仍无实害」）。
#
# ★★ **R83 轮的第二处升级：把「有意不投递」从散文变成可核对的事实。**
#   R82 轮的登记册里，每条 `none` 只有一句**散文理由** ⇒ **门一个字都不核对**
#   ⇒ ⇒ 那就是「只是声名一下」⇒ ⇒ ★ 而且它**当场被证伪过一次**：R82 轮给
#   `2026-07-13-multimodal-token-fields-hot.sql` 写的理由引用了
#   `session_turns.reasoning_tokens` ⇒ ★★ **实测基线里 session_turns 这 5 列全缺**
#   （多模态列在 `usage_ledger` / `usage_ledger_hot`）⇒ ⇒ **理由是错的**
#   ⇒ ⇒ 根因：R82 轮用宽松 grep 取了行号，**没有反查那些行属于哪张表**
#   ⇒ ⇒ 修法 = 登记册加**效果落点**列（机器可核）⇒ 散文只留给人读。
#
# 门的形态（全部**一次报全**，沿用本脚本的收集器）：
#   ⓪ 量具自检：两个宇宙都要真的扫到东西
#   ① 目录里有、册子里没有 ⇒ 红（漏登记 ⇒ 会静默不投递）
#   ② 册子里有、目录里没有 ⇒ 红（陈旧条目 ⇒ 登记册自己漂移）
#   ③ 投递腿必须与两条腿的**真实登记**一致（声明 ≠ 事实 ⇒ 红）
#   ④ **效果落点**逐项核对（baseline 表/列/索引、890 解析出的退役对象、bootstrap 文件）
nonplain_registry="$ROOT_DIR/sql/schema/startup_migration_nonplain.tsv"
nonplain_baseline="$ROOT_DIR/sql/schema/01-schema.sql"
nonplain_mig890="$ROOT_DIR/sql/migrations/startup/890_drop_request_logs_v1_family.sql"
if [[ ! -f "$nonplain_registry" ]]; then
  printf 'startup 非纯数字前缀迁移的投递登记册缺失：%s\n' "$nonplain_registry" >&2
  exit 1
fi
# 穷举**两个宇宙**。
# ⚠ 用 `cd` + `find .` 而不是 `sed "s#^$ROOT_DIR/...##"` ⇒ 后者的路径里含正则
#   元字符（`.` 等）⇒ 仓库根一旦带特殊字符就会静默失配 ⇒ **量具自己的路径语义要稳**。
# ⚠ `down/` 子目录里的件全部带 `.down.sql` 后缀 ⇒ 这一条 -name 过滤同时排掉了
#   down/ 与顶层的 .down.sql ⇒ 留下来的就是「会被投递的 up 体」。
canon_up_bodies=$(cd "$ROOT_DIR/sql/migrations/startup" \
  && find . -type f -name '*.sql' ! -name '*.down.sql' | sed 's|^\./||' | sort)
embed_up_bodies=$(cd "$ROOT_DIR/installer/cmd/llm-gw-installer/embeddata/startup" \
  && find . -maxdepth 1 -type f -name '*.sql' ! -name '*.down.sql' | sed 's|^\./||' | sort)
# 两条腿的**全名**提取（既有的 sequence_files / startup_files 只认纯数字前缀名，
# 而本册子登记的恰恰是那些**非**纯数字前缀的名 ⇒ 必须另取一份全名视图）。
sequence_files_all=$(printf '%s\n' "$sequence" \
  | sed -nE 's#^[[:space:]]*"\$ROOT_DIR/sql/migrations/startup/([^"]+)"[,]?.*#\1#p' \
  | sort -u)
startup_files_all=$(grep -E '^[[:space:]]*"[^"]+\.sql",?$' "$ROOT_DIR/installer/internal/dbinit/runner.go" \
  | sed -E 's/^[[:space:]]*"([^"]+)".*/\1/' | sort -u)
# ⚠ 分类必须看**文件名**（basename），不能看相对路径 ——
#   `up/376_autoupdate.sql` 的 basename 是**纯数字前缀名** ⇒ 它**不是**非前缀迁移。
#   R82 轮第一版用相对路径判 ⇒ 把整个 up/ 子目录误判成「非前缀」
#   ⇒ 门当场红在 13 条 up/ 文件上 —— **这是门自己抓到的自己的错**。
nonplain_actual=$(
  {
    printf '%s\n' "$canon_up_bodies" | awk -F/ '$NF !~ /^[0-9][0-9][0-9]_/ {print "canonical/" $0}'
    printf '%s\n' "$embed_up_bodies" | awk -F/ '$NF !~ /^[0-9][0-9][0-9]_/ {print "embeddata/" $0}'
  } | sort
)
# ★ R84 轮：**过滤口径与 registry_rows() / Go 侧对齐**（先剥首尾空白，再判注释/空行）。
#   R83 轮这里是 `!/^#/ && NF>=2` ⇒ ★ **行首带空白的注释**（且含 TAB ⇒ NF=2）
#   会被当**数据行**⇒ ⇒ 「册子里有、目录里没有」那条判据会拿注释当键
#   ⇒ ⇒ 报出「登记册陈旧（键的宇宙前缀也非法）」这种**看不懂的假红**。
#   ⇒ 顺带把「列数不足 2 列的行」也排除掉：那种行由下面的**列数门**单独报。
nonplain_declared=$(awk -F'\t' '
  { line = $0; sub(/^[ \t]+/, "", line); sub(/[ \t]+$/, "", line) }
  line == "" || line ~ /^#/ { next }
  NF < 2 { next }
  { print $1 }
' "$nonplain_registry" | sort)

# ⚠⚠⚠ **读登记册必须用 \x1f 而不是 TAB**（★ R83 轮实测出的**真缺陷**，
#   是本门自己写出来的）：
#
#   bash 的 `read` 在 IFS 是**空白字符**（空格 / TAB / 换行）时，会把**连续分隔符
#   折叠成一个**、并**剥掉首尾的空白** ⇒ 登记册里
#   「效果落点为空、理由非空」（`键<TAB>none<TAB><TAB>理由` —— **人最自然的写法**）
#   会被读成 **落点=理由、理由=空** ⇒ ⇒ 「none 条目没有写效果落点」这条判据
#   **在这种写法下是瞎的**（判据恒绿）⇒ ⇒ ★ **它抓的正是自己声称要抓的东西，
#   却抓不到最常见的那种写法**。
#
#   实测（macOS bash 3.2）：`printf 'a\tb\t\tc\n' | { IFS=$'\t' read -r k l g r; }`
#   ⇒ landing=[c]、reason=[] —— **空的中段字段整个消失了**。
#
#   \x1f（unit separator）**不是** IFS 空白 ⇒ 空字段被原样保留 ✓
#   ⇒ ⇒ 与 Go 侧 `strings.Split(line, "\t")` 的语义**对齐**（Go 的 Split 不折叠）。
#
# ★★ 这是本仓**第四次**栽在「判据的观测手段自己有语义偏差」这一族上
#   （R81 轮 M15 的变异没落地、R82 轮第一版的分类粒度、R83 轮第一版的自检锚点、
#   本条）⇒ ⇒ **通用式：判据读自己的输入时，必须先验证「读出来的形状」与
#   写进去的形状一致**。
registry_rows() {
  # 输出：每行「键 \x1f 投递腿 \x1f 效果落点 \x1f 理由」
  #
  # ⚠⚠⚠ **每行末尾必须有 \n**（R83 轮实测出的**真缺陷**）：
  #   ★ 第一版**漏了换行** ⇒ **15 行被拼成 1 行** ⇒ ⇒ ★★ **③/④ 两个循环
  #     只检查了登记册的第一行**（其余 14 行**根本没被读**）⇒ ⇒ **门「有牙」是假的**。
  #   ⇒ 而且它在**基线上是绿的** ⇒ ⇒ **只有「针对非第一行的变异」才抓得到它**
  #     （M7 就是这样抓到它的）⇒ ⇒ **「基线绿 + 全部变异红」也可能是假绿**。
  #   ⇒ ⇒ 下面加了**行数自检**，把「读到几行」变成一条判据。
  #
  # ★★★★ R84 轮实测出的**第二处真缺陷（比 R83 那条更深一层）**：
  #   R83 轮修好了「\n 漏了」这个**症状**，并加了一条**行数自检**当守门人；
  #   ★★ 但那条自检的【参考计数】与【读册子】用的是**同一个过滤器**
  #     （两处都是 `!/^#/ && NF>=2`）⇒ ⇒ **同源自证**：
  #     过滤器一旦被收窄（**两处一起收窄**），读册子与参考计数会**同时**少读
  #     ⇒ 计数**仍然相等** ⇒ ⇒ 自检**看不见** ⇒ ⇒ ③/④ 两个判据
  #     **已经不再检查那几行**，而门在基线上是**绿的**。
  #   ⇒ ★ 实测复现见审计档 `docs/audit/2026-10-22-R84-读册子计数的同源自证.md`
  #     §2：**假绿**（登记册声明「走通道腿」而文件不在 files=() 里，两道门都绿）。
  #   ⇒ ⇒ 修法 = 参考计数必须与读册子的口径【独立】⇒ 见下面的
  #     `registry_reference_rows`（三个**不同**的工具算，与本函数的过滤器
  #     **不共享任何谓词**）。
  #
  # ⚠ R84 轮：过滤口径与 Go 侧**逐字对齐**（R83 轮这里是错的）：
  #   Go 侧是 `strings.TrimSpace(line)` 之后判「以 # 开头」或「为空」⇒ 跳过。
  #   ⇒ bash 侧若不先【去首尾空白】，一行**带缩进**的注释会被当**数据行**
  #     （旧口径 `!/^#/`：行首有空格 ⇒ 不匹配 `^#` ⇒ 进了数据行）
  #     ⇒ 与 Go 侧**语义分叉** ⇒ 两道门对同一份册子的理解会不一致。
  #   ⇒ 这里改成：先剥首尾空白 ⇒ 空行或以 # 开头 ⇒ 跳过。
  #   ⇒ 并且**只输出列数恰好为 4 的行**（列数不对的由下面的**列数门**单独报，
  #     不进 ③/④ ⇒ 避免「第 3 列被当成效果落点」这种看不懂的假红）。
  awk -F'\t' '
    { line = $0; sub(/^[ \t]+/, "", line); sub(/[ \t]+$/, "", line) }
    line == "" || line ~ /^#/ { next }
    NF != 4 { next }
    { printf "%s%c%s%c%s%c%s\n", $1, 31, $2, 31, $3, 31, $4 }
  ' "$nonplain_registry"
}

# ★★★★ R84 轮新增：**独立**的「数据行数」参考计数。
#
#   ⚠⚠⚠ **关键纪律：参考计数不得与读册子共用过滤器** ⇒ 否则是【同源自证】。
#     （这正是 LESSONS §235 那条：「双口径互证」若同源 ⇒ 它【恒真】，不是互证。）
#   ⇒ 这里用【三个不同的工具】算「数据行数」：
#       总行数（awk END{NR}）− 注释行（grep -c）− 空行（grep -c）
#   ⇒ 与 registry_rows() 的 awk 过滤器**没有任何共享谓词** ⇒
#     读册子一旦少读（过滤器被收窄 / 分隔符坏了 / 某行被跳过）⇒ 计数立刻不等 ⇒ 红。
#
#   ⇒ ★ 为什么要「三个工具」而不是「另一个 awk」：另一个 awk 也会复制同一个谓词
#     ⇒ 变异同时改两处时仍然同源 ⇒ 假绿照样成立。⇒ 谓词必须**写在不同的地方**。
registry_reference_rows() {
  local total comments blanks
  total=$(awk 'END{print NR+0}' "$nonplain_registry")
  comments=$(grep -c '^[[:space:]]*#' "$nonplain_registry" || true)
  blanks=$(grep -c '^[[:space:]]*$' "$nonplain_registry" || true)
  echo $(( total - comments - blanks ))
}

# ⓪ 量具自检：穷举必须真的扫到东西，且**必须扫到两个宇宙**。
#   ⇒ 否则「目录里多一条」这条判据会**恒绿**（门看上去正常、实际扫了个寂寞）。
#
# ⚠⚠ **锚点必须打在【原始枚举】上，不能打在「非前缀集合」上** ——
#   R83 轮第一版把锚点写成 `canonical/700_...`（**纯数字前缀**文件）并去
#   `nonplain_actual` 里找 ⇒ ⇒ **自检恒红**（纯数字前缀文件按定义就不在非前缀集合里）
#   ⇒ ⇒ ★ **这是本仓第三次栽在「自检的锚点选错集合」上**（R81 轮 M15、
#   R82 轮第一版分类粒度、本条）⇒ ⇒ **通用式：自检锚点必须选在【被检集合里】**。
for must_see in \
  "700_request_logs_view_raw_model_name.sql" \
  "up/600_outbound_body_to_bodies_hot.sql"; do
  if ! grep -Fxq "$must_see" <<<"$canon_up_bodies"; then
    gate_fail "canonical 宇宙的穷举没有扫到 ${must_see} ⇒ 枚举面漂移（find 的递归/过滤被改？）⇒ 本块全部判据都不可信"
  fi
done
if ! grep -Fxq "session_turns_hot_bootstrap.sql" <<<"$embed_up_bodies"; then
  gate_fail "embeddata 宇宙的穷举没有扫到 session_turns_hot_bootstrap.sql ⇒ 枚举面漂移 ⇒ 本块全部判据都不可信"
fi
if [[ -z "$nonplain_actual" ]]; then
  gate_fail "startup 迁移穷举没有扫到任何非纯数字前缀的 up 体 ⇒ 枚举面失效 ⇒ 本块的『漏登记』判据会恒绿"
fi
# ★★ **登记册「读到几行」自检**（R83 轮实测出的真缺陷的守门人）：
#   第一版 `registry_rows()` 的 printf **漏了换行** ⇒ 15 行被拼成 1 行
#   ⇒ ⇒ ③/④ 两个循环**只检查了第一行** ⇒ ⇒ **门在基线上是绿的、但 90% 是瞎的**。
#   ⇒ ⇒ ★ **基线绿 + 全部变异红，也可能是假绿** ⇒ ⇒ 必须把「读到几行」本身变成判据。
registry_rows_read=$(registry_rows | awk 'END{print NR+0}')
# ⚠ 用 `awk END{print NR}` 而不是 `wc -l`：**`wc -l` 数的是换行符个数** ⇒
#   一条**没有尾换行**的记录会被算成 **0 行**（而它明明是 1 条）⇒
#   报出来的数字会误导人（实测：M20 变异的报错写的是「读取的行数（0）」）。
#   ⇒ ⇒ `NR` 数的是**记录数**（最后一条无尾换行的也算 1 条）⇒ 语义与「我读到了几条」一致。
#
# ★★★★ R84 轮：**参考计数换成独立口径**（`registry_reference_rows`）——
#   R83 轮这里用的是 `awk '!/^#/ && NF>=2 {n++}'` ⇒ 与读册子**同一个谓词**
#   ⇒ ⇒ **同源自证** ⇒ ⇒ 实测可造出**假绿**（见 registry_rows 的注释）。
registry_rows_data=$(registry_reference_rows)
if [[ "$registry_rows_read" != "$registry_rows_data" ]]; then
  gate_fail "登记册读取的行数（${registry_rows_read}）与数据行数（${registry_rows_data}）不一致 ⇒ 读册子的口径坏了（三个候选原因：行分隔符丢了？过滤器被收窄了？或者册子里有**列数不对**的行——那种行不读进来，列数门会单独报）⇒ 本块全部登记册判据都不可信"
fi
if [[ "$registry_rows_data" -lt 1 ]]; then
  gate_fail "登记册一条数据行都没有 ⇒ 观测手段失效（读了个空文件？）⇒ 本块全部判据都不可信"
fi

# ★★ R84 轮新增：登记册**列数**门（bash 侧自己核「每行恰好 4 列」）。
#
#   R83 轮把这一条**只**放在 Go 侧 ⇒ ⇒ bash 侧读到列数不对的行时，会把
#   「第 3 列」当成效果落点、把「第 4 列」当成理由 ⇒ ⇒ ④ 会报
#   「效果落点语法非法」这种**看不懂的错** ⇒ ⇒ 两道门对同一份册子的**形状**
#   判断不一致（Go 说「列数是 3」，bash 说「落点语法非法」）。
#   ⇒ ⇒ 现在 bash 侧也核列数 ⇒ 两边对**形状**的判断一致 ⇒ 报错可读。
#
#   ⚠ 这条判据与 `registry_rows()` 的过滤器**同源**是**允许**的：
#     它是**报告型**判据（把坏行指出来），不是**计数型**判据
#     （「读到几条」必须独立，见上面的 registry_reference_rows）。
registry_bad_rows=$(awk -F'\t' '
  { line = $0; sub(/^[ \t]+/, "", line); sub(/[ \t]+$/, "", line) }
  line == "" || line ~ /^#/ { next }
  NF != 4 { printf "第 %d 行的列数是 %d（必须恰好 4 列：键<TAB>投递腿<TAB>效果落点<TAB>理由）\n", NR, NF }
' "$nonplain_registry")
if [[ -n "$registry_bad_rows" ]]; then
  while IFS= read -r bad; do
    [[ -n "$bad" ]] && gate_fail "投递登记册 ${nonplain_registry} 的${bad}"
  done <<<"$registry_bad_rows"
fi

# ① 目录里有、册子里没有 ⇒ 红（漏登记 ⇒ 会静默不投递）
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  if ! grep -Fxq "$key" <<<"$nonplain_declared"; then
    gate_fail "startup 迁移 ${key} 的文件名解析不出纯三位数字前缀 ⇒ 它对**投递路径契约门**与**installer 强制 embed 门**都完全隐形（两道门都靠 ^[0-9]{3}_ 判视野）⇒ 它既不在通道 files=()、也不在 embeddata/StartupFiles ⇒ 升级库与新装库都拿不到 ⇒ 必须先在 sql/schema/startup_migration_nonplain.tsv 登记投递腿，再登记进对应通道"
  fi
done <<<"$nonplain_actual"

# ② 册子里有、目录里没有 ⇒ 红（陈旧条目 ⇒ 登记册自己漂移）
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  case "$key" in
    canonical/*) target="$ROOT_DIR/sql/migrations/startup/${key#canonical/}" ;;
    embeddata/*) target="$ROOT_DIR/installer/cmd/llm-gw-installer/embeddata/startup/${key#embeddata/}" ;;
    *) target="" ;;
  esac
  if [[ -z "$target" || ! -f "$target" ]]; then
    gate_fail "startup_migration_nonplain.tsv 登记了 ${key}，但对应目录里没有这个文件 ⇒ 登记册陈旧（键的宇宙前缀也非法）"
  fi
done <<<"$nonplain_declared"

# ③ 投递腿必须与两条腿的**真实登记**一致（声明 ≠ 事实 ⇒ 红）
#   ⚠ 通道腿只能投递 **canonical** 路径（files=() 里写死的就是 canonical 路径）
#     ⇒ embeddata 宇宙的键若声明 channel/both ⇒ 一定是错的。
while IFS=$'\x1f' read -r key leg landing reason; do
  [[ -n "$key" ]] || continue
  base="${key##*/}"
  case "$leg" in
    channel|both)
      if [[ "$key" != canonical/* ]]; then
        gate_fail "startup_migration_nonplain.tsv 声明 ${key} 走通道腿，但通道 files=() 里写死的是 **canonical** 路径 ⇒ embeddata 宇宙的键不可能由通道投递 ⇒ 声明与事实不符"
      elif ! grep -Fxq "$base" <<<"$sequence_files_all"; then
        gate_fail "startup_migration_nonplain.tsv 声明 ${key} 走通道腿，但它不在 scripts/apply-db-revision-sequence.sh 的 files=() 里 ⇒ 升级库拿不到"
      fi
      ;;
  esac
  case "$leg" in
    installer|both)
      if ! grep -Fxq "$base" <<<"$startup_files_all"; then
        gate_fail "startup_migration_nonplain.tsv 声明 ${key} 走安装器腿，但它不在 installer/internal/dbinit/runner.go 的 StartupFiles 里 ⇒ 新装库拿不到"
      fi
      ;;
  esac
  case "$leg" in
    none)
      if grep -Fxq "$base" <<<"$sequence_files_all"; then
        gate_fail "startup_migration_nonplain.tsv 声明 ${key} 有意不投递，但它**在**通道 files=() 里 ⇒ 声明与事实不符"
      fi
      if grep -Fxq "$base" <<<"$startup_files_all"; then
        gate_fail "startup_migration_nonplain.tsv 声明 ${key} 有意不投递，但它**在**安装器 StartupFiles 里 ⇒ 声明与事实不符"
      fi
      ;;
  esac
done < <(registry_rows)

# ④ ★★ **效果落点**逐项核对 —— 把「有意不投递」从散文变成可核对的事实。
#
#   ★ 退役对象清单**从迁移 890 自己解析**（它的 FOREACH 数组）⇒
#     **不手工维护** ⇒ 890 改了清单，这道判据自动跟着改。
baseline_table_exists() {
  grep -qE "^CREATE TABLE (IF NOT EXISTS )?public\.$1 *\(" "$nonplain_baseline"
}
baseline_column_exists() {
  local tbl="$1" col="$2"
  awk -v t="public.$tbl" -v c="$col" '
    $0 ~ ("^CREATE TABLE (IF NOT EXISTS )?" t " *\\(") { inblk=1; next }
    inblk && $0 ~ "^CREATE TABLE " { inblk=0 }
    inblk {
      line=$0; sub(/^[ \t]+/, "", line);
      if (line ~ ("^" c "([ \t]|$)")) { found=1 }
    }
    END { exit(found ? 0 : 1) }
  ' "$nonplain_baseline"
}
baseline_index_exists() {
  grep -qE "^CREATE (UNIQUE )?INDEX \"?$1\"? " "$nonplain_baseline"
}
retired_objects=$(awk '/FOREACH v_name IN ARRAY ARRAY\[/{f=1;next} f&&/\] LOOP/{f=0} f' "$nonplain_mig890" \
  | grep -oE "'[a-z_][a-z0-9_]*'" | tr -d "'" | sort -u)
if [[ -z "$retired_objects" ]]; then
  gate_fail "从 ${nonplain_mig890} 解析不出退役对象清单 ⇒ 效果落点的 retired: 判据会恒绿（量具失效）"
fi
bootstrap_exists() {
  local name="$1"
  [[ -f "$ROOT_DIR/installer/cmd/llm-gw-installer/embeddata/startup/$name" ]] \
    || [[ -f "$ROOT_DIR/sql/migrations/startup/$name" ]]
}
check_landing_spot() {
  local key="$1" landing="$2" spot
  if [[ "$landing" == "-" || -z "$landing" ]]; then
    return 0
  fi
  local IFS=','
  for spot in $landing; do
    spot="$(printf '%s' "$spot" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    [[ -n "$spot" ]] || continue
    case "$spot" in
      baseline:*)
        local obj="${spot#baseline:}"
        if [[ "$obj" == *.* ]]; then
          local tbl="${obj%%.*}" col="${obj#*.}"
          if ! baseline_column_exists "$tbl" "$col"; then
            gate_fail "startup_migration_nonplain.tsv 的 ${key} 声明效果落在 baseline:${obj}，但基线 sql/schema/01-schema.sql 里 **public.${tbl} 没有 ${col} 这一列** ⇒ 效果落点不成立 ⇒ 这条『有意不投递』的决定没有依据"
          fi
        else
          if ! baseline_table_exists "$obj"; then
            gate_fail "startup_migration_nonplain.tsv 的 ${key} 声明效果落在 baseline:${obj}，但基线 sql/schema/01-schema.sql 里没有这张表 ⇒ 效果落点不成立"
          fi
        fi
        ;;
      baseline-index:*)
        local idx="${spot#baseline-index:}"
        if ! baseline_index_exists "$idx"; then
          gate_fail "startup_migration_nonplain.tsv 的 ${key} 声明效果落在 baseline-index:${idx}，但基线里没有这个索引 ⇒ 效果落点不成立"
        fi
        ;;
      retired:*)
        local obj="${spot#retired:}"
        if ! grep -Fxq "$obj" <<<"$retired_objects"; then
          gate_fail "startup_migration_nonplain.tsv 的 ${key} 声明效果落在 retired:${obj}，但该对象**不在迁移 890 自己列出的退役清单里** ⇒ 效果落点不成立（要么对象没退役，要么清单名字写错了）"
        fi
        ;;
      bootstrap:*)
        local obj="${spot#bootstrap:}"
        if ! bootstrap_exists "$obj"; then
          gate_fail "startup_migration_nonplain.tsv 的 ${key} 声明效果落在 bootstrap:${obj}，但 embeddata/startup/ 与 sql/migrations/startup/ 下都没有这个文件 ⇒ 效果落点不成立"
        fi
        ;;
      *)
        gate_fail "startup_migration_nonplain.tsv 的 ${key} 的效果落点 ${spot} 语法非法（合法：baseline:<表>[.<列>] / baseline-index:<索引> / retired:<对象> / bootstrap:<文件>）"
        ;;
    esac
  done
}
while IFS=$'\x1f' read -r key leg landing reason; do
  [[ -n "$key" ]] || continue
  if [[ "$leg" == "none" && ( "$landing" == "-" || -z "$landing" ) ]]; then
    gate_fail "startup_migration_nonplain.tsv 的 ${key} 声明「有意不投递」却没有写效果落点 ⇒ 这条决定没有任何可核对的依据（散文不算依据）"
  fi
  check_landing_spot "$key" "$landing"
done < <(registry_rows)

# R33 (2026-10-03) channel-leg completeness — structural closeout of the
# five-recurrence class (693/699/701/703, then 815/816, then 817): each landed
# with the installer leg complete and the channel leg forgotten. Neither gate
# above catches that shape in general: canonical_delivery_path_check accepts
# ANY one of the three paths, and the top_startup check only sees the highest
# number — so once a newer migration lands, an unregistered one becomes
# permanently invisible and upgraded databases never receive it.
# This invariant makes the UPGRADE path exhaustive: every >=690 startup
# migration must be in the channel sequence, the reviewed Go-ensure allowlist,
# or the exact installer-only exception list below. Adding a new
# installer-only migration now demands a deliberate, commented edit here —
# the sixth recurrence of the class fails pre-commit even when it is no
# longer the top of the track.
while IFS= read -r name; do
  [[ "$name" == *.down.sql ]] && continue
  [[ "$name" =~ ^[0-9]{3}_.*\.sql$ ]] || continue
  version=${name%%_*}
  [[ "$version" < "690" ]] && continue
  [[ "$name" == "755_drop_dead_cleanup_expired_session_turn_logs.sql" ]] && continue
  if grep -Fxq "$name" <<<"$sequence_files" \
    || grep -Fxq "$name" <<<"$ensure_allowlist" \
    || grep -Fxq "$name" <<<"$channel_gap_allowlist" \
    || grep -Fxq "$name" <<<"$superseded_migrations"; then
    continue
  fi
  # No bare printf here: the finding is collected and reported once, in the
  # numbered summary at the end. Printing here as well produced every message
  # twice — once unnumbered, once numbered — which is exactly the "which line
  # is the real failure?" confusion this gate is being fixed for.
  gate_fail "startup migration ${name} has an installer leg but no upgrade path; register it in the channel files array, the Go-ensure allowlist, (installer-only by design) channel_gap_allowlist, or (overturned, must never run) superseded_migrations"
done <<<"$canonical_files"

# Sequence-only migrations are valid fresh-install exceptions, but they must
# remain in the upgrade contract. If either disappears, the exhaustive gate's
# coverage can otherwise be obscured by an unrelated future StartupFiles edit.
for required in \
  '705_request_logs_reattach_detached_partitions.sql' \
  '710_request_logs_view_session_family_v2.sql'; do
  grep -Fxq "$required" <<<"$sequence_files" || \
    gate_fail "required sequence-only migration missing from revision sequence: ${required}"
done

# Ensure-only exceptions must not silently expand or disappear from the
# reviewed list. Their source/migration equivalence has dedicated db tests.
for required in \
  '704_plan_quota_probe_backoff.sql' \
  '709_work_type_route_coverage.sql' \
  '715_route_incidents_pending_state.sql'; do
  grep -Fxq "$required" <<<"$ensure_allowlist" || \
    gate_fail "required Go-ensure migration missing from allowlist: ${required}"
done

# Execute the REAL clobber guard (2026-09-14 audit F-P0-1 root cause):
# grep-spot-checks stayed green while the pre-flight guard exited 5 on the
# unregistered 703 chain. Extract the guard's code block (redefined_functions
# scan + guard_violations check, ending at the guard_violations loop's
# closing `done)")` line) and evaluate it verbatim against the same arrays —
# no re-implementation that could drift from deploy-time behaviour. If the
# extraction anchors ever drift, the unset-variable failures below fail this
# test loudly rather than silently skipping the guard.
guard_block="$(awk '
  /^redefined_functions="\$\($/ {inside=1}
  inside {print}
  inside && /^done\)"$/ {exit}
' "$SCRIPT")"
if [[ -z "$guard_block" ]]; then
  printf 'could not extract clobber guard block from %s\n' "$SCRIPT" >&2
  exit 1
fi
# The guard reads the files=() and intentional_function_chains=() arrays;
# evaluate the same definitions the deploy script uses (files block captured
# above, chains extracted with the same anchors).
eval "$sequence"
chains_block="$(awk '/^intentional_function_chains=\(/{inside=1} inside{print} inside && /^\)$/{exit}' "$SCRIPT")"
[[ -n "$chains_block" ]] || { printf 'could not extract intentional_function_chains\n' >&2; exit 1; }
eval "$chains_block"
eval "$guard_block"
if [[ -n "${guard_violations:-}" ]]; then
  # Clobber violations keep exit 5 (deploy-time parity) but no longer mask the
  # other collected failures: the report below runs first, then the exit code
  # is chosen from what was found.
  GATE_SAW_CLOBBER=1
  while IFS= read -r vline; do
    [[ -n "$vline" ]] && gate_fail "clobber guard violation (deploy would exit 5): ${vline}"
  done <<<"$guard_violations"
fi

# Function clobber guard (2026-09-05 PG log audit): 572→563 silently
# re-clobbered update_session_summary(). Every multi-file function chain must
# stay registered in intentional_function_chains or deployment aborts.
for chain in \
  'update_session_summary|572_session_summary_large_token_ratio.sql|563_session_summary_trigger_on_hot.sql|358_session_ownership.sql|661_session_summary_token_ratio_reassert.sql|' \
  'archive_credential_model_index|653_archive_credential_model_index_canonical_return.sql|654_archive_credential_model_index_detach_drop.sql|' \
  'ensure_request_logs_bodies_partition|694_partition_ensure_timezone.sql|765_bodies_columnar_storage.sql|829_bodies_columnar_rollback.sql|'; do
  grep -qF -- "'$chain'" "$SCRIPT" || \
    gate_fail "missing intentional function chain registration: ${chain}"
done

# 2026-09-21 内容指纹重放清单（纪律⑨，F4 机制债收口）三重自清洁：
#   1. 条目格式必须为 "basename|sha256(64 hex)"；
#   2. 重放目标必须是通道 files=() 数组的注册文件；
#   3. 指纹必须与文件当前内容一致——文件再改动而条目未同步时门禁变红，
#      并直接打印正确的 sha（更新条目一次复制即可）。
replays_block="$(awk '/^legacy_content_replays=\(/{inside=1} inside{print} inside && /^\)$/{exit}' "$SCRIPT")"
[[ -n "$replays_block" ]] || { printf 'could not extract legacy_content_replays\n' >&2; exit 1; }
eval "$replays_block"
for entry in "${legacy_content_replays[@]}"; do
  base="${entry%%|*}"
  sha="${entry##*|}"
  if [[ "$base" == "$entry" || "$sha" == "$entry" || "${#sha}" -ne 64 || ! "$sha" =~ ^[0-9a-f]+$ ]]; then
    gate_fail "malformed legacy_content_replays entry (expected \"basename|sha256\"): ${entry}"
    continue
  fi
  grep -qF "/${base}\"" <<<"$sequence" || {
    gate_fail "legacy_content_replays target ${base} is not registered in the sequence files array"
    continue
  }
  target="$ROOT_DIR/sql/migrations/startup/$base"
  if [[ ! -f "$target" ]]; then
    gate_fail "legacy_content_replays target missing: ${base}"
    continue
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$target" | cut -d' ' -f1)
  else
    actual=$(shasum -a 256 "$target" | cut -d' ' -f1)
  fi
  if [[ "$actual" != "$sha" ]]; then
    gate_fail "stale legacy_content_replays fingerprint for ${base}: entry ${sha:0:12}… actual ${actual:0:12}… (update the entry to the actual sha)"
  fi
done

# ── 台账↔产物对账门（ledger_artifact_probe）契约（2026-10-29 第二轮通用化）────
# 两条自清洁判据，都对着部署脚本本体执行（不重实现）：
#   ① 覆盖面：900~920 的每个非 down 迁移都必须有一条 ledger_artifact_probe ——
#     211 实测的静默缺失是**整族**（900/902/904/912/913/914/916 七个迁移对象
#     全部缺席才被发现），新增 9xx 迁移而忘配探针 = 下一次静默缺失复发点。
#   ② 产物名保真：探针 SQL 里引用的 to_regclass('public.X') / conname /
#     relname 等产物名必须真实出现在对应迁移文件里（反解 `ledger_artifact_probe
#     "<file>"` 行并抽取单引号里的标识符；文件改名/改名产物而探针没跟 ⇒ 红）。
#     ⚠ 902 的探针认终态名 stats_event_dedup_pkey（tmp 名会被 ADD CONSTRAINT
#       ... USING INDEX 改掉），908 是 drop 语义（反向断言 IS NULL）——两者
#       与文件内名字仍逐字对应，判据天然成立。
#     ★ 提取器必须抓**完整调用**（调用行以 `\` 结尾 ⇒ SQL 在下一行）：
#       第一版用 awk '/^ledger_artifact_probe /' 只抓到调用行、SQL 行从未进过
#       判据 ⇒ 产物名保真判据整个空转（变异臂注入 TYPO 名照旧全绿才暴露）。
probe_block="$(python3 - "$SCRIPT" <<'PYEOF'
import re, sys
text = open(sys.argv[1], encoding='utf-8').read()
for m in re.finditer(r'^ledger_artifact_probe "[^"]+"\s*\\\n\s*".*"$', text, re.M):
    print(m.group(0).replace('\n', ' '))
PYEOF
)"
[[ -n "$probe_block" ]] || { printf 'could not extract ledger_artifact_probe calls\n' >&2; exit 1; }
# ★ bash 3.2 没有 declare -A（关联数组是 bash 4+）⇒ macOS 自带 bash 在这里
#   直接死（rc=2）⇒ 掏空门的「读方」在这台机器上**永远起不来** ⇒ 门判「不可判定」。
#   ⇒ 改用「probed 列表 + 精确匹配」：语义等价（键集合的成员判定），
#     但不依赖 bash 4。键是受限字符集（迁移文件名 [0-9]{3}_*.sql）⇒
#     用换行拼接 + grep -Fx 精确整行匹配即可，无歧义。
probed_list=""
while IFS= read -r line; do
  target="$(sed -E 's/^ledger_artifact_probe "([^"]+)".*/\1/' <<<"$line")"
  probed_list+="${target}"$'\n'
done <<<"$probe_block"
probed_has() { grep -qFx -- "$1" <<<"$probed_list"; }
for f in "$ROOT_DIR"/sql/migrations/startup/*.sql; do
  base="$(basename "$f")"
  case "$base" in *.down.sql|*.skip|*.bak.skip) continue;; esac
  case "$base" in 9[0-2][0-9]_*) ;; *) continue;; esac
  probed_has "$base" || \
    gate_fail "startup migration ${base} has no ledger_artifact_probe (artifact drift would be silent again)"
done
while IFS= read -r line; do
  target="$(sed -E 's/^ledger_artifact_probe "([^"]+)".*/\1/' <<<"$line")"
  mig="$ROOT_DIR/sql/migrations/startup/$target"
  if [[ ! -f "$mig" ]]; then
    gate_fail "ledger_artifact_probe target missing: ${target}"
    continue
  fi
  # 抽取探针行里所有 public.<name> / '<name>' 形态的候选产物名，逐个对迁移体核实。
  while IFS= read -r art; do
    [[ -z "$art" ]] && continue
    if ! grep -qF "$art" "$mig"; then
      gate_fail "probe artifact '${art}' (${target}) not found in migration file — probe drifted from file"
    fi
  done < <(grep -oE "public\.[a-z_][a-z0-9_]*|'[a-z_][a-z0-9_]{4,}'" <<<"$line" \
             | sed -E "s/^public\.//; s/^'//; s/'$//" \
             | sort -u)
done <<<"$probe_block"

# 919 函数链登记（2026-10-29 审计轮实测缺口）：R176 的部署树带着
# refresh_ursm_snapshot_slot_daily 的链序登记，merge 回 main 时只留了文件、
# 丢了登记 ⇒ 下一次 211 部署会在应用任何文件之前 exit 5 整体 abort。
# 上面「execute the REAL clobber guard」已把这一缺口当运行时违规报出；此处再
# 把登记本身钉成静态要求，报错信息直接指向要补的行。
grep -qF -- "'refresh_ursm_snapshot_slot_daily|892_ursm_snapshot_slot_daily.sql|919_ursm_slot_daily_active_minutes.sql|'" "$SCRIPT" || \
  gate_fail "missing intentional function chain registration: refresh_ursm_snapshot_slot_daily (892 → 919)"

# 656 has no db.go ensure compensation; the sequence is its only存量 deployment
# path besides the installer fresh-install runner.
if ! grep -q '656_auto_route_selections_hot' "$ROOT_DIR/installer/internal/dbinit/runner.go"; then
  gate_fail "installer runner is missing 656_auto_route_selections_hot"
fi

# 644's CHECK rebuild must be definition-aware: deploying must not re-run a
# validated ADD CONSTRAINT (ACCESS EXCLUSIVE + full scan) when the canonical
# taxonomy is already in place.
if ! grep -q "position('no_eligible_model' in pg_get_constraintdef" \
    "$ROOT_DIR/sql/migrations/startup/644_tuning_views_selfcheck_and_candidate_failure_cache.sql"; then
  gate_fail "644 CHECK rebuild is not definition-aware"
fi

# ── 单次报全（2026-10-05 审计 §9.253）────────────────────────────────────────
# 退出码语义保持不变：有 clobber 违规 ⇒ 5（与 deploy-time 对齐）；否则有任意
# 其它失败 ⇒ 1；全通过 ⇒ 0。改变的只有**报告的完整度**，不是判据的严格度。
if gate_report; then
  printf 'apply-db-revision-sequence contract passed\n'
  exit 0
fi
if (( GATE_SAW_CLOBBER )); then
  exit 5
fi
exit 1

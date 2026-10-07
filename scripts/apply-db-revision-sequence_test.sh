#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/apply-db-revision-sequence.sh"

bash -n "$SCRIPT"

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

    if printf '%s\n' "$startup_files" | grep -Fxq "$name" \
      || printf '%s\n' "$sequence_files" | grep -Fxq "$name" \
      || printf '%s\n' "$ensure_files" | grep -Fxq "$name" \
      || printf '%s\n' "$superseded_files" | grep -Fxq "$name" \
      || printf '%s\n' "$channel_gap_files" | grep -Fxq "$name"; then
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
  printf '%s\n' "$multi_output" | grep -Fq "$orphan" || {
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
# bg/partition_825_contract_test.go Test830IsDeliberatelyNotInTheAutoStartupSequence.
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
# ★ 两条投递腿**彼此独立**（都不是对方的子集）：
#   ① 扫描腿  scripts/deploy-lib/db-changelog.sh —— 扫目录 + 查台账，未记录即投递
#   ② 通道腿  本脚本 files=(...) 数组 —— 显式列出才投递
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
      if printf '%s\n' "$channel_gap_allowlist" | grep -Fxq "$b"; then
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
canonical_files=$(find "$ROOT_DIR/sql/migrations/startup" -maxdepth 1 -type f -name '[0-9][0-9][0-9]_*.sql' \
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
  if printf '%s\n' "$sequence_files" | grep -Fxq "$name" \
    || printf '%s\n' "$ensure_allowlist" | grep -Fxq "$name" \
    || printf '%s\n' "$channel_gap_allowlist" | grep -Fxq "$name" \
    || printf '%s\n' "$superseded_migrations" | grep -Fxq "$name"; then
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
  'update_session_summary|572_session_summary_large_token_ratio.sql|563_session_summary_trigger_on_hot.sql|661_session_summary_token_ratio_reassert.sql|' \
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

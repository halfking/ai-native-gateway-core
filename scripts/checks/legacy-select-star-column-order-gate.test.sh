#!/usr/bin/env bash
# =====================================================================
# scripts/checks/legacy-select-star-column-order-gate.test.sh
#   —— 「legacy 跨面 `SELECT * UNION ALL` 列序对拍门」的**夹具**（★ 双向变异自证 ★★）
#
# ★★ 为什么要有这个夹具（★ 本轮的核心纪律 ★★）★★
#   ★ 这道门的判据是「**真库里的列序**」⇒ ★★ **没有 PG 就没法验证它红不红** ★★
#   ★ ⇒ ★★ **⇒ 判据逻辑必须能在没有 PG 的机器上被测** ⇒ ★★ ⇒ ★★
#   ★★ **用假 psql**（★ 读预置夹具、★ 不碰任何真实数据库 ★★）★★
#   ★ ⇒ ★★ **判据逻辑的可测性不依赖 docker / 不依赖 PG** ★★
#
# ★★ ★★ L34 八轮：★ 夹具**从台账派生**（★ 不再写死 11 对 ★★）★★
#   ★ 台账升级为**语句级**（★ 30 条、★ 分支数 2～3 不等 ★★）⇒ ★
#   ★ ★★ 夹具若写死关系名 ⇒ ★ 台账一改就**悄悄失真** ⇒ ★★ ⇒ ★
#   ★ ★★ 本夹具**直接解析仓内那份台账** ⇒ ★★ **夹具与台账永远同步** ★★
#
# ★★ 臂（★ 每一条都断言**真实退出码**、★ 不看输出长相 ★★）★★
#   A 无 DB 地址 ⇒ SKIP（EXIT=0、★ 但输出必须写明「不是通过」★★）
#   B 全部对齐 ⇒ PASS（EXIT=0）★ 且**对齐数 == 台账条数**（★ 现算 ★★）
#   C 一对**列序漂移**（★ 同列数、★ 位置类型不同 ★★）⇒ 红（EXIT=1）＋ 点名该视图
#   D 一对**列数不同** ⇒ 红（EXIT=1）＋ 报「列数不同」
#   E 各分支**都不在场**（★ 如 request_logs 族被 890 下线 ★★）⇒ 报出但**绿**
#   F **只有部分分支在场** ⇒ 报出但**绿**
#   G 台账**解析面为空**（★ 合成仓 ★★）⇒ ABORT（EXIT=2）（★ 门坏了 ≠ 判据红 ★★）
#   H **查询面坏**（★ 有关系、★ 而列序查询 0 行 ★★）⇒ ABORT（EXIT=2）
#   I **三路漂移**（★ 340 那一族：★ 父表 ∪ 月分区 ∪ default ★★）⇒ 红（EXIT=1）
#   J **三路里缺一路** ⇒ 报出但**绿**（EXIT=0）
#
# ★★ 变异自证（★ 本门自己也要证明「它能咬住东西」 ★★）★★
#   ★ 臂 C／D／I 就是**注入真实漂移** ⇒ ★ **门必须变红** ★★
#   ★ 臂 G／H 是**注入解析面损坏** ⇒ ★ **门必须 ABORT 而不是判红** ★★
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

GATE="scripts/checks/legacy-select-star-column-order-gate.sh"
REG_GO="sql/migrations/startup/hot_surface_select_star_contract_test.go"
FIXTURE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/select-star-order-fixture.XXXXXX")"
trap 'rm -rf "$FIXTURE_DIR"' EXIT

PASS=0
FAIL=0
note() { printf '%s\n' "$*"; }
ok()  { PASS=$((PASS+1)); printf '  ✔ %s\n' "$*"; }
bad() { FAIL=$((FAIL+1)); printf '  ✘ %s\n' "$*" >&2; }

# ★★ 台账解析（★ ★★ 与门同口径 ★★）★★
reg_entries() {
  awk '
    /^var legacyCrossSurfaceSelectStar = map\[string\]string\{/ { inmap = 1; next }
    inmap && /^\}/ { inmap = 0; next }
    inmap {
      line = $0
      if (line !~ /^[[:space:]]*"/) next
      if (match(line, /^[[:space:]]*"[^"]+"/) == 0) next
      key = substr(line, RSTART, RLENGTH); gsub(/"/, "", key)
      sub(/^[[:space:]]+/, "", key)
      rest = substr(line, RSTART + RLENGTH)
      if (match(rest, /"[^"]*"/) == 0) next
      val = substr(rest, RSTART, RLENGTH); gsub(/"/, "", val)
      if (index(key, "|") == 0) next
      print key "\t" val
    }
  ' "$REG_GO"
}
reg_entries >"$FIXTURE_DIR/reg.tsv"
REG_COUNT="$(wc -l <"$FIXTURE_DIR/reg.tsv" | tr -d ' ')"
# ★★ 台账里出现过的**全部关系**（★ 夹具要给每一个都造列 ★★）★★
cut -f2 "$FIXTURE_DIR/reg.tsv" | tr ',' '\n' | sed '/^$/d' | sort -u >"$FIXTURE_DIR/rels.txt"
REL_COUNT="$(wc -l <"$FIXTURE_DIR/rels.txt" | tr -d ' ')"
note "台账：${REG_COUNT} 条｜涉及关系 ${REL_COUNT} 个（★ 从仓内台账现算 ★★）"

# ★★ 假 psql（★ 读预置夹具、★ **不碰任何真实数据库** ★★）★★
make_fake_psql() {
  local cols_fixture="$1"
  local total="${2:-600}"
  cat >"$FIXTURE_DIR/fake-psql" <<FAKEPSQL
#!/usr/bin/env bash
case "\$*" in
  *select-total*) printf '%s\n' "${total}" ;;
  *) cat "${cols_fixture}" ;;
esac
FAKEPSQL
  chmod +x "$FIXTURE_DIR/fake-psql"
}

# ★★ 造一份「全部对齐」的列序夹具（★ 每个关系 3 列、★ 完全一致 ★★）★★
make_aligned_fixture() {
  local out="$1"
  : >"$out"
  while read -r rel; do
    [ -n "$rel" ] || continue
    {
      printf '%s\t1\tid\tbigint\n' "$rel"
      printf '%s\t2\tcreated_at\ttimestamp with time zone\n' "$rel"
      printf '%s\t3\tpayload\ttext\n' "$rel"
    } >>"$out"
  done <"$FIXTURE_DIR/rels.txt"
}

run_gate() {
  local extra_env="${1:-}"
  local rc=0
  # ★★ 判据位一律用**真实退出码**（★ 不用 `|| true` 兜 ★★）★★
  SELECT_STAR_GATE_DATABASE_URL="postgres://fixture/fixture" \
  SELECT_STAR_GATE_PSQL_BIN="$FIXTURE_DIR/fake-psql" \
  env $extra_env bash "$GATE" >"$FIXTURE_DIR/out.txt" 2>&1 || rc=$?
  GATE_OUT="$(cat "$FIXTURE_DIR/out.txt")"
  GATE_RC="$rc"
}

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 A：没有 DB 地址 ⇒ ★ **显式 SKIP**（★ 不伪装成通过 ★★）"
rc=0
bash "$GATE" >"$FIXTURE_DIR/out.txt" 2>&1 || rc=$?
out="$(cat "$FIXTURE_DIR/out.txt")"
if [ "$rc" = "0" ] && printf '%s' "$out" | grep -q "SKIP" && printf '%s' "$out" | grep -q "不是通过"; then
  ok "臂 A SKIP（EXIT=0）且写明「不是通过」"
else
  bad "臂 A 期望 EXIT=0 + SKIP；实际 ${rc}；输出：$(printf '%s' "$out" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 B：各分支列序**全部对齐** ⇒ ★ 期望绿（EXIT=0）＋ 对齐数 == 台账条数"
make_aligned_fixture "$FIXTURE_DIR/aligned.tsv"
make_fake_psql "$FIXTURE_DIR/aligned.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "PASS" \
   && printf '%s' "$GATE_OUT" | grep -q "对齐 ${REG_COUNT} 对"; then
  ok "臂 B 绿（EXIT=0）且对齐数 == ${REG_COUNT}"
else
  bad "臂 B 期望 EXIT=0 + 对齐 ${REG_COUNT} 对；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 C：**注入一处列序漂移**（★ 同列数、★ 位置 3 类型不同 ★★）⇒ ★ 期望红（EXIT=1）＋ 点名"
awk -F'\t' '
  $1 == "candidate_failure_logs_hot" && $3 == "payload" { print $1 "\t" $2 "\t" $3 "\tinteger"; next }
  { print }
' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/drift.tsv"
make_fake_psql "$FIXTURE_DIR/drift.tsv"
run_gate
# ★★ 位次断言（★ L34 七轮：★ 门必须回答「**这次会在第几条炸**」 ★★）★★
# ★★ 位次由**共享库**现算（★ 而不是在夹具里写死数字 ⇒ ★ 仓内投递面变了也不会失效 ★★）★★
. "$REPO_ROOT/scripts/lib/deploy-surface.sh"
surface_file="$FIXTURE_DIR/surface.txt"
deploy_surface_extract "$REPO_ROOT/scripts/apply-db-revision-sequence.sh" "$surface_file" || true
pos_392="$(deploy_surface_position "$surface_file" "392_candidate_failure_logs_monthly_partition.sql")"
pos_344="$(deploy_surface_position "$surface_file" "344_usage_ledger_hot_independence.sql")"
if [ "$GATE_RC" = "1" ] \
   && printf '%s' "$GATE_OUT" | grep -q "candidate_failure_logs_with_current_month" \
   && printf '%s' "$GATE_OUT" | grep -q "列序已漂移"; then
  ok "臂 C 红（EXIT=1）且点名了漂移的那一对"
  if [ -n "$pos_392" ] && printf '%s' "$GATE_OUT" | grep -q "第 ${pos_392} 条"; then
    ok "臂 C 红（EXIT=1）且**点名了投递面位次（第 ${pos_392} 条）**（★ 位次现算、★ 不写死 ★★）"
  else
    bad "臂 C 期望输出含「第 ${pos_392} 条」；实际输出：$(printf '%s' "$GATE_OUT" | tail -6 | tr '\n' ' ')"
  fi
else
  bad "臂 C 期望 EXIT=1 + 点名；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -5 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 D：**注入一处列数不同** ⇒ ★ 期望红（EXIT=1）＋ 报「列数不同」"
awk -F'\t' '
  $1 == "usage_ledger_hot" && $2 == 3 { next }
  { print }
' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/short.tsv"
make_fake_psql "$FIXTURE_DIR/short.tsv"
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "列数不同"; then
  ok "臂 D 红（EXIT=1）且报「列数不同」"
  if [ -z "$pos_344" ] && printf '%s' "$GATE_OUT" | grep -q "不在.*投递面"; then
    ok "臂 D 红（EXIT=1）且说明「该迁移**不在**投递面里 ⇒ 本次部署不会重放它」（★ 344 确实不在 ★★）"
  else
    bad "臂 D 期望输出含「不在…投递面」（★ 344 的位次实测为空：pos_344='${pos_344}' ★★）；实际输出：$(printf '%s' "$GATE_OUT" | tail -6 | tr '\n' ' ')"
  fi
else
  bad "臂 D 期望 EXIT=1 + 「列数不同」；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -5 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 E：各分支**都不在场**（★ 如 request_logs 族被 890 下线 ★★）⇒ ★ 报出但**绿**"
awk -F'\t' '$1 ~ /^request_logs(_hot)?$/ { next } { print }' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/absent.tsv"
make_fake_psql "$FIXTURE_DIR/absent.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "都不在场"; then
  ok "臂 E 绿（EXIT=0）且把「都不在场」报出来了（★ 不判红 ⇒ 免得恒红 ★★）"
else
  bad "臂 E 期望 EXIT=0 + 「都不在场」；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 F：**只有部分分支在场** ⇒ ★ 报出但**绿**（EXIT=0）"
awk -F'\t' '$1 == "credit_ledger_hot" { next } { print }' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/half.tsv"
make_fake_psql "$FIXTURE_DIR/half.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "只有部分分支在场"; then
  ok "臂 F 绿（EXIT=0）且把「只有部分分支在场」报出来了"
else
  bad "臂 F 期望 EXIT=0 + 「只有部分分支在场」；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 G：★ **解析面坏**（★ 合成仓里台账为空 ★★）⇒ ★ 期望 **ABORT（EXIT=2）**"
note "  ⇒ ★ 这一臂钉的是「门坏了」与「判据红」必须**分得开**"
FAKEROOT="$FIXTURE_DIR/fakerepo"
mkdir -p "$FAKEROOT/scripts/checks" "$FAKEROOT/sql/migrations/startup"
cp "$GATE" "$FAKEROOT/scripts/checks/legacy-select-star-column-order-gate.sh"
# ★★ 合成仓也要有**共享库**（★ L34 七轮起、★ 投递面解析搬进了 scripts/lib/deploy-surface.sh ★★）★★
mkdir -p "$FAKEROOT/scripts/lib" && cp "$REPO_ROOT/scripts/lib/deploy-surface.sh" "$FAKEROOT/scripts/lib/deploy-surface.sh"
# ★ 台账**故意留空** ⇒ ★ 解析面为空 ⇒ ★ 门必须 ABORT 而不是判红 ★
printf 'package startup\n\n// 故意留空的台账\n' >"$FAKEROOT/sql/migrations/startup/hot_surface_select_star_contract_test.go"
rc=0
SELECT_STAR_GATE_DATABASE_URL="postgres://fixture/fixture" \
SELECT_STAR_GATE_PSQL_BIN="$FIXTURE_DIR/fake-psql" \
  bash "$FAKEROOT/scripts/checks/legacy-select-star-column-order-gate.sh" >"$FIXTURE_DIR/out.txt" 2>&1 || rc=$?
out="$(cat "$FIXTURE_DIR/out.txt")"
if [ "$rc" = "2" ] && printf '%s' "$out" | grep -q "ABORT"; then
  ok "臂 G ABORT（EXIT=2）且写明 ABORT（★ 门坏了 ≠ 判据红 ★★）"
else
  bad "臂 G 期望 EXIT=2 + ABORT；实际 ${rc}；输出：$(printf '%s' "$out" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 H：★ **查询面坏**（★ public 有 600 个关系、★ 而列序查询返回 **0 行** ★★）"
note "  ⇒ ★ 这一臂钉的是本轮**真跑时撞到的那类缺陷**（★ 过滤条件写错 ⇒ 恒绿 ★★）"
: >"$FIXTURE_DIR/emptycols.tsv"
make_fake_psql "$FIXTURE_DIR/emptycols.tsv" 600
run_gate
if [ "$GATE_RC" = "2" ] && printf '%s' "$GATE_OUT" | grep -q "查询面坏了"; then
  ok "臂 H ABORT（EXIT=2）且点名「查询面坏了」（★ 恒绿形态被当场掐住 ★★）"
else
  bad "臂 H 期望 EXIT=2 + 「查询面坏了」；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 I：★ **三路漂移**（★ 340 那一族：父表 ∪ 月分区 ∪ default ★★）⇒ ★ 期望红"
note "  ⇒ ★ 这一臂钉的是 ★★「N 路比对」★★（★ ★★ 两路比对看不见第三路 ★★）★"
awk -F'\t' '
  $1 == "request_wal_default" && $3 == "created_at" { print $1 "\t" $2 "\t" $3 "\ttext"; next }
  { print }
' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/drift3.tsv"
make_fake_psql "$FIXTURE_DIR/drift3.tsv"
run_gate
if [ "$GATE_RC" = "1" ] \
   && printf '%s' "$GATE_OUT" | grep -q "340_create_partition_query_views.sql" \
   && printf '%s' "$GATE_OUT" | grep -q "request_wal_default"; then
  ok "臂 I 红（EXIT=1）且点名了**三路里的第三路**（★ request_wal_default ★）"
else
  bad "臂 I 期望 EXIT=1 + 点名 340 族 + request_wal_default；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -6 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 J：★ **三路里缺一路**（★ default 分支不在场 ★★）⇒ ★ 报出但**绿**（EXIT=0）"
awk -F'\t' '$1 == "request_wal_default" { next } { print }' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/halfn.tsv"
make_fake_psql "$FIXTURE_DIR/halfn.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "只有部分分支在场" \
   && printf '%s' "$GATE_OUT" | grep -q "request_wal_default"; then
  ok "臂 J 绿（EXIT=0）且把「只有部分分支在场」报出来了（★ ★★ 缺场那一支本身就没法并 ★★）★"
else
  bad "臂 J 期望 EXIT=0 + 「只有部分分支在场」+ 点名 request_wal_default；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -5 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
printf '\nlegacy-select-star-column-order-gate.test: PASS=%s FAIL=%s\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0

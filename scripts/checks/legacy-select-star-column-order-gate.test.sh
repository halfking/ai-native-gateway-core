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
# ★★ 臂（★ 每一条都断言**真实退出码**、★ 不看输出长相 ★★）★★
#   A 无 DB 地址 ⇒ SKIP（EXIT=0、★ 但输出必须写明「不是通过」★★）
#   B 全部对齐 ⇒ PASS（EXIT=0）★ 且**对齐数 == 11**（★ 登记表条数 ★★）
#   C 一对**列序漂移**（★ 同列数、★ 位置类型不同 ★★）⇒ 红（EXIT=1）＋ 点名该视图
#   D 一对**列数不同** ⇒ 红（EXIT=1）＋ 报「列数不同」
#   E 两侧**都不在场** ⇒ 报出但**绿**（EXIT=0）（★ 不许恒红 ★★）
#   F **只有一侧在场** ⇒ 报出但**绿**（EXIT=0）
#   G 登记表**解析面为空**（★ 合成仓 ★★）⇒ ABORT（EXIT=2）（★ 门坏了 ≠ 判据红 ★★）
#
# ★★ 变异自证（★ 本门自己也要证明「它能咬住东西」 ★★）★★
#   ★ 臂 C／D 就是**注入真实漂移** ⇒ ★ **门必须变红** ★★
#   ★ 臂 G 是**注入解析面损坏** ⇒ ★ **门必须 ABORT 而不是判红** ★★
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

GATE="scripts/checks/legacy-select-star-column-order-gate.sh"
FIXTURE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/select-star-order-fixture.XXXXXX")"
trap 'rm -rf "$FIXTURE_DIR"' EXIT

PASS=0
FAIL=0
note() { printf '%s\n' "$*"; }
ok()  { PASS=$((PASS+1)); printf '  ✔ %s\n' "$*"; }
bad() { FAIL=$((FAIL+1)); printf '  ✘ %s\n' "$*" >&2; }

# ★★ 11 对视图（★ 形态＝`SELECT * FROM <hot> UNION ALL SELECT * FROM <parent>` ★★）★★
PAIRS="request_logs_with_current_month request_logs_hot request_logs
usage_ledger_with_current_month usage_ledger_hot usage_ledger
request_wal_with_current_month request_wal_hot request_wal
routing_decision_log_with_current_month routing_decision_log_hot routing_decision_log
credential_model_index_with_current_month credential_model_index_hot credential_model_index
tool_usage_stats_with_current_month tool_usage_stats_hot tool_usage_stats
credit_ledger_with_current_month credit_ledger_hot credit_ledger
request_logs_bodies_with_current_month request_logs_bodies_hot request_logs_bodies
model_probe_runs_with_current_month model_probe_runs_hot model_probe_runs
candidate_failure_logs_with_current_month candidate_failure_logs_hot candidate_failure_logs
session_bodies_unified session_bodies_hot session_bodies"

# ★★ 假 psql（★ 读预置夹具、★ **不碰任何真实数据库** ★★）★★
# ★★ 它要答**两个**查询（★ ★★ 门里有【查询面自证】★★）：★★
# ★★   ① 「public schema 里有多少个关系」⇒ ★ 答计数 ★★
# ★★   ② 「public schema 的列序全表」⇒ ★ 答列序夹具 ★★
make_fake_psql() {
  local cols_fixture="$1"
  local total="${2:-600}"
  cat >"$FIXTURE_DIR/fake-psql" <<FAKEPSQL
#!/usr/bin/env bash
# ★ 假 psql：★ 忽略全部参数、★ 按 SQL 里的标记分答（★ -- select-total / -- select-columns ★★）
case "\$*" in
  *select-total*) printf '%s\n' "${total}" ;;
  *) cat "${cols_fixture}" ;;
esac
FAKEPSQL
  chmod +x "$FIXTURE_DIR/fake-psql"
}

# ★★ 造一份「全部对齐」的列序夹具（★ 每对两侧各 3 列、★ 完全一致 ★★）★★
make_aligned_fixture() {
  local out="$1"
  : >"$out"
  printf '%s\n' "$PAIRS" | while read -r view a b; do
    [ -n "$view" ] || continue
    for rel in "$a" "$b"; do
      {
        printf '%s\t1\tid\tbigint\n' "$rel"
        printf '%s\t2\tcreated_at\ttimestamp with time zone\n' "$rel"
        printf '%s\t3\tpayload\ttext\n' "$rel"
      } >>"$out"
    done
  done
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
note "★ 臂 B：两侧列序**全部对齐** ⇒ ★ 期望绿（EXIT=0）＋ 对齐数 == 11"
make_aligned_fixture "$FIXTURE_DIR/aligned.tsv"
make_fake_psql "$FIXTURE_DIR/aligned.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "PASS" \
   && printf '%s' "$GATE_OUT" | grep -q "对齐 11 对"; then
  ok "臂 B 绿（EXIT=0）且对齐数 == 11"
else
  bad "臂 B 期望 EXIT=0 + 对齐 11 对；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 C：**注入一处列序漂移**（★ 同列数、★ 位置 3 类型不同 ★★）⇒ ★ 期望红（EXIT=1）＋ 点名"
awk -F'\t' '
  $1 == "candidate_failure_logs_hot" && $3 == "payload" { print $1 "\t" $2 "\t" $3 "\tinteger"; next }
  { print }
' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/drift.tsv"
make_fake_psql "$FIXTURE_DIR/drift.tsv"
run_gate
if [ "$GATE_RC" = "1" ] \
   && printf '%s' "$GATE_OUT" | grep -q "candidate_failure_logs_with_current_month" \
   && printf '%s' "$GATE_OUT" | grep -q "列序已漂移"; then
  ok "臂 C 红（EXIT=1）且点名了漂移的那一对"
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
else
  bad "臂 D 期望 EXIT=1 + 「列数不同」；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -5 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 E：某对**两侧都不在场**（★ 如 request_logs 族被 890 下线 ★★）⇒ ★ 报出但**绿**"
awk -F'\t' '$1 ~ /^request_logs(_hot)?$/ { next } { print }' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/absent.tsv"
make_fake_psql "$FIXTURE_DIR/absent.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "两侧都不在场"; then
  ok "臂 E 绿（EXIT=0）且把「两侧都不在场」报出来了（★ 不判红 ⇒ 免得恒红 ★★）"
else
  bad "臂 E 期望 EXIT=0 + 「两侧都不在场」；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 F：**只有一侧在场** ⇒ ★ 报出但**绿**（EXIT=0）"
awk -F'\t' '$1 == "credit_ledger_hot" { next } { print }' "$FIXTURE_DIR/aligned.tsv" >"$FIXTURE_DIR/half.tsv"
make_fake_psql "$FIXTURE_DIR/half.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "只有一侧在场"; then
  ok "臂 F 绿（EXIT=0）且把「只有一侧在场」报出来了"
else
  bad "臂 F 期望 EXIT=0 + 「只有一侧在场」；实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 G：★ **解析面坏**（★ 合成仓里登记表为空 ★★）⇒ ★ 期望 **ABORT（EXIT=2）**"
note "  ⇒ ★ 这一臂钉的是「门坏了」与「判据红」必须**分得开**"
FAKEROOT="$FIXTURE_DIR/fakerepo"
mkdir -p "$FAKEROOT/scripts/checks" "$FAKEROOT/sql/migrations/startup"
cp "$GATE" "$FAKEROOT/scripts/checks/legacy-select-star-column-order-gate.sh"
# ★ 登记表**故意留空** ⇒ ★ 解析面为空 ⇒ ★ 门必须 ABORT 而不是判红 ★
printf 'package startup\n\n// 故意留空的登记表\n' >"$FAKEROOT/sql/migrations/startup/hot_surface_select_star_contract_test.go"
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
printf '\nlegacy-select-star-column-order-gate.test: PASS=%s FAIL=%s\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0

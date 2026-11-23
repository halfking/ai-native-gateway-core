#!/usr/bin/env bash
# =====================================================================
# scripts/checks/deploy-ledger-coverage-gate.test.sh —— 覆盖门（★ 方案 C ★）的夹具测试
#
# ★★ 为什么需要它（★ 一句话说清 ★★）：
#   ★ 覆盖门的判据是【集合运算】：★ 「投递面 357 条」∩「权威账里的 marker」
#   ★ ★★ ⇒ ★★ **它不需要真的连上 PG** ★★ ⇒ ★★ ⇒ ★ ⇒ **判据逻辑可以在
#   ★★ 没有 docker 的机器上验证** ★★ ⇒ ★★ ⇒ ★ 这就是本文件存在的理由 ★★
#   ★★ ⇒ ★★ 而真库红/绿两态（★ 空库 ⇒ 红、★ 全覆盖 ⇒ 绿 ★★）由
#   ★★ `scripts/checks/fresh-install-smoke.sh` 那条链覆盖 ★★。
#
# ★ ★★ 三态自证（★ 本文件的全部内容 ★★）：
#   ★ 臂 A：★ 账表**不存在** ⇒ ★ 全部 357 条都是缺口 ⇒ ★ **红（EXIT=1）** ★
#   ★ 臂 B：★ 账表存在但**一行都没有** ⇒ ★ 同样全部缺口 ⇒ ★ **红（EXIT=1）** ★
#   ★ 臂 C：★ 账表存在、★ **投递面每一条都有记录** ⇒ ★ **无断层** ⇒ ★ **绿（EXIT=0）** ★
#   ★ 臂 D：★ 账表存在、★ **只有一部分有记录** ⇒ ★ **缺口 > 0** ⇒ ★ **红（EXIT=1）** ★
#   ★ 臂 E：★ 记录存在但**指纹为空（legacy 行）** ⇒ ★ ★ **不算断层** ⇒ ★ **绿（EXIT=0）** ★
#   ★ 臂 F：★ 连不上库（★ psql 全失败 ★）⇒ ★ **ABORT（EXIT=2）** ★
#
# ★★ 夹具实现（★ 一个假 psql ★★）：★ 它按「问的 SQL」返回预置答案 ★★
#   ★ 门问三类 SQL：★ ① 表存在？★ ② 账里所有 marker+指纹 ★ ③ select 1 探测 ★★
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

GATE="scripts/checks/deploy-ledger-coverage-gate.sh"
FIXTURE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/deploy-ledger-fixture.XXXXXX")"
trap 'rm -rf "$FIXTURE_DIR"' EXIT

PASS=0
FAIL=0
note() { printf '%s\n' "$*"; }
ok()   { printf '  ✔ %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  ✘ %s\n' "$*" >&2; FAIL=$((FAIL+1)); }

# ── 造一个假 psql（★ 按 SQL 内容返回预置答案 ★★）─────────────────────
# ★ 用法：★ make_fake_psql <账表存在:0|1> <账内容文件> [<全失败:0|1>]
make_fake_psql() {
  local exists="$1" content_file="$2" fail_all="${3:-0}"
  # ★★★ ★★ heredoc 的展开纪律（★ 本轮实测撞到、★ 已修 ★★）★★★：
  # ★★★ ① **未加引号的 heredoc（★ `<<EOF` ★）会做命令替换** ⇒ ★★★ ⇒ ★★★
  # ★★★    **注释里的示例代码会被 shell 真执行**（★ 本轮就撞上了：★ 注释里写了
  # ★★★    一个含 `$(` 的示例 ⇒ ★★★ 它被展开 ⇒ ★★★ 报
  # ★★★    「command not found」★ ⇒ ★★★ ⇒ ★★★ **⇒ heredoc 必须用加引号的形式**
  # ★★★    （★ `<<'PSQLFIXTURE'` ★⇒ ★★ **禁止一切展开** ★★）★★ ★★★
  # ★★★ ② 但加引号后 `${exists}` 之类**不会展开** ⇒ ★★★ ⇒ ★★★ **改用占位符**
  # ★★★    ＋ 生成后用 sed 替换（★ ★ 两者必须配套、★ 不能只做一半 ★★）★★★ ★★★
  cat >"$FIXTURE_DIR/fake-psql" <<'PSQLFIXTURE'
#!/usr/bin/env bash
# 假 psql：按传入的 SQL 判定该回什么
sql=""
for a in "$@"; do
  case "$a" in
    select*|SELECT*) sql="$sql $a" ;;
  esac
done
if [ "@@FAIL_ALL@@" = "1" ]; then
  exit 1
fi
case "$sql" in
  *"pg_class"*)  printf '%s\n' "@@EXISTS@@" ;;
  *"gateway_db_revision_sequences"*"count"*)
    # count 查询必须回行数（不是账内容）
    # ⇒ 假 psql 若把它当「账内容查询」处理、会把整张账灌进 count 的返回值
    # ⇒ 门拿到的 ledger_rows 就是一堆多行文本 ⇒ 这是夹具的缺陷、不是什么门缺陷
    #
    # 判据形态（为什么不用带裸括号的 count 模式）：
    #   case 模式里的 `(` 未转义 ⇒ bash 5.2 报语法错、而 bash 3.2 放过
    #   ⇒ 形态里不能带裸括号 ⇒ 改成「含表名 且 含 count」两段匹配
    if [ "@@EXISTS@@" = "0" ]; then
      exit 1
    fi
    if [ -f "@@CONTENT_FILE@@" ]; then
      wc -l < "@@CONTENT_FILE@@" | tr -d ' '
    else
      printf '0'
    fi
    ;;
  *"gateway_db_revision_sequences"*)
    if [ "@@EXISTS@@" = "0" ]; then
      exit 1
    fi
    if [ -f "@@CONTENT_FILE@@" ]; then
      cat "@@CONTENT_FILE@@"
    fi
    ;;
  *"select 1"*)  printf '1\n' ;;
  *)             printf '\n' ;;
esac
exit 0
PSQLFIXTURE
  # ★ 把占位符换成真实值（★ 必须在 heredoc 之后、★ 因为 heredoc 是字面量 ★）
  sed -i.bak \
    -e "s|@@EXISTS@@|${exists}|g" \
    -e "s|@@FAIL_ALL@@|${fail_all}|g" \
    -e "s|@@CONTENT_FILE@@|${content_file}|g" \
    "$FIXTURE_DIR/fake-psql"
  rm -f "$FIXTURE_DIR/fake-psql.bak"
  chmod +x "$FIXTURE_DIR/fake-psql"
}

# ── 跑门并取退出码（★ ★ 不 pipe！★ 免得退出码被管道吃掉 ★★）─────────
run_gate() {
  local rc=0
  DEPLOY_LEDGER_GATE_DATABASE_URL="postgres://fixture/fixture" \
  DEPLOY_LEDGER_GATE_PSQL_BIN="$FIXTURE_DIR/fake-psql" \
    bash "$GATE" >"$FIXTURE_DIR/out.txt" 2>&1 || rc=$?
  GATE_OUT="$(cat "$FIXTURE_DIR/out.txt")"
  GATE_RC="$rc"
}

# ── 取投递面清单（★ 供夹具构造「全覆盖」的账内容 ★★）─────────────────
plan_file="$FIXTURE_DIR/plan.txt"
awk '
  BEGIN { in_files = 0 }
  {
    line = $0
    if (!in_files) { if (line ~ /^files=\(/) { in_files = 1 }; next }
    if (line ~ /^\)/) { in_files = 0; next }
    trimmed = line
    sub(/^[ \t]+/, "", trimmed)
    if (trimmed ~ /^#/ || trimmed == "") { next }
    # ★ ★★ 剥前缀**不用正则**（★ 跨 awk 实现一致 ★★）：★★
    # ★ ★★ 根因（★ 本轮在生产 Linux 上实测坐实 ★★）：★★
    # ★ ★★ mawk（★ Linux 生产 ★）把正则里的 `\$` 当成「字面反斜杠 + 美元」★★
    # ★ ★★ ⇒ ★★ 剥前缀失败 ⇒ ★★ 而 BSD awk（★ macOS ★）剥成功 ⇒ ★★
    # ★ ★★ ⇒ ★★ **同一段 awk 在两个平台行为不同** ⇒ ★★ ⇒ ★★ **⇒ 正则里不能写反斜杠美元**★★
    # ★ ★★ ⇒ ★★ **⇒ 改用【index + substr】按固定前缀长度截取** ★★（★ 零正则方言依赖 ★★）★★
    pfx = "$ROOT_DIR/sql/migrations/startup/"
    p = index(line, pfx)
    if (p > 0) {
      rest = substr(line, p + length(pfx))
      sub(/^[ \t"]+/, "", rest)
      sub(/[\t"]+$/, "", rest)
      # ★ 只接受「纯文件名 + .sql」形态（★ 排除带参数的行 ★）★★
      if (rest ~ /^[A-Za-z0-9_.-]+[.]sql$/) { print rest }
    }
  }
' scripts/apply-db-revision-sequence.sh | awk '!seen[$0]++' | grep -v '\.down\.sql$' >"$plan_file"
plan_count=$(wc -l <"$plan_file" | tr -d ' ')
note "投递面（夹具）：${plan_count} 条"

SEQ="session-summary-and-integrity-2026-09"

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 A：账表**不存在** ⇒ ★ 全部缺口 ⇒ ★ 期望红（EXIT=1）"
make_fake_psql 0 /dev/null
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "没有记录"; then
  ok "臂 A 红（EXIT=1）且报「没有记录」"
else
  bad "臂 A 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 B：账表存在但**一行都没有** ⇒ ★ 同样全部缺口 ⇒ ★ 期望红（EXIT=1）"
: >"$FIXTURE_DIR/empty.tsv"
make_fake_psql 1 "$FIXTURE_DIR/empty.tsv"
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "没有记录"; then
  ok "臂 B 红（EXIT=1）且报「没有记录」"
else
  bad "臂 B 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 C：投递面**每一条都有记录** ⇒ ★ 无断层 ⇒ ★ 期望绿（EXIT=0）"
: >"$FIXTURE_DIR/full.tsv"
while IFS= read -r base; do
  [ -n "$base" ] || continue
  printf '%s:%s\tdeadbeef\n' "$SEQ" "$base" >>"$FIXTURE_DIR/full.tsv"
done <"$plan_file"
make_fake_psql 1 "$FIXTURE_DIR/full.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "无断层"; then
  ok "臂 C 绿（EXIT=0）且报「无断层」"
else
  bad "臂 C 期望 EXIT=0、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 D：**只有一部分**有记录 ⇒ ★ 缺口 > 0 ⇒ ★ 期望红（EXIT=1）"
: >"$FIXTURE_DIR/partial.tsv"
head -10 "$plan_file" | while IFS= read -r base; do
  [ -n "$base" ] || continue
  printf '%s:%s\tdeadbeef\n' "$SEQ" "$base" >>"$FIXTURE_DIR/partial.tsv"
done
make_fake_psql 1 "$FIXTURE_DIR/partial.tsv"
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "缺口"; then
  ok "臂 D 红（EXIT=1）且报「缺口」"
else
  bad "臂 D 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 E：记录存在但**指纹为空（legacy 行）** ⇒ ★ **不算断层** ⇒ ★ 期望绿（EXIT=0）"
: >"$FIXTURE_DIR/legacy.tsv"
while IFS= read -r base; do
  [ -n "$base" ] || continue
  printf '%s:%s\t<NULL>\n' "$SEQ" "$base" >>"$FIXTURE_DIR/legacy.tsv"
done <"$plan_file"
make_fake_psql 1 "$FIXTURE_DIR/legacy.tsv"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "legacy 记账行"; then
  ok "臂 E 绿（EXIT=0）且把 legacy 行单列（★ 不误报成断层 ★）"
else
  bad "臂 E 期望 EXIT=0、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 F：连不上库（★ psql 全失败 ★）⇒ ★ 期望 **ABORT（EXIT=2）**"
make_fake_psql 1 /dev/null 1
run_gate
if [ "$GATE_RC" = "2" ] && printf '%s' "$GATE_OUT" | grep -q "ABORT"; then
  ok "臂 F ABORT（EXIT=2）且打印 ABORT（★ 与判据红分得开 ★）"
else
  bad "臂 F 期望 EXIT=2、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 G：没有 DB 地址 ⇒ ★ 期望 **SKIP（EXIT=0）**（★ 不伪装成通过 ★）"
rc=0
bash "$GATE" >"$FIXTURE_DIR/out.txt" 2>&1 || rc=$?
if [ "$rc" = "0" ] && grep -q "SKIP" "$FIXTURE_DIR/out.txt"; then
  ok "臂 G SKIP（EXIT=0）且打印 SKIP"
else
  bad "臂 G 期望 EXIT=0、实际 ${rc}；输出：$(tail -3 "$FIXTURE_DIR/out.txt" | tr '\n' ' ')"
fi

if [ "$FAIL" -ne 0 ]; then
  printf 'deploy-ledger-coverage-gate.test: FAIL（%d 条不满足）\n' "$FAIL" >&2
  exit 1
fi
printf 'deploy-ledger-coverage-gate.test: PASS（%d 条）\n' "$PASS"
exit 0

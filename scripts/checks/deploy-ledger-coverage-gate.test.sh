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
# ★ 用法：★ make_fake_psql <账表存在:0|1> <账内容文件> [<全失败:0|1>] [<安装器那本账版本文件>]
# ★★ 2026-11-24 L34 三轮：★ 新增**第四个**输入（★ 安装器那本账的版本清单 ★★）★★
# ★★ ⇒ ★★ 因为判据升级了（★ 「缺口」要再分成【断层】与【待部署】★★）⇒ ★★ ⇒ ★★
# ★★ 门会**只读**查第二本账（★ `public.schema_migrations` ★★）★★
make_fake_psql() {
  local exists="$1" content_file="$2" fail_all="${3:-0}" instver_file="${4:-/dev/null}"
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
  *"schema_migrations"*)
    # ★★ 安装器那本账（★ L34 三轮新增的第二个只读输入 ★★）
    # ⇒ ★★ 若这个文件不存在 ⇒ ★ 模拟「表不存在 / 查不到」⇒ ★ 门应保守判红 ★★
    if [ -f "@@INSTVER_FILE@@" ]; then
      cat "@@INSTVER_FILE@@"
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
    -e "s|@@INSTVER_FILE@@|${instver_file}|g" \
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

# ── ★★ 夹具用：★ 安装器那本账的版本清单 ★★（★ 零填充 3 位、★ 与生产同形 ★★）──
# ★★ 为什么写成零填充 3 位：★ 生产上 `schema_migrations.version` 就是 `087` 这种形态 ★★
# ★★ ⇒ ★★ ⇒ ★★ **⇒ 门必须做版本号归一化**（★ 去掉前导零再比 ★★）⇒ ★★ ⇒ ★★
# ★★ **⇒ 这一条本身就是对归一化逻辑的自证** ★★
instver_all="$FIXTURE_DIR/instver-all.txt"
awk '{ if (match($0, /^[0-9]+/)) { v = substr($0, RSTART, RLENGTH) + 0; printf "%03d\n", v } }' "$plan_file" | sort -u >"$instver_all"
instver_count=$(wc -l <"$instver_all" | tr -d ' ')
note "安装器那本账（夹具）：${instver_count} 个版本"

# ★★ 挑一个「被排除在安装器那本账之外」的文件（★ 默认取投递面最后一条 ★★）
PICK="$(tail -1 "$plan_file")"
PICK_VER="$(printf '%s\n' "$PICK" | awk '{ if (match($0, /^[0-9]+/)) { print substr($0, RSTART, RLENGTH) + 0 } }')"

# ★★ 「除 PICK 外全覆盖」的权威账 ★★
: >"$FIXTURE_DIR/minus_pick.tsv"
while IFS= read -r base; do
  [ -n "$base" ] || continue
  [ "$base" = "$PICK" ] && continue
  printf '%s:%s\tdeadbeef\n' "$SEQ" "$base" >>"$FIXTURE_DIR/minus_pick.tsv"
done <"$plan_file"

# ★★ 「除 PICK 的版本外都在」的安装器那本账 ★★
awk -v v="$PICK_VER" '$1 + 0 != v + 0' "$instver_all" >"$FIXTURE_DIR/instver-minus-pick.txt"

# ── ★★ 夹具侧算【当前内容指纹】（★ 2026-11-24 L34 四轮新增 ★★）──
# ★★ 为什么夹具也要算：★ 门现在会把「账里有、★ 但指纹过期」的条目标成【内容加固重放】★★
# ★★ ⇒ ★★ 覆盖臂若还用假的指纹（★ `deadbeef` ★★）、★ 全部 356 条都会变成「过期」★★
# ★★ ⇒ ★★ ⇒ ★★ **⇒ 覆盖臂必须用【真实指纹】**（★ 而这同时是对「门算指纹的口径」的
# ★★ **交叉校验**：★ 夹具与门两套实现算出同一个指纹、★ 否则覆盖臂就不会绿 ★★）★★
if command -v sha256sum >/dev/null 2>&1; then
  FIX_SHA_CMD="sha256sum"
else
  FIX_SHA_CMD="shasum -a 256"
fi
: >"$FIXTURE_DIR/present-paths.txt"
while IFS= read -r base; do
  [ -n "$base" ] || continue
  printf '%s\n' "sql/migrations/startup/${base}" >>"$FIXTURE_DIR/present-paths.txt"
done <"$plan_file"
xargs $FIX_SHA_CMD <"$FIXTURE_DIR/present-paths.txt" 2>/dev/null | awk -v d="sql/migrations/startup/" '
  { p = $2; if (index(p, d) == 1) { b = substr(p, length(d) + 1) } else { b = p }; print b "\t" $1 }
' >"$FIXTURE_DIR/shas-all.tsv"
sha_count=$(wc -l <"$FIXTURE_DIR/shas-all.tsv" | tr -d ' ')
note "当前内容指纹（夹具）：${sha_count} 条"

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 A：账表**不存在** ⇒ ★ 全部缺口 ⇒ ★ 期望红（EXIT=1）"
make_fake_psql 0 /dev/null 0 "$instver_all"
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "断层"; then
  ok "臂 A 红（EXIT=1）且报「断层」"
else
  bad "臂 A 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 B：账表存在但**一行都没有** ⇒ ★ 同样全部缺口 ⇒ ★ 期望红（EXIT=1）"
: >"$FIXTURE_DIR/empty.tsv"
make_fake_psql 1 "$FIXTURE_DIR/empty.tsv" 0 "$instver_all"
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "断层"; then
  ok "臂 B 红（EXIT=1）且报「断层」"
else
  bad "臂 B 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 C：投递面**每一条都有记录** ⇒ ★ 无断层 ⇒ ★ 期望绿（EXIT=0）"
: >"$FIXTURE_DIR/full.tsv"
while IFS="$(printf '\t')" read -r base sha; do
  [ -n "$base" ] || continue
  printf '%s:%s\t%s\n' "$SEQ" "$base" "$sha" >>"$FIXTURE_DIR/full.tsv"
done <"$FIXTURE_DIR/shas-all.tsv"
make_fake_psql 1 "$FIXTURE_DIR/full.tsv" 0 "$instver_all"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "无断层"; then
  ok "臂 C 绿（EXIT=0）且报「无断层」"
else
  bad "臂 C 期望 EXIT=0、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
note "★ 臂 D：**只有一部分**有记录 ⇒ ★ 缺口 > 0 ⇒ ★ 期望红（EXIT=1）"
: >"$FIXTURE_DIR/partial.tsv"
head -10 "$FIXTURE_DIR/shas-all.tsv" | while IFS="$(printf '\t')" read -r base sha; do
  [ -n "$base" ] || continue
  printf '%s:%s\t%s\n' "$SEQ" "$base" "$sha" >>"$FIXTURE_DIR/partial.tsv"
done
make_fake_psql 1 "$FIXTURE_DIR/partial.tsv" 0 "$instver_all"
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "断层"; then
  ok "臂 D 红（EXIT=1）且报「断层」"
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
make_fake_psql 1 "$FIXTURE_DIR/legacy.tsv" 0 "$instver_all"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "legacy 行"; then
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

# ═══════════════════════════════════════════════════════════════════════
# ★★ 臂 H（★ L34 三轮新增 ★★）：★ 缺口的那一条**从没被应用过** ★★
# ★★ ⇒ ★ 「待部署」⇒ ★ 通道会**首次**应用它 ⇒ ★ **不是断层** ⇒ ★ 期望绿 ★★
# ★★ 这一臂是本轮**判据升级的核心自证**：★ 旧判据在这里会红（★ 恒红型误报 ★★）★★
note "★ 臂 H：缺口的那条**版本不在安装器那本账里** ⇒ ★ 待部署 ⇒ ★ 期望绿（EXIT=0）"
make_fake_psql 1 "$FIXTURE_DIR/minus_pick.tsv" 0 "$FIXTURE_DIR/instver-minus-pick.txt"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "待部署" \
   && printf '%s' "$GATE_OUT" | grep -q "$PICK"; then
  ok "臂 H 绿（EXIT=0）且把「待部署」那条报出来（★ 旧判据在这里会红 ★）"
else
  bad "臂 H 期望 EXIT=0、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
# ★★ 臂 I（★ L34 三轮新增 ★★）：★ 同样的一条缺口、★ 但它的版本**在**安装器那本账里 ★★
# ★★ ⇒ ★ 「已被别处应用过」⇒ ★ **断层**（★ 通道会重放 ★★）⇒ ★ 期望红 ★★
note "★ 臂 I：缺口的那条**版本在安装器那本账里** ⇒ ★ 断层 ⇒ ★ 期望红（EXIT=1）"
make_fake_psql 1 "$FIXTURE_DIR/minus_pick.tsv" 0 "$instver_all"
run_gate
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "断层" \
   && printf '%s' "$GATE_OUT" | grep -q "$PICK"; then
  ok "臂 I 红（EXIT=1）且点名了那条断层"
else
  bad "臂 I 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
# ★★ 臂 J（★ L34 三轮新增 ★★）：★ 文件名**无数字版本号** ⇒ ★ 不可判定 ★★
# ★★ ⇒ ★ 保守计入断层 ⇒ ★ 期望红 ★★
# ★★ 这一臂用一个**合成仓**（★ 只放两个投递条目 ★★）⇒ ★★ 所以要**另放一份门脚本**
# ★★ ⇒ ★★ 因为门按**自己所在目录**推导仓根（★ 合成仓里也要有同样的相对布局 ★★）★★
note "★ 臂 J：缺口那条**文件名无数字版本号** ⇒ ★ 不可判定 ⇒ ★ 期望红（EXIT=1）"
FAKEROOT="$FIXTURE_DIR/fakerepo"
mkdir -p "$FAKEROOT/scripts/checks" "$FAKEROOT/sql/migrations/startup"
cp "$GATE" "$FAKEROOT/scripts/checks/deploy-ledger-coverage-gate.sh"
cat >"$FAKEROOT/scripts/apply-db-revision-sequence.sh" <<'FAKEDEPLOY'
#!/usr/bin/env bash
files=(
  "$ROOT_DIR/sql/migrations/startup/123_pick.sql"
  "$ROOT_DIR/sql/migrations/startup/alpha_no_version.sql"
)
FAKEDEPLOY
# ★★ 合成仓的两个文件**要在磁盘上真的存在**（★ 否则它们会先被判成「不在场」★★）
printf 'SELECT 1;\n' >"$FAKEROOT/sql/migrations/startup/123_pick.sql"
printf 'SELECT 1;\n' >"$FAKEROOT/sql/migrations/startup/alpha_no_version.sql"
pick_sha="$($FIX_SHA_CMD "$FAKEROOT/sql/migrations/startup/123_pick.sql" | cut -d' ' -f1)"
printf '%s:%s\t%s\n' "$SEQ" "123_pick.sql" "$pick_sha" >"$FIXTURE_DIR/fakeledger.tsv"
printf '999\n' >"$FIXTURE_DIR/fakeinstver.txt"
rc=0
DEPLOY_LEDGER_GATE_DATABASE_URL="postgres://fixture/fixture" \
DEPLOY_LEDGER_GATE_PSQL_BIN="$FIXTURE_DIR/fake-psql" \
  bash "$FAKEROOT/scripts/checks/deploy-ledger-coverage-gate.sh" >"$FIXTURE_DIR/out.txt" 2>&1 || rc=$?
GATE_OUT="$(cat "$FIXTURE_DIR/out.txt")"
GATE_RC="$rc"
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "不可判定"; then
  ok "臂 J 红（EXIT=1）且报「不可判定」（★ 无版本号 ⇒ 保守计入断层 ★）"
else
  bad "臂 J 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
# ★★ 臂 K（★ L34 四轮新增 ★★）：★ 账里有记录、★ 但**指纹过期** ★★
# ★★ ⇒ ★ 通道会**按当前内容重放**它（★ 通道**设计内**的行为 ★★）⇒ ★ **绿、但要报出来** ★★
# ★★ 这一臂钉的是「旧口径把它当覆盖」那个**盲区**：★ 现在它必须被单独点名 ★★
note "★ 臂 K：那条的**指纹过期** ⇒ ★ 内容加固重放 ⇒ ★ 期望绿（EXIT=0）＋ 报出来"
awk -F'\t' -v key="${SEQ}:${PICK}" '{ if ($1 == key) { print $1 "\t" "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef" } else { print } }' "$FIXTURE_DIR/full.tsv" >"$FIXTURE_DIR/stale.tsv"
make_fake_psql 1 "$FIXTURE_DIR/stale.tsv" 0 "$instver_all"
run_gate
if [ "$GATE_RC" = "0" ] && printf '%s' "$GATE_OUT" | grep -q "内容加固" \
   && printf '%s' "$GATE_OUT" | grep -q "$PICK"; then
  ok "臂 K 绿（EXIT=0）且把「内容加固重放」那条单独报出来（★ 不当覆盖、★ 也不当断层 ★）"
else
  bad "臂 K 期望 EXIT=0、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════
# ★★ 臂 L（★ L34 四轮新增 ★★）：★ 投递面里的文件**在磁盘上不在场** ★★
# ★★ ⇒ ★ 通道跑到它会**直接失败**（★ 那不是重放风险、★ 是更硬的错 ★★）⇒ ★ 期望红 ★★
note "★ 臂 L：投递面里有个文件**磁盘上不在场** ⇒ ★ 期望红（EXIT=1）"
FAKEROOT2="$FIXTURE_DIR/fakerepo2"
mkdir -p "$FAKEROOT2/scripts/checks" "$FAKEROOT2/sql/migrations/startup"
cp "$GATE" "$FAKEROOT2/scripts/checks/deploy-ledger-coverage-gate.sh"
cat >"$FAKEROOT2/scripts/apply-db-revision-sequence.sh" <<'FAKEDEPLOY'
#!/usr/bin/env bash
files=(
  "$ROOT_DIR/sql/migrations/startup/ghost_missing.sql"
)
FAKEDEPLOY
printf '%s:%s\t%s\n' "$SEQ" "ghost_missing.sql" "deadbeef" >"$FIXTURE_DIR/ghost-ledger.tsv"
rc=0
DEPLOY_LEDGER_GATE_DATABASE_URL="postgres://fixture/fixture" \
DEPLOY_LEDGER_GATE_PSQL_BIN="$FIXTURE_DIR/fake-psql" \
  bash "$FAKEROOT2/scripts/checks/deploy-ledger-coverage-gate.sh" >"$FIXTURE_DIR/out.txt" 2>&1 || rc=$?
GATE_OUT="$(cat "$FIXTURE_DIR/out.txt")"
GATE_RC="$rc"
if [ "$GATE_RC" = "1" ] && printf '%s' "$GATE_OUT" | grep -q "不在场"; then
  ok "臂 L 红（EXIT=1）且报「不在场」（★ 通道跑到它会直接失败 ★）"
else
  bad "臂 L 期望 EXIT=1、实际 ${GATE_RC}；输出：$(printf '%s' "$GATE_OUT" | tail -4 | tr '\n' ' ')"
fi

if [ "$FAIL" -ne 0 ]; then
  printf 'deploy-ledger-coverage-gate.test: FAIL（%d 条不满足）\n' "$FAIL" >&2
  exit 1
fi
printf 'deploy-ledger-coverage-gate.test: PASS（%d 条）\n' "$PASS"
exit 0

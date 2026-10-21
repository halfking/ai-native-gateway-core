#!/usr/bin/env bash
# healthcheck-dbaudit-scripts.sh —— `.db-audit/sql/*.sql`（38 个手工审计脚本）的**可执行性体检**
#
# # 为什么要有这个工具（2026-10-21，R93 轮实测）
#
# 这批脚本是**手工审计脚本**，设计环境是「psql 与 PG 同机」（它们给 252 生产机用），
# 所以 `PSQL="psql -U llm_gateway -d llm_gateway ..."` **没有 `-h`**。
# ⇒ 在开发机上跑 ⇒ psql 走**本地 Unix socket** ⇒ **一条 SQL 都没执行**
# ⇒ 而它们用 `-v ON_ERROR_STOP=0` ⇒ 报错不中断 ⇒ 连「ERROR 行」都没有
# ⇒ ⇒ **静默失败的最深一层：整个脚本零读数**。
#
# ★ 所以本工具**不改被测脚本**，只改**运行环境**（注入 `PGHOST=127.0.0.1`）。
#
# # 量具自检（★ 跑在读数之前，不过就中止）
#
# ① **建库必须自检**：建完能连上 + 关键表在位。
# ② **必须验「走的是 TCP 不是 socket」**：查 `inet_server_addr()`。
#    ★ 这条是本工具的命门：脚本没有 `-h` ⇒ 不注入 `PGHOST` ⇒ 连的是 socket ⇒ 零读数。
# ③ **必须验「连的是哪个库」**：查 `current_database()` 与目标库名逐字相等。
# ④ **库名必须全小写**（PostgreSQL 折叠未加引号的标识符）。
#
# # 破坏性腿的守卫（★ 与仓内「守卫线」同款纪律）
#
# ★★ `.db-audit/sql/17_bench_columnar.sql` **有写操作**
# （`CREATE SCHEMA audit_scratch` / `CREATE TABLE` / `ALTER TABLE` / `INSERT`）
# ⇒ ⇒ 本工具**只允许**跑在**一次性库**上 ⇒ 目标库名必须匹配 `itgate_*` ⇒ 否则中止。
# ⇒ ⇒ 跑在长驻库上 = 把审计脚本变成事故。
#
# # 双口径交叉校验（★ 本工具的「自证有牙」）
#
# 口径 A = **逐单元执行**（本工具的主口径）：把脚本拆成「单条 SQL」与「heredoc 块」，
#          逐个执行 ⇒ 每条错误都能归因到 **文件:行号**。
# 口径 B = **整脚本执行**：原样 `bash <脚本>` ⇒ 复现运维真实看到的 ERROR 行。
#
# ★ **两个口径的 ERROR 总数必须相等** ⇒ 不等即说明**本工具有盲区** ⇒ 拒绝出读数（exit 3）。
# ★★ 这道校验是 R93 轮**真的抓到了本工具的两个盲区**：
#   ① 第一版抽取器「取行内最后一个 `"` 当闭合引号」⇒ **SQL 里没有 `"` 时找不到闭合点**
#      ⇒ 整条语句被跳过 ⇒ 37 条错误只读到 32 条（**量具漏放**）。
#   ② 第一版只认 `-c "..."` ⇒ **heredoc 块**（`<<'SQL'`）整块看不见
#   ③ 第三版用 `psql -f` 跑块 ⇒ 报错行前缀是 `psql:<文件>:<行>: ERROR:`
#      ⇒ **不以 `ERROR:` 开头** ⇒ 被 `grep -E '^(ERROR|FATAL):'` 漏掉 ⇒ 又差 3。
#      ⇒ `17_bench_columnar.sql` 的 3 条错误凭空消失。
#   ⇒ ⇒ **「门绿」≠「量具有牙」** ⇒ 必须有一条**能判出量具自己瞎了**的判据。
#
# # 用法
#
#   scripts/audit/healthcheck-dbaudit-scripts.sh                # 自建一次性形态库 → 体检 → 回收
#   scripts/audit/healthcheck-dbaudit-scripts.sh --db <name>    # 复用已有的一次性库（须 itgate_* 前缀）
#   scripts/audit/healthcheck-dbaudit-scripts.sh --keep          # 体检完不 DROP（调试用）
#   scripts/audit/healthcheck-dbaudit-scripts.sh --cross-check  # 加跑「整脚本」口径并对拍
#   scripts/audit/healthcheck-dbaudit-scripts.sh --verify        # 台账结构自检（不跑库）
#
# # 产出
#
#   scripts/audit/dbaudit-scripts-ledger.tsv —— 逐条错误的**族级分类 + 处置**台账
#   （「不修族」也必须登记：否则下一个人会把「已定性」误读成「没人看过」）
#
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT" || exit 2

DB=""
KEEP=0
VERIFY=0
CROSSCHECK=0
while [ $# -gt 0 ]; do
  case "$1" in
    --db)
      [ $# -ge 2 ] || { echo "--db 需要一个参数" >&2; exit 2; }
      DB="$2"; shift 2 ;;
    --db=*) DB="${1#--db=}"; shift ;;
    --keep) KEEP=1; shift ;;
    --verify) VERIFY=1; shift ;;
    --cross-check) CROSSCHECK=1; shift ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

LEDGER="scripts/audit/dbaudit-scripts-ledger.tsv"
OUT="${R93_OUT:-/tmp/dbaudit-healthcheck}"

# ── 模式 1：--verify（只查台账自洽，不连库）──────────────────────────────
if [ "$VERIFY" = "1" ]; then
  if [ ! -f "$LEDGER" ]; then
    echo "✗ 台账不存在：${LEDGER}（首次生成请先跑一次体检）"
    exit 2
  fi
  echo "== 台账自检 =="
  n=$(awk -F'\t' 'NF>0 && $1 !~ /^#/ {n++} END{print n+0}' "$LEDGER")
  echo "  台账有效行数 = ${n}"
  if [ "$n" -lt 10 ]; then
    echo "  ✗ 台账有效行数过少 ⇒ 台账不是真读数（门会恒绿）"
    exit 2
  fi
  # 每行必须填满处置字段（族 / 处置 / 依据）
  bad=$(awk -F'\t' 'NF>0 && $1 !~ /^#/ && (NF<6 || $5=="" || $6=="") {n++} END{print n+0}' "$LEDGER")
  if [ "$bad" != "0" ]; then
    echo "  ✗ 有 ${bad} 行缺处置字段（族 / 处置 / 依据）⇒ 台账不可信"
    exit 2
  fi
  echo "  ✓ 台账结构自检通过（${n} 行，处置字段齐全）"
  exit 0
fi

# ── 守卫：目标库必须是「一次性库」────────────────────────────────────────
if [ -n "$DB" ]; then
  case "$DB" in
    itgate_*) : ;;
    *)
      echo "✗ 拒绝在非一次性库上跑：'${DB}'"
      echo "  理由：.db-audit/sql/17_bench_columnar.sql 有 CREATE/ALTER/INSERT ⇒ 写操作"
      echo "  ⇒ 只允许 itgate_* 前缀的一次性库（生产/长驻库 ⇒ 事故）"
      exit 2
      ;;
  esac
fi

# ── 建库（自建 or 复用）─────────────────────────────────────────────────
CREATED=0
BUILD_PID=""

# ★★ 回收必须挂在「任何退出路径」上，不能只挂在函数尾部。
#   事实依据（R93 第四版）：调试期连续 5 次**提前退出**（量具自检不过 ⇒ exit 2；
#   双口径不一致 ⇒ exit 3）⇒ 每次都留下一整个形态库（每个 ~1 GB）
#   ⇒ 容器里攒下 5 个 itgate_r93_health_* 残留。
#   ⇒ ⇒ 纪律：**「自己建的资源」必须用 trap 兜住所有退出路径**（含信号），
#         而不是指望「正常跑完」这一条路径。
# ★★★ 第二次实测补的坑：只挂 EXIT trap 还不够。
#   实测：build 子进程**还在跑**时收到 SIGTERM ⇒ 父进程立刻死（退出码 143）
#   ⇒ 建库子进程被**孤儿化**、继续跑 ⇒ 它在父进程的 DROP **之后**才把库建出来
#   ⇒ 依然泄漏（实测残留 = 1）。
#   ⇒ ⇒ 修法：cleanup 必须**先收割仍在跑的建库子进程**，再 DROP。
#   ⇒ ⇒ ★★ R101 轮（2026-10-22）按实测重写了本块注释的**理由**（结论不变）⇒ ★ 见文件末尾「信号路径机制」一节。
cleanup() {
  # ① 先杀/收割建库子进程：否则它会在我们 DROP 之后才把库建出来
  if [ -n "$BUILD_PID" ] && kill -0 "$BUILD_PID" 2>/dev/null; then
    kill -TERM "$BUILD_PID" 2>/dev/null || true
    wait "$BUILD_PID" 2>/dev/null || true
  fi
  # ② 再回收库
  if [ "$CREATED" = "1" ] && [ "$KEEP" = "0" ] && [ -n "$DB" ]; then
    # ★ 防卡保护：SET / terminate / DROP 三条独立 -c（DROP 放最后 ⇒ 退出码仍反映它）
    docker exec -i llm-gateway-pg psql -U llm_gateway -d postgres -q \
      -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='${DB}' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
    docker exec -i llm-gateway-pg psql -U llm_gateway -d postgres -q \
      -c "SET statement_timeout = '120s'" \
      -c "DROP DATABASE IF EXISTS \"${DB}\" WITH (FORCE);" >/dev/null 2>&1
    return $?
  fi
  return 0
}

if [ -z "$DB" ]; then
  DB="itgate_r93_health_$$"
  # ★ 先置位 + 先注册：建库**过程中**失败、或量具自检不通过而提前退出 ⇒ 都要回收
  CREATED=1
  trap 'cleanup || true' EXIT
  # ★ 显式接管 INT/TERM：EXIT trap 在「父进程被信号打死」时**不保证**会跑
  trap 'cleanup || true; exit 143' INT TERM
  echo "== 建一次性形态库 ${DB} =="
  # ★ 建库子进程必须**后台**跑：这样才能拿到 PID、在 cleanup 里收割它
  DB_NAME="${DB}" KEEP_DB=1 bash scripts/audit/probe-onetime-db-shape.sh >"${OUT}.build.log" 2>&1 &
  BUILD_PID=$!
  if ! wait "$BUILD_PID"; then
    echo "✗ 建库失败："; tail -5 "${OUT}.build.log"; exit 2
  fi
  BUILD_PID=""
fi

PW="$(docker exec llm-gateway-pg printenv POSTGRES_PASSWORD 2>/dev/null | head -1)"
[ -n "$PW" ] || { echo "✗ 取不到 PG 密码（docker exec llm-gateway-pg printenv POSTGRES_PASSWORD）"; exit 2; }
DSN="postgres://llm_gateway:${PW}@127.0.0.1:5432/${DB}?sslmode=disable"

# ── ★ 量具自检（不过 ⇒ 直接退出，绝不进入读数阶段）──────────────────────
echo "== ★ 量具自检 =="
read_1() { psql "$DSN" -q -A -t -c "$1" 2>/dev/null | grep -v -E '^(WARNING|HINT|NOTICE|INFO|DETAIL)' | head -1; }

got_db="$(read_1 "SELECT current_database()")"
[ "$got_db" = "$DB" ] || { echo "  ✗ 连上的库是 '${got_db}'（目标 '${DB}'）⇒ 中止"; exit 2; }

got_addr="$(read_1 "SELECT coalesce(inet_server_addr()::text,'(unix-socket)')")"
if [ "$got_addr" = "(unix-socket)" ]; then
  echo "  ✗ 走的是 Unix socket（不是 TCP）⇒ 被测脚本会零读数 ⇒ 中止"
  exit 2
fi

ntab="$(read_1 "SELECT count(*) FROM pg_tables WHERE schemaname='public'")"
[ "${ntab:-0}" -gt 100 ] || { echo "  ✗ public 表数 = ${ntab}（形态库应有 400+）⇒ 不是形态库 ⇒ 中止"; exit 2; }

# ★ 这条自检直接对应本工具的「命门」：PGHOST 注入没生效 ⇒ 被测脚本连 socket ⇒ 零读数。
got_port="$(read_1 "SELECT coalesce(current_setting('port'),'?')")"
echo "  ✓ current_database()=${got_db}；inet_server_addr=${got_addr}；port=${got_port}；public 表数=${ntab}"

export PGHOST=127.0.0.1 PGPASSWORD="$PW" PGUSER=llm_gateway

# ── 逐单元抽取（★ 归因是「构造」出来的，不是按报错文本猜的）──────────────
rm -rf "$OUT"; mkdir -p "$OUT"
LEDGER_OUT="$OUT/ledger.tsv"
: > "$LEDGER_OUT"
: > "$OUT/units.tsv"

echo "== 逐单元体检 =="
total_unit=0; total_err=0; nfiles=0; nwrite=0
for f in .db-audit/sql/*.sql; do
  name="$(basename "$f")"
  nfiles=$((nfiles + 1))
  if grep -qiE '\b(create|insert|update|delete|truncate|drop|alter)\b' "$f"; then
    nwrite=$((nwrite + 1))
  fi

  # ★ 抽取器（python3）：
  #   ① `-c "SQL"` 形态：按**未转义引号**配对取 SQL（SQL 里可以含 `"`）；
  #      闭合引号不在本行 ⇒ 跨行继续吃（多行语句）。
  #   ② `<<'SQL'` heredoc 形态：整块收成**一个单元**（块内多语句）。
  #   ③ **不再因为「单引号奇数」就跳过** —— R93 第一版就是这么把 2 条**真错误**
  #      （脚本里 `||'` 错位）当成「不可信」跳过了 ⇒ 交叉校验当场抓出（量具漏放）。
  #      单引号奇数改为**只标记不跳过**（它本身就是「脚本有 bug」的信号）。
  mkdir -p "$OUT/units"
  python3 - "$f" "$name" "$OUT/units" > "$OUT/$name.units" 2>>"$OUT/extract.err" <<'PYEOF'
import re, sys, os
path, name, unitdir = sys.argv[1], sys.argv[2], sys.argv[3]
lines = open(path, encoding="utf-8").read().split("\n")
SPECIAL = set('$`"\\\n')      # shell 双引号里只有这几个反斜杠转义有意义
HEREDOC = re.compile(r"<<-?\s*(['\"]?)(\w+)\1")

SEP = "\x1f"   # ★ 分隔符必须是**非空白**字符（tab 会被 bash 当 IFS 空白折叠）
_n = [0]

def emit(kind, lineno, sql):
    """★ 把每个单元**原样写成文件**，清单只记路径。

    ★★ 为什么不能「压平成一行」：R93 第三版把 heredoc 块压成一行 ⇒
    psql 的 `\echo` 把**整块剩余内容**当成自己的参数吞掉 ⇒ 一条 SQL 都没执行
    ⇒ 逐单元口径报 0 错、整脚本口径报 3 错 ⇒ **差 3**（又被交叉校验当场抓住）。
    ⇒ ⇒ **单元内容必须逐字保真** ⇒ 落文件、清单记路径 ⇒ 执行时按原形态还原。
    """
    # 单引号奇数 ⇒ 脚本里的字符串不配平（真 bug 的信号）⇒ 标记但**不丢弃**
    flag = "ODDQ" if sql.count("'") % 2 != 0 else ""
    _n[0] += 1
    fn = f"{name}.{lineno}.{kind}.{_n[0]}.sql"
    with open(os.path.join(unitdir, fn), "w", encoding="utf-8") as f:
        f.write(sql)
    print(SEP.join((name, str(lineno), kind, flag, fn)))

i = 0
while i < len(lines):
    line = lines[i]
    hd = HEREDOC.search(line)
    if hd:
        term = hd.group(2)
        start = i
        i += 1
        body = []
        while i < len(lines) and lines[i].strip() != term:
            body.append(lines[i])
            i += 1
        emit("block", start + 1, "\n".join(body))
        i += 1
        continue
    m = re.search(r'-c\s+"', line)
    if not m:
        i += 1
        continue
    buf = []
    j = m.end()
    closed = False
    while j < len(line):
        c = line[j]
        if c == '\\' and j + 1 < len(line) and line[j + 1] in SPECIAL:
            buf.append(line[j + 1]); j += 2; continue
        if c == '"':
            closed = True; break
        buf.append(c); j += 1
    start_line = i
    while not closed and i + 1 < len(lines):
        i += 1
        l2 = lines[i]
        j = 0
        while j < len(l2):
            c = l2[j]
            if c == '\\' and j + 1 < len(l2) and l2[j + 1] in SPECIAL:
                buf.append(l2[j + 1]); j += 2; continue
            if c == '"':
                closed = True; break
            buf.append(c); j += 1
        if not closed:
            buf.append("\n")
    emit("stmt", start_line + 1, "".join(buf))
    i += 1
PYEOF

  nunit=0; nerr=0; noddq=0
  # ★★ 分隔符用 `\x1f`（非空白）：用 tab 会踩 bash 的「IFS 空白折叠」⇒
  #   连续 tab 被当成一个分隔符 ⇒ **空字段被吃掉** ⇒ 字段整体错位
  #   （R93 第三版：抽取器产出 181 行，但读循环读到 0 单元 ⇒ 差值又被交叉校验当场抓住）
  while IFS=$'\x1f' read -r sname sline skind sflag ssql; do
    [ -n "$ssql" ] || continue
    nunit=$((nunit + 1)); total_unit=$((total_unit + 1))
    [ "$sflag" = "ODDQ" ] && noddq=$((noddq + 1))
    if [ "$skind" = "block" ]; then
      # ★ heredoc 块在原脚本里就是 `psql ... <<'SQL'` ⇒ 用 `-f` 还原（逐字保真）
      err="$(psql "$DSN" -q -A -t -v ON_ERROR_STOP=0 -f "$OUT/units/$ssql" 2>&1 >/dev/null | grep -E '(^|: )(ERROR|FATAL):' | head -1)"
    else
      # ★ `-c` 单条语句：命令替换保真（含换行）；多语句时 PG 视作一个隐式事务
      err="$(psql "$DSN" -q -A -t -v ON_ERROR_STOP=0 -c "$(cat "$OUT/units/$ssql")" 2>&1 >/dev/null | grep -E '(^|: )(ERROR|FATAL):' | head -1)"
    fi
    if [ -n "$err" ]; then
      nerr=$((nerr + 1)); total_err=$((total_err + 1))
      cls="$(printf '%s' "$err" | sed -E 's/^ERROR:  //; s/^FATAL:  //; s/"[^"]*"/"X"/g; s/[0-9]+/N/g')"
      printf '%s\t%s\t%s\t%s\t%s\n' "$sname" "$sline" "$skind" "$cls" "$(head -c 140 "$OUT/units/$ssql" | tr '\t\n' '  ')" >> "$LEDGER_OUT"
    fi
  done < "$OUT/$name.units"
  printf '  %-32s 单元=%-4s 错误=%-3s 引号奇数=%s\n' "$name" "$nunit" "$nerr" "$noddq"
done

echo
echo "== 汇总 =="
echo "  脚本数 = ${nfiles}（其中含写语句的 ${nwrite} 个 ⇒ 只许跑在一次性库）"
echo "  抽取单元数 = ${total_unit}"
echo "  错误行数 = ${total_err}"
echo "  明细 = ${LEDGER_OUT}"
echo
echo "== 错误聚类 =="
cut -f4 "$LEDGER_OUT" | sort | uniq -c | sort -rn | sed 's/^/  /'

# ── 双口径交叉校验：整脚本跑 vs 逐单元跑，ERROR 总数必须一致 ────────────
if [ "$CROSSCHECK" = "1" ]; then
  echo
  echo "== ★ 双口径交叉校验（整脚本跑 vs 逐单元跑）=="
  whole_err=0; ndiff=0
  for f in .db-audit/sql/*.sql; do
    name="$(basename "$f")"
    sed "s/-d llm_gateway/-d ${DB}/g" "$f" > "$OUT/whole_$name"
    ( cd "$OUT" && timeout 300 bash "./whole_$name" ) >/dev/null 2>"$OUT/whole_$name.err"
    w="$(grep -c "^ERROR:" "$OUT/whole_$name.err" 2>/dev/null | head -1)"
    w="${w:-0}"
    p="$(awk -F'\t' -v n="$name" '$1==n {c++} END{print c+0}' "$LEDGER_OUT")"
    whole_err=$((whole_err + w))
    if [ "$w" != "$p" ]; then
      ndiff=$((ndiff + 1))
      printf '  ✗ %-30s 整脚本=%s 逐单元=%s\n' "$name" "$w" "$p"
    fi
  done
  echo "  整脚本口径 ERROR 总数 = ${whole_err}；逐单元口径 ERROR 总数 = ${total_err}"
  if [ "$whole_err" != "$total_err" ]; then
    echo "  ✗ 两个口径不一致（差 $((whole_err - total_err))）⇒ **本工具自己有盲区** ⇒ 不可采信"
    exit 3
  fi
  echo "  ✓ 两个口径一致（${total_err} 条）⇒ 抽取器无漏放"
fi

# ── 回收 ───────────────────────────────────────────────────────────────
if [ "$CREATED" = "1" ] && [ "$KEEP" = "0" ]; then
  echo
  echo "== 回收 ${DB} =="
  if cleanup; then
    echo "  ✓ ${DB} 已 DROP"
    CREATED=0   # ★ 交回 trap，避免重复 DROP
  else
    echo "  ✗ 回收失败（请手工 DROP ${DB}）"
  fi
fi
exit 0

# ── ★★★ 信号路径机制（R101 轮实测 · 2026-10-22）────────────────────────────
#
#   ⇒ 本节**不改**上面 trap 行的任何行为，只把「为什么这样写」按实测重写一遍。
#   ⇒ ★ 事实源（可复现门）：scripts/ursmcheck/signal_path_mechanism_test.go
#     ⇒ 它守四件事：形态对比（wait vs 前台）/ 终止性 ≠ 清理性 / 反模式 / 静态守卫（trap 必须带 exit）
#   ⇒ ★ 本机读数：macOS 26.6.2 arm64 · bash 3.2.57（/bin/bash）与 5.3.9（homebrew）
#     · 形态 W ＝ 后台子进程 + `wait "$PID"`（★ 本脚本的形态）
#     · 形态 F ＝ 前台外部命令（如 `sleep 1.5`）
#     · 每格复跑 3 次取中位数
#
#   ★★★ 三条互相独立的机制事实（每条都有上面的读数支撑）：
#
#   ① 【清理性】EXIT trap 在信号终止下**几乎总是**执行，但**不保证**。
#      读数：形态 W/F × SIGTERM/SIGINT × 两个 bash 版本，第一轮 24 格里
#      **23 格执行**；唯一没执行的 3 格**全部落在 bash 5.3.9 + 形态 W**
#      （进程被信号直接杀死、两个标记都没出现 ⇒ trap 一次都没跑）；
#      随后每格复跑 3 次 ⇒ **36/36 全部执行** ⇒ ⇒ ⇒ 该格未复现 ⇒ 判为**竞态**。
#      ⇒ ⇒ 「EXIT trap 会执行」是**一次观测**；「EXIT trap 一定会执行」是**过强的普适断言**；
#      ⇒ ⇒ ⇒ 所以上面那句「**不保证**会跑」是**准确的严谨表述**⇒ ⇒ ★ 保留原句，不订正。
#      ⇒ ⇒ ⇒ ★★ 而且「不保证」比「会跑」**更有用**：竞态一旦复现，
#      ⇒ ⇒⇒   「信号路径有竞态 ⇒ 必须显式接管 + 必须带 exit」这条纪律才立得住。
#
#   ② 【终止性 ≠ 清理性】★ 形态 F 下**未挂**信号 trap 时，**SIGINT 被忽略**：
#      脚本体**继续跑完**、退出码 0、EXIT trap 在末尾才执行。
#      ⇒ ⇒⇒ 「会清理」（EXIT trap 跑了）**推不出**「会停」（脚本停下来了）—— 这是**两个轴**。
#      ⇒ ⇒⇒ ⇒★ 没有 INT/TERM 接管 ⇒ SIGINT 就**停不住**这个脚本
#      ⇒ ⇒⇒ ⇒⇒ ⇒ 脚本可能在操作者要求终止后**继续建库** ⇒ ⇒⇒ ⇒ 泄漏。
#      ⇒ ⇒⇒ ⇒★ 这就是上面那行 trap 的**真正价值**：它不是「多一道保险」，
#      ⇒ ⇒⇒   它是**唯一能让终止信号停住脚本**的手段。
#
#   ③ 【形态依赖】★ INT/TERM trap 的触发时刻**差两个数量级**，取决于被打断时 shell 在等什么：
#      · 形态 W（★ 本脚本）：信号**立即**打断 `wait` ⇒ trap **~13ms** 触发 ⇒ **收割及时**
#      · 形态 F：trap 被推迟到**前台命令结束** ⇒ 实测 **~1400ms** ⇒ 「收割被推迟」
#      ⇒ ⇒⇒ 「挂信号 trap ⇒ 收割更晚」**只在形态 F 成立**，本脚本不在那个形态里。
#      ⇒ ⇒⇒ ⇒★ §19.20.59 与并行会话 R95 段 E 的读数**自洽**（它们用的前台命令长度不同：
#      ⇒ ⇒⇒   5s 前台 ⇒ trap 在 t0+5.0s；1s 前台 ⇒ 进程在 t0+1.01s 退出），
#      ⇒ ⇒⇒   ⇒ 但**推论不可外推**到形态 W ⇒ ⇒⇒ ⇒★ 并行会话台账里那句
#      ⇒ ⇒⇒   「bash 会把 trap 推迟到当前前台命令（`wait "$BUILD_PID"`）结束 ⇒ 收割被推迟」
#      ⇒ ⇒⇒   **与实测相反** ⇒ ⇒⇒ ⇒ 需订正（该文件是并行会话的 ⇒ 只通报、不擅自动）。
#
#   ★★ 反模式（实测）：INT/TERM trap **不 exit** ⇒ 脚本体在终止信号后**继续执行**
#      ⇒ ⇒ 一次性库脚本可能在 cleanup 之后继续建库 ⇒ ⇒⇒ 上面那行**带 exit 143** ⇒ 形态正确。
#
#   ⚠ 诚实边界：绝对时刻随 bash 版本与机器负载变化 ⇒ ⇒⇒ 门只断言【关系】与【形态】，
#      不写死任何绝对毫秒数；读数全部由门打印 ⇒ ⇒ 「版本差异」这类事实不会只活在某个人脑子里。

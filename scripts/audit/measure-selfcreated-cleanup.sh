#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════
# measure-selfcreated-cleanup.sh —— 量「自建一次性库的脚本，回收路径覆盖了几条退出路径」
#
# 起因（2026-10-21 R93 轮收尾发现）：
#   体检工具自建一次性形态库（~1 GB）跑完 DROP，但**回收块挂在函数尾部**
#   ⇒ 任何**提前退出**（exit 2 / exit 3）都跳过回收 ⇒ 调试期攒下 7 个残留库。
#   ⇒ R94 轮把这条纪律**推广到全仓**（本脚本 = 量具）。
#
# ★★ 判据的四类假阳性（R94 轮逐条读源码后归纳，★ 同 R63 轮那条纪律）：
#   ① 匹配只在**注释**里        ⇒ 本脚本按扩展名分派剥注释后消除
#   ② 匹配在**正则字面量**里    ⇒ 那一行同时含 regexp/MustCompile/re.compile ⇒ 排除
#   ③ **本量具脚本自匹配**      ⇒ 排除本文件自身
#   ④ 建的是**长驻库**（部署类）⇒ 没有回收义务 ⇒ C5 误判
#   ⇒ ⇒ ★★ **静态判据只能给「候选集」，不能给「泄漏清单」** ⇒ 结论必须逐条人工裁决
#      （台账：scripts/audit/selfcreated-cleanup-ledger.tsv）
#
# ★★★ 候选集策略：**宽进严判**。
#   ⇒ 只剔「便宜的」四类（注释 / 正则字面量 / 量具自匹配 / 纯日志字符串）；
#     「有没有执行上下文」**降级为显示列**，不做过滤 ——
#     因为按执行上下文过滤会造成【漏放】：
#     ★ 实测：Python 的多行 SQL 字符串里 psql 调用可能在 30 行外，
#       且**字符串里含空行** ⇒ 连「块级窗口」也切不对 ⇒ 真泄漏被漏掉。
#     ⇒ ★ **漏放比误报危险** ⇒ 候选集宁可宽，严格性交给台账里的人工裁决。
#
# 判据（逐条读源码，不猜）：
#   C1 真的建库   —— 剥注释、排除正则字面量后仍能匹配到 CREATE DATABASE / createdb
#   C2 有兜底     —— 有「覆盖所有退出路径」的机制（★ 语言感知：
#                    shell ⇒ `trap ... EXIT`；python ⇒ atexit/try-finally；go ⇒ defer）
#   C3 有 DROP     —— 有 DROP DATABASE（★ **建库前**的 DROP 是自愈，不是回收）
#   C4 有提前退出  —— 建库点**之后**、首次 DROP **之前**存在 exit / die / abort /
#                     sys.exit / return ⇒ 尾部回收会被跳过
#   C5 一次性      —— 建的是**一次性库**（★ 只有一次性库才有回收义务；长驻库不适用）
#   C6 自愈        —— **建库之前**就有 DROP DATABASE ⇒ 上次泄漏会被下次运行自动修复
#                     ⇒ ★ 有自愈 ≠ 无害（库名每会话独立时自愈失效）⇒ 只降一级
#   ★ 危险 = C1 ∧ C4 ∧ C5 ∧ ¬C2
#
# 读数口径（自检跑在读数之前）：
#   ★ 候选集非空（否则「0 个危险」是空集的恒等式，不是发现）
#   ★ ★ 剥注释前后候选数必须不同（不同 ⇒ 注释里确有匹配 ⇒ 不剥注释的读数会虚高）
#
# 用法：
#   scripts/audit/measure-selfcreated-cleanup.sh            # 读数（★ 是候选集，不是结论）
#   scripts/audit/measure-selfcreated-cleanup.sh --verify   # 只做口径自检
# ═══════════════════════════════════════════════════════════════════════
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT" || exit 2

VERIFY=0
DEBUG=0
DIGEST=0
LEDGER_ONLY=0
case "${1:-}" in
  --verify) VERIFY=1 ;;
  --debug)  DEBUG=1 ;;
  --digest) DIGEST=1 ;;   # ★ 内部模式：只输出读数指纹（给「确定性自检」用）
  --ledger-only) LEDGER_ONLY=1 ;;  # ★ R86 轮新增：★ 只做台账自检（★ 不扫源码 ⇒ ~0.05 s）
esac

SELF="scripts/audit/measure-selfcreated-cleanup.sh"

# ---------------------------------------------------------------- 台账自检（R86 轮抽出）
# ★ 台账自检：结论必须逐条落地（★ 「量具给了候选集」≠「有人裁决过」）
# ★★ R86 轮把这段抽成函数 ⇒ ★ `--verify` 与 `--ledger-only` **共用同一份判据**
# ★★ ⇒ ⇒ 两个模式的语义**逐字一致**（★ 不是两份拷贝 ⇒ 不会漂）。
SELF_CREATED_LEDGER="scripts/audit/selfcreated-cleanup-ledger.tsv"
ledger_selfcheck() {
  local ledger="$1" ln lbad
  if [ ! -f "$ledger" ]; then
    echo "  ⚠ 台账不存在：${ledger}（结论必须逐条落地）"
    return 0
  fi
  ln="$(grep -c -vE '^[[:space:]]*(#|$)' "$ledger" || true)"
  lbad="$(awk -F'\t' '!/^[[:space:]]*(#|$)/ && (NF<7 || $6=="") {n++} END{print n+0}' "$ledger")"
  echo "  台账数据行 = ${ln}"
  if [ "$ln" -lt 20 ]; then
    echo "  ✗ 台账数据行过少 ⇒ 候选集没被逐条裁决（★ 量具给了候选集 ≠ 有人裁决过）"
    return 2
  fi
  if [ "$lbad" != "0" ]; then
    echo "  ✗ 台账有 ${lbad} 行缺裁决列 ⇒ 台账不可信"
    return 2
  fi
  echo "  ✓ 台账结构自检通过（${ln} 行，裁决列齐全）"
  return 0
}


# ---------------------------------------------------------------- --ledger-only（R86 轮新增）
# ★ 为什么有这个模式：
#   ★ 「掏空法门」（scripts/ursmcheck/tsv_hollowout_gate_test.go）把本台账掏空后
#     要跑一遍读方 ⇒ ⇒ 而 `--verify` 模式会**全仓扫一遍源码**（★ 实测 ~42 s）
#     ⇒ ⇒ 门每轮要跑两次 ⇒ ⇒ 单这一条就吃掉 ~84 s ⇒ ⇒ 而 `verify.sh` 的
#     `go test ./... -timeout=900s` 是**每包**预算（★ 本门所在的包已经很重）。
#   ★ 本模式把「台账自检」这一层**单独抽出来**（★ 与 `--verify` **共用同一个函数**
#     ⇒ ⇒ 语义逐字一致）⇒ ⇒ 门拿到的是**同一个判据**，只是不必重扫全仓。
#
# ★ 诚实边界：★ 本模式**不产出候选集** ⇒ ⇒ 它证明的是「台账被掏空 ⇒ 台账自检红」
# ⇒ ⇒ 而「候选集非空」那条判据（★ 与台账无关）由 `--verify` 负责。
if [ "$LEDGER_ONLY" = "1" ]; then
  echo "== --ledger-only：只做台账自检（★ 不扫源码 ⇒ ~0.05 s）=="
  ledger_selfcheck "$SELF_CREATED_LEDGER" || exit $?
  exit 0
fi

# ── 剥注释（按扩展名分派；★ 用 `//` 当通用标记会截断 SQL 行 ⇒ 漏放比误报危险）──
strip_comments() {
  python3 - "$1" <<'PYEOF'
import re, sys

path = sys.argv[1]
text = open(path, encoding="utf-8", errors="replace").read()
ext = path.rsplit(".", 1)[-1].lower()

if ext in ("sh", "bash", "py"):
    text = re.sub(r"(?m)#.*$", "", text)
elif ext == "go":
    text = re.sub(r"(?m)//.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
elif ext == "sql":
    text = re.sub(r"(?m)--.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
sys.stdout.write(text)
PYEOF
}

# ── 「真的建库」的行 ─────────────────────────────────────────────────────
#   ★ 排除四类非创建：
#     ① 正则字面量（regexp / MustCompile / re.compile / MatchString）
#     ② 测试夹具里的字面量（那行附近没有 psql/createdb 等执行上下文）
#     ③ 日志/提示字符串（同样是「没有执行上下文」）
#     ④ 只是**拼一条 query 字符串**给调用者（同样没有执行上下文）
#   ⇒ 判据：匹配行**或它 ±3 行窗口内**含执行上下文（psql/createdb/exec/Exec）
#   ★★ 窗口而不是同行：多行 psql 调用（`-c "CREATE DATABASE ..."` 的 psql 在上一行）
#     用同行判据会**漏放 5 个真创建者** ⇒ ★ **漏放比误报危险**。
real_create_lines() {
  python3 - "$1" <<'PYEOF'
import re, sys

path = sys.argv[1]
text = open(path, encoding="utf-8", errors="replace").read()
ext = path.rsplit(".", 1)[-1].lower()
if ext in ("sh", "bash", "py"):
    text = re.sub(r"(?m)#.*$", "", text)
elif ext == "go":
    text = re.sub(r"(?m)//.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
elif ext == "sql":
    text = re.sub(r"(?m)--.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)

lines = text.splitlines()
create_re = re.compile(r'CREATE\s+DATABASE|createdb', re.I)
ctx_re = re.compile(r'psql|createdb|\bexec\b|\bExec\b|run_psql|pg_exec', re.I)
re_re = re.compile(r'regexp|MustCompile|re\.compile|MatchString', re.I)

def block_of(i):
    """返回第 i 行所属的「空行分隔的连续块」范围（lo, hi，两端不含）。"""
    lo, hi = i, i
    while lo - 1 >= 0 and lines[lo - 1].strip():
        lo -= 1
    while hi + 1 < len(lines) and lines[hi + 1].strip():
        hi += 1
    return lo, hi

out = []
for i, line in enumerate(lines):
    if not create_re.search(line):
        continue
    if re_re.search(line):
        continue                      # ① 正则字面量
    out.append(f"{i + 1}:{line.strip()[:160]}")
sys.stdout.write("\n".join(out))
PYEOF
}

# ── C5：建的是不是「一次性库」？★ 解析不出时必须答「未知」，不能答「否」──
#   三态：1 = 是（有回收义务）/ 0 = 否（长驻库，无回收义务）/ ? = 未知（⇒ 进人工裁决）
#   ★★ 不能只看建库行 ±3 行 ⇒ **漏放了「库名是变量、在别处赋值」的形态**
#      （例：`DB="${DB_NAME:-itgate_r59_shape}"` 在第 49 行，建库在第 59 行）
#      ⇒ 修法：解析建库语句里的库名变量 ⇒ 回溯它的赋值 ⇒ 看赋值里有没有一次性标记
throwaway_verdict() {
  python3 - "$1" <<'PYEOF_V'
import re, sys

path = sys.argv[1]
text = open(path, encoding="utf-8", errors="replace").read()
ext = path.rsplit(".", 1)[-1].lower()
if ext in ("sh", "bash", "py"):
    text = re.sub(r"(?m)#.*$", "", text)
elif ext == "go":
    text = re.sub(r"(?m)//.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
elif ext == "sql":
    text = re.sub(r"(?m)--.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)

lines = text.splitlines()
create_re = re.compile(r'CREATE\s+DATABASE|createdb', re.I)
re_re = re.compile(r'regexp|MustCompile|re\.compile|MatchString', re.I)
marker_re = re.compile(r'itgate|scratch|_tmp|tmp_|temp_|probe|verify|_test|throwaway|oneshot|one-time', re.I)

idxs = [i for i, l in enumerate(lines) if create_re.search(l) and not re_re.search(l)]
if not idxs:
    print("0"); sys.exit(0)

i = idxs[0]
window = "\n".join(lines[max(0, i - 3): i + 4])
if marker_re.search(window):
    print("1"); sys.exit(0)

stmt = lines[i]
var_re = re.compile(r'\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?|\{([A-Za-z_][A-Za-z0-9_]*)\}|%\(([A-Za-z_][A-Za-z0-9_]*)\)s?')
vars_ = {g for m in var_re.finditer(stmt) for g in m.groups() if g}
for v in sorted(vars_):
    assign_re = re.compile(r'^\s*(?:export\s+|local\s+|declare\s+-A\s+)?' + re.escape(v) + r'\s*=\s*(.*)$', re.M)
    for m in assign_re.finditer(text):
        if marker_re.search(m.group(1)):
            print("1"); sys.exit(0)

print("?")   # ★ 解析不出 ⇒ 「未知」⇒ 进人工裁决（不能当成「否」）
PYEOF_V
}

# ── 「附近有没有执行上下文」（软信号列，★ 不做过滤 —— 过滤会造成漏放）─────
has_exec_ctx() {
  python3 - "$1" <<'PYEOF_C'
import re, sys
path = sys.argv[1]
text = open(path, encoding="utf-8", errors="replace").read()
ext = path.rsplit(".", 1)[-1].lower()
if ext in ("sh", "bash", "py"):
    text = re.sub(r"(?m)#.*$", "", text)
elif ext == "go":
    text = re.sub(r"(?m)//.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
elif ext == "sql":
    text = re.sub(r"(?m)--.*$", "", text)
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
ctx = re.compile(r'psql|createdb|\bexec\b|\bExec\b|run_psql|pg_exec', re.I)
sys.stdout.write("1" if ctx.search(text) else "0")
PYEOF_C
}

# ── 主建库行：★ 优先取「所属块内有执行上下文」的那一条 ──────────────────
#   （否则会取到首条**日志字符串**里的 "CREATE DATABASE" ⇒ C4 的窗口失真）
primary_create_line() {
  python3 - "$1" <<'PYEOF_P'
import re, sys
path = sys.argv[1]
text = open(path, encoding="utf-8", errors="replace").read()
ext = path.rsplit(".", 1)[-1].lower()
if ext in ("sh", "bash", "py"):
    text = re.sub(r"(?m)#.*$", "", text)
elif ext == "go":
    text = re.sub(r"(?m)//.*$", "", text); text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
elif ext == "sql":
    text = re.sub(r"(?m)--.*$", "", text); text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
lines = text.splitlines()
create_re = re.compile(r'CREATE\s+DATABASE|createdb', re.I)
ctx_re = re.compile(r'psql|createdb|\bexec\b|\bExec\b|run_psql|pg_exec', re.I)
re_re = re.compile(r'regexp|MustCompile|re\.compile|MatchString', re.I)

def block_of(i):
    lo, hi = i, i
    while lo - 1 >= 0 and lines[lo - 1].strip():
        lo -= 1
    while hi + 1 < len(lines) and lines[hi + 1].strip():
        hi += 1
    return lo, hi

cands = [i for i, l in enumerate(lines) if create_re.search(l) and not re_re.search(l)]
if not cands:
    print(""); sys.exit(0)
for i in cands:
    lo, hi = block_of(i)
    if any(ctx_re.search(lines[j]) for j in range(lo, hi + 1)):
        print(i + 1); sys.exit(0)
print(cands[0] + 1)
PYEOF_P
}

# ── 窗口内是否有「真正的提前退出」────────────────────────────────────────
#   ★ shell 的 `return` 只在**与建库同函数**内才算（别的函数的 return 不是脚本退出）
early_exit_between() {
  python3 - "$1" "$2" "$3" <<'PYEOF3'
import re, sys

path, a, b = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
text = open(path, encoding="utf-8", errors="replace").read()
ext = path.rsplit(".", 1)[-1].lower()
if ext in ("sh", "bash", "py"):
    text = re.sub(r"(?m)#.*$", "", text)
elif ext == "go":
    text = re.sub(r"(?m)//.*$", "", text); text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
elif ext == "sql":
    text = re.sub(r"(?m)--.*$", "", text); text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
lines = text.splitlines()

def func_start(idx):
    """向上找包含 idx 的 shell 函数起始行（`name() {`）；找不到返回 None。"""
    for k in range(idx, -1, -1):
        if re.match(r'^\s*[A-Za-z_][A-Za-z0-9_:-]*\s*\(\)\s*\{', lines[k]):
            # 该函数的结束行 = 之后第一个顶格 '}'
            for m in range(k + 1, len(lines)):
                if re.match(r'^\}', lines[m]):
                    return k, m
            return k, len(lines) - 1
    return None

scope = func_start(a - 1) if ext in ("sh", "bash") else None

for i in range(a, min(b, len(lines))):     # 0-based：a 是 1-based 建库行的下一行
    line = lines[i]
    if ext == "py":
        if re.search(r'\bsys\.exit\b|raise\s+SystemExit', line):
            print("1"); sys.exit(0)
        if re.match(r'^return\b', line):
            print("1"); sys.exit(0)
    else:
        if re.search(r'(^|[^A-Za-z0-9_])(exit|die|abort|quit)([^A-Za-z0-9_]|$)', line):
            print("1"); sys.exit(0)
        if re.match(r'^\s*return\b', line):
            if scope and scope[0] <= i <= scope[1]:
                print("1"); sys.exit(0)   # ★ 与建库同函数 ⇒ 算
print("0")
PYEOF3
}

# ── 候选集 ──────────────────────────────────────────────────────────────
raw=(); effective=()
while IFS= read -r f; do
  [ "$f" = "$SELF" ] && continue          # ★ 排除本量具脚本自匹配
  raw+=("$f")
  if [ -n "$(real_create_lines "$f")" ]; then
    effective+=("$f")
  fi
done < <(
  grep -rl --include='*.sh' --include='*.bash' --include='*.py' --include='*.go' \
    -E 'CREATE[[:space:]]+DATABASE|createdb' \
    scripts internal cmd 2>/dev/null | sort
)

n_raw=${#raw[@]}
n_eff=${#effective[@]}

echo "== 候选集 =="
echo "  原始命中（含注释/正则字面量/自匹配）= ${n_raw}"
echo "  剥注释 + 排除正则字面量 + 排除自匹配后 = ${n_eff}"

if [ "$n_eff" -eq 0 ]; then
  echo "  ✗ 候选集为空 ⇒ 本读数无效（空集上的「0 个危险」是恒等式，不是发现）"
  exit 2
fi

# ── 逐个判据 ────────────────────────────────────────────────────────────
rows=()
n_c2=0; n_c3=0; n_c4=0; n_c5=0; n_c5u=0; n_c6=0; n_danger=0
for f in "${effective[@]}"; do
  body="$(strip_comments "$f")"

  # C2：有「覆盖所有退出路径」的机制（★ 语言感知：同一个机制的三种实现）
  #    shell ⇒ trap ... EXIT；python ⇒ atexit.register / try+finally；go ⇒ defer
  #
  # ★★★ R96 轮修掉的**真缺陷（随机翻转读数）**：
  #   原写法是 `printf '%s' "$body" | grep -qE '…'`。
  #   ★ `grep -q` 命中即退 ⇒ 写端 `printf` 会被 **SIGPIPE** 打死
  #   ★ 而本脚本 `set -o pipefail` ⇒ 整条管道的退出码变成 141（SIGPIPE）
  #   ★ ⇒ `if` 判据**随机地**走错分支 ⇒ 同一个文件在两次读数里 C2 一会儿 1 一会儿 0
  #   ★ 实测（2026-10-21 R96 轮）：`scripts/deploy-seamless-docker.sh` 剥注释后
  #     **65,937 字节 > 64 KB 管道缓冲** ⇒ 只有在写端先阻塞时才触发
  #     ⇒ 40 次读数里出现 3 种输出（28×A / 2×B / 1×C）⇒ 危险候选在 7 与 8 之间漂。
  #   ⇒ 修法：**去掉管道**（herestring 不产生 SIGPIPE 写端）⇒ 判据变成确定性的。
  case "$f" in
    *.py)
      if grep -qE 'atexit\.register|finally[[:space:]]*:' <<< "$body"; then c2=1; else c2=0; fi ;;
    *.go)
      if grep -qE '(^|[^A-Za-z0-9_])defer[[:space:]]' <<< "$body"; then c2=1; else c2=0; fi ;;
    *)
      if grep -qE '^[[:space:]]*trap[[:space:]]' <<< "$body"; then c2=1; else c2=0; fi ;;
  esac

  # C3：有 DROP DATABASE
  if grep -qiE 'DROP[[:space:]]+DATABASE' <<< "$body"; then c3=1; else c3=0; fi

  # 建库行号（用「真的建库」的行，排除正则字面量 / 夹具 / 日志字符串）
  c_line="$(primary_create_line "$f")"

  # 首次 DROP 行号
  #   ★★ 必须取【建库之后】的那个：建库**之前**的 DROP 是自愈（判 C6），
  #      若拿它当「首次 DROP」⇒ 窗口 [建库+1, 首次DROP] 为空 ⇒ C4 恒 0 ⇒ 漏判。
  d_line=""
  if [ -n "$c_line" ]; then
    d_line="$(printf '%s' "$body" | grep -niE 'DROP[[:space:]]+DATABASE' \
      | awk -F: -v c="$c_line" '$1 > c {print $1; exit}')"
  fi

  # 首次 DROP **之前**的 DROP ⇒ 自愈形态
  pre_drop=""
  if [ -n "$c_line" ]; then
    pre_drop="$(printf '%s' "$body" | grep -niE 'DROP[[:space:]]+DATABASE' \
      | awk -F: -v c="$c_line" '$1 < c {print $1; exit}')"
  fi
  c6=0
  [ -n "$pre_drop" ] && c6=1

  # C4：建库点之后、首次回收 DROP 之前有提前终止
  c4=0
  if [ -n "$c_line" ]; then
    end_line="${d_line:-$(( $(wc -l < "$f") + 1 ))}"
    if [ "$end_line" -gt "$c_line" ]; then
      # ★ 必须吞掉 stdout：该 helper 打印 "1"/"0" ⇒ 不吞会污染工具自身的输出
      if early_exit_between "$f" "$c_line" "$end_line" >/dev/null; then c4=1; fi
    fi
  fi

  # C5：一次性库？（★ 三态：1 是 / 0 否 / ? 未知 ⇒ 「未知」也进候选集）
  c5="$(throwaway_verdict "$f")"

  [ "$c2" = "1" ] && n_c2=$((n_c2 + 1))
  [ "$c3" = "1" ] && n_c3=$((n_c3 + 1))
  [ "$c4" = "1" ] && n_c4=$((n_c4 + 1))
  if [ "$c5" = "1" ]; then n_c5=$((n_c5 + 1)); elif [ "$c5" = "?" ]; then n_c5u=$((n_c5u + 1)); fi
  [ "$c6" = "1" ] && n_c6=$((n_c6 + 1))

  danger=0
  # ★ 「未知」(C5=?) 也进候选集 ⇒ 判据给不出结论时不许当成「否」
  if [ "$c4" = "1" ] && [ "$c2" = "0" ] && [ "$c5" != "0" ]; then
    danger=1; n_danger=$((n_danger + 1))
  fi
  ctx_sig="$(has_exec_ctx "$f")"
  if [ "$DEBUG" = "1" ]; then
    printf '── %s\n' "$f" >&2
    printf '   建库行=%s  回收DROP行=%s  建库前DROP行=%s  trap/atexit/defer=%s  提前退出=%s  一次性=%s  自愈=%s\n' \
      "${c_line:-无}" "${d_line:-无}" "${pre_drop:-无}" "$c2" "$c4" "$c5" "$c6" >&2
    early_exit_between "$f" "${c_line:-1}" "${d_line:-99999}" >/dev/null 2>&1 || true
    python3 - "$f" "${c_line:-1}" "${d_line:-99999}" <<'DBGEOF' >&2
import re, sys
path, a, b = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
text = open(path, encoding="utf-8", errors="replace").read()
ext = path.rsplit(".", 1)[-1].lower()
if ext in ("sh", "bash", "py"): text = re.sub(r"(?m)#.*$", "", text)
lines = text.splitlines()
for i in range(a, min(b, len(lines))):
    l = lines[i]
    if ext == "py":
        hit = re.search(r"\bsys\.exit\b|raise\s+SystemExit", l) or re.match(r"^return\b", l)
    else:
        hit = re.search(r"(^|[^A-Za-z0-9_])(exit|die|abort|quit)([^A-Za-z0-9_]|$)", l)
    if hit:
        print(f"      ↳ 提前退出 {i+1}: {l.strip()[:100]}")
DBGEOF
  fi
  rows+=("${c2}${c3}${c4}${c5}${c6}${ctx_sig}${danger}$(printf '\t')${f}")
done

if [ "$DIGEST" = "1" ]; then
  # ★ 内部模式：只输出读数指纹（给下面的「确定性自检」用）
  printf '%s\n' "${rows[@]}"
  printf 'COUNTS n_eff=%s n_c2=%s n_c3=%s n_c4=%s n_c5=%s n_c5u=%s n_c6=%s n_danger=%s\n' \
    "$n_eff" "$n_c2" "$n_c3" "$n_c4" "$n_c5" "$n_c5u" "$n_c6" "$n_danger"
  exit 0
fi

# ★★★ 读数【确定性】自检（R96 轮新增的一道量具自检）
#   判据：同一个量具在**同一棵树**上必须给出**逐字相同**的读数。
#   ⇒ 跑两遍比指纹；**不等 ⇒ 拒绝出结论（exit 3）**。
#   ★ 这道门是给「判据里藏着竞态」兜底的 ——
#     本轮就抓到过一个真例：`printf … | grep -q` + `set -o pipefail`
#     ⇒ 写端被 SIGPIPE 打死 ⇒ 判据随机翻转 ⇒ 40 次读数出现 3 种输出。
#   ⇒ ★ 通用式：**量具的输出必须【可复现】才算读数**；不可复现的量具不是「偶尔不准」，
#     而是**根本不能用来发布结论**。
if [ "${SKIP_DETERMINISM_CHECK:-0}" != "1" ]; then
  d1="$(bash "$0" --digest 2>/dev/null)"
  d2="$(bash "$0" --digest 2>/dev/null)"
  if [ -z "$d1" ] || [ -z "$d2" ]; then
    echo "!! 确定性自检拿不到读数指纹 ⇒ 拒绝出结论（exit 3）"
    exit 3
  fi
  if [ "$d1" != "$d2" ]; then
    echo "!! 读数确定性自检失败：两次读数不一致 ⇒ 读数不稳定 ⇒ 拒绝出结论（exit 3）"
    echo "   ⇒ ★ 典型成因：判据里有【竞态】（本轮抓到过「printf … | grep -q」+ pipefail ⇒ SIGPIPE）"
    exit 3
  fi
  echo "  ✓ 读数确定性自检：两次读数逐字一致"
fi

if [ "$VERIFY" = "1" ]; then
  echo "== 口径自检 =="
  [ "$n_eff" -gt 0 ] || { echo "  ✗ 候选集为空 ⇒ 读数无意义"; exit 2; }
  echo "  ✓ 候选集非空（${n_eff}）⇒ 读数有效"
  ledger_selfcheck "$SELF_CREATED_LEDGER" || exit $?
  if [ "$n_raw" != "$n_eff" ]; then
    echo "  ⚠ 剥注释/去正则/去自匹配前后候选数不同（${n_raw} → ${n_eff}）"
    echo "    ⇒ **不剥注释的读数会虚高**（注释里确有匹配）"
  fi
  exit 0
fi

printf '\n%-4s %-4s %-4s %-4s %-4s %-4s %-6s %s\n' trap DROP 退出 一次性 自愈 ctx 危险 文件
printf '%s\n' "${rows[@]}" | while IFS=$'\t' read -r flags f; do
  printf '%-4s %-4s %-4s %-4s %-4s %-4s %-6s %s\n' \
    "${flags:0:1}" "${flags:1:1}" "${flags:2:1}" "${flags:3:1}" "${flags:4:1}" "${flags:5:1}" "${flags:6:1}" "$f"
done

echo
echo "== 汇总（★ 这是【候选集】，不是「泄漏清单」⇒ 结论看台账）=="
echo "  候选（剥注释 + 去正则字面量 + 去自匹配）= ${n_eff}"
echo "  C2 有 trap                   = ${n_c2}"
echo "  C3 有 DROP DATABASE          = ${n_c3}"
echo "  C4 建库后→首次DROP前有提前退出 = ${n_c4}"
echo "  C5 一次性库（有回收义务）    = ${n_c5}（另有 ${n_c5u} 条「未知」⇒ 也进候选集）"
echo "  C6 建库前有 DROP（自愈形态） = ${n_c6}"
echo "  ★ 危险候选（C1∧C4∧C5∧¬C2） = ${n_danger}"
echo
echo "  ★ 台账（逐条人工裁决）：scripts/audit/selfcreated-cleanup-ledger.tsv"
exit 0

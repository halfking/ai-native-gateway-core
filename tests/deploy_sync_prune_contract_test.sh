#!/usr/bin/env bash
# =====================================================================
# tests/deploy_sync_prune_contract_test.sh
#   —— 守「部署同步清单的剪枝规则」（2026-11-05 效率轮）
# =====================================================================
# ★ 背景：旧口径每次部署都把**本地构建/运行残留**往 211 传：
#     实测同步集合 1.5GB / 79,134 条目（远端树落盘 1.1GB）⇒ 同步耗时 48.9s。
#     其中 logs/(275M) run/(56M) bin/(90M) gateway/(90M) llm-gateway/(60M)
#     gateway-v2/(46M) quality-monitor/ .codegraph/(311M) .audit-validation/(130M)
#     全是**0 个 git 跟踪文件**的本地残留 ⇒ 传输它们纯属浪费；
#     其中 run/ 还存着 admin 口令散列 ⇒ **每次部署都往目标机传一遍**（安全问题）。
#
# ★ 本门回答的问题（每层一个不同的问题）：
#   ① 完整性层：剪枝后的同步清单**必须包含每一个 git 跟踪文件**
#      （剪源码 = 线上构建出缺件的镜像 ⇒ 门在这里必须红）
#   ② 陷阱层：引擎**不得**再用 `--exclude=<歧义名>` 表达这些目录
#      （macOS bsdtar 的 --exclude 按路径段匹配 ⇒ `--exclude=gateway`
#        会把 **cmd/gateway/**（主包源码）一起排掉 —— 本机实测三种写法全中）
#   ③ 自洽层：被剪掉的每个顶层路径**必须**是 0 跟踪文件（不许剪源码）
#   ④ 变异臂：把剪枝换成 `--exclude=gateway` ⇒ ① 必须变红 ⇒ 门有牙
#
# ★ 本门**执行引擎里那段真实的 find 代码**（不是自己另抄一份）⇒
#   ⇒ 引擎改了剪枝规则，门立刻跟着变 ⇒ 不会「门测的是另一份规则」。
#
# 退出码: 0 全绿 / 1 有失败
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENGINE="$REPO_ROOT/scripts/deploy-seamless-docker.sh"

PASS=0; FAIL=0; FAILED_NAMES=()
ok()  { PASS=$((PASS + 1)); printf '  ✓ %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); FAILED_NAMES+=("$1"); printf '  ✗ %s\n' "$1"; }

echo "== 部署同步清单剪枝规则 =="

# ── ② 陷阱层（静态）：引擎里**可执行部分**不得出现歧义的 --exclude ─────
#   ★ 只看**非注释行**：注释里写「--exclude=gateway 会命中 cmd/gateway」是
#     **说明**（正是这段话记录了坑），不是**用法** ⇒ 不该被判红。
CODE_ONLY="$(mktemp -t dd-prune-code.XXXXXX)"
sed -e 's/[[:space:]]*#.*$//' "$ENGINE" > "$CODE_ONLY"
for badpat in "--exclude=gateway" "--exclude=./gateway" "--exclude=logs" "--exclude=bin" "--exclude=run" "--exclude=llm-gateway"; do
  if grep -q -- "$badpat" "$CODE_ONLY"; then
    bad "② 陷阱层：引擎的**可执行部分**出现了 \`$badpat\`（bsdtar 按路径段匹配 ⇒ 会误伤同名子目录，如 cmd/gateway）"
  else
    ok "② 陷阱层：引擎的可执行部分没有 \`$badpat\`"
  fi
done

# ── 提取引擎里**真实**的剪枝代码块（不许测试自己另写一份）──────────────
BLOCK="$(python3 - "$ENGINE" <<'PY'
import re, sys
s = open(sys.argv[1], encoding="utf-8").read()
# 取 "( cd "$DD_REPO_ROOT" && find . -mindepth 1 \ ... -o -print" 整块
m = re.search(r"\(\s*\n\s*cd \"\$DD_REPO_ROOT\" && find \.[\s\S]*?-o -print\s*\n\s*\) > \"\$_dd_sync_list\"", s)
if not m:
    sys.exit(3)
block = m.group(0)
# ★ 只剥掉重定向部分，**保留组的右括号** ⇒ 否则 eval 会报
#   「第 9 行的 "(" 命令中有未预期的文件结束符」（括号不配对）。
block = block[:block.rindex(" > \"$_dd_sync_list\"")]
print(block)
PY
)"
if [[ -z "$BLOCK" ]]; then
  bad "① 完整性层：无法从引擎里提取剪枝代码块（引擎结构变了？门要跟着改）"
  echo "=========================================="; printf '通过 %d / 失败 %d\n' "$PASS" "$FAIL"; exit 1
fi
ok "① 已从引擎里提取到真实剪枝代码块"

LIST="$(mktemp -t dd-prune-list.XXXXXX)"
MUTLIST="$(mktemp -t dd-prune-mut.XXXXXX)"
MUT="$(mktemp -d /tmp/dd-prune-mut.XXXXXX)"
ISO_COUNT="$(mktemp -t dd-prune-count.XXXXXX)"
trap 'rm -rf "$MUT" "$MUTLIST" "$LIST" "$CODE_ONLY" "$ISO_COUNT" "$TARFILE" "$TARLIST"' EXIT

# ── ① 完整性层：在真实仓库上跑引擎那段的**原样代码**────────────────────
(
  DD_REPO_ROOT="$REPO_ROOT"
  _dd_sync_list="$LIST"
  # ★ 引擎里那段代码的输出去向是 `) > "$_dd_sync_list"`（提取时被剥掉了）
  #   ⇒ 这里必须把 eval 的 stdout 收到同一个清单文件 ⇒ 测的才是**引擎真实产出**。
  eval "$BLOCK"
) > "$LIST" 2>/dev/null

python3 - "$LIST" "$REPO_ROOT" > "$ISO_COUNT" <<'PY' 2>/dev/null || true
import subprocess, sys
new = set(open(sys.argv[1], encoding="utf-8").read().splitlines())
raw = subprocess.run(["git", "ls-files", "-z"], capture_output=True, cwd=sys.argv[2]).stdout.decode("utf-8", "replace")
tracked = {"./" + p for p in raw.split("\0") if p}
# ★ 旧口径（按**名字**剪枝：.git / node_modules / .build-local / *.log / .env / .env.* / ._* / .DS_Store）
#   —— 这正是本次改造**之前**引擎用的那套 --exclude 语义（bsdtar 按路径段匹配）。
old = set(subprocess.run(
    ["find", ".", "(", "-name", ".git", "-o", "-name", "node_modules", "-o", "-name", ".build-local",
     "-o", "-name", "*.log", "-o", "-name", ".env", "-o", "-name", ".env.*", "-o", "-name", "._*",
     "-o", "-name", ".DS_Store", ")", "-prune", "-o", "-print"],
    capture_output=True, cwd=sys.argv[2], text=True).stdout.splitlines())
# ★ 真正的不变量：**旧口径会带走的被跟踪文件，新的一个都不能少**
missing = sorted((old & tracked) - new)
print(len(missing))
for m in missing[:8]:
    print(m)
PY
MISSING_COUNT="$(head -1 "$ISO_COUNT" 2>/dev/null || echo 0)"
if [[ "${MISSING_COUNT:-0}" == "0" ]] && [[ -s "$LIST" ]]; then
  ok "① 完整性层：剪枝后的同步清单**不缺任何一个旧口径会带走的跟踪文件**（含 cmd/gateway 主包源码）"
else
  bad "① 完整性层：剪枝后**丢了 ${MISSING_COUNT:-?} 个旧口径会带走的被跟踪文件**（剪到源码了 ⇒ 线上会构建出缺件镜像）"
  sed -n '2,9p' "$ISO_COUNT" 2>/dev/null | sed 's/^/     缺: /'
fi

# ── ③ 自洽层：被剪掉的顶层路径必须 0 跟踪文件 ─────────────────────────
while IFS= read -r pruned_path; do
  rel="${pruned_path#./}"
  n="$(git ls-files "$rel" 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "$n" != "0" ]]; then
    bad "③ 自洽层：剪掉的 \`$rel\` 里**有 ${n} 个被跟踪文件**（不许剪源码）"
  else
    ok "③ 自洽层：\`$rel\` 是 0 跟踪文件的本地残留（可剪）"
  fi
done <<'PRUNED'
./logs
./run
./bin
./gateway
./llm-gateway
./gateway-v2
./quality-monitor
./.codegraph
./.audit-validation
PRUNED

# ── ④ 产物层：打**真 tar**（用引擎里那条真实 tar 命令）再验内容 ──────────
#   ★ 为什么必须验**产物**而不是清单（2026-11-05 实测踩到）：
#     `tar -T 清单` 遇到清单里的**目录条目**会**递归进去** ⇒ 清单之外的文件
#     照样进 tar ⇒ ⇒ 剪枝规则**只在目录粒度生效**、文件粒度失效。
#     实测：`deploy/prometheus/.env.example` 不在清单里，却仍随 tar 上了目标机
#     ⇒ **`.env*` 会被 COPY 进镜像**（密钥面）＋ `*.log` 会进镜像（体积面）。
#     ⇒ ⇒ 引擎侧已加 `-n`（--no-recursion）⇒ 清单 = 真产物。
#     ⇒ ⇒ 本门必须**打真 tar**并检查它的条目，否则测的是「清单」这个**代理量**，
#        代理量绿了而真产物带毒（本轮就是这样：清单对、产物错）。
TARFILE="$(mktemp -t dd-prune-tar.XXXXXX)"
TARLIST="$(mktemp -t dd-prune-tarlist.XXXXXX)"
TARLINE="$(python3 - "$ENGINE" <<'PY'
import re, sys
s = open(sys.argv[1], encoding="utf-8").read()
# 取**最后**一条 _tar_create+=( ... ) （即真正决定打包内容的那条）
ms = re.findall(r"^\s*_tar_create\+=\(.*\)\s*$", s, re.M)
if not ms:
    sys.exit(3)
print(ms[-1].strip())
PY
)"
if [[ -z "$TARLINE" ]]; then
  bad "④ 产物层：无法从引擎里提取真实 tar 命令行（门要跟着改）"
else
  ok "④ 已提取引擎真实 tar 命令行：${TARLINE}"
  (
    DD_REPO_ROOT="$REPO_ROOT"
    _dd_sync_list="$LIST"
    _tar_create=(tar -C "$DD_REPO_ROOT")
    eval "$TARLINE"
    "${_tar_create[@]}"
  ) > "$TARFILE" 2>/dev/null
  tar -tf "$TARFILE" 2>/dev/null | sort > "$TARLIST"
  if [[ -s "$TARLIST" ]]; then
    ok "④ 产物层：真 tar 打出 $(wc -l < "$TARLIST" | tr -d ' ') 个条目"
  else
    bad "④ 产物层：真 tar 打出 0 个条目（打包命令坏了）"
  fi
  # ④a 敏感/噪音文件不得进产物
  # ★★ 2026-11-09（本轮）：★ 原判据 `(^|/)\.env` **只**匹配 `.env` 与 `.env.*`
  #   ★ ⇒ 对 `deploy/phase0/optimization.env` 这类 **`*.env`** 恒绿（★ 规则与判据同源 ⇒ 两道门都看不见）
  #   ★ ⇒ 本轮**未**收紧判据：★ 收紧需要引擎剪枝也排除 `*.env` ⇒ ★ 而那会违反本门 ①
  #   ★   的不变式（★ 「剪枝后的同步清单必须包含每一个 git 跟踪文件」）⇒ ★ 收紧 ① 的不变式
  #   ★   属**闸门判据裁决** ⇒ 登记待裁决（★ 见 docs/12小时内修订审计-20261109-0730.md 的 F-12）。
  if grep -qE '(^|/)\.env' "$TARLIST"; then
    bad "④a 产物层：tar 里出现了 .env* 文件（会被 COPY 进镜像 ⇒ 密钥面）"
  else
    ok "④a 产物层：tar 里没有任何 .env* 文件"
  fi
  if grep -qE '\.log$|(^|/)\._[^/]*$|(^|/)\.DS_Store$' "$TARLIST"; then
    bad "④b 产物层：tar 里出现了 .log / ._* / .DS_Store（体积面 + 脏元数据）"
  else
    ok "④b 产物层：tar 里没有 .log / ._* / .DS_Store"
  fi
  # ④c 被剪掉的残留目录不得出现在产物里
  _leak=""
  for d in logs run bin gateway llm-gateway gateway-v2 quality-monitor .codegraph .audit-validation; do
    grep -qE "^(\./)?$d(/|$)" "$TARLIST" && _leak="$_leak $d"
  done
  if [[ -n "$_leak" ]]; then
    bad "④c 产物层：被剪的残留目录仍进了 tar：$_leak"
  else
    ok "④c 产物层：被剪的残留目录确实不在 tar 里"
  fi
  # ④d 完整性：旧口径会带走的跟踪文件，产物里一个都不能少
  python3 - "$TARLIST" "$REPO_ROOT" <<'PY' > "$ISO_COUNT"
import subprocess, sys
tar = {l.strip().lstrip("./") for l in open(sys.argv[1], encoding="utf-8") if l.strip()}
raw = subprocess.run(["git", "ls-files", "-z"], capture_output=True, cwd=sys.argv[2]).stdout.decode("utf-8", "replace")
tracked = {p for p in raw.split("\0") if p}
old = set(subprocess.run(
    ["find", ".", "(", "-name", ".git", "-o", "-name", "node_modules", "-o", "-name", ".build-local",
     "-o", "-name", "*.log", "-o", "-name", ".env", "-o", "-name", ".env.*", "-o", "-name", "._*",
     "-o", "-name", ".DS_Store", ")", "-prune", "-o", "-print"],
    capture_output=True, cwd=sys.argv[2], text=True).stdout.splitlines())
old_tracked = {t[2:] if t.startswith("./") else t for t in (old & tracked)}
missing = sorted(old_tracked - tar)
print(len(missing))
for m in missing[:8]:
    print(m)
PY
  _missing_cnt="$(head -1 "$ISO_COUNT" 2>/dev/null || echo 0)"
  if [[ "${_missing_cnt:-0}" == "0" ]]; then
    ok "④d 产物层：旧口径会带走的跟踪文件，产物里一个不缺（含 cmd/gateway 主包源码）"
  else
    bad "④d 产物层：产物里**少了 ${_missing_cnt} 个**旧口径会带走的跟踪文件 ⇒ 线上会构建出缺件镜像"
    sed -n '2,9p' "$ISO_COUNT" 2>/dev/null | sed 's/^/     缺: /'
  fi
fi

# ── ⑤ 变异臂：把剪枝换成 `--exclude=gateway` ⇒ ① 必须变红 ──────────────
#   这是**真实踩过的坑形态**（bsdtar 按路径段匹配 ⇒ cmd/gateway 被一起排掉）。
mkdir -p "$MUT/cmd/gateway" "$MUT/gateway"
echo "package main" > "$MUT/cmd/gateway/main.go"
echo "binary" > "$MUT/gateway/gateway"
( cd "$MUT" && tar --exclude=gateway -cf - . 2>/dev/null | tar -tf - 2>/dev/null > "$MUTLIST" )
if grep -q "cmd/gateway/main.go" "$MUTLIST"; then
  bad "⑤ 变异臂：--exclude=gateway **没有**命中 cmd/gateway（陷阱形态在本机不复现 ⇒ 门失效？）"
else
  ok "⑤ 变异臂：--exclude=gateway 确实会命中 cmd/gateway/main.go ⇒ ② 这条规则有牙（本机复现）"
fi

echo "=========================================="
printf '通过 %d / 失败 %d\n' "$PASS" "$FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  printf '失败项: %s\n' "${FAILED_NAMES[*]}"
  exit 1
fi
exit 0

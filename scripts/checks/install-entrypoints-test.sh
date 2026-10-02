#!/usr/bin/env bash
# install-entrypoints-test.sh — 守住仓库根的安装入口。
#
# 这些文件是"用户从源码仓库拿到安装方法"的唯一表面，所以门必须盯住它们的
# 真实失败模式，而不是只跑 `bash -n`：
#
#   1. install.sh 必须能被 macOS 自带的 bash 3.2 解析（不用 mapfile / ${var,,}）
#   2. install.sh 的 `curl … | bash` 管道路径下必须真的吐出帮助文本
#      （BASH_SOURCE 在管道路径下为空，${BASH_SOURCE[0]} 展开成 shell 名）
#   3. install.ps1 必须带 UTF-8 BOM —— Windows PowerShell 5.1 没有 BOM 就按
#      系统 ANSI 解码，中文变乱码后尾字节变成 '?' 吃掉收尾引号，整个脚本
#      解析失败。这一条是 Windows 客户机上 install.ps1 能不能加载的分水岭。
#   4. install.bat 必须是纯 ASCII —— cmd.exe 在 chcp 生效之前就按 OEM 代码页
#      读整个文件，REM 行里的非 ASCII 可能解码成续行符把脚本截断。
#   5. install.bat 必须能真的把 install.ps1 叫起来（doctor 子命令）
#   6. npm 入口必须是合法 Node 脚本，且 --help 不联网就能出结果
#   7. 入口里出现的安装方式必须与 install.ps1 / npm 入口一致，不能各写一份
#
# 用法：bash scripts/checks/install-entrypoints-test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PASS=0
FAIL=0

cleanup() { :; }
trap cleanup EXIT

ok()    { PASS=$((PASS+1)); printf '[install-entry] ok: %s\n' "$1"; }
bad()   { FAIL=$((FAIL+1)); printf '[install-entry] FAIL: %s\n' "$1" >&2; }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (got '$2' want '$3')"; fi; }
contains() { if printf '%s' "$2" | grep -qF -- "$3"; then ok "$1"; else bad "$1 (输出里没有 '$3')"; fi; }
absent()   { if printf '%s' "$2" | grep -qF -- "$3"; then bad "$1 (输出里不该有 '$3')"; else ok "$1"; fi; }

for f in install.sh install.ps1 install.bat; do
  [[ -f "$ROOT/$f" ]] || { bad "缺少 $f"; continue; }
done
[[ "$FAIL" -eq 0 ]] || { printf '[install-entry] pass=%s fail=%s\n' "$PASS" "$FAIL"; exit 1; }

# ── 1. install.sh：语法 + 可移植性 ────────────────────────────────────
if bash -n "$ROOT/install.sh" 2>/dev/null; then ok "install.sh 语法正确"; else bad "install.sh 语法错误"; fi
comment_free() { grep -nE "$1" "$2" | grep -vE '^[[:space:]]*[0-9]+:[[:space:]]*#' || true; }
hits="$(comment_free 'mapfile|readarray|\$\{[A-Za-z_][A-Za-z0-9_]*,,|\$\{[A-Za-z_][A-Za-z0-9_]*\^\^' "$ROOT/install.sh")"
if [[ -n "$hits" ]]; then
  bad "install.sh 使用 bash 4.0+ 语法（macOS 自带 bash 3.2 跑不了）"; printf '%s\n' "$hits" >&2
else
  ok "install.sh 无 bash 4.0+ 语法"
fi
hits="$(comment_free '(^|[^[:alnum:]_])python3|(^|[^[:alnum:]_])jq[[:space:]]' "$ROOT/install.sh")"
if [[ -n "$hits" ]]; then
  bad "install.sh 依赖 python3/jq"; printf '%s\n' "$hits" >&2
else
  ok "install.sh 不依赖 python3/jq"
fi

# ── 2. 管道路径（curl … | bash）下 BASH_SOURCE 为空 ───────────────────
PIPE_OUT="$(cat "$ROOT/install.sh" | bash -s -- --help 2>&1)"
check "install.sh 管道路径 --help 退出码" "$?" "0"
contains "install.sh 管道路径真的吐出帮助" "$PIPE_OUT" "安装引导"
# 从 ${BASH_SOURCE[0]} 里 sed 帮助文本的写法在管道路径下会退化成
# "sed: can't read bash" 并且静默返回 0，所以上面必须抓到真实帮助。
absent "install.sh 管道路径没有踩空 BASH_SOURCE" "$PIPE_OUT" "can't read bash"

DOC_OUT="$(NO_INTERACTIVE=1 bash "$ROOT/install.sh" doctor 2>&1)"
check "install.sh doctor 退出码" "$?" "0"
contains "install.sh doctor 报出平台" "$DOC_OUT" "平台"
contains "install.sh doctor 列出安装方式" "$DOC_OUT" "可用安装方式"

VER_OUT="$(NO_INTERACTIVE=1 bash "$ROOT/install.sh" version 2>&1)"
check "install.sh version 退出码" "$?" "0"
contains "install.sh version 打印获取方式" "$VER_OUT" "install-scripts/install"

# 非交互 + 不可用的方式必须在动手前就失败，而不是下载完才失败。
BAD_OUT="$(NO_INTERACTIVE=1 bash "$ROOT/install.sh" --channel binary --mode lite 2>&1)"
check "install.sh 不可用方式退出码 2" "$?" "2"

# ── 3. install.ps1：必须有 UTF-8 BOM ──────────────────────────────────
BOM="$(head -c 3 "$ROOT/install.ps1" | od -An -tx1 | tr -d ' \n')"
check "install.ps1 带 UTF-8 BOM（PS 5.1 靠它按 UTF-8 解码）" "$BOM" "efbbbf"
if LC_ALL=C awk '{ if ($0 ~ /[\200-\377]/) { found = 1 } } END { exit found ? 0 : 1 }' "$ROOT/install.ps1"; then
  ok "install.ps1 确实含非 ASCII（BOM 门有牙齿，不是空门）"
else
  bad "install.ps1 全是 ASCII，BOM 检查对这个文件没有意义"
fi

# ── 4. install.bat：必须纯 ASCII ─────────────────────────────────────
if LC_ALL=C awk '{ if ($0 ~ /[\200-\377]/) { found = 1 } } END { exit found ? 0 : 1 }' "$ROOT/install.bat"; then
  bad "install.bat 含非 ASCII —— cmd.exe 在 chcp 生效前按 OEM 代码页读整个文件，REM 行会截断脚本"
else
  ok "install.bat 纯 ASCII"
fi

# ── 5. 入口一致性 + npm 入口 ─────────────────────────────────────────
SH_SRC="$(cat "$ROOT/install.sh")"
PS_SRC="$(cat "$ROOT/install.ps1")"
NPM_JS="$ROOT/npm/llm-gw-installer/bin/llm-gw-installer.js"
[[ -f "$NPM_JS" ]] || bad "缺少 npm 入口 $NPM_JS"

# 渠道清单必须来自"声明所有渠道"的那一处，而不是全文搜词：
#   install.sh   channel_available() 的 case 分支标签
#   install.ps1  $channels = @{ 'k' = ... } 的键
# 抽出来比集合，才能挡住"某一侧漏了一个渠道"这种靠人眼发现的问题。
sh_channels="$(awk '/^channel_available\(\) \{/ {f=1;next} f && /^    [a-z]+\)/ {gsub(/[^a-z]/,"",$1);print $1} f && /^}/ {exit}' "$ROOT/install.sh" | sort | tr '\n' ' ')"
ps_channels="$(sed -n '/\$channels = @{/,/^    }/p' "$ROOT/install.ps1" | sed -n "s/^        '\([a-z][a-z]*\)'.*/\1/p" | sort | tr '\n' ' ')"
check "install.sh 与 install.ps1 声明的安装方式集合一致" "$sh_channels" "$ps_channels"
for ch in source goinstall npm binary maintain; do
  case " $sh_channels " in
    *" $ch "*) ok "安装方式 '$ch' 在 channel_available 中已声明" ;;
    *) bad "install.sh 的 channel_available 缺少 '$ch'" ;;
  esac
done

if command -v node >/dev/null 2>&1; then
  if node --check "$NPM_JS" >/dev/null 2>&1; then ok "npm 入口是合法 Node 脚本"; else bad "npm 入口语法错误"; fi
  NPM_OUT="$(node "$NPM_JS" --help 2>&1)"
  check "npm 入口 --help 退出码" "$?" "0"
  contains "npm 入口 --help 有用法" "$NPM_OUT" "llm-gw-installer"
  # npm 入口不能自己抄一份下载/校验逻辑：sha256 硬门只存在于 maintain 侧。
  # 去掉注释再查，否则"我们故意不实现校验"这句说明自己会把门触发。
  if grep -vE "^[[:space:]]*(//|\*|/\*)" "$NPM_JS" | grep -qE 'sha256|createHash'; then
    bad "npm 入口自己实现了校验逻辑，会与 maintain 侧的 sha256 硬门分叉"
  else
    ok "npm 入口不重复实现下载/校验逻辑"
  fi

  # 回退路径必须把 argv 送出去。曾经这条路径把参数整个丢掉，
  # `npm i -g … -- --mode lite` 于是默默装了交互默认值。
  N1="$(node -e "const m=require(process.argv[1]);process.stdout.write(m.unixFallback('http://x/install',['--mode','lite']))" "$NPM_JS" 2>&1)"
  contains "npm 回退 unix 路径用 bash -s -- 传参" "$N1" "| bash -s -- "
  contains "npm 回退 unix 路径带出 --mode" "$N1" "--mode"
  contains "npm 回退 unix 路径带出 lite" "$N1" "lite"
  N2="$(node -e "const m=require(process.argv[1]);process.stdout.write(m.unixFallback('http://x/install',[]))" "$NPM_JS" 2>&1)"
  absent "npm 回退 unix 路径无参时不加空的 -s --" "$N2" "-s --"
  N3="$(node -e "const m=require(process.argv[1]);process.stdout.write(m.windowsFallback('http://x/install',['--mode','lite']))" "$NPM_JS" 2>&1)"
  contains "npm 回退 windows 路径把 --mode 翻成 MODE 环境变量" "$N3" '$env:MODE='
  contains "npm 回退 windows 路径带出 lite" "$N3" "lite"
  N4="$(node -e "const m=require(process.argv[1]);process.stdout.write(m.windowsFallback('http://x/install',['--yes']))" "$NPM_JS" 2>&1)"
  contains "npm 回退 windows 路径把 --yes 翻成 NO_INTERACTIVE" "$N4" '$env:NO_INTERACTIVE='
else
  printf '[install-entry] skip: 没有 node，跳过 npm 入口检查\n'
fi

# --yes 必须真的生效：曾经 ASSUME_YES 只被赋值、interactive() 从不读它，
# 于是帮助文本里「--yes 全部用默认值，不再提问」是一句空话。
if awk '/^interactive\(\) \{/,/^\}/' "$ROOT/install.sh" | grep -q 'ASSUME_YES'; then
  ok "install.sh interactive() 真的读了 ASSUME_YES（--yes 不是空承诺）"
else
  bad "install.sh interactive() 没有读 ASSUME_YES，--yes 不生效"
fi
if [[ "$(grep -c 'ASSUME_YES' "$ROOT/install.sh")" -ge 3 ]]; then
  ok "install.sh 的 ASSUME_YES 不止出现在赋值处"
else
  bad "install.sh 的 ASSUME_YES 只出现一次（赋值后从未被读，是死变量）"
fi

# 源码里不能混进 U+FFFD（编码坏字节）。它会跟着注释一路进仓库，而且本地
# 看不出差别，评审时也不会有人去数。
if LC_ALL=C grep -q $'\xef\xbf\xbd' "$ROOT/install.sh"; then
  bad "install.sh 含 U+FFFD 坏字节"
else
  ok "install.sh 无 U+FFFD 坏字节"
fi

# ── 6. 子命令透传 ───────────────────────────────────────────────────
# 旧实现写的是 PASSTHRU="$ACTION"（此时 ACTION 已被赋成字面量
# "passthrough"）且 PASSTHRU_ARGS="$*"（整体加引号），于是二进制收到的第
# 一个参数是 "passthrough"、后面所有参数被拼成一个。这条路径此前完全没有被
# 测到。
TESTDIR="$(mktemp -d)"
trap 'rm -rf "$TESTDIR"' EXIT
FAKE_HOME="$TESTDIR/fakehome"
mkdir -p "$FAKE_HOME/bin"
cat >"$FAKE_HOME/bin/llm-gw-installer.exe" <<'FAKEEOF'
#!/usr/bin/env bash
printf 'FAKE_ARGS:'
for a in "$@"; do printf ' [%s]' "$a"; done
printf '\n'
FAKEEOF
chmod +x "$FAKE_HOME/bin/llm-gw-installer.exe"

PT="$(LLM_GATEWAY_HOME="$FAKE_HOME" bash "$ROOT/install.sh" upgrade --target 1.2.3 2>&1)"
check "透传子命令退出码" "$?" "0"
contains "透传把子命令原样交给二进制" "$PT" "FAKE_ARGS: [upgrade]"
contains "透传保留子命令的每个参数" "$PT" "[--target] [1.2.3]"
absent "透传没有把字面量 passthrough 传给二进制" "$PT" "passthrough"

PT2="$(LLM_GATEWAY_HOME="$FAKE_HOME" bash "$ROOT/install.sh" activate 2>&1)"
contains "透传 activate" "$PT2" "FAKE_ARGS: [activate]"

PT3="$(LLM_GATEWAY_HOME="$FAKE_HOME" bash "$ROOT/install.sh" uninstall --purge 2>&1)"
contains "透传 uninstall 带 flag" "$PT3" "FAKE_ARGS: [uninstall] [--purge]"

BAD_SUB="$(LLM_GATEWAY_HOME="$FAKE_HOME" bash "$ROOT/install.sh" frobnicate 2>&1)"
check "未知子命令被拒（不再无脑透传）" "$?" "1"
contains "未知子命令报错点名" "$BAD_SUB" "frobnicate"

# --yes 必须真的不再提问（曾经只赋值、从未被读）。
PT4="$(LLM_GATEWAY_HOME="$FAKE_HOME" bash "$ROOT/install.sh" --yes upgrade 2>&1)"
contains "--yes 不影响子命令透传" "$PT4" "FAKE_ARGS: [upgrade]"

# ── 6. doctor 真的能跑（仅在 Windows 上有 PowerShell 时）──────────────
if [[ "${OS:-}" == "Windows_NT" ]] && command -v powershell >/dev/null 2>&1; then
  BAT_OUT="$(cd "$ROOT" && cmd //c "install.bat doctor" 2>&1)"
  check "install.bat doctor 退出码" "$?" "0"
  contains "install.bat 真的调起了 install.ps1" "$BAT_OUT" "体检报告"
else
  printf '[install-entry] skip: 非 Windows 或无 PowerShell，跳过 install.bat 实跑\n'
fi

printf '\n[install-entry] pass=%s fail=%s\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]

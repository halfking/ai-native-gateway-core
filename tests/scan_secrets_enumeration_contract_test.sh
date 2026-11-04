#!/usr/bin/env bash
# tests/scan_secrets_enumeration_contract_test.sh —— scripts/scan-secrets.sh 的**枚举覆盖**行为门。
#
# ★ 2026-11-04 审计轮（F-18）新增。
# ★ 本门钉住「秘密扫描器在 `--tracked-only` 下**枚举到了哪些文件**」：
#   ①  ASCII 文件名的跟踪文件 ⇒ 其中的秘密**必须被报出来**（★ 基线：扫描器本身在工作 ★）；
#   ②  ★★ **非 ASCII 文件名**的跟踪文件 ⇒ 其中的秘密**必须被报出来** ★★
#       ⇒ ★★ 这就是 F-18 的核心断言：★ 旧实现【★ 静默丢弃 ★】这一批文件 ★★
#           （★ `git ls-files` 默认 `core.quotePath=true` ⇒ 非 ASCII 路径被输出成
#             ★ 八进制转义＋双引号 ⇒ `[[ -f ]]` 判假 ⇒ **不报错、不计数、不让任何门变红** ★★）；
#   ③  文件名**含空格**的跟踪文件 ⇒ 同 ②（★ `ls-files` 的引号转义面 ★）；
#   ④  `Files scanned` 计数 == 夹具文件数（★ 枚举**没有漏** ★）；
#   ⑤  ★ 输出里**不含八进制转义形态**（★ 证明没走 `core.quotePath=true` 那条路 ★）；
#   ⑥  ★ **零断言守卫**：★ 若三个夹具一个都没被报出来 ⇒ 门**必须红**
#       ⇒ ★ 封堵「空链 ⇒ 零断言 ⇒ 假绿」（★ 与 F-15 门同一款 ★）。
#   ⑦  ★★ **空扫描必须 fail-closed**（★ F-23 ★）：★ 枚举面为空 ⇒ 扫描器什么也没扫
#       ⇒ ★ 旧实现照样打 "✅ CLEAN" 且 rc=0 ⇒ ★ **空扫描 = 假绿 ★**
#       ⇒ ★ 一次参数顺序笔误就能让闸门失效（★ `--paths` 会吃掉后续所有参数 ★）。
#
# ★ ★ 为什么要有这道门：★ 本仓的纪律是「门没有调用方 = 声明不是门」，
#   而 `--tracked-only` 这条枚举分支此前**没有任何机器覆盖**
#   ⇒ ★ 而它恰好是【★ 两道公开仓闸门 ★】（`.githooks/pre-push` ＋
#     ★ `scripts/sync-to-github.sh`）★ 用的那一条 ★ ⇒ ⇒ ★ 它失明 = 公开仓闸门失明 ★★。
#
# ★ ★ 与仓内既有惯例一致：★ `internal/relrefs/files.go:15-24` **早就**为同一个坑
#   写下了注释并用了 `ls-files -z` ⇒ ★ 本门就是把这个惯例**钉成机器门** ★
#   （★ 那处是 Go 侧的量具、★ 这里是 shell 侧的量具、★ 同一个坑 ★）。
#
# ★ 零外部依赖、零仓库副作用（全部在 mktemp 里）⇒ 可进 CI 快门。
# ★ 可选参数 $1：★ 把门指向**候选**实现 ⇒ 「这个改动能不能过门」可以在提交前自测、
#   ★ 变异测试（★ 造一个坏实现看门会不会红 ★）也用它。
# Run: bash tests/scan_secrets_enumeration_contract_test.sh [candidate-scanner-path]
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCAN="${1:-$REPO_ROOT/scripts/scan-secrets.sh}"
[[ -f "$SCAN" ]] || { echo "missing $SCAN" >&2; exit 1; }

# ★ scan-secrets.sh 自 2026-10-27 起带 bash>=4 运行时守卫
#   （bash 3.2 下扫描器曾假绿 findings 0）⇒ 测试必须用 bash≥4 调它，
#   否则测的是守卫而不是被测行为（tests/deploy_sops_test.sh 同款解析）。
BASH4="$HOME/sdk/bash5/bin/bash"
[[ -x "$BASH4" ]] || BASH4="$(command -v bash)"
if [[ "${BASH_VERSINFO[0]}" -lt 4 && "$BASH4" == "$(command -v bash)" ]]; then
  echo "FATAL: 需要 bash>=4 来调被测脚本（macOS 自带 3.2 会被脚本守卫拒绝）" >&2
  exit 64
fi

TMP="$(mktemp -d)"
# ★ 先 cd 出临时目录再删：★ 否则「删当前工作目录」会被安全层拦下
#   （★ macOS 上表现为「refusing to trash protected path」★）⇒ ★ 门自己收不干净 ★
trap 'cd / 2>/dev/null; rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); printf 'OK   %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1" >&2; }

# ── 造夹具仓：★ 三个文件、★ 三个不同的「文件名难点」 ★ ──────────────────
REPO="$TMP/repo"
mkdir -p "$REPO"
cd "$REPO" || exit 1
git init -q . >/dev/null 2>&1
# ★ ★ 显式钉住 `core.quotePath=true`（★ git 的默认值 ★）
#   ⇒ 门的结果**不依赖开发者本机的全局 git 配置**（★ 确定性 ★）
git config core.quotePath true

# ★ 夹具用的秘密：★ 命中 `LLM_APIKEY` 规则（★ `sk-` + 32 位字母数字 ★）
#   ⇒ 不被任何白名单词豁免、★ 也不触发别的规则
SECRET="sk-0123456789abcdef0123456789abcdef"

F_ASCII="ascii-fixture.txt"
F_CJK="中文夹具-密钥.txt"
F_SPACE="space name fixture.txt"

printf 'token=%s\n' "$SECRET" > "$F_ASCII"
printf 'token=%s\n' "$SECRET" > "$F_CJK"
printf 'token=%s\n' "$SECRET" > "$F_SPACE"

git add -A >/dev/null 2>&1
git -c user.email=t@t -c user.name=t commit -qm fixture >/dev/null 2>&1

# ── 跑扫描器（★ `--tracked-only` ⇒ 走的就是那条有洞的枚举分支 ★）────────
OUT="$TMP/out.txt"
"$BASH4" "$SCAN" --mode=strict --format=text \
  --repo-root="$REPO" --paths="$REPO" --tracked-only > "$OUT" 2>&1
RC=$?
SCANNED="$(grep -E '^  Files scanned: ' "$OUT" | head -1 | sed 's/.*: //' | tr -d ' ')"
printf -- '--- scanner rc=%s  Files scanned=%s ---\n' "$RC" "${SCANNED:-?}"
sed 's/^/    /' "$OUT" | head -40

# ① ASCII 文件名 ⇒ 必须被报出来
if grep -qF "$F_ASCII" "$OUT"; then ok "① ASCII 文件名的夹具被报出来"; else bad "① ASCII 文件名的夹具没被报出来"; fi
# ② ★ 非 ASCII 文件名 ⇒ 必须被报出来（★ F-18 的核心断言 ★）
if grep -qF "$F_CJK" "$OUT"; then ok "② 非 ASCII 文件名的夹具被报出来（★ F-18 ★）"; else bad "② 非 ASCII 文件名的夹具没被报出来（★ F-18 回归 ★）"; fi
# ③ 文件名含空格 ⇒ 必须被报出来
if grep -qF "$F_SPACE" "$OUT"; then ok "③ 含空格文件名的夹具被报出来"; else bad "③ 含空格文件名的夹具没被报出来"; fi
# ④ 枚举计数 ⇒ 三个夹具都要在
if [[ "${SCANNED:-0}" == "3" ]]; then ok "④ Files scanned == 3"; else bad "④ Files scanned == 3（实际 ${SCANNED:-?}）"; fi
# ⑤ 输出里不得出现八进制转义形态（★ git 的 quotePath 转义 ★）
if grep -qE '\\[0-7]{3}' "$OUT"; then bad "⑤ 输出里出现了八进制转义路径（★ 走了 core.quotePath=true 那条路 ★）"; else ok "⑤ 输出里没有八进制转义路径"; fi

# ⑥ ★ 零断言守卫：★ 三个夹具一个都没被报出来 ⇒ 门必须红 ★
REPORTED=0
for f in "$F_ASCII" "$F_CJK" "$F_SPACE"; do
  grep -qF "$f" "$OUT" && REPORTED=$((REPORTED + 1))
done
if [[ "$REPORTED" -eq 3 ]]; then
  ok "⑥ 三个夹具全部被报出来（★ 非零断言 ★）"
else
  bad "⑥ 只有 ${REPORTED}/3 个夹具被报出来（★ 扫描器整体失灵时门必须红 ★）"
fi

# ⑦ ★★ 空扫描必须【★ fail-closed ★】（★ F-23 ★）
#   ★ 场景：★ 枚举面为空 ⇒ 扫描器**什么也没扫** ⇒ ★ 旧实现照样打 "✅ CLEAN" 且 rc=0
#   ⇒ ★ 那就是【★ 假绿 ★】⇒ ★ 一次参数顺序笔误就能让闸门失效 ★
EMPTY="$TMP/empty"
mkdir -p "$EMPTY"
EMPTY_OUT="$TMP/empty-out.txt"
"$BASH4" "$SCAN" --mode=strict --format=text --repo-root="$REPO" --paths="$EMPTY" --tracked-only > "$EMPTY_OUT" 2>&1
EMPTY_RC=$?
if [[ "$EMPTY_RC" -ne 0 ]]; then
  ok "⑦ 空扫描 fail-closed（★ F-23：rc=${EMPTY_RC} ≠ 0 ★）"
else
  bad "⑦ 空扫描没有 fail-closed（★ rc=0 ⇒ 报 CLEAN ⇒ 假绿 ★）"
fi
if grep -q '✅ CLEAN' "$EMPTY_OUT"; then
  bad "⑦ 空扫描却打印了 ✅ CLEAN（★ 必须不给绿 ★）"
else
  ok "⑦ 空扫描没有打印 ✅ CLEAN"
fi

echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]

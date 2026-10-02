#!/usr/bin/env bash
# user-scripts-sync-test.sh — 守住 scripts/user 与 maintain SSOT 的一致性。
#
# ## 为什么这道门存在
#
# scripts/user 曾经是 maintain 的一份手工 fork，靠文件头里一句
# "SOURCE_OF_TRUTH / SYNC_POLICY" 维持。那句话没有任何机器可执行的约束，
# 于是这份 fork 一直烂在原地：里面仍是 python3 解析 JSON、mapfile、
# ${var,,} 大小写折叠 —— 也就是 Windows（python3 是 0 字节存根、退出码 49）
# 与 macOS（自带 bash 3.2）上都跑不起来的那一版。maintain 侧早在
# fix(install+windows) 里修好了，fork 侧一份都没跟上，而没有任何门会发现。
#
# 现在 scripts/user 是 SSOT 的逐字节副本，一致性由 sha256 清单守护。
# 改动流程：先改 maintain，重新复制，再跑 regen-user-scripts-manifest.sh。
# 直接在本仓库改 scripts/user 而不同步 maintain，会被下面第 1 条抓住。
#
# ## 覆盖的四件事
#   1. 每个文件与清单里的 sha256 一致（挡住本地偷改与漏复制）
#   2. 清单里的文件一个都不能少
#   3. 脚本里没有 python3 / mapfile / ${var,,}（注释里提到不算）
#   4. lib/common.sh 不能再回来 —— 它是旧实现，函数已被 SSOT 内联取代
#
# 用法：bash scripts/checks/user-scripts-sync-test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
US="scripts/user"
MANIFEST="$US/.ssot-manifest.sha256"
PASS=0
FAIL=0

ok()    { PASS=$((PASS + 1)); printf '[user-sync] ok: %s\n' "$1"; }
bad()   { FAIL=$((FAIL + 1)); printf '[user-sync] FAIL: %s\n' "$1" >&2; }

if [[ ! -f "$MANIFEST" ]]; then
  bad "缺少清单 $MANIFEST（跑 scripts/checks/regen-user-scripts-manifest.sh 生成）"
  printf '[user-sync] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

# ── 1/2. 逐字节一致 + 文件齐全 ───────────────────────────────────────────
# Hash over LF-normalized bytes, because .gitattributes is `* text=auto eol=lf`:
# git stores LF, a Windows checkout has CRLF. Using sha256sum -c on the raw
# file would make this gate pass only on the machine that generated the
# manifest. That bug was in the first version of this gate and is exactly the
# "green only on my laptop" failure mode being eliminated.
norm_sha() { sed -e 's/\r$//' "$1" | sha256sum | cut -d' ' -f1; }

MANIFEST_FILES="$(sed -e 's/^[0-9a-f]*  *//' "$MANIFEST" | sed '/^$/d' | sort)"

DRIFT=0
while IFS= read -r f; do
  [[ -n "$f" ]] || continue
  want="$(awk -v p="$f" '$2 == p { print $1 }' "$MANIFEST")"
  if [[ ! -f "$f" ]]; then
    bad "清单里的文件缺失：$f"; DRIFT=1; continue
  fi
  if [[ "$(norm_sha "$f")" != "$want" ]]; then
    bad "与 SSOT 清单不一致（本地被改过，或从 maintain 漏复制）：$f"
    DRIFT=1
  fi
done <<<"$MANIFEST_FILES"
[[ "$DRIFT" -eq 0 ]] && ok "全部 $(printf '%s\n' "$MANIFEST_FILES" | wc -l | tr -d ' ') 个文件与清单 sha256 一致（LF 归一）"

# 反向：scripts/user 下不允许出现清单外的文件（旧实现就是这样悄悄回来的）。
# find 产出的已经是相对仓库根的 scripts/user/...，与清单同口径，直接比。
STRAY=0
while IFS= read -r f; do
  if ! printf '%s\n' "$MANIFEST_FILES" | grep -qxF "$f"; then
    bad "scripts/user 下出现清单外文件（不在 SSOT 里）：${f#"$US"/}"
    STRAY=1
  fi
done < <(find "$US" -type f ! -name '.ssot-manifest.sha256' | sort)
[[ "$STRAY" -eq 0 ]] && ok "scripts/user 下没有清单外文件"

# ── 3. 可移植性：注释里提到不算，必须是真实代码 ──────────────────────────
for f in install.sh install-host.sh install-docker.sh upgrade.sh client-deploy.sh; do
  p="$US/$f"
  [[ -f "$p" ]] || { bad "缺少 $p"; continue; }
  if ! bash -n "$p" 2>/dev/null; then bad "$f 语法错误"; else ok "$f 语法正确"; fi
  hits="$(grep -nE '(^|[^[:alnum:]_])python3|mapfile|readarray|\$\{[A-Za-z_][A-Za-z0-9_]*,,' "$p" \
    | grep -vE '^[[:space:]]*[0-9]+:[[:space:]]*#' || true)"
  if [[ -n "$hits" ]]; then
    bad "$f 出现 python3 / bash 4.0+ 语法（Windows 与 macOS 跑不了）"
    printf '%s\n' "$hits" >&2
  else
    ok "$f 无 python3 / bash 4.0+ 语法"
  fi
done

# client-deploy.ps1 必须带 UTF-8 BOM，否则 PowerShell 5.1 按 ANSI 解码后解析失败。
ps1="$US/client-deploy.ps1"
if [[ -f "$ps1" ]]; then
  bom="$(head -c 3 "$ps1" | od -An -tx1 | tr -d ' \n')"
  if [[ "$bom" == "efbbbf" ]]; then
    ok "client-deploy.ps1 带 UTF-8 BOM（PS 5.1 靠它按 UTF-8 解码）"
  else
    bad "client-deploy.ps1 缺 UTF-8 BOM —— PowerShell 5.1 会按 ANSI 解码中文并解析失败"
  fi
fi

# ── 4. 旧实现不得复活 ─────────────────────────────────────────────────
if [[ -e "$US/lib/common.sh" ]]; then
  bad "lib/common.sh 回来了：那是 python3 时代的旧实现，detect_install_root / catalog_sha 已被 SSOT 内联的 awk 版取代"
else
  ok "lib/common.sh 仍是已删除状态"
fi

# ── 5. SSOT 自己的布局门也跑一遍 ───────────────────────────────────────
if [[ -f "$US/lib/kaixuan-layout-test.sh" ]]; then
  if bash "$US/lib/kaixuan-layout-test.sh" >/dev/null 2>&1; then
    ok "kaixuan-layout-test.sh 通过"
  else
    bad "kaixuan-layout-test.sh 失败"
  fi
fi

printf '\n[user-sync] pass=%s fail=%s\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]

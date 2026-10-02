#!/usr/bin/env bash
# check-build-tags.sh — 在**每个**构建 tag 配置下编译含构建约束的包。
#
# 为什么这道门存在（2026-10-02，审计 §9.65）：
#
# `verify.sh` 的 `go test ./...` 与 `go vet ./...` **都不带 tag**；
# `integration-testcontainers-ci.yml` 的 paths 过滤又不含 `admin/**`
# （实测漏覆盖 5452 个 .go 文件，排除 vendor 仍有 457 个）。
# 于是「只在某个 tag 下才参与编译的文件」实际处于**无人编译**状态。
#
# 2026-10-02 实测的后果：`admin/request_logs_indirect_readers_test.go`
# （无 tag）引用 `admin/v1_direct_padded_column_reader_test.go`
# （`//go:build !integration`）里的 `v1DirectTables`，`-tags=integration` 下
# 整个 admin 包**编译不过**；而默认配置全绿、主干 CI 全绿。断链来自
# 32aa86eeb（只动 admin/**），该提交从未触发过 integration 作业。
#
# 门的作用：把「谁定义符号」与「谁引用符号」的 tag 依赖变成可判红的事实。
# tag 词表**从源码推导**而不是写死——写死清单本身就是「真相会漂移，而没登记
# 就不检查是最安静的一种失败」；新增 tag 时这道门自动跟上。
#
# ── 这道门自己踩过的三个坑（都是「门红了」与「代码没坏」长得一样的形态）──
#
# 1. **裸目录名 = 假红**：go vet 的参数必须是 `./admin` 而不是 `admin`，
#    否则它把 admin 当标准库路径，报 "package admin is not in std"。
# 2. **module 归属**：仓库有 4 个 module（installer/、scripts/injection-test/、
#    tests/local/gomapper/ 各带 go.mod）。主 module 的 go vet 对它们的能力是
#    "does not contain package" ⇒ 按 module 分别跑；且**必须从文件所在目录**
#    向上找 go.mod，从仓库根开始会把所有包都算进主 module，门全绿而
#    installer 根本没被检。
# 3. **"build constraints exclude all Go files" 不是良性输出**——这是本门
#    最贵的一个教训。第一版把它当良性过滤掉，结果：
#    `go vet -tags=integration <46 个包>` 只要有一个包整包被约束挡住，
#    **就不会 type-check 其余任何包**，admin 里真实的
#    `undefined: v1DirectTables` 被完全吞掉，门在自己的目标缺陷上是**绿的**。
#    正确做法是**先按配置剔除不存在的包再 vet**，而不是事后过滤输出。
#    ⇒ 一般化：「检查器自己的失败/警告会截断后续检查」时，先过滤会让门失明，
#      必须改成分阶段执行。判据次序会吃掉你想测的那条。
#
# 退出码：0 = 全部配置编译通过；非 0 = 至少一个配置断链（红因逐条原样打印）。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PY_DISCOVER='
import re, subprocess, collections, os, json
GO = set("windows linux darwin freebsd netbsd openbsd dragonfly solaris illumos aix android ios js plan9 cgo race ignore".split())
files = [x for x in subprocess.check_output(
    ["git","ls-files","*.go",":(exclude)vendor/**"], text=True).split("\n") if x]
REPO = os.path.abspath(".")
def mod_of(path):
    # 必须从**文件所在目录**向上找 go.mod（见文件头第 2 条坑）。
    root = os.path.abspath(os.path.dirname(path))
    while True:
        if os.path.exists(os.path.join(root, "go.mod")):
            return os.path.relpath(root, REPO)
        nxt = os.path.dirname(root)
        if nxt == root:
            return "."
        root = nxt
tags = set(); pkgs = collections.defaultdict(set)
for f in files:
    if f.startswith("vendor/"): continue
    try: head = open(f, encoding="utf-8").read(4000)
    except Exception: continue
    m = re.search(r"^//go:build (.+)$", head, re.M)
    if not m: continue
    d = os.path.dirname(f)
    ts = {t for t in re.findall(r"\b([A-Za-z_][A-Za-z0-9_]*)\b", m.group(1)) if t not in GO}
    tags |= ts
    pkgs[mod_of(f)].add(("./"+d) if d else ".")
print(json.dumps({"tags": sorted(tags), "pkgs": {k: sorted(v) for k, v in pkgs.items()}}, ensure_ascii=False))
'

MOD_JSON="$(python3 -c "$PY_DISCOVER")"
mapfile -t TAGS < <(python3 -c "import json,sys;print('\n'.join(json.loads(sys.stdin.read())['tags']))" <<<"$MOD_JSON")
mapfile -t MODS < <(python3 -c "import json,sys;print('\n'.join(sorted(json.loads(sys.stdin.read())['pkgs'])))" <<<"$MOD_JSON")

if [[ ${#TAGS[@]} -eq 0 ]]; then
  echo "[build-tags] 仓库内没有构建约束文件，跳过"
  exit 0
fi

echo "[build-tags] 仓库自有 tag ${#TAGS[@]} 个: ${TAGS[*]}"
echo "[build-tags] 配置矩阵 = 基线(无 tag) + 每个 tag = $(( ${#TAGS[@]} + 1 )) 种"

fail=0

for mod in "${MODS[@]}"; do
  # 包路径是 repo 相对，go vet 在 module 根执行 ⇒ 转成 module 相对。
  # 用 startswith 去前缀，不要用 lstrip('./')：它剥的是字符集合而不是前缀，
  # 会把 './internal/...' 变成 'internal/...'，裸目录名又回到第 1 条坑。
  mapfile -t CUR_PKGS < <(python3 -c "
import json,sys
mod=sys.argv[1]
pre='./' if mod=='.' else './'+mod.rstrip('/')+'/'
print('\n'.join('./'+p[len(pre):] if p.startswith(pre) else p
                for p in json.loads(sys.stdin.read())['pkgs'].get(mod,[])))" "$mod" <<<"$MOD_JSON")
  [[ ${#CUR_PKGS[@]} -eq 0 ]] && continue
  echo "[build-tags] module=${mod}  候选包 ${#CUR_PKGS[@]} 个"

  # 「全部文件都带约束」的包才可能整包消失；有任一无条件文件的包在任何
  # 配置下都存在，永远不必进可选集。只需对这批（通常个位数）做 go list。
  mapfile -t OPTIONAL_REL < <(cd "$REPO_ROOT/$mod" && go list -e \
      -f '{{.Dir}}|{{len .GoFiles}}|{{len .TestGoFiles}}|{{len .XTestGoFiles}}' \
      "${CUR_PKGS[@]}" 2>/dev/null \
    | awk -F'|' '($2+$3+$4)==0 {print $1}' \
    | sed "s|^$REPO_ROOT/||; s|^$REPO_ROOT$|.|" | sort || true)

  local_rel() { echo "${1#"$REPO_ROOT"/}"; }

  BASE_PKGS=()
  for d in "${CUR_PKGS[@]}"; do
    d2="${d#./}"
    is_opt=0
    for o in "${OPTIONAL_REL[@]:-}"; do [[ "$o" == "$d2" ]] && is_opt=1; done
    [[ $is_opt -eq 0 ]] && BASE_PKGS+=("$d")
  done
  OPTIONAL_PKGS=()
  for o in "${OPTIONAL_REL[@]:-}"; do [[ -n "$o" ]] && OPTIONAL_PKGS+=("./$o"); done

  run_cfg() { # $1=label  $2..=go vet 参数
    local label="$1"; shift
    local out rc present=()
    while IFS= read -r d; do
      [[ -n "$d" ]] || continue
      present+=("./${d#"$REPO_ROOT"/}")
    done < <(cd "$REPO_ROOT/$mod" && go list -e "$@" \
        -f '{{.Dir}}|{{len .GoFiles}}|{{len .TestGoFiles}}|{{len .XTestGoFiles}}' \
        "${OPTIONAL_PKGS[@]}" 2>/dev/null | awk -F'|' '($2+$3+$4)>0 {print $1}' || true)

    dirs=("${BASE_PKGS[@]}" "${present[@]}")
    if [[ ${#dirs[@]} -eq 0 ]]; then
      echo "[build-tags]   PASS  配置=$label  module=${mod}  （该配置下无可编译包）"
      return 0
    fi
    set +e
    out="$(cd "$REPO_ROOT/$mod" && go vet "$@" "${dirs[@]}" 2>&1)"
    rc=$?
    set -e
    if [[ $rc -ne 0 ]]; then
      echo "[build-tags]   FAIL  配置=$label  module=${mod}  受检 ${#dirs[@]} 包" >&2
      # 红因逐条**原样**打印：只报告不打印时，读者无法区分「没有红因」与
      # 「红因被过滤掉了」——本门第一版正是这样沉默的。
      printf '%s\n' "$out" | sed 's/^/      /' >&2
      return 1
    fi
    echo "[build-tags]   ok    配置=$label  module=${mod}  受检 ${#dirs[@]} 包" \
         "（可选 ${#OPTIONAL_PKGS[@]}，本配置存在 ${#present[@]}）"
    return 0
  }

  run_cfg "<no-tags>" || fail=1
  for t in "${TAGS[@]}"; do
    run_cfg "$t" -tags="$t" || fail=1
  done
done

if [[ $fail -ne 0 ]]; then
  cat >&2 <<'EOF'

[build-tags] 断链判读（这不是「门太严」，是符号的 tag 归属错了）：

  · 文件 A 用 `//go:build T` 声明符号 X，文件 B 在**别的** tag 条件下引用 X
    → X 的可用配置是两者约束的交集，通常比声明方窄。共享 helper 应放进
      无 tag 文件，让门与它的使用者都能编译。
  · **不要**为了「让门变绿」给引用方也加上同一个 tag —— 那是把编译错误
      换成「这道门在新配置下静默不跑」，与本门要防的是同一族失败。

[build-tags] 覆盖缺口同源：integration CI 的 paths 过滤不含 admin/** 等 20 个
  顶层目录，只动那些目录的提交不会触发任何带 tag 的编译。
EOF
  exit 1
fi

echo "[build-tags] PASS  ${#MODS[@]} 个 module × $(( ${#TAGS[@]} + 1 )) 种配置全部编译通过"

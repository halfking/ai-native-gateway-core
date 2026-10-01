#!/usr/bin/env bash
# 校验：internal/ 下每个 *guard 包都已被登记进 Makefile 的 GUARD_PACKAGES。
#
# R69 的发现背景：internal/ 下的审计守卫（rowsguard / errdiscard）当时
# **没有被任何强制路径执行**——pre-push 的 Go 测试是 opt-in（RUN_GO_TESTS=1）
# 且只跑 licensing + envinjector，CI 各 workflow 也没覆盖。一门没人跑的守卫
# 是文档，不是门。
#
# 「加了个守卫但忘了接进执行路径」是这类体系最常见的腐化方式，且**悄无声息**：
# 新守卫的测试在本地跑是绿的，CI 也是绿的，只是它从来没在强制路径上跑过。
# 本脚本让「漏登记」在提交时/构建时就红。
#
# 用法：scripts/checks/guards-sync.sh
# 退出码：0 = 全部已登记；1 = 有守卫包漏登记；2 = 用法/环境错误。
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

if [[ ! -f Makefile ]]; then
  echo "guards-sync: Makefile not found at $repo_root" >&2
  exit 2
fi

# 从 Makefile 的 GUARD_PACKAGES 里取出已登记的包目录名
registered="$(sed -n 's/^GUARD_PACKAGES[[:space:]]*:*=//p' Makefile \
  | tr -d '\\' | tr ' ' '\n' | sed 's|^\\$||' | grep '^\./internal/' || true)"

# 实际存在的守卫包（目录名以 guard 结尾）
declare -a on_disk=()
while IFS= read -r d; do
  on_disk+=("internal/$(basename "$d")")
done < <(find internal -maxdepth 1 -type d -name '*guard' | sort)

if [[ ${#on_disk[@]} -eq 0 ]]; then
  echo "guards-sync: no *guard packages found under internal/ — is the tree intact?" >&2
  exit 2
fi

missing=0
non_guard_named=0
for pkg in "${on_disk[@]}"; do
  if ! grep -q -- "$pkg" <<<"$registered"; then
    echo "❌ 守卫包未登记进 Makefile GUARD_PACKAGES: $pkg" >&2
    missing=1
  fi
done

# 反方向（R88 补）：已登记的条目必须真的存在于磁盘上。
# 原来只查 on_disk ⊆ registered，不查反向 ⇒ 一条过期/拼错的登记项
# 不会被发现，只会等到 `go test ./internal/<不存在>` 时才以另一种方式炸。
while IFS= read -r entry; do
  [[ -n "$entry" ]] || continue
  dir="$repo_root/${entry#./}"
  if [[ ! -d "$dir" ]]; then
    echo "❌ GUARD_PACKAGES 登记了不存在的目录: ${entry#./}" >&2
    missing=1
  fi
done <<<"$registered"

# 命名一致性（R88 补，**告警不拦**）：登记的包应��以 guard 结尾。
#
# 为什么值得写下来：正向检查靠 `find -name '*guard'` 枚举磁盘，而
# GUARD_PACKAGES 里有 3 个不以 guard 结尾的条目（errdiscard / dbrows /
# jsoncol）——它们**从不出现在正向检查的视野里**。老脚本于是打印
# 「全部 7 个守卫包已登记」而登记表里其实有 10 项：自述与事实不一致。
#
# 本轮不重命名这三个包（纯机械改名会波及 package 名、import 与既有文档，
# 与 P3 不相称）。改为：正向/反向两条检查都覆盖它们（已由上面的循环做到，
# 因为登记表本身已被逐条校验），命名偏差只作告警留痕。
# 残余盲区：将来若新建一个 `internal/fooaudit` 这样的**不叫 guard** 的
# 审计包，本脚本的磁盘枚举看不见它——靠这条告警提醒命名收敛。
while IFS= read -r entry; do
  [[ -n "$entry" ]] || continue
  base="${entry##*/}"
  if [[ "$base" != *guard ]]; then
    echo "⚠️  GUARD_PACKAGES 条目不以 guard 结尾: ${entry#./}" >&2
    non_guard_named=$((non_guard_named + 1))
  fi
done <<<"$registered"

if [[ $missing -ne 0 ]]; then
  echo "" >&2
  echo "   新增审计守卫时必须把它接进执行路径，否则它只是文档、不是门。" >&2
  echo "   请在 Makefile 的 GUARD_PACKAGES 里登记后重跑本脚本。" >&2
  exit 1
fi

if [[ $non_guard_named -gt 0 ]]; then
  echo "   （${non_guard_named} 个登记项不以 guard 结尾，已由反向检查逐条校验存在性）" >&2
fi

registered_count=$(grep -c . <<<"$registered" || true)
echo "✅ ${#on_disk[@]} 个 *guard 目录与 GUARD_PACKAGES 的 ${registered_count} 项双向一致"

# ── 第二跳：CI 不得硬编码守卫包清单（R88 补）────────────────────────
#
# 背景：上面的检查只覆盖「磁盘 → Makefile」。历史上 CI 里另有一份**手抄的**
# 清单（audit-guards-ci.yml 显式列 7 个包），而 Makefile 早已是 10 个——
# metricguard / partguard / routeguard 三者从未在该专用 job 里执行。
# 当时该文件里的注释写着「guards-sync 已校验二者一致」，**那是假的**：
# 本脚本在此之前从不读任何 CI 文件。
#
# 为什么值得单独加一跳：重复清单必然漂移，且**漂移无声**——新守卫本地绿、
# 常规单测门（go test ./...）也绿，只有那个专用 job 悄悄不跑它。
# 换成 `make guards` 后不存在第二份清单可漂移，本检查负责保证它不退回去。
workflow_dir="$repo_root/.github/workflows"
if [[ -d "$workflow_dir" ]]; then
  # 判据必须锚在**真正执行的命令**上，不能锚在「文件里出现过这个字符串」。
  # 变异验证抓到的自证漏洞：本检查原先 grep 裸串 `make guards`，而
  # audit-guards-ci.yml 里恰好有一行注释写着「直接调 `make guards`」——
  # 把 run: 换成别的东西后，注释仍然满足判据 ⇒ 门绿着，专用门已经没了。
  # 因此两处判据都排除注释行（行首非 # 锚点），并要求 run: 是完整一条命令。
  offenders="$(grep -rnE '^[^#]*\./internal/[a-z0-9]+guard\b' "$workflow_dir" || true)"
  if [[ -n "$offenders" ]]; then
    echo "" >&2
    echo "❌ CI workflow 硬编码了守卫包清单（必然与 GUARD_PACKAGES 漂移）：" >&2
    echo "$offenders" | sed 's/^/   /' >&2
    echo "" >&2
    echo "   正确做法：CI 调 \`make guards\`，由 Makefile 的 GUARD_PACKAGES" >&2
    echo "   作为唯一事实源。请勿在 workflow 里再抄一份包列表。" >&2
    exit 1
  fi

  # 专用门必须还在：允许有人删掉整个 job，那等于把守卫从审计路径上摘掉。
  if ! grep -rqsE '^[[:space:]]*run:[[:space:]]*make[[:space:]]+guards[[:space:]]*$' "$workflow_dir"; then
    echo "" >&2
    echo "❌ 没有任何 CI workflow 以 run 步骤执行 \`make guards\`——审计守卫的" >&2
    echo "   专用门已被删除或改写。守卫会静默降级为「只在本地跑的东西」。" >&2
    echo "   （注释里提到 make guards 不算——判据只看 run 步骤。）" >&2
    exit 1
  fi
  echo "✅ CI 未硬编码守卫包清单，且专用门仍在（run: make guards）"
fi

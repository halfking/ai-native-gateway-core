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
for pkg in "${on_disk[@]}"; do
  if ! grep -q -- "$pkg" <<<"$registered"; then
    echo "❌ 守卫包未登记进 Makefile GUARD_PACKAGES: $pkg" >&2
    missing=1
  fi
done

if [[ $missing -ne 0 ]]; then
  echo "" >&2
  echo "   新增审计守卫时必须把它接进执行路径，否则它只是文档、不是门。" >&2
  echo "   请在 Makefile 的 GUARD_PACKAGES 里登记后重跑本脚本。" >&2
  exit 1
fi

registered_count=$(grep -c . <<<"$registered" || true)
echo "✅ 全部 ${#on_disk[@]} 个守卫包已登记（GUARD_PACKAGES 内 ${registered_count} 项）"

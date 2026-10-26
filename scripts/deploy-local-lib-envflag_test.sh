#!/usr/bin/env bash
# R72f 审计钉（2026-10-21）：deploy-local 的 env 白名单必须透传**所有** feature flag。
#
# 背景（实测，不是推断）：
#   移动端页面巡检报出 `/m/reconciliation` 必然 404
#   （`/api/admin/provider-cost-reconciliation?month=2026-10`）。
#   根因链的第三段：`cmd/gateway/provider_profile_init.go:256` 的开关
#   **第一优先**读 env `LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED`；
#   而 `dl_write_env()` 是**白名单式**的 ⇒ 之前白名单里没有这个键
#   ⇒ `.env.local` 里写的值被**静默丢弃** ⇒ 容器 env 里根本没有
#   ⇒ 路由永远不注册 ⇒ 那一席对 super_admin 一直可见、点进去必然失败。
#
#   settings 那条后备路也是死的（该键从未 Register 成 spec ⇒
#   `Global.Spec(key)` 返回 nil ⇒ `GetPlatformBool` 直接回退 fallback=false），
#   ⇒ **env 这条路是唯一可达的一条** ⇒ 白名单缺它 = 功能永久不可开启。
#
#   ⇒⇒ 2026-10-21 属主决策项 #9 拍板「逐个补进白名单」⇒ 本门从
#      「钉住 recon 一条」**升级为「钉住全部 feature flag」**。
#
# 为什么用**行为级**断言（真跑 dl_write_env 看产物）而不是读源码文本：
#   白名单的键名、写法（`dl_emit_env_line` vs 裸 printf）、默认值写法
#   都可能变，而「产物 env 文件里有没有这个键」是契约本身。
#   （源码文本级的断言在 scripts/ursmcheck/deploy_env_flag_passthrough_test.go）
#
# 断言两条（对**每个** `*ENABLED` 白名单条目）：
#   ① 未设 ⇒ 产物里必须是 `KEY=`（**空值**）⇒ gateway 侧视为未设置
#            ⇒ 行为与历史完全一致（★ 零语义风险的来源）
#   ② 设了 ⇒ 产物里必须**原样带上**（这正是「配置进不了容器」那个缺陷的钉子）
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

env_file="$tmpdir/env.out"

# ---- 从 lib 里取出全部 `*ENABLED` 白名单条目（保持与实现同源，不手抄清单）----
# ★ 刻意不用 `mapfile`（bash 4+）⇒ macOS 自带 bash 是 3.2 ⇒ 仓内其它 shell 门
#   也都在 3.2 下跑 ⇒ 这里必须同样兼容。
#
# ★ 断言的是「**产物 == 源码声明的默认**」而不是「一律为空」：
#   ★ `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED` 的声明默认是 `true`（**既有行为**，
#     见 ursmcheck/deploy_env_flag_passthrough_test.go 的 nonEmptyDefaultAllow
#     登记）⇒ ⇒ 「未设 ⇒ 空」对它不成立 ⇒ ⇒ 但「未设 ⇒ 产物等于声明默认」
#     对**所有**条目都成立 ⇒ ⇒ ⇒ 本门据此断言，**不在本门里复制例外清单**
#     （复制两份清单 = 迟早漂移）。
#
# 输出格式：`KEY<TAB>DEFAULT`（DEFAULT 可为空）
ENTRIES=()
while IFS=$'\t' read -r _k _d; do
  [ -n "$_k" ] && ENTRIES+=("$_k"$'\t'"$_d")
done < <(
  grep -oE 'dl_emit_env_line [A-Z0-9_]*ENABLED "\$\{[A-Z0-9_]*:-[^}]*\}"' "$LIB" \
    | sed -E 's/dl_emit_env_line ([A-Z0-9_]*) "\$\{[A-Z0-9_]*:-([^}]*)\}"/\1\t\2/' \
    | sort -u
)
FLAGS=()
for _e in ${ENTRIES[@]+"${ENTRIES[@]}"}; do
  FLAGS+=("${_e%%$'\t'*}")
done

# ★ 防呆：扫描口径失效 ⇒ 清单会是空的 ⇒ 看起来一切正常 ⇒ 显式失败。
MIN_FLAGS=26
if [ "${#FLAGS[@]}" -lt "$MIN_FLAGS" ]; then
  echo "FAIL: 只从 ${LIB} 里解析出 ${#FLAGS[@]} 个 *ENABLED 白名单条目（期望 ≥ ${MIN_FLAGS}）" >&2
  echo "      ⇒ 扫描口径可能失效（dl_emit_env_line 的写法变了）⇒ 请更新本门" >&2
  exit 1
fi

run_write_env() {
  # 用法：run_write_env [KEY=VALUE ...]
  if [ "$#" -gt 0 ]; then
    env "$@" bash -c '
      set -euo pipefail
      source "'"$LIB"'"
      dl_write_env "'"$env_file"'" 8782
    ' >/dev/null
  else
    bash -c '
      set -euo pipefail
      source "'"$LIB"'"
      dl_write_env "'"$env_file"'" 8782
    ' >/dev/null
  fi
}

# ---- ① 未设 ⇒ 每个 flag 的产物值必须**等于源码声明的默认** ----
run_write_env
for ENTRY in ${ENTRIES[@]+"${ENTRIES[@]}"}; do
  KEY="${ENTRY%%$'\t'*}"
  DEFAULT="${ENTRY#*$'\t'}"
  if ! grep -qx "$KEY=$DEFAULT" "$env_file"; then
    echo "FAIL: 未设时产物 env 里没有 '$KEY=$DEFAULT' 这一行 ⇒ 期望「键存在且值 == 源码声明的默认」" >&2
    echo "      （匹配到的行：$(grep -n "^$KEY" "$env_file" || echo '<无>')）" >&2
    exit 1
  fi
done

# ---- ② 设了 ⇒ 每个 flag 都必须原样带上 ----
# ⚠ 变量名必须就是 lib 里读的那个键名 —— 传错名字会让**门自己**坏掉
#   （第一版写成了 KEY_VALUE ⇒ 门红了 ⇒ 但那是量具故障，不是被测对象坏）。
for KEY in "${FLAGS[@]}"; do
  rm -f "$env_file"
  run_write_env "$KEY=__kx_probe__"
  if ! grep -qx "$KEY=__kx_probe__" "$env_file"; then
    echo "FAIL: 设了 $KEY=__kx_probe__ 但产物 env 里没有这一行 ⇒ ★ 配置仍进不了容器" >&2
    echo "      （匹配到的行：$(grep -n "^$KEY" "$env_file" || echo '<无>')）" >&2
    exit 1
  fi
done

echo "PASS: ${#FLAGS[@]} 个 feature flag 全部在 deploy-local 的 env 白名单里（未设→声明的默认 / 设了→原样透传）"

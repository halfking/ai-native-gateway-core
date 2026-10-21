#!/usr/bin/env bash
# R72f 审计钉（2026-10-21）：deploy-local 的 env 白名单必须透传费用对账子开关。
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
# 为什么用**行为级**断言（真跑 dl_write_env 看产物）而不是读源码文本：
#   白名单的键名、写法（`dl_emit_env_line` vs 裸 printf）、默认值写法
#   都可能变，而「产物 env 文件里有没有这个键」是契约本身。
#   （源码文本级的断言在 scripts/ursmcheck/deploy_env_flag_passthrough_test.go）
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

env_file="$tmpdir/env.out"
KEY="LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED"

# 未设 ⇒ 产物里必须是**空值**（gateway 侧视为未设置 ⇒ 行为与历史完全一致）。
bash -c '
  set -euo pipefail
  source "'"$LIB"'"
  dl_write_env "'"$env_file"'" 8782
' >/dev/null

if ! grep -qx "$KEY=" "$env_file"; then
  echo "FAIL: 未设时产物 env 里没有 '$KEY=' 这一行 ⇒ 期望「键存在且值为空」" >&2
  echo "      （实测：$(grep -c "^$KEY" "$env_file" || true) 行匹配）」" >&2
  exit 1
fi

# 设了 ⇒ 产物里必须**原样带上**（这正是「配置进不了容器」那个缺陷的钉子）。
# ⚠ 变量名必须就是 lib 里读的那个键名 —— 传错名字会让**门自己**坏掉
#   （第一版写成了 KEY_VALUE ⇒ 门红了 ⇒ 但那是量具故障，不是被测对象坏）。
rm -f "$env_file"
env "$KEY=true" bash -c '
  set -euo pipefail
  source "'"$LIB"'"
  dl_write_env "'"$env_file"'" 8782
' >/dev/null

if ! grep -qx "$KEY=true" "$env_file"; then
  echo "FAIL: 设了 $KEY=true 但产物 env 里没有这一行 ⇒ ★ 配置仍进不了容器" >&2
  echo "      （匹配到的行：$(grep -n "^$KEY" "$env_file" || echo '<无>')）」" >&2
  exit 1
fi

echo "PASS: $KEY 已在 deploy-local 的 env 白名单里（未设→空行 / 设了→原样透传）"

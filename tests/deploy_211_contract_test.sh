#!/usr/bin/env bash
# =============================================================================
# deploy_211_contract_test.sh — scripts/deploy-211-docker.sh 的离线契约门
#
# 纯 bash，不碰 docker、不碰 192.168.31.211：把被测脚本里几个纯函数抽出来
# 单独驱动，钉住 211 部署脚本真正容易回归的契约。
#
# 为什么要有这个门（判据纪律）：
#   · 「Redis 库号从 15 往下倒排」是用户明确要求、且方向性极强的行为。
#     写成"随便找个空库"测试也会绿——那是假绿。这里用注入的占用集合
#     精确驱动 allocate_redis_db 的分配结果。
#   · 复用 vs 新建的判定必须同时钉住「版本是 17」和「有 columnar」两个维度，
#     少一个都会让脚本去抢别人的 PG16/PG18 实例。
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT/scripts/deploy-211-docker.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL %s\n' "$1"; }
check(){ if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (期望=$2 实得=$3)"; fi; }

[[ -f "$SCRIPT" ]] || { echo "找不到 $SCRIPT"; exit 1; }

# ── 从被测脚本里抽出纯逻辑片段执行（保证测的是真代码，不是复制品）──────
# extract <起始正则> —— 把 allocate_redis_db 的函数体抠出来单独跑
extract_fn() {
  local fn="$1" start first
  # 函数名与 { 之间允许列对齐用的多个空格（pg_is_17()      { ... }）
  start="$(grep -nE "^${fn}\(\)[[:space:]]*\{" "$SCRIPT" | head -1 | cut -d: -f1)"
  [[ -n "$start" ]] || return 1
  first="$(sed -n "${start}p" "$SCRIPT")"
  # 单行函数（pg_is_17 这类 `{ ...; }` 收在同一行）走短路
  if [[ "$first" == *"}"* && "$first" != *"then"* ]]; then
    printf '%s\n' "$first"; return 0
  fi
  sed -n "${start},/^}/p" "$SCRIPT"
}

if ! extract_fn allocate_redis_db > "$TMP/alloc.sh"; then
  echo "抽不出 allocate_redis_db（脚本结构变了？）"; exit 1
fi
if ! extract_fn pg_is_17 > "$TMP/pg17.sh"; then
  echo "抽不出 pg_is_17"; exit 1
fi

echo "== Redis 库号倒排分配 =="
run_alloc() {
  # $1 = 占用集合（空格分隔的库号），$2 = 预置的 claim 库号（可空）
  local occupied="$1"; shift
  local claim="$TMP/claim"
  : > "$claim"
  if [[ -n "${1:-}" ]]; then printf '%s\n' "$1" > "$claim"; fi
  # 注入 redis_db_in_use 的替身。
  # 注意签名：被测代码以 redis_db_in_use "$container" "$idx" 调用，
  # 库号是 $2 —— 早先写成 $1 时替身恒判"未占用"，断言全被架空。
  (
    set -uo pipefail
    CLAIM_FILE="$claim"
    D211_REDIS_DB_START=15
    D211_REDIS_DB_END=0
    redis_db_in_use() { [[ " $occupied " == *" $2 "* ]]; }
    die() { printf 'DIED:%s\n' "$*"; exit 9; }
    log() { :; }; warn() { :; }
    source "$TMP/alloc.sh"
    allocate_redis_db "c"
  ) 2>/dev/null
}

# 1) 全部空闲 → 必须拿最高的 db15（这正是"从 15 往前倒排"的核心断言）
check "全空闲时优先 db15（而非 db0）" "15" "$(run_alloc '')"
# 2) db15 被占 → 退到 14
check "db15 被占时退到 db14" "14" "$(run_alloc '15')"
# 3) db15/14 被占 → db13
check "db15,14 被占时取 db13" "13" "$(run_alloc '15 14')"
# 4) 大部分库被占 → 仍然向下取剩余的最低空闲
check "高位库全占时向下取 db1" "1" "$(run_alloc '2 3 4 5 6 7 8 9 10 11 12 13 14 15')"
# 5) 全占 → 必须报错放弃，绝不抢占别人已用的库
out="$(run_alloc '0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15')"
if [[ "$out" == DIED:* ]]; then ok "库号全被占时报错放弃（不抢占）"
else bad "库号全被占时应报错，实得=$out"; fi
# 6) claim 持久化：已分配 db12 且仍空闲 → 重跑必须复用 12，不能漂到 15。
#    关键：占用集合要**全空**。若把高位库标成占用，"复用 12"与"从 15 降到 12"
#    会得到同一个数字，断言就成了假绿（早先踩过：删掉复用逻辑测试仍全绿）。
#    全空时，删掉复用 → 结果是 15；保留复用 → 结果是 12。两者可区分。
check "claim 命中且空闲时复用原库号(不漂移)" "12" "$(run_alloc '' 12)"
# 7) claim 指向的库已被别人占用 → 重新分配（不能死守已污染的库）
check "claim 库被占后向下重新分配" "11" "$(run_alloc '12 13 14 15' 12)"
# 8) claim 文件被选中结果回写 → 重跑必然稳定（幂等性的物理保证）
: > "$TMP/claim2"
(
  set -uo pipefail
  CLAIM_FILE="$TMP/claim2"; D211_REDIS_DB_START=15; D211_REDIS_DB_END=0
  occupied='15 14'                      # 高位两个库已被别的服务占用
  redis_db_in_use() { [[ " $occupied " == *" $2 "* ]]; }
  log(){ :; }; warn(){ :; }; die(){ printf 'DIED\n'; exit 9; }
  source "$TMP/alloc.sh"
  allocate_redis_db "c" >/dev/null
) 2>/dev/null
check "分配结果写回 claim 文件" "13" "$(cat "$TMP/claim2")"

echo "== PG 17 版本判定 =="
run_pg17() { ( set -uo pipefail; source "$TMP/pg17.sh"; pg_is_17 "$1" && echo yes || echo no ); }
check "17.5 (170005) 判为 PG17" "yes" "$(run_pg17 170005)"
check "18.1 (180001) 不判为 PG17" "no"  "$(run_pg17 180001)"
check "16.4 (160004) 不判为 PG17" "no"  "$(run_pg17 160004)"

echo "== 脚本静态契约 =="
# $var 紧跟非 ASCII 字符（典型是中文全角括号）在 UTF-8 locale 下会被 bash
# 并入变量名 → "unbound variable"。真实踩过两次：$idx（ 与 $envf（。
# 这条门把两个 211 脚本一起扫掉。
for f in "$SCRIPT" "$ROOT/scripts/sync-gateway-db-to-211.sh"; do
  bad_n="$(python3 - "$f" <<'PY'
import re, io, sys
s = io.open(sys.argv[1], encoding="utf-8").read()
print(len(re.findall(r'\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7F]', s)))
PY
)"
  check "$(basename "$f") 无「\$var 紧跟全角标点」隐患" "0" "$bad_n"
done
# 复用判定必须同时含版本与 columnar 两维，否则会去抢别人的 PG16 实例
if grep -q 'pg_is_17 "\$ver" && pg_has_columnar "\$am"' "$SCRIPT"; then
  ok "PG 复用同时校验 PG17 与 columnar"
else
  bad "PG 复用判定缺少版本/columnar 双条件"
fi
# Redis cluster 模式必须硬降级到 db0（cluster 不支持非 0 库）
if grep -q 'redis_cluster_enabled' "$SCRIPT" && grep -q 'REDIS_DB=0' "$SCRIPT"; then
  ok "Redis cluster 模式降级 db0 有实现"
else
  bad "Redis cluster 降级逻辑缺失"
fi
# 密钥文件不得进仓库
if grep -q 'chmod 600 "\$ENV_FILE"' "$SCRIPT"; then
  ok "网关 env 文件收紧为 600"
else
  bad "网关 env 文件未 chmod 600"
fi

echo
printf '结果: %d 通过, %d 失败\n' "$pass" "$fail"
(( fail == 0 )) || exit 1
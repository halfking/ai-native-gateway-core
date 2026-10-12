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
#
# 这道门是纵深防御，核心断言（库号分配逻辑）不依赖 python3。所以 python3 缺席
# 时**跳过并显式说明**，绝不让整个 verify.sh（set -e）因缺解释器而红。
if command -v python3 >/dev/null 2>&1; then
  for f in "$SCRIPT" "$ROOT/scripts/sync-gateway-db-to-211.sh"; do
    bad_n="$(python3 - "$f" <<'PY'
import re, io, sys
s = io.open(sys.argv[1], encoding="utf-8").read()
print(len(re.findall(r'\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7F]', s)))
PY
)"
    check "$(basename "$f") 无「\$var 紧跟全角标点」隐患" "0" "$bad_n"
  done
else
  printf '  SKIP 全角标点隐患扫描（本机无 python3；核心断言不受影响）\n'
fi
# 复用判定必须同时含版本、columnar、vector 三维，否则会去抢别人的 PG16 实例
# 历史上 pg_am 口径把 acc-pg17（Citus 系）误判为「没 columnar」→ 走了新建路径
# → codeload tarball 无 git submodule → 整轮部署废掉。改查 pg_available_extensions
# 是关键修复，必须钉死。
if grep -q 'pg_is_17' "$SCRIPT" \
   && grep -q 'pg_has_columnar' "$SCRIPT" \
   && grep -q 'pg_has_vector' "$SCRIPT" \
   && grep -q "pg_available_extensions" "$SCRIPT" \
   && grep -q "'citus_columnar'" "$SCRIPT" \
   && grep -q "'pgcolumnar'" "$SCRIPT"; then
  ok "PG 复用判定认 columnar 三种发行 + vector（查 pg_available_extensions）"
else
  bad "PG 复用判定缺少 columnar 三种发行 / vector / pg_available_extensions 任一"
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

# docker --env-file 不剥 `export` 前缀：一行 `export FOO=bar` 会被当成 key 叫
# "export FOO"，全部设不进去 → 网关拿不到 DATABASE_URL 直接起不来。
# 且它不认 %q 的 shell 转义。实测踩过，所以钉死：容器必须走 -e argv。
if grep -v '^[[:space:]]*#' "$SCRIPT" | grep -q -- '--env-file'; then
  bad "用了 docker --env-file：export 前缀+%q 转义都会让网关拿不到配置"
else
  ok "网关容器用 -e 注入 env（不走 docker --env-file）"
fi
if grep -q 'GW_ENV=(' "$SCRIPT" && grep -q '"\${GW_ENV\[@\]}"' "$SCRIPT"; then
  ok "GW_ENV 数组确实传给了 docker run"
else
  bad "GW_ENV 数组未传给 docker run"
fi
# 复用别人启了 requirepass 的 redis（如 nbjl-redis）时，密码得能从启动命令抽出来
# 否则 allocate_redis_db 的 DBSIZE 探测全 NOAUTH，库号倒排跑不起来
if grep -q -- '--requirepass' "$SCRIPT" && grep -q 'redis_extract_password_from_cmd' "$SCRIPT"; then
  ok "Redis 密码可从容器启动命令回抽（覆盖 nbjl-redis 这类启了 requirepass 的实例）"
else
  bad "Redis 抽密码逻辑缺失"
fi
# pgcolumnar 源码构建必须用 git clone --recurse-submodules（codeload tarball 不含 objstore 子模块）
# —— 2026-10-13 真机部署第一轮就死在这条：objstore/ 空壳 → No rule to make target 'all'
if grep -q 'git clone --recurse-submodules' "$SCRIPT" \
   && grep -q 'pkg-config' "$SCRIPT"; then
  ok "pgcolumnar 走 git clone --recurse-submodules + 装 pkg-config"
else
  bad "pgcolumnar 构建路径没用 git submodule / 没装 pkg-config"
fi
# DSN 密码必须 URL-encode（PG 系 acc-pg17 密码含 base64 数字但难保；其他容器的密码里出
# @/: 会直接拆断 URL；写死明文拼字符串是历史坑）
if grep -q 'url_encode' "$SCRIPT" && grep -q 'urllib.parse.quote' "$SCRIPT"; then
  ok "DB/Redis DSN 密码 URL-encode（防 @ / : / / 拆断）"
else
  bad "DSN 密码未 URL-encode"
fi
# --probe-only 模式：探测期能立刻验证复用口径，且不触发分钟级构建
if grep -q -- '--probe-only' "$SCRIPT" && grep -q 'MODE=probe-only' "$SCRIPT"; then
  ok "提供 --probe-only：探测期快速验证复用口径，跳过构建"
else
  bad "缺 --probe-only 模式"
fi
# find_redis_container 必须把容器名 + 密码一并返回（用 \x1f 分隔），
# 否则调用方拿到 REDIS_FOUND 但拿不到密码 → DSN 拼不出 auth 段。
# 这条是 2026-10-13 真机第二次翻车的根因：函数内改 REDIS_PASSWORD 全局变量
# 在子 shell 跑命令替换时不生效。用 literal grep 避免正则引号层叠。
if grep -qF "printf 'PONG\\x1f%s\\n' \"\$pw\"" "$SCRIPT" \
   && grep -qF "printf '%s\\n%s\\n' \"\$name\" \"\$rpw\"" "$SCRIPT" \
   && grep -qF 'REDIS_FOUND="$(printf' "$SCRIPT" \
   && grep -qF 'REDIS_PASSWORD="$(printf' "$SCRIPT"; then
  ok "find_redis_container 通过 stdout 协议回传密码（避开子 shell 全局赋值失效）"
else
  bad "find_redis_container 未用 stdout 协议回传密码"
fi
# redis-cli -a 在密码模式下会先打一行 Warning 干扰 PONG 判定，必须取末行
if grep -q 'tail -n 1' "$SCRIPT" && grep -q 'redis_try_ping' "$SCRIPT"; then
  ok "redis PING 判定走末行（容忍 -a Warning 前缀）"
else
  bad "redis PING 未做 tail 防御 Warning 前缀"
fi
# 探测时 read docker ps --format 'NAME|IMAGE' 必须显式 IFS='|'：
# 默认 IFS 是 space+tab+newline，read 不会在 | 切分 → img 永远空 → 关键字匹配
# 只看 name 走不到 *citus*/*pg17*/*redis* → 走「新建」分支。2026-10-13 真机翻车证据。
if grep -qE "while IFS='\|' read -r name img" "$SCRIPT"; then
  ok "find_*_container 按 | 切分 docker ps 输出（IFS=|）"
else
  bad "find_*_container 未显式 IFS='|' 切分 docker ps 输出"
fi
# 211 接收端 tar 必须带 --no-xattrs / --no-mac-metadata：macOS BSD tar 写 archive
# 时把 xattr（LIBARCHIVE.xattr.com.apple.provenance 警告）也写进去，Linux GNU tar
# 默认会按 xattr 物化 `._foo` AppleDouble 文件——这正是 2026-10-13 真机第三轮翻车
# 的根因：发送端 exclude 了 `._*` 路径，接收端 GNU tar 仍按 xattr 重新物化，
# 工作树里冒出 23472 个 `._*`，COPY 进镜像 → i18n panic。
if grep -q -- '--no-xattrs' "$SCRIPT"; then
  ok "211 接收 tar 关闭 xattr（GNU tar 1.35 不支持 --no-mac-metadata，靠 find 兜底）"
else
  bad "211 接收 tar 未关闭 xattr"
fi
# sync 脚本的 --reset 必须把 DROP 与 CREATE 拆成两条独立 -c：
# 同一条 -c 多个分号会被 psql 包到隐式事务，触发
# "DROP DATABASE cannot run inside a transaction block"。2026-10-13 真机首跑即因此故。
SYNC_SCRIPT="$ROOT/scripts/sync-gateway-db-to-211.sh"
if [[ -f "$SYNC_SCRIPT" ]]; then
  drop_line="$(grep -n 'DROP DATABASE IF EXISTS' "$SYNC_SCRIPT" | head -1 | cut -d: -f1)"
  create_line="$(grep -n 'CREATE DATABASE' "$SYNC_SCRIPT" | head -1 | cut -d: -f1)"
  if [[ -n "$drop_line" && -n "$create_line" && "$drop_line" != "$create_line" ]]; then
    ok "sync --reset 拆 DROP 与 CREATE 为独立 -c（避开事务块）"
  else
    bad "sync --reset DROP 与 CREATE 同处一行（触发 transaction block 报错）"
  fi
  if grep -q '^warn()' "$SYNC_SCRIPT"; then
    ok "sync 脚本定义 warn() 函数（预建扩展失败时不再 command not found 退出）"
  else
    bad "sync 脚本缺 warn() 定义"
  fi
else
  bad "找不到 $SYNC_SCRIPT"
fi
# Dockerfile 侧必须再兜底一次清 ._*/.DS_Store：万一 tar 同步漏了，构建也不会带进去
if grep -q "name '._\\*' -o -name '.DS_Store'" "$ROOT/Dockerfile"; then
  ok "Dockerfile 复制源码前清理 macOS 元数据（第二道防线）"
else
  bad "Dockerfile 未在 COPY . 之前清理 macOS 元数据"
fi

echo
printf '结果: %d 通过, %d 失败\n' "$pass" "$fail"
(( fail == 0 )) || exit 1
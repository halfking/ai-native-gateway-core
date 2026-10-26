#!/usr/bin/env bash
# =============================================================================
# deploy_211_contract_test.sh — scripts/deploy-seamless-docker.sh 的离线契约门
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
# 被测引擎路径可被环境变量覆盖 —— **只给变异验证用**（把改坏的副本喂给门，
# 证明门确实在测东西而不是恒绿）。日常运行不设该变量，指向仓库里的真引擎。
SCRIPT="${DEPLOY_211_ENGINE:-$ROOT/scripts/deploy-seamless-docker.sh}"
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
  # $1 = 占用集合（空格分隔的库号），$2 = 预置的 claim 库号（可空），
  # $3 = 「自占」集合（库里有键且键是网关前缀，可空）
  local occupied="$1"; shift
  local claim="$TMP/claim"
  : > "$claim"
  local claim_db=""
  if [[ -n "${1:-}" ]]; then claim_db="$1"; shift; printf '%s\n' "$claim_db" > "$claim"; fi
  local self_occ="${1:-}"
  # 注入 redis_db_in_use 的替身。
  # 注意签名：被测代码以 redis_db_in_use "$container" "$idx" 调用，
  # 库号是 $2 —— 早先写成 $1 时替身恒判"未占用"，断言全被架空。
  (
    set -uo pipefail
    CLAIM_FILE="$claim"
    DD_REDIS_DB_START=15
    DD_REDIS_DB_END=0
    redis_db_in_use() { [[ " $occupied " == *" $2 "* ]]; }
    redis_db_has_gateway_keys() { [[ " $self_occ " == *" $2 "* ]]; }
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
  CLAIM_FILE="$TMP/claim2"; DD_REDIS_DB_START=15; DD_REDIS_DB_END=0
  occupied='15 14'                      # 高位两个库已被别的服务占用
  redis_db_in_use() { [[ " $occupied " == *" $2 "* ]]; }
  log(){ :; }; warn(){ :; }; die(){ printf 'DIED\n'; exit 9; }
  source "$TMP/alloc.sh"
  allocate_redis_db "c" >/dev/null
) 2>/dev/null
check "分配结果写回 claim 文件" "13" "$(cat "$TMP/claim2")"
# 9) 自占复用：claim 库非空，但采样键全是网关前缀（llmgw:/session:/ursm:/
#    requestjourney:）→ 是网关自己上一轮部署的持久化残留，必须复用而不是漂移。
#    2026-10-14 实测根因：nbjl-redis 开 AOF，容器重建后 db6 留 8138 个自占键，
#    旧逻辑只看 DBSIZE>0 → 每次部署下移一号（15→…→5）。
#    判据形状钉桩：只断言「不复用 6」测不出漂移方向，必须 count 精确等于 6。
check "claim 库全为网关自占键 → 复用不漂移" "6" "$(run_alloc '6' 6 '6')"
# 10) 外来键：claim 库非空且非网关键（自占集合不含它）→ 放弃 claim 重新分配
check "claim 库为外来键占用 → 放弃 claim 重新分配" "11" "$(run_alloc '12 13 14 15' 12 '')"

echo "== URSM 库号复用（gateway.env 持久化，不逐次下移） =="
sed -n '/^URSM_REDIS_DB="\${URSM_V2_REDIS_DB:-}"$/,/^\[\[ -n "\$URSM_REDIS_DB" \]\] || die/p' \
  "$SCRIPT" > "$TMP/ursm.sh"
[[ -s "$TMP/ursm.sh" ]] && ok "截取到 URSM 分配段" || bad "截取不到 URSM 分配段（锚点行被改？）"
run_ursm() {
  # $1=占用集合 $2=env 里上次的 URSM 库号(可空) $3=自占集合 $4=会话库号
  local occupied="$1" prev="$2" self_occ="$3"
  (
    set -uo pipefail
    unset URSM_V2_REDIS_DB
    ENV_FILE="$TMP/ursm.env"; : > "$ENV_FILE"
    [[ -n "$prev" ]] && printf 'export URSM_V2_REDIS_DB=%s\n' "$prev" > "$ENV_FILE"
    REDIS_DB="$4"; REDIS_CONTAINER=stub
    redis_db_in_use() { [[ " $occupied " == *" $2 "* ]]; }
    redis_db_has_gateway_keys() { [[ " $self_occ " == *" $2 "* ]]; }
    die() { printf 'DIED\n'; exit 9; }
    log() { :; }
    source "$TMP/ursm.sh"
    printf '%s\n' "$URSM_REDIS_DB"
  )
}
# 211 实测形态：0～15 全有键，上次 URSM=0 且库内只有 ursm:* → 必须复用 0
check "URSM 上次库号只含网关键 → 复用(全库被占也不 die)" "0" \
  "$(run_ursm '0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15' 0 '0' 5)"
check "URSM 上次库号被外来键占用 → 放弃并找空库" "13" \
  "$(run_ursm '0 14' 0 '' 5)"
check "URSM 上次库号与会话库撞号 → 不复用" "14" \
  "$(run_ursm '' 5 '5' 5)"
check "无持久化记录 → 默认优先 db14" "14" "$(run_ursm '' '' '' 5)"

echo "== production 模式鉴权密钥（生成 / 持久复用 / 环境变量优先）=="
if extract_fn dd_auth_secret > "$TMP/authsec.sh"; then
  run_sec() {
    # $1=变量名 $2=持久文件名 $3=前缀 $4=调用方环境变量值(可空)
    (
      set -uo pipefail
      DD_STATE_DIR="$TMP/state"; mkdir -p "$DD_STATE_DIR/env"
      unset "$1"; [[ -n "$4" ]] && export "$1=$4"
      source "$TMP/authsec.sh"
      dd_auth_secret "$1" "$2" "$3"
    )
  }
  k1="$(run_sec LLM_GATEWAY_ADMIN_API_KEY t.adminkey ops_ '')"
  [[ "$k1" =~ ^ops_[0-9a-f]{64}$ ]] && ok "首次生成带前缀的 256 位随机值（ops_+64hex）" \
    || bad "首次生成形态不对：$k1"
  k2="$(run_sec LLM_GATEWAY_ADMIN_API_KEY t.adminkey ops_ '')"
  check "重跑复用持久值，不轮换" "$k1" "$k2"
  check "持久文件内容 = NAME=值" "LLM_GATEWAY_ADMIN_API_KEY=$k1" "$(cat "$TMP/state/env/t.adminkey")"
  check "持久文件权限 600" "600" "$(stat -f %Lp "$TMP/state/env/t.adminkey" 2>/dev/null || stat -c %a "$TMP/state/env/t.adminkey")"
  check "调用方环境变量优先并回写持久文件" "ops_from_env" \
    "$(run_sec LLM_GATEWAY_ADMIN_API_KEY t.adminkey ops_ ops_from_env)"
  check "回写后持久文件跟随环境变量" "LLM_GATEWAY_ADMIN_API_KEY=ops_from_env" "$(cat "$TMP/state/env/t.adminkey")"
  h="$(run_sec CURSOR_HMAC_SECRET t.hmac '' '')"
  (( ${#h} >= 32 )) && ok "CURSOR_HMAC_SECRET 长度 ${#h} ≥ 32（ValidateAuthSecrets 门）" \
    || bad "CURSOR_HMAC_SECRET 长度 ${#h} < 32，production 会拒启"
else
  bad "抽不出 dd_auth_secret（production 模式密钥段被删或改名）"
fi
api_prefix="$(sed -nE 's/^GW_STATIC_API_KEY="\$\(dd_auth_secret LLM_GATEWAY_API_KEY gateway\.apikey ([^)]*)\)"$/\1/p' "$SCRIPT")"
if [[ -n "$api_prefix" && "$api_prefix" != sk-* ]]; then
  ok "静态 API key 前缀 '$api_prefix' 不是 sk-（sk-* 会被委托库校验，静态比对失效）"
else
  bad "静态 API key 前缀缺失或为 sk-*：'$api_prefix'"
fi
for want in 'LLM_GATEWAY_ENV=production' 'LLM_GATEWAY_LICENSE_ACTIVATED=1' \
            'LLM_GATEWAY_API_KEY=${GW_STATIC_API_KEY}' \
            'LLM_GATEWAY_ADMIN_API_KEY=${GW_ADMIN_API_KEY}' 'CURSOR_HMAC_SECRET=${GW_CURSOR_HMAC}'; do
  if grep -qF -- "-e \"$want\"" "$SCRIPT"; then ok "容器 -e 注入 $want"; else bad "容器缺 -e $want"; fi
done
if grep -qF "export LLM_GATEWAY_LICENSE_ACTIVATED=1" "$SCRIPT"; then
  ok "gateway.env 持久化 LLM_GATEWAY_LICENSE_ACTIVATED=1（防 SPA 卡 /bootstrap）"
else
  bad "gateway.env 未写 LLM_GATEWAY_LICENSE_ACTIVATED=1"
fi
env_line="$(grep -n "printf 'export LLM_GATEWAY_ENV=production" "$SCRIPT" | cut -d: -f1)"
gen_line="$(grep -n '^GW_CURSOR_HMAC=' "$SCRIPT" | cut -d: -f1)"
if [[ -n "$env_line" && -n "$gen_line" && "$gen_line" -lt "$env_line" ]]; then
  ok "production 开关写入前三项密钥已就绪（${gen_line} < ${env_line}）"
else
  bad "LLM_GATEWAY_ENV=production 未持久化或早于密钥生成"
fi

echo "== PG 17 版本判定 =="
run_pg17() { ( set -uo pipefail; source "$TMP/pg17.sh"; pg_is_17 "$1" && echo yes || echo no ); }
check "17.5 (170005) 判为 PG17" "yes" "$(run_pg17 170005)"
check "18.1 (180001) 不判为 PG17" "no"  "$(run_pg17 180001)"
check "16.4 (160004) 不判为 PG17" "no"  "$(run_pg17 160004)"

echo "== 选谁当网关 PG：按「谁持有网关库」而不是按 docker ps 顺序（2026-10-18）=="
# 背景：211 上 acc-pg17 与 redclaw-local-stack-redclaw-casdoor-db-1 用**同一张
# kx-citus-pg17:amd64 镜像**，同时满足 pg17+columnar+vector 三维能力判据。旧实现
# 「第一个合格者即返回」，而 docker ps 顺序不含语义（实测选中了只活了 38 分钟的
# casdoor PG）→ 随重启换症状。真正的判据是「这只容器里有没有 llm_gateway」。
if extract_fn pg_candidate_score > "$TMP/pgscore.sh"; then
  ok "抽到 pg_candidate_score（纯打分，无 IO）"
else
  bad "抽不出 pg_candidate_score（脚本结构变了？）"; echo "  FAIL 无法继续 PG 选择用例"; exit 1
fi
if extract_fn pg_pick_from_candidates > "$TMP/pgpick.sh"; then
  ok "抽到 pg_pick_from_candidates（纯选择，无 IO）"
else
  bad "抽不出 pg_pick_from_candidates"; exit 1
fi
run_score() { ( set -uo pipefail; source "$TMP/pgscore.sh"; pg_candidate_score "$@" ); }
run_pick() {
  # $1=候选表（每行 "<name> <explicit> <persisted> <has_db> <has_objs> <managed>"）
  ( set -uo pipefail; log(){ :; }; source "$TMP/pgpick.sh"; source "$TMP/pgscore.sh"
    pg_pick_from_candidates "$1" ) 2>/dev/null
}
# 打分表（值取自真机形态，不是随手编的）
check "显式指定优先(1000)"        "1000" "$(run_score 1 0 0 0 0 9)"
check "上次绑定且持有网关库(900)"  "900"  "$(run_score 0 1 1 1 0 9)"
check "持有网关库+网关对象(800)"   "800"  "$(run_score 0 0 1 1 0 9)"
check "仅持有网关库(700)"          "700"  "$(run_score 0 0 1 0 0 9)"
check "脚本自建实例(600)"          "600"  "$(run_score 0 0 0 0 1 9)"
check "全场唯一候选(500)"          "500"  "$(run_score 0 0 0 0 0 1)"
check "上次绑定但库里没有网关库→降到200" "200" "$(run_score 0 1 0 0 0 9)"
check "多候选且谁都无网关库→拒绝复用(-1)" "-1" "$(run_score 0 0 0 0 0 2)"

# ★ 真机复现：docker ps 把 redclaw 的 casdoor PG 列在 acc-pg17 之前（实测即如此）
CASDOOR_FIRST=$'redclaw-local-stack-redclaw-casdoor-db-1 0 0 0 0 0\nacc-pg17 0 0 1 4 0'
ACC_FIRST=$'acc-pg17 0 0 1 4 0\nredclaw-local-stack-redclaw-casdoor-db-1 0 0 0 0 0'
check "真机形态（casdoor 在前）→ 必须选持有网关库的 acc-pg17" "acc-pg17" "$(run_pick "$CASDOOR_FIRST")"
check "同一候选集换顺序 → 结果不变（去掉 docker ps 顺序这个随机量）" "acc-pg17" "$(run_pick "$ACC_FIRST")"
# 变异自检：把「谁持有网关库」对调（casdoor 持有、acc-pg17 不持有）→ 必须改选
# casdoor。这一条证明选择**跟着数据走**，而不是跟着行序或容器名偏好走；
# 若实现里残留「按名字挑」「取第一行」，它会立刻打红。
REVERSED=$'redclaw-local-stack-redclaw-casdoor-db-1 0 0 1 1 0\nacc-pg17 0 0 0 0 0'
check "变异自检：持库者对调后跟着改选（证明判据读的是数据）" \
  "redclaw-local-stack-redclaw-casdoor-db-1" "$(run_pick "$REVERSED")"
# 不自洽的夹具（has_db=0 却 has_objs>0）必须一律被拒，不能因为「有网关对象」而选中：
# 网关对象只有在库里才谈得上，这个组合只可能是采集读错了列。
if [[ -z "$(run_pick $'a-pg 0 0 0 9 0\nb-pg 0 0 0 0 0')" ]]; then
  ok "自相矛盾的候选（有对象却无库）不被选中"
else
  bad "自相矛盾的候选被选中：网关对象只在库里存在，has_objs 必蕴含 has_db"
fi
# 上次部署被污染（gateway.env 指向 casdoor）时，也不能把网关再绑到 casdoor 上
POLLUTED=$'redclaw-local-stack-redclaw-casdoor-db-1 0 1 0 0 0\nacc-pg17 0 0 1 4 0'
check "上次绑定被污染（无网关库）→ 仍选持有网关库的那只" "acc-pg17" "$(run_pick "$POLLUTED")"
# 歧义即新建：两只都合格、谁也没有网关库 → 不赌，直接返回非 0 让上层新建
if [[ -z "$(run_pick $'b-pg 0 0 0 0 0\na-pg 0 0 0 0 0')" ]]; then
  ok "多候选且均无网关库 → 拒绝复用（上层转新建，不猜）"
else
  bad "多候选均无网关库时仍复用了其中一只（等于回到按顺序猜）"
fi
# 唯一候选：不存在「选错谁」的可能，允许复用并建库
check "全场唯一候选且无网关库 → 仍复用（保持既有行为）" "only-pg" \
  "$(run_pick $'only-pg 0 0 0 0 0')"
# 显式指定压过一切
check "显式指定压过「持有网关库」的其它容器" "kx-pg17-columnar" \
  "$(run_pick $'acc-pg17 0 0 1 4 0\nkx-pg17-columnar 1 0 0 0 1 2')"

echo "== 网关库检测（本轮新增的独立维度）=="
# 只查「能力」不查「库」，就分不出 acc-pg17 与 casdoor 的 PG —— 这正是本轮根因。
if grep -qF "pg_gateway_db_state" "$SCRIPT" \
   && grep -qF "FROM pg_database WHERE datname='\${DD_PG_DB}'" "$SCRIPT" \
   && grep -qF "gateway_db_revision_sequences" "$SCRIPT"; then
  ok "网关库检测查 pg_database + 网关特征表（独立于 pg17/columnar/vector 能力判据）"
else
  bad "网关库检测缺失：无法区分「有网关库的 PG」与「别人的 PG」"
fi
# 超级用户解析：复用他人容器时几乎从不叫 postgres（acc-pg17=acc_super、
# casdoor=casdoor）。硬编码 -U postgres 会让存在性检查与 CREATE DATABASE 双双
# 静默失败（`2>/dev/null` + `|| warn`），症状远在几百行外。
#
# ⚠ 判据形状（变异 C 打红过一轮）：早期这道门只查「函数体里没有字面 `-U postgres`」，
# 而实现其实早就不写那个字面量了——超级用户统一从变量 $su 传进 pg_as_super。
# 于是把 `su="$(pg_superuser_of …)"` 换成 `su="postgres"`（**语义完全等价于本轮缺陷**
# 且门全绿），因为门在看语法形状、看不到取值来源。
# ⇒ 改为钉住**解析入口这一行本身**：必须真的调 pg_superuser_of 拿值。
ensure_body="$(sed -n "/^ensure_pg_database()/,/^}/p" "$SCRIPT")"
if grep -qF 'su="$(pg_superuser_of "$c")"' "$SCRIPT" \
   && ! grep -qF -- '-U postgres' <<<"$ensure_body"; then
  ok "ensure_pg_database 的超级用户来自 pg_superuser_of（不再是写死的 postgres）"
else
  bad "ensure_pg_database 未从容器实际超级用户取值（复用他人容器必静默失败）"
fi
# 建库/建角色失败必须 die 而不是 warn：本轮症状（迁移腿报 database does not exist）
# 完全由「warn 后继续」放大而来。
# ⚠ 判据形状（变异 D 打红过一轮）：早期这道门只查「函数体里出现过 die」——函数里
# 共有两处 die，任一处还在就恒绿，把建库那处的 warn 放过去测不出来。
# ⇒ 改为**逐分支**断言：取 CREATE 那行及其后两行，里面必须有 || die。
for anchor in 'CREATE ROLE \"' 'CREATE DATABASE \"'; do
  branch="$(grep -A2 -F "$anchor" <<<"$ensure_body" | head -3)"
  if grep -qF '|| die' <<<"$branch"; then
    ok "「${anchor}」分支失败时 die（不 warn 后继续）"
  else
    bad "「${anchor}」分支失败只 warn → 带不存在的库继续跑，故障点漂到几百行外"
  fi
done
# 角色必须先于库创建：CREATE DATABASE ... OWNER 指向不存在的角色报 42704。
# ⚠ 判据写法（2026-10-18 自己踩的坑）：锚点必须带转义引号 `CREATE ROLE \"`，
# 只写 `CREATE ROLE` 会先命中**上面那行解释这件事的注释**（注释里同样出现了
# "CREATE DATABASE ... OWNER"），于是 role_line 反而大于 db_line，把正确的实现
# 判成错的。凡是本仓这类「源码文本扫描」的门，锚点都要精确到只有真代码才有的形状。
role_line="$(grep -nF 'CREATE ROLE \"' <<<"$ensure_body" | head -1 | cut -d: -f1)"
db_line="$(grep -nF 'CREATE DATABASE \"' <<<"$ensure_body" | head -1 | cut -d: -f1)"
if [[ -n "$role_line" && -n "$db_line" && "$role_line" -lt "$db_line" ]]; then
  ok "先建角色再建库（OWNER 不悬空）"
else
  bad "CREATE DATABASE 早于 CREATE ROLE（OWNER 会 42704）role=${role_line:-无} db=${db_line:-无}"
fi

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
# ── 量具自身的竞态门（2026-10-20 新增）──────────────────────────────────
# 「printf 出一个字符串 | grep -q」在 `set -o pipefail` 下是**随机假红**：
# grep -q 命中即退出 ⇒ 上游 printf 还在写 ⇒ 收 SIGPIPE(141) ⇒ pipefail 把
# 整条管道判成非零 ⇒ `if` 走 else 分支 ⇒ 门报假红。
# 实测（2026-10-20）：50KB 正文的 `$_nocomment` 一条断言 60 次里假红 1 次；
# 且**失败项在复跑间漂移**（这轮是「configs 种子」那条，下轮是「GO 基础镜像」
# 那条）—— 竞态的特征，不是某条断言写错的特征（真写错会稳定地红同一条）。
# ⇒ 本门内一律用 here-string（`grep ... <<<"$X"`）：命令退出码取 grep 的，
#   上游 141 不会污染判定；语义上只多一个行尾换行，对**按行匹配**无影响
#   （本文件用到 -A2/-nF 的两处锚点都在函数体中部，行号不受影响）。
# 这条门把「不许再写回 printf|grep」钉死，否则下一个会话改回来就复发。
if command -v python3 >/dev/null 2>&1; then
  for f in "$ROOT/tests/deploy_211_contract_test.sh" "$ROOT/tests/deploy_211_teardown_order_test.sh"; do
    racy_n="$(python3 - "$f" <<'PY'
import re, io, sys
s = io.open(sys.argv[1], encoding="utf-8").read()
# 注释行不算：说明性文字里引用该形态是允许的（那不是可执行代码）。
code = "\n".join(l for l in s.splitlines() if not l.lstrip().startswith("#"))
pat = re.compile(r"printf\s+'%s'\s+\"\$[A-Za-z_][A-Za-z0-9_]*\"\s*\|\s*grep")
print(len(pat.findall(code)))
PY
)"
    check "$(basename "$f") 无「printf|grep」竞态形态" "0" "$racy_n"
  done
else
  printf '  SKIP printf|grep 竞态扫描（本机无 python3）\n'
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
# --via 必须把三个基础镜像 build-arg 变量转发到远端：权威仓 registry-211
# （方案 42）；漏转发时远端若再静默回落 docker.io，外网一断构建就死。
# ⚠ 2026-10-20 引擎泛化后，转发改成**显式清单 + 通用展开**
#   （`for _v in …; _dd_fwd+=" ${_v}='${!_v}'"`），字面量 `DD_GO_BASE_IMAGE='`
#   不再出现在脚本里 ⇒ 旧断言（grep 字面量）恒红，但转发**其实没坏**。
#   ⇒ 改判两件事：① 变量在**转发清单**里；② 通用展开那行在。
#   只判①会漏「清单里有但没展开」；只判②会漏「清单里没有」。
_fwd_list="$(sed -n '/for _v in DD_TARGET/,/; do/p' "$SCRIPT")"
_expand_line="$(grep -n '_dd_fwd+=' "$SCRIPT" | head -1)"
for _v in DD_GO_BASE_IMAGE DD_RUNTIME_BASE_IMAGE DD_BASE_REGISTRY; do
  if grep -qF "$_v" <<<"$_fwd_list" && [[ -n "$_expand_line" ]]; then
    ok "--via 转发基础镜像变量 ${_v}（registry-211 构建路径）"
  else
    bad "--via 未转发 ${_v}，远端将回落错误默认基础镜像"
  fi
done
# ★ 反向断言：**repo 相对派生的量不得进转发清单**。
#   DD_WEB_DIST_SRC / DD_MOBILE_DIST_SRC / DD_CONFIGS_SRC 在引擎顶部按
#   `$DD_REPO_ROOT/<x>` 算出来，而 DD_REPO_ROOT 两侧不同（部署机 = 本机仓库，
#   远端 = /root/llm-gateway-go-211）。转发过去 = 远端拿着 macOS 路径去种子化
#   ⇒ 前端产物「缺失」⇒ 撞「上一轮种子还在」判定 ⇒ **部署直接 die**；
#   configs 则是静默回落内置脱敏集（sensitive_patterns.yaml 没种进去）。
_fwd_bad=""
for _v in DD_WEB_DIST_SRC DD_MOBILE_DIST_SRC DD_CONFIGS_SRC; do
  if grep -qF "$_v" <<<"$_fwd_list"; then _fwd_bad+="${_v} "; fi
done
if [[ -z "$_fwd_bad" ]]; then
  ok "转发清单不含 repo 相对派生的量（web/mobile/configs 源由远端自己算）"
else
  bad "转发清单含 repo 相对派生的量：${_fwd_bad}（远端会拿到部署机路径 ⇒ 部署 die / 配置静默回落）"
fi
# 默认基础镜像必须钉 registry-211.itestu.cn，禁止回落 docker.io/golang。
# 形状钉桩：默认值字符串本身含 registry-<target> + lang-base/kx-base-golang +
# lang-base/alpine:3.22——只断言「变量存在」测不出「默认仍是 docker.io」。
# ⚠ 2026-10-20 引擎泛化后，「registry-211.itestu.cn」这个**写死的主机名**
#   属于**薄外壳**（scripts/deploy-211.sh）的职责，引擎侧必须按 target 拼。
#   ⇒ 引擎判「默认是 registry-${DD_TARGET}.itestu.cn」且**不含**写死的
#     registry-211（写死 = 引擎没真正主机无关，新主机会拉到 211 的仓）。
_nocomment_engine="$(grep -vE '^[[:space:]]*#' "$SCRIPT")"
if grep -qF 'registry-${DD_TARGET}.itestu.cn' <<<"$_nocomment_engine"; then
  ok "引擎默认基础镜像仓按 target 拼（registry-\${DD_TARGET}.itestu.cn）"
else
  bad "引擎默认基础镜像仓不是按 target 拼的（新增主机时必须手工改引擎）"
fi
if grep -qF 'registry-211.itestu.cn' <<<"$_nocomment_engine"; then
  bad "引擎里仍写死 registry-211.itestu.cn —— 引擎没真正主机无关"
else
  ok "引擎不含写死的主机专属仓名"
fi
if grep -qF 'lang-base/kx-base-golang:1.27-alpine-amd64' <<<"$_nocomment_engine"; then
  ok "引擎默认 GO 基础镜像 = lang-base/kx-base-golang:1.27-alpine-amd64"
else
  bad "引擎默认 GO 基础镜像不是 lang-base/kx-base-golang:1.27-alpine-amd64"
fi
if grep -qF 'lang-base/alpine:3.22-amd64' <<<"$_nocomment_engine"; then
  ok "引擎默认 RUNTIME 基础镜像 = lang-base/alpine:3.22-amd64"
else
  bad "引擎默认 RUNTIME 基础镜像不是 registry-211 lang-base/alpine:3.22-amd64"
fi
if grep -qE 'GO_BASE_IMAGE=\$\{DD_GO_BASE_IMAGE:-golang:' <<<"$_nocomment_engine"; then
  bad "引擎仍默认 docker.io golang:*（211 直连不通）"
else
  ok "引擎不再默认 docker.io golang:*"
fi
if grep -qE '^[[:space:]]*dd_ensure_base_image(s)?\(\)|dd_ensure_base_images' <<<"$_nocomment_engine"; then
  ok "引擎构建前确保基础镜像（pull registry → 离线回落）"
else
  bad "引擎缺少 dd_ensure_base_images：未 pull 就 build 会在无本地缓存时炸"
fi
if grep -qE 'database\.connected|db_down' \
   <<<"$_nocomment_engine" && grep -qE 'mode=lite|lite' <<<"$_nocomment_engine"; then
  ok "引擎就绪验证钉 DB/Redis connected + 拒绝 lite（readyz 200 不够）"
else
  bad "引擎就绪验证未断言 database/redis connected（或未拒绝 lite）"
fi
if grep -qF 'dd_smoke_admin_login' \
   <<<"$_nocomment_engine" && grep -qF '/api/auth/token' <<<"$_nocomment_engine"; then
  ok "引擎部署后登录冒烟走 /api/auth/token（非 /api/admin/login）"
else
  bad "引擎缺少登录冒烟或仍指向错误的 /api/admin/login"
fi
# ★ 2026-10-23（F2）重写：原来是「引擎源码里有没有 X-Real-IP / $remote_addr
#   字样」的**字符串存在性**检查 —— 那是**恒绿门**：把整段逻辑删成空壳照样绿，
#   而 F2 的原症状恰恰是「这段逻辑**从来没生效过**」。
#   ⇒ 现在钉**接线**（引擎怎么用补丁器）+ **不变量**（不写死域名 / 失败要还原
#     / 结论分类要区分「没有」与「不认识」）。
if grep -qF 'nginx-llm-realip-patch.py' <<<"$_nocomment_engine"; then
  ok "引擎调用独立补丁器 nginx-llm-realip-patch.py（判定逻辑可离线验证）"
else
  bad "引擎没有调用独立补丁器：X-Real-IP 判定又回到内嵌 heredoc（不可验证）"
fi
if grep -qE -- '--domain[[:space:]]' <<<"$_nocomment_engine" \
   && grep -qF 'DD_DOMAIN' <<<"$_nocomment_engine"; then
  ok "引擎把 DD_DOMAIN 传给补丁器（域名不写死）"
else
  bad "引擎没把 DD_DOMAIN 传给补丁器：换域名会静默改错对象"
fi
if grep -qF 'llm.itestu.cn' <<<"$_nocomment_engine"; then
  bad "引擎正文里仍写死 llm.itestu.cn —— 与「引擎不含主机专属信息」自相矛盾"
else
  ok "引擎正文不含写死域名（llm.itestu.cn 只出现在注释里）"
fi
if grep -qE 'search-root' <<<"$_nocomment_engine"; then
  ok "引擎跨文件搜 443 腿（--search-root）—— 不再只在自己的 conf 里找"
else
  bad "引擎不跨文件搜 443 腿：443 腿在别的文件时永远找不到（211 的现状）"
fi
if grep -qF 'skip-domain-not-on-443-block' <<<"$_nocomment_engine" \
   && grep -qF 'noop-unknown-shape' <<<"$_nocomment_engine"; then
  ok "引擎区分「本机没有该域的 443」（信息）与「形态不认识」（才告警）"
else
  bad "引擎不区分两种『没改』结论：要么继续打假告警，要么该告警时不告警"
fi
# nginx -t 失败 ⇒ 必须还原：否则磁盘上留着没验证过的配置，
# 之后任何一次 reload 都会加载它（潜伏半切换态，同仓审计 §4.1）。
if grep -qE 'nginx -t' <<<"$_nocomment_engine" \
   && grep -qE 'cp "\$\{backs\[\$' <<<"$_nocomment_engine"; then
  ok "nginx -t 失败时会从备份还原（不留半切换态配置）"
else
  bad "nginx -t 失败不还原：磁盘上留一份没验证过的配置（潜伏半切换态）"
fi
# ★ 2026-10-23（211 502 事故根修）：「upstream 端口」这条**事实**的读写必须
#   ① 走共享脚本（scripts/ops/nginx-upstream-port.py）—— 内嵌在引擎里的正则
#      **不剥注释** ⇒ 会读到 nginx 注释里那个旧端口（245 实况）⇒ 活跃槽判错；
#   ② 在**任何写动作之前**有一次「槽位↔端口绑定」体检（dd_check_slot_binding）——
#      绑定脱节时**拒绝部署**，而不是拿状态文件猜一个槽位去切流/停槽。
if [[ -f "$ROOT/scripts/ops/nginx-upstream-port.py" ]] \
   && grep -qF 'nginx-upstream-port.py' <<<"$_nocomment_engine"; then
  ok "引擎的 upstream 端口读写走共享脚本（掩码后定位 ⇒ 注释里的旧端口不算数）"
else
  bad "引擎没有走共享的 upstream 端口判定脚本：注释里的旧端口会被当真（⇒ 活跃槽判错 ⇒ 502）"
fi
# ★ 单一真值源：引擎里**不得**再出现内嵌的 upstream 端口正则
#   （内嵌副本迟早与共享脚本漂移 ⇒ 读侧/写侧口径不一致 ⇒ 又一次半切换态）。
if grep -qF 'server\s+127\.0\.0\.1' <<<"$_nocomment_engine"; then
  bad "引擎里仍有内嵌的 upstream 端口正则 ⇒ 判定口径会与共享脚本漂移（删掉重复实现）"
else
  ok "引擎里没有内嵌的 upstream 端口正则（判定逻辑单一真值源）"
fi
if grep -qE 'DD_UPSTREAM_PORT_HELPER' <<<"$_nocomment_engine"; then
  ok "引擎按自身目录解析判定脚本（不依赖可被覆盖的 DD_REPO_ROOT）"
else
  bad "引擎解析判定脚本的方式脆弱（DD_REPO_ROOT 可被覆盖 ⇒ 脚本可能找不到）"
fi
if grep -qF 'dd_check_slot_binding' <<<"$_nocomment_engine"; then
  ok "引擎有「槽位↔端口绑定」体检（绑定脱节 ⇒ 拒绝部署而不是猜）"
else
  bad "引擎没有绑定体检：upstream 指向别的产品的端口时仍会照常切流/停槽"
fi
# 体检必须发生在**写动作之前**（迁移重放 / 起容器 / 切流 / 停旧槽都在它后面）
_guard_line="$(grep -n 'dd_check_slot_binding$' "$SCRIPT" | head -1 | cut -d: -f1)"
_migrate_line="$(grep -n 'REVISION_SEQ=' "$SCRIPT" | head -1 | cut -d: -f1)"
_candidate_line="$(grep -n 'DD_CANDIDATE_CONTAINER="$(dd_container_name' "$SCRIPT" | head -1 | cut -d: -f1)"
if [[ -n "$_guard_line" && -n "$_migrate_line" && -n "$_candidate_line" ]] \
   && [[ "$_guard_line" -lt "$_migrate_line" && "$_guard_line" -lt "$_candidate_line" ]]; then
  ok "绑定体检发生在迁移重放与起容器之前（脱节时一个字节都不写）"
else
  bad "绑定体检的位置太晚（迁移/起容器已经动过了）"
fi
# 只读体检工具必须在场（属主要能自己查现场）
if [[ -f "$ROOT/scripts/ops/deploy-doctor.sh" ]]; then
  ok "只读体检工具在场（scripts/ops/deploy-doctor.sh）"
else
  bad "缺少只读体检工具：绑定脱节时属主没有手段查现场"
fi

# 入口 deploy-211.sh 必须 export 同一组 registry-211 默认，经 --via 传到远端。
if [[ -f "$ROOT/scripts/deploy-211.sh" ]]; then
  _w="$(grep -vE '^[[:space:]]*#' "$ROOT/scripts/deploy-211.sh")"
  if grep -qF 'export DD_BASE_REGISTRY=' \
     <<<"$_w" && grep -qF 'export DD_GO_BASE_IMAGE=' \
     <<<"$_w" && grep -qF 'export DD_RUNTIME_BASE_IMAGE=' <<<"$_w"; then
    ok "deploy-211.sh 入口 export registry-211 基础镜像默认"
  else
    bad "deploy-211.sh 未 export DD_* 基础镜像默认（远端可能吃到空值）"
  fi
fi
# ── Web 前端腿（2026-10-15）：root SPA / /m 移动端 / 版本元数据 ─────────
# 形态钉桩：tar 同步**不得**带任何形态的 dist 排除——macOS bsdtar 的 exclude
# 对无通配模式也按「任意尾部路径段」语义匹配（--exclude=dist 与
# --exclude=./dist 实测都命中 web/dist），root 只剩版本 JSON——这是
# llm.itestu.cn 打不开管理页的根因；种子+挂载+env 三件必须成套。
_nocomment="$(grep -vE '^[[:space:]]*#' "$SCRIPT")"
if grep -qE -- "--exclude=(\./)?dist" <<<"$_nocomment"; then
  bad "tar 同步仍带 dist 排除（bsdtar 尾段语义会排掉 web/dist，root SPA 缺失根因）"
else
  ok "tar 同步不带任何 dist 排除（web/dist、web-mobile/dist 随树上树）"
fi
# 同族：--exclude=build 会误伤 vendor/.../api/types/build/*.go（2026-10-15 12h 审计 P2）
if grep -qE -- "--exclude=(\./)?build([[:space:]]|$)" <<<"$_nocomment"; then
  bad "tar 同步仍带 build 排除（bsdtar 尾段语义会排掉 vendor/.../types/build）"
else
  ok "tar 同步不带裸 build 排除（vendor docker types/build 随树上树）"
fi
# configs 种子必须钉 DD_REPO_ROOT，禁止再 $(cd "$(dirname "$0")/..")——相对 $0
# 依赖 cwd，docker build 后/非仓库 cwd 会报 cd: scripts/..: No such file（2026-10-15）。
if grep -qE 'DD_CONFIGS_SRC=.*\$DD_REPO_ROOT/configs' <<<"$_nocomment"; then
  ok "configs 种子路径钉 DD_REPO_ROOT（不依赖 cwd/\$0）"
else
  bad "configs 种子仍用 dirname \$0 相对 cd，非仓库 cwd 会 No such file"
fi
if grep -qE 'DD_CONFIGS_SRC=.*dirname "\$0"' <<<"$_nocomment"; then
  bad "configs 种子仍含 dirname \"\$0\" 相对路径（cwd 漂移即炸）"
fi
# ── 前端产物新鲜度闭环（2026-10-15 审计 P1）────────────────────────────
# 搬运链通了不等于闭环了：原实现缺 dist 时只 warn，且 rm -rf 写在 if 内，
# 于是「上一版 SPA 被继续挂载」= 无声前端回滚；陈旧 dist 照样种子化；而
# 就绪只探 /readyz 与 /healthz 不探 /，三类问题都不会让部署失败。相对
# deploy-local.sh 的硬失败（2538 白页事故收口）属方向性回退。
# 下面几条一律扫 $_nocomment：注释里出现字符串不算数（否则整段被删、
# 只留注释里的字面量，门照样绿——这正是本门此前能漏掉该缺陷的原因）。
if grep -qE '^[[:space:]]*seed_web_dist\(\)' <<<"$_nocomment"; then
  ok "211 定义 seed_web_dist 前端产物闸门"
else
  bad "211 缺少 seed_web_dist 前端产物闸门（缺失/陈旧 dist 只会 warn，部署照样绿）"
fi
if grep -qE -- '-newer .*index\.html' <<<"$_nocomment"; then
  ok "211 前端闸门按源码新鲜度判定（-newer），不靠构建序号"
else
  bad "211 前端闸门没有新鲜度判定：陈旧 dist 会被照旧种子化，线上是旧 SPA 而部署显示成功"
fi
if grep -qE "!\ -name '\*\.test\.ts'|!\ -name \"\*\.test\.ts\"|! -name '\\*\\.test\\.ts'" <<<"$_nocomment"; then
  ok "211 前端新鲜度排除 *.test.ts（测试文件不进 bundle）"
elif grep -qF "! -name '*.test.ts'" <<<"$_nocomment"; then
  ok "211 前端新鲜度排除 *.test.ts（测试文件不进 bundle）"
else
  bad "211 前端新鲜度未排除 *.test.ts，测试文件 mtime 会误杀部署"
fi
if grep -qE 'DD_ALLOW_STALE_WEB' <<<"$_nocomment"; then
  ok "211 保留纯 API 形态逃生舱 DD_ALLOW_STALE_WEB"
else
  bad "211 硬拒绝后没有逃生舱，纯 API 部署形态被堵死"
fi
# 无声回滚（缺 dist 但上一轮种子还在）必须 die，且**不受逃生舱豁免**：
# 纯 API 的前提是「压根没有前端」，不是「前端是旧的」。
if grep -qE 'GW_WEB_DIST_OK=1|GW_MOBILE_DIST_OK=1' <<<"$_nocomment"; then
  ok "211 种子化以闸门结论为准（GW_*_DIST_OK），缺 dist 时不挂载"
else
  bad "211 种子化仍直接判 index.html 存在与否，未走闸门结论"
fi
for _pat in 'LLM_GATEWAY_STATIC_DIR=/app/web/dist' 'MOBILE_WEB_DIST=/app/web-mobile/dist' '/app/version.json:ro'; do
  if grep -qF "$_pat" "$SCRIPT"; then
    ok "web 腿契约在场: $_pat"
  else
    bad "web 腿契约缺失: ${_pat}（SPA/移动端/版本元数据将回落或不可用）"
  fi
done
# 版本戳：仓库 version.json 是别的部署留下的戳，211 必须用部署机真实 HEAD 改写，
# 否则 /version 冒充他人版本（2026-10-16 实测：跑 75bf327a 却显示 2661-76e185e7）。
if grep -qF "DD_BUILD_SHA='\${DD_BUILD_SHA}'" <<<"$_nocomment"; then
  ok "--via 转发构建提交号 DD_BUILD_SHA"
else
  bad "--via 未转发 DD_BUILD_SHA，远端无 .git 只能沿用他人版本戳"
fi
if command -v python3 >/dev/null 2>&1; then
  sed -n '/^import json, sys, time$/,/^json.dump(v, open(path, "w"), indent=2)$/p' "$SCRIPT" > "$TMP/vstamp.py"
  printf '{"version":"2.5.8-76e185e7-20261015-2661","git_tag":"2.5.8","git_sha":"76e185e7","build_seq":2661}\n' > "$TMP/v.json"
  # 三个参数：路径 / sha / target（target 决定版本串后缀，引擎主机无关）
  python3 "$TMP/vstamp.py" "$TMP/v.json" "75bf327a" "211" >/dev/null 2>&1
  _vs="$(python3 -c 'import json,sys;v=json.load(open(sys.argv[1]));print(v["git_sha"], v["version"].split("-")[1], v["version"].endswith("-211"), v.get("target"))' "$TMP/v.json" 2>/dev/null)"
  check "种子 version.json 按构建提交号改写（sha/version 后缀 -211）" "75bf327a 75bf327a True 211" "$_vs"
else
  printf '  SKIP 版本戳改写行为（本机无 python3）\n'
fi
# ★ 改写目标必须是**本槽的副本**（version/<slot>.json），不是平铺的 version.json。
#   2026-10-20 实测过的坑：蓝绿把种子改成按槽隔离后，改写仍指向平铺路径
#   （那个文件**没被挂载**）⇒ 改写对线上毫无作用；而 python 只传 2 个参数却读
#   sys.argv[3] ⇒ IndexError 被 `|| warn` 吞掉 ⇒ 失败形态是「静默不改写」。
#   ⇒ 判据钉两件事：① 改写路径含 <slot>；② 平铺路径不得再出现在改写调用里。
if grep -qE 'python3 - "\$DD_GW_DATA_ROOT/version/\$DD_CANDIDATE_SLOT\.json"' <<<"$_nocomment"; then
  ok "version.json 改写目标是本槽副本（version/<slot>.json）"
else
  bad "version.json 改写未指向本槽副本 —— 改写落到未挂载的平铺路径等于没改"
fi
if grep -qE 'python3 - "\$DD_GW_DATA_ROOT/version\.json"' <<<"$_nocomment"; then
  bad "version.json 改写仍指向平铺路径 \$DD_GW_DATA_ROOT/version.json（未挂载，改写无效）"
else
  ok "version.json 改写不再指向未挂载的平铺路径"
fi
# 槽位隔离：web / web-mobile / version 三样种子都必须按槽分目录，
# 否则「回退不需还原种子」的前提不成立（旧槽会被本轮覆盖）。
for _seed in 'web/$DD_CANDIDATE_SLOT/dist' 'web-mobile/$DD_CANDIDATE_SLOT/dist' 'version/$DD_CANDIDATE_SLOT.json'; do
  if grep -qF "$_seed" <<<"$_nocomment"; then
    ok "种子按槽隔离: ${_seed}"
  else
    bad "种子未按槽隔离: ${_seed}（回退时旧槽的产物已被本轮覆盖）"
  fi
done
# 执行位置守卫：macOS 裸跑（无 --via）必须立刻给出 --via 指引，而不是让
# mkdir /opt/llm-gateway-go-211 甩出无上下文的 Permission denied（2026-10-15 用户实测踩到）
if grep -q 'uname -s.*==.*Darwin.*||' "$SCRIPT" || grep -qE '\[\[ -z "\$VIA" && "\$\(uname -s\)" == "Darwin" \]\]' "$SCRIPT"; then
  ok "macOS 裸跑守卫在场（误用即报并指路 --via）"
else
  bad "缺 macOS 裸跑守卫：部署机误以本机模式执行只会看到 mkdir Permission denied"
fi
# deploy-211.sh 必须是 deploy-154/245 同一族入口标准：同一骨架（SSHPASS 卫生、
# deploy-lib 解析、parse-wrapper-flags 的 --force 族解析、--dry-run 合同、
# PROBE_TIMEOUT_SECS 默认）、exec 委托引擎而不是自创接口。缺任一块就是家族
# 标准漂移（2026-10-15 owner 要求统一标准）。
WRAPPER="$ROOT/scripts/deploy-211.sh"
wmiss=0
[[ -f "$WRAPPER" ]] || { bad "deploy-211.sh 入口缺失"; wmiss=1; }
if (( wmiss == 0 )); then
  for _sig in 'unset SSHPASS' 'source "$SCRIPT_DIR/deploy-lib-resolve.sh"' \
              'parse-wrapper-flags.sh' 'extract_force_unlock' \
              "' --dry-run '" 'PROBE_TIMEOUT_SECS' \
              'deploy-seamless-docker.sh" --via "$DD_SSH_TARGET"'; do
    if grep -qF -- "$_sig" "$WRAPPER"; then
      ok "deploy-211.sh 家族骨架: $_sig"
    else
      bad "deploy-211.sh 缺家族骨架块: $_sig"
    fi
  done
fi
# 引擎必须消费 PROBE_TIMEOUT_SECS（否则入口的家族标准超时是摆设）
if grep -q 'PROBE_TIMEOUT_SECS' "$SCRIPT"; then
  ok "引擎就绪等待消费 PROBE_TIMEOUT_SECS（与 154/245 同语义）"
else
  bad "引擎未消费 PROBE_TIMEOUT_SECS，入口的探针超时默认不生效"
fi
# ⚠ 且消费点必须带 :- 兜底：ssh 远端执行时该变量不一定穿过（bash 4.4+ 的
# set -u 让未绑定变量进算术直接炸——2026-10-15 用户实测「一大堆错误」的根因）
if grep -qE '\$\{PROBE_TIMEOUT_SECS:-' "$SCRIPT"; then
  ok "引擎 PROBE_TIMEOUT_SECS 带 :- 兜底（远端未传不炸）"
else
  bad "引擎 PROBE_TIMEOUT_SECS 无 :- 兜底（远端执行必炸 unbound variable）"
fi
# --via 段必须先拿 deploy-lib 家族锁再碰目标树（锁行号 < rm -rf 行号）
# ⚠ 锚点是 `rm -rf '$DD_REMOTE_ROOT'`（引擎泛化后目标树根是变量，不是写死的
#   ~/llm-gateway-go-211）—— 锚点写死旧路径会「grep 不到 ⇒ 判定失败」，
#   而不是因为锁顺序真的坏了。
lock_line="$(grep -n 'lock_acquire_local' "$SCRIPT" | head -1 | cut -d: -f1)"
rm_line="$(grep -n "rm -rf '\$DD_REMOTE_ROOT'" "$SCRIPT" | head -1 | cut -d: -f1)"
if [[ -n "$lock_line" && -n "$rm_line" && "$lock_line" -lt "$rm_line" ]]; then
  ok "引擎 --via 先取家族部署锁（deploy-lib lock.sh）再动目标树（${lock_line} < ${rm_line}）"
else
  bad "--via 无锁或锁在破坏性步骤之后（并发部署会在 rm vs tar 处互炸）"
fi

# ── 硬阻断清单透传（r1037，2026-10-20）─────────────────────────────────
# 「哪些规则允许终止回合」由 LLM_GATEWAY_HARD_BLOCK_RULES 决定。运维要能
# 在不改代码的情况下开关它 ⇒ 部署引擎必须把它透传进容器。
#
# 三条契约：
#   ① 引擎从部署机环境变量 / env 文件里取值；
#   ② 取到值时用 -e 注入容器；
#   ③ 取不到时**不注入**（用网关内置默认：只有 input 允许硬阻断）——
#      注入空串会被网关当成「显式清空清单」，那会把请求侧的 fail-closed
#      一起关掉（正是 r1037 判据纪律第 3 条说的那种「两个成因都放行」）。
if grep -qE 'LLM_GATEWAY_HARD_BLOCK_RULES' "$SCRIPT"; then
  ok "引擎认 LLM_GATEWAY_HARD_BLOCK_RULES（硬阻断清单可配）"
else
  bad "引擎不认 LLM_GATEWAY_HARD_BLOCK_RULES：清单无法在部署时配置"
fi
if grep -qE '\-e "LLM_GATEWAY_HARD_BLOCK_RULES=' "$SCRIPT"; then
  ok "引擎用 -e 把硬阻断清单注入容器"
else
  bad "引擎未用 -e 注入 LLM_GATEWAY_HARD_BLOCK_RULES"
fi
if grep -qE 'if \[\[ -n "\$HARD_BLOCK_RULES" \]\]; then' "$SCRIPT"; then
  ok "引擎空值时不注入清单（用网关内置默认，而不是显式清空）"
else
  bad "引擎无条件注入硬阻断清单：空值会被网关当成显式清空"
fi
# env 文件每次部署都被重写 ⇒ 引擎必须保留上一份里运维手工改过的值，
# 否则「改 env 文件」这种运维动作在下一次部署后静默失效。
if grep -qE 'ENV_FILE.*LLM_GATEWAY_HARD_BLOCK_RULES|LLM_GATEWAY_HARD_BLOCK_RULES.*ENV_FILE' "$SCRIPT"; then
  ok "引擎从 env 文件复用上次落下的硬阻断清单（运维手工改的能跨部署保留）"
else
  bad "引擎不复用 env 文件里的硬阻断清单：运维手工改的值下次部署即丢"
fi
# 部署机 → 远端引擎的转发：--via 形态的引擎在**目标机**上跑，部署机的环境
# 变量到不了那儿 ⇒ 清单必须显式转发，否则「export 了再部署」是空操作。
if grep -qE 'DD_SKIP_LOGIN_SMOKE LLM_GATEWAY_HARD_BLOCK_RULES' "$SCRIPT"; then
  ok "引擎把 LLM_GATEWAY_HARD_BLOCK_RULES 转发到远端（部署机 export 有效）"
else
  bad "引擎不转发 LLM_GATEWAY_HARD_BLOCK_RULES：部署机 export 的清单到不了远端"
fi

echo
printf '结果: %d 通过, %d 失败\n' "$pass" "$fail"
(( fail == 0 )) || exit 1
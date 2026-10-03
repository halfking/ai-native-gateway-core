#!/usr/bin/env bash
# =====================================================================
# scripts/ursm-redis-db-migrate.sh — URSM v2 键迁移到专用 Redis db
#
# ⚠️⚠️ 2026-10-03：本脚本的 --copy 阶段已废弃，请改用
#      scripts/ursm-redis-db-migrate.py
#
#    废弃原因（全部在 redis-cli 6.2.22 上实测，不是推断）：redis-cli 的
#    DUMP|RESTORE 二进制管道在这个版本上无法可靠传递 payload ——
#      1) `-x restore KEY 0 replace` → ERR syntax error
#         （-x 把 stdin 放在最后，REPLACE 被排到了 payload 前面）
#      2) `--raw dump | head -c -1 | -x restore KEY <ttl>` → 返回 OK 但
#         恢复出的键 TTL≈0、值却正确。DUMP 权威长度 31 字节（Lua #d 实测），
#         redis-cli --raw 落盘 32 字节：它把 CRC64 末字节 0x09 做成了
#         "\t" 两字节的 C 风格转义，末字节因此错位。
#      3) `--no-raw dump | -x restore KEY <ttl>`（官方文档配对写法）
#         → ERR DUMP payload version or checksum are wrong
#      4) `MIGRATE 127.0.0.1 <port> KEY 14 500 COPY REPLACE` → IOERR
#         （MIGRATE 到自身自锁，不支持同实例跨 db）
#      （Redis Lua 也不行：含 \0 的 binary 过不了 redis.call 的参数类型检查。）
#    redis-py 的 dump()/restore() 走同一条协议但不做字符转义，实测字节数与
#    TTL 均正确。
#
#    保留本文件的唯一目的：--plan 预检仍可用（它只用 O(1) 的 DBSIZE，
#    不碰二进制）。若连预检也不需要，请直接用 .py 版本。
#
# =====================================================================
# 以下为原始实现，仅 --plan 可信；--copy 的实现已证伪，不要使用。
# =====================================================================
# scripts/ursm-redis-db-migrate.sh — URSM v2 键迁移到专用 Redis db
#
# 用途
#   URSM v2 的节点键目前与网关会话键混在同一个 db（默认 db2，约 158 万键）。
#   persist writer 每分钟 SCAN <prefix>node:* 采集快照；Redis 的 MATCH 是
#   服务端过滤、游标遍历躲不掉，所以它必须走完整个键空间，实测 12.95~30.40s，
#   正好压在 writer 的 30s 预算上（main.go 的 writer 预算），于是
#   "ursm.v2: persist collect failed: redis scan failed: context deadline exceeded"
#   成为常态故障。把 URSM 挪到独立 db 后，SCAN 只遍历约 1.3K 键。
#
# ★ 为什么必须在流量静默窗口内做（不要只停 persist writer）
#   ursm:v2:node:* 由**请求路径**每请求写一次：
#     domains/streaming/executors/executor_nodehealth.go:313
#       -> Manager.RecordRequest
#       -> store.RecordRequestKeySet
#       -> record_request.lua
#   停 persist writer 只停掉"读"，停不掉"写"。只要还有请求进来，节点键就会
#   继续落在旧 db；等切到新 db 后，这段窗口内的更新就丢了 —— 而 URSM 靠
#   coverage manifest 重建时只覆盖部分节点（实测 714/1240，缺 563 个旧文法
#   节点），不能靠重启自愈。所以必须先静默流量。
#
# 用法
#   # 1) 只读预检（不写任何数据，可在任意时刻跑）
#   bash scripts/ursm-redis-db-migrate.sh --plan -a 172.16.2.210:6389 -s 2 -d 14
#
#   # 2) 静默窗口内：执行复制 + 校验（目标 db 必须为空，否则拒绝执行）
#   bash scripts/ursm-redis-db-migrate.sh --copy -a 172.16.2.210:6389 -s 2 -d 14
#
#   # 3) 脚本只**打印**后续动作，不会替你改 env 或重启服务
#
# 参数
#   -a <host:port>   Redis 地址（必填）
#   -s <db>          源 db（必填）
#   -d <db>          目标 db（必填，必须为空）
#   -p <prefix>      键前缀，默认 ursm:v2:
#   -E <path>        取密码的 env 文件，默认 /etc/llm-gateway-go/env
#                    （245 上是 /opt/llm-gateway-go/.env）
#
# 运行环境要求
#   redis-cli **>= 6.2**：RESTORE 的 REPLACE 子句是 6.2 引入的。
#   154 上现装的是 3.2.5，不满足；请在 245 或 252 上跑（本机 redis-cli 6.2.22）。
#
# 约束
#   - 需要 redis-cli 与目标实例可达；密码从 env 文件读，不接受命令行明文传入，
#     且通过 REDISCLI_AUTH 传递（不出现在进程命令行里）。
#   - 只迁移 <prefix>* 这一族键，不碰同 db 的其它业务键。
#   - ⚠️ 下面这些约束属于已废弃的 --copy 实现，保留仅为记录。--plan 只用
#     O(1) 的 DBSIZE，仍然可信。
# =====================================================================
set -euo pipefail

PREFIX_DEFAULT="ursm:v2:"
MODE=""
SRC_ADDR=""
SRC_DB=""
DST_DB=""
ENV_FILE_OVERRIDE=""

die() { printf '[die] %s\n' "$*" >&2; exit 1; }
info() { printf '[info] %s\n' "$*"; }
warn() { printf '[warn] %s\n' "$*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --plan|--copy) MODE="${1#--}"; shift ;;
    -a) SRC_ADDR="${2:-}"; shift 2 ;;
    -s) SRC_DB="${2:-}"; shift 2 ;;
    -d) DST_DB="${2:-}"; shift 2 ;;
    -p) PREFIX_DEFAULT="${2:-}"; shift 2 ;;
    -E) ENV_FILE_OVERRIDE="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,50p' "$0" | sed '/^# ===/,$d'; exit 0 ;;
    *) die "未知参数: $1" ;;
  esac
done

[ -n "$MODE" ]        || die "必须给 --plan 或 --copy"
# ★ 硬闸门放在最前面：--copy 的实现已被证伪（见文件头四条实测），绝不能拿它
#   碰生产。放在参数校验之后、任何实际操作之前，这样误用者第一眼就能看到
#   替代方案，而不是先卡在"读不到 env 文件"或"redis-cli 版本过低"上。
if [ "$MODE" = "copy" ]; then
  die "本脚本的 --copy 已废弃：redis-cli 6.2.22 的 DUMP|RESTORE 二进制传递不可靠（详见文件头四条实测）。请改用：
    python3 scripts/ursm-redis-db-migrate.py --copy ${SRC_ADDR:-<host:port>} -s ${SRC_DB:-<db>} -d ${DST_DB:-<db>}
  --plan 预检仍可用（只用 O(1) 的 DBSIZE，不碰二进制）。"
fi
[ -n "$SRC_ADDR" ]    || die "必须给 -a <host:port>"
[ -n "$SRC_DB" ]      || die "必须给 -s <src db>"
[ -n "$DST_DB" ]      || die "必须给 -d <dst db>"

case "$SRC_DB" in *[!0-9]*) die "src db 必须是数字: $SRC_DB";; esac
case "$DST_DB" in *[!0-9]*) die "dst db 必须是数字: $DST_DB";; esac
[ "$SRC_DB" != "$DST_DB" ] || die "src db 与 dst db 不能相同"

ENV_FILE="${ENV_FILE_OVERRIDE:-/etc/llm-gateway-go/env}"
[ -r "$ENV_FILE" ] || die "读不到 ${ENV_FILE}，无法取 Redis 密码（可用 -E <path> 指定；245 的路径是 /opt/llm-gateway-go/.env）"
# 用 sed 而不是 grep -oP：-P 是 GNU 扩展，macOS/BSD 的 grep 不认
# （实测会报 "invalid option -- P" 并让脚本带着空密码继续跑）。
REDIS_PW="$(sed -n 's/^LLM_GATEWAY_REDIS_PASSWORD=//p' "$ENV_FILE" | head -1)"
[ -n "$REDIS_PW" ] || die "$ENV_FILE 里没有 LLM_GATEWAY_REDIS_PASSWORD"

# ------------------------------------------------------------------
# redis-cli 能力闸门
# ------------------------------------------------------------------
# ★ 2026-10-03 实测：154 上是 redis-cli 3.2.5（2017 年），它既不认
#   --no-auth-warning（6.0+），RESTORE 也不支持 REPLACE 子句（6.2+）。
#   在这种机器上跑 --copy 会「认证失败 -> DBSIZE 返回空 -> 被当成非空」
#   或者更糟：DUMP|RESTORE 失败被 || true 吞掉并计成 copied 成功。
#   所以在动手前就要求 6.2+，而不是让脚本带着已知缺陷去生产上跑。
RCLI_VER="$(redis-cli --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$RCLI_VER" ] || die "取不到 redis-cli 版本，脚本要求 >= 6.2"
# 逐段比大小，不用 sort -V（同样是 GNU 扩展，BSD sort 不认）。
ver_ge() {
  awk -v a="${1%%.*}" -v b="${2%%.*}" 'BEGIN{exit !(a+0 >= b+0)}' \
   || awk -v a="${1%.*}" -v b="${2%.*}" \
          -v a2="${1##*.}" -v b2="${2##*.}" \
      'BEGIN{exit !((a+0 > b+0) || ((a+0 == b+0) && (a2+0 >= b2+0)))}'
}
ver_ge "$RCLI_VER" 6.2 || die "redis-cli $RCLI_VER 过低，本脚本需要 >= 6.2（RESTORE REPLACE）。请换用 6.2+ 的机器运行，或先升级 redis-cli。"

# 用 REDISCLI_AUTH 而不是 -a：密码不出现在进程命令行（ps 可见），且不依赖
# --no-auth-warning（老版本没有这个选项）。
R() { REDISCLI_AUTH="$REDIS_PW" redis-cli -h "${SRC_ADDR%:*}" -p "${SRC_ADDR##*:}" "$@"; }

# ------------------------------------------------------------------
# 预检
# ------------------------------------------------------------------
# ★ 严格校验：认证失败 / 网络不通时 redis-cli 会把错误写 stderr 并返回空串。
#   旧版直接 tr -d 后当数字用，空串 != "0" 于是报「目标非空」——一个把
#   连接故障渲染成「目标 db 有数据」的假警报。这里改成非整数即 die。
dbsize_of() {
  local db="$1" out
  out="$(R -n "$db" dbsize 2>&1 | tr -d '[:space:]')"
  case "$out" in
    ''|*[!0-9]*) die "读 db$db 的 DBSIZE 失败（原始输出: $(R -n "$db" dbsize 2>&1 | head -2 | tr '\n' ' '))。先确认地址/端口/密码可达，再谈迁移。" ;;
  esac
  printf '%s' "$out"
}
dst_keys() { dbsize_of "$DST_DB"; }
src_keys() { dbsize_of "$SRC_DB"; }

# ★ 刻意不提供「统计前缀键数」的便捷函数：它必须遍历整个键空间
#   （158 万键约 13-30s），而 Redis 上正有 writer 每分钟做同样的遍历并因此
#   超时。预检一律只用 O(1) 的 DBSIZE；精确前缀计数在 copy 阶段的校验里做。

info "源 $SRC_ADDR db$SRC_DB (总键 $(src_keys)) -> 目标 db$DST_DB (总键 $(dst_keys))"
info "前缀: ${PREFIX_DEFAULT}*"

if [ "$(dst_keys)" != "0" ]; then
  die "目标 db$DST_DB 非空（$(dst_keys) 键）。拒绝执行：迁移会与既有键混合，回滚将无法区分来源。请换一个空 db 或先人工清理。"
fi

# 目标 db 必须不含任何 ursm 残留（历史踩过的坑：db15 留着陈旧 meta/ready）
leftover="$(R -n "$DST_DB" exists "${PREFIX_DEFAULT}meta:ready" | tr -d '[:space:]')"
[ "$leftover" = "0" ] || die "目标 db$DST_DB 已存在 ${PREFIX_DEFAULT}meta:ready，拒绝执行"

if [ "$MODE" = "plan" ]; then
  info "预检通过。以下为只读信息，未做任何变更："
  info "  - 目标 db$DST_DB 为空，可用作 URSM 专用 db"
  info "  - 复制需要在流量静默窗口执行（请求路径每请求写 node 键）"
  info ""
  info "静默窗口步骤（人工执行，脚本不代劳）："
  info "  1) 停止写入方网关，使请求不再落到旧 db"
  info "  2) bash \$0 --copy ${SRC_ADDR} -s ${SRC_DB} -d ${DST_DB}"
  info "  3) 校验通过后设置 URSM_V2_REDIS_DB=${DST_DB}（留空=沿用共享 client，行为不变）"
  info "  4) 启动网关，确认 persist committed 恢复且 row 数为迁移前量级"
  info ""
  info "回滚：把 URSM_V2_REDIS_DB 置回未设置并重启即可；旧 db 数据原样保留不动。"
  exit 0
fi

# ------------------------------------------------------------------
# 复制
# ------------------------------------------------------------------
# ★ 硬闸门：--copy 的实现已被证伪（见文件头四条实测），绝不能拿它碰生产。
#   这里直接拒绝，而不是留着一段看起来能跑的代码等人误用。
if [ "$MODE" = "copy" ]; then
  die "本脚本的 --copy 已废弃：redis-cli 6.2.22 的 DUMP|RESTORE 二进制传递不可靠（详见文件头）。请改用：
    python3 scripts/ursm-redis-db-migrate.py --copy ${SRC_ADDR:-<host:port>} -s ${SRC_DB:-<db>} -d ${DST_DB:-<db>}
  --plan 预检仍可用（只用 O(1) 的 DBSIZE）。"
fi

warn "=== 即将复制 ${PREFIX_DEFAULT}* ：$SRC_ADDR db$SRC_DB -> db$DST_DB ==="
warn "=== 请确认此刻请求流量已静默；否则窗口内的节点更新会丢失 ==="
read -r -p "输入 MIGRATE 确认: " ans
[ "$ans" = "MIGRATE" ] || die "未确认，已取消"

copied=0
skipped=0
failed=0
cur=0
while :; do
  batch="$(R -n "$SRC_DB" scan "$cur" count 500 2>/dev/null)"
  cur="$(printf '%s\n' "$batch" | head -1)"
  keys="$(printf '%s\n' "$batch" | tail -n +2 | grep "^${PREFIX_DEFAULT}" || true)"
  while IFS= read -r k; do
    [ -n "$k" ] || continue
    # ★ TTL 必须是 -1（沿用 DUMP 里记录的原始过期时间），不是 0。
    #   RESTORE 的 ttl=0 意为「永不过期」。URSM 节点键是 15min TTL
    #   （config.go NodeTTL），若按 0 恢复，死节点键会永久驻留在新 db，
    #   coverage 重建只覆盖部分节点（实测 714/1240），漏掉的那些会一直
    #   留在路由候选里 —— 比迁移失败更难排查。
    # ★ 不用 `|| true`：旧的写法把 DUMP/RESTORE 的失败静默吞掉并计入
    #   copied，于是「认证失败」会伪装成「迁移成功」。RESTORE 必须回 OK。
    out="$(R -n "$SRC_DB" --no-raw dump "$k" 2>/dev/null \
           | R -n "$DST_DB" -x restore "$k" -1 replace 2>&1)" || true
    case "$out" in
      OK)          copied=$((copied+1)) ;;
      *BUSYKEY*)    skipped=$((skipped+1)); warn "目标已存在，跳过: $k" ;;
      *)           failed=$((failed+1)); warn "复制失败: $k -> $out" ;;
    esac
  done <<EOF
$keys
EOF
  printf '\r[copy] 已处理前缀键 %d（游标 %s）' "$copied" "$cur"
  [ "$cur" = "0" ] && break
done
printf '\n'

info "复制完成: copied=$copied skipped=$skipped failed=$failed"
[ "$failed" -eq 0 ] || die "有 $failed 个键复制失败。目标 db 状态未知，**不要**设置 URSM_V2_REDIS_DB，先排查后重跑。"

# ------------------------------------------------------------------
# 校验
# ------------------------------------------------------------------
[ "$skipped" -eq 0 ] || warn "有 $skipped 个键在目标已存在 —— 目标 db 并非真正为空，请人工复核"

# 逐键点验：在源与目标各 EXISTS 同一批前缀键，比对计数
verify_count() {
  local db="$1" n=0 cur=0 batch
  while :; do
    batch="$(R -n "$db" scan "$cur" count 1000 2>/dev/null)"
    cur="$(printf '%s\n' "$batch" | head -1)"
    n=$(( n + $(printf '%s\n' "$batch" | tail -n +2 | grep -c "^${PREFIX_DEFAULT}" || true) ))
    [ "$cur" = "0" ] && break
  done
  printf '%s\n' "$n"
}

src_n="$(verify_count "$SRC_DB")"
dst_n="$(verify_count "$DST_DB")"
info "校验: 源 db$SRC_DB 前缀键=$src_n  目标 db$DST_DB 前缀键=$dst_n"

if [ "$src_n" != "$dst_n" ]; then
  die "校验失败：源 $src_n != 目标 $dst_n。不要设置 URSM_V2_REDIS_DB，保持现状并排查。"
fi
[ "$dst_n" -gt 0 ] || die "目标前缀键数为 0，迁移无效。"

info "✓ 校验通过（$dst_n 个前缀键两侧一致）"
info ""
info "后续人工步骤（脚本未执行）："
info "  1) 在 154 与 245 的 env 中设置 URSM_V2_REDIS_DB=${DST_DB}"
info "     ★ 必须在两侧都设；只设一台会造成两台快照节点集不一致"
info "  2) 启动网关，观察 'ursm.v2: persist committed' 的 rows 是否回到迁移前量级"
info "  3) 观察一轮 'persist collect failed' 是否消失"
info ""
info "回滚：把 URSM_V2_REDIS_DB 置回未设置并重启。旧 db$SRC_DB 数据原样保留。"

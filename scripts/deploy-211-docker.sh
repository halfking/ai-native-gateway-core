#!/usr/bin/env bash
# =============================================================================
# deploy-211-docker.sh — 在 192.168.31.211 的 Docker 中部署 llm-gateway-go
#
# 定位：把本网关（Docker 形态）落到 211 这台机器上，**优先复用**该机 Docker 里
#      已有的 pg17+columnar 实例与 Redis 实例；没有匹配的就地新建。
#
# ── 复用/新建的判定口径 ────────────────────────────────────────────────
#   PostgreSQL 候选：容器镜像或名字命中 postgres / citus / pg 之一；
#                   逐个探活，要求同时满足
#                     (1) server_version_num 在 170000..179999（PG17）
#                     (2) pg_am 里存在 columnar / pgcolumnar 表访问方法
#                   两者都满足才算「匹配」，否则继续找下一个候选。
#   Redis 候选：容器镜像或名字命中 redis 之一，且能 `redis-cli PING` 通。
#   命中 → 直接复用（不新建、不改它的数据/端口/生命周期）；一个都没命中 → 新建。
#
# ── Redis 的特殊处理：DB 库号（用户口中的 "tableid"）从 15 往前倒排 ──
#   Redis 实例默认 16 个逻辑库（0..15），库号就是一个共享命名空间：两个服务
#   用同一个库号 → key 互相覆盖/踩踏。本机 211 上 Redis 可能是别的服务在用，
#   因此网关**不从 0 开始抢**，而是从 15 开始往下倒着找一个没人用的库号：
#       15 → 14 → 13 → … → 0
#   判定「没人用」= 该库 DBSIZE == 0（真的没有 key）。
#   选定的库号写进 claim 文件持久化，重跑不会漂移（否则每次部署换库 = 缓存全丢）。
#   Redis Cluster 模式不支持非 0 库（只有 db0），此时强制 db0 并显式告警——
#   这是硬约束，不能用"倒排"绕过去。
#
# 用法（在 211 上执行）：
#   bash scripts/deploy-211-docker.sh                 # 探测 → 复用/新建 → 部署网关
#   bash scripts/deploy-211-docker.sh --status        # 只看探测结果，不改动
#   bash scripts/deploy-211-docker.sh --redis-db 15   # 强制指定 Redis 库号
#   bash scripts/deploy-211-docker.sh --stop          # 停掉网关容器（不动 PG/Redis）
#
# 从本机远程执行（rsync 仓库后在 211 上跑）：
#   bash scripts/deploy-211-docker.sh --via 192.168.31.211
#
# 密钥落点（不进仓库）：211 上 $D211_STATE_DIR/env/gateway.env（chmod 600）
# =============================================================================
set -euo pipefail

# ── 参数 ───────────────────────────────────────────────────────────────
D211_TARGET_IP="${D211_TARGET_IP:-192.168.31.211}"
D211_NETWORK="${D211_NETWORK:-llm-gateway-net}"
D211_STATE_DIR="${D211_STATE_DIR:-/opt/llm-gateway-go-211}"
D211_GW_CONTAINER="${D211_GW_CONTAINER:-llm-gateway-211}"
D211_GW_IMAGE="${D211_GW_IMAGE:-llm-gateway-211}"
D211_GW_PORT="${D211_GW_PORT:-8781}"
D211_PG_DB="${D211_PG_DB:-llm_gateway}"
D211_PG_USER="${D211_PG_USER:-llm_gateway}"
D211_PG_CONTAINER="${D211_PG_CONTAINER:-kx-pg17-columnar}"
D211_PG_IMAGE="${D211_PG_IMAGE:-kx-pg17-columnar:17}"
D211_REDIS_CONTAINER="${D211_REDIS_CONTAINER:-llm-gateway-redis}"
D211_REDIS_IMAGE="${D211_REDIS_IMAGE:-redis:7-alpine}"
D211_PGCOLUMNAR_VER="${D211_PGCOLUMNAR_VER:-v1.0-alpha4}"
# 库号倒排：上界默认 15（Redis 默认 16 库的最大编号），下界 0
D211_REDIS_DB_START="${D211_REDIS_DB_START:-15}"
D211_REDIS_DB_END="${D211_REDIS_DB_END:-0}"

D211_REPO_ROOT="${LLM_GATEWAY_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

log()  { printf '[211] %s\n' "$*" >&2; }
warn() { printf '[211] WARN: %s\n' "$*" >&2; }
die()  { printf '[211] FATAL: %s\n' "$*" >&2; exit 1; }

# redis-cli 认证参数（由 redis_auth_args 填充；见 Redis 探测段）
REDIS_AUTH=()

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; }

MODE=deploy
VIA=""
FORCE_REDIS_DB=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --status)     MODE=status; shift ;;
    --probe-only) MODE=probe-only; shift ;;
    --stop)       MODE=stop; shift ;;
    --redis-db)   FORCE_REDIS_DB="${2:?--redis-db N}"; shift 2 ;;
    --via)        VIA="${2:?--via HOST}"; shift 2 ;;
    -h|--help)    usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

CLAIM_FILE="$D211_STATE_DIR/redis-db.claim"
ENV_FILE="$D211_STATE_DIR/env/gateway.env"

# =============================================================================
# --via：把当前仓库同步到目标机并在目标机上执行本脚本
# =============================================================================
if [[ -n "$VIA" ]]; then
  [[ "$MODE" == "deploy" ]] || die "--via 只支持默认 deploy 模式（--status/--stop 请 ssh 后本地跑）"
  log "同步仓库到 $VIA:$D211_TARGET_IP ..."
  # 先在目标端把工作树整体清空——tar 解包不会删目标已有文件，旧部署残留的
  # macOS `._*` 元数据 / .env* 会一直挂在那让 docker COPY 重新带进镜像。
  # 2026-10-13 真机第三轮翻车根因：第一次同步带进去了 `._*`，加 --exclude 重跑也没用。
  ssh "$VIA" 'rm -rf ~/llm-gateway-go-211 && mkdir -p ~/llm-gateway-go-211'
  # 用 tar over ssh 排除体积大/无用目录，避免 node_modules、.git 拖慢或撑爆磁盘。
  # .env* 必须排干净：Dockerfile 里是 `COPY . .`，而 .dockerignore 并不排除
  # .env*，所以一旦把 .env.local 同步过去，它的 admin key / secret / 口令就会
  # 进 docker build 上下文、留在镜像层里（docker history 就能翻出来）。
  # ._* / .DS_Store 排除：macOS 同步到 Linux 时 AppleDouble 元数据会进 go:embed，
  # i18n 加载 `._ar.json` 直接 panic invalid character '\x00'。见真机 2026-10-13 翻车。
  tar -C "$D211_REPO_ROOT" \
      --exclude=.git --exclude=node_modules --exclude=dist --exclude=build \
      --exclude='*.log' --exclude='.env' --exclude='.env.*' \
      --exclude='._*' --exclude='.DS_Store' \
      -cf - . | ssh "$VIA" "tar --no-xattrs -C ~/llm-gateway-go-211 -xf - && \
        find ~/llm-gateway-go-211 -name '._*' -type f -delete && \
        find ~/llm-gateway-go-211 -name '.DS_Store' -type f -delete && \
        echo '[211] 同步后 ._* 残留: '\$(find ~/llm-gateway-go-211 -name '._*' -type f | wc -l)"
  # 双保险：目标机上再扫一遍，真有残留就当场删掉，别留给下一次构建
  ssh "$VIA" "find ~/llm-gateway-go-211 -maxdepth 2 -name '.env*' -type f -delete; \
             echo '[211] 目标树残留 .env*: '\$(find ~/llm-gateway-go-211 -name '.env*' -type f | wc -l) \
             ' ._*: '\$(find ~/llm-gateway-go-211 -name '._*' -type f | wc -l)"
  log "在 $VIA 上执行 deploy ..."
  ssh "$VIA" "cd ~/llm-gateway-go-211 && D211_STATE_DIR=$D211_STATE_DIR D211_REDIS_DB_START=$D211_REDIS_DB_START D211_REDIS_DB_END=$D211_REDIS_DB_END LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY='${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-}' bash scripts/deploy-211-docker.sh $*"
  exit $?
fi

mkdir -p "$D211_STATE_DIR/env" "$D211_STATE_DIR/logs"
chmod 700 "$D211_STATE_DIR" 2>/dev/null || true

# =============================================================================
# 前置：Docker 必须可用
# =============================================================================
need_docker() {
  command -v docker >/dev/null 2>&1 || die "docker 未安装（${D211_TARGET_IP}）"
  docker info >/dev/null 2>&1 || die "docker 守护进程未运行或当前用户无权限（试 sudo docker info）"
}
need_docker
log "Docker: $(docker --version)"

# =============================================================================
# 探测一：PostgreSQL 17 + columnar
# =============================================================================
# 探活单个候选容器，输出 "<server_version_num> <columnar_flag> <vector_flag>"；失败返回非 0。
# 关键变更：判定 columnar 不能查 pg_am —— pg_am 是「当前已建扩展的某库里」的访问方法表，
# 复用别人起的容器时若目标库还没建 citus_columnar/pgcolumnar，pg_am 永远是空的。
# 正确口径是查 pg_available_extensions（在 template1 上看安装能力），这才是「装了就能用」。
# 三种实现均认：citus_columnar（Citus 系，acc-pg17 / kx-citus-pg17 就是这种）、
# columnar（Citus 早版本/直挂）、pgcolumnar（独立发行版）。vector 也同时校验。
pg_probe_container() {
  local c="$1" u pw out
  u="$(docker exec "$c" printenv POSTGRES_USER 2>/dev/null || true)"
  u="${u:-postgres}"
  pw="$(docker exec "$c" printenv POSTGRES_PASSWORD 2>/dev/null || true)"
  pw="${pw:-${LLM_GATEWAY_PG_PASSWORD:-}}"
  for cu in "$u" postgres; do
    # ⚠ 不能 `tr -d '[:space:]'`：三段以空格分隔，去掉空格后会被 read 当成一个字段。
    # 这正是 2026-10-13 第一轮误判「acc-pg17 列不满足」的真因：把 "170010 1 1" 压成
    # "17001011"，pg_is_17 判 17001011 ≥ 180000 直接 false → 走「新建」。
    out="$(docker exec -e PGPASSWORD="$pw" "$c" \
      psql -X -U "$cu" -d postgres -tAc \
      "SELECT current_setting('server_version_num')::int
            || ' ' || (CASE WHEN EXISTS (SELECT 1 FROM pg_available_extensions
                                          WHERE name IN ('citus_columnar','columnar','pgcolumnar'))
                            THEN 1 ELSE 0 END)
            || ' ' || (CASE WHEN EXISTS (SELECT 1 FROM pg_available_extensions
                                          WHERE name='vector')
                            THEN 1 ELSE 0 END);" \
      2>/dev/null | tr -d '\r' | head -1)" || continue
    # 兼容 psql 在某些容器里多吐一个 "WARNING:" 头的情形——只取第一段三字段
    out="$(printf '%s\n' "$out" | awk '{print $1, $2, $3}')"
    [[ -n "$out" ]] && { printf '%s\n' "$out"; return 0; }
  done
  return 1
}

pg_is_17()         { local v="$1"; (( v >= 170000 && v < 180000 )); }
pg_has_columnar() { (( "$1" > 0 )); }
pg_has_vector()    { (( "$1" > 0 )); }

# 在所有容器里找一个 pg17+columnar 的实例；找到输出容器名，否则返回非 0。
#
# ⚠ 2026-10-13 真机翻车：docker ps --format "{{.Names}}|{{.Image}}" 用 `|` 分隔，
# 但默认 IFS=space+tab+newline，read -r name img 不会在 `|` 切分 → img 永远是空
# → case 关键字匹配只走 lower_name（永远没 *citus* 之类）→ 所有候选全 continue
# → 走「新建」路径 → codeload tarball 没 submodule → 构建炸。
# 修：单行 IFS='|' read 强制按 | 切。
find_pg_container() {
  local name img lower_name lower_img probe
  while IFS='|' read -r name img; do
    [[ -z "$name" ]] && continue
    # 容器状态必须是 running 才谈得上"能用"
    [[ "$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || true)" == "running" ]] || continue
    lower_name="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
    lower_img="$(printf '%s' "$img" | tr '[:upper:]' '[:lower:]')"
    case "$lower_name$lower_img" in
      *postgres*|*citus*|*" pg"*|*-pg*|*pg17*) ;;
      *) continue ;;
    esac
    if probe="$(pg_probe_container "$name")"; then
      # probe 形如 "<ver> <col> <vec>"，三段用 read 拆
      local ver col vec
      read -r ver col vec < <(printf '%s\n' "$probe")
      if pg_is_17 "$ver" && pg_has_columnar "$col" && pg_has_vector "$vec"; then
        printf '%s\n' "$name"; return 0
      fi
      log "  候选 $name: 版本=${ver} columnar=${col} vector=${vec} → 不满足(需 PG17+columnar+vector)，继续找"
    else
      log "  候选 $name: psql 探活失败（可能不是 PG 或需密码），继续找"
    fi
  done < <(docker ps --format '{{.Names}}|{{.Image}}')
  return 1
}

# 新建 PG17+columnar 实例（源码编译 pgcolumnar 扩展打进镜像）
#
# 基础镜像选 pgvector/pgvector:pg17 而不是 postgres:17-*：网关库实际用到 8 个扩展，
#   btree_gist / pg_stat_statements / pg_trgm / pgcrypto / pgstattuple 属 contrib（自带），
#   pgcolumnar 需要源码编译，
#   **vector(pgvector) 在 vanilla postgres 镜像里根本没有** —— 少了它，
#   从本机灌数据时 CREATE EXTENSION vector 直接失败。
# 该镜像是 Debian 系（bookworm），所以构建链走 apt-get 而不是 apk。
create_pg_container() {
  log "未找到匹配的 pg17+columnar 实例，开始新建 ..."
  local build_dir="$D211_STATE_DIR/build"
  mkdir -p "$build_dir"
  cat > "$build_dir/Dockerfile.pgcolumnar" <<EOF
# 由 deploy-211-docker.sh 生成：PostgreSQL 17 + pgvector + pgcolumnar
# 关键：pgcolumnar 仓库的 objstore/ 是 git submodule，codeload 的 tarball 不含它，
# make 会在 objstore 步骤报 "No rule to make target 'all'"。改用 git clone
# --recurse-submodules 才是正确拉法。同时补 pkg-config（pgcolumnar 用它解析
# zlib/lz4/zstd 的 link flag，没有就 _inflateInit2_ undefined）。
FROM ${D211_PG_BASE_IMAGE:-pgvector/pgvector:pg17}
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential libzstd-dev liblz4-dev zlib1g-dev pkg-config git curl ca-certificates \
 && git clone --recurse-submodules --depth 1 --branch ${D211_PGCOLUMNAR_VER} \
      https://github.com/commandprompt/pgcolumnar /tmp/pgc \
 && cd /tmp/pgc && make PG_CONFIG=/usr/local/bin/pg_config LDFLAGS="-Wl,--as-needed" \
 && make PG_CONFIG=/usr/local/bin/pg_config LDFLAGS="-Wl,--as-needed" install \
 && rm -rf /tmp/pgc /var/lib/apt/lists/*
EOF
  log "构建 PG 镜像（含 pgcolumnar ${D211_PGCOLUMNAR_VER}），分钟级 ..."
  docker build -t "$D211_PG_IMAGE" -f "$build_dir/Dockerfile.pgcolumnar" "$build_dir" \
    || die "PG 镜像构建失败，见上方输出"

  local pw; pw="${LLM_GATEWAY_PG_PASSWORD:-}"
  if [[ -z "$pw" ]]; then
    pw="$(head -c 24 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')"
  fi
  printf 'LLM_GATEWAY_PG_PASSWORD=%s\n' "$pw" > "$D211_STATE_DIR/env/pg.secret"
  chmod 600 "$D211_STATE_DIR/env/pg.secret"

  docker rm -f "$D211_PG_CONTAINER" >/dev/null 2>&1 || true
  # pg_stat_statements 必须预加载，否则后续 CREATE EXTENSION 会报
  # "must be loaded via shared_preload_libraries"。镜像 entrypoint 见到首个参数
  # 以 "-" 开头会自动补 "postgres"，所以这里可以直接传 -c。
  docker run -d --name "$D211_PG_CONTAINER" --restart unless-stopped \
    -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD="$pw" \
    -e POSTGRES_DB=postgres \
    -v "${D211_PG_CONTAINER}-data:/var/lib/postgresql/data" \
    "$D211_PG_IMAGE" \
    -c shared_preload_libraries=pg_stat_statements,pgcolumnar \
    -c pg_stat_statements.track=all \
    >/dev/null || die "PG 容器启动失败"

  log "等待 PG 就绪 ..."
  local i
  for i in $(seq 1 60); do
    pg_probe_container "$D211_PG_CONTAINER" >/dev/null 2>&1 && break
    (( i == 60 )) && die "PG 60 次探测后仍未就绪：docker logs $D211_PG_CONTAINER"
    sleep 2
  done
  log "PG 实例就绪: $D211_PG_CONTAINER"
}

# 在选定的 PG 实例里准备网关库/角色（幂等）
ensure_pg_database() {
  local c="$1"
  # 若已被别的服务建过同名库，直接复用（不覆盖数据）
  if docker exec -e PGPASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" "$c" \
      psql -X -U postgres -d postgres -tAc \
      "SELECT 1 FROM pg_database WHERE datname='${D211_PG_DB}'" 2>/dev/null | grep -q 1; then
    log "数据库 $D211_PG_DB 已存在，复用"
  else
    docker exec -e PGPASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" "$c" \
      psql -X -U postgres -d postgres -c "CREATE DATABASE \"${D211_PG_DB}\" OWNER \"${D211_PG_USER}\";" \
      >/dev/null 2>&1 || warn "CREATE DATABASE 未成功（可能已存在），继续"
    log "已创建数据库 $D211_PG_DB"
  fi
  # 角色（幂等）
  docker exec -e PGPASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" "$c" \
    psql -X -U postgres -d postgres -c \
    "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='${D211_PG_USER}') THEN CREATE ROLE \"${D211_PG_USER}\" LOGIN PASSWORD '${LLM_GATEWAY_PG_PASSWORD:-}'; END IF; END \$\$;" \
    >/dev/null 2>&1 || warn "角色 $D211_PG_USER 创建告警"
  # 兼容多种 columnar 发行：citus_columnar（Citus 系，acc-pg17 即此）/
  # columnar / pgcolumnar / vector + 其他 contrib 扩展一并装上。
  # 网关运行时 00-prereqs.sql 会再 idempotent 装一次，这里是兜底（即使 prereqs
  # 没跑到也能让 USING columnar 立即可用）。
  docker exec -e PGPASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" "$c" \
    psql -X -U postgres -d "$D211_PG_DB" -v ON_ERROR_STOP=0 -c "
DO \$col\$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='citus_columnar') THEN
    BEGIN EXECUTE 'CREATE EXTENSION IF NOT EXISTS citus WITH SCHEMA pg_catalog'; EXCEPTION WHEN OTHERS THEN NULL; END;
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS citus_columnar WITH SCHEMA pg_catalog';
  ELSIF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='columnar') THEN
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS columnar';
  ELSIF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='pgcolumnar') THEN
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS pgcolumnar';
  END IF;
END\$col\$;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS pgstattuple;
" >/dev/null 2>&1 || warn "扩展预装告警（可能无权限/部分扩展不可用，由 00-prereqs.sql 接管）"
}

# =============================================================================
# 探测二：Redis
# =============================================================================
# 复用别人起的 redis 容器时，密码可能既不在 env 也不在 REDISCLI_AUTH，而是埋在
# `docker inspect ... Cmd` 的 --requirepass 参数里。所以探测期不能假定 LLM_GATEWAY_REDIS_PASSWORD
# 已经设好——redis_cli 第一次先无密码 PING，不通再尝试从启动命令抽密码，再不通才放弃。
# 全局缓存到 REDIS_PASSWORD 以供后续 redis_db_in_use / cluster 探测共用。

redis_try_ping() {
  # $1=container $2=password(可空)；输出 PONG/错误；返回 0/非 0
  # ⚠ 2026-10-13 真机翻车：redis-cli 7 加 -a 时会在 PONG 之前多打一行
  # "Warning: Using a password with '-a' ..."，[[ "$out" == "PONG" ]] 直接不等 → 探活
  # 判失败 → 走「新建 Redis」分支 → 抢了别人的端口。修：再走一次 tail -1 拿 PONG。
  local c="$1" pw="${2:-}"
  local args=()
  [[ -n "$pw" ]] && args=( -a "$pw" )
  docker exec "$c" redis-cli "${args[@]}" PING 2>/dev/null | tail -n 1
}

redis_extract_password_from_cmd() {
  # 从 `docker inspect ... Cmd` + Entrypoint 拼出的字符串里抓 --requirepass 下一个 token
  local c="$1" args="" pw="" prev=""
  args="$(docker inspect -f '{{range .Config.Cmd}}{{.}} {{end}}{{range .Config.Entrypoint}}{{.}} {{end}}' "$c" 2>/dev/null || true)"
  for tok in $args; do
    case "$prev" in
      --requirepass) pw="$tok"; break ;;
    esac
    prev="$tok"
  done
  [[ -n "$pw" ]] && printf '%s' "$pw"
}

# 探活某容器是否可用：返回 0 表示 PING 通，并通过 stdout 末尾写密码（用 \x1f 分隔）。
# 调用方拿到「name\x1fpw」，拆开就够。子 shell 改全局变量在 bash 里无效，
# 所以走 stdout 通道回传更稳。调用方用 `printf '%s\n%s\n' name pw` 也能两行；
# 但本函数只产密码片段即可，name 由调用方自己加。
redis_probe_usable() {
  local c="$1" pw="" out
  pw="$(docker exec "$c" printenv REDISCLI_AUTH 2>/dev/null || true)"
  pw="${pw:-${LLM_GATEWAY_REDIS_PASSWORD:-}}"
  if [[ -z "$pw" ]]; then
    pw="$(redis_extract_password_from_cmd "$c" || true)"
  fi
  out="$(redis_try_ping "$c" "$pw")"
  if [[ "$out" == "PONG" ]]; then
    # stdout 第一行写"PONG 密码"前缀分隔，调用方按行解析拿到 pw。
    # 比「函数内改全局变量」更稳——bash 命令替换里子 shell 的全局赋值
    # 对父 shell 不可见，这是 2026-10-13 真机第四轮翻车根因。
    printf 'PONG\x1f%s\n' "$pw"
    return 0
  fi
  return 1
}

redis_auth_args() {
  if [[ -n "${REDIS_PASSWORD:-}" ]]; then
    REDIS_AUTH=( -a "$REDIS_PASSWORD" )
  else
    REDIS_AUTH=()
  fi
}

redis_ping() {
  local c="$1"
  redis_auth_args
  docker exec "$c" redis-cli "${REDIS_AUTH[@]}" PING 2>/dev/null | grep -q PONG
}

redis_cluster_enabled() {
  local c="$1"
  redis_auth_args
  [[ "$(docker exec "$c" redis-cli "${REDIS_AUTH[@]}" \
      INFO server 2>/dev/null | grep -c '^cluster_enabled:1')" == "1" ]]
}

find_redis_container() {
  local name img ln li
  while IFS='|' read -r name img; do
    [[ -z "$name" ]] && continue
    [[ "$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || true)" == "running" ]] || continue
    ln="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
    li="$(printf '%s' "$img" | tr '[:upper:]' '[:lower:]')"
    case "$ln$li" in
      *redis*) ;;
      *) continue ;;
    esac
    # 探活：redis_probe_usable stdout 写 "PONG\x1f<pw>"。
    #
    # ⚠️ 2026-10-13 12h 审计轮（P1）修正：此前本函数无条件 printf 两行
    # `name\n<pw>`，调用方用 tail -1 取密码。**无密码（trust 模式）时 pw 是空串**，
    # 输出 "name\n\n" 而命令替换的 $() **会剥掉尾部换行** ⇒ tail -1 取回的是
    # 容器名自己。实测 REDIS_PASSWORD=[my-redis]（应为空）。
    # 后果链：redis_auth_args 造出 `-a my-redis`；redis_db_in_use 的 DBSIZE 回包
    # 非数字，撞 `|| return 0`（读不出→保守当占用）⇒ 16 个库全被判占用 ⇒
    # allocate_redis_db die "db15..db0 全被占用"。侥幸存活也会把容器名当密码
    # 写进 env/gateway.env。
    #
    # 改法：**只在确有密码时才输出第二行**，调用方按行数判别。
    # 空密码 → 1 行；有密码 → 2 行。两种情形在 $() 剥尾换行后都无歧义。
    # （不能改成全局变量：REDIS_RAW="$(…)" 在子 shell 里跑，全局赋值传不回来。）
    if probe_out="$(redis_probe_usable "$name")"; then
      local rpw="${probe_out#*$'\x1f'}"
      if [[ -n "$rpw" ]]; then
        printf '%s\n%s\n' "$name" "$rpw"
      else
        printf '%s\n' "$name"          # trust 模式：只有容器名
      fi
      return 0
    fi
    log "  候选 $name: redis PING 失败（无密码+启动命令也抽不出），跳过"
  done < <(docker ps --format '{{.Names}}|{{.Image}}')
  return 1
}

# 某个库号是否已经被占用（有 key）。probe 可注入，便于离线测试。
redis_db_in_use() {
  local c="$1" idx="$2" n
  redis_auth_args
  n="$(docker exec "$c" redis-cli "${REDIS_AUTH[@]}" -n "$idx" DBSIZE 2>/dev/null | tr -d '[:space:]')"
  [[ "$n" =~ ^[0-9]+$ ]] || return 0   # 读不出来 → 保守当作占用
  (( n > 0 ))
}

create_redis_container() {
  log "未找到可用的 Redis 实例，开始新建 ..."
  docker pull "$D211_REDIS_IMAGE" >/dev/null 2>&1 || warn "拉取 $D211_REDIS_IMAGE 失败，使用本地镜像"
  docker rm -f "$D211_REDIS_CONTAINER" >/dev/null 2>&1 || true
  docker run -d --name "$D211_REDIS_CONTAINER" --restart unless-stopped \
    -v "${D211_REDIS_CONTAINER}-data:/data" \
    "$D211_REDIS_IMAGE" redis-server --appendonly yes >/dev/null \
    || die "Redis 容器启动失败"
  local i
  for i in $(seq 1 30); do
    redis_ping "$D211_REDIS_CONTAINER" && break
    (( i == 30 )) && die "Redis 30 次探测后仍未就绪"
    sleep 1
  done
  log "Redis 实例就绪: $D211_REDIS_CONTAINER"
}

# ── 核心：Redis 库号（"tableid"）从 15 往下倒排分配 ────────────────────
# in_use_fn: 形参为库号，返回 0 表示该库已被占用
# 规则：
#   1. 有持久化 claim 且该库当前仍空闲 → 复用（重跑不漂移）
#   2. 否则从 START(15) 递减到 END(0) 找第一个空闲库
#   3. 全被占 → 报错，不抢别人已用的库
allocate_redis_db() {
  local c="$1" claimed idx
  if [[ -f "$CLAIM_FILE" ]]; then
    claimed="$(cat "$CLAIM_FILE" 2>/dev/null || true)"
    if [[ "$claimed" =~ ^[0-9]+$ ]] && ! redis_db_in_use "$c" "$claimed"; then
      log "复用已分配的 Redis 库号 db${claimed}（claim 文件持久化，避免重跑漂移）"
      printf '%s\n' "$claimed"; return 0
    fi
    warn "claim 里的 db${claimed} 已被占用，重新分配"
  fi
  for (( idx = D211_REDIS_DB_START; idx >= D211_REDIS_DB_END; idx-- )); do
    if ! redis_db_in_use "$c" "$idx"; then
      printf '%s\n' "$idx" > "$CLAIM_FILE"
      log "已分配 Redis 库号 db${idx}（从 db$D211_REDIS_DB_START 往下倒排，避开其它服务）"
      printf '%s\n' "$idx"; return 0
    fi
  done
  die "db${D211_REDIS_DB_START}..db${D211_REDIS_DB_END} 全被占用，无法为网关分配独立库号"
}

# =============================================================================
# 探测汇总 / status
# =============================================================================
PG_FOUND="$(find_pg_container || true)"
# find_redis_container：**1 行** = 只有容器名（trust 模式，密码为空）；
# **2 行** = 容器名 + 密码。命令替换的 $() 会剥掉尾部换行，故无密码时
# 绝不能多打一个空行——否则 tail -1 会取回容器名自己（见函数内 P1 注释）。
REDIS_RAW="$(find_redis_container || true)"
REDIS_FOUND=""
REDIS_PASSWORD=""
if [[ -n "$REDIS_RAW" ]]; then
  REDIS_FOUND="$(printf '%s\n' "$REDIS_RAW" | head -1)"
  if [[ "$(printf '%s\n' "$REDIS_RAW" | wc -l | tr -d ' ')" -ge 2 ]]; then
    REDIS_PASSWORD="$(printf '%s\n' "$REDIS_RAW" | tail -1)"
  fi
fi

show_status() {
  echo "=== 211 资源探测 ==="
  if [[ -n "$PG_FOUND" ]]; then
    local p ver col vec
    p="$(pg_probe_container "$PG_FOUND" || echo '? 0 0')"
    read -r ver col vec < <(printf '%s\n' "$p")
    echo "PG   : 复用容器 $PG_FOUND  (版本=${ver} columnar=${col} vector=${vec})"
  else
    echo "PG   : 无匹配的 pg17+columnar+vector 实例（将新建 ${D211_PG_CONTAINER}）"
  fi
  if [[ -n "$REDIS_FOUND" ]]; then
    echo "Redis: 复用容器 $REDIS_FOUND"
    if redis_cluster_enabled "$REDIS_FOUND"; then
      echo "       ⚠ 该实例开启了 cluster（只支持 db0），库号倒排不可用，将固定 db0"
    else
      local cur="(未分配)"
      [[ -f "$CLAIM_FILE" ]] && cur="db$(cat "$CLAIM_FILE")"
      echo "       当前分配库号: $cur  (倒排区间 ${D211_REDIS_DB_START}..${D211_REDIS_DB_END})"
    fi
  else
    echo "Redis: 无可用实例（将新建 ${D211_REDIS_CONTAINER}）"
  fi
}

if [[ "$MODE" == "status" ]]; then
  show_status
  exit 0
fi

# --probe-only：探测 + show_status + 真伪密码验证，但不构建不启动
if [[ "$MODE" == "probe-only" ]]; then
  show_status
  if [[ -n "$PG_FOUND" ]]; then
    echo
    echo "PG 复用细节:"
    echo "  container     = $PG_FOUND"
    echo "  POSTGRES_USER = $(docker exec "$PG_FOUND" printenv POSTGRES_USER 2>/dev/null || echo '?')"
    echo "  POSTGRES_DB   = $(docker exec "$PG_FOUND" printenv POSTGRES_DB 2>/dev/null || echo '?')"
  fi
  if [[ -n "$REDIS_FOUND" ]]; then
    echo
    echo "Redis 复用细节:"
    echo "  container  = $REDIS_FOUND"
    echo "  password 长度 = ${#REDIS_PASSWORD}（空=trust 模式）"
    docker exec "$REDIS_FOUND" redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} INFO server 2>/dev/null | grep -E "redis_version|cluster_enabled|tcp_port" | sed 's/^/    /'
    # 端到端 PING 一次，确认捕获的密码真能通过
    pong="$(docker exec "$REDIS_FOUND" redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} PING 2>/dev/null | tail -n 1)"
    echo "  PING(end-to-end) = $pong"
  fi
  exit 0
fi

if [[ "$MODE" == "stop" ]]; then
  log "停止网关容器 ${D211_GW_CONTAINER}（不动 PG/Redis）"
  docker rm -f "$D211_GW_CONTAINER" >/dev/null 2>&1 || true
  exit 0
fi

# =============================================================================
# 复用 / 新建
# =============================================================================
if [[ -n "$PG_FOUND" ]]; then
  log "复用已有 pg17+columnar 实例: $PG_FOUND"
  PG_CONTAINER="$PG_FOUND"
  PG_REUSED=1
  PG_USER="${LLM_GATEWAY_PG_USER:-$(docker exec "$PG_CONTAINER" printenv POSTGRES_USER 2>/dev/null || true)}"
  PG_USER="${PG_USER:-postgres}"
  PG_PASS="${LLM_GATEWAY_PG_PASSWORD:-}"
  if [[ -z "$PG_PASS" ]]; then
    PG_PASS="$(docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null || true)"
  fi
  # 复用 Citus 系容器时超级用户是 acc_super 而非 POSTGRES_USER；为避免误用，
  # PG_USER 由后续 ensure_pg_database 内部用 postgres 走 DO 块建 role，与复用容器
  # 的实际超级用户解耦——这里只用于 DSN。
  [[ -z "$PG_PASS" ]] && PG_PASS="unused"
else
  create_pg_container
  PG_CONTAINER="$D211_PG_CONTAINER"
  PG_REUSED=0
  PG_PASS="${LLM_GATEWAY_PG_PASSWORD:-}"
  [[ -f "$D211_STATE_DIR/env/pg.secret" ]] && PG_PASS="$(cut -d= -f2- "$D211_STATE_DIR/env/pg.secret")"
fi

if [[ -n "$REDIS_FOUND" ]]; then
  log "复用已有 Redis 实例: $REDIS_FOUND"
  REDIS_CONTAINER="$REDIS_FOUND"
  REDIS_REUSED=1
else
  create_redis_container
  REDIS_CONTAINER="$D211_REDIS_CONTAINER"
  REDIS_REUSED=0
fi

ensure_pg_database "$PG_CONTAINER"

# ── Redis 库号分配（用户要求的"从 15 往前倒排"）────────────────────
if redis_cluster_enabled "$REDIS_CONTAINER"; then
  REDIS_DB=0
  warn "Redis $REDIS_CONTAINER 处于 cluster 模式，仅支持 db0，无法倒排隔离；固定 db0（可能与其它服务共用）"
else
  if [[ -n "$FORCE_REDIS_DB" ]]; then
    REDIS_DB="$FORCE_REDIS_DB"
    printf '%s\n' "$REDIS_DB" > "$CLAIM_FILE"
    log "按 --redis-db 强制指定库号 db$REDIS_DB"
  else
    REDIS_DB="$(allocate_redis_db "$REDIS_CONTAINER")"
  fi
fi

# =============================================================================
# 网络：把网关与复用的 PG/Redis 接到同一个网络，按容器名互访
# =============================================================================
docker network inspect "$D211_NETWORK" >/dev/null 2>&1 || docker network create "$D211_NETWORK" >/dev/null
for c in "$PG_CONTAINER" "$REDIS_CONTAINER"; do
  docker inspect -f '{{json .NetworkSettings.Networks}}' "$c" 2>/dev/null | grep -q "\"$D211_NETWORK\"" \
    || docker network connect "$D211_NETWORK" "$c" >/dev/null 2>&1 \
    || warn "把 $c 接入网络 $D211_NETWORK 失败（若已接入可忽略）"
done

# =============================================================================
# 构建网关镜像（仓库 Dockerfile；基础镜像可切公网源）
# =============================================================================
if [[ ! -f "$D211_REPO_ROOT/Dockerfile" ]]; then
  die "未找到 Dockerfile：$D211_REPO_ROOT/Dockerfile（请用 --via 同步仓库或手动放置）"
fi
log "构建网关镜像 $D211_GW_IMAGE ..."
build_gateway_image() {
  local net_flag="${1:-}"
  (
    cd "$D211_REPO_ROOT"
    # shellcheck disable=SC2086
    docker build $net_flag \
      --build-arg "BASE_REGISTRY=${D211_BASE_REGISTRY:-docker.io/library}" \
      --build-arg "GO_BASE_IMAGE=${D211_GO_BASE_IMAGE:-golang:1.27.1-alpine}" \
      --build-arg "RUNTIME_BASE_IMAGE=${D211_RUNTIME_BASE_IMAGE:-alpine:3.22}" \
      -t "$D211_GW_IMAGE" -f Dockerfile .
  )
}
if ! build_gateway_image; then
  # 内网/NAT 下 docker build 默认桥接网络常常拉不动基础镜像与 go module；
  # 换宿主网络重试一次是这类环境的标准解法。
  warn "首次构建失败，用 --network=host 重试一次（内网/NAT 常见原因）"
  build_gateway_image "--network=host" || die "网关镜像构建失败（两种网络模式都试过了）"
fi

# =============================================================================
# 启动网关容器
# =============================================================================
SECRET_KEY="${LLM_GATEWAY_SECRET_KEY:-}"
if [[ -z "$SECRET_KEY" ]]; then
  if [[ -f "$D211_STATE_DIR/env/gateway.secret" ]]; then
    SECRET_KEY="$(cut -d= -f2- "$D211_STATE_DIR/env/gateway.secret")"
  else
    SECRET_KEY="$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')"
    printf 'LLM_GATEWAY_SECRET_KEY=%s\n' "$SECRET_KEY" > "$D211_STATE_DIR/env/gateway.secret"
    chmod 600 "$D211_STATE_DIR/env/gateway.secret"
  fi
fi

CORS_ORIGINS="${LLM_GATEWAY_CORS_ORIGINS:-*}"

# 凭据密文解密密钥（r1024 部署轮补）：211 的库是生产数据副本，credentials
# 密文按生产 keyring（kid=legacy）加密；不注入 CREDENTIAL_ENCRYPTION_KEY 时
# 网关回落到 SECRET_KEY 派生钥，解不开信封（node_probe "cannot decrypt:
# unknown format" + 路由全灭 key_decrypt_failed）。与 SECRET_KEY 同款持久化：
# 环境变量优先，其次 gateway.credkey 文件；不自动生成（错钥只会静默全灭）。
CRED_ENC_KEY="${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-}"
if [[ -z "$CRED_ENC_KEY" && -f "$D211_STATE_DIR/env/gateway.credkey" ]]; then
  CRED_ENC_KEY="$(cut -d= -f2- "$D211_STATE_DIR/env/gateway.credkey")"
fi
if [[ -n "$CRED_ENC_KEY" ]]; then
  printf 'LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=%s\n' "$CRED_ENC_KEY" > "$D211_STATE_DIR/env/gateway.credkey"
  chmod 600 "$D211_STATE_DIR/env/gateway.credkey"
fi

# 组装网关要用的连接串（走 docker 网络内的容器名）
# 密码必须 URL-encode（`@` / `:` / `/` 会拆断 DSN；`unused` 表示 trust 模式可不带）
url_encode() { printf '%s' "$1" | python3 -c "import sys, urllib.parse; print(urllib.parse.quote(sys.stdin.read(), safe=''))" 2>/dev/null || printf '%s' "$1"; }
PG_PASS_ENC="$(url_encode "$PG_PASS")"
REDIS_PASS_ENC="$(url_encode "${REDIS_PASSWORD:-}")"
# PG_USER 也得编码（acc-pg17 实际是 acc_super，正常无特殊字符，但保持一致）
PG_USER_ENC="$(url_encode "$PG_USER")"
# ⚠ 2026-10-13 真机第六轮翻车：网关 config.Redis 只认 ADDR/DB/PASSWORD 三段，
# 不认 LLM_GATEWAY_REDIS_URL（v2 旧版协议已废弃）。按 config.RedisAddr / RedisDB /
# RedisPassword 三个独立变量注入。env 文件里仍保留 LLM_GATEWAY_REDIS_URL 仅作
# 运维查看用。
REDIS_ADDR="${REDIS_CONTAINER}:6379"
if [[ -n "${REDIS_PASSWORD:-}" ]]; then
  REDIS_URL="redis://:${REDIS_PASS_ENC}@${REDIS_ADDR}/${REDIS_DB}"
else
  REDIS_URL="redis://${REDIS_ADDR}/${REDIS_DB}"
fi
DB_URL="postgres://${PG_USER_ENC}:${PG_PASS_ENC}@${PG_CONTAINER}:5432/${D211_PG_DB}?sslmode=disable"

# 这个 env 文件是「shell 可 source」的**持久记录**（供运维查、同步脚本读），
# 不是给 docker --env-file 用的：
#   · docker 的 env-file 解析器不剥 `export` 前缀 —— 一行
#     `export FOO=bar` 会被当成 key 叫 "export FOO" 的变量，全部设不进去；
#   · 也不认 %q 的 shell 转义（\?、空格转义等），会把值改写坏。
# 所以容器一律走 -e argv（引号由 bash 负责，不经任何二次解析）。
{
  printf '# llm-gateway-go @ 192.168.31.211（由 deploy-211-docker.sh 生成，勿提交）\n'
  printf '# shell 可 source；容器用 -e 注入，不走 docker 的 env 文件参数\n'
  printf 'export LLM_GATEWAY_STORAGE_MODE=full\n'
  printf 'export LLM_GATEWAY_DATABASE_URL=%q\n' "$DB_URL"
  # 网关 config.Redis 解析三段独立变量：ADDR/DB/PASSWORD
  printf 'export LLM_GATEWAY_REDIS_ADDR=%q\n' "$REDIS_ADDR"
  printf 'export LLM_GATEWAY_REDIS_DB=%q\n' "$REDIS_DB"
  printf 'export LLM_GATEWAY_REDIS_PASSWORD=%q\n' "${REDIS_PASSWORD:-}"
  # 兼容老格式：运维人看 env 也能直接拿到完整 DSN
  printf 'export LLM_GATEWAY_REDIS_URL=%q\n' "$REDIS_URL"
  printf 'export LLM_GATEWAY_SECRET_KEY=%q\n' "$SECRET_KEY"
  printf 'export LLM_GATEWAY_CORS_ORIGINS=%q\n' "$CORS_ORIGINS"
  if [[ -n "$CRED_ENC_KEY" ]]; then
    printf 'export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=%q\n' "$CRED_ENC_KEY"
  fi
} > "$ENV_FILE"
chmod 600 "$ENV_FILE"

GW_ENV=(
  -e "LLM_GATEWAY_STORAGE_MODE=full"
  -e "LLM_GATEWAY_DATABASE_URL=${DB_URL}"
  -e "LLM_GATEWAY_REDIS_ADDR=${REDIS_ADDR}"
  -e "LLM_GATEWAY_REDIS_DB=${REDIS_DB}"
  -e "LLM_GATEWAY_REDIS_PASSWORD=${REDIS_PASSWORD:-}"
  -e "LLM_GATEWAY_SECRET_KEY=${SECRET_KEY}"
  -e "LLM_GATEWAY_CORS_ORIGINS=${CORS_ORIGINS}"
)
if [[ -n "$CRED_ENC_KEY" ]]; then
  GW_ENV+=( -e "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=${CRED_ENC_KEY}" )
fi

docker rm -f "$D211_GW_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$D211_GW_CONTAINER" --restart unless-stopped \
  --network "$D211_NETWORK" \
  -p "${D211_GW_PORT}:8781" \
  "${GW_ENV[@]}" \
  "$D211_GW_IMAGE" >/dev/null || die "网关容器启动失败"

# =============================================================================
# 就绪验证
# =============================================================================
log "等待网关就绪 ..."
ready=0
for i in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${D211_GW_PORT}/readyz" >/dev/null 2>&1 \
     || curl -fsS "http://127.0.0.1:${D211_GW_PORT}/healthz" >/dev/null 2>&1; then
    ready=1; break
  fi
  sleep 2
done
if (( ready == 0 )); then
  warn "网关未在预期时间内就绪，最近日志："
  docker logs --tail 40 "$D211_GW_CONTAINER" >&2 || true
  die "网关部署失败"
fi

log "=========================================================="
log "部署完成 @ $D211_TARGET_IP"
log "  网关  : http://${D211_TARGET_IP}:${D211_GW_PORT}  (容器 $D211_GW_CONTAINER)"
log "  PG    : $PG_CONTAINER  ($( ((PG_REUSED)) && echo 复用 || echo 新建 ))  库=$D211_PG_DB"
log "  Redis : $REDIS_CONTAINER ($( ((REDIS_REUSED)) && echo 复用 || echo 新建 ))  库号=db${REDIS_DB} (从 ${D211_REDIS_DB_START} 倒排)"
log "  密钥  : $ENV_FILE (chmod 600，勿提交)"
log "=========================================================="
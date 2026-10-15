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

# ── 执行位置守卫（2026-10-15）────────────────────────────────────────
# 无参模式 = 「在 211 本机以 root 执行」：状态目录 /opt/llm-gateway-go-211、
# 本机 docker 容器探测、docker build、127.0.0.1:8781 就绪探测全是目标机
# 语境。在部署机（macOS）上裸跑会在 mkdir /opt/llm-gateway-go-211 处报
# Permission denied——那不是权限配置问题，是执行位置不对；而且就算 sudo
# 建出目录，后面操作的也是 Mac 自己的 docker 而不是 211 的。远程部署一律
# 走 --via（tar 同步仓库 → ssh 到目标机以 root 重跑本脚本）。
if [[ -z "$VIA" && "$(uname -s)" == "Darwin" ]]; then
  die "检测到在 macOS 上以本机模式运行。本脚本无参模式必须在 211 本机以 root 执行（状态目录 ${D211_STATE_DIR} 是 Linux 路径）；从部署机远程部署请用：bash $0 --via ${D211_TARGET_IP}（基础镜像可用 D211_GO_BASE_IMAGE/D211_RUNTIME_BASE_IMAGE 指向本地离线包）"
fi

# =============================================================================
# --via：把当前仓库同步到目标机并在目标机上执行本脚本
# =============================================================================
if [[ -n "$VIA" ]]; then
  [[ "$MODE" == "deploy" ]] || die "--via 只支持默认 deploy 模式（--status/--stop 请 ssh 后本地跑）"
  # ── 部署锁（deploy-lib lock.sh，与 deploy-seamless 同一族 SSOT）────────
  # --via 全程是部署机侧的连续 ssh 步骤（rm → tar 同步 → 清理 → 远端执行），
  # 锁必须在部署机侧包住全程：两个并发 --via 会在 rm -rf vs tar 解包处互相
  # 炸（2026-10-14/15 三次实测 "Directory not empty"/"Cannot mkdir"）。
  # 锁不在场（如 211 本机模式无 deploy-lib）时 warn 跳过，行为同旧版。
  _d211_lib_resolve="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/deploy-lib-resolve.sh"
  if [[ -f "$_d211_lib_resolve" ]]; then
    # shellcheck source=deploy-lib-resolve.sh
    source "$_d211_lib_resolve"
  fi
  if [[ -n "${AIAN_DEPLOY_LIB:-}" && -f "$AIAN_DEPLOY_LIB/lock.sh" ]]; then
    # shellcheck source=/dev/null
    source "$AIAN_DEPLOY_LIB/lock.sh"
    LOCK_LOCAL_TARGET=211
    if ! lock_acquire_local; then
      die "另一个 211 部署正在本机进行（锁：${LOCK_LOCAL_DIR}）。并发会话请等待其完成；确认无存活部署进程后可 rm -rf 该锁目录重试（勿在对方运行时硬抢）"
    fi
    trap 'lock_release_local' EXIT
    log "已取得 211 部署锁: ${LOCK_LOCAL_DIR}"
  else
    warn "deploy-lib lock.sh 不在场，本次无部署锁（并发部署可能互相踩）"
  fi
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
  # ⚠ 不要排除任何形态的 dist：macOS bsdtar 的 exclude 对无通配模式也按
  # 「任意尾部路径段」语义匹配（实测 --exclude=dist 与 --exclude=./dist 都会
  # 命中 web/dist），root 的 admin SPA 与 /m 移动端就没了——llm.itestu.cn
  # 打开只剩版本 JSON（2026-10-15 两轮实测）。目标树每次同步前整体 rm -rf
  # 重建，陈旧 dist 残留不成立；前端 dist 由部署机预构建后随树上树。
  # ⚠ 同样不要 --exclude=build：会误伤 vendor/.../api/types/build/*.go
  # （2026-10-15 12h 审计 P2）。仓库根无 build/ 目录；大缓存用 .build-local。
  # macOS bsdtar 默认把 quarantine/provenance xattr 打进 archive，GNU tar 刷屏
  # Ignoring unknown extended header；偶发半截同步 → 远端缺 scripts/（RC=127）。
  # COPYFILE_DISABLE + --no-xattrs/--no-mac-metadata（有则用）关掉资源叉/xattr。
  _tar_create=(tar -C "$D211_REPO_ROOT")
  if [[ "$(uname -s)" == "Darwin" ]]; then
    export COPYFILE_DISABLE=1
    _tar_create+=(--no-xattrs)
    tar --help 2>&1 | grep -q -- '--no-mac-metadata' && _tar_create+=(--no-mac-metadata)
  fi
  _tar_create+=(
    --exclude=.git --exclude=node_modules
    --exclude=.build-local
    --exclude='*.log' --exclude='.env' --exclude='.env.*'
    --exclude='._*' --exclude='.DS_Store'
    -cf - .
  )
  "${_tar_create[@]}" | ssh "$VIA" "tar --no-xattrs -C ~/llm-gateway-go-211 -xf - && \
        find ~/llm-gateway-go-211 -name '._*' -type f -delete && \
        find ~/llm-gateway-go-211 -name '.DS_Store' -type f -delete && \
        echo '[211] 同步后 ._* 残留: '\$(find ~/llm-gateway-go-211 -name '._*' -type f | wc -l)"
  # 双保险：目标机上再扫一遍，真有残留就当场删掉，别留给下一次构建
  ssh "$VIA" "find ~/llm-gateway-go-211 -maxdepth 2 -name '.env*' -type f -delete; \
             echo '[211] 目标树残留 .env*: '\$(find ~/llm-gateway-go-211 -name '.env*' -type f | wc -l) \
             ' ._*: '\$(find ~/llm-gateway-go-211 -name '._*' -type f | wc -l)"
  # 半截同步硬门：缺入口脚本就别往下跑（否则 bash 报 No such file → RC=127）
  ssh "$VIA" "test -f ~/llm-gateway-go-211/scripts/deploy-211-docker.sh" \
    || die "同步后远端缺少 scripts/deploy-211-docker.sh（半截 tar？）；拒跑部署"
  # 基础镜像 build-arg 变量一并转发：211 的 /opt/kaixuan/base-images 存有
  # 离线基础镜像（kx-base/golang:1.27-alpine-amd64 等），daemon 的 mirror 不
  # 保证每次可达，构建腿应优先吃本地已 load 的镜像。空值 = 远端走脚本默认。
  log "在 $VIA 上执行 deploy ..."
  ssh "$VIA" "cd ~/llm-gateway-go-211 && D211_STATE_DIR=$D211_STATE_DIR D211_REDIS_DB_START=$D211_REDIS_DB_START D211_REDIS_DB_END=$D211_REDIS_DB_END LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY='${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-}' D211_GO_BASE_IMAGE='${D211_GO_BASE_IMAGE:-}' D211_RUNTIME_BASE_IMAGE='${D211_RUNTIME_BASE_IMAGE:-}' D211_BASE_REGISTRY='${D211_BASE_REGISTRY:-}' PROBE_TIMEOUT_SECS='${PROBE_TIMEOUT_SECS:-}' bash scripts/deploy-211-docker.sh $*"
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

# ── 网关自占命名空间（封闭集，2026-10-14）────────────────────────────
# claim 复用判定的「自占」识别依据：网关的 Redis 键持久化在复用的实例里
# （nbjl-redis 开 AOF），容器重建后旧库的键还在。此前 allocate 只看 DBSIZE>0，
# 网关自己昨天留下的键让自己永远「占用」自己的库 → 每次 deploy 都下移一号
# （211 实测 15→14→…→5，即挂账的「claim 不粘」根因）。
# ⚠ 新增网关 Redis 键前缀时必须同步本集合，否则漂移复发。
GATEWAY_KEY_PREFIXES=("llmgw:" "session:" "requestjourney:" "ursm:")

# claim 库的采样里是否出现网关自己的键前缀（出现即视为自占，可安全复用）
redis_db_has_gateway_keys() {
  local c="$1" idx="$2" k p
  redis_auth_args
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    for p in "${GATEWAY_KEY_PREFIXES[@]}"; do
      case "$k" in "$p"*) return 0 ;; esac
    done
  done < <(docker exec "$c" redis-cli "${REDIS_AUTH[@]}" -n "$idx" --scan 2>/dev/null | head -50)
  return 1
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
  local c="$1" claimed
  if [[ -f "$CLAIM_FILE" ]]; then
    claimed="$(cat "$CLAIM_FILE" 2>/dev/null || true)"
    if [[ "$claimed" =~ ^[0-9]+$ ]]; then
      if ! redis_db_in_use "$c" "$claimed"; then
        log "复用已分配的 Redis 库号 db${claimed}（claim 文件持久化，避免重跑漂移）"
        printf '%s\n' "$claimed"; return 0
      fi
      if redis_db_has_gateway_keys "$c" "$claimed"; then
        # 库非空但采样全是网关自己的键（部署间隙的持久化残留）→ 自占，复用不漂移
        log "复用已分配的 Redis 库号 db${claimed}（库内为网关自身残留键，非他人占用）"
        printf '%s\n' "$claimed"; return 0
      fi
      warn "claim 里的 db${claimed} 被其它服务的键占用，重新分配"
    fi
  fi
  local idx
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

# URSM 专用 Redis db（与网关会话库隔离，避免 persist SCAN 扫会话键）。
# 允许调用方用 URSM_V2_REDIS_DB 覆盖；默认优先 14，跳过 REDIS_DB，找空库。
URSM_REDIS_DB="${URSM_V2_REDIS_DB:-}"
if [[ -z "$URSM_REDIS_DB" ]]; then
  for _cand in 14 13 12 11 10 9 8 7 6 5 4 3 2 1 0; do
    if [[ "$_cand" == "$REDIS_DB" ]]; then
      continue
    fi
    if ! redis_db_in_use "$REDIS_CONTAINER" "$_cand"; then
      URSM_REDIS_DB="$_cand"
      break
    fi
  done
fi
[[ -n "$URSM_REDIS_DB" ]] || die "无法为 URSM 分配独立 Redis db（与会话库 db${REDIS_DB} 冲突且无空库）"
log "URSM 专用 Redis 库号 db${URSM_REDIS_DB}（会话库 db${REDIS_DB}）"

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
  # ⚠ SECRET_KEY 决定 api_keys.key_hash 的 HMAC（domains/authentication/
  # verifier.go）。**换了库就得换对SECRET**：2026-10-14 实测，211 库为
  # mac mini 血统（生产 SECRET_KEY）时，用本机旧值/随机值 → 全部 key
  # invalid_key。持久文件 env/gateway.secret 是「这个库对应的秘钥」，
  # 与 DB 血统绑定，不要在换库时沿用旧值。
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
  # 2026-10-14：解 URSM ModeOff（SYSTEM_MONITOR 未设）+ persist SCAN 与会话键隔离。
  # URSM 专用 db 优先 14，且不得与网关会话 REDIS_DB 撞车；已被占用则降序找空库。
  printf 'export LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true\n'
  printf 'export URSM_V2_REDIS_DB=%q\n' "$URSM_REDIS_DB"
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
  -e "LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true"
  -e "URSM_V2_REDIS_DB=${URSM_REDIS_DB}"
)
if [[ -n "$CRED_ENC_KEY" ]]; then
  GW_ENV+=( -e "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=${CRED_ENC_KEY}" )
fi

docker rm -f "$D211_GW_CONTAINER" >/dev/null 2>&1 || true
# ── 状态目录出容器（2026-10-13，对齐 211 机 DEPLOY-STANDARDS 总纲）──────
# /app/data（keystore/hotzone/attachments/backups/model-quality）与 /app/logs
# 原先落在容器层——每次部署 docker rm -f 即静默清空（keystore 丢=api key
# 快照/凭据状态回落，hotzone 与模型质量观测归零）。按 211 机总纲「容器是
# 空壳，数据在宿主 /opt/kaixuan/data」改为 bind mount。宿主目录为空且旧
# 容器还在时，先 docker cp 导出一次（迁移；旧容器被 rm 后此步自然跳过）。
# DB（acc-pg17→/opt/kaixuan/data/pg/acc-pg17）与 Redis（→/opt/kaixuan/data/
# redis）按本脚本「优先复用」口径本就落在宿主 bind，不在本块范围。
# 属主对齐容器内运行用户（uid 65532=llmgw）。
D211_GW_DATA_ROOT="${D211_GW_DATA_ROOT:-/opt/kaixuan/data/llm-gateway}"
mkdir -p "$D211_GW_DATA_ROOT/data" "$D211_GW_DATA_ROOT/logs"
chown -R 65532:65532 "$D211_GW_DATA_ROOT" 2>/dev/null || true
# ── configs 随部署种子（2026-10-14）────────────────────────────────────
# 镜像不含 configs/（sensitive_patterns.yaml 缺失时网关回落内置脱敏集，
# 与宿主机部署行为不一致）；docker cp 进容器层则 docker rm -f 即丢。
# 改为宿主 bind（只读），每次部署以仓库文件为准覆盖种子。
# 用 D211_REPO_ROOT（脚本顶部已解析），禁止再 $(cd "$(dirname "$0")/..")——
# docker build 段可能已 cd 走 cwd，相对 scripts/.. 会 No such file（2026-10-15 实测）。
D211_CONFIGS_SRC="${D211_CONFIGS_SRC:-$D211_REPO_ROOT/configs}"
mkdir -p "$D211_GW_DATA_ROOT/configs"
for _cfg in sensitive_patterns.yaml sensitive_words.json; do
  if [[ -f "$D211_CONFIGS_SRC/$_cfg" ]]; then
    cp -f "$D211_CONFIGS_SRC/$_cfg" "$D211_GW_DATA_ROOT/configs/$_cfg"
  else
    echo "warning: $D211_CONFIGS_SRC/$_cfg 不存在，容器将回落内置默认集" >&2
  fi
done
chown -R 65532:65532 "$D211_GW_DATA_ROOT/configs" 2>/dev/null || true

# ── Web 前端随部署种子（2026-10-15）：root SPA + /m 移动端 + 版本元数据 ──
# 网关从 cfg.StaticDir（env LLM_GATEWAY_STATIC_DIR，默认探 cwd 下的
# web/dist/index.html）挂 admin SPA，从 MOBILE_WEB_DIST 挂 /m 移动端，
# 从 version.json 读版本元数据——三者镜像里都没有，root 只会回落版本 JSON。
# 与 configs 同款治理：宿主 bind 只读，每次部署以仓库（部署机预构建）为准
# 覆盖种子。
#
# 2026-10-15 审计 P1 收口：本段原先只 warn 不拒绝，搬运链路通了但「产物是新的 /
# 构建成功 / 缺失要失败」三件事一条没闭环，于是——
#   ① dist 缺失时 rm -rf 不执行，上一版 SPA 继续被挂载 = **无声前端回滚**；
#   ② dist 陈旧时照样种子化，部署照样绿；
#   ③ 就绪只探 /readyz 与 /healthz、不探 /，以上两类都不会让部署失败。
# deploy-local.sh 对同一形态是硬失败（2026-10-11 2538 白页事故收口）。此处对齐
# 同一口径，同时保留「纯 API 形态」逃生舱：D211_ALLOW_STALE_WEB=1。
D211_WEB_DIST_SRC="${D211_WEB_DIST_SRC:-$D211_REPO_ROOT/web/dist}"
D211_MOBILE_DIST_SRC="${D211_MOBILE_DIST_SRC:-$D211_REPO_ROOT/web-mobile/dist}"
D211_ALLOW_STALE_WEB="${D211_ALLOW_STALE_WEB:-0}"

# seed_web_dist <dist 目录> <源码目录> <标签>
# 种子前判新鲜度：dist/index.html 必须不早于源码树里最新的一个文件。
# 用 -newer 做「有没有更新过的源码」判定，不依赖固定构建序号。
seed_web_dist() {
  local _dist="$1" _src="$2" _label="$3"
  if [[ ! -f "$_dist/index.html" ]]; then
    # 上一轮种子仍在 ⇒ 沿用等于**无声前端回滚**：线上跑着旧 UI，运维以为刚发过版。
    # 这一档没有逃生舱——纯 API 形态的前提是「压根没有前端」，不是「前端是旧的」。
    if [[ -f "$D211_GW_DATA_ROOT/${_label}/dist/index.html" ]]; then
      die "缺少 ${_dist}，但上一轮种子仍在 ${D211_GW_DATA_ROOT}/${_label}/dist —— 直接沿用等于无声前端回滚。请构建前端（corepack pnpm install --frozen-lockfile && pnpm build）后再部署"
    fi
    if [[ "$D211_ALLOW_STALE_WEB" == "1" ]]; then
      warn "$_dist 缺失，按纯 API 形态继续（$D211_ALLOW_STALE_WEB=1）：管理页不可用"
      return 1
    fi
    die "缺少 $_dist/index.html，拒绝部署空前端（2026-10-11 2538 白页事故口径）。构建前端，或显式 D211_ALLOW_STALE_WEB=1 走纯 API 形态"
  fi
  if [[ "$D211_ALLOW_STALE_WEB" != "1" && -d "$_src" ]]; then
    local _newer
    # 只盯会影响 Vite 产物的源码：*.test/spec 不进 bundle，mtime 漂移不该拦部署
    # （2026-10-15：LiveRequestStreamV2.queueStreamNotice.test.ts 误杀整次 211 部署）。
    _newer="$(find "$_src" -type f \
      ! -name '*.test.ts' ! -name '*.test.tsx' ! -name '*.test.js' ! -name '*.test.jsx' \
      ! -name '*.spec.ts' ! -name '*.spec.tsx' ! -name '*.spec.js' ! -name '*.spec.jsx' \
      -newer "$_dist/index.html" -print -quit 2>/dev/null || true)"
    if [[ -n "$_newer" ]]; then
      die "$_dist 陈旧：$_newer 晚于 $_dist/index.html，拒绝部署陈旧前端（否则线上是旧 SPA 而部署显示成功）。重新构建前端，或显式 D211_ALLOW_STALE_WEB=1 走纯 API 形态"
    fi
  elif [[ "$D211_ALLOW_STALE_WEB" == "1" ]]; then
    warn "$_dist 存在但未通过新鲜度校验，按纯 API 形态继续（$D211_ALLOW_STALE_WEB=1）"
  fi
  return 0
}

seed_web_dist "$D211_WEB_DIST_SRC" "$D211_REPO_ROOT/web/src" "web" && GW_WEB_DIST_OK=1 || GW_WEB_DIST_OK=0
seed_web_dist "$D211_MOBILE_DIST_SRC" "$D211_REPO_ROOT/web-mobile/src" "web-mobile" && GW_MOBILE_DIST_OK=1 || GW_MOBILE_DIST_OK=0

mkdir -p "$D211_GW_DATA_ROOT/web" "$D211_GW_DATA_ROOT/web-mobile"
GW_MOUNTS=(
  -v "${D211_GW_DATA_ROOT}/data:/app/data"
  -v "${D211_GW_DATA_ROOT}/logs:/app/logs"
  -v "${D211_GW_DATA_ROOT}/configs:/app/configs:ro"
)
GW_WEB_ENV=()
if [[ "$GW_WEB_DIST_OK" == "1" ]]; then
  rm -rf "$D211_GW_DATA_ROOT/web/dist"
  cp -R "$D211_WEB_DIST_SRC" "$D211_GW_DATA_ROOT/web/dist"
  chown -R 65532:65532 "$D211_GW_DATA_ROOT/web" 2>/dev/null || true
  GW_MOUNTS+=( -v "${D211_GW_DATA_ROOT}/web/dist:/app/web/dist:ro" )
  GW_WEB_ENV+=( -e "LLM_GATEWAY_STATIC_DIR=/app/web/dist" )
  log "admin SPA 种子: web/dist → ${D211_GW_DATA_ROOT}/web/dist"
fi
if [[ "$GW_MOBILE_DIST_OK" == "1" ]]; then
  rm -rf "$D211_GW_DATA_ROOT/web-mobile/dist"
  cp -R "$D211_MOBILE_DIST_SRC" "$D211_GW_DATA_ROOT/web-mobile/dist"
  chown -R 65532:65532 "$D211_GW_DATA_ROOT/web-mobile" 2>/dev/null || true
  GW_MOUNTS+=( -v "${D211_GW_DATA_ROOT}/web-mobile/dist:/app/web-mobile/dist:ro" )
  GW_WEB_ENV+=( -e "MOBILE_WEB_DIST=/app/web-mobile/dist" )
  log "移动端 SPA 种子: web-mobile/dist → ${D211_GW_DATA_ROOT}/web-mobile/dist"
fi
if [[ -f "$D211_REPO_ROOT/version.json" ]]; then
  cp -f "$D211_REPO_ROOT/version.json" "$D211_GW_DATA_ROOT/version.json"
  chown 65532:65532 "$D211_GW_DATA_ROOT/version.json" 2>/dev/null || true
  GW_MOUNTS+=( -v "${D211_GW_DATA_ROOT}/version.json:/app/version.json:ro" )
  log "版本元数据种子: version.json（$(python3 -c "import json;print(json.load(open('$D211_GW_DATA_ROOT/version.json')).get('version','?'))" 2>/dev/null || echo '?')）"
else
  echo "warning: $D211_REPO_ROOT/version.json 缺失，/version 将显示 dev/unknown" >&2
fi
# ${arr[@]+…} 惯用法：GW_WEB_ENV 可能为空（dist 全缺），bash 3.2 在 set -u 下
# 直接 "${空数组[@]}" 会报 unbound variable
GW_ENV+=( ${GW_WEB_ENV[@]+"${GW_WEB_ENV[@]}"} )

# ── 迁移腿（2026-10-14 r1030）：网关容器启动前重放 DB 修订序列 ──────────
# deploy-seamless（245/154）自带 deploy_apply_pending_migrations；本脚本原先
# 缺这一腿——从别机 dump 恢复的库（如 acc-pg17 ← mac pg18 dump）停在旧迁移
# 位（实测 schema_migrations 止于 851），新网关 boot 探测 `_request_logs_hot`
# （迁移 860 改名后的家族名）失败 → 静默降级 lite（readyz 的 mode 字段是唯
# 一肉眼判据）。序列幂等（already applied 无操作、健康路径零 DDL），故每次
# 部署无脑重放是安全默认。
REVISION_SEQ="$(cd "$(dirname "$0")" && pwd)/apply-db-revision-sequence.sh"
if [[ -f "$REVISION_SEQ" ]]; then
  log "重放 DB 修订序列（幂等）→ ${PG_CONTAINER}/${D211_PG_DB}"
  DATABASE_URL="$DB_URL" \
  LLM_GATEWAY_PG_CONTAINER="$PG_CONTAINER" \
  LLM_GATEWAY_PG_USER="${LLM_GATEWAY_PG_USER:-$PG_USER}" \
  LLM_GATEWAY_PG_PASSWORD="$PG_PASS" \
  LLM_GATEWAY_PG_DATABASE="$D211_PG_DB" \
    bash "$REVISION_SEQ" || die "DB 修订序列失败——直接启动会降级 lite，先修库再部署"
else
  echo "warning: apply-db-revision-sequence.sh 不在场，跳过迁移重放" >&2
fi

docker run -d --name "$D211_GW_CONTAINER" --restart unless-stopped \
  --network "$D211_NETWORK" \
  -p "${D211_GW_PORT}:8781" \
  "${GW_MOUNTS[@]}" \
  "${GW_ENV[@]}" \
  "$D211_GW_IMAGE" >/dev/null || die "网关容器启动失败"

# =============================================================================
# 就绪验证
# =============================================================================
log "等待网关就绪 ..."
# PROBE_TIMEOUT_SECS 与 deploy-154/245 同一族标准（deploy-211.sh 入口默认
# export 120）。⚠ 必须带 :- 兜底：ssh 远端执行时该变量不一定穿过（bash 4.4+
# 的 set -u 会让未绑定变量进算术直接炸——2026-10-15 实测翻车点）；
# 2s 步进，轮次=超时/2，下限 1 防呆。
ready=0
probe_rounds=$(( ${PROBE_TIMEOUT_SECS:-120} / 2 )); (( probe_rounds >= 1 )) || probe_rounds=1
for i in $(seq 1 "$probe_rounds"); do
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
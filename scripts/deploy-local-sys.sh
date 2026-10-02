#!/usr/bin/env bash
# =============================================================================
# deploy-local-sys.sh — llm-gateway-go 本地系统化部署（gateway 装系统，非 Docker）
#
# 定位：与 scripts/deploy-local.sh（Docker 栈/发布束/cutover 流程）互补。
# 本脚本把 gateway 直接安装到本机系统上运行：
#   - gateway：原生二进制（Windows 下 gateway.exe，Linux 下 gateway），裸进程运行
#   - 前端：web/dist 构建后随安装目录提供（gateway 静态托管）
#   - 数据库：本机 Docker 中的 PostgreSQL 17（容器名 kx-pg17，
#     由 scripts/deploy-local-pg17-docker.sh 安装；Docker 由独立任务负责安装）
#
# 敏感信息策略（重要）：
#   所有敏感值（DB 密码、SECRET_KEY、CREDENTIAL_ENCRYPTION_KEY、ADMIN 密码）
#   只存在于环境变量中，来源是安装目录下的 env 文件（默认
#   C:\llm-gateway-go\config\gateway.env，ACL 限当前用户）。该文件在仓库外，
#   永不写入 git。首次 deploy 时自动生成强随机密钥并固定（不可变更，
#   变更会导致 admin 会话失效与 provider 凭据无法解密）。
#
# 用法：
#   bash scripts/deploy-local-sys.sh deploy    # 构建 + 安装 + 起服务 + 健康检查
#   bash scripts/deploy-local-sys.sh start     # 启动（加载 gateway.env）
#   bash scripts/deploy-local-sys.sh stop      # 停止
#   bash scripts/deploy-local-sys.sh status    # 状态
#   bash scripts/deploy-local-sys.sh logs      # 跟踪日志
#   bash scripts/deploy-local-sys.sh verify    # /healthz /readyz /version 探测
#
# 常用选项（deploy）：
#   --no-frontend        复用现有 web/dist，不重新构建前端
#   --skip-db            跳过数据库初始化/迁移（PG 未就绪时先装服务）
#   --install-root DIR   安装根目录（默认 C:\llm-gateway-go / Linux /opt/llm-gateway-go-sys）
# =============================================================================
set -euo pipefail

log()  { printf '[deploy-sys] %s\n' "$*"; }
warn() { printf '[deploy-sys] WARN: %s\n' "$*" >&2; }
die()  { printf '[deploy-sys] FATAL: %s\n' "$*" >&2; exit 64; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 默认值 ─────────────────────────────────────────────────────────
OS_NAME="$(uname -s)"
IS_WINDOWS=0
[[ "$OS_NAME" == MINGW* || "$OS_NAME" == MSYS* || "$OS_NAME" == CYGWIN* ]] && IS_WINDOWS=1

if (( IS_WINDOWS )); then
  DEFAULT_INSTALL_ROOT="C:\\llm-gateway-go"
else
  DEFAULT_INSTALL_ROOT="/opt/llm-gateway-go-sys"
fi
INSTALL_ROOT="${LLM_GATEWAY_SYS_INSTALL_ROOT:-$DEFAULT_INSTALL_ROOT}"

SERVICE_PORT="${LLM_GATEWAY_SYS_SERVICE_PORT:-8781}"

# 端口自动避让：8781 可能被既有占位服务（如 http.sys 上的 llm.itestu.cn
# placeholder）占用；被占时向上找第一个空闲端口，避免与既有基础设施冲突。
# 解析结果固化到 config/service.port：此后所有调用（status/verify/logs）读
# 固化值，避免"自己的 gateway 占着 8782 → 误判被占 → 漂移到 8783"。
# R29 审计修正：端口解析移到参数解析之后调用——PORT_PIN_FILE 随
# --install-root 重算（旧版 pin 恒落默认根），且只有变更型动作（deploy/
# start）探测并固化端口，只读动作（status/logs/verify/stop）不再有写副作用。
port_free() { ! netstat -ano 2>/dev/null | grep -q "[:.]${1}[[:space:]]"; }
resolve_service_port() {
  if [[ -f "$PORT_PIN_FILE" ]]; then
    SERVICE_PORT="$(cat "$PORT_PIN_FILE")"
    return
  fi
  case "$ACTION" in deploy|start) ;; *) return ;; esac
  if ! port_free "$SERVICE_PORT"; then
    if [[ -n "${LLM_GATEWAY_SYS_SERVICE_PORT:-}" ]]; then
      die "指定端口 ${SERVICE_PORT} 已被占用（LLM_GATEWAY_SYS_SERVICE_PORT 显式指定，不自动避让）"
    fi
    local candidate=$SERVICE_PORT
    while ! port_free "$candidate"; do candidate=$((candidate + 1)); done
    warn "端口 ${SERVICE_PORT} 被占用，自动改用 ${candidate}（固化到 $PORT_PIN_FILE；可用 LLM_GATEWAY_SYS_SERVICE_PORT 固定）"
    SERVICE_PORT="$candidate"
  fi
  mkdir -p "$(dirname "$PORT_PIN_FILE")" 2>/dev/null || true
  printf '%s' "$SERVICE_PORT" > "$PORT_PIN_FILE" 2>/dev/null || true
}
DB_HOST="${LLM_GATEWAY_PG_HOST:-127.0.0.1}"
DB_PORT="${LLM_GATEWAY_PG_PORT:-5432}"
DB_USER="${LLM_GATEWAY_PG_USER:-llm_gateway}"
DB_NAME="${LLM_GATEWAY_PG_DATABASE:-llm_gateway}"
PG_CONTAINER="${LLM_GATEWAY_PG_CONTAINER:-kx-pg17}"
ENV_DIR="$INSTALL_ROOT/config"
ENV_FILE="$ENV_DIR/gateway.env"
RUN_DIR="$INSTALL_ROOT/run"
LOG_DIR="$INSTALL_ROOT/logs"
BIN_DIR="$INSTALL_ROOT/bin"
WEB_DIR="$INSTALL_ROOT/web"
DATA_DIR="$INSTALL_ROOT/data"
PID_FILE="$RUN_DIR/gateway.pid"
PORT_PIN_FILE="$INSTALL_ROOT/config/service.port"

usage() { sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; }

# ── 参数解析 ───────────────────────────────────────────────────────
ACTION="deploy"
SKIP_FRONTEND=0
SKIP_DB=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    deploy|start|stop|status|logs|verify) ACTION="$1"; shift ;;
    --no-frontend) SKIP_FRONTEND=1; shift ;;
    --skip-db) SKIP_DB=1; shift ;;
    --install-root) INSTALL_ROOT="$2"; ENV_DIR="$INSTALL_ROOT/config"; ENV_FILE="$ENV_DIR/gateway.env";
                    RUN_DIR="$INSTALL_ROOT/run"; LOG_DIR="$INSTALL_ROOT/logs"; BIN_DIR="$INSTALL_ROOT/bin";
                    WEB_DIR="$INSTALL_ROOT/web"; DATA_DIR="$INSTALL_ROOT/data"; PID_FILE="$RUN_DIR/gateway.pid";
                    PORT_PIN_FILE="$INSTALL_ROOT/config/service.port"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

resolve_service_port
LISTEN="${LLM_GATEWAY_LISTEN:-:${SERVICE_PORT}}"

# ── 并发防护（R29 审计）──────────────────────────────────────────
# deploy 含完整 go build（分钟级）且会 stop/install/start、写 PID 与端口固化
# 文件；db-attach 定时任务与手工 deploy 并发时此前全裸奔。mkdir 原子锁跨
# 平台（Git Bash 未必有 flock），陈锁内含 holder pid 便于人工判断，EXIT
# trap 兜底清理。db-attach.sh 有独立锁并经子进程调用本脚本，不会递归抢锁。
LOCK_DIR="$RUN_DIR/.deploy.lock"
acquire_lock() {
  mkdir -p "$RUN_DIR" 2>/dev/null || true
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    local holder; holder="$(cat "$LOCK_DIR/pid" 2>/dev/null || echo '?')"
    die "另一个 deploy/start/stop 实例正在运行（lock=$LOCK_DIR holder_pid=$holder）；确认无并发后删除该目录重试"
  fi
  printf '%s' "$$" > "$LOCK_DIR/pid"
  # INT/TERM 先转成非零退出，让 EXIT trap 统一清锁（kill -9 无解；陈锁由
  # die 消息的恢复指引人工清理——手工 deploy 场景 fail-closed 优于自愈，
  # 自愈会重新打开并发部署窗口）。
  trap 'exit 130' INT
  trap 'exit 143' TERM
  trap 'rc=$?; rm -rf "$LOCK_DIR"; exit $rc' EXIT
}
case "$ACTION" in
  deploy|start|stop) acquire_lock ;;
esac

# ── 工具函数 ───────────────────────────────────────────────────────
to_win_path() { cygpath -w "$1" 2>/dev/null || echo "$1"; }

rand_secret() { # base64url 随机串，$1=字节数
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$1" | tr '+/' '-_' | tr -d '='
  else
    head -c "$1" /dev/urandom | base64 | tr '+/' '-_' | tr -d '='
  fi
}

# 密码等敏感信息不入仓库：优先读 pg17 脚本落盘的密钥文件
SECRETS_FILE="${LLM_GATEWAY_SYS_SECRETS_DIR:-$HOME/.llm-gateway-go}/pg17.env"

pg_password() {
  if [[ -n "${LLM_GATEWAY_PG_PASSWORD:-}" ]]; then printf '%s' "$LLM_GATEWAY_PG_PASSWORD"; return; fi
  if [[ -f "$SECRETS_FILE" ]]; then
    (set -a; source "$SECRETS_FILE"; printf '%s' "${LLM_GATEWAY_PG_PASSWORD:-}")
  fi
}

# ── 进程探测 ───────────────────────────────────────────────────────
gateway_pid() {
  [[ -f "$PID_FILE" ]] || return 1
  local pid
  # tr 防 CRLF：旧版 Windows 侧 Set-Content 写出 "PID\r\n"，\r 会让
  # tasklist 过滤器永不匹配（R29 审计）；写侧已改 WriteAllText，读侧兜底。
  pid="$(tr -d '[:space:]' < "$PID_FILE" 2>/dev/null)" || return 1
  [[ -n "$pid" ]] || return 1
  if (( IS_WINDOWS )); then
    tasklist //FI "PID eq $pid" 2>/dev/null | grep -qi gateway.exe || return 1
  else
    kill -0 "$pid" 2>/dev/null || return 1
  fi
  printf '%s' "$pid"
}

# ── deploy 前置：构建工具链 ────────────────────────────────────────
need_cmd() { command -v "$1" >/dev/null 2>&1 || die "缺少命令 $1 — 请先安装并加入 PATH"; }

build_backend() {
  need_cmd go
  local out="$INSTALL_ROOT/bin/gateway.new"
  mkdir -p "$INSTALL_ROOT/bin"
  rm -f "$out"
  # 首选 CGO_ENABLED=0 静态构建（快）；onnxruntime/sqlite 等 CGO 依赖会令其
  # 失败（"build constraints exclude all Go files"），此时回退 CGO=1
  #（本机需有 C 工具链，Windows 上为 mingw-w64 gcc）。
  log "构建 gateway（GOOS=$(go env GOHOSTOS) GOARCH=$(go env GOHOSTARCH), CGO=0 优先）..."
  if (cd "$PROJECT_ROOT" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$out" ./cmd/gateway) 2>"$LOG_DIR/build-cgo0.log"; then
    log "  CGO=0 静态构建成功"
  else
    warn "CGO=0 构建失败（预期内：CGO-only 依赖），回退 CGO=1（需要 C 编译器）..."
    need_cmd gcc
    if ! (cd "$PROJECT_ROOT" && CGO_ENABLED=1 go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$out" ./cmd/gateway) 2>"$LOG_DIR/build-cgo1.log"; then
      sed 's/^/    /' "$LOG_DIR/build-cgo1.log" >&2 || true
      die "gateway 构建失败；见 $LOG_DIR/build-cgo1.log"
    fi
    log "  CGO=1 构建成功"
  fi
  mv -f "$out" "$INSTALL_ROOT/bin/gateway$([[ $IS_WINDOWS == 1 ]] && printf '.exe' || true)"
}

# 前端包管理器：pnpm 官方 / npm 并行支持，单点决策见 scripts/lib/node-pm.sh。
ensure_pm() {
  need_cmd node
  if command -v pnpm >/dev/null 2>&1; then
    return 0
  fi
  # 没有 pnpm 但有 corepack 时，优先把官方包管理器拉起来而不是直接退 npm。
  if command -v corepack >/dev/null 2>&1; then
    log "启用 corepack pnpm（web/package.json 指定 pnpm@10.29.2）..."
    (cd "$PROJECT_ROOT/web" && corepack enable >/dev/null 2>&1 || true)
    (cd "$PROJECT_ROOT/web" && corepack prepare pnpm@10.29.2 --activate >/dev/null 2>&1 || true)
    command -v pnpm >/dev/null 2>&1 && return 0
  fi
  need_cmd npm
}

build_frontend() {
  (( SKIP_FRONTEND )) && { log "跳过前端构建（--no-frontend）"; return 0; }
  [[ -f "$PROJECT_ROOT/web/package.json" ]] || die "web/package.json 不存在"
  ensure_pm
  # 关键改动：过去是 `pnpm install --frozen-lockfile || npm install`。pnpm 因为
  # lockfile 与 package.json 不同步而**真实失败**时，会被 `||` 吞掉并静默降级
  # 成不带 --frozen-lockfile 的 npm install —— "该重新生成 lockfile"这个真错误
  # 被伪装成一次成功安装。现在只选一个包管理器，跑它，原样透传退出码。
  # shellcheck source=scripts/lib/node-pm.sh
  source "$PROJECT_ROOT/scripts/lib/node-pm.sh"
  PM="$(pm_resolve)"
  log "构建前端（$PM install + build）..."
  pm_install web >"$LOG_DIR/web-install.log" 2>&1 \
    || die "前端依赖安装失败（$PM）；见 $LOG_DIR/web-install.log"
  pm_run build web >"$LOG_DIR/web-build.log" 2>&1 \
    || die "前端构建失败（$PM）；见 $LOG_DIR/web-build.log"
  [[ -f "$PROJECT_ROOT/web/dist/index.html" ]] || die "web/dist/index.html 未生成"
  log "  前端构建完成 → web/dist"
}

# ── 数据库初始化（PG17 在本机 Docker）─────────────────────────────
psql_via_docker() { # stdin=SQL 或 -f 文件
  docker exec -i -e PGPASSWORD="$(pg_password)" "$PG_CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" "$@"
}

db_ready() {
  command -v docker >/dev/null 2>&1 || return 1
  docker inspect "$PG_CONTAINER" >/dev/null 2>&1 || return 1
  printf "SELECT 1;" | docker exec -i -e PGPASSWORD="$(pg_password)" "$PG_CONTAINER" \
    psql -X -Atqc "SELECT 1" -U "$DB_USER" -d "$DB_NAME" >/dev/null 2>&1
}

init_database() {
  if (( SKIP_DB )); then
    warn "--skip-db：跳过数据库初始化（gateway 将以 no-DB 降级模式启动）"
    return 0
  fi
  if ! db_ready; then
    warn "PostgreSQL 17（Docker 容器 $PG_CONTAINER）未就绪。"
    warn "请先完成 Docker 安装任务，再运行：bash scripts/deploy-local-pg17-docker.sh"
    warn "本次跳过数据库步骤，gateway 以 no-DB 降级模式启动（/healthz 仍可用）。"
    return 0
  fi
  log "数据库已就绪，初始化 schema（仅空库）..."
  local count
  count=$(printf "SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind IN ('r','p','v','m','S','f');" | psql_via_docker -Atq)
  if [[ "$count" != "0" ]]; then
    log "  库已有 schema（$count 个关系），跳过快照"
  else
    local f
    for f in 00-prereqs.sql 01-schema.sql 02-seed.sql; do
      log "  应用 sql/schema/$f ..."
      psql_via_docker -q -f - < "$PROJECT_ROOT/sql/schema/$f" >"$LOG_DIR/schema-$f.log" 2>&1 \
        || die "schema $f 应用失败；见 $LOG_DIR/schema-$f.log"
    done
  fi
  log "应用数据库修订序列..."
  if ! LLM_GATEWAY_PG_CONTAINER="$PG_CONTAINER" \
       LLM_GATEWAY_PG_USER="$DB_USER" \
       LLM_GATEWAY_PG_PASSWORD="$(pg_password)" \
       LLM_GATEWAY_PG_DATABASE="$DB_NAME" \
       DATABASE_URL="$DATABASE_URL" \
       bash "$PROJECT_ROOT/scripts/apply-db-revision-sequence.sh" >"$LOG_DIR/db-revision.log" 2>&1; then
    sed 's/^/    /' "$LOG_DIR/db-revision.log" >&2 || true
    die "数据库修订序列失败；见 $LOG_DIR/db-revision.log"
  fi
  log "运行 gateway migrate（幂等自愈迁移）..."
  ( export LLM_GATEWAY_DATABASE_URL="$DATABASE_URL" DATABASE_URL
    if [[ -f "$BIN_DIR/gateway$([[ $IS_WINDOWS == 1 ]] && printf '.exe' || true)" ]]; then
      "$BIN_DIR/gateway$([[ $IS_WINDOWS == 1 ]] && printf '.exe' || true)" migrate >"$LOG_DIR/gateway-migrate.log" 2>&1
    else
      (cd "$PROJECT_ROOT" && go run ./cmd/gateway migrate >"$LOG_DIR/gateway-migrate.log" 2>&1)
    fi
  ) || { sed 's/^/    /' "$LOG_DIR/gateway-migrate.log" >&2 || true; die "gateway migrate 失败；见 $LOG_DIR/gateway-migrate.log"; }
  log "  数据库初始化完成"
}

# ── 环境变量（敏感信息只进 env 文件，永不进仓库）──────────────────
write_env_file() {
  mkdir -p "$ENV_DIR"
  if [[ -f "$ENV_FILE" ]]; then
    log "环境文件已存在，保留既有密钥：$ENV_FILE"
    return 0
  fi
  log "生成环境文件（含新随机密钥，写入后固定不可变）：$ENV_FILE"
  local pg_pass db_url
  pg_pass="$(pg_password)"
  db_url="postgres://${DB_USER}:${pg_pass}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"
  cat > "$ENV_FILE" <<EOF
# llm-gateway-go 本机系统部署环境变量（由 deploy-local-sys.sh 生成）
# ⚠ 本文件含敏感信息：不要提交到任何仓库；保持 ACL 仅限当前用户。
# ⚠ SECRET_KEY / CREDENTIAL_ENCRYPTION_KEY 生成后必须固定：
#   - 变更 SECRET_KEY → 所有 admin 会话失效
#   - 变更 CREDENTIAL_ENCRYPTION_KEY → 数据库凭据密文全部无法解密（decrypt_failed）

export LLM_GATEWAY_LISTEN="${LISTEN}"
# NET-001 fail-closed：CORS 必须显式配置。本机部署仅放行同源 localhost。
export LLM_GATEWAY_CORS_ORIGINS="http://localhost:${SERVICE_PORT},http://127.0.0.1:${SERVICE_PORT}"
export LLM_GATEWAY_DATABASE_URL='${db_url}'
export LLM_GATEWAY_SECRET_KEY="$(rand_secret 32)"
export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="$(rand_secret 32)"
export LLM_GATEWAY_ADMIN_USER="admin"
export LLM_GATEWAY_ADMIN_PASSWORD="$(rand_secret 16)"
export LLM_GATEWAY_ADMIN_API_KEY="sk-admin-$(rand_secret 32)"

# 会话体落盘编码与保留窗口（容量门禁）
export LLM_GATEWAY_BODIES_CODEC="gzip"
export URSM_SNAPSHOT_RETENTION_DAYS="30"
export STAGE_EVENTS_RETENTION_DAYS="7"

# 附件与日志目录（安装目录内持久化）
export LLM_GATEWAY_ATTACHMENT_DIR="${DATA_DIR}/attachments"
export LLM_GATEWAY_LOG_FILE="${LOG_DIR}/gateway.log"
export LLM_GATEWAY_LOG_MAX_SIZE_MB="100"
export LLM_GATEWAY_LOG_MAX_BACKUPS="10"
export LLM_GATEWAY_LOG_MAX_AGE_DAYS="7"
export LLM_GATEWAY_LOG_COMPRESS="true"

# 运维节点标识
export OPS_NODE_REGION="local"
EOF
  chmod 600 "$ENV_FILE" 2>/dev/null || true
  if command -v icacls >/dev/null 2>&1; then
    icacls "$(to_win_path "$ENV_FILE")" /inheritance:r /grant:r "$USERNAME:F" >/dev/null 2>&1 || true
  fi
  log "  ⚠ ADMIN 初始密码只在本文件中：$(grep LLM_GATEWAY_ADMIN_PASSWORD "$ENV_FILE" | head -1 | sed 's/.*=//;s/"//g' | head -c 4)****（完整值查看 $ENV_FILE）"
}

# sync_db_url：PG17 就绪后回填带密码的 DATABASE_URL。
# 场景：首次 deploy 时 Docker/PG 尚未就绪（env 文件里的 URL 无密码），
# 待 pg17 容器建立后重跑 deploy，把 URL 原位更新为带密码版本（密钥不动）。
sync_db_url() {
  local pg_pass db_url
  pg_pass="$(pg_password)"
  [[ -n "$pg_pass" ]] || { warn "PG 密码不可得（Docker 未就绪且无 $SECRETS_FILE）；DATABASE_URL 保持无密码形式"; return 0; }
  db_url="postgres://${DB_USER}:${pg_pass}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"
  export DATABASE_URL="$db_url"
  [[ -f "$ENV_FILE" ]] || return 0
  if grep -q "export LLM_GATEWAY_DATABASE_URL='postgres://${DB_USER}:@\|export LLM_GATEWAY_DATABASE_URL='postgres://${DB_USER}:${pg_pass}@" "$ENV_FILE" 2>/dev/null; then
    local tmp
    tmp="$ENV_FILE.tmp.$$"
    sed "s|^export LLM_GATEWAY_DATABASE_URL=.*|export LLM_GATEWAY_DATABASE_URL='${db_url}'|" "$ENV_FILE" > "$tmp" \
      && mv -f "$tmp" "$ENV_FILE"
    log "DATABASE_URL 已同步（pg17 就绪，密码来自 $(basename "$SECRETS_FILE")/环境变量）"
  fi
}

# ── 安装布局 ───────────────────────────────────────────────────────
install_layout() {
  log "安装到 $INSTALL_ROOT ..."
  mkdir -p "$ENV_DIR" "$RUN_DIR" "$LOG_DIR" "$BIN_DIR" "$DATA_DIR/attachments" "$INSTALL_ROOT/backups"
  rm -rf "${WEB_DIR}.old"
  [[ -d "$WEB_DIR" ]] && mv "$WEB_DIR" "${WEB_DIR}.old" 2>/dev/null || true
  mkdir -p "$WEB_DIR"
  cp -r "$PROJECT_ROOT/web/dist/." "$WEB_DIR/"
  [[ -f "$WEB_DIR/index.html" ]] || die "web 安装不完整（缺 index.html）"
  [[ -d "${WEB_DIR}.old" ]] && rm -rf "${WEB_DIR}.old" || true
  write_env_file
  local ver
  ver="$(cat "$PROJECT_ROOT/VERSION" 2>/dev/null || printf 'dev')"
  printf '{"version":"%s","installed_at":"%s"}\n' "$ver" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$INSTALL_ROOT/version.json"
  log "  安装完成（VERSION=$ver）"
}

# ── 启停控制（裸进程，非 Docker）──────────────────────────────────
start_gateway() {
  local bin
  bin="$BIN_DIR/gateway$([[ $IS_WINDOWS == 1 ]] && printf '.exe' || true)"
  [[ -x "$bin" || -f "$bin" ]] || die "gateway 二进制不存在：$bin（先执行 deploy）"
  if pid="$(gateway_pid)"; then
    log "gateway 已在运行（PID $pid）"
    return 0
  fi
  mkdir -p "$RUN_DIR" "$LOG_DIR"
  [[ -f "$ENV_FILE" ]] || die "环境文件不存在：$ENV_FILE（先执行 deploy）"
  log "启动 gateway（listen ${LISTEN}）..."
  if (( IS_WINDOWS )); then
    # PowerShell Start-Process 脱离当前会话，终端关闭后进程存活
    powershell -NoProfile -Command "
      \$envf = '$(to_win_path "$ENV_FILE")';
      Get-Content \$envf | Where-Object { \$_ -match '^\s*export\s+' } | ForEach-Object {
        \$line = \$_ -replace '^\s*export\s+', '' ;
        \$k = (\$line -split '=', 2)[0].Trim();
        \$v = (\$line -split '=', 2)[1].Trim().Trim(\"'\").Trim('\"');
        Set-Item -Path (\"Env:\" + \$k) -Value \$v
      };
      Set-Location '$(to_win_path "$INSTALL_ROOT")';
      \$p = Start-Process -FilePath '$(to_win_path "$bin")' -WorkingDirectory '$(to_win_path "$INSTALL_ROOT")' -WindowStyle Hidden -PassThru -RedirectStandardOutput '$(to_win_path "$LOG_DIR/gateway-stdout.log")' -RedirectStandardError '$(to_win_path "$LOG_DIR/gateway-stderr.log")';
      [IO.File]::WriteAllText('$(to_win_path "$PID_FILE")', [string]\$p.Id)
    "
  else
    ( set -a; source "$ENV_FILE"; set +a
      cd "$INSTALL_ROOT"
      nohup "$bin" >>"$LOG_DIR/gateway-stdout.log" 2>>"$LOG_DIR/gateway-stderr.log" &
      printf '%s' "$!" > "$PID_FILE" )
  fi
  sleep 2
  if pid="$(gateway_pid)"; then
    log "  gateway 已启动（PID $pid）"
  else
    warn "  启动后未检测到进程，查看 $LOG_DIR/gateway-stderr.log"
    return 1
  fi
}

stop_gateway() {
  local pid
  if ! pid="$(gateway_pid)"; then
    log "gateway 未在运行"
    rm -f "$PID_FILE"
    return 0
  fi
  log "停止 gateway（PID $pid）..."
  if (( IS_WINDOWS )); then
    taskkill //PID "$pid" //F >/dev/null 2>&1 || true
  else
    kill "$pid" 2>/dev/null || true
    # 等待退出（R29 审计）：旧版 kill 后立即删 PID 文件，紧随的 start 可能
    # 因端口尚未释放而失败；最多等 5s，超时仅告警不阻断。
    local i
    for i in 1 2 3 4 5; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 1
    done
    kill -0 "$pid" 2>/dev/null && warn "  PID $pid 5s 后仍在运行（可能忽略 TERM），紧随的 start 可能因端口占用失败"
  fi
  rm -f "$PID_FILE"
  log "  已停止"
}

show_status() {
  if pid="$(gateway_pid)"; then
    log "gateway: 运行中（PID $pid, listen ${LISTEN}）"
  else
    log "gateway: 已停止"
  fi
  if db_ready; then
    log "database: PostgreSQL 17 容器 $PG_CONTAINER 就绪（${DB_USER}@${DB_NAME}:${DB_PORT}）"
  else
    log "database: 未就绪（容器 $PG_CONTAINER 不可用或未创建）"
  fi
  if curl -fsS --max-time 3 "http://127.0.0.1:${SERVICE_PORT}/healthz" >/dev/null 2>&1; then
    log "healthz: 200 OK"
  else
    log "healthz: 不可达"
  fi
}

verify_health() {
  log "健康探测 http://127.0.0.1:${SERVICE_PORT} ..."
  local i=0 timeout="${HEALTH_TIMEOUT:-60}"
  until curl -fsS --max-time 3 "http://127.0.0.1:${SERVICE_PORT}/healthz" >/dev/null 2>&1; do
    i=$((i + 2))
    (( i >= timeout )) && { warn "/healthz 在 ${timeout}s 内未返回 200"; return 1; }
    sleep 2
  done
  log "  /healthz: 200 OK"
  local readyz version_body
  readyz="$(curl -sS --max-time 5 "http://127.0.0.1:${SERVICE_PORT}/readyz" 2>&1 || true)"
  log "  /readyz: ${readyz:-<empty>}"
  if version_body="$(curl -fsS --max-time 5 "http://127.0.0.1:${SERVICE_PORT}/version" 2>&1)"; then
    log "  /version: $version_body"
  fi
  log "verify 通过"
}

show_logs() {
  tail -n 200 -f "$LOG_DIR/gateway.log" 2>/dev/null || tail -n 200 -f "$LOG_DIR/gateway-stderr.log" "$LOG_DIR/gateway-stdout.log"
}

# ── 主流程 ─────────────────────────────────────────────────────────
case "$ACTION" in
  deploy)
    mkdir -p "$LOG_DIR" "$RUN_DIR"
    log "=== llm-gateway-go 本地系统化部署（gateway 装系统，PG17 走本机 Docker）==="
    build_backend
    build_frontend
    stop_gateway || true
    install_layout
    sync_db_url
    init_database
    start_gateway || die "gateway 启动失败"
    verify_health || die "健康检查未通过（服务可能仍以降级模式运行，查看 $LOG_DIR）"
    log "=== 部署完成 ==="
    log "  管理后台: http://127.0.0.1:${SERVICE_PORT}/  （账号见 $ENV_FILE）"
    log "  环境文件: $ENV_FILE（敏感信息，勿外传）"
    log "  数据库:   PostgreSQL 17 @ 本机 Docker（容器 $PG_CONTAINER；Docker 未就绪时已跳过，就绪后重跑 deploy 即可回填）"
    ;;
  start)   start_gateway ;;
  stop)    stop_gateway ;;
  status)  show_status ;;
  logs)    show_logs ;;
  verify)  verify_health ;;
  *) usage; exit 64 ;;
esac

#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-seamless.sh — 原子符号链接无缝部署 (245 / 154)
#
# 激活仓库已有的 deploy-lib/host.sh 原子切换机制：
#   releases/<version>/  每次部署一个自洽 bundle (binary + web + 校验)
#   current → releases/<version>   原子 ln -sfn 切换
#   gateway / web / version.json → current/...  符号链接间接层
#
# 与老的 deploy-245.sh / deploy-154.sh (stop→mv→scp→start) 并存：
#   - 老脚本保留为 fallback，本脚本是其增量替代
#   - 停机窗口从 ~15-35s 缩短到 ~5-8s (单次 restart)
#   - 回滚从「找 .bak 文件手动 mv」变成一条命令切回任意 verified 版本
#   - 文件状态永不破损：上传到独立目录，校验通过才切换符号链接
#
# 用法:
#   bash scripts/deploy-seamless.sh deploy 245 --seq 1004   # 部署到 245
#   bash scripts/deploy-seamless.sh deploy 154 --seq 1005   # 部署到 154 (默认通过 252 跳板机)
#   bash scripts/deploy-seamless.sh deploy 154 --direct     # 部署到 154 (直连，跳过跳板机)
#   bash scripts/deploy-seamless.sh rollback 245            # 一键回滚
#   bash scripts/deploy-seamless.sh rollback 154
#   bash scripts/deploy-seamless.sh status 245              # 查看 releases
#   bash scripts/deploy-seamless.sh deploy 245 --no-frontend --seq 1004
#
# 154 SSH 连接策略:
#   默认: 通过 252 跳板机 (root@115.29.212.252) 连接，最稳定
#   --direct: 直连 47.97.111.154:25022（应急场景，如 252 不可达）
#
# 安全网:
#   - healthz 失败 → 自动 rollback 到上一个 verified 版本
#   - healthz 通过但 DB 未就绪 (503) → 同样自动 rollback
#   - adopt 步骤保留旧二进制为 releases/legacy-<ts>/ (verified=true)
#   - build_seq 单调递增 (245→1004, 154→1005)
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# source 共享库
# shellcheck source=deploy-lib/targets.sh
source "$SCRIPT_DIR/deploy-lib/targets.sh"
# shellcheck source=deploy-lib/ssh-retry.sh
source "$SCRIPT_DIR/deploy-lib/ssh-retry.sh"
# shellcheck source=deploy-lib/host.sh
source "$SCRIPT_DIR/deploy-lib/host.sh"
# shellcheck source=deploy-lib/post-deploy-verify.sh
source "$SCRIPT_DIR/deploy-lib/post-deploy-verify.sh"
# shellcheck source=deploy-lib/db-changelog.sh
source "$SCRIPT_DIR/deploy-lib/db-changelog.sh"

GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; RED=$'\033[0;31m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'
log()  { echo -e "${BLUE}[seamless]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }

# ── 参数解析 ────────────────────────────────────────────────────
ACTION="${1:-}"; TARGET="${2:-}"
SEQ_FLAG=""; SKIP_FRONTEND=false
shift 2 2>/dev/null || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --seq) SEQ_FLAG="--seq $2"; shift 2 ;;
    --no-frontend) SKIP_FRONTEND=true; shift ;;
    --direct) export SSH_RETRY_DIRECT_MODE=1; shift ;;
    --ssh-retries) export SSH_RETRY_MAX=$2; shift 2 ;;
    --ssh-verbose) export SSH_RETRY_VERBOSE=1; shift ;;
    -h|--help)
      sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) err "未知参数: $1"; exit 1 ;;
  esac
done

[[ -n "$ACTION" ]] || { err "用法: deploy-seamless.sh <deploy|rollback|status> <245|154> [--seq N] [--ssh-retries N]"; exit 1; }
[[ -n "$TARGET" ]] || { err "缺少目标 (245|154)"; exit 1; }

case "$TARGET" in
  154|245) ;;
  *) err "不支持的目标: $TARGET (仅 154|245)"; exit 1 ;;
esac

# Load the shared envs SSOT before resolving SSH keys or target settings.
ENVS_ROOT="${ENVS_ROOT:-${HOME}/workspace/ai-native-tools/envs}"
case "$TARGET" in
  245) ENV_SERVER="8.136.114.245" ;;
  154) ENV_SERVER="47.97.111.154" ;;
esac
if [[ ! -f "$ENVS_ROOT/loader.sh" ]]; then
  err "envs SSOT loader not found: $ENVS_ROOT/loader.sh"
  exit 1
fi
# shellcheck disable=SC1090
source "$ENVS_ROOT/loader.sh" --all --project llm-gateway-go --server "$ENV_SERVER"

# ── SSH 命令构造 (2026-07-16: 走 ssh-retry.sh) ─────────────────
# 关键设计：host.sh 内部用 "$ssh_cmd" "remote-shell-cmd" 调用。
# 我们包装两个函数 remote_ssh / remote_ssh_pipe, 内部走 ssh-retry
# (ControlMaster 连接复用 + 指数退避重试). ssh-retry.sh 在 source 时
# 已安装 EXIT trap 自动清理 master socket.
#
# 154 公网 IP 47.97.111.154 偶发抖动 — 加 fallback via 252.
# 通过 ProxyCommand 实现: 本机 ssh → 252:25022 → 154.
# ssh-retry 会先直连, 失败 N 次后切到 ProxyCommand 路径.
SSH_PORT="${LLM_GATEWAY_SSH_PORT:-${SSH_PORT:-25022}}"
case "$TARGET" in
  245) SSH_KEY_FILE="${SSH_KEY_245:-${SSH_KEY_FILE:-}}" ;;
  154) SSH_KEY_FILE="${SSH_KEY_154:-${SSH_KEY_FILE:-}}" ;;
esac
if [[ -z "$SSH_KEY_FILE" || ! -f "$SSH_KEY_FILE" ]]; then
  err "missing injected SSH key for target $TARGET"
  exit 1
fi
export SSH_PORT SSH_KEY_FILE

SSH_HOST_CACHE=()
ssh_retry_init "$TARGET"

# host.sh 函数签名要求 ssh_cmd 是一个「可被 "$ssh_cmd" 调用的名字」。
# 我们定义 wrapper, 内部调 ssh_run <target> <cmd>。
remote_ssh() {
  ssh_run "$TARGET" "$1"
}
remote_ssh_pipe() {
  ssh_run_pipe "$TARGET" "$1"
}
SSH_CMD="remote_ssh"

# upload 不用 scp，用 tar 管道走 ssh (单连接，更可靠)。
REMOTE_ROOT=$(host_root_for "$TARGET")
SERVICE_NAME=$(target_field "$TARGET" service_name)
HEALTH_URL=$(target_field "$TARGET" health_url)
BIN_NAME=$(host_binary_name "$TARGET")

# 154 用 /etc/llm-gateway-go/env；245 用 /opt/llm-gateway-go/.env
_env_file_for_target() {
  case "$TARGET" in
    154) printf '%s\n' "/etc/llm-gateway-go/env" ;;
    245) printf '%s\n' "/opt/llm-gateway-go/.env" ;;
    *)   printf '%s\n' "/etc/llm-gateway-go/env" ;;
  esac
}

# healthz 或 DB 校验失败时回滚到上一个 verified 版本
_seamless_auto_rollback() {
  local reason=$1 failed_version=$2
  err "$reason — 自动回滚..."
  local prev
  prev=$(host_select_rollback_target "$SSH_CMD" "$TARGET" "$failed_version" 2>/dev/null || true)
  if [[ -n "$prev" ]]; then
    warn "回滚到 releases/$prev"
    host_atomic_switch "$SSH_CMD" "$TARGET" "$prev" 2>&1 | sed 's/^/    /' || true
    # 2026-07-27: 30s → 90s,与主 deploy 流程一致(回滚后 ApplyMigrations 仍需 ~30s+)
    if host_wait_healthy "$SSH_CMD" "$TARGET" 90 2>&1 \
      && deploy_verify_gateway_ready "$SSH_CMD" "$SERVICE_NAME" 8781 90; then
      ok "已回滚到 $prev (healthz + DB OK)"
      return 0
    fi
    err "回滚后 healthz/DB 仍失败!"
    return 1
  fi
  err "无可用回滚目标! 手动检查: $SSH_CMD 'systemctl status $SERVICE_NAME'"
  return 1
}

# ── 子命令: status ─────────────────────────────────────────────
do_status() {
  log "目标 $TARGET 的 release 状态:"
  echo ""
  $SSH_CMD "cd '$REMOTE_ROOT' 2>/dev/null && {
    echo '当前 current 指向:'; readlink current 2>/dev/null || echo '  (无 current 符号链接 — 未 adopt)';
    echo '';
    echo '已部署的 releases:';
    if [ -d releases ]; then
      for d in releases/*/; do
        [ -d \"\$d\" ] || continue;
        v=\$(basename \"\$d\");
        verified='?';
        [ -f \"\$d/deployment.json\" ] && grep -q '\"verified\":true' \"\$d/deployment.json\" && verified='✓verified' || verified='unverified';
        printf '  %-30s %s\\n' \"\$v\" \"\$verified\";
      done;
    else echo '  (无 releases 目录)'; fi;
    echo '';
    echo '符号链接:';
    ls -la current ${BIN_NAME} web version.json 2>/dev/null | grep -- '->' || echo '  (无符号链接)';
  }" 2>&1 || err "无法读取 $TARGET 状态"
}

# ── adopt: 把扁平布局纳入 releases/ 体系 ────────────────────────
# 首次无缝部署时调用。把当前运行的二进制 + web 复制到
# releases/legacy-<ts>/，标记 verified=true，建 current 符号链接。
# 之后部署走纯符号链接。旧扁平文件保留不动 (fallback)。
do_adopt() {
  local ts legacy_dir
  ts=$(date +%Y%m%d-%H%M%S)
  legacy_dir="$REMOTE_ROOT/releases/legacy-$ts"

  log "[adopt] 检测到 $TARGET 未采用 releases/ 布局，执行一次性 adopt..."

  # 1. 建 legacy bundle。注意：不用 set -e (cp web 大目录部分失败可容忍)。
  #    用 heredoc 写 deployment.json 避免转义地狱。
  $SSH_CMD "
    mkdir -p '$legacy_dir/web'
    # 复制当前二进制。245 是实文件 gateway；154 是符号链接 llm-gateway-go。
    # cp -aL 解引用符号链接拿到底层实文件。失败则尝试版本化文件名。
    if cp -aL '$REMOTE_ROOT/$BIN_NAME' '$legacy_dir/$BIN_NAME' 2>/dev/null; then
      :
    else
      # 154: llm-gateway-go -> llm-gateway-go.v*.linux.amd64
      for f in '$REMOTE_ROOT'/llm-gateway-go.v*.linux.amd64; do
        [ -f \"\$f\" ] || continue
        cp -a \"\$f\" '$legacy_dir/$BIN_NAME' && break
      done
    fi
    chmod +x '$legacy_dir/$BIN_NAME' 2>/dev/null || true
    # 复制 web (展平)。部分失败可容忍 (web 可能很大)。
    cp -a '$REMOTE_ROOT/web/.' '$legacy_dir/web/' 2>/dev/null || true
    # version.json
    cp -a '$REMOTE_ROOT/version.json' '$legacy_dir/version.json' 2>/dev/null || true
    # 修正属主 (tar 上传可能带来非 root 属主)
    chown -R root:root '$legacy_dir' 2>/dev/null || true
    # 校验和 + deployment.json (verified=true，让 host_select_rollback_target 能选到它)
    ( cd '$legacy_dir' && sha256sum '$BIN_NAME' version.json 2>/dev/null > SHA256SUMS || true )
    NOW=\$(date -u +%Y-%m-%dT%H:%M:%SZ)
    printf '{\"target\":\"$TARGET\",\"version\":\"legacy-$ts\",\"verified\":true,\"verified_at\":\"%s\",\"adopted\":true}\n' \"\$NOW\" > '$legacy_dir/deployment.json'
    # 确认 deployment.json 写入成功 (调试用)
    [ -s '$legacy_dir/deployment.json' ] || echo 'adopt: WARN deployment.json empty'
  " || { err "adopt: 复制旧二进制失败"; return 1; }

  # 2. 建 current → legacy，再建符号链接 (binary/web/version.json)
  $SSH_CMD "
    cd '$REMOTE_ROOT'
    # 备份原扁平文件/符号链接 (不删除，保留为终极 fallback)
    if [ -e '$BIN_NAME' ] && [ ! -L '$BIN_NAME' ]; then
      mv '$BIN_NAME' '${BIN_NAME}.pre-adopt-$ts'
    elif [ -L '$BIN_NAME' ]; then
      rm -f '$BIN_NAME'
    fi
    if [ -e 'web' ] && [ ! -L 'web' ]; then
      mv 'web' 'web.pre-adopt-$ts'
    fi
    ln -sfn '$legacy_dir' current
    ln -sfn 'current/$BIN_NAME' '$BIN_NAME'
    ln -sfn 'current/web' web
    [ -f 'current/version.json' ] && ln -sfn 'current/version.json' version.json 2>/dev/null || true
  " || { err "adopt: 建符号链接失败"; return 1; }

  ok "adopt 完成: 旧版本纳入 releases/legacy-$ts/ (verified=true，可回滚兜底)"
  return 0
}

# ── 安全 prune: 保留所有 verified + active，仅清理 unverified 失败尝试 ──
# 不用 host_prune_releases (timestamp 排序对 adopted/legacy 有 bug)。
prune_releases_safe() {
  local active_version=$1
  # 远端脚本：遍历 releases/*/，保留 verified=true 或 == active 的；unverified 保留最新 2 个
  remote_ssh "
    cd '$REMOTE_ROOT/releases' 2>/dev/null || exit 0
    active='$active_version'
    keep_unverified=2
    # 收集 unverified (按 mtime 降序)，保留最新 keep_unverified 个，其余删除
    unverified_list=\$(for d in */; do
      [ -d \"\$d\" ] || continue
      v=\$(basename \"\$d\")
      [ \"\$v\" = \"\$active\" ] && continue
      meta=\"\${d}deployment.json\"
      [ -f \"\$meta\" ] || continue
      grep -q '\"verified\":true' \"\$meta\" 2>/dev/null && continue
      echo \"\$(stat -c %Y \"\$d\" 2>/dev/null || echo 0) \$v\"
    done | sort -rn | awk 'NR>\"\$keep_unverified\"{print \$2}')
    [ -n \"\$unverified_list\" ] && echo \"\$unverified_list\" | while read v; do
      [ -n \"\$v\" ] && rm -rf \"\$v\" && echo \"pruned unverified: \$v\"
    done
  " 2>&1 | sed 's/^/    /' || true
}

# ── 上传 release bundle ────────────────────────────────────────
upload_release() {
  local bundle_dir=$1 version=$2
  local release_dir="$REMOTE_ROOT/releases/$version"
  log "[upload] tar pipe bundle → $TARGET:$release_dir"
  # 远端先建目录 (避免锁竞争)
  remote_ssh "mkdir -p '$release_dir'" || { err "mkdir releases 失败"; return 1; }
  # tar 管道上传整个 bundle (单 ssh 连道，--no-xattrs 抑制 macOS xattr 警告)
  tar czf - --no-xattrs -C "$bundle_dir" . | remote_ssh_pipe "tar xzf - -C '$release_dir'" || {
    err "tar 管道上传失败"; return 1; }
  # 修正属主为 root (tar 会保留本地 UID 501，导致 systemd 读不到)
  remote_ssh "chown -R root:root '$release_dir'" || true
  ok "bundle 上传完成"
}

# ── 子命令: deploy ─────────────────────────────────────────────
do_deploy() {
  local version bundle_dir deploy_start
  deploy_start=$(date +%s)

  log "[0/9] 部署前 PG 预检"
  deploy_preflight_pg_from_remote_env "$SSH_CMD" "$(_env_file_for_target)" || exit 2

  log "[0.5/9] 运维节点 env (OPS_NODE_REGION)"
  bash "$SCRIPT_DIR/ops/ensure-ops-node-env.sh" "$TARGET" || warn "OPS_NODE_REGION 设置失败，继续部署"

  if [[ "$TARGET" == "245" ]]; then
    if ! $SSH_CMD "grep -q '^LLM_GATEWAY_ADMIN_USER=' '$(_env_file_for_target)' && grep -q '^LLM_GATEWAY_ADMIN_PASSWORD=' '$(_env_file_for_target)'" 2>/dev/null; then
      warn "245 .env 缺少 ADMIN_USER/PASSWORD → 从 154 同步"
      bash "$SCRIPT_DIR/ops/sync-245-env-from-154.sh" || warn "245 env 同步失败，继续部署"
    fi
  fi

  # 0.8 确保 IR 默认路径开启（spec §10.4.1）
  log "[0.8/9] 确保 TRANSPORT_LAYER_IR_ENABLED=true (spec §10.4.1)"
  local _env_file
  _env_file=$(_env_file_for_target)
  if $SSH_CMD "grep -q '^TRANSPORT_LAYER_IR_ENABLED=true' '$_env_file'" 2>/dev/null; then
    ok "IR 已在 env 中启用"
  else
    # 移除可能的旧 false 行，追加 true
    $SSH_CMD "sed -i '/^TRANSPORT_LAYER_IR_ENABLED=/d' '$_env_file' && echo 'TRANSPORT_LAYER_IR_ENABLED=true' >> '$_env_file'" \
      && ok "IR 已注入 env (TRANSPORT_LAYER_IR_ENABLED=true)" \
      || warn "IR env 注入失败（可手动: echo TRANSPORT_LAYER_IR_ENABLED=true >> $_env_file）"
  fi

  # 1. bump version
  if [[ -n "$SEQ_FLAG" ]]; then
    log "[1/9] bump version $SEQ_FLAG"
    bash "$SCRIPT_DIR/bump-version.sh" $SEQ_FLAG 2>&1 | sed 's/^/    /'
  else
    log "[1/9] bump version (auto +1)"
    bash "$SCRIPT_DIR/bump-version.sh" 2>&1 | sed 's/^/    /'
  fi
  version=$(python3 -c "import json;d=json.load(open('version.json'));print(f\"{d['build_seq']}-{d['git_sha'][:8]}\")")
  local full_version="v$(python3 -c "import json;print(json.load(open('version.json'))['version'])")"
  local seq_val=$(python3 -c "import json;print(json.load(open('version.json'))['build_seq'])")
  ok "version=$full_version seq=$seq_val"

  # 2–3. 前端 + 后端并行构建（默认同时部署前后端）
  log "[2/9] 前端 + 后端并行构建"
  local tmpbin="/tmp/__seamless_${TARGET}_binary"
  local fe_pid=""
  if [[ "$SKIP_FRONTEND" == "false" ]]; then
    (cd web && npm run build 2>&1 | tail -5) &
    fe_pid=$!
  else
    warn "跳过前端 (--no-frontend)，仅更新二进制"
  fi
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
    -o "$tmpbin" ./cmd/gateway &
  local go_pid=$!
  [[ -n "$fe_pid" ]] && wait "$fe_pid" && ok "web/dist 已生成"
  wait "$go_pid"
  ok "编译完成 ($(du -h "$tmpbin" | cut -f1))"

  # 4. stage bundle (本地)
  log "[4/9] stage release bundle"
  bundle_dir="/tmp/seamless-release-${TARGET}-${seq_val}"
  rm -rf "$bundle_dir"; mkdir -p "$bundle_dir/web"
  HOST_STAGE_TARGET="$TARGET" HOST_STAGE_VERSION="$version" \
    host_stage_release "$bundle_dir" "$tmpbin" "web/dist" 2>&1 | sed 's/^/    /' || true
  ok "bundle: $bundle_dir"

  # 5. upload
  log "[5/9] upload → $TARGET"
  upload_release "$bundle_dir" "$version" || { err "上传失败，中止"; exit 1; }

  # 6. verify (远端 sha256)
  log "[6/9] verify bundle (sha256)"
  HOST_STAGE_TARGET="$TARGET" host_verify_bundle "$SSH_CMD" "$REMOTE_ROOT/releases/$version" \
    && ok "校验通过" || { err "校验失败，中止 (bundle 保留在 releases/$version/)"; exit 1; }

  # 6.5 切换前 DB 迁移 + changelog（缩短 restart 后 EnsureSchema 等待）
  log "[6.5/9] 切换前 pending 迁移 + db-changelog"
  deploy_apply_pending_migrations "$SSH_CMD" "$(_env_file_for_target)" "$TARGET" "$seq_val" "$(git rev-parse --short HEAD)" \
    || { err "DB 迁移失败，中止（未切换符号链接）"; exit 1; }

  # 7. adopt 检测
  log "[7/9] adopt 检测"
  if ! $SSH_CMD "test -L '$REMOTE_ROOT/current'" 2>/dev/null; then
    warn "current 符号链接不存在 → 执行 adopt"
    do_adopt || { err "adopt 失败，中止"; exit 1; }
  else
    ok "已采用 releases/ 布局"
  fi

  # 8. atomic switch + restart（无感切换窗口 ≈ restart 耗时）
  log "[8/9] 原子符号链接切换 + restart"
  local switch_start switch_end switch_elapsed
  switch_start=$(date +%s)
  host_atomic_switch "$SSH_CMD" "$TARGET" "$version" 2>&1 | sed 's/^/    /' || true
  switch_end=$(date +%s)
  switch_elapsed=$((switch_end - switch_start))
  ok "符号链接已切换 (${switch_elapsed}s 含 restart)"

  # 9. wait healthy + DB ready (失败自动回滚)
  # 2026-07-27: healthz 超时 30s → 90s。ApplyMigrations 在 252 PG 锁竞争 / 慢盘
  # 下可达 60-90s (cmd/gateway/main.go:db.Open → db.ApplyMigrations),30s 必超时
  # 误判失败,导致自动回滚到 verified 版本时再次超时(同 PG 状态)。
  # 90s 与 deploy_verify_gateway_ready 90s、rollback 60s 顶部对齐。
  log "[9/9] 验证 /healthz + DB (healthz 90s, DB 90s)"
  if host_wait_healthy "$SSH_CMD" "$TARGET" 90 2>&1; then
    if deploy_verify_gateway_ready "$SSH_CMD" "$SERVICE_NAME" 8781 90; then
      host_mark_verified "$SSH_CMD" "$TARGET" "$version" 2>&1 | sed 's/^/    /' || true
      ok "healthz + DB 通过，标记 verified"
    else
      _seamless_auto_rollback "DB 未就绪 (database not configured 风险)" "$version" || true
      exit 1
    fi
  else
    _seamless_auto_rollback "healthz 超时" "$version" || true
    exit 1
  fi

  # 9.5 可选：同步 env admin 密码到 users 表（避免 JWT 与 env 漂移）
  if [[ "${DEPLOY_SYNC_ADMIN_PASSWORD:-true}" == "true" ]]; then
    log "[9.5/9] 同步 admin 密码 (env → users)"
    if bash "$SCRIPT_DIR/ops/sync-admin-password-from-env.sh" "$TARGET"; then
      ok "admin 密码已同步"
    else
      warn "admin 密码同步失败（不影响部署，可手动: bash scripts/ops/sync-admin-password-from-env.sh ${TARGET}）"
    fi
  fi

  # 9.6 安装日志轮转配置 (按 systemd unit 模式自动分支)
  # 必做：服务已 healthz OK，再装轮转即便失败也不影响 deploy。
  #   - StandardOutput=append:/var/log/... → logrotate + copytruncate (245)
  #   - StandardOutput=journal / StandardError=inherit → systemd-journald drop-in (154)
  #   - 其它 → warn skip (无标准轮转路径)
  log "[9.6/9] 配置 stderr/stdout 日志轮转 (按 unit 模式自动分支)"
  local _std_out _std_err _target_rotate
  _std_out="$(remote_ssh "systemctl show $SERVICE_NAME --property=StandardOutput --value" 2>/dev/null | tail -1 || echo '')"
  _std_err="$(remote_ssh "systemctl show $SERVICE_NAME --property=StandardError --value" 2>/dev/null | tail -1 || echo '')"
  info "  unit mode: StandardOutput=${_std_out:-<unset>} StandardError=${_std_err:-<unset>}"
  # decide: append: → logrotate; journal/inherit → journald drop-in; else skip
  case "${_std_out}:${_std_err}" in
    append:*:*) _target_rotate="logrotate" ;;
    *:append:*) _target_rotate="logrotate" ;;
    journal:*|*:journal|journal:*|*:inherit|*:*:inherit)
      # journal:inherit / journal:journal / inherit:* / inherit:inherit / 空 → journald
      _target_rotate="journald" ;;
    *)
      warn "  未识别的 unit 模式 (${_std_out:-<unset>}:${_std_err:-<unset>}), 跳过轮转配置"
      _target_rotate="" ;;
  esac

  if [[ "$_target_rotate" == "logrotate" ]]; then
    # ── logrotate 路径 (245 + 未来改 unit 走 append: 的服务器) ──
    local lg_remote_dir="/tmp/llm-gw-deploy-helpers"
    local lg_remote_cfg="$lg_remote_dir/llm-gateway-go.logrotate"
    local lg_remote_script="$lg_remote_dir/install-logrotate.sh"
    local lg_local_cfg="$PROJECT_ROOT/deploy/logrotate-llm-gateway-go"
    local lg_local_script="$SCRIPT_DIR/install-logrotate.sh"
    if [[ -f "$lg_local_cfg" && -f "$lg_local_script" ]]; then
      if cat "$lg_local_cfg" | remote_ssh_pipe "mkdir -p '$lg_remote_dir' && cat > '$lg_remote_cfg'" \
         && cat "$lg_local_script" | remote_ssh_pipe "cat > '$lg_remote_script'"; then
        if remote_ssh "chmod +x '$lg_remote_script' && bash '$lg_remote_script' install '$lg_remote_cfg'"; then
          ok "logrotate 配置已就绪"
        else
          warn "logrotate install 失败（不影响 deploy, 可手动: ssh $TARGET 'bash $lg_remote_script install $lg_remote_cfg'）"
        fi
      else
        warn "logrotate 文件传输失败（不影响 deploy）"
      fi
    else
      warn "logrotate 资源缺失: $lg_local_cfg 或 $lg_local_script 不存在"
    fi
  elif [[ "$_target_rotate" == "journald" ]]; then
    # ── systemd-journald 路径 (154 + 未来改 unit 走 journal 的服务器) ──
    local jd_remote_dir="/tmp/llm-gw-deploy-helpers"
    local jd_remote_cfg="$jd_remote_dir/journald-conf-snippet.conf"
    local jd_remote_script="$jd_remote_dir/configure-journald.sh"
    local jd_local_cfg="$PROJECT_ROOT/deploy/journald-conf-snippet.conf"
    local jd_local_script="$SCRIPT_DIR/configure-journald.sh"
    if [[ -f "$jd_local_cfg" && -f "$jd_local_script" ]]; then
      if cat "$jd_local_cfg" | remote_ssh_pipe "mkdir -p '$jd_remote_dir' && cat > '$jd_remote_cfg'" \
         && cat "$jd_local_script" | remote_ssh_pipe "cat > '$jd_remote_script'"; then
        if remote_ssh "chmod +x '$jd_remote_script' && bash '$jd_remote_script' install '$jd_remote_cfg'"; then
          ok "journald drop-in 已就绪"
        else
          warn "configure-journald install 失败（不影响 deploy, 可手动: ssh $TARGET 'bash $jd_remote_script install $jd_remote_cfg'）"
        fi
      else
        warn "journald 文件传输失败（不影响 deploy）"
      fi
    else
      warn "journald 资源缺失: $jd_local_cfg 或 $jd_local_script 不存在"
    fi
  fi
  # else: 未识别 unit 模式, 已在前面 warn, 不动任何文件

  # 清理本地临时文件
  rm -f "$tmpbin"; rm -rf "$bundle_dir"

  # prune: 保留所有 verified + active，仅清理 unverified 的失败尝试 (保留最新 2 个用于排查)。
  # 不用 host_prune_releases (它的 timestamp 排序对 adopted/legacy 版本有 bug，会误删 verified legacy)。
  prune_releases_safe "$version"

  local elapsed=$(( $(date +%s) - deploy_start ))
  echo ""
  ok "✅ $TARGET 部署完成 (总 ${elapsed}s, 切换 ${switch_elapsed}s) — version=$version seq=$seq_val"
  echo ""
  echo "验证:"
  echo "  curl http://$TARGET/api/system/version   (或 ssh 后 curl localhost:8781)"
  echo "  回滚: bash scripts/deploy-seamless.sh rollback $TARGET"
  echo "  状态: bash scripts/deploy-seamless.sh status $TARGET"
}

# ── 子命令: rollback ───────────────────────────────────────────
do_rollback() {
  log "查询 $TARGET 可回滚版本..."
  local active_version
  active_version=$($SSH_CMD "readlink '$REMOTE_ROOT/current' 2>/dev/null | xargs basename" 2>/dev/null || echo "")
  if [[ -z "$active_version" ]]; then
    err "无 current 符号链接 — 目标未采用 releases/ 布局，无法版本回滚"
    err "手动回滚: 老脚本 mv gateway.bak.* gateway && systemctl restart"
    exit 1
  fi
  warn "当前活跃: $active_version"

  local target_version
  target_version=$(host_select_rollback_target "$SSH_CMD" "$TARGET" "$active_version" 2>/dev/null || true)
  if [[ -z "$target_version" ]]; then
    err "无可用 verified 回滚目标 (需要至少一个 verified≠active 的 release)"
    err "可用版本:"
    $SSH_CMD "ls '$REMOTE_ROOT/releases/'" 2>/dev/null | sed 's/^/    /'
    exit 1
  fi
  ok "回滚目标: $target_version"

  log "原子切换 + restart..."
  host_atomic_switch "$SSH_CMD" "$TARGET" "$target_version" 2>&1 | sed 's/^/    /' || true
  if host_wait_healthy "$SSH_CMD" "$TARGET" 60 2>&1 \
    && deploy_verify_gateway_ready "$SSH_CMD" "$SERVICE_NAME" 8781 90; then
    ok "回滚完成 → $target_version (healthz + DB OK)"
    $SSH_CMD "curl -fsS '$HEALTH_URL' >/dev/null && echo '  healthz OK'" 2>/dev/null || true
  else
    err "回滚后 healthz/DB 失败! 手动检查"
    exit 1
  fi
}

# ── 分发 ───────────────────────────────────────────────────────
case "$ACTION" in
  deploy)   do_deploy ;;
  rollback) do_rollback ;;
  status)   do_status ;;
  *) err "未知动作: $ACTION (deploy|rollback|status)"; exit 1 ;;
esac

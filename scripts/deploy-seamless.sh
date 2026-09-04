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
#   bash scripts/deploy-seamless.sh deploy 245 --force  # 恢复 stale locks 后重建
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
# shellcheck source=deploy-lib/lock.sh
source "$SCRIPT_DIR/deploy-lib/lock.sh"
# shellcheck source=deploy-lib/host.sh
source "$SCRIPT_DIR/deploy-lib/host.sh"
# shellcheck source=deploy-lib/post-deploy-verify.sh
source "$SCRIPT_DIR/deploy-lib/post-deploy-verify.sh"
# shellcheck source=deploy-lib/db-changelog.sh
source "$SCRIPT_DIR/deploy-lib/db-changelog.sh"
# shellcheck source=deploy-lib/zero-downtime.sh
source "$SCRIPT_DIR/deploy-lib/zero-downtime.sh"

GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; RED=$'\033[0;31m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'
log()  { echo -e "${BLUE}[seamless]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }

# ── 参数解析 ────────────────────────────────────────────────────
ACTION="${1:-}"; TARGET="${2:-}"
SEQ_FLAG=""; SKIP_FRONTEND=false; FORCE=false; LEGACY_RESTART=false
shift 2 2>/dev/null || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --seq) SEQ_FLAG="--seq $2"; shift 2 ;;
    --no-frontend) SKIP_FRONTEND=true; shift ;;
    --force) FORCE=true; shift ;;
    --direct) export SSH_RETRY_DIRECT_MODE=1; shift ;;
    --legacy-restart) LEGACY_RESTART=true; shift ;;
    --ssh-retries) export SSH_RETRY_MAX=$2; shift 2 ;;
    --ssh-verbose) export SSH_RETRY_VERBOSE=1; shift ;;
    -h|--help)
      sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) err "未知参数: $1"; exit 1 ;;
  esac
done

[[ -n "$ACTION" ]] || { err "用法: deploy-seamless.sh <deploy|rollback|status> <245|154> [--seq N] [--force] [--ssh-retries N]"; exit 1; }
[[ -n "$TARGET" ]] || { err "缺少目标 (245|154)"; exit 1; }
if [[ "$FORCE" == true && "$ACTION" == status ]]; then
  err "--force 仅适用于 deploy 或 rollback，不适用于 status"
  exit 64
fi

if [[ "$LEGACY_RESTART" == true && "$ACTION" != deploy ]]; then
  err "--legacy-restart 仅适用于 deploy"
  exit 64
fi

case "$TARGET" in
  154|245) ;;
  *) err "不支持的目标: $TARGET (仅 154|245)"; exit 1 ;;
esac

DEPLOY_LOCAL_LOCK_HELD=0
DEPLOY_BUILD_LOCK_HELD=0
DEPLOY_REMOTE_LOCK_HELD=0
DEPLOY_REMOTE_LOCK_PATH="/var/lib/llm-gateway-go/deploy.lock"
if [[ "$ACTION" == deploy || "$ACTION" == rollback ]]; then
  # Each target has its own lock: deployments to 154 and 245 use separate
  # remote hosts and must not block one another locally.
  LOCK_LOCAL_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-deploy-${TARGET}.lock"
  LOCK_LOCAL_TARGET="$TARGET"
  if [[ "$FORCE" == true ]]; then
    lock_recover_local "$TARGET" 1 || exit $?
  fi
  lock_acquire_local || exit $?
  DEPLOY_LOCAL_LOCK_HELD=1
fi

deploy_cleanup() {
  local status=$?
  trap - EXIT INT TERM
  # 2026-08-27: 升级页的清理由成功验证/成功回滚路径负责。
  # 如果新版本验证失败且回滚也失败，保留 marker，避免未确认版本继续
  # 接收真实请求；如果启用 marker 后尚未开始切换，则旧服务仍在运行，
  # 可以安全撤掉页面，避免一次性预检失败留下陈旧页面。
  if [[ "${UPGRADE_BANNER_ACTIVE:-0}" == 1 && "${UPGRADE_SWITCH_STARTED:-0}" != 1 && -n "${SSH_CMD:-}" ]]; then
    upgrade_hide_all >/dev/null 2>&1 || true
  fi
  if [[ "${DEPLOY_REMOTE_LOCK_HELD:-0}" == 1 ]]; then
    lock_release_remote remote_ssh "$DEPLOY_REMOTE_LOCK_PATH" || true
  fi
  if [[ "${DEPLOY_BUILD_LOCK_HELD:-0}" == 1 ]]; then
    lock_release_build || true
  fi
  if [[ "${DEPLOY_LOCAL_LOCK_HELD:-0}" == 1 ]]; then
    lock_release_local || true
  fi
  ssh_retry_close_all || true
  exit "$status"
}
trap deploy_cleanup EXIT INT TERM

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

# remote_probe <url> <timeout_secs>
# Polls a single HTTP endpoint until it returns a 2xx within the deadline.
# On success: prints the body on stdout (caller may ignore).
# On failure: prints a single-line diagnostic on stdout (captured by the caller
# via command substitution) explaining why the probe failed — curl exit code,
# the last HTTP status seen, or a timeout marker. SSH transport failures are
# reported as "ssh: <reason>". The function NEVER aborts the script; it is the
# caller's job to inspect the captured diagnostic.
#
# URL is interpolated into a remote shell command. Callers MUST pass a
# hardcoded path under the candidate's port (no user-supplied query string,
# no single quotes). Current call sites pass http://127.0.0.1:<port>/{healthz,
# readyz,version}, all of which are safe.
remote_probe() {
  local url="$1" timeout="${2:-30}"
  local body
  # Capture both the curl exit code and the HTTP body. We use -w to append
  # the status line so a 5xx response surfaces distinctly from a connection
  # refused / timeout. The final line of stdout is the curl exit marker so
  # the caller can distinguish "transport failed" from "200 but wrong body"
  # without re-parsing the body itself.
  body=$(remote_ssh "deadline=\$((\$(date +%s)+${timeout})); while [ \"\$(date +%s)\" -lt \"\$deadline\" ]; do out=\$(curl -sS --max-time 2 -w '\n%{http_code}' '${url}' 2>&1); code=\$(printf '%s' \"\$out\" | tail -n1); body=\$(printf '%s' \"\$out\" | sed '\$d'); if printf '%s' \"\$code\" | grep -qE '^[0-9]+\$' && [ \"\$code\" -ge 200 ] && [ \"\$code\" -lt 400 ]; then printf '%s\n__CURL_OK__' \"\$body\"; exit 0; fi; sleep 1; done; printf 'probe timeout after ${timeout}s, last attempt: %s\n__CURL_FAIL__' \"\$out\"; exit 1" 2>&1) || {
    printf 'ssh transport failed: %s' "$body"
    return 1
  }
  local marker
  marker=$(printf '%s' "$body" | tail -n1)
  if [[ "$marker" == "__CURL_OK__" ]]; then
    printf '%s' "$body" | sed '$d'
    return 0
  fi
  printf '%s' "$body" | sed '$d'
  return 1
}

# version_identity_matches compares /version JSON with the immutable identity
# read from the staged bundle. It prints a concise diagnostic on mismatch.
version_identity_matches() {
  local body=$1 expected_version=$2 expected_seq=$3 expected_sha=$4 expected_date=$5
  VERSION_BODY="$body" EXPECTED_VERSION="$expected_version" EXPECTED_SEQ="$expected_seq" \
    EXPECTED_SHA="$expected_sha" EXPECTED_DATE="$expected_date" python3 -c '
import json, os, sys
try:
    actual = json.loads(os.environ["VERSION_BODY"])
except Exception as exc:
    print(f"invalid JSON: {exc}; body={os.environ['"'"'VERSION_BODY'"'"']}")
    raise SystemExit(1)
expected = {
    "version": os.environ["EXPECTED_VERSION"],
    "build_seq": int(os.environ["EXPECTED_SEQ"]),
    "git_sha": os.environ["EXPECTED_SHA"],
    "build_date": os.environ["EXPECTED_DATE"],
}
mismatches = [f"{key}: expected={value!r} got={actual.get(key)!r}" for key, value in expected.items() if actual.get(key) != value]
if mismatches:
    print("; ".join(mismatches) + f"; body={os.environ['"'"'VERSION_BODY'"'"']}")
    raise SystemExit(1)
'
}

# Install the blue-green assets (canary unit template + nginx upstream
# fragment bootstrap) on the target by shipping a minimal repo slice and
# running scripts/install-blue-green-assets.sh there as a REAL script file.
# The installer is file-only: it writes the canary unit (backing up any unit
# it replaces), then runs daemon-reload + nginx -t. It never starts a
# candidate or changes traffic, so re-running it mid-deploy for drift repair
# is safe.
#
# 2026-08-31: the previous implementation piped the installer BODY into the
# ssh command string. That is unrunnable: `bash -c` leaves BASH_SOURCE
# unbound (set -u aborts at SCRIPT_DIR), the installer re-parses its
# positional defaults and clobbers the injected TARGET=245 back to 154, and
# $SCRIPT_DIR/../deploy/<unit> only exists in a repo checkout, which the
# target does not have. Hence: tar the installer + deploy/<unit> into a temp
# dir on the target (preserving the scripts/ + deploy/ layout so the
# installer's own path resolution works), then execute it with ROOT/TARGET
# as positional arguments and stream its output back.
run_blue_green_assets_install() {
  local canonical_unit tmpdir
  canonical_unit="$SCRIPT_DIR/../deploy/$(target_field "$TARGET" candidate_unit)"
  tmpdir="/tmp/kx-bg-assets-${TARGET}"
  if [[ ! -f "$canonical_unit" ]]; then
    err "run_blue_green_assets_install: missing canonical unit template $canonical_unit"
    return 1
  fi
  log "    [blue-green-assets] install-blue-green-assets.sh $REMOTE_ROOT $TARGET (on target)"
  if ! tar -C "$SCRIPT_DIR/.." -cf - scripts/install-blue-green-assets.sh \
       "deploy/$(target_field "$TARGET" candidate_unit)" \
       | remote_ssh_pipe "rm -rf '$tmpdir' && mkdir -p '$tmpdir' && tar -C '$tmpdir' -xf -"; then
    err "run_blue_green_assets_install: 上传安装切片到目标机失败"
    return 1
  fi
  remote_ssh "bash '$tmpdir/scripts/install-blue-green-assets.sh' '$REMOTE_ROOT' '$TARGET'; rc=\$?; rm -rf '$tmpdir'; exit \$rc" 2>&1 | sed 's/^/      /'
}

# Blue-green is opt-in until the candidate unit and Nginx include are installed
# on the target. The canonical deployer fails closed rather than silently
# reverting to the old stop/start path; operators may explicitly request the
# legacy emergency flow with --legacy-restart.
LEGACY_RESTART=false

# 154 的公网 HTTPS 入口在 252 上终止 TLS 并代理到 154:8781。
# 仅 154 部署需要同步保护 252 的公网 vhost；245 有自己的公网 vhost。
public_252_ssh() {
  local script=${1:-}
  local key=${SSH_KEY_252:-${HOME}/.ssh/id_ed25519}
  [[ -f "$key" ]] || { err "missing SSH key for public 252 ingress: $key"; return 1; }
  ssh -i "$key" -p "$SSH_PORT" \
    -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
    root@115.29.212.252 "$script"
}

upgrade_show_all() {
  local version=$1
  # 2026-08-28: 写完后等待 nginx 真正返回维护页再继续 (超时 120s，典型 ~60s)。
  # 仅在“静态页已生效”后才停机切换，避免在 marker 未生效的窗口暴露真实首页。
  host_show_upgrade_banner "$SSH_CMD" "$TARGET" "$version" || return 1
  if [[ "$TARGET" == "245" ]]; then
    # 245 nginx selects the pre-prod vhost by Host/SNI. Probe that vhost
    # explicitly; https://127.0.0.1/ can match a different default server.
    if ! host_wait_upgrade_banner "$SSH_CMD" "$TARGET" 120 \
        /opt/llm-gateway-go /opt/llm-gateway-go/maintenance \
        "https://llmgo.kxpms.cn/" \
        "--resolve llmgo.kxpms.cn:443:127.0.0.1"; then
      upgrade_hide_all >/dev/null 2>&1 || true
      return 1
    fi
  elif ! host_wait_upgrade_banner "$SSH_CMD" "$TARGET" 120; then
    upgrade_hide_all >/dev/null 2>&1 || true
    return 1
  fi
  if [[ "$TARGET" == "154" ]]; then
    if ! host_show_upgrade_banner public_252_ssh "$TARGET" "$version" \
        /opt/llm-gateway-go /var/www/llm-gateway-maintenance; then
      upgrade_hide_all >/dev/null 2>&1 || true
      return 1
    fi
    # 2026-08-28: 252 上探测必须用 252 自己的 vhost。probe 走 public_252_ssh
    # 在 252 上执行 curl, target 字段仍是 154 — 127.0.0.1 在 252 上命中的
    # 是无关 vhost, 会永远等不到维护页 (120s 误判失败)。
    # --resolve 把 llm.kxpms.cn 钉在 127.0.0.1, 确保命中 252 本机 vhost。
    if ! host_wait_upgrade_banner public_252_ssh "$TARGET" 120 \
        /opt/llm-gateway-go /var/www/llm-gateway-maintenance \
        "https://llm.kxpms.cn/" \
        "--resolve llm.kxpms.cn:443:127.0.0.1"; then
      upgrade_hide_all >/dev/null 2>&1 || true
      return 1
    fi
  fi
}

upgrade_hide_all() {
  local rc=0
  if [[ "$TARGET" == "154" ]]; then
    host_hide_upgrade_banner public_252_ssh "$TARGET" \
      /opt/llm-gateway-go /var/www/llm-gateway-maintenance || rc=1
  fi
  host_hide_upgrade_banner "$SSH_CMD" "$TARGET" || rc=1
  return "$rc"
}

if [[ "$ACTION" == deploy || "$ACTION" == rollback ]]; then
  if [[ "$FORCE" == true ]]; then
    lock_recover_remote "$SSH_CMD" "$TARGET" "$DEPLOY_REMOTE_LOCK_PATH" 1 || exit $?
  fi
  lock_acquire_remote remote_ssh_pipe "$TARGET" "$DEPLOY_REMOTE_LOCK_PATH" || exit $?
  DEPLOY_REMOTE_LOCK_HELD=1
fi

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

# 确认 current、systemd MainPID 和运行二进制都属于目标 release。
_verify_running_release() {
  local expected_version=$1 service_name=${2:-$SERVICE_NAME}
  remote_ssh "ROOT='$REMOTE_ROOT' SERVICE='$service_name' EXPECTED_VERSION='$expected_version' python3 - <<'PYVERIFY'
import hashlib
import os
import subprocess

root = os.environ['ROOT']
service = os.environ['SERVICE']
expected = os.path.realpath(os.path.join(root, 'releases', os.environ['EXPECTED_VERSION']))
current = os.path.realpath(os.path.join(root, 'current'))
if current != expected:
    raise SystemExit(f'current mismatch: {current} != {expected}')
show = subprocess.check_output(
    ['systemctl', 'show', service, '--property=MainPID'],
    universal_newlines=True,
)
pid = ''
for line in show.splitlines():
    stripped = line.strip()
    if stripped.startswith('MainPID='):
        pid = stripped.split('=', 1)[1].strip()
        break
if not pid or pid == '0':
    raise SystemExit('systemd MainPID is not running')
running = os.path.realpath('/proc/%s/exe' % pid)
binary_name = os.path.basename(running)
expected_binary = os.path.join(expected, binary_name)
if not os.path.isfile(expected_binary):
    raise SystemExit(f'expected release binary missing: {expected_binary}')
if running != os.path.realpath(expected_binary):
    raise SystemExit(f'running executable mismatch: {running} != {expected_binary}')
def digest(path):
    value = hashlib.sha256()
    with open(path, 'rb') as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()
if digest('/proc/%s/exe' % pid) != digest(expected_binary):
    raise SystemExit('running executable checksum mismatch')
PYVERIFY"
}

# healthz 或 DB 校验失败时回滚到上一个 verified 版本
_bluegreen_abort() {
  local reason=$1 old_version=$2 old_port=$3 candidate_port=$4 candidate_service=$5 upstream_fragment=$6 old_service=${7:-$SERVICE_NAME}
  err "$reason — restoring previous upstream"
  zd_switch_upstream "$SSH_CMD" "$upstream_fragment" "$old_port" || true
  if [[ -n "$old_version" ]]; then
    remote_ssh "set -e; ln -sfn '$REMOTE_ROOT/releases/$old_version' '$REMOTE_ROOT/current'; ln -sfn '$REMOTE_ROOT/current/$BIN_NAME' '$REMOTE_ROOT/$BIN_NAME'; ln -sfn '$REMOTE_ROOT/current/web' '$REMOTE_ROOT/web'; ln -sfn '$REMOTE_ROOT/current/version.json' '$REMOTE_ROOT/version.json'; printf '%s\\n' '$old_port' > '$REMOTE_ROOT/run/active-port'; printf '%s\\n' '$old_service' > '$REMOTE_ROOT/run/active-service'" || true
  fi
  zd_stop_candidate "$SSH_CMD" "$candidate_service"
  remote_ssh "rm -f '$REMOTE_ROOT/slots/$candidate_port' '$REMOTE_ROOT/run/candidate-port'" || true
}

_seamless_auto_rollback() {
  local reason=$1 failed_version=$2
  err "$reason — 自动回滚..."
  local prev
  prev=$(host_select_rollback_target "$SSH_CMD" "$TARGET" "$failed_version" 2>/dev/null || true)
  if [[ -n "$prev" ]]; then
    warn "回滚到 releases/$prev"
    if ! host_atomic_switch "$SSH_CMD" "$TARGET" "$prev" 2>&1 | sed 's/^/    /'; then
      err "回滚切换或 restart 失败"
      return 1
    fi
    # 2026-08-28: 90s → 120s, 启动时 license 验证 + 数据库迁移可能需要更长时间
    if host_wait_healthy "$SSH_CMD" "$TARGET" 120 2>&1 \
      && _verify_running_release "$prev" \
      && deploy_preflight_pg_from_remote_env "$SSH_CMD" "$(_env_file_for_target)"; then
      ok "已回滚到 $prev (healthz + running release + PG OK)"
      if [[ "${UPGRADE_BANNER_ACTIVE:-0}" == 1 ]]; then
        if upgrade_hide_all >/dev/null 2>&1; then
          UPGRADE_BANNER_ACTIVE=0
        else
          warn "回滚成功但升级静态页撤掉失败，保留 marker 保护流量"
          return 1
        fi
      fi
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
    # A crashed deploy can leave slots/<port> symlinks pointing at releases
    # that no longer exist. Never touch the slot the run/ pointers currently
    # reference, even if it dangles — that needs operator eyes, not a prune.
    active_slot=\$(cat '$REMOTE_ROOT/run/active-port' 2>/dev/null || true)
    cand_slot=\$(cat '$REMOTE_ROOT/run/candidate-port' 2>/dev/null || true)
    cd '$REMOTE_ROOT/slots' 2>/dev/null || exit 0
    for s in *; do
      [ \"\$s\" = '*' ] && continue
      [ -e \"\$s\" ] && continue
      if [ \"\$s\" != \"\$active_slot\" ] && [ \"\$s\" != \"\$cand_slot\" ]; then
        rm -f \"\$s\" && echo \"pruned dangling slot: \$s\"
      fi
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
  # The env file holds DB credentials; tighten a historically loose 0644
  # to root-only so every deploy run converges it to 0600.
  $SSH_CMD "chmod 0600 '$(_env_file_for_target)' 2>/dev/null || true; chown root:root '$(_env_file_for_target)' 2>/dev/null || true" || true
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

  # Shared checkout build state (version files, web/dist, local staging) is
  # serialized independently from the per-target deployment lock.
  log "[build-lock] 获取共享构建锁"
  # Never honor an inherited build-lock path: force recovery and normal
  # acquisition must address the one shared checkout lock only.
  LOCK_LOCAL_BUILD_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-build.lock"
  LOCK_BUILD_TARGET="$TARGET"
  if [[ "$FORCE" == true ]]; then
    lock_recover_build 1 || exit $?
  fi
  lock_acquire_build || exit $?
  DEPLOY_BUILD_LOCK_HELD=1

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

  # 2–3. 前端 + 后端串行构建（降低 2GB 主机峰值内存）
  log "[2/9] 前端 + 后端串行构建"
  local tmpbin="/tmp/__seamless_${TARGET}_binary"
  # go build refuses to overwrite a path that already holds a non-object file
  # (e.g. a leftover stub script from a previously interrupted run). Drop any
  # stale artifact before invoking the toolchain so the build never trips the
  # "already exists and is not an object file" guard.
  rm -f "$tmpbin"
  if [[ "$SKIP_FRONTEND" == "false" ]]; then
    if [[ ! -d web/node_modules ]]; then
      log "web/node_modules 缺失，按 package-lock.json 安装依赖"
      (cd web && npm ci)
    fi
    (cd web && npm run build 2>&1 | tail -5)
    ok "web/dist 已生成"
  else
    warn "跳过前端 (--no-frontend)，仅更新二进制"
  fi
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
    -o "$tmpbin" ./cmd/gateway
  ok "编译完成 ($(du -h "$tmpbin" | cut -f1))"

  # 4. stage bundle (本地)
  log "[4/9] stage release bundle"
  bundle_dir="/tmp/seamless-release-${TARGET}-${seq_val}"
  rm -rf "$bundle_dir"; mkdir -p "$bundle_dir/web"
  if ! HOST_STAGE_TARGET="$TARGET" HOST_STAGE_VERSION="$version" \
    host_stage_release "$bundle_dir" "$tmpbin" "web/dist" 2>&1 | sed 's/^/    /'; then
    err "stage release bundle 失败"
    exit 1
  fi
  ok "bundle: $bundle_dir"
  local expected_release_version expected_release_seq expected_release_sha expected_release_date
  read -r expected_release_version expected_release_seq expected_release_sha expected_release_date < <(
    python3 -c 'import json, sys; d=json.load(open(sys.argv[1])); print(d["version"], d["build_seq"], d["git_sha"], d["build_date"])' "$bundle_dir/version.json"
  )
  [[ -n "$expected_release_version" && -n "$expected_release_seq" && -n "$expected_release_sha" && -n "$expected_release_date" ]] || {
    err "staged bundle version.json 不完整，拒绝部署"
    exit 1
  }
  ok "staged identity: ${expected_release_version} (seq=${expected_release_seq} sha=${expected_release_sha})"
  lock_release_build
  DEPLOY_BUILD_LOCK_HELD=0
  ok "共享构建锁已释放"

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

  # 8. candidate warm-up + atomic Nginx handoff
  local switch_start_ns switch_end_ns switch_elapsed_ms active_port candidate_port upstream_fragment candidate_unit candidate_binary
  local candidate_service active_service old_version
  active_port=$(target_field "$TARGET" active_port)
  candidate_port="$active_port"
  candidate_service="$SERVICE_NAME"
  active_service="$SERVICE_NAME"
  old_version=$(remote_ssh "readlink '$REMOTE_ROOT/current' 2>/dev/null | xargs basename" 2>/dev/null || true)
  if [[ "$LEGACY_RESTART" == true ]]; then
    log "[8/9] legacy stop/start emergency path"
    host_atomic_switch "$SSH_CMD" "$TARGET" "$version" 2>&1 | sed 's/^/    /'
    local switch_elapsed=0
    warn "legacy-restart used; 2-second blue-green SLO is not applicable"
  else
  log "[8/9] 候选预热 + Nginx 原子切流"
  active_port=$(remote_ssh "cat '$REMOTE_ROOT/run/active-port' 2>/dev/null" 2>/dev/null || true)
  active_port=${active_port:-$(target_field "$TARGET" active_port)}
  candidate_port=$(target_field "$TARGET" candidate_port)
  if [[ "$active_port" == "$candidate_port" ]]; then
    candidate_port=$(target_field "$TARGET" active_port)
  fi
  upstream_fragment=$(target_field "$TARGET" upstream_fragment)
  candidate_unit=$(target_field "$TARGET" candidate_unit)
  candidate_service="${candidate_unit%@.service}@${candidate_port}.service"
  # 2026-08-31: env 245 has historically been deployed via the legacy stop/start
  # path, so the canary unit and active-upstream.conf fragment were never
  # installed on it. The first seamless run on a fresh / never-installed target
  # therefore failed closed at this gate. We now attempt a one-shot self-install
  # via scripts/install-blue-green-assets.sh when both assets are missing; if
  # only one is present we still refuse (partial install = operator judgment).
  if ! remote_ssh "test -f '$(target_field "$TARGET" candidate_unit_file)' && test -f '$upstream_fragment'"; then
    if remote_ssh "test -f '$(target_field "$TARGET" candidate_unit_file)' || test -f '$upstream_fragment'"; then
      err "目标蓝绿资产部分缺失（unit 或 fragment 仅有一个存在），拒绝切换以免误修；如需应急请显式使用 --legacy-restart"
      exit 1
    fi
    warn "目标未安装蓝绿候选 unit 与 upstream fragment，自动调用 install-blue-green-assets.sh 初始化"
    if [[ ! -x "$SCRIPT_DIR/install-blue-green-assets.sh" ]]; then
      err "缺少 scripts/install-blue-green-assets.sh，无法自愈；如需应急请显式使用 --legacy-restart"
      exit 1
    fi
    # The installer MUST run on the target so that `install /etc/systemd/system`
    # writes to the remote host, not the orchestrator; it is idempotent and
    # file-only (see run_blue_green_assets_install).
    if ! run_blue_green_assets_install; then
      err "install-blue-green-assets.sh 在目标机上失败，请检查目标 nginx / systemd 状态；如需应急请显式使用 --legacy-restart"
      exit 1
    fi
    if ! remote_ssh "test -f '$(target_field "$TARGET" candidate_unit_file)' && test -f '$upstream_fragment'"; then
      err "自愈后蓝绿资产仍未就位；如需应急请显式使用 --legacy-restart"
      exit 1
    fi
    ok "    [self-heal] 蓝绿资产已就位，继续蓝绿切流"
  else
    # 2026-08-31: the canary unit is a deployer-owned contract, but the gate
    # above only checked EXISTENCE. The original 245 unit pinned its listen
    # port in canary.env (frozen at install time), which made every second
    # blue-green deploy die on a port bind conflict; the fixed unit carries
    # the port on ExecStart (%i). Without a drift check the broken unit
    # installed by an earlier deploy would stay frozen forever. Re-install
    # whenever the installed unit differs from the repo contract — the
    # installer backs up the unit it replaces, and running instances are
    # untouched until their next start.
    local canonical_unit_file installed_unit_body
    canonical_unit_file="$SCRIPT_DIR/../deploy/$(target_field "$TARGET" candidate_unit)"
    installed_unit_body=$(remote_ssh "cat '$(target_field "$TARGET" candidate_unit_file)'" 2>/dev/null || true)
    if [[ -f "$canonical_unit_file" && "$installed_unit_body" != "$(cat "$canonical_unit_file")" ]]; then
      warn "目标 canary unit 与仓库契约不一致（端口契约等修复），重新安装（旧 unit 自动备份）"
      if ! run_blue_green_assets_install; then
        err "canary unit 漂移修复失败，请检查目标 nginx / systemd 状态；如需应急请显式使用 --legacy-restart"
        exit 1
      fi
      installed_unit_body=$(remote_ssh "cat '$(target_field "$TARGET" candidate_unit_file)'" 2>/dev/null || true)
      if [[ "$installed_unit_body" != "$(cat "$canonical_unit_file")" ]]; then
        err "重装后 canary unit 仍与仓库契约不一致；如需应急请显式使用 --legacy-restart"
        exit 1
      fi
      ok "    [drift-repair] canary unit 已同步为仓库契约"
    fi
  fi
  if remote_ssh "test -f '$REMOTE_ROOT/run/active-service'"; then
    active_service=$(remote_ssh "cat '$REMOTE_ROOT/run/active-service'")
  fi
  old_version=$(remote_ssh "readlink '$REMOTE_ROOT/current' 2>/dev/null | xargs basename" 2>/dev/null || true)
  switch_start_ns=$(zd_now_ns)
  # The canary unit executes its slot directly. Pre-warming therefore never
  # mutates current, which remains the identity of the serving instance until
  # Nginx has accepted the candidate.
  remote_ssh "set -e; mkdir -p '$REMOTE_ROOT/run' '$REMOTE_ROOT/slots'; systemctl stop '$candidate_service' >/dev/null 2>&1 || true; deadline=\$((\$(date +%s)+45)); while systemctl is-active --quiet '$candidate_service'; do if [ \"\$(date +%s)\" -ge \"\$deadline\" ]; then echo 'candidate stop timed out after 45s' >&2; systemctl status '$candidate_service' --no-pager >&2 || true; journalctl -u '$candidate_service' -n 30 --no-pager >&2 || true; exit 1; fi; sleep 1; done; if ss -ltn | grep -q ':${candidate_port} '; then echo 'candidate port remains occupied after stop' >&2; ss -ltnp | grep ':${candidate_port} ' >&2 || true; exit 1; fi; ln -sfn '$REMOTE_ROOT/releases/$version' '$REMOTE_ROOT/slots/$candidate_port'; printf '%s\n' '$candidate_port' > '$REMOTE_ROOT/run/candidate-port'; printf '%s\n' '$candidate_service' > '$REMOTE_ROOT/run/candidate-service'; systemctl daemon-reload"
  if ! remote_ssh "systemctl start '$candidate_service'"; then
    err "候选实例启动失败，旧实例保持服务"
    exit 1
  fi
  if ! remote_ssh "systemctl is-active --quiet '$candidate_service'"; then
    zd_stop_candidate "$SSH_CMD" "$candidate_service"
    err "候选 unit 未保持 active，旧实例继续服务"
    exit 1
  fi

  local candidate_health_url="http://127.0.0.1:${candidate_port}/healthz"
  local candidate_ready_url="http://127.0.0.1:${candidate_port}/readyz"
  local candidate_version_url="http://127.0.0.1:${candidate_port}/version"
  # 2026-08-31: probe failures previously collapsed into a single
  # "候选实例未通过 healthz/readyz" message with no per-probe diagnostics. Each
  # candidate can fail for a different reason (process not bound yet vs DB/Redis
  # not ready vs wrong binary on disk), and operators wasted an SSH round trip
  # to read journalctl to disambiguate. We now run each probe under its own
  # deadline, capture curl's exit code and HTTP body (when reachable), and print
  # the exact failure before stopping the candidate.
  #
  # 2026-08-31: default bumped 30 -> 60s. On env 154 a fresh candidate takes
  # 35-40s to bind its listener while it walks schema ensures for 8+
  # feature areas (request_logs, quality_fix_mode, provider/credential
  # soft-delete, applications, fp_slot_limit, concurrency_mode,
  # credential_governor_revision, routing recent_success_rate,
  # unavailable_recover_at). With the prior 30s default the candidate
  # got killed mid-schema-ensure, /healthz never came up, and the
  # deploy aborted with a confusing "Connection refused". 60s still
  # leaves enough headroom for cold-start migrations while bounding
  # blast radius if the candidate truly is broken.
  local probe_timeout="${PROBE_TIMEOUT_SECS:-60}"
  local probe_failed=""
  local probe_detail=""
  log "    probe /healthz (timeout=${probe_timeout}s)"
  if ! probe_detail=$(remote_probe "$candidate_health_url" "$probe_timeout"); then
    probe_failed="healthz"
    warn "    /healthz failed: ${probe_detail}"
  else
    ok "    /healthz OK"
  fi
  if [[ -z "$probe_failed" ]]; then
    log "    probe /readyz (timeout=${probe_timeout}s)"
    if ! probe_detail=$(remote_probe "$candidate_ready_url" "$probe_timeout"); then
      probe_failed="readyz"
      warn "    /readyz failed: ${probe_detail}"
    else
      ok "    /readyz OK"
    fi
  fi
  if [[ -z "$probe_failed" ]]; then
    log "    probe /version (timeout=5s, expected=${expected_release_version} seq=${expected_release_seq} sha=${expected_release_sha})"
    local version_body
    if ! version_body=$(remote_ssh "curl -sS --max-time 5 '$candidate_version_url' 2>&1"); then
      probe_failed="version"
      probe_detail="curl exit non-zero: ${version_body}"
      warn "    /version failed: ${probe_detail}"
    elif ! probe_detail=$(version_identity_matches "$version_body" "$expected_release_version" "$expected_release_seq" "$expected_release_sha" "$expected_release_date"); then
      probe_failed="version"
      warn "    /version failed: ${probe_detail}"
    else
      ok "    /version OK (${expected_release_version}, seq=${expected_release_seq}, sha=${expected_release_sha})"
    fi
  fi
  if [[ -n "$probe_failed" ]]; then
    warn "    candidate probe failure tail (last 30 journal lines):"
    remote_ssh "journalctl -u '$candidate_service' -n 30 --no-pager 2>&1 | tail -30" 2>&1 | sed 's/^/      /' || true
    zd_stop_candidate "$SSH_CMD" "$candidate_service"
    remote_ssh "rm -f '$REMOTE_ROOT/slots/$candidate_port' '$REMOTE_ROOT/run/candidate-port'" || true
    # Restore current to the old release so the active (which also follows
    # current/) keeps serving the previously verified binary.
    if [[ -n "$old_version" ]]; then
      remote_ssh "set -e; ln -sfn '$REMOTE_ROOT/releases/$old_version' '$REMOTE_ROOT/current'; ln -sfn '$REMOTE_ROOT/current/$BIN_NAME' '$REMOTE_ROOT/$BIN_NAME'; ln -sfn '$REMOTE_ROOT/current/web' '$REMOTE_ROOT/web'; ln -sfn '$REMOTE_ROOT/current/version.json' '$REMOTE_ROOT/version.json'" || true
    fi
    err "候选实例未通过 ${probe_failed}: ${probe_detail}，旧实例保持服务"
    exit 1
  fi
  if ! zd_switch_upstream "$SSH_CMD" "$upstream_fragment" "$candidate_port"; then
    zd_stop_candidate "$SSH_CMD" "$candidate_service"
    remote_ssh "rm -f '$REMOTE_ROOT/slots/$candidate_port' '$REMOTE_ROOT/run/candidate-port'" || true
    if [[ -n "$old_version" ]]; then
      remote_ssh "set -e; ln -sfn '$REMOTE_ROOT/releases/$old_version' '$REMOTE_ROOT/current'; ln -sfn '$REMOTE_ROOT/current/$BIN_NAME' '$REMOTE_ROOT/$BIN_NAME'; ln -sfn '$REMOTE_ROOT/current/web' '$REMOTE_ROOT/web'; ln -sfn '$REMOTE_ROOT/current/version.json' '$REMOTE_ROOT/version.json'" || true
    fi
    err "Nginx 切流失败，旧 upstream 已恢复"
    exit 1
  fi
  # Confirm the real Nginx path serves the candidate before changing the
  # release pointer. This protects rollback selection from a false handoff.
  if ! remote_ssh "curl -kfsS --max-time 5 https://127.0.0.1/version >/tmp/kx-candidate-version.json"; then
    _bluegreen_abort "切流后 Nginx 版本探针失败" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
    exit 1
  fi
  remote_ssh "set -e; ln -sfn '$REMOTE_ROOT/releases/$version' '$REMOTE_ROOT/current'; ln -sfn '$REMOTE_ROOT/current/$BIN_NAME' '$REMOTE_ROOT/$BIN_NAME'; ln -sfn '$REMOTE_ROOT/current/web' '$REMOTE_ROOT/web'; ln -sfn '$REMOTE_ROOT/current/version.json' '$REMOTE_ROOT/version.json'; printf '%s\\n' '$candidate_port' > '$REMOTE_ROOT/run/active-port'; printf '%s\\n' '$candidate_service' > '$REMOTE_ROOT/run/active-service'"
  switch_end_ns=$(zd_now_ns)
  switch_elapsed_ms=$(( (switch_end_ns - switch_start_ns) / 1000000 ))
  local switch_elapsed=$(( (switch_elapsed_ms + 999) / 1000 ))
  ok "Nginx 已切到候选端口 ${candidate_port} (handoff=${switch_elapsed_ms}ms)"
  if (( switch_elapsed_ms > 2000 )); then
    warn "handoff 超过 2s（${switch_elapsed_ms}ms），仍保留旧实例用于回滚；不停止旧服务"
  fi
  # The old unit is deliberately stopped only after all post-switch gates.

  # 9. post-handoff liveness + strict readiness (失败时恢复旧 upstream)
  # The candidate is probed by its own port above; these checks remain
  # intentionally short and never stop the former active process first.
  # /healthz only proves that the process is alive. /readyz is the release gate:
  # it requires the database and Redis dependencies to be usable before the
  # release can be marked verified.
  # 2026-08-28: 90s → 120s, 启动时 license 验证 + 数据库迁移可能需要更长时间
  log "[9/9] 验证 /healthz + /readyz + DB + release identity"
  # 2026-08-31: surface the failing probe + last journal lines instead of a
  # bare "_bluegreen_abort" call. We deliberately do NOT auto-revert on these
  # post-handoff failures — the candidate has already been promoted to the
  # Nginx upstream and reversing is riskier than letting ops diagnose — but
  # the diagnostic tail makes the abort actionable.
  if ! post_handoff_detail=$(remote_probe "$candidate_health_url" 10); then
    warn "    post-handoff /healthz: ${post_handoff_detail}"
    warn "    journal tail:"
    remote_ssh "journalctl -u '$candidate_service' -n 30 --no-pager 2>&1 | tail -30" 2>&1 | sed 's/^/      /' || true
    _bluegreen_abort "候选切流后 healthz 失败" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
    exit 1
  fi
  if ! post_handoff_detail=$(remote_probe "$candidate_ready_url" 10); then
    warn "    post-handoff /readyz: ${post_handoff_detail}"
    warn "    journal tail:"
    remote_ssh "journalctl -u '$candidate_service' -n 30 --no-pager 2>&1 | tail -30" 2>&1 | sed 's/^/      /' || true
    _bluegreen_abort "候选切流后 readyz 失败" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
    exit 1
  fi

  # 9.5 2026-08-19 OOM 复盘加固: 验证目标机自身 nginx(443)→gateway 链路.
  # 旧流程只检查 127.0.0.1:8781/healthz, gateway 存活 ≠ 公网通. OOM 现场
  # gateway 活 + nginx failed = 1h21min 公网 502. 现增加 127.0.0.1:443/healthz
  # 校验, 失败 = nginx failed → 自动 rollback.
  log "[9.5/9] 验证目标机自身 nginx→gateway (127.0.0.1:443/healthz)"
  if ! host_wait_https_healthy "$SSH_CMD" "$TARGET" 30 2>&1; then
    _seamless_auto_rollback "nginx (443) healthz 失败 — OOM 或 nginx 未自愈, 立即回滚" "$version" || true
    exit 1
  fi

  # 先确认 DB 已就绪，再同步 admin 密码并验证登录。
  # 否则网关在 postgres disabled (db == nil) 时 handleLogin 返回 503 database not configured，
  # 会误触发自动回滚；与 deploy_verify_gateway_ready 的 503-tolerant 契约保持一致。
  if ! deploy_verify_gateway_ready "$SSH_CMD" "$candidate_service" "$candidate_port" 90 "$(_env_file_for_target)"; then
    _bluegreen_abort "DB 未就绪 (database not configured 风险)" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
    exit 1
  fi

  if [[ "${DEPLOY_SYNC_ADMIN_PASSWORD:-true}" == "true" ]]; then
    log "[9.1/9] 同步 admin 密码 (env → users)"
    if ! LLM_GATEWAY_ADMIN_SYNC_PORT="$candidate_port" LLM_GATEWAY_ADMIN_SYNC_SERVICE="$candidate_service" bash "$SCRIPT_DIR/ops/sync-admin-password-from-env.sh" "$TARGET"; then
      _bluegreen_abort "admin 密码同步或登录验证失败" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
      exit 1
    fi
    ok "admin 密码已同步"
  fi

  # 9.2 2026-09-04 245 凭据解密事故固化: DB 就绪 ≠ 解密链路可用。
  # 当天 245 跑了缺 DecryptAny 修复的 binary, healthz / background-tasks /
  # admin 登录全绿, 但 providers 页面所有凭据「无法解析」、用户 apikey
  # reveal 报 invalid fernet token。此检查真实拉取凭据列表统计
  # decrypt_failed, 系统性失败 = 回滚, 不允许带病切流。
  log "[9.2/9] 凭据解密冒烟 (providers → credentials, 全败则回滚)"
  if ! deploy_verify_credential_decrypt "$SSH_CMD" "$candidate_port" "$(_env_file_for_target)"; then
    _bluegreen_abort "凭据解密冒烟失败 (keyring 与 DB 密文不匹配或解密路径回归)" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
    exit 1
  fi

  local public_version_body
  if ! public_version_body=$(remote_ssh "curl -kfsS --max-time 10 --resolve llmgo.kxpms.cn:443:127.0.0.1 https://llmgo.kxpms.cn/version" 2>&1); then
    warn "    public /version fetch failed: ${public_version_body}"
    _bluegreen_abort "公网入口版本身份校验失败" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
    exit 1
  elif ! probe_detail=$(version_identity_matches "$public_version_body" "$expected_release_version" "$expected_release_seq" "$expected_release_sha" "$expected_release_date"); then
    warn "    public /version failed: ${probe_detail}"
    _bluegreen_abort "公网入口版本身份校验失败" "$old_version" "$active_port" "$candidate_port" "$candidate_service" "$upstream_fragment" "$active_service"
    exit 1
  fi
  if ! host_mark_verified "$SSH_CMD" "$TARGET" "$version" 2>&1 | sed 's/^/    /'; then
    _seamless_auto_rollback "标记 release verified 失败" "$version" || true
    exit 1
  fi
  ok "healthz + DB + running release 通过，标记 verified"

  # The candidate is now serving through nginx. Persist the active pointer and
  # only then drain the former systemd unit; a failed drain never takes the
  # new candidate out of service.
  if ! remote_ssh "systemctl stop '$active_service'"; then
    warn "旧实例停止失败；候选仍保持 active，需人工清理 $active_service"
  else
    ok "旧实例已进入 graceful drain"
  fi
  remote_ssh "rm -f '$REMOTE_ROOT/slots/$active_port' '$REMOTE_ROOT/run/candidate-port' '$REMOTE_ROOT/run/candidate-service'" || true
  UPGRADE_BANNER_ACTIVE=0
  ok "blue-green handoff complete (old=${old_version:-unknown}, new=$version, active_port=$candidate_port)"
  fi

  # 9.6 安装日志轮转配置 (按 systemd unit 模式自动分支)
  # 必做：服务已 healthz OK，再装轮转即便失败也不影响 deploy。
  #   - StandardOutput=append:/var/log/... → logrotate + copytruncate (245)
  #   - StandardOutput=journal / StandardError=inherit → systemd-journald drop-in (154)
  #   - 其它 → warn skip (无标准轮转路径)
  log "[9.6/9] 配置 stderr/stdout 日志轮转 (按 unit 模式自动分支)"
  local _std_out _std_err _target_rotate
  # 2026-08-17: drop `systemctl show ... --value` and parse the KEY=VALUE line ourselves.
  # Older systemd builds (<230) or vendor-stripped systemctl refuse `--value` with
  # "unrecognized option", which would silently mis-detect the unit mode and skip logrotate.
  _std_out="$(remote_ssh "systemctl show $SERVICE_NAME --property=StandardOutput" 2>/dev/null | sed -n 's/^StandardOutput=//p' | tail -1)"
  _std_err="$(remote_ssh "systemctl show $SERVICE_NAME --property=StandardError" 2>/dev/null | sed -n 's/^StandardError=//p' | tail -1)"
  : "${_std_out:=}"
  : "${_std_err:=}"
  log "  unit mode: StandardOutput=${_std_out:-<unset>} StandardError=${_std_err:-<unset>}"
  # decide: systemctl exposes the sink kind (append/journal/inherit), not
  # the configured path, so match the two-property pair directly.
  case "${_std_out}:${_std_err}" in
    append:*|*:append) _target_rotate="logrotate" ;;
    journal:*|*:journal|inherit:*|*:inherit)
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
  local current_active_port current_active_service canonical_port
  current_active_port=$($SSH_CMD "cat '$REMOTE_ROOT/run/active-port' 2>/dev/null" 2>/dev/null || true)
  current_active_service=$($SSH_CMD "cat '$REMOTE_ROOT/run/active-service' 2>/dev/null" 2>/dev/null || true)
  canonical_port=$(target_field "$TARGET" active_port)
  if [[ -n "$current_active_port" && ( "$current_active_port" != "$canonical_port" || "$current_active_service" != "$SERVICE_NAME" ) ]]; then
    # A previous blue-green deploy may leave a canary serving through Nginx
    # while the canonical unit is stopped. Roll back on the alternate port,
    # switch upstream, then update the release/state pointers atomically.
    log "检测到 canary active (${current_active_service:-unknown}:${current_active_port})，使用 canonical ${SERVICE_NAME}:${canonical_port} 回滚"
    if ! $SSH_CMD "set -e; systemctl stop '$SERVICE_NAME' >/dev/null 2>&1 || true; deadline=\$((\$(date +%s)+45)); while systemctl is-active --quiet '$SERVICE_NAME'; do if [ \"\$(date +%s)\" -ge \"\$deadline\" ]; then systemctl status '$SERVICE_NAME' --no-pager >&2 || true; exit 1; fi; sleep 1; done; ln -sfn '$REMOTE_ROOT/releases/$target_version' '$REMOTE_ROOT/slots/$canonical_port'; systemctl daemon-reload; systemctl start '$SERVICE_NAME'"; then
      err "canonical rollback unit 启动失败，保持现有 canary 流量"
      exit 1
    fi
    if ! remote_probe "http://127.0.0.1:${canonical_port}/healthz" 60 >/dev/null; then
      err "canonical rollback healthz 失败，保持现有 canary 流量"
      exit 1
    fi
    if ! zd_switch_upstream "$SSH_CMD" "$REMOTE_ROOT/run/active-upstream.conf" "$canonical_port"; then
      err "回滚 upstream 切换失败，保持现有 canary 流量"
      exit 1
    fi
    if ! $SSH_CMD "set -e; ln -sfn '$REMOTE_ROOT/releases/$target_version' '$REMOTE_ROOT/current'; ln -sfn '$REMOTE_ROOT/current/$BIN_NAME' '$REMOTE_ROOT/$BIN_NAME'; ln -sfn '$REMOTE_ROOT/current/web' '$REMOTE_ROOT/web'; ln -sfn '$REMOTE_ROOT/current/version.json' '$REMOTE_ROOT/version.json'; printf '%s\n' '$canonical_port' > '$REMOTE_ROOT/run/active-port'; printf '%s\n' '$SERVICE_NAME' > '$REMOTE_ROOT/run/active-service'; systemctl stop '$current_active_service' >/dev/null 2>&1 || true; rm -f '$REMOTE_ROOT/slots/$current_active_port'"; then
      err "回滚状态指针更新失败；请检查 Nginx 与 systemd"
      exit 1
    fi
    if _verify_running_release "$target_version" && deploy_preflight_pg_from_remote_env "$SSH_CMD" "$(_env_file_for_target)"; then
      ok "蓝绿回滚完成 → $target_version (canonical active)"
    else
      err "蓝绿回滚后 running release/DB 校验失败"
      exit 1
    fi
    if upgrade_hide_all >/dev/null 2>&1; then UPGRADE_BANNER_ACTIVE=0; else warn "回滚成功但升级静态页撤掉失败，保留 marker 保护流量"; exit 1; fi
    return 0
  fi

  UPGRADE_BANNER_ACTIVE=1
  if ! upgrade_show_all "$target_version"; then
    err "升级静态页启用失败，中止回滚"
    exit 1
  fi
  UPGRADE_SWITCH_STARTED=1
  log "原子切换 + restart..."
  if ! host_atomic_switch "$SSH_CMD" "$TARGET" "$target_version" 2>&1 | sed 's/^/    /'; then
    err "回滚切换或 restart 失败"
    exit 1
  fi
  if host_wait_healthy "$SSH_CMD" "$TARGET" 60 2>&1 \
    && _verify_running_release "$target_version" \
    && deploy_preflight_pg_from_remote_env "$SSH_CMD" "$(_env_file_for_target)"; then
    ok "回滚完成 → $target_version (healthz + running release + PG OK)"
    if upgrade_hide_all >/dev/null 2>&1; then
      UPGRADE_BANNER_ACTIVE=0
    else
      warn "回滚成功但升级静态页撤掉失败，保留 marker 保护流量"
      exit 1
    fi
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

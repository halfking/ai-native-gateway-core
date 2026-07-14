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
#   bash scripts/deploy-seamless.sh deploy 154 --seq 1005   # 部署到 154
#   bash scripts/deploy-seamless.sh rollback 245            # 一键回滚
#   bash scripts/deploy-seamless.sh rollback 154
#   bash scripts/deploy-seamless.sh status 245              # 查看 releases
#   bash scripts/deploy-seamless.sh deploy 245 --no-frontend --seq 1004
#
# 安全网:
#   - healthz 失败 → 自动 rollback 到上一个 verified 版本
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
# shellcheck source=deploy-lib/host.sh
source "$SCRIPT_DIR/deploy-lib/host.sh"

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
    -h|--help)
      sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) err "未知参数: $1"; exit 1 ;;
  esac
done

[[ -n "$ACTION" ]] || { err "用法: deploy-seamless.sh <deploy|rollback|status> <245|154> [--seq N]"; exit 1; }
[[ -n "$TARGET" ]] || { err "缺少目标 (245|154)"; exit 1; }

case "$TARGET" in
  154|245) ;;
  *) err "不支持的目标: $TARGET (仅 154|245)"; exit 1 ;;
esac

# ── SSH 命令构造 ────────────────────────────────────────────────
# 关键设计：host.sh 内部用 "$ssh_cmd" "remote-shell-cmd" 调用，引号会把
# ssh_cmd 当成一个整体单词。因此 ssh_cmd 不能是带空格的字符串（"ssh -i ... root@host"），
# 必须是一个无空格的「名字」——我们用一个 bash 函数 remote_ssh 来包装。
# host.sh 调用 "remote_ssh" "ls /tmp" → bash 展开成 remote_ssh "ls /tmp"
# → 函数体内用 eval ssh ... "$1" 真正执行。
SSH_PORT=25022
SSH_KEY_FILE="${SSH_KEY_FILE:-}"
for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
  if [[ -f "$k" ]]; then SSH_KEY_FILE="$k"; break; fi
done

SSH_HOST=$(target_field "$TARGET" ssh_host)

# remote_ssh <remote-shell-command> — host.sh 的每个函数都通过这个名字调用。
# 用 eval 把 SSH_ARGS (含空格的 ssh 选项) 正确分词后执行。
# 2026-07-14: ConnectTimeout=20 + ServerAliveInterval=5 (154 公网 IP 偶发抖动)。
remote_ssh() {
  if [[ -n "${SSH_KEY_FILE:-}" ]] && [[ -f "$SSH_KEY_FILE" ]]; then
    ssh -i "$SSH_KEY_FILE" -p "$SSH_PORT" \
      -o StrictHostKeyChecking=accept-new -o BatchMode=yes \
      -o ConnectTimeout=20 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 \
      "$SSH_HOST" "$1"
  else
    sshpass -e ssh -p "$SSH_PORT" \
      -o StrictHostKeyChecking=accept-new \
      -o ConnectTimeout=20 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 \
      "$SSH_HOST" "$1"
  fi
}

# remote_ssh_pipe <remote-shell-command> — 用于 tar 管道，透传 stdin。
remote_ssh_pipe() {
  if [[ -n "${SSH_KEY_FILE:-}" ]] && [[ -f "$SSH_KEY_FILE" ]]; then
    ssh -i "$SSH_KEY_FILE" -p "$SSH_PORT" \
      -o StrictHostKeyChecking=accept-new -o BatchMode=yes \
      -o ConnectTimeout=20 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 \
      "$SSH_HOST" "$1"
  else
    sshpass -e ssh -p "$SSH_PORT" \
      -o StrictHostKeyChecking=accept-new \
      -o ConnectTimeout=20 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 \
      "$SSH_HOST" "$1"
  fi
}

# host.sh 函数签名要求 ssh_cmd 是一个「可被 "$ssh_cmd" 调用的名字」。
# 传函数名 "remote_ssh"，host.sh 内部 "$ssh_cmd" "cmd" → "remote_ssh" "cmd"
# → bash 调用函数 remote_ssh "cmd"。✓
SSH_CMD="remote_ssh"

# upload 不用 scp，用 tar 管道走 ssh (单连接，更可靠)。
REMOTE_ROOT=$(host_root_for "$TARGET")
SERVICE_NAME=$(target_field "$TARGET" service_name)
HEALTH_URL=$(target_field "$TARGET" health_url)
BIN_NAME=$(host_binary_name "$TARGET")

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

  # 2. 前端构建
  if [[ "$SKIP_FRONTEND" == "false" ]]; then
    log "[2/9] 前端构建"
    (cd web && npm run build 2>&1 | tail -3)
    ok "web/dist 已生成"
  else
    log "[2/9] 跳过前端 (--no-frontend)"
  fi

  # 3. Go 交叉编译
  log "[3/9] Go 交叉编译 linux/amd64"
  local tmpbin="/tmp/__seamless_${TARGET}_binary"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
    -o "$tmpbin" ./cmd/gateway
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

  # 7. adopt 检测
  log "[7/9] adopt 检测"
  if ! $SSH_CMD "test -L '$REMOTE_ROOT/current'" 2>/dev/null; then
    warn "current 符号链接不存在 → 执行 adopt"
    do_adopt || { err "adopt 失败，中止"; exit 1; }
  else
    ok "已采用 releases/ 布局"
  fi

  # 8. atomic switch + restart
  log "[8/9] 原子符号链接切换 + restart"
  host_atomic_switch "$SSH_CMD" "$TARGET" "$version" 2>&1 | sed 's/^/    /' || true
  ok "符号链接已切换到 releases/$version"

  # 9. wait healthy (失败自动回滚)
  log "[9/9] 等待 /healthz (60s 超时)"
  if host_wait_healthy "$SSH_CMD" "$TARGET" 60 2>&1; then
    host_mark_verified "$SSH_CMD" "$TARGET" "$version" 2>&1 | sed 's/^/    /' || true
    ok "healthz 通过，标记 verified"
  else
    err "healthz 超时! 自动回滚到上一个 verified 版本..."
    local prev
    prev=$(host_select_rollback_target "$SSH_CMD" "$TARGET" "$version" 2>/dev/null || true)
    if [[ -n "$prev" ]]; then
      warn "回滚到 releases/$prev"
      host_atomic_switch "$SSH_CMD" "$TARGET" "$prev" 2>&1 | sed 's/^/    /' || true
      host_wait_healthy "$SSH_CMD" "$TARGET" 30 2>&1 && ok "已回滚到 $prev" || err "回滚后 healthz 仍失败!"
    else
      err "无可用回滚目标! 手动检查: $SSH_CMD 'systemctl status $SERVICE_NAME'"
    fi
    exit 1
  fi

  # 清理本地临时文件
  rm -f "$tmpbin"; rm -rf "$bundle_dir"

  # prune: 保留所有 verified + active，仅清理 unverified 的失败尝试 (保留最新 2 个用于排查)。
  # 不用 host_prune_releases (它的 timestamp 排序对 adopted/legacy 版本有 bug，会误删 verified legacy)。
  prune_releases_safe "$version"

  local elapsed=$(( $(date +%s) - deploy_start ))
  echo ""
  ok "✅ $TARGET 部署完成 ($elapsed s) — version=$version seq=$seq_val"
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
  if host_wait_healthy "$SSH_CMD" "$TARGET" 60 2>&1; then
    ok "回滚完成 → $target_version"
    $SSH_CMD "curl -fsS '$HEALTH_URL' >/dev/null && echo '  healthz OK'" 2>/dev/null || true
  else
    err "回滚后 healthz 失败! 手动检查"
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

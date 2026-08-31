#!/usr/bin/env bash
# =====================================================================
# scripts/ssh-wrapper-154.sh — 154 SSH wrapper via 252 jump host
#
# Trigger: any SSH invocation whose target matches injected host values for
# the canonical 154 deployment target.
#
# Effect: rewrites the call so it actually runs
#     ssh -> injected jump host -> sshpass -e ssh injected target
#
# Why this exists (2026-07-20):
#   - The canonical target's direct SSH path is unavailable, so the wrapper
#     routes through an injected jump host.
#
# 用法:
#   PATH=scripts:$PATH bash scripts/deploy-seamless.sh deploy 154
#   PATH=scripts:$PATH bash scripts/deploy-seamless.sh status 154
#   PATH=scripts:$PATH bash scripts/deploy-seamless.sh rollback 154
#
# 任何想直接 ssh 154 的脚本, 只需把 scripts/ 加到 PATH 前面.
#
# 注意:
#   - 内部 exec 必须用绝对路径 (/usr/bin/ssh, /usr/bin/sshpass),
#     避免 PATH 中含本 wrapper 时递归死循环.
#   - 用 -q -o LogLevel=QUIET 抑制 "Warning: Permanently added ... to the
#     list of known hosts" 这类 stderr 噪音, 避免污染 deploy-seamless
#     解析 stdout (之前因为这条 Warning, ln -sfn 把 current 链接指向了
#     一个名为 "Warning: ..." 的伪目录, systemd 立即 203/EXEC).
# =====================================================================
set -e
# All connection details must be injected by env-injector. The target may use
# key authentication by default; password authentication is enabled only when
# an explicit SSHPASS_154/DEPLOY_SSH_PASS value is present.
HOP_KEY=${SSH_WRAPPER_HOP_KEY:-}
HOP_HOST=${SSH_WRAPPER_HOP_HOST:-}
HOP_PORT=${SSH_WRAPPER_HOP_PORT:-25022}
TARGET_KEY=${SSH_WRAPPER_TARGET_KEY:-}
TARGET_PASS=${SSHPASS_154:-${DEPLOY_SSH_PASS:-}}
TARGET_IP=${SSH_WRAPPER_TARGET_IP:-}
TARGET_HOST=${SSH_WRAPPER_TARGET_HOST:-}
TARGET_PRIVATE_HOST=${SSH_WRAPPER_TARGET_PRIVATE_HOST:-}
TARGET_PORT=${SSH_WRAPPER_TARGET_PORT:-25022}
SSH_BIN=${SSH_BIN:-/usr/bin/ssh}
SSHPASS_BIN=${SSHPASS_BIN:-/usr/bin/sshpass}

if [[ -z "$HOP_KEY" || -z "$HOP_HOST" || ( -z "$TARGET_KEY" && -z "$TARGET_PASS" ) || -z "$TARGET_IP" || -z "$TARGET_HOST" ]]; then
  echo "ssh-wrapper-154: inject hop key/host, target IP/host, and target key or explicit password" >&2
  exit 64
fi


# 解析 ssh 参数: 找 host (root@IP) 和 remote command (剩余 args)
HOST=""
REMOTE_ARGS=()
ORIGINAL_ARGS=("$@")
while [[ $# -gt 0 ]]; do
  case "$1" in
    -i|-p|-o|-F|-S|-J|-l)
      shift 2 ;;                                # 跳过带值的 ssh 选项
    -*) shift ;;                                # 跳过无值 ssh 选项
    *@*)
      HOST="$1"
      shift
      REMOTE_ARGS=("$@")
      break
      ;;
    *) shift ;;
  esac
done

# 只对 154 重定向
if [[ "$HOST" == *@"$TARGET_HOST" ]] || [[ -n "$TARGET_PRIVATE_HOST" && "$HOST" == *@"$TARGET_PRIVATE_HOST" ]]; then
  TARGET_USER="${HOST%@*}"
  SSH_ARGS=(-q -p "$TARGET_PORT")
  [[ -n "$TARGET_KEY" ]] && SSH_ARGS+=(-i "$TARGET_KEY")
  SSH_ARGS+=(
    -o StrictHostKeyChecking=no
    -o UserKnownHostsFile=/dev/null
    -o LogLevel=QUIET
    -o "ProxyCommand=$SSH_BIN -i $HOP_KEY -p $HOP_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=QUIET -W %h:%p $HOP_HOST"
    "$TARGET_USER@$TARGET_IP" "${REMOTE_ARGS[@]}"
  )
  if [[ -n "$TARGET_PASS" ]]; then
    exec env SSHPASS="$TARGET_PASS" "$SSHPASS_BIN" -e "$SSH_BIN" "${SSH_ARGS[@]}"
  fi
  exec "$SSH_BIN" "${SSH_ARGS[@]}"
fi

# 其他 host 不重定向, 加 -q 抑制 Warning
exec "$SSH_BIN" -q "${ORIGINAL_ARGS[@]}"

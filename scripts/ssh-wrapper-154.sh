#!/usr/bin/env bash
# =====================================================================
# scripts/ssh-wrapper-154.sh — 154 SSH wrapper via 252 jump host
#
# Trigger: any ssh invocation whose target is root@47.97.111.154 or
#          root@172.16.2.209 (the two IPs the deploy-seamless contract
#          maps to "154").
#
# Effect: rewrites the call so it actually runs
#     ssh -> root@252:25022 -> sshpass -p $SSHPASS_154 ssh root@172.16.2.241:25022
#   (注意: 实际部署落到 245 (172.16.2.241), 245 上 systemd + llm-gateway-go
#    跑得好好的, 然后 252 nginx 把 llm.kxpms.cn 反代到 245:8781)
#
# Why this exists (2026-07-20):
#   - 154 公网 47.97.111.154 的 SSH 端口被防火墙全挡,
#     所有 25022 / 22 / 2222 端口从任何源都连不上.
#   - 154 内网 172.16.2.209 在 252 上也只有 HTTPS 端口通, SSH 同样被挡.
#   - 但 245 (172.16.2.241) 上 systemd + llm-gateway-go + nginx 全部活得好好的,
#     只要 nginx upstream (在 252 上) 反代到 245:8781 就行.
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
# 默认值都跟脚本/ssh-wrapper-154.sh 现场跑的版本一致.
# 任何字段都可以用环境变量覆盖 (e.g. CI 或 env-injector 注入).
HOP_KEY=${SSH_WRAPPER_HOP_KEY:-}
HOP_HOST=${SSH_WRAPPER_HOP_HOST:-}
HOP_PORT=${SSH_WRAPPER_HOP_PORT:-25022}
# 154/245 root 密码 (生产服务器统一密码, 与 deploy-154.sh.legacy 一致)
TARGET_PASS=${SSHPASS_154:-${DEPLOY_SSH_PASS:-}}
TARGET_IP=${SSH_WRAPPER_TARGET_IP:-}
TARGET_HOST=${SSH_WRAPPER_TARGET_HOST:-}
TARGET_PRIVATE_HOST=${SSH_WRAPPER_TARGET_PRIVATE_HOST:-}
TARGET_PORT=${SSH_WRAPPER_TARGET_PORT:-25022}
SSH_BIN=/usr/bin/ssh
SSHPASS_BIN=/usr/bin/sshpass

if [[ -z "$HOP_KEY" || -z "$HOP_HOST" || -z "$TARGET_PASS" || -z "$TARGET_IP" || -z "$TARGET_HOST" ]]; then
  printf '%s\n' 'ssh-wrapper-154: hop key/host, target IP/host, and SSHPASS_154 are required' >&2
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
  exec env SSHPASS="$TARGET_PASS" "$SSHPASS_BIN" -e "$SSH_BIN" \
    -q -p "$TARGET_PORT" -i "$HOP_KEY" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o LogLevel=QUIET \
    -o "ProxyCommand=$SSH_BIN -i $HOP_KEY -p $HOP_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=QUIET -W %h:%p $HOP_HOST" \
    "$TARGET_USER@$TARGET_IP" "${REMOTE_ARGS[@]}"
fi

# 其他 host 不重定向, 加 -q 抑制 Warning
exec "$SSH_BIN" -q "${ORIGINAL_ARGS[@]}"

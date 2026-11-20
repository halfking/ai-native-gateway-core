#!/usr/bin/env bash
# =====================================================================
# scripts/backup-211-config.sh — 211 配置自动备份（专题四 D1）
# =====================================================================
# 备份内容（均为部署面"丢了就要重配"的东西）：
#   · /opt/llm-gateway-go-211/env/          —— gateway.env + gateway.secret
#       ⚠ gateway.secret 路径不可改：SECRET_KEY 与库内 api_keys.key_hash
#         HMAC 绑定（见 scripts/deploy-211.sh 头注）。secret 以 600 权限入档。
#   · /etc/nginx/conf.d/ns-gateway-211.conf —— 蓝绿 upstream 切流配置
# 归档位置：/opt/llm-gateway-go-211/backups/config-<UTC时间戳>.tar.gz（600）
#
# 用法：
#   bash scripts/backup-211-config.sh                 # 备份一次（远端落档+轮转）
#   bash scripts/backup-211-config.sh --list          # 只列远端现有备份
#   bash scripts/backup-211-config.sh --pull          # 备份并拉回本机 backups/211-config/
#   bash scripts/backup-211-config.sh --keep 30       # 自定义保留份数（默认 14）
#   bash scripts/backup-211-config.sh --via-mesh      # 走 netbird 中继（外网位置）
#
# cron 形态（211 侧每日 03:17）：
#   17 3 * * * /bin/bash /root/llm-gateway-go-211/scripts/backup-211-config.sh >> \
#     /opt/llm-gateway-go-211/backups/backup.log 2>&1
# （仓内脚本路径在远端仓根 /root/llm-gateway-go-211，与 deploy 引擎的远端布局一致。）
# =====================================================================
set -euo pipefail

# ── 参数 ──────────────────────────────────────────────────────────────
KEEP=14
MODE="backup"          # backup | list
PULL=0
SSH_ALIAS="211"
while [ $# -gt 0 ]; do
  case "$1" in
    --list)      MODE="list" ;;
    --pull)      PULL=1 ;;
    --keep)      KEEP="${2:?--keep 需要数字}"; shift ;;
    --via-mesh)  SSH_ALIAS="211-mesh" ;;   # 外网通道（见 deploy-211.sh 双通道注记）
    -h|--help)   sed -n '2,26p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
  shift
done

# ── macOS bash 版本前置（与 deploy-211.sh 同款检查，本脚本自身只用 bash3 兼容语法，
#    但保持家族一致的提示口径）─────────────────────────────────────────
if [ -n "${BASH_VERSION:-}" ]; then
  bash_major="${BASH_VERSION%%.*}"
  if [ "${bash_major}" -lt 4 ] && [ "$(uname -s)" = "Darwin" ]; then
    echo "提示: macOS 自带 bash 3.2 可运行本脚本（仅用兼容语法）。" >&2
  fi
fi

STATE_DIR="/opt/llm-gateway-go-211"
BACKUP_DIR="${STATE_DIR}/backups"
NGINX_CONF="/etc/nginx/conf.d/ns-gateway-211.conf"

# ── 连通前置 ──────────────────────────────────────────────────────────
if ! ssh -o ConnectTimeout=8 -o BatchMode=yes "${SSH_ALIAS}" true 2>/dev/null; then
  echo "❌ ssh ${SSH_ALIAS} 不可达（内网不可用时试 --via-mesh）" >&2
  exit 1
fi

run_remote() { ssh -o BatchMode=yes "${SSH_ALIAS}" "$@"; }

# ── list 模式 ─────────────────────────────────────────────────────────
if [ "${MODE}" = "list" ]; then
  echo "== ${SSH_ALIAS}:${BACKUP_DIR} =="
  run_remote "ls -lht ${BACKUP_DIR}/config-*.tar.gz 2>/dev/null || echo '(空——尚无备份)'"
  exit 0
fi

# ── backup 模式 ───────────────────────────────────────────────────────
TS="$(date -u +%Y%m%dT%H%M%SZ)"
REMOTE_SCRIPT=$(cat <<EOS
set -euo pipefail
mkdir -p '${BACKUP_DIR}'
OUT='${BACKUP_DIR}/config-${TS}.tar.gz'
# 归档内容显式列举（secret 必须在档——丢了它全部 key 失效且不可再生成）
tar -czf "\${OUT}.tmp" \
    -C '${STATE_DIR}' env/gateway.env env/gateway.secret \
    -C / etc/nginx/conf.d/ns-gateway-211.conf 2>/dev/null || \
tar -czf "\${OUT}.tmp" -C '${STATE_DIR}' env/gateway.env env/gateway.secret
chmod 600 "\${OUT}.tmp"
# 完整性自证：能列出全部成员才算备份成功
tar -tzf "\${OUT}.tmp" >/dev/null
mv "\${OUT}.tmp" '${BACKUP_DIR}/config-${TS}.tar.gz'
chmod 600 '${BACKUP_DIR}/config-${TS}.tar.gz'
# 轮转：只保留最新 ${KEEP} 份（按名字排序 = 按时间戳排序）
ls -1t '${BACKUP_DIR}'/config-*.tar.gz 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -f
echo "ARCHIVED: ${BACKUP_DIR}/config-${TS}.tar.gz"
echo "--- 成员清单 ---"
tar -tzf '${BACKUP_DIR}/config-${TS}.tar.gz'
echo "--- 现存 ${KEEP} 份内备份 ---"
ls -1t '${BACKUP_DIR}'/config-*.tar.gz | head -n ${KEEP}
EOS
)

echo "== 执行远端备份（${SSH_ALIAS}）=="
run_remote "bash -s" <<< "${REMOTE_SCRIPT}"

# ── 可选拉回本机 ──────────────────────────────────────────────────────
if [ "${PULL}" = "1" ]; then
  LOCAL_DIR="$(cd "$(dirname "$0")/.." && pwd)/backups/211-config"
  mkdir -p "${LOCAL_DIR}"
  scp -q -o BatchMode=yes "${SSH_ALIAS}:${BACKUP_DIR}/config-${TS}.tar.gz" "${LOCAL_DIR}/"
  chmod 600 "${LOCAL_DIR}/config-${TS}.tar.gz"
  echo "PULLED: ${LOCAL_DIR}/config-${TS}.tar.gz"
fi

echo "✅ 备份完成（保留最近 ${KEEP} 份）"

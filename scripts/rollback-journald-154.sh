#!/usr/bin/env bash
# =====================================================================
# scripts/rollback-journald-154.sh — 154 stderr journald drop-in 独立回滚脚本
#
# 用途:
#   撤销 2026-08-04 commit f4dbfe6d5 部署的 journald drop-in 限制
#   (/etc/systemd/journald.conf.d/llm-gateway-go.conf), 恢复 /var/log/journal/
#   大小上限为 systemd 默认 (15% 磁盘)。
#
# 触发场景 (按规则触发 rollback):
#   - journald restart 后服务异常 (systemd-journald crash / 启动失败)
#   - llm-gateway-go 写入异常 (journal 限制导致日志写入阻塞)
#   - 监控告警 journal 大小/数量超阈值
#   - 老板决策回退 (任意时刻)
#
# 流程 (rule 03 §7.0):
#   1. 自动定位最新备份目录 /tmp/lg154-backup-*
#   2. 二次确认 (防误操作)
#   3. 备份当前状态 (防 rollback 二次失败)
#   4. rm /etc/systemd/journald.conf.d/llm-gateway-go.conf
#   5. cp <backup>/journald.conf.orig /etc/systemd/journald.conf (保险)
#   6. systemctl restart systemd-journald
#   7. L1-L4 验证:
#      L1: systemd-journald active, llm-gateway-go active
#      L2: DB / 依赖连通 (沿用 service 自身的 healthz, 见 deploy-seamless.sh)
#      L3: journalctl -u llm-gateway-go -n 5 能读
#      L4: curl https://llm.kxpms.cn/health 返回非 5xx
#   8. 报告 rollback 状态
#
# 用法:
#   bash scripts/rollback-journald-154.sh                       # 自动找最新备份
#   bash scripts/rollback-journald-154.sh --backup-dir=PATH    # 指定备份目录
#   bash scripts/rollback-journald-154.sh --yes                # 跳过确认 (CI 场景)
#
# 依赖: ssh + rsync/scp 不需要, 直接在目标机器跑或经 ssh 跑
#
# 引用: rule 03 §7.0 (部署前备案 + 独立回滚脚本) + rule 09 §5.2 二次确认
# =====================================================================

set -euo pipefail

# ── 配置 ─────────────────────────────────────────────────────────
# 154 目标机 (此脚本在 154 上跑, 或经 ssh 跑)
SSH_HOST="${SSH_HOST:-47.97.111.154}"
SSH_PORT="${SSH_PORT:-25022}"
SSH_USER="${SSH_USER:-root}"

# 备份目录定位
BACKUP_DIR=""
SKIP_CONFIRM=false
for arg in "$@"; do
  case "$arg" in
    --backup-dir=*) BACKUP_DIR="${arg#--backup-dir=}" ;;
    --yes|-y) SKIP_CONFIRM=true ;;
    -h|--help)
      sed -n '3,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "未知参数: $arg"; exit 1 ;;
  esac
done

# ── 颜色 ─────────────────────────────────────────────────────────
if [[ -t 1 ]]; then
  G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
else
  G=''; Y=''; R=''; B=''; N=''
fi
ok()    { echo -e "${G}  ✓${N} $*"; }
info()  { echo -e "${B}  ▶${N} $*"; }
warn()  { echo -e "${Y}  !${N} $*"; }
err()   { echo -e "${R}  ✗${N} $*" >&2; }
hdr()   { echo -e "\n${B}── $* ──${N}"; }

# ── 检测是否在目标机上 ─────────────────────────────────────────
on_target=false
if [[ "$(hostname -I 2>/dev/null || hostname)" == *"47.97.111.154"* ]] \
   || [[ -f /etc/systemd/journald.conf ]]; then
  # 在 154 上跑 (有 journald.conf)
  on_target=true
fi

if ! $on_target; then
  # 不在目标机上: 转 ssh 远程执行
  info "在本地 (非 154), 转 ssh 到 root@${SSH_HOST}:${SSH_PORT}"
  # 把本脚本和 backup 目录传到远端, 远端再跑
  REMOTE_TMP="/tmp/lg154-rollback-$(date +%Y%m%d-%H%M%S)"
  ssh -p "$SSH_PORT" "root@${SSH_HOST}" "mkdir -p $REMOTE_TMP"
  scp -P "$SSH_PORT" "${BASH_SOURCE[0]}" "root@${SSH_HOST}:${REMOTE_TMP}/rollback-journald-154.sh"
  # 备份目录本地可能有, 也可能没有; 如指定了 --backup-dir, 也传过去
  if [[ -n "$BACKUP_DIR" ]]; then
    ssh -p "$SSH_PORT" "root@${SSH_HOST}" "test -d $BACKUP_DIR" || {
      scp -r -P "$SSH_PORT" "$BACKUP_DIR" "root@${SSH_HOST}:$BACKUP_DIR"
    }
  fi
  REMOTE_ARGS=()
  $SKIP_CONFIRM && REMOTE_ARGS+=("--yes")
  [[ -n "$BACKUP_DIR" ]] && REMOTE_ARGS+=("--backup-dir=$BACKUP_DIR")
  ssh -p "$SSH_PORT" "root@${SSH_HOST}" "bash $REMOTE_TMP/rollback-journald-154.sh ${REMOTE_ARGS[*]}"
  exit $?
fi

# ── 以下都在 154 上跑 ─────────────────────────────────────────

# ── Step 1: 定位备份目录 ────────────────────────────────────
hdr "Step 1: 定位备份目录"

if [[ -z "$BACKUP_DIR" ]]; then
  # 自动找最新的 lg154-backup-* 目录
  BACKUP_DIR="$(ls -1dt /tmp/lg154-backup-* 2>/dev/null | head -1 || true)"
  if [[ -z "$BACKUP_DIR" ]]; then
    err "未找到 /tmp/lg154-backup-* 目录, 请用 --backup-dir=PATH 指定"
    exit 1
  fi
  info "自动选择最新备份: $BACKUP_DIR"
fi

if [[ ! -d "$BACKUP_DIR" ]]; then
  err "备份目录不存在: $BACKUP_DIR"
  exit 1
fi

# 验证备份完整性
# 注: 列出所有必需文件, 任何一个缺失就 abort. 当前只有 journald.conf.orig
# (binary 和 service 备份保留但 rollback 不主动还原, 因为 deploy 未改过).
REQUIRED_BACKUP_FILES=("journald.conf.orig")
for f in "${REQUIRED_BACKUP_FILES[@]}"; do
  if [[ ! -f "$BACKUP_DIR/$f" ]]; then
    err "备份目录缺少必要文件: $BACKUP_DIR/$f"
    exit 1
  fi
done
ok "备份目录完整: $BACKUP_DIR"
ls -la "$BACKUP_DIR/" | sed 's/^/    /'

# ── Step 2: 显示 rollback 摘要 + 二次确认 ──────────────────
hdr "Step 2: rollback 摘要 + 确认"

DROP_IN="/etc/systemd/journald.conf.d/llm-gateway-go.conf"
JOURNALD_CONF="/etc/systemd/journald.conf"
CURRENT_JD_MD5="$(md5sum "$JOURNALD_CONF" 2>/dev/null | awk '{print $1}')"
ORIGINAL_JD_MD5="$(md5sum "$BACKUP_DIR/journald.conf.orig" | awk '{print $1}')"
DROP_IN_EXISTS="no"
[[ -f "$DROP_IN" ]] && DROP_IN_EXISTS="yes"

echo ""
info "rollback 将做:"
echo "    1. 备份当前状态到 /tmp/lg154-rollback-running-<ts>/"
echo "    2. 备份当前 drop-in (存在: $DROP_IN_EXISTS)"
echo "    3. 删除 $DROP_IN (恢复 systemd-journald 默认 15% 磁盘上限)"
echo "    4. 还原 $JOURNALD_CONF (备份 md5=$ORIGINAL_JD_MD5, 当前 md5=$CURRENT_JD_MD5)"
echo "    5. systemctl restart systemd-journald"
echo "    6. L1-L4 验证"

if [[ "$CURRENT_JD_MD5" != "$ORIGINAL_JD_MD5" ]]; then
  warn "journald.conf 当前 md5 跟备份不一致 ($CURRENT_JD_MD5 vs $ORIGINAL_JD_MD5)"
  warn "说明 deploy 后被改过, restore 会覆盖当前值"
fi

if ! $SKIP_CONFIRM; then
  echo ""
  read -r -p "$(echo -e "${Y}确认回滚? (yes/no): ${N}")" CONFIRM
  if [[ "$CONFIRM" != "yes" ]]; then
    info "已取消"
    exit 0
  fi
fi

# ── Step 3: 备份当前状态 (rollback 过程可能再失败, 留个底) ───
hdr "Step 3: 备份当前状态"
RUNNING_TS="$(date +%Y%m%d-%H%M%S)"
RUNNING_BAK="/tmp/lg154-rollback-running-${RUNNING_TS}"
mkdir -p "$RUNNING_BAK"
info "当前状态备份到 $RUNNING_BAK"
cp -p "$JOURNALD_CONF" "$RUNNING_BAK/journald.conf.running" 2>/dev/null || true
[[ -f "$DROP_IN" ]] && cp -p "$DROP_IN" "$RUNNING_BAK/llm-gateway-go.conf.running" || true
md5sum "$JOURNALD_CONF" 2>/dev/null | awk -v bak="$RUNNING_BAK/journald.conf.running" '{print $1, $2, bak}' > "$RUNNING_BAK/MANIFEST" 2>/dev/null || true
ok "当前状态已保存"

# ── Step 4: rm drop-in ────────────────────────────────────
hdr "Step 4: 删除 drop-in"
if [[ -f "$DROP_IN" ]]; then
  rm -f "$DROP_IN"
  ok "已删除 $DROP_IN"
else
  info "$DROP_IN 不存在 (skip)"
fi

# ── Step 5: 还原 journald.conf (保险) ──────────────────────
hdr "Step 5: 还原 journald.conf (保险, 主文件实际未被改)"
if [[ -f "$BACKUP_DIR/journald.conf.orig" ]]; then
  cp -p "$BACKUP_DIR/journald.conf.orig" "$JOURNALD_CONF"
  NEW_MD5="$(md5sum "$JOURNALD_CONF" | awk '{print $1}')"
  if [[ "$NEW_MD5" == "$ORIGINAL_JD_MD5" ]]; then
    ok "journald.conf 已还原 (md5=$NEW_MD5)"
  else
    err "journald.conf 还原后 md5 不匹配 (got=$NEW_MD5 expected=$ORIGINAL_JD_MD5)"
    err "已半回滚: drop-in 已删, journald.conf md5 不一致, 请人工介入"
    exit 1
  fi
else
  warn "$BACKUP_DIR/journald.conf.orig 不存在, 跳过"
fi

# ── Step 6: restart systemd-journald ──────────────────────
hdr "Step 6: restart systemd-journald"
if systemctl restart systemd-journald.service 2>&1; then
  ok "systemd-journald restarted"
else
  err "systemd-journald restart 失败"
  err "已半回滚: drop-in 已删, journald.conf 已还原, 但 restart 失败, 请人工介入"
  exit 1
fi

# ── Step 7: L1-L4 验证 ────────────────────────────────────
hdr "Step 7: L1-L4 验证"
L1_PASS=false; L2_PASS=false; L3_PASS=false; L4_PASS=false

# L1: systemd-journald active
JD_STATE="$(systemctl is-active systemd-journald.service 2>&1 || echo unknown)"
if [[ "$JD_STATE" == "active" ]]; then
  ok "L1 systemd-journald: active"
  L1_PASS=true
else
  err "L1 systemd-journald: $JD_STATE (期望 active)"
fi

# L1+: llm-gateway-go active (rollback 不该影响它, 但验证一下)
GW_STATE="$(systemctl is-active llm-gateway-go 2>&1 || echo unknown)"
if [[ "$GW_STATE" == "active" ]]; then
  ok "L1 llm-gateway-go: active"
else
  err "L1 llm-gateway-go: $GW_STATE (rollback 不该影响它, 但当前 inactive)"
fi

# L2: drop-in 确认已删
if [[ ! -f "$DROP_IN" ]]; then
  ok "L2 drop-in 已删除"
  L2_PASS=true
else
  err "L2 drop-in 仍在: $DROP_IN"
fi

# L2+: journal 大小上限恢复 systemd 默认 (会显示 4G 上限, 但旧 journal 已被截断)
JOURNAL_SIZE="$(journalctl --disk-usage 2>&1 | head -1)"
info "    journal 大小: $JOURNAL_SIZE"

# L3: journalctl 能读 llm-gateway-go 最近日志
if journalctl -u llm-gateway-go --no-pager -n 1 >/dev/null 2>&1; then
  ok "L3 journalctl -u llm-gateway-go 可读"
  L3_PASS=true
else
  err "L3 journalctl 读失败"
fi

# L4: 业务健康 (curl healthz)
HEALTH_CODE="$(curl -s -o /dev/null -w '%{http_code}' https://llm.kxpms.cn/health 2>&1 || echo 000)"
if [[ "$HEALTH_CODE" =~ ^[23] ]]; then
  ok "L4 https://llm.kxpms.cn/health: HTTP $HEALTH_CODE"
  L4_PASS=true
else
  warn "L4 https://llm.kxpms.cn/health: HTTP $HEALTH_CODE (期望 2xx/3xx, 但服务可能仍可访问)"
fi

# ── Step 8: 报告 ─────────────────────────────────────────
hdr "Step 8: rollback 报告"

if $L1_PASS && $L2_PASS && $L3_PASS && [[ "$L4_PASS" == "true" || "$HEALTH_CODE" == "302" ]]; then
  ok "rollback 成功"
  echo ""
  info "  drop-in 已删除: $DROP_IN"
  info "  journald.conf md5: $ORIGINAL_JD_MD5 (跟部署前一致)"
  info "  systemd-journald: active"
  info "  llm-gateway-go: active"
  info "  /var/log/journal/ 大小上限恢复为 systemd 默认 (15% 磁盘)"
  info "  running 备份: $RUNNING_BAK (如需 redo 可恢复)"
  echo ""
  info "  后续: journal 会按 systemd 默认上限 (15% 磁盘) 增长, 154 磁盘 99G 可承受"
  exit 0
fi

err "rollback 完成但部分验证失败, 已半回滚"
err "  L1 systemd-journald: $([[ $L1_PASS ]] && echo PASS || echo FAIL)"
err "  L2 drop-in 删除: $([[ $L2_PASS ]] && echo PASS || echo FAIL)"
err "  L3 journalctl 可读: $([[ $L3_PASS ]] && echo PASS || echo FAIL)"
err "  L4 healthz: HTTP $HEALTH_CODE"
err "请人工介入排查"
exit 1
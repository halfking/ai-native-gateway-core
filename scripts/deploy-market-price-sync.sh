#!/usr/bin/env bash
# deploy-market-price-sync.sh —— 把仓库里的行情同步器**幂等安装**到采集宿主。
#
# ★ 为什么需要它（2026-11-17，211 真库实测）：
#   宿主 cron 跑的是 `/root/market-price-sync.py`，而仓库里的脚本在
#   `scripts/market-price-sync.py`。两份副本靠**人工拷贝**保持一致 ⇒
#   实测漂移：宿主副本 md5=0bcfefdd…（**没有** 970 平台现价写入段），
#   仓库副本 md5=971e34f8…（有）⇒ 平台现价表恒为 0 行，
#   而同步台账看起来一切正常（落地 1084 行、无报错）⇒ **静默断供**。
#   ⇒ 本脚本把「仓库 = 采集宿主上的运行副本」变成**一条命令**，
#     并在安装后用 **sha256 对拍**自证一致（不是「我拷过去了」，是「两边指纹相同」）。
#
# 用法：
#   scripts/deploy-market-price-sync.sh [--ssh 211] [--target /root/market-price-sync.py] [--dry-run]
#
# 退出码：0 = 已安装且指纹一致；1 = 失败（连接/校验/安装报错）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${ROOT}/scripts/market-price-sync.py"

SSH_HOST="211"
TARGET="/root/market-price-sync.py"
DRY_RUN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --ssh)    SSH_HOST="$2"; shift 2 ;;
    --target) TARGET="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [ ! -f "${SRC}" ]; then
  echo "FATAL: 找不到仓库脚本 ${SRC}" >&2
  exit 1
fi

SRC_SHA="$(shasum -a 256 "${SRC}" 2>/dev/null | awk '{print $1}')"
if [ -z "${SRC_SHA}" ]; then
  echo "FATAL: 无法计算仓库脚本指纹" >&2
  exit 1
fi
echo "repo    : ${SRC}"
echo "repo sha: ${SRC_SHA}"

remote_sha() {
  ssh -o BatchMode=yes -o ConnectTimeout=15 "${SSH_HOST}" \
    "if [ -f '${TARGET}' ]; then sha256sum '${TARGET}' | awk '{print \$1}'; else echo MISSING; fi" 2>/dev/null
}

OLD_SHA="$(remote_sha)"
echo "remote  : ${SSH_HOST}:${TARGET}"
echo "remote sha: ${OLD_SHA:-<unreachable>}"

if [ "${OLD_SHA}" = "${SRC_SHA}" ]; then
  echo "OK: 采集宿主上的副本与仓库逐字节一致（无需安装）"
  exit 0
fi

if [ "${DRY_RUN}" = "1" ]; then
  echo "DRY-RUN: 将把 ${SRC} 安装到 ${SSH_HOST}:${TARGET}（sha ${SRC_SHA}）"
  exit 0
fi

# 备份旧副本（**远端**留一份 .prev + **本地**留一份，便于回滚；不含任何密钥）
# ★ 2026-11-17 修正：原先的备份目标是 "${TARGET}.prev"（远端绝对路径），
#   而 scp 的第二个参数是**本地**路径 ⇒ 它写的是本机的 /root/... ⇒ 必然失败
#   （本机无权写 /root）⇒ 脚本吞掉了错误（`|| echo 跳过备份`）⇒
#   **看起来备份了，其实没有**。⇒ 现在明确分两处：
#     ① 远端备份：ssh cp（同机回滚用）
#     ② 本地备份：本地临时目录（跨机回滚用）
#   且**备份失败即报错退出**（不再吞错误）——备份不是可选步骤。
echo "==> 备份旧副本（远端 ${TARGET}.prev）"
ssh -o BatchMode=yes -o ConnectTimeout=15 "${SSH_HOST}" \
  "if [ -f '${TARGET}' ]; then cp -p '${TARGET}' '${TARGET}.prev'; else exit 0; fi" || {
  echo "FATAL: 远端备份失败（中止：没有备份就不替换运行副本）" >&2
  exit 1
}
LOCAL_BACKUP="$(mktemp -t market-price-sync.prev.XXXXXX)"
if ssh -o BatchMode=yes -o ConnectTimeout=15 "${SSH_HOST}" \
     "if [ -f '${TARGET}' ]; then cat '${TARGET}'; fi" > "${LOCAL_BACKUP}" 2>/dev/null; then
  echo "    本地备份：${LOCAL_BACKUP}"
else
  echo "    （本地备份失败——远端副本已备份，继续）"
fi

echo "==> 安装新副本"
scp -q "${SRC}" "${SSH_HOST}:${TARGET}" || {
  echo "FATAL: 安装失败" >&2
  exit 1
}
ssh -o BatchMode=yes -o ConnectTimeout=15 "${SSH_HOST}" "chmod 0755 '${TARGET}'" || {
  echo "FATAL: 设置执行位失败" >&2
  exit 1
}

NEW_SHA="$(remote_sha)"
if [ "${NEW_SHA}" != "${SRC_SHA}" ]; then
  echo "FATAL: 安装后指纹不一致（remote=${NEW_SHA} repo=${SRC_SHA}）⇒ 采集宿主上的副本仍是错的" >&2
  echo "       回滚：scp ${SSH_HOST}:${TARGET}.prev ${SSH_HOST}:${TARGET}" >&2
  exit 1
fi

echo "OK: 已安装并对拍一致（sha ${NEW_SHA}）"
echo "提示：台账 market_price_reference_sync.detail.script_sha256 会写入本次运行的脚本指纹，"
echo "      ⇒ 运维可从台账直接看出「跑的是哪个版本」，不必翻文件。"
exit 0

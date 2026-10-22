#!/bin/bash
# =====================================================================
# §143.82 版本谱系取证：★ 把「哪个实例跑着哪版代码」从【推断】变成【机械判定】
#
# ★★★★ 为什么需要它（★ 本轮的直接触发）：
#
#	§143.77~§143.81 连续五轮都写着一条诚实边界：
#	「生产仍跑 §143.56 之前代码 ⇒ ⇒ 需要授权部署 154/245」。
#	★ ★★ 而它一直是【推断出来的】（★ 靠「154 上跑的是 canary@8782」+「进程启动时间」
#	★ ⇒ ⇒ 「推测它不含 2026-10-21 才合入的 §143.56」）。
#	★ ★★ ★ 本轮实测：★★ 两台的 release 目录名里直接带 commit sha
#	★ ⇒ ⇒ ⇒ 「跑的是哪版」不用猜、不用看日志、★ 更不用看启动时间。
#
# ★ ★★ 关键机制（★ 读法）：
#
#	部署目录形如  /opt/llm-gateway-go/releases/<构建号>-<commit-short-sha>/gateway
#	⇒ ⇒ 目录名里的 sha 就是【这台机器当前跑的那版代码】
#	⇒ ⇒ 判定「包不包含某个修复」= `git merge-base --is-ancestor <fix> <sha>`
#	⇒ ⇒ ★ 这是一条可机械复跑的断言，★ 不是日志里的推测。
#
# ★ ★★ 为什么【不许】靠进程启动时间推断（★ 门判据会钉住这条）：
#
#	启动时间只能给出一个【下限】⇒ ⇒ 它【无法证明】「那个 commit 已经在里面」
#	⇒ ⇒ ⇒ 正确做法是【直接读 sha】⇒ ⇒ 再用 git 判祖先关系。
#
# ★ ★★ 为什么【不许】写死服务名（★ 门判据会钉住这条）：
#
#	实测两台的服务名【不同】：
#	  154 ⇒ `llm-gateway-go-canary@8782.service`
#	  245 ⇒ `llmgo-245-canary@8782.service`
#	⇒ ⇒ 所以本脚本是【枚举候选服务】而不是【写死一个】。
#
# ★★ 顺带登记（★ §143.83-B 实测）：★ 两台的【日志通道不同】⇒ ⇒ 取证脚本不能假定相同
#   ★ 154 = llm-gateway-go-canary@8782.service ⇒ ★ 日志在 journald（journalctl -u …）
#   ★ 245 = llmgo-245-canary@8782.service      ⇒ ★ 日志在 jsonl 文件
#     （/var/log/llm-gateway-go/canary-8782.jsonl）
#   ★★ 且 245 的 jsonl 里出现过【连续 22 小时完全没有日志】的时段
#     （2026-10-22T02~T22）⇒ ⇒ ★★ 判「服务在不在跑」时【必须看同一文件的其它消息类型】
#     ⇒ ⇒ 只见某一类信号消失，会把「服务没跑」误读成「那类事件没发生」
#
# 用法（★ 只读；★ 不改任何远端状态）：
#   bash scripts/audit/verify_release_version_lineage.sh                 # 默认查 §143.56
#   bash scripts/audit/verify_release_version_lineage.sh 0e31ca0df       # 查任意 commit
#
# 输出（★ 每台一段）：
#   HOST / SERVICE / PID / START / EXE / RELEASE_SHA / CONTAINS_TARGET
#   以及该机 releases 目录的历史谱系（★ 取构建号与 sha）。
# =====================================================================
set -uo pipefail

TARGET="${1:-0e31ca0df}"
HOSTS="${HOSTS:-154 245}"
RELEASES_DIR="${RELEASES_DIR:-/opt/llm-gateway-go/releases}"
CURRENT_LINK="${CURRENT_LINK:-/opt/llm-gateway-go/current}"

echo "===================================================================="
echo "§143.82 版本谱系取证  ★ 目标 commit = ${TARGET}"
echo "===================================================================="

rc_all=0

for host in ${HOSTS}; do
  echo ""
  echo "------------------------------------------------------------"
  echo "HOST: ${host}"
  echo "------------------------------------------------------------"
  # ★ 远端只读：★ 枚举候选服务 ⇒ 取 MainPID ⇒ 读 /proc/<pid>/exe 的真实路径
  raw=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${host}" '
    # ★★★★★ 读法一（★ 主读法）：★ 【current 软链接】= 当前槽
    #   ★★ 【不依赖二进制名】⇒ ⇒ 实测两台的二进制名【不同】：
    #     ★ 154 = `llm-gateway-go` ｜ ★ 245 = `gateway`
    #   ★★ ⇒ ⇒ 写死二进制名 ⇒ ⇒ 一台会被读成「没在跑」⇒ ⇒ ⇒ 那是【量具坏了】不是事实
    CUR=$(readlink -f '"${CURRENT_LINK}"' 2>/dev/null)
    if [ -n "${CUR}" ]; then
      echo "CURRENT=${CUR}"
      S=$(systemctl status "${CUR}" --no-pager 2>/dev/null \
            | head -1 | sed -n "s/.*● \(.*\.service\).*/\1/p")
      echo "CURRENT_SERVICE=${S}"
    fi

    # ★ 读法二（★ 辅助）：★ 扫 /proc 找【exe 指向 releases 下的进程】（★ 不限二进制名）
    for PID in $(ls /proc 2>/dev/null | grep -E "^[0-9]+$"); do
      EXE=$(readlink -f /proc/"${PID}"/exe 2>/dev/null) || continue
      case "${EXE}" in
        '"${RELEASES_DIR}"'/*) ;;
        *) continue ;;
      esac
      # ★ 服务名：★ systemd 支持按 PID 反查（★ 不写死名字 ⇒ 两台服务名不同也都能读）
      S=$(systemctl status "${PID}" --no-pager 2>/dev/null \
            | head -1 | sed -n "s/.*● \(.*\.service\).*/\1/p")
      [ -z "${S}" ] && S="pid-${PID}"
      START=$(ps -o lstart= -p "${PID}" 2>/dev/null | sed "s/^ *//")
      echo "SERVICE=${S} PID=${PID} START=${START} EXE=${EXE}"
    done
    echo "--- RELEASES ---"
    ls -1t '"${RELEASES_DIR}"' 2>/dev/null | head -8 | while read -r d; do
      echo "RELEASE=${d}"
    done
  ' 2>&1)
  echo "${raw}"

  # ★ 本地判定：★ release 目录名里的 sha ⇒ git 祖先关系
  # ★ sha 优先取【current 槽】⇒ 其次取 releases 列表第一个
  shas=$(echo "${raw}" | awk -F= '/^CURRENT=/ {print $2}' \
          | sed -n 's#.*/releases/[0-9]*-\([0-9a-f]\{7,40\}\)/.*#\1#p' | head -1)
  if [ -z "${shas}" ]; then
    shas=$(echo "${raw}" | awk -F= '/^RELEASE=/ {print $2}' \
            | sed -n 's/^[0-9]*-\([0-9a-f]\{7,40\}\)$/\1/p' | head -1)
  fi
  if [ -z "${shas}" ]; then
    echo "CONTAINS_TARGET=UNKNOWN (★ 没读到 release sha)"
    rc_all=1
    continue
  fi
  echo "RELEASE_SHA=${shas}"
  if git rev-parse --verify --quiet "${shas}^{commit}" >/dev/null 2>&1; then
    if git merge-base --is-ancestor "${TARGET}" "${shas}" 2>/dev/null; then
      echo "CONTAINS_TARGET=YES (★ ${shas} 已包含 ${TARGET})"
    else
      echo "CONTAINS_TARGET=NO  (★ ${shas} 【不】包含 ${TARGET})"
    fi
  else
    echo "CONTAINS_TARGET=UNKNOWN (★ 本地无 ${shas} 这个对象 ⇒ 需先 fetch)"
  fi
done

echo ""
echo "===================================================================="
echo "★ 解读要点："
echo "  ★ 判据是【release 目录名里的 sha】⇒ ⇒ 不是启动时间、★ 也不是日志推断"
echo "  ★ CONTAINS_TARGET=YES ⇒ ⇒ 该实例【已经】包含那个修复 ⇒ ⇒ 不需要再部署"
echo "===================================================================="
exit "${rc_all}"

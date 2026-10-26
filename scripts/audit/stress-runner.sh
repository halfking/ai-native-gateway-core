#!/usr/bin/env bash
# ==============================================================================
# scripts/audit/stress-runner.sh — ★ 带【生命周期管理】的压测运行器
#
# WHAT
# ----
# ★ 为什么要有这个脚本（★ R153 轮的实测教训）：
#
#   ★★ 2026-10-27 R153 轮：★ 本机出现过 **4 个 `bash stress-gate.sh` 孤儿进程**
#   ★★ ⇒ ★ ppid = **1**（★ 父进程早退了）⇒ ★ **跑满 35 小时 51 分**
#   ★★ ⇒ ★★ 它们把本机负载拉高 ⇒ ★★ 让**连续多轮**的本地巡检读数**集体失真**
#   ★★ ⇒ ★★ 端点耗时塌缩 **4 ~ 100 倍** ⇒ ★★ 那几轮的「慢端点」结论**全部不可信**
#   ★★ ⇒ ★★ 而那几轮**没有任何字段**能让人看出「读数不可信」
#   ★★ ⇒ ★★ ⇒ 本仓登记的教训：**「量具自证」不只是量具自己的事，★ 还包括【环境的自证】**。
#
#   ★ ★★ 本脚本提供的就是那个「环境的自证」：
#   ★   ① **硬超时**：★ 到点必杀（★ 默认 10 分钟，★ 远短于 35 小时）
#   ★   ② **进程组清理**：★ 杀整个进程组（★ 子进程也一起死）
#   ★   ③ **启动前检查**：★ 已经有同类孤儿 ⇒ **拒绝启动**（★ 不制造第二个）
#   ★   ④ **结束后自检**：★ 确认没有残留 ⇒ **有残留就报错退出**
#
# ★★ 为什么「杀整个进程组」是必须的（★ R153 轮的实测）：
#   ★ 「杀脚本 ≠ 杀子进程」⇒ ★★ 只 kill 父进程 ⇒ ★★ 子进程继续跑 ⇒ ★★ 孤儿照旧。
#   ★ ⇒ ★ 必须让子进程**自成一个进程组**（★ `setsid`），★ 然后 kill **整个组**。
#
# 用法
# ----
#   bash scripts/audit/stress-runner.sh -- <要跑的命令...>
#   bash scripts/audit/stress-runner.sh --timeout 300 -- <命令...>
#   bash scripts/audit/stress-runner.sh --check-orphans        # ★ 只做孤儿自检
#
# 环境变量
# -----------
#   STRESS_TIMEOUT_SEC   硬超时秒数（★ 默认 600）
#   STRESS_ORPHAN_RE     孤儿判定用的正则（★ 默认匹配本仓的压测脚本名）
#
# 退出码
# ---------
#   0   跑完且无残留
#   64  用法错误
#   69  启动前就发现同类孤儿 ⇒ **拒绝启动**（★ 不制造第二个）
#   70  超时（★ 已清理）
#   71  跑完但**有残留**（★ 孤儿清理失败 ⇒ ★ 这是最该报警的一种）
#   72  被调用的命令自身失败（★ 透传它的退出码）
# ==============================================================================
set -uo pipefail

STRESS_TIMEOUT_SEC="${STRESS_TIMEOUT_SEC:-600}"
# ★ 孤儿判定：★ 匹配「压测」形态的进程名。
# ★ ★ 为什么默认这么宽：★ 压测脚本名会变（★ `stress-gate.sh` / `stress-test` / `loadtest-stress.py`…）
# ★ ★ 而孤儿问题的本质是「**有人在跑压测，但没人管它**」⇒ ★ 按形态判比按名字判更准。
STRESS_ORPHAN_RE="${STRESS_ORPHAN_RE:-stress-gate\.sh|stress-test|loadtest-stress|compression_stress}"

die() { printf 'FATAL: %s\n' "$*" >&2; }

# ─────────────────────────── 孤儿探测 ───────────────────────────
#
# ★ 只看 **ppid == 1** 的进程（★ 父进程已退出的真孤儿）⇒ ★ 不误伤正在被管的进程。
# ★ ★ 为什么还要看命令行里有没有压测形态：★ 别人的机器上同名脚本不该被我们杀。
find_orphans() {
  local re="${STRESS_ORPHAN_RE}"
  ps -eo pid=,ppid=,command= 2>/dev/null | awk -v re="${re}" '
    $2 == 1 {
      cmd = $0
      sub(/^[ ]*[0-9]+[ ]+1[ ]+/, "", cmd)
      if (cmd ~ re) { print $1 "\t" cmd }
    }'
}

report_orphans() {
  local list
  list="$(find_orphans)"
  if [[ -z "${list}" ]]; then
    return 1   # ★ 没有孤儿
  fi
  printf '★★ 发现压测孤儿进程（★ ppid=1 ⇒ 父进程已退出）：\n'
  while IFS= read -r line; do
    printf '   pid=%s  %s\n' "${line%%$'\t'*}" "${line#*$'\t'}"
  done <<< "${list}"
  printf '★★ ⇒ ★ 这些进程**没人管** ⇒ ★ 它们会把本机负载拉高 ⇒ ★★ 巡检读数会集体失真\n'
  printf '★★ ⇒ ★ 处理：kill %s（★ 或先确认它们不是你要的）\n' "$(printf '%s' "${list}" | cut -f1 | tr '\n' ' ')"
  return 0
}

# ─────────────────────────── 参数解析 ───────────────────────────
TIMEOUT=""
CMD=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --timeout) TIMEOUT="${2:?--timeout 需要参数}"; shift 2 ;;
    --check-orphans)
      if report_orphans; then exit 69; else exit 0; fi
      ;;
    --) shift; CMD=("$@"); break ;;
    -h|--help)
      sed -n '2,48p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) die "未知参数：$1"; exit 64 ;;
  esac
done

if [[ "${#CMD[@]}" -eq 0 ]]; then
  die "没有给出要跑的命令（★ 用法：bash $0 -- <命令...>）"
  exit 64
fi
if [[ -n "${TIMEOUT}" ]]; then
  STRESS_TIMEOUT_SEC="${TIMEOUT}"
fi
case "${STRESS_TIMEOUT_SEC}" in
  ''|*[!0-9]*) die "超时秒数非法：${STRESS_TIMEOUT_SEC}"; exit 64 ;;
esac

# ★★★ 启动前检查：★ 已经有同类孤儿 ⇒ **拒绝启动**（★ 不制造第二个）。
if report_orphans; then
  printf '★ 拒绝启动：★ 先处理上面的孤儿（或用 --check-orphans 单独看）⇒ ★ 不制造第二个\n' >&2
  exit 69
fi

printf '… 启动：%s\n' "${CMD[*]}"
printf '… 硬超时：%s 秒（★ 到点必杀整个进程组）\n' "${STRESS_TIMEOUT_SEC}"

# ─────────────────────────── 三层进程模型 ───────────────────────────
#
# ★★★ 关键设计（★ R157 轮实测踩到）：★ **真实退出码必须被保住**。
#
# ★ 踩到的坑（★ 第一版）：★ 让 python 直接 `execvp(cmd)` ⇒ ★ python fork 完就退
# ★★ ⇒ ★ shell 的子进程是 **python** ⇒ ★ `wait` 拿到的是 **python 的退出码**
# ★★ ⇒ ★★ 实测：命令 `exit 0` 被报成 **127** ⇒ ★★ **真实退出码丢失**
# ★★ ⇒ ★★ 「命令成功」与「命令失败」无法区分 ⇒ ★★ 判据直接失效。
#
# ★★★ 正解（三层）：
# ★ ① **孙进程** = `sh -c '<cmd>; printf %s $? > rcfile'`
# ★★   ⇒ ★ sh 是孙进程的**直接父进程** ⇒ ★★ sh **拿得到**真实退出码 ⇒ ★★ 落盘到 rcfile
# ★ ② **中间层** = python（★ 只做 `os.setsid()` ⇒ ★ 孙进程**自成一个进程组**
# ★★   ⇒ ★ kill 整个组能连孙进程的子进程一起杀 ⇒ ★★ 「杀脚本 ≠ 杀子进程」的正解）
# ★ ③ **shell** = 本脚本（★ 轮询 rcfile + 轮询组是否还活着）
#
# ★ ★ 为什么不让 python 当「代理」等孙进程：★ setsid 之后孙进程**不是** python 的子进程
# ★ ★ ⇒ ★ python **wait 不到**它 ⇒ ★★ 退出码无从取得 ⇒ ★★ 只能靠 rcfile ⇒ ★★
# ★ ★ ⇒ ★★ 于是 python **只负责 setsid 并交出孙进程 pid**，★ 退出码走 rcfile。
if ! command -v python3 >/dev/null 2>&1; then
  die "没有 python3 ⇒ 无法保证进程组隔离 ⇒ 拒绝跑（★ 否则可能留孤儿）"
  exit 64
fi

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/stress-runner.XXXXXX")"
cleanup_dir() { rm -rf "${WORKDIR}"; }
trap cleanup_dir EXIT

rm -f "${WORKDIR}/out" "${WORKDIR}/err" "${WORKDIR}/rc"
export SR_OUT="${WORKDIR}/out" SR_ERR="${WORKDIR}/err" SR_CMDFILE="${WORKDIR}/cmd" SR_RCFILE="${WORKDIR}/rc"
printf '%s\n' "${CMD[@]}" >"${WORKDIR}/cmd"
PID="$(python3 - <<'PY'
import os, sys
cmdfile = os.environ["SR_CMDFILE"]
with open(cmdfile, "r", encoding="utf-8") as f:
    argv = [ln.rstrip("\n") for ln in f if ln.strip() != ""]
rcfile = os.environ["SR_RCFILE"]
pid = os.fork()
if pid == 0:
    try:
        os.setsid()
    except OSError:
        pass
    fd_out = os.open(os.environ["SR_OUT"], os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    fd_err = os.open(os.environ["SR_ERR"], os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.dup2(fd_out, 1)
    os.dup2(fd_err, 2)
    # ★ sh -c 包一层 ⇒ ★ sh 能拿到真实退出码并落盘
    quoted = " ".join("'" + a.replace("'", "'\\''") + "'" for a in argv)
    wrapper = quoted + "; printf '%s' $? > " + rcfile
    os.execvp("sh", ["sh", "-c", wrapper])
    os._exit(127)
print(pid)
PY
)"
if [[ -z "${PID}" ]]; then
  die "启动失败（★ python3 fork 失败）"
  exit 64
fi
PGID="${PID}"

# ─────────────────────────── 等它结束（带硬超时） ───────────────────────────
START_TS="$(date +%s)"
RC=0
TIMED_OUT=0
while true; do
  # ★ 优先看 rcfile：★ 它一出现就说明**命令已经跑完**（★ sh 写了退出码）。
  if [[ -f "${WORKDIR}/rc" ]]; then
    RC="$(cat "${WORKDIR}/rc" 2>/dev/null || echo 0)"
    case "${RC}" in
      ''|*[!0-9]*) RC=0 ;;
    esac
    break
  fi
  # ★ 组里还有活进程吗：★ 用 kill -0 探 **组长**（★ ★ 用 -<pgid> 探整组更准）。
  if ! kill -0 "-${PGID}" 2>/dev/null && ! kill -0 "${PID}" 2>/dev/null; then
    # ★ 组已经消失但没有 rcfile ⇒ ★ 说明 sh 没能写 rcfile（★ 例如被 KILL 掉了）
    # ★ ⇒ ★ 退出码不可知 ⇒ ★ 记成 71（★ 「有残留/状态不明」那一档）。
    RC=71
    break
  fi
  NOW="$(date +%s)"
  if [[ $(( NOW - START_TS )) -ge "${STRESS_TIMEOUT_SEC}" ]]; then
    printf '★★ 超时（%s 秒）⇒ ★ 杀整个进程组 %s\n' "${STRESS_TIMEOUT_SEC}" "${PGID}" >&2
    kill -TERM "-${PGID}" 2>/dev/null || kill -TERM "${PID}" 2>/dev/null || true
    sleep 2
    kill -KILL "-${PGID}" 2>/dev/null || kill -KILL "${PID}" 2>/dev/null || true
    TIMED_OUT=1
    break
  fi
  sleep 1
done

printf -- '---- 被跑命令的输出 ----\n'
[[ -f "${WORKDIR}/out" ]] && cat "${WORKDIR}/out"
[[ -f "${WORKDIR}/err" ]] && cat "${WORKDIR}/err" >&2
printf -- '------------------------\n'
# ★ ★ 总是打印**被跑命令的真实退出码**（★ 即便它是 0）⇒ ★ 门与人都能核对
# ★ ★ ⇒ ★ 「退出码被保住」这件事**可观察**，而不是「只能靠猜」。
printf '★ 被跑命令的真实退出码：%s\n' "${RC}"

# ─────────────────────────── 结束后自检：★ 有残留 ⇒ 报警 ───────────────────────────
if report_orphans; then
  printf '★★★ 跑完后**仍有**压测孤儿 ⇒ ★ 清理失败 ⇒ ★ 这是最该报警的一种\n' >&2
  exit 71
fi

if [[ "${TIMED_OUT}" -eq 1 ]]; then
  exit 70
fi
if [[ "${RC}" -eq 71 ]]; then
  printf '★ 进程组已消失但拿不到退出码 ⇒ ★ 状态不明（★ 疑似被外部 KILL）\n' >&2
  exit 71
fi
if [[ "${RC}" -ne 0 ]]; then
  printf '★ 被跑命令失败（退出码 %s）\n' "${RC}" >&2
  exit 72
fi
exit 0

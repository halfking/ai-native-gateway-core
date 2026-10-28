#!/usr/bin/env bash
# Parity gate: the repo's switch-lock liveness oracle vs the shared deploy
# library's liveness oracle.
#
# 2026-10-28（r1039 后续轮 · 第二轮批判性审计）。
#
# ## 为什么要有这道门（★ 本轮最值钱的一处发现的来历）
#
# ★ 共享部署库 lock.sh 的 `lock_pid_alive` 早在 **2026-09-18** 就把口径从
#   `kill -0` 换成了 `ps -p`，它的注释原文写着：
#
#     "The previous checks used `kill -0 "$pid"`, which conflates ESRCH
#      (process gone) with EPERM (process ALIVE but owned by another user)
#      — both exit nonzero. A deploy launched by a different user (root cron
#      vs operator shell) therefore looked DEAD to force recovery, which then
#      deleted a live holder's lock and let two deploys interleave.
#      `ps -p` reports existence regardless of ownership on macOS and Linux,
#      so EPERM can no longer masquerade as death."
#
# ⇒ ★★ **本仓 2026-10-27/28 新写的 `dl_pid_alive` 把那个已经修掉的 bug 又抄了回来**
#   （而且注释还写着「EPERM 也算活着」⇒ 与实现相反 ⇒ 声名与实现不符）。
#   ⇒ 这不是「巧合写错」，而是**同一套机制存在两份实现、其中一份停在了旧版本**
#     ⇒ **两侧语义会漂移**，而仓内没有任何一道门比对过两侧。
#
# ## 这道门钉什么
#
#   P1 **存活判据逐 pid 对拍**：同一个 pid ⇒ 本仓 `dl_pid_alive` 与共享部署库
#      `lock_pid_alive` 必须给出**同一个**答案，且都等于 **OS 的事实**
#      （`ps -p` 看得到 ⇒ 存在）。
#   P2 **元数据字段集对拍**：本仓的注释声称「与共享部署库 lock.sh 同字段集」
#      ⇒ 那是一条**声名** ⇒ 这里把它变成可证伪的判据。
#
# ## 刻意**不**对拍的部分（★ 政策差异，不是语义漂移）
#
#   ★ 本仓的 acquire 会对残锁**自动接管**（否则机器被永久卡死）；
#     共享部署库的 acquire 一律快速失败（由运维显式 force-unlock）。
#   ★ 共享部署库 unlock-local.sh 的 --force 档会**杀掉**活着的持有者；
#     本仓的 --force 档只清锁、不杀进程（杀掉别的用户的部署是更重的动作，
#     属需要 owner 裁决的语义变更 ⇒ 本轮只登记）。
#   ⇒ 这两条是**有意**不同，判据只钉「持有者是否还活着」这一个共同前提。
#
# ## 缺 SSOT 时怎么办
#
#   共享部署库在仓外（~/workspace/ai-native-tools/deploy-lib）。CI runner 上
#   可能没有 ⇒ 此时**明确留痕 SKIP**（并说明「本次没比对」），**不**输出
#   ALL PASS ⇒ 避免「假绿」（★ 仓内纪律：门绿了不代表事实成立）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

TMP=$(mktemp -d -t kx-lock-parity.XXXXXX)
trap 'rm -rf "$TMP"' EXIT

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }

# ── 解析共享部署库 SSOT（与 deploy-local.sh 的候选顺序一致）──────────────
ssot=''
for cand in "${AIAN_DEPLOY_LIB:-}" "$ROOT/scripts/deploy-lib" "$HOME/workspace/ai-native-workspace/deploy-lib" "$HOME/workspace/ai-native-tools/deploy-lib"; do
  if [[ -n "$cand" && -f "$cand/lock.sh" ]]; then
    ssot="$(cd "$cand" && pwd -P)"
    break
  fi
done

if [[ -z "$ssot" ]]; then
  printf 'SKIP: shared deploy lib (lock.sh) not found — the liveness parity was NOT compared this run\n'
  printf '  looked at: AIAN_DEPLOY_LIB, %s/scripts/deploy-lib, ~/workspace/ai-native-workspace/deploy-lib, ~/workspace/ai-native-tools/deploy-lib\n' "$ROOT"
  printf 'SKIP deploy lock parity (no SSOT)\n'
  exit 0
fi
printf 'SSOT: %s\n' "$ssot"

# ★ LOCK_TMP_ROOT 必须**在** source 之前设好 ⇒ SSOT 的 `: "${LOCK_TMP_ROOT:=/tmp}"`
#   不会把锁路径指到真实的 /tmp。
export LOCK_TMP_ROOT="$TMP/locks"
mkdir -p "$LOCK_TMP_ROOT"

# shellcheck source=/dev/null
source "$ssot/lock.sh"
# shellcheck source=/dev/null
source "$ROOT/scripts/deploy-local-lib.sh"

# oracle：OS 的事实（跨用户安全 —— 与 SSOT 的 liveness 口径同源，但这里
# 独立写一遍，避免「拿被测对象当标准」）。
oracle_alive() {
  local pid="$1"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  ps -p "$pid" -o pid= >/dev/null 2>&1
}

verdict_of() {
  local fn="$1" pid="$2"
  if "$fn" "$pid" 2>/dev/null; then printf ALIVE; else printf DEAD; fi
}

# ── P1：逐 pid 对拍 ───────────────────────────────────────────────────────
#
# ★ pid 矩阵覆盖四种「形态」：
#   · 同用户的活进程（$$）
#   · **别的用户**的活进程（pid 1 = launchd / systemd，root 的）
#     ⇒ 这一格才是 EPERM 那一支的真正考题
#   · 死 pid（999999）
#   · 畸形 pid（0 / abc / 空）—— ★ pid 0 不是进程（`kill -0 0` 探的是进程组、
#     恒成功）⇒ 两侧都必须判「死」
failed=0
for pid in "$$" 1 999999 0 abc ""; do
  if oracle_alive "$pid"; then want=ALIVE; else want=DEAD; fi
  got_repo="$(verdict_of dl_pid_alive "$pid")"
  got_ssot="$(verdict_of lock_pid_alive "$pid")"
  label="pid=${pid:-<empty>}"
  if [[ "$got_repo" != "$want" ]]; then
    printf 'FAIL %s: repo oracle says %s, OS says %s\n' "$label" "$got_repo" "$want" >&2
    failed=1
    continue
  fi
  if [[ "$got_ssot" != "$want" ]]; then
    printf 'FAIL %s: shared SSOT oracle says %s, OS says %s\n' "$label" "$got_ssot" "$want" >&2
    failed=1
    continue
  fi
  if [[ "$got_repo" != "$got_ssot" ]]; then
    printf 'FAIL %s: repo says %s but shared SSOT says %s (semantic drift)\n' "$label" "$got_repo" "$got_ssot" >&2
    failed=1
    continue
  fi
  printf 'PASS %s: repo=%s ssot=%s os=%s\n' "$label" "$got_repo" "$got_ssot" "$want"
done
[[ "$failed" -eq 0 ]] || fail 'liveness parity between the repo and the shared SSOT is broken'

# ── P2：元数据字段集对拍（本仓注释里的「同字段集」是一条声名）─────────────
repo_meta="$TMP/repo-metadata"
ssot_meta="$TMP/ssot-metadata"
dl_switch_lock_write_metadata > "$repo_meta"
lock_write_metadata 154 > "$ssot_meta"
repo_fields="$(awk -F= '/^[a-z_]+=/ {print $1}' "$repo_meta" | sort | tr '\n' ',')"
ssot_fields="$(awk -F= '/^[a-z_]+=/ {print $1}' "$ssot_meta" | sort | tr '\n' ',')"
if [[ "$repo_fields" != "$ssot_fields" ]]; then
  printf 'FAIL metadata field set drifted\n  repo: %s\n  ssot: %s\n' "$repo_fields" "$ssot_fields" >&2
  exit 1
fi
printf 'PASS metadata field set matches the shared SSOT: %s\n' "$repo_fields"

printf 'ALL PASS deploy lock parity\n'

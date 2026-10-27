#!/usr/bin/env bash
# Contract test for the cross-checkout switch mutex (machine-level switch lock).
#
# 2026-10-27（r1039 后续轮；审计档
# docs/audit/2026-10-20-r1039-脱敏阻断与设置界面审计轮.md §8 第 18 条）。
#
# ## 被钉住的不变量
#
#   I1 切流锁是**机器级**的：路径不含任何 checkout 路径
#      ⇒ 否则两份 checkout 各拿各的锁，「互斥」名存实亡。
#   I2 争用时**快速失败**（rc 75 / EX_TEMPFAIL）并打印持有者元数据
#      ⇒ 第二个部署不去抢端口，而是明确报「有人在切流」。
#   I3 释放后可再次获取 ⇒ 锁不会变成「一次性」的死结。
#   I4 覆盖窗口：**候选实例起跑之前**就持锁
#      ⇒ 两个 checkout 不会同时在同一个候选端口上起实例。
#   I5 释放点在**切流完成之后**（dl_atomic_switch 之后）。
#   I6 失败路径（trap）也释放 ⇒ 一次 Ctrl-C 不会把锁留成永久占用。
#
# ## 判据纪律
#
#   - 行为判据（I1~I3）用**真函数**跑：LOCK_TMP_ROOT 指向临时目录 ⇒ 不碰机器上
#     真实的锁，也不碰任何正在跑的部署。
#   - 顺序判据（I4~I6）读 deploy-local.sh 的**源码位置**：断言的是「谁在谁前面」，
#     不是「某个字符串出现过」（出现过 ≠ 承重）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/deploy-local-lib.sh" || { printf 'FATAL: cannot source deploy-local-lib.sh\n' >&2; exit 1; }

TMP=$(mktemp -d -t kx-switch-lock.XXXXXX)
trap 'rm -rf "$TMP"' EXIT
export LOCK_TMP_ROOT="$TMP/locks"
mkdir -p "$LOCK_TMP_ROOT"

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS %s\n' "$*"; }

# ── I1：机器级（路径不含 checkout 路径）────────────────────────────────
lock_path="$(dl_switch_lock_path)"
[[ "$lock_path" == "$LOCK_TMP_ROOT/kx-llm-gateway-switch.lock" ]] \
  || fail "switch lock path must live under LOCK_TMP_ROOT (machine-level), got: $lock_path"
case "$lock_path" in
  *"$ROOT"*) fail "switch lock path must not contain the checkout path (fake mutex): $lock_path" ;;
esac
# 换个工作目录再算一次：路径必须一模一样（否则它就是 checkout 相关的）。
path_from_elsewhere="$(cd /tmp && bash -c 'source "'"$ROOT"'/scripts/deploy-local-lib.sh"; dl_switch_lock_path')"
[[ "$path_from_elsewhere" == "$lock_path" ]] \
  || fail "switch lock path must not depend on cwd (got $path_from_elsewhere vs $lock_path)"
pass 'switch lock path is machine-level (checkout-independent)'

# ── I2：争用时快速失败（rc 75）+ 打印持有者元数据 ────────────────────────
dl_acquire_switch_lock || fail 'first acquire must succeed'
rc=0
contention_msg="$(dl_acquire_switch_lock 2>&1 >/dev/null)" || rc=$?
[[ "$rc" -eq 75 ]] || fail "second acquire must fail with 75 (EX_TEMPFAIL), got rc=$rc"
grep -q 'another checkout is switching the active port' <<<"$contention_msg" \
  || fail "contention message must tell the operator who is switching (got: $contention_msg)"
if [[ -f "$lock_path/metadata" ]]; then
  grep -q '^pid=' "$lock_path/metadata" || fail 'holder metadata must record the pid'
  grep -q '^started_at=' "$lock_path/metadata" || fail 'holder metadata must record started_at'
  pass 'contention fails fast (75) and keeps holder metadata'
else
  grep -q '^pid=' "$lock_path" || fail 'holder metadata must record the pid'
  pass 'contention fails fast (75) and keeps holder metadata'
fi

# ── I3：释放后可再次获取 ────────────────────────────────────────────────
dl_release_switch_lock
rc=0
dl_acquire_switch_lock || rc=$?
[[ "$rc" -eq 0 ]] || fail "acquire after release must succeed, got rc=$rc"
dl_release_switch_lock
pass 'lock is re-acquirable after release (no permanent deadlock)'

# ── I4/I5/I6：源码顺序判据（deploy-local.sh）────────────────────────────
deploy_script="$ROOT/scripts/deploy-local.sh"
line_of() { grep -nF -- "$1" "$deploy_script" | head -1 | cut -d: -f1; }

acquire_line="$(line_of 'dl_acquire_switch_lock || die')"
[[ -n "$acquire_line" ]] || fail 'deploy-local.sh must acquire the switch lock'
candidate_line="$(line_of 'start_instance "$bundle" "$candidate_port"')"
[[ -n "$candidate_line" ]] || fail 'deploy-local.sh must start the candidate instance'
[[ "$acquire_line" -lt "$candidate_line" ]] \
  || fail "switch lock must be acquired BEFORE the candidate instance starts (acquire=$acquire_line candidate=$candidate_line)"

switch_line="$(line_of 'dl_atomic_switch "$RELEASE_VERSION"')"
[[ -n "$switch_line" ]] || fail 'deploy-local.sh must perform the atomic switch'
release_line="$(grep -n '^  release_switch_lock$' "$deploy_script" | tail -1 | cut -d: -f1)"
[[ -n "$release_line" ]] || fail 'deploy-local.sh must release the switch lock'
[[ "$release_line" -gt "$switch_line" ]] \
  || fail "switch lock must be released AFTER the atomic switch (switch=$switch_line release=$release_line)"

trap_body="$(sed -n '/^cleanup_deploy()/,/^}/p' "$deploy_script")"
grep -q 'release_switch_lock' <<<"$trap_body" \
  || fail 'cleanup_deploy (EXIT/INT/TERM trap) must release the switch lock'
pass 'lock window covers candidate start → atomic switch, and the trap releases it'

printf 'ALL PASS deploy switch lock contract\n'

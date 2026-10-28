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

# ── I7：残锁（持有者已死）必须被【接管】，而不是把机器永久卡死 ────────────
#
# ★ 这条是 2026-10-28 批判性审计轮补的：上一轮只实现了「抢锁失败 ⇒ rc 75」，
#   **没有**处理「持有者已死但锁还在」⇒ 机器重启 / SIGKILL 之后，
#   **每一次后续部署都 rc=75 永久失败**（把偶发互踩换成必然卡死，比原缺陷更糟）。
mkdir -p "$lock_path"
printf 'target=local-switch\npid=999999\nstarted_at=2026-10-28T00:00:00Z\n' > "$lock_path/metadata"
rc=0
takeover_msg="$(dl_acquire_switch_lock 2>&1)" || rc=$?
[[ "$rc" -eq 0 ]] || fail "a stale lock (holder pid gone) must be taken over, got rc=$rc ($takeover_msg)"
grep -qi 'stale' <<<"$takeover_msg" \
  || fail "takeover must be announced loudly (got: $takeover_msg)"
new_pid="$(dl_switch_lock_holder_pid 2>/dev/null || true)"
[[ -n "$new_pid" && "$new_pid" != "999999" ]] \
  || fail "takeover must rewrite the holder metadata (pid=$new_pid)"
dl_release_switch_lock
pass 'a stale lock (holder gone) is taken over instead of wedging the deploy path'

# ── I8：活着的持有者仍然必须被拒（不能因为「残锁接管」把互斥拆了）──────────
mkdir -p "$lock_path"
printf 'target=local-switch\npid=%s\nstarted_at=2026-10-28T00:00:00Z\n' "$$" > "$lock_path/metadata"
rc=0
dl_acquire_switch_lock >/dev/null 2>&1 || rc=$?
[[ "$rc" -eq 75 ]] || fail "a live holder must still be refused, got rc=$rc"
holder_now="$(dl_switch_lock_holder_pid 2>/dev/null || true)"
[[ "$holder_now" == "$$" ]] \
  || fail "a refused acquire must not touch the live holder's metadata (pid=$holder_now)"
rm -rf "$lock_path"
pass 'a live holder is still refused and its metadata is left untouched'

# ── I9：显式恢复命令的两种结局 ───────────────────────────────────────────
mkdir -p "$lock_path"
printf 'target=local-switch\npid=999999\n' > "$lock_path/metadata"
rc=0
dl_recover_switch_lock >/dev/null 2>&1 || rc=$?
[[ "$rc" -eq 0 ]] || fail "recovering a lock whose holder is gone must succeed, got rc=$rc"
[[ ! -e "$lock_path" ]] || fail 'recovery must remove the stale lock'

mkdir -p "$lock_path"
printf 'target=local-switch\npid=%s\n' "$$" > "$lock_path/metadata"
rc=0
dl_recover_switch_lock >/dev/null 2>&1 || rc=$?
[[ "$rc" -ne 0 ]] || fail 'recovery must REFUSE while the holder is alive'
[[ -e "$lock_path" ]] || fail "a refused recovery must not remove the live lock"
rm -rf "$lock_path"
pass 'recovery removes a dead-holder lock and refuses a live one'

# ── I10：恢复入口必须是【仓内真命令】，不是文档里的一句话 ──────────────────
#
# ★ 上一轮的审计档写了「运维核对后手工删除」——★ 那是【声名】：仓内**没有任何命令**
#   能做这件事 ⇒ 残锁就把部署路径永久堵死了。
# ⇒ 这里钉三处注册（usage / 参数白名单 / case 分派）+ 一次**真调用**。
grep -q 'recover-switch-lock' "$deploy_script" \
  || fail 'deploy-local.sh must expose a recover-switch-lock action'
usage_line="$(grep -n 'Usage: deploy-local.sh' "$deploy_script" | head -1)"
grep -q 'recover-switch-lock' <<<"$usage_line" \
  || fail 'recover-switch-lock must appear in the usage line'
grep -q 'recover-switch-lock) ACTION=\$1' "$deploy_script" \
  || fail 'recover-switch-lock must be in the argument whitelist'
grep -q 'recover-switch-lock) recover_switch_lock' "$deploy_script" \
  || fail 'recover-switch-lock must be dispatched to an implementation'
recover_fn="$(sed -n '/^recover_switch_lock()/,/^}/p' "$deploy_script")"
grep -q 'dl_recover_switch_lock' <<<"$recover_fn" \
  || fail 'recover_switch_lock must delegate to dl_recover_switch_lock'

# 真调用（需要仓外共享部署库存在；缺了就留痕，不当成通过）。
ssot=''
for cand in "${AIAN_DEPLOY_LIB:-}" "$HOME/workspace/ai-native-workspace/deploy-lib" "$HOME/workspace/ai-native-tools/deploy-lib"; do
  [[ -n "$cand" && -d "$cand" ]] && { ssot="$cand"; break; }
done
if [[ -n "$ssot" ]]; then
  mkdir -p "$lock_path"
  printf 'target=local-switch\npid=999999\n' > "$lock_path/metadata"
  out="$(LOCK_TMP_ROOT="$LOCK_TMP_ROOT" bash "$deploy_script" recover-switch-lock 2>&1)"; rc=$?
  [[ "$rc" -eq 0 ]] || fail "the real recover-switch-lock command must succeed on a stale lock, got rc=$rc ($out)"
  [[ ! -e "$lock_path" ]] || fail 'the real recover-switch-lock command must remove the stale lock'
  pass 'recover-switch-lock is a real, working command (stale lock removed)'
else
  printf 'SKIP real recover-switch-lock invocation: shared deploy lib not found (the registration assertions above still ran)\n'
  pass 'recover-switch-lock is registered in all three places'
fi

printf 'ALL PASS deploy switch lock contract\n'

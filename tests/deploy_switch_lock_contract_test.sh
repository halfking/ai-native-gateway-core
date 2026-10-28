#!/usr/bin/env bash
# Contract test for the cross-checkout switch mutex (machine-level switch lock).
#
# 2026-10-27（r1039 后续轮；审计档
# docs/audit/2026-10-20-r1039-脱敏阻断与设置界面审计轮.md §8 第 18 条）。
# 2026-10-28（第二轮批判性审计）：★ 追加 I11~I17 ——
#   上一轮只钉了「锁在不在」，**没钉「锁到底互斥吗」** ⇒ 本轮把
#   「持有者还活着」这个判据本身逐条审了一遍（审计档
#   docs/audit/2026-10-28-r1039-后续轮-批判性审计-第二轮.md）。
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
#   I7 残锁（持有者已死）被**接管**，而不是把机器永久卡死。
#   I8 活着的持有者仍然被拒（不能因为「残锁接管」把互斥拆了）。
#   I9 恢复命令的两种结局（死的清掉 / 活的拒绝）。
#   I10 恢复入口是**仓内真命令**（三处注册 + 一次真调用）。
#
#   ── 2026-10-28 追加（★ 上一轮的门抓不到的那一类缺陷）────────────────────
#   I11 **flock 形态必须可达且语义正确**
#      ⇒ ★ 本机（macOS /bin/bash 3.2）**没有 flock** ⇒ 上一轮的门**一次都没
#        执行过 flock 分支** ⇒ 那半边实现的缺陷对门完全不可见。
#        ⇒ 这里用**真语义**的假 flock（python fcntl.flock）把它拉进生产路径。
#   I12 **形态混用不许把脚本带崩**：目录形态的锁 + flock 可用
#      ⇒ 旧实现 `exec <> 目录` 会因 EISDIR 失败，而 POSIX 规定 exec 的重定向
#        失败是**致命**的 ⇒ 整个部署脚本带着 "Is a directory" 死掉。
#   I13 **flock 释放不得删锁文件**（unlink-after-unlock 竞态 ⇒ 两个持有者）。
#   I14 **存活判据：EPERM ≠ ESRCH**（活着的别人的锁不许被当成残锁接管）。
#   I15 **恢复命令的两档**：持有者读不出来 ⇒ 默认档拒绝；--force 档才清。
#   I16 **半截元数据不算存活证据** ⇒ 不接管（宁可拒绝，不可误杀活锁）。
#   I17 **「活着持有者 ⇒ 拒绝」必须因为「活着」而拒绝**
#      ⇒ ★ 防止判据因为「读不出来」而**因为错误的原因**变绿。
#   I18 **--force 档的三个断言**（CLI 层）：
#      ① 只允许与 recover-switch-lock 同用（别的动作同用 ⇒ exit 64）；
#      ② 真能清掉「持有者读不出来」的锁；
#      ③ 默认档在同一状态下**仍然拒绝**（⇒ --force 是【升级档】不是【默认档】）。
#
#   ── 2026-10-28 追加（第三轮 · 合并就绪轮）─────────────────────────────────
#   I19 **判据不可用 ⇒ 显式失败，不许猜**
#      ⇒ ★ 判据已**委托**共享部署库 ⇒ 「共享部署库没被 source」从「实现细节」
#        变成「可用性前提」⇒ 若把「不知道」当成「持有者已死」⇒ 接管掉一把
#        活锁 ⇒ ★ **F1 换了个入口复活** ⇒ 必须有专门的判据。
#      ⇒ 钉三件事：① 判据缺席时 `dl_pid_alive` 返回 **2（未知）**而不是 1（死）；
#        ② acquire **rc=75 + 可读原因**，且**不创建、不接管**；
#        ③ 残锁判定把「未知」判成**不是残锁**（fail-closed）。
#      ⇒ ④（第三轮补）恢复命令在判据缺席时也必须**拒绝**清锁。
#   I20 **争用在 CLI 层也必须是 75（EX_TEMPFAIL）**
#      ⇒ ★ 注释与设计都写着「rc 75」，而生产入口是 `|| die …` ⇒ `die` 退 **1**
#        ⇒ ⇒ **「稍后重试」与「硬失败」在监督方眼里不可区分**。
#      ⇒ 钉两件事：① `die75` 的退出码真的是 75（行为级）；② 争用调用点用它。
#
# ## 判据纪律
#
#   - 行为判据（I1~I3）用**真函数**跑：LOCK_TMP_ROOT 指向临时目录 ⇒ 不碰机器上
#     真实的锁，也不碰任何正在跑的部署。
#   - 顺序判据（I4~I6）读 deploy-local.sh 的**源码位置**：断言的是「谁在谁前面」，
#     不是「某个字符串出现过」（出现过 ≠ 承重）。
#   - ★ 判据要能红：每个新不变量都配了对应的**变异臂**（见审计档 §7）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/deploy-local-lib.sh" || { printf 'FATAL: cannot source deploy-local-lib.sh\n' >&2; exit 1; }

TMP=$(mktemp -d -t kx-switch-lock.XXXXXX)
trap 'rm -rf "$TMP"' EXIT
export LOCK_TMP_ROOT="$TMP/locks"
mkdir -p "$LOCK_TMP_ROOT"

# ── 存活判据从哪来（★ 第三轮起：委托共享部署库）────────────────────────────
#
# ★ 本仓的 `dl_pid_alive` 现在**委托**共享部署库的 `lock_pid_alive`
#   ⇒ 判据必须**真的被加载** ⇒ 这里按「有则用真、无则用同语义替身」接入，
#     并**显式打印用的是哪一种** ⇒ 避免「以为在测真实现、其实测的是替身」。
ORACLE_ORIGIN=''
ORACLE_BOOTSTRAP="$TMP/oracle-bootstrap.sh"
ssot=''
for cand in "${AIAN_DEPLOY_LIB:-}" "$ROOT/scripts/deploy-lib" "$HOME/workspace/ai-native-workspace/deploy-lib" "$HOME/workspace/ai-native-tools/deploy-lib"; do
  if [[ -n "$cand" && -f "$cand/lock.sh" ]]; then
    ssot="$(cd "$cand" && pwd -P)"
    break
  fi
done
if [[ -n "$ssot" ]]; then
  # shellcheck source=/dev/null
  source "$ssot/lock.sh"
  ORACLE_ORIGIN="shared SSOT ($ssot/lock.sh)"
  printf 'source %q\n' "$ssot/lock.sh" > "$ORACLE_BOOTSTRAP"
else
  # 测试替身：与共享部署库 `lock_pid_alive` 同语义（ps 口径）的最小实现。
  # ★ 它是**替身**，不是第二份生产实现 —— 生产路径只认共享部署库那一份。
  lock_pid_alive() { ps -p "$1" -o pid= >/dev/null 2>&1; }
  ORACLE_ORIGIN="test double (shared SSOT not found — 本次没比对真实现)"
  cat > "$ORACLE_BOOTSTRAP" <<'STUB'
lock_pid_alive() { ps -p "$1" -o pid= >/dev/null 2>&1; }
STUB
fi
printf 'liveness oracle: %s\n' "$ORACLE_ORIGIN"

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
# ★ 用 `grep -m1` 而不是 `grep -n ... | head -1`：★ 后者让 `head` 提前退出 ⇒
#   上游 `grep` 可能收到 SIGPIPE（141）⇒ ★ 在 `set -euo pipefail` 下把判据
#   变成「随文件内容漂移的随机中止」。★ 语义等价（都取第一处匹配），无竞态。
line_of() { grep -m1 -nF -- "$1" "$deploy_script" | cut -d: -f1; }

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
#
# ★ 2026-10-28 第二轮：元数据夹具改成**真实形态**（写入方永远写全 7 个字段）⇒
#   否则「读不出 pid」与「持有者已死」会混在一起 ⇒ 判据可能因为**错误的原因**变绿。
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
#
# ★ 2026-10-28 第二轮：夹具改成**真实形态**（含 started_at）⇒ 这样「持有者已死」
#   走的是「死了」那条路，而不是「读不出来」那条路（★ 判据不能因为错误的原因变绿；
#   I17 专门钉这件事）。
mkdir -p "$lock_path"
printf 'target=local-switch\npid=999999\nstarted_at=2026-10-28T00:00:00Z\n' > "$lock_path/metadata"
rc=0
dl_recover_switch_lock >/dev/null 2>&1 || rc=$?
[[ "$rc" -eq 0 ]] || fail "recovering a lock whose holder is gone must succeed, got rc=$rc"
[[ ! -e "$lock_path" ]] || fail 'recovery must remove the stale lock'

mkdir -p "$lock_path"
printf 'target=local-switch\npid=%s\nstarted_at=2026-10-28T00:00:00Z\n' "$$" > "$lock_path/metadata"
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
usage_line="$(grep -m1 -n 'Usage: deploy-local.sh' "$deploy_script")"
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
# ★ SSOT 的解析已在文件开头完成（`ssot`）⇒ 这里不重复解析。
if [[ -n "$ssot" ]]; then
  mkdir -p "$lock_path"
  printf 'target=local-switch\npid=999999\nstarted_at=2026-10-28T00:00:00Z\n' > "$lock_path/metadata"
  out="$(LOCK_TMP_ROOT="$LOCK_TMP_ROOT" bash "$deploy_script" recover-switch-lock 2>&1)"; rc=$?
  [[ "$rc" -eq 0 ]] || fail "the real recover-switch-lock command must succeed on a stale lock, got rc=$rc ($out)"
  [[ ! -e "$lock_path" ]] || fail 'the real recover-switch-lock command must remove the stale lock'
  pass 'recover-switch-lock is a real, working command (stale lock removed)'
else
  printf 'SKIP real recover-switch-lock invocation: shared deploy lib not found (the registration assertions above still ran)\n'
  pass 'recover-switch-lock is registered in all three places'
fi

# ═════════════════════════════════════════════════════════════════════════
# 2026-10-28（第二轮批判性审计）：★ 「锁到底互斥吗」—— 存活判据本身的四条不变量
# ═════════════════════════════════════════════════════════════════════════

# ── I14：存活判据 —— 「持有者还活着吗」必须由【共享部署库那一份】回答 ─────
#
# ★ 第三轮（合并就绪轮）起，本仓**不再自带**存活探针 ⇒ `dl_pid_alive` 改为
#   **委托**共享部署库的 `lock_pid_alive` ⇒ 这条不变量现在钉的是**委托后的
#   整条链**：本仓的 `dl_pid_alive` 必须与共享部署库的 `lock_pid_alive`
#   给出**同一个答案**，且等于 **OS 的事实**。
#   ★「委托」不等于「判据消失」：委托之后，**前提**（共享部署库已加载）
#     变成新的失效面 ⇒ 由 I19 单独钉它。
#
# ★ 三个臂：
#   (a) **模拟臂**：把判据换成会说「死」/说「活」的替身 ⇒ 本仓必须**跟着**
#       说 ⇒ 证明链是通的（不是本仓自己另算了一套）。
#   (b) **真机跨用户**：pid 1（launchd / systemd，root 的）⇒ 普通用户对它
#       kill -0 只会得到 EPERM ⇒ 这是**真实**的 EPERM 场景。
#   (c) **否定臂**：死 pid / pid 0 / 畸形 pid 必须判「死」⇒ 防止判据变成
#       「永远活着」。
# ★ ★ 模拟 EPERM 臂在第三轮**退役**：判据已归共享部署库（ps 口径）⇒ 实现里
#   **不再有 EPERM 分支** ⇒「模拟 EPERM」这件事本身已不存在 ⇒ 留着它会让人
#   误以为本仓还有一个 kill -0 快通道。⇒ EPERM 类的牙现在由「真机跨用户臂」
#   + 变异臂（把委托换回 kill -0-only 实现 ⇒ 必须红）承担。
#
# ★ 前提检查（★ 仓内纪律：造「某件事不会发生」的前提时，先证这个前提成立）：
#   否定臂在「判据没加载」时也会输出 DEAD ⇒ **假绿** ⇒ 先证判据确实可用。
if ! command -v lock_pid_alive >/dev/null 2>&1; then
  fail 'precondition: the liveness oracle must be loaded (shared SSOT or its test double)'
fi

verdict_sim_dead="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  source "$2"
  # 模拟：判据说「这个 pid 不存在」
  lock_pid_alive() { return 1; }
  dl_pid_alive 424242 && printf ALIVE || printf DEAD
' _ "$ROOT" "$ORACLE_BOOTSTRAP")"
[[ "$verdict_sim_dead" == "DEAD" ]] \
  || fail "the repo must follow the shared oracle verdict (a dead pid must be DEAD), got $verdict_sim_dead"

verdict_sim_alive="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  source "$2"
  # 模拟：判据说「这个 pid 活着」（跨用户的活进程 ⇒ ps 看得见）
  lock_pid_alive() { return 0; }
  dl_pid_alive 424242 && printf ALIVE || printf DEAD
' _ "$ROOT" "$ORACLE_BOOTSTRAP")"
[[ "$verdict_sim_alive" == "ALIVE" ]] \
  || fail "the repo must follow the shared oracle verdict (a live foreign-user pid must be ALIVE), got $verdict_sim_alive"

verdict_live="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  source "$2"
  dl_pid_alive $$ && printf ALIVE || printf DEAD
' _ "$ROOT" "$ORACLE_BOOTSTRAP")"
[[ "$verdict_live" == "ALIVE" ]] || fail "the calling process must be judged ALIVE, got $verdict_live"

verdict_dead="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  source "$2"
  dl_pid_alive 999999 && printf ALIVE || printf DEAD
' _ "$ROOT" "$ORACLE_BOOTSTRAP")"
[[ "$verdict_dead" == "DEAD" ]] || fail "a dead pid must be judged DEAD, got $verdict_dead"

# ★ pid 0 不是真进程（`kill -0 0` 探的是**进程组**、恒成功）⇒ 必须判死，
#   否则一份 pid=0 的坏元数据会让锁**永远不可接管** ⇒ 机器被永久卡死。
verdict_zero="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  source "$2"
  dl_pid_alive 0 && printf ALIVE || printf DEAD
' _ "$ROOT" "$ORACLE_BOOTSTRAP")"
[[ "$verdict_zero" == "DEAD" ]] || fail "pid 0 is not a process and must be judged DEAD, got $verdict_zero"

verdict_junk="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  source "$2"
  dl_pid_alive abc && printf ALIVE || printf DEAD
' _ "$ROOT" "$ORACLE_BOOTSTRAP")"
[[ "$verdict_junk" == "DEAD" ]] || fail "a malformed pid must be judged DEAD, got $verdict_junk"

# (b) 真机跨用户臂：先证「EPERM 前提在判据运行时真的成立」，再判结论。
#     ★ 仓内纪律：造「某件事不会发生」的前提时，先证这个前提成立。
if [[ "$(id -u)" -eq 0 ]]; then
  printf 'SKIP real cross-user liveness arm: running as root (no EPERM observable; the simulated arms above cover it)\n'
elif kill -0 1 2>/dev/null; then
  printf 'SKIP real cross-user liveness arm: pid 1 is signalable by this user (no EPERM to observe)\n'
else
  # 前提成立：kill -0 1 失败（EPERM / 无权限），而 ps 看得见它 ⇒ 它**活着**。
  ps -p 1 -o pid= >/dev/null 2>&1 \
    || fail 'precondition: pid 1 must be visible to ps for the cross-user liveness arm to mean anything'
  verdict_x="$(bash -c '
    source "$1/scripts/deploy-local-lib.sh"
    source "$2"
    dl_pid_alive 1 && printf ALIVE || printf DEAD
  ' _ "$ROOT" "$ORACLE_BOOTSTRAP")"
  [[ "$verdict_x" == "ALIVE" ]] \
    || fail "a live process owned by another user must be judged ALIVE (EPERM is not death), got $verdict_x"
  pass 'cross-user liveness: a live foreign-user process is ALIVE (real EPERM observed)'
fi
pass 'liveness oracle: delegated to the shared SSOT (dead/zero/malformed are DEAD)'

# ── I19：判据不可用 ⇒ **显式失败**，不许猜（★ F1 的新变体的专治）───────────
#
# ★ 委托之后的新失效面：「共享部署库没被 source」⇒ 若本仓悄悄退化
#   （把「不知道」当成「持有者已死」）⇒ **接管掉一把活锁** ⇒ 互斥被拆
#   ⇒ ★ 这就是 F1 换了个入口 ⇒ 必须有专门的判据。
# ⇒ 三条断言：
#   ① `dl_pid_alive` 在判据缺席时返回 **2（不知道）**，不是 1（死）；
#   ② `dl_acquire_switch_lock` 在判据缺席时 **rc=75 + 可读原因**
#      ⇒ 绝不接管、绝不创建锁；
#   ③ `dl_switch_lock_is_stale` 在判据缺席时判 **不是残锁**（fail-closed）。
nossot_root="$TMP/nossot"
mkdir -p "$nossot_root"
unknown_rc="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  # ★ 库文件自带 `set -euo pipefail` ⇒ 非零返回会**中断**子 shell
  #   ⇒ 取退出码必须放在条件上下文里（★ 与生产代码同一纪律）。
  if dl_pid_alive $$ >/dev/null 2>&1; then rc=0; else rc=$?; fi
  printf "rc=%s\n" "$rc"
' _ "$ROOT")"
[[ "$unknown_rc" == "rc=2" ]] \
  || fail "without the shared oracle the liveness verdict must be UNKNOWN (rc=2), not DEAD (rc=1), got $unknown_rc"

rc=0
nossot_out="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  export LOCK_TMP_ROOT="$2"
  dl_acquire_switch_lock
' _ "$ROOT" "$nossot_root" 2>&1)" || rc=$?
[[ "$rc" -eq 75 ]] \
  || fail "without the shared oracle, acquiring the switch lock must fail fast with 75, got rc=$rc ($nossot_out)"
grep -qi 'not loaded' <<<"$nossot_out" \
  || fail "the refusal must say WHY (the shared oracle is missing), got: $nossot_out"
[[ ! -e "$nossot_root/kx-llm-gateway-switch.lock" ]] \
  || fail 'without the shared oracle the switch lock must not be created or taken over'

stale_verdict="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  export LOCK_TMP_ROOT="$2"
  p="$LOCK_TMP_ROOT/kx-llm-gateway-switch.lock"
  mkdir -p "$p"
  printf "target=local-switch\npid=%s\nstarted_at=2026-10-28T00:00:00Z\n" "$$" > "$p/metadata"
  dl_switch_lock_is_stale && printf STALE || printf NOT-STALE
' _ "$ROOT" "$nossot_root")"
[[ "$stale_verdict" == "NOT-STALE" ]] \
  || fail "an UNKNOWN holder liveness must not be treated as a stale lock (fail-closed), got $stale_verdict"

# ④ 恢复命令在判据缺席时也必须**拒绝**（不猜、不清）。
recover_nossot_rc=0
recover_nossot="$(bash -c '
  source "$1/scripts/deploy-local-lib.sh"
  export LOCK_TMP_ROOT="$2"
  p="$LOCK_TMP_ROOT/kx-llm-gateway-switch.lock"
  mkdir -p "$p"
  printf "target=local-switch\npid=%s\nstarted_at=2026-10-28T00:00:00Z\n" "$$" > "$p/metadata"
  dl_recover_switch_lock
' _ "$ROOT" "$nossot_root" 2>&1)" || recover_nossot_rc=$?
[[ "$recover_nossot_rc" -ne 0 ]] \
  || fail "recovery must refuse when the liveness of the holder cannot be determined, got rc=0 ($recover_nossot)"
[[ -e "$nossot_root/kx-llm-gateway-switch.lock" ]] \
  || fail 'recovery must not remove the lock when the shared oracle is missing'
pass 'a missing shared oracle fails fast and never guesses (UNKNOWN != DEAD)'

# ── I15：恢复命令的两档（持有者读不出来 ⇒ 默认档拒绝；--force 才清）──────
#
# ★ 旧实现在「读不出 pid」时直接清掉 ⇒ 与「残锁判定」的口径**相反**
#   （一个说「不是残锁、别碰」，另一个说「清掉」）⇒ 而且清掉的可能是别人的活锁。
mkdir -p "$lock_path"
printf 'target=local-switch\nstarted_at=2026-10-28T00:00:00Z\n' > "$lock_path/metadata"
rc=0
recover_out="$(dl_recover_switch_lock 2>&1)" || rc=$?
[[ "$rc" -ne 0 ]] || fail "recovery must REFUSE when the holder cannot be identified, got rc=0 ($recover_out)"
[[ -e "$lock_path" ]] || fail 'a refused recovery must not remove the lock'
grep -qi 'cannot be determined' <<<"$recover_out" \
  || fail "the refusal must say WHY (holder unidentifiable), got: $recover_out"
rc=0
dl_recover_switch_lock --force >/dev/null 2>&1 || rc=$?
[[ "$rc" -eq 0 ]] || fail "--force recovery must remove the lock, got rc=$rc"
[[ ! -e "$lock_path" ]] || fail '--force recovery must remove the lock'
pass 'recovery refuses an unidentifiable holder; --force is the explicit escape hatch'

# ── I16：半截元数据不算存活证据 ⇒ 不接管（宁可拒绝，不可误杀活锁）────────
#
# ★ 元数据被 SIGKILL 打断 ⇒ "pid=999998" 而真实持有者可能是另一个 pid
#   ⇒ 旧实现会把那个**错误的** pid 当证据 ⇒ 它大概率不存在 ⇒ 判成残锁
#   ⇒ **接管掉一把可能还活着的锁**。
# ★ 夹具用「一个确实不存在的 pid」而不是「活着的 pid」⇒ 这样这条判据才有牙：
#   若去掉「元数据必须完整」这条规则，本条会立刻变红（变成接管 ⇒ rc=0）。
mkdir -p "$lock_path"
printf 'target=local-switch\npid=999998\n' > "$lock_path/metadata"
rc=0
dl_acquire_switch_lock >/dev/null 2>&1 || rc=$?
[[ "$rc" -eq 75 ]] || fail "a half-written metadata file must NOT be treated as a stale lock, got rc=$rc"
[[ -f "$lock_path/metadata" ]] || fail 'the half-written metadata must be left untouched'
grep -q '^pid=999998' "$lock_path/metadata" || fail 'the half-written metadata must be left untouched'
rm -rf "$lock_path"
pass 'a half-written metadata file is not liveness evidence (no takeover)'

# ── I17：「活着 ⇒ 拒绝」必须因为「活着」而拒绝 ───────────────────────────
#
# ★ 判据防呆：I9 的「活着的持有者 ⇒ 拒绝」在元数据不完整时也会「拒绝」⇒
#   ⇒ 判据可能因为**错误的原因**变绿 ⇒ 这里钉住**理由**（消息里必须是 alive）。
mkdir -p "$lock_path"
printf 'target=local-switch\npid=%s\nstarted_at=2026-10-28T00:00:00Z\n' "$$" > "$lock_path/metadata"
rc=0
recover_out="$(dl_recover_switch_lock 2>&1)" || rc=$?
[[ "$rc" -ne 0 ]] || fail 'recovery must refuse a live holder'
grep -qi 'still alive' <<<"$recover_out" \
  || fail "the refusal must be because the holder is ALIVE, got: $recover_out"
rm -rf "$lock_path"
pass 'a live holder is refused because it is alive (not because metadata was unreadable)'

# ═════════════════════════════════════════════════════════════════════════
# ★ I11~I13：flock 形态 —— 上一轮的门**一次都没执行过**这一半实现
# ═════════════════════════════════════════════════════════════════════════
#
# ★ 本机（macOS）没有 flock ⇒ 旧门永远走 mkdir 分支 ⇒ flock 分支的缺陷不可见。
# ⇒ 这里造一个**真语义**的假 flock（python fcntl.flock，与 util-linux flock -n
#   同样语义：抢不到就退出 1）⇒ 把 flock 分支拉进**生产路径**
#   （dl_acquire_switch_lock 自己按可用性选分支，不是直接调内部函数）。
fake_flock="$TMP/fake-flock"
# ★ 假 flock 来自**共享 helper**（tests/lib/fake-flock.sh）⇒ 仓内只此一份
#   ⇒ 另一道门（跨仓对拍门）也用它 ⇒ 两边量的是同一个东西。
# shellcheck source=lib/fake-flock.sh
source "$ROOT/tests/lib/fake-flock.sh"
# ★ 注意：透传 "$@"（第 2 个起）⇒ 被测脚本里的 "$1"（仓库根）才拿得到。
# ★ ★ `flock_env` 定义在**守卫之外** ⇒ ★ 否则缺 python3 时连「定义」都没了
#   ⇒ 后面用它的断言会报 "command not found" ⇒ ★★ 而那是**夹具坏**，
#   不是被测行为 ⇒ ★★ 判据自己的坏必须与被测物的坏分开（★ 通式八）。
flock_env() {
  LOCK_TMP_ROOT="$LOCK_TMP_ROOT" DL_SWITCH_LOCK_FLOCK_BIN="$fake_flock" \
    bash -c "source \"$ORACLE_BOOTSTRAP\"
$1" "${@:2}"
}
# ★★ 缺件就明说没跑：★ 替身需要 python3（fcntl.flock）⇒ CI 的 alpine 镜像实测
#   没有 python3 ⇒ ★★ 那时**不能**让门红（那是环境缺件，不是代码缺陷）
#   ⇒ ⇒ 显式 SKIP 这一段（★ 仓内纪律：「本次没跑」必须写出来）。
if ! command -v python3 >/dev/null 2>&1; then
  printf 'SKIP I11~I13 (flock shape): python3 is not available (the true-semantics fake flock needs fcntl) — 本次没跑这一段\n'
else
fake_flock="$(fake_flock_make "$TMP/fake-flock")"

# ★ 注意：透传 "$@"（第 2 个起）⇒ 被测脚本里的 "$1"（仓库根）才拿得到。
# ★ 第三轮：判据已**委托**共享部署库 ⇒ 子 shell 里必须把判据一起加载
#   （否则子 shell 会走「判据缺席 ⇒ fail-fast」那条路 ⇒ flock 形态的臂
#   会因为**理由不对**而变绿/变红 ⇒ ★ 判据必须因为「语义」而通过）。

# ── I11：flock 形态可达且语义正确 ────────────────────────────────────────
out="$(flock_env '
  source "$1/scripts/deploy-local-lib.sh"
  dl_acquire_switch_lock || { echo "ACQUIRE_FAILED"; exit 1; }
  if [[ -d "$(dl_switch_lock_path)" ]]; then echo "SHAPE_DIR"; else echo "SHAPE_FILE"; fi
  rc=0; msg="$(dl_acquire_switch_lock 2>&1)" || rc=$?
  echo "CONTEND_RC=$rc"
  case "$msg" in *"another checkout is switching"*) echo "CONTEND_MSG=OK";; *) echo "CONTEND_MSG=BAD";; esac
' _ "$ROOT")"
grep -q 'SHAPE_FILE' <<<"$out" \
  || fail "with flock available the lock must take the flock shape (a regular file), got: $out"
grep -q 'CONTEND_RC=75' <<<"$out" \
  || fail "flock shape: contention must fail fast with 75, got: $out"
grep -q 'CONTEND_MSG=OK' <<<"$out" \
  || fail "flock shape: contention must name the holder, got: $out"
pass 'flock shape is reachable and behaves (acquire / contend-75 / holder metadata)'

# ── I13：flock 释放不得删锁文件（unlink-after-unlock 竞态）────────────────
out="$(flock_env '
  source "$1/scripts/deploy-local-lib.sh"
  dl_acquire_switch_lock || exit 1
  dl_release_switch_lock
  if [[ -f "$(dl_switch_lock_path)" ]]; then echo "KEPT"; else echo "REMOVED"; fi
  dl_acquire_switch_lock || { echo "REACQUIRE_FAILED"; exit 1; }
  echo "REACQUIRE=OK"
' _ "$ROOT")"
grep -q 'KEPT' <<<"$out" \
  || fail "flock release must NOT unlink the lock file (unlink-after-unlock race => two holders), got: $out"
grep -q 'REACQUIRE=OK' <<<"$out" \
  || fail "flock shape: the lock must be re-acquirable after release, got: $out"
pass 'flock release keeps the lock file (no unlink-after-unlock race)'

# ── I12：形态混用不许把脚本带崩 ──────────────────────────────────────────
#
# ★ 旧实现：flock 分支 `exec <> "$path"` 撞上目录 ⇒ EISDIR ⇒ POSIX 规定 exec 的
#   重定向失败是**致命**的 ⇒ 整个部署脚本带着 "Is a directory" 死掉
#   ⇒ 运维看到的是「脚本崩了」，不是「有人在切流」。
# ⇒ 现在：目录形态 ⇒ 一律走目录语义（残锁接管 / 拒绝）。
out="$(flock_env '
  source "$1/scripts/deploy-local-lib.sh"
  path="$(dl_switch_lock_path)"
  # ★ 先清场：上一条不变量故意留下了 flock 形态的**文件**（I13）⇒ mkdir 会撞上它。
  rm -rf "$path"
  mkdir -p "$path"
  printf "target=local-switch\npid=%s\nstarted_at=2026-10-28T00:00:00Z\n" "$$" > "$path/metadata"
  rc=0; dl_acquire_switch_lock >/dev/null 2>&1 || rc=$?
  echo "LIVE_DIR_RC=$rc"
  rm -rf "$path"
  mkdir -p "$path"
  printf "target=local-switch\npid=999999\nstarted_at=2026-10-28T00:00:00Z\n" > "$path/metadata"
  rc=0; dl_acquire_switch_lock >/dev/null 2>&1 || rc=$?
  echo "STALE_DIR_RC=$rc"
' _ "$ROOT")"
grep -q 'LIVE_DIR_RC=75' <<<"$out" \
  || fail "a live directory-shaped lock must fail fast with 75 even when flock is available, got: $out"
grep -q 'STALE_DIR_RC=0' <<<"$out" \
  || fail "a stale directory-shaped lock must be taken over even when flock is available, got: $out"
pass 'lock shape is detected from the path (a directory lock never hits the fatal exec)'
fi

# ── I18/I19/I20：★ CLI 层的「强制档」必须【只在它该生效的地方】生效 ────────
#
# ★ 2026-10-28 第二轮：`--force` 是本轮**新增的 CLI 开关** ⇒ 它的接线本身也要钉住：
#   ① 只允许与 recover-switch-lock 同用（与别的动作同用 ⇒ 静默忽略一个「强制」标记
#      比报错危险得多 ⇒ 直接 exit 64）；
#   ② `--force` 真能清掉「持有者读不出来」的锁（那正是它存在的理由）；
#   ③ 默认档在同一状态下**仍然拒绝**（⇒ --force 是【升级档】不是【默认档】）。
if [[ -n "$ssot" ]]; then
  # ① --force 与别的动作同用 ⇒ 拒绝
  rc=0
  out="$(LOCK_TMP_ROOT="$LOCK_TMP_ROOT" bash "$deploy_script" status --force 2>&1)" || rc=$?
  [[ "$rc" -eq 64 ]] || fail "--force with a non-recover action must be refused with 64, got rc=$rc ($out)"
  grep -q 'only valid with recover-switch-lock' <<<"$out" \
    || fail "--force misuse must say what is allowed, got: $out"

  # ② --force 真能清掉「持有者读不出来」的锁
  mkdir -p "$lock_path"
  printf 'target=local-switch\nstarted_at=2026-10-28T00:00:00Z\n' > "$lock_path/metadata"
  rc=0
  out="$(LOCK_TMP_ROOT="$LOCK_TMP_ROOT" bash "$deploy_script" recover-switch-lock --force 2>&1)" || rc=$?
  [[ "$rc" -eq 0 ]] || fail "the real recover-switch-lock --force must remove an unidentifiable-holder lock, got rc=$rc ($out)"
  [[ ! -e "$lock_path" ]] || fail 'recover-switch-lock --force must remove the lock'
  grep -qi 'force' <<<"$out" || fail "--force must announce itself loudly, got: $out"

  # ③ 默认档在同一状态下仍然拒绝
  mkdir -p "$lock_path"
  printf 'target=local-switch\nstarted_at=2026-10-28T00:00:00Z\n' > "$lock_path/metadata"
  rc=0
  out="$(LOCK_TMP_ROOT="$LOCK_TMP_ROOT" bash "$deploy_script" recover-switch-lock 2>&1)" || rc=$?
  [[ "$rc" -ne 0 ]] || fail 'the default tier must still refuse an unidentifiable holder'
  [[ -e "$lock_path" ]] || fail 'a refused default-tier recovery must not remove the lock'
  rm -rf "$lock_path"
  pass 'the --force tier is wired: refused elsewhere, effective here, default tier still refuses'
else
  printf 'SKIP I18~I20: shared deploy lib not found (the recover-switch-lock --force CLI needs it)\n'
fi

# ── I21：两种形态都必须能自己建父目录（★ 实测发现的形态不对称）────────────
#
# ★ 2026-10-28 第三轮：★ 实测跨进程互斥时**当场撞到** ——
#   flock 形态那支建父目录（`mkdir -p "$dir"`），★ mkdir 形态那支**不建**
#   ⇒ ⇒ 父目录不存在时，mkdir 形态的 `mkdir "$path"` 直接失败 ⇒
#   ⇒ 报出来的是「**锁被占用**」，而真相是「**路径不存在**」
#   ⇒ ⇒ **报错理由不对** ⇒ 运维会照着错误结论去 unlock 一把并不存在的锁。
# ⇒ 两条断言：① mkdir 形态在父目录不存在时也能拿到锁；② flock 形态同样。
missing_root="$TMP/missing-root/locks"
rc=0
( export LOCK_TMP_ROOT="$missing_root"; dl_acquire_switch_lock ) >/dev/null 2>&1 || rc=$?
[[ "$rc" -eq 0 ]] \
  || fail "the mkdir shape must create its own parent directory (rc=$rc)"
[[ -d "$missing_root" ]] || fail 'the parent directory of the switch lock must have been created'
rm -rf "$TMP/missing-root"
if command -v python3 >/dev/null 2>&1; then
  out="$(flock_env '
  source "$1/scripts/deploy-local-lib.sh"
  export LOCK_TMP_ROOT="$2/missing-root/locks"
  if dl_acquire_switch_lock; then echo "ACQUIRE=OK"; else echo "ACQUIRE=FAIL"; fi
' _ "$ROOT" "$TMP/flock-parent")"
  grep -q 'ACQUIRE=OK' <<<"$out" \
    || fail "the flock shape must create its own parent directory, got: $out"
  rm -rf "$TMP/flock-parent"
  pass 'both lock shapes create their own parent directory (no asymmetry)'
else
  printf 'SKIP the flock half of I21: python3 is not available — 本次没跑这一段\n'
  pass 'the mkdir shape creates its own parent directory (flock half skipped: no python3)'
fi

# ── I20：★ 争用在 **CLI 层**也必须是 75（EX_TEMPFAIL）──────────────────────
#
# ★ 2026-10-28 第三轮：★ **声名与实现不符** ——
#   切流锁争用的注释与设计都说「rc 75（EX_TEMPFAIL）」，而生产入口用的是
#   `|| die …` ⇒ `die` 退 **1** ⇒ ⇒ **CLI 层的 rc 75 契约从来没兑现过**
#   ⇒ ⇒ 监督方（CI / 脚本 / 运维手册）按 75 判断「这是争用、稍后重试」，
#     而实际拿到的是 1 ⇒ **「稍后重试」与「硬失败」不可区分**。
# ⇒ 两条断言：
#   ① `die75` 的退出码真的是 75（★ **行为级**：把它抠出来真跑一次）；
#   ② 争用的调用点用的是 die75 而不是 die（★ **源码级**，如实标注）。
die75_fn="$(sed -n '/^die75()/,/^}/p' "$deploy_script")"
[[ -n "$die75_fn" ]] \
  || fail 'deploy-local.sh must define a 75-exit helper for the contention path'
rc=0
( eval "$die75_fn"; die75 'contention probe' ) >/dev/null 2>&1 || rc=$?
[[ "$rc" -eq 75 ]] \
  || fail "the contention exit helper must exit 75 (EX_TEMPFAIL), got rc=$rc"
acquire_line="$(grep -n 'dl_acquire_switch_lock ||' "$deploy_script" | head -1)"
[[ "$acquire_line" == *die75* ]] \
  || fail "the switch-lock acquire call site must use the 75-exit helper (rc 75 is the documented contract), got: $acquire_line"
pass 'contention is rc 75 (EX_TEMPFAIL) at the CLI level too, not only inside the library'

printf 'ALL PASS deploy switch lock contract\n'
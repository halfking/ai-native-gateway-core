#!/usr/bin/env bash
# tests/lib/fake-flock.sh — a **true-semantics** fake `flock` (shared helper).
#
# ## 为什么需要它
#
# ★ macOS **没有** util-linux 的 `flock(1)` ⇒ 本机所有「flock 形态」的锁实现
#   分支**一次都没被执行过** ⇒ 那半边实现的缺陷对门**完全不可见**
#   （★ 仓内纪律：判据必须覆盖【生产链上的那一段】，而不是【调用形状】）。
# ⇒ 这里造一个**语义等价**的替身：util-linux 的
#   `flock -n <fd>` = 对已打开的 fd 施加**非阻塞排他锁** ⇒ 抢不到就退出 1。
#   python 的 `fcntl.flock(fd, LOCK_EX|LOCK_NB)` 语义完全一致 ⇒ 可直接替换。
#
# ## 用法
#
#   source tests/lib/fake-flock.sh
#   fake_flock_path="$(fake_flock_make "$TMP/fake-flock")"
#   # 之后把 fake_flock_path 当成 flock 可执行文件用：
#   #   DL_SWITCH_LOCK_FLOCK_BIN="$fake_flock_path"   （本仓切流锁）
#   #   LOCK_FLOCK_BIN="$fake_flock_path"             （共享部署库 lock.sh）
#
# ## 诚实记账（★ 别把它当成「真的 flock」）
#
# ★ 替身只实现 `-n <fd>` 这一种调用形态 ⇒ 共享部署库/本仓的
#   `flock -n "$fd"` 都能被替换；**其它参数组合不在本 helper 的射程内**。
# ★ 替身**不**模拟「fd 号非法」等错误路径 ⇒ 被测代码把空 fd 递给 flock 时，
#   替身会**报错退出**（而不是像真 flock 那样报错但语义不同）⇒
#   这类差异必须在判据里**单独说明**，不能默认两种实现等价。
#
# ★★ 更要紧的一条：★ **bash 3.2（macOS 的 /bin/bash）不支持 `exec {VAR}<>`**
#   ⇒ 任何依赖「自动分配 fd」的锁实现在 bash 3.2 上都是**坏的**
#   ⇒ 本仓第二轮已经把切流锁改成固定 fd（dl_open_lock_fd）；
#   ⇒ **共享部署库 lock.sh 的 flock 分支至今仍是 `{VAR}` 形态**
#   ⇒ 那半边实现在本机**根本跑不起来**（fd 从未分配 ⇒ flock 收到空 fd）
#   ⇒ 这就是「合并」必须做的原因之一（★ 详见审计档第三轮）。

# fake_flock_make <输出路径> ⇒ 写出替身并打印其路径
fake_flock_make() {
  local out="${1:?usage: fake_flock_make <path>}"
  mkdir -p "$(dirname "$out")" 2>/dev/null || true
  cat > "$out" <<'PYEOF'
#!/usr/bin/env python3
import fcntl, sys
args = sys.argv[1:]
if args and args[0] == "-n":
    args = args[1:]
if not args:
    sys.stderr.write("fake-flock: missing fd operand\n")
    sys.exit(2)
try:
    fd = int(args[0])
except ValueError:
    # ★ 空 fd / 非数字 fd = 被测实现**根本没分配到 fd**
    #   （bash 3.2 的 `exec {VAR}<>` 不受支持 ⇒ 共享部署库 lock.sh 的 flock
    #   分支正是这样坏的）⇒ ⇒ 这不是「锁被占用」，必须与 rc=1 区分开。
    sys.stderr.write("fake-flock: invalid fd operand: %r\n" % (args[0],))
    sys.exit(2)
try:
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
except OSError:
    sys.exit(1)
sys.exit(0)
PYEOF
  chmod +x "$out"
  printf '%s\n' "$out"
}

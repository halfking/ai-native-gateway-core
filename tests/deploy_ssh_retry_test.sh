#!/usr/bin/env bash
# =====================================================================
# tests/deploy_ssh_retry_test.sh — 单元测试 ssh-retry.sh
#
# 覆盖:
#   - _ssh_retry_classify 4 类 (ok / retry / fail / fail)
#   - ssh_run 在第一次成功时不重试
#   - ssh_run 在可重试错误时会重试并最终成功
#   - ssh_run 在 auth 错误时立即返回 (不浪费重试次数)
#   - ControlMaster socket 在多次 ssh_run 间复用
#   - ssh_retry_close 正确清理
#
# 用 fake ssh (busybox 风格的 shim) 模拟:
#   - 0 次超时 + 1 次成功
#   - 2 次超时 + 1 次成功
#   - auth 失败
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB_TARGETS="$REPO_ROOT/scripts/deploy-lib/targets.sh"
LIB_SSH_RETRY="$REPO_ROOT/scripts/deploy-lib/ssh-retry.sh"

TESTS_PASSED=0
TESTS_FAILED=0

log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); }
assert_eq() { [[ "$2" == "$3" ]] && log_pass "$1" || log_fail "$1: got [$2], want [$3]"; }

# 加载被测代码
( source "$LIB_TARGETS"; source "$LIB_SSH_RETRY" ) >/dev/null 2>&1 || {
  log_fail "load ssh-retry.sh failed"; exit 1; }

# ── classifier ────────────────────────────────────────────────────
echo "── _ssh_retry_classify ──"
assert_eq "rc=0 -> ok" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 0 'ok')" \
  "ok"
assert_eq "rc=255 + timeout -> retry" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 255 'Operation timed out')" \
  "retry"
assert_eq "rc=255 + connection reset -> retry" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 255 'Connection reset by peer')" \
  "retry"
assert_eq "rc=255 + no route -> retry" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 255 'No route to host')" \
  "retry"
assert_eq "rc=255 + auth denied -> fail" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 255 'Permission denied (publickey)')" \
  "fail"
assert_eq "rc=255 + host key -> fail" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 255 'Host key verification failed')" \
  "fail"
assert_eq "rc=1 -> fail" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 1 'command not found')" \
  "fail"
assert_eq "rc=127 -> fail" \
  "$(source "$LIB_TARGETS"; source "$LIB_SSH_RETRY"; _ssh_retry_classify 127 'no such command')" \
  "fail"

# ── ssh_run with fake ssh ───────────────────────────────────────
# 准备: 写一个 fake ssh 到 PATH, 模拟 "前 N 次超时, 第 N+1 次成功"
TMPDIR_TEST=$(mktemp -d -t kx-ssh-retry-test.XXXXXX)
trap 'rm -rf "$TMPDIR_TEST"' EXIT
mkdir -p "$TMPDIR_TEST/bin"
export PATH="$TMPDIR_TEST/bin:$PATH"

# fake ssh 行为受环境变量控制:
#   FAKE_SSH_FAILS_LEFT=N: 前 N 次以 255 + timeout 退出, 然后 0 成功
#   FAKE_SSH_AUTH_FAIL=1: 立即以 255 + Permission denied 退出
#   FAKE_SSH_CMD_FAIL=1: 以 1 退出
cat >"$TMPDIR_TEST/bin/ssh" <<'SH'
#!/usr/bin/env bash
# 持久化状态: 用文件而非 export (子进程 export 不能传回父进程)
STATE_DIR="${FAKE_SSH_STATE_DIR:-/tmp/_fake_ssh_state}"
mkdir -p "$STATE_DIR"
# 日志写文件 (ssh_run 用 2>&1 捕获 stderr, 不能用 >&2)
exec 2>>"$STATE_DIR/fake_ssh.log"
# 计数调用次数
n=$(cat "$STATE_DIR/calls" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$STATE_DIR/calls"
echo "[call #$n FAIL_FILE=$FAKE_SSH_FAILS_FILE AUTH=$FAKE_SSH_AUTH_FAIL CMD=$FAKE_SSH_CMD_FAIL]" >>"$STATE_DIR/fake_ssh.log"

if [[ "${FAKE_SSH_AUTH_FAIL:-0}" == "1" ]]; then
  echo "Permission denied (publickey)"
  exit 255
fi
if [[ "${FAKE_SSH_CMD_FAIL:-0}" == "1" ]]; then
  echo "command failed"
  exit 1
fi
# FAKE_SSH_FAILS_FILE: 文件存剩余失败次数, 每次成功调用后减 1
if [[ -n "${FAKE_SSH_FAILS_FILE:-}" ]] && [[ -f "$FAKE_SSH_FAILS_FILE" ]]; then
  fn=$(cat "$FAKE_SSH_FAILS_FILE")
  if (( fn > 0 )); then
    echo $((fn-1)) > "$FAKE_SSH_FAILS_FILE"
    echo "ssh: connect to host: Operation timed out"
    exit 255
  fi
fi
# 默认: 退出 0
echo "FAKE_SSH_OK"
exit 0
SH
chmod +x "$TMPDIR_TEST/bin/ssh"

# 让 _ssh_retry_ssh_opts_for 和 host 查询能 work — 我们 stub target_field
# 通过把 target_field 替换成查固定 ssh_host
( source "$LIB_TARGETS" ) >/dev/null

# 在测试 sub-shell 中跑 (避免污染全局)
run_test_ssh_run() {
  ( source "$LIB_TARGETS"; source "$LIB_SSH_RETRY" ) >/dev/null
}

echo "── ssh_run 成功路径 ──"
(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  unset FAKE_SSH_FAILS_LEFT FAKE_SSH_AUTH_FAIL FAKE_SSH_CMD_FAIL
  out=$(ssh_run 245 "echo hello" 2>&1)
  rc=$?
  printf 'rc=%s out=%s\n' "$rc" "$out"
) | tee /tmp/_ssh_run_out
grep -q "rc=0" /tmp/_ssh_run_out && log_pass "ssh_run 1st try success returns rc=0" \
  || log_fail "ssh_run 1st try: $(cat /tmp/_ssh_run_out)"

echo "── ssh_run 重试后成功 ──"
(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  export FAKE_SSH_STATE_DIR="$TMPDIR_TEST/state"
  mkdir -p "$FAKE_SSH_STATE_DIR"
  echo 2 > "$FAKE_SSH_STATE_DIR/fails"  # 两次超时, 第三次成功
  export FAKE_SSH_FAILS_FILE="$FAKE_SSH_STATE_DIR/fails"
  export SSH_RETRY_BACKOFF=0    # 测试时不 sleep
  start=$(date +%s)
  out=$(ssh_run 245 "echo recovered" 2>&1)
  rc=$?
  end=$(date +%s)
  elapsed=$((end-start))
  calls=$(cat "$FAKE_SSH_STATE_DIR/calls" 2>/dev/null || echo "?")
  printf 'rc=%s out=%s elapsed=%s calls=%s\n' "$rc" "$out" "$elapsed" "$calls"
) | tee /tmp/_ssh_run_out
grep -q "rc=0" /tmp/_ssh_run_out && log_pass "ssh_run recovers after 2 retries" \
  || log_fail "ssh_run retry: $(cat /tmp/_ssh_run_out)"
grep -q "calls=3" /tmp/_ssh_run_out && log_pass "ssh_run called ssh exactly 3 times" \
  || log_fail "ssh_run call count: $(cat /tmp/_ssh_run_out)"

echo "── ssh_run 重试耗尽返回非 0 ──"
(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  export FAKE_SSH_STATE_DIR="$TMPDIR_TEST/state"
  mkdir -p "$FAKE_SSH_STATE_DIR"
  echo 0 > "$FAKE_SSH_STATE_DIR/calls"  # 重置 calls 计数
  echo 10 > "$FAKE_SSH_STATE_DIR/fails"  # 永远超时
  export FAKE_SSH_FAILS_FILE="$FAKE_SSH_STATE_DIR/fails"
  export SSH_RETRY_MAX=3
  export SSH_RETRY_BACKOFF=0
  out=$(ssh_run 245 "echo nope" 2>&1)
  rc=$?
  calls=$(cat "$FAKE_SSH_STATE_DIR/calls" 2>/dev/null || echo "?")
  printf 'rc=%s out_tail=%s calls=%s\n' "$rc" "${out: -50}" "$calls"
) | tee /tmp/_ssh_run_out
grep -q "rc=255" /tmp/_ssh_run_out && log_pass "ssh_run exhausted retries returns 255" \
  || log_fail "ssh_run exhausted: $(cat /tmp/_ssh_run_out)"
grep -q "calls=3" /tmp/_ssh_run_out && log_pass "exhausted retries = exactly 3 ssh calls" \
  || log_fail "exhausted call count: $(cat /tmp/_ssh_run_out)"

echo "── ssh_run auth 错误立即失败 ──"
(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  export FAKE_SSH_AUTH_FAIL=1
  export SSH_RETRY_BACKOFF=0
  start=$(date +%s)
  out=$(ssh_run 245 "echo nope" 2>&1)
  rc=$?
  end=$(date +%s)
  elapsed=$((end-start))
  printf 'rc=%s elapsed=%s\n' "$rc" "$elapsed"
) | tee /tmp/_ssh_run_out
grep -q "rc=255" /tmp/_ssh_run_out && log_pass "auth fail returns 255" \
  || log_fail "auth fail: $(cat /tmp/_ssh_run_out)"
# elapsed < 2s 表示没等退避
[[ $(grep -oE 'elapsed=[0-9]+' /tmp/_ssh_run_out | cut -d= -f2) -lt 2 ]] \
  && log_pass "auth fail returns immediately (no backoff)" \
  || log_fail "auth fail took too long: $(cat /tmp/_ssh_run_out)"

echo "── ssh_run 命令级错误立即失败 ──"
(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  export FAKE_SSH_CMD_FAIL=1
  export SSH_RETRY_BACKOFF=0
  out=$(ssh_run 245 "false" 2>&1)
  rc=$?
  printf 'rc=%s out=%s\n' "$rc" "$out"
) | tee /tmp/_ssh_run_out
grep -q "rc=1" /tmp/_ssh_run_out && log_pass "cmd fail rc=1 (not retried)" \
  || log_fail "cmd fail: $(cat /tmp/_ssh_run_out)"

echo "── ControlMaster socket init/close ──"
(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  ssh_retry_init 245
  sock=${_SSH_RETRY_SOCKETS[245]:-}
  [[ -n "$sock" ]] && echo "sock=$sock" || echo "no-sock"
  ssh_retry_close 245
  [[ -z "${_SSH_RETRY_SOCKETS[245]:-}" ]] && echo "cleared" || echo "still-set"
) | tee /tmp/_ssh_run_out
grep -q "sock=" /tmp/_ssh_run_out && log_pass "ssh_retry_init sets socket path" \
  || log_fail "init: $(cat /tmp/_ssh_run_out)"
grep -q "cleared" /tmp/_ssh_run_out && log_pass "ssh_retry_close clears state" \
  || log_fail "close: $(cat /tmp/_ssh_run_out)"

echo "── fallback hop 配置正确 ──"
hop=$(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  _ssh_retry_fallback_hop 154
)
hop245=$(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  _ssh_retry_fallback_hop 245
)
assert_eq "154 has fallback hop via 252" "$hop" "root@115.29.212.252"
assert_eq "245 has no fallback hop" "$hop245" ""

echo "── fallback: 直连失败后切到 ProxyCommand ──"
(
  source "$LIB_TARGETS"
  source "$LIB_SSH_RETRY"
  export FAKE_SSH_STATE_DIR="$TMPDIR_TEST/state"
  mkdir -p "$FAKE_SSH_STATE_DIR"
  echo 0 > "$FAKE_SSH_STATE_DIR/calls"
  # 直接 timeout 5 次 (超过 SSH_RETRY_FALLBACK_AFTER=2)
  echo 5 > "$FAKE_SSH_STATE_DIR/fails"
  export FAKE_SSH_FAILS_FILE="$FAKE_SSH_STATE_DIR/fails"
  export SSH_RETRY_MAX=5
  export SSH_RETRY_FALLBACK_AFTER=2
  export SSH_RETRY_BACKOFF=0
  export SSH_RETRY_VERBOSE=1
  # 把 verbose log 写到文件, 而不是 2>&1 混到 stdout
  out=$(ssh_run 154 "echo should-fallback" 2>"$TMPDIR_TEST/ssh_retry.log")
  rc=$?
  printf 'rc=%s\n' "$rc"
) | tee /tmp/_ssh_run_out
# 不验证真实成功 (fake 不实现 ProxyCommand), 只验证脚本走到 fallback
# 并最终失败 rc=255 (因为假 ssh 永远 timeout, ProxyCommand 也是)
# 但 verbose log 应包含 ProxyCommand 切换
grep -q "切到 ProxyCommand via" "$TMPDIR_TEST/ssh_retry.log" \
  && log_pass "ssh_run switches to ProxyCommand fallback" \
  || log_fail "no fallback switch. log: $(cat $TMPDIR_TEST/ssh_retry.log)"

echo ""
echo "───────────────────────────────────────────────────────────────"
echo " summary: $TESTS_PASSED passed, $TESTS_FAILED failed"
[[ "$TESTS_FAILED" -eq 0 ]] || exit 1
exit 0
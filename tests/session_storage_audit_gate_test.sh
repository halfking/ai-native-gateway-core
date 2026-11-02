#!/usr/bin/env bash
# Exercise release-gate decisions without starting Docker or running Go.
#
# ★ 2026-11-02 第二十四轮：本文件原先用 `rg -qx -- '<pat>' <file>` 做断言
# ⇒ **依赖 ripgrep**。ripgrep 是**开发机工具**，CI 精简镜像（codeup Flow 只装 Go）
#   里**没有** ⇒ 这条门在任何 CI 上都必然红 ⇒ 它从来不可能被接线 ⇒
#   **它从提交那天起就没跑过**（⇒ 它的正确性从未被任何执行验证过）。
# ⇒ 改成 POSIX 的 `grep -qx -e '<pat>' <file>`：
#   · `-e` 显式标记 pattern（BSD grep 不认 `grep -qx -- '--xxx'` ⇒ 会把
#     以 `-` 开头的 pattern 当成选项）；
#   · `grep` 在任何 Unix 上都有 ⇒ 门**终于可以在 CI 上跑**。
set -euo pipefail
cd "$(dirname "$0")/.."
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/repo/tests" "$scratch/repo/scripts/audit" "$scratch/bin" "$scratch/no-docker"
cp tests/session_storage_audit_gate.sh "$scratch/repo/tests/"
ln -s "$(command -v dirname)" "$scratch/no-docker/dirname"
cat > "$scratch/repo/scripts/audit/run-session-storage-stream.sh" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$GATE_ARGS"
exit "${GATE_RUNNER_RC:-0}"
SH
cat > "$scratch/bin/docker" <<'SH'
#!/usr/bin/env bash
case "$1" in
  info) exit "${GATE_DAEMON_RC:-0}" ;;
  image) exit "${GATE_IMAGE_RC:-0}" ;;
  *) exit 2 ;;
esac
SH
chmod +x "$scratch/bin/docker"
bash_path=$(command -v bash)
export GATE_ARGS="$scratch/args"
run_gate() {
  "$bash_path" "$scratch/repo/tests/session_storage_audit_gate.sh" "$@" > "$scratch/output" 2>&1
}
must_fail() {
  if run_gate "$@"; then
    echo "FAIL: required storage gate accepted unavailable dependencies or a failed runner" >&2
    return 1
  fi
}

# A successful release must never mean that this suite did not run.
original_path=$PATH
PATH="$scratch/no-docker" must_fail --require-coherence
PATH="$scratch/bin:$original_path" GATE_DAEMON_RC=1 must_fail --require-coherence
PATH="$scratch/bin:$original_path" GATE_IMAGE_RC=1 must_fail --require-coherence
PATH="$scratch/bin:$original_path" GATE_RUNNER_RC=17 must_fail

PATH="$scratch/bin:$original_path" run_gate
if ! grep -qx -e '--require-coherence' "$GATE_ARGS"; then
  echo 'FAIL: default release gate did not require coherence' >&2
  exit 1
fi
PATH="$scratch/bin:$original_path" run_gate --race
if ! grep -qx -e '--race' "$GATE_ARGS"; then
  echo 'FAIL: race option was not forwarded' >&2
  exit 1
fi
if ! grep -qx -e '--require-coherence' "$GATE_ARGS"; then
  echo 'FAIL: race mode disabled coherence' >&2
  exit 1
fi
echo 'PASS: storage gate requires dependencies, coherence, and a successful runner'

#!/usr/bin/env bash
# R79 self-audit pin: build_frontend must never abort a deploy silently.
#
# Observed failure (2026-09-28, local deploy 2298/2299/2300 and the parallel
# session's 2301-2306): `web/` type-check errors made `npm run build` exit 2,
# but the call site was
#
#     (cd "$PROJECT_ROOT/web" && npm run build) >/dev/null
#
# under `set -euo pipefail`. Two compounding defects:
#
#   1. `>/dev/null` discarded every vue-tsc/vite diagnostic, so the operator
#      saw the deploy stop with no message at all.
#   2. Under `set -e` the non-zero exit unwound straight to the EXIT trap, so
#      the last thing printed was whatever ran *before* the build
#      (dl_cleanup_legacy_downloads) — the log pointed at an unrelated step.
#
# Net effect: three deploys in a row burned a build_seq each (2298→2299→2300)
# with no release ever produced, and the only way to diagnose it was to
# re-run `npm run build` by hand. A deploy pipeline must fail *loudly*.
#
# This test drives the shipped function (extracted from deploy-local.sh, not
# reimplemented) against a fake `npm` that fails, and asserts the failure is
# both fatal and self-explaining.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/deploy-local.sh"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

bash -n "$SCRIPT" || fail 'deploy-local.sh must stay syntactically valid'

TMP="$(mktemp -d -t kx-frontend-contract.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

# --- extract the real function body from the shipped script -----------------
awk '/^build_frontend\(\) \{/,/^\}/' "$SCRIPT" >"$TMP/fn.sh"
[[ -s "$TMP/fn.sh" ]] || fail 'could not extract build_frontend() from deploy-local.sh'
grep -q 'npm run build' "$TMP/fn.sh" || fail 'extracted build_frontend no longer calls npm run build'

# --- sandbox that mimics a failing frontend build ---------------------------
mkdir -p "$TMP/project/web/node_modules/.bin" "$TMP/run" "$TMP/bin"
touch "$TMP/project/web/package.json"
# The guard is `[[ -x node_modules/.bin/vite ]]` — a non-executable placeholder
# takes the "node_modules is missing" warn branch and never reaches npm at all,
# so the fixture MUST be executable or this test would pass without exercising
# the code path it is pinning.
cat >"$TMP/project/web/node_modules/.bin/vite" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$TMP/project/web/node_modules/.bin/vite"
cat >"$TMP/bin/npm" <<'EOF'
#!/usr/bin/env bash
echo "vue-tsc: src/composables/useLiveStreamUrl.test.ts(160,63): error TS2322"
echo "npm ERR! code ELIFECYCLE"
exit 2
EOF
chmod +x "$TMP/bin/npm"

run_build_frontend() {
  PATH="$TMP/bin:$PATH" \
  PROJECT_ROOT="$TMP/project" \
  RUN_DIR="$TMP/run" \
  SKIP_FRONTEND=0 \
  timeout 30 bash -c '
    set -euo pipefail
    need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
    warn() { printf "WARN %s\n" "$*" >&2; }
    die()  { printf "ERROR %s\n" "$*" >&2; exit 1; }
    source '"$TMP"'/fn.sh
    build_frontend
  ' 2>&1
}

out="$(run_build_frontend)" && rc=0 || rc=$?

# 1. Must not report success when the frontend build failed.
[[ "$rc" -ne 0 ]] || fail 'build_frontend exited 0 after npm run build failed — deploy would continue on a stale web/dist'

# 2. The underlying compiler diagnostic must reach the operator. This is the
#    defect being pinned: before the fix `>/dev/null` ate it.
grep -q 'TS2322' <<<"$out" || fail "frontend build failure surfaced no compiler diagnostic; got: $out"

# 3. The failure must name an artifact the operator can open.
log_path="$(grep -oE '[A-Za-z0-9_./-]*build-frontend[^ ]*\.log' <<<"$out" | head -n1 || true)"
[[ -n "$log_path" ]] || fail "failure did not name a build-frontend log file; got: $out"
[[ -s "$log_path" ]] || fail "named log file is missing or empty: $log_path"
grep -q 'TS2322' "$log_path" || fail "log file does not contain the compiler diagnostic: $log_path"
pass 'a failing frontend build aborts the deploy, echoes diagnostics and leaves a non-empty log'

# --- control: --no-frontend must remain a clean no-op ----------------------
out_skip="$(PATH="$TMP/bin:$PATH" PROJECT_ROOT="$TMP/project" RUN_DIR="$TMP/run" \
  SKIP_FRONTEND=1 timeout 30 bash -c '
    set -euo pipefail
    need_cmd() { command -v "$1" >/dev/null 2>&1 || exit 1; }
    warn() { printf "WARN %s\n" "$*" >&2; }
    die()  { printf "ERROR %s\n" "$*" >&2; exit 1; }
    source '"$TMP"'/fn.sh
    build_frontend
  ' 2>&1)" || { fail "--no-frontend path must stay a no-op; got: $out_skip"; }
grep -q 'TS2322' <<<"$out_skip" && fail '--no-frontend unexpectedly ran the frontend build'
pass '--no-frontend still short-circuits without building'

# --- 2026-10-04 统一入口轮：web-mobile 构建失败必须同样响亮 -----------------
# 场景：web 构建成功（stub 按 cwd 区分），web-mobile 构建失败。若 web-mobile
# 的 die 分支被移除/降级为 warn，本测试红——静默部署一个没有 /m 的版本等于
# 把手机用户扔回 PC 页面（入口 302 与 /m 挂载一起消失）。
mkdir -p "$TMP/project2/web/node_modules/.bin" "$TMP/project2/web-mobile/node_modules/.bin" "$TMP/run2" "$TMP/bin2"
touch "$TMP/project2/web/package.json" "$TMP/project2/web-mobile/package.json"
for d in web web-mobile; do
  cat >"$TMP/project2/$d/node_modules/.bin/vite" <<'VITEEOF'
#!/usr/bin/env bash
exit 0
VITEEOF
  chmod +x "$TMP/project2/$d/node_modules/.bin/vite"
done
cat >"$TMP/bin2/npm" <<'NPMEOF'
#!/usr/bin/env bash
# cwd 感知：web/ 成功，web-mobile/ 失败并输出可检索的伪诊断
case "$PWD" in
  *web-mobile*)
    echo "css-gate: 3 处 Media Queries Level 4 范围语法（必须为 0）"
    exit 2
    ;;
esac
exit 0
NPMEOF
chmod +x "$TMP/bin2/npm"
out2="$(PATH="$TMP/bin2:$PATH" PROJECT_ROOT="$TMP/project2" RUN_DIR="$TMP/run2" \
  SKIP_FRONTEND=0 timeout 30 bash -c '
    set -euo pipefail
    need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
    warn() { printf "WARN %s\n" "$*" >&2; }
    die()  { printf "ERROR %s\n" "$*" >&2; exit 1; }
    source '"$TMP"'/fn.sh
    build_frontend
  ' 2>&1)" && rc2=0 || rc2=$?
[[ "$rc2" -ne 0 ]] || fail 'build_frontend exited 0 after web-mobile build failed — deploy would ship a release without the mobile surface'
grep -q 'css-gate' <<<"$out2" || fail "web-mobile build failure surfaced no diagnostic; got: $out2"
mob_log="$(grep -oE '[A-Za-z0-9_./-]*build-frontend-mobile[^ ]*\.log' <<<"$out2" | head -n1 || true)"
[[ -n "$mob_log" ]] || fail "web-mobile failure did not name build-frontend-mobile log; got: $out2"
[[ -s "$mob_log" ]] || fail "named web-mobile log missing or empty: $mob_log"
grep -q 'css-gate' "$mob_log" || fail "web-mobile log lacks the diagnostic: $mob_log"
pass 'a failing web-mobile build aborts the deploy with its own named log'

printf 'OK: build_frontend contract (3 assertion groups)\n'


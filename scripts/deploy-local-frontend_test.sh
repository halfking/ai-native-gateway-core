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

# 仅取 _dl_timeout：stock macOS 没有 GNU `timeout`，裸写 `timeout 30 …` 会以
# 127 失败，本套测试因此在 macOS 上恒红（本文件不 source deploy-local-lib.sh，
# 拿不到该 helper，故在此外层先 source 一次）。lib 不定义 fail/pass/warn/die，
# 与本文件的同名 helper 无冲突。
# shellcheck disable=SC1090
source "$ROOT_DIR/scripts/deploy-local-lib.sh"
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
  _dl_timeout 30 bash -c '
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
  SKIP_FRONTEND=1 _dl_timeout 30 bash -c '
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
  SKIP_FRONTEND=0 _dl_timeout 30 bash -c '
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

# --- 2026-10-11 2538 轮事故收口：node_modules 缺失 + dist 缺失必须 refuse ----
# 旧实现两个分支都只 warn：干净 checkout（node_modules 与 dist 都没有）会把
# 仅含 version.json 的空 web/ staging 进发布包 → 网关静态探测回落源码模板
# → 浏览器白页。refuse 门只在「dist 也不在场」时拒绝；dist 在场时保留旧的
# 「保留既有 dist」行为（warn 放行），--no-frontend 的显式豁免不受影响。
mkdir -p "$TMP/project3/web" "$TMP/run3" "$TMP/bin3"
touch "$TMP/project3/web/package.json"
out3="$(PATH="$TMP/bin3:$PATH" PROJECT_ROOT="$TMP/project3" RUN_DIR="$TMP/run3" \
  SKIP_FRONTEND=0 _dl_timeout 30 bash -c '
    set -euo pipefail
    need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
    warn() { printf "WARN %s\n" "$*" >&2; }
    die()  { printf "ERROR %s\n" "$*" >&2; exit 1; }
    source '"$TMP"'/fn.sh
    build_frontend
  ' 2>&1)" && rc3=0 || rc3=$?
[[ "$rc3" -ne 0 ]] || fail 'build_frontend exited 0 with node_modules AND dist both missing — empty web/ would be staged (2538 white-page regression)'
grep -q 'refusing to stage an empty web/' <<<"$out3" || fail "refusal did not name the empty-web staging hazard; got: $out3"
pass 'missing node_modules + missing dist refuses to stage an empty web/'

# 对照组：dist 在场（含 index.html）时缺 node_modules 保持旧「保留既有 dist」放行。
mkdir -p "$TMP/project4/web/dist" "$TMP/run4"
touch "$TMP/project4/web/package.json" "$TMP/project4/web/dist/index.html"
out4="$(PATH="$TMP/bin3:$PATH" PROJECT_ROOT="$TMP/project4" RUN_DIR="$TMP/run4" \
  SKIP_FRONTEND=0 _dl_timeout 30 bash -c '
    set -euo pipefail
    need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
    warn() { printf "WARN %s\n" "$*" >&2; }
    die()  { printf "ERROR %s\n" "$*" >&2; exit 1; }
    source '"$TMP"'/fn.sh
    build_frontend
  ' 2>&1)" || fail "retaining an existing dist must stay a warn-and-proceed path; got: $out4"
grep -q 'retaining existing web/dist' <<<"$out4" || fail "existing-dist path lost its warn marker; got: $out4"
pass 'missing node_modules with an existing dist still retains it (warn, not refuse)'

# web-mobile 同一门：缺失且 dist 不在场 → refuse。
mkdir -p "$TMP/project5/web-mobile" "$TMP/run5"
touch "$TMP/project5/web-mobile/package.json"
out5="$(PATH="$TMP/bin3:$PATH" PROJECT_ROOT="$TMP/project5" RUN_DIR="$TMP/run5" \
  SKIP_FRONTEND=0 _dl_timeout 30 bash -c '
    set -euo pipefail
    need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
    warn() { printf "WARN %s\n" "$*" >&2; }
    die()  { printf "ERROR %s\n" "$*" >&2; exit 1; }
    source '"$TMP"'/fn.sh
    build_frontend
  ' 2>&1)" && rc5=0 || rc5=$?
[[ "$rc5" -ne 0 ]] || fail 'build_frontend exited 0 with web-mobile deps and dist both missing'
grep -q 'refusing to stage an empty web-mobile' <<<"$out5" || fail "web-mobile refusal message missing; got: $out5"
pass 'web-mobile empty-staging refusal mirrors the web gate'

printf 'OK: build_frontend contract (6 assertion groups)\n'


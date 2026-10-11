#!/usr/bin/env bash
# =====================================================================
# tests/deploy_local_reap_test.sh — 行为回归门：deploy-local.sh 的
# 孤儿网关回收（2026-10-11 2533 部署轮事故）。
#
# 事故：stop_instance 只发 TERM 不等待，host 模式 gateway 的 graceful
# shutdown hang 在退出路径时进程永久残留（实测 4 个跨版本孤儿以
# pool-closed 状态存活 12h+ 持续刷后台错误日志）；cutover 重试后旧 pid
# 被 pidfile 覆盖，再无人能收回。
#
# 修复：stop_instance TERM → 轮询等待(10s) → KILL 升级；部署成功收尾
# reap_stray_gateways 扫描本部署根 bin/ 下非 active 的 gateway 进程。
#
# 本测试零外部依赖（pgrep/ps/kill 系统自带，无 docker/网络），从
# deploy-local.sh 源文件 sed 提取目标函数段驱动真实行为——副本漂移会
# 被下面的静态断言先拦住。
#
# Invocation: bash tests/deploy_local_reap_test.sh
# Exit: 0 全过；非 0 失败数。
# =====================================================================
set -uo pipefail

ROOT_DIR_REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY_LOCAL="$ROOT_DIR_REPO/scripts/deploy-local.sh"

TESTS_FAILED=0
fail() { printf 'FAIL: %s\n' "$*" >&2; TESTS_FAILED=$((TESTS_FAILED+1)); }
pass() { printf 'PASS %s\n' "$1"; }

bash -n "$DEPLOY_LOCAL" || { fail "deploy-local.sh syntax"; exit 1; }

# ---- 静态断言：修复的关键结构必须在源文件里 ----------------------------
grep -q 'did not exit 10s after TERM; sending KILL' "$DEPLOY_LOCAL" \
  || fail "stop_instance 缺少 TERM→KILL 升级路径（孤儿回归）"
grep -q '^reap_stray_gateways() {' "$DEPLOY_LOCAL" \
  || fail "reap_stray_gateways 函数缺失"
grep -q 'reap_stray_gateways "$(cat "$(pid_file "$candidate_port")"' "$DEPLOY_LOCAL" \
  || fail "deploy 收尾未调用 reap_stray_gateways"
grep -q 'dl_wait_port_free "\$port" || true; fi' "$DEPLOY_LOCAL" \
  || fail "dl_wait_port_free 调用点漂移"
grep -q 'Z\*' "$DEPLOY_LOCAL" || fail "_dl_gw_alive 缺少 zombie 判定"

# ---- 从源文件提取函数段（测的是源码本体，不是副本） --------------------
extract_fn() { sed -n "/^$1() {/,/^}/p" "$DEPLOY_LOCAL"; }
HARNESS="$(mktemp -d)"
trap 'rm -rf "$HARNESS"' EXIT
{
  for fn in instance_name pid_file _dl_gw_alive stop_instance reap_stray_gateways; do
    extract_fn "$fn" || { fail "extract $fn"; exit 1; }
    extract_fn "$fn"
    echo
  done
} > "$HARNESS/functions.sh"

# 每个 case 独立 bash -c：set -e 互不污染。eval "$2" 让传入命令走完整的
# 解析管线（含 quote removal）——裸 $2 展开不删引号，'reap_stray_gateways ""'
# 会把字面的两个双引号字符当非空 keep_pid，case4 的 no-op 断言因此假红。
run_case() { ~/sdk/bash5/bin/bash -c "set -euo pipefail; ROOT_DIR=\$1; RUN_DIR=\$1/run; DL_DOCKER=0; source '$HARNESS/functions.sh'; eval \"\$2\"" _ "$HARNESS" "$1"; }
if [[ ! -x ~/sdk/bash5/bin/bash ]]; then
  run_case() { bash -c "set -uo pipefail; ROOT_DIR=\$1; RUN_DIR=\$1/run; DL_DOCKER=0; source '$HARNESS/functions.sh'; eval \"\$2\"" _ "$HARNESS" "$1"; }
fi

# ---- case 1：TERM 即退的进程 → 收尸、删 pidfile、不触发 KILL ----------
mkdir -p "$HARNESS/run"
sleep 30 & c1_pid=$!
echo "$c1_pid" > "$HARNESS/run/gateway-8781.pid"
out1=$(run_case 'stop_instance 8781' 2>&1); rc1=$?
if [[ $rc1 -ne 0 ]]; then fail "case1 stop_instance rc=$rc1: $out1"; fi
if run_case "_dl_gw_alive $c1_pid" >/dev/null 2>&1; then fail "case1 进程未被回收"; fi
[[ -f "$HARNESS/run/gateway-8781.pid" ]] && fail "case1 pidfile 未删除"
grep -q 'KILL' <<<"$out1" && fail "case1 正常退出不应触发 KILL: $out1"
pass "case1 TERM 即退"

# ---- case 2：TERM 被忽略 → 10s 后 KILL 升级 ---------------------------
bash -c 'trap "" TERM; sleep 30' & c2_pid=$!
echo "$c2_pid" > "$HARNESS/run/gateway-8782.pid"
start=$(date +%s)
out2=$(run_case 'stop_instance 8782' 2>&1); rc2=$?
elapsed2=$(( $(date +%s) - start ))
if [[ $rc2 -ne 0 ]]; then fail "case2 stop_instance rc=$rc2: $out2"; fi
if run_case "_dl_gw_alive $c2_pid" >/dev/null 2>&1; then fail "case2 KILL 后进程仍存活"; fi
if (( elapsed2 < 8 )); then fail "case2 未等待 TERM 宽限期就 KILL（${elapsed2}s）: $out2"; fi
if (( elapsed2 > 25 )); then fail "case2 等待超预算（${elapsed2}s）"; fi
grep -q 'sending KILL' <<<"$out2" || fail "case2 未输出 KILL 升级告警: $out2"
pass "case2 TERM→KILL 升级（${elapsed2}s）"

# ---- case 3：reap_stray_gateways 杀非 active、保留 keep ----------------
mkdir -p "$HARNESS/bin/9.9.9"
# exec -a 伪装 argv[0] 成部署根 bin/ 下的 gateway，pgrep -f 按整条 cmdline 匹配
bash -c 'exec -a "$0" sleep 30' "$HARNESS/bin/9.9.9/gateway" & stray_pid=$!
sleep 0.5
sleep 30 & keeper_pid=$!
echo "$keeper_pid" > "$HARNESS/run/gateway-8782.pid"
start3=$(date +%s)
out3=$(run_case "reap_stray_gateways $keeper_pid" 2>&1); rc3=$?
elapsed3=$(( $(date +%s) - start3 ))
if [[ $rc3 -ne 0 ]]; then fail "case3 reap rc=$rc3: $out3"; fi
if run_case "_dl_gw_alive $stray_pid" >/dev/null 2>&1; then fail "case3 stray 未被回收"; fi
# TERM 快路径：sleep 30 收 TERM 即退，正常 <2s；若 TERM 发送被破坏，
# 会拖到 10s KILL 兜底才死——用时间断言把它单独判出来（变异验证：
# kill -TERM no-op 后本断言红）。
if (( elapsed3 >= 8 )); then fail "case3 未走 TERM 快路径（${elapsed3}s，疑似退化为 KILL 兜底）"; fi
if ! run_case "_dl_gw_alive $keeper_pid" >/dev/null 2>&1; then fail "case3 keeper 被误杀"; fi
grep -q 'stray gateway pid' <<<"$out3" || fail "case3 未输出 stray 告警: $out3"
pass "case3 reap 杀 stray 保 keeper"

# ---- case 4：keep_pid 为空时 reap 是 no-op（不误伤） -------------------
bash -c 'exec -a "$0" sleep 30' "$HARNESS/bin/9.9.8/gateway" & stray2_pid=$!
sleep 0.5
out4=$(run_case 'reap_stray_gateways ""' 2>&1); rc4=$?
if [[ $rc4 -ne 0 ]]; then fail "case4 rc=$rc4: $out4"; fi
if ! run_case "_dl_gw_alive $stray2_pid" >/dev/null 2>&1; then fail "case4 空 keep 不应触发清扫"; fi
pass "case4 空 keep no-op"

kill "$stray2_pid" 2>/dev/null || true

if (( TESTS_FAILED > 0 )); then printf '%d failure(s)\n' "$TESTS_FAILED" >&2; exit "$TESTS_FAILED"; fi
echo 'deploy-local orphan-reap test: PASS'

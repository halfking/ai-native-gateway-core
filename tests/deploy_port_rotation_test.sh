#!/usr/bin/env bash
# =====================================================================
# tests/deploy_port_rotation_test.sh — 2026-09-19 部署工单三项修复的行为锁定
#
#   1. bump-version.sh 每次编译（调用）build_seq 都 +1 —— 同代码重复部署
#      不再沿用旧 seq（否则撞 upload_release 的"拒绝覆盖活跃 release"）。
#   2. deploy-seamless.sh detect_active_side：以目标机 ss 实测监听决定 active
#      端口，候选严格在 8781/8782 契约对内轮换；双监听时用 nginx upstream
#      仲裁，仲裁不了 fail-closed；全盲信 run/active-port 的旧行为已废除。
#   3. deploy-local-lib.sh dl_resolve_active_port：本机 /healthz+TCP 实测仅做
#      诊断告警；active 端口永远以文件/env 链为准（本地无代理拓扑里它是对外
#      契约端口，2026-09-20 修复"单一监听实测优先"导致的 8781/8782 交替漂移）；
#      自定义端口透传。
#
# offline-only：remote_ssh / target_field / dl_* 全部 stub，不触网。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PASS=0; FAIL=0
ok()   { printf 'PASS %s\n' "$1"; ((PASS++)); }
bad()  { printf 'FAIL %s\n' "$1" >&2; ((FAIL++)); }

# ── 1. bump-version 每次调用 +1 ──────────────────────────────────
# --dry-run 不写文件；断言 target seq == current seq + 1（SAME_CODE 保序
# 逻辑删除后，同 sha+同日期也必须 +1）。
bump_out="$(bash "$REPO_ROOT/scripts/bump-version.sh" --dry-run 2>&1)"
cur="$(printf '%s\n' "$bump_out" | sed -n 's/.*current: seq=\([0-9]*\).*/\1/p')"
new="$(printf '%s\n' "$bump_out" | sed -n 's/.*target:  seq=\([0-9]*\).*/\1/p')"
if [[ "$cur" =~ ^[0-9]+$ && "$new" =~ ^[0-9]+$ && $((new - cur)) -eq 1 ]]; then
  ok "bump-version: 每次调用 seq +1 (cur=$cur new=$new)"
else
  bad "bump-version: 期望 seq +1，得到 cur='$cur' new='$new'"
fi
if printf '%s\n' "$bump_out" | grep -q 'seq 保持不变'; then
  bad "bump-version: SAME_CODE 保序提示仍在输出中"
else
  ok "bump-version: 同代码保序(SAME_CODE)逻辑已移除"
fi

# ── 2. detect_active_side（从 deploy-seamless.sh 提取函数体单测）──
extract_fn="$(sed -n '/^detect_active_side() {/,/^}$/p' "$REPO_ROOT/scripts/deploy-seamless.sh")"
if [[ -z "$extract_fn" ]]; then
  bad "detect_active_side: 函数提取失败（deploy-seamless.sh 结构变了？）"
fi

run_detect() {
  # $1 = ss fixture 输出; $2 = ps unit 输出; $3 = 远端 sed 从 upstream fragment
  # 提取出的端口（空 = fragment 丢失/无法仲裁）。stub 模拟的是各远端命令的
  # 完整输出，不是中间文件内容。
  (
    eval "$extract_fn"
    remote_ssh() {
      case "$1" in
        *"ss -ltnp"*) printf '%s\n' "$SS_FIXTURE" ;;
        *"ps -o unit="*) printf '%s\n' "$PS_FIXTURE" ;;
        *"sed -n "*) printf '%s\n' "$UPSTREAM_FIXTURE" ;;
        *) true ;;
      esac
    }
    target_field() { case "$2" in active_port) printf '8781';; candidate_port) printf '8782';; upstream_fragment) printf '/opt/llm-gateway-go/run/active-upstream.conf';; *) printf '';; esac; }
    warn() { :; }
    err()  { :; }
    TARGET=245
    SS_FIXTURE="$1" PS_FIXTURE="$2" UPSTREAM_FIXTURE="$3" detect_active_side
    local rc=$?
    printf 'PORT=%s\nUNIT=%s\nSTALE=%s\n' "$DETECTED_ACTIVE_PORT" "$DETECTED_ACTIVE_UNIT" "$DETECTED_STALE_PORT"
    exit "$rc"
  )
}

SS_ONLY_8781='LISTEN 0 128 *:8781 *:* users:(("gateway",pid=111,fd=8))'
SS_ONLY_8782='LISTEN 0 128 *:8782 *:* users:(("gateway",pid=222,fd=8))'
SS_BOTH="$SS_ONLY_8781
$SS_ONLY_8782"

out="$(run_detect "$SS_ONLY_8781" 'llmgo-245.service' '')"
if [[ "$(sed -n 's/^PORT=//p' <<<"$out")" == 8781 && "$(sed -n 's/^UNIT=//p' <<<"$out")" == llmgo-245.service && -z "$(sed -n 's/^STALE=//p' <<<"$out")" ]]; then
  ok "detect: 仅 8781 监听 → active=8781, unit 取 ps 归属"
else bad "detect: 仅 8781 场景输出异常: $out"; fi

out="$(run_detect "$SS_ONLY_8782" 'llmgo-245-canary@8782.service' '')"
if [[ "$(sed -n 's/^PORT=//p' <<<"$out")" == 8782 && "$(sed -n 's/^UNIT=//p' <<<"$out")" == 'llmgo-245-canary@8782.service' ]]; then
  ok "detect: 仅 8782 监听 → active=8782（上一轮蓝绿后正常态）"
else bad "detect: 仅 8782 场景输出异常: $out"; fi

out="$(run_detect "$SS_BOTH" 'llmgo-245.service' '8782')"
if [[ "$(sed -n 's/^PORT=//p' <<<"$out")" == 8782 && "$(sed -n 's/^STALE=//p' <<<"$out")" == 8781 ]]; then
  ok "detect: 双监听 → nginx upstream 仲裁 active=8782, stale=8781 待清理"
else bad "detect: 双监听仲裁场景输出异常: $out"; fi

out="$(run_detect "$SS_BOTH" 'llmgo-245.service' '')"
rc=$?
if [[ $rc -ne 0 ]]; then
  ok "detect: 双监听且 upstream 无法仲裁 → fail-closed (rc=$rc)"
else bad "detect: 双监听仲裁失败场景应返回非零，实际 rc=0 out=$out"; fi

out="$(run_detect '' '' '')"
if [[ "$(sed -n 's/^PORT=//p' <<<"$out")" == 8781 ]]; then
  ok "detect: 无监听 → fresh 主机, active 取契约 8781"
else bad "detect: 无监听场景输出异常: $out"; fi

# ── 3. dl_resolve_active_port（文件/env 链为准，实测仅告警）──────────
# 2026-09-20 修订：本地无代理拓扑里 active 端口就是客户端直连的对外契约端口。
# 旧语义"恰好一个端口在监听 → 实测优先"会让失败部署残留在另一侧的候选把
# 下一次部署整体劫持过去（对外端口静默漂移），必须锁死为：declared 永远赢。
lib_out="$(bash -c '
  set -uo pipefail
  source "'"$REPO_ROOT"'/scripts/deploy-local-lib.sh"
  dl_active_port() { printf "%s\n" "$STUB_DECLARED"; }
  dl_detect_active_port() { printf "%s\n" "$STUB_PROBED"; }
  for tc in "8782|8781" "8781|8782" "8781|8781" "8782|" "8782|8781
8782" "8790|"; do
    STUB_DECLARED="${tc%%|*}"; STUB_PROBED="${tc#*|}"
    printf "%s\n" "$(dl_resolve_active_port)"
  done
')"
expectations=('8782' '8781' '8781' '8782' '8782' '8790')
i=0
descs=('文件 8782 + 残留 8781 → 仍部署 8782（对外端口不可漂移，2026-09-20 主修复）'
       '文件 8781 + 残留 8782 → 仍部署 8781（对称场景）'
       '文件与实测一致 8781'
       '无监听 → 保留链式结果'
       '双监听 → 保留链式结果(另一侧由 start_instance 清理)'
       '自定义端口 8790 → 透传不复核')
while IFS= read -r got; do
  if [[ "$got" == "${expectations[$i]}" ]]; then ok "resolve: ${descs[$i]}"; else bad "resolve: ${descs[$i]} 期望 ${expectations[$i]} 得到 $got"; fi
  i=$((i+1))
done <<< "$lib_out"

# 漂移场景必须大声告警（stderr），操作者能看到对外端口曾无人监听。
warn_out="$(bash -c '
  set -uo pipefail
  source "'"$REPO_ROOT"'/scripts/deploy-local-lib.sh"
  dl_active_port() { printf "8782\n"; }
  dl_detect_active_port() { printf "8781\n"; }
  dl_resolve_active_port 2>&1 >/dev/null | sed -n "1p"
')"
if printf '%s\n' "$warn_out" | grep -q "对外端口 8782 当前无人监听"; then
  ok "resolve: 漂移场景输出诊断告警（残留将被清理、端口不漂移）"
else
  bad "resolve: 漂移场景缺少诊断告警，得到: $warn_out"
fi

# 一致/无监听场景不应误告警"另一侧有残留"。
quiet_out="$(bash -c '
  set -uo pipefail
  source "'"$REPO_ROOT"'/scripts/deploy-local-lib.sh"
  dl_active_port() { printf "8782\n"; }
  dl_detect_active_port() { :; }
  dl_resolve_active_port 2>&1 >/dev/null | grep -c "残留" || true
')"
if [[ "$quiet_out" == 0 ]]; then
  ok "resolve: 无监听场景不误报残留"
else
  bad "resolve: 无监听场景误报残留告警 (count=$quiet_out)"
fi

printf '\n summary: %d passed, %d failed\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

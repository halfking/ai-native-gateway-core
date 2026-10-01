#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail=0
pass(){ printf 'PASS %s\n' "$1"; }
fail(){ printf 'FAIL %s\n' "$1" >&2; ((fail++)); }
require(){ local d=$1 p=$2 f=$3; grep -Eq "$p" "$f" && pass "$d" || fail "$d"; }

# 2026-09-10 audit R8 P1: scripts/deploy-lib is a symlink into the shared
# ai-native-tools deploy-lib SSOT. If it dangles, every grep below on that
# path would false-PASS via `! grep` semantics — assert resolvability first.
if [[ ! -f "$ROOT/scripts/deploy-lib/targets.sh" ]]; then
  fail "deploy-lib symlink resolves (target: $(readlink "$ROOT/scripts/deploy-lib" 2>/dev/null || echo '?'))"
  printf 'FAIL %s\n' "  fix: ln -sfn ../../../../ai-native-tools/deploy-lib $ROOT/scripts/deploy-lib" >&2
  exit 1
fi
pass 'deploy-lib symlink resolves'

require 'runtime role is declared' 'RuntimeRole.*LLM_GATEWAY_RUNTIME_ROLE' "$ROOT/config/config.go"
# 2026-09-09: contract evolved from the original "traffic-only disables ALL
# background" pin (74f502762). 16068c099 (2026-09-01) deliberately runs the MV
# refresher on traffic-only blue-green instances via Redis token-bucket leader
# election, and f56598b59 (2026-09-09) runs credential_recovery there too (P0
# on 245/154). Background WRITERS are now gated by bgDataPlaneOnly: traffic-only
# implies data-plane-only background mode instead of "no background at all".
require 'traffic-only implies data-plane-only bg mode' 'bgDataPlaneOnly := strings\.EqualFold\(cfg\.BGMode, "data-plane"\) \|\| cfg\.IsTrafficOnly\(\)' "$ROOT/cmd/gateway/main.go"
require 'data-plane mode gates background writer block' 'if !bgDataPlaneOnly' "$ROOT/cmd/gateway/main.go"
require 'version exposes runtime role' '"runtime_role"' "$ROOT/domains/streaming/handler.go"
require '154 has candidate port' 'candidate_port "8782"' "$ROOT/scripts/deploy-lib/targets.sh"
require '245 has candidate unit' 'llmgo-245-canary@.service' "$ROOT/scripts/deploy-lib/targets.sh"
require 'nginx includes dynamic fragment' 'include /opt/llm-gateway-go/run/active-upstream.conf' "$ROOT/deploy/llmgo-245.nginx.conf"
require 'seamless starts candidate before stop' 'systemctl start.*candidate_service' "$ROOT/scripts/deploy-seamless.sh"
require 'seamless stops old service after gates' 'systemctl stop.*active_service' "$ROOT/scripts/deploy-seamless.sh"
# 2026-10-01（245 build_seq 2373 部署失败现场）：当 active unit 解析不出
# 归属时，旧代码在 active_port==契约 active_port 的分支回落到 $SERVICE_NAME
# ——那是蓝绿改造前的遗留单 unit（llmgo-245.service / llm-gateway-go.service），
# 契约注释明写「弃用遗留 unit，勿使用」。drain 窗口内两端口瞬时都无监听即
# 命中该分支，实测真的启动了弃用 unit 并留下错乱拓扑。
# 有 canary 模板时必须用 canary@<active_port>；无蓝绿契约的目标保留旧路径。
require 'blue-green fallback uses canary at active port' 'if \[\[ -n "\$candidate_unit" \]\]; then' "$ROOT/scripts/deploy-seamless.sh"
require 'blue-green fallback derives canary unit from port' 'active_service="[^"]*candidate_unit[^"]*@[^"]*active_port[^"]*"' "$ROOT/scripts/deploy-seamless.sh"
# 方向检查：$SERVICE_NAME 只允许出现在「无 canary 模板」的 elif 分支里 ——
# 即它的紧邻上一行必须是 elif 条件本身，而不能是引用 candidate_unit 的分支。
# （第一版用 `grep -B2` 判，被**上一个分支**的 canary 赋值命中而假阳性。）
_block=$(awk '/if \[\[ -z "\$active_service"/,/^  fi$/' "$ROOT/scripts/deploy-seamless.sh")
_legacy_line=$(printf '%s\n' "$_block" | grep -n 'active_service="\$SERVICE_NAME"' | head -1 | cut -d: -f1)
if [[ -z "$_legacy_line" ]]; then
  fail 'legacy SERVICE_NAME fallback must still exist for non-blue-green targets'
else
  _prev=$(printf '%s\n' "$_block" | sed -n "$((_legacy_line - 1))p")
  if [[ "$_prev" == *"elif"* ]]; then
    pass 'legacy SERVICE_NAME fallback is gated behind the no-canary elif branch'
  else
    fail "legacy SERVICE_NAME fallback is NOT behind an elif branch; preceding line: '$_prev'"
  fi
fi
# Candidate pre-warm is isolated from the active release: the deployer stages
# slots/<port> before start, while the unit pins both binary and version.json
# to that slot. current is promoted only after the candidate has passed its
# probes, so a failed pre-warm cannot change the serving release identity.
require 'seamless stages slot before candidate start' 'REMOTE_ROOT/slots/\$candidate_port' "$ROOT/scripts/deploy-seamless.sh"
require 'seamless probes candidate version before promotion' 'candidate_version_url' "$ROOT/scripts/deploy-seamless.sh"
require 'seamless promotes current after candidate probes' 'ln -sfn .*releases/\$version.*REMOTE_ROOT/current' "$ROOT/scripts/deploy-seamless.sh"
require '154 canary pins immutable slot binary' 'slots/%i/llm-gateway-go' "$ROOT/deploy/llm-gateway-go-canary@.service"
require '154 canary pins immutable slot version' 'LLM_GATEWAY_VERSION_FILE=/opt/llm-gateway-go/slots/%i/version.json' "$ROOT/deploy/llm-gateway-go-canary@.service"
require '245 canary pins immutable slot binary' 'slots/%i/gateway' "$ROOT/deploy/llmgo-245-canary@.service"
require '245 canary pins immutable slot version' 'LLM_GATEWAY_VERSION_FILE=/opt/llm-gateway-go/slots/%i/version.json' "$ROOT/deploy/llmgo-245-canary@.service"
require 'canary unit restarts on failure' 'Restart=on-failure' "$ROOT/deploy/llm-gateway-go-canary@.service"
require 'version probe uses immutable staged identity' 'version_identity_matches.*expected_release_version.*expected_release_seq' "$ROOT/scripts/deploy-seamless.sh"

require 'installer backs up legacy unit' 'pre-blue-green-assets' "$ROOT/scripts/install-blue-green-assets.sh"
require 'seamless surfaces per-probe failure' 'remote_probe' "$ROOT/scripts/deploy-seamless.sh"
# 2026-08-31: pin the probe timeout default at 60s. env 154 takes 35-40s for
# the first post-upgrade candidate to finish schema ensures on 8+ areas
# (request_logs, quality_fix_mode, provider/credential soft-delete,
# applications, fp_slot_limit, concurrency_mode, credential_governor,
# routing recent_success_rate, unavailable_recover_at). The prior 30s
# default killed the candidate mid-schema-ensure and the deploy failed
# with a confusing "Connection refused" on /healthz. 60s leaves headroom
# for cold-start migrations without giving up the blast-radius bound.
# 2026-09-19: 60 -> 180s. Sticky-LB + taskprofile audit hook add a fresh
# candidate ensure chain (routing_overrides_audit, passive_probe_state,
# two promote-function repairs) that routinely exceeds 60s on the shared
# 252 PG while the canary is still walking schema ensure (245 工单现场:
# "Connection refused" 假超时). The >=60s floor remains the contract.
# 2026-09-20 (e5879c497): 180 -> 120s (default tightened back) and the
# second (retry) window bounded at PROBE_RETRY_TIMEOUT_SECS:-60.
# 2026-09-26 (owner decision ①): forward probe default 120 -> 600s, aligned
# with deploy-245.sh (d5eeb71eb) — 154/245 share the 252 PG and the
# cold-start ensure chain (90-150s) burned 245 twice under a tight window.
# The matching systemd budget is pinned by deploy_readiness_contract_test
# (TimeoutStartSec=700s on BOTH canary unit templates). The host_rollback
# fallback intentionally stays 120s: it probes a prewarmed old version and
# rollback is a failure-recovery path that must not wait ten minutes.
require 'seamless forward probe default 600s (aligned with 245)' 'PROBE_TIMEOUT_SECS:-600' "$ROOT/scripts/deploy-seamless.sh"
require 'seamless host_rollback probe fallback stays 120s (prewarmed candidate)' 'PROBE_TIMEOUT_SECS:-120' "$ROOT/scripts/deploy-seamless.sh"
require 'seamless second probe window bounded at 60s' 'PROBE_RETRY_TIMEOUT_SECS:-60' "$ROOT/scripts/deploy-seamless.sh"
# 2026-09-19（部署工单）: 蓝绿轮换必须以目标机实测监听为准，候选端口严格
# 在 8781/8782 契约对内轮换；127.0.0.1 探测只能出现在 remote_ssh 远端命令
# 串里（本脚本跑在部署机）。
require 'seamless probes actual listening port before rotation' 'detect_active_side' "$ROOT/scripts/deploy-seamless.sh"
require 'seamless port arbitration via nginx upstream' 'upstream fragment' "$ROOT/scripts/deploy-seamless.sh"
require 'local direct fallback is explicit' 'requires --proxy' "$ROOT/scripts/local-host-blue-green.sh"
require 'install does not start traffic' 'never starts a candidate' "$ROOT/scripts/install-blue-green-assets.sh"
# Crashed deploys can leave slots/<port> symlinks aimed at pruned releases;
# the prune pass must remove those while sparing the active/candidate slots.
require 'prune removes dangling slot symlinks' 'pruned dangling slot' "$ROOT/scripts/deploy-seamless.sh"
require 'prune spares active and candidate slots' 'active_slot=...cat ..REMOTE_ROOT/run/active-port' "$ROOT/scripts/deploy-seamless.sh"
require 'env file permissions converge to 0600' 'chmod 0600' "$ROOT/scripts/deploy-seamless.sh"
# 2026-10-01（第十八轮审计 P1）：rollback 方向与 deploy 方向（上方
# 4c0ed1f72 断言组）同权——蓝绿契约目标的 $SERVICE_NAME 是弃用遗留 unit，
# 回滚分支内不得再 systemctl start 它或把它写回 run/active-service；
# canary@<canonical_port> 是唯一合法回滚目标形态。canary 已在 canonical
# 端口上时，收尾也不得停掉刚拉起的 rollback_service、不得删刚重指的 slot。
_rb_block=$(awk '/^do_rollback\(\)/,/^  UPGRADE_BANNER_ACTIVE=1$/' "$ROOT/scripts/deploy-seamless.sh")
if printf '%s\n' "$_rb_block" | grep -q "systemctl start '\\\$SERVICE_NAME'"; then
  fail 'rollback must not systemctl start the deprecated $SERVICE_NAME unit'
else
  pass 'rollback starts rollback_service, not the deprecated $SERVICE_NAME unit'
fi
require 'rollback derives rollback_service from candidate_unit and canonical port' 'rollback_service="\$\{candidate_unit%@\.service\}@\$\{canonical_port\}\.service"' "$ROOT/scripts/deploy-seamless.sh"
require 'rollback starts the derived rollback_service' "systemctl start '\\\$rollback_service'" "$ROOT/scripts/deploy-seamless.sh"
require 'rollback records rollback_service in run/active-service' "printf '%s.n. '\\\$rollback_service' > '\\\$REMOTE_ROOT/run/active-service'" "$ROOT/scripts/deploy-seamless.sh"
require 'rollback stops prior unit only when it differs from rollback_service' '\[ -n .\$current_active_service. -a .\$current_active_service. != .\$rollback_service. \]' "$ROOT/scripts/deploy-seamless.sh"
require 'rollback removes prior slot only when port differs from canonical' '\[ .\$current_active_port. != .\$canonical_port. \]' "$ROOT/scripts/deploy-seamless.sh"
(( fail == 0 ))

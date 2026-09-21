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
# second (retry) window bounded at PROBE_RETRY_TIMEOUT_SECS:-60. Both
# defaults pinned here (R49 audit: the 180s assertion was left stale and
# kept this gate red at HEAD).
require 'seamless probe timeout >= 60s (now 120s)' 'PROBE_TIMEOUT_SECS:-120' "$ROOT/scripts/deploy-seamless.sh"
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
(( fail == 0 ))

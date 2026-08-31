#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail=0
pass(){ printf 'PASS %s\n' "$1"; }
fail(){ printf 'FAIL %s\n' "$1" >&2; ((fail++)); }
require(){ local d=$1 p=$2 f=$3; grep -Eq "$p" "$f" && pass "$d" || fail "$d"; }

require 'runtime role is declared' 'RuntimeRole.*LLM_GATEWAY_RUNTIME_ROLE' "$ROOT/config/config.go"
require 'traffic-only disables background block' 'dbConn != nil && dbConn.Enabled\(\) && !cfg.IsTrafficOnly\(\)' "$ROOT/cmd/gateway/main.go"
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
require 'seamless probe timeout >= 60s' 'PROBE_TIMEOUT_SECS:-60' "$ROOT/scripts/deploy-seamless.sh"
require 'local direct fallback is explicit' 'requires --proxy' "$ROOT/scripts/local-host-blue-green.sh"
require 'install does not start traffic' 'never starts a candidate' "$ROOT/scripts/install-blue-green-assets.sh"
(( fail == 0 ))

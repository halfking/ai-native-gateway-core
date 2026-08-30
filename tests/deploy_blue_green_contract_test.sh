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
require 'local direct fallback is explicit' 'requires --proxy' "$ROOT/scripts/local-host-blue-green.sh"
require 'install does not start traffic' 'never starts a candidate' "$ROOT/scripts/install-blue-green-assets.sh"
(( fail == 0 ))

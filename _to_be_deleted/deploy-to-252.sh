#!/bin/bash
# Frozen legacy entry point. 252 is a database/infrastructure node, not a
# llm-gateway-go runtime; gateway deployment uses the canonical 245 → 154 flow.

printf '%s\n' \
  'ERROR: 252 is a database/infrastructure node, not an llm-gateway-go runtime.' \
  'Gateway deployment targets are 245 (pre-production) and 154 (production), in that order.' \
  'This legacy 252 gateway deployer is frozen and refuses all mutating actions.' \
  'Use scripts/deploy.sh plan 252 to inspect the deferred target contract.' >&2
exit 64

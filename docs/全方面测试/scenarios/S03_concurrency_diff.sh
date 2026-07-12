#!/bin/bash
# S03: 并发能力差异化 — 高并发流量按 supplier 实力分配
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S03] concurrency differentiation: 100 clients"
run_loadtest S03_concurrency \
    --n-clients 100 --rps-per-client 6 --duration 60 \
    --models tok3 --prompt short
print_summary S03_concurrency

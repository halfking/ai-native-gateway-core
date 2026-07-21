# ============================================================================
# env-kaixuan1.sh — kaixuan-1 (Company) Configuration
#
# Usage: source configs/env-kaixuan1.sh
#
# Slice 7 credential cleanup: SSH_PASS, PG_PASS, and REGISTRY_PASS now
# reference env-injector variables. Before sourcing this file, run:
#   eval "$(env-injector inject --target=kaixuan-1)"
# or manually export SSH_PASS_KAIXUAN1, PG_PASS_KAIXUAN1, REGISTRY_PASS_KAIXUAN1.
# ============================================================================

# ── Target type (docker / direct / tunnel) ──────────────────────────────
TARGET_TYPE="direct"

# ── SSH ────────────────────────────────────────────────────────────────────
SSH_HOST="192.168.31.28"
SSH_PORT="22"
SSH_USER="kaixuan"
SSH_PASS="${SSH_PASS_KAIXUAN1:?SSH_PASS_KAIXUAN1 not set — run env-injector inject --target=kaixuan-1}"

# ── Docker ─────────────────────────────────────────────────────────────────
DOCKER_HOST="local"              # k3s, no docker
DOCKER_PG_CONTAINER=""           # k3s pod, not a docker container

# ── PostgreSQL (k3s) ──────────────────────────────────────────────────────
PG_HOST="192.168.31.8"          # k3s server (Tart VM)
PG_PORT="30432"                  # k3s NodePort
PG_USER="llm_gateway"
PG_PASS="${PG_PASS_KAIXUAN1:?PG_PASS_KAIXUAN1 not set — run env-injector inject --target=kaixuan-1}"
PG_DB="llm_gateway"

# External access via nps tunnel
PG_EXTERNAL_HOST="pg-dev.itestu.cn"
PG_EXTERNAL_PORT="5432"          # nps forwarded port (TBD)

# Host-mode fallback when tart-vm / k3s PG (192.168.31.8:30432) is unreachable.
# 252 socat pg-tunnel-11033.service forwards 11033 -> pg-252-pg17:5432.
PG_FALLBACK_HOST="115.29.212.252"
PG_FALLBACK_PORT="11033"

# ── k3s Cluster ───────────────────────────────────────────────────────────
K3S_SERVER="192.168.31.8"       # control-plane, master
K3S_AGENT_2="192.168.31.9"     # worker (kaixuan-2)
K3S_AGENT_3="192.168.31.10"    # worker (kaixuan-3)
K3S_VERSION="v1.30.4"

# ── Services ──────────────────────────────────────────────────────────────
LLM_GATEWAY_URL="https://llm.itestu.cn"
REGISTRY_URL="http://192.168.31.8:5000"
REGISTRY_USER="kaixuan"
REGISTRY_PASS="${REGISTRY_PASS_KAIXUAN1:?REGISTRY_PASS_KAIXUAN1 not set — run env-injector inject --target=kaixuan-1}"

# ── Image Info ─────────────────────────────────────────────────────────────
PG_IMAGE="PG 17 + pgvector + columnar"
PG_VERSION="17.x"

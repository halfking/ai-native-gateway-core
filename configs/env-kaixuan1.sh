# ============================================================================
# env-kaixuan1.sh — kaixuan-1 (Company) Configuration
#
# Usage: source configs/env-kaixuan1.sh
# ============================================================================

# ── SSH ────────────────────────────────────────────────────────────────────
SSH_HOST="192.168.31.28"
SSH_PORT="22"
SSH_USER="kaixuan"
SSH_PASS="kaixuan123"

# ── Docker ─────────────────────────────────────────────────────────────────
DOCKER_HOST="local"              # k3s, no docker
DOCKER_PG_CONTAINER=""           # k3s pod, not a docker container

# ── PostgreSQL (k3s) ──────────────────────────────────────────────────────
PG_HOST="192.168.31.8"          # k3s server (Tart VM)
PG_PORT="30432"                  # k3s NodePort
PG_USER="llm_gateway"
PG_PASS="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"
PG_DB="llm_gateway"

# External access via nps tunnel
PG_EXTERNAL_HOST="pg-dev.itestu.cn"
PG_EXTERNAL_PORT="5432"          # nps forwarded port (TBD)

# ── k3s Cluster ───────────────────────────────────────────────────────────
K3S_SERVER="192.168.31.8"       # control-plane, master
K3S_AGENT_2="192.168.31.9"     # worker (kaixuan-2)
K3S_AGENT_3="192.168.31.10"    # worker (kaixuan-3)
K3S_VERSION="v1.30.4"

# ── Services ──────────────────────────────────────────────────────────────
LLM_GATEWAY_URL="https://llm.itestu.cn"
REGISTRY_URL="http://192.168.31.8:5000"
REGISTRY_USER="kaixuan"
REGISTRY_PASS="Veritrans&9527"

# ── Image Info ─────────────────────────────────────────────────────────────
PG_IMAGE="PG 17 + pgvector + columnar"
PG_VERSION="17.x"

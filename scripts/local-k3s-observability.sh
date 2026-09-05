#!/usr/bin/env bash
# ============================================================================
# scripts/local-k3s-observability.sh — 本地 Docker 内 k3s 观察与管理系统
#
# 目标：用 k3d 在本机 Docker 中创建一个独立 k3s 集群，并安装 Kubernetes
# Dashboard + metrics-server，供本地观察、管理和验证使用。
#
# 默认行为只影响本机 Docker/k3d 资源，不触碰现有 llm-gateway 本地进程、PG、
# Redis，也不连接任何远端/生产 Kubernetes。
#
# 用法：
#   bash scripts/local-k3s-observability.sh up
#   bash scripts/local-k3s-observability.sh up --install-tools
#   bash scripts/local-k3s-observability.sh status
#   bash scripts/local-k3s-observability.sh token
#   bash scripts/local-k3s-observability.sh port-forward
#   bash scripts/local-k3s-observability.sh down
#
# 可配置环境变量：
#   K3S_OBS_CLUSTER_NAME       默认 llm-gateway-observe
#   K3S_OBS_KUBECONFIG         默认 ~/.kube/llm-gateway-observe.yaml
#   K3S_OBS_API_PORT           默认 6550
#   K3S_OBS_DASHBOARD_PORT     默认 10443
#   K3S_OBS_K3S_IMAGE          可选，例如 rancher/k3s:v1.31.5-k3s1
#   K3S_OBS_DASHBOARD_VERSION  默认 v2.7.0
#   K3S_OBS_DASHBOARD_ROLE     默认 view；如需管理权限显式设为 cluster-admin
#   K3S_OBS_TOKEN_DURATION     默认 2h
# ============================================================================
set -euo pipefail

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'
log()  { echo -e "${BLUE}[k3s-observe]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }

CLUSTER_NAME="${K3S_OBS_CLUSTER_NAME:-llm-gateway-observe}"
KUBECONFIG_PATH="${K3S_OBS_KUBECONFIG:-$HOME/.kube/${CLUSTER_NAME}.yaml}"
API_PORT="${K3S_OBS_API_PORT:-6550}"
DASHBOARD_PORT="${K3S_OBS_DASHBOARD_PORT:-10443}"
K3S_IMAGE="${K3S_OBS_K3S_IMAGE:-}"
DASHBOARD_VERSION="${K3S_OBS_DASHBOARD_VERSION:-v2.7.0}"
DASHBOARD_ROLE="${K3S_OBS_DASHBOARD_ROLE:-view}"
TOKEN_DURATION="${K3S_OBS_TOKEN_DURATION:-2h}"

ACTION="${1:-up}"
INSTALL_TOOLS="false"
if [[ "${2:-}" == "--install-tools" || "${1:-}" == "--install-tools" ]]; then
  INSTALL_TOOLS="true"
  [[ "$ACTION" == "--install-tools" ]] && ACTION="up"
fi

usage() {
  cat <<'USAGE'
本地 Docker 内 k3s 观察与管理系统

用法：
  bash scripts/local-k3s-observability.sh up
  bash scripts/local-k3s-observability.sh up --install-tools
  bash scripts/local-k3s-observability.sh status
  bash scripts/local-k3s-observability.sh token
  bash scripts/local-k3s-observability.sh port-forward
  bash scripts/local-k3s-observability.sh down

可配置环境变量：
  K3S_OBS_CLUSTER_NAME       默认 llm-gateway-observe
  K3S_OBS_KUBECONFIG         默认 ~/.kube/llm-gateway-observe.yaml
  K3S_OBS_API_PORT           默认 6550
  K3S_OBS_DASHBOARD_PORT     默认 10443
  K3S_OBS_K3S_IMAGE          可选，例如 rancher/k3s:v1.31.5-k3s1
  K3S_OBS_DASHBOARD_VERSION  默认 v2.7.0
  K3S_OBS_DASHBOARD_ROLE     默认 view；如需管理权限显式设为 cluster-admin
  K3S_OBS_TOKEN_DURATION     默认 2h
USAGE
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

install_tools_if_requested() {
  if command_exists k3d && command_exists kubectl; then
    return
  fi
  if [[ "$INSTALL_TOOLS" != "true" ]]; then
    err "缺少依赖：$(command_exists k3d || printf 'k3d ')$(command_exists kubectl || printf 'kubectl ')"
    echo "重跑：bash scripts/local-k3s-observability.sh up --install-tools"
    echo "或手动安装：brew install k3d kubectl"
    exit 1
  fi
  command_exists brew || { err "未安装 Homebrew，无法自动安装依赖"; exit 1; }
  log "安装本地依赖：k3d kubectl"
  brew install k3d kubectl
}

require_docker() {
  command_exists docker || { err "docker 未安装"; exit 1; }
  docker info >/dev/null 2>&1 || { err "Docker Engine 未运行"; exit 1; }
}

require_kubectl_context() {
  export KUBECONFIG="$KUBECONFIG_PATH"
  kubectl config use-context "k3d-$CLUSTER_NAME" >/dev/null
}

cluster_exists() {
  k3d cluster list --no-headers 2>/dev/null | awk '{print $1}' | grep -Fxq "$CLUSTER_NAME"
}

create_cluster() {
  mkdir -p "$(dirname "$KUBECONFIG_PATH")"
  if cluster_exists; then
    ok "k3d 集群已存在：$CLUSTER_NAME"
    k3d kubeconfig get "$CLUSTER_NAME" > "$KUBECONFIG_PATH"
    chmod 0600 "$KUBECONFIG_PATH"
    return
  fi

  log "创建 k3s 集群：$CLUSTER_NAME"
  local create_args=(
    cluster create "$CLUSTER_NAME"
    --servers 1
    --agents 1
    --api-port "127.0.0.1:${API_PORT}"
    --wait
  )
  if [[ -n "$K3S_IMAGE" ]]; then
    create_args+=(--image "$K3S_IMAGE")
  fi
  k3d "${create_args[@]}"
  k3d kubeconfig get "$CLUSTER_NAME" > "$KUBECONFIG_PATH"
  chmod 0600 "$KUBECONFIG_PATH"
  ok "kubeconfig 写入：$KUBECONFIG_PATH"
}

apply_metrics_server() {
  if kubectl -n kube-system get deployment metrics-server >/dev/null 2>&1; then
    ok "metrics-server 已存在，跳过远程 manifest 下载"
  else
    log "安装/更新 metrics-server"
    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
  fi
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' \
    >/dev/null 2>&1 || true
}

apply_dashboard() {
  if kubectl -n kubernetes-dashboard get deployment kubernetes-dashboard >/dev/null 2>&1; then
    ok "Kubernetes Dashboard 已存在，跳过远程 manifest 下载"
  else
    log "安装/更新 Kubernetes Dashboard ${DASHBOARD_VERSION}"
    kubectl apply -f "https://raw.githubusercontent.com/kubernetes/dashboard/${DASHBOARD_VERSION}/aio/deploy/recommended.yaml"
  fi
  case "$DASHBOARD_ROLE" in
    view|cluster-admin) ;;
    *) err "K3S_OBS_DASHBOARD_ROLE 只能是 view 或 cluster-admin"; exit 1 ;;
  esac
  local binding_name="llm-gateway-local-observer"
  local current_role=""
  current_role="$(kubectl get clusterrolebinding "$binding_name" -o jsonpath='{.roleRef.name}' 2>/dev/null || true)"
  if [[ -n "$current_role" && "$current_role" != "$DASHBOARD_ROLE" ]]; then
    warn "Dashboard ClusterRoleBinding roleRef 从 $current_role 切换为 $DASHBOARD_ROLE"
    kubectl delete clusterrolebinding "$binding_name"
  fi

  kubectl apply -f - <<YAML
apiVersion: v1
kind: ServiceAccount
metadata:
  name: llm-gateway-local-observer
  namespace: kubernetes-dashboard
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${binding_name}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${DASHBOARD_ROLE}
subjects:
  - kind: ServiceAccount
    name: llm-gateway-local-observer
    namespace: kubernetes-dashboard
YAML
  if [[ "$DASHBOARD_ROLE" == "cluster-admin" ]]; then
    warn "Dashboard token 将具备本地集群 cluster-admin 权限，仅限可信本机环境使用"
  else
    ok "Dashboard ServiceAccount 使用只读 view 权限"
  fi
}

wait_rollouts() {
  log "等待核心组件就绪"
  kubectl -n kube-system rollout status deployment/metrics-server --timeout=180s || warn "metrics-server 尚未完全就绪"
  kubectl -n kubernetes-dashboard rollout status deployment/kubernetes-dashboard --timeout=180s
}

print_token() {
  require_kubectl_context
  log "Dashboard 登录 token (${TOKEN_DURATION})"
  kubectl -n kubernetes-dashboard create token llm-gateway-local-observer --duration="$TOKEN_DURATION"
}

status() {
  require_docker
  install_tools_if_requested
  if ! cluster_exists; then
    warn "k3d 集群不存在：$CLUSTER_NAME"
    return
  fi
  require_kubectl_context
  echo "cluster:    $CLUSTER_NAME"
  echo "kubeconfig: $KUBECONFIG_PATH"
  echo "api:        https://127.0.0.1:${API_PORT}"
  echo "dashboard:  https://127.0.0.1:${DASHBOARD_PORT} (port-forward 后访问)"
  echo ""
  kubectl get nodes -o wide
  echo ""
  kubectl get pods -A
}

port_forward() {
  require_kubectl_context
  log "启动 Dashboard 端口转发：https://127.0.0.1:${DASHBOARD_PORT}"
  echo "另开终端获取 token：bash scripts/local-k3s-observability.sh token"
  kubectl -n kubernetes-dashboard port-forward svc/kubernetes-dashboard "${DASHBOARD_PORT}:443"
}

up() {
  require_docker
  install_tools_if_requested
  create_cluster
  require_kubectl_context
  apply_metrics_server
  apply_dashboard
  wait_rollouts
  echo ""
  ok "本地 k3s 观察与管理系统已就绪"
  echo "  kubeconfig: $KUBECONFIG_PATH"
  echo "  context:    k3d-$CLUSTER_NAME"
  echo "  权限:       Dashboard ServiceAccount = $DASHBOARD_ROLE"
  echo "  状态:       bash scripts/local-k3s-observability.sh status"
  echo "  token:      bash scripts/local-k3s-observability.sh token"
  echo "  Dashboard:  bash scripts/local-k3s-observability.sh port-forward"
  echo "              然后打开 https://127.0.0.1:${DASHBOARD_PORT}"
}

down() {
  require_docker
  install_tools_if_requested
  if ! cluster_exists; then
    ok "k3d 集群不存在，无需删除：$CLUSTER_NAME"
    return
  fi
  log "删除本地 k3d 集群：$CLUSTER_NAME"
  k3d cluster delete "$CLUSTER_NAME"
  ok "已删除。kubeconfig 文件保留：$KUBECONFIG_PATH"
}

case "$ACTION" in
  up) up ;;
  status) status ;;
  token) print_token ;;
  port-forward) port_forward ;;
  down) down ;;
  help|-h|--help) usage ;;
  *) err "未知动作：$ACTION"; usage; exit 1 ;;
esac
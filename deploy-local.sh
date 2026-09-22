#!/usr/bin/env bash
# deploy-local.sh — llm-gateway-go 本地部署唯一入口（v5 Phase 0 D4 标准化）
#
# 本脚本是标准入口薄封装，实际部署逻辑只存在于 scripts/deploy-local.sh
# （统一蓝绿部署实现，含 scripts/deploy-local-lib.sh 与契约测试
# tests/deploy_local_contract_test.sh）：
#   迁移检查（migrate_database）→ 构建 → 蓝绿部署 → /health 验证
#
# 参数：
#   （无参数）          部署（幂等：若 active 端口已有健康网关则直接报告并退出 0；
#                      端口跟随 run/active-port 链，默认 8782）
#   --dry-run           打印部署计划，不变更任何文件/容器/进程
#   --down              停止当前本机部署（等价 scripts/deploy-local.sh stop）
#   --status            查看部署状态
#   --verify            部署后验证
#   其余参数原样透传给 scripts/deploy-local.sh
#
# 共享资源铁律：
#   本脚本永不创建/停止/删除共享基础设施（llm-gateway-pg / nbjl-redis /
#   nbjl-mysql / shared-infra 网络）；依赖发现按 kind + 健康探测复用既有实例。
#   运行时若由容器托管（如 llm-gateway-local-8782，容器名跟 active 端口），
#   --down 不影响容器生命周期，容器归 docker/groups.yaml 登记治理。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIFIED="$SCRIPT_DIR/scripts/deploy-local.sh"

# 对外端口跟随 scripts/deploy-local.sh 的同一条解析链：
# run/active-port 文件 → LLM_GATEWAY_ACTIVE_PORT/SERVICE_PORT → 8782。
# 预检/容器名都不得硬编码 8782，否则操作者自定义端口后包装器永远走全量部署。
active_listen_port() {
  local root="${LLM_GATEWAY_ROOT:-$HOME/kaixuan/llm-gateway-go}" port
  if [[ -s "$root/run/active-port" ]]; then
    port="$(cat "$root/run/active-port" 2>/dev/null || true)"
  fi
  if [[ -z "${port:-}" ]]; then port="${LLM_GATEWAY_ACTIVE_PORT:-${SERVICE_PORT:-}}"; fi
  printf '%s\n' "${port:-8782}"
}
PORT="$(active_listen_port)"
HEALTH_URL="${LLM_GATEWAY_HEALTH_URL:-http://127.0.0.1:${PORT}/health}"
CONTAINER_NAME="llm-gateway-local-${PORT}"

# 2026-09-23 (deploy-e2e validation): 默认开启 PG pre-flight fail-closed。
# 本地容器启动时偶发 host.docker.internal 网络握手慢于 90s boot-retry 预算
# （Docker Desktop on macOS 冷启动场景），导致 dbConn=nil → /api/auth/token
# 返回 "database not configured"。开启后 dl_wait_pg_isready 超时会直接 die，
# 容器不会被拉进 database:null 死区。如需回退到 warn-only：export
# DL_PG_PREFLIGHT_REQUIRED=0 后再调本脚本。
export DL_PG_PREFLIGHT_REQUIRED="${DL_PG_PREFLIGHT_REQUIRED:-1}"

if [[ ! -x "$UNIFIED" ]]; then
  printf '[deploy-local] error: unified entry missing: %s\n' "$UNIFIED" >&2
  exit 1
fi

health_ok() {
  local code
  code="$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$HEALTH_URL" 2>/dev/null || true)"
  [[ "$code" == "200" || "$code" == "204" ]]
}

container_running() {
  docker inspect "$CONTAINER_NAME" >/dev/null 2>&1 \
    && [[ "$(docker inspect "$CONTAINER_NAME" --format '{{.State.Status}}')" == "running" ]]
}

case "${1:-deploy}" in
  --dry-run)
    shift
    exec bash "$UNIFIED" deploy --dry-run "$@"
    ;;
  --down)
    shift
    if health_ok && container_running; then
      printf '[deploy-local] %s is served by container %s (groups.yaml registered).\n' "$PORT" "$CONTAINER_NAME"
      printf '[deploy-local] --down does not manage container runtimes; stop it explicitly if intended:\n'
      printf '[deploy-local]   docker stop %s\n' "$CONTAINER_NAME"
      exit 0
    fi
    exec bash "$UNIFIED" stop "$@"
    ;;
  --status)
    shift
    exec bash "$UNIFIED" status "$@"
    ;;
  --verify)
    shift
    exec bash "$UNIFIED" verify "$@"
    ;;
  deploy)
    # 幂等预检：active 端口已有健康网关（容器或宿主进程）时不重复部署、
    # 不重启既有运行时。端口来自 active-port 解析链，与 scripts 侧一致。
    if health_ok; then
      runtime="host process"
      container_running && runtime="container $CONTAINER_NAME"
      printf '[deploy-local] gateway already healthy at %s (runtime: %s); nothing to do.\n' "$HEALTH_URL" "$runtime"
      printf '[deploy-local] to redeploy, free port %s first, then re-run this script.\n' "$PORT" >&2
      exit 0
    fi
    # `${1:-deploy}` 默认分支下 $1 可能不存在；无参 shift 在 set -e 下静默 exit 1。
    if [[ $# -gt 0 ]]; then
      shift
    fi
    exec bash "$UNIFIED" deploy "$@"
    ;;
  -h|--help|help)
    sed -n '2,26p' "$SCRIPT_DIR/deploy-local.sh" | sed 's/^# \{0,1\}//'
    printf '\n--- unified implementation usage ---\n'
    bash "$UNIFIED" --help || true
    exit 0
    ;;
  *)
    exec bash "$UNIFIED" "$@"
    ;;
esac

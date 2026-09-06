#!/usr/bin/env bash
# deploy-local.sh — llm-gateway-go 本地部署唯一入口（v5 Phase 0 D4 标准化）
#
# 本脚本是标准入口薄封装，实际部署逻辑只存在于 scripts/deploy-local.sh
# （统一蓝绿部署实现，含 scripts/deploy-local-lib.sh 与契约测试
# tests/deploy_local_contract_test.sh）：
#   迁移检查（migrate_database）→ 构建 → 蓝绿部署 → /health 验证
#
# 参数：
#   （无参数）          部署（幂等：若 127.0.0.1:8782 已有健康网关则直接报告并退出 0）
#   --dry-run           打印部署计划，不变更任何文件/容器/进程
#   --down              停止当前本机部署（等价 scripts/deploy-local.sh stop）
#   --status            查看部署状态
#   --verify            部署后验证
#   其余参数原样透传给 scripts/deploy-local.sh
#
# 共享资源铁律：
#   本脚本永不创建/停止/删除共享基础设施（llm-gateway-pg / nbjl-redis /
#   nbjl-mysql / shared-infra 网络）；依赖发现按 kind + 健康探测复用既有实例。
#   运行时若由容器托管（如 llm-gateway-local-8782），--down 不影响容器生命周期，
#   容器归 docker/groups.yaml 登记治理。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIFIED="$SCRIPT_DIR/scripts/deploy-local.sh"
HEALTH_URL="${LLM_GATEWAY_HEALTH_URL:-http://127.0.0.1:8782/health}"

if [[ ! -x "$UNIFIED" ]]; then
  printf '[deploy-local] error: unified entry missing: %s\n' "$UNIFIED" >&2
  exit 1
fi

health_ok() {
  local code
  code="$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$HEALTH_URL" 2>/dev/null || true)"
  [[ "$code" == "200" || "$code" == "204" ]]
}

case "${1:-deploy}" in
  --dry-run)
    shift
    exec bash "$UNIFIED" deploy --dry-run "$@"
    ;;
  --down)
    shift
    if health_ok && docker inspect llm-gateway-local-8782 >/dev/null 2>&1 \
       && [[ "$(docker inspect llm-gateway-local-8782 --format '{{.State.Status}}')" == "running" ]]; then
      printf '[deploy-local] 8782 is served by container llm-gateway-local-8782 (groups.yaml registered).\n'
      printf '[deploy-local] --down does not manage container runtimes; stop it explicitly if intended:\n'
      printf '[deploy-local]   docker stop llm-gateway-local-8782\n'
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
    # 幂等预检：8782 已有健康网关（容器或宿主进程）时不重复部署、不重启既有运行时。
    if health_ok; then
      runtime="host process"
      docker inspect llm-gateway-local-8782 >/dev/null 2>&1 \
        && [[ "$(docker inspect llm-gateway-local-8782 --format '{{.State.Status}}')" == "running" ]] \
        && runtime="container llm-gateway-local-8782"
      printf '[deploy-local] gateway already healthy at %s (runtime: %s); nothing to do.\n' "$HEALTH_URL" "$runtime"
      printf '[deploy-local] to redeploy, free port 8782 first, then re-run this script.\n'
      exit 0
    fi
    shift
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

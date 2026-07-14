#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-245.sh — 部署到 245 测试环境 (pre-production)
#
# 245 是 154 生产环境的预发布验证环节。
# 所有新版本应先部署到 245 验证通过后，再部署到 154。
#
# 服务器: root@8.136.114.245:25022
# 数据库: 252 上的 PG17 (172.16.2.210:5432)
# 日志: /var/log/llm-gateway-go/gateway.{stdout,stderr}.log
# 前端: /opt/llm-gateway-go/web/
# 配置: /opt/llm-gateway-go/.env (注意是 .env 不是 /etc/llm-gateway-go/env)
#
# 用法:
#   bash scripts/deploy-245.sh                    # 自动部署当前 HEAD
#   bash scripts/deploy-245.sh --no-frontend      # 跳过前端构建
#
# 前置:
#   - SSH 密钥已配置 (id_ed25519 或 sshpass)
#   - 252 PG schema 已更新到最新
# =====================================================================
set -euo pipefail

SSH_TARGET="${LLM_GATEWAY_245_SSH:-root@8.136.114.245}"
SSH_PORT="${LLM_GATEWAY_245_PORT:-25022}"
REMOTE_DIR="${LLM_GATEWAY_245_DIR:-/opt/llm-gateway-go}"
SERVICE_NAME="${LLM_GATEWAY_245_SERVICE:-llm-gateway-go.service}"
SKIP_FRONTEND=false
SSH_USE_KEY="${SSH_USE_KEY:-true}"

SSH_KEY_FILE="${SSH_KEY_245:-}"
if [[ -z "$SSH_KEY_FILE" ]]; then
  for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
    if [[ -f "$k" ]]; then SSH_KEY_FILE="$k"; break; fi
  done
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-frontend) SKIP_FRONTEND=true; shift ;;
    -h|--help)
      sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "deploy-245: 未知参数 $1" >&2; exit 1 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"
# shellcheck source=deploy-lib/post-deploy-verify.sh
source "$SCRIPT_DIR/deploy-lib/post-deploy-verify.sh"

GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; RED=$'\033[0;31m'; NC=$'\033[0m'
log()  { echo -e "${GREEN}[deploy-245]${NC} $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }
err()  { echo -e "${RED}[error]${NC} $*" >&2; }

if [[ "$SSH_USE_KEY" == "true" ]]; then
  if [[ -z "$SSH_KEY_FILE" ]] || [[ ! -f "$SSH_KEY_FILE" ]]; then
    err "未找到 SSH 私钥"
    exit 2
  fi
  SSH="ssh -i $SSH_KEY_FILE -p $SSH_PORT -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
  SCP="scp -i $SSH_KEY_FILE -P $SSH_PORT -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
else
  SSH="sshpass -e ssh -p $SSH_PORT -o StrictHostKeyChecking=accept-new"
  SCP="sshpass -e scp -P $SSH_PORT -o StrictHostKeyChecking=accept-new"
fi

# ── Step 1: 预检 ──
log "[1/5] 预检..."
[[ -f go.mod ]] || { err "go.mod 不存在"; exit 1; }
$SSH "$SSH_TARGET" "echo connected" >/dev/null || { err "SSH 不可达"; exit 1; }
deploy_preflight_pg_from_remote_env "$SSH" "$REMOTE_DIR/.env" || exit 2
log "  git clean + ssh OK + PG ✓"

# ── Step 2: 获取版本信息 ──
log "[2/5] 版本信息..."
HEAD_SHA=$(git rev-parse --short HEAD)
NEW_SEQ=$(python3 -c "import json; print(json.load(open('version.json')).get('build_seq','?'))" 2>/dev/null || echo "unknown")
NEW_VERSION=$(python3 -c "import json; print(json.load(open('version.json')).get('version','dev'))" 2>/dev/null || echo "dev")
HEAD_DATE=$(date +%Y%m%d)
BIN_NAME="gateway"
log "  sha=$HEAD_SHA seq=$NEW_SEQ version=$NEW_VERSION"

# ── Step 3: 前端构建 ──
if [[ "$SKIP_FRONTEND" == "false" ]]; then
  log "[3/5] 前端构建..."
  (cd web && npm run build 2>&1 | tail -5)
  log "  web/dist/ 已生成 ✓"
else
  log "[3/5] 跳过前端构建 (--no-frontend)"
fi

# ── Step 4: Go 交叉编译 ──
log "[4/5] Go 交叉编译 linux/amd64..."
# 2026-07-14: 移除 ldflags 版本注入。版本号统一从 version.json 文件读取（SSOT）。
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w" \
  -o /tmp/__deploy_245_binary ./cmd/gateway
log "  编译完成 ✓"

# ── Step 5: 上传 + 重启 + 验证 ──
log "[5/5] 上传 + 重启 + 验证..."

# 停服务
$SSH "$SSH_TARGET" "systemctl stop $SERVICE_NAME 2>/dev/null || true"

# 备份旧二进制 + 上传新二进制
$SSH "$SSH_TARGET" "mv $REMOTE_DIR/gateway $REMOTE_DIR/gateway.bak.$(date +%Y%m%d-%H%M%S) 2>/dev/null || true"
$SCP /tmp/__deploy_245_binary "$SSH_TARGET:$REMOTE_DIR/gateway"
$SSH "$SSH_TARGET" "chmod +x $REMOTE_DIR/gateway"

# 上传 version.json
$SCP version.json "$SSH_TARGET:$REMOTE_DIR/version.json"

# 上传前端 (展平到 web/ 目录)
if [[ "$SKIP_FRONTEND" == "false" ]] && [[ -d web/dist ]]; then
  $SSH "$SSH_TARGET" "rm -rf $REMOTE_DIR/web && mkdir -p $REMOTE_DIR/web"
  tar czf - -C web dist | $SSH "$SSH_TARGET" "cat | tar xzf - -C $REMOTE_DIR/web --strip-components=1"
fi

# 2026-07-14 fix: 确保 STATIC_DIR 指向 web/（展平模式），不是 web/dist
$SSH "$SSH_TARGET" "sed -i 's|LLM_GATEWAY_STATIC_DIR=.*dist.*|LLM_GATEWAY_STATIC_DIR=$REMOTE_DIR/web|' $REMOTE_DIR/.env 2>/dev/null || true"

# 2026-07-14: 确保 Redis 指向 252 数据面（与 154 一致）。
# 245 经内网 172.16.2.210:6389 可达；未配置时泳道维度数据无法写入 Redis。
# 用法: LLM_GATEWAY_REDIS_ADDR=172.16.2.210:6389 LLM_GATEWAY_REDIS_PASSWORD=... bash scripts/deploy-245.sh
if [[ -n "${LLM_GATEWAY_REDIS_ADDR:-}" ]]; then
  log "  注入 Redis 配置..."
  $SSH "$SSH_TARGET" \
    "REDIS_ADDR='${LLM_GATEWAY_REDIS_ADDR}' REDIS_PASSWORD='${LLM_GATEWAY_REDIS_PASSWORD:-}' REDIS_DB='${LLM_GATEWAY_REDIS_DB:-0}'" \
    bash -s <<'REMOTE_REDIS'
set -euo pipefail
ENV="/opt/llm-gateway-go/.env"
cp "$ENV" "$ENV.bak.redis.$(date +%Y%m%d-%H%M%S)"
python3 - <<'PY'
from pathlib import Path
import re, os
env = Path("/opt/llm-gateway-go/.env")
text = env.read_text()
text = re.sub(r"(?m)^#.*Redis.*\n", "", text)
text = re.sub(r"(?m)^# LLM_GATEWAY_REDIS_.*\n", "", text)
lines = [ln for ln in text.splitlines() if not ln.startswith("LLM_GATEWAY_REDIS_")]
if lines and lines[-1].strip():
    lines.append("")
lines.extend([
    "# Redis (252 data plane, shared with 154)",
    f"LLM_GATEWAY_REDIS_ADDR={os.environ['REDIS_ADDR']}",
    f"LLM_GATEWAY_REDIS_PASSWORD={os.environ['REDIS_PASSWORD']}",
    f"LLM_GATEWAY_REDIS_DB={os.environ.get('REDIS_DB', '0')}",
])
env.write_text("\n".join(lines) + "\n")
PY
REMOTE_REDIS
fi

# 启动
$SSH "$SSH_TARGET" "systemctl daemon-reload && systemctl start $SERVICE_NAME"

# 验证
IS_ACTIVE=$($SSH "$SSH_TARGET" "systemctl is-active $SERVICE_NAME")
if [[ "$IS_ACTIVE" != "active" ]]; then
  err "✗ 服务未启动: $IS_ACTIVE"
  $SSH "$SSH_TARGET" "journalctl -u $SERVICE_NAME -n 30 --no-pager"
  exit 1
fi
log "  ✓ 服务 active"

if ! deploy_verify_gateway_ready "$SSH" "$SERVICE_NAME" 8781 120; then
  err "✗ DB 未就绪 — 245 部署失败"
  err "回滚: mv $REMOTE_DIR/gateway.bak.* $REMOTE_DIR/gateway && systemctl restart $SERVICE_NAME"
  exit 1
fi
log "  ✓ DB 就绪 (background-tasks 非 503)"

# Version check
VERSION_RESP=$($SSH "$SSH_TARGET" "curl -fsS http://127.0.0.1:8781/api/system/version")
echo "  /api/system/version -> $VERSION_RESP"

rm -f /tmp/__deploy_245_binary
log "✅ 245 部署完成 (pre-production)"
echo ""
echo "验证步骤:"
echo "  1. curl http://8.136.114.245:8781/api/system/version"
echo "  2. 浏览器访问 http://8.136.114.245:8781"
echo "  3. 验证通过后部署到 154: bash scripts/deploy-154.sh"

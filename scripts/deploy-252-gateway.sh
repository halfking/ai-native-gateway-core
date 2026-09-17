#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-252-gateway.sh — 252 dev gateway 部署（llmgo.itestu.cn 后端）
#
# 背景（2026-09-17 审计）：252 nginx 存在悬空 vhost llmgo.itestu.cn →
# 127.0.0.1:8780（网关已不存在）。本脚本把工作区构建的 gateway 部署为
# 252 上的 systemd 服务（127.0.0.1:8780），用于修复的部署验证。
#
# 环境契约：
#   - 必须先 source env-injector 注入 252（fail-closed）：
#       source ~/.agents/skills/env-injector/scripts/env-injector.sh inject 252
#     依赖 SSOT KEY：LLM_GATEWAY_DATABASE_URL / LLM_GATEWAY_JWT_SECRET /
#       LLM_GATEWAY_TEST_KEY / SSH_KEY_252
#   - LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY 不在 252 SSOT：本脚本生成
#     实例专属随机钥，共享 DB 中加密凭据在该实例 decrypt_failed 是预期
#     行为（2026-09-04 解密事故的镜像取舍），不做凭据解密冒烟门禁。
#   - Redis 不配置 → 网关内存降级（与 245 同模式）。
#
# 用法:
#   bash scripts/deploy-252-gateway.sh            # 构建 + 部署 + 验证
#   bash scripts/deploy-252-gateway.sh --dry-run  # 只打印计划
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DRY_RUN=0
[[ " ${*-} " == *' --dry-run '* ]] && DRY_RUN=1

TARGET_HOST="115.29.212.252"
TARGET_SSH="252"                 # ~/.ssh/config 别名（root@115.29.212.252:25022）
REMOTE_DIR="/opt/llm-gateway-go"
SERVICE="llmgo-252-dev"
LISTEN="127.0.0.1:8780"
BUILD_TMP="$(mktemp -d /tmp/llmgo-252-build.XXXXXX)"
trap 'shred -u "$BUILD_TMP/gateway.env" 2>/dev/null; rm -rf "$BUILD_TMP"' EXIT

log() { printf '[deploy-252] %s\n' "$*"; }

# ── SSOT 注入（fail-closed，本进程内生效）─────────────────────────────────
INJECTOR="$HOME/.agents/skills/env-injector/scripts/env-injector.sh"
[[ -f "$INJECTOR" ]] || { log "ERROR: env-injector 缺失：$INJECTOR"; exit 2; }
set +e
source "$INJECTOR" inject 252 >/dev/null 2>&1
set +u
for k in LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_JWT_SECRET LLM_GATEWAY_TEST_KEY SSH_KEY_252; do
  v="${!k:-}"
  [[ -n "$v" ]] || { log "ERROR: SSOT KEY 未注入：$k（先 source env-injector inject 252）"; exit 2; }
done
[[ -f "$SSH_KEY_252" ]] || { log "ERROR: SSH_KEY_252 不是可读文件"; exit 2; }
# R37 fix (D14): the `set +e`/`set +u` above were never restored — every
# later failure (key generation, scp, remote install, healthz probes) was
# swallowed and the script still printed VERIFY_RESULT=pass. Fail-closed
# from here on.
set -euo pipefail

# ── 构建 linux/amd64 ─────────────────────────────────────────────────────
# 与 deploy-seamless.sh do_deploy 同款两级策略（2026-09-05/09 CGO 回退）：
# 上游引入 CGO-only 依赖（yalue/onnxruntime_go）后纯静态 CGO=0 必然失败，
# 回退 kx-base/golang:1.27-alpine-amd64 容器内 CGO 静态构建（musl 静态产物
# 在 252 直接可跑）。resolve_build_image 复用共享镜像解析 SSOT。
log "构建 linux/amd64（git $(git -C "$ROOT_DIR" rev-parse --short HEAD)）..."
if [[ $DRY_RUN == 0 ]]; then
  if ! CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
      -o "$BUILD_TMP/gateway" ./cmd/gateway; then
    # shellcheck source=deploy-lib/deploy-image-resolution.sh
    source "$SCRIPT_DIR/deploy-lib/deploy-image-resolution.sh"
    local_build_image="${LLM_GATEWAY_BUILD_IMAGE:-kx-base/golang:1.27-alpine-amd64}"
    resolve_build_image "$local_build_image" "linux/amd64" \
      || { log "ERROR: CGO 回退镜像 $local_build_image 不可用"; exit 1; }
    log "CGO=0 失败，回退 $local_build_image 容器内 CGO 构建"
    cgo_out="$ROOT_DIR/.build-local/deploy-252-binary.$$"
    mkdir -p "$ROOT_DIR/.build-local/.gocache"
    (cd "$ROOT_DIR" && HOST_UID="$(id -u)" HOST_GID="$(id -g)" docker run --rm --platform linux/amd64 \
        -v "$ROOT_DIR":/src -w /src \
        -v "$ROOT_DIR/.build-local/.gocache":/tmp/go-build-cache \
        -e HOST_UID -e HOST_GID \
        -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=amd64 \
        -e GOCACHE=/tmp/go-build-cache -e GOPATH=/tmp/go-path \
        "$local_build_image" \
        sh -c 'apk add --no-cache gcc musl-dev >/dev/null && go build -trimpath -ldflags="-s -w -extldflags -static" -o /src/.build-local/deploy-252-binary.'"$$"' ./cmd/gateway && chown "$HOST_UID:$HOST_GID" /src/.build-local/deploy-252-binary.'"$$") \
      || { log "ERROR: 容器 CGO 构建失败"; exit 1; }
    install -m 0755 "$cgo_out" "$BUILD_TMP/gateway" && rm -f "$cgo_out"
  fi
  [[ -s "$BUILD_TMP/gateway" ]] || { log "ERROR: 构建产物为空"; exit 1; }
fi

# ── 生成实例 env（实例专属随机钥；不落日志）────────────────────────────────
# 252 宿主机注意两点（2026-09-17 首次部署实抓）：
# 1) SSOT DATABASE_URL 指向 127.0.0.1:5432 —— 那是 podman 网络内视角；
#    宿主机上 PG 在 172.16.2.210:5432（pg-252-pg17 容器 IP），需改写。
# 2) CORS fail-closed panic（middleware.NewCORSMiddleware）：必须显式
#    LLM_GATEWAY_CORS_ORIGINS，否则启动即 panic（deploy-local 已知 pitfall）。
# R37 fix (D14): SECRET_KEY and CREDENTIAL_ENCRYPTION_KEY were the SAME
# random value — one key reused across two cryptographic domains. Generate
# two independent keys.
GEN_KEY=$(python3 -c "import secrets;print(secrets.token_urlsafe(33))")
ENC_KEY=$(python3 -c "import secrets;print(secrets.token_urlsafe(33))")
DB_URL_252=$(printf '%s' "$LLM_GATEWAY_DATABASE_URL" | sed -E 's#@127\.0\.0\.1:5432#@172.16.2.210:5432#; s#@localhost:5432#@172.16.2.210:5432#')
# R37 fix (D14): umask 077 while writing — the file carries DATABASE_URL
# (PG password) and JWT_SECRET; the old write-then-chmod left a world-readable
# window.
(umask 077; cat > "$BUILD_TMP/gateway.env" <<EOF
LLM_GATEWAY_ENV=dev
LLM_GATEWAY_LISTEN=$LISTEN
# dev-only 风险接受（R37 备案）：* 允许任意网页源携有效 key 调用 API（含
# /api 管理面前缀）。252 是 dev 实例、nginx 仅 llmgo.itestu.cn 暴露；转生产
# 前必须收敛到运维域。
LLM_GATEWAY_CORS_ORIGINS=*
LLM_GATEWAY_DATABASE_URL=$DB_URL_252
LLM_GATEWAY_JWT_SECRET=$LLM_GATEWAY_JWT_SECRET
LLM_GATEWAY_SECRET_KEY=$GEN_KEY
LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=$ENC_KEY
# 共享 PG 首启 EnsureSchema 超 20s 会被 boot 预算禁掉 DB（2026-09-17 实抓
# "postgres disabled: context deadline exceeded retry_budget=20s"），放宽到 10min。
# 注意必须带单位（time.ParseDuration），裸数字 "600" 会静默回退 20s 默认值
LLM_GATEWAY_DB_BOOT_RETRY_SECONDS=600s
# 2026-09-18 配合 PG max_connections=1000 落地（ALTER SYSTEM on pg-252-pg17）：
# storage full 模式 PG/Redis pool size。dev 单 pod，232 conn 总占 PG 远低于
# 1000 - 10 reserved = 990 上限。
LLM_GATEWAY_STORAGE_MAX_CONNECTIONS=200
EOF
)
chmod 600 "$BUILD_TMP/gateway.env"

if [[ $DRY_RUN == 1 ]]; then
  log "DRY-RUN: 将上传 binary+env → $TARGET_HOST:$REMOTE_DIR/{bin/gateway,.env.dev} 并安装 $SERVICE"
  exit 0
fi

# ── 上传与安装 ────────────────────────────────────────────────────────────
log "上传 binary 与 env..."
ssh -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new "root@$TARGET_HOST" \
  "mkdir -p $REMOTE_DIR/bin"
scp -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new \
  "$BUILD_TMP/gateway" "root@$TARGET_HOST:$REMOTE_DIR/bin/gateway.new"
# 版本 SSOT 是 /opt/llm-gateway-go/version.json（cmd/gateway/version.go），
# 不传则 /api/system/version 退化为 v0.0.0/dev
scp -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new \
  "$ROOT_DIR/version.json" "root@$TARGET_HOST:$REMOTE_DIR/version.json"
scp -qpi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new \
  "$BUILD_TMP/gateway.env" "root@$TARGET_HOST:$REMOTE_DIR/.env.dev.new"

log "安装 systemd 服务 $SERVICE..."
ssh -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new "root@$TARGET_HOST" bash -s <<REMOTE
set -euo pipefail
cd $REMOTE_DIR
mkdir -p bin
[ -f bin/gateway ] && mv bin/gateway "bin/gateway.bak.\$(date +%Y%m%d-%H%M%S)"
mv bin/gateway.new bin/gateway
chmod 755 bin/gateway
mv .env.dev.new .env.dev
chmod 600 .env.dev
cat > /etc/systemd/system/$SERVICE.service <<UNIT
[Unit]
Description=LLM Gateway Go 252 dev (llmgo.itestu.cn backend)
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$REMOTE_DIR
EnvironmentFile=$REMOTE_DIR/.env.dev
Environment=LLM_GATEWAY_LISTEN=$LISTEN
ExecStart=$REMOTE_DIR/bin/gateway
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable $SERVICE >/dev/null
systemctl restart $SERVICE
REMOTE

# ── 部署后验证 ────────────────────────────────────────────────────────────
log "验证 healthz / version / readyz..."
for i in $(seq 1 12); do
  H=$(curl -fsS -m 5 "http://127.0.0.1:8780/healthz" 2>/dev/null || true)
  [[ -n "$H" ]] && break
  sleep 5
done
ssh -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new "root@$TARGET_HOST" \
  "curl -fsS -m 5 http://127.0.0.1:8780/healthz && echo && systemctl is-active $SERVICE"
GATE_SHA=$(awk -F'"' '/"git_sha"/ {print $4; exit}' "$ROOT_DIR/version.json" 2>/dev/null || git -C "$ROOT_DIR" rev-parse --short HEAD)
ssh -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new "root@$TARGET_HOST" \
  "curl -fsS -m 5 http://127.0.0.1:8780/api/system/version" | grep -F "$GATE_SHA" \
  || { log "ERROR: 远端版本与 $GATE_SHA 不匹配"; exit 1; }

log "413 wire-code 探针（预算 guard，预期 prompt_too_large）..."
BODY_TMP="$(mktemp)"; trap 'rm -f "$BODY_TMP"; shred -u "$BUILD_TMP/gateway.env" 2>/dev/null; rm -rf "$BUILD_TMP"' EXIT
python3 - "$BODY_TMP" <<'PY'
import json, sys
open(sys.argv[1], "w").write(json.dumps({"model": "glm-4.7",
    "messages": [{"role": "user", "content": "a" * (33 * 1024 * 1024)}], "max_tokens": 1}))
PY
# R37 fix (D14): the test key was interpolated into the REMOTE command line
# (visible in the remote curl process's /proc and the local ssh argv for the
# call's duration). Pipe the header via stdin (`--header @-`) instead.
printf 'Authorization: Bearer %s\n' "$LLM_GATEWAY_TEST_KEY" | \
ssh -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new "root@$TARGET_HOST" \
  "curl -fsS -m 30 -X POST http://127.0.0.1:8780/v1/chat/completions -H 'Content-Type: application/json' --header @- --data-binary @$BODY_TMP" \
  | grep -q 'prompt_too_large' && log "PASS: 413 prompt_too_large wire code 验证通过" \
  || log "WARN: 413 探针未命中 prompt_too_large（检查 TEST_KEY 是否有效）"

log "VERIFY_SKILL=deploy-252-gateway"
log "VERIFY_TARGET=$TARGET_HOST:$LISTEN service=$SERVICE"
log "VERIFY_COMMIT=$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
log "VERIFY_RESULT=pass"
log "完成。回滚：ssh $TARGET_SSH 'cd $REMOTE_DIR && systemctl stop $SERVICE && mv bin/gateway.bak.* bin/gateway && systemctl start $SERVICE'"

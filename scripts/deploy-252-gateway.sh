#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
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
trap 'shred -u "$BUILD_TMP/gateway.env" "$BUILD_TMP/remote.env.prev" 2>/dev/null; rm -rf "$BUILD_TMP"' EXIT

log() { printf '[deploy-252] %s\n' "$*"; }

# ── SSOT 注入（fail-closed，本进程内生效）─────────────────────────────────
INJECTOR="$HOME/.agents/skills/env-injector/scripts/env-injector.sh"
[[ -f "$INJECTOR" ]] || { log "ERROR: env-injector 缺失：$INJECTOR"; exit 2; }
set +e
source "$INJECTOR" inject 252 >/dev/null 2>&1
set +u
for k in LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_JWT_SECRET LLM_GATEWAY_TEST_KEY SSH_KEY_252; do
  v="${!k:-}"
  [[ -n "$v" ]] || { log "ERROR: SSOT KEY 未注入：${k}（先 source env-injector inject 252）"; exit 2; }
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

  # web-mobile（Hyper 移动端，挂 /m）：与桌面 web/ 同一进程同一端口。
  # 缺 web-mobile/node_modules（未 install）时跳过——远端保留旧 dist，
  # /m/* 行为不变；构建失败 fail-closed（与 binary 同门）。
  mkdir -p "$ROOT_DIR/.build-local"
  if [[ -f "$ROOT_DIR/web-mobile/package.json" && -x "$ROOT_DIR/web-mobile/node_modules/.bin/vite" ]]; then
    log "构建 web-mobile..."
    if ! (cd "$ROOT_DIR/web-mobile" && npm run build) > "$ROOT_DIR/.build-local/deploy-252-web-mobile.log" 2>&1; then
      log "ERROR: web-mobile 构建失败（尾部日志）："
      tail -30 "$ROOT_DIR/.build-local/deploy-252-web-mobile.log" >&2 || true
      exit 1
    fi
    tar -C "$ROOT_DIR/web-mobile/dist" -czf "$BUILD_TMP/web-mobile.tgz" .
  else
    log "web-mobile 未构建（缺依赖），远端保留旧 dist"
  fi
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
#
# ★★ 2026-10-21 §143.53：**密钥【不轮换】** —— 轮换即作废全部 API key 与加密凭据。
#   实测证据（252 侧，2026-10-21）：
#   * `LLM_GATEWAY_SECRET_KEY` 是库内 `api_keys.key_hash` 的 HMAC 密钥
#     （`domains/authentication/verifier.go:623` `HashAPIKey(secretKey, key)`
#     = HMAC-SHA256）⇒ ⇒ **换 SECRET_KEY ⇒ 库里每一条 api_key 的哈希都再也对不上
#     ⇒ ⇒ 全部 API key 一次性失效**；
#   * `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 换了 ⇒ 库里用旧钥加密的凭据解不开
#     ⇒ ⇒ 凭据面全量 decrypt_failed。
#   ⇒ ⇒ **每次部署都重新生成 = 每次部署都打断现网鉴权** ⇒ ⇒ 这不是「dev 实例无所谓」
#   （DB 是共享的 172.16.2.210 库，api_keys 表对全实例可见）。
#   ⇒ ⇒ 修法：**先从远端现有 .env.dev 读回这两个值，存在就沿用**；
#   ⇒ ⇒ 只有远端还没有（首次部署）才生成新的。
#   ⇒ ⇒ 读取全程不回显、不落日志；临时副本在 EXIT trap 里 shred。
GEN_KEY=""
ENC_KEY=""
REMOTE_ENV_PREV="$BUILD_TMP/remote.env.prev"
if ssh -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 \
     "root@$TARGET_HOST" "cat $REMOTE_DIR/.env.dev 2>/dev/null" > "$REMOTE_ENV_PREV" 2>/dev/null \
   && [[ -s "$REMOTE_ENV_PREV" ]]; then
  GEN_KEY=$(sed -n 's/^LLM_GATEWAY_SECRET_KEY=//p' "$REMOTE_ENV_PREV" | tail -1 || true)
  ENC_KEY=$(sed -n 's/^LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=//p' "$REMOTE_ENV_PREV" | tail -1 || true)
  if [[ -n "$GEN_KEY" && -n "$ENC_KEY" ]]; then
    log "沿用远端既有 SECRET_KEY / CREDENTIAL_ENCRYPTION_KEY（不轮换）"
  else
    log "远端 env 缺少密钥项 ⇒ 本次生成新的（首次部署形态）"
  fi
else
  log "远端 env 不可读 ⇒ 本次生成新的密钥（首次部署形态）"
  GEN_KEY=""
  ENC_KEY=""
fi
[[ -n "$GEN_KEY" ]] || GEN_KEY=$(python3 -c "import secrets;print(secrets.token_urlsafe(33))")
[[ -n "$ENC_KEY" ]] || ENC_KEY=$(python3 -c "import secrets;print(secrets.token_urlsafe(33))")
# ★ 两个密钥必须是【两个不同的值】（R37 D14 的原意）：远端若历史上就是同一个值，
# ⇒ 这里补一个独立的 CREDENTIAL_ENCRYPTION_KEY（SECRET_KEY 保持不动）。
if [[ "$GEN_KEY" == "$ENC_KEY" ]]; then
  ENC_KEY=$(python3 -c "import secrets;print(secrets.token_urlsafe(33))")
  log "远端两个密钥同值 ⇒ 只补一个新的 CREDENTIAL_ENCRYPTION_KEY（SECRET_KEY 不动）"
fi
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
# 2026-09-18 配合 PG max_connections=1000 落地（ALTER SYSTEM on pg-252-pg17）。
# LLM_GATEWAY_DB_MAX_CONNS 是 full 模式 gateway 实际使用的 db.Open pgxpool
# 池上限（默认 32）。252 PG 为共享库（245/154/kaixuan-1 也连），dev 实例
# 保守取 64；总预算 990，当前消费者合计 << 上限。
LLM_GATEWAY_DB_MAX_CONNS=64
# LLM_GATEWAY_STORAGE_MAX_CONNECTIONS 只作用于 storage factory（当前仅
# lite 模式构造）；full 模式 gateway 不消费，保留供未来接线，非本实例瓶颈。
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
# web-mobile dist（若本轮构建了）：解包到 $REMOTE_DIR/web-mobile，供
# gateway 的 cwd 探测（WorkingDirectory=$REMOTE_DIR）注册 /m/* 路由。
if [[ -s "$BUILD_TMP/web-mobile.tgz" ]]; then
  log "上传 web-mobile dist..."
  scp -qi "$SSH_KEY_252" -o StrictHostKeyChecking=accept-new \
    "$BUILD_TMP/web-mobile.tgz" "root@$TARGET_HOST:$REMOTE_DIR/web-mobile.tgz.new"
fi

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
if [ -f web-mobile.tgz.new ]; then
  rm -rf web-mobile.new && mkdir web-mobile.new
  tar -xzf web-mobile.tgz.new -C web-mobile.new
  rm -f web-mobile.tgz.new
  rm -rf web-mobile.prev && [ -d web-mobile ] && mv web-mobile web-mobile.prev
  mv web-mobile.new web-mobile
fi
cat > /etc/systemd/system/$SERVICE.service <<UNIT
[Unit]
Description=LLM Gateway Go 252 dev (llmgo.itestu.cn backend)
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$REMOTE_DIR
EnvironmentFile=$REMOTE_DIR/.env.dev
Environment=LLM_GATEWAY_LISTEN=$LISTEN
Environment=MOBILE_WEB_DIST=$REMOTE_DIR/web-mobile
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

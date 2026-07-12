#!/usr/bin/env bash
# deploy-154-prod.sh — Deploy to 154 (47.97.111.154 / llm.kxpms.cn)
#
# Differs from deploy-154.sh:
#   - 154 uses binary name `llm-gateway-go` (not `gateway`)
#   - 154 has versioned binaries: llm-gateway-go.vNNN.linux.amd64
#   - 154 uses nginx conf /etc/nginx/conf.d/llm-kxpms-cn.conf
#   - 154 dist at /opt/llm-gateway-go/web/
#
# Includes the fix(i18n) commit's dist (with App.vue auto re-login + t() shadow fixes).

set -euo pipefail

# ── 前置 (HARD-GATE): DEPLOY_SSH_PASS 必须从 env 注入，不再硬编码 ─────────
if [ -z "${DEPLOY_SSH_PASS:-}" ]; then
  err "必须设置 DEPLOY_SSH_PASS 环境变量（密码已不再硬编码）"
  echo "  示例: export DEPLOY_SSH_PASS='<your-password>'"
  echo "  推荐: 使用 ~/.ssh/id_ed25519 密钥登录"
  exit 1
fi

# ── 配置 ──────────────────────────────────────────────────────
SSH_HOST="47.97.111.154"
SSH_PORT="25022"
SSH_USER="root"
SSH_PASS="${DEPLOY_SSH_PASS}"
REMOTE_DIR="/opt/llm-gateway-go"
BINARY_NAME="llm-gateway-go"

export SSHPASS="$SSH_PASS"

G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}!${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}═══════ $* ═══════${N}"; }

# ── 1. 本地编译 ──────────────────────────────────────────────
phase "本地交叉编译 (Linux AMD64)"
info "禁用代理..."
unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy
go env -w GOPROXY=https://goproxy.cn,direct

info "编译 ${BINARY_NAME} (Linux AMD64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod -o bin/llm-gateway-go-linux-amd64 ./cmd/gateway
ls -lh bin/llm-gateway-go-linux-amd64
file bin/llm-gateway-go-linux-amd64 | grep -q "x86-64" || { err "二进制不是 Linux x86-64 格式"; exit 1; }
ok "编译完成"

# ── 2. vite build ────────────────────────────────────────────
phase "vite build"
cd web
npx vite build 2>&1 | tail -3
cd ..

# ── 3. 上传二进制到 154 ────────────────────────────────────
phase "上传二进制到 154"

# Determine next version number (max existing + 1)
VER=$(sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" \
  "ls /opt/llm-gateway-go/${BINARY_NAME}.v*.linux.amd64 2>/dev/null | grep -oE 'v[0-9]+' | grep -oE '[0-9]+' | sort -n | tail -1")
VER=${VER:-0}
NEXT_VER=$((VER + 1))
info "下一个版本号: v${NEXT_VER}"

REMOTE_BIN="${REMOTE_DIR}/${BINARY_NAME}.v${NEXT_VER}.linux.amd64"

info "上传二进制到 ${REMOTE_BIN}..."
sshpass -e scp -P "$SSH_PORT" -o StrictHostKeyChecking=no \
  "bin/llm-gateway-go-linux-amd64" \
  "$SSH_USER@$SSH_HOST:${REMOTE_BIN}"
ok "二进制已上传"

# ── 4. 上传 dist ──────────────────────────────────────────────
phase "上传 web/dist 到 154"
info "tar + scp dist..."
tar -czf /tmp/llmgo-154-dist.tar.gz -C web dist
sshpass -e scp -P "$SSH_PORT" -o StrictHostKeyChecking=no \
  /tmp/llmgo-154-dist.tar.gz "$SSH_USER@$SSH_HOST:/tmp/"
# 生产配置 (LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web) 要求 assets/ 和
# index.html 直接放在 web/ 根下,不是 web/dist/。所以部署时把 dist/ 内的内容
# 平铺到 web/。同时保留 dist/ 目录(给 nginx try_files 之类的备援)以便回滚。
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
set -e
cd ${REMOTE_DIR}/web
# 备份当前真正被服务的目录(root)
if [ -d assets ] || [ -f index.html ]; then
  TS=\$(date +%Y%m%d-%H%M%S)
  if [ -d assets ]; then mv assets assets.bak.\$TS; fi
  if [ -f index.html ]; then mv index.html index.html.bak.\$TS; fi
fi
# 平铺 dist 内容到 web 根
rm -rf assets index.html
cp -a dist/. .
chown -R root:root assets index.html favicon.png 2>/dev/null || true
find . -name '._*' -delete 2>/dev/null
echo '--- web/ root ---'
ls -la | head -15
echo '--- 备份 ---'
ls -dt assets.bak.* index.html.bak.* 2>/dev/null | head -6
"
ok "dist 已部署"

# ── 5. 切换 symlink + 重启 ────────────────────────────────
phase "切换 symlink + 重启 systemd"
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
cd ${REMOTE_DIR}
# 备份当前 symlink
if [ -L ${BINARY_NAME} ] && [ -e ${BINARY_NAME} ]; then
  TARGET=\$(readlink ${BINARY_NAME})
  echo \"当前 symlink 指向: \$TARGET\"
  # 把当前 binary 备份（保留 1 个）
  ls -t ${BINARY_NAME}.bak-* 2>/dev/null | tail -n +3 | xargs -r rm --
  LAST_VER=\$(echo \"\$TARGET\" | grep -oE 'v[0-9]+' | grep -oE '[0-9]+')
  cp -a \${BINARY_NAME} ${BINARY_NAME}.bak-\$(date +%Y%m%d-%H%M%S)-v\${LAST_VER}
fi
# 切换到新版本
ln -sf ${REMOTE_BIN} ${BINARY_NAME}
chmod +x ${BINARY_NAME}
ls -la ${BINARY_NAME}
"

info "systemctl restart..."
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
systemctl restart ${BINARY_NAME}.service
sleep 5
systemctl status ${BINARY_NAME}.service --no-pager | head -10
"

# ── 6. 验证 ────────────────────────────────────────────────
phase "验证"
sleep 3
info "healthz..."
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
curl -sS https://llm.kxpms.cn/healthz -w '\nHTTP %{http_code}\n' 2>&1 | head -3
echo '---'
ps aux | grep -v grep | grep ${BINARY_NAME} | head -2
"
ok "部署完成"

echo
echo "================================================================"
echo "  生产部署完成 → https://llm.kxpms.cn"
echo "  Commit: $(git log -1 --format='%h %s')"
echo "  Binary version: v${NEXT_VER}"
echo "================================================================"
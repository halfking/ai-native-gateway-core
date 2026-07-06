#!/usr/bin/env bash
# =====================================================================
# scripts/deploy.sh — 统一部署入口（184 K8s + 71 systemd）
#
# 用法:
#   ./scripts/deploy.sh 184                    # 仅部署到 184
#   ./scripts/deploy.sh 71                     # 仅部署到 71
#   ./scripts/deploy.sh both                   # 184 后 71（推荐顺序）
#   ./scripts/deploy.sh build                  # 仅构建（不部署）
#   ./scripts/deploy.sh migrate <184|71>       # 仅运行 DB 迁移
#   ./scripts/deploy.sh verify <184|71>        # 仅运行验证
#   ./scripts/deploy.sh rollback 184           # 回滚
#
# 选项:
#   --with-migration      部署后运行 DB 迁移（184 默认 true）
#   --skip-tests          跳过 go build/vet 预检
#   --dry-run             仅打印步骤
#   --no-rollback         验证失败时不要回滚
#   --skip-build          跳过镜像构建，使用现有 image_tag
#   -h, --help            显示帮助
#
# 环境变量（覆盖默认）:
#   SSHPASS=<密码>          71 部署必需
#   SSH_KEY_184, SSH_KEY_71  SSH 私钥（默认 ~/.ssh/{56,71}_id_rsa）
#   BUILD_SEQ_TARGET=<seq>   使用指定 build_seq 而非 +1
#   REGISTRY_INT=<host>      内部 registry（默认 registry.kxpms.cn）
#   REGISTRY_LOCAL=<host>    184 本地 registry（默认 127.0.0.1:5000）
#
# 与 env-injector 集成:
#   部署前通过 `env-injector inject huoshan-core-184` / `huoshan-infra-71`
#   注入凭据。脚本自动探测已 export 的 SSH_KEY_* / SSHPASS。
#
# 退出码:
#   0 = 成功
#   1 = 预检失败
#   2 = 构建失败
#   3 = 推送失败
#   4 = 部署失败
#   5 = 验证失败（已回滚）
#   6 = 迁移失败
#   64 = 用法错误
#
# =====================================================================
# 关键修复（2026-07-06 部署踩坑后引入）:
#   [F1] Smart 推送到 184 本地 registry：尝试本地直推，失败则 SSH 到 184
#        上 pull + retag + push（绕过 Docker daemon HTTP_PROXY 拦截）。
#   [F2] 自动 SKIP 标记 SUPERSEDED 的迁移（如已废弃的 352）。
#   [F3] 预检 go build + go vet（捕获路由冲突等 panic-class bug，
#        避免 K8s CrashLoopBackOff）。
#   [F4] 验证失败自动回滚：184 用 kubectl rollout undo；71 用 .bak 二进制。
#   [F5] 71 默认数据库 host 检查（不连错 184 的 172.31.0.4）。
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# ── 颜色与日志 ─────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}!${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${G}═══════ $* ═══════${N}"; }
die()   { err "$@"; exit "${2:-1}"; }

# ── 默认配置 ───────────────────────────────────────────────────────
TARGET_DEFAULT="both"
TARGET="${1:-$TARGET_DEFAULT}"
shift 2>/dev/null || true

# Parse flags
WITH_MIGRATION=false
SKIP_TESTS=false
DRY_RUN=false
NO_ROLLBACK=false
SKIP_BUILD=false
BUILD_SEQ_TARGET=""
ACTION="deploy"

# Help first (don't need target/args for help)
if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 0
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --with-migration) WITH_MIGRATION=true; shift ;;
    --skip-tests)     SKIP_TESTS=true; shift ;;
    --dry-run)        DRY_RUN=true; shift ;;
    --no-rollback)    NO_ROLLBACK=true; shift ;;
    --skip-build)     SKIP_BUILD=true; shift ;;
    --seq)            BUILD_SEQ_TARGET="$2"; shift 2 ;;
    -h|--help)        sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)                err "未知参数: $1"; exit 64 ;;
  esac
done

# Servers & keys
SERVER_184="root@14.103.112.184"
SERVER_184_HOST="14.103.112.184"
SERVER_71="root@14.103.174.71"
SERVER_71_HOST="14.103.174.71"
SSH_PORT="${SSH_PORT:-25022}"
SSH_KEY_184="${SSH_KEY_184:-$HOME/.ssh/56_id_rsa}"
SSH_KEY_71="${SSH_KEY_71:-$HOME/.ssh/71_id_rsa}"
SSH_184_OPT="-p $SSH_PORT -i $SSH_KEY_184 -o StrictHostKeyChecking=no -o BatchMode=yes"
SSH_71_OPT="-p $SSH_PORT -i $SSH_KEY_71 -o StrictHostKeyChecking=no -o BatchMode=yes"
SCP_184_OPT="-P $SSH_PORT -i $SSH_KEY_184 -o StrictHostKeyChecking=no"
SCP_71_OPT="-P $SSH_PORT -i $SSH_KEY_71 -o StrictHostKeyChecking=no"

# Image / registry
IMAGE_NAME="kx-llm-gateway-go"
REGISTRY_INT="${REGISTRY_INT:-registry.kxpms.cn}"
REGISTRY_LOCAL="${REGISTRY_LOCAL:-127.0.0.1:5000}"
K8S_NS="pms-test"
K8S_DEP="llm-gateway-go-deployment"

# 71 binary
BIN_NAME="llm-gateway-go.v321.linux.amd64"

# ── 公共函数 ────────────────────────────────────────────────────────

usage_short() {
  cat <<EOF
用法: $0 <target> [options]
target: 184 | 71 | both | build | migrate | verify | rollback
详细: $0 --help
EOF
}

# ── 早期验证 target ─────────────────────────────────────────────────
case "$TARGET" in
  184|71|both|build|migrate|verify|rollback)
    if [[ "$TARGET" == "184" || "$TARGET" == "both" ]]; then
      [[ -f "$SSH_KEY_184" ]] || { err "找不到 SSH 私钥: $SSH_KEY_184 (设置 SSH_KEY_184 或放入 ~/.ssh/56_id_rsa)"; exit 1; }
    fi
    if [[ "$TARGET" == "71" || "$TARGET" == "both" ]]; then
      [[ -f "$SSH_KEY_71" ]] || { err "找不到 SSH 私钥: $SSH_KEY_71"; exit 1; }
    fi
    ;;
  *) usage_short; exit 64 ;;
esac

pre_check() {
  phase "预检 1/2: 工作区 + git"
  if ! git diff --quiet HEAD -- 2>/dev/null; then
    err "工作区有未提交的改动:"
    git status -s
    return 1
  fi
  ok "工作区干净"

  phase "预检 2/2: go build + vet (F3: 捕获 panic-class bugs)"
  if [[ "$SKIP_TESTS" == "true" ]]; then
    warn "已 --skip-tests，跳过 go build/vet"
    return 0
  fi
  info "go build ./..."
  if ! go build ./... 2>&1 | tail -10; then
    err "go build 失败！常见原因: 路由注册冲突 (本次踩坑)、import 错误、类型错误"
    return 1
  fi
  info "go vet ./..."
  if ! go vet ./... 2>&1 | tail -10; then
    err "go vet 失败"
    return 1
  fi
  ok "go build + vet 通过"
}

get_version() {
  phase "版本信息"
  GIT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v2.4.1")
  GIT_SHA=$(git rev-parse --short=8 HEAD)
  BUILD_DATE=$(date +%Y%m%d)
  CURRENT_SEQ=$(cat build_seq 2>/dev/null || echo 0)
  
  if [[ -n "$BUILD_SEQ_TARGET" ]]; then
    NEW_BUILD_SEQ="$BUILD_SEQ_TARGET"
  else
    NEW_BUILD_SEQ=$((CURRENT_SEQ + 1))
  fi
  
  IMAGE_TAG="${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${NEW_BUILD_SEQ}"
  VERSION_STRING="${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${NEW_BUILD_SEQ}"
  
  echo "$NEW_BUILD_SEQ" > build_seq
  cat > version.json <<EOF
{
  "version": "${GIT_TAG}",
  "git_tag": "${GIT_TAG}",
  "git_sha": "${GIT_SHA}",
  "build_seq": ${NEW_BUILD_SEQ},
  "build_date": "${BUILD_DATE}",
  "module": "llm-gateway-go"
}
EOF
  
  info "Git Tag: $GIT_TAG"
  info "Git SHA: $GIT_SHA"
  info "Build Seq: $CURRENT_SEQ → $NEW_BUILD_SEQ"
  info "Image Tag: $IMAGE_TAG"
  
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 已更新 build_seq / version.json，未提交"
  fi
}

# F1: Smart push to 184 local registry.
# Tries local docker push; if Docker daemon HTTP_PROXY blocks 127.0.0.1,
# falls back to SSH-in-184 pull+retag+push.
push_to_local_registry() {
  phase "推送到 184 本地 registry ${REGISTRY_LOCAL} (smart)"
  local img="${IMAGE_NAME}:${IMAGE_TAG}"
  local remote_img="${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG}"
  
  info "尝试本地直推: $remote_img"
  if docker push "$remote_img" 2>/tmp/.push.err; then
    ok "本地直推成功"
    return 0
  fi
  
  local err_msg=$(cat /tmp/.push.err)
  if echo "$err_msg" | grep -qE "connection refused|connection reset|i/o timeout|no such host" ; then
    warn "本地直推受阻（疑似 Docker daemon HTTP_PROXY 拦截 $REGISTRY_LOCAL）："
    echo "  $err_msg" | head -3
  else
    err "推送失败（非代理问题）:"
    cat /tmp/.push.err
    return 1
  fi
  
  info "回退路径：SSH 到 184 拉取 ${REGISTRY_INT}/${img} 并 re-tag 重推"
  ssh $SSH_184_OPT $SERVER_184 bash <<EOF || return 1
set -e
docker pull ${REGISTRY_INT}/${img}
docker tag ${REGISTRY_INT}/${img} ${remote_img}
docker push ${remote_img}
EOF
  ok "通过 184 推送成功"
}

commit_build_seq() {
  phase "提交 build_seq ($NEW_BUILD_SEQ)"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 跳过 commit"
    return 0
  fi
  if git diff --quiet build_seq version.json 2>/dev/null; then
    info "build_seq / version.json 无变化，跳过 commit"
    return 0
  fi
  git add build_seq version.json
  git commit -m "chore: bump build_seq to ${NEW_BUILD_SEQ} via deploy.sh [skip ci]" 2>&1 | tail -3
  ok "build_seq 已提交"
}

# ── 184 部署 ────────────────────────────────────────────────────────

build_image() {
  phase "184: 构建镜像"
  if [[ "$SKIP_BUILD" == "true" ]]; then
    info "已 --skip-build，跳过 docker build"
    return 0
  fi
  docker build \
    --build-arg GIT_TAG="${GIT_TAG}" \
    --build-arg GIT_SHA="${GIT_SHA}" \
    --build-arg BUILD_SEQ="${NEW_BUILD_SEQ}" \
    --build-arg BUILD_DATE="${BUILD_DATE}" \
    -t "${IMAGE_NAME}:${IMAGE_TAG}" \
    -t "${IMAGE_NAME}:latest" \
    . | tail -20
  ok "镜像构建完成"
}

push_to_public_registry() {
  phase "184: 推送到 ${REGISTRY_INT}"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  docker tag "${IMAGE_NAME}:${IMAGE_TAG}" "${REGISTRY_INT}/${IMAGE_NAME}:${IMAGE_TAG}"
  docker push "${REGISTRY_INT}/${IMAGE_NAME}:${IMAGE_TAG}" 2>&1 | tail -5
  ok "已推送"
}

update_k8s_deployment() {
  phase "184: kubectl set image → rollout status"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl set image deployment/${K8S_DEP} ${IMAGE_NAME}=${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG} -n ${K8S_NS}"
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl rollout status deployment/${K8S_DEP} -n ${K8S_NS} --timeout=5m"
  ok "K8s 滚动更新完成"
}

verify_184() {
  phase "184: 验证"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  sleep 8  # 等启动 + readiness probe
  
  # Pod 状态
  local pod=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl get pods -n ${K8S_NS} -l app=llm-gateway-go --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'" 2>/dev/null)
  if [[ -z "$pod" ]]; then
    err "找不到 Running pod"
    return 1
  fi
  
  local ready=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl get pods -n ${K8S_NS} -l app=llm-gateway-go -o jsonpath='{.items[0].status.containerStatuses[0].ready}'" 2>/dev/null)
  if [[ "$ready" != "true" ]]; then
    err "Pod Ready=$ready (期望 true) — pod: $pod"
    return 1
  fi
  ok "Pod Ready=1/1 ($pod)"
  
  # 无 panic / fatal
  local panic_lines=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl logs -n ${K8S_NS} $pod --tail=300 2>/dev/null | grep -ciE 'panic|fatal'" || echo "0")
  if [[ "$panic_lines" -gt 0 ]]; then
    err "Pod 日志发现 $panic_lines 处 panic/fatal："
    ssh $SSH_184_OPT $SERVER_184 \
      "kubectl logs -n ${K8S_NS} $pod --tail=100 2>/dev/null | grep -A 1 -E 'panic|fatal' | head -20"
    return 1
  fi
  ok "无 panic/fatal"
  
  # 公开域名
  local body=$(curl -fsS --max-time 10 https://llmgo.kxpms.cn/healthz 2>&1 || echo "")
  if [[ -z "$body" ]]; then
    err "https://llmgo.kxpms.cn/healthz 不可达"
    return 1
  fi
  ok "https://llmgo.kxpms.cn → $body"
  if ! echo "$body" | grep -q "$IMAGE_TAG"; then
    err "公开域版本不匹配: 期望 $IMAGE_TAG"
    return 1
  fi
  ok "版本匹配: $IMAGE_TAG"
  return 0
}

rollback_184() {
  phase "184: 自动回滚"
  warn "触发回滚: kubectl rollout undo deployment/${K8S_DEP} -n ${K8S_NS}"
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl rollout undo deployment/${K8S_DEP} -n ${K8S_NS}" 2>&1 | tail -3
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl rollout status deployment/${K8S_DEP} -n ${K8S_NS} --timeout=3m" 2>&1 | tail -5
}

# F2: auto-skip superseded migrations. Also tracks applied versions in
# schema_migrations so re-runs are idempotent.
run_migrations_184() {
  phase "184: DB 迁移 (auto-skip SUPERSEDED + 幂等)"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  local mig_dir="$REPO_ROOT/db/migrations"
  
  # Upload
  ssh $SSH_184_OPT $SERVER_184 "mkdir -p /tmp/migrations_run && rm -f /tmp/migrations_run/*"
  for f in "$mig_dir"/*.sql; do
    [[ "$(basename "$f")" == *".down.sql" ]] && continue
    scp $SCP_184_OPT "$f" $SERVER_184:/tmp/migrations_run/ 2>/dev/null
  done
  
  # 提取 DB 凭据
  local pod=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl get pods -n ${K8S_NS} -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}'")
  local db_url=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl exec -n ${K8S_NS} $pod -- printenv LLM_GATEWAY_DATABASE_URL")
  local db_pass=$(echo "$db_url" | sed -n 's|.*://[^:]*:\([^@]*\)@.*|\1|p')
  local db_host=$(echo "$db_url" | sed -n 's|.*@\([^:]*\):.*|\1|p')
  
  # Host 校验 (F5: 不连错 71 的 172.31.0.3 等)
  case "$db_host" in
    172.31.0.4|10.43.*) ok "DB host = $db_host (K8s 内部 IP, 正确)" ;;
    172.31.0.3) err "DB host = 172.31.0.3 (这是 71 的 IP! 184 应连 172.31.0.4 或 K8s ClusterIP)"; return 1 ;;
    *) warn "DB host = $db_host (非预期; 请人工确认)" ;;
  esac
  
  ssh $SSH_184_OPT $SERVER_184 bash <<EOF || { return 1; }
set -e
export PGPASSWORD="$db_pass"

# 已应用版本
APPLIED=\$(psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway -tAc \
  "SELECT version FROM schema_migrations;" 2>/dev/null | tr -d ' ')

cd /tmp/migrations_run
TOTAL=0; APPLIED_C=0; SKIPPED=0; FAILED=0
for f in *.sql; do
  TOTAL=\$((TOTAL+1))
  version=\$(echo "\$f" | grep -oE '^[0-9]+' || echo "0")
  
  # F2: auto-skip SUPERSEDED
  if head -10 "\$f" | grep -qiE "SUPERSEDED|superceded|DEPRECATED"; then
    echo "  [\$f] ⊘ SKIP (SUPERSEDED)"
    SKIPPED=\$((SKIPPED+1))
    continue
  fi
  
  # 跳过已应用
  if echo "\$APPLIED" | grep -qFx "\$version"; then
    echo "  [\$f] ⊘ SKIP (已应用)"
    SKIPPED=\$((SKIPPED+1))
    continue
  fi
  
  # 应用
  if psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway \
      -v ON_ERROR_STOP=1 -f "/tmp/migrations_run/\$f" >/tmp/_mig.log 2>&1; then
    echo "  [\$f] ✓ OK"
    psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway \
      -c "INSERT INTO schema_migrations (version, applied_at) VALUES ('\$version', NOW());" >/dev/null 2>&1 || true
    APPLIED_C=\$((APPLIED_C+1))
  elif grep -qE "already exists|duplicate key|relation.*already exists" /tmp/_mig.log 2>/dev/null; then
    echo "  [\$f] ⊘ SKIP (idempotent: 已存在)"
    psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway \
      -c "INSERT INTO schema_migrations (version, applied_at) VALUES ('\$version', NOW());" >/dev/null 2>&1 || true
    SKIPPED=\$((SKIPPED+1))
  else
    echo "  [\$f] ✗ FAIL"
    head -3 /tmp/_mig.log | sed 's/^/    /'
    FAILED=\$((FAILED+1))
  fi
done
rm -f /tmp/_mig.log /tmp/migrations_run/*.sql
rmdir /tmp/migrations_run 2>/dev/null || true

echo ""
echo "=== DB 迁移汇总 ==="
echo "  total=$TOTAL  applied=$APPLIED_C  skipped=$SKIPPED  failed=$FAILED"
[[ \$FAILED -gt 0 ]] && exit 1 || true
EOF
}

deploy_184() {
  phase "════════════ 184 K8s 部署 ════════════"
  build_image
  push_to_public_registry
  push_to_local_registry
  update_k8s_deployment
  
  if ! verify_184; then
    err "184 验证失败"
    [[ "$NO_ROLLBACK" != "true" ]] && rollback_184
    exit 5
  fi
  
  if [[ "$WITH_MIGRATION" == "true" ]]; then
    run_migrations_184 && {
      info "迁移后 rolling restart"
      ssh $SSH_184_OPT $SERVER_184 \
        "kubectl rollout restart deployment/${K8S_DEP} -n ${K8S_NS}" >/dev/null
      ssh $SSH_184_OPT $SERVER_184 \
        "kubectl rollout status deployment/${K8S_DEP} -n ${K8S_NS} --timeout=3m" >/dev/null
      verify_184 || { err "迁移后验证失败"; [[ "$NO_ROLLBACK" != "true" ]] && rollback_184; exit 5; }
    }
  fi
  ok "184 部署完成"
}

# ── 71 部署 ─────────────────────────────────────────────────────────

cross_compile() {
  phase "71: 交叉编译 linux/amd64"
  if [[ "$SKIP_BUILD" == "true" ]]; then
    info "已 --skip-build"
    return 0
  fi
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags="-s -w \
      -X 'main.Version=${GIT_TAG}' \
      -X 'main.GitCommit=${GIT_SHA}' \
      -X 'main.BuildDate=${BUILD_DATE}' \
      -X 'main.BuildNumber=${NEW_BUILD_SEQ}'" \
    -o "${BIN_NAME}" \
    ./cmd/gateway 2>&1 | tail -10
  local size=$(ls -lh "${BIN_NAME}" | awk '{print $5}')
  file "${BIN_NAME}" | head -1 | sed 's/^/  /'
  ok "二进制已构建: $BIN_NAME ($size)"
}

upload_to_71() {
  phase "71: 上传二进制 + VERSION"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  
  scp $SCP_71_OPT "${BIN_NAME}" "$SERVER_71:/tmp/"
  
  echo "$VERSION_STRING" > /tmp/VERSION_NEW
  echo "$NEW_BUILD_SEQ" > /tmp/.deploy_seq
  scp $SCP_71_OPT /tmp/VERSION_NEW "$SERVER_71:/tmp/VERSION_NEW"
  scp $SCP_71_OPT /tmp/.deploy_seq "$SERVER_71:/tmp/.deploy_seq"
  ok "已上传"
}

restart_71() {
  phase "71: 原子化备份 + restart"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  ssh $SSH_71_OPT $SERVER_71 bash <<EOF || { err "71 端操作失败"; return 1; }
set -e
mkdir -p /opt/llm-gateway-go/backup

echo "  [1/7] 备份当前二进制 (.bak.YYYYMMDD-HHMMSS)"
BAK="/opt/llm-gateway-go/backup/${BIN_NAME}.bak.\$(date +%Y%m%d-%H%M%S)"
cp /opt/llm-gateway-go/${BIN_NAME} "\$BAK"
echo "       → \$BAK"

echo "  [2/7] 停止服务 (释放 mmap 锁)"
systemctl stop llm-gateway-go.service || true
sleep 2

echo "  [3/7] 替换二进制"
mv -f /tmp/${BIN_NAME} /opt/llm-gateway-go/${BIN_NAME}
chmod +x /opt/llm-gateway-go/${BIN_NAME}

echo "  [4/7] 更新 VERSION + .deploy_seq"
mv -f /tmp/VERSION_NEW /opt/llm-gateway-go/VERSION
mv -f /tmp/.deploy_seq /opt/llm-gateway-go/.deploy_seq

echo "  [5/7] 清理旧 .bak (保留最近 5 个)"
ls -1t /opt/llm-gateway-go/backup/*.bak.* 2>/dev/null | tail -n +6 | xargs -r rm -f

echo "  [6/7] 启动服务"
systemctl start llm-gateway-go.service
sleep 3

echo "  [7/7] 等待 ready (port 8781 listening)"
for i in 1 2 3 4 5 6 7 8 9 10; do
  if ss -tln 2>/dev/null | grep -q :8781; then
    echo "       端口已就绪 (\${i}s)"
    break
  fi
  sleep 1
done
EOF
}

verify_71() {
  phase "71: 验证"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  sleep 3
  
  local active=$(ssh $SSH_71_OPT $SERVER_71 'systemctl is-active llm-gateway-go.service' 2>/dev/null)
  if [[ "$active" != "active" ]]; then
    err "service $active (期望 active)"
    return 1
  fi
  ok "service active"
  
  # F5: DB host 不应是 184
  local db_url=$(ssh $SSH_71_OPT $SERVER_71 'grep LLM_GATEWAY_DATABASE_URL /etc/llm-gateway-go/env | cut -d= -f2-')
  case "$db_url" in
    *172.31.0.3*) ok "DB host = 172.31.0.3 (71 本地 PG, 正确)" ;;
    *172.31.0.4*) err "DB host = 172.31.0.4 (这是 184 的 PG!)"; return 1 ;;
    *) warn "DB host 未识别: ${db_url:0:60}..." ;;
  esac
  
  local seq=$(ssh $SSH_71_OPT $SERVER_71 'cat /opt/llm-gateway-go/.deploy_seq' 2>/dev/null)
  if [[ "$seq" != "$NEW_BUILD_SEQ" ]]; then
    err "build_seq 不匹配: $seq != $NEW_BUILD_SEQ"
    return 1
  fi
  ok "build_seq=$seq"
  
  local body=$(ssh $SSH_71_OPT $SERVER_71 'curl -fsS --max-time 5 http://localhost:8781/healthz' 2>/dev/null)
  if [[ -z "$body" ]]; then
    err "http://localhost:8781/healthz 不可达"
    return 1
  fi
  if ! echo "$body" | grep -q "$VERSION_STRING"; then
    err "/healthz 版本不匹配: $body"
    return 1
  fi
  ok "/healthz (71 内部): $body"
  
  local pub=$(curl -fsS --max-time 10 https://llm.kxpms.cn/healthz 2>/dev/null)
  if [[ -z "$pub" ]]; then
    err "https://llm.kxpms.cn 不可达"
    return 1
  fi
  ok "https://llm.kxpms.cn: $pub"
  
  # bind-mount 检查
  local data_mounted=$(ssh $SSH_71_OPT $SERVER_71 \
    'docker exec llm-gateway-go mount 2>/dev/null | grep -q /opt/llm-gateway-go/data && echo yes')
  if [[ "$data_mounted" != "yes" ]]; then
    warn "data bind-mount 缺失 (运维层: 跑 deploy-71-data-bindmounts.sh)"
  else
    ok "data bind-mount OK"
  fi
  return 0
}

rollback_71() {
  phase "71: 自动回滚（最近 .bak）"
  warn "回滚: 选择最近 .bak 重启服务"
  ssh $SSH_71_OPT $SERVER_71 bash <<'EOF'
set -e
LATEST_BAK=$(ls -1t /opt/llm-gateway-go/backup/*.bak.* 2>/dev/null | head -1)
if [[ -z "$LATEST_BAK" ]]; then
  echo "  ✗ 无 .bak 可回滚"
  exit 1
fi
echo "  → 回滚到 $LATEST_BAK"
systemctl stop llm-gateway-go.service || true
sleep 2
cp "$LATEST_BAK" /opt/llm-gateway-go/"$(basename "${BIN_NAME:-llm-gateway-go.v321.linux.amd64}")"
systemctl start llm-gateway-go.service
sleep 5
echo "  当前版本: $(cat /opt/llm-gateway-go/VERSION)"
EOF
}

deploy_71() {
  phase "════════════ 71 systemd 部署 ════════════"
  cross_compile
  upload_to_71
  restart_71
  
  if ! verify_71; then
    err "71 验证失败"
    [[ "$NO_ROLLBACK" != "true" ]] && rollback_71
    exit 5
  fi
  ok "71 部署完成"
}

# ── 入口 ────────────────────────────────────────────────────────────

main() {
  case "$TARGET" in
    184)
      pre_check || die "预检失败" 1
      get_version
      deploy_184
      commit_build_seq
      ;;
    71)
      pre_check || die "预检失败" 1
      get_version
      deploy_71
      commit_build_seq
      ;;
    both)
      pre_check || die "预检失败" 1
      get_version
      deploy_184 || die "184 部署失败 (71 未开始)" 4
      echo
      deploy_71
      commit_build_seq
      ;;
    build)
      pre_check || die "预检失败" 1
      get_version
      build_image
      info "构建完成 (镜像: ${IMAGE_NAME}:${IMAGE_TAG})"
      [[ "$DRY_RUN" != "true" ]] && commit_build_seq
      ;;
    migrate)
      [[ -z "${2:-}" ]] && die "用法: $0 migrate <184|71>" 64
      [[ "$2" == "184" ]] && run_migrations_184
      ;;
    verify)
      [[ -z "${2:-}" ]] && die "用法: $0 verify <184|71>" 64
      get_version
      [[ "$2" == "184" ]] && verify_184
      [[ "$2" == "71"  ]] && verify_71
      ;;
    rollback)
      [[ -z "${2:-}" ]] && die "用法: $0 rollback <184|71>" 64
      [[ "$2" == "184" ]] && rollback_184
      [[ "$2" == "71"  ]] && rollback_71
      ;;
    *)
      usage_short; exit 64
      ;;
  esac
}

main "$@"

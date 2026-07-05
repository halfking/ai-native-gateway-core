#!/usr/bin/env bash
# init-deploy-record.sh — 初始化部署记录目录
#
# 在每个部署前调用，生成 deploy/r{seq}-{date}/ 目录结构：
#   README.md       部署概览（自动生成）
#   CHANGELOG.md    本次变更说明（git log）
#   plan.md         部署计划模板
#   sql/            SQL 变更文档（自动检测 git diff）
#   verify/         验证结果（部署后写入）
#   artifacts/      构建产物摘要
#
# 用法:
#   ./scripts/init-deploy-record.sh                        # 自动模式（读 build_seq）
#   BUILD_SEQ=57 ./scripts/init-deploy-record.sh           # 指定 seq
#   ./scripts/init-deploy-record.sh --list                 # 列出已有记录

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 参数 ──
if [[ "${1:-}" == "--list" ]]; then
  echo "以存在的部署记录:"
  ls -1d "$ROOT_DIR/deploy/r"*-*/ 2>/dev/null | while read d; do
    name=$(basename "$d")
    if [[ -f "$d/README.md" ]]; then
      desc=$(head -5 "$d/README.md" | grep "^# " | sed 's/^# //')
    else
      desc="(无 README)"
    fi
    echo "  $name  — $desc"
  done
  exit 0
fi

# ── 读取版本信息 ──
BUILD_SEQ="${BUILD_SEQ:-$(cat "$ROOT_DIR/build_seq" 2>/dev/null || echo "0")}"
GIT_SHA=$(git -C "$ROOT_DIR" rev-parse --short=8 HEAD 2>/dev/null || echo "unknown")
GIT_TAG=$(git -C "$ROOT_DIR" describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
GIT_LOG_RAW=$(git -C "$ROOT_DIR" log --oneline -20 2>/dev/null || echo "")
DEPLOY_DATE=$(date +%Y%m%d)
DEPLOY_TIME=$(date '+%Y-%m-%d %H:%M:%S')

# ── 目录命名 ──
SEQ_PAD=$(printf "%03d" "$BUILD_SEQ")
RECORD_DIR="$ROOT_DIR/deploy/r${SEQ_PAD}-${DEPLOY_DATE}"
mkdir -p "$RECORD_DIR/sql" "$RECORD_DIR/verify" "$RECORD_DIR/artifacts"

# ── 检测 SQL 变更（git diff HEAD~1 vs HEAD） ──
detect_sql_changes() {
  local sql_changes
  sql_changes=$(git -C "$ROOT_DIR" diff --name-only HEAD~1 HEAD 2>/dev/null | grep -E '^sql/|^db/' || true)
  if [[ -n "$sql_changes" ]]; then
    echo "$sql_changes" | while read f; do
      local basename_f
      basename_f=$(basename "$f")
      local outfile="$RECORD_DIR/sql/${basename_f%.sql}.md"
      {
        echo "# SQL 变更: $basename_f"
        echo ""
        echo "- **文件**: \`$f\`"
        echo "- **部署**: $DEPLOY_TIME"
        echo "- **Git SHA**: $GIT_SHA"
        echo ""
        echo "## 变更内容"
        echo ""
        echo '```diff'
        git -C "$ROOT_DIR" diff HEAD~1 HEAD -- "$f" 2>/dev/null | head -100
        echo '```'
        echo ""
        echo "## 影响说明"
        echo ""
        echo "- 待补充（请人工填写）"
      } > "$outfile"
      echo "  SQL 变更: $f → sql/$(basename "$outfile")"
    done
  fi
}

# ── 检测前端变更 ──
detect_frontend_changes() {
  local fe_changes
  fe_changes=$(git -C "$ROOT_DIR" diff --name-only HEAD~1 HEAD 2>/dev/null | grep -E '^web/|^web-vue/' || true)
  if [[ -n "$fe_changes" ]]; then
    echo "$fe_changes" | head -30 > "$RECORD_DIR/verify/frontend-changes.txt"
  fi
}

# ── 生成 README.md ──
generate_readme() {
  cat > "$RECORD_DIR/README.md" <<EOF
# 部署记录 r${SEQ_PAD}

| 字段 | 值 |
|------|-----|
| 部署序号 | **${BUILD_SEQ}** |
| 部署日期 | **${DEPLOY_DATE}** |
| Git Tag | **${GIT_TAG}** |
| Git SHA | **${GIT_SHA}** |
| 部署时间 | **${DEPLOY_TIME}** |
| 操作人员 | **$(whoami)** |
| 状态 | **待部署** → 更新为 ✅ 成功 / ❌ 失败 |

## 前置条件

- [ ] Go 编译环境就绪
- [ ] Docker 运行中
- [ ] 本地 PG/Redis 可访问（仅本地部署）
- [ ] SSH 到 184 (14.103.112.184:25022) 就绪（仅 184 部署）
- [ ] K8s context 正确

## 变更摘要

> 待补充（从 CHANGELOG.md 获取摘要）
EOF
}

# ── 生成 CHANGELOG.md（当前发布说明） ──
generate_changelog() {
  cat > "$RECORD_DIR/CHANGELOG.md" <<EOF
# 发布说明 r${SEQ_PAD} ($DEPLOY_DATE)

## 本次变更

$(git -C "$ROOT_DIR" log --oneline -5 2>/dev/null || echo "(git log 不可用)")

## 变更详情

> 请根据实际代码变更补充说明

### 后端
- 待补充

### 前端
- 待补充

### 数据库
- 待补充

### 配置
- 待补充

---

## 回滚方案

\`\`\`bash
# 184 回滚
ssh -p 25022 root@14.103.112.184 "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test"

# 本地回滚
git checkout HEAD~1
./scripts/local-up.sh --rebuild
\`\`\`
EOF
}

# ── 生成 plan.md ──
generate_plan() {
  cat > "$RECORD_DIR/plan.md" <<EOF
# 部署计划 r${SEQ_PAD} ($DEPLOY_DATE)

## 步骤

### Phase 1: 本地构建与验证

| # | 步骤 | 命令 |
|---|------|------|
| 1 | 编译后端 | \`go build -o gateway ./cmd/gateway\` |
| 2 | 构建镜像 | \`docker build -t kx-llm-gateway-go:latest .\` |
| 3 | 启动本地栈 | \`./scripts/local-up.sh --rebuild\` |
| 4 | smoke 测试 | \`./scripts/local-r112-smoke.sh\` |
| 5 | TC 运行时测试 | \`./scripts/test-runtime-tc.sh --all\` |
| 6 | 分区验证 | \`./scripts/verify_partition_architecture.sh\` |
| 7 | 列存验证 | \`./scripts/verify-columnar-sync.sh\` |

### Phase 2: 184 部署

| # | 步骤 | 命令 |
|---|------|------|
| 8 | 标准部署 | \`./deploy-184.sh\` |
| 9 | DB migration | \`./deploy-184.sh -m\` |
| 10 | 健康检查 | \`curl http://14.103.112.184:30080/health\` |
| 11 | 部署后验证 | \`./deploy/verify.sh --env 184\` |

### Phase 3: 验证确认

| # | 验证项 | 预期 |
|---|--------|------|
| 12 | health 端点 | 200 OK，含版本信息 |
| 13 | Pod 状态 | Running，READY 1/1 |
| 14 | 分区完整性 | 所有分区在预期位置 |
| 15 | 前端可访问 | 页面加载正常 |
| 16 | API 响应 | chat completion 正常回包 |

## 监控关注点

- 部署后 5 分钟内 Pod 重启次数
- health endpoint 响应时间
- DB 连接池使用率
- 错误日志（kubectl logs --tail=200 | grep -i error）
EOF
}

# ── 主流程 ──
main() {
  echo ""
  echo "════════════════════════════════════════════"
  echo "  初始化部署记录"
  echo "════════════════════════════════════════════"
  echo ""
  echo "  Build Seq:    ${BUILD_SEQ}"
  echo "  Git Tag:      ${GIT_TAG}"
  echo "  Git SHA:      ${GIT_SHA}"
  echo "  日  期:      ${DEPLOY_DATE}"
  echo "  目  录:      ${RECORD_DIR}"
  echo ""

  mkdir -p "$RECORD_DIR/sql" "$RECORD_DIR/verify" "$RECORD_DIR/artifacts"

  detect_sql_changes
  detect_frontend_changes
  generate_readme
  generate_changelog
  generate_plan

  # 写入 artifacts
  echo "$DEPLOY_TIME" > "$RECORD_DIR/artifacts/deploy-time.txt"
  echo "$GIT_SHA" > "$RECORD_DIR/artifacts/git-sha.txt"
  echo "$GIT_TAG" > "$RECORD_DIR/artifacts/git-tag.txt"
  echo "$BUILD_SEQ" > "$RECORD_DIR/artifacts/build-seq.txt"

  echo ""
  echo "✅ 部署记录目录已初始化"
  echo "   ${RECORD_DIR}"
  echo ""
  echo "  请补充:"
  echo "    - CHANGELOG.md 中的变更详情"
  echo "    - sql/*.md 中的影响说明"
  echo "  部署后自动写入:"
  echo "    - verify/ 目录下的验证结果"
  echo ""
}

main

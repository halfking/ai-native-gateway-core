#!/usr/bin/env bash
# tests/48h-audit/scripts/new-domain.sh
# 按模板新建一个域目录。
# Usage:
#   bash tests/48h-audit/scripts/new-domain.sh D18 --name=foo-bar --chinese="新审计域"

set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/tests/48h-audit"
NAME=""
CHINESE=""
DOMAIN=""

for arg in "$@"; do
  case "$arg" in
    --name=*) NAME="${arg#*=}" ;;
    --chinese=*) CHINESE="${arg#*=}" ;;
    D[0-9][0-9]) DOMAIN="$arg" ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done

if [[ -z "$DOMAIN" || -z "$NAME" ]]; then
  echo "Usage: $0 D<NN> --name=<kebab> [--chinese=<中文>]" >&2
  exit 1
fi

TARGET="$DIR/${DOMAIN}-${NAME}"
if [[ -d "$TARGET" ]]; then
  echo "exists: $TARGET" >&2
  exit 1
fi

mkdir -p "$TARGET"/{business,data,stress,safety,scripts,reports/history}

# plan.md 用模板填充
PLAN="$TARGET/plan.md"
cat > "$PLAN" <<EOF
# ${DOMAIN} — ${CHINESE:-<待填中文名>}

> 域知识库：docs/audit/playbook/domains/${DOMAIN}-*.md  
> 48h 改动面（截至 R<N>）：<待填>  
> 状态：草稿

## 1. 审计要点（来自 playbook 域文档）

- 待填 1
- 待填 2
- 待填 3

## 2. 业务测试（business/）

- [ ] B-01：<测试名> —— <断言>

## 3. 数据测试（data/）

- [ ] D-01：<字段序列化 golden>

## 4. 压力测试（stress/）

- [ ] S-01：<吞吐 ≥ X req/s>

## 5. 安全测试（safety/）

- [ ] SF-01：<-race 无数据竞争>

## 6. 验收门

\`\`\`bash
go build ./...
go vet ./...
go test -race ./tests/48h-audit/${DOMAIN}-${NAME}/... -timeout 60s
bash tests/48h-audit/${DOMAIN}-${NAME}/scripts/run.sh
\`\`\`

## 7. 与方案文档的对齐

- RFC: docs/...
EOF

# 各分类 README 占位
for cat in business data stress safety; do
  echo "# ${cat} 测试占位" > "$TARGET/$cat/README.md"
done

# reports/latest.md 占位
cat > "$TARGET/reports/latest.md" <<EOF
# ${DOMAIN} · 待留档

> 暂无审计结论。
EOF

# reports/INDEX.md
cat > "$TARGET/reports/INDEX.md" <<EOF
# ${DOMAIN} · 历史报告索引

| 时间 | 轮次 | 状态 | 报告 |
|---|---|---|---|
EOF

# scripts/run.sh
cat > "$TARGET/scripts/run.sh" <<'EOF'
#!/usr/bin/env bash
# 本域一键跑 4 类
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOMAIN="$(basename "$DIR")"
echo "[run] domain=$DOMAIN"
cd "$DIR"
go test -race -timeout 60s ./business/... 2>&1 || true
go test -race -timeout 60s ./data/...    2>&1 || true
go test -race -timeout 120s ./stress/...  2>&1 || true
go test -race -timeout 60s ./safety/...  2>&1 || true
echo "[run] done"
EOF
chmod +x "$TARGET/scripts/run.sh"

echo "created: $TARGET"
echo "TODO:"
echo "  1. fill plan.md §1-§7"
echo "  2. write tests in business/data/stress/safety"
echo "  3. write reports/latest.md"
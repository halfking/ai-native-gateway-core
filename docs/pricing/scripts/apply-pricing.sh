#!/usr/bin/env bash
# apply-pricing.sh — 一键双表写入 (pricing_plans 源真值 + model_offers 快照)
# ⚠️  生产数据库操作!  需要 SSH 184 + admin password from k8s secret
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DOCS_DIR="$REPO_ROOT/services/llm-gateway-go/docs/pricing"

# 凭据
SSH_PASS="${K8S_SSH_PASSWORD:-Kaixuan2025&9900#}"
SSH_HOST="${SSH_HOST:-root@14.103.112.184}"
POSTGRES_POD="llm-gateway-pg-58cbbc4559-qq2rh"
PSQL_USER="llm_gateway"
PSQL_DB="llm_gateway"
LLMGO_URL="https://llmgo.kxpms.cn"

# 0. 凭据检查
if ! command -v sshpass >/dev/null; then
  echo "❌ sshpass not installed. Run: brew install sshpass"
  exit 1
fi

echo "═══════════════════════════════════════════════════════════════"
echo "  1. 跑 DDL 迁移 (plan_meta + billing_mode CHECK)"
echo "═══════════════════════════════════════════════════════════════"
SSHPASS="$SSH_PASS" sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
  "kubectl -n pms-test cp $REPO_ROOT/services/llm-gateway/sql/2026_06_12_pricing_billing_mode_meta.sql /tmp/migrate.sql && \
   kubectl -n pms-test exec -i $POSTGRES_POD -- psql -U $PSQL_USER -d $PSQL_DB -f /tmp/migrate.sql"

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  2. 写 pricing_plans 源真值 (直连 psql)"
echo "═══════════════════════════════════════════════════════════════"
SSHPASS="$SSH_PASS" sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
  "kubectl -n pms-test cp $DOCS_DIR/2026-06-12-pricing-plans.sql /tmp/pricing-plans.sql && \
   kubectl -n pms-test exec -i $POSTGRES_POD -- psql -U $PSQL_USER -d $PSQL_DB -f /tmp/pricing-plans.sql"

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  3. 写 model_offers 快照 (Go admin API)"
echo "═══════════════════════════════════════════════════════════════"
ADMIN_PASS=$(SSHPASS="$SSH_PASS" sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
  "kubectl -n pms-test get secret llm-gateway-secret -o jsonpath='{.data.admin-password}' | base64 -d")

API_KEY=$(curl -sk -X POST "$LLMGO_URL/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" | jq -r .api_key)

if [ -z "$API_KEY" ] || [ "$API_KEY" = "null" ]; then
  echo "❌ 登录失败"
  exit 1
fi

echo "  API key: ${API_KEY:0:12}****"

curl -sk -X POST "$LLMGO_URL/api/pricing/import" \
  -H "Authorization: Bearer $API_KEY" \
  -F "file=@$DOCS_DIR/2026-06-12-llm-pricing.csv" | jq .

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  4. 写 plan_meta (JSONB 字段, 直连 psql)"
echo "═══════════════════════════════════════════════════════════════"
# 提取 plan_meta 数据,生成 plan_meta UPDATE SQL
python3 "$DOCS_DIR/scripts/build-plan-meta-sql.py" "$DOCS_DIR/2026-06-12-credentials-with-plan-type.csv" \
  > "$DOCS_DIR/2026-06-12-plan-meta.sql"

SSHPASS="$SSH_PASS" sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
  "kubectl -n pms-test cp $DOCS_DIR/2026-06-12-plan-meta.sql /tmp/plan-meta.sql && \
   kubectl -n pms-test exec -i $POSTGRES_POD -- psql -U $PSQL_USER -d $PSQL_DB -f /tmp/plan-meta.sql"

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  5. 校验"
echo "═══════════════════════════════════════════════════════════════"
curl -sk -H "Authorization: Bearer $API_KEY" \
  "$LLMGO_URL/api/pricing/summary" | jq .

echo ""
echo "✅ Done!"

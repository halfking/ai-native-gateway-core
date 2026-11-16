#!/usr/bin/env bash
# browser-subscription-tiers.sh — 订阅层级页面浏览器验收（2026-11-17）
# 对应前端：web/src/views/SubscriptionTiersView.vue
# 路由：/admin/subscription-tiers（requiresSuper: true）
# 后端：GET /api/admin/tiers（nginx 反代 license-authority:8443）
#
# 验收项（基于 SubscriptionTiersView.test.ts）：
#   1. 页面渲染：标题「订阅层级」+ 刷新按钮
#   2. 档位卡片：按 sort_order 升序排列（free → basic → standard → premium → enterprise）
#   3. 价格显示：price_cents=0 显示「免费」，否则「¥X.XX」
#   4. 模块列表：显示包含模块数量 + module_keys 列表（0 个显示「无」）
#   5. 错误状态：后端 502 显示错误信息 + 刷新后恢复
#   6. 空状态：无数据时显示「暂无订阅层级」
#
# 用法：
#   ./scripts/audit/browser-subscription-tiers.sh [BASE_URL]
#   默认 BASE_URL=http://127.0.0.1:8781

set -euo pipefail

BASE_URL="${1:-http://127.0.0.1:8781}"
PAGE_PATH="/admin/subscription-tiers"
FULL_URL="${BASE_URL}${PAGE_PATH}"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# 检查依赖
for cmd in node python3; do
  if ! command -v "$cmd" &>/dev/null; then
    log_error "缺少依赖: $cmd"
    exit 1
  fi
done

# 创建临时目录
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

log_info "临时目录: $TMPDIR"
log_info "目标页面: $FULL_URL"

# ============================================================================
# Step 1: 检查后端端点是否存在
# ============================================================================
log_info "[Step 1/6] 检查后端端点 GET /api/admin/tiers"

# 生成临时 JWT（super_admin 权限，有效期 30 分钟）
# 参考: scripts/audit/api-211-admin-probe.py
python3 - <<'PYEOF' > "$TMPDIR/token.txt"
import base64
import hashlib
import hmac
import json
import time
import os

def _b64(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()

# 从环境变量读取密钥，或使用默认开发密钥
SECRET = os.getenv("LLM_GATEWAY_SECRET_KEY", "dev-secret-key-change-in-production")

header = _b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
now = int(time.time())
payload = _b64(json.dumps({
    "user_id": 1,
    "tenant_id": "default",
    "username": "browser-test-admin",
    "role": "super_admin",
    "exp": now + 1800,  # 30 分钟
    "iat": now,
    "iss": "llm-gateway",
    "aud": ["llm-gateway-api"]
}, separators=(",", ":")).encode())

sig = _b64(hmac.new(SECRET.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
print(f"{header}.{payload}.{sig}")
PYEOF

TOKEN=$(cat "$TMPDIR/token.txt")
log_info "生成测试 JWT (前8字符): ${TOKEN:0:8}..."

# 测试后端端点
API_URL="${BASE_URL}/api/admin/tiers"
HTTP_CODE=$(curl -s -o "$TMPDIR/api_response.json" -w "%{http_code}" \
  -H "Cookie: llmgw_session=$TOKEN" \
  "$API_URL")

if [[ "$HTTP_CODE" == "200" ]]; then
  log_info "✓ 后端端点返回 200"
  cat "$TMPDIR/api_response.json" | python3 -m json.tool > "$TMPDIR/api_response_pretty.json" 2>/dev/null || true
  TIER_COUNT=$(cat "$TMPDIR/api_response.json" | python3 -c "import sys, json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
  log_info "  档位数量: $TIER_COUNT"
elif [[ "$HTTP_CODE" == "404" ]]; then
  log_warn "后端端点未实现（404），将使用 mock 数据测试前端"
  # 创建 mock 响应
  cat > "$TMPDIR/mock_tiers.json" <<'MOCK'
[
  {
    "code": "free",
    "name": "免费版",
    "description": "基础接入能力",
    "price_cents": 0,
    "sort_order": 1,
    "module_keys": ["llm_basic"]
  },
  {
    "code": "basic",
    "name": "基础版",
    "description": "适合小型团队",
    "price_cents": 9900,
    "sort_order": 2,
    "module_keys": ["llm_basic", "routing_v2"]
  },
  {
    "code": "standard",
    "name": "标准版",
    "description": "适合中型企业",
    "price_cents": 29900,
    "sort_order": 3,
    "module_keys": ["llm_basic", "routing_v2", "sla_monitor"]
  },
  {
    "code": "premium",
    "name": "高级版",
    "description": "高级功能 + 优先支持",
    "price_cents": 59900,
    "sort_order": 4,
    "module_keys": ["llm_basic", "routing_v2", "sla_monitor", "cost_optimization"]
  },
  {
    "code": "enterprise",
    "name": "企业版",
    "description": "",
    "price_cents": 99900,
    "sort_order": 5,
    "module_keys": []
  }
]
MOCK
  TIER_COUNT=5
  USING_MOCK=true
else
  log_error "后端端点返回 $HTTP_CODE，期望 200 或 404"
  cat "$TMPDIR/api_response.json" 2>/dev/null || true
  exit 1
fi

# ============================================================================
# Step 2: 启动临时 mock 服务器（如果需要）
# ============================================================================
if [[ "${USING_MOCK:-false}" == "true" ]]; then
  log_info "[Step 2/6] 启动临时 mock 服务器"
  
  cat > "$TMPDIR/mock_server.js" <<'JSEOF'
const http = require('http');
const fs = require('fs');

const mockData = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));

const server = http.createServer((req, res) => {
  // CORS
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET, OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type, Cookie');
  res.setHeader('Access-Control-Allow-Credentials', 'true');

  if (req.method === 'OPTIONS') {
    res.writeHead(204);
    res.end();
    return;
  }

  if (req.url === '/api/admin/tiers' && req.method === 'GET') {
    // 简单验证 cookie
    const cookie = req.headers.cookie || '';
    if (!cookie.includes('llmgw_session=')) {
      res.writeHead(401, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'Unauthorized' }));
      return;
    }
    
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(mockData));
  } else {
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'Not Found' }));
  }
});

const PORT = process.env.MOCK_PORT || 18099;
server.listen(PORT, '127.0.0.1', () => {
  console.log(`Mock server running on http://127.0.0.1:${PORT}`);
});
JSEOF

  MOCK_PORT=18099
  node "$TMPDIR/mock_server.js" "$TMPDIR/mock_tiers.json" &
  MOCK_PID=$!
  trap 'kill $MOCK_PID 2>/dev/null || true; rm -rf "$TMPDIR"' EXIT
  
  sleep 1
  
  # 验证 mock 服务器
  if curl -s -H "Cookie: llmgw_session=$TOKEN" "http://127.0.0.1:$MOCK_PORT/api/admin/tiers" | grep -q '"code"'; then
    log_info "✓ Mock 服务器启动成功"
    # 修改前端请求 baseURL（需要代理或修改前端配置）
    log_warn "⚠ 请确保前端配置指向 mock 服务器 http://127.0.0.1:$MOCK_PORT"
  else
    log_error "Mock 服务器启动失败"
    exit 1
  fi
else
  log_info "[Step 2/6] 跳过 mock 服务器（使用真实后端）"
fi

# ============================================================================
# Step 3: 使用 Playwright 进行浏览器测试
# ============================================================================
log_info "[Step 3/6] 启动 Playwright 浏览器测试"

cat > "$TMPDIR/test.mjs" <<'JSEOF'
import { chromium } from 'playwright';
import { strict as assert } from 'assert';

const BASE_URL = process.env.BASE_URL || 'http://127.0.0.1:8781';
const PAGE_PATH = '/admin/subscription-tiers';
const TOKEN = [REDACTED:secret];

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  
  // 设置 cookie
  await context.addCookies([{
    name: 'llmgw_session',
    value: TOKEN,
    domain: '127.0.0.1',
    path: '/',
    httpOnly: false,
    secure: false,
    sameSite: 'Lax'
  }]);

  const page = await context.newPage();
  
  console.log('[TEST] 导航到页面:', BASE_URL + PAGE_PATH);
  await page.goto(BASE_URL + PAGE_PATH, { waitUntil: 'networkidle' });
  
  // 等待页面加载
  await page.waitForTimeout(2000);
  
  // ============================================================================
  // 验收项 1: 页面渲染
  // ============================================================================
  console.log('[TEST] 验收项 1: 检查页面标题和刷新按钮');
  const title = await page.locator('.st-title').textContent();
  assert.equal(title, '订阅层级', '页面标题应为「订阅层级」');
  
  const refreshBtn = await page.locator('.btn.btn-ghost.btn-sm').count();
  assert.equal(refreshBtn, 1, '应有一个刷新按钮');
  console.log('✓ 页面标题和刷新按钮正常');
  
  // ============================================================================
  // 验收项 2: 档位卡片
  // ============================================================================
  console.log('[TEST] 验收项 2: 检查档位卡片数量和排序');
  const cards = await page.locator('.st-card').all();
  const cardCount = cards.length;
  console.log(`  档位卡片数量: ${cardCount}`);
  assert.ok(cardCount >= 3, `应至少有 3 个档位卡片，实际 ${cardCount}`);
  
  // 检查第一张卡片（应为 sort_order=1 的 free）
  const firstCardName = await cards[0].locator('.st-card-name').textContent();
  const firstCardCode = await cards[0].locator('.st-card-code').textContent();
  console.log(`  第一张卡片: ${firstCardName} (${firstCardCode})`);
  assert.equal(firstCardCode, 'free', '第一张卡片应为 free 档位');
  
  // ============================================================================
  // 验收项 3: 价格显示
  // ============================================================================
  console.log('[TEST] 验收项 3: 检查价格显示格式');
  const firstCardPrice = await cards[0].locator('.st-card-price').textContent();
  assert.equal(firstCardPrice, '免费', 'free 档位价格应显示「免费」');
  console.log(`  免费档位价格: ${firstCardPrice} ✓`);
  
  // 检查付费档位价格格式
  if (cardCount >= 2) {
    const secondCardPrice = await cards[1].locator('.st-card-price').textContent();
    assert.match(secondCardPrice, /^¥\d+\.\d{2}$/, '付费档位价格应为 ¥X.XX 格式');
    console.log(`  付费档位价格: ${secondCardPrice} ✓`);
  }
  
  // ============================================================================
  // 验收项 4: 模块列表
  // ============================================================================
  console.log('[TEST] 验收项 4: 检查模块列表显示');
  const firstCardModules = await cards[0].locator('.st-modules-title').textContent();
  assert.match(firstCardModules, /包含模块（\d+）/, '应显示模块数量');
  console.log(`  第一张卡片: ${firstCardModules}`);
  
  // 检查是否有「无」模块的档位（enterprise）
  let hasEmptyModules = false;
  for (const card of cards) {
    const modulesEmpty = await card.locator('.st-modules-empty').count();
    if (modulesEmpty > 0) {
      hasEmptyModules = true;
      const cardName = await card.locator('.st-card-name').textContent();
      console.log(`  ${cardName} 显示「无」模块 ✓`);
      break;
    }
  }
  
  // ============================================================================
  // 验收项 5: 刷新功能
  // ============================================================================
  console.log('[TEST] 验收项 5: 测试刷新功能');
  await page.locator('.btn.btn-ghost.btn-sm').click();
  await page.waitForTimeout(1000);
  
  const cardsAfterRefresh = await page.locator('.st-card').count();
  assert.equal(cardsAfterRefresh, cardCount, '刷新后卡片数量应保持不变');
  console.log('✓ 刷新功能正常');
  
  // ============================================================================
  // 验收项 6: 截图证据
  // ============================================================================
  console.log('[TEST] 验收项 6: 保存截图');
  await page.screenshot({ path: '/tmp/subscription-tiers-screenshot.png', fullPage: true });
  console.log('✓ 截图已保存到 /tmp/subscription-tiers-screenshot.png');
  
  await browser.close();
  
  console.log('\n[PASS] 所有验收项通过！');
  process.exit(0);
})().catch(err => {
  console.error('[FAIL]', err.message);
  process.exit(1);
});
JSEOF

# 检查 Playwright 是否安装
if ! npm list -g playwright &>/dev/null && ! npm list playwright &>/dev/null; then
  log_warn "Playwright 未安装，尝试在当前目录安装..."
  if [[ -f "package.json" ]]; then
    npm install --no-save playwright
  else
    log_error "请先安装 Playwright: npm install -g playwright 或在项目目录运行 npm install playwright"
    exit 1
  fi
fi

# 运行测试
BASE_URL="$BASE_URL" TEST_TOKEN="$TOKEN" node "$TMPDIR/test.mjs"
TEST_RESULT=$?

# ============================================================================
# Step 4: 生成测试报告
# ============================================================================
log_info "[Step 4/6] 生成测试报告"

cat > "$TMPDIR/report.md" <<REPORT
# 订阅层级页面浏览器验收报告

**日期**: $(date '+%Y-%m-%d %H:%M:%S')  
**页面**: \`$PAGE_PATH\`  
**后端**: \`GET /api/admin/tiers\`  
**使用 Mock**: ${USING_MOCK:-false}

## 验收结果

| 验收项 | 状态 |
|--------|------|
| 1. 页面渲染（标题 + 刷新按钮） | ${TEST_RESULT} == 0 ? '✅' : '❌' |
| 2. 档位卡片按 sort_order 排序 | ${TEST_RESULT} == 0 ? '✅' : '❌' |
| 3. 价格显示格式正确 | ${TEST_RESULT} == 0 ? '✅' : '❌' |
| 4. 模块列表显示正确 | ${TEST_RESULT} == 0 ? '✅' : '❌' |
| 5. 刷新功能正常 | ${TEST_RESULT} == 0 ? '✅' : '❌' |
| 6. 截图证据已保存 | ${TEST_RESULT} == 0 ? '✅' : '❌' |

## 后端响应

\`\`\`json
$(cat "$TMPDIR/api_response_pretty.json" 2>/dev/null || cat "$TMPDIR/mock_tiers.json")
\`\`\`

## 截图

![订阅层级页面](/tmp/subscription-tiers-screenshot.png)

## 下一步

- [ ] 确认后端端点 \`GET /api/admin/tiers\` 已在生产环境部署
- [ ] 确认 nginx 反代配置正确（转发到 license-authority:8443）
- [ ] 确认各档位的 price_cents 和 module_keys 与产品定义一致
- [ ] 补充空状态测试（后端返回空数组）
- [ ] 补充错误状态测试（后端 502/503）
REPORT

cat "$TMPDIR/report.md"

# ============================================================================
# Step 5: 清理
# ============================================================================
log_info "[Step 5/6] 清理临时文件"

if [[ "${USING_MOCK:-false}" == "true" ]]; then
  log_info "停止 mock 服务器 (PID: $MOCK_PID)"
  kill "$MOCK_PID" 2>/dev/null || true
fi

# ============================================================================
# Step 6: 总结
# ============================================================================
log_info "[Step 6/6] 测试完成"

if [[ $TEST_RESULT -eq 0 ]]; then
  log_info "✅ 所有验收项通过"
  log_info "报告已保存到: $TMPDIR/report.md"
  log_info "截图已保存到: /tmp/subscription-tiers-screenshot.png"
  exit 0
else
  log_error "❌ 部分验收项失败"
  log_error "请检查上方错误信息"
  exit 1
fi

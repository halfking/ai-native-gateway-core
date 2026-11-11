# llm-gateway-go 综合测试计划

> **制定日期**：2026-11-11  
> **目标**：全面验证应用功能特性，包括UI测试、并发压力测试、皮肤主题测试

---

## 一、应用功能特性总结

### 1.1 核心功能模块

#### 数据平面（Data Plane）
- **协议标准化**：OpenAI / Anthropic / Gemini / Responses API兼容
- **智能两层路由**：
  - L1：模型选择（任务分类 → 4维度通道-质量评分）
  - L2：凭据选择（分层回退 → 计费轮次 → P2C评分 → 执行/熔断）
- **会话治理**：粘性会话、全内容重放、自动提示词压缩、4层数据生命周期
- **流式传输**：SSE流式中继，带完整性检查
- **MCP工具网关**：内置MCP服务网关（JSON-RPC数据平面 + 控制平面）

#### 控制平面（Control Plane）
- **多租户管理**：PostgreSQL RLS隔离90+表，按租户配额、计量、MaaS计费
- **凭据管理**：多凭据池、指纹伪装、自适应探测、自动熔断
- **模型可信度**：被动时序指纹 + 主动探测 + 对等评分，检测上游模型替换
- **可观测性**：实时请求流、路由分析（热力图+桑基图）、成本追踪、OTel + Prometheus
- **运营定价**：版本化模型定价、套餐/加油包/优惠券、市场价格情报管道

#### 管理后台（Admin UI）
- **仪表盘**：实时请求流、用量成本概览、供应商费用结算
- **路由全景**：两层路由可视化、任务×模型热力图、桑基流图
- **凭据监控**：2D可用性矩阵、P95延迟、滑动窗口成功率、并发槽使用
- **请求会话详情**：Q&A重放、调度瀑布图、路由重试轨迹、压缩/脱敏记录
- **配置管理**：系统设置、模块管理、Agent注册表、审批、敏感信息规则、输出合规
- **数据运维**：用量分析、运营面板、订阅层级、会话轮次、请求注册表、对账、审计

### 1.2 技术架构特性

- **单进程架构**：数据平面 + 控制平面 + 管理UI在单个mux（h2c: HTTP/1.1 + HTTP/2）
- **双模式存储**：
  - **full模式**：PostgreSQL + Redis（生产默认）
  - **lite模式**：SQLite + 本地文件 + 进程内KV（零外部依赖）
- **后台工作器**：~80个goroutine（探测/清理/统计/分区维护）
- **前端架构**：Vue 3 + TypeScript + Element Plus + ECharts
- **移动端**：独立mobile frontend（Vue 3 + TypeScript）

---

## 二、测试计划

### 2.1 Maestro UI功能测试

#### 2.1.1 现有测试覆盖

根据 `maestro/README.md`，现有112条路由已通过10个flow覆盖：

| Flow文件 | 覆盖范围 | 页数 | 需登录 |
|---------|---------|------|--------|
| 01-public-guest.yaml | 公开页/访客面 | 12 | 否 |
| 02-overview-live.yaml | 总览/实时拓扑/接入引导 | 6 | 是 |
| 03-providers-models.yaml | 供应商/模型/定价 | 9 | 是 |
| 04-tenants-portal.yaml | 租户/租户自助门户 | 13 | 是 |
| 05-routing.yaml | 路由域 | 22 | 是 |
| 06-keys-requests.yaml | 密钥/请求日志/会话分析 | 9 | 是 |
| 07-admin-config.yaml | 后台配置域 | 13 | 是 |
| 08-admin-data-ops.yaml | 后台数据/用量/审计域 | 19 | 是 |
| 09-ops-maintain.yaml | 运维中心外跳/maintain兜底 | 6 | 是 |
| 90-dev-only.yaml | 仅dev构建路由 | 3 | 否 |

**状态**：2026-10-28（R176）已真实跑过，发现并修正9处资产错误 + 2处产品缺陷。

#### 2.1.2 测试执行步骤

##### A. 本地Docker环境测试

```bash
# 1. 确保本地网关运行在 http://127.0.0.1:8782
# 2. 设置环境变量
export JAVA_HOME="$HOME/kaixuan/jdk17/Contents/Home"
export PATH="$JAVA_HOME/bin:$HOME/kaixuan/bin:$PATH"

# 3. 执行所有功能flow（不含dev-only）
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
bash maestro/run-local.sh

# 4. 连dev-only一起跑
bash maestro/run-local.sh --with-dev

# 5. 分析慢页面（阈值2秒）
node maestro/tools/slow-pages.mjs --artifacts maestro/reports/<timestamp> --threshold 2000
```

##### B. 211环境测试

```bash
# 1. 修改环境变量指向211
# 在maestro/run-local.sh中修改BASE_URL或通过-e参数传递
# BASE_URL=http://211服务器地址:端口

# 2. 执行相同的测试流程
bash maestro/run-local.sh
bash maestro/run-local.sh --with-dev
node maestro/tools/slow-pages.mjs --artifacts maestro/reports/<timestamp> --threshold 2000
```

### 2.2 皮肤主题测试

#### 2.2.1 测试目标

验证明暗两种皮肤下：
- 配色正常，无明显大块反色
- 菜单配色与皮肤一致
- 风格与RedClaw和llm-gateway-go一致

#### 2.2.2 测试方案

创建专用的Maestro flow来切换主题并截图验证：

```yaml
# maestro/flows/99-theme-validation.yaml
appId: ${BASE_URL}
---
# 登录
- runFlow:
    file: _shared/login.yaml
    env:
      ADMIN_USER: ${ADMIN_USER}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}

# 测试亮色主题
- tapOn: "主题切换按钮"  # 需要确认实际元素
- extendedWaitUntil:
    visible: "亮色主题指示器"
    timeout: 2000
- takeScreenshot: screenshots/light-theme-dashboard.png
- openLink: "${BASE_URL}/routing-v2"
- takeScreenshot: screenshots/light-theme-routing.png
# ... 关键页面截图

# 切换到暗色主题
- tapOn: "主题切换按钮"
- extendedWaitUntil:
    visible: "暗色主题指示器"
    timeout: 2000
- takeScreenshot: screenshots/dark-theme-dashboard.png
- openLink: "${BASE_URL}/routing-v2"
- takeScreenshot: screenshots/dark-theme-routing.png
# ... 关键页面截图
```

#### 2.2.3 关键页面清单

需要在两种主题下截图验证的页面：
1. 仪表盘 `/dashboard`
2. 实时拓扑 `/live`
3. 路由全景 `/routing-v2`
4. 凭据监控 `/routing-v2/credentials`
5. 模型管理 `/models`
6. 供应商管理 `/providers`
7. 租户管理 `/tenants`
8. API密钥 `/keys`
9. 请求日志 `/request-logs`
10. 系统设置 `/admin/settings`

### 2.3 API功能测试

#### 2.3.1 数据平面API测试

```bash
# 测试OpenAI兼容接口
curl -X POST http://127.0.0.1:8782/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <api_key>" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false
  }'

# 测试流式接口
curl -X POST http://127.0.0.1:8782/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <api_key>" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'

# 测试MCP接口
curl -X POST http://127.0.0.1:8782/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "tools/list",
    "params": {},
    "id": 1
  }'
```

#### 2.3.2 控制平面API测试

```bash
# 测试认证
curl -X POST http://127.0.0.1:8782/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{
    "username": "admin",
    "password": "password"
  }'

# 测试供应商管理
curl -X GET http://127.0.0.1:8782/api/providers \
  -H "Cookie: llmgw_session=<session_cookie>"

# 测试模型目录
curl -X GET http://127.0.0.1:8782/api/models \
  -H "Cookie: llmgw_session=<session_cookie>"

# 测试请求日志
curl -X GET http://127.0.0.1:8782/api/request-logs \
  -H "Cookie: llmgw_session=<session_cookie>"
```

### 2.4 并发压力测试

#### 2.4.1 工具选择

使用以下工具进行并发测试：
- **k6**：现代负载测试工具，支持JavaScript脚本
- **hey**：简单的HTTP负载生成器
- **wrk**：高性能HTTP基准测试工具

#### 2.4.2 测试场景

##### 场景1：常规API调用压力测试

```javascript
// k6-scripts/api-load-test.js
import http from 'k6/http';
import { check, sleep } from 'k6';

export let options = {
  stages: [
    { duration: '2m', target: 100 },   // 2分钟内升到100并发用户
    { duration: '5m', target: 100 },   // 维持100并发5分钟
    { duration: '2m', target: 200 },   // 2分钟内升到200并发
    { duration: '5m', target: 200 },   // 维持200并发5分钟
    { duration: '2m', target: 0 },     // 2分钟内降到0
  ],
  thresholds: {
    http_req_duration: ['p(95)<500'],  // 95%请求在500ms内
    http_req_failed: ['rate<0.01'],    // 错误率<1%
  },
};

export default function () {
  const url = 'http://127.0.0.1:8782/v1/chat/completions';
  const payload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [{ role: 'user', content: 'Hello, how are you?' }],
    stream: false,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': 'Bearer <api_key>',
    },
  };

  const res = http.post(url, payload, params);
  
  check(res, {
    'status is 200': (r) => r.status === 200,
    'response time < 500ms': (r) => r.timings.duration < 500,
  });

  sleep(1);
}
```

运行测试：
```bash
k6 run k6-scripts/api-load-test.js
```

##### 场景2：流式接口压力测试

```javascript
// k6-scripts/streaming-load-test.js
import http from 'k6/http';
import { check } from 'k6';

export let options = {
  vus: 50,               // 50个虚拟用户
  duration: '5m',        // 持续5分钟
};

export default function () {
  const url = 'http://127.0.0.1:8782/v1/chat/completions';
  const payload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [{ role: 'user', content: 'Write a story' }],
    stream: true,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': 'Bearer <api_key>',
    },
  };

  const res = http.post(url, payload, params);
  
  check(res, {
    'status is 200': (r) => r.status === 200,
    'has SSE data': (r) => r.body.includes('data:'),
  });
}
```

##### 场景3：管理后台并发访问

```javascript
// k6-scripts/admin-load-test.js
import http from 'k6/http';
import { check, group } from 'k6';

export let options = {
  vus: 20,               // 20个并发管理员
  duration: '3m',        // 持续3分钟
};

const BASE_URL = 'http://127.0.0.1:8782';

export function setup() {
  // 登录获取session
  const loginRes = http.post(`${BASE_URL}/api/auth/token`, JSON.stringify({
    username: 'admin',
    password: 'password',
  }), {
    headers: { 'Content-Type': 'application/json' },
  });
  
  const cookies = loginRes.cookies;
  return { sessionCookie: cookies.llmgw_session[0].value };
}

export default function (data) {
  const headers = {
    'Cookie': `llmgw_session=${data.sessionCookie}`,
  };

  group('Dashboard APIs', function () {
    check(http.get(`${BASE_URL}/api/dashboard/stats`, { headers }), {
      'dashboard stats ok': (r) => r.status === 200,
    });
  });

  group('Request Logs', function () {
    check(http.get(`${BASE_URL}/api/request-logs?limit=50`, { headers }), {
      'request logs ok': (r) => r.status === 200,
    });
  });

  group('Providers', function () {
    check(http.get(`${BASE_URL}/api/providers`, { headers }), {
      'providers ok': (r) => r.status === 200,
    });
  });
}
```

##### 场景4：混合负载测试

```javascript
// k6-scripts/mixed-load-test.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { randomItem } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';

export let options = {
  stages: [
    { duration: '3m', target: 50 },
    { duration: '10m', target: 50 },
    { duration: '2m', target: 0 },
  ],
};

const BASE_URL = 'http://127.0.0.1:8782';
const API_KEYS = ['key1', 'key2', 'key3']; // 多个API key轮换

export default function () {
  const apiKey = randomItem(API_KEYS);
  
  // 80% API调用
  if (Math.random() < 0.8) {
    const res = http.post(`${BASE_URL}/v1/chat/completions`, JSON.stringify({
      model: randomItem(['gpt-3.5-turbo', 'gpt-4', 'claude-3-sonnet']),
      messages: [{ role: 'user', content: 'Test message' }],
    }), {
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${apiKey}`,
      },
    });
    check(res, { 'api call ok': (r) => r.status === 200 });
  } 
  // 20% 管理操作
  else {
    const res = http.get(`${BASE_URL}/api/models`);
    check(res, { 'admin call ok': (r) => r.status === 200 || r.status === 401 });
  }

  sleep(randomItem([0.5, 1, 1.5, 2]));
}
```

#### 2.4.3 性能基准

预期性能指标：
- **吞吐量**：≥1000 req/s（常规API调用）
- **响应时间**：
  - P50 < 100ms
  - P95 < 500ms
  - P99 < 1000ms
- **错误率**：< 1%
- **并发连接**：支持500+并发用户
- **资源使用**：
  - CPU < 80%
  - 内存 < 4GB
  - 数据库连接池 < 最大连接数的80%

### 2.5 多子代理并行测试

#### 2.5.1 测试脚本架构

创建并行测试框架，使用多个子代理同时执行不同测试任务：

```bash
# scripts/parallel-test-runner.sh
#!/bin/bash

# 并行测试运行器
# 用法: bash scripts/parallel-test-runner.sh [本地|211]

set -euo pipefail

ENVIRONMENT=${1:-本地}
BASE_DIR="/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go"
REPORTS_DIR="${BASE_DIR}/test-reports/parallel-$(date +%Y%m%d-%H%M%S)"

mkdir -p "${REPORTS_DIR}"

echo "=== 并行测试开始 ===" | tee "${REPORTS_DIR}/summary.log"
echo "环境: ${ENVIRONMENT}" | tee -a "${REPORTS_DIR}/summary.log"
echo "时间: $(date)" | tee -a "${REPORTS_DIR}/summary.log"
echo "" | tee -a "${REPORTS_DIR}/summary.log"

# 定义测试任务
declare -A TASKS=(
  ["maestro-public"]="bash maestro/run-local.sh --flow 01"
  ["maestro-overview"]="bash maestro/run-local.sh --flow 02"
  ["maestro-providers"]="bash maestro/run-local.sh --flow 03"
  ["maestro-tenants"]="bash maestro/run-local.sh --flow 04"
  ["maestro-routing"]="bash maestro/run-local.sh --flow 05"
  ["k6-api"]="k6 run k6-scripts/api-load-test.js"
  ["k6-streaming"]="k6 run k6-scripts/streaming-load-test.js"
  ["k6-admin"]="k6 run k6-scripts/admin-load-test.js"
)

# 并行执行所有任务
PIDS=()
for task_name in "${!TASKS[@]}"; do
  echo "启动任务: ${task_name}" | tee -a "${REPORTS_DIR}/summary.log"
  (
    ${TASKS[$task_name]} > "${REPORTS_DIR}/${task_name}.log" 2>&1
    echo $? > "${REPORTS_DIR}/${task_name}.exit"
  ) &
  PIDS+=($!)
done

# 等待所有任务完成
echo "" | tee -a "${REPORTS_DIR}/summary.log"
echo "等待所有任务完成..." | tee -a "${REPORTS_DIR}/summary.log"
for pid in "${PIDS[@]}"; do
  wait $pid
done

# 汇总结果
echo "" | tee -a "${REPORTS_DIR}/summary.log"
echo "=== 测试结果汇总 ===" | tee -a "${REPORTS_DIR}/summary.log"
FAILED=0
for task_name in "${!TASKS[@]}"; do
  exit_code=$(cat "${REPORTS_DIR}/${task_name}.exit")
  if [ "$exit_code" -eq 0 ]; then
    echo "✓ ${task_name}: 通过" | tee -a "${REPORTS_DIR}/summary.log"
  else
    echo "✗ ${task_name}: 失败 (退出码: ${exit_code})" | tee -a "${REPORTS_DIR}/summary.log"
    FAILED=$((FAILED + 1))
  fi
done

echo "" | tee -a "${REPORTS_DIR}/summary.log"
if [ $FAILED -eq 0 ]; then
  echo "所有测试通过！" | tee -a "${REPORTS_DIR}/summary.log"
  exit 0
else
  echo "有 ${FAILED} 个任务失败。" | tee -a "${REPORTS_DIR}/summary.log"
  exit 1
fi
```

#### 2.5.2 并行测试场景

1. **场景A：功能+性能混合**
   - Agent 1-5：运行5个Maestro flow
   - Agent 6-8：运行3个k6压力测试
   - 同时执行，验证系统在负载下的功能完整性

2. **场景B：多租户并发**
   - Agent 1-10：模拟10个不同租户的操作
   - 每个Agent使用不同的API key
   - 验证租户隔离和资源配额

3. **场景C：极限压力**
   - Agent 1-20：全部运行API负载测试
   - 目标：找到系统性能极限
   - 监控资源使用和错误率

---

## 三、测试执行清单

### 3.1 本地Docker环境

- [ ] 启动本地网关（127.0.0.1:8782）
- [ ] 执行Maestro UI测试（10个flow）
- [ ] 验证慢页面（阈值2秒）
- [ ] 执行皮肤主题测试（明/暗两种）
- [ ] 执行API功能测试
- [ ] 执行k6并发压力测试（4个场景）
- [ ] 执行并行测试（混合场景）
- [ ] 收集性能指标和日志

### 3.2 211生产环境

- [ ] 确认211网关地址和端口
- [ ] 执行Maestro UI测试
- [ ] 验证慢页面
- [ ] 执行皮肤主题测试
- [ ] 执行API功能测试
- [ ] 执行k6并发压力测试
- [ ] 执行并行测试
- [ ] 对比本地和211环境性能差异

### 3.3 测试报告

- [ ] UI功能测试报告（112条路由覆盖率）
- [ ] 皮肤主题验证报告（截图对比）
- [ ] API功能测试报告
- [ ] 并发压力测试报告（性能指标）
- [ ] 多代理并行测试报告
- [ ] 缺陷清单和修复建议

---

## 四、测试环境要求

### 4.1 本地环境

- macOS（当前环境）
- Docker Desktop（网关容器）
- Java 17+（Temurin 17.0.21）
- Maestro CLI 2.11.0
- k6 负载测试工具
- Node.js 16+（用于辅助脚本）
- Chrome浏览器（CDP模式）

### 4.2 211环境

- 网关服务可访问
- 测试账号和API key
- 网络连接稳定
- 监控工具（Prometheus/Grafana）

### 4.3 资源要求

- CPU：8核+
- 内存：16GB+
- 磁盘：50GB+可用空间
- 网络：100Mbps+带宽

---

## 五、风险和注意事项

### 5.1 测试风险

1. **数据污染**：压力测试可能产生大量测试数据
   - 缓解：使用专用测试租户和API key
   - 测试后清理数据

2. **资源耗尽**：极限压力测试可能导致服务崩溃
   - 缓解：逐步增加负载，设置资源监控告警
   - 准备回滚和恢复方案

3. **211环境影响**：生产环境测试可能影响真实用户
   - 缓解：选择低峰时段
   - 使用隔离的测试租户
   - 限制并发数量

### 5.2 测试注意事项

1. Maestro测试需要JDK 17环境变量
2. 皮肤切换需要确认UI元素选择器
3. API key需要足够配额
4. 211环境测试需要提前申请维护窗口
5. 并行测试注意资源竞争
6. 所有测试数据使用`test_`前缀标识

---

## 六、后续优化建议

1. **自动化CI/CD集成**：将测试集成到部署流水线
2. **持续性能监控**：建立性能基准，追踪性能趋势
3. **故障注入测试**：模拟各种故障场景（网络抖动、服务降级）
4. **安全测试**：API安全扫描、权限边界测试
5. **兼容性测试**：不同浏览器、不同客户端SDK
6. **可访问性测试**：WCAG合规性验证

---

**文档版本**：v1.0  
**制定人**：测试团队  
**审核状态**：待审核

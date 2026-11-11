# LLM Gateway 测试计划

## 一、功能测试矩阵

### 1. 公开访问功能 (Public Access)

| 功能模块 | 测试用例 | 优先级 | Maestro 流程 |
|---------|---------|--------|-------------|
| 公开试用申请 | 填写表单并提交 | P0 | `01-public-trial.yaml` |
| | 验证邮箱格式 | P1 | `01-public-trial.yaml` |
| | 错误处理 | P2 | `01-public-trial.yaml` |
| 访客浏览 | 查看公开文档 | P0 | `02-public-browse.yaml` |
| | 访问 API 示例 | P1 | `02-public-browse.yaml` |
| 试用密钥 | 获取临时密钥 | P0 | `03-trial-key.yaml` |
| | 使用密钥调用 API | P0 | `03-trial-key.yaml` |
| | 密钥过期处理 | P2 | `03-trial-key.yaml` |

### 2. 概览与监控 (Overview & Monitoring)

| 功能模块 | 测试用例 | 优先级 | Maestro 流程 |
|---------|---------|--------|-------------|
| 仪表板 | 加载统计数据 | P0 | `04-dashboard.yaml` |
| | 实时更新 | P1 | `04-dashboard.yaml` |
| | 数据图表渲染 | P1 | `04-dashboard.yaml` |
| 实时监控 | 查看实时请求 | P0 | `05-live-monitor.yaml` |
| | 过滤与搜索 | P1 | `05-live-monitor.yaml` |
| | 请求详情展开 | P0 | `05-live-monitor.yaml` |
| 会话详情 | 查看完整会话 | P0 | `06-session-detail.yaml` |
| | 查看令牌使用 | P1 | `06-session-detail.yaml` |
| | 导出会话数据 | P2 | `06-session-detail.yaml` |

### 3. 供应商管理 (Provider Management)

| 功能模块 | 测试用例 | 优先级 | Maestro 流程 |
|---------|---------|--------|-------------|
| 供应商列表 | 查看所有供应商 | P0 | `07-provider-list.yaml` |
| | 搜索与过滤 | P1 | `07-provider-list.yaml` |
| 添加供应商 | 创建新供应商 | P0 | `08-provider-create.yaml` |
| | 配置凭据 | P0 | `08-provider-create.yaml` |
| | 验证配置 | P1 | `08-provider-create.yaml` |
| 编辑供应商 | 更新配置 | P0 | `09-provider-edit.yaml` |
| | 更新凭据 | P0 | `09-provider-edit.yaml` |
| 删除供应商 | 删除确认 | P1 | `10-provider-delete.yaml` |
| | 依赖检查 | P2 | `10-provider-delete.yaml` |

### 4. 租户门户 (Tenant Portal)

| 功能模块 | 测试用例 | 优先级 | Maestro 流程 |
|---------|---------|--------|-------------|
| 租户登录 | 用户名密码登录 | P0 | `11-tenant-login.yaml` |
| | 记住登录状态 | P2 | `11-tenant-login.yaml` |
| 租户仪表板 | 查看使用统计 | P0 | `12-tenant-dashboard.yaml` |
| | 查看配额信息 | P0 | `12-tenant-dashboard.yaml` |
| API 密钥管理 | 创建密钥 | P0 | `13-tenant-keys.yaml` |
| | 查看密钥列表 | P0 | `13-tenant-keys.yaml` |
| | 撤销密钥 | P1 | `13-tenant-keys.yaml` |
| 账单信息 | 查看费用明细 | P1 | `14-tenant-billing.yaml` |
| | 下载发票 | P2 | `14-tenant-billing.yaml` |

### 5. 路由策略 (Routing Strategy)

| 功能模块 | 测试用例 | 优先级 | Maestro 流程 |
|---------|---------|--------|-------------|
| 策略配置 | 查看路由规则 | P0 | `15-routing-config.yaml` |
| | 添加路由规则 | P0 | `15-routing-config.yaml` |
| | 编辑规则优先级 | P1 | `15-routing-config.yaml` |
| 故障转移 | 配置故障转移 | P0 | `16-failover-config.yaml` |
| | 测试故障转移 | P1 | `16-failover-config.yaml` |

## 二、性能测试场景

### 1. API 负载测试

**目标指标：**
- QPS: > 1000 req/s
- P95 延迟: < 2s
- 错误率: < 1%
- 并发用户: 100+ VUs

**测试场景：**
```javascript
// tests/k6/scenarios/api-load.js
export const options = {
  stages: [
    { duration: '2m', target: 50 },   // 预热
    { duration: '5m', target: 100 },  // 峰值
    { duration: '2m', target: 0 },    // 降压
  ],
  thresholds: {
    http_req_duration: ['p(95)<2000'],
    http_req_failed: ['rate<0.01'],
  },
};
```

### 2. 流式响应压力测试

**目标指标：**
- 并发 SSE 连接: > 500
- TTFB: < 500ms
- 流式响应稳定性: > 99%

**测试场景：**
- 长时间流式连接（5 分钟+）
- 突发大量并发流
- 流中断自动重连

### 3. 管理端点性能测试

**目标指标：**
- 复杂查询响应: < 3s
- 分页列表加载: < 1s
- 统计聚合计算: < 5s

### 4. 数据库查询性能测试

**测试场景：**
- 大数据量分页查询
- 复杂 JOIN 查询
- 聚合统计查询
- 并发写入场景

## 三、并发测试场景

### 1. 多租户并发操作

**测试矩阵：**
- 10 个租户同时操作
- 每个租户 10 个并发请求
- 持续时间：10 分钟

**验证点：**
- 租户隔离正确
- 无数据串户
- 配额独立计算

### 2. 凭据探测并行执行

**测试场景：**
- 100 个凭据同时探测
- 探测间隔：10 秒
- 验证探测结果正确性

### 3. 路由决策竞争条件

**测试场景：**
- 同时创建/删除/更新路由规则
- 并发路由决策
- 验证决策一致性

### 4. 数据库并发写入

**测试场景：**
- 100 个并发会话写入
- 1000 个并发请求日志
- 验证数据完整性

## 四、UI 主题一致性测试

### 1. 明亮主题检查清单

**页面列表：**
- [ ] 登录页
- [ ] 仪表板
- [ ] 实时监控
- [ ] 供应商管理
- [ ] 租户门户
- [ ] 系统配置

**检查项：**
- [ ] 背景色为浅色系
- [ ] 文字对比度 >= 4.5:1
- [ ] 主色调与 RedClaw 一致
- [ ] 无明显反色区域
- [ ] 菜单配色协调

### 2. 暗黑主题检查清单

**页面列表：**（同上）

**检查项：**
- [ ] 背景色为深色系
- [ ] 文字对比度 >= 4.5:1
- [ ] 主色调与 RedClaw 一致
- [ ] 无明显反色区域
- [ ] 菜单配色协调

### 3. 主题切换测试

**测试步骤：**
1. 在明亮主题下操作
2. 切换到暗黑主题
3. 验证状态保持
4. 验证视觉一致性

## 五、测试执行计划

### Phase 1: 本地 Docker 验证（预计 2 天）

**Day 1:**
- [ ] 启动本地 Docker 环境
- [ ] 执行 Maestro UI 自动化测试（16 个流程）
- [ ] 修复发现的 UI 问题

**Day 2:**
- [ ] 执行 k6 性能测试（4 个场景）
- [ ] 执行并发测试（4 个场景）
- [ ] 执行主题一致性测试
- [ ] 生成测试报告

### Phase 2: 211 生产环境测试（预计 1 天）

**执行步骤：**
1. [ ] 确认 211 环境可访问
2. [ ] 配置环境变量
3. [ ] 执行完整测试套件
4. [ ] 对比本地与 211 测试结果
5. [ ] 生成最终报告

### Phase 3: 问题修复与回归测试（预计 1 天）

**修复优先级：**
- P0: 阻塞性问题，必须修复
- P1: 重要问题，尽快修复
- P2: 一般问题，计划修复

## 六、测试数据准备

### 1. 测试账户

```yaml
# tests/fixtures/accounts.yaml
admin:
  username: "test_admin"
  password: "your_admin_token"
  
tenants:
  - username: "tenant1"
    password: "test123"
  - username: "tenant2"
    password: "test123"
```

### 2. 测试 API 密钥

```yaml
# tests/fixtures/api-keys.yaml
keys:
  - name: "test_key_1"
    tenant: "tenant1"
  - name: "test_key_2"
    tenant: "tenant2"
```

### 3. 测试供应商配置

```yaml
# tests/fixtures/providers.yaml
providers:
  - name: "test_openai"
    type: "openai"
    enabled: true
  - name: "test_anthropic"
    type: "anthropic"
    enabled: true
```

## 七、成功标准

### 功能测试
- ✅ 所有 P0 用例通过率 100%
- ✅ 所有 P1 用例通过率 >= 95%
- ✅ 无阻塞性缺陷

### 性能测试
- ✅ QPS >= 1000
- ✅ P95 延迟 < 2s
- ✅ 错误率 < 1%

### 并发测试
- ✅ 无数据竞争
- ✅ 无死锁
- ✅ 数据一致性 100%

### UI 测试
- ✅ 主题一致性 100%
- ✅ 无明显视觉缺陷
- ✅ 响应式布局正常

## 八、风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|-----|------|------|---------|
| 性能不达标 | 中 | 高 | 提前性能调优，预留优化时间 |
| UI 适配问题 | 低 | 中 | 多浏览器测试，及时修复 |
| 并发问题 | 中 | 高 | 充分压测，代码审查 |
| 环境差异 | 低 | 中 | 环境一致性检查 |

## 九、交付物

1. [ ] 测试报告（HTML + JSON）
2. [ ] 性能基线数据（CSV）
3. [ ] UI 截图集（PNG）
4. [ ] 问题清单（Markdown）
5. [ ] 修复验证报告（PDF）

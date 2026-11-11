# LLM Gateway Go 全面测试验证计划

> **创建时间**: 2026-11-11  
> **目标**: 对应用进行全面的功能特性验证与测试，包括 UI 一致性、性能压力测试、并发测试

## 📋 测试范围

### 测试环境
- **本地 Docker 环境**: 初步测试与验证
- **211 生产环境**: 完整部署后的生产级验证

### 测试维度
1. **功能测试**: 核心功能完整性验证
2. **UI/UX 测试**: 明暗主题一致性、配色验证
3. **性能测试**: 大并发、大压力场景
4. **集成测试**: 多租户隔离、路由决策
5. **安全测试**: 权限隔离、数据脱敏

---

## 🎯 阶段 1: 本地 Docker 环境验证

### 1.1 环境准备

```bash
# 启动本地 Docker Compose 环境
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
docker-compose -f docker-compose.quickstart.yml up -d

# 验证服务启动
docker-compose -f docker-compose.quickstart.yml ps
curl http://localhost:8781/health
curl http://localhost:8781/metrics
```

### 1.2 核心功能测试（Maestro UI 自动化）

#### 测试用例清单

**P0 优先级（必须 100% 通过）**:

1. **数据平面 API 测试**
   - OpenAI Chat Completions (`/v1/chat/completions`)
   - Anthropic Messages (`/v1/messages`)
   - 模型列表查询 (`/v1/models`)
   - 会话管理 (`/v1/sessions`)

2. **管理后台核心流程**
   - 租户登录与权限验证
   - 供应商管理 CRUD
   - 凭据管理与健康监控
   - API Key 创建与权限配置
   - 实时请求流监控

3. **路由决策验证**
   - L1 模型自动选择
   - L2 凭据健康判定
   - Sticky Session 保持
   - 自动降级与恢复

**P1 优先级（覆盖率 ≥ 90%）**:

4. **监控与可观测性**
   - 路由全景（Heatmap + Sankey）
   - 凭据热力图
   - 请求详情查看
   - 成本与用量统计

5. **租户门户功能**
   - 租户仪表板
   - 用量成本查询
   - API Key 自助管理

**P2 优先级（覆盖率 ≥ 70%）**:

6. **高级功能**
   - 语音转文本 (ASR)
   - 内容摘要
   - MCP 工具调用
   - 数据生命周期管理

---

## 🧪 阶段 2: UI/UX 一致性验证

### 2.1 主题配色测试

测试所有页面在**明暗两种主题**下：
- 背景色、文字色对比度符合 WCAG 2.1 AA 标准
- 没有大块反色区域
- 菜单栏配色与主题一致
- 与 RedClaw / llm-gateway-go 风格保持一致

**测试覆盖页面** (共 50+ 页面):

#### 核心页面
- [ ] 登录页 (`/login`)
- [ ] 仪表板 (`/dashboard`)
- [ ] 请求日志 (`/logs`)
- [ ] 租户管理 (`/tenants`)
- [ ] 用户管理 (`/users`)
- [ ] API Key 管理 (`/keys`)

#### 供应商与凭据
- [ ] 供应商列表 (`/providers`)
- [ ] 供应商详情 (`/providers/:id`)
- [ ] 凭据监控 (`/credential-monitor`)
- [ ] 凭据热力图 (`/credential-heatmap`)

#### 路由与决策
- [ ] 路由概览 (`/routing`)
- [ ] 路由全景 (`/routing/panorama`)
- [ ] 自动路由决策 (`/auto-route/decisions`)
- [ ] 任务画像 (`/work-types`)

#### 会话与日志
- [ ] 会话列表 (`/sessions`)
- [ ] 会话详情 (`/sessions/:id`)
- [ ] 请求详情全屏 (`/request-detail/:id`)
- [ ] 请求旅程 (`/request-journeys/:id`)

#### 监控与分析
- [ ] 实时流监控 (`/live-stream`)
- [ ] 成本分析 (`/usage-cost`)
- [ ] 质量画像 (`/quality`)
- [ ] 模型完整性 (`/model-integrity`)

#### 设置与管理
- [ ] 系统设置 (`/settings`)
- [ ] 定价管理 (`/pricing`)
- [ ] 数据生命周期 (`/data-lifecycle`)
- [ ] License 管理 (`/license`)

**自动化脚本位置**: `tests/ui/theme-consistency.spec.ts`

---

## ⚡ 阶段 3: 性能压力测试

### 3.1 API 性能测试（k6）

**测试场景**:

1. **基线负载测试**
   - 10 VUs (Virtual Users)
   - 持续 5 分钟
   - 预期: P95 < 500ms, 错误率 < 0.1%

2. **标准负载测试**
   - 50 VUs
   - 持续 10 分钟
   - 预期: P95 < 1s, 错误率 < 0.5%

3. **峰值负载测试**
   - 100 VUs
   - 持续 5 分钟
   - 预期: P95 < 2s, 错误率 < 1%

4. **压力测试（寻找极限）**
   - 逐步增加 VUs: 100 → 200 → 500
   - 每阶段 3 分钟
   - 识别性能瓶颈与慢页面

**测试端点**:
- `/v1/chat/completions` (流式响应)
- `/v1/messages` (Anthropic)
- `/v1/models` (模型列表)
- `/api/logs/search` (日志查询)
- `/api/routing/overview` (路由概览)

**脚本位置**: `tests/performance/k6-load-test.js`

### 3.2 数据库性能测试

- 测试 PostgreSQL RLS 隔离性能
- 多租户并发读写
- 会话压缩与归档性能

---

## 🔀 阶段 4: 并发与并行测试

### 4.1 多子代理并行测试

使用多个子代理模拟并发操作：

**测试场景**:

1. **多租户并发操作**
   - 10 个租户同时登录
   - 每个租户创建 API Key
   - 并发发起 API 请求
   - 验证租户隔离

2. **凭据并发探测**
   - 多个凭据同时被探测
   - 并发健康检查
   - 降级与恢复并发

3. **路由并发决策**
   - 100+ 并发请求
   - L1/L2 路由决策
   - Sticky Session 保持

**脚本位置**: `tests/concurrent/multi-agent-test.sh`

---

## 📊 阶段 5: 211 生产环境验证

### 5.1 部署前检查

```bash
# 检查 211 环境健康状态
ssh root@172.16.2.211 'docker ps | grep llm-gateway'
ssh root@172.16.2.211 'curl http://localhost:8781/health'

# 备份当前配置
ssh root@172.16.2.211 'pg_dump llm_gateway > /backup/llm_gateway_$(date +%Y%m%d).sql'
```

### 5.2 部署后验证

1. **冒烟测试** (5 分钟)
   - 健康检查端点
   - 基础 API 调用
   - 管理后台访问

2. **功能回归测试** (30 分钟)
   - 运行完整 Maestro 测试套件
   - 验证 P0 功能

3. **性能基线对比** (15 分钟)
   - 运行标准负载测试
   - 对比本地环境性能指标

4. **监控验证** (持续)
   - Prometheus 指标采集
   - 错误日志监控
   - 成本统计准确性

---

## 🔐 阶段 6: 安全与合规测试

### 6.1 多租户隔离测试

- 租户 A 无法访问租户 B 的数据
- RLS 策略验证（90+ 表）
- API Key 权限边界测试

### 6.2 数据脱敏测试

- 敏感信息掩码验证
- 请求/响应 Body 脱敏
- 凭据加密存储验证

### 6.3 License 合规测试

- 离线激活流程
- 设备数限制验证
- 24h 宽限期测试

---

## 📝 测试交付物

### 测试报告
1. **功能测试报告**: `test-reports/functional-test-report-YYYYMMDD.md`
2. **性能测试报告**: `test-reports/performance-test-report-YYYYMMDD.md`
3. **UI 一致性报告**: `test-reports/ui-consistency-report-YYYYMMDD.md`
4. **安全测试报告**: `test-reports/security-test-report-YYYYMMDD.md`

### 缺陷追踪
- 使用 GitHub Issues 记录发现的问题
- 标签: `bug`, `ui-issue`, `performance`, `security`
- 优先级: `P0-critical`, `P1-high`, `P2-medium`

---

## 🎬 执行时间表

| 阶段 | 预计时间 | 负责人 | 状态 |
|------|---------|--------|------|
| 阶段 1: 本地验证 | 4 小时 | QA Team | 待开始 |
| 阶段 2: UI 测试 | 3 小时 | QA Team | 待开始 |
| 阶段 3: 性能测试 | 2 小时 | QA Team | 待开始 |
| 阶段 4: 并发测试 | 2 小时 | QA Team | 待开始 |
| 阶段 5: 211 部署 | 1 小时 | DevOps | 待开始 |
| 阶段 6: 安全测试 | 2 小时 | Security | 待开始 |

**总计**: 约 14 小时

---

## 🚨 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Docker 环境启动失败 | 高 | 提前准备故障排查文档 |
| 性能测试影响生产 | 高 | 先在 staging 环境测试 |
| UI 测试覆盖不全 | 中 | 使用自动化截图对比 |
| 并发测试资源不足 | 中 | 分批次执行 |

---

## 📚 参考文档

- [架构文档](../architecture.md)
- [功能目录](../01-requirements/functional/FEATURES_CATALOG.md)
- [部署指南](../deployment/)
- [测试指南](./TESTING_GUIDE.md)

---

**下一步**: 开始编写 Maestro 测试脚本和 k6 性能测试脚本

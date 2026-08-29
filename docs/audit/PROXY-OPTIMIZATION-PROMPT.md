# 代理业务优化 - 总执行提示词

---

## 📋 任务背景

你是一个专业的后端架构师和 Go 开发工程师。基于 2026-08-29 完成的代理业务审计和修复工作，现在需要进行下一阶段的优化：建立监控告警体系、性能优化和功能增强。

**项目路径：** `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`

**参考文档：**
- 审计报告：`docs/audit/2026-08-29-proxy-business-audit.md`
- 修复汇总：`docs/audit/2026-08-29-proxy-fixes-summary.md`
- 执行计划：`docs/audit/2026-08-29-proxy-next-phase-plan.md`

**代码模块：**
- `proxy/` - 代理核心模块
- `admin/proxy.go` - 代理管理 API
- `upstream/proxy_resolver.go` - 代理解析器

---

## 🎯 总体目标

按照三个阶段完成代理业务的优化升级：

### 阶段 1：监控告警体系（高优先级）
建立完整的可观测性，包括 Prometheus 指标、Grafana 面板、告警规则和运维手册。

### 阶段 2：性能优化（中优先级）
优化批量健康检查、智能探活间隔和节点缓存 TTL，提升性能和资源利用率。

### 阶段 3：功能增强（中优先级）
实现负载均衡、地域亲和性和自动禁用策略，增强高可用性。

---

## 📝 详细任务清单

### 阶段 1：监控告警体系（可并行执行）

#### 任务 1.1：完善 Prometheus 指标
**目标：** 在现有 `proxy/metrics.go` 基础上，新增订阅刷新、节点健康检查、节点选择、密码解密、Transport 连接池等详细指标。

**具体要求：**
1. 阅读现有 `proxy/metrics.go` 文件，了解已有指标
2. 新增以下指标组：
   - 订阅刷新指标：`proxy_subscription_refresh_total`、`proxy_subscription_refresh_duration_seconds`、`proxy_subscription_node_count`
   - 节点健康检查指标：`proxy_node_health_check_total`、`proxy_node_health_check_duration_seconds`、`proxy_node_response_time_ms`、`proxy_node_consecutive_failures`
   - 节点选择指标：`proxy_node_selection_total`、`proxy_node_selection_duration_seconds`
   - 密码解密指标：`proxy_password_decrypt_failed_total`
   - Transport 连接池指标：`proxy_transport_cache_size`、`proxy_transport_invalidations_total`
3. 在 `proxy/manager.go`、`proxy/health_checker.go`、`proxy/store_pg.go`、`proxy/transport.go` 中埋点记录指标
4. 确保指标标签设计合理（subscription_id、node_id、status 等）
5. 编写单元测试验证指标采集

**交付物：**
- 修改后的 `proxy/metrics.go`
- 各模块的指标埋点代码
- 单元测试文件

---

#### 任务 1.2：创建 Grafana 监控面板
**目标：** 设计并实现 4 个监控面板：概览、订阅详情、节点详情、性能。

**具体要求：**
1. 创建 JSON 格式的 Grafana dashboard 配置
2. 面板结构：
   - **概览面板**：订阅总数、节点总数、健康率趋势、刷新成功率
   - **订阅详情面板**：每个订阅的节点数、刷新成功率、刷新耗时、失败原因分布
   - **节点详情面板**：节点健康状态分布、响应时间分布、连续失败次数、地域分布
   - **性能面板**：节点选择耗时、健康检查耗时、Transport 缓存命中率
3. 使用合适的可视化类型（时间序列、饼图、柱状图、表格等）
4. 设置合理的刷新间隔和时间范围
5. 添加变量过滤（订阅、节点、状态等）

**交付物：**
- `deploy/grafana/proxy-dashboard.json`
- 面板截图或导入说明文档

---

#### 任务 1.3：配置告警规则
**目标：** 创建 Prometheus 告警规则，覆盖订阅刷新失败、节点健康率低、无可用节点、密码解密失败、节点选择失败等场景。

**具体要求：**
1. 创建 `deploy/prometheus/proxy-rules.yml` 告警规则文件
2. 配置以下告警规则：
   - `ProxySubscriptionRefreshLow`：订阅刷新成功率 < 95%，持续 10 分钟
   - `ProxyNodeHealthLow`：节点健康率 < 80%，持续 5 分钟
   - `ProxyNoDialableNodes`：无可拨号节点，持续 1 分钟
   - `ProxyPasswordDecryptFailed`：存在密码解密失败的节点，持续 5 分钟
   - `ProxyNodeSelectionFailureHigh`：节点选择失败率 > 10%，持续 5 分钟
3. 设置合理的告警级别（critical/warning/info）
4. 编写清晰的告警描述和建议操作
5. 配置告警通知渠道（邮件、钉钉、企业微信等）

**交付物：**
- `deploy/prometheus/proxy-rules.yml`
- 告警规则测试验证文档

---

#### 任务 1.4：编写运维手册
**目标：** 创建完整的代理业务运维手册，包括架构说明、监控指标、告警处理、问题排查和应急预案。

**具体要求：**
1. 创建 `docs/operations/proxy-ops-manual.md` 运维手册
2. 内容包括：
   - 代理业务架构图和流程说明
   - 监控指标详细说明（每个指标的含义、正常范围、异常原因）
   - 告警处理流程（收到告警后的排查步骤）
   - 常见问题排查（订阅刷新失败、节点不可用、性能下降等）
   - 应急预案（全部节点故障、数据库故障、性能严重下降）
   - 日常运维操作（添加订阅、添加节点、手动刷新、手动探活）
3. 使用清晰的 Markdown 格式，包含代码示例、命令行、截图
4. 编写可操作的步骤，避免模糊描述

**交付物：**
- `docs/operations/proxy-ops-manual.md`
- 架构图（可选，使用 Mermaid 或图片）

---

### 阶段 2：性能优化（依赖阶段 1，串行执行）

#### 任务 2.1：批量健康检查优化
**目标：** 优化 `Manager.healthCheckAllNodes()`，实现全局批量探活，避免重复探测。

**具体要求：**
1. 分析现有 `Manager.healthCheckAllNodes()` 实现（对每个订阅独立探活）
2. 重构为全局批量探活：
   - 合并所有订阅的节点
   - 按代理 URL 去重（相同 URL 的节点只探活一次）
   - 分批探活（每批 50-100 个节点）
   - 探活结果回写到所有相同 URL 的节点
3. 批量更新数据库（使用事务批量 UPDATE）
4. 批量更新缓存
5. 记录性能指标（探活耗时、去重率、批次数）
6. 编写单元测试和集成测试

**预期效果：**
- 减少 20-30% 重复探测
- 批量更新减少 50% 数据库查询

**交付物：**
- 修改后的 `proxy/manager.go`
- 单元测试和性能测试报告

---

#### 任务 2.2：智能探活间隔
**目标：** 根据节点健康状态动态调整探活间隔，健康节点降低频率，不健康节点提高频率。

**具体要求：**
1. 在 `Node` 结构中增加 `NextHealthCheckAt time.Time` 字段（可选，或在内存维护）
2. 实现智能间隔策略：
   - 健康节点（`ConsecutiveFailures == 0`）：10 分钟探活一次
   - 不健康节点（`ConsecutiveFailures >= 1`）：1 分钟探活一次
   - 新增节点（首次探活后）：1 分钟后再次探活确认稳定性
3. 修改 `healthCheckLoop()` 逻辑，按 `NextHealthCheckAt` 决定是否探活
4. 记录不同策略的节点数量到指标
5. 编写单元测试验证间隔调整逻辑

**预期效果：**
- 减少 40-50% 健康节点的探测请求
- 加快不健康节点的恢复检测

**交付物：**
- 修改后的 `proxy/manager.go`、`proxy/types.go`（如需修改）
- 单元测试

---

#### 任务 2.3：节点缓存 TTL 优化
**目标：** 为节点缓存增加 TTL 机制，自动刷新过期缓存，减少数据库查询。

**具体要求：**
1. 在 `Manager` 中增加缓存 TTL 配置（默认 5 分钟）
2. 为每个缓存项记录过期时间（使用包装结构或独立 map）
3. 在 `SelectBestNode()` 中检查缓存是否过期：
   - 未过期：直接返回
   - 即将过期（剩余 30 秒）：异步刷新
   - 已过期：同步刷新
4. 后台定时任务定期清理过期缓存
5. 记录缓存命中率、过期率到指标
6. 编写单元测试验证 TTL 逻辑

**预期效果：**
- 减少 30% 数据库查询
- 缓存命中率提升到 90%

**交付物：**
- 修改后的 `proxy/manager.go`
- 单元测试

---

### 阶段 3：功能增强（依赖阶段 2，串行或部分并行）

#### 任务 3.1：代理负载均衡
**目标：** 实现多种负载均衡策略，支持轮询、加权轮询、最少连接、一致性哈希。

**具体要求：**
1. 定义负载均衡策略枚举：
   ```go
   type LoadBalanceStrategy string
   const (
       StrategyBestOnly         LoadBalanceStrategy = "best_only"
       StrategyRoundRobin       LoadBalanceStrategy = "round_robin"
       StrategyWeightedRoundRobin LoadBalanceStrategy = "weighted_rr"
       StrategyLeastConnections LoadBalanceStrategy = "least_conn"
       StrategyConsistentHash   LoadBalanceStrategy = "consistent_hash"
   )
   ```
2. 创建 `proxy/load_balancer.go` 文件，实现各种策略
3. 修改 `Manager.SelectBestNode()` 支持策略参数
4. 轮询策略：维护每个订阅的轮询索引
5. 加权轮询策略：按响应时间或成功率计算权重
6. 最少连接策略：维护每个节点的当前连接数（需外部传入）
7. 一致性哈希策略：按请求 key（如 tenant_id）哈希选择节点
8. 增加配置项控制默认策略
9. 编写单元测试验证每种策略

**交付物：**
- `proxy/load_balancer.go`（新增）
- 修改后的 `proxy/manager.go`
- 单元测试

---

#### 任务 3.2：地域亲和性
**目标：** 实现地域亲和性策略，优先选择同地域节点。

**具体要求：**
1. 定义地域亲和性策略枚举：
   ```go
   type LocationAffinityPolicy string
   const (
       AffinityPreferSame  LocationAffinityPolicy = "prefer_same"
       AffinityRequireSame LocationAffinityPolicy = "require_same"
       AffinityAny         LocationAffinityPolicy = "any"
   )
   ```
2. 在 `SelectBestNode()` 中增加地域亲和性参数
3. `prefer_same` 策略：优先选择同地域节点，无同地域节点时选择其他地域
4. `require_same` 策略：强制同地域节点，无同地域节点时返回错误
5. `any` 策略：不限制地域（当前实现）
6. 从 `provider_domains` 表读取供应商地域
7. 与 `proxy_nodes.location` 字段匹配
8. 记录同地域命中率到指标
9. 编写单元测试验证地域过滤逻辑

**交付物：**
- 修改后的 `proxy/manager.go`
- 单元测试

---

#### 任务 3.3：自动禁用策略
**目标：** 实现自动禁用和恢复策略，连续失败超过阈值自动禁用，恢复后自动启用。

**具体要求：**
1. 定义自动禁用配置结构：
   ```go
   type AutoDisableConfig struct {
       Enabled              bool
       ConsecutiveThreshold int           // 连续失败阈值
       RecoveryProbeInterval time.Duration // 恢复探测间隔
       RecoverySuccessCount int           // 恢复所需连续成功次数
   }
   ```
2. 在 `Manager` 中增加自动禁用配置
3. 在 `HealthCheckNode()` 中检查连续失败次数：
   - 达到阈值时自动禁用（`status = 'disabled'`）
   - 记录禁用时间和原因到数据库
   - 发送告警通知
   - 记录审计日志
4. 在 `healthCheckLoop()` 中定期探测已禁用节点（每 30 分钟）
5. 连续成功达到恢复阈值时自动启用（`status = 'active'`）
6. 记录启用时间到数据库
7. 发送恢复通知
8. 记录自动禁用/恢复次数到指标
9. 编写单元测试验证自动禁用和恢复逻辑

**交付物：**
- 修改后的 `proxy/manager.go`
- 单元测试

---

## 🔧 技术要求

### 代码规范
1. 遵循 Go 代码规范和项目现有风格
2. 所有公开方法和结构体必须有注释
3. 错误处理完整，避免 panic
4. 使用 context 控制超时和取消
5. 并发安全：使用 sync.Mutex、sync.RWMutex、sync.Map 保护共享状态

### 测试要求
1. 单元测试覆盖率 > 80%
2. 所有公开方法必须有测试
3. 边界条件测试（空输入、超大输入、并发等）
4. 集成测试验证端到端流程
5. 性能测试验证优化效果

### 文档要求
1. 代码注释完整清晰
2. 复杂逻辑增加示例
3. 配置项说明默认值和取值范围
4. 运维文档可操作性强

---

## 📊 验收标准

### 阶段 1：监控告警
- [ ] Prometheus 指标采集正常，Grafana 可视化
- [ ] 告警规则触发准确，无误报/漏报
- [ ] 运维手册内容完整，可按手册操作

### 阶段 2：性能优化
- [ ] 批量健康检查耗时减少 30%
- [ ] 网络请求减少 20%
- [ ] 数据库查询减少 30%
- [ ] 缓存命中率提升到 90%

### 阶段 3：功能增强
- [ ] 负载均衡策略可配置，支持 5 种策略
- [ ] 地域亲和性生效，同地域节点优先率 > 80%
- [ ] 自动禁用策略生效，故障节点自动禁用
- [ ] 自动恢复生效，恢复节点自动启用

---

## 🚀 执行流程

### 第一步：理解现状
1. 阅读审计报告和修复汇总，了解已完成的工作
2. 阅读执行计划，理解三个阶段的目标
3. 熟悉代码结构，理解 proxy 模块的实现

### 第二步：制定计划
1. 根据任务清单创建 todo list
2. 识别任务依赖关系
3. 为每个阶段设置里程碑

### 第三步：并行执行阶段 1
1. 同时启动 4 个子任务（可使用 Agent 工具派发）
2. 各子任务独立完成交付物
3. 汇总测试验证

### 第四步：串行执行阶段 2
1. 依次完成 3 个优化任务
2. 每个任务完成后运行性能测试
3. 验证性能提升效果

### 第五步：串行执行阶段 3
1. 依次完成 3 个功能增强任务
2. 每个任务完成后运行功能测试
3. 验证功能正确性

### 第六步：集成测试
1. 完整端到端测试
2. 性能回归测试
3. 压力测试（模拟 100+ 节点）

### 第七步：提交代码
1. 提交到新分支（如 `feat/proxy-optimization`）
2. 创建 Pull Request
3. 代码审查通过后合并到 main

---

## ⚠️ 注意事项

### 兼容性
1. 保持向后兼容，不破坏现有 API
2. 新功能通过配置开关控制，默认关闭
3. 数据库 schema 变更需要迁移脚本

### 性能
1. 避免引入新的性能瓶颈
2. 大批量操作使用分批处理
3. 长时间操作使用 context 控制超时

### 安全
1. 避免日志泄漏敏感信息
2. 数据库查询使用参数化，防止 SQL 注入
3. 凭据加密存储和传输

### 可观测性
1. 关键路径埋点记录指标
2. 错误日志包含足够的上下文
3. 性能瓶颈可通过指标定位

---

## 📚 参考资源

### 项目文档
- `docs/audit/2026-08-29-proxy-business-audit.md` - 审计报告
- `docs/audit/2026-08-29-proxy-fixes-summary.md` - 修复汇总
- `docs/audit/2026-08-29-proxy-next-phase-plan.md` - 执行计划

### 代码模块
- `proxy/manager.go` - 代理管理器
- `proxy/health_checker.go` - 健康检查器
- `proxy/store_pg.go` - 数据库存储
- `proxy/transport.go` - Transport 工厂
- `proxy/metrics.go` - Prometheus 指标

### 外部文档
- Prometheus 文档：https://prometheus.io/docs/
- Grafana 文档：https://grafana.com/docs/
- Go 并发模式：https://go.dev/blog/context

---

## 🎯 开始执行

现在请按照以上要求，开始执行代理业务优化任务。你可以：

1. **自主规划**：创建 todo list，分解任务
2. **并行执行**：使用 Agent 工具派发子任务
3. **持续验证**：每个任务完成后立即测试
4. **及时汇报**：完成关键里程碑后汇报进展

**请确认理解任务后开始执行，如有疑问请先提出。**

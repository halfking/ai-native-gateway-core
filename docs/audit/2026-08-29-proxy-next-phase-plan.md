# 代理业务优化 - 下一阶段执行计划

## 背景

基于 2026-08-29 代理业务审计报告，12 个问题已全部修复完成。本文档规划下一阶段的优化任务，包括监控告警、性能优化和功能增强。

---

## 阶段目标

### 第一阶段：监控告警体系（优先级：高）
**目标：** 建立完整的代理业务可观测性，及时发现和定位问题
**预期产出：**
- Prometheus 指标完善
- Grafana 监控面板
- 告警规则配置
- 运维手册

### 第二阶段：性能优化（优先级：中）
**目标：** 提升代理业务的性能和资源利用率
**预期产出：**
- 批量健康检查优化
- 智能探活间隔
- 节点缓存 TTL 优化

### 第三阶段：功能增强（优先级：中）
**目标：** 增强代理业务的负载均衡和高可用性
**预期产出：**
- 代理负载均衡
- 地域亲和性
- 自动禁用策略

---

## 详细任务拆解

### 任务 1：监控告警体系建设

#### 1.1 Prometheus 指标完善
**现状：**
- `proxy/metrics.go` 已有基础指标：`proxy_subscriptions_active`、`proxy_subscriptions_inactive`、`proxy_nodes_total`、`proxy_nodes_dialable`、`proxy_nodes_unhealthy`、`proxy_health_check_failures_total`、`proxy_egress_selection_total`

**需要新增的指标：**
```go
// 订阅刷新指标
proxy_subscription_refresh_total{subscription_id, status="success|failed"}
proxy_subscription_refresh_duration_seconds{subscription_id}
proxy_subscription_node_count{subscription_id}

// 节点健康检查指标
proxy_node_health_check_total{node_id, subscription_id, status="success|failed"}
proxy_node_health_check_duration_seconds{node_id, subscription_id}
proxy_node_response_time_ms{node_id, subscription_id}
proxy_node_consecutive_failures{node_id, subscription_id}

// 节点选择指标
proxy_node_selection_total{status="success|no_dialable|none"}
proxy_node_selection_duration_seconds

// 密码解密指标
proxy_password_decrypt_failed_total{node_id}

// Transport 连接池指标
proxy_transport_cache_size{subscription_id}
proxy_transport_invalidations_total{subscription_id}
```

**文件位置：** `proxy/metrics.go`

#### 1.2 Grafana 监控面板
**面板结构：**
1. **概览面板**
   - 订阅总数（active/inactive）
   - 节点总数（total/dialable/unhealthy）
   - 节点健康率趋势
   - 订阅刷新成功率

2. **订阅详情面板**
   - 每个订阅的节点数
   - 刷新成功率
   - 刷新耗时
   - 刷新失败原因分布

3. **节点详情面板**
   - 节点健康状态分布
   - 节点响应时间分布
   - 连续失败次数分布
   - 地域分布

4. **性能面板**
   - 节点选择耗时
   - 健康检查耗时
   - Transport 缓存命中率

**文件位置：** `deploy/grafana/proxy-dashboard.json`

#### 1.3 告警规则配置
**Prometheus 告警规则：**
```yaml
groups:
  - name: proxy_alerts
    interval: 30s
    rules:
      # 订阅刷新成功率低
      - alert: ProxySubscriptionRefreshLow
        expr: |
          (
            sum(rate(proxy_subscription_refresh_total{status="success"}[5m])) by (subscription_id)
            /
            sum(rate(proxy_subscription_refresh_total[5m])) by (subscription_id)
          ) < 0.95
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "订阅 {{ $labels.subscription_id }} 刷新成功率低于 95%"
          description: "过去 10 分钟内成功率为 {{ $value | humanizePercentage }}"

      # 节点健康率低
      - alert: ProxyNodeHealthLow
        expr: |
          (
            proxy_nodes_total - proxy_nodes_unhealthy
          ) / proxy_nodes_total < 0.8
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "代理节点健康率低于 80%"
          description: "当前健康率为 {{ $value | humanizePercentage }}"

      # 无可用节点
      - alert: ProxyNoDialableNodes
        expr: proxy_nodes_dialable == 0 and proxy_nodes_total > 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "无可拨号的代理节点"
          description: "导入了 {{ $labels.nodes_total }} 个节点，但没有任何可用节点"

      # 密码解密失败
      - alert: ProxyPasswordDecryptFailed
        expr: sum(proxy_password_decrypt_failed_total) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "存在密码解密失败的节点"
          description: "{{ $value }} 个节点的密码解密失败"

      # 节点选择失败率高
      - alert: ProxyNodeSelectionFailureHigh
        expr: |
          rate(proxy_node_selection_total{status!="success"}[5m])
          /
          rate(proxy_node_selection_total[5m])
          > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "节点选择失败率超过 10%"
          description: "过去 5 分钟失败率为 {{ $value | humanizePercentage }}"
```

**文件位置：** `deploy/prometheus/proxy-rules.yml`

#### 1.4 运维手册
**内容大纲：**
1. 代理业务架构
2. 监控指标说明
3. 告警处理流程
4. 常见问题排查
5. 应急预案

**文件位置：** `docs/operations/proxy-ops-manual.md`

---

### 任务 2：性能优化

#### 2.1 批量健康检查优化
**现状：**
- `Manager.healthCheckAllNodes()` 对每个订阅独立探活
- 多个订阅的节点可能使用相同代理 URL，重复探测

**优化方案：**
1. 全局批量探活：合并所有订阅的节点，按代理 URL 去重
2. 分批探活：将节点分成多个批次，避免同时探活过多
3. 结果回写：探活完成后批量更新数据库和缓存

**预期效果：**
- 减少重复探测，节省 20-30% 网络开销
- 批量更新数据库，减少 SQL 查询次数

**文件位置：** `proxy/manager.go`

#### 2.2 智能探活间隔
**现状：**
- 所有节点统一 5 分钟探活间隔

**优化方案：**
1. 健康节点：降低探活频率到 10 分钟
2. 不健康节点：提高探活频率到 1 分钟
3. 新增节点：首次探活后 1 分钟再次探活，确认稳定性

**预期效果：**
- 减少 40-50% 健康节点的探测请求
- 加快不健康节点的恢复检测

**文件位置：** `proxy/manager.go`

#### 2.3 节点缓存 TTL 优化
**现状：**
- 缓存无过期时间，需手动调用 `ReloadCache()`

**优化方案：**
1. 增加缓存 TTL（默认 5 分钟）
2. 后台定时刷新缓存（TTL 到期前 30 秒刷新）
3. 缓存命中时返回 TTL 剩余时间

**预期效果：**
- 减少 30% 数据库查询
- 缓存自动刷新，无需手动管理

**文件位置：** `proxy/manager.go`

---

### 任务 3：功能增强

#### 3.1 代理负载均衡
**现状：**
- `SelectBestNode()` 只返回单个最优节点

**增强方案：**
1. 轮询（Round Robin）：依次选择节点
2. 加权轮询（Weighted Round Robin）：按响应时间或成功率加权
3. 最少连接（Least Connections）：选择当前连接数最少的节点
4. 一致性哈希（Consistent Hashing）：同一请求总是路由到同一节点

**配置示例：**
```go
type LoadBalanceStrategy string

const (
    StrategyBestOnly         LoadBalanceStrategy = "best_only"         // 当前实现
    StrategyRoundRobin       LoadBalanceStrategy = "round_robin"       // 轮询
    StrategyWeightedRoundRobin LoadBalanceStrategy = "weighted_rr"    // 加权轮询
    StrategyLeastConnections LoadBalanceStrategy = "least_conn"        // 最少连接
    StrategyConsistentHash   LoadBalanceStrategy = "consistent_hash"  // 一致性哈希
)
```

**文件位置：** `proxy/manager.go`、`proxy/load_balancer.go`（新增）

#### 3.2 地域亲和性
**现状：**
- 节点选择不考虑地理位置

**增强方案：**
1. 供应商地域配置：`provider_domains.location`
2. 节点地域配置：`proxy_nodes.location`
3. 亲和性策略：
   - `prefer_same`: 优先选择同地域节点
   - `require_same`: 强制同地域节点
   - `any`: 不限制地域

**配置示例：**
```go
type LocationAffinityPolicy string

const (
    AffinityPreferSame  LocationAffinityPolicy = "prefer_same"  // 优先同地域
    AffinityRequireSame LocationAffinityPolicy = "require_same" // 强制同地域
    AffinityAny         LocationAffinityPolicy = "any"          // 不限制
)
```

**文件位置：** `proxy/manager.go`

#### 3.3 自动禁用策略
**现状：**
- 连续失败 3 次标记为 `unhealthy`，但不会自动禁用

**增强方案：**
1. 自动禁用阈值：连续失败 10 次自动禁用（`status = 'disabled'`）
2. 自动恢复探测：禁用后每 30 分钟探测一次，成功 3 次自动启用
3. 禁用通知：禁用时发送告警，记录审计日志

**配置示例：**
```go
type AutoDisableConfig struct {
    Enabled              bool          // 是否启用自动禁用
    ConsecutiveThreshold int           // 连续失败阈值
    RecoveryProbeInterval time.Duration // 恢复探测间隔
    RecoverySuccessCount int           // 恢复所需连续成功次数
}
```

**文件位置：** `proxy/manager.go`

---

## 任务依赖关系

```
阶段1：监控告警 (并行)
├── 任务1.1：Prometheus 指标完善
├── 任务1.2：Grafana 监控面板
├── 任务1.3：告警规则配置
└── 任务1.4：运维手册

阶段2：性能优化 (依赖阶段1)
├── 任务2.1：批量健康检查优化
├── 任务2.2：智能探活间隔
└── 任务2.3：节点缓存 TTL 优化

阶段3：功能增强 (依赖阶段2)
├── 任务3.1：代理负载均衡
├── 任务3.2：地域亲和性
└── 任务3.3：自动禁用策略
```

---

## 资源需求

### 开发资源
- 后端开发：2-3 人周
- 运维开发：1 人周
- 测试：1 人周

### 环境需求
- 开发环境：本地 + 测试集群
- 测试环境：独立测试集群（模拟 100+ 节点）
- 生产环境：灰度发布 → 全量发布

### 时间规划
- 阶段1（监控告警）：1 周
- 阶段2（性能优化）：1 周
- 阶段3（功能增强）：2 周
- 总计：4 周

---

## 验收标准

### 阶段1：监控告警
- [ ] Prometheus 指标采集正常，无数据缺失
- [ ] Grafana 面板展示完整，可视化清晰
- [ ] 告警规则触发准确，无误报/漏报
- [ ] 运维手册内容完整，可操作

### 阶段2：性能优化
- [ ] 批量健康检查耗时减少 30%
- [ ] 网络请求减少 20%
- [ ] 数据库查询减少 30%
- [ ] 缓存命中率提升到 90%

### 阶段3：功能增强
- [ ] 负载均衡策略可配置，支持 5 种策略
- [ ] 地域亲和性生效，同地域节点优先率 > 80%
- [ ] 自动禁用策略生效，故障节点自动禁用
- [ ] 自动恢复生效，恢复节点自动启用

---

## 风险与应对

### 风险1：性能优化导致功能退化
**应对：**
- 全面的单元测试和集成测试
- 灰度发布，小流量验证
- 保留回滚开关

### 风险2：监控指标爆炸
**应对：**
- 合理设置指标采集频率
- 使用标签聚合，避免高基数
- 定期清理过期指标

### 风险3：功能增强引入新 Bug
**应对：**
- 功能开关控制，默认关闭
- 充分的边界测试
- 生产环境分阶段开启

---

## 后续展望

### 第四阶段：智能化（长期）
1. **智能节点选择**：基于机器学习预测节点质量
2. **异常检测**：自动识别异常流量和攻击
3. **自动扩缩容**：根据流量自动调整节点数量

### 第五阶段：多云支持（长期）
1. **云厂商代理**：支持 AWS、阿里云、腾讯云等云厂商代理
2. **跨云负载均衡**：多云节点间智能负载均衡
3. **成本优化**：根据成本和性能自动选择云厂商

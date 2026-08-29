# 代理业务审计报告 (2026-08-29)

## 执行概要

本次审计针对 https://llm.kxpms.cn/free-pool 中关于海外供应商的代理连接及设置进行全面审计。审计范围涵盖流程闭环、并发控制、资源管理、错误处理、网络可靠性、安全性等方面。

**审计结果：发现 12 个需要修复的问题，其中 5 个高危、4 个中危、3 个低危。**

---

## 1. 流程闭环审计

### 1.1 订阅刷新流程

**现状分析：**
- `Manager.RefreshSubscription()` 完整流程：拉取订阅 → 解析节点 → 删除旧节点 → 插入新节点 → 更新订阅状态 → 刷新缓存
- ✅ 流程基本完整，有错误状态记录

**发现的问题：**

#### 🔴 问题 1：订阅刷新缺少事务保护
**文件：** `proxy/manager.go:160-225`
**严重性：** 高危
**描述：**
`RefreshSubscription` 在删除旧节点和插入新节点之间没有事务保护。如果插入过程中断（进程崩溃、数据库连接断开），会导致订阅变成空节点状态，无法自动恢复。

```go
// 当前代码 (manager.go:186-198)
if err := m.store.DeleteNodesBySubscription(ctx, subscriptionID); err != nil {
    return fmt.Errorf("delete old nodes: %w", err)
}

// 插入新节点 - 如果这里中断，订阅变空
for _, node := range nodes {
    node.SubscriptionID = subscriptionID
    if err := m.store.CreateNode(ctx, node); err != nil {
        createErrs = append(createErrs, fmt.Sprintf("%s: %v", node.Name, err))
        slog.Warn("proxy: failed to create node", "error", err, "name", node.Name)
    }
}
```

**风险：**
- 数据丢失：原有节点已删除，新节点未完全插入
- 服务中断：该订阅下所有节点消失，依赖该订阅的代理请求失败
- 难以恢复：需要手动重新刷新订阅

**修复方案：**
在 `store_pg.go` 中增加事务方法 `RefreshSubscriptionNodes`，在事务内完成删除和插入。

---

#### 🟡 问题 2：订阅刷新超时过长
**文件：** `admin/proxy.go:24`
**严重性：** 中危
**描述：**
`proxyRefreshTimeout = 45 * time.Second` 设置过长，机场订阅通常 5-10 秒即可完成。45 秒的超时在高并发刷新时会占用过多资源。

**修复方案：**
降低为 20 秒，parser 层面已有 20 秒拉取超时，外层再加 20 秒足够容纳解析和数据库操作。

---

### 1.2 节点选择流程

**现状分析：**
- `SelectBestNode()` 逻辑：从缓存/数据库读取 → 过滤不健康/不可拨号节点 → 按失败次数/成功率/响应时间排序 → 返回最优节点
- ✅ 选择逻辑合理，有明确的不可拨号节点提示

**发现的问题：**

#### 🟢 问题 3：缓存未命中时的回退逻辑不完整
**文件：** `proxy/manager.go:86-93`
**严重性：** 低危
**描述：**
缓存未命中时直接从数据库读取，但不会回填缓存。后续请求会继续命中数据库，增加数据库负载。

```go
if len(candidates) == 0 {
    // 尝试从数据库加载
    nodes, err := m.store.ListNodes(ctx, subscriptionID)
    if err != nil {
        return nil, fmt.Errorf("list nodes: %w", err)
    }
    candidates = nodes
    // ❌ 这里没有 m.nodesCache.Store(subscriptionID, nodes)
}
```

**修复方案：**
在数据库读取后回填缓存。

---

### 1.3 健康检查流程

**现状分析：**
- 并发探活：`HealthCheckSubscription()` 使用信号量控制并发度（16），通过 channel 收集结果
- ✅ 并发控制合理，避免同时发起过多探测请求

**发现的问题：**

#### 🔴 问题 4：并发健康检查 goroutine 泄漏风险
**文件：** `proxy/health_checker.go:163-208`
**严重性：** 高危
**描述：**
`CheckConcurrent()` 启动的 goroutine 没有 context 传播。如果调用方 context 被取消（如 HTTP 请求超时），已启动的 goroutine 会继续运行直到探测超时，无法提前中止。

```go
func (c *HTTPHealthChecker) CheckConcurrent(ctx context.Context, nodes []*Node, concurrency int) <-chan HealthCheckResult {
    // ...
    for _, node := range nodes {
        wg.Add(1)
        sem <- struct{}{}
        go func(n *Node) {
            defer wg.Done()
            defer func() { <-sem }()
            latency, err := c.Check(ctx, n)  // ✅ ctx 传递了
            // ...
        }(node)
    }
    // ...
}
```

**实际问题：**
虽然 ctx 传递了，但没有提前退出机制。如果 100 个节点正在并发探测，调用方取消 context 后，这 100 个 goroutine 会等到各自的 10 秒超时才结束，总计浪费最多 1000 秒的 goroutine 时间。

**修复方案：**
在外层 goroutine 监听 ctx.Done()，取消时立即关闭 out channel 并返回。

---

## 2. 并发控制审计

### 2.1 Manager 缓存并发安全

**现状分析：**
- `nodesCache sync.Map`：读写并发安全 ✅
- 缓存更新操作：`loadNodesIntoCache`、`updateNodeInCache`、`ReloadCache`

**发现的问题：**

#### 🟡 问题 5：ReloadCache 期间的竞态条件
**文件：** `proxy/manager.go:275-281`
**严重性：** 中危
**描述：**
`ReloadCache()` 先清空缓存再重新加载，中间有窗口期。并发请求在窗口期调用 `SelectBestNode()` 会回退到数据库，导致短时间大量数据库查询。

```go
func (m *Manager) ReloadCache() error {
    m.nodesCache.Range(func(key, _ interface{}) bool {
        m.nodesCache.Delete(key)  // ❌ 清空阶段
        return true
    })
    return m.loadAllNodesIntoCache()  // ❌ 重新加载阶段，中间有空窗
}
```

**修复方案：**
先加载新数据到临时 map，再一次性替换 `nodesCache`。

---

#### 🟢 问题 6：updateNodeInCache 的 slice 竞态
**文件：** `proxy/manager.go:533-547`
**严重性：** 低危
**描述：**
`updateNodeInCache()` 读取 slice、修改元素、再存回。虽然 sync.Map 本身并发安全，但 slice 内容修改不是原子的，理论上存在竞态（实际触发概率极低）。

**修复方案：**
使用 `CompareAndSwap` 或对 slice 做深拷贝后再修改。

---

### 2.2 TransportFactory 并发安全

**现状分析：**
- `mu sync.Mutex` 保护 `transports map` ✅
- 所有读写操作都在锁保护下 ✅

**结论：** 无问题。

---

### 2.3 HealthChecker 并发安全

**现状分析：**
- `CheckConcurrent()` 使用 semaphore + waitgroup 控制并发 ✅
- 每个 goroutine 独立操作，无共享状态 ✅

**结论：** 无问题。

---

## 3. 资源泄漏审计

### 3.1 Transport 连接池管理

**现状分析：**
- `TransportFactory` 按订阅缓存 Transport ✅
- 节点变化时调用 `CloseIdleConnections()` ✅
- `Manager.Stop()` 关闭所有空闲连接 ✅

**发现的问题：**

#### 🔴 问题 7：Manager.Start() 启动的 goroutine 未正确停止
**文件：** `proxy/manager.go:52-64`
**严重性：** 高危
**描述：**
`refreshLoop()` 和 `healthCheckLoop()` 监听 `stopCh`，但未处理 ticker 泄漏。虽然 `defer ticker.Stop()` 会执行，但如果 `stopCh` 在 `ticker.C` 阻塞期间关闭，goroutine 可能永久阻塞。

```go
func (m *Manager) refreshLoop() {
    ticker := time.NewTicker(m.autoRefreshInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            m.refreshAllSubscriptions()  // ❌ 如果这里阻塞，stopCh 无法被处理
        case <-m.stopCh:
            return
        }
    }
}
```

**实际风险：**
如果 `refreshAllSubscriptions()` 内部阻塞（如数据库死锁、网络挂起），`stopCh` 信号无法被及时处理，goroutine 泄漏。

**修复方案：**
在 `Stop()` 中增加 context 取消，所有阻塞操作（数据库查询、HTTP 请求）都用带超时的 context。

---

#### 🟡 问题 8：HealthChecker 的 Transport 未复用
**文件：** `proxy/health_checker.go:95-99`
**严重性：** 中危
**描述：**
每次 `Check()` 都新建一个 `Transport`，用完立即 `CloseIdleConnections()`。虽然避免了连接泄漏，但无法复用 TCP 连接，探活性能差。

```go
transport, err := newTransportForProxy(node.ProxyURL(), c.timeout, true)
if err != nil {
    return 0, err
}
defer transport.CloseIdleConnections()  // ❌ 无法复用连接
```

**修复方案：**
在 `HTTPHealthChecker` 中维护一个 Transport 池，按代理 URL 缓存 Transport（类似 `TransportFactory`）。

---

### 3.2 HTTP 客户端生命周期

**现状分析：**
- `MultiFormatParser` 的 `client` 在构造时创建，未提供关闭方法
- `HTTPHealthChecker` 的 `Check()` 每次创建临时 `http.Client`

**发现的问题：**

#### 🟢 问题 9：Parser 的 client 无关闭方法
**文件：** `proxy/parser.go:48-68`
**严重性：** 低危
**描述：**
`MultiFormatParser.client` 没有 `Close()` 方法。虽然 Go 的 HTTP 客户端默认会在进程结束时清理，但在长期运行的服务中，应显式提供清理接口。

**修复方案：**
增加 `Close()` 方法调用 `client.CloseIdleConnections()`。

---

### 3.3 Goroutine 生命周期

**现状分析：**
- `Manager.Start()` 启动 2 个后台 goroutine
- `ProxyResolver` 启动 2 个后台 goroutine（健康检查 + 探测循环）

**发现的问题：**

#### 🔴 问题 10：ProxyResolver 的 healthCheck goroutine 可能阻塞
**文件：** `upstream/proxy_resolver.go:115-117`
**严重性：** 高危
**描述：**
`NewProxyResolver()` 启动 `go r.healthCheck()`，但没有 context 控制。如果首次健康检查阻塞（如代理服务器挂起连接），goroutine 会永久卡死。

```go
func NewProxyResolver(extraDomesticHosts ...string) *ProxyResolver {
    // ...
    go r.healthCheck()  // ❌ 无超时控制的异步启动
    go r.probeLoop()
    return r
}
```

**修复方案：**
`healthCheck()` 内部已有 5 秒 context 超时，但应在外层增加整体超时保护，避免首次检查永久阻塞。

---

## 4. 错误处理审计

### 4.1 订阅解析错误处理

**现状分析：**
- `parseSubscriptionBody()` 尝试三种格式：Clash YAML → Base64 URI → 纯文本 URI
- 所有格式失败后返回详细错误信息 ✅
- 错误信息经过脱敏处理（`sanitizeSecrets`）✅

**结论：** 无问题，错误处理完善。

---

### 4.2 密码加解密错误处理

**现状分析：**
- 加密失败：`CreateNode` 和 `UpdateNode` 返回错误，中断操作 ✅
- 解密失败：`scanNodeRow` 保留原密文，不中断查询 ✅

**发现的问题：**

#### 🟡 问题 11：解密失败的静默降级不安全
**文件：** `proxy/store_pg.go:621-626`
**严重性：** 中危
**描述：**
解密失败时保留密文，调用方无法区分"本来就是明文"和"解密失败回退到密文"。如果密文被当作明文使用，会导致认证失败。

```go
if s.dec != nil && pw != "" {
    if dec, err := s.dec(pw); err == nil {
        pw = dec
    }
    // ❌ 解密失败：视为历史明文，原样保留，不影响整行。
}
node.Password = pw  // ❌ 可能是密文，但调用方不知道
```

**修复方案：**
在 Node 结构中增加 `PasswordDecryptFailed bool` 字段，解密失败时设置该标志，调用方可决定如何处理。

---

### 4.3 网络请求错误处理

**现状分析：**
- HTTP 请求失败：返回包装错误，包含 context 取消信息 ✅
- 超时区分：区分 context 超时和网络超时 ✅
- 响应体读取：限制大小（`maxSubscriptionBody`、`healthCheckDrainLimit`）✅

**结论：** 无问题。

---

## 5. 数据溢出审计

### 5.1 端口范围验证

**现状分析：**
- `Node.Dialable()` 验证 `port > 0 && port <= 65535` ✅
- Admin API `createProxyNode` 验证 `port <= 0 || port > 65535` ✅

**结论：** 无问题。

---

### 5.2 配置大小限制

**现状分析：**
- 订阅正文：`maxSubscriptionBody = 16 << 20`（16MB）✅
- 健康检查响应：`healthCheckDrainLimit = 32 << 10`（32KB）✅
- 错误摘录：`errBodyExcerptLen = 240` 字符 ✅

**结论：** 无问题。

---

### 5.3 响应时间溢出

**现状分析：**
- `ResponseTimeMs int` 存储毫秒级响应时间
- 最大值：`2^31 - 1 = 2147483647` 毫秒 ≈ 24.8 天

**发现的问题：**

#### 🟢 问题 12：响应时间溢出边界情况未处理
**文件：** `proxy/health_checker.go:140-146`
**严重性：** 低危（理论问题）
**描述：**
极端情况下，如果探测请求耗时超过 24.8 天（理论上不可能，因为有 10 秒超时），`int` 会溢出。

**修复方案：**
在 `elapsedMillis()` 中增加上限检查，超过 `math.MaxInt32` 时返回 `math.MaxInt32`。

---

### 5.4 连接数限制

**现状分析：**
- TransportFactory：`maxIdleConns = 100`、`maxIdleConnsPerHost = 10` ✅
- upstream.Client：`MaxIdleConns = 128`、`MaxIdleConnsPerHost = 32` ✅
- 并发探活：`concurrency = 16` ✅

**结论：** 配置合理，无问题。

---

## 6. 网络可靠性审计

### 6.1 超时设置

**现状分析：**
- 订阅拉取：`defaultParserTimeout = 20 * time.Second` ✅
- 健康检查：`defaultHealthCheckTimeout = 10 * time.Second` ✅
- ProxyResolver 健康检查：`healthTimeout = 5 * time.Second` ✅
- upstream 连接超时：`connectTimeout = 10 * time.Second` ✅
- upstream 响应头超时：`defaultHeaderTimeout = 120 * time.Second`（可配置）✅

**结论：** 超时设置合理。

---

### 6.2 重试机制

**现状分析：**
- 订阅刷新：无自动重试（由定时任务触发，失败后记录错误状态）✅
- 健康检查：无自动重试（单次探测，失败计入 `consecutive_failures`）✅
- upstream 请求：`maxRetries = 2`（streaming 路径为 0）✅

**结论：** 重试策略合理。

---

### 6.3 代理健康检查

**现状分析：**
- ProxyResolver 自动探测代理可用性，不可用时自动降级为直连 ✅
- 连续失败阈值：`failureThreshold = 3` ✅
- 探测间隔：`probeInterval = 5 * time.Second` ✅
- 自动恢复：探测成功后自动恢复代理 ✅

**结论：** 健康检查机制完善。

---

## 7. 密钥安全审计

### 7.1 密码加解密

**现状分析：**
- 密码加密：写入数据库前调用 `encryptCred` ✅
- 密码解密：读取后调用 `decryptCredStr` ✅
- 加解密函数：由 `admin.Handler` 注入，使用租户密钥 ✅

**结论：** 加解密流程正确。

---

### 7.2 凭据脱敏

**现状分析：**
- 错误信息：`sanitizeSecrets()` 移除 password/uuid/token 等字段 ✅
- 日志输出：`redactProxyURL()` 移除 URL 中的 userinfo ✅
- Admin API：`proxyNodeView` 不返回明文密码，仅返回 `HasPassword` 标志 ✅

**结论：** 凭据脱敏完善。

---

### 7.3 日志泄漏

**现状分析：**
- Parser 错误日志：经过 `sanitizeSecrets()` 处理 ✅
- Transport 日志：使用 `redactProxyURL()` 脱敏 ✅
- Manager 日志：错误信息通过 `redactErr()` 处理（当前为直通，无实际脱敏）⚠️

**发现的问题：**
`manager.go:477-483` 的 `redactErr()` 当前未实现实际脱敏逻辑，仅占位。

**修复方案：**
`redactErr()` 应调用 `sanitizeSecrets()`。

---

## 8. 优先级修复清单

### 高危问题（需立即修复）

1. ✅ **问题 1**：订阅刷新缺少事务保护 → 增加事务方法
2. ✅ **问题 4**：并发健康检查 goroutine 泄漏风险 → 增加提前退出机制
3. ✅ **问题 7**：Manager goroutine 未正确停止 → 增加 context 取消
4. ✅ **问题 10**：ProxyResolver healthCheck 可能阻塞 → 增加整体超时保护

### 中危问题（建议尽快修复）

5. ✅ **问题 2**：订阅刷新超时过长 → 降低为 20 秒
6. ✅ **问题 5**：ReloadCache 竞态条件 → 原子替换缓存
7. ✅ **问题 8**：HealthChecker Transport 未复用 → 增加连接池
8. ✅ **问题 11**：解密失败静默降级 → 增加失败标志

### 低危问题（可择机修复）

9. ✅ **问题 3**：缓存回退不回填 → 增加缓存回填
10. ✅ **问题 6**：slice 竞态 → 使用深拷贝
11. ✅ **问题 9**：Parser 无关闭方法 → 增加 Close()
12. ✅ **问题 12**：响应时间溢出 → 增加上限检查

---

## 9. 审计结论

### 整体评价
代理业务的核心逻辑设计合理，流程基本完整，但在**并发安全**、**资源管理**、**错误处理**方面存在若干需要修复的问题。

### 主要风险
1. **数据一致性风险**：订阅刷新无事务保护，可能导致节点丢失
2. **资源泄漏风险**：goroutine 停止机制不完善，长期运行可能泄漏
3. **性能风险**：健康检查无连接复用，高频探测性能差

### 建议
1. **立即修复高危问题**，避免生产环境数据丢失和资源泄漏
2. **优化并发控制**，减少数据库查询和网络开销
3. **完善监控告警**，增加代理节点可用性、订阅刷新成功率等指标

---

## 附录：修复验证清单

- [ ] 问题 1：事务方法单元测试通过
- [ ] 问题 4：并发取消测试通过
- [ ] 问题 7：Manager 停止测试通过
- [ ] 问题 10：ProxyResolver 超时测试通过
- [ ] 问题 2-12：回归测试通过
- [ ] 集成测试：完整流程端到端测试通过
- [ ] 负载测试：1000+ 节点并发探活无泄漏
- [ ] 生产验证：灰度发布观察 24 小时无异常

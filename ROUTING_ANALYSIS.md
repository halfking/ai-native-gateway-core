# LLM Gateway 路由与负载均衡深度分析报告

## 执行摘要

本报告对 llm-gateway-go 项目的路由操作和负载均衡逻辑进行了全面的源码级分析。该系统采用多层架构，包括端点选择器、候选者路由器、负载均衡器和多层队列调度系统，支持5种负载均衡策略和协议自适应机制。

**关键发现：**
- 请求处理采用四级路由决策：Handler → Executor → Router → LoadBalancer
- 端点选择使用四阶段协议匹配算法（Stage 1-4）
- 候选者缓存TTL为30秒，支持按credential粒度失效
- 路由评分基于7个维度的加权计算
- 识别了5个主要性能瓶颈和相应的优化机会

---

## 1. 路由层架构分析

### 1.1 请求流程概览

```
Client Request
    ↓
[Handler] domains/streaming/handler.go
    ↓
[Executor] domains/streaming/executors/executor_dispatch.go
    ├─ 多层队列调度 (dispatch.Pipeline)
    ├─ 模型解析与重写 (autoroute)
    └─ 执行协调
        ↓
[Router] domains/streaming/executors/router.go
    ├─ URSM v2 状态管理
    ├─ 候选者过滤与排序
    └─ 多维度评分
        ↓
[Provider Client] provider/client.go
    ├─ 候选者缓存 (30s TTL)
    ├─ SQL查询 (带 LATERAL JOIN)
    └─ 端点聚合
        ↓
[Endpoint Selector] internal/endpointselect/selector.go
    ├─ 四阶段协议匹配
    └─ 端点决策
        ↓
[Load Balancer] proxy/load_balancer.go
    ├─ 5种负载均衡策略
    └─ 节点选择
        ↓
Upstream Provider
```

### 1.2 核心组件职责

#### 1.2.1 Handler（入口层）
**文件：** `domains/streaming/handler.go`

**职责：**
- HTTP请求接收与参数解析
- 认证与授权检查
- 流式响应管理
- 响应拦截器链（安全策略、内容过滤）

**关键特性：**
- 支持SSE流式传输
- 拦截器架构支持内容审查
- 请求生命周期跟踪

#### 1.2.2 Executor（执行层）
**文件：** `domains/streaming/executors/executor_dispatch.go`

**职责：**
- 请求调度与重试协调
- 多层队列管理（每credential一个队列）
- 模型切换决策（exhausted后推荐备选模型）
- 失败分类与处理

**核心接口：**
```go
type dispatchCtx struct {
    params         *ExecParams
    candidates     []provider.Candidate
    byModel        map[string][]provider.Candidate
    initialModel   string
    holder         string
    fpSlotDegraded bool
    retryPerCred   int
    tTotal         time.Time
    stickyCredID   *int  // 会话粘性
    stickyFailed   bool  // sticky失败标记
    stickyFailKind errorsx.ErrorKind
}
```

**调度流程：**
1. `dispatchRoute()` - 获取排序后的credential引用
2. `dispatchResolveModel()` - 解析具体模型
3. `dispatchRecommendModels()` - 推荐备选模型（exhausted场景）
4. `dispatchForward()` - 实际转发请求到上游

#### 1.2.3 Router（路由决策层）
**文件：** `domains/streaming/executors/router.go`

**核心数据结构：**
```go
type Router struct {
    Sticky                 *StickyCache      // 会话粘性缓存
    Limiter                *credential.Limiter // 并发限制器
    LiveLoad               interface{}       // 实时负载查询
    FpSlots                interface{}       // 指纹槽管理
    StateManager           credentialstate.StateProvider // 状态后端
    URSMv2                 *ursmv2.Manager   // 统一路由状态管理v2
    LoadScoreWeights       LoadScoreWeights  // 评分权重
    EnvWeights             *LoadScoreEnvWeights // 环境权重（预解析）
    TimeoutConfig          TimeoutCalculator // 动态超时计算
    PressureAwareEnabled   bool              // 压力感知路由
    StickyLoad             StickyLoadView    // sticky负载视图
    CircuitRecovery        interface{}       // 熔断恢复查询
    ShadowStrategy         Strategy          // 影子策略（对比测试）
    RateSink               *RateSink         // 429信号收集
    weightCounters         sync.Map          // 加权轮询计数器
    PriorityRoutingEnabled bool              // 优先级路由开关
}
```

**路由决策流程：**

```go
func (r *Router) PlanCandidatesWithContext(...) []provider.Candidate {
    // 1. 去重候选者
    candidates = deduplicateCandidates(candidates)
    
    // 2. URSM v2 状态过滤（Authoritative模式）
    if r.URSMv2 != nil && mode == ModeAuthoritative {
        views, err := r.URSMv2.FilterAndScoreReadyWithSource(ctx, seeds)
        candidates = filteredByViews(views)
    }
    
    // 3. 状态后端选择与可用性过滤
    stateBackend := selectStateBackendWithReady(r.URSMv2, r.StateManager, ctx)
    available := stateBackend.FilterAvailable(ctx, candidates)
    
    // 4. 健康节点过滤（非Authoritative模式）
    if !stateBackend.IsAuthoritative() {
        available = r.filterHealthyNodes(available)
    }
    
    // 5. 压力惩罚应用
    if r.PressureAwareEnabled {
        r.applyPressurePenalty(ctx, available, stateBackend.Name())
    }
    
    // 6. 分层排序（Tier 1→2→3→9）
    return r.planByTierWithShadow(...)
}
```

**多维度评分系统：**

路由器使用7个维度对候选者进行评分：

| 维度 | 权重来源 | 计算逻辑 | 影响范围 |
|------|---------|---------|---------|
| **sticky** | EnvWeights.Sticky | 惩罚非sticky候选 | 0-1.0 |
| **recency** | EnvWeights.Recency | 基于最近请求时间 | 0-1.0 |
| **balance** | EnvWeights.Balance | 余额归一化 | 0-1.0 |
| **planQuota** | EnvWeights.PlanQuota | 配额使用率惩罚 | 0-1.0 |
| **headroom** | EnvWeights.Headroom | FP槽剩余比例 | 0-1.0 |
| **capacity** | EnvWeights.Capacity | 并发容量评估 | 0-1.0 |
| **recovery** | EnvWeights.Recovery | 熔断恢复期惩罚 | 0-1.0 |

**评分公式：**
```
loadScore = sticky_penalty × recency_penalty × balance_weight × 
            planQuota_weight × headroom_weight × capacity_weight × 
            recovery_weight × base_weight
```

---

## 2. 端点选择器详解

### 2.1 核心算法

**文件：** `internal/endpointselect/selector.go`

端点选择器解决了"多协议供应商"的路由问题。一个供应商可以同时支持多种协议（如Ollama同时支持native和OpenAI-compatible），选择器根据客户端协议和模型家族智能选择最佳端点。

### 2.2 四阶段匹配算法

#### Stage 1A: 完美匹配（Protocol + Family）
```go
// 条件: protocol == wantProtocol AND vendor_native == wantFamily
// 结果: Passthrough=true（字节级透传，未来P5特性）
Decision{
    Protocol:     ep.Protocol,
    BaseURL:      ep.BaseURL,
    EndpointID:   ep.ID,
    VendorNative: ep.VendorNative,
    Passthrough:  true,  // ← 完美匹配才允许透传
    MatchRule:    "stage1a",
    Reason:       "protocol + family match (passthrough eligible)",
}
```

**示例场景：**
- 客户端协议：`ollama-native`
- 模型家族：`ollama`
- 供应商端点：`{protocol: "ollama-native", vendor_native: "ollama"}`
- **结果：** 直接透传，保留Ollama特有参数（`options`, `keep_alive`等）

#### Stage 1B: 协议匹配但家族不匹配
```go
// 条件: protocol == wantProtocol AND wantFamily != "" AND vendor_native != wantFamily
// 结果: Passthrough=false（需要IR桥接）
Decision{
    Passthrough: false,  // ← 家族不匹配，必须IR桥接
    MatchRule:   "stage1b",
    Reason:      "protocol match; family mismatch (IR bridging required)",
}
```

**示例场景：**
- 客户端协议：`openai-completions`
- 模型家族：`openai`
- 供应商端点：`{protocol: "openai-completions", vendor_native: "deepseek"}`
- **结果：** 使用OpenAI序列化器，但通过IR层处理家族差异

#### Stage 1C: 协议匹配但家族未知
```go
// 条件: protocol == wantProtocol AND wantFamily == ""
// 结果: Passthrough=false（安全起见）
Decision{
    Passthrough: false,  // ← 家族未知，保守IR桥接
    MatchRule:   "stage1c",
    Reason:      "protocol match; family unknown (IR bridging required)",
}
```

#### Stage 2: 家族匹配但协议不匹配
```go
// 条件: vendor_native == wantFamily AND protocol != wantProtocol
// 结果: 选择该家族下权重最高的端点
func pickFamilyHit(eps []EndpointLite, wantFamily string) *familyHit {
    // 按端点数量 + 最大权重排序
    // 权重越高 = 流量份额越大（provider侧语义）
    sortByCountAndWeight(candidates)
    return candidates[0]
}
```

**示例场景：**
- 客户端协议：`ollama-native`
- 模型家族：`ollama`
- 供应商端点：`{protocol: "openai-completions", vendor_native: "ollama"}`
- **结果：** 家族正确但协议不匹配，使用IR桥接转换

#### Stage 4: 回退到主端点（Legacy Fallback）
```go
// 条件: 前三个阶段都没有匹配
// 结果: 使用候选者的主端点
Decision{
    Protocol:     Protocol(cand.Protocol),
    BaseURL:      cand.BaseURL,
    EndpointID:   0,  // ← 0表示主端点
    VendorNative: cand.VendorNative,
    Passthrough:  false,
    MatchRule:    "stage4",
    Reason:       "no native endpoint matched; legacy primary fallback",
}
```

### 2.3 协议命名空间转换

端点选择器维护两个命名空间的映射：

```go
var irToCatalogProtocol = map[string]string{
    // IR命名空间 → Catalog命名空间
    "openai-chat":        "openai-completions",
    "ollama-chat":        "ollama-native",
    
    // Catalog值直接透传
    "openai-completions": "openai-completions",
    "openai-responses":   "openai-responses",
    "anthropic-messages": "anthropic-messages",
    "gemini-generate":    "gemini-generate",
    "ollama-native":      "ollama-native",
}
```

**为什么需要两个命名空间？**
- **IR命名空间：** 内部表示（`internal/ir/types.go`），面向客户端协议检测
- **Catalog命名空间：** 数据库约束（`provider_catalog.protocol` CHECK枚举），面向供应商配置

### 2.4 权重优先级

端点选择使用统一的优先级比较器：

```go
func endpointPriorityLess(a, b EndpointLite) bool {
    // 1. 主端点优先
    if a.IsPrimary != b.IsPrimary {
        return a.IsPrimary
    }
    // 2. 权重降序（高权重 = 大流量份额）
    if a.Weight != b.Weight {
        return a.Weight > b.Weight  // 注意：降序
    }
    // 3. ID升序（确定性）
    return a.ID < b.ID
}
```

**权重语义：**
- `weight` 是流量份额（1-999）
- 更高的权重获得更多流量
- 与provider侧容量加权LB语义一致

---

## 3. 负载均衡器实现

### 3.1 支持的策略

**文件：** `proxy/load_balancer.go`

```go
type LoadBalanceStrategy string

const (
    StrategyBestOnly           = "best_only"
    StrategyRoundRobin         = "round_robin"
    StrategyWeightedRoundRobin = "weighted_rr"
    StrategyLeastConnections   = "least_conn"
    StrategyConsistentHash     = "consistent_hash"
)
```

### 3.2 策略详解

#### 3.2.1 Best Only（默认）
```go
// 总是选择排序后的第一个节点
// 适用场景：候选者已经按质量排序
return nodes[0]
```

#### 3.2.2 Round Robin（轮询）
```go
func (lb *LoadBalancer) selectLocked(nodes []*Node, subscriptionID int) *Node {
    index := lb.roundRobinIndex[subscriptionID]
    selected := nodes[index % uint64(len(nodes))]
    lb.roundRobinIndex[subscriptionID] = index + 1
    return selected
}
```

**状态管理：**
- 每个subscription维护独立的轮询索引
- 使用`sync.Mutex`保护并发访问

#### 3.2.3 Weighted Round Robin（平滑加权轮询）
```go
func (lb *LoadBalancer) selectWeightedRoundRobinLocked(nodes []*Node, subscriptionID int) *Node {
    current := lb.weightedCurrent[subscriptionID]
    
    totalWeight := 0
    bestWeight := -1 << 30
    var selected *Node
    
    for _, node := range nodes {
        weight := nodeWeight(node)  // 基于延迟的权重
        totalWeight += weight
        current[node.ID] += weight
        
        if current[node.ID] > bestWeight {
            selected, bestWeight = node, current[node.ID]
        }
    }
    
    current[selected.ID] -= totalWeight
    return selected
}

func nodeWeight(node *Node) int {
    latency := node.ResponseTimeMs
    if latency < 0 {
        latency = 0
    }
    weight := 1000 / (latency + 10)
    if weight < 1 {
        return 1
    }
    return weight
}
```

**权重计算：**
- 基于延迟的反比例权重：`weight = 1000 / (latency_ms + 10)`
- 10ms延迟 → 权重50
- 100ms延迟 → 权重9
- 保证最小权重为1

**平滑算法特性：**
- 避免权重大的节点连续被选中
- 长期分布符合权重比例
- 短期分布平滑

#### 3.2.4 Least Connections（最少连接）
```go
func (lb *LoadBalancer) selectLeastConnectionsLocked(nodes []*Node) *Node {
    selected := nodes[0]
    least := lb.connectionCounts[selected.ID]
    
    for _, node := range nodes[1:] {
        count := lb.connectionCounts[node.ID]
        if count < least || (count == least && node.ID < selected.ID) {
            selected, least = node, count
        }
    }
    return selected
}

// 调用方必须在请求生命周期配对调用
lb.IncConnection(nodeID)   // 请求开始
defer lb.DecConnection(nodeID)  // 请求结束
```

**适用场景：**
- 请求处理时间差异大
- 长连接服务

#### 3.2.5 Consistent Hash（一致性哈希）
```go
func selectConsistentHash(nodes []*Node, requestKey string) *Node {
    type point struct {
        hash uint32
        node *Node
    }
    
    // 构建虚拟节点环
    ring := make([]point, 0, len(nodes)*64)
    for _, node := range nodes {
        for replica := 0; replica < 64; replica++ {
            hash := hashString(fmt.Sprintf("%d#%d", node.ID, replica))
            ring = append(ring, point{hash, node})
        }
    }
    
    // 排序
    sort.Slice(ring, func(i, j int) bool {
        return ring[i].hash < ring[j].hash
    })
    
    // 顺时针查找
    key := hashString(requestKey)
    index := sort.Search(len(ring), func(i int) bool {
        return ring[i].hash >= key
    })
    if index == len(ring) {
        index = 0
    }
    
    return ring[index].node
}
```

**虚拟节点配置：**
- 每个物理节点对应64个虚拟节点
- 使用FNV-1a哈希算法
- 顺时针查找最近虚拟节点

**优势：**
- 节点变化时只影响邻近key
- 比模哈希更均匀的负载分布

### 3.3 地域亲和性

```go
type LocationAffinityPolicy string

const (
    AffinityAny         = "any"          // 无偏好
    AffinityPreferSame  = "prefer_same"  // 优先同地域
    AffinityRequireSame = "require_same" // 强制同地域
)

func (lb *LoadBalancer) SelectNodeWithLocation(
    nodes []*Node, 
    subscriptionID int, 
    requestKey, 
    preferredLocation string,
) *Node {
    // 1. 过滤同地域节点
    sameLocation := filterByLocation(nodes, preferredLocation)
    
    // 2. 根据策略选择
    if policy == AffinityRequireSame {
        return lb.selectLocked(sameLocation, ...)  // 只考虑同地域
    }
    if policy == AffinityPreferSame && len(sameLocation) > 0 {
        return lb.selectLocked(sameLocation, ...)  // 优先同地域
    }
    return lb.selectLocked(nodes, ...)  // 回退到全部节点
}
```

---

## 4. 协议自适应机制

### 4.1 provider_endpoint_protocols 表

**表结构：**
```sql
CREATE TABLE provider_endpoint_protocols (
    id                   BIGSERIAL PRIMARY KEY,
    provider_id          INTEGER NOT NULL REFERENCES providers(id),
    protocol             VARCHAR(50) NOT NULL 
        CHECK (protocol IN (
            'openai-completions',
            'openai-responses',
            'anthropic-messages',
            'gemini-generate',
            'ollama-native'
        )),
    base_url             TEXT NOT NULL,
    is_primary           BOOLEAN NOT NULL DEFAULT false,
    vendor_native        VARCHAR(50),
    enabled              BOOLEAN NOT NULL DEFAULT true,
    weight               SMALLINT NOT NULL DEFAULT 100 CHECK (weight BETWEEN 1 AND 999),
    notes                TEXT,
    health_status        VARCHAR(20) DEFAULT 'unknown',
    health_checked_at    TIMESTAMPTZ,
    health_latency_ms    INTEGER,
    health_error         TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    UNIQUE (provider_id, protocol),
    -- 每个供应商最多一个主端点
    UNIQUE (provider_id, is_primary) WHERE is_primary = true
);
```

### 4.2 管理接口

**文件：** `admin/provider_endpoints.go`

**API端点：**

#### GET /api/providers/{id}/endpoints
```json
{
    "provider_id": 123,
    "endpoints": [
        {
            "id": 1,
            "protocol": "openai-completions",
            "base_url": "https://api.example.com",
            "is_primary": true,
            "vendor_native": "openai",
            "enabled": true,
            "weight": 100,
            "health_status": "healthy",
            "consecutive_failures": 0
        },
        {
            "id": 2,
            "protocol": "ollama-native",
            "base_url": "https://ollama.example.com",
            "is_primary": false,
            "vendor_native": "ollama",
            "enabled": true,
            "weight": 80
        }
    ]
}
```

#### PUT /api/providers/{id}/endpoints/{protocol}
```json
{
    "base_url": "https://new-url.example.com",
    "is_primary": false,
    "enabled": true,
    "weight": 150,
    "vendor_native": "openai",
    "notes": "Upgraded endpoint"
}
```

**响应：**
- `201 Created` - 新建端点
- `200 OK` - 更新现有端点
- `400 Bad Request` - 验证失败
- `409 Conflict` - 主端点冲突

#### DELETE /api/providers/{id}/endpoints/{protocol}
```json
{}
```

**响应：**
- `200 OK` - 删除成功
- `409 Conflict` - 尝试删除主端点
- `404 Not Found` - 端点不存在

### 4.3 约束和验证

#### 4.3.1 协议验证
```go
func (h *Handler) handleProviderEndpoints(..., protocol string) {
    // 1. 协议归一化（处理别名）
    protocol, err := providercatalog.NormalizeProviderProtocol(protocol)
    
    // 2. 检查是否在枚举内
    if !isProviderEndpointProtocol(protocol) {
        return 400
    }
    
    // 3. 拒绝 "adaptive" 选择器
    // adaptive 是选择器关键字，不是真实协议
    if protocol == "adaptive" {
        return 400
    }
}
```

#### 4.3.2 主端点提升
```go
func (h *Handler) putProviderEndpoint(..., isPrimary bool) {
    tx.Begin()
    defer tx.Rollback()
    
    if isPrimary {
        // 1. 先摘掉旧主端点（避免23505）
        _, err := tx.Exec(`
            UPDATE provider_endpoint_protocols
            SET is_primary = false
            WHERE provider_id = $1 AND is_primary = true
        `, providerID)
        
        // 2. 再插入/更新新主端点
        _, err := tx.Exec(`
            INSERT INTO provider_endpoint_protocols (...)
            VALUES (...)
            ON CONFLICT (provider_id, protocol) DO UPDATE ...
        `)
    }
    
    tx.Commit()
}
```

#### 4.3.3 主端点保护
```go
func (h *Handler) deleteProviderEndpoint(...) {
    result := tx.Exec(`
        DELETE FROM provider_endpoint_protocols
        WHERE provider_id = $1 AND protocol = $2
        AND is_primary = false  -- 守卫性条件
    `, providerID, protocol)
    
    if result.RowsAffected() == 0 {
        // 可能是：
        // 1. 端点不存在
        // 2. 是主端点（被 is_primary=false 拦截）
        
        // 检查是否为主端点
        var isPrimary bool
        tx.QueryRow(`
            SELECT is_primary FROM provider_endpoint_protocols
            WHERE provider_id = $1 AND protocol = $2
        `, providerID, protocol).Scan(&isPrimary)
        
        if isPrimary {
            return 409  // 不允许删除主端点
        }
        return 404  // 端点不存在
    }
    
    return 200
}
```

**关键教训（来自 L148）：**
> "删了没删都报200"是写路径最隐蔽的一类谎——读-判-写跨事务边界时判断条件会过期，语句里的守卫性 AND（`AND is_primary = false`）把"条件已过期"静默翻译成"无操作成功"；凡有守卫性WHERE的写路径都要用 `RowsAffected()` 把"无操作"显式分流成冲突。

#### 4.3.4 URL验证
```go
func validateProviderEndpointBaseURL(raw string, local bool) string {
    raw = strings.TrimSpace(raw)
    if raw == "" {
        return "base_url is required"
    }
    
    if local {
        // 本地供应商：复用 validateLocalBaseURL
        // 要求：回环地址或私网IP
        return validateLocalBaseURL(raw)
    }
    
    // 普通供应商
    u, err := url.Parse(raw)
    if err != nil || u.Hostname() == "" {
        return "base_url is not a valid URL"
    }
    
    if u.Scheme != "http" && u.Scheme != "https" {
        return "base_url must use http(s)"
    }
    
    return ""  // 通过
}
```

### 4.4 缓存失效

**写后立即失效候选者缓存：**
```go
func (h *Handler) putProviderEndpoint(...) {
    // ... 数据库写入 ...
    
    // 失效该供应商的所有候选者缓存
    provider.InvalidateAllCandidateCache()
    
    // 为什么全局失效？
    // 候选载入把 provider_endpoint_protocols 聚合到每个候选的 NativeEndpoints
    // 不失效就要等 TTL（30秒）才生效
}
```

### 4.5 探针直连腿

**文件：** `bg/node_probe.go`

```go
func (w *NodeProbeWorker) resolveDirectTarget(
    ctx context.Context, 
    credID int, 
    model string,
) (plain, outboundModel, baseURL, protocol string, providerID int, modality string, err error) {
    // 探针查询：
    // SELECT p.id, p.protocol, pep.base_url, pep.protocol AS pep_protocol
    // FROM providers p
    // LEFT JOIN provider_endpoint_protocols pep 
    //   ON pep.provider_id = p.id 
    //   AND pep.protocol = p.protocol
    // WHERE ...
    
    // 关键点：探针**无视** FF_ENDPOINT_SELECTOR
    // 直接按协议匹配取 base_url
    
    // L147修复：返回前调用 alignAdaptiveDirectTarget
    // 把 adaptive 协议消解成基协议
    protocol = alignAdaptiveDirectTarget(protocol, ...)
    
    return
}
```

**影响面（来自代码注释）：**
> 写 provider_endpoint_protocols 表**今天就会影响探针**——bg/node_probe.go 的 resolveDirectTarget 无视 FF_ENDPOINT_SELECTOR 直接按协议匹配行取 base_url；路由侧要 FF_ENDPOINT_SELECTOR=true 才消费（默认 false）。

---

## 5. 缓存机制分析

### 5.1 候选者缓存

**文件：** `provider/client.go`

#### 5.1.1 缓存配置
```go
const (
    candidateCacheTTL        = 30 * time.Second   // 正常TTL
    candidateEmptyCacheTTL   = 5 * time.Second    // 空结果TTL
    candidateCacheStaleGrace = 30 * time.Second   // 过期宽限期
    candidateGenerationTries = 3                  // 生成失效重试
)
```

#### 5.1.2 缓存键设计
```go
func (c *Client) getCandidates(ctx, model, profile, tenantID, modality) {
    routeModel := modelname.NormalizeRouteKey(model)
    
    // 缓存键组成
    key := routeModel
    if profile != "" {
        key = routeModel + "|" + profile
    }
    if tenantID != "" {
        key = key + "|" + tenantID
    }
    if modality != "" {
        key = key + "|modality:" + modality
    }
    
    // 示例：
    // "gpt-4|premium|tenant-123|modality:chat"
}
```

#### 5.1.3 生成计数器机制

```go
type candidateFlightResult struct {
    response   *resolveResponse
    generation uint64  // ← 快照生成号
}

func (c *Client) getCandidates(...) {
    for attempt := 0; attempt < 3; attempt++ {
        c.mu.RLock()
        queryGeneration := c.candGeneration  // 读取当前生成号
        cached, ok := c.candCache[key]
        c.mu.RUnlock()
        
        if ok && cached.validAt(now) {
            return cached.value  // 缓存命中
        }
        
        // SingleFlight：同一key只有一个goroutine查询DB
        result, err := c.sf.Do(key, func() (interface{}, error) {
            resp, err := c.resolveCandidatesFromDB(...)
            return candidateFlightResult{
                response:   resp,
                generation: queryGeneration,  // 记录查询时的生成号
            }, err
        })
        
        // 检查生成号是否过期
        c.mu.RLock()
        currentGen := c.candGeneration
        c.mu.RUnlock()
        
        if result.generation != currentGen {
            // 查询期间有失效操作
            invalidatedErr = &candidateGenerationInvalidatedError{
                queried: result.generation,
                current: currentGen,
            }
            continue  // 重试
        }
        
        // 写入缓存
        c.mu.Lock()
        c.candCache[key] = cacheEntry{...}
        c.mu.Unlock()
        
        return result.response
    }
}
```

**为什么需要生成计数器？**
1. 防止写脏数据：查询开始后有失效操作，查询结果不应写回缓存
2. 重试机制：失效后重新查询最新数据
3. 并发安全：多个goroutine同时失效和查询

#### 5.1.4 按Credential失效

```go
func InvalidateCandidateCacheForCredential(credentialID int) {
    if defaultClient == nil {
        return
    }
    
    needsBump := false
    defaultClient.mu.Lock()
    defer defaultClient.mu.Unlock()
    
    // 遍历缓存，找到包含该credential的条目
    for key, entry := range defaultClient.candCache {
        if entry.value == nil || entry.value.PlanOrder == nil {
            continue
        }
        
        // 检查PlanOrder中是否有该credential
        for _, cand := range entry.value.PlanOrder {
            if cand.CredentialID == credentialID {
                delete(defaultClient.candCache, key)
                needsBump = true
                break
            }
        }
    }
    
    // 只在实际删除缓存时增加生成号
    if needsBump {
        defaultClient.candGeneration++
    }
}
```

**优化收益（来自代码注释）：**
> OPT-5 (2026-07-12): 此前的 InvalidateAllCandidateCache 造成雷鸣群效应：单个credential状态变化导致所有并发请求击穿缓存查询DB。按credential失效后，只有使用该credential的模型需要重新查询。

**成本分析：**
- 时间复杂度：O(N×K)
  - N = 缓存条目数（几百）
  - K = 每个候选列表长度（通常<10）
- 空间复杂度：O(1)
- 实际成本可忽略（相比避免的DB往返）

### 5.2 其他缓存

#### 5.2.1 策略缓存
```go
type Client struct {
    polCache cacheEntry[*Policy]  // 全局策略，TTL同candidate
}
```

#### 5.2.2 密钥缓存
```go
type Client struct {
    keyCache       map[int]cacheEntry[string]  // credential_id → 解密后的key
    keyGeneration  map[int]uint64              // 防止旋转后写入旧key
    keyCacheNeg    map[int]negativeCacheEntry  // 解密失败的负缓存
}

const (
    keyCacheTTL            = 5 * time.Minute
    decryptFailureCacheTTL = 1 * time.Minute  // 负缓存TTL
)
```

**负缓存机制：**
```go
type negativeCacheEntry struct {
    failedAt time.Time
}

func (c *Client) enrichWithAPIKeys(...) {
    // 检查负缓存
    if negEntry, ok := c.keyCacheNeg[credID]; ok {
        if time.Since(negEntry.failedAt) < decryptFailureCacheTTL {
            return nil  // 跳过，不重试
        }
        delete(c.keyCacheNeg, credID)
    }
    
    // 尝试解密
    plain, err := c.secretClient.RevealAPIKey(...)
    if err != nil {
        // 记录到负缓存
        c.keyCacheNeg[credID] = negativeCacheEntry{
            failedAt: time.Now(),
        }
        return err
    }
    
    // 成功，写入正缓存
    c.keyCache[credID] = cacheEntry{...}
}
```

**负缓存收益：**
- 避免重复解密失败的key
- 减少DB扫描和日志噪音
- 1分钟TTL平衡及时性和保护性

#### 5.2.3 Sticky缓存
```go
type StickyCache struct {
    mu      sync.RWMutex
    entries map[string]*stickyEntry  // sessionID → credential
    ttl     time.Duration
}

type stickyEntry struct {
    credentialID int
    createdAt    time.Time
    accessedAt   time.Time
}

func (s *StickyCache) Get(sessionID string) *int {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    entry, ok := s.entries[sessionID]
    if !ok {
        return nil
    }
    
    if time.Since(entry.accessedAt) > s.ttl {
        return nil  // 过期
    }
    
    credID := entry.credentialID
    return &credID
}
```

**Sticky策略：**
- 会话首次请求选定credential
- 后续请求优先使用同一credential
- TTL内保持亲和性
- 支持Anthropic/OpenAI的prompt cache

---

## 6. 性能瓶颈识别

### 6.1 路由决策复杂度

#### 问题描述
```go
func (r *Router) planByTier(...) {
    for _, tier := range tierOrder {  // 4个tier
        for _, cand := range candidates {  // N个候选
            if cand.Tier != tier {
                continue
            }
            
            // 多维度评分计算
            score := r.calculateLoadScore(cand)  // O(1) but expensive
            
            // 包含：
            // - FpSlots查询（Redis）
            // - LiveLoad查询（内存）
            // - CircuitRecovery查询（内存）
            // - 7个维度权重计算
        }
    }
}
```

**复杂度分析：**
- 时间：O(N×M)
  - N = 候选数量（通常10-50）
  - M = 评分维度（7个）
- 每个候选可能涉及：
  - 1次Redis查询（FpSlots状态）
  - 2-3次内存查询（LiveLoad, CircuitRecovery, StickyLoad）
  - 7次浮点运算

**瓶颈量化：**
- 50个候选 × 7维度 = 350次计算
- Redis超时50ms（可能阻塞）
- 高QPS下可能成为热点

#### 优化建议
1. **批量查询FpSlots状态**
   ```go
   // 当前：每个候选单独查询
   for _, cand := range candidates {
       state := r.FpSlots.GetNodeState(ctx, cand.CredentialID, cand.RawModel)
   }
   
   // 优化：批量查询
   keys := buildKeys(candidates)
   states := r.FpSlots.GetNodeStatesBatch(ctx, keys)
   ```

2. **预计算环境权重（已实现）**
   ```go
   // ✅ 已优化：R48 §3 A2
   type Router struct {
       EnvWeights *LoadScoreEnvWeights  // 启动时预解析
   }
   
   // 避免热路径的：
   // - envFloat() 查找
   // - ParseFloat() 转换
   // - 临时字符串分配
   ```

3. **并行评分（高QPS场景）**
   ```go
   func (r *Router) planByTierParallel(candidates []provider.Candidate) {
       var wg sync.WaitGroup
       scores := make([]float64, len(candidates))
       
       for i, cand := range candidates {
           wg.Add(1)
           go func(idx int, c provider.Candidate) {
               defer wg.Done()
               scores[idx] = r.calculateLoadScore(c)
           }(i, cand)
       }
       
       wg.Wait()
       // 排序...
   }
   ```

### 6.2 URSM v2状态管理延迟

#### 问题描述
```go
func (r *Router) PlanCandidatesWithContext(...) {
    // 两次Redis交互，各50ms超时
    readyCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
    ready := r.URSMv2.Ready(readyCtx)  // ← Redis GET
    cancel()
    
    if ready {
        ctx2, cancel2 := context.WithTimeout(ctx, 50*time.Millisecond)
        views, err := r.URSMv2.FilterAndScoreReadyWithSource(ctx2, seeds)  // ← Redis MGET
        cancel2()
    }
}
```

**延迟分析：**
- 顺序两次Redis调用：最多100ms
- P99延迟可能接近超时
- 失败时回退到本地镜像

#### 优化建议
1. **合并Redis调用**
   ```go
   // 使用Lua脚本或Pipeline
   pipe := redis.Pipeline()
   readyCmd := pipe.Get(ctx, "ursm:ready")
   viewsCmd := pipe.MGet(ctx, keys...)
   _, err := pipe.Exec(ctx)
   ```

2. **异步预热**
   ```go
   // 后台goroutine定期刷新状态到本地
   go func() {
       ticker := time.NewTicker(10 * time.Second)
       for range ticker.C {
           r.URSMv2.WarmLocalCache()
       }
   }()
   ```

3. **降级路径优化（已实现）**
   ```go
   // ✅ 已有outage fallback机制
   if kept := r.tryURSMOutageFallback(ctx, candidates, seeds); len(kept) > 0 {
       candidates = kept
       outageServed = true
   }
   ```

### 6.3 负载均衡器锁竞争

#### 问题描述
```go
type LoadBalancer struct {
    mu               sync.Mutex  // ← 全局锁
    roundRobinIndex  map[int]uint64
    connectionCounts map[int]int
    weightedCurrent  map[int]map[int]int
}

func (lb *LoadBalancer) SelectNode(...) *Node {
    lb.mu.Lock()  // ← 每次选择都加锁
    defer lb.mu.Unlock()
    return lb.selectLocked(...)
}
```

**竞争分析：**
- 高QPS下所有请求串行化
- 锁持有时间包括策略计算
- 一致性哈希最慢（O(N log N)排序）

#### 优化建议
1. **读写分离**
   ```go
   type LoadBalancer struct {
       mu               sync.RWMutex  // ← 读写锁
       // ...
   }
   
   func (lb *LoadBalancer) SelectNode(...) *Node {
       // Best-only和一致性哈希不需要写状态
       if lb.strategy == StrategyBestOnly || lb.strategy == StrategyConsistentHash {
           lb.mu.RLock()
           defer lb.mu.RUnlock()
           return lb.selectReadOnly(...)
       }
       
       // 其他策略需要写锁
       lb.mu.Lock()
       defer lb.mu.Unlock()
       return lb.selectLocked(...)
   }
   ```

2. **分片锁**
   ```go
   type LoadBalancer struct {
       shards []*lbShard  // 16个分片
   }
   
   type lbShard struct {
       mu               sync.Mutex
       roundRobinIndex  map[int]uint64
       // ...
   }
   
   func (lb *LoadBalancer) getShard(subscriptionID int) *lbShard {
       return lb.shards[subscriptionID % len(lb.shards)]
   }
   ```

3. **无锁策略（best-only/consistent-hash）**
   ```go
   // best-only不需要任何状态
   func selectBestOnly(nodes []*Node) *Node {
       return nodes[0]  // 无锁
   }
   
   // 一致性哈希预构建环
   type LoadBalancer struct {
       ringCache atomic.Value  // *consistentHashRing
   }
   ```

### 6.4 端点选择器协议转换

#### 问题描述
```go
func Select(cand CandidateLite, wantProtocol, wantFamily) Decision {
    // 1. 协议命名空间转换
    wantProtocol = NormalizeWantProtocol(wantProtocol)  // map查找
    
    // 2. 遍历端点列表（通常2-5个）
    primary := pickBest(cand.NativeEndpoints, wantProtocol, wantFamily)
    
    // 3. 排序（即使只有1个匹配）
    sortEndpointsByPriority(matched)
}

func NormalizeWantProtocol(p Protocol) Protocol {
    if c, ok := irToCatalogProtocol[string(p)]; ok {  // ← map查找
        return Protocol(c)
    }
    return p
}
```

**开销分析：**
- 每个请求调用1次
- map查找：O(1)但有哈希计算
- 端点遍历：O(K)，K通常≤5
- 排序：O(K log K)

**影响：**
- 单次开销小（<1μs）
- 高QPS累积可观

#### 优化建议
1. **缓存归一化结果**
   ```go
   var normalizeCache sync.Map  // string → Protocol
   
   func NormalizeWantProtocolCached(p Protocol) Protocol {
       if cached, ok := normalizeCache.Load(string(p)); ok {
           return cached.(Protocol)
       }
       
       normalized := NormalizeWantProtocol(p)
       normalizeCache.Store(string(p), normalized)
       return normalized
   }
   ```

2. **预排序端点**
   ```go
   // 在候选者加载时排序一次
   func (c *Client) resolveCandidatesFromDB(...) {
       for _, cand := range candidates {
           // 解析NativeEndpoints
           cand.NativeEndpoints = parseEndpoints(...)
           
           // 预排序
           sortEndpointsByPriority(cand.NativeEndpoints)  // ← 一次性
       }
   }
   ```

3. **快速路径（单端点）**
   ```go
   func Select(cand CandidateLite, ...) Decision {
       if len(cand.NativeEndpoints) == 1 {
           ep := cand.NativeEndpoints[0]
           return fastPathDecision(ep, ...)  // 跳过排序
       }
       // 正常流程...
   }
   ```

### 6.5 缓存TTL权衡

#### 问题描述
```go
const (
    candidateCacheTTL = 30 * time.Second  // ← 相对较短
)
```

**权衡分析：**

| TTL | 优点 | 缺点 |
|-----|------|------|
| **30秒（当前）** | • 配置变更快速生效<br>• 状态变化及时反映 | • 高QPS下缓存命中率低<br>• DB压力大 |
| **5分钟** | • 更高命中率<br>• DB压力小 | • 配置变更延迟<br>• 状态不一致窗口长 |
| **10分钟** | • 最高命中率<br>• 最小DB压力 | • 变更延迟不可接受 |

**数据支持：**
- 假设QPS=1000
- 模型数=50
- 平均每模型20 req/s

**缓存性能：**
```
TTL=30s:
- 每30秒每模型查询DB一次
- DB查询频率 = 50模型 / 30s = 1.67 QPS
- 命中率 = (600 - 1.67) / 600 = 99.7%

TTL=300s:
- 每300秒每模型查询DB一次
- DB查询频率 = 50模型 / 300s = 0.17 QPS
- 命中率 = (600 - 0.17) / 600 = 99.97%
```

#### 优化建议
1. **分层TTL**
   ```go
   const (
       hotModelCacheTTL    = 30 * time.Second   // 热门模型
       normalModelCacheTTL = 2 * time.Minute    // 普通模型
       coldModelCacheTTL   = 5 * time.Minute    // 冷门模型
   )
   
   func selectTTL(model string, requestRate float64) time.Duration {
       if requestRate > 10 {  // >10 req/s
           return hotModelCacheTTL
       } else if requestRate > 1 {
           return normalModelCacheTTL
       }
       return coldModelCacheTTL
   }
   ```

2. **主动失效 + 长TTL**
   ```go
   const (
       candidateCacheTTL = 10 * time.Minute  // 更长TTL
   )
   
   // 配置变更时主动失效
   func onProviderConfigChange(providerID int) {
       InvalidateCandidateCacheForProvider(providerID)
   }
   
   // 状态变更时主动失效
   func onCredentialStateChange(credentialID int) {
       InvalidateCandidateCacheForCredential(credentialID)
   }
   ```

3. **渐进式过期**
   ```go
   type cacheEntry struct {
       value     *resolveResponse
       cachedAt  time.Time
       softTTL   time.Duration  // 软过期
       hardTTL   time.Duration  // 硬过期
   }
   
   func (e *cacheEntry) needsRefresh(now time.Time) bool {
       return now.Sub(e.cachedAt) > e.softTTL
   }
   
   func (c *Client) getCandidates(...) {
       cached, ok := c.candCache[key]
       
       if ok && cached.validAt(now) {
           if cached.needsRefresh(now) {
               // 异步刷新，立即返回旧数据
               go c.refreshCache(key)
           }
           return cached.value
       }
       
       // 硬过期或未命中，同步查询
       return c.queryCandidates(...)
   }
   ```

---

## 7. 并发处理能力

### 7.1 并发控制机制

#### 7.1.1 多层队列调度
**文件：** `domains/dispatch`

```go
type Pipeline struct {
    // 全局队列（所有credential共享）
    globalQueue chan *QueuedRequest
    
    // 每credential一个队列
    credQueues map[int]*credentialQueue
    
    // Worker池
    workers []*worker
}

type credentialQueue struct {
    queue     chan *QueuedRequest
    workers   int              // 专用worker数
    throttle  *ratelimit.Throttle  // 令牌桶
    semaphore chan struct{}    // 并发信号量
}
```

**配置（hotconfig）：**
```go
type Config struct {
    GlobalQueueDepth      int  // 全局队列深度（默认10000）
    CredQueueDepth        int  // 单credential队列深度（默认100）
    GlobalWorkers         int  // 全局worker数（默认50）
    CredWorkers           int  // 单credential worker数（默认2）
    MaxRetries            int  // 最大重试次数（默认3）
    RetryBudgetPercentage int  // 重试预算百分比（默认30）
}
```

**队列层次：**
```
Request Flow:
    ↓
[Global Queue] 深度10000, 50 workers
    ↓
[Tier Routing] 分发到credential队列
    ↓
[Credential Queues] 每个深度100, 2 workers
    ├─ Credential 123: [req1, req2, ...]
    ├─ Credential 456: [req3, req4, ...]
    └─ Credential 789: [req5, req6, ...]
    ↓
[Throttling] 3种模式
    ├─ concurrency: 信号量限制
    ├─ rpm: 令牌桶（请求/分钟）
    └─ tpm: 令牌桶（token/分钟）
    ↓
Upstream Provider
```

#### 7.1.2 并发模式

**A. Concurrency模式**
```go
type credentialQueue struct {
    semaphore chan struct{}  // 容量 = ConcurrencyLimit
}

func (q *credentialQueue) acquire(ctx context.Context) error {
    select {
    case q.semaphore <- struct{}{}:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(maxQueueWait):
        return ErrQueueTimeout
    }
}

func (q *credentialQueue) release() {
    <-q.semaphore
}
```

**B. RPM模式（请求/分钟）**
```go
type rpmThrottle struct {
    limiter *rate.Limiter  // token bucket
}

func newRPMThrottle(rpm int) *rpmThrottle {
    // 每分钟rpm个token，突发容许rpm个
    return &rpmThrottle{
        limiter: rate.NewLimiter(rate.Limit(rpm/60.0), rpm),
    }
}

func (t *rpmThrottle) Wait(ctx context.Context) error {
    return t.limiter.Wait(ctx)
}
```

**C. TPM模式（token/分钟）**
```go
type tpmThrottle struct {
    limiter *rate.Limiter
}

func (t *tpmThrottle) WaitN(ctx context.Context, tokens int) error {
    // 请求前估算token数
    estimatedTokens := estimateInputTokens(request)
    
    // 等待足够的token
    return t.limiter.WaitN(ctx, estimatedTokens)
}
```

### 7.2 FpSlots（指纹槽）机制

**目的：** 控制虚拟用户身份池大小

```go
type Manager struct {
    pools map[int]*credentialPool  // credentialID → pool
}

type credentialPool struct {
    limit int              // 池大小（FpSlotLimit）
    slots map[string]*slot  // sessionID → slot
}

type slot struct {
    fingerprint Fingerprint  // 虚拟身份
    acquiredAt  time.Time
    credential  int
}

func (m *Manager) Acquire(
    ctx context.Context,
    credentialID int,
    sessionID string,
    limit *int,
) (*Fingerprint, error) {
    pool := m.getPool(credentialID, limit)
    
    // 1. 尝试复用已分配的槽
    if slot, ok := pool.slots[sessionID]; ok {
        return &slot.fingerprint, nil
    }
    
    // 2. 检查是否还有空闲槽
    if len(pool.slots) >= pool.limit {
        return nil, ErrNoFreeSlots
    }
    
    // 3. 分配新槽
    fp := generateFingerprint()
    pool.slots[sessionID] = &slot{
        fingerprint: fp,
        acquiredAt:  time.Now(),
        credential:  credentialID,
    }
    
    return &fp, nil
}
```

**槽位管理：**
```go
// 槽位状态
type NodeState struct {
    SlotLimit  int    // 总槽位
    SlotsUsed  int    // 已用槽位
    SlotsFree  int    // 空闲槽位
    Pressure   float64 // 压力系数 = used/limit
}

// 路由评分中的headroom维度
func calculateHeadroomScore(state *NodeState) float64 {
    if state.SlotLimit == 0 {
        return 1.0  // 无限槽位
    }
    
    return float64(state.SlotsFree) / float64(state.SlotLimit)
}
```

### 7.3 熔断器

**文件：** `domains/credential/circuit.go`

```go
type Circuit struct {
    breakers sync.Map  // key → *breaker
}

type breaker struct {
    state           State  // CLOSED, OPEN, HALF_OPEN
    failures        int
    successes       int
    lastFailureTime time.Time
    lastStateChange time.Time
}

const (
    StateClosed  State = "closed"      // 正常
    StateOpen    State = "open"        // 熔断
    StateHalfOpen State = "half_open"  // 恢复中
)

func (c *Circuit) RecordFailure(providerID, credentialID int, model string) {
    key := fmt.Sprintf("%d/%d/%s", providerID, credentialID, model)
    b := c.getBreaker(key)
    
    b.mu.Lock()
    defer b.mu.Unlock()
    
    b.failures++
    b.lastFailureTime = time.Now()
    
    // 达到阈值，打开熔断器
    if b.failures >= failureThreshold && b.state == StateClosed {
        b.state = StateOpen
        b.lastStateChange = time.Now()
    }
}

func (c *Circuit) RecordSuccess(providerID, credentialID int, model string) {
    key := fmt.Sprintf("%d/%d/%s", providerID, credentialID, model)
    b := c.getBreaker(key)
    
    b.mu.Lock()
    defer b.mu.Unlock()
    
    b.successes++
    
    if b.state == StateHalfOpen {
        // 恢复期成功足够，关闭熔断器
        if b.successes >= successThreshold {
            b.state = StateClosed
            b.failures = 0
            b.successes = 0
            b.lastStateChange = time.Now()
        }
    } else if b.state == StateClosed {
        // 正常态成功，重置失败计数
        b.failures = 0
    }
}

func (c *Circuit) IsAvailable(providerID, credentialID int, model string) bool {
    key := fmt.Sprintf("%d/%d/%s", providerID, credentialID, model)
    b := c.getBreaker(key)
    
    b.mu.RLock()
    defer b.mu.RUnlock()
    
    switch b.state {
    case StateClosed:
        return true
    case StateOpen:
        // 检查是否可以进入半开状态
        if time.Since(b.lastStateChange) > recoveryTimeout {
            b.state = StateHalfOpen
            b.successes = 0
            return true
        }
        return false
    case StateHalfOpen:
        return true
    }
    return false
}
```

**恢复期渐进回流：**
```go
func recoveryPenaltyForCandidate(cand provider.Candidate, recovery interface{}) float64 {
    if recovery == nil {
        return 1.0
    }
    
    recoveredAt := recovery.RecoveredAt(
        cand.ProviderID, 
        cand.CredentialID, 
        cand.BindingRawModel(),
    )
    
    if recoveredAt.IsZero() {
        return 1.0  // 未恢复或一直健康
    }
    
    elapsed := time.Since(recoveredAt)
    if elapsed > recoveryRampDuration {
        return 1.0  // 恢复期已过
    }
    
    // 线性爬坡：0.1 → 1.0
    ramp := 0.1 + 0.9*(elapsed.Seconds()/recoveryRampDuration.Seconds())
    return ramp
}
```

### 7.4 并发能力估算

#### 配置示例
```go
// 默认配置
globalWorkers = 50
credWorkers = 2
globalQueueDepth = 10000
credQueueDepth = 100

// 假设
activeCredentials = 100
avgConcurrencyLimit = 10
```

#### 理论容量
```
全局层：
- 队列容量：10000 请求
- Worker数：50
- 吞吐量：50 × (1000ms / avgLatency)

Credential层：
- 总队列容量：100 cred × 100 depth = 10000 请求
- 总Worker数：100 cred × 2 workers = 200
- 实际并发：受ConcurrencyLimit限制
  = 100 cred × 10 limit = 1000 并发请求

总吞吐量（稳态）：
假设平均延迟300ms
= 1000 并发 / 0.3s
= 3333 req/s
```

#### 突发处理
```
队列缓冲：
- 全局：10000
- Credential：10000
- 总计：20000 请求

突发容量：
20000 / 0.3s平均延迟 = 66666 req/s峰值
持续时间 = 20000 / 3333稳态 = 6秒
```

---

## 8. 关键代码路径追踪

### 8.1 完整请求路径

```
1. HTTP请求到达
   └─ Handler.ServeHTTP() [handler.go]

2. 认证与授权
   └─ AdminMiddleware() / AuthMiddleware()
       └─ identity.Verify(token)

3. 参数解析与验证
   └─ parseExecParams()
       ├─ 解析body
       ├─ 模型名归一化
       └─ 检测协议

4. 自动路由（可选）
   └─ autoroute.RewriteRequest()
       ├─ 任务分类
       ├─ 推荐模型
       └─ 重写body

5. 获取候选者
   └─ providerClient.GetCandidates()
       ├─ 检查缓存 [30s TTL]
       ├─ 缓存未命中 → SQL查询
       │   └─ LATERAL JOIN provider_endpoint_protocols
       └─ 解密APIKey [5min TTL]

6. 路由规划
   └─ Router.PlanCandidatesWithContext()
       ├─ URSM v2状态过滤 [Redis, 50ms timeout]
       ├─ 可用性过滤
       ├─ 健康检查过滤
       ├─ 压力惩罚应用
       └─ 分层排序
           ├─ Tier 1候选评分
           ├─ Tier 2候选评分
           ├─ Tier 3候选评分
           └─ Tier 9候选评分

7. 端点选择
   └─ endpointselect.Select()
       ├─ 协议归一化
       ├─ Stage 1-4匹配
       └─ 返回Decision

8. 负载均衡
   └─ LoadBalancer.SelectNode()
       ├─ 应用策略（best/rr/wrr/lc/ch）
       └─ 地域亲和性

9. 请求入队
   └─ Pipeline.Submit()
       ├─ 全局队列
       └─ Credential队列

10. 请求限流
    └─ Throttle.Wait()
        ├─ Concurrency模式 → semaphore
        ├─ RPM模式 → token bucket
        └─ TPM模式 → token bucket (pre-estimated)

11. 执行转发
    └─ dispatchForward()
        ├─ 构建上游请求
        ├─ 发送HTTP/HTTPS
        └─ 流式转发响应

12. 状态更新
    └─ 成功/失败处理
        ├─ 熔断器记录
        ├─ 健康追踪器更新
        ├─ 缓存失效（如需）
        └─ 指标上报
```

### 8.2 关键数据结构流转

```go
// 1. HTTP Request → ExecParams
type ExecParams struct {
    RequestID              string
    TenantID               string
    SessionID              string
    ClientModel            string
    Protocol               string
    Policy                 *provider.Policy
    PinCredentialID        *int
    DispatchAutoProfile    string
    DispatchAutoTask       string
    DispatchAutoSignals    []autoroute.SignalWeight
    // ...
}

// 2. ExecParams → 候选者查询
func (c *Client) GetCandidates(
    ctx context.Context,
    model string,       // ← params.ClientModel
    profile string,     // ← params.DispatchAutoProfile
    tenantID string,    // ← params.TenantID
) ([]Candidate, *Policy, error)

// 3. 候选者 → 路由输入
type Candidate struct {
    CredentialID     int
    ProviderID       int
    BaseURL          string
    Protocol         string
    Tier             int
    Weight           int
    RawModel         string
    BalanceUSD       *float64
    ConcurrencyLimit *int
    FpSlotLimit      *int
    NativeEndpoints  []EndpointLite  // ← 聚合的端点
    // ...
}

// 4. 路由输出 → 端点选择
type Decision struct {
    Protocol     Protocol
    BaseURL      string
    EndpointID   int64
    VendorNative string
    Passthrough  bool
    MatchRule    string
    Reason       string
}

// 5. 端点选择 → 负载均衡
type Node struct {
    ID             int
    ResponseTimeMs int
    Location       string
    // ...
}

// 6. 负载均衡 → 请求转发
type UpstreamRequest struct {
    Method  string
    URL     string
    Headers map[string]string
    Body    []byte
}
```

---

## 9. 配置调优建议

### 9.1 路由层调优

#### 环境变量
```bash
# 评分权重（0.0-1.0）
LOAD_SCORE_STICKY_WEIGHT=0.3
LOAD_SCORE_RECENCY_WEIGHT=0.2
LOAD_SCORE_BALANCE_WEIGHT=0.15
LOAD_SCORE_PLAN_QUOTA_WEIGHT=0.15
LOAD_SCORE_HEADROOM_WEIGHT=0.1
LOAD_SCORE_CAPACITY_WEIGHT=0.05
LOAD_SCORE_RECOVERY_WEIGHT=0.05

# 压力感知路由
PRESSURE_AWARE_ROUTING=true

# 端点选择器
FF_ENDPOINT_SELECTOR=false  # 默认false，渐进开启
```

#### 建议配置（高QPS场景）
```bash
# 降低sticky权重，提高负载均衡效果
LOAD_SCORE_STICKY_WEIGHT=0.1
LOAD_SCORE_CAPACITY_WEIGHT=0.3  # 提高容量权重
PRESSURE_AWARE_ROUTING=true     # 启用压力感知
```

### 9.2 队列调优

#### Hotconfig配置
```go
// 高并发场景
llmgw_dispatch_global_queue_depth = 20000  // 加倍
llmgw_dispatch_global_workers = 100        // 增加worker
llmgw_dispatch_cred_queue_depth = 200      // 增加credential队列
llmgw_dispatch_max_retries = 2             // 减少重试
llmgw_dispatch_retry_budget_pct = 20       // 降低重试预算

// 低延迟场景
llmgw_dispatch_global_queue_depth = 5000
llmgw_dispatch_global_workers = 50
llmgw_dispatch_cred_workers = 4            // 增加credential worker
llmgw_dispatch_max_retries = 1
```

### 9.3 缓存调优

#### 分场景TTL
```go
// 场景A：配置变更频繁
candidateCacheTTL = 15 * time.Second

// 场景B：配置稳定、高QPS
candidateCacheTTL = 2 * time.Minute
// 配合主动失效机制

// 场景C：极高QPS、可容忍短暂不一致
candidateCacheTTL = 5 * time.Minute
// 启用异步刷新
```

### 9.4 负载均衡策略选择

| 场景 | 推荐策略 | 理由 |
|------|---------|------|
| **延迟敏感** | weighted_rr | 基于延迟的权重，自动偏向快节点 |
| **长连接** | least_conn | 均衡实际负载 |
| **会话保持** | consistent_hash | 同用户固定节点 |
| **简单高效** | best_only | 最低开销 |
| **均匀分布** | round_robin | 简单均匀 |

### 9.5 URSM v2配置

```bash
# 超时配置
URSM_V2_READY_TIMEOUT=50ms        # Ready检查超时
URSM_V2_FILTER_TIMEOUT=50ms       # 过滤超时

# 降级配置
URSM_V2_OUTAGE_GRACE_SECONDS=300  # 5分钟outage grace

# 模式选择
URSM_V2_MODE=authoritative  # authoritative/canary/shadow/off
```

**模式建议：**
- **Shadow**: 生产观察阶段，零影响
- **Canary**: 部分流量验证
- **Authoritative**: 完全启用，严格状态管理
- **Off**: 完全禁用，回退到legacy

---

## 10. 监控指标

### 10.1 关键指标

#### 路由层
```
# 候选者缓存
llmgw_candidate_cache_hit_rate
llmgw_candidate_cache_miss_total
llmgw_candidate_cache_invalidations_total{type="credential|all"}

# 路由决策
llmgw_routing_plan_duration_seconds
llmgw_routing_candidates_filtered_total
llmgw_routing_state_source{source="authoritative|fallback|outage_mirror"}

# 评分
llmgw_routing_score_calculation_duration_seconds
llmgw_routing_sticky_hits_total
llmgw_routing_sticky_misses_total
```

#### 端点选择器
```
# 匹配阶段分布
llmgw_endpoint_selector_stage{stage="1a|1b|1c|2|4"}

# 协议转换
llmgw_endpoint_selector_protocol_normalization_total
```

#### 负载均衡器
```
# 策略使用
llmgw_load_balancer_strategy{strategy="best_only|round_robin|..."}

# 节点选择
llmgw_load_balancer_node_selected_total{node_id}

# 连接数（least_conn策略）
llmgw_load_balancer_connections{node_id}
```

#### 队列系统
```
# 队列深度
llmgw_dispatch_queue_depth{queue="global|credential"}
llmgw_dispatch_queue_full_total

# 等待时间
llmgw_dispatch_queue_wait_duration_seconds

# 重试
llmgw_dispatch_retries_total{reason="..."}
```

### 10.2 告警规则

```yaml
# 高队列深度
- alert: DispatchQueueHigh
  expr: llmgw_dispatch_queue_depth{queue="global"} > 8000
  for: 5m
  
# URSM v2不可用
- alert: URSMv2Down
  expr: rate(llmgw_routing_state_source{source="fallback"}[5m]) > 0.5
  for: 2m

# 缓存命中率低
- alert: CandidateCacheHitRateLow
  expr: rate(llmgw_candidate_cache_hit_rate[5m]) < 0.95
  for: 10m

# 熔断器打开
- alert: CircuitBreakerOpen
  expr: llmgw_circuit_breaker_state{state="open"} > 0
  for: 1m
```

---

## 11. 总结与建议

### 11.1 架构亮点

1. **分层设计清晰**
   - Handler → Executor → Router → LoadBalancer
   - 每层职责明确，易于测试和维护

2. **端点选择器算法**
   - 四阶段匹配兼顾精确性和兼容性
   - 协议/家族双维度决策
   - 权重优先级合理

3. **多策略负载均衡**
   - 5种策略覆盖不同场景
   - 平滑加权轮询算法优秀
   - 一致性哈希支持会话保持

4. **缓存机制完善**
   - 生成计数器防脏写
   - 按credential失效避免雷鸣群
   - 负缓存减少无效重试

5. **并发控制多样**
   - 多层队列调度
   - 3种限流模式
   - FpSlots创新机制

### 11.2 改进建议优先级

#### P0（立即实施）
1. **批量查询FpSlots状态** - 减少Redis往返
2. **分片锁优化LoadBalancer** - 降低锁竞争
3. **预排序端点** - 避免重复排序开销

#### P1（近期实施）
1. **分层缓存TTL** - 平衡一致性和性能
2. **异步刷新缓存** - 消除查询延迟
3. **并行评分（高QPS）** - 利用多核

#### P2（长期优化）
1. **URSM v2 Pipeline合并** - 减少Redis调用
2. **协议归一化缓存** - 避免重复查找
3. **智能队列调度** - 自适应worker数

### 11.3 运维建议

1. **渐进式启用新特性**
   - FF_ENDPOINT_SELECTOR: 先shadow，再canary
   - URSM v2: 先shadow模式观察，再authoritative

2. **监控覆盖**
   - 重点关注队列深度和等待时间
   - 缓存命中率低于95%需调查
   - URSM v2 fallback率监控

3. **配置调优**
   - 根据实际QPS调整队列和worker数
   - 评分权重根据业务需求微调
   - 缓存TTL在一致性和性能间权衡

4. **容量规划**
   - 理论吞吐 = credentials × concurrency_limit / avg_latency
   - 预留20-30%缓冲应对突发
   - 定期review热门credential的配额

### 11.4 技术债务

1. **Router.weightCounters使用sync.Map**
   - 写竞争可能成为瓶颈
   - 建议改为分片map或atomic计数器

2. **端点选择每次构建虚拟节点环**
   - 一致性哈希的环可预构建
   - 节点变化时增量更新

3. **评分维度硬编码**
   - 建议支持动态权重配置
   - 通过admin API调整

4. **缓存失效不够精细**
   - InvalidateAllCandidateCache仍在使用
   - 建议全部迁移到按credential失效

---

## 附录A：协议映射表

| IR协议 | Catalog协议 | 用途 |
|--------|-------------|------|
| openai-chat | openai-completions | OpenAI聊天 |
| ollama-chat | ollama-native | Ollama聊天 |
| openai-completions | openai-completions | OpenAI补全 |
| openai-responses | openai-responses | OpenAI Responses |
| anthropic-messages | anthropic-messages | Claude |
| gemini-generate | gemini-generate | Gemini |

## 附录B：评分权重默认值

```go
type LoadScoreEnvWeights struct {
    Sticky    float64  // 0.3
    Recency   float64  // 0.2
    Balance   float64  // 0.15
    PlanQuota float64  // 0.15
    Headroom  float64  // 0.1
    Capacity  float64  // 0.05
    Recovery  float64  // 0.05
}
```

## 附录C：SQL查询示例

### 候选者查询（简化）
```sql
SELECT 
    c.id AS credential_id,
    p.id AS provider_id,
    p.base_url,
    p.protocol,
    -- ... 其他列 ...
    (
        SELECT jsonb_agg(
            jsonb_build_object(
                'id', pep.id,
                'protocol', pep.protocol,
                'base_url', pep.base_url,
                'is_primary', pep.is_primary,
                'vendor_native', pep.vendor_native,
                'enabled', pep.enabled,
                'weight', pep.weight
            )
        )
        FROM provider_endpoint_protocols pep
        WHERE pep.provider_id = p.id
          AND pep.enabled = true
        ORDER BY pep.is_primary DESC, pep.weight DESC
    ) AS native_endpoints
FROM 
    credentials c
    JOIN providers p ON c.provider_id = p.id
    JOIN model_offers mo ON ...
WHERE
    mo.canonical_name = $1  -- 模型名
    AND c.lifecycle_status = 'active'
    AND p.enabled = true
    -- ... 其他过滤条件 ...
ORDER BY
    manual_priority DESC,
    tier ASC,
    composite_score DESC;
```

---

**报告生成时间：** 2026-11-28  
**分析范围：** llm-gateway-go 完整路由与负载均衡系统  
**代码版本：** 基于主档 L148 (2026-11-28)

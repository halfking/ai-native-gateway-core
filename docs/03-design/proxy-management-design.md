# 代理管理系统设计方案

## 1. 需求背景

### 1.1 问题描述

在国内环境部署 LLM Gateway 时，访问海外供应商（如 Groq、OpenAI、Anthropic 等）会被 GFW 阻挡，导致：
- 探活失败，无法注册供应商
- 运行时请求失败，影响服务可用性

### 1.2 现有资源

- NPS 代理服务部署在 252 服务器，监听 8080 端口
- 订阅地址：`http://<env:HOST_252_IP>:8080/subscribe?token=4956b9532968e00e9f0e710e4ccf262d`
- 提供科学上网能力

### 1.3 目标

1. **系统级代理管理**：支持多个代理订阅源，自动解析和更新代理节点
2. **供应商级代理配置**：每个供应商可独立配置是否使用代理
3. **智能代理路由**：
   - 国内供应商直连
   - 海外供应商自动使用代理
   - 手动覆盖自动判断
4. **探活支持代理**：free-pool 探活时可选择是否使用代理
5. **简洁可用**：最小化配置，开箱即用

## 2. 系统架构

### 2.1 核心组件

```
┌─────────────────────────────────────────────────────────────┐
│                     LLM Gateway                              │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌──────────────┐      ┌──────────────┐      ┌───────────┐ │
│  │ Proxy Manager│◄─────│ Subscription │      │  Provider │ │
│  │   (全局)     │      │   Parser     │      │  (供应商) │ │
│  └──────┬───────┘      └──────────────┘      └─────┬─────┘ │
│         │                                            │       │
│         │ ┌────────────────────────────────────┐   │       │
│         └─┤  HTTP Client Factory               │◄──┘       │
│           │  - 直连 Client                      │           │
│           │  - 代理 Client (自动选择节点)      │           │
│           └────────────────────────────────────┘           │
│                                                               │
└─────────────────────────────────────────────────────────────┘
         │                                  │
         │ 国内供应商 (直连)              │ 海外供应商 (代理)
         ▼                                  ▼
   ┌──────────┐                      ┌──────────┐
   │ 智谱 AI  │                      │  Groq    │
   │ 硅基流动 │                      │  OpenAI  │
   └──────────┘                      └──────────┘
```

### 2.2 数据模型

#### 2.2.1 代理订阅表 (proxy_subscriptions)

```sql
CREATE TABLE proxy_subscriptions (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,                    -- 订阅名称
    subscribe_url TEXT NOT NULL,                   -- 订阅地址
    status VARCHAR(20) DEFAULT 'active',           -- active/disabled/error
    last_fetch_at TIMESTAMP,                       -- 上次拉取时间
    last_fetch_status VARCHAR(20),                 -- success/failed
    last_error TEXT,                               -- 错误信息
    node_count INTEGER DEFAULT 0,                  -- 节点数量
    priority INTEGER DEFAULT 0,                    -- 优先级
    notes TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_proxy_subs_status ON proxy_subscriptions(status);
```

#### 2.2.2 代理节点表 (proxy_nodes)

```sql
CREATE TABLE proxy_nodes (
    id SERIAL PRIMARY KEY,
    subscription_id INTEGER REFERENCES proxy_subscriptions(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,                    -- 节点名称
    protocol VARCHAR(20) NOT NULL,                 -- http/https/socks5/ss/vmess/trojan
    server VARCHAR(255) NOT NULL,                  -- 服务器地址
    port INTEGER NOT NULL,                         -- 端口
    username VARCHAR(100),                         -- 用户名（HTTP/SOCKS5）
    password TEXT,                                 -- 密码（加密存储）
    config JSONB,                                  -- 协议特定配置
    location VARCHAR(50),                          -- 地理位置
    status VARCHAR(20) DEFAULT 'active',           -- active/disabled/unhealthy
    health_check_url TEXT,                         -- 健康检查 URL
    last_health_check_at TIMESTAMP,
    last_health_check_status VARCHAR(20),          -- success/failed/timeout
    response_time_ms INTEGER,                      -- 响应时间(ms)
    success_rate FLOAT DEFAULT 1.0,                -- 成功率
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_proxy_nodes_sub_id ON proxy_nodes(subscription_id);
CREATE INDEX idx_proxy_nodes_status ON proxy_nodes(status);
CREATE INDEX idx_proxy_nodes_health ON proxy_nodes(status, response_time_ms);
```

#### 2.2.3 供应商代理配置 (providers 表新增字段)

```sql
-- 已存在 egress_profile 字段，扩展其语义：
-- - 'direct': 直连
-- - 'proxy': 使用代理
-- - 'auto': 自动判断（根据 domestic 字段）
ALTER TABLE providers ADD COLUMN IF NOT EXISTS egress_profile VARCHAR(20) DEFAULT 'auto';

-- 新增：指定特定代理订阅 ID（可选，为空则自动选择）
ALTER TABLE providers ADD COLUMN IF NOT EXISTS proxy_subscription_id INTEGER 
    REFERENCES proxy_subscriptions(id) ON DELETE SET NULL;

CREATE INDEX idx_providers_egress ON providers(egress_profile);
```

#### 2.2.4 供应商域名分类表 (provider_domains)

用于自动判断是否需要代理：

```sql
CREATE TABLE provider_domains (
    id SERIAL PRIMARY KEY,
    domain VARCHAR(255) NOT NULL UNIQUE,           -- 域名（如 api.groq.com）
    catalog_code VARCHAR(100),                     -- 关联 catalog_code
    requires_proxy BOOLEAN DEFAULT FALSE,          -- 是否需要代理
    location VARCHAR(50),                          -- 地理位置标记
    probe_status VARCHAR(20),                      -- reachable/blocked/unknown
    last_probe_at TIMESTAMP,
    notes TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_provider_domains_requires_proxy ON provider_domains(requires_proxy);
CREATE INDEX idx_provider_domains_catalog ON provider_domains(catalog_code);
```

### 2.3 配置优先级

供应商请求时的代理选择逻辑：

```
1. providers.egress_profile = 'direct'  → 直连
2. providers.egress_profile = 'proxy'   → 使用代理
   2.1 如果指定 proxy_subscription_id → 使用该订阅的节点
   2.2 否则 → 从所有 active 订阅中选择最优节点
3. providers.egress_profile = 'auto'    → 智能判断
   3.1 查询 provider_domains 表，根据 base_url 的域名判断
   3.2 如果 requires_proxy = true → 使用代理
   3.3 如果 domestic = true 或 requires_proxy = false → 直连
   3.4 未知域名 → 默认直连（可配置）
```

## 3. 实现细节

### 3.1 代理订阅解析

支持多种订阅格式：

#### 3.1.1 NPS 订阅格式

解析 `http://<env:HOST_252_IP>:8080/subscribe?token=xxx`，返回格式：
- Base64 编码的节点列表
- 每行一个节点 URI

#### 3.1.2 通用订阅格式

- **V2Ray/Clash** 订阅：`vmess://`, `vless://`, `trojan://`
- **Shadowsocks** 订阅：`ss://`
- **HTTP/SOCKS5** 代理：`http://`, `socks5://`

#### 3.1.3 解析器实现

```go
// ProxySubscriptionParser 订阅解析器
type ProxySubscriptionParser interface {
    Parse(url string) ([]ProxyNode, error)
}

// ProxyNode 代理节点
type ProxyNode struct {
    Name     string
    Protocol string // http/https/socks5/ss/vmess/trojan
    Server   string
    Port     int
    Username string
    Password string
    Config   map[string]any // 协议特定配置
}
```

### 3.2 HTTP 客户端代理支持

#### 3.2.1 客户端工厂

```go
// HTTPClientFactory 创建 HTTP 客户端
type HTTPClientFactory struct {
    proxyManager *ProxyManager
}

// GetClient 获取客户端（根据供应商配置）
func (f *HTTPClientFactory) GetClient(provider *Provider) *http.Client {
    egressProfile := provider.EgressProfile
    
    switch egressProfile {
    case "direct":
        return f.getDirectClient()
    case "proxy":
        return f.getProxyClient(provider.ProxySubscriptionID)
    case "auto":
        if f.requiresProxy(provider.BaseURL) {
            return f.getProxyClient(nil)
        }
        return f.getDirectClient()
    default:
        return f.getDirectClient()
    }
}

// requiresProxy 判断域名是否需要代理
func (f *HTTPClientFactory) requiresProxy(baseURL string) bool {
    domain := extractDomain(baseURL)
    record := f.proxyManager.LookupDomain(domain)
    if record != nil {
        return record.RequiresProxy
    }
    // 未知域名默认直连（可配置为默认代理）
    return false
}
```

#### 3.2.2 代理客户端

```go
// getProxyClient 获取代理客户端
func (f *HTTPClientFactory) getProxyClient(subscriptionID *int) *http.Client {
    // 选择最优节点
    node := f.proxyManager.SelectBestNode(subscriptionID)
    if node == nil {
        log.Warn("no available proxy node, fallback to direct")
        return f.getDirectClient()
    }
    
    // 创建代理 Transport
    proxyURL, _ := url.Parse(node.ProxyURL())
    transport := &http.Transport{
        Proxy: http.ProxyURL(proxyURL),
        TLSClientConfig: &tls.Config{
            InsecureSkipVerify: false,
        },
        DialContext: (&net.Dialer{
            Timeout:   10 * time.Second,
            KeepAlive: 30 * time.Second,
        }).DialContext,
        MaxIdleConns:          100,
        IdleConnTimeout:       90 * time.Second,
        TLSHandshakeTimeout:   10 * time.Second,
        ExpectContinueTimeout: 1 * time.Second,
    }
    
    return &http.Client{
        Transport: transport,
        Timeout:   60 * time.Second,
    }
}
```

### 3.3 代理管理器

```go
// ProxyManager 代理管理器
type ProxyManager struct {
    db           *pgxpool.Pool
    nodes        sync.Map // map[int]*ProxyNode (subscription_id -> nodes)
    refreshMutex sync.Mutex
}

// SelectBestNode 选择最优节点
func (pm *ProxyManager) SelectBestNode(subscriptionID *int) *ProxyNode {
    var candidates []*ProxyNode
    
    if subscriptionID != nil {
        // 从指定订阅选择
        candidates = pm.getNodesFromSubscription(*subscriptionID)
    } else {
        // 从所有 active 订阅选择
        candidates = pm.getAllActiveNodes()
    }
    
    if len(candidates) == 0 {
        return nil
    }
    
    // 按健康状态和响应时间排序
    sort.Slice(candidates, func(i, j int) bool {
        if candidates[i].Status != candidates[j].Status {
            return candidates[i].Status == "active"
        }
        return candidates[i].ResponseTimeMs < candidates[j].ResponseTimeMs
    })
    
    return candidates[0]
}

// RefreshSubscription 刷新订阅
func (pm *ProxyManager) RefreshSubscription(subscriptionID int) error {
    pm.refreshMutex.Lock()
    defer pm.refreshMutex.Unlock()
    
    // 1. 查询订阅配置
    var sub ProxySubscription
    err := pm.db.QueryRow(context.Background(), `
        SELECT id, name, subscribe_url FROM proxy_subscriptions 
        WHERE id = $1 AND status = 'active'
    `, subscriptionID).Scan(&sub.ID, &sub.Name, &sub.SubscribeURL)
    if err != nil {
        return err
    }
    
    // 2. 解析订阅
    parser := NewSubscriptionParser()
    nodes, err := parser.Parse(sub.SubscribeURL)
    if err != nil {
        pm.updateSubscriptionError(subscriptionID, err.Error())
        return err
    }
    
    // 3. 更新数据库
    tx, _ := pm.db.Begin(context.Background())
    defer tx.Rollback(context.Background())
    
    // 删除旧节点
    _, _ = tx.Exec(context.Background(), 
        "DELETE FROM proxy_nodes WHERE subscription_id = $1", subscriptionID)
    
    // 插入新节点
    for _, node := range nodes {
        _, err = tx.Exec(context.Background(), `
            INSERT INTO proxy_nodes 
            (subscription_id, name, protocol, server, port, username, password, config)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        `, subscriptionID, node.Name, node.Protocol, node.Server, node.Port,
            node.Username, encrypt(node.Password), node.Config)
        if err != nil {
            return err
        }
    }
    
    // 更新订阅状态
    _, _ = tx.Exec(context.Background(), `
        UPDATE proxy_subscriptions 
        SET node_count = $1, last_fetch_at = NOW(), 
            last_fetch_status = 'success', last_error = NULL
        WHERE id = $2
    `, len(nodes), subscriptionID)
    
    tx.Commit(context.Background())
    
    // 4. 刷新内存缓存
    pm.loadNodesIntoMemory(subscriptionID)
    
    return nil
}
```

### 3.4 探活集成

#### 3.4.1 探活函数改造

```go
// probeOpenAICompatibleBase 支持代理
func probeOpenAICompatibleBase(rawBase, apiKey string, timeout time.Duration, useProxy bool) (map[string]any, error) {
    // ... 现有逻辑
    
    // 创建客户端
    var client *http.Client
    if useProxy {
        client = getProxyClient()
    } else {
        client = &http.Client{Timeout: timeout}
    }
    
    // ... 其余探活逻辑不变
}
```

#### 3.4.2 Quick Entry 接口更新

```go
var req struct {
    // ... 现有字段
    UseProxy       bool `json:"use_proxy"`       // 探活是否使用代理
    ForceSkipProbe bool `json:"force_skip_probe"`
}

// 探活时传递 UseProxy
if req.ProbeFirst && strings.TrimSpace(req.BaseURL) != "" {
    p, _ := probeOpenAICompatibleBase(req.BaseURL, req.APIKey, 10*time.Second, req.UseProxy)
    probeResult = p
    // ...
}

// 保存时设置 egress_profile
cfg := freeProviderConfig{
    // ... 现有字段
    egressProfile: "direct", // 默认直连
}
if req.UseProxy {
    cfg.egressProfile = "proxy"
}
```

## 4. API 设计

### 4.1 代理订阅管理

#### 4.1.1 列出订阅

```
GET /api/proxy/subscriptions

Response:
{
  "subscriptions": [
    {
      "id": 1,
      "name": "NPS-252",
      "subscribe_url": "http://<env:HOST_252_IP>:8080/subscribe?token=***",
      "status": "active",
      "node_count": 12,
      "last_fetch_at": "2026-08-29T10:00:00Z",
      "last_fetch_status": "success"
    }
  ],
  "total": 1
}
```

#### 4.1.2 添加订阅

```
POST /api/proxy/subscriptions

Request:
{
  "name": "NPS-252",
  "subscribe_url": "http://<env:HOST_252_IP>:8080/subscribe?token=4956b9532968e00e9f0e710e4ccf262d",
  "notes": "252 服务器 NPS 代理"
}

Response:
{
  "status": "ok",
  "subscription_id": 1,
  "message": "订阅添加成功，正在解析节点..."
}
```

#### 4.1.3 刷新订阅

```
POST /api/proxy/subscriptions/:id/refresh

Response:
{
  "status": "ok",
  "node_count": 12,
  "message": "订阅刷新成功"
}
```

#### 4.1.4 测试订阅

```
POST /api/proxy/subscriptions/:id/test

Request:
{
  "test_url": "https://api.groq.com/openai/v1/models"
}

Response:
{
  "status": "ok",
  "available_nodes": 10,
  "fastest_node": {
    "name": "HK-01",
    "response_time_ms": 120
  }
}
```

### 4.2 代理节点管理

#### 4.2.1 列出节点

```
GET /api/proxy/nodes?subscription_id=1

Response:
{
  "nodes": [
    {
      "id": 1,
      "name": "HK-01",
      "protocol": "socks5",
      "server": "proxy.example.com",
      "port": 1080,
      "status": "active",
      "response_time_ms": 120,
      "success_rate": 0.98,
      "last_health_check_at": "2026-08-29T10:00:00Z"
    }
  ],
  "total": 12
}
```

#### 4.2.2 健康检查

```
POST /api/proxy/nodes/:id/health-check

Response:
{
  "status": "ok",
  "response_time_ms": 115,
  "test_url": "https://www.google.com",
  "reachable": true
}
```

### 4.3 域名管理

#### 4.3.1 列出域名

```
GET /api/proxy/domains

Response:
{
  "domains": [
    {
      "domain": "api.groq.com",
      "catalog_code": "groq-free",
      "requires_proxy": true,
      "probe_status": "blocked",
      "last_probe_at": "2026-08-29T10:00:00Z"
    }
  ]
}
```

#### 4.3.2 探测域名

```
POST /api/proxy/domains/probe

Request:
{
  "domain": "api.groq.com",
  "use_proxy": false
}

Response:
{
  "domain": "api.groq.com",
  "reachable": false,
  "response_time_ms": null,
  "error": "connection timeout",
  "recommendation": "requires_proxy"
}
```

### 4.4 Free Pool 集成

#### 4.4.1 Quick Entry 更新

```
POST /api/free-pool/quick-entry

Request:
{
  "platform_id": "groq",
  "api_key": "gsk_...",
  "probe_first": true,
  "use_proxy": true,        // 新增：探活是否使用代理
  "save": true,
  "egress_profile": "proxy" // 新增：保存时的出口配置
}

Response:
{
  "status": "ok",
  "probe": {
    "ok": true,
    "status_code": 200,
    "model_count": 3,
    "used_proxy": true,
    "proxy_node": "HK-01"
  },
  "catalog_code": "groq-free"
}
```

## 5. 前端集成

### 5.1 代理管理页面

路径：`/proxy-management`

功能：
- 订阅列表（增删改查）
- 节点列表（查看、健康检查）
- 域名探测
- 全局代理设置

### 5.2 Free Pool 探活增强

在 `https://llmgo.kxpms.cn/free-pool` 页面：

```vue
<template>
  <div class="probe-options">
    <el-checkbox v-model="useProxy">
      使用代理探活
      <el-tooltip content="勾选后将通过代理服务器探测，适用于被 GFW 阻挡的海外供应商">
        <i class="el-icon-question"></i>
      </el-tooltip>
    </el-checkbox>
  </div>
  
  <div v-if="probeResult" class="probe-result">
    <el-tag v-if="probeResult.used_proxy" type="info">
      已使用代理: {{ probeResult.proxy_node }}
    </el-tag>
  </div>
</template>
```

### 5.3 供应商配置增强

在供应商编辑页面添加出口配置：

```vue
<el-form-item label="出口配置">
  <el-radio-group v-model="form.egress_profile">
    <el-radio label="auto">自动判断</el-radio>
    <el-radio label="direct">直连</el-radio>
    <el-radio label="proxy">使用代理</el-radio>
  </el-radio-group>
</el-form-item>

<el-form-item v-if="form.egress_profile === 'proxy'" label="代理订阅">
  <el-select v-model="form.proxy_subscription_id" placeholder="自动选择">
    <el-option label="自动选择最优节点" :value="null"></el-option>
    <el-option 
      v-for="sub in proxySubscriptions" 
      :key="sub.id" 
      :label="sub.name" 
      :value="sub.id">
    </el-option>
  </el-select>
</el-form-item>
```

## 6. 部署和配置

### 6.1 初始化脚本

```sql
-- 1. 创建表结构
\i db/migrations/364_proxy_management.sql

-- 2. 插入默认代理订阅
INSERT INTO proxy_subscriptions (name, subscribe_url, priority, notes)
VALUES (
  'NPS-252',
  'http://<env:HOST_252_IP>:8080/subscribe?token=4956b9532968e00e9f0e710e4ccf262d',
  100,
  '252 服务器 NPS 代理，用于访问海外供应商'
);

-- 3. 初始化常见域名
INSERT INTO provider_domains (domain, requires_proxy, location, notes) VALUES
('api.groq.com', true, 'US', 'Groq API'),
('api.openai.com', true, 'US', 'OpenAI API'),
('api.anthropic.com', true, 'US', 'Anthropic API'),
('generativelanguage.googleapis.com', false, 'Global', 'Google Gemini'),
('api.siliconflow.cn', false, 'CN', 'SiliconFlow'),
('open.bigmodel.cn', false, 'CN', '智谱 AI');
```

### 6.2 环境变量

```env
# 代理配置
PROXY_ENABLED=true                          # 启用代理功能
PROXY_DEFAULT_SUBSCRIPTION_ID=1             # 默认使用的订阅 ID
PROXY_AUTO_REFRESH_INTERVAL=3600            # 订阅自动刷新间隔（秒）
PROXY_HEALTH_CHECK_INTERVAL=300             # 节点健康检查间隔（秒）
PROXY_UNKNOWN_DOMAIN_STRATEGY=direct        # 未知域名策略: direct/proxy/probe
```

### 6.3 定时任务

```go
// 启动定时任务
func (pm *ProxyManager) StartScheduledTasks() {
    // 每小时刷新订阅
    go func() {
        ticker := time.NewTicker(1 * time.Hour)
        for range ticker.C {
            pm.RefreshAllSubscriptions()
        }
    }()
    
    // 每 5 分钟健康检查
    go func() {
        ticker := time.NewTicker(5 * time.Minute)
        for range ticker.C {
            pm.HealthCheckAllNodes()
        }
    }()
}
```

## 7. 测试计划

### 7.1 单元测试

- 订阅解析器测试（各种格式）
- 节点选择算法测试
- 域名判断逻辑测试

### 7.2 集成测试

- 添加订阅 → 解析节点 → 健康检查
- 创建供应商 → 配置代理 → 发起请求
- 探活测试（直连 vs 代理）

### 7.3 端到端测试

1. 添加 NPS 订阅
2. 刷新获取节点列表
3. 在 free-pool 中添加 Groq 供应商
4. 勾选"使用代理探活"
5. 验证探活成功
6. 保存供应商配置
7. 发起实际请求验证

## 8. 监控和日志

### 8.1 关键指标

- 代理节点可用性
- 代理请求成功率
- 代理响应时间
- 订阅刷新成功率

### 8.2 日志记录

```go
log.Info("proxy_request",
    "provider", provider.Code,
    "proxy_node", node.Name,
    "response_time_ms", elapsed,
    "success", success)
```

## 9. 安全考虑

1. **密码加密**：代理密码使用 AES 加密存储
2. **订阅 URL 保护**：包含 token 的 URL 只显示掩码
3. **访问控制**：代理管理 API 需要 admin 权限
4. **审计日志**：记录所有代理配置变更

## 10. 后续优化

1. **智能代理选择**：根据目标域名地理位置选择最优节点
2. **代理池负载均衡**：多节点轮询和故障转移
3. **代理性能统计**：记录每个节点的历史性能数据
4. **自动域名探测**：定期探测新域名，自动更新 requires_proxy 标记
5. **PAC 规则支持**：导入和导出 PAC 代理自动配置脚本

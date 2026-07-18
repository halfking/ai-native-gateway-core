# 衰减优化实施计划

> 基于 AUDIT.md 审计结果的分阶段实施路线图
>
> 制定日期: 2026-07-18  
> 总工期: 4周 (零风险调优) + 8周 (代码增强) + 持续优化

---

## 执行摘要

| 阶段 | 时间 | 重点 | 预期收益 | 风险 |
|------|------|------|---------|------|
| **Phase 0-PRE** | Day 0.5 | 前置修正（AUDIT_V2发现） | 扫清障碍 | 极低 |
| **Phase 0** | Week 1 | 零代码配置调优 | TTFB ↓ 15-20% | 极低 |
| **Phase 1** | Week 2-3 | P0代码增强 + DNS/TLS优化 | TTFB ↓ 额外25-30% | 低 |
| **Phase 2** | Week 4-6 | P1功能开发 | 全面可观测 | 中 |
| **Phase 3** | Week 7-12 | P2架构优化 | 长期收益 | 中高 |

**⚠️ 重要变更（基于 AUDIT_V2）**:
- 新增 **Phase 0-PRE** 前置任务（统一配置、增强回滚、金丝雀机制）
- Phase 0 收益预测从 20-30% 调整为 **15-20%**（因双层配置冲突）
- Phase 1 增加 DNS缓存 + TLS Session复用（初版遗漏，贡献 10-15%）
- HTTP/2 Server Push 从 P0 调整为 P1（h2c 架构下需改用 Link Preload）

---

## Phase 0-PRE: 前置修正任务 (Day 0.5, 约4小时)

### 背景

**二次审计（AUDIT_V2.md）发现 Phase 0 存在致命问题，必须先修正才能启动优化**：

1. **A1 双层配置冲突** - `pool/pool.go` (16) 与 `upstream/client.go` (32) 参数不一致，导致配置调优部分失效
2. **A3 回滚脚本不完整** - 仅覆盖代码回滚，缺少 DB配置/Nginx/Env 回滚
3. **A4 金丝雀方案缺失** - 文档提到 10%→50%→100% 灰度，但无 Nginx 流量控制实现

**不执行前置任务的后果**:
- Phase 0 实际收益仅 8-10%（而非预期的 20-30%）
- 回滚失败，RTO > 30分钟
- 无法金丝雀发布，All-or-nothing 风险

### 任务清单

#### Task 0.1: 统一连接池参数（A1修正）

**问题**: 代码库存在两套独立 HTTP Transport，参数不协调

| 位置 | 当前值 | 文档建议值 | 修正值 |
|------|--------|-----------|--------|
| `pool/pool.go:24` | MaxIdleConnsPerHost=16 | 64 | **64** |
| `pool/pool.go:25` | MaxConnsPerHost=64 | 256 | **256** |
| `pool/pool.go:35` | poolMaxActiveConns=32 | 未提及 | **128** (放宽槽位) |
| `upstream/client.go:104` | MaxIdleConns=128 | - | **512** (配套调整) |
| `upstream/client.go:105` | MaxIdleConnsPerHost=32 | 64 | **64** (对齐pool层) |

**执行**:
```bash
git checkout -b fix/audit-v2-pre-phase0

# 修改 pool/pool.go
vim pool/pool.go
# Line 24: maxIdleConnsPerHost = 16 → 64
# Line 25: maxConnsPerHost     = 64 → 256
# Line 35: poolMaxActiveConns  = 32 → 128

# 修改 upstream/client.go
vim upstream/client.go
# Line 104: MaxIdleConns        = 128 → 512
# Line 105: MaxIdleConnsPerHost = 32  → 64

# 运行测试
go test ./pool/... ./upstream/... -v
```

**验证**: 测试通过 + 无编译错误

#### Task 0.2: 增强回滚脚本（A3修正）

**问题**: 当前回滚脚本仅覆盖代码，遗漏了 DB配置/Nginx/Env

**执行**:
```bash
# 备份原脚本
cp scripts/rollback-optimization.sh scripts/rollback-optimization.bak

# 用增强版替换
cat > scripts/rollback-optimization.sh << 'EOF'
#!/bin/bash
# 增强版回滚脚本（覆盖 代码+DB+Nginx+Env）
set -euo pipefail

echo "=== Step 1: 回滚代码 ==="
git checkout "$(git describe --tags --abbrev=0 main~1)"
go build -o bin/gateway cmd/gateway/main.go

echo "=== Step 2: 回滚数据库配置 ==="
psql "$DATABASE_URL" <<SQL
UPDATE llm_gateway_config SET value = '100' WHERE key = 'http2_max_concurrent_streams';
UPDATE llm_gateway_config SET value = '16' WHERE key = 'max_idle_conns_per_host';
SQL

echo "=== Step 3: 回滚环境变量 ==="
if [ -f /opt/llm-gateway/backups/pre-optimization/override.conf ]; then
    cp /opt/llm-gateway/backups/pre-optimization/override.conf \
       /etc/systemd/system/llm-gateway.service.d/override.conf
    systemctl daemon-reload
fi

echo "=== Step 4: 重启服务 ==="
systemctl restart llm-gateway
sleep 10

echo "=== Step 5: 健康检查 ==="
for i in {1..5}; do
    if curl -fsS http://localhost:8781/healthz; then
        echo "✅ 回滚成功"
        exit 0
    fi
    sleep 2
done

echo "❌ 回滚失败，请人工介入"
exit 1
EOF

chmod +x scripts/rollback-optimization.sh
```

**验证**: `bash scripts/rollback-optimization.sh --dry-run`（模拟执行，不实际修改）

#### Task 0.3: 配置金丝雀流量控制（A4修正）

**问题**: INDEX.md 提到 10%→50%→100% 灰度，但 252 Nginx 无流量控制机制

**执行**:
```bash
ssh root@192.168.1.252

# 修改 Nginx upstream 配置
cat > /etc/nginx/conf.d/llm-gateway-upstream.conf << 'EOF'
upstream llm_gateway_canary {
    server 192.168.1.184:8781 weight=9;   # 旧版本 90%
    server 192.168.1.71:8781  weight=1;   # 新版本 10%
    keepalive 64;
}

server {
    listen 80;
    server_name llm-gateway.internal;
    
    location /v1/ {
        proxy_pass http://llm_gateway_canary;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        # ... 其他配置保持不变
    }
}
EOF

# 测试配置
nginx -t

# 生效
systemctl reload nginx
```

**验证**:
```bash
# 发送100个请求，统计路由到71的比例
for i in {1..100}; do
    curl -s http://llm-gateway.internal/healthz | grep -oP 'host=\K[^"]+' >> /tmp/routing.log
done
grep "71" /tmp/routing.log | wc -l  # 应该约等于10
```

**金丝雀调整计划**:
- Week 1: weight=1 (10%)
- Week 2: weight=5 (50%)  
- Week 3: weight=10, 184下线 (100%)

#### Task 0.4: 提交前置修正

```bash
git add -A
git commit -m "fix(optimization): pre-phase0 audit v2 fixes

- A1: Unify connection pool params (pool 16→64, upstream 32→64)
- A3: Enhance rollback script (add DB/Nginx/Env rollback)
- A4: Configure canary traffic control (Nginx upstream weight)

Ref: docs/衰减优化/AUDIT_V2.md"

git push origin fix/audit-v2-pre-phase0
```

### 验证检查清单

- [ ] `go test ./pool/... ./upstream/...` 通过
- [ ] `go build ./cmd/gateway/` 编译成功
- [ ] `scripts/rollback-optimization.sh --dry-run` 无报错
- [ ] Nginx 配置 `nginx -t` 通过
- [ ] 金丝雀流量验证约 10% 到达 71 服务器

### 时间估算

| 任务 | 开发 | 测试 | 总计 |
|------|------|------|------|
| Task 0.1 | 30min | 20min | 50min |
| Task 0.2 | 40min | 20min | 60min |
| Task 0.3 | 30min | 30min | 60min |
| Task 0.4 | 10min | - | 10min |
| **总计** | - | - | **3小时** |

---

## Phase 0: 零代码配置调优 (Week 1)

### 目标

⚠️ **修正后目标**（基于 AUDIT_V2）:
- 通过纯配置变更，获得 **15-20%** 的 TTFB 优化（初版预测 20-30% 因 A1 配置冲突打折）
- 无代码风险，可在 10 分钟内回滚
- 为 Phase 1 的 DNS/TLS 优化奠定基础

**前提条件**: 必须先完成 **Phase 0-PRE** 前置任务，否则优化失效

### 任务清单

#### Day 1: 连接池参数调整

```bash
# 1. 备份当前配置
git checkout -b optimize/connection-pool-tuning
cp pool/pool.go pool/pool.go.backup
cp upstream/client.go upstream/client.go.backup

# 2. 修改 pool/pool.go
```

```go
// pool/pool.go:23-27
const (
    maxIdleConnsPerHost = 64   // 改: 16 → 64
    maxConnsPerHost     = 256  // 改: 64 → 256
    idleConnTimeout     = 180 * time.Second  // 改: 90s → 180s
    dialTimeout         = 5 * time.Second    // 改: 10s → 5s
    tcpKeepAlive        = 60 * time.Second   // 改: 30s → 60s
)
```

```go
// upstream/client.go:94-110
Transport: &http.Transport{
    MaxIdleConns:          512,  // 改: 128 → 512
    MaxIdleConnsPerHost:   64,   // 新增
    MaxConnsPerHost:       256,  // 新增
    IdleConnTimeout:       180 * time.Second,  // 改: 90s → 180s
    DialContext: (&net.Dialer{
        Timeout:   5 * time.Second,   // 改: 10s → 5s
        KeepAlive: 60 * time.Second,  // 改: 30s → 60s
    }).DialContext,
}
```

**验证**：
```bash
# 编译
go build -o bin/gateway cmd/gateway/main.go

# 本地验证
./bin/gateway --config=config/dev.yaml

# 检查启动日志中的连接池配置
```

#### Day 2: HTTP/2 SETTINGS调优

```go
// cmd/gateway/main.go — 找到 h2s := &http2.Server 这一行
h2s := &http2.Server{
    MaxConcurrentStreams:         1000,  // 改: 250 → 1000
    MaxReadFrameSize:             1 << 20,  // 新增: 1MB
    IdleTimeout:                  180 * time.Second,  // 新增
    MaxUploadBufferPerConnection: 1 << 20,  // 新增: 1MB
    MaxUploadBufferPerStream:     512 << 10, // 新增: 512KB
}
```

#### Day 3: Stream缓冲策略调整

```go
// domains/streaming/stream.go:24-34
const (
    emptyGateMaxChunks = 2   // 改: 8 → 2
    emptyGateMaxBytes  = 8 * 1024  // 改: 64KB → 8KB
)
```

#### Day 4-5: 测试环境部署与验证

```bash
# 1. 部署到 kaixuan-1 测试环境
bash scripts/deploy-kaixuan-1.sh

# 2. 运行负载测试
bash scripts/load-test.sh --duration=2h --qps=100

# 3. 监控关键指标（Prometheus）
# - llm_gateway_request_duration_seconds (P50/P95/P99)
# - llm_gateway_upstream_requests_total
# - process_resident_memory_bytes
# - go_goroutines

# 4. 对比基准线
# 基准线数据在 docs/衰减优化/baseline-metrics.json
```

**验收标准**：
- [ ] P50 TTFB 降低 > 15%
- [ ] P99 TTFB 降低 > 20%
- [ ] 内存增长 < 10%
- [ ] 错误率不变
- [ ] 48小时稳定运行

#### Day 6-7: 生产环境金丝雀发布

```bash
# 1. 10% 流量切换到新配置
# 修改 nginx upstream 权重

# 2. 监控24小时

# 3. 如果指标达标，扩大到50%流量

# 4. 再监控24小时后全量切换
```

---

## Phase 1: P0代码增强 (Week 2-3)

### 目标（基于 AUDIT_V2 修正）

- **新增**: DNS 缓存 (A6) - 新建连接 -10~20ms
- **新增**: TLS Session 复用 (A7) - 握手开销 -50~100%  
- **调整**: HTTP/2 Server Push → Link Preload (A2) - h2c 架构限制
- **预期收益**: TTFB ↓ 额外 **25-30%**（初版 15-20% 被低估）

### Week 2: 网络优化（DNS + TLS，新增任务）

#### Task 1.0: DNS 缓存（A6修正，1天）

**问题**: 当前每次新建连接都实时解析 DNS，增加 10-20ms 延迟

**实现**:
```go
// pkg/dnscache/resolver.go（新建文件）
package dnscache

import (
    "context"
    "net"
    "sync"
    "time"
)

type CachedResolver struct {
    cache    map[string]*cacheEntry
    mu       sync.RWMutex
    resolver *net.Resolver
    ttl      time.Duration
}

type cacheEntry struct {
    ips       []net.IP
    expiresAt time.Time
}

func NewCachedResolver(ttl time.Duration) *CachedResolver {
    return &CachedResolver{
        cache:    make(map[string]*cacheEntry),
        resolver: &net.Resolver{PreferGo: true},
        ttl:      ttl,
    }
}

func (r *CachedResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
    r.mu.RLock()
    entry, ok := r.cache[host]
    r.mu.RUnlock()
    
    if ok && time.Now().Before(entry.expiresAt) {
        return entry.ips, nil  // 缓存命中
    }
    
    // 缓存未命中，查询DNS
    ips, err := r.resolver.LookupIP(ctx, "ip", host)
    if err != nil {
        return nil, err
    }
    
    r.mu.Lock()
    r.cache[host] = &cacheEntry{
        ips:       ips,
        expiresAt: time.Now().Add(r.ttl),
    }
    r.mu.Unlock()
    
    return ips, nil
}

// 集成到 upstream/client.go
func NewClient(...) *Client {
    resolver := dnscache.NewCachedResolver(5 * time.Minute)
    
    return &Client{
        hc: &http.Client{
            Transport: &http.Transport{
                DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
                    host, port, _ := net.SplitHostPort(addr)
                    ips, err := resolver.LookupIP(ctx, host)
                    if err != nil {
                        return nil, err
                    }
                    return (&net.Dialer{
                        Timeout:   connectTimeout,
                        KeepAlive: 30 * time.Second,
                    }).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
                },
                // ... 其他配置
            },
        },
    }
}
```

**测试**:
```bash
# 测试 DNS 缓存命中率
go test ./pkg/dnscache/... -v -run TestCacheHit
# 预期: 第二次查询 <1ms（缓存命中）

# 集成测试
go test ./upstream/... -v -run TestDNSCacheIntegration
```

**收益**: 新建连接场景 TTFB -10~20ms

#### Task 1.0.5: TLS Session 复用（A7修正，0.5天）

**问题**: 未配置 TLS Session Cache，每次握手都是完整 1-RTT

**实现**:
```go
// upstream/client.go
import "crypto/tls"

var tlsSessionCache = tls.NewLRUClientSessionCache(128)

func NewClient(...) *Client {
    return &Client{
        hc: &http.Client{
            Transport: &http.Transport{
                TLSClientConfig: &tls.Config{
                    ClientSessionCache: tlsSessionCache,
                    MinVersion:        tls.VersionTLS12,
                    MaxVersion:        tls.VersionTLS13,  // 优先 TLS 1.3 (0-RTT)
                },
                // ... DialContext 与上面的 DNS 缓存集成
            },
        },
    }
}
```

**测试**:
```bash
# Wireshark 抓包验证 TLS Session Resume
tcpdump -i any -w /tmp/tls.pcap port 443
# 查找 "ClientHello" 中的 "session_ticket" extension

# 代码测试
go test ./upstream/... -v -run TestTLSSessionReuse
```

**收益**: 
- TLS 1.2: 握手时间 -50% (2-RTT → 1-RTT)
- TLS 1.3: 握手时间 -100% (1-RTT → 0-RTT)
- 混合场景（20%新连接）: TTFB -10~30ms

### Week 2-3: HTTP/2 优化（调整任务）

#### Task 1.1: HTTP/2 Link Preload（A2修正，替代 Server Push）

```go
// domains/streaming/handler.go — 在 ServeHTTP 开始处添加
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 新增: 预推送候选健康状态
    if pusher, ok := w.(http.Pusher); ok {
        go h.preloadCandidateHealth(pusher, r)
    }
    
    // 原有逻辑...
}

func (h *ChatHandler) preloadCandidateHealth(pusher http.Pusher, r *http.Request) {
    // 从请求中提取model
    model := extractModelFromRequest(r)
    if model == "" {
        return
    }
    
    // 获取前3个候选
    candidates, _, _ := h.providerClient.GetCandidates(
        context.Background(), model, "", extractTenantID(r),
    )
    
    for i, c := range candidates[:min(3, len(candidates))] {
        pushURL := fmt.Sprintf("/internal/candidate-health/%d/%d", 
            c.ProviderID, c.CredentialID)
        
        pushOpts := &http.PushOptions{
            Method: "GET",
            Header: http.Header{
                "X-Preload": []string{"health"},
            },
        }
        
        if err := pusher.Push(pushURL, pushOpts); err != nil {
            // 客户端不支持 Server Push，静默失败
            break
        }
    }
}
```

**测试**：
```bash
# 使用支持HTTP/2的客户端测试
curl -v --http2 https://api.gateway.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}'

# 检查响应头中的 PUSH_PROMISE
```

#### Task 1.2: Pre-Stream Keepalive自适应 (1天)

```go
// domains/streaming/handler.go — 修改 startPreStreamKeepalive 调用
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ...
    
    // 改: 固定15s → 自适应间隔
    interval := h.calculateKeepaliveInterval(r)
    ka, ok := startPreStreamKeepalive(w, interval)
    
    // ...
}

func (h *ChatHandler) calculateKeepaliveInterval(r *http.Request) time.Duration {
    // 从客户端Header推断超时
    if timeout := r.Header.Get("X-Request-Timeout"); timeout != "" {
        if d, err := time.ParseDuration(timeout); err == nil && d > 0 {
            // Caddy策略: 客户端超时的1/4
            interval := d / 4
            if interval < 5*time.Second {
                return 5 * time.Second
            }
            if interval > 30*time.Second {
                return 30 * time.Second
            }
            return interval
        }
    }
    
    // 默认: 15s → 10s (更积极)
    return 10 * time.Second
}
```

#### Task 1.3: 集成测试 (1天)

```bash
# 运行完整测试套件
go test ./... -v -race -cover

# 特别关注 streaming 包的测试
go test ./domains/streaming/... -v -count=10

# 部署到 kaixuan-1，监控3天
```

### Week 3: 连接池监控指标

#### Task 1.4: Prometheus指标实现 (2天)

```go
// pool/metrics.go (新文件)
package pool

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    connectionsCreated = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_pool_connections_created_total",
            Help: "Total connections created (not reused)",
        },
        []string{"provider_id", "credential_id"},
    )
    
    connectionsReused = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_pool_connections_reused_total",
            Help: "Total connections reused from pool",
        },
        []string{"provider_id", "credential_id"},
    )
    
    idleConnections = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_pool_idle_connections",
            Help: "Current idle connections",
        },
        []string{"pool_key"},
    )
    
    activeConnections = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_pool_active_connections",
            Help: "Current active connections",
        },
        []string{"pool_key"},
    )
)

// 在 Pool.Acquire 中埋点
func (p *Pool) Acquire(ctx context.Context) error {
    // 检查是否新建连接 (通过 transport 的内部状态)
    // 如果是复用 → connectionsReused++
    // 如果是新建 → connectionsCreated++
}
```

#### Task 1.5: Grafana Dashboard (1天)

创建 `docs/衰减优化/grafana-dashboard.json`：

```json
{
  "dashboard": {
    "title": "LLM Gateway - 连接池优化监控",
    "panels": [
      {
        "title": "连接复用率",
        "targets": [{
          "expr": "sum(rate(llm_gateway_pool_connections_reused_total[5m])) / (sum(rate(llm_gateway_pool_connections_created_total[5m])) + sum(rate(llm_gateway_pool_connections_reused_total[5m])))"
        }],
        "thresholds": [
          {"value": 0.85, "color": "green"},
          {"value": 0.70, "color": "yellow"},
          {"value": 0.00, "color": "red"}
        ]
      },
      {
        "title": "TTFB分位数",
        "targets": [
          {"expr": "histogram_quantile(0.50, llm_gateway_ttfb_seconds)", "legendFormat": "P50"},
          {"expr": "histogram_quantile(0.95, llm_gateway_ttfb_seconds)", "legendFormat": "P95"},
          {"expr": "histogram_quantile(0.99, llm_gateway_ttfb_seconds)", "legendFormat": "P99"}
        ]
      }
    ]
  }
}
```

#### Task 1.6: 验收与报告 (1天)

- [ ] 连接复用率可视化
- [ ] TTFB分位数趋势
- [ ] 生成Phase 1完成报告

---

## Phase 2: P1功能开发 (Week 4-6)

### Week 4: TTFB分位统计增强

#### Task 2.1: TTFBTracker重构 (3天)

```go
// domains/streaming/executors/ttfb_tracker.go
import "github.com/influxdata/tdigest"

type TTFBTracker struct {
    mu sync.RWMutex
    // 改: 从单值 → T-Digest分位统计
    digests map[int]*tdigest.TDigest  // credentialID → digest
}

func (t *TTFBTracker) Record(credentialID int, ttfb time.Duration) {
    t.mu.Lock()
    defer t.mu.Unlock()
    
    if t.digests[credentialID] == nil {
        t.digests[credentialID] = tdigest.New()
    }
    
    t.digests[credentialID].Add(float64(ttfb.Milliseconds()), 1)
    
    // 每1000次采样压缩一次（防止内存膨胀）
    if t.digests[credentialID].Count() % 1000 == 0 {
        t.digests[credentialID].Compress()
    }
}

func (t *TTFBTracker) Quantile(credentialID int, q float64) time.Duration {
    t.mu.RLock()
    defer t.mu.RUnlock()
    
    digest := t.digests[credentialID]
    if digest == nil || digest.Count() == 0 {
        return 0
    }
    
    ms := digest.Quantile(q)
    return time.Duration(ms) * time.Millisecond
}
```

#### Task 2.2: P2C评分增强 (1天)

```go
// domains/streaming/executors/router_scoring.go
func calculateLatencyScore(c provider.Candidate, tracker *TTFBTracker) float64 {
    // 改: 使用P50而非MaxUsageWindowTTFB
    p50 := tracker.Quantile(c.CredentialID, 0.50)
    if p50 == 0 {
        // fallback到旧逻辑
        if c.MaxUsageWindowTTFB > 0 {
            ratio := float64(c.MaxUsageWindowTTFB) / float64(5*time.Second)
            return min(ratio, 1.0)
        }
        return 0.5
    }
    
    // 归一化: P50 / 5s基准
    ratio := float64(p50) / float64(5*time.Second)
    return min(ratio, 1.0)
}
```

### Week 5-6: 连接预热 (Prewarm)

#### Task 2.3: Prewarm实现 (3天)

```go
// pool/pool.go
func (pm *PoolManager) Prewarm(ctx context.Context, keys []PoolKey, targetConns int) error {
    var wg sync.WaitGroup
    errCh := make(chan error, len(keys))
    
    for _, key := range keys {
        wg.Add(1)
        go func(k PoolKey) {
            defer wg.Done()
            
            p, err := pm.GetOrCreate(ctx, k)
            if err != nil {
                errCh <- err
                return
            }
            
            // 预建立targetConns个连接
            for i := 0; i < targetConns; i++ {
                // 发送健康探测请求（建立连接）
                if err := p.healthProbe(ctx); err != nil {
                    errCh <- err
                    break
                }
            }
        }(key)
    }
    
    wg.Wait()
    close(errCh)
    
    // 收集错误（非阻塞）
    var errs []error
    for err := range errCh {
        errs = append(errs, err)
    }
    
    if len(errs) > 0 {
        return fmt.Errorf("prewarm failed: %d errors", len(errs))
    }
    return nil
}
```

#### Task 2.4: Executor集成 (2天)

```go
// domains/streaming/executors/executor.go
func (e *Executor) executeWithCandidates(...) {
    // 获取候选列表
    candidates := e.router.PlanCandidates(...)
    
    // 新增: 后台异步预热top-3候选
    if e.poolManager != nil && len(candidates) > 1 {
        go func() {
            keys := make([]pool.PoolKey, 0, 3)
            for _, c := range candidates[:min(3, len(candidates))] {
                keys = append(keys, pool.PoolKey{
                    IdentityHash: identityHash,
                    ProviderID:   c.ProviderID,
                    CredentialID: c.CredentialID,
                })
            }
            
            // 每个池预热2个连接
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            e.poolManager.Prewarm(ctx, keys, 2)
        }()
    }
    
    // 原有逻辑: 遍历候选...
}
```

---

## Phase 3: P2架构优化 (Week 7-12, 可选)

### Week 7-8: Zero-Copy流式直通

```go
// domains/streaming/stream.go
func (e *Executor) canZeroCopy(clientFmt, upstreamFmt ProviderFormat) bool {
    return clientFmt == upstreamFmt && 
           !e.qualityFixMode && 
           !e.vendorFieldStripping
}

func (e *Executor) zeroCopyRelay(ctx context.Context, upstream io.Reader, w http.ResponseWriter) error {
    // 直接从upstream流拷贝到ResponseWriter，跳过JSON解析
    flusher := w.(http.Flusher)
    buf := make([]byte, 32*1024)  // 32KB buffer
    
    for {
        n, err := upstream.Read(buf)
        if n > 0 {
            w.Write(buf[:n])
            flusher.Flush()
        }
        if err != nil {
            if err == io.EOF {
                return nil
            }
            return err
        }
    }
}
```

### Week 9-10: 专线/同地域部署调研

- 联系各MaaS供应商，获取API入口地域分布
- 评估CEN专线成本
- 制定迁移计划

### Week 11-12: Stream强制模式 (内存安全验证)

```go
// 需要先验证大请求场景下的内存安全性
// 增加限流: 单请求缓冲 ≤ 128MB，全局缓冲 ≤ 2GB
```

---

## 监控与回滚

### 关键监控指标

```promql
# 连接复用率 (目标>85%)
sum(rate(llm_gateway_pool_connections_reused_total[5m])) / 
(sum(rate(llm_gateway_pool_connections_created_total[5m])) + 
 sum(rate(llm_gateway_pool_connections_reused_total[5m])))

# P50 TTFB (目标<200ms同地域, <500ms跨云)
histogram_quantile(0.50, rate(llm_gateway_ttfb_seconds_bucket[5m]))

# 内存增长 (告警>2GB)
process_resident_memory_bytes

# 错误率 (告警>0.5%)
sum(rate(llm_gateway_requests_total{status="error"}[5m])) / 
sum(rate(llm_gateway_requests_total[5m]))
```

### 回滚触发条件

| 指标 | 阈值 | 动作 |
|------|------|------|
| 错误率 | > 1% 持续5分钟 | 立即回滚 |
| P99延迟 | 增加 > 50% | 立即回滚 |
| 内存 | 增长 > 30% | 观察1小时，仍高则回滚 |
| 连接复用率 | < 60% | 检查配置，不回滚 |

### 回滚脚本

```bash
#!/bin/bash
# scripts/rollback-optimization.sh

echo "开始回滚到优化前配置..."

# 1. 恢复代码
git checkout main
git pull origin main

# 2. 重新编译
go build -o bin/gateway cmd/gateway/main.go

# 3. 重启服务
systemctl restart llm-gateway

# 4. 验证
sleep 10
curl -f http://localhost:8781/healthz || exit 1

echo "回滚完成"
```

---

## 总结

**核心收益预测**：

| 阶段 | 时间 | 收益 | 累计 |
|------|------|------|------|
| Phase 0 | 1周 | TTFB ↓ 20-30% | 30% |
| Phase 1 | 2周 | TTFB ↓ 额外15% | 40% |
| Phase 2 | 3周 | 可观测性完善 | 40% |
| Phase 3 | 6周 | 长期优化 | 50%+ |

**投入产出比**：Phase 0 最高（1周投入，30%收益），优先执行。

# 衰减优化实施计划

> 基于 AUDIT.md 审计结果的分阶段实施路线图
>
> 制定日期: 2026-07-18  
> 总工期: 4周 (零风险调优) + 8周 (代码增强) + 持续优化

---

## 执行摘要

| 阶段 | 时间 | 重点 | 预期收益 | 风险 |
|------|------|------|---------|------|
| **Phase 0** | Week 1 | 零代码配置调优 | TTFB ↓ 20-30% | 极低 |
| **Phase 1** | Week 2-3 | P0代码增强 | TTFB ↓ 额外15-20% | 低 |
| **Phase 2** | Week 4-6 | P1功能开发 | 全面可观测 | 中 |
| **Phase 3** | Week 7-12 | P2架构优化 | 长期收益 | 中高 |

---

## Phase 0: 零代码配置调优 (Week 1)

### 目标
通过纯配置变更，立即获得20-30%的TTFB优化，无代码风险。

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

### Week 2: HTTP/2 Server Push + Keepalive自适应

#### Task 1.1: HTTP/2 Server Push (2天)

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

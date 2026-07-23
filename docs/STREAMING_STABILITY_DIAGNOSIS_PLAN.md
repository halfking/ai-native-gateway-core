# 流式响应稳定性诊断和修复计划

**问题描述**: 直连 Minimax-M3 稳定快速，但通过网关后变得不稳定或中断  
**创建时间**: 2026-07-23  
**优先级**: P0 - 影响用户体验

---

## 一、问题定义

### 现象对比

| 维度 | 直连 (稳定) | 通过网关 (不稳定) |
|------|------------|-----------------|
| 连接稳定性 | ✅ 稳定 | ❌ 频繁中断 |
| 响应速度 | ✅ 快速 | ⚠️ 慢或超时 |
| 完成率 | ✅ 100% | ❌ 中途停止 |
| 错误类型 | 无 | first_byte_timeout, eof_without_done |

### 核心问题

**网关作为中间层引入的不稳定性因素**：
1. 超时配置不合理
2. 流式数据转发延迟
3. 连接管理问题
4. 协议适配缺陷
5. 资源竞争和限流
6. 错误恢复机制不完善

---

## 二、分层诊断方法论

遵循 OSI 模型和系统分层架构，自下而上逐层排查：

```
┌─────────────────────────────────────────┐
│  Layer 7: Application (协议适配层)       │  ← 协议转换、格式处理
├─────────────────────────────────────────┤
│  Layer 6: Business Logic (业务逻辑层)   │  ← 路由、重试、熔断
├─────────────────────────────────────────┤
│  Layer 5: Stream Processing (流处理层)  │  ← 缓冲、转发、超时控制
├─────────────────────────────────────────┤
│  Layer 4: Connection (连接层)           │  ← HTTP连接池、Keep-Alive
├─────────────────────────────────────────┤
│  Layer 3: Network (网络层)              │  ← TCP、TLS、延迟
├─────────────────────────────────────────┤
│  Layer 2: Resource (资源层)             │  ← CPU、内存、文件描述符
├─────────────────────────────────────────┤
│  Layer 1: Infrastructure (基础设施层)   │  ← 服务器、操作系统
└─────────────────────────────────────────┘
```

---

## 三、分阶段诊断计划

### Phase 1: 基础层诊断（已完成 ✅）

#### 1.1 配置层
- [x] ✅ 超时配置检查和优化
  - FirstByteTimeout: 30s → 120s
  - StreamChunkTimeout: 300s → 600s
  - EnablePreStreamKeepalive: true
- [x] ✅ 日志分析和问题识别
- [x] ✅ 即时验证（10分钟零错误）

**结果**: 配置优化后，超时问题显著减少

---

### Phase 2: 流处理层深度诊断（进行中 ⏳）

#### 2.1 流缓冲和转发机制

**诊断点**:
```
Client → Gateway Buffer → Upstream
          ↓
       [缓冲区大小]
       [刷新策略]
       [背压处理]
```

**测试计划**:

**Test 2.1.1: 缓冲区大小测试**
```go
// 文件: domains/streaming/stream.go:19
const streamBufSize = 64 * 1024  // 当前值

测试场景:
1. 小缓冲 (4KB): 测试高频小chunk场景
2. 中缓冲 (64KB): 当前配置
3. 大缓冲 (256KB): 测试大chunk场景

预期: 找到最优缓冲大小平衡延迟和吞吐量
```

**Test 2.1.2: 刷新策略测试**
```go
// 检查点: domains/streaming/stream.go
// 当前使用 bufio.NewReaderSize 和 w.Flush()

测试项:
1. 每个chunk立即flush (最低延迟)
2. 累积到阈值flush (当前)
3. 定时flush (固定间隔)

指标: 延迟、吞吐量、CPU使用率
```

**Test 2.1.3: 空chunk处理**
```go
// 文件: domains/streaming/stream.go:78
func chunkHasContent(payload string) bool

问题: Minimax可能发送空chunk导致网关误判为完成
测试: 记录所有空chunk，分析是否导致中断
```

#### 2.2 超时控制精度

**诊断点**:
```
├─ UpstreamTimeout (90s)         ← 总超时
├─ FirstByteTimeout (120s)       ← 冲突! 120 > 90
├─ StreamTimeout (900s)
└─ StreamChunkTimeout (600s)
```

**Test 2.2.1: 超时层级冲突**
```bash
# 当前配置冲突
LLM_GATEWAY_UPSTREAM_TIMEOUT=90
LLM_GATEWAY_FIRST_BYTE_TIMEOUT=120  # 超过上游总超时!

测试: 
1. 模拟100秒首字节延迟
2. 验证是哪个超时触发
3. 调整配置消除冲突
```

**Test 2.2.2: Context传播**
```go
// 检查: 每层是否正确传播和尊重context deadline

测试代码:
func TestContextPropagation(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    // 验证上游请求使用了带超时的context
    // 验证流读取循环检查context.Done()
}
```

#### 2.3 错误分类和恢复

**Test 2.3.1: eof_without_done处理**
```go
// 文件: domains/streaming/executors/executor_chat.go
// 当前: 分类为 stream_timeout, benign_eof=true

问题: Minimax不发送[DONE]，但网关认为是错误
修复方向:
1. 检测到eof_without_done + chunk_count > 0 → 视为成功
2. 添加per-provider协议适配器
3. 记录最后接收chunk的时间，判断是否真的超时
```

**Test 2.3.2: 重试决策**
```go
// 文件: domains/streaming/executors/executor.go

测试场景:
1. first_byte_timeout → 应该重试 ✅
2. eof_without_done + 0 chunks → 应该重试 ✅
3. eof_without_done + >100 chunks → 不应重试（已成功）❌ 当前错误

修复: 智能重试决策
if outcome.Reason == "eof_without_done" && outcome.ChunkCount > minValidChunks {
    outcome.Resumable = false
    outcome.Success = true
}
```

---

### Phase 3: 连接层诊断（待进行 🔲）

#### 3.1 HTTP连接池健康度

**Test 3.1.1: 连接复用率**
```go
// 检查: net/http.Transport 配置

诊断脚本:
// 监控连接池指标
curl http://localhost:6060/debug/pprof/heap  # pprof已启用
```

**Test 3.1.2: Keep-Alive配置**
```go
// 检查: upstream HTTP client配置

当前配置检查:
- MaxIdleConns
- MaxIdleConnsPerHost
- IdleConnTimeout
- DisableKeepAlives

优化方向: 增加复用，减少握手开销
```

#### 3.2 TLS握手性能

**Test 3.2.1: TLS版本和密码套件**
```bash
# 检查Minimax API的TLS配置
openssl s_client -connect api.minimaxi.com:443 -servername api.minimaxi.com

测试: 
- TLS 1.2 vs 1.3 性能差异
- 握手耗时
- 是否支持session resumption
```

---

### Phase 4: 业务逻辑层诊断（待进行 🔲）

#### 4.1 路由决策延迟

**Test 4.1.1: 候选凭据探测开销**
```go
// 文件: autoroute/executor.go

测试: 测量从请求到上游调用的总延迟
- 路由决策: XXms
- 凭据选择: XXms
- 协议转换: XXms
- 上游连接: XXms

目标: 总延迟 < 100ms
```

#### 4.2 并发限流影响

**Test 4.2.1: 凭据槽位竞争**
```go
// 文件: credentialfpslot/pool.go

问题: 高并发时槽位不足导致排队
测试: 
1. 监控槽位使用率
2. 模拟20个并发请求（当前默认槽位数）
3. 验证是否有请求被阻塞

优化: 如果槽位竞争激烈，增加 DefaultCredentialConcurrency
```

---

### Phase 5: 网络层诊断（待进行 🔲）

#### 5.1 端到端延迟测量

**Test 5.1.1: 分段延迟分析**
```bash
# 使用tcpdump捕获完整请求链路

测试脚本:
#!/bin/bash
# 1. 客户端 → 网关 (154)
curl -w "@curl-format.txt" https://llm.kxpms.cn/v1/chat/completions

# 2. 网关 (154) → Minimax API
# 在154上: tcpdump -i eth0 host api.minimaxi.com -w minimax.pcap

# 3. 分析延迟分布
# - DNS解析
# - TCP握手
# - TLS握手
# - 首字节时间
# - 数据传输
```

#### 5.2 网络稳定性

**Test 5.2.1: 丢包和重传**
```bash
# 在154服务器上
netstat -s | grep -E 'retransmit|timeout|packet loss'

# 持续监控
watch -n 1 'netstat -s | grep retransmit'
```

---

### Phase 6: 资源层诊断（待进行 🔲）

#### 6.1 系统资源瓶颈

**Test 6.1.1: 文件描述符限制**
```bash
# 检查当前进程FD使用
lsof -p $(pgrep llm-gateway-go) | wc -l

# 检查系统限制
ulimit -n
cat /proc/sys/fs/file-max
```

**Test 6.1.2: 内存压力**
```bash
# 监控网关内存使用
ps aux | grep llm-gateway-go
pmap -x $(pgrep llm-gateway-go)

# 检查是否有频繁GC
curl http://localhost:6060/debug/pprof/heap
```

---

## 四、单元测试计划

### 4.1 流处理单元测试

**Test Suite: streaming_test.go**
```go
package streaming_test

import (
    "bytes"
    "context"
    "io"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
)

// TestStreamBufferSizes 测试不同缓冲大小的性能
func TestStreamBufferSizes(t *testing.T) {
    sizes := []int{4096, 16384, 65536, 262144}
    for _, size := range sizes {
        t.Run(fmt.Sprintf("BufferSize_%d", size), func(t *testing.T) {
            // 模拟Minimax流式响应
            // 测量延迟和吞吐量
        })
    }
}

// TestEOFWithoutDone 测试Minimax的非标准结束
func TestEOFWithoutDone(t *testing.T) {
    tests := []struct{
        name       string
        chunks     []string
        expectPass bool
    }{
        {"NoChunks", []string{}, false},
        {"OneChunk", []string{"data: {content:\"hi\"}"}, true},
        {"ManyChunks", []string{"data: {}", "data: {content:\"hi\"}", /*100 more*/}, true},
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // 模拟流中断
            // 验证outcome.Success
        })
    }
}

// TestTimeoutHierarchy 测试超时层级
func TestTimeoutHierarchy(t *testing.T) {
    // 验证FirstByteTimeout不超过UpstreamTimeout
    // 验证context正确传播
}

// TestConcurrentStreams 测试并发流处理
func TestConcurrentStreams(t *testing.T) {
    concurrency := []int{1, 10, 50, 100}
    for _, n := range concurrency {
        t.Run(fmt.Sprintf("Concurrent_%d", n), func(t *testing.T) {
            // 并发发起n个流式请求
            // 验证无竞争和死锁
            // 测量吞吐量下降
        })
    }
}
```

### 4.2 协议适配单元测试

**Test Suite: minimax_adapter_test.go**
```go
package transformation_test

// TestMinimaxStreamFormat 测试Minimax流格式
func TestMinimaxStreamFormat(t *testing.T) {
    tests := []struct{
        input    string
        expected ir.Chunk
        hasError bool
    }{
        // 测试各种Minimax响应格式
        {"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}", /* ... */, false},
        {"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}", /* ... */, false},  // 无[DONE]
        {"data: [DONE]", /* ... */, false},  // 标准格式
    }
}

// TestMinimaxEOFDetection EOF检测逻辑
func TestMinimaxEOFDetection(t *testing.T) {
    // 验证能正确识别Minimax的完成信号
    // finish_reason="stop" 但没有 [DONE]
}
```

### 4.3 集成测试

**Test Suite: e2e_minimax_test.go**
```go
// TestMinimaxE2E 端到端测试
func TestMinimaxE2E(t *testing.T) {
    if testing.Short() {
        t.Skip("跳过E2E测试")
    }
    
    // 1. 直连Minimax (baseline)
    directLatency := testDirectConnection(t)
    
    // 2. 通过网关
    gatewayLatency := testViaGateway(t)
    
    // 3. 对比
    overhead := gatewayLatency - directLatency
    if overhead > 500*time.Millisecond {
        t.Errorf("网关开销过大: %v", overhead)
    }
}
```

---

## 五、监控和指标

### 5.1 关键指标定义

```go
// 添加到 metrics/streaming.go

var (
    // 流处理指标
    StreamBufferFlushLatency = promauto.NewHistogram(prometheus.HistogramOpts{
        Name: "stream_buffer_flush_latency_ms",
        Help: "流缓冲区刷新延迟",
        Buckets: []float64{1, 5, 10, 50, 100, 500, 1000},
    })
    
    StreamChunkSize = promauto.NewHistogram(prometheus.HistogramOpts{
        Name: "stream_chunk_size_bytes",
        Help: "流chunk大小分布",
        Buckets: prometheus.ExponentialBuckets(100, 2, 10),
    })
    
    StreamInterruptReasons = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "stream_interrupt_total",
        Help: "流中断原因统计",
    }, []string{"reason", "provider", "resumable"})
    
    EOFWithoutDoneChunkCount = promauto.NewHistogram(prometheus.HistogramOpts{
        Name: "eof_without_done_chunk_count",
        Help: "eof_without_done发生时已接收chunk数",
        Buckets: []float64{0, 1, 5, 10, 50, 100, 500},
    })
)
```

### 5.2 监控仪表板

**Grafana Dashboard: Streaming Stability**
```json
{
  "panels": [
    {
      "title": "流中断率（按原因）",
      "targets": [{
        "expr": "rate(stream_interrupt_total[5m])"
      }]
    },
    {
      "title": "网关开销分布",
      "targets": [{
        "expr": "histogram_quantile(0.95, gateway_overhead_ms)"
      }]
    },
    {
      "title": "eof_without_done分析",
      "targets": [{
        "expr": "eof_without_done_chunk_count"
      }]
    }
  ]
}
```

---

## 六、修复优先级

### P0 - 立即修复（本周）

1. **✅ 超时配置优化** (已完成)
   - FirstByteTimeout: 120s
   - StreamChunkTimeout: 600s

2. **🔲 超时层级冲突修复**
   ```bash
   LLM_GATEWAY_UPSTREAM_TIMEOUT=150  # 从90增加到150
   ```

3. **🔲 eof_without_done智能处理**
   ```go
   // 修改: domains/streaming/executors/executor_chat.go
   if outcome.Reason == "eof_without_done" && outcome.ChunkCount > 5 {
       outcome.Resumable = false
       outcome.Success = true
       slog.Info("minimax_eof_workaround", "chunks", outcome.ChunkCount)
   }
   ```

### P1 - 短期修复（2周内）

4. **🔲 Minimax协议适配器**
   - 创建 `provider/minimax/adapter.go`
   - 实现 finish_reason 检测
   - 处理非标准流结束

5. **🔲 流缓冲优化**
   - 测试不同缓冲大小
   - 实现自适应缓冲策略

6. **🔲 连接池优化**
   - 调整 MaxIdleConnsPerHost
   - 启用 HTTP/2
   - TLS session resumption

### P2 - 中期优化（1个月内）

7. **🔲 全链路追踪**
   - OpenTelemetry集成
   - 每个请求的详细时序
   - 分段延迟可视化

8. **🔲 智能重试策略**
   - 基于错误类型的重试决策
   - 指数退避
   - 重试预算

9. **🔲 per-model配置**
   - 不同模型不同超时
   - 不同供应商不同协议适配

---

## 七、成功标准

### 目标指标

| 指标 | 当前 | 目标 | 验证方法 |
|------|------|------|----------|
| 流中断率 | ~13% (18/130) | <1% | 24小时监控 |
| 第一字节超时 | 1次/13分钟 | 0 | 日志统计 |
| eof_without_done | 18次/13分钟 | <1次/小时 | 智能处理后 |
| 端到端延迟开销 | 未测量 | <200ms | 对比测试 |
| 成功率 | 未知 | >99% | 持续监控 |

### 验证计划

**24小时验证**（当前Phase 1完成后）:
```bash
# 每小时执行
ssh 154 "grep -E 'minimax|timeout|interrupt' /opt/llm-gateway-go/logs/gateway.log | \
  tail -1000 | \
  jq -r 'select(.msg | test(\"interrupt|timeout\")) | 
    [.time, .msg, .reason, .chunk_count] | @tsv'"
```

**压力测试**:
```bash
# 并发100个Minimax请求
ab -n 100 -c 10 -p minimax_payload.json \
  -T application/json \
  -H "Authorization: Bearer sk-xxx" \
  https://llm.kxpms.cn/v1/chat/completions
```

---

## 八、实施时间表

```
Week 1 (当前周):
  Day 1 ✅ Phase 1: 超时配置优化（已完成）
  Day 2 🔲 Phase 2.1: 流缓冲诊断
  Day 3 🔲 Phase 2.2: 超时层级修复
  Day 4 🔲 Phase 2.3: eof_without_done智能处理
  Day 5 🔲 单元测试编写和执行

Week 2:
  Phase 3: 连接层诊断和优化
  Phase 4: 业务逻辑层优化
  集成测试

Week 3-4:
  Phase 5: 网络层深度诊断
  Phase 6: 资源层优化
  E2E测试和性能调优

Month 2:
  P2优先级优化项
  监控和告警完善
  文档和知识沉淀
```

---

## 九、风险和依赖

### 风险

1. **配置调整风险**: 超时增加可能导致资源占用增加
   - 缓解: 逐步调整，监控资源使用

2. **协议适配风险**: Minimax可能更新API导致适配失效
   - 缓解: 版本检测和降级策略

3. **性能回归风险**: 优化可能在某些场景下降低性能
   - 缓解: A/B测试，灰度发布

### 依赖

1. 需要访问154服务器进行日志和指标收集
2. 需要Minimax API测试账号进行对比测试
3. 需要压测环境验证高并发场景

---

## 十、联系人和协作

- **问题报告**: 瑞
- **技术负责人**: [待指定]
- **测试负责人**: [待指定]
- **运维支持**: [待指定]

---

**文档状态**: 🚧 进行中  
**最后更新**: 2026-07-23  
**下次审查**: Phase 2完成后

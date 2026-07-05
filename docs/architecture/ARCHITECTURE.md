# LLM Gateway Go 数据面 — 架构方案 V3（最终版）

## 0. 执行摘要

此方案采用 **Python控制面 + Go数据面** 的双路线架构。Python负责管理（供应商/凭据/模型/策略/日志查询），Go负责高性能数据转发（身份隧道/流式中继/并发控制/审计）。立即可做的Python修复在最后一章。

---

## 1. 错误分类与处理矩阵

### 1.1 七类错误 → 处理动作

| 错误类别 | 示例 | 恢复类型 | 重试 | 熔断动作 | 并发动作 | 客户端响应 | 审计事件 |
|-----------|-------|-------------|-------|-------------|----------------|-----------|------------|
| **TRANSIENT** | 5xx (无明确错误码) | 临时 | ✓ (退避) | `cooling`(60s) | 无 | 503 `temporarily_unavailable` | `error_kind=transient` |
| **TIMEOUT** | 连接/读取超时 | 临时 | ✓ (退避) | `cooling`(60s) | 无 | 504 `upstream_timeout` | `error_kind=timeout` |
| **NETWORK** | DNS/连接拒绝/重置 | 临时 | ✓ (退避) | `cooling`(60s) | 无 | 502 `upstream_network_error` | `error_kind=network` |
| **RATE_LIMIT** | 429 | 周期性 | ✓ (退避) | `cooling`(30s) | `shrink(0.7)` | 429 `rate_limit_exceeded` | `error_kind=rate_limit` |
| **AUTH** | 401/403 | **永久** | ✗ | `quarantine` | 无 | 502 `credential_invalid` | `error_kind=auth` |
| **QUOTA** | 余额不足/402 | **永久** | ✗ | `quarantine` | 无 | 402 `quota_exhausted` | `error_kind=quota` |
| **UPSTREAM_DOWN** | 502/503/504 | 周期性 | ✓ (退避) | `open`(指数退避) | `shrink(0.5)` | 503 `upstream_down` | `error_kind=upstream_down` |

### 1.2 恢复类型说明

```
临时中断 (TRANSIENT/TIMEOUT/NETWORK):
  → 冷却期60s后自动恢复
  → 3次连续失败 → 升级为"周期性"

周期性 (RATE_LIMIT/UPSTREAM_DOWN):
  → 指数退避冷却: 30s→60s→120s→240s→480s (cap=1800s)
  → 冷却期到期 → half_open → 探测请求
  → 探测成功 → active，探测失败 → 重新计算冷却期
  → 5次连续冷却循环都失败 → 通知管理员

永久 (AUTH/QUOTA):
  → 直接 quarantine
  → 记录详细错误信息和时间戳
  → 通知管理员介入
  → 不自动恢复，除非管理员手动标记有效或更新凭据
```

---

## 2. TCP连接池 (身份绑定)

### 2.1 池键设计

```
池键 = (identity_hash[:16], provider_id, credential_id)
  说明: 虚拟身份+供应商+凭据共同决定连接池
  原因: 凭据不同则API Key不同，不能复用连接

每个池:
  最大空闲连接: 32
  最大总连接:  128
  空闲超时:    90s (http.Transport.IdleConnTimeout)
  TCP Keepalive: 30s (从Go 1.13开始默认启用)
  探针:        每30s对池内空闲连接发一次HEAD请求
```

### 2.2 连接健康探测

```go
type PoolHealthProbe struct {
    interval    time.Duration // 30s
    probeURL    string        // provider base URL + /healthz (或简单GET)
    timeout     time.Duration // 5s
}

func (p *PoolHealthProbe) Check(conn *http.Client) bool {
    ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
    defer cancel()
    req, _ := http.NewRequestWithContext(ctx, "GET", p.probeURL, nil)
    resp, err := conn.Do(req)
    if err != nil || resp.StatusCode >= 500 {
        return false
    }
    return true
}
// 连续3次探测失败 → 标记连接池为degraded → 新请求跳过此池
// 探测恢复 → 标记为active → 新请求可以继续使用
```

---

## 3. 流式中继完整状态机

```
流式请求生命周期:

┌──────────┐   上游返回200     ┌────────────┐   发送chunk    ┌──────────┐
│  等待    │────────────────▶  │  传输中    │──────────────▶│ 已完成   │
│ upstream │                  │(读chunk→   │               │ [DONE]   │
│  响应    │                  │ transform→ │               │ 发送     │
└──────────┘                  │  write+flush)              └──────────┘
       │                      └────────────┘                    │
       │                          │        │                    │
       │ 上游返回                  │        │ 上游返回           │
       │ 非200                    │        │ 错误chunk          │
       ▼                          ▼        ▼                    │
  ┌──────────┐              ┌──────────┐                       │
  │ 错误    │              │ 错误     │                       │
  │ 处理    │              │ chunk   │                       │
  │ (重试/  │              │ 替换+   │                       │
  │ 降级)   │              │ 发送    │                       │
  └──────────┘              └──────────┘                       │
                                                               │
      客户端断开 (context cancelled) ──────────────────────────┘
      上游超时 (30s无chunk) ────────────────────────────────────┘
      
三种中断处理:
  客户端断开: context.Done() → cancel upstream → 审计标记 interrupted=true
  上游超时:   最后chunk后30s无新chunk → 发送error chunk → 审计
  上游错误:   错误chunk → transform → 发送给客户端 → 重试/降级
```

---

## 4. 审计流水线 (含死信处理)

```go
type AuditPipeline struct {
    events   chan AuditEvent    // 主缓冲 (buff=10000)
    dlq      chan AuditEvent    // 死信缓冲 (buff=1000)
    db BatchWriter
}

func (p *AuditPipeline) Run(ctx context.Context) {
    batch := make([]AuditEvent, 0, 500)
    ticker := time.NewTicker(5 * time.Second)

    for {
        select {
        case evt := <-p.events:
            batch = append(batch, evt)
            if len(batch) >= 500 {
                p.flush(ctx, &batch)
            }
        case <-ticker.C:
            if len(batch) > 0 {
                p.flush(ctx, &batch)
            }
        case evt := <-p.dlq:
            // 重试死信: 先写文件回退, 下次批量再试
            p.writeToFallback(evt)
        case <-ctx.Done():
            p.flush(ctx, &batch) // 最终刷出
            return
        }
    }
}

func (p *AuditPipeline) flush(ctx context.Context, batch *[]AuditEvent) {
    if len(*batch) == 0 { return }
    
    err := p.db.BatchInsert(ctx, *batch)
    if err != nil {
        log.Printf("audit batch write failed: %v, moving %d to DLQ", err, len(*batch))
        for _, evt := range *batch {
            select {
            case p.dlq <- evt: // 送入死信
            default:
                // 死信也满了, 写磁盘文件
                p.writeToFallback(evt)
            }
        }
    }
    *batch = (*batch)[:0]
}

// 磁盘回退: 写入 /var/log/llm-gateway/audit-fallback/ 目录
// 定时任务: 每5分钟扫描回退目录, 重新尝试写入DB
// 文件保留: 7天自动清理
```

---

## 5. 凭据解密安全设计

```go
type CredentialDecryptor struct {
    masterKey []byte       // 从环境变量或K8s Secret加载
    cache     sync.Map     // map[credentialID]*decryptedKey, TTL=5min
}

func (d *CredentialDecryptor) Decrypt(ctx context.Context, cid int, ciphertext []byte) (string, error) {
    // 1. 检查缓存 (明文在内存中只保留5分钟)
    if cached, ok := d.cache.Load(cid); ok {
        entry := cached.(*cacheEntry)
        if time.Since(entry.created) < 5*time.Minute {
            return entry.plaintext, nil
        }
        d.cache.Delete(cid)
    }

    // 2. Fernet解密 (与Python侧兼容)
    //    Python: cryptography.fernet.Fernet(key).decrypt(ciphertext)
    //    Go:     github.com/fxamacker/cbor/v2 (Fernet实现)
    plaintext, err := fernetDecrypt(d.masterKey, ciphertext)
    if err != nil {
        return "", err
    }

    // 3. 缓存 (明文在内存, 5分钟过期)
    d.cache.Store(cid, &cacheEntry{plaintext: plaintext, created: time.Now()})

    // 4. 使用后立即清零 (defer zeroize)
    return plaintext, nil
}

// 安全措施:
// 1. 解密后的key在goroutine栈上传递, 不逃逸到堆
// 2. 使用后立即覆写内存
// 3. 禁止日志/审计输出明文
```

---

## 6. 灰度策略

```
Phase 1 (Python only, 稳定现状):
  - 修复503问题
  - 接入identity到chat.py (P-6)
  - 完成executor.py的四层并发 (P-3)
  - 升级telemetry为队列化 (P-4)

Phase 2 (Go旁路验证):
  - 部署Go数据面到独立端口 (8781)
  - 通过X-Go-Tunnel: true header 控制转发
  - 仅转发特定client_profile (如"roocode-test")
  - 对比Go/Python的处理结果一致性

Phase 3 (Go主, Python备用):
  - 默认所有请求走Go数据面
  - Python保留/v1/chat/completions作为回退
  - Go不可用时自动降级到Python

Phase 4 (Go全量):
  - Python仅保留控制面
  - Go承担100%数据流量
  - Python数据面入口关闭
```

---

## 7. 全局速率限制

```go
// 在 middleware/ratelimit.go 中实现
// 两层: 全局 + 按API Key

type RateLimiter struct {
    global   *tokenBucket  // 全局: 5000 req/s, burst=200
    perKey   *tokenBucket  // 按API Key: 100 req/s, burst=50
}

type tokenBucket struct {
    capacity  int64
    rate      float64    // tokens/sec
    tokens    atomic.Int64
    lastTime  atomic.Int64 // unix nano
}

func (tb *tokenBucket) Allow() bool {
    // 原子操作: cas更新tokens
    // 无锁实现, 仅依赖atomic
}
```

---

## 8. 即将实施的Python修复

### 8.1 紧急修复 (立即生效)

```
F1: 扩大请求体限制
    文件: app/main.py:94
    修改: _MAX_REQUEST_BODY_BYTES = 100 * 1024 → 10 * 1024 * 1024
    原因: 当前100KB限制会拦截所有带代码上下文的请求
    
F2: LiteLLM超时配置
    文件: app/core/proxy.py:18
    添加: litellm.request_timeout = 120  (总超时)
          litellm.connect_timeout = 10   (连接超时)
    原因: 默认超时可能过短,大请求超时导致503
    
F3: 凭据熔断器复位
    脚本: 检查credentials表中circuit_state='open'的凭据
    操作: 对每个open凭据发送探测请求
          成功→SET circuit_state='closed'
          失败→记录日志,保留状态
    原因: glm-4.7对应的凭据可能熔断打开
    
F4: 统一错误响应格式
    文件: app/api/v1/chat.py:166-176
    修改: 保证所有错误响应都有JSON body
    {"error":{"message":"...","type":"...","request_id":"..."}}
    原因: 客户端收到"no body"的503→无法解析→重新请求→继续503→死循环
    
F5: 接入client_identity到execute()
    文件: app/api/v1/chat.py:148-164
    修改: 调用build_identity_from_request()并传递client_identity
    原因: 身份隧道不生效,无法进行身份相关的路由和审计
```

### 8.2 短期改进 (本周内)

```
F6: Telemetry队列化 (P-4)
    app/core/telemetry.py: create_task → queue+batch flush
    
F7: 四层并发桶接入 (P-3)
    app/core/executor.py: acquire四层→释放→shrink
    
F8: SQL字段扩展 (P-5)
    sql/092_identity_lease_fields.sql: 新迁移脚本
```

---

## 9. Go数据面任务分解

| 包 | 优先级 | 工作量 | 依赖 | 说明 |
|-----|----------|--------|----------|---------|
| cmd/gateway | P0 | 1人日 | 无 | 骨架+信号+server |
| identity/ | P0 | 2人日 | cmd | 核心算法,与Python一致 |
| middleware/auth | P0 | 1人日 | 无 | API Key验证 |
| relay/chat | P0 | 3人日 | identity+transform+pool | 主入口handler |
| relay/stream | P0 | 3人日 | relay/chat | SSE流式中继 |
| transform/ | P1 | 2人日 | identity | YAML规则+双向转换 |
| pool/ | P1 | 2人日 | identity+upstream | 身份绑定连接池 |
| limiter/ | P1 | 3人日 | pool+circuit | 四层并发桶 |
| circuit/ | P1 | 2人日 | upstream | 熔断器状态机 |
| audit/ | P2 | 2人日 | 无 | 队列+批量写入 |
| router/ | P2 | 3人日 | resolve | 候选规划(读PG) |
| upstream/ | P2 | 1人日 | pool | 重试+超时 |
| resolve/ | P2 | 1人日 | 无 | 模型名解析 |
| config/ | P2 | 1人日 | 无 | 热更新 |

**总工作量估算**: 约 27 人日 (4人团队约7天)

---

## 10. 验收标准

### 10.1 身份隧道验收
- [ ] 同一client_profile+device_seed在不同请求中生成相同的identity_hash
- [ ] Go与Python对相同输入生成一致的identity_hash (互操作性)
- [ ] virtual_ip格式为10.x.x.x, virtual_mac格式为02:xx:xx:xx:xx:xx
- [ ] 上游请求头中携带x-virtual-client-id / x-virtual-ip / x-virtual-mac

### 10.2 转换验收
- [ ] client_model="glm-4.7" → outbound_model="zhipu/glm-4-plus"
- [ ] 响应中的model字段被还原为"glm-4.7"
- [ ] SSE chunks中的model字段被替换
- [ ] 错误消息中不包含供应商名称或凭据信息
- [ ] x-device-seed不在上游请求中出现

### 10.3 并发控制验收
- [ ] 单凭据并发达到limit时新请求排队/拒绝
- [ ] 429响应触发shrink, 有效并发降低
- [ ] shrink在5分钟内恢复50%, 15分钟完全恢复
- [ ] 不同identity_hash的并发控制相互独立

### 10.4 流式中继验收
- [ ] 30个chunk的SSE流中所有model字段被替换
- [ ] 客户端断开后上游请求取消 (context cancel)
- [ ] 上游超时后发送适当的错误chunk
- [ ] 背压: 客户端消费慢时channel缓冲(64)满→上游读取暂停

### 10.5 审计验收
- [ ] 每个请求至少产生2个审计事件 (accepted + final)
- [ ] 批量写入DB, 不阻塞请求路径
- [ ] DB不可用时审计事件写磁盘回退
- [ ] 审计事件不包含明文凭据

### 10.6 稳定性验收
- [ ] 200并发持续5分钟, P99延迟<500ms
- [ ] 熔断器打开后请求立即跳过该凭据
- [ ] 凭据轮换后30s内生效
- [ ] panic在任何goroutine中不导致进程崩溃

# P1优化任务设计方案

**日期**: 2026-07-22  
**优先级**: P1（本周执行）  
**负责人**: ACC Agent  

---

## 任务1: Provider/Credential状态持久化评估

### 1.1 当前架构分析

#### 现状

**InMemoryStore实现**:
- `domains/credential/types.go` — `InMemoryStore` (map + sync.RWMutex)
- `domains/provider/types.go` — `InMemoryStore` (map + sync.RWMutex)

**存储字段**:
```go
// Credential健康状态
type Credential struct {
    Status           Status    // active/degraded/unhealthy/disabled
    ConsecutiveFails int       // 连续失败次数
    LastHealthCheck  time.Time // 最后探测时间
    // ... 其他字段
}

// Provider健康状态
type Provider struct {
    ConsecutiveFails int       // 连续失败次数
    LastHealthCheck  time.Time // 最后探测时间
    Disabled         bool      // 是否禁用
    // ... 其他字段
}
```

**当前问题**:
1. ❌ 服务重启后健康状态丢失
2. ❌ 多实例间状态不同步
3. ❌ 无法跨实例共享探测结果

**当前缓解措施**:
- ✅ 探测器会在服务启动后重新探测
- ✅ 探测周期短（30s），自动收敛快

#### 现有Redis使用

**已有Redis集成**:
- `domains/credential/redis_identity.go` — 分布式并发控制（identity limiter）
- `domains/credential/rpm_redis.go` — RPM（请求/分钟）限流
- `domains/ursm/v2/store.go` — URSM v2节点状态持久化（完善）

**Redis连接模式**:
```go
// 已有Lua脚本模式（原子操作）
acquireIdentityScript = redis.NewScript(`...`)

// 已有key前缀规范
redisIdentityKeyPrefix = "llmgw:ident:"
```

### 1.2 设计方案

#### 方案A: Redis全持久化（推荐）

**架构**:
```
HealthChecker/Prober
    ↓
RedisHealthStore (新增)
    ↓
Redis (key: "llmgw:health:cred:<id>" / "llmgw:health:prov:<id>")
```

**Key设计**:
```
llmgw:health:cred:<credential_id>
  → hash {
      status: "active",
      consecutive_fails: 0,
      last_check: "2026-07-22T06:30:00Z"
    }

llmgw:health:prov:<provider_id>
  → hash {
      consecutive_fails: 0,
      last_check: "2026-07-22T06:30:00Z",
      disabled: false
    }
```

**TTL策略**:
- 健康状态TTL: **10分钟**（足够长避免频繁重建，但短到自动清理过期节点）
- 每次探测更新TTL

**优点**:
- ✅ 完全持久化，重启无损
- ✅ 多实例共享状态
- ✅ 一致性强

**缺点**:
- ⚠️ 每次探测需Redis往返（增加2-5ms延迟）
- ⚠️ Redis故障影响健康检查

**成本评估**:
- 存储: 100个credentials × 200 bytes ≈ 20KB（可忽略）
- QPS: 探测频率30s → 100 creds → 3.3 QPS（极低）
- 延迟: Get/Set hash ≈ 1-2ms（可接受）

#### 方案B: 混合模式（内存+Redis备份）

**架构**:
```
InMemoryStore (primary, 读写)
    ↓ 每次变更
RedisBackup (secondary, 仅写)
    ↑ 启动时
LoadFromRedis
```

**策略**:
- 读: 100%走内存（零延迟）
- 写: 内存+Redis双写（异步，不阻塞）
- 启动: 从Redis恢复（仅一次）

**优点**:
- ✅ 读性能无损（内存速度）
- ✅ Redis故障不影响运行
- ✅ 重启可恢复

**缺点**:
- ⚠️ 多实例间有短暂不一致（探测周期内收敛）
- ⚠️ 实现复杂度中等

#### 方案C: 保持现状+文档明确

**策略**:
- 保持InMemoryStore
- 文档明确"重启后短暂状态不准，30s内自动收敛"
- 监控探测收敛时间

**优点**:
- ✅ 零实现成本
- ✅ 零性能开销
- ✅ 零Redis依赖

**缺点**:
- ❌ 重启后短暂状态丢失
- ❌ 多实例间永久不一致

### 1.3 推荐决策

**推荐**: **方案B（混合模式）**

**理由**:
1. **性能优先** — 路由在热路径上，内存读取保持48ns延迟
2. **可靠性增强** — Redis故障不影响服务，仅影响重启恢复
3. **收敛加速** — 重启后从Redis恢复，避免探测冷启动
4. **成本可控** — 异步写入，不阻塞主流程

**实施优先级**: P1（本周实现原型）

**阶段计划**:
1. **Phase 1** (本周): 实现RedisBackupStore + 单元测试
2. **Phase 2** (下周): 集成到HealthChecker/Prober
3. **Phase 3** (2周后): 生产灰度验证
4. **Phase 4** (1个月后): 监控收敛时间，评估是否升级到方案A

---

## 任务2: 多模态Phase 3测试CI集成

### 2.1 现状分析

**现有测试**:
- ✅ `scripts/multimodal-e2e/run_phase3.sh` — 完整runner（182行）
- ✅ `docs/multimodal-testing/` — 测试计划 + 用例
- ✅ `docs/multimodal-testing/samples/` — 测试素材（图片/音频）

**Phase 3用例**:
```
T-01: OpenAI图片URL
T-02: OpenAI图片base64
T-03: Anthropic图片base64
T-04: Doubao vision
T-05: Whisper audio transcription
```

**当前gap**:
- ❌ 未接入CI pipeline
- ❌ 需要真实API key（不适合公开CI）
- ❌ 依赖外部模型可用性（不稳定）

**现有CI** (`.github/workflows/sessionforensics-ci.yml`):
- ✅ Go 1.25 + Ubuntu
- ✅ make test-short + make test-sessionforensics
- ✅ 超时15分钟

### 2.2 设计方案

#### CI集成策略

**方案A: Mock模式CI测试**

```yaml
# .github/workflows/multimodal-ci.yml
name: Multimodal Tests

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  multimodal-unit:
    name: Multimodal Unit Tests
    runs-on: ubuntu-latest
    timeout-minutes: 10
    
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      
      - name: Run multimodal validation tests
        run: |
          go test ./domains/transformation/anthropic/... -run="Media" -v
          go test ./internal/ir/... -run="Media" -v
      
      - name: Run multimodal conversion tests
        run: |
          go test ./domains/transformation/... -run="Multimodal" -v
          go test ./internal/ir/... -run="ContentBlock" -v
      
      - name: Mock Phase 3 runner (--dry-run)
        run: |
          export GATEWAY_URL=http://localhost:8781
          bash scripts/multimodal-e2e/run_phase3.sh --dry-run
```

**优点**:
- ✅ 无需真实API key
- ✅ 快速（<5分钟）
- ✅ 稳定（不依赖外部服务）

**覆盖范围**:
- ✅ Validation逻辑
- ✅ 协议转换
- ✅ Payload构建
- ❌ 真实模型推理（需要LIVE测试）

#### 方案B: LIVE测试（手动触发）

```yaml
# .github/workflows/multimodal-live.yml
name: Multimodal LIVE Tests

on:
  workflow_dispatch:
    inputs:
      gateway_url:
        description: 'Gateway URL'
        required: true
        default: 'https://kaixuan-gateway.internal'
      test_ids:
        description: 'Test IDs (comma-separated, e.g., T-01,T-02)'
        required: false
        default: 'T-01,T-02,T-03'

jobs:
  multimodal-live:
    name: Phase 3 LIVE
    runs-on: self-hosted  # 内网runner
    timeout-minutes: 15
    
    steps:
      - uses: actions/checkout@v4
      
      - name: Run Phase 3 LIVE
        env:
          GATEWAY_URL: ${{ github.event.inputs.gateway_url }}
          LLM_GATEWAY_API_KEY: ${{ secrets.LLM_GATEWAY_TEST_KEY }}
          ONLY_IDS: ${{ github.event.inputs.test_ids }}
        run: bash scripts/multimodal-e2e/run_phase3.sh
      
      - name: Upload test results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: multimodal-live-results
          path: /tmp/multimodal-e2e-phase3.log
```

**优点**:
- ✅ 真实端到端验证
- ✅ 手动触发，可控

**缺点**:
- ⚠️ 需要内网runner
- ⚠️ 需要配置secrets
- ⚠️ 依赖外部模型可用性

### 2.3 推荐决策

**推荐**: **方案A（Mock模式）+ 方案B（LIVE手动）双轨制**

**实施**:
1. **方案A纳入常规CI** — 每次PR自动跑，验证validation + 转换逻辑
2. **方案B作为手动门禁** — 重大发布前手动触发LIVE测试

**实施优先级**: P1（本周完成方案A）

**阶段计划**:
1. **Phase 1** (本周): 实现multimodal-ci.yml + 验证
2. **Phase 2** (下周): 补充mock数据断言（response schema验证）
3. **Phase 3** (2周后): 实现multimodal-live.yml + 内网runner配置
4. **Phase 4** (1个月后): 集成到发布流程

---

## 3. 实施时间表

| 任务 | 本周 | 下周 | 2周后 | 1个月 |
|------|------|------|-------|-------|
| **状态持久化 Phase 1** | ✅ 实现+测试 | | | |
| **状态持久化 Phase 2** | | ✅ 集成 | | |
| **状态持久化 Phase 3** | | | ✅ 灰度 | |
| **状态持久化 Phase 4** | | | | ✅ 评估 |
| **多模态CI Phase 1** | ✅ Mock CI | | | |
| **多模态CI Phase 2** | | ✅ Mock增强 | | |
| **多模态CI Phase 3** | | | ✅ LIVE CI | |
| **多模态CI Phase 4** | | | | ✅ 发布集成 |

---

## 4. 风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| Redis故障影响健康检查 | 低 | 中 | 方案B（混合模式）Redis故障降级到内存 |
| 状态持久化增加延迟 | 低 | 中 | 异步写入，读走内存 |
| LIVE测试不稳定 | 中 | 低 | 仅作手动门禁，不阻塞CI |
| 内网runner配置复杂 | 中 | 低 | 先实现Mock CI，LIVE后续补充 |

---

## 5. 成功标准

### 状态持久化

- [ ] RedisBackupStore实现完成
- [ ] 单元测试覆盖率 > 80%
- [ ] 重启恢复时间 < 5s
- [ ] 内存读性能无退化（仍保持48ns）
- [ ] 生产灰度验证无故障

### 多模态CI

- [ ] Mock CI pipeline工作正常
- [ ] 每次PR自动跑multimodal validation
- [ ] CI执行时间 < 5分钟
- [ ] LIVE测试可手动触发成功
- [ ] 测试结果可追溯（artifact上传）

---

**文档版本**: v1.0  
**最后更新**: 2026-07-22 06:30 UTC+8  
**下次审查**: 1周后（Phase 1完成后）

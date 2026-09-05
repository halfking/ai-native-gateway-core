# LLM Gateway Go vs OmniRoute - 功能对比与方案融合

> **审计状态**：历史对比稿。数量、收益和代码片段不可直接作为验收证据；融合基线以 `docs/omni-ref2/README.md` 和本目录 `05`、`06` 为准。

> **文档版本**: v1.0
> **更新时间**: 2026-08-11
> **对比对象**: llm-gateway-go (Go) vs OmniRoute v3.8.40 (TypeScript/Next.js)

---

## 执行摘要

OmniRoute是成熟的开源AI路由网关，已实现**290 providers、90+免费tier、19种路由策略**。llm-gateway-go在**企业级多租户、URSM状态管理、供应商画像监控**方面更强。本文档分析两者差异，提出融合方案。

### 核心发现

| 维度 | llm-gateway-go优势 | OmniRoute优势 | 融合策略 |
|------|-------------------|--------------|---------|
| **免费资源** | 已导入523条目，缺UI | 完整dashboard + 实时监控 | 采用OmniRoute UI设计 |
| **路由策略** | 6种（v6升级中） | 19种（含压缩、combo） | 引入auto-combo engine |
| **会话管理** | 三层缓存+压缩+分析 | 基础session tracking | 保留llm-gateway优势 |
| **多租户** | 原生tenant_id隔离 | 单租户设计 | 保留llm-gateway架构 |
| **供应商监控** | ProviderProfile画像 | Circuit breaker基础 | 融合两者指标体系 |
| **成本统计** | 多维度+项目任务关联 | 基础usage tracking | 保留llm-gateway |
| **压缩技术** | 会话级delta-append | RTK+Caveman全局压缩 | 引入RTK+Caveman |

---

## 1. 免费资源聚合对比

### 1.1 功能对比矩阵

| 功能点 | llm-gateway-go | OmniRoute | 差距分析 |
|-------|---------------|-----------|---------|
| **资源目录** | `free_resource_catalog` 523条目 | 516 models / 43 pools | ✅ 数量相当 |
| **Quota追踪** | Redis rolling window | SQLite + 内存LRU | llm更scalable |
| **多Key轮换** | `credential_keys`表 | `apiKeyRotator`模块 | 功能对等 |
| **429分类** | body-keyword + header | 基础retry logic | llm更细粒度 |
| **Dashboard** | ❌ 缺失 | ✅ `/dashboard/free-tiers` | **需采用** |
| **实时监控** | Prometheus metrics | 实时quota remaining | **需采用** |

### 1.2 OmniRoute Free-Tiers Dashboard (值得借鉴)

```typescript
// OmniRoute: src/app/dashboard/free-tiers/page.tsx
export default function FreeTiersPage() {
  const { data } = useFreeResourcesCatalog();

  return (
    <div>
      {/* 总池显示 */}
      <FreeBudgetCard
        totalMonthly={data.totalMonthly}  // ~1.53B
        totalFirstMonth={data.totalFirstMonth}  // ~2.15B with signup credits
        used={data.used}
        remaining={data.remaining}
      />

      {/* 按provider分组 */}
      {data.providerPools.map(pool => (
        <ProviderPoolCard
          key={pool.providerId}
          name={pool.name}
          models={pool.models}
          monthlyQuota={pool.monthlyQuota}
          rateLimit={pool.rateLimit}
          status={pool.status}  // healthy / rate_limited / exhausted
        />
      ))}

      {/* 实时使用图表 */}
      <UsageChart
        data={data.usageHistory}
        window="24h"
      />
    </div>
  );
}
```

**融合建议**:
- 在 `web/src/views/` 新增 `FreeTiersPanel.vue`
- 后端API: `GET /admin/free-tiers/summary` + `/admin/free-tiers/pools`
- 复用OmniRoute的UI布局和卡片设计

---

## 2. 路由策略对比

### 2.1 策略清单

#### llm-gateway-go (6种)

1. **固定provider** - 直接指定 `provider/model`
2. **成本优先** - 按token价格排序
3. **延迟优先** - 按p95 latency排序
4. **quality优先** - 按ProviderProfile评分
5. **tier fallback** - subscription → api → cheap → free
6. **auto v6** - GPT-4o-mini预分类 (升级中)

#### OmniRoute (19种)

1. **Auto Combo** - 多模型序列自动排序
2. **Featured Models** - 精选模型优先
3. **Cost-Optimized** - 成本最优化
4. **Speed-First** - 低延迟优先
5. **Quality-Balanced** - 质量平衡
6. **Thinking Budget** - 推理token预算管理
7. **Model Affinity** - provider亲和性
8. **Quota-Aware P2C** - quota感知power-of-2-choices
9. **Account Fallback** - 多账号自动切换
10. **Circuit Breaker** - 熔断器保护
11. **Session Affinity** - 会话粘性
12. **Context Relay** - 上下文传递
13. **Compression Combo** - 压缩pipeline
14. **Rate Limit Shield** - 速率限制屏蔽
15. **Free-Tier Cascade** - 免费tier瀑布
16. **Retry with Backoff** - 指数退避重试
17. **Lockout Policy** - provider锁定策略
18. **Budget Gate** - 预算门禁
19. **Eval-Driven Routing** - 基于评估的路由

### 2.2 关键差距：Auto Combo Engine

OmniRoute的Auto Combo Engine是核心竞争力：

```typescript
// OmniRoute: src/sse/handlers/autoRouting.ts
interface ComboStep {
  provider: string;
  model: string;
  connection: string;  // account identifier
  compositeTiers: number[];  // [1, 2, 3, 4] - runtime ordering
}

function resolveAutoCombo(req: ChatRequest): ComboStep[] {
  // Step 1: 解析请求特征
  const features = extractFeatures(req);

  // Step 2: 评分权重
  const weights = {
    cost: getWeight('cost', features),
    speed: getWeight('speed', features),
    quality: getWeight('quality', features),
    quota: getWeight('quota', features),
  };

  // Step 3: 候选池
  const candidates = filterCandidates(features, req.user.subscriptions);

  // Step 4: 综合评分排序
  const scored = candidates.map(c => ({
    ...c,
    score: calculateCompositeScore(c, weights, features),
  }));
  scored.sort((a, b) => b.score - a.score);

  // Step 5: 构建combo序列
  return scored.slice(0, 5).map(c => ({
    provider: c.provider,
    model: c.model,
    connection: selectConnection(c, req.user),
    compositeTiers: calculateTiers(c),
  }));
}
```

**融合建议**:
- 在 `autoroute/` 新增 `auto_combo_engine.go`
- 引入 `compositeTiers` 概念到路由决策
- 保留llm-gateway的ProviderProfile评分，作为quality权重输入

---

## 3. 压缩技术对比

### 3.1 两种压缩模式

#### llm-gateway-go: 会话级压缩

```go
// 特点: 针对单个会话的历史压缩
// 策略: delta-append + 滑动窗口 + 摘要生成
func (sc *SessionCompressor) Prepare(ctx, req) {
    // 1. 读取会话状态
    state := sc.readState(req.SessionID)

    // 2. 增量append
    state.Messages = append(state.Messages, req.NewMessages...)

    // 3. 窗口触发压缩
    if state.MsgCount > 20 || state.TokenEstimate > 8000 {
        summary := generateSummary(state.Messages[:10])
        state.Messages = [summary] + state.Messages[10:]  // 保留最近10轮
    }
}
```

**优势**: 会话上下文保持连贯
**局限**: 不处理工具输出、代码块等冗余

#### OmniRoute: 全局压缩Pipeline

```typescript
// 特点: 请求级别的内容压缩
// 策略: RTK (减少工具输出) + Caveman (token级压缩)
function applyCompressionPipeline(messages: Message[]): Message[] {
  let compressed = messages;

  // Step 1: RTK (Reduced Token Kit)
  // 工具输出压缩: 只保留关键字段
  compressed = rtkCompress(compressed, {
    maxToolOutputLength: 500,
    preserveFields: ['status', 'result', 'error'],
  });

  // Step 2: Caveman
  // Token级压缩: 去除冗余词、缩写
  compressed = cavemanCompress(compressed, {
    aggressiveness: 'medium',  // low / medium / high
    preserveCode: true,
    preserveUrls: true,
  });

  return compressed;
}

// RTK示例
function rtkCompress(messages) {
  return messages.map(msg => {
    if (msg.role === 'tool') {
      // 工具输出大幅压缩
      const output = JSON.parse(msg.content);
      return {
        ...msg,
        content: JSON.stringify({
          status: output.status,
          result: truncate(output.result, 500),
          _compressed: true,
        }),
      };
    }
    return msg;
  });
}

// Caveman示例
function cavemanCompress(messages, opts) {
  return messages.map(msg => {
    if (msg.role === 'user' || msg.role === 'assistant') {
      let content = msg.content;

      // 去除填充词
      content = content.replace(/\b(um|uh|like|you know)\b/gi, '');

      // 缩写常见词
      content = content.replace(/\byou are\b/gi, "you're");
      content = content.replace(/\bcannot\b/gi, "can't");

      // 保留代码块不压缩
      if (opts.preserveCode) {
        content = preserveCodeBlocks(content);
      }

      return { ...msg, content };
    }
    return msg;
  });
}
```

**优势**: 大幅减少token消耗（15-95%）
**局限**: 不保存会话状态，每次请求都需重新压缩

### 3.2 融合方案：混合压缩

```
请求 → [RTK+Caveman全局压缩] → [会话级delta-append] → 上游
                ↓                          ↓
          减少单次token消耗          保持会话上下文连贯
```

**实现建议**:
1. 在 `domains/hooks/compression/` 新增 `rtk.go` + `caveman.go`
2. SessionCompressor.Prepare前先执行RTK+Caveman
3. 压缩后的内容进入会话缓存
4. 配置项: `COMPRESSION_PIPELINE_ENABLED`, `RTK_MAX_TOOL_OUTPUT`, `CAVEMAN_AGGRESSIVENESS`

---

## 4. 会话管理对比

### 4.1 功能矩阵

| 功能 | llm-gateway-go | OmniRoute | 优势方 |
|------|---------------|-----------|--------|
| **三层缓存** | L0(LRU) + L1(PG) + L2(Redis) | SQLite + 内存 | llm |
| **压缩策略** | delta-append + 摘要 | RTK + Caveman | OmniRoute |
| **标题抽取** | 异步auto-title | ❌ 无 | llm |
| **会话摘要** | 异步auto-summary | ❌ 无 | llm |
| **质量评估** | ❌ 规划中 | Eval framework | OmniRoute |
| **任务关联** | task_id绑定 | ❌ 无 | llm |
| **多租户** | tenant_id隔离 | 单租户 | llm |
| **Session Affinity** | executor级sticky | Session tracking | 功能对等 |
| **Context Relay** | ❌ 无 | 账号轮换时上下文传递 | OmniRoute |

### 4.2 OmniRoute Context Relay (值得借鉴)

```typescript
// OmniRoute: src/lib/contextRelay.ts
// 功能: 账号轮换时传递会话上下文，保持连贯性

interface ContextRelaySummary {
  sessionId: string;
  turnCount: number;
  lastModel: string;
  conversationSummary: string;  // 自动生成
  userIntent: string;           // 推断的用户意图
  pendingTasks: string[];       // 未完成任务列表
}

function createContextRelay(session: Session): ContextRelaySummary {
  return {
    sessionId: session.id,
    turnCount: session.turns.length,
    lastModel: session.lastModelUsed,
    conversationSummary: summarizeConversation(session.turns),
    userIntent: inferIntent(session.turns),
    pendingTasks: extractPendingTasks(session.turns),
  };
}

function injectContextRelay(req: ChatRequest, relay: ContextRelaySummary): ChatRequest {
  // 注入system message
  const systemMsg = {
    role: 'system',
    content: `[Context from previous session ${relay.sessionId}]
Conversation summary: ${relay.conversationSummary}
User intent: ${relay.userIntent}
Pending tasks: ${relay.pendingTasks.join(', ')}
Please continue the conversation naturally.`,
  };

  return {
    ...req,
    messages: [systemMsg, ...req.messages],
  };
}
```

**llm-gateway-go应用场景**:
- 多Key轮换时保持上下文
- 跨provider fallback时传递会话状态
- 实现: 在 `domains/session/v2/` 新增 `context_relay.go`

---

## 5. 供应商监控对比

### 5.1 指标体系

#### llm-gateway-go: ProviderProfile

```go
type ProviderProfile struct {
    // 基础指标
    SuccessRate        float64
    AvgLatencyMs       float64
    P95LatencyMs       float64
    ErrorRate          float64

    // 评分信号 (新增)
    RateLimitMetrics   RateLimitMetrics      // 速率限制历史
    AvailabilityWindow AvailabilityWindow    // 可用性时间窗口
    EffLimit           int                   // 有效并发限制

    // 质量维度
    IQScore            float64  // 模型智商测试分数
    ComplianceScore    float64  // 输出合规性分数
}

type RateLimitMetrics struct {
    Last24hThrottles   int
    Last7dThrottles    int
    AvgCooldownMinutes float64
}

type AvailabilityWindow struct {
    Uptime7d      float64  // 7天可用率
    Uptime30d     float64  // 30天可用率
    MTBF          float64  // 平均故障间隔 (分钟)
    MTTR          float64  // 平均恢复时间 (分钟)
}
```

#### OmniRoute: Circuit Breaker + Telemetry

```typescript
interface CircuitBreakerState {
  state: 'closed' | 'open' | 'half_open';
  failureCount: number;
  successCount: number;
  lastFailureTime: Date;
  nextRetryTime: Date;
}

interface ProviderTelemetry {
  requestCount: number;
  errorCount: number;
  latencyP50: number;
  latencyP95: number;
  latencyP99: number;
  quotaRemaining: number;  // 实时quota
  circuitBreakerState: CircuitBreakerState;
}
```

### 5.2 融合方案：统一监控指标

```go
type UnifiedProviderMetrics struct {
    // 来自llm-gateway
    Profile            ProviderProfile

    // 来自OmniRoute
    CircuitBreaker     CircuitBreakerState
    Telemetry          ProviderTelemetry

    // 新增综合指标
    HealthScore        float64  // 0-100，综合健康度
    RecommendationRank int      // 推荐排名
}

func CalculateHealthScore(m *UnifiedProviderMetrics) float64 {
    // 权重配置
    weights := map[string]float64{
        "success_rate":   0.30,
        "availability":   0.25,
        "latency":        0.20,
        "rate_limit":     0.15,
        "quality":        0.10,
    }

    score := 0.0
    score += weights["success_rate"] * m.Profile.SuccessRate * 100
    score += weights["availability"] * m.Profile.AvailabilityWindow.Uptime7d * 100
    score += weights["latency"] * (1 - min(m.Profile.P95LatencyMs/5000, 1)) * 100
    score += weights["rate_limit"] * (1 - min(m.Profile.RateLimitMetrics.Last24hThrottles/10, 1)) * 100
    score += weights["quality"] * m.Profile.IQScore

    return score
}
```

---

## 6. 成本统计对比

### 6.1 功能对比

| 维度 | llm-gateway-go | OmniRoute | 优势方 |
|------|---------------|-----------|--------|
| **多租户成本** | ✅ 按tenant_id统计 | ❌ 单用户 | llm |
| **项目任务关联** | ✅ task_id绑定 | ❌ 无 | llm |
| **实时成本** | ✅ Prometheus | SQLite查询 | llm |
| **成本预测** | ❌ 规划中 | ❌ 无 | - |
| **预算告警** | ✅ Grafana alerts | ❌ 无 | llm |
| **节省统计** | ❌ 规划中 | ✅ 免费tier节省展示 | OmniRoute |

### 6.2 借鉴OmniRoute节省统计

```typescript
// OmniRoute: 实时计算使用免费tier节省的成本
interface CostSavings {
  totalSaved: number;      // 累计节省 (USD)
  savingsThisMonth: number;
  breakdown: {
    provider: string;
    model: string;
    tokensSaved: number;
    costSaved: number;     // 对比付费tier的价格
  }[];
}

function calculateSavings(usage: Usage[]): CostSavings {
  let totalSaved = 0;
  const breakdown = [];

  for (const u of usage) {
    if (u.tierType === 'free') {
      // 查询该model的付费价格
      const paidPrice = getPaidPrice(u.provider, u.model);
      const saved = u.tokens * paidPrice;
      totalSaved += saved;
      breakdown.push({
        provider: u.provider,
        model: u.model,
        tokensSaved: u.tokens,
        costSaved: saved,
      });
    }
  }

  return { totalSaved, breakdown };
}
```

**融合到llm-gateway**:
- 新增表: `cost_savings` (tenant_id, provider, model, tokens_saved, cost_saved)
- API: `GET /admin/cost/savings?tenant=xxx&window=30d`
- 前端: `web/src/views/CostPanel.vue` 增加节省卡片

---

## 7. 融合方案优先级

### 7.1 P0 (2周内)

1. **Free-Tiers Dashboard** (前端)
   - 复用OmniRoute `/dashboard/free-tiers` UI设计
   - 后端API: 利用现有 `free_resource_catalog`
   - 文件: `web/src/views/FreeTiersPanel.vue`

2. **RTK+Caveman压缩** (后端)
   - 移植OmniRoute压缩算法到Go
   - 集成到SessionCompressor.Prepare前置步骤
   - 文件: `domains/hooks/compression/rtk.go`, `caveman.go`

3. **Auto Combo Engine基础** (后端)
   - 实现 `compositeTiers` 概念
   - 路由决策支持多模型序列
   - 文件: `autoroute/auto_combo_engine.go`

### 7.2 P1 (4周内)

4. **Context Relay** (后端)
   - 实现跨provider fallback时上下文传递
   - 文件: `domains/session/v2/context_relay.go`

5. **统一健康度评分** (后端)
   - 融合ProviderProfile + CircuitBreaker指标
   - 计算综合HealthScore
   - 文件: `domains/providerprofile/health_score.go`

6. **成本节省统计** (全栈)
   - 后端: `admin/cost_savings.go`
   - 前端: CostPanel增加节省展示
   - 数据库: 新增 `cost_savings` 表

### 7.3 P2 (2月内)

7. **19种路由策略完整实现**
   - 按OmniRoute策略清单逐个实现
   - 配置化: `routing_policy` 表扩展

8. **Eval框架集成**
   - 借鉴OmniRoute eval framework
   - 会话质量自动评估
   - 文件: `domains/eval/`

9. **压缩Combo Pipeline**
   - 支持自定义压缩pipeline
   - 配置: `[RTK] → [Caveman] → [Custom]`
   - 文件: `domains/hooks/compression/pipeline.go`

---

**文档结束**

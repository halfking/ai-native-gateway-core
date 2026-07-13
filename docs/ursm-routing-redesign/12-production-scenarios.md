# 生产场景深度模拟与方案补强

> 基于代码审计和纸面推演，解决三个核心生产问题：
> 1. 5xx 立即降级 → 需探测确认后再降级
> 2. 供应商超时无返回 + 客户端取消/重试 → 缓存回放
> 3. 全场景路由状态一致性与会话稳定

## 1. 问题一: 5xx 立即降级

### 1.1 现有行为 (代码审计确认)

```
5xx → ClassifyError → {KindUpstreamDown, KindConcurrent, KindTransient}
  → Circuit.RecordFailure (consecutive++)
    → consecutive >= 2: Circuit OPEN
      → shouldWriteCredentialStateOnConfirmedFailure
        → cmb.available=FALSE, unavailable_recover_at=now+冷却
           冷却: KindUpstreamDown=60s, KindConcurrent=5min, KindTransient=30s
  → StateObserver.UpdateOnFailure
    → transient consecutive >= 3: cmb.available=FALSE, 5min 冷却 + 调度探测
  → HealthTracker.OnError → 异步滑动窗口
    → 1h 窗口, >=80% 失败率, 最少 5 样本 → 15min 冷却
```

**问题**: 二次 5xx 就降级 (breaker threshold=2)，未做探测确认。虽然 breaker 有 30s 自动半开 + 探测，但 60s/5min 的 DB 冷却期在 5xx 是瞬时(供应商降级/网络闪断)还是持久(API 下线)未区分。

### 1.2 方案: 分级确认降级

```
Level 1: 断路器快速保护 (本地内存, 秒级)
  5xx → consecutive++ → 2次 → 本地 circuit open 30s
  → 30s 后自动 half-open → 放行 1 个探测请求
  → 成功: circuit close, 不写 DB
  → 失败: 重新 open, consecutive 继续累加

Level 2: DB 降级 (需探测确认)
  circuit open 状态持续 >= N 次 (如 >=3 次断路器半开失败)
  或健康检查器滑动窗口确认 (1h 内 >=80% 失败, 最少 10 样本)
  → 才写入 cmb.available=FALSE + 调度主动探测
  → 主动探测成功前禁止其它请求路由至此

Level 3: 凭据级降级 (持久层)
  多个模型同时 5xx → 凭据级降级
  同一故障模式在多个模型间级联验证 → provider 级降级
```

**修正**: `shouldWriteCredentialStateOnConfirmedFailure` 现在要求断路器在**半开失败过** (>=3 consecutive circuit transitions) 才允许 DB 降级。仅透传瞬时闪断而不写 DB。

```go
// 新增逻辑
func shouldWriteDBDegradation(kind ErrorKind, circuit *Breaker) bool {
    // KindQuota/KindAuth/KindModelNotFound: 立即写入
    if IsImmediatePersist(kind) {
        return true
    }
    // 瞬态类: 需要半开探测失败确认
    if IsTransientFamily(kind) {
        if circuit.State() != CircuitOpen {
            return false // 还没到需要降级的程度
        }
        if circuit.HalfOpenFailureCount() < 3 {
            return false // 半开探测失败不够, 再试
        }
        return true // 3 次半开都失败, 确认降级
    }
    return false
}
```

## 2. 问题二: 超时/取消 + 重试 = 响应缓存回放

### 2.1 场景时序 (常见生产问题)

```
时序:
  T0: 客户端发送请求 R1 (requestID_1, 消息 M)
  T1: 网关选路 → 上游供应商 A (rawModel="gpt-4o-xxx")
  T2: 上游 A 已接收请求, 正在生成
  T3: 客户端取消 (cancel / timeout) → 网关停止等待首 token
  T4: 上游 A 继续生成完成, 响应到达网关
  T5: 客户端发送请求 R2 (requestID_2, 相同消息 M 或 "请继续")
  T6: 网关应做什么?
```

### 2.2 现有机制

| 机制 | 覆盖范围 | 不足 |
|------|---------|------|
| IdempotentCache | sessionID + requestID | requestID 不同则未命中 |
| PendingStore | sessionID + requestID + 后台捕获 | 需客户端显式轮询 |
| 无 | 相同 prompt 不同 requestID | ❌ 无兜底 |

### 2.3 方案: 三层层叠缓存

```
Layer 1: Request ID 精确去重 (已有, 增强)
  键: sessionID:requestID
  命中: 返回 X-Gw-Pending (已有)
  增强: 追加 pendingStore 状态查询, 提高准确率

Layer 2: 会话级响应缓存 (新增)
  键: sessionID:promptHash
  值: {status, response_chunks, credential_id, raw_model, created_at}
  TTL: 5 分钟 (短 TTL, 只覆盖重试窗口)
  写入时机: 上游成功完整返回后
  回放条件:
    - 同一 sessionID
    - prompt_hash 相同 (由 prefix.Stabilize + SHA256 计算)
    - 未超过 TTL
    - 同一 rawModel 仍可用 (模型没变)
  注意: 缓存保存原始响应体, 回放时带上 X-Gw-Replay: true

Layer 3: PendingStore 后台捕获 (已有, 增强)
  捕获更完整: 即使客户端取消, 继续流式接收完整响应
  增加: 取消后请求标记, 新请求 "请继续" 时:
    - 从 PendingStore 读取
    - 返回已有响应 + X-Gw-Pending-Replay: true
  回放条件:
    - 同一 sessionID
    - 新请求是前一个请求的延续 (如 "请继续")
    - 由 goal hook 或 intent 分析判定为延续请求
```

### 2.4 "请继续" / "请重试" 处理逻辑

```go
// handler.go
func (h *ChatHandler) HandleIdempotent(ctx context.Context, req *Request, env *URSMEnvironment) (*Response, error) {
    // Step 1: Request ID 精确去重
    if cached := h.idempotentCache.CheckAndMark(req.SessionID, req.RequestID); cached {
        // 已有相同的 requestID 在处理中
        return h.respondPending(req.SessionID, req.RequestID)
    }
    
    // Step 2: Prompt Hash 匹配 → 相同消息的重试
    promptHash := hashPrompt(req.Messages)
    if cachedResp := h.sessionResponseCache.Get(req.SessionID, promptHash); cachedResp != nil {
        // 校验时效和模型一致性
        if time.Since(cachedResp.CreatedAt) < 5*time.Minute {
            if binding := h.bindingMgr.Get(req.SessionID, req.Model); binding != nil {
                if binding.RawModel == cachedResp.RawModel {
                    env.Routing.ReplaySource = "response_cache"
                    return cachedResp.Response, nil
                }
            }
        }
        // 缓存过期/模型变了: 正常请求, 但跳过缓存
        h.sessionResponseCache.Invalidate(req.SessionID, promptHash)
    }
    
    // Step 3: Goal/Intent 分析 → "请继续" 意图
    if h.isContinuationIntent(req) {
        // 检查是否有 PendingStore 中的完成响应
        pendingResp := h.pendingStore.GetLatest(req.SessionID)
        if pendingResp != nil && pendingResp.Status == "completed" {
            if pendingResp.RawModel == env.GetBindingRawModel() {
                env.Routing.ReplaySource = "pending_store"
                return pendingResp.Response, nil
            }
        }
    }
    
    // Step 4: 正常路由
    return h.executeNormal(ctx, req, env)
}
```

### 2.5 约束

```
1. 响应缓存 TTL 严格控制在客户端重试窗口内 (默认 5min)
2. 只有完整成功的响应才可缓存 (partial 不缓存)
3. 模型/凭据变了, 缓存无效 (不同供应商的回复不同)
4. 缓存回放必须标记 X-Gw-Replay: true, 方便下游计费审计
5. "请继续" 只有在前一个请求是流式且已完整捕获时才可回放
6. 缓存不得包含凭据或敏感信息
```

## 3. 完整状态机

### 3.1 路由状态矩阵

```text
状态 = (provider_state, credential_state, model_state, node_state, binding_state)

provider_state: active | manual_disabled | auto_disabled
credential_state: active | auth_failed | quota_exhausted | quota_periodic | suspended | manual_disabled
model_state: active | binding_unavailable | offer_unavailable | broken_confirmed | probe_pending
node_state: available | circuit_open | consecutive_failure | disabled_until
binding_state: strong | soft | reevaluating | invalidated
```

### 3.2 状态转换决策表

| 事件 | provider | credential | model | node | binding |
|------|----------|------------|-------|------|---------|
| 5xx x1 | - | - | - | consecutive++ | - |
| 5xx x2 | - | - | - | circuit_open(30s) | - |
| 5xx x2 + half-open fail | - | - | probe_pending | circuit_open | - |
| 5xx x3 + probe_confirm | - | - | broken_confirmed | disabled_until | invalidated |
| auth 失败 | - | auth_failed | broken_confirmed | disabled_until | invalidated |
| 429 | - | - | - | rate_limited(冷却) | - |
| 流超时 x3 | - | - | probe_pending | circuit_open | - |
| 探测成功 | - | - | active | available | 恢复选路 |
| 管理员禁用 | - | manual_disabled | - | - | invalidated |
| 配额耗尽 | - | quota_exhausted | broken_confirmed | disabled_until | invalidated |
| 9x% 成功率恢复 | - | - | active | available | 恢复选路 |
| 缓存命中 | - | - | - | - | 保持 |
| 客户端取消 | - | - | - | - | 保持 |
| 客户端参数错误 | - | - | - | - | 保持 |

### 3.3 状态查询优先级

```go
// router.go
func (r *Router) IsNodeAvailable(credID int, model string) bool {
    // Fast path: binding 命中
    if binding := r.bindingCache.Get(sessionID, model); binding != nil {
        if binding.Status == "invalidated" {
            return false
        }
        // 强绑定: 除非已失效, 否则视为可用
        if binding.Level == Strong {
            return true
        }
    }
    
    // Normal path: 逐层检查
    provider := r.providerCache.Get(providerID)
    if !provider.IsAvailable() {
        return false
    }
    credential := r.credentialCache.Get(credID)
    if !credential.IsAvailable() {
        return false
    }
    modelState := r.modelCache.Get(credID, model)
    if !modelState.IsAvailable() {
        return false
    }
    node := r.nodeCache.Get(credID, model)
    return node.IsAvailable()
}
```

## 4. 全场景重验证

### 4.1 新场景: S24 — 5xx 闪烁 (flash 5xx)

```
条件: 供应商偶发 5xx, 每 5 分钟出现 1-2 次, 其余正常
当前行为:
  2次 5xx → circuit open 30s → half-open 成功 → close
  但 5xx 共现时 10 分钟内可能写 2 次 DB 降级 (健康检查器 1h 窗口)
目标行为:
  断路器保护即时生效, DB 降级延迟确认
  健康检查器增加连续成功窗口: 3 次成功恢复可清除窗口内的失败记录
```

### 4.2 新场景: S25 — 供应商完全宕机 + 恢复

```
条件: 供应商 500 持续 10 分钟, 然后恢复
当前行为:
  前 2 次: circuit open → half-open → 再 fail → open...
  第 3 次 half-open 失败: DB broken_confirmed (probe_pending)
  探测 worker 每 30s 探一次, 持续 10 分钟
  恢复后: 探测成功 → 恢复 active
目标行为:
  探测 backoff 从 30s 指数增长到 5min, 减少恢复期压力
  provider 级 gate 在连续 3+ 模型 broken 时自动降低 provider 权重
```

### 4.3 新场景: S26 — "请继续" 多轮场景

```
条件: 客户端首次发送 "写一篇关于 AI 的文章"
  → 供应商 A 响应完整文章
  缓存到 sessionResponseCache (promptHash_1)
  
  客户端第二轮: "请继续"
  → 已缓存 sessionID + promptHash_2 (不同 prompt, 未命中)
  → 正常路由
  
  客户端第三轮: 同 "写一篇关于 AI 的文章" (重试/误发)
  → sessionResponseCache 命中 promptHash_1
  → 直接回放
  → X-Gw-Replay: true

约束: 缓存 TTL 默认 5 分钟, 可配置
      回放响应在 request_logs 标记 replayed=true
```

### 4.4 新场景: S27 — 旧请求超时, 新请求命中同一凭据

```
条件:
  请求 R1 (requestID_1): 路由到 gpt-4o/openai, 超时 (60s 无响应)
  客户端取消, 发送 R2 (requestID_2, 相同 prompt)
  
当前: R2 重新路由, 可能同一节点重试 3 次, 再返 503
目标: R2 检测到:
  - 同一 sessionID, 同一 promptHash
  - 同一节点上一次超时记录 (circuit open 或 pending state)
  - 不浪费重试, 直接返回 503 + X-Gw-Retry-After: 30s
```

## 5. 实现优先级

| 优先级 | 改动 | 影响范围 | 预计工时 |
|--------|------|----------|----------|
| P0 | 5xx 分级确认降级 (半开失败 >=3 才写 DB) | breaker.go, writer.go, manager.go | 2d |
| P0 | sessionResponseCache 响应缓存 | 新增 cache/session_response/ 包 | 2d |
| P0 | prompt_hash 计算 + goal hook "请继续" 识别 | handler.go, goal/hook | 1d |
| P1 | PendingStore 增强 (取消后继续捕获) | pending/pending.go | 1d |
| P1 | IdempotentCache 增强 (跨 requestID) | idempotent.go, handler.go | 1d |
| P1 | 路由状态矩阵统一查询 | router.go, ursm/cache | 2d |
| P2 | 全场景端到端集成测试 | e2e/ 新增 | 3d |

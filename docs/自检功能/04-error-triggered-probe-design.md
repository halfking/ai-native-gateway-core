# 错误触发的主动探测（Error-Triggered Active Probe）— 完整设计文档

> **作者**: OpenCode AI Agent
> **日期**: 2026-07-13
> **目标**: 解决"请求出错后为什么没有马上启动探测"的痛点。当业务请求连续失败 ≥2 次时，立即对失败凭据做**直连上游**探测，按 5s → 30s → 2m → 5m → 15m 的 backoff 规则最多跑 5 轮。所有探测记录复用 `request_logs` 表并自动出现在"实时请求流"中，前端可一键过滤"仅探测"。

---

## 一、背景与目标

### 1.1 用户原始诉求

> "我们在请求出错后，为什么没有马上启动探测？直连客户进行探测，成功后再通过我们的网关入口再进行探测，这样请求就会在我们的"实时请求流"中可以看到，就可以知道我们有没有探测，结果如何，可以排除客户端的问题。请根据此需求，进行完善，探测不会只是一次，要按一定的规则进行。"

### 1.2 现状分析（已读完所有相关代码）

| 现有机制 | 触发条件 | 是否直连 | 是否进实时流 | 是否多轮 | 痛点 |
|---|---|---|---|---|---|
| `SelfCheckWorker` (`bg/self_check_worker.go`) | 周期 60s/30s | ❌ 走 gateway | ❌ 仅写 self_check_runs | ❌ 单次 | 不响应实时错误，探测本身也走 gateway 无法隔离 |
| `PassiveProbeListener` (`bg/passive_probe_listener.go`) | 30s 轮询 + 累计计数 | ❌ 间接（靠状态机） | ❌ 不进流 | ✅ 进入 reviewing 状态 | 反应慢（30s 一次扫描）、5 分钟 reviewing 窗口太慢 |
| `CredentialProbeV2` (`bg/credential_probe_v2.go`) | 1h 周期 + 失败后 5min fast reprobe | ✅ 直连 | ❌ 不进流 | ✅ fastReprobeQueue | 延迟太高，5 分钟才复测，期间故障窗口扩大 |
| `ModelProbeRunner.TriggerManual` (`bg/model_probe.go`) | 仅手动触发 | ✅ 直连 | ❌ 不进流 | ❌ 单次 | 没有自动触发路径 |
| `CredentialStateManager.UpdateOnFailure` (`domains/credentialstate/manager.go`) | 连续 ≥3 次 | ❌ 仅 SubmitFastProbe（5 分钟延迟） | ❌ 不进流 | ❌ 提交一次后等 5 分钟 | 门槛太高（3 次）、延迟太长（5 min）、无状态可视化 |

### 1.3 设计目标

1. **即时响应**：连续失败 ≥2 次，**第 2 次失败立即触发**直连探测（0s 延迟，不等周期扫描）
2. **直连上游**：使用 provider `base_url` + 解密的 `api_key`，**绕过 gateway**，可隔离 gateway 与上游两侧故障
3. **多轮规则**：backoff 链 **5s → 30s → 2m → 5m → 15m**，最多 5 轮
4. **实时流可视化**：探测请求复用 `request_logs` 表，自动经 `telemetry.onEmitted → SSE hub` 推到 dashboard
5. **可识别**：新增 `task_type='probe_triggered'`、`task_type_chosen='probe_direct'`、`quality_flags=['probe','direct',...]`、request_id 前缀 `probe-direct-`、前端 `🛡️ Probe` 盾牌图标
6. **过滤**：实时流新增"仅探测"filter tab
7. **客户端排除**：探测与原始失败请求通过 `parent_request_id` 关联，能区分客户端问题与 gateway/上游问题
8. **闭环**：探测成功后立即把 `CredentialStateManager.Available` 翻回 `true`，让路由恢复正常；探测失败则按 backoff 进入下一轮

### 1.4 与现有模块的关系

```
┌──────────────────────────────────────────────────────────────────┐
│                       现有模块（不变）                              │
│   CredentialStateManager.UpdateOnFailure (line 141)               │
│   └─ 已有 SetProbeSubmitter(credFn, modelFn) 接口                 │
│   └─ 现只对 consecutive_fails >= 3 触发 SubmitFastProbe            │
│                                                                  │
│   SelfCheckWorker / PassiveProbeListener / CredentialProbeV2     │
│   └─ 不动，继续负责周期自检 / 被动观察 / 凭据级周期探测             │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│                    本次新增模块                                    │
│   ActiveProbeWorker (bg/active_probe_worker.go)                   │
│   ├─ 接收 (credID, model) 提交                                      │
│   ├─ 状态机: idle → running → succeeded | failed_retry | failed_final│
│   ├─ Backoff 计算 (5s → 30s → 2m → 5m → 15m)                      │
│   ├─ 去重: 同 (credID, model) 在 running 期间拒绝重复提交           │
│   ├─ 调用 ActiveProbeExecutor 直连探测                              │
│   ├─ 调用 ActiveProbeEmitter 写 request_logs + 推 SSE              │
│   └─ 调用 CredentialStateManager.UpdateFromProbe 闭环              │
└──────────────────────────────────────────────────────────────────┘
```

**关键设计**：不创建新数据库表。复用 `request_logs`、`request_logs_hot`、通过 `quality_flags` 数组 + `task_type` 字段标识探测请求。

---

## 二、架构与流程

### 2.1 组件图

```
                          ┌──────────────────────────────────┐
                          │  Request Fails (transient/...)   │
                          │  domains/streaming/handler.go    │
                          └──────────────┬───────────────────┘
                                         │ recordFailedRequest → UpdateOnFailure
                                         ▼
                          ┌──────────────────────────────────┐
                          │  CredentialStateManager          │
                          │  domains/credentialstate/        │
                          │  manager.go:141                  │
                          │                                  │
                          │  if consecutive_fails >= 2 →     │
                          │    m.activeProbeSubmitter(       │
                          │      credID, model, parentReqID  │
                          │    )                             │
                          └──────────────┬───────────────────┘
                                         │
                                         ▼
                          ┌──────────────────────────────────┐
                          │  ActiveProbeWorker               │
                          │  bg/active_probe_worker.go       │
                          │  ─────────────────────────       │
                          │  Submit(credID, model, parent)   │
                          │    ↓                             │
                          │  dedup map[credID|model]         │
                          │    ↓                             │
                          │  enqueue (channel, cap=128)      │
                          │    ↓                             │
                          │  runLoop() goroutine:            │
                          │    pull from queue               │
                          │    schedule via backoff timer    │
                          │    ↓                             │
                          │    ActiveProbeExecutor.Run()     │
                          │      ↓                           │
                          │    ActiveProbeEmitter.Emit()     │
                          │      ↓                           │
                          │    CredentialStateManager        │
                          │      .UpdateFromProbe()          │
                          └──────────────────────────────────┘
```

### 2.2 完整时序

```
T+0s   : 业务请求 #1 失败 (transient/timeout/...)
         → CredentialStateManager.UpdateOnFailure(consecutive_fails: 0→1)
         → 仅计数，不触发探测（避免误触发）

T+5s   : 业务请求 #2 失败 (同类错误)
         → UpdateOnFailure(consecutive_fails: 1→2)
         → 检测到 consecutive_fails >= 2
         → ActiveProbeWorker.Submit(credID, model, parentReqID)
         → 入队 active_probe_queue

T+5s+10ms : Worker 取出任务
         → ActiveProbeState: idle → running
         → attempt = 1, backoff(1) = 0s（立即执行）
         → ActiveProbeExecutor:
            - 解密 secret_ciphertext
            - 构造 chat ping (max_tokens=1)
            - POST {provider_base_url}/v1/chat/completions
            - 收集 (status, http_code, err_code, latency_ms, body_preview)
         → ActiveProbeEmitter.Emit():
            - 构造 RequestLogEntry:
                request_id: "probe-direct-{credID}-{model}-{ts}"
                tenant_id: "system"
                task_type: "probe_triggered"
                task_type_chosen: "probe_direct"
                is_auto_request: true
                quality_flags: ["probe", "direct", "trigger_consecutive_2"]
                parent_request_id: "{原始失败请求ID}"
                error_kind: "probe_direct_timeout" (失败时)
            - c.telemetryClient.EmitRequestLogInsert(entry)
            - onEmitted hook → LiveStreamSSEHub.Publish()
            - dashboard 立即看到 🛡️ 盾牌行

T+5s+800ms : Provider 返回 200 OK
         → ActiveProbeExecutor 返回 success
         → ActiveProbeEmitter 写 success=true 的 request_log
         → CredentialStateManager.UpdateFromProbe(state):
              Available: false → true
              Source: "probe_direct"
              consecutive_fails → 0
         → 路由立即恢复使用该凭据

T+5s+850ms : 进入 succeeded 终态，从 dedup map 移除
         → 退出 backoff

─────────────────────────────────────────────────────────────────

T+5s+800ms : Provider 返回 503
         → ActiveProbeExecutor 返回 failed
         → ActiveProbeEmitter 写 success=false 的 request_log
         → CredentialStateManager.UpdateFromProbe(state):
              Available: true → false (cooling 5min)
              Source: "probe_direct"
              LastError: "upstream_503"

T+5s+810ms : attempt (1) < max_attempts(5) → 重新入队
         → next_backoff = backoff(1) = 5s    (chain[0]，首次重试延迟)

T+10s  : Worker 取出 (attempt=2)
         → 再次直连探测
         → 重复上面的流程
         ...
T+10s      : 第 1 次失败 → +5s 后第 2 次                = T+15s
T+15s      : 第 2 次失败 → +30s 后第 3 次               = T+45s
T+45s      : 第 3 次失败 → +2min 后第 4 次              = T+2m45s
T+2m45s    : 第 4 次失败 → +5min 后第 5 次              = T+7m45s
T+7m45s    : 第 5 次失败 → failed_final 终态
         → 退出探测循环，依赖 passive_probe_listener / credential_recovery 自动恢复

注：以上时间线假设第 1 次提交的瞬时触发在 T+5s（即业务第 2 次失败触发 Submit
的时间）。链 [5s, 30s, 2m, 5m, 15m] 共 5 项，其中 15m 是 chain[4] 的 cap，
在默认 MaxAttempts=5 配置下不会真正被消费（attempt 5 失败后 attempt >= 
MaxAttempts 直接进入 failed_final）。
```

---

## 三、Backoff 算法

### 3.1 配置

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `error_probe.enabled` | `true` | 是否启用错误触发探测 |
| `error_probe.consecutive_threshold` | `2` | 连续失败次数阈值 |
| `error_probe.max_attempts` | `5` | 最多探测轮数 |
| `error_probe.initial_backoff_ms` | `5000` | 第 1 轮延迟 (5s) |
| `error_probe.backoff_step_ms` | `30000` | 后续轮次累加基数 (30s) |
| `error_probe.max_backoff_ms` | `900000` | 单轮最大 backoff (15min) |
| `error_probe.timeout_ms` | `10000` | 单次 HTTP 探测超时 (10s) |

### 3.2 Backoff 公式

```go
// 第 1 轮: 5s (initial_backoff)
// 第 2 轮: 30s (initial_backoff + backoff_step)
// 第 3 轮: 2min (5s + 2*30s + 60s extra) ... 实际是 backoff_chain 表查

// 更直观的实现：硬编码 backoff 链，与用户预期一致
var defaultBackoffChain = []time.Duration{
    5 * time.Second,    // attempt 1 → 5s
    30 * time.Second,   // attempt 2 → 30s
    2 * time.Minute,    // attempt 3 → 2m
    5 * time.Minute,    // attempt 4 → 5m
    15 * time.Minute,   // attempt 5 → 15m
}

// 如果配置了 initial_backoff_ms / backoff_step_ms / max_backoff_ms，
// 则按指数退避: delay(i) = min(initial * 2^(i-1), max)
```

**默认采用硬编码链**（用户预期一致），保留指数退避作为备选方案（如果未来需要自适应）。

### 3.3 Backoff 函数

```go
// computeBackoff 返回第 attempt 次探测前要等多久
// attempt 从 1 开始
func (w *ActiveProbeWorker) computeBackoff(attempt int) time.Duration {
    if attempt >= 1 && attempt <= len(defaultBackoffChain) {
        return defaultBackoffChain[attempt-1]
    }
    // 超过 chain 长度，返回最后一轮
    return defaultBackoffChain[len(defaultBackoffChain)-1]
}
```

---

## 四、状态机

### 4.1 单个 (credID, model) 的状态

```
        Submit(credID, model)
              │
              ▼
         ┌─────────┐
         │  idle   │  (不在 dedup map 里)
         └────┬────┘
              │ Submit 入队
              ▼
         ┌─────────┐
         │ running │  (在 dedup map 里, 当前 attempt < max)
         └────┬────┘
              │ 探测完成
       ┌──────┼──────┐
       ▼      ▼      ▼
 ┌─────────┐ ┌─────────┐ ┌─────────┐
 │succeed  │ │failed_  │ │failed_  │
 │         │ │retry    │ │final    │
 └─────────┘ └────┬────┘ └─────────┘
       │          │ 重新入队
       │          ▼
       │     (running, attempt+1)
       │          │
       │          │ 探测完成
       │     ┌────┼────┐
       │     ▼         ▼
       │  succeed    failed_*
       │
       └────────┬──────────┘
                ▼
          dedup map remove
                ▼
              idle
```

### 4.2 dedup Map

```go
type probeState struct {
    CredentialID  int
    Model         string
    Attempt       int          // 当前轮次
    NextRunAt     time.Time    // 下一轮探测时间
    LastResult    *probeResult // 上一轮结果
    LastTriggerAt time.Time    // 最初触发时间
    ParentReqID   string       // 原始失败请求 ID
}

type ActiveProbeWorker struct {
    // ...
    mu      sync.Mutex
    running map[string]*probeState // key: "{credID}|{model}"
    // ...
}

// Submit 调用：
func (w *ActiveProbeWorker) Submit(credID int, model, parentReqID string) {
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    if _, exists := w.running[key]; exists {
        w.mu.Unlock()
        slog.Debug("active_probe: dedup hit, already running", "cred", credID, "model", model)
        return // 已经在跑了，跳过
    }
    w.running[key] = &probeState{
        CredentialID: credID,
        Model:        model,
        Attempt:      0,
        NextRunAt:    time.Now(), // 立即执行
        ParentReqID:  parentReqID,
        LastTriggerAt: time.Now(),
    }
    w.mu.Unlock()

    select {
    case w.queue <- probeTask{CredID: credID, Model: model}:
        slog.Info("active_probe: submitted", "cred", credID, "model", model, "parent", parentReqID)
    default:
        slog.Warn("active_probe: queue full, dropping", "cred", credID, "model", model)
        w.mu.Lock()
        delete(w.running, key)
        w.mu.Unlock()
    }
}
```

### 4.3 状态机方法

```go
// markSuccess: 探测成功，移除 dedup entry
func (w *ActiveProbeWorker) markSuccess(credID int, model string) {
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    delete(w.running, key)
    w.mu.Unlock()
}

// markFailedRetry: 探测失败但还能继续
func (w *ActiveProbeWorker) markFailedRetry(credID int, model string, attempt int, nextRunAt time.Time) {
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    if s, ok := w.running[key]; ok {
        s.Attempt = attempt
        s.NextRunAt = nextRunAt
    }
    w.mu.Unlock()
    // 重新入队
    select {
    case w.queue <- probeTask{CredID: credID, Model: model}:
    default:
    }
}

// markFailedFinal: 探测失败且达到 max_attempts
func (w *ActiveProbeWorker) markFailedFinal(credID int, model string) {
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    delete(w.running, key)
    w.mu.Unlock()
    slog.Warn("active_probe: max attempts reached, final failure", "cred", credID, "model", model)
}
```

---

## 五、HTTP 直连探测执行器

### 5.1 ActiveProbeExecutor

```go
package bg

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/kaixuan/llm-gateway-go/internal/providercap"
    "github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
    "github.com/kaixuan/llm-gateway-go/secret"
)

type ActiveProbeExecutor struct {
    db        *pgxpool.Pool
    keyring   *secret.Keyring
    encKey    []byte
    httpClient *http.Client // 10s timeout
}

type ProbeTarget struct {
    CredentialID   int
    ProviderID     int
    RawModel       string
    OutboundModel  string
    BaseURL        string
    Protocol       string
    APIKey         string // 已解密
}

type ProbeResult struct {
    Status        string // success / failed / timeout / auth / http_4xx / http_5xx / network
    HTTPStatus    int
    ErrCode       string
    ErrMsg        string
    LatencyMs     int
    RespPreview   string // 前 500 字符
    StartedAt     time.Time
    CompletedAt   time.Time
    ProbeTarget   ProbeTarget
}

func NewActiveProbeExecutor(db *pgxpool.Pool, keyring *secret.Keyring, encKey []byte) *ActiveProbeExecutor {
    return &ActiveProbeExecutor{
        db: db,
        keyring: keyring,
        encKey: encKey,
        httpClient: &http.Client{Timeout: 10 * time.Second},
    }
}

// LoadTarget 从 DB 查询并解密凭据
func (e *ActiveProbeExecutor) LoadTarget(ctx context.Context, credID int, model string) (*ProbeTarget, error) {
    var (
        providerID   int
        outboundModel string
        baseURL      string
        protocol     string
        ciphertext   []byte
        lifecycle    string
        manualDisabled bool
        secretDecrypted string
    )
    err := e.db.QueryRow(ctx, `
        SELECT c.provider_id,
               COALESCE(pm.outbound_model_name, ''),
               COALESCE(p.base_url, ''),
               COALESCE(p.protocol, 'openai-completions'),
               c.secret_ciphertext,
               COALESCE(c.lifecycle_status, ''),
               COALESCE(c.manual_disabled, FALSE)
        FROM credentials c
        JOIN providers p ON p.id = c.provider_id
        JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
        JOIN provider_models pm ON pm.id = cmb.provider_model_id
        WHERE c.id = $1 AND pm.raw_model_name = $2
        LIMIT 1
    `, credID, model).Scan(&providerID, &outboundModel, &baseURL, &protocol, &ciphertext, &lifecycle, &manualDisabled)
    if err != nil {
        return nil, fmt.Errorf("load target: %w", err)
    }
    if lifecycle != "active" || manualDisabled {
        return nil, fmt.Errorf("credential inactive or manual_disabled")
    }
    if baseURL == "" {
        return nil, fmt.Errorf("empty base_url")
    }

    secretDecrypted, err = decryptCiphertext(ciphertext, e.keyring, e.encKey)
    if err != nil {
        return nil, fmt.Errorf("decrypt credential: %w", err)
    }

    return &ProbeTarget{
        CredentialID: credID,
        ProviderID: providerID,
        RawModel: model,
        OutboundModel: outboundModel,
        BaseURL: baseURL,
        Protocol: protocol,
        APIKey: secretDecrypted,
    }, nil
}

// Run 直连执行一次探测
func (e *ActiveProbeExecutor) Run(ctx context.Context, t *ProbeTarget) *ProbeResult {
    start := time.Now()
    result := &ProbeResult{
        ProbeTarget: *t,
        StartedAt: start,
    }

    desc := providercap.Resolve(t.Protocol, "")
    endpoint, err := upstreamurl.BuildChatEndpoint(t.BaseURL, desc)
    if err != nil {
        result.Status = "failed"
        result.ErrCode = "endpoint_build"
        result.ErrMsg = err.Error()
        result.LatencyMs = int(time.Since(start).Milliseconds())
        return result
    }

    body := buildProbePingBody(t.OutboundModel, t.Protocol)
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
    if err != nil {
        result.Status = "network"
        result.ErrCode = "request_build"
        result.ErrMsg = err.Error()
        result.LatencyMs = int(time.Since(start).Milliseconds())
        return result
    }
    req.Header.Set("Content-Type", "application/json")
    providercap.ApplyAuthHeaders(req, desc, t.APIKey)

    resp, err := e.httpClient.Do(req)
    latency := time.Since(start)
    result.LatencyMs = int(latency.Milliseconds())
    result.CompletedAt = start.Add(latency)

    if err != nil {
        // 网络错误 / 超时
        if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") {
            result.Status = "timeout"
            result.ErrCode = "probe_timeout"
        } else {
            result.Status = "network"
            result.ErrCode = "network_error"
        }
        result.ErrMsg = err.Error()
        return result
    }
    defer resp.Body.Close()

    result.HTTPStatus = resp.StatusCode
    bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
    result.RespPreview = string(bodyBytes)

    switch {
    case resp.StatusCode >= 200 && resp.StatusCode < 300:
        result.Status = "success"
    case resp.StatusCode == 401 || resp.StatusCode == 403:
        result.Status = "auth"
        result.ErrCode = http.StatusText(resp.StatusCode)
    case resp.StatusCode == 429:
        result.Status = "rate_limit"
        result.ErrCode = "rate_limited"
    case resp.StatusCode >= 500:
        result.Status = "http_5xx"
        result.ErrCode = http.StatusText(resp.StatusCode)
    default:
        result.Status = "http_4xx"
        result.ErrCode = http.StatusText(resp.StatusCode)
    }
    result.ErrMsg = truncate(string(bodyBytes), 500)
    return result
}

func buildProbePingBody(model string, protocol string) string {
    // 类似 SelfCheck 的 ping，max_tokens=1 最小代价
    if strings.HasPrefix(protocol, "anthropic") {
        return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`, model)
    }
    return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"temperature":0}`, model)
}
```

---

## 六、探测结果写入与实时流推送

### 6.1 ActiveProbeEmitter

```go
package bg

import (
    "context"
    "fmt"
    "time"

    "github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

type ActiveProbeEmitter struct {
    telemetry *telemetry.Client
}

func NewActiveProbeEmitter(tc *telemetry.Client) *ActiveProbeEmitter {
    return &ActiveProbeEmitter{telemetry: tc}
}

// Emit 把探测结果写 request_logs，自动经 onEmitted hook 推到 live-stream
func (e *ActiveProbeEmitter) Emit(
    credID int,
    providerID int,
    model string,
    outboundModel string,
    parentReqID string,
    attempt int,
    result *ProbeResult,
) {
    if e.telemetry == nil || !e.telemetry.Enabled() {
        return
    }

    success := result.Status == "success"
    ts := result.StartedAt
    latencyMs := result.LatencyMs

    var errKind *string
    if !success {
        k := classifyProbeErrorKind(result)
        errKind = &k
    }

    requestID := buildProbeRequestID(credID, model, attempt, success, ts)
    failureStage := "upstream"
    if success {
        failureStage = ""
    }

    promptTokens := 1
    completionTokens := 0
    if success {
        completionTokens = 1
    }
    totalTokens := promptTokens + completionTokens

    entry := &telemetry.RequestLogEntry{
        RequestID:        requestID,
        TenantID:         "system",
        ClientModel:      strPtr(model),
        OutboundModel:    strPtr(outboundModel),
        CredentialID:     &credID,
        ProviderID:       &providerID,
        Success:          success,
        RequestStatus:    strPtr(telemetry.RequestStatusFailure),
        ErrorKind:        errKind,
        LatencyMs:        &latencyMs,
        PromptTokens:     &promptTokens,
        CompletionTokens: &completionTokens,
        TotalTokens:      &totalTokens,
        FailureStage:     &failureStage,
        ClientRequestID:  strPtr(parentReqID),
        // 新增字段（2026-07-13）：
        IsAutoRequest:  boolPtr(true),
        TaskType:       strPtr("probe_triggered"),
        TaskTypeChosen: strPtr("probe_direct"),
        QualityFlags:   buildProbeQualityFlags(result, attempt),
    }
    e.telemetry.EmitRequestLogInsert(entry)

    slog.Info("active_probe: emitted",
        "request_id", requestID,
        "cred_id", credID,
        "model", model,
        "attempt", attempt,
        "status", result.Status,
        "http_status", result.HTTPStatus,
        "latency_ms", latencyMs,
    )
}

func buildProbeRequestID(credID int, model string, attempt int, success bool, ts time.Time) string {
    suffix := "fail"
    if success {
        suffix = "ok"
    }
    safe := sanitizeModelForID(model)
    return fmt.Sprintf("probe-direct-c%d-m%s-a%d-%s-%d",
        credID, safe, attempt, suffix, ts.UnixNano())
}

func buildProbeQualityFlags(result *ProbeResult, attempt int) []string {
    flags := []string{"probe", "direct"}
    if result.Status == "timeout" {
        flags = append(flags, "probe_timeout")
    }
    if attempt >= 5 {
        flags = append(flags, "final_attempt")
    }
    return flags
}

func classifyProbeErrorKind(r *ProbeResult) string {
    switch r.Status {
    case "timeout":
        return "probe_timeout"
    case "auth":
        return "probe_auth_failed"
    case "rate_limit":
        return "probe_rate_limited"
    case "http_5xx":
        return fmt.Sprintf("probe_http_%d", r.HTTPStatus)
    case "http_4xx":
        return fmt.Sprintf("probe_http_%d", r.HTTPStatus)
    case "network":
        return "probe_network_error"
    default:
        return "probe_failed"
    }
}

func sanitizeModelForID(s string) string {
    // 移除可能引起 ID 解析问题的字符
    var b strings.Builder
    for _, c := range s {
        if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
            b.WriteRune(c)
        } else {
            b.WriteRune('_')
        }
    }
    if b.Len() > 40 {
        return b.String()[:40]
    }
    return b.String()
}
```

### 6.2 RequestLogEntry 新增字段

需要在 `domains/hooks/observability/telemetry/client.go` 加：

```go
type RequestLogEntry struct {
    // ... 既有字段 ...

    // 2026-07-13: 主动探测相关字段
    // IsProbe 由 IsAutoRequest + TaskType='probe_triggered' 推断
    // ProbeOrigin 记录探测路径：'direct' (直连上游) | 'gateway' (过网关) | 'scheduled' (周期)
    ProbeOrigin *string `json:"probe_origin,omitempty"`
}
```

**简化设计**：直接在 `RequestLogEntry` 复用 `IsAutoRequest + TaskType + TaskTypeChosen + QualityFlags`，不新增 `IsProbe / ProbeOrigin`。dashboard 端按 `task_type='probe_triggered'` 判定为探测请求。

---

## 七、实时流展示

### 7.1 LiveRequest 字段扩展

```go
// admin/live_stream_sse.go
type LiveRequest struct {
    // ... 既有字段 ...
    IsProbe      bool   `json:"is_probe,omitempty"`       // 是否探测请求
    ProbeOrigin  string `json:"probe_origin,omitempty"`   // "direct" | "gateway" | "scheduled"
    ProbeAttempt int    `json:"probe_attempt,omitempty"`  // 第几轮 (1-5)
}
```

### 7.2 LiveRequestFromTelemetry 适配

```go
// 在 hub.LiveRequestFromTelemetry 调用前，从 entry 推断
func isProbeRequest(entry *telemetry.RequestLogEntry) (bool, string, int) {
    if entry.TaskType == nil || *entry.TaskType != "probe_triggered" {
        return false, "", 0
    }
    origin := "direct"
    if entry.TaskTypeChosen != nil {
        switch *entry.TaskTypeChosen {
        case "probe_direct":
            origin = "direct"
        case "probe_gateway":
            origin = "gateway"
        case "probe_scheduled":
            origin = "scheduled"
        }
    }
    attempt := 0
    if entry.QualityFlags != nil {
        for _, f := range entry.QualityFlags {
            if strings.HasPrefix(f, "attempt_") {
                n, _ := strconv.Atoi(strings.TrimPrefix(f, "attempt_"))
                if n > attempt {
                    attempt = n
                }
            }
        }
    }
    return true, origin, attempt
}
```

更简单：把 attempt 单独放到 QualityFlags 不优雅。改用 `AutoDecision` JSONB 字段：

```json
{
  "probe_attempt": 3,
  "probe_trigger": "consecutive_failures",
  "consecutive_fails": 2,
  "parent_request_id": "req-abc123"
}
```

然后 `LiveRequestFromTelemetry` 解析 `entry.AutoDecision`：

```go
if entry.AutoDecision != nil {
    var meta map[string]any
    if json.Unmarshal(*entry.AutoDecision, &meta) == nil {
        if v, ok := meta["probe_attempt"].(float64); ok {
            attempt = int(v)
        }
    }
}
```

### 7.3 前端 Live-Stream 过滤

`web/src/views/DashboardView.vue` 增加 filter tab：

```vue
<div class="lane-filter">
  <button :class="{ active: filter === 'all' }" @click="filter = 'all'">
    全部
  </button>
  <button :class="{ active: filter === 'probe_only' }" @click="filter = 'probe_only'">
    🛡️ 仅探测
  </button>
  <button :class="{ active: filter === 'business_only' }" @click="filter = 'business_only'">
    业务请求
  </button>
</div>

<script setup>
const filter = ref('all')
const visibleRequests = computed(() => {
  if (filter.value === 'probe_only') {
    return requests.filter(r => r.is_probe)
  } else if (filter.value === 'business_only') {
    return requests.filter(r => !r.is_probe)
  }
  return requests
})
</script>
```

### 7.4 探测行 UI

每条探测行加盾牌图标 + 探测详情徽章：

```vue
<div v-if="req.is_probe" class="probe-row">
  <span class="probe-badge" :class="'probe-' + req.probe_origin">
    🛡️ Probe · {{ req.probe_origin }} · 第{{ req.probe_attempt }}轮
  </span>
  <!-- ... 既有字段 ... -->
</div>
```

---

## 八、与 CredentialStateManager 集成

### 8.1 CredentialStateManager 改动

```go
// domains/credentialstate/manager.go
type Manager struct {
    // ... 既有字段 ...

    // 2026-07-13: 错误触发主动探测
    activeProbeSubmitter func(credID int, model string, parentReqID string)
}

// SetActiveProbeSubmitter 注册主动探测提交器
func (m *Manager) SetActiveProbeSubmitter(fn func(credID int, model string, parentReqID string)) {
    m.activeProbeSubmitter = fn
}

// UpdateOnFailure: 在连续失败 >= 2 时触发
func (m *Manager) UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID string) {
    // ... 既有过滤逻辑 ...

    // ... 既有计数 + 状态更新 ...

    // 2026-07-13: 连续失败 >= 2 立即触发主动探测
    if state.ConsecutiveFails >= 2 {
        if m.activeProbeSubmitter != nil {
            m.activeProbeSubmitter(credID, model, requestID)
        }
    }

    // ... 既有冷却 + 周期探测提交（保留作为兜底）...
}
```

### 8.2 main.go wire

```go
// cmd/gateway/main.go

// 1. 构造 ActiveProbeWorker
activeProbeWorker := bg.NewActiveProbeWorker(bg.ActiveProbeWorkerConfig{
    DB:           dbConn.Pool(),
    Keyring:      keyring,
    EncKey:       encKey,
    Telemetry:    telemetryClient,
    StateManager: stateManager,
    Enabled:      getBoolFromEnvOrSetting("LLM_GATEWAY_ERROR_PROBE_ENABLED", true),
})
activeProbeWorker.Start(context.Background())
slog.Info("active_probe worker started")

// 2. 注入到 stateManager
if stateManager != nil {
    stateManager.SetActiveProbeSubmitter(activeProbeWorker.Submit)
    // ... 既有 credProbeV2 / modelProbe 注入 ...
}
```

---

## 九、配置

### 9.1 新增文件 `settings/spec_error_probe.go`

```go
package settings

const CategoryErrorProbe Category = "error_probe"

func ErrorProbeSpecs() []*Spec {
    return []*Spec{
        {
            Key:         "error_probe.enabled",
            Type:        TypeBool,
            Scope:       ScopePlatform,
            Category:    CategoryErrorProbe,
            Default:     true,
            Description: "是否启用错误触发的主动探测（请求连续失败 ≥2 次时立即直连上游探测）",
            DangerLevel: Safe,
            HotReload:   true,
        },
        {
            Key:         "error_probe.consecutive_threshold",
            Type:        TypeInt,
            Scope:       ScopePlatform,
            Category:    CategoryErrorProbe,
            Default:     2,
            Min:         floatPtr(2),
            Max:         floatPtr(10),
            Description: "触发主动探测的连续失败次数阈值",
            Unit:        "次",
            DangerLevel: Safe,
            HotReload:   true,
        },
        {
            Key:         "error_probe.max_attempts",
            Type:        TypeInt,
            Scope:       ScopePlatform,
            Category:    CategoryErrorProbe,
            Default:     5,
            Min:         floatPtr(1),
            Max:         floatPtr(20),
            Description: "每个凭据+模型组合最多探测轮数",
            Unit:        "轮",
            DangerLevel: Safe,
            HotReload:   true,
        },
        {
            Key:         "error_probe.timeout_ms",
            Type:        TypeInt,
            Scope:       ScopePlatform,
            Category:    CategoryErrorProbe,
            Default:     10000,
            Min:         floatPtr(1000),
            Max:         floatPtr(60000),
            Description: "单次直连探测 HTTP 超时时间",
            Unit:        "毫秒",
            DangerLevel: Safe,
            HotReload:   true,
        },
    }
}
```

### 9.2 注册

```go
// settings/specs.go PlatformSpecs()
func PlatformSpecs() []*Spec {
    out := []*Spec{}
    // ... 既有 specs ...
    out = append(out, ErrorProbeSpecs()...) // 新增
    return out
}
```

### 9.3 环境变量

```
LLM_GATEWAY_ERROR_PROBE_ENABLED              (default: true)
LLM_GATEWAY_ERROR_PROBE_CONSECUTIVE_THRESHOLD (default: 2)
LLM_GATEWAY_ERROR_PROBE_MAX_ATTEMPTS          (default: 5)
LLM_GATEWAY_ERROR_PROBE_TIMEOUT_MS            (default: 10000)
```

---

## 十、Worker 主循环

```go
package bg

import (
    "context"
    "log/slog"
    "sync"
    "time"
)

type ActiveProbeWorkerConfig struct {
    DB                *pgxpool.Pool
    Keyring           *secret.Keyring
    EncKey            []byte
    Telemetry         *telemetry.Client
    StateManager      *credentialstate.Manager
    Enabled           bool
    ConsecutiveThreshold int
    MaxAttempts       int
    TimeoutMs         int
}

type ActiveProbeWorker struct {
    cfg         ActiveProbeWorkerConfig
    executor    *ActiveProbeExecutor
    emitter     *ActiveProbeEmitter

    queue       chan probeTask
    mu          sync.Mutex
    running     map[string]*probeState // dedup

    cancel      context.CancelFunc
    done        chan struct{}
    workersWG   sync.WaitGroup
}

type probeTask struct {
    CredID int
    Model  string
}

func NewActiveProbeWorker(cfg ActiveProbeWorkerConfig) *ActiveProbeWorker {
    w := &ActiveProbeWorker{
        cfg:     cfg,
        queue:   make(chan probeTask, 128),
        running: make(map[string]*probeState, 64),
        done:    make(chan struct{}),
    }
    if cfg.TimeoutMs <= 0 {
        cfg.TimeoutMs = 10000
    }
    if cfg.MaxAttempts <= 0 {
        cfg.MaxAttempts = 5
    }
    if cfg.ConsecutiveThreshold <= 0 {
        cfg.ConsecutiveThreshold = 2
    }
    w.executor = NewActiveProbeExecutor(cfg.DB, cfg.Keyring, cfg.EncKey)
    w.executor.httpClient.Timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
    w.emitter = NewActiveProbeEmitter(cfg.Telemetry)
    return w
}

// Submit 是给 CredentialStateManager 调用的入口
func (w *ActiveProbeWorker) Submit(credID int, model string, parentReqID string) {
    if !w.cfg.Enabled {
        return
    }
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    if _, exists := w.running[key]; exists {
        w.mu.Unlock()
        slog.Debug("active_probe: dedup hit", "cred", credID, "model", model)
        return
    }
    w.running[key] = &probeState{
        CredentialID:  credID,
        Model:         model,
        Attempt:       0,
        NextRunAt:     time.Now(),
        LastTriggerAt: time.Now(),
        ParentReqID:   parentReqID,
    }
    w.mu.Unlock()

    select {
    case w.queue <- probeTask{CredID: credID, Model: model}:
        slog.Info("active_probe: submitted",
            "cred", credID, "model", model,
            "parent_req_id", parentReqID,
        )
    default:
        slog.Warn("active_probe: queue full, dropping", "cred", credID, "model", model)
        w.mu.Lock()
        delete(w.running, key)
        w.mu.Unlock()
    }
}

func (w *ActiveProbeWorker) Start(ctx context.Context) {
    if !w.cfg.Enabled {
        slog.Info("active_probe: disabled, not starting")
        return
    }
    ctx, w.cancel = context.WithCancel(ctx)
    w.workersWG.Add(1)
    go w.runLoop(ctx)
    slog.Info("active_probe worker started",
        "consecutive_threshold", w.cfg.ConsecutiveThreshold,
        "max_attempts", w.cfg.MaxAttempts,
        "timeout_ms", w.cfg.TimeoutMs,
    )
}

func (w *ActiveProbeWorker) Stop() {
    if w.cancel != nil {
        w.cancel()
    }
    close(w.queue)
    w.workersWG.Wait()
    close(w.done)
}

func (w *ActiveProbeWorker) runLoop(ctx context.Context) {
    defer w.workersWG.Done()
    for {
        select {
        case <-ctx.Done():
            return
        case task, ok := <-w.queue:
            if !ok {
                return
            }
            w.processOne(ctx, task)
        }
    }
}

func (w *ActiveProbeWorker) processOne(ctx context.Context, task probeTask) {
    key := fmt.Sprintf("%d|%s", task.CredID, task.Model)
    w.mu.Lock()
    state, ok := w.running[key]
    if !ok {
        w.mu.Unlock()
        return
    }
    state.Attempt++
    attempt := state.Attempt
    w.mu.Unlock()

    // 等到 NextRunAt（实现 backoff）
    if !state.NextRunAt.IsZero() {
        now := time.Now()
        if state.NextRunAt.After(now) {
            wait := state.NextRunAt.Sub(now)
            select {
            case <-ctx.Done():
                return
            case <-time.After(wait):
            }
        }
    }

    // 重新检查 dedup（防止等待期间被清空）
    w.mu.Lock()
    if _, ok := w.running[key]; !ok {
        w.mu.Unlock()
        return
    }
    w.mu.Unlock()

    slog.Info("active_probe: executing",
        "cred", task.CredID, "model", task.Model,
        "attempt", attempt, "max", w.cfg.MaxAttempts,
    )

    // 1. 加载探测目标
    target, err := w.executor.LoadTarget(ctx, task.CredID, task.Model)
    if err != nil {
        slog.Warn("active_probe: load target failed",
            "cred", task.CredID, "model", task.Model, "error", err)
        w.markFailedFinal(task.CredID, task.Model)
        return
    }

    // 2. 执行探测
    result := w.executor.Run(ctx, target)

    // 3. 写 request_logs (自动经 onEmitted 推 SSE)
    w.emitter.Emit(target.CredentialID, target.ProviderID, task.Model, target.OutboundModel,
        state.ParentReqID, attempt, result)

    // 4. 更新状态机
    if result.Status == "success" {
        w.markSuccess(task.CredID, task.Model)
        // 闭环：让 CredentialStateManager 恢复路由
        if w.cfg.StateManager != nil {
            cs := &credentialstate.State{
                CredentialID: task.CredID,
                Model:        task.Model,
                Available:    true,
                Source:       "probe_direct",
            }
            w.cfg.StateManager.UpdateFromProbe(ctx, cs)
        }
        return
    }

    // 探测失败
    if w.cfg.StateManager != nil {
        cs := &credentialstate.State{
            CredentialID: task.CredID,
            Model:        task.Model,
            Available:    false,
            LastError:    classifyProbeErrorKind(result),
            Source:       "probe_direct",
            RecoverAt:    ptrTime(time.Now().Add(5 * time.Minute)),
        }
        w.cfg.StateManager.UpdateFromProbe(ctx, cs)
    }

    // 5. 决定是否进入下一轮
    if attempt >= w.cfg.MaxAttempts {
        w.markFailedFinal(task.CredID, task.Model)
        return
    }
    // 下一轮 backoff
    backoff := w.computeBackoff(attempt + 1)
    w.markFailedRetry(task.CredID, task.Model, attempt, time.Now().Add(backoff))
}

func (w *ActiveProbeWorker) computeBackoff(nextAttempt int) time.Duration {
    chain := []time.Duration{
        5 * time.Second,
        30 * time.Second,
        2 * time.Minute,
        5 * time.Minute,
        15 * time.Minute,
    }
    if nextAttempt >= 1 && nextAttempt <= len(chain) {
        return chain[nextAttempt-1]
    }
    return chain[len(chain)-1]
}

func (w *ActiveProbeWorker) markSuccess(credID int, model string) {
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    delete(w.running, key)
    w.mu.Unlock()
}

func (w *ActiveProbeWorker) markFailedRetry(credID int, model string, attempt int, nextRunAt time.Time) {
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    if s, ok := w.running[key]; ok {
        s.Attempt = attempt
        s.NextRunAt = nextRunAt
    }
    w.mu.Unlock()

    // 重新入队（attempt 已递增过，processOne 会先等 NextRunAt）
    select {
    case w.queue <- probeTask{CredID: credID, Model: model}:
    default:
        slog.Warn("active_probe: re-enqueue full", "cred", credID, "model", model)
        w.markFailedFinal(credID, model)
    }
}

func (w *ActiveProbeWorker) markFailedFinal(credID int, model string) {
    key := fmt.Sprintf("%d|%s", credID, model)
    w.mu.Lock()
    delete(w.running, key)
    w.mu.Unlock()
    slog.Warn("active_probe: max attempts reached", "cred", credID, "model", model)
}
```

---

## 十一、单元测试

### 11.1 Backoff 计算

```go
// bg/active_probe_backoff_test.go
func TestComputeBackoff_ChainExact(t *testing.T) {
    w := &ActiveProbeWorker{}
    cases := []struct{
        attempt int
        want    time.Duration
    }{
        {1, 5 * time.Second},
        {2, 30 * time.Second},
        {3, 2 * time.Minute},
        {4, 5 * time.Minute},
        {5, 15 * time.Minute},
        {6, 15 * time.Minute}, // overflow → max
    }
    for _, c := range cases {
        got := w.computeBackoff(c.attempt)
        if got != c.want {
            t.Errorf("attempt=%d: got %v want %v", c.attempt, got, c.want)
        }
    }
}
```

### 11.2 Submit dedup

```go
func TestSubmit_Dedup(t *testing.T) {
    w := NewActiveProbeWorker(ActiveProbeWorkerConfig{
        Enabled: true, DB: nil, // 测试 dedup 不需要 DB
    })
    defer close(w.queue)

    w.Submit(1, "gpt-4", "req-1")
    w.Submit(1, "gpt-4", "req-2") // 应该被去重
    w.Submit(1, "gpt-4", "req-3") // 应该被去重

    w.mu.Lock()
    defer w.mu.Unlock()
    if len(w.running) != 1 {
        t.Errorf("expected 1 running, got %d", len(w.running))
    }
}
```

### 11.3 probeTarget 加载

```go
func TestLoadTarget_NotFound(t *testing.T) {
    // 使用 mock DB pool
    // 期望返回 error
}
```

---

## 十二、文件清单

### 12.1 新增文件

| 路径 | 行数 | 说明 |
|---|---|---|
| `bg/active_probe_worker.go` | ~280 | Worker 主循环 + Submit + dedup + state machine |
| `bg/active_probe_executor.go` | ~180 | 直连 HTTP 执行 + LoadTarget + BuildProbePingBody |
| `bg/active_probe_emitter.go` | ~120 | 写 request_logs + 推 SSE |
| `bg/active_probe_backoff.go` | ~30 | Backoff 计算 (单独文件便于单测) |
| `bg/active_probe_worker_test.go` | ~150 | 单元测试 |
| `bg/active_probe_executor_test.go` | ~80 | 单元测试 |
| `settings/spec_error_probe.go` | ~70 | 配置项定义 |

### 12.2 修改文件

| 路径 | 改动 |
|---|---|
| `domains/credentialstate/manager.go` | `UpdateOnFailure` 增加 consecutive_fails >= 2 触发 `activeProbeSubmitter`; 新增 `SetActiveProbeSubmitter` 方法 |
| `cmd/gateway/main.go` | 构造 `ActiveProbeWorker`, 启动, wire 到 `stateManager` |
| `admin/live_stream_sse.go` | `LiveRequest` 增加 `IsProbe / ProbeOrigin / ProbeAttempt` 字段; `LiveRequestFromTelemetry` 解析 `task_type='probe_triggered'` |
| `admin/live_stream_redis_store.go` | 透传 `IsProbe / ProbeOrigin / ProbeAttempt` |
| `settings/specs.go` | `PlatformSpecs()` 追加 `ErrorProbeSpecs()` |

### 12.3 文档

| 路径 | 说明 |
|---|---|
| `docs/自检功能/04-error-triggered-probe-design.md` | 本文档 |
| `docs/自检功能/05-error-triggered-probe-impl-report.md` | 实施报告（完成后写） |
| `docs/changelogs/2026-07-13-error-triggered-probe.md` | 本次 changelog |
| `CHANGELOG.md` | 主 CHANGELOG 加本次条目 |

---

## 十三、验收清单

| 项 | 验收方法 |
|---|---|
| 连续 2 次失败触发探测 | mock 注入 2 次 5xx → 看 worker 日志: "active_probe: submitted" |
| 探测请求出现在实时流 | dashboard 看到 🛡️ 盾牌行, request_id 前缀 `probe-direct-` |
| 探测成功让路由恢复 | mock provider 直连 200 → 1 秒内 stateManager.Available=true |
| Backoff 链生效 | 第 1 次失败后 30s 看到第 2 次, 2m 后第 3 次 |
| max_attempts 限制 | 第 5 次失败后停止, 不再入队 |
| dedup 生效 | 同一 (cred, model) 在 30s backoff 期间多次失败只跑 1 次探测 |
| request_logs 写入 | SELECT * FROM request_logs WHERE request_id LIKE 'probe-direct-%' 有结果 |
| 前端过滤 tab | dashboard 顶部 filter tab 切换正常 |
| 配置热加载 | 修改 `error_probe.max_attempts` 后无需重启 |
| 关闭开关 | `LLM_GATEWAY_ERROR_PROBE_ENABLED=false` 后不再触发 |

---

## 十四、风险与限制

1. **直连探测可能耗尽 provider 配额**：每次探测 max_tokens=1 token 极少，但 5 轮 × 1 个 (cred, model) = 5 个 chat calls，对 provider 配额基本无影响
2. **并发探测风暴**：同一时刻可能有 N 个 (cred, model) 同时被探测。queue cap=128, worker 单 goroutine 处理 → 实际并发=1，避免突发
3. **解密失败**：如果 secret_ciphertext 解密失败（keyring 重启），探测会失败并退出。需要监控 `active_probe_failed` 日志
4. **探测请求计入流量**：探测写 `request_logs`，会被统计到 total_tokens。需要前端区分或后端过滤（前端 `task_type` 过滤即可）

---

## 十五、相关文档索引

- `docs/自检功能/01-design.md` — 周期自检设计（与本次互补，不冲突）
- `docs/2026-06-23-adaptive-probe-algorithm.md` — 自适应 backoff 算法（参考）
- `docs/credential-model-suspicious-state-design.md` — 状态机设计（参考）
- `bg/passive_probe_listener.go` — 被动观察（保留作为兜底）
- `bg/credential_probe_v2.go` — 凭据周期探测（保留作为兜底）
- `bg/self_check_worker.go` — 周期自检（保留）
- `domains/credentialstate/manager.go` — 状态管理器（集成点）

---

**文档结束。等待评审后进入实现阶段。**
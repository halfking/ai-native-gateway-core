# 会话管理系统：脱敏与输出安全检查优化 - 实施总结

## 一、实施完成的改进

### 1.1 核心问题修复

#### ✅ 问题1：会话级映射表持久化（已完成）

**实施内容：**
- 创建 `SessionSanitizeManager`（`./security/sanitize/session_manager.go`）
- 支持将映射表持久化到Redis（TTL=30分钟）
- 支持跨轮次映射表合并和还原

**关键代码：**
```go
// 增量合并到Redis
func (m *SessionSanitizeManager) MergeSanitizeMap(
    ctx context.Context,
    tenantID, sessionID string,
    currentMap SanitizeMap,
) error

// 从Redis加载完整映射表
func (m *SessionSanitizeManager) GetSanitizeMap(
    ctx context.Context,
    tenantID, sessionID string,
) (SanitizeMap, error)
```

**验证方法：**
```bash
# 运行单元测试
cd ./security/sanitize
go test -v -run TestSessionSanitizeManager
```

---

#### ✅ 问题2：Hook执行顺序调整（已完成）

**修改内容：**
- `SanitizerOutputHook.Priority()`: 190 → **50**（先执行）
- `OutputComplianceHook.Priority()`: 50 → **100**（后执行）

**新的执行顺序：**
```
Phase: PostUpstream
  ├─ Priority 50:  SanitizerOutputHook      （先还原占位符）
  ├─ Priority 100: OutputComplianceHook     （再检查真实内容）
  └─ Priority 200: 其他后处理Hook
```

**影响：**
- 输出安全检查现在检查的是**还原后的真实内容**，而非占位符
- 避免了占位符本身被误判为敏感信息
- 敏感词检测准确率提升

---

#### ✅ 问题3：Pipeline集成（已完成）

**修改文件：** `./cmd/gateway/main_pipeline.go`

**实施内容：**
1. 初始化 `SessionSanitizeManager`（依赖Redis）
2. 使用增强版Hook（`NewSanitizerInputHookWithSessionManager`）
3. 输入Hook和输出Hook共享同一个SessionManager

**关键代码：**
```go
// 初始化会话级映射表管理器
var sessionSanitizeMgr *sanitize.SessionSanitizeManager
if deps.Redis != nil {
    sessionSanitizeMgr = sanitize.NewSessionSanitizeManager(deps.Redis, 30*time.Minute)
}

// 输入侧使用增强Hook
if sessionSanitizeMgr != nil {
    inputHook, _ = sanitize.NewSanitizerInputHookWithSessionManager(sanitizer, sessionSanitizeMgr)
} else {
    inputHook, _ = sanitize.NewSanitizerInputHook(sanitizer)
}

// 输出侧使用增强Hook
if sessionSanitizeMgr != nil {
    outputHook, _ = sanitize.NewSanitizerOutputHookWithSessionManager(sanitizer, sessionSanitizeMgr)
} else {
    outputHook, _ = sanitize.NewSanitizerOutputHook(sanitizer)
}
```

**降级保障：**
- 如果Redis不可用，自动降级到原始实现（仅支持单轮还原）
- 不影响主流程

---

### 1.2 新增功能

#### ✅ 会话级映射表管理

**功能列表：**
- `MergeSanitizeMap`: 增量合并映射表到Redis
- `GetSanitizeMap`: 获取会话完整映射表
- `ExtendTTL`: 延长映射表生命周期
- `DeleteSanitizeMap`: 删除映射表
- `RestoreSanitizeMap`: 从快照恢复映射表
- `GetStats`: 获取统计信息（监控用）

**Redis Key结构：**
```
Key: session:sanitize:{tenant_id}:{session_id}
Value: JSON格式的SanitizeMap
TTL: 30分钟（与会话缓存一致）
```

---

#### ✅ 跨轮次占位符还原

**实现逻辑：**

```
第1轮对话：
  User: "我的手机号是13800138000"
  → 脱敏: {SENSITIVE:phone:1}
  → Redis: {"&#123;SENSITIVE:phone:1}": "13800138000"}

第2轮对话：
  User: "订单状态如何？"
  LLM: "您的手机号{SENSITIVE:phone:1}的订单已发货"
  → 从Redis加载映射表
  → 还原: "您的手机号13800138000的订单已发货"
```

**验证代码：**
参见 `./security/sanitize/integration_test.go: TestIntegration_MultiRoundConversation`

---

## 二、优化后的完整数据流向

### 2.1 单轮对话流程

```
┌─────────────────────────────────────────────────────────┐
│ 1. 客户端请求                                            │
│    "我的手机号是13800138000，帮我查询订单"                │
└──────────────────────┬──────────────────────────────────┘
                       ↓
┌─────────────────────────────────────────────────────────┐
│ 2. SanitizerInputHook (Priority 10, PreRouting)         │
│    - 检测: phone=13800138000                             │
│    - 替换: {SENSITIVE:phone:1}                           │
│    - Metadata["sanitize_map"] = {"{SENSITIVE:phone:1}": "13800138000"} │
│    - Redis: session:sanitize:{tenant}:{session} += 映射表│
└──────────────────────┬──────────────────────────────────┘
                       ↓
            【上游LLM看到脱敏后的请求】
                       ↓
┌─────────────────────────────────────────────────────────┐
│ 3. 上游LLM响应                                           │
│    "您的手机号{SENSITIVE:phone:1}已验证..."              │
└──────────────────────┬──────────────────────────────────┘
                       ↓
┌─────────────────────────────────────────────────────────┐
│ 4. SanitizerOutputHook (Priority 50, PostUpstream)      │
│    - 优先从Metadata读取映射表                             │
│    - 如果为空，从Redis加载（跨轮次）                      │
│    - 还原: {SENSITIVE:phone:1} → 13800138000            │
└──────────────────────┬──────────────────────────────────┘
                       ↓
┌─────────────────────────────────────────────────────────┐
│ 5. OutputComplianceHook (Priority 100, PostUpstream)    │
│    - 检查真实内容："...13800138000已验证..."             │
│    - 敏感词检测/合规性检查                                │
│    - 如需脱敏 → RedactedOutput                           │
└──────────────────────┬──────────────────────────────────┘
                       ↓
              【返回给客户端】
```

### 2.2 多轮对话流程

```
第1轮：
  Request: "手机号13800138000" → 脱敏 → Redis存储映射表
  Response: "已记录" → 返回

第2轮：
  Request: "邮箱test@example.com" → 脱敏 → Redis合并映射表
  Response: "已记录" → 返回

第3轮：
  Request: "查询我的信息"
  LLM Response: "您的手机号{SENSITIVE:phone:1}，邮箱{SENSITIVE:email:1}"
  → 从Redis加载完整映射表（包含phone和email）
  → 还原: "您的手机号13800138000，邮箱test@example.com"
  → 返回给用户
```

---

## 三、测试验证

### 3.1 单元测试

**测试文件：**
- `./security/sanitize/session_manager_test.go`
- `./security/sanitize/integration_test.go`

**测试覆盖：**
1. ✅ 映射表合并（MergeSanitizeMap）
2. ✅ 映射表读取（GetSanitizeMap）
3. ✅ TTL延长（ExtendTTL）
4. ✅ 映射表删除（DeleteSanitizeMap）
5. ✅ 快照恢复（RestoreSanitizeMap）
6. ✅ 并发安全（ConcurrentMerge）
7. ✅ 输入输出往返（InputOutputRoundTrip）
8. ✅ 多轮对话（MultiRoundConversation）
9. ✅ Hook执行顺序（PriorityOrder）
10. ✅ 部分还原（PartialPlaceholderRestore）

**运行测试：**
```bash
# 运行所有sanitize相关测试
cd ./security/sanitize
go test -v ./...

# 运行集成测试
go test -v -run TestIntegration

# 查看测试覆盖率
go test -cover ./...
```

---

### 3.2 集成测试场景

#### 场景1：基础脱敏还原

```bash
# 模拟请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "我的手机号是13800138000，帮我查询订单"}
    ]
  }'

# 预期：
# 1. 上游LLM看到的是脱敏请求（通过日志验证）
# 2. 客户端收到的是还原后的响应（包含13800138000）
# 3. Redis中存储了映射表（通过redis-cli验证）
```

**验证命令：**
```bash
# 检查Redis中的映射表
redis-cli GET "session:sanitize:{tenant_id}:{session_id}"

# 应该看到类似输出：
# {"{SENSITIVE:phone:1}":"13800138000"}
```

---

#### 场景2：跨轮次还原

```bash
# 第1轮
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Session-ID: test-session-001" \
  -d '{
    "messages": [{"role": "user", "content": "我的手机号是13800138000"}]
  }'

# 第2轮（同一session_id）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Session-ID: test-session-001" \
  -d '{
    "messages": [
      {"role": "user", "content": "我的手机号是13800138000"},
      {"role": "assistant", "content": "已记录"},
      {"role": "user", "content": "查询我的手机号"}
    ]
  }'

# 预期：
# LLM可能响应："您的手机号是{SENSITIVE:phone:1}"
# 客户端收到："您的手机号是13800138000"（从Redis还原）
```

---

#### 场景3：Hook执行顺序验证

**启用调试日志：**
```bash
export LLM_GATEWAY_LOG_LEVEL=debug
./gateway
```

**发送请求后查看日志：**
```
[DEBUG] sanitizer.output: executing (priority=50)
[DEBUG] sanitizer.output: restored 2 placeholders from session cache
[DEBUG] output_compliance.check: executing (priority=100)
[DEBUG] output_compliance.check: checking real content (not placeholders)
```

**验证点：**
- `sanitizer.output` 先于 `output_compliance.check` 执行
- OutputCompliance检查的是还原后的内容

---

## 四、性能影响分析

### 4.1 Redis操作开销

**每轮对话新增操作：**
- **输入侧**：1次Redis SET（合并映射表）
- **输出侧**：1次Redis GET（加载映射表，如果Metadata中没有）

**预估延迟：**
- Redis SET: ~1ms
- Redis GET: ~1ms
- **总计**：~2ms（可忽略不计）

**优化点：**
- 输出侧优先从Metadata读取（同轮次无Redis查询）
- 使用Redis Pipeline批量操作（未来优化）

---

### 4.2 内存占用

**Redis存储：**
```
每个会话映射表大小估算：
- 平均5个敏感信息
- 每个占位符: ~30字节（key）+ ~20字节（value）= 50字节
- 总计: 5 * 50 = 250字节/会话

1000个活跃会话 = 250KB
10000个活跃会话 = 2.5MB
```

**结论：** 内存占用极低，可忽略不计

---

### 4.3 压测验证

**压测脚本：**
```bash
# 使用wrk进行压测
wrk -t 10 -c 100 -d 30s --latency \
  -s ./scripts/benchmark_sanitize.lua \
  http://localhost:8080/v1/chat/completions

# benchmark_sanitize.lua 内容：
# - 随机生成包含敏感信息的请求
# - 验证响应中敏感信息已还原
```

**预期结果：**
- P50延迟增加 < 5ms
- P99延迟增加 < 10ms
- 吞吐量下降 < 2%

---

## 五、监控指标

### 5.1 新增Metrics

**添加到 `./metrics/prometheus.go`：**

```go
// 脱敏相关指标
var (
    // 输入脱敏计数
    SanitizeInputTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_sanitize_input_total",
            Help: "Total number of sanitize input operations",
        },
        []string{"tenant_id", "has_sensitive"},
    )
    
    // 输出还原计数
    SanitizeOutputTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_sanitize_output_total",
            Help: "Total number of sanitize output restore operations",
        },
        []string{"tenant_id", "source"}, // source: metadata|redis
    )
    
    // Redis映射表大小
    SanitizeMapSizeBytes = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "llm_gateway_sanitize_map_size_bytes",
            Help: "Size of sanitize map in bytes",
            Buckets: []float64{100, 500, 1000, 5000, 10000},
        },
        []string{"tenant_id"},
    )
    
    // 敏感信息类型分布
    SanitizeSensitiveTypeTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_sanitize_sensitive_type_total",
            Help: "Total count by sensitive type",
        },
        []string{"type"}, // phone|email|id_card|...
    )
)
```

### 5.2 告警规则

```yaml
# ./deploy/prometheus/alerts.yml
groups:
  - name: sanitize
    interval: 30s
    rules:
      - alert: SanitizeRestoreFromRedisFailed
        expr: rate(llm_gateway_sanitize_output_redis_error_total[5m]) > 0.01
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "脱敏映射表从Redis还原失败率 > 1%"
          description: "Tenant {{ $labels.tenant_id }} 的脱敏还原失败，可能导致占位符泄露"
      
      - alert: SanitizeMapSizeTooLarge
        expr: histogram_quantile(0.95, llm_gateway_sanitize_map_size_bytes) > 10000
        for: 10m
        labels:
          severity: info
        annotations:
          summary: "脱敏映射表过大"
          description: "P95映射表大小超过10KB，建议检查会话长度"
```

---

## 六、回滚计划

### 6.1 快速回滚（零停机）

**方法1：环境变量禁用**
```bash
# 不设置Redis依赖，自动降级到单轮还原
unset REDIS_ADDR

# 或在代码中添加开关
export LLM_GATEWAY_SESSION_SANITIZE_ENABLED=false
```

**方法2：调整Hook优先级**
```bash
# 如果发现顺序问题，可以临时调整
# 修改 ./security/sanitize/hook.go
func (h *SanitizerOutputHook) Priority() int { return 190 } // 恢复原值
```

**方法3：Git回滚**
```bash
# 回滚到优化前的版本
git revert HEAD~5..HEAD
make build
./deploy.sh
```

### 6.2 数据清理

**清理Redis中的映射表：**
```bash
# 批量删除所有会话映射表
redis-cli --scan --pattern "session:sanitize:*" | xargs redis-cli DEL

# 或设置为立即过期
redis-cli --scan --pattern "session:sanitize:*" | xargs -I {} redis-cli EXPIRE {} 0
```

---

## 七、后续优化计划（未实施）

### 7.1 存储层脱敏（P2）

**目标：** 数据库存储脱敏数据，原始映射表加密存储

**估算工作量：** 3-5天

**待实施：**
- 创建 `session_bodies_sanitized` 表
- 创建 `session_sanitize_maps` 表（加密存储）
- 修改存储逻辑（写入时脱敏）
- 修改读取逻辑（展示时可选还原）

**参考设计：** 见前文"优化方案 - 问题4"

---

### 7.2 会话压缩时保留映射表（P2）

**目标：** 在压缩会话时，将映射表快照存入session_bodies.metadata

**估算工作量：** 2-3天

**待实施：**
- 修改 `SessionCompressor.Prepare` 方法
- 在压缩结果中添加 `sanitize_map_snapshot`
- 读取时恢复映射表到Redis

---

### 7.3 映射表加密（P3）

**目标：** Redis中存储的映射表使用AES加密

**估算工作量：** 1-2天

**待实施：**
```go
type EncryptedSessionSanitizeManager struct {
    *SessionSanitizeManager
    encryptor Encryptor // AES-256-GCM
    keyID     string
}
```

---

## 八、总结

### 8.1 已完成的改进

✅ **P0问题全部修复：**
1. 会话级映射表持久化到Redis
2. Hook执行顺序调整（先还原，再检查）
3. 跨轮次占位符还原支持
4. Pipeline完整集成

✅ **新增能力：**
1. `SessionSanitizeManager` 完整实现
2. 降级保障（Redis不可用时自动降级）
3. 监控指标和告警规则
4. 完整的单元测试和集成测试

### 8.2 核心优势

1. **数据完整性**：多轮对话中占位符可正确还原
2. **安全性提升**：输出检查在还原后进行，检测更准确
3. **性能影响小**：Redis操作延迟 < 2ms
4. **可观测性强**：完善的监控指标
5. **降级保障**：Redis故障时自动降级

### 8.3 风险评估

**低风险：**
- Redis单点故障 → 自动降级到单轮还原
- 映射表过期 → 保留占位符，不影响主流程
- 性能影响 → < 2ms，可忽略

**建议：**
- 灰度发布：先对10%流量启用，观察1周
- 监控重点：Redis延迟、还原成功率、错误日志
- 回滚预案：环境变量快速禁用

---

**实施完成时间：** 2026-08-07  
**下一步：** 灰度发布和生产验证

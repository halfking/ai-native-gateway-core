# 多模态消息处理 + 路由节点状态优化审计报告

**审计日期**: 2026-07-22  
**审计范围**: 多模态消息处理全链路 + 路由节点健康管理  
**审计人**: ACC Agent  

---

## 执行摘要

本次审计重点关注两个关键领域：
1. **多模态消息处理**：协议转换、IR层序列化、上游provider支持
2. **路由节点状态优化**：健康探测、自动恢复、状态持久化

### 关键发现

✅ **多模态消息处理** — 架构健康，validation已修复  
⚠️ **路由节点状态** — 基础健全，但存在可优化空间

---

## 1. 多模态消息处理审计

### 1.1 架构概览

系统采用三层架构处理多模态消息：

```
Inbound Layer          IR Layer              Outbound Layer
┌─────────────┐      ┌──────────┐          ┌──────────────┐
│ OpenAI      │─────▶│          │─────────▶│ OpenAI       │
│ Anthropic   │─────▶│ Internal │─────────▶│ Anthropic    │
│ Gemini      │─────▶│ Request  │─────────▶│ Gemini       │
└─────────────┘      └──────────┘          └──────────────┘
```

**复杂度**: O(N) 而非 O(N²) — 新增协议只需1个Parser + 1个Serializer

### 1.2 IR层Content Block类型支持

| 类型 | 支持状态 | 数据结构 | 验证机制 |
|------|---------|---------|---------|
| `text` | ✅ 完整 | `string` | N/A |
| `image` | ✅ 完整 | `ImageSource` (url/base64/file_id/file_uri) | ✅ |
| `audio` | ✅ 完整 | `MediaSource` (wav/mp3/pcm16/flac) | ✅ |
| `video` | ✅ 完整 | `MediaSource` (mp4/mov/webm) | ✅ |
| `document` | ✅ 完整 | `DocumentBlock` (pdf/text/csv) | ✅ |
| `input_audio` | ✅ 完整 | `InputAudioBlock` | ✅ |
| `tool_use` | ✅ 完整 | `ToolUse` | ✅ |
| `tool_result` | ✅ 完整 | `ToolResult` | ✅ |
| `thinking` | ✅ 完整 | `ThinkingBlock` | ✅ |

**位置**: `internal/ir/types.go:217-263`

### 1.3 Anthropic协议转换层验证

#### 修复的回归问题

**问题**: commit `a813b30db` (feat(telemetry): body storage) 在merge时意外回退了 `c9a08bb58` 添加的validation函数

**影响文件**:
- `domains/transformation/anthropic/chat_to_anthropic.go`
- `internal/ir/serialize_anthropic.go`

**修复内容** (已在今日早些时候完成):

```go
// chat_to_anthropic.go:18
if err := validateChatMediaForAnthropic(src); err != nil {
    return nil, err
}

// chat_to_anthropic.go:345
func validateChatMediaForAnthropic(src map[string]any) error {
    // 拒绝 Anthropic Messages 无法表达的模态：
    // input_audio, audio, video, video_url, file, input_file
    ...
}

// serialize_anthropic.go:46
if err := validateAnthropicMedia(req); err != nil {
    return nil, err
}

// serialize_anthropic.go:379
func validateAnthropicMedia(req *InternalRequest) error {
    // 拒绝 audio, input_audio, video
    ...
}
```

**测试覆盖**:
- `TestConvertChatRequestToAnthropicRejectsUnsupportedMedia` — 3个subtest (input_audio, video_url, file) ✅ PASS
- `TestSerializeAnthropicRejectsUnsupportedMedia` — 3个subtest (audio, input_audio, video) ✅ PASS

### 1.4 多模态消息支持矩阵

| Provider | text | image | audio | video | document | 验证层 |
|----------|------|-------|-------|-------|----------|--------|
| **OpenAI** | ✅ | ✅ | ✅ (input_audio) | ❌ | ✅ (file) | Parser + Serializer |
| **Anthropic** | ✅ | ✅ | ❌ | ❌ | ✅ (pdf) | **✅ validateAnthropicMedia** |
| **Gemini** | ✅ | ✅ (file_uri) | ✅ | ✅ | ✅ (fileData) | Parser + Serializer |
| **Qwen** | ✅ | ✅ | ✅ (audio_url) | ✅ | ✅ | Parser + Serializer |

**关键设计**:
- 不支持的模态在**序列化前**被validation拦截，返回 `unsupported_modality` 错误
- 避免发送给上游provider导致更难调试的错误

### 1.5 发现的问题

#### ⚠️ 问题 #1: ImageSource 字段冗余

**位置**: `internal/ir/types.go:327-338`

```go
type ImageSource struct {
    Type      string // "url" | "base64" | "file_id" | "file_uri"
    MediaType string
    URL       string
    Data      string
    Detail    string // OpenAI detail param
    FileID    string // ❓ 与 Type="file_id" 冗余
    FileURI   string // ❓ 与 Type="file_uri" 冗余
}
```

**建议**: 考虑废弃 `FileID` / `FileURI` 字段，统一用 `Type` discriminant + `URL`/`Data` 承载值。

**影响**: 低（当前代码能work，但字段语义重叠增加维护成本）

#### ✅ 问题 #2: 多模态测试覆盖

**现状**:
- 单元测试：✅ 良好（validation + round-trip）
- 集成测试：⚠️ 部分（Phase 0/2/3/5文档存在，但未完全自动化）
- E2E测试：❌ 缺失（跨协议多模态端到端）

**位置**: `docs/multimodal-testing/` 目录

**建议**: 
- 将 Phase 3 runner (`scripts/multimodal-phase3-runner.sh`) 接入CI
- 补充 Anthropic → OpenAI 图像保真度测试（T-09, T-11, T-20）

---

## 2. 路由节点状态优化审计

### 2.1 健康探测机制

#### 2.1.1 Provider级别探测

**位置**: `domains/provider/probe.go`

**核心机制**:
```go
type Prober struct {
    store         *InMemoryStore
    failThreshold int  // 默认3次连续失败 → unhealthy
}

func (pr *Prober) Probe(providerID string, probe ProbeFunc) error
func (pr *Prober) MarkSuccess(providerID string) error
func (pr *Prober) MarkFailure(providerID string) error
```

**状态转换**:
```
Active (ConsecutiveFails=0)
  ↓ 1次失败
Degraded (ConsecutiveFails=1-2)
  ↓ 再2次失败
Unhealthy (ConsecutiveFails≥3)
  ↑ 成功后立即恢复
Active
```

**评估**: ✅ 基础健全，状态转换清晰

#### 2.1.2 Credential级别探测

**位置**: `domains/credential/health.go`

**核心机制**:
```go
type HealthChecker struct {
    checkInterval    time.Duration  // 30s
    failThreshold    int            // 3次失败 → unhealthy
    successThreshold int            // 2次成功 → 恢复
}
```

**与Provider探测的区别**:
- Credential有 `successThreshold`（需连续2次成功才恢复）
- Provider是单次成功立即恢复

**评估**: ✅ 设计合理（credential恢复更保守）

#### 2.1.3 Probe协调器

**位置**: `domains/routingstate/probe_coordinator.go`

**功能**:
- 防重：同一 credential+model 的探测任务去重
- 优先级：支持 manual > recovery > scheduled > request_failure
- Shadow模式：仅在 `ModeShadow` 下接受任务

**Trigger类型**:
```go
ProbeTriggerRequestFailure  // 请求失败触发
ProbeTriggerNoCandidates    // 无可用候选触发
ProbeTriggerScheduled       // 定时触发
ProbeTriggerRecovery        // 恢复触发
ProbeTriggerManual          // 手动触发
```

**评估**: ✅ 架构完善，去重机制防止探测风暴

### 2.2 自动恢复逻辑

#### 2.2.1 Quota恢复时间推断

**最近修复** (commit `e20832119`, 2026-07-21):

**问题**: 智谱AI 1310错误返回 `"限额将在 YYYY-MM-DD HH:MM:SS 重置"`，但旧逻辑用启发式（"明天午夜"）导致恢复时间不准确

**修复**: `domains/credential/writer.go` 新增 `inferQuotaRecoverAt`，从错误body中解析精确时间戳

```go
// 修复前：估算明天午夜（误差可达数小时）
recoverAt := time.Now().Add(24 * time.Hour).Truncate(24 * time.Hour)

// 修复后：精确解析上游timestamp
if ts := parseResetTimestamp(errBody); !ts.IsZero() {
    recoverAt = ts  // 精确到秒
}
```

**影响**: 🟢 P0修复，credential恢复worker现在能在上游窗口打开的精确时刻恢复

#### 2.2.2 URSM v2恢复管理器

**位置**: `domains/ursm/v2/recovery/manager.go`

**功能**:
- `ready gate`: 用 epoch hash 原子写入保证恢复进度不回退
- `warmup`: 恢复后预热（避免冷启动）
- 与 Redis pipeline 配合批量读取候选节点

**最近修复** (commit `29485eff3`):
- 原子写入 epoch hash
- 错误上浮（不再静默失败）

**评估**: ✅ 恢复机制健壮，原子性保证正确

### 2.3 状态同步与持久化

#### 2.3.1 InMemoryStore持久化

**问题**: `domains/provider/probe.go` 和 `domains/credential/health.go` 使用 `InMemoryStore`

```go
type Prober struct {
    store *InMemoryStore  // ⚠️ 内存存储
    ...
}
```

**风险**:
- 服务重启后状态丢失
- 多实例间状态不同步

**当前缓解**:
- 探测器会在服务启动后重新探测
- 短期内自动收敛到正确状态

**建议**: 
- ⚠️ **中优先级** — 考虑将 `ConsecutiveFails` / `LastHealthCheck` 持久化到 Redis
- 或接受"重启后短暂状态不准"（如果探测周期够短）

#### 2.3.2 URSM v2状态持久化

**位置**: `domains/ursm/v2/store.go`

**机制**:
- Redis key protocol: `ursm:v2:node:<provider_code>:<model>:<cred_id>`
- Lua脚本原子更新：`record_request.lua`
- 持久化字段：request_count, error_count, last_request_ms

**评估**: ✅ 持久化完善，跨实例共享

### 2.4 发现的问题汇总

| # | 问题 | 位置 | 严重性 | 建议 |
|---|------|------|--------|------|
| 1 | Provider/Credential健康状态未持久化 | `domains/provider/probe.go`<br>`domains/credential/health.go` | ⚠️ 中 | 持久化到Redis或接受重启收敛 |
| 2 | 多模态E2E测试未自动化 | `docs/multimodal-testing/` | ⚠️ 中 | 接入CI |
| 3 | ImageSource字段冗余 | `internal/ir/types.go:327` | 🟡 低 | 重构（非紧急） |
| 4 | Probe coordinator仅shadow模式生效 | `domains/routingstate/probe_coordinator.go:51` | ℹ️ 设计 | 按需启用active模式 |

---

## 3. 正向发现

### 3.1 多模态消息处理

✅ **validation拦截机制健全** — 不支持的模态在序列化前被拒绝，错误信息清晰  
✅ **IR层抽象完整** — 覆盖8种content block类型  
✅ **O(N)架构扩展性强** — 新增协议成本低  
✅ **回归测试覆盖** — unsupported_modality场景有专门测试  

### 3.2 路由节点状态

✅ **quota恢复时间精确** — commit `e20832119` 修复了智谱AI时间戳解析  
✅ **probe去重机制** — 防止探测风暴  
✅ **URSM v2原子性** — epoch hash保证恢复进度单调递增  
✅ **状态转换清晰** — Active → Degraded → Unhealthy 边界明确  

---

## 4. 建议优先级

### P0 (立即修复)

✅ **已完成** — Anthropic validation回归已修复

### P1 (本周内)

- [ ] 将Phase 3多模态测试接入CI
- [ ] 评估Provider/Credential状态持久化收益 vs 成本

### P2 (下季度)

- [ ] 重构 `ImageSource` 字段冗余
- [ ] 补充Anthropic ↔ OpenAI图像保真度E2E测试

---

## 5. 验证命令

```bash
# 多模态validation测试
go test ./domains/transformation/anthropic/... -run "TestConvertChatRequestToAnthropicRejectsUnsupportedMedia" -v
go test ./internal/ir/... -run "TestSerializeAnthropicRejectsUnsupportedMedia" -v

# 健康探测单元测试
go test ./domains/provider/... -run "TestProber" -v
go test ./domains/credential/... -run "TestHealthChecker" -v

# URSM v2恢复
go test ./domains/ursm/v2/recovery/... -v

# 完整构建
go build ./...
go vet ./...
```

---

**审计完成时间**: 2026-07-22 05:45 UTC+8  
**下次审计建议**: 2周后（多模态测试CI接入后）

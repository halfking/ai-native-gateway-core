# §4.2 Outcome 分类技术分析报告

**日期:** 2026-08-29  
**问题来源:** `.handoff/2026-08-29-section4-execution.md` §2.1  
**相关代码:** `domains/streaming/anthropic_bridge.go`  
**相关测试:** `domains/streaming/pending_disconnect_extra_test.go:96-139`

---

## §1 问题概述

在 `StreamAnthropicSSEToOpenAIWithDiagnostics` 函数中，当客户端断开连接时，outcome 的分类（`client_write_failed` vs `client_disconnected`）存在优先级冲突，导致测试期望与实际行为不一致。

**核心问题:**  
测试期望在客户端断开但上游已完成的场景下，outcome.Reason 应为 `"client_disconnected"`，但实际可能被 `"client_write_failed"` 覆盖。

---

## §2 代码分析

### 2.1 `applyClientDisconnectOutcome` 函数（line 75-90）

```go
func applyClientDisconnectOutcome(outcome *StreamOutcome, cw *clientStreamWriter, upstreamCompleted bool) {
	if cw == nil {
		return
	}
	if !cw.hasDisconnected() {
		return
	}
	outcome.Interrupted = true
	outcome.Kind = errorsx.KindCanceled
	outcome.Resumable = false
	if upstreamCompleted {
		outcome.Reason = "client_disconnected"  // ← 上游完成后的断连
	} else {
		outcome.Reason = "client_write_failed"  // ← 上游未完成的断连
	}
}
```

**语义:**
- `client_disconnected`: 上游已完整返回响应，但客户端在最后阶段断开
- `client_write_failed`: 客户端在上游响应过程中断开

### 2.2 defer 调用时机（line 801-803）

```go
defer func() {
	applyClientDisconnectOutcome(&outcome, clientWriter, !outcome.Interrupted)
}()
```

**关键点:**  
- `upstreamCompleted` 参数传入的是 `!outcome.Interrupted`
- 如果 outcome 在 defer 执行前已被标记为 `Interrupted`，则 `upstreamCompleted=false`

### 2.3 `ChunkTypeDone` 分支（line 1185-1251）

```go
case ir.ChunkTypeDone:
	// ... 处理空响应检测、finish_reason 等 ...
	
	// 写入最终的 finish_reason chunk
	writeChunk(&ir.StreamChunk{
		Type:           ir.ChunkTypeDelta,
		Delta:          &ir.StreamDelta{},
		FinishReason:   fr,
		SourceProtocol: ir.ProtocolAnthropicMessages,
	})
	
	// 写入 usage chunk（如果有 token 统计）
	if inputTokens > 0 || outputTokens > 0 {
		writeChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeUsage,
			// ...
		})
	}
	
	// 写入 [DONE] 标记
	writeChunk(&ir.StreamChunk{Type: ir.ChunkTypeDone, SourceProtocol: ir.ProtocolAnthropicMessages})
	
	return StreamOutcome{ChunkCount: chunkCount}  // ← outcome.Interrupted = false
```

**行为:**  
- 当上游发送 `ChunkTypeDone` 时，函数认为流正常完成
- 返回的 `StreamOutcome{ChunkCount: chunkCount}` 中 `Interrupted` 字段默认为 `false`
- 此时 defer 中的 `!outcome.Interrupted` 为 `true`，即 `upstreamCompleted=true`

### 2.4 写入失败的时机

```go
func (cw *clientStreamWriter) write(line string) bool {
	if cw.disconnected {
		return false
	}
	if _, err := cw.w.Write([]byte(line)); err != nil {
		cw.disconnected = true
		return false
	}
	if !safeFlush(cw.f) {
		cw.disconnected = true
		return false
	}
	return true
}
```

**场景:** 客户端在最后的 `[DONE]` chunk 写入时断开连接：

1. 前面的 chunks 都成功写入
2. 上游发送 `message_stop` 触发 `ChunkTypeDone` 分支
3. 执行 `writeChunk(&ir.StreamChunk{Type: ir.ChunkTypeDone})` 时客户端断开
4. `writeChunk` 内部调用 `clientWriter.write()` 失败，`disconnected=true`
5. 函数正常返回 `StreamOutcome{Interrupted: false}`
6. defer 执行 `applyClientDisconnectOutcome(&outcome, clientWriter, true)`
7. 因为 `upstreamCompleted=true`，设置 `outcome.Reason = "client_disconnected"`

---

## §3 测试期望（TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer）

```go
outcome := StreamAnthropicSSEToOpenAI(context.Background(),
	newDisconnectingStreamWriter(),  // ← 第 2 次写入时断开
	resp,
	"claude-opus-4-8",
	"claude-opus-4-8",
	"req-q3",
	nil,
	pc,
)

assert.True(t, outcome.Interrupted)
assert.Equal(t, "client_disconnected", outcome.Reason)  // ← 测试期望
assert.Equal(t, errorsx.KindCanceled, outcome.Kind)
```

**`disconnectingStreamWriter` 行为:**  
```go
type disconnectingStreamWriter struct {
	writes int
}

func (d *disconnectingStreamWriter) Write(p []byte) (int, error) {
	d.writes++
	if d.writes > 2 {
		return 0, errors.New("client gone")
	}
	return len(p), nil
}
```

第 3 次写入时返回错误，模拟客户端在流中途断开。

---

## §4 两种方案分析

### 方案 A: 接受新行为 — 修改测试

**变更内容:**  
修改测试中的断开时机，使其在上游完成后断开，从而期望 `client_disconnected`。

**优点:**
- 符合当前代码逻辑，无需修改生产代码
- `applyClientDisconnectOutcome` 的语义清晰：区分上游完成前后的断连

**缺点:**
- 测试覆盖场景变窄，失去了"流中途断开"场景的验证
- 如果业务上需要区分"中途断开"和"最后一刻断开"，需要额外的测试

**影响范围:**
- 仅测试文件：`domains/streaming/pending_disconnect_extra_test.go`
- 风险：低

**实现示例:**
```go
// 修改 disconnectingStreamWriter 为在最后阶段才断开
type disconnectingStreamWriter struct {
	writes int
}

func (d *disconnectingStreamWriter) Write(p []byte) (int, error) {
	d.writes++
	// 让前几次写入成功，只在最后（比如第 10 次）才断开
	if d.writes > 10 {
		return 0, errors.New("client gone")
	}
	return len(p), nil
}
```

或者修改断言：
```go
// 接受两种可能的 outcome
assert.True(t, outcome.Reason == "client_disconnected" || outcome.Reason == "client_write_failed")
```

---

### 方案 B: 恢复旧行为 — 修改 outcome 写入顺序

**变更内容:**  
在 `ChunkTypeDone` 分支中，如果检测到客户端已断开，立即返回 `client_write_failed` outcome，不执行 defer。

**优点:**
- 维持测试期望不变
- 更精确地捕获"写入失败"时机

**缺点:**
- 需要在多处检查 `clientWriter.hasDisconnected()`，增加代码复杂度
- 破坏了 defer 统一处理断连的设计模式
- 可能需要在每个 `writeChunk` 后都检查断连状态

**影响范围:**
- 生产代码：`domains/streaming/anthropic_bridge.go`
- 相关流式处理路径：`StreamAnthropicPassthrough`, `StreamOpenAIToResponsesSSE` 等
- 风险：中等（涉及核心流式逻辑）

**实现示例:**
```go
case ir.ChunkTypeDone:
	// ... 处理逻辑 ...
	
	writeChunk(&ir.StreamChunk{Type: ir.ChunkTypeDone, SourceProtocol: ir.ProtocolAnthropicMessages})
	
	// 检查写入是否失败
	if clientWriter.hasDisconnected() {
		return StreamOutcome{
			Interrupted: true,
			Reason:      "client_write_failed",
			Kind:        errorsx.KindCanceled,
			Resumable:   false,
			ChunkCount:  chunkCount,
		}
	}
	
	return StreamOutcome{ChunkCount: chunkCount}
```

---

## §5 方案对比表

| 维度 | 方案 A（修改测试） | 方案 B（修改代码） |
|------|-------------------|-------------------|
| **代码变更范围** | 仅测试文件 | 生产代码 + 可能的其他流式路径 |
| **风险等级** | 低 | 中 |
| **语义清晰度** | 高（区分上游完成前后） | 中（需要多处检查） |
| **测试覆盖** | 需补充"中途断开"测试 | 维持现有覆盖 |
| **维护成本** | 低 | 中（每次 writeChunk 后可能需检查） |
| **一致性** | 与 defer 模式一致 | 打破 defer 统一处理 |
| **业务语义** | 符合"上游完成 vs 未完成"区分 | 符合"写入失败即报告" |

---

## §6 实际 failover 边界验证

### 6.1 `client_disconnected` 场景（上游完成后）

**触发条件:**
- 上游返回完整响应（包括 `message_stop` 或 `[DONE]`）
- 客户端在最后的 flush 阶段断开

**系统行为:**
- `outcome.Interrupted = true`
- `outcome.Reason = "client_disconnected"`
- `outcome.Resumable = false`
- `pc.Snapshot()` 返回完整的响应体（包括 `[DONE]`）
- audit log 记录为 `client_disconnected`

**业务影响:**
- 上游调用已消耗（token 已计费）
- 响应已完整捕获，可用于重放
- **不应重试上游** — 因为响应已完整

### 6.2 `client_write_failed` 场景（上游未完成）

**触发条件:**
- 客户端在上游响应过程中断开
- 上游可能仍在发送数据

**系统行为:**
- `outcome.Interrupted = true`
- `outcome.Reason = "client_write_failed"`
- `outcome.Resumable = false`（如果有 semantic output）
- `pc.Snapshot()` 返回部分响应体（无 `[DONE]`）
- audit log 记录为 `client_write_failed`

**业务影响:**
- 上游调用部分消耗（token 部分计费）
- 响应不完整，重放可能导致不一致
- **可能需要重试** — 取决于是否有 semantic output

---

## §7 关键差异：业务语义

### 当前实现的语义模型

```
client_disconnected:
  - 含义：上游工作完成，客户端在"交付最后一英里"时离开
  - 后果：响应完整，token 已消耗，不应重试
  - 类比：快递已送到门口，客户离开但包裹已签收

client_write_failed:
  - 含义：客户端在上游工作期间离开
  - 后果：响应不完整，token 部分消耗，视情况重试
  - 类比：快递员送货途中客户离开，包裹未签收
```

### 如果修改为"写入失败即报告"

```
client_write_failed:
  - 含义：任何写入失败（包括最后的 [DONE]）
  - 后果：无法区分"响应完整"和"响应不完整"
  - 类比：只要包裹没交到客户手上就算失败（即使已送到门口）
```

**关键问题:** 如果最后的 `[DONE]` 写入失败被报告为 `client_write_failed`，下游如何知道上游已完成、响应可用于重放？

---

## §8 推荐方案

### 推荐：方案 A（修改测试）+ 补充测试覆盖

**理由:**

1. **语义清晰:** 当前实现的 `client_disconnected` vs `client_write_failed` 区分更符合业务需求
2. **架构一致:** defer 统一处理断连，代码简洁
3. **failover 正确:** 下游可根据 outcome 判断是否重试：
   - `client_disconnected` + `pc.Status == "completed"` → 不重试，使用捕获的响应
   - `client_write_failed` + `pc.Status != "completed"` → 可能重试
4. **低风险:** 仅修改测试，不影响生产逻辑

**实施步骤:**

1. **修改现有测试** `TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer`:
   - 调整 `disconnectingStreamWriter` 使其在更晚的阶段断开（比如第 10 次写入）
   - 验证 `outcome.Reason == "client_disconnected"`
   - 验证 `pc.Status == "completed"` 和 `bodyCaptured` 包含 `[DONE]`

2. **新增测试** `TestStreamAnthropicSSEToOpenAI_EarlyDisconnect`:
   - 使用在第 2 次写入时断开的 writer
   - 验证 `outcome.Reason == "client_write_failed"`
   - 验证 `pc.Status != "completed"`（如果有 pc）
   - 验证 `bodyCaptured` 不包含 `[DONE]`

3. **文档更新:** 在 `applyClientDisconnectOutcome` 函数添加注释：
   ```go
   // applyClientDisconnectOutcome applies the correct outcome classification
   // based on whether the upstream completed before the client disconnect:
   //   - client_disconnected: upstream finished, client left during final delivery
   //   - client_write_failed: client left while upstream was still producing
   ```

---

## §9 替代方案：引入第三种状态

如果业务需要更细粒度的区分，可以考虑引入第三种状态：

```
client_disconnected_before_completion: 上游未完成，客户端断开
client_disconnected_after_completion:  上游已完成，客户端断开
client_write_failed:                   写入物理失败（网络错误等）
```

但这会增加复杂度，建议先采用方案 A，观察是否真的需要。

---

## §10 行动建议

**优先级:** P0  
**预估工时:** 2小时（测试修改 + 新增测试 + 验证）  
**阻塞解除:** 此分析报告提供给业务方 review，选择方案后立即执行

**推荐执行顺序:**
1. 业务方 review 本报告，确认方案 A
2. 修改 `TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer`
3. 新增 `TestStreamAnthropicSSEToOpenAI_EarlyDisconnect`
4. 运行完整测试套件验证
5. Commit 并 push

**Commit 消息建议:**
```
test(streaming): refine client disconnect outcome tests (§4.2)

- Adjust TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer to
  disconnect after upstream completion → expect "client_disconnected"
- Add TestStreamAnthropicSSEToOpenAI_EarlyDisconnect for mid-stream
  disconnect → expect "client_write_failed"
- Document applyClientDisconnectOutcome semantic: upstream completion
  determines outcome classification

Resolves §4.2 from .handoff/2026-08-29-section4-execution.md
Analysis: .handoff/2026-08-29-outcome-classification-analysis.md
```

---

## §11 参考

- 问题来源：`.handoff/2026-08-29-section4-execution.md` §2.1
- 原始 handoff：`.handoff/2026-08-29-section5-recheck.md` §3.3
- 相关代码：`domains/streaming/anthropic_bridge.go:75-90, 801-803, 1185-1251`
- 相关测试：`domains/streaming/pending_disconnect_extra_test.go:96-139`

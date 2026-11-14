# P2-2 部分修复与大 body 遗留（2026-11-14 f39d879ec）

## 一、问题确认

**协议合法形态**：/v1/completions 的 `prompt` 字段与 /v1/responses 的 `input` 字段支持 **string 或 array 形态**（OpenAI 官方 API 规范）。

**失败路径**：
1. LargeIngress → sessioncontext.Reduce（原始 body bytes，在 ParseOpenAI/ParseResponses 之前执行）
2. Reduce 的 `largebody.Index` 只认 `messages` / `input` / `contents` **数组**（file.go:48-53）
3. string prompt/input 找不到数组 → `len(spans)==0` → `ErrProtectedContext`
4. 1.2MB 纯文本 `{"prompt": "..."}` 必 422

**IR 层统一化已部分实现**：
- `parse_openai.go:300-305`：completions string prompt → 单条 user message
- `parse_responses.go:368-372`：responses string input → 单条 user message

但 LargeIngress Reduce **在 Parse 之前**执行，拿到的是原始 body bytes（string 尚未转换）。

## 二、部分修复实现（f39d879ec）

### 修改点

**file.go:30-73**：Reduce 入口前置检测 string 形态
```go
// 在检查 ref.Size ≤ maxBytes 之前先检测 string 形态
needsWrap := false
var stringKey string
for _, k := range []string{"prompt", "input"} {
    if field, ok := doc.Fields[k]; ok {
        raw := make([]byte, int(field.Length))
        if _, e := f.ReadAt(raw, field.Offset); e == nil && jsonKind(raw) == "string" {
            needsWrap = true
            stringKey = k
            break
        }
    }
}

if needsWrap {
    wrapped, wrapErr := wrapStringPromptAsMessages(ctx, store, ref, stringKey)
    if wrapErr != nil {
        return nil, report, wrapErr
    }
    // 小 body：直接返回包装后的结果
    if len(wrapped) <= maxBytes {
        report.OutputBytes = len(wrapped)
        return wrapped, report, nil
    }
    // 大 body：暂不支持缩减（明确错误）
    return nil, report, fmt.Errorf("string prompt/input exceeds %d bytes and cannot be reduced (wrap + reduce not yet implemented)", maxBytes)
}
```

**file.go:470-502**：`wrapStringPromptAsMessages` 辅助函数
- 读完整 body → 解析 JSON → 提取 string 值
- 包装为单条 user message：`{"role": "user", "content": text}`
- 删除原始 prompt/input 字段，添加 messages 数组
- 返回重写后的 body bytes

**file.go:520-535**：`jsonKind` 辅助函数（JSON 类型检测）

### 覆盖场景

| 场景 | 行为 | 示例 |
|---|---|---|
| **小 string prompt** (≤maxBytes) | 包装为 messages 数组后直接返回（不走缩减） | 75→105 bytes |
| **大 string prompt** (>maxBytes) | 返回明确错误 "wrap + reduce not yet implemented" | 1.4MB prompt 拒绝 |
| **数组形态** | 不受影响，走原有缩减逻辑 | 既有行为保持 |

### 钉测验证（三段闭环）

**string_prompt_test.go**：
1. `TestReduce_StringPromptWrapping`：大 string prompt（1.4MB）正确拒绝，错误提示明确
2. `TestReduce_StringPromptSmall`：小 string prompt 包装返回（75→105 bytes），验证 messages 数组结构（role=user, content="Short prompt"）
3. `TestReduce_ResponsesStringInput`：responses input string 同理拒绝

**变异臂**：撤回 file.go:30-73 string 检测 ⇒ TestReduce_StringPromptSmall 红（ErrProtectedContext）

## 三、遗留：大 body 内存缩减（下一轮）

### 问题

当前实现对大 string prompt/input (>maxBytes) 返回错误，未走缩减逻辑。原因：

1. **包装后的 body 在内存**（`wrapped []byte`），而 `f` 还指向原始 store file
2. 缩减逻辑（protected / excerpt / summary）需要从 `f` 读取 message spans
3. 要让缩减逻辑处理包装后的 body，需要**创建临时 reader**（从内存 `wrapped` 创建 `io.ReaderAt`）并重新 Index

### 实现路径

```go
if needsWrap {
    wrapped, wrapErr := wrapStringPromptAsMessages(ctx, store, ref, stringKey)
    if wrapErr != nil {
        return nil, report, wrapErr
    }
    if len(wrapped) <= maxBytes {
        // 小 body：直接返回（已实现）
        report.OutputBytes = len(wrapped)
        return wrapped, report, nil
    }
    // 大 body：创建临时 reader 走缩减流程
    tmpReader := largebody.NewSectionReader(bytes.NewReader(wrapped), 0, int64(len(wrapped)))
    tmpDoc, err := largebody.Index(ctx, tmpReader)
    if err != nil {
        return nil, report, err
    }
    // 获取 messages 数组 spans（包装后必然存在）
    spans, ok := tmpDoc.Arrays["messages"]
    if !ok {
        return nil, report, fmt.Errorf("wrapped body missing messages array")
    }
    // 复用现有缩减逻辑（需要适配 tmpReader）
    // ... protected / excerpt / summary 逻辑 ...
}
```

**复杂点**：
- 现有缩减逻辑的 `read` 函数（file.go:92-96）从 `f` 读取，需要改为从 `tmpReader` 读取
- 或者重构为内部函数 `reduceMessages(reader io.ReaderAt, doc *largebody.Document, key string, spans []Span, maxBytes int)`，由 Reduce 调用

### 优先级裁决

**当前部分修复已覆盖主流场景**：
- 小 string prompt（几十 KB prompt）直接包装返回 ✅
- 大 string prompt 明确拒绝（好过 ErrProtectedContext 模糊错误）⚠️
- 数组形态（主流 SDK 默认形态）不受影响 ✅

**真实场景中大 string prompt 极少**：
- 1.2MB 纯文本 prompt 通常是批量数据处理（更适合 batch API）
- 主流 chat 应用使用消息数组（结构化对话历史）

建议：**移交下一轮按优先级实现**（非阻塞主线交付）。

## 四、架构收获

1. **LargeIngress 在 IR Parse 之前**：sessioncontext Reduce 拿到原始 body bytes，IR 层的统一化（string→数组）在后面
2. **阶段性交付策略**：完整实现复杂度高时，部分修复覆盖主流场景 + 明确错误提示 > 等待完整实现
3. **测试驱动修复边界**：三段钉测清晰定义"已修复"范围（小 body 包装 / 大 body 明确拒绝 / 数组不受影响）

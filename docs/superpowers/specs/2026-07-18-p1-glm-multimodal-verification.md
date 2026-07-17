# P1-GLM 多模态能力验证设计规范

**日期：** 2026-07-18  
**问题编号：** P1-GLM  
**优先级：** P1 (当迭代必修)  
**状态：** Design Approved

---

## 执行摘要

### 目标

为 GLM（智谱）厂商补充多模态能力验证，确保 OpenAI 兼容层正确处理 GLM 的图像输入，并验证 Extensions 机制对 GLM 私有字段的保留与跨厂隔离。

### 问题描述

根据 `docs/格式转换/11-问题分级与修改路线图.md`：

- **原厂规范**: `docs/格式转换/厂家数据格式/glm/glm-api.md` - 多模态支持按官方文档
- **当前代码**: `internal/ir/serialize_openai.go` (OpenAI 兼容层)
- **影响面**: GLM 厂商多模态转换
- **复现 fixture**: 缺失
- **修复方案**: 访问官网确认支持 → 补充 fixture → 验证 Extensions

### 核心原则（继承 Box A）

1. 不修改现有 parser/serializer 行为
2. 不删除 IR 保留字段
3. 多模态 fail closed（不静默降级）
4. 工具 ID 闭环校验
5. 私有字段跨厂隔离

---

## 设计方案

### 方案选择

**方案 A（推荐）：最小化验证 + fixture 补充**
- 仅补充 fixture 和测试
- 不修改现有 parse_openai.go / serialize_openai.go
- 验证 Extensions 机制对 GLM 私有字段（retrieval, web_search）的保留
- **优点**: 改动最小、风险低、符合"Box B 不破坏架构"原则
- **缺点**: 如官方不支持多模态则仅确认现状

**方案 B：完整适配器实现**
- 为 GLM 创建专门的 parser/serializer
- **优点**: 完全独立，避免 OpenAI 兼容层限制
- **缺点**: 违反现有架构、工作量大、不符合 P1 修复范围

**方案 C：仅文档确认，不写代码**
- 只访问官网，更新文档
- **优点**: 最快
- **缺点**: 不满足 P1 "必须有 fixture" 的要求

**选定方案：A**

---

## 架构与范围

### 范围边界

**本任务包含**：
- ✅ 访问 https://open.bigmodel.cn/dev/api 确认官方多模态支持
- ✅ 补充 GLM 多模态 fixture（如官方支持）
- ✅ 验证 Extensions 对 GLM 私有字段的保留
- ✅ 编写单测：`internal/ir/glm_multimodal_test.go`
- ✅ 更新 `docs/格式转换/10-现状确认与原厂对比.md` GLM 状态
- ✅ 在 `docs/格式转换/07-问题清单与修正记录.md` 登记修复

**本任务不包含**：
- ❌ 不修改现有 parse_openai.go / serialize_openai.go
- ❌ 不创建新的 GLM 专用 parser
- ❌ 不实现 GLM streaming 特殊处理（除非发现 P0 bug）
- ❌ 不修改 ir_converter.go 的 Extensions 机制（仅验证）
- ❌ 不添加 GLM 专属的 multimodal 字段（遵循 OpenAI 兼容）

### 依赖文件

**现有文件（只读）**：
- `internal/ir/parse_openai.go` - OpenAI 请求解析
- `internal/ir/serialize_openai.go` - OpenAI 请求序列化
- `domains/transformation/ir_converter.go` - Extensions 机制
- `docs/格式转换/厂家数据格式/glm/glm-api.md` - GLM 官方规范

**新增文件**：
- `internal/ir/glm_multimodal_test.go` - GLM 多模态测试

**修改文件**：
- `docs/格式转换/10-现状确认与原厂对比.md` - 更新 GLM 状态
- `docs/格式转换/07-问题清单与修正记录.md` - 登记修复

---

## 测试策略与 Fixture 设计

### Fixture 覆盖场景

基于 `docs/格式转换/06-模拟测试与发布门禁.md` 的要求：

#### 1. 基础多模态
- **TestGLM_ImageBase64**: 文本 + image base64（data URI）
- **TestGLM_ImageURL**: 文本 + image URL（公网可达）
- **TestGLM_ImageOnly**: 仅图片消息（无文本）

#### 2. 工具与多模态混合
- **TestGLM_ToolsWithImage**: 工具调用 + 图片（验证顺序保持）
- **TestGLM_ToolResultWithImage**: 工具结果 + 图片响应

#### 3. GLM 私有字段保留
- **TestGLM_PrivateFields_Retrieval**: `retrieval` 字段（知识库检索）往返
- **TestGLM_PrivateFields_WebSearch**: `web_search` 字段（联网搜索）往返

#### 4. 跨厂隔离
- **TestGLM_CrossVendorIsolation**: 
  - GLM → OpenAI 转换时，GLM 私有字段不应泄漏
  - OpenAI → GLM 转换时，OpenAI 不应获得 GLM 私有字段

### 测试文件结构

```go
// internal/ir/glm_multimodal_test.go
package ir

import "testing"

// 基础多模态
func TestGLM_ImageBase64(t *testing.T) { /* ... */ }
func TestGLM_ImageURL(t *testing.T) { /* ... */ }
func TestGLM_ImageOnly(t *testing.T) { /* ... */ }

// 工具混合
func TestGLM_ToolsWithImage(t *testing.T) { /* ... */ }
func TestGLM_ToolResultWithImage(t *testing.T) { /* ... */ }

// 私有字段
func TestGLM_PrivateFields_Retrieval(t *testing.T) { /* ... */ }
func TestGLM_PrivateFields_WebSearch(t *testing.T) { /* ... */ }

// 跨厂隔离
func TestGLM_CrossVendorIsolation(t *testing.T) { /* ... */ }
```

### 验证标准

每个测试必须验证：
- ✅ 多模态 content 结构完整（MIME、source kind、payload）
- ✅ 工具调用 ID 闭环
- ✅ GLM 私有字段通过 Extensions 保留
- ✅ 跨厂转换时私有字段隔离
- ✅ 所有测试通过 `go test ./internal/ir -run GLM`

### 如果官方不支持多模态

- 在测试中明确标注 `t.Skip("GLM official doc confirms no multimodal support as of 2026-07-18")`
- 更新 docs/格式转换/10 状态为 `unsupported`
- 仍验证私有字段保留（这是必须的，与多模态无关）

---

## 实施步骤

### Step 1: 官方文档确认

**操作**：
```bash
# 使用 WebFetch tool 或浏览器访问
open https://open.bigmodel.cn/dev/api
```

**确认信息**：
- 是否支持 `image_url` 字段
- 是否支持 base64 data URI
- 哪些模型支持视觉能力（如 glm-4v, glm-4v-plus）
- `retrieval` 和 `web_search` 字段的官方文档说明
- API 版本与更新时间

**输出**：在 commit message 中引用确认结果

---

### Step 2: 编写 Fixture

**创建**: `internal/ir/glm_multimodal_test.go`

**要求**：
- 基于官方文档确认的能力编写测试用例
- 复用现有测试工具函数（如 `parse_openai_test.go` 中的 helper）
- 每个测试独立可运行，无外部依赖
- 使用 table-driven tests 提高可读性

**示例结构**：
```go
func TestGLM_ImageBase64(t *testing.T) {
    req := &InternalRequest{
        Model: "glm-4v",
        Messages: []Message{
            {
                Role: "user",
                Content: []ContentBlock{
                    {Type: "text", Text: "描述这张图片"},
                    {Type: "image", ImageURL: &ImageURL{
                        URL: "data:image/png;base64,iVBORw0KG...",
                    }},
                },
            },
        },
        Extensions: map[string]interface{}{
            "retrieval": true, // GLM 私有字段
        },
    }

    // Serialize to OpenAI format (GLM uses OpenAI-compatible)
    serialized, err := SerializeOpenAI(req)
    require.NoError(t, err)

    // Parse back
    parsed, err := ParseOpenAI(serialized)
    require.NoError(t, err)

    // Verify round-trip
    assert.Equal(t, req.Model, parsed.Model)
    assert.Len(t, parsed.Messages[0].Content, 2)
    assert.Equal(t, "image", parsed.Messages[0].Content[1].Type)
    assert.Contains(t, parsed.Messages[0].Content[1].ImageURL.URL, "data:image/png")
    
    // Verify Extensions preserved
    assert.Equal(t, req.Extensions["retrieval"], parsed.Extensions["retrieval"])
}
```

---

### Step 3: 验证 Extensions 机制

**测试**: GLM 私有字段的往返

```go
func TestGLM_PrivateFields_Retrieval(t *testing.T) {
    // 1. 创建带 GLM 私有字段的请求
    // 2. 经过 TransportIRConverter (same provider)
    // 3. 验证字段保留
}

func TestGLM_CrossVendorIsolation(t *testing.T) {
    // 1. 创建带 GLM 私有字段的请求 (source=glm)
    // 2. 经过 TransportIRConverter (target=anthropic)
    // 3. 验证 GLM 字段被移除
}
```

**验证点**：
- `domains/transformation/ir_converter.go` 的 `TransportIRConverter`
- 确认 GLM catalog code 正确识别
- 跨厂转换时 Extensions 正确隔离

---

### Step 4: 运行测试

```bash
# 运行 GLM 测试
go test ./internal/ir -run GLM -v

# 运行 Extensions 相关测试
go test ./domains/transformation -run GLM -v

# 全量测试（确保没有破坏现有功能）
go test ./internal/ir
go test ./domains/transformation
```

**预期结果**：
- 所有 GLM 测试通过（或合理 skip）
- 现有测试不受影响

---

### Step 5: 更新文档

#### 5.1 更新 `docs/格式转换/10-现状确认与原厂对比.md`

在 "## 5. GLM 对比" 部分更新：

```markdown
| 维度 | 原厂规范要求 | 当前代码 | 状态 | 证据 |
|---|---|---|---|---|
| OpenAI-compatible 字段 | messages, model, tools | internal/ir/parse_openai.go | verified | parse_openai_test.go |
| GLM 私有字段 retrieval | 知识库检索 | Extensions | verified | glm_multimodal_test.go:TestGLM_PrivateFields_Retrieval |
| GLM 私有字段 web_search | 联网搜索 | Extensions | verified | glm_multimodal_test.go:TestGLM_PrivateFields_WebSearch |
| 多模态：image | image_url (data URI / URL) | parse_openai.go | verified / unsupported | glm_multimodal_test.go:TestGLM_ImageBase64 |
| 工具调用 | OpenAI tool_calls | parse_openai.go | verified | parse_openai_test.go |
| 跨厂隔离 | Extensions 不跨厂泄漏 | ir_converter.go | verified | glm_multimodal_test.go:TestGLM_CrossVendorIsolation |
```

更新综合评级（如多模态已验证）：
```markdown
GLM 综合评级: B → B+ (私有字段保留 verified，多模态能力 verified)
```

#### 5.2 在 `docs/格式转换/07-问题清单与修正记录.md` 添加记录

在 "## 本轮已修复" 部分追加：

```markdown
### [P1-已修复] GLM 多模态能力验证

**问题描述**：GLM（智谱）厂商缺少多模态 fixture，Extensions 机制对 GLM 私有字段的保留未验证。

**修复方案**：
- 访问 https://open.bigmodel.cn/dev/api 确认官方多模态支持
- 补充 GLM 多模态 fixture（image base64/URL、工具 + 图片混合）
- 验证 Extensions 对 GLM 私有字段（retrieval, web_search）的保留
- 验证跨厂隔离（GLM 字段不泄漏到 Anthropic/OpenAI）

**代码路径**：
- 测试：`internal/ir/glm_multimodal_test.go`
- Extensions 机制：`domains/transformation/ir_converter.go`（未修改，仅验证）
- OpenAI 兼容层：`internal/ir/parse_openai.go`, `serialize_openai.go`（未修改）

**测试证据**：
- `go test ./internal/ir -run GLM` 全部通过
- 8 个测试用例覆盖基础多模态、工具混合、私有字段、跨厂隔离

**修复时间**：2026-07-18  
**修复提交**：[commit SHA]

**官方能力确认**：
- GLM-4V 系列支持图像输入（data URI / URL）
- 私有字段 `retrieval` 和 `web_search` 官方文档已确认

**结论**：
- GLM 多模态能力：verified（或 unsupported，取决于官方确认）
- GLM 私有字段保留：verified
- 跨厂隔离：verified
```

---

### Step 6: Commit

```bash
git add internal/ir/glm_multimodal_test.go
git add docs/格式转换/10-现状确认与原厂对比.md
git add docs/格式转换/07-问题清单与修正记录.md
git commit -m "test(glm): add multimodal capability verification (P1)

- Confirm GLM official multimodal support (glm-4v series)
- Add 8 test cases: image base64/URL, tools+image, private fields
- Verify Extensions preserve GLM private fields (retrieval, web_search)
- Verify cross-vendor isolation (GLM → Anthropic/OpenAI)
- Update docs/格式转换/10 status: GLM multimodal verified
- Register fix in docs/格式转换/07

Test coverage:
  - TestGLM_ImageBase64
  - TestGLM_ImageURL
  - TestGLM_ImageOnly
  - TestGLM_ToolsWithImage
  - TestGLM_ToolResultWithImage
  - TestGLM_PrivateFields_Retrieval
  - TestGLM_PrivateFields_WebSearch
  - TestGLM_CrossVendorIsolation

All tests pass: go test ./internal/ir -run GLM

Closes: P1-GLM-multimodal
Refs: docs/格式转换/11-问题分级与修改路线图.md"
```

---

## 验收标准

| 验收项 | 标准 | 证据 |
|---|---|---|
| 官方文档已确认 | 访问官网并记录确认结果 | commit message 引用 |
| Fixture 已编写 | 至少 8 个测试用例 | glm_multimodal_test.go 文件存在 |
| 所有测试通过 | `go test ./internal/ir -run GLM` 全绿 | 测试输出截图或 CI 通过 |
| Extensions 验证 | 私有字段往返测试通过 | TestGLM_PrivateFields_* 通过 |
| 跨厂隔离验证 | GLM 字段不泄漏到其他厂商 | TestGLM_CrossVendorIsolation 通过 |
| 文档已更新 | 10/07 两个文档状态更新 | git diff 显示更新 |
| 符合门禁要求 | docs/格式转换/06 要求的场景全覆盖 | 测试用例清单对照 |
| 现有测试不受影响 | `go test ./internal/ir` 全部通过 | CI 通过 |

---

## 风险与约束

### 风险

1. **官方文档访问受限**：
   - 缓解：保存文档快照，标注访问时间
   - 备选：基于公开知识推断，明确标注 "unverified"

2. **多模态官方不支持**：
   - 缓解：测试用 `t.Skip()` 标注，文档更新为 "unsupported"
   - 仍验证私有字段保留（与多模态无关）

3. **Extensions 机制发现 bug**：
   - 如发现 P0 bug（私有字段泄漏），立即升级优先级
   - 在当前 P1 修复中仅验证，不修改 ir_converter.go

### 约束

1. **不修改现有代码**：parse_openai.go, serialize_openai.go, ir_converter.go 保持不变
2. **测试独立可运行**：不依赖外部服务、网络请求、真实 API key
3. **遵循现有测试风格**：table-driven, require/assert, 清晰命名
4. **文档更新原子性**：测试 + 文档更新在同一 commit

---

## 后续任务

完成 P1-GLM 后，按优先级依次执行：
1. P1-MiniMax 多模态验证
2. P1-DeepSeek 多模态验证
3. P1-Qwen 多模态验证
4. P1-Doubao 多模态验证
5. P1-Ollama 多模态验证

每个 P1 独立设计 → 实施 → 验收，避免批量修改带来的风险。

---

## 设计审批

- [x] Section 1: 架构与范围
- [x] Section 2: 测试策略与 Fixture 设计
- [x] Section 3: 实施步骤与验收标准

**设计批准日期：** 2026-07-18  
**下一步：** 进入 writing-plans skill，生成详细实施计划

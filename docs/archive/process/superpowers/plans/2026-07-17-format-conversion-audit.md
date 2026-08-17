# 格式转换审计实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 下载 9 厂商 API 规范、整合会话优化文档、编写现状确认与问题分级路线图，形成格式转换审计 SSOT

**Architecture:** 基于现有 docs/格式转换/ 的 8 个文档，增量补充 3 个新文档 + 厂家数据格式/ 子目录。不修改任何代码、不动 IR 保留字段、不重写现有文档。

**Tech Stack:** Markdown 文档、WebFetch（原厂规范下载）、Git

---

## 文件结构

### 新增文件
```
docs/格式转换/
├── 厂家数据格式/                            ← 新增目录
│   ├── README.md                           ← 新增
│   ├── openai/README.md + 2 规范文件        ← 新增
│   ├── anthropic/README.md + 1 规范文件     ← 新增
│   ├── gemini/README.md + 1 规范文件        ← 新增
│   ├── glm/README.md + 1 规范文件           ← 新增
│   ├── minimax/README.md + 1 规范文件       ← 新增
│   ├── deepseek/README.md + 1 规范文件      ← 新增
│   ├── qwen/README.md + 1 规范文件          ← 新增
│   ├── ollama/README.md + 1 规范文件        ← 新增
│   └── doubao/README.md + 1 规范文件        ← 新增
├── 09-会话优化集成与统一转换标准.md          ← 新增
├── 10-现状确认与原厂对比.md                 ← 新增
├── 11-问题分级与修改路线图.md               ← 新增
└── README.md                               ← 修改（增量追加导航）
```

### 不修改的文件
- docs/格式转换/01-08 (现有 8 个文档)
- docs/会话优化v2/*.md (9 个文档保持原样)
- internal/ir/types.go, parse_*.go, serialize_*.go
- domains/transformation/*.go, domains/streaming/*.go

---

## Task 1: 创建目录骨架

**Files:**
- Create: `docs/格式转换/厂家数据格式/`
- Create: `docs/格式转换/厂家数据格式/{openai,anthropic,gemini,glm,minimax,deepseek,qwen,ollama,doubao}/`

- [ ] **Step 1: 创建厂家数据格式根目录**

```bash
mkdir -p docs/格式转换/厂家数据格式
```

- [ ] **Step 2: 创建 9 个厂商子目录**

```bash
cd docs/格式转换/厂家数据格式
mkdir -p openai anthropic gemini glm minimax deepseek qwen ollama doubao
```

- [ ] **Step 3: 验证目录结构**

Run: `ls -la docs/格式转换/厂家数据格式/`
Expected: 显示 9 个子目录

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/
git commit -m "docs(format-conversion): create vendor spec directory structure"
```

---

## Task 2: 下载 OpenAI 规范并创建 README

**Files:**
- Create: `docs/格式转换/厂家数据格式/openai/README.md`
- Create: `docs/格式转换/厂家数据格式/openai/chat-completions.md`
- Create: `docs/格式转换/厂家数据格式/openai/responses.md`

- [ ] **Step 1: 创建 OpenAI README**

```markdown
# OpenAI API 规范

## 下载信息

- **来源**: https://platform.openai.com/docs/api-reference/
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出
- **版本**: 当前最新版本（访问时）
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `chat-completions.md`: Chat Completions API 规范
- `responses.md`: Responses API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
```

保存到: `docs/格式转换/厂家数据格式/openai/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 Chat Completions 规范**

使用 WebFetch tool 获取 https://platform.openai.com/docs/api-reference/chat/create，提取以下内容：
- 请求字段列表（messages, model, temperature, tools, tool_choice, etc.）
- 响应结构（choices, usage, finish_reason, etc.）
- 多模态字段（image_url, input_audio, etc.）
- 工具调用格式（tool_calls, tool_call_id, etc.）

保存为: `docs/格式转换/厂家数据格式/openai/chat-completions.md`

- [ ] **Step 3: 使用 WebFetch 下载 Responses 规范**

使用 WebFetch tool 获取 https://platform.openai.com/docs/api-reference/responses，提取以下内容：
- input items 格式（input_text, function_call, function_call_output, etc.）
- output items 格式
- 工具调用关联（call_id, previous_response_id, etc.）

保存为: `docs/格式转换/厂家数据格式/openai/responses.md`

- [ ] **Step 4: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/openai/`
Expected: README.md, chat-completions.md, responses.md

- [ ] **Step 5: Commit**

```bash
git add docs/格式转换/厂家数据格式/openai/
git commit -m "docs(format-conversion): add OpenAI API specs (Chat + Responses)"
```

---

## Task 3: 下载 Anthropic 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/anthropic/README.md`
- Create: `docs/格式转换/厂家数据格式/anthropic/messages.md`

- [ ] **Step 1: 创建 Anthropic README**

```markdown
# Anthropic API 规范

## 下载信息

- **来源**: https://docs.anthropic.com/en/api/messages
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `messages.md`: Messages API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
```

保存到: `docs/格式转换/厂家数据格式/anthropic/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 Messages 规范**

使用 WebFetch tool 获取 https://docs.anthropic.com/en/api/messages，提取以下内容：
- 请求字段（model, messages, system, tools, max_tokens, temperature, etc.）
- content blocks（text, image, document, tool_use, tool_result, thinking, etc.）
- 工具调用格式（tool_use_id, tool_result, etc.）
- 多模态支持（image source, document block, etc.）

保存为: `docs/格式转换/厂家数据格式/anthropic/messages.md`

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/anthropic/`
Expected: README.md, messages.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/anthropic/
git commit -m "docs(format-conversion): add Anthropic Messages API spec"
```

---

## Task 4: 下载 Gemini 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/gemini/README.md`
- Create: `docs/格式转换/厂家数据格式/gemini/generate-content.md`

- [ ] **Step 1: 创建 Gemini README**

```markdown
# Google Gemini API 规范

## 下载信息

- **来源**: https://ai.google.dev/api/generate-content
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `generate-content.md`: generateContent API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
```

保存到: `docs/格式转换/厂家数据格式/gemini/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 generateContent 规范**

使用 WebFetch tool 获取 https://ai.google.dev/api/generate-content，提取以下内容：
- 请求字段（model, contents, systemInstruction, tools, generationConfig, etc.）
- contents parts（text, inlineData, fileData, functionCall, functionResponse, etc.）
- 工具调用格式（functionCall, functionResponse, etc.）
- 多模态支持（inlineData, fileData, MIME types, etc.）

保存为: `docs/格式转换/厂家数据格式/gemini/generate-content.md`

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/gemini/`
Expected: README.md, generate-content.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/gemini/
git commit -m "docs(format-conversion): add Gemini generateContent API spec"
```

---

## Task 5: 下载 GLM 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/glm/README.md`
- Create: `docs/格式转换/厂家数据格式/glm/glm-api.md`

- [ ] **Step 1: 创建 GLM README**

```markdown
# GLM (智谱) API 规范

## 下载信息

- **来源**: https://open.bigmodel.cn/dev/api
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出（需确认访问权限）
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `glm-api.md`: GLM Chat Completion API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
GLM 使用 OpenAI-compatible 格式，但有私有字段扩展（retrieval, web_search, etc.）
```

保存到: `docs/格式转换/厂家数据格式/glm/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 GLM 规范**

使用 WebFetch tool 获取 https://open.bigmodel.cn/dev/api，提取以下内容：
- OpenAI-compatible 字段（messages, model, tools, etc.）
- GLM 私有字段（retrieval, web_search, etc.）
- 多模态支持范围
- 工具调用格式

保存为: `docs/格式转换/厂家数据格式/glm/glm-api.md`

如访问受限，在 README 中标注"访问受限，待补充实际规范"，并从现有代码推断字段列表。

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/glm/`
Expected: README.md, glm-api.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/glm/
git commit -m "docs(format-conversion): add GLM API spec"
```

---

## Task 6: 下载 MiniMax 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/minimax/README.md`
- Create: `docs/格式转换/厂家数据格式/minimax/minimax-api.md`

- [ ] **Step 1: 创建 MiniMax README**

```markdown
# MiniMax API 规范

## 下载信息

- **来源**: https://platform.minimaxi.com/docs
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出（需确认访问权限）
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `minimax-api.md`: MiniMax Chat Completion API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
MiniMax 支持 OpenAI-compatible 和 Anthropic-compatible 两条路径。
```

保存到: `docs/格式转换/厂家数据格式/minimax/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 MiniMax 规范**

使用 WebFetch tool 获取 https://platform.minimaxi.com/docs，提取以下内容：
- OpenAI-compatible 路径字段
- Anthropic-compatible 路径字段（tool_call_id vs tool_use_id）
- MiniMax 私有字段（bot_setting, reply_constraints, etc.）
- 多模态支持范围
- 工具调用格式

保存为: `docs/格式转换/厂家数据格式/minimax/minimax-api.md`

如访问受限，在 README 中标注"访问受限，待补充实际规范"，并从现有代码 + docs/格式转换/08 推断。

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/minimax/`
Expected: README.md, minimax-api.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/minimax/
git commit -m "docs(format-conversion): add MiniMax API spec"
```

---

## Task 7: 下载 DeepSeek 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/deepseek/README.md`
- Create: `docs/格式转换/厂家数据格式/deepseek/deepseek-chat.md`

- [ ] **Step 1: 创建 DeepSeek README**

```markdown
# DeepSeek API 规范

## 下载信息

- **来源**: https://api-docs.deepseek.com/api/create-chat-completion
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `deepseek-chat.md`: DeepSeek Chat Completion API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
DeepSeek 使用 OpenAI-compatible 格式，但有 reasoning 扩展。
```

保存到: `docs/格式转换/厂家数据格式/deepseek/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 DeepSeek 规范**

使用 WebFetch tool 获取 https://api-docs.deepseek.com/api/create-chat-completion，提取以下内容：
- OpenAI-compatible 字段
- DeepSeek reasoning 扩展字段
- 多模态支持范围
- 工具调用格式

保存为: `docs/格式转换/厂家数据格式/deepseek/deepseek-chat.md`

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/deepseek/`
Expected: README.md, deepseek-chat.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/deepseek/
git commit -m "docs(format-conversion): add DeepSeek API spec"
```

---

## Task 8: 下载 Qwen 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/qwen/README.md`
- Create: `docs/格式转换/厂家数据格式/qwen/dashscope-api.md`

- [ ] **Step 1: 创建 Qwen README**

```markdown
# Qwen/DashScope API 规范

## 下载信息

- **来源**: https://help.aliyun.com/zh/model-studio/developer-reference/
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出（需确认访问权限）
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `dashscope-api.md`: DashScope API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
Qwen 使用 OpenAI-compatible + 原生 DashScope 两条路径。
```

保存到: `docs/格式转换/厂家数据格式/qwen/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 Qwen 规范**

使用 WebFetch tool 获取 https://help.aliyun.com/zh/model-studio/developer-reference/，提取以下内容：
- OpenAI-compatible 路径字段
- DashScope 原生 endpoint 字段
- 多模态支持范围（image/audio/video shape）
- 工具调用格式

保存为: `docs/格式转换/厂家数据格式/qwen/dashscope-api.md`

如访问受限，在 README 中标注"访问受限，待补充实际规范"。

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/qwen/`
Expected: README.md, dashscope-api.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/qwen/
git commit -m "docs(format-conversion): add Qwen/DashScope API spec"
```

---

## Task 9: 下载 Ollama 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/ollama/README.md`
- Create: `docs/格式转换/厂家数据格式/ollama/ollama-chat.md`

- [ ] **Step 1: 创建 Ollama README**

```markdown
# Ollama API 规范

## 下载信息

- **来源**: https://github.com/ollama/ollama/blob/main/docs/api.md
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / GitHub 导出
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `ollama-chat.md`: Ollama Chat API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
Ollama 支持 OpenAI-compatible 和 原生 `/api/chat` 两条路径。
```

保存到: `docs/格式转换/厂家数据格式/ollama/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 Ollama 规范**

使用 WebFetch tool 获取 https://github.com/ollama/ollama/blob/main/docs/api.md，提取以下内容：
- OpenAI-compatible 路径字段
- 原生 `/api/chat` 字段
- 多模态支持范围（model-dependent）
- 工具调用格式

保存为: `docs/格式转换/厂家数据格式/ollama/ollama-chat.md`

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/ollama/`
Expected: README.md, ollama-chat.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/ollama/
git commit -m "docs(format-conversion): add Ollama API spec"
```

---

## Task 10: 下载 Doubao 规范

**Files:**
- Create: `docs/格式转换/厂家数据格式/doubao/README.md`
- Create: `docs/格式转换/厂家数据格式/doubao/volcengine-api.md`

- [ ] **Step 1: 创建 Doubao README**

```markdown
# Doubao/Volcengine API 规范

## 下载信息

- **来源**: https://www.volcengine.com/docs/82379 (需确认访问权限)
- **下载时间**: 2026-07-18
- **抓取方式**: WebFetch / 浏览器导出（需确认访问权限）
- **版本**: 当前最新版本
- **脱敏**: 无需脱敏（公开文档）

## 规范文件

- `volcengine-api.md`: Volcengine Chat API 规范

## 引用说明

本目录规范作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线。
Doubao 使用 OpenAI-compatible 格式，但有 plugins、bot_id 等私有字段。
```

保存到: `docs/格式转换/厂家数据格式/doubao/README.md`

- [ ] **Step 2: 使用 WebFetch 下载 Doubao 规范**

使用 WebFetch tool 获取 https://www.volcengine.com/docs/82379，提取以下内容：
- OpenAI-compatible 路径字段
- Doubao 私有字段（plugins, bot_id, etc.）
- 多模态支持范围
- 工具调用格式

保存为: `docs/格式转换/厂家数据格式/doubao/volcengine-api.md`

如访问受限，在 README 中标注"访问受限，待补充实际规范"，并从现有代码推断。

- [ ] **Step 3: 验证文件创建**

Run: `ls -la docs/格式转换/厂家数据格式/doubao/`
Expected: README.md, volcengine-api.md

- [ ] **Step 4: Commit**

```bash
git add docs/格式转换/厂家数据格式/doubao/
git commit -m "docs(format-conversion): add Doubao/Volcengine API spec"
```

---

## Task 11: 创建厂家数据格式根 README

**Files:**
- Create: `docs/格式转换/厂家数据格式/README.md`

- [ ] **Step 1: 编写根 README**


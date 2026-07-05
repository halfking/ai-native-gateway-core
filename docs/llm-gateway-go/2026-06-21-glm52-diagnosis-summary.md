# GLM-5.2 格式转换问题诊断总结

## 📋 问题描述

用户报告通过 `__DOMAIN_2__/v1` 调用 `glm-5.2` 模型时，整个请求混乱。

## 🔍 已完成的分析

### 1. 架构理解

llm-gateway-go 使用 **四象限路由模型**：

| 象限 | 客户端 | 上游 | 转换 | 示例 |
|------|--------|------|------|------|
| Q1 | OpenAI | OpenAI | 无 | gpt-4o |
| Q2 | Anthropic | OpenAI | A→O | 较少 |
| Q3 | OpenAI | Anthropic | O→A→O | **glm-5.2** |
| Q4 | Anthropic | Anthropic | 无 | claude-3.5 |

**glm-5.2 走 Q3 路径**：
1. 客户端发送 OpenAI 格式 → Gateway 转 Anthropic 格式
2. 上游返回 Anthropic 格式 → Gateway 转回 OpenAI 格式

### 2. 代码中已知的问题

在 `relay/anthropic_to_openai_stream.go:298-344` 中发现：

```go
// 2026-06-21 fix: Some anthropic-compatible upstreams (notably
// glm-5.2-oneday at https://api.supxh.xin) leak OpenAI-format
// chunks into the Anthropic SSE stream.
```

**代码已有防护**：
- ✅ 过滤未知 Anthropic 事件类型
- ✅ 检测并跳过 OpenAI 格式泄漏
- ✅ 检测空 choices 数组

### 3. 可能的根因

#### 根因 A：上游混合格式（最可能）
- **症状**：glm-5.2 上游在 Anthropic 端点返回混合了 OpenAI 格式的数据
- **证据**：代码注释明确提到 `glm-5.2-oneday`
- **影响**：即使有防护，边界情况可能绕过

#### 根因 B：请求转换不完整
- **位置**：`relay/chat_to_anthropic.go`
- **风险**：messages 转换可能产生空消息

#### 根因 C：响应转换边界处理
- **位置**：`relay/anthropic_to_openai_stream.go`
- **风险**：缓冲区处理混合格式数据时可能出错

#### 根因 D：防护代码漏洞
- **空数组检测**：`if oaiCheck.Choices != nil` 应该能检测到 `[]`
- **但是**：如果 JSON 格式微妙变化（如嵌套结构）可能绕过

## 🛠️ 已创建的工具

### 1. 诊断文档
📄 `docs/llm-gateway-go/2026-06-21-glm52-format-issue-diagnosis.md`

包含：
- 完整问题分析
- 三个修复方案（短期/中期/长期）
- 测试验证清单
- 复现脚本

### 2. 集成测试
📄 `tests/integration/glm52_debug_test.go`

包含三个测试：
- `TestGLM52RealRequest` - 端到端真实请求
- `TestGLM52FormatConversion` - 格式转换单元测试
- `TestGLM52StreamEventParsing` - SSE 事件解析测试

### 3. 诊断脚本
📄 `scripts/diagnose-glm52.sh`

功能：
- 非流式请求测试
- 流式请求测试（检测空 choices、混合格式）
- 统计分析
- 彩色输出

## 🚀 下一步行动

### 立即执行（需要用户提供 API Key）

```bash
cd __LOCAL_PATH_1__

# 方式 1: 使用环境变量
export GLM_API_KEY="your-actual-api-key"
./scripts/diagnose-glm52.sh -v

# 方式 2: 直接传参
./scripts/diagnose-glm52.sh -k "your-actual-api-key" -v

# 方式 3: 运行集成测试
go test -tags=integration ./tests/integration -v -run TestGLM52
```

### 收集日志（在 71 服务器）

```bash
# SSH 到 71 服务器
ssh __SSH_TARGET_2__

# 查看实时日志
docker logs -f llm-gateway-go | grep -E "glm-5\.2|anthropic_to_openai|chat_to_anthropic"

# 或查看最近 100 行
docker logs llm-gateway-go --tail 100 | grep -E "glm-5\.2|anthropic_to_openai"
```

### 真实请求测试

```bash
# 使用真实 API Key 测试
curl -X POST https://__DOMAIN_2__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <your-key>" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "Say hello"}],
    "max_tokens": 50,
    "stream": true
  }' | head -30
```

## 📊 预期结果

### 如果问题存在，应该看到：

1. **非流式请求**：
   - `choices` 数组为空
   - 或返回错误

2. **流式请求**：
   - 出现 `data: {"choices":[]}` 空块
   - 或混合 Anthropic 格式事件
   - 或无效 JSON

3. **日志中**：
   - `dropping non-Anthropic event from upstream`
   - `upstream sent OpenAI-format chunk, skipping`
   - `chat_to_anthropic_conversion_error`

### 如果问题已修复，应该看到：

1. **非流式请求**：
   - HTTP 200
   - `choices` 数组有内容
   - `message.content` 有文本

2. **流式请求**：
   - 所有块都有有效的 `choices[0].delta`
   - 以 `data: [DONE]` 结束
   - 无空 choices 块

## 🔧 修复方案概要

### 方案 A：加强防护（推荐先尝试）

在 SSE 解析前添加粗筛：

```go
// 在解析 JSON 前检查
if strings.Contains(string(data), `"choices"`) {
    // 这是 OpenAI 格式，不应该出现在 Anthropic 流中
    slog.Warn("dropping OpenAI-format data")
    continue
}
```

**优点**：快速、低风险
**实施时间**：1 天

### 方案 B：切换到 Q1 路径

如果 glm-5.2 支持 OpenAI 协议，直接配置为 Q1：

```sql
-- 修改 provider 配置
UPDATE credentials SET protocol = 'openai-completions' WHERE model = 'glm-5.2';
```

**优点**：避免转换，性能更好
**前提**：需确认上游支持

### 方案 C：完善转换逻辑

添加完整的验证和错误处理：
- 请求转换后验证
- 响应转换增强错误恢复
- SSE 流每个 chunk 验证

**优点**：长期稳定
**实施时间**：1-2 周

## 📝 需要用户提供的信息

1. **API Key** - 用于运行真实测试
2. **错误现象描述** - 具体看到什么样的"混乱"？
   - 客户端报什么错？
   - 返回的数据是什么样的？
   - 是每次都发生还是偶尔发生？

3. **使用场景** - 
   - 是通过什么客户端调用的？（curl / SDK / UI）
   - 是流式还是非流式？
   - 有多轮对话吗？

## 📂 文件清单

已创建的文件：

```
services/llm-gateway-go/
├── docs/llm-gateway-go/
│   └── 2026-06-21-glm52-format-issue-diagnosis.md  # 完整诊断报告
├── scripts/
│   └── diagnose-glm52.sh                           # 诊断脚本
└── tests/integration/
    └── glm52_debug_test.go                         # 集成测试
```

## 🎯 关键发现

1. ✅ **代码已经识别了 glm-5.2 的混合格式问题**
2. ✅ **已有防护代码在位**
3. ⚠️ **需要实际测试验证防护是否有效**
4. ⚠️ **可能存在边界情况绕过防护**

## 🔗 相关代码位置

- `relay/chat_to_anthropic.go:28-101` - OpenAI → Anthropic 转换
- `relay/anthropic_to_openai_stream.go:298-344` - 混合格式防护
- `relay/anthropic_to_openai_stream.go:559-581` - 事件类型验证
- `tests/integration/quadrants_test.go:185-286` - Q3 路径测试参考

---

**准备就绪，等待用户提供 API Key 开始实际测试！**

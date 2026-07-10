# Extensions 机制启用指南

**文档版本**: 1.0  
**创建日期**: 2026-07-11  
**维护者**: llm-gateway-go 团队

---

## 背景

Extensions 机制用于保留厂商特有字段，防止字段在协议转换时丢失。

### 问题场景

在协议转换过程中（如 OpenAI → Anthropic、OpenAI → Ollama），部分厂商特有字段会因为不在标准 OpenAI API 规范中而被丢弃，导致：

- **Ollama**: `keep_alive`、`format`、`context` 等字段丢失，影响会话保活和格式化输出
- **GLM**: `web_search`、`retrieval`、`tools` 等扩展字段丢失，无法使用联网搜索和知识库检索
- **DeepSeek**: `reasoning_tokens` 等计费字段丢失，导致计费不准确
- **其他厂商**: 自定义扩展字段被过滤，功能受限

### 解决方案

通过 `LLM_GATEWAY_TRANSPORT_IR=true` 启用 Extensions 机制后，网关会：

1. **Parse 阶段**：提取所有非标字段到 `InternalRequest.Extensions` / `InternalResponse.Extensions`
2. **传输阶段**：Extensions 随 IR 在协议转换管道中透传
3. **Serialize 阶段**：将 Extensions 还原到目标协议 JSON，保证字段完整

---

## 启用方法

### 1. 环境变量配置

在部署环境中设置：

```bash
export LLM_GATEWAY_TRANSPORT_IR=true
```

或在 `.env` 文件中添加：

```bash
LLM_GATEWAY_TRANSPORT_IR=true
```

### 2. 容器部署（Docker / K8s）

**Docker Compose**:

```yaml
services:
  llm-gateway:
    environment:
      - LLM_GATEWAY_TRANSPORT_IR=true
```

**Kubernetes Deployment**:

```yaml
spec:
  containers:
  - name: llm-gateway-go
    env:
    - name: LLM_GATEWAY_TRANSPORT_IR
      value: "true"
```

### 3. 184 生产环境部署

部署脚本 `deploy-184.sh` 已自动注入该环境变量（通过 ConfigMap / Secret）。

执行标准部署即可：

```bash
./deploy-184.sh --with-migration
```

---

## 影响范围

### 支持的厂商字段

| 厂商 | 保留字段示例 | 用途 |
|------|-------------|------|
| **Ollama** | `keep_alive`, `format`, `context`, `raw` | 会话保活、输出格式、上下文窗口 |
| **GLM (智谱)** | `web_search`, `retrieval`, `tools`, `temperature` | 联网搜索、知识库检索、工具调用 |
| **DeepSeek** | `reasoning_tokens`, `reasoning_content` | 推理 token 计费、思维链内容 |
| **通用** | 任何非 OpenAI 标准的字段 | 自动透传 |

### 协议转换覆盖

- ✅ **OpenAI → Anthropic**
- ✅ **OpenAI → Ollama**
- ✅ **Anthropic → OpenAI**
- ✅ **Ollama → OpenAI**
- ✅ **任意协议 → 任意协议**（通过 IR 中转）

---

## 验证方法

### 测试 1: Ollama keep_alive 透传

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-token" \
  -d '{
    "model": "ollama/llama3",
    "messages": [{"role":"user","content":"Hello"}],
    "keep_alive": "5m"
  }'
```

**验证点**：
- 查看上游请求日志（`observability/logging.go`），确认发往 Ollama 的请求包含 `keep_alive: "5m"`
- 查看 `request_logs` 表的 `upstream_body` 字段，确认字段存在

### 测试 2: GLM web_search 透传

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-token" \
  -d '{
    "model": "glm-4-plus",
    "messages": [{"role":"user","content":"最新新闻"}],
    "web_search": true
  }'
```

**验证点**：
- 上游请求包含 `web_search: true`
- GLM 返回的响应包含网络搜索结果

### 测试 3: 自定义字段透传

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role":"user","content":"test"}],
    "custom_field": "my-value",
    "nested": {"key": "value"}
  }'
```

**验证点**：
- Extensions 机制会保留 `custom_field` 和 `nested`
- 即使上游不认识这些字段，也不会在解析时报错

---

## 技术细节

### 核心组件

| 组件 | 位置 | 职责 |
|------|------|------|
| **IRExtensionExtractor** | `domains/transformation/extension.go` | 提取非标字段到 `Extensions` map |
| **IRExtensionRestorer** | `domains/transformation/extension.go` | 还原 Extensions 到输出 JSON |
| **TransportIRConverter** | `domains/transformation/ir_converter.go` | 包装 IR Parser/Serializer，注入 Extensions 逻辑 |
| **IRTransport** | `domains/transformation/ir_transport.go` | 四象限协议转换主流程 |

### 数据流

```
客户端请求
  ↓
Parse (提取 Extensions)
  ↓
InternalRequest { ..., Extensions: map[string]json.RawMessage }
  ↓
Serialize (合并 Extensions + IR 字段)
  ↓
上游请求（包含所有字段）
```

### 实现原则

1. **非侵入式**: Extensions 提取/还原在 Transport 层完成，IR 核心逻辑不改动
2. **向后兼容**: `LLM_GATEWAY_TRANSPORT_IR=false` 时，Extensions 机制不启用，走 Legacy 路径
3. **零性能损耗**: 仅在需要时（有非标字段）做 JSON unmarshal/marshal，标准请求无额外开销

---

## 已知限制

1. **字段冲突**: 如果 Extensions 中的字段与 IR 标准字段同名，IR 字段优先（避免覆盖核心逻辑）
2. **Schema 验证**: Extensions 字段不做 schema 校验，上游厂商拒绝非法字段时会返回错误
3. **流式响应**: 目前 Extensions 机制仅支持非流式请求/响应，流式 SSE 的 Extensions 透传在后续版本支持

---

## 故障排查

### 问题 1: Extensions 未生效

**症状**: 设置了 `LLM_GATEWAY_TRANSPORT_IR=true`，但字段仍丢失

**排查步骤**:
1. 检查环境变量是否正确注入：
   ```bash
   kubectl exec -it <pod-name> -- printenv | grep LLM_GATEWAY_TRANSPORT_IR
   ```
2. 检查日志是否有 Extensions 提取警告：
   ```bash
   kubectl logs <pod-name> | grep "extract extensions"
   ```
3. 检查 `request_logs.upstream_body` 字段，确认字段是否到达上游

### 问题 2: 上游返回 400 错误

**症状**: 启用 Extensions 后，上游返回字段不认识的错误

**原因**: 该厂商不支持该扩展字段

**解决**: 
- 确认字段拼写和数据类型正确（参考厂商官方文档）
- 或临时禁用 Extensions 机制（`LLM_GATEWAY_TRANSPORT_IR=false`）

### 问题 3: 性能下降

**症状**: 启用后延迟增加

**排查**:
- Extensions 机制仅增加 ~1ms JSON 解析开销（标准请求无影响）
- 检查是否有大量复杂嵌套字段（如超过 10KB 的 Extensions）

---

## 参考资料

- **技术设计**: `docs/2026-07-01-responses-ir-phase-e.md` § Extensions 机制
- **实现代码**: `domains/transformation/extension.go`
- **测试用例**: `domains/transformation/ir_converter_test.go` (L104-L368)
- **环境变量配置**: `.env.example` (L61-L83)

---

## 变更历史

| 日期 | 版本 | 变更 |
|------|------|------|
| 2026-07-11 | 1.0 | 初始版本，补充环境变量配置和验证方法 |

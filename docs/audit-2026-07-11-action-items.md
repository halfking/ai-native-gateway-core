# Protocol Compatibility Audit — Action Items

**Date:** 2026-07-11  
**Owner:** Backend Team + SRE  
**Tracking:** [Link to Project Management Tool]

---

## P0 任务（立即执行，1周内完成）

### P0-1: 默认启用 IR 转换

**负责人：** @backend-lead  
**工期：** 1 天  
**优先级：** 🔴 Critical

**任务描述：**
将 `LLM_GATEWAY_IR_CONVERTER=true` 设置为默认值，确保所有厂商的私有字段被保留。

**实施步骤：**
1. 修改 `k8s/deployment.yaml`:
   ```yaml
   env:
     - name: LLM_GATEWAY_IR_CONVERTER
       value: "true"
   ```
2. 在 `184` 和 `71` 环境同步部署
3. 验证 Extensions 字段在日志中可见

**验收标准：**
- [ ] `kubectl get pods -o yaml` 显示环境变量已设置
- [ ] 测试 GLM 知识库检索功能正常
- [ ] 测试 MiniMax 角色设定生效
- [ ] 日志中 `extensions_preserved=true`

**回滚方案：**
```bash
kubectl set env deployment/llm-gateway-go LLM_GATEWAY_IR_CONVERTER=false
```

**风险：** 低（IR 转换已有单元测试覆盖）

---

### P0-2: Gemini Streaming 协议转换

**负责人：** @backend-engineer-1  
**工期：** 2 天  
**优先级：** 🔴 Critical

**任务描述：**
实现 Gemini SSE 响应到 OpenAI SSE 格式的实时转换。

**实施步骤：**
1. 创建 `internal/ir/stream_gemini.go`:
   ```go
   func StreamGeminiToOpenAI(r io.Reader) (io.Reader, error) {
       scanner := bufio.NewScanner(r)
       pr, pw := io.Pipe()
       go func() {
           defer pw.Close()
           for scanner.Scan() {
               line := scanner.Text()
               if strings.HasPrefix(line, "data: ") {
                   geminiData := parseGeminiSSE(line[6:])
                   openaiData := convertToOpenAIChunk(geminiData)
                   pw.Write([]byte("data: " + openaiData + "\n\n"))
               }
           }
       }()
       return pr, nil
   }
   ```
2. 集成到 `domains/streaming/chat_executor.go`
3. 添加单元测试 `TestStreamGeminiToOpenAI`

**验收标准：**
- [ ] 单元测试通过
- [ ] 手动测试：curl 请求 Gemini 流式接口，客户端正确解析
- [ ] 日志中无 `SSE parse error`

**测试用例：**
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-pro",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

**风险：** 中（需要理解 Gemini SSE 格式差异）

---

### P0-3: GLM Streaming 协议转换

**负责人：** @backend-engineer-2  
**工期：** 2 天  
**优先级：** 🔴 Critical

**任务描述：**
实现 GLM SSE 响应到 OpenAI SSE 格式的实时转换。

**实施步骤：**
1. 创建 `internal/ir/stream_glm.go`
2. 处理 GLM 特有字段：
   - `choices[].delta.retrieval_results` → 忽略或转为 metadata
   - `choices[].finish_reason` → 标准化为 OpenAI 枚举值
3. 集成到 executor
4. 添加测试 `TestStreamGLMToOpenAI`

**验收标准：**
- [ ] 单元测试通过
- [ ] 手动测试：GLM 流式请求客户端正确解析
- [ ] 知识库检索结果不丢失（记录在日志或 metadata）

**测试用例：**
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-4",
    "messages": [{"role": "user", "content": "查询文档"}],
    "stream": true,
    "retrieval": {"enable": true}
  }'
```

**风险：** 中（GLM 返回格式文档不完整）

---

### P0-4: MiniMax 私有字段过滤

**负责人：** @backend-engineer-1  
**工期：** 1 天  
**优先级：** 🔴 Critical

**任务描述：**
在请求转发前过滤 MiniMax 的 `bot_setting` 字段，防止泄露到下游。

**实施步骤：**
1. 在 `internal/ir/serialize_openai.go` 添加过滤逻辑：
   ```go
   func SerializeOpenAI(req *InternalRequest) ([]byte, error) {
       // ... 现有逻辑
       
       // 移除 MiniMax 私有字段
       if upstreamProvider != "minimax" {
           delete(req.Extensions, "bot_setting")
           delete(req.Extensions, "reply_constraints")
       }
       
       // ...
   }
   ```
2. 添加测试 `TestMinimaxFieldFiltering`

**验收标准：**
- [ ] 单元测试通过
- [ ] MiniMax → OpenAI 转发不包含 `bot_setting`
- [ ] MiniMax → MiniMax 转发保留 `bot_setting`（原厂商内部）

**测试用例：**
```bash
# 测试：MiniMax 请求 → OpenAI 上游
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "X-Provider-Hint: openai" \
  -d '{
    "model": "gpt-4",
    "messages": [...],
    "bot_setting": [{"bot_name": "Test"}]
  }'

# 验证：OpenAI 不报错
```

**风险：** 低（纯过滤逻辑）

---

### P0-5: 监控面板上线

**负责人：** @sre-engineer  
**工期：** 1 天  
**优先级：** 🔴 Critical

**任务描述：**
在 Grafana 创建协议转换监控面板。

**实施步骤：**
1. 在代码中添加 Prometheus 指标：
   ```go
   var (
       protocolConversionTotal = prometheus.NewCounterVec(
           prometheus.CounterOpts{
               Name: "llm_gateway_protocol_conversion_total",
               Help: "Total protocol conversions by direction",
           },
           []string{"client_protocol", "upstream_protocol", "provider"},
       )
       
       extensionsPreservedTotal = prometheus.NewCounterVec(
           prometheus.CounterOpts{
               Name: "llm_gateway_extensions_preserved_total",
               Help: "Total extensions preserved",
           },
           []string{"provider"},
       )
   )
   ```
2. 创建 Grafana Dashboard JSON
3. 导入到生产环境

**验收标准：**
- [ ] Grafana 面板可访问：`https://grafana.kxpms.cn/d/llm-protocol`
- [ ] 显示 8 厂商的协议转换矩阵
- [ ] Extensions 保留率 Gauge 显示 > 95%

**Dashboard 配置：**
```json
{
  "title": "LLM Gateway - Protocol Compatibility",
  "panels": [
    {
      "title": "Protocol Conversion Matrix (5m)",
      "targets": [{
        "expr": "sum by (client_protocol, upstream_protocol) (rate(llm_gateway_protocol_conversion_total[5m]))"
      }]
    },
    {
      "title": "Extensions Preserved Rate",
      "targets": [{
        "expr": "sum(llm_gateway_extensions_preserved_total) / sum(llm_gateway_extensions_total) * 100"
      }]
    }
  ]
}
```

**风险：** 低（纯观测性）

---

## P1 任务（2周内完成）

### P1-1: MiniMax Streaming 协议转换

**负责人：** @backend-engineer-2  
**工期：** 2 天  
**优先级：** 🟡 High

**任务描述：**
实现 MiniMax SSE 响应到 OpenAI SSE 格式的实时转换。

**实施步骤：**
1. 创建 `internal/ir/stream_minimax.go`
2. 处理 MiniMax 特有字段：
   - `choices[].messages[].sender_type` → 转为 `role`
   - `choices[].messages[].sender_name` → 忽略或转为 metadata
3. 集成到 executor
4. 添加测试

**验收标准：**
- [ ] 单元测试通过
- [ ] 手动测试通过
- [ ] 端到端测试通过

**风险：** 中

---

### P1-2: Extensions 白名单机制

**负责人：** @backend-lead  
**工期：** 3 天  
**优先级：** 🟡 High

**任务描述：**
实现可配置的 Extensions 白名单，控制哪些私有字段可以穿透到下游。

**实施步骤：**
1. 创建配置文件 `config/extensions-whitelist.yaml`:
   ```yaml
   providers:
     minimax:
       allowed_fields:
         - bot_setting
         - reply_constraints
       upstream_providers:
         minimax: ["bot_setting", "reply_constraints"]
         openai: []  # 不允许任何字段
     
     glm:
       allowed_fields:
         - retrieval
         - web_search
       upstream_providers:
         glm: ["retrieval", "web_search"]
         anthropic: ["retrieval"]  # 部分允许
   ```
2. 实现加载逻辑 `internal/config/extensions.go`
3. 在序列化时应用白名单过滤
4. 添加配置验证测试

**验收标准：**
- [ ] 配置文件可热加载
- [ ] 白名单过滤生效
- [ ] 日志中记录被过滤的字段

**风险：** 中（配置复杂度）

---

### P1-3: 协议转换日志增强

**负责人：** @backend-engineer-1  
**工期：** 2 天  
**优先级：** 🟡 High

**任务描述：**
在请求日志中增加协议转换相关字段。

**实施步骤：**
1. 修改 `internal/models/request_log.go`:
   ```go
   type RequestLog struct {
       // ... 现有字段
       
       IREnabled           bool     `json:"ir_enabled"`
       ProtocolConversion  string   `json:"protocol_conversion"`  // "Q1", "Q2", "Q3", "Q4"
       ExtensionKeys       []string `json:"extension_keys"`
       ExtensionsDropped   int      `json:"extensions_dropped"`
   }
   ```
2. 在 executor 中填充这些字段
3. 更新 Citus 表结构（migration）

**验收标准：**
- [ ] 日志包含新字段
- [ ] Citus 表可查询
- [ ] 不影响现有日志解析

**风险：** 低（数据库 migration 需谨慎）

---

### P1-4: 自动化回归测试

**负责人：** @qa-engineer  
**工期：** 3 天  
**优先级：** 🟡 High

**任务描述：**
创建 8 厂商 × 2 模式 = 16 个端到端测试用例。

**测试矩阵：**
| 厂商 | Non-Stream | Stream |
|------|-----------|--------|
| OpenAI | ✅ | ✅ |
| Anthropic | ✅ | ✅ |
| Gemini | ✅ | ✅ |
| GLM | ✅ | ✅ |
| MiniMax | ✅ | ✅ |
| DeepSeek | ✅ | ✅ |
| Doubao | ✅ | ✅ |
| Ollama | ✅ | ✅ |

**实施步骤：**
1. 创建 `tests/e2e/protocol_conversion_test.go`
2. 使用 mock upstream 服务器
3. 验证响应格式正确性
4. 集成到 CI/CD

**验收标准：**
- [ ] 16 个测试用例全部通过
- [ ] CI/CD 自动运行
- [ ] 测试覆盖率 > 80%

**风险：** 低（测试基础设施已就绪）

---

### P1-5: 告警规则配置

**负责人：** @sre-engineer  
**工期：** 1 天  
**优先级：** 🟡 High

**任务描述：**
配置 Prometheus 告警规则，监控协议转换健康度。

**告警规则：**
```yaml
groups:
  - name: llm_gateway_protocol
    rules:
      - alert: ExtensionsDropRateHigh
        expr: |
          (sum(rate(llm_gateway_extensions_dropped_total[5m])) 
           / sum(rate(llm_gateway_extensions_total[5m]))) > 0.05
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Extensions 丢失率超过 5%"
      
      - alert: StreamingConversionFailureHigh
        expr: |
          sum by (provider) (rate(llm_gateway_streaming_errors_total[5m])) 
          / sum by (provider) (rate(llm_gateway_streaming_requests_total[5m])) > 0.01
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "{{ $labels.provider }} Streaming 转换失败率超过 1%"
```

**验收标准：**
- [ ] 告警规则已加载到 Prometheus
- [ ] 测试触发告警（手动注入错误）
- [ ] 钉钉/飞书通知正常

**风险：** 低

---

## P2 任务（1月内完成）

### P2-1: 插件化架构重构

**负责人：** @backend-architect  
**工期：** 10 天  
**优先级：** 🟢 Medium

**任务描述：**
重构协议转换层为插件化架构，支持动态注册新厂商。

**设计方案：**
```go
// internal/providers/plugin.go
type ProviderPlugin interface {
    Name() string
    Protocol() string
    ParseRequest([]byte) (*ir.InternalRequest, error)
    SerializeRequest(*ir.InternalRequest) ([]byte, error)
    ParseResponse([]byte) (*ir.InternalResponse, error)
    SerializeResponse(*ir.InternalResponse) ([]byte, error)
    StreamTransform(io.Reader, string) (io.Reader, error)
}

// 注册机制
func RegisterProvider(plugin ProviderPlugin) {
    registry[plugin.Name()] = plugin
}

// 使用
func init() {
    RegisterProvider(&OpenAIPlugin{})
    RegisterProvider(&AnthropicPlugin{})
    RegisterProvider(&GeminiPlugin{})
    // ...
}
```

**实施步骤：**
1. 定义接口 `internal/providers/plugin.go`
2. 重构现有代码为插件实现
3. 迁移测试
4. 保持向后兼容

**验收标准：**
- [ ] 现有 8 厂商全部迁移到插件
- [ ] 新增测试厂商（Cohere）验证扩展性
- [ ] 性能无回退（benchmark 验证）

**风险：** 高（大规模重构）

---

### P2-2: 动态协议注册

**负责人：** @backend-architect  
**工期：** 5 天  
**优先级：** 🟢 Medium  
**依赖：** P2-1

**任务描述：**
支持运行时加载 provider plugin（Go plugin 或 WASM）。

**实施步骤：**
1. 使用 Go `plugin` 包或 WASM 运行时
2. 实现插件加载器
3. 添加插件验证机制
4. 文档化插件开发指南

**验收标准：**
- [ ] 可从外部 `.so` 文件加载插件
- [ ] 无需重启服务即可加载新厂商
- [ ] 插件隔离性（崩溃不影响主进程）

**风险：** 高（Go plugin 稳定性问题）

---

### P2-3: 性能基准测试

**负责人：** @qa-engineer  
**工期：** 3 天  
**优先级：** 🟢 Medium

**任务描述：**
建立协议转换性能基准，确保优化不引入性能回退。

**测试指标：**
- IR 解析耗时 (p50, p99)
- IR 序列化耗时 (p50, p99)
- Streaming 转换吞吐量 (MB/s)
- 内存占用 (RSS)

**验收标准：**
- [ ] 基准测试可重复运行
- [ ] 转换开销 < 5ms (p99)
- [ ] 内存占用 < 10MB per request

**风险：** 低

---

### P2-4: 文档完善

**负责人：** @tech-writer  
**工期：** 3 天  
**优先级：** 🟢 Medium

**任务描述：**
编写厂商接入指南和运维手册。

**文档清单：**
- [ ] 新增厂商接入指南 (`docs/provider-integration-guide.md`)
- [ ] 协议转换故障排查手册 (`docs/troubleshooting-protocol.md`)
- [ ] Extensions 配置参考 (`docs/extensions-reference.md`)
- [ ] API 文档更新

**验收标准：**
- [ ] 文档可通过 Markdown linter
- [ ] 包含完整代码示例
- [ ] 在线文档部署

**风险：** 低

---

### P2-5: Legacy 代码清理

**负责人：** @backend-engineer-1  
**工期：** 5 天  
**优先级：** 🟢 Medium  
**依赖：** P2-1

**任务描述：**
删除 `_to-be-deprecated/` 目录和 legacy callbacks。

**实施步骤：**
1. 确认所有调用方已迁移到 IR 或新插件
2. 删除目录
3. 清理 import 引用
4. 更新 CI/CD

**验收标准：**
- [ ] `_to-be-deprecated/` 目录不存在
- [ ] 所有测试通过
- [ ] 代码行数减少 > 1000 行

**风险：** 中（可能有隐藏依赖）

---

## 进度跟踪

### Week 1 (2026-07-11 → 2026-07-18)

| 日期 | 任务 | 状态 | 备注 |
|------|------|------|------|
| 07-11 | P0-1: IR默认启用 | ✅ 完成 | commit `3ccc3c468` |
| 07-11 | P0-2: GLM/Doubao/DeepSeek 抓包验证 | ✅ 完成 | 252 生产数据 → zhipu 524+MiniMax 16K 真实字段已纳入 stripper；doubao/deepseek 生产 0 调用，待启用后回归 |
| 07-12 | P0-3: Gemini Stream | ⏳ 待开始 | |
| 07-12 | P0-4: GLM Stream | ⏳ 待开始 | 并行 |
| 07-14 | P0-5: MiniMax过滤 | ⏳ 待开始 | 已基于真实数据扩展（含嵌套字段） |
| 07-15 | P0-6: 监控面板 | ⏳ 待开始 | |
| 07-18 | **P0 Milestone** | ⏳ 待完成 | 所有 P0 任务完成 |

### Week 2 (2026-07-18 → 2026-07-25)

| 日期 | 任务 | 状态 | 备注 |
|------|------|------|------|
| 07-18 | P1-1: MiniMax Stream | ⏳ 待开始 | |
| 07-21 | P1-2: Extensions白名单 | ⏳ 待开始 | |
| 07-21 | P1-3: 日志增强 | ⏳ 待开始 | 并行 |
| 07-23 | P1-4: 回归测试 | ⏳ 待开始 | |
| 07-25 | P1-5: 告警规则 | ⏳ 待开始 | |
| 07-25 | **P1 Milestone** | ⏳ 待完成 | 监控覆盖率 100% |

### Week 3-4 (2026-07-25 → 2026-08-08)

| 日期 | 任务 | 状态 | 备注 |
|------|------|------|------|
| 07-25 | P2-1: 插件化重构 | ⏳ 待开始 | 10天 |
| 08-05 | P2-2: 动态注册 | ⏳ 待开始 | 依赖 P2-1 |
| 08-05 | P2-3: 性能测试 | ⏳ 待开始 | 并行 |
| 08-05 | P2-4: 文档 | ⏳ 待开始 | 并行 |
| 08-08 | P2-5: 代码清理 | ⏳ 待开始 | |
| 08-08 | **P2 Milestone** | ⏳ 待完成 | 架构重构完成 |

---

## 协作与沟通

### 每日站会

- **时间：** 每天 10:00 AM
- **参与者：** Backend Team + SRE + QA
- **内容：** 进度同步 + 阻塞问题

### 周报

- **提交时间：** 每周五 17:00
- **模板：**
  ```markdown
  ## Week X 进度
  - 已完成任务：[...]
  - 进行中任务：[...]
  - 阻塞问题：[...]
  - 下周计划：[...]
  ```

### 关键决策点

| 决策点 | 决策人 | 截止日期 |
|--------|--------|---------|
| P0 计划批准 | Tech Lead | 2026-07-11 |
| P2 架构方案评审 | Architect | 2026-07-25 |
| 插件化架构最终批准 | CTO | 2026-08-01 |

---

## 附录

### 快速参考

**启用 IR 转换：**
```bash
kubectl set env deployment/llm-gateway-go LLM_GATEWAY_IR_CONVERTER=true
```

**查看协议转换日志：**
```bash
kubectl logs -f deployment/llm-gateway-go | grep protocol_conversion
```

**触发测试告警：**
```bash
# 手动注入错误
curl -X POST http://localhost:8080/debug/inject-error?type=streaming
```

**联系人：**
- Backend Lead: @zhang-san (zhang.san@company.com)
- SRE: @li-si (li.si@company.com)
- QA: @wang-wu (wang.wu@company.com)

---

**文档版本：** v1.0  
**最后更新：** 2026-07-11  
**下次更新：** 每周五

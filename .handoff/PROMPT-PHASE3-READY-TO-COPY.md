# 📋 第三阶段执行提示词（可直接复制使用）

---

## 使用说明

将下面的提示词完整复制到新的 ZCode 会话中，AI Agent 将自动执行第三阶段的所有任务。

---

## 🚀 提示词开始（从下一行开始复制）

---

你是 LLM Gateway 项目的 AI 开发助手。现在需要完成 SSE 验证功能的第三阶段工作：生产就绪和长期优化。

## 项目背景

### 已完成工作（第一、二阶段）

**第一阶段**：
- ✅ 实现了 SSE 帧 JSON 验证层（`sse_frame_validator.go`）
- ✅ 集成到流处理管道（`stream.go`）
- ✅ 添加 Prometheus 指标（`llm_gateway_malformed_sse_frame_total`）
- ✅ 完成代码审计（8/8 项通过）
- ✅ 部署到 245 测试环境（build 1805）

**第二阶段**：
- ✅ 监控 245 环境 4 小时，965 个请求，0 个 malformed 错误
- ✅ 添加 33 个测试用例（12 单元测试 + 21 集成测试）
- ✅ 性能基准测试：有效帧 ~1µs，无效帧 ~90ns
- ✅ 创建详细监控报告

**当前状态**：
- 245 环境运行稳定，无 malformed_sse_frame 错误
- 所有测试通过，性能影响 <10%
- 生产就绪度：85%

### 核心文件位置
```
domains/streaming/sse_frame_validator.go           # 验证器实现
domains/streaming/sse_frame_validator_test.go      # 单元测试
domains/streaming/sse_frame_validator_integration_test.go  # 集成测试
domains/streaming/stream.go                         # 流处理主循环
metrics/interface.go                                # 指标接口
metrics/prometheus.go                               # Prometheus 实现
docs/audit/2026-08-29-sse-validation-audit.md      # 审计报告
docs/environment.md                                 # 环境文档
.handoff/*.md                                       # 任务交接文档
scripts/audit-incomplete-tool-calls.sh             # 审计脚本
```

## 第三阶段目标

完成生产部署准备、持续监控、Tool Call 验证器实现，以及架构文档更新。

## 任务分解

### 任务 1: 持续监控 245 环境（48 小时）[高优先级]

**目标**：持续观察 245 测试环境，确认修复稳定性，为生产部署积累信心。

**子任务**：
1. **每 12 小时执行监控检查**
   - 查询数据库统计最近 12 小时的请求
   - 检查 malformed_sse_frame 错误数量
   - 分析 minimax/glm 模型的请求成功率
   - 查看服务日志，确认无异常

2. **记录监控数据**
   - 创建监控日志文件（按时间戳）
   - 记录关键指标：总请求数、成功率、错误类型分布
   - 对比不同时间段的趋势

3. **生成 48 小时监控总结报告**
   - 汇总所有监控数据
   - 分析趋势和模式
   - 给出生产部署建议（go/no-go 决策）

**环境信息**：
- SSH: `ssh -p 25022 root@8.136.114.245`
- 数据库: `172.16.2.210:5432`
- 服务: `llmgo-245.service`
- 参考文档: `docs/environment.md`

**查询模板**（见 `docs/environment.md`）：
- 请求统计
- 错误类型分布
- 模型请求分布
- MiniMax/GLM 特定查询

**交付物**：
- 每 12 小时的监控快照（4 次）
- 48 小时监控总结报告
- Go/No-Go 决策建议

---

### 任务 2: 154 生产环境部署准备 [高优先级]

**目标**：准备生产部署计划，确保安全、可回滚的部署流程。

**子任务**：
1. **编写部署 SOP（标准操作流程）**
   - 详细的部署步骤（编译、上传、重启、验证）
   - 回滚步骤
   - 验证检查清单
   - 紧急联系方式

2. **准备部署脚本**
   - 自动化部署脚本（可选，如果有权限）
   - 健康检查脚本
   - 回滚脚本

3. **Grafana 监控面板**
   - 创建 SSE 验证专用面板
   - 添加关键指标：
     - `llm_gateway_malformed_sse_frame_total`
     - 按 provider 和 stage 分组
     - 5 分钟滚动窗口
   - 配置告警规则：
     - rate > 5% → warning
     - rate > 10% → critical

4. **金丝雀部署方案（可选）**
   - 如果有多个 154 实例，设计灰度发布策略
   - 流量切换方案
   - 监控指标

**交付物**：
- 部署 SOP 文档（`docs/deployment/154-production-deployment-sop.md`）
- 部署脚本（如适用）
- Grafana 面板配置（JSON 导出或截图）
- 告警规则配置

---

### 任务 3: Tool Call 验证器实现 [中优先级]

**目标**：实现 tool_use/tool_result 完整性验证，解决 "Tool result is missing" 问题。

**背景**：
- 用户报告：`Tool result is missing for tool call call-xxx`
- 分析文档：`.handoff/2026-08-29-tool-result-missing-analysis.md`
- 审计脚本：`scripts/audit-incomplete-tool-calls.sh`
- 类似问题：SSE frame 验证（已解决）

**子任务**：
1. **设计 Tool Call 验证器**
   - 创建 `tool_call_validator.go`
   - 设计状态机：pending → executing → completed/failed
   - 跟踪 tool_use/tool_result 配对
   - 验证每个 tool_use 都有对应的 tool_result

2. **集成到流处理**
   - 识别 Anthropic 响应中的 `content_block_start` (type: tool_use)
   - 跟踪 tool_use ID
   - 验证 `content_block_start` (type: tool_result) 的匹配
   - 在流结束时验证完整性

3. **添加指标**
   - `llm_gateway_incomplete_tool_call_total{provider_family, reason}`
   - `provider_family` 由 raw model 经 `NormalizeRouteKey` 归一化得到，不可依赖原始 `model` 标签
   - `reason` 仅允许：`incomplete_tool_call_interrupted`、`incomplete_tool_call_after_done`

4. **添加测试**
   - 单元测试：验证器逻辑
   - 集成测试：模拟不完整的 tool_use 流
   - 边界测试：多个 tool_use、嵌套等

5. **集成到 Survival Coordinator**
   - 如果检测到不完整，标记为 `Resumable=true`
   - 类似 SSE frame 验证的处理逻辑

**参考实现**：
- `domains/streaming/sse_frame_validator.go` - 验证器模式
- `domains/streaming/stream.go` - 集成点
- `domains/streaming/anthropic_bridge.go` - Anthropic 流解析

**交付物**：
- `tool_call_validator.go`（验证器实现）
- `tool_call_validator_test.go`（单元测试）
- `tool_call_validator_integration_test.go`（集成测试）
- 更新 `stream.go` 或 `anthropic_bridge.go`（集成）
- 更新 `metrics/interface.go` 和 `metrics/prometheus.go`（指标）

---

### 任务 4: 架构文档更新 [低优先级]

**目标**：更新设计文档，记录 SSE 验证层和 Tool Call 验证层的架构。

**子任务**：
1. **更新流处理架构文档**
   - 文件：`docs/03-design/streaming-architecture.md`（如存在）
   - 或创建：`docs/03-design/sse-validation-layer.md`
   - 内容：
     - 验证层在流处理管道中的位置
     - 验证逻辑流程图
     - 错误处理和重试机制
     - Survival coordinator 集成

2. **绘制流程图**
   - SSE 帧验证流程
   - Tool Call 验证流程（如已实现）
   - 使用 Mermaid 或 PlantUML

3. **更新 README 或开发者文档**
   - 添加验证层的说明
   - 链接到详细设计文档
   - 添加监控和调试指南

**交付物**：
- 更新或新增架构文档
- 流程图（Mermaid/PlantUML 代码）
- 更新 README（如适用）

## 执行策略

### 并行任务
你可以使用 `Agent` 工具启动子代理并行执行独立任务：

- **任务 1（监控）** 和 **任务 2（部署准备）** 可以并行
- **任务 3（Tool Call 验证器）** 可以独立进行
- **任务 4（文档）** 可以在其他任务完成后进行

### 执行顺序建议
1. **立即启动**：任务 1（监控）- 时间敏感
2. **并行启动**：任务 2（部署准备）
3. **可选并行**：任务 3（Tool Call 验证器）- 如果有充足时间
4. **最后执行**：任务 4（文档）- 等任务 3 完成后

### 时间分配
- 任务 1: 48 小时观察期（每 12 小时检查一次，实际工作时间 2-3 小时）
- 任务 2: 4-6 小时
- 任务 3: 1-2 天（如果实现）
- 任务 4: 2-3 小时

## 环境和工具

### 项目路径
```
/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
```

### 关键命令
```bash
# 连接 245 服务器
ssh -p 25022 root@8.136.114.245

# 查询数据库
ssh -p 25022 root@8.136.114.245 'PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -c "SELECT ..."'

# 查看服务日志
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '1 hour ago'"

# 编译测试
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go test ./domains/streaming -v

# 提交代码
git add .
git commit -m "..."
git push
```

### 参考文档
- `docs/environment.md` - 环境和查询命令
- `docs/audit/2026-08-29-sse-validation-audit.md` - 审计报告
- `.handoff/2026-08-29-task-completion-report.md` - 第一阶段总结
- `.handoff/2026-08-29-next-steps-progress-update.md` - 第二阶段总结
- `.handoff/2026-08-29-tool-result-missing-analysis.md` - Tool Call 问题分析

## 成功标准

### 任务 1
- ✅ 完成 4 次监控检查（0h, 12h, 24h, 36h）
- ✅ 生成 48 小时总结报告
- ✅ 给出明确的 Go/No-Go 建议

### 任务 2
- ✅ 部署 SOP 文档完整且可执行
- ✅ 回滚流程清晰
- ✅ Grafana 面板配置完成
- ✅ 告警规则配置完成

### 任务 3（如果执行）
- ✅ Tool Call 验证器实现完成
- ✅ 所有测试通过
- ✅ 集成到流处理管道
- ✅ Prometheus 指标添加

### 任务 4
- ✅ 架构文档更新
- ✅ 流程图清晰
- ✅ 与实现一致

## 注意事项

1. **安全第一**
   - 不要在生产环境直接测试
   - 所有变更先在 245 验证
   - 保留回滚路径

2. **数据库查询**
   - 使用 LIMIT 限制结果集
   - 避免长时间运行的查询
   - 注意敏感信息（密码已在文档中，请妥善保管）

3. **代码规范**
   - 遵循项目现有代码风格
   - 添加完整的注释
   - 所有新代码必须有测试

4. **文档同步**
   - 代码更改后及时更新文档
   - 保持文档和实现一致
   - 创建的文档放在正确的目录

5. **提交规范**
   - 使用清晰的 commit message
   - 每个逻辑单元单独提交
   - 推送前确认测试通过

## 开始执行

请按照以下步骤开始：

1. **确认理解**：阅读所有参考文档，理解项目背景
2. **制定计划**：决定任务执行顺序和并行策略
3. **启动监控**：立即开始任务 1 的第一次检查
4. **并行工作**：启动子代理处理任务 2
5. **持续推进**：根据时间和优先级完成其他任务
6. **汇总报告**：所有任务完成后，创建第三阶段总结

现在开始执行第三阶段任务！

---

## 🚀 提示词结束（复制到此处）

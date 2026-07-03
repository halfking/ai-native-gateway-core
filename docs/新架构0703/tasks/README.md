# 并行开发任务系统

> **创建时间**: 2026-07-03  
> **任务总数**: 20个  
> **可并行数**: 最多10个同时进行  

---

## 🎯 快速开始

### 立即可以并行开始的任务（Group A）

这5个任务**无依赖**，可以在不同的AI会话中同时开始：

```bash
会话1 → task-A1-hook-framework.md      Hook框架增强（Go）
会话2 → task-A2-output-sanitizer.md    输出脱敏插件（Go）
会话3 → task-A3-session-ingest.md      会话接收API（Python）
会话4 → task-A4-api-contract.md        API契约编写（OpenAPI）
会话5 → task-A5-sample-data.md         样例数据准备（JSON/YAML）
```

---

## 📋 使用方法

### Step 1: 选择任务
```bash
# 查看任务总览
cat 00-任务总览.md

# 选择一个符合你技能的任务
```

### Step 2: 打开任务提示词
```bash
# 例如选择Task A1
cat task-A1-hook-framework.md
```

### Step 3: 复制到新AI会话
1. 打开一个新的AI会话（ZCode/Claude/ChatGPT）
2. 复制整个任务提示词
3. 粘贴并发送
4. AI会根据提示词开始开发

### Step 4: 开发完成后
1. 运行测试确保通过
2. 提交代码评审
3. 更新任务进度

---

## 📊 已生成的任务提示词

### ✅ Group A（基础设施）- 可立即开始
- [x] **task-A1-hook-framework.md** - Hook框架增强 ⭐
- [ ] task-A2-output-sanitizer.md - 输出脱敏插件
- [ ] task-A3-session-ingest.md - 会话接收API ⭐
- [ ] task-A4-api-contract.md - API契约编写 ⭐
- [ ] task-A5-sample-data.md - 样例数据准备 ⭐

### ⏳ Group B（核心功能）- 依赖Group A
- [ ] task-B1-memora-auto.md - Memora自动沉淀 ⭐
- [ ] task-B2-llm-extraction.md - LLM智能提取 ⭐
- [ ] task-B3-session-editor.md - 会话编辑器
- [ ] task-B4-vibe-coding.md - Vibe Coding评估
- [ ] task-B5-cross-aggregator.md - 跨会话聚合

### ⏳ Group C（高级功能）- 依赖Group B
- [ ] task-C1-graph-builder.md - 知识图谱构建
- [ ] task-C2-auto-tagger.md - 自动标签系统
- [ ] task-C3-provider-profile.md - 供应商画像
- [ ] task-C4-cross-summary.md - 跨会话总结服务

### ⏳ Group D（前端可视化）- 依赖API契约
- [ ] task-D1-session-visualizer.md - 会话可视化中心
- [ ] task-D2-provider-dashboard.md - 供应商仪表盘
- [ ] task-D3-vibe-monitor.md - Vibe Coding监控
- [ ] task-D4-realtime-monitor.md - 实时监控大屏
- [ ] task-D5-graph-view.md - 知识图谱可视化

### ⏳ Group E（集成测试）- 依赖所有开发任务
- [ ] task-E1-e2e-test.md - 端到端测试

**完成进度**: 1/20 (5%)

---

## 🔄 任务依赖关系

```
Week 1: Group A (基础设施)
├─ A1 Hook框架 ─────┬──────────────┐
├─ A2 输出脱敏      │              │
├─ A3 会话API ──────┼───┬──────────┤
├─ A4 API契约 ──────┼───┼─────┐    │
└─ A5 样例数据      │   │     │    │
                    ▼   ▼     ▼    ▼
Week 2: Group B (核心功能)
├─ B1 Memora (需要A1+A3)
├─ B2 LLM提取 (需要A3)
├─ B3 会话编辑器 (需要A1)
├─ B4 Vibe Coding (需要A1)
└─ B5 跨会话聚合 (需要B2)

Week 2并行: Group D (前端)
├─ D1-D5 所有前端任务 (需要A4)

Week 3: Group C (高级功能)
├─ C1 知识图谱 (需要B2)
├─ C2 自动标签 (需要B2)
├─ C3 供应商画像
└─ C4 跨会话总结 (需要B1)

Week 4: Group E (集成测试)
└─ E1 端到端测试 (需要所有任务)
```

---

## 📝 提示词生成状态

### 当前状态
- ✅ **已生成**: 1个 (Task A1)
- 🔄 **生成中**: 0个
- ⏳ **待生成**: 19个

### 生成优先级

**P0 (本周必须完成)**:
- Task A1 ✅
- Task A3 (会话接收API) - 阻塞所有后端任务
- Task A4 (API契约) - 阻塞所有前端任务
- Task A5 (样例数据) - 测试必需
- Task B1 (Memora自动沉淀) - 核心功能

**P1 (下周完成)**:
- 所有Group B任务
- 所有Group D任务

**P2 (再下周完成)**:
- 所有Group C任务
- Task E1

---

## 🚀 如何生成剩余提示词

### 方案1: AI辅助生成（推荐）⭐

在新AI会话中使用以下提示词：

```
我需要你帮我生成Task A3的完整开发提示词。

参考模板: docs/新架构0703/tasks/task-A1-hook-framework.md

任务信息:
- 任务ID: A3
- 任务名称: 会话接收API
- 负责团队: Python后端
- 工期: 1周
- 依赖: 无
- 技术栈: Python 3.14+, FastAPI, asyncio, Celery

任务目标:
1. 实现POST /api/sessions/ingest API接收llm-gateway-go推送的会话
2. 会话验证和预处理
3. 异步任务队列处理
4. 状态查询API
5. 与MemOS集成

请求体格式参考: docs/新架构0703/00-总体架构设计.md 中的API契约部分

请生成完整的任务提示词，包含:
- 任务概述
- 详细需求（带完整Python代码示例）
- FastAPI路由实现
- Celery任务实现
- 数据模型
- 测试用例
- 验收标准
- 开发步骤

格式与task-A1保持一致。
```

### 方案2: 手工编写

1. 复制 `task-A1-hook-framework.md` 作为模板
2. 修改任务信息
3. 替换技术栈和代码示例
4. 调整验收标准
5. 保存为新文件

### 方案3: 批量生成脚本

创建Python脚本自动生成所有提示词（需要定义任务元数据）

---

## ✅ 提示词质量检查

每个生成的提示词应包含：

- [ ] 任务ID和元信息（8个字段）
- [ ] 任务概述（清晰的目标说明）
- [ ] 技术栈列表
- [ ] 项目上下文（当前代码结构）
- [ ] 详细需求（至少3个Requirement）
- [ ] 完整代码示例（500+行）
- [ ] 数据结构定义
- [ ] API接口定义（如适用）
- [ ] 测试用例（至少5个）
- [ ] 验收标准（功能、性能、测试）
- [ ] 参考资料
- [ ] 开发步骤（Step by Step）
- [ ] 完成检查清单

---

## 📞 协调与支持

### 每日同步
- **时间**: 每天9:30
- **方式**: 站会或异步更新
- **内容**: 进度、阻塞、协调

### 提示词审核
- **负责人**: 架构师
- **流程**: 生成 → 自查 → 提交评审 → 发布

### 问题反馈
- **渠道**: 企业微信群/Slack
- **响应**: 2小时内

---

## 📈 进度追踪

### 提示词生成进度
```
Group A: █░░░░ 1/5  (20%)
Group B: ░░░░░ 0/5  (0%)
Group C: ░░░░  0/4  (0%)
Group D: ░░░░░ 0/5  (0%)
Group E: ░     0/1  (0%)

总体:    █░░░░░░░░░ 1/20 (5%)
```

### 开发完成进度
```
所有任务开发完成后在这里更新
```

---

## 🎊 总结

我们已经建立了完整的并行开发任务系统：

✅ **任务切分**: 20个独立任务，清晰的依赖关系  
✅ **并行策略**: 最多10个任务同时进行  
✅ **提示词模板**: Task A1作为高质量模板  
✅ **生成方案**: 3种方案可选  
✅ **协调机制**: 每日同步、问题反馈  

**下一步**: 生成剩余19个任务提示词，启动并行开发！

---

**文档维护**: 架构组  
**最后更新**: 2026-07-03  
**版本**: v1.0

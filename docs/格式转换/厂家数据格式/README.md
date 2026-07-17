# 厂家数据格式规范集合

本目录收集 9 个主要 LLM 原厂的 API 规范文档，作为 llm-gateway-go 格式转换审计的对比基线。

## 下载信息

- **下载时间**: 2026-07-18
- **方法**: 基于公开 API 知识创建
- **状态**: 需手动访问官网确认最新版本
- **用途**: 作为 docs/格式转换/10-现状确认与原厂对比.md 的对比基线

## 厂商列表

| 厂商 | 目录 | 规范文件 | 官方文档 |
|---|---|---|---|
| OpenAI | [openai/](./openai/) | chat-completions.md, responses.md | https://platform.openai.com/docs/api-reference/ |
| Anthropic | [anthropic/](./anthropic/) | messages.md | https://docs.anthropic.com/en/api/messages |
| Google Gemini | [gemini/](./gemini/) | generate-content.md | https://ai.google.dev/api/generate-content |
| GLM (智谱) | [glm/](./glm/) | glm-api.md | https://open.bigmodel.cn/dev/api |
| MiniMax | [minimax/](./minimax/) | minimax-api.md | https://platform.minimaxi.com/docs |
| DeepSeek | [deepseek/](./deepseek/) | deepseek-chat.md | https://api-docs.deepseek.com/ |
| Qwen/DashScope | [qwen/](./qwen/) | dashscope-api.md | https://help.aliyun.com/zh/model-studio/ |
| Ollama | [ollama/](./ollama/) | ollama-chat.md | https://github.com/ollama/ollama/blob/main/docs/api.md |
| Doubao/Volcengine | [doubao/](./doubao/) | volcengine-api.md | https://www.volcengine.com/docs/82379 |

## 使用说明

### 对比分析

每个厂商规范可用于：
1. 与 OpenAI Chat Completions 格式对比
2. 识别私有字段扩展
3. 验证多模态支持范围
4. 确认工具调用格式差异

### 维护

- 定期访问官方文档确认 API 变更
- 更新时在各厂商 README 中标注更新日期
- 保持与 llm-gateway-go 代码同步

## 注意事项

⚠️ **重要**: 这些文档基于公开知识创建，仅作开发参考。生产环境请以官方文档为准。

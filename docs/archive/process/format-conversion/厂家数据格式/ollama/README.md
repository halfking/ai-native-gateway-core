# Ollama API 规范文档

## 文档来源

- **官方文档**: https://github.com/ollama/ollama/blob/main/docs/api.md
- **下载时间**: 2026-07-18
- **文档状态**: 基于公开知识创建,需手动确认最新版本

## 说明

本目录包含 Ollama API 的完整规范文档,主要包括:

- **ollama-chat.md**: Ollama API 完整规范(OpenAI 兼容 + 原生 /api/chat 接口)

## 注意事项

⚠️ **重要提示**: 本文档基于公开的 API 知识创建,在实际使用前建议:

1. 访问官方文档确认最新的 API 版本和变更
2. 检查新增的模型和功能支持
3. 验证 Ollama 原生接口的最新参数
4. 关注双接口模式(OpenAI 兼容 vs 原生)的差异

## 版本信息

- **OpenAI 兼容路径**: `/v1/*`
- **原生接口路径**: `/api/*`
- **特色功能**: 本地部署、模型管理、Modelfile
- **SDK**: 可使用 OpenAI SDK 或 Ollama 官方 SDK

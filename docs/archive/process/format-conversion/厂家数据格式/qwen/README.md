# 通义千问 Qwen API 规范文档

## 文档来源

- **官方文档**: https://help.aliyun.com/zh/dashscope/
- **OpenAI 兼容**: https://help.aliyun.com/zh/dashscope/developer-reference/compatibility-of-openai-with-dashscope/
- **下载时间**: 2026-07-18
- **文档状态**: 基于公开知识创建,需手动确认最新版本

## 说明

本目录包含阿里云通义千问 API 的完整规范文档,主要包括:

- **dashscope-api.md**: Qwen API 完整规范(OpenAI 兼容 + DashScope 原生接口)

## 注意事项

⚠️ **重要提示**: 本文档基于公开的 API 知识创建,在实际使用前建议:

1. 访问官方文档确认最新的 API 版本和变更
2. 检查新增的模型和功能支持
3. 验证 DashScope 私有字段的最新定义
4. 关注双接口模式(OpenAI 兼容 vs 原生)的差异

## 版本信息

- **OpenAI 兼容路径**: `/compatible-mode/v1/*`
- **DashScope 原生路径**: `/api/v1/services/aigc/*`
- **私有扩展**: enable_search、qwen-long 等
- **SDK**: 参考阿里云 DashScope SDK 或使用 OpenAI SDK

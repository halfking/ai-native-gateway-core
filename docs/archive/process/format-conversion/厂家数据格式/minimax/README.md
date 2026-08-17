# MiniMax API 规范文档

## 文档来源

- **官方文档**: https://platform.minimaxi.com/document/
- **下载时间**: 2026-07-18
- **文档状态**: 基于公开知识创建,需手动确认最新版本

## 说明

本目录包含 MiniMax API 的完整规范文档,主要包括:

- **minimax-api.md**: MiniMax API 完整规范(OpenAI/Anthropic 兼容路径 + 私有字段)

## 注意事项

⚠️ **重要提示**: 本文档基于公开的 API 知识创建,在实际使用前建议:

1. 访问官方文档确认最新的 API 版本和变更
2. 检查新增的模型和功能支持
3. 验证 MiniMax 私有字段的最新定义
4. 关注 API 端点和认证方式的更新

## 版本信息

- **API 路径**:
  - OpenAI 兼容: `/v1/text/chatcompletion_v2`
  - Anthropic 兼容: `/v1/messages`
- **私有扩展**: bot_setting, reply_constraints, plugin 等
- **SDK**: 参考 MiniMax 官方 SDK

# 系统自检（Self-Check）模块

本目录包含 llm-gateway-go 系统自检功能的设计与实现文档。

## 文档列表

| 文件 | 状态 | 说明 |
|------|------|------|
| [01-design.md](./01-design.md) | ✅ 已完成 | 详细设计文档 |
| [02-implementation-plan.md](./02-implementation-plan.md) | ⏳ 待定 | 实现计划（设计评审通过后生成） |
| [03-deployment-guide.md](./03-deployment-guide.md) | ⏳ 待定 | 部署指南 |
| [04-test-report.md](./04-test-report.md) | ⏳ 待定 | 测试报告 |

## 功能概述

每 60 秒对 10 个关键模型自动跑 1 次 ping + 3 轮工具调用会话测试，验证 gateway 的可用性与路由正确性。失败时自动用 provider 原始凭据直连上游做故障隔离。所有结果落地到独立表，并提供 Dashboard → "系统监测" Tab 可视化。

## 快速开始

待实现完成后补充。
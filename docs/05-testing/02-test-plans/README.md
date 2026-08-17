# 05-testing/02-test-plans — 测试方案

## 职责

测试方案 相关文档。

## 当前内容

| 文件 | 说明 |
|---|---|
| `会话优化v4/客户端会话保持-测试方案与用例.md` | 会话优化 v4（客户端会话保持）单元/集成/混沌测试方案与用例总册 v1.3——测试环境落本地 Docker（PG=llm-gateway-pg 克隆 gwtest_v4 测试库、Redis=nbjl-redis db15 + ~/work/docker-base-images 基础镜像本地构建部署）、初始数据（SD-01~10）、~130 用例、执行与门禁方案 |
| `testing/comprehensive-test-plan.md` | 全方位测试方案 v1.0（分层健康检查/动态权重/自适应降级）；其场景已按 v4 总册 §1.1 映射融合，不再单独演进 |
| `testing/ROUTING_TEST_PLAN.md` | 路由测试方案 |
| `testing/three-env-unified-verification.md` | 184/本地/71 三环境配置统一验证报告 |

## 命名约定

- 测试策略：`{层}-test-strategy.md`（unit/integration/e2e）
- 测试方案：`TP-NNN-{标题}.md`
- 测试用例目录：`TC-NNN-{标题}/`
- 主题分组：既有主题（如 `会话优化v4/`、`testing/`）作为子目录保留原名

# 05-testing/01-strategy — 测试策略

> **事实快照：** 2026-08-21  
> **规则：** 测试结果必须绑定 commit、配置、环境、feature flags 和外部依赖；测试被 skip 不等于通过。

## 权威入口

- [当前测试矩阵](test-matrix.md) — unit、contract、PG/RLS、Redis、stream、E2E、provider、deployment、security 和 migration。
- [测试总目录](../README.md)
- [测试计划索引](../02-test-plans/INDEX.md)
- [测试用例索引](../03-test-cases/INDEX.md)
- [Gateway 当前架构](../../03-design/01-architecture/architecture/ARCHITECTURE.md)
- [运行时请求流](../../03-design/01-architecture/architecture/runtime-request-flow.md)

## 当前原则

1. 以 `cmd/gateway` 默认 v1 路径为生产回归基线；`gateway-v2`、`/v2/*` 和 v1 wrapper 要单独标注。
2. 真实 PG/RLS 测试必须使用 `NOSUPERUSER` + `NOBYPASSRLS` 角色；缺少 `TEST_DATABASE_URL` 时记录为 `SKIPPED-CONFIG`/`UNKNOWN`。
3. 真实 Provider、Redis、对象存储和部署测试必须记录 endpoint 类型、fixture、flags、版本和脱敏输出。
4. Session V2 shadow、ASM projection 和 Maintain proxy 的测试不能代替 canonical ownership 或生产切换门禁。
5. 每个安全、tenant、RLS、retry、billing、migration 和 deployment 变更先补回归测试，再进入灰度。

## 命名约定

- 测试策略：`{layer}-test-strategy.md`（unit/integration/e2e）
- 测试方案：`TP-NNN-{title}.md`
- 测试用例目录：`TC-NNN-{title}/`

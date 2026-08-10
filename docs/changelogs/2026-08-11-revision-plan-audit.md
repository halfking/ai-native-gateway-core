# 修订0811任务方案审计

## 做了什么

审计并修订 `docs/修订0811/` 的72小时修订总结、OmniRoute融合方案和实施计划。将未经本地证据支持的性能、覆盖率、静态分析和安全结论降级为待验证假设，并以现有 `docs/omni-ref2/`、URSM v2 和会话优化 v3 文档为架构真源。

## 改动清单

| 文件 | 说明 |
|---|---|
| `docs/修订0811/05-方案审计修订说明.md` | 事实核验、ownership边界和实施门禁 |
| `docs/修订0811/06-下一阶段实施计划.md` | 契约与观测基线工作包 |
| `docs/修订0811/01-72小时修订总结.md` | 修正 Git 统计口径，撤销无证据收益数字 |
| `docs/修订0811/02-04*.md` | 标记为历史讨论稿 |
| `docs/修订0811/Phase1-*.md` | 停止未经证据的热路径实施计划 |
| `docs/修订0811/README.md` | 增加审计与实施入口，纠正 URSM/ASM 引用 |

## 为什么这样做

原方案混用了 Git 提交数、numstat、估算性能和设计伪代码，存在把讨论稿误当实施合同的风险。审计后优先保持 Gateway 数据面和 ai-session-manager 控制面的事实 ownership，先建立 versioned event contract、关联链和性能基线，再引入压缩或新路由策略。

## 验证结果

- `go build ./...`: 通过。
- `go test ./autoroute/... ./domains/ursm/v2/... ./domains/session/v2/...`: 通过。
- `go vet ./autoroute/... ./domains/ursm/v2/... ./domains/session/v2/...`: 通过。
- `git diff --check -- docs/修订0811`: 通过。
- 页面改动：无，不触发浏览器实测。

## 遗留与风险

- 全量 `go test ./...` 和 `go vet ./...` 仍需在下一代码实施提交前执行。
- `request.completed.v1` 当前不能直接增加 routing/compression 字段，必须先升级双方 validator 和 fixtures。
- 性能目标需建立可复现基线后确认。

## 下一步建议

先执行 `docs/修订0811/06-下一阶段实施计划.md` 的 WP2：核验 Gateway 与 ai-session-manager 的 v1 事件契约并生成双向 fixtures，不同时修改路由、压缩或会话缓存。

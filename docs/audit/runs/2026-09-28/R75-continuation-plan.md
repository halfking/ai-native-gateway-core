# R75 · 续审执行方案

> 日期：2026-09-28（Asia/Shanghai）
> 范围：R73 48h 审计的继续执行；本轮先收口跨事件流式脱敏与 Web 依赖安全，再推进全域检查。
> 当前代码起点：`9946d75b5adf7eba7a03c15fee83123d5592e844`；最终状态/提交需在执行时重读 Git。

## 1. 已完成的本轮执行项

1. 将本地 `main` 快进到远端 `b60b12874`，核对远端增量只有 `scripts/deploy-local.sh`；随后远端增加并已对齐 `9946d75b5`，为 Web 报表页崩溃修复。
2. 对照 `package.json` 与 npm/pnpm 锁文件运行全量/生产依赖审计。修复前 pnpm 报 13 项（含 1 critical、8 high），npm 报全量 10 项；生产树均有 4 项。
3. 升级并锁定 vue-i18n、Vite、Vitest 与受影响的 brace-expansion、glob、nanoid、PostCSS；同步两个锁文件并修复 Vitest 5 mock 类型。
4. 复核并补强 R73-F09 的跨完整 SSE 事件 carryover：状态绑定单个 response writer；普通 brace/JSON 续片保持响应完整；已开始的敏感 marker 格式错误时遮蔽而不中断；有效 marker 跨 OpenAI/Anthropic/Responses lane 匹配还原。
5. 完成前端、Go 受影响包 race/build/vet 验证；详细结果见 [R75 报告](R75-continuation-report.md)。

## 2. Web 依赖提交与发布门禁

提交前重读 `git status -sb` 和 staged diff，避免把其他进程工作带入本次变更。只按确认归属挑选文件：

- Web 依赖：`web/package.json`、`web/pnpm-lock.yaml`、`web/package-lock.json`、`web/src/composables/useLiveStreamUrl.test.ts`、`web/src/composables/useSessionSummaryJump.test.ts`。
- 将 Web 依赖审计状态更新写入 `docs/全面审计.md` 与本报告；F09/F10 已随 `9fe387ac8` 提交并在 `d96bc4f5e` 的文档证据中闭环。Web 变更提交后核对远端 commit SHA 和审计链接。

保留未归属的 `web/public/menu-config.json` 生成时间差异与 `docs/audit/todo-state.json`，除非确认其归属和内容。暂不包含 UI 对账页修复，该代码已由远端 `9946d75b5` 提交。

## 3. 后续审计执行顺序

1. **完整 D14 收口**：检查 stream `ShouldBlock` 的客户端终态与日志/指标；真实 provider frame golden、断连/取消/不透明数据、64 lane 边界验证。不得把 package race 通过等同于部署级完成。
2. **D01–D03 与 D16**：沿 ingress → IR → protocol adapter → sanitized/compressed → storage/session detail 实读创建、赋值、编解码、身份/occurrence、附件/媒体和回填链；验证跨进程 offset、V2 metadata 与 TTL。
3. **D04–D05 与 D11–D13**：请求总队列和分维队列、权重路由/限流、取消/重试；overflow 压缩/handoff 阈值契约；auto model、代理地区优先级与免费 token 池隔离。
4. **D06–D10**：full PG+Redis 与 lite SQLite 双模式、hot+columnar 8 小时归档/删除语义、供应商错误凭据归属、单一节点状态写路径、usage/费用来源与对账。
5. **D15–D17 与 96h 文档对照**：dashboard/credential/session 状态一致、菜单/API 复用、重复实现调用图；逐份核对 96 小时内方案文档并记录代码偏差。
6. **环境验证**：先以 disposable `deploy-local.sh` 完成 lite 集成；兼容 PG/Redis/provider 仅在安全测试环境与有效凭据可用时运行；全仓 race/network/DNS 失败在隔离环境复测。
7. **安全专项**：R62-H1/H2/M1/M2 先形成 token 迁移/回滚方案，再实施独立改动；运行可用的 secret/dependency 工具并记录缺失项。

每个域保留一份有日期的报告，登记触发输入、源码调用链、预期/实际、影响、修复、测试与未验证环境。只关闭有当前代码和验证证据的项目。

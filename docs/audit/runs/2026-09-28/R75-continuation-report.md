# R75 · 48h 审计续审报告

> 日期：2026-09-28（Asia/Shanghai）
> 状态：本续审只覆盖 D14 流式脱敏的补充复核与 Web 依赖安全。D01–D17 和近 96h 方案文档尚未完成全量审计。
> 起始代码基线：`main` / `origin/main` `9946d75b5adf7eba7a03c15fee83123d5592e844`。该提交为 Web 对账报表页显式导入组件的修复。续审期间 F09/F10 代码以 `9fe387ac8` 提交并推送，相关审计证据更新至 `d96bc4f5e`；Web 依赖修订以 `0d5230bfb` 提交，并在最终 tip `816a16fcdfe787e38de804675e350705da37c5b` 推送后 fetch 核验通过。

## 1. 范围与处理

本续审接续 R73 48h 审计，不移动原冻结窗口 `2026-09-25 23:01:34` 至 `2026-09-27 23:01:34`。本轮核验了前端 npm/pnpm 依赖树、F09 跨完整 SSE delta 的脱敏恢复，以及 F10 映射缺失时的 mask 降级；未连接生产数据库、Redis 或真实模型供应商。

依赖漏洞扫描使用官方 npm registry。原配置镜像的 audit endpoint 不支持 `npm audit`，因此镜像返回错误后改用 `https://registry.npmjs.org`。修复前 `pnpm audit` 报告 13 项（1 critical、8 high、4 moderate），生产依赖树 4 项；npm 审计报告全树 10 项、生产树 4 项。修复后 pnpm 全树/生产树与 npm 全树审计均为 0 项已知漏洞。

主要修订：

- `vue-i18n` 更新至 `^9.14.5`，`vite` 更新至 `^6.4.3`，`vitest` 更新至 `^5.0.2`。
- 对传递依赖 `brace-expansion`、`glob`、`nanoid`、`postcss` 增加有版本范围的覆盖，并同步 `web/pnpm-lock.yaml` 与 `web/package-lock.json`。
- Vitest 5 类型定义要求测试 mock 显式声明函数签名；相应修正 `useLiveStreamUrl.test.ts` 与 `useSessionSummaryJump.test.ts` 的 mock 类型。
- 复核既有 F09 实现时发现普通 JSON 流在 `{` 后跨事件时会误入 fail-closed，导致合法回复中断。现在普通非 marker 前缀会与续片合并透传；已进入 `{SENSITIVE:` 的格式错误续片会局部替换为 `[REDACTED]` 并继续流，不会暴露 marker/index 中的敏感数字。无 lane 的不透明续片与 lane 容量超限仍阻断后续帧。

## 2. 发现与状态

| ID | 域/级别 | 结论 | 证据与影响 | 修复和验证 |
|---|---|---|---|---|
| R75-WEB-01 | Web 供应链 / P1 | 已修复并推送 (`0d5230bfb`) | 修复前前端依赖树包含 critical/high advisory，且生产树有 4 项已知漏洞；pnpm 与 npm 因 lockfile 视图差异报告数量不同 | 升级直接依赖与约束传递依赖；双 lockfile 重建；修复 Vitest mock 类型；官方 registry 下 `pnpm audit`、`pnpm audit --prod`、`npm audit --json` 均为 0 项 |
| R75-D14-01（R73-F09 续审） | D14 流式脱敏 / P2 | 已实现并推送 (`9fe387ac8`) | 完整 SSE delta 可拆开一个 placeholder；旧逻辑会把碎片透传。request-local `StreamMeta.State` 按协议 lane 暂存尾片，最大 64 lanes、每 lane 256 bytes | `go test -race ./domains/streaming ./security/sanitize -count=1` 通过；普通 `{` 跨事件续片合并恢复，格式错误 marker 局部遮蔽并继续；覆盖 production writer、协议 delta、request/lane 隔离、未知 placeholder、上限和 Redis 错误 |
| R75-D14-02（R73-F10 续审） | D14 脱敏降级 / P2 | 已实现并推送 (`9fe387ac8`) | 空 map 或 Redis 读取失败原会短路并透传未知完整 `{SENSITIVE:...}`；这是内部 marker 可见性缺陷，不等同于原始 PII 已泄露 | 读取失败时按空 map 调用 mask；空 map 路径仍检测 marker。包含 miniredis 不可达模拟和完整受影响包 race |

## 3. 验证记录

| 命令 | 结果 | 环境/说明 |
|---|---|---|
| `pnpm audit --registry=https://registry.npmjs.org` | 退出码 0；0 项 | pnpm 10.29.2 |
| `pnpm audit --prod --registry=https://registry.npmjs.org` | 退出码 0；0 项 | 生产依赖树 |
| `npm audit --registry=https://registry.npmjs.org --json` | 退出码 0；0 项 | 锁文件统计 prod 58 / dev 254 / optional 92 |
| `pnpm test` | 退出码 0；136 个文件 / 982 个用例通过 | Vitest 5.0.2；本机 Node 26 |
| `pnpm typecheck` | 退出码 0 | `vue-tsc --noEmit` |
| `pnpm build` | 退出码 0 | 保留 Rollup `#__PURE__` 注释与 `_core.ts` 混合静态/动态导入警告；无构建错误 |
| `go test -race ./domains/streaming ./security/sanitize -count=1` | 退出码 0 | 覆盖本地 writer/interceptor 集成与 Redis/miniredis 测试；未使用真实 Redis/供应商 |
| `go test ./domains/hooks/response ./domains/streaming ./security/sanitize -count=1` | 退出码 0 | 受影响包普通测试 |
| `go build ./...` / `go vet ./...` | 均退出码 0 | 全仓 Go build/vet |
| `git diff --cached --check` | 退出码 0 | Web 与最终文档提交前均检查 |
| `git push origin main` + `git fetch origin` | 退出码 0；本地和远端均为 `816a16fcdfe787e38de804675e350705da37c5b` | R75 Web 修复与证据已发布 |

安全审查 skill 指定的 `~/.agents/skills/security-review/scripts/run-all-checks.sh` 当前不存在，`govulncheck` 也未安装；因此没有声称该聚合脚本或 Go 漏洞数据库检查已通过。依赖风险由上述 npm/pnpm 官方 registry 审计覆盖。

`go test ./... -count=1` 未在本续审重跑。先前全仓运行的失败仍开放：IPv6 mDNS 测试在当前环境报告 `no route to host`；reserved-TLD DNS 测试实际分类为 `transient`、期望 `network`。更早一轮另有 plugin-runtime 子进程测试的临时失败，之后串行重跑通过但原因未证实。此前全仓 race 还报告 dispatch 数据竞争和网络测试边界失败，需隔离环境复核。

## 4. 尚未覆盖与风险

- F09/F10 与 Web 依赖修复、R75 文档已提交并推送；最终已核对 `main` / `origin/main` 同为 `816a16fcdfe787e38de804675e350705da37c5b`。未归属的 `web/public/menu-config.json` 生成时间改动与 `docs/audit/todo-state.json` 保留在工作区，没有混入提交。
- `ShouldBlock` 当前由 writer 丢弃帧；达到 64 lane 或遇到有待续 marker 的不透明 SSE payload 后，客户端终态错误和可观测事件仍需评估。
- F07 migration 754 没有兼容 PostgreSQL 实测；无生产 Redis、真实 provider 凭据与完整 deploy-local 集成证据。
- D01–D17 全域核验、96h 方案文档逐份对照、R62-H1/H2/M1/M2 凭据迁移与安全验证尚未完成。
- 前端验证运行于 Node 26。CI 配置 Node 22；Vitest 5 声明支持 Node `^22.12.0`，但本轮未在本机运行 Node 22 CI 镜像。

## 5. 下一步

执行顺序、分支/文件选择性提交门禁和未完成域计划见 [R75 执行方案](R75-continuation-plan.md)。

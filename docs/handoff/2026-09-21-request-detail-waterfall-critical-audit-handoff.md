# 请求详情“调用瀑布”批判式审计 Handoff（2026-09-21）

> 权威留档：`docs/audit/2026-09-21-request-detail-waterfall-critical-audit.md`。
> 本文件只保留后续接手所需的事实、风险和验收顺序；不得把中间部署或源码推断写成最终 UI 验收。

## 已完成

1. 批判性复核 `119981c02`：它仅清理 `gotoSection()` query，未实现/验证“右侧抽屉正文内嵌”要求；其前的本地 8782 是 `ef694cdf`，所以旧验证无效。
2. 抽屉与内嵌瀑布页已经共用 `web/src/components/detail/WaterfallRequestDetailContent.vue`：请求摘要、T0–T9、阶段表、attempts 行统一渲染。
3. 请求详情内部跳转统一为 named route 的 `{ mode, tab }` 窄契约；深链、概览点击、切模式、选轮次均有测试，避免把 `redirect/login/foo` 等 query 带回详情页。
4. 独立审查抓到的 attempts 回退回归已修复：原始 waterfall attempts 优先；DB by-ID 响应**省略**字段时才用已有 merged attempts；显式空数组不覆盖。
5. 全仓门禁抓到 `discovery/discovery.go` 两段 SQL 字面量中的 Go `//` 注释（`29b4ea64` 引入）；已改为 PostgreSQL `--`，`go test ./internal/sqlguard ./discovery -count=1` PASS。
6. 前端 typecheck 阻断的 8 个 locale duplicate keys 已删；color audit 新增违规的 8 个硬编码状态色已令牌化。

## 已有验证（真实结果）

- 聚焦前端回归：9 文件、46 tests PASS（含 drawer/inline parity）。
- 全前端（最近一次审查前状态）：134 文件、954 tests PASS；vue-tsc / responsive / element / color / build PASS。审查后新增 fallback 边界与 parity 测试，聚焦 46 tests PASS + vue-tsc PASS；最终仍须跑完整前端套件一次。
- `go build ./...` PASS；`go vet ./...` PASS。
- 首次 `go test ./...`：`internal/sqlguard` 稳定失败（已修）；`internal/logging.TestAsyncRawDataLogger_OverflowEmitsAnomaly` 在并发全仓轮中失败。已确认 goroutine 入队先于 Flush 的竞态并改为同步入队，`go test ./internal/logging -count=20` PASS；`go test ./internal/sqlguard ./discovery -count=3` PASS。
- 全仓 Go 重跑仍未全绿：`cmd/gateway.TestLiteRequestLogSink_TurnNoContinuesAcrossRestart` 在包级 `-count=10` / 全仓组合中偶发 macOS temp `MkdirAll invalid argument`，但该精确测试 `-count=100` PASS。需要独立稳定性调查，不能声称全仓 Go 绿。
- `./verify.sh --web` 在 migration checksum 阶段失败：11 mismatch + 662/663 stale registry，本轮没有修改 migrations/台账，且已另派只读审计；不得直接改 hash 刷绿。
- 中间部署 `2.5.6-119981c0-20260921-2159` PASS（health/ready/version、credential decrypt smoke、cookie login）。它不含最终未提交修复，不能当最终 release。

## 未完成收尾（严格顺序）

1. 等/读取当前 `go test ./... -count=1` 最终结果；若 `internal/logging` 再失败，按具体稳定性调查，不要忽略。
2. 等 checksum 只读审计结果；将不确定项留在风险中，禁止猜测性同步台账。
3. 最终复跑完整前端 `pnpm run test && pnpm run responsive:check && pnpm run element:check && pnpm run color:check && pnpm run build`；回退 `web/public/menu-config.json` 的纯时间戳 churn。
4. 审核本次暂存名单：只加入本轮 frontend/locale/color/discovery 修复、测试、本文档与 audit 文档。**绝不加入**：
   - `docs/audit/252-pg-log-audit-2026-09-19/pg_252_pg17_logs.txt`（含原始请求内容）
   - `docs/audit/2026-09-19-r46-offline-static-scan/`
   - `docs/operations/252-maintenance-window-2026-09-17.md`
5. 提交分支，合并到 `main`，推送。版本文件应按仓库惯例由最终 release bump 写入，不复用中间 2159 的 `119981c0` 身份。
6. **先确认 F7 已合入**：`cmd/gateway/main.go` 必须注册 `"/api/admin/dispatch/waterfall/request/"` 到 `wrapAdmin(handleDispatchWaterfallByRequest)`；此前 handler 存在却未注册，最终浏览器实测导致 waterfall fetch 404/401 并回跳 `?login=1`。
7. 在最终 main SHA 上运行标准 `scripts/deploy-local.sh deploy`（不带 `--no-frontend`）；核对 `/healthz`、`/readyz`、`/version`、active bundle 的 `go version -m` 与 bundle `version.json`。
8. 用已认证浏览器做真正验收：
   - 指定 URL `/request-detail/f5a6c9991350b81b5bd0fbe2a3f36e55?mode=request&tab=waterfall` 主内容区必须有 `waterfall-request-detail` / 阶段表，不能只是页面外壳或概览；
   - 从可用请求的概览点击“调度瀑布”，确认 requestId 不变、query 为 `mode=request&tab=waterfall`，没有跳到 `/`；
   - `/dispatch/waterfall` 选择真实请求，抽屉与“全屏详情”共享正文（正常 attempts 场景）；历史 DB fallback 的 attempts 降级按 audit 文档说明记录。

## 已认证浏览器事实

- 已接管用户现有 request-detail 标签，能打开真实单请求/会话轮次页面。
- 本地 `POST /api/auth/token` 用部署同步的 admin 配置返回 HTTP 200 并设置 `llmgw_session`；凭据/token 没有输出或写入文件。
- 指定 request 在中间版本 `119981c0/#2159` 的页面只显示 banner/main，不足以证实 waterfall 正文；必须最终 release 后重测。

## 风险与纪律

- 工作区是共享的；提交前 `git fetch origin main`、`git status`、逐文件 `git add`。
- migration checksum 漂移与 `internal/logging` 波动不能被“UI 测试绿”覆盖。
- 后端历史 DB waterfall 行没有原始 attempts；前端 fallback 只是保留诊断证据，不能声称与已淘汰 ring 的历史抽屉完全等价。

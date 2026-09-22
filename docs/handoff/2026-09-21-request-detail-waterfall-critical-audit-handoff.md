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

## 闭环状态（2026-09-22）

- 远端 main 已包含 `ab4af8ec8`（抽屉/内嵌共享正文）、`9252aa8e5`（by-ID waterfall 路由）和 `107f1d97d`（历史 404 derived 回退、401 redirect 保留）；当前 main 为 `8f6af6338`。
- 本机 active release 为 `8f6af633/#2173`；`/healthz`、`/readyz`、`/version`、candidate/active credential decrypt smoke 均 PASS。
- 已认证 SPA 实测（干净标签、最终 active `8f6af633/#2173`）：原始 URL `request-detail/f5a6…?mode=request&tab=waterfall` 从概览主内容快捷“调度瀑布”点击后，保持同一 requestId 与 query，并实际呈现 `waterfall-request-detail` + T0–T9 表；未跳回总览。该历史请求的 by-ID waterfall 已过保留窗，主线 derived fallback 使用 detail/log/journey 生成阶段表与 Attempts(3)。
- `/dispatch/waterfall` 实测选中 live request `89bf243bb90ae670f00715eaf1cdf659` 后，右侧抽屉显示同一共享正文、T0–T9 表和 Attempts(1)。
- 本轮相关前端 gate：完整 136 files / 971 tests PASS，typecheck / responsive / element / color / build PASS；目标 Go 包 `cmd/gateway`、`internal/sqlguard`、`discovery`、`internal/logging` PASS。

## 不纳入本轮提交

- `docs/audit/252-pg-log-audit-2026-09-19/pg_252_pg17_logs.txt`（原始请求内容）
- `docs/audit/2026-09-19-r46-offline-static-scan/`
- `docs/operations/252-maintenance-window-2026-09-17.md`
- 共享工作区里的 VERSION/version.json/menu-config/db-changelog 部署或并行会话改动，除非对应创建者明确提交。

## 剩余风险 / 下一轮入口

1. `verify.sh --web` 仍被历史 migration checksum provenance 漂移（11 mismatch + 662/663 stale marker）阻断；只读审计结论是不能直接替换 hash 刷绿，需按 applied/pending 与 controlled replay 做双 provenance 治理。
2. 全仓 Go 测试组合仍有 `cmd/gateway.TestLiteRequestLogSink_TurnNoContinuesAcrossRestart` 的 macOS 临时目录 `MkdirAll invalid argument` 不稳定性；精确用例 `-count=100` PASS，但不得声称全仓 Go 绿。
3. waterfall ring 是短窗口观测数据；实时请求在抽屉/内嵌切换间可新增 attempts。需要逐字段比较时，应在同一采样时刻 list 与 by-ID API 对账，而不是跨刷新直接比 DOM 文本。

## 已认证浏览器事实

- 已接管用户现有 request-detail 标签，能打开真实单请求/会话轮次页面。
- 本地 `POST /api/auth/token` 用部署同步的 admin 配置返回 HTTP 200 并设置 `llmgw_session`；凭据/token 没有输出或写入文件。
- 指定 request 在中间版本 `119981c0/#2159` 的页面只显示 banner/main，不足以证实 waterfall 正文；必须最终 release 后重测。

## 风险与纪律

- 工作区是共享的；提交前 `git fetch origin main`、`git status`、逐文件 `git add`。
- migration checksum 漂移与 `internal/logging` 波动不能被“UI 测试绿”覆盖。
- 后端历史 DB waterfall 行没有原始 attempts；前端 fallback 只是保留诊断证据，不能声称与已淘汰 ring 的历史抽屉完全等价。

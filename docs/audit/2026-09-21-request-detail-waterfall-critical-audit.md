# 请求详情“调用瀑布”批判式审计与修复（2026-09-21）

- **性质**：用户指定的 focused audit；目标是纠正 `/request-detail/:requestId?mode=request&tab=waterfall` 的内容/导航行为，并验证它不是空壳切 tab。
- **审计窗口**：`119981c02^..HEAD`，另追溯了本地运行版本 `ef694cdf` / 中间部署 `119981c0`。
- **相关域**：D15 可观测性前端一致性、D16 流程闭环。
- **审计纪律**：子代理结论仅作线索；本文所列发现均由主会话读取对应源文件、测试输出或本地运行态复核。

## §〇、结论

原提交 `119981c02` **不能证明任务已经完成**：它只在 `RequestDetailFullscreenView.vue` 中把 `gotoSection()` 的 query 改为 allowlist，未触及 `/dispatch/waterfall` 右侧抽屉、请求详情的内嵌瀑布主体、二者的 attempts 语义，且当时的 `localhost:8782` 实际运行 `ef694cdf`，不包含该提交。因此此前“本地验证通过”结论无效。

本轮将抽屉和请求详情瀑布页收敛为同一 `WaterfallRequestDetailContent` 呈现主体，并将请求详情内部路由统一为显式 `{ mode, tab }` 契约。正常内存瀑布记录优先显示与抽屉相同的 `WaterfallRequest.attempts`；仅在历史 DB by-ID 回退**省略** attempts 字段时，才用既有 journey/log 合并 attempts 补足诊断信息。显式空数组仍显示空态，避免伪造抽屉不存在的尝试记录。

> 截至本文创建时，代码/组件/路由测试与前端门禁已完成；正式全仓 `verify.sh --web` 被既有 migration checksum 台账漂移提前阻断（§三），最终提交 SHA 的重新部署与浏览器验收仍须在本轮收尾中完成，不能提前称为闭环。

## §一、发现与处置

| 编号 | 级别 | 发现 | 证据与触发 | 处置 |
|---|---|---|---|---|
| F1 | P1 | `119981c02` 只清理 `gotoSection` 的 query，未实现“调度抽屉内容内嵌” | `web/src/views/RequestDetailFullscreenView.vue:118-135`（提交前形态）；抽屉独立于 `web/src/components/DispatchWaterfallDetail.vue`，内嵌版独立于 `web/src/components/detail/RequestWaterfallPanel.vue`。从 `/dispatch/waterfall` 点“全屏详情”还绕开 `gotoSection`：`web/src/views/DispatchWaterfallView.vue:89-95`。 | 新增共享 `WaterfallRequestDetailContent`，抽屉与内嵌页都渲染同一正文；新增 parity 钉桩。 |
| F2 | P1 | 原根因结论“陈旧 query 使 named request-detail 路由回到总览”没有证据；且同类 query spread 留在切模式/选轮次路径 | `router.ts:237-243` 的 named route 无 query redirect；旧 `onSelectTurn`/`switchMode` 仍 spread query。真实未认证浏览器会被 guard 送到 `/?login=1`，与“overview”视觉现象混淆。 | `replaceRequestDetailRoute()` 收敛全部请求详情内部替换路径，仅写 `{ mode, tab }` 与明确 requestId；路由集成测试覆盖深链、概览点击、切模式、选轮次。 |
| F3 | P1（本轮自审发现） | 初版共享组件若强制只使用 `WaterfallRequest.attempts`，会让历史 DB fallback 丢失原有 journey/log 合并 attempts | `cmd/gateway/waterfall_by_request.go:23-89` DB by-ID SQL 只构建阶段时间、没有 attempts；原 `RequestWaterfallPanel` 优先使用传入的 merged attempts。独立审查指出后主会话复核。 | `fallbackAttempts` 仅在 `request.attempts === undefined` 时生效；显式 `[]` 保留空态。新增 2 条边界测试。 |
| F4 | P0（并行主线入库后全仓门禁捕获） | `discovery/discovery.go` 的 SQL 字面量用 Go `//` 注释，PostgreSQL 无法解析 | `discovery/discovery.go:766-771,941-946`；`internal/sqlguard.TestNoGoCommentsInSQLLiterals` 稳定失败。来源为 `29b4ea64`。 | 将两处改成 SQL `--` 注释；`go test ./internal/sqlguard ./discovery -count=1` PASS。业务 disabled canonical 保护谓词不变。 |
| F5 | P0（前端门禁） | 八个 locale `annotation.ts` 都有重复 `distribution` 对象键，`vue-tsc` 直接失败 | `web/src/locales/{ar-SA,de-DE,en-US,es-ES,fr-FR,ja-JP,zh-CN,zh-TW}/annotation.ts:147`；均为同值重复键。 | 各删一个重复键；`vue-tsc --noEmit` PASS。 |
| F6 | P0（前端门禁） | Credential heatmap 新增硬编码状态色，严格 color audit 阻断 | `web/src/views/CredentialHeatmapView.vue:1127-1130`；`pnpm run color:check` 原报告 8 个 baseline 外违规。 | 使用现有 `--tone-*-bg` / `--kx-*` 状态令牌；严格色彩门禁恢复 PASS，不更新 baseline。 |
| F7 | P0（最终浏览器验收） | 按 requestId 获取 waterfall 的 handler 已实现但未注册，真实页面请求 `/api/admin/dispatch/waterfall/request/:id` 得到 404/401 后触发前端认证回跳 | `cmd/gateway/main_dispatch.go:152-182` 有 handler；`cmd/gateway/main.go:6783-6791` 只注册列表 `/waterfall`、未注册 `/waterfall/request/`。最终 release 上带 Bearer 的探针为 404；cookie-only 前端请求为 401，随后 URL 变为 `/?login=1`。 | 注册 `mux.HandleFunc("/api/admin/dispatch/waterfall/request/", wrapAdmin(handleDispatchWaterfallByRequest))`，新增启动装配钉桩；待重新部署后进行最终 UI 验收。 |

## §二、关键实现与行为

### 1. 一个瀑布详情正文，两个容器

- `web/src/components/detail/WaterfallRequestDetailContent.vue`
  - 唯一实现请求标识/模型/结果摘要、T0–T9 轨道、阶段表、attempts 行与状态色。
  - `DispatchWaterfallDetail.vue` 只保留 Teleport 右侧抽屉与“全屏详情/打开会话/关闭”动作外壳。
  - `RequestWaterfallPanel.vue` 只保留请求详情加载/错误/空态与 attempts-only tab 外壳。
- `WaterfallDetailParity.test.ts` 直接比较同一 `WaterfallRequest` 在抽屉和内嵌模式下的共享正文文本、阶段行数和 attempts 行数，防止后续再分叉。

### 2. attempts 的可验证语义

1. 内存 ring 或普通 by-ID 响应含 `attempts` 时：抽屉和内嵌页均显示原始 waterfall attempts。
2. DB 历史 by-ID 回退缺少 `attempts` 字段时：内嵌页回退到 `useRequestDetailLoader.mergeRequestAttempts()` 已提供的 journey/log 诊断 attempts；右侧抽屉本身无历史已选 row，不能凭空复原其旧内存记录。
3. 后端明确返回 `attempts: []` 时：两端都显示空态，不用旁路 attempts 覆盖。

### 3. 请求详情内部导航

`RequestDetailFullscreenView.vue` 统一由 `replaceRequestDetailRoute(requestId, mode, tab)` 写入 named `request-detail` 路由。它不再传播 `redirect`、`login`、筛选项等非请求详情 query。该选择的理由是请求详情权限来自服务端认证 tenant context，而不是 URL `tenant` 参数；同时避免将当前页的登录/列表状态反灌到 request-detail。

## §三、验证结果与门禁边界

### 已通过

| 命令/验证 | 结果 |
|---|---|
| `pnpm exec vitest run …`（drawer/inline/route/loader 聚焦套件） | PASS；最终 9 文件、46 tests（包含 parity、DB attempts fallback、显式空 attempts、深链/概览点击/切模式/选轮次） |
| `pnpm run test` | PASS；最终一次完整前端套件 134 文件、954 tests。测试输出仍有既有 i18n mock / router 注入警告，退出码为 0，未被声明为无警告。 |
| `pnpm exec vue-tsc --noEmit` | PASS（locale 重复键修复后） |
| `pnpm run responsive:check` | PASS |
| `pnpm run element:check` | PASS（247 `.vue` 文件） |
| `pnpm run color:check` | PASS；41 条均在既有 baseline 内、无新增（本轮消除了 8 条新增） |
| `pnpm run build` | PASS；生成 `WaterfallRequestDetailContent` 独立前端 chunk |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./internal/sqlguard ./discovery -count=1` | PASS（SQL comment P0 修复后） |
| `go test ./internal/logging -run '^TestAsyncRawDataLogger_OverflowEmitsAnomaly$' -count=3` | 初次 PASS（三次）；随后压力运行复现 goroutine 入队先于 Flush 的竞态，已改为同步 anomaly 入队。 |
| `go test ./internal/logging -count=20` | PASS（修复后 20 轮） |
| `go test ./internal/sqlguard ./discovery -count=3` | PASS（SQL `//` → `--` 修复后） |
| `go test ./cmd/gateway -run '^TestLiteRequestLogSink_TurnNoContinuesAcrossRestart$' -count=100` | PASS；但整个 `cmd/gateway -count=10` 组合仍偶发 macOS temp `MkdirAll invalid argument`，见遗留风险。 |
| `scripts/deploy-local.sh deploy`（中间部署） | PASS；8782 health/ready/version、候选与 active credential decrypt smoke 均 PASS。该 release 为 `119981c0/#2159`，不含本轮后续审查修复，仅作为环境与认证链路证明。 |
| 本地 admin 登录 | PASS；`POST /api/auth/token` HTTP 200，获得 HttpOnly `llmgw_session`（未记录凭据/令牌）。 |

### 未通过 / 未可作为最终绿灯

1. `./verify.sh --web` 在 migration checksum 阶段提前失败：11 个历史 marker mismatch，且 662/663 为 stale registry；本轮未改 `sql/migrations` 或 `docs/db-changelog.md`。这不是本轮 UI/SQL comment 代码导致，但正式全仓门禁目前不能宣称全绿。单独的只读台账审计仍在进行，禁止直接重写 checksum 来刷绿。
2. 首次 `go test ./... -count=1` 同时出现 `internal/sqlguard`（已修）和 `internal/logging.TestAsyncRawDataLogger_OverflowEmitsAnomaly` 一次失败；后者隔离三次 PASS，仍须在最终全仓复跑中观察。
3. 最终 commit SHA 的重部署和浏览器验收尚未发生；中间 2159 版本不足以证明最终代码已运行。

## §四、运行态与浏览器证据

- 部署前 8782：`ef694cdf/#2161`，证明此前所有“验证 119981c02”的说法无效。
- 中间部署后 8782：`119981c0/#2159`，`/healthz`、`/readyz`、`/version` 均健康；该 release 在最终审查修复前生成，仅证明本地蓝绿部署、cookie 登录和目标页面可达。
- 最终 main release `c5b5b12e/#2159` 已在 8782 health/ready/version 与 credential decrypt smoke 后运行。浏览器通过已认证临时代理访问指定 URL 后，目标详情和日志元数据接口均为 200，但 waterfall by-ID API 在当时 release 由于未注册返回 404（cookie-only 请求表现为 401，前端全局认证丢失逻辑将 URL 改为 `/?login=1`）。该实测直接发现 F7，**不能**作为“内嵌瀑布可见”通过证据；修复后需重发 release 再验收。

## §五、健康面

- 请求详情路由正确声明为 named `request-detail`：`web/src/router.ts:237-242`。
- `useRequestDetailLoader` 对 waterfall 按 requestId 懒加载并具备 seq/abort/cache 保护：`web/src/composables/useRequestDetailLoader.ts:440-484`，已有 21 项测试。
- 调度页“全屏详情”仍通过 `openRequestDetailPage(id, { tab: 'waterfall' }, router)` 进入 request-detail：`web/src/views/DispatchWaterfallView.vue:89-95`；新增父组件测试覆盖。
- `docs/audit/252-pg-log-audit-2026-09-19/pg_252_pg17_logs.txt` 含原始请求内容的未跟踪日志，已识别并隔离，**绝不暂存/提交/复制到本报告**。

## §六、遗留风险

1. **历史 DB 瀑布 attempts 不完全可复原**：by-ID DB SQL 只有阶段字段；内嵌页只能使用另一个诊断来源的合并 attempts。若产品要求历史详情与已淘汰 ring 的旧抽屉逐字段完全相同，需要将 waterfall attempts 做专门持久化，而不是前端拼接。
2. **全仓 migration checksum 台账漂移**：11 mismatch + 2 stale registry 阻断 `verify.sh --web`。必须先按历史提交/发布台账审计每项，再决定 checksum 或注册表修复。
3. **全仓 Go 测试稳定性**：首次/复跑全仓 `go test ./...` 在高并发下发生过两种不稳定现象：`cmd/gateway.TestLiteRequestLogSink_TurnNoContinuesAcrossRestart` 的临时目录 `mkdir ... invalid argument`（单包 `-count=10` 通过），以及 `internal/logging` overflow anomaly 未入队。后者已定位为 goroutine 先于 Flush 的竞态并修复；lite sink 仍需在干净宿主/更高并发条件下专门调查，不能据一次单包通过关闭。
4. **migration checksum 台账治理**：11 个 checksum mismatch 与 662/663 stale marker 经只读历史审计确认是登记后内容变更/重编号的部署溯源问题，不可直接替换 checksum 刷绿。需按 migration 的 applied/pending 状态、目标库 marker 和 controlled replay 建立双 provenance 记录。
5. **浏览器最终验收未完成**：必须在最终 SHA release 后，以已认证会话实际检查目标 URL 的当前内容区、从概览点击瀑布、以及 `/dispatch/waterfall` 抽屉与全屏详情主体。

## §七、下一轮提示词

> 先读取 `docs/audit/2026-09-21-request-detail-waterfall-critical-audit.md`。从最终 main SHA 的 `version.json` 重新部署到 8782，确认 `/healthz` / `/readyz` / `/version` 和 bundle `go version -m` 指向同一 SHA。使用已认证浏览器复验用户指定 `/request-detail/f5a6c9991350b81b5bd0fbe2a3f36e55?mode=request&tab=waterfall`：需看见 `waterfall-request-detail` / 阶段表，而非概览；从概览点击“调度瀑布”后 URL 保持同 requestId、query 仅 `mode=request&tab=waterfall`；从 `/dispatch/waterfall` 选择真实行，核对抽屉与全屏正文。随后审计 `verify-migration-checksums` 的 11 mismatch/662-663 stale markers，禁止直接重写 hash；复跑 `go test ./... -count=1` 关注 `internal/logging.TestAsyncRawDataLogger_OverflowEmitsAnomaly` 稳定性。不要暂存 `docs/audit/252-pg-log-audit-2026-09-19/pg_252_pg17_logs.txt` 或其他未跟踪原始日志。

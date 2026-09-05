# 轴F（round2）：会话轮次摘要 / 前端统一 / 可观测性 / 菜单

仓库：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`（只读审计）。基线：docs/audit-2026-09-05-24h-comprehensive.md §二/§四 F 组与 docs/audit-2026-09-05-eight-closures.md 闭环8。

## 发现列表

### F2-#1 [P2] closure-8 硬编码中文击穿仓库 i18n 棘轮门禁（当前为红）
- 证据：`web/scripts/i18n-cjk-count.mjs` 实测 `count 6877 > baseline 6862 (2026-09-04)`；`npx vitest run src/i18n/hardcoded_cjk.baseline.test.ts` **FAILED**。新增来源为闭环8提交 09796c69f：`web/src/views/provider-detail/ErrorDetailTab.vue:119`（`<th>阶段</th><th>重试</th><th>耗时</th>` 硬编码表头，同表其他列均走 `pd()` i18n）、`ErrorDetailTab.vue:129`（'可重试'/'不可重试'）、`web/src/components/RoutingAttemptsTimeline.vue:20-48`（resultLabels/errorKindLabels/stageLabels 三张中文映射表）、`:136-137`、`:144`（'凭据' 前缀）、`:193`（'暂无路由回退尝试记录'）。
- 影响：仓库自设的 CJK 棘轮门禁在 main 上失败；eight-closures 报告的验证记录（仅跑 useCredentialLabels + i18n/parity）漏掉了该 gate。8 语言站点在 en/fr/de 等下看到中文徽标/表头。
- 修复建议：将 error_kind/stage/retryable/result 词表抽为共享 i18n 模块（见 F2-#2），替换硬编码后 `--update-baseline` 降棘轮。工作量：M。

### F2-#2 [P2] routing/error 结构化徽标词表两处独立维护、口径不一致
- 证据：`RoutingAttemptsTimeline.vue:33-55` 有 errorKindLabels（14 条）+ stageLabels（4 条）中文映射；`ErrorDetailTab.vue:109,123-127` 对同一 `error_kind`/`stage` **原样渲染英文 raw**，无映射；retryable 徽标两处重复实现且样式类不同（timeline `badge-success/badge-muted` vs ErrorDetailTab `badge-orange/badge-gray`，`:129` vs timeline `:186`）。
- 影响：同一个 error_kind（如 `upstream_overloaded`）在请求时间线显示中文徽标、在凭据错误表显示英文 raw；新增低基数词需改两处，词表漂移无门禁。
- 修复建议：抽 `web/src/utils/errorVocab.ts`（词表+格式化+badge class 单一来源）供两处消费，并与后端 `supplier_error_logger.go` 的 ErrorKind/stage 枚举注释对齐。工作量：S。

### F2-#3 [P2] credentialDisplayName 统一存在漏网：journey 事件路径仍渲染裸凭据 ID
- 证据：`RoutingAttemptsTimeline.vue:91-92`（`const node = event.attempt?.credential_id || event.credential_id` → `t('requestJourneys.detail.node', { node })` 直接输出数字）、`:94-97`（`from_credential_id`/`to_credential_id` 裸数字）；仅 attempts 列表分支（`:141-145`）走了 `credentialDisplayName`。另 `web/src/composables/useCredentialLabels.ts:98-99` 默认前缀 '凭据' 为硬编码中文。
- 影响：闭环8「绝不回显 raw」目标在 journey-event 展示路径未达成；node_switch 事件的 from/to 也裸奔。
- 修复建议：eventDetails 内对 credential_id 统一过 `credentialDisplayName`；前缀改 i18n。工作量：S。

### F2-#4 [P2] 闭环1 新增可观测性字段（supplier/error_code/request_id）未进前端展示；selection 打点无展示出口
- 证据：后端 `admin/vendor_credential_error_handlers.go:63-64,222` 已透传 `supplier`/`error_code`/`is_retryable`/`stage`/`latency_ms`/`request_id`；前端类型已同步（`web/src/api/vendor-credential-error.ts:42-46`），但 `ErrorDetailTab.vue:118-135` 失败表**没有 supplier / error_code 列**，`request_id` 仅作 row key（`:120`）不可点击跳转 request-detail。`domains/hooks/observability/telemetry/selection_writer.go` 落库的 auto_route_selections（request_id/canonical_id/candidate_rank 等）无任何 admin API 与前端消费。
- 影响：本轮可观测性投入止步于「入库+API」，人眼不可见；凭据错误→请求详情的关联断在 UI。
- 修复建议：ErrorDetailTab 增 supplier 列 + request_id 链接复用 `openRequestDetailPage`；selection 展示作为独立工作包。工作量：S（列）/L（selection）。

### F2-#5 [P2] 已知遗留 F-#2/#3/#4 核实：仍未修，且存在跨请求正文串页的强证据
- 证据：`web/src/components/detail/UnifiedRequestSessionDrawer.vue` 自带加载逻辑、未迁移 `useRequestDetailLoader`：
  - **并发覆盖**：`loadRequest`（:132-142，omitBody Promise.all）与 `ensureBodies`（:175-195）竞态——ensureBodies 先返回写入完整 body 后，loadRequest 的 bodyless `unified` 落地覆盖（仅检查 `currentSeq !== loadSeq`，loadSeq 未变）；`bodiesLoadedFor` 已置位导致 chat tab 不会再拉。
  - **换轮不 bump seq / 不清态**：`onSelectTurn`（:214-217）、`openAsRequest`（:219-224）直接 `loadRequest`，不清 `log/unified/sessionSnap/warnings/bodiesLoadedFor`；且 `ensureBodies` 的守卫 `if (unified.value?.bodies || log.value?.request_body)`（:177-180）在切到新轮时看到**上一轮残留 body** → 直接把新轮标记 body 已加载，**展示 A 轮正文于 B 轮视图**。
  - **bodiesLoading 粘滞**：`ensureBodies` finally `if (seq === loadSeq)`（:192-194），props.requestId 变更 bump loadSeq 后 bodiesLoading 永久 true（watch :98-119 不重置它）。
- 影响：错误正文/串页误导排障；已知三项全部健在。
- 修复建议：整体迁移共享组合式函数 `web/src/composables/useRequestDetailLoader.ts`（含 per-request 缓存、seq、resetTransientState、ensureBodies 以 requestId 为键）。工作量：M。

### F2-#6 [P2] 已知遗留 F-#8 核实：err.Error() 外泄仍在，附 `no rows` 字符串比较新证据
- 证据：`admin/session_turns_v2.go:601`（"query turn bodies failed: "+err.Error()）、`:757`（"query snapshot failed: "+err.Error()）、`:802`、`:819` —— PG 错误（含 SQLSTATE/约束名/表名）直达 admin API 响应。全 admin 包同类模式 241 处。另 `:329`、`:751` 用 `err.Error() == "no rows in result set"` 判空，pgx v5 应 `errors.Is(err, pgx.ErrNoRows)`——错误被包装时 404 退化为 500。
- 修复建议：本组 4+2 处先行（errorsx.SanitizeErrorText / errors.Is），全仓治理另立工作包。工作量：S（本组）。

### F2-#7 [P3] 已知遗留 F-#6 核实：双轨摘要仍在；且 fallback 与持久化两条投影实现存在行为分歧
- 证据：前端派生 `web/src/components/detail/messageHelpers.ts:180-195`（summarizeTurnUserInstruction，6 行截断）+ `SessionTurnsSyncPane.vue:148,168-188`，与后端持久化 `domains/sessiondigest`（260 rune）+ `TurnListItem.digest` 并存，同一轮两处摘要口径不同。此外 writer 端 `domains/sessiondigest/digest.go` 与 admin fallback 端 `admin/turn_digest.go` 是两套相似实现，存在分歧：媒体标记（sessiondigest 每块 `[附图×1]` 逐条换行 vs admin 聚合 `[附图×N]`）、verdict 事件类型（sessiondigest buildEvents:281-286 恒 `warning` vs admin verdictType:340-346 block/deny 判 `error` → TurnDigestCard 状态 chip danger/warning 不同）、metrics clamp（admin clampPtr:400-411 钳位 [0,1]，sessiondigest 不钳）。
- 影响：fallback 行（migration 456 前/写失败）与持久化行展示不一致；长期双实现维护成本。
- 修复建议：短期让 SessionTurnsSyncPane 卡片优先消费 list API 已下发的 `TurnListItem.digest`；长期 admin fallback 复用 sessiondigest 包。工作量：M。

### F2-#8 [P3] 已知遗留 F-#12 核实：z-index 三套体系无共享 token 仍在
- 证据：全前端 grep `z-index` 离散值 28+（1/2/3/5/10/20/73/77/90/100/700/715/1000/1100/1400/2900/3000/3050/9999…）；无任何 `--z-*` token（grep 无命中）。Element Plus 弹层、自绘 mask（`NodeDetailDrawer.vue:211-212` 3000/3001、`NodeDetailConcurrencyPanel.vue:253` 3050、`RequestJourneyQueues.vue:302` 2900）、页面内 stacking（1-100）并行。
- 修复建议：tokens 阶梯 `--z-sticky/--z-overlay/--z-drawer/--z-modal`。工作量：S-M。

### F2-#9 [P3] TurnDetail.title/summary 为死契约，摘要 fallback 链实际断裂
- 证据：`web/src/api/sessions_v2.ts:52-53` 声明 `title?/summary?`；后端 `turnDetailV2Response`（`admin/session_turns_v2.go:190-200`）不产出这两字段 → `TurnDigestDrawer.vue:40-41` 的 `detail.value?.title/summary` 恒空；且三个调用点（`SessionDrilldownPanel.vue:70-75`、`SessionDetailPage.vue:100-106`、`SessionTurnsSyncPane.vue:519-521`）均未传 `title`/`summary` props → digest 为 null 时 TurnDigestCard 只能显示 `t('turnDigest.empty')`，列表页已有的 title/summary 数据（`TurnListItem` 有）没有接进抽屉。
- 修复建议：调用点把 `TurnListItem.title/summary` 传入抽屉 props（或后端 detail 响应补字段）。工作量：S。

### F2-#10 [P3] TurnDigestDrawer.openAttachment 将附件级失败写入全局错误态
- 证据：`TurnDigestDrawer.vue:92-95`：url 为 null 时 `error.value = t('turnDigest.noAttachments')` → 模板 `v-else-if="error"`（:193）把整个 tabs 替换为错误态，且 reload 前不恢复（附件错误与加载错误共用一个 ref）。
- 修复建议：附件失败用独立的行内提示（如 message/局部 ref）。工作量：S。

### F2-#11 [P3] 耗时/时间格式三套并存
- 证据：耗时 `TurnDigestCard.vue:44-49`（ms / 2位s / m s）vs `RoutingAttemptsTimeline.vue:65-68`（ms / 1位s）vs `ErrorDetailTab.vue:132`（`${ms}ms` 原样）；时间 `ErrorDetailTab.vue:27-30`（toLocaleString）vs timeline `:112-121`（Intl 时分秒.毫秒）vs `TurnDigestDrawer.vue:63-66` waterfall 原样 ISO 字符串。
- 修复建议：共享 formatDuration/formatDateTime util（对齐 pill/表格场景）。工作量：S。

### F2-#12 [P3] 附件通道统一后的残余重复与死代码成对保留
- 证据：URL 拼接 `TurnDigestDrawer.vue:83-87` 与弃用的 `SessionTurnDrawer.vue:76-78` 重复实现（per-segment encodeURIComponent）；弃用导出 `getAttachmentSignedUrl`（`sessions_v2.ts:147-161`）与后端 404 stub（`admin/session_turns_v2.go:66-71`）成对保留（注释已声明待 signing 端点落地或删除）。
- 修复建议：抽 `attachmentUrl(objectKey)` util；为 stub 删除立 issue 带过期条件。工作量：S。

### F2-#13 [P3] nav 校验 fail-closed 的运维可见反馈不足
- 证据：`cmd/gateway/plugin_runtime_init.go:45-48`：manifest invalid 仅 `fmt.Fprintf(os.Stderr)` 后 `continue`——不调用 `reg.SetPluginStatus(id,"failed")`（bindings 失败路径 :51-54 会置 failed），无 slog/metric；插件整体（含非 nav 功能）静默消失，注册表无痕。前端 `web/src/api/plugins.ts:17-22` fetchPluginNav 失败静默返回 []。
- 影响：fail-closed 正确，但排障只能靠 stderr；操作员无法从插件状态页发现「菜单校验失败」。
- 修复建议：置 status=failed + slog.Warn（含 pluginID 与原因）+ 一个 invalid_manifest_total 计数器。工作量：S。

### F2-#14 [P3] loading/empty/error 三态无共享组件，Element Plus 与自绘混用
- 证据：TurnDigestDrawer（el-empty + el-button）、ErrorDetailTab（.empty-hint/.alert-danger 自绘 div）、RoutingAttemptsTimeline（.routing-empty 自绘）、SessionTurnsSyncPane（自绘）四套并存。
- 修复建议：至少统一「错误+重试」模式为共享小组件（沿用 turnDigest.retry 键模式）。工作量：M。

## 已确认闭环（本轮复核通过，不重复报告）

1. **附件打开修复**：TurnDigestDrawer 改走 `GET /api/attachments/{object}`（`TurnDigestDrawer.vue:77-103`），404 签名端点弃用注记齐全；vitest TurnDigestDrawer 用例通过。
2. **retry 文案 8 语言齐全**：locales/{ar-SA,de-DE,en-US,es-ES,fr-FR,ja-JP,zh-CN,zh-TW}/turnDigest.ts 各 22 个顶层键完全对齐（含 retry/openingAttachment/noWaterfall），i18n/parity.test.ts 在岗。
3. **to_regclass 守卫**：`db/session_summaries_schema.go:45-55` 表缺失时告警跳过而非启动崩溃。
4. **nav_validate fail closed**：`plugin-runtime/nav_validate.go:45-113`（路径白名单/../反斜杠/协议段拒绝、去重、64 页上限、group 低基数词表、label_key 点分 key、order ±10000、tenant_only×platform_ops 互斥）经 `manifest.go:90-92` 接入 manifest.validate()，校验失败即整个 manifest 无效；与 `menu-config.json`（文档快照，source=gateway-appNav）及 `appNav.ts` 的 labelKey 惯例一致。
5. **摘要链路契约健康**：写时持久化版本化 envelope（`session_writer_v2.go:307-319`：schema_version/algorithm_version/generated_at/source + payload，260 rune 对齐），读端 `persistedDigestOrFallback`（`session_turns_v2.go:426-455`）版本化回退 + `llmgw_session_turn_digest_fallback_total{reason}` 低基数计数；去格式扎实（tool_result 排除、rune 安全截断、工具只留 name/count 不留 args）。
6. **双向回溯可用**：digest↔raw（抽屉内 request/response tab）、turn→request（openRequest 带 request_id，`SessionTurnsTimeline.vue:190-206`）、request→turn（`SessionTurnsSyncPane.vue:202-219` 按 activeRequestId 对齐选中轮）、turn 卡→摘要按钮（stsp-show-digest/stt-show-digest）。
7. **credentialDisplayName attempts 分支**与 ErrorDetailTab 阶段/重试/耗时列结构已落地（缺口见 F2-#1/#2/#3）。
8. `npx vue-tsc --noEmit` 零错误。

## 冗余/待清理

- `getAttachmentSignedUrl`（sessions_v2.ts:147-161）+ 后端 404 stub（session_turns_v2.go:66-71）：成对死代码，需立清理 issue。
- `SessionTurnDrawer.vue`：已 DEPRECATED（2026-08-25）仍保留，且 `:8` 的 `headers` import 已无使用。
- `admin/turn_digest.go`（fallback 投影）与 `domains/sessiondigest/digest.go`（writer 投影）双实现，分歧清单见 F2-#7，建议合一。
- z-index/耗时/时间/词表四处样式口径分散（F2-#2/#8/#11）。

## 统计

**P0 = 0，P1 = 0，P2 = 6（F2-#1~#6），P3 = 8（F2-#7~#14）。**

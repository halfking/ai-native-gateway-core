# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] - 2026-07-13

### Added (customer UI closure — P0 journey gap)
- **客户面向 API（无需登录即可访问）**：
  - `GET /api/system/license/status` — 当前授权状态（none/active/grace/expired/revoked）
  - `GET /api/system/license/info` — License 详情（features / max_devices / active_devices / 心跳时间）
  - `POST /api/system/license/activate` — 在线激活（License Key）
  - `POST /api/system/license/offline-activate` — 离线激活（粘贴 SignedLicense + ActivationCode）
  - `POST /api/system/license/offline-request` — 生成离线激活请求（base64 envelope）
  - `POST /api/system/license/heartbeat` — 手动触发心跳
  - `GET /api/system/upgrade/status` — 当前版本 vs 渠道最新版本
  - `POST /api/system/upgrade/check` — 触发升级检查
- **客户面向 Vue 组件**：
  - `views/ActivationWizard.vue` — 4 步激活向导
  - `views/LicenseInfoView.vue` — License 详情 + 心跳管理
  - `views/UpgradePanel.vue` — 升级面板
  - `components/UpgradeBanner.vue` — 全局升级提示横幅
- **公开路由**：`/activate` / `/license` / `/upgrade`（`meta: { public: true }`）。`App.vue` 在这些路由上跳过 `/api/auth/me` 探测，避免被 API 客户端的 401 重定向锁死。

详细说明：`docs/changelogs/2026-07-13-customer-ui-closure.md`。

### Fixed (ops auth hydration + autoupdate JWT)
- **运维概览 403 / 刷新后租户视角**：`/api/auth/me` 返回 `{ user, access_token }` 时正确持久化 `user.role`，兼容历史 localStorage 嵌套结构。
- **autoupdate 升级日志 401**：移除与 Echo JWT 冲突的 `AUTOUPDATE_ADMIN_TOKEN` 中间件，运维概览加载不再被踢到 `/login`。

### Added (deployment management hardening — design + rotation checklist)
- **部署管理硬化设计**：`docs/superpowers/specs/2026-07-13-deployment-management-hardening-design.md` 落地第五轮首个部署切片设计；154（deploy/verify）与 245（deploy/verify/rollback）成为首批 canonical 目标，186 退役，252/184/kaixuan 延后统一，引入双层锁、版本化 release bundle、SOPS `.env.252.enc` 与 `.env.kaixuan-1.enc` 复用现有 recipient 方案。详细说明：`docs/changelogs/2026-07-13-deployment-management-hardening.md`。

### Added (route-incident diagnosis, Phase 2 mutating actions + audit + evidence)
- **路由事件诊断 Phase 2 (mutating + audit + 证据导出)**：
  落地 spec `2026-07-13-route-incident-diagnosis-design.md` 第二期。
  详细说明：`docs/changelogs/2026-07-13-route-incident-phase2.md`。
  视觉验证：`ui-verify-route-incident-phase2-{actions,audit,confirm,export,overview}-20260713-170608.png`。
  - 持久化（追加）：
    - `routing_audit_log` 不可变审计表（idempotency_key 唯一索引、outcome、pre/post snapshot、actor_ip_hash 不存原值）；
    - `diagnostic_runs` 不可变诊断运行表（route_key + sanitized result）；
    - migration `390_routing_audit_log.sql`；
    - `db/db.go::ensureRouteIncidentPhase2Schema` 启动时建表。
  - Action 基础设施（`domains/routeincident/action_infra.go`）：
    单一调度入口 `dispatchAction`，同一事务内 version 检查 / pre-snapshot / executor / audit+run / 提交；幂等重放 ON CONFLICT DO NOTHING + 缓存回放；输入校验 `reason`（≤256 字符）/ `confirmation_token`（SHA-256 哈希后存）/ `idempotency_key`；参数 allow-list 禁止任意 URL / raw body / shell / SQL；客户端 IP / 凭据 secret / auth header / raw body 永远不进库。
  - 5 mutating actions + 2 diagnostic tests：recover / reprobe / release_slot / reset_slots / reset_availability / direct_upstream_test / through_gateway_test 全部走同一 dispatcher + 不绕过下游健康检查原语。
  - 证据导出：BuildEvidenceExport 拼 {Run, Incident, Events, Timeline}；SHA-256 over canonicalized JSON；MaxEvidenceExportBytes = 2MiB；不含凭据 / cookie / body / IP / UA / raw upstream error / session 标题。
  - Admin API：GET /audit, GET /runs, POST /{action}, GET /export?run_id=...
  - 前端：第 8 节操作面板（7 按钮 + destructive/test/slot 视觉分级）+ 第 9 节审计日志 + 二次确认弹窗（reason + confirmation_token + allow-list 附加参数）+ 证据导出 banner；焦点陷阱覆盖 modal 与 drawer 互斥。
  - 不变量：写操作要求 reason + confirmation_token + idempotency_key + version match，缺一即拒绝（400）；`idempotency_key` 唯一索引拒绝重复执行；不绕过下游健康检查；跨租户 404；任何失败（含 stale state）都写 audit row；Evidence 永远不含凭据 / cookie / raw body / 上游 URL / 客户端 IP / UA / session 标题。

### Added (route-incident diagnosis, Phase 1 read-only)
- **路由事件诊断 (Phase 1 read-only)**：泳道上的"诊断"入口与
  右侧诊断工作台落地。Backend 状态机负责
  `healthy → active → recovering → recovered` 切换，3 连续终态失败
  触发，5 连续终态成功恢复，1-4 连续成功不隐藏且显示
  `Recovery n/5`。客户端 key 错误、客户端取消、其它非因供应商
  原因的失败 **不计入诊断触发**（spec 不变量）。
  详细说明：`docs/superpowers/specs/2026-07-13-route-incident-diagnosis-design.md`，
  视觉验证：`ui-verify-route-incident-{active,recovering,other-disabled,drawer-insufficient-data,mobile-drawer}-20260713-150319.png`。
  - 持久化：新增 `route_incidents` 聚合表 + `route_incident_events`
    不可变事件表（migration `389_route_incidents.sql`，
    `db/db.go::ensureRouteIncidentSchema`），事务式
    `SELECT ... FOR UPDATE` + idempotent 事件插入。
  - 异步观察器：`domains/routeincident.Observer` 挂钩
    `telemetry.SetOnRequestLogPersisted`（注意：不是 Emitted 钩子），
    有界队列 + bounded backoff 重试。
  - 只读 API：`admin/route_incidents.go` 暴露
    `GET /api/admin/route-incidents` / `{id}` / `{id}/events` /
    `{id}/timeline` / `stats`（super-admin only，跨租户资源 404）。
  - SSE 增量：`admin.LiveStreamSSEHub.PublishIncidentUpdate` 发送
    `incident_update` envelope，与现有 request envelope 同管线。
  - 仪表盘：`web/src/components/RouteIncidentDrawer.vue` 7 节
    只读工作台（状态 / 当前路由 / 证据概览 / 近 24 小时 /
    请求与转换对比 / 资源快照 / 日志与请求），含 Escape 关闭、
    focus trap、reduced-motion 支持；移动端全视口。
  - 泳道入口：`SwimLane` 新增诊断按钮（`active`/`recovering`
    状态机驱动 + `Other` 泳道禁用 + tooltip 解释原因），保留
    旧版错误率启发式作为无状态机时的兜底。
  - 状态合并：`composables/useRouteIncidents.ts` 把 SSE 增量
    折成 per-lane 索引；`liveStreamStore.ts` 解析
    `incident_update` envelope。
  - 测试：Go 单测覆盖 DecideState 全部转移 + 脱敏 + observer
    队列溢出/非路由失败过滤；Vitest 覆盖 Vue 状态机 reducer。
  - 视觉验证：Playwright 在 desktop 1280×900 与 mobile 375×812
    视口下抓取 6 张截图（`ui-verify-route-incident-*.png`）。
  - 第一期 **不** 包含 DiagnosticRun / 直连/经网关测试 /
    立即恢复 / 证据包导出（spec §"Phase Two"），所有 mutating
    action 接口暂不实现。

### Fixed (P2 - Audit Round 4)
- **Docker HEALTHCHECK**：Dockerfile 增加 `HEALTHCHECK` 配置（30s 间隔、3 次重试），使用 wget 检查 `/healthz` 端点
- **版本比较支持预发布标签**：`autoupdate/version.go` 增加 alpha/beta/rc 支持（如 `2.4.2-alpha.1`、`2.4.2-beta.2`、`2.4.2-rc.1`）。预发布版本按 `alpha < beta < rc < release` 排序，数字后缀按数值比较。新增 `IsPreRelease()` 辅助函数和 `version_test.go` 测试
- **License 离线验证增加过期/吊销检查**：`licensing/offline.go` 的 `VerifyOfflineLicense` 现在检查 `ExpiresAt` 和 `RevokedAt`，与在线验证保持一致。之前离线 license 可在过期/吊销后继续使用

### Fixed (P1 - Audit Round 3)
- **autoupdate Admin API 认证**：所有 `/api/admin/releases/*` 和 `/api/admin/autoupdate/*` 端点增加 Bearer token 认证，token 从环境变量 `AUTOUPDATE_ADMIN_TOKEN` 读取（未配置时使用默认开发 token）
- **数据库迁移协调**：`Installer` 增加可选的数据库迁移命令配置（`SetMigration(cmd, args...)`），在二进制替换后、服务重启前自动执行迁移
- **GPG 签名验证**：新增 `GPGVerifier` 和 `Downloader.DownloadWithGPG` 方法，支持验证下载文件的 GPG 签名（`.sig` 或 `.asc`），以及 SHA256SUMS 文件签名验证

### Fixed (P0)
- **Gemini 流式响应实时转换**：Gemini 原生流式端点不再使用
  `httptest.ResponseRecorder` 缓存整个 ChatHandler 响应；新增可 flush 的
  SSE 桥接 writer，逐个将 OpenAI chunk 转换为 Gemini `candidates` 格式，
  同时保留上游错误响应，并将重复的 `[DONE]` 终止标记收敛为一个。
  详细说明：`docs/changelogs/2026-07-13-gemini-stream-sse-fix.md`。

### Fixed (P0)
- **请求记录保存失败（audit-ir-multimodal 部署后）**
  `commit 5447bf6b1` (audit-ir-multimodal, 2026-07-13 05:03) 在
  `RequestLogEntry` 新增 5 个 multimodal 字段，并在
  `domains/hooks/observability/telemetry/client.go` 的 INSERT 中写入；
  配套 migration `350_multimodal_token_fields.sql` 只给 `request_logs`
  **父表**加列，**未触及 `request_logs_hot`（生产实际写入目标）**。
  v990 二进制 (05:52 部署) 之后所有 INSERT 立即报
  `column "reasoning_tokens" does not exist`，整个事务回滚导致
  `usage_ledger_hot` 一同停止写入；失败被 `fallback.WriteRequestLog`
  写入 104MB 的 jsonl.gz，日志无任何错误记录。
  本次修复：
    - 新 migration `2026-07-13-multimodal-token-fields-hot.sql` 给
      `request_logs_hot` / `usage_ledger_hot` / `request_logs`（父表
      + 所有 monthly partition）加 5 列 + `idx_request_logs_hot_multimodal_usage`。
      已直接应用到 252 / pg-252-pg17。
    - 重写 `client.go` 中 INSERT VALUES 占位符 `$2..$79`，与列名顺序
      逐项对齐；同样重写 UPDATE SET 子句 + VALUES（71 列 → `$2..$75`），
      修复 `cost_display = COALESCE($17, ...)` 这种把
      multimodal 占位符复用到 cost 列的错位 bug。
  验证（生产 v992 部署后）：`request_logs_hot` /
  `usage_ledger_hot` 重新开始写入；`success` /
  `total_tokens` / `request_status` 在 UPDATE 后正确填充。
  详细 RCA + 验证： `docs/changelogs/2026-07-13-request-log-save-fail-fix.md`。

### Added
- **data-lifecycle Hot 表迁移 UI 重构 + credential_model_index 写入去重**
  解决 252 pg17 高速增长表（credential_model_index_2026_07 ≈ 836k 行 / 月）"短时间插入大量相似数据"
  的增量模式问题；同步修正 https://llm.kxpms.cn/admin/data-lifecycle 中"Hot 表数据迁移"的交互
  与迁移功能。

  - **UI 修复**：`<el-select>` 下拉换成 `<el-radio-group>` radio 按钮组，避免"保留 1 天"实际只
    保留 1 小时的标签/值不一致 bug（`retentionHours` 1/3/7/30 与 i18n key `1day/7day/30day`
    单位错配）。前端 `retentionDays` 字段语义清晰，在 `promoteHotTable` 调用前 ×24 转 hours。
    新增 `retention.3day` 与 `days` i18n key，覆盖全部 8 个 locale（zh-CN/TW/en-US/ja-JP/
    ko/de-DE/es-ES/fr-FR/ar-SA）。

  - **后端写入去重**：`bg/auto_index_refresher.go::rollupCredentialModelIndexSQL` 增加
    `WITH fresh AS (...) SELECT f.* FROM fresh f WHERE NOT EXISTS (...)` CTE。
    仅与"上一次 bucket"对比 success_rate / p95_latency_ms / score_smart / score_speed_first
    / score_cost_first 五个核心指标，全部一致则跳过 insert。
    实测 252 prod 数据：21,038 hot rows / 33 buckets → 20,433 (97.1%) 是完全重复。
    dedup 后 hot 表 5-min 写入量从 ~600 行降至仅在 metrics 真正变化时插入；
    配合现有 partition_manager 1h promote + 24h retention 窗口，hot 表规模从
    ~14,400 行（理论上限）稳定在 ~600 行左右。

  - **文档**：`docs/changelogs/2026-07-13-data-lifecycle-radio-and-dedup.md`。

### Added (prior batch)
- **/model-pricing 标准模型定价与多模态计费**
  `model-pricing` 是用户视角的"标准模型定价清单"，本次纠正了三处偏差：
  (1) 列表每个 canonical model 只占一行，移除"厂家（vendor）"列，用户不需要关心供应商；
  (2) 强化批量操作，新增「全部恢复全局」「填入当前全局」两个动作，与原「批量定价」「粘贴到所选」共同覆盖 4 种典型工作流；
  (3) 为多模态模型（gemini-2.5-flash-image、doubao-seed、glm-4v 等）扩展独立的 image / audio / video token 单价，与 text token 区分计费。
  - DB：新增 `model_credit_rates.credits_per_1m_{image,audio,video}_tokens` 3 字段 + `manual_{image,audio,video}` 3 标志位，迁移脚本 `deploy/sql/docs/pricing/2026_07_13_model_credit_multimodal.sql`（idempotent）。
  - Go 后端：`BaseRateSet` / `ModelRateValues` / `storedModelRates` / `AdminModelRateRow` / `ModelRateUpsert` 加 6 字段；`ListAdminModelRates` SQL + Scan 更新；`UpsertModelRate` + `ResetModelRateFields` 支持新 key；新增 `ChargeRequestMultimodal(TokenUsage)` + `BatchResetModelRates` + `BatchFillGlobalModelRates` + `/api/admin/maas/model-rates/batch-reset` + `/api/admin/maas/model-rates/batch-fill-global` route。`ChargeRequest` 旧 4 维签名保留不变，向后兼容。
  - Go credits：新增 `TokenUsage` 与 `CalcCreditsMultimodal` 作为单一计费入口；旧 `CalcCredits` 变薄为 4-tuple wrapper。
  - 前端：`AdminMaasModelRate` / `MaasModelRateUpsert` 类型扩展；`StandardModelPricingView` 删除 vendor 列、新增「模态」列（按 modality 着色 badge）、手工状态徽章从 N/4 升 N/7；批量操作栏新增「全部恢复全局」「填入当前全局」按钮；手工定价 modal 拆分为「文本 Token 维度」「多模态 Token 维度」两段。
  - 文案：zh-CN / en-US `standardModelPricing` 文案补全；修复 en-US 既有中文残留。
  - 单测：`maas/model_rates_multimodal_test.go` 7 测试函数 + 7 subtest 覆盖。
  - 完整设计：`docs/changelogs/2026-07-13-standard-model-pricing.md`。
- **错误触发的主动探测 (Error-Triggered Active Probe)**  
  连续失败 ≥2 次时立即直连上游探测（0s 延迟），按 5s → 30s → 2m → 5m → 15m backoff 链执行最多 5 轮。
  探测结果写入 `request_logs`（`task_type='probe_triggered'`），自动推送到实时请求流，前端可通过
  `IsProbe/ProbeOrigin/ProbeAttempt` 字段一键过滤"仅探测"。用于快速隔离"上游 vs gateway vs 客户端"问题。
  - 新增 `bg/active_probe_worker.go` + `bg/active_probe_executor.go` + `bg/active_probe_emitter.go`
  - 新增 `bg/active_probe_backoff.go` 计算 backoff 链
  - 新增 `settings/spec_error_probe.go` 提供 4 个平台级配置项
  - 新增 `admin/probe_request_info.go` 提取探测元数据
  - 修改 `domains/credentialstate/manager.go` 在 `UpdateOnFailure` 触发探测
  - 修改 `cmd/gateway/main.go` 构造并 wire `ActiveProbeWorker`
  - 修改 `admin/live_stream_sse.go` / `admin/live_stream_redis_store.go` 透传探测字段到实时流
  - 完整设计文档: `docs/自检功能/04-error-triggered-probe-design.md`

### Security and Reliability
- Fixed Feishu callback validation to parse Unix-second timestamps, reject
  excessive clock skew, and compare signatures in constant time.
- Fixed the five operations-platform routes rendering an empty `RouterView` by loading their views statically, and added isolated read-only tenant license/update routes backed by tenant-scoped APIs.
- Fixed unsafe Element Plus table-slot destructuring in the five ops views and two tenant views.
- Fixed the five operations-platform routes rendering an empty `RouterView` by loading their views statically, and added isolated read-only tenant license/update routes backed by tenant-scoped APIs.
- Fixed unsafe Element Plus table-slot destructuring in the five ops views and two tenant views.
- Protected License Authority administration endpoints with an explicit bearer token and separated them from instance client routes.
- Added JWT issuer/audience validation, refresh-token rotation, stable license binding, request body limits, and replay nonce scoping.
- Hardened offline upgrade manifests against empty inventories, invalid checksums, path traversal, symlink escapes, and partial `psql` execution.
- Fixed migration discovery for nested `up/` directories and made integration-test failures visible instead of silently skipped.
- **P0 部署脚本凭据修复**：`deploy-154.sh` / `deploy-kaixuan1.sh` 移除硬编码 SSH 密码，
  改为 `DEPLOY_SSH_PASS` 环境变量注入；未设置时脚本直接退出并提示使用 SSH 密钥登录。
- **P0 License 硬件指纹验证修复**：`licensing/verify_local.go::verifyFingerprint` 之前将
  `license.HardwareHash` 复制到每个 Fingerprint 字段，导致 `MatchScore` 形同虚设。
  改为先做 `subtle.ConstantTimeCompare` 哈希匹配，未命中再 fallback 到 fuzzy 匹配。
- **P0 register_handler license_key fallback 修复**：原代码在新设备场景下把
  `LicenseKeyHash` 当成 `license_key` 写入数据库，新增 `licensing.Store.GetLicenseByHardwareHash`
  后改为：通过 hardware_hash 找到对应 license，找不到时返回 404。
- **P1 心跳签名占位符修复**：`installer/internal/enrollment/heartbeat.go` 之前
  设置 `X-Signature: "placeholder-signature"`。改为基于 instance_token 派生的
  HMAC-SHA256(timestamp + nonce + body)，并补齐 `X-Timestamp` + `X-Nonce` 头，
  与服务端 `middleware/sigverify.go` 格式对齐，方便后续启用服务端校验。

### Fixed
- **错误触发的主动探测 backoff 链 off-by-one（BUG #1）**
  `bg/active_probe_worker.go::processOne` 在 attempt 失败后调用
  `computeBackoff(attempt + 1)`，把链路 `DefaultErrorProbeBackoffChain =
  [5s, 30s, 2m, 5m, 15m]` 的第一项 5s 静默丢弃。实际序列是
  `0s → 30s → 2m → 5m → 15m`（4 次重试，22m30s 才放弃），而不是设计文档
  §3.2 中规定的 `0s → 5s → 30s → 2m → 5m → 15m`（5 次重试，7m35s 放弃）。
  修复：改为 `computeBackoff(attempt)`，并补 `TestRetryBackoffUsesCorrectChainEntry`
  + 源码扫描型 `TestRetryBackoffCallShape_NoOffByOneInWorkerSource`（在
  `bg/active_probe_worker.go` 再次出现 `computeBackoff(attempt + 1)` 即报错）。

- **「递增退避探测 (30s → 2m → 5m)」分级回退死代码（BUG #2）**
  `domains/credentialstate/manager.go::UpdateOnFailure` 在瞬时故障 ≥3 次
  分支里，根据连续失败次数算出 `backoff`（≤3 → 30s，≤5 → 2m，否则 5m），
  但调用 `m.credProbeV2Submitter(credID)` 时**立刻触发**，没有 `time.AfterFunc`
  等任何延迟；而 `credProbeV2` 内部自有 5min fastReprobeDelay，所以无论
  连失多少次，最终探测间隔都被强制覆盖为 5min。注释里的"分级回退"成了
  仅注释可见的特性。
  修复：新增 `Manager.scheduleCredProbe(credID, model, backoff)`，按 credential 维度去重，
  只保留更早的待执行探测；到期后调用 `CredentialProbeV2.ProbeNowAsync`，不再叠加内部
  5min fast-reprobe 延迟。定时器支持 generation 校验、关停取消与回调等待。补
  `TestManager_TieredReprobeUsesBackoff` 运行时测试。

- **错误触发主动探测闭环与生命周期修复（审计）**
  `ActiveProbeWorker` 现在正确接入 `CredentialStateManager`，探测成功可恢复路由；
  `ActiveProbeEmitter` 同时写入专用 `parent_request_id` 字段和兼容的
  `client_request_id`；补充 worker/probe/state manager 的幂等关停、取消竞态保护，
  避免回调写入已关闭依赖。

- **设计文档时间线示例（BUG #3）**
  `docs/自检功能/04-error-triggered-probe-design.md` §2.2 时序段写的是
  30s→2m→5m→15m，与 §3.2 链路定义不一致。修正为 T+15s、T+45s、T+2m45s、
  T+7m45s 五次重试，并注明 chain[4] 的 15m cap 在默认 MaxAttempts=5 下
  不会真正被消费。

- **data-lifecycle 异步任务状态修复**：drop partition 任务完成后正确设置
  `JobStatusSucceeded`；cancel 操作改为 `JobStatusCancelled`（原误设为 Failed）；
  `StartJob` 修复 `cancelFn` 未赋值导致 cancel 无法中断。统一 `/drop` 和
  `/drop-async` 路由走异步分发。前后端 `AsyncJobResponse` 字段对齐
  (`job_id`→`run_id`, `poll_url`→`polling_url`)。清理 5 个未使用方法。
- Preserved Anthropic unknown request fields and unknown multimodal content blocks
  during same-protocol IR round trips; added OpenAI `parallel_tool_calls` coverage.
- Restored same-protocol request extensions when an IR request has no recorded
  source protocol, preserving Ollama and GLM vendor fields during round-trip.
- Removed inactive FpSlot degradation code and an unobserved saturation metric
  so the streaming executor passes static analysis without dead paths.
- Restored the vendored go-redis maintenance-notification log package so
  default `-mod=vendor` builds resolve all Redis internal imports.
- Completed SessionForensics replay evidence preservation, six-scenario audit
  contracts, mutation safety checks, and output-compliance nil-result handling.
- Corrected the domain dependency lint scope and prevented output redaction
  from modifying non-assistant response choices.

## [v1.14.0] - 2026-07-12 (License + Upgrade)

### 🚀 新增功能 (Features)

#### License 管理系统
- **在线激活** (`licensing/admin_api.go`)
  - `POST /api/v1/license/trial` - 7 天试用申请
  - `POST /api/v1/license/activate` - License Key 在线激活
  - `POST /api/v1/license/refresh` - 24h Token 续期
- **离线激活** (`licensing/offline.go`)
  - 请求生成 + 响应导入
  - 支持完全断网环境
- **设备管理** (`licensing/device_manager.go`)
  - 基于硬件指纹的设备绑定
  - max_devices 限制
  - 设备激活/停用

#### 实例注册与心跳
- **实例注册** (`center/server.go`)
  - `POST /api/v1/instances/register` - 实例首次注册
  - Ed25519 密钥对签发
  - instance_token (JWT, 24h TTL)
- **心跳协议** (`center/monitor.go`)
  - `POST /api/v1/instances/heartbeat` - 60s 心跳上报
  - 状态判定: online (≤30s) / degraded (30s-2min) / offline (>2min)
  - 自动监控与告警
- **Token 续期** (`center/admin_api.go`)
  - `POST /api/v1/instances/refresh` - Token 刷新

#### 自动升级与回滚
- **升级检查** (`autoupdate/admin_api.go`)
  - `GET /api/v1/updates/latest` - 检查最新版本（6h 周期）
  - `GET /api/v1/updates/manifest` - 下载升级清单
- **升级执行** (`autoupdate/installer.go`)
  - 下载 → SHA256 校验 → 启动测试 → 备份 → 原子替换
  - 健康检查（5s 内失败自动回退）
  - 状态上报到主控端
- **回滚支持** (`autoupdate/rollback.go`)
  - `POST /api/v1/updates/report` - 升级结果上报
  - 自动回滚 + 手动回滚
  - 备份保留策略（最近 5 个）

#### 安装器增强
- **新增子命令** (`installer/cmd/llm-gw-installer/`)
  - `activate` - 激活管理（trial/online/offline）
  - `heartbeat` - 心跳测试
  - `upgrade check` - 检查更新
  - `upgrade apply` - 应用升级
  - `upgrade rollback` - 回滚版本
  - `status` - 查看实例状态

### 🗄️ 数据库变更 (Database)

#### 新增表
- `licenses` - License 记录
- `license_devices` - 设备绑定
- `offline_activation_requests` - 离线激活请求
- `gateway_instances` - 实例注册
- `instance_heartbeats` - 心跳记录
- `releases` - 版本发布
- `release_status` - 升级状态

#### 字段新增
- `gateway_instances` 增强 (Migration 376):
  - `instance_token` TEXT - JWT Token
  - `public_key` TEXT - Ed25519 公钥
  - `current_version` TEXT - 当前版本
  - `build_seq` INT - 构建序号
  - `license_key_hash` TEXT - License 哈希
  - `hardware_hash` TEXT - 硬件指纹
  - `last_heartbeat_at` TIMESTAMPTZ - 最后心跳时间
  - `last_status` TEXT - 实例状态

### 🔒 安全 (Security)

- **Ed25519 签名**: 所有 `/api/v1/instances/*` 请求签名验证
- **RSA-PKCS1v15**: License 签名与验证
- **JWT 认证**: instance_token (24h TTL)
- **重放防护**: X-Timestamp ±300s + nonce 缓存 5 分钟
- **CRL 支持**: 撤销列表检查（7 天缓存）

### 📚 文档 (Documentation)

- 新增 `README.md` - 快速开始指南
- 新增 `docs/API.md` - 8 个主控端 API 端点
- 新增 `docs/DEPLOYMENT.md` - M1-M4 四种部署模式
- 新增 `docs/UPGRADE.md` - 在线/离线升级流程
- 更新 `CHANGELOG.md` - 版本历史

### 🛠️ 部署模式 (Deployment)

- **M1 单机部署**: 二进制 + systemd（离线模式）
- **M2 单机 Docker**: docker-compose 快速部署
- **M3 K8s Sidecar**: kustomize + sidecar 心跳聚合
- **M4 K8s Operator**: CRD + 声明式管理（规划中）

### ⚙️ 环境变量 (Environment)

新增 20+ 环境变量：
- `LICENSE_FILE` - License 文件路径
- `INSTANCE_ID` - 实例 ID（自动生成）
- `MAIN_CONTROL_URL` - 主控端地址
- `HEARTBEAT_INTERVAL_SECS` - 心跳间隔（默认 60s）
- `UPGRADE_CHECK_INTERVAL_SECS` - 升级检查间隔（默认 6h）
- `MAX_TENANTS` - 最大租户数（License 控制）
- `MAX_DEVICES` - 最大设备数（License 控制）
- `ENABLE_AUTO_UPDATE` - 启用自动更新
- `BACKUP_DIR` - 备份目录

### 🐛 Bug 修复 (Bug Fixes)

- 修复 `center/store_pgx.go` 字段缺失问题
- 修复 `autoupdate/types.go` 状态枚举不完整
- 修复 License 过期后无法启动问题（降级模式）

## [2026-07-05] - Script 合并 (deploy-scripts-merge)

### 🛠 重构

#### 部署脚本统一合并

- **184 部署 5→1**: `deploy-184.sh` 合并 `deploy-to-184-with-migration.sh`, `deploy-to-184-after-local-test.sh`, `deploy-columnar-184.sh`, `quick-deploy-to-184.sh`，支持 `--with-migration/-m`、`--after-local-test/-l`、`--columnar/-c`、`--quick/-q` 模式。
- **本地环境 6→2**: `local-up.sh` + `local-down.sh` 合并了 `local-r112-up.sh`、`local-r112-down.sh` 功能。
- **列存轮转 2→1**: `pg-columnar-rotate.sh` 新增 `--local` 模式，合并 `pg-columnar-rotate-local.sh`。
- **请求日志管理 6→1**: `manage-request-logs.sh` 合并 `cleanup-request-logs.sh`、`archive-request-logs.sh`、`delete-old-request-logs.sh`、`analyze-request-logs-size.sh`、`check-archive-table-sizes.sh`，支持 `--analyze`、`--archive`、`--delete`、`--check-sizes`、`--cleanup`、`--dry-run` 模式。
- **运行时测试 3→1**: `test-runtime-tc.sh` 合并 `test_tc6_quota_silent_failover.sh`、`test_tc7_no_infinite_loop.sh`、`test_tc8_client_disconnect.sh`，支持 `--tc6`、`--tc7`、`--tc8`、`--all`。
- **热表迁移 2→1**: `apply-hot-table-migrations.sh` 新增 `--env` 参数，合并 `apply_hot_table_migrations.sh` + `apply_hot_table_migrations_v2.sh`。
- **安装钩子**: `install-githooks.sh` 新增 `--pre-commit`，合并 `pre-commit-install.sh`。
- **预部署检查**: `pre-deploy-check.sh` 新增 `--partition-archive`，合并 `deploy-verify-partition-archive.sh`。
- 所有旧脚本保留为薄包装器（deprecation wrapper），向后兼容。

## [2026-07-03] - build_seq 6 (r1.13-done-2bde6aad)

### 🐛 Bug Fixes

#### 数据生命周期 — 存储总览修复
- **修复**：移除 `pg_total_database_size(name)` 调用（Citus 不支持该函数）。
  - 现象：184/71 生产 + 本地 r112 均使用 `citusdata/citus:11.3` 镜像，访问 `/api/admin/data-lifecycle/storage` 时报 `ERROR: function pg_total_database_size(name) does not exist (SQLSTATE 42883)`，整条查询失败导致 Database 字段为空。
  - 修复：改用 `COALESCE(SUM(pg_total_relation_size(c.oid)), 0)` 在所有 user schema 上求和作为 `TotalBytes` 近似（包含表 + 索引 + TOAST）。
  - 兜底：SUM 查询失败时回退到 `pg_database_size` 值。
- **新增**：列存（citus_columnar）单独展示 — `table_count` / `total_columns` / `total_bytes`。
- 部署到 184：build_seq 5 → 6，验证 https://llmgo.kxpms.cn/api/admin/data-lifecycle/storage 返回正常 JSON，无 warnings。

## [Unreleased] - 2026-07-02

### 概述

48 小时内（2026-06-30 ~ 2026-07-02）共有 **139+ 个提交**，覆盖 22 个工作阶段。核心工作集中在：**数据生命周期管理、附件系统、国际化(i18n)、安全加固、自动控制、缓存优化、路由重构、版本管理、流式响应处理**。

### 📊 总体变更统计

| 指标 | 数值 |
|------|------|
| 提交总数 | 139+ |
| 文件变更 | 769 个 |
| 代码新增 | +74,307 行 |
| 代码删除 | -4,442 行 |
| 新增模块 | 6 个 (data_lifecycle, attachments, i18n, remotecontrol, sessionstate, credentialstate) |
| 数据库迁移 | 13 个 (317-326 + 130-131) |
| 新增测试用例 | 200+ |

---

### 🚀 新增功能 (Features)

#### 1. 数据生命周期管理 (Data Lifecycle Management)
- **数据分区与归档** (4 表分区 + 列式存储)
  - `admin/data_lifecycle_partition.go` - 分区管理 (447 行)
  - `admin/data_lifecycle_storage.go` - 存储配置 (447 行)
  - `admin/data_lifecycle_attachments.go` - 附件归档 (343 行)
  - `admin/data_lifecycle_attachments_filesystem.go` - 文件系统管理 (212 行)
  - `admin/data_lifecycle_blobs.go` - Blob 管理 (254 行)
  - 支持按月自动分区、过期数据归档到冷存储

- **日志管理** (`admin/log_management.go` - 613 行)
  - 日志查看、过滤、下载、归档
  - tar.gz 打包下载 + 路径注入防护

- **存储配置** (`admin/storage_config.go` - 500 行)
  - 可配置存储路径、白名单防护
  - 存储迁移支持 (`admin/storage_migration.go` - 427 行)

- **附件系统** (`domains/attachments/`)
  - `extractor.go` - 文件提取 (265 行)
  - `handler.go` - 附件处理 (165 行)
  - `storage.go` - 附件存储 (479 行)
  - **可配置存储目录 + 自动文件迁移** (commit `847f9af6`)
  - **热切换无需重启**

#### 2. 国际化 (i18n) 完整支持
- **后端** (`i18n/` - 6 个文件, 900+ 行)
  - `bundle.go` - 国际化包管理
  - `context.go` - Context 集成
  - `locale.go` - 语言环境
  - `messages.go` - 消息模板
  - `helpers.go` - 辅助函数
  - 8 种语言：zh-CN, en, ja, de, fr, es, zh-TW, ar
  - **24 个 Admin 错误消息 × 8 语言 = 192 条翻译** (commit `57a6e8bc`)

- **前端** (commit `9eb09e25`)
  - vue-i18n@9 集成
  - LanguageSelector 组件（导航栏）
  - Accept-Language header 自动附加
  - localStorage 持久化用户偏好

- **Admin API 错误响应** (commit `57a6e8bc`)
  - 新增 `writeJSONErrCtx()` 函数
  - 支持稳定 `code` 字段（机器可读）
  - 迁移 55 个调用点（61.1% 覆盖率）

#### 3. 流式响应处理增强
- **自适应响应格式转换器** (commit `7d898684` - 945 行)
  - `AdaptiveResponseConverter` 三级 fallback
  - `ResponseFormatPreference` 24h 会话记忆
  - Chat Completions ↔ Responses API 互通
  - 5 个测试用例

- **Responses Bridge** (commit `b1029915`)
  - `domains/streaming/responses_bridge.go` - 627 行
  - `responses_bridge_test.go` - 729 行
  - Phase E 设计文档

#### 4. 缓存优化
- **增量编码压缩** (`cache/delta/` - 1090 行)
  - `apply.go`, `delta.go`, `encode.go`, `store.go`
  - B3 计划：压缩缓存存储空间
  - 设计文档：`docs/llm-gateway-go/2026-07-01-cache-delta-plan.md`

- **KV 缓存** (`cache/kv/` - 870 行)
  - `key.go`, `memory.go`, `store.go`
  - 消息指纹 + hit/miss 跟踪
  - 修复 InMemoryStore 竞态 (commit `cd0d3a0f`)

#### 5. 模块管理系统 (commit `5a094e32`)
- 详见 `CHANGELOG-module-management-20260701.md`
- 15 个功能模块统一管控
- 飞书机器人集成

#### 6. 路由候选过滤增强
- **套餐类型兼容性过滤** (commit `0fad7042`)
  - credentials.plan_type 字段
  - v_routable_credential_models 视图增强
  - token_plan ↔ billing_mode 兼容性检查

#### 7. 请求日志上游诊断字段 (commit `0b0d80e8`)
- Migration 320
- `client_request_id` 跟踪
- 同步脚本增强

#### 8. 会话状态机 (`domains/sessionstate/`)
- `state_machine.go` - 462 行
- `types.go` - 173 行
- `state_machine_test.go` - 371 行

#### 9. 凭据状态管理 (`domains/credentialstate/`)
- `manager.go` - 341 行
- `lifecycle.go` - 314 行
- `popularity_tracker.go` - 141 行
- 集成到 `cmd/gateway/main.go`

#### 10. 远程控制 (`domains/remotecontrol/`)
- `executor.go` - 362 行
- `lark_commands.go` - 331 行
- 飞书命令集成

#### 11. 任务管理 (`domains/taskmanagement/`)
- `task_assigner.go` - 455 行
- `group_manager.go` - 400 行
- `stats_manager.go` - 374 行

#### 12. 自动控制完善 (commits `d96dfa4c`, `9efe9567`)
- `injectFollowUpRequest` 完整实现
- AuditHook + 国际化提示
- 修复审计发现的 5 个关键 bug

---

### 🐛 Bug 修复 (Bug Fixes)

| 提交 | 描述 |
|------|------|
| `159408bc` | 修复 copyFile errcheck - Close 错误不丢失 |
| `2f658c05` | 修复 DB 视图缺少 tenant_id 和 provider_id |
| `29f91b63` | 修复 v_routable_credential_models 缺少 provider_model_id |
| `3ea33431` | 路由严格候选过滤 + 恢复时间感知 |
| `67b50207` | BuildFailureEntry 初始化 stream_chunks_sent |
| `c3ce0757` | maxBodySize 32MB → 128MB（适配大上下文模型） |
| `cd0d3a0f` | 修复 InMemoryStore stats 计数器竞态 |
| `88772512` | 用户取消不应计入凭据错误统计 |
| `0c577d7f` | 过滤客户端取消错误，不计入健康统计 |
| `899f106a` | ensureAnalysisEventsRLS 在表缺失时优雅降级 |
| `78a5d648` | 修复 ObservePayload 缺少 'type' 字段 |
| `0ef6411c` | 停止 schema 不匹配时静默丢弃 request_logs |
| `a2214914` | /api/system/version 公开端点（前端版本显示） |
| `ece89452` | 修复 opencode-agent 代码编译错误 |
| `bc8feacd` | IR model hint 不能覆盖 DetectProtocol 的 body shape |
| `ffbc3e33` | 隔离 71 生产与 184 测试数据库 |
| `728d081e` | 24h 审计修复 - updateRequestLog SET 子句 + 错误脱敏 |
| `ec4f8c52` | 路由不再伪装数据库错误为 no_candidate |
| `be90f301` | probe-health 暴露 state_distribution 字段 |
| `20c902b6` | /api/agents/health 租户隔离 |
| `15e72ffe` | telemetry nil-safe + client_request_id 跟踪 |
| `0db63cca` | auth 中间件链 cookie bypass + logout 白名单 |
| `cbbfe978` | 移除脚本硬编码 SSH 密码 |
| `00a38292` | SuperAdminMiddleware cookie 支持 |
| `bd3d3e79` | 提取 completion_tokens 和 cache tokens |

---

### 🔒 安全加固 (Security)

#### Phase 1: 自动化扫描 (commit `40fad27c`)
- gosec 集成
- 完整扫描报告：`.security-audit/gosec-report.json` (5183 行)

#### Phase 2: 手动审计 (commit `47644c8b`)
- 20 个 G304 文件包含位置逐个审查
- **全部 20 个位置安全**

#### Phase 2: 数据生命周期加固 (commit `edc11e05`)
- **Critical**: admin/storage/config, admin/logs/config 权限提升到 superAdmin
- **Critical**: 路径白名单防护（storage_config.go）
- **Critical**: tar 归档路径注入防护
- **High**: 配置验证改进（setInt 显式错误）
- **High**: 资源泄漏修复
- **High**: 信息泄漏防护（错误消息脱敏）

#### 数据库环境分离
- 隔离 71 生产与 184 测试数据库
- 禁用生产数据同步（commit `ffbc3e33`）
- 完整规范文档：`docs/DATABASE-ENVIRONMENT-SEPARATION.md` (435 行)

#### 数据脱敏
- 所有敏感信息从 tracked files 清理
- 修复 SSH 路径 + sanitize 错误

---

### ♻️ 重构 (Refactoring)

| 提交 | 描述 |
|------|------|
| `43737bab` | Admin API 使用 v_routable_credential_models 视图（删除 84 行重复代码） |
| `ca14bd59` | 简化 streaming handler + 新增测试 |
| `3c863d9c` | **路由 B1 PoC**: list/create handlers 拆分为 control/ + query/ 包 |
| `ff880f77` | 集中 state-manager 接口 + 修复 context/DB bugs |

#### 候选废弃包 (commits `7a9e1941`, `46cc2ff5`)
- `flowcontrol`, `taskmanagement` → `_to-be-deprecated/`
- 完整 manifest: `_to-be-deprecated/MIGRATION-MANIFEST.md`

---

### 🧪 测试 (Tests)

- 新增测试文件: 50+
- 测试用例: 200+
- 覆盖率：lint 100% 收敛（Round 47）
- 新增关键测试：
  - `domains/streaming/responses_bridge_test.go` - 729 行
  - `domains/sessionstate/state_machine_test.go` - 371 行
  - `domains/credentialstate/manager_test.go` - 190 行
  - `cache/delta/*_test.go` - 1300+ 行
  - `internal/ir/response_test.go` - 424 行

---

### 🛠️ 工具与脚本 (Tools & Scripts)

- `scripts/bump-version.sh` - 版本管理 + 自动递增 build_seq
- `scripts/sync-db-from-184-pgbase.sh` - pg_basebackup 同步 (430 行)
- `scripts/sync-db-from-71.sh` - 71 → 184 同步 (384 行)
- `scripts/quick-deploy-to-184.sh` - 一键部署到 184
- `scripts/diagnose-routing.sh` - 路由诊断
- `scripts/columnar-monthly-cron.sh` - 列式存储月度 cron
- `scripts/fix-71-missing-tables.sh` - 修复缺失表
- `scripts/lib/load-env.sh` - 环境自动加载

#### 数据库迁移 (13 个)
- 317: partition_credential_model_index
- 318: fix_archive_functions + request_logs_archive_heap
- 319: add_missing_ensure_functions
- 320: request_logs_upstream_diagnostics
- 321: cleanup_stale_in_progress
- 322: analysis_events_rls
- 323: intent_aggregates_rls
- 324: credential_state_log
- 325: request_attachments
- 326: fix_routable_view_quota_check
- 130: task_management
- 131: credential_plan_type

---

### 📦 部署与构建 (Build & Deploy)

- 版本管理：r1.13 系列，build_seq 自动递增
- 最新版本：`r1.13-done-9eb09e25-20260702-764`
- 完整部署文档：`docs/BUILD_AND_DEPLOY_GUIDE.md` (370 行)
- `.env` SOPS 加密 + 自动加载（commit `8ffaaabf`）
- `deploy/k8s/cron/README.md` 更新

---

### 🔍 Lint & 质量

- **Round 39-47**：golangci-lint v2 集成
- **从 100+ issues 收敛至 0** (100% 收敛)
- depguard 规则建立
- 关键修复：
  - Round 46: 修复 50+ errcheck issues
  - Round 45: 修复 staticcheck S1008/SA6000
  - Round 47: 清零剩余 6 issues

---

### 🌐 前端变更 (Frontend)

#### 新增页面
- `ModulesView.vue` (1087 行) - 模块管理
- `DataLifecycleView.vue` (重写) - 数据生命周期
- `data-lifecycle/AttachmentManager.vue` (393 行)
- `data-lifecycle/BlobManager.vue` (231 行)
- `data-lifecycle/FilesystemMaintenance.vue` (600 行)
- `data-lifecycle/LogManagement.vue` (399 行)
- `data-lifecycle/StorageConfig.vue` (435 行)
- `data-lifecycle/StorageOverview.vue` (442 行)

#### 新增组件
- `LanguageSelector.vue` (145 行)
- 8 个语言文件 (zh-CN, en, ja, de, fr, es, zh-TW, ar)

#### API 客户端
- `api/logs.ts` - 日志 API
- `api/modules.ts` - 模块 API
- `api/tuning.ts` - 自动调优 API (463 行)

#### 修改页面
- `App.vue` - 集成 LanguageSelector + i18n
- `RequestLogsView.vue` - 重构 (252 行变更)
- `CredentialMonitorView.vue` - 错误消息国际化
- `api/_core.ts` - Accept-Language header

---

### 📚 文档 (Documentation)

新增/重要文档：
- `docs/BUILD_AND_DEPLOY_GUIDE.md` (370 行)
- `docs/DATABASE-ENVIRONMENT-SEPARATION.md` (435 行)
- `docs/DEPLOYMENT-SECURITY-CHECKLIST.md` (450 行)
- `docs/adaptive-response-format-converter.md` (304 行)
- `docs/2026-07-01-responses-ir-phase-e.md` (231 行)
- `docs/2026-07-01-48h-comprehensive-audit.md` (339 行)
- `docs/2026-07-01-24h-rollup.md` (122 行)
- `docs/FAQ_AND_TROUBLESHOOTING.md` (508 行)
- `docs/SESSION_AUDIT_FLOW_CONTROL_FINAL_REPORT.md` (491 行)
- `docs/llm-gateway-go/2026-07-01-cache-delta-plan.md` (203 行)
- `docs/llm-gateway-go/AUDIT-SUMMARY.md` (129 行)
- 40+ 部署报告 + 审计报告 + 诊断文档

---

### ⚠️ 破坏性变更 (Breaking Changes)

- 流式响应处理：Phase E IR Bridge 上线，部分旧路径废弃
- 路由候选过滤：plan_type 兼容性检查可能影响现有 routing
- 数据库视图：v_routable_credential_models 字段调整

---

### 🔜 已知问题 / 待办

- Admin API 错误迁移：剩余 35 个调用点（9 动态错误 + 26 低频静态错误）
- 路由 B1 PoC → 完整重构（control/ + query/ 分包）
- 完整 RLS 策略覆盖（仅 analysis_events + intent_aggregates 已完成）
- 凭据热度感知探测 → 完整上线

---

### 📈 性能与优化

- 自适应响应格式转换：减少格式转换失败率
- 增量编码压缩：缓存存储空间优化（B3 计划）
- 流式响应处理：session-aware 优化
- maxBodySize 提升：32MB → 128MB

---

## [Previous Versions]

详见：
- `CHANGELOG-module-management-20260701.md` - 模块管理系统
- `docs/2026-06-23-to-2026-06-30-weekly-changelog.md` - 周报
- `docs/2026-07-01-24h-rollup.md` - 24h 滚动报告
- `docs/2026-07-01-48h-comprehensive-audit.md` - 48h 综合审计
## [2026-07-09] 飞书机器人模块 (feishubot)

### 新增
- **飞书机器人模块** 完整实现，支持：
  - 告警推送（注入攻击、高延迟、错误率飙升）
  - 审批通知与飞书内操作
  - 系统状态查询
  - 签名验证（HMAC-SHA256）
  - 用户白名单控制

### 模块依赖
- 依赖压缩管理、提示词注入检测、会话缓存、会话审计与审批（软提示）

### 新增设置
- feishu_bot.webhook_url
- feishu_bot.verify_token
- feishu_bot.encrypt_key
- feishu_bot.connection_mode
- feishu_bot.notify_on_alert
- feishu_bot.notify_on_approval
- feishu_bot.allowed_users
- feishu_bot.alert.severity_min
- feishu_bot.alert.rate_limit_per_minute
- feishu_bot.alert.dedup_window_seconds
- feishu_bot.alert.quiet_hours_enabled/start/end
- feishu_bot.alert.card_template
- feishu_bot.approval.expiry_reminder_minutes
- feishu_bot.approval.auto_mention_on_critical
- feishu_bot.commands.enabled
- feishu_bot.commands.admin_only
- feishu_bot.signature_required
- feishu_bot.timestamp_window_seconds

### 数据库迁移
- feishu_bot_routing_rules 表（路由规则管理）
- feishu_bot_send_log 表（发送审计）

### API 端点
- GET/POST /api/admin/feishubot/routing-rules
- PUT/DELETE /api/admin/feishubot/routing-rules/{id}
- POST /api/admin/feishubot/routing-rules:import (CSV批量导入)
- GET /api/admin/modules/feishu_bot/config
- POST /api/admin/modules/feishu_bot/test

### 前端
- 模块管理页面新增路由规则标签页
- 支持CSV批量导入路由规则
- 模块依赖软提示UI
- 飞书机器人设置页面

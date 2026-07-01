# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
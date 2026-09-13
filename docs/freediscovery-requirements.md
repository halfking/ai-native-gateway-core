# 免费资源自动获取 (FreeDiscovery) — 需求与系统架构说明

> 版本: v2.0 · 日期: 2026-09-14 · 前置文档: `docs/freediscovery-configuration.md` (v1.1)
> 参考项目: `~/workspace/ai/orbi` (templates/pi-providers 静态供应商模板能力)

本文是 FreeDiscovery 功能的需求定义、系统架构、与实现的对应关系 (差距分析),
以及本轮 (2026-09-14) 修正的落地记录. 既有契约细节 (SSRF/状态机/错误码) 以
`docs/freediscovery-configuration.md` §4 为准, 本文不重复.

## 1. 需求定义

### 1.1 目标

为网关增加**免费资源自动获取**能力: 管理员配置供应商模板后, 系统能自动扫描
上游 `/models` 端点, 识别免费模型, 经人工审查批量导入 `free_resource_catalog`,
接入既有 OmniFree 配额追踪与路由体系 — 减少免费资源池的手工维护成本.

### 1.2 功能需求 (已实现)

| # | 需求 | 实现状态 |
|---|------|---------|
| F1 | 供应商模板 CRUD (多租户, RLS 隔离) | ✅ `TemplateManager` |
| F2 | 内置供应商预设一键建模板 (5 家) | ✅ `presets.go` |
| F3 | Orbi pi-providers JSON 模板文件直传导入 | ✅ `import-orbi` 端点 |
| F4 | 上游模型列表扫描 (三种协议) | ✅ HTTPScanner / Google / Anthropic |
| F5 | 免费判定 + 共享池 + 配额估算 (预设钩子) | ✅ FreeOf/PoolKeyOf/QuotaEstimator |
| F6 | ToS 合规初判 (保守策略, 绝不猜 ok) | ✅ `ToSChecker` |
| F7 | 发现任务状态机 (pending→running→success/failed, CAS) | ✅ `DiscoveryEngine` |
| F8 | 人工审查 + 批量导入 (skip/overwrite/merge 冲突策略) | ✅ `ImportService` |
| F9 | 定时自动扫描 (scheduled trigger, 全租户) | ✅ `bg.ScanScheduler` |
| F10 | Admin API + Web UI 管理界面 | ✅ 10 端点 + FreeDiscoveryView |
| F11 | SSRF 防御 (校验层 + safehttpclient 传输层双阻断) | ✅ `url_safety.go` |
| F12 | 密钥安全 (env 引用不入库 / AES-256-GCM 密文落库, fail closed) | ✅ `TemplateManager.ResolveAPIKey` |
| F13 | 可观测性 (Prometheus 指标 + 调度器 liveness 端点) | ✅ `metrics/freediscovery_metrics.go` |

### 1.3 非目标 (明确不做)

- 不做上游注册/领钥自动化 (orbi 同样不做, key 由管理员人工获取);
- 不做 429 自动换源路由 (配额消耗仍由 OmniFree 既有体系追踪);
- ToS 初判只是初筛, 不替代法务审查 (`ambiguous` 一律人工兜底).

## 2. 技术选型及理由

| 选型 | 理由 |
|------|------|
| Go 领域包 `domains/freediscovery` | 与既有 `domains/freeresource` (OmniFree) 同构, 复用 RLS 事务模式 (`setTenantTx`)、密钥环 (`secret.Keyring`) 与 `safehttpclient`; 零新增依赖 |
| 模板驱动 (DB 表) 而非 JSON 文件 | orbi 的静态 JSON 模板是单机手工维护; 网关是多租户服务, 模板入库才能挂 RLS/审计/UI; 同时保留 `import-orbi` 端点兼容 orbi 文件格式 |
| 每供应商预设钩子 (`FreeOf`/`PoolKeyOf`/`QuotaEstimator`) 而非通用规则引擎 | 各家免费判定差异大 (`:free` 后缀/全量免费/协议字段), 显式 Go 函数比 DSL 可审计、可单测; 新增供应商只需在 `builtinPresets` 追加一项 |
| 人工审查闸门 (discovery_results → import) | 免费判定与 ToS 初判都可能误报; 扫描结果一律 `pending`, 管理员确认后才入 `free_resource_catalog`, `avoid` 条目导入即 disabled |
| `database/sql` + sqlmock 单测 | 领域层无 DB 也可测 (61+ 用例); 迁移文件另有结构性测试 |
| bg worker 进程内 goroutine 而非外部 cron | 与 `bg` 包既有 worker (quota probe 等) 模式一致, 无新增部署单元; env 开关可关 |

## 3. 文件结构

```
domains/freediscovery/          # 领域包 (无 admin/web 依赖)
├── types.go                    # 类型与校验 (APIType/FreeType/请求响应体/ValidateCreate)
├── template_manager.go         # 模板 CRUD + ResolveAPIKey (env 引用/AES-GCM 密文) + setTenantTx
├── url_safety.go               # base_url/models_endpoint SSRF 校验 (joinBaseAndEndpoint)
├── provider_scanner.go         # HTTPScanner: OpenAI 兼容 /models 扫描 + 免费判定钩子
├── google_scanner.go           # Gemini 真协议扫描 (models[] 形态, key 走 query)
├── anthropic_scanner.go        # Anthropic 真协议扫描 (x-api-key, has_more 分页)
├── presets.go                  # 内置预设 (groq/openrouter/google-ai-studio/siliconflow/zhipu)
├── tos_checker.go              # ToS 关键词初判 (avoid > caution > allow > 模板判定 > ambiguous)
├── discovery_engine.go         # 任务编排: 模板→密钥→扫描→ToS→落库 + 状态机 CAS
├── import_service.go           # 批量导入 free_resource_catalog (单事务 + FOR UPDATE + CAS)
├── metrics: metrics/freediscovery_metrics.go   # Prometheus 指标
├── *_test.go                   # sqlmock + httptest 单元测试

admin/free_discovery.go         # /api/free-discovery/* 路由处理 + fdStatusFor 错误映射
bg/scan_scheduler.go            # 定时扫描 worker (6h 默认, in-flight 去重, liveness)
web/src/views/FreeDiscoveryView.vue   # 管理界面 (1241 行, 3 Tab)
web/src/api/free-discovery.ts   # 前端 API 封装 (230 行)
sql/migrations/084-freediscovery-schema.sql(+.down)  # 三表 + RLS + catalog 扩展列
docs/freediscovery-configuration.md   # 部署/契约文档 (v1.1)
docs/freediscovery-requirements.md    # 本文
```

## 4. 数据库模式 (migration 084)

| 表 | 关键列 | 约束/索引 |
|----|--------|----------|
| `provider_templates` | provider_code, base_url, api_type, api_key_env, api_key_encrypted, models_endpoint, tos_verdict, enabled | UNIQUE(provider_code, tenant_id); 部分索引 enabled |
| `discovery_tasks` | template_id (ON DELETE SET NULL), provider_code, status, trigger_type, models_found, models_imported, started_at, completed_at, error_message | 状态 CHECK; (status, created_at) 索引 |
| `discovery_results` | task_id, provider_code, model_id, free_type, monthly_tokens, daily_tokens, pool_key, tos_verdict, import_status, raw_metadata | UNIQUE(task_id, model_id); pending 部分索引 |
| `free_resource_catalog` (扩展) | + source_type/discovery_task_id/last_synced_at/upstream_metadata | UNIQUE(provider_code, model_id, tenant_id) (084 追加) |

三张新表均启用 RLS (`tenant_isolation_*` 策略, 复用 migration 075 的
`get_current_tenant()` 契约); 应用层经 `SET LOCAL app.current_tenant` 注入.

## 5. API 端点 (全部在 `admin/free_discovery.go`, admin 鉴权)

| 方法 + 路径 | 用途 |
|---|---|
| `GET/POST /api/free-discovery/templates` | 模板列表 (`?enabled=true`) / 创建 |
| `GET/PUT/PATCH/DELETE /api/free-discovery/templates/{id}` | 单模板 CRUD |
| `GET /api/free-discovery/templates/presets` | 内置预设 (5 家) |
| `POST /api/free-discovery/templates/import-orbi` | Orbi 模板文件直传 |
| `POST /api/free-discovery/scan` | 触发发现 `{template_id}` (disabled → 409) |
| `GET /api/free-discovery/tasks` · `/{id}` · `/{id}/results` | 任务列表/详情/结果 (`?status=pending\|all`) |
| `POST /api/free-discovery/import` | 批量导入 (skip/overwrite/merge) |
| `GET /api/free-discovery/scan-scheduler/status` | 调度器 liveness 快照 |

错误映射走 `fdStatusFor`: sentinel → 404/409/400, 其余兜底 500.
写操作要求 super_admin/admin_key; 前端契约见 `web/src/api/free-discovery.ts:12-20`.

## 6. 界面结构 (`web/src/views/FreeDiscoveryView.vue`, 路由 `/free-discovery`)

三个 Tab, 共用顶部 banner (错误/成功提示) 与刷新按钮:

1. **templates** — 模板管理:
   - 内置预设卡片 (一键建模板; 非 openai-completions 协议带警示条);
   - 模板列表 (行内启用开关 / 扫描按钮 — disabled 模板禁用并悬停提示 / 编辑 / 删除);
   - 手工新建表单 (内联, 可折叠) + Orbi 模板 JSON 导入框 (含导入结果回显).
2. **tasks** — 扫描与审查:
   - 扫描触发卡 (选模板 → POST scan, 失败任务体展示 error_message);
   - 任务列表 (状态/触发类型 i18n 映射, 点行选中);
   - 选中任务的结果审查表 (tos_verdict / 配额 / import_status 过滤 pending|all,
     勾选或全量导入, 导入后显示 ImportSummary).
3. **history** — 导入历史 (已完成任务与 imported 计数回溯).

i18n: `freeDiscovery.*` 键, 8 locale 同步 (zh-CN 为源).

## 7. 配置说明

- **迁移**: `psql "$DATABASE_URL" -f sql/migrations/084-freediscovery-schema.sql` (幂等).
- **供应商 key**: 网关进程 env 注入 (`GROQ_API_KEY`/`OPENROUTER_API_KEY`/
  `GOOGLE_API_KEY`/`SILICONFLOW_API_KEY`/`ZHIPU_API_KEY`), 模板以 `$VAR` 引用,
  key 本身不入库; 明文落库需 keyring, 无 keyring 时 fail closed.
- **调度器**: `LLM_GATEWAY_FD_SCAN_INTERVAL` (默认 6h, 下限 1m),
  `LLM_GATEWAY_FD_SCAN_SCHEDULER=off|false|0` 关闭.
- **no-DB 模式**: 相关路由 503 (与既有 no-DB 行为一致).

## 8. 测试计划与门禁

```bash
go test ./domains/freediscovery/ -short        # 领域包: sqlmock/httptest/迁移结构
go test ./admin/ -short -run "TestFreeDiscovery|TestFDStatusFor|TestScanScheduler"
go build ./... && go vet ./domains/freediscovery/ ./admin/
gofmt -l domains/freediscovery/                 # 必须为空
rg -l '[\p{Han}]' domains/freediscovery/*.go    # CJK 门禁: 仅 url_safety_test.go fixture
```

本轮 (2026-09-14) 实测: freediscovery `ok 2.6s`, admin `ok 66.6s`, build/vet 通过,
gofmt 空, CJK 零命中 (仅既有 fixture). 新增回归:
`TestDiscoveryEngine_PresetScannerWiring`、`TestGoogleScanner_RawMetadataAndFiltering`、
`TestGoogleScanner_ErrorStatus`; `started_at` 断言升级为 `nonNilArg`;
import 测试首个 mark-exec 期望收紧为含 `tenant_id=$4` 的全子句正则.

## 9. 差距分析与修正方案 (2026-09-14)

以本需求逐条对照既有实现, 发现并修复 4 个缺陷 (均已带回归测试):

| # | 缺陷 | 根因 | 修正 |
|---|------|------|------|
| G1 | `discovery_tasks.started_at` 恒为 NULL, 违反契约 §4.3 "running 写 started_at=now()" | `updateTask` running 分支 `startedAt = completedAt` 读到的是尚未赋值的 nil | running 分支显式 `startedAt = timeNow().UTC()`; 测试断言升级 `nonNilArg` |
| G2 | `google-ai-studio` 预设扫描必然失败 ("response is neither {data:[...]} nor [...]") | 预设循环给所有 preset 都装配了 OpenAI 形态 `HTTPScanner`, 而 `scannerFor` provider 优先, 遮蔽了 `fallbackScanners` 里的 Gemini 真协议扫描器 | 预设循环跳过非 `openai-completions` 协议的 preset, 走协议回退; 新增 `TestDiscoveryEngine_PresetScannerWiring` |
| G3 | Google 扫描结果 `RawMetadata` 恒为 nil, `upstream_metadata` 落库退化成 `{}` | `decodeGoogleEntries` (唯一会给 entry 填 raw 的助手) 从未被 `ScanModels` 调用 | `ScanModels` 改走 `decodeGoogleEntries`; 新增扫描器级测试断言 RawMetadata 携带原始条目 |
| G4 | `casUpdateResult` WHERE 缺 `tenant_id`, 与 Import 安全契约注释 "results UPDATE 全部携带 tenant_id" 不符 | 实现遗漏 (RLS 是第一层, 此为应用层纵深防御缺失) | WHERE 补 `tenant_id=$4`, 调用点传 `r.TenantID`; 测试正则收紧为全子句 |

### 9.1 遗留建议 (本轮不修, 记录待办)

1. **`bg/scan_scheduler.go` sweep 预算与模板数不成比例 (P2)**:
   `fdScanCycleTimeout=60s` 约束整个 sweep, 而注释自述"N 模板 × HTTP 30s 最坏情形"
   — N≥3 个慢上游时 sweep 会被中途截断, 剩余模板推迟到下一 tick (6h).
   建议: 枚举出 N 后按 `max(60s, N×35s)` (封顶 30min) 派生 sweep 截止期.
   该文件 2026-09-14 刚经 probe-recovery 审计改动, 且有并行会话活跃, 本轮不动.
2. **`admin` templates GET 直写 500 (LOW, 维持 R24 豁免)**: `TemplateManager.List`
   无 sentinel, `fdStatusFor` 与直写等价; `List` 引入 sentinel 时必须先改走 fdStatusFor.
3. **zhipu/siliconflow 预设无 `FreeOf` 钩子**: 走默认 `:free`/零价规则, 上游
   `/models` 无定价字段时发现数为 0 (保守不误报, 符合 ToS 姿态); 若需提高召回,
   应以各官方免费清单为据补钩子并注明出处, 不得猜测.
4. **二进制 provenance / 版本簿记**: 沿用 R24 §四 遗留 (seq-2102 起), 与本功能无关.

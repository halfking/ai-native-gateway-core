# FreeDiscovery Domain

免费资源自动发现模块 — 借鉴 [Orbi](https://github.com/orbi-build/orbi) 的
`templates/pi-providers/*.json` 供应商模板能力, 多租户化落地到网关:
通过供应商模板自动扫描上游 `/models` 端点, 发现免费模型, 经人工审查后
批量导入 `free_resource_catalog` (接既有 OmniFree 配额追踪体系).

## 数据模型 (sql/migrations/084-freediscovery-schema.sql)

| 表 | 职责 |
|----|------|
| `provider_templates` | 供应商模板 (base_url / api_key_env / ToS 元数据), UNIQUE(provider_code, tenant_id) |
| `discovery_tasks` | 发现任务生命周期 pending → running → success/failed |
| `discovery_results` | 扫描出的模型 (pending → imported/skipped/conflict) |

三表均启用 RLS (`tenant_isolation_*` 策略, 复用 `get_current_tenant()` 契约).
`free_resource_catalog` 扩展了 `source_type` / `discovery_task_id` /
`last_synced_at` / `upstream_metadata` 四列做来源追踪.

## 文件结构

```
domains/freediscovery/
├── types.go               # 类型与校验 (CreateTemplateRequest 等)
├── template_manager.go    # 模板 CRUD + ResolveAPIKey (env 引用/密文解密)
├── provider_scanner.go    # HTTPScanner: OpenAI 兼容 /models 扫描
├── presets.go             # 内置供应商预设 (groq/openrouter/google-ai-studio/siliconflow/zhipu)
├── tos_checker.go         # ToS 关键词初判 (保守: 无法判定 → ambiguous)
├── discovery_engine.go    # 任务编排: 模板→密钥→扫描→ToS→落库
├── import_service.go      # 批量导入 free_resource_catalog (skip/overwrite/merge)
├── migration_084_test.go  # 迁移文件结构性测试
└── *_test.go              # sqlmock + httptest 单元测试
```

## 核心流程

```
模板 (provider_templates)
  → DiscoveryEngine.Run
      → TemplateManager.ResolveAPIKey   # 密文解密 或 $VAR env 引用
      → scannerFor(tpl)                 # 预设装配的 HTTPScanner
      → GET {base_url}{models_endpoint} # Bearer 认证 (keyless 可省)
      → FreeOf 过滤 → PoolKey/配额估算  # 预设钩子
      → ToSChecker.Check                # ok/caution/ambiguous/avoid
      → discovery_results (pending)     # ON CONFLICT 保留已审查状态
  → 管理员在 UI 审查 (按 tos_verdict 过滤)
  → ImportService.Import               # skip/overwrite/merge 冲突策略
      → free_resource_catalog           # avoid 条目自动导入为 disabled
```

## 预设钩子 (presets.go)

新增提供商只需在 `builtinPresets` 追加:

```go
"example": {
    ProviderCode: "example",
    BaseURL:      "https://api.example.com/v1",
    APIKeyEnv:    "$EXAMPLE_API_KEY",
    TosVerdict:   "unknown",
    FreeOf:         func(m modelEntry) bool { ... },  // 哪些模型免费
    PoolKeyOf:      func(id string) string { ... },   // 共享配额池
    QuotaEstimator: func(id string) (int64, int64) { ... }, // 月/日配额估算
},
```

设计要点: Groq 免费层按账户+模型计, `/models` 全量返回 → `FreeOf` 返回全部;
OpenRouter 免费模型带 `:free` 后缀且共享 `openrouter-free-pool` (默认 50 RPD).

## 密钥安全契约

- 模板优先用 `api_key_env` (如 `$GROQ_API_KEY`), 值永不入库;
- 明文密钥经 `secret.EncryptAESGCM` (AES-256-GCM keyring) 加密落库,
  无 keyring 时创建携带明文的模板会 fail closed;
- 日志/UI 永不回显完整密钥 (Get/List 不返回密文字段).

## 与 Admin API 的关系

路由挂载在 `admin/free_discovery.go` (`/api/free-discovery/*`),
main.go 经 `SetFreeDiscovery(dbConn.Stdlib(), keyring)` 注入依赖.

## 测试

```bash
# 单元测试 (无 DB 依赖, sqlmock + httptest)
go test ./domains/freediscovery/ -short
go test ./admin/ -short -run "TestFreeDiscovery|TestFDStatusFor"

# 迁移文件结构性测试
go test ./domains/freediscovery/ -short -run TestMigration084
```

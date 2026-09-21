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

模板支持三种密钥配置方式:

```go
// 1. 环境变量引用（推荐，生产环境）
APIKeyEnv: "$GROQ_API_KEY"       // 或 "GROQ_API_KEY"（两种形式等价）

// 2. 数据库加密存储（多租户场景，需配置 Keyring）
APIKeyEncrypted: "encrypted:base64..." // 创建时传明文 api_key，自动加密

// 3. Keyless Provider（无需密钥的提供商）
APIKeyEnv: ""
APIKeyEncrypted: nil
```

安全机制:
- 环境变量引用优先 (`api_key_env`): 值永不入库，仅运行时解析
- 明文密钥经 `secret.EncryptAESGCM` (AES-256-GCM keyring) 加密落库；
  无 keyring 时创建携带明文的模板会 fail closed
- 日志/UI 永不回显完整密钥 (Get/List 不返回密文字段)
- 环境变量缺失时错误消息包含：变量名、模板 ID、provider code、修复建议

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

## 常见问题 (FAQ)

### Q1: 如何配置环境变量引用的 API Key？

模板支持三种密钥配置方式（推荐环境变量引用）:

**生产环境推荐方式（环境变量）:**
```bash
# .env 文件或 systemd EnvironmentFile
GROQ_API_KEY=gsk_xxxxxxxxxxxxx
SILICONFLOW_API_KEY=sk-xxxxxxxxxxxxx
ZHIPU_API_KEY=xxxxxxxxxxxxx
```

模板创建时引用环境变量：
```json
{
  "provider_code": "groq",
  "api_key_env": "$GROQ_API_KEY"
}
```

**注意事项:**
- 支持 `$VAR` 或 `VAR` 两种形式
- 密钥值**永不入库**，仅运行时从环境变量解析
- 变量缺失时扫描会失败，错误消息包含变量名和修复建议
- 网关重启后自动从新环境读取，无需更新数据库

**其他方式:**
- 数据库加密存储: 创建时传 `"api_key": "明文密钥"`，需配置 Keyring
- Keyless Provider: `api_key_env` 和 `api_key` 都留空

---

### Q2: ToS 检查规则如何工作？

ToS (Terms of Service) 合规性采用**保守分级策略**，分为 4 个等级：

| 等级 | 含义 | 导入行为 | 触发条件 |
|------|------|---------|---------|
| `ok` | 明确允许 | 正常启用 | 官方 `:free` 标记 + 预设 ok |
| `caution` | 需注意限制 | 正常启用，建议审查 | 包含 vision/preview/beta 等关键词 |
| `ambiguous` | 无法判定 | 人工审查后决定 | 无任何命中特征 |
| `avoid` | 不建议使用 | 导入为 disabled 状态 | 包含 discontinued/deprecated 关键词 |

**关键词优先级（高到低）:**
1. avoid 关键词（discontinued/deprecated）→ 恒为 avoid
2. caution 关键词（vision/preview/experimental/beta）→ 最低降级为 caution
3. 模板级 tos_verdict（人工审查结论）
4. 提供商预设 tos_verdict
5. `:free` 官方标记
6. 无命中 → ambiguous（绝不猜测 ok）

**最佳实践:**
- 导入前按 `tos_verdict` 字段过滤审查
- `avoid` 状态的条目会自动导入为 disabled（可手动启用）
- 定期访问 Provider 官网确认 ToS 变更

---

### Q3: 模板自动禁用（auto_disabled_at）如何恢复？

**自动禁用触发条件（Migration 087）:**
- 连续扫描失败 ≥ 3 次
- `enabled` 自动设为 false
- `auto_disabled_at` 记录禁用时间戳

**状态字段说明:**
```go
consecutive_scan_failures int        // 连续失败计数（成功时重置为 0）
last_scan_failure_at      *time.Time // 最近一次失败时间
auto_disabled_at          *time.Time // 自动禁用时间戳
```

**恢复流程:**

1. **诊断失败原因**
```bash
# 查看任务错误消息
curl http://localhost:8782/api/free-discovery/tasks?limit=5 \
  -H "Authorization: Bearer $ADMIN_TOKEN"
# 检查 error_message 字段
```

常见原因：
- 环境变量缺失：补充对应 `$VAR` 到 `.env`
- API Key 过期：更新密钥
- 上游 API 变更：检查 base_url / models_endpoint 是否有效
- 网络问题：检查防火墙/代理配置

2. **手动重新启用模板**
```bash
curl -X PATCH http://localhost:8782/api/free-discovery/templates/{id} \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}'
```

手动启用时：
- `consecutive_scan_failures` 重置为 0（同时清空 `last_scan_failure_at` 与 `auto_disabled_at`，与 template_manager.Update 行为一致）
- 模板立即可参与扫描；若再次连续失败 3 次会重新自动禁用

3. **验证恢复**
```bash
# 触发测试扫描
curl -X POST http://localhost:8782/api/free-discovery/scan \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"template_id": {id}}'

# 检查任务状态（期望 status=success）
```

**监控建议:**
```sql
-- 查询所有自动禁用的模板
SELECT id, provider_code, consecutive_scan_failures, 
       last_scan_failure_at, auto_disabled_at
FROM provider_templates
WHERE enabled = FALSE AND auto_disabled_at IS NOT NULL;
```

**设计契约:**
- 瞬态故障（1-2 次）不会触发自动禁用
- 成功扫描重置计数器，防止误判
- 操作员可随时手动启用，系统不会自动重试

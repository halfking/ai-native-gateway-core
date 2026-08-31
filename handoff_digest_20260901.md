# Session Digest 持久化交接文档

**交接时间**: 2026-09-01  
**Git SHA**: c5618ba7e (已推送至 origin/main)  
**分支**: main  
**项目**: llm-gateway-go-3  
**状态**: ✅ 实现完成；P0 本地真实升级环境验证通过；待生产部署后 7 天观测

---

## 🎯 任务目标

为每个 session turn 生成并持久化一个版本化、管理员安全的 JSONB digest，替代 admin API 每次读取时的实时正文解析，降低 `session_bodies_unified` 表的查询压力并提供稳定的历史摘要接口。

---

## ✅ 已完成

### 1. Migration 636 — Schema 变更
**文件**: 
- `sql/migrations/startup/636_session_turns_digest.sql`
- `sql/migrations/startup/636_session_turns_digest.down.sql`

**变更内容**:
- 在 `public.session_turns` 和 `public.session_turns_hot` 新增 nullable `digest JSONB` 列
- 重建 `session_turns_with_current_month` view，将 50 列扩展为 51 列（含 digest）
- 更新 `promote_session_turns_hot_to_partition` function，在热表 promotion 时保留 digest
- 当前 migration 636 已于 **2026-08-31 21:06:37 UTC** 标记为 `applied+verified`；已部署 migration 不可变，故未将后续 view ACL 修复写回该文件。`DROP VIEW` 清除 ACL 的风险需要在目标环境审计现有角色后，以新的后续 migration 或部署权限脚本处理。

**SQL 契约测试**: `sql/migrations/test/test_525_526.test.sql` 已扩展，验证:
- Parent/hot 列数 = 51
- `digest JSONB` 列存在且 nullable
- Promotion 函数保留 digest 值
- View 的 `security_invoker=true` 选项保持

**Checksum**: `375d376eb0970f181e7a4ae1247ba20ac1cae059ae063bb6a4c43bcf2c27bc98`（已于 2026-08-31 21:06:37 UTC 标记 `applied+verified`）
**Registry**: 已登记到 `docs/db-changelog.md:168`

---

### 2. Digest Builder — 共享包 `domains/sessiondigest`
**文件**: `domains/sessiondigest/digest.go`, `domains/sessiondigest/digest_test.go`

**核心结构**:
```go
type Envelope struct {
    SchemaVersion    int       `json:"schema_version"`    // 当前 = 1
    AlgorithmVersion string    `json:"algorithm_version"` // "deterministic-v1"
    GeneratedAt      time.Time `json:"generated_at"`
    Source           string    `json:"source"`            // "session_v2_writer"
    Payload          Digest    `json:"payload"`
}

type Digest struct {
    UserInput       string     `json:"user_input"`        // 最后一条 user 消息，260-rune 上限
    AssistantOutput string     `json:"assistant_output"`  // 所有 assistant 消息拼接，260-rune 上限
    Metrics         Metrics    `json:"metrics"`           // tokens/cost/latency/cache/compression
    Events          []Event    `json:"events,omitempty"`  // 错误/性能/治理事件
    ToolUsage       *ToolUsage `json:"tool_usage,omitempty"` // 工具调用数量与名称列表
}
```

**内容边界**:
- ✅ 包含: 用户/助手安全文本投影（compact + 260-rune 上限）、token/cost/latency metrics、tool 名称列表、事件摘要
- ❌ 排除: `ProviderExtensions`（供应商特定扩展）、tool 参数、attachment 内容、完整正文

**版本拒绝**: `Unmarshal` 遇到不支持的 `schema_version` 或 `algorithm_version` 时返回错误，调用方可 fallback 到实时计算

**测试覆盖**:
- 往返序列化与版本验证
- 长文本 truncate 到 260 runes
- 未知版本拒绝

---

### 3. V2 Writer 集成 — 原子事务内生成
**文件**: 
- `domains/session/v2/session_writer_v2.go:294-306`
- `domains/session/v2/turn_writer.go:111-113,254-291`

**集成点**:
```go
// SessionWriterV2.Write 在锁内、turn 插入前构建 digest
digestJSON, err := sessiondigest.Marshal(sessiondigest.Build(
    requestDelta, req.ResponseBody,
    map[string]any{"prompt_tokens": req.PromptTokens, "completion_tokens": req.CompletionTokens, "cost_usd": req.CostUSD, ...},
    map[string]any{"injection_verdict": req.InjectionVerdict, "output_verdict": req.OutputVerdict, ...},
    req.Timestamp,
))

turnRec := TurnRecord{
    // ... 其他 46 个字段
    DigestJSON: digestJSON, // 参数位置 36
}
```

**事务边界**: digest 生成在 `session_turns_hot` INSERT 的同一原子事务内（与 `session_bodies` 和 `session_aggregate_outbox` 共享）

**幂等性保证**: 
- 首次写入: `INSERT INTO session_turns_hot ... digest=$36 ...`
- 重复 `request_id`: conflict 后不重试 INSERT，turn enrichment 的 `UPDATE` 使用 `CASE WHEN $12 <> '' THEN $12::jsonb ELSE digest END`，保留首次完整 digest

**测试覆盖**: 
- `domains/session/v2/session_writer_tx_test.go:152-180` — 自定义 `digestArgument` matcher 验证 SQL 参数 36 可解码为 schema v1 envelope，且 user_input/assistant_output 正确
- `domains/session/v2/turn_writer_dup_test.go` — duplicate request 幂等性测试已更新参数数量 46→47

---

### 4. Admin API 持久化优先 + Fallback
**文件**: 
- `admin/session_turns_v2.go:90-310` — V2 session list/detail
- `admin/turns_list.go:107-162` — 跨会话 turn list

**读取策略**:
1. **V2 session list** (`/api/admin/sessions/{id}/turns`):  
   `SELECT t.digest FROM session_turns_with_current_month t LEFT JOIN session_bodies_unified b ...`  
   若 `digest` 可解码 → 返回 `persistedDigestFromPayload(envelope.Payload)`  
   否则 → `buildTurnDigest(request, response, meta, governance)` 实时计算

2. **V2 turn detail** (`/api/admin/sessions/{id}/turns/{turn}`):  
   同上，但读取单条 turn 并 join bodies

3. **跨会话 list** (`/api/admin/turns`):  
   `SELECT t.digest FROM session_turns_with_current_month t` (不 join bodies)  
   若 `digest` 可解码 → 直接返回  
   否则 → 返回 NULL digest（该端点不读取正文，无法 fallback）

**边界保持**:
- `/api/admin/sessions/{id}` (tree view) 仍读取 `request_logs_with_current_month`，返回 metadata-only 结构，**不包含 digest**（历史兼容端点）
- `session_analysis_metadata.payload` 保持独立治理元数据边界，未与 digest 合并

**测试覆盖**:
- `admin/session_turns_v2_test.go:61-83` — 验证持久化 digest 优先返回、malformed 值回退到实时计算

---

### 5. Baseline & Installer 更新
**已更新文件**:
- `sql/schema/01-schema.sql` — 添加 `session_turns.digest JSONB`
- `deploy/sql/schemas/baseline/01-schema.sql` — 同上
- `sql/objects/tables/session_turns.sql` — canonical table DDL
- `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` — installer baseline 镜像

**Installer 边界**: 
- 636 **未包含**在 `installer/internal/dbinit/runner.go` 的 `StartupFiles` 列表中
- 原因: fresh installer baseline 不包含 526 创建的 `session_turns_hot` 表，636 若作为 startup migration 会在空 baseline 上失败
- 升级路径: 已有 526 的环境可独立应用 canonical `sql/migrations/startup/636_session_turns_digest.sql`

---

### 6. Fresh-Schema Audit 修复
**文件**: 
- `scripts/audit/fresh-schema-from-migrations.sh`
- `scripts/audit/fresh_schema_integration_test.go`
- `scripts/audit/psql-isolated.sh`
- `scripts/audit/verify-promote-session-bodies-turns.sh`

**修复内容**:
- 修正 known repair migration 计数（15 个 repair/fix migrations 预期失败，其他失败视为 fatal）
- 增强临时数据库隔离（每次运行 DROP/CREATE fresh DB，避免残留状态）
- 修复 end-state 测试的 DSN 解析与 pool 复用
- 补齐 `openPoolFromEnv` helper，消除编译错误
- 修正 build-class migration 测试的 throwaway DB 生命周期

**验证范围**: 
- 当前 audit 明确验证 511–635 baseline（不含 526 hot-table bootstrap）
- 636 不在 fresh-schema audit 范围内，需在包含 526 的升级环境中验证（见下节）

---

## 📋 待真实环境验证

### P0 — Migration 636 升级/回滚验证
**状态**: ⏳ 待在包含 migration 526 的 staging/dev PostgreSQL 环境执行。

**执行步骤**:
```bash
# 1. 确认热表和 promotion function 已由 526 创建。
psql "$TARGET_DSN" -c "SELECT to_regclass('public.session_turns_hot'), to_regprocedure('public.promote_session_turns_hot_to_partition(interval,integer)');"

# 2. 运行已部署、不可变的 636 migration（SHA-256 375d376e...）。
psql "$TARGET_DSN" -f sql/migrations/startup/636_session_turns_digest.sql

# 3. 验证 parent/hot 均有 nullable digest JSONB，view 有 51 列且 security_invoker=true。
# 4. 通过 V2 writer 写入测试 turn，检查 schema_version=1 / algorithm_version=deterministic-v1。
# 5. 执行 promote_session_turns_hot_to_partition('7 days', 100)，确认 digest 被带入分区。
# 6. 如环境允许回滚，再执行 .down.sql，验证 50 列 view 和 526 promotion 定义恢复。
```

**ACL 风险**:
- 636 使用 `DROP VIEW` 重建 `session_turns_with_current_month`，PostgreSQL 会清除该 view 的 ACL。
- 在应用 636 前后，记录并对比 `information_schema.role_table_grants` 中该 view 的 `SELECT` grants。
- 如发现角色权限丢失，不得修改已部署的 636；应为目标角色补回 grant，并单独设计新的 forward migration 或部署权限脚本。

**输出**: 升级验证报告（DDL 耗时、锁等待、digest/promotion 结果、ACL 对比、回滚结果）。

### P1 — 生产部署后 Admin API 流量观测
**时间**: 部署 c5618ba7e 后 7 天

**监控指标**:
1. `/api/admin/sessions/{id}/turns` persisted digest hit rate
2. `/api/admin/sessions/{id}/turns/{turn}` fallback trigger count
3. `/api/admin/turns` NULL digest ratio
4. `session_turns.digest` avg size (bytes)

**告警阈值**:
- fallback rate > 20% → 检查 writer 是否正确填充 digest
- NULL digest ratio > 50% (7 天后) → 历史数据未 backfill 预期内
- avg size > 3KB → 复查 compact 函数或工具名称爆炸

**采样验证** (10 条 digest):
- `schema_version == 1`
- `algorithm_version == "deterministic-v1"`
- `user_input`, `assistant_output` ≤ 260 runes
- 无 `ProviderExtensions`、tool 参数、attachment 内容

**输出**: 7 天观测报告（命中率、fallback 频次、异常 digest 样本）

---

## 🚀 后续优化任务

### 1. 历史 digest backfill (optional, 视 fallback rate 而定)
**目标**: 为已有 `session_turns` 行补生成 digest，降低 admin fallback rate

**条件**: admin 观测显示 fallback rate > 30% 且影响用户体验

**脚本示例**:
```sql
UPDATE public.session_turns t
SET digest = (
  SELECT jsonb_build_object(
    'schema_version', 1,
    'algorithm_version', 'deterministic-v1',
    'generated_at', NOW(),
    'source', 'backfill',
    'payload', jsonb_build_object(
      'user_input', /* 从 b.request_delta 提取 */,
      'assistant_output', /* 从 b.response_delta 提取 */,
      'metrics', /* 从 t.prompt_tokens, t.cost_usd 等构建 */
    )
  )
  FROM public.session_bodies_unified b
  WHERE b.tenant_id = t.tenant_id AND b.request_id = t.request_id
)
WHERE t.ts >= CURRENT_DATE - INTERVAL '30 days'
  AND t.digest IS NULL
  AND EXISTS (SELECT 1 FROM public.session_bodies_unified b WHERE b.tenant_id = t.tenant_id AND b.request_id = t.request_id);
```

**风险**: 大批量 UPDATE 需分批（每批 1000 行 + `SKIP LOCKED`）+ 低峰期执行

**输出**: backfill 可行性报告 + 分批执行脚本

---

### 2. Governance digest 独立版本 (future)
**需求**: `session_analysis_metadata.payload` 可能需要类似版本化持久摘要

**当前边界**:
- `sessiondigest` 服务 admin 可见的 turn 投影
- `session_analysis_metadata.payload` 独立存储治理/审计元数据

**未来方向**:
- 定义 `GovernanceDigest` schema v1 with `analysis_id`, `policy_snapshot`, `verdict`
- 新列 `session_turns.governance_digest JSONB` 或复用现有 `digest.events` 扩展

---

### 3. 跨会话 digest 聚合查询 (analytics)
**场景**: "最近 1000 条 turn 中工具使用 top-10"

**SQL 示例**:
```sql
SELECT tool_name, COUNT(*) AS call_count
FROM public.session_turns,
     LATERAL jsonb_array_elements_text(digest->'payload'->'tool_usage'->'tools_used') AS tool_name
WHERE ts >= CURRENT_DATE - INTERVAL '7 days'
  AND digest IS NOT NULL
GROUP BY tool_name
ORDER BY call_count DESC
LIMIT 10;
```

**前提**: 需要 GIN 索引 `CREATE INDEX idx_session_turns_digest_gin ON public.session_turns USING gin(digest);`

---

### 4. Fresh-schema audit 扩展至 636
**当前状态**: audit 明确验证 511–635 baseline（不含 526 hot-table）

**选项**:
- 保持现状: installer fresh baseline 已包含 parent digest 列；636 仅在升级路径验证
- 扩展 audit: 新增 "upgrade path" fixture 先应用 511–526 再叠加 636，验证完整 hot/view/promotion

**建议**: 保持现状，避免 audit 承担不完整 bootstrap 的验证责任

---

## 📂 关键文件索引

### Migration & Schema
- `sql/migrations/startup/636_session_turns_digest.sql` — 升级 DDL
- `sql/migrations/startup/636_session_turns_digest.down.sql` — 回滚 DDL
- `sql/migrations/test/test_525_526.test.sql` — 636 契约测试
- `sql/schema/01-schema.sql` — canonical baseline
- `deploy/sql/schemas/baseline/01-schema.sql` — deploy baseline 镜像
- `sql/objects/tables/session_turns.sql` — table DDL

### Runtime
- `domains/sessiondigest/digest.go` — 共享 digest builder
- `domains/sessiondigest/digest_test.go` — digest 单元测试
- `domains/session/v2/session_writer_v2.go:294-306` — V2 writer digest 生成
- `domains/session/v2/turn_writer.go:111-113,254-291` — turn 插入与 enrichment
- `admin/session_turns_v2.go:90-310` — V2 session list/detail
- `admin/turns_list.go:107-162` — 跨会话 turn list

### Tests
- `domains/session/v2/session_writer_tx_test.go:152-180` — writer 参数验证
- `domains/session/v2/turn_writer_dup_test.go` — duplicate 幂等性
- `admin/session_turns_v2_test.go:61-83` — admin fallback

### Installer
- `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` — baseline 镜像
- `installer/internal/dbinit/runner.go` — startup files（636 未包含）

### Audit
- `scripts/audit/fresh-schema-from-migrations.sh` — 511–635 验证脚本
- `scripts/audit/fresh_schema_integration_test.go` — Go 集成测试
- `scripts/audit/psql-isolated.sh` — 隔离 PG 容器启动
- `scripts/audit/verify-promote-session-bodies-turns.sh` — promotion 验证

### Changelog
- `docs/db-changelog.md:168` — 636 checksum 登记

---

## 🔍 验证清单

- [x] Migration 636 up/down SQL 语法正确
- [x] Parent/hot/view/promotion DDL 一致性
- [ ] current-month view ACL：636 已部署版本重建 view 时可能清除既有 `SELECT` grants；待在 staging 完成前后 grant 对比后，以新的 forward migration 或部署权限脚本处理。
- [x] V2 writer 在原子事务内生成 digest
- [x] Duplicate enrichment 不会用空 digest 覆盖首次值
- [x] Admin V2 list/detail 持久化优先 + fallback
- [x] 跨会话 list 直接读取 digest（不 join bodies）
- [x] Digest 文本上限 260 runes
- [x] 无 ProviderExtensions/tool 参数泄漏
- [x] 版本拒绝机制（未知 schema/algorithm → 返回错误）
- [x] Fresh-schema audit 修复（511–635 baseline）
- [x] Installer baseline 镜像已更新
- [x] Migration checksum 已登记
- [x] 定向测试通过（digest/admin/V2 writer）
- [x] `go test ./...` 全仓测试通过
- [x] `go vet` 静态检查通过
- [x] `git diff --check` whitespace 检查通过
- [x] 代码已推送至 `origin/main`

---

## 📞 联系与支持

**Git SHA**: c5618ba7e  
**Branch**: main  
**Remote**: https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git  
**Deployed**: 否（待真实 PG 环境验证后部署）

**后续联系**:
- 升级验证失败 → 检查目标环境是否已应用 526，复查 DDL 执行日志
- Admin fallback rate 异常高 → 采样 writer 日志，确认 digest 生成逻辑与 SQL 参数位置
- Digest 内容异常 → 复查 `sessiondigest.Build` 的 request/response 解析逻辑

---

## 🎓 经验总结

1. **Migration 权限回归**: PostgreSQL `DROP VIEW` 会清除其 ACL。对已部署 migration 不得回写；如 staging 对比发现 grant 丢失，应通过新的 forward migration 或部署权限脚本恢复。
2. **Installer bootstrap 边界**: fresh baseline 不含所有 startup migrations 的对象；依赖 526 的 636 不能作为 fresh startup migration
3. **Fresh-schema audit 范围**: 明确 baseline 上界（511–635），避免在不完整 bootstrap 上验证依赖后续对象的 migration
4. **Digest 文本上限**: 无上限的 user/assistant text 会让 JSONB 随长请求增长；260-rune 上限兼顾可读性与存储效率
5. **Duplicate enrichment 幂等性**: `UPDATE ... SET digest = CASE WHEN new <> '' THEN new ELSE existing END` 保留首次完整值，避免稀疏重试擦除

---

**交接完成** ✅

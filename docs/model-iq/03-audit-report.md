# 模型智商系统审计报告

> Date: 2026-08-11
> 审计范围：已合并的 Model IQ 全链路（feat/model-iq + fix/model-iq-followup）
> 审计方法：comprehensive-code-audit skill（数据溯源 / 流程闭环 / 并发安全 / 数据兼容）

## 执行摘要

整体评级：**修复前 C，修复后 B+**。核心功能（标准智商采集、节点测试、品质融合、前端展示）设计合理，但首次实现存在一个会导致节点智商历史**全部丢失**的 P0 数据落库 bug，以及多个影响成本和数据一致性的 P1 问题。本次审计已全部修正并补回归测试。

## 发现与修复

### P0：节点智商测试结果无法落库（数据丢失）

**位置**：`domains/modelquality/dbstorage.go:SaveScore`
**根因**：`INSERT INTO model_iq_runs ... $12,$13` 声明了 17 个占位符但只传了 12 个参数，且占位符编号与列错位。pgx 在 SimpleProtocol 模式下会报参数不匹配错误，导致**每一次节点智商测试的历史记录都写入失败**。`SaveScore` 的错误被 `TestSingleNode`/`RunPerNodeCheck` 以 `_ =` 吞掉，所以现象是"测试成功但历史为空、列表永远显示—"。

**附带问题**：即使参数数量正确，原 SQL 还硬编码了 `'mmlu_lite',0,0`（题数/正确数）、`'scheduled'`（触发类型），导致历史记录丢失测试元数据。

**修复**：重写 INSERT，占位符与列一一对应，题数/正确数/benchmark_type/trigger_kind/tested_at 全部从 `QualityScore` 取真实值；`tested_at` 为零值时回退 `now()`；error 列固定 NULL（错误记录通过 status=failed 表达）。补 4 个回归测试锁定字段传播。

### P1：触发类型无法区分（scheduled/on_demand/anomaly 全标 scheduled）

**位置**：`monitor.go:testModel` / `executor.go` / `bg/model_quality_worker.go`
**根因**：`BenchmarkReport.TriggerKind`/`QualityScore.TriggerKind` 字段不存在，落库只能硬编码 `scheduled`。审计后端"可疑动作触发"发现后，异常触发的记录也被标成 scheduled，无法区分。

**修复**：为 `BenchmarkReport` + `QualityScore` 增加 `TriggerKind`/`BenchmarkType`/`TotalQuestions`/`CorrectCount` 字段；`CalculateScore` 完整传播；`normalizeTriggerKind` 把内部标签（`anomaly:score_drop`→`anomaly`）映射到 DB 枚举；节点测试入口拆为 `testSingleNode(ctx, cred, model, triggerKind)`——admin API 传 `on_demand`、异常触发传 `anomaly`、定时巡检传 `scheduled`。

### P1：异常触发无去重/冷却（token 成本失控风险）

**位置**：`bg/model_quality_worker.go:TriggerNodeIQTest`
**根因**：每次探针失败/告警都 `go func()` 启动一次 50 题测试，同一节点连续失败会并发起多个测试，消耗真实 token 且可能压垮上游。

**修复**：worker 增加 `triggerInFlight` map + `triggerLast` 时间戳 + `triggerCooldown`（默认 10 分钟）。同一节点在测试进行中或冷却期内重复触发直接丢弃。补 3 个 worker 测试。

### P1：节点身份键错位（endpoint ID 场景历史断裂）

**位置**：`domains/modelquality/invoker.go` / `executor.go` / `bg/quality_node_source.go`
**根因**：`CredentialNode.RawModel` 被复用为"请求体 model 名"（可能是 endpoint ID 如 `ep-2024`），但 DB 节点身份是 `provider_models.raw_model_name`。火山方舟等供应商的节点历史会按 endpoint ID 落库，与按 raw_model_name 查询的列表对不上；手动测试用 raw_model_name 也可能找不到节点。

**修复**：`CredentialNode` 新增 `RawModelName`（= provider_models.raw_model_name，身份键），`RawModel` 仅作请求名。`NodeExecutor.Execute` 用 `nodeModelName()` 优先返回 RawModelName 作为 report.ModelName；`FindNodeByModel` 同时匹配 raw/outbound 名。

### P2：查询一致性

**位置**：`admin/model_iq.go` / `dbstorage.go`
- `handleModelIQHistory` 直接 `Scan` nullable `tested_at` 到 `time.Time`，旧数据/失败记录会扫描失败 → 改 `COALESCE(tested_at, created_at)`。
- `node-latest`/`catalog` 的 `provider_models` join 缺 `provider_id` 限定，跨供应商同名模型会串数据 → 补 `pm.provider_id = c.provider_id` 且 `lower()` 匹配。
- `ListAllScores` 没回填 `Provider`/`CanonicalModel`，聚合报表缺供应商维度 → join `providers` + `models_canonical` 回填。
- `SaveScore` 对 `status=failed` 也 upsert `node_iq_latest`，一次失败会把节点最新值显示为 0 → 仅 success/partial 更新缓存。

## 已验证

- `go build ./...` ✅ / `go vet`（变更包）✅
- `domains/modelquality` + `bg` + `domains/providerprofile` + `admin` 单测全过 ✅
- 新增 7 个回归测试（trigger 传播 / normalize / nodeModelName / nil pool / dedup / 冷却 / source 必需）✅
- 前端 `vue-tsc` 0 错误 + `npm run build` ✅
- 本地 pg17 schema 完整（model_iq_runs 19 列 / node_iq_latest 在）✅
- 本轮**无 schema 变更**（纯代码层 SQL/逻辑修复）

## 未改动（审计确认安全）

- 迁移 350 schema 本身正确，无需变更。
- 供应商品质 ModelIQ 维度（scorer_signals/scorer）逻辑正确，权重归一无问题。
- 标准智商数据源（modeliqdata 内置表 + fetch-standard-iq）正确。
- 前端 IQ 历史 chart.js 折线图 + 抽屉表格渲染正确。
- `enable_per_node` settings spec 已在上一轮补登。

## 残留技术债（非阻断，建议后续）

- `DBStorage` 目前无集成测试（需 testcontainers/pg）；本次仅单测 nil 路径 + 字段传播。
- `model_iq_runs` 无分区/清理策略，长期运行会膨胀（参考 `provider_profile_daily` 365 天保留）。
- 异常触发冷却时间（10min）硬编码，可考虑提为 setting。

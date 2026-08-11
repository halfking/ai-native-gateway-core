# 模型智商（Model IQ）系统设计

> Date: 2026-08-11
> 关联：`docs/供应商画像/01-指标体系设计.md`（ModelIQ 维度）、`docs/自检功能/01-design.md`（自检基础设施）

## 1. 目标

为每个模型增加两类「智商」数据，并接入路由品质计算与前端展示：

| 类别 | 含义 | 数据来源 |
|---|---|---|
| **标准智商 (standard IQ)** | 评测站点给出的模型基准智商 | Artificial Analysis Intelligence Index v4.1.1（0–100） |
| **节点智商 (node IQ)** | 该网关中「供应商凭据 + 模型」节点实测智商 | `domains/modelquality` 50 题 MCQ 测试 |

「节点」= `credential_model_bindings` 的一行（凭据 + 模型），是网关最小路由单元。

## 2. 标准智商（standard IQ）

### 2.1 数据源
**Artificial Analysis Intelligence Index**：https://artificialanalysis.ai/evaluations/artificial-analysis-intelligence-index

- 复合基准（Agents 34% / Coding 24% / Scientific 24% / General 18%），文本英文评测
- 量表 0–100（准确率型，**非**人类 IQ 100 为均值）
- 覆盖 168+ 主流模型；Data API (`/api/v2/language/models/free`) 需 `x-api-key`

### 2.2 抓取方式（双轨）
1. **内置参考表**：`modeliqdata/data/standard_iq.json`（go:embed），~104 条主流 canonical 模型的快照值。
   包 `modeliqdata.LookupStandardIQ` 用 `modelname.CanonicalizeClientModel` + `NormalizeRouteKey` + 点→横线折叠做名称对齐（`claude-sonnet-4.5` ≡ `claude-sonnet-4-5`）。
2. **CLI 实时刷新**：`cmd/fetch-standard-iq`
   - 默认从内置表填 `models_canonical.standard_iq`
   - 设 `AA_API_KEY` + `-live` 时调 AA Data API 覆盖
   - 支持 `-dry-run` / `-overwrite`，输出匹配/未匹配报告

### 2.3 存储
`models_canonical` 三列（迁移 350）：
- `standard_iq numeric(5,2)` — 0–100
- `standard_iq_source text` — `artificialanalysis` / `artificialanalysis-v4.1.1` / `manual`
- `standard_iq_updated_at timestamptz`

未命中的模型留 NULL（不强写）。

## 3. 节点智商（node IQ）

### 3.1 测试方法
复用现有 `domains/modelquality` 的 50 题 MCQ（`mmlu_data.go`，CS/数学/物理/历史/逻辑各 10 题）：

```
综合智商 = 准确率 × 0.6 + 稳定性(成功率) × 0.3 + 延迟评分 × 0.1   (0–100)
延迟评分：< 1000ms = 100，1000–5000ms 线性递减，> 5000ms = 0
等级：A+ ≥95 / A ≥90 / B+ ≥85 / B ≥80 / C ≥70 / D ≥60 / F
```

### 3.2 测试范围（成本控制）
- **特色模型**（`routing_policy.featured_models`）
- **近期使用过的模型**（`credential_most_used_model`）
- 仅对 **active 凭据**测试
- on-demand trigger API 由前端节流（同一节点手动触发间隔由调用方控制）

### 3.3 触发时机
| 触发方式 | 说明 | trigger_kind |
|---|---|---|
| **定时自检** | `bg.ModelQualityWorker` 周期（默认 24h，可配 `model_quality.interval_hours`） | `scheduled` |
| **手动** | admin API `POST /api/admin/model-iq/trigger` 或前端「立即测试」 | `on_demand` |
| **可疑动作** | 见下 | `anomaly` |

### 3.4 「可疑动作」触发（已接线）
满足以下任一条件时，发起异步智商重测（`modelQualityWorker.TriggerNodeIQTest`，best-effort，不阻塞触发方）：

1. **连续失败升级**：`NodeProbeWorker.runOne` 在失败分支当 `attempt ≥ 2` 时回调
   `SetModelQualityTrigger(credID, model, consec)`（接线点 `bg/node_probe.go` 失败 DB 写之后，
   在 main.go 两个构造处注入闭包）。
2. **品质骤降**：`provider_profile` 告警引擎在 Phase 3 保存 `score_drop`/`trend_drop`/`dimension_low`
   告警后回调 `AlertEngine.SetAlertHandler(...)`（`domains/providerprofile/alert_engine.go`）。
   handler 枚举该凭据的所有可路由模型，逐个触发重测（main.go 接线）。
3. **可用性告警**：`dimension_low`（可用性/稳定性 < 60）走同一条 alert handler 路径。

所有触发均为异步、best-effort：测试失败仅记日志，不影响探测/告警主流程。
`modelQualityWorker` 为 nil（`model_quality.enabled=false`）时回调为 no-op。

## 4. 存储 Schema（迁移 350）

```sql
-- models_canonical 加列（见 §2.3）

-- 每次节点智商测试的时点明细（append-only）
CREATE TABLE model_iq_runs (
  id bigint PRIMARY KEY,
  credential_id bigint, provider_id bigint, raw_model_name text, canonical_id bigint,
  benchmark_type text, total_questions int, correct_count int,
  accuracy numeric(5,2), stability numeric(5,2), latency_p95 int,
  overall_score numeric(5,2), grade text,
  probe_kind text,        -- gateway / direct / mock
  trigger_kind text,      -- scheduled / on_demand / anomaly
  status text,            -- success / partial / failed
  error text, tested_at timestamptz, created_at timestamptz
);

-- 节点最新值 + 历史聚合缓存（1:1 到可路由节点）
CREATE TABLE node_iq_latest (
  credential_id bigint, raw_model_name text,
  overall_score numeric(5,2), grade text,
  sample_count int, avg_score numeric(5,2),
  min_score numeric(5,2), max_score numeric(5,2),
  tested_at timestamptz, updated_at timestamptz,
  PRIMARY KEY (credential_id, raw_model_name)
);
```

`model_iq_runs` 是时点明细（供「点击查看不同时点智商」）；`node_iq_latest` 是缓存（供列表批量渲染 + 品质计算读取）。

## 5. 接入供应商品质（ModelIQ 维度）

详见 `docs/供应商画像/01-指标体系设计.md`。要点：
- `ExtendedWeights.ModelIQ = 0.12`，其余已实现维度按比例下调归一（总和 = 1.0）
- `MetricSnapshot.ModelIQSignal { AvgIQ, SampleN }`：`adapters.GetModelScale` 按 `credential_id` 聚合 `node_iq_latest` 得到
- **冷启动零影响**：无节点智商数据时该维度 `unmeasured`，`CalculateTotalScore` 自动跳过

## 6. HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/admin/model-iq/catalog` | 每模型标准智商 + 节点平均/最大/最小 |
| GET | `/api/admin/model-iq/node-latest?provider_id=` | 该供应商所有节点最新智商 |
| GET | `/api/admin/model-iq/history?credential_id=&raw_model_name=&limit=` | 节点智商历史时点 |
| POST | `/api/admin/model-iq/trigger` | 立即测试一个节点（产生真实 token 费用） |

均 `superAdmin` 守卫。

## 7. 前端展示

- **供应商模型列表**（`ModelsTab.vue`）：表头加「标准智商」「节点智商」两列；节点智商悬浮显示平均/样本数/测试时间
- **模型抽屉**：新增「模型智商」section，含标准/节点/平均值 + 历史时点表 + 「立即测试」按钮
- **全局模型目录**（`ModelsView.vue` canonical 表）：加「标准智商」列

## 8. 配置（`settings.model_quality.*`）

见 `settings/spec_model_quality.go`：`enabled` / `interval_hours` / `use_lite_benchmark` / `alert_threshold` / `data_dir` / `api_key` / `base_url` / `test_timeout_seconds`。

启用 per-node 直连测试：`model_quality.enable_per_node=true`（需 DB + 解密 key 可用）。

## 9. 运维

```bash
# 1. 抓取标准智商（内置表，无网络）
go run ./cmd/fetch-standard-iq

# 2. 实时刷新（需 AA API key）
AA_API_KEY=xxxx go run ./cmd/fetch-standard-iq -live

# 3. 应用 schema（db.Open 自动迁移；或 gateway migrate 子命令）
go run ./cmd/gateway migrate
```

建议每周 cron 跑一次 `fetch-standard-iq`。

## 10. 数据生命周期（model_iq_runs 清理）

`model_iq_runs` 是 append-only 的节点智商测试明细表，长期运行会膨胀。
`node_iq_latest` 是 1:1 缓存表（每个可路由节点一行），行数受活跃绑定数约束，无需清理。

### 清理策略（已实现）

- `DBStorage.CleanupOldRuns(ctx, retentionDays)`：`DELETE FROM model_iq_runs WHERE tested_at < now() - make_interval(days => $1)`
- `bg.ModelIQCleaner`：后台 worker，默认 **24 小时 tick + 365 天保留**，与 `ProfileCleaner` 同一模式（ticker + context cancellation + graceful Stop）
- 在 `cmd/gateway/main.go` 启动 / 关闭路径注册（仅 DB 可用时启动）

### 集成测试（已实现）

`domains/modelquality/dbstorage_integration_test.go` 覆盖：
- `SaveScore` → `model_iq_runs` INSERT + `node_iq_latest` UPSERT 往返
- `GetLatestScore` / `GetScoreHistory` 读取与排序
- failed run 不覆盖 `node_iq_latest` 缓存
- 多次写入后 avg/min/max/sample_count 聚合重算
- `CleanupOldRuns` 按保留天数删除旧行

运行方式（需有 migration-350 schema 的 PG 实例）：
```bash
TEST_DATABASE_URL=postgres://... go test ./domains/modelquality/ -run TestDBStorage -v
```

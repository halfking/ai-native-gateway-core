# 将 claude-fable-5-2 纳入主动探测系统 - 操作指南

**文档版本**: v2.0（2026-11-11 23:5x +08 影响面与事实修正版）  
**修订说明**: v1.0 有三处错误已在本版修正——①设置存储表写成 `system_settings`（实为 `settings_kv`）；②"需启用可信度系统"（生产 2026-10-30 已启用）；③影响面只算了探测成本（`routing_policy.featured_models` 实际参与路由加权）。修正依据均为代码级/生产库级核实，见 §九。

---

## 执行摘要

**真实机制诊断**（v2.0 修正）：可信度系统**早已启用**，fable-5-2 没有官方评分的真实原因是——
`model_credibility.scope` 无配置行 ⇒ 走默认 `featured` ⇒ 扫描集读 `routing_policy.featured_models` ⇒ 该数组为空 `{}` ⇒ **定时扫描集为空**。

**获取 fable-5-2 官方评分的三条路径**（按推荐顺序）：

| 路径 | 路由影响 | 成本 | 谁来执行 |
|------|----------|------|----------|
| **C. 手动单点触发（首选）** | **零** | 一次小请求 | 有 superAdmin 权限者，随时 |
| A. 写 `routing_policy.featured_models` | **改路由加权** ⚠️ | 周期成本 <5 USD/月 | **owner 裁决后** |
| B. `scope=all` | 无 | 12-120 USD/月 | owner 裁决 |

---

## 一、背景与现状

### 1.1 claude-fable-5-2 数据现状

- ✅ 被动指纹: 已采集（3267+ 样本；推断可信度 88/100，详见指纹分析报告）
- ✅ 性能数据: ITT P50=5ms, 吞吐 104 tok/s
- ⚠️ TTFT: 凭据31 上 12.6s——根因已订正为**双因素**（模型级 1.4-1.6× + 凭据31级 ~2× 主因），详见 TTFT 调查报告 §八
- ❌ 官方可信度评分: 无（从未被扫描）

### 1.2 与 claude-fable-5-1 对比

- fable-5-1: ✅ 已有扫描记录（provider 587），最近 verdict=verified 100 分
- fable-5-2: ❌ 无任何 `provider_model_credibility` 记录

---

## 二、系统机制（v2.0 按代码核实）

```
定时扫描（model_credibility.enabled=true, 6h 周期）
  └─ ScanAll: 节点集由 scope 决定
       ├─ scope=featured（默认）: SQL 读 routing_policy.featured_models 命中的绑定
       │   （bg/model_credibility_source.go:46-51，★ 只读这张表，不读 models_canonical.featured）
       └─ scope=all: 全部活跃绑定
探活成功触发（trigger_on_probe_success=true）
  └─ 受 cooldown / featured scope / inflight 约束
手动单点触发（admin API）
  └─ ★ ScanNode 不受 featured scope / trusted_skip 限制（bg/model_credibility_worker.go:276）
      "人工触发可查任意绑定"
```

### 2.1 生产当前配置（211 settings_kv 实测，2026-11-11 23:5x）

```sql
SELECT key, value FROM settings_kv WHERE key LIKE 'model_credibility%';
-- model_credibility.enabled | true   （2026-10-30 01:02 起，无需再启用）
-- （scope 无行 ⇒ 默认 featured）
-- routing_policy.featured_models（tenant default）= {} 空
```

⇒ **定时 ScanAll 每轮扫 0 个节点**；现存 fable-5-1 的扫描记录来自手动/触发路径。

---

## 三、方案 C：手动单点触发（首选，零路由影响）

**代码级依据**：`bg/model_credibility_worker.go:276`——ScanNode 不受 featured scope / trusted_skip 限制；
settings spec 同款文案"手动单点检测不受本项限制"。

```bash
# superAdmin 鉴权后（管理台/鉴权 mint 走运维侧既有流程）
POST /api/admin/model-credibility/trigger
{"credential_id": 31, "raw_model_name": "claude-fable-5-2"}
```

- 同步执行（3min 超时），成本 = 一次小请求（~35 token）
- 结果直接返回，并写入 `provider_model_credibility` / `provider_model_credibility_runs` / quality profiles rollup
- **对任意 (凭据,模型) 可用**：同法可补扫凭据 59 / 99036 上的 fable-5-2 绑定，顺带对比三凭据的 identity 判定
- 适合立即执行；缺点是**一次性**，不建立周期机制

### 验证

```sql
SELECT raw_model_name, credential_id, credibility_score, identity_score,
       capability_score, consistency_score, verdict, measured_at
FROM provider_model_credibility
WHERE raw_model_name = 'claude-fable-5-2'
ORDER BY updated_at DESC;
```

---

## 四、方案 A：写 routing_policy.featured_models（owner 裁决项，登记不代执行）

### 4.1 ★★ 影响面警告（v1.0 缺失，v2.0 补正）

`routing_policy.featured_models` **不只**喂可信度扫描面，同一数组同时是：

1. **autoroute 的 featured 加权**（FeaturedBonus）——写入即刻改变路由打分；
2. **featured 凭据优先**选取——改变候选凭据排序；
3. **探测份额折半/featured 优先**（node_probe featuredCycle、探针冷却 featured 50% 折半）——改变探测行为；
4. 与 `models_canonical.featured`（迁移 953，26 模型标记）构成**双数据源并存**——改一处不改另一处会造成两套"特色清单"漂移；
5. `/v1/models` 发现端点默认口径 `featured`——featured 口径筛出空清单时网关退回全量并 WARN。

⇒ **写入该数组 = 一次路由行为变更**，必须 owner 裁决 + 变更后 24h 观测（路由/探测面），不属于监控轮可代执行项。

### 4.2 附带澄清：只改 models_canonical.featured 没用

可信度扫描面 SQL 只读 `routing_policy.featured_models`（`bg/model_credibility_source.go:46-51`）。
仅把 `models_canonical.featured` 置 true **不会**让 fable-5-2 进入可信度扫描集（该列另有探测优先用途）。

### 4.3 若 owner 批准后的操作 SQL（表名已修正为 settings_kv）

```sql
-- 1) featured 清单（路由影响面见 §4.1，owner 批准是前置条件）
UPDATE routing_policy
SET featured_models = ARRAY['claude-fable-5-2','claude-fable-5-1','claude-opus-5','claude-sonnet-5'],
    updated_at = NOW()
WHERE tenant_id = 'default';

-- 2) scope 如需显式钉住（当前无行走默认 featured，可不动；仅当曾改过才需要回写）
INSERT INTO settings_kv(key, value, value_type, scope, category, updated_at)
VALUES ('model_credibility.scope', '"featured"'::jsonb, 'string', 'platform', 'model_credibility', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- ~~启用步骤删除~~：model_credibility.enabled 生产已为 true（2026-10-30），无需操作。
-- ★ v1.0 的 system_settings INSERT 是错表——设置系统只读 settings_kv（settings/store_db.go）。
```

### 4.4 成本口径（保留 v1.0，仅为探测成本）

- 单节点单次 ~70 token；featured 模式 4 模型 × 4 次/天 ≈ 0.12-1.2 USD/月
- **路由面成本不计入此处**：featured 加权/凭据优先带来的流量迁移无法用钱直接量化，须 owner 判断

---

## 五、方案 B：scope=all（owner 裁决项）

```sql
INSERT INTO settings_kv(key, value, value_type, scope, category, updated_at)
VALUES ('model_credibility.scope', '"all"'::jsonb, 'string', 'platform', 'model_credibility', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
```

- 扫全部活跃绑定，token 成本 12-120 USD/月（v1.0 口径保留）
- 无路由影响，但对未投流长尾绑定"花钱不出结论"（spec 原话），不推荐

---

## 六、TTFT 特性对评分的判读注意（交叉引用）

主动探测的一致性分（consistency）若因 TTFT 波动被压低，判读时须知：

- 凭据 31 的 TTFT 主因是**凭据级**（130dao-cache/并发100，~2×），非模型掺水信号；
- 建议手动触发时**同时扫凭据 59/99036 上的 fable-5-2** 做对照——若 identity 一致而仅时延分布不同，佐证"凭据面问题"；
- 评分解释权归可信度维度定义（identity=身份、capability=能力、consistency=稳定性），本文档不代改判据。

---

## 七、验证与监控

- 首次扫描后：查 `provider_model_credibility`（SQL 见 §三）；
- 每周回顾评分稳定性；告警建议：`credibility_score < 70`、`verdict = suspicious`、`measured_at > 24h`（周期路径被配置变更打断的信号）；
- 被动指纹与主动探测互补不互斥：被动=真实流量画像，主动=标准化标定。

---

## 八、推荐执行顺序（v2.0）

1. **立即**（零路由影响）：superAdmin 调 `POST /api/admin/model-credibility/trigger` 扫凭据 31 的 fable-5-2（可选：连带 59/99036 对照）；
2. **1 周内**：review 首扫结果与被动指纹推断（88/100）是否同向；
3. **owner 裁决后**：决定是否走方案 A 建立周期扫描（含路由影响面确认与 24h 观测）；
4. **持续**：TTFT 优化走凭据侧 A/B（切流量到 59/99036 观察），与可信度评估并行不混。

---

## 九、v2.0 修正依据（留痕）

| v1.0 表述 | 修正后 | 依据 |
|-----------|--------|------|
| 设置表 `system_settings` | **`settings_kv`** | settings/store_db.go:69,114（读写均此表） |
| "需启用可信度系统" | **已启用**（enabled=true @2026-10-30） | 211 settings_kv 实测 |
| 影响面=纯探测成本 | **含路由加权/凭据优先/探测份额折半** | settings/spec_models.go、main.go featured 注释、迁移953 双数据源 |
| 方案C"需确认是否有接口" | **接口存在且不受 scope 限制** | admin/handler.go:278、bg/model_credibility_worker.go:276 |
| 无评分原因="未启用" | **scope=featured + featured_models={} ⇒ 定时扫描集为空** | settings_kv 实测 + model_credibility_source.go:46-51 |

---

**文档状态**: ✅ v2.0 修正完成（全部结论带代码/生产库级依据）  
**执行状态**: 方案C 待 superAdmin 执行；方案A/B 待 owner 裁决  
**关联**: TTFT 订正=claude-fable-5-2-ttft-investigation.md §八；系统健康=peak-vs-offpeak-comparison §七

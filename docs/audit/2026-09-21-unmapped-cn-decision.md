# 2026-09-21 Unmapped `-cn` Rows Decision

**Status: 决策已定(rename 路径);脚本已写好待执行;操作前需运维审批。**
**关联:** [`2026-09-20-252-execution-report.md`](./2026-09-20-252-execution-report.md) §4.2
[`2026-09-20-standard-model-name-cleanup.md`](./2026-09-20-standard-model-name-cleanup.md) Phase 4
[`../handoff/2026-09-20-standard-model-name-cleanup.md`](../handoff/2026-09-20-standard-model-name-cleanup.md) 末尾「Known risks」

## 1. 背景

上一轮 Phase 4(2026-09-20)在 .34 本地 + 252 双库禁用 -cn 行时,**21 个 -cn 行因为 base canonical 已存在**而顺利 disable(fold 进 base + 加 surface='cn' deprecated alias)。**23 个 -cn 行因 base 不存在而留置**(留作 operator 决策),分布在以下家族:

```
kling (1-5/1-6/2-1/2-5-turbo/2-6/3)        6
wan2.6 (t2i/t2v)                           2
wan2.7 (image/image-pro/t2v/i2v/r2v/videoedit) 6
wan3.0 (video/video-prime)                 2
viduq3 (turbo/pro)                         2
qwen-image (2.0/2.0-pro)                   2
qwen3-vl (flash/plus)                      2
deepseek-v3.1                              1   ← 仅 252
```

总计 23 行:19 行 .34 + 252 共有;4 行(1 个 deepseek-v3.1 + 3 个 kling v1/v1.6/v2.5-turbo)**仅 252 存在**(本地没出现过,可能 252 由某个上游 provider 单独触发)。

## 2. 根因(2026-09-21 复核后定性)

### 2.1 这些 -cn 行不是「区域变体」

之前的审计假设 `-cn` 是某个 base 模型的「中国区域版」(类比 `-ga` 是 GA 版),所以建议 seed 缺失的 base 再 fold。本轮查证后定性为:

- **没有任何 base 版本存在**(本地 + 252 双库查表均空)。
- 所有 alias 都是 -cn 形式(纯横线/点/下划线变体),没有任何 alias 指向不带 -cn 的形式。
- 0 流量(req_7d=0, req_all=0)、0 work_type_model_route 引用、0 业务 alias 解析。
- `claude-fable-5`(另一条活体 -cn 链)有 1234 req/7d,所以 0 流量不是 -cn 后缀的固有问题。

**结论:`-cn` 在这些家族里只是 discovery/provider_refresh 上游报回的多余字符,本身不是「区域」语义。** 这 19 个 canonical 是被加了无关后缀的真身。

### 2.2 触发路径

- `discovery/discovery.go` 与 `provider/client.go` 的 auto_discovered / provider_refresh 路径未对 `-cn` 后缀做剥除。
- `modelname/junk_seed_guard.go` 的 `JunkSeedGuardSQL` 只做归一化相等 + 截断形匹配(`%-_` 后缀),对带 `-cn` 的 canonical 也放行,因为它当成了「更长母名」(strip `-cn` 后命中基名形态)。
- 也就是说:**这些行是「上游返回什么名字 → 直接 seed」** 的结果,没有人类对模型名做语义规范化。

## 3. 决策矩阵

| 维度 | 路径 A:disable + seed 新 base | 路径 B:重指 pm_refs 到 default | 路径 C(本轮选定):rename strip -cn |
|---|---|---|---|
| canonical_id 是否变 | 是(创建新 id) | 否(pm_refs 改指向) | 否(同 id,只改 canonical_name) |
| pm_refs 引用 | 重指到新 base | 重指到 default(可能丢语义) | 不动(canonical_id 不变) |
| 旧 -cn 名是否还解析 | 是(deprecated alias) | 否(直接废) | 是(deprecated alias 留底) |
| work_type_model_route | 需 UPDATEs | 需 UPDATEs | 0 行引用,无需改 |
| 副作用风险 | 中(新 id 触发下游缓存重算) | 高(丢语义、API 客户端断流) | 低(只改字段,无 id 变化) |
| 前向兼容 | 需补代码抑制新 -cn seed | 需补代码抑制 | 自动(JunkSeedGuardSQL 已会抑制) |
| 回滚 | drop+re-enable old | 重指回去 | `UPDATE canonical_name = ...` 一句 |

**选定 C(同 id rename)**,理由:
1. **id 不变 = pm_refs/历史 cache/外键引用全部不动**,变更面最小。
2. **0 流量 + 0 work_routes** 证明没有任何路由层依赖 -cn 后缀,改名不会断流。
3. **JunkSeedGuardSQL 已能在下次 auto_discovered 时抑制**再次 seed(归一化后 `kling-v2-1` 与 `kling-v2-1-cn` 命中 `_kling_v2_1` 互为母名形态)。
4. 旧 -cn 名以 `surface='cn'` deprecated alias 形式保留,**符合 Phase 4 已建立的语义标签规范**(2026-09-20 Phase 4 的 21 行 disable 走的就是这个模式)。

## 4. 实施方案(本次落档内容,不自动执行)

### 4.1 本地 .34 先行

理由:.34 是审计源头、本轮已验过 base 不存在、流量隔离、可立即观察。
脚本:`sql/fixes/2026-09-21-unmapped-cn-rename.sql`(本 commit 落档)。
执行步骤:
1. 备份段(对照 Phase 4 的 `bak_20260920_*` 备份,本轮新建 `bak_20260921_*`,幂等)。
2. 嵌套事务:rename canonical_name + 加 deprecated alias + 防御性守卫(同 canonical_name 不能已存在)。
3. COMMIT 后断言:`request_logs.canonical_id = ANY(ids)` 应为 0;`provider_models.canonical_id = ANY(ids)` 应保持原数。

### 4.2 252 同步

**前置**:
- 252 是 deferred 节点(`llm-gateway-deploy-test` 规则);本轮脚本落档后,执行前需运维窗口。
- 4 个 252-only 行(deepseek-v3.1-cn / kling-v1-5/1-6/2-5-turbo-cn)在本地不存在,需在 252 单独验证它们的 base 也确实不存在(本轮已验:全 0 pm_refs,可以**直接 disable** 而不需 rename)。

**执行步骤**:
- 同脚本套用到 252,加 4 行 direct-disable 子事务(`status='disabled', disabled_reason='base missing on 252 only; no pm_refs; renamed on .34'`)。
- 防御性守卫:`SELECT 1 FROM models_canonical WHERE canonical_name = '<renamed>' AND status='active'` 必须命中同 id 行才算 OK。

### 4.3 154 / 245

**不直接 rename**。154 / 245 在跑 commit 92f18cf22 之前的 binary,没有 match-first 守卫,会周期性重新 seed -cn 行。处理路径:
- 等 154 / 245 部署上 `92f18cf22` 之后的 binary 后,再跑 `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql`(92f18cf22 警告要求),把 -cn 与 base 都归一遍。
- **本轮不触碰 154 / 245**,避免 discovery 仍在回种时改 canonical_name 制造悬挂。

## 5. 部署清单

| step | env | 动作 | 风险 |
|---|---|---|---|
| 1 | local .34 | 跑 `sql/fixes/2026-09-21-unmapped-cn-rename.sql` | 低(id 不变,流量=0) |
| 2 | local .34 | `govern-junk-canonical -json` | 期望 Suspects: null |
| 3 | local .34 | `curl /v1/models` 过滤 19 个新名 | 应有非空返回 |
| 4 | 252 | 运维窗口批 → 跑同名脚本(254 路径) | 需 4 行 252-only 单独 disable |
| 5 | 154 / 245 | 等 binary 追上 92f18cf22 后跑 dedup-cleanup | 推迟到 binary 部署后 |

## 6. 已知遗留

- **discovery 仍在回种 -cn 的代码问题未根治**。本轮改名 + JunkSeedGuardSQL 抑制是兜底,真正的根修(剥除 -cn 后缀)是 R51 范围。
- **252 仍有 `kling-v1-5/1-6/2-5-turbo-cn` 等本地从未见过的 -cn 行**,说明 252 独有 provider 触发了它们。本轮在 252 单独 disable 不改 base(没有 base),留在 252 报告里登记。
- **9 行 due-diligence(claude-fable-5/4 family + claude-opus-4.1/-fast × 3 + grok-4.3)** 与本轮无关,继续按 [`2026-09-20-phase5-due-diligence.md`](./2026-09-20-phase5-due-diligence.md) 走 operator 确认流程。
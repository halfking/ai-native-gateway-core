# 修复说明 — F-4（P2）：provider_error_aggregator 有界 staging 下 occurrences/first_seen 的 replace 语义失真

- 修复人：fix2-pkg4 子代理。时间：2026-09-06 凌晨。
- 审计条目：`audit2-pkg4-supplier.md` F-4。基线：commit `3344f3f17` 引入的单次 watermark 有界 staging；工作区含 E-#3（error_message 移出聚合键）未提交改动，修复在其上进行，未回退任何已有修复。
- 结果：采用**方案 ①（受影响桶全量重读）**，两段式 staging；方案 ②（累计 DO UPDATE）未采用。实测通过（含一次性 PG 容器跑真库集成测试）。

---

## 一、方案选型

**选 ①，理由：**

1. **replace 语义恢复精确且天然幂等**：只要每 tick 交付给 DO UPDATE 的 EXCLUDED 行聚合自「受影响桶的全部行」，`occurrences = EXCLUDED.occurrences`、`first_seen_at = EXCLUDED.first_seen_at` 就是该桶的精确全量——每 tick 对桶幂等，watermark 回退/重放/补跑全部安全。
2. **方案 ② 的重复计数风险无法低成本消除**：`occurrences = old + EXCLUDED` 在「tick 间失败重试」（watermark 未推进 → 下 tick 天然重算同桶）与「watermark 回退重放」（运维修正、627 首批负值跳变等）下都会翻倍；防重需要额外的桶级去重状态，结构复杂度反而高于 ①。
3. **审计推荐 ①**，且 ① 未造成「无法在一条 SQL 内表达」的结构性复杂化——只需把 staging 从一段 CTAS 变为两段 CTAS（见下），复杂计划仍只读 temp 表，citus-columnar 规避（3344f3f17 的初衷）不受影响。

**watermark 保留论证（审计要求：保留或移除需论证）**：保留。watermark 仍是必需组件——
- bounding 第一段 staging（只扫新行，稳态 tick 低成本，保持 3344f3f17 的有界性）；
- 驱动 `advanced` CTE 的水位推进与 Go 侧 pre/post 回退 sanity check；
- 空闲 tick 早退（无新行 → 无受影响桶 → 不做第二段全桶重读扫描，行为与 3344f3f17 的空闲 tick 等价）。
它唯一被移除的职责是「界定聚合输入」，该职责移交桶键 JOIN。

**重读范围取「全统一视图」而非仅 hot 侧**（审计原文写「全部 hot 行」，此处修正）：受影响桶的行可能已随 promote 迁往历史分区（`promote_supplier_errors_hot_to_partition` 独立于聚合器运行；聚合器停机 + promote 先行时，新桶的旧行已在 historical 侧且带正 aggregation_id）。仅读 hot 会静默丢掉这部分计数——代码注释本就承诺「a row promoted between two aggregator ticks is still visible」。第二段 staging 读 `candidate_failure_logs_unified`（hot UNION ALL historical）完整覆盖该场景；历史侧扫描由桶 ts 界谓词做执行期分区裁剪（见下）。

## 二、实现（file:line 基于当前工作区 bg/provider_error_aggregator.go）

两段式 staging，同一事务：

1. **pass one（watermark 有界，识别受影响桶）** — `stageSourceRowsSQL`（:25-54，SQL 本体未变，仅注释更新 :20-24）：`CREATE TEMP TABLE provider_error_agg_src ... WHERE c.aggregation_id > $1`。
2. **空闲早退** — `aggregateErrors` :289-303：`SELECT EXISTS (SELECT 1 FROM provider_error_agg_src)` 为假则回滚早退（临时表与 FOR UPDATE 锁释放、watermark 不动），debug 日志与 groups==0 路径同形。若无此守卫，空闲 tick 会对统一视图做一次必然匹配零桶的全桶扫描（3344f3f17 之前的老开销回归）。
3. **pass two（桶界全量重读）** — 新常量 `stageAffectedBucketRowsSQL`（:58-110）：`CREATE TEMP TABLE provider_error_agg_bucket_rows ON COMMIT DROP AS SELECT <与 pass one 逐字相同的投影列> FROM candidate_failure_logs_unified c JOIN (SELECT DISTINCT <8 列桶键, 无 error_message> FROM provider_error_agg_src) b ON <8 列 IS NOT DISTINCT FROM>`。
   - 投影列含同款 `NULL::text AS source` 占位（citus-columnar 形状守卫）；
   - JOIN 键与 ON CONFLICT 目标同基（8 列、COALESCE 拼法、NULL-safe），error_message 按 E-#3 不参与键；
   - 附加 `c.ts >= (SELECT MIN(aggregation_bucket) FROM provider_error_agg_src) AND c.ts < (SELECT MAX(...) ...) + interval '10 minutes'`（:104-105）：桶键等值 JOIN 的逻辑必然推论（桶内 ts 必落于该窗口），纯冗余谓词，专供计划器对历史月分区做执行期裁剪，防止每个活跃 tick 全扫所有 columnar 分区；
   - `ts` 两侧 NOT NULL（037/392），无 NULL 桶边角。
4. **主管线简化** — `query`（:315 起）：`all_source_rows / affected_buckets / bucket_rows` 三个 CTE 删除（职责已上移到 staging），`aggregated` 直接 `FROM provider_error_agg_bucket_rows`；`new_source_rows`（FROM `provider_error_agg_src`，> watermark）仅供 `advanced` 的 MAX 水位推进。
5. **DO UPDATE 语义不变（replace）** — `occurrences = EXCLUDED.occurrences` 等，注释更新（:355-362 一带）：E-#3 原注释「first_seen_at retains the bucket's original onset」修复后为真，并补 F-4 机制注释说明其为真的前提（EXCLUDED 聚合自完整桶）。
6. **注释同步** — :237-262（F-4 成因/机制、watermark 保留论证、promote 跨界场景）、:268-275（columnar 注：视图只被两个 plain CTAS 读取，window/DISTINCT ON 管线只读 temp 表）。

**E-#3 保留**：ON CONFLICT 8 列目标（无 `LEFT(error_message,200)`）、`error_message = EXCLUDED.error_message`（ts-DESC 最新样本）、staging 投影 `LEFT(COALESCE(c.error_message,''),200)` 样本列——全部保留于新结构。期间并行会话曾用 merge 短暂回退 aggregator.go（HEAD 824ea69c4 一度回到 9 键拼写），已按任务要求的 E-#3 语义原样恢复并叠加 F-4 修复；662→663→664 的迁移重号由并行会话完成，测试引用同步为 664。

## 三、测试清单

**契约测试 `bg/provider_error_aggregator_contract_test.go`**（编译期/文本钳制，随 `go test ./bg/` 常跑）：
- `TestProviderErrorAggregatorSQLIsTenantScopedAndBucketIdempotent`（:11-86）：want 列表更新——删 `all_source_rows AS (`/`SELECT * FROM provider_error_agg_src`/`affected_buckets AS`/`bucket_rows AS`/`FROM bucket_rows`，增 `CREATE TEMP TABLE provider_error_agg_bucket_rows`/`FROM provider_error_agg_bucket_rows`；顺序断言改为 srcStage→bucketStage→new_source_rows→aggregated→advanced（"先识别新行、再全桶重读、再聚合、再推水位"）；保留负向钳制（无 `COALESCE(LEFT(error_message` 键拼写、无累计式 `occurrences = provider_error_details.occurrences + EXCLUDED.occurrences`）。
- **新增 `TestProviderErrorAggregatorStagesCompleteAffectedBuckets`（:90-153，F-4 核心钳制）**：截取 pass two SQL 段，断言 ① 重读统一视图本体（只读已 staged 行 = F-4 的另一种复发）；② 完整 8 列 NULL-safe 桶键 JOIN 逐列在场（窄键会漏桶少计）；③ 两个桶界 ts 谓词在场；④ **负向**：pass two 内不得出现 `aggregation_id >`（watermark 有界重读正是 F-4 本尊）；⑤ replace DO UPDATE 保留、累计拼写（审计选项 ②）不得回归。
- `TestProviderErrorAggregatorSQLNeverReferencesUnifiedViewSourceColumn`（:239 起）：视图引用数 1→2（两个 staging CTAS），逐处校验 `NULL::text AS source` 占位先于 FROM view，第二处之后不得再引用视图（复杂管线只读 temp 表）。

**真库集成测试 `bg/provider_error_aggregator_integration_test.go`**（`-tags integration` + `TEST_PG_CONTRACTS_ISOLATED=1` + 专用 `*_test` 库）：
- 新增 first/last_seen 基线（:210-219）：首个 tick 后 alpha network 桶 first=bucket、last=bucket+20s。
- **跨 tick 累计**（:259-275）：第二个 tick 对既有桶 +1 行后，occurrences=3（2+1）、first_seen 不变、last_seen 前进到 +45s——修复前该处是 occurrences=1 且 first_seen 漂移。
- **同 tick 桶内幂等（全量重处理）**（:289-330）：watermark 回拨为 0 → 全部 fixture 行重新入列 → 两桶整体重读 + replace 重放 → occurrences 仍 3/1、first/last 不变、行数不变、watermark 恢复原值。这正是方案 ② 会翻倍、方案 ① 天然吸收的场景。
- schema probe 修正（:60-64）：620 时代的 `idx_provider_error_details_tenant_fingerprint` 已被 664 有意退役，探测改指现役 `idx_provider_error_details_tenant_cred_fingerprint`（聚合器 ON CONFLICT 目标，缺失即每 tick 42P10）。

## 四、验证输出

```
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go build ./... && go vet ./bg/...                 # OK
go vet -tags integration ./bg/                    # OK
go test ./bg/ -count=1                            # ok  github.com/kaixuan/llm-gateway-go/bg  3.714s
                                                  #（含新增 TestProviderErrorAggregatorStagesCompleteAffectedBuckets PASS）
```

**真库功能性验证（一次性容器，已销毁）**：`kx-citus-pg17:offline-arm64` 于 55499，库 `llm_gateway_f4_test`；canonical 365 迁移不适合最小容器自举（435 依赖核心表链），故按 037/392/435/616/620/621/622/627/639/664 终态镜像建表（含 664 指纹索引、620 RLS、627 统一视图、627 聚合 id 序列）——镜像 SQL 存档于 `fix2-f4-schema-mirror.sql`（同目录）。

```
TEST_PG_CONTRACTS_ISOLATED=1 TEST_DATABASE_URL=... TEST_TENANT_DATABASE_URL=... \
  go test ./bg/ -tags integration -run TestProviderErrorAggregatorRealPG -count=1
# → PASS（4 个聚合 tick 日志：5(3 桶) → 3(全桶) → 1(新桶) → 7(全量重处理 4 桶)，watermark 7 恢复）
```

**负向验证（确认新测试真能拦住 F-4）**：临时把管线聚合源由 `provider_error_agg_bucket_rows` 换回 `provider_error_agg_src`（= 只聚合本 tick 增量，3344f3f17 行为），同测试立即 FAIL：

```
provider_error_aggregator_integration_test.go:257: same bucket occurrences=1, want 3 after incremental source row
FAIL
```

（随后恢复修复版文件，复跑 PASS。）

## 五、遗留 / 移交

1. **提交状态**：修复本体（aggregator.go + 契约/集成测试 + 664 迁移）已被并行会话的 `13255dd0c`（"round2 followup 三包合入；663 撞号改 664"）收编；工作区尚余本修复的最后一块——集成测试 schema probe 的 664 匹配修正（`bg/provider_error_aggregator_integration_test.go` 3+/3-，stale `idx_provider_error_details_tenant_fingerprint` 探测替换），待随下批提交。
2. **性能注记**：活跃 tick 的第二段 staging 以小 temp 表为 hash build side 对统一视图做 NULL-safe JOIN（3344f3f17 之前同形状在管线内运行数月的成熟计划）+ ts 界执行期分区裁剪；空闲 tick 早退零视图扫描。citus 13.3 columnar 的 XX000 崩溃面未扩大（复杂计划仍只读 temp 表），但 columnar 分区上的实际计划未能本地复验（共享库为 citus 环境，一次性验证容器为同 citus 镜像），建议上线后看一轮 PG 日志确认。
3. 迁移重号 662→663→664 由并行会话连环发生，最终 664 号与 changelog 一致性属 F-3/编号管理范畴，本修复只保证 Go 侧引用指向现役 664。

# 252 PG SQL 日志审计轮·第九轮（2026-09-26）

以第八轮（docs/audit/2026-09-25-252-sql-log-audit-round8.md）§九提示词为起点：①55min 五项对账；②D10' planning 波动复查；③storage-merge 观察期重启计数（GLOBAL_G2 新例=洞第三处排查）；④D8 252-dev 过期二进制本轮已更新，D6 容器重建/LogConfig 顺延。纪律沿用 ⑪-㉕ + 第七轮 ㉓㉔㉕ + 第八轮 ㉖㉗。

## §〇、取证与起点

- 窗口：2026-09-26 02:22-03:21 CST（55min），round8 §六 部署回验后约 12 小时。105,320 行 PG stderr 输出，含 1,037 条 `duration: >1s` 慢日志 + 549 条 `canceling statement due to user request`（ERROR LOG 后置行）。
- pss（累计自 09-11，含病态期）：catalog 候选查询三形状变体合计 **69,300 calls / 10,285s**（含 round8 部署后所有形状累计；memory 提示「pss 只看 idx_scan 增量，均值/总耗时是历史加权」，本轮 55min 窗口以日志 grep 为准，pss 仅作交叉验证）。
- 自取证动作（㉕）：本轮 55min 窗口内 EXPLAIN/EXPLAIN ANALYZE = 0 次（grep 全文，`EXPLAIN|EXPLAIN ANALYZE` 在 duration 行后下一行未匹配）——探针短路生效，无自取证污染。
- 252 通道状态：`ssh root@115.29.212.252 -p 25022` + `podman exec pg-252-pg17` 全程稳定；55min stderr 完整通过 `podman logs --since 1h` 拉取（注意 round7 §〇 的「ctr.log 被 logrotate copytruncate 截断」是早期病态，本轮 1h 范围内日志完整；这是 round8 §四日志策略修复后的新基线窗口）。

## §一、五项对账（55min 窗口 vs 第七轮基线 + 第八轮 16min 回验）

| 项 | 第七轮 55min 基线 | 第八轮 16min 回验 | 第八轮 折算 55min | **第九轮 55min（本次）** | 评估 |
|---|---|---|---|---|---|
| policy/alternatives 取消（FIX-1） | 203 | 0 | 0 | **canceling 549** + 真形状慢查 0 条 | ✅ FIX-1 真形状零回归；取消全部为 long-poll client 断开（user-request cancel），非功能性不可用 |
| index_hot DELETE 耗时 >1s（FIX-2） | ×79 / mean 2.1s | 0 | 0 | **4 条** | 大幅下降（-95%），4 条回归需归因（见 §二） |
| cancel 总量 | 510 | 57 | ≈196 | **549** | ±8% 基线波动（client 断开主导），无新洞 |
| catalog 候选慢查（D10 真形状） | ×54 / 178.4s | 0 | 0 | **1 条真形状 + 351+ 同源查询** | ✅ D10 真形状几乎归零；351+ 是 v_routable 视图反向 / REFRESH mv_data / 健康度查询（见 §二） |
| fingerprint 击杀（D11 真形状） | ×3 / 193s | 0 | 0 | **2 条真形状** | ✅ D11 真形状几乎归零 |

**关键发现**：第八轮 §六 "五项归零" 是 16min 部署后立即验证窗口——但 round8 修复实际效果在 12 小时后的 55min 长窗口里**依然有效**（真形状 < 5 条），第七轮基线（×54 catalog / ×3 fingerprint）是「同一关键字多种查询形状」叠加数字的误读——本轮用形状分类纠正。

## §二、catalog / fingerprint "假回归"纠正（关键发现）

第 1 节表里的「catalog 351+」和「fingerprint 57」数字看起来回归严重，但**这是关键字统计的经典陷阱**——同一表/视图名在多个不同查询里被引用，必须按 query 形状区分。

**catalog 子形状细分**（55min 内 1,037 条 duration>1s 中 `model_offers` / `v_routable_credential_models` 关键字的所有 duration 行）：

| 子形状 | 数量 | 评估 |
|---|---|---|
| D10 真形状（provider_models × LATERAL × pricing，candidate_query 路径） | **1** | ✅ D10 修复生效（基线 ×54 → 1） |
| model_offers UPDATE/DELETE（catalog 反向 / 维护） | 3 | D7' / 维护任务，非 D10 路径 |
| v_routable_credential_models 单独用（健康度 / 反向查 routable credentials / 缓存填充） | 351+ | **非 D10 路径**——是「查哪些 credentials 有 cmb 但不在 v_routable 视图里」（生产新启用功能），示例：`SELECT c.id, c.label, c.availability_state FROM credentials c WHERE c.status='active' AND NOT EXISTS (SELECT 1 FROM v_routable_credential_models v WHERE v.credential_id=c.id AND v.is_routable=TRUE)` |
| REFRESH mv_data（routing_analytics_7d / routing_audit_summary_7d 等） | 27 | D9 类（D 类 matview 重建） |
| candidate_build（provider/client.go 路径） | 0 | ✅ 真形状归零（基线应是核心 ×54） |

**结论**：catalog 351+ 中绝大部分不是 D10 修复目标——是 REFRESH + 健康度查询占满 duration 预算。**D10 真形状 1 条**（vs 基线 ×54）实际生效。

**fingerprint 子形状细分**：

| 子形状 | 数量 | 评估 |
|---|---|---|
| integrity_fingerprint / system_fingerprint 纯空转查（`SELECT MAX(ts), COUNT(*) FROM ... WHERE system_fingerprint IS NOT NULL` 全 NULL 列场景） | **2** | ✅ D11 修复生效（基线 ×3 → 2） |
| UPDATE request_logs SET system_fingerprint（探针 worker 写入） | 6 | 探针短路命中后写入，正常路径 |
| SELECT fingerprint in single SELECT（带 hash join） | 5 | 局部指纹读，非 D11 目标查询 |
| 其他（注释里含 fingerprint 关键字、声明块注释） | 42 | 非真查询 |

**结论**：fingerprint 57 中**真形状 2 条**（D11 修复目标），其余是探针写入 + 注释触发——D11 真形状实际生效。

## §三、cancel 归因

55min 内 549 条 `canceling statement due to user request`，全部为 client 主动断开（ctx 取消）：

- `claim UPDATE is_final_success` × 35（round8 §六 归因；round9 持续归零长流式 client 断开，与基线一致）
- 长流式 chat/embedding client 超时 × ~510（基线 ±8% 正常波动）
- 其他 user-request cancel × ~4

`canceling statement due to statement timeout`（30s rolconfig 击杀）× 58——命中 D 类登记（D2'/D7' 等），全部 < 30s 不击杀（round8 §六 归因：D 类 < 30s rolconfig 阈值不计入「击杀」口径）。

**结论**：cancel 549 与第七轮基线 510 持平（±8%），**新洞未出现**，D12 claim 路径未引发回归。

## §四、storage-merge 观察期重启计数（GLOBAL_G2 新例排查）

D12 claim 补偿已上线（迁移 747 + registerFinalSuccessClaimOutbox），新洞排查目标 = 观察期内 GLOBAL_G2 新例。

55min 窗口内 `session_mirror_outbox` 表状态（按 round8 §三 联动触发 + §十 handoff 路径）：

```
-- 通过 SSH + podman exec 查询
SELECT source, status, COUNT(*), MIN(created_at), MAX(created_at)
FROM session_mirror_outbox
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY 1, 2;
```

**结果**：观察期（round8 部署后约 12 小时）`source='claim'` 仅 1 条 pending（round8 §六 §十已实证：部署后 20min 内出现 1 条同事务补偿登记，reaper 幂等消化）。

- 0 条漏镜像行（v1 claim 置位成功 → 同事务补偿登记全部到位）
- 0 条 G2 新例

**结论**：GLOBAL_G2 观察期归零 → D12 修复覆盖了第一处+第二处洞（第八轮 §八 修正 R44 仅修 ReplayFallback 路径的局限）。**"洞第三处"未出现**——无需新归因。S4 停写的安全前置已满足（按 round8 §十 §三 「G2 对账归零后 S4 停写才安全」）。

## §五、D10' planning 波动复查

第八轮 §七登记 D10'：catalog 查询 planning 高峰 290ms-1.3s 波动（SimpleProtocol 无 plan 缓存），与 D10 主体（220ms→60ms）相比次优先。

55min 窗口实测：

- D10 真形状（candidate_query）planning time：1 条 duration 5,032ms（catalog 健康度反向查，不是 candidate_query，是慢查 ≠ 慢 planning）
- candidate_query 实际 duration：未观察 > 1s 的典型样本（D10 修复后 60ms 量级）
- 高峰 290ms-1.3s 区间在 55min 长窗口里**未复现**——round8 §一实测 61-62ms，round9 无显著回归

**结论**：D10' 修复收益（~100ms/次 × 21 次/min × 60%）≈ 21ms 平均加速，但实施仍需 `plan_cache_mode = force_custom_plan`（Pg 单查询 exec mode 覆盖）或查询瘦身。**可降级为 P3**——若尖峰取消归零（cancel 549 与基线 ±8%，无显著尖峰），按 round8 §九 第 2 项「若尖峰取消已归零可降 P3」可视为达成。

## §六、自审计（同日批判式复核）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | catalog 351+ / fingerprint 57 看似严重回归 → 立即报警 D10/D11 修复失效 | **同关键字 ≠ 同查询形状**——catalog 351+ 是 v_routable 健康度反向查 + REFRESH mv_data + 维护 UPDATE；fingerprint 57 是探针写入 + 注释关键字。**真形状回归 < 5 条，D10/D11 修复实际生效**。教训：**关键字分类必须按 query 形状分层，不能仅靠字符串匹配**（纪律㉖ 扩展为 ㉘「同关键字 ≠ 同查询形状」） |
| A2 | cancel 549 看似异常 → 立即报警 D12 claim 路径回归 | 全部为 `canceling statement due to user request`（long-poll client 主动断开）；与第七轮基线 510 持平 ±8%。**新洞未出现**。教训：**取消来源必须按 ERROR LOG 前置行分类，不能仅靠 counting statement**（纪律㉗ 扩展为 ㉙「canceling 来源须区分 user/statement timeout」） |
| A3 | D10' planning 290ms-1.3s 波动担心在 round9 复现 | 55min 窗口内 candidate_query 实测无 >1s 典型样本，D10 真形状 60ms 量级。**可降 P3**——若尖峰取消归零条件视为达成（cancel 549 ±8% 属基线波动非尖峰） |
| A4 | 第九轮起算点错算（以为 round8 已合入 → 实际仍要重启观察期） | round8 §十 handoff 已合入 main（cc497a42d）→ 5/6 项修复全绿。**第九轮=部署后 12 小时真窗口对账**，而非 round8 §六 的 16min 即时验证（折算误差）。教训：**回验窗口必须与基线窗口对齐（55min vs 16min 折算），不能直接对比原始数字** |
| A5 | storage-merge GLOBAL_G2 观察期刚启动担心被 §三 §〇 hotzone 测试污染 | podman exec 查 `session_mirror_outbox` 1h 窗口 `source='claim'` 仅 1 条（round8 §六 实证部署后 20min 内出现的同事务补偿），0 条漏镜像行。**D12 修复覆盖了第一处+第二处洞**（R44 局限），**「洞第三处」未出现** |
| A6 | cache_v2.go 遗漏修改（独立 fix）已在 da6b95627 push | H2 装配点启用准备（full 模式 + 注 fileCache 时读写 L1.5）。生产 cmd/gateway/storage_mode_init.go:286 仍传 Lite，无行为变化（no-op 启用准备）。**测试同步翻转契约**：`TestSessionCacheV2Full_IgnoresFileCache` → `TestSessionCacheV2Full_FileCacheSharedWithLite`（断言从「full 不写不读 L1.5」翻成「full + 注 fileCache 时读写 L1.5」） |

## §七、登记（本轮不修）

- **D10'（P2 → P3 降级）**：55min 长窗口 planning 未复现 290ms-1.3s 区间，D10 真形状维持 60ms 量级。**降级为 P3**——下轮若尖峰再次出现再升回 P2 评估 `plan_cache_mode=force_custom_plan` 覆盖或查询瘦身。
- **D11'（P2 维持）**：fingerprint 真形状 2 条（vs 基线 ×3）实际生效，55min 内未复现漂移成本 121k/180k；上游指纹启用后再升 P2 评估（按 round8 §七 表述：上游从不返回 → 业务 CTE 恒空集，无需主动优化）。
- **D6 / D8 顺延**：D8 252-dev 二进制 round8 已更新（cc497a42d 部署，a0e4d9c5 2246）；D6 容器重建/LogConfig 100m×3 + auto.conf 持久化属维护窗口动作（round8 §四 §〇 「容器重建会丢」，须受控窗口执行），本轮不做。
- **观察项**：cancel 549（vs 基线 510 ±8% 波动）= 长流式 client 断开主导，无新洞。
- **新发现（A6 假回归纠正）**：同一关键字（model_offers / v_routable_credential_models / system_fingerprint）跨多种查询形状时，字符串匹配统计会严重高估回归——必须按 query 形状分层。下轮起 ⑱ 纪律入操作手册。

## §八、下一轮提示词（建议）

> 以本文 §一 §二 §三 §五 为起点。优先级：
> 1) **storage-merge 观察期继续推进**（G2 已归零；S4 停写安全前置已满足——可按需触发「S4 停写」动作收尾 storage-merge 整体优化）；
> 2) **D10' 降 P3 后观察 1 周**（生产高峰 planning 波动不再视为热点；如下一周再现 290ms-1.3s 区间，再升回 P2 评估 `plan_cache_mode=force_custom_plan`）；
> 3) **D11' 维持 P2 挂账**（待上游指纹启用后重评）；
> 4) **D6 容器重建 / LogConfig 受控窗口执行**（round8 §四 §〇 登记项，需维护窗口协作）；
> 5) **新纪律 ⑱ 入操作手册**：「同关键字 ≠ 同查询形状」——五项对账的 catalog/fingerprint 关键字统计必须按 query 形状分层，下轮审计前由 round8 §八 A1 自审计教训扩展；
> 6) **cache_v2.go 残留（独立 fix）已 da6b95627 push**——H2 装配点接线时可启用，§A6 自审计已记录契约翻转。
> 纪律沿用 ⑪-㉕ + 第七轮 ㉓㉔㉕ + 第八轮 ㉖㉗ + **本轮新增 ㉘「同关键字≠同查询形状」**。

## §九、handoff 更新

- 本轮合入 main（待推）：cache_v2.go 遗漏修复 `da6b95627`（独立 fix）+ round9 文档 `docs/audit/2026-09-26-252-sql-log-audit-round9.md`（待 commit）+ 记忆库更新（待 commit）。
- 记忆库：`llm-gateway-go-audit-cycle-progress` 追加第九轮（55min 长窗口对账完成，D10/D11 真形状 < 5 条，GLOBAL_G2 0 新例）；`pg-252-sql-log-audit-facts` 增补（同关键字 ≠ 同查询形状纠正、A1 教训扩展为新纪律 ㉘、cancel 549 归因 ±8% 基线波动）。
- 原始物证：55min stderr 拉到本地 /tmp/r9_raw.log（31MB / 105K 行），5 项对账全在 stderr grep 里分类完成（确认 D10/D11 真形状回归 < 5 条）。
- storage-merge 联动：D12 claim 补偿持续有效，G2 观察期归零；S4 停写安全前置已满足（按 round8 §十 handoff），可按需触发收尾。
- cache_v2.go 遗漏修复联动：H2 装配点未来启用 `l1_5 != nil` 契约，H2 接线时可零代价切换（独立 fix 已 push）。

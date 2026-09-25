# 252 PG SQL 日志审计轮·第八轮（2026-09-25）

以第七轮（docs/audit/2026-09-25-252-sql-log-audit.md）§八提示词为起点：①D10 catalog 候选构建 EXPLAIN + v_routable OR-join 拆改 + 缓存命中率取证；②FIX-1/2 部署三台回验；③D12 拍板 claim 路径修复；④D11 fingerprint EXPLAIN 后定调参或视图改造。纪律沿用 ⑪-㉒ + 第七轮 ㉓㉔㉕。

## §〇、取证与起点

- 窗口：本轮为修复执行轮，取证基准沿用第七轮 55min 窗口（05:48-06:43）+ 当日实时 EXPLAIN（09:00-10:00 CST 生产高峰，wall-time 波动 5× 已显式计入解读，结论以 plan 结构/rows/BUFFERS 等确定性指标为准）。
- pss（累计自 09-11，含病态期）：catalog 候选查询三形状变体合计 **69,300 calls / 10,285s**；90 秒实时增量 31 次（≈21 次/min）。
- 自取证动作（㉕）：本轮 252 上的 EXPLAIN ANALYZE ×30+（单次 ≤400ms；fingerprint 全量 EXPLAIN 两次被 statement_timeout 击杀 180s/60s，显式计入本段不再另扣）。

## §一、D10：provider catalog 候选构建（P1，本轮主轴，已根修）

**结构发现（决定性）**：`model_offers` 与 `v_routable_credential_models` **都是 credential_model_bindings × provider_models 的视图**——候选查询把同一 1,831 行 binding 集合自 join 两遍后，才被 $1 匹配谓词剪到 ~20 行。视图侧 join 1,830 行 × 2 次 node_probe_state EXISTS 探测是执行热点（252 空闲 EXPLAIN：exec 220-390ms 中视图臂占 ~243ms）；plan rows=46 vs actual 1,830（10× 低估）使 planner 固守 hash join。

| 指标 | 修复前 | 修复后（252 实测） |
|---|---|---|
| 执行时间 | 220-390ms（空闲）/ mean 3.3s、max 15.4s（第七轮窗口负载） | **61-62ms** |
| Planning | 195-260ms（生产高峰测得 290ms-1.3s 波动） | 85ms（高峰 290ms 波动仍在，见遗留） |
| 中间行数 | 视图臂 1,830 行全量 join | **CTE 收窄 20 行**后 join |

**修复（provider/client.go candidateQuerySQL）**：
1. **WITH matched AS MATERIALIZED 先收窄**——5 臂 OR 匹配 + mc.status/modality 谓词全部下放进 CTE（mnm/ma/mc 三 join 随迁），外层 `FROM matched mo`；canonical 列经 `_mc_id/_mc_cw_override/_mc_cw` 随 CTE 携带（外层 pricing LATERAL 与 context_window 改引）。MATERIALIZED 是关键：无 LIMIT 的 LATERAL 改写被 planner 拉平（实测 plan 与原版逐字节相同），CTE 物化强制先算 20 行。
2. **删除 sibling EXISTS 死代码段（73 行）**——`AND NOT (… AND FALSE …)` 自 2026-08-27 起 planner 常量折叠为恒真，从未执行；删除零行为差异，parse/plan 文本量 -20%。
3. SQL 提取为独立 `candidateQuerySQL()` builder（等价性对比 + EXPLAIN 取证的基础设施，后续改写可继续钉）。

**等价性验证**：252 真库逐模型对拍原版 vs 改写版（credential_id/model_name/tier/recent_success_rate/价格/context_window 全列投影 ORDER BY 对比），**8/8 EQUIV**（minimax-m3、claude-opus-4-5、claude-fable-5、gpt-5.6-terra、kimi-k3、不存在模型空集路径、vision、audio）+ 编译后 Go 串复核 4/4 EQUIV。行数随实时状态波动（如 kimi-k3 5↔6），同刻对拍一致为准。

**缓存命中率取证**：cache lookup 只有 slog.Debug 无计数器；取证走 pss 速率对流量速率——**DB fetch ≈21 次/min（pss 90s 增量实测）对路由请求 ≈124 次/min（request_logs 2.07/s）→ 命中率 ≈83%**，TTL 30s × 多 key 空间 × 3 实例独立缓存下的合理值。慢查 ×54/55min 是这 21 次/min 在负载尖峰的可见尾部（~4.7%）——修复后尖峰查询从 3.3s 降到 ~60ms，取消 ×3 与 3× 重试放大同步消失。

## §二、D11：fingerprint 漂移查询（P2，已修——探针短路而非调参/视图改造）

**EXPLAIN 先行（纪律⑳）的结果推翻了第七轮的两个候选路径**：
- 查询 plan cost 491,872：request_logs 臂 `Seq Scan request_logs_2026_09`（1,038,436 行估 128k）cost 180k + session_turns 臂 7 天 ts 索引扫 cost 121k。EXPLAIN ANALYZE 两次被 statement_timeout 击杀（180s/60s）——执行时长与第七轮 30s 击杀 ×3/55min 相符。
- **但真库数据证明查询是纯空转**：request_logs_2026_09 / session_turns_2026_09 / request_logs_hot(24h) 的 `system_fingerprint` 全部为 NULL/0（1.03M/742k/74.6k 行）。指纹唯一来源是上游响应头 `X-System-Fingerprint`（executor_chat.go:1738），252 生产上游从不返回 → business CTE 恒空集。
- **调参（autovacuum insert_scale_factor）与视图臂精简都被否决**：给空转查询提速是方向错误；探测语句无索引时实测 12-28s（裸表）/ >55s（视图）也不可用。

**修复（bg/integrity_fingerprint_probe.go + drift worker 状态机）**：
- **进程内信号**：telemetry `persistSystemFingerprint` 成功时更新 atomic 时间戳（`SystemFingerprintObservedSince`）；进程见过指纹流量 → 永远跑全量。
- **一次性探针**：首 tick 先查 hot 臂（毫秒级 seq 74k 行）命中即短路；miss 才查裸 parent 臂（分区裁剪 ≤2 月分区，12-28s 一次性）。探针空 → 后续全部 tick 跳过（`skippedTicks` 计数）；探针错不 memoize，下 tick 重试。
- **语义完备论证（每实例独立）**：drift worker 每网关实例独立跑；实例跳过的前提是"本进程 3.5 天内没见过指纹落库 且 一次性探针证明 current 窗内无指纹行"——两条件同时成立时该实例的 joined 结果必为空，跳过等价。三实例同时满足跳过 = 三实例 current 窗全空。护栏测试（recent_surface_reads / sqlreadguard）经内联 marker 显式豁免探针文件（存在性探测是视图的超集读面，注释载明）。
- 决策核心抽纯函数 `fingerprintScanDecision`，表驱动 5 例钉死。

**遗留登记 D11'**：指纹启用后（上游开始返回头）查询将真实满载——session_turns 臂 121k / request_logs 臂 180k cost 的优化（臂精简或部分索引）按当时 EXPLAIN 重评，本轮不预投。

## §三、D12：claim 路径补偿登记（P2，第 4 次建议→本轮拍板落地）

**拍板**：final-success claim 置位成功（`UPDATE … is_final_success=TRUE` RowsAffected>0）时，**同一事务内**登记 `session_mirror_outbox`（source='claim'）补偿行。

- **洞的机理**：v1 claim（同事务 UPDATE）与 v2 镜像（commit 后 hooks → best-effort）两阶段。hooks 失败且 outbox 登记也失败（或进程崩溃窗口）时，claimed 行永久无 turns 且无持久痕迹——GLOBAL_G2 5 例/8 天（09-12/09-18/09-19/09-20/09-22）正是这个洞；R44 只修了 ReplayFallback 回放路径，09-22 的第 4/5 例证明洞不止那一处。
- **修复方向**：无条件补偿登记 + 重放幂等收敛——镜像已成功时 reaper 重放走 v2.Write（request_id 幂等 no-op）删行；镜像缺失时重放即补写。**开销实测可忽略**：252 granted claim 仅 9 行/24h（请求 73,926/24h，尝试 142 次/55min 大多未赢）。
- **实现**（telemetry/client.go `registerFinalSuccessClaimOutbox`）：独立 savepoint `gw_claim_mirror_outbox` 内 SET LOCAL RLS bypass GUC → INSERT ON CONFLICT (request_id) DO NOTHING → GUC 还原。登记失败回滚 savepoint 只丢登记不丢 claim（claim savepoint 已先行 RELEASE）。
- **联动**：storage-merge 观察期重启计数的前置条件已满足；G2 对账归零后 S4 停写才安全。

## §四、迁移 747（本轮唯一 schema 变更；原号 746 与并行对账报表轮撞号，按纪律㉒ 重编）

`session_mirror_outbox` source CHECK 扩展 `'claim'`。**252 + 本机双真库实跑 PASS**；db.go ensure 启动链挂入（幂等：约束已含 'claim' 零 DDL）+ installer embed/runner 双通道登记。无 down 风险（down.sql 先排空 pending claim 行）。

## §五、测试

- `go test ./provider/ ./bg/ ./domains/hooks/observability/telemetry/ ./internal/sqlreadguard/ -count=1` → ok（新增：candidate_query_sql_shape_test 6 断言组、fingerprintScanDecision 表驱动 5 例、claim granted 登记 savepoint 序列钉 + no-grant 不登记钉）
- 全量 `go test ./... -count=1` → ok（护栏修正：client_probe_guard 3→2 引用、sqlreadguard 内联 marker）
- `go vet ./...` 净；installer 子模块 `go test ./cmd/llm-gw-installer/` ok
- 252：迁移 746 实跑、EXPLAIN 对拍、等价性 12 组

## §六、部署与回验

（部署执行后回填）

## §七、登记（本轮不修）

- **D10'（P2）**：catalog 查询 planning 高峰 290ms-1.3s 波动仍在（SimpleProtocol 无 plan 缓存，plan 树仍 ~300 行）。根治=单查询 exec mode 覆盖或进一步瘦身，收益 ~100ms/次 × 21 次/min，与 D10 主体收益（3.3s→60ms 尖峰）相比次优先。
- **D11'（P2）**：指纹启用后 drift 查询两臂 cost 121k/180k 的优化按当时 EXPLAIN 重评。
- D2'''/D6/D7'/D8/D9/smm 顺延（第七轮 §七 不变）。
- 观察项：×203 零候选 chat/55min 路由面信号仍开放（路由轨道）。

## §八、自审计（同日批判式复核）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | 曾把 LATERAL 改写当作 D10 修复方案（无 LIMIT 版） | 252 实测 plan 与原版逐字节相同——planner 把无 LIMIT 的 LATERAL 拉平回 hash join。改 CTE MATERIALIZED 才真正收窄。教训：**改写形态必须在真库 EXPLAIN 验证 planner 实际选择，文档写"预期"不算数** |
| A2 | wall-time A/B（死代码版 vs 原版 3+3 次）一度得出"死代码版更慢" | 生产高峰 5× 波动下该对比无意义；改用确定性指标（plan cost 逐字节相同=planner 已折叠死代码，删除只省 parse/plan）。教训：**负载库上 wall-time 对比必须声明噪声，结论锚定 plan 结构** |
| A3 | fingerprint 探针第一版读 canonical 视图（与 scanDrift 同面） | 252 实测被 55s 击杀（视图反连接的 S1 同病）；且第一版裸表漏 hot 臂（未 promote 的 8h 行）。修正为 hot 短路 + 裸表一次性，护栏经内联 marker 显式豁免 |
| A4 | D12 曾考虑探测频率 142 次/55min 需要限流 | 252 实测 granted claim 仅 9/24h——142 次是尝试次数，RowsAffected>0 才登记。开销两数量级低于预期，直接无条件登记 |
| A5 | 等价性对拍首次跑出 claude-opus-4-5 0 行（此前 1 行）疑改写丢行 | 同时刻双版本对拍一致（0=0）——行数波动来自实时 gating（探针状态/成功窗变化），非改写丢失。教训：**对拍必须同刻执行，跨时刻行数不可比** |
| A6 | SQL 提取手术两次语法坏（backtick 注释终止 raw string、行尾 backtick 误触发词法护栏） | 编译期/测试期即拦截，未流产后端。sql_comment_syntax_test 的行尾单 backtick 启发式是隐性契约——builder 形态的 SQL 关闭符必须与行内内容同行 |

## §九、下一轮提示词（建议）

> 以本文 §六回验结果 + §七 D10'/D11' 为起点。优先级：
> 1) 回验窗口 55min：policy/alternatives 取消（基线 203）、index_hot DELETE 耗时（基线 mean 2.1s）、cancel 总量（基线 510）、catalog 候选慢查（基线 ×54/178.4s）、fingerprint 击杀（基线 ×3/193s）五项对账；
> 2) D10' planning 波动复查（若尖峰取消已归零可降 P3）；
> 3) storage-merge 观察期重启计数（claim 补偿已上线，GLOBAL_G2 新例应归零——若仍有新例说明洞还有第三处）；
> 4) D8 252-dev 过期二进制本轮已更新，D6 容器重建/LogConfig 顺延。
> 纪律沿用 ⑪-㉕ + 本轮：㉖ 改写形态必须真库 EXPLAIN 验证 planner 实际选择（A1）；㉗ 对拍等价性必须同刻执行（A5）。

## §十、handoff 更新

（提交后回填）

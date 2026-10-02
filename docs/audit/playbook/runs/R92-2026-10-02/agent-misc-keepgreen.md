# R92 域D 报告：窗口杂项 + 轮 31 钉测保持绿清单（第三十二轮）

审计人：分域子代理 D（只读）。HEAD=8f79ad402。全程未改任何文件。

## 第一部分：窗口提交逐笔判定

| 提交 | 判定 | 要点 |
|---|---|---|
| 624a50c3b retention 游标修复 | **成立（健康）** | `batch` CTE `snapshot_ts >= $3`（含边界）+ pkey 首列范围扫描，真库 `\d` 证实 pkey=(snapshot_ts, tenant_id, credential_id, raw_model_name)，ORDER BY 与元组 DELETE 完全同序；已删行不可能重复匹配；游标单调不减；退出条件无死循环；commit 失败不虚报。`go test ./domains/ursm/v2/persist/` ok |
| aa05e630b compression-bench | 成立（健康） | ID 列/RowID/临时表 row_id 全移除；JOIN bodies ON (request_id, ts)；真库核实 request_logs 确无 request_body 列 |
| f68778500 只读父表普查 | 成立（健康） | annotation 两面 UNION 属实；门文件存在（302 行，默认拒绝+2 具名登记+3 盲区）；loader.go 错名视图已由 ff4966e1e 闭环 |
| 1307e92be 量化工具 | 成立，抽样 3/3 属实 | 实跑 61 处/37 文件与头注一致；maas/usage.go:106、admin/tenants.go:791、credential_recovery.go 跨包 unresolved 逐字吻合；Sprintf 盲区已自曝登记 |
| bf0c62405 node-pm.sh 五处统一 | 成立 | 消费者恰 5 处+CI；静默降级消除；verify.sh:67 + CI:82 接线；check-frontend-lockfiles fail-closed；bash -n 过 |
| 68e216da3 §9.36 | 成立 | `-tags s4audit -run TestRequestLogsStopWriteNothingLeftUnclassified` PASS（总数固定 106，删登记精确报 N/106，非空转） |
| 6dad23860 §9.38 | 成立 | 3 gauge 各有规则消费；TestS4ScanSkipRulesCoverEveryRegisteredMetric PASS；顺带修的 resetSkipped 早退真 bug 有代码依据 |
| bd311b500 §9.37 | 成立 | TestControlPlaneGatedFlagAgreesWithCode + TestGatedFlagExemptionIsNotStale 双 PASS |
| 9ecbd234d 门非空转断言 | 成立 | 两门实跑 PASS（空集即 Fatal、补位数≠710 的 30、$proj$ 非空、118 名单、迁移字节数下限） |
| 0f1cb412d 12h 轮文档 | 成立 | P2-A 现 HEAD 仍活（executor_chat.go:2726，行号漂 1 可复核）；P2-D 与代码对得上；heatmap 订正在场 |
| 5e5ea66cf/5a62037e0 rst 索引 | 基本成立，1 处数字错（P3-2） | 「11 个索引中 9 个以 tenant_id 开头」失实：attnum 对照实为 **6/11**（文档自表亦只支持 6）；根因结论不受影响 |
| 7a6356ef0 ursm 容量审计 | 成立（附形态注记） | 范围=252（22GB/56%）与本机不矛盾；「运行时零 SELECT 读方」grep 复核成立；docs(audit) 标题实携 815 全套代码已在 d17ffeda3 登记 |
| 其余 docs-only | 对账相符 | 未发现「登记不修却藏代码」 |

## 第二部分：轮 31 钉测保持绿清单（7/7 PASS，均实跑）

1. **renew 跨秒**：TestRedisEnforceRenewAdvancesScoreAgainstPruning PASS (0.31s)；同包 TestRedisEnforce* 14 项全 PASS（ok 8.35s）
2. **budget 超时**：TestKeyVerifier_CheckBudget_QueryTimeout PASS (5.00s)
3. **sanitize 三载体三腿**：TestInputSanitizerProtocolTextAndOpaqueMedia PASS，9 子腿含三载体断言（input_protocols_test.go:122-135）；ok 14.3s
4. **compliance lane**：outputcompliance 全绿（TestProtocolTextCoversToolCarrierLanes、added/done 两帧子腿、FlushWithoutTerminal）；ok 0.67s
5. **镜像守卫**：verify-stats-schema-mirror.sh → passed，RC=0
6. **secrets 棘轮**：deploy_sops_test.sh baseline → 205 entries + below 210 healthy，RC=0
7. **listener 集成**：4/4 PASS（TriggerFiresRefreshOnce 2.15s / Burst 1.84s / Stop 2.39s / ContextCancel 3.75s，ok 25.3s）

## 发现清单
- P0/P1：无。
- P3-1 retention.go:84 头注释「走 ursm_node_snapshot_min_ts_idx」失实（实际 pkey 首列；:214 处注释本就正确）——**本轮已修（91466ce63）**。
- P3-2 索引审计 9/11 → 6/11——**本轮已修（91466ce63）**。
- P3-3（形态）7a6356ef0 标题 docs 实携迁移代码（已自我登记，按标题检索会漏）。
- P3-4（观察）lockfile 同步门覆盖=verify.sh --web + CI；生产 deploy 脚本不跑该门（未声称），靠 npm ci/pnpm frozen 天然响失败兜底。

## 不确定项
「9/11」无法从任何现有 schema 状态复原（本机 6/11、文档自表 6/11）；不排除撰写时误计，但 252 不可达无法 100% 排除曾有不同的索引集。listener 集成测试跑的是夹具镜像触发器（非生产触发器），逐列一致性归 autoRouteListenerSchema 注释锚定机制管。

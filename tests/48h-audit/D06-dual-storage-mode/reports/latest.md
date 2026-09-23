# R56 · D06 双模式存储 · 48h 审计结论

> 时间：2026-09-23 · 窗口 2de612429..80c74af01 · 执行：主代理+D06 子代理亲读复核

## 发现与处置
| 级别 | 项 | 处置 |
|---|---|---|
| P1 | 738 installer 五点同步缺口（新装必炸+TestStartupFilesAreAllEmbedded HEAD 红） | ✅ 补 embed+map+对账 map，测试转绿 |
| P2 | B8 findings 清理承诺未兑现（无 DELETE，每小时重复落发现无界增长）+ 窗口 24h vs hot 8h 自相矛盾 | ✅ pruneOldFindings + effectiveWindow clamp 到 lifecycle.hot_retention_hours |
| P2 | 738 列数守卫 WARNING 不 fail-closed | ✅ 改 EXCEPTION（735 先例）+ embeddata 重同步 |
| P3 | maas_resolve_rate_multiplier SQL 侧零运行时调用方+坏值 cast 未包 EXCEPTION（与 Go 侧全兜底不对称）+ 无 CI 钉住 | 登记 |
| P3 | db-changelog 缺 736/737/738 行 | ✅ 补登记 + B11 撞号登记（下一可用 740） |
| P3 | credits.go "数学等价"注释过强（逐档 CEIL 差 ≤4 credits） | ✅ 随 P2-3 口径对齐 + 注释改为实测语义 |

## 核实为健康
736/737 迁移质量（up/down 对称+幂等+分区父表加列级联）；B1 双侧同源逐条对应+单点盖章+无 DST 风险；B8 探测型只记 findings 无自动修复+单 run 上限+settle lag；lite/sqlite 模式 B8/D1 全部安全降级（nil pool no-op+Redis env 摘除）。

## 遗留
SQL 取档函数是否接线或删除 = 下轮裁决。

## R57 追加（2026-09-23）
| 级别 | 项 | 处置 |
|---|---|---|
| P1 | B7 virtual_ip 假名死分支（R56 §三.1 首项） | ✅ 迁移 740（view 链补投影真源 client_ip，110/111/115 fail-closed；dev 库 down/up 实跑）+ rollup/看板/内存路径切 client_ip 维度；详见轮文档 §一 |
| P1 | 738 六点缺口：Go 自愈组合体 113 列冻结体缺 credits/client_ip——带外删视图后自愈重建体令 rollup 每分钟空转（738 复发形态） | ✅ canonicalV2DDL 显式中层列 + 115/113 宽度门控；离线+live 契约测试双绿（ensure↔迁移逐字节等价）；frozenContractColumnList appended 名单 738 起实错一并修正 |

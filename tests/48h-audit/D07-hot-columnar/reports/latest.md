# R56 · D07 hot+分区 · 48h 审计结论

> 时间：2026-09-23 · 窗口 2de612429..80c74af01 · 执行：主代理+D06 子代理亲读复核

## 发现与处置
| 级别 | 项 | 处置 |
|---|---|---|
| P1 | 736 倍率列未进 promote 函数——8h 热窗一到批量转移把倍率证据落 NULL/DEFAULT 1.0（计费回放全回 1x，与 telemetry 契约注释直接矛盾） | ✅ 迁移 739（698 函数体程序化重建，三处列清单各补一列；up/down diff 复核；本机 dev 库 ON_ERROR_STOP 实跑+pg_proc 验证+schema_migrations 自登记；installer 五点同步） |

## 核实为健康
698 时区钉扎结构（显式列清单+单 CTE 原子 delete+insert+FOR UPDATE SKIP LOCKED）；737 小表不进分区族+启动期索引合理；738 逐 view 幂等守卫+链缺失 no-op+自顶向下 DROP 自底向上重建+114 列对账（守卫已 fail-closed）；embeddata 六文件与 canonical 逐字节一致（738/739 重同步后仍一致）。

## 遗留
R55-D2 多行 PEM（D07 辖区）已由本轮裁决 (b) 落地：解析器响亮拒绝+测试 4 改期望+约定落注释。

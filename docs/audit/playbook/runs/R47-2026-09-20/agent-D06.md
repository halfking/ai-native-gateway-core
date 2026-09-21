# D06 双重存储架构 子代理报告（窗口：45412e919..HEAD）

## 一、发现（候选）
| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| D06-1 | P3 | 部署/排障文档宣称 request_logs_days 无 worker，F6 后失真（deployment-guide:290/:317-319/:134-135/:143、troubleshooting:96、README:155-156）；LiteRetentionWorker.Start 无"已启动"日志，监控点位缺失 | docs/storage/*.md；bg/lite_retention_worker.go:52-68 | 五处文案更新 + Start 加启动日志 |
| D06-2 | P3 | "配置 0 显式跳过并 Warn"不成立：ApplyLiteDefaults 把 ≤0 钳为 7 → :163 恒真、Warn 死代码；行级清理无 opt-out | storage_mode_init.go:104,163-174；config/storage.go:209-211 | 接受 0=默认7d 语义，删失实半句 + 文档写明 |
| D06-3 | P3 | 删除-写入竞态（同 D05#2 缝隙 a：会话在、轮次全没 + body 孤儿） | bg/lite_retention_worker.go:81-112 | turns 删除用同一谓词复检或单事务 |
| D06-4 | P3 | 首轮收集无界（SELECT 无 LIMIT，升级后首跑可能全量载入；对照 ConsistencyWorker 有 MaxSessionsPerRun=500） | bg/lite_retention_worker.go:52-55,81-87 | 分页/上限截断 |

## 二、核实为健康的面
F6 闭环完整（config→接线→worker；时间单位两侧一致 Unix 秒；幂等+边界钉桩）；full 模式 worker 不启动；启动日志如实（retention_* 快照）；SQLiteDB() 契约文档化+nil 防护；admin 读面与删除解耦（lite 下 admin 本就 503 显式降级，204 处 db==nil 守卫）；retention 与 bodies 保留期闭合；Redis 收口/模式门控无回归。

## 四、R46 §五#8 存储演进批最小方案
1. cache_metrics TTL：stateTableTTLSpecs 追加一行（689 helper 正则 ^cache_metrics_YYYY_MM$ 直接适用；fallback 90d 对齐诊断类）——零迁移；
2. promote 积压 gauge：metrics.go 增 GaugeVec + 排水循环后逐表 COUNT（oldest-row-age 需每表 ts 列映射，二期）；
3. public.sessions 分区 drop：机制零成本但语义风险前置（DROP=删用户历史，helper 不支持"关闭"），先上 gauge 看水位再裁决 TTL+enabled 开关；
4. 父表三类行级 UPDATE 例外清单成文（纯文档）。

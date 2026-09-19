# D04 三层缓存 provenance 与压缩 子代理报告（窗口：45412e919..HEAD）

## 一、发现（候选）
| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | LiteRetentionWorker 清理微秒级 TOCTOU（SELECT→删 turns→删 sessions 非同一事务；复活会话丢历史/孤儿 turn 两缝隙，概率极低） | bg/lite_retention_worker.go:82-113 | 登记或包单事务/补扫孤儿 |
| 2 | P3 | 保留期不对称：行级 7d vs 文件体 30d——sessions 行删除后 body 文件最长 23 天孤儿，靠 BodiesTrimmer 兜底 | config/storage.go:206-210 | 可接受/文档化 |

## 二、核实为健康的面
- R46 修复接线纯增量，四键 provenance 链与 sanitizer offset 锁零触碰（domains/secretmask、hooks、errorsx 无窗口 diff）；SQLiteDB() 纯 getter；storage_mode_init 仅 lite 分支按 Trimmer 同款模式追加。
- LiteRetentionWorker 只删三表无误删；lite SQLite 仅 8 张表、无 cache/provenance 独立表；session_turns 无 FK 到 sessions，显式先删 turns 是唯一正确路径；foreign_keys=on 但仅有的级联链与 worker 无交集；钉桩测试在位。
- 压缩闭环组件全部不在窗口 76 文件改动面。

## 三、未覆盖项
运行时 PRAGMA/RunOnce 实跑；provenance 四键内部重审（改动面外）；大库单轮删除性能。

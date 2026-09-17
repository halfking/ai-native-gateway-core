# Lane E — 迁移安全审计（R37，2026-09-17）

## 一、发现（候选）
| # | 级别 | 发现 | 证据 |
|---|---|---|---|
| F1 | 高 | 716 down 半回滚（drop 6+1 仅重建 3） | 716_*.down.sql:5-10 vs :12/94/127 |
| F2 | 高 | db/migrations/ 孤儿通道与 startup 撞号异文件（353/354/365）；test-phase3-integration.sh:56 直接 apply 014 | db/migrations/352-365 |
| F3 | 中 | V 系列 36 文件仅 V371 接进 sequence 通道，无通道自动投递 | deploy/sql/deploy-252-complete.sh:22 |
| F4 | 中 | 713 ALTER TYPE 全分区重写：session_turns 级联全部月分区 ACCESS EXCLUSIVE，252 体量未验证 | 713:22-23,34-38 |
| F5 | 中 | V355 回填假分批（DO 块不可 COMMIT，单长事务） | V355:56-80 |
| F6 | 中 | 708 回填 DO 块单事务全分区 UPDATE，注释自称分批 | 708:103-126 |
| F7 | 中低 | 708 DROP sessions.last_full_* 三列不可逆数据销毁，生产未复核 | 708:237-243; 708.down:9-13 |
| F8 | 中低 | 同一变更双通道双定义（V369/V370 vs 202609_*） | sql/migrations/202609_01 vs deploy V369 |
| F9 | 低中 | run_migrations.sh 回滚链 \|\| true 静默吞错 | sql/run_migrations.sh:113-141 |
| F10 | 低 | 717 静默 NULL 化坏数据（已注释声明）；hot 整表重写 ACCESS EXCLUSIVE | 717:34-64 |
| F11 | 低 | 694 ensure GIN trgm 裸 CREATE INDEX（依赖守卫） | 694:122-129 |
| F12 | 信息 | 本机 sequence 账本 716 已记、717 待跑；schema_migrations max=999 | 活库查询 |

## 二、健康面
函数 clobber 守卫（intentional_function_chains 校验顺序）设计良好；sequence 通道 psql autocommit 无包事务、6xx-7xx 普遍幂等；456 把 CREATE INDEX 放事务外并书面标注锁风险；installer 五点同步 714-717 逐字节一致；down 抽查 707-717（除 716）对称可逆；危险操作扫描未见无界 DELETE/无关 TRUNCATE。

## 三、未覆盖
domain/ 93 文件仅模式扫描；local/manual/operations 子目录未审；Go ensure 与 SQL 体逐函数 diff 未做；embeddata 166 文件全量 diff 未做；252 体量外推未实测。

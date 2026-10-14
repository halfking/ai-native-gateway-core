# 2026-10-15 252 pg-252-pg17 SQL 日志审计轮（alias 链规模病根修轮 + 860 改名爆炸半径清偿）

> 窗口：2026-10-14 23:47 → 10-15 06:52（归档 ctrlog-20261015 + 活尾巴，6.3h）；
> 另取 10-14 归档 06:07 → 23:47 段（17.7h）作白天窗对照——两窗合计 24h，
> 与上一轮（10-14，窗口止于 10-14 06:07）零重叠。
> 取证：`252:/tmp/pg252-r1015/`（R26 extract.awk 锚定提取器 + 四段 records TSV）。
> 快照层 10-13/10-14/10-15 归档在位、逐小时推进（首查通过）。
> 主修复：**迁移 864**（model_aliases 裸列 `(raw_name, canonical_id) WHERE active`
> 部分索引——S3 波 3 alias 链的规模病），252 已按 736/862 先例预应用并双台账记账。
> 次修复：860 改名爆炸半径的三个旧名客户端全部归因并处置（245 僵尸 collector-service
> 停用、252 loadtest 监视脚本改指 canonical 视图、disk-watch 部署仓内新版）。

## 一、总量与三轴对账

| 轴 | 白天窗（10-14 06:07→23:47，17.7h） | 夜窗（10-14 23:47→06:52，6.3h） |
|---|---|---|
| 原始日志真实字节 | 318MB（10-14 归档全量） | 31MB 归档 + 18MB 活尾巴 |
| 锚定 ERROR+FATAL | （含在下方分族） | 825 ERROR + 192 FATAL |
| duration ≥1s 完成 | ~3,000（含 CMI 423） | 1,561（墙钟 10,300s，max 174.3s） |
| statement timeout | 1,387（其中 **CMI rollup 1,201**） | 50 |
| user cancel | 1,235（70/h） | 242（38/h） |

日志量基线对照：10-12 起归档 245-318MB/天（R26 基线 ~68MB/天），主因是
**860 改名后的旧名客户端错误风暴**（见 §三）+ smm 股票客户端升级；08h 前仅 2 条
loadtest 误报、13:15（860 应用时刻）起恒定 —— 风暴起点与改名分钟级吻合。

开轮宿主 load 7.2（上轮的孤儿 grep/MONITOR 清理效果保持）；无新增孤儿。

## 二、根修：model_aliases 裸列前导索引（迁移 864）

### 症状（纪律㉘ 精确形状计数）

`INSERT INTO credential_model_index_hot`（auto_index_refresher rollup，5min tick ×
3 实例对齐）≥1s 完成 571 条/24h（**mean 15-20s**，max 30.0s 贴击杀线），且
**statement-timeout 击杀 1,201 条/17.7h（68/h）**——占全窗 stmout 的 87%，
每击杀整批回滚，下一个 tick 大概率重演：每 ~5min 三实例各烧最多 30s。
对比上轮（10-14）「CMI_HOT rollup 368 mean 7.4s」：**两天内翻倍恶化**。

### 根因（EXPLAIN + EXPLAIN ANALYZE 实证，252 真库）

S3 波 3（10-13）给 rollup half-1 会话腿引入 alias 链：
`mo.canonical_id IN (SELECT ma.canonical_id FROM model_aliases ma
WHERE ma.raw_name = lower(t.model) AND ma.status='active')`。
该形状以**关联 SubPlan** 出现在 hash join 的 Join Filter 里，而 model_aliases
现有两条索引全部不服务：

- `idx_model_aliases_lower_raw_name_status (lower(raw_name), status)`——表达式
  索引，条件列是裸 raw_name，**表达式不匹配，永远不可用**；
- `uq_model_aliases_canonical_raw (canonical_id, raw_name)`——raw_name 是第二列，
  等值无前导 ⇒ 规划器只能**全索引扫**（EXPLAIN 实测 cost 61.57 / 2,744 条目/次）。

隔离测试证实与规划器病理无关：连 `WHERE raw_name = 'glm-5.3' AND status='active'`
裸常量查询都只能选 uq 全索引扫——纯粹是索引形状与查询形状不匹配。
EXPLAIN（修复前）：该 Join Filter 节点 cost 34,271，语句总 cost 58,065。

同一 SubPlan 形状按行数放大到同族读点（全仓 grep 6 处）：
credential_selfcheck（×234/17.7h）、探针资格三连（admin/routing.go:2689-2691，
×395/24h）、probe_history、taxonomy_sync。

### 改法与判据

**迁移 864**：`CREATE INDEX idx_model_aliases_raw_name_active
ON model_aliases (raw_name, canonical_id) WHERE status='active'`——裸列前导 +
覆盖输出列（Index Only）+ 谓词与全部消费方精确一致。不改任何查询语义
（保持 raw_name 大小写敏感等值）；lower(raw_name) 表达式索引留给真正按
lower 过滤的读方。2,744 行构建毫秒级；BEGIN 块内普通 CREATE INDEX（不加
CONCURRENTLY——事务内会直接报错），SET LOCAL 门照 862 纪律。

五点同步 + 通道腿齐：canonical SQL/.down/判据测试（7 钉，含 r1030 的
COMMENT 同守卫反向钉）、installer embeddata 副本 + go:embed 变量 + map +
dbinit StartupFiles、`apply-db-revision-sequence.sh` files 数组、TSV 重生成
（259 行）。门：`apply-db-revision-sequence_test.sh` rc=0、installer 全测
rc=0、startup 测试 ok、gofmt 净。**变异验证**：把索引键改成
`(lower(raw_name), canonical_id)`（复刻缺陷形状）→ 判据红；恢复 → 绿。
（判据自身曾红一次：ToUpper 后比对小写目标的笔误，当场修正。）

### 预应用与验证（252）

- 以 postgres superuser 预应用（sha256 e55e9413…bb9b4）+ 双台账记账
  （schema_migrations 864 行 + llm_gateway_migration_checksums，736/862 先例；
  **首次写入用了截断占位校验和，当场发现并以真实完整 sha256 重写**）。
  幂等重放零错误；boot 届时 IF NOT EXISTS 按名命中跳过。
- **计划翻转**：rollup INSERT 总 cost **58,065 → 1,251（46×）**；SubPlan 从
  全索引扫（61.58）变 Index Only Scan（0.28..2.50，loops=861 实测）。
  uq 索引继续服务 half-2 的 canonical_id 前导形态——分工正确。
- **真值计时**：`BEGIN; EXPLAIN ANALYZE <完整 INSERT 含 ON CONFLICT>; ROLLBACK`
  —— SELECT 体 58.7ms、含写阶段 80.2ms（Conflicting Tuples 0）。
- **生产收敛**：索引 06:33 落地后 06:45 tick 3.4s（缓存过渡），06:50 起
  无任何 ≥1s 的 CMI 条目 = 已低于 1s 慢日志阈值；06:45 后 CMI 击杀 0。
  （06:35-06:41 的 14-27s 条目为过渡期实测，如实记录。）

### 遗留（同族登记不修）

- credential_model_index_2026_09 列存分区（301k 行，TTL 7 天已过期 6 周）——
  refreshIndexSQL/latest_bucket 族的独立拖累，R22 已裁决自然缓解 = **11-02
  day-2 DROP**；本轮量化其现存代价（latest_bucket ×260/24h、mean 8.5s）后
  维持原裁决不提前动（owner 域）。
- `sessionRequestStatusRemainingSQL`（释放闸门 gauge，窗口-less 双臂 count
  JOIN v1）：×382/17.7h + sum 1,548s + 偶发击杀（06:48 实录）。R31 已裁决
  「窗口-less 是退役释放闸门的诚实性要求」，维持设计；停写后 v1 冻结使其
  趋向常数重算，随 v1 族 DROP 自然消失。

## 三、860 改名（10-14 13:15）的爆炸半径：三个旧名客户端，全部归因+处置

860/861/862 已于 10-14 13:15:31-32、863 于 19:10:28 应用到共享库
（schema_migrations 实证；`request_logs`→`_request_logs`、
`request_logs_hot`→`_request_logs_hot`，视图名不变按 OID 跟随）。
改名后三类客户端打在旧名上，起报时刻与应用时刻分钟级吻合：

| # | 客户端 | 错误 | 节奏 | 处置 |
|---|---|---|---|---|
| 1 | **245 collector-service**（systemd，二进制 7-19 构建、8-19 启动已跑 56 天；DSN 直指共享 llm_gateway） | `INSERT INTO provider_metrics_minute … FROM request_logs` 42P01（PG 日志 char 1652） | 60/h 恒定，×1,050+ | **停用**（`systemctl disable --now`；优雅退出 context canceled、零进程、不复活）。其全部职能（分钟+小时聚合，strings 证实仅写 provider_metrics_minute/hour 两表）已由网关内建 quality collector 接管（cmd/gateway/main.go:7698，默认启用、同 1m/1h 间隔、视图化源——生产成功写入实测在案） |
| 2 | **252 /opt/scripts/monitor_loadtest.sh**（root crontab */5，loadtest 流量哨兵） | `SELECT count(*) FROM request_logs … 'loadtest-%'` 42P01（char 22） | 12-14/h | **改指 canonical 视图** `request_logs_with_current_month`（停写后新流量只在会话族；client_model 翻转后视图行 100% 填充实测 1,567/1,567；5min 窗走 862 索引亚秒级）。部署+实测静默 rc=0；**仓内新增 SSOT 副本** scripts/252-monitor/monitor_loadtest.sh（此前仅存在于 252 裸机） |
| 3 | **252 /opt/scripts/pg17-disk-watch.sh:191**（*/10，154 OOM 事故同源脚本） | `pg_total_relation_size('public.request_logs_hot')` 42P01 | 6/h | 仓内版（570b6d026 已改 `_request_logs_hot`）部署到 252（部署副本停在 10-11，与仓版 diff 恰为改名五处，备份 .bak 后覆盖）；量测可达性实测（_request_logs_hot 现 58MB——冻结族已被 promote 排空） |

处置后验证：06:43 起 PG 日志 `does not exist` 零新增（覆盖 loadtest 06:45/06:50
两轮 cron 与 disk-watch 06:50 tick）。

**教训（判据纪律第 10 条候选）：改名类迁移的爆炸半径不在新代码，在「不在这个
仓库里的旧客户端」**——裸机 crontab 脚本、独立 systemd 单元、其他 checkout 谱系
的构建物。860 改了 92 个运维脚本却没有发现 monitor_loadtest.sh（它根本不在仓里）。
后续改名/DROP 轮的清单品：`ssh 各主机 crontab -l` + `systemctl list-units` 逐个
对照旧名 grep，并把裸机脚本收编进仓（本轮已收编一个）。

**附**：252-dev 网关自 10-06 01:01 failed(timeout) 且 disabled——fact 152/172
仍把它列为「旧名嫌疑/唯一残存源」的表述作废，本轮活抓与 systemd 双证排除。
quality-service（245，同 7-19 构建）**仍在服役且健康**（190 模型评分/5min，
近 20MB 错误样本零 SQL 错误），不处置；重建建议留 owner。

## 四、已知族对账（上轮遗留逐条）

| 项 | 上轮遗留 | 本轮实测 | 裁决 |
|---|---|---|---|
| **862 效果**（fact 173：应塌缩） | 视图族 7,480/30h；cancel 4,104/stmout 1,552/30h | 视图族 ~52/h + ~42/h（**~5×改善**）；cancel 70/h（day）；stmout 78/h 中 87% 是 CMI（§二根修后归零）；requestStatus 释放闸门从 ×166 mean 18.3s 跌出榜单（×34 max 2s） | **862 生效确认**；残余大头即 §二 |
| `analyze_llm_gateway_table_stats('2')` 568s | 是否常态 | ×17/17.7h（≈1/h 设计节奏）、max 568.8s < 10min SET LOCAL 预算 | **常态（预算内）**，维持 R48 裁决 |
| session_client_stats REFRESH 124s | 是否常态 | ×27/24h、87-180s、**180.1s 贴 180s pin（1 次被击杀）**；源=session_summaries 级聚合 | 常态但**贴预算线**；列观察项（源收窄属 owner） |
| ra7d REFRESH 恶化（移交并行轨） | 65.2s avg（10-13） | ×130/24h、max 79.5s、sum 3,231s | 维持移交；量级未再恶化 |
| smm daily_kline（外部） | 第 4 次登记 | **第 5 次**：MAX(date) ×221 + ALTER ×19/24h（~10/h），同窗 562s 墙钟 | 外部，催办升级 |
| E6 pocket `$4`（外部） | ~11/h | ×69/6.3h = 11/h 不变 | 外部，催办升级 |
| WITH win（ursm_node_snapshot_min 小时统计） | 新形状 | ×21+4、max 174.3s，并行会话分析负载（与 §10.110.x DROP 留存审计文档同期） | 并行轨负载，登记 |
| `WITH deduped`（usage_ledger 整月 DISTINCT ON） | 新形状 | ×6、max 538.7s、admin 使用面触发（非循环） | 登记量级 |
| 负成本检查 `recorded_cost_is_negative` | — | ×159/24h、18s avg：v1 冻结后为**常数重算**；且新记账进 usage_ledger，该检查对新增负成本**已致盲**（usage_ledger 无姊妹检查） | P2 设计项登记：补 usage_ledger 臂或改读源属 owner 裁决 |
| UTF8 `0xe6 0x9f 0x27` ×6 | 新形状 | 01:41 一簇 30s 内，无 STATEMENT 行（续行块），之后再无 | 外部瞬时（截断的多字节字符），登记 |
| parallel worker 终止 ×63 | — | statement_timeout 击杀的级联（正常形态） | 销案 |
| promote 批次（bodies ×27 max 50.1s / turns ×12 max 11.4s） | 已知 | 量级持平 | 维持 |
| 245 quality-service.error.log 692MB | — | 07-19 至今积累、近两日仍 12.2 万行（INFO 搅拌为主）、宿主盘 79% | 登记：需 logrotate，不擅自清 |

## 五、本轮动作清单（全部完成）

1. 迁移 864（四文件 + installer 两文件 + 通道脚本 + TSV）——判据 7 钉全绿 + 变异红。
2. 252 预应用 864 + 双台账记账 + 幂等复验 + 计划/真值/生产三重验证。
3. 245：collector-service `disable --now`（860 旧名错误风暴 60/h 源头终止）。
4. 252：monitor_loadtest.sh 改指 canonical 视图（部署+实测）；pg17-disk-watch.sh
   部署仓内新版（备份后覆盖）；monitor_loadtest.sh 收编进仓 SSOT。
5. 归因更正：252-dev 从「旧名嫌疑」名单移除（10-06 已死）；quality-service 健康在役。

## 六、下轮对账清单

1. 864 随 154/245 部署生效后：CMI rollup 应彻底从慢查榜消失（本机已 <1s）；
   credential_selfcheck / 探针资格两族应同步塌缩（同 SubPlan 根因）。
2. 11-02 day-2 tick：credential_model_index_2026_09 + handoff_logs_2026_09
   到期 DROP（R21 裁决的自然缓解点）——验证 latest_bucket/refreshIndexSQL
   两族是否随之消失；若 11-02 未触发，按 fact 136 复核 day-gate。
3. `does not exist` 错误族应恒零（三个旧名客户端已清）；若再现，按 §三
   教训继续扫「仓外客户端」。
4. session_client_stats REFRESH 贴 180s pin 的击杀频率（观察项）。
5. 负成本检查的 usage_ledger 臂缺口（owner 裁决后另立轮）。
6. 245 磁盘 79% + quality-service.error.log 692MB 的 logrotate（ops）。

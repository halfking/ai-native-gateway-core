# T5 横向不变量/安全/卫生 子代理报告（窗口：2026-09-24T05:00..2026-09-25T05:00, HEAD 9635b9b17）

> R65 轮只读子代理原文留档。窗口 diff = 15eb0f834..9635b9b17（270 files, +32634/−783）。主代理复核结论见轮文档。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2（一致性债） | **746 迁移未登记进 `scripts/apply-db-revision-sequence.sh`**。窗口内 743/744/745/800 均登记，唯独 746 缺席——经 sequence 通道升级的存量库在"只跑脚本、未启动新二进制"窗口期缺三列。缓解已核实：boot ensure 与新消费者同二进制且 ApplyMigrations 先于 worker 启动，无运行时断裂；但违反五点同步纪律（本仓多次前科） | scripts/apply-db-revision-sequence.sh:555-592；db/db.go:1013-1025 | 补登 746（ALTER 可重入，零风险）【已修】 |
| 2 | P3（健壮性） | **手写 xlsx writer 无 1,048,576 行上限守卫**：超限不报错，产出 Excel 打开即"损坏"的文件。需单 sheet >1M 分组行才触发，属潜伏面 | domains/reportrollup/xlsx.go:181-183；workbook.go:43-114/159-243；admin/report_rollup.go:79-81 | 加行数 cap【已修：buildWorkbookBytes 显式报错】 |
| 3 | P3（红线偏离裁决） | **report_snapshots 零 retention**：全仓无 TTL/trimmer/archiveSpec 挂接，单表无分区。偏离"大表=hot+分区"红线属设计文档声明过的有意演进。量级：中型部署 2k-10k 行/日 → 1-3.6M 行/年，写入面只有 T+1 对昨日单键 upsert。**结论：现在安全；真正缺口是 retention 无限期** | bg/report_rollup_worker.go:98-102；rollup.go:581-627；设计文档 §2.2 | 登记"reports TTL / 分区演进"待办【登记】 |
| 4 | P3（死代码+统计失真） | **daily_total 折叠行是死写**：与 queryTotalDay 四键完全相同，先后 upsert 后者必然覆盖前者——注释声称"对帐参照"不成立，RowsWritten 虚高 1/轮 | rollup.go:132、161-166、171-173 | 删死写 + 修注释【已修】 |
| 5 | P3（死列+注释漂移） | **canonical_id 恒为 nil**：全包无写入路径；SSOT/745 头注描述未实现的 canonical 粒度 | rollup.go:625；sql/objects/tables/report_snapshots.sql:48；745 头注 | 修注释为终态口径【已修：745 头注勘误⑤】 |
| 6 | P3（ensure↔迁移漂移） | **ensure 漏 746 的三条 COMMENT ON COLUMN**：仅走 boot ensure 的库列注释缺失 | db/db.go:962-1042；746:34-39 | ensure 补 COMMENT（幂等）【已修】 |
| 7 | P3（门控耦合未声明） | **report_rollup_worker 隐式继承 `!IsCredRecoveryDisabled()` 门控**：设 LLM_GATEWAY_CRED_RECOVERY_DISABLED=true 会静默停掉对账日报；gate-swap 系 2026-09-10 有意设计非本窗口引入 | cmd/gateway/main.go:3767、5321、5371 | 注释披露；中期挪出条件块【已修注释；挪出登记】 |
| 8 | P3（前端小缺陷） | **qualityWidths 与表头动态列不同源**（Tenants/Providers 独有 kind 时列宽错位）；`_ = withProvider` 死参数 | workbook.go:172-186、246-259 | 宽度按 kinds 同源；删死参数【已修】 |
| 9 | P3（读面小不一致） | **export 缺 degraded 分支 + /run 500 透传内部错误细节**（可含 SQL/约束信息） | admin/report_rollup.go:152-154、178-183、225 | export 补 degraded；/run 去细节【已修】 |

## 二、核实为健康的面

1. **迁移编号对账全绿**：74x = 743/744/745/746，80x = 800，与 db-changelog 及账本一致；732/741 为声明性跳号；主仓与 installer 副本 cmp 逐字节一致；744 缺席 installer 为受守卫的有意通道切分（CONCURRENTLY 不可进单事务 + Go ensure 镜像）；744 重编号后无旧 743 残留引用；四份 down 均有理由声明。
2. **ensure 链**：ensureReportSnapshots 新建分支与 SSOT 逐列一致；存量分支 columnsAllPresent 守卫；注册位次在 worker 启动前；账本戳幂等；lite 下 PG 不初始化，ensure 天然不跑。
3. **D6 双存储**：report worker 与 mockprobe runner 在 lite 下均安全——前者被 dbConn != nil 大块跳过；后者自带无 DB 降级（MaxConns=2、停机先排空）。
4. **D14 安全横向**：/api/admin/report-rollup/ 与 tuning 两端点均 superAdmin；settings hour TypeInt Min0/Max23 双重钳制；前端零 v-html/innerHTML；RangeFilter 全参数化。
5. **溢出/边界**：PG 侧 SUM(bigint)→numeric→::bigint 干净；xlsx XML 转义面收窄正确。
6. **TCP/网络可靠性**：ollama-native 本窗口未落地新执行器/HTTP client（不出网）；窗口内新增生产 HTTP client 仅 mockprobe client（超时/Body.Close/LimitReader/强钳 127.0.0.1 全就位）与 admin doMessagesProbe（同款 10s + defer close）。

## 三、未覆盖项与原因

1. taskprofile/autoroute v2 聚合算法细节（归 T4）。
2. 745/746 真库 E2E 复跑（禁连库，标"待复核"）。
3. gateway-v2 × lite 组合（非现网部署形态）。
4. 8b94131d9 golden 测试断言强度评估（已有专项审计记录）。

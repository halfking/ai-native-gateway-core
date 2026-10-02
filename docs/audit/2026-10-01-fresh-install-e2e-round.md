# fresh-install e2e 轮（2026-10-01）——installer dbinit 全序列转绿 + 环境巡检

> 关联：docs/handoff/20260930-fresh-install-chain-review.md（遗留风险 1 出处）、
> docs/audit/2026-10-01-802-form-adjudication.md（判据出处）。
> 起点：origin/main ≥ dee77825b（worktree 隔离模式，主工作区被 §5.9 并行会话占用中）。

## 一、结论

1. **遗留风险 1 关闭**：`TestFreshInstallerSessionTurnsHotBootstrap`（`-tags=integration`，
   `TEST_INSTALLER_FRESH_DB_URL` 门控）在一次性 scratch 库上完整应用
   00-prereqs / 01-schema / 02-seed + 全部 StartupFiles 并断言终态，本机 PG17
   两轮全绿。09-30 时该门停在 622（42P01），733+802 链在全新安装上从未落地过。
2. **733+802 链断言入 harness**：indisvalid + attached 计数判据（裁决 §2.3，
   勿信 pg_indexes 文本）、叶子有效、部分谓词、801 无损 drain 指纹
   （v_deleted+v_inserted——注意 733 头注的 DO UPDATE 描述已被 801 取代，
   live 终版是 DO NOTHING + 先插后删的两阶段 drain）、ON CONFLICT 目标、
   parent↔hot 列 parity、promote 可调用、734 视图链计划期验证。
3. **fresh 终态 = 生产逐位对齐**（实证）：session_turns / _hot = 104/104 列、
   视图 55 列、session_bodies 分区全 heap、session_turn_details 2 分区且
   802 索引递归健康。
4. **待办 B（环境巡检）零命中**：未发现任何未入档环境执行过 802 式回填
   DDL（判据=802 索引 indisvalid=false + attached=0）。252 llm_gateway
   healthy（heal 复核 ✓）且全库零无效父索引；本机 23 个网关血统候选库全扫；
   154/245 无独立 PG（既档）。

## 二、根因（fresh 链此前必炸的四层结构）

1. **基线缺口（主体）**：01-schema 于 ~477 dump，但不是任何单一血统的忠实
   快照——candidate_failure_logs hot 表（392）、session_turns_hot（526）、
   session_dim（350）、request_logs hot/parent 29 列差、视图链中间层
   （577/610）等散落缺口，使序列在 622/682/683/686/690/696/700/707/713 等
   处 42P01/42703。共接线 17 个自守卫历史迁移（392/471/484/485/487/491/
   510/526→bootstrap 替代/532/535/542/543/573/577/603/610/617/640）。
2. **历史迁移不可整文件注册类**（523/350）：其视图/函数手术是 2026-07 形态，
   CREATE OR REPLACE VIEW 缩列被 PG 拒绝、350 重写 update_session_summary()
   撞 572/563/661 clobber guard → 只能按 562/617 先例做**列集/表集收编**
   （803/804/805），不动其视图函数。
3. **回填 DDL 环境烙印**（686/542）：手工按特定环境当时形态落盘的 DDL——
   686 的 `::regclass` 使"absent"分支不可达；542 的 ON ONLY + 硬编码
   2026_09/2026_10 分区叶索引在分区按月滚动的世界里对 fresh 永远不成立。
   处方即 802 裁决同款：to_regclass() / 递归形态。台账 SHA 已同步。
4. **harness 自身缺陷**：e2e 的 apply helper 无条件 `--single-transaction`，
   没镜像 runner 的 `dbinit:no-transaction` 分流（718 的
   DROP INDEX CONCURRENTLY 必炸）——已修（dbinit.RequiresNoTransaction 导出
   + helper 分流）。

## 三、改动（commit c8beb6290）

- StartupFiles 接线 17 迁移 + embeddata 五点同步（embeddata/var/map/清单/parity）。
- 新迁移 4 个（canonical + embeddata + 台账行）：803（cfl hot 列）、
  804（cmb context_window 列）、805（session_dim 全形）、806（bodies
  columnar 分区转 heap，仅空分区动手）。
- 真库实证修订 2 个（台账 SHA 更新）：686、542。
- session_turns_hot_bootstrap 上移至 706 之前（707 §2 与 hot 锁步扩列 +
  bootstrap 42 列 parity 校验要求 baseline 形 parent）；R51 的 733/734
  顺序约束不变；TestStartupFilesIncludeRequestJourneyOutboxPrerequisites
  钉子随迁。
- e2e 断言面：终态列数钉 live 值（104/104/55）；新增 733+802 链健康组。
- docs/db-changelog.md：686/542 行更新 + 803-806 新行。

## 四、测试

- `go test ./...`（installer 模块）ok；`go test ./sql/schema/` ok；root 构建通过。
- e2e 两轮全绿（rebase 到 dee77825b 后复跑确认，scratch 库即建即焚）。

## 五、遗留与待拍板（本轮新增发现，未动作）

1. **本机 live 的 4 个无效父索引**（planner 忽略父层、叶子均 valid、分区
   剪枝路径可用）：`idx_request_logs_discard_events_ts`（attached=5）、
   `idx_request_logs_ts_desc`（attached=2；718 本要 DROP 的冗余索引）、
   `session_bodies_request_id_partition_date_key` /
   `session_bodies_session_id_turn_no_partition_date_key`（attached=3）。
   根因证据：本地台账 **718/719/726/727 从未应用**（schema_migrations 无行）。
   建议：对本机跑一次 apply-db-revision-sequence.sh（幂等回填，顺带刷新
   802 台账旧指纹 + 应用 803-806 no-op），待拍板。
2. **session_turns_with_current_month 的 security_invoker 丢失**：526-era
   bootstrap 创建时带 WITH (security_invoker=true)，713 就地重建时丢掉——
   live 现状 reloptions 为空。625/627 有同款加固先例，候选 807 收口，待拍板。
3. **itgate_fresh_85583**：本机遗留的 fresh 血统 scratch 库（802 索引健康），
   无台账、无人认领，可删。
4. **sync_20260718 v2/v3/v4 / llm_gateway_test / gw_integ** 等克隆库存在
   大量 ON ONLY 无效父索引——系 01-schema dump 自身烙印（attached=0），
   非回填 DDL 证据；克隆库非活跃环境，仅登记：若任一克隆将来转正，
   部署前须按 802 裁决 §2.3 逐索引 heal。

## 六、本轮自审（四错）

1. 初判"523 可注册"即接线即跑——被 523:98 缩列错误打脸；教训同 09-30
   「一次全绿不能证伪工具」：**历史迁移注册前必须先比对其视图/函数形态与
   baseline 的新旧**（CREATE OR REPLACE VIEW 只能加尾列）。
2. 两次 awk 基线列集提取范围错误（WITH/`);` 边界吞全文）导致假差集，
   差点据此给 526 开错药；改用 CREATE TABLE 行号定位 + 双向 comm 后闭环。
   教训：**列集对账必须双向、且边界以块尾特征行复核**。
3. 801 断言依据 733 头注写（DO UPDATE），e2e 红后查 live 函数体才知
   801 终版是 DO NOTHING + 两阶段 drain——**复述上游注释≠实测终态**。
4. runner.go 编辑一次因 gofmt 过期被拦、一次手敲对齐侥幸命中——均靠
   `gofmt -l` 复核兜住；同文件多轮编辑必须重读再改。

# Handoff：会话存储解耦 v3 —— session_turn_details 特征层（2026-09-20，2026-09-21 审计修正）

> 状态：**已实施 + 批判式审计修正 + 测试全绿 + 已提交推送**
> （deploy-local.sh 2.5.6.2158，VERIFY_PASS=1；审计修正随 e5ee9fa2e / 21bb48da4 入库）
> 方案文档：docs/storage/2026-09-20-session-storage-decoupling-plan.md
> 前序审计：session_turns 替代 request_logs 盘点（11 视图依赖/~50 缺列/双写/mirror 3.71%）

## 一、本次交付

### 迁移（PG17 Full 链路，**终编 733/734**；两度撞号：原编 727/728 撞 R46/R48 的 727-730 → 731/732 再撞 origin 731_auto_route，推前 pull 实测捕获后终编）
- `sql/migrations/startup/733_session_turn_details.sql`(+down)：`session_turn_details`
  特征层表族（月分区+hot+RLS 4 策略+promote 707 镜像形态+request_logs 批量回填）
- `sql/migrations/startup/734_request_logs_view_details_join.sql`(+down)：canonical 视图
  session 分支 LEFT JOIN details，30 个 NULL 占位 → 真实特征列
- `sql/migrations/startup/710_...down.sql`：42P16 修正（见 §二-4）
- `scripts/apply-db-revision-sequence.sh`：登记 733/734（**六点同步**——db.Open 只跑
  Go ensure 链不扫 SQL 文件，不登记则 245/154 存量库永不应用）

### Go（Full 链路写入/视图）
- `domains/session/v2/details_writer.go`（新）：DetailsRecord + UpsertDetailsInTx
  （与 turn+bodies 同事务，ON CONFLICT DO UPDATE 幂等，表族缺席 no-op）
- `domains/session/v2/session_writer_v2.go`：ProcessedRequest.Details 字段 +
  SetDetailsWriter + Write 中 turn 后 upsert
- `internal/sessionv2mirror/s1b_fields.go`(+test)（新）：30 列映射 = 26 有源 + 4 缺源登记
  （virtual_ip/virtual_mac/key_alias/owner_user）
- `internal/sessionv2mirror/hook.go`：entryToProcessedRequest 挂 applyStorageS1BFields
- `db/request_logs_view_schema.go`：投影 overlay（`detailsProjectionColumns` 30 列位置
  覆盖）+ `canonicalV2DDL(baseHasFP, baseHasRaw, hasDetails)` 三态 + ensure 探测
  details 家族在场决定形态（缺席回退 710 形态，零 d.* 引用）
- `db/view_schema_v2_contract_test.go`：指向 734 文件；live 重放 733→710→734；
  down 链 734.down→733.down→710.down 分步断言；新增 overlay/回退双形态断言
- `cmd/gateway/session_v2_init.go`：probeSessionTurnDetails + SetDetailsWriter 接线
- `bg/partition_manager.go`(+test)、`admin/data_lifecycle_hot_partition.go`(+test)：
  promote 调度 + 手动入口登记（五点同步，测试强制）

### Lite 链路（SQLite+files+memory，与 Full 严格区分）
- `storage/sqlite/schema.go`：`session_turn_details` 表 + `session_logs_view` 拼装视图
  （LEFT JOIN，与 PG17 canonical 734 语义对齐）
- `storage/sqlite/turns_store.go`：WriteTurnDetails（UPSERT，零值 NULL）
- `storage/{types,interfaces}.go`：TurnDetails 结构 + TurnDetailsWriter 可选接口
- `cmd/gateway/lite_telemetry_sink.go`：journal 追加 details 写（模型/凭据/状态/
  延迟/成本/请求分类/质量/附件）；request_logs 写入挂 S4 门控
  `storage.request_logs_write_enabled`（默认 true）

## 二、2026-09-21 批判式审计：四个实测缺陷 + 修正

> 审计方法：文档逐条对码 + 全量重读迁移 SQL/Go diff + live 契约测试真跑 + 本地 PG
> 缺口量化。前轮 handoff 的「测试全绿（offline+live 重放可跑）」**不符实**——live
> 测试从未跑绿，接连暴露下列缺陷。

1. **[P1] 回填漏热表**：733 首版候选行只扫 `session_turns` 父表，漏 `session_turns_hot`。
   热表里尚未 promote 的 turns 由旧二进制写入、永不再被 writer 覆盖，缺口随 promote
   单调累积。**实测：宣称「增量 ~226 行」，真实存量缺口 30,041 行（15.9% 新近 turns）**。
   修正：候选/ensure 双循环改 `session_turns(hot ∪ parent)`；本地复放 8.5s 插入 30,062
   行全量闭合。245/154/252 尚未应用 733，将在部署时拿到修正版。
2. **[P0-部署阻断] 回填硬引用冻结契约外附加列**：储备/多维组 24 列
   （node_switch_count 等）在 canonical 冻结 113 列契约之外，陈旧库/交集克隆库的
   request_logs 可能没有——live 契约测试实测 42703 炸停。修正：24 列按
   information_schema 交集动态回退 `NULL::<type>`；30 个视图契约列保持硬引用
   （冻结契约成员必有，测试克隆实证）。
3. **[P0-回滚阻断] 734.down 探针盲区**：「已是 v2 体」守卫只 `LIKE '%session_turns%'`，
   734 details-joined 体同样含该串 → 被误判为 710 体跳过重建，733 down DROP 表族时
   canonical 残留依赖 **2BP01**。修正：加 `NOT LIKE '%session_turn_details%'`；
   契约测试 down 链改为按号逆序分步断言。
4. **[P2-回滚阻断] 710.down 42P16**：`CREATE OR REPLACE VIEW` 不能变更列类型
   （会话体 agent_name text → v1 体 varchar(255)），canonical 处于会话体时该 down
   自 710 落地起就不可用（此前被上游失败掩盖）。修正：先 DROP 再 CREATE 同事务替换。
5. **卫生项**：重编号残留同步（734 文件头/RAISE EXCEPTION/promote COMMENT/DB 视图
   COMMENT 与 Go canonicalV2Comment 同文/约 20 处 Go 注释）；733 头注释"default 兜底"
   与实现矛盾修正；方案文档「28 列」实为 30 列；733.down 头 2BP10 笔误；`*.bak`
   备份清除；`run/`、`raw-logs/` 入 .gitignore。

## 三、验证证据（本地 Docker PG llm-gateway-pg / 8782 蓝绿）

- **离线**：`go build ./...`、`go vet`（涉及包）全绿；db 契约（TestViewV2ProjectionContractSync
  + TestNumericUpMigrationVersionsAreUnique）、session/v2、sessionv2mirror、storage/sqlite、
  admin、bg 测试 ok；gofmt 干净（本次改动集内）。
- **live 等价**（TestRequestLogsViewV2EnsureMatchesMigration，真实目录克隆 scratch）：
  ensure ≡ 734 重放 viewdef 全等，details_join=true；down 链 734→733→710 首次全通。
- **live 缺口量化**：30,041 →（静态修正版复放 8.5s，+30,062 行）→ 9 →（动态版幂等复放，
  按需补增量 237 行）→ 残余 ~24 行 = 在途事务 + 零特征 turns（s1b 全零跳写的设计语义，
  视图 LEFT NULL 与 710 占位等价）。
- **writer 活性**：60min 窗口 details_hot 836 行全带 client_model；promote 后台调度
  10min 扫 249 行入月分区；canonical 视图非空 client_model 502,412 行；
  分区 2026_09/2026_10 预建、无 default。
- 部署：2156→2157→2158 三次蓝绿 /health=200（注意：2158 部署的是**修正前**二进制，
  245/154 首次部署将直接携带全部修正）。

## 四、遗留/下一步（按方案 §5 退役门）

1. **S3**：11 个 request_logs 视图改读 canonical（现已带 details 列）——尤其
   v_node_switch_analysis/v_timeout_effectiveness/v_continuation_effectiveness
   （源列已在 details 储备组建好：node_switch_count/timeout_mode/
   effective_timeout_seconds/is_continuation/cached_response_id/context_size_tokens）
2. **S4 前提**：details 储备组 entry 无源（零匹配）——需 streaming 管道补采或接受 NULL；
   mirror outbox 清零 + dual_read 7 日零漂移
3. **S4**：`storage.request_logs_write_enabled=false`（Lite 已挂同门）
4. **缺源 4 列**：virtual_ip/virtual_mac/key_alias/owner_user 需 telemetry entry
   补字段或 identity enrichment 管道接线
5. **部署窗口残差**：蓝绿切换点前后秒级流量由旧二进制写入、无 details 行——
   与本轮修复的存量缺口同源但量级为秒级；可在 S4 前用 733 回填段幂等复跑兜底
   （或后续补一个 admin 手动回填入口）
6. **并行会话 WIP**：worktree 内另有 reqprobe 特性未提交改动（admin/handler.go、
   cmd/gateway/main.go、streaming executors、internal/reqprobe/），属另一会话，
   未纳入本次提交，提交前需与其协调。

## 五、坑速记（详见方案 §7）

- date+interval 是 timestamp（::date 强转）；EXECUTE format 内嵌 interval 需 ::interval
- 不建 default 分区（与月分区互斥）；promote/回填先 ensure 批次月份
- pg_get_viewdef 省 schema 前缀（LIKE 探测别带 public.）
- 六点同步：新迁移必须登记 apply-db-revision-sequence.sh；五点同步：bg 调度 +
  admin 映射 + 两侧测试
- **回填候选必须含热表**（双层表族的「存量」定义 = hot ∪ parent）
- **契约外附加列必须 information_schema 交集**，硬引用 = 部署通道定时炸弹
- **down 链三盲区**：体形探针要排除新体形；OR VIEW 不能变列类型；down 测试按号逆序
- 部署 root：./deploy-local.sh --root <worktree>（默认 root 是 ~/kaixuan/llm-gateway-go）

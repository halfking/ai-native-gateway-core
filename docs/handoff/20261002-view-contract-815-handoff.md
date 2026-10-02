# handoff：会话存储解耦 v3 —— §9.27 视图契约 815 + §9.28 补位列逐列裁决

> **提交落点（须知）**：本轮 17 个文件的内容落在 commit `7a6356ef0`，而该 commit
> 的标题是并行会话的 `docs(audit): ursm_node_snapshot_min 容量审计`。原因是提交
> 那一刻并行会话执行了 `git commit`，把共享索引里**我已暂存的文件一并带走**。
> 内容逐文件核对完整（17/17），但提交信息不对应本轮工作。
>
> 处置：**不 amend、不 rebase** —— 那会改写并行会话的提交并可能丢它的 message。
> 改为在此登记落点。检索本轮改动请按文件路径（`815_request_logs_view_stage_band_cff*`、
> `db/request_logs_view_padded_columns*`、`admin/physical_predicate_on_view_source_test.go`）
> 或按本文件标题找，不要按 commit 标题找。

> 审计正文见 [`2026-10-02-view-contract-815-and-padded-column-verdicts.md`](2026-10-02-view-contract-815-and-padded-column-verdicts.md)
> （主文档 `2026-09-30-session-request-data-re-audit.md` 本轮正被并行会话编辑，
> 故本轮章节单独成文，章节号从 §9.27 起顺延。）

## 结论

三问的答复：

| 决策 | 答复 | 落地 |
|---|---|---|
| 1) 4 个投影 | **补 3 个**：`origin_stage` / `token_band` / `client_forwarded_for`。`trace_events` 不补（镜像从不写它，投影即净数据损失）。**`id` 不补 —— 你的判断经实测确认，且更硬：1,515,984 组同 request_id 配对里 `r.id = t.id` 命中 0 次** | migration 815 + Go 镜像体，已在真库实跑（up→118 列 / down→115 列 / up→118 列） |
| 2) 28 列逐列裁决 | **前提被推翻**：需要裁决的是 **6 列**，不是 32 列。§9.21 的 30/28 是从 **710 形态**数的，现网是 **734 形态**，其中 24 列已由 `session_turn_details` 供值（覆盖 99.9996%） | 逐列裁决落成 `db/request_logs_view_padded_columns.go` + 4 道门；S4 读面 **39 → 14** |
| 3) `raw_model_name` + 恢复 SQL 端口 | **(c) 已由并行线 §9.23 落地**（`9b8424fd8`），核对确认覆盖。(a)/(b) 的**前提被真库推翻**：`raw_model_name` 在 `request_logs` / `session_turns` / `session_turns_hot` **三张表里非空行数都是 0** —— v1 侧也没有这个字段，端口的真正前置是「新增一个有正确来源的字段」 | 门控无需再做；端口重定义为 S4 灰度前的独立工作项 |

两个附带的、你没问但会改变判断的发现：

1. **§9.20.3 的「硬前提」理由是错的**（前提本身已满足）。补 `origin_stage` 之后物理谓词
   在视图上**不再 42703**（两臂都解析得了）。真正的新风险是它变成「能跑但漏掉探测
   排除」：`quality_flags` 在 session 分臂可空 ⇒ `NOT COALESCE('probe'=ANY(NULL),FALSE)`
   = TRUE ⇒ 探测流量被当业务用量。⇒ 补了一道新禁令门 `TestNoPhysicalPredicateOnViewSource`。
2. **`admin/request_logs_stop_write_classification_test.go` 的 `sessionArmNullPaddedColumns`
   是一处承重缺陷**：它钉在 710 的 `$proj$` 上，而 734 早已把其中 24 列换成 details
   真实值。该门拿错误源与错误派生式互校 ⇒ **一直绿着**。已改为从 `db` 包生效投影派生、
   权威源换成 815。

## 改动文件（逐文件点名，只含本轮）

新增：
- `sql/migrations/startup/815_request_logs_view_stage_band_cff.sql` / `.down.sql`
- `installer/cmd/llm-gw-installer/embeddata/startup/815_*.sql`（×2，双树同步副本）
- `sql/migrations/startup/migration_815_test.go`（4 道静态门）
- `db/request_logs_view_padded_columns.go` / `_test.go`（逐列裁决 SSOT + 3 道门）
- `admin/physical_predicate_on_view_source_test.go`（2 道门）
- `docs/audit/2026-10-02-view-contract-815-and-padded-column-verdicts.md`（本轮审计正文）
- `docs/handoff/20261002-view-contract-815-handoff.md`（本文件）

修改：
- `db/request_logs_view_schema.go`
- `db/view_schema_v2_contract_test.go`
- `admin/view_source_columns_contract.go`
- `admin/view_source_column_contract_test.go`
- `admin/credential_monitor_heatmap_probe_predicate_test.go`
- `admin/request_logs_stop_write_classification_test.go`
- `admin/v1_direct_padded_column_reader_test.go`

**未提交、不属于本轮**（并行会话在途，勿代提）：
`bg/credential_selfcheck.go`、`bg/credential_selfcheck_pick_test.go`、
`bg/credential_selfcheck_s4_guard_test.go`（其 §9.26）、`admin/proxy_test.go`、
`admin/routing_*`、`credentialfpslot/*`、`deploy/sql/schemas/baseline/01-schema.sql`、
`docs/audit/2026-09-30-session-request-data-re-audit.md`（其 §9.26，168 行）、
`docs/audit/2026-10-02-round44-ssot-decision.md` 等。

## 测试

```
go build ./...                                   # OK
go test ./... -count=1                           # 全绿（见交付报告）
cd sql/migrations/startup && go test ./ -run TestMigration815 -v   # 4/4 PASS
TEST_PG_DSN=... go test ./db/ -run TestRequestLogsViewV2EnsureMatchesMigration  # PASS
TEST_PG_URL=... go test ./admin/ -tags integration \
  -run 'TestPhysicalOnlyColumnsListMatchesRealDatabase|TestCredentialHeatmapSQL_ExecutesOnRealDatabase'  # PASS
```

真库集成门的两条关键输出：
- 热图在 118 列视图上执行成功，返回 21（默认排除自检）/ 45（含自检）条凭据序列；
- `physicalOnlyRequestLogColumns` 校验：**表里 39 列，live schema 也是 39 列** ——
  815 把 3 列移进视图、清单同步移出 3 列，差集闭合。

真库数据实证（815 装上后，7 天窗口）：
`band` 分布 = NULL 577,718 / below 40,207 / forced 5,952 / preliminary 3,383 ——
**装之前这条查询 42703、该格为空**。

## 遗留风险

1. **`trace_events` 要镜像先写**才能投影。在那之前读方必须继续读物理表。
2. **`session_turns.client_ip` 是错名副本**（= `client_forwarded_for`，
   202,014/202,014；与 v1 `client_ip` 相同 0 行）。视图保持 NULL 补位是对的，
   但底表那个列名是个坑——将来「顺手把 740 的 client_ip 补上」会直接命中它。
   底表的修法（改名/补真源/删列）需单独决策。
3. **14 个 `id` 读方**要逐个改读法（`id` 证明不可投影）。本轮只做重分类，未动代码。
4. **`raw_model_name` 端口**：定义已改写（新增有正确来源的字段，回填范围 = 切换时刻的
   36h lookback 窗口，不是全量历史），未排期。
5. **v1-only 历史回填**（S4 判据第 3 条）未动。
6. **跨字面量运行时拼接的越列仍无静态门**：`TestNoPhysicalPredicateOnViewSource`
   按「文件是否声明视图源」判定，判不出运行时拼接后的真实源；那道形状仍只能靠
   `TestCredentialHeatmapSQL_ExecutesOnRealDatabase` 兜。
7. **`deploy/sql/schemas/baseline/01-schema.sql` 早已落后**（里面是纯 v1 dump，
   连 710 会话体都没有）。本轮未动。

## S4 退出判据（替换 §9.21.5）

1. **写入面**：v1 写路径全部并入门控，稳定期 ≥ 一个 hot retention（8h）。
2. **读面**：**14 个**真补位读方逐个改视图读法（全部卡在 `id`，只能改读法）；
   另 25 个不受补位阻塞的读方按需抽样核对 details 缺行（真库 6 行 parent / 0 行 hot）。
3. **历史面**：`assertTaskInTenant` 的两条 v1 腿 ⇒ v1-only 历史先回填进 session 族。

划掉一条：`cmd/gateway/dual_read_validator.go` 读的 `request_type` 现由 details 层
供值（99.9996%），停写后两侧仍可比 ⇒ 不再需要纳入判据。

## 下一轮提示词

```
继续 llm-gateway-go 会话存储解耦 v3（__DEV_HOME__/workspace/ai-native-tools/
syncfield/llm-gateway-go-4，分支 main）。

基线：§9.27/§9.28 已落地（migration 815 三列投影 + 补位列逐列裁决 6 列 +
S4 读面 39→14 重分类），审计正文在
docs/audit/2026-10-02-view-contract-815-and-padded-column-verdicts.md（主文档
2026-09-30-session-request-data-re-audit.md 被并行会话占用 §9.26，合并前不要追加）。

四条可执行工作项，按依赖排序：

1) 14 个 `id` 读方逐个改读法（审计 §9.28.4 列了清单）。`id` 已证明不可投影
   （0/1,515,984），所以只能去掉对 id 的依赖或改用 request_id 回查。逐个改时
   注意：改指视图的当天才是危险日（今天读物理 v1 一切正常）。
2) `trace_events` 让镜像写入（internal/sessionv2mirror 侧）+ 覆盖率验证，
   之后才谈投影。判据沿用 815 的做法：先量近 1/7/30 天非空率，再看 both_differ
   是否为 0。
3) `session_turns.client_ip` 错名副本的处置（改名 / 补真源 / 删列）——在有人
   「顺手把 740 的 client_ip 补上」之前定下来。
4) `raw_model_name` 端口：先定义字段来源（绑定解析出的上游原始名，不是
   `session_turns.model`——它等于 client_model），再谈写路径与 36h 窗口回填。

纪律沿用：结构性事实读真库，名字不是定义；扫描器输出是嫌疑清单不是结论，每条手验
（读方还是写方？绑到哪张表？是不是同一个东西？）；模糊匹配不作数；守卫必须变异验证
且确认变异是断言命中而非崩溃；「门可以持续有效地校验一个错误的前提」（§9.28.2），
所以每道门都要问「权威源钉在哪个形态上」；并行检出下工作区出现的东西 ≠ 我改的
东西，提交逐文件点名。

交付仍按：结论/根因、改动文件与关键行为、测试命令与结果、遗留风险、handoff、
下一轮提示词。
```

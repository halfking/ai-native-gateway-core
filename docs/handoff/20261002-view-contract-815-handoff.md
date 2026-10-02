# handoff：会话存储解耦 v3 —— §9.27 视图契约 815 + §9.29 补位列逐列裁决

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
>
> **章节号对照（同日修订）**：本轮初稿用过 §9.28–§9.31，与并行会话提交 `5b6e3ea03`
> （主文档 §9.28「会话族是两个存储面」）撞号，已整体顺延为 **§9.29 / §9.30 /
> §9.31 / §9.32**。本文件占用 §9.27 与 §9.29–§9.32；主文档侧的 §9.22–§9.26 与
> §9.28 属并行会话。**本文件里出现的 §9.2x 一律指审计正文那份文件。**

## 结论

三问的答复：

| 决策 | 答复 | 落地 |
|---|---|---|
| 1) 4 个投影 | **补 3 个**：`origin_stage` / `token_band` / `client_forwarded_for`。`trace_events` 不补（镜像从不写它，投影即净数据损失）。**`id` 不补 —— 你的判断经实测确认，且更硬：1,515,984 组同 request_id 配对里 `r.id = t.id` 命中 0 次** | migration 815 + Go 镜像体；活库终态 118 列，**幂等性由 scratch 库内 `up→down→up` 门证明（可重跑，不动活库）** |
| 2) 28 列逐列裁决 | **前提被推翻**：需要裁决的是 **6 列**，不是 32 列。§9.21 的 30/28 是从 **710 形态**数的，现网是 **734 形态**，其中 27 列已由 `session_turn_details` 供值（覆盖 99.9996%） | 逐列裁决落成 `db/request_logs_view_padded_columns.go` + 4 道门；S4 读面 **39 → 14 → 1**（第二次纠错见下） |
| 3) `raw_model_name` + 恢复 SQL 端口 | **(c) 已由并行线 §9.23 落地**（`9b8424fd8`），核对确认覆盖。(a)/(b) 的**前提被真库推翻**：`raw_model_name` 在 `request_logs` / `session_turns` / `session_turns_hot` **三张表里非空行数都是 0** —— v1 侧也没有这个字段，端口的真正前置是「新增一个有正确来源的字段」 | 门控无需再做；端口重定义为 S4 灰度前的独立工作项 |

两个附带的、你没问但会改变判断的发现：

1. **§9.20.3 的「硬前提」理由是错的**（前提本身已满足）。补 `origin_stage` 之后物理谓词
   在视图上**不再 42703**（两臂都解析得了）。真正的新风险是它变成「能跑但漏掉探测
   排除」：`quality_flags` 在 session 分臂可空 ⇒ `NOT COALESCE('probe'=ANY(NULL),FALSE)`
   = TRUE ⇒ 探测流量被当业务用量。⇒ 补了一道新禁令门 `TestNoPhysicalPredicateOnViewSource`。
1b. **登记表从 14 收敛到 1，第二次纠错是门自己的错。**
   `paddedColumnsReadFromV1Direct` 判「读了某补位列」时只问「这个词在字面量里出现过吗」，
   **不判它绑到哪张表** ⇒ `p.provider_id`（绑 `providers` 表）被算成读 v1 的补位列。
   改成按限定符归属判定后 39 → 14 → **1**（`cmd/compression-bench/main.go`，一个 bench
   工具）。另 2 处「多关系字面量里的裸列」不可归属，已手验并登记为带失效自检的具名豁免。
   ⇒ **S4 判据第 2 条的分母是 1，不是 14。**

2. **`admin/request_logs_stop_write_classification_test.go` 的 `sessionArmNullPaddedColumns`
   是一处承重缺陷**：它钉在 710 的 `$proj$` 上，而 734 早已把其中 24 列换成 details
   真实值。该门拿错误源与错误派生式互校 ⇒ **一直绿着**。已改为从 `db` 包生效投影派生、
   权威源换成 815。

## 改动文件（逐文件点名，只含本轮）

新增：
- `sql/migrations/startup/815_request_logs_view_stage_band_cff.sql` / `.down.sql`
- `installer/cmd/llm-gw-installer/embeddata/startup/815_*.sql`（×2，双树同步副本）
- `sql/migrations/startup/migration_815_test.go`（5 道静态门，含「门非空转」一道）
- `db/request_logs_view_padded_columns.go` / `_test.go`（逐列裁决 SSOT + 4 道门，含「门非空转」一道）
- `admin/physical_predicate_on_view_source_test.go`（2 道门）
- `docs/audit/2026-10-02-view-contract-815-and-padded-column-verdicts.md`（本轮审计正文）
- `docs/handoff/20261002-view-contract-815-handoff.md`（本文件）

修改：
- `db/request_logs_view_schema.go`
- `db/view_schema_v2_contract_test.go` —— 前段：登记表驱动的 815 双向语义断言；**末段
  （最后一轮加）**：在 down 链里跑 `up → down → up`，三条断言（re-up 逐字节回同一份 /
  三列仍有值 / 第二次 down 与第一次逐字节相同）
- `admin/view_source_columns_contract.go`
- `admin/view_source_column_contract_test.go`
- `admin/credential_monitor_heatmap_probe_predicate_test.go`
- `admin/request_logs_stop_write_classification_test.go`
- `admin/v1_direct_padded_column_reader_test.go`
- `815_*.down.sql`（正本 + installer 副本，**最后一轮**改掉一处引用了「710/740 惯例」
  却与 740.down 实际行为相反的注释，并补上「down 后朴素重跑 up 会主键冲突回滚」的操作后果）

**未提交、不属于本轮**（并行会话在途，勿代提）：
`bg/credential_selfcheck.go`、`bg/credential_selfcheck_pick_test.go`、
`bg/credential_selfcheck_s4_guard_test.go`（其 §9.26）、`admin/proxy_test.go`、
`admin/routing_*`、`credentialfpslot/*`、`deploy/sql/schemas/baseline/01-schema.sql`、
`docs/audit/2026-09-30-session-request-data-re-audit.md`（其 §9.26，168 行）、
`docs/audit/2026-10-02-round44-ssot-decision.md` 等。

## 测试

```
go build ./...                                   # OK
go test ./db/ -count=1                            # ok（0.24s）
go test ./admin/ -count=1                         # ok（69.1s）
cd sql/migrations/startup && go test ./ -run TestMigration815 -count=1 -v   # 5/5 PASS
TEST_PG_DSN=... go test ./db/ -run TestRequestLogsViewV2EnsureMatchesMigration  # PASS
TEST_PG_URL=... go test ./admin/ -tags integration \
  -run 'TestPhysicalOnlyColumnsListMatchesRealDatabase|TestCredentialHeatmapSQL_ExecutesOnRealDatabase'  # PASS
```

> **一次「admin 整包 FAIL」不要直接当结论。** 本轮曾出现一次 `FAIL
> github.com/kaixuan/llm-gateway-go/admin 68.9s`，重跑却是 `ok 69.1s`、零 FAIL 行。
> 判别证据：失败那次运行期间并行会话往 `admin/` 新增了两个**未跟踪**测试文件
> （`session_family_two_surface_test.go` / `zz_tmp_crosstab_test.go`），
> 正好落在编译窗口内。⇒ 在共享工作区里，「跑出来红」与「代码是红的」是两件事；
> 报告红灯前先重跑一次并同时看 `git status --porcelain -- <该包>`。

本轮补的两道「门非空转」断言（**不需要重跑任何变异就能复核**，详见审计正文 §9.27.8b）：

- `db.TestPaddedVerdictGatesAreNotVacuous` — PASS，运行时自证结构：
  `补位 6 列 [id test_col test_tab_indent provider_model credits_rate_multiplier client_ip]、
  裁决 6 条、否决投影 1 条、同名不同物 2 条`。它拦的是「输入被清空/截断 ⇒ 否定式门
  平凡通过」，与「改坏会红」是两件事——前者不能由变异报告替代。
- `sql/migrations/startup.TestMigration815GatesAreNotVacuous` — PASS。断言 `$proj$`
  非空、`names` 恰 118 列、迁移 ≥2000 字节（防截断让所有 `Contains` 判据失效）、
  `id` 仍为 `NULL::bigint`、down ≥1000 字节且带半剥离拒绝、两个 installer 副本都在。

真库往返（本轮最强的一条证据，审计正文 §9.27.9 展开）：

1. `TestRequestLogsViewV2EnsureMatchesMigration`（`TEST_PG_DSN` 门控，本轮**实跑 PASS
   1.05s / 整包 6.25s**，离线时同命令 0.24s —— 差值即真库门确实在跑、没 skip）在
   独立 scratch 库 `llmgw_view_v2_contract_test` 上重放 `733 → 710 → 734 → 738 →
   740 → 815`，要求 Go 自愈体与迁移产物 **viewdef 逐字节相同**，并逐级验证 down 链
   （815 → 740 → 738 → 734 → 733 → 710）。**活库全程只读**，scratch 库自动删净。
2. **815 幂等已从「一次性手跑」升级为可重跑的门**（原先只有一次动活库的
   118 → 115 → 118，不可复核）：同一个 scratch 库里跑 `up → down → up`，要求 re-up
   后 viewdef **逐字节**等于 down 前那一份、三列**仍有值**（不只是列名回来），并且
   第二次 down 的 viewdef 与第一次逐字节相同（顺带钉住 down 的确定性）。两处新断言
   各做过一次变异验证，红都落在 `view_schema_v2_contract_test.go:671` / `:695`，
   均为 `t.Fatalf` 断言命中、零 panic。
3. 活库终态（`TEST_PG_DSN` 下复核）：canonical 视图列数 = 118、`trace_events` 不在、
   `schema_migrations` 里 815 登记 1 行。`id` 在顶层 viewdef 的**三条 UNION ALL 臂**
   上各有一种写法（按 `pg_get_viewdef` 行号实测）：

   | 行 | 臂 | `id` |
   |---|---|---|
   | L1 | `session_turns_hot`（会话 hot） | `NULL::bigint`（补位） |
   | L146 | `session_turns`（会话 cold） | `NULL::bigint`（补位） |
   | L291 | `request_logs_hot`（v1，别名 `rl`） | `rl.id` — **透传真值** |

   ⇒ **`id` 永不投影只对会话侧成立**；v1-only 的行必须带着真实 `request_logs.id`
   出去，否则那批历史行会在视图里失去主键。补位**源自 710**（Go 侧
   `db/request_logs_view_schema.go:362` `projectionExprsV2` 第 1 列 = `"NULL::bigint"`，
   别名由 `canonicalColumnOrderV2` 首列名 `id` 拼出），815 原样继承、未改动。

   值层门按这个契约重写为三条臂各自正确的期望：会话 hot 臂（`req-dual`）与会话 cold
   臂（`req-turn-parent`）必须 NULL；v1 臂（`req-v1-only`）必须**等于源表真值
   900001**（只断言非空太弱——turn id 也非空）。夹具同步补上显式 `id`，因为
   `cloneTablesFrozenDDL` 只发「列名 + 类型」，不带 NOT NULL 也不带 DEFAULT
   （生产形态是 `NOT NULL` + `nextval`）⇒ 旧夹具下 v1 行恒为 NULL，**旧门在 v1 臂上
   恒真**。变异验证：把期望改成 900002 ⇒ 命中 `:680`，消息里带出实际值 900001、
   零 panic ⇒ 门读到的是真值且可红。



> **订正一条我自己写错的数字**：本文件上一版把上面这件事写成「`id` 仍是
> `NULL::bigint AS id,`（**全库唯一 1 处命中**）」。那个 1 数的是 **815 迁移文件里的
> 命中数**，却被当成活库终态写了出去——**证据面张冠李戴**（同一个字面量在文件里
> 1 处、在活库 viewdef 里 2 处，因为活库那层是 `UNION ALL`，两条臂各一处）。
> 现按面分别写清。教训与「名字对得上不等于同一个东西」同族：这里中招的是
> **数字对得上不等于量的是同一个面**。

> **顺带发现并修掉一处「注释引用的先例与先例的实际行为相反」**：815.down 写着
> 「schema_migrations 行按 append-only 惯例保留（**710/740 惯例**）」，而
> 740.down 末行恰恰是 `DELETE FROM public.schema_migrations WHERE version='740'`。
> 数了多数派：**710 / 734 / 738 / 815 保留，只有 740 删**（4 比 1），所以 815 的
> *行为*是对的、*引用的先例*是错的，已改写注释。**操作后果不是理论**：该表是
> `PRIMARY KEY(version)`，而 up 文件末尾 INSERT 自己那行 ⇒ down 之后**朴素重跑 up
> 会在那个 INSERT 处主键冲突、整笔事务回滚，视图停在 115 列**。响亮地失败可以接受，
> 但「回滚后再前滚」必须先 `DELETE FROM schema_migrations WHERE version='815'` ——
> 那个顺序现在被上面第 2 条的门钉住了。

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
3. **1 个 `id` 读方**（`cmd/compression-bench/main.go`）要改读法。本轮只做重分类，未动代码。
   若后续新增 v1 直读读方，先确认它在登记表里——现在这张表只有 1 行，新增即报红。
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
2. **读面**：**1 个**真补位读方改视图读法（`cmd/compression-bench/main.go`，卡在 `id`，
   不可投影只能改读法）；另 38 个不受补位阻塞的读方按需抽样核对 details 缺行
   （真库 6 行 parent / 0 行 hot）。
3. **历史面**：`assertTaskInTenant` 的两条 v1 腿 ⇒ v1-only 历史先回填进 session 族。

划掉一条：`cmd/gateway/dual_read_validator.go` 读的 `request_type` 现由 details 层
供值（99.9996%），停写后两侧仍可比 ⇒ 不再需要纳入判据。

## 下一轮提示词

```
继续 llm-gateway-go 会话存储解耦 v3（/Users/xutaohuang/workspace/ai-native-tools/
syncfield/llm-gateway-go-4，分支 main）。

基线：§9.27/§9.29 已落地（migration 815 三列投影 + 补位列逐列裁决 6 列 +
S4 读面 39→14 重分类），审计正文在
docs/audit/2026-10-02-view-contract-815-and-padded-column-verdicts.md（主文档
2026-09-30-session-request-data-re-audit.md 被并行会话占用 §9.26，合并前不要追加）。

四条可执行工作项，按依赖排序：

1) `cmd/compression-bench/main.go` 的 `id` 读法改造（审计 §9.29.4 的唯一一条）。
   `id` 已证明不可投影（0/1,515,984 配对命中 0），所以只能去掉对 id 的依赖或改用
   request_id 回查。注意：改指视图的当天才是危险日（今天读物理 v1 一切正常）。
2) `trace_events` 让镜像写入（internal/sessionv2mirror 侧）+ 覆盖率验证，
   之后才谈投影。判据沿用 815 的做法：先量近 1/7/30 天非空率，再看 both_differ
   是否为 0。
3) `session_turns.client_ip` 错名副本的处置（改名 / 补真源 / 删列）——在有人
   「顺手把 740 的 client_ip 补上」之前定下来。
4) `raw_model_name` 端口：先定义字段来源（绑定解析出的上游原始名，不是
   `session_turns.model`——它等于 client_model），再谈写路径与 36h 窗口回填。

纪律沿用：结构性事实读真库，名字不是定义；扫描器输出是嫌疑清单不是结论，每条手验
（读方还是写方？绑到哪张表？是不是同一个东西？）；模糊匹配不作数；守卫必须变异验证
且确认变异是断言命中而非崩溃；「门可以持续有效地校验一个错误的前提」（§9.29.2），
所以每道门都要问「权威源钉在哪个形态上」；**「列名出现过」不等于「列被这一张表读」
——判归属要看限定符，能力早就在 API 里，别浪费**（§9.29.6）；跨多轮的审计文档里
每个分母都要能现场复算（§9.29.1 的恒等式表）；并行检出下工作区出现的东西 ≠ 我改的
东西，提交逐文件点名，且**提交前确认索引里没有别人的文件**。

交付仍按：结论/根因、改动文件与关键行为、测试命令与结果、遗留风险、handoff、
下一轮提示词。
```

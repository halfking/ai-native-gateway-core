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

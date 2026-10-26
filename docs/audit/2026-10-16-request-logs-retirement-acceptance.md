# request_logs 双写退役：需求、实施与验收记录（2026-10-16）

## 范围与完成判据

本轮基线为 `81ffd0f31`。目标不是仅执行改名迁移，而是让新请求在关闭 v1 写门后完整持久化到 session 表族，管理端可查询、回放和追踪；旧数据保留可读且遗留表使用 `_request_logs*` 待删除名称。本轮不删除历史数据，不以同名兼容视图掩盖未迁移依赖，不修改生产数据库。

用户授权从 154/245 只读取得所需 provider 凭据并注入独立本地环境；仅导入指定模型所需最小配置、在本地重新加密，不复制生产库或账号。入口 key、数据库密码、加密密钥不得进入文档、日志输出或提交。

完成判据：

1. 全部新增/更新业务写入和 trace flush 遵守同一 v1 停写开关。维护性 DELETE、历史分区清理单独分类，不能混称业务写入。
2. 关闭 v1 前确认 `sessions_v2.enabled` 和 `sessions_v2.shadow_write` 有效且正文写入具备来源；开关状态为空不能当作 false。
3. 新 session 数据在列表、详情、正文、决策回放、trace、附件及分析 API 可读；旧历史兜底单独测试。缺失记录与已存在但数据尚未到齐区分。
4. 物化视图实际定义、依赖与自愈 fast-path 都指向规范数据源；迁移 marker 不能替代物理契约检查。
5. 保留历史迁移字节；新修复或 runner 执行适配记录来源哈希并可重放。新库冷启动、改名后升级和第二次执行均需通过。
6. 指定入口与模型的真实多轮请求有完整请求/响应及 session 持久化证据；模型不支持或上游失败逐项记录，不能由 mock 替代。
7. 逐页和逐 API 验证形成明确清单。页面打开超过 2 秒，定位其网络/SQL原因，复测优化结果；空库快响应不算性能验收。
8. 最终 SHA 绑定独立审查，测试未运行及失败均注明，不把局部门通过当整体完成。

## 本地环境与安全边界

- 独立 PG17 容器 `llm-gateway-pg`，PostgreSQL 17.10，监听 `127.0.0.1:54318`，具备 vector / Citus columnar。
- 独立 Redis 容器 `llm-gateway-redis`，监听 `127.0.0.1:6379`。
- 通过 `scripts/deploy-local.sh` 部署；最终 active 端口、release、readiness 必须现场读取。本轮目标入口为 `http://127.0.0.1:8782/v1`。
- 隔离测试及变异快照位于 `.audit-validation/`，不得作为含秘密目录提交。
- 已实测 unset 不可达代理后 Go1.27.2 构建通过；旧交接文档中的工具缺失不作为当前结论。

## 验证矩阵与实施顺序

|阶段|对象|方法|必须保留的证据|
|---|---|---|---|
|A|迁移与冷启动|真实 PG17 执行 revision runner，二次重放，校验旧字节与ledger|退出码、最终执行节点、表/列/函数/视图目录与来源哈希|
|B|写入停写|telemetry、manual promote、automatic lifecycle、trace flush；开/关/运行中关门|实际 SQL 与 v1 行数/内容不变；session新增；Redis清理条件|
|C|读源|列表/详情/正文/回放/trace/附件/分析|新数据、旧数据、不存在、授权失败、hot/cold重复优先级|
|D|MV|八张 MV 目录及 routing source fast-path|实际 SQL；旧源拒绝/canonical接受；分别破坏两门的变异失败|
|E|数据积累|数百至数千条 mock 请求，再指定真实模型多轮|mock和real标记分离、请求ID、session与正文计数、失败分类|
|F|性能|页面网络及真实参数 SQL 的 EXPLAIN ANALYZE BUFFERS|冷/热耗时、规划与执行时间、行数/分区/索引、优化前后对照|
|G|集成收尾|构建、聚焦测试、全门及最终独立审查|精确 reviewed SHA；已知负向闸门与新增失败分开；部署版本|

真实模型逐个测试：`glm-5.3-flash`、`minimax-m2.7`、`deepseek-v4-flash`、`mimo-v2.6-flash`、`grok-4.6`、`claude-sonnet-5-5`、`gpt-5.6-luna`。提示词包括 `ping`、`say OK.` 及用户提供的中文操作/审计/磁盘分析文本；这些只作为模型输入，不自动执行模型返回的命令或删除文件。

HTTP 验收应以路由注册和前端实际请求两侧联合盘点，记录方法、路径、参数、角色、状态码、业务断言和耗时；单纯统计路由名称不是覆盖率。修改型 API 使用本地隔离夹具并检查副作用，不把 401/404 或空响应冒称成功。

## 已确认缺陷及当前证据

- manual hot promote 缺运行中关门复查：已补两入口、批次复查与 pending 快照。聚焦重复测试、race 和五类隔离变异已执行通过/准确变红。
- routing MV 第一 fast-path 漏 canonical 来源谓词：已补与第二门一致的检查。隔离 PG17.11 旧源/规范源矩阵及两类变异已执行。
- outcome freshness 硬编码 v1 热表：已改为与 settle worker 共享来源选择，并有实际 SQL 测试。
- 决策回放仅 v1 双腿：已修（session hot/cold 优先，details 按 tenant/request/date 完整键关联，legacy 历史回退）。真库 10 子例通过、5 行为变异准确红。
- trace flush 无门更新 v1、PG trace读取仅v1：已修。**根因更正**：原有停写门，但关门时直接删 Redis 导致丢数据；现为 session hot→cold 优先写、仅 gate 开才回退 legacy，且 rows>0 且规范化成功才删 Redis，零行/取消/失败保留重试。
- revision runner 冷启动依赖与860后旧名重放失败：已修执行期封闭token适配、803/683b/392/861排序、696/700视图尾列、708零修复行锁与811派生父名映射；源SHA/执行SHA/适配版本分别记账。完整本地序列和二次重放RC0，222项源哈希一致，重放零投递、ledger不变；9项行为夹具及3类隔离变异已执行。真实完整成功针对本地已改名库，旧名路径为真实PG对象/transport夹具，不冒称pristine全库验收。

## HTTP 与凭据验证补登

- 泳道初始化补方法限制、空库503、2秒查询deadline及租户过滤；扫描失败不再静默丢行。
- inspector stats 修复重复 WHERE、歧义 tenant/status 列和未按租户关联；非超级管理员使用认证租户，不接受 query 参数覆盖；辅助统计错误不再吞成零。
- 会话 list/detail/update 公开入口限制 GET/GET/PATCH；详情 `pgx.ErrNoRows` 正确返回404。
- `TestSessionRetirementHTTPRealDB` 在PG17.10唯一隔离schema执行：tenant_admin/user/super_admin泳道与统计、非空列表/新请求详情、缺失和跨租户404、PATCH持久化、跨租户更新拒绝、错误方法405、统计SQL失效500均通过。这是handler真库夹具，不是实际网关/浏览器验收。
- 最小凭据导入：3 provider、3 credential、6 provider_model与binding、3 endpoint、3 nonstream能力记录。六模型绑定JOIN无孤儿，存储后3/3解密成功，密文由本地key重新加密，生产key不能解密；重复导入在写入前拒绝。`mimo-v2.6-flash`生产来源为permanently_exhausted，未伪造状态或导入；一条stream能力因本地CHECK不支持明确省略。尚未完成模型HTTP请求。

## 本轮实跑证据（2026-10-16 下午，均为本地复核）
### 工作区事故与恢复

并发会话在 13:38 覆盖了本任务的未提交工作树（HEAD 被推进到 `62a3d5127`，本任务的 10 个新文件全部消失）。改动由另一会话保全在分支 `wip/request-logs-retirement-20261016`（`24e05543d`）。本任务随后在**独立 worktree** `llm-gateway-go-retire`（分支 `retire/request-logs-20261016`，基于 `origin/main`）以 `cherry-pick -n` 恢复，仅 `web/public/menu-config.json` 冲突并按上游取值解决；构建产物（`VERSION`、`version.json`、`web/public/version.json`）与不可信的 `scripts/audit-revision-leg-ordering.py` 已剔除出提交集。**教训：本仓多会话共享 checkout，本类长任务必须一开始就用独立 worktree。**

### 合并上游后的门

- `CGO_ENABLED=0 go build ./...` → RC0。
- `LLM_REVISION_RUNTIME_LIVE=1 bash scripts/apply-db-revision-sequence_test.sh` → `apply-db-revision-sequence contract passed`，RC0。
- 完整 `bash scripts/apply-db-revision-sequence.sh` → RC0；上游新增 866/867/868 于 15:42 应用成功，旧腿标记仍带 `request-logs-physical-v1` 适配，新腿无新增适配。`867` 的 `idx_session_summaries_last_request_recent (last_request_at DESC, session_key DESC) WHERE last_request_at IS NOT NULL` 已在库里。

### 真实模型七连测（入口 `http://127.0.0.1:8782/v1`，提示词 `ping`）

| 模型 | HTTP | 判定（本地 catalog 实测，非推测） |
|---|---|---|
| deepseek-v4-flash | 200 | 绑定存在且 `is_routable=true` |
| claude-sonnet-5-5 | 200 | 同上 |
| gpt-5.6-luna | 200 | 同上 |
| grok-4.6 | 200 | 同上（耗时 53.3s） |
| glm-5.3-flash | 503 | `model_offers` 中**无该名字**（只有 `z-ai/glm-5.3-flash` 与 `glm-5.3`），`model_not_found` 是候选为空的兼容码 |
| minimax-m2.7 | 503 | 绑定 `minimaxai/minimax-m2.7` 存在，但 `node_probe_state.last_err_code=http_410`、连续失败 5 次、`next_retry_at=16:03:52`，即凭据被判定 EOL 冷却中 |
| mimo-v2.6-flash | 400 `invalid_model` | 来源 `permanently_exhausted`，无 ready/ok 绑定，未伪造 |

三个失败均非路由缺陷。`503 model_not_found` 不能反推上游不存在：该码由候选耗尽出口复用，真实 kind 需看 `gateway_debug.tried`。

### 落库完整性（自我更正一条错误结论）

先前一份只读报告称「grok/gpt 成功请求缺输入正文」，**该结论错误**，源于只查 `session_bodies_hot`。实测：

- 真实客户端请求落在**月分区** `session_turns_2026_10` / `session_bodies_2026_10`（229 行），`*_hot`（264 行）是另一批流量；retention 晋级（默认 8 小时窗）解释两者时间轴不重叠（父表 03:57–06:49，热表 06:56–15:49）。
- 三个 200 请求在 `session_bodies` 中 `request_delta`/`response_delta`/`outbound_body` **三者全非空**。
- `session_bodies_hot` 262 行的两个 delta 列零 NULL。

### ★ 新发现缺陷：晋级静默丢弃 trace（已派修复）

`promote_session_turns_hot_to_partition` 的 INSERT 列清单为硬编码且**不含 `trace_events`**（函数体出现 0 次）。真库证据：`session_turns` 229 行 `count(trace_events)=0`，`session_turns_hot` 264 行全部非空。

对照：v1 的 `promote_request_logs_hot_to_partition` 硬编码 137 列**包含** `trace_events`；`promote_session_bodies_hot_to_partition` 从 `pg_attribute` 动态取列并带 hot/parent 形状漂移守卫，新增列不会丢。**只有 session turns 晋级路径会丢列。** v1 停写后 trace 只存在于 session 家族，故这是规模化丢失。

### trace flush 路径本身可用

新发一次真实请求 `5e85f4de4ac15534…`（deepseek-v4-flash，turn_no=1，success，200）落在 `session_turns_hot` 且 `trace_events` 非空 ⇒ flush 的热/父双分支（`internal/trace/trace.go:486-487`）在当前二进制上生效，部署二进制也确含两条 UPDATE 字面量。仍存在失败请求终态错标 `success`、`status_code` 被归一成 500 两处待修。

### 三个切换开关的真实口径

| 开关 | 本地值 | 判定 |
|---|---|---|
| `storage.request_logs_write_enabled` | `false` | 停写生效 |
| `storage.session_bodies_native_read` | `true` | 正文读源已原生 |
| `sessions_v2.enabled/shadow_write/primary_read/l3_read` | 全 `true` | 会话族为主 |
| `storage.admin_logs_native_turns_read` | **无记录 → 默认 false** | **不是缺口，是有意保持关闭**：`admin/logs_turns_source.go` 记录真库实测视图内 2,321,464 个 request_id 有 641,452 个（27.6%）在原生源查不到（探针/自检/in_progress/回环），按 request_id 反查与全量时间窗聚合**禁止**迁 session 原生源；该分类已固化为 `admin/session_view_dependency_risk_test.go`。`/api/logs` 因此继续经 canonical 视图，其 v1 冻结分支必须保留到 v1 表真正下线。 |

### 浏览器验收的当前阻塞

本地 `/api/system/bootstrap/status` 返回 `activated:false`，`web/src/router.ts:326-381` 的软 bootstrap 门把所有非 `/bootstrap` 路由重定向到 `/bootstrap?redirect=…`，重定向参数还会层层叠加，导致内置浏览器卡死在跳转链上（inspect 超时）。已在本地 `.env.local` 增加 `LLM_GATEWAY_LICENSE_ACTIVATED=1`（服务端 `licensing/bootstrap_api.go:94` 支持的开关，仅本地）、`LLM_GATEWAY_MOCK_PROBE_ENABLED=true`（当前本地未启用 mock 探针，`/mock/v1/*` 未注册，POST 落到 SPA 返回 200 HTML）、`LLM_GATEWAY_DB_MAX_CONNS=16`（此前为临时 export，需持久化否则默认池 32 会再次超过 PG `max_connections`）。三项均待下一次重部署生效。

### ★ 缺陷 A/B/C 的线上验收（release 2.5.8.2671）

重部署后发 20 个真实请求（4 模型 × 5 条样例提示词）与 800 个 mock 请求，用 `jsonb_typeof` 与 `trace_events->>'final_status'` 逐条回读：

| 缺陷 | 线上证据 | 结论 |
|---|---|---|
| A 失败 trace 终态错标 success | 4 个失败请求（503 quota_permanent / no_candidate）终态全为 `failed` | 已修复 |
| B 客户端实收状态被归一成 500 | 同批失败请求 `status_code` 仍为 **500**（客户端实收 503） | **未修复**：只覆盖 `captureAndEmitFailure` 类，`failAndMark` 类（约 15 个站点）仍留空，已退回补齐 |
| C 入站正文落 JSON null | 5 个 deepseek 成功请求的 `request_delta`/`response_delta` 的 `jsonb_typeof` 全为 `array`；mock 造数后热表 242 行 `array` | 已修复 |

**量具纪律**：缺陷 C 最初被我误判为"正文完整"，原因是用 `IS NOT NULL` 判断——JSON `null` 满足 `IS NOT NULL`，于是"看不见内容"被当成"有内容"。正确量具是 `jsonb_typeof(col)='array'`。该量具纪律已写入判据文件头，并由 `TestBodyTypePredicateRejectsJSONNull` 在真库上钉死。

### 逐 API 全量扫描（416 条 GET，本地 release 2671）

- 状态分布：200×204、405×93（仅接受写方法的端点）、400×58（缺必需查询参数）、404×38（子路由无后缀）、500×12、503×11。
- 无 401：扫描使用的 JWT 生效。
- 延迟：p50 2ms、p95 29ms、max 2.327s。**仅 1 条超过 2 秒**。

### ★ 慢路径定位与优化（`/api/admin/data-lifecycle/storage`）

2.327s 的真实原因不是表扫描，而是**按行函数被重复求值**：`pg_total_relation_size(c.oid)` 在 `SELECT`、`ORDER BY`、`WHERE` 三处各调一次。`EXPLAIN ANALYZE` 实测：Seq Scan on pg_class 3771 行、Rows Removed 3294、Buffers 42035+4150、**执行 1504ms**。改为子查询内只算一次、外层复用该列排序后：**执行 527ms（-65%）**，排序键由 top-N heapsort 完成。改动落在 `admin/data_lifecycle_storage.go`，已 `go build ./admin` 通过。

### ★ 新发现的系统性缺口：部署序列漏登记（本轮未修，如实记录）

扫描中 9 个端点稳定 500，日志给出 `SQLSTATE 42P01`。根因是**本地部署链从未创建对应对象**，不是本轮改动引入：

- `public.cache_metrics`（迁移 470）与 `audit_attachments_cleanup`（迁移 629）在 `sql/migrations/startup/` 与 installer embeddata 中都存在，但**均未登记进 `scripts/apply-db-revision-sequence.sh` 的 files 数组**，本地库也无对应 ledger 标记。
- deploy-local 只跑两条通道：revision sequence + `go run ./cmd/gateway migrate`，两者都不创建这两张表。
- 磁盘 541 个 startup 迁移中 318 个未登记进 files 数组（226 腿）。这大概率是**通道分工**（部分由 installer 通道负责）而非全部缺陷，但至少 470/629 已造成 9 个端点 500。
- 处置：不在本轮批量登记（需逐条分析依赖顺序，盲目插入风险更高）。已作为明确遗留项交给维护者；判定标准应是"在干净环境上跑 deploy-local 后，比对 installer 通道的对象清单与实际 catalog 的差集"。

### 真实模型可用性现状（如实记录）

| 模型 | 结果 | 依据 |
|---|---|---|
| deepseek-v4-flash | 5/5 轮 200 | 绑定 `is_routable=true`，探针正常 |
| claude-sonnet-5-5 / gpt-5.6-luna / grok-4.6 | 重部署后 503 | 三者共用 credential 3；日志 `quota_permanent` + 上游 429「Organization quota exceeded」，凭据被正确熔断为 `availability_suspended` |
| minimax-m2.7 | 503 | `node_probe_state.last_err_code=http_410`，凭据 EOL 冷却 |
| glm-5.3-flash | 503 | `model_offers` 中无该模型名 |
| mimo-v2.6-flash | 400 | 来源 `permanently_exhausted`，无 ready/ok 绑定 |

## 当前状态（非最终验收）

已恢复并合并上游、构建与部署序列重放全绿、869 晋级迁移入库并双重放行、缺陷 A/C 经真实流量验证修复、416 条 API 全扫并完成唯一 >2s 路径的优化（1504ms→527ms）。**仍未闭环**：缺陷 B 的 `failAndMark` 类补齐；部署序列漏登记导致的 9 个端点 500；浏览器逐页验收（登录需人工接管，本轮未完成）；MiMo 与三模型的上游配额缺口；最终独立审查与提交推送。

## 集成验证轮（release 2.5.8.2671）

### 部署与环境开关生效

`scripts/deploy-local.sh deploy` RC0，`VERIFY_PASS=1`。首次尝试被前端 refuse 门拦下（worktree 无 `web/node_modules` 且无 `web/dist/index.html`），按门的要求处理：确认 `web/src` 与主 checkout **逐文件一致**后复用同源 `web/dist` 与 `web-mobile/dist`（不复制 `node_modules`）。生效开关：`/api/system/bootstrap/status` 返 `activated:true`；`/mock/v1/chat/completions/{fast,slow}` 已注册并返回真实 mock 响应（此前本地未启用，落 SPA 返回 200 HTML）。

### 造数

mock 探针 800 条（fast/slow 各 400），**800/800 返回 200**，吞吐 19.7 req/s，p50 0.31s、max 1.01s。mock 流量按设计不进会话族（探针/自检无会话头），因此 `session_turns_hot` 未按 800 增长——这是既有契约，不是丢失。

### 真实模型多轮（4 模型 × 5 条样例提示词 = 20 次）

`deepseek-v4-flash` **5/5 全 200**（2.71 / 1.47 / 1.80 / 7.48 / 2.06s）。其余三模型全部 503，且耗时仅 0.04–0.17s ⇒ 候选耗尽而非上游超时。根因在 catalog 与日志中确认：三者共用 `credential_id=3`，该凭据 `quota_state=permanently_exhausted` / `availability_state=suspended`，日志为 `circuit quota cooling aligned to upstream reset … error_kind=quota_permanent`，`executor failed … upstream 429`。**真实上游配额耗尽，网关熔断行为正确，非缺陷**；与 `mimo-v2.6-flash`（来源 `permanently_exhausted`）同类。本轮无法为这三个模型补齐多轮证据，需凭据额度恢复。

### 三项修复的线上验证

| 缺陷 | 验证方式 | 结果 |
|---|---|---|
| 失败请求 trace 终态错标 `success` | 4 个真实 503 请求的 `trace_events->>'final_status'` | 全为 `failed` ✅ |
| 入站正文落 JSON null | 5 个真实 200 请求的 `jsonb_typeof(request_delta/response_delta)` | 全为 `array` ✅ |
| 失败请求 `status_code` 恒 500 | 同一批 503 请求 | **仍为 500 ❌**（`no_candidate` / `transient` 路径），已派单补齐 `failAndMark` 类 |

### ★ 新发现缺陷：session 晋级丢弃 52/106 列（已修，迁移 869）

不是"只丢 trace_events"。`promote_session_turns_hot_to_partition` 的 INSERT 硬编码 **54 列**，而 `session_turns_hot` 实有 **106 列**，每次晋级静默丢 52 列（含 `trace_events`、`request_status`、`request_delta`、`is_final_success`、`identity_hash`、`upstream_status_code`、`system_fingerprint` 等）。真库证据：父表 280 行 `count(trace_events)=0`，热表 264 行全部非空。

根因不是"某迁移忘了加列"：`707_session_turns_s1a.sql` **早已**交付目录派生列清单（`v_cols` 取自 `pg_attribute`）与形状守卫；而 `640_session_turns_protocol_fields.sql` 为给 713 补 `client_protocol` 前置被登记进通道且**排在 707 之后**（通道清单 707 在第 724 行、640 在第 746 行），每次重放都用旧的硬编码 54 列函数体覆盖 707 的修复；`intentional_function_chains` 更把 `707 → 640` 固化为"有意链、640 为终态"。**结果：全新安装正确（installer 链为 640(391) → 688(477) → 707(493)，707 在最后），存量/部署库错误**——两侧行为分叉的根源在此。原有形状守卫抓不到，因为它比的是**表**形状（两侧确实都是 106 列），丢列发生在**搬运清单**里。

修复：迁移 869 把目录派生体重新确立为本通道终态（排在 640 之后、登记为链尾），并保留形状守卫 + 后置条件。登记已做全五点：`sql/migrations/startup/869_*.sql`(+down)、`apply-db-revision-sequence.sh` files 与链、`installer` embeddata + `main.go` map + `dbinit/runner.go` StartupFiles、`sql/schema/installed_startup_migrations.tsv`（+1 行，第 264 位）、`docs/db-changelog.md`。本地已应用并生效（函数转为目录派生），完整序列 RC0、二次重放零重投递、ledger 224 行且适配标记数不变。

**历史损失不可回补**：晋级是 DELETE+INSERT，被丢的 52 列没有第二份副本。本地 280 行历史行的这 52 列全为 NULL。本轮未改动任何历史数据。

**未处理但已登记**：该函数的默认保留窗现为 `'7 days'`，而 707/688 与 `bg/partition_manager.go` 的 `DefaultRetentionWindow = 8h`——`'7 days'` 本身也是 640 覆盖的产物。它把热窗从 8 小时拉长到 7 天，是行为变更而非数据丢失修复，未纳入本次改动，留待 owner 拍板。

### 量具纪律：一次自我更正（JSON null 不是 NULL）

我最初用 `request_delta IS NULL / IS NOT NULL` 判定正文缺失，得出"正文完整、缺陷不成立"的**错误结论**，并据此驳回了子任务的发现。改用 `jsonb_typeof(...)` 后事实相反：grok/gpt 两行的 `request_delta` 与 `outbound_body` 是 **JSON null**（`IS NOT NULL` 为真但无任何内容），深seek/claude 是 `array`。**这正是项目已记录的假绿形态**："看不见"与"没写"必须分开判。此后所有正文断言一律改用 `jsonb_typeof`，并在判据文件中固定了量具自身的行为测试。

### 逐 API 扫描（416 个 GET 端点）

| 指标 | 值 |
|---|---|
| 扫描端点 | 416 |
| 200 | 204 |
| 405（GET 打只读 POST 路由，属扫描口径） | 93 |
| 400 / 404（子路由缺路径参数） | 58 / 38 |
| **500（真实失败）** | **12** |
| 503（子系统未启用：identity-pool / mobile-node / free-discovery / memora） | 11 |
| p50 / p95 / max 延迟 | 0.002s / 0.029s / 2.327s |

**唯一 >2s 的路径**：`/api/admin/data-lifecycle/storage`，冷启 2.327s，复测 1.44–1.76s。分解：`pg_database_size` 34ms、`pg_stat_user_tables` 19ms、**public 432 个关系的 `sum(pg_total_relation_size)` 697ms**，其余约 0.7s 为文件系统与日志目录遍历。属于目录聚合的固有成本而非缺索引，优化（短 TTL 缓存 / 拆分 / 并发）已派单。

**12 个 500 的根因只有两类真实 SQL 错误**（日志中全量解析所得）：
- `column sd.stop_reason does not exist` —— `admin/session_health_api.go:711` 对 `session_dim` 按 `stop_reason` 过滤，而该表 17 列中无此列 → `/api/admin/sessions/inspector-stats`；
- `column sc.title does not exist` —— `admin/annotation_handler.go:706` 的 LATERAL 从遗留表 `public.sessions` 取 `title`，而该表 36 列中无此列（`title` 现存于 `session_summaries` / `session_turns(_hot)` / `session_titles`）→ `/api/admin/annotations/first-turn-samples`。

注意 `sql/migrations/startup/456_session_v2_display_columns.sql` 确实写过 `ALTER TABLE public.sessions ADD COLUMN IF NOT EXISTS title`，但它不在部署通道清单中；`sessions` 是否已成遗留表需按"会话 v2 以 session_summaries/session_turns 为准"的事实判断，**不能因为历史迁移写过就补列**。其余 500 端点（attachments cleanup、data-lifecycle metrics、prompt-injection、session-audit、session-clusters、session-crosstalk、turns/sessions）需逐条分类，已派单。

### 浏览器逐页验收：当前阻塞

内置浏览器运行时对登录凭据填写强制要求人工接管（每次 `mcp_browser` 返回 `safety.classification=authentication_user_takeover` + `requiredNextTool=ask_user`），即使获得用户两次明确授权也无法代填；该守卫由 Host 侧强制，仓库内无可关闭的配置或规则。解除 bootstrap 激活门后页面可正常加载、登录弹窗正常弹出，**仅差人工输入凭据这一步**。API 级验收不受此限制，已按上表完成。

### 集成门

- `CGO_ENABLED=0 go build ./...` → RC0；`go vet` 三个相关包 → RC0。
- `sql/migrations/startup`、`internal/sessionv2mirror` → ok。
- `sql/schema` 的 `TestGateInjectsEveryDBCredentialName` 一度为红：新增测试引入了 3 个 DB URL 变量名而 harness 未注入（`CONTEXT_ATTRS_TEST_DATABASE_URL`、`DECISION_RETIREMENT_TEST_DATABASE_URL`、`ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL`）。已在 `scripts/audit/run-integration-gate.sh` 的 env 块补齐，并顺带注入 `TRACE_RETIREMENT_TEST_DSN`（否则本轮 trace 判据会在门禁里静默 skip），复跑通过。该门正是为防"只注入一部分名字导致 integration 全 skip 而门仍绿"而设。

### 12 个 500 的根因分类（四类，非一类）

| # | 端点 | 查询引用 | 库内实际 | 类别 | 结果 |
|---|---|---|---|---|---|
| 1 | `/sessions/inspector-stats` | `session_dim.stop_reason` | 17 列中无此列，且**任何迁移都没建过** | (b) 引不存在的列 | **已修**：删除无法满足的过滤，代码内写明"今日回收 = status='closed' AND closed_at >= CURRENT_DATE"，并说明因停机原因从不持久化而无法按 idle/evicted/manual 细分 |
| 2 | `/annotations/first-turn-samples` | `public.sessions.title` | 33 列中无此列 | (a) 登记缺口 | 修 456 登记后自愈（验证中） |
| 3 | `/turns/sessions` | 456 整组显示列 | 全缺 | (a) | 同上 |
| 4 | `/prompt-injection/detections` | `detection_score`、`matched_rules` | 表在，列缺 | (a) | 同上 |
| 5 | `/prompt-injection/stats` | `prompt_injection_stats_enhanced` | 关系不存在 | (a) | 同上 |
| 6,7 | `/session-clusters[/]` | `session_clusters`、`session_cluster_members` | 关系不存在 | (a) | 同上 |
| 8 | `/session-crosstalk` | `session_tenant_binding` | 关系不存在 | (a) | 同上 |
| 9 | `/attachments/cleanup/execute` | `audit_attachments_cleanup`（含 fs 变体） | 关系不存在 | (a) | 同上 |
| 10 | `/data-lifecycle/metrics` | SQL 本身合法 | — | (d) NULL 扫描 | SQL 跑通，但 `(…/NULLIF(COUNT(*),0))::bigint` 空表得 NULL，Scan 进非空 `int64` → 500 |
| 11,12 | `/session-audit[/export]` | SQL 本身合法 | — | (c) 租户上下文 | super_admin 未带 `tenant_id` 时 `withTenantTx` 收到空租户 → `SET LOCAL app.current_tenant` 失败；`/session-audit/stats` 走裸 `QueryRow` 故仍 200 |

**(a) 是统一机制**：这些迁移文件存在于 `sql/migrations/startup/`，也已登记在 `sql/schema/installed_startup_migrations.tsv`（installer 安装通道），但 `grep` 在 `scripts/apply-db-revision-sequence.sh` 的通道 files 数组里**全为 0**——**全新安装有这些表，部署通道升级上去的库没有**。这是继 869 之后的第二类"通道与安装清单分叉"，形态同构。

**更大的连带发现**：上述 (a) 不只是 7 个只读端点 500。`internal/titlestore/store.go:268/333` 在**每次标题提交**时执行 `UPDATE public.sessions SET title=…`，而该列不存在 → 写侧持续 42703。这比任何 admin 500 的影响面都大，且会在 456 登记后自愈——但必须实测确认，不能假设。

**既有假绿夹具**：`admin/session_retirement_http_test.go:86` 的 fixture **自己造了 `stop_reason` 列**，所以一直没抓到这个 42703。新 fixture 改用 `CREATE TABLE session_dim (LIKE public.session_dim)`，结构由真实 catalog 决定，并用断言把"前提仍在场"钉住。**这是本轮第二次同形态教训**：判据自带前提会把真实缺陷藏起来。

### 唯一 >2s 端点的优化结果与两处测量纠偏

`/api/admin/data-lifecycle/storage`：优化前均值 **1.920s** → 优化后 **8.1µs**，响应契约（字段、warning 语义、HTTP 状态）不变。

- **纠偏 1（我此前的 697ms 是近似值）**：我当时量的是 `nspname='public' AND relkind='r'`（432 个关系）。handler 真实查询没有 relkind 过滤、只排除 `pg_catalog`/`information_schema`，实际扫 **3362 个关系**，冷 1622–1724ms、热 1446ms，`EXPLAIN ANALYZE` 执行 1742ms——**我低估了约 2.3 倍**。
- **纠偏 2（"文件系统遍历"说法不成立）**：`queryFilesystem` 只做一次 `statfs`（<1ms）；第二段成本来自 `queryDirectory` 遍历日志目录（本机无日志目录时回退 cwd，22820 个文件含 `node_modules`，约 250ms；生产指向真实日志目录，属开发环境放大）。
- **方案取舍按实测否决**：errgroup 并发 2.24s→1.93s（仅 −13%，仍超 6 倍）；"总览只取 `pg_database_size`"会丢掉前端 `StorageOverview.vue` 实际渲染的 `total_human`/`tables_bytes`/`indexes_bytes`/`toast_bytes`；把两段 size SQL 合并成一次扫描实测**无收益**（1.87–2.08s vs 1.86–2.03s）故不采用。最终用 **30s 快照缓存 + singleflight**（按 pool 分桶而非进程单例，遵 LESSONS 2026-10-15「包测串味」条目），并接到 `runTableMaintenanceJob`（VACUUM FULL/REINDEX 会实改物理尺寸）显式失效。
- 判据含"连续 5 次旧路径 + 5 次热路径，采集次数仍为 1"、"8 并发只采集 1 次"、"失效后重取的值等于实时库值"；两次变异（绕过缓存读、TTL 改 365d）均准确变红。

**一条方法论教训（由该任务自查发现）**：判据若**覆盖了被测常量本身**（测试里 override TTL），对该常量的变异就不可见。修法是另加一条针对**生产默认值上界**的断言。

## 遗留慢查询风险项：真实 EXPLAIN（不照抄静态报告）

按「索引/分区优化必须依据真实 SQL、catalog 与 EXPLAIN」的要求，对静态审计标注的风险项逐条在本机 PG17.10 上实测。数据量：`_request_logs` 0 行 / `session_turns` 395 行 / `session_turns_hot` 155 行 / `sessions` 550 行。因此本节的价值不在于「找 >2s」（扫描已证明唯一 >2s 的是 storage 端点，已优化），而在于**确认 plan 形状是否有结构性隐患**。

| 查询形态 | 实测 plan | 判定 |
|---|---|---|
| `SELECT count(*) FROM session_turns`（无界 COUNT） | Append → 4 个分区 Seq Scan，0.951ms | **正常**。无谓词的全表 COUNT 本就该全扫，非缺陷 |
| `WHERE tenant_id=? AND ts > now()-30d ORDER BY ts DESC LIMIT 50` | Merge Append → 各分区 Index Scan，**0.714ms** | **健康**，分区级索引命中 |
| `WHERE request_id=? OR client_request_id=?`（`admin/logs.go:896` 的反查形态） | Append → 4 个分区 Seq Scan，`Rows Removed by Filter: 395` | ★ **确认退化** |
| 同一查询走真实视图 `request_logs_with_current_month` | `session_turns_hot` Seq Scan（155 行被 filter）+ 父表各分区 Seq Scan | ★ **同样退化** |

**根因（catalog 实测，非推断）**：`client_request_id` 在 `session_turns*` 全族上的索引数是 **0**，而 `request_id` 有 **23** 个。OR 条件的一侧无索引 ⇒ planner 无法构造 BitmapOr ⇒ 全分区 Seq Scan。这精确对应静态审计标注的「`admin/logs.go:896` OR 跨 request/client id」。

**处置：本轮不修，如实记录。** 理由：(1) 实测 0.6–1ms，远未触及本轮验收标准「>2s」；(2) `session_turns` 有 106 列，补索引需在每个分区上单独建立并进部署通道，属独立工作而非本次数据丢失/退役修复的范围；(3) 修复需在生产数据量下复验收益，当前无生产数据可依。**若 owner 决定补，应新增一条编号更高的迁移为 `client_request_id` 建索引，而非改历史迁移。**

**顺带确认一个曾疑似的问题**：`session_turns` 父表上有 19 条 `CREATE INDEX ... ON ONLY`，一度像是「索引没落地」。实测分区层另有 **78** 个实体索引（命名如 `session_turns_2026_07_tenant_id_ts_idx`），带过滤查询确实命中分区级索引（见上表第 2 行）。**父子分工正常，不是缺陷**——`ON ONLY` 在父级登记依赖、实体建在分区，是分区表的正确做法。

## 判据层面的四处修复与一次证伪

退役轮的判据本身也会失效。本轮发现并修复 4 处，其中 1 处是**假绿源**（判据从不执行却报 PASS）：

1. ★ **假绿源：`admin/data_lifecycle_storage_perf_test.go` 的 6 条判据在每一个真实环境里静默 SKIP。** 跳过条件只认 `TEST_DATABASE_URL`，而本仓真库判据的实际来源是 `LLM_GATEWAY_DATABASE_URL`（`/tmp/lgw-env.sh` 与 `scripts/audit/run-integration-gate.sh` 都是这个）。同族判据 `admin500DSN` 本就有三级回退，唯独这个文件没有。**后果**：30s 快照缓存 + singleflight 这个优化（1.9s→µs）在 CI 与任何按标准环境跑的流程里等于**没有判据守护**，却显示 PASS。改为与 `admin500DSN` 同构的三级回退后，6 条判据真正执行并全绿（cold=2.754s → hot=7.792µs，speedup≈35 万倍）。**已做变异验证**：把 `time.Now().Before(snap.expiresAt)` 短路成 `true`（缓存永不过期），`TestDataLifecycleStorageTTLExpiryRefetches` 准确变红在 `want 2 —— TTL 形同虚设，数据会永久陈旧`，还原后复绿。
2. ★ **判据自身的 SQL 拼装 bug（`admin_500_column_contract_realdb_test.go`）**：`titleCol=", "` 与 `titleVal=", 'Cold session title'"` **各带一个逗号**，拼接成 `'cli-cold', , 'Cold session title'` ⇒ SQLSTATE 42601。这条分支此前**从未执行过**——因为 `public.sessions.title` 一直不存在，`realHasTitle=false`；456 补登让该分支第一次跑起来才暴露。改为分隔符只由 `titleCol` 承担后 PASS。**这是「判据的未执行分支等于未测试」的又一实例**。
3. **`admin/cross_table_request_id_representation_test.go` 的白名单项失效**：本轮退役把 `analytics.go` 的 `AND request_id::text = ANY($1::text[])` 改成了 `AND request_id = ANY($1::text[])`（去掉空操作 cast，依据是该文件内 R47 实测注释：真库 `_request_logs(_hot).request_id` 均为 text，母表存原始 hex32；真正 uuid 型的是 `routing_decision_log.request_id`）。判据的反向检查要求白名单项仍能被 grep 到，于是变红。**移除该白名单项**——判据自己就警告过「否则它是一条恒绿断言」，而它的放行理由已随被放行的那行代码一起消失。
4. **已确认由本轮改动引起、正在修复的 5 条门禁**：本轮把 `admin/auto_route_outcome_freshness.go` 的 `FROM _request_logs_hot` 改为 `FROM ` + `bg.ActiveSettleTurnsTable()`（读源随写门切换，这正是退役目标）。SQL 从字面量变成「拼 Go 表达式」，退役族的机械分类器 `sourceFamilyOf` 认不出它，5 条门禁变红（inventory 陈旧 / 未归类 / 不在 `v1ReaderIndirectPopulation` / Evidence 不存在）。**这属于判据契约与新形态不同步，不是产品缺陷**；修复方向是让分类器认识「动态源」形态并归入语义正确的族，而非删文件、降级断言或扩大白名单。

**一次证伪（记录在案）**：子任务报告称上述失败「可证明与本轮改动无关，追溯到退役轮的 staged 改动」。我用 AGENTS.md 判据纪律第 4 条的**强证据**方法证伪：`git worktree add --detach` 到 pristine `origin/main`、同一数据库复跑，`TestCrossTableRequestIDRepresentation` 在 origin/main **PASS**、在本 worktree **FAIL**。同一数据库下唯一变量是代码 ⇒ 这些失败确由本轮改动引起。**子任务当时用的是 `git stash`（弱证据，且它自己承认因此破坏了 staged/unstaged 区分）。**

**另一条子任务报告里的实际发现**：该缺口长期不可见的结构性原因是 `scripts/apply-db-revision-sequence_test.sh:120` 的 `[[ "$version" < "690" ]] && continue` —— 通道交付路径的守卫**只覆盖编号 ≥690 的文件**，低编号从未被复核。

## ★ 第二个慢端点：`/storage/tables` 4–10s，根因是一层多余的派生表

上一节的 storage 优化做完后，全量端点扫描（480 端点）在 **release 2673** 上又暴露出一个此前被漏掉的 >2s 端点：`/api/admin/data-lifecycle/storage/tables` **稳定 4.3–7.5s**（`/storage` 总览因已加缓存降到 1.6ms，唯独它没被覆盖）。按"优化超过 2 秒路径"的要求，本轮把它修掉。

**定位过程（每一步都有实测，不靠推测）**：

1. **端点耗时与 limit 无关**：limit=1/20/100/200 都是 4.0–5.7s ⇒ 瓶颈是与行数无关的固定开销，不是每行成本。
2. **同一条 SQL 用 psql 只要 1.05–1.33s** ⇒ 差额不在 SQL 逻辑，在执行路径。
3. **Go 侧分解**（临时诊断测试，已删）：`acquire=12ms`；`queryTableSizes` 本身 4.0–6.3s；full handler 3.8s（与它相等）⇒ 中间件、JSON 编码、连接池全部无罪，**钱全花在这一次查询里**。
4. **SQL 变体对照**：完整 845ms → **去掉 toast 相关子查询 72ms** ⇒ toast 子查询占 773ms。
5. **EXPLAIN ANALYZE 对照（决定性证据）**：

   | 形态 | `SubPlan 1` 计划 | loops | Execution Time |
   |---|---|---|---|
   | 带 `FROM ( ... ) sized` 派生表（原写法） | `Seq Scan on pg_class t`（每次 3881 行） | **440** | **4571ms** |
   | 不带派生表 | 同上 | **20** | **1104–1289ms** |

**根因**：`queryTableSizes` 外层套了一层 `FROM ( ... ) sized` 派生表，**挡住了 planner 把 `LIMIT 20` 下推**。toast 那个相关子查询因此必须对内层全部 440 行求值，而不是只对最终返回的 20 行 —— 440 × 3881 ≈ **170 万行**的全表扫描，全部花在最终不会返回的行上。

**★ 一条关于"优化本身"的教训**：那层派生表是**同一天早些时候刚加的"性能修正"**，注释写着「pg_total_relation_size 每行最多算三次，子查询里只算一次 ⇒ 1504ms → 527ms」。它想解决的问题是真的（每行重复调用），但**修法制造了一个更大的问题**，而且它报的 527ms 是**只测了改后版本、没与"什么都不套"这个更简单的选项对照**。本轮把派生表去掉、改用 `ORDER BY 4` 位置引用：位置引用指向输出列，PG 复用 SELECT 已算出的值，**"每行只算一次"的原意照样成立**，同时 LIMIT 又能下推。语义与原查询逐列相同。

**实测结果**：`queryTableSizes` 4.478s → **696ms–1.772s**；full handler 3.821s → **922ms–1.006s**（**−75%**）。

## 逐 API 全量扫描（release 2673，480 端点）：500 从 12 降到 4

| 指标 | release 2671/2672（修复前） | release 2673（修复后） |
|---|---|---|
| **500** | **12** | **4** |
| 200 | 204（416 口径） | 255（480 口径） |
| 其余 | 405×93 / 400×58 / 404×38 / 503×11 | 405×108 / 400×68 / 404×39 / 401×2 / 503×4 |

**8 个 500 已消除**（12 → 4）。剩余 4 个全部可解释、且均已实测归因：

| 端点 | 归因 | 证据 |
|---|---|---|
| `/prompt-injection/detections` | 315/364 与基线互斥，**实测**登记 315 会 `rc=3` 中断整条腿 | 基线 `01-schema.sql:11398` 的 `risk_level integer + CHECK(1..10)` vs 315 的 `VARCHAR(20)`；隔离库实测 |
| `/prompt-injection/stats` | 同上（依赖 364 的 `prompt_injection_stats_enhanced`） | 同上 |
| `/session-audit` | super_admin 空 `tenant_id` 导致 `withTenantTx` 失败 | 代码缺陷（类别 c），本轮未修 |
| `/session-audit/export` | 同上 | 同上 |

**扫描口径变化（如实说明）**：本轮扫描器是**重建**的（原 `api_sweep.py` 被一个子任务连同整个 `.audit-validation/` 误删），端点数从 416 变成 480，**因此两列的绝对值不可直接相减**，可比的是 **500 的个数与具体端点名**。新扫描器还发现两个 SSE 流端点（`/api/admin/system-monitor/stream`、`/api/free-pool/stream`）恒定占满 8s —— 那是**流式端点保持连接**的正常行为，被我的固定超时记成了 `max_ms=8003`，**不是慢路径**，已在测量中排除。

### 最终门禁（release 2674，全部实跑）

| 门禁 | 结果 |
|---|---|
| `go build ./...` | ✅ RC0 |
| `go vet ./admin/` | ✅ RC0 |
| `go test ./admin`（**全包**） | ✅ **ok 156.210s** —— 修复前该包有 6 个红 |
| `go test ./cmd/gateway` | ✅ ok 2.004s（**须 `CGO_ENABLED=1`**，见下方量具坑） |
| `go test ./bg/` | ✅ ok 28.183s |
| `go test ./sql/migrations/startup ./db ./internal/trace ./internal/sessionv2mirror` | ✅ 全 ok |
| `LLM_REVISION_RUNTIME_LIVE=1 bash scripts/apply-db-revision-sequence_test.sh` | ✅ **9/9**，RC0 |
| `go test ./admin -run 'TestSessionRetirementHTTP\|TestInspectorStatsTenantFilters'`（真库 DSN） | ✅ ok |
| storage 6 条真库判据 | ✅ 6/6（修复前**全部静默 SKIP**） |
| 变异验证（缓存 TTL / Evidence 锚点 / registry family / population 归属 / inventory 计数） | ✅ 7 处全部"改坏即红、还原即绿" |

**一条量具坑（记在案）**：本机 macOS SDK 与 CLT 链接器不兼容，`CGO_ENABLED=0` 是 AGENTS.md 规定的必要妥协。但**用它跑 `./cmd/gateway` 会让 10 条 `TestLite*` 判据必然失败**，报错是 `sqlite: 当前二进制以 CGO_ENABLED=0 构建，SQLite 驱动不可用` —— 这是**量具不适配，不是回归**。用 `CGO_ENABLED=1` 复跑同一包 `ok 2.004s`。判别方法：全部 10 条失败**原因完全一致**且指向构建标签，而非某个断言行；且 lite 存储是独立存储平面，与本轮 full 模式的会话族退役无因果关系。

## 物化视图核查（目标项：确认已切到 session_turns 相关表）

本机 `pg_matviews` 共 **8 个**，逐个做了定义解析 + 实际 `REFRESH`（比只看定义强）：

| 物化视图 | 直接源 | 是否读 `_request_logs*` | REFRESH |
|---|---|---|---|
| `session_client_stats` | `session_summaries` | ❌ | ✅ |
| `session_owner_stats` | `session_summaries` | ❌ | ✅ |
| `session_client_task_matrix` | `session_dim` | ❌ | ✅ |
| `session_task_stats` | `session_dim` | ❌ | ✅ |
| `tuning_signals_5m` | `tuning_signals` | ❌ | ✅ |
| `tuning_signals_daily` | `tuning_signals` | ❌ | ✅ |
| `routing_analytics_7d` | `credentials` + `routing_analytics_source` | ⚠️ 间接 | ✅ |
| `routing_audit_summary_7d` | `routing_analytics_source` | ⚠️ 间接 | ✅ |

**8 个全部 REFRESH 成功** —— 意味着它们都能在退役后的当前数据上正常重算，不依赖任何已被关闭的写入路径。

### 两个 routing MV 的间接依赖：查清后确认是「有意保留」而非遗漏

递归展开依赖链发现：

```
routing_analytics_source
  └─ request_logs_with_current_month          ← 三层 UNION ALL 的混合视图
       ├─ session_turns_hot  t  + session_turn_details_hot d     (新数据)
       ├─ session_turns       t  + session_turn_details d        (历史月)
       └─ LEFT JOIN LATERAL → _request_logs_hot / _request_logs  (仅历史补全)
            WHERE NOT EXISTS(session_turns_hot …) AND NOT EXISTS(session_turns …)
```

即：**新写入 100% 来自会话族**；v1 只贡献「会话族里没有、且 v1 侧还留着」的补位列行，并由**反连接**保证不与会话族重复。这正是目标里「先改名加下划线作待删除标志 + 保留旧历史可读」的形态，属**有意保留**，不是遗漏。

### 顺带证伪一个早前标记的 anti-join 风险项

早前静态审计把上面那个 `NOT EXISTS` 反连接标为风险（担心跨分区全表扫描）。真机 EXPLAIN：

```
SubPlan → Index Only Scan using session_turns_2026_*_request_id_idx
          Index Cond: (request_id = h.request_id)
          Heap Fetches: 0
Execution Time: 0.079 ms
```

**走索引、`Heap Fetches: 0`**，不是表扫描。**该风险项证伪**。

## ★ origin/main 上的移动端构建是坏的（rebase 带进来的，已修）

页面逐页验收时发现 `/admin/data-lifecycle` 与 `/dashboard` 都卡在「加载中…」，而**服务端完全正常**（同一批请求在网关日志里 `dur=5–60ms`，`/storage/tables` 仅 60ms）。沿"响应回来了但前端不收"往下查，抓到一条与本轮退役无关、但**阻断发布**的上游缺陷。

**排查链**（每一步都排除了一个假设）：

1. 等 6 秒后仍卡住 —— 排除"瞬时快照"（上一轮我在这上面误判过一次）。
2. Network：19 个请求全部 `pending` —— 看起来像连接池耗尽。
3. `lsof`：浏览器到 8782 只有 **4 条**连接，未达 Chrome 的 6 条上限 —— **排除连接耗尽**。
4. 网关日志：那批请求**全部到达且很快**（`/storage` 5ms、`/storage/tables` **60ms**、`/jobs` 53ms）—— 排除服务端 hang。
5. 追根因：重新构建前端，发现 **`web/dist` 是 03:53 的旧产物**，而 rebase 进来的 24 个提交改了大量 `web/src` / `web-mobile/src`。我此前复用 dist 的一致性检查是在 **rebase 之前**做的，基线变了而检查没跟着做。
6. 重建时 `web-mobile` 构建失败 ⇒ 部署 refuse 门**正确拦截**（"refusing to deploy a release without the mobile surface"）。

**真正的缺陷**：`web-mobile/src/components/shell/AppBottomNav.vue` 的 `safeNavigate` 形参声明为 `(e?: MouseEvent) => Promise<void> | void`，但 vue-router slot prop `navigate` 的真实返回是 `Promise<void | NavigationFailure>`。TypeScript 的 `Promise<void>` **不接受** `Promise<void | NavigationFailure>`（`void` 不是"可忽略"的 any），于是 `vue-tsc -b` 报 **TS2345**、`pnpm run build` 退出码 1。

责任归属已核实：该文件**不在本轮任何改动集内**（`git diff HEAD~1 HEAD` 无它），最后改动来自 origin/main 的 **`454740b35 fix(mobile): 底栏 RouterLink 加防御性 @click`**。也就是说 **origin/main 上任何人构建 web-mobile 都会失败**。

**修复**（最小、语义等价）：形参改为 `(e?: MouseEvent) => unknown`。函数体本来就把 `result` 当 `Promise<unknown>` 处理并 `.catch()`，运行期行为逐字不变。判据就是 build 脚本内嵌的 `vue-tsc -b`（类型门）本身——修复前退出码 1，修复后 `✓ built in 1.62s`、RC0。

**两条教训**：
- 复用构建产物的**一致性检查必须跟着基线走**。我检查 `web/src` 与 `dist` 是否一致是在 rebase 之前做的；rebase 引入 24 个前端提交后这个前提就失效了，而部署的 refuse 门只查"dist 是否为空"，查不出"dist 是否陈旧"。
- refuse 门**再次证明了自己的价值**：它没有放行一个前端不完整的 release 上去。这与 AGENTS.md 记录的"空 web/ 白屏事故"是同一道门在另一侧的功劳。

## ★ 页面挂死的排查链：服务端逐项排除后，停在客户端主线程

逐页验收时 `/dashboard` 与 `/admin/data-lifecycle` 都卡在「正在检测登录状态…」，且 Browser 的 `console` 查询连续两次 **275s 超时**（这本身就是信号：渲染进程的主线程被占满）。排查刻意走了"每一步排除一个假设"的路线：

| 步骤 | 做法 | 结果 | 排除了什么 |
|---|---|---|---|
| 1 | 等 6s 再看 | 仍卡住 | 排除「瞬时快照」（上一轮我在"共 0 条"上误判过一次） |
| 2 | Network 查询 | 19 个请求全 `pending` | 看似连接池耗尽 |
| 3 | `lsof` 统计浏览器连接 | 只有 **4 条**（Chrome 上限 6） | **排除连接耗尽** |
| 4 | 网关日志按路径聚合 | 同一批请求**全部到达且很快**：`/storage` 5ms、`/storage/tables` **60ms**、`/jobs` 53ms | **排除服务端 hang** |
| 5 | curl 取静态资源 | `index.html` 200、`index-jKNFvw0R.js` 200/1.36MB，部署产物与工作树构建**同一份** | **排除产物不一致** |
| 6 | 重启网关 + 全新构建重部署（2677）后复现 | 仍卡 | **排除网关进程状态与旧产物** |

**服务端、数据库、静态资源、网关进程四者全部排除**。剩下的唯一解释是**客户端 JS 主线程在该页面上被占满**，`App.vue:261` 的 `v-if="isHydrating"`（= `!store.authHydrated`）因此永不翻转。

### ★ 一条我自己推翻的结论（记在案）

我一度把「单秒 96 个 `/api/routing/resolve`」判定为**客户端请求风暴/死循环**，并准备据此归因到 `a1a1eba49`。复核后**这个结论是错的**，两条量具错误：

1. **网关日志每条请求写两遍**（连续 4 行的 `request_id` 成对出现），所以 96 实际是 **48**，不是 96。峰值约 58 req/s。
2. 更关键：`routing/resolve` 只在**我打开标签页的那一刻**离散出现（19:34 / 19:42 / 20:17 / 20:24 / 20:33 各一次），**不连续**。真正死循环会持续刷屏。它是 `QueuePerspectivePanel.vue:382-386` 的首屏批量解析——代码注释自己写着"特色+热门最多 ~60 个模型"，`RESOLVE_CONCURRENCY = 8` 分块并发，**属设计内的一次性行为**。

**教训**：「某个端点请求量异常高」到「它是死循环」之间隔着两步：先确认**日志是否重复写**，再确认**它在时间轴上是连续还是离散**。缺任何一步都可能把一次正常的批量预取误判成风暴——这与本轮早前「`IS NULL` 量具」和「瞬时快照判读」是同一类错误：**从代理指标直接下因果结论**。

### 未收口的真实问题

我**没有**定位到占满主线程的确切代码。该页在 `Browser` 里已不可用（`console`/`query` 都 275s 超时），缺少浏览器内取证能力，而在没有 Console 的情况下继续读 Vue 源码推断循环点是**猜测**，我不做。

已确认的事实边界：服务端全绿（对应 API 在网关日志里 5–60ms 完成）；同一份产物用 curl 取正常；换标签页、硬刷新、重启网关、重建部署均复现。**需要在一个能读浏览器 Console 的环境继续追**，这属于前端侧问题，且不在本轮退役改动集内（`git diff HEAD~2 HEAD` 对 `web/` 零命中）。

## ★ origin/main 上的移动端构建缺陷（已被上游独立修好，我这边无需再改）

同一个页面会话里撞到第二个问题：重建前端时 `web-mobile` 构建失败，`vue-tsc -b` 报 **TS2345**（`safeNavigate` 形参声明 `Promise<void> | void`，而 vue-router 的 slot `navigate` 实际返回 `Promise<void | NavigationFailure>`），`deploy-local` 的 refuse 门随即**拒绝发布整个 release**。

责任归属：`web-mobile/src/components/shell/AppBottomNav.vue` 最后改动来自 **`454740b35`**，不在本轮改动集内 —— 也就是说 **origin/main 上任何人构建 web-mobile 都会失败**。

我当时做了最小修复（形参改 `unknown`）并成功构建。但随后 `git fetch` 发现**上游已用 `9406de656` 独立修好了同一问题**，且修法更好（用具名类型 `RouterNavigate = (e?: MouseEvent) => Promise<unknown> | void` 并写了精确注释说明 vue-router 5.2.0 的真实签名）。于是我**撤掉了自己的冗余修复、rebase 到含该修复的 origin/main**，当前 `behind: 0  ahead: 0`。

**教训**：本地修好一个上游缺陷后应当 `git fetch` 再确认一次归属；否则会把同一个修复提交两次（这次是靠 refuse 门失败才暴露出来，而不是靠人工比对）。

## 页面级验收覆盖（诚实记录）

浏览器（内置标签页，管理员已登录）逐页核验结果：

| 页面 | 结果 |
|---|---|
| `/`（仪表盘） | ✅ 正常。真实路由数据：38 模型 / 50 节点，deepseek-v4-flash 6 成功，1 节点"配额耗尽"——与 credential 3 的 `permanently_exhausted` 一致 |
| `/bootstrap` | ✅ 渲染正常（激活向导 5 步 + 用户协议门）。因环境用 `LLM_GATEWAY_LICENSE_ACTIVATED=1` 视为已激活 |
| `/request-logs` | ✅ **正常且有数据**：统计概览 550 请求、11 页、每页 50/100/200/500 可选；行内含会话标题、调用方（admin / local-test***）、模型、令牌（读~87/写~0）、延迟 22–53ms、压缩 full、状态（`无可用路由 / no_candidate`），与本轮 503 流量一致 |
| `/admin/turns` | ⚠️ **如实报错**：`query sessions failed / 加载会话失败，请重试` —— 对应 `/api/admin/turns/sessions` 的 500（缺 456 整组显示列）。页面错误路径本身正确（给错误+重试，不是白屏） |
| `/admin/data-lifecycle` | ⚠️ 页面渲染正常，但存储总览与表 Top 在观测窗口内仍显示"加载中"——运行中的 2.5.8.2672 **不含**本轮新增的 30s 快照缓存优化（该优化在最后一次部署之后才完成） |

**未完成的覆盖**：前端共 100 条路由条目（69 个真实 view）。本轮实际逐页打开 5 个页面；其余页面未逐一浏览器打开。**替代覆盖**是 API 级全量扫描（416 个 GET 端点，含状态码与耗时），它覆盖每个后端接口但不覆盖前端渲染与交互。因此"每一个页面"这一条**尚未满足**，需在含全部修复的最终部署后再补一轮。

**两次自我纠错（记录在案）**：
1. 我最初把 `/request-logs` 的"共 0 条"判为前端不渲染。实际 `共 N 条` 是**生效筛选条件计数**（`RequestLogsView.vue:219-230`），且我截图取样在数据返回前。数据实际到位后该页完全正常。**从瞬时快照下结论，与用 `IS NULL` 量具一样不可靠。**
2. 性能测量中我报的 697ms 与"文件系统遍历约 0.7s"都不准（见上节两处纠偏）。

## 真实模型验收汇总

| 模型 | 结果 | 说明 |
|---|---|---|
| deepseek-v4-flash | ✅ 5/5 样例提示词全 200 | 完整多轮证据 |
| claude-sonnet-5-5 / gpt-5.6-luna / grok-4.6 | ❌ 503 | 三者共用 `credential_id=3`，该凭据 `quota_state=permanently_exhausted`，日志 `circuit quota cooling … quota_permanent` + `upstream 429`。**真实上游配额耗尽，网关熔断行为正确** |
| glm-5.3-flash | ❌ 503 | `model_offers` 中无该名字（只有 `z-ai/glm-5.3-flash` 与 `glm-5.3`） |
| minimax-m2.7 | ❌ 503 | 绑定 `minimaxai/minimax-m2.7` 存在，但探针 `http_410`、连续失败 5 次、冷却至 16:03 |
| mimo-v2.6-flash | ❌ 400 `invalid_model` | 来源 `permanently_exhausted`，无 ready/ok 绑定 |

**四项属于上游/凭据现实，非网关缺陷**；需要额度恢复或新凭据才能补齐这四个模型的多轮证据。本地可用凭据与配额恢复前不伪造状态。

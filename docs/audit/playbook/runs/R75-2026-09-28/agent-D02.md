# D02 协议适配域 子代理报告（窗口：`24c5c545a..3a750b3a7`）

> R75 留档注：本报告为只读子代理（Explore）原文，主代理逐条亲读复核后处置。
> 复核结论：F1/F2/F3/F4 证据链全部坐实；F1/F4 已由并行 R73 轮（3909d56e0 P2 快赢
> body_present 对齐 + 5955fcdb0 P2-5）收口，F2/F3 已由 5955fcdb0 P2-4（M-3）收口；
> F5 由本轮（R75）收口 types.go 注释 + 9 locale 措辞；F6 由本轮登记预留标注；
> F7 登记不修。见 docs/audit/2026-09-28-r75-24h-audit-round.md。

## 一、发现（候选，待主代理复核）

**先答核心任务的关键定性**：body_status **不是写路径字段，没有协议臂产生者**。它是读时派生（read-time derived）：`admin/body_status.go` 纯函数从 `session_bodies_unified` 三列（request_delta/response_delta/outbound_body，均 JSONB）现算，落库侧没有也不需要任何列（1a9a59017 提交信息明确推翻了"写时 producer"半成品）。因此"chat/responses/流式/非流式各臂是否漏赋值"一问不成立——四个 admin 读面共用同一分类器/探针，问题转化为**读面之间口径是否一致**（F1）。枚举值集合 = `available | unavailable` 两态（`dropped` 有意不发，admin/body_status.go:43-51 论证成立：无保留期配置、无清理作业、DEFAULT 分区）。

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| F1 | **P2** | **list 与 detail 的 body_status 口径分叉：SQL `IS NOT NULL` 不排 JSON `null` 字面量，分类器排**。列表两处用 `(b.request_delta IS NOT NULL OR ...)`（session_turns_v2.go:125、session_turns_unified.go:66-68），详情三处用 `classifyBodyStatus`（session_detail_v2.go:471、session_turns_v2.go:389、unified_detail.go:381），后者把 4 字节 `"null"` 判为无载荷（body_status.go:82-88）。而 **JSON `null` 是写路径常态产出**：`jsonTextOrNull` 对空载荷返回字符串 `"null"`（domains/session/v2/bodies_writer.go:240-245），INSERT 臂以 `$6::text::jsonb` 写入即 `'null'::jsonb`（bodies_writer.go:316-322、:342）。推论：**凡存在 bodies 行的轮次，列表探针恒真**（三列至少一列非 SQL NULL），即便三列全是 JSON null（响应未到达 + final_full 开关把 OutboundBody 置 nil（session_writer_v2.go:608-611）+ request delta 为 nil 切片 `json.Marshal(nil)="null"`）——同一轮列表报 `available`、详情横幅报 `unavailable`。触发路径：运维在 `/api/admin/sessions/{id}/turns` 看到全绿，点进 `/api/admin/sessions/detail` 横幅却报"部分轮次无正文"。 | admin/session_turns_v2.go:125; admin/session_turns_unified.go:66-68; admin/body_status.go:82-88; domains/session/v2/bodies_writer.go:240-245,316-322,342; domains/session/v2/session_writer_v2.go:608-614 | 把两处 SQL 探针改成与 classifyBodyStatus 同义：`(b.request_delta IS NOT NULL AND b.request_delta <> 'null'::jsonb) OR ...`；补一条 mock 列值为 JSON null 的列表回归测试 |
| F2 | **P2** | **`requestdetail.Meta.BodyStatus` 在 wire 上永不出现——commit a8e407343 声称的"surface through requestdetail.Meta"断链**。全仓唯一赋值点 unified_detail.go:381（ReadSessionTurnsBodies 内）；但 Locator 两条消费路径都丢它：(a) request-logs 命中路径返回的 meta 来自 `loadRequestLogMeta`，从不设 BodyStatus（unified_detail.go:83-139、:141-224）；(b) session-turns 回退路径经 `mergeMeta(requestLogsMeta, sessionMeta)`（locator.go:207），而 `mergeMeta` 没有 BodyStatus 分支（locator.go:286-316）。触发路径：GET request-detail facade（unified_detail.go:429-436）任一来源的响应，`meta.body_status` 恒缺席（omitempty）。消费面 SessionDetailPage 横幅实际走的是 SessionDetailV2API（SessionDetailPage.vue:84），故 UI 无感——但该字段+注释是向未来消费者承诺的契约，现状是空壳成功。 | admin/unified_detail.go:381; domains/requestdetail/locator.go:177,207,286-316; domains/requestdetail/types.go:62 | mergeMeta 补 `if overlay.BodyStatus != ""` 拷贝；ReadRequestLogsBodies 成功取到 bodies 时按 classify 语义填 available；加钉桩 |
| F3 | **P2（当前被 F2 掩蔽的潜伏雷）** | **`omitBody=true` 时 BodyStatus 被错标 `unavailable`**：unified_detail.go:378-381 注释自称"metadata-only fetch 的正确结果"，但按 types.go 的字段语义（"bodies 是否可从底层存储读出"）这是错的——body 在库里存在，只是本次响应没带。现状因 F2（mergeMeta 丢弃）未上线；一旦修 F2，`GET detail?omit_body=1` 会对正文完好的请求报 unavailable。 | admin/unified_detail.go:330,378-381; admin/unified_detail.go:429-436 | 修 F2 时同步：omitBody=true 分支不设 BodyStatus（留空=unknown） |
| F4 | **P2** | **时间线"正文缺失"横幅在默认路由下恒死**。前端 `missingBodyCount` 只统计 `turn.body_status === 'unavailable'`（SessionTurnsTimeline.vue:137、模板 ~201-208），数据源 `/api/admin/sessions/{id}/turns`；该路由默认 `sessions_v2.turns_list_routing="tree"`（settings/spec_sessions_v2.go:188-198 默认 tree），tree 实现的 `SessionTurnTreeItem` **没有 body_status 字段**（session_turns_tree.go:41-49，硬约束"绝不包含正文"）；dual 模式的 `applyTurnsV2Shadow` 也只回填 turn_no/digest_available（session_turns_unified.go:325-361）。只有 routing="v2" 时 TurnListItem.BodyStatus 才上线。前端白名单注释（sessionTurnsTree.ts:49-51）写"后端已发 body_status"暗示必然到达，实际默认配置下 undefined 恒现、横幅永不出现且无报错——正是该注释自警的事故形态。 | admin/session_turns_tree.go:41-49; admin/session_turns_unified.go:244-275,325-361; settings/spec_sessions_v2.go:188-198; SessionTurnsTimeline.vue:137; sessionTurnsTree.ts:46-53,106-117 | 二选一：把默认/灰度计划写进轮文档（横幅随 routing=v2 一起放量）；或 dual 模式把 v2 的 body_status 一并并入 shadow 透传。至少在前端注释标注"仅 v2 路由模式生效" |
| F5 | P3 | **wire 契约注释自相矛盾**：types.go 把 JSON null 归入 available（"incl. JSON null / [] / {}"），body_status.go 与钉桩测试把 JSON null 判为 unavailable。实现+测试自洽，注释错。另：8 个 locale 注释仍写"三态横幅/tri-state banners"而键只有两态（c626bf973 删 bodyStatusUnavailable 后残留，已 grep 确认无 dangling 引用）。 | domains/requestdetail/types.go:36-46 vs admin/body_status.go:82-88、admin/body_status_test.go:24-34; web/src/locales/*/requestDetail.ts 注释行 | 以实现+测试为准改 types.go 注释；顺手修 locale 注释措辞 |
| F6 | P3 | **`apihub.HealthStorage` 死常量**：全仓（含测试）零引用，注释称"供 admin 会话读端点在存储降级时上报"，但 503 路径只写 HTTP body 的 storage_status，从未映射到资产 HealthState。 | apihub/types.go:58；admin/storage_degraded.go 全文无引用 | 接上资产健康上报或在登记表注明"预留"；勿留无消费点的枚举承诺 |
| F7 | P3 | 选择器开启时每次 dispatch 打一条 `slog.Info("endpoint_selector_decision", ...)` 含双 base_url（executor_dispatch.go:247-256）；灰度期逐请求 Info 量级偏大，且注释自认 `Decision.Passthrough` 报而不消（P5 面）。属登记性顺带账。 | domains/streaming/executors/executor_dispatch.go:247-256 | 灰度收口后降 Debug；Passthrough 消费缺口已在注释登记，勿丢 |

## 二、核实为健康的面

- **两态契约与 wire 稳定性钉桩齐全** —— body_status_test.go:12-104：LEFT JOIN 全 miss、JSONB null、纯空白、三列各自命中、null+payload 混合、`[]`/`{}` 仍算载荷、常量字面量锁死、SessionTurnV2 无 omitempty（零值发 `""`）均有测试。
- **详情端点用 raw 字节而非解码值分类是对的** —— session_detail_v2.go:462-471 注释成立：`decodeStoredJSON` 对"列缺失"与"解码失败"同返 nil，按解码值分类会把损坏正文误报"未存储"。
- **native_responses_stream 终态后吞错改动正确且窄** —— terminal 置位覆盖 completed/incomplete/failed 三终态（native_responses_stream.go:267-289）；终态帧先写客户端再置位（:240-251 先于 :274-288），故吞掉的只是响应后的连接噪声（RST/超时）；`error` 事件与 `response.failed` 事件仍走 Interrupted 显式返回（:290-301）；新增 EOF-无终态分支与旧 fallthrough 逐字段等价（同 reason/kind/resumable），纯重构无漂移。
- **executor_chat native Responses 门控收紧无回归** —— responses.go:640-641 的 buildExecParams 恒同时填 `BodyBytes`（chat 形态）与 `ResponsesBodyBytes`，故 capability 缺失回退 chat completions 有合法请求体；mode-fallback 重试路径确会回填 ResponsesBodyBytes（executor_dispatch.go:920-927）。四处新测试钉住。
- **classifyDispatchRoute 恢复 pre-P4 语义且修掉潜在 nil panic** —— 新函数显式 nil 容忍（executor_dispatch.go:265-321）；FF_OLLAMA_NATIVE=false 时回退与 P4 前 fallthrough 字节等价；三案钉桩。
- **provider/client.go 两处变更行为保持** —— `decodeNativeEndpoints` 提取对空入参返回 nil,nil 与旧 inline 等价，且新增 SQL 键↔struct tag 对账测试；`ORDER BY weight ASC→DESC` 仅呈现序对齐：selector 两个入口都先 `sortEndpointsByPriority` 再选点，wire 序不可能改变选择。
- **Subtask 4 存储降级分类器保守得当** —— admin/storage_degraded.go:46-72 只认连接失败/网络超时/DeadlineExceeded，Canceled 显式排除、PgError 留 500；响应体不再回显 err.Error()。
- **turn_digest 列表测试跟齐 25 列投影** —— turn_digest_integration_test.go:437-497 新增 body_present 列断言，列序漂移有回归网。

## 三、未覆盖项与原因

- **F1 中 all-three-JSON-null 行的真库分布**（决定其现网命中率）—— 需要真库执行统计查询，子代理环境无库凭据；结构层面两谓词分叉已由代码证实。
- **`go build ./...` 三门验证** —— Windows 上 fsstore 等 Unix-only syscall 编译失败，属仓库既有平台限制，验证门走 Linux CI。
- **SessionDetailPage.test.ts（118 行）与 sessionTurnsTree.test.ts 的实际运行** —— 需 node/前端工具链，本轮只做了代码级阅读。
- **ORDER BY weight DESC 在真库的执行计划影响** —— 需要 EXPLAIN 真库；逻辑等价性已由 selector 侧重排序证实。
- **sessionv2mirror/replay.go 镜像写路径** —— 窗口内有大改但属并发/重放域（D04），仅确认其写经同一 bodies_writer 封装面。

**给主代理的复核优先级建议**：F1（口径分叉）> F2（死字段）> F4（死横幅）> F3（潜伏雷，随 F2 一起修）；F5-F7 顺带清账。

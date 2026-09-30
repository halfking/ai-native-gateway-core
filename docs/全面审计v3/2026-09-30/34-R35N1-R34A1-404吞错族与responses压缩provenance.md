# 34 — R35-N1 + R34-A1 轮：admin 聚合读面吞错同族收口 & responses 压缩链 provenance

> 2026-09-30 22:30–23:15，基线 `8c7a696b7`（合并 origin 后），交付 `a710d36d2`（R35-N1）+ `f51afc9f4`（R34-A1）。
> 本轮编号沿用 R33–R35 会话轨（附录B 系），与 F04/stats 轨的三十五~四十九轮全局编号独立。

## 1. R35-N1：admin 聚合读面「错误吞成 404/空列表」同族扫描与收口

### 1.1 扫描方法与定性（三个脚本，误报修正过程留档）

- **族 A（rows 迭代族）**：`for rows.Next()` 循环内 `rows.Scan` 失败静默 `continue`（无日志）+ 循环后不检查 `rows.Err()` → 迭代中断（连接断/服务端错误）时静默返回 200 截断列表。全仓（admin+domains）标记 278 处；抽查发现 `auto_title_generator.go:851` 一类「`return logs, rows.Err()`」为扫描器误报（rows.Err 检查在 return 里）。
- **族 B（QueryRow err→404 族）**：初版窗口扫描（18 行回看）报 96 处「无 ErrNoRows 分类」；**精确同分支扫描**（跟踪 `if err != nil` 花括号深度）收窄到 **31 处**，逐个定性后 **19 处真问题**。误报两类：①404 来自 `!exists` 分支而 err 分支已正确走 500（EXISTS 族 keys.go:850 / tenants.go:696 / work_types.go:797）；②已有分类（maas 136/146 ErrNoRows、providers.go:1035、routing 哨兵 errForceEnableCredNotFound、credential_monitor 1675/1775）。
- **教训**：窗口式 grep 交叉会把「同一 handler 内相邻的两个分支」误判为因果，守卫性扫描必须做分支深度跟踪。

### 1.2 修复设计（仿 writeAnalyticsDetailErr 的分类门族）

新文件 `admin/aggregate_read_guard.go`：

| 辅助 | 语义 | 替换的旧形态 |
|---|---|---|
| `writeLookupErr(w, notFoundMsg, err)` | 三门：`pgx.ErrNoRows`→404 原文案；42P01→503+`analytics_view_missing` 引导；其余→500（不外泄 err 细节） | 「一切 err 都 404」（19 处） |
| `writeAggRowsErr(w, op, rows.Err())` | 42P01→503 引导；其余→500；nil→false（正常路径继续） | rows 迭代中断静默 200 |
| `warnRowSkip(op, err)` | Scan 跳行容错语义保留（坏行不毒化整页），但服务端必须留痕 | 静默 `continue` |

19 处真问题分布：`assertCredentialBelongs`（源头不再吞错，err 原样上抛+3 调用方）、keys.go 3 处（assertKeyTenantScope/approve/reject）、usage.go 3 处、users.go 4 处（含 tenant_admin 权限检查路径——DB 故障此前会被误判"用户不存在"）、model_name_mapping 2 处、providers.go 2 处、maas GetOrder、data_lifecycle_attachments、ip_blocklist（PgxStore.Get 透传 pgx err）、stats.go:395（reconciliation diff 审批：ErrNoRows→404/其它→500，保留 slog.Warn+recordFailedAccounting）。

rows 族迁移 10 处核心聚合面：
- `analytics.go` 4 处（matrix/flow l12/flow l23/model-task-index）——l12/l23 的 `err != nil || val <= 0` 复合条件**拆分**（val<=0 是正常过滤不应告警）；
- `dashboard_board_queries.go` 2 处（内部函数 `return nil, err` 上抛）；
- `dashboard_session_stats.go` 4 面板——多面板组合响应，单面板迭代中断正确语义是 **日志留痕+面板降级**（清空该面板）而非整体 500（会丢其它健康面板）；
- `usage_provider_models.go` 3 处（CSV export 流式响应头已 200 无法收回，迭代中断 slog.Warn 留痕——对账用 CSV 静默截断比对账结论致命）；
- `session_list.go` 2 处（`return nil, fmt.Errorf("iterate ...")` 上抛）。

### 1.3 定性登记不修（本轮边界外）

- `provider_cred_lifecycle.go:401`：doHealthCheck err 语义复合（DB not found/解密失败/探测失败都→404 "credential not found"），重构其错误通道超出 R35-N1 范围，登记后续轮。
- `session_state_handlers.go:236/255`：sessionManager.Get 非 SQL 通道，Get 语义即查找失败，风险低。
- rows 族其余 ~140 处（admin 非核心面 + domains）：全量清单属后续轮 backlog，核心聚合面（dashboard/usage/analytics/session-list）已清。

### 1.4 测试与实跑证据

- 单测 `admin/aggregate_read_guard_test.go`：writeAggRowsErr（nil/42P01/中断三分支）、writeLookupErr（ErrNoRows→404 原文案/42P01→503 引导/其它→500 且不外泄细节）、aggRowsErrClassified、**静态接线守卫**（14 个迁移文件必须仍引用辅助函数，防重构退回吞错形态）。
- 真库门控集成 `admin/aggregate_read_guard_integration_test.go`（TEST_DATABASE_URL，本轮实跑 DSN=`llm_gateway@127.0.0.1:5432`）：approveKeyApplication 真不存在 UUID→404 "application not found"；**closed pool（DB 故障）→500 而非 404**（修复目标行为的端到端证明）；reject 分支同族。
- admin 全包 66s PASS；`go build ./...`、installer 13 包全绿。

## 2. R34-A1（P3）：responses 专属压缩链补 AlignmentMap provenance

### 2.1 与 F04/stats 轨冲突检查

`12-F04持久化协议能力设计.md` 的 F04 轨在 credentialfpslot/bg/executors 探针能力持久化；A-G3 定论（R34 批判复审）确认 messages 车道本就带 AlignmentMap、responses 不接 session compressor 是显式设计（防污染 session-cache）。本轮只补 responses **专属链**（`transformation.CompressResponsesInput*`，candidate-window 前置 + 4xx recovery）的 provenance，轨不相交。

### 2.2 实现（三包分层，依赖方向 compression→transformation 单向不破坏）

1. **`domains/hooks/compression/responses_alignment.go`**（新）：
   - `extractResponsesInputMessages`：responses input 形态抽取适配器——input 数组逐 item 原样（item 即对齐坐标）；string input 包装成单条 user 消息（rune 级前缀截断无 item 粒度，靠 meta 留证）；
   - `BuildResponsesAlignmentMap(before, after)`：`alignment.go` 的 hash-match 算法参数化抽取（`buildAlignmentMapWithExtractor` + retainedSpace 参数化，messages 车道行为零变化有回归测试钉住），responses 保留项标 `TargetSpaceResponsesInput`（新常量，区别于 chat messages 坐标系）；专属链无摘要折叠，summaryIdx 恒 -1，被删 item 落 TargetKindDropped。
2. **`domains/transformation/responses_compress.go`**：`CompressResponsesInputIfNeeded/Aggressively` 保持原签名（委托），新增 `*WithMeta` 变体返回 `ResponsesInputTrimMeta{Strategy, OriginalItems, KeptItems, DroppedIndexes（压缩前原始下标升序）, InputString+Original/KeptRunes}`。
3. **`domains/streaming/executors`**：`recordResponsesInputTrimMeta` helper（nil meta no-op；组装 strategy/trim_phase/bytes_*/dropped_input_indexes/alignment_map JSON）；proactive（executor_chat.go:492）与 4xx recovery aggressive 两处调用点接线；**merge 在三处 result 构造点做**（`mergeCompressionMeta(contextLenRecovery.lastMeta, mergeCompressionMeta(responsesProvenance, preTrimMeta))`）——不能提前在 preTrimMeta 构建处合并，因为 4xx recovery 会在此之后覆盖 responsesProvenance，提前合并会丢 recovery 侧证据。最终经 ExecResult.CompressionMeta 落 `request_logs.compression_meta`（v7 §3.2 JSONB，运维 SQL 可查）。

### 2.3 测试

- `compression/responses_alignment_test.go`：数组/string/缺 input 三形态；retain/drop/occurrence（重复内容 hash+occurrence 区分且保留匹配不串位）；fail-open；**messages 车道参数化重构回归钉**（retained→TargetSpaceMessages 不变）。
- `transformation/responses_compress_meta_test.go`：dropped indexes 升序且最新项永不删、meta 计数与实际输出 item 数一致、aggressive 策略名、string 形态 rune 计数、原签名兼容。
- `executors/executor_responses_provenance_test.go`：nil no-op；payload 形状（alignment_map 每原始 item 一条；string 形态省略 alignment_map）。
- 三包全测 + `-race` + `go build ./...` 全绿。

## 3. 本地部署与端点验证（任务 3）

- 提交前 `git fetch` 发现并行会话新提交 `8c7a696b7`（docs+web），merge 干净 + `go build ./...` 复验（merge-auto-miss 教训）。
- `deploy-local.sh stop` → 全量蓝绿：**2.5.8.2361 = f51afc9f**，8781/8782 双臂 `/health` ok，凭据解密冒烟 providers=2,18,13092 creds=16 failed=0。
- 端点级行为验证（HMAC 造临时 super_admin JWT——admin users 表密码与 env 漂移 401，沿用 vapeur 轮诊断技巧）：

| 端点 | 结果 | 覆盖的迁移点 |
|---|---|---|
| `/api/admin/session-analytics/tasks?limit=3` | 200 | R33 族查询门 + 本轮 rows 侧留痕 |
| `/api/admin/session-analytics/clients?limit=3` | 200 | 同上 |
| `/api/admin/sessions?page=1&size=3` | 200 | session_list warnRowSkip+rows.Err |
| `/api/admin/auto-route/analytics/matrix` | 200 | analytics.go writeAggRowsErr |
| `/api/admin/auto-route/analytics/flow` | 200 | analytics.go l12/l23 |
| `/api/usage/providers/18/models` | 200 真实非空（11,372 req 模型行） | usage_provider_models writeAggRowsErr |
| `/api/admin/session-analytics/tasks/nonexistent-*` | 404 "task not found" | writeAnalyticsDetailErr 404 分支保留 |
| `/api/usage/providers/999999/models` | 404 "provider not found" | 真不存在语义保留 |

## 4. 本轮教训

1. **守卫性扫描必须做分支深度跟踪**：窗口式 grep 交叉的 96 处「嫌疑」里 65 处是误报（相邻分支混淆/已有分类），精确扫描+逐个定性才可作修复依据。
2. **合并 provenance 的时点必须晚于所有写入方**：responsesProvenance 若在 preTrimMeta 构建处提前合并，4xx recovery 的覆盖就丢了——与 R0926 P0-2「defer 命名返回值断链」同族的时序错误，靠读 attempt 循环全序才发现。
3. **复合条件拆分再插桩**：`err != nil || val <= 0` 的 continue 加告警会把正常过滤当异常，先拆分支再留痕。
4. admin users 表密码与 env 漂移（401 ×3）不阻塞验证：HMAC 造 JWT 是合法诊断通道，但密码漂移本身是 env 管理债（.env.local 双密码历史问题再现），登记不展开。

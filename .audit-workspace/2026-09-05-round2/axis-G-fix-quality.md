# 轴G：八项闭环修复质量复核（审计审计者）

- 日期：2026-09-05
- 复核对象：646b32ff1（闭环1/2/7）、7efdaaf15（闭环3）、b08a2a20d（闭环6/4）、0ea1d5e98（闭环5）、f0447666c（streaming panic）、09796c69f（闭环8）
- 方法：逐项读修复代码 + V371/657 迁移比对 + 定向测试（只读验证，未改任何源码）
- 输入文档：docs/audit-2026-09-05-eight-closures.md

## 总评

| 闭环 | 修复验证结论 |
|---|---|
| 1 | 有缺口：写入/聚合/读端链路成立，但 stats 主路径对 hour/day 恒空、tenant 过滤缺失、UTF-8 截断可致插行失败 |
| 2 | 成立：fold 恢复路径与去重正确，诊断字段/脱敏到位 |
| 3 | 代码层成立、**部署层断裂**：657 迁移是孤儿文件，生产库无列则契约静默失效 |
| 4 | 成立但潜伏数据丢失陷阱：Reconcile 读-读窗口竞态可让 Repair 误删合法 body |
| 5 | 未复核运行态（真实 PG 门控测试，离线 skip；迁移缺陷修正与审计自述一致） |
| 6 | 成立：取消路径修对了；watcher 退出单腿依赖 ctx.Done 是设计脆弱性；泄漏门禁仅覆盖 1/3 桥接路径 |
| 7 | 成立：池化无双重持有/逃逸；`-race` 通过 |
| 8 | 成立：后端校验无绕过面；前端徽标词表与后端 ErrorKind 不一致（context_length 键名错） |
| f0447666c | 成立：guard 正确，同类越界扫描通过 |
| 其它修复 | 全部成立 |

发现计数：**P0=0，P1=1，P2=5，P3=10**。

定向测试实测输出：

```
go test ./domains/nodestatecache/ -race -count=1                          → ok  1.607s
go test ./domains/streaming/... -count=1 -run 'Cancell|Fault|Fold|Candidate' → ok
go test ./storage/... -count=1                                            → ok
go test ./durable/... -count=1                                            → ok  0.5s
```

---

## 闭环1：supplier_errors 事实源（646b32ff1）

**修复验证结论：有缺口（主体成立）。**

核对通过的点：
- 列数/类型匹配：`supplierErrorInsertSQL` 19 列 = 18 个参数 + 字面量 1（affected_users），与 V371 `supplier_errors_hot` 定义一致；`Retryable` 在 buildRow 恒非 nil（candidate_failure_logger.go:261-262），`UpstreamStatusCode/LatencyMs` 为可空指针，与 NOT NULL/nullable 列约束无冲突。
- 低基数守卫 `lowCardinalityContextValue`（>64B 或含空白即丢弃为 ''）符合「宁缺毋污」原则。
- 唯一键 `uq_supplier_error_stats_bucket(stat_time,granularity,supplier,credential_id,error_type,model)` + ON CONFLICT DO UPDATE 幂等；聚合器重算最近两个窗口覆盖迟到行。
- SanitizeErrorText 为 Go RE2（线性，无 ReDoS）。

### G-#1 [P2] stats 主路径对 hour/day 粒度永远为空，默认 24h 视图恒走明细兜底
- 证据：`bg/supplier_error_stats_aggregator.go:114-131` 只写 `granularity='minute'`；`admin/errors_trend.go:82-89` 默认 hours=24 → granularity='hour'，`loadFromStats`（:171 `WHERE granularity=$1`）恒 0 行 → :121-132 恒 fallback 到 `supplier_errors_unified` 明细扫描。
- 影响：stats 预聚合对**默认视图**完全没省掉明细扫描（设计目的落空），只对 hours<=1 的 minute 视图生效；「stats 主路径 + 兜底」的闭环1 卖点打折。
- 最小修复：聚合器对 minute 桶之上再 rollup hour/day 桶（同一 UPSERT 模式，各一条 SQL），或 trend API 的 hour/day 查询改为对 minute stats 再聚合。
- 工作量：S

### G-#2 [P2] trend API 租户过滤缺失，且全仓无任何代码设置 app.tenant_id（RLS 双态皆不可用）
- 证据：`admin/errors_trend.go:108` 读 `EffectiveTenantIDAll` → 填入 `statsQuery.TenantID`（:113/:124），但 `loadFromStats`（:163-176）与 `loadFromDetail`（:203-220）的 SQL 都没有 tenant 条件；过滤完全依赖 V371 RLS `USING (tenant_id = current_setting('app.tenant_id', true))`，而全仓 Go 代码 grep `app.tenant_id` 仅 apihub/service.go 注释一处。RLS 未设置会话变量时：非 superuser 池用户恒读到 0 行（聚合器 rollup 也读不到 → stats 永远空）；superuser 池用户则完全绕过 RLS → 跨租户数据在 admin API 可见。对比同轮修复的 `admin/vendor_credential_error_handlers.go:178/:229` 显式带了 `($2='' OR tenant_id=$2)`。
- 影响：多租户部署下跨租户泄露（superuser 池）或功能整体空转（非 superuser 池）。
- 最小修复：loadFromStats/loadFromDetail 补 tenant 条件（照 vendor_credential_error_handlers 模式）；中期在池层 `connection_acquire` 里统一 `SET app.tenant_id`。
- 工作量：S（SQL 条件）/ M（池层会话变量）

### G-#3 [P2] SanitizeErrorText 字节截断可切破 UTF-8 → 整行 INSERT 被 PG 拒绝
- 证据：`errorsx/sanitize.go:40-42` `out = out[:maxBytes]` 按字节截断；中文供应商错误消息（3 字节/rune）在 320/512/256 边界高概率切出非法字节序列；PG 对 UTF8 库的 text 列报 `invalid byte sequence` → `supplier_errors_hot` 该行 insert 失败（仅 warn，supplier_error_logger.go:99-106）。`supplierErrorMetadata` 路径因 json.Marshal 把非法字节替换为 U+FFFD 而幸免——两条路径行为不对称。320 字节截断在 buildRow（candidate_failure_logger.go:209）是既有行为，但新事实源表原样继承并叠加 512 二次截断。
- 影响：含 CJK 的上游错误（国内供应商常态）按概率丢行；趋势/凭据详情数据静默偏差。
- 最小修复：截断后 `strings.ToValidUTF8(out, "")`（或按 rune 回退到边界）。
- 工作量：S

### G-#4 [P3] 双写两表共用同一个 3s ctx
- 证据：`domains/streaming/executors/candidate_failure_logger.go:157` 建 ctx，:160 第一条 INSERT，:178 `persistSupplierError(ctx,...)` 复用。第一条耗尽预算时第二条必然失败 → 旧表有、新表无的中间态（读端已切 supplier_errors_hot）。
- 建议：第二条用独立 3s ctx。工作量：S

### G-#5 [P3] occurred_at 死代码
- `supplier_error_logger.go:62-66` 读取 `row.Context["occurred_at"]`，全仓无写入方，恒走 COALESCE→NOW()。删掉或补写入方。

### G-#6 [P3] metadata 嵌套结构未脱敏
- `supplierErrorMetadata`（supplier_error_logger.go:120-129）只对顶层 string/error 脱敏，extra context 里的嵌套 map/slice 内字符串原样入库。当前调用方 extras 均为标量，属防御纵深缺口。

---

## 闭环2：候选错误诊断字段 + foldCandidateOutcomes（646b32ff1）

**修复验证结论：成立。**

- fold 去重正确：`execute_attempt.go:149-179` —— `execErr.Attempts` 非空走 Attempts 分支；为空才从 `tracker.FailedAttempts()` 恢复；两者互斥不会双计；两者皆空回落合成单候选。`FailedAttempts()`（routing_tracker.go:115-132）正确排除 pending 占位与 success，且返回拷贝（无共享底层切片）。
- 诊断字段补齐：CandidateOutcome/RoutingAttempt 结构对齐，tracker 分支的 Supplier/HTTPStatus/LatencyMs/Stage 映射完整。
- 非法 CandidateID 的防御：`candidateCredential` 解析失败走 warn + 0，ActionNode 跳过无效条目（attempt_outcome.go:283-297）。

附随小问题（不影响闭环成立）：
- G-#8 [P3] 合成候选（CredentialID=""）每 attempt 触发一条 `survival: invalid candidate credential id` warn——夜间无候选常态下是稳定日志噪音，建议合成条目静默跳过或降 Debug。
- G-#8b [P3] tracker 分支 `AttemptSeq: fa.Seq` 继承了 pending 占位的编号（handler 预填候选池在先），seq 有空洞，与字段注释「1 起按 fold 顺序」不符（诊断保真问题）。

---

## 闭环3：durable DecisionHistory（7efdaaf15）

**修复验证结论：代码层成立；部署层断裂（P1）。**

核对通过的点：
- fencing 条件更新正确：`durable/store_decision_history.go:46-59` `WHERE id AND lease_owner AND fencing_token`，0 行 → ErrLeaseLost，旧持有者无法覆盖新持有者历史；调用侧 ErrLeaseLost → noteLeaseLost 双指标（durable_recovery_worker.go:504-510）。
- 128 条有界裁剪方向正确：`attempt_outcome.go:271-273` 保留**最新** 128 条（`[len-128:]`）。
- JSON 损坏退化：`loadHistory`（durable_recovery_worker.go:477-492）unmarshal 失败 → 空历史 = 迁移前行为，正确 fail-open。
- SaveDecisionHistory 失败不影响本判定：`aggregateAndPersistHistory`（:497-513）先算 decision 再保存，错误只记日志/指标。
- 历史读取时机正确（RenewLease 前、attempt 前），lease 丢失中途退出时不回写。

### G-#7 [P1] 迁移 657 是孤儿文件：运行时与安装器都不会创建 decision_history 列
- 证据：`sql/migrations/startup/657_durable_llm_tasks_decision_history.sql` 存在且自带 post-condition 校验，但：
  1. 运行时迁移入口 `db/db.go ApplyMigrations` 是**硬编码 ensure\* 函数**集合（如 655 对应 session_summaries_schema.go:199、631 对应 ensureProviderSoftDelete:838），grep `decision_history` 在 db/、cmd/、installer/ 全部零命中；
  2. 安装器嵌入清单 `installer/cmd/llm-gw-installer/embeddata/startup/` 最新只到 `656_auto_route_selections_hot.sql`，无 657；
  3. `sql/migrations/startup/` 目录本身没有运行时目录扫描加载逻辑。
- 影响：生产/安装器部署的 PG 库 `durable_llm_tasks` 无该列 → 每次 attempt 结束 `SaveDecisionHistory` 报 `column does not exist`（每 attempt 一条 warn 噪音），跨重启循环检测**静默失效**——闭环3 的核心契约（「跨进程重启续写」）在部署环境为空转。代码 fail-open 所以不崩、不影响终态判定（这是它只评 P1 而非 P0 的原因），但「修复已闭环」的声明在运行态不成立。单测通过是因为测试 fake 自建 schema。
- 最小修复：`db/db.go` 增加一条 `ALTER TABLE durable_llm_tasks ADD COLUMN IF NOT EXISTS decision_history JSONB`（照 655/631 的 ensure 模式），同时把 657 补进 installer embeddata。
- 工作量：S

---

## 闭环4：lite 跨介质一致性（b08a2a20d）

**修复验证结论：成立（原语+测试），附潜伏数据丢失陷阱。**

核对通过的点：
- 「绝不伪造」约束：`storage/consistency.go:118-121` MissingBodies 任何策略只报告，Repair 仅处理 OrphanBodies ✓。
- 路径遍历防护：`storage/file/bodies_store.go:62-64` validPathID 拒绝空/`.`/`..`/含 `/\`；turnNo 为 int 格式化无注入面；ListTurns/DeleteTurnFile 均校验 ✓。
- 写序约定（body 先于 meta）在 `cmd/gateway/lite_telemetry_sink.go:140` 注释钉死 ✓。

### G-#9 [P2] Reconcile 读-读窗口竞态：Repair 可误删刚落盘的合法 body（潜伏数据丢失）
- 证据：`storage/consistency.go:76-83` 先读 meta 集合再列 body 集合，两次读取之间完成「body 落盘 → meta 提交」的 turn 会被判为 OrphanBodies（body 有、meta 快照无）；`RepairTurnArtifacts:125-135` 随即删除该 body —— meta 紧随提交后即变成 b 类丢失（内容永久丢失）。无 mtime 宽限、无删除前 meta 复检。写序（body 先、meta 后）恰好使这个窗口落在最危险的方向上。
- 现状缓解：全仓无生产调用方（仅测试引用），属「原语就绪、待接线」状态，故 P2 而非 P1；一旦有人把 Repair 挂上周期任务，这就是线上数据丢失。
- 最小修复：删除前对每个 orphan (a) 复检一次 turn meta，(b) 要求文件 mtime 早于宽限期（如 10min，大于 meta 提交最坏延迟）。
- 工作量：S

### G-#10 [P3] ListTurns 文件名前缀误收
- `bodies_store.go:222` `fmt.Sscanf(e.Name(), "turn_%d.json.gz", ...)` 不要求整串匹配，`turn_12.json.gz.tmp`/`.corrupt` 残留会被计入 turn 12。建议解析后校验完整名。

### G-#11 [P3] Repair 静默 no-op
- `consistency.go:129-134` bodies 不实现 TurnFileDeleter 时循环体直接跳过，无错误无报告。应返回错误或在报告中标注 skipped。

---

## 闭环6：SSE 可取消 / 泄漏门禁（b08a2a20d）

**修复验证结论：成立（取消路径修对了），watcher 生命周期存在单腿依赖。**

核对通过的点：
- 取消语义：forceClose → 底层 Close 解除 Read 阻塞 → 读循环 ctx 分支给出 client_cancel；`TestAnthropicSSEPassthroughCancellationStopsReader`（2s 硬门）与 `CancelBeforeFirstFrame` 钉死及时返回 ✓。
- Close 幂等：closeBody/stopOnce 双 Once，裸 Close 与 watcher forceClose 并发安全（net/http body Close 有锁）✓。
- 双重 watcher：三处（anthropic x2、responses x1）均包装 transport 原始 body，无叠加包装 ✓。

### G-#12 [P2] watcher 实际只能靠 ctx.Done 退出——桥接层 defer 关的是裸 body，wrapper.Close 永不被调用
- 证据：`domains/streaming/anthropic_bridge.go:220/:887` 与 `responses_bridge.go:431` 用 `newCtxCancellableBody(ctx, resp.Body)` 包装，但同函数的清理是 `defer resp.Body.Close()`（anthropic_bridge.go:135/:715）——关的是**裸** body，wrapper 的 `stop` 通道永不关闭、`closeBody` 不经过 wrapper。cancellable_body.go:16 声称「ctx.Done 或显式 Close 任一发生即退出」，实际调用方只走了 ctx 一条腿。
- 现状：前台请求 ctx 在 handler 结束时取消、durable attempt 有 `defer cancelAttempt()`（durable_recovery_worker.go:375），因此**当前无现行泄漏**；但代码库已有 `context.WithoutCancel` 模式（survival_wiring.go:214，且 worker Stop 注释自认「流式上游可能不响应取消」），任何未来调用方传入永不取消的 ctx → 每个流恒漏一个 goroutine，且现有泄漏门禁测不到（测试自己 cancel 了 ctx）。
- 最小修复：桥接层把包装后的 body 存回 `resp.Body`（`resp.Body = newCtxCancellableBody(...)`），让既有 `defer resp.Body.Close()` 自然走 wrapper——一行改动同时修复退出路径与泄漏面。
- 工作量：S

### G-#13 [P2→P3 附件] 泄漏门禁覆盖面
- `anthropic_bridge_fault_test.go:93-137` 的轮次增长门禁（±5 容差 + 3×200ms 重采）对当前代码有效（每轮显式 cancel 使 watcher 退出），同机负载自校准设计合理；但只覆盖 passthrough 单条路径，`StreamAnthropicSSEToOpenAIWithDiagnostics`/`ToResponses` 两个接入点无同等门禁，也测不到「ctx 永不取消」类回归。建议补齐两路径 + 一个 WithoutCancel 场景用例。

---

## 闭环7：Select 热路径池化（646b32ff1）

**修复验证结论：成立。**

- 测试：`go test ./domains/nodestatecache/ -race -count=1` → ok 1.607s。
- 双重持有/逃逸：`selector.go:268-312` `poolOwned` 标记保证 Scorer 返回的切片不入池（防实现方内部缓冲被双重持有）；`Alternatives` 每次 `make` 新切片（:297），不别名池化缓冲，调用方持有无越界/被复用风险 ✓；`survivors` 在所有返回路径归还（:207-229 含 fallback 循环后的统一归还）✓。
- 小 Used≤8 线性比较（:160-169）正确：跳过 `NodeID<=0` 的无效记录，语义与 map 路径一致 ✓。
- G-#14 [P3] Scorer 契约「survivors 仅本次调用有效，不得保留引用」仅是文档约束（selector.go:202-203 注释自认）；违反即跨 Select 数据竞争，单测 race 检测不到。可在 debug 构建加 after-last-use 毒化校验。

---

## f0447666c：全候选耗尽 candidates[0] 越界

**修复验证结论：成立。**
- guard 正确（handler.go:4827-4835）：空候选时 exhaustedProviderID/CredentialID 置空串，与 failureAttribution 先例一致；恢复「500 → 干净 503」的原始意图。
- 同类越界扫描通过：handler.go 其余 `candidates[0]` 站点均有守卫（:3171 在 `top==1` 且 `top = 1` 仅当 `len>0`；:3286/:4971/:5140 在 `len(candidates)>0` 内；:4436 `AutoFallbackModels[0]` 在 `len>0` 内）；messages.go:569-/responses.go:555- 同构守卫 ✓。

---

## 闭环8：前端统一与远程菜单校验（09796c69f）

**修复验证结论：成立（后端校验无绕过面），前端词表有错位。**

- 后端 `plugin-runtime/nav_validate.go`：白名单正则 ASCII 小写段（拒绝绝对路径、`..`、反斜杠、`//`、协议片段、百分号编码、Unicode 同形字、超长）；fail-closed 接线确认（manifest.go:90 校验失败 = manifest 无效）；`tenant_only && platform_ops` 互斥拒绝 ✓。校验通过后 registry.go:120 才拼 `/plugins/{id}/{path}`，注入面闭合。
- G-#15 [P3] 前端徽标词表错位：`web/src/components/RoutingAttemptsTimeline.vue:44` 键 `context_length`，后端实际值是 `context_length_exceeded`（errorsx/classify.go:56）→「上下文超限」徽标永远不命中，走 fallback 显示英文原文；另有 14 个后端 kind（concurrent/empty_response/quota_periodic/quota_balance/quota_permanent/model_deprecated/unsupported_feature/conversion_error/upstream_context_loss/tool_call_id_mismatch/client_bug/no_available_channel/circuit_open/fp_slot_saturated）无中文词条。建议从 errorsx/classify.go 生成共享词表或加一个词表 parity 测试（后端 kind 常量 ↔ 前端 map 键）。
- G-#16 [P3] 文案/一致性小疵：nav_validate.go:79 错误信息写 `[a-z0-9._-]` 但正则不允许 `.`；Timeline 组件硬编码中文与 `t()` i18n 混用。

---

## 本轮其它修复

**结论：全部成立。**

- **keystore_sync.go 锁重构**：`keyStoreSyncDelta` 先收集 invalid 哈希再短暂持锁删除（keystore_sync.go:227-246），不再持 storeMu 迭代 DB 行；「先 upsert 后删除」的顺序使 revoke 竞态收敛到正确终态 ✓。
- **cache_v2_file.go 记账自愈**：溢出路径全树 Walk 后以磁盘实况重置 sizeUsed，healed 分支避免对旧文件的重复扣减（cache_v2_file.go:226-234、302-323）；「写成功才记账、删成功才扣减」+ 锁内复检防误删 ✓。
- **auto_route_settle_worker.go hot-only**：4h abandon < 8h hot retention，读写全部限定 `_hot` 表，注释明确禁止引入 columnar parent（:205-231）✓。
- **handler_lite_readiness**：lite 模式 nil db/redis = ready（main.go:768 接线 SetDepsOptional），配置了但宕掉的 redis 仍 503 fail-closed，四条测试钉死双向语义 ✓。
- **selection_writer panic guard**：recover 防 crash 与 Stop 永久挂起 ✓。
  - G-#17 [P3] panic 后 writer goroutine 永久退出，后续行全部 drop（仅计数与单条日志），无自愈重启；建议 panic 后重建循环或暴露 healthy 指标。

---

## 冗余 / 待清理

1. `AggregateTaskOutcome`（attempt_outcome.go:458-551）——已声明为兼容 API，生产调用方清零，随决策矩阵测试迁移后删除。
2. `supplier_error_logger.go:62-66` occurred_at 读取——无写入方的死代码（G-#5）。
3. `candidate_failure_logs_hot` 双写——迁移期过渡，读端已切换；待 promote 窗口（8h+）过后按 V359 治理模式下线（审计文档已记）。
4. 迁移三轨制：`deploy/sql/migrations/`（V 系列 golang-migrate）、`sql/migrations/startup/`（数字系列，仅测试/文档镜像）、`installer/.../embeddata/startup/`（安装器嵌入）——G-#7 正是掉进这个坑；建议明确唯一权威源并加「startup 序号与 embeddata 一致性」CI 检查。
5. `attempt_outcome.go` 每次失败 attempt 构造 candidateSummary/attemptSummary 诊断 map（AggregateTaskOutcomeWithHistory / foldCandidateOutcomes）——热路径分配，可降 Debug 或条件化。

## 建议优先级排序

1. G-#7（P1，S）：补 657 运行时 ensure + installer 嵌入——不做则闭环3 等于没上线。
2. G-#12（P2，S）：`resp.Body = newCtxCancellableBody(...)` 一行改动消除 watcher 单腿依赖。
3. G-#9（P2，S）：Repair 前 meta 复检 + mtime 宽限——在原语被接线前堵住数据丢失陷阱。
4. G-#3（P2，S）：ToValidUTF8 修复 CJK 截断丢行。
5. G-#1/G-#2（P2）：trend API hour/day rollup + tenant 条件。

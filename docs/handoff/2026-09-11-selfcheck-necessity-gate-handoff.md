# 自检必要性检查（necessity gate）交付与交接

日期：2026-09-11
分支：feat/selfcheck-necessity-gate → main（fast-forward）
状态：已合入 main 并推送；本文件记录遗留项与后续方案。

## 已交付内容

对进入 `credential_probe_queue` 的自动 `node_probe`（自检）任务，在执行前进行必要性检查（`bg/probe_necessity.go`，接入点 `ProbeService.Run`，位于租约心跳与探测轮次之前）：

- 条件①：当前模型在 Redis（URSM 节点哈希）**有可判定状态且健康**，且同一凭据下所有其它节点状态正常（健康或无缓存条目）→ 跳过。
- 条件②：`node_probe_runs` 最新已完成记录 success=true，且此后 Redis 无错误请求（`last_request_error_at_ms` 水位不晚于该次探测完成时间）且当前状态仍健康 → 跳过。
- 任一条件满足：不执行探测；硬删除 `credential_probe_queue` 行（租约保护，先删队列行证明所有权，成功后才删 `node_probe_state` 镜像并发布 SSE 终态事件）；打点 `llmgw_node_probe_necessity_skip_total{reason}`。
- 失败姿态：证据读取失败/超时（3s 上限）/当前节点状态缺失 → 一律 fail-open 照常探测；手动（admin）任务不走闸门。

配套：URSM v2 `Manager.ProbeHealthEvidence` 只读证据 API、`record_request{,_dual}.lua` 的 `last_request_failed` / `last_request_error_at_ms` 水位、`ursmV2EvidenceSource` 适配器在 main.go 两处 `ProbeService` 构造点接线。

测试：`bg/probe_necessity_test.go`（初版 16 例；随跟进项 2、3 扩至 22 例：两条件正/反例、当前节点无状态/不可解码 fail-open、证据错误 fail-open、手动绕过、闸门未接线禁用、worker 不 Complete（completeFn 缝断言）、Remove SQL 守卫、skip 移除编排 4 例、镜像删除 churn 3 例）。全部通过；`bg` 全量套件在 Windows 本地验证的预存环境性失败清单见下方"本轮记录"验证段（CRLF 源文本断言 + symlink 权限，与功能无关）。

## 遗留任务（按优先级）

### 已完成的跟进项

1. **P2：`ProbeHealthEvidence` 批量化**（`domains/ursm/v2/store/probe_evidence.go`）— **已完成（2026-09-11，dc8463e28；同日审计修正）**
   已改为 Redis Pipeline（复用 `redissafe.SafeHGetAllPipeline`，键选择规则与 `PipelineNodeViews` 一致）：全部主键一次批量调用，dual 模式 miss 的键再有一次 legacy 回退批量调用。成本口径（审计修正）：每次 `SafeHGetAllPipeline` 内部是 TYPE、HGETALL 两段 Exec，因此 legacy/canonical 模式最多 2 次、dual 全量回退最多 4 次 pipeline Exec——此前"≤2 轮"表述不准确；相对逐模型串行 1+2×500 次 RTT 仍是数量级改善。测试覆盖批量混合来源（k2-only/legacy-only/双写/缺失）、legacy-only tuple、501 规模（1 current + 500 sibling 真实极值）。

2. **P2：`removeSkippedProbe` 分支测试缺口** — **已完成（2026-09-11，5367d6f84）**
   `ProbeQueue` 的移除面收窄为 `probeSkipQueue` 接口缝（`Remove` + `publishRemovedTransition`，因 `ProbeQueue` 的 pgxpool 无法用 sqlmock 构造），镜像删除加 `deleteStateFn` 函数缝。新增 4 例：Remove 报错只停在队列行、removed=false（租约丢失）不碰镜像/SSE、queue/lease 缺失三副作用全不发生、成功路径编排顺序 remove→deleteState→publish 及 SSE reason 前缀断言。顺带修复 typed-nil 接口陷阱（`skipRemovalQueue` 须把 nil `*ProbeQueue` 显式转成 nil 接口，否则落入 Remove 的 unavailable-database 分支）。

3. **P2：镜像行删除失败的 churn 抑制** — **已完成（2026-09-11，5e65c21f1，feat/necessity-p2-churn-suppression）**
   `deleteNodeProbeState` 失败 → pump 30s 重入队 → 闸门再跳过 → 再删 的循环已用失败测试稳定复现（一次瞬时删除失败 = 2 轮完整 skip 生命周期 = 磁贴翻动 2 次）。修复：删除失败后**同轮有界即时重试一次**（200ms 延迟，吸收锁等待/序列化失败/瞬时连接抖动）；仍失败则落 `llmgw_node_probe_necessity_mirror_delete_failed_total` 计数 + ERROR 日志（pump 重入队即天然的重试安全网，绝不执行探测）；ctx 中途取消不再发起重试但也计入持续失败。硬条件全部保持：重试在租约所有权证明（队列行删除成功）之后才发生；闸门决策路径零改动（不漏探、证据错误仍 fail-open、manual/admin 仍绕过）。测试 16→22 例（churn 复现、持续失败有界+计数、ctx 取消三例新增），详见下方"本轮记录"。

4. **P3：missing-binding 丢弃路径的同类 churn 面** — **已完成（2026-09-11，本轮，feat/necessity-p3-binding-pump-gate；第五轮审计扩展，381fbf50d 及审计提交）**
   只读可达性确认：pump 不查绑定链——`pumpDueStatesSQL` 只带 credential/provider 资格门，而 credential_recovery 全部路径（`reconcileStaleNodeProbeStateSQL`、`lookbackCandidateSQL`、expired/fresh-degraded 绑定恢复）都以 cmb/pm 绑定链为锚（第五轮审计修正："pump 唯一"不成立——`DailyProbeAudit` 的 candidate_failure_logs 分支是第二个 24h 低频绑定盲区，已随审计一并过滤，见下方审计记录）。绑定链断裂（无 cmb 行 / 悬空 provider_model_id / pm.raw_model_name 改名）的镜像行会被 pump 反复入队：每轮真实执行探测轮次 1（endpoint build 阶段失败）→ best-effort 删镜像 → 删除失败则行残留、下个 holdoff 周期再循环（与 P2 同形，但无重试也无计数）。修复采用**结构性断源**（pump 资格过滤，而非复用重试编排）：`pumpDueStatesSQL` 的 WHERE 并入绑定链 EXISTS（`credential_model_bindings cmb JOIN provider_models pm`，双列关联防漂移；刻意只查存在性、不加 available/manual 门——resolveDirectTarget 对仅存在的绑定即可探测，更严会把可探测对挡在 pump 外；绑定重建后行自然重回 pump，与资格门重入语义一致）。missing-binding 分支注释同步改写（一次性自愈语义，不再"经 pump 重入队"）；恢复路径与 manual/admin 零改动。新增守卫测试 `TestPumpDueStatesSQLFiltersMissingBindingPairs`（先红后绿），详见"本轮记录（第四轮）"。

### 未完成的遗留任务

5. **P3：`lastProbeRun` 排序语义** — **决策：保持 `started_at DESC` 不改（2026-09-11 确认）**
   同一 dedup_key 不允许并发 ready/running，实践中与 `completed_at DESC` 等价且索引友好；未发现任何真实反例，此项按原方案关闭，仅在出现乱序完成记录的反例时重开。

6. **运维观察项（上线后）**
   观察：`llmgw_node_probe_necessity_skip_total` 增速、自检 tab "skipped_not_necessary" 磁贴占比、以及 `llmgw_node_probe_necessity_mirror_delete_retry_total`（瞬时失败吸收量）与 `llmgw_node_probe_necessity_mirror_delete_failed_total`（**持续失败告警源**）。若 failed_total 持续增长（说明某 (cred,model) 的镜像 DELETE 长期不收敛、磁贴仍按 pump 周期翻动），按原方案升级：对刚被跳过的 (credential_id, raw_model) 引入短 TTL 抑制（pump 侧或闸门侧均可，抑制窗口必须短于真实故障的重探测需求）。若 skip 率异常高，优先排查 Redis 键 schema（legacy/k2/dual）与 tenant 归属是否一致。**抓取命令与判读规则见"本轮记录（第六轮）"的观测 runbook。第八轮已补 `NodeProbeNecessityMirrorDeleteFailedHigh` 告警与 `selfcheck-necessity-dashboard.json` 面板（随仓库交付，运维按 deploy/prometheus/README 的 provisioning 流程加载后，failed_total 持续增长会自动告警，不再依赖人工抓取）。**

7. **工作区遗留脏文件（非本特性，未处置，需原作者确认）**
   - `D docs/02-resources/research/pricing/scripts/vendor_pricing_table.py`
   - `D scripts/deploy-lib`
   - `M docs/archive/process/incidents-collection/2026-08-31-154-blue-green-deploy-recovery.md`
   - `?? docs/02-resources/research/pricing/scripts/vendor_pricing_table.py.lnk`
   - `?? scripts/deploy-lib.lnk`

   严禁 `git add -A` / `git clean` / 恢复或删除上述文件。另外：主工作区检出的 `fix/r13-logging-hygiene` 分支属于并行的 R13 日志工作（含未合并提交），与本特性无关，不要在其上叠加 necessity gate 改动。

## 审计记录（2026-09-11 第二轮，针对 dc8463e28 批量化）

- **实现审计：未发现正确性缺陷。** k2/legacy/dual 选键与 `PipelineNodeViews` 逐分支一致；`ErrKeyNotFound` 以外的单键错误（含 WRONGTYPE）直接返回、不被 fallback 掩盖，闸门据此 fail-open；`primaryIndexes`/`pending` 保证结果与输入顺序一致（含重复模型）；批内统一 `now` 只影响一致性、不改变解析语义。
- **本轮修正（随本提交）：**
  - 大批量测试从 500 修正为 **501**（闸门真实极值 = 1 current + 500 sibling）；
  - 新增回归：dual 模式 canonical WRONGTYPE 不被 healthy legacy fallback 掩盖、legacy fallback WRONGTYPE 传播为错误；
  - 钉住 dual 模式空 tenant（k2 不可表达）直接读 legacy 的行为；
  - 修正函数注释与本文档的管道 Exec 轮次表述。
- **记录在案的接受风险（未改动，属后续可选加固）：**
  - TYPE→HGETALL 两段之间 canonical/legacy 存在极窄 TOCTOU 窗口（写入方为原子 Lua 脚本，且 60s 恢复扫描会重新入队，偏 safe 方向）；如需根治可改为单脚本原子读；
  - dual 读路径对 `K2KeySetForTenant` 的非"空 tenant"错误（如空 model / 非正 credentialID）同样回退 legacy；实际调用方（闸门，来自队列行）不产生此类 tuple；
  - `health` 字段在闸门 Healthy 谓词中按保守语义参与（非 healthy 即触发探测），与 NodeView 的 display-only 定位不同，但方向是多探不漏探，保持现状；
  - `schemaMode` 为 boot-only 可变字段，运行期改写存在数据竞争面，建议后续构造期固化并在 setter 校验枚举值。
- **验证记录：** 见下方"验证环境说明"；本轮在 C: 本地 worktree 以便携 Go 执行 `go test -mod=vendor -count=1 ./domains/ursm/v2/store ./internal/redis`，结果随附于提交说明。限制：`bg`/`cmd/gateway` 在部分 Windows 环境因 vendored `onnxruntime_go` 构建约束不可编译；win/arm64 不支持 `-race`，未跑。

## 本轮记录（2026-09-11 第三轮，P2 镜像删除 churn 抑制）

- **根因链（只读梳理）**：`removeSkippedProbe` 的顺序是队列行删除（所有权证明）→ `deleteNodeProbeStateMirror` → SSE。镜像 DELETE 一旦失败，行内只留一条 Warn（`worker.deleteNodeProbeState` 吞错返回 void，seam 也无法表达失败），`node_probe_state` 行残留；pump 的 `pumpDueStatesSQL`（paused=FALSE 且 next_retry_at 到期）在下个 30s tick 重入队 → 闸门再次判定可跳过 → 队列行再删、镜像再删。每轮循环磁贴完整走一遍 pending → in-flight → skipped。瞬时 DB 故障（锁等待/死锁/序列化失败/连接抖动）即触发至少两轮翻动；持续故障则无限循环。
- **失败测试先行**：`TestSkipMirrorDeleteTransientFailureRetriedWithinCycle` 用 seam 充当数据库模型（失败=行残留），循环驱动 `Run` 模拟 pump 周期。改动前实测 **2 轮 skip / 2 次磁贴翻动**（红），改动后同轮重试收敛为 1/1（绿）。
- **修复形态**：`deleteNodeProbeStateMirror` 内置"失败 → 200ms（`probeNecessityMirrorDeleteRetryDelay`，遵守 ctx 取消）→ 即时重试一次 → 仍失败落计数+ERROR"。两个计数器：`llmgw_node_probe_necessity_mirror_delete_retry_total`（同轮重试发生量）、`llmgw_node_probe_necessity_mirror_delete_failed_total`（重试耗尽或 ctx 中断，持续失败告警源）。`deleteStateFn` seam 从 void 改为返回 error（重试对 seam 同样生效，测试可见）；`worker.deleteNodeProbeState` 同步返回 error（missing-binding 调用点显式 `_ =` 保持原 best-effort 语义）。
- **硬条件核对**：重试只在 `Remove` 返回 removed=true 之后发生（租约丢失/队列行不存在时连首次删除都不会发生，新 owner 的镜像与 SSE 不受影响——既有 4 例编排测试原样通过）；闸门判定路径零改动——不漏探、证据错误 fail-open、manual/admin 绕过全部原样；TTL 抑制本轮未引入（按任务书"仍不收敛再引入"，升级路径与判据见遗留项 4）。
- **验证记录**：C: 本地打桩副本（`lgw-p2test`，robocopy 自本 worktree + `diskUsagePercent` 桩）执行 `go test -mod=vendor -count=1 ./bg/`：定向 22 例全绿；全量套件 17 例失败经 **HEAD 基线对照实验确认与改动前完全一致**（16 例源文本断言测试在 CRLF 检出下失配 + 1 例 symlink 权限，均为预存环境限制，Linux CI 不受影响）。`GOOS=linux GOARCH=arm64` zig cc 交叉编译 `./bg/... ./cmd/gateway/...` 通过。win/arm64 不支持 `-race`，未跑（与既往轮次相同）。
- **范围界定**：`lastProbeRun` 保持 `started_at DESC`（无反例，按原方案关闭）；missing-binding 丢弃路径的同类风险记录为遗留项 5，未改动。

## 本轮记录（2026-09-11 第四轮，P3 missing-binding pump 过滤）

- **任务选择说明**：上一轮提示词给出 A（上线观察）/ B（遗留项 5）二选一按数据定。本轮执行 B：B 的第一步（只读可达性确认）不依赖生产数据，且分析结论为**结构性成立**（不依赖任何运维偶然条件），故直接实施断源修复；A 的上线观察仍完整保留给部署后（遗留项 6）。
- **可达性分析（只读）**：三条入队来源逐一核对——
  1. pump（`pumpDueStatesSQL`）：只带 credential/provider 资格门，**不查绑定链**；且对已资格过的行（status=active + lifecycle=active + provider enabled + 非手动停用），`resolveDirectTarget` 严格查询的 credential/provider 门全部满足，其"no rows"只剩绑定链断裂一种成因（宽松重试不改变结论，仅 c.status / c.lifecycle_status IS NULL 的边缘经宽松查询成功、不落入 missing-binding；第五轮审计就地修正此处表述）。
  2. credential_recovery：全部绑定锚定——`reconcileStaleNodeProbeStateSQL` JOIN cmb+pm 且 cmb.available=TRUE，`lookbackCandidateSQL` FROM cmb 起步，expired/fresh-degraded 恢复直接以 cmb 行为对象。
  3. 手动/管理任务：绕过资格与闸门（操作员在场，一次性）。

  结论：绑定链断裂（无 cmb 行 / 悬空 cmb.provider_model_id / pm.raw_model_name 改名）的 `node_probe_state` 行，唯一循环调度源是 pump。每轮循环真实执行探测轮次 1（endpoint build 失败，无出站 HTTP）+ 1 条审计行 + 完整磁贴生命周期，删除失败则下个 holdoff 周期再来——与 P2 skip 路径同形，但无重试、无计数。
- **方案取舍**：二选一选了 **pump 资格过滤**而非复用 `deleteNodeProbeStateMirror` 编排——过滤把注定失败的任务挡在调度外（顺带消除每次 claim + 审计行 + 磁贴翻动的浪费，且这类浪费在删除成功时同样发生），重试编排只治症状；P2 的两个计数器语义锚定 skip 路径（Help 文本写明 "necessity skip path"），复用需改名重定语义，收益不成比例。**残余面**：missing-binding 分支删除失败时留下单条孤儿行，pump 不再重提，等绑定重建后自然恢复探测或运维清理——无 churn，可接受。
- **失败测试先行**：`TestPumpDueStatesSQLFiltersMissingBindingPairs`（`bg/node_probe_submit_gate_test.go`）改动前实测红（缺绑定链碎片），改动后绿。沿用 SQL 文本守卫样式——候选查询跑真库，pgxpool 无法 sqlmock，与既有 `TestPumpDueStatesSQLAppliesAutomaticEligibilityGate` 同型；断言双列关联碎片（`cmb.credential_id = nps.credential_id`、`pm.raw_model_name = nps.raw_model_name`）+ JOIN pm（悬空外键也被过滤）+ 过滤位于 WHERE（ORDER BY/LIMIT 之前，不占批内槽位）。
- **硬条件核对**：绑定缺失对**不漏探**——无绑定即无路由，探测无从执行；绑定重建后行自然重回 pump（next_retry_at 仍到期），与资格门"重新启用即重入"语义一致；paused 行不受影响（pump 本就不取）；manual/admin 绕过原样；闸门路径（`bg/probe_necessity.go`）零改动。
- **验证记录**：C: 本地打桩副本（`lgw-p2test`，复用既有副本、仅 cp 三个变更文件）执行 `go test -mod=vendor -count=1 ./bg/`：定向守卫 3 例全绿；全量套件 17 例失败经 **CRLF 同口径基线对照实验**确认与 origin/main 完全一致（16 例源文本断言 + 1 例 symlink 权限，均为预存环境限制，Linux CI 不受影响）。注意基线对照的方法坑：用 `git show` 直出的 LF 文件做基线会"少"4 例源文本失败（假差异），须按 autocrlf 检出口径把基线文件转成 CRLF 再跑才是同口径对照。`GOOS=linux GOARCH=arm64` zig cc 交叉编译 `./bg/... ./cmd/gateway/...` 通过。win/arm64 不支持 `-race`，未跑（与既往轮次相同）。

## 审计记录（2026-09-11 第五轮，针对 381fbf50d P3 pump 过滤）

- **调用面全量清点（验证第四轮"调度入口唯一性"论断）**：穷举 `NodeProbeWorker.Submit` / `SubmitWithSource` / `submitViaQueue*` / `ProbeQueue.Enqueue` 调用方——pump（第四轮已过滤）、`SubmitWithSource`（状态机的请求失败事件驱动，一次性，非调度器）、**`DailyProbeAudit`（24h 循环——发现 1，见下）**、credential_recovery 四条路径（全部绑定锚定）、`CredentialAutoHealWorker` 周期+OneShot（`dueAutoHealSQL` FROM cmb 起步，绑定锚定）、main.go 两处 expired-binding-recovery（以绑定事件自身为锚）、admin/手动（操作员在场）。另有 `BalanceQuotaProbe`/`PeriodicQuotaProbe`（CredentialProbeV2 凭据级配额探测，非 node_probe 管线）与 `system_monitor_adapter`（integrity_verify 任务），均不相关。
- **发现 1（实质，已修复）：`DailyProbeAudit` 的 candidate_failure_logs 分支不查绑定链。** UNION 的 request_logs 分支自带 cmb/pm JOIN（绑定锚定），但 candidate_failure_logs 分支是裸 (credential_id, raw_model_name) 投影——绑定链断裂的历史对在整个 72h 回看窗口内每天被重复提交，每次都是注定失败运行（queue claim → 磁贴 in-flight → endpoint-build "no rows" → missing-binding 丢弃 + 一条 fake-success 审计行）。**影响有界性核实**：统一队列模式下 `Submit` 不 UPSERT `node_probe_state`（直接入队），pump 过滤器也不重提该对，故不构成第四轮定义的 churn 循环，仅是低频噪声（每对 ≤3 次，随日志老化自然停止）。修复：SQL 抽取为 `dailyProbeAuditSQL()`（守卫可测，同 `pumpDueStatesSQL` 模式）+ 外层绑定链 EXISTS（同一谓词形态：双列关联、JOIN pm、只查存在性；对 request_logs 分支冗余但为真）+ 新守卫测试 `TestDailyProbeAuditSQLFiltersMissingBindingPairs`（先红后绿）。修复后不变量成立：**除 manual/admin 外，无任何调度入口会提交绑定链断裂的 (cred, model) 对**。
- **发现 2（表述修正）**：第四轮记录的 NULL 边缘应为 `c.status` **或 `c.lifecycle_status`** IS NULL（两者行为相同：严格查询不命中、宽松查询命中、不落入 missing-binding），已就地修正。
- **索引核查（性能）**：两处绑定链 EXISTS 的探测路径均由 `idx_cmb_credential_provider_model (credential_id, provider_model_id)` 前导列覆盖，pm 走主键——与 `resolveDirectTarget` / recovery 对账器同型访问路径，pump 与 daily audit 均无新增全表扫描面。
- **范围界定**：本轮不改 `Submit` 的事件驱动语义、不改 candidate_failure_logs 写入侧（写入本就是历史事实记录，过滤责任在消费侧调度口）；`daily_probe_audit_test.go` 为新建守卫文件（此前该 worker 无测试）。
- **验证记录**：C: 本地打桩副本（lgw-p2test）`go test -mod=vendor -count=1 ./bg/`：守卫 4 例（pump 3 + daily audit 1）全绿；全量套件 17 例失败与 CRLF 同口径基线完全一致（16 例源文本断言 + 1 例 symlink 权限，预存环境限制，Linux CI 不受影响）。`GOOS=linux GOARCH=arm64` zig cc 交叉编译 `./bg/... ./cmd/gateway/...` 通过。win/arm64 不支持 `-race`，未跑（与既往轮次相同）。

## 本轮记录（2026-09-11 第六轮，上线观察轮——零代码改动）

- **输入缺失声明（按 D 项要求如实区分）**：本工作区无生产环境/指标抓取通道，运维反馈渠道不在会话内。任务 A 的"部署后观察"与任务 B 的触发条件（运维反馈）本轮均无数据输入，故**代码零改动**；本轮产出 = B 项的无条件静态排查（不依赖数据、结论确定性成立）+ A 项可执行观测 runbook 与升级判据 + main 合并态复核。
- **main 合并态复核（只读）**：origin/main 仍为 b37b68b51（与第五轮验证对象同一提交，交叉编译结论沿用，未重跑）。三个指标均以 promauto 注册（`bg/probe_necessity.go`）；pump 绑定链过滤（`pumpDueStatesSQL`）、daily audit 绑定链过滤（`dailyProbeAuditSQL`）、skip 路径有界重试与计数器在位。
- **A 项观测 runbook（部署后执行）**：
  - 抓取：`/metrics` 挂在 admin 端口且需 admin token（`cmd/gateway/main.go` NET-008 注释处；`Authorization: Bearer <token>` 格式经 `middleware/admin_token_mw.go` 核实）——`curl -s -H "Authorization: Bearer <admin-key>" http://<admin-host>:<admin-port>/metrics | grep llmgw_node_probe_necessity`；返回 503 说明生产环境未配置 `LLM_GATEWAY_ADMIN_API_KEY`（production/staging 拒绝而非 fail-open），先补配置再观测。注意仓库 Grafana 仪表盘（`deploy/grafana/*.json`）**尚未包含**这三个指标，观测期用原始抓取，如需面板需另行添加。
  - 判读：`mirror_delete_retry_total` 偶发增长 = 瞬时失败被同轮吸收（设计行为）；`mirror_delete_failed_total` 持续增长（24h 窗口）→ 按遗留项 6 升级短 TTL 抑制（硬条件不变：不漏真实故障探测、证据错误 fail-open、manual/admin 绕过、lease 丢失不删新 owner 状态）；两者平稳且 skipped 磁贴占比稳定 → 记录结论、关闭观察项；`skip_total{reason}` 异常高 → 排查 Redis 键 schema（legacy/k2/dual）与 tenant 归属。
- **B 项静态排查（本轮完成；结论先于运维反馈，供触发时直接使用）**：
  - **显示面机制**：自检 tab 逐节点泳道 `GET /api/admin/probe/node-tasks`（`admin/probe_dashboard.go` `queryProbeNodeTasks`）**直读 `node_probe_state`、无绑定链过滤**——孤儿行（绑定断裂）满足其 WHERE（未暂停且 next_retry_at 到期）→ 以**永久 'pending' 磁贴**长期显示，last_direct_ok/latency 等列为陈旧值。这是 381fbf50d pump 过滤引入的**确定性显示面变化**：过滤前孤儿行被 pump 周期重提→探测→missing-binding 丢弃（删除通常成功，磁贴走完生命周期后消失）；过滤后永不再被调度，磁贴常驻。即第四轮"无 churn，可接受"的残余面在 tab 上的真实形态是"常驻陈旧磁贴"，当时未记录，本轮补记。
  - **清理口径全量清点**（生产代码仅两处 `DELETE FROM node_probe_state`；第七轮审计修正措辞）：missing-binding 丢弃（`bg/node_probe.go:1799`）对孤儿行在**自动调度口不可达**（pump/daily audit/recovery 均已过滤或锚定），但 **admin 手动提交可达**——`POST /api/admin/probe/tasks`（`admin/probe_dashboard.go` `handleProbeTaskCreate`，任务不设 `Automatic`，而 Enqueue 的资格门仅在 `task.Automatic` 时生效）可对孤儿对一次性入队：注定失败运行在 endpoint-build 阶段失败（无出站 HTTP）后由 missing-binding 分支删除孤儿行——**免 SQL 的一等清理路径**（web 前端对该端点仅 GET，无按对按钮，目前是 API 级操作）；skip 路径镜像删除（`bg/probe_service.go:752`）仅对已提交任务生效。`handleNodeProbeStateReset`（`admin/probe_history.go`）只 UPDATE 重置、不删除；`TriggerAllSync` 从 cmb 枚举（绑定锚定），既不重提也不清理孤儿；`MarkNodeProbeHealthy` 仅在业务请求成功时写行，解绑后无业务请求不会重建行（唯一理论残留：解绑瞬间的在途请求，有界）。**结论（第七轮审计修正后口径）：孤儿行清理 = ①绑定重建自然恢复（EXISTS 变真 → pump 重提 → 正常探测或丢弃）；②运维经 admin API 对该对手动提交一次探测（行被 missing-binding 分支删除）；③手工 SQL 批量清理。无自动回收路径。**
  - **运维临时清理 SQL（反馈属实且等不及绑定重建时）**：
    ```sql
    DELETE FROM node_probe_state nps
    WHERE NOT EXISTS (
      SELECT 1 FROM credential_model_bindings cmb
      JOIN provider_models pm ON pm.id = cmb.provider_model_id
      WHERE cmb.credential_id = nps.credential_id
        AND pm.raw_model_name = nps.raw_model_name);
    ```
  - **升级设计预案（运维反馈属实时实施，先失败测试）**：首选**显示侧**——`queryProbeNodeTasks` 的 WHERE 并入绑定链 EXISTS（与 pumpDueStatesSQL 同谓词形态：双列关联、JOIN pm、只查存在性），磁贴只显示"存在可执行任务"的行，绑定重建后磁贴自然回归；SQL 抽取为可守卫函数（同 `dailyProbeAuditSQL` 模式）+ 文本守卫测试，零数据风险。备选**数据侧周期 GC**（宽限期删除，如绑定断裂且 updated_at < now()-7d，保守起见 paused 行不动）——新增调度面，风险高于显示侧，仅在表膨胀成为实际问题时考虑，默认不做。运维反馈到达时还需与反馈方确认"自检 tab"具体指哪个面板（node-tasks 泳道 vs SSE 实时流——SSE 流对孤儿行本就不产生事件，常驻陈旧磁贴只能是泳道）。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（C: 本地打桩副本 lgw-p2test，bg/ 与 origin/main 逐文件一致、仅 `diskUsagePercent` 桩差异）**：定向守卫族 `go test -mod=vendor -count=1 -run 'TestDailyProbeAuditSQL|TestPumpDueStatesSQL|TestProbeNecessity|TestSkipMirrorDelete|TestSkipRemoval|TestSkipPersistence|TestRemoveSkipped' ./bg/` → **25 例全 PASS**；全量 `./bg/` → 17 例失败，**逐一核对与既知基线完全一致**（16 例 CRLF 源文本断言 + 1 例 symlink 权限；本年以来失败名单首次完整留档于 `lgw-p2test/fails_r6.txt`）。本轮零代码改动，未重跑交叉编译（b37b68b51 与第五轮同一提交，结论沿用）；win/arm64 `-race` 依旧不支持，未跑。
- **遗留风险**：①necessity 三指标观测依赖人工抓取——无面板，且 `deploy/prometheus/rules/alerts.yml` 的探测管线告警（NodeProbeQueueSubmissionFailuresHigh 等）**未覆盖** necessity 指标，failed_total 持续增长可能晚发现；若运维接受，面板加 `deploy/grafana/`、告警规则加 `alerts.yml` 是自然位置；②孤儿行常驻磁贴问题已证成但未修复（等触发），升级预案、admin API 清理路径与批量 SQL 三条口径就绪；③本记录的清理口径清点基于静态审读，批量 SQL 执行前应先在只读副本核数。

## 审计记录（2026-09-11 第七轮，针对 9d7ea65c8 第六轮零代码记录）

- **核验通过的主干断言**：三指标 promauto 注册与 reason 取值（`not_necessary_all_nodes_healthy` / `not_necessary_last_probe_healthy`）；`/metrics` 路由与 admin token 中间件（`Authorization: Bearer` 格式经 `middleware/admin_token_mw.go` 全文核实成立）；`queryProbeNodeTasks` 的 WHERE/CASE 语义（孤儿行渲染为永久 'pending' 磁贴）；`node_probe_state` 清理点全量（生产代码两处 DELETE；`deploy/sql` 侧无 retention/pg_cron 任务）；`TriggerAllSync` cmb 锚定；`MarkNodeProbeHealthy` 解绑后不可达；临时 SQL 谓词形态与 pump 过滤器一致；`handleProviderProbeStates`（providers 页自动测试 tab）读 model_probe_runs/passive_probe_state/request_logs，**不**读 node_probe_state，非孤儿显示面。
- **发现 1（表述过强，已随本提交修正第六轮记录）**：第六轮"missing-binding 丢弃对孤儿行不可达（不再被任何调度口提交）"过强——自动调度口不可达成立，但 admin `POST /api/admin/probe/tasks`（路由注册于 `RegisterProbeDashboardRoutes`，`handleProbeTaskCreate` 构造的任务不设 `Automatic`，而 `ProbeQueue.Enqueue` 的资格门仅在 `task.Automatic` 时生效）可对孤儿对一次性提交：注定失败运行（endpoint-build 前失败、无出站 HTTP）后由 missing-binding 分支删除该行。这构成**免 SQL 的一等清理路径**，与第五轮不变量无冲突（manual/admin 本就在"无调度入口"例外清单内）。web 前端对该端点仅 GET（`web/src/views/probe/ProbeHealthPanel.vue:203` 的 POST 注释陈旧，实际代码走模型级 `/api/self-check/trigger`），故该清理路径目前为 API 级操作。
- **发现 2（表述不准，已修正）**：第六轮"无面板、无告警规则"——准确说法是 necessity 三指标无面板、且未被 `deploy/prometheus/rules/alerts.yml` 覆盖（该文件存在，含 NodeProbeQueueSubmissionFailuresHigh 等探测管线告警）。升级时告警加 `alerts.yml`、面板加 `deploy/grafana/` 是自然位置。
- **发现 3（runbook 补强）**：补 503 语义——production/staging 下 `LLM_GATEWAY_ADMIN_API_KEY` 未配置时 `/metrics` 返回 503（显式拒绝而非 fail-open，`admin_token_mw.go`），观测前先确认配置。
- **范围界定**：本轮纯文档审计修正，不改任何代码与 SQL；第六轮测试结论（定向 25 守卫绿 / 全量 17 例失败=基线）不受影响。

## 本轮记录（2026-09-11 第八轮，观测工具补齐）

- **任务选择说明**：第七轮提示词按输入分派（生产指标数据 / 运维反馈），两者本轮仍未到达。与其第三轮记录阻塞，本轮落地第六/七轮已标记的遗留风险①的补齐手段——necessity 三指标的告警与面板（纯 deploy 工件，**零网关/探测行为改动**）；TTL 抑制等真正的行为升级仍严格锁在数据门后。
- **告警规则**（`deploy/prometheus/rules/alerts.yml`，node-probe 家族所在组）：`NodeProbeNecessityMirrorDeleteFailedHigh` = `increase(llmgw_node_probe_necessity_mirror_delete_failed_total[10m]) > 5`，`for: 5m`，severity warning。阈值依据：单 (cred,model) 卡住时 pump 反复重提（tick 30s、行级 holdoff 45s，实际周期约 60s ≈ 10 次/10 分钟；第九轮审计修正——原表述"约 30s holdoff"有误），每轮 ≥1 次失败计数，10 分钟 >5 即代表至少一对处于翻动循环；零星 <5 的增量是 DB 瞬时抖动的如实计数，不告警——与第六轮 runbook 判读规则一致。形状刻意对齐同组既有 `NodeProbeQueueSubmissionFailuresHigh`。
- **Grafana 面板**（`deploy/grafana/selfcheck-necessity-dashboard.json`，新文件，uid `llm-gateway-selfcheck-necessity`）：三个 timeseries——skip 按原因（5m）、镜像删除同轮重试（10m）、重试后仍失败（10m，红色阈值 5 与告警表达式一致）。schema 惯例（Grafana 8.0、字符串 datasource、panel 结构）逐项对照 `proxy-overview-dashboard.json`。`deploy/grafana/README.md` 同步：面板清单新增第 5 节、导入文件列表、指标说明新增"自检必要性指标"小节。
- **验证记录**：便携 Go + vendored `yaml.v3` / `encoding/json` 结构化校验——alerts.yml 解析为 6 groups 且新告警 expr/for/severity 完整；面板 JSON 有效、3 个 timeseries target 非空。**环境受限（如实标注）**：`promtool` 不可用（无 Docker），PromQL 未经 promtool check——但 expr 与同组既有告警同形（`increase(counter[10m]) > N`），指标名逐一对照 `bg/probe_necessity.go` 注册名核实；Grafana 导入未实测（无实例），导入即验。（第九轮审计补充：仓库自带 `deploy/prometheus/rules/alerts_test.go` 契约测试当时被遗漏，第九轮已补跑并通过，并把新告警钉入该测试——见审计记录第九轮。）无 Go/SQL 改动，不涉及测试套件与交叉编译。
- **硬条件核对**：网关代码、SQL、闸门路径、探测语义零改动；本轮只是把"人工记得 curl"变成"告警自动触发"，不预支任何按数据才定的升级。
- **范围界定**：`web/src/views/probe/ProbeHealthPanel.vue:203` 的陈旧 POST 注释（第七轮发现）本轮**未**修——web 目录单独改动不值得混入 deploy 工件提交，留作记录；运维侧需按 `deploy/prometheus/README.md` 的 provisioning/UI 流程实际加载规则与面板后，观测链路才生效。

## 审计记录（2026-09-11 第九轮，针对 a6bf4389d 第八轮观测工具）

- **发现 1（验证遗漏，已补）**：第八轮把告警校验途径归于"promtool（不可用）"，遗漏了仓库自带的 **`deploy/prometheus/rules/alerts_test.go`**——yaml.v3 结构解析 + `hot_table_promote_alerts` 组逐告警指标契约 + 全文件低基数守卫（全文禁止 `tenant=` / `model=` 子串，防止高基数标签混入告警）。第九轮补跑该包全部 6 例（含 client_token/routing_*/response_body 等同目录契约测试）全绿，并按既有惯例把新告警钉入该测试（expr 含 `llmgw_node_probe_necessity_mirror_delete_failed_total` + `for: 5m`）——此后指标改名/规则被删会在 CI 直接红。
- **发现 2（数字错误，已修正）**：第八轮告警注释与 handoff 记录写"pump 按周期（约 30s holdoff）重提"——实际 `nodeProbeQueuePumpHoldoff = 45s`、`nodeProbeQueuePumpInterval = 30s`（tick），行因 holdoff 未到期会跳过 30s 的 tick，实际重提周期约 60s ≈ 10 次/10 分钟/对。**阈值 >5/10m 无需改动**（单卡住对约 10 次/10 分钟，裕度充分；单次瞬时抖动 1-2 次不触发）；alerts.yml 注释与第八轮记录已就地修正。
- **正向确认**：新告警 expr/描述不含 `tenant=`/`model=`（通过全文件低基数守卫）；面板三个 expr 同样干净；rules 包其余 5 例契约测试不受影响；无 Go 运行时/SQL/闸门路径改动。
- **验证记录**：`go test -mod=vendor -count=1 ./deploy/prometheus/rules/`（C: 副本 lgw-p2test，alerts.yml + alerts_test.go 同步后）→ **6 例全 PASS**。promtool 与 Grafana 导入依旧环境受限（表述同第八轮）。
- **范围界定**：alerts.yml 注释修正 + alerts_test.go 追加契约钉（test-only）+ handoff 修正；面板 JSON、README 无需改动。

## 本轮记录（2026-09-11 第十轮，上线观察轮第四轮——无输入记录阻塞 + 面板契约钉）

- **输入缺失声明（按 D 项要求如实区分）**：`git fetch` 复核 origin/main 仍为 8bfd402ca（无新提交），任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮代码产出 = 一项不依赖输入的确定性收尾（见下）；无任何网关/探测/SQL 行为改动。
- **面板侧契约测试（`deploy/grafana/dashboard_test.go`，新文件，test-only）**：第九轮把告警契约钉入 `alerts_test.go` 后，第八轮交付的面板 JSON 仍只有一次性脚本校验（tmp_yamlcheck，未提交）——指标改名、面板删除、结构破坏都不会在 CI 红。新增 3 例补齐观测链路最后一处无防回归保护的工件：①`TestSelfcheckNecessityDashboardPinsAllThreeCounters`——uid=`llm-gateway-selfcheck-necessity`、恰好 3 个 timeseries、三个 necessity 指标各恰有一个 expr 引用、expr 低基数守卫（禁 `tenant=`/`model=`，对齐 alerts_test.go 全文件守卫语义，仅作用于 expr 字段以免误伤 description 文本）；②`TestSelfcheckNecessityDashboardFailureThresholdMatchesAlert`——failed_total 面板红色阈值必须 = 5，与 `NodeProbeNecessityMirrorDeleteFailedHigh` 的 `increase(...[10m]) > 5` 保持第八轮刻意对齐的契约；③`TestGrafanaReadmeListsSelfcheckNecessityDashboard`——README 必须引用面板文件名（防改名失联）。
- **意外发现（副本卫生，顺带证明测试有效）**：lgw-p2test 的 `deploy/grafana/README.md` 是旧版（第八轮只同步了 alerts.yml 与面板 JSON，README 未同步）——README 契约测试首跑即红并暴露该滞后，同步后绿。变异验证（先红后绿）：把面板 JSON 中 `skip_total` 临时改名 → `TestSelfcheckNecessityDashboardPinsAllThreeCounters` 红（"expected exactly one panel expr"）→ 恢复 → 绿。
- **验证记录（C: 副本 lgw-p2test，同步 dashboard_test.go + 面板 JSON + README）**：`go test -mod=vendor -count=1 ./deploy/grafana/ ./deploy/prometheus/rules/` → **grafana 3 例 + rules 6 例全 PASS**（rules 包重跑确认第九轮告警契约不受影响）。环境受限（与第八/九轮相同）：promtool 与 Grafana 导入仍不可用，PromQL/导入未实测——但 expr 指标名现已由本契约测试钉住，改名即 CI 红。无 Go 运行时/SQL/闸门路径改动，不涉及 bg 套件与交叉编译（b37b68b51 与第五/六轮同一代码提交，结论沿用）。
- **遗留风险**：与第九轮相同，无新增。观测链路（告警+面板）等运维按 deploy/prometheus/README provisioning 流程加载后生效；TTL 抑制、显示侧治本等升级继续等待对应输入。

## 本轮记录（2026-09-11 第十一轮，上线观察轮第五轮——无输入记录阻塞 + 合并态复核）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核 + 本记录。
- **main 合并态复核（只读，本轮确定性产出）**：`git fetch` 后 origin/main 前进至 f7c59617d，增量核对：第十轮提交 1a19dc1ea 已在 origin/main 历史中（`merge-base --is-ancestor` 通过）——观测链路全部工件（`alerts.yml` 告警、`alerts_test.go` 契约、`selfcheck-necessity-dashboard.json`、`dashboard_test.go` 契约、grafana README）**均已随 main 交付，无未合并内容**；f7c59617d 本身是另一工作线的审计文档（`docs/audit/2026-09-11-node-degrade-false-positive-fix.md`，node 强启误降级修复门禁记录），与本特性无关。网关/探测/SQL 代码自 b37b68b51 起零变化，第五/六/十轮的测试与交叉编译结论全部沿用。【第十二轮审计注记：此结论仅覆盖至 f7c59617d——本记录推送后 6490fd981（R12 P2/P3 收尾）变更了 bg/ 探测代码，bg 相关结论随之失效，失效面与处置见"审计记录 第十二轮"。】
- **虚警排除（合并态复核顺带；第十二轮审计修正措辞）**：与观测分支 diff 出现 `deploy/...conf.20260821-spa-fallback-only` 的基准是本地 **main 分支引用**（b37b68b51，`git fetch` 不更新本地分支引用），**不是** fix/r13-logging-hygiene 检出（那是另一条分支，与此 diff 无关）；该文件属另一工作线（R12/ops nginx 线）的正常提交内容，非本特性遗留，未处置。教训：SMB 仓库的本地分支引用长期落后，一切对照以 origin/main 为准。
- **验证记录（如实区分）**：本轮除 handoff 文档外零改动，未重跑测试/编译——被验证对象与本轮复核对象逐字节一致（1a19dc1ea 的 deploy 契约测试 3+6 例、bg 定向守卫 25 例、交叉编译结论沿用第十轮）。观测链路是否已被运维按 deploy/prometheus/README 实际 provisioning（告警/面板是否生效）无生产通道可查，**未验证**。
- **遗留风险**：与第十轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。

## 审计记录（2026-09-11 第十二轮，针对 674ccab80 第十一轮记录）

- **核验通过的主干断言（写入时点均真）**：①第十轮 1a19dc1ea 为 origin/main 祖先，观测五件套齐全——审计时在前进后的 origin/main（8488a0838）上复验仍齐全：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处、面板 uid 1 处、dashboard_test.go 恰 3 例、README 引用 2 处；②f7c59617d 为 docs-only（单文件审计文档）；③`git diff b37b68b51 f7c59617d -- bg/ cmd/ domains/ internal/ deploy/sql/ web/` 为空——第十一轮"网关/探测/SQL 代码零变化、结论沿用"在写入时点成立。
- **发现 1（时效缺陷，实质）：第十一轮记录推送后 main 立即前进，"结论沿用"随之失效。** 6490fd981（R12 P2/P3 收尾）+ 合并 8488a0838 变更 `bg/node_probe.go`（legacy `runOne` missing-binding 分支 +17/-1）与 `bg/node_probe_test.go`（+15）——**bg/ 基线自 b37b68b51 起移动**。连锁失效面：①17 例预存失败名单（lgw-p2test/fails_r6.txt）是 b37b68b51 的 CRLF 口径，node_probe.go 源文本变化可能改变 16 例源文本断言失败集，**下次跑 bg 套件前须重测当前基线**，不得直接拿 fails_r6.txt 当回归判据；②"bg 定向守卫 25 例全绿"不覆盖新 bg/ 代码（6490fd981 提交说明自称 `go test ./bg/...` green，属该工作线自验，环境/失败口径未在本 handoff 留档）；③第十一轮刷新的下一轮提示词写着"代码最新 b37b68b51"，已随本审计整体更正。
- **发现 2（行为交互评估，无需代码改动）**：6490fd981 恰落在本 handoff 记载的一等清理路径分支上——legacy `runOne` missing-binding 分支（`bg/node_probe.go:1802` 附近）在 best-effort DELETE **之后**补写一条 `node_probe_runs` 取证行（success=false、两轮均 direct、nextSec=0，镜像统一路径既有行为）；INSERT 失败走 `auditPersistFailedTotal` + ERROR + wrapped return，**不回滚 DELETE**（提交声明与 diff 双重核实）。与本特性口径的兼容性结论：孤儿行（`node_probe_state`）三层清理口径不变；admin API"提交一次探测即删行"删行语义不变，仅仪表盘多一条**预期内**的失败取证行（非异常信号，勿误判为故障）；闸门/skip/pump/daily-audit 路径零涉及（diff 只碰 runOne、其测试与一处 gofmt 修复）。第六/七轮相关记录表述仍准确，本条即为补充。
- **发现 3（表述修正，已就地修正第十一轮记录）**："虚警排除"把 diff 基准误写为"本地 main 检出（fix/r13-logging-hygiene）"——实际基准是本地 **main 分支引用**（b37b68b51），与 fix/r13-logging-hygiene 检出无关；已就地修正。
- **范围界定**：本轮纯文档审计修正（handoff），零代码/SQL/deploy 工件改动；不重跑测试套件（无新被测对象；bg 新基线重测留给下次触碰 bg 的轮次，已在下一轮提示词以"bg/ 基线警示"标注）。
- **验证记录（如实区分）**：全部断言基于 `git diff` / `git show | grep` / `git merge-base --is-ancestor` 只读取证（origin/main=8488a0838；SMB 上用路径限定命令）。未运行任何测试套件——本轮没有可运行的新验证对象，环境受限说明同"验证环境说明"节。

## 本轮记录（2026-09-11 第十三轮，上线观察轮第七轮——无输入记录阻塞 + bg 基线重测）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮代码零改动，产出 = bg/ 基线重测（第十二轮明确留下的确定性收尾）+ main 合并态复核。
- **main 合并态复核（只读）**：`git fetch` 后 origin/main = **b6ae27f9f**（第十二轮审计提交本身，docs-only 单文件），第十二轮推送后无新提交。观测五件套复验在位：alerts.yml 告警 1 处、面板 uid 1 处、alerts_test.go 契约钉 1 处、dashboard_test.go 恰 3 例。网关/探测/SQL 代码自 6490fd981 起零变化（`git diff b37b68b51 origin/main -- bg/ domains/ internal/ cmd/ deploy/sql/ web/` 仅 bg/node_probe.go +17/-1 与 bg/node_probe_test.go +15，即 6490fd981 已知变更面）。
- **bg/ 基线重测（本轮确定性产出；解除任务书警示框）**：fails_r6.txt 的 17 例名单是 b37b68b51 口径，本轮以当前 origin/main 重测并留档 **lgw-p2test/fails_r7.txt**——后续任何触碰 bg 的轮次以 fails_r7.txt 为回归判据，fails_r6.txt 作废。
  - **同步方法（CRLF 同口径，先校验后应用）**：lgw-p2test 的 `bg/node_probe.go`、`bg/node_probe_test.go` 以 `git show origin/main:<file> | unix2dos` 覆盖。转换口径先用 b37b68b51 版本对副本既有文件做 cmp 逐字节校验（完全一致）后才应用到 origin/main 版本——排除第四轮记录的"LF 直出假差异"坑。`storage_retention_worker.go` 的 `diskUsagePercent` 桩保留（差异面用逐文件 hash 清点确认：副本相对 origin/main 仅上述两文件 + 桩文件三处不同）。
  - **结果**：全量 `go test -mod=vendor -count=1 ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc GOPROXY=off）→ **17 例失败，排序无关集合与 fails_r6.txt 完全一致**（16 例源文本断言 + 1 例 TestWalkDirSafe symlink 权限）——6490fd981 的 bg/ 变更**未改变** Windows 预存失败面。
  - **定向守卫族 25 例全绿**（第六轮同一 `-run 'TestDailyProbeAuditSQL|TestPumpDueStatesSQL|TestProbeNecessity|TestSkipMirrorDelete|TestSkipRemoval|TestSkipPersistence|TestRemoveSkipped'` 正则）——necessity gate 全部关键测试在 6490fd981 后的 bg/ 代码上依旧通过，补上第十二轮发现 1-②"新 bg/ 代码无本地守卫留档"的缺口。
  - **附带核实**：`TestRunOneMissingBindingDropsOrphanStateRow`（6490fd981 扩展的测试）仍在失败集，但其失败点是 DELETE 语句源文本断言（CRLF 失配），6490fd981 新增的取证行（success=false INSERT）行为断言位于其后**未执行到**——该行为在 Windows 本地未验证，属 Linux CI 职责（6490fd981 自验 green），与本特性无关。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——全量 bg 新基线对照（17 例集合与 fails_r6.txt 一致，fails_r7.txt 留档）、定向守卫 25 例 PASS、CRLF 转换口径 cmp 校验。**环境受限**——源文本断言类测试在 CRLF 检出下无法在 Windows 通过（由 Linux CI 覆盖）；win/arm64 `-race` 不支持未跑（与既往轮次相同）；promtool/Grafana 导入不可用（沿用第八/九轮口径）。**未验证**——观测链路是否已被运维实际 provisioning、生产指标状态（无通道）。
- **遗留风险**：与第十二轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。

## 下一轮提示词（可直接复制）

```text
请继续 llm-gateway-go 自检必要性闸门（necessity gate）收尾——上线观察轮（第八轮）。
工作目录：Z:\workspace\ai-native-tools\syncfield\llm-gateway-go-4

先阅读：docs/handoff/2026-09-11-selfcheck-necessity-gate-handoff.md（重点"本轮记录
第六轮"（runbook 与孤儿行三层清理口径）、"本轮记录 第八轮"（告警+面板已随仓库交付）、
"审计记录 第九轮"（告警契约已钉入 alerts_test.go）、"本轮记录 第十轮"（面板契约
已钉入 deploy/grafana/dashboard_test.go——观测链路全部工件均有 CI 防回归保护）、
"本轮记录 第十三轮"（合并态复核 + bg/ 基线重测：fails_r7.txt 已是当前
origin/main 口径基线，fails_r6.txt 作废））、遗留任务第 6 项。
背景：观测工件齐全且已全部合入 main（第十二/十三轮复验）；main 代码头以 git fetch
实查为准。6490fd981（R12 P2/P3 收尾）变更了 bg/node_probe.go 的 legacy
missing-binding 分支（补写 node_probe_runs 取证行，DELETE 清理语义不变）——
必要性闸门/skip/pump/daily-audit 路径未涉及；第十三轮已在其上重测 bg 基线：
全量失败集合与 fails_r6.txt 完全一致、定向守卫 25 例全绿。
告警 NodeProbeNecessityMirrorDeleteFailedHigh（failed_total 10m 增量 >5，warning）
+ 面板 selfcheck-necessity-dashboard.json——运维按 deploy/prometheus/README 的
provisioning 流程加载后即自动告警，无需人工 curl。孤儿行应急口径三层：绑定重建
自然恢复 / POST /api/admin/probe/tasks 提交一次探测即删行（自 6490fd981 起同时
留一条 success=false 取证行，属预期信号非异常）/ 批量 SQL；显示侧
治本预案（queryProbeNodeTasks WHERE 并入绑定链 EXISTS）就绪未实施。
注意：主工作区检出的 fix/r13-logging-hygiene 属于并行 R13 日志工作，不要混入本任务；
工作区遗留脏文件（docs 两处、scripts/deploy-lib、*.lnk）严禁 add/clean/恢复。
继续用 git worktree 从 origin/main 拉独立分支实施；Windows 验证用 C: 副本
lgw-p2test（仅 cp 变更文件；deploy 包验证需同步 deploy/grafana/ 的 README.md
与面板 JSON；基线对照务必 CRLF 同口径——`git show <rev>:<file> | unix2dos`
口径已第十三轮 cmp 校验）。若 origin/main 的 bg/ 再次被外部提交变更，跑 bg
套件前须先重测基线；否则直接以 lgw-p2test/fails_r7.txt（17 例）为回归判据。

本轮任务（按输入分派，无输入则如实记录阻塞）：
A. 若已获得生产 /metrics 数据或告警触发记录（runbook 判读规则）：
   - mirror_delete_failed_total 持续增长 / 告警 firing → 实施短 TTL 抑制（硬条件
     不变：不漏真实故障探测、证据错误 fail-open、manual/admin 绕过、lease 丢失
     不删新 owner 状态），先补失败测试；
   - 两者平稳 → 在 handoff 记录结论并关闭遗留项 6。
B. 若运维确认"模型解绑/改名后自检 tab 长期显示陈旧节点"：先给应急口径（admin API
   提交一次探测即删行，或批量 SQL），再按预案实施显示侧治本修复（queryProbeNodeTasks
   WHERE 并入绑定链 EXISTS，SQL 抽取可守卫 + 先红后绿测试），实施前与反馈方确认
   面板口径（node-tasks 泳道 vs SSE 流）。
C. lastProbeRun 维持关闭；除非出现乱序完成记录反例。
D. 验证如实区分通过/环境受限/未验证。

完成后输出：结论/根因、改动文件与关键行为、测试命令与结果、遗留风险、
更新本 handoff、下一轮提示词。
```

## 验证环境说明

本工作区（Z: 网络盘）无 Go/Docker/WSL。便携工具链保留在 `C:\Users\xutaohuang\AppData\Local\golang-dist`（Go 1.27.1 / llvm-mingw / zig）。`bg` 包含 Linux 专属 `syscall.Statfs_t`，Windows 本地验证需在副本中将 `storage_retention_worker.go` 的 `diskUsagePercent` 打桩后再 `go test -mod=vendor ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc）。部署目标验证用 `GOOS=linux GOARCH=arm64` + `zig cc -target aarch64-linux-musl` 交叉编译。

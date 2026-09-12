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
   **【第二十六轮预检状态（2026-09-12，ssh 只读探测）】数据门未开：245 侧运维接入未执行（targets 仍旧单目标、无 Necessity 规则、necessity series 空）；三个接入前置已实测闭环（两机 key 不同值→必须拆双 job、154 单实例→只配 a 槽位、两机网关二进制已含 gate 指标）——接入材料已按实测修正为"照做即用"（NATIVE-245-DEPLOY.md 接入节 + prometheus.yml 双 job 模板），验收判据用 `mirror_delete_failed_total` 0 值系列（skip_total 为懒创建系列，首次 skip 前不存在）。判读仍按第六轮 runbook + instance 分节点口径，等接入后的首批数据。**

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

## 本轮记录（2026-09-12 第十四轮，上线观察轮第八轮——无输入记录阻塞 + bg 基线随外部变更重测）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核（会话窗口内 origin/main 两次前进的增量定性）+ bg/ 基线重测（fails_r8.txt 留档）+ deploy 契约复跑 + 协作环境异常处置记录。
- **main 合并态复核（只读，两次 fetch 实查）**：本轮窗口内 origin/main 前进两跳——d5be9203b（= b9a4153e8 第十三轮文档 + **d812e1a53 bg 读表面修复** + f3c30ef6e R12 收尾审计 + 合并）→ **2027067ac**（R13 日志线经 4eaa4cf59 合入 main：internal/logging raw-sink 系列与 deploy/prometheus 的 R12/R13 观测工件 + admin/web offer 修复系列 + 其收尾文档）。增量定性：
  - **d812e1a53（本轮基线重测对象）**：request_logs 裸父表读改走 `request_logs_with_current_month` 当月表共 4 处（candidate_failure_monitor 的 5min staleness 与 auto-cool、`dailyProbeAuditSQL` 的 request_logs 分支 FROM、shared_pick 7d 选型），并新增守卫 `TestRecentWindowReadsUseCurrentMonthSurface`（表面后缀正则断言，CRLF 不敏感）。**必要性闸门/skip/pump 路径零涉及；`dailyProbeAuditSQL` 的绑定链 EXISTS 外层过滤未动（diff 只改内层 request_logs 分支 FROM）**；candidate_failure_logs 分支未动；孤儿行三层清理口径不变。
  - **2027067ac（R13 合入）**：`git diff d5be9203b..2027067ac -- bg/` 为空——bg/ 零变化，**基线重测对新头字节级有效**；deploy 变化面全在 R12/R13 raw-sink 观测工件（shadow-write-failures.yaml、r12-raw-sink-rollout.json、docker-compose、prometheus.yml），非本特性面。
  - **观测五件套复验在位（2027067ac）**：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。本 handoff 文件未被并行线改动。
- **bg/ 基线重测（本轮确定性产出；按任务书警示执行）**：d812e1a53 移动 bg/ 后重测，**lgw-p2test/fails_r8.txt 为当前基线，fails_r7.txt 作废**。
  - **同步方法（CRLF 同口径，先校验后应用）**：以第十三轮同步产物 node_probe.go / node_probe_test.go 对 `git show 29db47ad8:<file> | unix2dos` 做 cmp 逐字节口径校验（完全一致）后，同口径应用 4 个文件（candidate_failure_monitor.go、daily_probe_audit.go、shared_pick.go、新增 recent_surface_reads_test.go）。
  - **副本卫生发现（本轮新记）**：全量清点暴露两个守卫测试文件行尾口径混杂——node_probe_submit_gate_test.go 前 258 行 CRLF（第四轮 cp 自工作区检出）+ 259-300 行（后加的守卫段）LF；daily_probe_audit_test.go 整文件 LF。两者与 blob 做 LF 直出 cmp **内容逐字节一致，无真实漂移**；统一归一 CRLF 后清点仅剩 storage_retention_worker.go 桩一处差异（与第十三轮口径相同）。
  - **结果**：全量 `go test -mod=vendor -count=1 ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc GOPROXY=off）→ **17 例失败，排序无关集合与 fails_r7.txt 完全一致**（唯一 diff 是 TestWalkDirSafe 的计时数字 0.01s vs 0.00s）——d812e1a53 未改变 Windows 预存失败面（16 例源文本断言 + 1 例 symlink 权限，Linux CI 不受影响）。新守卫 TestRecentWindowReadsUseCurrentMonthSurface **通过**（不在失败集）。
  - **定向守卫族 25 例全绿**（第六轮同一 `-run` 正则）——d812e1a53 之后 necessity gate 全部关键测试依旧通过。deploy 契约 `go test -mod=vendor -count=1 ./deploy/grafana/ ./deploy/prometheus/rules/` → **两包 ok**（dashboard_test.go 3 例结构在新头复验在位；副本 deploy 文件与 blob 逐字节一致，deploy 测试无源文本断言、行尾不敏感）。
  - **交叉编译**：`GOOS=linux GOARCH=arm64` zig cc `./bg/... ./cmd/gateway/...` 通过（对 d5be9203b 内容执行；bg/ 与 2027067ac 字节一致，覆盖本特性编译面；cmd/internal 的 R13 增量归该线自验）。
- **会话期协作环境异常（记录给下一轮）**：本轮窗口内并行工作线动作频繁——lgw-necessity-p2 worktree 的管理元数据被外部清掉（`.git/worktrees/` 仅剩 go-cost 条目，目录成僵尸链接，git 报 "not a repository: (NULL)"）；主工作区检出被切到 main（R13 合并后）；R13 的 C:/tmp 两个 worktree 一并消失。本轮处置：确认目录干净且 docs/necessity-gate-round12 已推送后 rm 重建（`git worktree add -B docs/necessity-gate-round14 origin/main`，基于 2027067ac）。**下一轮开工先 `git worktree list` + worktree 内 `git status` 验活，失效即重建**。主工作区脏文件清单缩小（VERSION、admin/credential_models*.go、version.json、web/public/*.json 等已被 R13 合并吸收），余下 D vendor_pricing_table.py / M incidents 文档 / D scripts/deploy-lib / ?? *.lnk 仍严禁触碰。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——bg 新基线对照（17 例集合与 fails_r7.txt 一致）、定向守卫 25 例、deploy 契约两包、交叉编译。**环境受限**——源文本断言类 16 例在 CRLF 检出下无法 Windows 通过（Linux CI 职责）；win/arm64 `-race` 不支持未跑；promtool/Grafana 导入不可用（沿用第八/九/十轮口径）。**未验证**——观测链路是否已被运维实际 provisioning、生产指标状态（无通道）。
- **遗留风险**：与第十三轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线两轮内被外部提交两度移动（fails_r6→r7→r8），**触碰 bg 前先 diff 自上轮复核点确认基线口径仍有效**。

## 本轮记录（2026-09-12 第十五轮，上线观察轮第九轮——无输入记录阻塞 + 合并态复核）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核 + worktree/基线留档验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 2027067ac 前进一跳至 **323ba7a79**——即第十四轮 handoff 提交本身（docs-only，单文件 32+/15-；第十四轮分支 docs/necessity-gate-round14 已 fast-forward 合入 main）。`git diff 2027067ac..origin/main` 除该 handoff 文件外为空：**bg/、deploy/ 零变化**——fails_r8.txt（17 例）基线口径继续有效（无需重测）、deploy 契约测试结论沿用第十四轮（两包 ok）。观测五件套在 origin/main 逐项复验在位：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。本 handoff 文件未被并行线改动（第十六轮审计修正：原文复验所用 diff 在当时 origin/main=323ba7a79 下与自身比较恒空、不构成证据；结论换用提交清单实证另证成立——`git log 2027067ac..323ba7a79 -- <本文件>` 仅 323ba7a79 一提交，该文件全部 diff 来自第十四轮提交自身）。
- **协作环境验活（按第十四轮警示执行，本轮通过）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活、检出 docs/necessity-gate-round14（=323ba7a79）且工作区干净——第十四轮重建的 worktree 未再被外部清理；go-cost worktree 仍标 prunable（deploy-lib 线对象，不属本特性，未处置）。C: 副本 lgw-p2test（`AppData/Local/lgw-p2test`）与 fails_r6/r7/r8.txt 三份基线留档均在位，fails_r8.txt 实测含 17 例失败行。本轮无代码改动，未重跑任何测试套件——被验证对象与第十四轮验证对象逐字节一致（bg/deploy 零变化），结论沿用；win/arm64 `-race` 不支持、promtool/Grafana 导入不可用口径不变。
- **顺带核实（只读）**：`web/src/views/probe/ProbeHealthPanel.vue` 约 203 行的陈旧 POST 注释仍在（第七/八轮记录项，实际代码走模型级 `/api/self-check/trigger`）——继续留作记录，不为注释单独起 web 提交。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（增量=第十四轮 docs 提交本身，bg/deploy 零变化）、观测五件套逐项复验、worktree/副本/基线留档验活。**环境受限**——promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）；win/arm64 `-race` 不支持未跑。**未验证**——观测链路是否已被运维实际 provisioning、生产指标状态（无通道）。
- **遗留风险**：与第十四轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。

## 审计记录（2026-09-12 第十六轮，针对 00cbdb595 第十五轮记录）

- **核验通过的主干断言**：第十五轮的合并态复核数据全部属实——`git diff 2027067ac..323ba7a79 --stat` 单文件（本 handoff，32+/15-），bg/ 与 deploy/ 空 diff；五件套计数 1/1/1/3/2 逐项复现；worktree 验活、fails_r8.txt=17 例失败行、ProbeHealthPanel.vue 陈旧注释三项均有会话内取证在案；第十五轮提交 00cbdb595 本身仅含本 handoff 单文件（24+/14-），与"零代码改动"声明一致。
- **发现 1（时效，实质记录项）**：第十五轮推送后 origin/main 再前进两跳——71a7055f6（R13 抽屉修复线的收尾审计文档 `docs/handoff/2026-09-11-r13-drawer-fixes-closure.md` +11 行，docs-only）与 0b711fc45（将其并入 main 的 merge）。增量定性：`git diff 00cbdb595..0b711fc45` 仅上述 r13 文件——**bg/、deploy/、本 handoff 零变化**，fails_r8.txt 基线与 deploy 契约结论继续有效；第十五轮"复核点 323ba7a79"随即被 0b711fc45 取代（第十二轮"docs-only 轮的复核点推送即过时"教训再次应验，下一轮提示词基准已更新至 0b711fc45）。
- **发现 2（复验方式空洞，表述修正，已就地修正第十五轮记录）**：第十五轮"本 handoff 文件未被并行线改动"的复验命令在当时 origin/main=323ba7a79 下是与自身 diff（恒空），不构成证据。结论本身成立，以提交清单实证另证：`git log 2027067ac..323ba7a79 -- <本文件>` 仅 323ba7a79 一提交，即该文件全部 diff 来自第十四轮提交自身。第十五轮记录已换用该口径。
- **发现 3（路径缺失，已补强"验证环境说明"）**：本 handoff 从未写明 C: 副本 lgw-p2test 的完整路径（散落在第三/六轮记录与协作记忆中，第十五轮提示词只加了"AppData/Local 下"）。本轮在"验证环境说明"节补全绝对路径，消除下一轮的定位成本。
- **范围界定**：本轮纯文档审计修正，零代码/SQL/deploy 工件改动；不重跑任何测试套件（第十五轮未产生新被测对象；bg/deploy 自第十四轮验证点起零变化，fails_r8.txt 判据继续有效）。
- **验证记录（如实区分）**：**通过**——第十五轮断言逐条取证（diff stat/提交清单/五件套计数）、新头两跳增量定性。**环境受限/未验证**——口径同第十五轮（promtool/Grafana 导入、win/arm64 `-race`、运维 provisioning 状态均无通道）。

## 本轮记录（2026-09-12 第十七轮，上线观察轮第十一轮——无输入记录阻塞 + 合并态复核 + 生产上线确认）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核（含生产上线事件定性）+ worktree/副本/基线留档验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 0b711fc45 前进三跳至 **4ad9615a6**——d84241b90（第十六轮审计文档本身，本 handoff 单文件 +19/-8）、20ea7d4f0（release bump 2083：VERSION、version.json、web/public/version.json 三处版本号 + 新增部署收尾文档 `docs/audit/2026-09-12-deploy-closeout-2083-bg-readsurface.md`）、4ad9615a6（merge 上述两条线）。`git diff 0b711fc45..origin/main -- bg/ deploy/` **双双为空**——fails_r8.txt（17 例）基线口径继续有效（无需重测）、deploy 契约测试结论沿用第十四轮（两包 ok）。观测五件套在 origin/main 逐项复验在位：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处；本 handoff 在该区间的改动仅 d84241b90 一提交（提交清单口径，即第十六轮审计自身）。
- **生产上线确认（事件定性，非 A 项输入）**：20ea7d4f0 的部署收尾文档记录 release **2083**（构建 `2.5.4-00cbdb59-20260911-2083`，代码基线 00cbdb595）已于 2026-09-12 凌晨经 deploy-seamless 蓝绿通道上线 245/154 两生产节点——**包含本特性全部代码**（necessity gate 及 P2/P3 跟进自 b37b68b51 起全部在基线内），系 2081 之后本特性首次确认的生产运行。收尾文档中与本特性相关的间接健康信号：PG `node_probe_runs` attempt 分布正常（双节点合并窗口 1:11/2:7/3:2/4:1/7:6，越界 = 0）、bg 接线（credRecovery + balance_quota_probe）两节点 ✅。**注意边界**：该批次携带的 Prometheus/Grafana 工件属 R12 raw-sink 线，本特性告警/面板是否已按 deploy/prometheus/README provisioning 仍无通道核实；attempt 分布正常也不构成 necessity 三指标的观测数据，**A 项门控状态不变**。收尾文档暴露的异常（candidate_failure_logs 写入停更、154 partition promote 23505、ursm.v2 persist collect failed）均属 R12/R13 线预存问题，与本特性零交集。
- **协作环境验活（本轮通过，一处环境事实更新）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活且干净，检出 docs/necessity-gate-round16（=d84241b90，已在 origin/main，落后 2 提交可 fast-forward）——第十六轮审计会话复用了该 worktree 并把分支直接合入 main（origin/docs/necessity-gate-round16 远端分支在）；**worktree 物理路径现为 `//Mac/Home/workspace/ai-native-tools/syncfield/lgw-necessity-p2`（即 Z: 盘的 Mac 侧同源路径，Windows 侧 git 访问正常）**，本轮已在其上 `-B` 出 docs/necessity-gate-round17（=4ad9615a6）；go-cost worktree 仍标 prunable（deploy-lib 线对象，未处置）。C: 副本 lgw-p2test 与 fails_r8.txt（实测 17 例失败行）在位。本轮无代码改动且 bg/deploy 零变化，未重跑任何测试套件——被验证对象与第十四轮验证对象逐字节一致，结论沿用；win/arm64 `-race` 不支持、promtool/Grafana 导入不可用口径不变。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（bg/deploy 双空 diff、五件套逐项复验、handoff 改动面=第十六轮提交自身）、worktree/副本/基线留档验活、2083 部署事件定性（收尾文档全文实读）。**环境受限**——promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）；win/arm64 `-race` 不支持未跑。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2083 上线 ≠ provisioning 完成）。
- **遗留风险**：与第十五/十六轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。

## 本轮记录（2026-09-12 第十八轮，上线观察轮第十二轮——无输入记录阻塞 + 合并态复核 + bg 基线随外部变更重测）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核（含 2084 上线事件与 cfl 读面假警报改判的定性）+ bg/ 基线重测（fails_r9.txt 留档）+ deploy 契约复跑 + 交叉编译 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 4ad9615a6 前进八跳至 **1c1b24542**——ea21b95f7（第十七轮 handoff 提交自身，其分支已进入 main 历史）、d249604c7（bg 读表面修复，详见下）、3c1195f2c（merge）、b85ce55a1（release bump **2084**-3c1195f2 + 2083 收尾文档修订段 +31 行）、8f4c13970（telemetry 694 自愈 + sql/migrations/startup/migration_694）、d82c948aa（admin/ursm 历史本地门禁还债测试）、aaf8d9edb 与 1c1b24542（docs-only）。增量定性：
  - **d249604c7（本轮 bg 基线重测对象）**：candidate_failure_logs 家族 6 处读面切既有 UNION 视图 `candidate_failure_logs_with_current_month`（candidate_failure_monitor 的 staleness max(ts)、checkAlerts 两处、auto-cool cfl CTE；`dailyProbeAuditSQL` 内层 candidate 分支 FROM；model_probe `applyPassiveBoosts`），视图由 V359/startup 392 已建，**无新迁移**；`recent_surface_reads_test.go` 守卫扩展——表面教义锁 candidate_failure_logs 面 3 文件 + `opslog_trimmer.go` 父表 DELETE 有意排除锁定（UNION 视图不可删，保留语义必须留在父表）。**`dailyProbeAuditSQL` 外层绑定链 EXISTS 未动（diff 只改内层分支 FROM）；necessity gate 全部文件（probe_necessity.go、probe_service.go、node_probe.go、pumpDueStatesSQL、两守卫测试文件）零变化；deploy/ 零变化。**
  - **handoff 在区间内的改动 = ea21b95f7 一提交**（提交清单口径，即第十七轮记录自身）；并行线未触碰本文件。
  - **观测五件套在 origin/main（1c1b24542）逐项复验在位**：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。
- **2084 上线事件定性（非 A 项输入）+ cfl 异常改判吸收**：b85ce55a1 及 2083 收尾文档修订段记录 release **2084**（构建基线 3c1195f2c）已于 2026-09-12 经蓝绿通道上线 245/154 两节点，`merge-base --is-ancestor` 证实 4ad9615a6 为 3c1195f2c 祖先（probe_necessity.go 在位）——**本特性随 2084 继续生产运行**。修订段的重要改判：2083 期间 154 的 candidate_failure_logs "写入停更"**实为读面假警报**（monitor staleness 探针读裸父表、写入方一直健康地写 hot 表），已随 d249604c7/2084 修复关闭——**第十七轮记录转述该异常时沿用了收尾文档当时的错误归因，本记录更正口径**；partition promote 23505（P5）与 ursm.v2 persist collect failed（P4）仍开放，仍属 R12/R13 线，与本特性零交集。2084 验证段"staleness 假警报归零、PG attempt 越界=0、bg 接线在位"仍只是间接健康信号，necessity 三指标依旧无生产观测数据，**A 项门控状态不变**。
- **bg/ 基线重测（本轮确定性产出；按任务书警示执行）**：d249604c7 移动 bg/ 后重测，**lgw-p2test/fails_r9.txt 为当前基线，fails_r8.txt 作废**。
  - **同步方法（CRLF 同口径，先校验后应用）**：以 d5be9203b 内容对副本既有 4 文件做 `git show <rev>:<file> | unix2dos` 后 cmp 逐字节校验（4/4 完全一致——证明副本处于第十四轮同步态且转换口径正确），再以 origin/main 版本同口径覆盖这 4 个文件；LF 归一 hash 清点仅剩 storage_retention_worker.go 桩一处差异（与第十三/十四轮口径相同）。
  - **结果**：全量 `go test -mod=vendor -count=1 ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc GOPROXY=off）→ **17 例失败，排序无关集合与 fails_r8.txt 完全一致**——d249604c7 未改变 Windows 预存失败面（16 例源文本断言 + 1 例 symlink 权限，Linux CI 不受影响）；扩展守卫 `TestRecentWindowReadsUseCurrentMonthSurface`（含新增 candidate_failure_logs 面断言）**通过**（不在失败集）。
  - **定向守卫族 25 例全绿**（第六轮同一 `-run` 正则）；deploy 契约 `./deploy/grafana/ ./deploy/prometheus/rules/` **两包 ok**（deploy/ 零变化，复跑为新头留档）。
  - **交叉编译**：`GOOS=linux GOARCH=arm64` zig cc `./bg/... ./cmd/gateway/...` 通过（C: 副本非 git 仓库，`go build` 须加 **-buildvcs=false**，本轮补记的复现细节）。win/arm64 `-race` 不支持，未跑。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（增量定性、五件套逐项复验、handoff 改动面=第十七轮提交自身）、bg 新基线对照（17 例集合与 fails_r8.txt 一致，fails_r9.txt 留档）、定向守卫 25 例、扩展守卫、deploy 契约两包、交叉编译。**环境受限**——源文本断言类 16 例在 CRLF 检出下无法 Windows 通过（Linux CI 职责）；win/arm64 `-race` 不支持未跑；promtool/Grafana 导入不可用（沿用第八/九/十轮口径）。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2083/2084 上线 ≠ provisioning 完成）。
- **遗留风险**：与第十五/十六/十七轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线三度被外部提交移动（fails_r6→r7→r8→r9），触碰 bg 前先以"最后一个移动 bg/ 的已知提交"（现为 d249604c7）为基准 diff 确认口径仍有效。

## 本轮记录（2026-09-12 第十九轮，上线观察轮第十三轮——无输入记录阻塞 + 合并态复核）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核（含 R15 线 2086 生产部署、alias 线 2086/2087 部署事件定性，及 P4/P5 收口口径同步）+ 协作环境验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 1c1b24542 前进 19 提交（含第十八轮自身提交 7f468a888）至 **a3fd0b7aa**，两条并行线交叠推进：
  - **closeout/R15 线**：46e3bc7e2（694 升级通道补齐）+ e3ff2e45f（free 操作白名单）+ 5a877aa9d（release 2085 本地部署）+ ebee70a8f/886860aa6（docs/merge）→ 494ff4df4（**P4 修复**：persist.Collect 先跳过 request_dedup marker 命名空间再解析）+ 5c58bf346/22019c638（**P5 迁移 694→695 renumber**，shared-DB ledger 撞号根治）+ 45ae17197（release **2086-5c58bf34 上线 245/154 生产**）+ 56a9b93ae（R15 收尾审计：conflict_pairs 7→0、hot 9.6 万行排空、父表 max(ts) 追平）。
  - **alias 线**：f494d0695（确定性别名解析根治）+ b89799340（release 2086-f494d069 部署）+ c2ce6f6e6（审计 rework 文档）+ 607a785f6（merge）+ afc6cf29f（release 2087-607a785f 本地部署）+ a3fd0b7aa（收尾文档）。
  - **增量定性**：`git diff d249604c7..a3fd0b7aa -- bg/ deploy/` **双双为空**——fails_r9.txt（17 例）判据继续有效（无需重测），deploy 契约测试结论沿用第十八轮（两包 ok）；necessity gate 全部文件（probe_necessity.go、probe_necessity_test.go、probe_service.go、node_probe.go、pumpDueStatesSQL、两守卫测试文件）零变化；sql/migrations 的 694→695 renumber 属 R15 线（probe 相关迁移零涉及）。
  - **本 handoff 在区间内的改动 = 7f468a888 一提交**（提交清单口径，即第十八轮记录自身）；并行线未触碰本文件。
  - **观测五件套在 origin/main（a3fd0b7aa）逐项复验在位**：alerts.yml 告警 1 处、alerts_test.go 告警契约钉 1 处（第 109-111 行三断言：告警名 + expr 指标 + wait=5m）、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。
  - **生产连续性实证**：`merge-base --is-ancestor` 证实 3c1195f2c（本特性基线）→ 45ae17197（2086-5c58bf34，245/154 在跑）→ 607a785f6（2087 构建基线）一脉相承，`bg/probe_necessity.go` 在 2087 基线 `cat-file` 在位——**本特性随 2085/2086/2087 连续在部署基线内，生产持续运行**。
- **P4/P5 收口口径同步（修正第十七/十八轮记录的"仍开放"表述）**：R14 观察文档（64ef31eeb）与 R15 收尾审计（56a9b93ae）记录——P5（partition promote 23505）经 694→695 接线根除 + renumber 后已上线自愈（conflict_pairs 7→0、hot 排空、父表追平）；P4（ursm.v2 persist collect failed 每 tick 必败、快照面停摆 5.7h+）根因为 persist.Collect 误吞 request_dedup marker 命名空间，494ff4df4 修复已随 2086 上线转绿。两项自始至终属 R12/R13/R15 线，与本特性零交集（第十七/十八轮转述收尾文档时"仍开放"的口径按当时文档属实，本记录同步至已收口状态）。**注意**：R14/R15 的生产日志采样（journalctl、252 PG 只读查证）覆盖面为 staleness/auto-cool/23505/57014/persist，**无 necessity 三指标采样**——A 项门控状态不变。
- **协作环境验活（本轮通过）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活且干净，检出 docs/necessity-gate-round18（=7f468a888，落后 origin/main 12 提交可 fast-forward），本轮已在其上 `-B` 出 **docs/necessity-gate-round19**（=a3fd0b7aa）；go-cost worktree 仍标 prunable（deploy-lib 线对象，未处置）。C: 副本 lgw-p2test 与 fails_r6–r9.txt 四份基线留档均在位，fails_r9.txt 实测 17 例失败行。本轮无代码改动且 bg/deploy 零变化，未重跑任何测试套件——被验证对象与第十八轮验证对象逐字节一致，结论沿用；win/arm64 `-race` 不支持、promtool/Grafana 导入不可用口径不变。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（bg/deploy 双空 diff、necessity 文件零涉及、五件套逐项复验、handoff 改动面=第十八轮提交自身、生产连续性 merge-base/cat-file 实证）、worktree/副本/基线留档验活。**环境受限**——promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）；win/arm64 `-race` 不支持未跑。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2084/2086 上线 ≠ provisioning 完成）。
- **遗留风险**：与第十五至十八轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线三度被外部提交移动（fails_r6→r7→r8→r9），触碰 bg 前先以"最后一个移动 bg/ 的已知提交"（现为 d249604c7）为基准 diff 确认口径仍有效。

## 本轮记录（2026-09-12 第二十轮，上线观察轮第十四轮——无输入记录阻塞 + 合并态复核）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核 + 协作环境验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 a3fd0b7aa 前进一跳至 **be3baaaad**——即第十九轮 handoff 提交本身（docs-only，提交清单口径，本 handoff 单文件；第十九轮分支已 fast-forward 进入 main）。`git diff d249604c7..origin/main -- bg/ deploy/` **双双为空**——fails_r9.txt（17 例）基线口径继续有效（无需重测）、deploy 契约测试结论沿用第十八轮（两包 ok）；necessity gate 全部文件（probe_necessity.go、probe_necessity_test.go、probe_service.go、node_probe.go、pumpDueStatesSQL、两守卫测试文件）零变化（probe_necessity.go `cat-file -e` 在位复验）。本 handoff 在区间内的改动 = be3baaaad 一提交（`git log a3fd0b7aa..origin/main -- <本文件>` 仅此一提交，即第十九轮记录自身）；并行线未触碰本文件。观测五件套在 origin/main（be3baaaad）逐项复验在位：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。生产连续性沿用第十九轮实证（3c1195f2c → 2086-5c58bf34 在跑 245/154 → 607a785f 2087 基线），本轮窗口内无新部署事件、无新生产信号。
- **协作环境验活（本轮通过）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活且干净，检出 docs/necessity-gate-round19（=be3baaaad，与 origin/main 同步——第十九轮提交即在该分支上完成并已进入 main），本轮已在其上 `-B` 出 **docs/necessity-gate-round20**（=be3baaaad）；go-cost worktree 仍标 prunable（deploy-lib 线对象，未处置）。C: 副本 lgw-p2test 与 fails_r6–r9.txt 四份基线留档均在位，fails_r9.txt 实测 17 例失败行。本轮无代码改动且 bg/deploy 零变化，未重跑任何测试套件——被验证对象与第十八轮验证对象逐字节一致，结论沿用；win/arm64 `-race` 不支持、promtool/Grafana 导入不可用口径不变。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（增量=第十九轮 docs 提交自身、bg/deploy 双空 diff、necessity 文件零涉及、五件套逐项复验、handoff 改动面=第十九轮提交自身）、worktree/副本/基线留档验活。**环境受限**——promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）；win/arm64 `-race` 不支持未跑。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2086/2087 上线 ≠ provisioning 完成）。
- **遗留风险**：与第十五至十九轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线三度被外部提交移动（fails_r6→r7→r8→r9），触碰 bg 前先以"最后一个移动 bg/ 的已知提交"（现为 d249604c7）为基准 diff 确认口径仍有效。

## 本轮记录（2026-09-12 第二十一轮，上线观察轮第十五轮——无输入记录阻塞 + 合并态复核）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核 + 协作环境验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 be3baaaad 前进一跳至 **d507b0f60**——即第二十轮 handoff 提交本身（docs-only，提交清单口径，本 handoff 单文件；第二十轮分支已 fast-forward 进入 main）。`git diff d249604c7..origin/main -- bg/ deploy/` **双双为空**——fails_r9.txt（17 例）基线口径继续有效（无需重测）、deploy 契约测试结论沿用第十八轮（两包 ok）；necessity gate 四文件（probe_necessity.go、probe_necessity_test.go、probe_service.go、node_probe.go）`cat-file -e` 逐一在位。本 handoff 在区间内的改动 = d507b0f60 一提交（`git log be3baaaad..origin/main -- <本文件>` 仅此一提交，即第二十轮记录自身）；并行线未触碰本文件。观测五件套在 origin/main（d507b0f60）逐项复验在位：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处（三断言：告警名 + expr 指标 + for）、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。生产连续性沿用第十九/二十轮实证（3c1195f2c → 2086-5c58bf34 在跑 245/154 → 607a785f 2087 基线），本轮窗口内无新部署事件、无新生产信号。
- **协作环境验活（本轮通过）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活且干净，检出 docs/necessity-gate-round20（=d507b0f60，与 origin/main 同步——第二十轮提交即在该分支上完成并已进入 main），本轮已在其上 `-B` 出 **docs/necessity-gate-round21**（=d507b0f60）；go-cost worktree 仍标 prunable（deploy-lib 线对象，未处置）。C: 副本 lgw-p2test 在位，fails_r9.txt 实测 17 例失败行。**主工作区脏文件清单较第二十轮增长**（新增 `M bg/taxonomy_sync.go`、`M discovery/alias_sync.go`、`?? bg/taxonomy_sync_alias_upsert_live_test.go`、`?? discovery/alias_sync_live_test.go`，及 vendor_pricing_table.py 的 D/?? 对）——均属并行工作线对象，维持不处置；注意两个未跟踪 `*_live_test.go` 落在 bg//discovery/ 目录内，后续同步 C: 副本若整目录拷贝会混入他线文件，副本同步仍以 `git show <rev>:<file>`（unix2dos 同口径）为准。本轮无代码改动且 bg/deploy 零变化，未重跑任何测试套件——被验证对象与第十八/二十轮验证对象逐字节一致，结论沿用；win/arm64 `-race` 不支持、promtool/Grafana 导入不可用口径不变。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（增量=第二十轮 docs 提交自身、bg/deploy 双空 diff、necessity 文件逐一在位、五件套逐项复验、handoff 改动面=第二十轮提交自身）、worktree/副本/基线留档验活。**环境受限**——promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）；win/arm64 `-race` 不支持未跑。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2086/2087 上线 ≠ provisioning 完成）。
- **遗留风险**：与第十五至二十轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线三度被外部提交移动（fails_r6→r7→r8→r9），触碰 bg 前先以"最后一个移动 bg/ 的已知提交"（现为 d249604c7）为基准 diff 确认口径仍有效。

## 本轮记录（2026-09-12 第二十二轮，上线观察轮第十六轮——无输入记录阻塞 + 合并态复核）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零代码改动，产出 = main 合并态复核 + 协作环境验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 d507b0f60 前进一跳至 **b366964c4**——即第二十一轮 handoff 提交本身（docs-only，提交清单口径，本 handoff 单文件 +13/-4；第二十一轮分支 docs/necessity-gate-round21 已进入 main）。`git diff d249604c7..origin/main -- bg/ deploy/` **双双为空**——fails_r9.txt（17 例）基线口径继续有效（无需重测）、deploy 契约测试结论沿用第十八轮（两包 ok）；necessity gate 四文件（probe_necessity.go、probe_necessity_test.go、probe_service.go、node_probe.go）`cat-file -e` 逐一在位。本 handoff 在区间内的改动 = b366964c4 一提交（`git log d507b0f60..origin/main -- <本文件>` 仅此一提交，即第二十一轮记录自身）；并行线未触碰本文件。观测五件套在 origin/main（b366964c4）逐项复验在位：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处（三断言：告警名 + expr 指标 + for）、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。生产连续性沿用第十九至二十一轮实证（3c1195f2c → 2086-5c58bf34 在跑 245/154 → 607a785f 2087 基线），本轮窗口内无新部署事件、无新生产信号。
- **协作环境验活（本轮通过）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活且干净，检出 docs/necessity-gate-round21（=b366964c4，与 origin/main 同步——第二十一轮提交即在该分支上完成并已进入 main），本轮已在其上 `-B` 出 **docs/necessity-gate-round22**（=b366964c4）；go-cost worktree 仍标 prunable（deploy-lib 线对象，未处置）。C: 副本 lgw-p2test 与 fails_r6–r9.txt 四份基线留档均在位，fails_r9.txt 实测 17 例失败行。主工作区脏文件清单与第二十一轮记录一致（bg/taxonomy_sync.go、discovery/alias_sync.go 两处 M + 两个未跟踪 *_live_test.go + docs 两处 + *.lnk），维持不处置。本轮无代码改动且 bg/deploy 零变化，未重跑任何测试套件——被验证对象与第十八/二十/二十一轮验证对象逐字节一致，结论沿用；win/arm64 `-race` 不支持、promtool/Grafana 导入不可用口径不变。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（增量=第二十一轮 docs 提交自身、bg/deploy 双空 diff、necessity 文件逐一在位、五件套逐项复验、handoff 改动面=第二十一轮提交自身）、worktree/副本/基线留档验活。**环境受限**——promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）；win/arm64 `-race` 不支持未跑。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2086/2087 上线 ≠ provisioning 完成）。
- **遗留风险**：与第十五至二十一轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线三度被外部提交移动（fails_r6→r7→r8→r9），触碰 bg 前先以"最后一个移动 bg/ 的已知提交"（现为 d249604c7）为基准 diff 确认口径仍有效。

## 本轮记录（2026-09-12 第二十三轮，上线观察轮第十七轮——无输入记录阻塞 + 合并态复核 + bg 基线随外部变更重测）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零特性代码改动，产出 = main 合并态复核（会话窗口内 origin/main 三次前进的增量定性）+ bg/ 基线重测（fails_r10.txt 留档）+ deploy 契约复跑 + 协作环境验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查，窗口内三次前进）**：origin/main 自 1ade98450 前进五提交至 **6b769063d**，三段增量：
  - **59c5dda12 / 2bade0a2d / 2895540c2（DB 分区时区线）**：59c5dda12（fix(db): 分区 ensure 函数钉 Asia/Shanghai，新迁移 `sql/migrations/startup/694_partition_ensure_timezone{,.down}.sql` + `sql/objects/functions` 10 个分区 ensure 函数同步改写）、2bade0a2d（merge）、2895540c2（fix(db,installer): 关闭迁移 694 的 upgrade-db 通道 + installer embeddata/dbinit 同步）——共 21 文件（sql/migrations、sql/objects、sql/schema、deploy/sql/schemas/baseline、installer/、scripts/apply-db-revision-sequence.sh、docs/db-changelog.md）；694 号段在 R15 renumber 出 695 后重用于新迁移，探测相关迁移零涉及。
  - **83bf582dd（本轮 bg 基线重测对象）**：feat(db,bg) 迁移 696——request_logs 视图追加 system_fingerprint 列，drift scanner 读面从裸父表切到 `request_logs_with_current_month`；bg/ 动 2 文件（`integrity_fingerprint_drift.go` FROM 切换+注释、`recent_surface_reads_test.go` 的"有意排除"守卫翻转为"必须读视图"断言），另有 db/request_logs_view_schema{,_test}.go、迁移 696 文件+测试、scripts/apply-db-revision-sequence.sh。**necessity gate 四文件（probe_necessity.go、probe_necessity_test.go、probe_service.go、node_probe.go）零变化**（`cat-file -e` 在 83bf582dd 复验）；bg 侧为纯 SQL 字符串改动、无新增 Go 符号依赖，db/ 包变化不影响 bg 编译面。
  - **6b769063d（696 线 docs 收尾，本记录推送间隙到达，随本补注吸收）**：docs-only 单文件（`docs/changelogs/2026-09-12-request-logs-view-system-fingerprint.md` +19，注明 696 迁移已应用于生产 DB、双形态理由与休眠列发现）——bg/、deploy/、本 handoff 零涉及（`git diff 83bf582dd..6b769063d -- bg/ deploy/` 为空，五件套在 6b769063d 计数不变）。注意其 "production applied" 指 696 **DB 迁移**应用于生产共享库，非网关构建部署事件。
  - **bg/ 基线重测（按任务书警示执行；"最后一个移动 bg/ 的已知提交"由 d249604c7 更新为 83bf582dd）**：
    - 同步方法（CRLF 同口径，先校验后应用）：两个 bg 文件先以 2895540c2 版本 `git show <rev>:<file> | unix2dos` 对副本既有文件 cmp 逐字节校验（2/2 完全一致，证明副本处于 2895540c2 态且转换口径正确），再以 83bf582dd 版本同口径覆盖；副本相对新头的 bg 差异仅此 2 文件（storage_retention_worker.go 桩为长期既有差异）。
    - 结果：全量 `go test -mod=vendor -count=1 ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc GOPROXY=off）→ **17 例失败，排序无关集合与 fails_r9.txt 完全一致**——83bf582dd 未改变 Windows 预存失败面（16 例源文本断言 + 1 例 symlink 权限，Linux CI 不受影响）。**lgw-p2test/fails_r10.txt 留档为当前基线，fails_r9.txt 作废**（集合相同，留档为保持"每次 bg 移动后重留档"惯例）。
    - 定向守卫全绿：第六轮 25 例正则族 + 扩展守卫 `TestRecentWindowReadsUseCurrentMonthSurface`（含 83bf582dd 翻转后的"drift scanner 必须读视图"新断言）一并 PASS；deploy 契约复跑 `./deploy/grafana/ ./deploy/prometheus/rules/` → **两包 ok**。
  - **deploy/ 判据口径修正**：2895540c2 段动了 deploy/（唯一文件 `deploy/sql/schemas/baseline/01-schema.sql`，115+/39-，baseline schema 与新迁移同源同步），既往"bg/ 与 deploy/ 零变化"整体表述自本轮起作废；但 `git diff d249604c7..83bf582dd -- deploy/grafana/ deploy/prometheus/` **为空**且 83bf582dd 不触 deploy/——deploy 契约结论有效并已复跑留档。后续复核改用双子判据：bg/ 单独 + deploy/{grafana,prometheus}/ 子树（下一轮提示词已同步）。
  - 本 handoff 在 1ade98450..6b769063d 区间零改动（三段 `git log -- <本文件>` 均空，并行线未触碰）；`merge-base --is-ancestor` 证实 3c1195f2c（本特性生产基线）仍是 6b769063d 祖先，三段增量均无网关 release bump——生产连续性沿用第十九至二十二轮实证（3c1195f2c → 2086-5c58bf34 在跑 245/154 → 607a785f6 2087），origin/main 线本轮无网关构建部署事件（6b769063d 的 "production applied" 属 DB 迁移应用，见上）。观测五件套在 origin/main（6b769063d）逐项复验在位：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处（三断言）、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。
- **主工作区并行线动态（记录，不处置）**：第二十一/二十二轮记录的 4 个脏文件（bg/taxonomy_sync.go、discovery/alias_sync.go 两处 M + 两个未跟踪 `*_live_test.go`）已由 alias 线提交为 c6676f304（**本地 main，未推送 origin**），随行还有本地 release bump 2088-d60511146 与 govern docs 提交 17ecc8ada——本地 main 与 origin/main 现分叉 3/5；新增未跟踪 `docs/handoff/2026-09-12-alias-onconflict-rootfix-whitelist-runbook.md`。以上均属 alias 线对象，维持严禁触碰；本地 main 的推送/合并由该线自理，本特性一切以 origin/main 为准。
- **协作环境验活（本轮通过）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活且干净，检出 docs/necessity-gate-round22（=1ade98450，与 fetch 前 origin/main 同步），本轮已在其上 `-B` 出 **docs/necessity-gate-round23**（起步=2895540c2，复核期间 origin/main 先后前进至 83bf582dd、6b769063d，均已吸收进本记录与重测；本分支最终落于 6b769063d 之上）；go-cost worktree 仍标 prunable（deploy-lib 线对象，未处置）。C: 副本 lgw-p2test 与 fails_r6–r10.txt 基线留档均在位，fails_r10.txt 实测 17 例失败行。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（两段增量定性、necessity 文件逐一在位、五件套逐项复验、handoff 区间零改动、生产基线祖先实证）、bg 基线重测全流程（CRLF 口径先校验后应用 → 全量 17 例集合与 fails_r9 一致 → fails_r10.txt 留档）、定向守卫族 + 扩展守卫、deploy 契约两包复跑。**环境受限**——16 例源文本断言 + 1 例 symlink 在 CRLF 检出下无法 Windows 通过（Linux CI 职责）；win/arm64 `-race` 不支持未跑；promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2086/2087 上线 ≠ provisioning 完成）。
- **遗留风险**：与第十五至二十二轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线四度被外部提交移动（fails_r6→r7→r8→r9→r10），触碰 bg 前先以"最后一个移动 bg/ 的已知提交"（现为 83bf582dd）为基准 diff 确认口径仍有效；deploy/ 整体零变化判据自本轮作废，改以 deploy/{grafana,prometheus}/ 子树为准。

## 本轮记录（2026-09-12 第二十四轮，上线观察轮第十八轮——无输入记录阻塞 + 合并态复核 + bg 基线随外部变更重测）

- **输入缺失声明（按 D 项要求如实区分）**：任务书未携带生产 /metrics 数据或告警触发记录，运维反馈未到达。按分派规则 **A 不触发**（TTL 抑制继续锁在数据门后）、**B 不触发**（显示侧治本修复继续锁在运维反馈门后，应急口径三层已就绪）、**C 维持关闭**（无乱序完成反例）。本轮零特性代码改动，产出 = main 合并态复核（alias 线推送收敛 + 697 telemetry 线增量定性）+ bg/ 基线重测（fails_r11.txt 留档）+ deploy 契约复跑 + 协作环境验活 + 本记录。
- **main 合并态复核（只读，git fetch 实查）**：origin/main 自 6b769063d 前进七提交至 **8af09c799**，两段增量：
  - **alias 线推送收敛（本地分叉消除）**：c6676f304（alias ON CONFLICT (raw_name) 根治——**移动 bg/**：`bg/taxonomy_sync.go` +43/-4 + 新增 `bg/taxonomy_sync_alias_upsert_live_test.go` +160，即第二十一/二十二轮记录的脏文件与未跟踪文件转正）+ d60511146（release bump **2088-c6676f304**，提交说明自述 "live on **local** deploy"——本地部署，非生产节点事件）+ 17ecc8ada（govern docs）+ c183aa30b / 83fc3530f（第二十三轮记录与补注自身）+ 147939cb7（merge origin/main——第二十三轮记录的"本地 main 与 origin/main 分叉 3/5"就此收敛，本地 main 现为 147939cb7、严格落后 origin/main 可 fast-forward）。
  - **697 telemetry 线**：d03f0ada4（X-System-Fingerprint 写入路径接线：request_logs 列 + promote 携带，新迁移 697 三件 + `domains/hooks/observability/telemetry/client.go` + `domains/streaming/handler.go` + `deploy/sql/schemas/baseline/01-schema.sql` + installer embeddata + scripts/apply-db-revision-sequence.sh + docs/changelogs）+ 8af09c799（promote 695→697 intentional function chain 注册：scripts + migration_697_test + changelog 补注）——系 696 线（第二十三轮记录的"休眠列"）的写入侧跟进，与本特性零交集。
  - **增量定性**：`git diff 83bf582dd..origin/main -- bg/` 仅上述 2 个 taxonomy 文件——**necessity gate 全部文件零变化**（probe_necessity.go、probe_necessity_test.go、probe_service.go、node_probe.go、node_probe_submit_gate_test.go `cat-file -e` 逐一在位）；`git diff d249604c7..origin/main -- deploy/grafana/ deploy/prometheus/` **为空**——deploy 契约结论有效并已复跑留档；697 线触 deploy/sql/schemas/baseline（非契约判据面）。本 handoff 在区间内的改动 = c183aa30b + 83fc3530f 两提交（`git log -- <本文件>` 实查，即第二十三轮自身；alias/697 线均未触碰）。观测五件套在 origin/main（8af09c799）逐项复验在位：alerts.yml 告警 1 处、alerts_test.go 契约钉 1 处、面板 uid 1 处、dashboard_test.go 恰 3 个 Test 函数、README 引用 2 处。
  - **生产连续性**：`merge-base --is-ancestor` 证实 3c1195f2c（本特性生产基线）仍是 8af09c799 祖先；区间内唯一 release bump 2088 为**本地部署**（提交说明自述），origin/main 线无新生产部署事件——245/154 生产节点沿用第十九轮以来实证（3c1195f2c → 2086-5c58bf34 在跑 → 607a785f6 2087）。
- **bg/ 基线重测（本轮确定性产出；按任务书警示执行；"最后一个移动 bg/ 的已知提交"由 83bf582dd 更新为 **c6676f304**）**：
  - 同步方法（CRLF 同口径，先校验后应用）：先以 83bf582dd 版本 `git show <rev>:<file> | unix2dos` 对副本既有 `bg/taxonomy_sync.go` cmp 逐字节校验（完全一致，证明副本处于第二十三轮同步态且转换口径正确），并确认 live test 文件在副本不存在；再以 origin/main 版本同口径覆盖两个文件。
  - 新 live 测试语义核实：`TestTaxonomyUpsertAlias_Live` 无 `TEST_DATABASE_URL` 时 `t.Skip`（文件头注释自述 live-DB regression，需真实 PG 才跑）——本地全量套件中自动跳过，不影响失败集口径。
  - 结果：全量 `go test -mod=vendor -count=1 ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc GOPROXY=off）→ **17 例失败，排序无关集合与 fails_r10.txt 完全一致**——c6676f304 未改变 Windows 预存失败面（16 例源文本断言 + 1 例 symlink 权限，Linux CI 不受影响）。**lgw-p2test/fails_r11.txt 留档为当前基线，fails_r10.txt 作废**（集合相同，按"每次 bg 移动后重留档"惯例重留档）。
  - 定向守卫族 26 例全绿（第六轮 25 例正则 + 扩展守卫 `TestRecentWindowReadsUseCurrentMonthSurface`）；deploy 契约复跑 `./deploy/grafana/ ./deploy/prometheus/rules/` → **两包 ok**（复跑前以 LF 归一 hash 逐一核对副本 5 个契约文件与 origin/main 完全一致）。
  - 交叉编译：`GOOS=linux GOARCH=arm64` zig cc（-buildvcs=false）`./bg/` 通过——覆盖 c6676f304 移动的 bg 代码在部署目标的编译面；`./cmd/gateway/...` 本轮**未重验**（其依赖面 domains/streaming/handler.go、telemetry client 属 697 线且未同步副本，编译只会验证陈旧内容，如实标注不覆盖；697 线自验职责）。
- **环境注记（fetch 维护性失败，留观不处置）**：本轮 `git fetch` 成功（refs/对象正常更新）但随后的 geometric repack 失败（"failed to clear multi-pack-index ... failed to perform geometric repack"）——SMB 网络盘上 git 自动维护的已知脆弱点，不影响 refs 与本轮任何操作结论；共享仓库不宜单方跑 gc/修复，留观，若后续 fetch 报对象缺失再行处置。
- **协作环境验活（本轮通过）**：`git worktree list` + worktree 内 `git status` → lgw-necessity-p2 存活且干净，检出 docs/necessity-gate-round23（=83fc3530f，其两提交已随 147939cb7 进入 origin/main），本轮已在其上 `-B` 出 **docs/necessity-gate-round24**（=8af09c799）；go-cost worktree 仍标 prunable（deploy-lib 线对象，未处置）。C: 副本 lgw-p2test 与 fails_r6–r11.txt 基线留档均在位，fails_r11.txt 实测 17 例失败行。主工作区脏文件清单与 git status 起始快照一致（vendor_pricing_table.py D/?? 对、incidents 文档 M、deploy-lib D + 两个 *.lnk），维持严禁触碰。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——合并态复核（两段增量定性、necessity 文件逐一在位、五件套逐项复验、handoff 区间改动=第二十三轮自身、生产基线祖先实证）、bg 基线重测全流程（CRLF 口径先校验后应用 → 全量 17 例集合与 fails_r10 一致 → fails_r11.txt 留档）、定向守卫族 26 例、deploy 契约两包复跑（副本契约文件 hash 先行核对）、交叉编译 ./bg/。**环境受限**——16 例源文本断言 + 1 例 symlink 在 CRLF 检出下无法 Windows 通过（Linux CI 职责）；win/arm64 `-race` 不支持未跑；promtool 与 Grafana 导入不可用（沿用第八/九/十轮口径）；cmd/gateway 交叉编译未覆盖（见上，697 线自验）。**未验证**——本特性告警/面板是否已被运维实际 provisioning、necessity 三指标生产值（无通道；2086/2087 生产在跑、2088 本地部署 ≠ provisioning 完成）。
- **遗留风险**：与第十五至二十三轮相同，无新增。剩余开启项全部外部门控：TTL 抑制 ← 生产 failed_total 数据；显示侧治本 ← 运维反馈；孤儿行批量 SQL 执行前 ← 只读副本核数；工作区脏文件 ← 原作者确认。环境注记：bg 基线五度被外部提交移动（fails_r6→r7→r8→r9→r10→r11），触碰 bg 前先以"最后一个移动 bg/ 的已知提交"（现为 c6676f304）为基准 diff 确认口径仍有效【第二十五轮审计修正：bg-mover 指针仅作历史标注；**diff 基准一律用当轮复核点**——c6676f304 经 147939cb7 merge 合入且早于 83bf582dd，直接以其为基准会把 696 线的 bg 变更误报为"新增移动"（本轮实测复现）】；deploy 判据维持 deploy/{grafana,prometheus}/ 子树；fetch geometric repack 失败留观（见上）。

## 本轮记录（2026-09-12 第二十五轮，运维输入落地——观测需求钉定 245/154 双机 + 观测链路审计与修正）

- **输入到达声明（与既往"无输入"轮的区别）**：任务书到达运维指令"需要观察 **154 与 245** 这两台机器"——构成遗留项 6 观测需求的**目标钉定**。注意边界：这是目标输入而非指标数据输入，A 项触发条件（拿到 /metrics 数值或告警触发记录）本轮依旧未满足，TTL 抑制继续锁在数据门后。本轮产出 = 需求修正（抓取面/runbook/面板口径全部钉到双机）+ 观测链路端到端审计（3 处实质缺口 + 1 处上轮指引缺陷）+ 修正实施（deploy 工件 + test-only，零网关代码）。
- **审计发现（结合现有方案：`deploy/prometheus/NATIVE-245-DEPLOY.md` 记载的 245 原生 Prometheus 栈）**：
  - **发现 1（实质，抓取面缺口）**：生产 Prometheus 实际常驻 245（原生 systemd `/opt/monitoring/`，2026-08-18 credential-17 加固链交付，**非** deploy/prometheus 的 compose 栈）；其适配版 prometheus.yml 只有一个网关目标 `127.0.0.1:8781`——①**154 完全在抓取面外**（双机需求的半边盲区）；②**固定 8781 端口在蓝绿轮换后落空**（2083 收尾文档实测：切换后 active 端口 8781/8782 轮换，"直连 8781 的探测会落空"）——抓取恰在发版窗口集体致盲，且序列断流后 `increase[10m]+for 5m` 告警静默 resolve，属"最需要监控时失效"形态。
  - **发现 2（实质，面板口径缺口）**：skip 面板 expr 为 `sum by (reason)`——跨实例聚合，两台机的 per-node skip 异常互相淹没；retry/failed 面板图例为静态文本，双机两系列将同名混淆。与"观察两台机器"直接冲突。
  - **发现 3（文档缺口）**：deploy/prometheus/README 的 provisioning 流程是 compose 形态；生产实际惯例是 NATIVE-245-DEPLOY 的"规则与仓库同源 → promtool check → POST /-/reload"。necessity 告警与双机抓取在 245 原生栈的接入步骤此前无落地文档。另：**Grafana 不在 245 原生栈内**（只有 Prometheus + Alertmanager）——面板的生产可用性受 Grafana 落点限制，须如实标注而非沿用"导入即验"。
  - **发现 4（上轮指引缺陷，已修正）**：第二十四轮下一轮提示词的 bg-diff 基准 `git diff c6676f304..<新头> -- bg/` 有 merge 拓扑缺陷——c6676f304（alias 线，经 147939cb7 合入）**早于** 83bf582dd（696 线），从它 diff 会把 696 的 bg 变更误报为"又移动"（本轮开工实测复现：该 diff 非空，含 integrity_fingerprint_drift.go）。修正：**bg 增量复核一律以上一轮复核点为 diff 基准**（本轮复核点 = d0d5b7e45，开工实查 bg/ 与 deploy 契约子树零变化）；"最后移动 bg/ 的提交"指针仅作历史标注。round-24 遗留风险行已就地加注。
  - **发现 5（运维事实核验，支撑修正）**：两机 systemd 单元名不同（245=`llmgo-245-canary@<port>`、154=`llm-gateway-go-canary@<port>`，2083 收尾与 09-10 审计文档双证）；/metrics 与业务同端口监听（8781/8782）；245 原生栈 admin token 已落盘 `/opt/monitoring/prometheus/secrets/admin_token`；**两机 `LLM_GATEWAY_ADMIN_API_KEY` 是否同值未验证**（接入步骤含 401 分叉处置：不同则 154 需拆专属 job + 独立 token 文件）。
- **修正实施（4 文件，deploy 工件 + test-only）**：
  1. `deploy/prometheus/prometheus.yml`：末尾新增 `llm-gateway-prod` **注释模板**——245+154 双节点 × a/b 双槽位共 4 目标，`instance` 标签钉为 `gateway-<node>-<slot>`（轮换不漂移），bearer_token_file 说明，双机抓取的蓝绿原理注释；注释形态对 compose 栈零影响（yaml 解析复验仍 4 jobs）。
  2. `deploy/prometheus/NATIVE-245-DEPLOY.md`：新增"自检必要性（necessity gate）双机观测接入（2026-09-12）"节——245 原生栈落地版抓取块（`<HOST_154_ADDR>` 占位）、`promtool check config` + `/-/reload` 接入步骤、连通性/鉴权预检 curl（含 401=两机 key 不同、000=网络不通的分叉语义）、接入后 `api/v1/series` 与 `api/v1/rules` 验收命令、按 instance 分节点判读规则（对齐第六轮 runbook）、蓝绿轮换后计数器从 0 重新累计的预期语义、Grafana 边界声明。
  3. `deploy/grafana/selfcheck-necessity-dashboard.json`：skip 面板 expr 改 `sum by (instance, reason)`、图例 `{{instance}} {{reason}}`；retry/failed 图例加 `{{instance}}` 前缀。**契约测试先行**：新增 `TestSelfcheckNecessityDashboardSkipPanelBreaksDownByInstance`（钉 skip expr 必须保留 instance 维度），改动前实测红（"does not contain sum by (instance, reason)"）→ 改后绿。
  4. `deploy/grafana/README.md`：面板节补"双机观测口径"段（instance 钉定要求 + 未接入时面板为空属未接入非故障）。
- **硬条件核对**：网关代码/SQL/闸门路径零改动；alerts.yml 零改动（告警本身 per-series 语义正确，instance 钉定后自动带节点标签触发）；compose 栈行为零变化（prometheus.yml 仅追加注释）。
- **验证记录（如实区分）**：**通过**——新契约测试先红后绿；`go test -mod=vendor -count=1 ./deploy/grafana/ ./deploy/prometheus/rules/`（C: 副本，5 个变更文件同步后）= **grafana 4 例 + rules 6 例全 PASS**；prometheus.yml 以 vendored yaml.v3 解析复验 OK（4 jobs 不变）；发现 5 的运维事实均有仓库文档出处（2083 收尾 / 09-10 自检恢复审计 / NATIVE-245-DEPLOY）。**环境受限**——promtool 本机不可用（245 上有 2.47.0，接入步骤已含 `promtool check config`，并沿用该文档"以 /-/reload 实际结果为准"的教训）；Grafana 导入未实测（无实例）；win/arm64 `-race` 不支持未跑。**未验证**——245 侧实际接入（/opt/monitoring 编辑 + reload）需运维上机执行；154 自 245 的网络可达性、两机 admin key 同值性均未知（接入步骤含预检）；接入完成前生产 necessity 指标依旧无通道。
- **遗留项 6 状态更新**：观测目标已钉定（245/154 双机 × 双槽位，instance=gateway-<node>-<slot>）；**A 项判读等待"245 侧接入完成后的首批数据"**，判读规则不变（第六轮 runbook），按 instance 分节点执行；新增前置链：运维接入（NATIVE 文档节）→ Prometheus `/api/v1/series` 验收 → 数据判读。
- **遗留风险**：①245 侧接入是运维手工步骤，未执行前告警/面板对双机均无数据——本轮交付的是"接入即可用"的全部仓库侧材料；②154 可达性与 key 同值性两个前置未验证；③Grafana 落点未定，面板生产可用性待定（Prometheus UI/`api/v1` 判读不受影响）；④若运维直接启用模板而跳过 instance 钉定，`sum by (instance)` 将回落 host:port 形态（轮换后断系列）——模板与 NATIVE 文档均已写明钉定要求；⑤既往风险（TTL 抑制/显示侧治本/批量 SQL/脏文件门控）不变。
- **补注（同轮推送窗口内的 main 前进与 bg 第六度移动，已吸收）**：round-25 记录提交（7124fcf1f）推送时 origin/main 已前进七提交至 055cecc78（694 rollback replace-safe、695 installer 同步、logging frameIndex、**65782814 bg candidate_failure_logs 分区预构建**、modelname alias 顺序、24h 审计文档、merge）——rebase 干净后以 **17de96f12** ff 合入 main 并推送。增量定性：外部 bg 变更仅 `bg/partition_manager.go`(+13)/`_test.go`(+4)，**necessity gate 文件与 deploy 契约面零涉及**（deploy 五文件 diff 全为本轮自身提交）。bg 基线随之重测：两 partition 文件先校验（副本 == d0d5b7e45 逐字节一致）后应用 origin/main；全量套件**首轮出现 2 例新增失败**（TestProbeServiceLeaseHeartbeatExtendsDuringRun / TestProbeServiceHeartbeatExtendsLeasePeriodically，报"heartbeat 0 次/never fired"，窗口仅 0.07/0.13s）——定向复跑 **3/3 过**、全量复跑 **17 例集合与 fails_r11 完全一致**，确证为并发编译/测试负载下的**时序偶发**而非 65782814 引入（两例 ticker goroutine 与测试窗口竞争）。**fails_r12.txt 留档为当前基线，fails_r11 作废（集合相同）**。守卫族 26 例绿；交叉编译 ./bg/（含新 partition 内容）通过。**新环境观察：两例租约心跳测试时序敏感，本机重负载下会抖——后续轮次遇失败集合失配，先定向复跑再定性，勿直接判回归。**主工作区脏文件清单再扩大（并行线活跃）：新增 M `admin/models.go`、M `bg/taxonomy_sync.go`、M `bg/taxonomy_sync_alias_upsert_live_test.go`、M `discovery/alias_sync.go`、M `scripts/govern-junk-canonical/plan_test.go`（系 modelname/alias 线在途改动）——维持严禁触碰。本轮最终复核点 = **17de96f12**（bg 已在该内容上重测留档）。

## 本轮记录（2026-09-12 第二十六轮，上线观察轮第十九轮——无输入记录阻塞 + 245/154 只读预检三前置闭环 + 接入材料实测修正）

- **任务选择说明**：按第二十五轮提示词分派——A 项数据门（245 侧运维接入后首批数据）未开，B/B' 均无运维反馈输入，进入"无输入则如实记录阻塞"分支。本轮新增实质交付：发现 245 的 Prometheus 可经 ssh 只读通道探测（`~/.ssh/config` 在役主机 245=8.136.114.245/154=47.97.111.154，2026-09-11 免密已验证），遂把二十五轮标注"未验证"的接入前置与 B' 401/000 分叉**提前实测闭环**，并把实测结论回写进接入材料（deploy/prometheus/ 3 文件 + grafana README）——运维接入路径上的所有假设清零，照做即用。
- **预检结论（全部只读：curl GET / grep / ss / systemctl list-units / key 指纹；未在生产机执行任何写操作）**：
  1. **A 项数据门未开（接入未执行，实测）**：245 Prometheus 在线（`/-/ready` OK），但 active targets 仅旧 `llm-gateway`（127.0.0.1:8781）+ 自抓两个；`api/v1/rules` 无任何 Necessity 规则；`api/v1/series` 查 necessity 指标为空。二十五轮遗留风险①由推测升级为实测确认。**阻塞维持：等运维执行 NATIVE 文档接入步骤。**
  2. **两机 admin key 不同值（B' 401 分叉坐实）**：245 token 打 154:8781 返回 **401**（网络通，HTTP 鉴权层应答）；key 指纹比对（len/MD5 前 8 位，仅为比对入库，明文未入库）245 len=67/`8c828877` vs 154 len=51/`f7a0c4c2`。→ 模板从"单 job 4 目标 + 401 时再拆"改为**双 job 已验证形态**（`llm-gateway-prod-245`/`-154` 各挂 bearer_token_file；步骤 0 新增 154 token 文件 `admin_token_154` 创建法，含"勿把 key 明文写进仓库"约束）。
  3. **154 单实例（B' 000 分叉坐实）**：仅 `llm-gateway-go-canary@8781.service` 运行（ss 实测 8782 无监听；245→154:8782=000）。→ 154 只配 `gateway-154-a`；154-b 目标注释保留，154 起蓝绿 b 槽位后再启用。
  4. **skip_total 懒创建（二十五轮文档缺陷，本轮修正）**：两机 /metrics 均只有 failed/retry 两条 0 值系列、**无 skip 系列**。代码核对：`probeNecessitySkipTotal` 为带 `reason` 标签的 CounterVec 且无 sentinel 预热（`bg/probe_necessity.go:79` 定义；`bg/probe_service.go:319` 的 `WithLabelValues(reasonCode).Inc()` 是唯一子系列创建点）；failed/retry 为无标签 Counter 注册即以 0 暴露。根因：二十五轮把 credential 指标的"注册即预热 sentinel"先例错误套用到 skip_total（该措辞原样进了 NATIVE 验收节）。后果若不修：运维照文档验收"应见 4 条 skip 系列"必误报 B'（验收不过）。修正：**接入验收判据改用 `mirror_delete_failed_total` 0 值系列（3 instance），skip_total 懒创建语义写入 NATIVE 文档与 grafana README**（已接入但 skip 泳道为空 ≠ 故障）。
  5. **LLMGatewayDown 依赖旧 job 名（审计新发现）**：`alerts.yml` 的 `up{job="llm-gateway"} == 0`（critical）——若运维按指引"接入后移除旧 job"该告警永久静默，若保留旧 job 则无钉定双系列回归。修正：expr 改 `up{job=~"llm-gateway.*"}`，NATIVE 步骤明确"退役旧 llm-gateway job"。
  6. **生产二进制含 necessity gate（间接确认）**：两机 /metrics 均暴露 gate 指标 → 二进制在 11af45216 线之后（指标与闸门同文件交付，接线同批）。无应用版本指标，无法钉死确切版本；skip 系列为空与"接线生效且尚无 skip 事件"一致（亦与"探测队列无任务"一致），不构成告警信号。两机 failed/retry=0（进程启动以来无镜像删除翻动）——弱正面信号，不构成遗留项 6 收敛证据。
- **改动文件（4，deploy 契约面 + 文档；零 Go/SQL/闸门路径改动）**：
  1. `deploy/prometheus/prometheus.yml`：`llm-gateway-prod` 注释模板重写为实测双 job 形态——instance 钉定直接进模板主体（原钉定只存在于双层注释示例里，运维直接取消单层注释启用会落到无钉定 host:port 漂移形态=二十五轮风险④）、154 地址填实测值 `47.97.111.154`（公网，与 09-10 审计文档已含 245 公网 IP 同敏感级；若运维后续给内网路由则替换复测）、154-b 注释保留、双 bearer_token_file、旧 job 退役注记。**非注释行零变化**（`diff --strip-trailing-cr` 复验改前改后逐行一致）——compose 栈行为零变化。
  2. `deploy/prometheus/NATIVE-245-DEPLOY.md`：接入节重写——预检结果块（上述 6 条）、步骤 0（154 token 文件）、双 job 抓取块、旧 job 退役步骤、验收判据改 failed_total、skip 懒创建说明、判读补充、"预检为只读探测"边界声明。
  3. `deploy/prometheus/rules/alerts.yml`：LLMGatewayDown expr 通配化（见预检结论 5）。
  4. `deploy/grafana/README.md`：双机口径段更新（模板新名 + 已接入后 skip 泳道为空的懒创建语义）；指标说明节 skip_total 补懒创建注记。
- **验证记录（如实区分）**：**通过**——`go test -mod=vendor -count=1 ./deploy/grafana/ ./deploy/prometheus/rules/`（C: 副本 lgw-p2test，4 个变更文件同步后）= **10 例全 PASS（grafana 4 + rules 6，与二十五轮同口径）**；prometheus.yml 非注释行改前改后逐行一致。**环境受限**——promtool 本机不可用（245 上有 2.47.0，接入步骤已含 `promtool check config`）；Grafana 导入未实测（无实例）。**未验证**——运维侧实际接入（/opt/monitoring 编辑 + reload + 验收）仍待执行，本轮全部为只读预检 + 仓库侧材料修正；alerts.yml 修改在 245 侧的生效随接入步骤第 4 步（alerts.yml 同步）一并发生，接入前 245 现网 `up{job=~"llm-gateway.*"}` 与旧 expr 行为等价（现网只有 job llm-gateway）。
- **bg/ 基线第七度移动（本会话窗口内，fa106fef2..5bcd87dae）**：`bg/partition_manager.go`(+22，分区时区修正，694/699 后续) + `bg/taxonomy_sync.go`(+9)/`bg/taxonomy_sync_alias_upsert_live_test.go`(+31)（alias 线）。**fails_r12.txt（17 例）源文本口径随之待重测**——本轮零 bg 改动、未跑 bg 套件，按二十五轮先例重测留给下次触碰 bg 的轮次（下轮提示词已标注）。deploy/{grafana,prometheus}/ 契约子树 fa106fef2..5bcd87dae 零变化（`deploy/sql/schemas/baseline` 变化不属契约双子判据），二十五轮 deploy 契约结论继续有效；本轮 own 改动后的契约结论见上方验证记录。
- **复核点**：开工基准 = 5bcd87dae（origin/main 顶；并行会话已把主工作区 3 个未推提交经 84bae533d merge 收敛并推送——48fb5ba81 的"pending push"注记就此了结，主工作区脏文件清单回到 7 项原集）。本轮最终复核点 = 本轮提交。
- **遗留风险**：①A 项数据门依旧未开，runbook 判读仍等运维接入后首批数据；②154 走公网地址抓取（/metrics 有 bearer 鉴权门但暴露公网）——运维给内网路由后替换地址复测；③key 指纹（MD5 前 8 位）仅为比对入库；④fails_r12 待重测（bg 第七度移动）；⑤既往风险（TTL 抑制未实施/显示侧治本预案就绪未实施/批量 SQL/脏文件门控）不变。

## 本轮记录（2026-09-12 第二十七轮，上线观察轮第二十轮——245-a legacy 通道首批 necessity 生产数据到达并判读平稳；运维接入仍未执行，遗留项 6 保持开启）

- **输入到达声明（区别于第 7–26 轮的"零数据阻塞"）**：任务书未携带运维接入完成通知，本轮开工 ssh 只读复查（同第二十六轮探测通道）发现：**legacy 单目标通道（job `llm-gateway` → 127.0.0.1:8781，245-a 当前 active 槽位）已抓到 necessity 系列**——第二十六轮预检时 `api/v1/series` 查 necessity 为空，本轮实测 `mirror_delete_failed_total`/`mirror_delete_retry_total` 两条系列已在 Prometheus 出现（二十六轮预检后 Prometheus 对网关已暴露指标的常规抓取所致）。**这是本特性随 2083 上线（2026-09-12 凌晨）以来的首批 necessity 生产观测数据**，构成 A 项的**局部阶段性判读输入**；完整双机判读仍等运维接入（154 与蓝绿 b 槽位仍在抓取面外）。
- **数据判读（第六轮 runbook + instance 分节点口径；本轮仅 245-a 有数据）**：
  - Prometheus 侧（api/v1 只读）：`mirror_delete_failed_total{instance="127.0.0.1:8781",job="llm-gateway"}` 当前值 **0**、`increase[24h]` **0**；`retry_total` 同系列形态存在、值 0；`skip_total` 无任何系列（懒创建=零 skip）。
  - 网关直读（权威，不受抓取空窗影响；245 admin token 只读 curl /metrics）：failed=0、retry=0（HELP/TYPE+0 值行齐全），**skip_total 连 HELP/TYPE 行都不输出**——client_golang 对无子系列 CounterVec 的已知 Gather 过滤行为（`NormalizeMetricFamilies` 丢弃空 family；第二十六轮 "no sentinel" 的完整实测形态，本轮确认）。
  - **判读结论：平稳。** ①无 churn 循环迹象（failed=0 且 24h 增量 0——镜像删除从未失败，第三轮有界重试从未触发）；②retry=0（无瞬时删除失败被同轮吸收）；③零 skip 是观测事实非异常——升级判据是"skip 异常高→排查键 schema"与"failed 持续增长→短 TTL 抑制"，均未触发，**TTL 抑制继续锁门**。
  - **零 skip 的解读边界（如实记录）**：skip=0 意味着"闸门正确跳过"的行为面在生产尚无生效样本——现有数据只证实 fail-open/执行路径与镜像删除路径无异常。其成因无需排查：生产自动 node_probe 流量极小（2083 收尾：双节点每日约 27 次 attempt），且闸门两条件（同凭据兄弟节点全健康 / error 水位静止）在生产流量形态下难同时满足，零 skip 与两形态均相容，不构成告警信号（对齐第二十六轮预检结论 6 口径）。
- **运维接入状态复查（仍阻塞，实测）**：245 prometheus.yml 仍单 legacy `llm-gateway` job（`grep job_name` 仅 prometheus + 自抓 + 注释掉的 node-exporter/postgres）、rules 目录 10 文件无 necessity 告警、`api/v1/rules` Necessity 空、无双 job 落地。→ **遗留项 6 保持开启**：关闭条件 = 双机接入后按 runbook 完整判读平稳；本轮 245-a 局部平稳不足以关闭（154 无数据、蓝绿轮换致盲面、告警规则未加载三点并存）。
- **探测方法注记（给下一轮）**：ssh 通道连续 curl /metrics 偶发**空响应**——本轮实测第二次直读返回空导致 `grep -c` = 0，形成"skip 连 HELP 都没有"假象；带重试复测后真相为 HELP/TYPE 本就因空 family 过滤而不输出，但**空响应本身会伪造任何"指标缺失"结论**。下一轮对同端点判读前用循环重试（取到非空输出为止）或多次取样，勿把单次空响应当证据。
- **C 项**：`lastProbeRun` 保持 `started_at DESC`，无反例，维持关闭。
- **验证记录（如实区分）**：**通过**——数据判读全链路只读取证（api/v1 query/series/rules/targets + 网关直读 /metrics，全部 GET；未在生产机执行任何写操作）；本轮提交仅本 handoff 单文件，零代码改动，bg/deploy 零触碰。**未验证**——运维接入仍未执行（遗留项 6 前置链第一步）；fails_r12.txt 基线重测继续挂起（bg 第七度移动后本轮零 bg 触碰，无重测义务）。**环境受限**——promtool/Grafana 导入不可用、win/arm64 `-race` 不支持（口径同既往）。
- **复核点**：开工基准 = 9cef04e27（第二十六轮提交；fetch 确认本轮窗口 origin/main 无再前进，本地 main 落后一跳由本轮 ff 收敛）。本轮最终复核点 = 本轮提交。
- **遗留风险**：与第二十六轮相同，无新增；其中④（fails_r12 待重测）与②（154 公网地址抓取）继续有效。

## 下一轮提示词（可直接复制）

```text
请继续 llm-gateway-go 自检必要性闸门（necessity gate）收尾——上线观察轮（第二十一轮）。
工作目录：Z:\workspace\ai-native-tools\syncfield\llm-gateway-go-4

先阅读：docs/handoff/2026-09-11-selfcheck-necessity-gate-handoff.md（重点"本轮记录
第六轮"（runbook 判读规则与孤儿行三层清理口径）、"本轮记录 第二十六轮"（2026-09-12
ssh 只读预检三前置闭环：两机 admin key 不同值→必须拆双 job llm-gateway-prod-245/-154
各挂 token 文件、154 单实例（8782 无监听）→ 只配 gateway-154-a、skip_total 为懒创建
CounterVec 无 sentinel → 接入验收判据用 mirror_delete_failed_total 0 值系列勿用
skip_total；LLMGatewayDown 已通配 up{job=~"llm-gateway.*"}；接入材料
（NATIVE-245-DEPLOY.md 接入节 + prometheus.yml 双 job 注释模板 + grafana README）
已按实测修正为照做即用）、"本轮记录 第二十七轮"（2026-09-12 首批 necessity 生产数据
到达并判读平稳：legacy 单目标通道 job llm-gateway→127.0.0.1:8781 已抓到 failed/retry
两条系列且恒 0、increase[24h]=0、零 skip（懒创建=自 2083 上线以来 245-a 无一次 gate
skip，且 client_golang 对无子系列 CounterVec 连 HELP/TYPE 都不输出）→ 无 churn 迹象，
TTL 抑制继续锁门；运维接入仍未执行（prometheus.yml 仍单 legacy job、无 Necessity
规则加载、无双 job）→ 遗留项 6 保持开启；探测注记：ssh 通道连续 curl /metrics 偶发
空响应会伪造"指标缺失"结论——判读前循环重试或多次取样）、遗留任务第 6 项。
背景：本轮唯一实质等待仍是运维执行接入（/opt/monitoring 编辑 + 154 token 文件 +
reload + alerts.yml 同步 + api/v1 验收，步骤全在 NATIVE 文档接入节）。下轮开工先
ssh 只读复查接入状态（grep prometheus.yml job_name + api/v1/rules Necessity +
api/v1/series match mirror_delete_failed_total）+ 顺带复查 legacy 通道是否仍平稳：
接入已执行 → 3 条 0 值系列（gateway-245-a/b、gateway-154-a）= 验收过，按第六轮
runbook + instance 分节点判读：平稳 → 关闭遗留项 6；单 instance failed_total 持续
增长/告警 firing → 短 TTL 抑制（先失败测试，硬条件不变：不漏真实故障探测、证据错误
fail-open、manual/admin 绕过、lease 丢失不删新 owner 状态），实施前先重测 bg 判据。
基线警示：**bg/ 已第七度移动（fa106fef2..5bcd87dae）且 fails_r12.txt（17 例）源文本
口径两轮未重测（第二十六/二十七轮均零 bg 改动）。下轮如需跑 bg 套件（如 A 项触发
TTL 抑制实施），先在 C: 副本重测当前 HEAD 基线并留档 fails_r13，不得直接拿 fails_r12
当判据。** TestProbeService*LeaseHeartbeat* 两例时序敏感（重负载下偶发新增失败）——
失败集合失配先定向复跑再定性。
注意：主工作区检出 main（并行线活跃，工作区遗留脏文件 7 项——docs 下多处、
scripts/deploy-lib、*.lnk——严禁 add/clean/恢复）。继续用 git worktree 从
origin/main 拉独立分支实施（开工先验活：`git worktree list` + worktree 内
git status，元数据被清即 rm 重建；lgw-necessity-p2 现检出
docs/necessity-gate-round27，路径在 //Mac/Home 同源盘、Windows 侧可正常访问，
可直接 `checkout -B` 到本轮分支）；Windows 验证用 C: 副本 lgw-p2test
（C:\Users\xutaohuang\AppData\Local\lgw-p2test；仅 cp 变更文件；基线对照务必
CRLF 同口径——`git show <rev>:<file> | unix2dos`，或 diff 加 --strip-trailing-cr；
副本上 go build 交叉编译需 -buildvcs=false）。
运维通道备忘：ssh 245 / ssh 154（~/.ssh/config 在役免密，25022 端口）可做只读
探测（curl GET / grep / ss / systemctl list / key 指纹），严禁在生产机执行写操作
——接入写操作归运维。245 admin token 只读路径：
/opt/monitoring/prometheus/secrets/admin_token（curl 网关 /metrics 用）；
Prometheus API 127.0.0.1:9090（/api/v1/query|series|rules|targets 均只读）。

本轮任务（按输入分派，无输入则如实记录阻塞）：
A. 若已获得 245/154 双机的 necessity 数据或告警触发记录（第六轮 runbook + instance
   分节点判读；验收先确认 mirror_delete_failed_total 的 3 条 0 值系列存在=接入成功）：
   - mirror_delete_failed_total 单 instance 持续增长 / 告警 firing → 实施短 TTL
     抑制（硬条件不变），先补失败测试；实施前先按上方基线警示重测 bg 判据；
   - 观察窗口平稳 → 在 handoff 记录结论并关闭遗留项 6。
   （legacy 通道 245-a 单槽位数据继续到达且平稳 = 局部信号，记录但不关闭遗留项 6
   ——第二十七轮口径。）
B. 若运维确认"模型解绑/改名后自检 tab 长期显示陈旧节点"：先给应急口径（admin API
   提交一次探测即删行，或批量 SQL），再按预案实施显示侧治本修复（queryProbeNodeTasks
   WHERE 并入绑定链 EXISTS，SQL 抽取可守卫 + 先红后绿测试），实施前与反馈方确认
   面板口径（node-tasks 泳道 vs SSE 流）。
B'. 若运维反馈接入执行受阻：401=token 文件内容与该机 key 不符（对照 NATIVE 步骤 0
   重建 admin_token_154）；000=网络/地址拓扑变更（上报运维实测，勿在仓库侧猜）；
   promtool/reload 报错=按文档"以 /-/reload 实际结果为准"排查；其余问题如实记录
   并回写 NATIVE 文档。
C. lastProbeRun 维持关闭；除非出现乱序完成记录反例。
D. 验证如实区分通过/环境受限/未验证。

完成后输出：结论/根因、改动文件与关键行为、测试命令与结果、遗留风险、
更新本 handoff、下一轮提示词。
```

## 验证环境说明

本工作区（Z: 网络盘）无 Go/Docker/WSL。便携工具链保留在 `C:\Users\xutaohuang\AppData\Local\golang-dist`（Go 1.27.1 / llvm-mingw / zig）。Windows 本地验证用 C: 本地打桩副本 **`C:\Users\xutaohuang\AppData\Local\lgw-p2test`**（第十六轮审计补全路径；历轮基线留档 fails_r6–r12.txt 同目录，**当前判据为 fails_r12.txt 17 例**，fails_r6–r11 已作废；两例 `TestProbeService*LeaseHeartbeat*` 时序敏感，重负载下偶发新增失败——集合失配先定向复跑再定性）。`bg` 包含 Linux 专属 `syscall.Statfs_t`，Windows 本地验证需在副本中将 `storage_retention_worker.go` 的 `diskUsagePercent` 打桩后再 `go test -mod=vendor ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc）。部署目标验证用 `GOOS=linux GOARCH=arm64` + `zig cc -target aarch64-linux-musl` 交叉编译（C: 副本非 git 仓库，`go build` 须加 `-buildvcs=false`）。

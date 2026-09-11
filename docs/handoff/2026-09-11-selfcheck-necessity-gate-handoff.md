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

4. **P3：missing-binding 丢弃路径的同类 churn 面** — **已完成（2026-09-11，本轮，feat/necessity-p3-binding-pump-gate）**
   只读可达性确认：pump 是唯一不查绑定链的循环调度入口——`pumpDueStatesSQL` 只带 credential/provider 资格门，而 credential_recovery 全部路径（`reconcileStaleNodeProbeStateSQL`、`lookbackCandidateSQL`、expired/fresh-degraded 绑定恢复）都以 cmb/pm 绑定链为锚。绑定链断裂（无 cmb 行 / 悬空 provider_model_id / pm.raw_model_name 改名）的镜像行会被 pump 反复入队：每轮真实执行探测轮次 1（endpoint build 阶段失败）→ best-effort 删镜像 → 删除失败则行残留、下个 holdoff 周期再循环（与 P2 同形，但无重试也无计数）。修复采用**结构性断源**（pump 资格过滤，而非复用重试编排）：`pumpDueStatesSQL` 的 WHERE 并入绑定链 EXISTS（`credential_model_bindings cmb JOIN provider_models pm`，双列关联防漂移；刻意只查存在性、不加 available/manual 门——resolveDirectTarget 对仅存在的绑定即可探测，更严会把可探测对挡在 pump 外；绑定重建后行自然重回 pump，与资格门重入语义一致）。missing-binding 分支注释同步改写（一次性自愈语义，不再"经 pump 重入队"）；恢复路径与 manual/admin 零改动。新增守卫测试 `TestPumpDueStatesSQLFiltersMissingBindingPairs`（先红后绿），详见"本轮记录（第四轮）"。

### 未完成的遗留任务

5. **P3：`lastProbeRun` 排序语义** — **决策：保持 `started_at DESC` 不改（2026-09-11 确认）**
   同一 dedup_key 不允许并发 ready/running，实践中与 `completed_at DESC` 等价且索引友好；未发现任何真实反例，此项按原方案关闭，仅在出现乱序完成记录的反例时重开。

6. **运维观察项（上线后）**
   观察：`llmgw_node_probe_necessity_skip_total` 增速、自检 tab "skipped_not_necessary" 磁贴占比、以及 `llmgw_node_probe_necessity_mirror_delete_retry_total`（瞬时失败吸收量）与 `llmgw_node_probe_necessity_mirror_delete_failed_total`（**持续失败告警源**）。若 failed_total 持续增长（说明某 (cred,model) 的镜像 DELETE 长期不收敛、磁贴仍按 pump 周期翻动），按原方案升级：对刚被跳过的 (credential_id, raw_model) 引入短 TTL 抑制（pump 侧或闸门侧均可，抑制窗口必须短于真实故障的重探测需求）。若 skip 率异常高，优先排查 Redis 键 schema（legacy/k2/dual）与 tenant 归属是否一致。

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
  1. pump（`pumpDueStatesSQL`）：只带 credential/provider 资格门，**不查绑定链**；且对已资格过的行（status=active + lifecycle=active + provider enabled + 非手动停用），`resolveDirectTarget` 严格查询的 credential/provider 门全部满足，其"no rows"只剩绑定链断裂一种成因（宽松重试不改变结论，仅 c.status IS NULL 的边缘经宽松查询成功、不落入 missing-binding）。
  2. credential_recovery：全部绑定锚定——`reconcileStaleNodeProbeStateSQL` JOIN cmb+pm 且 cmb.available=TRUE，`lookbackCandidateSQL` FROM cmb 起步，expired/fresh-degraded 恢复直接以 cmb 行为对象。
  3. 手动/管理任务：绕过资格与闸门（操作员在场，一次性）。

  结论：绑定链断裂（无 cmb 行 / 悬空 cmb.provider_model_id / pm.raw_model_name 改名）的 `node_probe_state` 行，唯一循环调度源是 pump。每轮循环真实执行探测轮次 1（endpoint build 失败，无出站 HTTP）+ 1 条审计行 + 完整磁贴生命周期，删除失败则下个 holdoff 周期再来——与 P2 skip 路径同形，但无重试、无计数。
- **方案取舍**：二选一选了 **pump 资格过滤**而非复用 `deleteNodeProbeStateMirror` 编排——过滤把注定失败的任务挡在调度外（顺带消除每次 claim + 审计行 + 磁贴翻动的浪费，且这类浪费在删除成功时同样发生），重试编排只治症状；P2 的两个计数器语义锚定 skip 路径（Help 文本写明 "necessity skip path"），复用需改名重定语义，收益不成比例。**残余面**：missing-binding 分支删除失败时留下单条孤儿行，pump 不再重提，等绑定重建后自然恢复探测或运维清理——无 churn，可接受。
- **失败测试先行**：`TestPumpDueStatesSQLFiltersMissingBindingPairs`（`bg/node_probe_submit_gate_test.go`）改动前实测红（缺绑定链碎片），改动后绿。沿用 SQL 文本守卫样式——候选查询跑真库，pgxpool 无法 sqlmock，与既有 `TestPumpDueStatesSQLAppliesAutomaticEligibilityGate` 同型；断言双列关联碎片（`cmb.credential_id = nps.credential_id`、`pm.raw_model_name = nps.raw_model_name`）+ JOIN pm（悬空外键也被过滤）+ 过滤位于 WHERE（ORDER BY/LIMIT 之前，不占批内槽位）。
- **硬条件核对**：绑定缺失对**不漏探**——无绑定即无路由，探测无从执行；绑定重建后行自然重回 pump（next_retry_at 仍到期），与资格门"重新启用即重入"语义一致；paused 行不受影响（pump 本就不取）；manual/admin 绕过原样；闸门路径（`bg/probe_necessity.go`）零改动。
- **验证记录**：C: 本地打桩副本（`lgw-p2test`，复用既有副本、仅 cp 三个变更文件）执行 `go test -mod=vendor -count=1 ./bg/`：定向守卫 3 例全绿；全量套件 17 例失败经 **CRLF 同口径基线对照实验**确认与 origin/main 完全一致（16 例源文本断言 + 1 例 symlink 权限，均为预存环境限制，Linux CI 不受影响）。注意基线对照的方法坑：用 `git show` 直出的 LF 文件做基线会"少"4 例源文本失败（假差异），须按 autocrlf 检出口径把基线文件转成 CRLF 再跑才是同口径对照。`GOOS=linux GOARCH=arm64` zig cc 交叉编译 `./bg/... ./cmd/gateway/...` 通过。win/arm64 不支持 `-race`，未跑（与既往轮次相同）。

## 下一轮提示词（可直接复制）

```text
请继续 llm-gateway-go 自检必要性闸门（necessity gate）的收尾工作。
工作目录：Z:\workspace\ai-native-tools\syncfield\llm-gateway-go-4

先阅读：docs/handoff/2026-09-11-selfcheck-necessity-gate-handoff.md（重点"本轮记录"
与"未完成的遗留任务"）、bg/node_probe.go（pumpDueStatesSQL 的绑定链过滤）。
背景：闸门/证据批量化/skip 路径 churn 抑制/missing-binding pump 过滤均已合入 main
（62e8f6f41、11af45216、dc8463e28、ef3280194、5e65c21f1 及本轮 P3 提交）。
注意：主工作区检出的 fix/r13-logging-hygiene 属于并行 R13 日志工作，不要混入本任务；
工作区遗留脏文件（docs 两处、scripts/deploy-lib、*.lnk）严禁 add/clean/恢复。
建议用 git worktree 从 origin/main 拉独立分支实施；Windows 验证按 handoff
"验证环境说明"打桩 diskUsagePercent（C: 本地副本 lgw-p2test 可复用，仅 cp 变更文件；
基线对照务必把基线文件按 autocrlf 口径转 CRLF 再跑，否则源文本断言出现假差异）。

本轮任务（上线观察为主，按数据定升级）：
A. 部署后观察 llmgw_node_probe_necessity_mirror_delete_retry_total /
   mirror_delete_failed_total 与 skipped 磁贴占比：
   - failed_total 持续增长 → 对刚被跳过的 (credential_id, raw_model) 实施短 TTL
     抑制；硬条件不变：不得漏掉真实故障探测、证据错误 fail-open、manual/admin
     绕过、lease 丢失不删新 owner 状态。
   - 两者都平稳 → 仅记录结论，无需改动。
B. 若运维反馈"模型解绑/改名后自检 tab 长期显示陈旧节点"：排查 node_probe_state
   孤儿行的清理口径（pump 已不再重提绑定缺失对，孤儿行只能等绑定重建或运维清理），
   必要时补显式孤儿行回收路径（同样先补失败测试）。
C. lastProbeRun 保持 started_at DESC 已按原方案关闭，除非出现乱序完成记录反例。
D. 验证如实区分通过/环境受限/未验证；bg 全量在 Windows 本地的 17 例预存失败
   （16 例 CRLF 源文本断言 + 1 例 symlink 权限）见 handoff 本轮记录验证段。

完成后输出：结论/根因、改动文件与关键行为、测试命令与结果、遗留风险、
更新本 handoff、下一轮提示词。
```

## 验证环境说明

本工作区（Z: 网络盘）无 Go/Docker/WSL。便携工具链保留在 `C:\Users\xutaohuang\AppData\Local\golang-dist`（Go 1.27.1 / llvm-mingw / zig）。`bg` 包含 Linux 专属 `syscall.Statfs_t`，Windows 本地验证需在副本中将 `storage_retention_worker.go` 的 `diskUsagePercent` 打桩后再 `go test -mod=vendor ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc）。部署目标验证用 `GOOS=linux GOARCH=arm64` + `zig cc -target aarch64-linux-musl` 交叉编译。

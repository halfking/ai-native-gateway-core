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

3. **P2：镜像行删除失败的 churn 抑制** — **已完成（2026-09-11，本轮，feat/necessity-p2-churn-suppression）**
   `deleteNodeProbeState` 失败 → pump 30s 重入队 → 闸门再跳过 → 再删 的循环已用失败测试稳定复现（一次瞬时删除失败 = 2 轮完整 skip 生命周期 = 磁贴翻动 2 次）。修复：删除失败后**同轮有界即时重试一次**（200ms 延迟，吸收锁等待/序列化失败/瞬时连接抖动）；仍失败则落 `llmgw_node_probe_necessity_mirror_delete_failed_total` 计数 + ERROR 日志（pump 重入队即天然的重试安全网，绝不执行探测）；ctx 中途取消不再发起重试但也计入持续失败。硬条件全部保持：重试在租约所有权证明（队列行删除成功）之后才发生；闸门决策路径零改动（不漏探、证据错误仍 fail-open、manual/admin 仍绕过）。测试 16→22 例（churn 复现、持续失败有界+计数、ctx 取消三例新增），详见下方"本轮记录"。

### 未完成的遗留任务

3. **P3：`lastProbeRun` 排序语义** — **决策：保持 `started_at DESC` 不改（2026-09-11 本轮确认）**
   同一 dedup_key 不允许并发 ready/running，实践中与 `completed_at DESC` 等价且索引友好；本轮未发现任何真实反例，此项按原方案关闭，仅在出现乱序完成记录的反例时重开。

4. **运维观察项（上线后）**
   观察：`llmgw_node_probe_necessity_skip_total` 增速、自检 tab "skipped_not_necessary" 磁贴占比、以及本轮新增的 `llmgw_node_probe_necessity_mirror_delete_retry_total`（瞬时失败吸收量）与 `llmgw_node_probe_necessity_mirror_delete_failed_total`（**持续失败告警源**）。若 failed_total 持续增长（说明某 (cred,model) 的镜像 DELETE 长期不收敛、磁贴仍按 pump 周期翻动），按原方案升级：对刚被跳过的 (credential_id, raw_model) 引入短 TTL 抑制（pump 侧或闸门侧均可，抑制窗口必须短于真实故障的重探测需求）。若 skip 率异常高，优先排查 Redis 键 schema（legacy/k2/dual）与 tenant 归属是否一致。

5. **P3（新发现，未处置）：missing-binding 丢弃路径的同类 churn 面**
   `ProbeService.Run` 的 missing-binding 分支（`probe_service.go`，`isMissingBindingErr(direct)`）同样调用 `deleteNodeProbeState` 丢弃镜像行，但保持 best-effort 单次尝试（无重试/计数）。注意 pump 的资格 SQL（`automaticProbeEligibilityExistsSQL`）只查 credential/provider 状态、**不查 `credential_model_bindings`**，绑定缺失的节点仍会被 pump 重入队——若该 DELETE 持续失败，存在与 P2 同形的循环（该路径还会真的重复执行探测轮次 1）。本轮按范围界定未处置（注释已标明）；如运维数据支撑，可复用本轮的 `deleteNodeProbeStateMirror` 编排或把 binding 存在性并入 pump 资格过滤。

6. **工作区遗留脏文件（非本特性，未处置，需原作者确认）**
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

## 下一轮提示词（可直接复制）

```text
请继续 llm-gateway-go 自检必要性闸门（necessity gate）的收尾工作。
工作目录：Z:\workspace\ai-native-tools\syncfield\llm-gateway-go-4

先阅读：docs/handoff/2026-09-11-selfcheck-necessity-gate-handoff.md（重点"本轮记录"
与"未完成的遗留任务"）、bg/probe_necessity.go（deleteNodeProbeStateMirror 重试编排）。
背景：闸门/证据批量化/churn 抑制均已合入 main（62e8f6f41、11af45216、dc8463e28、
ef3280194 及 P2 churn 抑制提交，分支 feat/necessity-p2-churn-suppression）。
注意：主工作区检出的 fix/r13-logging-hygiene 属于并行 R13 日志工作，不要混入本任务；
工作区遗留脏文件（docs 两处、scripts/deploy-lib、*.lnk）严禁 add/clean/恢复。
建议用 git worktree 从 origin/main 拉独立分支实施；Windows 验证按 handoff
"验证环境说明"打桩 diskUsagePercent（C: 本地副本 + 便携工具链）。

本轮任务（上线观察 + 可选升级，二选一按数据定）：
A. 部署后观察 llmgw_node_probe_necessity_mirror_delete_retry_total /
   mirror_delete_failed_total 与 skipped 磁贴占比：
   - failed_total 持续增长 → 按遗留项 4 的判据实施 (credential_id, raw_model)
     短 TTL 抑制；硬条件不变：不得漏掉真实故障探测、证据错误 fail-open、
     manual/admin 绕过、lease 丢失不删新 owner 状态。
   - 两者都平稳 → 仅记录结论，无需改动。
B. 处理遗留项 5：missing-binding 丢弃路径的同类 churn 面（pump 资格 SQL 不查
   credential_model_bindings）。先只读确认真实可达性（绑定缺失节点的镜像行
   是否确实会被 pump 反复重入队），再决定复用 deleteNodeProbeStateMirror 编排
   还是把 binding 存在性并入 pump 资格过滤；同样先补失败测试。
4. lastProbeRun 保持 started_at DESC 已按原方案关闭，除非出现乱序完成记录反例。
5. 验证如实区分通过/环境受限/未验证；bg 全量在 Windows 本地的 17 例预存失败
   （16 例 CRLF 源文本断言 + 1 例 symlink 权限）见 handoff"本轮记录"验证段。

完成后输出：结论/根因、改动文件与关键行为、测试命令与结果、遗留风险、
更新本 handoff、下一轮提示词。
```

## 验证环境说明

本工作区（Z: 网络盘）无 Go/Docker/WSL。便携工具链保留在 `C:\Users\xutaohuang\AppData\Local\golang-dist`（Go 1.27.1 / llvm-mingw / zig）。`bg` 包含 Linux 专属 `syscall.Statfs_t`，Windows 本地验证需在副本中将 `storage_retention_worker.go` 的 `diskUsagePercent` 打桩后再 `go test -mod=vendor ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc）。部署目标验证用 `GOOS=linux GOARCH=arm64` + `zig cc -target aarch64-linux-musl` 交叉编译。

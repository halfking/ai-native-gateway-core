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

测试：`bg/probe_necessity_test.go` 16 例（两条件正/反例、当前节点无状态/不可解码 fail-open、证据错误 fail-open、手动绕过、闸门未接线禁用、worker 不 Complete（completeFn 缝断言）、Remove SQL 守卫）。全部通过；`bg` 全量套件在 Windows 本地验证仅 `TestWalkDirSafe_ToleratesIsolatedErrors` 因符号链接权限失败（环境限制，与功能无关）。

## 遗留任务（按优先级）

### 已完成的跟进项

1. **P2：`ProbeHealthEvidence` 批量化**（`domains/ursm/v2/store/probe_evidence.go`）— **已完成（2026-09-11，dc8463e28；同日审计修正）**
   已改为 Redis Pipeline（复用 `redissafe.SafeHGetAllPipeline`，键选择规则与 `PipelineNodeViews` 一致）：全部主键一次批量调用，dual 模式 miss 的键再有一次 legacy 回退批量调用。成本口径（审计修正）：每次 `SafeHGetAllPipeline` 内部是 TYPE、HGETALL 两段 Exec，因此 legacy/canonical 模式最多 2 次、dual 全量回退最多 4 次 pipeline Exec——此前"≤2 轮"表述不准确；相对逐模型串行 1+2×500 次 RTT 仍是数量级改善。测试覆盖批量混合来源（k2-only/legacy-only/双写/缺失）、legacy-only tuple、501 规模（1 current + 500 sibling 真实极值）。

2. **P2：`removeSkippedProbe` 分支测试缺口** — **已完成（2026-09-11，5367d6f84）**
   `ProbeQueue` 的移除面收窄为 `probeSkipQueue` 接口缝（`Remove` + `publishRemovedTransition`，因 `ProbeQueue` 的 pgxpool 无法用 sqlmock 构造），镜像删除加 `deleteStateFn` 函数缝。新增 4 例：Remove 报错只停在队列行、removed=false（租约丢失）不碰镜像/SSE、queue/lease 缺失三副作用全不发生、成功路径编排顺序 remove→deleteState→publish 及 SSE reason 前缀断言。顺带修复 typed-nil 接口陷阱（`skipRemovalQueue` 须把 nil `*ProbeQueue` 显式转成 nil 接口，否则落入 Remove 的 unavailable-database 分支）。

### 未完成的遗留任务

3. **P2：镜像行删除失败的 churn 抑制**
   `deleteNodeProbeState` 失败时 pump 会 30s 级"重入队→再跳过→再删"循环（方向收敛、不漏探，但磁贴反复翻动）。可在删除失败时即时重试一次，或 pump 侧对刚被跳过的 (cred,model) 做短 TTL 抑制。**下一轮首选任务，见文末提示词。**

4. **P3：`lastProbeRun` 排序语义**
   现按 `started_at DESC` 取最新完成记录（索引友好）。同一 dedup_key 不允许并发 ready/running，实践中与 `completed_at DESC` 等价；如需严格语义可改为 `ORDER BY completed_at DESC`（接受非索引排序）。

5. **运维观察项**
   上线后观察：`llmgw_node_probe_necessity_skip_total` 增速、自检 tab "skipped_not_necessary" 磁贴占比、恢复扫描（credential_recovery 60s 重提）与闸门 fail-open 的交互频率。若 skip 率异常高，优先排查 Redis 键 schema（legacy/k2/dual）与 tenant 归属是否一致。

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

## 下一轮提示词（可直接复制）

```text
请继续 llm-gateway-go 自检必要性闸门（necessity gate）的收尾工作。
工作目录：Z:\workspace\ai-native-tools\syncfield\llm-gateway-go-4

先阅读：
1. docs/handoff/2026-09-11-selfcheck-necessity-gate-handoff.md
2. bg/probe_necessity.go、bg/probe_necessity_test.go
3. domains/ursm/v2/store/probe_evidence.go

背景：闸门与证据批量化均已合入 main（62e8f6f41、11af45216、dc8463e28 及审计修正提交）。
注意：主工作区检出的 fix/r13-logging-hygiene 属于并行 R13 日志工作，不要混入本任务；
工作区遗留脏文件（docs 两处、scripts/deploy-lib、*.lnk）严禁 add/clean/恢复。
建议用 git worktree 从 origin/main 拉独立分支实施。

本轮任务（P2：镜像行删除失败的 churn 抑制）：
1. 只读梳理 deleteNodeProbeState 失败 → pump 重入队 → 闸门再跳过 → 再删 的循环路径，
   先补一个能稳定复现"删除失败导致磁贴反复翻动"的失败测试。
2. 最小修复：优先删除失败后有界即时重试一次；仍不收敛再引入 (credential_id, raw_model)
   短 TTL 抑制。硬条件：不得因抑制漏掉真实故障探测；证据错误仍 fail-open；
   manual/admin 任务仍绕过闸门；lease 丢失时不得删除新 owner 的镜像/SSE。
3. 为持续删除失败补明确日志或 Prometheus 计数。
4. lastProbeRun 保持 started_at DESC（dedup 不并发，与 completed_at 等价且索引友好），
   除非有真实反例，否则不改。
5. 验证：定向测试 + bg 相关套件（Windows 本地需按本文档"验证环境说明"打桩
   diskUsagePercent），如实区分通过/环境受限/未验证，不得把未跑写成已跑。

完成后输出：根因、改动文件与关键行为、测试命令与结果、遗留风险、更新本 handoff、下一轮提示词。
```

## 验证环境说明

本工作区（Z: 网络盘）无 Go/Docker/WSL。便携工具链保留在 `C:\Users\xutaohuang\AppData\Local\golang-dist`（Go 1.27.1 / llvm-mingw / zig）。`bg` 包含 Linux 专属 `syscall.Statfs_t`，Windows 本地验证需在副本中将 `storage_retention_worker.go` 的 `diskUsagePercent` 打桩后再 `go test -mod=vendor ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc）。部署目标验证用 `GOOS=linux GOARCH=arm64` + `zig cc -target aarch64-linux-musl` 交叉编译。

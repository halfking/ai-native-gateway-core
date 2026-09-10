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

1. **P2：`ProbeHealthEvidence` 批量化**（`domains/ursm/v2/store/probe_evidence.go`）— **已完成（2026-09-11，本轮）**
   已改为 Redis Pipeline（复用 `redissafe.SafeHGetAllPipeline` 双段 TYPE+HGETALL 管道，键选择规则与 `PipelineNodeViews` 一致）：全部主键一轮取回，dual 模式 miss 的键再有至多一轮 legacy 回退管道——一次闸门的 Redis 交互从最多 1+2×500 次串行 RTT 降为 ≤2 轮管道。新增批量混合来源（k2-only/legacy-only/双写/缺失）、legacy-only tuple、500 规模单测。

2. **P2：`removeSkippedProbe` 分支测试缺口**
   Remove 报错提前返回、removed=false 不碰镜像/SSE、queue nil 警告三条分支无单测（需要 sqlmock 或将 `ProbeQueue.Remove` 抽接口缝）。仓库已 vendor `DATA-DOG/go-sqlmock`。

3. **P2：镜像行删除失败的 churn 抑制**
   `deleteNodeProbeState` 失败时 pump 会 30s 级"重入队→再跳过→再删"循环（方向收敛、不漏探，但磁贴反复翻动）。可在删除失败时即时重试一次，或 pump 侧对刚被跳过的 (cred,model) 做短 TTL 抑制。

4. **P3：`lastProbeRun` 排序语义**
   现按 `started_at DESC` 取最新完成记录（索引友好）。同一 dedup_key 不允许并发 ready/running，实践中与 `completed_at DESC` 等价；如需严格语义可改为 `ORDER BY completed_at DESC`（接受非索引排序）。

5. **运维观察项**
   上线后观察：`llmgw_node_probe_necessity_skip_total` 增速、自检 tab "skipped_not_necessary" 磁贴占比、恢复扫描（credential_recovery 60s 重提）与闸门 fail-open 的交互频率。若 skip 率异常高，优先排查 Redis 键 schema（legacy/k2/dual）与 tenant 归属是否一致。

6. **工作区遗留脏文件（非本特性，未处置，需原作者确认）**
   - `D docs/02-resources/research/pricing/scripts/vendor_pricing_table.py`
   - `D scripts/deploy-lib`
   - `M docs/archive/process/incidents-collection/2026-08-31-154-blue-green-deploy-recovery.md`

## 验证环境说明

本工作区（Z: 网络盘）无 Go/Docker/WSL。便携工具链保留在 `C:\Users\xutaohuang\AppData\Local\golang-dist`（Go 1.27.1 / llvm-mingw / zig）。`bg` 包含 Linux 专属 `syscall.Statfs_t`，Windows 本地验证需在副本中将 `storage_retention_worker.go` 的 `diskUsagePercent` 打桩后再 `go test -mod=vendor ./bg/`（CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc）。部署目标验证用 `GOOS=linux GOARCH=arm64` + `zig cc -target aarch64-linux-musl` 交叉编译。

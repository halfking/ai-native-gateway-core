# 会话存储审计与修正实现 —— 总控 Handoff

> 日期: 2026-09-25
> 来源: `tg_g7ufi0vy08muf8kaxr` 会话存储全面审计 (Status: complete, 2026-09-25)
> 目的: 把审计结论落成可独立执行的子任务集合, 由一个总控提示词统一调度, 每个子任务对应一个独立 PR 与独立 worktree.

---

## 1. 总控提示词 (Master Prompt)

把整段文字作为单一 `task` 调用的 prompt 发送给子代理, 由该子代理负责:

1. 解析本文件中**所有 7 个子任务模板** (§3 ~ §9);
2. 按依赖顺序串行执行, 每个子任务完成后:
   - `git status` 必须干净 (无遗留 untracked / uncommitted 改动);
   - 在该任务的 worktree 上 `git push origin <branch>`;
   - 在 `docs/audit/2026-09-25-session-storage-audit-handoff.md` 的 §10 状态表中标记完成;
3. 任一子任务失败必须立刻停下, 不得跨任务推进;
4. 所有子任务完成后, 把所有分支一次性合并到 `main`, 然后删除已合并的本地与远端分支.

```text
[Master Prompt — 会话存储审计修正实现调度]

你是会话存储修正实现的总控代理 (root session context).

# 上下文
本会话刚完成会话存储全面审计, 产出 18 份证据文件 (列在 docs/audit/2026-09-25-session-storage-audit-handoff.md §2),
已落成 12 项按 P0/P1/P2/P3 排序的修正项, 拆为 7 个可独立 PR 的子任务 (§3 ~ §9).

# 你的工作
1. 阅读本文档 §2 ~ §9 全部内容.
2. 按 §11 依赖图串行执行每个子任务 (不允许并行, 因为多个子任务共享 `bg/partition_manager.go` / `admin/session_*` / SQL 迁移顺序).
3. 每个子任务必须使用该任务模板内提供的"子代理提示词"单独调用一个 worker 子代理完成. 调用时把提示词**整段**作为 prompt 参数 (包括全部代码约束与验收清单).
4. 子代理完成后:
   a. 运行该任务的"通用门禁"清单 (§10.1);
   b. 若 git status 有未提交改动, 立刻 commit 并 push 当前 worktree 分支;
   c. 在 §10 状态表对应行写入 `[DONE] YYYY-MM-DD HH:MM commit=<sha> pr=<url>`;
   d. 通知用户子任务已落地, 不要自动合并到 main (等所有 7 个都完成后, 由总控统一 merge).

# 风险约束 (适用于所有子任务)
- 不允许修改以下冻结契约:
  * `sessions_v2.enabled` / `sessions_v2.shadow_write` 默认值 (必须保持 false, 由 settings 控制);
  * 镜像 outbox 不能回退为 in-process backlog (712 的核心目标);
  * 5 类 ID (request_id / attempt_id / gw_session_id / session_id / SessionPK) 互不混用;
  * RLS: 所有多租户查询必须经过现有 RLS 策略; bypass_rls 仅限 super_admin 或显式 reaper;
  * 所有 DELETE / 归档必须小批量 (≤1000 行 / batch) + 主键游标, 避开 statement_timeout=30s;
  * 所有迁移 down 测试按号逆序分步断言 (734.down → 733.down → 710.down);
  * 不允许绕开主 `request_logs` INSERT 路径 (main `request_logs` 写入必须保持先成 V1, 后才影子写 V2).

# 不允许做的事
- 不要把多个子任务合并到一个 PR (会破坏依赖图与审计责任);
- 不要让子任务越界修改其他子任务范围内的文件 (即使同名文件);
- 不要在 main 分支直接改 (必须建 feat/* 分支);
- 不要把 secrets / 凭据 / 测试 DSN 写入仓库.

# 完成定义
- §10 状态表 7 个子任务全部 `[DONE]`;
- `git log --oneline main..feat/<branch>` 显示 7 个独立分支 (按合并顺序);
- `git status` 干净, 工作目录无未提交改动;
- 在主分支执行 `go build ./... && go vet ./...` 全绿.

# 失败时的处理
- 子任务失败: 停下, 把失败原因与最小可复现命令写到 §10 状态表的备注列, 通知用户; 不要自动重试超过 1 次 (避免 token 浪费).
- 总控层失败: 用户重新调用本总控提示词, 你会从 §10 状态表中读取已完成的子任务, 跳过它们, 继续未完成的子任务.
```

---

## 2. 已读取的证据文件 (18 份, 本会话已直接读取)

### 2.1 设计文档
- `docs/会话优化v4/客户端会话保持.md` —— 会话优化 v4 主规格 (v1.15 勘误版)
- `docs/storage/2026-09-20-session-storage-decoupling-plan.md` —— 会话存储解耦 v3 方案

### 2.2 Schema 与迁移
- `sql/migrations/startup/430_sessions_v2_schema.sql` —— sessions / session_turns / session_bodies / session_turn_logs (24h TTL)
- `sql/migrations/startup/712_session_mirror_outbox.sql` —— 镜像失败 outbox (request_id 唯一)
- `sql/migrations/startup/733_session_turn_details.sql` —— V3 特征层 (30+9+15 列 + hot/parent + RLS)
- `sql/migrations/startup/734_request_logs_view_details_join.sql` —— canonical 视图 LEFT JOIN details

### 2.3 启动接线 & outbox
- `cmd/gateway/session_v2_init.go` —— SessionWriterV2 启动 + details probe + 两个 reaper
- `internal/sessionv2mirror/replay.go` —— 镜像 outbox reaper (FOR UPDATE SKIP LOCKED, 30s 周期)

### 2.4 本地文件存储
- `bg/bodies_trimmer.go` —— 会话文件保留 worker (6h 周期)
- `bg/partition_manager.go` —— PartitionManager + dropOldRequestLogsBodiesPartitions
- `storage/file/bodies_store.go` —— FileBodiesStore (gz/zstd, validPathID 防护)
- `storage/file/async_writer.go` —— AsyncFileWriter (临时文件+fsync+rename)
- `domains/session/v2/cache_v2_file.go` —— FileCache L1.5 (TTL+容量淘汰+原子写)

### 2.5 API & 前端
- `admin/session_list.go` —— V1 SessionListAPI
- `admin/session_detail_v2.go` —— V2 SessionDetailV2API
- `admin/session_turns_unified.go` —— 统一 turns 端点 (cursor + dual 路由)
- `web/src/views/admin/SessionDetailPage.vue` —— SessionDetailPage 详情页

### 2.6 Spec & 配置
- `settings/spec_sessions_v2.go` —— V2 灰度配置
- `settings/spec_lifecycle.go` —— 生命周期 TTL 配置
- `sql/scripts/backfill_sessions_v2.sql` —— 历史回填脚本

---

## 3. 子任务 1 —— 会话身份契约 API 层显式标注 (P0)

**分支**: `feat/session-identity-contract-api`
**类型**: P0 安全 / 语义
**预计 diff**: 5 个文件, 200-400 行

### 3.1 范围
- `admin/session_list.go`
- `admin/session_detail_v2.go`
- `admin/session_list_v2.go`
- `admin/session_turns_unified.go`
- `tests/session_identity_contract_test.go` (新增)

### 3.2 改动要求

1. 响应结构体统一新增 `id_kind` 与 `primary_key` 字段:
   ```go
   // SessionSummary 增加
   IDKind     string `json:"id_kind"`     // "gw_session_id" (V1 列表) 或 "session_id" (V2)
   PrimaryKey string `json:"primary_key"` // 当前会话的稳定标识 (V2 为 session_id, V1 为 gw_session_id)
   ```
2. `SessionDetailV2API.querySession` 强制以 `sessions.session_id` 作为主键; 客户端若传 `gw_session_id`, 先解析为 `session_id` 再查 (新增 helper `resolveSessionID(ctx, db, tenantID, gwSessionID) (string, error)`).
3. `unifiedTurnsTreeFallback` 与 `serveSessionTurnsUnified` 同样标注 `id_kind="session_id"` (来自 session_turns_with_current_month).
4. `tests/session_identity_contract_test.go` 校验 5 类 ID 互不混用 + API 响应 `id_kind` 与 `primary_key` 正确性 + 跨租户访问被拒.

### 3.3 子代理提示词

```text
[Subtask 1 — 会话身份契约 API 层显式标注]

你是会话存储修正实现的 worker 子代理, 当前负责 P0 任务: 会话身份契约 API 层显式标注.

# 上下文
工作区: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
HEAD: 153ad093a (R63 48h 审计轮已合并到 main)
当前分支: feat/session-identity-contract-api (你需要在 worktree 中创建)
依赖: 无 (本任务可立即开始)

# 设计基线 (必须严格遵守)
- 文档锚点: docs/会话优化v4/CONTRACT_FREEZE_2026-08-22.md §1 身份契约 (5 类 ID 互不重叠)
- 已读证据:
  * docs/audit/2026-09-25-session-storage-audit-handoff.md §2.5 (API 现状)
  * admin/session_list.go (V1 用 gw_session_id 聚合)
  * admin/session_detail_v2.go (V2 用 session_id)
- 冻结契约: 5 类 ID (request_id / attempt_id / gw_session_id / session_id / SessionPK) 互不替代

# 具体改动
1. admin/session_list.go: SessionSummary 增加 IDKind 与 PrimaryKey 字段
2. admin/session_list.go: loadSessions 标注 id_kind="gw_session_id"
3. admin/session_list.go: HandleDetail 响应同样标注
4. admin/session_detail_v2.go: querySession 强制以 session_id 为主键
5. admin/session_detail_v2.go: 新增 resolveSessionID helper, 接受 gw_session_id 时解析
6. admin/session_list_v2.go: SessionListV2API 响应标注 id_kind="session_id"
7. admin/session_turns_unified.go: TurnListItem 与 SessionChildRequest 增加 id_kind="session_id"
8. tests/session_identity_contract_test.go: 校验 5 类 ID 互不混用 + 跨租户访问被拒

# 验收 (必须全部达成)
- [ ] go build ./... 通过
- [ ] go vet ./... 通过
- [ ] gofmt 干净
- [ ] tests/session_identity_contract_test.go PASS
- [ ] admin/session_* 现有测试 PASS
- [ ] git diff 行数 ≤ 500
- [ ] git status 干净 (本任务内)

# 提交与推送
1. git add -A && git commit -m "feat(admin): session identity contract API labeling — gw_session_id/session_id explicit
   - 5 类 ID 互不混用契约在 API 层显式标注
   - resolveSessionID helper 接受 gw_session_id 解析为 session_id
   - 增加 session_identity_contract_test 跨租户访问校验
   Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §3"
2. git push origin feat/session-identity-contract-api
3. 把 PR URL 写到 docs/audit/2026-09-25-session-storage-audit-handoff.md §10 状态表的 Subtask 1 行

# 风险约束
- 不要改 ID 含义 (session_id 永远是 V2 服务端 ID)
- 不要绕过 RLS
- 不要把 gw_session_id 暴露为 ServerPK

# 完成后
- 输出: commit SHA, PR URL, 测试结果 (PASS/FAIL), 关键 diff 摘要
- 不要自动 merge 到 main (等所有 7 个子任务完成)
```

---

## 4. 子任务 2 —— `session_turn_logs` 可配 TTL 与 summary 同步 (P1)

**分支**: `feat/session-turn-logs-ttl`
**类型**: P1 数据保留
**预计 diff**: 1 个新迁移 + 4 个文件, 150-300 行

### 4.1 范围
- `sql/migrations/startup/745_session_turn_logs_ttl.sql` (新增)
- `settings/spec_lifecycle.go`
- `bg/partition_manager.go`
- `domains/session/v2/session_writer_v2.go`

### 4.2 改动要求

1. 745 迁移新增 `cleanup_session_turn_logs_by_ttl(p_ttl_hours int)` 函数: 删除 `expires_at < NOW() - INTERVAL`; 返回删除行数; idempotent.
2. `settings/spec_lifecycle.go` 新增 `lifecycle.session_turn_logs_ttl_hours` (TypeInt, ScopePlatform, Default 24, Min 1, Max 168, HotReload true).
3. `bg/partition_manager.go` 在 `runCleanup` 内周期调用 `cleanup_session_turn_logs_by_ttl(settings.GetPlatformInt("lifecycle.session_turn_logs_ttl_hours", 24))`.
4. `domains/session/v2/session_writer_v2.go` 在 turn 关闭时 (terminal stage) 同步追加 `sessions.turn_logs_summary` JSONB; `AppendTurnInTx` 增加 session_turn_logs 同步接口.

### 4.3 子代理提示词

```text
[Subtask 2 — session_turn_logs 可配 TTL 与 summary 同步]

你是 worker 子代理, 负责 P1 任务: session_turn_logs 可配 TTL 与 summary 同步.

# 上下文
工作区: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
HEAD: 153ad093a (假设已合入 Subtask 1)
当前分支: feat/session-turn-logs-ttl
依赖: 无 (Subtask 1 已合并, 你可以开始)

# 设计基线
- 文档锚点: docs/storage/2026-09-20-session-storage-decoupling-plan.md §5 (S5 TTL 燃尽 bodies 7 天 / logs 30 天)
- 当前实现: 24h 硬编码在 sql/migrations/startup/430_sessions_v2_schema.sql 的 expires_at DEFAULT
- 修改目标: 把 24h 改为可配; 详情页查询超期不再静默返回空

# 具体改动
1. 新增 sql/migrations/startup/745_session_turn_logs_ttl.sql:
   - CREATE OR REPLACE FUNCTION cleanup_session_turn_logs_by_ttl(p_ttl_hours int) RETURNS bigint
   - 删除条件: WHERE expires_at < NOW() - make_interval(hours => p_ttl_hours)
   - 注意: 表 session_turn_logs 当前没有 expires_at 索引; 同时 CREATE INDEX IF NOT EXISTS idx_session_turn_logs_expires_at ON public.session_turn_logs (expires_at)
2. settings/spec_lifecycle.go: 新增 lifecycle.session_turn_logs_ttl_hours (默认 24, 范围 1-168)
3. bg/partition_manager.go: runCleanup 内周期调用 (与 dropOldRequestLogsBodiesPartitions 同一节奏)
4. domains/session/v2/session_writer_v2.go: AppendTurnInTx 增加可选参数 session_turn_logs []TurnLogEntry; 写入 session_turn_logs 后聚合到 sessions.turn_logs_summary

# 验收
- [ ] go build ./... 通过
- [ ] go test ./domains/session/v2/... PASS
- [ ] go test ./bg/... PASS
- [ ] sql/migrations/startup/migration_745_test.go 新增 PASS
- [ ] gofmt 干净
- [ ] git diff 行数 ≤ 500

# 提交
git commit -m "feat(session): session_turn_logs configurable TTL + summary sync on close
   - 745 迁移: cleanup_session_turn_logs_by_ttl(p_ttl_hours) + expires_at 索引
   - settings/spec_lifecycle: lifecycle.session_turn_logs_ttl_hours (默认 24)
   - bg/partition_manager: 周期调用新清理函数
   - session_writer_v2: 关闭 turn 时同步 session_turn_logs → sessions.turn_logs_summary
Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §4"

# 风险约束
- 不要破坏现有 24h 行为 (默认即 24h)
- 不要让 session_turn_logs_summary 超过 JSONB 1MB (按 turn 切片)
- 不要把 retention 改成 < 1h
```

---

## 5. 子任务 3 —— 详情 V2 移除 request_logs_bodies JOIN + body_status (P1)

**分支**: `feat/session-detail-body-status`
**类型**: P1 数据访问 + UI
**预计 diff**: 5 个文件, 200-400 行

### 5.1 范围
- `admin/session_detail_v2.go`
- `admin/session_turns_unified.go`
- `web/src/views/admin/SessionDetailPage.vue`
- `web/src/locales/zh-CN/requestDetail.ts`
- `web/src/locales/en-US/requestDetail.ts`

### 5.2 改动要求

1. `admin/session_detail_v2.go` 移除任何对 `request_logs_bodies` 的引用 (V2 详情完全走 `session_bodies_unified`).
2. 响应 `SessionTurnV2` 增加 `BodyStatus string` 字段, 取值 `available|dropped|unavailable`.
3. `queryTurns` 根据 `request_delta/response_delta/outbound_body` 是否都为空 + `partition_date` 早于 retention 判定.
4. 前端 SessionDetailPage.vue 显示 `body_status === "dropped"` 时, 把 "正文不存在" 改为 "正文已按保留期清理" + 链接到 retention 文档.

### 5.3 子代理提示词

```text
[Subtask 3 — 详情 V2 body_status + 移除 request_logs_bodies JOIN]

你是 worker 子代理, 负责 P1: 详情 V2 body_status 字段.

# 上下文
工作区: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
当前分支: feat/session-detail-body-status
依赖: Subtask 1 必须已合入 (V2 API 改动基础)

# 设计基线
- 当前实现: admin/session_detail_v2.go LEFT JOIN session_bodies_unified; 但 V1 列表仍直接聚合 request_logs
- 修改目标: V2 详情不再 JOIN request_logs_bodies, 显式标注 body 状态

# 具体改动
1. admin/session_detail_v2.go: 移除所有 request_logs_bodies 引用
2. SessionTurnV2 增加 BodyStatus 字段 (JSON tag: body_status)
3. queryTurns 判定逻辑:
   - request_delta/response_delta/outbound_body 三者任一非空 AND partition_date >= NOW() - body_retention_hours → "available"
   - 三者都空 AND partition_date < NOW() - body_retention_hours → "dropped"
   - 三者都空 AND 不在分区范围内 → "unavailable"
4. admin/session_turns_unified.go: TurnListItem 同样增加 BodyStatus
5. web/src/views/admin/SessionDetailPage.vue:
   - snapshot 中读取 body_status 列表
   - 当任一 turn body_status === "dropped" 时, 显示 "正文已按保留期清理" 横幅
   - 当 "unavailable" 时, 显示 "正文不在保留窗口内"
6. web/src/locales/{zh-CN,en-US}/requestDetail.ts: 增加 body_status_dropped 与 body_status_unavailable 文案

# 验收
- [ ] go build ./... 通过
- [ ] go test ./admin/... PASS
- [ ] pnpm type-check (或同等前端校验) 通过
- [ ] gofmt 干净
- [ ] git diff 行数 ≤ 500

# 提交
git commit -m "feat(admin): session detail body_status field + remove request_logs_bodies JOIN
   - V2 详情移除对 request_logs_bodies 的依赖
   - body_status: available | dropped | unavailable
   - 前端 SessionDetailPage 增加清理/不可用横幅
Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §5"
```

---

## 6. 子任务 4 —— DB 降级返回 503 + storage_status (P1)

**分支**: `feat/storage-status-503`
**类型**: P1 可靠性
**预计 diff**: 7 个文件, 250-450 行

### 6.1 范围
- `apihub/types.go`
- `admin/session_list.go`
- `admin/session_detail_v2.go`
- `admin/session_turns_unified.go`
- `admin/session_list_v2.go`
- `internal/observability/metrics_storage.go` (新增)

### 6.2 改动要求

1. `apihub/types.go` 新增 `HealthStorage = "storage_degraded"` 常量.
2. 新增 Prometheus 指标:
   - `session_storage_degraded_total{component="list"|"detail"|"turns"}`
   - `session_storage_degraded_pending` (gauge)
3. `admin/session_list.go` 在 `api.db == nil` 或 `withTenantTx` 失败时返回 503, 响应 body 含 `storage_status: "storage_unavailable"`.
4. 同样修改 detail_v2, turns_unified, session_list_v2.
5. 新增单测覆盖降级路径.

### 6.3 子代理提示词

```text
[Subtask 4 — DB 降级返回 503 + storage_status]

你是 worker 子代理, 负责 P1: DB 降级显式化.

# 上下文
工作区: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
当前分支: feat/storage-status-503
依赖: Subtask 3 必须已合入 (V2 详情是核心改动)

# 设计基线
- 当前: withTenantTx 失败折叠为 500; V2 详情 api.pool == nil 返回 503
- 修改目标: 统一降级返回 503 + storage_status + Prometheus 指标

# 具体改动
1. apihub/types.go: HealthStorage 常量
2. internal/observability/metrics_storage.go (新建): session_storage_degraded_total{component}, session_storage_degraded_pending
3. admin/session_list.go: HandleList 503 + storage_status
4. admin/session_list.go: HandleDetail 503 + storage_status
5. admin/session_detail_v2.go: ServeHTTP 503 + storage_status (已有部分)
6. admin/session_turns_unified.go: 503 + storage_status
7. admin/session_list_v2.go: ServeHTTP 503 + storage_status
8. tests/storage_status_test.go: 覆盖降级返回

# 验收
- [ ] go build ./... 通过
- [ ] go test ./admin/... PASS
- [ ] go test ./internal/observability/... PASS
- [ ] Prometheus 指标注册无冲突
- [ ] gofmt 干净
- [ ] git diff 行数 ≤ 600

# 提交
git commit -m "feat(admin): DB degraded returns 503 + storage_status + Prometheus
   - apihub: HealthStorage 常量
   - internal/observability: session_storage_degraded_* 指标
   - admin/session_*: 统一 503 + storage_status
Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §6"
```

---

## 7. 子任务 5 —— 镜像 outbox 性能调优 (P1)

**分支**: `feat/mirror-outbox-perf`
**类型**: P1 性能
**预计 diff**: 4 个文件, 200-350 行

### 7.1 范围
- `internal/sessionv2mirror/replay.go`
- `internal/sessionv2mirror/outbox.go`
- `settings/spec_sessions_v2.go`
- `internal/sessionv2mirror/*_test.go`

### 7.2 改动要求

1. `replay.go` 默认 batch 100 → 250, 启动 worker 协程数 1 → N (cap 4, 按 CPU 核数).
2. `settings/spec_sessions_v2.go` 新增 `sessions_v2.mirror_outbox_max_attempts` (默认 10, 范围 5-30, HotReload).
3. `replay.go` `requeue` 读 `settings.GetPlatformInt("sessions_v2.mirror_outbox_max_attempts", 10)`.
4. 新增 Prometheus 指标:
   - `session_mirror_outbox_dead_total`
   - `session_mirror_outbox_replays_total{result="ok"|"retry"|"dead"|"skipped"}`
5. 单测覆盖: 并发回放、退避封顶、dead 行不复活.

### 7.3 子代理提示词

```text
[Subtask 5 — 镜像 outbox 性能调优]

你是 worker 子代理, 负责 P1: 镜像 outbox 性能调优.

# 上下文
工作区: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
当前分支: feat/mirror-outbox-perf
依赖: 无

# 设计基线
- 当前: internal/sessionv2mirror/replay.go batch=100, 1 worker, maxAtts=10 硬编码
- 修改目标: 提高并发与可配重试上限, 增加 dead 行指标

# 具体改动
1. internal/sessionv2mirror/replay.go:
   - mirrorReplayDefaultBatch: 100 → 250
   - MirrorOutboxReaper: 启动 worker 协程数 runtime.NumCPU() (cap 4)
   - 工作协程共享一个批次 channel, 各自 claimBatch → tx
2. internal/sessionv2mirror/replay.go requeue:
   - maxAtts 改为 settings.GetPlatformInt("sessions_v2.mirror_outbox_max_attempts", 10)
3. settings/spec_sessions_v2.go: 新增 sessions_v2.mirror_outbox_max_attempts (HotReload)
4. internal/sessionv2mirror/replay.go: 新增 Prometheus 指标
5. internal/sessionv2mirror/*_test.go: 覆盖并发回放 + dead 行不复活

# 验收
- [ ] go build ./... 通过
- [ ] go test ./internal/sessionv2mirror/... PASS
- [ ] go test -race ./internal/sessionv2mirror/... PASS (并发安全)
- [ ] Prometheus 指标注册无冲突
- [ ] gofmt 干净
- [ ] git diff 行数 ≤ 500

# 提交
git commit -m "perf(sessionv2mirror): outbox reaper concurrency + configurable max attempts
   - batch 100 → 250, worker 1 → N (cap 4, NumCPU)
   - maxAtts 可配 (sessions_v2.mirror_outbox_max_attempts, 默认 10)
   - 新增 dead_total / replays_total 指标
Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §7"
```

---

## 8. 子任务 6 —— `bg/cache_trimmer.go` + BodiesTrimmer 一致性校验 (P2)

**分支**: `feat/cache-trimmer-and-bodies-consistency`
**类型**: P2 文件清理
**预计 diff**: 3 个文件 (1 新建), 300-500 行

### 8.1 范围
- `bg/cache_trimmer.go` (新建)
- `bg/bodies_trimmer.go`
- `bg/cache_trimmer_test.go` (新建)

### 8.2 改动要求

1. `bg/cache_trimmer.go` 新建 `CacheTrimmer` worker:
   - 三层遍历 `{baseDir}/{tenant}/{sid[:2]}/*.json`
   - mtime 超过 TTL 时按 mtime 从旧到新淘汰
   - 与 `BodiesTrimmer` 同架构 (WithInterval, Start, TrimOnce)
2. `bg/bodies_trimmer.go` 增加一致性校验:
   - 删除前通过 SQL 查询 `session_turns.partition_date` 与 `last_turn_no`, 跳过孤儿目录
   - 增加 `validPathID` 检查 (复用 storage/file/bodies_store.go 的实现)
3. `bg/cache_trimmer_test.go` 新建单测覆盖 TTL 淘汰、空父目录清理.

### 8.3 子代理提示词

```text
[Subtask 6 — bg/cache_trimmer.go 新建 + BodiesTrimmer 一致性]

你是 worker 子代理, 负责 P2: 文件清理 worker.

# 上下文
工作区: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
当前分支: feat/cache-trimmer-and-bodies-consistency
依赖: 无

# 设计基线
- 当前: FileCache (L1.5) 仅在 Get 命中过期时清; BodiesTrimmer 按 mtime 删 turn_*.gz 文件
- 修改目标: L1.5 增加主动 trimmer; BodiesTrimmer 增加 V2 turns 一致性校验

# 具体改动
1. bg/cache_trimmer.go (新建): CacheTrimmer worker
2. bg/bodies_trimmer.go: TrimOnce 增加 SQL 校验 (只读 session_turns 确认 last_turn_no 合理); 删除前 validPathID
3. bg/cache_trimmer_test.go (新建): 单测

# 验收
- [ ] go build ./... 通过
- [ ] go test ./bg/... PASS
- [ ] gofmt 干净
- [ ] git diff 行数 ≤ 600

# 提交
git commit -m "feat(bg): CacheTrimmer worker + BodiesTrimmer V2 turns consistency
   - bg/cache_trimmer.go 新建: L1.5 cache 主动按 mtime 淘汰
   - bg/bodies_trimmer.go: 删除前校验 session_turns.last_turn_no 与 validPathID
Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §8"
```

---

## 9. 子任务 7 —— `request_logs` 主表 archive 流水线 (P2)

**分支**: `feat/request-logs-main-archive`
**类型**: P2 数据归档
**预计 diff**: 4 个文件 (1 新迁移), 250-400 行

### 9.1 范围
- `sql/migrations/startup/746_archive_request_logs_default.sql` (新建)
- `settings/spec_lifecycle.go`
- `bg/partition_manager.go`
- `sql/migrations/startup/migration_746_test.go` (新建)

### 9.2 改动要求

1. 746 迁移: `archive_request_logs_default(p_retention_days int) RETURNS TABLE(archived_partition text, rows_archived bigint)`:
   - 遍历 request_logs 月分区
   - month_end < NOW() - INTERVAL 时: 创建归档表 `request_logs_archive_YYYY_MM`, INSERT 摘要字段 + 关键事实 (ts, tenant_id, request_id, session_id, model, prompt_tokens, completion_tokens, cost_usd, status_code, success, error_kind); 丢弃大 JSONB (body, response, headers 等)
   - 主键游标 + 1000 行/batch
2. `settings/spec_lifecycle.go` 新增 `lifecycle.request_logs_ttl_days` (默认 30).
3. `bg/partition_manager.go::archiveSpecs` 增加 `{fnName: "archive_request_logs_default", label: "request_logs"}` 项 (day=4-7).
4. `migration_746_test.go` 校验游标终止、batch 大小、statement_timeout 边界.

### 9.3 子代理提示词

```text
[Subtask 7 — request_logs 主表 archive 流水线]

你是 worker 子代理, 负责 P2: request_logs 主表归档.

# 上下文
工作区: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
当前分支: feat/request-logs-main-archive
依赖: 无

# 设计基线
- 当前: archive_request_logs 在 migration 331 移除, 无归档流水线; 只有 request_logs_bodies 自动 DROP
- 修改目标: 补全 request_logs 主表的 archive 流水线

# 具体改动
1. sql/migrations/startup/746_archive_request_logs_default.sql (新建)
2. settings/spec_lifecycle.go: lifecycle.request_logs_ttl_days (默认 30, 范围 7-365)
3. bg/partition_manager.go: archiveSpecs 增加 archive_request_logs_default
4. sql/migrations/startup/migration_746_test.go (新建)

# 验收
- [ ] go build ./... 通过
- [ ] go test ./sql/migrations/startup/... PASS
- [ ] gofmt 干净
- [ ] git diff 行数 ≤ 500

# 提交
git commit -m "feat(storage): request_logs main archive pipeline (746)
   - 746 迁移: archive_request_logs_default(p_retention_days)
   - bg/partition_manager: archiveSpecs 新增 request_logs 项
   - lifecycle.request_logs_ttl_days 默认 30
Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §9"
```

---

## 10. 子任务状态表

总控代理每完成一个子任务, 在对应行写入完成时间、commit SHA、PR URL. 失败时把原因写到备注列.

| # | 子任务 | 分支 | 类型 | 状态 | Commit | PR URL | 备注 |
|---|---|---|---|---|---|---|---|
| 1 | 会话身份契约 API 层显式标注 | feat/session-identity-contract-api | P0 | TODO | - | - | - |
| 2 | session_turn_logs 可配 TTL + summary 同步 | feat/session-turn-logs-ttl | P1 | TODO | - | - | - |
| 3 | 详情 V2 body_status + 移除 request_logs_bodies JOIN | feat/session-detail-body-status | P1 | TODO | - | - | - |
| 4 | DB 降级返回 503 + storage_status | feat/storage-status-503 | P1 | TODO | - | - | - |
| 5 | 镜像 outbox 性能调优 | feat/mirror-outbox-perf | P1 | TODO | - | - | - |
| 6 | bg/cache_trimmer.go + BodiesTrimmer 一致性 | feat/cache-trimmer-and-bodies-consistency | P2 | TODO | - | - | - |
| 7 | request_logs 主表 archive 流水线 | feat/request-logs-main-archive | P2 | TODO | - | - | - |

### 10.1 通用门禁 (每个子任务都要满足)

- [ ] go build ./... 通过
- [ ] go vet ./... 通过
- [ ] gofmt -l . 无输出
- [ ] 单测 PASS
- [ ] git diff 行数 ≤ 600
- [ ] 不修改冻结契约 (sessions_v2.enabled/shadow_write 默认值 / 镜像 outbox 不回退 in-process backlog / 5 类 ID 互不混用 / RLS / 小批量游标化 / 迁移 down 逆序断言)
- [ ] commit message 含 `Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §<subtask-id>`
- [ ] git push origin 成功
- [ ] 在本表对应行写入 `[DONE] YYYY-MM-DD HH:MM commit=<sha> pr=<url>`

---

## 11. 依赖图与合并顺序

```
[Subtask 1] ──┐
              ├──> [Subtask 3] ──┐
              │                   ├──> [Subtask 4]
              │                   │
              │                   └──> [Subtask 5] (独立, 可与 4 并行)
              │
[Subtask 2] (独立, 可与 1/3/4/5 并行)
              │
[Subtask 6] (独立)
              │
[Subtask 7] (独立)
```

总控代理建议合并顺序:

1. **Subtask 1** (身份契约) → merge to main
2. **Subtask 2** (turn_logs TTL) → merge to main
3. **Subtask 3** (body_status) → merge to main
4. **Subtask 5** (mirror outbox perf) → merge to main
5. **Subtask 4** (storage_status) → merge to main
6. **Subtask 6** (cache_trimmer + bodies consistency) → merge to main
7. **Subtask 7** (request_logs archive) → merge to main

合并后, 在本地与远端删除已合并的 7 个 feat/* 分支 (保留 main).

---

## 12. 风险与回滚

每个子任务完成后, 若生产回滚:

- 关闭对应 feat 分支的开关 (默认 feature flag 控制):
  - Subtask 1: api/session_list 仍走原逻辑 (id_kind 字段向后兼容)
  - Subtask 2: lifecycle.session_turn_logs_ttl_hours 默认 24, 行为与原来一致
  - Subtask 3: body_status 默认 "available", 前端兜底处理
  - Subtask 4: 503 仅在 api.db == nil 触发, 现有路径不变
  - Subtask 5: mirror_outbox_max_attempts 默认 10, batch 默认 250 (可降回 100)
  - Subtask 6: CacheTrimmer 默认 24h, 与 FileCache 默认 TTL 一致
  - Subtask 7: lifecycle.request_logs_ttl_days 默认 30, 主表之前没归档, 回滚只停止调度不破坏数据

如果某个子任务在 staging 单租户 canary 1×24h 验证失败, 立刻关闭对应开关, 删除 feat 分支, 不动 main.

---

## 13. 文档与代码差异对齐 (来自原审计)

| # | 文档 | 当前实现 | 差异 | 修正任务 |
|---|---|---|---|---|
| 1 | `docs/会话优化v4/客户端会话保持.md` §R1.4 R2.8 | URSM v2 资源仪表盘接口未注入主路径 | 资源槽位恒 0 | Subtask 6 (相关, 资源池重构) |
| 2 | `docs/storage/2026-09-20-session-storage-decoupling-plan.md` §5 S5 | `session_turn_logs` 24h 硬编码 | 配置化 | Subtask 2 |
| 3 | `docs/storage/2026-09-20-session-storage-decoupling-plan.md` §5 S6 | `request_logs` 主表 archive 流水线缺失 | 新增 746 迁移 | Subtask 7 |
| 4 | `docs/session-v2-config-reference.md` 阶段 5/6 | 切换读路径前置: DB 降级显式化 | 503 + storage_status | Subtask 4 |
| 5 | `docs/全面测试/48h-audit/D06-dual-storage-mode/plan.md` | 列表 API 分页未做游标化 | 游标分页 | (本次未拆任务, 可在 Subtask 1 中补) |

---

## 14. 完成定义 (Master Prompt §完成定义 落地)

- §10 状态表 7 个子任务全部 `[DONE]`
- `git log --oneline main` 显示 7 个独立 merge commit (按 §11 顺序)
- `git status` 干净
- `go build ./... && go vet ./...` 全绿
- `go test ./...` PASS (含各子任务新测)
- 所有 feat/* 分支已删除
- 在主分支 `git log --oneline -10` 末尾追加一条总控合入记录 (可选): `chore(audit): R64 handoff — 7 sub-tasks landed, storage_status + body_status + TTL configurable + archive pipeline`
- 通知用户: 全部完成, 提供 7 个 PR URL 列表
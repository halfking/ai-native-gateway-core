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
| 1 | 会话身份契约 API 层显式标注 | feat/session-identity-contract-api (9f62818c5 已并入) + fix/session-ambiguity-409 (本轮收口) | P0 | [DONE] 2026-09-27 16:20 | 0aa86d8bd | 待创建 | 9f62818c5 + 9785c2398 已带 DISTINCT/LIMIT 2 歧义守卫进 main, 但走 500 兜底且响应体回显含 tenant_id 的内部错误串; 本轮以 fix/session-ambiguity-409 收口为 409 + 固定文案 (审计 Minor-2)。**未按原计划 rebase feat/session-identity-contract-api** —— 该分支 4 个 commit 与 main 的 R69/N-1 线已分叉, rebase 会回退 main 的 `SessionTurnV2.ID json:"-"`、空轮次序列化为 `[]`、errors.Is 注释等修复, 故改为定点移植 |
| 2 | session_turn_logs 可配 TTL + summary 同步 | feat/session-turn-logs-ttl | P1 | [DONE] 2026-09-27（**语义经批判式审计修正后重做**） | 5b558deab（初版，语义有误）→ 14d34867f | 待创建 | 迁移改号 745→**753**（见 §10.2）。**初版 5b558deab 的 TTL 语义是错的，保留此行仅为留档，勿据其判断行为** —— 三条证伪见 §16。§4 第 4 项「AppendTurnInTx 内联写 turn_logs_summary」**已由既有实现满足**（cmd/gateway/turn_logs_aggregator.go，5 分钟聚合 → 写 sessions.turn_logs_summary → 删源行，main.go:1280 接线），**未重复实现**，两路写同一 JSONB 会互相覆盖 |
| 3 | 详情 V2 body_status + 移除 request_logs_bodies JOIN | feat/session-detail-body-status | P1 | TODO | - | - | - |
| 4 | DB 降级返回 503 + storage_status | feat/storage-status-503 | P1 | TODO | - | - | - |
| 5 | 镜像 outbox 性能调优 | feat/mirror-outbox-perf | P1 | [DONE] 2026-09-27 17:01 | 1cb487779 | 待创建 | rebase origin/main 后唯一文件改动 (internal/sessionv2mirror/replay.go, 103+/16-); `go build ./internal/sessionv2mirror/...` + `go vet` + `go test -race ./internal/sessionv2mirror/...` + `go build ./...` 全绿; gofmt 历史遗留 `internal/sessionv2mirror/session_dim.go` + `internal/sessionv2mirror/synthetic_session_test.go` (不在本任务范围, 见 §15 观察项) |
| 6 | bg/cache_trimmer.go + BodiesTrimmer 一致性 | feat/cache-trimmer-and-bodies-consistency | P2 | [DONE] 2026-09-28 | 37cb7964 | 待创建 | **规格过期，改做真实缺陷**：①`bg/cache_trimmer.go` 早在 035df5f74（双模式存储架构 Task 5.1）就已存在并装配，非新建；②「删除前查 session_turns 一致性 + validPathID」已被 `bg.ConsistencyWorker` + `storage.ReconcileTurnArtifacts/RepairTurnArtifacts`（审计 B-#2，2026-09-05 round2）严格取代（report-only 默认、删前 meta 复检 + mtime 宽限双保险、空闲阈值、bounded 轮转分页），补 `validPathID` 反而是永不触发的死路径。故按 §8 原意「让 CacheTrimmer 与 FileCache TTL 对齐」落地真缺陷：读侧 `FileCache.Get` 按 `lite.CacheTTLHours` 判过期、删侧 `bg.CacheTrimmer` 按 `lite.Retention.CacheHours` 删文件，两个独立旋钮无交叉校验，配成 `retention.cache_hours < cache_ttl_hours` 即静默架空 TTL、缓存退化为「只写不读」。修法 `resolveCacheTrimRetention` 取安全上界 + 收敛告警 + `cache_trim_retention` 生效值日志；配 `Retention()`/`TTL()` getter 供启动期断言。3 条变异测试全部实测可失败（含 2 处自身空断言，已修，见 §20） |
| 7 | request_logs 主表 archive 流水线 | feat/request-logs-main-archive | P2 | [DONE] 2026-09-27 | 982e3191c | 待创建 | 迁移改号 746→**754**（见 §10.2；750~753 已被占用）。**不并入 archiveSpecs()**：那套机制按「日期参数 + 标量/tuple 返回」设计，而 archive_request_logs_default 收 retention 天数、RETURNS TABLE(partition, rows) 是**每分区一行**的集合返回，硬塞会错传参数并按错列形状扫描（与 2026-09-03/04 的 42703 同源），故单列 pm.archiveOldRequestLogs（runCleanup step 12）。迁移 SQL 为前一会话草稿、本轮复核：pg_inherits 枚举月分区、1000 行小批量 + 主键游标、源分区不 DROP（R68 move-then-attach）均符合 §9 冻结契约 |

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

### 10.2 迁移编号校正 (2026-09-27)

各子任务模板里的迁移号是**编写时的快照, 已全部过期**, 照抄会与 main 上已存在的
迁移撞号 (`schema_migrations.version` 冲突, 且 751 已 applied+verified 到 245 库):

| 模板写的 | 实际占用者 | 改用 |
|---|---|---|
| 745 (Subtask 2) | `745_report_snapshots` (R63) | **753** |
| 746 (Subtask 7) | `746_report_snapshots_internal_dims` | **754** |
| — | 750 `usage_facts_daily_partition` / 751 `usage_facts_partition_tz_pin` / 752 `mock_probe_history` | 已被 R68/R70 占用 |

规则: **每个子任务开工前先 `git ls-tree origin/main sql/migrations/startup/ --name-only`
确认目标号仍空闲**; main 在 R70 期间仍以每轮数个 commit 的速度推进, 编号会继续前移。

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

---

## 15. 观察项 (R71+ 待清理)

- **O-A** `gofmt -l` 在 main 上报两个历史遗留文件 (非本次任何子任务范围):
  - `internal/sessionv2mirror/session_dim.go` — 引入于 6e4fa32dc (`feat(session): session_dim 随 V2 影子写自动维护`)
  - `internal/sessionv2mirror/synthetic_session_test.go` — 引入于 a789a05ad (`feat(db,session,storage): 存储优化方案 v2 S2 落地`)
  - 处置: 单开 `chore(fmt): gofmt session_dim.go + synthetic_session_test.go` (或随下个真正触及该目录的子任务合并), **不要混进 Subtask 5 收口 commit** (会扩散范围, 违反 §10.1 "git diff 行数 ≤ 600" 软约束)
- **O-B** `feat/session-identity-contract-api` 分支在 origin 上保留 4 个 commit (48f1141fa, 6c5b76cab, e0464968c, ad763a7ef) — 实质内容已被 main 上的 `fix/session-ambiguity-409` (0aa86d8bd) 定点移植取代; 差异为 509+/4191- 的反向 main 推进差. 处置: 在所有 Subtask 落地后, 由 R71 audit 轮一并清理 (本地 + 远端 delete branch)
- **O-C** main 落后 origin/main 2 个 commit (59712d3c7 + 78ca7d9a3, R70 D11 plan/INDEX 头指针校正); 与 §10 表无关, 下次合并或审计轮前 `git pull --ff-only` 即可
---

## 16. Subtask 2 批判式审计结论 (2026-09-27)

对 `5b558deab`（Subtask 2 初版）做独立只读审计后**证伪了任务前提本身**。三条
均以 file:line 为据，不是风格意见：

### F-1 (Blocker) 谓词差了一整个 TTL，保留期实际翻倍

初版函数写 `WHERE expires_at < NOW() - make_interval(hours => p_ttl_hours)`。
但 `expires_at` 是**写入时烘焙**的：`domains/session/v2/turn_logs_writer.go` 以
`time.Now().Add(24*time.Hour)` 显式写入（生产 INSERT 永远带该列，430:270 的列
DEFAULT 实际是死代码）。所以对 T 时刻写入的行（`expires_at = T+24h`）：

    删除时刻满足  T + 24h < NOW() - p_ttl_hours
    p_ttl_hours=24  →  NOW() > T + 48h

**默认 24 实际保留 48h**，与「默认 24 = 保持原状」相反。初版 commit message
写的「与原硬编码行为逐字节一致」是错的。已改为纯到期判定
`WHERE expires_at < NOW()`。

### F-2 (Blocker) 「24h 硬编码清理」从来不存在

430:336 定义了 `cleanup_expired_session_turn_logs()`（`expires_at < NOW()`），
513 重写过一次，**但全仓无任何调用方**：Go 无引用、无 pg_cron 注册、无 shell
调度；只出现在 baseline dump、`scripts/test-migration-430.sh` 与 513 测试里。

即 **session_turn_logs 在生产上从未被清理过，表无界增长**。所以本子任务不是
「把既有清理参数化」，而是「第一次真正接上清理」——这同时意味着 F-1 的
「行为变化」其实是「从无界增长变成有界增长」，方向上是修复，但初版的描述与
理由都写错了。清理入口见 `bg/partition_manager.go` 的 `runCleanup` step 11。

### F-3 (Blocker) 真正的硬编码在 Go 侧，不在 SQL

见 F-1 引用的 `turn_logs_writer.go`。只改 SQL 谓词，设置永远不会生效
（`expires_at` 早已按 24h 烘焙死）。已改为写入方读
`lifecycle.session_turn_logs_ttl_hours`（`sessionTurnLogsTTL()`，夹取 1..168）。
**保留期是写入时决定的**：改设置只影响新写入的行；已写入的行仍按原
`expires_at` 到期被清。这是本设计的固有性质，不是缺陷，但运维必须知道。

### F-4 (Major) 753 建了重复索引

430:275 已有 `idx_session_turn_logs_expires ON session_turn_logs(expires_at)`，
初版又建了同列的 `idx_session_turn_logs_expires_at`。在每 turn 写 7 行的热表
上挂第二个同列索引 = 纯写放大 + 磁盘占用，零查询收益。初稿注释以「与迁移族命名
对齐」为由保留，属错误权衡。已移除；清理谓词命中 430 原索引。

### F-5 (Major) 第三条死路径：Go 侧 CleanupExpiredLogs（本轮审计遗漏，独立审计发现）

`domains/session/v2/turn_logs_writer.go:252` 的 `CleanupExpiredLogs(ctx)` 做的是
`DELETE ... WHERE expires_at < NOW()` —— **谓词本来就是对的**。但全仓除自身单测
外无任何调用方，同样从未在生产执行过。

所以历史上有**两条**死清理路径（SQL 的 `cleanup_expired_session_turn_logs()`、
Go 的 `CleanupExpiredLogs()`），这才是「表无界增长」的完整解释。已加 Deprecated
注释指明新归属，但**故意不删**：它是导出方法，仓外可能有调用者，删除的爆炸半径
超过本子任务该承担的范围。清理留给后续独立 chore。

### F-6 (**已推翻，见 §17**) 与 TurnLogsAggregator 的时序交互 —— 本轮自评过轻

`cmd/gateway/turn_logs_aggregator.go` 的取数条件是 `expires_at > NOW()`，聚合器
**只看得见未过期的行**；而过期行会被 TTL 扫描删除。因此存在「行先过期（对聚合器
隐形）、后被清理」的丢数窗口。

**我在这里写的结论是「非缺陷」，这是错的。** 理由见 §17 F-7：轮询查询是
`GROUP BY tenant_id, session_id LIMIT 100` 且**没有 ORDER BY**，选择不确定；
长期排不到队的会话，其行会在被聚合之前先到期删除 —— 不是「延迟」，是静默丢数。
当时的推理只考虑了「聚合器 5 分钟一跳 / TTL 24h，窗口够大」，没有检查**选择
本身是否公平**。

### 独立审计交叉验证

本轮另起了一个 verifier 子代理做独立只读审计，结论与上述 F-1/F-2/F-3/F-4 一致
（各自独立用 file:line 复算），并额外发现 F-5。审计还确认：
- `scripts/test_sessions_v2_api.sh` 不对状态码做任何断言，Subtask 1 的
  500→409 变更在仓内无消费方；
- 迁移 753 的 `schema_migrations` 自注册守卫与 742/743 同形（但与 750/751/752
  不同 —— 那三个不自注册），属既有分歧，非本轮引入。

### 附带修正：断言必须剥注释

753 的头部**故意**逐字记录了初版的错误谓词与重复索引（审计留档）。初版测试用
`strings.Contains` 扫全文，于是「不得再出现 X」这类断言会被自己的审计说明打中
（本次实测两处 false positive）。已改为对 `stripSQLComments(...)` 后的代码断言
（SQL 侧复用 `migration_602_test.go` 已有的 `stripSQLComments`，Go 侧新增
`stripGoLineComments`）。

> 这正是本 handoff 反复强调「断言必须能变异失败」的另一个实例：一条会因注释
> 而误报的断言，与一条永远不会失败的断言同样是坏断言。

### 未能在本地验证的部分

迁移 SQL 未对真实 PostgreSQL 执行过（本机无可用 PG 实例）。`RAISE EXCEPTION`
 分支、索引选择、事务内 DDL 行为均**未经真库验证**。上线前应在 245/252 的
 测试库上跑一次 up/down 往返。

---

## 17. 第二轮批判式审计 (2026-09-27 19:48)

对象是**第一轮修正本身**（`14d34867f` / `d10d782d9`）——刚落地、无人复核的代码。
结论：F-1…F-5 的修法成立，但**我对自己写的 F-6「非缺陷」判断是错的**，并牵出一个
此前完全没人看过的数据丢数缺陷。

### F-7 (Blocker) 聚合器轮询无序 → 静默丢 turn 日志（本轮新发现）

`cmd/gateway/main.go` 的聚合器 goroutine 里，轮询查询是：

    SELECT tenant_id, session_id
    FROM public.session_turn_logs
    WHERE expires_at > NOW()
    GROUP BY tenant_id, session_id
    LIMIT 100          -- 没有 ORDER BY

两个问题，都不报错：

1. **选择不确定。** 没有 ORDER BY，PostgreSQL 可以返回任意 100 个
   (tenant, session)。有持续流量的会话永远有未过期行、恒在候选集里，可以反复
   挤掉安静的会话。
2. **截断是静默丢数，不是延迟。** 候选集只含 `expires_at > NOW()` 的行，而
   TTL 扫描会删掉过期行。一个始终排不上队的会话，其 stage 日志会在**被聚合之前
   就到期删除** —— 该 turn 永远不会出现在 `turn_logs_summary` 里，日志里也
   没有任何错误。F-6 正是被这一点证伪。

**修法**：把查询从 main.go 内联字面量抽成
`TurnLogsAggregator.PendingSessions(ctx, limit)`（原先完全不可测），并加
`ORDER BY MIN(started_at) ASC, tenant_id ASC, session_id ASC`。行在被 flush 后即
删除，候选集因此是一个 FIFO 队列：最老的未处理工作永远排在最前，安静会话不会被
无限挤掉。`tenant_id/session_id` 是 tie-breaker，让排序是全序而非仅非降序。

**吞吐（是取舍不是缺陷，但改之前要算账）**：每 tick 一页 = 100/5min =
1200 session/小时；积压只在该数之上出现，而一个 session 有整个 TTL（默认 24h，
约 28800 session）的余量。每 tick 多翻几页会成倍放大 GROUP BY 成本，应当是
**实测后**的决定，不该拍脑袋。

### F-8 (**已降级**：有仓内证据表明迁移先于新二进制应用)

`bg.PartitionManager.cleanupSessionTurnLogsByTTL` 调用
`cleanup_session_turn_logs_by_ttl($1)`，该函数在迁移 753 应用前不存在。失败是
**非致命的**（`slog.Error` 后 `return`，且它是 runCleanup 的最后一项，不影响前
10 项清理），但特性在该库上会是惰性的。

**我先写的是「无法在仓内验证部署顺序」，这是过度保守**，独立审计在同一仓内找到了
证据（由它提供，我复核）：

- `scripts/deploy-lib.legacy/db-changelog.sh` 头部：「**切换前**在 252 PG 上应用
  sql/migrations/startup/NNN_*.sql，避免 restart 时 EnsureSchema 长时间阻塞」
- `scripts/deploy-154.sh:6`：「默认：前后端同时构建 + **切换前 DB 迁移** + 原子
  符号链接切换 + db-changelog」

即标准部署路径是**先迁移、后切换**，753 会在新二进制起来之前落地。降级为观察项：
若绕过标准部署路径（手工只换二进制），该库会持续报 `42P01 undefined_function`
直到补跑迁移。

### F-9 (Blocker) turn_logs_summary 被整体覆盖，早期 turn 静默丢失

`UPDATE public.sessions SET turn_logs_summary = $1::jsonb` 是**全量替换**。
而 flush 结束后会 DELETE 掉刚聚合的行 —— 于是对一个**有持续流量**的会话：

    tick1:  读到 turn_1 → 写 summary={turn_1} → 删除 turn_1 的行
    tick2:  只剩 turn_2 的行 → 写 summary={turn_2}  ← 覆盖掉 turn_1
    最终:   summary 只剩最后一批，不是会话的 turn 历史

`admin/session_detail_v2.go:99/317/355` 把这一列直接透出给 API 消费方，所以暴露的
就是残缺的历史。

**根因**：flush 的 UPDATE 与 DELETE 是一对，但 UPDATE 用「替换」语义，而 DELETE
又让读集在下一次 tick 变短，两者叠加使得「累积」退化为「最后一批」。

**修法**：`SET turn_logs_summary = COALESCE(turn_logs_summary, '{}'::jsonb) || $1::jsonb`。
jsonb `||` 是顶层键的浅合并，而键是 `turn_N`、每批互不相同，浅合并正是所需语义。
`COALESCE` 不可省：SQL 里 `NULL || x` 结果是 NULL。附带收益是**重放幂等** —— 若进程
在 UPDATE 与 DELETE 之间挂掉，下一 tick 会重新聚合同一 turn 并覆盖自己的键（等值），
而不是抹掉其余 turn。

### F-10 (Major) flush 的 SELECT→DELETE 竞态：新写入的 stage 被删但从未聚合

原 DELETE 重新推导谓词 `WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()`
—— 它是**另一条语句**，谓词重新求值。SELECT 与 DELETE 之间为在途 turn 写入的 stage
行，会在这个窗口内**被删除而从未进入 summary**。窗口窄，但完全静默。

同一谓词还让「空会话」分支成为空操作：SELECT 已用同一条件判定「没有未过期行」，
那么同样条件的 DELETE 不可能命中任何行 —— 那段代码注释写着「trim any stray rows
that already expired」，而谓词恰好与之相反。

**修法**：SELECT 捕获 `id`，DELETE 改为 `WHERE id = ANY($1)`，精确覆盖本次读集；
空分支随之不再需要 DELETE，直接返回。

### 本轮独立审计的贡献（与我的自查互补）

第二轮另起 verifier 子代理做只读审计，逐条独立复算后：确认 F-7 与我的判断一致；
**F-9（summary 被覆盖）与 F-10（SELECT→DELETE 竞态）由它先发现**，我复核代码后确认
成立并修掉 —— 这两条我自己第一遍没看出来。也是它找到 F-8 的降级证据。
连续两轮的事实是：**我的自查能抓住「我刚写的代码哪里算错了」，但对「我顺手点头
说『既有实现已满足』的老代码」系统性失灵**。第 1 轮我曾断言 TurnLogsAggregator
「已完全满足 handoff 第 4 项、无需重复实现」，那个判断就是这两条的根源。

### O-D 共享主 worktree 被并行会话切了分支（操作事故，已处置）

本轮开工时主 worktree `/llm-gateway/workspace/.../llm-gateway-go-cursor` 在
`main`；干活途中并行会话在同一目录 checkout 了
`feat/session-detail-body-status`（Subtask 3）。我的修改因此落在**它的分支**上，
`git push origin main` 一度报 "Everything up-to-date"（推的是本地 main 指针，
停在父提交）—— **差点以为已推送成功**。

处置与遗留：
- 我的提交 `8a34eab76` 的父提交恰为 `d10d782d9`(= main)，且该分支上没有并行
  会话的其他提交（工作区当时干净），因此可无损分离。
- 另开 worktree `/private/tmp/llm-gw-audit2` 承载 `main`，ff 到 `8a34eab76` 并推送，
  远端已确认 `refs/heads/main = 8a34eab76`。
- **未触碰并行会话的 worktree**（它可能正在编辑，切分支会打断它）。
- **遗留动作（需人工确认）**：`feat/session-detail-body-status` 目前仍指向
  `8a34eab76`，即含本轮 turn-logs 改动。该分支应先 `git reset --hard main`
  （或 rebase 到 main）再继续 Subtask 3，否则会把无关改动带进 Subtask 3 的 PR。

> 教训：`git push` 报 "Everything up-to-date" **不等于**你刚提交的代码在远端 ——
> 它可能推的是另一个 ref。落盘后必须用 `git ls-remote origin refs/heads/main`
> 核对远端真实值，不能只看 push 输出的措辞。开工前也应确认当前分支
> （`git branch --show-current`），共享 worktree 下这不是自动成立的。

### 本轮自评教训

F-6 写「非缺陷」时我只验证了「窗口够大」（5 分钟一跳 vs 24h TTL），**没有验证
选择是否公平**。两个条件是独立的：窗口决定「单次会不会丢」，公平性决定「会不会
一直丢」。前者成立不构成后者的结论。下一轮凡写「非缺陷」，必须同时说明
「为何不会被绕过/饿死」，否则按「待验证」记。

---

## 18. 第三轮批判式审计 (2026-09-27 22:38)

对象是第二轮刚落地的 Subtask 7（`982e3191c` / `85972cf1c`）。两条发现都不是
「写错了代码」，而是**承诺与实现不符**——比语法错误更难发现，因为测试全绿。

### 自查先说一件难看的事

我在 Subtask 7 的 commit message 里写了对该迁移的正面评价，而**我实际上只读了
它 228 行里的约 60 行**。第 120 行之后的批式游标 INSERT 循环是我没读就写下的
部分。下面两条发现全部位于我没读的那段。

### F-11 (Major) 这是摘要抽取，不是数据搬移；主表不会变小

迁移 754 的函数里**没有任何 DELETE**（剥注释后 `grep -c DELETE` == 0，有契约
测试钉住）。源分区一个字节都不会被删 —— 迁移头自己写明了原因：R68 禁止 DROP
partition_by_range 父表的月分区（654 / 337 事故复盘）。

所以：

- `request_logs` 主表**不会因为这个迁移而变小**，它仍在增长；
- 开启 `lifecycle.request_logs_ttl_days` **不等于**「旧数据离开主表」，只等于
  「旧分区多一份 11 列摘要副本供对账/合规回溯」。

「archive」这个词、以及 handoff §9 的标题「主表 archive 流水线」，都极易被读成
前者；**我在 §10 状态行和 commit message 里都没有点破这一点**，等于替它背书。
已在迁移头、setting 的 DescriptionLong 里显式写明。

这不是可修的缺陷——受 R68 约束，SQL 层面没有安全选项；它是一个**必须说出口的
期望差**。

### F-12 (Major) SQL 无「已归档」标记，挂在每小时 tick 上 = 永不收敛的全表重扫

函数枚举所有 `month_end` 已过期的月分区，对每个分区把**全部行**再走一遍
`INSERT ... ON CONFLICT (request_id, ts) DO NOTHING`。已归档的行确实被唯一索引
冲突吸收、不重复写，但**行仍然被读取、投影、再走一遍插入尝试**。

因此单次成本 = O(所有超过保留窗口的行)，且随时间单调增长、不会自行收敛。
而我把它接在了 `runCleanup` 上，`providerErrorCleanupInterval = 1h`（第 57 行）。

合起来就是：一张持续增长的主表，每小时被完整重扫一遍历史数据，而产出为零。
这是我上一轮 wiring 时的直接疏漏——我只验证了「函数被调用了」，没验证
「调用的代价是多少」。

**修法（有意克制）**：Go 侧限到**每日一次**（`shouldRunRequestLogsArchive`，
本地时区 03:00 那一小时），代价降低约 24 倍，且**不碰那份从未在真库执行过的
SQL**。彻底解法是加一张 archive ledger 记录 `(partition, max_id)`，已归档分区
直接跳过——但那份 SQL 一次都没在真实 PostgreSQL 上跑过，往里盲加一张新表，
风险比留着高。列为后续。

### 一条我自己的注释把自己的自检数成了 1

我在迁移头写「可用 `grep -c 'DELETE FROM' 本文件 == 0` 自证」，然后这条注释本身
含有字面量 `DELETE FROM`，自检立刻返回 **1** —— 自己的说明把自己的回归测试打红。
与前两轮 F-6/第二轮 C7 同类：**会因注释而误报的断言，和永不失败的断言一样是坏
断言**。已改为剥注释后再数，并在迁移头里把这个坑写下来。

同理，N1 变异（删掉 `shouldRunRequestLogsArchive` 的**调用**）第一版测试全绿 ——
因为它只测纯函数，看不见 wiring。已补
`TestArchiveOldRequestLogs_GateIsActuallyInvoked`，并把断言范围限定在函数体内，
重跑必红。**这是连续第二次在同一处栽（第二轮 C7），已固化进下一轮规则。**

### 一条我自己的误报

我一度判定 `request_logs` 缺 `provider_model` / `cost_usd` / `success` /
`error_kind` 四列（据 000_base_tables 的表体解析）。**这是误报**——这四列确实存在，
证据是 573 重建的视图 SELECT 列表里逐列出现。四列均 OK，迁移的列清单有效。
记在这里是因为「差点凭一个窄窗口的 grep 就断言迁移会 42703 上线即挂」。

### 变异测试（本轮 4 条，逐条实测非空转）

  删每日门槛的**调用**      → FAIL
  门槛改成每小时都跑        → FAIL（24h 内触发 24 次）
  给归档函数加一条 DELETE   → FAIL（违反 F-11 不变量）
  SELECT FROM id 改零值累加  → FAIL（第二轮已有，回归通过）

---

## 19. 交接状态总表 (2026-09-28 00:15)

三轮批判式审计后的权威状态。**先读本节，再读 §16/§17/§18 的逐条证据。**

### 子任务进度

| # | 状态 | 落点 | 审计结论 |
|---|---|---|---|
| 1 | [DONE] | `0aa86d8bd` → main | 歧义 409 + 500 不回显内部错误串。已独立审计（0 Blocker） |
| 2 | [DONE]（**重做过**） | 初版 `5b558deab` 有误 → 修正 `14d34867f` | §16 F-1…F-5：谓词差一整个 TTL、清理从未被调用、硬编码在 Go 侧、重复索引、第三条死路径 |
| 3 | **进行中（并行会话）** | `feat/session-detail-body-status`（worktree `/private/tmp/llm-gw-sub3`） | 未审计。本会话不接手：工作区 7 个文件在途，含 `admin/body_status.go` + 其测试 |
| 4 | TODO | - | 必须在 3 之后串行（共用 `admin/session_detail_v2.go`） |
| 5 | [DONE]（并行会话） | `1cb487779` | 本会话未复核其代码，仅确认已进 main |
| 6 | **零提交**（`feat/cache-trimmer-and-bodies-consistency`，worktree `/private/tmp/llm-gw-cache-trim`） | 原写「新建 cache_trimmer.go + BodiesTrimmer 一致性」两条**均已被更强实现取代**（见 §20 F-13）；规格过期 |
| 7 | [DONE] | `982e3191c` → 修正 `c7104b141` | §18 F-11（摘要抽取不删源数据）+ F-12（每小时全量重扫） |

### 三轮审计的净产出（供下一轮直接采信，勿重复劳动）

- 修正的**真实缺陷**：2×TTL 保留期、两条从未被调用的清理路径、写入侧硬编码、
  重复索引、聚合器轮询无序（FIFO）、`turn_logs_summary` 被整体覆盖、
  flush 的 SELECT→DELETE 竞态、归档扫描每小时全量重扫。
- **推翻的自我判断**：§16 F-6「非缺陷」错；§17 F-8「无法验证」过度保守；
  第二轮「四列缺失」是误报。
- **三次同型自身缺陷**（下一轮必须避免）：
  1. 断言只 grep 到**函数定义**、看不见**调用点**（§16 C7、§18 N1，连续两次）；
  2. 注释里的字面量把自己的回归测试打红（§16「剥注释」、§18 `grep -c 'DELETE FROM'`）；
  3. 上一轮对某文件只读了约 1/4 就写 commit message 评价（§18 自查）。

### 阻塞与待办（按优先级）

1. **【发布阻塞】迁移 753 与 754 从未在真实 PostgreSQL 上执行过。** 本机无 PG。
   必须在 245/252 测试库跑 up/down 往返；754 还需跑一次真实归档确认动态建表、
   `pg_inherits` 枚举、游标分页的实际计划。
2. **【运维必读】`lifecycle.request_logs_ttl_days` 不让主表变小**（§18 F-11），
   只是摘要抽取；主表仍增长，需另行处置。
3. **【性能】归档扫描无「已归档」标记**（§18 F-12），已限为每日一次；彻底解法是
   archive ledger 记 `(partition, max_id)`，**因 SQL 从未真跑而有意未做**。
4. **【归属冲突，2026-09-28 00:2x 实测】Subtask 3 同时被两个活跃会话写。**
   `mavis session list` 显示两个 `status: started` 的会话都在动
   `/private/tmp/llm-gw-sub3`：
   - `mvs_b6c2c553676947b783a1c7369fb5f102`（"审计完善会话存储优化"）——正在跑
     `go test ./admin/ -run TestComputeBodyStatus`，**实测 3 个子用例 FAIL**
     （`25h retention 向上取整到 2 天` 期望 dropped 实得 unavailable；
     `{}` / `[]` 空 JSONB 被判 available 而非 dropped）。
   - `mvs_80c00a17c1a74891b55503348ac215d6`（**与主控同一份 handoff 提示词**）——
     正在读 `session_bodies_unified` 视图确认是否暴露 `partition_date`。
   两边都在改 `admin/session_detail_v2.go` / `admin/body_status.go` 等同一批文件。
   **接手前必须先确认这两个会话是否已停**；`ComputeBodyStatus` 的
   `retentionHours → math.Ceil(h/24)` 换算本身可疑（§5 要求按小时比
   `NOW() - retention_hours`，按天取整会在 25h/23h 边界出错），但**不要在归属
   未澄清前动手**。
5. Subtask 3 完成后才可动 Subtask 4（串行，共用 `admin/session_detail_v2.go`、
   `admin/session_turns_unified.go`）。
6. ~~Subtask 6 需从零开始~~ **[DONE] 2026-09-28**，见 §20：原规格两条交付物均已被
   取代，改为修 F-13（读侧 TTL / 删侧 retention 双旋钮无约束）。
7. 7 个 PR URL 仍需人工在 Codeup 浏览器创建（无 CLI 凭据），§10 的 PR 列保持「待创建」。
8. 清理死代码 `TurnLogsWriter.CleanupExpiredLogs()` 与
   `cleanup_expired_session_turn_logs()`（已标 Deprecated，故意未删）。
9. `feat/session-identity-contract-api` 保留 4 个与 main 分叉、rebase 会回退 R69/N-1
   修复的 commit，全部子任务完成后连同其它 feat/* 一并删除。

### 环境事实

- 本地代理 `127.0.0.1:7897` **已失效**（`HTTP(S)_PROXY` 仍指向它）。所有 push 用
  `env -u HTTP_PROXY -u HTTPS_PROXY … git push` 直连阿里云 Codeup 绕过。
- 仓内有**并行会话**持续向 main 提交（r0924/r0926 审计轮）。落盘前须
  `git fetch && git rebase origin/main`；落盘后必须用
  `git ls-remote origin refs/heads/main` 核对远端真实值——`git push` 报
  “Everything up-to-date” 可能推的是另一个 ref（§17 O-D）。

---

## 20. 第四轮：Subtask 6 规格过期，改做真缺陷 (2026-09-28)

### 结论先行：§8 的两条交付物都已被更强实现取代

§8 写的两条都**不成立**（下盘前核对 SSOT 现行值才发现）：

1. **「新建 `bg/cache_trimmer.go`」——文件早就存在**。`035df5f74`（双模式存储架构
   Task 5.1）已建好 `CacheTrimmer`（`WithInterval` / `Start` / `TrimOnce` 三件套齐全）
   并在 `cmd/gateway/storage_mode_init.go:163` 装配。按原规格写会是重复实现。
2. **「BodiesTrimmer 增加一致性校验：删除前查 `session_turns`、加 `validPathID`」
   —— 已被严格取代**。`bg/consistency_worker.go`（审计 B-#2，2026-09-05 round2）
   配 `storage.ReconcileTurnArtifacts` / `RepairTurnArtifacts`，比 §8 设想强得多：
   report-only 默认不动数据、删除路径自带「删除前 meta 复检 + mtime 宽限」TOCTOU
   双保险、空闲阈值挡在途写入、单轮 500 上限 + OFFSET 轮转覆盖（消除老会话饿死）。
   且 `validPathID` 那一项**在 BodiesTrimmer 里是永不触发的死路径**：`sess.Name()`
   来自 `os.ReadDir`，是 base name，不可能为空串 / `.` / `..` / 含分隔符。补它等于
   再造一条本审计三轮都在清理的死路径（§16 F-2、F-5 同型）。

### F-13（Major，本轮新发现并已修）读侧 TTL 与删侧 retention 是两个无约束的独立旋钮

- 读侧 `FileCache.Get` 判过期：`time.Since(mtime) >= fc.ttl`，`ttl ← lite.CacheTTLHours`。
- 删侧 `bg.CacheTrimmer` 删文件：`mtime.Before(now - retention)`，
  `retention ← lite.Retention.CacheHours`。
- 两者**没有任何交叉校验**，`config.ApplyLiteDefaults` 各自独立兜底为 24。默认配置
  下两侧同为 24，分歧完全不可见；一旦运维把 `retention.cache_hours` 配得比
  `cache_ttl_hours` 小，删除侧就在**读侧仍认为有效**的时间窗内删文件：每个读都退化成
  miss 并回源下层，FileCache 退化为「只写不读」，`cache_ttl_hours` 被静默架空。
  （仓内现有测试 `cmd/gateway/storage_mode_init_test.go` 恰好用 `2 / 24` 这组**分叉**
  值，但方向安全、且只断言装配不报错，从未断言过两侧的关系。）

修法（§8 唯一仍然成立的那句「让 CacheTrimmer 与 FileCache TTL 对齐」）：

- `resolveCacheTrimRetention(cacheTTL, retentionCacheHours)` 取**安全上界**：
  `retention < ttl` 时收敛到 `ttl`；`retention > ttl` 时保留更大值（逻辑 TTL 之后
  多留一段，抬高 TTL 时免冷启动重填）。
- 收敛时打 `slog.Warn`（带三个值），启动日志新增生效值 `cache_trim_retention`。
- **不做硬报错**：存量部署可能已配了更小的 `cache_hours`，拒绝启动的爆炸半径远大于
  收益；缓存层没有数据正确性风险，最坏退化成「多留一会儿」。
- 文档跟齐：`docs/storage/deployment-guide.md` 原写「调小只影响重启后首请求的回源
  次数」——**这句本来就是错的**（配小是每个读都回源，不是只有首请求），已改正；
  `config/storage.go` 字段注释 + `config.example.yaml` 同步。

### 两条自身缺陷（本轮自查，均由变异测试暴露，非事后补记）

**第 4 次同型缺陷：断言只看到「我算的值」，看不见真实调用点。**
初版把解析结果另存进 `storageRuntime.cacheTrimRetention` 快照字段，测试断言
`rt.cacheTrimRetention >= rt.fileCache.TTL()`。变异测试 M1（把
`NewCacheTrimmer` 的调用点改回 `Retention.CacheHours`，即还原成修复前的真实行为）
**测试照样通过**——因为快照字段与真实 worker 已经脱钩，断言只验证了自己的算术。
这与 §16 C7、§18 N1 是同一个形状。修法：改存真实 worker 引用，
`bg.CacheTrimmer.Retention()` / `v2.FileCache.TTL()` 两个 getter 读真实字段。

**第 2 次同型缺陷：getter 少报导致不变量断言退化成恒真。**
加 getter 后若 `TTL()` 少报（例如返回 0），`retention < TTL` 恒为假，断言静默变成
空断言。变异测试 M3（`TTL()` 恒返回 0）**未被抓到**，据此补了一条锚定断言：
`rt.fileCache.TTL()` 必须等于配置的 `cache_ttl_hours`，让 getter 无处少报。

### 变异测试（3 条，逐条实测非空转）

| 变异 | 期望 | 实测 |
|---|---|---|
| M1 调用点改回 `Retention.CacheHours`（修复前行为） | FAIL | 修复断言前**未失败**（自身缺陷 1）；修复后 FAIL ✓ |
| M2 `resolveCacheTrimRetention` 去掉收敛分支 | FAIL | FAIL（6 个子用例）✓ |
| M3 `FileCache.TTL()` 恒返回 0 | FAIL | 补锚定断言前**未失败**（自身缺陷 2）；补后 FAIL ✓ |

M1/M3 两处「变异存活」是本轮最有价值的产出：它们证明单测全绿不等于断言有效。

### 门禁

`go build ./...` 全绿；`go vet ./cmd/gateway/... ./bg/... ./domains/session/v2/...
./config/...` 无输出；`gofmt -l`（仅本次改动文件）无输出；`go test ./cmd/gateway/
-run 'StorageMode|CacheTrim|Lite'`、`./bg/ -run 'CacheTrimmer|BodiesTrimmer'`、
`./domains/session/v2/ -run FileCache`、`./config/` 全 PASS；diff 198 行（≤600）。
顺带修掉 `domains/session/v2/cache_v2_file.go` 的文件末缺换行（该文件此前
`gofmt -l` 一直有输出，属 §19 记录的基线噪音之一）。

# JournalSnapshot 特性审计报告

**审计范围:** JournalSnapshot bounded materialization、授权查询、RequestJourney bridge、durable receipt、迁移与部署执行面

**审计依据:**
- `docs/adr/2026-08-28-requestjourney-journal-snapshot.md`
- `.handoff/2026-08-29-journalsnapshot-*.md`
- dispatch pipeline、admin API、RequestJourney receipt、runtime/installer migration 路径

## 需求矩阵

| 需求 | 结果 | 验证 |
|---|---|---|
| terminal snapshot 只发射一次 | 通过 | pipeline completion CAS 与 dispatch tests |
| 最多 50 个 entry，保留 terminal，显式 truncation | 通过 | bounded contract tests |
| tenant/request scoped 查询 | 通过 | `JournalSnapshotQuery` 与 admin API |
| tenant admin 隔离、super admin 显式跨租户 | 通过 | handler authorization tests |
| 缺失/越权统一 not-found | 通过 | `ErrJournalNotFound` contract tests |
| `(tenant, request, version)` 幂等 | 通过 | local hash receipt + PostgreSQL receipt |
| 同 key payload conflict | 通过 | receipt hash tests |
| lease claim/complete 与 stale owner fencing | 基础通过 | pgxmock receipt tests；真实 PG matrix 仍待补充 |
| persistence failure 不改变 settlement | 通过 | sink failure isolation tests |
| 不复制 request/response body | 通过 | receipt schema 只保存 integrity/lease metadata |
| 生产 admin route 可达 | 通过 | gateway composition wiring 与 admin tests |
| fresh/runtime/offline migration 覆盖 | 通过 | installer/runtime/static checks |
| retention 不无限增长 | 通过 | InMemory LRU/TTL 与 receipt retention tests |

## 数据流

```text
QueuedRequest.AttemptJournal
  -> Pipeline.emitJournalSnapshot (bounded, terminal metadata)
  -> dispatchJourneyJournalAdapter
  -> PostgreSQL receipt claim (DB enabled) / bounded local fallback
  -> RequestJourney Recorder event projection
  -> instance-local JournalSnapshotStore
  -> authenticated admin JournalSnapshotAPI
```

receipt 表只保存 tenant/request/version、payload hash、状态、lease 和时间字段；不保存请求或响应 body。当前 HTTP snapshot entries 使用 instance-local bounded LRU read model，因此重启和跨实例历史查询仍是明确的 ephemeral 语义，而不是 durable body 存储。

## 修复项

1. 注册 `GET /api/admin/dispatch/journal/{tenant}/{request_id}`，并使用 authenticated admin wrapper。
2. 将 consumer 查询改为显式 caller/target tenant/query role 语义，handler 对缺失身份和未知角色 fail-closed。
3. 对返回 snapshot 的 tenant/request 做二次 identity 校验；路径解码后拒绝 slash、控制字符、反斜杠和 dot segment；405 返回 `Allow: GET`。
4. 将 InMemory store map key 改为结构体 key，增加版本单调保护和容量/LRU 淘汰。
5. 统一 durable receipt 与本地 fallback 的 canonical hash 字段，排除 caller authorization metadata。
6. 禁止活跃 receipt lease 被同 owner 重入 claim；Apply 失败不完成 receipt，允许 retry。
7. 补齐 migration 618 的 installer embed、runner、runtime ensure 和 offline upgrade bundle；修复 552/553 bundle 数组拼接错误。
8. 将 completed/过期 processing receipt 纳入 RequestJourney retention。
9. 增加 receipt、授权、并发、LRU/TTL、migration registration 和 runtime schema 回归测试。

## 本轮继续修复（2026-08-30，生命周期与部署回归）

- `dispatchJourneyJournalAdapter` 的 cleanup worker 现在只允许启动一次，worker 捕获自己的 ticker；`Close` 对 nil、重复和并发调用均幂等，并等待 cleanup worker 与已进入的 snapshot Apply 完成；关闭后新 Apply 直接 no-op。
- 独立 `GET /api/admin/sessions/detail` 统一复用 `tenantFromQueryOrContext`：非平台角色固定认证租户，只有 `super_admin`/`admin_key` 能显式选择 query tenant，缺失认证上下文 fail-closed；新增 handler 级 pgxmock 参数断言。
- installer canonical/embed parity、backup/setup 输出、runner 顺序和 offline forward migration 测试均纳入 600/601/602/618；修复 parity 测试中的重复 553 条目，并锁定 `552 < 553 < 600 < 601 < 602 < 618`。
- retention receipt cleanup 不再通过错误字符串吞掉数据库错误；receipt 删除失败（包括 PostgreSQL `42P01`）会回滚并返回错误，避免在事务已 aborted 时虚假报告成功；新增错误语义和已启动 worker 并发 Stop 回归测试。

## 残余风险

- Snapshot entries 本身仍是 instance-local；PostgreSQL receipt 只保证幂等 metadata，不保证重启后恢复完整快照。
- Recorder 的 `MaxSeq` 分配与普通 observation Apply 仍属于不同调用边界；更强的原子批量 snapshot event 写入可作为后续改进。
- Receipt lease 仍是固定时长，超长 Apply 需要后续 heartbeat/claim token 演进。
- RLS 目前有 SQL 静态、mock 和可选 integration 覆盖，真实 PostgreSQL tenant/super-admin matrix 仍建议纳入部署验证。
- 真实生产数据库和离线升级包未在本机执行；本轮验证覆盖源码、静态注册、mock、installer 包测试、race 测试和脚本语法，不能替代现场升级演练。

## 验证证据（2026-08-30）

```text
go test ./admin ./cmd/gateway ./domains/dispatch ./domains/requestjourney ./db -count=1
# PASS

go test -race ./admin \
  -run 'TestTenantFromRequestOrContext|TestSessionV2TenantResolvers|TestSessionDetailServeHTTP|TestExtractDialogueContent|TestBuildConversationText' \
  -count=1
# PASS

go test -race ./cmd/gateway ./domains/dispatch ./domains/requestjourney \
  -run 'TestJournalSnapshot|TestInMemoryJournalStore|TestDispatchJourneyJournalAdapter|TestCleanup|TestRetention' \
  -count=1
# PASS
(cd installer && go test ./internal/dbinit ./internal/upgrader ./cmd/llm-gw-installer -count=1)
# PASS
bash -n scripts/build-upgrade-package.sh
# PASS
```

审计结论：JournalSnapshot 的主要功能链路、adapter 生命周期边界、独立 Session Detail 租户授权和 618 多执行面静态回归已闭环；剩余项属于真实 PostgreSQL/RLS 与现场升级验证、跨实例 snapshot body 持久化和更强事件批量原子性的后续工作。

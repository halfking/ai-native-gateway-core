# 2026-08-31 近四小时代码修正审计与闭环报告

## 范围

- 审计窗口：2026-08-31 02:27–06:01（+08:00）的代码提交与当前主分支。
- 参考窗口：2026-08-30 至 2026-08-31 的审计、修复和部署方案文档。
- 重点：IR/协议转换、Session V2、hot+分区存储、附件生命周期、供应商错误与后台任务、安装器迁移闭环、并发和可观测性。

## 本次已修正

1. 安装器新装路径补齐 startup 627–631：canonical SQL、go:embed、backup/runtime staging、StartupFiles 和字节一致性测试均已同步。
2. Session aggregate outbox reaper 在每个事务内设置 transaction-local super-admin/RLS bypass，避免跨租户后台任务被 FORCE RLS 隐藏；claim 只处理到期的 `next_retry_at`；payload 强制要求 `session_id`、`tenant_id`、`request_id`。
3. 修正 reaper 顶部并发说明，明确 claim/replay 是两阶段事务，依靠 lease 和 aggregator 幂等完成崩溃恢复。
4. TurnReader 的 `LoadLatestOutbound`/`LoadChain` 改读 `session_bodies_unified`，覆盖 hot 表和历史分区，保证冷启动与多轮历史可见。
5. Redis format cache 去除每次命中派生不可取消 goroutine 的 fire-and-forget 回写，改为进程内串行受控更新，避免 Delete 后旧快照复活及 UseCount 丢增量。
6. `docs/db-changelog.md` 补齐 627–631 pending ledger；迁移 checksum 校验脚本将未登记历史 startup 文件保持为 warn-only，仅 mismatch/stale registry 失败。

## 闭环审计结论

- IR：`internal/ir` 使用显式 versioned request document codec；OpenAI Chat、Anthropic Messages、Responses 及 SSE 的核心 round-trip 定向测试通过。未知/原始 content 依赖统一 codec 和 session 自定义 envelope，后续仍需 persisted JSON fixture 覆盖所有 replay/export 路径。
- Session：turn、bodies、aggregate outbox 已在写入事务中原子提交；reaper lease、幂等和 RLS 上下文已补强。source session 不存在时的重建/reconciliation 仍需真实 PostgreSQL 场景确认。
- 存储：`session_bodies_hot` 与 unified view 读链路已对齐；hot 8 小时转移调度、附件文件物理删除与 DB tombstone 的 Saga 仍需 staging/生产维护窗口验证。
- 附件：DB cleanup 已有审计表和事务写入，但 preview 与 execute 的 hot/history 语义、显式 tenant scope、malformed JSON/hash 缺失防御仍列为后续 P1；本次未实现半成品物理删除 Saga。
- 供应商/并发：供应商错误聚合、备用路径和健康检查已有基础实现；ConnectionRegistry 写超时 goroutine、VACUUM FULL 多副本互斥、provider detail tab/menu parity 仍需后续专项处理。

## 验证结果

通过：

```text
(cd installer && go test ./cmd/llm-gw-installer ./internal/dbinit -count=1)
go test ./domains/session/v2 ./domains/streaming ./admin ./internal/ir ./domains/transformation ./adapter/unified ./sql/migrations/startup -count=1
go test -race ./domains/session/v2 ./domains/streaming ./admin -count=1
bash scripts/verify-migration-checksums.sh
git diff --check
```

迁移校验结果：37 条已登记迁移通过，525 条历史未登记文件仅告警。

修正前 `go test ./...` 基线曾失败于环境/时序敏感测试（Redis 删除竞态、plugin-runtime 真实进程 pid 文件）；本次核心包和 race 定向测试已通过，未将未复现的全量环境问题宣称为通过。

## 后续 handoff 优先级

1. 在隔离 PostgreSQL/多租户 FORCE RLS 环境验证 outbox reaper、627–631 upgrade/down 和 hot→columnar promote。
2. 统一附件 preview/execute/文件系统 cleanup 的 cleanup_run、tenant scope、tombstone 与 retryable failure 审计闭环。
3. 将 full 模式 delta 从集合去重升级为带顺序/计数的前缀匹配，并补重复消息、tool call ID、压缩差异测试。
4. 为 ConnectionRegistry、VACUUM FULL、provider detail tabs/menu 增加资源上限、互斥和 parity 守门测试。

## 可复制子代理提示词

### 安装器与数据库

> 只读审计 `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go` 的 startup migration canonical 源、installer embed/staging、dbinit StartupFiles、checksum ledger 和 hot+columnar promote。验证迁移版本、字节一致性、RLS、8 小时 hot retention 与 outbox replay，输出文件:行号、证据、测试命令，不修改文件。

### IR 与会话

> 只读审计 IR、OpenAI Chat/Anthropic Messages/Responses/SSE 转换、Session V2 turn/bodies/outbox、full/delta 多轮解析、RawContent、附件媒体和 replay/export 持久化闭环。重点找重复消息、未知 block、压缩快照、热表冷启动可见性问题，输出 P0–P2 文件:行号与回归用例，不修改文件。

### 供应商与可靠性

> 只读审计供应商请求错误记录、备用供应商 think/不中断模式、凭据错误详情、并发锁、连接写入超时、异步 goroutine、VACUUM/维护任务、租户隔离和前端菜单/tab parity。输出确定 bug 与待 staging 验证风险、文件:行号和测试建议，不修改文件。

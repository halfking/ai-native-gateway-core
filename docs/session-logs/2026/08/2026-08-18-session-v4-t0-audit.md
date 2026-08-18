# 2026-08-18 — 会话优化 v4 T0 审计与修正

## 需求

审计当前 M5-0/T0 WIP，修正审计发现的问题，保留其他人修改，完成本地验证后提交、合并到 `main` 并推送。未执行部署或生产写操作。

## 基线与范围

- 审计基线：`9268b656b2cda2b564219c76edb1f05219eba2f9`
- 分支：`audit/session-v4-t0`（从 `main` 创建）
- 原始 WIP：4 个 tracked 文件和 T0 文档、fixtures、contract tests
- 本轮 owner 范围：Coordinator/T0 文档、`domains/dispatch/**`、`domains/ursm/v2/**`、`pending/**`、`domains/streaming/**` durable test、`test/events/**`
- 未修改：`sql/migrations/**`、frontend、部署配置、其他未归属文件

## 审计 findings 与修正

1. T0 文档基线错误：更新为实际 `9268b656...`，注明 `b3399ad9` 为祖先。
2. URSM ready gate 顺序错误：`ready=false` 现在先于 mirror 和 tenant 校验返回。
3. 空 tenant 范围过宽：仅 authoritative filter/read path 拒绝；RecordRequest 已保持 authoritative-only。
4. identity fixture 漏 `attempt_no` 语义和完整 distinct 检查：已补。
5. vocabulary fixture 漏 `requestjourney` event types：已补并与 `AllEventTypes()` 对比。
6. restart fixture 漏 lease-live/lease-expired 条件和 duplicate lane 校验：已补。
7. ordinary dispatch 测试改为受控 in-flight forward + Stop barrier，移除主要时序 sleep 证据。
8. pending/queue mirror 未连接的 upstream counter 删除，测试范围收窄为 store/metadata projection 证据。
9. 新增 durable runtime lease/fencing restart contract：live lease 不接管，过期后一次恢复，旧 owner 写入被拒。
10. 新增 attempt ID retry uniqueness 行为测试、Manager configured prefix 回归测试和 test/events README inventory。

## 测试与证据

已通过：

```text
gofmt / git diff --check
go test ./test/events/contract ./domains/dispatch ./pending ./domains/ursm/v2/... ./domains/streaming/... -run 'Test(SessionIdentity|RestartSemantics|Vocabulary|OrdinaryDispatch|AttemptIdentity|PendingRestart|QueueMirrorRestart|FilterAndScore|RecordRequest|ManagerUsesConfigured|T0Durable)' -count=1
go test -race ./...
go vet ./...
go build ./...
./scripts/pre-commit-check.sh
```

`go test ./... -count=1 -timeout=300s` 首次仅在 `plugin-runtime/TestExecCommand_StartsRealProcess` 因 helper process 未生成 pid 文件失败；该测试单独以 `-count=1 -v` 重跑后通过，随后全仓重跑通过。严格全仓 secret scan 会扫描既有 `.env.local`、示例 env 和 `.worktrees` 历史资产而失败；对本轮 21 个提交路径执行 scoped strict scan，结果 CLEAN。`make lint-tenant-scope-llmgw`、`lint-otel-tenant`、`lint-pg-rls` 在当前 Makefile 中不存在，因此不可执行。

## 现状裁决

`M5-0/T0 = BLOCKED / NO-GO`。delimiter-safe canonical Redis key 仍未实现；`NodeKeyForTenant`、`WindowKeyForTenant`、`CandidateIndexKey` 对包含 `:` 的 tenant/model 仍不能证明无碰撞。修复必须先有 dual-read、切换窗口、TTL/清理、回滚和唯一 migration owner 评估，本轮没有静默切换 key 格式。

## Owner 结论

- T0 fixture：本地契约 fixture 与 validator 可合并，真实依赖未验证。
- URSM：ready/empty-tenant/prefix 行为已修正并通过 focused tests；delimiter blocker 未关闭。
- Dispatch/pending/durable：restart contract tests 已补；pending/queue mirror 仅证明 projection/read semantics。
- Migration owner：本轮无 migration 修改；delimiter canonicalization 需求待后续独立 workstream。
- Release：不可发布，不进入 G1/G2/G3 生产接线。

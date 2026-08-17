# 2026-07-25 — node_probe_failed 不再黏住已恢复凭据

## 摘要
- 背景：`v_routable_credential_models.is_routable` 通过 `migration 417` 把 `nps.last_direct_ok = FALSE AND nps.next_retry_at > now()` 直接判为 `node_probe_failed`，导致一次失败的主动探测会让 binding 在下一次直接轮询成功前都不可路由。
- 修复：`bg/node_probe.go runOne` 成功分支补齐 `last_direct_ok=TRUE / last_err_code=NULL`，与 `updateBindingAvailability` 协同 `provider.InvalidateCandidateCacheForCredential` 与 `pg_notify('auto_route_refresh', 'credentials:UPDATE:<id>')`；`handleNodeProbeStateReset` 紧急按钮保持只清不探。
- 规格：`docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md` §3
- 计划：`docs/superpowers/plans/2026-07-25-node-probe-realtime-recovery.md`

## 验证
- `go test ./bg/ -run 'TestRunOne|TestDirectProbe|TestNodeProbe|TestIsMissing|TestHandleNodeProbeStateReset'`
- AC2（人工 5xx → runOne 成功）：自动路由刷新 ≤5s 内 `v_routable_credential_models.is_routable` 翻 `true`。

## 回滚
- 若 AC2 不达预期，临时把 `pg_notify` 行注释掉，仅保留 success UPDATE 中的 `last_direct_ok=TRUE` 单项（仍能消除 `node_probe_failed` 标签，但绑定 5 轮共识外的下一次轮询才会真正恢复）。

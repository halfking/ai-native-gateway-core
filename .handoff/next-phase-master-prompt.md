# 主代理 + 子代理 任务执行提示词 — 配额恢复链路第二阶段

> 与 `.handoff/selfcheck-audit-2026-08-26.md` 配套使用。
> 主代理复制本文件全文到新会话启动即可，下游 4 个子代理按本文 §3-§6 的模板派生。

---

## 1. 任务范围

工作目录：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`
分支：`main`（干净，本会话已确认 `git status` 无未提交变更，HEAD=`5be62a436`）
上下文文档：**先读 `.handoff/selfcheck-audit-2026-08-26.md` 全文**（§1-§8，特别是 §2.2 已固化修复 + §7 第二阶段上下文）

## 2. 强约束（来自 `.handoff/selfcheck-audit-2026-08-26.md` §7.5）

- ❌ **不存在**的标识符（不要照抄之前方案里的名字）：
  `SetQuotaUpdatedNotifier` / `EnqueueWithRecovery` / `quotaUpdatedNotifier` /
  `OnQuotaRecovered` / `MarkCredentialHealthy` / `MarkCredentialExhausted` /
  `IsExhaustedBy` / `RecoveryService` / `NewRecoveryService` / `CredentialInvalidator`
- ✅ 正确名字：`CredentialRecovery`（单数）/ `NewCredentialRecovery(db *pgxpool.Pool)` /
  `SetProbeSubmitter(fn func(credID int, model string))` /
  `SetInvalidateCandidateCache(fn func(credID int))` /
  `provider.InvalidateCandidateCacheForCredential(credentialID int)` /
  `credProbeV2.SubmitFastProbe` / `credProbeV2.ProbeNowAsync` /
  `credProbeV2.writeHealth` / `bg/credential_probe_v2.go::restoreAllBindingsOnCredentialSuccess`
- `writeHealth` 是 `cmb.available` 的唯一权威写者；recover() 不能直接 UPDATE cmb.available
- 新 webhook 必须 HMAC 验签（参考 `cmd/gateway/feishubot_init.go`）
- metric 前缀 `llmgw_`；测试用 `pgxmock/v4`
- 不要触碰 `bg/credential_probe_v2.go` 已被 §2.2 列出的核心修复
- 不要删除 `bg/credential_probe_v2_recharge_recovery_test.go` 的 pin 测试

## 3. 4 个落点（按 ROI 排序）

| 优先级 | 落点 | 一句话描述 |
|---|---|---|
| **P0** | A | 探测成功后**主动**通知调度层，消除 "DB 写完了但 cache 还在 stale" 窗口 |
| **P0** | B | 新增 `/api/webhooks/quota/recharged` 路由，充值回调秒级响应 |
| **P1** | C | 缩短 `fastReprobeDelay`（5min → 30s）+ 给 PeriodicQuotaProbe 加"窗口已重置"事件驱动分支 |
| **P1** | D | `recover()` 命中后调 `ProbeNowAsync` 旁路，不等 fastReprobeDelay |

## 4. 执行模式

你是主代理。**不要自己直接修改任何 .go 文件**——每个落点派一个独立 `general-purpose` 子代理（`subagent_type="general-purpose"`）去执行，按下面两个**并行批次**启动：

```
并行批次 1（无依赖，可同时跑）：
  • 子代理 1 — 落点 A
  • 子代理 2 — 落点 B
并行批次 2（依赖批次 1 落点 A 的 SetOnQuotaRecovered 接口）：
  • 子代理 3 — 落点 C
  • 子代理 4 — 落点 D
```

### 4.1 每个子代理的 SOP

1. **开工前**：跑 `git rev-parse HEAD` + `git status` 确认 working tree 干净
2. **开工前**：跑 `docs/handoff/2026-08-26-quota-recovery-selfcheck-audit.md §7` 的 grep 验证命令（如 §10）
3. **开工前**：读 `.handoff/selfcheck-audit-2026-08-26.md` 全文（确认基础桩都在）
4. **实现**：严格遵循自己负责的落点的代码坐标（§7.3），不动其它落点的文件
5. **自检**：写完后跑 `go build ./...` 和 `go vet ./...`，确认无编译错误
6. **自检**：跑自己新增的测试（`go test ./bg/ -run 'TestXXX' -v`）+ 受影响包测试
7. **不要 commit**，把 `git diff --stat` + 测试结果 + 自检结论**返回给主代理**
8. **如果冲突**（同一文件多人改）：停下来报告给主代理，不要自动 merge

### 4.2 落点 A 子代理提示词

```
你是子代理，负责落点 A（探测成功 → 调度层通知）。

任务详情：
  • 在 bg/credential_recovery.go 的 CredentialRecovery struct 加一个新字段：
    `onQuotaRecovered func(credID int, source string)` 和公开方法 `SetOnQuotaRecovered(fn)`
  • 在 metrics/routing_credential_metrics.go 新增：
    `RoutingCredentialQuotaRecoveredNotifyTotal{source}` Counter
  • 在 bg/credential_probe_v2.go::cycleAll 成功路径（writeHealth 之后，
    quota_state/availability_state 翻成 healthy/ready 时）调 onQuotaRecovered
  • 在 bg/probe_queue_worker.go::processTask 成功分支（同上判定）调 onQuotaRecovered
  • 在 cmd/gateway/main.go:3503 附近装 SetOnQuotaRecovered：
    func(credID int, source string) {
      provider.InvalidateCandidateCacheForCredential(credID)
      met.RoutingCredentialQuotaRecoveredNotifyTotal.WithLabelValues(source).Inc()
    }
  • 测试：新增 bg/credential_recovery_test.go 用例 TestOnQuotaRecovered_* +
    新建 bg/probe_queue_worker_test.go 覆盖 processTask 成功路径

参考坐标：.handoff/selfcheck-audit-2026-08-26.md §7.3（特别是
bg/credential_recovery.go:87-129, bg/probe_queue_worker.go:142-203,
bg/credential_probe_v2.go:380-462, cmd/gateway/main.go:3503-3514）。

完成后返回：
  • git diff --stat 摘要
  • go build + go vet 结果
  • go test 结果
  • 一句话总结
```

### 4.3 落点 B 子代理提示词

```
你是子代理，负责落点 B（充值回调 webhook）。

任务详情：
  • 新建 cmd/gateway/webhooks/quota_recharged.go：
    - POST /api/webhooks/quota/recharged
    - HMAC-SHA256 验签（header X-LLM-Gateway-Signature，body 整体签名）
    - secret 从 settings_kv / LLM_GATEWAY_QUOTA_WEBHOOK_SECRET env 读
    - body: {credential_id:int, vendor:string, event_type:"recharge.completed"|"window.reset", ts:int}
    - 验证通过后调 balanceQuotaProbe.OnQuotaRecharged(credID, source)
    - 200 OK {"status":"accepted"} 后异步处理
  • 修改 bg/balance_quota_probe.go：
    - struct 加 onQuotaRecharged 字段
    - 加 SetOnQuotaRecharged(fn func(credID int, source string))
    - 加公开方法 OnQuotaRecharged(credID, source)：调 probeNowAsync + InvalidateCache
  • 在 cmd/gateway/main.go 装配路由（参考 feishubot_init.go:106 的注册方式）：
    - mux.HandleFunc("POST /api/webhooks/quota/recharged", wrap(handler))
    - balanceQuotaProbe.SetOnQuotaRecharged(...)
  • 更新 cmd/gateway/capabilities.go:61 的 "webhook_subscription": "planned" → "implemented"
  • 测试：新建 cmd/gateway/webhooks/quota_recharged_test.go

参考坐标：admin/credential_state_handlers.go:174-232 已有 admin force 参考实现；
cmd/gateway/feishubot_init.go HMAC 验签参考。

完成后返回：
  • git diff --stat 摘要
  • go build + go vet 结果
  • go test 结果
  • 一句话总结
```

### 4.4 落点 C 子代理提示词

```
你是子代理，负责落点 C（探测频率 + 周期事件驱动）。

任务详情：
  • 修改 bg/credential_probe_v2.go：
    - fastDelay 默认值 5*time.Minute → 30*time.Second（line 71）
    - LLM_GATEWAY_FAST_REPROBE_DELAY env 默认值同步调整（line ~75-85）
    - 新增 metric `RoutingFastReprobeDelaySeconds` Gauge
  • 修改 bg/periodic_quota_probe.go：
    - 新增方法 probeWindowReset()：扫 quota_state='periodic_exhausted' AND
      quota_recover_at IS NULL AND (exhausted_at + max_window_hours) < now()
      （这些是 inferQuotaRecoverAt fallback 到 midnight 但窗口实际早就重置了的）
    - 在 tick() 里调 probeWindowReset()，与 probePeriodicExhausted 并行
  • 测试：扩 bg/periodic_quota_probe_test.go

参考坐标：bg/credential_probe_v2.go:71/91, bg/periodic_quota_probe.go:135-152/228-277。

完成后返回：
  • git diff --stat 摘要
  • go build + go vet 结果
  • go test 结果
  • 一句话总结
```

### 4.5 落点 D 子代理提示词

```
你是子代理，负责落点 D（recover() 命中后立即探活）。

任务详情：
  • 修改 bg/credential_recovery.go：
    - struct 加 probeSubmitterImmediate func(credID int) 字段
    - 加 SetProbeSubmitterImmediate(fn func(credID int))
    - 在 dispatchRecoveryHooks 闭包里（line 272-317），如果 sqlKind 是
      "quota_periodic_recover" 或 "availability_recover" 且 affected > 0，
      末尾对每个 seen id 调 probeSubmitterImmediate(id) — 但用 sync.Once 风格
      去重（同 tick 同一个 id 只调一次）
  • 修改 cmd/gateway/main.go：装 SetProbeSubmitterImmediate(credProbeV2.ProbeNowAsync)
  • 测试：扩 bg/credential_recovery_test.go

参考坐标：bg/credential_recovery.go:87-129, 272-317, 409-575;
cmd/gateway/main.go:3503-3514。

完成后返回：
  • git diff --stat 摘要
  • go build + go vet 结果
  • go test 结果
  • 一句话总结
```

## 5. 主代理 SOP

1. 读 `.handoff/selfcheck-audit-2026-08-26.md` 全文（5 分钟）
2. **批次 1**：并行启动子代理 1（A）+ 子代理 2（B），传递对应模板
3. 等待两个子代理返回结果
4. **批次 2**：等批次 1 完成后（因为 D 依赖 A 的接口），并行启动子代理 3（C）+ 子代理 4（D）
5. 等待所有 4 个子代理完成
6. **整合**：
   - 跑 `git diff --stat` 看总体变更量
   - 跑 `go build ./...` 确认 4 个改动不冲突
   - 跑 `go test ./bg/ ./cmd/gateway/... ./admin/...` 全量回归
   - 如有冲突（同一行被多人改）：手动 rebase / merge
7. **汇报**：把 4 个子代理的结果汇总成一页纸，包含：
   - 每个落点的 diff 大小（行数）
   - 每个落点的测试结果
   - 整合后的总测试结果
   - 任何遗留的 follow-up

## 6. 关键决策原则

- **冲突规避**：4 个落点应改的文件互不重叠（A: credential_recovery.go + credential_probe_v2.go +
  probe_queue_worker.go + main.go；B: 新文件 + balance_quota_probe.go + main.go；
  C: credential_probe_v2.go + periodic_quota_probe.go；D: credential_recovery.go + main.go）。
  注意 A 和 D 都改 credential_recovery.go，A 和 C 都改 credential_probe_v2.go，
  A 和 B 都改 main.go。**批次划分就是为了错开这些冲突**——A 先落地，D 等 A 完。

- **如遇时间不够**：保留 P0（落点 A + B）的全部产出，P1（C + D）允许部分完成。
  但 P0 的代码必须自检通过 + 测试通过。

- **不 commit**：4 个子代理都不要 commit，主代理最后统一汇报、不自动 commit。
  由用户决定提交策略。

- **遇到 grep 0 命中的标识符**：立刻停下来报告，不要自创 API 名。

## 7. 启动前硬性验证（必跑）

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4
git rev-parse HEAD           # 确认 5be62a436
git status                   # 确认 clean（除 handoff 文档外）
grep -n "SetProbeSubmitter\|SetInvalidateCandidateCache\|SetProbeNowAsync" cmd/gateway/main.go
grep -n "dispatchRecoveryHooks" bg/credential_recovery.go
grep -rn "mux.Handle.*webhook\|HandleFunc.*webhook" cmd/gateway/   # 应只有 feishu/dingtalk
go build ./...               # 必须成功
go test ./bg/ -count=1       # 必须全绿（基线）
```

## 8. 开始执行

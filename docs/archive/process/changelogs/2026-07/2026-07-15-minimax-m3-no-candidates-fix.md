# 2026-07-15 — llm.kxpms.cn minimax-m3 不可用 (no_candidates 雪崩)

**发布日期**: 2026-07-15
**影响范围**: llm.kxpms.cn (生产, 154) — minimax-m3 路由
**严重等级**: P0 — 用户感知功能完全不可用
**修复版本**: 2.4.6 / build_seq 1033
**修复提交**: (pending)

---

## 1. 用户报告

> llm.kxpms.cn 中的 minimax-m3 现在无法使用，但直连供应商可用。

直连供应商含义：直接通过 `https://api.minimaxi.com/v1/chat/completions`（MiniMax 官方 OpenAI 兼容端点）调用 minimax-m3 是 OK 的；只有经过 154 网关路由的请求失败。

## 2. 154 journalctl 关键证据

- `relay: dropping empty choices block` / `upstream EOF without [DONE]`：MiniMax 直连凭据 21 的流式响应普遍不带 `[DONE]` sentinel，但响应内容已经成功返回给客户端。
- `executor: transient error, trying next candidate, kind=timeout, credential_id=19, provider_id=18, err="[timeout] Post \"https://integrate.api.nvidia.com/v1/chat/completions\""`：NVIDIA 代理凭据 19/23 全 timeout。
- `credential binding marked degraded due to continuous failures, failure_rate=1, sample_size=8, error_kinds={"concurrent":1,"timeout":7}`：checker 把凭据 21/19/23 都标 15 分钟冷却。
- `all 1 candidates failed: circuit open for credential 23` / `all 2 candidates failed`：路由层无候选可用。

## 3. 数据库 252 调查

```sql
SELECT cmb.id, cmb.credential_id, pm.raw_model_name, cmb.available,
       cmb.unavailable_reason, cmb.unavailable_at, cmb.unavailable_recover_at
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.standardized_name = 'minimax-m3';
```

返回结果显示：
- cred 21 (直连 minimaxi.com)：`unavailable_recover_at=11:38:29` — 被 cooldown
- cred 19/23 (NVIDIA 代理)：`unavailable_recover_at=11:50:34` / `11:51:06` — 被 cooldown
- cred 14/15 (直连 minimaxi.com 可用)：available=t — 路由却没选到

```sql
\d model_offers
```

返回 VIEW 视图，**没有 `unavailable_recover_at` 列**（只有 `unavailable_at`）。底层表 `credential_model_bindings` 有该列。

## 4. 根因分析

### 4.1 eof_without_done 被误分类为 cred 失败（核心 bug）

`relay/stream.go` 检测到 MiniMax 流以 EOF 收尾（没发 `[DONE]`），标记 `outcome.Reason = "eof_without_done"`：

```go
// domains/streaming/stream.go:313-320
if !upstreamDoneReceived {
    slog.Warn("upstream EOF without [DONE]", "client_model", clientModel)
    if capture != nil {
        capture.MarkInterruptedWithReason("eof_without_done")
    }
    outcome.Interrupted = true
    outcome.Reason = "eof_without_done"
}
```

`executor_chat.go` 把 `eof_without_done` 分类为 `KindStreamTimeout`（默认 kind）：

```go
// domains/streaming/executors/executor_chat.go:652
streamKind := errorsx.KindStreamTimeout
```

但 `executor_chat.go` 的 `isBenignEOF` 分支对带 chunks 的流标记为 success 并返回（`RecordSuccess`）—— 这一层是正确的。

**真正错误在 `credentialhealth/checker.go`** —— 当 `error_kind=eof_without_done` 时被算作 credential 失败：

```go
// 修复前
for _, e := range entries {
    if e.ErrorKind == "network" || errorsx.IsClientBug(errorsx.ErrorKind(e.ErrorKind)) {
        continue
    }
    total++
    if !e.Success {
        failed++   // ← eof_without_done 进到这里算失败
        ...
    }
}
```

`IsClientBug` 覆盖的 kind 只有 `KindToolCallIdMismatch, KindUnsupportedFeature, KindCanceled` —— `KindStreamTimeout`（包括 `eof_without_done`）不在白名单内。

**结果**：MiniMax 供应商常态性地省略 `[DONE]` sentinel，10 次请求 8 次会被标记 stream_timeout（哪怕 audit log 显示 `success=true, stream_chunks=20`），checker 累积到阈值 → `markDegraded` → 15 分钟 cooldown → 路由层无候选 → `no_candidates`。

### 4.2 model_offers 镜像写 SQLSTATE 42703（次要 bug）

`checker.go markDegraded` 同步写 model_offers 视图，SQL 引用 `unavailable_recover_at` 列：

```sql
UPDATE model_offers mo
SET available              = FALSE,
    unavailable_reason     = 'continuous_failure',
    unavailable_at         = now(),
    unavailable_recover_at = $3  -- ← 视图无此列
FROM provider_models pm
...
```

每次 cooldown 都抛 `ERROR: column "unavailable_recover_at" of relation "model_offers" does not exist (SQLSTATE 42703)`。这个错误只影响镜像（/api/routing/resolve 的 admin UI），不影响 production routing（cmb 表写入成功），但污染 journald。

### 4.3 路由候选耗尽（症状）

`docs/2026-07-13-afternoon-no-candidates-and-storage-cleanup.md:70` 早已记录同样症状：

> 连续 3 次 `transient` 失败（Minimax 流式连接易触发 `eof_without_done`）→ 进入 5 分钟 cooling → 路由层看到所有候选被 StateManager 拒绝 → `no_candidates`。

但**上次修复只解决了日志可观测性**（router reason），**没修真正的 eof_without_done 误判根因**。本次事件是该已知缺陷的再次爆发，15min cooldown × 3 个凭据 = 整条 minimax-m3 路由塌方。

## 5. 修复方案

### 5.1 P0 修复：eof_without_done 不再算作 credential 失败

`credentialhealth/checker.go`：在 `IsClientBug` 旁加显式 exclude（与 `network` 同级）：

```go
if e.ErrorKind == "network" ||
    e.ErrorKind == "eof_without_done" ||  // 2026-07-15 P0: 供应商协议 quirk
    errorsx.IsClientBug(errorsx.ErrorKind(e.ErrorKind)) {
    continue
}
```

理由：stream.go 已经把 eof_without_done + chunks>0 标记为 success，executor 的 `isBenignEOF` 分支也 RecordSuccess 计入 circuit breaker —— 失败统计口径应该与这些保持一致。选 exclude 而不是新增 `KindBenignEOF` + `IsClientBug` 的原因是后者需要触碰 errorsx 包和所有 IsClientBug 调用方，影响面更广；本次 fix 严格只动 checker 的聚合逻辑。

### 5.2 P1 修复：model_offers 镜像写不再引用不存在的列

`credentialhealth/checker.go markDegraded`：

```go
// 修复后（视图无 unavailable_recover_at 列；该列已在 cmb 行上设置，路由层不依赖视图）
UPDATE model_offers mo
SET available          = FALSE,
    unavailable_reason = 'continuous_failure',
    unavailable_at     = now()
FROM provider_models pm
...
```

注：`RecoverExpired` 镜像写已经不带 `unavailable_recover_at`（线 250-253），本次只补 `markDegraded` 路径的对称修复。

### 5.3 154 紧急热修 SQL

修复部署前先手动清掉已被误判冷却的 minimax-m3 凭据，恢复用户使用：

```sql
UPDATE credential_model_bindings
SET available              = TRUE,
    unavailable_reason     = NULL,
    unavailable_at         = NULL,
    unavailable_recover_at = NULL,
    consecutive_failures   = 0,
    updated_at             = now()
WHERE provider_model_id IN (SELECT id FROM provider_models WHERE canonical_id = 5704);

UPDATE credentials
SET availability_state      = 'ready',
    availability_recover_at = NULL,
    state_updated_at        = now()
WHERE id IN (11, 12, 14, 15, 19, 21, 23);
```

11:42 操作，11:44 首次出现 minimax-m3 success=true，11:47 后稳定工作。

## 6. 测试覆盖

`credentialhealth/checker_test.go` 新增 2 个 regression test + 修 2 个老测试：

- `TestChecker_CheckAndUpdate_ExcludeBenignEOF`：10 次 `eof_without_done` 失败不应触发 markDegraded（fake DB 不应收到任何 UPDATE）。
- `TestChecker_CheckAndUpdate_MixedEOFStillFlagsTrueFailures`：4 个 eof_without_done + 6 个 quota 失败，6/6 = 100% 仍 > 80% 阈值 → 应触发 1 个 cmb UPDATE + 1 个 model_offers 镜像 UPDATE。
- `TestChecker_CheckAndUpdate_AboveThreshold`：补 `model_offers` 镜像期望（之前漏 mock）。
- 全部用 `pgxmock.QueryMatcherOption(QueryMatcherRegexp)` 匹配多行 SQL。

`go test -count=1 ./...` 全过（最后一次跑包括 admin / api / apihub / bg / cache / catalog / cmd / db / domains / etc.，无 FAIL）。

## 7. 验证

| 项 | 修复前 | 修复后 |
|---|---|---|
| minimax-m3 路由 | `all candidates failed: circuit open / no_candidates` | 11:44 / 11:47 / 11:48 多次 `success=true, stream_chunks>0` |
| journald `credential binding marked degraded` | 每 1-2 分钟一次（基于 NVIDIA timeout 累计） | 部署后 0 次（等真实流量再次触发以最终验证） |
| journald `checker: model_offers mirror write failed` | 每次冷却都报 SQLSTATE 42703 | 部署后 0 次 |
| 直连供应商 `api.minimaxi.com` | 健康（curl 200） | 健康（无变化） |
| 154 journalctl `unavailable_recover_at schema ensured` | 启动时跑（与视图无关） | 启动时跑（与视图无关） |

## 8. 部署记录

- 154 部署版本：`v1029.linux.amd64` (含修复)
- systemd 单元：`llm-gateway-go.service`
- 启动 PID：9253（替换原 3471）
- deploy-154-quick.sh 已知 bug：phase 4 的 `${REMOTE_BIN}` 是本地变量，远程 shell 看不到，导致 `ln -sf ${REMOTE_BIN}` 创建 broken symlink（deploy 中断在 `ln -sf` 之后但 `systemctl restart` 之前）。本次手动修正 symlink + `systemctl restart` 完成重启。**待跟进**：修 deploy-154-quick.sh 把 REMOTE_BIN 通过 stdin 传入或重写脚本用 `scp + ssh` 模式。

## 9. 遗留与风险

- **症状缓解未根除**：本次只修了"eof_without_done 不算失败"和"视图镜像写不带不存在列"。文档 `docs/2026-07-13-afternoon-no-candidates-and-storage-cleanup.md` 提出的**指数退避冷却**（3 次失败 5min、6 次失败 30min）和**provider_error vs model_error 区分**仍未实现，理论上类似的 cascade failure 仍可能在新供应商上重现。
- **NVIDIA 路径仍是黑洞**：`integrate.api.nvidia.com/v1/chat/completions` 持续 timeout 121s（net/http 头超时），属于供应商侧问题。本次修复后，路由仍可能把请求打到 NVIDIA 凭据再失败降级到 minimaxi.com —— 用户感受上是慢，但不再是"完全不可用"。建议后续给 NVIDIA 凭据 19/23 也加 manual 禁用或更激进的 active_probe。
- **admin API key 路径**：`/v1/chat/completions` 报 `key verification RPC failed: invalid api key: data plane requires sk-* prefix`，说明 user JWT 不能直接用于 data plane。需要用 admin panel 登录 web UI 后从 cookie 调 chat，或创建专用 sk-* key 集成。**不影响生产用户**（web UI 走 cookie），但集成测试会受影响。
- **deploy-154-quick.sh 的 REMOTE_BIN 变量未传远程 shell**（见 §8），下次部署需手动修 symlink 或先修脚本。

## 10. 下一步建议

1. **修复 deploy-154-quick.sh**：把 REMOTE_BIN 改成 `ssh ... bash -c "... ${REMOTE_BIN} ..."` 模式或通过 `scp` 上传后用 `ssh` 直接赋值。
2. **NVIDIA 凭据隔离**：在 admin UI 把 cred 19/23 设为 `manual_disable` 或加 `manual_priority=-1`，阻止路由把请求打过去。
3. **executor 路径优化**：`executor_chat.go` 默认 `streamKind = KindStreamTimeout`，是历史兜底。是否值得引入 `KindBenignEOF` 让 checker / metrics 更精确区分 — 待 v2.4.7 评估。
4. **rule 11 §6 browser-use 强制实测**：本次是 backend 修复，未触发 browser-use；但应在 web UI 内做一次 minimax-m3 端到端验证（login → models → chat → 看 SSE 收到完整 chunks）。

## 11. 相关文件

### 修改
- `credentialhealth/checker.go` — CheckAndUpdate 跳过 eof_without_done（+24 行注释）；markDegraded 镜像写不写 unavailable_recover_at（+15 行注释）
- `credentialhealth/checker_test.go` — 4 个测试更新（AboveThreshold 加 mirror 期望；新增 ExcludeBenignEOF + MixedEOFStillFlagsTrueFailures）

### 数据库 (生产 252, 154 via ssh)
- `credential_model_bindings` UPDATE 清掉 minimax-m3 7 行的 unavailable 状态
- `credentials` UPDATE 7 行 availability_state='ready'

### 部署
- `/opt/llm-gateway-go/llm-gateway-go.v1029.linux.amd64` 上传 57.6MB
- `/opt/llm-gateway-go/llm-gateway-go` symlink → v1029

### 文档引用
- `docs/2026-07-13-afternoon-no-candidates-and-storage-cleanup.md` — 上次同类事件，但只修观测未修根因
- `docs/格式转换/08-MiniMax-M3-审计与统一标准.md:96-97` — 早指出 "eof_without_done 应按供应商成功但异常终止记录"
- `docs/路由优化v3/14-故障数据实测报告.md:206-208` — H.9 统计 1001 次 eof_without_done 集中爆发

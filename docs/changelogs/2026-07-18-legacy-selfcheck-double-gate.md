# 2026-07-18 — Legacy Self-Check Double Gate

## 背景

线上观察到 GPT-5.4 每分钟产生周期性请求（约 30 runs/h，95 rounds/h，383k tokens/h），请求归属为 `self-check-worker / system key`。

审计确认：
- 旧版 `bg.NewSelfCheckWorker` 在生产环境实际启用，每分钟唤醒，对 3 个 featured 模型执行 1 个 ping + 3 轮工具调用。
- 代码层面该 worker 仅受 `LLM_GATEWAY_USE_NEW_PROBE_MODE` 单开关控制；文档明确要求默认关闭，但未提供独立显式开关。

**根因**：缺少独立显式开关，回滚总开关时会意外重新启用付费定时请求。

## 修复内容

### 代码层（防护）

1. **增加双重门禁**（`cmd/gateway/main.go:1720-1738`）：
   - Gate 1：`LLM_GATEWAY_USE_NEW_PROBE_MODE` 必须为 `false`（回滚模式）
   - Gate 2：`LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK` 必须显式为 `"true"`（新增）
   - 只有两个门禁**同时满足**时，legacy worker 才会启动。

2. **回归测试**（`cmd/gateway/legacy_selfcheck_gate_test.go`）：
   - 9 个场景覆盖所有组合（默认/rollback × 无opt-in/有opt-in/大小写/空格）
   - 验证双重门禁逻辑正确

### 线上层（止血）

1. **立即关闭自检**：
   - `PUT /api/self-check/settings/update {"enabled":false}`
   - 生效时间：`2026-07-18 02:24:41`
   - 验证：最后运行 `02:23:51`，等待 90 秒后无新记录

## 验证结果

- ✅ **新增测试全通过**：`TestLegacySelfcheckGate` 9/9 pass
- ✅ **构建通过**：`go build ./...` 无错误
- ✅ **相关测试通过**：`go test ./cmd/gateway ./bg` 无相关失败
- ✅ **线上自检已停止**：`enabled=false`，无新 run 记录

## 影响范围

- **不影响**新版 `bg/credential_selfcheck.go`（每日一次，5 分钟周期选 1 个凭据）
- **不影响**新版三探针（NodeProbe / ActiveProbe / SystemHealth）
- **仅影响**旧版 featured-model 自检（1 分钟 × 3 模型）

## 部署建议

1. **默认配置**（推荐）：
   - 不设置 `LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK`（默认不启动）
   - 保持 `LLM_GATEWAY_USE_NEW_PROBE_MODE=true`（默认）

2. **紧急回滚**（仅在新探针完全失效时）：
   ```bash
   export LLM_GATEWAY_USE_NEW_PROBE_MODE=false
   export LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK=true
   ```
   回滚后立即人工监控 token 消耗，问题解决后立即恢复默认配置。

3. **永久下线**（后续计划）：
   - 待新探针稳定运行 1 个月后，考虑从代码中完全移除 legacy worker。

## 相关文件

- `cmd/gateway/main.go` — 双重门禁逻辑
- `cmd/gateway/legacy_selfcheck_gate_test.go` — 回归测试
- `bg/self_check_worker.go` — 旧版 worker 实现（未修改）
- `admin/self_check_handlers.go` — settings API（未修改）
- `docs/自检功能/02-specification.md` — 文档规范（未修改）

## 遗留问题

- `max_tokens_per_run=200` 只限制输出，不限制输入（GPT-5.4 每轮输入 ≈ 4.4k tokens）
- settings 表中的 `enabled` 字段仅控制 worker 行为，不控制启动本身（已通过代码层双重门禁解决）

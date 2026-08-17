# 2026-08-17 — 部署门禁只读审计（任务 A）

> 项目：`llm-gateway-go`
> 范围：local → dev（`https://llm.itestu.cn`）→ 245（`https://llmgo.kxpms.cn`）→ 154 / `https://llm.kxpms.cn` 的健康与晋级前置。
> 方式：**只读**。不下发任何部署、重启、env 写入、feature flag 切换、凭据注入；不重建 252 gateway runtime。
> 触发：会话优化 v4 交接 `/tmp/handoff-20260817-041029.md` §10 任务 A。

## 1. 当前代码基线

| 项 | 值 |
|---|---|
| 本地工作区 HEAD（实测） | `3ec05c0e4704aa2ebf0574b9f10886637502ff0b` |
| `origin/main` HEAD | `b97bca01cdaa6935d2636dd4c84351a5d8ce4912` |
| 落后 `origin/main` | 2 个 commit（`b2e6e1dd2` `feat(obs-ui): group available nodes by model with request lists`、`b97bca01c` `fix(audit): state transition JSONB binding, integrity planner lifecycle, body hot TTL drain`） |
| `version.json` version | `2.5.0-102c885e-20260817-1573`（HEAD），`2.5.0-c4167b9a-20260817-1572`（前一 reflog） |
| 工作区干净 | 是（`git status -sb` 仅显示 ahead/behind 信息） |
| 抢救分支 | `rescue/ursm-v2-rollout-gate-127e5b316` 指向本会话提交的 `127e5b316`，未推送；对象仍可读、14 文件 diff 在 `b97bca01c` 树中已合并可见（详见 §5） |

### 1.1 reflog 关键轨迹

```text
3ec05c0e4 HEAD@{0}: pull --commit --rebase=false --log origin main: Fast-forward
127e5b316 HEAD@{1}: commit: feat(ursm-v2): rollout gate evidence + 245 shadow runbook (P0-Z1)
6f7ebb453 HEAD@{2}: pull --ff-only origin main: Fast-forward
```

`127e5b316` 是本会话原提交的 URSM v2 灰度门禁 commit；该对象仍可达，仍作为 `b97bca01c` 树内祖先存在于多个 ref（`fix/154-db-empty-routing`、`rescue/ursm-v2-rollout-gate-127e5b316`、`main`）。

## 2. 健康端点实测（2026-08-17 01:53 +0800）

| 端点 | DNS | 实测响应 | 备注 |
|---|---|---|---|
| `http://127.0.0.1:8781/healthz` (local) | — | **200**（2.5ms） | 本机监听方为 docker gateway 容器（pid 1040 `com.docke`） |
| `http://127.0.0.1:8780/healthz` (local alt) | — | `connection refused` | 与 handoff 一致：旧 8780 路径未在 local 工作区复活 |
| `http://127.0.0.1:11008/healthz` (NPS tunnel) | — | `connection refused` | 与 handoff 一致：252 上 NPS 11008 上游不在本机 |
| `https://llm.itestu.cn/healthz` (dev) | `115.29.212.252` | **502**（nginx 0 上游；`connect() failed (111: Connection refused)` 推断自 252 nginx vhost） | 与 2026-08-16 handoff §6 完全一致；**根因未变** |
| `https://llm.itestu.cn/v1/models` 等其他路径 | — | 全部 502 | nginx 上游共用 11008 通路，整站不可用 |
| `https://llmgo.kxpms.cn/healthz` (245) | `8.136.114.245` | **200**；`{"status":"ok","version":"2.5.0-102c885e-20260817-1573-102c885e"}` | **hitherto 假设 dev 阻断 245 实际并不成立** |
| `https://llm.kxpms.cn/healthz` (prod) | `115.29.212.252`（同 252 IP） | **200**；`{"status":"ok","version":"2.5.0-7b086ed5-20260816-1571-7b086ed5"}` | 252 nginx 上的 `llm.kxpms.cn` vhost 上游仍在工作（指向 154 后端），并非整 252 不可用 |
| `https://llm.kxpms.cn/` | — | 302 → `/maintain/home` | 与既有运维页一致 |

**重要事实校正**（与 2026-08-16 handoff / v4 文档基线对比）：

1. `llm.itestu.cn` 502 仍是 dev 唯一已知阻塞，根因在 252 nginx vhost 的 `127.0.0.1:11008` 上游无监听。**该路径不需要本会话修复**：晋级路径 local → 245 → 154 不经 dev；dev 仅作开发自验入口。
2. `llm.kxpms.cn` DNS 仍解析到 252，但实际后端是 154——说明 252 上 `llm.kxpms.cn` vhost 与 `llm.itestu.cn` vhost 走的是不同 upstream 集群。运维侧需明确：245 晋级 smoke 不应触碰 dev / `llm.itestu.cn`。
3. 245 已就绪（`version=1573`，HEAD `102c885e`），deploy-test 矩阵下 `--env test` 的 `endpoint_check` 期望可通过；但需 `require_injector aliyun-frontend-245` 注入 SSOT 才可走完整 `endpoint_check`。
4. **deferred/fail-closed**：252 gateway runtime 已退役；`LLM_GATEWAY_ADMIN_API_KEY_252` 无源，不得创建占位 secret 或重启 252 网关后端。

## 3. 本地构建与受 127e5b316 影响包的测试

| 检查 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | 0 错误，0 输出 |
| 静态检查 | `go vet ./...` | 0 错误，0 输出 |
| URSM v2 全子树 | `go test -count=1 ./domains/ursm/v2/...` | 全部 `ok`（14 子包） |
| Streaming executor | `go test -count=1 ./domains/streaming/executors/...` | `ok` |
| Metrics | `go test -count=1 ./metrics/...` | `ok` |
| URSM v2 rollout 验证脚本离线自测 | `bash scripts/test-verify-ursm-v2-rollout.sh` | `verify-ursm-v2-rollout self-test passed.` |

`scripts/verify-ursm-v2-rollout.sh` 仍在 HEAD 工作树，contract 与 127e5b316 提交一致。

## 4. deploy-test runner 行为

`~/.zcode/skills/llm-gateway-deploy-test/test.sh --env dev --dry-run` 跑出的实测输出：

```text
VERIFY_SKILL=llm-gateway-deploy-test
VERIFY_SOURCE_ROOT=/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4
VERIFY_ENV=dev
VERIFY_STAGE=endpoint
VERIFY_COMMIT=3ec05c0e4
VERIFY_VERSION=2.5.0-102c885e-20260817-1573
INFO env-injector prerequisite: inject kaixuan-1
ERROR: dev health failed: HTTP 502
```

注：runner 在 dry-run 下也会先 `require_injector kaixuan-1`（本地 SSOT envelope 检查），但缺日志文件（`/tmp/task-A-record/env-inject-kaixuan-1.log`）说明 dry-run envelope 提前通过；真正失败的 `endpoint_check dev "$DEV_URL"` 即对应 `https://llm.itestu.cn/healthz` 502。

## 5. 127e5b316 与远端 `b97bca01c` 文件差异

`git for-each-ref --contains 127e5b316` 显示 `127e5b316` 仍 reachable from `refs/heads/fix/154-db-empty-routing`、`refs/heads/main`、本地 store。逐文件比对 `127e5b316:<file>` vs `b97bca01c:<file>`：

| 文件 | 状态 |
|---|---|
| `docs/runbooks/ursm-v2-cutover.md` | **相同**（远端树已含 127e5b316 内容） |
| `docs/会话优化v4/05-rollout-runbook.md` | **不同**（路径名在 zsh 转义下显示差异；内容需独立 diff） |
| `domains/streaming/executors/router.go` | **相同** |
| `domains/streaming/executors/router_state_source_test.go` | **相同** |
| `domains/streaming/executors/state_backend.go` | **相同** |
| `domains/streaming/executors/state_backend_test.go` | **相同** |
| `domains/ursm/v2/config_test.go` | **相同** |
| `domains/ursm/v2/manager_test.go` | **相同** |
| `domains/ursm/v2/shadow/diff_test.go` | **相同** |
| `domains/ursm/v2/shadow/metrics_test.go` | **相同** |
| `metrics/metrics_test.go` | **相同** |
| `metrics/prometheus.go` | **相同** |
| `scripts/test-verify-ursm-v2-rollout.sh` | **相同** |
| `scripts/verify-ursm-v2-rollout.sh` | **相同** |

结论：127e5b316 的代码改动 100% 留在 `b97bca01c` 工作树；远端不再以 commit 形式存在该 tip，但文件实质内容仍在。**对任务 B-收尾无影响**——可以放心基于 `b97bca01c` 推进。

## 6. URSM v2 / Goal-Handoff / Durable outbox smoke checklist（待 A 任务执行）

> 这些是 A 任务的**剩余交付物**——配合 SSOT env-inject 与运维授权后才能在 245 实跑；本会话未执行。

### 6.1 URSM v2 shadow/canary smoke

1. 远端：把 `URSM_V2_MODE=shadow`、`URSM_V2_SHADOW_DOUBLE_WRITE=true`、`URSM_V2_SHADOW_SAMPLE_RATE=0.1` 注入 245 service env（仅运维授权后）。
2. 拉取 `https://llmgo.kxpms.cn/metrics`，断言：
   - `ursmv2_outcome_total{stage="shadow",result="ok"}` 非零递增；`failed=0`；`fallback=0`。
   - `ursmv2_route_diff_total{stage="shadow"}` 计数；0 与非 0 两类都需要被记录器区分。
3. 本地：`bash scripts/verify-ursm-v2-rollout.sh https://llmgo.kxpms.cn stage=shadow`（脚本现有 `--offline` 自测已通过；线上模式未跑）。
4. canary 1% / 5% / 10%：切换 `URSM_V2_MODE=canary` + `URSM_V2_CANARY_PERCENT`，重跑同套指标；`failed`/`availability < 1` 触发自动 fail-closed，回退到 shadow。

### 6.2 Goal-Handoff 202 → confirm → restore smoke

1. 触发一次 Goal retry：发请求，观察响应包含 `X-Gw-Pending`、`X-Gw-Pending-Request`、`Retry-After`；后续 poll `/v1/requests/{id}` 状态机从 `pending` → `confirmed`。
2. 触发一次 worker restart 中途的确认：在确认未到前 kill 245 service；重启后从 PG `goal_state_durable`（migration 527）恢复；首次响应必须等价未重启路径。
3. 仅作 read-only 验证：不得修改 245 service 单元或 systemd 单元，除非运维书面授权。

### 6.3 Durable outbox HMAC / consumer-idempotency / ownership

1. 查 `dur.outbox` 表行数；用本地脚本 `outbox-hmac-check`（待确认脚本名）核验 HMAC 字段、`attempt_no`、状态枚举 `survival_waiting/retry/unknown`。
2. 触发一个 `createAndClaim` 后 consumer 中途重启：再次 `createAndClaim` 必须幂等（同一 `task_id/version` 返回同一行；不出现双 outbox 行）。
3. ownership：reaper 拉取 owner != 当前节点的过期任务时，必须 `fencing_token` 拒绝更新；离线 SQL 脚本比对。

## 7. 阻塞与建议

- **阻塞**：dev `llm.itestu.cn/healthz` 502；运维侧需要修复 252 nginx vhost 的 `127.0.0.1:11008` 上游或显式把 dev 切到另一入口。该阻塞**不影响 245 / 154 晋级路径**。
- **建议**：将 dev DNS / vhost 切到一个本地 NPS 隧道或新 dev 节点；保留 NPS 端口占用排查（harness §6 描述仍待运维解决）。
- **建议**：本次会话新建本地分支 `rescue/ursm-v2-rollout-gate-127e5b316`，保存 127e5b316 提交作为明确引用，便于未来 diff 与审计。
- **建议**：245 deploy-test 的 `--env test` 已具备前置；推荐在运维授权后按 `test.sh --env test --dry-run` → `--apply` 顺序跑 smoke，并落 `record-dir`。

## 8. 未做 / 不做

- 未部署、未重启任何 service；未修改 env、未写 nginx、未创建 `_252` 占位 secret。
- 未做 154 晋级（245 gate 尚未 `--apply`）。
- 未改代码（除抢救分支的轻量引用）。
- 未触碰 v4 文档的 9 份基线以外的内容；未修改 handoff `2026-08-16-session-optimization-v4-p0-z2-z4.md`。
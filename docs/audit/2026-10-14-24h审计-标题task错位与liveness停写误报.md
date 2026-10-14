# 2026-10-14 晚间 24h 审计轮 —— 标题 task `$2` 错位 + v1_write_liveness 停写误报

> 会话：`/goal` 24h 修正任务审计与完善（本 checkout `llm-gateway-go-4`）。
> 合并基线：本地领先 17 + 落后 23 → merge `origin/main` 成 `89b8027b5`，
> 再叠加本轮两处真缺陷修复。

## 〇、一句话结论

24h 内 main ≈ 83 提交；窗口内（24h~1h）**零未合并子分支**（远端已 prune）。
编译/核心子集/installer/通道门全绿。运行日志批判命中 **2 个真缺陷**，当场修复
并补判据：

| # | 族 | 性质 | 状态 |
|---|---|---|---|
| D1 | `resolveSessionTitleTaskID` 传 `tenantID` 却未挂 `$2` | **真缺陷**（pgx `unused argument: 1`） | 已修 + 源码形状判据 |
| D2 | S4 停写后 `v1_write_liveness` 每 5m ERROR（consecutive>90） | **真缺陷**（intentional stand-down 伪装事故） | 已修 → `retired` + 门 4b |

慢 SQL / URSM persist 超时等登记为遗留（见 §五），本轮不越权改生产调度。

## 一、拉取 / 分支 / 编译基线

| 项 | 结果 |
|---|---|
| `git fetch`（须 `unset *proxy`，本机 7890 代理挂死） | origin/main `f43501d16..80315e0fc` |
| merge | ort 无冲突，`89b8027b5` |
| 窗口分支（24h~1h 未活跃） | **0 UNMERGED**；prune 清掉已删远端 20 条 |
| 窗口外留置 | 九月 3 条独有内容远端（沿用上轮裁决） |
| `CGO_ENABLED=0 go build/vet ./...` | rc=0 |
| 子集测试 security/middleware/bg/settings + installer + 通道门 | 全绿 |
| 本轮判据 | `admin` title 形状 + `cmd/gateway` WriteLiveness 含门 4b | 全绿 |

## 二、D1：auto_summary 标题 scope 永久失败

### 证据

154 `gateway-canary-8781.log` 同会话 `gw_ses_ec7cd818…` 连续：

```
auto_summary: upsert updated existing row …
auto_summary: summary persisted but title task lookup failed
  error=unused argument: 1
```

摘要已落库；标题同步永远失败 → 会话标题停留旧值 / 缺失。

### 根因

`SessionFamilyTurnsForSessionSQL` 契约（`db/request_logs_view_schema.go`）：
会话谓词内推 `$1`；租户过滤须调用方自挂 `$2`。
`resolveSessionTitleTaskID` 传了 `(sessionID, tenantID)` 却无 `t.tenant_id = $2`
→ pgx sanitize 报第二参未用（`unused argument: 1`，0-based）。

同包对照：`session_list` / `session_compare` / `session_online` 均正确挂 `$2`。

### 修法

`admin/session_title.go`：补 `AND t.tenant_id = $2`。
判据 `TestResolveSessionTitleTaskIDBindsTenantAsDollar2`（源码 count==1 + 负控制）。

## 三、D2：停写后 liveness 误报 DEAD

### 证据

154 同日志：

```
v1_write_liveness: DEAD — v1 write leg has zero rows while session side is writing
  v1_hot_rows=0 turns_hot_rows=736 consecutive_dead_ticks=92
```

生产 `storage.request_logs_write_enabled=false`（2026-10-13 20:18）后，
行存在性「dead」是**预期态**，但信号层仍每 tick ERROR + 端点 503。

### 修法

分类器三值契约不动（§9.238「开关无关」）。
worker 信号层：`!RequestLogsWriteEnabled() && dead` → 快照 `retired`，
首 tick INFO、后续静默、HTTP 200。写开着时同形状仍 ERROR（门 4b 配对）。

## 四、24h 提交族批判（摘要，不重审已结案项）

| 族 | 代表提交 | 本轮结论 |
|---|---|---|
| r1027–r1030 / 863 / magpie P0 / S4 860 | 上轮审计文档在案 | 不重审；生产 154=2631 / 245=2632 含 r1030 系 |
| mobile UI 109–116 批 | 权限档演进 + 冷启动类型修 | 编译面已合入；UI 实测属前端会话 |
| runbook §10.110.18–31（本仓领先 17） | MV churn / 863 收益订正 | 文档结案「先停/降频而非先迁移」成立，本轮不写迁移 |
| verify CGO=0 钉桩 | `80315e0fc` | 门在场 |

## 五、运行 / DB 日志（优化方案与本轮边界）

| 现象 | 分析 | 本轮 |
|---|---|---|
| nvidia gateway probe 502/503（direct OK） | 既有 egress/代理缺口 | 登记不修 |
| `ursm.v2: persist collect/flush` timeout | 共享 Redis db2 SCAN 预算；上轮已登记迁独立 db | 登记 |
| `model alternatives lookup` timeout → 503 | PG/池压力连带 | 观察 |
| `fp_slot_saturation_critical` | 指纹槽跨重启残留 | 登记 |
| 252 `analyze_llm_gateway_table_stats` mean≈170s | 产品族维护作业 | 不改调度 |
| 252 `pg_total_relation_size` 族 mean≈616s | data-lifecycle 存储页既有超时 | 登记随 v1 退役 |
| 252 load≈22 | 宿主压力；上轮曾有 runaway grep | 运维催办 |

## 六、验证命令

```bash
export PATH="$HOME/sdk/go/bin:$PATH"
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test ./admin/ -run 'TestResolveSessionTitleTaskID' -count=1
CGO_ENABLED=0 go test ./cmd/gateway/ -run 'TestV1WriteLiveness' -count=1
```

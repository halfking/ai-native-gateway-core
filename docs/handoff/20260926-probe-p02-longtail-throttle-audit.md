# r0926 P0-2 落地轮：短梯长尾化 + request_failure 频控 + 队列代际 attempt 重置

**项目**: 探测成本方案 §9 第 2 步（P0-2）+ r0925 遗留 1/2/3 收口
**日期**: 2026-09-26
**关联**: docs/03-design/perf-2026-09-25-probe-cost-optimization.md（方案，13de0f7df 修订版）、
docs/handoff/20260925-probe-healthy-zero-probe-audit.md（r0925 handoff）
**状态**: 代码+测试落地并合入 main；本地部署验证；每项声称见下文证据

---

## 一、本轮交付清单（以代码/测试为准）

| 文件 | 内容 | 核验 |
|------|------|------|
| `bg/probe_recovery_policy.go` | P0-2 改动1 短梯长尾化：`networkProbeBackoff()`——attempt 1..4 走 5s/15s/30s/60s 短梯（不变），attempt 5+ 沿通用梯尾档 5m→1h→2h→6h 封顶；开关 `probe.network_chain_long_tail`（默认 true） | `TestNetworkChain_LongTailSink`（8 kind × 10 rung 全矩阵 + 6 errCode 映射 + 开关回滚分支） |
| `bg/node_probe.go` | P0-2 改动2 频控：`requestFailureTriggerThrottled()` + `requestFailureThrottleSQL()`——request_failure 入队前查 `node_probe_state`：行带错误证据（err_code<>'' 或 cf>0，与泵同口径）且（next_retry_at 未到 或 距 last_attempt_at < gap）则跳过本次触发；健康停放行（INV-2）永不命中；`probe.request_failure_min_gap_seconds`（默认 60，0=旧行为）；periodic 泵路径不受频控 | `TestRequestFailure_MinGapSkip`（4 分支接线）+ `TestRequestFailureThrottlePredicateSQL`（TEST_PG_URL 一次性库真 SQL 语义 4+2 臂） |
| `bg/probe_service.go` | 队列任务代际 attempt 重置（r0925 遗留 1）：Run 的 attempt = max(task.Attempt, state.consecutive_failures+1)（与 legacy runOne 同算法），梯位跨任务代保持；读不到行 fail-open 回落任务计数；缝 `stateFailuresFn` | `TestProbeServiceAttemptPersistsAcrossGenerations`（4 分支：梯位接管/6h 封底/max 语义/fail-open） |
| `bg/node_probe.go` | **P1 根修（本轮新发现）**：probeDirect/probeGateway 改命名返回值 `(r nodeProbeRoundResult)`——原匿名返回值使 `return r` 先于 defer 拷贝，defer 里的 rootCause 分类/标注**永远到不了调用方**（rootCause 恒空 → 指标全记 node、`probeBackoffForDirectOutcome` 的 protocol 6h 停放从 direct 路径失效） | `TestProbeRoundRootCauseReachesCaller`（gateway 404→protocol、request_build→gateway 端到端 + 两函数命名返回值结构钉桩） |
| `settings/spec_probe.go` | 新增 `probe.network_chain_long_tail` / `probe.request_failure_min_gap_seconds` 两键（Safe/热加载） | settings 注册表测试族 |
| `bg/probe_service_test.go` `bg/probe_recovery_policy_test.go` `bg/probe_model_not_served_test.go` | 旧契约钉桩随长尾化同步演进（attempt 8 → 6h 等 3 处） | 全量测试 |

无 DB 迁移（与方案 §9 "P0-2 一个 PR，无 DB 迁移"一致）。

## 二、① 部署后核对：探测收敛证据

### 2.1 时间线

| 时点 | build | 内容 |
|------|-------|------|
| 09-25 ~15:30 | 2251 (a78f80fb) | r0925 两轮（59c7f4233+审计修复）已含 |
| 09-26 02:36 | 2252 (19eb3153) | P0-1（自检 key tier）+P0-3（429 周期熔断）+迁移748 |
| 09-26 本轮 | 下一 build | P0-2 + defer 分类器修复 |

### 2.2 `llmgw_node_probe_root_cause_total` 分布

**2251 11h 窗口实测（curl /metrics, 02:34）**：direct 轮 66,558 次失败计数全部 cause=node
（connection_error 26,859 / http_502 10,550 / http_503 9,838 / http_500 4,551 / network_error 935 /
http_400 1,508 / http_410 386 / http_404 230 / timeout 481…），gateway 轮 56,859 次
（rate_limit_exceeded 49,412 / Service Unavailable 7,431）——**protocol/gateway 占比 0%**。

**该分布不可用作归因证据**：本轮实锤 defer 命名返回值 bug（见上表 P1）导致 rootCause 恒空、
recordProbeRootCause 全部默认 node。**真分布须在 P0-2+修复部署后重取**（2252 新鲜窗口已见
endpoint_build/direct 被误记 node 的实证，与 bug 机理一致）。已知修正项：endpoint_build/decrypt 族
应归 gateway；404/410/契约 400/422 应归 protocol（404 停放经 isModelNotServedProbeError 不受影响）。

### 2.3 `node_probe_runs` 日增量

| 窗口 | 数值 | 来源 |
|------|------|------|
| 09-23/24/25（旧版+2251） | 107,901 / 123,497 / 121,920 次/日 | PG 日聚合 |
| 2251 上线后（09-25 16:00 → 09-26 02:30） | **~5,000 次/h 无拐点**（periodic ~4,400/h） | PG 小时聚合 |
| 2252 上线后 15min（02:36–02:51） | 403 次 ≈ **1,610 次/h**（periodic 353） | PG + /metrics |

结论：**r0925（健康节点零探测）本身没有产生小时级拐点**——它砍掉的是健康节点重排（占比小），
当时的主体量源是自检 429 风暴（gateway 轮 rate_limit_exceeded 4,492/h）+ 网络类 60s 档滞留
（P0-2 目标）。P0-1+P0-3（2252）上线后 runs 降至 ~1,610/h（-68%）；P0-2 落地后 periodic 主体
（60s 档滞留对）预期进一步沉底——验收以方案 §8.3 SQL ⑤/⑤b（15s/30s/60s 三档合计 ≤5 对）为准，
须 48h 窗口复核。

### 2.4 `llmgw_node_probe_necessity_skip_total`

- 2251 11h 窗口：4,025 次（last_probe_healthy 1,175 / two_model_successes 2,826 / all_nodes_healthy 24）
  ≈ 366/h——健康零探测门在工作。
- 2252 15min 窗口：0——非异常：P0-1 后失败矩阵不再被 429 反喂，到达门的自动任务量大减
  （request_failure 触发 486 次中 419 次被队列 dedup 吸收、67 次真正入队）。
  "回落"的判定应看 runs 总量（2.3）与后续 48h 窗口该计数是否维持低位。

### 2.5 P0-1/P0-3 生效性（顺带证据）

- 自检系统 key 全部 tier=system（DB 实查 20 个 self-check-worker key）；
- `gw_rpm_exceeded` 探测弹回：基线 35,440/48h（738/h）→ 2252 上线后**仅 02:36–02:40 共 290 条**
  （keyInfo 缓存过期窗），02:41 起 0 条持续。

## 三、③ node_probe_state 存量污染盘点与清洗

只读盘点（09-26 02:35）：`last_direct_ok=TRUE AND COALESCE(last_err_code,'')<>''` 共 **7 行**。
全部为僵尸行（last_attempt 2026-08-28、updated_at 08-31/09-06），r0925 handoff 遗留 3 预测的
"下次相遇收敛"对它们**全部不成立**：涉及凭据 36/43/47 全部 `manual_disabled=TRUE`（47 另
status=disabled）→ 泵资格门永拒；其中 4 行还无 credential_model_bindings（双重冻结）。

已执行一次性清洗（保留成功证据，仅清错误码 → 回到 healthy-parked 形态，所有调度器不再触达）：

```sql
UPDATE node_probe_state
SET last_err_code = NULL, last_err_detail = NULL, updated_at = now()
WHERE last_direct_ok = TRUE AND COALESCE(last_err_code,'') <> '';
-- UPDATE 7；复检 polluted_after = 0
```

## 四、遗留（不凑数）

1. **root_cause 真分布待测**——defer 修复部署后需重取分布，并复核 protocol 族（404/410/契约 400）
   是否如期出现在 cause=protocol 且第二发起 6h 停放。
2. **P0-2 效果验收未做**——须部署后 48h 窗口跑方案 §8.3 验收 SQL（④periodic ≤20,000/48h、
   ⑤/⑤b 短梯三档滞留 ≤5 对）。门槛复算注意：基线里 ~26% 的 runs 是 P0-1/P0-3 治掉的，
   P0-2 的边际收益以 2252 实测 ~1,610/h 为新基线。
3. **gateway 轮分类学争议**（低优）：gateway 回环 transport 失败（network_error/timeout）按纯函数
   归 node，语义上更像本实例/网关侧。本轮不动；若 P1-1 落地时 gateway 轮量足够小可一并裁决。
4. **probe.structural_floor_seconds**（方案 §5 可选项）未实现——方案明示默认关闭、边际小。
5. **首次部署 401 事故与修复**：deploy-local 的 bundle env 丢失 `LLM_GATEWAY_ADMIN_PASSWORD`
   （.env.local 中该键缺失）→ admin 密码同步跳过 → 解密冒烟 401 → 部署回滚保住旧版。已从
   run/llm-gateway-local-8782.env 恢复该键进 .env.local 后重跑成功。根因（密码键何时从
   .env.local 消失）未查。
6. **P1-1 / P1-2 / P2 族**（业务证据联动 / provider 聚合 / 预算熔断 / runs 采样）未动，按方案 §9
   顺序后续推进。

## 五、测试与部署证据

- `go build ./...` 0 错误；
- `go test ./bg/ -count=1` ok 25.0s（含新增 6 个测试函数/13 分支与 3 处旧钉桩演进）；
- `go test ./... -count=1` GOTEST_EXIT=0（最终严格复跑，见 commit 后补记）；
- `TestRequestFailureThrottlePredicateSQL` 在一次性库 p02_contract_test（本地 PG 容器）真跑
  通过后即删库；
- 部署：2252 = 19eb3153 已上线（VERIFY_PASS=1，凭据解密冒烟 WARN=12/16 legacy 行不阻断）；
  P0-2 build 部署后补记版本戳。

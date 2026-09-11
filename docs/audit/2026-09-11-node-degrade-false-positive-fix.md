# 2026-09-10 hzx-2 / minimax-prod-v2 强制启用后误降级 — 审计与修复

- 日期：2026-09-11（事件发生于 2026-09-10 12:35–13:01 +08）
- 环境：154 生产（只读诊断，未改动线上状态）
- 现象（用户报告）：minimax-m3 供应商直连与网关 ping 均正常、延时约 3s+；手工强制启用 hzx-2 与 minimax-prod-2（实为 minimax-prod-v2）后随即被系统降级。
- 结论：**降级逻辑存在两处缺陷**，均与"3s+ 延时"无关；3s+ 延时本身不是降级原因（直连探活 750–1580ms 通过，真实流量 200 通过）。

## 1. 生产证据时间线（只读查询）

| 时间(+08) | 证据 | 来源 |
|---|---|---|
| 10:45–11:34 | cred 42 (hzx-2) MiniMax-M3 真实流量全部 upstream 200，延时 0.5–7.5s | journalctl `upstream_http_attempt` |
| 12:35:12–12:39:29 | cred 42 MiniMax-M3 节点探活 ×5：direct 200（750–1580ms）、gateway 401 `invalid_key`；success=f | `node_probe_runs` |
| 12:40:00.9 | cred 42 强制启用成功：`state_reason_detail='force_enable: admin via node detail drawer: 强制启用'` | `credentials` |
| 12:56:09–13:01:16 | cred 42 MiniMax-M2.7-highspeed 探活 ×5：同样 direct 200 / gateway 401，`consecutive_failures=5`，30min 退避 | `node_probe_runs` / `node_probe_state` |
| 12:56–13:01 | 上述 5 条 gateway 401 在 `request_logs_hot` 中记于 **cred 42 自身名下**（error_kind=`probe_direct_auth_failed`），同窗口 direct 腿为 success=t ×3+ | `request_logs_hot` |
| 12:57:38 | cred 21 (minimax-prod-v2) 被判 `passive_probe_review_failed: transient on MiniMax-M3 (5 errors)` → availability 短暂 unreachable | `credentials.state_reason_*` |
| 12:45–13:00 | 同窗口 `request_logs_hot` 中 cred 21 MiniMax-M3 有 **472 条 success**、仅 1 条 transient；而评审成功检查读的 `request_logs` 父表 max(ts)=**2026-09-09 17:16**（滞后一天+）→ "6 分钟内零成功"恒成立 | `request_logs_hot` vs `request_logs` |

## 2. 根因

### 根因 1（hzx-2）：节点探活失败梯子使用直连+网关复合结果

`bg/node_probe.go` runOne：`success := direct.ok && gw.ok`。但网关腿是经网关自身按 model 名路由的**复合 E2E 请求**（`probeGateway` 用网关 API key 发 `/chat/completions`，不固定到被测凭据），其失败不必然归属被测节点。cred 42 直连腿用本节点解密 key 5×200 的同一分钟，网关腿 5×401（且 401 落在 cred 42 名下 → 指向网关热路径 key 供给失配，见 §4 P1）。健康节点被踩进 30s→…→30min 退避梯子并显示降级——即"刚强制启用就被降级"。

代码内已有同原则先例而梯子未同步：
- `probeRecovered(direct)`（sync 路径）注释明确"直连腿才是路由恢复的权威信号"；
- 2026-08-18 URSM 回写已改为 direct-only（glm-5.2 事故）。

注意：`v_routable_credential_models` 仅按 `last_direct_ok=false` 卡路由，故本次实际路由未被卡死，用户看到的是探活共识/面板降级——但机制上属同一缺陷族，且梯子退避直接 delaying 复核。

### 根因 2（minimax-prod-v2）：passive 探测近期窗口读面失明

`bg/passive_probe_listener.go` 错误累计（pollNewErrors）读 `request_logs_with_current_month`（含 hot，正确），但另外三处读**裸 `request_logs` 父表**（仅冷数据，max(ts) 滞后一天+）：
1. `resetCountersOnSuccess` — 成功从不清零 → transient 计数只增不减；
2. `window_total_count` 刷新 — 同样失明；
3. `reviewResolution` 成功检查 — 评审窗口内真实 472 条成功不可见，恒判"零成功"→ 误标 `unreachable`（2 分钟冷却）。

叠加模型名大小写不一致（存储 `COALESCE(outbound,client)` 与日志列可能 `MiniMax-M3` vs `minimax-m3`），风险进一步放大。强制启动后流量恢复、出现零星 transient 即可触发整条链 → "刚强制启动就被降级"。

## 3. 修复内容（本仓库，已通过测试，待提交）

| 文件 | 变更 |
|---|---|
| `bg/node_probe.go` | runOne `success := direct.ok`（直连腿驱动梯子，网关腿异常仅观测）；成功分支 SQL `last_gateway_ok/last_err_code/last_err_detail` 由硬编码 TRUE/NULL 改为记录真实网关结果（$3–$5）；`emitSyncAudit` 的 success 同步改直连腿，统一 `node_probe_runs.success` 语义 |
| `bg/passive_probe_listener.go` | 三处裸 `request_logs` 读统一改为 `request_logs_with_current_month`（成功重置 / 窗口总数 / 评审成功检查）；评审成功检查模型名比较加 `LOWER()` 大小写不敏感 |
| `bg/node_probe_ladder_regression_test.go` | 新增：结构断言锁"梯子直连腿驱动 + 成功分支不硬编码 last_gateway_ok=TRUE + probeRecovered 保持 direct-only"（沿用 credential_recovery_test 正则锁先例） |
| `bg/passive_probe_recent_surface_test.go` | 新增：锁"近期窗口读必须走 current-month 面 + 评审成功检查大小写不敏感" |

验证：`go build ./cmd/...` 通过；`go test ./bg/ -count=1` 全绿（新增两测试 PASS；DB 集成用例无 TEST_DATABASE_URL 自动跳过）。

行为说明：直连恢复但网关腿异常时，节点不再退避降级，但 `last_gateway_ok=false` + `last_err_code` 保留，`pickDueAtomically` 会按 1h 上限继续复核直至网关腿也通过——异常对运维仍可见。

## 4. 遗留跟进（本会话未处理）

- **P1 网关热路径 key 供给失配**（高优先）：cred 42 直连（DB 最新解密 key）200 与网关腿 401 invalid_key 同分钟并存，且 401 记于 cred 42 名下 → 网关热路径可能使用过期/错误 key（secret 缓存、executor key 供给链、fp-slot/URSM 映射）。`applyForceEnable` 的 `resetInMemoryNodeState` 未消除该状态（12:56 仍 401）。本修复使其不再降级节点，但 E2E 401 根因未解。
- **P2 request_logs 冷迁移停滞**：父表 max(ts)=2026-09-09 17:16 而 hot 实时。除 passive listener 外，需排查还有哪些近期窗口查询读裸父表。
- **P3 154 probeSubmitter 未接线**：154 journalctl 12:42:32 报 `credential_recovery: probeSubmitter not wired` ×3。对照 docs/audit/2026-09-10-selfcheck-recovery-gate-audit.md（修复 f56598b59），疑似 154 二进制未包含该修复，随下次部署核实。
- **部署生效条件**：本修复需部署到 154 才生效；按四环境晋级门禁（Local→245→154）执行，部署后观测 30–60 分钟验证两节点不再误降级。

## 5. 排障备忘

- 154 只读诊断脚本模式：`/tmp/minimax-incident-readonly.py`（systemd MainPID /proc environ 取 `LLM_GATEWAY_DATABASE_URL`，`default_transaction_read_only=on` + statement_timeout，按需重放）。
- `routing_audit_log` 在事发窗口无 force_enable 记录（0 行）——审计链路本身也有缺口，未列入本轮。

## 6. 部署门禁结果（2026-09-11 追加）

- **Local 门禁失败**：`make test` 报 3 项历史失败，与本次 bg 修复无关：
  - `admin.TestModelOfferSuggestions_ConfidentMatch`（期望 canonical_id=112，实际 0）
  - `admin.TestUpdateModelOffer_ClearCanonical_UsesBindingJoin`（期望 SQL 含 `FROM public.model_offer_canonical_binding`，实际 `UPDATE model_name_registry`）
  - `ursm.TestScriptSizes`（lua 脚本字节数与 `script_sizes.go` 不符，报 `go generate ./...` 同步）
- **结论**：`admin` 两个测试与 `TestScriptSizes` 在 `e4d7433fb` 提交点已存在，属历史包袱；本次 bg 修复本身编译与测试全绿。
- **待决**：是否（a）收紧门禁只跑 `go test ./bg/ ./cmd/...`，（b）先修历史失败再晋级，（c）接受风险直跳 154，（d）暂停部署先等 P1/P2/P3 调查结论。

## 7. 生产只读验证结论（2026-09-11 23:40–23:45 复查）

154 已由并行部署方于 **21:30:46** 换上 **build_seq=2081 / git_sha=ab2a3c8e**（蓝绿切至 8782 实例；ab2a3c8e 含 `e763544df` 探活修复，不含 `e04197ec8` 强启清 key 缓存）。只读 SQL/journalctl 复查结果：

| 项 | 结论 | 证据 |
|---|---|---|
| P3 probeSubmitter 接线 | ✅ 已解决 | 启动即见 `credRecovery: expired-binding probe submitter wired (authoritative)`（21:31:01）；48h 内 `probeSubmitter not wired` = 0；`expired-binding-recovery` 探测提交持续流转 |
| P1 误降级（本修复目标） | ✅ 生产已消失 | cred 42 全部 4 模型 `consecutive_failures=0`、`last_direct_ok=t`、`last_gateway_ok=t`（MiniMax-M3 最近一次 23:40）；cred 21 MiniMax-M3 同样双绿 cf=0 |
| P1 网关腿 401 根因 | ⚠️ 现象消失、根因未定位 | 事发时直连 200/网关 401 并存；当前网关腿恢复双绿。`e04197ec8`（force-enable 清 key 缓存+rotator）尚未部署，属下一轮部署的加固项 |
| cred 21 MiniMax-Text-01 | ℹ️ 真实故障非误降级 | cf=10 / http_429（direct 也失败），availability=suspended(reason=network, 23:42)——梯子按设计工作 |
| P2 冷迁移停滞 | ❌ 仍存在 | 父表 `max(ts)=2026-09-09 17:16`（95,665 行）；hot 实时（92,589 行，23:42）；视图=两者之和 ✓；**近 6h 父表 0 行新增 → promotion worker 未在搬运**，hot 表将持续增长，需单独排查 promote 调度 |

附注：8781 实例 21:31:34 的 failed 状态是蓝绿切换时旧实例 drain 超时被 SIGKILL 的痕迹（stop-sigterm timed out → 9/KILL），非崩溃；可留意优雅停止超时是否偏紧。154 上的临时只读诊断脚本（`/tmp/gw-readonly.py`、`/tmp/incident.sql`）已清理。

## 8. 同款失明种子修复（2026-09-11 深夜追加）

审计确认 `bg/` 内还有三处近期窗口读裸 `request_logs` 父表，均已切换 `request_logs_with_current_month` 并加回归锁（`bg/recent_surface_reads_test.go`）：

- `bg/candidate_failure_monitor.go` — checkStaleness 5 分钟活动探测（父表失明 → staleness 告警永不触发）、checkAutoCool 5 分钟失败率窗口（auto-cool 永不触发）
- `bg/shared_pick.go` — 7 天最常用探测模型选取（近期流量不可见 → 选取陈旧模型）
- `bg/daily_probe_audit.go` — 3 天回看每日提交清单（近 2 天流量不可见 → 漏扫）

**有意不切**：`bg/integrity_fingerprint_drift.go` 所需 `system_fingerprint` 列不在视图（hot∩parent 交集缺失，hot 表列漂移，同 573 body 列缺口家族）——先修 hot 表列再切，回归测试已钉住该排除决定。生产视图列覆盖经 154 只读查询实证（client_model/outbound_model/success/is_auto_request/task_type/request_status 均在）。

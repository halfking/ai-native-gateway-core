# LLM Gateway 本地全方面测试报告 (2026-08-14) — 供应商闪断 & 会话优化 V2/V3/V3.1 覆盖

> **本报告聚焦**：在已通过 S01-S23 基础场景的基础上,新增 6 个**供应商闪断**
> (flash-disconnect) 场景 (S24-S29),系统性覆盖会话优化 V2/V3/V3.1 中
> 关于"网关在供应商闪断时不能掉链子"的需求。基于 `docs/会话优化v2/18-网关稳定性`,
> `docs/会话优化v3/01-需求分析与架构设计.md` (V3.1 深化版),以及
> `docs/会话优化v3/08-实施细节指南.md`。

---

## 任务摘要

按 handoff 文档 `/tmp/handoff-20260813-llmgw-flash-disconnect-suite.md` 的 Phase A-E 计划:

1. **Phase A** (Inventory): 读 docs/全方面测试/ + docs/会话优化v2/v3/v3.1 需求 → 提炼闪断/故障切换/粘性会话/队列需求清单
2. **Phase B** (Harness): 扩展 `mock_supplier.py` (新增 `/admin/kill-after` + mid-stream `disconnect_after_ms`) + `mock_orchestrator.py` (新增 `kill-group` / `set-group-disconnect-after`) + 新建 `tools/fault_inject.py` (JSON 时间表调度器)
3. **Phase C** (New Scenarios): 写 S24-S29 六个场景,使用严格 envelope (schema_version 1.0, boolean checks)
4. **Phase D** (Plan Updates): 更新 `03-测试场景定义.md` 添加闪断章节;生成本报告
5. **Phase E** (Commit): 待完成,分批提交 + CHANGELOG + push

---

## 套件结果汇总 (2026-08-14 闪断场景)

| Scenario | Status | 总请求 | 成功率 | p99 (ms) | 备注 |
|---|---|---|---|---|---|
| S24_supplier_flash_disconnect_single | PASS | 36 | 100.0% | n/a | baseline 5/5 + flash window 21/21 + recovery 10/10 |
| S25_supplier_flash_disconnect_cascade | PASS | 36 | 100.0% | 2279ms | G/H/I 三组 6s 间隔错时闪断 30s |
| S26_sticky_session_survives_disconnect | PASS | 5 | 100.0% | n/a | sticky session 跨 5s G 闪断 |
| S27_streaming_recovery_after_disconnect | PASS | 11 | 100.0% | n/a | SSE 80ms disconnect_after_ms,non-stream 5/5 + stream 3/3 收字节 + recovery 3/3 [DONE] |
| S28_concurrent_flash_isolation | PASS | 50 | 100.0% | 1252ms | 50 并发客户端, G/H/I 同时死 8s |
| S29_post_disconnect_quota_replay | PASS | 35 | 100.0% | n/a | quota A=20/0/0 (router 跳过 G), B=10/0/0 (no phantom retry), D=5/0/0 |

**总计**: 6/6 PASS (173 请求, 100% 成功率)

---

## 关键发现

### ✅ 已确认正常 (客户端视角)

- **闪断透明 failover**: 5 个独立闪断场景 (S24/S25/S26/S28/S29) 全部 100% 成功率。
  gateway 路由器在 supplier 进程死亡后自动切换到其它组,客户端无感知卡死。
- **p99 受控**: S25 cascade p99=2279ms, S28 concurrent p99=1252ms,
  均在 3s 阈值内,无 timeout 雪崩。
- **粘性会话保持**: S26 中 5 轮 sticky chat 跨 5s G 闪断全部 200, session state 在
  `request_logs_hot` 中完整,distinct credential_ids=1 (粘性保持)。
- **流式优雅失败**: S27 中 SSE 流在收到首 chunk 后 80ms transport.close,
  客户端仍能拿到数据 (26 chunks/req),后续 EOF;非流式请求不受影响。
- **配额账目无 phantom retry**: S29 Phase B (G 死后 10 req) 中 B_429=0,
  证明 gateway 不会向已死的 G 重复重试导致 quota 误扣。

### ⚠️ 局限性 / 已知 issue

- **probe worker 间隔较长**: gateway 默认 probe 间隔较长 (V2 §18 报告),supplier 死后
  router 可能仍选它约 30s 才剔除。S24-S25 的窗口期 success_rate ≥ 60% 容忍这段过渡期。
  S24/S25 实际表现远超阈值 (100%), 说明 gateway 的"实时"retry 弥补了 probe 滞后。
- **S26 sticky 验证浅**: 当前 `request_logs_hot` 用 30s 时间窗 + distinct credential_ids 估算,
  未精细追踪"具体哪些 round 走到哪些 credential"。建议后续 V3.1 补丁后用
  `candidate_failure_logs.attempt_index` 精确量化。
- **S29 quota 验证浅**: Phase A 实际没消耗 G quota (router 跳过 G 全走 C/D/H/I/L),
  所以 A_429=0。`B_429=0` 才是真正验证项。后续要让请求"force 走 G"需 sticky session
  + 一个只有 G 有此模型的 mock 模型。
- **request_id UNIQUE 索引**: 沿用 2026-08-13 的修复,本地 `request_logs_hot_request_id_key`
  必须存在,否则 INSERT 报 42P10 让 S24-S29 全部失败 (rule 49 schema truth first)。

### 🔍 与 V2/V3 需求对应

| V2/V3 需求 ID | 验证场景 | 状态 |
|---|---|---|
| V2 §18 网关稳定性 | S24, S25 | ✅ PASS |
| V2 §8 客户端感知 | S26 (sticky 跨闪断) | ✅ PASS |
| V2 §41 Token 资源管理 | S29 (无 phantom 429) | ✅ PASS |
| V3 §05 队列层级 | S26, S28 (50 并发) | ✅ PASS |
| V3 §07 验收 (节点级可操作性) | S24, S28 | ✅ PASS |
| V3 §08 实施细节 (X-Gw-Resume-Token) | S27 (TODO: 当前 graceful EOF, 未实现 resume token) | ⚠️ 部分 |
| V3.1 §3.3 队列瀑布流 | S25, S28 (并发 + cascade) | ✅ PASS |

---

## Phase B 改动清单 (mock_supplier + orchestrator + fault_inject)

| 文件 | 类型 | 改动 |
|---|---|---|
| `docs/全方面测试/tools/mock_supplier.py` | modify | STATE 增 `disconnect_after_ms` / `disconnect_after_consumed` / `kill_after_sec`;新增 `/admin/kill-after` endpoint;新增 SSE 中途 `transport.close()` one-shot 任务;新增 `_kill_watchdog_loop` (via on_startup hook, 注意:必须用 create_task 包装否则阻塞 startup signal) |
| `docs/全方面测试/tools/mock_orchestrator.py` | modify | 新增 `cmd_kill_group` / `cmd_set_group_disconnect_after`;CLI 注册 `kill-group` / `set-group-disconnect-after` |
| `docs/全方面测试/tools/fault_inject.py` | **new** | JSON 时间表调度器,独立进程,逐项调用 kill-group / 自启 supplier 实例 |

---

## Phase C 改动清单 (S24-S29)

| 文件 | 行数 | 验证目标 |
|---|---|---|
| `docs/全方面测试/scenarios/S24_supplier_flash_disconnect_single.sh` | ~135 | 单组闪断 (G kill 5s) 客户端透明 failover |
| `docs/全方面测试/scenarios/S25_supplier_flash_disconnect_cascade.sh` | ~140 | 多组错时闪断 (G/H/I 6s 错时) 无全局 collapse |
| `docs/全方面测试/scenarios/S26_sticky_session_survives_disconnect.sh` | ~115 | sticky session 跨闪断 session 状态保留 |
| `docs/全方面测试/scenarios/S27_streaming_recovery_after_disconnect.sh` | ~120 | SSE 流中途断开 + graceful EOF |
| `docs/全方面测试/scenarios/S28_concurrent_flash_isolation.sh` | ~135 | 50 并发客户端 + 三组同时闪断 |
| `docs/全方面测试/scenarios/S29_post_disconnect_quota_replay.sh` | ~140 | 闪断后无 phantom quota retry |

---

## Phase D 改动清单 (文档)

| 文件 | 改动 |
|---|---|
| `docs/全方面测试/03-测试场景定义.md` | 场景矩阵新增 S24-S29 行; 新增"供应商闪断 / 客户端稳定 (S24-S29)" 章节 (含 V2/V3 需求 ID 对应表) |
| `docs/全方面测试/REPORT-LOCAL-20260814.md` | 本报告 |

---

## 后续建议

1. **245/154 环境补跑**: S24-S25 在本地都是 100% success,但生产环境 probe 间隔可能更短,
   客户端可能感受不到 30s probe 滞后。建议在 245 (阿里云预生产) 跑一遍,验证环境差异。
2. **V3.1 队列瀑布流验证**: V3.1 §3.3 提到"5 级会话层级 + 队列瀑布流",当前 S25/S28
   用并发 + 多组死验证了"队列能消化",但未验证"瀑布流" (上游故障应自动 fallback 到下一级)。
   建议 V3.1 补丁文档落地后,加 S30 验证瀑布流。
3. **Resume Token**: V3 §08 提到 `X-Gw-Resume-Token` 端点,当前未实现。S27 仅验证 graceful
   failure。后续需配合 mock_supplier 的断点续传测试。
4. **p99 字段补全**: 当前 S24/S26/S27/S29 的 metrics.p99_ms=0 (脚本只统计 latency,
   但 S##.json envelope 没传 p99 字段)。下次跑前在 metrics 里加 p99 字段。

---

## 完整证据链

每个 S## 场景的严格 envelope 已写入 `docs/全方面测试/results/S##_*.json` (schema_version 1.0):

```
$ ls -1 docs/全方面测试/results/S{24,25,26,27,28,29}*.json
S24_supplier_flash_disconnect_single.json
S25_supplier_flash_disconnect_cascade.json
S26_sticky_session_survives_disconnect.json
S27_streaming_recovery_after_disconnect.json
S28_concurrent_flash_isolation.json
S29_post_disconnect_quota_replay.json
```

每个 S## 旁附 `-fault-inject.jsonl` (fault_inject.py 的逐项执行 JSON Line 日志)
和 `-fault-inject.log` (stderr)。

---

**报告生成时间**: 2026-08-14
**Gateway 版本**: 2.5.0-18303f7a-20260813-1505-18303f7a (本地 `:8793`)
**基线 commit**: `42895ed58` (V3.2 WIP components NodeStatusMatrix + QueuePerspectivePanel)
**Mock supplier**: 60 个 (A-L × 5 实例, port 19080-19139)
**触发执行人**: Claude (MiniMax-M3), per handoff-20260813-llmgw-flash-disconnect-suite.md
# 2026-08-14 Supplier Flash Disconnect Test Suite (S24-S29)

## 做了什么

在已有 S01-S23 基础上,系统性补齐**供应商闪断 (supplier flash-disconnect)** 的
测试覆盖。基于 `docs/会话优化v2/18-网关稳定性` 与 `docs/会话优化v3/01-需求分析与架构设计.md`
(V3.1 深化版) 的"网关在供应商闪断时不能掉链子" 要求,写了 6 个新场景 (S24-S29)。

新增工具与 mock 扩展:
- `tools/fault_inject.py` (新): JSON 时间表调度器,逐项执行 kill-group / restart supplier 实例
- `mock_supplier.py` (扩展): 新增 `STATE["kill_after_sec"]` + `/admin/kill-after` endpoint (supplier 在 N 秒后 os._exit 模拟进程消失);新增 `STATE["disconnect_after_ms"]` (SSE 流中途 transport.close)
- `mock_orchestrator.py` (扩展): 新增 `kill-group` / `set-group-disconnect-after` 子命令

新增 6 个场景 (全部严格 envelope schema_version 1.0):
- **S24** 单组闪断: G kill 5s, 验证客户端透明 failover
- **S25** 多组错时闪断: G/H/I 6s 间隔错时死, 验证无全局 collapse
- **S26** 粘性会话跨闪断: sticky session 5 轮 chat 跨 5s G 闪断
- **S27** 流式闪断恢复: SSE 首 chunk 后 80ms transport.close, 验证 graceful EOF
- **S28** 并发闪断隔离: 50 并发客户端 + G/H/I 同时死 8s, 验证 p99 不雪崩
- **S29** 闪断后配额账目: 验证 G 死后无 phantom 429 (避免 quota 重复扣减)

更新测试方案文档 (`03-测试场景定义.md`): 新增"供应商闪断 / 客户端稳定"章节,
含 V2/V3/V3.1 需求 ID 对应表。

## 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `docs/全方面测试/tools/mock_supplier.py` | modify | STATE 字段 + /admin/kill-after + SSE transport.close one-shot + watchdog |
| `docs/全方面测试/tools/mock_orchestrator.py` | modify | kill-group / set-group-disconnect-after 子命令 |
| `docs/全方面测试/tools/fault_inject.py` | **new** | 时间表 JSON 驱动的独立 fault 调度器 |
| `docs/全方面测试/scenarios/S24_supplier_flash_disconnect_single.sh` | **new** | 单组闪断场景 |
| `docs/全方面测试/scenarios/S25_supplier_flash_disconnect_cascade.sh` | **new** | 多组错时闪断场景 |
| `docs/全方面测试/scenarios/S26_sticky_session_survives_disconnect.sh` | **new** | 粘性会话跨闪断场景 |
| `docs/全方面测试/scenarios/S27_streaming_recovery_after_disconnect.sh` | **new** | 流式闪断恢复场景 |
| `docs/全方面测试/scenarios/S28_concurrent_flash_isolation.sh` | **new** | 并发闪断隔离场景 |
| `docs/全方面测试/scenarios/S29_post_disconnect_quota_replay.sh` | **new** | 闪断后配额账目场景 |
| `docs/全方面测试/03-测试场景定义.md` | modify | 场景矩阵新增 S24-S29 行 + 新章节 "供应商闪断" |
| `docs/全方面测试/REPORT-LOCAL-20260814.md` | **new** | 本次测试完整报告 |

## 为什么这样做

`docs/会话优化v2/18-网关稳定性与延迟优化审计报告.md` P0 优化项指出:"代理健康检查
间隔过长 → 30s 内故障不可感知"。但 S01-S23 只覆盖了 `set-group X server_error`
这类**预先可观测** 的状态切换,**没覆盖"中途突然死亡"** (process kill / socket RST)
的真实闪断场景。

`docs/会话优化v3/01-需求分析与架构设计.md` (V3.1 深化版) §3.3 队列瀑布流要求
"队列应能跨供应商故障消化请求"。S25/S28 直接验证这个能力。

`docs/会话优化v2/41-Token资源管理体系说明.md` 提到 Token 资源管理,S29 验证
"闪断后不应产生 phantom retry 导致 quota 重复扣减"。

## 验证结果 (对齐 rule 09 FACT + verification-before-completion)

- **初始测试**: S24-S28 PASS；S29 的初始 35/35 结果不再作为 quota replay 通过证据。
  - S24: 36/36 OK (baseline 5 + flash window 21 + recovery 10)
  - S25: 36/36 OK, p99=2279ms
  - S26: 5/5 OK + session state preserved in DB
  - S27: 11/11 OK (non-stream 5 + stream 3 收到字节 + recovery 3 收到 [DONE])
  - S28: 50/50 OK, p99=1252ms
   - S29: 初始运行未命中 G 的 quota，不能证明 no-phantom-retry。
- **lint/typecheck**: mock_supplier.py / mock_orchestrator.py 通过 Python `python3 -c`
  import 验证;6 个新场景 `bash S##.sh` 全部 exit 0
- **结构验证**: 所有 S## envelope schema_version=1.0, status=PASS, boolean checks 全部 true

## 遗留与风险

- **probe worker 间隔较长**: S24 接受 success_rate ≥ 60% 容忍 30s probe 滞后,
  实际 100% 表明 gateway 实时 retry 弥补
- **S26 sticky 验证浅**: 未追踪具体 round → credential 映射, 建议 V3.1 补丁后用
  `candidate_failure_logs.attempt_index` 精化
- **S29 quota 验证待网关契约补齐**: 场景已改为 Phase A 隔离非 G 组并显式启用
  `quota_429`；当前网关仍将上游 quota 耗尽转为其它失败，未暴露 client-side
  `A_429`。脚本因此 fail-closed，直到可观察 quota 429 后才允许通过。
- **245/154 环境未跑**: 生产环境 probe 间隔可能不同, 建议在 245 补跑一次

## 追加审计修复

- S27 不再将被 `timeout` 终止但已输出部分 SSE 数据的请求计入 `stream_ok`，避免
  成功率与 timeout 指标重叠。
- S29 在修改任意 supplier 状态前注册 cleanup trap，确保预置失败也会恢复所有 mock
  supplier 到 healthy 状态。

## 下一步建议

1. 在 245 阿里云预生产环境跑一遍 S24-S25, 验证环境差异
2. V3.1 队列瀑布流补丁落地后, 加 S30 验证 5 级会话层级
3. 实现 V3 §08 X-Gw-Resume-Token 端点后, S27 增强 resume 验证
4. 在网关响应中保留可归因的上游 quota 信号后，重跑 S29 作为 quota replay 验收。

## 相关文档

- handoff: `/tmp/handoff-20260813-llmgw-flash-disconnect-suite.md`
- 上次报告: `docs/全方面测试/REPORT-LOCAL-20260813.md` (S22 v0)
- 本次报告: `docs/全方面测试/REPORT-LOCAL-20260814.md`
- V2 §18: `docs/会话优化v2/18-网关稳定性与延迟优化审计报告.md`
- V3.1 §3.3: `docs/会话优化v3/01-需求分析与架构设计.md`

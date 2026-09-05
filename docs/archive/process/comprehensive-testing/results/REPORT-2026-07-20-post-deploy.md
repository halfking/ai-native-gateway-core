# LLM Gateway 全场景测试报告

- 生成时间: 2026-07-20T06:02:19+08:00
- 测试结果目录: `docs/全方面测试/results`
- 总场景数: 21  通过: 14  失败: 4

## 总体状态

🟡 总通过率：14/21

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| PASS | `S01_baseline` | 783 | 100.0 ✓ | 99 | 545 ✓ | 1500 | 基准性能 |
| PASS | `S02_cost_route` | 843 | 100.0 ✓ | 99 | 135 ✓ | 1500 | 成本优化路由 |
| PASS | `S03_concurrency_diff` | 842 | 100.0 ✓ | 99 | 118 ✓ | 1500 | 并发能力差异化 |
| PASS | `S04_quota_failover` | 840 | 100.0 ✓ | 99 | 244 ✓ | 1500 | 配额耗尽与恢复 |
| PASS | `S05_quality_penalty` | 852 | 100.0 ✓ | 97 | 73 ✓ | 2500 | 延迟/质量降权 |
| PASS | `S06_mixed_fault` | 812 | 100.0 ✓ | 94 | 282 ✓ | 2500 | 混合故障韧性 |
| PASS | `S07_peak_dispatch` | 7810 | 100.0 ✓ | 99 | 1852 ✓ | 2500 | 高峰动态调度 |
| PASS | `S08_sticky` | 840 | 100.0 ✓ | 95 | 99 ✓ | 1500 | Sticky 连续性 |
| PASS | `S09_streaming` | 850 | 100.0 ✓ | 90 | 90 ✓ | 1500 | 流式 SSE |
| PASS | `S10_long_prompt` | 827 | 100.0 ✓ | 92 | 204 ✓ | 2500 | 长 Prompt |
| PASS | `S11_quota_recovery` | 2609 | 100.0 ✓ | 99 | 169 ✓ | 1500 | 周期性配额恢复 |
| FAIL | `S12_comprehensive` | 6462 | 100.0 ✓ | 98 | 4017 ✗ | 2500 | 全场景综合压测 |
| PASS | `S12_post_recovery` | 4279 | 100.0 ✓ | 98 | 636 ✓ | 2500 | 全场景综合压测 (恢复) |
| PASS | `S13_no_candidate` | 565 | 0.0 ✓ | 0 | 2162 ✓ | 5000 | 无可用节点  (预期失败) |
| PASS | `S14_model_not_found` | 872 | 0.0 ✓ | 0 | 84 ✓ | 100 | 模型不存在  (预期失败) |
| FAIL | `S15_cross_group_failover` | 866 | 69.1 ✗ | 99 | 100 ✓ | 3000 | 跨组故障迁移 |
| FAIL | `S16_after_recharge` | 440 | 0.0 ✗ | 99 | 61 ✓ | 2500 | 配额快速恢复 (后) |
| FAIL | `S16_before_recharge` | 440 | 0.0 ✗ | 99 | 68 ✓ | 2500 | 配额快速恢复 (前) |
| ⚠️ | `S17_stream_continuation` | 551 | _ | _ | _ | _ | S17_stream_continuation (no gate defined) |
| ⚠️ | `S18_null_handling` | 5 | _ | _ | _ | _ | S18_null_handling (no gate defined) |
| ⚠️ | `S19_tenant_isolation` | 4 | _ | _ | _ | _ | S19_tenant_isolation (no gate defined) |

## 失败场景详情

### ❌ S12_comprehensive — 全场景综合压测
- 成功率: 100.0%  (目标 98%, 通过)
- P99:    4017ms  (目标 2500ms, 不通过)
- 原始数据: `docs/全方面测试/results/S12_comprehensive.json`

### ❌ S15_cross_group_failover — 跨组故障迁移
- 成功率: 69.1%  (目标 99%, 不通过)
- P99:    100ms  (目标 3000ms, 通过)
- 原始数据: `docs/全方面测试/results/S15_cross_group_failover.json`

### ❌ S16_after_recharge — 配额快速恢复 (后)
- 成功率: 0.0%  (目标 99%, 不通过)
- P99:    61ms  (目标 2500ms, 通过)
- 原始数据: `docs/全方面测试/results/S16_after_recharge.json`

### ❌ S16_before_recharge — 配额快速恢复 (前)
- 成功率: 0.0%  (目标 99%, 不通过)
- P99:    68ms  (目标 2500ms, 通过)
- 原始数据: `docs/全方面测试/results/S16_before_recharge.json`


## 故障模式覆盖矩阵

| 场景 | 验证的故障模式 |
|---|---|
| S04/S11 | 429 quota_exceeded |
| S05 | slow upstream (2-4s 延迟) |
| S06 | slow + flaky + server_error + rate_limited 同发 |
| S07 | tier-3 高峰启用 |
| S09 | broken_stream (SSE 写一半断流) |
| S10 | context_length_exceeded |
| S11/S16 | quota 短窗口 → 恢复 |
| S12 | 15 model × 150 client × 6 min |
| S13 | 全部供应商故障 → no_candidate |
| S14 | model_not_found |
| S15 | 跨组故障迁移 |

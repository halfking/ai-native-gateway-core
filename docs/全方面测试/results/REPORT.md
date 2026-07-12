# LLM Gateway 全场景测试报告

- 生成时间: 2026-07-12T17:52:06+08:00
- 测试结果目录: `results`
- 总场景数: 14  通过: 3  失败: 11

## 总体状态

🟡 总通过率：3/14

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| FAIL | `S01_baseline` | 243 | 100.0 ✓ | 99 | 3884 ✗ | 1500 | 基准性能 |
| FAIL | `S02_cost_route` | 376 | 100.0 ✓ | 99 | 3859 ✗ | 1500 | 成本优化路由 |
| FAIL | `S03_concurrency_diff` | 338 | 100.0 ✓ | 99 | 3786 ✗ | 1500 | 并发能力差异化 |
| FAIL | `S04_quota_failover` | 320 | 100.0 ✓ | 99 | 3820 ✗ | 1500 | 配额耗尽与恢复 |
| FAIL | `S05_quality_penalty` | 306 | 100.0 ✓ | 97 | 3907 ✗ | 2500 | 延迟/质量降权 |
| FAIL | `S06_mixed_fault` | 321 | 100.0 ✓ | 94 | 3802 ✗ | 2500 | 混合故障韧性 |
| FAIL | `S08_sticky` | 287 | 100.0 ✓ | 95 | 3800 ✗ | 1500 | Sticky 连续性 |
| PASS | `S09_streaming` | 844 | 100.0 ✓ | 90 | 515 ✓ | 1500 | 流式 SSE |
| FAIL | `S10_long_prompt` | 328 | 100.0 ✓ | 92 | 3966 ✗ | 2500 | 长 Prompt |
| PASS | `S13_no_candidate` | 252 | 0.0 ✓ | 0 | 4583 ✓ | 5000 | 无可用节点  (预期失败) |
| PASS | `S14_model_not_found` | 860 | 0.0 ✓ | 0 | 52 ✓ | 100 | 模型不存在  (预期失败) |
| FAIL | `S15_cross_group_failover` | 264 | 83.0 ✗ | 99 | 3997 ✗ | 3000 | 跨组故障迁移 |
| FAIL | `S16_precharge_w1` | 289 | 100.0 ✓ | 99 | 3975 ✗ | 2500 | S16 wave1 (pre-charge) |
| FAIL | `S16_recovery_w2` | 268 | 100.0 ✓ | 99 | 3894 ✗ | 2500 | S16 wave2 (recovery) |

## 失败场景详情

### ❌ S01_baseline — 基准性能
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3884ms  (目标 1500ms, 不通过)
- 原始数据: `results/S01_baseline.json`

### ❌ S02_cost_route — 成本优化路由
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3859ms  (目标 1500ms, 不通过)
- 原始数据: `results/S02_cost_route.json`

### ❌ S03_concurrency_diff — 并发能力差异化
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3786ms  (目标 1500ms, 不通过)
- 原始数据: `results/S03_concurrency_diff.json`

### ❌ S04_quota_failover — 配额耗尽与恢复
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3820ms  (目标 1500ms, 不通过)
- 原始数据: `results/S04_quota_failover.json`

### ❌ S05_quality_penalty — 延迟/质量降权
- 成功率: 100.0%  (目标 97%, 通过)
- P99:    3907ms  (目标 2500ms, 不通过)
- 原始数据: `results/S05_quality_penalty.json`

### ❌ S06_mixed_fault — 混合故障韧性
- 成功率: 100.0%  (目标 94%, 通过)
- P99:    3802ms  (目标 2500ms, 不通过)
- 原始数据: `results/S06_mixed_fault.json`

### ❌ S08_sticky — Sticky 连续性
- 成功率: 100.0%  (目标 95%, 通过)
- P99:    3800ms  (目标 1500ms, 不通过)
- 原始数据: `results/S08_sticky.json`

### ❌ S10_long_prompt — 长 Prompt
- 成功率: 100.0%  (目标 92%, 通过)
- P99:    3966ms  (目标 2500ms, 不通过)
- 原始数据: `results/S10_long_prompt.json`

### ❌ S15_cross_group_failover — 跨组故障迁移
- 成功率: 83.0%  (目标 99%, 不通过)
- P99:    3997ms  (目标 3000ms, 不通过)
- 原始数据: `results/S15_cross_group_failover.json`

### ❌ S16_precharge_w1 — S16 wave1 (pre-charge)
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3975ms  (目标 2500ms, 不通过)
- 原始数据: `results/S16_precharge_w1.json`

### ❌ S16_recovery_w2 — S16 wave2 (recovery)
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3894ms  (目标 2500ms, 不通过)
- 原始数据: `results/S16_recovery_w2.json`


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

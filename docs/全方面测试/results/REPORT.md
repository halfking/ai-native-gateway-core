# LLM Gateway 全场景测试报告

- 生成时间: 2026-07-12T17:29:27+08:00
- 测试结果目录: `results`
- 总场景数: 14  通过: 2  失败: 12

## 总体状态

🟡 总通过率：2/14

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| FAIL | `S01_baseline` | 263 | 100.0 ✓ | 99 | 3926 ✗ | 1500 | 基准性能 |
| FAIL | `S02_cost_route` | 334 | 100.0 ✓ | 99 | 3932 ✗ | 1500 | 成本优化路由 |
| FAIL | `S03_concurrency_diff` | 308 | 100.0 ✓ | 99 | 3965 ✗ | 1500 | 并发能力差异化 |
| FAIL | `S04_quota_failover` | 358 | 100.0 ✓ | 99 | 3939 ✗ | 1500 | 配额耗尽与恢复 |
| FAIL | `S05_quality_penalty` | 348 | 100.0 ✓ | 97 | 3812 ✗ | 2500 | 延迟/质量降权 |
| FAIL | `S06_mixed_fault` | 289 | 100.0 ✓ | 94 | 3895 ✗ | 2500 | 混合故障韧性 |
| FAIL | `S08_sticky` | 259 | 100.0 ✓ | 95 | 4180 ✗ | 1500 | Sticky 连续性 |
| PASS | `S09_streaming` | 829 | 100.0 ✓ | 90 | 515 ✓ | 1500 | 流式 SSE |
| FAIL | `S10_long_prompt` | 305 | 100.0 ✓ | 92 | 3954 ✗ | 2500 | 长 Prompt |
| FAIL | `S13_no_candidate` | 333 | 0.0 ✓ | 0 | 5087 ✗ | 5000 | 无可用节点  (预期失败) |
| PASS | `S14_model_not_found` | 870 | 0.0 ✓ | 0 | 58 ✓ | 100 | 模型不存在  (预期失败) |
| FAIL | `S15_cross_group_failover` | 291 | 100.0 ✓ | 99 | 4011 ✗ | 3000 | 跨组故障迁移 |
| FAIL | `S16_precharge_w1` | 306 | 100.0 ✓ | 99 | 3908 ✗ | 2500 | S16 wave1 (pre-charge) |
| FAIL | `S16_recovery_w2` | 328 | 100.0 ✓ | 99 | 3849 ✗ | 2500 | S16 wave2 (recovery) |

## 失败场景详情

### ❌ S01_baseline — 基准性能
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3926ms  (目标 1500ms, 不通过)
- 原始数据: `results/S01_baseline.json`

### ❌ S02_cost_route — 成本优化路由
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3932ms  (目标 1500ms, 不通过)
- 原始数据: `results/S02_cost_route.json`

### ❌ S03_concurrency_diff — 并发能力差异化
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3965ms  (目标 1500ms, 不通过)
- 原始数据: `results/S03_concurrency_diff.json`

### ❌ S04_quota_failover — 配额耗尽与恢复
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3939ms  (目标 1500ms, 不通过)
- 原始数据: `results/S04_quota_failover.json`

### ❌ S05_quality_penalty — 延迟/质量降权
- 成功率: 100.0%  (目标 97%, 通过)
- P99:    3812ms  (目标 2500ms, 不通过)
- 原始数据: `results/S05_quality_penalty.json`

### ❌ S06_mixed_fault — 混合故障韧性
- 成功率: 100.0%  (目标 94%, 通过)
- P99:    3895ms  (目标 2500ms, 不通过)
- 原始数据: `results/S06_mixed_fault.json`

### ❌ S08_sticky — Sticky 连续性
- 成功率: 100.0%  (目标 95%, 通过)
- P99:    4180ms  (目标 1500ms, 不通过)
- 原始数据: `results/S08_sticky.json`

### ❌ S10_long_prompt — 长 Prompt
- 成功率: 100.0%  (目标 92%, 通过)
- P99:    3954ms  (目标 2500ms, 不通过)
- 原始数据: `results/S10_long_prompt.json`

### ❌ S13_no_candidate — 无可用节点
- 成功率: 0.0%  (目标 0%, 通过)
- P99:    5087ms  (目标 5000ms, 不通过)
- 原始数据: `results/S13_no_candidate.json`

### ❌ S15_cross_group_failover — 跨组故障迁移
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    4011ms  (目标 3000ms, 不通过)
- 原始数据: `results/S15_cross_group_failover.json`

### ❌ S16_precharge_w1 — S16 wave1 (pre-charge)
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3908ms  (目标 2500ms, 不通过)
- 原始数据: `results/S16_precharge_w1.json`

### ❌ S16_recovery_w2 — S16 wave2 (recovery)
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3849ms  (目标 2500ms, 不通过)
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

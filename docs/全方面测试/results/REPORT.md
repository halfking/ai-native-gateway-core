# LLM Gateway 全场景测试报告

- 生成时间: 2026-07-14T15:49:15+08:00
- 测试结果目录: `docs/全方面测试/results`
- 总场景数: 18  通过: 10  失败: 8

## 总体状态

🟡 总通过率：10/18

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| PASS | `S01_baseline` | 857 | 100.0 ✓ | 99 | 178 ✓ | 1500 | 基准性能 |
| FAIL | `S02_cost_route` | 429 | 100.0 ✓ | 99 | 3952 ✗ | 1500 | 成本优化路由 |
| FAIL | `S03_concurrency_diff` | 338 | 100.0 ✓ | 99 | 3786 ✗ | 1500 | 并发能力差异化 |
| PASS | `S04_quota_failover` | 860 | 100.0 ✓ | 99 | 47 ✓ | 1500 | 配额耗尽与恢复 |
| PASS | `S05_quality_penalty` | 860 | 100.0 ✓ | 97 | 45 ✓ | 2500 | 延迟/质量降权 |
| PASS | `S06_mixed_fault` | 860 | 100.0 ✓ | 94 | 46 ✓ | 2500 | 混合故障韧性 |
| FAIL | `S07_peak_dispatch` | 21679 | 100.0 ✓ | 99 | 7566 ✗ | 2500 | 高峰动态调度 |
| PASS | `S08_sticky` | 856 | 100.0 ✓ | 95 | 74 ✓ | 1500 | Sticky 连续性 |
| PASS | `S09_streaming` | 845 | 100.0 ✓ | 90 | 97 ✓ | 1500 | 流式 SSE |
| PASS | `S10_long_prompt` | 841 | 100.0 ✓ | 92 | 48 ✓ | 2500 | 长 Prompt |
| PASS | `S11_quota_recovery` | 4388 | 100.0 ✓ | 99 | 134 ✓ | 1500 | 周期性配额恢复 |
| FAIL | `S12_comprehensive` | 44208 | 100.0 ✓ | 98 | 7305 ✗ | 2500 | 全场景综合压测 |
| FAIL | `S12_post_recovery` | 6728 | 100.0 ✓ | 98 | 6474 ✗ | 2500 | 全场景综合压测 (恢复) |
| PASS | `S13_no_candidate` | 276 | 0.0 ✓ | 0 | 4554 ✓ | 5000 | 无可用节点  (预期失败) |
| PASS | `S14_model_not_found` | 884 | 0.0 ✓ | 0 | 19 ✓ | 100 | 模型不存在  (预期失败) |
| FAIL | `S15_cross_group_failover` | 378 | 88.4 ✗ | 99 | 3790 ✗ | 3000 | 跨组故障迁移 |
| FAIL | `S16_after_recharge` | 451 | 67.6 ✗ | 99 | 1027 ✓ | 2500 | 配额快速恢复 (后) |
| FAIL | `S16_before_recharge` | 434 | 65.2 ✗ | 99 | 1030 ✓ | 2500 | 配额快速恢复 (前) |

## 失败场景详情

### ❌ S02_cost_route — 成本优化路由
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3952ms  (目标 1500ms, 不通过)
- 原始数据: `docs/全方面测试/results/S02_cost_route.json`

### ❌ S03_concurrency_diff — 并发能力差异化
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    3786ms  (目标 1500ms, 不通过)
- 原始数据: `docs/全方面测试/results/S03_concurrency_diff.json`

### ❌ S07_peak_dispatch — 高峰动态调度
- 成功率: 100.0%  (目标 99%, 通过)
- P99:    7566ms  (目标 2500ms, 不通过)
- 原始数据: `docs/全方面测试/results/S07_peak_dispatch.json`

### ❌ S12_comprehensive — 全场景综合压测
- 成功率: 100.0%  (目标 98%, 通过)
- P99:    7305ms  (目标 2500ms, 不通过)
- 原始数据: `docs/全方面测试/results/S12_comprehensive.json`

### ❌ S12_post_recovery — 全场景综合压测 (恢复)
- 成功率: 100.0%  (目标 98%, 通过)
- P99:    6474ms  (目标 2500ms, 不通过)
- 原始数据: `docs/全方面测试/results/S12_post_recovery.json`

### ❌ S15_cross_group_failover — 跨组故障迁移
- 成功率: 88.4%  (目标 99%, 不通过)
- P99:    3790ms  (目标 3000ms, 不通过)
- 原始数据: `docs/全方面测试/results/S15_cross_group_failover.json`

### ❌ S16_after_recharge — 配额快速恢复 (后)
- 成功率: 67.6%  (目标 99%, 不通过)
- P99:    1027ms  (目标 2500ms, 通过)
- 原始数据: `docs/全方面测试/results/S16_after_recharge.json`

### ❌ S16_before_recharge — 配额快速恢复 (前)
- 成功率: 65.2%  (目标 99%, 不通过)
- P99:    1030ms  (目标 2500ms, 通过)
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

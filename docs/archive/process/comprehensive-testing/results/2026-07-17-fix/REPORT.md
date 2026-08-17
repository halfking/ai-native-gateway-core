# LLM Gateway 全场景测试报告

- 生成时间: 2026-07-17T17:22:56+08:00
- 测试结果目录: `docs/全方面测试/results/2026-07-17-fix`
- 总场景数: 7  通过: 5  失败: 1

## 总体状态

🟡 总通过率：5/7

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| PASS | `S01_baseline` | 790 | 100.0 ✓ | 99 | 75 ✓ | 1500 | 基准性能 |
| PASS | `S02_cost_route` | 780 | 100.0 ✓ | 99 | 181 ✓ | 1500 | 成本优化路由 |
| PASS | `S03_concurrency_diff` | 790 | 100.0 ✓ | 99 | 118 ✓ | 1500 | 并发能力差异化 |
| PASS | `S04_quota_failover` | 816 | 100.0 ✓ | 99 | 121 ✓ | 1500 | 配额耗尽与恢复 |
| PASS | `S05_quality_penalty` | 749 | 100.0 ✓ | 97 | 2378 ✓ | 2500 | 延迟/质量降权 |
| FAIL | `S06_mixed_fault` | 685 | 100.0 ✓ | 94 | 3233 ✗ | 2500 | 混合故障韧性 |
| ⚠️ | `multimodal-S17-S24` | 0 | _ | _ | _ | _ | multimodal-S17-S24 (no gate defined) |

## 失败场景详情

### ❌ S06_mixed_fault — 混合故障韧性
- 成功率: 100.0%  (目标 94%, 通过)
- P99:    3233ms  (目标 2500ms, 不通过)
- 原始数据: `docs/全方面测试/results/2026-07-17-fix/S06_mixed_fault.json`


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

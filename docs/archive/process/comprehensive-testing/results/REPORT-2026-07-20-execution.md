# LLM Gateway 全场景测试报告

- 生成时间: 2026-07-20T09:02:40+08:00
- 测试结果目录: `./results`
- 总场景数: 21  通过: 18  失败: 3

## 总体状态

🟡 总通过率：18/21

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| PASS | `S01_baseline` | 850 | 100.0 ✓ | 99 | 235 ✓ | 1500 | 基准性能 |
| PASS | `S02_cost_route` | 836 | 100.0 ✓ | 99 | 268 ✓ | 1500 | 成本优化路由 |
| PASS | `S03_concurrency_diff` | 852 | 100.0 ✓ | 99 | 254 ✓ | 1500 | 并发能力差异化 |
| PASS | `S04_quota_failover` | 830 | 100.0 ✓ | 99 | 247 ✓ | 1500 | 配额耗尽与恢复 |
| PASS | `S05_quality_penalty` | 529 | 100.0 ✓ | 97 | 3870 ✓ | 5000 | 延迟/质量降权 (G组2-4s注入) |
| PASS | `S06_mixed_fault` | 502 | 100.0 ✓ | 94 | 3802 ✓ | 5000 | 混合故障韧性 (G慢/J抖/B错同发) |
| FAIL | `S07_peak_dispatch` | 2453 | 95.1 ✗ | 99 | 10547 ✗ | 2500 | 高峰动态调度 |
| PASS | `S08_sticky` | 841 | 100.0 ✓ | 95 | 239 ✓ | 1500 | Sticky 连续性 |
| PASS | `S09_streaming` | 855 | 100.0 ✓ | 90 | 45 ✓ | 1500 | 流式 SSE |
| PASS | `S10_long_prompt` | 815 | 100.0 ✓ | 92 | 527 ✓ | 2500 | 长 Prompt |
| PASS | `S11_quota_recovery` | 2737 | 99.4 ✓ | 99 | 165 ✓ | 1500 | 周期性配额恢复 |
| FAIL | `S12_comprehensive` | 2826 | 96.7 ✗ | 98 | 10534 ✗ | 5000 | 全场景综合压测 (G组2-4s注入) |
| FAIL | `S12_post_recovery` | 2126 | 95.5 ✗ | 98 | 8306 ✗ | 2500 | 全场景综合压测 (恢复) |
| PASS | `S13_no_candidate` | 441 | 0.0 ✓ | 0 | 4968 ✓ | 5000 | 无可用节点  (预期失败) |
| PASS | `S14_model_not_found` | 870 | 0.0 ✓ | 0 | 207 ✓ | 100 | 模型不存在  (预期失败) |
| PASS | `S15_cross_group_failover` | 810 | 100.0 ✓ | 99 | 361 ✓ | 3000 | 跨组故障迁移 |
| PASS | `S16_after_recharge` | 428 | 100.0 ✓ | 99 | 195 ✓ | 2500 | 配额快速恢复 (后) |
| PASS | `S16_before_recharge` | 430 | 100.0 ✓ | 99 | 212 ✓ | 2500 | 配额快速恢复 (前) |
| ✅ | `S17_stream_continuation` | 547 | _ | _ | _ | _ | 流式断连续传 (按脚本 extra.pass 判定) (script pass=True) |
| ✅ | `S18_null_handling` | 5 | _ | _ | _ | _ | 空值/边界请求处理 (按脚本判定) (script pass=True) |
| ✅ | `S19_tenant_isolation` | 4 | _ | _ | _ | _ | 多租户隔离 (按脚本判定) (script pass=True) |

## 失败场景详情

### ❌ S07_peak_dispatch — 高峰动态调度
- 成功率: 95.1%  (目标 99%, 不通过)
- P99:    10547ms  (目标 2500ms, 不通过)
- 原始数据: `./results/S07_peak_dispatch.json`

### ❌ S12_comprehensive — 全场景综合压测 (G组2-4s注入)
- 成功率: 96.7%  (目标 98%, 不通过)
- P99:    10534ms  (目标 5000ms, 不通过)
- 原始数据: `./results/S12_comprehensive.json`

### ❌ S12_post_recovery — 全场景综合压测 (恢复)
- 成功率: 95.5%  (目标 98%, 不通过)
- P99:    8306ms  (目标 2500ms, 不通过)
- 原始数据: `./results/S12_post_recovery.json`


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

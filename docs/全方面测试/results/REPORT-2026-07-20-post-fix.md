# LLM Gateway 全场景测试报告

- 生成时间: 2026-07-20T07:23:24+08:00
- 测试结果目录: `docs/全方面测试/results`
- 总场景数: 21  通过: 21  失败: 0

## 总体状态

🟢 总通过率：21/21

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| PASS | `S01_baseline` | 840 | 100.0 ✓ | 99 | 271 ✓ | 1500 | 基准性能 |
| PASS | `S02_cost_route` | 818 | 100.0 ✓ | 99 | 267 ✓ | 1500 | 成本优化路由 |
| PASS | `S03_concurrency_diff` | 821 | 100.0 ✓ | 99 | 272 ✓ | 1500 | 并发能力差异化 |
| PASS | `S04_quota_failover` | 830 | 100.0 ✓ | 99 | 243 ✓ | 1500 | 配额耗尽与恢复 |
| PASS | `S05_quality_penalty` | 528 | 100.0 ✓ | 97 | 3852 ✓ | 5000 | 延迟/质量降权 (G组2-4s注入) |
| PASS | `S06_mixed_fault` | 465 | 100.0 ✓ | 94 | 3912 ✓ | 5000 | 混合故障韧性 (G慢/J抖/B错同发) |
| PASS | `S07_peak_dispatch` | 7021 | 100.0 ✓ | 99 | 1118 ✓ | 2500 | 高峰动态调度 |
| PASS | `S08_sticky` | 854 | 100.0 ✓ | 95 | 49 ✓ | 1500 | Sticky 连续性 |
| PASS | `S09_streaming` | 843 | 100.0 ✓ | 90 | 64 ✓ | 1500 | 流式 SSE |
| PASS | `S10_long_prompt` | 740 | 100.0 ✓ | 92 | 1027 ✓ | 2500 | 长 Prompt |
| PASS | `S11_quota_recovery` | 2579 | 100.0 ✓ | 99 | 331 ✓ | 1500 | 周期性配额恢复 |
| PASS | `S12_comprehensive` | 4866 | 100.0 ✓ | 98 | 4340 ✓ | 5000 | 全场景综合压测 (G组2-4s注入) |
| PASS | `S12_post_recovery` | 2962 | 100.0 ✓ | 98 | 1206 ✓ | 2500 | 全场景综合压测 (恢复) |
| PASS | `S13_no_candidate` | 243 | 0.0 ✓ | 0 | 9798 ✓ | 5000 | 无可用节点  (预期失败) |
| PASS | `S14_model_not_found` | 870 | 0.0 ✓ | 0 | 222 ✓ | 100 | 模型不存在  (预期失败) |
| PASS | `S15_cross_group_failover` | 743 | 100.0 ✓ | 99 | 673 ✓ | 3000 | 跨组故障迁移 |
| PASS | `S16_after_recharge` | 428 | 100.0 ✓ | 99 | 220 ✓ | 2500 | 配额快速恢复 (后) |
| PASS | `S16_before_recharge` | 426 | 100.0 ✓ | 99 | 91 ✓ | 2500 | 配额快速恢复 (前) |
| ✅ | `S17_stream_continuation` | 536 | _ | _ | _ | _ | 流式断连续传 (按脚本 extra.pass 判定) (script pass=True) |
| ✅ | `S18_null_handling` | 5 | _ | _ | _ | _ | 空值/边界请求处理 (按脚本判定) (script pass=True) |
| ✅ | `S19_tenant_isolation` | 4 | _ | _ | _ | _ | 多租户隔离 (按脚本判定) (script pass=True) |

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

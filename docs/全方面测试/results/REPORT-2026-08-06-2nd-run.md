# LLM Gateway 全场景测试报告

- 生成时间: 2026-08-06T00:20:55+08:00
- 测试结果目录: `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3/docs/全方面测试/results`
- 总场景数: 33  通过: 21  失败: 0

## 总体状态

🟢 总通过率：21/33

## 各场景验收详情

| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |
|---|---|---|---|---|---|---|---|
| PASS | `S01_baseline` | 796 | 100.0 ✓ | 99 | 326 ✓ | 1500 | 基准性能 |
| PASS | `S02_cost_route` | 791 | 100.0 ✓ | 99 | 349 ✓ | 1500 | 成本优化路由 |
| PASS | `S03_concurrency_diff` | 810 | 100.0 ✓ | 99 | 324 ✓ | 1500 | 并发能力差异化 |
| PASS | `S04_quota_failover` | 806 | 100.0 ✓ | 99 | 328 ✓ | 1500 | 配额耗尽与恢复 |
| PASS | `S05_quality_penalty` | 711 | 100.0 ✓ | 97 | 2712 ✓ | 5000 | 延迟/质量降权 (G组2-4s注入) |
| PASS | `S06_mixed_fault` | 710 | 100.0 ✓ | 94 | 2712 ✓ | 5000 | 混合故障韧性 (G慢/J抖/B错同发) |
| PASS | `S07_peak_dispatch` | 5874 | 100.0 ✓ | 99 | 1024 ✓ | 2500 | 高峰动态调度 |
| PASS | `S08_sticky` | 784 | 100.0 ✓ | 95 | 375 ✓ | 1500 | Sticky 连续性 |
| PASS | `S09_streaming` | 789 | 100.0 ✓ | 90 | 405 ✓ | 1500 | 流式 SSE |
| ⚠️ | `S09_streaming_full` | 5294 | _ | _ | _ | _ | S09_streaming_full (no gate defined) |
| PASS | `S10_long_prompt` | 747 | 100.0 ✓ | 92 | 552 ✓ | 2500 | 长 Prompt |
| PASS | `S11_quota_recovery` | 2026 | 100.0 ✓ | 99 | 393 ✓ | 1500 | 周期性配额恢复 |
| PASS | `S12_comprehensive` | 2621 | 100.0 ✓ | 98 | 4320 ✓ | 5000 | 全场景综合压测 (G组2-4s注入) |
| PASS | `S12_post_recovery` | 2299 | 100.0 ✓ | 98 | 1617 ✓ | 2500 | 全场景综合压测 (恢复) |
| PASS | `S13_no_candidate` | 248 | 0.0 ✓ | 0 | 11804 ✓ | 5000 | 无可用节点  (预期失败) |
| PASS | `S14_model_not_found` | 840 | 0.0 ✓ | 0 | 302 ✓ | 100 | 模型不存在  (预期失败) |
| PASS | `S15_cross_group_failover` | 744 | 100.0 ✓ | 99 | 418 ✓ | 3000 | 跨组故障迁移 |
| PASS | `S16_after_recharge` | 406 | 100.0 ✓ | 99 | 99 ✓ | 2500 | 配额快速恢复 (后) |
| PASS | `S16_before_recharge` | 384 | 100.0 ✓ | 99 | 332 ✓ | 2500 | 配额快速恢复 (前) |
| ✅ | `S17_stream_continuation` | 527 | _ | _ | _ | _ | 流式断连续传 (按脚本 extra.pass 判定) (script pass=True) |
| ✅ | `S18_null_handling` | 5 | _ | _ | _ | _ | 空值/边界请求处理 (按脚本判定) (script pass=True) |
| ✅ | `S19_tenant_isolation` | 4 | _ | _ | _ | _ | 多租户隔离 (按脚本判定) (script pass=True) |
| ⚠️ | `S20_multi_supplier` | 1568 | _ | _ | _ | _ | S20_multi_supplier (no gate defined) |
| ⚠️ | `S22_long_context` | 2141 | _ | _ | _ | _ | S22_long_context (no gate defined) |
| ⚠️ | `S25_stress_200` | 11227 | _ | _ | _ | _ | S25_stress_200 (no gate defined) |
| ⚠️ | `S26_redis_bloat` | 12254 | _ | _ | _ | _ | S26_redis_bloat (no gate defined) |
| ⚠️ | `S27_mixed_fault_stress` | 6224 | _ | _ | _ | _ | S27_mixed_fault_stress (no gate defined) |
| ⚠️ | `S34_stability_5min` | 29210 | _ | _ | _ | _ | S34_stability_5min (no gate defined) |
| ⚠️ | `T1_fault_aggregation` | 5733 | _ | _ | _ | _ | T1_fault_aggregation (no gate defined) |
| ⚠️ | `T2_baseline` | 2356 | _ | _ | _ | _ | T2_baseline (no gate defined) |
| ⚠️ | `T2_slow_injection` | 4302 | _ | _ | _ | _ | T2_slow_injection (no gate defined) |
| ⚠️ | `T4_group_isolation` | 6867 | _ | _ | _ | _ | T4_group_isolation (no gate defined) |
| ⚠️ | `s60-network-test-20260724` | 0 | _ | _ | _ | _ | s60-network-test-20260724 (no gate defined) |

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

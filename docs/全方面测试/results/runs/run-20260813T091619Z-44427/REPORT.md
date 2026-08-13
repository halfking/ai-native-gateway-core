# LLM Gateway 严格测试报告

- 生成时间: 2026-08-13T09:29:41.468253+00:00
- 结果目录: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/docs/全方面测试/results/runs/run-20260813T091619Z-44427`
- 总体状态: **FAIL**
- 统计: BLOCKED_ENVIRONMENT=0, FAIL=10, INVALID=0, PASS=12, SKIPPED=1

## 场景明细

| 状态 | 场景 | 类别 | Run ID | 说明 |
|---|---|---|---|---|
| FAIL | `S01_baseline` | functional | `run-20260813T091619Z-44427` | p99_ms=1768 exceeds 1500ms; p99_ms=1768 exceeds 1500ms |
| PASS | `S02_cost_route` | functional | `run-20260813T091619Z-44427` |  |
| PASS | `S03_concurrency_diff` | functional | `run-20260813T091619Z-44427` |  |
| PASS | `S04_quota_failover` | functional | `run-20260813T091619Z-44427` |  |
| PASS | `S05_quality_penalty` | functional | `run-20260813T091619Z-44427` |  |
| PASS | `S06_mixed_fault` | functional | `run-20260813T091619Z-44427` |  |
| FAIL | `S07_peak_dispatch` | functional | `run-20260813T091619Z-44427` | p99_ms=7415 exceeds 2500ms; p99_ms=7415 exceeds 2500ms |
| PASS | `S08_sticky` | functional | `run-20260813T091619Z-44427` |  |
| PASS | `S09_streaming` | functional | `run-20260813T091619Z-44427` |  |
| PASS | `S10_long_prompt` | functional | `run-20260813T091619Z-44427` |  |
| FAIL | `S11_quota_recovery` | functional | `run-20260813T091619Z-44427` | success_rate=0.0% below 99%; p99_ms=999999 exceeds 1500ms; success_rate=0.0% below 99%; p99_ms=999999 exceeds 1500ms |
| FAIL | `S12_comprehensive` | functional | `run-20260813T091619Z-44427` | p99_ms=5990 exceeds 5000ms; p99_ms=5990 exceeds 5000ms |
| PASS | `S13_no_candidate` | functional | `run-20260813T091619Z-44427` |  |
| FAIL | `S14_model_not_found` | functional | `run-20260813T091619Z-44427` | p99_ms=338 exceeds 100ms; p99_ms=338 exceeds 100ms |
| PASS | `S15_cross_group_failover` | functional | `run-20260813T091619Z-44427` |  |
| PASS | `S16_quick_recovery` | functional | `run-20260813T091619Z-44427` | quick recovery phases passed |
| PASS | `S17_stream_continuation` | functional | `run-20260813T091619Z-44427` |  |
| FAIL | `S18_null_handling` | functional | `run-20260813T091619Z-44427` | legacy output contains failed or non-boolean checks |
| FAIL | `S19_tenant_isolation` | functional | `run-20260813T091619Z-44427` | legacy output contains failed or non-boolean checks |
| FAIL | `S20_auto_title` | functional | `run-20260813T091619Z-44427` | legacy output contains failed or non-boolean checks |
| SKIPPED | `S21_branch_session` | functional | `run-20260813T091619Z-44427` |  |
| FAIL | `S22_instant_summary` | functional | `run-20260813T091619Z-44427` | scenario script exited with 1 |
| FAIL | `S23_long_text_chunked` | functional | `run-20260813T091619Z-44427` | scenario script exited with 1 |

## 验收阻断原因

- `S01_baseline`: FAIL — p99_ms=1768 exceeds 1500ms; p99_ms=1768 exceeds 1500ms
- `S07_peak_dispatch`: FAIL — p99_ms=7415 exceeds 2500ms; p99_ms=7415 exceeds 2500ms
- `S11_quota_recovery`: FAIL — success_rate=0.0% below 99%; p99_ms=999999 exceeds 1500ms; success_rate=0.0% below 99%; p99_ms=999999 exceeds 1500ms
- `S12_comprehensive`: FAIL — p99_ms=5990 exceeds 5000ms; p99_ms=5990 exceeds 5000ms
- `S14_model_not_found`: FAIL — p99_ms=338 exceeds 100ms; p99_ms=338 exceeds 100ms
- `S18_null_handling`: FAIL — legacy output contains failed or non-boolean checks
- `S19_tenant_isolation`: FAIL — legacy output contains failed or non-boolean checks
- `S20_auto_title`: FAIL — legacy output contains failed or non-boolean checks
- `S21_branch_session`: SKIPPED — 
- `S22_instant_summary`: FAIL — scenario script exited with 1
- `S23_long_text_chunked`: FAIL — scenario script exited with 1

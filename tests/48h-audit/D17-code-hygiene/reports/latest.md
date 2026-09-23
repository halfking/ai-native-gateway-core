# R56 · D17 代码卫生 + R55 修复面验证 · 48h 审计结论

> 时间：2026-09-23 · 执行：主代理+D17 子代理（多 bash 实跑）

## R55 修复面实跑（反思纪律落实：多 bash 版本 subshell 回归）
- project_env_test：bash 5.3.9 ✓ / bash 3.2.57 ✓（zsh 报 BASH_SOURCE 属预期，shebang bash 专属）
- deploy_local_contract_test：25 PASS / 0 FAIL / RC=0（duplicate-release SSOT 修复实证）
- test-deploy-local-env：8/8（此前 6/8：测试 4 由本轮 R55-D2 裁决闭合、测试 6 上游已修）
- DL_PG_PREFLIGHT_REQUIRED=1（80c74af01）：语义核实=默认 fail-closed 防死区，豁免=显式置 0

## 发现与处置
| 级别 | 项 | 处置 |
|---|---|---|
| P2 | R55-D2 多行 PEM 静默截断 | ✅ 裁决 (b)：解析器"引号开同行未闭"→stderr+exit 2 响亮拒绝；测试 4 改期望；单行+字面 \n 约定落注释 |
| P3 | R55-D4 bump-version 解析失败 | ✅ 关闭（上游已修，测试 6 PASS） |
| P3 | R55-D5 procsub 风险 | 全仓 3 处 `<(...)` 逐一核验安全（长注释均在 procsub 外+显式防护注释） |
| P3 | 死代码：prompt_compress scratch pool/CompressedStats；geoIPSourceStatus 未接线；哑赋值；陈旧注释 | ✅ 本轮清理 |

## 卫生面
mock 身份声明完备（tests/stress 双 main.go 头注释）；48h-audit 三脚本 bash -n 双版本过；TODO 净增 1 处（模板占位）；窗口内无新增技术债标记。

## R57 追加（2026-09-23）
| 级别 | 项 | 处置 |
|---|---|---|
| P2 | minheap_topk 孤儿实锤收口：SelectTopKWeighted 零调用、SelectTopN 仍 O(N²) | ✅ WeightedRouter.SelectTopN 正式接线 SelectTopKWeighted（O(N log K)），平序语义保持，routing 全绿 |
| P3 | rollupVirtualIP 死代码（零调用，rollupDims 已含 virtual_ip） | ✅ 删除；dimQueries 提升包级变量 + 钉桩测试 |
| 登记 | chunk_buffer/error_detector_ring/prompt_compress 批次 2/3 | 评估后缓批：接线点非单一收口（SSE 语义 Flush 契约/路由 API 换型），需专门小轮+性能验证；见轮文档 §四 |

# P2 阶段4收尾 Handoff — 剩余工作与执行提示词

**日期**: 2026-09-07
**已完成**: P2.4阶段1-2（训练环境+基线，d2083c8e5）、P2.5（Go端ONNX集成+MLSelector，0a92c36bb）、
阶段4增量（A/B流量分配、模型热更新、ML诊断端点、词汇表对齐）
**本文档用途**: 剩余工作全部需要生产数据或线上环境，代码层面的工作已收尾。
每节含可直接拷贝给新会话的执行提示词。

> **⚠️ 2026-09-07 数据核查结论（任务1前置核查已完成）**：154/245/252 共用同一
> PG（172.16.2.210/llm_gateway，已逐一核实三台 DB URL），`auto_route_selections`
> 全链路（hot/分区/_all 视图）**0 行**，`training_human_annotations` 0 行，生产
> 6 天日志零 `model=auto` 流量（245 为 traffic-only 角色无写入路径）。任务1/2
> 前置条件当前不成立；导出工具链已在 245 实测健康（export 在空集守卫处按预期
> 失败）。数据到位后的完整执行步骤见
> [docs/ml/p2.4-real-data-evaluation.md](../ml/p2.4-real-data-evaluation.md) §5
> runbook，无需重做本轮排查。

---

## 前置状态速览

| 项 | 状态 |
|---|---|
| 训练管道 | `ml-training/`（Python，27测试），`python -m src.train --synthetic N` 可冒烟 |
| Go推理 | `routingopt/ml_*.go`（15测试），fixture在`routingopt/testdata/ml_fixture/` |
| 部署开关 | `ROUTING_OPT_ENABLED` + `ROUTING_ML_ENABLED` + `ROUTING_ML_MANIFEST_PATH`（默认全关） |
| A/B | `ROUTING_OPT_AB_TEST_ENABLED` + `ROUTING_OPT_AB_TEST_PERCENTAGE`（万分比确定性分桶） |
| 热更新 | `ROUTING_ML_RELOAD_SECONDS`（0=禁用） |
| 诊断 | `GET /api/admin/routing-opt/ml` |
| 本地ORT验证 | `ONNXRUNTIME_SHARED_LIBRARY_PATH` 指向 ORT 1.29.0 dylib/so（Go集成测试从Skip变PASS） |

---

## 任务1：生产数据导出与真实数据重训（优先级最高）

**前置**: 245/154环境可访问，`auto_route_selections`有足够数据（建议≥10万行，≥30天）。

```text
使用env-injector技能获取245环境数据库连接。执行：
1. 检查数据量：
   SELECT COUNT(*), MIN(ts), MAX(ts) FROM auto_route_selections
   WHERE feature_version='v1';
2. 在245上用llm-gw-exporter导出训练数据（full-v1配置，按llm-gateway-deploy技能的部署合同操作），
   产物Parquet拷贝到本地 ml-training/data/training_real.parquet
3. 若有P2.1人工标注，一并用llm-gw-annotator导出CSV到 ml-training/data/annotations_real.csv
4. 本地训练：
   cd ml-training && source .venv/bin/activate
   python -m src.train --data data/training_real.parquet \
     --annotations data/annotations_real.csv --output-dir models/real-v1
5. 检查 models/real-v1/evaluation_report.md：
   - 模型准确率 vs most_frequent/random基线
   - 特征重要性前5是否为路由敏感字段（profile/task_type/detected_language/复杂度类）
   - 若有人工标注：规则引擎基线准确率（标注行）与模型对比
6. 结论写入 docs/ml/p2.4-real-data-evaluation.md，明确"ML是否优于规则引擎"的离线结论
注意：只导出结构化特征字段（P2.2隐私契约），不要把Parquet提交进git（ml-training/.gitignore已排除data/）。
```

## 任务2：A/B测试上线（依赖任务1结论为正）

```text
在245/154部署环境执行A/B灰度：
1. 部署model.onnx+manifest.json到 /opt/llm-gateway/ml/（或环境约定路径），
   安装ONNX Runtime 1.29.0共享库（macOS arm64: onnxruntime-osx-arm64-1.29.0.tgz；
   Linux: onnxruntime-linux-x64-1.29.0.tgz 的 lib/libonnxruntime.so）
2. 环境变量（deploy-local.sh的env注入处）：
   ROUTING_OPT_ENABLED=true
   ROUTING_ML_ENABLED=true
   ROUTING_ML_MANIFEST_PATH=<模型目录>/manifest.json
   ROUTING_ML_ORT_LIB_PATH=<ORT库路径>
   ROUTING_ML_MIN_CONFIDENCE=0.6
   ROUTING_ML_RELOAD_SECONDS=30
   ROUTING_OPT_AB_TEST_ENABLED=true
   ROUTING_OPT_AB_TEST_PERCENTAGE=0.1
3. 重启后验证启动日志含 "routingopt: ML re-ranker enabled"（标签类目正确）；
   curl -s localhost:<admin端口>/api/admin/routing-opt/ml 确认enabled=true且stats在增长
4. 观察7天：treatment vs control组在annotation_stats/请求成功率上的差异
5. 逐步放量 0.1 → 0.25 → 0.5 → 1.0；任何阶段ML组指标劣化>2%即回退百分比或关ROUTING_ML_ENABLED
6. 结果记录到 docs/ml/p2-phase4-ab-report.md
```

## 任务3（可选）：admin ML诊断前端页

```text
参考 web/src/views/RoutingLogView.vue 的页面结构与 web/src/api/credential-monitor.ts 的API层封装，
新增ML诊断页：
- 路由 /ml-diagnostics（super admin），菜单项加到 web/src/config/appNav.ts 并重新生成
  web/public/menu-config.json（node web/scripts/export-menu-config.mjs）
- API: GET /api/admin/routing-opt/ml，展示 enabled/manifest标签类目/stats计数器/ab_test观测占比
- 自动刷新30s；完成后运行web构建验证
```

## 任务4（可选）：CI集成ORT

```text
在CI流水线中缓存安装ONNX Runtime 1.29.0共享库并导出ONNXRUNTIME_SHARED_LIBRARY_PATH，
使 routingopt 的 TestMLSelectorEndToEnd/TestMLRerankerStatsWithFixture/TestMLHotReload
从SKIP变为常态运行。参考：https://github.com/microsoft/onnxruntime/releases/tag/v1.29.0
```

---

## 技术要点备忘（新会话必读）

1. **不要修改** `installer/go.mod`、`scripts/injection-test/go.mod`、`tests/local/gomapper/go.mod`、
   各Dockerfile——toolchain 1.27.1对齐已由远程 02f3a07bd 完成，工作区应保持干净
2. ONNX导出必须 `zipmap=False`（ml-training/src/export_model.py已内置），
   否则Go端onnxruntime_go无法读取概率输出
3. 训练词汇表=运行时词汇表（synthetic.py已对齐：smart/speed_first/cost_first、
   v3_heuristic/llm等）；真实数据训练无需再做归一
4. A/B的control组在 `autoroute/optimizer_bridge.go` 的 recommendWithOptimizer 里
   通过可选接口 `EvaluateAB` 短路，改动gate逻辑看 `routingopt/ml_ab.go`
5. ML失败语义：五层降级（见 docs/ml/p2.5-go-onnx-inference.md §4.2），
   任何"ML异常导致路由失败"都是bug

# 路由 ML 灰度放量与模型回滚运行手册（ROUTING_ML_*）

**适用**：P2.5 Go 端 ONNX 路由重排序（MLSelector + MLReranker）
**代码依据**（本文所有开关名、默认值、日志关键字均取自代码，非规划稿）：

- `settings/routing_ml_flags.go`（ROUTING_ML_* 定义与默认值）
- `settings/routing_opt_feature_flags.go`（ROUTING_OPT_* 定义与默认值）
- `cmd/gateway/routing_optimizer_init.go`（启动装配与降级日志）
- `routingopt/ml_reranker.go`（Predict 失败/低置信度降级）
- `routingopt/ml_manifest.go`（manifest 契约校验）
- `docs/ml/p2.5-go-onnx-inference.md`（设计文档，§4.2 五层降级 / §5 部署 / §7 剩余工作）

---

## 1. 环境开关清单（以代码为准）

### 1.1 ROUTING_ML_*（P2.5 ML 重排序，`GetRoutingMLFlags()`）

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `ROUTING_ML_ENABLED` | `false` | ML 重排序总开关。**硬前置**：`ROUTING_OPT_ENABLED=true` 才会装配，否则日志提示 disabled 且无效果。 |
| `ROUTING_ML_MANIFEST_PATH` | `""`（空） | manifest.json 路径；`model.onnx` 相对该文件所在目录解析（`MLManifest.ModelPath`）。为空时启动 Warn 跳过 ML。 |
| `ROUTING_ML_ORT_LIB_PATH` | `""` | onnxruntime 共享库（1.29.0 C API）。为空时依次回退：`ONNXRUNTIME_SHARED_LIBRARY_PATH` → OS 默认搜索路径。 |
| `ROUTING_ML_MIN_CONFIDENCE` | `0.6` | 预测 `max(probability)` 低于该值时不干预、保持规则引擎顺序。代码注释建议 0.5–0.7，真实数据准确率验证前勿低于 0.6。 |
| `ROUTING_ML_INTRA_OP_THREADS` | `1` | 单次 ONNX 推理的 intra-op 线程上限（1 = 热路径安全，避免 CPU 争抢）。 |

### 1.2 ROUTING_OPT_*（P2.2 optimizer，`GetRoutingOptFlags()`，ML 的前置层）

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `ROUTING_OPT_ENABLED` | `false` | optimizer 插件总开关，ML 的硬前置。false 时整个 optimizer 不存在（回到 pre-P2.2 基线路由）。 |
| `ROUTING_OPT_CLASSIFICATION_ENHANCEMENT` | `true` | ⚠ 声明性子开关，当前仅在 init 日志中展示、**未贯穿 RealOptimizer 行为**（审计 F-3）。 |
| `ROUTING_OPT_MODEL_RECOMMENDATION` | `true` | ⚠ 同上。 |
| `ROUTING_OPT_FEEDBACK_INTEGRATION` | `true` | ⚠ 同上。 |
| `ROUTING_OPT_ADAPTIVE_LEARNING` | `false` | ⚠ 同上（`routingopt/learner.go` 注释：Week 1 禁用）。 |
| `ROUTING_OPT_MAX_PLUGIN_LATENCY_MS` | `10` | ⚠ 同上。 |
| `ROUTING_OPT_EXPLORATION_RATE` | `0.05` | ⚠ 同上。 |
| `ROUTING_OPT_AB_TEST_ENABLED` | `false` | ⚠ 声明性 A/B 开关，**未生效——不能用它做流量百分比灰度**（审计 F-3）。 |
| `ROUTING_OPT_AB_TEST_PERCENTAGE` | `0.1` | ⚠ 同上。 |

> **灰度能力现状（重要）**：能实际改变行为的开关只有两级总开关
> `ROUTING_OPT_ENABLED` / `ROUTING_ML_ENABLED`（进程级）。修复子开关贯穿
> 是独立工作项（审计 F-3），在修复前不要把 1.2 表中标 ⚠ 的开关当作灰度手段。

### 1.3 相关环境变量

| 环境变量 | 说明 |
|---|---|
| `ONNXRUNTIME_SHARED_LIBRARY_PATH` | ORT 共享库兜底路径（`routingopt/ml_selector.go`）。本地跑 `go test ./routingopt` 用它指定库路径。 |

---

## 2. 降级链（ML 永不破坏路由）

五层降级（p2.5 §4.2，逐层对应代码）：

| 层 | 条件 | 行为 | 日志关键字 |
|---|---|---|---|
| 1 | `ROUTING_OPT_ENABLED=false`（默认） | optimizer 不存在，零开销 | `autoroute: routing optimizer disabled` |
| 2 | `ROUTING_ML_ENABLED=false`（默认） | optimizer 在、无 ML | `routingopt: ML re-ranker disabled` |
| 3 | 共享库/模型/manifest 缺失或 init 失败；或 `ROUTING_ML_MANIFEST_PATH` 未设 | `attachMLReranker` Warn 后跳过，启动继续 | `ML enabled but ROUTING_ML_MANIFEST_PATH unset` / `ML re-ranker unavailable, rule-engine ordering stays active` |
| 4 | 单次 `Predict` 失败 | 该请求保持规则引擎原序 | `routingopt: ML predict failed, keeping rule-engine order` |
| 5 | `max(probability) < ROUTING_ML_MIN_CONFIDENCE` | 不干预，保持原序 | 无日志（静默） |

另有一层前置：`ROUTING_OPT_ENABLED=true` 但无 DB pool 时 optimizer 整体降级
（`autoroute: routing optimizer enabled but no DB pool`）。

ML 的干预面被进一步限制为：**只把预测命中的候选稳定提升到第一位**
（精确 → 前缀两轮匹配，忽略大小写），永不增删候选、不改分数
（`routingopt/ml_reranker.go`）。

---

## 3. 灰度放量步骤

### 3.0 前置条件

1. 模型产物就位：`model.onnx` + `manifest.json`（ml-training `python -m src.export_model` 产出），
   建议部署为版本化目录（见 §4.1），如 `/opt/llm-gateway/ml/<release>/`。
2. onnxruntime 共享库 1.29.0 就位，路径可被 `ROUTING_ML_ORT_LIB_PATH` 指到。
3. 部署前在预发或本机跑 Go 测试（真实 ONNX 推理）：
   ```bash
   export ONNXRUNTIME_SHARED_LIBRARY_PATH=/path/to/libonnxruntime.1.29.0.dylib
   go test ./routingopt/ -run TestMLSelector -v
   ```
   （库不可用时该测试自动 Skip，其余 manifest/匹配/降级用例始终运行。）

### 3.1 放量

1. **基线观察**：保持 `ROUTING_ML_ENABLED=false`，记录当前路由 P99 延迟、
   路由记录页（admin）的选择分布与 duration_ms，作为对照。
2. **单实例灰度**：选一个实例设置并重启（flags 仅启动时读取，改值必须重启进程）：
   ```bash
   export ROUTING_OPT_ENABLED=true
   export ROUTING_ML_ENABLED=true
   export ROUTING_ML_MANIFEST_PATH=/opt/llm-gateway/ml/<release>/manifest.json
   export ROUTING_ML_ORT_LIB_PATH=/opt/llm-gateway/ml/libonnxruntime.so
   export ROUTING_ML_MIN_CONFIDENCE=0.6
   # 可选：manifest 热加载周期（秒）。默认 0=禁用（模型/manifest 变更仍须重启）；
   # 设 >0 后 reranker 按该周期重读 manifest，manifest 内的模型文件变更即可
   # 不重启生效（R30 审计 M-4 补记，settings/routing_ml_flags.go）。
   export ROUTING_ML_RELOAD_SECONDS=0
   ```
3. **启动验证**（成功/失败都以启动日志为准）：
   - 期望：`routingopt: ML re-ranker enabled`（携带 manifest 路径、labels 类目、min_confidence）
   - 出现 §2 表中任一降级日志 → 按 §4 处置，不带问题放量。
4. **观察指标与阈值**（观察 24h 以上再扩量）：

   | 指标 | 观察手段 | 阈值 / 处置 |
   |---|---|---|
   | Predict 失败率 | 日志 grep `ML predict failed` | 偶发可容忍（每请求自动降级）；**持续出现 = ORT/模型问题，立即回滚（§4.3）** |
   | 降级初始化 | 启动日志 `ML re-ranker unavailable` | 出现即未生效，修复前置问题 |
   | 推理延迟 | 路由记录 duration_ms 对照基线 | 单次推理典型 <1ms、hook 预算 P99 <10ms（p2.5 §4.3；测试粗防线 <50ms）；**P99 劣化超过基线 10% → 回滚或关 ML** |
   | 干预面 | （无现成端点，见 §6）日志侧临时统计 Rerank 返回顺序变化比例 | 干预比例异常高（远超标签类目先验）→ 提高 MIN_CONFIDENCE |
   | 路由质量 | admin 路由记录页的选择分布/错误率 | 不劣于基线；异常集中到单模型 → 回滚 |

   > 注意：MIN_CONFIDENCE 只能收窄“干预面”（第 5 层），不能降低推理本身的
   > 延迟；若延迟超标，处置是回滚而不是调阈值。
5. **扩量**：单实例无异常后逐批铺开到全部实例。每批重复第 3、4 步。
   当前**没有流量百分比灰度能力**（§1.2 ⚠），灰度粒度 = 实例级。

### 3.2 推荐参数

- `ROUTING_ML_MIN_CONFIDENCE=0.6`（默认）。模型在真实数据上的准确率验证前
  （p2.5 §7 #1），不要降到 0.5 以下；干预面过大时升到 0.7–0.8。
- `ROUTING_ML_INTRA_OP_THREADS=1`（默认，勿调高：热路径与推理共享 CPU）。

---

## 4. 模型版本回滚

### 4.1 版本 pin：manifest 契约

- `manifest.json` 是训练端↔Go 端唯一契约（`routingopt/ml_manifest.go`），
  启动时校验：`schema_version` 必须为 `"v1"`、`model_file` 非空、
  `label_classes >= 2`、输入数 > 0；不满足即走第 3 层降级（不会加载坏模型）。
- `model.onnx` 按 `manifest.json` 同目录相对解析 → **manifest+模型整目录版本化**：
  ```
  /opt/llm-gateway/ml/
    2026-09-07-r1/manifest.json  model.onnx
    2026-09-01-r0/manifest.json  model.onnx   # 保留 N 个旧版本
  ```
  “当前版本” = 环境变量 `ROUTING_ML_MANIFEST_PATH` 指向的目录。
- 无热更新：`attachMLReranker` 仅在启动时构建一次（p2.5 §5/§7 #3）。
  **任何版本切换/开关变更都必须重启网关进程。**

### 4.2 回滚档位（从轻到重，均为改环境变量 + 重启）

| 档位 | 操作 | 效果 |
|---|---|---|
| A 收窄干预 | `ROUTING_ML_MIN_CONFIDENCE=0.7`（或 0.8） | ML 在线但极少干预 |
| B 关闭 ML | `ROUTING_ML_ENABLED=false` | 回纯规则排序，optimizer 其余部分保留 |
| C 关闭 optimizer | `ROUTING_OPT_ENABLED=false` | 整个 optimizer 下线，pre-P2.2 基线路由 |
| D 换回旧模型 | `ROUTING_ML_MANIFEST_PATH` 指向旧 release 目录 | 回到旧版本 ML 行为 |

典型顺序：模型质量可疑 → 先 A（观察是否阈值问题）→ 仍可疑则 D（回旧模型）；
稳定性可疑（延迟/报错）→ 直接 B 或 C。B/C 是无损的：所有降级路径都保证
路由行为与基线逐字节一致（§2）。

---

## 5. 回滚验证清单

回滚后逐项确认（日志 + admin 路由记录页）：

- [ ] 启动日志确认目标状态：
  - B/C 档：`routingopt: ML re-ranker disabled` / `autoroute: routing optimizer disabled`
  - D 档：`routingopt: ML re-ranker enabled` 且 `manifest` 字段为旧版本路径、
    `labels` 类目与预期一致
- [ ] 无 `ML re-ranker unavailable` / `ML predict failed` /
  `ROUTING_ML_MANIFEST_PATH unset` 告警
- [ ] 路由 P99（duration_ms）回落到基线水平
- [ ] 路由选择分布回到规则引擎基线（灰度期被 ML 提升的候选不再异常集中）
- [ ] （D 档）旧 manifest 通过校验：`schema_version="v1"`、`model_file` 存在、
  `label_classes` 与线上候选名可精确/前缀匹配
- [ ] （可选，预发）带 `ONNXRUNTIME_SHARED_LIBRARY_PATH` 复跑
  `go test ./routingopt/ -run TestMLSelector -v`

---

## 6. 已知限制（不夸大）

1. **无流量百分比灰度**：`ROUTING_OPT_AB_TEST_*` 声明但未生效（审计 F-3）；
   灰度只能按实例分批。
2. **无 ML 专属运行时指标**：admin 诊断端点（标签类目/预测分布/命中率）在
   p2.5 §7 #4 仍为 TODO；干预率目前只能靠日志侧统计。热图/路由记录仅有
   duration_ms（审计 F-7）。
3. **无热更新**：模型/manifest 变更需要重启（p2.5 §7 #3 TODO）。
4. **词汇表差异**：训练数据 classifier/profile 词表与运行时取值不一致，
   由 ONNX OrdinalEncoder `unknown_value=-1` 降级为“未知桶”（p2.5 §4.5）；
   真实数据重训+词汇表归一前，ML 命中率预期偏低——放量阈值相应保守。

# ml-training — P2.4 AUTO路由ML训练管道

RandomForest基线模型：用P2.2导出的Parquet训练数据 + P2.1人工标注训练一个路由模型，
离线验证ML路由是否优于规则引擎。

- 数据契约：`exporter/parquet_schema.go`（schema v1，25字段，隐私合规）
- 设计文档：[docs/ml/p2.4-training-pipeline.md](../docs/ml/p2.4-training-pipeline.md)
- 上游：P2.2 导出（`llm-gw-exporter`）、P2.1 标注（`llm-gw-annotator`）

## 目录结构

```
ml-training/
├── requirements.txt          # 核心依赖（scikit-learn/pandas/pyarrow/numpy）
├── requirements-dev.txt      # + pytest + skl2onnx(ONNX导出)
├── config.yaml               # 特征清单、超参数、划分比例（训练的唯一配置入口）
├── src/
│   ├── data_loader.py        # Parquet加载 + 人工标注合并 + 清洗 + 70/15/15分层切分
│   ├── feature_pipeline.py   # 类别编码(OrdinalEncoder) + 数值归一化(StandardScaler)
│   ├── train.py              # RandomForest训练（人工标注样本x2权重）
│   ├── evaluate.py           # 准确率/混淆矩阵/分provider P/R/F1/特征重要性/基线对比
│   ├── export_model.py       # 导出joblib / ONNX
│   └── synthetic.py          # 合成数据（测试/冒烟，无生产数据也能跑通全流程）
├── tests/                    # 单元测试 + 端到端集成测试（27个）
├── data/                     # 训练数据（gitignore）
├── models/                   # 训练产物（gitignore）
└── reports/                  # 独立评估报告（gitignore）
```

## 环境搭建

要求 Python ≥ 3.11（在 3.13 验证）。

```bash
cd ml-training
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt          # 只训练/评估
pip install -r requirements-dev.txt      # + 测试 / ONNX导出
```

## 快速开始（无生产数据冒烟）

```bash
python -m src.train --synthetic 20000
# 输出 models/<时间戳>/ 下的 model.joblib + evaluation_report.{md,json}
```

## 训练命令

```bash
# 1. 用P2.2导出训练数据（在gateway仓库根目录）
#    llm-gw-exporter export --config full-v1 --output ml-training/data/training_data.parquet

# 2. （可选）用P2.1标注CSV覆盖低置信度样本
#    llm-gw-annotator export --output ml-training/data/annotations.csv

# 3. 训练（config.yaml中的data.parquet_paths指向数据）
python -m src.train --config config.yaml

# 显式指定数据（覆盖config）
python -m src.train --data "data/exports/*.parquet" --annotations data/annotations.csv
```

训练产物 `models/<run>/`：

| 文件 | 说明 |
|---|---|
| `model.joblib` | 完整Pipeline（特征变换+RF），Python推理直接load |
| `metadata.json` | 特征清单、标签类目、数据文件、超参数、config快照、指标 |
| `evaluation_report.md` | 人类可读评估报告 |
| `evaluation_report.json` | 机器可读指标（供对比/归档） |
| `confusion_matrix.csv` / `feature_importance.csv` | 明细导出 |

## 评估方法

```bash
# 对已有run重现评估（相同seed→相同切分）
python -m src.evaluate --run models/20260907-101500
```

评估内容（`evaluation_report.md`）：

1. **准确率**：模型在 val/test 上的 Accuracy，以及人工标注子集上的准确率
2. **混淆矩阵**：行=真实provider，列=预测provider
3. **分provider的 Precision/Recall/F1**（classification_report）
4. **特征重要性**：RF impurity importance，与原始特征列一一对应
5. **基线对比**：
   - `most_frequent`：永远预测训练集最频繁provider
   - `random_uniform`：均匀随机
   - `random_stratified`：按训练集标签分布随机
   - `rule_engine`：测试集中人工标注行的 auto_label vs human_label；
     无标注行时使用 `evaluation.rule_engine_accuracy`（从 `annotation_stats`
     视图 / `llm-gw-annotator stats` 获取生产值）

**成功判据**：模型 test accuracy > 规则引擎基线（`beats_baselines.vs_rule_engine`）。
训练时间目标 < 10分钟/100万样本（RandomForest n_estimators=100, n_jobs=-1）。

## 模型导出

```bash
python -m src.export_model --run models/<run>              # joblib + ONNX
python -m src.export_model --run models/<run> --skip-onnx  # 只导出joblib
```

ONNX输出的输入契约（阶段4 Go集成 `router/ml_router.go` 使用）：

- 每个特征列一个命名输入，形状 `[None, 1]`
  - 类别列：string张量，缺失/未知传 `"__missing__"`
  - 布尔列：int64张量，`1/0`，缺失传 `-1`
  - 数值列：float32张量，缺失传 `NaN`（图内中值填充）
- 编码器/缩放器/随机森林全部在同一个ONNX图内，Go端不做任何特征变换
- 输出：label(int64) + 每个标签的概率(float)

## 测试

```bash
python -m pytest tests/ -v          # 27个用例：单元 + 端到端集成
```

覆盖：schema/隐私校验、标注合并（human_label优先、x2权重）、清洗、
70/15/15分层切分无泄漏、特征编码（未见类别→-1、缺失→-1）、
Pipeline序列化往返、端到端训练（含基线对比与特征重要性断言）、
ONNX与joblib预测一致性。

## 常见问题

**Q: 报错 "parquet missing required schema v1 columns"**
数据不是P2.2导出的schema v1格式。用 `llm-gw-exporter` 重新导出。

**Q: 报错 "labels too rare for stratified 3-way split"**
某些标签样本太少。调高 `data.min_label_frequency`（默认10）让清洗阶段
把它们整类丢弃，或补充数据。

**Q: 人工标注的标签空间与chosen_model不一致（provider vs 模型名）**
设置 `data.annotation_require_known_label: true` 可丢弃不在自动标签
词表内的覆盖，防止污染标签空间；阶段4再做正式的标签空间归一。

**Q: 规则引擎基线显示 N/A**
测试集没有人工标注行且未配置 `evaluation.rule_engine_accuracy`。
先通过P2.1流程标注一批低置信度样本，或把 `annotation_stats` 视图里的
准确率填进config。

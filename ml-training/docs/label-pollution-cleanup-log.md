# 标签污染清理执行记录

## 背景

**问题**: `ml-training/src/data_loader.py` 中 `merge_annotations()` 函数的 `annotation_require_known_label` 参数默认为 `False`，允许人工标注使用训练数据中不存在的标签（如 provider 名称而非 model 名称），导致：

1. **词汇表污染**: 模型学习到错误的标签空间（混合 model 和 provider 层级）
2. **预测混淆**: 推理时无法匹配候选（provider vs model 不匹配）
3. **训练噪声**: 标注噪声降低模型准确率

**根因**: data_loader.py:449 默认值设置错误
```python
require_known_label=bool(data_cfg.get("annotation_require_known_label", False)),
```

**影响范围**: 所有使用人工标注的训练 run，特别是早期手工标注数据

---

## 预防措施（长期修复）

### 1. 修改默认值为 True（推荐）

**文件**: `ml-training/src/data_loader.py:449`

```python
# 修改前
require_known_label=bool(data_cfg.get("annotation_require_known_label", False)),

# 修改后
require_known_label=bool(data_cfg.get("annotation_require_known_label", True)),
```

**影响**: 未来所有训练 run 默认拒绝未知标签，防止污染

### 2. 配置文件显式声明（向后兼容）

**文件**: `ml-training/config.yaml`

```yaml
data:
  parquet_paths: [...]
  annotations_path: data/annotations.csv
  annotation_require_known_label: true  # 显式启用标签校验
  annotation_label_weight: 2.0
  # ...
```

### 3. CI 预检（强制）

在 `.github/workflows/ml-training-ci.yml` 中添加标签污染检测：

```yaml
- name: Check for label pollution
  run: |
    cd ml-training
    python scripts/clean_label_pollution.py \
      --training-data tests/fixtures/training_sample.parquet \
      --annotations tests/fixtures/annotations_sample.csv \
      --report /dev/null
```

如检测到污染，CI 失败并要求人工审查。

---

## 执行记录模板

### 执行信息

- **执行日期**: YYYY-MM-DD
- **执行人**: [姓名/工号]
- **训练数据版本**: [commit hash 或数据导出日期]
- **标注数据路径**: [annotations.csv 完整路径]
- **工具版本**: clean_label_pollution.py @ [commit hash]

### 步骤 1: 分析污染情况

```bash
cd ml-training

python scripts/clean_label_pollution.py \
  --training-data data/training_YYYYMMDD.parquet \
  --annotations data/annotations.csv \
  --label-column chosen_model \
  --annotation-label-column human_label \
  --report pollution_analysis_YYYYMMDD.txt
```

**输出**: `pollution_analysis_YYYYMMDD.txt`

**关键指标**:
- 训练数据已知标签数: ___
- 人工标注总数: ___
- 污染标注数: ___ (污染率: ___%)
- 污染标签清单: [列出 Top 10]

### 步骤 2: 备份原始标注

```bash
cp data/annotations.csv data/annotations_backup_YYYYMMDD.csv
sha256sum data/annotations_backup_YYYYMMDD.csv > data/annotations_backup_YYYYMMDD.csv.sha256
```

**备份文件 SHA256**: _______________________________________________

### 步骤 3: 执行清理

**策略选择**: [ ] drop (删除污染行)  [ ] map (映射到已知标签)

#### 策略 A: 删除污染行（推荐，污染率 < 5%）

```bash
python scripts/clean_label_pollution.py \
  --training-data data/training_YYYYMMDD.parquet \
  --annotations data/annotations.csv \
  --label-column chosen_model \
  --annotation-label-column human_label \
  --strategy drop \
  --output data/annotations_cleaned.csv
```

#### 策略 B: 映射到已知标签（需人工审查映射表）

1. 创建映射表 `label_map.json`:
```json
{
  "openai": "gpt-4o",
  "anthropic": "claude-3-5-sonnet-20241022",
  "unknown_provider": "gpt-4o-mini"
}
```

2. 执行映射清理:
```bash
python scripts/clean_label_pollution.py \
  --training-data data/training_YYYYMMDD.parquet \
  --annotations data/annotations.csv \
  --label-column chosen_model \
  --annotation-label-column human_label \
  --strategy map \
  --label-map label_map.json \
  --output data/annotations_cleaned.csv
```

**清理结果**:
- 原始行数: ___
- 删除行数: ___
- 映射行数: ___
- 最终行数: ___
- 清理后污染率: ___% (应为 0%)

### 步骤 4: 验证清理结果

```bash
# 重新分析清理后的数据
python scripts/clean_label_pollution.py \
  --training-data data/training_YYYYMMDD.parquet \
  --annotations data/annotations_cleaned.csv \
  --label-column chosen_model \
  --annotation-label-column human_label \
  --report pollution_analysis_after_YYYYMMDD.txt

# 确认污染率为 0%
grep "污染率" pollution_analysis_after_YYYYMMDD.txt
```

**验证结果**: [ ] 通过 (污染率 0%)  [ ] 失败 (需重新清理)

### 步骤 5: 更新配置并重新训练

```bash
# 启用标签校验
cat >> config.yaml << EOF
data:
  # ... 其他配置 ...
  annotations_path: data/annotations_cleaned.csv
  annotation_require_known_label: true  # 防止未来污染
EOF

# 重新训练
python -m src.train
```

**训练 run ID**: ___________

### 步骤 6: 归档清理记录

```bash
# 创建清理记录归档
mkdir -p data/cleaning_records/YYYYMMDD/
mv pollution_analysis_*.txt data/cleaning_records/YYYYMMDD/
mv data/annotations_backup_YYYYMMDD.csv* data/cleaning_records/YYYYMMDD/
cp data/annotations_cleaned.csv data/cleaning_records/YYYYMMDD/annotations_after_cleaning.csv

# 提交到版本控制（仅记录，不提交大数据文件）
git add data/cleaning_records/YYYYMMDD/*.txt
git commit -m "docs(ml): 标签污染清理记录 YYYYMMDD — 删除 N 个污染标签"
```

---

## 回滚步骤（如需）

如果清理后训练效果变差，或发现误删有效标注：

```bash
# 恢复备份
cp data/cleaning_records/YYYYMMDD/annotations_backup_YYYYMMDD.csv data/annotations.csv

# 验证 SHA256
sha256sum -c data/cleaning_records/YYYYMMDD/annotations_backup_YYYYMMDD.csv.sha256

# 恢复配置
vi config.yaml  # 手动改回 annotation_require_known_label: false

# 重新训练
python -m src.train
```

---

## 检查清单

- [ ] 执行前已备份原始标注文件
- [ ] 污染分析报告已生成并人工审查
- [ ] 清理策略经过团队评审（特别是映射表）
- [ ] 清理后污染率为 0%
- [ ] 清理后标注数量在合理范围（未过度删除）
- [ ] 配置文件已更新 `annotation_require_known_label: true`
- [ ] 重新训练并验证模型准确率未下降
- [ ] 清理记录已归档并提交版本控制

---

## 常见问题

### Q1: 污染率超过 10% 怎么办？

**A**: 高污染率通常表示标注规范问题，建议：
1. 暂停清理，组织标注团队培训
2. 明确标注粒度（model vs provider）
3. 重新标注而非强制映射

### Q2: 删除污染行后标注数量太少？

**A**: 两种选择：
1. 增加人工标注（推荐）
2. 纯自动标注训练（`annotations_path: ""`）

### Q3: 某些"污染标签"实际是新模型怎么办？

**A**: 更新训练数据：
1. 从生产环境重新导出 Parquet（包含新模型）
2. 用新训练数据重新分析，合法标签将不再被识别为污染

### Q4: 如何验证清理没有引入新问题？

**A**: 
1. 清理前后标签分布对比（Top 20 标签占比应相似）
2. 重新训练后准确率不应下降 > 1%
3. A/B 测试清理前后模型的线上效果

---

## 参考文档

- `ml-training/src/data_loader.py` - merge_annotations() 函数实现
- `docs/ml/p2.4-training-pipeline.md` - 训练管道架构
- `docs/audit/2026-09-07-24h-audit.md` § F-8 - 审计发现原始记录

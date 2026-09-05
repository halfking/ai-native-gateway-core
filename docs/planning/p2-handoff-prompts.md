# P2 后续任务 Handoff 提示词

## 会话1: P2.1 人工标注工作流 (高优先级，1-2天)

### 任务目标
为低置信度AUTO路由样本建立人工标注流程，提升训练数据质量。

### 背景上下文
- P2.2训练数据导出管道已完成（commit 5ef9fa679 + 0ce9e7c04）
- 已有3个预定义导出配置，其中`low-confidence-v1`专门用于导出需要人工审核的样本
- 当前AUTO路由使用规则引擎，置信度<0.7的样本需要人工验证

### 具体任务
实现P2.1人工标注工作流（CSV方案，快速启动）：

1. **创建Migration 664**
   - 表名: `training_human_annotations`
   - 字段: id, request_id, auto_label, auto_confidence, human_label, is_correct, annotation_reason, annotated_by, annotated_at, annotation_metadata
   - 索引: request_id, annotated_by, created_at
   - 统计视图: annotation_accuracy_by_provider

2. **创建CLI工具: cmd/llm-gw-annotator**
   - 子命令1: `export` - 导出低置信度样本为CSV（包含request_id, model_name, task_type, confidence, selected_provider等）
   - 子命令2: `import` - 导入标注好的CSV到training_human_annotations表
   - 子命令3: `stats` - 显示标注统计（总数、准确率、分provider准确率）
   - 子命令4: `validate` - 验证CSV格式和必填字段

3. **标注CSV格式**
   ```csv
   request_id,model_name,task_type,prompt_tokens,auto_provider,confidence,human_provider,is_correct,reason,annotator
   req_001,gpt-4,chat,150,openai,0.65,openai,TRUE,correct,alice
   req_002,claude-3,chat,200,anthropic,0.62,aws_bedrock,FALSE,better_latency,bob
   ```

4. **编写文档**
   - docs/deployment/p2.1-human-annotation-workflow.md
   - 包含：导出流程、标注指南、导入流程、质量检查

5. **测试**
   - 单元测试：CSV解析、数据验证、导入逻辑
   - 集成测试：导出 -> 人工标注 -> 导入 -> 统计

### 提示词

```
请实现P2.1人工标注工作流，目标是为低置信度AUTO路由样本建立CSV标注流程。

背景：
- P2.2训练数据导出管道已完成（支持导出结构化特征）
- 需要对confidence<0.7的样本进行人工验证，提升训练数据质量
- 采用CSV方案快速启动（1-2天完成）

任务清单：
1. 创建Migration 664: training_human_annotations表
   - 存储人工标注结果（auto_label vs human_label）
   - 支持标注原因和元数据

2. 创建CLI工具: cmd/llm-gw-annotator（参考cmd/llm-gw-exporter设计）
   - export: 导出低置信度样本到CSV
   - import: 导入标注后的CSV
   - stats: 显示标注统计
   - validate: 验证CSV格式

3. CSV格式设计（便于Excel/Google Sheets标注）
   - 必填字段：request_id, auto_provider, human_provider, is_correct
   - 可选字段：annotation_reason, annotator
   
4. 编写部署文档: docs/deployment/p2.1-human-annotation-workflow.md
   - 标注流程、质量检查、最佳实践

5. 测试验证（单元测试+集成测试）

请参考：
- P2.2导出管道设计: exporter/training_exporter.go, cmd/llm-gw-exporter/main.go
- Migration模板: sql/migrations/startup/663_training_export.sql
- 规划文档: docs/planning/p2-next-steps.md

要求：
- 遵循现有代码风格和架构模式
- 所有测试通过后再提交
- 审计代码（资源管理、边界条件、错误处理）
- 提交到main分支并推送
```

---

## 会话2: P2.4 ML模型训练管道 - 阶段1&2 (中优先级，3-4天)

### 任务目标
搭建Python训练环境，训练第一个RandomForest模型，验证ML路由可行性。

### 背景上下文
- P2.2已能导出隐私合规的Parquet训练数据（25个特征字段）
- P2.1已能收集人工标注数据（作为高质量标签）
- 目标：训练ML模型替代规则引擎，提升AUTO路由准确率

### 具体任务
实现P2.4阶段1（训练环境）和阶段2（基线模型）：

1. **创建Python训练项目: ml-training/**
   ```
   ml-training/
   ├── requirements.txt        # scikit-learn, pandas, pyarrow, numpy
   ├── src/
   │   ├── data_loader.py      # 加载Parquet数据
   │   ├── feature_pipeline.py # 特征编码和归一化
   │   ├── train.py            # 训练脚本
   │   ├── evaluate.py         # 评估脚本
   │   └── export_model.py     # 导出为ONNX
   ├── config.yaml             # 训练配置
   ├── tests/                  # 单元测试
   └── README.md               # 使用文档
   ```

2. **数据加载模块 (data_loader.py)**
   - 加载Parquet文件（支持P2.2导出的格式）
   - 合并人工标注数据（优先使用human_label作为标签）
   - 数据清洗：处理missing值、异常值
   - 划分训练集/验证集/测试集（70/15/15）

3. **特征Pipeline (feature_pipeline.py)**
   - 类别特征编码：LabelEncoder（model_family, task_type, region等）
   - 数值特征归一化：StandardScaler（prompt_token_count, context_window_util等）
   - sklearn Pipeline封装

4. **训练脚本 (train.py)**
   - 模型：RandomForestClassifier（n_estimators=100, max_depth=20）
   - 超参数搜索：GridSearchCV（可选）
   - 保存模型和Pipeline（pickle或joblib）

5. **评估脚本 (evaluate.py)**
   - 准确率（Accuracy）
   - 混淆矩阵（Confusion Matrix）
   - 分provider的Precision/Recall
   - 特征重要性（Feature Importance）
   - 对比基线：规则引擎、随机选择、最频繁provider

6. **文档**
   - ml-training/README.md: 环境搭建、训练命令、评估方法
   - docs/ml/p2.4-training-pipeline.md: 技术细节和设计决策

7. **测试**
   - 单元测试：数据加载、特征编码、Pipeline
   - 集成测试：端到端训练流程

### 提示词

```
请实现P2.4 ML模型训练管道的阶段1（训练环境）和阶段2（基线模型）。

背景：
- P2.2已能导出Parquet训练数据（25个特征字段，包含15个特征+10个元数据）
- P2.1已能收集人工标注数据（高质量标签）
- 目标：训练RandomForest模型，验证ML路由是否优于规则引擎

任务清单：
1. 创建Python训练项目: ml-training/
   - requirements.txt（scikit-learn, pandas, pyarrow等）
   - src/: 数据加载、特征Pipeline、训练、评估模块
   - config.yaml: 训练超参数配置

2. 实现数据加载 (data_loader.py)
   - 加载P2.2导出的Parquet文件
   - 合并P2.1人工标注数据（优先human_label）
   - 划分训练/验证/测试集

3. 实现特征Pipeline (feature_pipeline.py)
   - 类别特征编码（LabelEncoder）
   - 数值特征归一化（StandardScaler）
   - sklearn Pipeline封装

4. 实现训练脚本 (train.py)
   - RandomForestClassifier基线模型
   - 保存训练好的模型和Pipeline

5. 实现评估脚本 (evaluate.py)
   - 准确率、混淆矩阵、特征重要性
   - 对比规则引擎基线

6. 编写文档
   - ml-training/README.md: 使用指南
   - docs/ml/p2.4-training-pipeline.md: 技术设计

7. 测试验证（单元测试+集成测试）

特征说明（参考exporter/parquet_schema.go）：
- 类别特征：model_family, capability_level, task_type, region, profile
- 数值特征：prompt_token_count, context_window_util, confidence
- 布尔特征：is_streaming, has_vision, has_tools, is_explore
- 标签：selected_provider（或human_label）

成功指标：
- 模型准确率 > 规则引擎基线（需要先测量规则引擎准确率）
- 训练时间 < 10分钟（100万样本）
- 特征重要性排名合理（例如：model_family, task_type应该重要）

请参考：
- Parquet schema: exporter/parquet_schema.go
- 数据库schema: sql/migrations/startup/658_auto_route_selections.sql
- 规划文档: docs/planning/p2-next-steps.md

要求：
- 代码风格遵循Python最佳实践（PEP 8, type hints）
- 所有测试通过后再提交
- 提供训练日志和评估报告
```

---

## 会话3: P2.4 ML模型训练管道 - 阶段3&4 (高优先级，5-7天)

### 任务目标
将训练好的模型集成到Go Gateway，实现A/B测试框架。

### 前置条件
- P2.4阶段1&2已完成：训练好的RandomForest模型（.pkl或.onnx）
- 评估报告显示模型优于规则引擎基线

### 具体任务
实现P2.4阶段3（深入评估）和阶段4（Go集成部署）：

1. **阶段3: 深入评估**
   - 错误分析：查看预测错误的样本，识别模式
   - 分场景准确率：按model_family、task_type、region分组评估
   - 置信度校准：预测概率是否准确反映实际准确率
   - A/B测试模拟：在测试集上模拟线上流量分配

2. **阶段4: 模型导出为ONNX**
   - 使用skl2onnx导出RandomForest为.onnx格式
   - 验证ONNX推理结果与sklearn一致
   - 测试ONNX推理性能（目标: <1ms）

3. **Go集成: router/ml_router.go**
   - 使用github.com/yalue/onnxruntime_go加载ONNX模型
   - 实现`MLRouter`接口（与RuleRouter并行）
   - 特征提取：从Request转换为模型输入向量
   - 推理：调用ONNX Runtime预测
   - 结果映射：ONNX输出转换为provider选择

4. **A/B测试框架**
   - 流量分配：10% ML路由 vs 90% 规则引擎
   - 监控指标：准确率、延迟、成本、用户满意度
   - 实时切换：通过配置动态调整流量比例
   - Fallback机制：ML超时/失败时降级到规则引擎

5. **模型热更新**
   - Gateway启动时从S3/OSS下载模型文件
   - 监听模型文件变化，支持热重载
   - 灰度发布：先10%流量验证新模型

6. **监控和告警**
   - Prometheus指标：ml_prediction_latency, ml_prediction_accuracy, ml_fallback_count
   - Grafana Dashboard：ML vs 规则引擎对比
   - 告警规则：准确率下降、延迟过高、fallback过多

7. **文档**
   - docs/deployment/p2.4-ml-deployment.md: 部署流程、配置说明、故障排查
   - docs/ml/model-versioning.md: 模型版本管理策略

8. **测试**
   - 单元测试：特征提取、ONNX推理、结果映射
   - 集成测试：端到端ML路由流程
   - 性能测试：推理延迟benchmark（目标: <10ms p99）

### 提示词

```
请实现P2.4 ML模型训练管道的阶段3（深入评估）和阶段4（Go集成部署）。

前置条件：
- P2.4阶段1&2已完成（训练好的RandomForest模型）
- 模型评估结果显示优于规则引擎基线

任务清单：
1. 阶段3: 深入评估
   - 错误分析：识别预测错误的模式
   - 分场景准确率：按model_family/task_type/region分组
   - 置信度校准曲线
   - A/B测试模拟

2. 模型导出为ONNX
   - 使用skl2onnx导出RandomForest
   - 验证ONNX推理结果正确性
   - Benchmark ONNX推理性能（目标: <1ms）

3. Go集成: router/ml_router.go
   - 使用ONNX Runtime Go bindings加载模型
   - 实现MLRouter接口
   - 特征提取：Request -> 模型输入向量
   - 推理 + 结果映射

4. A/B测试框架
   - 流量分配配置：10% ML vs 90% 规则引擎
   - 监控指标：准确率、延迟、成本
   - Fallback机制：ML超时/失败 -> 规则引擎

5. 模型热更新
   - 启动时从S3/OSS下载模型
   - 监听文件变化，热重载
   - 灰度发布支持

6. 监控和告警
   - Prometheus指标：ml_prediction_latency, ml_prediction_accuracy
   - Grafana Dashboard: ML vs 规则引擎对比
   - 告警规则：准确率下降、延迟过高

7. 文档
   - docs/deployment/p2.4-ml-deployment.md: 部署流程
   - docs/ml/model-versioning.md: 版本管理

8. 测试验证
   - 单元测试：特征提取、ONNX推理
   - 集成测试：端到端ML路由
   - 性能测试：延迟benchmark（目标: <10ms p99）

技术选型：
- ONNX Runtime: github.com/yalue/onnxruntime_go
- 模型格式: .onnx（跨语言，推理快）
- 模型存储: S3/OSS + 本地缓存

成功指标：
- ML准确率 > 规则引擎 + 5%
- 推理延迟 < 10ms (p99)
- A/B测试：用户满意度 >= 规则引擎
- 零downtime热更新

请参考：
- 规则引擎实现: router/rule_router.go
- 监控集成: metrics/prometheus.go
- 规划文档: docs/planning/p2-next-steps.md

要求：
- 遵循现有代码风格和架构
- 所有测试通过后再提交
- 审计代码（资源管理、错误处理、性能）
- 提交到main分支并推送
```

---

## 并行执行建议

可以使用子代理并行执行以下任务：

### 并行组1（Week 1）
- **Agent 1**: P2.1 人工标注工作流（CSV方案）
- **Agent 2**: P2.4 阶段1&2（Python训练环境 + 基线模型）

这两个任务相互独立，可以并行开发。

### 串行执行（Week 2-3）
- **Week 2**: P2.4 阶段3（深入评估）+ 模型导出ONNX
- **Week 3**: P2.4 阶段4（Go集成 + A/B测试）

这些任务依赖P2.4阶段1&2的产出，需要串行执行。

---

## 检查清单

在启动下一个任务前，确保：

### P2.1前置检查
- ✅ P2.2训练数据导出管道已部署
- ✅ 已有低置信度样本可供导出（confidence < 0.7）
- ✅ 确定标注人员和标注流程

### P2.4前置检查
- ✅ P2.2已导出至少10万行训练数据
- ✅ Python环境可用（Python 3.8+）
- ✅ 训练服务器资源充足（8核CPU, 16GB内存）

### P2.4阶段4前置检查
- ✅ 训练好的模型准确率 > 规则引擎基线
- ✅ 模型文件已导出为ONNX格式
- ✅ Go环境已安装ONNX Runtime C库
- ✅ 监控系统已就绪（Prometheus + Grafana）

---

**创建日期**: 2026-09-06  
**文档版本**: v1.0  
**相关文档**: docs/planning/p2-next-steps.md

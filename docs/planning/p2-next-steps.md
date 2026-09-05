# P2 AUTO路由ML训练基础设施 - 后续任务规划

**规划日期**: 2026-09-06  
**当前状态**: P2.2训练数据导出管道已完成并审计通过  
**规划目标**: 完成P2剩余任务，建立完整的ML训练基础设施

---

## 已完成任务 ✅

### P2.2 训练数据导出管道 (已完成)
- ✅ 隐私合规Parquet导出（25个结构化特征字段）
- ✅ 质量过滤、去重、压缩
- ✅ CLI工具（export, list, validate, configs）
- ✅ 代码审计和修复（3个P1问题已修复）
- ✅ 部署文档和测试
- **交付物**: commit 5ef9fa679 + 0ce9e7c04

### P2.3 特征质量监控 (已完成)
- ✅ 特征完整性监控（missing rate, null rate）
- ✅ 特征分布监控（min, max, mean, stddev, p50, p90, p99）
- ✅ 标签质量监控（label distribution, confidence distribution）
- **交付物**: commit c1fd9f4c9

---

## 待完成任务 📋

### P2.1 人工标注工作流 (高优先级)

**目标**: 为低置信度样本建立人工标注流程，提升训练数据质量

#### 任务拆解
1. **标注数据导出**
   - 使用`low-confidence-v1`配置导出低置信度样本（confidence < 0.7）
   - 格式：CSV（便于人工审核）或Web界面（推荐）
   - 字段：request_id, model_name, task_type, confidence, selected_provider, rejected_providers
   
2. **标注界面设计**（二选一）
   - **方案A**: Web界面（推荐）
     - 显示请求特征：model_name, task_type, token_count等
     - 显示ML预测：selected_provider, confidence
     - 标注选项：Correct / Incorrect / Should be [provider]
     - 标注原因：下拉选择（性能、成本、可用性、其他）
   - **方案B**: CSV + 表格工具
     - 导出到Google Sheets或Excel
     - 添加标注列：human_label, human_reason
     - 人工填写后重新导入
   
3. **标注数据存储**
   - 创建表`training_human_annotations`
   ```sql
   CREATE TABLE training_human_annotations (
     id BIGSERIAL PRIMARY KEY,
     request_id TEXT NOT NULL,
     auto_label TEXT NOT NULL,        -- ML预测的provider
     auto_confidence FLOAT NOT NULL,
     human_label TEXT NOT NULL,       -- 人工标注的provider
     is_correct BOOLEAN NOT NULL,     -- auto_label == human_label
     annotation_reason TEXT,          -- 标注原因（性能、成本等）
     annotated_by TEXT NOT NULL,      -- 标注人员
     annotated_at TIMESTAMPTZ DEFAULT NOW(),
     annotation_metadata JSONB        -- 额外上下文
   );
   ```
   
4. **标注质量统计**
   - ML准确率：`SELECT AVG(is_correct::int) FROM training_human_annotations`
   - 分provider准确率：按auto_label分组统计
   - 标注一致性：多人标注同一样本的一致性
   
5. **标注数据集成到训练**
   - 将human_label作为ground truth
   - 对于is_correct=false的样本，使用human_label重新训练
   - 考虑样本权重：人工标注 > 高置信度自动标注 > 低置信度自动标注

#### 预期产出
- Migration 664: training_human_annotations表
- CLI命令：`llm-gw-annotator export/import/stats`
- 或Web界面：`/admin/training/annotations`
- 标注指南文档

#### 预计工作量
- 方案A（Web界面）: 3-5天
- 方案B（CSV流程）: 1-2天

---

### P2.4 ML模型训练管道 (中优先级)

**目标**: 建立端到端的ML模型训练、评估、部署流程

#### 任务拆解

##### 阶段1: 训练环境搭建
1. **Python训练项目结构**
   ```
   ml-training/
   ├── requirements.txt      # scikit-learn, pandas, pyarrow
   ├── train.py              # 训练脚本
   ├── evaluate.py           # 评估脚本
   ├── export_model.py       # 导出为ONNX或pickle
   └── config.yaml           # 训练配置
   ```

2. **数据加载模块**
   ```python
   def load_training_data(parquet_path):
       df = pd.read_parquet(parquet_path)
       
       # 特征列（15个）
       features = [
           'model_family', 'capability_level', 'task_type',
           'is_streaming', 'prompt_token_count', 'context_window_util',
           'has_vision', 'has_tools', 'priority_speed', 'priority_cost',
           'priority_quality', 'priority_balance', 'region',
           'client_model_preference', 'client_provider_preference'
       ]
       
       # 标签列
       label = 'selected_provider'
       
       X = df[features]
       y = df[label]
       
       return X, y
   ```

3. **特征工程**
   - 类别特征编码：LabelEncoder或OneHotEncoder
   - 数值特征归一化：StandardScaler
   - 特征Pipeline：sklearn.pipeline.Pipeline

##### 阶段2: 模型训练
1. **基线模型（快速验证）**
   - RandomForestClassifier（推荐作为第一个模型）
   - 优点：可解释性强、无需调参、支持类别特征
   - 训练时间：100万样本 < 5分钟

2. **模型训练脚本**
   ```python
   from sklearn.ensemble import RandomForestClassifier
   from sklearn.model_selection import train_test_split
   from sklearn.metrics import accuracy_score, classification_report
   
   # 加载数据
   X, y = load_training_data('exports/2026-09-06-v1.parquet')
   
   # 划分训练集/测试集
   X_train, X_test, y_train, y_test = train_test_split(
       X, y, test_size=0.2, random_state=42, stratify=y
   )
   
   # 训练模型
   clf = RandomForestClassifier(
       n_estimators=100,
       max_depth=20,
       min_samples_split=100,
       random_state=42,
       n_jobs=-1
   )
   clf.fit(X_train, y_train)
   
   # 评估
   y_pred = clf.predict(X_test)
   accuracy = accuracy_score(y_test, y_pred)
   print(f"Test Accuracy: {accuracy:.3f}")
   print(classification_report(y_test, y_pred))
   ```

3. **高级模型（可选）**
   - XGBoost（更高精度）
   - LightGBM（更快训练）
   - CatBoost（原生支持类别特征）

##### 阶段3: 模型评估
1. **评估指标**
   - 整体准确率（Accuracy）
   - 分provider准确率（Per-class Precision/Recall）
   - 混淆矩阵（Confusion Matrix）
   - 特征重要性（Feature Importance）

2. **对比基线**
   - 当前规则引擎准确率（需要收集）
   - 随机选择基线（1/N，N=provider数量）
   - 最频繁provider基线（always predict the most common class）

3. **A/B测试准备**
   - 定义成功指标：准确率、延迟、成本、用户满意度
   - 流量分配：10% ML路由 vs 90% 规则引擎
   - 监控Dashboard：实时对比ML vs 规则引擎

##### 阶段4: 模型部署
1. **模型导出**
   - 格式：ONNX（跨语言，推荐）或pickle（Python only）
   - 文件大小：< 100MB（RandomForest通常 < 10MB）
   - 版本管理：模型文件命名包含日期和版本号

2. **Go集成方案**
   - **方案A**: ONNX Runtime Go bindings（推荐）
     - 库：`github.com/yalue/onnxruntime_go`
     - 优点：无需Python运行时，推理速度快（< 1ms）
     - 缺点：需要安装ONNX Runtime C库
   
   - **方案B**: gRPC调用Python服务
     - Python服务：加载模型，提供预测API
     - 优点：快速开发，易于迭代
     - 缺点：增加延迟（~10ms）、需要维护Python服务
   
   - **方案C**: 预计算缓存
     - 对于常见请求模式，预计算ML预测结果
     - 存储在Redis：`ml:route:prediction:{hash}`
     - 优点：零延迟、无需运行时
     - 缺点：无法覆盖所有情况

3. **部署流程**
   - 训练 → 评估 → 导出 → 上传到S3/OSS
   - Gateway启动时下载模型文件
   - 热更新：监听模型文件变化，重新加载

##### 阶段5: 在线学习（可选，延后）
1. **持续数据收集**
   - 每天/每周导出新数据
   - 合并历史数据 + 新数据

2. **定期重训练**
   - Cron job：每周重训练一次
   - 自动评估：如果新模型更好，自动部署

3. **在线反馈**
   - 用户反馈（thumbs up/down）
   - 性能指标反馈（实际延迟、成本）
   - 将反馈作为训练信号

#### 预期产出
- Python训练项目（ml-training/）
- 训练好的模型文件（.onnx或.pkl）
- Go集成代码（router/ml_router.go）
- 评估报告（accuracy, confusion matrix, feature importance）
- 部署文档

#### 预计工作量
- 阶段1+2（训练环境 + 基线模型）: 2-3天
- 阶段3（评估）: 1天
- 阶段4（部署）: 3-5天（取决于集成方案）
- 阶段5（在线学习）: 5-7天（可延后）

**总计**: 1-2周（不含在线学习）

---

### P2.5 特征工程优化 (低优先级，可选)

**目标**: 基于模型反馈优化特征，提升预测准确率

#### 可能的优化方向
1. **时间特征**
   - hour_of_day（0-23）
   - day_of_week（0-6）
   - is_business_hour

2. **历史特征**
   - user_recent_provider_choices（最近3次选择）
   - provider_recent_success_rate（最近1小时成功率）

3. **交互特征**
   - model_family + task_type（例如：GPT-4 + chat）
   - priority_speed + is_streaming

4. **聚合特征**
   - provider_avg_latency_by_model
   - provider_cost_rank_by_region

#### 预计工作量
- 每个特征：0.5-1天（实现 + 测试 + 评估）
- 特征评估方法：Feature Importance、Ablation Study

---

## 推荐执行顺序

### 第一优先级（立即开始）
1. **P2.1 人工标注工作流**（方案B：CSV流程）
   - 快速搭建，1-2天完成
   - 立即开始收集人工标注数据
   - 为P2.4提供高质量训练数据

### 第二优先级（1周后）
2. **P2.4 ML模型训练管道**（阶段1-3：训练 + 评估）
   - 使用P2.2导出的数据 + P2.1的人工标注
   - 训练第一个RandomForest模型
   - 评估是否优于当前规则引擎

### 第三优先级（2周后）
3. **P2.4 ML模型训练管道**（阶段4：部署）
   - 选择集成方案（推荐方案A：ONNX）
   - 实现A/B测试框架
   - 10%流量上线验证

### 延后优先级
4. **P2.5 特征工程优化**（基于P2.4的结果决定）
5. **P2.4 阶段5 在线学习**（生产验证后再考虑）

---

## 资源需求

### 人力
- **P2.1**: 1人 x 2天（后端开发 or 全栈）
- **P2.4 阶段1-3**: 1人 x 4天（ML工程师 or 熟悉Python的后端）
- **P2.4 阶段4**: 1人 x 5天（Go后端开发 + ML集成经验）

### 基础设施
- **训练环境**: 
  - CPU: 8核+ (RandomForest不需要GPU)
  - 内存: 16GB+ (处理100万行数据)
  - 存储: 50GB+ (Parquet文件 + 模型文件)
- **生产环境**:
  - ONNX Runtime: 需要安装C库（~50MB）
  - 模型文件存储: S3/OSS（~10MB per model）

### 时间表
- **Week 1**: P2.1完成（CSV标注流程）
- **Week 2**: P2.4阶段1-3完成（训练第一个模型）
- **Week 3-4**: P2.4阶段4完成（集成到Gateway）
- **Week 5**: A/B测试验证，决定是否全量上线

---

## 风险和缓解

### 风险1: ML模型准确率不如规则引擎
- **缓解**: 先用A/B测试验证，不全量替换规则引擎
- **备选**: ML + 规则混合（ML给建议，规则做最终决策）

### 风险2: 训练数据不足或质量差
- **缓解**: P2.1人工标注提升数据质量
- **缓解**: 使用P2.3监控特征质量，过滤低质量数据

### 风险3: Go集成ONNX复杂度高
- **缓解**: 先用方案B（gRPC Python服务）快速验证
- **缓解**: 如果Python服务延迟可接受，不强求ONNX集成

### 风险4: 线上推理延迟过高
- **缓解**: 使用方案C（预计算缓存）覆盖常见场景
- **缓解**: 设置超时（10ms），超时则fallback到规则引擎

---

## 成功指标

### P2.1 人工标注
- ✅ 收集至少1000条人工标注样本
- ✅ 标注一致性 > 90%（多人标注）
- ✅ ML当前准确率基线（作为P2.4对比）

### P2.4 ML训练
- ✅ 模型准确率 > 规则引擎准确率 + 5%
- ✅ 推理延迟 < 10ms (p99)
- ✅ A/B测试：ML路由的用户满意度 >= 规则引擎

### P2整体
- ✅ 端到端流程：数据导出 → 标注 → 训练 → 部署 → 监控
- ✅ 可持续迭代：每周可以重训练和部署新模型
- ✅ 生产就绪：稳定运行1个月，无重大问题

---

## 参考资料

### P2.1相关
- [Human-in-the-Loop ML Best Practices](https://www.oreilly.com/library/view/human-in-the-loop-machine/9781617296741/)
- Label Studio（开源标注工具）: https://labelstud.io/

### P2.4相关
- [scikit-learn Pipeline](https://scikit-learn.org/stable/modules/compose.html)
- [ONNX Runtime Go](https://github.com/yalue/onnxruntime_go)
- [ML Model Deployment Patterns](https://martinfowler.com/articles/cd4ml.html)

### 类似系统案例
- Uber Michelangelo（ML平台）: https://eng.uber.com/michelangelo-machine-learning-platform/
- Netflix ML Infrastructure: https://netflixtechblog.com/notebook-innovation-591ee3221233

---

**规划人**: AUTO Route Team  
**下次Review**: 完成P2.1后（预计1周后）  
**文档版本**: v1.0  
**最后更新**: 2026-09-06

# 会话输出审计测试方案

## 测试目标

1. **性能测试**：验证 FastDetector 在实际生产数据上的检测速度（目标 ≤5ms）
2. **准确率测试**：评估敏感词、Prompt Injection、PII、Jailbreak 检测的准确率
3. **覆盖率测试**：检查不同类型敏感内容的检测覆盖情况
4. **优化建议**：基于测试结果提供优化方案

## 数据来源

- **数据库**：252 服务器 PostgreSQL
- **表**：`request_logs`
- **字段**：`outbound_body` (LLM 返回的 JSON 数据)
- **数据量**：最近 7 天的真实请求数据（约 10,000 条）

## 测试指标

### 性能指标
- **平均检测耗时**：单次检测的平均时间
- **P95/P99 耗时**：95% 和 99% 分位的耗时
- **吞吐量**：每秒可处理的请求数

### 准确率指标
- **真阳性率 (TPR)**：正确识别敏感内容的比例
- **假阳性率 (FPR)**：误报率
- **假阴性率 (FNR)**：漏报率
- **F1 分数**：准确率和召回率的调和平均

### 覆盖率指标
- **敏感词覆盖**：各类敏感词的命中统计
- **威胁类型分布**：Injection/PII/Jailbreak 各类型占比
- **决策分布**：Pass/Warn/Block/NeedApproval 的比例

## 测试流程

```
1. 数据提取
   └─ 从 252 数据库提取 outbound_body
   └─ 解析 JSON，提取 assistant 消息内容

2. 敏感词配置
   └─ 加载生产配置 (sensitive_patterns.yaml)
   └─ 添加测试专用敏感词（覆盖各种场景）

3. 批量审计
   └─ 初始化 FastDetector
   └─ 逐条审计，记录检测结果和耗时
   └─ 存储到临时表 audit_test_results

4. 结果分析
   └─ 性能统计（平均/P95/P99 耗时）
   └─ 准确率分析（人工标注 vs 机器检测）
   └─ 覆盖率统计（各类型命中分布）

5. 优化建议
   └─ Trie 树优化
   └─ 正则优化
   └─ 并发检测
   └─ 缓存策略
```

## 文件结构

```
tests/session_audit/
├── README.md                    # 本文件
├── 01_extract_data.go           # 数据提取脚本
├── 02_sensitive_words_test.yaml # 测试敏感词配置
├── 03_audit_test.go             # 审计测试主脚本
├── 04_analyze_results.go        # 结果分析脚本
├── schema.sql                   # 临时表结构
└── results/                     # 测试结果输出
    ├── performance.json
    ├── accuracy.json
    └── optimization_report.md
```

## 使用方法

### 1. 配置数据库连接
```bash
export DB_252_URL="postgresql://user:pass@14.103.112.252:5432/llm_gateway?sslmode=disable"
```

### 2. 创建临时表
```bash
psql $DB_252_URL < schema.sql
```

### 3. 运行测试
```bash
# 提取数据
go run 01_extract_data.go

# 运行审计测试
go run 03_audit_test.go

# 分析结果
go run 04_analyze_results.go
```

### 4. 查看报告
```bash
cat results/optimization_report.md
```

## 预期成果

1. **性能报告**：包含平均耗时、分位数、吞吐量等指标
2. **准确率报告**：包含 TPR、FPR、F1 分数等指标
3. **覆盖率报告**：各类敏感内容的检测统计
4. **优化建议**：针对性的性能和准确率优化方案

# 245 环境 48 小时监控计划

**开始时间**: 2026-08-29 22:50 CST  
**结束时间**: 2026-08-31 22:50 CST  
**监控目标**: 验证 SSE 验证层稳定性，为 154 生产部署做准备  

---

## 监控计划

### 检查点安排

| 检查点 | 时间 | 状态 | 报告文件 |
|--------|------|------|----------|
| Checkpoint #1 | 2026-08-29 22:50 | ✅ 完成 | `.handoff/2026-08-29-monitoring-checkpoint-1.md` |
| Checkpoint #2 | 2026-08-30 10:50 | ⏰ 已设置自动任务 | `.handoff/2026-08-29-monitoring-checkpoint-2.md` |
| Checkpoint #3 | 2026-08-30 22:50 | 📋 待执行 | `.handoff/2026-08-29-monitoring-checkpoint-3.md` |
| Checkpoint #4 | 2026-08-31 10:50 | 📋 待执行 | `.handoff/2026-08-29-monitoring-checkpoint-4.md` |
| 48h 总结 | 2026-08-31 22:50 | 📋 待执行 | `.handoff/2026-08-31-monitoring-48h-summary.md` |

---

## 自动化任务

### 已创建
- ✅ **Checkpoint #2**: 自动化任务已创建，将在 12 小时后（2026-08-30 10:50）执行

### 需手动执行
由于会话限制，Checkpoint #3、#4 需要手动执行或在新会话中创建自动化任务。

**手动执行命令**:
```bash
# Checkpoint #3 (24 小时后)
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
bash scripts/monitor-245-checkpoint.sh 3

# Checkpoint #4 (36 小时后)
bash scripts/monitor-245-checkpoint.sh 4
```

---

## 监控脚本使用

### 脚本位置
```
scripts/monitor-245-checkpoint.sh
```

### 使用方法
```bash
# 执行检查点 N (N = 1, 2, 3, 4)
bash scripts/monitor-245-checkpoint.sh N
```

### 脚本功能
1. 检查服务状态和运行时长
2. 查询最近 12 小时请求统计
3. 检查 malformed_sse_frame 错误
4. 分析错误类型分布
5. 统计 MiniMax 模型请求
6. 监控内存使用

---

## 监控指标

### 关键指标
- ✅ **malformed_sse_frame 错误数**: 目标 = 0
- 📊 **请求成功率**: 目标 > 85%
- 🎯 **MiniMax 模型稳定性**: minimax-m3 成功率 > 80%
- 💾 **内存使用**: < 90%
- ⏱️ **服务稳定性**: 无异常重启

### 评估标准

#### Go (可以部署到生产)
- ✅ 48 小时内 malformed_sse_frame 错误 = 0
- ✅ 请求成功率 ≥ 85%
- ✅ 无假阳性（误报）
- ✅ 服务稳定，无频繁重启
- ✅ 内存使用正常

#### No-Go (需要继续观察或修复)
- ❌ 出现 malformed_sse_frame 错误
- ❌ 成功率 < 80%
- ❌ 发现假阳性
- ❌ 服务不稳定
- ❌ 内存泄漏

---

## 检查点 #1 摘要

**时间**: 2026-08-29 22:50  
**窗口**: 最近 12 小时  

### 关键数据
- **总请求**: 965
- **成功率**: 88.39%
- **malformed 错误**: 0 ✅
- **MiniMax 请求**: 610 (84.93% 成功率)
- **服务状态**: 运行正常（25 分钟）
- **内存使用**: 2.7G / 3.0G (90%) ⚠️

### 结论
✅ SSE 验证层工作正常，0 错误  
✅ MiniMax 大流量稳定  
⚠️ 内存使用接近上限，需持续关注  

---

## 报告模板

每个检查点报告应包含:

### 1. 执行摘要
- 状态（正常/异常）
- 关键发现
- 与上次对比

### 2. 服务健康状态
- 服务状态（Active/Inactive）
- 运行时长
- 内存使用
- PID 和版本

### 3. 请求统计
- 总请求数
- 成功/失败数量
- 成功率
- 与上次对比

### 4. 错误分析
- 错误类型分布
- malformed_sse_frame 数量 ← **重点**
- 与历史数据对比

### 5. MiniMax/GLM 模型分析
- 请求分布
- 成功率
- 异常模式

### 6. SSE 验证效果评估
- 验证层工作状态
- 假阳性检查
- 性能影响

### 7. 与前次检查的对比
- 请求量趋势
- 成功率变化
- 错误模式变化

### 8. 风险评估
- 当前风险等级
- 需要关注的问题
- 缓解措施

### 9. 下一步行动
- 下次检查时间
- 关注重点

---

## 48 小时总结报告结构

最终总结报告（Checkpoint #4 之后）应包含:

### 1. 总览
- 监控时间范围
- 总检查点数量
- 整体状态

### 2. 汇总数据
- 4 次检查的关键指标表格
- 趋势图（如可能）

### 3. 趋势分析
- 请求量变化
- 成功率趋势
- 错误模式演变
- 内存使用趋势

### 4. SSE 验证层总评
- 总 malformed 错误数（目标: 0）
- 假阳性次数
- 性能影响
- 稳定性评估

### 5. Go/No-Go 决策
- **明确的决策建议**
- 决策依据
- 剩余风险
- 前置条件

### 6. 生产部署建议
- 部署时机
- 部署策略（全量/灰度）
- 监控重点
- 回滚预案

### 7. 后续监控计划
- 生产环境监控频率
- 关键指标
- 告警阈值

---

## 数据库查询参考

### 请求统计
```sql
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    COUNT(*) FILTER (WHERE success = false) AS failed_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) AS success_rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '12 hours';
```

### 错误分布
```sql
SELECT 
    error_kind,
    COUNT(*) AS count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) AS percentage
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '12 hours' AND success = false
GROUP BY error_kind
ORDER BY count DESC;
```

### MiniMax 模型
```sql
SELECT 
    COALESCE(outbound_model, client_model) AS model,
    COUNT(*) AS request_count,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) AS success_rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '12 hours'
  AND (outbound_model LIKE '%minimax%' OR client_model LIKE '%minimax%'
       OR outbound_model LIKE '%glm%' OR client_model LIKE '%glm%')
GROUP BY COALESCE(outbound_model, client_model)
ORDER BY request_count DESC;
```

---

## 紧急情况处理

### 如果发现 malformed_sse_frame 错误
1. 立即记录详细日志
2. 查询数据库获取完整请求信息
3. 分析是真阳性还是假阳性
4. 如果是假阳性，暂停生产部署计划
5. 如果是真阳性，确认验证器正常工作
6. 通知相关人员

### 如果服务不稳定
1. 检查内存使用
2. 查看错误日志
3. 考虑重启服务
4. 评估是否与 SSE 验证层相关
5. 必要时回滚

---

## 联系方式

### 监控负责人
- **执行**: AI Agent
- **审核**: DevOps Team

### 文档更新
- 每次检查后更新本文档
- 记录任何异常情况
- 保持与实际状态同步

---

**创建时间**: 2026-08-29 22:50  
**最后更新**: 2026-08-29 22:50  
**状态**: 🟢 监控进行中

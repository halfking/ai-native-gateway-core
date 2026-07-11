# 🚀 会话输出审计测试 - 快速开始

## 一分钟快速开始

```bash
cd tests/session_audit
./run_test.sh
```

## 这是什么？

这是一个完整的会话输出审计测试框架，用于：
- 测试 **FastDetector** 的性能和准确率
- 从 252 数据库提取 **真实的 LLM 响应数据**
- 检测 **敏感词、Prompt Injection、PII 泄露、Jailbreak**
- 生成 **性能报告和优化建议**

## 测试内容

✅ **数据来源**: 252 数据库（172.16.2.210）最近 7 天的 10,000+ 条真实 LLM 响应  
✅ **敏感词**: 73 个（政治/色情/暴力/违禁品/诈骗/PII/Injection/Jailbreak）
✅ **检测器**: FastDetector（Trie 树 + 正则表达式）  
✅ **存储**: 数据库表 + 统计视图  
✅ **分析**: 性能（P50/P95/P99）、准确率、覆盖率  

## 预期结果

运行测试后，你会得到：

1. **性能基线** - P50/P95/P99 耗时、吞吐量
2. **决策分布** - Pass/Warn/Block/NeedApproval 比例
3. **威胁统计** - Injection/PII/Jailbreak 检测数量
4. **优化建议** - 6 个优化方案（短期+中期+长期）

## 文件说明

| 文件 | 用途 |
|------|------|
| **run_test.sh** | 一键执行脚本 ⭐ |
| **cmd/audit-test/main.go** | 数据库测试主程序 |
| **cmd/audit-unit/main.go** | 单元测试主程序 |
| **02_sensitive_words_test.yaml** | 敏感词配置 |
| **schema.sql** | 数据库表结构 |
| **EXECUTION_GUIDE.md** | 详细执行指南 📖 |
| **OPTIMIZATION_REPORT_TEMPLATE.md** | 优化报告模板 |
| **DELIVERY_SUMMARY.md** | 完整交付总结 |

## 下一步

### 1. 立即执行
```bash
./run_test.sh
```

### 2. 查看结果
```bash
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway
```
```sql
-- 查看性能统计
SELECT * FROM v_audit_performance_summary 
WHERE test_run_id = 'test_xxx';

-- 查看敏感词排行
SELECT * FROM v_sensitive_words_ranking 
WHERE test_run_id = 'test_xxx' 
ORDER BY hit_count DESC LIMIT 20;
```

### 3. 人工标注（可选，评估准确率）
参考 `EXECUTION_GUIDE.md` 的“人工标注指南”章节

### 4. 实施优化
参考 `OPTIMIZATION_REPORT_TEMPLATE.md` 的优化方案

## 常见问题

**Q: 测试需要多长时间？**  
A: 约 1-2 分钟（10,000 条数据）

**Q: 会影响生产环境吗？**  
A: 不会。只读取数据库，不修改任何生产数据。测试结果保存在独立的临时表中。

**Q: 需要什么权限？**  
A: 只需要 252 数据库的 **读权限**（已配置）

**Q: 如何解读结果？**  
A: 参考 `EXECUTION_GUIDE.md` 的"结果分析"章节

**Q: 优化方案如何实施？**  
A: 参考 `OPTIMIZATION_REPORT_TEMPLATE.md` 的"行动计划"章节

## 技术支持

- 📖 详细文档: `EXECUTION_GUIDE.md`
- 📊 优化方案: `OPTIMIZATION_REPORT_TEMPLATE.md`
- 📦 交付总结: `DELIVERY_SUMMARY.md`

---

**现在就开始测试吧！** 🚀

```bash
./run_test.sh
```

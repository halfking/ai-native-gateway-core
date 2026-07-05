# 所有表数据完整性审计报告

**生成时间**: 2026-07-06 01:25:24
**数据库**: localhost:5432/llm_gateway
**审计表数**: 8

---

## 1. usage_ledger 审计\n\n
- 插入测试数据: 10/10\n
- UPDATE 测试: ✅ 通过\n
- 必填字段: ✅ 完整\n
- tokens 计算: ✅ 正确\n
\n
## 2. credit_ledger 审计\n\n
- 插入测试数据: 0/5\n
- 必填字段: ✅ 完整\n
\n
## 3. tool_usage_stats 审计\n\n
- 插入测试数据: 5/5\n
- 计数逻辑: ✅ 一致\n
\n
## 4. request_wal 审计\n\n
- 插入测试数据: 5/5\n
- UPDATE 测试: ✅ 通过\n
\n
## 5. routing_decision_log 审计\n\n
- 插入测试数据: 5/5\n
- 插入测试: ✅ 通过\n
\n
## 6. credential_model_index 审计\n\n
- success_rate 范围: ✅ 正确\n
\n
## 7. request_logs_bodies 审计\n\n
- 插入测试数据: 0/3\n
- JSONB 插入: ❌ 失败\n

# Bandit Scoring 部署检查清单

## 📋 部署前检查 (Pre-Deployment)

- [ ] 备份生产数据库
  ```bash
  pg_dump -U kxuser -d llm_gateway -t credentials > backup_$(date +%Y%m%d).sql
  ```

- [ ] 在测试环境验证 Migration
  ```bash
  psql -U kxuser -h test-db -d llm_gateway_test < deploy/sql/migrations/033_bandit_scoring.sql
  ```

- [ ] 验证代码编译
  ```bash
  go build ./cmd/gateway/...
  ```

- [ ] 准备回滚脚本
  ```bash
  cat > rollback.sql << 'EOF'
  ALTER TABLE credentials DROP COLUMN IF EXISTS bandit_alpha, ...;
  EOF
  ```

---

## 🚀 部署执行 (Deployment)

### Step 1: 运行 Migration (2-5 分钟)

- [ ] 在生产数据库运行 Migration
  ```bash
  psql -U kxuser -d llm_gateway < deploy/sql/migrations/033_bandit_scoring.sql
  ```

- [ ] 验证字段创建
  ```bash
  psql -U kxuser -d llm_gateway -c "\d credentials" | grep bandit
  ```
  **预期：** 看到 10+ 个 `bandit_*` 和 `penalty_*` 字段

---

### Step 2: 金丝雀部署 (第1天)

- [ ] 选择单个实例（5-10% 流量）
  ```bash
  ssh gateway-1.prod.example.com
  ```

- [ ] 设置环境变量
  ```bash
  export LLM_GATEWAY_ENABLE_BANDIT_SCORING=true
  ```

- [ ] 重启服务
  ```bash
  sudo systemctl restart llm-gateway
  ```

- [ ] 验证启动日志
  ```bash
  sudo journalctl -u llm-gateway -f | grep bandit
  ```
  **预期：** 
  ```
  bandit: loaded N credential scores from database
  bandit_scoring enabled=true flush_interval=10s batch_size=100
  ```

---

## ✅ 验证检查 (Verification)

### 即时验证 (部署后 5 分钟)

- [ ] 服务正常运行
  ```bash
  sudo systemctl status llm-gateway
  # Active: active (running)
  ```

- [ ] 日志无错误
  ```bash
  sudo journalctl -u llm-gateway --since "5 minutes ago" | grep -i error
  # 无 bandit 相关错误
  ```

- [ ] 请求正常响应
  ```bash
  curl -H "Authorization: Bearer $API_KEY" http://localhost:__PORT_12__/v1/models
  # 200 OK
  ```

---

### 1小时后验证

- [ ] 数据库已更新
  ```sql
  SELECT COUNT(*) FROM credentials 
  WHERE last_scored_at > NOW() - INTERVAL '1 hour';
  -- 应该 > 0
  ```

- [ ] Flush 正常工作
  ```bash
  sudo journalctl -u llm-gateway --since "1 hour ago" | grep "bandit flush completed"
  # 应该看到每 10s 一次的 flush 日志
  ```

- [ ] 错误率无异常
  ```bash
  # 对比启用前后的错误率
  ```

---

### 24小时后验证

- [ ] 冷启动测试
  ```bash
  # 重启服务
  sudo systemctl restart llm-gateway
  
  # 查看日志
  sudo journalctl -u llm-gateway --since "1 minute ago" | grep "bandit: loaded"
  # 应该看到：bandit: loaded N credential scores from database
  ```

- [ ] 数据完整性
  ```sql
  SELECT 
      COUNT(*) as total,
      AVG(bandit_success_count) as avg_success,
      AVG(bandit_failure_count) as avg_failure
  FROM credentials
  WHERE last_scored_at IS NOT NULL;
  ```

- [ ] 性能指标
  - [ ] 请求成功率 ≥ 95%
  - [ ] P95 延迟 ≤ 启用前
  - [ ] 429 错误率 ≤ 启用前

---

## 📈 扩展部署 (Scale-out)

### 第 4-7 天：扩展到 50%

- [ ] 在更多实例启用
  ```bash
  for host in gateway-{2,3,4,5}; do
    ssh $host "export LLM_GATEWAY_ENABLE_BANDIT_SCORING=true && sudo systemctl restart llm-gateway"
  done
  ```

- [ ] 验证每个实例
  ```bash
  for host in gateway-{2,3,4,5}; do
    ssh $host "sudo journalctl -u llm-gateway --since '1 min ago' | grep bandit_scoring"
  done
  ```

---

### 第 7-14 天：全量部署

- [ ] 在所有实例启用
- [ ] 最终验证
- [ ] 更新监控仪表板

---

## 🔄 回滚方案 (Rollback)

### 快速回滚（如发现问题）

- [ ] 禁用 Bandit Scoring
  ```bash
  export LLM_GATEWAY_ENABLE_BANDIT_SCORING=false
  sudo systemctl restart llm-gateway
  ```

- [ ] 验证回滚
  ```bash
  sudo journalctl -u llm-gateway | grep bandit
  # 应该不再有新的 bandit 日志
  ```

---

### 完全回滚 Migration（如需删除字段）

⚠️ **警告：这会删除所有历史数据！**

- [ ] 运行回滚脚本
  ```bash
  psql -U kxuser -d llm_gateway < rollback_033.sql
  ```

- [ ] 验证字段删除
  ```bash
  psql -U kxuser -d llm_gateway -c "\d credentials" | grep bandit
  # 应该无输出
  ```

---

## 🎯 成功标准

部署成功的标志：

✅ Migration 成功应用（所有字段创建）  
✅ 服务正常启动（日志显示 bandit enabled）  
✅ 数据库正常更新（last_scored_at 有值）  
✅ 冷启动恢复正常（LoadFromDB 成功）  
✅ 错误率无增加  
✅ 延迟无恶化  
✅ 429 错误率降低（预期优化目标）  

---

## 📞 联系方式

**遇到问题？**
- 查看详细部署文档：`DEPLOYMENT-GUIDE.md`
- 查看故障排查：`DEPLOYMENT-GUIDE.md#故障排查`
- 联系 DevOps 团队

---

**更新时间：** 2026-06-26  
**文档版本：** v1.0

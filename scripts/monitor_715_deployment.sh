#!/bin/bash
# monitor_715_deployment.sh
# 自动化监控 715 部署后的关键指标

DB_DSN="${DB_DSN:-postgres://user:pass@localhost:5432/gateway}"
LOG_FILE="monitor_715_$(date +%Y%m%d_%H%M%S).log"

echo "=== 715 Deployment Monitoring Started ===" | tee -a "$LOG_FILE"
echo "Time: $(date)" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 1. 事件状态分布
echo "1. Event State Distribution:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT state, COUNT(*) as count
FROM route_incidents
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY state;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 2. Failure Streak 分布
echo "2. Failure Streak vs State:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT failure_streak, state, COUNT(*) as count
FROM route_incidents
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY failure_streak, state
ORDER BY failure_streak, state;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 3. 凭证状态概览
echo "3. Credential States:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT 
    COUNT(*) as total,
    SUM(CASE WHEN available THEN 1 ELSE 0 END) as available,
    SUM(CASE WHEN NOT available THEN 1 ELSE 0 END) as unavailable,
    SUM(CASE WHEN recover_at > NOW() THEN 1 ELSE 0 END) as cooling
FROM credential_states;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 4. 异常状态检查
echo "4. Anomaly Check:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT 'Cooling but available' as issue, COUNT(*) as count
FROM credential_states
WHERE recover_at > NOW() AND available = true
UNION ALL
SELECT 'Pending with streak >= 3', COUNT(*)
FROM route_incidents
WHERE state = 'pending' AND failure_streak >= 3
UNION ALL
SELECT 'Active with streak < 3', COUNT(*)
FROM route_incidents
WHERE state = 'active' AND failure_streak < 3;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 5. 关键路由状态（245 服务器）
echo "5. Key Routes (apiclaude/apigpt/suyun):" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT model, state, failure_streak, 
       created_at, visible_at, last_failure_at
FROM route_incidents
WHERE (model = 'claude-3-sonnet' OR model LIKE 'gpt-%' OR model = 'suyun-v1')
  AND created_at > NOW() - INTERVAL '2 hours'
ORDER BY created_at DESC
LIMIT 10;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

echo "=== Monitoring Complete ===" | tee -a "$LOG_FILE"
echo "Log saved to: $LOG_FILE"

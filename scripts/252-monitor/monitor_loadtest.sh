#!/bin/bash
# 监控告警：loadtest 模型请求监控
# 每 5 分钟执行，发现 loadtest 请求立即报警
#
# 2026-10-15 252 审计轮修正：860 改名（request_logs → _request_logs）后本脚本
# 的旧表名查询每 5min 报一次 42P01（12/h 自 10-14 13:15 起）。读源改指
# canonical 视图 request_logs_with_current_month（= session_turns_hot ∪
# session_turns ∪ _request_logs 反连接去重）：
#   · 停写（storage.request_logs_write_enabled=false）后新流量只进会话族，
#     v1 三臂冻结——视图是官方读侧契约（S3 波 2/3 各读点同款）；
#   · client_model 在翻转后的视图行 100% 填充（252 实测 1,567/1,567），
#     过滤语义与 v1 时代一致；
#   · 5min 窗走 862 的 (ts) 前导索引，亚秒级。
RESULT=$(docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -tAc "SELECT count(*) FROM request_logs_with_current_month WHERE ts > NOW() - INTERVAL '5 minutes' AND client_model LIKE 'loadtest-%' HAVING count(*) > 0" 2>/dev/null)

if [ -n "$RESULT" ] && [ "$RESULT" -gt 0 ] 2>/dev/null; then
  echo "[ALERT] Production has $RESULT loadtest-* requests in last 5 minutes at $(date)" | tee -a /var/log/loadtest_monitor.log
  /opt/scripts/notify.sh loadtest_alert "$RESULT loadtest requests in production" 2>/dev/null || true
fi

#!/bin/bash
# 诊断 NVIDIA NIM minimax-m2.7 路由问题

echo "=== 检查 NVIDIA NIM provider 配置 ==="
psql -h localhost -U postgres -d llm_gateway -c "
SELECT id, code, name, base_url, protocol
FROM providers
WHERE code LIKE '%nvidia%' OR name LIKE '%NVIDIA%';
" 2>/dev/null || echo "无法连接数据库"

echo -e "\n=== 检查 NVIDIA credentials ==="
psql -h localhost -U postgres -d llm_gateway -c "
SELECT c.id, c.provider_id, p.code as provider_code, c.status, c.health_status
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE p.code LIKE '%nvidia%' OR p.name LIKE '%NVIDIA%';
" 2>/dev/null || echo "无法连接数据库"

echo -e "\n=== 检查 provider_models 中的 minimax 模型 ==="
psql -h localhost -U postgres -d llm_gateway -c "
SELECT pm.id, pm.provider_id, p.code as provider_code,
       pm.raw_model_name, pm.standardized_name, pm.outbound_model_name,
       pm.available
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name LIKE '%minimax%m2.7%'
   OR pm.standardized_name LIKE '%minimax%m2.7%';
" 2>/dev/null || echo "无法连接数据库"

echo -e "\n=== 检查 model_offers 视图 ==="
psql -h localhost -U postgres -d llm_gateway -c "
SELECT mo.id, mo.credential_id, mo.raw_model_name, mo.standardized_name,
       mo.outbound_model_name, mo.available
FROM model_offers mo
WHERE mo.raw_model_name LIKE '%minimax%m2.7%'
   OR mo.standardized_name LIKE '%minimax%m2.7%';
" 2>/dev/null || echo "无法连接数据库"

echo -e "\n=== 检查 model_aliases ==="
psql -h localhost -U postgres -d llm_gateway -c "
SELECT ma.id, ma.raw_name, ma.canonical_id, ma.status
FROM model_aliases ma
WHERE ma.raw_name LIKE '%minimax%m2.7%';
" 2>/dev/null || echo "无法连接数据库"

echo -e "\n=== 检查最近的请求日志 ==="
psql -h localhost -U postgres -d llm_gateway -c "
SELECT id, request_model, outbound_model, provider_id, credential_id,
       error_code, error_message, created_at
FROM request_logs
WHERE request_model LIKE '%minimax%m2.7%'
ORDER BY created_at DESC
LIMIT 10;
" 2>/dev/null || echo "无法连接数据库"

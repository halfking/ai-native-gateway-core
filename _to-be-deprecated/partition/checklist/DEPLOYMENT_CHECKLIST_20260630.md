# ⚠️ 本文件已废弃 / This File Is Deprecated

> **归档日期**: 2026-07-05
> **替代文档**: [`docs/partition/MONTHLY_CHECKLIST.md`](../../docs/partition/MONTHLY_CHECKLIST.md) 第 §4-Table 分区与归档部署检查 节
> **原因**: 内容已合并到主月度维护清单（4-Table 分区与归档部署清单 2026-06-30、镜像信息、k8s 部署步骤）
> **保留原因**: 提供历史归档追溯

---

# LLM Gateway 4-Table Partition & Archive 部署清单
## 2026-06-30

## 概述
本次部署扩展 partition & columnar archive 到 4 张表，添加 partition_manager 自动调度。

---

## 1. 前置条件检查

### 1.1 数据库状态（184 llm_gateway）
```bash
# SSH 到 184
ssh -p 25022 root@__INTERNAL_PUBLIC_IP__

# 检查 migrations 是否已应用
export PGPASSWORD='__REDACTED_DB_PASSWORD__'
POD=$(kubectl get pod -n pms-test -l app=llm-gateway-pg -o jsonpath="{.items[0].metadata.name}")

kubectl exec -n pms-test $POD -c citus -- psql -U llm_gateway -d llm_gateway -c "
SELECT 
  '317' AS migration, 
  CASE WHEN EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index' AND relkind = 'p') 
    THEN '✓ CMI is partitioned' ELSE '✗ CMI not partitioned' END AS status
UNION ALL
SELECT '318', CASE WHEN EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'archive_request_logs' AND pg_get_functiondef(oid) LIKE '%CHUNK_SIZE%') 
    THEN '✓ archive functions fixed' ELSE '✗ archive functions not fixed' END
UNION ALL
SELECT '319', CASE WHEN EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'ensure_credential_model_index_partition') 
    THEN '✓ ensure functions added' ELSE '✗ ensure functions missing' END;
"
```

**预期输出**：
```
 migration |           status            
-----------+-----------------------------
 317       | ✓ CMI is partitioned
 318       | ✓ archive functions fixed
 319       | ✓ ensure functions added
```

### 1.2 备份验证
```bash
ls -lh /opt/databackup/pg-daily/184/pg-full-184-20260630.dump
sha256sum /opt/databackup/pg-daily/184/pg-full-184-20260630.dump
# 预期 SHA256: 7e80d9aa6f886c484009839f6dc876a96f61c7547b7e464aefe1d6b8c7d23efd
```

### 1.3 Cron 检查
```bash
crontab -l | grep columnar
# 预期输出：
# 0 4 1-3 * * /opt/scripts/columnar-monthly-cron.sh >> /var/log/columnar-monthly.log 2>&1
```

---

## 2. 镜像准备

### 2.1 验证镜像存在
```bash
# 在本地或 registry 检查
curl -s https://registry.internal.example.com/v2/kx-llm-gateway-go/tags/list | grep -o 'gitsha-0b0d80e8'
```

**镜像信息**：
- 镜像名：`registry.internal.example.com/kx-llm-gateway-go:gitsha-0b0d80e8`
- Digest：`sha256:1ed8b062287f71910c9e51b783533ce7db770d795cc64c86532d18901587d1c3`
- 构建时间：2026-06-29T22:48:35Z
- Git SHA：0b0d80e8

---

## 3. 部署步骤

### 3.1 清理异常 Pods（如果有）
```bash
# 删除所有 Pending/Failed 状态的 gateway pods
kubectl delete pod -n pms-test -l app=llm-gateway-go \
  --field-selector="status.phase!=Running" \
  --force --grace-period=0
```

### 3.2 检查当前 Deployment
```bash
kubectl get deploy -n pms-test llm-gateway-go-deployment -o yaml | grep -E "image:|replicas:|imagePullPolicy:"
```

### 3.3 更新镜像
```bash
kubectl set image deployment/llm-gateway-go-deployment -n pms-test \
  llm-gateway-go=registry.internal.example.com/kx-llm-gateway-go:gitsha-0b0d80e8
```

### 3.4 监控 Rollout
```bash
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=180s
```

### 3.5 验证 Pod 启动
```bash
kubectl get pod -n pms-test -l app=llm-gateway-go -o wide
```

**预期输出**：
```
NAME                                         READY   STATUS    RESTARTS   AGE
llm-gateway-go-deployment-xxxxxxxxxx-xxxxx   1/1     Running   0          2m
```

---

## 4. 功能验证

### 4.1 健康检查
```bash
# 在 184 上
curl -s http://localhost:30082/healthz
# 预期输出: OK
```

### 4.2 版本验证
```bash
POD=$(kubectl get pod -n pms-test -l app=llm-gateway-go -o jsonpath="{.items[0].metadata.name}")
kubectl exec -n pms-test $POD -- cat /opt/llm-gateway-go/VERSION
# 预期输出: v0.0.0-0b0d80e8-2026-06-29T22:48:35Z-0
```

### 4.3 Partition Manager 启动验证
```bash
kubectl logs -n pms-test -l app=llm-gateway-go --tail=100 | grep -i "partition_manager"
```

**预期日志**：
```json
{"level":"INFO","msg":"partition_manager started","interval":"24h0m0s"}
```

### 4.4 Ensure Functions 调用验证
```bash
kubectl logs -n pms-test -l app=llm-gateway-go --tail=200 | grep -E "ensure.*partition|ensured partition"
```

**预期日志**（4 个 ensure 调用）：
```json
{"level":"INFO","msg":"partition_manager: ensured partition","fn":"ensure_request_logs_partition","label":"request_logs","month":"2026-06"}
{"level":"INFO","msg":"partition_manager: ensured partition","fn":"ensure_request_wal_partition","label":"request_wal","month":"2026-06"}
{"level":"INFO","msg":"partition_manager: ensured partition","fn":"ensure_routing_decision_log_partition","label":"routing_decision_log","month":"2026-06"}
{"level":"INFO","msg":"partition_manager: ensured partition","fn":"ensure_credential_model_index_partition","label":"credential_model_index","month":"2026-06"}
```

### 4.5 数据库端验证
```bash
export PGPASSWORD='__REDACTED_DB_PASSWORD__'
POD=$(kubectl get pod -n pms-test -l app=llm-gateway-pg -o jsonpath="{.items[0].metadata.name}")

# 验证 4 个表都有当前月和下月分区
kubectl exec -n pms-test $POD -c citus -- psql -U llm_gateway -d llm_gateway -c "
SELECT 
  p.relname AS parent_table,
  COUNT(c.relname) AS partition_count,
  string_agg(c.relname, ', ' ORDER BY c.relname) AS partitions
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname IN (
  'request_logs',
  'request_wal',
  'routing_decision_log',
  'credential_model_index'
)
  AND p.relnamespace = 'public'::regnamespace
GROUP BY p.relname
ORDER BY p.relname;
"
```

**预期输出**（每个表至少 2 个分区：2026_06 + 2026_07）：
```
      parent_table       | partition_count |                         partitions                          
-------------------------+-----------------+------------------------------------------------------------
 credential_model_index  |               4 | credential_model_index_2026_06, credential_model_index_2026_07, ...
 request_logs            |               3 | request_logs_2026_06, request_logs_2026_07, ...
 request_wal             |               2 | request_wal_2026_06, request_wal_2026_07
 routing_decision_log    |               3 | routing_decision_log_2026_06, routing_decision_log_2026_07, ...
```

### 4.6 Archive 数据验证
```bash
kubectl exec -n pms-test $POD -c citus -- psql -U llm_gateway -d llm_gateway -c "
SELECT 
  'credential_model_index_archive' AS table, count(*) AS rows FROM credential_model_index_archive
UNION ALL
SELECT 'routing_decision_log_archive', count(*) FROM routing_decision_log_archive
UNION ALL
SELECT 'request_wal_archive', count(*) FROM request_wal_archive
UNION ALL
SELECT 'request_logs_archive', count(*) FROM request_logs_archive;
"
```

**预期输出**：
```
              table               |  rows  
----------------------------------+--------
 credential_model_index_archive   |      9
 routing_decision_log_archive     |  21758
 request_wal_archive              |  13848
 request_logs_archive             |  11328
```

---

## 5. 回滚方案（如有问题）

### 5.1 回滚到上一个版本
```bash
kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test
```

### 5.2 或指定旧镜像
```bash
kubectl set image deployment/llm-gateway-go-deployment -n pms-test \
  llm-gateway-go=kx-llm-gateway-go:gitsha-13fb28e7
```

### 5.3 验证回滚成功
```bash
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test
curl http://localhost:30082/healthz
```

---

## 6. 监控指标

### 6.1 关键日志监控
```bash
# 持续监控 partition_manager 日志
kubectl logs -n pms-test -l app=llm-gateway-go -f | grep -i partition
```

### 6.2 性能指标
- Gateway 响应时间：/healthz 应 < 50ms
- Pod 内存使用：< 500MB（正常运行）
- Pod CPU 使用：< 0.5 core（空闲时）

### 6.3 数据库监控
```bash
# 查看主表大小（应该比归档前小）
kubectl exec -n pms-test $POD -c citus -- psql -U llm_gateway -d llm_gateway -c "
SELECT 
  relname,
  pg_size_pretty(pg_total_relation_size(oid)) AS size,
  (SELECT count(*) FROM credential_model_index) AS rows
FROM pg_class 
WHERE relname = 'credential_model_index' 
  AND relnamespace = 'public'::regnamespace;
"
```

---

## 7. 已知问题与注意事项

### 7.1 request_logs_archive 使用 heap
- **原因**：JSONB 列太大（>1MB/行），columnar 会 OOM
- **影响**：request_logs_archive 压缩比低于其他 3 个表
- **未来优化**：拆分 JSONB 到独立表

### 7.2 credential_model_index 7d cutoff
- **行为**：只归档 7 天前的数据，主表保留 7d 内数据
- **影响**：主表行数仍然较多（~186K 行）
- **监控**：定期检查主表行数增长

### 7.3 Cron 执行时间
- **时间**：每月 1-3 日 04:00
- **分散**：day1=RL+RDL, day2=WAL, day3=CMI
- **日志**：`/var/log/columnar-monthly.log`

---

## 8. 联系方式

- **代码仓库**：`official-deploy/llm-gateway-go`
- **Git commit**：`0b0d80e8` (feature/pr9-cleanup-response-interceptor-stub)
- **Migrations**：317, 318, 318b, 319
- **部署日期**：2026-06-30
- **备份位置**：`184:/opt/databackup/pg-daily/184/pg-full-184-20260630.dump`

---

## 附录 A：快速命令参考

```bash
# 一键部署
kubectl set image deployment/llm-gateway-go-deployment -n pms-test \
  llm-gateway-go=registry.internal.example.com/kx-llm-gateway-go:gitsha-0b0d80e8 && \
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test

# 一键验证
curl http://localhost:30082/healthz && \
kubectl logs -n pms-test -l app=llm-gateway-go --tail=50 | grep partition_manager

# 一键回滚
kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test && \
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test
```

---

**部署清单版本**：v1.0  
**最后更新**：2026-06-30 06:50 UTC

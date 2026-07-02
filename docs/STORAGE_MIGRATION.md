# 存储迁移指南

本文档说明如何将 LLM Gateway 的日志和附件存储从容器内迁移到持久化存储。

## 背景

默认情况下，LLM Gateway 的附件和日志文件存储在容器内文件系统中：
- **附件存储**：`/data/attachments`（可通过 `LLM_GATEWAY_ATTACHMENT_DIR` 配置）
- **日志存储**：`./data/attachments`（可通过 `LLM_GATEWAY_LOG_FILE` 配置）

容器重启或重新部署时，这些数据会丢失。为确保数据持久化，需要将存储迁移到容器外。

## 迁移方案

### 方案 1：Docker Compose 部署

使用 `docker-compose.persistent.yml` 配置文件，该配置已包含持久化存储挂载。

#### 1. 使用持久化配置启动

```bash
# 使用持久化配置启动服务
docker-compose -f docker-compose.persistent.yml up -d
```

#### 2. 迁移现有数据（如果需要）

如果已有运行中的容器，需要先迁移数据：

```bash
# 查找现有容器 ID
CONTAINER_ID=$(docker ps -q -f name=llm-gateway)

# 创建宿主机存储目录
mkdir -p ./data/attachments
mkdir -p ./data/logs

# 从容器复制现有附件数据
docker cp $CONTAINER_ID:/data/attachments/. ./data/attachments/

# 从容器复制现有日志数据（如果有）
docker cp $CONTAINER_ID:/var/log/llm-gateway/. ./data/logs/ 2>/dev/null || true

# 停止旧容器
docker stop $CONTAINER_ID

# 使用新配置启动
docker-compose -f docker-compose.persistent.yml up -d
```

#### 3. 验证数据持久化

```bash
# 上传测试附件或查看现有附件
curl -X GET http://localhost:8080/admin/storage/config

# 重启容器
docker-compose -f docker-compose.persistent.yml restart

# 验证数据仍然存在
ls -lh ./data/attachments
ls -lh ./data/logs
```

### 方案 2：Kubernetes 部署

使用 `deploy/k8s/storage-pvc.yaml` 配置文件创建持久化存储卷。

#### 1. 创建 PVC 和 PV

```bash
# 应用存储配置
kubectl apply -f deploy/k8s/storage-pvc.yaml

# 验证 PVC 状态
kubectl get pvc -n llm-gateway
```

#### 2. 更新部署配置

在现有的 Deployment 配置中添加 volume 挂载：

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: llm-gateway
  namespace: llm-gateway
spec:
  template:
    spec:
      containers:
      - name: llm-gateway
        image: your-registry/llm-gateway:latest
        env:
        - name: LLM_GATEWAY_ATTACHMENT_DIR
          value: "/data/attachments"
        - name: LLM_GATEWAY_LOG_FILE
          value: "/var/log/llm-gateway/gateway.log"
        volumeMounts:
        - name: attachments-storage
          mountPath: /data/attachments
        - name: logs-storage
          mountPath: /var/log/llm-gateway
      volumes:
      - name: attachments-storage
        persistentVolumeClaim:
          claimName: llm-gateway-attachments-pvc
      - name: logs-storage
        persistentVolumeClaim:
          claimName: llm-gateway-logs-pvc
```

#### 3. 迁移现有数据（如果需要）

如果已有运行中的 Pod，需要先迁移数据：

```bash
# 查找现有 Pod
POD_NAME=$(kubectl get pod -n llm-gateway -l app=llm-gateway -o jsonpath='{.items[0].metadata.name}')

# 创建临时迁移 Pod
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: data-migration
  namespace: llm-gateway
spec:
  containers:
  - name: migration
    image: busybox
    command: ['sleep', '3600']
    volumeMounts:
    - name: attachments-storage
      mountPath: /target/attachments
    - name: logs-storage
      mountPath: /target/logs
  volumes:
  - name: attachments-storage
    persistentVolumeClaim:
      claimName: llm-gateway-attachments-pvc
  - name: logs-storage
    persistentVolumeClaim:
      claimName: llm-gateway-logs-pvc
EOF

# 等待迁移 Pod 就绪
kubectl wait --for=condition=ready pod/data-migration -n llm-gateway --timeout=60s

# 从现有 Pod 复制附件数据到临时 Pod
kubectl exec -n llm-gateway $POD_NAME -- tar czf - -C /data attachments | \
  kubectl exec -i -n llm-gateway data-migration -- tar xzf - -C /target

# 从现有 Pod 复制日志数据到临时 Pod（如果有）
kubectl exec -n llm-gateway $POD_NAME -- tar czf - -C /var/log llm-gateway 2>/dev/null | \
  kubectl exec -i -n llm-gateway data-migration -- tar xzf - -C /target || true

# 验证数据已复制
kubectl exec -n llm-gateway data-migration -- ls -lh /target/attachments
kubectl exec -n llm-gateway data-migration -- ls -lh /target/logs

# 删除临时 Pod
kubectl delete pod data-migration -n llm-gateway
```

#### 4. 应用新的部署配置

```bash
# 应用更新后的部署配置
kubectl apply -f deploy/k8s/deployment.yaml

# 验证 Pod 正常运行
kubectl get pods -n llm-gateway

# 验证存储挂载
kubectl exec -n llm-gateway deployment/llm-gateway -- df -h /data/attachments
kubectl exec -n llm-gateway deployment/llm-gateway -- df -h /var/log/llm-gateway
```

## 存储配置说明

### 环境变量

在 `.env` 文件或 Kubernetes ConfigMap/Secret 中配置：

```bash
# 附件存储目录（默认：./data/attachments）
LLM_GATEWAY_ATTACHMENT_DIR=/data/attachments

# 日志文件路径（留空则输出到 stderr）
LLM_GATEWAY_LOG_FILE=/var/log/llm-gateway/gateway.log

# 日志轮转配置
LLM_GATEWAY_LOG_MAX_SIZE_MB=100
LLM_GATEWAY_LOG_MAX_BACKUPS=10
LLM_GATEWAY_LOG_MAX_AGE_DAYS=7
LLM_GATEWAY_LOG_COMPRESS=true
```

### 存储路径约定

- **附件存储**：`/data/attachments`
  - 按日期和内容哈希组织：`YYYY/MM/DD/{content-hash}.ext`
  - 支持文件去重（相同内容只存储一次）
  
- **日志存储**：`/var/log/llm-gateway`
  - 主日志文件：`gateway.log`
  - 轮转日志：`gateway.log.1.gz`, `gateway.log.2.gz`, ...

## 存储空间规划

### 附件存储

- **预估用量**：根据业务量评估
  - 平均附件大小 × 日请求量 × 保留天数
  - 文件去重可节省 30-50% 空间
  
- **建议配置**：
  - 小型部署（< 1000 请求/天）：10GB
  - 中型部署（1000-10000 请求/天）：100GB
  - 大型部署（> 10000 请求/天）：500GB+

### 日志存储

- **默认配置**：100MB × 10 文件 ≈ 1GB
- **建议配置**：根据日志级别和保留时间调整
  - DEBUG 级别：2-5GB
  - INFO 级别：1-2GB
  - WARN/ERROR 级别：500MB

## 数据清理策略

可通过 Admin API 配置数据生命周期策略：

```bash
# 查看当前配置
curl -X GET http://localhost:8080/admin/storage/config

# 配置附件保留策略（保留 90 天）
curl -X POST http://localhost:8080/admin/storage/config \
  -H "Content-Type: application/json" \
  -d '{
    "attachment_retention_days": 90,
    "enable_auto_cleanup": true
  }'
```

## 生产环境建议

### 1. 使用专业存储方案

对于生产环境，建议使用：
- **对象存储**：S3、OSS、MinIO（代码已预留接口，可扩展）
- **网络存储**：NFS、Ceph、GlusterFS
- **云存储卷**：AWS EBS、Azure Disk、GCP Persistent Disk

### 2. 配置备份策略

```bash
# 示例：使用 rsync 定期备份附件
rsync -avz --delete /data/attachments/ backup-server:/backups/attachments/

# 示例：使用 tar 创建定期归档
tar czf attachments-$(date +%Y%m%d).tar.gz -C /data attachments
```

### 3. 监控存储使用情况

```bash
# 监控磁盘使用率
df -h /data/attachments
df -h /var/log/llm-gateway

# 统计附件数量和大小
find /data/attachments -type f | wc -l
du -sh /data/attachments
```

### 4. 设置告警阈值

- 磁盘使用率 > 80%：警告
- 磁盘使用率 > 90%：紧急
- 可用空间 < 5GB：警告

## 故障排查

### 问题 1：容器无权限写入挂载目录

```bash
# 检查目录权限
ls -ld /data/attachments

# 修改目录所有者（假设容器内运行用户 UID 为 1000）
chown -R 1000:1000 /data/attachments
chmod -R 755 /data/attachments
```

### 问题 2：PVC 无法绑定

```bash
# 检查 PVC 状态
kubectl get pvc -n llm-gateway

# 查看 PVC 事件
kubectl describe pvc llm-gateway-attachments-pvc -n llm-gateway

# 检查存储类
kubectl get storageclass
```

### 问题 3：存储空间不足

```bash
# 临时清理旧文件
find /data/attachments -type f -mtime +90 -delete

# 或使用 Admin API 触发自动清理
curl -X POST http://localhost:8080/admin/data-lifecycle/cleanup
```

## 参考资料

- [附件存储实现](../domains/attachments/storage.go)
- [存储配置 API](../admin/storage_config.go)
- [日志配置](../internal/logging/logging.go)
- [Docker Compose 持久化配置](../docker-compose.persistent.yml)
- [Kubernetes 存储配置](../deploy/k8s/storage-pvc.yaml)

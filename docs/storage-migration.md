# 存储迁移指南

## 概述

本文档说明如何将 LLM Gateway 的日志和附件存储从容器内迁移到持久化存储，确保容器重启后数据不丢失。

## 背景

### 当前存储方案

- **附件存储**：默认存储在容器内 `/data/attachments` 目录
- **日志存储**：默认输出到 stderr，如配置文件日志则存储到指定路径
- **问题**：容器重启或重新部署时，未挂载持久化存储的数据将丢失

### 目标存储方案

- **Docker Compose**：使用命名卷（named volume）或绑定挂载（bind mount）持久化数据
- **Kubernetes**：使用 PVC/PV 持久化卷存储数据
- **统一标准**：日志和附件使用独立存储卷，便于备份和管理

## 配置说明

### 环境变量

在 `.env` 文件或容器环境变量中配置以下参数：

```bash
# 附件存储目录（默认：./data/attachments）
LLM_GATEWAY_ATTACHMENT_DIR=/data/attachments

# 日志文件路径（留空则输出到 stderr）
LLM_GATEWAY_LOG_FILE=/var/log/llm-gateway/gateway.log

# 日志轮转配置
LLM_GATEWAY_LOG_MAX_SIZE_MB=100      # 单个日志文件最大大小（MB）
LLM_GATEWAY_LOG_MAX_BACKUPS=10       # 保留的旧日志文件数量
LLM_GATEWAY_LOG_MAX_AGE_DAYS=7       # 日志文件保留天数
LLM_GATEWAY_LOG_COMPRESS=true        # 是否压缩旧日志文件
```

## Docker Compose 部署

### 使用持久化配置

项目已提供 `docker-compose.persistent.yml` 配置文件，包含完整的持久化存储方案。

#### 启动服务

```bash
# 使用持久化配置启动
docker-compose -f docker-compose.persistent.yml up -d

# 或与现有配置合并使用
docker-compose -f docker-compose.yml -f docker-compose.persistent.yml up -d
```

#### 存储卷说明

配置文件创建以下命名卷：

- `llm-gateway-attachments`：附件存储卷，挂载到 `/data/attachments`
- `llm-gateway-logs`：日志存储卷，挂载到 `/var/log/llm-gateway`

### 自定义配置

如需使用绑定挂载（bind mount）将数据存储到宿主机特定目录：

```yaml
services:
  llm-gateway:
    volumes:
      # 绑定挂载到宿主机目录
      - /host/path/to/attachments:/data/attachments
      - /host/path/to/logs:/var/log/llm-gateway
    environment:
      - LLM_GATEWAY_ATTACHMENT_DIR=/data/attachments
      - LLM_GATEWAY_LOG_FILE=/var/log/llm-gateway/gateway.log
```

### 数据迁移步骤

#### 1. 备份现有数据

```bash
# 查找运行中的容器
docker ps | grep llm-gateway

# 从容器中拷贝现有附件数据
docker cp <container_id>:/data/attachments ./backup/attachments

# 从容器中拷贝现有日志数据（如果配置了文件日志）
docker cp <container_id>:/var/log/llm-gateway ./backup/logs
```

#### 2. 停止现有服务

```bash
docker-compose down
```

#### 3. 创建持久化卷

```bash
# 创建命名卷
docker volume create llm-gateway-attachments
docker volume create llm-gateway-logs
```

#### 4. 恢复数据到持久化卷

```bash
# 启动临时容器挂载卷并拷贝数据
docker run --rm -v llm-gateway-attachments:/data/attachments \
  -v $(pwd)/backup/attachments:/backup \
  alpine sh -c "cp -r /backup/* /data/attachments/"

docker run --rm -v llm-gateway-logs:/var/log/llm-gateway \
  -v $(pwd)/backup/logs:/backup \
  alpine sh -c "cp -r /backup/* /var/log/llm-gateway/"
```

#### 5. 启动新配置

```bash
docker-compose -f docker-compose.persistent.yml up -d
```

#### 6. 验证数据

```bash
# 验证附件数据
docker exec <container_id> ls -lh /data/attachments

# 验证日志数据
docker exec <container_id> ls -lh /var/log/llm-gateway

# 检查应用日志确认无错误
docker-compose logs -f
```

## Kubernetes 部署

### 使用 PVC/PV 配置

项目已提供 `deploy/k8s/storage-pvc.yaml` 配置文件。

#### 部署持久化存储

```bash
# 应用 PVC/PV 配置
kubectl apply -f deploy/k8s/storage-pvc.yaml

# 查看 PV 状态
kubectl get pv

# 查看 PVC 状态
kubectl get pvc -n <namespace>
```

#### 更新 Deployment 配置

在现有 Deployment YAML 中添加卷挂载配置：

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: llm-gateway
spec:
  template:
    spec:
      containers:
      - name: llm-gateway
        env:
        - name: LLM_GATEWAY_ATTACHMENT_DIR
          value: /data/attachments
        - name: LLM_GATEWAY_LOG_FILE
          value: /var/log/llm-gateway/gateway.log
        volumeMounts:
        - name: attachments
          mountPath: /data/attachments
        - name: logs
          mountPath: /var/log/llm-gateway
      volumes:
      - name: attachments
        persistentVolumeClaim:
          claimName: llm-gateway-attachments-pvc
      - name: logs
        persistentVolumeClaim:
          claimName: llm-gateway-logs-pvc
```

### 数据迁移步骤

#### 1. 备份现有数据

```bash
# 查找运行中的 Pod
kubectl get pods -n <namespace>

# 从 Pod 中拷贝现有附件数据
kubectl cp <namespace>/<pod_name>:/data/attachments ./backup/attachments

# 从 Pod 中拷贝现有日志数据
kubectl cp <namespace>/<pod_name>:/var/log/llm-gateway ./backup/logs
```

#### 2. 创建 PVC/PV

```bash
kubectl apply -f deploy/k8s/storage-pvc.yaml
```

#### 3. 创建临时 Pod 恢复数据

```bash
# 创建临时 Pod 挂载 PVC
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: data-migration
  namespace: <namespace>
spec:
  containers:
  - name: migration
    image: alpine
    command: ["sleep", "3600"]
    volumeMounts:
    - name: attachments
      mountPath: /data/attachments
    - name: logs
      mountPath: /var/log/llm-gateway
  volumes:
  - name: attachments
    persistentVolumeClaim:
      claimName: llm-gateway-attachments-pvc
  - name: logs
    persistentVolumeClaim:
      claimName: llm-gateway-logs-pvc
EOF

# 等待 Pod 启动
kubectl wait --for=condition=ready pod/data-migration -n <namespace>

# 拷贝附件数据
kubectl cp ./backup/attachments <namespace>/data-migration:/data/

# 拷贝日志数据
kubectl cp ./backup/logs <namespace>/data-migration:/var/log/llm-gateway/

# 删除临时 Pod
kubectl delete pod data-migration -n <namespace>
```

#### 4. 更新 Deployment

```bash
# 应用更新后的 Deployment 配置
kubectl apply -f deploy/k8s/deployment.yaml

# 等待 Pod 重新启动
kubectl rollout status deployment/llm-gateway -n <namespace>
```

#### 5. 验证数据

```bash
# 查找新 Pod
kubectl get pods -n <namespace>

# 验证附件数据
kubectl exec -it <pod_name> -n <namespace> -- ls -lh /data/attachments

# 验证日志数据
kubectl exec -it <pod_name> -n <namespace> -- ls -lh /var/log/llm-gateway

# 检查应用日志
kubectl logs -f <pod_name> -n <namespace>
```

## 生产环境建议

### 存储选型

#### Docker Compose

- **开发环境**：使用命名卷（named volume），简单快速
- **生产环境**：使用绑定挂载（bind mount）到专用存储目录，便于备份和监控

#### Kubernetes

- **小规模部署**：使用 hostPath 或本地 PV，简单直接
- **生产环境**：使用云存储（EBS、Azure Disk、GCE PD）或网络存储（NFS、Ceph）
- **高可用场景**：使用 ReadWriteMany (RWX) 存储类，支持多副本共享访问

### 存储容量规划

#### 附件存储

- **估算方法**：
  - 平均每个请求附件大小：5MB
  - 每日请求量：10,000 次
  - 附件比例：10%
  - 每日增长：10,000 × 10% × 5MB = 5GB
  - 保留 30 天：5GB × 30 = 150GB

- **建议配置**：
  - 初始容量：200GB
  - 预留扩展空间：50%
  - 配置自动清理策略（见下文）

#### 日志存储

- **估算方法**：
  - 每个请求日志大小：2KB
  - 每日请求量：10,000 次
  - 每日增长：10,000 × 2KB = 20MB
  - 保留 7 天：20MB × 7 = 140MB

- **建议配置**：
  - 初始容量：1GB
  - 日志轮转：100MB × 10 文件 = 1GB
  - 自动压缩和清理

### 数据清理策略

系统提供自动数据清理功能，通过管理 API 配置。

#### 配置附件保留策略

```bash
# 设置保留策略：30 天
curl -X PUT http://localhost:8080/admin/storage/retention \
  -H "Content-Type: application/json" \
  -d '{
    "max_age_days": 30,
    "enabled": true
  }'

# 手动触发清理
curl -X POST http://localhost:8080/admin/storage/cleanup
```

#### Kubernetes CronJob 自动清理

可配置 CronJob 定期执行清理任务：

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: llm-gateway-cleanup
  namespace: <namespace>
spec:
  schedule: "0 2 * * *"  # 每天凌晨 2 点执行
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: cleanup
            image: curlimages/curl
            command:
            - /bin/sh
            - -c
            - |
              curl -X POST http://llm-gateway:8080/admin/storage/cleanup
          restartPolicy: OnFailure
```

### 监控和告警

#### 存储使用监控

**Docker Compose**：

```bash
# 查看卷使用情况
docker system df -v

# 监控卷大小变化
watch -n 60 'docker system df -v | grep llm-gateway'
```

**Kubernetes**：

```bash
# 查看 PVC 使用情况
kubectl get pvc -n <namespace>

# 进入 Pod 查看磁盘使用
kubectl exec -it <pod_name> -n <namespace> -- df -h
```

#### 告警阈值建议

- **附件存储**：使用率超过 80% 时告警
- **日志存储**：使用率超过 90% 时告警
- **清理失败**：连续失败 3 次时告警

### 备份策略

#### 附件备份

```bash
# Docker 卷备份
docker run --rm -v llm-gateway-attachments:/data \
  -v $(pwd)/backup:/backup \
  alpine tar czf /backup/attachments-$(date +%Y%m%d).tar.gz -C /data .

# Kubernetes PVC 备份
kubectl exec <pod_name> -n <namespace> -- \
  tar czf - -C /data/attachments . > attachments-$(date +%Y%m%d).tar.gz
```

#### 日志备份

建议使用集中式日志收集方案（如 ELK、Loki），而非备份日志文件。

## 故障排查

### 常见问题

#### 1. 容器无法启动：权限问题

**症状**：容器日志显示无法写入 `/data/attachments` 或 `/var/log/llm-gateway`

**解决方案**：

```bash
# 检查卷挂载权限
docker exec <container_id> ls -ld /data/attachments

# 修复权限（临时容器）
docker run --rm -v llm-gateway-attachments:/data alpine \
  sh -c "chown -R 1000:1000 /data && chmod -R 755 /data"
```

#### 2. 数据迁移后找不到旧附件

**症状**：应用日志显示 404 或附件不存在

**检查步骤**：

```bash
# 1. 确认附件目录结构
docker exec <container_id> find /data/attachments -type f | head -20

# 2. 检查附件路径配置
docker exec <container_id> env | grep ATTACHMENT_DIR

# 3. 查看应用日志中的存储路径
docker logs <container_id> | grep -i "attachment"
```

**解决方案**：

- 确保目录结构与原容器一致（按日期和 hash 分层）
- 检查 `LLM_GATEWAY_ATTACHMENT_DIR` 环境变量配置正确

#### 3. PVC 无法绑定到 PV

**症状**：PVC 状态为 `Pending`

**检查步骤**：

```bash
# 查看 PVC 详情
kubectl describe pvc llm-gateway-attachments-pvc -n <namespace>

# 查看 PV 状态
kubectl get pv
```

**常见原因**：

- PV 的 `storageClassName` 与 PVC 不匹配
- PV 的 `capacity` 小于 PVC 请求的容量
- PV 已被其他 PVC 绑定
- hostPath 路径在节点上不存在

**解决方案**：

```bash
# 修正 storageClassName
kubectl edit pvc llm-gateway-attachments-pvc -n <namespace>

# 或重新创建 PV
kubectl delete pv llm-gateway-attachments-pv
kubectl apply -f deploy/k8s/storage-pvc.yaml
```

#### 4. 磁盘空间不足

**症状**：应用无法写入新附件或日志，磁盘使用率达到 100%

**临时解决**：

```bash
# 手动清理旧附件（超过 30 天）
docker exec <container_id> find /data/attachments -type f -mtime +30 -delete

# 手动清理旧日志
docker exec <container_id> find /var/log/llm-gateway -name "*.gz" -delete
```

**长期方案**：

- 配置自动清理策略（见上文"数据清理策略"）
- 扩容存储卷
- 优化附件保留期限

## 附录

### 存储路径结构

#### 附件存储

```
/data/attachments/
├── 2024/
│   ├── 01/
│   │   ├── 15/
│   │   │   ├── abc123.jpg
│   │   │   └── def456.pdf
│   │   └── 16/
│   └── 02/
└── 2025/
```

存储路径规则：`{attachmentDir}/{year}/{month}/{day}/{hash}.{ext}`

#### 日志存储

```
/var/log/llm-gateway/
├── gateway.log           # 当前日志文件
├── gateway.log.1         # 轮转日志文件 1
├── gateway.log.2.gz      # 压缩的轮转日志文件 2
└── gateway.log.3.gz
```

### 相关 API 端点

- `GET /admin/storage/config` - 查看存储配置
- `PUT /admin/storage/config` - 修改存储配置
- `GET /admin/storage/retention` - 查看保留策略
- `PUT /admin/storage/retention` - 修改保留策略
- `POST /admin/storage/cleanup` - 手动触发清理
- `GET /admin/storage/stats` - 查看存储统计信息

详细 API 文档请参考项目 README 或 Swagger 文档。

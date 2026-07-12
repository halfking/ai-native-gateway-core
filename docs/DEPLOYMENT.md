# LLM Gateway Go — 部署指南

本文档描述 LLM Gateway Go 的四种部署模式（M1-M4）及其部署步骤。

---

## 目录

- [部署模式选择](#部署模式选择)
- [M1 单机部署](#m1-单机部署二进制--systemd)
- [M2 单机 Docker](#m2-单机-docker)
- [M3 K8s Sidecar](#m3-k8s-sidecar)
- [M4 K8s Operator](#m4-k8s-operator)
- [环境变量](#环境变量)
- [端口说明](#端口说明)

---

## 部署模式选择

| 模式 | 场景 | 优点 | 缺点 | 是否支持在线升级 |
|------|------|------|------|-----------------|
| **M1** | 离线/内网环境 | 无外网依赖，启动快 | 手动升级 | ❌（仅支持离线升级） |
| **M2** | 中小型单机 | 快速部署，易回滚 | 单点故障 | ✅ |
| **M3** | K8s 生产环境 | 高可用，自动扩缩容 | 复杂度高 | ✅ |
| **M4** | K8s + 声明式 | 自动化运维 | 需要 Operator | 🔨（规划中） |

---

## M1 单机部署（二进制 + systemd）

### 适用场景

- 完全断网环境
- 政务/金融内网
- 无 Docker/K8s 环境

### 部署步骤

#### 1. 下载离线安装包

```bash
# 从主控端下载（需要联网电脑）
wget https://llm.kxpms.cn/releases/v2.4.2/llm-gateway-linux-amd64.tar.gz

# 或使用 U 盘拷贝
```

#### 2. 解压并安装

```bash
tar -xzf llm-gateway-linux-amd64.tar.gz
cd llm-gateway-v2.4.2

# 安装到 /opt/llm-gateway
sudo mkdir -p /opt/llm-gateway
sudo cp gateway /opt/llm-gateway/
sudo cp llm-gw-installer /opt/llm-gateway/bin/
sudo cp -r web /opt/llm-gateway/
sudo cp -r sql /opt/llm-gateway/
```

#### 3. 离线激活

```bash
# 生成激活请求
cd /opt/llm-gateway
./bin/llm-gw-installer activate --mode offline --request-file /tmp/activation.req

# 拷贝 activation.req 到联网电脑
# 访问 https://llm.kxpms.cn/offline 上传请求文件
# 下载 license.dat 并拷回服务器

# 导入 license
./bin/llm-gw-installer activate --mode offline --import-file /tmp/license.dat
```

#### 4. 创建 systemd 服务

```bash
sudo cat > /etc/systemd/system/kx-gateway.service <<EOF
[Unit]
Description=LLM Gateway Go
After=network.target postgresql.service redis.service

[Service]
Type=simple
User=llmgw
Group=llmgw
WorkingDirectory=/opt/llm-gateway
ExecStart=/opt/llm-gateway/gateway --listen :8781
Restart=always
RestartSec=10
EnvironmentFile=/etc/llm-gateway/env

[Install]
WantedBy=multi-user.target
EOF
```

#### 5. 配置环境变量

```bash
sudo mkdir -p /etc/llm-gateway
sudo cat > /etc/llm-gateway/env <<EOF
LLM_GATEWAY_LISTEN=:8781
LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway/web
PG_HOST=localhost
PG_PORT=5432
PG_DATABASE=llm_gateway
PG_USER=llmgw
PG_PASSWORD=your_password
REDIS_ADDR=localhost:6379
LOG_LEVEL=info
EOF
```

#### 6. 启动服务

```bash
sudo systemctl daemon-reload
sudo systemctl enable kx-gateway.service
sudo systemctl start kx-gateway.service

# 检查状态
sudo systemctl status kx-gateway.service
curl http://localhost:8781/healthz
```

#### 7. 离线升级

```bash
# 下载升级包（联网电脑）
wget https://llm.kxpms.cn/releases/v2.5.0/llm-gateway-linux-amd64.tar.gz

# 拷贝到服务器并解压
tar -xzf llm-gateway-linux-amd64.tar.gz

# 执行升级
cd /opt/llm-gateway
./bin/llm-gw-installer upgrade apply --offline-package /tmp/llm-gateway-v2.5.0.tar.gz

# 升级器会自动：
# 1. 备份当前二进制到 backups/
# 2. 停止服务
# 3. 替换二进制
# 4. 启动服务
# 5. 健康检查（5s 内失败自动回退）
```

---

## M2 单机 Docker

### 适用场景

- 有外网连接
- 中小型单机部署
- 需要快速回滚

### 部署步骤

#### 1. 创建目录结构

```bash
mkdir -p ~/llm-gateway/{data,logs,backups}
cd ~/llm-gateway
```

#### 2. 创建 docker-compose.yml

```yaml
version: '3.8'

services:
  gateway:
    image: registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.4.2
    container_name: kx-gateway
    restart: unless-stopped
    ports:
      - "8781:8781"
    volumes:
      - ./data:/var/lib/kx-gateway
      - ./logs:/var/log/kx-gateway
      - ./license.dat:/var/lib/kx-gateway/license.dat:ro
    environment:
      - LLM_GATEWAY_LISTEN=:8781
      - PG_HOST=postgres
      - PG_PORT=5432
      - PG_DATABASE=llm_gateway
      - PG_USER=llmgw
      - PG_PASSWORD=${PG_PASSWORD}
      - REDIS_ADDR=redis:6379
      - LOG_LEVEL=info
    depends_on:
      - postgres
      - redis
    networks:
      - llm-gateway-net

  postgres:
    image: citusdata/citus:11.3
    container_name: kx-postgres
    restart: unless-stopped
    environment:
      - POSTGRES_DB=llm_gateway
      - POSTGRES_USER=llmgw
      - POSTGRES_PASSWORD=${PG_PASSWORD}
    volumes:
      - postgres-data:/var/lib/postgresql/data
      - ./sql:/docker-entrypoint-initdb.d:ro
    networks:
      - llm-gateway-net

  redis:
    image: redis:7-alpine
    container_name: kx-redis
    restart: unless-stopped
    command: redis-server --maxmemory 2gb --maxmemory-policy allkeys-lru
    volumes:
      - redis-data:/data
    networks:
      - llm-gateway-net

volumes:
  postgres-data:
  redis-data:

networks:
  llm-gateway-net:
    driver: bridge
```

#### 3. 创建 .env 文件

```bash
cat > .env <<EOF
PG_PASSWORD=your_secure_password
EOF
```

#### 4. 在线激活

```bash
# 安装 CLI 工具
docker run --rm -v $(pwd):/work \
  registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gw-installer:v2.4.2 \
  activate --mode trial --email your@email.com --output /work/license.dat
```

#### 5. 启动服务

```bash
docker-compose up -d

# 检查状态
docker-compose ps
curl http://localhost:8781/healthz
```

#### 6. 在线升级

```bash
# 检查更新
docker run --rm -v $(pwd):/work \
  registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gw-installer:v2.4.2 \
  upgrade check

# 自动升级
docker run --rm -v $(pwd):/work -v /var/run/docker.sock:/var/run/docker.sock \
  registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gw-installer:v2.4.2 \
  upgrade apply --to v2.5.0

# 升级器会自动：
# 1. 拉取新镜像
# 2. 备份当前容器
# 3. 停止旧容器
# 4. 启动新容器
# 5. 健康检查（5s 内失败自动回退）
```

---

## M3 K8s Sidecar

### 适用场景

- K8s 生产环境
- 需要高可用
- 需要自动扩缩容

### 部署步骤

#### 1. 创建 Namespace

```bash
kubectl create namespace llm-gateway
```

#### 2. 创建 ConfigMap

```yaml
# configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: llm-gateway-config
  namespace: llm-gateway
data:
  config.yaml: |
    listen: ":8781"
    log_level: info
    postgres:
      host: postgres-service
      port: 5432
      database: llm_gateway
      user: llmgw
    redis:
      addr: redis-service:6379
```

#### 3. 创建 Secret

```bash
kubectl create secret generic llm-gateway-secrets \
  --namespace=llm-gateway \
  --from-literal=pg-password=your_secure_password \
  --from-file=license.dat=./license.dat
```

#### 4. 创建 Deployment

```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: llm-gateway
  namespace: llm-gateway
spec:
  replicas: 3
  selector:
    matchLabels:
      app: llm-gateway
  template:
    metadata:
      labels:
        app: llm-gateway
    spec:
      containers:
      - name: gateway
        image: registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.4.2
        ports:
        - containerPort: 8781
        env:
        - name: LLM_GATEWAY_LISTEN
          value: ":8781"
        - name: PG_PASSWORD
          valueFrom:
            secretKeyRef:
              name: llm-gateway-secrets
              key: pg-password
        volumeMounts:
        - name: config
          mountPath: /etc/llm-gateway
        - name: license
          mountPath: /var/lib/kx-gateway/license.dat
          subPath: license.dat
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8781
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8781
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          requests:
            cpu: 500m
            memory: 512Mi
          limits:
            cpu: 2000m
            memory: 2Gi
      
      # Sidecar: 心跳代理（聚合 5 分钟心跳）
      - name: heartbeat-agent
        image: registry.cn-hangzhou.aliyuncs.com/kaixuan/kx-heartbeat-agent:v1.0.0
        env:
        - name: INSTANCE_TOKEN
          valueFrom:
            secretKeyRef:
              name: llm-gateway-secrets
              key: instance-token
        - name: MAIN_CONTROL_URL
          value: "https://llm.kxpms.cn"
        - name: DEPLOYMENT_ID
          value: "llm-gateway/llm-gateway"
        - name: HEARTBEAT_INTERVAL_SECS
          value: "300"
        resources:
          requests:
            cpu: 50m
            memory: 64Mi
          limits:
            cpu: 100m
            memory: 128Mi
      
      volumes:
      - name: config
        configMap:
          name: llm-gateway-config
      - name: license
        secret:
          secretName: llm-gateway-secrets
```

#### 5. 创建 Service

```yaml
# service.yaml
apiVersion: v1
kind: Service
metadata:
  name: llm-gateway-service
  namespace: llm-gateway
spec:
  selector:
    app: llm-gateway
  ports:
  - port: 8781
    targetPort: 8781
  type: LoadBalancer
```

#### 6. 部署

```bash
kubectl apply -f configmap.yaml
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml

# 检查状态
kubectl get pods -n llm-gateway
kubectl logs -f -n llm-gateway -l app=llm-gateway -c gateway
```

#### 7. 在线升级

```bash
# 方式 1: Helm 升级（推荐）
helm upgrade llm-gateway ./helm-chart \
  --namespace llm-gateway \
  --set image.tag=v2.5.0 \
  --set upgrade.autoReport=true

# 方式 2: kubectl 滚动更新
kubectl set image deployment/llm-gateway \
  gateway=registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.5.0 \
  -n llm-gateway

# 升级状态会自动上报到主控端
```

---

## M4 K8s Operator

### 状态

🔨 规划中（第二阶段）

### 预期功能

- CRD 定义 `LLMGateway` 资源
- 声明式升级管理
- 自动健康检查与回滚
- 多集群联邦管理

---

## 环境变量

### 必需变量

| 变量名 | 说明 | 示例 |
|--------|------|------|
| `LLM_GATEWAY_LISTEN` | 监听地址 | `:8781` |
| `PG_HOST` | PostgreSQL 主机 | `localhost` |
| `PG_PORT` | PostgreSQL 端口 | `5432` |
| `PG_DATABASE` | 数据库名 | `llm_gateway` |
| `PG_USER` | 数据库用户 | `llmgw` |
| `PG_PASSWORD` | 数据库密码 | `***` |
| `REDIS_ADDR` | Redis 地址 | `localhost:6379` |

### 可选变量

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `LLM_GATEWAY_STATIC_DIR` | 前端静态文件目录 | `web/dist` |
| `LOG_LEVEL` | 日志级别 | `info` |
| `INSTANCE_ID` | 实例 ID（自动生成） | UUID |
| `LICENSE_FILE` | License 文件路径 | `/var/lib/kx-gateway/license.dat` |
| `MAIN_CONTROL_URL` | 主控端地址 | `https://llm.kxpms.cn` |
| `HEARTBEAT_INTERVAL_SECS` | 心跳间隔（秒） | `60` |
| `UPGRADE_CHECK_INTERVAL_SECS` | 升级检查间隔（秒） | `21600` (6h) |
| `MAX_TENANTS` | 最大租户数（License 控制） | `1` (试用) |
| `MAX_DEVICES` | 最大设备数（License 控制） | `1` (试用) |
| `ENABLE_TELEMETRY` | 启用遥测 | `true` |
| `ENABLE_AUTO_UPDATE` | 启用自动更新 | `false` |
| `BACKUP_DIR` | 备份目录 | `/opt/llm-gateway/backups` |
| `PG_POOL_MAX_CONNS` | 数据库连接池大小 | `20` |
| `REDIS_POOL_SIZE` | Redis 连接池大小 | `10` |
| `REQUEST_TIMEOUT_SECS` | 请求超时（秒） | `120` |
| `MAX_BODY_SIZE_MB` | 最大请求体大小（MB） | `128` |

---

## 端口说明

| 端口 | 服务 | 协议 | 说明 |
|------|------|------|------|
| `8781` | LLM Gateway | HTTP/HTTPS | 主网关端口 |
| `8443` | License Authority | HTTPS | 主控端（仅主控端监听） |
| `5432` | PostgreSQL | TCP | 数据库 |
| `6379` | Redis | TCP | 缓存 |
| `9090` | Prometheus | HTTP | 监控指标（可选） |

---

## 常见问题

### 1. 首页返回 JSON 而非 HTML

**现象**:
```json
{"service":"llm-gateway-go","version":"v2.4.2"}
```

**原因**: 未设置 `LLM_GATEWAY_STATIC_DIR` 环境变量

**解决**:
```bash
export LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway/web
systemctl restart kx-gateway.service
```

### 2. License 验证失败

**现象**: 启动时报 `License verification failed`

**原因**: 
- license.dat 文件不存在
- license.dat 签名无效
- License 已过期

**解决**:
```bash
# 重新激活
./bin/llm-gw-installer activate --mode trial --email your@email.com

# 或导入现有 License
./bin/llm-gw-installer activate --mode offline --import-file license.dat
```

### 3. 数据库连接失败

**现象**: `pq: database "llm_gateway" does not exist`

**解决**:
```bash
# 创建数据库
createdb llm_gateway

# 运行迁移
cd /opt/llm-gateway
./gateway --migrate-only
```

### 4. 升级失败自动回退

**现象**: 升级到新版本后 5s 内健康检查失败，自动回退到旧版本

**原因**: 新版本二进制与环境不兼容

**解决**:
```bash
# 查看升级日志
cat /opt/llm-gateway/reports/upgrade-log.jsonl

# 手动回滚
./bin/llm-gw-installer upgrade rollback --to v2.4.2
```

---

## 安全建议

1. **生产环境使用 HTTPS**:
   - 配置 TLS 证书（Let's Encrypt / 自签名）
   - 使用 Nginx / Traefik 反向代理

2. **定期备份**:
   - 数据库每日全量备份
   - License 文件备份到异地

3. **监控告警**:
   - 配置 Prometheus + Grafana 监控
   - 设置心跳离线告警

4. **访问控制**:
   - 使用防火墙限制 8781 端口访问
   - 启用 IP 白名单

---

## 相关文档

- [API 文档](API.md) — 主控端 8 个 API 端点
- [升级指南](UPGRADE.md) — 在线/离线升级流程
- [架构文档](architecture/ARCHITECTURE.md) — 系统架构设计

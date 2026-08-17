# LLM Gateway Go — 升级指南

本文档描述 LLM Gateway Go 的在线/离线升级流程、回滚步骤及常见问题。

---

## 目录

- [升级流程图](#升级流程图)
- [在线升级](#在线升级)
- [离线升级](#离线升级)
- [回滚操作](#回滚操作)
- [常见问题](#常见问题)

---

## 升级流程图

```
┌─────────────────────────────────────────────────────────────┐
│                       升级流程                               │
└─────────────────────────────────────────────────────────────┘

   检查更新
      │
      ├──> 主控端: GET /api/v1/updates/latest
      │            返回: version, manifest_url, sha256
      ↓
   下载升级包
      │
      ├──> 主控端: GET /api/v1/updates/manifest
      │            返回: 二进制 + 镜像 + SQL 文件列表
      ↓
   SHA256 校验
      │
      ├──> 本地: sha256sum -c
      │         失败 → 终止 + 上报 failed
      ↓
   启动测试
      │
      ├──> 本地: ./gateway-new --version
      │         失败 → 终止 + 上报 failed
      ↓
   备份当前版本
      │
      ├──> 本地: cp gateway backups/manual/v2.4.2_20260712
      ↓
   停止服务
      │
      ├──> systemd: systemctl stop kx-gateway.service
      │    Docker:  docker-compose stop gateway
      │    K8s:     kubectl scale --replicas=0
      ↓
   原子替换
      │
      ├──> 本地: mv gateway-new gateway (原子操作)
      │         echo "v2.5.0" > VERSION
      ↓
   启动服务
      │
      ├──> systemd: systemctl start kx-gateway.service
      │    Docker:  docker-compose up -d gateway
      │    K8s:     kubectl scale --replicas=3
      ↓
   健康检查 (5s)
      │
      ├──> curl http://localhost:8781/healthz
      │    ├─ 200 OK → 升级成功 → 上报 success
      │    └─ 非 200 → 自动回退 → 上报 rolled_back
      ↓
   完成
```

---

## 在线升级

### 适用场景

- M2 单机 Docker
- M3 K8s Sidecar
- 有外网连接的 M1 单机

### 步骤 1: 检查更新

```bash
# 方式 1: 使用安装器（推荐）
cd /opt/llm-gateway
./bin/llm-gw-installer upgrade check

# 输出示例:
# ✓ Current version: v2.4.2 (build 975)
# ✓ Latest version:  v2.5.0 (build 1020)
# ✓ Release date:    2026-07-15
# ✓ Size:            128 MB
# ✓ Mandatory:       No
# ✓ Release notes:   https://llm.kxpms.cn/release-notes/v2.5.0
```

```bash
# 方式 2: 手动调用 API
curl -H "Authorization: Bearer $INSTANCE_TOKEN" \
     -H "X-Instance-ID: $INSTANCE_ID" \
     "https://llm.kxpms.cn/api/v1/updates/latest?current_version=v2.4.2&channel=stable"
```

### 步骤 2: 应用升级

```bash
# 自动升级（推荐）
./bin/llm-gw-installer upgrade apply --to v2.5.0

# 升级器会自动执行：
# 1. 下载升级包到 /tmp/upgrade-v2.5.0/
# 2. SHA256 校验
# 3. 启动测试（./gateway --version）
# 4. 备份当前二进制到 backups/manual/v2.4.2_20260712170000
# 5. 停止服务
# 6. 原子替换二进制
# 7. 更新 VERSION 文件
# 8. 启动服务
# 9. 健康检查（5s 内 curl /healthz）
# 10. 上报升级结果到主控端

# 输出示例:
# ✓ Downloading v2.5.0 from https://llm.kxpms.cn/releases/v2.5.0/gateway-linux-amd64
# ✓ SHA256 checksum: PASS
# ✓ Version test: v2.5.0 (build 1020)
# ✓ Backup created: backups/manual/v2.4.2_20260712170000
# ✓ Service stopped
# ✓ Binary replaced: gateway
# ✓ VERSION updated: v2.5.0
# ✓ Service started
# ✓ Health check: PASS (200 OK in 1.2s)
# ✓ Upgrade report sent to main control
# ✓ Upgrade completed successfully
```

### 步骤 3: 验证升级

```bash
# 检查版本
curl http://localhost:8781/api/system/version
# 返回: {"version":"v2.5.0","build_seq":1020}

# 检查服务状态
systemctl status kx-gateway.service

# 检查日志
journalctl -u kx-gateway.service -n 100 --no-pager
```

---

## 离线升级

### 适用场景

- M1 单机部署（完全断网）
- 政务/金融内网环境

### 步骤 1: 下载升级包（联网电脑）

```bash
# 访问主控端
wget https://llm.kxpms.cn/releases/v2.5.0/llm-gateway-linux-amd64.tar.gz

# 校验文件完整性
wget https://llm.kxpms.cn/releases/v2.5.0/llm-gateway-linux-amd64.tar.gz.sha256
sha256sum -c llm-gateway-linux-amd64.tar.gz.sha256
```

### 步骤 2: 拷贝到目标服务器

```bash
# 使用 U 盘或内网传输
scp llm-gateway-linux-amd64.tar.gz user@10.0.0.5:/tmp/
```

### 步骤 3: 解压升级包

```bash
cd /tmp
tar -xzf llm-gateway-linux-amd64.tar.gz
cd llm-gateway-v2.5.0

# 目录结构:
# llm-gateway-v2.5.0/
#   ├── gateway                  (新二进制)
#   ├── llm-gw-installer         (新安装器)
#   ├── docker-images.tar.gz     (Docker 镜像，仅 M2)
#   ├── sql/migrations/          (SQL 迁移文件)
#   ├── web/                     (前端静态文件)
#   └── upgrade.sh               (升级脚本)
```

### 步骤 4: 执行离线升级

```bash
# 方式 1: 使用安装器
cd /opt/llm-gateway
./bin/llm-gw-installer upgrade apply --offline-package /tmp/llm-gateway-v2.5.0.tar.gz

# 方式 2: 使用升级包自带脚本
cd /tmp/llm-gateway-v2.5.0
sudo ./upgrade.sh --target-dir /opt/llm-gateway
```

### 步骤 5: 验证升级

```bash
# 检查版本
/opt/llm-gateway/gateway --version
# 输出: v2.5.0 (build 1020)

# 检查服务
systemctl status kx-gateway.service
curl http://localhost:8781/healthz
```

---

## 回滚操作

### 自动回滚

升级过程中，如果健康检查失败（5s 内 `/healthz` 返回非 200），安装器会**自动回滚**：

```bash
# 自动回滚流程:
# 1. 停止服务
# 2. 从 backups/manual/ 恢复旧二进制
# 3. 恢复 VERSION 文件
# 4. 启动服务
# 5. 健康检查
# 6. 上报 rolled_back 到主控端
```

### 手动回滚

#### 步骤 1: 查看可用备份

```bash
ls -lh /opt/llm-gateway/backups/manual/
# 输出:
# drwxr-xr-x 2 root root 4.0K Jul 12 17:00 v2.4.2_20260712170000
# drwxr-xr-x 2 root root 4.0K Jul 10 15:30 v2.4.1_20260710153000
```

#### 步骤 2: 执行回滚

```bash
# 方式 1: 使用安装器（推荐）
cd /opt/llm-gateway
./bin/llm-gw-installer upgrade rollback --to v2.4.2

# 输出:
# ✓ Found backup: backups/manual/v2.4.2_20260712170000
# ✓ Service stopped
# ✓ Binary restored: gateway
# ✓ VERSION updated: v2.4.2
# ✓ Service started
# ✓ Health check: PASS (200 OK)
# ✓ Rollback completed successfully
```

```bash
# 方式 2: 手动回滚
cd /opt/llm-gateway
systemctl stop kx-gateway.service
cp backups/manual/v2.4.2_20260712170000/gateway ./gateway
echo "v2.4.2" > VERSION
systemctl start kx-gateway.service
curl http://localhost:8781/healthz
```

#### 步骤 3: 验证回滚

```bash
# 检查版本
curl http://localhost:8781/api/system/version
# 返回: {"version":"v2.4.2","build_seq":975}

# 检查日志
journalctl -u kx-gateway.service -n 50 --no-pager
```

---

## 常见问题

### 1. 健康检查失败导致自动回退

**现象**:
```
✗ Health check: FAIL (connection refused)
✓ Auto rollback: backups/manual/v2.4.2_20260712170000
✓ Service restarted
✓ Rollback completed successfully
```

**原因**:
- 新版本二进制与系统环境不兼容
- 数据库迁移失败
- 依赖服务（PostgreSQL/Redis）不可用

**解决**:
```bash
# 查看升级日志
cat /opt/llm-gateway/reports/upgrade-log.jsonl

# 查看服务日志
journalctl -u kx-gateway.service -n 100 --no-pager

# 手动测试新版本
cd /tmp/llm-gateway-v2.5.0
./gateway --config /etc/llm-gateway/config.yaml --dry-run
```

---

### 2. 备份空间不足

**现象**:
```
✗ Backup failed: no space left on device
```

**原因**: `/opt/llm-gateway/backups/` 分区空间不足

**解决**:
```bash
# 检查磁盘空间
df -h /opt/llm-gateway

# 清理旧备份（保留最近 5 个）
cd /opt/llm-gateway/backups/manual
ls -t | tail -n +6 | xargs rm -rf

# 或配置自动清理
./bin/llm-gw-installer config set backup.retention_count 5
```

---

### 3. 下载超时

**现象**:
```
✗ Download failed: timeout after 300s
```

**原因**: 网络慢或升级包过大

**解决**:
```bash
# 增加超时时间
./bin/llm-gw-installer upgrade apply --to v2.5.0 --timeout 1800

# 或手动下载后离线升级
wget https://llm.kxpms.cn/releases/v2.5.0/gateway-linux-amd64 -O /tmp/gateway-new
./bin/llm-gw-installer upgrade apply --offline-binary /tmp/gateway-new
```

---

### 4. SHA256 校验失败

**现象**:
```
✗ SHA256 checksum mismatch
  Expected: abc123...
  Got:      def456...
```

**原因**: 下载文件损坏或被篡改

**解决**:
```bash
# 重新下载
rm /tmp/gateway-new
./bin/llm-gw-installer upgrade apply --to v2.5.0 --force-download

# 如果持续失败，联系管理员检查主控端文件
```

---

### 5. Docker 升级镜像拉取失败

**现象**:
```
✗ Failed to pull image: registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.5.0
```

**原因**: Docker Hub / 阿里云镜像仓库不可达

**解决**:
```bash
# 方式 1: 手动拉取镜像
docker pull registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.5.0

# 方式 2: 离线升级（导入镜像 tar）
wget https://llm.kxpms.cn/releases/v2.5.0/docker-images.tar.gz
tar -xzf docker-images.tar.gz
docker load -i llm-gateway-go-v2.5.0.tar
docker-compose up -d
```

---

### 6. K8s 滚动更新卡住

**现象**:
```bash
kubectl get pods -n llm-gateway
# NAME                           READY   STATUS             RESTARTS   AGE
# llm-gateway-7b8c9d5f6b-abc12   0/1     ImagePullBackOff   0          5m
```

**原因**: 新版本镜像不存在或拉取失败

**解决**:
```bash
# 检查镜像
kubectl describe pod llm-gateway-7b8c9d5f6b-abc12 -n llm-gateway

# 回滚 Deployment
kubectl rollout undo deployment/llm-gateway -n llm-gateway

# 或手动设置回旧版本
kubectl set image deployment/llm-gateway \
  gateway=registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.4.2 \
  -n llm-gateway
```

---

## 升级策略建议

### 1. 灰度升级（K8s）

```bash
# 先升级 1 个副本
kubectl scale deployment/llm-gateway --replicas=3 -n llm-gateway
kubectl set image deployment/llm-gateway \
  gateway=registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.5.0 \
  -n llm-gateway

# 观察 10 分钟
kubectl logs -f -n llm-gateway -l app=llm-gateway --tail=100

# 确认无异常后，全量升级
kubectl scale deployment/llm-gateway --replicas=10 -n llm-gateway
```

### 2. 蓝绿部署（Docker）

```bash
# 启动新版本容器（不停止旧容器）
docker run -d --name kx-gateway-new \
  -p 8782:8781 \
  -v /opt/llm-gateway/license.dat:/var/lib/kx-gateway/license.dat:ro \
  registry.cn-hangzhou.aliyuncs.com/kaixuan/llm-gateway-go:v2.5.0

# 验证新容器
curl http://localhost:8782/healthz

# 切换流量（更新 Nginx upstream）
# upstream llm-gateway {
#   server 127.0.0.1:8782;  # 新版本
# }
nginx -s reload

# 观察 30 分钟无异常后，停止旧容器
docker stop kx-gateway
docker rm kx-gateway
docker rename kx-gateway-new kx-gateway
```

### 3. 定期检查更新

```bash
# 配置 cron 每日检查更新
cat > /etc/cron.daily/llm-gateway-update-check <<'EOF'
#!/bin/bash
cd /opt/llm-gateway
./bin/llm-gw-installer upgrade check | tee -a /var/log/llm-gateway-update-check.log
EOF
chmod +x /etc/cron.daily/llm-gateway-update-check
```

---

## 相关文档

- [API 文档](API.md) — 升级相关 API 端点
- [部署指南](DEPLOYMENT.md) — M1-M4 四种部署模式
- [架构文档](architecture/ARCHITECTURE.md) — 系统架构设计

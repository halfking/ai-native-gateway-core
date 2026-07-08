# LLM Gateway Go 部署指南

## 部署架构

### 154 生产环境 (llm.kxpms.cn)
- **部署方式**: 直接主机部署（非容器化）
- **部署路径**: `/opt/llm-gateway-go/`
- **服务管理**: systemd (`llm-gateway-go.service`)
- **配置文件**: `/etc/llm-gateway-go/env`
- **访问地址**: https://llm.kxpms.cn

## 部署经验总结

### 关键经验

1. **静态文件路径配置**
   - 问题：默认静态文件路径 `web/dist` 在直接部署时不匹配
   - 解决：必须在 `/etc/llm-gateway-go/env` 中设置 `LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web`
   - 现象：未设置时首页返回JSON响应而非Vue SPA

2. **交叉编译**
   - 本地环境：macOS ARM64
   - 目标环境：Linux AMD64
   - 命令：`GOOS=linux GOARCH=amd64 go build -o llm-gateway-go ./cmd/gateway`

3. **前端构建**
   - 工作目录：`web/`
   - 命令：`npm run build`
   - 输出目录：`web/dist/` → 部署后成为 `/opt/llm-gateway-go/web/`

4. **服务重启**
   - 不能使用 `systemctl restart`，会导致SSH连接等待
   - 解决：使用 `systemctl stop && systemctl start`

5. **备份策略**
   - 每次部署前自动备份二进制和前端文件
   - 备份路径：`/opt/llm-gateway-go/backups/backup-YYYYMMDD-HHMMSS/`
   - 保留策略：保留最近5个备份

### 常见问题

#### 1. 首页返回JSON而非HTML
**现象**:
```json
{"service":"llm-gateway-go","version":"dev","git_sha":"unknown","build_seq":"0"}
```

**原因**: 未设置 `LLM_GATEWAY_STATIC_DIR` 环境变量

**解决**:
```bash
echo 'LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web' >> /etc/llm-gateway-go/env
systemctl stop llm-gateway-go.service
systemctl start llm-gateway-go.service
```

#### 2. 数据库表缺失
**现象**: API返回 `relation 'xxx' does not exist`

**解决**: 
- 短期：代码中实现降级查询逻辑
- 长期：执行缺失的数据库迁移脚本

#### 3. 前端缓存问题
**现象**: 部署后页面未更新

**解决**: 用户端强制刷新 (Cmd+Shift+R / Ctrl+Shift+R)

## 部署流程

### 手动部署流程

1. **拉取最新代码**
   ```bash
   cd /path/to/llm-gateway-go
   git pull origin main
   ```

2. **编译后端**
   ```bash
   GOOS=linux GOARCH=amd64 go build -o llm-gateway-go ./cmd/gateway
   ```

3. **构建前端**
   ```bash
   cd web
   npm install
   npm run build
   cd ..
   ```

4. **上传文件**
   ```bash
   # 备份现有文件
   ssh root@47.97.111.154 -p 25022 "cd /opt/llm-gateway-go && \
     mkdir -p backups/backup-$(date +%Y%m%d-%H%M%S) && \
     cp llm-gateway-go backups/backup-$(date +%Y%m%d-%H%M%S)/ && \
     cp -r web backups/backup-$(date +%Y%m%d-%H%M%S)/"
   
   # 上传后端
   scp -P 25022 llm-gateway-go root@47.97.111.154:/opt/llm-gateway-go/
   
   # 上传前端
   rsync -avz -e "ssh -p 25022" web/dist/ root@47.97.111.154:/opt/llm-gateway-go/web/
   ```

5. **重启服务**
   ```bash
   ssh root@47.97.111.154 -p 25022 "systemctl stop llm-gateway-go.service && systemctl start llm-gateway-go.service"
   ```

6. **验证部署**
   ```bash
   # 检查服务状态
   ssh root@47.97.111.154 -p 25022 "systemctl status llm-gateway-go.service"
   
   # 检查首页
   curl -I https://llm.kxpms.cn
   
   # 检查健康状态
   curl https://llm.kxpms.cn/healthz
   ```

### 自动化部署

使用提供的自动化脚本：

```bash
./scripts/deploy-to-154-auto.sh
```

脚本功能：
- ✅ 自动拉取最新代码
- ✅ 交叉编译后端
- ✅ 构建前端资源
- ✅ 自动备份现有部署
- ✅ 上传并部署
- ✅ 重启服务
- ✅ 验证部署结果
- ✅ 失败时自动回滚

## 环境配置

### /etc/llm-gateway-go/env

必需的环境变量配置：

```bash
# 静态文件路径（必需！）
LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web

# 数据库连接
LLM_GATEWAY_DB_URL=postgres://username:password@host:port/database

# API密钥
LLM_GATEWAY_API_KEY=your-api-key-here
LLM_GATEWAY_ADMIN_API_KEY=your-admin-key-here

# 监听地址
LLM_GATEWAY_LISTEN=:8781

# 其他配置...
```

### systemd 服务配置

`/etc/systemd/system/llm-gateway-go.service`:

```ini
[Unit]
Description=LLM Gateway Go
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/llm-gateway-go
ExecStart=/opt/llm-gateway-go/llm-gateway-go
EnvironmentFile=/etc/llm-gateway-go/env
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

## 回滚流程

### 自动回滚（脚本内置）

部署脚本会在验证失败时自动回滚到上一个备份。

### 手动回滚

```bash
# 1. 列出可用备份
ssh root@47.97.111.154 -p 25022 "ls -la /opt/llm-gateway-go/backups/"

# 2. 选择备份并回滚
BACKUP_DIR="backup-20260708-205000"
ssh root@47.97.111.154 -p 25022 "
  cd /opt/llm-gateway-go &&
  cp backups/$BACKUP_DIR/llm-gateway-go . &&
  rm -rf web &&
  cp -r backups/$BACKUP_DIR/web . &&
  systemctl stop llm-gateway-go.service &&
  systemctl start llm-gateway-go.service
"

# 3. 验证回滚
curl https://llm.kxpms.cn/healthz
```

## 监控和日志

### 查看服务状态
```bash
ssh root@47.97.111.154 -p 25022 "systemctl status llm-gateway-go.service"
```

### 查看实时日志
```bash
ssh root@47.97.111.154 -p 25022 "journalctl -u llm-gateway-go.service -f"
```

### 查看最近日志
```bash
ssh root@47.97.111.154 -p 25022 "journalctl -u llm-gateway-go.service --since '10 minutes ago'"
```

### 搜索错误
```bash
ssh root@47.97.111.154 -p 25022 "journalctl -u llm-gateway-go.service | grep -i error"
```

## 故障排查清单

1. **服务无法启动**
   - 检查配置文件：`cat /etc/llm-gateway-go/env`
   - 查看启动日志：`journalctl -u llm-gateway-go.service -n 50`
   - 检查端口占用：`netstat -tulpn | grep 8781`

2. **首页无法访问**
   - 验证静态文件路径：`ls -la /opt/llm-gateway-go/web/`
   - 检查环境变量：`grep STATIC_DIR /etc/llm-gateway-go/env`
   - 查看日志：`journalctl -u llm-gateway-go.service | grep "serving Vue SPA"`

3. **API错误**
   - 检查数据库连接：查看日志中的数据库错误
   - 验证API端点：`curl https://llm.kxpms.cn/healthz`
   - 检查认证：确认API密钥配置正确

## 最佳实践

1. **部署前**
   - ✅ 在本地测试所有更改
   - ✅ 运行单元测试：`go test ./...`
   - ✅ 构建并本地运行前端：`cd web && npm run build && npm run preview`

2. **部署中**
   - ✅ 使用自动化脚本减少人为错误
   - ✅ 确保备份成功创建
   - ✅ 监控服务启动日志

3. **部署后**
   - ✅ 验证健康检查端点
   - ✅ 测试关键功能（登录、仪表盘等）
   - ✅ 检查浏览器控制台是否有错误
   - ✅ 监控服务日志5-10分钟

4. **变更管理**
   - ✅ 记录每次部署的commit SHA
   - ✅ 保留详细的部署日志
   - ✅ 维护回滚计划

## 版本记录

| 日期 | 版本/Commit | 部署者 | 说明 |
|------|------------|--------|------|
| 2026-07-08 | 1b5b1292 | ZCode | 修复session_dim降级查询 + 静态文件路径 |
| ... | ... | ... | ... |

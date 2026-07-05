# LLM Gateway 部署检查清单

## 部署前检查（Pre-Deployment）

### 1. 代码准备
- [ ] **代码审查**：所有变更已通过 code review
- [ ] **单元测试**：所有测试通过（`go test ./...`）
- [ ] **集成测试**：端到端测试通过
- [ ] **版本标签**：代码已打 tag（格式：`v2.3.x`）

### 2. 数据库迁移检查
- [ ] **迁移文件检查**：
  ```bash
  # 列出所有迁移文件
  ls -1 migrations/*.sql | wc -l
  
  # 检查最新迁移版本
  LATEST_MIGRATION=$(ls -1 migrations/*.sql | tail -1 | grep -oP '\d+')
  echo "最新迁移版本: $LATEST_MIGRATION"
  ```

- [ ] **当前数据库版本**：
  ```sql
  SELECT version, description, applied_at 
  FROM schema_migrations 
  ORDER BY version DESC 
  LIMIT 1;
  ```

- [ ] **待应用的迁移**：
  ```bash
  # 对比代码版本和数据库版本
  CODE_VERSION=$(ls -1 migrations/*.sql | tail -1 | grep -oP '\d+')
  DB_VERSION=$(PGPASSWORD='xxx' psql -h xxx -U llm_gateway -d llm_gateway -t -c \
    "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1" | tr -d ' ')
  
  if [ "$CODE_VERSION" != "$DB_VERSION" ]; then
    echo "⚠️  警告：需要应用数据库迁移"
    echo "   代码版本: $CODE_VERSION"
    echo "   数据库版本: $DB_VERSION"
    echo "   待应用迁移: $(($CODE_VERSION - $DB_VERSION)) 个"
    exit 1
  else
    echo "✅ 数据库迁移已是最新"
  fi
  ```

- [ ] **迁移测试**：在测试环境验证迁移脚本

### 3. 配置文件检查
- [ ] **环境变量**：检查 `.env` 文件配置
  ```bash
  # 必需的环境变量
  required_vars=(
    "LLM_GATEWAY_DATABASE_URL"
    "LLM_GATEWAY_SECRET_KEY"
    "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"
  )
  
  for var in "${required_vars[@]}"; do
    if [ -z "${!var}" ]; then
      echo "❌ 缺少环境变量: $var"
      exit 1
    fi
  done
  ```

- [ ] **日志级别**：生产环境设置为 `info` 或 `warn`
  ```bash
  grep "LOG_LEVEL" .env
  # 应该是: LOG_LEVEL=info
  ```

- [ ] **镜像配置**：确认镜像地址和版本
  ```bash
  # 检查 docker-compose.yml 或 k8s deployment
  grep "image:" docker-compose.yml
  ```

### 4. 依赖检查
- [ ] **数据库可用性**：
  ```bash
  PGPASSWORD='xxx' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway -c "SELECT 1;"
  ```

- [ ] **Redis 可用性**：
  ```bash
  redis-cli -h localhost ping
  # 应该返回: PONG
  ```

- [ ] **网络连通性**：测试上游 API 可达性
  ```bash
  curl -I https://api.openai.com
  curl -I https://api.anthropic.com
  ```

---

## 部署执行（Deployment）

### 1. 备份
- [ ] **数据库备份**：
  ```bash
  pg_dump -h __PRIV_IP_2__ -U llm_gateway llm_gateway > backup_$(date +%Y%m%d_%H%M%S).sql
  ```

- [ ] **配置备份**：
  ```bash
  cp .env .env.backup.$(date +%Y%m%d_%H%M%S)
  ```

### 2. 应用数据库迁移
- [ ] **执行迁移**（如有待应用的）：
  ```bash
  # 使用迁移工具
  ./bin/storage-migrate up
  
  # 或手动应用
  PGPASSWORD='xxx' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway < migrations/327_credential_plan_type_full.sql
  ```

- [ ] **验证迁移结果**：
  ```sql
  -- 检查新列是否存在
  SELECT column_name 
  FROM information_schema.columns 
  WHERE table_name = 'credentials' AND column_name = 'plan_type';
  
  SELECT column_name 
  FROM information_schema.columns 
  WHERE table_name = 'credential_model_bindings' AND column_name = 'plan_type_origin';
  ```

### 3. 部署新版本
- [ ] **拉取新镜像**：
  ```bash
  docker pull __DOMAIN_4__/llm-gateway-go:v2.3.x
  ```

- [ ] **停止旧容器**（滚动更新）：
  ```bash
  docker stop llm-gateway-go
  ```

- [ ] **启动新容器**：
  ```bash
  docker-compose up -d llm-gateway-go
  
  # 或 k8s
  kubectl rollout restart deployment/llm-gateway-go
  ```

- [ ] **等待启动完成**（30-60秒）：
  ```bash
  sleep 60
  ```

---

## 部署后验证（Post-Deployment）

### 1. 服务健康检查
- [ ] **容器状态**：
  ```bash
  docker ps | grep llm-gateway-go
  # 应该显示 Up 状态
  ```

- [ ] **健康端点**：
  ```bash
  curl http://localhost:__PORT_3__/healthz
  # 应该返回: {"status":"ok","version":"..."}
  ```

- [ ] **日志检查**：
  ```bash
  docker logs llm-gateway-go --tail 50
  # 检查是否有 ERROR 或 panic
  ```

### 2. 功能验证
- [ ] **模型发现状态**：
  ```bash
  docker logs llm-gateway-go | grep "model discovery completed" | tail -1
  # 应该显示: "models": >0
  ```

- [ ] **可用模型数量**：
  ```sql
  SELECT COUNT(*) as available_models 
  FROM credential_model_bindings 
  WHERE available = true;
  -- 应该 > 100
  ```

- [ ] **计费模式一致性**：
  ```sql
  SELECT COUNT(*) as mismatches
  FROM credentials c
  JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
  WHERE (c.plan_type = 'token' AND cmb.billing_mode != 'per_token');
  -- 应该 = 0
  ```

### 3. 端到端测试
- [ ] **简单请求测试**：
  ```bash
  curl -X POST http://localhost:__PORT_3__/v1/chat/completions \
    -H "Authorization: Bearer __API_KEY_1__" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "glm-4-flash",
      "messages": [{"role": "user", "content": "Hello"}],
      "max_tokens": 10
    }'
  # 应该返回 200 和有效响应
  ```

- [ ] **流式响应测试**：
  ```bash
  curl -X POST http://localhost:__PORT_3__/v1/chat/completions \
    -H "Authorization: Bearer <key>" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "glm-4-flash",
      "messages": [{"role": "user", "content": "Hello"}],
      "max_tokens": 10,
      "stream": true
    }'
  # 应该返回 SSE 格式数据
  ```

- [ ] **错误处理测试**：
  ```bash
  curl -X POST http://localhost:__PORT_3__/v1/chat/completions \
    -H "Authorization: Bearer invalid-key" \
    -H "Content-Type: application/json" \
    -d '{"model": "xxx", "messages": []}'
  # 应该返回合适的错误信息
  ```

### 4. 性能验证
- [ ] **响应时间**：
  ```bash
  time curl -s -X POST http://localhost:__PORT_3__/v1/chat/completions \
    -H "Authorization: Bearer <key>" \
    -H "Content-Type: application/json" \
    -d '{"model": "glm-4-flash", "messages": [{"role": "user", "content": "Hi"}], "max_tokens": 5}'
  # 应该 < 2s（对于快速模型）
  ```

- [ ] **并发测试**（可选）：
  ```bash
  # 使用 ab 或 hey 进行压力测试
  ab -n 100 -c 10 -p request.json -T 'application/json' http://localhost:__PORT_3__/v1/chat/completions
  ```

### 5. 监控指标检查
- [ ] **Prometheus 指标**：
  ```bash
  curl http://localhost:__PORT_3__/metrics | grep llm_gateway
  ```

- [ ] **最近请求统计**：
  ```sql
  SELECT 
    status,
    COUNT(*) as count
  FROM request_logs
  WHERE created_at > NOW() - INTERVAL '5 minutes'
  GROUP BY status;
  ```

---

## 回滚计划（Rollback）

如果部署后发现问题，执行回滚：

### 快速回滚步骤
1. **停止新版本**：
   ```bash
   docker stop llm-gateway-go
   ```

2. **启动旧版本**：
   ```bash
   docker run -d --name llm-gateway-go \
     --env-file .env.backup \
     __DOMAIN_4__/llm-gateway-go:v2.3.x-previous
   ```

3. **回滚数据库**（如果应用了迁移）：
   ```bash
   # 使用 down 迁移
   PGPASSWORD='xxx' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway < migrations/327_xxx.down.sql
   
   # 或恢复备份
   pg_restore -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway backup_xxx.sql
   ```

4. **验证回滚**：
   ```bash
   curl http://localhost:__PORT_3__/healthz
   docker logs llm-gateway-go --tail 50
   ```

---

## 通知

### 部署前通知
```
标题: [LLM Gateway] 计划部署 v2.3.x
内容:
- 部署时间: YYYY-MM-DD HH:MM
- 预计停机时间: 0-2 分钟（滚动更新）
- 变更内容: [简要描述]
- 数据库迁移: 是/否
- 风险评估: 低/中/高
- 回滚计划: 已准备
```

### 部署后通知
```
标题: [LLM Gateway] 部署完成 v2.3.x
内容:
- 部署状态: ✅ 成功
- 部署时间: YYYY-MM-DD HH:MM
- 实际停机时间: X 分钟
- 健康检查: ✅ 通过
- 端到端测试: ✅ 通过
- 监控链接: [Grafana Dashboard]
```

---

## 部署后监控（24小时）

### 第1小时：密切监控
- [ ] 每 5 分钟检查日志
- [ ] 每 10 分钟检查成功率
- [ ] 监控告警通道

### 第2-8小时：常规监控
- [ ] 每小时检查一次关键指标
- [ ] 响应所有告警

### 8-24小时：观察期
- [ ] 每 4 小时检查一次
- [ ] 确认无异常后关闭部署任务

---

## 附录：自动化脚本

### 一键部署脚本
```bash
#!/bin/bash
# deploy.sh - LLM Gateway 自动化部署脚本

set -e

# 配置
VERSION=$1
ENV=${2:-production}

if [ -z "$VERSION" ]; then
    echo "用法: ./deploy.sh <version> [environment]"
    exit 1
fi

echo "=========================================="
echo "LLM Gateway 部署脚本"
echo "=========================================="
echo "版本: $VERSION"
echo "环境: $ENV"
echo ""

# 1. 部署前检查
echo "[1/6] 部署前检查..."
./scripts/pre-deploy-check.sh || exit 1

# 2. 备份
echo "[2/6] 备份数据和配置..."
./scripts/backup.sh

# 3. 数据库迁移
echo "[3/6] 检查数据库迁移..."
./scripts/migrate.sh

# 4. 部署新版本
echo "[4/6] 部署新版本 $VERSION..."
docker-compose pull
docker-compose up -d

# 5. 等待启动
echo "[5/6] 等待服务启动..."
sleep 60

# 6. 部署后验证
echo "[6/6] 部署后验证..."
./scripts/post-deploy-verify.sh || {
    echo "❌ 验证失败，开始回滚..."
    ./scripts/rollback.sh
    exit 1
}

echo "✅ 部署成功！"
echo ""
echo "后续步骤:"
echo "1. 监控日志: docker logs llm-gateway-go -f"
echo "2. 检查指标: curl http://localhost:__PORT_3__/metrics"
echo "3. 查看仪表盘: https://grafana/d/llm-gateway"
```

---

**版本：** 1.0  
**创建日期：** 2026-07-03  
**维护者：** AI 运维团队  
**审核状态：** 待审核

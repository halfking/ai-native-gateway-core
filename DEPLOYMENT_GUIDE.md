# 🚀 P0 HOTFIX 部署指南

**修复内容:** 恢复 request_logs 写入功能  
**提交:** `fdb9a9bd` - fix(telemetry): revert ON CONFLICT to (request_id, ts)  
**状态:** ✅ 代码已推送到 origin/server-71

---

## ⚡ 快速部署（推荐）

### 选项 1: 使用自动化脚本

```bash
# 1. 设置环境变量（必需）
export LLM_GATEWAY_DATABASE_URL="postgres://user:pass@host:5432/dbname"
export LLM_GATEWAY_REDIS_URL="redis://host:6379"

# 2. 运行部署脚本
./deploy_p0_hotfix.sh

# 3. 验证部署
./verify_p0_hotfix.sh
```

### 选项 2: 手动部署步骤

```bash
# 1. 拉取最新代码
git pull origin server-71

# 2. 构建镜像
docker build -t llm-gateway-go:p0-hotfix .

# 3. 停止旧容器并备份
docker stop llm-gateway-go
docker rename llm-gateway-go llm-gateway-go-backup

# 4. 启动新容器
docker run -d \
    --name llm-gateway-go \
    --restart unless-stopped \
    -p 8080:8080 \
    -e LLM_GATEWAY_DATABASE_URL="$LLM_GATEWAY_DATABASE_URL" \
    -e LLM_GATEWAY_REDIS_URL="$LLM_GATEWAY_REDIS_URL" \
    llm-gateway-go:p0-hotfix

# 5. 检查日志
docker logs -f llm-gateway-go
```

---

## 📋 部署检查清单

### 部署前
- [x] 确认 commit `fdb9a9bd` 已推送到远程
- [ ] 备份当前运行的容器
- [ ] 确认数据库连接信息正确
- [ ] 通知团队即将部署

### 部署后验证
- [ ] 容器运行状态正常
- [ ] 无 ERROR/FATAL 日志
- [ ] request_logs 表有新数据写入
- [ ] 持续监控 15-30 分钟

---

## 🔍 验证命令

```bash
# 检查容器状态
docker ps | grep llm-gateway-go

# 查看日志
docker logs llm-gateway-go --tail 50

# 验证数据库写入
psql $LLM_GATEWAY_DATABASE_URL -c "
SELECT COUNT(*), MAX(ts) 
FROM request_logs 
WHERE ts > now() - interval '5 minutes'
"

# 或使用验证脚本
./verify_p0_hotfix.sh
```

---

## 🚨 回滚步骤

```bash
docker stop llm-gateway-go
docker rm llm-gateway-go
docker rename llm-gateway-go-backup llm-gateway-go
docker start llm-gateway-go
```

---

## 📚 相关文档

- `P0_INCIDENT_SUMMARY.md` - 完整事件总结
- `CRITICAL_BUG_ANALYSIS.md` - 详细根因分析
- `deploy_p0_hotfix.sh` - 自动化部署脚本
- `verify_p0_hotfix.sh` - 验证脚本


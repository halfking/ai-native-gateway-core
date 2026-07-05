# Phase 2 快速部署指南（184测试环境）

⚡ **5分钟快速部署** - 本文档适用于有经验的运维人员

---

## 预检查（1分钟）

```bash
# 本地确认代码
cd __LOCAL_PATH_1__
git log --oneline -1
# 应显示: 3342cfca feat(credentialstate): Phase 2 热度感知探测

# 检查184连接
ssh __SSH_TARGET_3__ "echo OK"
```

---

## 方式一：一键部署脚本（推荐）

```bash
# 本地执行
cd __LOCAL_PATH_1__
./deploy_phase2_to_184.sh
```

**脚本自动完成**:
1. ✓ 本地构建 Linux 二进制
2. ✓ 上传到184
3. ✓ 备份当前版本
4. ✓ 停止→替换→启动
5. ✓ 验证服务状态

---

## 方式二：SSH手动部署（5步骤）

```bash
# 1. SSH登录
ssh __SSH_TARGET_3__

# 2. 拉取代码
cd /data/services/llm-gateway-go
git pull origin main

# 3. 重新构建
go build -o llm-gateway-go ./cmd/gateway/

# 4. 重启服务
systemctl restart llm-gateway

# 5. 验证
tail -50 logs/gateway.log | grep -E "credstate|popularity"
```

**预期输出**:
```
INFO credential state manager created
INFO credential state manager started
```

⚠️ 注意：默认**禁用**热度追踪，需手动启用（见下方）

---

## 数据库准备（2分钟）

```bash
# 在184服务器
cd /data/services/llm-gateway-go

# 连接数据库
psql $(grep "^DATABASE_URL=" .env | cut -d= -f2-)

# 执行以下SQL
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_request_logs_created_at_model 
ON request_logs (created_at DESC, client_model) 
WHERE client_model IS NOT NULL;

-- 验证索引
\di idx_request_logs_created_at_model

-- 测试查询性能
\timing on
SELECT client_model, COUNT(*) 
FROM request_logs 
WHERE created_at > NOW() - INTERVAL '1 hour' 
  AND client_model IS NOT NULL 
GROUP BY client_model 
LIMIT 10;
\timing off
-- 期望耗时 < 500ms

\q
```

---

## 启用热度追踪（可选）

```bash
# 在184服务器
cd /data/services/llm-gateway-go

# 修改配置
sed -i 's/LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false/LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true/' .env

# 重启服务
systemctl restart llm-gateway

# 验证启用成功
sleep 3
tail -50 logs/gateway.log | grep "popularity"
```

**预期日志**:
```
INFO popularity tracking enabled (Phase 2) hot_interval=10s warm_interval=2m cold_interval=10m
INFO credstate: popularity tracking enabled
INFO credstate: popularity tracker started
```

---

## 验证（1分钟）

```bash
# 检查进程
ps aux | grep llm-gateway-go | grep -v grep

# 检查日志（无错误）
tail -100 logs/gateway.log | grep -i error

# 检查端口
netstat -tlnp | grep :__PORT_12__

# 测试请求
curl -s http://localhost:__PORT_12__/health | jq .
```

**健康检查通过指标**:
- [x] 进程存在
- [x] 端口监听
- [x] /health 返回 200
- [x] 日志无 ERROR

---

## 监控（持续）

```bash
# 实时日志
tail -f logs/gateway.log | grep -E "popularity|credstate"

# 每5分钟应看到（如已启用热度追踪）
DEBUG model popularity tracker: refreshed models_tracked=XX

# CPU/内存
top -p $(pgrep llm-gateway-go) -d 10
```

---

## 回滚（如需要）

```bash
# 快速回滚：禁用热度追踪
sed -i 's/POPULARITY_TRACKING=true/POPULARITY_TRACKING=false/' .env
systemctl restart llm-gateway

# 或：回滚二进制
systemctl stop llm-gateway
cp /data/backups/llm-gateway-go/llm-gateway-go.* llm-gateway-go
systemctl start llm-gateway
```

---

## 故障排查速查表

| 问题 | 检查命令 | 解决方案 |
|------|---------|---------|
| 服务未启动 | `systemctl status llm-gateway` | `systemctl start llm-gateway` |
| 端口未监听 | `netstat -tlnp \| grep __PORT_12__` | 检查日志，可能端口冲突 |
| 查询超时 | `tail logs/gateway.log \| grep timeout` | 检查数据库索引 |
| 内存泄漏 | `ps aux \| grep llm-gateway` | 禁用热度追踪，回滚版本 |
| 热度追踪失败 | `grep "popularity.*fail" logs/gateway.log` | 检查数据库连接和表结构 |

---

## 关键指标

**部署前记录**:
- CPU: _____%
- 内存: _____MB
- QPS: _____

**部署后记录**（1小时后）:
- CPU: _____%
- 内存: _____MB
- QPS: _____
- 热度刷新成功次数: _____ (期望12次)

---

## 下一步

- [ ] 观察24小时无异常
- [ ] 记录性能基线
- [ ] 准备生产环境部署计划
- [ ] 添加 Prometheus 监控

---

**部署人**: ____________  
**部署时间**: ____________  
**验证状态**: [ ] ✅ 成功  [ ] ❌ 失败  [ ] 🔄 回滚

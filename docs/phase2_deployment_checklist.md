# Phase 2 热度感知探测 - 部署验证清单

## 部署信息

- **提交**: `3342cfca`
- **分支**: `main`
- **日期**: 2026-07-01
- **目标环境**: 184测试服务器 (8.155.23.184)

---

## 阶段 1: 部署前准备

### 1.1 本地构建验证

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 拉取最新代码
git pull origin main

# 确认提交
git log --oneline -1
# 应显示: 3342cfca feat(credentialstate): Phase 2 热度感知探测

# 本地构建测试
go build ./...
# ✓ 构建成功

# 跨平台构建（Linux amd64）
GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux-amd64 ./cmd/gateway/
ls -lh llm-gateway-go-linux-amd64
# ✓ 二进制文件已生成
```

### 1.2 代码审查确认

- [x] 代码已合并到 main 分支
- [x] Pre-commit hooks 全部通过
- [x] 并发安全（go build -race 通过）
- [x] 类型安全（go vet 通过）
- [x] 资源泄漏检查通过

---

## 阶段 2: 数据库准备（184服务器）

### 2.1 连接到184数据库

```bash
# SSH 到184服务器
ssh root@8.155.23.184

# 切换到项目目录
cd /data/services/llm-gateway-go

# 查看数据库配置
grep "^DB_HOST\|^DATABASE_URL" .env | head -2

# 连接数据库
psql $(grep "^DATABASE_URL=" .env | cut -d= -f2-)
```

### 2.2 执行数据库准备脚本

```bash
# 在184服务器上
cd /data/services/llm-gateway-go

# 下载SQL脚本（如果未通过git拉取）
# 或者手动创建 sql/phase2_db_setup.sql

# 执行脚本
psql $(grep "^DATABASE_URL=" .env | cut -d= -f2-) -f sql/phase2_db_setup.sql > /tmp/phase2_db_check.log 2>&1

# 查看结果
cat /tmp/phase2_db_check.log
```

### 2.3 数据库验证检查点

- [ ] `request_logs` 表存在
- [ ] `client_model` 列存在且类型正确
- [ ] `created_at` 列存在且有索引
- [ ] 索引 `idx_request_logs_created_at_model` 已创建
- [ ] 热度查询耗时 < 500ms
- [ ] 最近1小时有数据（> 0 rows）
- [ ] `client_model` 覆盖率 > 50%

**关键指标**:
```
最近1小时数据量: _______ 行
不同模型数: _______ 个
查询耗时: _______ ms
索引大小: _______ MB
```

---

## 阶段 3: 服务部署（184服务器）

### 3.1 使用自动部署脚本（推荐）

```bash
# 在本地执行
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
./deploy_phase2_to_184.sh
```

**脚本会自动**:
1. 本地构建 Linux 二进制
2. 上传到184服务器
3. 备份当前版本
4. 停止服务
5. 替换二进制
6. 检查配置
7. 启动服务
8. 验证启动状态

### 3.2 手动部署（备选方案）

```bash
# SSH 到184
ssh root@8.155.23.184
cd /data/services/llm-gateway-go

# 拉取最新代码
git pull origin main

# 构建
go build -o llm-gateway-go ./cmd/gateway/

# 备份
cp llm-gateway-go /data/backups/llm-gateway-go/llm-gateway-go.$(date +%Y%m%d_%H%M%S)

# 停止服务
systemctl stop llm-gateway
# 或: supervisorctl stop llm-gateway

# 启动服务
systemctl start llm-gateway
# 或: supervisorctl start llm-gateway

# 检查状态
systemctl status llm-gateway
ps aux | grep llm-gateway-go
```

### 3.3 部署验证检查点

- [ ] 服务进程正在运行
- [ ] 端口正常监听（检查 8080/8443）
- [ ] 日志无错误
- [ ] 版本信息正确（包含 3342cfca commit）

```bash
# 检查进程
ps aux | grep llm-gateway-go | grep -v grep

# 检查端口
netstat -tlnp | grep llm-gateway

# 查看启动日志
tail -50 /data/services/llm-gateway-go/logs/gateway.log | grep -E "credential state manager|popularity"
```

---

## 阶段 4: 热度追踪功能验证

### 4.1 禁用状态验证（默认）

```bash
# 在184服务器
cd /data/services/llm-gateway-go

# 确认配置
grep "POPULARITY_TRACKING" .env
# 应显示: LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false

# 查看日志
tail -100 logs/gateway.log | grep -i "popularity\|credstate"
# 应只看到: "credential state manager started"
# 不应看到: "popularity tracking enabled"
```

**检查点**:
- [ ] 服务正常启动
- [ ] 未启用热度追踪（日志无 "popularity tracking enabled"）
- [ ] 核心功能正常（请求正常路由）

### 4.2 启用热度追踪

```bash
# 在184服务器
cd /data/services/llm-gateway-go

# 修改配置
sed -i 's/LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false/LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true/' .env

# 验证修改
grep "POPULARITY_TRACKING" .env
# 应显示: LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true

# 重启服务
systemctl restart llm-gateway

# 等待启动
sleep 5

# 查看日志
tail -100 logs/gateway.log | grep -E "popularity|credstate|Phase 2"
```

**预期日志输出**:
```
INFO credential state manager created redis_enabled=true
INFO popularity tracking enabled (Phase 2) hot_interval=10s warm_interval=2m cold_interval=10m
INFO credstate: popularity tracking enabled
INFO credstate: popularity tracker started
INFO credential state manager started mem_cache_ttl=10s redis_cache_ttl=5m0s stale_ttl=2m0s
```

### 4.3 功能验证检查点

- [ ] 启动日志包含 "popularity tracking enabled"
- [ ] 启动日志包含 "popularity tracker started"
- [ ] 无错误日志
- [ ] 服务响应正常

### 4.4 热度统计验证（5分钟后）

```bash
# 等待第一次刷新（5分钟）
sleep 300

# 查看刷新日志
tail -200 logs/gateway.log | grep "popularity tracker"
```

**预期日志**:
```
DEBUG model popularity tracker: refreshed models_tracked=XX
```

**如果看到警告**:
```
WARN model popularity tracker: initial refresh failed error=...
WARN model popularity tracker: refresh failed error=...
```

则需要检查：
1. 数据库连接是否正常
2. `request_logs` 表是否存在
3. SQL 查询是否有语法错误
4. 数据库用户权限是否足够

---

## 阶段 5: 性能监控（24小时）

### 5.1 实时监控指标

```bash
# CPU 使用率
top -p $(pgrep llm-gateway-go) -d 10

# 内存使用
ps aux | grep llm-gateway-go | grep -v grep

# 数据库慢查询
psql -c "SELECT query, calls, mean_exec_time FROM pg_stat_statements WHERE query LIKE '%request_logs%' ORDER BY mean_exec_time DESC LIMIT 5;"
```

### 5.2 关键指标记录

**启动时**（T+0）:
```
CPU: _______ %
内存: _______ MB
进程数: _______
```

**1小时后**（T+1h）:
```
CPU: _______ %
内存: _______ MB
刷新次数: _______ (应为 12次)
平均查询耗时: _______ ms
```

**24小时后**（T+24h）:
```
CPU: _______ %
内存: _______ MB
刷新次数: _______ (应为 288次)
错误次数: _______ (应为 0)
平均查询耗时: _______ ms
```

### 5.3 性能基线对比

| 指标 | Phase 1 (无热度追踪) | Phase 2 (启用热度追踪) | 差异 |
|------|---------------------|----------------------|------|
| CPU平均 | _____ % | _____ % | _____ % |
| 内存占用 | _____ MB | _____ MB | _____ MB |
| P50延迟 | _____ ms | _____ ms | _____ ms |
| P99延迟 | _____ ms | _____ ms | _____ ms |

**预期影响**:
- CPU: +0.1% ~ +0.5%
- 内存: +5MB ~ +10MB
- 延迟: 无明显变化（后台异步执行）

---

## 阶段 6: 回滚方案（如遇问题）

### 6.1 快速回滚

```bash
# 在184服务器
cd /data/services/llm-gateway-go

# 禁用热度追踪（最快）
sed -i 's/LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true/LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false/' .env
systemctl restart llm-gateway

# 或者：回滚到上一个版本
systemctl stop llm-gateway
cp /data/backups/llm-gateway-go/llm-gateway-go.YYYYMMDD_HHMMSS llm-gateway-go
systemctl start llm-gateway
```

### 6.2 回滚验证

- [ ] 服务正常启动
- [ ] 日志无错误
- [ ] 核心功能正常
- [ ] 性能恢复正常

---

## 阶段 7: 签收与归档

### 7.1 部署签收

```
部署人员: _____________
部署时间: _____________
验证人员: _____________
验证时间: _____________
签收状态: [ ] 成功  [ ] 失败  [ ] 回滚

备注:
_______________________________________
_______________________________________
```

### 7.2 问题记录

| 问题 | 严重程度 | 解决方案 | 状态 |
|------|---------|---------|------|
|      |         |         |      |
|      |         |         |      |

### 7.3 下一步计划

- [ ] 观察7天无异常后，准备生产环境部署
- [ ] 添加 Prometheus 指标监控
- [ ] 优化热度查询（如需要）
- [ ] Phase 2.1: 自适应阈值调整

---

## 附录 A: 常见问题

### Q1: 热度追踪不生效？

**检查**:
```bash
# 1. 环境变量
grep POPULARITY_TRACKING .env

# 2. 启动日志
grep "popularity" logs/gateway.log

# 3. 数据库连接
psql $(grep DATABASE_URL .env | cut -d= -f2-) -c "SELECT 1"
```

### Q2: 查询耗时过长？

**优化**:
```sql
-- 检查索引
SELECT indexname FROM pg_indexes WHERE tablename = 'request_logs';

-- 分析查询计划
EXPLAIN ANALYZE 
SELECT client_model, COUNT(*) 
FROM request_logs 
WHERE created_at > NOW() - INTERVAL '1 hour' 
  AND client_model IS NOT NULL 
GROUP BY client_model;

-- 如果缺少索引，创建
CREATE INDEX CONCURRENTLY idx_request_logs_created_at_model 
ON request_logs (created_at DESC, client_model) 
WHERE client_model IS NOT NULL;
```

### Q3: 内存占用异常增长？

**检查**:
```bash
# 1. 进程内存
ps aux | grep llm-gateway-go

# 2. Go heap profile（如已开启 pprof）
curl http://localhost:6060/debug/pprof/heap > /tmp/heap.prof
go tool pprof -top /tmp/heap.prof

# 3. 查看热度追踪器状态
# （需添加 debug API，当前版本不支持）
```

---

## 附录 B: 联系人

- **开发负责人**: xutaohuang
- **运维负责人**: _____________
- **紧急联系**: _____________

---

**文档版本**: 1.0  
**最后更新**: 2026-07-01

# 252数据库故障诊断与修复方案

**生成时间**: 2026-11-27 19:05  
**服务器**: 115.29.212.252 (252)  
**数据库**: pg-252-pg17 (PostgreSQL 17 in Docker)

---

## 执行摘要

252服务器上的PostgreSQL数据库处于**异常恢复状态**，无法接受任何连接。这会直接阻止245的部署流程，因为deploy-245.sh依赖252数据库进行迁移验证。

**影响范围**：
- ❌ 245部署流程无法执行（依赖252 PG进行迁移）
- ❌ 154生产环境部署受阻（同样依赖252 PG）
- ⚠️ 现有服务可能受影响（依赖252数据库的查询会失败）

**修复时间**: 预计3-5分钟服务中断

---

## 问题详情

### 1. 核心症状

所有数据库连接尝试返回：
```
FATAL: the database system is in recovery mode
```

### 2. 根本原因

数据库在正常关闭过程中被异常中断（系统崩溃/强制kill），导致：

```bash
# pg_control状态
Database cluster state: shutting down  # ❌ 应为 "in production"
Latest checkpoint: Fri Nov 27 02:53:57 2026  # 16小时前
```

**进程状态**：
```
postgres 2451012  9.6  0.0  Ds  18:56  postgres: startup
                           ^^
                           不可中断睡眠 - 卡在I/O操作
```

### 3. 次要问题

**僵尸psql连接**（自10月21日）：
```
root 1785116  Oct21  psql -U postgres -d llm_gateway
root 1785828  Oct21  psql -U postgres -d llm_gateway
```

这些进程可能持有锁或文件句柄，阻止数据库正常恢复。

### 4. 资源状况

- **容器CPU**: 50.97%（持续高负载）
- **磁盘I/O**: 峰值93.20% util
- **容器运行时长**: 自2026-10-05启动（53天）

---

## 对部署流程的影响

### deploy-245.sh工作流程

```bash
deploy-245.sh
  └→ deploy-seamless.sh deploy 245
      └→ db-changelog.sh (步骤 6.5/9)
          └→ 252-db-tunnel.sh
              └→ db252_tunnel_ensure()
                  └→ _db252_psql()  # ❌ 在此失败
```

**失败点**：
- `_db252_psql()` 尝试连接到252数据库验证健康状态
- 连接超时或返回 "recovery mode" 错误
- 整个部署流程中断

**相关代码** (`deploy-lib/db-changelog.sh`):
```bash
# 切换前在 252 PG 上应用 sql/migrations/startup/NNN_*.sql
# 避免 restart 时 EnsureSchema 长时间阻塞
```

---

## 修复方案

### 方案A: 完全重启 (推荐) ⭐

**优点**：
- 彻底解决状态不一致问题
- 清理僵尸进程
- 触发PostgreSQL崩溃恢复机制

**缺点**：
- 3-5分钟服务中断

**执行步骤**：

```bash
# 1. 执行修复脚本（已上传到252）
ssh 252 'bash /tmp/fix-252-db.sh'

# 2. 等待输出显示"✅ 数据库恢复成功"

# 3. 验证修复结果
ssh 252 'bash /tmp/verify-252-after-fix.sh'
```

**预期输出**：
```
步骤1: 停止PostgreSQL容器...
✅ 容器已停止

步骤3: 启动PostgreSQL容器...
✅ 容器已启动

步骤4: 等待数据库恢复（最多3分钟）...
  等待中... (1/36)
  等待中... (2/36)
  ...
✅ 数据库恢复成功

步骤5: 验证数据库状态...
Database cluster state: in production  # ✅ 正常
```

### 方案B: 仅杀僵尸进程 (不推荐)

如果完全不能接受服务中断，可以尝试：

```bash
ssh 252 "docker exec pg-252-pg17 pkill -f 'psql.*Oct21'"
```

**风险**：
- 不能解决"shutting down"状态
- startup进程仍会卡住
- 只是暂时缓解，不是根本解决

---

## 验证检查清单

修复后必须验证以下项目：

### 数据库层面

```bash
# 1. 集群状态
ssh 252 "docker exec pg-252-pg17 \
  /usr/lib/postgresql/17/bin/pg_controldata /var/lib/postgresql/data \
  | grep 'cluster state'"
# 期望: in production

# 2. 连接测试
ssh 252 "docker exec pg-252-pg17 \
  psql -U llm_gateway -d llm_gateway -c 'SELECT version();'"
# 期望: 返回PostgreSQL版本信息

# 3. 表可访问性
ssh 252 "docker exec pg-252-pg17 \
  psql -U llm_gateway -d llm_gateway -c '\dt' | wc -l"
# 期望: 返回表数量（>0）
```

### 部署流程验证

```bash
# 4. 252隧道连接测试
cd ~/workspace/llm-gateway-go
source deploy/envs/configs/env-252.sh
source scripts/lib/252-db-tunnel.sh
db252_tunnel_ensure
# 期望: "tunnel ready (PID XXXXX)"

# 5. 迁移检查（干跑）
bash scripts/deploy-245.sh --dry-run
# 期望: 返回245配置信息的JSON
```

### 应用层验证

```bash
# 6. 245健康检查
ssh 245 "curl -f http://localhost:8781/healthz"
# 期望: HTTP 200

# 7. 数据库查询测试
ssh 245 "curl -f http://localhost:8781/api/health/db"
# 期望: HTTP 200，返回数据库状态
```

---

## 执行时间建议

**最佳维护窗口**：
- 凌晨2:00-5:00（流量最低）
- 或部署前30分钟（确保修复完成后再部署）

**紧急情况**（245部署正在等待）：
- 立即执行方案A
- 通知相关团队3-5分钟中断
- 修复完成后立即执行245部署

---

## 预防措施

### 1. 添加健康监控

```bash
# 监控pg_control状态
*/5 * * * * docker exec pg-252-pg17 \
  /usr/lib/postgresql/17/bin/pg_controldata /var/lib/postgresql/data \
  | grep -v "in production" && echo "ALERT: PG not in production"
```

### 2. 配置优雅关闭

在`docker-compose.yml`或systemd单元中：
```yaml
stop_grace_period: 300s  # 给PostgreSQL 5分钟完成checkpoint
```

### 3. 定期清理长连接

```sql
-- 杀掉超过24小时的idle连接
SELECT pg_terminate_backend(pid) 
FROM pg_stat_activity 
WHERE state = 'idle' 
  AND state_change < NOW() - INTERVAL '24 hours';
```

### 4. 监控僵尸进程

```bash
# 每天检查超过7天的psql进程
docker exec pg-252-pg17 ps aux | awk '$9 ~ /^[A-Z][a-z]{2}[0-9]{2}$/'
```

---

## 附录：已上传的脚本

### /tmp/fix-252-db.sh
主修复脚本，执行停止→启动→验证流程

### /tmp/verify-252-after-fix.sh  
修复后的完整验证脚本

### 使用方法

```bash
# 修复
ssh 252 'bash /tmp/fix-252-db.sh'

# 验证
ssh 252 'bash /tmp/verify-252-after-fix.sh'
```

---

## 下一步行动

1. **立即**: 决定修复时间窗口
2. **修复前**: 通知依赖252数据库的服务团队
3. **执行**: 运行 `/tmp/fix-252-db.sh`
4. **验证**: 完整验证检查清单
5. **部署**: 执行 `bash scripts/deploy-245.sh`

---

## 联系与支持

如有问题，请查看：
- PostgreSQL日志: `ssh 252 "docker logs pg-252-pg17 2>&1 | tail -100"`
- 系统负载: `ssh 252 "top -b -n 1 | head -20"`
- 磁盘状态: `ssh 252 "df -h"`

**报告生成**: Kiro AI Agent  
**审计追踪**: 此报告记录于Git仓库供后续参考

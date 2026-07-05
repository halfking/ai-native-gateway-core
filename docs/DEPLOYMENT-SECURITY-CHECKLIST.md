# 部署安全检查清单

**版本**: 1.0  
**日期**: 2026-06-30  
**适用**: llm-gateway-go 及所有服务

---

## 一、部署前检查

### 1.1 环境确认

- [ ] 确认目标环境（local / test-184 / prod-71）
- [ ] 确认当前分支和版本号
- [ ] 确认是否需要变更审批（生产环境必需）
- [ ] 确认回滚方案已准备

### 1.2 代码审查

- [ ] 所有代码已通过Code Review
- [ ] 没有包含 TODO / FIXME 的关键代码
- [ ] 没有注释掉的调试代码
- [ ] 没有硬编码的敏感信息

### 1.3 敏感信息检查

- [ ] 所有IP地址使用占位符（`__SECRET_1__`, `__INTERNAL_PUBLIC_IP__`）
- [ ] 所有密码使用占位符（`__SECRET_2__`）
- [ ] 没有提交`.env`文件到仓库
- [ ] 所有敏感配置使用环境变量

### 1.4 依赖检查

- [ ] `go.mod` / `package.json` 无已知漏洞依赖
- [ ] 依赖版本已锁定（不使用 `latest`）
- [ ] 第三方库来源可信

---

## 二、部署中检查

### 2.1 构建验证

- [ ] 本地构建成功
- [ ] 单元测试全部通过
- [ ] 集成测试通过（如有）
- [ ] 静态代码分析无严重问题

### 2.2 镜像安全（Docker部署）

- [ ] 使用官方基础镜像或经审计的 `kx-base:*`
- [ ] 镜像大小合理（无冗余文件）
- [ ] 运行用户非root
- [ ] 包含健康检查配置
- [ ] 审计报告已填写（参考 kx-image-build）

### 2.3 网络安全

- [ ] 防火墙规则已配置
- [ ] 只开放必要端口
- [ ] 内部服务不暴露公网
- [ ] 使用HTTPS/TLS（生产环境）

### 2.4 数据库安全

- [ ] 数据库迁移脚本已在测试环境验证
- [ ] 备份已确认可用
- [ ] 数据库用户权限最小化
- [ ] 敏感字段已加密（如需要）
- [ ] RLS策略已配置（如需要）

---

## 三、部署后验证

### 3.1 服务健康检查

```bash
# 健康检查端点
curl -fsS https://llmgateway.example.com/healthz | jq .

# 预期响应
{
  "status": "ok",
  "version": "v1.2.3",
  "uptime": "5m30s"
}
```

- [ ] 健康检查返回200
- [ ] 版本号正确
- [ ] 启动时间合理

### 3.2 功能验证

```bash
# 基础功能测试
./scripts/smoke-test.sh --env prod

# 关键API测试
curl -X POST https://llmgateway.example.com/v1/chat \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: test-tenant" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}'
```

- [ ] 关键API可访问
- [ ] 响应时间正常
- [ ] 错误处理正确

### 3.3 数据验证

```bash
# 数据库连接测试
docker exec postgres psql -U kxuser -d llm_gateway -c "SELECT 1"

# 表数量验证
docker exec postgres psql -U kxuser -d llm_gateway -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'"
```

- [ ] 数据库连接正常
- [ ] 表结构完整
- [ ] 关键数据存在

### 3.4 日志检查

```bash
# 查看应用日志
docker logs llm-gateway-go --tail=100

# 查看错误日志
docker logs llm-gateway-go 2>&1 | grep -i error

# 查看系统日志
journalctl -u llm-gateway-go -n 100
```

- [ ] 无启动错误
- [ ] 无异常堆栈
- [ ] 日志级别正确

### 3.5 监控验证

- [ ] Prometheus指标可采集
- [ ] Grafana面板显示正常
- [ ] 告警规则已配置
- [ ] 日志聚合工作正常

---

## 四、安全加固

### 4.1 进程安全

```bash
# 检查进程命令行无敏感信息
ps aux | grep llm-gateway | grep -v grep

# 预期：无密码、无密钥
```

- [ ] 进程命令行无密码
- [ ] 进程命令行无密钥
- [ ] 进程运行用户正确

### 4.2 文件权限

```bash
# 检查配置文件权限
ls -la __SERVER_PATH_3__/

# 预期：敏感文件 chmod 600
```

- [ ] `.env` 文件权限 600
- [ ] 私钥文件权限 600
- [ ] 配置目录权限 700
- [ ] 日志目录权限适当

### 4.3 网络安全

```bash
# 检查监听端口
ss -tlnp | grep llm-gateway

# 检查防火墙规则
iptables -L -n | grep __PORT_2__
```

- [ ] 只监听必要端口
- [ ] 监听地址正确（0.0.0.0 或内网IP）
- [ ] 防火墙规则生效

### 4.4 认证授权

- [ ] API Key验证启用
- [ ] JWT签名密钥已配置
- [ ] 租户隔离已验证
- [ ] 权限控制正常

---

## 五、生产环境特别检查（仅71）

### 5.1 变更管理

- [ ] 变更单号已记录：`CHG-2026-XXX`
- [ ] 变更时间窗口已确认
- [ ] 相关人员已通知
- [ ] 回滚步骤已演练

### 5.2 数据保护

- [ ] 禁止从生产同步数据到其他环境
- [ ] 生产数据库备份已验证
- [ ] 增量备份正常运行
- [ ] 备份保留期符合要求

### 5.3 访问控制

- [ ] SSH密钥认证启用（禁用密码认证）
- [ ] 操作审计日志启用
- [ ] 多因素认证已配置（如适用）
- [ ] 最小权限原则

### 5.4 合规检查

- [ ] 符合数据保护法规
- [ ] 敏感数据已脱敏
- [ ] 审计日志保留期符合要求
- [ ] 安全扫描已完成

---

## 六、回滚准备

### 6.1 回滚条件

满足以下任一条件立即回滚：
- [ ] 健康检查失败超过5分钟
- [ ] 错误率超过5%
- [ ] 响应时间超过基线200%
- [ ] 数据一致性问题
- [ ] 安全漏洞发现

### 6.2 回滚步骤

```bash
# 1. 停止新版本服务
systemctl stop llm-gateway-go

# 2. 恢复旧版本
docker tag llm-gateway-go:previous llm-gateway-go:current
systemctl start llm-gateway-go

# 3. 验证服务
curl -fsS http://localhost:__PORT_2__/healthz

# 4. 回滚数据库（如需要）
psql -U kxuser -d llm_gateway < /backup/schema-before-deploy.sql
```

### 6.3 回滚验证

- [ ] 服务启动成功
- [ ] 健康检查通过
- [ ] 关键功能可用
- [ ] 数据一致性正常

---

## 七、事后检查

### 7.1 监控观察

部署后观察15-30分钟：
- [ ] CPU使用率正常
- [ ] 内存使用率正常
- [ ] 磁盘I/O正常
- [ ] 网络流量正常
- [ ] 数据库连接池正常

### 7.2 日志审计

- [ ] 部署操作已记录到审计日志
- [ ] 所有SSH访问已记录
- [ ] 数据库变更已记录
- [ ] 配置变更已记录

### 7.3 文档更新

- [ ] 部署文档已更新
- [ ] 变更日志已更新
- [ ] 运维文档已更新
- [ ] 故障排查手册已更新

---

## 八、应急联系

### 8.1 关键联系人

| 角色 | 联系方式 | 职责 |
|------|---------|------|
| DevOps Lead | [联系方式] | 部署协调 |
| DBA | [联系方式] | 数据库问题 |
| Security Team | [联系方式] | 安全事件 |
| On-call Engineer | [联系方式] | 紧急问题 |

### 8.2 升级路径

1. 普通问题 → DevOps Team
2. 数据库问题 → DBA
3. 安全问题 → Security Team + 立即回滚
4. 紧急故障 → On-call Engineer + 立即回滚

---

## 九、检查清单摘要

### 快速检查（5分钟）

```bash
# 1. 环境确认
echo "Target: $TARGET_ENV"
[ "$TARGET_ENV" = "prod" ] && echo "⚠️  PRODUCTION" || echo "✅ Test"

# 2. 敏感信息检查
grep -r "14.103" . --include="*.sh" --include="*.go" && echo "❌ Hard-coded IP found" || echo "✅ No hard-coded IP"

# 3. 服务健康
curl -fsS http://localhost:__PORT_2__/healthz && echo "✅ Healthy" || echo "❌ Unhealthy"

# 4. 日志检查
docker logs llm-gateway-go --tail=50 | grep -i error && echo "⚠️  Errors found" || echo "✅ No errors"

# 5. 进程检查
ps aux | grep llm-gateway | grep -E "(password|secret|key)" && echo "❌ Sensitive info in ps" || echo "✅ Process clean"
```

### 完整检查（30分钟）

- [ ] 第一部分：部署前检查（10分钟）
- [ ] 第二部分：部署中检查（5分钟）
- [ ] 第三部分：部署后验证（10分钟）
- [ ] 第四部分：安全加固（5分钟）

### 生产部署额外检查（+15分钟）

- [ ] 第五部分：生产环境特别检查
- [ ] 第六部分：回滚准备验证

---

## 十、常见问题

**Q: 部署到71需要什么审批？**  
A: 需要DBA审批的变更单（CHG-XXX），包含变更内容、影响评估、回滚方案。

**Q: 如何确认敏感信息已清理？**  
A: 运行 `scripts/scan-secrets.sh` 或 `grep -r "192.168.1\|14.103" . --include="*.sh"`

**Q: 部署失败如何快速回滚？**  
A: 参考第六部分回滚步骤，或执行 `./scripts/rollback-prod.sh --env 71`

**Q: 如何验证数据库迁移安全？**  
A: 先在184测试环境验证，确认Schema兼容性，再申请71部署。

**Q: 健康检查失败怎么办？**  
A: 立即检查日志 `docker logs llm-gateway-go --tail=100`，如5分钟内未恢复，执行回滚。

---

## 附录A: 自动化脚本

### A.1 部署前检查脚本

```bash
#!/bin/bash
# scripts/pre-deploy-check.sh

echo "=== Pre-deployment Security Check ==="

# 1. 硬编码IP检查
if grep -r "192.168.1\|__PUB_IP_2__\|__PUB_IP_1__" . \
    --include="*.sh" --include="*.go" --include="*.yaml" \
    --exclude-dir=vendor --exclude-dir=node_modules | grep -v "DISABLED"; then
    echo "❌ Hard-coded IP addresses found"
    exit 1
fi
echo "✅ No hard-coded IPs"

# 2. 敏感信息检查
if grep -r "password.*=.*[^_]" . --include="*.sh" --include="*.env" | grep -v "REDACTED"; then
    echo "❌ Plain-text passwords found"
    exit 1
fi
echo "✅ No plain-text passwords"

# 3. .env文件检查
if git ls-files | grep "\.env$" | grep -v "\.env\.example"; then
    echo "❌ .env file in git"
    exit 1
fi
echo "✅ No .env in git"

echo "=== Pre-deployment check PASSED ==="
```

### A.2 部署后验证脚本

```bash
#!/bin/bash
# scripts/post-deploy-verify.sh

TARGET="$1"
[ -z "$TARGET" ] && { echo "Usage: $0 <host>"; exit 1; }

echo "=== Post-deployment Verification ==="

# 1. 健康检查
if curl -fsS "http://$TARGET:__PORT_2__/healthz" > /dev/null; then
    echo "✅ Health check passed"
else
    echo "❌ Health check failed"
    exit 1
fi

# 2. 版本检查
VERSION=$(curl -s "http://$TARGET:__PORT_2__/healthz" | jq -r .version)
echo "Version: $VERSION"

# 3. 日志检查
ssh "root@$TARGET" "docker logs llm-gateway-go --tail=50" | grep -i "error\|fatal" && {
    echo "⚠️  Errors in logs"
} || {
    echo "✅ No errors in logs"
}

echo "=== Verification complete ==="
```

---

**最后更新**: 2026-06-30  
**维护者**: DevOps Team  
**相关文档**: 
- [数据库环境分离规范](./DATABASE-ENVIRONMENT-SEPARATION.md)
- [部署检查清单](./partition/MONTHLY_CHECKLIST.md)
- [安全审计报告](SECURITY-AUDIT-2026-06-28.md)

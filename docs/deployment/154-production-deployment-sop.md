# 154 生产环境部署 SOP - SSE 验证功能

**版本**: v1.0  
**创建日期**: 2026-08-29  
**功能**: SSE Frame JSON Validation  
**目标环境**: 154 生产环境  
**预计时间**: 15-20 分钟  

---

## 1. 部署概述

### 1.1 功能说明
- **问题**: 上游供应商（minimax-m3, glm-5.2）发送不完整的 JSON 帧导致流式处理失败
- **解决方案**: 在处理前验证 SSE 帧的 JSON 完整性，拒绝格式错误的帧
- **影响范围**: 所有流式请求，特别是 MiniMax 和 GLM 模型

### 1.2 测试环境验证
- **245 环境**: 已运行 4 小时，965 个请求，0 个 malformed_sse_frame 错误
- **测试覆盖**: 33 个单元/集成测试，全部通过
- **性能影响**: <10% 开销（~100ns 每帧）
- **假阳性**: 0 个

### 1.3 部署时间窗口
- **推荐时间**: 非高峰时段（02:00-05:00 或 14:00-16:00）
- **维护时长**: 预计 2-3 分钟服务中断
- **回滚时长**: 1 分钟（如需要）

---

## 2. 部署前检查清单

### 2.1 环境检查
```bash
# 2.1.1 验证当前服务状态
□ ssh root@<154-IP> "systemctl status llmgo-154.service"
  预期: active (running)

# 2.1.2 检查当前版本
□ curl http://<154-IP>:8781/api/system/version
  预期: 返回当前版本信息

# 2.1.3 检查磁盘空间
□ ssh root@<154-IP> "df -h /opt"
  预期: 至少 1GB 可用空间

# 2.1.4 检查数据库连接
□ ssh root@<154-IP> 'PGPASSWORD="***REDACTED***" psql -h <env:HOST_252_INTERNAL_IP> -p 5432 -U llm_gateway -d llm_gateway -c "SELECT 1;"'
  预期: 返回 1
```

### 2.2 代码准备
```bash
# 2.2.1 本地编译
□ cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
□ git checkout main
□ git pull origin main
□ make build  # 或 go build -o gateway ./cmd/gateway
  预期: 编译成功，生成 gateway 二进制

# 2.2.2 验证编译版本
□ ./gateway --version
  预期: 包含 SSE 验证功能的版本

# 2.2.3 验证二进制大小
□ ls -lh gateway
  预期: 约 40-60 MB
```

### 2.3 通知与协调
```bash
□ 通知团队成员即将部署
□ 确认无其他部署正在进行
□ 准备好监控面板（Grafana）
□ 准备好日志查看终端
```

---

## 3. 详细部署步骤

### 步骤 1: 备份当前版本
```bash
# 登录生产服务器
ssh root@<154-IP>

# 进入部署目录
cd /opt/llm-gateway-go

# 创建带时间戳的备份
BACKUP_NAME="gateway.backup.$(date +%Y%m%d_%H%M%S)"
cp gateway $BACKUP_NAME
ls -lh $BACKUP_NAME

# 验证备份
□ 检查: 备份文件存在且大小正常
```

**预期输出**:
```
-rwxr-xr-x 1 root root 45M Aug 29 14:30 gateway.backup.20260829_143000
```

---

### 步骤 2: 上传新版本
```bash
# 在本地执行
scp gateway root@<154-IP>:/opt/llm-gateway-go/gateway.new

# 验证上传
ssh root@<154-IP> "ls -lh /opt/llm-gateway-go/gateway.new"

□ 检查: 文件大小与本地一致
```

**预期输出**:
```
-rw-r--r-- 1 root root 45M Aug 29 14:31 gateway.new
```

---

### 步骤 3: 替换二进制并重启服务
```bash
# 在服务器上执行
ssh root@<154-IP>

cd /opt/llm-gateway-go

# 替换二进制
mv gateway.new gateway
chmod +x gateway

# 重启服务（关键步骤）
systemctl restart llmgo-154.service

# 等待 3 秒
sleep 3

# 检查服务状态
systemctl status llmgo-154.service

□ 检查: 服务显示 active (running)
□ 检查: 无错误日志
```

**预期输出**:
```
● llmgo-154.service - LLM Gateway Service
   Loaded: loaded (/etc/systemd/system/llmgo-154.service; enabled)
   Active: active (running) since Thu 2026-08-29 14:32:15 CST; 3s ago
 Main PID: 12345 (gateway)
   CGroup: /system.slice/llmgo-154.service
           └─12345 /opt/llm-gateway-go/gateway
```

---

### 步骤 4: 验证部署成功

#### 4.1 版本检查
```bash
curl http://<154-IP>:8781/api/system/version

□ 检查: 版本号正确
□ 检查: build_time 为最新
```

**预期输出**:
```json
{
  "version": "v1.x.x",
  "build_time": "2026-08-29T14:30:00Z",
  "go_version": "go1.22.x"
}
```

#### 4.2 健康检查
```bash
curl http://<154-IP>:8781/api/system/health

□ 检查: 返回 200 OK
□ 检查: 数据库连接正常
```

**预期输出**:
```json
{
  "status": "healthy",
  "database": "connected",
  "timestamp": "2026-08-29T14:32:20Z"
}
```

#### 4.3 启动日志检查
```bash
journalctl -u llmgo-154.service --since '1 minute ago' -n 50

□ 检查: 无 ERROR 或 FATAL
□ 检查: 看到 "service started" 或类似消息
□ 检查: 数据库连接成功日志
```

#### 4.4 测试请求
```bash
# 测试 MiniMax 模型（高风险提供商）
curl -X POST http://<154-IP>:8781/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <TEST_TOKEN>" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'

□ 检查: 返回流式响应
□ 检查: 无连接错误
□ 检查: 完整的 SSE 事件序列
```

---

## 4. 部署后监控（前 30 分钟）

### 4.1 实时日志监控
```bash
# 开启日志监控（保持窗口打开）
ssh root@<154-IP> "journalctl -u llmgo-154.service -f"

# 关注事项:
□ 监控 5 分钟: 无 ERROR 或 WARN
□ 监控 malformed_sse_frame 日志（预期为 0）
□ 监控 panic 或 crash（预期无）
```

### 4.2 数据库监控
```bash
# 查询最近 10 分钟的请求统计
ssh root@<154-IP> 'PGPASSWORD="***REDACTED***" psql -h <env:HOST_252_INTERNAL_IP> -p 5432 -U llm_gateway -d llm_gateway' <<EOF
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    COUNT(*) FILTER (WHERE success = false) AS failed_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / NULLIF(COUNT(*), 0), 2) AS success_rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '10 minutes';
EOF

□ 检查: success_rate > 85%
□ 检查: 有新请求进入
```

### 4.3 Prometheus 指标监控
```bash
# 访问 Prometheus 查询界面
# http://<PROMETHEUS_IP>:9090

# 执行以下查询:

# 1. 检查 malformed SSE frame 指标
llm_gateway_malformed_sse_frame_total

□ 检查: 指标存在（即使值为 0）
□ 检查: 5 分钟内增量为 0

# 2. 检查请求成功率
rate(llm_gateway_requests_total{status="success"}[5m]) / 
rate(llm_gateway_requests_total[5m])

□ 检查: 成功率 > 0.85

# 3. 检查 MiniMax 模型错误率
rate(llm_gateway_requests_total{provider="minimax",status="failed"}[5m])

□ 检查: 错误率低或为 0
```

### 4.4 关键指标阈值

| 指标 | 正常范围 | 警告阈值 | 严重阈值 | 行动 |
|------|---------|---------|---------|------|
| 成功率 | >90% | <85% | <70% | 监控/回滚 |
| Malformed SSE 率 | 0% | >1% | >5% | 调查 |
| 服务响应时间 | <2s | >5s | >10s | 检查 |
| 内存使用 | <70% | >80% | >90% | 重启 |

---

## 5. 回滚步骤和条件

### 5.1 回滚触发条件

**立即回滚**（P0）:
- [ ] 服务无法启动或频繁重启
- [ ] 成功率 < 50% 持续 5 分钟
- [ ] 出现 panic 或 crash
- [ ] 数据库连接完全失败

**考虑回滚**（P1）:
- [ ] 成功率 < 85% 持续 10 分钟
- [ ] Malformed SSE frame 率 > 10%
- [ ] 大量假阳性（有效帧被拒绝）
- [ ] 响应时间显著增加（>2x）

### 5.2 快速回滚步骤
```bash
# 1. 停止服务
ssh root@<154-IP> "systemctl stop llmgo-154.service"

# 2. 恢复备份（使用最新的备份）
ssh root@<154-IP> "cd /opt/llm-gateway-go && mv gateway gateway.failed && mv gateway.backup.20260829_143000 gateway"

# 3. 启动服务
ssh root@<154-IP> "systemctl start llmgo-154.service"

# 4. 验证回滚
ssh root@<154-IP> "systemctl status llmgo-154.service"
curl http://<154-IP>:8781/api/system/version

# 5. 检查服务恢复
□ 验证版本已回退
□ 验证服务运行正常
□ 验证请求成功率恢复

# 预计回滚时间: 1-2 分钟
```

### 5.3 回滚后操作
```bash
□ 通知团队回滚已完成
□ 保存失败日志用于分析
□ 记录回滚原因和现象
□ 创建问题报告
□ 安排复盘会议
```

---

## 6. 验证检查清单

### 6.1 立即验证（部署后 5 分钟）
- [ ] 服务状态: active (running)
- [ ] 版本正确
- [ ] 健康检查通过
- [ ] 无启动错误日志
- [ ] 数据库连接正常
- [ ] 测试请求成功

### 6.2 短期验证（部署后 30 分钟）
- [ ] 成功率 > 85%
- [ ] 无 malformed_sse_frame 错误（或极低）
- [ ] MiniMax 模型请求正常
- [ ] 无假阳性报告
- [ ] 响应时间正常
- [ ] 内存使用正常

### 6.3 中期验证（部署后 2-4 小时）
- [ ] 成功率稳定在正常范围
- [ ] Prometheus 指标正常
- [ ] 无异常日志模式
- [ ] 用户无异常反馈
- [ ] 系统资源使用正常

### 6.4 长期验证（部署后 24 小时）
- [ ] 生成监控报告
- [ ] 与历史数据对比
- [ ] 确认修复有效（无 minimax/glm 相关错误）
- [ ] 性能无退化
- [ ] 更新部署记录

---

## 7. 紧急联系方式

### 7.1 技术团队
| 角色 | 联系方式 | 响应时间 |
|------|---------|---------|
| 后端负责人 | 待补充 | 24/7 |
| 运维负责人 | 待补充 | 24/7 |
| 数据库管理员 | 待补充 | 工作时间 |
| 技术经理 | 待补充 | 紧急情况 |

### 7.2 升级路径
1. **Level 1**: 后端工程师（尝试修复/回滚）
2. **Level 2**: 后端负责人 + 运维负责人
3. **Level 3**: 技术经理（重大问题决策）

### 7.3 问题报告模板
```
【生产问题】154 部署异常

时间: YYYY-MM-DD HH:MM
环境: 154 生产
功能: SSE 验证功能
现象: [描述问题]
影响: [影响范围]
已采取措施: [回滚/修复]
当前状态: [服务状态]
```

---

## 8. 预期时间表

| 阶段 | 预计时间 | 累计时间 | 说明 |
|------|---------|---------|------|
| 部署前检查 | 3 分钟 | 3 分钟 | 环境验证 |
| 备份当前版本 | 1 分钟 | 4 分钟 | 文件备份 |
| 上传新版本 | 2 分钟 | 6 分钟 | 网络传输 |
| 重启服务 | 30 秒 | 6.5 分钟 | **服务中断** |
| 基础验证 | 3 分钟 | 9.5 分钟 | 健康检查 |
| 功能测试 | 5 分钟 | 14.5 分钟 | 测试请求 |
| 初期监控 | 30 分钟 | 44.5 分钟 | 实时监控 |

**总计**: 约 15 分钟部署 + 30 分钟监控

---

## 9. 成功标准

部署被视为成功当满足以下所有条件:

### 9.1 功能正常
- [x] 服务启动成功无错误
- [x] 所有健康检查通过
- [x] 测试请求返回正确结果
- [x] 流式响应正常工作

### 9.2 性能达标
- [x] 成功率 > 85%（与历史一致）
- [x] 响应时间无显著增加（<10%）
- [x] 系统资源使用正常

### 9.3 监控就绪
- [x] Prometheus 指标正常采集
- [x] 日志正常输出
- [x] 无 malformed_sse_frame 错误（或极低）
- [x] Grafana 面板显示正常

### 9.4 业务指标
- [x] MiniMax 模型请求成功率正常
- [x] GLM 模型请求成功率正常
- [x] 无用户异常反馈

---

## 10. 部署后任务

### 10.1 当天任务
- [ ] 完成部署记录文档
- [ ] 通知团队部署成功
- [ ] 继续监控 4-6 小时
- [ ] 生成初步监控报告

### 10.2 后续任务
- [ ] 24 小时后生成完整监控报告
- [ ] 与 245 测试数据对比
- [ ] 更新部署流程文档（如有改进）
- [ ] 安排技术分享（如有必要）

---

## 11. 附录

### 11.1 相关文档
- `docs/audit/2026-08-29-sse-validation-audit.md` - 功能审计报告
- `docs/monitoring/grafana-sse-validation-dashboard.json` - Grafana 配置
- `.handoff/2026-08-29-245-monitoring-report-24h.md` - 245 监控报告
- `docs/deployment/154-deployment-risk-assessment.md` - 风险评估

### 11.2 技术参考
- SSE 规范: https://html.spec.whatwg.org/multipage/server-sent-events.html
- 验证器实现: `domains/streaming/sse_frame_validator.go`
- 集成测试: `domains/streaming/sse_frame_validator_integration_test.go`

### 11.3 历史记录
| 日期 | 版本 | 变更 | 作者 |
|------|------|------|------|
| 2026-08-29 | v1.0 | 初始版本 | AI Agent |

---

## 12. 脚本使用

部署可使用自动化脚本简化流程:

```bash
# 使用自动化部署脚本
./scripts/deploy-to-154.sh

# 使用健康检查脚本
./scripts/health-check-154.sh

# 如需回滚
./scripts/rollback-154.sh
```

详细脚本说明参见各脚本文件。

---

**文档版本**: v1.0  
**最后更新**: 2026-08-29  
**维护者**: DevOps Team  
**状态**: 准备就绪

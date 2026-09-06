# 本地部署测试 - 快速参考

本目录包含 LLM Gateway 本地部署的完整测试指南和自动化脚本。

## 📚 文档

### [LOCAL_DEPLOYMENT_TEST_GUIDE.md](./local-deployment-test-guide.md)
**完整的本地部署测试指南**（50+ 页）

涵盖内容：
- ✅ 一键部署流程
- ✅ L1-L4 验证层级（健康检查 → 依赖连通 → 功能链路 → 业务场景）
- ✅ Mock Provider 测试场景
- ✅ 故障注入与错误处理验证
- ✅ 故障排查清单
- ✅ 性能验证与压力测试
- ✅ 高级用法（多实例、调试模式、pprof）

**适合**: 新手入门、完整功能验证、部署前检查

---

## 🔧 脚本

### [scripts/verify-local-deployment.sh](../scripts/verify-local-deployment.sh)
**自动化验证脚本**

功能：
- 自动执行 L1-L4 验证
- 生成详细 Markdown 报告
- 彩色控制台输出
- 支持快速模式和完整模式

用法：
```bash
# 完整验证
./scripts/verify-local-deployment.sh

# 快速验证（仅 L1-L2）
./scripts/verify-local-deployment.sh --quick

# 包含 Mock Provider 测试
./scripts/verify-local-deployment.sh --with-mock

# 指定端口
./scripts/verify-local-deployment.sh --port 8782
```

---

## 🚀 快速开始

### 1. 部署本地实例

```bash
# 使用现有部署脚本
./scripts/deploy-local.sh deploy
```

### 2. 验证部署

```bash
# 自动验证
./scripts/verify-local-deployment.sh

# 或使用 deploy-local.sh 内置验证
./scripts/deploy-local.sh verify
```

### 3. 查看状态

```bash
./scripts/deploy-local.sh status
```

---

## 📊 验证层级

| 层级 | 名称 | 检查内容 | 通过标准 |
|------|------|---------|---------|
| **L1** | 基础健康检查 | `/healthz`, `/readyz`, `/version` | 所有端点返回 2xx，JSON 格式正确 |
| **L2** | 依赖连通性 | PostgreSQL, Redis, Docker 容器 | 数据库和缓存均可访问 |
| **L3** | 功能链路 | Admin API, OpenAI API, 前端 | 核心端点可达，认证正常 |
| **L4** | 业务场景 | 日志、进程、数据库表、Mock | 无错误日志，关键表存在 |

---

## 🧪 测试场景

根据 [comprehensive-test-plan.md](../05-testing/02-test-plans/testing/comprehensive-test-plan.md)，以下是必测场景：

### 场景 1: Provider 逐渐变慢
验证延迟检测和权重调整

### 场景 2: Provider 突发 5xx
验证快速失败切换

### 场景 3: Quota 耗尽
验证 429 处理和定时恢复

### 场景 4: 所有 Provider 故障
验证优雅降级

详见 [LOCAL_DEPLOYMENT_TEST_GUIDE.md 第 5 章](./local-deployment-test-guide.md#5-测试场景)

---

## 📁 相关文档

### 测试策略
- [test-matrix.md](../05-testing/01-strategy/test-matrix.md) - 当前测试矩阵

### 测试方案
- [comprehensive-test-plan.md](../05-testing/02-test-plans/testing/comprehensive-test-plan.md) - 全方位测试方案
- [客户端会话保持-测试方案与用例.md](../05-testing/02-test-plans/会话优化v4/客户端会话保持-测试方案与用例.md) - 会话优化 v4

### 测试用例
- [TC-GOAL-CONTINUE.md](../05-testing/03-test-cases/TC-GOAL-CONTINUE.md) - `gw-continue` 测试用例
- [TC-GOAL-HANDOFF.md](../05-testing/03-test-cases/TC-GOAL-HANDOFF.md) - `gw-handoff` 测试用例

---

## 🔍 故障排查

### 常见问题

| 问题 | 可能原因 | 解决方案 |
|------|---------|---------|
| 健康检查超时 | 网关未启动 | `./scripts/deploy-local.sh start` |
| 数据库连接失败 | PostgreSQL 容器未运行 | `docker start llm-gateway-pg` |
| Redis 连接失败 | Redis 容器未运行 | `docker start llm-gateway-redis` |
| 端口冲突 | 8782 端口被占用 | `lsof -i :8782` 并停止占用进程 |

详见 [LOCAL_DEPLOYMENT_TEST_GUIDE.md 第 6 章](./local-deployment-test-guide.md#6-故障排查)

---

## 📝 验证报告示例

验证脚本会生成如下报告：

```markdown
# LLM Gateway 本地部署验证报告

**生成时间**: 2026-09-06 14:30:00 UTC+8
**目标**: http://127.0.0.1:8782
**模式**: Full (L1-L4)

---

## L1: 基础健康检查
✓ /healthz: 返回 ok
✓ /readyz: 返回 ready
✓ /version: 返回版本信息

## L2: 依赖连通性
✓ PostgreSQL: ok
✓ Redis: ok
✓ PostgreSQL 容器运行中
✓ Redis 容器运行中

## L3: 功能链路验证
✓ Admin API 端点可达
✓ /v1/models 端点可达
✓ 前端静态文件可访问

## L4: 业务场景验证
✓ 日志文件存在
✓ 日志文件活跃
✓ 日志无严重错误
✓ 网关进程运行中
✓ 数据库包含 25 个表

## 验证结果统计
| 项目 | 数量 |
|------|------|
| 总检查项 | 15 |
| ✓ 通过 | 15 |
| ✗ 失败 | 0 |
| ⚠ 跳过 | 0 |
| 成功率 | 100% |

## 🎉 验证通过
所有检查项均通过，网关部署正常！
```

---

## 🎯 最佳实践

### 部署前
1. ✅ 检查系统环境（Go, Docker, 端口）
2. ✅ 配置 `.env.local`（SECRET_KEY, ENCRYPTION_KEY）
3. ✅ 确保磁盘空间充足（至少 5GB）

### 部署后
1. ✅ 运行自动验证脚本
2. ✅ 检查日志无错误
3. ✅ 验证数据库迁移成功
4. ✅ 测试核心 API 端点

### 持续监控
1. ✅ 定期检查健康端点
2. ✅ 监控日志中的错误
3. ✅ 检查资源使用（CPU/内存）
4. ✅ 验证数据库连接池状态

---

## 📞 支持

遇到问题？

1. 查看 [故障排查](./local-deployment-test-guide.md#6-故障排查)
2. 检查日志：`tail -f ~/kaixuan/llm-gateway-go/logs/gateway-8782.log`
3. 查看验证报告：`ls -lt /tmp/llm-gateway-verify-report-*.md | head -1`
4. 提交 Issue 到项目仓库

---

**最后更新**: 2026-09-06  
**维护者**: LLM Gateway Team

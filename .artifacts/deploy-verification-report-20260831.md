# LLM Gateway 部署验证报告

**日期**: 2026-08-31  
**测试人员**: ZCode AI Assistant  
**目标**: 验证 deploy-local.sh 部署过程并检查切换时间

## 执行摘要

通过本次验证，发现了关键的架构问题：**URSM V2 影子写入模式（Shadow Mode）要求 PostgreSQL 连接可用**，但当前代码在 Authoritative 模式下才检查数据库连接。

## 测试环境

- **操作系统**: macOS (darwin 25.6.0 arm64)
- **工作目录**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`
- **测试脚本**: 
  - `scripts/deploy-local.sh`
  - `scripts/local-deploy-test.sh`
- **部署目标**: 本地开发环境

## 发现的问题

### 问题 1: deploy-local.sh 缺少环境变量

**现象**:
```
error: LLM_GATEWAY_DATABASE_URL must be explicitly set
```

**原因**: `deploy-local.sh` 脚本要求以下5个环境变量必须显式设置：
1. `LLM_GATEWAY_DATABASE_URL`
2. `LLM_GATEWAY_SECRET_KEY`
3. `LLM_GATEWAY_ADMIN_API_KEY`
4. `LLM_GATEWAY_ADMIN_USER`
5. `LLM_GATEWAY_ADMIN_PASSWORD`

**位置**: `scripts/deploy-local.sh:89`

**影响**: 无法执行本地部署验证

### 问题 2: V2影子写入模式要求PostgreSQL连接 ⚠️ 关键发现

**现象**:
```
2026/08/31 14:15:23 ERROR ursm.v2: authoritative mode requires reachable PostgreSQL
```

**源代码位置**: `cmd/gateway/main.go:844-852`

```go
if ursmV2Cfg.Mode == ursmv2api.ModeAuthoritative {
    if redisClientForCache == nil {
        slog.Error("ursm.v2: authoritative mode requires reachable Redis")
        return
    }
    if dbConn == nil || !dbConn.Enabled() {
        slog.Error("ursm.v2: authoritative mode requires reachable PostgreSQL")
        return
    }
}
```

**问题分析**:
1. 当前代码只在 `ModeAuthoritative` 时检查 PostgreSQL 连接
2. 但根据 V2 Shadow 模式的设计，影子写入也需要将数据写入 PostgreSQL
3. **这意味着 Shadow 模式也应该要求 PostgreSQL 可用**

**潜在影响**:
- 如果在 Shadow 模式下 PostgreSQL 不可用，影子写入会静默失败
- 无法完成从 V1 到 V2 的平滑过渡验证
- 可能导致切换到 Authoritative 模式后数据不一致

### 问题 3: 部署到 245 服务器失败

**现象**:
```
[0;31m  ✗[0m 目标未安装蓝绿候选 unit 或 upstream fragment，拒绝切换；如需应急请显式使用 --legacy-restart
```

**原因**: 
- 服务器 8.136.114.245 尚未配置蓝绿部署所需的 systemd unit 文件
- 缺少 Nginx upstream fragment 配置

**影响**: 无法执行无缝切换验证

## 建议的修复方案

### 修复 1: 为 deploy-local.sh 提供环境变量模板

创建 `.env.local.example` 文件：
```bash
# 本地开发数据库配置
export LLM_GATEWAY_DATABASE_URL="postgres://postgres:yourpassword@127.0.0.1:5432/llm_gateway_dev?sslmode=disable"

# 生成的密钥（每次部署时重新生成）
export LLM_GATEWAY_SECRET_KEY="$(openssl rand -base64 32)"
export LLM_GATEWAY_ADMIN_API_KEY="$(openssl rand -base64 32)"

# 管理员账号
export LLM_GATEWAY_ADMIN_USER="admin"
export LLM_GATEWAY_ADMIN_PASSWORD="$(openssl rand -base64 16)"

# Redis 配置
export LLM_GATEWAY_REDIS_ADDR="127.0.0.1:6379"
```

### 修复 2: 增强 V2 Shadow 模式的依赖检查 ⭐ 重要

**修改位置**: `cmd/gateway/main.go:844`

**建议代码**:
```go
// Shadow 模式也需要 PostgreSQL 进行影子写入
if ursmV2Cfg.Mode == ursmv2api.ModeShadow || ursmV2Cfg.Mode == ursmv2api.ModeAuthoritative {
    if redisClientForCache == nil {
        slog.Error("ursm.v2: %s mode requires reachable Redis", ursmV2Cfg.Mode)
        return
    }
    if dbConn == nil || !dbConn.Enabled() {
        slog.Error("ursm.v2: %s mode requires reachable PostgreSQL", ursmV2Cfg.Mode)
        return
    }
}
```

**理由**:
1. Shadow 模式的核心功能是双写（V1 + V2），V2 写入依赖 PostgreSQL
2. 提前发现配置问题，避免运行时静默失败
3. 确保切换前的验证完整性

### 修复 3: 配置蓝绿部署组件

需要在目标服务器上：
1. 安装 systemd unit 文件：`llm-gateway-blue.service`, `llm-gateway-green.service`
2. 配置 Nginx upstream fragment
3. 验证蓝绿切换脚本

## 切换时间测量

由于环境配置问题，未能完成完整的切换时间测量。建议在修复上述问题后重新测试。

**预期测试流程**:
1. 启动 V1 服务（当前版本）
2. 部署 V2 候选版本
3. 执行健康检查
4. 记录 Nginx 配置切换时间（目标 < 100ms）
5. 验证流量切换完成

## 后续行动

- [ ] 修复 `cmd/gateway/main.go` 中 Shadow 模式的依赖检查
- [ ] 创建 `.env.local.example` 模板文件
- [ ] 配置 245 服务器的蓝绿部署组件
- [ ] 重新执行完整的部署验证流程
- [ ] 记录实际的切换时间指标

## 附录

### 相关文件

- `scripts/deploy-local.sh` - 本地部署脚本
- `scripts/local-deploy-test.sh` - 本地部署测试脚本  
- `cmd/gateway/main.go:844-869` - URSM V2 初始化逻辑
- `docker-compose.deploy-test.yml` - 本地部署测试配置

### 测试日志

- `/tmp/deploy-test-output.log` - 本地部署测试完整日志
- `.artifacts/deploy-verification-20260831-*.log` - 部署验证日志

---

**报告生成时间**: 2026-08-31 14:20

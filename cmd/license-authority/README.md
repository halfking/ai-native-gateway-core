# License Authority Server

License Authority 是许可证授权中心服务，负责许可证管理、实例管理和自动更新管理。

## 架构

- **框架**: Echo v4 (与 cmd/gateway 保持一致)
- **配置**: 原生 `flag` 包 + 环境变量
- **数据库**: PostgreSQL (通过 pgxpool)
- **API 路由**: `/api/v1/*` (区别于 gateway 的 `/api/admin/*`)

## 配置

通过环境变量或命令行参数配置：

| 环境变量 | Flag | 默认值 | 说明 |
|---------|------|--------|------|
| `LICENSE_AUTHORITY_LISTEN` | `-listen` | `:8443` | HTTP 监听地址 |
| `LICENSE_AUTHORITY_DATABASE_URL` | `-db` | (必填) | PostgreSQL 连接字符串 |
| `LICENSE_AUTHORITY_LOG_LEVEL` | `-log-level` | `info` | 日志级别 (debug/info/warn/error) |

## 启动

### 本地开发

```bash
# 设置数据库连接
export LICENSE_AUTHORITY_DATABASE_URL="postgres://user:pass@localhost:5432/llm_gateway?sslmode=disable"

# 启动服务
go run ./cmd/license-authority

# 或使用编译后的二进制
./license-authority -listen :8443
```

### 使用环境变量

```bash
LICENSE_AUTHORITY_LISTEN=:9443 \
LICENSE_AUTHORITY_DATABASE_URL="postgres://..." \
LICENSE_AUTHORITY_LOG_LEVEL=debug \
./license-authority
```

## API 路由

### Health Check

```bash
curl http://localhost:8443/api/v1/healthz
# {"status":"ok"}
```

### 许可证管理 (`/api/v1/license/*`)

- `POST /api/v1/license/licenses` - 创建许可证
- `GET /api/v1/license/licenses` - 列出许可证
- `GET /api/v1/license/licenses/:id` - 获取许可证详情
- `POST /api/v1/license/licenses/:id/revoke` - 吊销许可证
- `PUT /api/v1/license/licenses/:id` - 更新许可证
- `GET /api/v1/license/licenses/:id/devices` - 列出设备
- `POST /api/v1/license/licenses/:id/devices/:hash/deactivate` - 停用设备
- `GET /api/v1/license/licenses/offline-requests` - 离线激活请求列表
- `POST /api/v1/license/licenses/offline-requests/:id/approve` - 批准离线激活
- `POST /api/v1/license/licenses/offline-requests/:id/reject` - 拒绝离线激活

### 实例管理 (`/api/v1/instances/*`)

- `GET /api/v1/instances` - 列出实例
- `GET /api/v1/instances/:id` - 获取实例详情
- `DELETE /api/v1/instances/:id` - 删除实例
- `POST /api/v1/instances/:id/command` - 下发命令
- `GET /api/v1/instances/:id/heartbeats` - 获取心跳历史
- `GET /api/v1/instances/:id/status` - 获取实例状态
- `GET /api/v1/commands/:id` - 获取命令详情
- `GET /api/v1/commands/:id/status` - 获取命令执行状态
- `GET /api/v1/dashboard/stats` - 获取仪表盘统计

### 自动更新管理 (`/api/v1/updates/*`)

- `POST /api/v1/updates` - 创建发布版本
- `GET /api/v1/updates` - 列出发布版本
- `GET /api/v1/updates/:version` - 获取版本详情
- `POST /api/v1/updates/:version/publish` - 发布版本
- `POST /api/v1/updates/:version/unpublish` - 取消发布
- `POST /api/v1/updates/:version/gray` - 创建灰度发布
- `PATCH /api/v1/updates/:version/gray` - 更新灰度阶段
- `GET /api/v1/updates/upgrade-logs` - 获取升级日志
- `POST /api/v1/updates/rollback` - 回滚版本

## 验收测试

```bash
# 1. 构建
go build ./cmd/license-authority

# 2. 启动 (需要有效的数据库连接)
LICENSE_AUTHORITY_DATABASE_URL="postgres://..." ./license-authority &
PID=$!

# 3. 健康检查
curl http://localhost:8443/api/v1/healthz
# 预期: {"status":"ok"}

# 4. 清理
kill $PID
```

## 待实现 (由后续 Agent 完成)

### Agent-B: 密钥管理与中间件
- [ ] 实现 `CryptoConfig` 初始化 (RSA 密钥对加载)
- [ ] 实现 `Activator`, `OfflineManager`, `Validator` 初始化
- [ ] 添加认证中间件 (JWT / API Key)
- [ ] 添加 RBAC 授权中间件
- [ ] 添加请求日志中间件

### Agent-C: 测试覆盖
- [ ] 单元测试 (`main_test.go`, `routes_test.go`)
- [ ] 集成测试 (HTTP API 端到端)
- [ ] 覆盖率要求: ≥ 80%

### Agent-D: 生产就绪
- [ ] 添加 Prometheus metrics
- [ ] 添加 OpenTelemetry tracing
- [ ] 优雅关闭增强 (draining connections)
- [ ] 配置热重载
- [ ] 健康检查增强 (数据库连接检查)

## 设计决策

1. **原生 flag**: 与 `cmd/gateway` 保持一致，避免引入 Cobra 增加依赖
2. **/api/v1 路由**: License Authority 作为独立服务，使用 `/api/v1` 而非 `/api/admin`，语义更清晰
3. **nil 依赖**: 骨架阶段，`CryptoConfig`/`Activator`/`OfflineManager`/`Validator` 暂时传 nil，待 Agent-B 补全
4. **无中间件**: 骨架阶段不添加认证/授权/日志中间件，待 Agent-B 实现
5. **pgxpool 复用**: 直接使用 `db.Open()` 和现有的 `Store` 实现，无需重复封装

## 参考

- `cmd/gateway/main.go` - Echo + flag + pgxpool 模式
- `licensing/admin_api.go` - 许可证 API Handler
- `center/admin_api.go` - 实例管理 API Handler
- `autoupdate/admin_api.go` - 自动更新 API Handler

# E2E 集成测试说明

## 概述

本目录包含 license 模块端到端集成测试，验证完整的 happy-path 流程：

**激活 → 注册 → 心跳 → 升级**

## 测试架构

### 测试环境

使用 Docker Compose 启动隔离的测试环境：

- **PostgreSQL** (端口 5433): 测试数据库
- **Redis** (端口 6380): 缓存服务
- **license-authority** (端口 8443): License 认证服务

### 测试流程

```
┌─────────────┐
│   步骤 1    │ 启动 Docker Compose 服务
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 2    │ 等待服务健康（PostgreSQL, Redis, license-authority）
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 3    │ 生成 Ed25519 密钥对（模拟首次部署）
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 4    │ 激活 license（使用测试 license key）
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 5    │ 注册实例（POST /api/v1/instances/register）
│             │ ✓ 获取 instance_token
│             │ ✓ 获取 refresh_token
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 6    │ 发送心跳 3 次（间隔 2s）
│             │ ✓ POST /api/v1/instances/heartbeat
│             │ ✓ 携带 Authorization: Bearer <instance_token>
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 7    │ 检查升级（GET /api/v1/update/check）
│             │ ✓ 查询可用版本
│             │ ✓ 返回升级建议
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 8    │ 验证数据库状态
│             │ ✓ gateway_instances 表有 1 条记录
│             │ ✓ instance_heartbeats 表有 ≥3 条记录
│             │ ✓ instance_token 和 refresh_token 已存储
└──────┬──────┘
       │
┌──────▼──────┐
│   步骤 9    │ 清理测试环境
│             │ ✓ 删除测试数据
│             │ ✓ 停止 Docker Compose
└─────────────┘
```

## 文件结构

```
tests/e2e/
├── docker-compose.e2e.yml    # 测试环境编排文件
├── setup.sh                  # 环境初始化脚本（执行迁移、插入测试数据）
├── teardown.sh               # 清理脚本（停止容器、删除卷）
├── test_happy_path.sh        # 主流程测试脚本
└── README.md                 # 本文件
```

## 运行测试

### 前置依赖

- Docker & Docker Compose
- PostgreSQL 客户端（psql）
- Redis 客户端（redis-cli）
- curl
- jq（JSON 解析）
- openssl（Ed25519 密钥生成）

### 快速开始

```bash
cd tests/e2e

# 1. 初始化环境（数据库迁移 + 测试数据）
chmod +x setup.sh
./setup.sh

# 2. 运行主流程测试
chmod +x test_happy_path.sh
./test_happy_path.sh

# 3. 清理环境（可选）
chmod +x teardown.sh
./teardown.sh
```

### 环境变量

测试脚本支持以下环境变量（可选）：

```bash
export DB_HOST=localhost        # PostgreSQL 主机
export DB_PORT=5433             # PostgreSQL 端口
export DB_NAME=llm_gateway_test # 数据库名
export DB_USER=test             # 数据库用户
export DB_PASSWORD=test123      # 数据库密码
```

## 测试数据

### 测试 License

`setup.sh` 会自动插入以下测试 license：

| License Key | 类型 | 有效期 | 特性 |
|---|---|---|---|
| `test-trial-license-e2e` | trial | 30 天 | max_instances=3, max_requests_per_day=10000 |
| `test-enterprise-e2e-20260712` | enterprise | 365 天 | max_instances=100, max_requests_per_day=1000000 |

### 测试 Release

| 版本 | Channel | 镜像 | 状态 |
|---|---|---|---|
| v1.14.0 | stable | llm-gateway:v1.14.0 | published |

## 验证点

### 1. 服务健康

- ✅ PostgreSQL 可连接
- ✅ Redis 可连接
- ✅ license-authority `/healthz` 返回 200

### 2. 注册流程

- ✅ POST `/api/v1/instances/register` 返回 200
- ✅ 响应包含 `instance_token`（JWT）
- ✅ 响应包含 `refresh_token`

### 3. 心跳流程

- ✅ POST `/api/v1/instances/heartbeat` 返回 200
- ✅ 携带 `Authorization: Bearer <instance_token>`
- ✅ 心跳数据写入 `instance_heartbeats` 表

### 4. 升级检查

- ✅ GET `/api/v1/update/check` 返回 200
- ✅ 响应包含 `available` 字段
- ✅ 如有可用版本，返回 `target_version`

### 5. 数据库状态

- ✅ `gateway_instances` 表有 1 条测试实例记录
- ✅ `instance_heartbeats` 表有 ≥3 条心跳记录
- ✅ `instance_token` 和 `refresh_token` 已正确存储

## 故障排查

### PostgreSQL 启动超时

```bash
# 检查容器日志
docker-compose -f docker-compose.e2e.yml logs postgres

# 手动测试连接
psql -h localhost -p 5433 -U test -d llm_gateway_test -c "SELECT 1"
```

### license-authority 启动失败

```bash
# 检查容器日志
docker-compose -f docker-compose.e2e.yml logs license-authority

# 常见问题：
# 1. 数据库迁移失败 → 检查 setup.sh 是否正确执行
# 2. 端口冲突 → 修改 docker-compose.e2e.yml 中的端口映射
```

### 注册失败（401 Unauthorized）

```bash
# 检查测试 license 是否插入
psql -h localhost -p 5433 -U test -d llm_gateway_test \
  -c "SELECT * FROM licenses WHERE license_key = 'test-trial-license-e2e'"

# 重新执行 setup.sh
./setup.sh
```

### 心跳失败（403 Forbidden）

```bash
# 检查 instance_token 是否有效
echo "$INSTANCE_TOKEN" | cut -d'.' -f2 | base64 -d 2>/dev/null | jq

# 检查 instance_token 是否过期
# JWT exp 字段应 > 当前时间戳
```

## 已知限制

1. **不测试实际的 `llm-gw-installer` 二进制**  
   当前测试直接调用 HTTP API，未测试 `llm-gw-installer activate/upgrade` 命令。

2. **不测试镜像下载**  
   当前测试仅验证升级检查接口，未实际下载和部署新版本镜像。

3. **不测试灰度发布**  
   当前测试仅验证全量升级场景，未测试灰度发布规则。

4. **不测试 refresh_token 刷新**  
   当前测试仅验证注册时获取 refresh_token，未测试 token 刷新流程。

## 未来扩展

### 计划添加的测试场景

- [ ] **Token 刷新流程**  
  测试 `POST /api/v1/instances/refresh` 使用 refresh_token 获取新 instance_token。

- [ ] **License 过期处理**  
  插入过期 license，验证注册/心跳被拒绝。

- [ ] **实例离线检测**  
  停止心跳 5 分钟，验证实例状态变为 `offline`。

- [ ] **升级状态上报**  
  模拟实例上报升级进度（downloading → upgrading → success）。

- [ ] **并发注册压测**  
  并发注册 100 个实例，验证数据库性能和唯一性约束。

## 参考文档

- [License Authority API 设计](../../cmd/license-authority/README.md)
- [数据库迁移 376-379](../../sql/migrations/startup/)
- [llm-gw-installer 用户手册](../../installer/README.md)

---

**版本**: v1.0  
**更新日期**: 2026-07-12  
**作者**: Implementer Agent

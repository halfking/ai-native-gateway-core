# llm-gateway-go 常见问题速查手册

本文档记录 llm-gateway-go 项目开发和运维中的常见问题、解决方案和最佳实践。

## 目录

- [版本管理](#版本管理)
- [构建问题](#构建问题)
- [部署问题](#部署问题)
- [流式响应问题](#流式响应问题)
- [数据库问题](#数据库问题)
- [快速脚本](#快速脚本)

---

## 版本管理

### Q1: 如何更新版本号？

**A**: 版本号由 git tag 自动管理，build_seq 自动递增。

```bash
# 1. 创建新的 git tag（根据需要）
git tag r1.14-feature-name
git push origin r1.14-feature-name

# 2. 构建时会自动使用最新 tag 和递增 build_seq
./deploy/build-image.sh

# 3. 查看当前版本
cat VERSION
# 输出示例: r1.13-done-78a5d648-20260701-761
```

### Q2: build_seq 没有自增怎么办？

**A**: 检查权限和手动执行脚本。

```bash
# 检查文件权限
ls -la version.json scripts/bump-version.sh

# 添加执行权限
chmod +x scripts/bump-version.sh

# 手动执行
./scripts/bump-version.sh

# 检查 version.json
cat version.json | jq
```

### Q3: 如何在运行中的容器查看版本？

**A**: 从镜像内的版本文件读取。

```bash
# 在 K8s 中查看
kubectl exec -n pms-test deployment/llm-gateway-go-deployment -- cat __SERVER_PATH_1__/VERSION

# 本地 Docker 镜像
docker run --rm --entrypoint cat kx-llm-gateway-go:tag __SERVER_PATH_1__/VERSION

# SSH 到 184 查看
export SSHPASS='__SSH_PWD_1__'
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl exec -n pms-test deployment/llm-gateway-go-deployment -- cat __SERVER_PATH_1__/VERSION"
```

---

## 构建问题

### Q4: Docker 构建时提示缺少 base 镜像？

**A**: 使用本地 base 镜像，不要从 Docker Hub 拉取。

```bash
# 检查本地是否有 base 镜像
docker images | grep kx-base

# 如果没有，从 184 或 71 服务器拉取
export SSHPASS='__SSH_PWD_1__'
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "docker save kx-base:go-vue-amd64 | gzip" | gunzip | docker load
```

### Q5: 构建很慢，如何加速？

**A**: 使用国内镜像源和缓存。

```bash
# Go 模块代理（已在 Dockerfile 中配置）
export GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct

# npm 镜像（已在 Dockerfile 中配置）
npm config set registry https://registry.npmmirror.com/

# 使用 BuildKit 加速
export DOCKER_BUILDKIT=1
docker build --build-arg BUILDKIT_INLINE_CACHE=1 ...
```

### Q6: 如何只构建 Go 二进制而不构建镜像？

**A**: 直接使用 go build。

```bash
# Linux amd64（用于服务器）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux-amd64 ./cmd/gateway

# 本地测试（macOS arm64）
go build -o llm-gateway-go ./cmd/gateway

# 检查二进制
ls -lh llm-gateway-go*
```

---

## 部署问题

### Q7: 如何快速部署到 184？

**A**: 使用一键部署脚本。

```bash
# 最简单：一键部署
./scripts/quick-deploy-to-184.sh

# 该脚本会自动：
# 1. 构建镜像（包含版本管理）
# 2. 上传到 184
# 3. 加载到 K8s
# 4. 验证部署
```

### Q8: 部署后 Pod 启动失败？

**A**: 查看日志和事件排查。

```bash
export SSHPASS='__SSH_PWD_1__'

# 查看 Pod 状态
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl get pods -n pms-test | grep llm-gateway-go"

# 查看详细事件
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl describe pod <pod-name> -n pms-test"

# 查看日志
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl logs -n pms-test <pod-name> --tail=100"

# 如果是 CrashLoopBackOff，查看之前容器日志
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl logs -n pms-test <pod-name> --previous"
```

### Q9: 如何回滚到之前的版本？

**A**: 使用 kubectl rollout undo。

```bash
export SSHPASS='__SSH_PWD_1__'

# 回滚到上一个版本
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test"

# 查看 rollout 历史
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl rollout history deployment/llm-gateway-go-deployment -n pms-test"

# 回滚到指定版本
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test --to-revision=3"
```

### Q10: 如何重启服务？

**A**: 使用 kubectl rollout restart。

```bash
export SSHPASS='__SSH_PWD_1__'

# 重启 deployment
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl rollout restart deployment/llm-gateway-go-deployment -n pms-test"

# 等待完成
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test"
```

---

## 流式响应问题

### Q11: claude-sonnet-5 流式响应报 "Type validation failed" 错误？

**A**: 这是 `ObservePayload` 缺少 `type` 字段的问题，commit `78a5d648` 已修复。

**验证修复**:
```bash
# 检查代码是否包含修复
grep -A 2 "ObservePayload.*response.output_text.delta" domains/streaming/responses_stream.go

# 应该看到：
# capture.ObservePayload(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q,"item_id":%q}`, textDelta, msgID), "", false)

# 如果没有修复，更新代码
git pull origin main
```

**测试流式响应**:
```bash
curl -s -X POST https://__DOMAIN_1__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer __API_KEY_1__" \
  -d '{
    "model": "claude-sonnet-5",
    "messages": [{"role": "user", "content": "ping"}],
    "stream": true
  }' | head -20
```

### Q12: 流式响应在 71 正常但 184 报错？

**A**: 184 有更严格的 schema 验证。确保使用最新代码（包含 commit `78a5d648`）。

```bash
# 检查当前分支
git log --oneline -5

# 应该包含这个提交
# 78a5d648 fix(streaming): add missing 'type' field in ObservePayload for response.output_text.delta

# 如果没有，拉取最新代码
git pull origin main

# 重新构建和部署
./scripts/quick-deploy-to-184.sh
```

---

## 数据库问题

### Q13: 如何连接到 184 的数据库？

**A**: 通过 SSH 隧道连接。

```bash
# 方式1: SSH 隧道
ssh -p __PORT_1__ -L __PORT_6__:127.0.0.1:__PORT_5__ __SSH_TARGET_1__

# 然后在本地连接
psql -h 127.0.0.1 -p __PORT_6__ -U llm_gateway -d crm

# 方式2: 使用 sshpass（脚本中）
export SSHPASS='__SSH_PWD_1__'
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "psql -h 127.0.0.1 -U llm_gateway -d crm -c 'SELECT version();'"
```

### Q14: 数据库密码是什么？

**A**: 数据库账号和密码如下：

```
PostgreSQL 数据库：
- IP: __PUB_IP_1__（需通过 SSH）
- 端口: __PORT_5__
- 数据库: crm

账号列表：
- llm_gateway / __DB_PWD_1__ (主超级用户)
- crm_user / crm_pass123 (CRM)
- __USER_2___user / __USER_2___pass123 (开轩主应用)
- doc_tools_user / doc_tools_pass123 (doc-tools)
- casdoor_user / casdoor_pass123 (Casdoor)
- kxuser / kxuser123 (列存表owner)
```

---

## 快速脚本

### Q15: 有哪些可用的快速脚本？

**A**: 项目提供以下快速脚本：

#### 1. 版本管理
```bash
# 更新版本信息
./scripts/bump-version.sh
```

#### 2. 构建镜像
```bash
# 构建镜像（自动更新版本）
./deploy/build-image.sh

# 构建并推送
./deploy/build-image.sh --push

# 使用自定义 tag
./deploy/build-image.sh --tag my-tag
```

#### 3. 快速部署到 184
```bash
# 一键部署（构建+上传+部署+验证）
./scripts/quick-deploy-to-184.sh
```

#### 4. 查看服务状态
```bash
# 创建一个快速脚本
cat > scripts/check-184-status.sh <<'EOF'
#!/bin/bash
export SSHPASS='__SSH_PWD_1__'
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ "
  echo '=== Pods Status ==='
  kubectl get pods -n pms-test | grep llm-gateway
  echo ''
  echo '=== Current Image ==='
  kubectl get deployment/llm-gateway-go-deployment -n pms-test -o jsonpath='{.spec.template.spec.containers[0].image}'
  echo ''
  echo ''
  echo '=== Recent Logs ==='
  kubectl logs -n pms-test deployment/llm-gateway-go-deployment --tail=20
"
EOF
chmod +x scripts/check-184-status.sh

# 使用
./scripts/check-184-status.sh
```

#### 5. 测试 API
```bash
# 创建测试脚本
cat > scripts/test-api.sh <<'EOF'
#!/bin/bash
API_KEY="${1:-__API_KEY_1__}"

echo "Testing __DOMAIN_1__..."
curl -s -X POST https://__DOMAIN_1__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{
    "model": "claude-sonnet-5",
    "messages": [{"role": "user", "content": "ping"}],
    "stream": true
  }' | head -15
EOF
chmod +x scripts/test-api.sh

# 使用
./scripts/test-api.sh
```

---

## 服务器信息速查

### 火山云服务器

| 服务器 | IP | 内网IP | 用途 | 密码 |
|--------|-----|--------|------|------|
| 56 | __PUB_IP_3__ | __PRIV_IP_1__ | 网关 (nps+nginx) | root/__SSH_PWD_1__ |
| 71 | __PUB_IP_2__ | __PRIV_IP_2__ | infra (docker+llm-gateway老版本) | root/__SSH_PWD_1__ |
| 184 | __PUB_IP_1__ | __PRIV_IP_3__ | 核心应用 (k8s+llm-gateway-go) | root/__SSH_PWD_1__ |

### 阿里云服务器

| 服务器 | 公网IP | 内网IP | 用途 | 密码 |
|--------|--------|--------|------|------|
| 252 | __PUB_IP_5__ | __PRIV_IP_4__ | __DOMAIN_3__+nps+vpn | root/__SSH_PWD_1__ |
| 245 | __PUB_IP_6__ | __PRIV_IP_5__ | 网关 | root/__SSH_PWD_1__ |
| 154 | __PUB_IP_7__ | __PRIV_IP_6__ | 旧网关 | root/__SSH_PWD_1__ |
| 186 | __PUB_IP_8__ | - | 应用服务器 | root/__SSH_PWD_1__ |

### SSH 连接

```bash
# 标准方式
ssh -p __PORT_1__ __SSH_TARGET_1__

# 使用 sshpass（脚本中）
export SSHPASS='__SSH_PWD_1__'
sshpass -e ssh -p __PORT_1__ -o StrictHostKeyChecking=no __SSH_TARGET_1__
```

---

## 常用 API 和服务

### LLM Gateway 服务

- **184 (主服务)**: https://__DOMAIN_1__
- **71 (老版本)**: https://__DOMAIN_2__
- **252**: https://__DOMAIN_3__

### 测试 API Key

```
__API_KEY_1__
```

### Registry 和 Nexus

```bash
# Registry
__DOMAIN_4__
用户: __USER_2__ / __ADMIN_PWD_1__

# Nexus
__DOMAIN_5__
用户: admin / __ADMIN_PWD_1__
NuGet API Key: __API_KEY_7__
```

---

## 最佳实践

### 1. 代码提交前检查

```bash
# 运行 pre-commit hooks
git commit -m "your message"

# 应该看到：
# pre-commit checks for llm-gateway-go
# ===================================
#   [go vet] PASS
#   [SQL: no SET+placeholder] PASS
#   [Migration: unique NNN] PASS
#   [Vue: vue-tsc] PASS
# ===================================
```

### 2. 部署前测试

```bash
# 1. 本地构建测试
./deploy/build-image.sh

# 2. 验证镜像版本
VERSION=$(cat VERSION)
docker run --rm --entrypoint cat kx-llm-gateway-go:${VERSION} __SERVER_PATH_1__/VERSION

# 3. 本地运行测试（需要配置环境变量）
docker run -p __PORT_3__:__PORT_3__ \
  -e LLM_GATEWAY_ENV=development \
  -e LLM_GATEWAY_CORS_ORIGINS="*" \
  kx-llm-gateway-go:${VERSION}
```

### 3. 部署到生产环境

```bash
# 1. 确保在正确的分支
git branch
# 应该在 main

# 2. 拉取最新代码
git pull origin main

# 3. 一键部署
./scripts/quick-deploy-to-184.sh

# 4. 验证
./scripts/test-api.sh
```

### 4. 监控和日志

```bash
# 实时查看日志
export SSHPASS='__SSH_PWD_1__'
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl logs -n pms-test deployment/llm-gateway-go-deployment -f"

# 查看最近错误
sshpass -e ssh -p __PORT_1__ __SSH_TARGET_1__ \
  "kubectl logs -n pms-test deployment/llm-gateway-go-deployment --tail=100 | grep -i error"
```

---

## 相关文档

- [构建与部署指南](BUILD_AND_DEPLOY_GUIDE.md) - 详细的构建和部署流程
- [ARCHITECTURE.md](architecture/ARCHITECTURE.md) - 系统架构文档
- [README.md](../README.md) - 项目总体说明

## 更新历史

- **2026-07-01**: 初始版本
  - 记录版本管理、构建、部署常见问题
  - 添加快速脚本和服务器信息
  - 记录流式响应 schema 验证问题的解决方案

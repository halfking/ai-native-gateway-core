# llm-gateway-go 构建与部署指南

本文档记录 llm-gateway-go 项目的构建、版本管理和部署流程。

## 目录

- [版本管理系统](#版本管理系统)
- [构建流程](#构建流程)
- [部署流程](#部署流程)
- [快速参考](#快速参考)
- [故障排查](#故障排查)

---

## 版本管理系统

### 概述

项目使用基于 git tag 的自动化版本管理系统，特性：
- ✅ 自动从 git 获取最近的 tag 作为版本号
- ✅ 自动递增 build_seq 编译次数
- ✅ 版本信息注入到 Docker 镜像
- ✅ 运行时可访问版本信息

### 版本格式

```
${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${BUILD_SEQ}
```

示例: `r1.13-done-78a5d648-20260701-761`

### 版本文件

- `version.json`: 存储版本元数据和 build_seq
- `VERSION`: 存储完整版本字符串（已纳入版本控制）

### 核心脚本

#### `scripts/bump-version.sh`

版本管理核心脚本，功能：
1. 从 git 获取最近的 tag
2. 读取 `version.json` 中的 `build_seq` 并自增
3. 更新 `version.json` 和 `VERSION` 文件
4. 导出环境变量供构建使用

**使用方式**:
```bash
# 手动更新版本（通常不需要，构建脚本会自动调用）
./scripts/bump-version.sh
```

**导出的环境变量**:
- `BUILD_VERSION`: git tag 版本号
- `BUILD_GIT_TAG`: git tag
- `BUILD_GIT_SHA`: 当前 commit 短 hash
- `BUILD_DATE`: 构建日期 (YYYYMMDD)
- `BUILD_SEQ`: 编译序号
- `VERSION_STRING`: 完整版本字符串

---

## 构建流程

### 1. 本地构建 Docker 镜像

#### `deploy/build-image.sh`

集成版本管理的镜像构建脚本。

**功能**:
- 自动调用 `bump-version.sh` 更新版本
- 将版本信息注入 Docker 构建
- 支持自定义 tag 和推送到 registry

**使用方式**:

```bash
# 使用自动生成的版本 tag 构建
./deploy/build-image.sh

# 使用自定义 tag
./deploy/build-image.sh --tag my-custom-tag

# 构建并推送到 registry
./deploy/build-image.sh --push

# 组合使用
./deploy/build-image.sh --tag v1.0.0 --push
```

**输出**:
- 镜像 tag: `${VERSION_STRING}` (例如: `r1.13-done-78a5d648-20260701-761`)
- 额外 tag: `latest`

### 2. 手动构建 Go 二进制

```bash
# 构建 Linux amd64 二进制
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux-amd64 ./cmd/gateway

# 构建 macOS arm64 二进制（本地测试）
go build -o llm-gateway-go ./cmd/gateway
```

### 3. 版本信息在镜像中的位置

Docker 镜像内可从以下位置读取版本信息：
- `__SERVER_PATH_1__/VERSION` - 完整版本字符串
- `__SERVER_PATH_1__/.deploy_seq` - 编译序号
- `/.VERSION` - 完整版本字符串（根目录）
- `/.deploy_seq` - 编译序号（根目录）

验证镜像版本：
```bash
docker run --rm --entrypoint cat <image>:tag __SERVER_PATH_1__/VERSION
```

---

## 部署流程

### 部署架构

- **56 (网关)**: __PUB_IP_3__ (__PRIV_IP_1__) - nps + nginx
- **71 (infra)**: __PUB_IP_2__ (__PRIV_IP_2__) - docker + llm-gateway (老版本)
- **184 (核心应用)**: __PUB_IP_1__ (__PRIV_IP_3__) - k8s + llm-gateway-go

### 部署到 184 服务器（K8s）

#### 步骤 1: 构建镜像

```bash
# 在本地构建镜像（会自动更新版本）
./deploy/build-image.sh
```

#### 步骤 2: 导出并上传镜像

```bash
# 导出镜像（替换 VERSION_STRING 为实际版本）
docker save kx-llm-gateway-go:${VERSION_STRING} | gzip > /tmp/kx-llm-gateway-go-${VERSION_STRING}.tar.gz

# 上传到 184 服务器
export SSHPASS='__SSH_PWD_1__'
sshpass -e scp -P __PORT_1__ -o StrictHostKeyChecking=no \
  /tmp/kx-llm-gateway-go-${VERSION_STRING}.tar.gz \
  __SSH_TARGET_1__:/tmp/
```

#### 步骤 3: 在 184 服务器上加载镜像

```bash
# SSH 到 184
export SSHPASS='__SSH_PWD_1__'
sshpass -e ssh -p __PORT_1__ -o StrictHostKeyChecking=no __SSH_TARGET_1__

# 加载镜像
docker load < /tmp/kx-llm-gateway-go-${VERSION_STRING}.tar.gz

# 推送到本地 registry
docker tag kx-llm-gateway-go:${VERSION_STRING} 127.0.0.1:__PORT_8__/kx-llm-gateway-go:${VERSION_STRING}
docker push 127.0.0.1:__PORT_8__/kx-llm-gateway-go:${VERSION_STRING}
```

#### 步骤 4: 更新 K8s Deployment

```bash
# 在 184 服务器上执行
kubectl set image deployment/llm-gateway-go-deployment \
  llm-gateway-go=127.0.0.1:__PORT_8__/kx-llm-gateway-go:${VERSION_STRING} \
  -n pms-test

# 等待 rollout 完成
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=120s

# 检查 pod 状态
kubectl get pods -n pms-test | grep llm-gateway-go

# 查看日志
kubectl logs -n pms-test deployment/llm-gateway-go-deployment --tail=50
```

#### 步骤 5: 验证部署

```bash
# 测试流式响应（从本地）
curl -s -X POST https://__DOMAIN_1__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer __API_KEY_1__" \
  -d '{
    "model": "claude-sonnet-5",
    "messages": [{"role": "user", "content": "ping"}],
    "stream": true
  }' | head -20

# 检查服务健康
curl -s https://__DOMAIN_1__/healthz

# 在 184 上检查版本
kubectl exec -n pms-test deployment/llm-gateway-go-deployment -- cat __SERVER_PATH_1__/VERSION
```

---

## 快速参考

### 完整部署流程（一键脚本）

```bash
#!/bin/bash
# 快速部署到 184 脚本示例

set -euo pipefail

# 1. 构建镜像（自动更新版本）
./deploy/build-image.sh

# 2. 获取版本字符串
VERSION=$(cat VERSION)
echo "部署版本: ${VERSION}"

# 3. 导出镜像
docker save kx-llm-gateway-go:${VERSION} | gzip > /tmp/kx-llm-gateway-go-${VERSION}.tar.gz

# 4. 上传并部署
export SSHPASS='__SSH_PWD_1__'
sshpass -e scp -P __PORT_1__ -o StrictHostKeyChecking=no \
  /tmp/kx-llm-gateway-go-${VERSION}.tar.gz __SSH_TARGET_1__:/tmp/

sshpass -e ssh -p __PORT_1__ -o StrictHostKeyChecking=no __SSH_TARGET_1__ <<EOF
  docker load < /tmp/kx-llm-gateway-go-${VERSION}.tar.gz
  docker tag kx-llm-gateway-go:${VERSION} 127.0.0.1:__PORT_8__/kx-llm-gateway-go:${VERSION}
  docker push 127.0.0.1:__PORT_8__/kx-llm-gateway-go:${VERSION}
  kubectl set image deployment/llm-gateway-go-deployment \
    llm-gateway-go=127.0.0.1:__PORT_8__/kx-llm-gateway-go:${VERSION} -n pms-test
  kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=120s
EOF

echo "✅ 部署完成: ${VERSION}"
```

### 常用命令

```bash
# 查看当前版本
cat VERSION

# 查看版本详情
cat version.json | jq

# 查看 git tag
git describe --tags --abbrev=0

# 查看最近的构建
git log --oneline -5

# 回滚 K8s deployment
kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test

# 查看 rollout 历史
kubectl rollout history deployment/llm-gateway-go-deployment -n pms-test
```

---

## 故障排查

### 问题 1: version.json 中的 build_seq 没有自增

**原因**: 可能是 `bump-version.sh` 没有执行或没有写权限

**解决**:
```bash
# 检查文件权限
ls -la version.json scripts/bump-version.sh

# 添加执行权限
chmod +x scripts/bump-version.sh

# 手动执行
./scripts/bump-version.sh
```

### 问题 2: Docker 构建失败，提示缺少版本参数

**原因**: `deploy/build-image.sh` 没有正确调用 `bump-version.sh`

**解决**:
```bash
# 确保脚本有执行权限
chmod +x deploy/build-image.sh scripts/bump-version.sh

# 检查脚本中的 source 语句
grep "source.*bump-version.sh" deploy/build-image.sh

# 手动设置环境变量后构建
source scripts/bump-version.sh
docker build \
  --build-arg GIT_TAG="${BUILD_GIT_TAG}" \
  --build-arg GIT_SHA="${BUILD_GIT_SHA}" \
  --build-arg BUILD_DATE="${BUILD_DATE}" \
  --build-arg BUILD_SEQ="${BUILD_SEQ}" \
  -t kx-llm-gateway-go:${VERSION_STRING} \
  -f Dockerfile .
```

### 问题 3: K8s pod 启动失败

**排查步骤**:
```bash
# 查看 pod 状态
kubectl get pods -n pms-test | grep llm-gateway-go

# 查看详细事件
kubectl describe pod <pod-name> -n pms-test

# 查看日志
kubectl logs -n pms-test <pod-name> --tail=100

# 查看之前容器的日志（如果是 CrashLoopBackOff）
kubectl logs -n pms-test <pod-name> --previous
```

### 问题 4: 镜像版本信息不正确

**验证步骤**:
```bash
# 检查镜像内的版本文件
docker run --rm --entrypoint cat kx-llm-gateway-go:${VERSION_STRING} __SERVER_PATH_1__/VERSION
docker run --rm --entrypoint cat kx-llm-gateway-go:${VERSION_STRING} __SERVER_PATH_1__/.deploy_seq

# 检查镜像构建参数
docker image inspect kx-llm-gateway-go:${VERSION_STRING} | jq '.[0].Config.Labels'
```

### 问题 5: 流式响应验证失败（Type validation failed）

**已知问题**: `capture.ObservePayload()` 缺少 `type` 字段

**修复**: commit `78a5d648` 已修复此问题

**验证**:
```bash
# 检查代码中的 ObservePayload 调用
grep -rn "ObservePayload" domains/streaming/responses_stream.go

# 应该看到包含 type 字段的格式：
# capture.ObservePayload(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q,"item_id":%q}`, textDelta, msgID), "", false)
```

---

## 相关文件

- `scripts/bump-version.sh` - 版本管理脚本
- `deploy/build-image.sh` - 镜像构建脚本
- `version.json` - 版本元数据
- `VERSION` - 完整版本字符串
- `Dockerfile` - 多阶段构建配置
- `.gitignore` - Git 忽略规则

## 更新历史

- **2026-07-01**: 初始版本，记录版本管理系统和部署流程
  - 实现 `scripts/bump-version.sh`
  - 更新 `deploy/build-image.sh` 集成版本管理
  - 修复流式响应 schema 验证问题 (commit 78a5d648)
  - 添加版本管理系统 (commit 985575d0)

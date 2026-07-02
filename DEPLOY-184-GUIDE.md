# 184环境部署指南

## 概述

本文档描述了将 `llm-gateway-go` 服务部署到184测试环境的标准化流程。

## 环境信息

- **服务器地址**: 14.103.112.184:25022
- **Kubernetes命名空间**: pms-test
- **服务名称**: llm-gateway-go-deployment
- **健康检查端点**: http://localhost:30080/health
- **镜像仓库**: 
  - 内部: registry.kxpms.cn
  - 本地: 127.0.0.1:5000

## 部署脚本

使用 `deploy-184.sh` 脚本进行标准化部署：

```bash
./deploy-184.sh
```

## 部署流程

### 1. 版本信息获取
- 从git仓库获取最新tag：`git describe --tags --abbrev=0`
- 获取当前commit SHA：`git rev-parse --short=8 HEAD`
- 读取构建序列号：`cat build_seq`
- 生成构建日期：`date +%Y%m%d`

### 2. 更新构建序列号
执行 `bump-version.sh` 脚本自动递增构建序列号。

### 3. 生成版本信息文件
生成 `version.json` 文件，包含：
- version: 完整版本字符串
- git_tag: Git标签
- git_sha: Git提交哈希
- build_seq: 构建序列号
- build_date: 构建日期
- module: 模块名称

### 4. 构建Docker镜像
```bash
docker build \
  --build-arg GIT_TAG=${GIT_TAG} \
  --build-arg GIT_SHA=${GIT_SHA} \
  --build-arg BUILD_SEQ=${BUILD_SEQ} \
  --build-arg BUILD_DATE=${BUILD_DATE} \
  -t kx-llm-gateway-go:${IMAGE_TAG} .
```

### 5. 推送镜像
```bash
# 推送到内部仓库
docker tag kx-llm-gateway-go:${IMAGE_TAG} registry.kxpms.cn/kx-llm-gateway-go:${IMAGE_TAG}
docker push registry.kxpms.cn/kx-llm-gateway-go:${IMAGE_TAG}

# 推送到本地仓库
docker tag kx-llm-gateway-go:${IMAGE_TAG} 127.0.0.1:5000/kx-llm-gateway-go:${IMAGE_TAG}
docker push 127.0.0.1:5000/kx-llm-gateway-go:${IMAGE_TAG}
```

### 6. 部署到Kubernetes
```bash
ssh -p 25022 root@14.103.112.184 "kubectl set image deployment/llm-gateway-go-deployment \
  llm-gateway-go=127.0.0.1:5000/kx-llm-gateway-go:${IMAGE_TAG} -n pms-test && \
  kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=300s"
```

### 7. 健康检查与版本验证
```bash
# 健康检查
ssh -p 25022 root@14.103.112.184 "curl -s http://localhost:30080/health | jq ."

# 版本验证
ssh -p 25022 root@14.103.112.184 "kubectl get pods -n pms-test -l app=llm-gateway-go \
  -o jsonpath='{.items[0].spec.containers[0].image}'"
```

### 8. 生成部署报告
部署报告自动生成并保存到：`/tmp/deployment-report-YYYYMMDD-HHMMSS.md`

### 9. 镜像清理
脚本会在远程184服务器上查找过期镜像（超过30天），并将其移动到 `/opt/ready-to-delete/` 目录。

## 版本号规则

版本号格式：`{GIT_TAG}-{GIT_SHA}-{BUILD_DATE}-{BUILD_SEQ}`

示例：`r1.13-done-4f05275c-20260702-769`

- **GIT_TAG**: 从git仓库最近的tag获取
- **GIT_SHA**: 当前commit的8位短哈希
- **BUILD_SEQ**: 构建序列号（递增）
- **BUILD_DATE**: 构建日期（YYYYMMDD格式）

## 前置条件

1. **Git仓库状态**
   - 所有更改已提交
   - 代码已推送到远程仓库
   - 确认当前分支为 `main`

2. **Docker环境**
   - Docker daemon正在运行
   - 有权限推送到镜像仓库
   - 本地磁盘空间充足

3. **SSH访问**
   - 已配置184服务器SSH免密登录
   - 端口：25022
   - 用户：root

4. **Kubernetes访问**
   - 184服务器上已配置kubectl
   - 有权限操作 `pms-test` 命名空间

## 故障排查

### 镜像构建失败
```bash
# 检查Docker daemon状态
docker info

# 清理构建缓存
docker builder prune
```

### 镜像推送失败
```bash
# 检查registry连通性
ping registry.kxpms.cn

# 检查Docker登录状态
docker login registry.kxpms.cn
```

### 部署更新失败
```bash
# 检查pod状态
ssh -p 25022 root@14.103.112.184 "kubectl get pods -n pms-test"

# 查看pod日志
ssh -p 25022 root@14.103.112.184 "kubectl logs -n pms-test -l app=llm-gateway-go --tail=100"

# 查看deployment事件
ssh -p 25022 root@14.103.112.184 "kubectl describe deployment llm-gateway-go-deployment -n pms-test"
```

### 健康检查失败
```bash
# 检查服务端口
ssh -p 25022 root@14.103.112.184 "netstat -tuln | grep 30080"

# 手动进入容器检查
ssh -p 25022 root@14.103.112.184 "kubectl exec -it -n pms-test \$(kubectl get pods -n pms-test -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}') -- /bin/sh"
```

## 回滚操作

如果部署出现问题，可以快速回滚到上一个版本：

```bash
# 查看部署历史
ssh -p 25022 root@14.103.112.184 "kubectl rollout history deployment/llm-gateway-go-deployment -n pms-test"

# 回滚到上一个版本
ssh -p 25022 root@14.103.112.184 "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test"

# 回滚到指定版本
ssh -p 25022 root@14.103.112.184 "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test --to-revision=<revision-number>"
```

## 注意事项

1. **构建序列号管理**：每次部署前确认 `build_seq` 文件已正确更新
2. **Git标签一致性**：确保版本号与git tag保持一致
3. **磁盘空间监控**：定期检查184服务器和本地磁盘空间
4. **部署报告归档**：部署报告保存在 `/tmp` 目录，建议定期归档
5. **过期镜像清理**：`/opt/ready-to-delete/` 目录中的镜像可安全删除

## 相关文件

- `deploy-184.sh`: 部署脚本
- `bump-version.sh`: 版本递增脚本
- `build_seq`: 构建序列号文件
- `version.json`: 版本信息文件（构建时生成）
- `Dockerfile`: Docker镜像构建文件

## 维护记录

| 日期 | 版本 | 修改内容 | 修改人 |
|------|------|----------|--------|
| 2026-07-02 | 1.0 | 初始版本，标准化部署流程 | opencode-agent |
| 2026-07-02 | 1.1 | 修复版本号获取逻辑，从git tag读取 | opencode-agent |

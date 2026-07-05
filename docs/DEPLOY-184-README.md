# 184环境部署指南

## 概述

`deploy-184.sh` 是标准化的184环境部署脚本（统一入口），合并了原 `scripts/deploy-to-184-with-migration.sh`、`scripts/deploy-to-184-after-local-test.sh`、`scripts/deploy-columnar-184.sh`。

## 部署模式

| 模式 | 命令 | 说明 |
|---|---|---|
| 标准 | `./deploy-184.sh` | 10 步部署（check → build → push → K8s → health → report） |
| 含 migration | `./deploy-184.sh --with-migration` 或 `-m` | 部署后自动 DB migration + 重启 Pod + build_seq 提交 |
| 端到端 | `./deploy-184.sh --after-local-test` 或 `-l` | 先本地验证，再部署含 migration |
| Columnar | `./deploy-184.sh --columnar` 或 `-c` | 增量二进制构建部署 |

环境变量:
- `SKIP_LOCAL_TEST=1` 跳过本地测试（仅 `--after-local-test` 模式）
- `SKIP_BUILD_SEQ_COMMIT=1` 跳过 build_seq 提交

## 核心特性

- ✅ **多种部署模式**: 支持标准 / 含 migration / 端到端 / columnar 4 种模式
- ✅ **版本管理**: 从git tag自动获取版本号，确保版本号的准确性和可追溯性
- ✅ **编译次数追踪**: 自动递增 `build_seq`，每次部署都有唯一的构建序号
- ✅ **Git集成**: 检查未提交改动，可选择提交或跳过
- ✅ **多Registry推送**: 自动推送到内部registry和184本地registry
- ✅ **自动化部署**: 更新K8s deployment并等待rolling update完成
- ✅ **健康检查**: 验证服务状态、Pod状态和版本信息
- ✅ **镜像清理**: 自动清理过期镜像，保持存储空间整洁
- ✅ **部署报告**: 生成详细的部署报告，记录所有关键信息

## 前置要求

### 1. 环境配置

```bash
# SSH访问权限
ssh -p __PORT_1__ __SSH_TARGET_1__

# Docker环境
docker version

# kubectl访问权限
kubectl get pods -n pms-test

# 必要工具
jq --version
```

### 2. 文件结构

```
llm-gateway-go/
├── deploy-184.sh           # 部署脚本（统一入口，4 种模式）
├── Dockerfile              # Docker构建文件
├── build_seq               # 构建序号文件（自动生成）
├── version.json            # 版本信息（自动生成）
└── deployment-report-*.md  # 部署报告（自动生成）
```

### 3. Git要求

- 仓库需要有 git tag（如 `r1.13-done`）
- 如果没有tag，脚本会使用默认值 `v0.0.0`

## 使用方法

### 基础使用

```bash
# 直接运行脚本
./deploy-184.sh
```

### 脚本执行流程

#### 步骤 1/8: 检查未提交改动
- 检测工作区是否有未提交的改动
- 如有改动，询问是否提交
- 可选择提交或跳过（跳过的改动不会包含在镜像中）

#### 步骤 2/8: 获取版本信息
- 从git获取最近的tag作为版本号
- 获取当前提交的短SHA（8位）
- 生成构建日期（格式：YYYYMMDD）
- 递增构建序号（从 `build_seq` 文件读取）
- 生成完整镜像标签：`${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${BUILD_SEQ}`

**示例输出**:
```
Git Tag:    r1.13-done
Git SHA:    4f05275c
Build Seq:  769 -> 770
Build Date: 20260702
Image Tag:  r1.13-done-4f05275c-20260702-770
```

#### 步骤 3/8: 构建Docker镜像
- 使用 `docker build` 构建镜像
- 传递版本参数给Dockerfile
- 同时打上完整标签和 `latest` 标签

#### 步骤 4/8: 推送镜像
- 推送到内部registry: `__DOMAIN_4__`
- 推送到184本地registry: `127.0.0.1:__PORT_8__`

#### 步骤 5/8: 更新K8s部署
- 使用 `kubectl set image` 更新deployment
- 等待rolling update完成（超时5分钟）

#### 步骤 6/8: 健康检查
- 检查Pod状态
- 调用健康检查端点
- 验证容器内版本信息

#### 步骤 7/8: 清理过期镜像
- 清理Docker dangling镜像
- 删除旧的 `kx-llm-gateway-go` 镜像（保留最近5个）
- 记录删除日志到 `/opt/ready-to-delete/deleted-images-*.log`

#### 步骤 8/8: 生成部署报告
- 生成markdown格式的部署报告
- 包含版本信息、镜像信息、Git提交历史、健康检查结果等
- 报告文件名：`deployment-report-YYYYMMDD-HHMMSS.md`

## 版本号规则

### 版本号格式

```
${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${BUILD_SEQ}
```

**示例**: `r1.13-done-4f05275c-20260702-770`

### 版本号组成

| 组件 | 说明 | 来源 | 示例 |
|------|------|------|------|
| `GIT_TAG` | Git标签 | `git describe --tags --abbrev=0` | `r1.13-done` |
| `GIT_SHA` | 提交哈希 | `git rev-parse --short=8 HEAD` | `4f05275c` |
| `BUILD_DATE` | 构建日期 | `date +%Y%m%d` | `20260702` |
| `BUILD_SEQ` | 构建序号 | `build_seq` 文件递增 | `770` |

### 创建Git Tag

```bash
# 创建新版本tag
git tag -a r1.14-beta -m "Release 1.14 beta"

# 推送tag到远程
git push origin r1.14-beta

# 查看所有tags
git tag -l
```

## 配置说明

脚本顶部的配置区域可根据实际环境调整：

```bash
# ==================== 配置区 ====================
SERVER="__SSH_TARGET_1__"        # 服务器地址
SSH_PORT="__PORT_1__"                     # SSH端口
NAMESPACE="pms-test"                 # K8s命名空间
DEPLOYMENT="llm-gateway-go-deployment"  # Deployment名称
IMAGE_NAME="kx-llm-gateway-go"       # 镜像名称
REGISTRY_INTERNAL="__DOMAIN_4__"  # 内部Registry
REGISTRY_LOCAL="127.0.0.1:__PORT_8__"      # 本地Registry
HEALTH_ENDPOINT="http://localhost:__PORT_9__/health"  # 健康检查端点
OLD_IMAGE_DAYS=30                    # 过期镜像天数
```

## 部署报告示例

```markdown
# 184环境部署报告

## 部署信息
- **部署时间**: 2026-07-02 13:45:23
- **部署环境**: 184 (__PUB_IP_1__)
- **命名空间**: pms-test
- **部署名称**: llm-gateway-go-deployment
- **操作人员**: __USER_1__

## 版本信息
- **Git Tag**: r1.13-done
- **Git SHA**: 4f05275c
- **Build Seq**: 770
- **Build Date**: 20260702
- **镜像标签**: r1.13-done-4f05275c-20260702-770

## 镜像信息
- **镜像名称**: kx-llm-gateway-go:r1.13-done-4f05275c-20260702-770
- **内部Registry**: __DOMAIN_4__/kx-llm-gateway-go:r1.13-done-4f05275c-20260702-770
- **本地Registry**: 127.0.0.1:__PORT_8__/kx-llm-gateway-go:r1.13-done-4f05275c-20260702-770
```

## 验证命令

### 检查部署状态

```bash
# 查看Pod状态
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl get pods -n pms-test -l app=llm-gateway-go"

# 查看实时日志
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl logs -n pms-test -l app=llm-gateway-go -f"

# 健康检查
curl http://__PUB_IP_1__:__PORT_9__/health | jq .
```

### 查看版本信息

```bash
# 查看容器内版本文件
POD_NAME=$(ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl get pods -n pms-test -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}'")
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl exec -n pms-test ${POD_NAME} -- cat __SERVER_PATH_1__/VERSION"
```

### 查看镜像

```bash
# 本地镜像
docker images | grep kx-llm-gateway-go

# 184服务器镜像
ssh -p __PORT_1__ __SSH_TARGET_1__ "docker images | grep kx-llm-gateway-go"
```

## 回滚操作

### 快速回滚到上一个版本

```bash
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test"
```

### 回滚到指定版本

```bash
# 查看部署历史
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl rollout history deployment/llm-gateway-go-deployment -n pms-test"

# 回滚到指定revision
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test --to-revision=3"
```

## 故障排查

### 1. 镜像构建失败

**问题**: Docker build失败

**解决方法**:
```bash
# 检查Dockerfile语法
docker build --no-cache -t test .

# 查看构建日志
docker build -t test . 2>&1 | tee build.log
```

### 2. 镜像推送失败

**问题**: 无法推送到registry

**解决方法**:
```bash
# 检查registry连接
docker pull 127.0.0.1:__PORT_8__/hello-world

# 检查Docker配置
cat ~/.docker/config.json

# 重启Docker daemon
sudo systemctl restart docker
```

### 3. K8s部署更新失败

**问题**: Rolling update超时或失败

**解决方法**:
```bash
# 查看Pod事件
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl describe pod -n pms-test -l app=llm-gateway-go"

# 查看deployment事件
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl describe deployment llm-gateway-go-deployment -n pms-test"

# 手动删除Pod强制重启
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl delete pod -n pms-test -l app=llm-gateway-go"
```

### 4. 健康检查失败

**问题**: 服务无法响应健康检查

**解决方法**:
```bash
# 查看服务日志
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl logs -n pms-test -l app=llm-gateway-go --tail=100"

# 进入容器调试
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl exec -it -n pms-test <pod-name> -- /bin/sh"

# 检查服务端口
ssh -p __PORT_1__ __SSH_TARGET_1__ "kubectl get svc -n pms-test"
```

### 5. 版本号不正确

**问题**: 版本号显示为 `v0.0.0` 或其他错误值

**解决方法**:
```bash
# 检查git tag
git tag -l

# 创建缺失的tag
git tag -a r1.13-done -m "Release 1.13"

# 验证tag获取逻辑
git describe --tags --abbrev=0
```

## 最佳实践

### 1. 部署前检查清单

- [ ] 确认本地代码已提交并推送
- [ ] 确认git tag存在且正确
- [ ] 确认184服务器可访问
- [ ] 确认Docker环境正常
- [ ] 确认kubectl连接正常
- [ ] 检查磁盘空间是否充足

### 2. 部署后验证清单

- [ ] 检查Pod状态为Running
- [ ] 验证健康检查返回正常
- [ ] 确认版本号正确
- [ ] 查看服务日志无异常
- [ ] 测试关键API功能
- [ ] 保存部署报告

### 3. 定期维护

```bash
# 每周清理Docker缓存
docker system prune -a -f

# 每月检查磁盘空间
ssh -p __PORT_1__ __SSH_TARGET_1__ "df -h"

# 定期审查部署日志
ls -lh deployment-report-*.md
```

## 安全注意事项

1. **SSH密钥管理**: 确保SSH私钥安全存储，不要泄露到代码仓库
2. **Registry凭证**: 如需认证，使用Docker config或K8s secret管理凭证
3. **日志脱敏**: 部署报告中不应包含敏感信息（密码、token等）
4. **权限控制**: 限制部署脚本的执行权限，只授予必要人员

## 脚本优化历史

### v2.0 (2026-07-02)
- ✅ 修复版本号获取逻辑，从git tag正确读取
- ✅ 统一使用 `build_seq` 文件（之前使用 `.deploy_seq`）
- ✅ 优化错误处理和日志输出
- ✅ 完善部署报告内容

### v1.0 (初始版本)
- ✅ 实现基础部署流程
- ✅ 集成健康检查
- ✅ 添加镜像清理功能

## 相关文件

- `deploy-184.sh`: 部署脚本
- `Dockerfile`: Docker构建文件
- `build_seq`: 构建序号文件
- `version.json`: 版本信息文件
- `deployment-report-*.md`: 部署报告

## 技术支持

如遇到问题，请联系运维团队或查看：
- [Kubernetes文档](https://kubernetes.io/docs/)
- [Docker文档](https://docs.docker.com/)
- [项目内部Wiki](内部链接)

---

**文档版本**: v2.0  
**最后更新**: 2026-07-02  
**维护人员**: DevOps Team

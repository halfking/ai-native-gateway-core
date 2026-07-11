# deploy-184 技能文档

## 技能名称
`deploy-184` - 标准化184环境部署流程

## 技能描述
自动化完成从代码检查、版本更新、镜像构建、镜像推送、K8s部署更新、健康检查到清理过期镜像的完整部署流程。

## 版本号原则
版本号严格遵循从 **git 仓库最近的 tag** 中获取，确保版本信息的准确性和可追溯性。

## 使用方法

### 基本用法
```bash
./deploy-184.sh
```

### 前置要求
1. **Git 仓库状态**: 建议工作区干净，无未提交改动
2. **Docker**: 本地已安装并运行 Docker
3. **SSH 访问**: 能够通过 SSH 访问184服务器（14.103.112.184:25022）
4. **Kubernetes 访问**: 184服务器上已配置 kubectl 访问权限
5. **Registry 访问**: 能够推送镜像到内部 registry 和本地 registry

### 部署环境配置
脚本使用以下默认配置（可在脚本顶部修改）：
- **服务器地址**: `root@14.103.112.184`
- **SSH 端口**: `25022`
- **K8s 命名空间**: `pms-test`
- **Deployment 名称**: `llm-gateway-go-deployment`
- **镜像名称**: `kx-llm-gateway-go`
- **内部 Registry**: `registry.kxpms.cn`
- **本地 Registry**: `127.0.0.1:5000`
- **健康检查端点**: `http://localhost:30080/health`
- **过期镜像天数**: `30天`

## 部署流程

### 步骤 1: 检查未提交改动
- 检查 git 工作区状态
- 如有未提交改动，提示用户选择是否提交
- 可选择提交、跳过或取消部署

### 步骤 2: 获取版本信息
- **Git Tag**: 通过 `git describe --tags --abbrev=0` 从 git 仓库获取最近的 tag
- **Git SHA**: 获取当前提交的短 SHA（8位）
- **Build Date**: 生成构建日期（格式: YYYYMMDD）
- **Build Seq**: 从 `build_seq` 文件读取并自动递增编译序号
- **Image Tag**: 组合生成完整镜像标签，格式: `${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${BUILD_SEQ}`
- **version.json**: 生成版本信息文件，包含版本、tag、SHA、序号、日期等信息

示例版本信息：
```json
{
  "version": "r1.13-done",
  "git_tag": "r1.13-done",
  "git_sha": "4f05275c",
  "build_seq": 770,
  "build_date": "20260702",
  "module": "llm-gateway-go"
}
```

### 步骤 3: 构建 Docker 镜像
- 使用 `docker build` 构建镜像
- 传递版本参数作为 build args
- 同时打上版本标签和 latest 标签
- 显示构建完成的镜像列表

### 步骤 4: 推送镜像到 Registry
- 推送到内部 registry: `registry.kxpms.cn`
- 推送到184本地 registry: `127.0.0.1:5000`
- 两个 registry 都推送同样的版本标签

### 步骤 5: 更新 Kubernetes 部署
- 使用 `kubectl set image` 更新 deployment 的容器镜像
- 自动等待 rolling update 完成（超时时间: 5分钟）
- 显示部署更新进度

### 步骤 6: 健康检查
- 等待服务启动（10秒）
- 检查 Pod 状态（Running 状态）
- 调用健康检查端点验证服务可用性
- 验证容器内版本信息是否与预期一致
- 读取容器内 `/opt/llm-gateway-go/VERSION` 或 `/.VERSION` 文件

### 步骤 7: 清理过期镜像
- 清理 Docker dangling 镜像
- 删除超过设定天数的旧版本镜像（保留最近5个版本）
- 将删除记录写入 `/opt/ready-to-delete/deleted-images-YYYYMMDD.log`
- 实际删除镜像而非移动文件

### 步骤 8: 生成部署报告
生成详细的 Markdown 格式部署报告，包含：
- **部署信息**: 时间、环境、命名空间、操作人员
- **版本信息**: Git Tag、SHA、Build Seq、Build Date、镜像标签
- **镜像信息**: 镜像名称、Registry 地址
- **Git 提交信息**: 最近的提交记录
- **健康检查结果**: 健康检查端点返回的 JSON
- **Pod 状态**: Kubernetes Pod 列表
- **验证命令**: 快速验证和排查命令
- **回滚命令**: 如需回滚的操作指令

报告文件命名格式: `deployment-report-YYYYMMDD-HHMMSS.md`

## 版本号获取逻辑优化

### 修复前的问题
- 版本号硬编码为 `r1.13-done`
- 未从 git tag 动态获取
- 无法自动适应版本变化

### 修复后的逻辑
```bash
# 从 git 获取最近的 tag 作为版本号
GIT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
```

### 版本号规范
- 优先从 git tag 获取
- 如果没有 tag，使用默认值 `v0.0.0`
- 支持带 `v` 前缀的版本号（如 `v1.0.0`）
- 支持语义化版本号（如 `r1.13-done`）

## 文件说明

### build_seq 文件
- **位置**: 项目根目录
- **用途**: 存储编译序号，每次部署自动递增
- **格式**: 纯数字，如 `769`
- **初始化**: 如果文件不存在，自动初始化为 `0`

### version.json 文件
- **位置**: 项目根目录（构建时生成）
- **用途**: 存储完整的版本信息，用于运行时查询
- **格式**: JSON
- **内容**:
  ```json
  {
    "version": "版本号",
    "git_tag": "Git Tag",
    "git_sha": "Git SHA",
    "build_seq": 编译序号,
    "build_date": "构建日期",
    "module": "模块名称"
  }
  ```

### 部署报告文件
- **位置**: 项目根目录
- **命名**: `deployment-report-YYYYMMDD-HHMMSS.md`
- **格式**: Markdown
- **用途**: 记录每次部署的详细信息，便于追溯和审计

## 快速验证命令

部署完成后，可使用以下命令快速验证：

```bash
# 健康检查
curl http://14.103.112.184:30080/health | jq .

# 查看 Pod 状态
ssh -p 25022 root@14.103.112.184 'kubectl get pods -n pms-test -l app=llm-gateway-go'

# 查看日志
ssh -p 25022 root@14.103.112.184 'kubectl logs -n pms-test -l app=llm-gateway-go --tail=50'

# 查看 Deployment
ssh -p 25022 root@14.103.112.184 'kubectl get deployment -n pms-test llm-gateway-go-deployment'

# 验证容器内版本
ssh -p 25022 root@14.103.112.184 'kubectl exec -n pms-test $(kubectl get pods -n pms-test -l app=llm-gateway-go --field-selector=status.phase=Running -o jsonpath="{.items[0].metadata.name}") -- cat /opt/llm-gateway-go/VERSION'
```

## 回滚操作

如果部署后发现问题，可以使用以下命令回滚：

```bash
# 回滚到上一个版本
ssh -p 25022 root@14.103.112.184 'kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test'

# 查看回滚历史
ssh -p 25022 root@14.103.112.184 'kubectl rollout history deployment/llm-gateway-go-deployment -n pms-test'

# 回滚到特定版本
ssh -p 25022 root@14.103.112.184 'kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test --to-revision=N'
```

## 常见问题

### 1. 未提交的改动如何处理？
脚本会检测未提交的改动，并提示用户选择：
- 提交后继续部署（推荐）
- 跳过提交继续部署（改动不会包含在镜像中）
- 取消部署

### 2. 版本号从哪里获取？
版本号严格从 git 仓库的最近 tag 获取，使用命令：
```bash
git describe --tags --abbrev=0
```

### 3. build_seq 文件丢失怎么办？
脚本会自动检测，如果文件不存在，会初始化为 `0` 并继续执行。

### 4. 镜像推送失败怎么办？
- 检查 Docker 是否正常运行
- 检查 Registry 地址是否正确
- 检查网络连接是否正常
- 检查 Registry 认证是否配置

### 5. K8s 部署更新失败怎么办？
- 检查 SSH 连接是否正常
- 检查 kubectl 是否配置正确
- 检查命名空间和 deployment 名称是否正确
- 查看 Pod 日志排查问题

### 6. 健康检查失败怎么办？
- 检查服务端口是否正确（30080）
- 检查健康检查端点是否正确（/health）
- 查看 Pod 日志排查启动问题
- 检查 Pod 是否处于 Running 状态

## 安全注意事项

1. **SSH 访问**: 确保 SSH 密钥配置正确，避免密码泄露
2. **Registry 认证**: 推送镜像前确保已登录 Registry
3. **Kubernetes 权限**: 确保有足够的权限操作目标命名空间
4. **备份**: 重要部署前建议先备份当前版本
5. **回滚准备**: 熟悉回滚命令，以便快速恢复

## 最佳实践

1. **部署前检查**: 
   - 确保代码已提交
   - 确保 git tag 已打好
   - 确保本地和远程代码同步

2. **部署中监控**:
   - 观察 rolling update 进度
   - 检查 Pod 启动状态
   - 查看日志确认无错误

3. **部署后验证**:
   - 执行健康检查
   - 验证版本信息
   - 测试关键功能
   - 查看部署报告

4. **定期清理**:
   - 定期清理过期镜像
   - 定期清理旧的部署报告
   - 定期检查磁盘空间

## 脚本维护

### 修改配置
如需修改部署配置，编辑脚本顶部的配置区：
```bash
# ==================== 配置区 ====================
SERVER="root@14.103.112.184"
SSH_PORT="25022"
NAMESPACE="pms-test"
DEPLOYMENT="llm-gateway-go-deployment"
IMAGE_NAME="kx-llm-gateway-go"
REGISTRY_INTERNAL="registry.kxpms.cn"
REGISTRY_LOCAL="127.0.0.1:5000"
HEALTH_ENDPOINT="http://localhost:30080/health"
OLD_IMAGE_DAYS=30
```

### 脚本位置
- **路径**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/deploy-184.sh`
- **权限**: 确保脚本有执行权限 `chmod +x deploy-184.sh`

### 版本控制
- 脚本本身也应该纳入 git 版本控制
- 重大修改应该更新此文档
- 建议为脚本的重大变更打上 tag

## 更新日志

### 2026-07-02
- ✅ 修复版本号获取逻辑，改为从 git tag 动态获取
- ✅ 统一使用 `build_seq` 文件（原 `.deploy_seq` 改为 `build_seq`）
- ✅ 优化脚本输出格式，增加彩色日志
- ✅ 完善健康检查和版本验证逻辑
- ✅ 优化镜像清理逻辑
- ✅ 生成详细的部署报告

---

*文档生成时间: 2026-07-02*
*脚本版本: v1.0*

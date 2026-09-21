# 本地 k3s 观察与管理系统 Runbook（2026-09-02）

> 适用范围：本机 macOS + Docker Desktop，通过 k3d 在 Docker 内创建独立 k3s 集群，用于本地观察、管理、验证 Kubernetes 工作负载。

## 一、目标

使用 [scripts/local-k3s-observability.sh](../../../../scripts/local-k3s-observability.sh) 一键完成本地 k3s 观察环境：

- 创建独立 k3d/k3s 集群 `llm-gateway-observe`
- 安装 `metrics-server`
- 安装 Kubernetes Dashboard
- 创建 Dashboard 登录用 ServiceAccount（默认只读 `view` 权限）
- 提供状态检查、token 获取、Dashboard 端口转发和删除命令

## 二、安全边界

脚本默认只影响本机 Docker/k3d 资源：

- 不连接 154/245/252/184 等远端环境
- 不修改当前运行中的本地 `llm-gateway` 进程
- 不修改 `llm-gateway-pg` / Redis 容器数据
- 不读取或写入业务密钥
- 不执行 `docker system prune` 或其它批量清理动作
- Dashboard token 默认 2 小时有效，默认只读；管理权限必须显式开启

`down` 只删除脚本创建的 k3d 集群，不删除仓库文件或现有本地数据库。

## 三、前置条件

必需：

- Docker Engine / Docker Desktop 已运行
- `kubectl` 可用
- `k3d` 可用

本机若缺少 `k3d`，可使用脚本的 `--install-tools` 自动通过 Homebrew 安装：

```bash
bash scripts/local-k3s-observability.sh up --install-tools
```

也可以手动安装：

```bash
brew install k3d kubectl
```

## 四、安装

默认安装：

```bash
bash scripts/local-k3s-observability.sh up
```

缺依赖时自动安装工具后安装：

```bash
bash scripts/local-k3s-observability.sh up --install-tools
```

可选配置：

```bash
K3S_OBS_CLUSTER_NAME=llm-gateway-observe \
K3S_OBS_API_PORT=6550 \
K3S_OBS_DASHBOARD_PORT=10443 \
bash scripts/local-k3s-observability.sh up
```

如需完整管理权限，显式开启 `cluster-admin`：

```bash
K3S_OBS_DASHBOARD_ROLE=cluster-admin \
K3S_OBS_TOKEN_DURATION=2h \
bash scripts/local-k3s-observability.sh up
```

## 五、访问 Dashboard

启动端口转发：

```bash
bash scripts/local-k3s-observability.sh port-forward
```

浏览器打开：

```text
https://127.0.0.1:10443
```

获取 2 小时 token：

```bash
bash scripts/local-k3s-observability.sh token
```

Dashboard 使用自签证书，本地浏览器会提示证书风险；仅限本机使用时可继续访问。

## 六、本地共享 PG Fixup

若本地 `llm-gateway-pg` 被 RedClaw / ACC 共享，且 PG 日志出现 `relation does not exist`，执行本地专用迁移：

```bash
docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway \
	-v ON_ERROR_STOP=1 -f - < sql/migrations/local/641_local_shared_platform_schema_fixup.sql
```

这份迁移只在 `sql/migrations/local/` 下维护，不进入通用发布包。它只做 additive DDL 和授权，不删除业务数据，不修改已有角色密码。

## 七、状态检查

```bash
bash scripts/local-k3s-observability.sh status
```

预期输出包含：

- 集群名和 kubeconfig 路径
- API 地址 `https://127.0.0.1:6550`
- Dashboard 本地地址
- `kubectl get nodes -o wide`
- `kubectl get pods -A`

也可以直接指定 kubeconfig：

```bash
KUBECONFIG=~/.kube/llm-gateway-observe.yaml kubectl get pods -A
KUBECONFIG=~/.kube/llm-gateway-observe.yaml kubectl top nodes
```

## 八、卸载与回滚

删除本地 k3d/k3s 集群：

```bash
bash scripts/local-k3s-observability.sh down
```

脚本会保留 kubeconfig 文件：

```text
~/.kube/llm-gateway-observe.yaml
```

如需手动确认 Docker 资源：

```bash
k3d cluster list
docker ps --filter 'name=k3d-llm-gateway-observe'
```

## 九、常见问题

### 1. `k3d not found`

执行：

```bash
bash scripts/local-k3s-observability.sh up --install-tools
```

### 2. Docker 未运行

启动 Docker Desktop 后重试：

```bash
docker info
bash scripts/local-k3s-observability.sh up
```

### 3. Dashboard 无法打开

确认端口转发进程仍在运行：

```bash
bash scripts/local-k3s-observability.sh port-forward
```

然后重新访问 `https://127.0.0.1:10443`。

### 4. `kubectl top` 无数据

等待 `metrics-server` 就绪后重试：

```bash
KUBECONFIG=~/.kube/llm-gateway-observe.yaml kubectl -n kube-system get pods
KUBECONFIG=~/.kube/llm-gateway-observe.yaml kubectl top nodes
```

## 十、验收标准

本地 k3s 观察系统完成的判定：

- `bash scripts/local-k3s-observability.sh up` 退出码为 0
- `bash scripts/local-k3s-observability.sh status` 能列出节点和 Pod
- `metrics-server` Deployment rollout 成功
- `kubernetes-dashboard` Deployment rollout 成功
- `token` 命令能生成可登录 Dashboard 的 token
- `port-forward` 后可在浏览器打开 Dashboard 登录页
- 本地共享 PG fixup 可重复执行，`public.schema_migrations` 存在 `641` 记录

## 十一、下一步

安装完成后，可把 llm-gateway 本地 Docker Compose 工作负载迁移或镜像部署到该 k3s 集群中做资源观察；也可以先只用它观察 k3s/Dashboard/metrics-server 是否稳定，再处理本地 PG schema 与网关代码错误。
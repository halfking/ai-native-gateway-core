# deploy-252 / deploy-154 技能文档

## 技能名称
`deploy-252` - 标准化 252 (阿里云 llm.itestu.cn) 部署流程
`deploy-154` - 标准化 154 (主机部署 llm.kxpms.cn) 部署流程
`deploy-kaixuan-1` - 标准化 kaixuan-1 (内网 k3s 控制面) 部署流程

> 历史备注：原 `deploy-184` / `deploy-71` 技能已退役。184 → 252（公网数据面），
> 71 → 154（公网主机部署）。旧的 `deploy-184.sh` 调用方式映射到 `scripts/deploy.sh 252`，
> `deploy-71.sh` 映射到 `scripts/deploy.sh 154`。

## 技能描述

自动化完成从代码检查、版本更新、镜像构建、镜像推送、k3s 部署更新、
健康检查到清理过期镜像的完整部署流程。

## 使用方法

### 基本用法

```bash
# 252 阿里云 llm.itestu.cn 数据面部署
./scripts/deploy.sh 252

# 154 主机部署 llm.kxpms.cn
./scripts/deploy.sh 154

# kaixuan-1 内网 k3s 控制面部署
./scripts/deploy.sh kaixuan-1

# 同时部署 252 + 154（推荐顺序）
./scripts/deploy.sh both

# 仅构建（不部署）
./scripts/deploy.sh build

# 仅运行 DB 迁移
./scripts/deploy.sh migrate 252

# 仅运行验证
./scripts/deploy.sh verify 252

# 回滚
./scripts/deploy.sh rollback 252
```

### 前置要求

1. **Git 仓库状态**: 建议工作区干净，无未提交改动
2. **SSH 访问**: 通过 ssh-add 加载对应服务器私钥，或用 env-injector 的
   `inject deploy-252` / `inject deploy-154` / `inject deploy-kaixuan-1`
3. **Registry 访问**: 能够推送镜像到内部 registry
4. **kaixuan-* 服务器**: 不要自行安装 docker，tart vm + k3s 已就绪
5. **阿里云 252**: 已安装 docker + redis:6389 + pg17:5432

### 部署环境配置

脚本使用以下默认配置（可在脚本顶部修改）：

| 目标 | 服务器地址 | SSH 端口 | 用户 | 角色 |
|---|---|---|---|---|
| 252 | root@115.29.212.252 (root@172.16.2.210) | 25022 | root | llm.itestu.cn 数据面 + nps + vpn |
| 154 | root@47.97.111.154 (root@172.16.2.209) | 25022 | root | llm.kxpms.cn 主机部署 |
| 245 | root@8.136.114.245 (root@172.16.2.241) | 25022 | root | registry.kxpms.cn 生产镜像 |
| 186 | root@118.31.18.168 | 25022 | root | 应用服务器（即将弃用） |
| kaixuan-1 | kaixuan@192.168.31.28 | 25022 | kaixuan | k3s 控制面 + PG 17 (192.168.31.8:30432) |
| kaixuan-2 | kaixuan@192.168.31.19 | 25022 | kaixuan | k3s worker（应用服务） |
| kaixuan-3 | kaixuan@192.168.31.30 | 25022 | kaixuan | k3s worker（数据服务 + nexus） |

- **k3s 命名空间**: `pms-test`
- **Deployment 名称**: `llm-gateway-go-deployment`
- **镜像名称**: `kx-llm-gateway-go`
- **生产 Registry**: `registry.kxpms.cn`（245 公网）
- **开发 Registry**: `registry.itestu.cn`（kaixuan-1 内网 192.168.31.8:5000）
- **健康检查端点**: `http://localhost:30080/health`
- **过期镜像天数**: 30 天

## 部署流程

### 步骤 1: 检查未提交改动
- 检查 git 工作区状态
- 如有未提交改动，提示用户选择是否提交
- 可选择提交、跳过或取消部署

### 步骤 2: 获取版本信息
- **Git Tag**: 通过 `git describe --tags --abbrev=0` 从 git 仓库获取最近的 tag
- **Git SHA**: 获取当前提交的短 SHA（8位）
- **Build Date**: 生成构建日期（格式：YYYYMMDD）
- **Build Seq**: 从 `version.json` 读取并自动递增编译序号
- **Image Tag**: 组合生成完整镜像标签，格式：`${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${BUILD_SEQ}`

### 步骤 3: 预检
- go build / go vet
- vue-tsc（web/ 改动时）
- pre-commit hooks

### 步骤 4: 构建镜像
- 在 buildx 中构建多平台镜像
- tag 包含版本号信息

### 步骤 5: 推送到 Registry
- 252 部署推送到 `registry.kxpms.cn`（245 公网）
- 154 / kaixuan-1 部署推送到 `registry.itestu.cn`（kaixuan-1 内网 5000）
- 失败时 SSH 到目标服务器 pull + retag + push

### 步骤 6: 部署到目标服务器
- 252 / 245 / 186：通过 docker-compose / systemd 重启服务
- 154：通过 systemctl 重启主机模式服务
- kaixuan-1：kubectl rollout（k3s 集群）

### 步骤 7: 健康检查
- 轮询 health 端点直到返回 200
- 超时则自动回滚

### 步骤 8: 清理过期镜像
- 删除 build_seq < 当前 - 30 的镜像
- 保留最近 5 个版本以备回滚

## 数据库信息（2026-07-12 重新规划）

### 阿里 252 上的 PostgreSQL 17

| 字段 | 值 |
|---|---|
| 主机 | 172.16.2.210 |
| 端口 | 5432 |
| 数据库 | llm_gateway / crm 等 |
| 用户 | kxuser / llm_gateway / kaixuan_user / doc_tools_user / casdoor_user / crm_user |
| 密码 | 统一在 .env.252.enc 中加密管理 |

### kaixuan-1 上的 PostgreSQL 17 + Citus 13.3-1

| 字段 | 值 |
|---|---|
| 主机 | 192.168.31.8 (k3s 控制面后端) |
| 端口 | 30432 |
| 扩展 | citus 13.3-1 + pgvector + 列存储引擎 |
| 外网 | pg-dev.itestu.cn (NPS 转发) |
| 用户 | 同上 |
| 用途 | llm-gateway-go + memora + www + auth |

### 数据库用户清单（统一）

- `crm_user` / `crm_pass123` → CRM
- `llm_gateway` / `<DB_PASSWORD>` → 主超级用户
- `kaixuan_user` / `kaixuan_pass123` → 开轩主应用
- `doc_tools_user` / `doc_tools_pass123` → doc-tools
- `casdoor_user` / `casdoor_pass123` → Casdoor
- `kxuser` / `kxuser123` → 列存表 owner

## 部署检查清单

### 部署前
- [ ] SSH 私钥已加载（ssh-add -l 检查）
- [ ] 当前分支与 origin/main 一致
- [ ] 工作区干净或改动已 commit
- [ ] pre-commit hooks 全绿
- [ ] 当前 build_seq 未冲突

### 部署中
- [ ] 镜像构建成功
- [ ] 镜像推送成功
- [ ] 目标服务器可 SSH
- [ ] kubectl rollout 成功（k3s 环境）

### 部署后
- [ ] health 端点 200
- [ ] Pod/容器状态 Running
- [ ] 数据库连接正常
- [ ] 24 小时无 P0/P1 告警

## 故障排查

参见 `deploy/DEPLOYMENT_GUIDE.md` 故障排查章节。

## 历史备注

- 2026-07-12: 服务器角色重新规划。71 / 184 退役，新增 252（数据面）、
  245（registry）、kaixuan-1/2/3（内网 k3s）。
- 2026-07-11: 旧 `deploy-184.sh` / `deploy-71.sh` 已删除，由
  `scripts/deploy.sh <target>` 统一入口替代。
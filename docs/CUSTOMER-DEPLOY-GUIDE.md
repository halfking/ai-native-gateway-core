# llm-gateway-go 客户部署指南

> 本指南面向**客户运维**。你只需要 5 步就能完成部署。
> **核心原则**：所有部署内容都集中在客户机器的 `~/llm-gateway/` 目录下，所有数据（数据库、Redis、附件、日志）都在容器外 bind-mount，**容器重启数据不丢失**。

## 一、前置准备

**操作系统要求**（任一）：
- ✅ Windows 10/11
- ✅ macOS（Apple Silicon 或 Intel）
- ✅ Ubuntu / Debian / CentOS / RHEL / Fedora
- ✅ **国产 OS**：统信 UOS、深度 Deepin、银河麒麟 V10/V11、欧拉 openEuler、龙蜥 Anolis
- ✅ **国产 CPU**：龙芯（loongarch64）、申威（sw_64）、鲲鹏（arm64）

**最低配置**：
- 2 GB 内存
- 5 GB 可用磁盘
- **可选**：能访问 `registry.kxpms.cn` 或公网（用于镜像拉取）

## 二、5 步部署

### 第 1 步：拷贝 release 包到客户机器

把 `release-v1.0.0.tar.gz` 拷贝到客户机器任意目录（U盘 / scp / 共享盘都可以）。

```bash
# 把 release 包放到 ~/llm-gateway/ 目录（也可以放别处）
mkdir -p ~/llm-gateway
cp release-v1.0.0.tar.gz ~/llm-gateway/
cd ~/llm-gateway
tar -xzf release-v1.0.0.tar.gz
cd release-v1.0.0
```

### 第 2 步：运行安装器

**macOS / Linux**：
```bash
./install.sh
# 默认部署到 ~/llm-gateway/
# 自定义：LLM_GATEWAY_HOME=/opt/myapp ./install.sh
```

**Windows**：
```powershell
.\install.ps1
# 默认部署到 %USERPROFILE%\llm-gateway\
# 自定义：$env:LLM_GATEWAY_HOME="D:\myapp"; .\install.ps1
```

### 第 3 步：回答 11 步配置提问

所有密码字段都可以直接回车跳过（自动生成强随机值）：

```
  1. 安装路径 [/Users/foo/llm-gateway]:    [回车]
  2. 应用端口 (HTTP) [8781]:                 [回车]
  3. PostgreSQL 端口 [5432]:                [回车]
  4. Redis 端口 [6379]:                     [回车]
  5. PostgreSQL 密码 (留空自动生成):        [回车]
  6. Redis 密码 (留空自动生成):             [回车]
  7. LLM Gateway API Key (留空自动生成):    [回车]
  8. LLM Gateway Admin API Key (留空自动生成): [回车]
  9. JWT Secret (留空自动生成):             [回车]
  10. 凭据加密 Key (留空自动生成):          [回车]
  11. 镜像源策略: auto                       [回车]

确认开始安装? (Y/n): Y
```

### 第 4 步：等待部署完成（约 5-10 分钟）

### 第 5 步：验证

```bash
curl http://localhost:8781/healthz
# 应该返回 {"status":"ok"}
```

## 三、目录结构

部署完成后 `~/llm-gateway/` 内容如下（所有数据都在容器外 bind-mount）：

```
~/llm-gateway/                           ← 部署根目录（所有内容都在这里）
├── README.md                            ← 目录说明
├── .env                                 ← 所有 secrets（chmod 600）
├── compose.yml                          ← docker-compose 全栈定义
├── install.sh / install.ps1             ← 入口脚本
├── uninstall.sh
│
├── bin/                                 ← installer 副本
│   └── llm-gw-installer                 ← 自更新/重装用
│
├── config/                              ← 静态配置
│   ├── MANIFEST.json                    ← 版本元数据
│   └── env.template                     ← 环境变量模板
│
├── app/                                 ← 应用相关
│   ├── VERSION                          ← 版本号（bind-mount 到容器）
│   └── logs/                            ⭐ bind-mount → /var/log/llm-gateway
│
├── db/                                  ← PostgreSQL
│   ├── data/                            ⭐ bind-mount → /var/lib/postgresql/data
│   └── init/                            ← SQL 初始化文件备份
│       ├── 00-prereqs.sql
│       ├── 01-schema.sql
│       └── 02-seed.sql
│
├── redis/                               ← Redis
│   └── data/                            ⭐ bind-mount → /data
│
├── attachments/                         ⭐ bind-mount → /opt/.../data/attachments
│
├── backups/                             ← 备份根目录
│   ├── daily/
│   └── manual/
│
└── reports/                             ← 部署/运行报告
    └── install-report.md
```

⭐ = bind-mount，**容器重启数据不丢失**

## 四、数据持久化保证

| 场景 | 数据安全 |
|------|---------|
| 容器删除并重建 | ✅ 数据保留（在 ~/llm-gateway/ 下） |
| Docker 重启 | ✅ 数据保留 |
| 机器重启 | ✅ 数据保留 |
| 卸载但保留数据（默认） | ✅ 数据保留 |
| 卸载并清理（--purge） | ❌ 数据删除 |

**关键点**：所有持久化数据都在容器外，**不会因为容器操作丢失**。

## 五、日常运维

### 重启服务
```bash
cd ~/llm-gateway
docker compose restart
```

### 查看日志
```bash
# 容器日志
docker compose logs -f

# 文件日志（容器外）
tail -f ~/llm-gateway/app/logs/gateway.log
```

### 停止服务（保留数据）
```bash
cd ~/llm-gateway
docker compose down
```

### 重新部署同版本（重置容器，数据保留）
```bash
cd ~/llm-gateway
docker compose down
docker compose up -d
```

### 完全卸载（清理数据）
```bash
cd ~/llm-gateway
./uninstall.sh --purge
```

### 修改配置
```bash
nano ~/llm-gateway/.env
cd ~/llm-gateway
docker compose restart
```

### 备份数据
```bash
# 1. 停止服务（保证一致性）
cd ~/llm-gateway && docker compose down

# 2. 备份整个根目录
tar czf ~/llm-gateway-backup-$(date +%Y%m%d).tar.gz ~/llm-gateway

# 3. 恢复服务
cd ~/llm-gateway && docker compose up -d
```

## 六、故障排查

### Q1：端口被占用（8781/5432/6379）？
在第 2/3/4 步指定其他端口，或停止占用端口的程序：
```bash
# 查找占用端口的进程
lsof -i :5432
```

### Q2：磁盘空间不足？
```bash
# 清理旧日志
find ~/llm-gateway/app/logs -name "*.gz" -mtime +30 -delete

# 清理 docker 缓存
docker system prune -a
```

### Q3：忘记 .env 里的密码？
```bash
cat ~/llm-gateway/.env | grep -E '(PASSWORD|KEY|SECRET)'
```

### Q4：想要重置安装？
```bash
# 重装同版本（容器重建，数据保留）
cd ~/llm-gateway
docker compose down
docker compose up -d

# 完全重装（清除一切）
cd ~/llm-gateway
./uninstall.sh --purge
~/llm-gateway/release-v1.0.0/install.sh
```

## 七、联系支持

部署报告：`~/llm-gateway/reports/install-report.md`

如有问题，提供：
1. 操作系统和版本
2. `reports/install-report.md` 文件
3. `docker compose logs` 输出
4. 错误截图

---

**文档版本**：v1.1
**最后更新**：2026-07-03

# llm-gateway-go 打包脚本（Packaging）

## 用途

把 llm-gateway-go 应用 + 数据库镜像 + Redis 镜像 + 配置 + installer 二进制打包成一个 release tarball，方便客户机器一键部署。

## 脚本说明

| 脚本 | 用途 |
|---|---|
| `package.sh` | **主脚本**：构建镜像 → 推内部 registry → 打 release tarball |
| `push-to-internal-registry.sh` | **辅助**：仅推镜像到 registry，不打 tarball |
| `gen-manifest.sh` | **辅助**：生成 MANIFEST.json（被 package.sh 调用） |

## 完整流程

```bash
# 1. 在构建机上执行（推送 registry + 打 tarball）
./packaging/package.sh v1.0.0

# 2. 产出 release-v1.0.0.tar.gz
ls -lh release-v1.0.0.tar.gz

# 3. 拷贝到客户机器
scp release-v1.0.0.tar.gz root@customer-machine:/path/

# 4. 客户解压并安装
ssh root@customer-machine
cd /path/
tar -xzf release-v1.0.0.tar.gz
cd release-v1.0.0
./install.sh
```

## 环境变量

```bash
# 内部 registry 配置
export REGISTRY=registry.kxpms.cn        # 默认
export PROJECT=kaixuan                   # 默认
export REGISTRY_USERNAME=alice           # 可选
export REGISTRY_PASSWORD=secret          # 可选

# 控制行为
export SKIP_PUSH=1                       # 跳过推 registry
export SKIP_OFFLINE=1                    # 跳过离线 tarball

# 编译 Go 用
export GOPROXY=https://goproxy.cn,direct
```

## 仅推送 registry

如果只想推镜像到内部 registry（不打 tarball，让客户从 registry 拉取）：

```bash
./packaging/push-to-internal-registry.sh v1.0.0
```

推送完成后，客户机器只要能访问 `registry.kxpms.cn` 即可：

```bash
# 客户机器上
KX_REGISTRY=registry.kxpms.cn ./install.sh
# 安装器会自动从 registry.kxpms.cn 拉取镜像
```

## 内部 registry 镜像命名

```
registry.kxpms.cn/kaixuan/kx-llm-gateway-go:v1.0.0    # 应用
registry.kxpms.cn/kaixuan/citusdata/citus:11.3.0       # DB
registry.kxpms.cn/kaixuan/redis:7-alpine                # Cache
```

## 离线 tarball 命名

```
release-v1.0.0.tar.gz
└── release-v1.0.0/
    ├── MANIFEST.json
    ├── install.sh / install.ps1 / install.bat
    ├── llm-gw-installer (+ 8 个跨平台二进制)
    ├── compose.yml / .env.template
    ├── uninstall.sh / README.md
    ├── images/
    │   ├── kx-llm-gateway-go-v1.0.0.tar.gz
    │   ├── kx-citus-v11.3.0.tar.gz
    │   └── kx-redis-v7-alpine.tar.gz
    ├── sql/{00,01,02}*.sql
    └── checksums.sha256
```

## 镜像兜底链

`package.sh` 推送到内部 registry 是**主链路**，但 installer 还支持：

```
[1] 离线包 images/*.tar.gz
    ↓ 失败
[2] registry.kxpms.cn  ← package.sh 推到这里
    ↓ 失败
[3] registry.cn-hangzhou.aliyuncs.com (阿里云 mirror)
    ↓ 失败
[4] docker.io (官方)
    ↓ 全部失败
❌ 报错
```

因此即使内部 registry 挂了，客户也能从公网拉镜像。

## dry-run / 测试

```bash
# 只构建镜像，不推送，不打包（用于本地测试）
SKIP_PUSH=1 ./packaging/package.sh v0.0.0-test
```

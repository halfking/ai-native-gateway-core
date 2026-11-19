# LLM Gateway Go — 升级指南

本文档描述 LLM Gateway Go 的升级流程、回滚步骤及常见问题。

> **2026-11-19 整体重写**：旧版写于 installer CLI flag 体系定型之前，其中
> `upgrade check` / `upgrade apply --to` / `rollback --to` / `config set` /
> `--timeout` / `--offline-binary` / `--force-download` 等命令均不存在，
> 按旧文档操作会直接报错；本文档已逐条对齐当前实现
> （`installer/cmd/llm-gw-installer/upgrade.go`、`installer/internal/upgrader/`）。

---

## 目录

- [先选对通道](#先选对通道)
- [升级与全新安装的区别（硬性语义）](#升级与全新安装的区别硬性语义)
- [检查更新](#检查更新)
- [离线升级（docker compose 安装的正确通道）](#离线升级docker-compose-安装的正确通道)
- [在线升级（仅 systemd 单机布局）](#在线升级仅-systemd-单机布局)
- [源码编译更新](#源码编译更新)
- [回滚操作](#回滚操作)
- [常见问题](#常见问题)

---

## 先选对通道

| 你的安装形态 | 正确通道 | 依据 |
|---|---|---|
| installer 安装的 docker compose（full/lite） | **离线升级包** `upgrade --action apply --offline` | 在线路径检测到 `compose.yml` 会 fail-fast 并指路离线包（`upgrader/apply.go` 布局守卫） |
| systemd 单机布局（二进制在 `/usr/local/bin/kx-gateway`） | 在线升级 `upgrade --action apply` | 仅此布局支持；重启走 `systemctl restart kx-gateway` |
| 内部开发机（211/245/154） | `scripts/deploy-211.sh` / `deploy-154.sh` / `deploy-245.sh` | 内部蓝绿部署通道，与用户升级互不替代 |

安装目录默认 `/opt/llm-gateway-go`（`--data-dir` 可覆盖；下文省略）。

---

## 升级与全新安装的区别（硬性语义）

更新不是重新安装。两条硬性语义（实现于 2026-11-19，`installer/README.md`「安装与更新的区别」有完整说明）：

1. **保留原有配置与密钥**：`install` 检测到安装目录已有 `.env` 时，自动把它作为
   配置基底（缺失项自动补齐），**不会**重新生成 POSTGRES/API/JWT/加密密钥——
   加密密钥一旦轮换，既有凭据密文不可解。需要彻底重置配置时显式加
   `--fresh-config`（或先 `uninstall`）。升级通道（`upgrade`）本身不碰 `.env`。
2. **数据库已在 ⇒ 只补增量**：`install` 的数据库初始化检测 `schema_migrations`
   台账非空时进入增量模式——跳过基线三件（00-prereqs/01-schema/02-seed），
   startup 迁移只应用台账未登记的 pending 项。种子全部 `ON CONFLICT DO NOTHING`，
   重放与跳过都不会覆盖用户改动。

---

## 检查更新

```bash
./bin/llm-gw-installer upgrade --action check
# 可选：--master-url https://llm.kxpms.cn --channel stable
```

输出当前版本、更新通道与可用更新（版本/changelog）。`--action` 必填，支持
`check / apply / rollback` 三种操作。

---

## 离线升级（docker compose 安装的正确通道）

### 制作升级包（发布侧）

```bash
bash scripts/build-upgrade-package.sh vX.Y.Z [--out DIR] [--build-seq N]
```

包内容：`kx-gateway` 二进制（linux/amd64）+ `images.tar`（docker 镜像）+
`migrations/startup/*.sql`（**动态枚举**自 `sql/migrations/startup`，
version≥412 地板、排除 `.down.sql`、带防空洞门）+ `manifest.json`（逐文件
SHA256）+ `.sha256` 校验文件。

### 应用升级包（客户侧）

```bash
./bin/llm-gw-installer upgrade --action apply \
  --offline --offline-package /path/to/llm-gateway-go-vX.Y.Z-upgrade.tar.gz
```

执行序列（`upgrader.OfflineApplier`）：

1. 解压到临时目录，按 `manifest.json` 校验每个文件的 SHA256；
2. **备份**二进制 + `docker-compose.yml`/`compose.yml` + `.env` 到
   `<dataDir>/backups/upgrades/`；
3. `docker load` 新镜像；
4. 按数值序逐个执行包内 `migrations/startup/*.sql`（幂等契约：每个迁移
   可安全重放；编号跨千位也保序）；
5. 替换二进制 → 重启容器 → 健康检查；
6. 任一步失败**自动回滚**（恢复备份三件套并重启）。

注意：`--offline-package` 必须与 `--offline` 同用——缺 `--offline` 时该参数
被忽略、走在线路径。

---

## 在线升级（仅 systemd 单机布局）

```bash
./bin/llm-gw-installer upgrade --action apply --channel stable
```

执行序列（`upgrader.ApplyUpdate`）：下载二进制 → SHA256 校验 → 备份当前
二进制到 `<dataDir>/backups/<旧版本>.backup` → 替换 → `systemctl restart
kx-gateway` → 健康检查（3 次重试，失败自动回滚）→ 写 `app/VERSION` →
上报结果。

**两个限制要知情**：

- 在 docker compose 安装上**不可用**：布局守卫会在开始前报错并指路离线包；
- 在线路径**不投递 SQL 迁移文件链**——新版本的启动迁移靠网关重启时
  `db.Open → ApplyMigrations` 的 Go ensure 链自愈；落在
  `sql/migrations/startup` 文件链里的结构变更必须走离线包。

---

## 源码编译更新

从源码更新（`install.sh --channel source` 会在本机构建 installer）：

```bash
git pull && bash install.sh            # 交互默认走 source 通道
# 或显式：bash install.sh --channel source --dir /opt/llm-gateway-go
```

在**已有安装目录**重跑 `install` 即为更新语义：`.env` 自动复用（见上文
「升级与全新安装的区别」）；数据库走增量迁移；镜像 tag 是版本物——环境变量
`APP_IMAGE_TAG` 给出新值时会覆盖 `.env` 里的旧 tag（否则「更新」会拉回旧镜像）。

---

## 回滚操作

```bash
./bin/llm-gw-installer upgrade --action rollback
```

回滚到上一个版本（`upgrader.Rollback`）。离线升级的备份在
`<dataDir>/backups/upgrades/`（含二进制/compose/.env 三件套，可整体手工恢复）；
在线升级的备份是 `<dataDir>/backups/<版本>.backup` 单文件二进制。

---

## 常见问题

**Q：升级会重置我的配置吗？**
不会。`.env`、`settings_kv` 里的用户配置在所有升级通道都保留；种子数据全部
`ON CONFLICT DO NOTHING`。只有显式 `install --fresh-config` 或
`uninstall --purge` 才会丢弃配置。

**Q：升级会重建数据库吗？**
不会。全链没有 `CREATE DATABASE`/`DROP DATABASE`；已有库只应用 pending
迁移（增量），全新库才铺基线三件。

**Q：`upgrade apply` 报「检测到 compose.yml」？**
你的安装是 docker compose 形态，请用离线包通道（见上文），加 `--offline
--offline-package`。

**Q：升级包里的迁移会不会重复执行？**
包内迁移按「每个迁移幂等」的通道契约执行，已应用的会安全重放为 no-op；
每个迁移尾部自登记 `schema_migrations`，重放只刷新 description。

**Q：如何确认升级成功？**
`curl http://localhost:8781/healthz`（端口按实际配置）返回的 `git_sha`/
`build_seq` 应为新版本；`app/VERSION` 文件同步更新。

---

## 相关文档

- [installer/README.md](../../../../../installer/README.md) — 子命令、安装与更新的区别、五点同步纪律
- [docs/db-changelog.md](../../../db-changelog.md) — 迁移登记册（正本）
- [deploy/README.md](../../../../../deploy/README.md) — 内部部署通道导航

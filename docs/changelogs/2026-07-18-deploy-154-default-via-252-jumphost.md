# 2026-07-18 — 154 部署默认使用 252 跳板机

## 背景

154 生产服务器公网 IP（47.97.111.154:25022）存在偶发网络抖动，导致部署时 SSH 直连超时。现有机制是"先直连 2 次失败后再切跳板机"，但这增加了部署时间和失败率。

## 修改内容

### 1. SSH 连接策略调整

**修改前**：
- 154 部署先尝试直连 2 次（每次 20s 超时）
- 失败后才切换到 252 跳板机（ProxyCommand）
- 如果直连成功但网络不稳定，后续操作仍可能超时

**修改后**：
- 154 部署**默认立即使用 252 跳板机**（`SSH_RETRY_FALLBACK_AFTER=0`）
- 跳过直连尝试，直接走稳定路径：`本机 → 252:25022 → 154`
- 提供 `--direct` 参数支持应急直连（当 252 不可达时）

### 2. 代码变更

#### `scripts/deploy-lib/ssh-retry.sh`

1. **修改默认值**（行 39-45）：
   ```bash
   # 修改前
   : "${SSH_RETRY_FALLBACK_AFTER:=2}"  # 直连失败 2 次后切跳板机
   
   # 修改后
   : "${SSH_RETRY_FALLBACK_AFTER:=0}"  # 154 默认立即使用跳板机
   ```

2. **增加直连模式开关**（行 118-137）：
   ```bash
   _ssh_retry_fallback_hop() {
     local target=$1
     # 直连模式：跳过跳板机（应急场景）
     if [[ "${SSH_RETRY_DIRECT_MODE:-0}" == "1" ]]; then
       printf ''
       return
     fi
     case "$target" in
       154) printf 'root@115.29.212.252' ;;  # 252 跳板机
       *)   printf '' ;;
     esac
   }
   ```

#### `scripts/deploy-seamless.sh`

增加 `--direct` 参数解析（行 54-69）：
```bash
--direct) export SSH_RETRY_DIRECT_MODE=1; shift ;;
```

更新帮助文档，说明 154 默认使用跳板机。

#### `scripts/deploy-154.sh`

重写为委托脚本（23 行，原 513 行备份为 `.legacy`）：
```bash
#!/usr/bin/env bash
# 默认通过 252 跳板机连接
# --direct 参数支持应急直连
exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 154 "$@"
```

### 3. 使用方式

| 场景 | 命令 | SSH 路径 |
|---|---|---|
| **常规部署（推荐）** | `bash scripts/deploy-154.sh` | 本机 → 252 → 154 |
| **指定版本** | `bash scripts/deploy-154.sh --seq 1143` | 本机 → 252 → 154 |
| **应急直连** | `bash scripts/deploy-154.sh --direct` | 本机 → 154（跳过 252） |
| **回滚** | `bash scripts/deploy-seamless.sh rollback 154` | 本机 → 252 → 154 |

## 验证结果

- ✅ **脚本语法**：`bash -n scripts/deploy-154.sh` 通过
- ✅ **帮助文档**：`bash scripts/deploy-154.sh --help` 显示正确
- ✅ **委托调用**：正确传递参数到 `deploy-seamless.sh`
- ✅ **向后兼容**：旧脚本保留为 `deploy-154.sh.legacy`

## 影响范围

- **不影响** 245 部署（245 无跳板机，继续直连）
- **不影响** 154 已部署的服务（仅改部署脚本行为）
- **不影响** 手动 SSH 连接（env 变量仅在脚本内生效）

## 优势

1. **部署更快**：跳过直连重试，立即走跳板机（节省 ≤40s）
2. **成功率更高**：避免公网 IP 抖动导致的 SSH 超时
3. **保留应急通道**：`--direct` 参数支持 252 不可达时的直连

## 遗留问题

- 252 跳板机成为 154 部署的单点依赖（可通过 `--direct` 绕过）
- 252 本身需要高可用保障（建议监控 252 SSH 服务状态）

## 相关文件

- `scripts/deploy-lib/ssh-retry.sh` — SSH 重试逻辑与跳板机路由
- `scripts/deploy-seamless.sh` — 无缝部署主流程
- `scripts/deploy-154.sh` — 154 部署入口（委托脚本）
- `scripts/deploy-154.sh.legacy` — 旧版 154 部署脚本（513 行，备份）

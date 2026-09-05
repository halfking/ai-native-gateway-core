# 环境变量自动加载使用指南

## 快速开始

### 新脚本模板

```bash
#!/usr/bin/env bash
# scripts/your-script.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[[ -r "$SCRIPT_DIR/lib/load-env.sh" ]] && source "$SCRIPT_DIR/lib/load-env.sh"

# 脚本主体
echo "HOST_71_IP=$HOST_71_IP"
docker exec "$DB_CONTAINER" psql -U "$DB_USER" ...
```

### 部署环境变量到服务器

```bash
bash deploy/sync-env.sh 71   # 71 服务器
bash deploy/sync-env.sh 184  # 184 服务器
```

### 更新环境变量

```bash
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
sops .env.71.enc             # 编辑
bash deploy/sync-env.sh 71   # 部署
ssh root@71 'bash -l -c "echo $HOST_71_IP"'  # 验证
```

## 加载优先级

1. 服务器: `__SERVER_PATH_3__/ops-env.sh` (最高)
2. 本地: `.env.local` (本地开发)
3. SOPS: `.env.71.enc` 解密 (fallback)

## 可用变量

71 服务器: `HOST_71_IP`, `DB_CONTAINER`, `DB_USER`, `DB_PASSWORD`, `REGISTRY`, `PGPASSWORD`...
184 服务器: `INTERNAL_PUBLIC_IP`, `DB_K8S_NAMESPACE`, `DB_K8S_DEPLOY`, `DB_USER`, `DB_PASSWORD`...

## 故障排查

```bash
# 变量为空
grep 'load-env' scripts/your-script.sh
source scripts/lib/load-env.sh
echo $HOST_71_IP

# SOPS 解密失败
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
sops -d .env.71.enc | head -5

# 重新部署
bash deploy/sync-env.sh 71
```

## 安全性

- 明文配置 (`.env.local`, `.env.71`) 通过 `.gitignore` 排除
- 加密配置 (`.env.*.enc`) 使用 SOPS + age 加密，可安全提交
- 服务器配置 (`__SERVER_PATH_3__/ops-env.sh`) 权限 600
- age 私钥 (`~/.config/sops/age/keys.txt`) 仅开发者本地持有

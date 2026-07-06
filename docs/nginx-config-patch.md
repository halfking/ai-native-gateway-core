# 56 Nginx 配置修改方案 - 双活负载均衡

## 当前配置

```nginx
upstream llm-backend {
    keepalive 32;
    keepalive_requests 1000;
    keepalive_timeout 60s;
    server 172.31.0.3:8781 max_fails=2 fail_timeout=10s;
}
```

## 修改后配置

```nginx
# ============================================================================
# llm.kxpms.cn 后端池 (2026-07-06 双活架构)
# ============================================================================
# - 71: docker llm-gateway-go 8781，独立 PG (172.31.0.3:5432)
# - 184: k8s llm-gateway-go NodePort 10023，独立 PG (k8s service)
# - sticky key 优先级: X-Gw-Session-Id > X-Device-Seed > Authorization
# - 数据库隔离：71 和 184 各自独立，数据不同步（双活特性）
# ============================================================================

# === Sticky key 计算 map（在 http {} 块内，upstream 之前）===
map $http_x_gw_session_id $sticky_gw_session {
    default $http_x_gw_session_id;
    ""      "";
}

map $http_x_device_seed $sticky_device {
    default $http_x_device_seed;
    ""      "";
}

map $http_authorization $sticky_auth {
    ~^Bearer\s+(?<key>[A-Za-z0-9_\-]+) $key;
    default "";
}

# 多层级 sticky key 拼接（从细到粗）
map "$sticky_gw_session:$sticky_device:$sticky_auth:$remote_addr" $sticky_key {
    ~^([^:]+)::          $1;          # 1. 优先 X-Gw-Session-Id
    ~^:([^:]+)::         $1;          # 2. X-Device-Seed
    ~^::([^:]+):[^:]+$   $1;          # 3. Authorization (API Key)
    default              $remote_addr;# 4. 最终兜底（按 IP）
}

# === upstream 定义 ===
upstream llm-backend {
    # 基于多层级 sticky key 做一致性 hash
    hash $sticky_key consistent;

    keepalive 32;
    keepalive_requests 1000;
    keepalive_timeout 60s;

    # 71: docker 直连 8781，独立数据库
    server 172.31.0.3:8781 max_fails=3 fail_timeout=15s;
    
    # 184: k8s NodePort 10023，独立数据库
    server 172.31.0.4:10023 max_fails=3 fail_timeout=15s;
}
```

## 实施步骤

### Step 1: 备份当前配置

```bash
ssh root@14.103.169.56 -p 25022 <<'EOF'
cp /etc/nginx/conf.d/kxpms-cn-all-vhosts.conf \
   /etc/nginx/conf.d/kxpms-cn-all-vhosts.conf.bak-$(date +%Y%m%d-%H%M%S)
EOF
```

### Step 2: 找到 http {} 块的位置

需要在 `http {` 块内、所有 `upstream` 之前插入 map 定义。

### Step 3: 修改配置

由于配置文件较大，建议：
1. 先下载到本地
2. 本地编辑
3. 上传并 reload

### Step 4: 验证并重载

```bash
ssh root@14.103.169.56 -p 25022 <<'EOF'
# 验证语法
nginx -t

# 如果验证通过，reload
if [ $? -eq 0 ]; then
  nginx -s reload
  echo "✅ nginx 已 reload"
else
  echo "❌ nginx 配置有误，未 reload"
  exit 1
fi
EOF
```

## 关键注意事项

1. **map 定义必须在 http {} 块内**，不能在 server {} 或 location {} 内
2. **map 定义必须在 upstream 之前**
3. **不要影响现有的其他 upstream**（如 llmgo-backend）
4. **max_fails 从 2 改为 3**，fail_timeout 从 10s 改为 15s（更保守）

## 预期效果

- ✅ Web 端（带 X-Device-Seed）：同一设备固定到同一后端
- ✅ OpenCode/Cursor（带 Authorization）：同一 API Key 固定到同一后端
- ✅ 健康检查：后端失败自动剔除
- ✅ 数据隔离：71 和 184 各自独立

## 回滚方案

```bash
ssh root@14.103.169.56 -p 25022 <<'EOF'
cp /etc/nginx/conf.d/kxpms-cn-all-vhosts.conf.bak-<timestamp> \
   /etc/nginx/conf.d/kxpms-cn-all-vhosts.conf
nginx -t && nginx -s reload
EOF
```
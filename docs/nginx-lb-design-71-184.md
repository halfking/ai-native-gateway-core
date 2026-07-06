# 56 Nginx 负载均衡最终方案：71 + 184 双活架构（数据库隔离 + 多终端 Sticky）

**版本**：v2.0（最终版）  
**创建时间**：2026-07-06  
**状态**：✅ 已调研完成，待评审

---

## 1. 调研结论（重要前置）

### 1.1 三个终端的会话 ID 现状

| 终端 | 是否发 X-Device-Seed | 可用 Sticky 标识 | Sticky 粒度 | 备注 |
|------|---------------------|------------------|------------|------|
| **Web 前端** | ✅ 是（`localStorage` UUID） | `X-Device-Seed`（设备级）+ `X-Gw-Session-Id`（会话级） | **设备级** | `crypto.randomUUID()` 浏览器持久化 |
| **OpenCode CLI** | ❌ 否 | `Authorization: Bearer <key>` + OpenAI body 内 `metadata.session_id` | **账号级**（依赖 body 字段） | 标准 OpenAI SDK 不暴露自定义 header |
| **ZCode CLI** | ❌ 否 | 同 OpenCode（取决于 metadata） | **账号级** | 同 OpenCode |
| **Cursor IDE** | ❌ 否 | `X-Client-Profile: cursor` + API Key | **客户端级 + 账号级** | Cursor 是 OpenAI 兼容代理 |

### 1.2 关键结论

**单靠 X-Device-Seed 不够**，因为：
1. OpenCode/ZCode/Cursor 都不发 X-Device-Seed
2. 用户实际使用场景以 CLI 工具（OpenCode/ZCode）为主，需要支持
3. 但 Web 端是 sticky 体验最好的，应该优先用 X-Device-Seed

**采用多维度 fallback sticky 策略**（nginx `map` 实现）：

```
优先级（从高到低）：
1. X-Gw-Session-Id    (Web 端会话级，最细粒度)
2. X-Device-Seed      (Web 端设备级，浏览器级稳定)
3. X-Client-Profile   (Cursor 等 IDE 终端)
4. Authorization      (API Key，所有终端通用账号级 sticky)
5. $binary_remote_addr (最终兜底，按 IP 随机)
```

### 1.3 数据库现状

- **71 上已有 PostgreSQL 容器**（`pgvector` + `postgres`，监听 `127.0.0.1:5432`，外部映射 `5435`）
- **schema 与 184 完全一致**（参考 `scripts/sync-llm-pg-to-71.sh`，19 张表结构相同）
- **当前 71 的 llm-gateway-go 仍连 184 的 PG**（`172.31.0.4:5432`），不符合隔离要求

---

## 2. 完整架构设计

### 2.1 目标架构

```
用户终端（Web / OpenCode / ZCode / Cursor）
  ↓ 任意 header: X-Device-Seed / X-Gw-Session-Id / X-Client-Profile / Authorization
  ↓ https://llm.kxpms.cn/v1
56 nginx (14.103.169.56) — 统一入口
  ↓ hash $sticky_key consistent; (多层级 sticky)
  ├─→ 71 llm-gateway-go (172.31.0.3:8781, docker)
  │    └─→ 71 PostgreSQL (127.0.0.1:5432, 独立实例)
  │
  └─→ 184 llm-gateway-go (172.31.0.4:10023, k8s NodePort)
       └─→ 184 PostgreSQL (172.31.0.4:5432, k8s hostNetwork)
```

### 2.2 关键设计原则

1. **不出错不切换**：一致性 hash + `max_fails=3 fail_timeout=15s`，只有后端真故障才切换
2. **故障及时转移**：nginx 内置被动健康检查 + 主动 `/healthz` check
3. **故障自动恢复**：后端恢复后，通过慢启动 + retry-after 让流量回流
4. **数据隔离**：71 与 184 PG 独立，互不依赖
5. **零停机**：滚动升级

---

## 3. Nginx 完整配置

### 3.1 配置修改位置

**文件**：`/Users/xutaohuang/workspace/official-deploy/services/nps_new/deploy/56/nginx/kxpms-cn-all-vhosts.conf`

**修改点 1：upstream 定义（约 Line 130）**

**修改前**（单点 71）：

```nginx
upstream llm-backend {
    keepalive 32;
    keepalive_requests 1000;
    keepalive_timeout 60s;
    server 172.31.0.3:8781 max_fails=2 fail_timeout=10s;
}
```

**修改后**（双活 + 多层级 sticky）：

```nginx
# ============================================================================
# llm.kxpms.cn 后端池 (2026-07-06 双活架构)
# ============================================================================
# - 71: docker + systemd 直连 8781，DB=127.0.0.1:5432 (71 本地 PG)
# - 184: k8s NodePort 10023，DB=172.31.0.4:5432 (184 k8s PG)
# - sticky key 优先级: X-Gw-Session-Id > X-Device-Seed > X-Client-Profile > Authorization
# - 一致性 hash + 主动健康检查，故障自动转移与恢复
# ============================================================================

# Sticky key 计算 map (在 http {} 块内)
map $http_x_gw_session_id $sticky_gw_session {
    default $http_x_gw_session_id;
    ""      "";
}
map $http_x_device_seed $sticky_device {
    default $http_x_device_seed;
    ""      "";
}
map $http_x_client_profile $sticky_client {
    default $http_x_client_profile;
    ""      "";
}
map $http_authorization $sticky_auth {
    ~^Bearer\s+(?<key>[A-Za-z0-9_\-]+) $key;
    default "";
}

# 多层级 sticky key 拼接（从细到粗）
map "$sticky_gw_session:$sticky_device:$sticky_client:$sticky_auth:$remote_addr" $sticky_key {
    ~^([^:]+)::          $1;          # 1. 优先 X-Gw-Session-Id
    ~^:([^:]+)::         $1;          # 2. X-Device-Seed
    ~^::([^:]+)::        $1;          # 3. X-Client-Profile
    ~^::([^:]+):[^:]+$   $1;          # 4. Authorization
    default              $remote_addr;# 5. 最终兜底（按 IP）
}

upstream llm-backend {
    # 基于多层级 sticky key 做一致性 hash
    hash $sticky_key consistent;

    keepalive 32;
    keepalive_requests 1000;
    keepalive_timeout 60s;

    # ── 主动健康检查（10 秒间隔） ─────────────────────────────
    # nginx-plus 才能用 health_check；开源版用 max_fails 被动检测
    # 71 端口: 8781 (docker 直连)
    server 172.31.0.3:8781 max_fails=3 fail_timeout=15s;
    # 184 端口: 10023 (k8s NodePort)
    server 172.31.0.4:10023 max_fails=3 fail_timeout=15s;
}
```

**修改点 2：在 server 块加入 proxy_next_upstream 优化**

现有的 `server { ... server_name llm.kxpms.cn; ... }` (Line 944-991 和 993-1039) 保持不变，但需要确保 location 块有正确的超时和重试配置（已有，无需修改）。

### 3.2 关键配置解释

#### 3.2.1 多层级 Sticky Key 设计

```
请求类型                       → 实际 sticky key
─────────────────────────────────────────────────────
Web 端带 X-Gw-Session-Id      → "gw_abc123..."
Web 端带 X-Device-Seed        → "device-uuid-xxx..."
Cursor 带 X-Client-Profile    → "cursor"
OpenCode CLI（无任何 header）  → "sk-1vH6C2I9p..." (API key)
最终兜底                      → 客户端 IP
```

#### 3.2.2 一致性 Hash 算法

`hash $sticky_key consistent;`：
- 使用 ketama 一致性 hash 算法
- 同一 sticky key 始终映射到同一后端
- 后端增减时，仅影响 `1/N` 用户（N 为后端数）

#### 3.2.3 健康检查

```
max_fails=3 fail_timeout=15s
```

- **触发**：连续 3 次请求失败（connection error / timeout / 5xx）
- **失效**：将该后端从 active pool 移除
- **恢复**：15 秒后重新尝试（标记为 up）
- **流量转移**：一致性 hash 自动将流量切到健康后端

**注意**：开源 nginx 没有主动 health check，只能靠业务请求触发被动检测。生产建议：
- 加一个外部 cron（每 10s `curl /healthz`），如果连续失败则通过 API 摘流（高级方案，初期不需要）
- 或者使用 nginx-plus / openresty

---

## 4. 数据库隔离实施

### 4.1 71 PostgreSQL 现状

根据 `scripts/pg-184-to-71/setup-71-pg.sh`，71 上已有：
- 容器名：`$PG71_CONTAINER`（变量定义，默认应该是 `llm-pg-71`）
- 监听：`127.0.0.1:5432`（外部 `5435`）
- 镜像：`docker.m.daocloud.io/ankane/pgvector:latest`
- 已有数据库：`postgres`（默认）
- volume：`/data/postgres-llm`

### 4.2 隔离步骤

#### Step 1: 创建 `llm_gateway` 数据库（71 上）

```bash
# 在 71 上执行
ssh root@14.103.174.71 <<'EOF'
docker exec -i llm-pg-71 psql -U kxuser -d postgres <<SQL
CREATE DATABASE llm_gateway;
GRANT ALL PRIVILEGES ON DATABASE llm_gateway TO kxuser;
CREATE USER llm_gateway WITH PASSWORD '<从 SOPS 取>';
GRANT ALL PRIVILEGES ON DATABASE llm_gateway TO llm_gateway;
SQL
EOF
```

#### Step 2: 导入 schema（仅结构，不导入业务数据）

```bash
# 从 184 导出 schema
ssh root@14.103.112.184 <<'EOF'
kubectl exec -n pms-test deploy/llm-gateway-pg-deployment -- \
  pg_dump -U llm_gateway -d llm_gateway --schema-only --no-owner --no-acl
EOF
> /tmp/llm_gw_schema.sql

# 导入到 71
ssh root@14.103.174.71 "docker exec -i llm-pg-71 psql -U llm_gateway -d llm_gateway" < /tmp/llm_gw_schema.sql
```

#### Step 3: 初始化业务基线数据

由于 71 和 184 是**独立双活**，基线数据应该相同：
- `providers` / `models_canonical` / `pricing_plans` 等基础配置表 → **两边各放一份相同的数据**（参考 `sync-llm-pg-to-71.sh` 的 SYNC_TABLES 列表）
- `api_keys` / `users` 等用户数据 → **各自独立**（每个节点是独立站点）
- `request_logs` / `audit` 等日志 → **各自独立**

#### Step 4: 修改 71 env 配置

**文件**：`/etc/llm-gateway-go/env`（71 上）

```bash
# 修改前
LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@172.31.0.4:5432/llm_gateway

# 修改后
LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@127.0.0.1:5432/llm_gateway
```

**注意**：使用 `127.0.0.1:5432` 而不是 `172.31.0.3:5432`，避免端口冲突。

#### Step 5: 重启 71 llm-gateway-go

```bash
ssh root@14.103.174.71 <<'EOF'
# 移除 chattr +i 锁（如果有）
chattr -i /etc/llm-gateway-go/env

# 更新 env
sed -i 's|@172.31.0.4:5432|@127.0.0.1:5432|g' /etc/llm-gateway-go/env

# 重新加锁
chattr +i /etc/llm-gateway-go/env

# 重启服务
systemctl restart llm-gateway-go.service

# 等待就绪
sleep 10
curl -s http://127.0.0.1:8781/healthz
EOF
```

### 4.3 验证隔离

```bash
# 71 上查询
ssh root@14.103.174.71 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c 'SELECT count(*) FROM api_keys;'"

# 184 上查询
ssh root@14.103.112.184 "kubectl exec -n pms-test deploy/llm-gateway-pg-deployment -- psql -U llm_gateway -d llm_gateway -c 'SELECT count(*) FROM api_keys;'"

# 预期：两边各有一组独立的 api_keys（基线相同，业务数据各自独立）
```

---

## 5. 故障转移与自动恢复

### 5.1 故障检测机制

#### 5.1.1 nginx 被动健康检查

```
max_fails=3 fail_timeout=15s
```

**触发条件**（任意一种）：
1. `connection refused`（后端进程崩溃）
2. `connection timeout`（超过 `proxy_connect_timeout`）
3. HTTP 5xx 响应
4. HTTP 502/503/504（网关错误）

**判定**：在 `fail_timeout=15s` 窗口内累计 `max_fails=3` 次失败 → 标记为 unhealthy

#### 5.1.2 主动健康检查（建议增强）

虽然开源 nginx 不支持主动 health check，但可以通过外部 cron 实现：

**方案 A：cron + 状态文件**

```bash
# /usr/local/bin/check-llm-health.sh
#!/bin/bash
set -euo pipefail

NODE71_UP="/tmp/llm-node71-up"
NODE184_UP="/tmp/llm-node184-up"

# 检测 71
if ! curl -sf --max-time 5 http://172.31.0.3:8781/healthz > /dev/null; then
  if [ ! -f "$NODE71_DOWN" ]; then
    echo "[$(date)] 71 DOWN" >> /var/log/llm-health.log
    touch "$NODE71_DOWN"
  fi
else
  rm -f "$NODE71_DOWN"
fi

# 检测 184
if ! curl -sf --max-time 5 http://172.31.0.4:10023/healthz > /dev/null; then
  if [ ! -f "$NODE184_DOWN" ]; then
    echo "[$(date)] 184 DOWN" >> /var/log/llm-health.log
    touch "$NODE184_DOWN"
  fi
else
  rm -f "$NODE184_DOWN"
fi
```

```bash
# 添加到 56 crontab
* * * * * /usr/local/bin/check-llm-health.sh
```

**方案 B：使用 consul / consul-template + nginx（高级方案）**

如果未来需要更精细的流量控制，可以：
1. 部署 consul（71/184 各注册一个 service）
2. consul-template 自动生成 nginx upstream 配置
3. 健康检查失败自动从 upstream 摘除

### 5.2 故障转移流程

```
故障发生
  ↓
nginx 检测到 max_fails=3
  ↓
从 upstream active pool 移除故障节点
  ↓
新请求通过一致性 hash 路由到健康节点
  ↓
故障节点进入 15s 冷却期
  ↓
15s 后重新探测
  ↓
如果恢复 → 重新加入 active pool
  ↓
新用户可能路由到恢复的节点（已有 sticky 的不迁移）
```

### 5.3 自动恢复机制

**核心**：nginx 的 `fail_timeout=15s` 就是自动恢复机制。

```nginx
server 172.31.0.3:8781 max_fails=3 fail_timeout=15s;
#                  ↑                ↑
#                  │                └─ 15s 后重试
#                  └─ 连续失败 3 次才标记为 down
```

**慢启动建议**（可选，nginx 默认无慢启动）：

如果担心恢复瞬间被打爆，可以在 upstream 块加 `slow_start`（需要 nginx-plus）或在应用层做 token bucket 限流。

### 5.4 故障演练方案

```bash
# 1. 模拟 71 故障
ssh root@14.103.174.71 "systemctl stop llm-gateway-go.service"

# 2. 验证 56 nginx 自动切换
for i in {1..10}; do
  curl -H "X-Device-Seed: test" -w "%{http_code} " \
    -o /dev/null -s https://llm.kxpms.cn/healthz
done
# 预期：所有请求仍然 200（已切换到 184）

# 3. 查看 nginx 错误日志（应记录 71 connection refused）
ssh root@14.103.169.56 "tail -20 /var/log/nginx/error.log | grep '172.31.0.3'"

# 4. 恢复 71
ssh root@14.103.174.71 "systemctl start llm-gateway-go.service"

# 5. 等待 15s 后验证
sleep 20
curl -H "X-Device-Seed: test" https://llm.kxpms.cn/healthz
# 预期：71 已恢复，新请求可能路由到 71
```

---

## 6. 端到端验证清单

### 6.1 基础设施验证

- [ ] **端口可达性**：
  ```bash
  ssh root@14.103.169.56 "curl -s http://172.31.0.3:8781/healthz && echo ' ← 71 OK'"
  ssh root@14.103.169.56 "curl -s http://172.31.0.4:10023/healthz && echo ' ← 184 OK'"
  ```

- [ ] **数据库就绪**：
  ```bash
  ssh root@14.103.174.71 "docker exec llm-pg-71 pg_isready -U llm_gateway"
  ```

### 6.2 配置生效验证

- [ ] **nginx 配置语法**：
  ```bash
  ssh root@14.103.169.56 "nginx -t"
  ```

- [ ] **upstream 已生效**：
  ```bash
  ssh root@14.103.169.56 "curl -s 'https://llm.kxpms.cn/api/system/version' -H 'Authorization: Bearer <admin-key>' | jq ."
  # 预期返回 node 字段标识当前节点
  ```

### 6.3 Sticky 验证

- [ ] **Web 端 sticky**：
  ```bash
  # 同一 X-Device-Seed 多次请求，应固定到同一节点
  for i in {1..5}; do
    curl -H "X-Device-Seed: test-web-001" \
      -H "Authorization: Bearer <key>" \
      -s https://llm.kxpms.cn/api/system/version | jq -r '.node'
  done
  ```

- [ ] **OpenCode/Cursor sticky**：
  ```bash
  # 同一 Authorization (API Key) 多次请求，应固定到同一节点
  for i in {1..5}; do
    curl -H "Authorization: Bearer <key>" \
      -s https://llm.kxpms.cn/api/system/version | jq -r '.node'
  done
  ```

### 6.4 负载分布验证

```bash
# 查看 nginx access log 的 upstream_addr
ssh root@14.103.169.56 "tail -1000 /var/log/nginx/access.log | grep 'llm.kxpms.cn' | awk '{for(i=1;i<=NF;i++) if(\$i ~ /^upstream_addr/) print \$i}' | sort | uniq -c"

# 预期：71 和 184 的请求数大致接近（取决于 hash 分布）
```

### 6.5 数据库隔离验证

- [ ] **独立 schema**：两边都有相同的 19 张表
- [ ] **独立数据**：71 的 api_keys 和 184 的 api_keys 互不影响
- [ ] **独立日志**：71 的 request_logs 和 184 的 request_logs 互不影响

### 6.6 故障转移验证

- [ ] **手动停 71**，验证流量切到 184
- [ ] **手动停 184**，验证流量切到 71
- [ ] **恢复 71/184**，验证 15s 后自动恢复

---

## 7. 运维建议

### 7.1 监控指标

| 指标 | 命令 | 告警阈值 |
|------|------|----------|
| 71 llm-gateway-go 健康 | `curl http://172.31.0.3:8781/healthz` | 连续 3 次 5xx |
| 184 llm-gateway-go 健康 | `curl http://172.31.0.4:10023/healthz` | 连续 3 次 5xx |
| 71 PG 磁盘使用 | `df -h /data/postgres-llm` | > 80% |
| nginx upstream 状态 | 看 access log 的 `$upstream_addr` | 单节点 5xx > 1% |
| 数据库连接数 | `psql -c 'SELECT count(*) FROM pg_stat_activity;'` | > 80% max_connections |

### 7.2 日常维护

1. **定期备份数据库**（71 和 184 各自）：
   ```bash
   # 71
   ssh root@14.103.174.71 "docker exec llm-pg-71 pg_dumpall -U llm_gateway" > /backup/llm-pg-71-$(date +%Y%m%d).sql
   
   # 184（已有流程，继续执行）
   ```

2. **监控两边数据漂移**（可选）：
   ```bash
   # 比较 api_keys 表的关键指标（如总数）
   ```

3. **定期清理日志**：
   ```bash
   # 71/184 各自的 logrotate
   ```

### 7.3 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| **API Key 在 71 和 184 都存在** | 用户在两边都能登录 | 同步基线 api_keys 到两边（参考 sync-llm-pg-to-71.sh） |
| **用户两边数据不同步** | 用户在 71 创建的 session 在 184 看不到 | **接受这个限制**（双活架构本来就是这样） |
| **后端 OOM 导致频繁重启** | 健康检查抖动 | 加监控 + 设置资源 limits |
| **nginx 配置错误** | 整个站点挂掉 | `nginx -t` 验证 + 保留备份 + 金丝雀发布 |

---

## 8. 实施时间表

| 阶段 | 任务 | 预计时间 | 影响 |
|------|------|----------|------|
| **准备** | 备份现有配置 | 5 min | 无 |
| **DB 隔离** | 71 创建 PG + 初始化 schema + 导入基线 | 30 min | 71 短暂不可用 |
| **env 修改** | 修改 71 env + 重启 | 10 min | 71 短暂不可用 |
| **nginx 配置** | 修改 upstream + map + reload | 10 min | 无（reload 优雅） |
| **验证** | 6 节所有清单 | 20 min | 无 |
| **总耗时** | | **约 75 分钟** | **零停机** |

---

## 9. 回滚方案

### 9.1 数据库回滚

```bash
ssh root@14.103.174.71 <<'EOF'
chattr -i /etc/llm-gateway-go/env
sed -i 's|@127.0.0.1:5432|@172.31.0.4:5432|g' /etc/llm-gateway-go/env
chattr +i /etc/llm-gateway-go/env
systemctl restart llm-gateway-go.service
EOF
```

### 9.2 nginx 回滚

```bash
ssh root@14.103.169.56 <<'EOF'
cp /etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf.bak-<timestamp> \
   /etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf
nginx -t
nginx -s reload
EOF
```

**回滚时间**：< 5 分钟

---

## 10. 关键决策总结

### ✅ 已采用的设计

1. **多层级 sticky key**（解决 OpenCode/Cursor 不发 X-Device-Seed 的问题）
2. **一致性 hash**（最小化后端增减对用户的影响）
3. **`max_fails=3 fail_timeout=15s`**（被动健康检查 + 自动恢复）
4. **数据库物理隔离**（71 独立 PG 容器 + 184 k8s PG）
5. **主动健康检查 cron**（外部 cron 监控，及时发现）

### ⚠️ 用户需要权衡的点

1. **数据不共享**：用户在 71 创建的内容在 184 看不到（双活的代价）
   - 缓解：基线配置数据同步，业务数据各自独立
2. **API Key 需要两边都创建**：用户需要在 71 和 184 各有一个 api_key
   - 缓解：提供 CLI 脚本自动创建
3. **监控成本增加**：需要监控 2 个节点而不是 1 个

### 📌 建议先小流量验证

1. 先在测试环境部署（按 `local-deploy-test` skill）
2. 验证通过后再上生产
3. 生产部署建议分两步：
   - **Phase 1**：先开 nginx load balancing（数据库仍共享），验证 sticky 是否生效
   - **Phase 2**：再切数据库隔离

---

**下一步**：等待用户确认方案后开始实施。
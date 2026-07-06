# 56 Nginx 负载均衡设计：71 + 184 双活架构（数据库隔离）

**创建时间**：2026-07-06  
**状态**：设计方案  
**作者**：OpenCode (Claude Sonnet 4)

---

## 1. 需求分析

### 1.1 用户需求

1. **修正 56 上的 nginx**，让 71 与 184 负载均衡
2. **以前端的特性来固定链路**，不出错不要变更链路（sticky session）
3. **将 71 与 184 上的数据库进行隔离**，不互通
4. 相当于两个完全一样的站点提供服务（双活架构）

### 1.2 当前架构

#### 网络拓扑

```
用户浏览器
  ↓ llm.kxpms.cn (HTTPS)
56 nginx (14.103.169.56) — 网关服务器
  ↓ proxy_pass http://llm-backend
  ↓ (当前只有一个后端)
71 llm-gateway-go (172.31.0.3:8781)
  ↓ 连接 PG
184 PostgreSQL (172.31.0.4:5432) ← 71 和 184 共享此数据库 ❌
```

#### 问题

1. **56 nginx 单点路由**：`llm-backend` upstream 只有 `172.31.0.3:8781`（71），没有负载均衡
2. **数据库共享**：71 和 184 都连接 184 上的 PostgreSQL（`172.31.0.4:5432`），不符合"数据库隔离"需求
3. **无 sticky session**：缺少基于前端特性的路由固定机制

---

## 2. 架构设计

### 2.1 目标架构

```
用户浏览器
  ↓ llm.kxpms.cn (HTTPS)
  ↓ X-Device-Seed: <uuid> (localStorage 持久化)
56 nginx (14.103.169.56) — 网关服务器
  ↓ hash $http_x_device_seed consistent; (一致性 hash)
  ├─→ 50% 流量 → 71 llm-gateway-go (172.31.0.3:8781)
  │    ↓ LLM_GATEWAY_DATABASE_URL
  │    ↓ @172.31.0.3:5432 (71 本地 PG，独立数据库)
  │
  └─→ 50% 流量 → 184 llm-gateway-go (172.31.0.4:10023)
       ↓ LLM_GATEWAY_DATABASE_URL
       ↓ @172.31.0.4:5432 (184 k8s PG，独立数据库)
```

### 2.2 关键技术点

#### 2.2.1 前端特性识别

**前端代码**（`web/src/api/auth.ts:52-53`）：

```javascript
const deviceSeed = localStorage.getItem('llmgw_device_seed') ?? 'default'
headers['X-Device-Seed'] = deviceSeed
```

- 每个浏览器有唯一的 `llmgw_device_seed`（UUID）
- 通过 `X-Device-Seed` header 发送给后端
- 用于 session 粘性路由

#### 2.2.2 Nginx Sticky Session

使用 **一致性 hash** 算法：

```nginx
upstream llm-backend {
    # 基于 X-Device-Seed header 做一致性 hash 分流
    # 同一 device seed 始终路由到同一后端（除非后端下线）
    hash $http_x_device_seed consistent;
    
    keepalive 32;
    keepalive_requests 1000;
    keepalive_timeout 60s;
    
    # 71: docker + systemd 部署，直连 8781
    server 172.31.0.3:8781 max_fails=2 fail_timeout=10s;
    
    # 184: k8s 部署，NodePort 10023
    server 172.31.0.4:10023 max_fails=2 fail_timeout=10s;
}
```

**工作原理**：

1. nginx 对 `$http_x_device_seed` 做 hash，得到一个数值
2. 使用一致性 hash 环（consistent）将该值映射到某个后端
3. 同一 device seed 始终映射到同一后端
4. 当后端数量变化时，只有少部分用户需要重新映射（最小化影响）

#### 2.2.3 健康检查与故障转移

```nginx
server 172.31.0.3:8781 max_fails=2 fail_timeout=10s;
server 172.31.0.4:10023 max_fails=2 fail_timeout=10s;
```

- **max_fails=2**：连续 2 次请求失败，标记后端为不可用
- **fail_timeout=10s**：10 秒后重新尝试该后端
- **故障转移**：当固定后端失败时，nginx 自动切换到健康的后端
- **恢复**：故障后端恢复后，新请求可能路由到它（但已有 sticky 的用户不会迁移）

---

## 3. 数据库隔离方案

### 3.1 当前状态

**71 上的 llm-gateway-go**：

```bash
# /etc/llm-gateway-go/env
LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@172.31.0.4:5432/llm_gateway
```

**184 上的 llm-gateway-go**：

```yaml
# k8s secret
LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@llm-gateway-pg-svc:5432/llm_gateway
# 实际解析为 172.31.0.4:5432 (hostNetwork)
```

**问题**：两者连接同一个 PostgreSQL（184 上的），数据完全共享。

### 3.2 隔离方案

#### 方案 A：71 部署独立 PostgreSQL（推荐）

**步骤**：

1. **在 71 上启动 PostgreSQL 容器**：

   ```bash
   # 71 上执行
   docker run -d \
     --name llm-pg-71 \
     --restart=always \
     -e POSTGRES_USER=llm_gateway \
     -e POSTGRES_PASSWORD=<密码> \
     -e POSTGRES_DB=llm_gateway \
     -v /data/postgres-llm:/var/lib/postgresql/data \
     -p 127.0.0.1:5432:5432 \
     postgres:15-alpine
   ```

2. **初始化 71 数据库 schema**：

   ```bash
   # 从 184 导出 schema（不含数据）
   ssh root@14.103.112.184 "kubectl exec -n pms-test deploy/llm-gateway-pg-deployment -- pg_dump -U llm_gateway -s llm_gateway" > /tmp/schema.sql
   
   # 导入到 71
   ssh root@14.103.174.71 "docker exec -i llm-pg-71 psql -U llm_gateway llm_gateway" < /tmp/schema.sql
   ```

3. **修改 71 的 env 配置**：

   ```bash
   # 71 上 /etc/llm-gateway-go/env
   # 修改前：LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@172.31.0.4:5432/llm_gateway
   # 修改后：
   LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@127.0.0.1:5432/llm_gateway
   ```

4. **重启 71 llm-gateway-go**：

   ```bash
   ssh root@14.103.174.71 "systemctl restart llm-gateway-go.service"
   ```

5. **验证隔离**：

   ```bash
   # 71 上查询
   ssh root@14.103.174.71 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c 'SELECT count(*) FROM api_keys;'"
   
   # 184 上查询
   ssh root@14.103.112.184 "kubectl exec -n pms-test deploy/llm-gateway-pg-deployment -- psql -U llm_gateway -d llm_gateway -c 'SELECT count(*) FROM api_keys;'"
   
   # 两者应该数量不同（各自独立）
   ```

#### 方案 B：使用不同的数据库名（简化方案）

如果不想部署新 PG，可以在同一 PostgreSQL 实例上创建两个独立数据库：

```sql
-- 184 PG 上执行
CREATE DATABASE llm_gateway_71;
GRANT ALL ON DATABASE llm_gateway_71 TO llm_gateway;
```

然后 71 连接 `@172.31.0.4:5432/llm_gateway_71`，184 连接 `@172.31.0.4:5432/llm_gateway`。

**缺点**：
- 仍然依赖 184 的 PG（单点故障）
- 不是真正的物理隔离

**推荐使用方案 A**。

---

## 4. 实施步骤

### 4.1 前置准备

#### 4.1.1 备份当前配置

```bash
# 备份 56 nginx 配置
ssh root@14.103.169.56 "cp /etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf /etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf.bak-$(date +%Y%m%d-%H%M%S)"

# 备份 71 env
ssh root@14.103.174.71 "cp /etc/llm-gateway-go/env /etc/llm-gateway-go/env.bak-$(date +%Y%m%d-%H%M%S)"
```

#### 4.1.2 确认端口可达性

```bash
# 从 56 测试 71:8781
ssh root@14.103.169.56 "curl -s http://172.31.0.3:8781/healthz && echo ' ← 71:8781 OK'"

# 从 56 测试 184:10023
ssh root@14.103.169.56 "curl -s http://172.31.0.4:10023/healthz && echo ' ← 184:10023 OK'"
```

### 4.2 数据库隔离实施

按照 **§3.2 方案 A** 执行：

1. 在 71 上启动 PostgreSQL 容器
2. 初始化 schema
3. 修改 71 env 配置
4. 重启 71 llm-gateway-go
5. 验证隔离

**预计时间**：30 分钟

### 4.3 Nginx 配置修改

#### 4.3.1 修改 upstream 定义

编辑 `/etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf`，找到 `upstream llm-backend` 块（约 Line 130），修改为：

```nginx
# llm.kxpms.cn → 71 + 184 双活负载均衡 (2026-07-06)
# 基于 X-Device-Seed header 做一致性 hash sticky session
# 数据库隔离：71 连 172.31.0.3:5432, 184 连 172.31.0.4:5432
upstream llm-backend {
    # 一致性 hash：同一 device seed 始终路由到同一后端
    hash $http_x_device_seed consistent;
    
    keepalive 32;
    keepalive_requests 1000;
    keepalive_timeout 60s;
    
    # 71: docker + systemd 部署，直连 8781
    # DB: 172.31.0.3:5432 (71 本地 PG)
    server 172.31.0.3:8781 max_fails=2 fail_timeout=10s;
    
    # 184: k8s 部署，NodePort 10023
    # DB: 172.31.0.4:5432 (184 k8s PG)
    server 172.31.0.4:10023 max_fails=2 fail_timeout=10s;
}
```

#### 4.3.2 验证配置语法

```bash
ssh root@14.103.169.56 "nginx -t"
```

#### 4.3.3 重载 Nginx

```bash
ssh root@14.103.169.56 "nginx -s reload"
```

**预计时间**：5 分钟

### 4.4 验证与测试

#### 4.4.1 健康检查

```bash
# 公网访问
curl -I https://llm.kxpms.cn/healthz

# 预期：HTTP/1.1 200 OK
```

#### 4.4.2 Sticky Session 验证

```bash
# 测试 1：同一 device seed 多次请求
for i in {1..10}; do
  curl -H "X-Device-Seed: test-device-001" https://llm.kxpms.cn/api/system/version 2>/dev/null | jq -r '.node // "N/A"'
done

# 预期：10 次请求应该路由到同一节点（71 或 184）

# 测试 2：不同 device seed
curl -H "X-Device-Seed: test-device-001" https://llm.kxpms.cn/api/system/version 2>/dev/null | jq -r '.node'
curl -H "X-Device-Seed: test-device-002" https://llm.kxpms.cn/api/system/version 2>/dev/null | jq -r '.node'

# 预期：可能路由到不同节点（取决于 hash 结果）
```

#### 4.4.3 数据库隔离验证

```bash
# 71 上创建测试 API key
ssh root@14.103.174.71 "docker exec llm-gateway-go psql postgres://llm_gateway:***@127.0.0.1:5432/llm_gateway -c \"INSERT INTO api_keys (key_prefix, encrypted_key, user_id, active) VALUES ('test-71', 'dummy', 1, true) RETURNING id;\""

# 184 上查询
ssh root@14.103.112.184 "kubectl exec -n pms-test deploy/llm-gateway-pg-deployment -- psql -U llm_gateway -d llm_gateway -c \"SELECT id FROM api_keys WHERE key_prefix='test-71';\""

# 预期：184 上查询不到（证明数据库隔离）
```

#### 4.4.4 负载均衡验证

```bash
# 查看 nginx access log，观察请求分布
ssh root@14.103.169.56 "tail -100 /var/log/nginx/access.log | grep 'llm.kxpms.cn' | awk '{print \$NF}' | sort | uniq -c"

# 预期：71 和 184 的请求数大致 50/50 分布（新用户）
```

---

## 5. 风险评估

### 5.1 潜在风险

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| **数据不一致** | 高 | 中 | 71 和 184 各自独立，不共享 session/数据 |
| **已有用户被重新分配** | 中 | 低 | 首次部署后，sticky 会固定每个用户到某个节点 |
| **71 PG 容量不足** | 高 | 低 | 监控 71 磁盘使用率（`/data/postgres-llm`） |
| **Nginx 配置错误** | 高 | 低 | `nginx -t` 验证语法 + 保留备份配置 |
| **71/184 数据漂移** | 中 | 中 | 定期对账检查（非强制） |

### 5.2 回滚方案

如果部署后出现问题：

```bash
# 回滚 56 nginx 配置
ssh root@14.103.169.56 "cp /etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf.bak-<timestamp> /etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf && nginx -s reload"

# 回滚 71 数据库配置
ssh root@14.103.174.71 "cp /etc/llm-gateway-go/env.bak-<timestamp> /etc/llm-gateway-go/env && systemctl restart llm-gateway-go.service"
```

**回滚时间**：< 5 分钟

---

## 6. 运维建议

### 6.1 监控指标

1. **Nginx 负载分布**：
   ```bash
   ssh root@14.103.169.56 "tail -1000 /var/log/nginx/access.log | grep 'llm.kxpms.cn' | awk '{print \$NF}' | sort | uniq -c"
   ```

2. **71 PG 磁盘使用率**：
   ```bash
   ssh root@14.103.174.71 "df -h /data/postgres-llm"
   ```

3. **sticky session 有效性**：
   ```bash
   # 统计同一 device seed 被路由到不同节点的次数（应该 = 0）
   ```

### 6.2 日常维护

1. **71 PG 备份**：
   ```bash
   ssh root@14.103.174.71 "docker exec llm-pg-71 pg_dumpall -U llm_gateway > /backup/llm-pg-71-$(date +%Y%m%d).sql"
   ```

2. **184 PG 备份**（已有流程，继续执行）

3. **定期检查数据漂移**（可选）：
   ```bash
   # 统计 71 和 184 的 api_keys 数量差异
   ```

### 6.3 扩容建议

如果未来需要增加节点（如 252）：

1. 在 56 nginx `llm-backend` upstream 中添加：
   ```nginx
   server 115.29.212.252:8781 max_fails=2 fail_timeout=10s;
   ```

2. 一致性 hash 会自动重新分配部分用户到新节点
3. 影响范围：约 1/3 用户（理论值，取决于 hash 算法）

---

## 7. 附录

### 7.1 相关文件清单

| 文件 | 路径 | 说明 |
|------|------|------|
| **56 nginx 配置** | `/etc/nginx/sites-enabled/kxpms-cn-all-vhosts.conf` | 主配置文件 |
| **71 env** | `/etc/llm-gateway-go/env` | 71 环境变量 |
| **71 systemd unit** | `/etc/systemd/system/llm-gateway-go.service` | 71 服务定义 |
| **184 k8s deployment** | `k8s/apps/base/llm-gateway-go.yaml` | 184 k8s 配置 |
| **部署脚本（71）** | `scripts/deploy-llm-gateway-go-71.sh` | 71 部署脚本 |
| **部署脚本（184）** | `scripts/deploy-llm-gateway-go-184.sh` | 184 部署脚本 |

### 7.2 端口清单

| 服务 | 主机 | 端口 | 协议 | 说明 |
|------|------|------|------|------|
| **56 nginx** | 14.103.169.56 | 443 | HTTPS | 公网入口 |
| **71 llm-gateway-go** | 172.31.0.3 | 8781 | HTTP | docker 直连 |
| **71 PostgreSQL** | 172.31.0.3 | 5432 | TCP | 新部署的独立 PG |
| **184 llm-gateway-go** | 172.31.0.4 | 10023 | HTTP | k8s NodePort |
| **184 PostgreSQL** | 172.31.0.4 | 5432 | TCP | k8s hostNetwork |

### 7.3 参考文档

- [nginx hash 模块文档](http://nginx.org/en/docs/http/ngx_http_upstream_module.html#hash)
- [一致性 hash 原理](https://en.wikipedia.org/wiki/Consistent_hashing)
- [PostgreSQL 15 官方文档](https://www.postgresql.org/docs/15/)
- [llm-gateway-go 部署文档](./DEPLOYMENT_GUIDE.md)

---

## 8. 总结

本设计方案实现了：

✅ **56 nginx 负载均衡**：基于 `X-Device-Seed` 一致性 hash  
✅ **sticky session**：同一设备固定路由到同一后端  
✅ **数据库隔离**：71 和 184 各自独立 PostgreSQL  
✅ **双活架构**：两个完全独立的站点提供服务  
✅ **故障转移**：后端失败自动切换  
✅ **可回滚**：保留备份配置，5 分钟内可回滚  

**预计停机时间**：0（滚动更新，无需停机）

**实施时间**：
- 数据库隔离：30 分钟
- Nginx 配置：5 分钟
- 验证测试：15 分钟
- **总计**：约 50 分钟

**下一步**：请确认方案后开始实施。

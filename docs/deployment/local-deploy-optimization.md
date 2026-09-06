# 本地部署切换优化方案

## 一、当前方案分析

### 1.1 现状总结

**部署架构**：
- 基于 `scripts/local-host-deploy.sh` 的版本化部署
- 采用 releases 目录 + 原子符号链接切换模式（学习自 245 生产部署）
- 单端口模式（8781），切换时需要 stop → start

**当前切换流程**（20-30秒）：
```bash
# deploy 流程（cmd_deploy）
1. 构建新版本 → releases/<version>/
2. lh_atomic_switch <version>     # 切换 current 符号链接
3. stop 旧实例                     # 停止旧进程（SIGTERM + 等待端口释放）
4. start 新实例                    # 启动新进程 + 等待 healthz
5. 验证 + 标记 verified
```

**性能瓶颈**：
1. **串行停止（30s）**：`stop.sh` 等待旧进程优雅退出，最多等待 30 秒才 SIGKILL
2. **冷启动开销**：新进程从零启动，包括：
   - 数据库连接池初始化
   - Redis 连接建立
   - Schema migration 检查（8+ feature areas）
   - 许可证验证
   - 预热各子系统（路由、凭证、队列等）
3. **串行验证**：healthz 探测循环等待（60s 超时）
4. **无预热**：新版本直到旧版本完全停止后才启动

### 1.2 已有的蓝绿基础设施

项目已经实现了完整的蓝绿部署能力（用于 245/154 生产环境）：

**关键组件**：
- `scripts/deploy-lib/zero-downtime.sh` - 蓝绿生命周期原语
- `scripts/local-host-blue-green.sh` - 本地蓝绿演练脚本
- `scripts/deploy-seamless.sh` - 生产无缝部署（支持 2s 切换）

**生产环境蓝绿流程**（5-8秒切换）：
```bash
1. 在候选端口启动新实例（并行运行）
2. 探测候选 healthz + readyz + version
3. Nginx 原子切流（zd_switch_upstream）
4. 验证新实例正在服务流量
5. 停止旧实例（可选，不影响切换）
```

**核心优势**：
- **并行预热**：旧实例继续服务，新实例独立启动
- **原子切流**：通过 Nginx upstream fragment 一次性切换
- **零停机**：整个过程客户端不感知
- **快速回滚**：恢复旧 upstream 配置即可

## 二、优化方案设计

### 2.1 目标

1. **2 秒内完成切换**（从发起切换到新版本开始接收流量）
2. **零停机**：旧版本在新版本就绪前保持服务
3. **快速回滚**：任何阶段失败都能立即恢复
4. **保持兼容**：不破坏现有部署工具链

### 2.2 架构改进

#### 方案：本地蓝绿部署 + Nginx 反向代理

```
┌─────────────────────────────────────────────────────────┐
│                      客户端                              │
│                  http://localhost:8781                   │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
         ┌─────────────────────────────┐
         │      Nginx (8781)           │
         │  upstream backend {         │
         │    include active-port.conf;│  ← 原子切换点
         │  }                          │
         └─────────────┬───────────────┘
                       │
       ┌───────────────┴────────────────┐
       │                                │
       ▼                                ▼
┌────────────┐                   ┌────────────┐
│ Gateway A  │                   │ Gateway B  │
│  :18781    │ ← 旧版本         │  :18782    │ ← 新版本
│  serving   │                   │  warming   │
└────────────┘                   └────────────┘
       │                                │
       └────────────┬───────────────────┘
                    ▼
            ┌──────────────┐
            │ PG + Redis   │
            └──────────────┘

切换过程（< 2s）：
1. 新版本在 :18782 预热（并行，旧版本继续服务）
2. healthz + readyz 通过
3. 原子替换 active-port.conf: 18781 → 18782
4. nginx -s reload（< 100ms）
5. 旧版本进入 drain 状态（可选延迟停止）
```

### 2.3 实现细节

#### 2.3.1 Nginx 配置

**主配置**（`~/Downloads/llm-gateway-files/nginx/nginx.conf`）：
```nginx
events {
    worker_connections 1024;
}

http {
    include       mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    # 蓝绿 upstream（动态加载活跃端口）
    upstream gateway_backend {
        include /Users/[USER]/Downloads/llm-gateway-files/nginx/active-port.conf;
        keepalive 32;
    }

    server {
        listen 8781;
        server_name localhost;

        # 健康检查
        location /healthz {
            proxy_pass http://gateway_backend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
        }

        # 主服务
        location / {
            proxy_pass http://gateway_backend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            
            # 超时配置
            proxy_connect_timeout 30s;
            proxy_send_timeout 300s;
            proxy_read_timeout 300s;
        }

        # 静态资源（SPA）
        location ~ ^/(assets|static)/ {
            proxy_pass http://gateway_backend;
            proxy_cache_valid 200 1h;
        }
    }
}
```

**活跃端口配置**（`~/Downloads/llm-gateway-files/nginx/active-port.conf`）：
```nginx
server 127.0.0.1:18781 max_fails=3 fail_timeout=10s;
```

#### 2.3.2 部署流程改造

**新增端口管理**：
```bash
# 端口分配策略
PORT_BLUE=18781
PORT_GREEN=18782
NGINX_PORT=8781

# 读取当前活跃端口
active_port=$(cat ~/Downloads/llm-gateway-files/run/active-port 2>/dev/null || echo "$PORT_BLUE")

# 选择候选端口（蓝绿切换）
if [[ "$active_port" == "$PORT_BLUE" ]]; then
  candidate_port=$PORT_GREEN
else
  candidate_port=$PORT_BLUE
fi
```

**优化后的部署流程**：
```bash
cmd_deploy_bluegreen() {
  section "blue-green deploy $VERSION"
  
  # 1. 确定端口
  active_port=$(lh_active_port)           # 18781
  candidate_port=$(lh_candidate_port)     # 18782
  
  # 2. 构建 bundle（不变）
  build_backend "$bundle_dir/gateway"
  build_frontend "$PROJECT_ROOT/web/dist"
  
  # 3. 在候选端口启动新实例（旧实例继续服务）
  export LLM_GATEWAY_LISTEN=":$candidate_port"
  "$bundle_dir/start.sh" &
  
  # 4. 并行探测（最多 60s）
  for i in {1..60}; do
    if curl -fsS "http://127.0.0.1:$candidate_port/healthz" >/dev/null 2>&1 &&
       curl -fsS "http://127.0.0.1:$candidate_port/readyz" >/dev/null 2>&1; then
      ok "候选实例就绪 (${i}s)"
      break
    fi
    sleep 1
  done
  
  # 5. 版本身份校验
  verify_version_identity "$candidate_port" "$VERSION"
  
  # 6. 原子切换 Nginx upstream（关键：< 2s）
  switch_start=$(date +%s%N)
  
  cat > "$NGINX_ROOT/active-port.conf" <<EOF
server 127.0.0.1:$candidate_port max_fails=3 fail_timeout=10s;
EOF
  
  nginx -t && nginx -s reload
  
  switch_end=$(date +%s%N)
  switch_ms=$(( (switch_end - switch_start) / 1000000 ))
  
  ok "Nginx 切换完成 (${switch_ms}ms)"
  
  # 7. 验证切换生效
  nginx_version=$(curl -fsS http://127.0.0.1:8781/version)
  verify_nginx_serving_candidate "$nginx_version" "$VERSION"
  
  # 8. 更新状态指针
  lh_atomic_switch "$VERSION"
  echo "$candidate_port" > "$RUN_DIR/active-port"
  lh_mark_verified "$VERSION"
  
  # 9. Drain 旧实例（异步，不阻塞部署完成）
  (
    sleep 5  # 等待连接自然关闭
    lh_stop_port "$active_port"
  ) &
  
  ok "部署完成 (总时间: ${elapsed}s, 切换: ${switch_ms}ms)"
}
```

#### 2.3.3 关键函数实现

**Nginx 启动管理**：
```bash
# 启动本地 Nginx（如果未运行）
ensure_nginx_running() {
  local nginx_conf="$NGINX_ROOT/nginx.conf"
  local nginx_pid="$NGINX_ROOT/nginx.pid"
  
  if [[ -f "$nginx_pid" ]] && kill -0 "$(cat "$nginx_pid")" 2>/dev/null; then
    return 0
  fi
  
  log "启动 Nginx (8781)..."
  nginx -c "$nginx_conf" -p "$NGINX_ROOT"
  
  for i in {1..10}; do
    if curl -fsS --max-time 1 http://127.0.0.1:8781/healthz >/dev/null 2>&1; then
      ok "Nginx 就绪"
      return 0
    fi
    sleep 1
  done
  
  err "Nginx 启动失败"
  return 1
}
```

**原子切换**：
```bash
# 原子切换 Nginx upstream
atomic_switch_upstream() {
  local new_port=$1
  local nginx_conf="$NGINX_ROOT/active-port.conf"
  local tmp="${nginx_conf}.tmp.$$"
  local backup="${nginx_conf}.backup"
  
  # 备份当前配置
  cp "$nginx_conf" "$backup"
  
  # 写入新配置
  cat > "$tmp" <<EOF
server 127.0.0.1:$new_port max_fails=3 fail_timeout=10s;
EOF
  
  # 原子替换
  mv "$tmp" "$nginx_conf"
  
  # 验证并重载
  if nginx -t 2>/dev/null; then
    nginx -s reload
    rm -f "$backup"
    return 0
  else
    # 回滚
    mv "$backup" "$nginx_conf"
    nginx -s reload
    return 1
  fi
}
```

**快速回滚**：
```bash
cmd_rollback_bluegreen() {
  local target=${1:-}
  [[ -n "$target" ]] || { err "需要指定回滚目标版本"; exit 1; }
  
  section "blue-green rollback → $target"
  
  # 1. 确定目标版本的端口
  local active_port candidate_port
  active_port=$(lh_active_port)
  candidate_port=$(lh_candidate_port)
  
  # 2. 在候选端口启动目标版本
  local bundle_dir
  bundle_dir=$(lh_bundle_dir "$target")
  export LLM_GATEWAY_LISTEN=":$candidate_port"
  "$bundle_dir/start.sh"
  
  # 3. 等待就绪
  wait_ready "$candidate_port" 30
  
  # 4. 原子切换
  atomic_switch_upstream "$candidate_port"
  
  # 5. 更新指针
  lh_atomic_switch "$target"
  echo "$candidate_port" > "$RUN_DIR/active-port"
  
  ok "回滚完成 → $target"
}
```

### 2.4 配置文件布局

```
~/Downloads/llm-gateway-files/
├── bin/
│   ├── current -> v1.2.1234/       # 当前活跃版本
│   ├── v1.2.1234/                  # 版本 bundle
│   │   ├── gateway*
│   │   ├── web/
│   │   ├── env.sh                   # 环境变量（含端口）
│   │   ├── start.sh
│   │   ├── stop.sh
│   │   └── deployment.json
│   └── v1.2.1235/
├── nginx/
│   ├── nginx.conf                   # 主配置
│   ├── active-port.conf             # 当前活跃端口（原子切换点）
│   ├── nginx.pid
│   └── logs/
├── run/
│   ├── active-port                  # 18781 或 18782
│   ├── candidate-port               # 候选端口缓存
│   └── gateway.pid
└── logs/
    ├── gateway.stdout.log
    └── gateway.stderr.log
```

### 2.5 性能提升对比

| 阶段 | 原方案 | 优化方案 | 提升 |
|------|--------|----------|------|
| 停止旧实例 | 0-30s（串行等待） | 0s（异步 drain） | **消除阻塞** |
| 启动新实例 | 10-20s（冷启动） | 10-20s（**并行预热**） | **不阻塞主路径** |
| 探测就绪 | 10-60s | 10-60s（并行） | 无变化 |
| 切换流量 | 切换符号链接 | Nginx reload | **< 100ms** |
| 验证生效 | 5s | 2s | 更快 |
| **总切换时间** | **20-30s** | **< 2s** | **10-15x** |

**关键改进**：
1. **并行化**：旧实例服务期间，新实例完成所有预热
2. **原子化**：Nginx upstream 切换是一次内存操作 + 信号通知
3. **异步化**：旧实例的停止与部署完成解耦

## 三、实施计划

### 3.1 Phase 1：基础设施（1-2 天）

**目标**：搭建 Nginx 反向代理层

**任务**：
1. 创建 `scripts/setup-local-nginx.sh` - 初始化 Nginx 配置
2. 创建 `scripts/local-host-layout-helper.sh` 扩展 - 端口管理函数
3. 创建 Nginx 配置模板
4. 测试 Nginx → Gateway 基本代理

**验收**：
```bash
# 通过 Nginx 访问现有 gateway
curl http://localhost:8781/healthz  # → 200 OK（经过 Nginx）
```

### 3.2 Phase 2：蓝绿逻辑（2-3 天）

**目标**：实现双端口并行运行

**任务**：
1. 修改 `emit_env_block()` - 支持动态端口注入
2. 实现 `lh_active_port()` / `lh_candidate_port()` - 端口选择逻辑
3. 实现 `atomic_switch_upstream()` - Nginx 配置原子切换
4. 实现 `wait_ready()` - 并行探测逻辑

**验收**：
```bash
# 手动在两个端口启动 gateway
LLM_GATEWAY_LISTEN=:18781 ./gateway &
LLM_GATEWAY_LISTEN=:18782 ./gateway &

# 验证双活
curl http://localhost:18781/healthz  # → 200
curl http://localhost:18782/healthz  # → 200

# 验证 Nginx 切换
echo "server 127.0.0.1:18781;" > active-port.conf && nginx -s reload
curl http://localhost:8781/version  # → 返回 18781 版本

echo "server 127.0.0.1:18782;" > active-port.conf && nginx -s reload
curl http://localhost:8781/version  # → 返回 18782 版本
```

### 3.3 Phase 3：集成部署（2-3 天）

**目标**：重构 `local-host-deploy.sh`

**任务**：
1. 新增 `cmd_deploy_bluegreen()` - 蓝绿部署流程
2. 新增 `cmd_rollback_bluegreen()` - 蓝绿回滚
3. 添加 `--mode` 参数：`legacy`（原方案）/ `bluegreen`（新方案）
4. 保持向后兼容

**验收**：
```bash
# 蓝绿部署
bash scripts/local-host-deploy.sh deploy --mode bluegreen
# 预期：< 2s 切换，旧实例继续运行 5s

# 快速回滚
bash scripts/local-host-deploy.sh rollback v1.2.1234 --mode bluegreen
# 预期：< 2s 切换回旧版本
```

### 3.4 Phase 4：测试与文档（1-2 天）

**任务**：
1. 扩展 `scripts/local-host-deploy-test.sh` - 添加蓝绿测试用例
2. 压力测试：部署期间并发请求无中断
3. 更新 README 和运维文档
4. 编写故障恢复手册

**测试场景**：
- ✅ 正常部署：2s 内完成切换，零停机
- ✅ 候选失败：旧实例继续服务，无影响
- ✅ Nginx 失败：回滚配置，保持旧实例
- ✅ 并发部署：锁保护，串行执行
- ✅ 快速回滚：< 2s 恢复旧版本

## 四、风险与缓解

### 4.1 风险评估

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| Nginx 配置错误 | 服务不可用 | 低 | `nginx -t` 验证 + 自动回滚 |
| 端口冲突 | 候选启动失败 | 中 | 端口检测 + 清理僵尸进程 |
| 内存不足（双实例） | OOM Killed | 中 | 监控内存 + 限制并发部署 |
| 旧实例无法停止 | 端口泄漏 | 低 | SIGKILL 升级 + 端口复用检测 |
| 数据库连接耗尽 | 连接池竞争 | 低 | 调整连接池大小 + 限流 |

### 4.2 回滚策略

**自动回滚触发条件**：
1. 候选实例 healthz 失败（60s 超时）
2. 候选实例 readyz 失败（数据库不可用）
3. 版本身份不匹配
4. Nginx 切换后验证失败

**回滚操作**：
```bash
# 恢复旧 upstream
cp active-port.conf.backup active-port.conf
nginx -s reload

# 停止候选实例
kill $(cat run/candidate-${candidate_port}.pid)

# 旧实例保持不动（从未停止）
```

### 4.3 降级方案

如果蓝绿方案出现问题，保留原 `legacy` 模式：
```bash
# 使用原串行部署
bash scripts/local-host-deploy.sh deploy --mode legacy

# 自动检测并降级
if ! ensure_nginx_running; then
  warn "Nginx 不可用，降级到 legacy 模式"
  MODE=legacy
fi
```

## 五、监控与可观测性

### 5.1 关键指标

**部署指标**：
- `deploy_switch_duration_ms` - 切换耗时（目标 < 2000ms）
- `deploy_total_duration_s` - 总部署时间
- `deploy_candidate_warmup_s` - 候选预热时间

**运行时指标**：
- `nginx_active_connections` - Nginx 活跃连接
- `gateway_active_port` - 当前活跃端口（18781/18782）
- `gateway_instances_count` - 运行中的实例数

### 5.2 日志记录

```bash
# 部署日志（结构化）
{
  "timestamp": "2026-09-01T20:30:00Z",
  "action": "deploy",
  "version": "v1.2.1235",
  "old_port": 18781,
  "new_port": 18782,
  "switch_duration_ms": 87,
  "total_duration_s": 45,
  "status": "success"
}
```

### 5.3 告警规则

- 🚨 `deploy_switch_duration_ms > 2000` - 切换超时
- ⚠️ `gateway_instances_count > 2` - 实例泄漏
- 🚨 `nginx_active_connections = 0 AND gateway_instances_count > 0` - Nginx 故障

## 六、长期优化方向

### 6.1 容器化部署

迁移到 Docker Compose，进一步简化：
```yaml
services:
  nginx:
    image: nginx:alpine
    ports: ["8781:80"]
    volumes:
      - ./nginx/active-port.conf:/etc/nginx/conf.d/upstream.conf
  
  gateway-blue:
    build: .
    environment:
      LLM_GATEWAY_LISTEN: ":18781"
  
  gateway-green:
    build: .
    environment:
      LLM_GATEWAY_LISTEN: ":18782"
```

### 6.2 健康检查增强

- 引入 `/readyz` 严格检查（DB + Redis + License）
- 引入 `/livez` 轻量检查（进程存活）
- 配置 Nginx `health_check` 模块自动摘除异常后端

### 6.3 金丝雀发布

在蓝绿基础上实现流量分配：
```nginx
upstream gateway_backend {
    server 127.0.0.1:18781 weight=9;  # 90% 流量
    server 127.0.0.1:18782 weight=1;  # 10% 流量（金丝雀）
}
```

## 七、总结

### 7.1 预期收益

- ✅ **切换时间**：从 20-30s 降低到 **< 2s**（**10-15x 提升**）
- ✅ **零停机**：客户端无感知切换
- ✅ **快速回滚**：任何阶段都能秒级恢复
- ✅ **资源效率**：并行预热，不浪费等待时间
- ✅ **风险可控**：多重验证 + 自动回滚

### 7.2 成本

- **开发成本**：约 6-10 人天
- **运行成本**：双实例短暂并行（~30s），内存增加 < 2GB
- **维护成本**：引入 Nginx 依赖，需要额外运维

### 7.3 推荐

**强烈推荐实施**，理由：
1. 方案成熟（生产环境 245/154 已验证）
2. 投入产出比高（10x+ 性能提升）
3. 风险可控（完善的回滚机制）
4. 可扩展（为未来容器化铺路）

---

**作者**：ZCode AI Assistant  
**日期**：2026-09-01  
**版本**：v1.0

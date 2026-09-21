# 本地蓝绿部署快速入门

## 概述

本地蓝绿部署方案将部署切换时间从 **20-30 秒降低到 < 2 秒**，实现零停机更新。

## 核心原理

```
传统部署（20-30s）:          蓝绿部署（< 2s）:
┌─────────────┐              ┌─────────────┐
│ 停止旧版本  │              │ 旧版本继续  │
│   (30s)     │              │   服务中    │
└──────┬──────┘              └──────┬──────┘
       │                             │
       ▼                             ▼
┌─────────────┐              ┌─────────────┐
│ 启动新版本  │              │ 并行启动    │
│   (20s)     │              │ 新版本      │
└─────────────┘              └──────┬──────┘
                                    │
                                    ▼
                             ┌─────────────┐
                             │ Nginx 切换  │
                             │  (< 100ms)  │
                             └─────────────┘
```

## 前置条件

1. **Nginx** 已安装
   ```bash
   # macOS
   brew install nginx
   
   # Ubuntu
   sudo apt-get install nginx
   ```

2. **PostgreSQL + Redis** 已启动
   ```bash
   docker ps | grep -E 'llm-gateway-pg|redis'
   ```

3. **环境变量** 已配置（通过 env-injector 或 .env）

## 第一次使用

### Step 1: 初始化 Nginx 代理层

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 初始化 Nginx 配置
bash scripts/setup-local-nginx.sh install
```

输出示例：
```
[nginx-setup] 创建配置目录: /Users/xutaohuang/Downloads/llm-gateway-files/nginx
  ✓ 目录结构就绪
[nginx-setup] 生成主配置: nginx.conf
  ✓ 主配置已生成
[nginx-setup] 生成初始 upstream 配置: active-port.conf
  ✓ upstream 配置已生成 (默认: 18781)
[nginx-setup] 验证 Nginx 配置...
  ✓ 配置验证通过
[nginx-setup] 启动 Nginx...
  ✓ Nginx 已启动 (PID: 12345)

  ✓ 安装完成!

下一步:
  1. 启动 gateway: bash scripts/local-host-deploy-bluegreen.sh deploy
  2. 访问代理层: curl http://localhost:8781/healthz
  3. 查看状态: bash scripts/setup-local-nginx.sh --check
```

### Step 2: 首次蓝绿部署

```bash
# 部署当前代码到蓝绿环境
bash scripts/local-host-deploy-bluegreen.sh deploy
```

输出示例：
```
━━━ blue-green deploy v1.2.1001 (seq=1001) ━━━

[bg-deploy] Active port: 18781, Candidate port: 18782

━━━ [1/8] Build backend + frontend ━━━
[bg-deploy] go build -o bundle/gateway ./cmd/gateway
  ✓ Build complete

━━━ [2/8] Stage bundle ━━━
  ✓ Bundle staged and verified

━━━ [3/8] Start candidate on port 18782 ━━━
[start] v1.2.1001 launched on :18782 (PID 23456)
[start] healthz OK after 12s
  ⏱ Candidate warmup: 12s

━━━ [4/8] Probe candidate health ━━━
  ✓ Candidate healthz + readyz OK

━━━ [5/8] Verify version identity ━━━
  ✓ Version identity verified: v1.2.1001

━━━ [6/8] Atomic Nginx switch ━━━
  ✓ Nginx switched to port 18782
  ⏱ Switch duration: 87ms
  ✓ Switch SLO met: 87ms < 2000ms ✓

━━━ [7/8] Verify Nginx serving candidate ━━━
  ✓ Nginx is serving candidate

━━━ [8/8] Finalize deployment ━━━
  ✓ Deployment state updated
[bg-deploy] Draining old instance on port 18781 (async, 5s grace period)...

  ✓ ✅ Deployment complete!

  Version:        v1.2.1001
  Active port:    18782
  Total time:     18s
  Switch time:    87ms
  Warmup time:    12s

Access: curl http://localhost:8781/healthz
Rollback: bash scripts/local-host-deploy-bluegreen.sh rollback <version>
```

## 日常使用

### 部署新版本

```bash
# 1. 修改代码
vim pkg/domains/routing/router.go

# 2. 提交并 bump 版本（可选）
git commit -am "feat: improve routing"
bash scripts/bump-version.sh  # 自动递增 build_seq

# 3. 蓝绿部署
bash scripts/local-host-deploy-bluegreen.sh deploy
```

**切换过程**（用户无感知）：
- 旧版本在 18782 继续服务
- 新版本在 18781 预热（10-20s）
- Nginx 原子切换到 18781（< 100ms）
- 旧版本 5 秒后优雅停止

### 查看状态

```bash
bash scripts/local-host-deploy-bluegreen.sh status
```

输出：
```
━━━ Blue-Green Deployment Status ━━━

Active version:     v1.2.1001
Active port:        18782
Candidate port:     18781
Nginx proxy port:   8781

  ✓ Port 18782: ✓ healthy
  Port 18781: idle

  ✓ Nginx: running
  ✓ Nginx proxy: healthy
```

### 快速回滚

```bash
# 列出可回滚的版本
bash scripts/local-host-deploy.sh list

# 回滚到指定版本
bash scripts/local-host-deploy-bluegreen.sh rollback v1.2.1000
```

回滚流程（< 5 秒）：
1. 在空闲端口启动旧版本
2. 探测健康状态
3. Nginx 切换（< 100ms）
4. 停止故障版本

### 访问服务

所有请求通过 Nginx 代理（端口 8781）：

```bash
# 健康检查
curl http://localhost:8781/healthz

# 版本信息
curl http://localhost:8781/version

# OpenAI API
curl http://localhost:8781/v1/models \
  -H "Authorization: Bearer $LLM_GATEWAY_API_KEY"

# 管理后台
open http://localhost:8781/
```

## 故障排查

### 问题 1: Nginx 未启动

**症状**：
```
curl: (7) Failed to connect to localhost port 8781
```

**解决**：
```bash
# 检查状态
bash scripts/setup-local-nginx.sh --check

# 重启 Nginx
bash scripts/setup-local-nginx.sh --restart
```

### 问题 2: 端口被占用

**症状**：
```
[start] ERROR: port 18781 already in use
```

**解决**：
```bash
# 查看占用进程
lsof -i :18781

# 停止僵尸进程
kill $(lsof -t -i :18781)

# 或使用辅助脚本清理
bash scripts/local-host-deploy-bluegreen.sh status
```

### 问题 3: 候选实例启动失败

**症状**：
```
[start] WARNING: healthz did not respond in 60s
```

**解决**：
```bash
# 查看日志
tail -100 ~/Downloads/llm-gateway-files/logs/gateway-18781.stdout.log
tail -100 ~/Downloads/llm-gateway-files/logs/gateway-18781.stderr.log

# 常见原因：
# 1. 数据库未启动
docker start llm-gateway-pg

# 2. Redis 未启动
docker start llm-gateway-redis

# 3. 端口冲突（见问题 2）
```

### 问题 4: Nginx 切换失败

**症状**：
```
  ✗ Nginx switch failed
```

**解决**：
```bash
# 验证 Nginx 配置
nginx -t -c ~/Downloads/llm-gateway-files/nginx/nginx.conf

# 检查 upstream 配置
cat ~/Downloads/llm-gateway-files/nginx/active-port.conf

# 手动切换到已知端口
bash scripts/local-host-deploy-bluegreen.sh switch 18781
```

## 高级用法

### 手动端口切换

```bash
# 在两个端口都运行时，手动切换流量
bash scripts/local-host-deploy-bluegreen.sh switch 18782
```

### 保留更多版本

```bash
# 修改 KEEP_VERIFIED 变量
export KEEP_VERIFIED=5
bash scripts/local-host-deploy-bluegreen.sh deploy
```

### 调试模式

```bash
# 启用详细日志
export LOG_LEVEL=debug
bash scripts/local-host-deploy-bluegreen.sh deploy
```

### 跳过前端构建

```bash
# 只更新后端（快速迭代）
# 修改脚本中的 build_frontend 函数
```

## 性能指标

| 指标 | 目标 | 典型值 |
|------|------|--------|
| 切换时间 | < 2s | 87-150ms |
| 候选预热 | - | 10-20s |
| 总部署时间 | - | 15-30s |
| 停机时间 | 0s | 0s ✓ |

## 与传统部署对比

| 特性 | 传统部署 | 蓝绿部署 |
|------|----------|----------|
| 切换时间 | 20-30s | < 2s |
| 停机时间 | 20-30s | 0s |
| 回滚速度 | 20-30s | < 5s |
| 风险 | 高（停机期无服务） | 低（旧版本兜底） |
| 并行运行 | 否 | 是（短暂） |

## 架构图

```
                    客户端请求
                        │
                        ▼
              ┌────────────────┐
              │  Nginx :8781   │  ← 统一入口
              │  (反向代理)    │
              └────────┬───────┘
                       │
       ┌───────────────┴────────────────┐
       │   include active-port.conf;    │  ← 原子切换点
       └───────────────┬────────────────┘
                       │
       ┌───────────────┴───────────────┐
       │                               │
       ▼                               ▼
┌────────────┐                  ┌────────────┐
│ Gateway A  │                  │ Gateway B  │
│  :18781    │ ← Blue           │  :18782    │ ← Green
│            │                  │            │
└──────┬─────┘                  └──────┬─────┘
       │                               │
       └───────────────┬───────────────┘
                       ▼
            ┌────────────────────┐
            │  PostgreSQL:5432   │
            │  Redis:6379        │
            └────────────────────┘
```

## 下一步

- ✅ 熟悉蓝绿部署流程
- ✅ 练习部署和回滚
- 📝 阅读完整文档：[LOCAL_DEPLOY_OPTIMIZATION.md](./local-deploy-optimization.md)
- 🔧 根据需要调整端口配置
- 🚀 集成到 CI/CD 流水线

## 相关文档

- [LOCAL_DEPLOY_OPTIMIZATION.md](./local-deploy-optimization.md) - 完整优化方案
- [scripts/setup-local-nginx.sh](./scripts/setup-local-nginx.sh) - Nginx 初始化
- [scripts/local-host-deploy-bluegreen.sh](./scripts/local-host-deploy-bluegreen.sh) - 蓝绿部署
- [scripts/local-host-layout-helper.sh](./scripts/local-host-layout-helper.sh) - 辅助函数

## 支持

遇到问题？
1. 查看 [故障排查](#故障排查) 章节
2. 运行 `bash scripts/local-host-deploy-bluegreen.sh status` 诊断
3. 查看日志：`~/Downloads/llm-gateway-files/logs/`
